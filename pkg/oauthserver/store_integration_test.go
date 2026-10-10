package oauthserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ory/fosite"
)

func TestPendingConsentApprovalRollbackAndDenialCommit(t *testing.T) {
	ctx := t.Context()
	pool := openOAuthTestPool(t)

	registry, err := ParseClientRegistry([]byte(clientRegistryJSON(clientJSON("client", "https://client.example/callback"))), testResource)
	if err != nil {
		t.Fatalf("ParseClientRegistry() error = %v", err)
	}
	store, err := NewStore(pool, registry, integrationStrategy{})
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}

	now := time.Now().UTC().Add(-time.Minute).Round(time.Microsecond)
	identity := hex.EncodeToString(randomTestBytes(t, 16))
	var accountID int64
	if err := pool.QueryRow(ctx, `INSERT INTO auth.user_account
		(issuer, subject, email, display_name, first_seen_at, last_login_at)
		VALUES ($1, $2, $3, '', $4, $4) RETURNING id`,
		"https://accounts.example", identity, identity+"@example.com", now).Scan(&accountID); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	sessionHash := randomTestBytes(t, 32)
	if _, err := pool.Exec(ctx, `INSERT INTO auth.web_session
		(session_hash, account_id, account_epoch, issued_at, expires_at)
		VALUES ($1, $2, 1, $3, $4)`, sessionHash, accountID, now, now.Add(time.Hour)); err != nil {
		t.Fatalf("insert web session: %v", err)
	}

	handleHash := randomTestBytes(t, 32)
	ciphertext := randomTestBytes(t, 48)
	if err := store.CreatePendingConsent(ctx, handleHash, ciphertext, now.Add(10*time.Minute)); err != nil {
		t.Fatalf("CreatePendingConsent() error = %v", err)
	}
	if _, err := store.BindPendingConsent(ctx, handleHash, sessionHash, now); err != nil {
		t.Fatalf("BindPendingConsent() error = %v", err)
	}
	otherAccountID := insertOAuthTestAccount(t, pool)
	otherSessionHash := randomTestBytes(t, 32)
	if _, err := pool.Exec(ctx, `INSERT INTO auth.web_session
		(session_hash, account_id, account_epoch, issued_at, expires_at)
		VALUES ($1, $2, 1, $3, $4)`, otherSessionHash, otherAccountID, now, now.Add(time.Hour)); err != nil {
		t.Fatalf("insert other web session: %v", err)
	}
	otherHandleHash := randomTestBytes(t, 32)
	if err := store.CreatePendingConsent(ctx, otherHandleHash, randomTestBytes(t, 48), now.Add(10*time.Minute)); err != nil {
		t.Fatalf("CreatePendingConsent(other) error = %v", err)
	}
	if _, err := store.BindPendingConsent(ctx, otherHandleHash, otherSessionHash, now); err != nil {
		t.Fatalf("BindPendingConsent(other) error = %v", err)
	}

	op, err := store.Begin(ctx, Binding{Purpose: PurposeAuthorize, ClientID: "client", Resource: testResource, AccountID: accountID}, CredentialNone, "")
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	if err := store.Commit(op.Context()); err == nil {
		t.Fatal("Commit(outer context) error = nil, want rejection")
	}
	savepointCtx, err := store.BeginTX(op.Context())
	if err != nil {
		t.Fatalf("BeginTX() error = %v", err)
	}
	if err := store.Rollback(savepointCtx); err != nil {
		t.Fatalf("Rollback(savepoint) error = %v", err)
	}
	if _, err := store.ConsumePendingConsent(op.Context(), otherHandleHash, otherSessionHash, now, ConsentApprove); !errors.Is(err, fosite.ErrAccessDenied) {
		t.Fatalf("ConsumePendingConsent(cross-account) error = %v, want access denied", err)
	}
	if _, err := store.ConsumePendingConsent(op.Context(), handleHash, sessionHash, now, ConsentApprove); err != nil {
		t.Fatalf("ConsumePendingConsent(approve) error = %v", err)
	}
	outcome, err := op.Finish(nil)
	if err == nil || outcome != OutcomeRolledBack {
		t.Fatalf("Finish(incomplete approval) = (%v, %v), want rollback with error", outcome, err)
	}
	if _, err := store.ReadPendingConsent(ctx, handleHash, sessionHash, now); err != nil {
		t.Fatalf("ReadPendingConsent() after rollback error = %v", err)
	}

	op, err = store.Begin(ctx, Binding{Purpose: PurposeAuthorize, ClientID: "client", Resource: testResource, AccountID: accountID}, CredentialNone, "")
	if err != nil {
		t.Fatalf("Begin(denial) error = %v", err)
	}
	if _, err := store.ConsumePendingConsent(op.Context(), handleHash, sessionHash, now, ConsentDeny); err != nil {
		t.Fatalf("ConsumePendingConsent(deny) error = %v", err)
	}
	outcome, err = op.Finish(fosite.ErrAccessDenied)
	if err != nil || outcome != OutcomeDeniedCommitted {
		t.Fatalf("Finish(denial) = (%v, %v), want committed denial", outcome, err)
	}
	if _, err := store.ReadPendingConsent(ctx, handleHash, sessionHash, now); !errors.Is(err, fosite.ErrAccessDenied) {
		t.Fatalf("ReadPendingConsent() after denial error = %v, want access denied", err)
	}
}

