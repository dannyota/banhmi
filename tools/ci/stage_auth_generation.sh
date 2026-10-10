#!/usr/bin/env bash

set -euo pipefail

if (($# != 1)); then
  printf 'usage: %s OUTPUT_DIRECTORY\n' "$0" >&2
  exit 2
fi

readonly output_dir="$1"

if [[ -e "${output_dir}" ]]; then
  printf 'refusing to replace existing output directory: %s\n' "${output_dir}" >&2
  exit 1
fi
mkdir -p \
  "${output_dir}/pkg/store/auth" \
  "${output_dir}/deploy/authmigrations"

mapfile -t generated_go < <(find pkg/store/auth -maxdepth 1 -type f -name '*.go' -print | sort)
if ((${#generated_go[@]} == 0)); then
  printf 'sqlc produced no auth Go files\n' >&2
  exit 1
fi
cp "${generated_go[@]}" "${output_dir}/pkg/store/auth/"

mapfile -t migration_sql < <(find deploy/authmigrations -maxdepth 1 -type f -name '*.sql' -print | sort)
if ((${#migration_sql[@]} == 0)); then
  printf 'auth generation produced no migration SQL\n' >&2
  exit 1
fi
cp "${migration_sql[@]}" "${output_dir}/deploy/authmigrations/"
cp deploy/authmigrations/atlas.sum "${output_dir}/deploy/authmigrations/"
cp go.mod go.sum "${output_dir}/"

git diff --binary -- '*.go' ':(exclude)pkg/store/auth/**' > "${output_dir}/gofmt.patch"
