# Hosted MCP authentication and user blocking

**Status:** proposed for review. Nothing in this document is implemented unless a section says
otherwise. Do not start implementation until the design and the open product decisions are approved.

## Decision

The hosted MCP endpoint for every jurisdiction will require a banhmi access token. Version 1 will
support Google and Microsoft login. It will not support local passwords, shared passwords, static API
keys, GitHub login, or anonymous hosted MCP access.

Public website and protocol routes will remain anonymous. Authentication will apply to `/mcp`, not to
the whole site. The local stdio server (`cmd/mcp`) is outside this design.

PostgreSQL will be the authorization source of truth. One dedicated `banhmi_auth` database will hold
identities, blocks, OAuth grants, and audit events for all jurisdictions. It will not hold corpus data.
GitHub Issues will start support and access-review requests, but will not be an identity provider or
authorization store.

## Current implementation gap

The repository does not implement this design:

- [`cmd/server/middleware.go`](../../cmd/server/middleware.go) currently allows public access by
  default and can apply one optional static API key to every non-health route.
- [`cmd/server/sites.go`](../../cmd/server/sites.go) serves all six jurisdictions from one process and
  opens one corpus database pool per jurisdiction. It has no shared auth database pool.
- The current configuration and migration commands know only the corpus databases and their existing
  schemas. There is no auth schema, auth migration path, or auth database configuration.
- The [AWS runbook](../../deploy/aws/setup-checklist.md) has no `auth.danny.vn` route, identity-provider
  credentials, auth database role, or auth secret injection.
- The required Fosite, Google OIDC, and Microsoft authentication packages are not current direct
  dependencies. The MCP SDK already contains bearer-auth and OAuth metadata helpers, but they are not
  wired into the server.

Implementation must replace the optional API-key behavior for hosted MCP. It must not silently leave
the API key as a fallback.

## Terms and boundaries

| Term | Meaning |
|------|---------|
| Provider identity | One durable Google or Microsoft account, identified only by validated provider claims. |
| User account | banhmi's row for one provider identity. Version 1 does not represent a natural person across providers. |
| MCP resource | One configured canonical URL, such as `https://banhmi.danny.vn/mcp`. |
| Token family | One authorization grant and its rotating refresh-token lineage. |
| Authorization service | The logical OAuth service at `https://auth.danny.vn`; this does not require a separate process. |

Authentication data and corpus data have separate purposes and database roles. The authorization
service may share the existing ECS task or process, but every server instance must use the same auth
database. No corpus database may become an identity or block store.

## Route and resource contract

| Route class | Authentication |
|-------------|----------------|
| `/mcp` | Valid banhmi bearer token for that exact MCP resource. |
| Per-site protected-resource metadata | Public. Names `https://auth.danny.vn` as the authorization server and advertises `mcp:read`. |
| Authorization-server metadata, client registration, authorize, provider callback, and token endpoints | Public protocol endpoints. Each endpoint still applies its protocol checks and abuse limits. |
| `/`, `/healthz`, `/privacy`, `/terms`, `/support`, `/llms.txt`, `/robots.txt`, `/sitemap.xml`, `/demo.mp4`, and directory-verification routes | Public. |

The server will load an allowlist that maps each jurisdiction to one canonical MCP resource URL. It
must not derive a resource from `Host`, `X-Forwarded-*`, or another request-controlled header. It will
canonicalize only the URI scheme and host case, then match the configured URL exactly. It will not
accept path, port, query, fragment, or trailing-slash aliases. An authorization request, authorization
code, access token, refresh token, and token verification will all carry the same allowlisted resource.
A token for one resource will not work on any other jurisdiction.

The current CloudFront jurisdiction header remains a corpus-routing signal. It is not an authorization
audience. Before the MCP handler runs, the server must independently confirm that the routed
jurisdiction matches the token's canonical resource.

## Architecture

```text
MCP client
    -> https://<site>.danny.vn/mcp
    <- 401 + protected-resource metadata
    -> https://auth.danny.vn/authorize
    -> choose Google or Microsoft
    -> provider login and callback
    -> validated identity upsert + block check in banhmi_auth
    <- one-time authorization code
    -> https://auth.danny.vn/token
    <- resource-bound banhmi access token + rotating refresh token
    -> https://<site>.danny.vn/mcp with the access token
    -> token, resource, and current block checks in banhmi_auth
```

