#!/usr/bin/env bash
set -euo pipefail
readonly clean_database=banhmi_corpus_clean_ci
readonly race_database=banhmi_corpus_race_ci
readonly rollback_database=banhmi_corpus_rollback_ci
readonly auth_database=banhmi_auth_generation_ci
export BANHMI_DATABASE_HOST="${PGHOST:?}"
export BANHMI_DATABASE_PORT="${PGPORT:?}"
export BANHMI_DATABASE_USER="${PGUSER:?}"
export BANHMI_DATABASE_SSLMODE=disable
export BANHMI_DATABASE_PASSWORD="${PGPASSWORD:-}"
for database in "${clean_database}" "${race_database}" "${rollback_database}"; do
  createdb "${database}"
  BANHMI_DATABASE_NAME="${database}" go run ./cmd/migrate -config config/config.example.yaml
done
createdb "${auth_database}"
BANHMI_AUTH_MIGRATION_DATABASE_URL="postgres://${PGUSER}@${PGHOST}:${PGPORT}/${auth_database}?sslmode=disable" \
  go run ./cmd/auth-migrate
