#!/usr/bin/env bash
set -euo pipefail
export CORPUS_RACE_URL="postgres://${PGUSER:?}@${PGHOST:?}:${PGPORT:?}/banhmi_corpus_race_ci?sslmode=disable"
export CORPUS_ROLLBACK_URL="postgres://${PGUSER}@${PGHOST}:${PGPORT}/banhmi_corpus_rollback_ci?sslmode=disable"
readonly events="${RUNNER_TEMP:?}/schema-tests.jsonl"
BANHMI_CORPUS_SCHEMA_TEST_DATABASE_URL="${CORPUS_RACE_URL}" \
  go test -json -count=1 -run '^TestReleaseActivationRace$' ./internal/corpus/schematest | tee "${events}"
BANHMI_CORPUS_SCHEMA_TEST_DATABASE_URL="${CORPUS_ROLLBACK_URL}" \
  go test -json -count=1 -run '^(TestCorpusSchemaBoundaries|TestImmutableEvidenceAndReviewConstraints|TestStageIdempotencyCompletionAndFence|TestReleaseActivationCompareAndSet|TestReleaseManifestRequiresEvidence)$' \
    ./internal/corpus/schematest | tee -a "${events}"
python3 tools/ci/check_schema_tests.py "${events}"
# Mutated copies must fail the retained test, without skips or unrelated failures.
for mutation in function trigger; do
  database="banhmi_corpus_missing_${mutation}_ci"
  createdb --template banhmi_corpus_rollback_ci "${database}"
  if [[ "${mutation}" == function ]]; then
    psql --dbname "${database}" --set ON_ERROR_STOP=1 \
      --command 'DROP FUNCTION bronze.reject_immutable_change() CASCADE' >/dev/null
  else
    psql --dbname "${database}" --set ON_ERROR_STOP=1 \
      --command 'DROP TRIGGER observation_revision_immutable ON bronze.observation_revision' >/dev/null
  fi
  negative_events="${RUNNER_TEMP}/missing-${mutation}.jsonl"
  # Capture Go and tee separately: an output-write error is not expected test failure.
  set +e
  BANHMI_CORPUS_SCHEMA_TEST_DATABASE_URL="postgres://${PGUSER}@${PGHOST}:${PGPORT}/${database}?sslmode=disable" \
    go test -json -count=1 -run '^TestImmutableEvidenceAndReviewConstraints$' \
      ./internal/corpus/schematest | tee "${negative_events}"
  statuses=("${PIPESTATUS[@]}")
  set -e
  if [[ "${statuses[0]}" != 1 || "${statuses[1]}" != 0 ]]; then
    printf 'omitted %s control requires Go test failure and successful event capture\n' "${mutation}" >&2
    exit 1
  fi
  python3 tools/ci/check_schema_tests.py "${negative_events}" --expected-failure
done
