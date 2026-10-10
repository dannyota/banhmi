# Google access implementation plan

> Use subagent-driven development for the bounded tasks below. The primary owns Git operations and hosted verification. Implement with Sol at medium effort; independent Sol reviewers use xhigh effort.

**Goal:** Let users register with Google, search Vietnamese evidence on the website, and authorize an MCP client through the same account.

**Architecture:** One Go process serves one configured origin. A dedicated PostgreSQL auth database holds accounts and credentials. Website and MCP adapters share the existing evidence operations. Google proves identity; Fosite issues MCP credentials.

**Stack:** Go, PostgreSQL, sqlc, embedded HTML/CSS/JavaScript, Google OpenID Connect, Fosite v0.49.0, and the existing MCP Go SDK.

**Specifications:** [Authentication](../design/AUTH.md), [OAuth storage](../design/OAUTH-STORAGE.md), and [product direction](../design/ASEAN-BANKING-MCP.md).

## Global constraints

- Google is the only login provider. First successful login creates the account.
- Require verified email. Identity is canonical Google issuer plus case-sensitive subject, never email.
- One trusted HTTPS origin defines every callback, issuer, and MCP resource URL. Never derive those values from request headers.
- Website sessions and MCP bearer tokens cannot substitute for one another.
- Preserve five MCP tools, stored evidence, citations, validity, provenance, gaps, and retrieval defaults.
- The first website serves Vietnam explicitly. Do not introduce subject filters without indexed assignments.
- Keep auth state outside corpus databases and corpus migrations.
- Use maintained protocol libraries. Do not implement signature or PKCE validation from scratch.
- New hosted serving fails closed when configuration or authorization storage is unavailable. No static-key or anonymous fallback.
- Run dependency resolution, generation, formatting, builds, tests, and database checks on GitHub runners only.
- Read, edit, and review files on the workstation. Never install dependencies or run the application there.
- Keep public documents free of workstation paths and unrelated repository references.
- No cloud, DNS, Google project, production database, or deployed AWS changes in this phase.
- Publish feature commits for CI under the owner's existing commit-and-push authorization. Do not merge or deploy.

## Review focus

1. Callback and consent replay must never create a second credential or authorize another browser's account.
2. Concurrent refresh and block operations must preserve revocation, including when OAuth returns an error.
3. Missing configuration, removed clients, stale epochs, database errors, and wrong resources must fail closed before evidence runs.
4. The website must show the same citation, validity, provenance, relation, and gap payloads as MCP.
5. Errors, logs, stored request snapshots, and CI artifacts must exclude runtime secrets and raw account query histories.

## Delivery boundaries

Account status and transactional revocation are foundations for the later private admin portal. This phase does not implement admin UI, subject taxonomy, account linking, account-specific quota configuration, product rename, or hosting migration. Existing IP flood limits remain. Production quotas and private operator access are release gates.

The canonical domain, Google production credentials, approved client redirects, privacy retention, and production access policy are deployment inputs. Their absence does not prevent fake-provider and isolated-database verification.

## Task 1: Auth persistence and configuration

**Owner:** one implementation worker. This owner does not implement OAuth protocol handlers.

**Files:** `pkg/identity/types.go`, `pkg/identity/config.go`, `pkg/identity/store.go`, `pkg/identity/store_test.go`, `pkg/identity/store_integration_test.go`, `sql/auth/schema.sql`, `sql/auth/queries/identity.sql`, `sqlc.yaml`, `deploy/authmigrations/`, `cmd/auth-migrate/`, and `cmd/auth-cleanup/`.

**Contract:**

