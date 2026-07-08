#!/usr/bin/env bash
set -euo pipefail

# Cache container build dependencies in GCS so Containerfile builds pull from
# in-region GCS instead of GitHub/HuggingFace. Run once (or when versions bump)
# from any machine with gcloud CLI + internet. Idempotent — overwrites existing.
#
# Usage: bash deploy/gcp/cache-build-deps.sh
# Requires: gcloud auth (danh.software@gmail.com).

# ── Config ───────────────────────────────────────────────────────────────────
BUCKET="${BANHMI_GCS_CACHE_BUCKET:-danny-banhmi-build-cache}"
REGION="asia-southeast1"
ORT_VERSION="1.26.0"
TOKENIZERS_VERSION="v1.27.0"
HF_REPO="onnx-community/Qwen3-Embedding-0.6B-ONNX"
# ─────────────────────────────────────────────────────────────────────────────

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

echo "==> Downloading build dependencies to $WORK"

# ONNX Runtime — amd64 only (GCP write path is x64)
echo "  ORT x64..."
curl -fsSL "https://github.com/microsoft/onnxruntime/releases/download/v${ORT_VERSION}/onnxruntime-linux-x64-${ORT_VERSION}.tgz" \
  -o "${WORK}/onnxruntime-linux-x64-${ORT_VERSION}.tgz"

# Tokenizer lib — amd64 only
echo "  tokenizers amd64..."
curl -fsSL "https://github.com/daulet/tokenizers/releases/download/${TOKENIZERS_VERSION}/libtokenizers.linux-amd64.tar.gz" \
  -o "${WORK}/libtokenizers.linux-amd64.tar.gz"

# Qwen3-Embedding model (FP16, external data format)
echo "  model_fp16.onnx..."
curl -fsSL "https://huggingface.co/${HF_REPO}/resolve/main/onnx/model_fp16.onnx" \
  -o "${WORK}/model_fp16.onnx"
echo "  model_fp16.onnx_data..."
curl -fsSL "https://huggingface.co/${HF_REPO}/resolve/main/onnx/model_fp16.onnx_data" \
  -o "${WORK}/model_fp16.onnx_data"
echo "  tokenizer.json..."
curl -fsSL "https://huggingface.co/${HF_REPO}/resolve/main/tokenizer.json" \
  -o "${WORK}/tokenizer.json"

echo "==> Uploading to gs://${BUCKET}/deps/"

# Ensure bucket exists
gsutil ls "gs://${BUCKET}/" 2>/dev/null || \
  gsutil mb -l "$REGION" "gs://${BUCKET}/"

gsutil -m cp \
  "${WORK}/onnxruntime-linux-x64-${ORT_VERSION}.tgz" \
  "gs://${BUCKET}/deps/ort/${ORT_VERSION}/onnxruntime-linux-x64.tgz"

gsutil -m cp \
  "${WORK}/libtokenizers.linux-amd64.tar.gz" \
  "gs://${BUCKET}/deps/tokenizers/${TOKENIZERS_VERSION}/libtokenizers.linux-amd64.tar.gz"

gsutil -m cp \
  "${WORK}/model_fp16.onnx"      "gs://${BUCKET}/deps/qwen3-embedding/model_fp16.onnx"
gsutil -m cp \
  "${WORK}/model_fp16.onnx_data" "gs://${BUCKET}/deps/qwen3-embedding/model_fp16.onnx_data"
gsutil -m cp \
  "${WORK}/tokenizer.json"       "gs://${BUCKET}/deps/qwen3-embedding/tokenizer.json"

echo "==> Done. GCS layout:"
gsutil ls -lh "gs://${BUCKET}/deps/**"
