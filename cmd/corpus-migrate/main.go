// Command corpus-migrate applies exactly one fresh corpus chain.
package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"danny.vn/banhmi/deploy/corpusmigrations"
)

const migrationDatabaseURLEnv = "BANHMI_CORPUS_MIGRATION_DATABASE_URL"

// Goose's package configuration is process-global. The database lock additionally
// serializes independent processes choosing different chains on the same database.
var gooseMu sync.Mutex

type options struct {
	chain  string
	to     int64
	hasTo  bool
	status bool
}

type chainSpec struct {
	name      string
	table     string
	schemas   []string
	files     []string
	versions  []int64
	inventory []inventoryEntry
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		slog.Error("corpus migration failed", "err", err)
		os.Exit(1)
	}
}

func parseOptions(args []string) (options, error) {
	var opt options
	flags := flag.NewFlagSet("corpus-migrate", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&opt.chain, "chain", "", "writer, release, or control (required)")
	flags.Int64Var(&opt.to, "to", 0, "positive target version")
	flags.BoolVar(&opt.status, "status", false, "read-only migration status")
	if err := flags.Parse(args); err != nil {
		return opt, fmt.Errorf("parse flags: %w", err)
	}
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "to" {
			opt.hasTo = true
		}
	})
	if flags.NArg() != 0 {
		return opt, fmt.Errorf("positional arguments are not supported")
	}
	if opt.chain != "writer" && opt.chain != "release" && opt.chain != "control" {
		return opt, fmt.Errorf("-chain must be writer, release, or control")
	}
	if opt.hasTo && (opt.to <= 0 || opt.status) {
		return opt, fmt.Errorf("-to must be positive and cannot accompany -status")
	}
	return opt, nil
}

func loadChain(name string) (chainSpec, error) {
	c := chainSpec{name: name, table: "goose_db_version_corpus_" + name}
	switch name {
	case "writer":
		c.schemas = []string{"ingest", "bronze", "silver", "gold", "config"}
	case "release":
		c.schemas = []string{"silver", "gold", "config", "release"}
	case "control":
		c.schemas = []string{"control"}
	default:
		return c, fmt.Errorf("unknown chain")
	}
	files, err := fs.ReadDir(corpusmigrations.FS, name)
	if err != nil {
		return c, fmt.Errorf("read chain: %w", err)
	}
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".sql") {
			return c, fmt.Errorf("invalid chain file")
		}
		prefix, _, ok := strings.Cut(file.Name(), "_")
		version, err := strconv.ParseInt(prefix, 10, 64)
		if !ok || err != nil || version != int64(len(c.versions)+1) {
			return c, fmt.Errorf("chain versions must be contiguous from one")
		}
		c.files = append(c.files, file.Name())
		c.versions = append(c.versions, version)
	}
	if len(c.files) == 0 {
		return c, fmt.Errorf("empty chain")
	}
	c.inventory, err = loadInventory(c)
	return c, err
}

func run(ctx context.Context, args []string, out io.Writer) error {
	opt, err := parseOptions(args)
	if err != nil {
		return err
	}
	c, err := loadChain(opt.chain)
	if err != nil {
		return err
	}
	if opt.hasTo && opt.to > int64(len(c.versions)) {
		return fmt.Errorf("target is not in embedded chain")
	}
	url := strings.TrimSpace(os.Getenv(migrationDatabaseURLEnv))
	if url == "" {
		return fmt.Errorf("%s is required", migrationDatabaseURLEnv)
	}
	db, err := sql.Open("pgx", url)
	if err != nil {
		return fmt.Errorf("open migration database")
	}
	defer func() { _ = db.Close() }()
	// Connection errors can contain the URL or authentication data.
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("connect to migration database")
	}
	return migrate(ctx, db, c, opt, out)
}

func migrate(ctx context.Context, db *sql.DB, c chainSpec, opt options, out io.Writer) (result error) {
	if opt.hasTo && (opt.to <= 0 || opt.to > int64(len(c.versions)) || opt.status) {
		return fmt.Errorf("invalid target version")
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("pin migration connection: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock(1129271888, 1)"); err != nil {
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		// Cancellation must not return a still-locked connection to the pool.
		var unlocked bool
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := conn.QueryRowContext(unlockCtx, "SELECT pg_advisory_unlock(1129271888, 1)").Scan(&unlocked)
		if err != nil || !unlocked {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			if result == nil {
				result = fmt.Errorf("release migration lock failed")
			}
		}
	}()
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return fmt.Errorf("begin preflight: %w", err)
	}
	version, err := preflight(ctx, tx, c, opt.status)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("preflight: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("finish preflight: %w", err)
	}
	if opt.hasTo && opt.to < version {
		return fmt.Errorf("target precedes current version")
	}
	if opt.status {
		for i, file := range c.files {
			state := "pending"
			if c.versions[i] <= version {
				state = "applied"
			}
			if _, err := fmt.Fprintf(out, "%s %s\n", file, state); err != nil {
				return err
			}
		}
		return nil
	}
	if version == 0 {
		// Goose cannot replay an existing empty history table. Initialize only an
		// already validated empty table, after preflight and under the same lock.
		var exists bool
		if err := conn.QueryRowContext(ctx, "SELECT to_regclass($1) IS NOT NULL", "public."+c.table).Scan(&exists); err != nil {
			return err
		}
		if exists {
			if _, err := conn.ExecContext(ctx, "INSERT INTO public."+c.table+" (version_id,is_applied) SELECT 0,true WHERE NOT EXISTS (SELECT 1 FROM public."+c.table+")"); err != nil {
				return fmt.Errorf("initialize empty history: %w", err)
			}
		}
	}
	gooseMu.Lock()
	defer gooseMu.Unlock()
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set dialect: %w", err)
	}
	sub, err := fs.Sub(corpusmigrations.FS, c.name)
	if err != nil {
		return fmt.Errorf("select chain: %w", err)
	}
	goose.SetBaseFS(sub)
	goose.SetTableName("public." + c.table)
	if opt.hasTo {
		err = goose.UpToContext(ctx, db, ".", opt.to)
	} else {
		err = goose.UpContext(ctx, db, ".")
	}
	if err != nil {
		return fmt.Errorf("apply chain: %w", err)
	}
	// Recheck the catalog and history while the same advisory lock is held.
	tx, err = conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return fmt.Errorf("begin final check: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	final, err := preflight(ctx, tx, c, false)
	if err != nil {
		return fmt.Errorf("final check: %w", err)
	}
	want := int64(len(c.versions))
	if opt.hasTo {
		want = opt.to
	}
	if final != want {
		return fmt.Errorf("final version %d, want %d", final, want)
	}
	return tx.Commit()
}
