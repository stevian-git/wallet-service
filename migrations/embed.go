package migrations

import "embed"

// FS contains the golang-migrate migration files for the service.
//
//go:embed *.sql
var FS embed.FS
