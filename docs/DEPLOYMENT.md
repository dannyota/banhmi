# Deployment

banhmi has a write path and a read path. Both use PostgreSQL, but they do not need to run on the
same host. The pipeline may also use object storage for fetched files and optical character
recognition (OCR) text. The Model Context Protocol (MCP) server needs a query embedder.

This document first describes the portable deployment contract. The final section records banhmi's
current reference deployment on Amazon Web Services (AWS).

## Runtime components

| Component | Purpose | Network access |
|---|---|---|
| PostgreSQL 17 with pgvector | Stores pipeline state, normalized law, chunks, dense vectors, and BM25 sparse vectors | Private access from the pipeline and MCP server |
| `cmd/pipeline` | Discovers, fetches, extracts, normalizes, chunks, embeds, and builds the lexical index | Database, source websites, and any configured OCR, embedding, or object-storage provider |
| Query embedder | Embeds each search query with Qwen3-Embedding-0.6B FP16, 1,024 dimensions | In-process, or a private `cmd/embedder` service |
| `cmd/server` | Serves the evidence tools over Streamable HTTP at `/mcp` | Database, query embedder, and HTTPS ingress |

`cmd/mcp` is the local stdio alternative to `cmd/server`. It uses the same database and query
embedder.

Use one database per jurisdiction. A single `cmd/server` process may serve one jurisdiction or route
several jurisdiction databases through one shared query embedder.

## Portable deployment

### 1. Create each database

Use PostgreSQL 17 with the pgvector extension. The schema uses both HNSW dense-vector indexes and
pgvector's `sparsevec` type. ParadeDB and `pg_search` are not required.

For each jurisdiction:

1. Create its database.
2. Set the database connection variables and `BANHMI_JURISDICTION`.
3. Run `go run ./cmd/migrate`.
4. Run `go run ./cmd/seed`.

Require Transport Layer Security (TLS) for remote connections. Keep port 5432 private to the
pipeline and MCP server. Back up the database before applying new migrations.

### 2. Run the pipeline

Run one pipeline process per jurisdiction. `go run ./cmd/pipeline -run-all` performs these stages:

1. Discover.
2. Fetch, extract, and normalize to convergence.
3. OCR gate-flagged scans, then normalize the new text.
4. Build Gold chunks.
5. Build dense embeddings.
6. Build BM25 sparse vectors.

The pipeline is not a public service. It needs outbound access to the configured official sources.
Some sources restrict requests by country or client behavior, so place the pipeline where those
sources are reachable.

Fetched files use the local storage directory. `BANHMI_S3_DATA_BUCKET` adds an S3 mirror for files
and OCR text. The pipeline's `EmbedAll` stage implements `kaggle` and `sagemaker`, but only Kaggle
matches the current Qwen3 index. The wired SageMaker path still embeds BGE-M3 and must not be used
with that index. Although the configuration also accepts `local` and `onnx`, `EmbedAll` rejects them.
Use `cmd/embed-backfill` for local HTTP or ONNX maintenance embedding. `auto` selects Kaggle when
`KAGGLE_API_TOKEN` is set and otherwise resolves to the unsupported `local` bulk path, so set
`BANHMI_EMBED_ENGINE=kaggle` before using `-run-all`.

OCR is always a batch stage. `BANHMI_OCR_ENGINE=documentai` selects Google Vision OCR despite the
legacy engine name. `local` selects EasyOCR on the host, `kaggle` selects EasyOCR on Kaggle, and
`auto` selects Kaggle when its token is present and otherwise selects local EasyOCR.

### 3. Provide query embedding

The query model must match the indexed model and dimensions.

- In-process mode: build the MCP binary with `-tags onnx` and set
  `BANHMI_EMBED_QUERY=onnx`.
- Split mode: build `cmd/embedder` with `-tags onnx`, set `BANHMI_EMBED_ENDPOINT` on the MCP
  server, and set the same `BANHMI_EMBED_TOKEN` on both processes.

In split mode, keep the embedder on a private network or loopback. Use `GET /ready` for readiness;
it becomes successful only after a warm-up inference. `GET /healthz` is not an embedder endpoint.
The MCP server probes model identity and dimensions at startup and does not fall back silently when
the embedder is unavailable.

### 4. Deploy the MCP server

For a single jurisdiction, set `BANHMI_JURISDICTION` and the database variables. For a
multi-jurisdiction process:

1. Set `BANHMI_JURISDICTIONS` to comma-separated country codes such as `vn,my,id`.
2. Set `BANHMI_DATABASE_NAME_<CC>` when a database name differs from the built-in jurisdiction
   name.
3. Route each public host to the process with the correct `X-Banhmi-Jurisdiction` header.

Place `cmd/server` behind HTTPS. `GET /healthz` checks the HTTP process. After deployment, connect an
MCP client and verify `corpus_status`, then run a known `search` and open one result with `document`.

The server is public when `BANHMI_MCP_API_KEY` is empty. Set it to one or more comma-separated keys
to accept `Authorization: Bearer <key>` or `X-API-Key`. When a reverse proxy is the only trusted
ingress, also consider:

- `BANHMI_ORIGIN_VERIFY_SECRET`: require the proxy's `X-Origin-Verify` header.
- `BANHMI_TRUST_PROXY=true`: use the proxy-appended client address for rate limiting.
- `BANHMI_MCP_ALLOWED_ORIGINS`: allow listed browser origins through cross-origin protection.
- `BANHMI_MCP_RATE_RPS` and `BANHMI_MCP_RATE_BURST`: tune per-client rate limits.

