// Command migragen generates per-schema migration files using Atlas CLI.
//
// It runs atlas migrate diff for each of banhmi's five PG schemas
// (bronze, silver, gold, ingest, config), reading sql/{schema}/schema.sql
// as the desired state. The vector extension is NOT managed by Atlas — it
// lives in a hand-written migration (deploy/migrations/extensions/).
//
// Usage:
//
//	go run tools/migragen/main.go          # all schemas
//	go run tools/migragen/main.go -name update  # force name suffix
//
// The tool connects to Postgres using the same env + config as the main app,
// creates a temporary database (banhmi_atlasdev) for Atlas's dev-url, runs
// atlas migrate diff for each schema, then drops the temp database.
package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"

	"danny.vn/banhmi/pkg/base/config"
)

// schemaOrder is the order in which schemas are processed (also the apply order).
var schemaOrder = []string{"bronze", "silver", "gold", "ingest", "config"}

func main() {
	migName := flag.String("name", "", "migration name suffix (default: 'init' for new dirs, 'update' for existing)")
	flag.Parse()

	if err := run(*migName); err != nil {
		slog.Error("migragen failed", "error", err)
		os.Exit(1)
	}
}

func run(migName string) error {
	// Verify atlas is installed.
	if _, err := exec.LookPath("atlas"); err != nil {
		return errors.New("atlas CLI not found — install from https://atlasgo.io")
	}

	cfg, err := config.Load("config/config.yaml")
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	devDBName := cfg.Database.DBName + "_atlasdev"
	devURL, cleanup, err := createDevDB(cfg, devDBName)
	if err != nil {
		return fmt.Errorf("create dev database: %w", err)
	}
	defer cleanup()

	ctx := context.Background()

	var generated, unchanged, failed int
	for _, pgSchema := range schemaOrder {
		schemaPath := filepath.Join("sql", pgSchema, "schema.sql")
		migDir := filepath.Join("deploy", "migrations", pgSchema)

		name := migName
		if name == "" {
			name = "init"
			if hasSQLFiles(migDir) {
				name = "update"
			}
		}

		created, err := runAtlasDiff(ctx, migDir, schemaPath, pgSchema, name, devURL)
		if err != nil {
			slog.Error("failed", "schema", pgSchema, "error", err)
			failed++
			continue
		}
		if created {
			generated++
			slog.Info("generated", "schema", pgSchema, "name", name)
		} else {
			unchanged++
			slog.Info("no changes", "schema", pgSchema)
		}
	}

	slog.Info("migragen complete",
		"generated", generated,
		"unchanged", unchanged,
		"failed", failed,
	)
	if failed > 0 {
		return fmt.Errorf("%d schema(s) failed", failed)
	}
	return nil
}

// createDevDB creates a temporary Postgres database for Atlas's dev-url.
// Returns the postgres:// URL, a cleanup function, and any error.
func createDevDB(cfg *config.Config, devDBName string) (string, func(), error) {
	adminURL := postgresURL(cfg, cfg.Database.DBName)
	adminDB, err := sql.Open("pgx", adminURL)
	if err != nil {
		return "", nil, fmt.Errorf("open admin connection: %w", err)
	}

	ctx := context.Background()

	// Drop if exists (leftover from a previous crash), then create.
	_, _ = adminDB.ExecContext(ctx, "DROP DATABASE IF EXISTS "+devDBName)
	if _, err := adminDB.ExecContext(ctx, "CREATE DATABASE "+devDBName); err != nil {
		_ = adminDB.Close()
		return "", nil, fmt.Errorf("create database %s: %w", devDBName, err)
	}

	// Connect to the dev DB to clean up ParadeDB extension schemas that the
	// ParadeDB image injects into every new database via template1. Atlas
	// requires a "clean" dev DB (only `public` schema) so we must remove them
	// before the first diff. We also install `vector` so Atlas can resolve the
	// vector(1024) column type. pg_search is NOT installed here — it would
	// recreate the `paradedb` schema and break the clean-check again.
	devURL := postgresURL(cfg, devDBName)
	devDB, err := sql.Open("pgx", devURL)
	if err != nil {
		_, _ = adminDB.ExecContext(ctx, "DROP DATABASE IF EXISTS "+devDBName)
		_ = adminDB.Close()
		return "", nil, fmt.Errorf("open dev database: %w", err)
	}
	if err := cleanDevDB(ctx, devDB); err != nil {
		_ = devDB.Close()
		_, _ = adminDB.ExecContext(ctx, "DROP DATABASE IF EXISTS "+devDBName)
		_ = adminDB.Close()
		return "", nil, fmt.Errorf("clean dev database: %w", err)
	}
	_ = devDB.Close()

	slog.Info("created dev database", "name", devDBName)

	cleanup := func() {
		_, _ = adminDB.ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+devDBName)
		_ = adminDB.Close()
		slog.Info("dropped dev database", "name", devDBName)
	}

	return devURL, cleanup, nil
}

