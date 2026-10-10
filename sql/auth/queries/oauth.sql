-- name: LockOAuthAccount :one
SELECT id, status, authorization_epoch
FROM auth.user_account
WHERE id = sqlc.arg(account_id)
FOR UPDATE;

-- name: FindOAuthCredentialOwner :one
SELECT g.id AS grant_id, g.account_id
FROM auth.oauth_session AS s
JOIN auth.oauth_grant AS g ON g.id = s.grant_id
WHERE s.kind = sqlc.arg(kind) AND s.signature = sqlc.arg(signature);

-- name: LockOAuthGrant :one
SELECT id, request_id, account_id, account_epoch, client_id, resource,
       created_at, revoked_at, revocation_reason, retain_until
FROM auth.oauth_grant
WHERE id = sqlc.arg(grant_id)
FOR UPDATE;

-- name: LockOAuthGrantByRequestID :one
SELECT id, request_id, account_id, account_epoch, client_id, resource,
       created_at, revoked_at, revocation_reason, retain_until
FROM auth.oauth_grant
WHERE request_id = sqlc.arg(request_id)
FOR UPDATE;

-- name: FindOAuthGrantOwner :one
SELECT id AS grant_id, account_id
FROM auth.oauth_grant
WHERE request_id = sqlc.arg(request_id);

-- name: LockOAuthCredential :one
SELECT id, grant_id, kind, signature, access_token_id, created_at, expires_at,
       consumed_at, revoked_at, request_payload
FROM auth.oauth_session
WHERE kind = sqlc.arg(kind) AND signature = sqlc.arg(signature)
FOR UPDATE;

-- name: CreateOAuthGrant :one
INSERT INTO auth.oauth_grant
    (request_id, account_id, account_epoch, client_id, resource, created_at, retain_until)
VALUES
    (sqlc.arg(request_id), sqlc.arg(account_id), sqlc.arg(account_epoch),
     sqlc.arg(client_id), sqlc.arg(resource), sqlc.arg(created_at), sqlc.arg(retain_until))
RETURNING id, request_id, account_id, account_epoch, client_id, resource,
          created_at, revoked_at, revocation_reason, retain_until;

-- name: CreateOAuthSession :one
INSERT INTO auth.oauth_session
    (grant_id, kind, signature, created_at, expires_at, request_payload)
VALUES
    (sqlc.arg(grant_id), sqlc.arg(kind), sqlc.arg(signature),
     sqlc.arg(created_at), sqlc.arg(expires_at), sqlc.arg(request_payload))
RETURNING id;

-- name: CreateOAuthRefreshSession :one
INSERT INTO auth.oauth_session
    (grant_id, kind, signature, access_token_id, created_at, expires_at, request_payload)
VALUES
    (sqlc.arg(grant_id), 'refresh_token', sqlc.arg(signature), sqlc.arg(access_token_id),
     sqlc.arg(created_at), sqlc.arg(expires_at), sqlc.arg(request_payload))
RETURNING id;

-- name: ExtendOAuthGrantRetention :exec
UPDATE auth.oauth_grant
SET retain_until = GREATEST(retain_until, sqlc.arg(retain_until)::timestamptz)
WHERE id = sqlc.arg(grant_id);

-- name: ConsumeOAuthSession :execrows
UPDATE auth.oauth_session
SET consumed_at = GREATEST(sqlc.arg(now)::timestamptz, created_at)
WHERE kind = sqlc.arg(kind) AND signature = sqlc.arg(signature)
  AND consumed_at IS NULL AND revoked_at IS NULL;

-- name: RevokeOAuthSession :execrows
UPDATE auth.oauth_session
SET revoked_at = COALESCE(revoked_at, GREATEST(sqlc.arg(now)::timestamptz, created_at))
WHERE kind = sqlc.arg(kind) AND signature = sqlc.arg(signature);

-- name: RevokeOAuthAccessTokensForGrant :exec
UPDATE auth.oauth_session
SET revoked_at = COALESCE(revoked_at, GREATEST(sqlc.arg(now)::timestamptz, created_at))
WHERE grant_id = sqlc.arg(grant_id) AND kind = 'access_token';

-- name: RevokeOAuthGrantFamily :execrows
WITH revoked AS (
    UPDATE auth.oauth_grant AS g
    SET revoked_at = COALESCE(g.revoked_at, GREATEST(sqlc.arg(now)::timestamptz, g.created_at)),
        revocation_reason = COALESCE(g.revocation_reason, sqlc.arg(reason)::text)
    WHERE g.id = sqlc.arg(grant_id)
    RETURNING g.id
)
UPDATE auth.oauth_session AS s
SET revoked_at = COALESCE(s.revoked_at, GREATEST(sqlc.arg(now)::timestamptz, s.created_at))
WHERE s.grant_id IN (SELECT revoked.id FROM revoked);

-- name: RecordOAuthSecurityEvent :exec
SELECT auth.record_oauth_security_event(sqlc.arg(grant_id), sqlc.arg(event_kind));

-- name: CreatePendingConsentRow :exec
INSERT INTO auth.pending_consent (handle_hash, request_ciphertext, expires_at)
VALUES (sqlc.arg(handle_hash), sqlc.arg(request_ciphertext), sqlc.arg(expires_at));

-- name: FindPendingConsentSessionOwner :one
SELECT p.id AS consent_id, w.id AS web_session_id, w.account_id
FROM auth.pending_consent AS p
JOIN auth.web_session AS w ON w.session_hash = sqlc.arg(session_hash)
WHERE p.handle_hash = sqlc.arg(handle_hash);

-- name: LockWebSessionForConsent :one
SELECT id, account_id, account_epoch, issued_at, expires_at, revoked_at
FROM auth.web_session
WHERE id = sqlc.arg(web_session_id)
FOR UPDATE;

-- name: LockPendingConsent :one
SELECT id, request_ciphertext, created_at, expires_at,
       COALESCE(bound_web_session_id, 0)::bigint AS bound_web_session_id,
       consumed_at IS NOT NULL AS consumed
FROM auth.pending_consent
WHERE id = sqlc.arg(consent_id)
FOR UPDATE;

-- name: BindPendingConsentRow :execrows
UPDATE auth.pending_consent
SET bound_web_session_id = sqlc.arg(web_session_id),
    bound_at = GREATEST(sqlc.arg(now)::timestamptz, created_at)
WHERE id = sqlc.arg(consent_id) AND bound_web_session_id IS NULL
  AND consumed_at IS NULL;

-- name: ConsumePendingConsentRow :execrows
UPDATE auth.pending_consent
SET consumed_at = GREATEST(sqlc.arg(now)::timestamptz, created_at)
WHERE id = sqlc.arg(consent_id) AND bound_web_session_id = sqlc.arg(web_session_id)
  AND consumed_at IS NULL;
