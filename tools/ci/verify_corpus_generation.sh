#!/usr/bin/env bash

set -euo pipefail

readonly runner_temp="${RUNNER_TEMP:-/tmp}"
readonly baseline_migrate="${CORPUS_BASELINE_MIGRATE:-${runner_temp}/banhmi-corpus-migrate-baseline}"
readonly manifest="${CORPUS_NEW_MIGRATIONS:-${runner_temp}/banhmi-corpus-new-migrations.txt}"
readonly report="${CORPUS_VERIFICATION_REPORT:-${runner_temp}/banhmi-corpus-verification.md}"
readonly runtime_config="config/config.example.yaml"
readonly canonical_database="banhmi_corpus_canonical_ci"
readonly clean_database="banhmi_corpus_clean_ci"
readonly upgrade_database="banhmi_corpus_upgrade_ci"
readonly test_database="banhmi_corpus_schema_test_ci"
readonly schemas=(bronze silver gold ingest config)

if [[ ! -x "${baseline_migrate}" || ! -f "${manifest}" ]]; then
  printf 'corpus generation must produce the baseline migrator and new migration manifest first\n' >&2
  exit 1
fi
if [[ -z "${PGHOST:-}" || -z "${PGPORT:-}" || -z "${PGUSER:-}" ]]; then
  printf 'PGHOST, PGPORT, and PGUSER are required for corpus verification\n' >&2
  exit 1
fi

export BANHMI_DATABASE_HOST="${PGHOST}"
export BANHMI_DATABASE_PORT="${PGPORT}"
export BANHMI_DATABASE_USER="${PGUSER}"
export BANHMI_DATABASE_SSLMODE=disable
export BANHMI_DATABASE_PASSWORD="${PGPASSWORD:-}"

for database in \
  "${canonical_database}" "${clean_database}" "${upgrade_database}" "${test_database}"; do
  dropdb --if-exists --force "${database}"
  createdb "${database}"
done

psql --dbname "${canonical_database}" --set ON_ERROR_STOP=1 \
  --command 'CREATE EXTENSION vector'
for schema in "${schemas[@]}"; do
  psql --dbname "${canonical_database}" --set ON_ERROR_STOP=1 \
    --single-transaction --file "sql/${schema}/schema.sql"
done

BANHMI_DATABASE_NAME="${clean_database}" \
  go run ./cmd/migrate -config "${runtime_config}"

BANHMI_DATABASE_NAME="${upgrade_database}" \
  "${baseline_migrate}" -config "${runtime_config}"
BANHMI_DATABASE_NAME="${upgrade_database}" \
  go run ./cmd/migrate -config "${runtime_config}"

BANHMI_DATABASE_NAME="${test_database}" \
  go run ./cmd/migrate -config "${runtime_config}"
if [[ ! -f internal/corpus/schematest/schema_integration_test.go ]] || \
   ! grep -q '^func Test' internal/corpus/schematest/schema_integration_test.go; then
  printf 'corpus schema integration test suite is missing\n' >&2
  exit 1
fi
readonly test_database_url="postgres://${PGUSER}@${PGHOST}:${PGPORT}/${test_database}?sslmode=disable"
readonly schema_test_output="${runner_temp}/banhmi-corpus-schema-test.txt"
BANHMI_CORPUS_SCHEMA_TEST_DATABASE_URL="${test_database_url}" \
  go test -count=1 -v ./internal/corpus/schematest | tee "${schema_test_output}"
if grep -q -- '^--- SKIP:' "${schema_test_output}"; then
  printf 'corpus schema integration test suite skipped a selected test\n' >&2
  exit 1
fi
if ! grep -q -- '^--- PASS: Test' "${schema_test_output}"; then
  printf 'corpus schema integration test suite ran no selected tests\n' >&2
  exit 1
fi

