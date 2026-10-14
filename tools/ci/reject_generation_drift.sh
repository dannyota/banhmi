#!/usr/bin/env bash
set -euo pipefail
if [[ $# -eq 1 && "$1" == --repository ]]; then
  # Check the whole repository and ignored additions in committed generation scopes.
  # Ignored caches elsewhere are allowed; the pre-test check stays strict.
  # Keep decision inputs in memory; same-user test processes can replace files.
  if ! repository_status="$(git status --porcelain --untracked-files=all -- .)"; then
    printf 'failed to inspect repository status\n' >&2
    exit 1
  fi
  if ! scope_output="$(python3 "$(dirname "${BASH_SOURCE[0]}")/sqlc_outputs.py" repository-scopes)"; then
    printf 'failed to determine guarded repository scopes\n' >&2
    exit 1
  fi
  mapfile -t repository_scopes <<< "${scope_output}"
  if ! ignored_paths="$(git ls-files --others --ignored --exclude-standard -- "${repository_scopes[@]}")"; then
    printf 'failed to inspect ignored repository additions\n' >&2
    exit 1
  fi
  diff_failed=0
  git diff HEAD --exit-code -- . || diff_failed=1
  repository_failed=0
  if [[ -n "${repository_status}" || -n "${ignored_paths}" ]] || ((diff_failed)); then
    repository_failed=1
  fi
  # Reports are written only after the verdict; no report file is read back.
  {
    printf '### Post-test repository integrity\n\n'
    if [[ -n "${repository_status}" || -n "${ignored_paths}" ]]; then
      printf 'Changed paths:\n\n```text\n'
      if [[ -n "${repository_status}" ]]; then printf '%s\n' "${repository_status}"; fi
      if [[ -n "${ignored_paths}" ]]; then printf '%s\n' "${ignored_paths}"; fi
      printf '```\n'
    else
      printf 'No changed paths.\n'
    fi
  } >> "${GITHUB_STEP_SUMMARY:?}"
  if ((repository_failed)); then
    printf 'replay or tests changed the committed tree, created non-ignored files, or added ignored files in guarded scopes\n' >&2
    if [[ -n "${repository_status}" ]]; then printf '%s\n' "${repository_status}" >&2; fi
    if [[ -n "${ignored_paths}" ]]; then printf '%s\n' "${ignored_paths}" >&2; fi
    exit 1
  fi
  exit 0
elif [[ $# -ne 0 ]]; then
  printf 'usage: reject_generation_drift.sh [--repository]\n' >&2
  exit 1
fi
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
