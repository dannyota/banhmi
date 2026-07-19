# Build cache

Container builds download ~1.4 GB of dependencies (ONNX model, ORT runtime, tokenizer lib).
The cache scripts pre-stage these in S3 so builds pull from in-region storage instead
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
    onnxruntime-linux-aarch64.tgz   (ARM64)
  tokenizers/{version}/
    libtokenizers.linux-amd64.tar.gz
    libtokenizers.linux-arm64.tar.gz (ARM64)
```

## Bucket (private, not public)

| Cloud | Bucket | Region |
|-------|--------|--------|
| AWS   | `banhmi-build-cache` | `ap-southeast-1` |

The bucket is **private** — no public access. Builds access it via authenticated channels:
the EC2/ECS instance's IAM role has S3 read access; Containerfile builds on the instance
can use `CACHE_BASE=https://...` with presigned URLs or pull via `aws s3 cp` in a build
script.

## Seeding the cache

**AWS — via local script (uses local bandwidth):**
```bash
bash deploy/aws/cache-build-deps.sh
```

## Using the cache

### Local build (pulls from upstream directly)

```bash
podman build -t banhmi-pipeline \
  -f deploy/containerfiles/Containerfile .
```

The `cache/` directory in the repo root has empty subdirectories (tracked via `.gitkeep`).
Builds can pre-populate them from S3; local builds leave them empty and the Containerfile
curls from upstream.

## Updating the cache

When dependency versions change (ORT version bump, model switch), re-run the local script
above. Update the version ARGs in the Containerfiles and the buildspec yaml.
