package reconciliation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type transferRow struct {
	ID          uuid.UUID
	Reference   string
	Amount      int64
	Currency    string
	Status      string
	HasReversal bool
}

type Reconciler struct {
	pool *pgxpool.Pool
}

func NewReconciler(pool *pgxpool.Pool) *Reconciler {
	return &Reconciler{pool: pool}
}

func (r *Reconciler) ClaimRun(ctx context.Context) (*ReconciliationRun, error) {
	var run ReconciliationRun
	err := r.pool.QueryRow(ctx, `
		UPDATE reconciliation_runs
		SET status = 'running'
		WHERE id = (
			SELECT id FROM reconciliation_runs
			WHERE status = 'running'
			ORDER BY started_at
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id, file_id, status, started_at, finished_at, matched_count, discrepancy_count, error
	`).Scan(
		&run.ID, &run.FileID, &run.Status, &run.StartedAt,
		&run.FinishedAt, &run.MatchedCount, &run.DiscrepancyCount, &run.Error,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}

func (r *Reconciler) Execute(ctx context.Context, run ReconciliationRun) error {
	var settlementDate time.Time
	err := r.pool.QueryRow(ctx, `
		SELECT settlement_date FROM settlement_files WHERE id = $1
	`, run.FileID).Scan(&settlementDate)
	if err != nil {
		return fmt.Errorf("lookup settlement date: %w", err)
	}

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.RepeatableRead,
	})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	settlementRecords, err := loadSettlementRecords(ctx, tx, run.FileID)
	if err != nil {
		return fmt.Errorf("load settlement records: %w", err)
	}

	transfers, err := loadTransfersInScope(ctx, tx, settlementDate)
	if err != nil {
		return fmt.Errorf("load transfers: %w", err)
	}

	discrepancies, matchedCount := compare(settlementRecords, transfers)

	batchSize := 500
	for start := 0; start < len(discrepancies); start += batchSize {
		end := start + batchSize
		if end > len(discrepancies) {
			end = len(discrepancies)
		}
		batch := discrepancies[start:end]

		rows := make([][]any, 0, len(batch))
		for _, d := range batch {
			rows = append(rows, []any{
				run.ID, string(d.Kind), d.TransferID, d.SettlementRecordID,
				d.ExpectedAmount, d.ActualAmount, d.Resolution, d.Note,
			})
		}

		_, err = tx.CopyFrom(ctx,
			pgx.Identifier{"reconciliation_discrepancies"},
			[]string{"run_id", "kind", "transfer_id", "settlement_record_id",
				"expected_amount", "actual_amount", "resolution", "note"},
			pgx.CopyFromRows(rows),
		)
		if err != nil {
			return fmt.Errorf("insert discrepancies: %w", err)
		}
	}

	now := time.Now().UTC()
	_, err = tx.Exec(ctx, `
		UPDATE reconciliation_runs
		SET status = 'completed', finished_at = $2, matched_count = $3, discrepancy_count = $4
		WHERE id = $1
	`, run.ID, now, matchedCount, len(discrepancies))
	if err != nil {
		return fmt.Errorf("update run: %w", err)
	}

	return tx.Commit(ctx)
}

func (r *Reconciler) MarkFailed(ctx context.Context, runID uuid.UUID, errMsg string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE reconciliation_runs
		SET status = 'failed', finished_at = now(), error = $2
		WHERE id = $1
	`, runID, errMsg)
	return err
}

func loadSettlementRecords(ctx context.Context, tx pgx.Tx, fileID uuid.UUID) ([]SettlementRecord, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, file_id, provider_ref, our_reference, amount, currency, status, occurred_at
		FROM settlement_records
		WHERE file_id = $1
		ORDER BY id
	`, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []SettlementRecord
	for rows.Next() {
		var rec SettlementRecord
		if err := rows.Scan(&rec.ID, &rec.FileID, &rec.ProviderRef, &rec.OurReference,
			&rec.Amount, &rec.Currency, &rec.Status, &rec.OccurredAt); err != nil {
			return nil, err
		}
		records = append(records, rec)
	}
	return records, rows.Err()
}

func loadTransfersInScope(ctx context.Context, tx pgx.Tx, settlementDate time.Time) ([]transferRow, error) {
	dayStart := settlementDate.UTC().Truncate(24 * time.Hour)
	dayEnd := dayStart.Add(24 * time.Hour)

	rows, err := tx.Query(ctx, `
		SELECT t.id, t.reference, t.amount, t.currency, t.status,
		       EXISTS(SELECT 1 FROM transfers r WHERE r.reverses_transfer_id = t.id) AS has_reversal
		FROM transfers t
		WHERE t.reference IS NOT NULL
		  AND t.type != 'reversal'
		  AND t.created_at >= $1
		  AND t.created_at < $2
		ORDER BY t.created_at, t.id
	`, dayStart, dayEnd)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var transfers []transferRow
	for rows.Next() {
		var t transferRow
		if err := rows.Scan(&t.ID, &t.Reference, &t.Amount, &t.Currency, &t.Status, &t.HasReversal); err != nil {
			return nil, err
		}
		transfers = append(transfers, t)
	}
	return transfers, rows.Err()
}

