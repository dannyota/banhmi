-- +goose Up

-- The migration owner owns this schema and every SECURITY DEFINER function.
CREATE SCHEMA auth;
REVOKE ALL ON SCHEMA auth FROM PUBLIC;

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'banhmi_auth_runtime') THEN
        CREATE ROLE banhmi_auth_runtime NOLOGIN NOINHERIT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'banhmi_auth_operator') THEN
        CREATE ROLE banhmi_auth_operator NOLOGIN NOINHERIT;
    END IF;
END
$$;
-- +goose StatementEnd

CREATE TABLE auth.user_account (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    issuer text NOT NULL,
    subject text NOT NULL,
    email text NOT NULL,
    display_name text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'active',
    authorization_epoch bigint NOT NULL DEFAULT 1,
    first_seen_at timestamptz NOT NULL,
    last_login_at timestamptz NOT NULL,
    CONSTRAINT uq_auth_user_account_identity UNIQUE (issuer, subject),
    CONSTRAINT chk_auth_user_account_issuer CHECK (btrim(issuer) <> ''),
    CONSTRAINT chk_auth_user_account_subject CHECK (btrim(subject) <> ''),
    CONSTRAINT chk_auth_user_account_email CHECK (email = lower(btrim(email)) AND email <> ''),
    CONSTRAINT chk_auth_user_account_status CHECK (status IN ('active', 'blocked')),
    CONSTRAINT chk_auth_user_account_epoch CHECK (authorization_epoch >= 1),
    CONSTRAINT chk_auth_user_account_time CHECK (last_login_at >= first_seen_at)
);
CREATE INDEX idx_auth_user_account_email ON auth.user_account (email, id);

CREATE TABLE auth.login_transaction (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    state_hash bytea NOT NULL,
    browser_hash bytea NOT NULL,
    nonce_hash bytea NOT NULL,
    pkce_verifier_ciphertext bytea NOT NULL,
    return_path text NOT NULL,
    pending_consent_hash bytea NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz NULL,
    CONSTRAINT uq_auth_login_transaction_state UNIQUE (state_hash),
    CONSTRAINT chk_auth_login_transaction_hashes CHECK (
        octet_length(state_hash) > 0 AND octet_length(browser_hash) > 0 AND octet_length(nonce_hash) > 0
    ),
    CONSTRAINT chk_auth_login_transaction_pkce CHECK (octet_length(pkce_verifier_ciphertext) > 0),
    CONSTRAINT chk_auth_login_transaction_return CHECK (
        return_path LIKE '/%' AND return_path NOT LIKE '//%' AND position(E'\\' IN return_path) = 0
    ),
    CONSTRAINT chk_auth_login_transaction_pending CHECK (
        pending_consent_hash IS NULL OR octet_length(pending_consent_hash) > 0
    ),
    CONSTRAINT chk_auth_login_transaction_expiry CHECK (expires_at > created_at),
    CONSTRAINT chk_auth_login_transaction_consumed CHECK (consumed_at IS NULL OR consumed_at >= created_at)
);
CREATE INDEX idx_auth_login_transaction_expiry ON auth.login_transaction (expires_at, id);

CREATE TABLE auth.web_session (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    session_hash bytea NOT NULL,
    account_id bigint NOT NULL REFERENCES auth.user_account (id),
    account_epoch bigint NOT NULL,
    issued_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz NULL,
    CONSTRAINT uq_auth_web_session_hash UNIQUE (session_hash),
    CONSTRAINT chk_auth_web_session_hash CHECK (octet_length(session_hash) > 0),
    CONSTRAINT chk_auth_web_session_epoch CHECK (account_epoch >= 1),
    CONSTRAINT chk_auth_web_session_expiry CHECK (expires_at > issued_at),
    CONSTRAINT chk_auth_web_session_revoked CHECK (revoked_at IS NULL OR revoked_at >= issued_at)
);
CREATE INDEX idx_auth_web_session_account ON auth.web_session (account_id, id);
CREATE INDEX idx_auth_web_session_expiry ON auth.web_session (expires_at, id);

