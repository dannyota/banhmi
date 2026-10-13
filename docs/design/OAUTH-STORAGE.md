# OAuth storage and transaction contract

Implement the first MCP authorization server with Fosite and PostgreSQL. Keep browser sessions,
Google login, and MCP credentials separate. [AUTH.md](AUTH.md) owns the product and access policy;
this document owns the OAuth library adapter, persistence, and transaction contract.

Status: implementation design, 2026-10-11. The upstream APIs below were checked against source.
Treat the described code as unimplemented until implementation and hosted verification establish
otherwise. Source inspection does not prove build compatibility, dependency security, or deployed
behavior. The existing AWS deployment remains unchanged until a later explicit cutover.

## Library and first-release defaults

Pin `github.com/ory/fosite v0.49.0`, commit
`653c812bc40cbda049857b432ad3346f1a53d42d`. GitHub reported this as the latest official release
and tag when inspected. See the [release](https://github.com/ory/fosite/releases/tag/v0.49.0).

| Setting | First-release value |
|---|---|
| Google identity | Canonical Google issuer plus case-sensitive `sub` |
| Google email | Present email and `email_verified=true` required |
| Website session | 12 hours absolute |
| Google login transaction | 10 minutes, one use |
| MCP consent transaction | 10 minutes, one use |
| Authorization code | 5 minutes, one use |
| Access token | 15 minutes |
| Refresh token | 30 days from successful issuance or rotation |
| Refresh replay grace | None |
| MCP permission | `mcp:read` |
| Client enrollment | Preregistered public clients |
| Client authentication | `none`, with mandatory PKCE `S256` |
| Authorization response mode | Absent or `query` only |
| Empty client configuration | Connections unavailable; authentication remains enforced |

Email supports operator lookup. It never identifies or merges accounts. Reject Google login when
email is absent or unverified. Keep the account revocation primitive available for the later private
admin portal; this slice adds no admin UI.

The selected domain and provisioned Google credentials are deployment prerequisites, not code
prerequisites. Missing production auth configuration must fail closed. There is no anonymous or
static-key fallback in the new hosted server.

## Fosite composition

Use `compose.NewOAuth2HMACStrategy(config)` for opaque credentials and these factories only:

```go
compose.Compose(
    config,
    store,
    strategy,
    compose.OAuth2AuthorizeExplicitFactory,
    compose.OAuth2RefreshTokenGrantFactory,
    compose.OAuth2TokenIntrospectionFactory,
    compose.OAuth2TokenRevocationFactory,
    compose.OAuth2PKCEFactory,
)
```

PKCE must follow the authorization-code factory. Do not use `ComposeAllEnabled`, which enables
unwanted grants and OpenID Connect issuance. See [composition source](https://github.com/ory/fosite/blob/v0.49.0/compose/compose.go)
and [factory source](https://github.com/ory/fosite/blob/v0.49.0/compose/compose_oauth2.go).

Set these `fosite.Config` fields explicitly:

```go
AccessTokenLifespan:            15 * time.Minute,
AuthorizeCodeLifespan:          5 * time.Minute,
RefreshTokenLifespan:           30 * 24 * time.Hour,
EnforcePKCE:                    true,
EnforcePKCEForPublicClients:    true,
EnablePKCEPlainChallengeMethod: false,
ScopeStrategy:                 fosite.ExactScopeStrategy,
AudienceMatchingStrategy:      fosite.ExactAudienceMatchingStrategy,
RefreshTokenScopes:            []string{},
SendDebugMessagesToClients:    false,
SanitationWhiteList:           []string{"redirect_uri", "resource"},
```

`RefreshTokenScopes` must be a non-nil empty slice. Nil enables the library's default requirement
for `offline` or `offline_access`. Load `GlobalSecret` through runtime secret injection. Keep the
same key across instances and restarts; support `RotatedGlobalSecrets` for planned rotation.
Do not fetch secret values into tools, configuration examples, or logs. See
[configuration source](https://github.com/ory/fosite/blob/v0.49.0/config_default.go).

## Client configuration

Load a versioned JSON file with no secrets:

```json
{
  "version": 1,
  "clients": [
    {
      "client_id": "approved-client",
      "client_name": "Approved client",
      "redirect_uris": ["https://client.example/oauth/callback"],
      "token_endpoint_auth_method": "none",
      "grant_types": ["authorization_code", "refresh_token"],
      "response_types": ["code"],
      "scope": "mcp:read"
    }
  ]
}
```

Reject unknown fields, duplicate client IDs, empty IDs or names, and empty redirect lists. Require
the exact authentication method, grant set, response type, and scope shown above. Reject wildcard
redirects. Redirects must be absolute HTTPS URLs without fragments or userinfo. A preregistered
public native client may use an HTTP redirect to the loopback IP literal `127.0.0.1` or `::1` with
an explicit port. Match its full registered URI, including that exact port. Reject `localhost`
hostnames, omitted or dynamic ports, wildcards, fragments, and userinfo.

The configured list is authoritative. Removing a client prevents token issuance, refresh, and use
when the updated configuration is active on every instance. A valid empty list disables connections
without bypassing auth. Client ID Metadata Documents and Dynamic Client Registration are deferred;
do not advertise either capability or a registration endpoint.

`GetClient` constructs a `fosite.DefaultClient` with `Public=true`, no secret, and
`Audience=[]string{configuredMCPResource}`. Store only `client_id` in protocol records and load the
current client configuration during hydration. Do not serialize a client secret or a stale client
policy snapshot into each token.

`fosite.ClientManager` also requires `ClientAssertionJWTValid` and `SetClientAssertionJWT`.
Implement both as explicit unsupported-operation errors. Supported clients cannot select
`private_key_jwt`, so supported flows must never call them. See
[client manager source](https://github.com/ory/fosite/blob/v0.49.0/client_manager.go) and
[client types](https://github.com/ory/fosite/blob/v0.49.0/client.go).

## Schema ownership

Auth lives in a dedicated database. Handwritten Goose SQL under `deploy/authmigrations/` is the
schema source; sqlc reads its migration directory, excluding bootstrap SQL. Keep auth queries under
`sql/auth/` and generated code under `pkg/store/auth/`. Auth migrations must not install corpus
schemas or pgvector. Corpus restore must not overwrite auth state. Account, browser-session,
Google-login, consent, and audit records follow the product contract in [AUTH.md](AUTH.md).

The initial auth migration preserves functions and privileges as well as tables. Handwritten Goose
SQL is the schema source, and sqlc reads the auth migration directory while excluding bootstrap SQL.
Trusted Git history checks protect migration bytes. Hosted checks compare schema, roles, and
privileges against PostgreSQL. Migration-backed generation and parity evidence are under
implementation; no result is accepted yet.

Use the tables below for Fosite persistence and replay auditing. Every table has one surrogate
primary key. Account status, resource, client, revocation, and expiry remain scalar columns.
The JSONB payload is only
a versioned library hydration record, not a second authorization authority.

### `auth.oauth_grant`

| Column | Type and constraint | Purpose |
|---|---|---|
| `id` | `bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY` | Internal key |
| `request_id` | `text NOT NULL UNIQUE` | Fosite request ID, stable across rotations |
| `account_id` | `bigint NOT NULL` | Foreign key to `auth.user_account(id)` |
| `account_epoch` | `bigint NOT NULL` | Epoch at authorization |
| `client_id` | `text NOT NULL` | Configured OAuth client |
| `resource` | `text NOT NULL` | Exact canonical MCP resource |
| `created_at` | `timestamptz NOT NULL` | Creation |
| `revoked_at` | `timestamptz NULL` | Family revocation |
| `revocation_reason` | `text NULL` | Controlled internal reason |
| `retain_until` | `timestamptz NOT NULL` | Latest expiry of issued credentials |

Require a nonblank reason when `revoked_at` is set and null reason otherwise. Do not erase the
revocation when an account is unblocked. Index `account_id` for account-wide revocation and
`retain_until` for bounded family cleanup.

### `auth.oauth_session`

| Column | Type and constraint | Purpose |
|---|---|---|
| `id` | `bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY` | Internal key |
| `grant_id` | `bigint NOT NULL` | Grant foreign key |
| `kind` | `text NOT NULL` | `authorize_code`, `pkce`, `access_token`, or `refresh_token` |
| `signature` | `text NOT NULL` | Library-generated lookup signature |
| `access_token_id` | `bigint NULL` | Refresh row's associated access-token row |
| `created_at` | `timestamptz NOT NULL` | Creation |
| `expires_at` | `timestamptz NOT NULL` | Indexed expiry |
| `consumed_at` | `timestamptz NULL` | Code, refresh, or PKCE consumption |
| `revoked_at` | `timestamptz NULL` | Explicit invalidation |
| `request_payload` | `jsonb NOT NULL` | Versioned Fosite hydration snapshot |

Use `UNIQUE (kind, signature)` and indexes on `grant_id` and expiry. Check allowed kinds and
timestamp consistency. Require `access_token_id` for refresh rows and forbid it for other kinds.
The adapter must verify that the referenced row is an access token in the same grant. The grant
retention horizon increases to cover every newly issued credential.

Retain consumed code and refresh rows until the grant retention horizon passes. Never physically
delete a refresh row during rotation. Its signature is needed to recognize replay. Cleanup must
not remove live grants or replay evidence for credentials within their retention horizon.

### `auth.oauth_security_event`

Append one event for each proven replay attempt, in the same transaction as family revocation.
These events record OAuth replay separately from operator block and unblock events.

| Column | Type and constraint | Purpose |
|---|---|---|
| `id` | `bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY` | Internal event key |
| `grant_id` | `bigint NOT NULL` | Immutable grant ID snapshot |
| `account_id` | `bigint NOT NULL` | Account ID derived from the grant |
| `request_id` | `text NOT NULL` | Fosite request ID derived from the grant |
| `event_kind` | `text NOT NULL` | `code_replay` or `refresh_replay` only |
| `occurred_at` | `timestamptz NOT NULL` | Database-generated event time |

Keep identity snapshots independent of grant deletion. Do not add a cascading grant foreign key
or a grant foreign key that prevents expired-family cleanup. The insertion function establishes
the grant/account/request relationship before cleanup. Add a check constraint for the two event
kinds and an index on `account_id, occurred_at` for later private audit lookup. Store no credential,
signature, provider token, or raw request in the event.

Expose only this insertion primitive:

```text
auth.record_oauth_security_event(grant_id bigint, event_kind text) RETURNS void
```

The function is `SECURITY DEFINER`, owned by the schema-owner role, with a fixed
`search_path = pg_catalog, pg_temp`. Fully qualify every application table reference. Reject an
unknown event kind or missing grant. Read account ID and request ID from the grant rather than
accepting either from the caller. Require the grant to be revoked before appending the event and
set `occurred_at` from the database clock. Resolve parameter/column names explicitly to avoid
ambiguous references.

Revoke `PUBLIC` function execution and all runtime privileges on the audit table and its identity
sequence. Grant the runtime role `EXECUTE` on this function only for the audit interface. Runtime
cannot directly insert, update, delete, or truncate audit rows. Runtime and operator roles cannot
assume or inherit the owner role. Audit records remain append-only; credential cleanup never
deletes them. A later audit-retention policy requires its own reviewed owner operation.

The function does not prove credential possession. The OAuth adapter must first validate the
complete replayed credential through the library, then revoke the family and invoke the function
within the existing outer transaction. If audit insertion fails, roll back revocation as well and
return a sanitized server error. Do not report a successfully handled replay with a missing audit
event.

### Bounded cleanup

Add expiry indexes to `auth.login_transaction`, `auth.pending_consent`, and `auth.web_session`.
Use each table's expiration timestamp as the leading index column. The cleanup command deletes
expired transient rows in bounded batches; it does not delete unexpired rows merely because they
were consumed or revoked.

Clean OAuth credentials by whole grant family only after `retain_until` has passed. Select a
bounded set through the grant retention index, follow the account/grant lock order, and recheck
retention after locking. Skip locked work rather than blocking active issuance. Delete the family's
session rows and grant in one transaction. Do not delete individual expired code or refresh rows
while their grant remains within its retention horizon. Concurrent rotation may extend that
horizon; cleanup must observe the extension. Keep `auth.oauth_security_event` snapshots intact.

The primary agent assigns the cleanup command and hosted database checks with the persistence
task. Cleanup implementation and scheduling are required work, not an assumed database feature.

## Session and request codec

Define a private, versioned codec. Do not marshal an arbitrary `fosite.Requester` interface and
rely on JSON to recover its concrete client and session types.

```go
type StoredRequest struct {
    Version           int
    ID                string
    RequestedAt       time.Time
    ClientID          string
    RequestedScopes   []string
    GrantedScopes     []string
    RequestedAudience []string
    GrantedAudience   []string
    Form              url.Values
    Session           StoredSession
}

type StoredSession struct {
    ExpiresAt    map[fosite.TokenType]time.Time
    Subject      string
    AccountID    int64
    AccountEpoch int64
    Resource     string
}
```

Use explicit JSON tags in the implementation. `Subject` is the decimal internal account ID;
username remains empty. Cross-check account ID, epoch, resource, request ID, and client ID against
the grant columns during hydration. Reject unknown codec versions and malformed snapshots.

Persist only these form keys:

```text
grant_type, response_type, scope, client_id, redirect_uri, resource,
code_challenge, code_challenge_method
```

Never persist the full incoming form. The snapshot contains no email, provider token, browser
session, plaintext OAuth token, authorization code, or code verifier. The Google login transaction
separately needs a recoverable, runtime-key-encrypted upstream PKCE verifier until code exchange.

Decode into a fresh `fosite.Request`, current client configuration, and a typed session implementing
`fosite.Session`. Hydrate the supplied session object when non-nil: the code handler checks expiry
against that object before replacing it with the stored request's session. If the typed session
embeds `DefaultSession`, override `Clone`; the embedded method would drop custom fields. Deep-copy
the expiry map. See [request source](https://github.com/ory/fosite/blob/v0.49.0/request.go) and
[session source](https://github.com/ory/fosite/blob/v0.49.0/session.go).

## Storage adapter

Implement `fosite.ClientManager`, `oauth2.CoreStorage`, `oauth2.TokenRevocationStorage`,
`pkce.PKCERequestStorage`, and `storage.Transactional` with compile-time interface assertions.

| Method group | Required behavior |
|---|---|
| `CreateAuthorizeCodeSession` | Create the grant if absent and insert the code row |
| `GetAuthorizeCodeSession` | Hydrate; consumed code returns the request and `fosite.ErrInvalidatedAuthorizeCode` |
| `InvalidateAuthorizeCodeSession` | Consume once under the account and grant locks |
| Access-token create/get/delete | Insert, validate current grant/account, or mark revoked |
| `CreateRefreshTokenSession` | Insert snapshot and associated access-token reference |
| `GetRefreshTokenSession` | Hydrate; consumed refresh returns the request and `fosite.ErrInactiveToken` |
| `RotateRefreshToken` | Consume the presented refresh once and revoke the grant's prior access tokens |
| `DeleteRefreshTokenSession` | Mark inactive and preserve replay evidence |
| `RevokeRefreshToken`, `RevokeAccessToken` | Idempotently revoke the entire grant and all its credentials |
| PKCE create/get/delete | Store, hydrate, and consume the paired PKCE snapshot |

Use `fosite.ErrNotFound` for absent records. Returning the hydrated request with the consumed-code
or consumed-refresh error is mandatory: Fosite needs its request ID to revoke related credentials.
The request ID identifies the family across all refresh rotations. Do not copy the example memory
store's single-current-token maps into SQL.

The adapter must know the operation purpose. Token exchange and refresh may read consumed snapshots
to detect replay. Evidence access must reject inactive credentials. A revoked grant or inactive
account must never authorize a read merely because hydration succeeded.

Verified interfaces: [core storage](https://github.com/ory/fosite/blob/v0.49.0/handler/oauth2/storage.go),
[revocation storage](https://github.com/ory/fosite/blob/v0.49.0/handler/oauth2/revocation_storage.go),
and [PKCE storage](https://github.com/ory/fosite/blob/v0.49.0/handler/pkce/storage.go).

## Transactions and replay

Wrap the full token operation in a request-owned PostgreSQL transaction:

1. Parse the bounded request and validate its parameter shape and resource.
2. Begin the outer transaction.
3. Find the grant owner, then lock account, grant, and credential in that order. An initial lookup
   may locate the owner, but reread credential state after taking the locks.
4. Validate account status, epoch, client, and resource.
5. Call `NewAccessRequest`.
6. Call `NewAccessResponse`.
7. Commit before writing a successful response.

The consent submission likewise wraps account validation, consent consumption, grant creation,
code creation, and PKCE creation in one transaction. The account block primitive locks the same
account row before incrementing its epoch and revoking browser sessions and every OAuth grant.
Use one lock order across all paths.

The library's transaction hook has these exact methods:

```go
BeginTX(context.Context) (context.Context, error)
Commit(context.Context) error
Rollback(context.Context) error
```

Implement nested library transactions as savepoints. A library commit releases its savepoint;
only the HTTP orchestration commits the outer transaction. Every store method obtains its sqlc
executor from the transaction context. See
[transaction interface](https://github.com/ory/fosite/blob/v0.49.0/storage/transactional.go).

The outer transaction is required because PKCE consumption occurs in `NewAccessRequest`, before
Fosite begins its own issuance transaction. Replay handling also mutates storage while returning
an OAuth error. See the [PKCE handler](https://github.com/ory/fosite/blob/v0.49.0/handler/pkce/handler.go).

Before a consumed credential can trigger family revocation, validate the presented credential's
signature with Fosite's strategy and its hydrated snapshot. Do not treat signature lookup alone
as proof that the complete presented token is valid. Fosite's consumed-code and consumed-refresh
branches precede its normal credential validation. Keep cryptographic validation in the library.

Apply this error policy:

| Outcome | Transaction result |
|---|---|
| Successful issuance | Commit complete issuance, then send response |
| Ordinary validation failure | Roll back |
| Database or savepoint failure | Roll back; return sanitized server error |
| Proven code or refresh replay | Commit only family revocation and audit; return protocol error |

Use a typed security-revocation outcome in transaction state. Commit that outcome only when the
transaction remains healthy and no new credential issuance succeeded. Never commit arbitrary
partial work because an error happens to be an OAuth error. Never roll back proven replay
revocation merely because the library returns `invalid_grant`.

For a proven replay, call `auth.record_oauth_security_event` with `code_replay` or `refresh_replay`
after revoking the family. Commit revocation and the event atomically. The two Fosite revocation
callbacks must not append duplicate events for the same handled attempt. A storage or audit failure
rolls back both changes and returns a server error instead of the ordinary replay response.

Account locking serializes competing refreshes. The losing request reads the now-consumed
credential and follows replay revocation. Unblocking the account restores no old credentials.
See [refresh handling](https://github.com/ory/fosite/blob/v0.49.0/handler/oauth2/flow_refresh.go) and
[authorization-code exchange](https://github.com/ory/fosite/blob/v0.49.0/handler/oauth2/flow_authorize_code_token.go).

## MCP resource binding

Use a configured HTTPS origin as issuer and `<origin>/mcp` as resource. Never derive either from
request host or proxy headers. Fosite reads `audience`; the adapter must bridge the MCP `resource`
parameter explicitly:

1. Require exactly one `resource` in authorization requests and authorization-code exchanges.
   Refresh requests may omit `resource`. If present on refresh, it must occur exactly once.
2. Require every supplied resource value to equal the configured MCP resource exactly. An empty
   value is not omission. The origin is already canonical configuration. Accept no case, path,
   port, encoding, query, fragment, or trailing-slash aliases.
3. Reject caller-supplied `audience`.
4. Populate Fosite requested and granted audience from the approved canonical resource.
5. Store resource in both the grant column and typed session.
6. Check persisted resource before issuance and every evidence request.

The refresh exception supports `golang.org/x/oauth2 v0.36.0`: its `TokenSource` constructs refresh
requests without a resource parameter. An omitted refresh resource never changes the grant's
audience. Bind the operation to the trusted configured MCP resource and verify both the stored
grant resource column and typed-session resource before rotating or issuing credentials. See the
[pinned refresh implementation](https://github.com/golang/oauth2/blob/v0.36.0/oauth2.go) and
[resource indicators](https://www.rfc-editor.org/rfc/rfc8707.html#section-2.2).

Revocation has a separate form contract. Require public `client_id` and `token`, with optional
`token_type_hint`. Do not include `resource` in the revocation form allowlist. Bind revocation to
the trusted configured resource internally, then verify the stored grant and typed-session
resource. Removing the form parameter does not relax token possession or client binding. See
[RFC 7009 revocation requests](https://www.rfc-editor.org/rfc/rfc7009.html#section-2.1).

Require `mcp:read` at authorization. Refresh may omit scope or repeat `mcp:read`; it cannot expand
permissions. Use `ExactAudienceMatchingStrategy`: the default accepts URI path descendants.
See [audience source](https://github.com/ory/fosite/blob/v0.49.0/audience_strategy.go).

For `/mcp`, extract credentials only from the Authorization bearer header and call:

```go
provider.IntrospectToken(
    ctx, token, fosite.AccessToken, newSession(), "mcp:read",
)
```

Require the returned token use to equal `fosite.AccessToken`; a hint alone is insufficient. Check
current account status and epoch, grant state, resource, and client configuration. Do not expose
a public introspection endpoint. Website cookies, Google tokens, static keys, and query-string
tokens cannot authorize MCP. See [provider API](https://github.com/ory/fosite/blob/v0.49.0/oauth2.go).

## Response adapters

Use Fosite's request, response, and error APIs. Keep protocol changes limited to the required MCP
resource and issuer adapters. Accept absent or `query` authorization `response_mode`; reject every
other mode before consent.

Add `iss` through `AuthorizeResponder.AddParameter` on successful authorization. On authorized
error redirects, wrap the Fosite response writer to add the configured issuer to its validated
redirect. Never introduce a redirect where Fosite rejected the URI. Advertise
`authorization_response_iss_parameter_supported: true`. See
[authorization error writer](https://github.com/ory/fosite/blob/v0.49.0/authorize_error.go).

Fosite's revocation writer returns HTTP 200 for error classes beyond invalid request and invalid
client, including temporary storage failures. Intercept server/storage failures and return a
sanitized HTTP 503 rather than reporting successful revocation. See
[revocation writer](https://github.com/ory/fosite/blob/v0.49.0/revoke_handler.go).

The MCP SDK bearer middleware must receive sanitized verifier errors. Its current version can
write verifier error strings to the response. Keep blocked-account policy separate so that valid
credentials cannot bypass policy and internal database errors do not become public messages.

## Required hosted verification

GitHub owns generation, builds, tests, and database checks. The primary agent owns verification
and Git. Require independent review of the adapter and these cases before declaring it validated:

- Codec round trips preserve every expiry, account field, scope, audience, and PKCE challenge.
  Unknown versions and mismatches with grant columns fail closed.
- Missing or plain PKCE, wrong verifier, wrong client, changed redirect, wrong resource, duplicate
  resource, and unsupported response modes fail without issuance.
- Authorization and code exchange reject missing resource. Refresh accepts an omitted resource,
  but rejects an empty, duplicate, or different value and rejects any mismatch in stored grant or
  typed-session resource. Exercise refresh through the pinned OAuth library's `TokenSource`.
- Revocation accepts public `client_id` and `token` with an optional hint and no resource. A
  supplied resource is outside the form allowlist. Internal canonical-resource checks still reject
  mismatched grant or typed-session bindings before revocation.
- Failed validation rolls back PKCE consumption. Failed issuance leaves no partial tokens.
- Code and refresh replay revoke the family durably even though the response is an OAuth error.
- Each proven replay appends exactly one event with grant-derived account/request IDs and the
  correct event kind. The event and revocation survive the protocol-error response together.
  Audit insertion failure leaves neither partial revocation nor an event.
- A malformed token sharing a consumed credential's lookup signature does not revoke the family.
- Invalid or unproven replay creates no security event. The audit function rejects unknown event
  kinds, unknown grants, and grants that have not been revoked.
- Concurrent refresh permits one issuance, detects reuse, and revokes the family without a race.
- Blocking races safely with login, consent, code exchange, and refresh. Unblocking restores none
  of the old sessions or grants.
- Client removal and an empty client list never enable anonymous access.
- Revocation storage failure returns a server error, not false success.
- Issuer is present in successful and authorized error redirects. Invalid redirects remain local
  errors. No token, verifier, callback code, or secret appears in logs or stored request payloads.
- Auth migrations operate independently of corpus migrations; restart and restore retain revocation
  and credential state. Multiple instances share the same state.
- Role tests prove that runtime can execute the audit function but cannot directly insert, update,
  delete, or truncate its table or alter its sequence. `PUBLIC` cannot execute the function, and
  runtime/operator roles cannot assume owner rights. Function lookup remains safe with hostile
  caller search paths and temporary objects.
- Migration checks prove that the initial migration contains the reviewed functions and privileges
  and that later changes preserve trusted migration bytes.
- Cleanup deletes expired transient rows and expired grant families within its batch limit. It
  preserves unexpired state, consumed credential replay evidence inside `retain_until`, and audit
  events. Concurrent rotation that extends retention prevents premature family deletion.

Current protocol references: [MCP authorization](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization),
[discovery](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization/authorization-server-discovery),
[client registration](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization/client-registration),
and [security requirements](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization/security-considerations).
