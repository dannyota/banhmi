#!/usr/bin/env bash

set -euo pipefail

readonly runner_temp="${RUNNER_TEMP:-/tmp}"
readonly baseline="${CORPUS_MIGRATION_BASELINE:-${runner_temp}/banhmi-corpus-migration-baseline.json}"
readonly manifest="${CORPUS_NEW_MIGRATIONS:-${runner_temp}/banhmi-corpus-new-migrations.txt}"
readonly baseline_migrate="${CORPUS_BASELINE_MIGRATE:-${runner_temp}/banhmi-corpus-migrate-baseline}"
readonly sqlc_config=".sqlc-corpus-ci.yaml"
readonly runtime_config="config/config.yaml"
readonly corpus_schemas=(bronze silver gold ingest config)
readonly opaque_baseline="${runner_temp}/banhmi-corpus-opaque-baseline.tsv"
readonly opaque_canonical="${runner_temp}/banhmi-corpus-opaque-canonical.tsv"
readonly opaque_baseline_database="banhmi_corpus_opaque_baseline_ci"
readonly opaque_canonical_database="banhmi_corpus_opaque_canonical_ci"
runtime_config_owned=0
sqlc_config_owned=0

cleanup() {
  if ((sqlc_config_owned)); then
    rm -f "${sqlc_config}"
  fi
  if ((runtime_config_owned)); then
    rm -f "${runtime_config}"
  fi
}

for command in atlas createdb dropdb go psql python3 sqlc; do
  if ! command -v "${command}" >/dev/null 2>&1; then
    printf 'required corpus generation command is missing: %s\n' "${command}" >&2
    exit 1
  fi
done

if [[ -e "${runtime_config}" || -e "${sqlc_config}" ]]; then
  printf 'refusing to replace a local generation input: %s or %s\n' \
    "${runtime_config}" "${sqlc_config}" >&2
  exit 1
fi
if [[ ! -f config/config.example.yaml || ! -f sqlc.yaml ]]; then
  printf 'corpus generation requires config/config.example.yaml and sqlc.yaml\n' >&2
  exit 1
fi
trap cleanup EXIT
if [[ -z "${PGHOST:-}" || -z "${PGPORT:-}" || -z "${PGUSER:-}" ]]; then
  printf 'PGHOST, PGPORT, and PGUSER are required for corpus generation\n' >&2
  exit 1
fi

python3 tools/ci/corpus_generation.py snapshot --root . --output "${baseline}"
for schema in "${corpus_schemas[@]}"; do
  atlas migrate validate --dir "file://deploy/migrations/${schema}"
done

# This binary embeds the committed migration history before generation. The
# upgrade replay applies it first, then applies the newly generated tree.
go build -o "${baseline_migrate}" ./cmd/migrate

runtime_config_owned=1
install -m 0600 config/config.example.yaml "${runtime_config}"
export BANHMI_DATABASE_HOST="${PGHOST}"
export BANHMI_DATABASE_PORT="${PGPORT}"
export BANHMI_DATABASE_USER="${PGUSER}"
export BANHMI_DATABASE_NAME="${PGDATABASE:-postgres}"
export BANHMI_DATABASE_SSLMODE=disable
export BANHMI_DATABASE_PASSWORD="${PGPASSWORD:-}"

for database in "${opaque_baseline_database}" "${opaque_canonical_database}"; do
  dropdb --if-exists --force "${database}"
  createdb "${database}"
done
BANHMI_DATABASE_NAME="${opaque_baseline_database}" \
  "${baseline_migrate}" -config "${runtime_config}"
psql --dbname "${opaque_canonical_database}" --set ON_ERROR_STOP=1 \
  --command 'CREATE EXTENSION vector'
for schema in "${corpus_schemas[@]}"; do
  psql --dbname "${opaque_canonical_database}" --set ON_ERROR_STOP=1 \
    --single-transaction --file "sql/${schema}/schema.sql"
done

