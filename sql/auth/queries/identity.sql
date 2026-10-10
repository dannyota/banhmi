-- name: CreateLogin :exec
INSERT INTO auth.login_transaction (
    state_hash,
    browser_hash,
    nonce_hash,
    pkce_verifier_ciphertext,
    return_path,
    pending_consent_hash,
    expires_at
) VALUES (
    sqlc.arg(state_hash),
    sqlc.arg(browser_hash),
    sqlc.arg(nonce_hash),
    sqlc.arg(pkce_verifier_ciphertext),
    sqlc.arg(return_path),
    sqlc.narg(pending_consent_hash),
    sqlc.arg(expires_at)
);

-- name: ConsumeLogin :one
UPDATE auth.login_transaction
SET consumed_at = GREATEST(sqlc.arg(now)::timestamptz, created_at)
WHERE state_hash = sqlc.arg(state_hash)::bytea
  AND browser_hash = sqlc.arg(browser_hash)::bytea
  AND consumed_at IS NULL
  AND expires_at > sqlc.arg(now)::timestamptz
RETURNING state_hash, browser_hash, nonce_hash, pkce_verifier_ciphertext,
    return_path, pending_consent_hash, expires_at;

-- name: UpsertUserAccount :one
INSERT INTO auth.user_account AS account (
    issuer,
    subject,
    email,
    display_name,
    first_seen_at,
    last_login_at
) VALUES (
    sqlc.arg(issuer),
    sqlc.arg(subject),
    sqlc.arg(email),
    sqlc.arg(display_name),
    sqlc.arg(now),
    sqlc.arg(now)
)
ON CONFLICT (issuer, subject) DO UPDATE
SET email = EXCLUDED.email,
    display_name = EXCLUDED.display_name,
    last_login_at = GREATEST(account.last_login_at, EXCLUDED.last_login_at)
RETURNING id, issuer, subject, email, display_name, status, authorization_epoch,
    first_seen_at, last_login_at;

-- name: LockUserAccount :one
SELECT id, issuer, subject, email, display_name, status, authorization_epoch,
    first_seen_at, last_login_at
FROM auth.user_account
WHERE id = sqlc.arg(account_id)
FOR UPDATE;

-- name: CreateWebSession :exec
INSERT INTO auth.web_session (
    session_hash,
    account_id,
    account_epoch,
    issued_at,
    expires_at
) VALUES (
    sqlc.arg(session_hash),
    sqlc.arg(account_id),
    sqlc.arg(account_epoch),
    sqlc.arg(issued_at),
    sqlc.arg(expires_at)
);

-- name: CurrentWebSession :one
SELECT a.id AS account_id,
    a.email,
    a.display_name,
    a.status,
    a.authorization_epoch,
    s.account_epoch,
    s.session_hash,
    s.expires_at,
    s.revoked_at
FROM auth.web_session AS s
JOIN auth.user_account AS a ON a.id = s.account_id
WHERE s.session_hash = sqlc.arg(session_hash)::bytea;

-- name: RevokeWebSession :execrows
UPDATE auth.web_session
SET revoked_at = GREATEST(sqlc.arg(now)::timestamptz, issued_at)
WHERE session_hash = sqlc.arg(session_hash)::bytea
  AND revoked_at IS NULL;

-- name: DeleteExpiredLoginTransactions :execrows
WITH expired AS (
    SELECT id
    FROM auth.login_transaction
    WHERE expires_at <= sqlc.arg(now)::timestamptz
    ORDER BY expires_at, id
    LIMIT sqlc.arg(batch_size)::integer
    FOR UPDATE SKIP LOCKED
)
DELETE FROM auth.login_transaction AS target
USING expired
WHERE target.id = expired.id;

-- name: DeleteExpiredPendingConsents :execrows
WITH expired AS (
    SELECT id
    FROM auth.pending_consent
    WHERE expires_at <= sqlc.arg(now)::timestamptz
    ORDER BY expires_at, id
    LIMIT sqlc.arg(batch_size)::integer
    FOR UPDATE SKIP LOCKED
)
DELETE FROM auth.pending_consent AS target
USING expired
WHERE target.id = expired.id;

-- name: DeleteExpiredWebSessions :execrows
WITH expired AS (
    SELECT id
    FROM auth.web_session
    WHERE expires_at <= sqlc.arg(now)::timestamptz
    ORDER BY expires_at, id
    LIMIT sqlc.arg(batch_size)::integer
    FOR UPDATE SKIP LOCKED
)
DELETE FROM auth.web_session AS target
USING expired
WHERE target.id = expired.id;

-- name: ListExpiredOAuthGrantCandidates :many
SELECT id, account_id
FROM auth.oauth_grant
WHERE retain_until <= sqlc.arg(now)::timestamptz
ORDER BY retain_until, id
LIMIT sqlc.arg(batch_size)::integer;

-- name: LockAccountForCleanup :one
SELECT id
FROM auth.user_account
WHERE id = sqlc.arg(account_id)
FOR UPDATE SKIP LOCKED;

-- name: LockOAuthGrantForCleanup :one
SELECT id
FROM auth.oauth_grant
WHERE id = sqlc.arg(grant_id)::bigint
  AND retain_until <= sqlc.arg(now)::timestamptz
FOR UPDATE SKIP LOCKED;

-- name: DeleteOAuthSessionsForGrant :exec
DELETE FROM auth.oauth_session
WHERE grant_id = sqlc.arg(grant_id);

-- name: DeleteOAuthGrant :execrows
DELETE FROM auth.oauth_grant
WHERE id = sqlc.arg(grant_id);

