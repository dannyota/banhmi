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
