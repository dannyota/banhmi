#!/usr/bin/env bash
set -euo pipefail
readonly drift_file="${RUNNER_TEMP:?}/generation-drift.txt"
readonly output_file="${RUNNER_TEMP}/sqlc-output-directories.txt"
python3 "$(dirname "${BASH_SOURCE[0]}")/sqlc_outputs.py" dirs > "${output_file}"
mapfile -t output_dirs < "${output_file}"
git status --porcelain --untracked-files=all -- "${output_dirs[@]}" go.mod go.sum > "${drift_file}"
# Ignored output must not evade status, including repository-wide ignores.
git ls-files --others --ignored --exclude-standard -- "${output_dirs[@]}" go.mod go.sum >> "${drift_file}"
diff_failed=0
git diff HEAD --exit-code -- "${output_dirs[@]}" go.mod go.sum || diff_failed=1
{
  printf '### Generation drift\n\n'
  if [[ -s "${drift_file}" ]]; then
    printf 'Drifted paths:\n\n```text\n'
    cat "${drift_file}"
    printf '```\n'
  else
    printf 'No drifted paths.\n'
  fi
} >> "${GITHUB_STEP_SUMMARY:?}"
if [[ -s "${drift_file}" ]] || ((diff_failed)); then
  printf 'hosted generation differs from the committed tree; review the generation artifact\n' >&2
  cat "${drift_file}" >&2
  exit 1
fi
