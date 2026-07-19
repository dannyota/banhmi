# Deployment

banhmi is **three independently-hostable parts** connected only through the database. Host each on
whatever stack you like — only the per-part requirements below are fixed. (Local dev:
[`DEVELOPMENT.md`](DEVELOPMENT.md). banhmi's own reference stack is one example at the end.)

## The three parts

| # | Part | Role | Public? | Hard requirements |
|---|------|------|---------|-------------------|
| 1 | **Pipeline** | Crawl, extract, normalize, chunk, embed — **write** the corpus | No | `cmd/pipeline` (Go), DB access, outbound internet, the Kaggle bulk embedder (dataset I/O) |
| 2 | **Database** | The corpus + pipeline state — the only shared state | No | **PostgreSQL 17 + pgvector** (HNSW + `sparsevec`) |
| 3 | **MCP server** | **Read** the corpus, serve evidence over MCP | Yes | `cmd/server` (HTTP) or `cmd/mcp` (stdio), DB read access, a Qwen3-Embedding query embedder — in-process (`-tags onnx`) or the `cmd/embedder` service — HTTPS ingress |

**Data flow:** Pipeline → DB (write) · MCP → DB (read) · Agents → MCP (remote MCP over HTTPS).
The pipeline and MCP **never talk directly** — the DB is the only thing they share. Host all three
together or spread them across machines/clouds.

## 1. Pipeline (write path) — any host that reaches the DB

1. **What it does:** batch ingestion (`-run-all` or per-stage) on a schedule or one-shot; writes Bronze, Silver, Gold + embeddings. Not network-exposed. No Temporal, no Redis.
2. **Stages:** `cmd/pipeline -run-all` runs discover, fetch, extract, normalize, index, embed, lexindex to convergence. Individual stages can be run with their own flags.
3. **Extraction:** **go-fitz** (MuPDF via purego, zero-Python) handles DOCX, HTML, DOC, and born-digital PDF in the same Go binary. OCR is a batch fallback (Vision OCR default, EasyOCR offline).
4. **Bulk embedding:** offloads to **Kaggle T4 GPU** (`embed.engine=kaggle`, `KAGGLE_API_TOKEN`; free, fresh GPU per run) via **Kaggle dataset I/O** — pipeline uploads chunk texts as a Kaggle dataset, the kernel embeds, pipeline downloads the vectors; kernel + input dataset auto-delete on success. No GCS involved. **Never bulk-embed on the dev machine** (8 GB RAM).
5. **BM25 sparse vectors:** built by the `lexindex` stage (or standalone `cmd/lexindex`). Required for hybrid retrieval.
6. **Where:** anywhere CPU-only with DB access — a VM, a CI runner, or local. Some sources **geo-lock** (VN blocks non-VN IPs), so banhmi's reference stack runs **self-terminating EC2 per country** for in-country egress IPs (see the reference deployment below). No GPU needed; embedding offloads to Kaggle.

## 2. Database — any PostgreSQL 17 with pgvector

1. **Required:** PostgreSQL 17 with **pgvector** (HNSW index + `sparsevec` type). Holds `bronze`/`silver`/`gold`/`ingest`/`config` schemas + `chunk_embedding`.
2. **Not required:** `pg_search`/ParadeDB — retrieval is **hybrid inside plain pgvector** (dense vectors + BM25 `sparsevec`), so any Postgres + pgvector works, including managed RDS.
3. **Where:** self-hosted, or managed (AWS RDS, Cloud SQL, Neon, Supabase, ...). Scale-to-zero managed Postgres is fine. **Lock network access** to the pipeline + MCP only, and require TLS.
4. **Multi-jurisdiction:** one **database per country** (e.g. `banhmi`, `laksa`) — same server until load says otherwise. The database is the jurisdiction boundary; no cross-country data.
5. **Tip:** co-locate the DB in the same region as the MCP server for low query latency.

## 3. MCP server (read path) — any container host with HTTPS

1. **What it does:** serves evidence over MCP (Streamable HTTP via `cmd/server`, or stdio via `cmd/mcp`). Read-only against the DB.
2. **Query embedder (required):** embeds the incoming query at search time, two supported modes — the model is always **Qwen3-Embedding-0.6B FP16** (1024 dims) and **must match the index model**:
   - **In-process** — build with `-tags onnx` (ONNX Runtime in the server binary; simplest single-container deploy). Set `BANHMI_EMBED_QUERY=onnx`.
   - **Split service** — run `cmd/embedder` (OpenAI-compatible `POST /embeddings`, `-tags onnx`, ~2.3 GB RSS) and point the MCP server at it: `BANHMI_EMBED_QUERY` unset, `BANHMI_EMBED_ENDPOINT=<url>`, shared `BANHMI_EMBED_TOKEN`. The MCP image needs no model/ORT (small, fast restarts); the server probe-verifies dims + model tag at startup and refuses a mismatched embedder. Embedder down ⇒ `search` returns an explicit retryable error (no silent degraded mode).
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
| `BANHMI_EMBED_ENGINE` | pipeline | Bulk embed engine: `kaggle` (dataset I/O), `local`, `auto` (= kaggle when the token is set, else local) |
| `BANHMI_S3_DATA_BUCKET` | pipeline | Per-region S3 bucket for the fetched-file cache + OCR text mirror (`danny-banhmi-data-{vn,my,id}`) |
| `GOOGLE_APPLICATION_CREDENTIALS` | pipeline | SA key for Vision OCR auth (off-GCP only) |
| `KAGGLE_API_TOKEN` | pipeline | Kaggle API auth (when `embed.engine=kaggle`) |
| `BANHMI_EMBED_QUERY` | MCP | Query-embed mode: `onnx` = in-process; unset = HTTP client to `BANHMI_EMBED_ENDPOINT` |
| `BANHMI_EMBED_ENDPOINT` | MCP | URL of the `cmd/embedder` service (split mode) |
| `BANHMI_EMBED_TOKEN` | MCP, embedder | Shared Bearer token between MCP and the embedder service (secret) |
| `BANHMI_MCP_API_KEY` | MCP | Optional — gate the public endpoint |
| `BANHMI_TRUST_PROXY` | MCP | Set `true` behind a reverse proxy (Cloud Run, ALB) to trust `X-Forwarded-For` for rate limiting |

## Deploy sequence (per jurisdiction)

1. **Database:** create the jurisdiction's DB on Postgres + pgvector, then `go run ./cmd/migrate` (schema), then `go run ./cmd/seed` (config vocabularies).
2. **Pipeline:** point it at that DB + embedder with `BANHMI_JURISDICTION=<cc>`, then build the corpus (`go run ./cmd/pipeline -run-all`). Confirm real rows (chunks + embeddings + sparse vectors).
3. **MCP server:** deploy with the same DB and `BANHMI_JURISDICTION`, with a query embedder in either mode (in-process `-tags onnx`, or `cmd/embedder` + `BANHMI_EMBED_ENDPOINT`), expose over HTTPS. Verify `corpus_status` returns `search_ready` and `search` returns hits.
4. **Connect agents** to the MCP URL.

Adding a country = repeating this sequence with its own DB, service, and domain off the **same image** —
see the [jurisdiction playbook](design/jurisdictions/PLAYBOOK.md).

## IAM and service accounts (banhmi reference deployment)

Both clouds use **purpose-specific identities** with least privilege. Pattern: **resource-level IAM
bindings** for service-to-service calls (no key files), **key files only for off-cloud / local testing**.

### GCP service accounts

| Service account | Purpose | Roles |
|-----------------|---------|-------|
| `banhmi-pipeline-dev` | Local dev pipeline calling Vision OCR | no roles — Vision needs only the enabled API + valid credentials |

### AWS IAM roles

| Role | Purpose | Permissions |
|------|---------|-------------|
| `ecsTaskExecutionRole` | ECS agent: pull images, inject secrets, write logs | `AmazonECSTaskExecutionRolePolicy` (managed) + `secretsmanager:GetSecretValue` scoped to `banhmi-db-url-*` |
| `ecsInstanceRole` | EC2 host: register with ECS cluster | `AmazonEC2ContainerServiceforEC2Role` (managed) |
| `banhmi-pipeline-ec2` (v0.3.0) | Write-path EC2: file cache, secrets, image pull | scoped S3 RW (data buckets) + `ssm:GetParameter` (`/banhmi/*`) + ECR pull + CloudWatch Logs |
| *(no task role)* | MCP containers make no AWS SDK calls at runtime — RDS access is network-level (SCRAM + TLS, same VPC) | — |

### Rules

- **Key files must be gitignored.** Store GCP SA keys in `.claude/` or another gitignored path. Pattern `*-sa.json` is in `.gitignore`.
- **Never commit credentials** — not in code, YAML, env files, or docs.
- **Cloud-to-cloud: IAM roles/bindings, no keys.** EC2/ECS use instance roles; ECS → RDS uses VPC network + DB password (injected from Secrets Manager).
- **Off-cloud / local: key file + env var.** `GOOGLE_APPLICATION_CREDENTIALS` for GCP. AWS credentials via `~/.aws/credentials` or env vars. Scope to minimum roles.
- **Separate dev and prod identities.** `banhmi-pipeline-dev` is for local testing only; deployed services use their platform's default identity (compute SA, instance role).

## Reference deployment (banhmi's own — one example)

Split-cloud, **repeated per country** (live: VN `banhmi.danny.vn`, MY `laksa.danny.vn`,
ID `rendang.danny.vn`; proposed: SG, TH):

### Write path (pipeline)

- **Local pipeline runs now** (dev machine egresses from a VN IP), dumped/restored to RDS. The
  validated **self-terminating EC2 per country, in-country IP** infra is parked for future refresh
  runs (CPU-only, `cmd/pipeline -run-all`): VN **Hanoi Local Zone** `ap-southeast-1-han-1a` (VN
  sources geo-lock non-VN IPs), MY `ap-southeast-5`. Writes corpus over TLS to RDS.
- **File cache** in per-region **S3 buckets** (`danny-banhmi-data-{vn,my,id}`); pipeline image via
  **CodeBuild → ECR** (replicated per region); write-path secrets in **SSM Parameter Store** (`/banhmi/*`).
- **Bulk embedding** offloads to **Kaggle T4 GPU** via dataset I/O (input uploaded as a Kaggle dataset, vectors downloaded from the kernel; free, fresh GPU per run).
- **OCR** offloads to **Google Vision OCR** (`images:annotate`, page-per-request; file-first cache with S3 mirror). EasyOCR is the offline fallback.

### Database

- **AWS RDS PostgreSQL 17 + pgvector** (Singapore `ap-southeast-1`), one database per country on the same instance.
- Dense vectors (Qwen3-Embedding 1024d) + BM25 sparse vectors (`sparsevec`) — single datastore, no separate search engine.
- TLS required (`rds.force_ssl=1`), password-gated.

### Read path (MCP server)

- **Production:** AWS — CloudFront (ACM TLS, per-country distribution) → ECS on EC2 t4g.large ARM64 Graviton; RDS reachable only from the origin SG. Public: `<codename>.danny.vn/mcp` for all six jurisdictions.
- **v0.4.0 split (validated locally, prod cutover pending):** two ECS services on that host — slim `cmd/server` MCP container (no model, `Containerfile.ecs.server`) + `cmd/embedder` on loopback `127.0.0.1:8089` (`Containerfile.ecs.embedder`, model baked). Until cutover, prod runs the pre-split single container (`Containerfile.ecs.onnx`, in-process embedder — kept as the rollback image).

This is one valid stack; swap any part for your own (e.g. self-hosted Postgres + a VM MCP behind nginx).
See [`PLAN.md`](../PLAN.md).