func TestPendingConsentStorageFailurePoisonsAuthorizeOperation(t *testing.T) {
	ctx := t.Context()
	pool := openOAuthTestPool(t)
	store := newOAuthIntegrationStore(t, pool)
	accountID := insertOAuthTestAccount(t, pool)

	op, err := store.Begin(ctx, Binding{Purpose: PurposeAuthorize, ClientID: "client", Resource: testResource, AccountID: accountID}, CredentialNone, "")
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	canceledCtx, cancel := context.WithCancel(op.Context())
	cancel()
	if _, err := store.ConsumePendingConsent(canceledCtx, randomTestBytes(t, 32), randomTestBytes(t, 32), time.Now().UTC(), ConsentApprove); !errors.Is(err, errStorageFailure) {
		t.Fatalf("ConsumePendingConsent() error = %v, want storage failure", err)
	}
	if !op.storageFailed {
		t.Fatal("ConsumePendingConsent() storage failure did not poison operation")
	}
	if outcome, err := op.Finish(errStorageFailure); !errors.Is(err, errStorageFailure) || outcome != OutcomeRolledBack {
		t.Fatalf("Finish() = (%v, %v), want rollback with storage failure", outcome, err)
	}
}

func TestOAuthStoreRejectsScalarSnapshotMismatchAndMissingOperation(t *testing.T) {
	ctx := t.Context()
	pool := openOAuthTestPool(t)
	store := newOAuthIntegrationStore(t, pool)
	accountID := insertOAuthTestAccount(t, pool)
	fixture := insertOAuthTestCredential(t, pool, store, accountID, oauthKindAccess, false, true)

	if _, err := store.GetAccessTokenSession(ctx, fixture.signature, NewSession(0, 0, testResource)); !errors.Is(err, errOperationRequired) {
		t.Fatalf("GetAccessTokenSession() without operation error = %v, want operation required", err)
	}
	op, err := store.Begin(ctx, Binding{Purpose: PurposeIntrospection, Resource: testResource}, CredentialAccess, "valid-access")
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	if _, err := store.GetAccessTokenSession(op.Context(), fixture.signature, NewSession(0, 0, testResource)); !errors.Is(err, errStorageFailure) {
		t.Fatalf("GetAccessTokenSession() mismatch error = %v, want storage failure", err)
	}
	if outcome, err := op.Finish(fosite.ErrInvalidRequest); err == nil || outcome != OutcomeRolledBack {
		t.Fatalf("Finish() = (%v, %v), want rollback with error", outcome, err)
	}
}

