package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stevians/wallet-service/internal/platform/telemetry"
)

func Open(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}

	config.MaxConns = 20
	config.MinConns = 2
	config.MaxConnLifetime = time.Hour
	config.MaxConnIdleTime = 30 * time.Minute
	config.HealthCheckPeriod = 30 * time.Second
	config.ConnConfig.RuntimeParams["application_name"] = "wallet-api"
	config.ConnConfig.Tracer = telemetry.NewPGXTracer()

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("open database pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	return pool, nil
}

// VerifyAppRole refuses to start when the application could update or delete
// append-only ledger tables. It is the runtime guard for the privilege model
// established by the migration GRANTs.
func VerifyAppRole(ctx context.Context, pool *pgxpool.Pool) error {
	var (
		currentUser    string
		superuser      bool
		canMutateLogs  bool
		canMutateBooks bool
	)
	err := pool.QueryRow(ctx, `
		SELECT current_user,
		       (SELECT rolsuper FROM pg_roles WHERE rolname = current_user),
		       has_table_privilege(current_user, 'public.ledger_entries', 'UPDATE')
		         OR has_table_privilege(current_user, 'public.ledger_entries', 'DELETE')
		         OR has_table_privilege(current_user, 'public.ledger_entries', 'TRUNCATE'),
		       has_table_privilege(current_user, 'public.audit_log', 'UPDATE')
		         OR has_table_privilege(current_user, 'public.audit_log', 'DELETE')
		         OR has_table_privilege(current_user, 'public.audit_log', 'TRUNCATE')
	`).Scan(&currentUser, &superuser, &canMutateBooks, &canMutateLogs)
	if err != nil {
		return fmt.Errorf("verify database role (are migrations applied?): %w", err)
	}
	if superuser {
		return fmt.Errorf("refusing to run as superuser %q; use the ledger_app role", currentUser)
	}
	if canMutateBooks || canMutateLogs {
		return fmt.Errorf("refusing to run as %q: it can mutate append-only tables; use the ledger_app role", currentUser)
	}
	return nil
}
