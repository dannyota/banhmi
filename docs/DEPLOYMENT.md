# Deployment

banhmi is **three independently-hostable parts** connected only through the database. Host each on
whatever stack you like — only the per-part requirements below are fixed. (Local dev:
[`DEVELOPMENT.md`](DEVELOPMENT.md). banhmi's own reference stack is one example at the end.)

## The three parts

| # | Part | Role | Public? | Hard requirements |
|---|------|------|---------|-------------------|
| 1 | **Pipeline** | Crawl, extract, normalize, chunk, embed — **write** the corpus | No | `cmd/pipeline` (Go), DB access, outbound internet, the Kaggle bulk embedder (dataset I/O) |
| 2 | **Database** | The corpus + pipeline state — the only shared state | No | **PostgreSQL 17 + pgvector** (HNSW + `sparsevec`) |
| 3 | **MCP server** | **Read** the corpus, serve evidence over MCP | Yes | `cmd/server` (HTTP) or `cmd/mcp` (stdio), DB read access, in-process ONNX Qwen3-Embedding query embedder (`-tags onnx` build), HTTPS ingress |

**Data flow:** Pipeline → DB (write) · MCP → DB (read) · Agents → MCP (remote MCP over HTTPS).
The pipeline and MCP **never talk directly** — the DB is the only thing they share. Host all three
together or spread them across machines/clouds.

## 1. Pipeline (write path) — any host that reaches the DB

1. **What it does:** batch ingestion (`-run-all` or per-stage) on a schedule or one-shot; writes Bronze, Silver, Gold + embeddings. Not network-exposed. No Temporal, no Redis.
2. **Stages:** `cmd/pipeline -run-all` runs discover, fetch, extract, normalize, index, embed, lexindex to convergence. Individual stages can be run with their own flags.
3. **Extraction:** **go-fitz** (MuPDF via purego, zero-Python) handles DOCX, HTML, DOC, and born-digital PDF in the same Go binary. OCR is a batch fallback (Document AI default, EasyOCR offline).
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
| `BANHMI_EMBED_ENGINE` | pipeline | Bulk embed engine: `kaggle` (dataset I/O), `local`, `auto` (= kaggle when the token is set, else local) |
| `BANHMI_GCS_DATA_BUCKET` | pipeline | GCS bucket for the fetched-file cache (default `danny-banhmi-data`) — retired in v0.3.0 when the file cache moves to per-region S3 (`BANHMI_S3_DATA_BUCKET`) |
| `BANHMI_DOCAI_BUCKET` | pipeline | GCS bucket for the Document AI OCR cache (separate from the data bucket) |
| `GOOGLE_APPLICATION_CREDENTIALS` | pipeline | SA key for GCS + Document AI auth (off-GCP only; on-GCP use metadata server) |
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
| `banhmi-pipeline-dev` | Local dev pipeline calling GCP services | `documentai.apiUser` + `storage.objectAdmin` (Document AI cache bucket + data bucket until the v0.3.0 S3 move) |
| Default Compute SA | Cloud Run read-path services (until the v0.3.0 cutover) | metadata server auth, no key files |

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

Split-cloud, scale-to-zero, **repeated per country** (live: VN `banhmi.danny.vn`, MY `laksa.danny.vn`;
ID dormant — decommissioned 2026-07-11; proposed: SG, TH):

### Write path (pipeline)

- **Local pipeline runs now** (dev machine egresses from a VN IP), dumped/restored to RDS. The
  validated **self-terminating EC2 per country, in-country IP** infra is parked for future refresh
  runs (CPU-only, `cmd/pipeline -run-all`): VN **Hanoi Local Zone** `ap-southeast-1-han-1a` (VN
  sources geo-lock non-VN IPs), MY `ap-southeast-5`. Writes corpus over TLS to RDS.
- **File cache** in per-region **S3 buckets** (`danny-banhmi-data-{vn,my}`); pipeline image via
  **CodeBuild → ECR** (replicated per region); write-path secrets in **SSM Parameter Store** (`/banhmi/*`).
- **Bulk embedding** offloads to **Kaggle T4 GPU** via dataset I/O (input uploaded as a Kaggle dataset, vectors downloaded from the kernel; free, fresh GPU per run).
- **OCR** offloads to **GCP Document AI** Enterprise OCR (GCS-cached). EasyOCR is the offline fallback.

### Database

- **AWS RDS PostgreSQL 17 + pgvector** (Singapore `ap-southeast-1`), one database per country on the same instance.
- Dense vectors (Qwen3-Embedding 1024d) + BM25 sparse vectors (`sparsevec`) — single datastore, no separate search engine.
- TLS required (`rds.force_ssl=1`), password-gated.

### Read path (MCP server)

- **Current (production):** AWS — CloudFront (ACM TLS, per-country distribution) → ECS on EC2 ARM64 Graviton, Go MCP binary built `-tags onnx` with **in-process ONNX Qwen3-Embedding-0.6B FP16** query embedder; RDS reachable only from the origin SG. Public: `banhmi.danny.vn/mcp`, `laksa.danny.vn/mcp`.
- **v0.3.0 target:** AWS **CloudFront + ECS on EC2 ARM64 Graviton** (same VPC as RDS), in-process ONNX Qwen3-Embedding query embedder. Same-VPC DB access eliminates cross-cloud latency.

This is one valid stack; swap any part for your own (e.g. self-hosted Postgres + a VM MCP behind nginx).
See [`PLAN.md`](../PLAN.md).