CREATE TABLE auth.pending_consent (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    handle_hash bytea NOT NULL,
    request_ciphertext bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz NOT NULL,
    bound_web_session_id bigint NULL REFERENCES auth.web_session (id) ON DELETE CASCADE,
    bound_at timestamptz NULL,
    consumed_at timestamptz NULL,
    CONSTRAINT uq_auth_pending_consent_handle UNIQUE (handle_hash),
    CONSTRAINT chk_auth_pending_consent_values CHECK (
        octet_length(handle_hash) > 0 AND octet_length(request_ciphertext) > 0
    ),
    CONSTRAINT chk_auth_pending_consent_expiry CHECK (expires_at > created_at),
    CONSTRAINT chk_auth_pending_consent_bound CHECK (
        (bound_web_session_id IS NULL AND bound_at IS NULL) OR
        (bound_web_session_id IS NOT NULL AND bound_at IS NOT NULL AND bound_at >= created_at)
    ),
    CONSTRAINT chk_auth_pending_consent_consumed CHECK (consumed_at IS NULL OR consumed_at >= created_at)
);
CREATE INDEX idx_auth_pending_consent_expiry ON auth.pending_consent (expires_at, id);
CREATE INDEX idx_auth_pending_consent_session ON auth.pending_consent (bound_web_session_id)
    WHERE bound_web_session_id IS NOT NULL;

CREATE TABLE auth.oauth_grant (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    request_id text NOT NULL,
    account_id bigint NOT NULL REFERENCES auth.user_account (id),
    account_epoch bigint NOT NULL,
    client_id text NOT NULL,
    resource text NOT NULL,
    created_at timestamptz NOT NULL,
    revoked_at timestamptz NULL,
    revocation_reason text NULL,
    retain_until timestamptz NOT NULL,
    CONSTRAINT uq_auth_oauth_grant_request UNIQUE (request_id),
    CONSTRAINT chk_auth_oauth_grant_values CHECK (
        btrim(request_id) <> '' AND btrim(client_id) <> '' AND btrim(resource) <> ''
    ),
    CONSTRAINT chk_auth_oauth_grant_epoch CHECK (account_epoch >= 1),
    CONSTRAINT chk_auth_oauth_grant_revocation CHECK (
        (revoked_at IS NULL AND revocation_reason IS NULL) OR
        (revoked_at IS NOT NULL AND revocation_reason IS NOT NULL AND btrim(revocation_reason) <> '')
    ),
    CONSTRAINT chk_auth_oauth_grant_retention CHECK (retain_until >= created_at)
);
CREATE INDEX idx_auth_oauth_grant_account ON auth.oauth_grant (account_id, id);
CREATE INDEX idx_auth_oauth_grant_retention ON auth.oauth_grant (retain_until, id);

CREATE TABLE auth.oauth_session (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    grant_id bigint NOT NULL REFERENCES auth.oauth_grant (id) ON DELETE CASCADE,
    kind text NOT NULL,
    signature text NOT NULL,
    access_token_id bigint NULL REFERENCES auth.oauth_session (id),
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz NULL,
    revoked_at timestamptz NULL,
    request_payload jsonb NOT NULL,
    CONSTRAINT uq_auth_oauth_session_signature UNIQUE (kind, signature),
    CONSTRAINT chk_auth_oauth_session_kind CHECK (
        kind IN ('authorize_code', 'pkce', 'access_token', 'refresh_token')
    ),
    CONSTRAINT chk_auth_oauth_session_signature CHECK (btrim(signature) <> ''),
    CONSTRAINT chk_auth_oauth_session_access_token CHECK (
        (kind = 'refresh_token' AND access_token_id IS NOT NULL) OR
        (kind <> 'refresh_token' AND access_token_id IS NULL)
    ),
    CONSTRAINT chk_auth_oauth_session_expiry CHECK (expires_at > created_at),
    CONSTRAINT chk_auth_oauth_session_consumed CHECK (consumed_at IS NULL OR consumed_at >= created_at),
    CONSTRAINT chk_auth_oauth_session_revoked CHECK (revoked_at IS NULL OR revoked_at >= created_at),
    CONSTRAINT chk_auth_oauth_session_payload CHECK (jsonb_typeof(request_payload) = 'object')
);
CREATE INDEX idx_auth_oauth_session_grant ON auth.oauth_session (grant_id, id);
CREATE INDEX idx_auth_oauth_session_expiry ON auth.oauth_session (expires_at, id);

