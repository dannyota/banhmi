// Command auth-migrate applies migrations only to the dedicated authentication database.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/pressly/goose/v3"

	_ "github.com/jackc/pgx/v5/stdlib"

	"danny.vn/banhmi/deploy/authmigrations"
	"danny.vn/banhmi/pkg/base/db"
)

const migrationDatabaseURLEnv = "BANHMI_AUTH_MIGRATION_DATABASE_URL"

func main() {
	if err := run(context.Background()); err != nil {
		slog.Error("auth migration failed", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	databaseURL := strings.TrimSpace(os.Getenv(migrationDatabaseURLEnv))
	if databaseURL == "" {
		return fmt.Errorf("%s is required", migrationDatabaseURLEnv)
	}

	if err := db.ValidateConnectionURI(migrationDatabaseURLEnv, databaseURL); err != nil {
		return err
	}

	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("open authentication database: %w", err)
	}
	defer func() { _ = db.Close() }()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping authentication database: %w", err)
	}

	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set migration dialect: %w", err)
	}
	goose.SetBaseFS(authmigrations.FS)
	goose.SetTableName("goose_db_version_auth")
	if err := goose.UpContext(ctx, db, "."); err != nil {
		return fmt.Errorf("apply authentication migrations: %w", err)
	}
	return nil
}