write_opaque_inventory() {
  local database="$1"
  local destination="$2"
  psql --dbname "${database}" --set ON_ERROR_STOP=1 \
    --tuples-only --no-align --field-separator $'\t' \
    > "${destination}" <<'SQL'
WITH target_namespace AS (
    SELECT oid, nspname
    FROM pg_namespace
    WHERE nspname IN ('bronze', 'silver', 'gold', 'ingest', 'config')
), opaque_object AS (
    SELECT 'function' AS kind,
           namespace.nspname AS schema_name,
           namespace.nspname || '.' || function_value.proname || '(' ||
             pg_get_function_identity_arguments(function_value.oid) || ')' AS object_key,
           replace(encode(convert_to(pg_get_functiondef(function_value.oid), 'UTF8'), 'base64'), chr(10), '') AS definition
    FROM pg_proc AS function_value
    JOIN target_namespace AS namespace ON namespace.oid = function_value.pronamespace
    UNION ALL
    SELECT 'trigger',
           namespace.nspname,
           namespace.nspname || '.' || relation.relname || '.' || trigger_value.tgname,
           replace(encode(convert_to(pg_get_triggerdef(trigger_value.oid, true), 'UTF8'), 'base64'), chr(10), '')
    FROM pg_trigger AS trigger_value
    JOIN pg_class AS relation ON relation.oid = trigger_value.tgrelid
    JOIN target_namespace AS namespace ON namespace.oid = relation.relnamespace
    WHERE NOT trigger_value.tgisinternal
)
SELECT kind, schema_name, object_key, definition
FROM opaque_object
ORDER BY kind, schema_name, object_key;
SQL
}

write_opaque_inventory "${opaque_baseline_database}" "${opaque_baseline}"
write_opaque_inventory "${opaque_canonical_database}" "${opaque_canonical}"

go run ./tools/migragen -name corpus_redesign

python3 tools/ci/corpus_generation.py verify \
  --root . \
  --baseline "${baseline}" \
  --manifest "${manifest}"

python3 tools/ci/corpus_generation.py append-opaque-delta \
  --root . \
  --baseline "${opaque_baseline}" \
  --canonical "${opaque_canonical}" \
  --manifest "${manifest}"
for schema in "${corpus_schemas[@]}"; do
  atlas migrate hash --dir "file://deploy/migrations/${schema}"
done
python3 tools/ci/corpus_generation.py verify \
  --root . \
  --baseline "${baseline}" \
  --manifest "${manifest}"

sqlc_config_owned=1
awk '
  BEGIN {
    print "version: \"2\""
    print "sql:"
  }

  /^  - engine:/ {
    if (in_block && !is_auth) {
      printf "%s", block
      matches++
    }
    block = $0 ORS
    in_block = 1
    is_auth = 0
    next
  }

  in_block {
    block = block $0 ORS
    if ($0 ~ /^[[:space:]]+schema:[[:space:]]+"?sql\/auth\/schema\.sql"?[[:space:]]*$/) {
      is_auth = 1
    }
  }

  END {
    if (in_block && !is_auth) {
      printf "%s", block
      matches++
    }
    if (matches != 5) {
      printf "expected five corpus sqlc entries, found %d\n", matches > "/dev/stderr"
      exit 1
    }
  }
' sqlc.yaml > "${sqlc_config}"

for schema in "${corpus_schemas[@]}"; do
  find "pkg/store/${schema}" -maxdepth 1 -type f -name '*.go' -delete
done
sqlc generate --file "${sqlc_config}"

for schema in "${corpus_schemas[@]}"; do
  atlas migrate validate --dir "file://deploy/migrations/${schema}"
done

if [[ -n "$(git status --porcelain=v1 --untracked-files=all -- pkg/store/auth)" ]]; then
  printf 'corpus generation changed auth sqlc output\n' >&2
  git status --short --untracked-files=all -- pkg/store/auth >&2
  exit 1
fi