There are two separate OAuth relationships:

1. The MCP client is an OAuth client of banhmi. Banhmi issues the code and MCP tokens.
2. Banhmi is an OAuth/OIDC client of Google or Microsoft. Provider tokens prove the identity only.

Google or Microsoft tokens will never be accepted by `/mcp`. Banhmi will not retain upstream access or
refresh tokens after extracting and validating identity claims.

## Proposed implementation boundaries

Protocol and cryptographic behavior should come from maintained libraries. These are proposed
dependencies, not current repository behavior:

| Boundary | Proposed library |
|----------|------------------|
| Banhmi OAuth authorization server | `github.com/ory/fosite` |
| Google discovery and ID-token verification | `github.com/coreos/go-oidc/v3/oidc` |
| Google authorization-code exchange | `golang.org/x/oauth2` |
| Microsoft authorization-code flow | `github.com/AzureAD/microsoft-authentication-library-for-go/apps/confidential` |
| MCP bearer enforcement and metadata types | `github.com/modelcontextprotocol/go-sdk/auth` and `oauthex` |

Banhmi code may select a provider, persist a login transaction, map a validated identity, enforce the
resource allowlist, adapt Fosite storage to PostgreSQL, and apply block policy. It must not implement
token parsing, signature validation, PKCE validation, or OAuth error semantics from scratch.

The implementation must verify the selected library versions and APIs before freezing handler or
storage interfaces. In particular, Fosite defines protocol behavior but does not remove the need for a
repository-specific PostgreSQL adapter.

## Identity contract

Email and login name are searchable profile data. Neither is an identity key, authorization key, or
cross-provider link key.

| Provider | Durable key | Search and display fields |
|----------|-------------|---------------------------|
| Google | validated OIDC `iss` + `sub` | verified `email`, name, picture |
| Microsoft | validated Entra `tid` + `oid` | `email` when present, `preferred_username`, name |

Provider adapters will accept only configured issuers and audiences. They will validate signature,
issuer, audience, expiry, authorization-flow nonce, and provider-specific required claims before a
database lookup.

Google login will request `openid email profile`, require `email_verified=true`, and key the account by
validated `iss` + `sub`. Microsoft login will accept personal and work or school accounts, validate the
tenant-aware issuer, reject app-only identities, and key the account by validated `tid` + `oid`.
Microsoft `email` and `preferred_username` remain optional lookup fields.

Each successful provider callback will perform one transaction that:

1. finds or creates the user account by its durable provider key;
2. records changed current and historical identifiers;
3. updates profile fields and `last_login_at`;
4. preserves the existing block state; and
5. checks the block state before issuing a banhmi authorization code.

Version 1 will not link Google and Microsoft accounts, even when they report the same email. A block
therefore applies to one provider identity, not to a natural person.

## Auth data model

All tables below are proposed in the `auth` schema of the dedicated `banhmi_auth` database. They will
follow repository conventions: one generated `BIGINT` primary key and named `UNIQUE` and `CHECK`
constraints. Schema changes require a dedicated migration path; the current corpus migration command
cannot be reused unchanged because it applies every corpus schema and pgvector extension.

### `auth.user_account`

One row represents one provider identity.

| Column | Purpose |
|--------|---------|
| `id` | Internal immutable user ID. |
| `provider` | `google` or `microsoft`. |
| `issuer` | Validated provider issuer. |
| `subject` | Google `sub` or Microsoft `oid`. |
| `tenant_id` | Microsoft `tid`; null for Google. |
| `current_email` | Current verified Google email or provider-supplied Microsoft email; nullable. |
| `login_name` | Microsoft `preferred_username` or another provider-supplied login; nullable. |
| `display_name`, `picture_url` | Optional profile fields. |
| `first_seen_at`, `last_login_at` | Login activity. |
| `blocked_at`, `blocked_reason` | Current block state. Both are null when unblocked. |

Required constraints:

- Google identities are unique by `(provider, issuer, subject)` and have no `tenant_id`.
- Microsoft identities are unique by `(provider, tenant_id, subject)` and require `tenant_id`.
- A profile upsert may change profile and login-activity fields only. It must not clear or replace
  block fields.
