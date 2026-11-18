package main

import (
	"context"
	"encoding/csv"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stevians/wallet-service/internal/platform/bootstrap"
	"github.com/stevians/wallet-service/internal/platform/config"
	"github.com/stevians/wallet-service/internal/platform/db"
	"github.com/stevians/wallet-service/internal/platform/log"
)

func main() {
	cfg, err := config.LoadTool()
	if err != nil {
		bootstrap.Exit("load config", err)
	}
	slog.SetDefault(log.New(cfg.LogLevel))
	if err := run(cfg); err != nil {
		bootstrap.Exit("mock settlement", err)
	}
}

func run(cfg config.ToolConfig) error {
	logger := slog.Default()
	ctx := context.Background()

	pool, err := db.OpenApp(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	outputFile := "mock_settlement.csv"
	if len(os.Args) > 1 {
		outputFile = os.Args[1]
	}

	settlementDate := time.Now().UTC().Truncate(24 * time.Hour)
	if len(os.Args) > 2 {
		parsed, parseErr := time.Parse("2006-01-02", os.Args[2])
		if parseErr != nil {
			return fmt.Errorf("invalid date %q, use YYYY-MM-DD: %w", os.Args[2], parseErr)
		}
		settlementDate = parsed
	}

	logger.Info("generating mock settlement",
		"date", settlementDate.Format("2006-01-02"),
		"output", outputFile,
	)

	transfers, err := loadTransfersForDate(ctx, pool, settlementDate)
	if err != nil {
		return fmt.Errorf("load transfers: %w", err)
	}

	logger.Info("loaded transfers", "count", len(transfers))

	if err := writeMockCSV(outputFile, transfers); err != nil {
		return fmt.Errorf("write csv: %w", err)
	}

	logger.Info("mock settlement generated", "file", outputFile, "records", len(transfers))
	return nil
}

type transferRecord struct {
	ID        uuid.UUID
	Reference string
	Amount    int64
	Currency  string
	Status    string
	CreatedAt time.Time
}

func loadTransfersForDate(ctx context.Context, pool *pgxpool.Pool, date time.Time) ([]transferRecord, error) {
	dayStart := date.UTC().Truncate(24 * time.Hour)
	dayEnd := dayStart.Add(24 * time.Hour)

	rows, err := pool.Query(ctx, `
		SELECT id, reference, amount, currency, status, created_at
		FROM transfers
		WHERE reference IS NOT NULL
		  AND type != 'reversal'
		  AND created_at >= $1
		  AND created_at < $2
		ORDER BY created_at
	`, dayStart, dayEnd)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var transfers []transferRecord
	for rows.Next() {
		var t transferRecord
		if err := rows.Scan(&t.ID, &t.Reference, &t.Amount, &t.Currency, &t.Status, &t.CreatedAt); err != nil {
			return nil, err
		}
		transfers = append(transfers, t)
	}
	return transfers, rows.Err()
}

func writeMockCSV(filename string, transfers []transferRecord) error {
	file, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	defer writer.Flush()

	if err := writer.Write([]string{"provider_ref", "our_reference", "amount", "currency", "status", "occurred_at"}); err != nil {
		return err
	}

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	for _, t := range transfers {
		providerRef := fmt.Sprintf("prov_%s", uuid.NewString()[:8])

		ourRef := t.Reference
		amount := t.Amount
		status := "success"

		if rng.Float64() < 0.05 {
			amount = int64(float64(t.Amount) * (0.9 + rng.Float64()*0.2))
		}

		if rng.Float64() < 0.02 {
			ourRef = ""
		}

		if rng.Float64() < 0.01 {
			status = "reversed"
		}

		occurredAt := t.CreatedAt.Add(time.Duration(rng.Intn(3600)) * time.Second)

		row := []string{
			providerRef,
			ourRef,
			fmt.Sprintf("%d", amount),
			t.Currency,
			status,
			occurredAt.Format(time.RFC3339),
		}
		if err := writer.Write(row); err != nil {
			return err
		}
	}

	return nil
}