func TestOAuthStoreRefreshRotationRejectsMismatchedRequestID(t *testing.T) {
	ctx := t.Context()
	pool := openOAuthTestPool(t)
	store := newOAuthIntegrationStore(t, pool)
	accountID := insertOAuthTestAccount(t, pool)
	presented := insertOAuthTestCredential(t, pool, store, accountID, oauthKindRefresh, false, false)
	other := insertOAuthTestCredential(t, pool, store, accountID, oauthKindRefresh, false, false)

	op, err := store.Begin(ctx, Binding{Purpose: PurposeTokenExchange, ClientID: "client", Resource: testResource}, CredentialRefresh, "valid-refresh")
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	if err := store.RotateRefreshToken(op.Context(), other.requestID, presented.signature); !errors.Is(err, fosite.ErrNotFound) {
		t.Fatalf("RotateRefreshToken() error = %v, want not found", err)
	}
	if outcome, err := op.Finish(fosite.ErrInvalidGrant); err != nil || outcome != OutcomeRolledBack {
		t.Fatalf("Finish() = (%v, %v), want ordinary rollback", outcome, err)
	}

	var active bool
	if err := pool.QueryRow(ctx, `SELECT consumed_at IS NULL AND revoked_at IS NULL
		FROM auth.oauth_session WHERE kind = 'refresh_token' AND signature = $1`, presented.signature).Scan(&active); err != nil {
		t.Fatalf("query refresh state: %v", err)
	}
	if !active {
		t.Fatal("mismatched request ID consumed or revoked the presented refresh token")
	}
}

func TestOAuthStoreProvesConsumedCodeBeforeReplay(t *testing.T) {
	ctx := t.Context()
	runtimePool := openOAuthTestPool(t)
	ownerPool := openOAuthOwnerTestPool(t)
	store := newOAuthIntegrationStore(t, runtimePool)
	accountID := insertOAuthTestAccount(t, runtimePool)
	forged := insertOAuthTestCredential(t, runtimePool, store, accountID, oauthKindCode, true, false)

	op, err := store.Begin(ctx, Binding{Purpose: PurposeTokenExchange, ClientID: "client", Resource: testResource}, CredentialCode, "forged-code")
	if err != nil {
		t.Fatalf("Begin(forged) error = %v", err)
	}
	if _, err := store.GetAuthorizeCodeSession(op.Context(), forged.signature, NewSession(0, 0, testResource)); !errors.Is(err, fosite.ErrNotFound) {
		t.Fatalf("GetAuthorizeCodeSession(forged) error = %v, want not found", err)
	}
	if outcome, err := op.Finish(fosite.ErrInvalidGrant); err != nil || outcome != OutcomeRolledBack {
		t.Fatalf("Finish(forged) = (%v, %v), want ordinary rollback", outcome, err)
	}
	assertOAuthGrantAuditState(t, ownerPool, forged.grantID, false, 0)

	valid := insertOAuthTestCredential(t, runtimePool, store, accountID, oauthKindCode, true, false)
	op, err = store.Begin(ctx, Binding{Purpose: PurposeTokenExchange, ClientID: "client", Resource: testResource}, CredentialCode, "valid-code")
	if err != nil {
		t.Fatalf("Begin(valid replay) error = %v", err)
	}
	request, err := store.GetAuthorizeCodeSession(op.Context(), valid.signature, NewSession(0, 0, testResource))
	if !errors.Is(err, fosite.ErrInvalidatedAuthorizeCode) || request == nil {
		t.Fatalf("GetAuthorizeCodeSession(valid replay) = (%v, %v), want requester and invalidated code", request, err)
	}
	if err := store.RevokeAccessToken(op.Context(), request.GetID()); err != nil {
		t.Fatalf("RevokeAccessToken() error = %v", err)
	}
	if err := store.RevokeRefreshToken(op.Context(), request.GetID()); err != nil {
		t.Fatalf("RevokeRefreshToken() duplicate callback error = %v", err)
	}
	if outcome, err := op.Finish(fosite.ErrInvalidGrant); err != nil || outcome != OutcomeReplayCommitted {
		t.Fatalf("Finish(valid replay) = (%v, %v), want replay commit", outcome, err)
	}
	assertOAuthGrantAuditState(t, ownerPool, valid.grantID, true, 1)
}