- `blocked_at` and `blocked_reason` are either both null or both set. A block reason is required.

The auth feature will not write per-query activity to this row. This keeps identity records separate
from search history.

### `auth.user_identifier`

This table retains current and historical lookup values.

| Column | Purpose |
|--------|---------|
| `user_id` | Parent user account. |
| `kind` | `email` or `login_name`. |
| `value_raw` | Provider value for operator display. |
| `value_normalized` | `lower(trim(value_raw))` for exact lookup. |
| `first_seen_at`, `last_seen_at` | Observation window. |
| `is_current` | Whether the provider supplied this value on the latest login. |

Required constraints and update behavior:

- `(user_id, kind, value_normalized)` is unique.
- At most one row per `(user_id, kind)` is current.
- A login updates identifiers in the same transaction as the account upsert. The previous current value
  becomes historical before the new value becomes current.
- The same normalized identifier may belong to several users. Search may return several rows and must
  never merge them.

### OAuth and audit data

The PostgreSQL adapter will persist registered clients, authorization requests, authorization-code
sessions, access-token sessions, refresh-token families, PKCE sessions, granted scopes, canonical
resource, internal user ID, expiry, use, and revocation state.

Bearer tokens, authorization codes, client secrets, provider credentials, `state`, and `nonce` must not
be logged. Database storage will use only the hashes or signatures required for lookup and validation;
it must not store plaintext bearer tokens. Encryption keys and provider client secrets stay in the
deployment secret store, not PostgreSQL or YAML.

`auth.user_block_event` will be append-only. It records user ID, action (`block` or `unblock`), reason,
database operator identity, and timestamp. The block functions, not callers, derive the operator from
the authenticated database session.

## Authorization and provider-login flow

The authorization page will show only:

```text
Continue with Google
Continue with Microsoft
```

There is no separate signup. The authorization flow will:

1. validate the MCP client, exact redirect URI, requested scope, PKCE `S256` challenge, and allowlisted
   resource before starting provider login;
2. persist a ten-minute login transaction and send the browser a random, one-time correlation cookie;
3. use a random one-time provider `state` and OIDC `nonce` bound to that transaction;
4. set the correlation cookie as `Secure`, `HttpOnly`, and `SameSite=Lax` with no broader domain than
   required;
5. consume the transaction exactly once on callback, even when validation later fails;
6. validate the provider response and perform the identity transaction; and
7. issue a short-lived, one-time banhmi authorization code only when the user is not blocked.

Callback errors will not reveal whether an email, provider identity, or block reason exists. A blocked
user will receive HTTP 403 and a link to the access-review instructions. No authorization code will be
issued.

Upstream provider tokens will be discarded after claim validation. The implementation will not request
offline provider access or configure a persistent provider token cache.

## MCP client registration

Version 1 will prefer Client ID Metadata Documents (CIMD). It will retain Dynamic Client Registration
(DCR) for compatible clients that need the older registration path. All current MCP tools are
read-only, so version 1 will advertise and require one scope: `mcp:read`.

- Metadata and redirect URIs must use HTTPS, be parsed and validated, and match exactly. Wildcards and
  fragments are rejected unless a tested hosted client requires a narrower exception that is approved
  before implementation.
- A registered client may redirect only to URIs recorded for that client.
- Authorization-server metadata must publish the accepted token-endpoint authentication methods.
  Public clients use PKCE `S256`; a confidential client must authenticate with an advertised method.
- Anonymous registration endpoints keep the existing request body and IP rate limits and add
  registration-specific quotas, metadata fetch timeouts and size limits, idle expiry, and a total
  client cap.
- Metadata fetches must enforce scheme, DNS, IP, redirect, timeout, and response-size controls. A
  client-supplied URL must not become a server-side request forgery (SSRF) path.
- Registration failure must not affect already registered clients or public routes.

Exact DCR quotas, retained metadata fields, confidential-client authentication methods, and accepted
redirect-URI exceptions must be approved before implementation. Hosted-client compatibility tests
must use the clients' real documented metadata rather than assumed behavior.

## Banhmi tokens and enforcement

