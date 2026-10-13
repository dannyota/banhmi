// Package migrations embeds the append-only Goose SQL chains.
// cmd/migrate applies extensions, bronze, silver, gold, ingest, and config in order.
package migrations

import "embed"

// FS holds only the SQL files needed to apply the corpus migrations.
//
//go:embed extensions/*.sql bronze/*.sql silver/*.sql gold/*.sql ingest/*.sql config/*.sql
var FS embed.FS
