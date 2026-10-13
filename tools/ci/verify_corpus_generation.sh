#!/usr/bin/env bash
set -euo pipefail
readonly validation_dir="${VALIDATION_DIR:?}"
readonly report="${validation_dir}/corpus-verification.md"
readonly canonical_database=banhmi_corpus_canonical_ci
readonly clean_database=banhmi_corpus_clean_ci
readonly upgrade_database=banhmi_corpus_upgrade_ci
readonly race_database=banhmi_corpus_race_ci
readonly rollback_database=banhmi_corpus_rollback_ci
readonly schemas=(bronze silver gold ingest config)
export BANHMI_DATABASE_HOST="${PGHOST:?}"
export BANHMI_DATABASE_PORT="${PGPORT:?}"
export BANHMI_DATABASE_USER="${PGUSER:?}"
export BANHMI_DATABASE_SSLMODE=disable
export BANHMI_DATABASE_PASSWORD="${PGPASSWORD:-}"
bash tools/ci/build_base_migrator.sh migrate
for database in "${clean_database}" "${upgrade_database}" "${race_database}" "${rollback_database}"; do
  createdb "${database}"
done
run_candidate() {
  BANHMI_DATABASE_NAME="$1" go run ./cmd/migrate -config config/config.example.yaml
}
run_candidate "${clean_database}"
BANHMI_DATABASE_NAME="${upgrade_database}" \
  "${validation_dir}/base-migrate-binary" -config config/config.example.yaml
psql --dbname "${upgrade_database}" --set ON_ERROR_STOP=1 --file tools/ci/corpus_legacy_seed.sql >/dev/null
# Freeze base table, sequence, and ledger selections before candidate additions.
psql --dbname "${upgrade_database}" --set ON_ERROR_STOP=1 --tuples-only --no-align \
  --set freeze_ledger=true --file tools/ci/corpus_legacy_snapshot.sql > "${validation_dir}/legacy-selection.sql"
psql --dbname "${upgrade_database}" --set ON_ERROR_STOP=1 --tuples-only --no-align \
  --file "${validation_dir}/legacy-selection.sql" > "${validation_dir}/legacy-before.txt"
run_candidate "${upgrade_database}"
psql --dbname "${upgrade_database}" --set ON_ERROR_STOP=1 --tuples-only --no-align \
  --file "${validation_dir}/legacy-selection.sql" > "${validation_dir}/legacy-after.txt"
cmp "${validation_dir}/legacy-before.txt" "${validation_dir}/legacy-after.txt"
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
               coalesce(collation_value.collname, '<default>'),
               coalesce(pg_get_expr(default_value.adbin, default_value.adrelid), '<null>')
           )
    FROM pg_class AS relation
    JOIN target_namespace AS namespace ON namespace.oid = relation.relnamespace
    JOIN pg_attribute AS attribute ON attribute.attrelid = relation.oid
    LEFT JOIN pg_attrdef AS default_value
      ON default_value.adrelid = relation.oid AND default_value.adnum = attribute.attnum
    LEFT JOIN pg_collation AS collation_value ON collation_value.oid = attribute.attcollation
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

readonly clean_dump="${validation_dir}/clean-catalog.txt"
readonly upgrade_dump="${validation_dir}/upgrade-catalog.txt"
dump_database "${clean_database}" "${clean_dump}"
dump_database "${upgrade_database}" "${upgrade_dump}"
cmp "${clean_dump}" "${upgrade_dump}"
transition=not-applicable
canonical_count=0
for schema in "${schemas[@]}"; do
  if git cat-file -e "${BASE_SHA}:sql/${schema}/schema.sql" 2>/dev/null; then
    canonical_count=$((canonical_count + 1))
  fi
done
if ((canonical_count != 0 && canonical_count != 5)); then
  printf 'trusted base contains only part of the canonical corpus schema\n' >&2
  exit 1
fi
if ((canonical_count == 5)); then
  createdb "${canonical_database}"
  psql --dbname "${canonical_database}" --set ON_ERROR_STOP=1 --command 'CREATE EXTENSION vector' >/dev/null
  for schema in "${schemas[@]}"; do
    git show "${BASE_SHA}:sql/${schema}/schema.sql" > "${validation_dir}/canonical-${schema}.sql"
    psql --dbname "${canonical_database}" --set ON_ERROR_STOP=1 --single-transaction \
      --file "${validation_dir}/canonical-${schema}.sql" >/dev/null
  done
  dump_database "${canonical_database}" "${validation_dir}/canonical-catalog.txt"
  cmp "${validation_dir}/canonical-catalog.txt" "${clean_dump}"
  transition=passed
fi
# Replay must preserve catalogs, legacy data, sequences, and all candidate ledger rows.
psql --dbname "${upgrade_database}" --set ON_ERROR_STOP=1 --tuples-only --no-align \
  --set freeze_ledger=false --file tools/ci/corpus_legacy_snapshot.sql > "${validation_dir}/candidate-selection.sql"
psql --dbname "${upgrade_database}" --set ON_ERROR_STOP=1 --tuples-only --no-align \
  --file "${validation_dir}/candidate-selection.sql" > "${validation_dir}/candidate-before.txt"
run_candidate "${upgrade_database}"
dump_database "${upgrade_database}" "${validation_dir}/replayed-catalog.txt"
cmp "${upgrade_dump}" "${validation_dir}/replayed-catalog.txt"
psql --dbname "${upgrade_database}" --set ON_ERROR_STOP=1 --tuples-only --no-align \
  --file "${validation_dir}/candidate-selection.sql" > "${validation_dir}/candidate-after.txt"
