package identity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"

	_ "github.com/jackc/pgx/v5/stdlib"

	"danny.vn/banhmi/deploy/authmigrations"
)

func TestPostgresStoreIntegration(t *testing.T) {
	pool := authTestPool(t)
	store := NewPostgresStore(pool)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	unique := fmt.Sprintf("%d", now.UnixNano())

	t.Run("login transaction is consumed once", func(t *testing.T) {
		transaction := LoginTransaction{
			StateHash:              []byte("state-" + unique),
			BrowserHash:            []byte("browser-" + unique),
			NonceHash:              []byte("nonce-" + unique),
			PKCEVerifierCiphertext: []byte("encrypted-verifier-" + unique),
			ReturnPath:             "/oauth/resume",
			PendingConsentHash:     []byte("consent-" + unique),
			ExpiresAt:              now.Add(10 * time.Minute),
		}
		if err := store.CreateLogin(ctx, transaction); err != nil {
			t.Fatalf("CreateLogin() error = %v", err)
		}
		got, err := store.ConsumeLogin(ctx, transaction.StateHash, transaction.BrowserHash, now)
		if err != nil {
			t.Fatalf("ConsumeLogin() error = %v", err)
		}
		if string(got.PendingConsentHash) != string(transaction.PendingConsentHash) {
			t.Fatalf("PendingConsentHash = %q, want %q", got.PendingConsentHash, transaction.PendingConsentHash)
		}
		if _, err := store.ConsumeLogin(ctx, transaction.StateHash, transaction.BrowserHash, now); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("second ConsumeLogin() error = %v, want ErrUnauthenticated", err)
		}
	})

	t.Run("concurrent login consumption has one winner", func(t *testing.T) {
		transaction := LoginTransaction{
			StateHash:              []byte("race-state-" + unique),
			BrowserHash:            []byte("race-browser-" + unique),
			NonceHash:              []byte("race-nonce-" + unique),
			PKCEVerifierCiphertext: []byte("race-verifier-" + unique),
			ReturnPath:             "/app",
			ExpiresAt:              now.Add(10 * time.Minute),
		}
		if err := store.CreateLogin(ctx, transaction); err != nil {
			t.Fatalf("CreateLogin() error = %v", err)
		}
		var succeeded atomic.Int32
		var rejected atomic.Int32
		var unexpected atomic.Value
		var wait sync.WaitGroup
		for range 2 {
			wait.Add(1)
			go func() {
				defer wait.Done()
				_, err := store.ConsumeLogin(ctx, transaction.StateHash, transaction.BrowserHash, now)
				switch {
				case err == nil:
					succeeded.Add(1)
				case errors.Is(err, ErrUnauthenticated):
					rejected.Add(1)
				default:
					unexpected.Store(err)
				}
			}()
		}
		wait.Wait()
		if err, ok := unexpected.Load().(error); ok {
			t.Fatalf("ConsumeLogin() unexpected error = %v", err)
		}
		if succeeded.Load() != 1 || rejected.Load() != 1 {
			t.Fatalf("consume results = %d success, %d rejected; want 1 and 1", succeeded.Load(), rejected.Load())
		}
	})

	t.Run("login consumption tolerates an application clock behind the database", func(t *testing.T) {
		transaction := LoginTransaction{
			StateHash:              []byte("past-clock-state-" + unique),
			BrowserHash:            []byte("past-clock-browser-" + unique),
			NonceHash:              []byte("past-clock-nonce-" + unique),
			PKCEVerifierCiphertext: []byte("past-clock-verifier-" + unique),
			ReturnPath:             "/app",
			ExpiresAt:              now.Add(10 * time.Minute),
		}
		if err := store.CreateLogin(ctx, transaction); err != nil {
			t.Fatalf("CreateLogin() error = %v", err)
		}
		if _, err := store.ConsumeLogin(
			ctx, transaction.StateHash, transaction.BrowserHash, now.Add(-10*time.Minute),
		); err != nil {
			t.Fatalf("ConsumeLogin() error = %v", err)
		}
	})

	var account Principal
	t.Run("identity follows issuer and subject while profile changes", func(t *testing.T) {
		profile := Profile{
			Issuer:      "https://accounts.google.com",
			Subject:     "subject-" + unique,
			Email:       "first-" + unique + "@example.com",
			DisplayName: "First Name",
		}
		first, err := store.CompleteLogin(ctx, profile, []byte("session-one-"+unique), now, now.Add(12*time.Hour))
		if err != nil {
			t.Fatalf("CompleteLogin(first) error = %v", err)
		}
		profile.Email = "changed-" + unique + "@example.com"
		profile.DisplayName = "Changed Name"
		second, err := store.CompleteLogin(ctx, profile, []byte("session-two-"+unique), now.Add(time.Second), now.Add(12*time.Hour))
		if err != nil {
			t.Fatalf("CompleteLogin(second) error = %v", err)
		}
		if first.AccountID != second.AccountID {
			t.Fatalf("account IDs = %d and %d, want one account", first.AccountID, second.AccountID)
		}
		if second.Email != profile.Email || second.DisplayName != profile.DisplayName {
			t.Fatalf("updated principal = %#v, want current profile", second)
		}
		account = second
	})

	t.Run("repeated login tolerates clocks crossing first seen time", func(t *testing.T) {
		profile := Profile{
			Issuer: "https://accounts.google.com", Subject: "cross-clock-" + unique, Email: "cross-clock-" + unique + "@example.com",
		}
		firstNow := now.Add(10 * time.Minute)
		first, err := store.CompleteLogin(
			ctx, profile, []byte("cross-clock-one-"+unique), firstNow, firstNow.Add(time.Hour),
		)
		if err != nil {
			t.Fatalf("CompleteLogin(first) error = %v", err)
		}
		secondNow := now.Add(-10 * time.Minute)
		second, err := store.CompleteLogin(
			ctx, profile, []byte("cross-clock-two-"+unique), secondNow, now.Add(time.Hour),
		)
		if err != nil {
			t.Fatalf("CompleteLogin(second) error = %v", err)
		}
		if second.AccountID != first.AccountID {
			t.Fatalf("account IDs = %d and %d, want one account", first.AccountID, second.AccountID)
		}
	})

	t.Run("email never merges identities", func(t *testing.T) {
		email := "shared-" + unique + "@example.com"
		one, err := store.CompleteLogin(ctx, Profile{
			Issuer: "https://accounts.google.com", Subject: "email-one-" + unique, Email: email,
		}, []byte("email-session-one-"+unique), now, now.Add(time.Hour))
		if err != nil {
			t.Fatalf("CompleteLogin(one) error = %v", err)
		}
		two, err := store.CompleteLogin(ctx, Profile{
			Issuer: "https://accounts.google.com", Subject: "email-two-" + unique, Email: email,
		}, []byte("email-session-two-"+unique), now, now.Add(time.Hour))
		if err != nil {
			t.Fatalf("CompleteLogin(two) error = %v", err)
		}
		if one.AccountID == two.AccountID {
			t.Fatalf("shared email merged account %d", one.AccountID)
		}
	})

	t.Run("same subject under another issuer is distinct", func(t *testing.T) {
		subject := "duplicate-subject-" + unique
		one, err := store.CompleteLogin(ctx, Profile{
			Issuer: "https://accounts.google.com", Subject: subject, Email: "issuer-one-" + unique + "@example.com",
		}, []byte("issuer-session-one-"+unique), now, now.Add(time.Hour))
		if err != nil {
			t.Fatalf("CompleteLogin(one) error = %v", err)
		}
		two, err := store.CompleteLogin(ctx, Profile{
			Issuer: "https://issuer.example", Subject: subject, Email: "issuer-two-" + unique + "@example.com",
		}, []byte("issuer-session-two-"+unique), now, now.Add(time.Hour))
		if err != nil {
			t.Fatalf("CompleteLogin(two) error = %v", err)
		}
		if one.AccountID == two.AccountID {
			t.Fatalf("duplicate subject merged account %d across issuers", one.AccountID)
		}
	})

	t.Run("expired and revoked sessions fail", func(t *testing.T) {
		session := []byte("short-session-" + unique)
		_, err := store.CompleteLogin(ctx, Profile{
			Issuer: "https://accounts.google.com", Subject: "short-" + unique, Email: "short-" + unique + "@example.com",
		}, session, now, now.Add(time.Minute))
		if err != nil {
			t.Fatalf("CompleteLogin() error = %v", err)
		}
		if _, err := store.Session(ctx, session, now.Add(2*time.Minute)); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("expired Session() error = %v, want ErrUnauthenticated", err)
		}
		if err := store.RevokeSession(ctx, session, now.Add(30*time.Second)); err != nil {
			t.Fatalf("RevokeSession() error = %v", err)
		}
		if _, err := store.Session(ctx, session, now.Add(30*time.Second)); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("revoked Session() error = %v, want ErrUnauthenticated", err)
		}
	})

	t.Run("failed session insert rolls profile update back", func(t *testing.T) {
		profile := Profile{
			Issuer: "https://accounts.google.com", Subject: "rollback-" + unique, Email: "before-" + unique + "@example.com",
		}
		session := []byte("rollback-session-" + unique)
		_, err := store.CompleteLogin(ctx, profile, session, now, now.Add(time.Hour))
		if err != nil {
			t.Fatalf("CompleteLogin(first) error = %v", err)
		}
		profile.Email = "after-" + unique + "@example.com"
		if _, err := store.CompleteLogin(ctx, profile, session, now.Add(time.Second), now.Add(time.Hour)); err == nil {
			t.Fatal("CompleteLogin(duplicate session) error = nil, want constraint error")
		}
		principal, err := store.Session(ctx, session, now.Add(2*time.Second))
		if err != nil {
			t.Fatalf("Session() error = %v", err)
		}
		if principal.Email != "before-"+unique+"@example.com" {
			t.Fatalf("email = %q, want rolled-back profile", principal.Email)
		}
	})

	t.Run("block and unblock serialize epoch and revoke credentials", func(t *testing.T) {
		before := account.AuthorizationEpoch
		if _, err := pool.Exec(ctx, "SELECT auth.block_user_account($1, $2)", account.AccountID, "integration block"); err != nil {
			t.Fatalf("block_user_account() error = %v", err)
		}
		if _, err := store.Session(ctx, account.SessionHash, now.Add(time.Minute)); !errors.Is(err, ErrBlocked) {
			t.Fatalf("blocked Session() error = %v, want ErrBlocked", err)
		}
		if _, err := pool.Exec(ctx, "SELECT auth.unblock_user_account($1, $2)", account.AccountID, "integration unblock"); err != nil {
			t.Fatalf("unblock_user_account() error = %v", err)
		}
		if _, err := store.Session(ctx, account.SessionHash, now.Add(time.Minute)); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("old Session() after unblock error = %v, want ErrUnauthenticated", err)
		}
		fresh, err := store.CompleteLogin(ctx, Profile{
			Issuer: "https://accounts.google.com", Subject: "subject-" + unique, Email: "changed-" + unique + "@example.com",
		}, []byte("post-unblock-session-"+unique), now.Add(2*time.Minute), now.Add(time.Hour))
		if err != nil {
			t.Fatalf("CompleteLogin(after unblock) error = %v", err)
		}
		if fresh.AuthorizationEpoch != before+2 {
			t.Fatalf("authorization epoch = %d, want %d", fresh.AuthorizationEpoch, before+2)
		}
	})

	t.Run("block timestamp follows the account lock", func(t *testing.T) {
		profile := Profile{
			Issuer: "https://accounts.google.com", Subject: "blocking-" + unique, Email: "blocking-" + unique + "@example.com",
		}
		principal, err := store.CompleteLogin(
			ctx, profile, []byte("blocking-initial-"+unique), now, now.Add(time.Hour),
		)
		if err != nil {
			t.Fatalf("CompleteLogin() error = %v", err)
		}

		issuance, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("Begin(issuance) error = %v", err)
		}
		defer func() { _ = issuance.Rollback(ctx) }()
		if _, err := issuance.Exec(ctx, "SELECT id FROM auth.user_account WHERE id = $1 FOR UPDATE", principal.AccountID); err != nil {
			t.Fatalf("lock account error = %v", err)
		}

		blockResult := make(chan error, 1)
		go func() {
			_, err := pool.Exec(ctx, "SELECT auth.block_user_account($1, $2)", principal.AccountID, "concurrent block")
			blockResult <- err
		}()
		waitForBlockedOperator(t, pool)

		if _, err := issuance.Exec(ctx, `
			INSERT INTO auth.web_session
			    (session_hash, account_id, account_epoch, issued_at, expires_at)
			VALUES ($1, $2, $3, clock_timestamp(), clock_timestamp() + interval '1 hour')`,
			[]byte("blocking-issued-"+unique), principal.AccountID, principal.AuthorizationEpoch,
		); err != nil {
			t.Fatalf("insert concurrent web session error = %v", err)
		}
		if err := issuance.Commit(ctx); err != nil {
			t.Fatalf("Commit(issuance) error = %v", err)
		}
		if err := <-blockResult; err != nil {
			t.Fatalf("concurrent block error = %v", err)
		}
	})

	t.Run("block clamps revocation to a future application issue time", func(t *testing.T) {
		applicationNow := now.Add(10 * time.Minute)
		principal, err := store.CompleteLogin(ctx, Profile{
			Issuer: "https://accounts.google.com", Subject: "future-clock-" + unique, Email: "future-clock-" + unique + "@example.com",
		}, []byte("future-clock-session-"+unique), applicationNow, applicationNow.Add(time.Hour))
		if err != nil {
			t.Fatalf("CompleteLogin() error = %v", err)
		}
		if _, err := pool.Exec(ctx,
			"SELECT auth.block_user_account($1, $2)", principal.AccountID, "future clock block",
		); err != nil {
			t.Fatalf("block_user_account() error = %v", err)
		}
		var issuedAt time.Time
		var revokedAt time.Time
		if err := pool.QueryRow(ctx, `
			SELECT issued_at, revoked_at
			FROM auth.web_session
			WHERE session_hash = $1`, principal.SessionHash).Scan(&issuedAt, &revokedAt); err != nil {
			t.Fatalf("load blocked web session error = %v", err)
		}
		if revokedAt.Before(issuedAt) {
			t.Fatalf("revoked_at %s is before issued_at %s", revokedAt, issuedAt)
		}
	})

	t.Run("logout clamps revocation to the session issue time", func(t *testing.T) {
		applicationNow := now.Add(10 * time.Minute)
		principal, err := store.CompleteLogin(ctx, Profile{
			Issuer: "https://accounts.google.com", Subject: "future-logout-" + unique, Email: "future-logout-" + unique + "@example.com",
		}, []byte("future-logout-session-"+unique), applicationNow, applicationNow.Add(time.Hour))
		if err != nil {
			t.Fatalf("CompleteLogin() error = %v", err)
		}
		if err := store.RevokeSession(ctx, principal.SessionHash, now); err != nil {
			t.Fatalf("RevokeSession() error = %v", err)
		}
		var issuedAt time.Time
		var revokedAt time.Time
		if err := pool.QueryRow(ctx, `
			SELECT issued_at, revoked_at
			FROM auth.web_session
			WHERE session_hash = $1`, principal.SessionHash).Scan(&issuedAt, &revokedAt); err != nil {
			t.Fatalf("load revoked web session error = %v", err)
		}
		if revokedAt.Before(issuedAt) {
			t.Fatalf("revoked_at %s is before issued_at %s", revokedAt, issuedAt)
		}
	})

	t.Run("deleting a bound web session removes pending consent", func(t *testing.T) {
		principal, err := store.CompleteLogin(ctx, Profile{
			Issuer: "https://accounts.google.com", Subject: "consent-" + unique, Email: "consent-" + unique + "@example.com",
		}, []byte("consent-session-"+unique), now, now.Add(time.Hour))
		if err != nil {
			t.Fatalf("CompleteLogin() error = %v", err)
		}
		var sessionID int64
		if err := pool.QueryRow(ctx,
			"SELECT id FROM auth.web_session WHERE session_hash = $1", principal.SessionHash,
		).Scan(&sessionID); err != nil {
			t.Fatalf("find web session error = %v", err)
		}
		var consentID int64
		if err := pool.QueryRow(ctx, `
			INSERT INTO auth.pending_consent
			    (handle_hash, request_ciphertext, expires_at, bound_web_session_id, bound_at)
			VALUES ($1, $2, $3, $4, $5)
			RETURNING id`,
			[]byte("bound-consent-"+unique), []byte("ciphertext"), now.Add(10*time.Minute), sessionID, now,
		).Scan(&consentID); err != nil {
			t.Fatalf("insert pending consent error = %v", err)
		}
		if _, err := pool.Exec(ctx, "DELETE FROM auth.web_session WHERE id = $1", sessionID); err != nil {
			t.Fatalf("delete web session error = %v", err)
		}
		var count int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM auth.pending_consent WHERE id = $1", consentID).Scan(&count); err != nil {
			t.Fatalf("count pending consent error = %v", err)
		}
		if count != 0 {
			t.Fatalf("pending consent count = %d, want 0", count)
		}
	})

	t.Run("revoked grant requires a reason", func(t *testing.T) {
		_, err := pool.Exec(ctx, `
			INSERT INTO auth.oauth_grant
			    (request_id, account_id, account_epoch, client_id, resource, created_at, revoked_at, retain_until)
			VALUES ($1, $2, $3, 'client', 'https://evidence.example/mcp', $4, $4, $5)`,
			"missing-reason-"+unique, account.AccountID, account.AuthorizationEpoch, now, now.Add(time.Hour),
		)
		if err == nil {
			t.Fatal("oauth_grant insert error = nil, want revocation-reason constraint error")
		}
	})

	t.Run("runtime records replay only through the restricted function", func(t *testing.T) {
		var grantID int64
		err := pool.QueryRow(ctx, `
			INSERT INTO auth.oauth_grant
			    (request_id, account_id, account_epoch, client_id, resource, created_at,
			     revoked_at, revocation_reason, retain_until)
			VALUES ($1, $2, $3, 'client', 'https://evidence.example/mcp', $4,
			        $4, 'refresh_replay', $5)
			RETURNING id`,
			"replay-grant-"+unique, account.AccountID, account.AuthorizationEpoch, now, now.Add(time.Hour),
		).Scan(&grantID)
		if err != nil {
			t.Fatalf("insert revoked OAuth grant error = %v", err)
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("Begin() error = %v", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, "SET LOCAL ROLE banhmi_auth_runtime"); err != nil {
			t.Fatalf("SET ROLE error = %v", err)
		}
		if _, err := tx.Exec(ctx, "SET LOCAL search_path = hostile, public"); err != nil {
			t.Fatalf("SET search_path error = %v", err)
		}
		if _, err := tx.Exec(ctx, "SELECT auth.record_oauth_security_event($1, 'refresh_replay')", grantID); err != nil {
			t.Fatalf("record_oauth_security_event() error = %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("Commit() error = %v", err)
		}

		var eventAccountID int64
		var requestID string
		if err := pool.QueryRow(ctx, `
			SELECT account_id, request_id
			FROM auth.oauth_security_event
			WHERE grant_id = $1`, grantID).Scan(&eventAccountID, &requestID); err != nil {
			t.Fatalf("load OAuth security event error = %v", err)
		}
		if eventAccountID != account.AccountID || requestID != "replay-grant-"+unique {
			t.Fatalf("security event snapshot = account %d, request %q", eventAccountID, requestID)
		}
	})

	t.Run("database roles enforce mutation boundaries", func(t *testing.T) {
		assertRoleDenied(t, pool, "banhmi_auth_runtime",
			"UPDATE auth.user_account SET authorization_epoch = authorization_epoch + 1 WHERE id = $1", account.AccountID)
		assertRoleDenied(t, pool, "banhmi_auth_runtime",
			"INSERT INTO auth.oauth_security_event (grant_id, account_id, request_id, event_kind, occurred_at) VALUES (1, $1, 'bad', 'code_replay', clock_timestamp())", account.AccountID)
		assertRoleDenied(t, pool, "banhmi_auth_operator",
			"UPDATE auth.user_account SET status = 'blocked' WHERE id = $1", account.AccountID)
		assertRoleDenied(t, pool, "banhmi_auth_operator",
			"UPDATE auth.web_session SET revoked_at = clock_timestamp() WHERE account_id = $1", account.AccountID)

		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("Begin() error = %v", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, "SET LOCAL ROLE banhmi_auth_operator"); err != nil {
			t.Fatalf("SET ROLE error = %v", err)
		}
		if _, err := tx.Exec(ctx, "SET LOCAL search_path = hostile, public"); err != nil {
			t.Fatalf("SET search_path error = %v", err)
		}
		var visibleAccountID int64
		if err := tx.QueryRow(ctx,
			"SELECT id FROM auth.operator_user_account WHERE email = $1", account.Email,
		).Scan(&visibleAccountID); err != nil {
			t.Fatalf("operator account lookup error = %v", err)
		}
		if visibleAccountID != account.AccountID {
			t.Fatalf("operator account ID = %d, want %d", visibleAccountID, account.AccountID)
		}
		if _, err := tx.Exec(ctx, "SELECT auth.block_user_account($1, $2)", account.AccountID, "operator role test"); err != nil {
			t.Fatalf("operator block_user_account() error = %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("Commit() error = %v", err)
		}
	})
}

func waitForBlockedOperator(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var waiting bool
		err := pool.QueryRow(context.Background(), `
			SELECT EXISTS (
			    SELECT 1
			    FROM pg_catalog.pg_stat_activity
			    WHERE query LIKE 'SELECT auth.block_user_account%'
			      AND wait_event_type = 'Lock'
			)`).Scan(&waiting)
		if err != nil {
			t.Fatalf("inspect blocked operator error = %v", err)
		}
		if waiting {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("block_user_account did not wait for the account lock")
}

func authTestPool(t *testing.T) *pgxpool.Pool {
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
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("goose.SetDialect() error = %v", err)
	}
	goose.SetBaseFS(authmigrations.FS)
	goose.SetTableName("goose_db_version_auth")
	if err := goose.UpContext(context.Background(), db, "."); err != nil {
		_ = db.Close()
		t.Fatalf("goose.UpContext() error = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("database close error = %v", err)
	}

	pool, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		t.Fatalf("pgxpool.New() error = %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func assertRoleDenied(t *testing.T, pool *pgxpool.Pool, role string, query string, arguments ...any) {
	t.Helper()
	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(context.Background(), "SET LOCAL ROLE "+role); err != nil {
		t.Fatalf("SET ROLE %s error = %v", role, err)
	}
	if _, err := tx.Exec(context.Background(), query, arguments...); err == nil {
		t.Fatalf("role %s unexpectedly executed %q", role, query)
	}
}
