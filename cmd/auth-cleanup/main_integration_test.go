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
	for _, suffix := range []string{"one", "two"} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO auth.web_session
			    (session_hash, account_id, account_epoch, issued_at, expires_at)
			VALUES ($1, $2, 1, $3, $4)`,
			[]byte("expired-web-"+suffix+unique), accountID, now.Add(-2*time.Hour), now.Add(-time.Hour),
		); err != nil {
			t.Fatalf("insert expired web session error = %v", err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO auth.pending_consent
			    (handle_hash, request_ciphertext, created_at, expires_at)
			VALUES ($1, $2, $3, $4)`,
			[]byte("expired-consent-"+suffix+unique), []byte("ciphertext"),
			now.Add(-2*time.Hour), now.Add(-time.Hour),
		); err != nil {
			t.Fatalf("insert expired pending consent error = %v", err)
		}
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
	assertCount(t, pool, "SELECT count(*) FROM auth.pending_consent WHERE handle_hash IN ($1, $2)", 1,
		[]byte("expired-consent-one"+unique), []byte("expired-consent-two"+unique))
	assertCount(t, pool, "SELECT count(*) FROM auth.web_session WHERE session_hash IN ($1, $2)", 1,
		[]byte("expired-web-one"+unique), []byte("expired-web-two"+unique))
	assertCount(t, pool, "SELECT count(*) FROM auth.oauth_grant WHERE id = $1", 0, expiredGrantID)
	assertCount(t, pool, "SELECT count(*) FROM auth.oauth_session WHERE grant_id = $1", 0, expiredGrantID)
	assertCount(t, pool, "SELECT count(*) FROM auth.oauth_security_event WHERE grant_id = $1", 1, expiredGrantID)
	assertCount(t, pool, "SELECT count(*) FROM auth.oauth_grant WHERE id = $1", 1, liveGrantID)

	var lockedLoginID int64
	if err := pool.QueryRow(ctx, `
		SELECT id
		FROM auth.login_transaction
		WHERE state_hash IN ($1, $2)`,
		[]byte("state-one"+unique), []byte("state-two"+unique),
	).Scan(&lockedLoginID); err != nil {
		t.Fatalf("find remaining login transaction error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO auth.login_transaction
		    (state_hash, browser_hash, nonce_hash, pkce_verifier_ciphertext,
		     return_path, created_at, expires_at)
		VALUES ($1, $2, $3, $4, '/app', $5, $6)`,
		[]byte("state-unlocked"+unique), []byte("browser-unlocked"+unique),
		[]byte("nonce-unlocked"+unique), []byte("ciphertext"), now.Add(-2*time.Hour), now.Add(-time.Hour),
	); err != nil {
		t.Fatalf("insert unlocked login transaction error = %v", err)
	}
	lockTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin login lock error = %v", err)
	}
	defer func() { _ = lockTx.Rollback(ctx) }()
	if _, err := lockTx.Exec(ctx,
		"SELECT id FROM auth.login_transaction WHERE id = $1 FOR UPDATE", lockedLoginID,
	); err != nil {
		t.Fatalf("lock login transaction error = %v", err)
	}
	lockedCleanupContext, cancelLockedCleanup := context.WithTimeout(ctx, 2*time.Second)
	defer cancelLockedCleanup()
	if err := cleanup(lockedCleanupContext, pool, 1, now); err != nil {
		t.Fatalf("cleanup() with locked row error = %v", err)
	}
	assertCount(t, pool, "SELECT count(*) FROM auth.login_transaction WHERE id = $1", 1, lockedLoginID)
	assertCount(t, pool, "SELECT count(*) FROM auth.login_transaction WHERE state_hash = $1", 0,
		[]byte("state-unlocked"+unique))
	if err := lockTx.Rollback(ctx); err != nil {
		t.Fatalf("rollback login lock error = %v", err)
	}

	var recheckedGrantID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO auth.oauth_grant
		    (request_id, account_id, account_epoch, client_id, resource, created_at, retain_until)
		VALUES ($1, $2, 1, 'client', 'https://evidence.example/mcp', $3, $4)
		RETURNING id`, "rechecked-"+unique, accountID, now.Add(-2*time.Hour), now.Add(-time.Hour),
	).Scan(&recheckedGrantID); err != nil {
		t.Fatalf("insert rechecked grant error = %v", err)
	}
	if _, err := pool.Exec(ctx,
		"UPDATE auth.oauth_grant SET retain_until = $1 WHERE id = $2", now.Add(time.Hour), recheckedGrantID,
	); err != nil {
		t.Fatalf("extend grant retention error = %v", err)
	}
	if err := deleteGrantFamily(ctx, pool, recheckedGrantID, accountID, now); err != nil {
		t.Fatalf("deleteGrantFamily() retention recheck error = %v", err)
	}
	assertCount(t, pool, "SELECT count(*) FROM auth.oauth_grant WHERE id = $1", 1, recheckedGrantID)

	var lockedGrantID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO auth.oauth_grant
		    (request_id, account_id, account_epoch, client_id, resource, created_at, retain_until)
		VALUES ($1, $2, 1, 'client', 'https://evidence.example/mcp', $3, $4)
		RETURNING id`, "locked-"+unique, accountID, now.Add(-2*time.Hour), now.Add(-time.Hour),
	).Scan(&lockedGrantID); err != nil {
		t.Fatalf("insert locked grant error = %v", err)
	}
	accountLock, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin account lock error = %v", err)
	}
	defer func() { _ = accountLock.Rollback(ctx) }()
	if _, err := accountLock.Exec(ctx,
		"SELECT id FROM auth.user_account WHERE id = $1 FOR UPDATE", accountID,
	); err != nil {
		t.Fatalf("lock cleanup account error = %v", err)
	}
	lockedFamilyContext, cancelLockedFamily := context.WithTimeout(ctx, 2*time.Second)
	defer cancelLockedFamily()
	if err := deleteGrantFamily(lockedFamilyContext, pool, lockedGrantID, accountID, now); err != nil {
		t.Fatalf("deleteGrantFamily() locked-account error = %v", err)
	}
	assertCount(t, pool, "SELECT count(*) FROM auth.oauth_grant WHERE id = $1", 1, lockedGrantID)
	if err := accountLock.Rollback(ctx); err != nil {
		t.Fatalf("rollback account lock error = %v", err)
	}
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

