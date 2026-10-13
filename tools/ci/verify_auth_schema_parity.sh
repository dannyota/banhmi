#!/usr/bin/env bash
set -euo pipefail
readonly output_dir="${VALIDATION_DIR:?}/auth-parity"
mkdir "${output_dir}"
: "${CANONICAL_POSTGRES_CONTAINER_ID:?}"
: "${MIGRATED_POSTGRES_CONTAINER_ID:?}"
: "${UPGRADE_POSTGRES_CONTAINER_ID:?}"
bash tools/ci/build_base_migrator.sh auth-migrate
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

  if ! cmp -s "${canonical_file}" "${migration_file}"; then
    printf 'canonical auth %s differs from the generated migration chain:\n' "${label}" >&2
    printf 'auth parity failed; private comparison remains in runner temporary storage\n' >&2
    exit 1
  fi
}

readonly owner_url='postgres://banhmi_auth_owner:ci-owner-password@localhost'
BANHMI_AUTH_MIGRATION_DATABASE_URL="${owner_url}:5433/auth_migration_validation?sslmode=disable" \
  go run ./cmd/auth-migrate
BANHMI_AUTH_MIGRATION_DATABASE_URL="${owner_url}:5434/auth_upgrade_validation?sslmode=disable" \
  "${VALIDATION_DIR}/base-auth-migrate-binary"
PGPORT=5434 PGDATABASE=auth_upgrade_validation psql --set ON_ERROR_STOP=1 >/dev/null <<'SQL'
INSERT INTO auth.user_account (issuer, subject, email, first_seen_at, last_login_at)
VALUES ('https://ci.invalid', 'legacy-account', 'legacy@example.invalid', '2026-01-01', '2026-01-01');
SQL
ledger_limit="$(PGPORT=5434 PGDATABASE=auth_upgrade_validation psql --set ON_ERROR_STOP=1 --tuples-only --no-align --command 'SELECT max(id) FROM public.goose_db_version_auth')"
snapshot_upgrade() {
  local freeze_ledger="${1:-true}"
  local database="${2:-auth_upgrade_validation}"
  PGPORT=5434 PGDATABASE="${database}" psql --set ON_ERROR_STOP=1 --set ledger_limit="${ledger_limit}" \
    --set freeze_ledger="${freeze_ledger}" --tuples-only --no-align <<'SQL'
SELECT row_to_json(account) FROM auth.user_account account ORDER BY id;
SELECT last_value, is_called FROM auth.user_account_id_seq;
SELECT row_to_json(ledger) FROM public.goose_db_version_auth ledger WHERE NOT :'freeze_ledger'::boolean OR id <= :'ledger_limit'::bigint ORDER BY id;
SQL
}
snapshot_upgrade > "${output_dir}/before.txt"
BANHMI_AUTH_MIGRATION_DATABASE_URL="${owner_url}:5434/auth_upgrade_validation?sslmode=disable" \
  go run ./cmd/auth-migrate
snapshot_upgrade > "${output_dir}/after.txt"
cmp "${output_dir}/before.txt" "${output_dir}/after.txt"
dump_auth_schema "${MIGRATED_POSTGRES_CONTAINER_ID}" auth_migration_validation "${output_dir}/clean.sql"
dump_auth_schema "${UPGRADE_POSTGRES_CONTAINER_ID}" auth_upgrade_validation "${output_dir}/upgrade.sql"
dump_auth_roles "${MIGRATED_POSTGRES_CONTAINER_ID}" auth_migration_validation "${output_dir}/clean-roles.txt" "${output_dir}/clean-memberships.txt"
dump_auth_roles "${UPGRADE_POSTGRES_CONTAINER_ID}" auth_upgrade_validation "${output_dir}/upgrade-roles.txt" "${output_dir}/upgrade-memberships.txt"
compare_files schema "${output_dir}/clean.sql" "${output_dir}/upgrade.sql"
compare_files roles "${output_dir}/clean-roles.txt" "${output_dir}/upgrade-roles.txt"
compare_files memberships "${output_dir}/clean-memberships.txt" "${output_dir}/upgrade-memberships.txt"
transition=not-applicable
if git cat-file -e "${BASE_SHA}:sql/auth/schema.sql" 2>/dev/null; then
  git show "${BASE_SHA}:sql/auth/schema.sql" > "${output_dir}/canonical-source.sql"
  psql --set ON_ERROR_STOP=1 --single-transaction --file "${output_dir}/canonical-source.sql" >/dev/null
  dump_auth_schema "${CANONICAL_POSTGRES_CONTAINER_ID}" auth_schema_validation "${output_dir}/canonical.sql"
  dump_auth_roles "${CANONICAL_POSTGRES_CONTAINER_ID}" auth_schema_validation "${output_dir}/canonical-roles.txt" "${output_dir}/canonical-memberships.txt"
  compare_files schema "${output_dir}/canonical.sql" "${output_dir}/clean.sql"
  compare_files roles "${output_dir}/canonical-roles.txt" "${output_dir}/clean-roles.txt"
  compare_files memberships "${output_dir}/canonical-memberships.txt" "${output_dir}/clean-memberships.txt"
  transition=passed