CREATE TABLE auth.oauth_security_event (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    grant_id bigint NOT NULL,
    account_id bigint NOT NULL,
    request_id text NOT NULL,
    event_kind text NOT NULL,
    occurred_at timestamptz NOT NULL,
    CONSTRAINT chk_auth_oauth_security_event_kind CHECK (event_kind IN ('code_replay', 'refresh_replay')),
    CONSTRAINT chk_auth_oauth_security_event_request CHECK (btrim(request_id) <> '')
);
CREATE INDEX idx_auth_oauth_security_event_account_time
    ON auth.oauth_security_event (account_id, occurred_at);

CREATE TABLE auth.user_account_status_event (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    account_id bigint NOT NULL,
    event_kind text NOT NULL,
    actor text NOT NULL,
    reason text NOT NULL,
    occurred_at timestamptz NOT NULL,
    CONSTRAINT chk_auth_user_account_status_event_kind CHECK (event_kind IN ('blocked', 'unblocked')),
    CONSTRAINT chk_auth_user_account_status_event_actor CHECK (btrim(actor) <> ''),
    CONSTRAINT chk_auth_user_account_status_event_reason CHECK (btrim(reason) <> '')
);
CREATE INDEX idx_auth_user_account_status_event_account_time
    ON auth.user_account_status_event (account_id, occurred_at);

CREATE VIEW auth.operator_user_account
WITH (security_barrier = true)
AS
SELECT id, email, display_name, status, authorization_epoch, first_seen_at, last_login_at
FROM auth.user_account;

-- +goose StatementBegin
CREATE FUNCTION auth.block_user_account(p_account_id bigint, p_reason text)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_now timestamptz;
BEGIN
    IF p_reason IS NULL OR btrim(p_reason) = '' THEN
        RAISE EXCEPTION 'block reason is required' USING ERRCODE = '22023';
    END IF;

    PERFORM 1 FROM auth.user_account WHERE id = p_account_id FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'account not found' USING ERRCODE = 'P0002';
    END IF;

    v_now := clock_timestamp();

    UPDATE auth.user_account
    SET status = 'blocked', authorization_epoch = authorization_epoch + 1
    WHERE id = p_account_id;

    UPDATE auth.web_session
    SET revoked_at = COALESCE(revoked_at, GREATEST(v_now, issued_at))
    WHERE account_id = p_account_id;

    UPDATE auth.oauth_grant
    SET revoked_at = COALESCE(revoked_at, v_now),
        revocation_reason = COALESCE(revocation_reason, 'account_blocked')
    WHERE account_id = p_account_id;

    INSERT INTO auth.user_account_status_event
        (account_id, event_kind, actor, reason, occurred_at)
    VALUES
        (p_account_id, 'blocked', session_user, btrim(p_reason), v_now);
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION auth.unblock_user_account(p_account_id bigint, p_reason text)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_now timestamptz;
BEGIN
    IF p_reason IS NULL OR btrim(p_reason) = '' THEN
        RAISE EXCEPTION 'unblock reason is required' USING ERRCODE = '22023';
    END IF;

    PERFORM 1 FROM auth.user_account WHERE id = p_account_id FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'account not found' USING ERRCODE = 'P0002';
    END IF;

    v_now := clock_timestamp();

    UPDATE auth.user_account
    SET status = 'active', authorization_epoch = authorization_epoch + 1
    WHERE id = p_account_id;

    INSERT INTO auth.user_account_status_event
        (account_id, event_kind, actor, reason, occurred_at)
    VALUES
        (p_account_id, 'unblocked', session_user, btrim(p_reason), v_now);
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION auth.record_oauth_security_event(p_grant_id bigint, p_event_kind text)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $$
DECLARE
    v_account_id bigint;
    v_request_id text;
    v_revoked_at timestamptz;
BEGIN
    IF p_event_kind NOT IN ('code_replay', 'refresh_replay') THEN
        RAISE EXCEPTION 'unknown OAuth security event kind' USING ERRCODE = '22023';
    END IF;

    SELECT account_id, request_id, revoked_at
    INTO v_account_id, v_request_id, v_revoked_at
    FROM auth.oauth_grant
    WHERE id = p_grant_id;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'grant not found' USING ERRCODE = 'P0002';
    END IF;
    IF v_revoked_at IS NULL THEN
        RAISE EXCEPTION 'grant must be revoked before audit' USING ERRCODE = '23514';
    END IF;

    INSERT INTO auth.oauth_security_event
        (grant_id, account_id, request_id, event_kind, occurred_at)
    VALUES
        (p_grant_id, v_account_id, v_request_id, p_event_kind, clock_timestamp());
