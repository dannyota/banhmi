#!/usr/bin/env bash

set -euo pipefail

readonly auth_schema="sql/auth/schema.sql"
readonly auth_migration_dir="deploy/authmigrations"
readonly initial_auth_migration="${auth_migration_dir}/20261011000000_init.sql"
readonly sqlc_config=".sqlc-auth-ci.yaml"

cleanup() {
  rm -f "${sqlc_config}"
}
trap cleanup EXIT

if [[ ! -f "${auth_schema}" ]]; then
  printf 'missing auth schema: %s\n' "${auth_schema}" >&2
  exit 1
fi

if [[ ! -f sqlc.yaml ]]; then
  printf 'missing sqlc configuration: sqlc.yaml\n' >&2
  exit 1
fi

if [[ -z "${PGHOST:-}" || -z "${PGDATABASE:-}" || -z "${PGUSER:-}" ]]; then
  printf 'PGHOST, PGDATABASE, and PGUSER are required for auth schema validation\n' >&2
  exit 1
fi
psql --set ON_ERROR_STOP=1 --single-transaction --file "${auth_schema}"

mkdir -p "${auth_migration_dir}"
mapfile -t auth_migrations < <(find "${auth_migration_dir}" -maxdepth 1 -type f -name '*.sql' -print | sort)

if [[ ! -f "${initial_auth_migration}" ]]; then
  if ((${#auth_migrations[@]} != 0)); then
    printf 'refusing to create the initial auth migration beside existing migrations\n' >&2
    exit 1
  fi
  install -m 0644 "${auth_schema}" "${initial_auth_migration}"
elif ((${#auth_migrations[@]} == 1)) && ! cmp -s "${auth_schema}" "${initial_auth_migration}"; then
  printf '%s differs from the canonical schema; committed migrations are append-only\n' \
    "${initial_auth_migration}" >&2
  exit 1
fi

atlas migrate hash --dir "file://${auth_migration_dir}"
atlas migrate validate --dir "file://${auth_migration_dir}"

awk '
  BEGIN {
    print "version: \"2\""
    print "sql:"
  }

  /^  - engine:/ {
    if (in_block && is_auth) {
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
    if (in_block && is_auth) {
      printf "%s", block
      matches++
    }
    if (matches != 1) {
      printf "expected one auth sqlc entry, found %d\n", matches > "/dev/stderr"
      exit 1
    }
  }
' sqlc.yaml > "${sqlc_config}"

if [[ -d pkg/store/auth ]]; then
  find pkg/store/auth -maxdepth 1 -type f -name '*.go' -delete
fi
sqlc generate --file "${sqlc_config}"

mapfile -d '' go_files < <(git ls-files -co --exclude-standard -z -- '*.go')
if ((${#go_files[@]} > 0)); then
  gofmt -w "${go_files[@]}"
fi

# Dependency resolution comes last so first-pass package loading can see the
# generated files referenced by go:embed and generated-store imports.
go mod tidy
