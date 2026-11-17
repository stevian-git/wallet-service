package reconciliation

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
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

	settlements, transfers, err := openScopeCursors(ctx, tx, run.FileID, settlementDate)
	if err != nil {
		return err
	}
	sink := &discrepancySink{tx: tx, runID: run.ID}
	matchedCount, err := merge(ctx, settlements, transfers, sink.add)
	if err != nil {
		return err
	}
	if err := sink.flush(ctx); err != nil {
		return err
	}

	tag, err := tx.Exec(ctx, `
		UPDATE reconciliation_runs
		SET status = 'completed', finished_at = $2, matched_count = $3, discrepancy_count = $4
		WHERE id = $1 AND status = 'running'
	`, run.ID, time.Now().UTC(), matchedCount, sink.total)
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

// Streaming comparison
//
// Both sides are read through server-side cursors ordered by reference with
// COLLATE "C" (byte order, which is also Go's string order), so the two
// streams can be merge-joined in one pass. Memory is bounded by the cursor
// batch, one reference group, and the discrepancy batch, not by file size.

const (
	cursorFetchSize      = 1000
	discrepancyBatchSize = 5000
)

func openScopeCursors(ctx context.Context, tx pgx.Tx, fileID uuid.UUID, settlementDate time.Time) (rowStream[SettlementRecord], rowStream[transferRow], error) {
	dayStart := settlementDate.UTC().Truncate(24 * time.Hour)
	dayEnd := dayStart.Add(24 * time.Hour)

	_, err := tx.Exec(ctx, `
		DECLARE settlement_cur NO SCROLL CURSOR FOR
		SELECT id, file_id, provider_ref, our_reference, amount, currency, status, occurred_at
		  FROM settlement_records
		 WHERE file_id = $1
		 ORDER BY our_reference COLLATE "C" NULLS LAST, id`, fileID)
	if err != nil {
		return nil, nil, fmt.Errorf("open settlement cursor: %w", err)
	}
	_, err = tx.Exec(ctx, `
		DECLARE transfer_cur NO SCROLL CURSOR FOR
		SELECT t.id, t.reference, t.amount, t.currency, t.status,
		       EXISTS(SELECT 1 FROM transfers r WHERE r.reverses_transfer_id = t.id) AS has_reversal
		  FROM transfers t
		 WHERE t.reference IS NOT NULL
		   AND t.type != 'reversal'
		   AND t.status = 'posted'
		   AND t.created_at >= $1
		   AND t.created_at < $2
		 ORDER BY t.reference COLLATE "C", t.created_at, t.id`, dayStart, dayEnd)
	if err != nil {
		return nil, nil, fmt.Errorf("open transfer cursor: %w", err)
	}

	settlements := &cursorStream[SettlementRecord]{tx: tx, name: "settlement_cur", scan: scanSettlementRecord}
	transfers := &cursorStream[transferRow]{tx: tx, name: "transfer_cur", scan: scanTransferRow}
	return settlements, transfers, nil
}

func scanSettlementRecord(row pgx.CollectableRow) (SettlementRecord, error) {
	var rec SettlementRecord
	err := row.Scan(&rec.ID, &rec.FileID, &rec.ProviderRef, &rec.OurReference,
		&rec.Amount, &rec.Currency, &rec.Status, &rec.OccurredAt)
	return rec, err
}

func scanTransferRow(row pgx.CollectableRow) (transferRow, error) {
	var t transferRow
	err := row.Scan(&t.ID, &t.Reference, &t.Amount, &t.Currency, &t.Status, &t.HasReversal)
	return t, err
}

// rowStream is a forward-only sequence with one-row lookahead.
type rowStream[T any] interface {
	peek(ctx context.Context) (T, bool, error)
	advance()
}

// cursorStream reads a declared cursor in batches of cursorFetchSize.
type cursorStream[T any] struct {
	tx   pgx.Tx
	name string
	scan pgx.RowToFunc[T]
	buf  []T
	pos  int
	done bool
}