// cleanDevDB removes ParadeDB extension schemas that the ParadeDB image injects
// into every new database via template1, and installs the `vector` extension so
// Atlas can resolve vector(N) column types. pg_search is intentionally NOT
// installed because it recreates `paradedb` schema — the BM25 index in gold
// is wrapped in a guarded DO block that no-ops when the extension is absent.
//
// Schemas removed: paradedb, pdb, pgivm, tiger, topology (all ParadeDB/postgis
// artifacts). Extensions dropped before schema removal: pg_search, pg_ivm,
// postgis and its dependents.
func cleanDevDB(ctx context.Context, db *sql.DB) error {
	// Drop extensions that pin the extra schemas (best-effort; ignore "does not exist").
	for _, ext := range []string{"pg_search", "pg_ivm", "postgis_tiger_geocoder", "postgis_topology", "postgis"} {
		_, _ = db.ExecContext(ctx, "DROP EXTENSION IF EXISTS "+ext+" CASCADE")
	}

	// Drop schemas that Atlas's clean-check rejects.
	for _, schema := range []string{"paradedb", "pdb", "pgivm", "tiger", "topology"} {
		_, _ = db.ExecContext(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
	}

	// Install vector so Atlas can plan CREATE TABLE ... vector(1024) columns.
	if _, err := db.ExecContext(ctx, "CREATE EXTENSION IF NOT EXISTS vector"); err != nil {
		return fmt.Errorf("install vector extension: %w", err)
	}

	// Move vector's type objects into pg_catalog so that Atlas's schema-scoped
	// replay sessions (which set search_path = <schema> only) can resolve the
	// "vector" type without needing "public" in the search_path. pg_catalog is
	// always included implicitly by Postgres, so this makes vector universally
	// accessible. This is safe for the ephemeral dev DB; the production database
	// always installs vector in public via the hand-written extensions migration.
	if _, err := db.ExecContext(ctx, "ALTER EXTENSION vector SET SCHEMA pg_catalog"); err != nil {
		return fmt.Errorf("move vector to pg_catalog: %w", err)
	}

	return nil
}

// postgresURL builds a postgres:// URL for the given database using cfg credentials.
func postgresURL(cfg *config.Config, dbName string) string {
	db := cfg.Database
	sslmode := db.SSLMode
	if sslmode == "" {
		sslmode = "disable"
	}
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=%s",
		db.User, db.Password, db.Host, db.Port, dbName, sslmode)
}

// hasSQLFiles returns true if dir contains at least one .sql file.
func hasSQLFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			return true
		}
	}
	return false
}

func countSQLFiles(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			n++
		}
	}
	return n
}

// runAtlasDiff runs atlas migrate diff for one PG schema.
// Returns true if a new migration file was created.
func runAtlasDiff(ctx context.Context, migDir, schemaPath, pgSchema, diffName, devURL string) (bool, error) {
	if err := os.MkdirAll(migDir, 0755); err != nil {
		return false, fmt.Errorf("mkdir %s: %w", migDir, err)
	}

	// Remove stale atlas.sum when no .sql files exist (Atlas rejects orphaned sums).
	sumPath := filepath.Join(migDir, "atlas.sum")
	if !hasSQLFiles(migDir) {
		_ = os.Remove(sumPath)
	}

	// Rehash before diffing so hand-edited files don't break the run.
	if hasSQLFiles(migDir) {
		cmd := exec.CommandContext(ctx, "atlas", "migrate", "hash",
			"--dir", "file://"+migDir,
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			return false, fmt.Errorf("hash %s: %s: %w", migDir, strings.TrimSpace(string(out)), err)
		}
	}

	beforeCount := countSQLFiles(migDir)

	cmd := exec.CommandContext(ctx, "atlas", "migrate", "diff", diffName,
		"--dir", "file://"+migDir,
		"--to", "file://"+schemaPath,
		"--dev-url", devURL,
		"--schema", pgSchema,
	)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := stderr.String()
		if strings.Contains(errMsg, "is synced") {
			return false, nil
		}
		return false, fmt.Errorf("%s: %w", strings.TrimSpace(errMsg), err)
	}

	afterCount := countSQLFiles(migDir)
	if afterCount <= beforeCount {
		return false, nil
	}

	// Post-process: add goose headers, fix CREATE SCHEMA, strip DROP SCHEMA for
	// sibling schemas, remove empty files, rename timestamps to sequential names.
	if err := postProcess(ctx, migDir, pgSchema); err != nil {
		return false, err
	}

	finalCount := countSQLFiles(migDir)
	return finalCount > beforeCount, nil
}