func TestOAuthStoreRevocationOfConsumedRefreshDoesNotCreateReplay(t *testing.T) {
	ctx := t.Context()
	runtimePool := openOAuthTestPool(t)
	ownerPool := openOAuthOwnerTestPool(t)
	store := newOAuthIntegrationStore(t, runtimePool)
	accountID := insertOAuthTestAccount(t, runtimePool)
	fixture := insertOAuthTestCredential(t, runtimePool, store, accountID, oauthKindRefresh, true, false)

	op, err := store.Begin(ctx, Binding{Purpose: PurposeRevocation, ClientID: "client", Resource: testResource}, CredentialAccessOrRefresh, "valid-refresh")
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	if _, err := store.GetRefreshTokenSession(op.Context(), fixture.signature, NewSession(0, 0, testResource)); !errors.Is(err, fosite.ErrInactiveToken) {
		t.Fatalf("GetRefreshTokenSession() error = %v, want inactive token", err)
	}
	if op.replay != nil {
		t.Fatal("GetRefreshTokenSession() created a replay candidate during revocation")
	}
	if outcome, err := op.Finish(nil); err != nil || outcome != OutcomeCommitted {
		t.Fatalf("Finish() = (%v, %v), want read-only commit", outcome, err)
	}
	assertOAuthGrantAuditState(t, ownerPool, fixture.grantID, false, 0)
}

func TestOAuthStoreRollsBackReplayAuditAndOpenSavepointFailures(t *testing.T) {
	ctx := t.Context()
	runtimePool := openOAuthTestPool(t)
	ownerPool := openOAuthOwnerTestPool(t)
	store := newOAuthIntegrationStore(t, runtimePool)
	accountID := insertOAuthTestAccount(t, runtimePool)
	auditFailure := insertOAuthTestCredential(t, runtimePool, store, accountID, oauthKindCode, true, false)

	op, err := store.Begin(ctx, Binding{Purpose: PurposeTokenExchange, ClientID: "client", Resource: testResource}, CredentialCode, "valid-code")
	if err != nil {
		t.Fatalf("Begin(audit failure) error = %v", err)
	}
	op.replay = &replayCandidate{grantID: auditFailure.grantID, requestID: auditFailure.requestID, eventKind: "invalid_event"}
	if err := store.RevokeAccessToken(op.Context(), auditFailure.requestID); !errors.Is(err, errStorageFailure) {
		t.Fatalf("RevokeAccessToken(audit failure) error = %v, want storage failure", err)
	}
	if outcome, err := op.Finish(fosite.ErrInvalidGrant); err == nil || outcome != OutcomeRolledBack {
		t.Fatalf("Finish(audit failure) = (%v, %v), want rollback with error", outcome, err)
	}
	assertOAuthGrantAuditState(t, ownerPool, auditFailure.grantID, false, 0)

	openSavepoint := insertOAuthTestCredential(t, runtimePool, store, accountID, oauthKindAccess, false, false)
	op, err = store.Begin(ctx, Binding{Purpose: PurposeIntrospection, Resource: testResource}, CredentialAccess, "valid-access")
	if err != nil {
		t.Fatalf("Begin(open savepoint) error = %v", err)
	}
	if _, err := store.BeginTX(op.Context()); err != nil {
		t.Fatalf("BeginTX() error = %v", err)
	}
	if outcome, err := op.Finish(nil); err == nil || outcome != OutcomeRolledBack {
		t.Fatalf("Finish(open savepoint) = (%v, %v), want rollback with error", outcome, err)
	}
	assertOAuthGrantAuditState(t, ownerPool, openSavepoint.grantID, false, 0)
}

