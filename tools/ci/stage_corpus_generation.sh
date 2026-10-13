#!/usr/bin/env bash
set -euo pipefail
python3 tools/ci/corpus_generation.py stage corpus --output "${1:?output directory required}"
