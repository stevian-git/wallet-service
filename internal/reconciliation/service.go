package reconciliation

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

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

func (s *Service) CreateRun(ctx context.Context, input CreateRunInput) (ReconciliationRun, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM settlement_files WHERE id = $1)`, input.FileID).Scan(&exists)
	if err != nil {
		return ReconciliationRun{}, err
	}
	if !exists {
		return ReconciliationRun{}, errors.New("settlement file not found")
	}

	var run ReconciliationRun
	err = s.pool.QueryRow(ctx, `
		INSERT INTO reconciliation_runs (id, file_id)
		VALUES ($1, $2)
		RETURNING id, file_id, status, started_at, finished_at, matched_count, discrepancy_count, error
	`, uuid.New(), input.FileID).Scan(
		&run.ID, &run.FileID, &run.Status, &run.StartedAt,
		&run.FinishedAt, &run.MatchedCount, &run.DiscrepancyCount, &run.Error,
	)
	return run, err
}

func (s *Service) GetRun(ctx context.Context, runID uuid.UUID) (ReconciliationRun, error) {
	var run ReconciliationRun
	err := s.pool.QueryRow(ctx, `
		SELECT id, file_id, status, started_at, finished_at, matched_count, discrepancy_count, error
		FROM reconciliation_runs
		WHERE id = $1
	`, runID).Scan(
		&run.ID, &run.FileID, &run.Status, &run.StartedAt,
		&run.FinishedAt, &run.MatchedCount, &run.DiscrepancyCount, &run.Error,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReconciliationRun{}, fmt.Errorf("reconciliation run not found")
	}
	return run, err
}

func (s *Service) ListDiscrepancies(ctx context.Context, input ListDiscrepanciesInput) (DiscrepanciesPage, error) {
	limit := input.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	cursorID, err := decodeReconCursor(input.Cursor)
	if err != nil {
		return DiscrepanciesPage{}, err
	}

	var query strings.Builder
	var args []any
	argIdx := 1

	query.WriteString(`
		SELECT id, run_id, kind, transfer_id, settlement_record_id,
		       expected_amount, actual_amount, resolution, note, created_at
		FROM reconciliation_discrepancies
		WHERE run_id = $1`)
	args = append(args, input.RunID)
	argIdx++

	if input.Resolution != nil {
		query.WriteString(fmt.Sprintf(" AND resolution = $%d", argIdx))
		args = append(args, *input.Resolution)
		argIdx++
	}

	if cursorID != nil {
		query.WriteString(fmt.Sprintf(" AND id > $%d", argIdx))
		args = append(args, *cursorID)
		argIdx++
	}

	query.WriteString(fmt.Sprintf(" ORDER BY id LIMIT $%d", argIdx))
	args = append(args, limit+1)

	rows, err := s.pool.Query(ctx, query.String(), args...)
	if err != nil {
		return DiscrepanciesPage{}, err
	}
	defer rows.Close()

	var discrepancies []Discrepancy
	for rows.Next() {
		var d Discrepancy
		if err := rows.Scan(&d.ID, &d.RunID, &d.Kind, &d.TransferID, &d.SettlementRecordID,
			&d.ExpectedAmount, &d.ActualAmount, &d.Resolution, &d.Note, &d.CreatedAt); err != nil {
			return DiscrepanciesPage{}, err
		}
		discrepancies = append(discrepancies, d)
	}
	if err := rows.Err(); err != nil {
		return DiscrepanciesPage{}, err
	}

	var nextCursor *string
	if len(discrepancies) > limit {
		lastID := discrepancies[limit-1].ID
		cursor := encodeReconCursor(lastID)
		nextCursor = &cursor
		discrepancies = discrepancies[:limit]
	}

	if discrepancies == nil {
		discrepancies = []Discrepancy{}
	}

	return DiscrepanciesPage{
		Data:       discrepancies,
		NextCursor: nextCursor,
	}, nil
}

func (s *Service) Reconciler() *Reconciler {
	return s.reconciler
}

func (s *Service) ListRuns(ctx context.Context, input ListRunsInput) (RunsPage, error) {
	limit := input.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	cursorID, err := decodeReconCursorUUID(input.Cursor)
	if err != nil {
		return RunsPage{}, err
	}

	var query strings.Builder
	var args []any
	argIdx := 1

	query.WriteString(`
		SELECT id, file_id, status, started_at, finished_at, matched_count, discrepancy_count, error
		FROM reconciliation_runs
		WHERE 1=1`)

	if cursorID != nil {
		query.WriteString(fmt.Sprintf(" AND id < $%d", argIdx))
		args = append(args, *cursorID)
		argIdx++
	}

	query.WriteString(fmt.Sprintf(" ORDER BY id DESC LIMIT $%d", argIdx))
	args = append(args, limit+1)

	rows, err := s.pool.Query(ctx, query.String(), args...)
	if err != nil {
		return RunsPage{}, err
	}
	defer rows.Close()

	var runs []ReconciliationRun
	for rows.Next() {
		var r ReconciliationRun
		if err := rows.Scan(&r.ID, &r.FileID, &r.Status, &r.StartedAt, &r.FinishedAt, &r.MatchedCount, &r.DiscrepancyCount, &r.Error); err != nil {
			return RunsPage{}, err
		}
		runs = append(runs, r)
	}
	if err := rows.Err(); err != nil {
		return RunsPage{}, err
	}

	var nextCursor *string
	if len(runs) > limit {
		lastID := runs[limit-1].ID
		cursor := encodeReconCursorUUID(lastID)
		nextCursor = &cursor
		runs = runs[:limit]
	}

	if runs == nil {
		runs = []ReconciliationRun{}
	}

	return RunsPage{
		Data:       runs,
		NextCursor: nextCursor,
	}, nil
}

func (s *Service) ListSettlementFiles(ctx context.Context, input ListSettlementFilesInput) (SettlementFilesPage, error) {
	limit := input.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	cursorID, err := decodeReconCursorUUID(input.Cursor)
	if err != nil {
		return SettlementFilesPage{}, err
	}

	var query strings.Builder
	var args []any
	argIdx := 1

	query.WriteString(`
		SELECT id, provider, file_name, file_sha256, settlement_date, record_count, imported_at
		FROM settlement_files
		WHERE 1=1`)

	if cursorID != nil {
		query.WriteString(fmt.Sprintf(" AND id < $%d", argIdx))
		args = append(args, *cursorID)
		argIdx++
	}

	query.WriteString(fmt.Sprintf(" ORDER BY id DESC LIMIT $%d", argIdx))
	args = append(args, limit+1)

	rows, err := s.pool.Query(ctx, query.String(), args...)
	if err != nil {
		return SettlementFilesPage{}, err
	}
	defer rows.Close()

	var files []SettlementFile
	for rows.Next() {
		var f SettlementFile
		if err := rows.Scan(&f.ID, &f.Provider, &f.FileName, &f.FileSHA256, &f.SettlementDate, &f.RecordCount, &f.ImportedAt); err != nil {
			return SettlementFilesPage{}, err
		}
		files = append(files, f)
	}
	if err := rows.Err(); err != nil {
		return SettlementFilesPage{}, err
	}

	var nextCursor *string
	if len(files) > limit {
		lastID := files[limit-1].ID
		cursor := encodeReconCursorUUID(lastID)
		nextCursor = &cursor
		files = files[:limit]
	}

	if files == nil {
		files = []SettlementFile{}
	}

	return SettlementFilesPage{
		Data:       files,
		NextCursor: nextCursor,
	}, nil
}

func encodeReconCursor(id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(id, 10)))
}

func encodeReconCursorUUID(id uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(id.String()))
}

func decodeReconCursor(cursor string) (*int64, error) {
	if cursor == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return nil, fmt.Errorf("invalid cursor")
	}
	id, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil || id <= 0 {
		return nil, fmt.Errorf("invalid cursor")
	}
	return &id, nil
}

func decodeReconCursorUUID(cursor string) (*uuid.UUID, error) {
	if cursor == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return nil, fmt.Errorf("invalid cursor")
	}
	id, err := uuid.Parse(string(raw))
	if err != nil {
		return nil, fmt.Errorf("invalid cursor")
	}
	return &id, nil
}
