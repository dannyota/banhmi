// Package authmigrations embeds only the dedicated authentication migrations.
package authmigrations

import "embed"

// FS holds the append-only Goose SQL chain. Bootstrap SQL stays separate.
//
//go:embed *.sql
var FS embed.FS
