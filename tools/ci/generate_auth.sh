#!/usr/bin/env bash
set -euo pipefail
readonly config=.sqlc-auth-ci.yaml
trap 'rm -f "${config}"' EXIT
python3 tools/ci/corpus_generation.py select auth > "${config}"
find pkg/store/auth -maxdepth 1 -type f -name '*.go' -delete
sqlc generate --file "${config}"
mapfile -d '' go_files < <(git ls-files -co --exclude-standard -z -- '*.go')
if ((${#go_files[@]} > 0)); then
  gofmt -w "${go_files[@]}"
fi
go mod tidy
