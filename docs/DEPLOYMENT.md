# Deployment

banhmi is **three independently-hostable parts** connected only through the database. Host each on whatever
stack you like — only the per-part requirements below are fixed. (Local dev: [`DEVELOPMENT.md`](DEVELOPMENT.md).
banhmi's own reference stack is one example at the end — you are not bound to it.)

## The three parts

| # | Part | Role | Public? | Hard requirements |
|---|------|------|---------|-------------------|
| 1 | **Worker** | Crawl → extract → normalize → chunk → embed → **write** the corpus | No | `cmd/worker` (Go), DB access, a BGE-M3 embedder, Temporal + Redis, outbound internet |
| 2 | **Database** | The corpus + pipeline state — the only shared state | No | **PostgreSQL + pgvector** (HNSW) |
| 3 | **MCP server** | **Read** the corpus, serve evidence over MCP | Yes | `cmd/server` (HTTP) or `cmd/mcp` (stdio), DB read access, a query-time BGE-M3 embedder, HTTPS ingress |

**Data flow:** Worker → DB (write) · MCP → DB (read) · Agents → MCP (remote MCP over HTTPS). The worker
and MCP **never talk directly** — the DB is the only thing they share. So you can host all three together,
or spread them across machines/clouds, as long as both reach the DB.

## 1. Worker — any host that runs containers + reaches the DB

1. **What it does:** batch ingestion (`-run-all` or per-stage) on a schedule or one-shot; writes Bronze→Silver→Gold + embeddings. Not network-exposed.
2. **Embedder (for indexing):** pick one — (a) **local GPU** via the OVMS BGE-M3 container (fastest), (b) **in-process OpenVINO** (build `-tags openvino`), or (c) **offload bulk embedding to a GPU batch service** (banhmi supports Kaggle via `KAGGLE_API_TOKEN`) while chunking stays local.
3. **Also needs:** Temporal + Redis (orchestration) reachable, and outbound internet to crawl official sources.
4. **Where:** a GPU box, a VM, a CI runner, or a cloud worker — anywhere with the DB reachable. CPU-only works (slower embeds) or use the bulk-offload option.

## 2. Database — any PostgreSQL with pgvector

1. **Required:** PostgreSQL with the **pgvector** extension (HNSW index). Holds the `bronze`/`silver`/`gold`/`ingest`/`config` schemas + `chunk_embedding`.
2. **Temporal** needs its own Postgres DBs — same server or a separate one; never mix with the app schemas.
3. **Not required:** `pg_search`/ParadeDB — that's **eval-only**; production retrieval is vector-only, so any plain Postgres + pgvector suffices.
4. **Where:** self-hosted Postgres, or managed (AWS RDS, Cloud SQL, Neon, Supabase, …). Scale-to-zero managed Postgres is fine. **Lock network access** to the worker + MCP only, and require TLS.
5. **Tip:** co-locate the DB in the same region as the MCP server to keep query latency low.

## 3. MCP server — any container host with HTTPS

1. **What it does:** serves evidence over MCP (Streamable HTTP via `cmd/server`, or stdio via `cmd/mcp`). Read-only against the DB.
2. **Query embedder (required):** embeds the incoming query — (a) **in-process OpenVINO** (`-tags openvino`, single self-contained binary) or (b) an **OVMS BGE-M3 service/sidecar**. It must use the **same BGE-M3 model + tag** as the corpus embeddings.
3. **Ingress:** any HTTPS front — a managed cert on the host, a CDN/static host that proxies to it, or a load balancer. Scale-to-zero is fine (cold start is a few seconds).
4. **Auth:** public by default; set `BANHMI_MCP_API_KEY` to require a key.
5. **Where:** Cloud Run, Fly.io, Render, a VM behind a reverse proxy, Kubernetes — any container platform.

## Wiring (env vars)

Both worker and MCP point at the DB and embedder via env (secrets via env/file/Vault, never YAML):

| Variable | Used by | Purpose |
|----------|---------|---------|
| `BANHMI_DATABASE_HOST` / `PORT` / `USER` / `NAME` / `SSLMODE` | worker, MCP | DB connection (use `sslmode=require` for remote) |
| `BANHMI_DATABASE_PASSWORD` | worker, MCP | DB password (secret) |
| `BANHMI_EMBED_ENDPOINT` | worker, MCP | OVMS embedder URL (when not using the in-process build) |
| `BANHMI_MCP_API_KEY` | MCP | Optional — gate the public endpoint |
| `KAGGLE_API_TOKEN` | worker | Optional — offload bulk embedding to a Kaggle GPU |

## Deploy sequence

1. **Database:** provision Postgres + pgvector → `go run ./cmd/migrate` (schema) → `go run ./cmd/seed` (config vocabularies).
2. **Worker:** point it at the DB + embedder → build the corpus (`cmd/worker -run-all`). Confirm real rows (chunks + embeddings).
3. **MCP server:** deploy pointed at the DB with a query embedder → expose HTTPS. Verify `corpus_status` is `search_ready` and `search` returns hits.
4. **Connect agents** to the MCP URL.

## Reference deployment (banhmi's own — one example)

Split-cloud, scale-to-zero: **worker** local (GPU) → **DB** AWS RDS PostgreSQL (Singapore) → **MCP** GCP
Cloud Run (in-process OpenVINO) → public domain via Firebase Hosting. This is just one valid stack;
swap any part for your own (e.g. self-hosted Postgres + a VM MCP behind nginx). See [`PLAN.md`](../PLAN.md).