func TestOAuthStoreIntrospectionCannotMutateAndUsesCurrentRegistry(t *testing.T) {
	ctx := t.Context()
	runtimePool := openOAuthTestPool(t)
	ownerPool := openOAuthOwnerTestPool(t)
	store := newOAuthIntegrationStore(t, runtimePool)
	accountID := insertOAuthTestAccount(t, runtimePool)
	fixture := insertOAuthTestCredential(t, runtimePool, store, accountID, oauthKindAccess, false, false)

	op, err := store.Begin(ctx, Binding{Purpose: PurposeIntrospection, Resource: testResource}, CredentialAccess, "valid-access")
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	if _, err := store.GetAccessTokenSession(op.Context(), fixture.signature, NewSession(0, 0, testResource)); err != nil {
		t.Fatalf("GetAccessTokenSession() error = %v", err)
	}
	if err := store.DeleteAccessTokenSession(op.Context(), fixture.signature); !errors.Is(err, errOperationRequired) {
		t.Fatalf("DeleteAccessTokenSession() error = %v, want operation required", err)
	}
	if outcome, err := op.Finish(nil); err != nil || outcome != OutcomeCommitted {
		t.Fatalf("Finish() = (%v, %v), want read-only commit", outcome, err)
	}
	assertOAuthGrantAuditState(t, ownerPool, fixture.grantID, false, 0)

	emptyRegistry, err := ParseClientRegistry([]byte(`{"version":1,"clients":[]}`), testResource)
	if err != nil {
		t.Fatalf("ParseClientRegistry(empty) error = %v", err)
	}
	removedStore, err := NewStore(runtimePool, emptyRegistry, integrationStrategy{})
	if err != nil {
		t.Fatalf("NewStore(empty registry) error = %v", err)
	}
	op, err = removedStore.Begin(ctx, Binding{Purpose: PurposeIntrospection, Resource: testResource}, CredentialAccess, "valid-access")
	if err != nil {
		t.Fatalf("Begin(removed client) error = %v", err)
	}
	if _, err := removedStore.GetAccessTokenSession(op.Context(), fixture.signature, NewSession(0, 0, testResource)); !errors.Is(err, fosite.ErrNotFound) {
		t.Fatalf("GetAccessTokenSession(removed client) error = %v, want not found", err)
	}
	if outcome, err := op.Finish(fosite.ErrInvalidRequest); err != nil || outcome != OutcomeRolledBack {
		t.Fatalf("Finish(removed client) = (%v, %v), want rollback", outcome, err)
	}
}

func randomTestBytes(t *testing.T, size int) []byte {
	t.Helper()
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		t.Fatalf("rand.Read() error = %v", err)
	}
	return value
}

type oauthCredentialFixture struct {
	grantID   int64
	requestID string
	signature string
}

func newOAuthIntegrationStore(t *testing.T, pool *pgxpool.Pool) *Store {
	t.Helper()
	registry, err := ParseClientRegistry([]byte(clientRegistryJSON(clientJSON("client", "https://client.example/callback"))), testResource)
	if err != nil {
		t.Fatalf("ParseClientRegistry() error = %v", err)
	}
	store, err := NewStore(pool, registry, integrationStrategy{})
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	return store
}

func insertOAuthTestAccount(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	now := time.Now().UTC()
	identity := hex.EncodeToString(randomTestBytes(t, 16))
	var accountID int64
	if err := pool.QueryRow(t.Context(), `INSERT INTO auth.user_account
		(issuer, subject, email, display_name, first_seen_at, last_login_at)
		VALUES ($1, $2, $3, '', $4, $4) RETURNING id`,
		"https://accounts.example", identity, identity+"@example.com", now).Scan(&accountID); err != nil {
		t.Fatalf("insert OAuth test account: %v", err)
	}
	return accountID
}

