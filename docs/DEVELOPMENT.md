# Local development

The normal development setup runs PostgreSQL in Podman and runs Go commands on the host. The full
Compose application profile is optional. See [`ARCHITECTURE.md`](ARCHITECTURE.md) for the system and
[`design/PIPELINE.md`](design/PIPELINE.md) for pipeline behavior.

## Prerequisites

- Podman with `podman compose` for PostgreSQL.
- Go 1.26.3.
- `sqlc` only when running `make generate`.
- Atlas CLI only when running `make migrate-gen`. The goose migration library is built into
  `cmd/migrate`; no goose CLI is required.
- `golangci-lint` for `make lint` and preferred formatting.
- `air` only for `make pipeline-dev`.

Query and bulk embedding have separate setup. Plain builds and unit tests need neither model files
nor a running embedder.

## Initial setup

1. Copy the host configuration:

   ```bash
   cp config/config.example.yaml config/config.yaml
   ```

   `config/config.yaml` is gitignored. The example connects to PostgreSQL on
   `localhost:10001`.

2. Set the documented local database password:

   ```bash
   export BANHMI_DATABASE_PASSWORD=banhmi
   ```

3. Start PostgreSQL, apply migrations, and seed operator vocabularies:

   ```bash
   make dev-up
   make migrate
   go run ./cmd/seed
   ```

`make dev-up` starts only PostgreSQL 17 with pgvector. It does not start an embedder, the pipeline,
or the MCP server.

For another jurisdiction, set `BANHMI_JURISDICTION=<cc>`. Unless the database name is explicit in
the config or `BANHMI_DATABASE_NAME`, the built-in jurisdiction descriptor selects its normal
database name. Create that database in the development container before migrating it, for example:

```bash
podman compose -f deploy/compose/banhmi.yaml exec postgres createdb -U banhmi <database>
```

## Query embedding

Local search uses Qwen3-Embedding-0.6B ONNX FP16 with 1,024 dimensions. Use either mode below.

### In-process ONNX Runtime

Place these files in `~/.cache/banhmi/qwen3-embedding/`:

- `model_fp16.onnx`
- `model_fp16.onnx_data`
- `tokenizer.json`

Place `libonnxruntime.so` in `~/.local/lib/`. The Makefile supplies the build tag, CGo flags, model
paths, and runtime path:

```bash
make mcp-onnx
```

This starts the Streamable HTTP MCP server on `:8088`. `make eval-onnx` uses the same setup for
retrieval evaluation.

### Split embedder

Start the standalone service:

```bash
make embedder-onnx
```

It listens on `:8089` and exposes `POST /embeddings` plus `GET /ready`. In another shell, point the
MCP process at it:

```bash
export BANHMI_EMBED_ENDPOINT=http://127.0.0.1:8089
go run ./cmd/server -addr localhost:9099
```

An empty `BANHMI_EMBED_TOKEN` disables embedder authentication for local development. If you set a
token, set the same value for both processes.

## Build a corpus

For a complete run, select the Kaggle bulk engine. It is the only `EmbedAll` branch that matches the
current Qwen3 index:

```bash
export BANHMI_EMBED_ENGINE=kaggle
export KAGGLE_API_TOKEN='<token>'
go run ./cmd/pipeline -run-all
```

It runs discovery, drains fetch/extract/normalize, performs batch OCR, re-normalizes OCR results,
builds chunks, embeds them, and builds BM25 sparse vectors. Use `-limit N` for a small development
run. `EmbedAll` implements Kaggle and SageMaker, but the wired SageMaker path still embeds BGE-M3 and
must not write to the current Qwen3 index. Its `local` and `onnx` configuration values are not
implemented, so `-run-all` rejects them. For local HTTP or ONNX maintenance embedding, build chunks
through `-index-all`, then use `cmd/embed-backfill`. See `go run ./cmd/pipeline -h` and
`go run ./cmd/embed-backfill -h` for the available flags.

For manual stage work, preserve this order:

1. `go run ./cmd/pipeline -discover <source>`
2. `go run ./cmd/pipeline -fetch <source>`
3. `go run ./cmd/pipeline -extract-all`
4. `go run ./cmd/pipeline -normalize-all`
5. `go run ./cmd/pipeline -ocr-all`
6. `go run ./cmd/pipeline -normalize-all` if OCR processed documents
7. `go run ./cmd/pipeline -index-all`
8. `go run ./cmd/pipeline -embed-all`
9. `go run ./cmd/pipeline -lexindex`

