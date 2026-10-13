# Authentication migrations

This directory contains Goose migrations for the dedicated authentication database. The initial
migration is `20261011000000_init.sql`. The approved migration transition makes these migration
files the only schema source and configures sqlc to read this directory, excluding bootstrap SQL.
Trusted Git history checks replace Atlas checksums. The transition is under implementation; no
hosted generation or parity result is accepted yet. Later schema changes append migrations and
preserve every trusted migration path and byte.

`cmd/auth-migrate` reads only `BANHMI_AUTH_MIGRATION_DATABASE_URL`. The website and MCP runtime use
`BANHMI_AUTH_DATABASE_URL` with a separate login role.

The schema creates the `banhmi_auth_runtime` and `banhmi_auth_operator` NOLOGIN roles. After migration,
run `bootstrap/grant_roles.sql` with explicit deployment login-role names. Deployment login roles may
inherit the granted capability role. Neither capability role receives migration-owner membership.
The bootstrap file stays outside this directory's migration glob.