- `Profile` contains issuer, subject, verified email, and optional display name.
- `Principal` contains account ID, email, display name, authorization epoch, and session hash. It contains no raw credential.
- `LoginTransaction` contains hashed state, browser correlation and nonce, encrypted PKCE verifier, exact return path, optional `PendingConsentHash []byte`, and expiry. OAuth-started login records the hash of the exact pending-consent cookie. Callback and resume must reject a different cookie or consent row. Ordinary website login leaves the reference empty.
- `Store` exposes `CreateLogin(ctx, transaction) error`, `ConsumeLogin(ctx, stateHash, browserHash, now) (LoginTransaction, error)`, `CompleteLogin(ctx, profile, sessionHash, now, expiresAt) (Principal, error)`, `Session(ctx, sessionHash, now) (Principal, error)`, and `RevokeSession(ctx, sessionHash, now) error`.
- Hash values are `[]byte`; timestamps are `time.Time`; context is `context.Context`.
- `NewPostgresStore(pool *pgxpool.Pool) *PostgresStore` implements `Store` through generated sqlc queries.
- Export distinct `ErrUnauthenticated` and `ErrBlocked` sentinels. Storage failures remain distinct and are sanitized at HTTP boundaries.
- `Config` includes the public origin, runtime auth database URL, Google client ID/secret, and a 32-byte transaction key. `BANHMI_AUTH_DATABASE_URL` is runtime-only; `BANHMI_AUTH_MIGRATION_DATABASE_URL` is used only by the explicit migration command with a distinct owner credential. Read secret fields from the process environment only. Configuration errors name fields, never values.
- Keep provider-specific HTTP construction out of persistence. The server composition owns pools and migration execution remains an explicit separate command.

- [ ] Write account/session/login tests before implementation. Cover changed email, duplicate subjects, one-use callback transactions, expired/revoked sessions, account epochs, block/unblock, and rollback.
- [ ] Define the account, login, website-session, consent, OAuth grant/session, and audit schema from the two specifications. Single-column surrogate primary keys remain the rule.
- [ ] Consent has an optional bound website session and one final consumed timestamp. Resume conditionally binds the session without consuming the row. Same-session resume is idempotent; another session is rejected. Approval or denial consumes the row once and clears the pending cookie.
- [ ] Add identity query sources and sqlc configuration. Do not hand-edit generated output.
- [ ] Add an explicit auth-only migration command. It must never apply corpus migrations or install pgvector. Generate the initial migration and its checksum on GitHub from the new schema.
- [ ] Implement store methods, with account-row locking for login/session issuance and private block operations. Profile upserts cannot clear status or epoch.
- [ ] Add private SQL block/unblock functions with a required reason and database-session audit actor. Block revokes website sessions and OAuth families in the same transaction. Unblock restores none.
- [ ] Add append-only `auth.oauth_security_event` with grant, account, request ID, event kind, and occurrence time. `auth.record_oauth_security_event(grant_id bigint, event_kind text)` derives account/request from the grant and accepts only `code_replay` or `refresh_replay`. Runtime receives function execution only. Family revocation and its replay audit commit together; ordinary failures roll back both.
- [ ] Provide separate no-login `banhmi_auth_runtime` and `banhmi_auth_operator` roles and explicit bootstrap grants to configurable deployment login roles. The schema owner runs migrations. Runtime receives only required table/column privileges and cannot update status/epoch or audit records. Operator receives the lookup view plus block/unblock function execution, no underlying writes. Revoke PUBLIC privileges and use a fixed safe search path for SECURITY DEFINER functions.
- [ ] Index expiry and retention columns. Add an explicit bounded cleanup command using the migration-owner connection. Delete expired login, consent, and website sessions in bounded batches. Delete grant families only after `retain_until`, preserving consumed code/refresh replay evidence until then. Production scheduling and audit retention remain cutover gates.
- [ ] Test migrations, constraints, concurrent transaction consumption, and block serialization against a disposable GitHub PostgreSQL service.
- [ ] Test role permissions with SET ROLE: runtime and operator cannot bypass status/epoch transitions or directly alter audit records; operator cannot write credential tables.
- [ ] Gate database tests on `!testing.Short()` and `BANHMI_AUTH_TEST_DATABASE_URL`; the hosted integration job supplies an isolated owner connection and uses SET ROLE to prove restrictions.
- [ ] Freeze and independently review these types before dependent workers implement against them.

## Task 2: Hosted generation and database verification

**Files:** `.github/workflows/reusable-checks.yml`, optional `.github/workflows/generate.yml`, and `tools/ci/` scripts specific to hosted generation.

**Consumes:** auth SQL and Go source from the feature tasks. **Produces:** downloadable generated source and dependency files, plus a separate PostgreSQL integration result in the always-reported CI gate.