fi
snapshot_upgrade false > "${output_dir}/candidate-before.txt"
BANHMI_AUTH_MIGRATION_DATABASE_URL="${owner_url}:5434/auth_upgrade_validation?sslmode=disable" \
  go run ./cmd/auth-migrate
snapshot_upgrade false > "${output_dir}/replayed.txt"
cmp "${output_dir}/candidate-before.txt" "${output_dir}/replayed.txt"
# Verify the same full-ledger snapshot rejects an append after no-op replay.
readonly ledger_negative_database=auth_ledger_negative_ci
PGPORT=5434 createdb --template auth_upgrade_validation "${ledger_negative_database}"
snapshot_upgrade false "${ledger_negative_database}" > "${output_dir}/ledger-negative-before.txt"
PGPORT=5434 PGDATABASE="${ledger_negative_database}" psql --set ON_ERROR_STOP=1 >/dev/null <<'SQL'
INSERT INTO public.goose_db_version_auth (id, version_id, is_applied)
SELECT coalesce(max(id), 0) + 1, 9223372036854775806, true FROM public.goose_db_version_auth;
SQL
snapshot_upgrade false "${ledger_negative_database}" > "${output_dir}/ledger-negative-after.txt"
if cmp -s "${output_dir}/ledger-negative-before.txt" "${output_dir}/ledger-negative-after.txt"; then
  printf 'no-op replay evidence ignored an appended auth ledger row\n' >&2
  exit 1
fi
dump_auth_schema "${UPGRADE_POSTGRES_CONTAINER_ID}" auth_upgrade_validation "${output_dir}/replayed.sql"
compare_files idempotence "${output_dir}/upgrade.sql" "${output_dir}/replayed.sql"
dump_auth_roles "${UPGRADE_POSTGRES_CONTAINER_ID}" auth_upgrade_validation "${output_dir}/replayed-roles.txt" "${output_dir}/replayed-memberships.txt"
compare_files roles-idempotence "${output_dir}/upgrade-roles.txt" "${output_dir}/replayed-roles.txt"
compare_files memberships-idempotence "${output_dir}/upgrade-memberships.txt" "${output_dir}/replayed-memberships.txt"
PGPORT=5434 PGDATABASE=auth_upgrade_validation psql --set ON_ERROR_STOP=1 >/dev/null <<'SQL'
DO $$
DECLARE prior bigint; allocated bigint;
BEGIN
    SELECT last_value INTO prior FROM auth.user_account_id_seq;
    allocated := nextval('auth.user_account_id_seq');
    IF allocated <> prior + 1 THEN
        RAISE EXCEPTION 'auth identity sequence changed after upgrade';
    END IF;
END $$;
SQL
{
  printf '# Auth verification\n\n'
  printf -- '- Candidate: %s\n- Trusted base: %s\n' "${CANDIDATE_SHA}" "${BASE_SHA}"
  printf -- '- PostgreSQL: %s\n' "$(psql --tuples-only --no-align --command 'SHOW server_version')"
  printf -- '- Transition canonical comparison: %s\n' "${transition}"
  python3 - "${VALIDATION_DIR}/history.json" <<'PY'
import json
import sys
report = json.load(open(sys.argv[1]))
print('- Migration comparison range: ' + ('SQL additions present' if report['migration_additions'] else 'no SQL additions; structural and replay checks only'))
PY
  printf -- '- Independent clean/upgrade role states, schema, roles, and memberships: passed\n'
  printf -- '- Populated legacy account, identity sequence, inherited ledger retention, and full-ledger replay idempotence: passed\n'
  printf -- '- Appended auth ledger row rejected by no-op replay comparison: passed\n'
} > "${VALIDATION_DIR}/auth-verification.md"
