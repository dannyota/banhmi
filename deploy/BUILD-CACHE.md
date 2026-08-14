# Build dependency cache

The private S3 bucket `banhmi-build-cache` stores large ONNX build inputs in
`ap-southeast-1`. It reduces repeated downloads from GitHub and Hugging Face. It does not make a
container build hermetic: base images and Go modules still come from their configured registries.

## Contents

`deploy/aws/cache-build-deps.sh` uploads this layout:

```text
s3://banhmi-build-cache/deps/
├── qwen3-embedding/
│   ├── model_fp16.onnx
│   ├── model_fp16.onnx_data
│   └── tokenizer.json
├── ort/1.26.0/
│   ├── onnxruntime-linux-aarch64.tgz
│   ├── onnxruntime-linux-x64.tgz
│   └── onnxruntime-linux-x64-gpu.tgz
└── tokenizers/v1.27.0/
    ├── libtokenizers.linux-amd64.tar.gz
    └── libtokenizers.linux-arm64.tar.gz
```

The model weights account for most of the download. The script and Containerfile arguments define
the versions. The Containerfile checksum arguments define the accepted artifact hashes. The example
versions above can become stale.

## Seed or refresh the cache

Requirements:

- AWS CLI credentials with `s3:CreateBucket`, `s3:ListBucket`, and `s3:PutObject` for the cache
  bucket;
- outbound HTTPS access to GitHub and Hugging Face;
- enough local disk space for the downloaded files.

Run from the repository root:

```bash
bash deploy/aws/cache-build-deps.sh
```

Set `BANHMI_S3_CACHE_BUCKET` or `AWS_DEFAULT_REGION` to override the default bucket or region. The
script overwrites objects at the same keys. It does not delete older version directories.

Verify the upload:

```bash
aws s3 ls s3://banhmi-build-cache/deps/ --recursive --human-readable
```

## Use the cache

The bucket blocks public access. The current embedder Containerfile accepts an HTTP `CACHE_BASE`
and downloads with `curl`; it cannot authenticate directly to private S3. A raw URL such as
`https://banhmi-build-cache.s3.ap-southeast-1.amazonaws.com/deps` therefore fails unless another
authenticated service exposes those objects.

The retired CodeBuild flow in `deploy/aws/buildspec-mcp.yml` shows the supported private-cache
pattern: copy the S3 prefix with the AWS CLI, serve the copied files over a local HTTP endpoint, and
pass that endpoint as `CACHE_BASE`. CodeBuild itself was retired on 2026-08-02.

For a normal local build, omit `CACHE_BASE`. The build then downloads pinned upstream artifacts:

```bash
podman build --platform linux/arm64 \
  -f deploy/containerfiles/Containerfile.ecs.embedder \
  -t banhmi-embedder .
```

The tracked `cache/` directories are placeholders. Current Containerfiles do not read them.

## Change a dependency

When the model, ONNX Runtime, or tokenizer library changes:

1. Update the versions and upstream paths in `deploy/aws/cache-build-deps.sh`.
2. Update the matching arguments in each Containerfile that uses the dependency.
3. Recompute every affected SHA-256 value in those Containerfiles.
4. Run the cache script and verify the new S3 keys.
5. Build once from upstream and once through the authenticated cache path.

Do not reuse a checksum from an older dependency version. The Containerfiles reject mismatched
artifacts during the build.
