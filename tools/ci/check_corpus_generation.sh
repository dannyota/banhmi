#!/usr/bin/env bash

set -euo pipefail

readonly drift_file="${RUNNER_TEMP:-/tmp}/banhmi-corpus-generation-drift.txt"
readonly report="${CORPUS_VERIFICATION_REPORT:-${RUNNER_TEMP:-/tmp}/banhmi-corpus-verification.md}"

git status --porcelain=v1 --untracked-files=all -- \
  pkg/store/bronze \
  pkg/store/silver \
  pkg/store/gold \
  pkg/store/ingest \
  pkg/store/config \
  deploy/migrations/bronze \
  deploy/migrations/silver \
  deploy/migrations/gold \
  deploy/migrations/ingest \
  deploy/migrations/config > "${drift_file}"

if [[ "${1:-}" == "--report-only" ]]; then
  if [[ ! -f "${report}" ]]; then
    printf 'verification report does not exist: %s\n' "${report}" >&2
    exit 1
  fi
  {
    printf '\n## Committed-tree drift\n\n'
    if [[ -s "${drift_file}" ]]; then
      printf 'Generated corpus files differ from the committed tree. Import and review the artifact before Task 7.\n\n'
      printf '```text\n'
      sed -n '1,200p' "${drift_file}"
      printf '```\n'
    else
      printf 'Generated corpus files match the committed tree.\n'
    fi
  } >> "${report}"
  exit 0
fi

if [[ -s "${drift_file}" ]]; then
  printf 'hosted corpus generation differs from the committed tree:\n' >&2
  sed -n '1,200p' "${drift_file}" >&2
  printf 'download and review the corpus-source artifact; the workflow cannot write to the repository\n' >&2
  exit 1
fi