func insertOAuthTestCredential(t *testing.T, pool *pgxpool.Pool, store *Store, accountID int64, kind string, consumed, mismatchedExpiry bool) oauthCredentialFixture {
	t.Helper()
	now := time.Now().UTC()
	requestID := "request-" + hex.EncodeToString(randomTestBytes(t, 12))
	signature := "shared-signature-" + hex.EncodeToString(randomTestBytes(t, 12))
	store.strategy = integrationStrategy{signature: signature}
	request := testRequest(t)
	request.ID = requestID
	request.RequestedAt = now
	request.Client, _ = store.clients.GetClient(t.Context(), "client")
	request.Session = NewSession(accountID, 1, testResource)
	tokenType := fosite.AccessToken
	if kind == oauthKindCode {
		tokenType = fosite.AuthorizeCode
	} else if kind == oauthKindRefresh {
		tokenType = fosite.RefreshToken
	}
	expiresAt := now.Add(time.Hour)
	request.Session.SetExpiresAt(tokenType, expiresAt)
	if kind == oauthKindRefresh {
		request.Session.SetExpiresAt(fosite.AccessToken, expiresAt)
	}
	payload, err := store.codec.Encode(request)
	if err != nil {
		t.Fatalf("RequestCodec.Encode() error = %v", err)
	}
	var grantID int64
	if err := pool.QueryRow(t.Context(), `INSERT INTO auth.oauth_grant
		(request_id, account_id, account_epoch, client_id, resource, created_at, retain_until)
		VALUES ($1, $2, 1, 'client', $3, $4, $5) RETURNING id`,
		requestID, accountID, testResource, now, expiresAt).Scan(&grantID); err != nil {
		t.Fatalf("insert OAuth test grant: %v", err)
	}
	scalarExpiry := expiresAt
	if mismatchedExpiry {
		scalarExpiry = scalarExpiry.Add(time.Minute)
	}
	var consumedAt any
	if consumed {
		consumedAt = now
	}
	var accessTokenID any
	if kind == oauthKindRefresh {
		var id int64
		if err := pool.QueryRow(t.Context(), `INSERT INTO auth.oauth_session
			(grant_id, kind, signature, created_at, expires_at, request_payload)
			VALUES ($1, 'access_token', $2, $3, $4, $5::jsonb) RETURNING id`,
			grantID, "access-"+signature, now, expiresAt, string(payload)).Scan(&id); err != nil {
			t.Fatalf("insert OAuth test access association: %v", err)
		}
		accessTokenID = id
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO auth.oauth_session
		(grant_id, kind, signature, access_token_id, created_at, expires_at, consumed_at, request_payload)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb)`,
		grantID, kind, signature, accessTokenID, now, scalarExpiry, consumedAt, string(payload)); err != nil {
		t.Fatalf("insert OAuth test credential: %v", err)
	}
	return oauthCredentialFixture{grantID: grantID, requestID: requestID, signature: signature}
}

func assertOAuthGrantAuditState(t *testing.T, pool *pgxpool.Pool, grantID int64, revoked bool, eventCount int) {
	t.Helper()
	var revokedAt *time.Time
	if err := pool.QueryRow(t.Context(), "SELECT revoked_at FROM auth.oauth_grant WHERE id = $1", grantID).Scan(&revokedAt); err != nil {
		t.Fatalf("select OAuth grant state: %v", err)
	}
	if (revokedAt != nil) != revoked {
		t.Fatalf("grant revoked = %v, want %v", revokedAt != nil, revoked)
	}
	var gotEvents int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM auth.oauth_security_event WHERE grant_id = $1", grantID).Scan(&gotEvents); err != nil {
		t.Fatalf("select OAuth security events: %v", err)
	}
	if gotEvents != eventCount {
		t.Fatalf("security event count = %d, want %d", gotEvents, eventCount)
	}
}

func openOAuthTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("PostgreSQL integration test")
	}
	databaseURL := os.Getenv("BANHMI_AUTH_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("BANHMI_AUTH_TEST_DATABASE_URL is not set")
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("pgxpool.ParseConfig() error = %v", err)
	}
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET ROLE banhmi_auth_runtime")
		return err
	}
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatalf("pgxpool.NewWithConfig() error = %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func openOAuthOwnerTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("BANHMI_AUTH_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("BANHMI_AUTH_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), databaseURL)
	if err != nil {
		t.Fatalf("pgxpool.New(owner) error = %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

type integrationStrategy struct{ signature string }

func (s integrationStrategy) AccessTokenSignature(context.Context, string) string { return s.signature }
func (s integrationStrategy) GenerateAccessToken(context.Context, fosite.Requester) (string, string, error) {
	return "token", s.signature, nil
}
func (integrationStrategy) ValidateAccessToken(_ context.Context, _ fosite.Requester, token string) error {
	if token != "valid-access" {
		return errors.New("invalid access token")
	}
	return nil
}
func (s integrationStrategy) RefreshTokenSignature(context.Context, string) string {
	return s.signature
}
func (s integrationStrategy) GenerateRefreshToken(context.Context, fosite.Requester) (string, string, error) {
	return "token", s.signature, nil
}
func (integrationStrategy) ValidateRefreshToken(_ context.Context, _ fosite.Requester, token string) error {
	if token != "valid-refresh" {
		return errors.New("invalid refresh token")
	}
	return nil
}
func (s integrationStrategy) AuthorizeCodeSignature(context.Context, string) string {
	return s.signature
}
func (s integrationStrategy) GenerateAuthorizeCode(context.Context, fosite.Requester) (string, string, error) {
	return "token", s.signature, nil
}
func (integrationStrategy) ValidateAuthorizeCode(_ context.Context, _ fosite.Requester, token string) error {
	if token != "valid-code" {
		return errors.New("invalid authorization code")
	}
	return nil
}
