package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/stevians/wallet-service/migrations"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	databaseURL := os.Getenv("MIGRATE_DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("MIGRATE_DATABASE_URL is required (must use the ledger_migrator role)")
	}

	source, err := iofs.New(migrations.FS, ".")
	if err != nil {
		log.Fatalf("open migrations: %v", err)
	}
	defer source.Close()

	m, err := migrate.NewWithSourceInstance("iofs", source, databaseURL)
	if err != nil {
		log.Fatalf("create migrate instance: %v", err)
	}
	defer closeMigrate(m)

	switch os.Args[1] {
	case "up":
		err = m.Up()
	case "down":
		steps := 1
		if len(os.Args) > 2 {
			steps, err = strconv.Atoi(os.Args[2])
			if err != nil || steps < 1 {
				log.Fatalf("invalid down step count: %q", os.Args[2])
			}
		}
		err = m.Steps(-steps)
	case "version":
		version, dirty, versionErr := m.Version()
		if versionErr != nil {
			log.Fatalf("version: %v", versionErr)
		}
		fmt.Printf("version=%d dirty=%t\n", version, dirty)
		return
	case "force":
		if len(os.Args) != 3 {
			usage()
			os.Exit(2)
		}
		version, parseErr := strconv.Atoi(os.Args[2])
		if parseErr != nil {
			log.Fatalf("invalid version: %q", os.Args[2])
		}
		err = m.Force(version)
	default:
		usage()
		os.Exit(2)
	}

	if err != nil && !errors.Is(err, migrate.ErrNoChange) {
		log.Fatalf("migrate %s: %v", os.Args[1], err)
	}
	if errors.Is(err, migrate.ErrNoChange) {
		log.Println("no change")
	}
}

func closeMigrate(m *migrate.Migrate) {
	sourceErr, databaseErr := m.Close()
	if sourceErr != nil {
		log.Printf("close migration source: %v", sourceErr)
	}
	if databaseErr != nil {
		log.Printf("close migration database: %v", databaseErr)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: migrate <up|down [steps]|version|force <version>>")
	fmt.Fprintln(os.Stderr, "MIGRATE_DATABASE_URL must point at the ledger_migrator role")
}
