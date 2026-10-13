#!/usr/bin/env bash
set -euo pipefail
python3 tools/ci/corpus_generation.py stage auth --output "${1:?output directory required}"