Use `all` as the source where the command help permits it. `-index-all` builds chunks; embedding is
a separate stage. Each stage exits after its own work, and the database ledger is the handoff.

### Bulk embedding

`BANHMI_EMBED_ENGINE=auto` selects Kaggle when `KAGGLE_API_TOKEN` is set and otherwise resolves to
`local`. Because `EmbedAll` does not implement `local`, set the bulk engine explicitly for a full
pipeline run:

```bash
export BANHMI_EMBED_ENGINE=kaggle
export KAGGLE_API_TOKEN='<token>'
```

The pipeline uploads chunk text as a Kaggle dataset, runs a GPU kernel, downloads vectors, and
deletes the kernel and input dataset after success. Chunking stays local. Do not bulk-embed a full
corpus on a small CPU-only host.

### Optical character recognition

OCR is a batch fallback for gate-flagged scans. The engine values are:

| Value | Behavior |
|---|---|
| `documentai` | Google Vision `images:annotate`; the name is retained for compatibility |
| `local` | Local EasyOCR |
| `kaggle` | EasyOCR on Kaggle |
| `auto` | Kaggle when its token is set; otherwise local EasyOCR |

For Google Vision, set `BANHMI_OCR_ENGINE=documentai` and provide Application Default Credentials,
for example with `GOOGLE_APPLICATION_CREDENTIALS`. Do not assume the production service account or
its Systems Manager parameter is available to every developer.

## Storage

The compiled storage default becomes `data/<jurisdiction>/`, including the `ocr/` text cache. An
explicit `BANHMI_STORAGE_DIR` is used unchanged.

`BANHMI_S3_DATA_BUCKET` enables the pipeline's S3 mirror. Manual synchronization, when needed, uses
the same layout:

```bash
aws s3 sync data/<jurisdiction>/ s3://<bucket>/files/ --exclude 'ocr/*'
aws s3 sync data/<jurisdiction>/ocr/ s3://<bucket>/ocr/
```

These commands write to S3. Confirm the jurisdiction, bucket, AWS account, and region before running
them.

## Serve the MCP tools

- Local stdio client: `go run ./cmd/mcp`
- Streamable HTTP: `go run ./cmd/server -addr localhost:9099`, then connect to
  `http://localhost:9099/mcp`
- In-process ONNX HTTP server: `make mcp-onnx`, then connect to `http://localhost:8088/mcp`

All three need a query embedder for hybrid search. Use an MCP client to initialize a session and call
`guide`, `corpus_status`, `search`, and `document`; a plain one-shot `curl` request is not a complete
MCP session test.

## Commands

| Command | Result |
|---|---|
| `make build` | Run `go build ./...` without leaving binaries in the repository |
| `make test` | Run `go test ./...` |
| `make vet` | Run `go vet ./...` |
| `make lint` | Run `golangci-lint run ./...` |
| `make fmt` | Format Go and sort imports with golangci-lint, falling back to `gofmt` |
| `make generate` | Regenerate sqlc code from `sql/` |
| `make migrate-gen` | Run Atlas diffs and update goose-formatted migrations plus `atlas.sum` |
| `make migrate` | Verify migration hashes and apply pending migrations |
| `make dev-up` / `make dev-down` | Start or stop the development PostgreSQL container |
| `make dev-reset` | Stop PostgreSQL and delete its Compose volume |
| `make stack-up` / `make stack-down` | Start or stop the Compose application profile; provider credentials and query embedding still need configuration |
| `make pipeline-dev` | Run the pipeline through `air` hot reload |
| `make embedder-onnx` | Run the standalone ONNX embedder on `:8089` |
| `make mcp-onnx` | Run the in-process ONNX MCP server on `:8088` |
| `make eval-vn`, `eval-my`, `eval-id`, `eval-sg`, `eval-th`, `eval-kh` | Run the jurisdiction evaluation and enforce its current floors |

`make dev-reset` destroys the local database volume. Back up local work before using it.

## Repository rules

- Change SQL sources under `sql/`; do not hand-edit generated code under `pkg/store/`.
- After a schema change, run `make migrate-gen`, review the migration, then run `make generate` when
  queries or generated models changed.
- Do not expect `-index-all -force` to reproduce a live corpus. It replaces chunk IDs and requires a
  full re-embed and evaluation re-baseline.
- Keep secrets out of YAML and Git. The fixed local PostgreSQL password above is the development-only
  exception.
