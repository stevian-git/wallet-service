package reconciliation

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrDuplicateFile = errors.New("settlement file with same sha256 already exists")

// ErrInvalidSettlementFile wraps every problem with the uploaded file itself
// (empty file, bad header, bad row), so the API can answer 422 instead of 500.
var ErrInvalidSettlementFile = errors.New("invalid settlement file")

func invalidFile(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidSettlementFile, fmt.Sprintf(format, args...))
}

var settlementCSVHeader = []string{"provider_ref", "our_reference", "amount", "currency", "status", "occurred_at"}

type Importer struct {
	pool *pgxpool.Pool
}

func NewImporter(pool *pgxpool.Pool) *Importer {
	return &Importer{pool: pool}
}

// Import streams the file twice and never holds it in memory: the first pass
// validates every row, counts them, and hashes the bytes (both are needed for
// the settlement_files row, which must exist before its records); the second
// pass rewinds and COPYs the rows straight from the reader. A bad row is
// reported before anything touches the database.
func (imp *Importer) Import(ctx context.Context, input ImportInput) (SettlementFile, error) {
	if input.Provider == "" {
		return SettlementFile{}, errors.New("provider is required")
	}
	if input.FileName == "" {
		return SettlementFile{}, errors.New("file_name is required")
	}
	if input.Content == nil {
		return SettlementFile{}, invalidFile("file content is required")
	}

	if _, err := input.Content.Seek(0, io.SeekStart); err != nil {
		return SettlementFile{}, fmt.Errorf("rewind settlement file: %w", err)
	}
	sha, count, err := scanSettlementCSV(input.Content)
	if err != nil {
		return SettlementFile{}, err
	}
	if _, err := input.Content.Seek(0, io.SeekStart); err != nil {
		return SettlementFile{}, fmt.Errorf("rewind settlement file: %w", err)
	}

	file := SettlementFile{
		ID:             uuid.New(),
		Provider:       input.Provider,
		FileName:       input.FileName,
		FileSHA256:     sha,
		SettlementDate: input.SettlementDate.UTC().Truncate(24 * time.Hour),
		RecordCount:    count,
	}

	rows, err := newSettlementCSVReader(input.Content)
	if err != nil {
		return SettlementFile{}, err
	}
	source := &settlementCopySource{rows: rows, fileID: file.ID}
	if err := imp.insertFile(ctx, &file, source); err != nil {
		return SettlementFile{}, err
	}
	return file, nil
}

// scanSettlementCSV is the validating first pass: it returns the SHA-256 of
// the whole input and the number of data rows.
func scanSettlementCSV(content io.Reader) ([]byte, int, error) {
	hasher := sha256.New()
	tee := io.TeeReader(content, hasher)
	rows, err := newSettlementCSVReader(tee)
	if err != nil {
		return nil, 0, err
	}
	count := 0
	for {
		row, err := rows.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, 0, invalidFile("row %d: %v", count+1, err)
		}
		count++
		if _, err := toSettlementRecord(uuid.Nil, row, count); err != nil {
			return nil, 0, invalidFile("row %d: %v", count, err)
		}
	}
	if count == 0 {
		return nil, 0, invalidFile("csv file must contain at least one data row")
	}
	// The CSV reader may stop early on a trailing partial buffer; hash the rest.
	if _, err := io.Copy(io.Discard, tee); err != nil {
		return nil, 0, fmt.Errorf("read settlement file: %w", err)
	}
	return hasher.Sum(nil), count, nil
}

// newSettlementCSVReader reads and checks the header and returns a reader
// positioned at the first data row.
func newSettlementCSVReader(content io.Reader) (*csv.Reader, error) {
	reader := csv.NewReader(content)
	reader.TrimLeadingSpace = true
	reader.FieldsPerRecord = len(settlementCSVHeader)
	reader.ReuseRecord = true

	header, err := reader.Read()
	if errors.Is(err, io.EOF) {
		return nil, invalidFile("file content is required")
	}
	if err != nil {
		return nil, invalidFile("read header: %v", err)
	}
	for i, col := range settlementCSVHeader {
		if strings.TrimSpace(strings.ToLower(header[i])) != col {
			return nil, invalidFile("expected column %d to be %q, got %q", i+1, col, header[i])
		}
	}
	return reader, nil
}

// settlementCopySource feeds COPY one CSV row at a time (pgx.CopyFromSource).
type settlementCopySource struct {
	rows   *csv.Reader
	fileID uuid.UUID
	line   int
	values []any
	err    error
}

