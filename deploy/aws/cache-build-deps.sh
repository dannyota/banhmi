#!/usr/bin/env bash
set -euo pipefail

# Cache container build dependencies in S3 so Containerfile builds pull from
# in-region S3 instead of GitHub/HuggingFace. Run once (or when versions bump)
# from any machine with aws CLI + internet. Idempotent — overwrites existing.
#
# Usage: bash deploy/aws/cache-build-deps.sh
# Requires: AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY or IAM role with s3:PutObject.

# ── Config ───────────────────────────────────────────────────────────────────
BUCKET="${BANHMI_S3_CACHE_BUCKET:-banhmi-build-cache}"
REGION="${AWS_DEFAULT_REGION:-ap-southeast-1}"
ORT_VERSION="1.26.0"
TOKENIZERS_VERSION="v1.27.0"
HF_REPO="onnx-community/Qwen3-Embedding-0.6B-ONNX"
# ─────────────────────────────────────────────────────────────────────────────

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

echo "==> Downloading build dependencies to $WORK"

# ONNX Runtime — CPU for read path (both arches), GPU for embedder (x64)
for ARCH in x64 aarch64; do
  TARBALL="onnxruntime-linux-${ARCH}-${ORT_VERSION}.tgz"
  echo "  ORT ${ARCH} CPU..."
  curl -fsSL "https://github.com/microsoft/onnxruntime/releases/download/v${ORT_VERSION}/${TARBALL}" \
    -o "${WORK}/${TARBALL}"
done
echo "  ORT x64 GPU..."
curl -fsSL "https://github.com/microsoft/onnxruntime/releases/download/v${ORT_VERSION}/onnxruntime-linux-x64-gpu-${ORT_VERSION}.tgz" \
  -o "${WORK}/onnxruntime-linux-x64-gpu-${ORT_VERSION}.tgz"

# Tokenizer lib — both architectures
for ARCH in amd64 arm64; do
  TARBALL="libtokenizers.linux-${ARCH}.tar.gz"
  echo "  tokenizers ${ARCH}..."
  curl -fsSL "https://github.com/daulet/tokenizers/releases/download/${TOKENIZERS_VERSION}/${TARBALL}" \
    -o "${WORK}/${TARBALL}"
done

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

echo "==> Uploading to s3://${BUCKET}/deps/"

# Ensure bucket exists (no-op if it does)
aws s3api head-bucket --bucket "$BUCKET" 2>/dev/null || \
  aws s3api create-bucket --bucket "$BUCKET" --region "$REGION" \
    --create-bucket-configuration LocationConstraint="$REGION"

aws s3 cp "${WORK}/onnxruntime-linux-x64-${ORT_VERSION}.tgz" \
  "s3://${BUCKET}/deps/ort/${ORT_VERSION}/onnxruntime-linux-x64.tgz"
aws s3 cp "${WORK}/onnxruntime-linux-x64-gpu-${ORT_VERSION}.tgz" \
  "s3://${BUCKET}/deps/ort/${ORT_VERSION}/onnxruntime-linux-x64-gpu.tgz"
aws s3 cp "${WORK}/onnxruntime-linux-aarch64-${ORT_VERSION}.tgz" \
  "s3://${BUCKET}/deps/ort/${ORT_VERSION}/onnxruntime-linux-aarch64.tgz"

aws s3 cp "${WORK}/libtokenizers.linux-amd64.tar.gz" \
  "s3://${BUCKET}/deps/tokenizers/${TOKENIZERS_VERSION}/libtokenizers.linux-amd64.tar.gz"
aws s3 cp "${WORK}/libtokenizers.linux-arm64.tar.gz" \
  "s3://${BUCKET}/deps/tokenizers/${TOKENIZERS_VERSION}/libtokenizers.linux-arm64.tar.gz"

aws s3 cp "${WORK}/model_fp16.onnx"      "s3://${BUCKET}/deps/qwen3-embedding/model_fp16.onnx"
aws s3 cp "${WORK}/model_fp16.onnx_data" "s3://${BUCKET}/deps/qwen3-embedding/model_fp16.onnx_data"
aws s3 cp "${WORK}/tokenizer.json"       "s3://${BUCKET}/deps/qwen3-embedding/tokenizer.json"

echo "==> Done. S3 layout:"
aws s3 ls "s3://${BUCKET}/deps/" --recursive --human-readable
