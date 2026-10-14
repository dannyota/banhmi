#!/usr/bin/env bash
set -euo pipefail
# This mode runs before replay, under the disposable service's trust authentication.
if [[ "${1:-}" == provision ]]; then
  for chain in writer release control; do
    database="banhmi_corpus_${chain}_ci"
    psql --no-psqlrc --set ON_ERROR_STOP=1 --command "CREATE ROLE banhmi_corpus_${chain}_migrator LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS"
    psql --no-psqlrc --set ON_ERROR_STOP=1 --command "CREATE ROLE banhmi_corpus_${chain}_app LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS"
    variables=(--set "migration_login_role=banhmi_corpus_${chain}_migrator" --set "runtime_login_role=banhmi_corpus_${chain}_app")
    if [[ "${chain}" == writer ]]; then
      psql --no-psqlrc --set ON_ERROR_STOP=1 --command 'CREATE ROLE banhmi_corpus_writer_exporter LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS'
      variables+=(--set export_login_role=banhmi_corpus_writer_exporter)
    elif [[ "${chain}" == release ]]; then
      psql --no-psqlrc --set ON_ERROR_STOP=1 --command 'CREATE ROLE banhmi_corpus_release_load LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS'
      psql --no-psqlrc --set ON_ERROR_STOP=1 --command 'CREATE ROLE banhmi_corpus_release_restore LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS'
      variables+=(--set loader_login_role=banhmi_corpus_release_load)
    fi
    createdb "${database}"
    if [[ "${chain}" != control ]]; then
      psql --no-psqlrc --dbname "${database}" --set ON_ERROR_STOP=1 \
        --command "CREATE EXTENSION vector WITH SCHEMA public VERSION '0.8.2'; CREATE EXTENSION btree_gist WITH SCHEMA public VERSION '1.7'"
    fi
    psql --no-psqlrc --dbname "${database}" --set ON_ERROR_STOP=1 "${variables[@]}" \
      --file "deploy/corpusmigrations/bootstrap/${chain}.sql"
  done
  exit 0
fi
if [[ $# != 0 ]]; then
  printf 'only the provision argument is supported\n' >&2
  exit 1
fi
export BANHMI_CORPUS_WRITER_TEST_DATABASE_URL="postgres://banhmi_corpus_writer_migrator@${PGHOST:?}:${PGPORT:?}/banhmi_corpus_writer_ci?sslmode=disable"
export BANHMI_CORPUS_RELEASE_TEST_DATABASE_URL="postgres://banhmi_corpus_release_migrator@${PGHOST}:${PGPORT}/banhmi_corpus_release_ci?sslmode=disable"
export BANHMI_CORPUS_CONTROL_TEST_DATABASE_URL="postgres://banhmi_corpus_control_migrator@${PGHOST}:${PGPORT}/banhmi_corpus_control_ci?sslmode=disable"
go test -count=1 ./cmd/corpus-migrate
go test -count=1 ./internal/corpus/store/...
