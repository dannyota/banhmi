# Google access for the website and hosted MCP

**Status:** authentication source implemented, undeployed. The
[Google access plan](../plans/google-access.md#progress) records hosted verification.
The current AWS service remains unchanged until a separate deployment and cutover decision. The
[tracuuquydinh.vn website proposal](website.md) revises the public routes, the login `next`
allowlist, and the `/connect` access rule below. Those website changes remain pending.

## Decision

One canonical origin will serve the public website, Google login, the signed-in evidence workspace,
OAuth authorization, and `/mcp`. The first release covers the Vietnam corpus. A user signs in only
with Google. The first successful login creates the account, so there is no separate registration
form.

The website and MCP expose the same evidence. They do not answer legal questions, translate queries,
or retain query history. The website calls shared Search and Document application methods in
`pkg/mcp`; it does not call its own MCP endpoint or copy retrieval and evidence-shaping logic.

The hosted service requires authentication by default. Missing authentication configuration or an
unavailable authentication database denies protected access. Hosted `/mcp` has no anonymous or
static-key fallback. The local stdio server remains outside this design.

## First-release scope

The first release includes:

- a public landing page;
- automatic account creation and login through Google OpenID Connect (OIDC);
- an authenticated website for Vietnam evidence Search and Document calls;
- OAuth 2.0 authorization for preregistered public MCP clients;
- browser sessions, MCP grants, revocation, and refresh-token rotation;
- account status, an authorization epoch, blocking, unblocking, and an append-only audit record; and
- a private database operator path for account lookup and block actions.

The release does not include passwords, another identity provider, provider linking, dynamic client
registration, Client ID Metadata Documents, a private admin website, configurable account quotas,
billing, organizations, saved searches, query history, subject taxonomy, multi-country website
selection, a product rename, or a hosting cutover.

## Routes and user experience

| Route | Access | Purpose |
|---|---|---|
| `GET /` | Public | Landing page with `Try evidence` and `Connect MCP` actions. |
| `GET /login?next=...` | Public | One `Continue with Google` action. |
| `GET /auth/google/start` | Public, rate limited | Start a Google authorization-code flow. |
| `GET /auth/google/callback` | Public, rate limited | Consume the login transaction and establish a web session. |
| `GET /oauth/resume` | Web session and pending-consent cookie | Resume one validated MCP authorization after Google login. |
| `GET /app` | Web session | Vietnam evidence search page. |
| `GET /connect` | Web session | Exact MCP URL and supported-client instructions. |
| `POST /api/v1/search` | Web session, Origin and CSRF checks | Call the shared Search method. |
| `POST /api/v1/document` | Web session, Origin and CSRF checks | Call the shared Document method. |
| `POST /logout` | Web session, Origin and CSRF checks | Revoke the current web session. |
| `GET /.well-known/oauth-protected-resource/mcp` | Public | MCP protected-resource metadata. |
| `GET /.well-known/oauth-protected-resource` | Public | Root alias for the same protected-resource metadata. |
| `GET /.well-known/oauth-authorization-server` | Public | Authorization-server metadata. |
| `GET /oauth/authorize` | OAuth protocol checks, then web session | Validate the request and show consent. |
| `POST /oauth/authorize/decision` | Web session, Origin and CSRF checks | Approve or deny the validated request. |
| `POST /oauth/token` | Public protocol endpoint, rate limited | Exchange a code or rotate a refresh token. |
| `POST /oauth/revoke` | Public protocol endpoint, rate limited | Revoke a token through Fosite. |
| `/mcp` | MCP access token | Existing Streamable HTTP MCP service. |

Existing health, legal, support, crawler, and directory-verification routes remain public. Login,
application, connection, consent, and error pages send `noindex`. Authentication, account, consent,
and evidence responses send `Cache-Control: no-store`.

Public `next` accepts only an explicit set of same-origin relative application paths. The first
release allows `/app` and `/connect`. The server alone may create the exact internal
`/oauth/resume` destination after it has validated and persisted an MCP authorization request.
User-supplied `next=/oauth/resume` is rejected unless the request also carries the independent live
pending-consent cookie. Absolute URLs, scheme-relative URLs, encoded bypasses, and other protocol,
callback, API, or MCP paths fall back to `/app`.

The `/app` page stays small. It contains a Vietnamese query field, a `Search evidence` action, an
evidence-only notice, a Vietnamese-query notice, result cards, and an inline document panel. Search
uses the MCP defaults. A result shows its citation, document number and title, location, snippet,
validity and warnings, text provenance, official source, and explicit gaps. `Open document` sends the
returned document ID and optional parent citation to the shared Document method. The panel renders
the returned metadata, chunks, validity, provenance, relations, official sources and files,
amendment context, pagination state, and gaps.

The `/connect` page displays the configured canonical `/mcp` URL. It explains that the MCP client
opens the same Google account flow. It never displays or creates a personal API key.

## Implementation boundaries

Protocol and cryptographic validation come from maintained libraries:

| Boundary | Required implementation |
|---|---|
| Banhmi OAuth authorization server | `github.com/ory/fosite` v0.49.0 at official commit [`653c812bc40cbda049857b432ad3346f1a53d42d`](https://github.com/ory/fosite/commit/653c812bc40cbda049857b432ad3346f1a53d42d) |
| Google discovery and ID-token verification | A maintained `github.com/coreos/go-oidc/v3/oidc` release |
| Google authorization-code exchange | A maintained `golang.org/x/oauth2` release |
| MCP bearer and metadata integration | The pinned official Go MCP SDK |

Banhmi code may hold application policy, persist Fosite sessions in PostgreSQL, map a validated
Google identity, enforce the exact configured resource, and apply account status. It must not
implement signature verification, PKCE verification, bearer parsing, authorization-code validation,
refresh-token protocol, or OAuth error semantics itself.

## Authentication database

A dedicated PostgreSQL database holds authentication and authorization data. Corpus databases hold
no accounts or credentials. The authentication database has its own configuration, migrations,
roles, and sqlc package. The current corpus migration command must not apply authentication schemas or
pgvector extensions to this database.

The service runtime connects through `BANHMI_AUTH_DATABASE_URL`. The migration owner connects
through the distinct, owner-only `BANHMI_AUTH_MIGRATION_DATABASE_URL`. Runtime and operator roles
have `NOLOGIN` owner separation and cannot assume or inherit the migration-owner role. Only the
migration owner may apply migrations. Migrations revoke default and existing `PUBLIC` privileges.
Operator `SECURITY DEFINER` functions set a fixed safe `search_path` and do not depend on the
caller's path.

The initial full-schema migration is handwritten Goose SQL and is the schema source. sqlc reads the
auth migration directory, excluding bootstrap SQL. Trusted Git history checks protect migration
bytes, and Goose records applied versions. Migration-backed auth generation and trusted-history
checks are coded and independently reviewed. The [Goose migration plan](../plans/goose-migrations.md#evidence-and-remaining-gates)
owns hosted parity evidence and current status.

The [OAuth storage design](OAUTH-STORAGE.md) defines the Fosite v0.49.0 PostgreSQL adapter, transaction
boundaries, replay behavior, schema, sqlc queries, and migration rules. This document owns the product,
identity, route, and authorization policy. The storage design must preserve those policies without
creating a second protocol contract.

The minimum data model contains:

- `auth.user_account`: internal ID, canonical Google issuer and subject, current verified email,
  optional display name, status, authorization epoch, first-seen time, and last-login time;
- `auth.login_transaction`: hashes of the correlation handle, state, and nonce, a runtime-key-encrypted
  and recoverable upstream PKCE verifier, a validated return path,
  `LoginTransaction.PendingConsentHash`, expiry, and consumption time. `PendingConsentHash`
  records the hash of the exact pending-consent cookie for an OAuth-started login and is empty for an
  ordinary website login;
- `auth.pending_consent`: hashed independent browser handle, runtime-key-encrypted validated OAuth
  request, expiry, consumption time, and the web session to which resume bound it;
- `auth.web_session`: hashed opaque token, account ID, copied authorization epoch, issue time, absolute
  expiry, and revocation time;
- Fosite client and session records for preregistered clients, authorization requests, codes, access
  tokens, refresh-token families, scopes, exact resource, expiry, use, and revocation; and
- `auth.oauth_security_event`: append-only OAuth replay audit with grant ID, account ID, request ID,
  event kind `code_replay` or `refresh_replay`, and `occurred_at`, with no token, code, cookie,
  provider value, or other secret; and
- `auth.user_account_status_event`: append-only block and unblock actions with account, actor, reason,
  and time.

Tables follow repository conventions: one generated `BIGINT` primary key, named `UNIQUE` and `CHECK`
constraints, and sqlc-generated access. The Google identity is unique by canonical `(issuer,
subject)`. Email is profile and operator lookup data. The same normalized email may belong to more
than one account and never links or authorizes accounts. The first release stores no profile picture
and no email or identifier history.

Raw browser tokens, authorization codes, access tokens, and refresh tokens are stored only in the
one-way form required for lookup and validation. Provider credentials stay in the runtime secret
store. No credential, provider token, state, nonce, PKCE verifier, callback query, verified email, or
evidence request body may enter logs or error responses. An authenticated account page may display
the account's current verified email as profile data; that display does not authorize or link the
account.

## Google login and web sessions

Google login requests `openid email profile`. The callback uses discovery and the maintained OIDC
verifier to validate the signature, canonical issuer, configured audience, expiry, and nonce. It
also validates the stored state and uses the stored PKCE verifier during the authorization-code
exchange. The callback rejects a missing email or any value other than `email_verified=true`.

The account identity is the canonical Google issuer plus `sub`. A valid callback creates the account
when that identity is new, or updates its current verified email, optional display name, and
last-login time when it exists. It never finds or merges an account by email. Google access and ID
tokens are discarded after validation. The service requests no offline Google access.

A login transaction and its host-only correlation cookie expire after 10 minutes. The service
consumes the transaction once before completing callback validation, including failure paths. The
cookie uses the `__Host-` prefix, `Secure`, `HttpOnly`, `Path=/`, no `Domain`, and `SameSite=Lax`.
The database stores hashes of the browser handle, state, and nonce. It stores the upstream PKCE
verifier only as runtime-key-encrypted recoverable data because the callback needs the original value
for the Google code exchange. For an OAuth-started login, the callback also requires the current
pending-consent cookie hash to equal `LoginTransaction.PendingConsentHash` before it issues a web
session. An ordinary website login requires `PendingConsentHash` to be empty. A replaced
pending-consent cookie or row fails closed.

A successful login issues a random opaque web session. The database stores only its hash. The
session cookie uses the same host-only security attributes and has a 12-hour absolute lifetime. It
does not extend on use. Every authenticated website request reloads the account, requires active
status, and requires the session authorization epoch to equal the account authorization epoch.

Generic callback errors do not reveal whether an account, email, block, transaction, or provider
claim exists. A blocked account receives no new session.

## MCP OAuth contract

The authorization server supports only preregistered public clients. Client configuration is an
operator-controlled allowlist. The server publishes token endpoint authentication method `none` and
supports only authorization code plus PKCE `S256`. It exposes no dynamic registration endpoint and
performs no client-supplied metadata fetch.

Each client has an exact client ID, display name, and redirect URI allowlist. Redirect URIs must be
exact preregistered HTTPS URIs, except that a native client may use an exact preregistered HTTP URI
whose host is a loopback IP literal and whose port matches the registered port. `localhost`,
wildcards, fragments, omitted ports, variable ports, and redirect aliases are rejected.

The server advertises and accepts only scope `mcp:read`. Authorization requests and authorization-code
exchanges require exactly one RFC 8707 `resource` value equal to the configured canonical `/mcp`
URL. A refresh request may omit `resource` because the standard `golang.org/x/oauth2` token source
does so. If a refresh request supplies `resource`, it must supply exactly one canonical value. The
server always binds the trusted canonical resource internally and verifies that the persisted grant
and session carry that same resource. It accepts no resource alias, trailing-slash variant, query,
fragment, port variant, forwarded-host reconstruction, or client-supplied `audience`.
Authorization codes and both token types bind the client, exact redirect URI, scope, account,
authorization epoch, and exact resource. The authorization response includes the authorization-server
issuer as the RFC 9207 `iss` parameter.

RFC 7009 revocation requires the public `client_id` and `token` form values and accepts the optional
`token_type_hint`. It does not accept `resource`; the handler rejects a supplied `resource` as
unsupported. The handler derives the canonical resource binding internally and verifies it against
the persisted grant and session before revocation.

The consent transaction expires after 10 minutes. An authorization code expires after 5 minutes and
is single-use. An opaque access token expires after 15 minutes. A rotating opaque refresh token
expires after 30 days without successful use. Each valid rotation starts a new 30-day period. Refresh
replay has no grace window and revokes the complete token family.

Code or refresh replay appends an `auth.oauth_security_event` in the same transaction that revokes
the token family. The narrow `SECURITY DEFINER` function
`auth.record_oauth_security_event` accepts the grant and event kind, then derives the account and
request IDs from that grant. The runtime role receives `EXECUTE` on this function only. It has no
direct insert, update, or delete privilege on the audit table.

An anonymous authorization request is validated before the user goes to Google. The server encrypts
the validated request into a 10-minute pending-consent row and sets a separate random opaque
host-only pending-consent cookie whose hash identifies that row. The Google login transaction stores
the hash of that exact pending-consent cookie and the internal `/oauth/resume` return destination.
The login cookie and pending-consent cookie are independent credentials. The pending cookie uses a distinct
`__Host-` name, `Secure`, `HttpOnly`, `Path=/`, no `Domain`, and `SameSite=Lax`.

After Google login, `/oauth/resume` requires both the new web session and the live pending-consent
cookie. It resolves the same pending-consent row and conditionally binds that row to the web session.
It does not set `consumed_at`. The same session may repeat resume. A different session cannot resume
or take over the bound request. Consent GET and POST require that same pending-consent cookie and web
session. Approval or denial atomically consumes the row once and clears the pending-consent cookie.
Approval performs consumption, grant and code creation, and PKCE persistence in one transaction.
Denial issues no code. The callback cannot replace the client, redirect URI, scope, resource, state,
or PKCE challenge. A user who already has a valid web session reaches consent without the Google
round trip, but the same validated-request and binding rules apply. The consent page displays the
preregistered client name, exact resource, and `mcp:read`. A replaced cookie or row fails closed.

Google tokens never authorize `/mcp`. A web session never authorizes `/mcp`. An MCP token never
authorizes the website or its JSON endpoints. Before invoking an MCP tool, the server validates the
bearer through Fosite and the MCP middleware, reloads the account, requires active status, and checks
the authorization epoch. An empty client allowlist disables authorization and token issuance; it
never makes `/mcp` anonymous.

## Account status and private operation

`auth.user_account.status` is `active` or `blocked`. Every web request, MCP request, authorization
request, consent decision, code exchange, and refresh checks current status and authorization epoch
against the database. Version 1 has no status cache. An authentication database error denies access
before retrieval or an MCP tool runs.

The authentication schema supplies restricted operator lookup plus block and unblock primitives. An
operator reaches the database through a documented private, TLS-protected path and a dedicated
database role. The database is not made public. Lookup uses the internal account ID or exact
normalized current email and returns all matches for deliberate account selection.

Blocking locks the account and, in one transaction, changes status, increments the authorization
epoch, revokes every web session and OAuth token family, and appends an audit event with the database
actor and a required reason. Unblocking locks the account, returns it to active status, increments
the authorization epoch, and appends an event. Unblocking restores no session, authorization code,
grant, access token, or refresh token. The runtime and operator roles cannot bypass these primitives
with unrestricted table writes.

The runtime role may read status and epoch for admission and perform the narrowly granted login,
session, and protocol writes. It cannot alter account status or authorization epoch. The restricted
operator role can change them only by executing the block and unblock functions; it cannot update the
underlying account or credential tables directly.

The private admin website, account usage display, temporary suspension interface, and configurable
per-account quotas remain later work. Existing IP, request-body, timeout, and concurrency controls
continue to bound the first release.

## Middleware and failure behavior

Route classes have separate admission:

- public pages and metadata need no account but retain request limits and security headers;
- Google and OAuth protocol endpoints add route-specific rate and body limits;
- website APIs require a current web session, an exact allowed `Origin`, a CSRF token, an allowed
  method, and the existing body limit before calling evidence code; and
- `/mcp` requires a current resource-bound bearer token before the existing MCP handler runs.

Every non-health route first requires `r.Host` to equal the authority from the configured canonical
origin exactly. The service does not reconstruct authority from `Forwarded` or
`X-Forwarded-Host`. It rejects aliases and unknown hosts before issuing a cookie or accepting a
bearer token. The configured reverse proxy must preserve the canonical `Host`. The new surface does
not use the existing unknown-host fallback.

The current optional static API key does not remain as a hosted fallback. Hosted startup fails closed
when required canonical-origin, Google, database, token, or client configuration is invalid. An empty
client allowlist is valid only as a disabled OAuth state: website login may operate, authorization
requests fail, token issuance fails, and `/mcp` remains protected.

An authentication database outage denies login completion, website evidence calls, consent, code
exchange, refresh, and MCP access. Public pages and health remain available. A Google outage prevents
new login but does not invalidate an existing session before its normal checks and expiry. The
Google manager constructor validates local configuration only. Login and callback resolve and cache
Google discovery data lazily with bounded request timeouts. Google discovery availability is not a
startup dependency. A process restarted while Google is unavailable still serves public pages and
validates existing website sessions through the authentication database.

Authentication tables have expiry and retention indexes that support bounded cleanup without table
scans. A bounded authentication cleanup command deletes expired transient login, pending-consent,
session, code, and token rows in limited batches. It deletes a grant family only after its
`retain_until` time, so authorization-code and refresh-token replay evidence remains available
through the defined retention period. Cleanup does not rewrite or delete either append-only audit
table.

Authentication security logs may contain a route, result class, request ID, and opaque internal
account ID when needed. Evidence access logs do not contain account ID or request bodies. The service
does not store raw search queries or associate evidence requests with accounts.

## Verification and acceptance

GitHub Actions performs all dependency changes, sqlc generation and drift checks, formatting drift,
builds, tests, `go vet`, database integration, and route tests. Contributors do not run those
commands locally.
The implementation uses fake Google, fake stores, fake retrieval, and fake corpus dependencies for
short in-process route tests. A hosted PostgreSQL job proves migrations, Fosite persistence,
concurrency, revocation, restart behavior, and sqlc queries before deployment.

Acceptance requires:

1. A first valid Google callback with a verified email creates one account and web session. The same
   canonical issuer and subject reuse that account after email or display-name changes.
2. Missing or unverified email, wrong state or nonce, wrong issuer or audience, expiry, signature
   failure, replay, missing cookie, denial, and storage failure issue no session and disclose no
   identity detail.
3. The pending-consent credential is independent of Google login and expires after 10 minutes. An
   OAuth-started callback requires its exact cookie hash, while an ordinary login has no pending
   reference. `/oauth/resume` requires the cookie with the new web session, binds the encrypted
   validated request once, permits repeat resume by that same session, and denies cross-session
   resume. Consent GET and POST require the same cookie and session. Approval or denial consumes the
   request once and clears the cookie. A second decision, missing or replaced row, or replaced cookie
   fails closed and issues no second code.
4. Website Search and Document requests require session, status, epoch, Origin, CSRF, method, and body
   checks before evidence code runs.
5. A fake Search and Document call produce the same typed evidence payload through the website and
   MCP adapters, excluding protocol envelopes.
6. Embedder or corpus failure remains explicit. The service never falls back to anonymous access,
   lexical-only retrieval, another jurisdiction, cached legal text, or a generated answer.
7. OAuth metadata, redirect matching, PKCE, issuer response, scope, client binding, expiry, one-use
   codes, rotation, and replay revocation have positive and negative tests. Authorization and code
   exchange require exactly one canonical resource. Refresh accepts an omitted resource, accepts one
   exact canonical resource, and rejects every other supplied form. Each path binds the trusted
   canonical resource and verifies the persisted grant and session resource. Revocation requires
   public `client_id` and `token`, accepts optional `token_type_hint`, rejects `resource`, and
   derives and verifies the resource binding internally.
8. Browser cookies, Google tokens, and MCP tokens cannot cross their authorization boundaries.
9. Blocking takes effect on the next website, MCP, authorize, consent, exchange, or refresh request.
   The transaction revokes credentials and records the actor and reason. Unblocking restores none.
10. Multiple server instances and restart preserve transactions, sessions, grants, token families,
   status, epoch, revocation, and audit records.
11. Logs, OAuth credential columns, and error responses contain none of the forbidden credential,
    provider-token, email, callback, or evidence-request values. A signed-in account view may render
    the current profile email but never credential material.
12. Landing, connection, privacy, terms, support, publishing, deployment, and machine-readable copy
    match authenticated behavior before cutover.
13. Every non-health route rejects a noncanonical or forwarded-only authority before setting cookies
    or accepting bearer credentials. Reverse-proxy tests preserve the exact canonical `Host`.
14. Database role tests prove that only the migration owner can migrate, `PUBLIC` has no unintended
    privileges, runtime and operator roles cannot assume owner rights, runtime cannot alter status or
    epoch, operator changes use only the restricted functions, and `SECURITY DEFINER` functions
    retain their fixed safe `search_path` under a hostile caller path.
15. Authorization-code or refresh-token replay atomically revokes the family and records one
    secret-free `auth.oauth_security_event`. Tests prove that the function derives account and
    request IDs from the grant and that the runtime cannot write the audit table directly.
16. Expiry and retention indexes support bounded cleanup. Cleanup removes expired transient rows,
    retains a grant family through `retain_until`, preserves replay evidence for that period, and
    never changes either append-only audit table.
17. The initial migration includes the reviewed functions and grants. Later changes append Goose
    migrations and preserve every trusted migration's path and bytes.
18. Startup with Google discovery unavailable still serves public pages and existing sessions.
    Login and callback discovery requests use bounded timeouts and recover when Google becomes
    available.

Passing unit and route tests proves coded behavior. Passing hosted database tests proves persistence
behavior. Real MCP-client smoke tests prove compatibility. Only an authorized deployment and
post-deployment checks prove deployed behavior.

## Deployment boundary

Implementation, hosted verification, and deployment authority follow
[AGENTS.md roles and dispatch](../../AGENTS.md#roles-and-dispatch) and
[Git rules](../../AGENTS.md#git). This design does not authorize deployment. The current AWS
service remains public and unchanged during the work. A later cutover requires the selected canonical
domain, Google production credentials, preregistered client records, privacy and retention approval,
authentication database backup and recovery procedures, and real-client smoke results. AWS remains
the authoritative identity writer until a separately approved VNGCloud transition can preserve the
latest status, epochs, revocations, and audit state.

## References

- [Google OpenID Connect](https://developers.google.com/identity/openid-connect/openid-connect)
- [OAuth 2.0 Protected Resource Metadata](https://www.rfc-editor.org/rfc/rfc9728)
- [OAuth 2.0 Authorization Server Metadata](https://www.rfc-editor.org/rfc/rfc8414)
- [OAuth 2.0 Authorization Server Issuer Identification](https://www.rfc-editor.org/rfc/rfc9207)
- [OAuth 2.0 Resource Indicators](https://www.rfc-editor.org/rfc/rfc8707)
- [OAuth 2.0 for Native Apps](https://www.rfc-editor.org/rfc/rfc8252)
- [MCP authorization specification, 2026-07-28](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization)
- [Ory Fosite](https://github.com/ory/fosite)
