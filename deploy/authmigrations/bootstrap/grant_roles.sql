-- Run with psql variables that name existing deployment login roles:
-- psql -v runtime_login_role=... -v operator_login_role=... -f grant_roles.sql
-- The login roles may inherit the assigned capability. Neither capability role may receive
-- migration-owner membership.

\if :{?runtime_login_role}
GRANT banhmi_auth_runtime TO :"runtime_login_role";
\else
\error runtime_login_role is required
\endif

\if :{?operator_login_role}
GRANT banhmi_auth_operator TO :"operator_login_role";
\else
\error operator_login_role is required
\endif

