#!/usr/bin/env bash
set -euo pipefail
if (($# != 1)) || [[ "$1" != migrate && "$1" != auth-migrate ]]; then
  printf 'usage: %s migrate|auth-migrate\n' "$0" >&2
  exit 2
fi
readonly command="$1"
readonly source_dir="${VALIDATION_DIR:?}/base-${command}"
mkdir "${source_dir}"
git archive "${BASE_SHA:?}" | tar -x -C "${source_dir}"
(
  cd "${source_dir}"
  go build -o "${VALIDATION_DIR}/base-${command}-binary" "./cmd/${command}"
)
