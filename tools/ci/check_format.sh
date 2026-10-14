#!/usr/bin/env bash
# One file set and formatter for Make and CI. sqlc output is included because
# sqlc v1.31.1 formats its Go output with go/format.Source.
set -euo pipefail

mode=--check
artifact_dir=
if [[ $# -gt 0 ]]; then
  mode=$1
  shift
fi
if [[ "$mode" != --check && "$mode" != --write ]]; then
  echo 'usage: check_format.sh [--check [--artifact-dir DIRECTORY] | --write]' >&2
  exit 2
fi
if [[ $# -gt 0 ]]; then
  if [[ "$mode" != --check || $# != 2 || "$1" != --artifact-dir || -z "$2" ]]; then
    echo 'invalid formatting arguments' >&2
    exit 2
  fi
  artifact_dir=$2
fi

cd "$(git rev-parse --show-toplevel)"
if [[ -n "$artifact_dir" ]]; then
  # Require a new directory, so stale repairs cannot enter an artifact.
  mkdir -- "$artifact_dir"
  artifact_dir=$(cd "$artifact_dir" && pwd)
  printf 'Go formatting check did not complete.\n' > "$artifact_dir/result.txt"
fi

version=$(awk '$1 == "go" { print $2 }' go.mod)
if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo 'go.mod must pin an exact Go version' >&2
  exit 1
fi
actual=$(GOTOOLCHAIN=local go env GOVERSION)
if [[ "$actual" != "go$version" ]]; then
  printf 'need Go %s from go.mod; found %s\n' "$version" "$actual" >&2
  exit 1
fi
goroot=$(GOTOOLCHAIN=local go env GOROOT)
formatter="$goroot/bin/gofmt"
[[ -x "$formatter" ]]

listing=$(mktemp)
trap 'rm -f -- "$listing"' EXIT
# Capture Git's exit status before reading its NUL-delimited output.
git ls-files -z -- '*.go' > "$listing"
mapfile -d '' -t files < "$listing"
unformatted=()
for file in "${files[@]}"; do
  if [[ ! -f "$file" || -L "$file" ]]; then
    printf 'tracked Go path is not a regular file: %q\n' "$file" >&2
    exit 1
  fi
  if [[ "$mode" == --write ]]; then
    "$formatter" -w "./$file"
  else
    # A formatter error is different from an unformatted file. Both must fail.
    output=$("$formatter" -l "./$file")
    if [[ -n "$output" ]]; then
      unformatted+=("$file")
      if [[ -n "$artifact_dir" ]]; then
        destination="$artifact_dir/files/$file"
        mkdir -p -- "$(dirname "$destination")"
        "$formatter" "./$file" > "$destination"
      fi
    fi
  fi
done

if [[ ${#unformatted[@]} -gt 0 ]]; then
  printf 'Unformatted tracked Go files:\n' >&2
  printf '%q\n' "${unformatted[@]}" >&2
  if [[ -n "$artifact_dir" ]]; then
    {
      printf 'Go %s: unformatted tracked Go files (shell-quoted paths):\n' "$version"
      printf '%q\n' "${unformatted[@]}"
    } > "$artifact_dir/result.txt"
  fi
  exit 1
fi
if [[ -n "$artifact_dir" ]]; then
  printf 'Go %s: all tracked Go files are formatted.\n' "$version" > "$artifact_dir/result.txt"
fi
