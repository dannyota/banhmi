// Command auth-cleanup removes expired authentication rows in bounded batches.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	dbauth "danny.vn/banhmi/pkg/store/auth"
)

const migrationDatabaseURLEnv = "BANHMI_AUTH_MIGRATION_DATABASE_URL"

func main() {
	batchSize := flag.Int("batch", 1000, "maximum rows of each class to delete")
	flag.Parse()
	if err := run(context.Background(), int32(*batchSize), time.Now()); err != nil {
		slog.Error("auth cleanup failed", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, batchSize int32, now time.Time) error {
	if batchSize < 1 || batchSize > 10000 {
		return fmt.Errorf("batch must be between 1 and 10000")
	}
	databaseURL := strings.TrimSpace(os.Getenv(migrationDatabaseURLEnv))
	if databaseURL == "" {
		return fmt.Errorf("%s is required", migrationDatabaseURLEnv)
	}

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("open authentication database: %w", err)
	}
	defer pool.Close()
	return cleanup(ctx, pool, batchSize, now)
}

func cleanup(ctx context.Context, pool *pgxpool.Pool, batchSize int32, now time.Time) error {
	queries := dbauth.New(pool)

	if _, err := queries.DeleteExpiredLoginTransactions(ctx, dbauth.DeleteExpiredLoginTransactionsParams{
		Now: now, BatchSize: batchSize,
	}); err != nil {
		return fmt.Errorf("delete login transactions: %w", err)
	}
	if _, err := queries.DeleteExpiredPendingConsents(ctx, dbauth.DeleteExpiredPendingConsentsParams{
		Now: now, BatchSize: batchSize,
	}); err != nil {
		return fmt.Errorf("delete pending consents: %w", err)
	}
	if _, err := queries.DeleteExpiredWebSessions(ctx, dbauth.DeleteExpiredWebSessionsParams{
		Now: now, BatchSize: batchSize,
	}); err != nil {
		return fmt.Errorf("delete web sessions: %w", err)
	}

	candidates, err := queries.ListExpiredOAuthGrantCandidates(
		ctx,
		dbauth.ListExpiredOAuthGrantCandidatesParams{Now: now, BatchSize: batchSize},
	)
	if err != nil {
		return fmt.Errorf("list expired OAuth grants: %w", err)
	}
	for _, candidate := range candidates {
		if err := deleteGrantFamily(ctx, pool, candidate.ID, candidate.AccountID, now); err != nil {
			return err
		}
	}
	return nil
}

func deleteGrantFamily(
	ctx context.Context,
	pool *pgxpool.Pool,
	grantID int64,
	accountID int64,
	now time.Time,
) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin OAuth grant cleanup: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbauth.New(pool).WithTx(tx)

	if _, err := queries.LockAccountForCleanup(ctx, accountID); errors.Is(err, pgx.ErrNoRows) {
		return nil
	} else if err != nil {
		return fmt.Errorf("lock account for cleanup: %w", err)
	}
	if _, err := queries.LockOAuthGrantForCleanup(ctx, dbauth.LockOAuthGrantForCleanupParams{
		GrantID: grantID,
		Now:     now,
	}); errors.Is(err, pgx.ErrNoRows) {
		return nil
	} else if err != nil {
		return fmt.Errorf("lock OAuth grant for cleanup: %w", err)
	}
	if err := queries.DeleteOAuthSessionsForGrant(ctx, grantID); err != nil {
		return fmt.Errorf("delete OAuth sessions: %w", err)
	}
	if _, err := queries.DeleteOAuthGrant(ctx, grantID); err != nil {
		return fmt.Errorf("delete OAuth grant: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit OAuth grant cleanup: %w", err)
	}
	return nil
}