func compare(settlements []SettlementRecord, transfers []transferRow) ([]Discrepancy, int) {
	var discrepancies []Discrepancy
	matchedCount := 0

	settlementByRef := make(map[string][]SettlementRecord)
	var nullRefSettlements []SettlementRecord
	for _, s := range settlements {
		if s.OurReference == nil {
			nullRefSettlements = append(nullRefSettlements, s)
			continue
		}
		settlementByRef[*s.OurReference] = append(settlementByRef[*s.OurReference], s)
	}

	transferByRef := make(map[string][]transferRow)
	for _, t := range transfers {
		transferByRef[t.Reference] = append(transferByRef[t.Reference], t)
	}

	for ref, recs := range settlementByRef {
		if len(recs) > 1 {
			firstID := recs[0].ID
			discrepancies = append(discrepancies, Discrepancy{
				Kind:               DiscrepancyDuplicateInSettlement,
				SettlementRecordID: &firstID,
				Note:               strPtr(fmt.Sprintf("duplicate our_reference: %s (count: %d)", ref, len(recs))),
				Resolution:         "open",
			})
		}

		transfersForRef, hasTransfer := transferByRef[ref]
		if !hasTransfer {
			for _, rec := range recs {
				recID := rec.ID
				discrepancies = append(discrepancies, Discrepancy{
					Kind:               DiscrepancyMissingInLedger,
					SettlementRecordID: &recID,
					Note:               strPtr("settlement record has no matching transfer"),
					Resolution:         "open",
				})
			}
			continue
		}

		matchedCount++

		var totalSettlementAmount int64
		for _, rec := range recs {
			totalSettlementAmount += rec.Amount
		}

		var totalTransferAmount int64
		for _, t := range transfersForRef {
			totalTransferAmount += t.Amount
		}

		if totalTransferAmount != totalSettlementAmount {
			expected := totalTransferAmount
			actual := totalSettlementAmount
			tID := transfersForRef[0].ID
			recID := recs[0].ID
			discrepancies = append(discrepancies, Discrepancy{
				Kind:               DiscrepancyAmountMismatch,
				TransferID:         &tID,
				SettlementRecordID: &recID,
				ExpectedAmount:     &expected,
				ActualAmount:       &actual,
				Note:               strPtr(fmt.Sprintf("expected %d, got %d", expected, actual)),
				Resolution:         "open",
			})
		}

		transferHasReversal := false
		for _, t := range transfersForRef {
			if t.HasReversal {
				transferHasReversal = true
				break
			}
		}

		settlementReversed := false
		for _, rec := range recs {
			if rec.Status == "reversed" {
				settlementReversed = true
				break
			}
		}

		if transferHasReversal != settlementReversed {
			tID := transfersForRef[0].ID
			recID := recs[0].ID
			discrepancies = append(discrepancies, Discrepancy{
				Kind:               DiscrepancyStatusMismatch,
				TransferID:         &tID,
				SettlementRecordID: &recID,
				Note:               strPtr(fmt.Sprintf("transfer has_reversal=%v, settlement has_reversed=%v", transferHasReversal, settlementReversed)),
				Resolution:         "open",
			})
		}
	}

	for _, rec := range nullRefSettlements {
		recID := rec.ID
		discrepancies = append(discrepancies, Discrepancy{
			Kind:               DiscrepancyMissingInLedger,
			SettlementRecordID: &recID,
			Note:               strPtr("settlement record has null our_reference"),
			Resolution:         "open",
		})
	}

	matchedRefs := make(map[string]bool)
	for ref := range settlementByRef {
		if _, hasTransfer := transferByRef[ref]; hasTransfer {
			matchedRefs[ref] = true
		}
	}

	for ref, transfersForRef := range transferByRef {
		if !matchedRefs[ref] {
			for _, t := range transfersForRef {
				tID := t.ID
				discrepancies = append(discrepancies, Discrepancy{
					Kind:       DiscrepancyMissingInSettlement,
					TransferID: &tID,
					Note:       strPtr(fmt.Sprintf("transfer %s has no matching settlement record", ref)),
					Resolution: "open",
				})
			}
		}
	}

	return discrepancies, matchedCount
}

func strPtr(s string) *string {
	return &s
}
