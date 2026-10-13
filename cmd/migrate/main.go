// Command migrate applies the embedded corpus migrations with Goose.
//
// Usage:
//
//	migrate            # apply all pending migrations (default: up)
//
// Layout applied in order:
//
//	extensions/   - extensions (vector)            - goose version table: public.goose_db_version
//	bronze/       - bronze schema tables            - goose version table: public.goose_db_version_bronze
//	silver/       - silver schema tables            - goose version table: public.goose_db_version_silver
//	gold/         - gold schema tables              - goose version table: public.goose_db_version_gold
//	ingest/       - ingest schema tables            - goose version table: public.goose_db_version_ingest
//	config/       - config schema tables            - goose version table: public.goose_db_version_config
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"os"

	"github.com/pressly/goose/v3"

	_ "github.com/jackc/pgx/v5/stdlib"

	"danny.vn/banhmi/deploy/migrations"
	"danny.vn/banhmi/pkg/base/config"
	blog "danny.vn/banhmi/pkg/base/log"
)

// dirOrder defines the order migration directories are applied.
// extensions must come first (creates the vector extension that gold depends on).
// Bronze, silver, gold, ingest, and config follow. The lexical arm uses
// native pgvector sparsevec BM25 on managed RDS.
var dirOrder = []struct {
	dir        string // subdirectory under deploy/migrations/
	gooseTable string // goose version table name in public schema
}{
	{"extensions", "goose_db_version"},
	{"bronze", "goose_db_version_bronze"},
	{"silver", "goose_db_version_silver"},
	{"gold", "goose_db_version_gold"},
	{"ingest", "goose_db_version_ingest"},
	{"config", "goose_db_version_config"},
}

func main() {
	cfgPath := flag.String("config", "config/config.yaml", "path to config file")
	flag.Parse()

	log := blog.New(os.Getenv("BANHMI_LOG_LEVEL"))

	if err := run(*cfgPath, log); err != nil {
		log.Error("migrate", "err", err)
		os.Exit(1)
	}
}

func run(cfgPath string, log *slog.Logger) error {
	ctx := context.Background()

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	log.Info("banhmi migrate", "db", cfg.Database.Redacted())

	db, err := sql.Open("pgx", cfg.Database.DSN())
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = db.Close() }()

	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}

	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set dialect: %w", err)
	}
	goose.SetLogger(gooseLogger{})

	for _, entry := range dirOrder {
		sub, err := fs.Sub(migrations.FS, entry.dir)
		if err != nil {
			return fmt.Errorf("sub FS for %s: %w", entry.dir, err)
		}

		goose.SetBaseFS(sub)
		goose.SetTableName(entry.gooseTable)

		if err := goose.UpContext(ctx, db, "."); err != nil {
			return fmt.Errorf("apply %s migrations: %w", entry.dir, err)
		}
		log.Info("applied", "dir", entry.dir)
	}

	log.Info("migrate done")
	return nil
}

// gooseLogger adapts goose's Printf/Fatalf logger to stdout/stderr.
type gooseLogger struct{}

func (gooseLogger) Printf(format string, v ...any) { _, _ = fmt.Fprintf(os.Stdout, format, v...) }
func (gooseLogger) Fatalf(format string, v ...any) { _, _ = fmt.Fprintf(os.Stderr, format, v...) }
