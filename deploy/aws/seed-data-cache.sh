#!/usr/bin/env bash
set -euo pipefail

# Deploy and run the seed-data-cache Lambda that copies the banhmi fetch
# cache from a GCS bucket (via S3-compat interop) into an S3 bucket.
# Resumable — re-invoke until the response shows "done": true.
#
# Usage:
#   bash deploy/aws/seed-data-cache.sh deploy   # create or update Lambda
#   bash deploy/aws/seed-data-cache.sh run       # synchronous invoke

# ── Config ───────────────────────────────────────────────────────────────────
FUNCTION_NAME="banhmi-seed-data-cache"
REGION="ap-southeast-1"
RUNTIME="python3.13"
HANDLER="seed_data_cache.lambda_handler"
ROLE="arn:aws:iam::$(aws sts get-caller-identity --query Account --output text):role/banhmi-seed-lambda"
TIMEOUT=900
MEMORY=1024
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# ─────────────────────────────────────────────────────────────────────────────

cmd_deploy() {
  WORK=$(mktemp -d)
  trap 'rm -rf "$WORK"' EXIT

  # Lambda handler module name must use underscores, not dashes.
  cp "${SCRIPT_DIR}/seed-data-cache.py" "${WORK}/seed_data_cache.py"
  (cd "$WORK" && zip -q seed_data_cache.zip seed_data_cache.py)

  # Create or update.
  if aws lambda get-function --function-name "$FUNCTION_NAME" --region "$REGION" >/dev/null 2>&1; then
    echo "Updating existing function ${FUNCTION_NAME}..."
    aws lambda update-function-code \
      --function-name "$FUNCTION_NAME" \
      --zip-file "fileb://${WORK}/seed_data_cache.zip" \
      --region "$REGION"
    aws lambda wait function-updated-v2 \
      --function-name "$FUNCTION_NAME" \
      --region "$REGION"
  else
    echo "Creating function ${FUNCTION_NAME}..."
    aws lambda create-function \
      --function-name "$FUNCTION_NAME" \
      --runtime "$RUNTIME" \
      --handler "$HANDLER" \
      --role "$ROLE" \
      --timeout "$TIMEOUT" \
      --memory-size "$MEMORY" \
      --zip-file "fileb://${WORK}/seed_data_cache.zip" \
      --region "$REGION"
    aws lambda wait function-active-v2 \
      --function-name "$FUNCTION_NAME" \
      --region "$REGION"
  fi

  echo "Done."
}

cmd_run() {
  OUTFILE=$(mktemp)
  trap 'rm -f "$OUTFILE"' EXIT

  echo "Invoking ${FUNCTION_NAME} (sync, timeout ${TIMEOUT}s)..."
  aws lambda invoke \
    --function-name "$FUNCTION_NAME" \
    --cli-binary-format raw-in-base64-out \
    --cli-read-timeout 920 \
    --region "$REGION" \
    "$OUTFILE"

  echo "--- Response ---"
  cat "$OUTFILE"
  echo
}

case "${1:-}" in
  deploy) cmd_deploy ;;
  run)    cmd_run ;;
  *)
    echo "Usage: $0 {deploy|run}" >&2
    exit 1
    ;;
esac