func (s *settlementCopySource) Next() bool {
	row, err := s.rows.Read()
	if errors.Is(err, io.EOF) {
		return false
	}
	s.line++
	if err != nil {
		s.err = invalidFile("row %d: %v", s.line, err)
		return false
	}
	rec, err := toSettlementRecord(s.fileID, row, s.line)
	if err != nil {
		s.err = invalidFile("row %d: %v", s.line, err)
		return false
	}
	s.values = []any{rec.FileID, rec.ProviderRef, rec.OurReference, rec.Amount, rec.Currency, rec.Status, rec.OccurredAt}
	return true
}

func (s *settlementCopySource) Values() ([]any, error) { return s.values, nil }
func (s *settlementCopySource) Err() error             { return s.err }

// insertFile stores the file and its records in one transaction and marks the
// currently active file for the same provider and date as superseded by it.
func (imp *Importer) insertFile(ctx context.Context, file *SettlementFile, records pgx.CopyFromSource) error {
	tx, err := imp.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.ReadCommitted,
	})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Imports for the same provider and date are serialized by a
	// transaction-scoped advisory lock: without it, two concurrent imports both
	// see the same active file and the loser fails on settlement_files_one_active
	// (which stays as the backstop). Imports for other providers/dates do not wait.
	_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		"settlement_file:"+file.Provider+":"+file.SettlementDate.Format(time.DateOnly))
	if err != nil {
		return fmt.Errorf("lock provider/date: %w", err)
	}

	// The FK to the new file is deferred, so the old file can point to it
	// before the INSERT below.
	_, err = tx.Exec(ctx, `
		UPDATE settlement_files SET superseded_by = $1
		 WHERE provider = $2 AND settlement_date = $3 AND superseded_by IS NULL`,
		file.ID, file.Provider, file.SettlementDate)
	if err != nil {
		return fmt.Errorf("supersede previous file: %w", err)
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO settlement_files (id, provider, file_name, file_sha256, settlement_date, record_count)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING imported_at`,
		file.ID, file.Provider, file.FileName, file.FileSHA256, file.SettlementDate, file.RecordCount,
	).Scan(&file.ImportedAt)
	if err != nil {
		if uniqueViolationOn(err, "settlement_files_file_sha256_key") {
			return ErrDuplicateFile
		}
		return err
	}

	copied, err := tx.CopyFrom(ctx,
		pgx.Identifier{"settlement_records"},
		[]string{"file_id", "provider_ref", "our_reference", "amount", "currency", "status", "occurred_at"},
		records,
	)
	if err != nil {
		return err
	}
	if int(copied) != file.RecordCount {
		// The reader changed between the two passes; refuse a partial file.
		return fmt.Errorf("settlement file changed while importing: copied %d of %d rows", copied, file.RecordCount)
	}

	return tx.Commit(ctx)
}

func toSettlementRecord(fileID uuid.UUID, row []string, lineNum int) (SettlementRecord, error) {
	providerRef := strings.TrimSpace(row[0])
	if providerRef == "" {
		return SettlementRecord{}, errors.New("provider_ref is required")
	}

	ourRef := strings.TrimSpace(row[1])
	var ourReference *string
	if ourRef != "" {
		ourReference = &ourRef
	}

	amount, err := strconv.ParseInt(strings.TrimSpace(row[2]), 10, 64)
	if err != nil || amount <= 0 {
		return SettlementRecord{}, errors.New("amount must be a positive integer")
	}

	currency := strings.ToUpper(strings.TrimSpace(row[3]))
	if len(currency) != 3 {
		return SettlementRecord{}, errors.New("currency must be a 3-letter code")
	}

	status := strings.ToLower(strings.TrimSpace(row[4]))
	if status != "success" && status != "failed" && status != "reversed" {
		return SettlementRecord{}, errors.New("status must be success, failed, or reversed")
	}

	occurredAt, err := time.Parse(time.RFC3339, strings.TrimSpace(row[5]))
	if err != nil {
		return SettlementRecord{}, errors.New("occurred_at must be RFC3339 format")
	}

	return SettlementRecord{
		FileID:       fileID,
		ProviderRef:  providerRef,
		OurReference: ourReference,
		Amount:       amount,
		Currency:     currency,
		Status:       status,
		OccurredAt:   occurredAt.UTC(),
	}, nil
}
