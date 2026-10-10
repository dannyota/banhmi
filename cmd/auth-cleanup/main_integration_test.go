package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"

	_ "github.com/jackc/pgx/v5/stdlib"

	"danny.vn/banhmi/deploy/authmigrations"
)

func TestCleanupIsBoundedAndPreservesAudit(t *testing.T) {
	pool := cleanupTestPool(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	unique := fmt.Sprintf("%d", now.UnixNano())

	for _, suffix := range []string{"one", "two"} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO auth.login_transaction
			    (state_hash, browser_hash, nonce_hash, pkce_verifier_ciphertext,
			     return_path, created_at, expires_at)
			VALUES ($1, $2, $3, $4, '/app', $5, $6)`,
			[]byte("state-"+suffix+unique), []byte("browser-"+suffix+unique),
			[]byte("nonce-"+suffix+unique), []byte("ciphertext"), now.Add(-2*time.Hour), now.Add(-time.Hour),
		); err != nil {
			t.Fatalf("insert expired login transaction error = %v", err)
		}
	}

	var accountID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO auth.user_account
		    (issuer, subject, email, first_seen_at, last_login_at)
		VALUES ('https://accounts.google.com', $1, $2, $3, $3)
		RETURNING id`, "cleanup-"+unique, "cleanup-"+unique+"@example.com", now.Add(-2*time.Hour),
	).Scan(&accountID); err != nil {
		t.Fatalf("insert cleanup account error = %v", err)
	}

	var expiredGrantID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO auth.oauth_grant
		    (request_id, account_id, account_epoch, client_id, resource, created_at,
		     revoked_at, revocation_reason, retain_until)
		VALUES ($1, $2, 1, 'client', 'https://evidence.example/mcp', $3,
		        $4, 'refresh_replay', $5)
		RETURNING id`, "expired-"+unique, accountID, now.Add(-2*time.Hour), now.Add(-time.Hour),
		now.Add(-time.Hour),
	).Scan(&expiredGrantID); err != nil {
		t.Fatalf("insert expired grant error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO auth.oauth_session
		    (grant_id, kind, signature, created_at, expires_at, request_payload)
		VALUES ($1, 'access_token', $2, $3, $4, '{}'::jsonb)`,
		expiredGrantID, "expired-signature-"+unique, now.Add(-2*time.Hour), now.Add(-time.Hour),
	); err != nil {
		t.Fatalf("insert expired OAuth session error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO auth.oauth_security_event
		    (grant_id, account_id, request_id, event_kind, occurred_at)
		VALUES ($1, $2, $3, 'refresh_replay', $4)`,
		expiredGrantID, accountID, "expired-"+unique, now.Add(-time.Hour),
	); err != nil {
		t.Fatalf("insert OAuth audit event error = %v", err)
	}

	var liveGrantID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO auth.oauth_grant
		    (request_id, account_id, account_epoch, client_id, resource, created_at, retain_until)
		VALUES ($1, $2, 1, 'client', 'https://evidence.example/mcp', $3, $4)
		RETURNING id`, "live-"+unique, accountID, now, now.Add(time.Hour),
	).Scan(&liveGrantID); err != nil {
		t.Fatalf("insert live grant error = %v", err)
	}

	if err := cleanup(ctx, pool, 1, now); err != nil {
		t.Fatalf("cleanup() error = %v", err)
	}
	assertCount(t, pool, "SELECT count(*) FROM auth.login_transaction WHERE state_hash IN ($1, $2)", 1,
		[]byte("state-one"+unique), []byte("state-two"+unique))
	assertCount(t, pool, "SELECT count(*) FROM auth.oauth_grant WHERE id = $1", 0, expiredGrantID)
	assertCount(t, pool, "SELECT count(*) FROM auth.oauth_session WHERE grant_id = $1", 0, expiredGrantID)
	assertCount(t, pool, "SELECT count(*) FROM auth.oauth_security_event WHERE grant_id = $1", 1, expiredGrantID)
	assertCount(t, pool, "SELECT count(*) FROM auth.oauth_grant WHERE id = $1", 1, liveGrantID)
}

func cleanupTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("auth database integration is disabled in short mode")
	}
	databaseURL := strings.TrimSpace(os.Getenv("BANHMI_AUTH_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("BANHMI_AUTH_TEST_DATABASE_URL is not set")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("goose.SetDialect() error = %v", err)
	}
	goose.SetBaseFS(authmigrations.FS)
	goose.SetTableName("goose_db_version_auth")
	if err := goose.UpContext(context.Background(), db, "."); err != nil {
		t.Fatalf("goose.UpContext() error = %v", err)
	}
	pool, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		t.Fatalf("pgxpool.New() error = %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func assertCount(t *testing.T, pool *pgxpool.Pool, query string, want int, arguments ...any) {
	t.Helper()
	var got int
	if err := pool.QueryRow(context.Background(), query, arguments...).Scan(&got); err != nil {
		t.Fatalf("count query error = %v", err)
	}
	if got != want {
		t.Fatalf("count = %d, want %d", got, want)
	}
}

