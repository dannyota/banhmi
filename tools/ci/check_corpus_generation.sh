#!/usr/bin/env bash
set -euo pipefail
readonly drift_file="${VALIDATION_DIR:?}/corpus-drift.txt"
git status --porcelain=v1 --untracked-files=all -- \
  pkg/store/bronze pkg/store/silver pkg/store/gold pkg/store/ingest pkg/store/config > "${drift_file}"
git ls-files --others --ignored --exclude-standard -- \
  pkg/store/bronze pkg/store/silver pkg/store/gold pkg/store/ingest pkg/store/config >> "${drift_file}"
if [[ "${1:-}" == --report-only ]]; then
  {
    printf '\nGenerated Go drift: '
    if [[ -s "${drift_file}" ]]; then printf 'present\n'; else printf 'absent\n'; fi
  } >> "${VALIDATION_DIR}/corpus-verification.md"
  exit 0
fi
if [[ -s "${drift_file}" ]]; then
  printf 'generated corpus Go differs from the candidate; review the uploaded artifact\n' >&2
  cat "${drift_file}" >&2
  exit 1
fi
