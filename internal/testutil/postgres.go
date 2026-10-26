package testutil

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/stevians/wallet-service/internal/platform/db"
	"github.com/stevians/wallet-service/migrations"
)

type Postgres struct {
	Pool        *pgxpool.Pool
	AppURL      string
	MigratorURL string
}

func NewPostgres(t *testing.T) *Postgres {
	t.Helper()

	ctx := context.Background()
	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("wallet"),
		postgres.WithUsername("postgres"),
		postgres.WithPassword("postgres"),
		postgres.WithInitScripts(initScriptPath(t)),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			t.Logf("terminate postgres container: %v", err)
		}
	})

	connectionString, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("get container connection string: %v", err)
	}
	parsed, err := url.Parse(connectionString)
	if err != nil {
		t.Fatalf("parse container connection string: %v", err)
	}
	endpoint := net.JoinHostPort(parsed.Hostname(), parsed.Port())

	appURL := fmt.Sprintf("postgres://ledger_app:app_dev_pw@%s/wallet?sslmode=disable", endpoint)
	migratorURL := fmt.Sprintf("pgx5://ledger_migrator:migrator_dev_pw@%s/wallet?sslmode=disable&default_query_exec_mode=simple_protocol", endpoint)

	if err := runMigrations(migratorURL); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	pool, err := db.Open(ctx, appURL)
	if err != nil {
		t.Fatalf("open app pool: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := db.VerifyAppRole(ctx, pool); err != nil {
		t.Fatalf("verify app role: %v", err)
	}

	return &Postgres{Pool: pool, AppURL: appURL, MigratorURL: migratorURL}
}

func runMigrations(databaseURL string) error {
	source, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return err
	}
	m, err := migrate.NewWithSourceInstance("iofs", source, databaseURL)
	if err != nil {
		source.Close()
		return err
	}
	defer m.Close()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

func initScriptPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve testutil source path")
	}
	path := filepath.Join(filepath.Dir(file), "..", "..", "deploy", "postgres", "init", "00-roles.sql")
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("resolve init script path: %v", err)
	}
	return absolute
}
