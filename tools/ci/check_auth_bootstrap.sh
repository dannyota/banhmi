#!/usr/bin/env bash

set -euo pipefail

readonly bootstrap_script="deploy/authmigrations/bootstrap/grant_roles.sql"

if [[ ! -f "${bootstrap_script}" ]]; then
  printf 'missing auth role bootstrap: %s\n' "${bootstrap_script}" >&2
  exit 1
fi

assert_no_capability_memberships() {
  local membership_count
  membership_count="$(
    psql \
      --no-psqlrc \
      --tuples-only \
      --no-align \
      --set ON_ERROR_STOP=1 \
      --command "
        SELECT count(*)
        FROM pg_catalog.pg_auth_members AS membership
        JOIN pg_catalog.pg_roles AS parent ON parent.oid = membership.roleid
        JOIN pg_catalog.pg_roles AS member ON member.oid = membership.member
        WHERE (parent.rolname = 'banhmi_auth_runtime' AND member.rolname = 'banhmi_auth_app')
           OR (parent.rolname = 'banhmi_auth_operator' AND member.rolname = 'banhmi_auth_ops')
      "
  )"
  if [[ "${membership_count}" != "0" ]]; then
    printf 'failed bootstrap left %s auth capability membership(s)\n' "${membership_count}" >&2
    exit 1
  fi
}

expect_missing_variable_failure() {
  local missing_variable="$1"
  shift

  if psql \
    --no-psqlrc \
    "$@" \
    --file "${bootstrap_script}"; then
    printf 'auth bootstrap succeeded without %s\n' "${missing_variable}" >&2
    exit 1
  fi
  assert_no_capability_memberships
}

expect_missing_variable_failure \
  runtime_login_role \
  --set operator_login_role=banhmi_auth_ops
expect_missing_variable_failure \
  operator_login_role \
  --set runtime_login_role=banhmi_auth_app