cmp "${validation_dir}/candidate-before.txt" "${validation_dir}/candidate-after.txt"
# The complete no-op snapshot must detect even a newly appended ledger row.
readonly ledger_negative_database=banhmi_corpus_ledger_negative_ci
createdb --template "${clean_database}" "${ledger_negative_database}"
psql --dbname "${ledger_negative_database}" --set ON_ERROR_STOP=1 --tuples-only --no-align \
  --file "${validation_dir}/candidate-selection.sql" > "${validation_dir}/ledger-negative-before.txt"
psql --dbname "${ledger_negative_database}" --set ON_ERROR_STOP=1 >/dev/null <<'SQL'
INSERT INTO public.goose_db_version (id, version_id, is_applied)
SELECT coalesce(max(id), 0) + 1, 9223372036854775806, true FROM public.goose_db_version;
SQL
psql --dbname "${ledger_negative_database}" --set ON_ERROR_STOP=1 --tuples-only --no-align \
  --file "${validation_dir}/candidate-selection.sql" > "${validation_dir}/ledger-negative-after.txt"
if cmp -s "${validation_dir}/ledger-negative-before.txt" "${validation_dir}/ledger-negative-after.txt"; then
  printf 'no-op replay evidence ignored an appended corpus ledger row\n' >&2
  exit 1
fi
psql --dbname "${upgrade_database}" --set ON_ERROR_STOP=1 --file tools/ci/check_legacy_sequences.sql >/dev/null
run_candidate "${race_database}"
run_candidate "${rollback_database}"
export CORPUS_RACE_URL="postgres://${PGUSER}@${PGHOST}:${PGPORT}/${race_database}?sslmode=disable"
export CORPUS_ROLLBACK_URL="postgres://${PGUSER}@${PGHOST}:${PGPORT}/${rollback_database}?sslmode=disable"
readonly events="${validation_dir}/schema-tests.jsonl"
BANHMI_CORPUS_SCHEMA_TEST_DATABASE_URL="${CORPUS_RACE_URL}" \
  go test -json -count=1 -run '^TestReleaseActivationRace$' ./internal/corpus/schematest | tee "${events}"
BANHMI_CORPUS_SCHEMA_TEST_DATABASE_URL="${CORPUS_ROLLBACK_URL}" \
  go test -json -count=1 -run '^(TestCorpusSchemaBoundaries|TestImmutableEvidenceAndReviewConstraints|TestStageIdempotencyCompletionAndFence|TestReleaseActivationCompareAndSet|TestReleaseManifestRequiresEvidence)$' \
    ./internal/corpus/schematest | tee -a "${events}"
python3 tools/ci/corpus_generation.py tests corpus --events "${events}"
# Mutation negatives prove the behavior suite notices missing required DDL.
for mutation in function trigger; do
  database="banhmi_corpus_missing_${mutation}_ci"
  createdb --template "${clean_database}" "${database}"
  if [[ "${mutation}" == function ]]; then
    psql --dbname "${database}" --set ON_ERROR_STOP=1 \
      --command 'DROP FUNCTION bronze.reject_immutable_change() CASCADE' >/dev/null 2>&1
  else
    psql --dbname "${database}" --set ON_ERROR_STOP=1 \
      --command 'DROP TRIGGER observation_revision_immutable ON bronze.observation_revision' >/dev/null
  fi
  negative_events="${validation_dir}/missing-${mutation}.jsonl"
  if BANHMI_CORPUS_SCHEMA_TEST_DATABASE_URL="postgres://${PGUSER}@${PGHOST}:${PGPORT}/${database}?sslmode=disable" \
    go test -json -count=1 -run '^TestImmutableEvidenceAndReviewConstraints$' ./internal/corpus/schematest | tee "${negative_events}"; then
    printf 'schema behavior suite accepted an omitted %s\n' "${mutation}" >&2
    exit 1
  fi
  python3 tools/ci/corpus_generation.py negative-tests corpus --events "${negative_events}"
done
postgres_version="$(psql --dbname "${clean_database}" --tuples-only --no-align --command 'SHOW server_version')"
vector_version="$(psql --dbname "${clean_database}" --tuples-only --no-align --command "SELECT extversion FROM pg_extension WHERE extname = 'vector'")"
{
  printf '# Corpus verification\n\n'
  printf -- '- Candidate: %s\n- Trusted base: %s\n' "${CANDIDATE_SHA}" "${BASE_SHA}"
  printf -- '- PostgreSQL: %s\n- pgvector: %s\n' "${postgres_version}" "${vector_version}"
  printf -- '- Transition canonical comparison: %s\n' "${transition}"
  python3 - "${VALIDATION_DIR}/history.json" <<'PY'
import json
import sys
report = json.load(open(sys.argv[1]))
print('- Migration comparison range: ' + ('SQL additions present' if report['migration_additions'] else 'no SQL additions; structural and replay checks only'))
PY
  printf -- '- Clean and populated upgrade catalog parity: passed\n'
  printf -- '- Legacy rows, identities, sequences, and ledger retention: passed\n'
  printf -- '- Candidate replay idempotence with complete ledgers: passed\n'
  printf -- '- Appended ledger row rejected by no-op replay comparison: passed\n'
  printf -- '- Six schema tests exactly once, isolated race and rollback databases, no skips: passed\n'
  printf -- '- Omitted required function and trigger fail the retained behavior suite: passed\n'
} > "${report}"
