package db_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stevians/wallet-service/internal/platform/db"
	"github.com/stevians/wallet-service/internal/testutil"
)

func TestOpenAppAcceptsAppRoleAndRejectsOwner(t *testing.T) {
	pg := testutil.NewPostgres(t)
	ctx := context.Background()

	pool, err := db.OpenApp(ctx, pg.AppURL)
	if err != nil {
		t.Fatalf("open as ledger_app: %v", err)
	}
	pool.Close()

	// The schema owner can UPDATE/DELETE ledger_entries, so it must be refused.
	ownerURL := strings.Replace(pg.MigratorURL, "pgx5://", "postgres://", 1)
	if pool, err := db.OpenApp(ctx, ownerURL); err == nil {
		pool.Close()
		t.Fatal("open as ledger_migrator succeeded, want a role check error")
	}
}
