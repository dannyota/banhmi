#!/usr/bin/env bash

set -euo pipefail

if (($# != 1)); then
  printf 'usage: %s OUTPUT_DIRECTORY\n' "$0" >&2
  exit 2
fi

readonly runner_temp="${RUNNER_TEMP:-/tmp}"
readonly manifest="${CORPUS_NEW_MIGRATIONS:-${runner_temp}/banhmi-corpus-new-migrations.txt}"
readonly report="${CORPUS_VERIFICATION_REPORT:-${runner_temp}/banhmi-corpus-verification.md}"

python3 tools/ci/corpus_generation.py stage \
  --root . \
  --output "$1" \
  --manifest "${manifest}" \
  --report "${report}"
