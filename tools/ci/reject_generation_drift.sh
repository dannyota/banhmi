#!/usr/bin/env bash
set -euo pipefail
if [[ $# -eq 1 && "$1" == --repository ]]; then
  # Check the whole repository and ignored additions in committed generation scopes.
  # Ignored caches elsewhere are allowed; the pre-test check stays strict.
  capture_dir="$(mktemp -d "${RUNNER_TEMP:?}/repository-drift.XXXXXX")"
  readonly capture_dir
  trap 'rm -rf -- "${capture_dir}"' EXIT
  readonly drift_file="${capture_dir}/status.txt"
  readonly output_file="${capture_dir}/scopes.txt"
  readonly diff_file="${capture_dir}/diff.txt"
  # Exclusive creation and retained descriptors bind captures to files we created.
  # Directory privacy does not isolate surviving processes with the same user.
  set -o noclobber
  exec {drift_fd}> "${drift_file}"
  exec {output_fd}> "${output_file}"
  exec {diff_fd}> "${diff_file}"
  validate_captures() {
    local pair path descriptor
    for pair in "${drift_file}:${drift_fd}" "${output_file}:${output_fd}" "${diff_file}:${diff_fd}"; do
      path="${pair%:*}"
      descriptor="${pair##*:}"
      if [[ ! -f "${path}" || -L "${path}" || ! "${path}" -ef "/dev/fd/${descriptor}" ]]; then
        printf 'repository capture is not the regular file created by this verifier: %s\n' "${path}" >&2
        exit 1
      fi
    done
  }
  validate_captures
  git status --porcelain --untracked-files=all -- . >&"${drift_fd}"
  python3 "$(dirname "${BASH_SOURCE[0]}")/sqlc_outputs.py" repository-scopes >&"${output_fd}"
  validate_captures
  mapfile -t repository_scopes < "${output_file}"
  git ls-files --others --ignored --exclude-standard -- "${repository_scopes[@]}" >&"${drift_fd}"
  diff_failed=0
  git diff HEAD --exit-code -- . >&"${diff_fd}" || diff_failed=1
  validate_captures
  cat "${diff_file}"
  {
    printf '### Post-test repository integrity\n\n'
    if [[ -s "${drift_file}" ]]; then
      printf 'Changed paths:\n\n```text\n'
      cat "${drift_file}"
      printf '```\n'
    else
      printf 'No changed paths.\n'
    fi
  } >> "${GITHUB_STEP_SUMMARY:?}"
  if [[ -s "${drift_file}" ]] || ((diff_failed)); then
    printf 'replay or tests changed the committed tree, created non-ignored files, or added ignored files in guarded scopes\n' >&2
    cat "${drift_file}" >&2
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