| Credential | Lifetime and behavior |
|------------|-----------------------|
| Authorization code | At most ten minutes, one use, bound to client, exact redirect URI, PKCE challenge, scope, user, and resource. |
| Access token | Opaque, one hour, bound to client, scope, user, token family, and exact resource. |
| Refresh token | Opaque, rotating, expires after 30 days without a successful rotation, bound to the same client, user, scope, family, and resource. |

Refresh-token replay will revoke the whole token family. Unblocking a user will not clear revocation or
make any old code or token valid again.

Every `/mcp` request will perform these checks before the MCP handler runs:

1. parse and validate the bearer token through the MCP SDK middleware;
2. verify expiry, revocation, scope, and the exact resource;
3. load the user account from the auth database and reject a current block; and
4. set `auth.TokenInfo.UserID` to the internal user ID.

No block-result cache will exist in version 1. Blocking will therefore take effect on the next request
to every jurisdiction. A missing, invalid, expired, revoked, or wrong-resource token returns 401 with
protected-resource metadata. A valid token for a blocked account returns 403 without the stored reason.
An auth database error returns a server error and never invokes the MCP handler. It must not be reported
as an invalid user credential.

The authorize and token endpoints will also check the current block state. A refresh attempt by a
blocked user fails without issuing tokens.

## Operator workflow through AWS

Version 1 will have no admin web UI. An operator will connect to the private `banhmi_auth` database
through a documented, TLS-protected AWS operator path and use a dedicated `auth_operator` database
role. The deployment must not make PostgreSQL public merely to support this workflow.

Lookup uses an exact normalized current or historical identifier:

```sql
SELECT *
FROM auth.user_admin
WHERE identifier_normalized = lower(trim('user@example.com'));
```

The view must return enough information to distinguish multiple matches: internal user ID, provider,
issuer or tenant, current identifier, matched identifier, block state, and first and last login times.
The operator must select the internal user ID deliberately before changing state.

Block and revoke every existing token family in one transaction:

```sql
SELECT auth.block_user(123, 'abuse');
```

Unblock without restoring old sessions:

```sql
SELECT auth.unblock_user(123, 'review accepted');
```

`block_user` will lock the account row, refuse an already-blocked transition, set the block fields,
revoke all token families, and append one audit event. `unblock_user` will lock the row, refuse an
already-unblocked transition, clear the current block fields, and append one event. Both functions will
fail on an unknown user ID or blank reason.

The runtime role may read and update only the auth rows needed for protocol execution. The operator
role may read `auth.user_admin` and execute the block functions, but may not mutate the underlying
tables directly. Corpus roles receive no auth grants.

## Privacy and support

Before release, product copy must stop claiming “no auth”, “no signup”, “no accounts”, “no cookies”,
or “nothing that identifies you”. This includes the landing page, README, `/llms.txt`, privacy policy,
terms, publishing guide, and deployment docs.

The privacy policy must disclose:

- provider, durable provider identifier, current and historical email or login name, optional profile
  fields, and login times;
- short-lived authentication cookies and operational security logs;
- the purposes of authentication, abuse prevention, blocking, and access review;
- retention periods and the process for access, deletion, and review requests; and
- the cloud and identity providers that process authentication data.

The auth feature will not attach search queries to user accounts, write queries to auth tables, or add
user IDs to MCP request logs. Existing infrastructure logging must be reviewed before release so the
published retention statement matches deployed behavior.

GitHub Issues may start an access-review request. Issue templates and blocked-page copy must tell users
not to publish email addresses, provider IDs, tokens, or other authentication data. The private account
verification step remains an open product decision.

## Failure and recovery requirements

- Auth database unavailability denies authorization, token refresh, and MCP access. Public routes,
  including `/healthz`, remain available.
- A provider outage prevents new login through that provider but does not invalidate existing banhmi
  sessions.
- Restarting the server or running several instances does not lose login transactions, grants, token
  families, revocation, or block state.
- Backup and restore must preserve identity, identifier history, block events, grants, and revocation
  together. A restore must not revive tokens that were revoked after the backup without an explicit
  incident procedure.
- Authentication security events may identify a route, provider, result class, and pseudonymous
  internal account ID. MCP request logs must not include that ID. Neither log class may contain
  credentials, provider claims, email, login name, callback parameters, or MCP request bodies.

## Non-goals for version 1

