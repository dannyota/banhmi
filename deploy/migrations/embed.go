// Package migrations embeds banhmi's goose SQL migrations so the migrate command
// can apply them with no external files at runtime.
//
// Layout:
//
//	extensions/  — hand-written extensions migration (vector, pg_search)
//	bronze/      — Atlas-generated: bronze schema tables
//	silver/      — Atlas-generated: silver schema tables
//	gold/        — Atlas-generated: gold schema tables
//	gold_bm25/   — hand-written: ParadeDB BM25 index on gold.chunk
//	ingest/      — Atlas-generated: ingest schema tables
//	config/      — Atlas-generated: config schema tables
//
// Each subdirectory contains goose .sql files and an atlas.sum checksum file.
// cmd/migrate applies them in the order above and verifies atlas.sum for each
// Atlas-managed directory before applying.
package migrations

import "embed"

// FS holds every embedded migration file. goose reads .sql files;
// atlas.sum files are read by cmd/migrate for checksum verification.
//
//go:embed extensions/*.sql extensions/atlas.sum bronze/*.sql bronze/atlas.sum silver/*.sql silver/atlas.sum gold/*.sql gold/atlas.sum gold_bm25/*.sql gold_bm25/atlas.sum gold_fts/*.sql gold_fts/atlas.sum ingest/*.sql ingest/atlas.sum config/*.sql config/atlas.sum
var FS embed.FS
