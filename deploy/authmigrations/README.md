# Authentication migrations

This directory contains only timestamped Goose migrations for the dedicated authentication database
and the Atlas checksum file. The initial migration is an exact copy of `sql/auth/schema.sql` named
`20261011000000_init.sql`. GitHub creates that file and `atlas.sum` once. Later schema changes add a
new migration and keep all existing migration bytes unchanged.

`cmd/auth-migrate` reads only `BANHMI_AUTH_MIGRATION_DATABASE_URL`. The website and MCP runtime use
`BANHMI_AUTH_DATABASE_URL` with a separate login role.

The schema creates the `banhmi_auth_runtime` and `banhmi_auth_operator` NOLOGIN roles. After migration,
run `bootstrap/grant_roles.sql` with explicit deployment login-role names. Deployment login roles may
inherit the granted capability role. Neither capability role receives migration-owner membership.
The bootstrap file stays outside this directory's migration glob and checksum.

