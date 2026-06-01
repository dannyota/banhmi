// Package seed embeds banhmi's default config CSVs, loaded by cmd/seed into the
// config schema. Edit a CSV and re-run cmd/seed to refresh the seeded defaults;
// operator customizations (origin='user' rows) are preserved.
package seed

import "embed"

// FS holds the default config CSVs (scope_term, issuer_code, discovery_keyword).
//
//go:embed *.csv
var FS embed.FS