- [ ] Add a read-only hosted generation job for sqlc, auth migration/checksum, Go dependency sums, and formatting. Pin tools and actions to verified versions or commits. Atlas OSS omits functions and privileges, so validate the complete auth schema on plain PostgreSQL and copy its exact bytes once to `20261011000000_init.sql`, then run Atlas checksum generation. Never overwrite an existing migration or run the corpus-only tools/migragen for auth.
- [ ] Publish only a fixed allowlist of generated source, migration, dependency, and formatting outputs. Never upload environment files, credentials, logs with secrets, or whole workspaces.
- [ ] Keep generation failure visible. Do not silently accept generated drift.
- [ ] Add an isolated PostgreSQL service with synthetic credentials used only in that job. No cloud or production credentials.
- [ ] Run auth migration and concurrency tests there, separate from the short fake-backed suite.
- [ ] The primary downloads and reviews source artifacts, then commits them. Workflows cannot push source changes.
- [ ] Bootstrap in two passes: generation runs independently of application compilation and publishes its allowlisted artifact; the primary imports and commits it; then normal CI checks deterministic generation drift and builds/tests the committed tree. Missing generated packages during the first pass are pending bootstrap, not a successful build. Existing migrations are append-only; normal drift checks do not overwrite them.

## Task 3: Google login and website sessions

**Files:** new `pkg/identity` provider, session, crypto, HTTP, and unit-test files. Do not modify Task 1 files until its owner has finished and changes are coordinated.

**Consumes:** Task 1 `Store`, `Profile`, `Principal`, `LoginTransaction`, and `Config`.

**Produces:** `Manager` with `Authenticate(r *http.Request) (Principal, error)`, `CSRFToken(r *http.Request) string`, `ValidCSRF(r *http.Request, token string) bool`, and `Register(mux *http.ServeMux)` for Google start/callback and logout. `BeginLogin(w http.ResponseWriter, r *http.Request, next string, pendingConsentHash []byte)` lets trusted OAuth orchestration start a login bound to its consent cookie that returns to `/oauth/resume`. The constructor validates local configuration only and fails startup on invalid fields. Google discovery resolves lazily with bounded timeouts on login/callback and caches successful results, so a Google outage during restart does not disable existing sessions. `Seal(purpose string, plain []byte) ([]byte, error)` and `Open(purpose string, sealed []byte) ([]byte, error)` provide purpose-bound AES-GCM for short-lived transactions.

- [ ] Write fake-provider tests for state, nonce, browser binding, PKCE exchange, email verification, callback expiry/replay, session rotation, logout, and storage failure.
- [ ] Use maintained Google OIDC verification and `golang.org/x/oauth2`. Discard provider tokens after verified claim extraction.
- [ ] Persist independent random state, nonce, browser correlation, and encrypted verifier. Consume a matched login transaction once, including failed provider outcomes.
- [ ] Issue a 12-hour opaque session stored by hash. Set secure host-only HttpOnly SameSite=Lax cookies with `__Host-` names.
- [ ] Public Google start accepts only `/app` and `/connect` as return destinations. Trusted OAuth orchestration may call `BeginLogin` with `/oauth/resume` after persisting pending consent. The resume route independently requires a valid pending consent cookie.
- [ ] Protect POSTs using the exact configured Origin plus a session-bound CSRF token. Responses use no-store and generic failure messages.
- [ ] Run fake OIDC tests on GitHub. Real Google credentials are not required for those tests.

## Task 4: Shared evidence and website

**Files:** `pkg/mcp/operations.go`, focused edits/tests in `pkg/mcp`, and new `pkg/web/` handlers, templates, assets, and tests. Separate workers may own the evidence and web directories.

**Produces:** exported `SearchInput`, `SearchOutput`, `DocumentInput`, and `DocumentOutput` types with unchanged JSON fields; `(*mcp.Server).Search(ctx, SearchInput) (SearchOutput, error)` and `Document(ctx, DocumentInput) (DocumentOutput, error)`. Existing MCP handlers adapt to these methods. Website constructors receive those operations and the Task 3 manager; they do not open pools or loop back through HTTP.

- [ ] Add a parity test that compares existing MCP and exported operation evidence payloads, including errors, validity warnings, non-binding text, relations, and gaps.
- [ ] Extract operations without changing retrieval, default arguments, or MCP schemas.
- [ ] Build an indexable landing page and one-button Google login page with privacy and terms links.
- [ ] Build `/app` with a Vietnamese query box, result cards, and an inline document view. Search uses existing MCP defaults.
- [ ] Add signed-in POST `/api/v1/search` and `/api/v1/document`; enforce session, Origin, CSRF, body limit, bounded execution, and method before evidence calls.
- [ ] Build `/connect` from the configured canonical MCP resource. Explain OAuth connection without minting or displaying API keys.
- [ ] Preserve structured evidence and warnings in the UI. Escape text and constrain outgoing links to safe HTTP(S) destinations.
- [ ] Add response headers, responsive styles, keyboard labels, loading/empty/error states, noindex account pages, and no-store auth/evidence responses.
- [ ] Test cross-surface payload parity and rejection before evidence invocation. Do not introduce a frontend dependency toolchain.