// postProcess applies all post-processing to newly generated migration files.
func postProcess(ctx context.Context, migDir, pgSchema string) error {
	entries, err := os.ReadDir(migDir)
	if err != nil {
		return err
	}

	modified := false
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		path := filepath.Join(migDir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		result := processFile(string(data), pgSchema)

		// Remove empty files (Atlas sometimes emits empty diffs). Strip the
		// goose header before the emptiness check so "-- +goose Up\n" files
		// are also removed.
		stripped := strings.TrimSpace(strings.ReplaceAll(result, "-- +goose Up", ""))
		stripped = strings.TrimSpace(strings.ReplaceAll(stripped, "-- +goose Down", ""))
		if stripped == "" {
			_ = os.Remove(path)
			modified = true
			continue
		}

		if result != string(data) {
			if err := os.WriteFile(path, []byte(result), 0644); err != nil {
				return err
			}
			modified = true
		}
	}

	if modified {
		cmd := exec.CommandContext(ctx, "atlas", "migrate", "hash", "--dir", "file://"+migDir)
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return err
		}
	}

	return renameToSequential(ctx, migDir)
}

// processFile transforms a single Atlas-generated SQL file:
//  1. Strips DROP SCHEMA for schemas other than pgSchema.
//  2. Makes CREATE SCHEMA idempotent (IF NOT EXISTS).
//  3. Strips the "public." qualifier from extension types (e.g. public.vector → vector)
//     so Atlas can replay the file without needing public in its search_path.
//  4. Prepends "-- +goose Up" if absent.
func processFile(content, pgSchema string) string {
	var lines []string
	skipNext := false // used to drop the Atlas comment above a DROP SCHEMA

	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)

		// Atlas emits `-- Drop schema named "X"` before `DROP SCHEMA "X" CASCADE;`
		if isDropSchemaComment(trimmed) {
			schema := extractSchemaFromComment(trimmed)
			if schema != pgSchema {
				skipNext = true
				continue
			}
		}

		if skipNext {
			skipNext = false
			if isDropSchema(trimmed) {
				continue // drop the DROP SCHEMA for sibling schemas
			}
		}

		// Strip DROP SCHEMA for schemas other than pgSchema.
		if isDropSchema(trimmed) {
			schema := extractSchemaFromDrop(trimmed)
			if schema != pgSchema {
				continue
			}
		}

		// Make CREATE SCHEMA idempotent.
		if strings.HasPrefix(trimmed, "CREATE SCHEMA \"") && !strings.Contains(trimmed, "IF NOT EXISTS") {
			line = strings.Replace(line, "CREATE SCHEMA \"", `CREATE SCHEMA IF NOT EXISTS "`, 1)
		}

		lines = append(lines, line)
	}

	result := strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n"

	// Strip the "public." qualifier from extension types so Atlas can replay
	// the file without needing public in its session search_path. This handles
	// pgvector's "public.vector" type and any future extension types.
	result = strings.ReplaceAll(result, `"public".`, "")
	result = strings.ReplaceAll(result, "public.vector(", "vector(")
	result = strings.ReplaceAll(result, "public.vector)", "vector)")

	if !strings.Contains(result, "-- +goose Up") {
		result = "-- +goose Up\n" + result
	}

	return result
}

func isDropSchemaComment(line string) bool {
	return strings.HasPrefix(line, `-- Drop schema named "`)
}

func extractSchemaFromComment(line string) string {
	// `-- Drop schema named "bronze"`
	line = strings.TrimPrefix(line, `-- Drop schema named "`)
	line = strings.TrimSuffix(line, `"`)
	return line
}

func isDropSchema(line string) bool {
	return strings.HasPrefix(line, `DROP SCHEMA "`) && strings.Contains(line, "CASCADE")
}

func extractSchemaFromDrop(line string) string {
	// `DROP SCHEMA "bronze" CASCADE;`
	line = strings.TrimPrefix(line, `DROP SCHEMA "`)
	if idx := strings.Index(line, `"`); idx >= 0 {
		return line[:idx]
	}
	return ""
}

// timestampRe matches Atlas's default timestamp-named files (20260406145153_init.sql).
const timestampLen = 14

func renameToSequential(ctx context.Context, migDir string) error {
	entries, err := os.ReadDir(migDir)
	if err != nil {
		return err
	}

	type sqlFile struct {
		name string
		idx  int
	}
	var files []sqlFile
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, sqlFile{name: e.Name(), idx: len(files) + 1})
		}
	}

	renamed := false
	for _, f := range files {
		// Check if the filename starts with a 14-digit timestamp.
		if len(f.name) <= timestampLen+1 || !isDigits(f.name[:timestampLen]) || f.name[timestampLen] != '_' {
			continue // already sequential or different format
		}
		rest := f.name[timestampLen+1:] // "init.sql"
		newName := fmt.Sprintf("%05d_%s", f.idx, rest)
		if newName == f.name {
			continue
		}
		if err := os.Rename(
			filepath.Join(migDir, f.name),
			filepath.Join(migDir, newName),
		); err != nil {
			return fmt.Errorf("rename %s → %s: %w", f.name, newName, err)
		}
		renamed = true
	}

	if renamed {
		cmd := exec.CommandContext(ctx, "atlas", "migrate", "hash", "--dir", "file://"+migDir)
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
	return nil
}

func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
