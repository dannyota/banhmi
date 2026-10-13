#!/usr/bin/env bash
set -euo pipefail
readonly config=.sqlc-corpus-ci.yaml
trap 'rm -f "${config}"' EXIT
python3 tools/ci/corpus_generation.py select corpus > "${config}"
for schema in bronze silver gold ingest config; do
  find "pkg/store/${schema}" -maxdepth 1 -type f -name '*.go' -delete
done
sqlc generate --file "${config}"
