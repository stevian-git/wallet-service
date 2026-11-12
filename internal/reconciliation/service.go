package reconciliation

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stevians/wallet-service/internal/pagination"
)

var (
	ErrRunNotFound  = errors.New("reconciliation run not found")
	ErrFileNotFound = errors.New("settlement file not found")
)

const runColumns = `id, file_id, status, started_at, finished_at, matched_count, discrepancy_count, error`

func scanRun(row pgx.Row) (ReconciliationRun, error) {
	var run ReconciliationRun
	err := row.Scan(
		&run.ID, &run.FileID, &run.Status, &run.StartedAt,
		&run.FinishedAt, &run.MatchedCount, &run.DiscrepancyCount, &run.Error,
	)
	return run, err
}

type Service struct {
	pool       *pgxpool.Pool
	importer   *Importer
	reconciler *Reconciler
}

func NewService(pool *pgxpool.Pool) *Service {
	return &Service{
		pool:       pool,
		importer:   NewImporter(pool),
		reconciler: NewReconciler(pool),
	}
}

func (s *Service) Import(ctx context.Context, input ImportInput) (SettlementFile, error) {
	return s.importer.Import(ctx, input)
}

func (s *Service) Reconciler() *Reconciler {
	return s.reconciler
}

func (s *Service) CreateRun(ctx context.Context, input CreateRunInput) (ReconciliationRun, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM settlement_files WHERE id = $1)`, input.FileID).Scan(&exists)
	if err != nil {
		return ReconciliationRun{}, err
	}
	if !exists {
		return ReconciliationRun{}, ErrFileNotFound
	}

	return scanRun(s.pool.QueryRow(ctx, `
		INSERT INTO reconciliation_runs (id, file_id)
		VALUES ($1, $2)
		RETURNING `+runColumns, uuid.New(), input.FileID))
}

func (s *Service) GetRun(ctx context.Context, runID uuid.UUID) (ReconciliationRun, error) {
	run, err := scanRun(s.pool.QueryRow(ctx,
		`SELECT `+runColumns+` FROM reconciliation_runs WHERE id = $1`, runID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ReconciliationRun{}, ErrRunNotFound
	}
	return run, err
}

func (s *Service) ListDiscrepancies(ctx context.Context, input ListDiscrepanciesInput) (DiscrepanciesPage, error) {
	limit := pagination.ClampLimit(input.Limit)
	afterID, err := pagination.DecodeInt64(input.Cursor)
	if err != nil {
		return DiscrepanciesPage{}, err
	}

	rows, err := s.pool.Query(ctx, `
		SELECT id, run_id, kind, transfer_id, settlement_record_id,
		       expected_amount, actual_amount, resolution, note, created_at
		  FROM reconciliation_discrepancies
		 WHERE run_id = $1
		   AND ($2::text IS NULL OR resolution = $2)
		   AND ($3::bigint IS NULL OR id > $3)
		 ORDER BY id
		 LIMIT $4`, input.RunID, input.Resolution, afterID, limit+1)
	if err != nil {
		return DiscrepanciesPage{}, err
	}
	discrepancies, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Discrepancy, error) {
		var d Discrepancy
		err := row.Scan(&d.ID, &d.RunID, &d.Kind, &d.TransferID, &d.SettlementRecordID,
			&d.ExpectedAmount, &d.ActualAmount, &d.Resolution, &d.Note, &d.CreatedAt)
		return d, err
	})
	if err != nil {
		return DiscrepanciesPage{}, err
	}

	page := DiscrepanciesPage{}
	page.Data, page.NextCursor = trimPage(discrepancies, limit, func(d Discrepancy) string {
		return pagination.EncodeInt64(d.ID)
	})
	return page, nil
}

func (s *Service) ListRuns(ctx context.Context, input ListRunsInput) (RunsPage, error) {
	limit := pagination.ClampLimit(input.Limit)
	beforeID, err := pagination.DecodeUUID(input.Cursor)
	if err != nil {
		return RunsPage{}, err
	}

	rows, err := s.pool.Query(ctx, `
		SELECT `+runColumns+`
		  FROM reconciliation_runs
		 WHERE ($1::uuid IS NULL OR id < $1)
		 ORDER BY id DESC
		 LIMIT $2`, beforeID, limit+1)
	if err != nil {
		return RunsPage{}, err
	}
	runs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ReconciliationRun, error) {
		return scanRun(row)
	})
	if err != nil {
		return RunsPage{}, err
	}

	page := RunsPage{}
	page.Data, page.NextCursor = trimPage(runs, limit, func(r ReconciliationRun) string {
		return pagination.EncodeUUID(r.ID)
	})
	return page, nil
}

func (s *Service) ListSettlementFiles(ctx context.Context, input ListSettlementFilesInput) (SettlementFilesPage, error) {
	limit := pagination.ClampLimit(input.Limit)
	beforeID, err := pagination.DecodeUUID(input.Cursor)
	if err != nil {
		return SettlementFilesPage{}, err
	}

	rows, err := s.pool.Query(ctx, `
		SELECT id, provider, file_name, file_sha256, settlement_date, record_count, imported_at
		  FROM settlement_files
		 WHERE ($1::uuid IS NULL OR id < $1)
		 ORDER BY id DESC
		 LIMIT $2`, beforeID, limit+1)
	if err != nil {
		return SettlementFilesPage{}, err
	}
	files, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (SettlementFile, error) {
		var f SettlementFile
		err := row.Scan(&f.ID, &f.Provider, &f.FileName, &f.FileSHA256, &f.SettlementDate, &f.RecordCount, &f.ImportedAt)
		return f, err
	})
	if err != nil {
		return SettlementFilesPage{}, err
	}

	page := SettlementFilesPage{}
	page.Data, page.NextCursor = trimPage(files, limit, func(f SettlementFile) string {
		return pagination.EncodeUUID(f.ID)
	})
	return page, nil
}

// trimPage cuts a limit+1 result to limit and encodes the next cursor from the
// last row kept, if there are more rows.
func trimPage[T any](rows []T, limit int, cursorOf func(T) string) ([]T, *string) {
	rows, more := pagination.Trim(rows, limit)
	if !more {
		return rows, nil
	}
	cursor := cursorOf(rows[len(rows)-1])
	return rows, &cursor
}
