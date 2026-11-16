package reconciliation

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// resolutionOpen is the initial resolution of every discrepancy a run records.
const resolutionOpen = "open"

// Settlement record statuses accepted by the importer.
const (
	settlementStatusFailed   = "failed"
	settlementStatusReversed = "reversed"
)

var (
	// ErrRunBusy means another worker holds the run; the caller should move on.
	ErrRunBusy = errors.New("reconciliation run is being executed by another worker")
	// ErrRunNotRunning means the run already finished (completed or failed).
	ErrRunNotRunning = errors.New("reconciliation run is not running")
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

// ClaimRun picks the running run that is due soonest and that no worker is
// executing right now. It counts the attempt and pushes next_attempt_at out by
// an exponential backoff in the same committed statement, so a worker that dies
// mid-run still uses up an attempt and the run waits before it is retried
// instead of blocking the queue. Runs past retry.MaxAttempts are marked failed.
// The claim holds no lock: Execute takes the real lock, so a run picked by two
// workers at the same moment is still executed only once.
func (r *Reconciler) ClaimRun(ctx context.Context, retry RetryPolicy) (*ReconciliationRun, error) {
	for {
		run, err := scanRun(r.pool.QueryRow(ctx, `
			UPDATE reconciliation_runs
			   SET attempts = attempts + 1,
			       next_attempt_at = now() + make_interval(secs => least($1::float8 * power(2, attempts), $2::float8))
			 WHERE id = (
				SELECT id FROM reconciliation_runs
				 WHERE status = 'running' AND next_attempt_at <= now()
				 ORDER BY next_attempt_at
				 LIMIT 1
				 FOR UPDATE SKIP LOCKED)
			RETURNING `+runColumns,
			retry.Base.Seconds(), retry.Max.Seconds()))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if run.Attempts <= retry.MaxAttempts {
			return &run, nil
		}
		msg := fmt.Sprintf("gave up after %d attempts", retry.MaxAttempts)
		if err := r.MarkFailed(ctx, run.ID, msg); err != nil {
			return nil, fmt.Errorf("fail exhausted run %s: %w", run.ID, err)
		}
	}
}

// Execute reconciles one run in a single REPEATABLE READ transaction. It first
// locks the run row and holds that lock until commit, so concurrent workers get
// ErrRunBusy (or ErrRunNotRunning once it has finished) instead of recording the
// same discrepancies twice. If the worker dies the lock is released with the
// connection and the still-running run can be picked up again.
func (r *Reconciler) Execute(ctx context.Context, run ReconciliationRun) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.RepeatableRead,
	})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if err := lockRunForExecution(ctx, tx, run.ID); err != nil {
		return err
	}

	var settlementDate time.Time
	err = tx.QueryRow(ctx, `
		SELECT settlement_date FROM settlement_files WHERE id = $1
	`, run.FileID).Scan(&settlementDate)
	if err != nil {
		return fmt.Errorf("lookup settlement date: %w", err)
	}

	settlementRecords, err := loadSettlementRecords(ctx, tx, run.FileID)
	if err != nil {
		return fmt.Errorf("load settlement records: %w", err)
	}

	transfers, err := loadTransfersInScope(ctx, tx, settlementDate)
	if err != nil {
		return fmt.Errorf("load transfers: %w", err)
	}

	discrepancies, matchedCount := compare(settlementRecords, transfers)

	_, err = tx.CopyFrom(ctx,
		pgx.Identifier{"reconciliation_discrepancies"},
		[]string{"run_id", "kind", "transfer_id", "settlement_record_id",
			"expected_amount", "actual_amount", "resolution", "note"},
		pgx.CopyFromSlice(len(discrepancies), func(i int) ([]any, error) {
			d := discrepancies[i]
			return []any{
				run.ID, string(d.Kind), d.TransferID, d.SettlementRecordID,
				d.ExpectedAmount, d.ActualAmount, d.Resolution, d.Note,
			}, nil
		}),
	)
	if err != nil {
		return fmt.Errorf("insert discrepancies: %w", err)
	}

	tag, err := tx.Exec(ctx, `
		UPDATE reconciliation_runs
		SET status = 'completed', finished_at = $2, matched_count = $3, discrepancy_count = $4
		WHERE id = $1 AND status = 'running'
	`, run.ID, time.Now().UTC(), matchedCount, len(discrepancies))
	if err != nil {
		return fmt.Errorf("update run: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrRunNotRunning
	}

	return tx.Commit(ctx)
}

