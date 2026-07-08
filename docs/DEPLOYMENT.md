# Deployment

banhmi is **three independently-hostable parts** connected only through the database. Host each on
whatever stack you like — only the per-part requirements below are fixed. (Local dev:
[`DEVELOPMENT.md`](DEVELOPMENT.md). banhmi's own reference stack is one example at the end.)

## The three parts

| # | Part | Role | Public? | Hard requirements |
|---|------|------|---------|-------------------|
| 1 | **Pipeline** | Crawl, extract, normalize, chunk, embed — **write** the corpus | No | `cmd/pipeline` (Go), DB access, outbound internet, a bulk embedder service (Cloud Run L4 or Kaggle) |
| 2 | **Database** | The corpus + pipeline state — the only shared state | No | **PostgreSQL 17 + pgvector** (HNSW + `sparsevec`) |
| 3 | **MCP server** | **Read** the corpus, serve evidence over MCP | Yes | `cmd/server` (HTTP) or `cmd/mcp` (stdio), DB read access, in-process ONNX Qwen3-Embedding query embedder (`-tags onnx` build), HTTPS ingress |

**Data flow:** Pipeline --> DB (write) . MCP --> DB (read) . Agents --> MCP (remote MCP over HTTPS).
The pipeline and MCP **never talk directly** — the DB is the only thing they share. Host all three
together or spread them across machines/clouds.

## 1. Pipeline (write path) — any host that reaches the DB

1. **What it does:** batch ingestion (`-run-all` or per-stage) on a schedule or one-shot; writes Bronze, Silver, Gold + embeddings. Not network-exposed. No Temporal, no Redis.
2. **Stages:** `cmd/pipeline -run-all` runs discover, fetch, extract, normalize, index, embed, lexindex to convergence. Individual stages can be run with their own flags.
3. **Extraction:** **go-fitz** (MuPDF via purego, zero-Python) handles DOCX, HTML, DOC, and born-digital PDF in the same Go binary. OCR is a batch fallback (Document AI default, EasyOCR offline).
4. **Bulk embedding:** offloads to a GPU service — **Cloud Run L4** (`embed.engine=cloudrun`, `BANHMI_EMBEDDER_URL`) is the primary path; **Kaggle** (`embed.engine=kaggle`, `KAGGLE_API_TOKEN`) is the free fallback. **Never bulk-embed on the dev machine** (8 GB RAM).
5. **BM25 sparse vectors:** built by the `lexindex` stage (or standalone `cmd/lexindex`). Required for hybrid retrieval.
6. **Where:** a VM, a CI runner, a Cloud Run CPU Job, or local — anywhere CPU-only with DB access. No GPU needed locally; embedding offloads to the GPU service.

## 2. Database — any PostgreSQL 17 with pgvector

1. **Required:** PostgreSQL 17 with **pgvector** (HNSW index + `sparsevec` type). Holds `bronze`/`silver`/`gold`/`ingest`/`config` schemas + `chunk_embedding`.
2. **Not required:** `pg_search`/ParadeDB — retrieval is **hybrid inside plain pgvector** (dense vectors + BM25 `sparsevec`), so any Postgres + pgvector works, including managed RDS.
3. **Where:** self-hosted, or managed (AWS RDS, Cloud SQL, Neon, Supabase, ...). Scale-to-zero managed Postgres is fine. **Lock network access** to the pipeline + MCP only, and require TLS.
4. **Multi-jurisdiction:** one **database per country** (e.g. `banhmi`, `laksa`, `rendang`) — same server until load says otherwise. The database is the jurisdiction boundary; no cross-country data.
5. **Tip:** co-locate the DB in the same region as the MCP server for low query latency.

## 3. MCP server (read path) — any container host with HTTPS

1. **What it does:** serves evidence over MCP (Streamable HTTP via `cmd/server`, or stdio via `cmd/mcp`). Read-only against the DB.
2. **Query embedder (required):** embeds the incoming query at search time. Build with `-tags onnx` for **in-process ONNX Runtime** with **Qwen3-Embedding-0.6B FP16** (1024 dims, 32K context). The query embedder **must match the index model** — same Qwen3-Embedding weights.
3. **Ingress:** any HTTPS front — a managed cert, a CDN, or a load balancer. Scale-to-zero is fine (cold start is a few seconds).
4. **Auth:** public by default; set `BANHMI_MCP_API_KEY` to require a key.
5. **Where:** Cloud Run, ECS, Fly.io, Render, a VM behind a reverse proxy, Kubernetes — any container platform.

## Wiring (env vars)

Both pipeline and MCP point at the DB and embedder via env (secrets via env/file/Vault, never YAML):

| Variable | Used by | Purpose |
|----------|---------|---------|
| `BANHMI_JURISDICTION` | pipeline, MCP | Country served (`vn` default, `my`, `id`, ...) — selects sources, parser, scope, MCP brief |
| `BANHMI_DATABASE_HOST` / `PORT` / `USER` / `NAME` / `SSLMODE` | pipeline, MCP | DB connection (`NAME` = the jurisdiction's DB; use `sslmode=require` for remote) |
| `BANHMI_DATABASE_PASSWORD` | pipeline, MCP | DB password (secret) |
| `BANHMI_EMBED_ENGINE` | pipeline | Bulk embed engine: `cloudrun` (primary), `kaggle` (free fallback), `local` |
| `BANHMI_EMBEDDER_URL` | pipeline | Cloud Run L4 embedder service URL (when `embed.engine=cloudrun`) |
| `GOOGLE_APPLICATION_CREDENTIALS` | pipeline | SA key for Cloud Run embedder + Document AI auth (off-GCP only; on-GCP use metadata server) |
| `KAGGLE_API_TOKEN` | pipeline | Kaggle API auth (when `embed.engine=kaggle`) |
| `BANHMI_MCP_API_KEY` | MCP | Optional — gate the public endpoint |
| `BANHMI_TRUST_PROXY` | MCP | Set `true` behind a reverse proxy (Cloud Run, ALB) to trust `X-Forwarded-For` for rate limiting |

## Deploy sequence (per jurisdiction)

1. **Database:** create the jurisdiction's DB on Postgres + pgvector, then `go run ./cmd/migrate` (schema), then `go run ./cmd/seed` (config vocabularies).
2. **Pipeline:** point it at that DB + embedder with `BANHMI_JURISDICTION=<cc>`, then build the corpus (`go run ./cmd/pipeline -run-all`). Confirm real rows (chunks + embeddings + sparse vectors).
3. **MCP server:** deploy with the same DB and `BANHMI_JURISDICTION`, build with `-tags onnx` for the query embedder, expose over HTTPS. Verify `corpus_status` returns `search_ready` and `search` returns hits.
4. **Connect agents** to the MCP URL.

Adding a country = repeating this sequence with its own DB, service, and domain off the **same image** —
see the [jurisdiction playbook](design/jurisdictions/PLAYBOOK.md).

## IAM and service accounts (banhmi reference deployment)

Both clouds use **purpose-specific identities** with least privilege. Pattern: **resource-level IAM
bindings** for service-to-service calls (no key files), **key files only for off-cloud / local testing**.

### GCP service accounts

| Service account | Purpose | Roles |
|-----------------|---------|-------|
| `banhmi-pipeline-dev` | Local dev pipeline calling GCP services | `run.invoker` (on embedder service) + `documentai.apiUser` + `storage.objectAdmin` (OCR GCS cache) |
| Default Compute SA | Cloud Run services calling other Cloud Run services | `run.invoker` granted via IAM binding on the embedder service (metadata server auth, no key) |

### AWS IAM roles

| Role | Purpose | Permissions |
|------|---------|-------------|
| `ecsTaskExecutionRole` | ECS agent: pull images, inject secrets, write logs | `AmazonECSTaskExecutionRolePolicy` (managed) + `secretsmanager:GetSecretValue` scoped to `banhmi-db-url-*` |
| `ecsInstanceRole` | EC2 host: register with ECS cluster | `AmazonEC2ContainerServiceforEC2Role` (managed) |
| *(no task role)* | MCP containers make no AWS SDK calls at runtime — RDS access is network-level (SCRAM + TLS, same VPC) | — |

### Rules

- **Key files must be gitignored.** Store GCP SA keys in `.claude/` or another gitignored path. Pattern `*-sa.json` is in `.gitignore`.
- **Never commit credentials** — not in code, YAML, env files, or docs.
- **Cloud-to-cloud: IAM bindings, no keys.** Cloud Run → Cloud Run uses metadata server + `run.invoker` binding. ECS → RDS uses VPC network + DB password (injected from Secrets Manager).
- **Off-cloud / local: key file + env var.** `GOOGLE_APPLICATION_CREDENTIALS` for GCP. AWS credentials via `~/.aws/credentials` or env vars. Scope to minimum roles.
- **Separate dev and prod identities.** `banhmi-pipeline-dev` is for local testing only; deployed services use their platform's default identity (compute SA, instance role).

## Reference deployment (banhmi's own — one example)

Split-cloud, scale-to-zero, **repeated per country** (live: VN `banhmi.danny.vn`, MY `laksa.danny.vn`,
ID `rendang.danny.vn`; proposed: SG, TH):

### Write path (pipeline)

- **Local pipeline** (CPU-only, `cmd/pipeline -run-all`) writes corpus over TLS to RDS.
- **Bulk embedding** offloads to **Cloud Run L4 GPU** (`banhmi-embedder`, scale-to-zero, `--concurrency=1`, `--max-instances=2`, ~$1/hr active). Kaggle is the free fallback.
- **OCR** offloads to **GCP Document AI** Enterprise OCR (GCS-cached). EasyOCR is the offline fallback.
- Alternatively, the pipeline runs as a **Cloud Run CPU Job** (free tier) for scheduled/unattended runs.

### Database

- **AWS RDS PostgreSQL 17 + pgvector** (Singapore `ap-southeast-1`), one database per country on the same instance.
- Dense vectors (Qwen3-Embedding 1024d) + BM25 sparse vectors (`sparsevec`) — single datastore, no separate search engine.
- TLS required (`rds.force_ssl=1`), password-gated.

### Read path (MCP server)

- **Current (production):** GCP Cloud Run (`asia-southeast1`), one scale-to-zero service per country. Go MCP binary built `-tags onnx` with **in-process ONNX Qwen3-Embedding-0.6B FP16** query embedder. Public via **Firebase Hosting** (`banhmi.danny.vn/mcp`, `laksa.danny.vn/mcp`, `rendang.danny.vn/mcp`).
- **v0.3.0 target:** AWS **CloudFront + ECS on EC2 ARM64 Graviton** (same VPC as RDS), in-process ONNX Qwen3-Embedding query embedder. Same-VPC DB access eliminates cross-cloud latency.

This is one valid stack; swap any part for your own (e.g. self-hosted Postgres + a VM MCP behind nginx).
See [`PLAN.md`](../PLAN.md).
