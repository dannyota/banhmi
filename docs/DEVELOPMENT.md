# Local development

Everything runs in **podman** containers; the only host installs are Go and a few dev CLIs. The dev
stack is the checked-in localhost config -- connect to it freely. Architecture: [`ARCHITECTURE.md`](ARCHITECTURE.md);
pipeline commands: [`design/PIPELINE.md`](design/PIPELINE.md).

## Prerequisites

1. **podman** + **podman-compose** (no host service installs).
2. **Go 1.26.3**.
3. **sqlc** (for `make generate`), **Atlas** + **goose** (for `make migrate-gen`).
4. **Embedder** (for indexing + query) -- see [Section 3](#3-embedder-for-index--query). Not needed for
   plain `build`/`test`.

## 1. Config

1. `cp config/config.example.yaml config/config.yaml` -- `config.yaml` is gitignored (your local dev config).
2. `export BANHMI_DATABASE_PASSWORD=banhmi` -- the local dev DB password.
3. The dev config points at the podman stack; Postgres port lives in `config/config.yaml` (`:10001`).

## 2. Start infra + schema

1. `make dev-up` -- Postgres 17 + pgvector (matches prod RDS). No other services.
2. `make migrate` -- apply schema migrations (goose + `atlas.sum` verification).
3. `go run ./cmd/seed` -- load operator vocabularies (scope terms, issuer codes, discovery keywords) from `deploy/seed/*.csv`.

## 3. Embedder (for index + query)

**Qwen3-Embedding-0.6B ONNX FP16** is the embedder everywhere (index and query parity, 1024 dims).
`build`/`test` need none.

### Query-time -- in-process ONNX Runtime (for eval / MCP)

Needed for `make eval-onnx` and `make mcp-onnx`. One-time setup:

- **Model files:** `model_fp16.onnx` + `model_fp16.onnx_data` + `tokenizer.json` in `~/.cache/banhmi/qwen3-embedding/`.
- **Runtime:** `libonnxruntime.so` (ORT 1.26.0) in `~/.local/lib/`.
- **Build tag:** `-tags onnx` (the Makefile wires CGO flags and env automatically).
- **Targets:** `make eval-onnx` (RAG accuracy eval), `make mcp-onnx` (local MCP HTTP server on `:8088`).

### Bulk indexing -- Kaggle T4 GPU (dataset I/O)

Pipeline uploads chunk texts as a **Kaggle dataset**, pushes a GPU kernel (Qwen3-Embedding ONNX FP16), polls to completion, downloads the output vectors, and upserts `gold.chunk_embedding`. Each run gets a fresh GPU; on success the kernel + input dataset auto-delete.

- **`BANHMI_EMBED_ENGINE=kaggle`** + **`KAGGLE_API_TOKEN`** -- no GCS or GCP credentials involved.
- Chunking stays local; only the embedding step offloads to the Kaggle GPU kernel.
- `engine=local` embeds in-process (small batches only -- never the full corpus on the dev machine).

### Never bulk-embed locally

The dev machine (8 GB) cannot handle batch GPU workloads. Offload to Kaggle.
Query-time embedding locally is fine (~50 ms per query).

## 4. Build the corpus (the pipeline)

Stages are explicit -- no stage auto-starts the next (the DB ledger is the handoff). Run per stage or
the whole thing. See [`design/PIPELINE.md`](design/PIPELINE.md) and `go run ./cmd/pipeline -h` for all
flags.

1. `go run ./cmd/pipeline -fetch <source>` -- drain discovered docs to Bronze.
2. `go run ./cmd/pipeline -extract-all` -- Bronze to Silver (text extraction via **go-fitz** / MuPDF, zero-Python).
3. `go run ./cmd/pipeline -normalize-all` -- Silver to structured sections + validity.
4. `go run ./cmd/pipeline -index-all` -- Gold chunks + embeddings.
5. `go run ./cmd/pipeline -embed-all [-force]` -- bulk embed via Kaggle (needs engine env vars above).
6. `go run ./cmd/pipeline -lexindex` -- rebuild BM25 sparse vectors for hybrid retrieval.
7. **Whole pipeline to convergence:** `go run ./cmd/pipeline -run-all`.

**OCR** (scanned / failed PDFs): **Google Vision OCR** is the default engine (`images:annotate`,
file-first cache: local `{storageDir}/ocr/` + S3 mirror); **EasyOCR** is the offline fallback.
OCR runs as a batch (`-ocr-all`), never inline.

## 5. Serve + query the MCP

1. **stdio** (local MCP clients): `go run ./cmd/mcp`.
2. **HTTP / Streamable** (remote clients): `go run ./cmd/server -addr localhost:9099` -- POST `/mcp`.
3. Drive it with curl (`initialize` -- `tools/call` `search`/`document`) or a Haiku sub-agent acting as an external agent.

## 6. Everyday commands

| Command | What it does |
|---------|--------------|
| `make build` | `go build ./...` (compile check; no binaries left in the tree) |
| `make test` | `go test ./...` |
| `make fmt` | format + import sorting (run after touching Go) |
| `make generate` | regenerate sqlc after `sql/**/queries.sql` or `schema.sql` changes |
| `make migrate-gen` | Atlas diff -- goose migration + `atlas.sum` after `sql/**/schema.sql` changes |
| `make lint` | golangci-lint + project linters |
| `make eval-vn` / `eval-my` / `eval-id` | RAG accuracy eval per jurisdiction (with baseline floors) |
| `make mcp-onnx` | local MCP HTTP server with in-process ONNX Runtime on `:8088` |
| `make dev-up` / `make dev-down` | start / stop the dev Postgres container |
| `make dev-reset` | stop + wipe volumes (fresh DB) |
| `make pipeline-dev` | run the pipeline with hot reload (needs `air`) |

## Notes

1. **Layers communicate through the database** (Bronze -- Silver -- Gold), not Go imports.
2. **Don't edit generated code under `pkg/store/`** -- change `sql/` and `make generate`.
3. Pre-release the DB is **not immutable**: edit `sql/**/schema.sql`, `make migrate-gen`, then reset with `make dev-reset && make migrate`.
4. **Secrets** live in env/file/Vault, never in YAML. The local dev password (`banhmi`) is the documented exception.
5. **Service accounts for local testing:** the only GCP service is Vision OCR — set `GOOGLE_APPLICATION_CREDENTIALS` to the gitignored SA key (fetch from SSM `/banhmi/gcp-sa-key`). Vision needs no IAM role beyond the enabled API; the SA carries no roles. See [`DEPLOYMENT.md`](DEPLOYMENT.md).
