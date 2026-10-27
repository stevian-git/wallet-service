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
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrDuplicateFile = errors.New("settlement file with same sha256 already exists")

type Importer struct {
	pool *pgxpool.Pool
}

func NewImporter(pool *pgxpool.Pool) *Importer {
	return &Importer{pool: pool}
}

func (imp *Importer) Import(ctx context.Context, input ImportInput) (SettlementFile, error) {
	if input.Provider == "" {
		return SettlementFile{}, errors.New("provider is required")
	}
	if input.FileName == "" {
		return SettlementFile{}, errors.New("file_name is required")
	}
	if len(input.FileContent) == 0 {
		return SettlementFile{}, errors.New("file content is required")
	}

	records, err := parseCSV(input.FileContent)
	if err != nil {
		return SettlementFile{}, fmt.Errorf("parse csv: %w", err)
	}
	if len(records) == 0 {
		return SettlementFile{}, errors.New("csv file must contain at least one data row")
	}

	sha := sha256.Sum256(input.FileContent)
	settlementDate := input.SettlementDate.UTC().Truncate(24 * time.Hour)

	file := SettlementFile{
		ID:             uuid.New(),
		Provider:       input.Provider,
		FileName:       input.FileName,
		FileSHA256:     sha[:],
		SettlementDate: settlementDate,
		RecordCount:    len(records),
	}

	tx, err := imp.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.ReadCommitted,
	})
	if err != nil {
		return SettlementFile{}, err
	}
	defer tx.Rollback(ctx)

	err = tx.QueryRow(ctx, `
		INSERT INTO settlement_files (id, provider, file_name, file_sha256, settlement_date, record_count)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING imported_at`,
		file.ID, file.Provider, file.FileName, file.FileSHA256, file.SettlementDate, file.RecordCount,
	).Scan(&file.ImportedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return SettlementFile{}, ErrDuplicateFile
		}
		return SettlementFile{}, err
	}

	settlementRecords := make([]SettlementRecord, 0, len(records))
	for i, raw := range records {
		rec, err := toSettlementRecord(file.ID, raw, i+1)
		if err != nil {
			return SettlementFile{}, fmt.Errorf("row %d: %w", i+1, err)
		}
		settlementRecords = append(settlementRecords, rec)
	}

	batchSize := 500
	for start := 0; start < len(settlementRecords); start += batchSize {
		end := start + batchSize
		if end > len(settlementRecords) {
			end = len(settlementRecords)
		}
		batch := settlementRecords[start:end]

		rows := make([][]any, 0, len(batch))
		for _, rec := range batch {
			rows = append(rows, []any{
				rec.FileID, rec.ProviderRef, rec.OurReference,
				rec.Amount, rec.Currency, rec.Status, rec.OccurredAt,
			})
		}

		_, err = tx.CopyFrom(ctx,
			pgx.Identifier{"settlement_records"},
			[]string{"file_id", "provider_ref", "our_reference", "amount", "currency", "status", "occurred_at"},
			pgx.CopyFromRows(rows),
		)
		if err != nil {
			return SettlementFile{}, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return SettlementFile{}, err
	}

	return file, nil
}

func parseCSV(content []byte) ([][]string, error) {
	reader := csv.NewReader(strings.NewReader(string(content)))
	reader.TrimLeadingSpace = true

	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	expectedHeader := []string{"provider_ref", "our_reference", "amount", "currency", "status", "occurred_at"}
	if len(header) != len(expectedHeader) {
		return nil, fmt.Errorf("expected %d columns, got %d", len(expectedHeader), len(header))
	}
	for i, col := range expectedHeader {
		if strings.TrimSpace(strings.ToLower(header[i])) != col {
			return nil, fmt.Errorf("expected column %d to be %q, got %q", i+1, col, header[i])
		}
	}

	var records [][]string
	for {
		row, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read row: %w", err)
		}
		if len(row) != len(expectedHeader) {
			return nil, fmt.Errorf("expected %d columns, got %d", len(expectedHeader), len(row))
		}
		records = append(records, row)
	}
	return records, nil
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