## Task 5: MCP authorization

**Files:** `pkg/oauthserver/`, `sql/auth/queries/oauth.sql`, and its tests. Task 1 owns the schema; coordinate any necessary correction through that owner. Generated output comes from Task 2.

**Consumes:** the verified [OAuth storage contract](../design/OAUTH-STORAGE.md), Task 1 types/pool, Task 3 session/CSRF and encryption methods, and current client configuration.

- [ ] Pin Fosite v0.49.0 and implement only the specified public-client code, refresh, revocation, introspection, and PKCE factories.
- [ ] Implement the versioned request codec and sqlc persistence adapter. Keep scalar authorization fields authoritative and exclude raw incoming forms and secrets from snapshots.
- [ ] Wrap full token and consent operations in outer transactions; join Fosite transactions through savepoints. Test safe rollback and committed replay revocation separately. Proven replay appends through `auth.record_oauth_security_event` in the same transaction as family revocation; the runtime cannot mutate audit tables directly.
- [ ] Validate exactly one configured resource, bridge it into Fosite audience, reject caller-supplied audience, and disallow unsupported response modes.
- [ ] Add discovery, authorize, resume, consent decision, token, and revocation routes. Persist encrypted pending consent with a secure opaque browser cookie. Resume validates the cookie and conditionally binds the website session without setting consumed_at. Both consent GET and POST require that same cookie and session.
- [ ] Add explicit consent showing client, redirect hostname, resource, and scope. Approval uses CSRF and creates a single short-lived code; denial creates none.
- [ ] Emit `iss` on authorized response redirects and advertise issuer-response support. Never redirect to an unvalidated client URI.
- [ ] Protect MCP with Authorization-header bearer tokens only. Check current client configuration, account status/epoch, grant state, expiry, scope, resource, and token use before the SDK handler.
- [ ] Test changed resource/client/redirect, bad PKCE, code replay, token type confusion, refresh races/replay, removed clients, account blocking, and storage outages.
- [ ] Verify both fake-backed protocol paths and real PostgreSQL transactions on GitHub. Do not claim hosted-client compatibility from these tests.

## Task 6: Composition, documentation, and review

**Files:** `cmd/server`, `pkg/app` composition where needed, `config` examples, contributor/deployment docs, `PLAN.md`, `docs/ARCHITECTURE.md`, and documentation index.

- [ ] Open the separate auth pool and construct managers once. Select the Vietnam corpus explicitly for the new website and MCP resource.
- [ ] Replace global optional API-key authorization with route-class authorization. Keep origin verification, IP flood controls, body caps, recovery, and HTTP timeouts.
- [ ] Before cookies or bearer admission, reject non-health requests whose `r.Host` differs from the configured canonical origin authority. Never trust forwarded headers to choose authority. The reverse proxy must preserve canonical Host; do not use the legacy unknown-host fallback.
- [ ] Keep public discovery, landing, legal, support, and health routes reachable. Auth outages must not allow evidence access.
- [ ] Remove new-route public/no-signup claims and document Google registration, exact configuration, migration ordering, and cutover gates. Distinguish coded behavior from deployed AWS behavior.
- [ ] Add composed-route tests proving session/token separation, exact authority enforcement, and no wrong-jurisdiction or anonymous fallback.
- [ ] Have an independent Sol reviewer inspect the complete change and every security fix.
- [ ] Publish reviewed feature commits and verify the exact GitHub commit, generated-file drift, short suite, and database integration results.
- [ ] Record real Google/client/browser smoke checks as pending until a configured hosted test deployment exists.

## Progress

- Baseline: commit `add3886` passed [GitHub CI](https://github.com/dannyota/banhmi/actions/runs/38084176452).
- Requirements and protocol design: prepared with independent design ownership.
- Implementation, feature CI, and deployment: pending.