END
$$;
-- +goose StatementEnd

REVOKE ALL ON ALL TABLES IN SCHEMA auth FROM PUBLIC;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA auth FROM PUBLIC;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA auth FROM PUBLIC;
ALTER DEFAULT PRIVILEGES IN SCHEMA auth REVOKE ALL ON TABLES FROM PUBLIC;
ALTER DEFAULT PRIVILEGES IN SCHEMA auth REVOKE ALL ON SEQUENCES FROM PUBLIC;
ALTER DEFAULT PRIVILEGES IN SCHEMA auth REVOKE ALL ON FUNCTIONS FROM PUBLIC;

GRANT USAGE ON SCHEMA auth TO banhmi_auth_runtime;
GRANT SELECT ON auth.user_account TO banhmi_auth_runtime;
GRANT INSERT (issuer, subject, email, display_name, first_seen_at, last_login_at)
    ON auth.user_account TO banhmi_auth_runtime;
GRANT UPDATE (email, display_name, last_login_at)
    ON auth.user_account TO banhmi_auth_runtime;
GRANT SELECT ON auth.login_transaction TO banhmi_auth_runtime;
GRANT INSERT (state_hash, browser_hash, nonce_hash, pkce_verifier_ciphertext,
    return_path, pending_consent_hash, expires_at)
    ON auth.login_transaction TO banhmi_auth_runtime;
GRANT UPDATE (consumed_at) ON auth.login_transaction TO banhmi_auth_runtime;
GRANT SELECT ON auth.web_session TO banhmi_auth_runtime;
GRANT INSERT (session_hash, account_id, account_epoch, issued_at, expires_at)
    ON auth.web_session TO banhmi_auth_runtime;
GRANT UPDATE (revoked_at) ON auth.web_session TO banhmi_auth_runtime;
GRANT SELECT ON auth.pending_consent TO banhmi_auth_runtime;
GRANT INSERT (handle_hash, request_ciphertext, expires_at)
    ON auth.pending_consent TO banhmi_auth_runtime;
GRANT UPDATE (bound_web_session_id, bound_at, consumed_at)
    ON auth.pending_consent TO banhmi_auth_runtime;
GRANT SELECT ON auth.oauth_grant TO banhmi_auth_runtime;
GRANT INSERT (request_id, account_id, account_epoch, client_id, resource, created_at, retain_until)
    ON auth.oauth_grant TO banhmi_auth_runtime;
GRANT UPDATE (revoked_at, revocation_reason, retain_until)
    ON auth.oauth_grant TO banhmi_auth_runtime;
GRANT SELECT ON auth.oauth_session TO banhmi_auth_runtime;
GRANT INSERT (grant_id, kind, signature, access_token_id, created_at, expires_at, request_payload)
    ON auth.oauth_session TO banhmi_auth_runtime;
GRANT UPDATE (consumed_at, revoked_at) ON auth.oauth_session TO banhmi_auth_runtime;
GRANT USAGE, SELECT ON SEQUENCE auth.user_account_id_seq TO banhmi_auth_runtime;
GRANT USAGE, SELECT ON SEQUENCE auth.login_transaction_id_seq TO banhmi_auth_runtime;
GRANT USAGE, SELECT ON SEQUENCE auth.web_session_id_seq TO banhmi_auth_runtime;
GRANT USAGE, SELECT ON SEQUENCE auth.pending_consent_id_seq TO banhmi_auth_runtime;
GRANT USAGE, SELECT ON SEQUENCE auth.oauth_grant_id_seq TO banhmi_auth_runtime;
GRANT USAGE, SELECT ON SEQUENCE auth.oauth_session_id_seq TO banhmi_auth_runtime;
GRANT EXECUTE ON FUNCTION auth.record_oauth_security_event(bigint, text) TO banhmi_auth_runtime;

GRANT USAGE ON SCHEMA auth TO banhmi_auth_operator;
GRANT SELECT ON auth.operator_user_account TO banhmi_auth_operator;
GRANT EXECUTE ON FUNCTION auth.block_user_account(bigint, text) TO banhmi_auth_operator;
GRANT EXECUTE ON FUNCTION auth.unblock_user_account(bigint, text) TO banhmi_auth_operator;