func (c *cursorStream[T]) peek(ctx context.Context) (T, bool, error) {
	if c.pos == len(c.buf) && !c.done {
		rows, err := c.tx.Query(ctx, fmt.Sprintf("FETCH FORWARD %d FROM %s", cursorFetchSize, c.name))
		if err != nil {
			var zero T
			return zero, false, fmt.Errorf("fetch %s: %w", c.name, err)
		}
		c.buf, err = pgx.CollectRows(rows, c.scan)
		if err != nil {
			var zero T
			return zero, false, fmt.Errorf("fetch %s: %w", c.name, err)
		}
		c.pos = 0
		c.done = len(c.buf) < cursorFetchSize
	}
	if c.pos < len(c.buf) {
		return c.buf[c.pos], true, nil
	}
	var zero T
	return zero, false, nil
}

func (c *cursorStream[T]) advance() { c.pos++ }

// sliceStream feeds already-sorted rows through the same merge (used by compare).
type sliceStream[T any] struct {
	rows []T
	pos  int
}

func (s *sliceStream[T]) peek(context.Context) (T, bool, error) {
	if s.pos < len(s.rows) {
		return s.rows[s.pos], true, nil
	}
	var zero T
	return zero, false, nil
}

func (s *sliceStream[T]) advance() { s.pos++ }

// takeGroup consumes the run of rows at the head of s for which same is true.
func takeGroup[T any](ctx context.Context, s rowStream[T], same func(T) bool) ([]T, error) {
	var group []T
	for {
		row, ok, err := s.peek(ctx)
		if err != nil {
			return nil, err
		}
		if !ok || !same(row) {
			return group, nil
		}
		group = append(group, row)
		s.advance()
	}
}

// merge walks both streams in reference order (settlements must have NULL
// references last) and emits every discrepancy. It returns the matched count.
func merge(ctx context.Context, settlements rowStream[SettlementRecord], transfers rowStream[transferRow], emit func(context.Context, Discrepancy) error) (int, error) {
	matched := 0
	for {
		s, sOK, err := settlements.peek(ctx)
		if err != nil {
			return 0, err
		}
		if sOK && s.OurReference == nil {
			sOK = false // NULL references sort last; they are handled below
		}
		t, tOK, err := transfers.peek(ctx)
		if err != nil {
			return 0, err
		}
		if !sOK && !tOK {
			break
		}

		var ref string
		switch {
		case !sOK:
			ref = t.Reference
		case !tOK:
			ref = *s.OurReference
		default:
			ref = min(*s.OurReference, t.Reference)
		}

		recs, err := takeGroup(ctx, settlements, func(r SettlementRecord) bool {
			return r.OurReference != nil && *r.OurReference == ref
		})
		if err != nil {
			return 0, err
		}
		trs, err := takeGroup(ctx, transfers, func(t transferRow) bool { return t.Reference == ref })
		if err != nil {
			return 0, err
		}

		discrepancies, isMatch := reconcileRef(ref, recs, trs)
		if isMatch {
			matched++
		}
		for _, d := range discrepancies {
			if err := emit(ctx, d); err != nil {
				return 0, err
			}
		}
	}

	nullRefs, err := takeGroup(ctx, settlements, func(SettlementRecord) bool { return true })
	if err != nil {
		return 0, err
	}
	for _, rec := range nullRefs {
		recID := rec.ID
		err := emit(ctx, Discrepancy{
			Kind:               DiscrepancyMissingInLedger,
			SettlementRecordID: &recID,
			Note:               strPtr("settlement record has null our_reference"),
			Resolution:         resolutionOpen,
		})
		if err != nil {
			return 0, err
		}
	}
	return matched, nil
}

// discrepancySink buffers discrepancies and COPYs them in batches.
type discrepancySink struct {
	tx    pgx.Tx
	runID uuid.UUID
	buf   []Discrepancy
	total int
}

