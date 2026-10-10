# Authenticated access deployment

The authenticated `cmd/server` source serves one HTTPS origin and the Vietnam corpus. Website
evidence requires a Google-backed account. MCP evidence requires an OAuth access token issued to a
preregistered public client. The current AWS deployment still runs the earlier public six-domain
service. No authenticated deployment has been released or verified.

## Runtime configuration

Set these values before starting `cmd/server`. The process validates every value and the client file
before it opens a database pool or probes the query embedder.

| Variable | Requirement |
|---|---|
| `BANHMI_PUBLIC_ORIGIN` | One absolute HTTPS origin with no path, query, fragment, or user information. Every callback, issuer, cookie, and MCP resource derives from this value. |
| `BANHMI_AUTH_DATABASE_URL` | Runtime authentication database URL for a login role that inherits `banhmi_auth_runtime`. |
| `BANHMI_GOOGLE_CLIENT_ID` | Google OpenID Connect client ID. |
| `BANHMI_GOOGLE_CLIENT_SECRET` | Google OpenID Connect client secret. |
| `BANHMI_AUTH_TRANSACTION_KEY` | Base64 encoding of exactly 32 random bytes for short-lived transaction encryption. |
| `BANHMI_OAUTH_HMAC_KEY` | Base64 encoding of at least 32 random bytes for the current OAuth HMAC key. |
| `BANHMI_OAUTH_ROTATED_HMAC_KEYS` | Optional JSON array of previous OAuth HMAC keys. Each array element is a base64 string that decodes to at least 32 bytes. |
| `BANHMI_OAUTH_CLIENTS_FILE` | Path to the versioned public-client allowlist. The file is required. An empty `clients` array disables MCP connections without enabling anonymous access. |
| `BANHMI_ORIGIN_VERIFY_SECRET` | Optional proxy-to-origin verification secret. |
| `BANHMI_TRUST_PROXY` | Trust the proxy-appended client address for rate limits. Enable only with exclusive trusted proxy ingress. |
| `BANHMI_MCP_ALLOWED_ORIGINS` | Optional browser origins allowed by MCP cross-origin checks. |
| `BANHMI_MCP_RATE_RPS`, `BANHMI_MCP_RATE_BURST` | Global per-client flood limit. |
| `BANHMI_GOOGLE_RATE_RPS`, `BANHMI_GOOGLE_RATE_BURST` | Per-client Google login and logout limit. |
| `BANHMI_OAUTH_RATE_RPS`, `BANHMI_OAUTH_RATE_BURST` | Per-client OAuth protocol limit. |

Keep secret values in the deployment secret provider. Do not put them in the YAML config, the client
allowlist, command arguments, logs, or tracked files.

The client allowlist has this form:

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

Register each client and redirect URI explicitly. The process loads the complete allowlist at
startup. File changes take effect only after a process restart. After that restart, the runtime
rejects clients removed from the file and redirects that do not match byte for byte. Start with
[`config/auth-clients.example.json`](../config/auth-clients.example.json) until the owner approves a
client.

## Setup order

1. Create a dedicated PostgreSQL authentication database and separate migration, runtime, and
   operator login roles.
2. Set `BANHMI_AUTH_MIGRATION_DATABASE_URL` for the migration owner and run `cmd/auth-migrate`.
   Authentication migrations are separate from corpus migrations. The server never migrates at
   startup.
3. Apply [`deploy/authmigrations/bootstrap/grant_roles.sql`](authmigrations/bootstrap/grant_roles.sql)
   with the exact deployment login-role names. Give the server login only the runtime capability
   role.
4. Configure the Google web client with the exact redirect URI
   `${BANHMI_PUBLIC_ORIGIN}/auth/google/callback`. Google login must supply a verified email. The
   service identifies the account by Google's issuer and case-sensitive subject, not by email.
5. Review the client allowlist and mount it read-only. An empty allowlist is the safe initial state.
6. Put the service behind one HTTPS domain. The reverse proxy must preserve the canonical `Host`
   authority exactly. Do not rewrite `Host` and do not use `Forwarded` or `X-Forwarded-Host` as the
   public authority.
7. Set `BANHMI_ORIGIN_VERIFY_SECRET` when the proxy injects `X-Origin-Verify`. Set
   `BANHMI_TRUST_PROXY=true` only when direct client access to the origin is impossible and the proxy
   appends the trusted client address to `X-Forwarded-For`.
8. Set the corpus database, Vietnam query embedder, and HTTP limits. The authenticated service always
   selects Vietnam. It does not use a header or unknown host to select another corpus.

## Cleanup and release gates

Schedule `cmd/auth-cleanup -batch 1000` with the migration-owner connection in
`BANHMI_AUTH_MIGRATION_DATABASE_URL`. Run bounded batches often enough to remove expired login,
consent, website-session, and retained OAuth grant rows. Define the schedule, audit retention, job
monitoring, and failure alert before release.

Do not cut traffic over until all of these gates have evidence:

- GitHub build, vet, short tests, generated-file drift, and isolated authentication database tests
  pass for the exact commit.
- A hosted test deployment proves Google login with a verified email, logout, browser session
  rotation, an approved client authorization flow, token refresh, revocation, and rejected replay.
- Website cookies cannot authorize `/mcp`, and OAuth bearer tokens cannot authorize website APIs.
- An authentication database outage leaves `/healthz` available but denies evidence before retrieval.
- Unknown and forwarded hosts fail while the canonical host succeeds through the reverse proxy.
- The privacy notice states the account data, cookies, infrastructure logging retention, Google
  involvement, and that the service stores no account query history.
- The owner approves the canonical domain, Google consent configuration, client redirects, quotas,
  operator access, retention, backup, restore, rollback, and incident procedure.

Private admin UI, account-specific quotas, product rename, subject organization, and production
hosting migration remain later work.
