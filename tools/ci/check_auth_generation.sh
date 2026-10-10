#!/usr/bin/env bash

set -euo pipefail

readonly drift_file="${RUNNER_TEMP:-/tmp}/banhmi-auth-generation-drift.txt"

git status --porcelain=v1 --untracked-files=all -- \
  go.mod \
  go.sum \
  pkg/store/auth \
  deploy/authmigrations \
  ':(glob)**/*.go' > "${drift_file}"

if [[ -s "${drift_file}" ]]; then
  printf 'hosted generation differs from the committed tree:\n' >&2
  sed -n '1,200p' "${drift_file}" >&2
  printf 'download and review the auth-generation artifact; workflows never write to the repository\n' >&2
  exit 1
fi
