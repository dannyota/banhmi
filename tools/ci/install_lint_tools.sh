#!/usr/bin/env bash

set -euo pipefail

checksum_file="$(pwd)/tools/ci/lint-tools.sha256"
readonly checksum_file
readonly checksum_record='^([[:xdigit:]]{64}) [ *]([^[:space:]]+)$'
declare -A expected_archives=(
  [actionlint_1.7.11_linux_amd64.tar.gz]=0
  [shellcheck-v0.11.0.linux.x86_64.tar.xz]=0
)
# Comments and empty lines are metadata; every record must name one expected archive.
while IFS= read -r record || [[ -n "$record" ]]; do
  [[ -z "$record" || "$record" == \#* ]] && continue
  if [[ ! "$record" =~ $checksum_record ]]; then
    printf 'Malformed lint tool checksum record\n' >&2
    exit 1
  fi
  archive="${BASH_REMATCH[2]}"
  if [[ "${expected_archives[$archive]:-}" != 0 ]]; then
    printf 'Unexpected or duplicate lint tool checksum: %s\n' "$archive" >&2
    exit 1
  fi
  expected_archives[$archive]=1
done < "$checksum_file"
for archive in "${!expected_archives[@]}"; do
  if [[ "${expected_archives[$archive]}" != 1 ]]; then
    printf 'Missing lint tool checksum: %s\n' "$archive" >&2
    exit 1
  fi
done

readonly tool_dir="${RUNNER_TEMP:?}/banhmi-lint-tools"
mkdir -p "${tool_dir}/bin"
cd "${tool_dir}"

curl --fail --location --silent --show-error --retry 3 \
  --proto '=https' --proto-redir '=https' \
  --output actionlint_1.7.11_linux_amd64.tar.gz \
  https://github.com/rhysd/actionlint/releases/download/v1.7.11/actionlint_1.7.11_linux_amd64.tar.gz
curl --fail --location --silent --show-error --retry 3 \
  --proto '=https' --proto-redir '=https' \
  --output shellcheck-v0.11.0.linux.x86_64.tar.xz \
  https://github.com/koalaman/shellcheck/releases/download/v0.11.0/shellcheck-v0.11.0.linux.x86_64.tar.xz

# Verify both archives before extracting or executing either binary.
sha256sum --check --strict "${checksum_file}"
tar -xzf actionlint_1.7.11_linux_amd64.tar.gz actionlint
tar -xJf shellcheck-v0.11.0.linux.x86_64.tar.xz shellcheck-v0.11.0/shellcheck
install -m 0755 actionlint "${tool_dir}/bin/actionlint"
install -m 0755 shellcheck-v0.11.0/shellcheck "${tool_dir}/bin/shellcheck"
printf '%s\n' "${tool_dir}/bin" >> "${GITHUB_PATH:?}"
"${tool_dir}/bin/actionlint" -version
"${tool_dir}/bin/shellcheck" --version
