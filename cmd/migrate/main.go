// Command migrate applies banhmi's database migrations with goose and verifies
// atlas.sum checksums. Migration SQL is embedded, so the binary needs no
// external files at runtime.
//
// Usage:
//
//	migrate            # apply all pending migrations (default: up)
//	migrate -check     # verify atlas.sum checksums without applying
//
// Layout applied in order:
//
//	extensions/   — extensions (vector, pg_search) — goose version table: public.goose_db_version
//	bronze/       — bronze schema tables            — goose version table: public.goose_db_version_bronze
//	silver/       — silver schema tables            — goose version table: public.goose_db_version_silver
//	gold/         — gold schema tables              — goose version table: public.goose_db_version_gold
//	gold_bm25/    — hand-written BM25 index on gold — goose version table: public.goose_db_version_gold_bm25
//	ingest/       — ingest schema tables            — goose version table: public.goose_db_version_ingest
//	config/       — config schema tables            — goose version table: public.goose_db_version_config
package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strings"

	"github.com/pressly/goose/v3"

	_ "github.com/jackc/pgx/v5/stdlib"

	"danny.vn/banhmi/deploy/migrations"
	"danny.vn/banhmi/pkg/base/config"
	blog "danny.vn/banhmi/pkg/base/log"
)

// schemaOrder defines the order migration directories are applied.
// extensions must come first (creates vector/pg_search extensions that gold
// depends on). Then bronze → silver → gold → gold_bm25 → gold_fts → ingest →
// config. gold_bm25 (ParadeDB BM25) and gold_fts (native Postgres FTS) are
// hand-written lexical indexes on gold.chunk; they apply after gold because they
// index a gold table (Atlas cannot diff the bm25 access method nor a custom
// text-search configuration, so they are not part of the Atlas-generated gold dir).
var dirOrder = []struct {
	dir        string // subdirectory under deploy/migrations/
	gooseTable string // goose version table name in public schema
}{
	{"extensions", "goose_db_version"},
	{"bronze", "goose_db_version_bronze"},
	{"silver", "goose_db_version_silver"},
	{"gold", "goose_db_version_gold"},
	{"gold_bm25", "goose_db_version_gold_bm25"},
	{"gold_fts", "goose_db_version_gold_fts"},
	{"ingest", "goose_db_version_ingest"},
	{"config", "goose_db_version_config"},
}

func main() {
	cfgPath := flag.String("config", "config/config.yaml", "path to config file")
	check := flag.Bool("check", false, "verify atlas.sum checksums without applying migrations")
	flag.Parse()

	log := blog.New(os.Getenv("BANHMI_LOG_LEVEL"))

	if *check {
		if err := checkChecksums(log); err != nil {
			log.Error("checksum verification failed", "err", err)
			os.Exit(1)
		}
		log.Info("all atlas.sum checksums verified")
		return
	}

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

	// Verify all atlas.sum checksums before touching the database.
	if err := checkChecksums(log); err != nil {
		return fmt.Errorf("pre-apply checksum check: %w", err)
	}

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

// checkChecksums verifies atlas.sum for each Atlas-managed directory.
// The extensions dir is hand-written and also has an atlas.sum, so we verify all.
func checkChecksums(log *slog.Logger) error {
	var checked int
	for _, entry := range dirOrder {
		sumData, err := fs.ReadFile(migrations.FS, entry.dir+"/atlas.sum")
		if err != nil {
			return fmt.Errorf("missing atlas.sum for %s: %w", entry.dir, err)
		}

		sum, err := parseAtlasSum(string(sumData))
		if err != nil {
			return fmt.Errorf("parse %s/atlas.sum: %w", entry.dir, err)
		}

		actualDirHash, err := verifyFileHashes(entry.dir, sum)
		if err != nil {
			return err
		}
		if actualDirHash != sum.DirHash {
			return fmt.Errorf("directory checksum mismatch for %s: expected %s, got %s",
				entry.dir, sum.DirHash, actualDirHash)
		}
		checked += len(sum.Files)
		log.Info("verified", "dir", entry.dir, "files", len(sum.Files))
	}
	slog.Info("checksums verified", "files", checked)
	return nil
}

// atlasSum holds the parsed content of an atlas.sum file.
type atlasSum struct {
	DirHash string
	Files   []atlasSumFile
}

type atlasSumFile struct {
	Name string
	Hash string
}

// parseAtlasSum parses an atlas.sum file.
//
// Format:
//
//	h1:<directory_hash>
//	filename.sql h1:<file_hash>
func parseAtlasSum(content string) (atlasSum, error) {
	var sum atlasSum
	lines := strings.Split(strings.TrimSpace(content), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		return sum, errors.New("empty atlas.sum")
	}
	sum.DirHash = strings.TrimSpace(lines[0])
	if !strings.HasPrefix(sum.DirHash, "h1:") {
		return sum, fmt.Errorf("invalid directory hash %q", sum.DirHash)
	}
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			return sum, fmt.Errorf("invalid file checksum line %q", line)
		}
		hash := strings.TrimSpace(parts[1])
		if !strings.HasPrefix(hash, "h1:") {
			return sum, fmt.Errorf("invalid file hash %q", hash)
		}
		sum.Files = append(sum.Files, atlasSumFile{
			Name: strings.TrimSpace(parts[0]),
			Hash: hash,
		})
	}
	return sum, nil
}

// verifyFileHashes checks each file's hash and returns the computed directory hash.
func verifyFileHashes(dir string, sum atlasSum) (string, error) {
	fileHash := sha256.New()
	for _, file := range sum.Files {
		data, err := fs.ReadFile(migrations.FS, dir+"/"+file.Name)
		if err != nil {
			return "", fmt.Errorf("read %s/%s: %w", dir, file.Name, err)
		}
		_, _ = fileHash.Write([]byte(file.Name))
		_, _ = fileHash.Write(data)
		actualHash := "h1:" + base64.StdEncoding.EncodeToString(fileHash.Sum(nil))
		if actualHash != file.Hash {
			return "", fmt.Errorf("checksum mismatch for %s/%s: expected %s, got %s",
				dir, file.Name, file.Hash, actualHash)
		}
	}

	dirHash := sha256.New()
	for _, file := range sum.Files {
		_, _ = dirHash.Write([]byte(file.Name))
		_, _ = dirHash.Write([]byte(strings.TrimPrefix(file.Hash, "h1:")))
	}
	return "h1:" + base64.StdEncoding.EncodeToString(dirHash.Sum(nil)), nil
}

// gooseLogger adapts goose's Printf/Fatalf logger to stdout/stderr.
type gooseLogger struct{}

func (gooseLogger) Printf(format string, v ...any) { _, _ = fmt.Fprintf(os.Stdout, format, v...) }
func (gooseLogger) Fatalf(format string, v ...any) { _, _ = fmt.Fprintf(os.Stderr, format, v...) }