// lockRunForExecution takes the run row lock as the transaction's first
// statement. A run locked by another worker is skipped (ErrRunBusy); a run that
// already finished, or that changed after this snapshot, is not executed again.
func lockRunForExecution(ctx context.Context, tx pgx.Tx, runID uuid.UUID) error {
	var status RunStatus
	err := tx.QueryRow(ctx, `
		SELECT status FROM reconciliation_runs
		 WHERE id = $1
		 FOR UPDATE SKIP LOCKED
	`, runID).Scan(&status)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Locked by another worker (or deleted, which the schema never does).
		return ErrRunBusy
	case isSerializationFailure(err):
		// Another worker finished the run after our snapshot was taken.
		return ErrRunNotRunning
	case err != nil:
		return fmt.Errorf("lock run: %w", err)
	case status != RunStatusRunning:
		return ErrRunNotRunning
	}
	return nil
}

func isSerializationFailure(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "40001"
}

// MarkFailed records a failed run. It only touches a run that is still running,
// so a late failure from one worker cannot overwrite another worker's result.
func (r *Reconciler) MarkFailed(ctx context.Context, runID uuid.UUID, errMsg string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE reconciliation_runs
		SET status = 'failed', finished_at = now(), error = $2
		WHERE id = $1 AND status = 'running'
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
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (SettlementRecord, error) {
		var rec SettlementRecord
		err := row.Scan(&rec.ID, &rec.FileID, &rec.ProviderRef, &rec.OurReference,
			&rec.Amount, &rec.Currency, &rec.Status, &rec.OccurredAt)
		return rec, err
	})
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
		  AND t.status = 'posted'
		  AND t.created_at >= $1
		  AND t.created_at < $2
		ORDER BY t.created_at, t.id
	`, dayStart, dayEnd)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (transferRow, error) {
		var t transferRow
		err := row.Scan(&t.ID, &t.Reference, &t.Amount, &t.Currency, &t.Status, &t.HasReversal)
		return t, err
	})
}

// compare matches settlement records to ledger transfers by reference. Map keys
// are walked in sorted order so the same inputs always yield the same
// discrepancies in the same order.
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

	for _, ref := range slices.Sorted(maps.Keys(settlementByRef)) {
		recs := settlementByRef[ref]
		if len(recs) > 1 {
			firstID := recs[0].ID
			discrepancies = append(discrepancies, Discrepancy{
				Kind:               DiscrepancyDuplicateInSettlement,
				SettlementRecordID: &firstID,
				Note:               strPtr(fmt.Sprintf("duplicate our_reference: %s (count: %d)", ref, len(recs))),
				Resolution:         resolutionOpen,
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
					Resolution:         resolutionOpen,
				})
			}
			continue
		}

		tID := transfersForRef[0].ID
		recID := recs[0].ID

		// A record the provider reports as failed did not move money, so it
		// neither pays for the transfer nor counts toward the settled amount.
		var paid []SettlementRecord
		for _, rec := range recs {
			if rec.Status != settlementStatusFailed {
				paid = append(paid, rec)
			}
		}
		if len(paid) == 0 {
			discrepancies = append(discrepancies, Discrepancy{
				Kind:               DiscrepancyStatusMismatch,
				TransferID:         &tID,
				SettlementRecordID: &recID,
				Note:               strPtr("settlement status=failed but ledger transfer is posted"),
				Resolution:         resolutionOpen,
			})
			continue
		}

		matchedCount++

		if len(transfersForRef) > 1 {
			discrepancies = append(discrepancies, Discrepancy{
				Kind:       DiscrepancyDuplicateInLedger,
				TransferID: &tID,
				Note:       strPtr(fmt.Sprintf("duplicate reference in ledger: %s (count: %d)", ref, len(transfersForRef))),
				Resolution: resolutionOpen,
			})
		}

		if ledgerCur, settledCur, ok := sameCurrency(transfersForRef, paid); !ok {
			discrepancies = append(discrepancies, Discrepancy{
				Kind:               DiscrepancyCurrencyMismatch,
				TransferID:         &tID,
				SettlementRecordID: &recID,
				Note:               strPtr(fmt.Sprintf("currency mismatch: ledger %s, settlement %s", ledgerCur, settledCur)),
				Resolution:         resolutionOpen,
			})
		} else {
			var totalSettlementAmount int64
			for _, rec := range paid {
				totalSettlementAmount += rec.Amount
			}
			var totalTransferAmount int64
			for _, t := range transfersForRef {
				totalTransferAmount += t.Amount
			}
			if totalTransferAmount != totalSettlementAmount {
				expected := totalTransferAmount
				actual := totalSettlementAmount
				discrepancies = append(discrepancies, Discrepancy{
					Kind:               DiscrepancyAmountMismatch,
					TransferID:         &tID,
					SettlementRecordID: &recID,
					ExpectedAmount:     &expected,
					ActualAmount:       &actual,
					Note:               strPtr(fmt.Sprintf("expected %d, got %d", expected, actual)),
					Resolution:         resolutionOpen,
				})
			}
		}

		transferHasReversal := slices.ContainsFunc(transfersForRef, func(t transferRow) bool { return t.HasReversal })
		settlementReversed := slices.ContainsFunc(paid, func(rec SettlementRecord) bool { return rec.Status == settlementStatusReversed })
		if transferHasReversal != settlementReversed {
			discrepancies = append(discrepancies, Discrepancy{
				Kind:               DiscrepancyStatusMismatch,
				TransferID:         &tID,
				SettlementRecordID: &recID,
				Note:               strPtr(fmt.Sprintf("transfer has_reversal=%v, settlement has_reversed=%v", transferHasReversal, settlementReversed)),
				Resolution:         resolutionOpen,
			})
		}
	}

	for _, rec := range nullRefSettlements {
		recID := rec.ID
		discrepancies = append(discrepancies, Discrepancy{
			Kind:               DiscrepancyMissingInLedger,
			SettlementRecordID: &recID,
			Note:               strPtr("settlement record has null our_reference"),
			Resolution:         resolutionOpen,
		})
	}

	for _, ref := range slices.Sorted(maps.Keys(transferByRef)) {
		if _, settled := settlementByRef[ref]; settled {
			continue
		}
		for _, t := range transferByRef[ref] {
			tID := t.ID
			discrepancies = append(discrepancies, Discrepancy{
				Kind:       DiscrepancyMissingInSettlement,
				TransferID: &tID,
				Note:       strPtr(fmt.Sprintf("transfer %s has no matching settlement record", ref)),
				Resolution: resolutionOpen,
			})
		}
	}

	return discrepancies, matchedCount
}

// sameCurrency reports whether every transfer and settlement record for one
// reference uses the same currency, returning the first of each for the note.
func sameCurrency(transfers []transferRow, recs []SettlementRecord) (ledger, settled string, ok bool) {
	ledger, settled = transfers[0].Currency, recs[0].Currency
	for _, t := range transfers {
		if t.Currency != ledger {
			return ledger, t.Currency, false
		}
	}
	for _, rec := range recs {
		if rec.Currency != ledger {
			return ledger, rec.Currency, false
		}
	}
	return ledger, settled, true
}

func strPtr(s string) *string {
	return &s
}