func (s *discrepancySink) add(ctx context.Context, d Discrepancy) error {
	s.buf = append(s.buf, d)
	if len(s.buf) >= discrepancyBatchSize {
		return s.flush(ctx)
	}
	return nil
}

func (s *discrepancySink) flush(ctx context.Context) error {
	if len(s.buf) == 0 {
		return nil
	}
	_, err := s.tx.CopyFrom(ctx,
		pgx.Identifier{"reconciliation_discrepancies"},
		[]string{"run_id", "kind", "transfer_id", "settlement_record_id",
			"expected_amount", "actual_amount", "resolution", "note"},
		pgx.CopyFromSlice(len(s.buf), func(i int) ([]any, error) {
			d := s.buf[i]
			return []any{
				s.runID, string(d.Kind), d.TransferID, d.SettlementRecordID,
				d.ExpectedAmount, d.ActualAmount, d.Resolution, d.Note,
			}, nil
		}),
	)
	if err != nil {
		return fmt.Errorf("insert discrepancies: %w", err)
	}
	s.total += len(s.buf)
	s.buf = s.buf[:0]
	return nil
}

// compare is the in-memory form of the streaming comparison: it sorts both
// sides the way the cursors do and runs the same merge.
func compare(settlements []SettlementRecord, transfers []transferRow) ([]Discrepancy, int) {
	sortedSettlements := slices.Clone(settlements)
	slices.SortStableFunc(sortedSettlements, func(a, b SettlementRecord) int {
		switch {
		case a.OurReference == nil && b.OurReference == nil:
			return cmp.Compare(a.ID, b.ID)
		case a.OurReference == nil:
			return 1
		case b.OurReference == nil:
			return -1
		}
		if c := strings.Compare(*a.OurReference, *b.OurReference); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	sortedTransfers := slices.Clone(transfers)
	slices.SortStableFunc(sortedTransfers, func(a, b transferRow) int {
		return strings.Compare(a.Reference, b.Reference)
	})

	var discrepancies []Discrepancy
	matched, _ := merge(context.Background(),
		&sliceStream[SettlementRecord]{rows: sortedSettlements},
		&sliceStream[transferRow]{rows: sortedTransfers},
		func(_ context.Context, d Discrepancy) error {
			discrepancies = append(discrepancies, d)
			return nil
		})
	return discrepancies, matched
}

// reconcileRef compares every settlement record and ledger transfer that share
// one reference. It reports whether the reference counts as matched.
func reconcileRef(ref string, recs []SettlementRecord, transfersForRef []transferRow) ([]Discrepancy, bool) {
	var discrepancies []Discrepancy

	if len(recs) == 0 {
		for _, t := range transfersForRef {
			tID := t.ID
			discrepancies = append(discrepancies, Discrepancy{
				Kind:       DiscrepancyMissingInSettlement,
				TransferID: &tID,
				Note:       strPtr(fmt.Sprintf("transfer %s has no matching settlement record", ref)),
				Resolution: resolutionOpen,
			})
		}
		return discrepancies, false
	}

	if len(recs) > 1 {
		firstID := recs[0].ID
		discrepancies = append(discrepancies, Discrepancy{
			Kind:               DiscrepancyDuplicateInSettlement,
			SettlementRecordID: &firstID,
			Note:               strPtr(fmt.Sprintf("duplicate our_reference: %s (count: %d)", ref, len(recs))),
			Resolution:         resolutionOpen,
		})
	}

	if len(transfersForRef) == 0 {
		for _, rec := range recs {
			recID := rec.ID
			discrepancies = append(discrepancies, Discrepancy{
				Kind:               DiscrepancyMissingInLedger,
				SettlementRecordID: &recID,
				Note:               strPtr("settlement record has no matching transfer"),
				Resolution:         resolutionOpen,
			})
		}
		return discrepancies, false
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
		return discrepancies, false
	}

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

	return discrepancies, true
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
