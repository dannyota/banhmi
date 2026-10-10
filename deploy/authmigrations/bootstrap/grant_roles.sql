-- Run with psql variables that name existing deployment login roles:
-- psql -v runtime_login_role=... -v operator_login_role=... -f grant_roles.sql
-- The login roles may inherit the assigned capability. Neither capability role may receive
-- migration-owner membership.

\set ON_ERROR_STOP on

\if :{?runtime_login_role}
\else
DO $bootstrap$
BEGIN
    RAISE EXCEPTION 'runtime_login_role is required';
END
$bootstrap$;
\endif

\if :{?operator_login_role}
\else
DO $bootstrap$
BEGIN
    RAISE EXCEPTION 'operator_login_role is required';
END
$bootstrap$;
\endif

GRANT banhmi_auth_runtime TO :"runtime_login_role";
GRANT banhmi_auth_operator TO :"operator_login_role";