Do not set `BANHMI_TRUST_PROXY=true` when clients can reach the server directly and supply their own
`X-Forwarded-For` header.

## Configuration reference

Secrets are environment-only. Do not store them in YAML or commit them.

| Variable | Used by | Purpose |
|---|---|---|
| `BANHMI_JURISDICTION` | pipeline, single-site MCP | Country code; defaults to `vn` |
| `BANHMI_JURISDICTIONS` | multi-site MCP | Comma-separated country codes |
| `BANHMI_DATABASE_HOST`, `PORT`, `USER`, `NAME`, `SSLMODE` | pipeline, MCP | PostgreSQL connection |
| `BANHMI_DATABASE_NAME_<CC>` | multi-site MCP | Per-jurisdiction database-name override |
| `BANHMI_DATABASE_PASSWORD` | pipeline, MCP | PostgreSQL password |
| `BANHMI_STORAGE_DIR` | pipeline | Local fetched-file and OCR-cache root |
| `BANHMI_S3_DATA_BUCKET` | pipeline | Optional S3 mirror bucket |
| `BANHMI_OCR_ENGINE` | pipeline | `documentai`, `local`, `kaggle`, or `auto` |
| `GOOGLE_APPLICATION_CREDENTIALS` | pipeline | Application Default Credentials file for Google Vision OCR when needed |
| `BANHMI_EMBED_ENGINE` | pipeline | Bulk engine; use `kaggle` for the current Qwen3 index |
| `KAGGLE_API_TOKEN` | pipeline | Kaggle API token for Kaggle embedding or OCR |
| `BANHMI_EMBED_QUERY` | MCP | `onnx` for in-process query embedding; otherwise use HTTP |
| `BANHMI_EMBED_ENDPOINT` | MCP | OpenAI-compatible query-embedder endpoint |
| `BANHMI_EMBED_TOKEN` | MCP, embedder | Bearer token between the MCP server and split embedder |
| `BANHMI_MCP_API_KEY` | MCP | Optional public-client API key or comma-separated keys |
| `BANHMI_ORIGIN_VERIFY_SECRET` | MCP | Optional proxy-to-origin secret or comma-separated rotation set |
| `BANHMI_TRUST_PROXY` | MCP | Trust the proxy-appended client address for rate limiting |

## Release order

For a change that includes schema, seed, pipeline, and server updates:

1. Take and verify a database backup.
2. Apply `cmd/migrate` and `cmd/seed` to each target database.
3. Run the required pipeline stages and verify chunk, dense-vector, and sparse-vector counts.
4. Start or update the query embedder and wait for `/ready`.
5. Deploy the MCP server.
6. Verify `/healthz`, `corpus_status`, a known search, and a document lookup through public ingress.

Do not use `-force` on a live corpus as a routine refresh. A force re-index creates new chunk IDs,
orphans existing embeddings, and may not reproduce the current retrieval baseline. Treat it as a
rebuild with a new evaluation baseline and rollback plan.

## Reference deployment

The repository's current production stack is one implementation of the portable contract:

- **Write path:** pipeline runs on the maintainer's machine. Bulk embedding uses Kaggle T4 GPUs.
  Google Vision handles OCR with `BANHMI_OCR_ENGINE=documentai`. S3 mirrors fetched files and OCR
  text. The parked self-terminating EC2 scripts currently cover `vn`, `my`, and `id`; they are not a
  general six-jurisdiction scheduler.
- **Database:** one private AWS Relational Database Service (RDS) PostgreSQL 17 instance in
  `ap-southeast-1`, with six databases: `banhmi`, `laksa`, `rendang`, `kaya`, `tomyum`, and `amok`.
- **Read path:** six CloudFront distributions route to one slim `cmd/server` service on Amazon
  Elastic Container Service (ECS) on one ARM64 EC2 `t4g.medium`. One `cmd/embedder` ECS service binds
  to `127.0.0.1:8089` on the same host. The server process serves all six databases and shares that
  embedder.
- **Images:** build locally from `deploy/containerfiles/Containerfile.ecs.server` and
  `deploy/containerfiles/Containerfile.ecs.embedder`, then push immutable tags to Amazon Elastic
  Container Registry (ECR). CodeBuild was retired on 2026-08-02.
- **Availability:** the EC2 host and RDS run 24/7. The six former EventBridge Scheduler start and
  stop schedules are disabled and retained only for rollback.

The ECS execution role pulls ECR images, injects AWS Systems Manager Parameter Store secrets, and
writes logs. The checked-in MCP task definition has no task role. However, the built-in domain
challenge handler reads a private S3 object on every request. Before a directory submission, verify
that the running task has scoped `s3:GetObject` access to `danny-banhmi-public/challenge/*`; add a
task role if it does not.

The reference RDS instance has automated backups disabled according to [`PLAN.md`](../PLAN.md); its
S3 dumps are therefore the recorded recovery source. Verify a dump's timestamp and restore it to a
separate database before relying on it. The project has not yet settled one production restore
procedure: [`PLAN.md`](../PLAN.md) records both direct restore and restore-to-new-database followed by
a rename swap. Choose, test, and record one procedure before a production restore.

See the detailed [AWS setup checklist](../deploy/aws/setup-checklist.md), the
[retrieval design](design/RAG.md), and the [jurisdiction playbook](design/jurisdictions/PLAYBOOK.md).
