# Build cache

Container builds download ~1.4 GB of dependencies (ONNX model, ORT runtime, tokenizer lib).
The cache scripts pre-stage these in S3 and GCS so builds pull from in-region storage instead
of GitHub/HuggingFace — faster and hermetic.

## Cache layout

```
{bucket}/deps/
  qwen3-embedding/
    model_fp16.onnx        584 KB   (graph)
    model_fp16.onnx_data   1.2 GB   (weights, external data format)
    tokenizer.json         11 MB    (BPE tokenizer)
  ort/{version}/
    onnxruntime-linux-x64.tgz       (x86_64)
    onnxruntime-linux-aarch64.tgz   (ARM64, AWS only)
  tokenizers/{version}/
    libtokenizers.linux-amd64.tar.gz
    libtokenizers.linux-arm64.tar.gz (ARM64, AWS only)
```

## Buckets (private, not public)

| Cloud | Bucket | Region |
|-------|--------|--------|
| AWS   | `banhmi-build-cache` | `ap-southeast-1` |
| GCP   | `danny-banhmi-build-cache` | `asia-southeast1` |

Buckets are **private** — no public access. Builds access them via authenticated channels:
- **Cloud Build (GCP):** runs without `CACHE_BASE`; the Containerfile falls back to
  HuggingFace/GitHub directly (Cloud Build has fast egress). To use the GCS cache, add a
  `gsutil cp` step before the Docker build to download assets into the build context.
- **EC2/ECS (AWS):** the instance's IAM role has S3 read access; Containerfile builds on the
  instance can use `CACHE_BASE=https://...` with presigned URLs or pull via `aws s3 cp` in a
  build script.

## Seeding the cache (zero local bandwidth)

The cache scripts download from upstream **on the cloud provider's network**, not locally.

**AWS — via Lambda:**
```bash
# One-off: create Lambda, invoke, delete. All server-side.
# See deploy/aws/cache-build-deps.sh for the full script.
# To avoid local bandwidth: write a small Lambda handler that
# curls from HuggingFace/GitHub and uploads to S3 via boto3,
# then invoke it with the file list as the event payload.
```

**GCP — via Cloud Run Job:**
```bash
gcloud run jobs create cache-deps \
  --image google/cloud-sdk:slim \
  --region asia-southeast1 \
  --task-timeout 600 --max-retries 0 --memory 512Mi \
  --command bash \
  --args '-c,
    HF=https://huggingface.co/onnx-community/Qwen3-Embedding-0.6B-ONNX/resolve/main
    GCS=gs://danny-banhmi-build-cache/deps/qwen3-embedding
    curl -fsSL "$HF/onnx/model_fp16.onnx" | gsutil cp - "$GCS/model_fp16.onnx"
    curl -fsSL "$HF/onnx/model_fp16.onnx_data" | gsutil cp - "$GCS/model_fp16.onnx_data"
    curl -fsSL "$HF/tokenizer.json" | gsutil cp - "$GCS/tokenizer.json"'

gcloud run jobs execute cache-deps --region asia-southeast1 --wait
gcloud run jobs delete cache-deps --region asia-southeast1 --quiet
```

## Using the cache in Containerfile builds

All Containerfiles accept `--build-arg CACHE_BASE=<url>`. When set, `curl` pulls from the
cache URL; when empty, it falls back to upstream (HuggingFace/GitHub).

```bash
# From GCS cache (requires authenticated access):
podman build -t banhmi-onnx \
  --build-arg CACHE_BASE=https://storage.googleapis.com/danny-banhmi-build-cache/deps \
  -f deploy/containerfiles/Containerfile.cloudrun.onnx .

# From S3 cache:
podman build -t banhmi-onnx \
  --build-arg CACHE_BASE=https://banhmi-build-cache.s3.ap-southeast-1.amazonaws.com/deps \
  -f deploy/containerfiles/Containerfile.cloudrun.onnx .

# No cache (pull from upstream directly):
podman build -t banhmi-onnx \
  -f deploy/containerfiles/Containerfile.cloudrun.onnx .
```

## Updating the cache

Run the seed scripts when dependency versions change (ORT version bump, model switch, etc.):
- `deploy/aws/cache-build-deps.sh` — downloads + uploads to S3
- `deploy/gcp/cache-build-deps.sh` — downloads + uploads to GCS

For zero-local-bandwidth updates, use Lambda (AWS) or Cloud Run Job (GCP) as shown above.