dump_database() {
  local database="$1"
  local destination="$2"
  psql --dbname "${database}" --set ON_ERROR_STOP=1 \
    --tuples-only --no-align --field-separator $'\t' > "${destination}" <<'SQL'
WITH target_namespace AS (
    SELECT oid, nspname
    FROM pg_namespace
    WHERE nspname IN ('bronze', 'silver', 'gold', 'ingest', 'config')
), definitions AS (
    SELECT 'schema' AS kind, namespace.nspname AS object_name, '' AS definition
    FROM target_namespace AS namespace
    UNION ALL
    SELECT 'relation', namespace.nspname || '.' || relation.relname,
           concat_ws('|', relation.relkind, relation.relpersistence,
               relation.relrowsecurity::text, relation.relforcerowsecurity::text,
               coalesce(array_to_string(relation.reloptions, ','), '<null>'))
    FROM pg_class AS relation
    JOIN target_namespace AS namespace ON namespace.oid = relation.relnamespace
    WHERE relation.relkind IN ('r', 'p', 'S', 'v', 'm')
    UNION ALL
    SELECT 'column',
           namespace.nspname || '.' || relation.relname || '.' || attribute.attname,
           concat_ws('|',
               format_type(attribute.atttypid, attribute.atttypmod),
               attribute.attnotnull::text,
               attribute.attidentity,
               attribute.attgenerated,
               coalesce(collation.collname, '<default>'),
               coalesce(pg_get_expr(default_value.adbin, default_value.adrelid), '<null>')
           )
    FROM pg_class AS relation
    JOIN target_namespace AS namespace ON namespace.oid = relation.relnamespace
    JOIN pg_attribute AS attribute ON attribute.attrelid = relation.oid
    LEFT JOIN pg_attrdef AS default_value
      ON default_value.adrelid = relation.oid AND default_value.adnum = attribute.attnum
    LEFT JOIN pg_collation AS collation ON collation.oid = attribute.attcollation
    WHERE relation.relkind IN ('r', 'p')
      AND attribute.attnum > 0
      AND NOT attribute.attisdropped
    UNION ALL
    SELECT 'sequence', namespace.nspname || '.' || relation.relname,
           concat_ws('|', sequence_value.seqtypid::regtype::text,
               sequence_value.seqstart::text, sequence_value.seqincrement::text,
               sequence_value.seqmax::text, sequence_value.seqmin::text,
               sequence_value.seqcache::text, sequence_value.seqcycle::text)
    FROM pg_sequence AS sequence_value
    JOIN pg_class AS relation ON relation.oid = sequence_value.seqrelid
    JOIN target_namespace AS namespace ON namespace.oid = relation.relnamespace
    UNION ALL
    SELECT 'constraint',
           namespace.nspname || '.' || relation.relname || '.' || constraint_value.conname,
           pg_get_constraintdef(constraint_value.oid, true)
    FROM pg_constraint AS constraint_value
    JOIN pg_class AS relation ON relation.oid = constraint_value.conrelid
    JOIN target_namespace AS namespace ON namespace.oid = relation.relnamespace
    UNION ALL
    SELECT 'index', namespace.nspname || '.' || index_value.relname,
           pg_get_indexdef(index_value.oid, 0, true)
    FROM pg_index AS index_metadata
    JOIN pg_class AS index_value ON index_value.oid = index_metadata.indexrelid
    JOIN target_namespace AS namespace ON namespace.oid = index_value.relnamespace
    UNION ALL
    SELECT 'function',
           namespace.nspname || '.' || function_value.proname || '(' ||
             pg_get_function_identity_arguments(function_value.oid) || ')',
           pg_get_functiondef(function_value.oid)
    FROM pg_proc AS function_value
    JOIN target_namespace AS namespace ON namespace.oid = function_value.pronamespace
    UNION ALL
    SELECT 'trigger',
           namespace.nspname || '.' || relation.relname || '.' || trigger_value.tgname,
           pg_get_triggerdef(trigger_value.oid, true)
    FROM pg_trigger AS trigger_value
    JOIN pg_class AS relation ON relation.oid = trigger_value.tgrelid
    JOIN target_namespace AS namespace ON namespace.oid = relation.relnamespace
    WHERE NOT trigger_value.tgisinternal
    UNION ALL
    SELECT 'view', namespace.nspname || '.' || relation.relname,
           pg_get_viewdef(relation.oid, true)
    FROM pg_class AS relation
    JOIN target_namespace AS namespace ON namespace.oid = relation.relnamespace
    WHERE relation.relkind IN ('v', 'm')
)
SELECT kind, object_name, definition
FROM definitions
ORDER BY kind, object_name, definition;
SQL
}

readonly canonical_dump="${runner_temp}/banhmi-corpus-canonical.sql"
readonly clean_dump="${runner_temp}/banhmi-corpus-clean.sql"
readonly upgrade_dump="${runner_temp}/banhmi-corpus-upgrade.sql"
dump_database "${canonical_database}" "${canonical_dump}"
dump_database "${clean_database}" "${clean_dump}"
dump_database "${upgrade_database}" "${upgrade_dump}"
diff -u "${canonical_dump}" "${clean_dump}"
diff -u "${canonical_dump}" "${upgrade_dump}"

postgres_version="$(psql --dbname "${canonical_database}" --set ON_ERROR_STOP=1 \
  --tuples-only --no-align \
  --command 'SHOW server_version')"
readonly postgres_version
vector_version="$(psql --dbname "${canonical_database}" --set ON_ERROR_STOP=1 \
  --tuples-only --no-align \
  --command "SELECT extversion FROM pg_extension WHERE extname = 'vector'")"
readonly vector_version
if [[ -z "${postgres_version}" || -z "${vector_version}" ]]; then
  printf 'PostgreSQL and pgvector versions must be present in verification output\n' >&2
  exit 1
fi

{
  printf '# Corpus source generation verification\n\n'
  # Markdown backticks are literal, not shell command substitutions.
  # shellcheck disable=SC2016
  printf -- '- Commit: `%s`\n' "${GITHUB_SHA:-unpublished-worktree}"
  # Markdown backticks are literal, not shell command substitutions.
  # shellcheck disable=SC2016
  printf -- '- PostgreSQL: `%s`\n' "${postgres_version}"
  # Markdown backticks are literal, not shell command substitutions.
  # shellcheck disable=SC2016
  printf -- '- pgvector: `%s`\n' "${vector_version}"
  printf -- '- Existing migration bytes: preserved\n'
  printf -- '- Existing Atlas checksum entries: preserved as an exact prefix\n'
  printf -- '- New PostgreSQL functions and triggers: derived from canonical PostgreSQL definitions; edits and removals rejected\n'
  printf -- '- Clean migration replay: matches canonical schema in PostgreSQL\n'
  printf -- '- Upgrade replay: committed history plus generated delta matches canonical schema in PostgreSQL\n'
  printf -- '- Schema integration tests: passed against disposable PostgreSQL\n'
  printf '\n## Generated migrations\n\n'
  if [[ ! -s "${manifest}" ]]; then
    printf -- '- None. Canonical SQL and committed migration history were already in sync.\n'
  fi
  while IFS= read -r migration; do
    [[ -n "${migration}" ]] || continue
    # Markdown backticks are literal, not shell command substitutions.
    # shellcheck disable=SC2016
    printf -- '- `%s` (`sha256:%s`)\n' \
      "${migration}" "$(sha256sum "${migration}" | cut -d' ' -f1)"
  done < "${manifest}"
} > "${report}"