- Local passwords, shared secrets, static MCP API keys, or anonymous hosted MCP access.
- GitHub or Apple login.
- Linking Google and Microsoft accounts, automatically or manually.
- Identifying one natural person across provider accounts.
- Admin website, billing, roles, organizations, or per-user content history.
- Retaining Google or Microsoft access or refresh tokens.
- Changing authentication for the local stdio MCP server.

## Acceptance criteria

1. Unauthenticated `/mcp` requests receive 401 with valid protected-resource metadata and the required
   `mcp:read` scope. Every listed public route remains usable without login.
2. ChatGPT and Claude can discover OAuth, identify or register their client through an approved CIMD or
   DCR path, complete Google or Microsoft login, refresh a session, and call every MCP tool.
3. The same Google `iss` + `sub` or Microsoft `tid` + `oid` resolves to one user after email, login
   name, display name, or picture changes. Profile updates do not clear a block.
4. Current and historical identifier searches return every matching account without using the
   identifier for authorization or linking.
5. Blocking atomically records the event and revokes every token family. New authorization, existing
   access tokens, and refresh fail on every jurisdiction on their next use.
6. Unblocking records the event and permits a new login, but no previously issued credential becomes
   valid again.
7. A token issued for one canonical MCP resource is rejected by every other jurisdiction, including
   when request host or forwarding headers are spoofed. Tests accept scheme or host case differences
   and reject every other resource alias.
8. State, nonce, PKCE, redirect URI, issuer, audience, resource, scope, expiry, single use, revocation,
   and refresh replay each have positive and negative tests.
9. DCR and CIMD tests cover redirect validation, SSRF defenses, body and response limits, rate and
   registration caps, idle cleanup, and concurrent registration.
10. PostgreSQL restart and multiple server instances preserve consistent sessions and block state.
11. Auth database failure grants no access, does not run an MCP tool, and does not expose block or
    identity data. Public routes stay available.
12. Logs, OAuth credential tables, and error responses contain no plaintext codes, bearer or refresh
    tokens, client secrets, provider tokens, `state`, `nonce`, email, login name, or MCP request body.
    Email and login name exist only in the profile and identifier tables that this design defines.
13. Privacy, support, landing, README, publishing, and deployment copy match the deployed behavior
    before authentication is enabled.

## Open decisions before approval

1. Set retention periods for historical identifiers, login transactions, OAuth grants, token-family
   records, block audit events, and infrastructure logs.
2. Define account access and deletion behavior. In particular, decide whether a deletion removes the
   durable provider key or retains a minimal abuse-prevention tombstone.
3. Define a private way to verify the owner of an access-review request started through GitHub Issues.
4. Set DCR quotas, total client cap, idle lifetime, retained client metadata, confidential-client
   authentication methods, and any redirect-URI exceptions required by validated hosted clients.
5. Choose the AWS operator connection path and decide whether database `current_user` is enough audit
   identity or each human operator needs a distinct database principal.
6. Define auth database backup, restore, recovery point, and revocation-after-restore procedures. The
   current corpus backup policy is not sufficient evidence for identity and authorization data.

## Implementation workstreams after approval

Freeze the resource registry, schema, handler interfaces, and the decisions above first. Then:

1. Add the dedicated auth database configuration, schema, migrations, Fosite storage, restricted
   runtime/operator roles, admin view, and block functions.
2. Add Google and Microsoft provider adapters, persisted login transactions, and callback tests.
3. Add authorization-server and protected-resource metadata, CIMD/DCR, resource binding, bearer
   middleware, and multi-jurisdiction enforcement.
4. Add route and product copy, privacy and support flows, AWS DNS/CloudFront routing, secrets, database
   access, backup, monitoring, and end-to-end tests.

No workstream starts until this design is approved.

## References

- [Google OpenID Connect identity claims](https://developers.google.com/identity/openid-connect/openid-connect)
- [Microsoft immutable claims and authorization guidance](https://learn.microsoft.com/en-us/entra/identity-platform/claims-validation)
- [MSAL Go confidential-client package](https://learn.microsoft.com/en-us/entra/msal/go/packages/confidential/)
- [OpenAI MCP authentication requirements](https://developers.openai.com/plugins/build/auth)
- [MCP authorization specification](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization)
- [Ory Fosite OAuth2 framework](https://pkg.go.dev/github.com/ory/fosite)
