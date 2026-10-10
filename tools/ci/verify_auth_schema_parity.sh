#!/usr/bin/env bash

set -euo pipefail

if [[ -z "${CANONICAL_POSTGRES_CONTAINER_ID:-}" || -z "${MIGRATED_POSTGRES_CONTAINER_ID:-}" ]]; then
  printf 'canonical and migrated PostgreSQL container IDs are required\n' >&2
  exit 1
fi

readonly output_dir="${RUNNER_TEMP:-/tmp}/banhmi-auth-schema-parity"
readonly canonical_dump="${output_dir}/canonical.sql"
readonly migration_dump="${output_dir}/migration.sql"
readonly canonical_roles="${output_dir}/canonical-roles.txt"
readonly migration_roles="${output_dir}/migration-roles.txt"
readonly canonical_memberships="${output_dir}/canonical-memberships.txt"
readonly migration_memberships="${output_dir}/migration-memberships.txt"

if [[ -e "${output_dir}" ]]; then
  printf 'refusing to replace existing parity output: %s\n' "${output_dir}" >&2
  exit 1
fi
mkdir -p "${output_dir}"

dump_auth_schema() {
  local container_id="$1"
  local database_name="$2"
  local output_file="$3"

  docker exec "${container_id}" \
    pg_dump \
      --username banhmi_auth_owner \
      --dbname "${database_name}" \
      --schema auth \
      --schema-only \
      --no-owner |
    sed -e '/^\\restrict /d' -e '/^\\unrestrict /d' > "${output_file}"
}

dump_auth_roles() {
  local container_id="$1"
  local database_name="$2"
  local roles_file="$3"
  local memberships_file="$4"

  docker exec "${container_id}" \
    psql \
      --username banhmi_auth_owner \
      --dbname "${database_name}" \
      --no-psqlrc \
      --tuples-only \
      --no-align \
      --command "
        SELECT rolname, rolsuper, rolinherit, rolcreaterole, rolcreatedb,
               rolcanlogin, rolreplication, rolbypassrls, rolconnlimit
        FROM pg_catalog.pg_roles
        WHERE rolname IN ('banhmi_auth_runtime', 'banhmi_auth_operator')
        ORDER BY rolname
      " > "${roles_file}"

  docker exec "${container_id}" \
    psql \
      --username banhmi_auth_owner \
      --dbname "${database_name}" \
      --no-psqlrc \
      --tuples-only \
      --no-align \
      --command "
        SELECT parent.rolname, member.rolname, membership.admin_option,
               membership.inherit_option, membership.set_option
        FROM pg_catalog.pg_auth_members AS membership
        JOIN pg_catalog.pg_roles AS parent ON parent.oid = membership.roleid
        JOIN pg_catalog.pg_roles AS member ON member.oid = membership.member
        WHERE parent.rolname IN ('banhmi_auth_runtime', 'banhmi_auth_operator')
           OR member.rolname IN ('banhmi_auth_runtime', 'banhmi_auth_operator')
        ORDER BY parent.rolname, member.rolname
      " > "${memberships_file}"
}

compare_files() {
  local label="$1"
  local canonical_file="$2"
  local migration_file="$3"
  local parity_diff="${output_dir}/${label}.diff"

  if ! diff -u "${canonical_file}" "${migration_file}" > "${parity_diff}"; then
    printf 'canonical auth %s differs from the generated migration chain:\n' "${label}" >&2
    sed -n '1,240p' "${parity_diff}" >&2
    exit 1
  fi
}

dump_auth_schema \
  "${CANONICAL_POSTGRES_CONTAINER_ID}" auth_schema_validation "${canonical_dump}"
dump_auth_schema \
  "${MIGRATED_POSTGRES_CONTAINER_ID}" auth_migration_validation "${migration_dump}"
dump_auth_roles \
  "${CANONICAL_POSTGRES_CONTAINER_ID}" auth_schema_validation \
  "${canonical_roles}" "${canonical_memberships}"
dump_auth_roles \
  "${MIGRATED_POSTGRES_CONTAINER_ID}" auth_migration_validation \
  "${migration_roles}" "${migration_memberships}"

compare_files schema "${canonical_dump}" "${migration_dump}"
compare_files roles "${canonical_roles}" "${migration_roles}"
compare_files memberships "${canonical_memberships}" "${migration_memberships}"
