# banhmi plan

Living roadmap and progress tracker. Architecture detail in [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md);
conventions and the canonical agent guide in [`CLAUDE.md`](CLAUDE.md); the multi-country model in
[`docs/design/jurisdictions/`](docs/design/jurisdictions/). Last updated: 2026-07-10.

## Vision

A self-hostable, **multi-country** platform for Southeast-Asian **banking & financial regulation** and
**cross-cutting technology law**: one codebase that crawls each country's official sources, builds a
clean, citable corpus in that country's binding legal language, and **serves it as evidence over MCP** — exact native citations
(Điều/Khoản, Section/Subsection, Pasal/ayat, มาตรา), validity, relations, provenance, and explicit gaps.

- **One codebase → one corpus per country** — separate database, MCP service, and domain per
  jurisdiction ([playbook](docs/design/jurisdictions/PLAYBOOK.md)). Never a branch or fork.
- **The data is the product; the user brings the model.** No built-in answer LLM — hosted agents
  (Claude.ai, ChatGPT, Gemini, Grok) connect over MCP and reason over the evidence themselves. Good
  data + any decent model = good answers; bad data = *confidently wrong legal answers*. INPUT before
  OUTPUT, always.

> **Status convention:** "coded" = code written + unit/integration tests; "validated" = checked on real
> documents. Never report one as the other.

## Jurisdictions

| # | Country | Codename | Endpoint | Status | Design |
|---|---------|----------|----------|--------|--------|
| 1 | 🇻🇳 Vietnam | `banhmi` | banhmi.danny.vn/mcp | **LIVE** (2026-06-01) | [SOURCES](docs/design/SOURCES.md) (reference jurisdiction) |
| 2 | 🇲🇾 Malaysia | `laksa` | laksa.danny.vn/mcp | **LIVE** (2026-06-22) | [MALAYSIA](docs/design/jurisdictions/MALAYSIA.md) |
| 3 | 🇮🇩 Indonesia | `rendang` | rendang.danny.vn/mcp | **LIVE** (2026-07-06) | [INDONESIA](docs/design/jurisdictions/INDONESIA.md) |
| 4 | 🇸🇬 Singapore | `kaya`* | kaya.danny.vn* | PROPOSED | [SINGAPORE](docs/design/jurisdictions/SINGAPORE.md) |
| 5 | 🇹🇭 Thailand | `tomyum`* | tomyum.danny.vn* | PROPOSED | [THAILAND](docs/design/jurisdictions/THAILAND.md) |

\* codename/domain proposed, **pending maintainer sign-off**. Recommended **build order: SG → TH**
— SG is the cheapest build (English, MY citation family, SSO HTML statute trees); TH last because it
carries the heaviest language work (Thai word segmentation for the lexical arm, Buddhist-Era dates,
Thai numerals). Final order is the maintainer's call.

## Deployment shape (current prod + v0.3.0 target)

- **Write path — self-terminating AWS EC2 per country, in-country IP**, one jurisdiction per run
  (`BANHMI_JURISDICTION`), all `m7i.large` (offering verified in all three locations, 2026-07-10):
  - **VN:** **Hanoi Local Zone** `ap-southeast-1-han-1a` — VN IP. **Geo-lock:** `vbpl.vn`,
    `sbv.hanoi.gov.vn`, `vbpl-bientap-gateway.moj.gov.vn` block non-VN IPs — connections time out
    from every cloud region without a VN presence.
  - **MY:** `ap-southeast-5` (Malaysia).
  - **ID:** `ap-southeast-3` (Jakarta).
  - File cache in **per-region S3 buckets** (`danny-banhmi-data-{vn,my,id}`), seeded once from GCS.
  - Pipeline image in **ECR**, built by **AWS CodeBuild** (`ap-southeast-1`), **replicated** to
    `-3`/`-5`; base images via **ECR pull-through cache from ECR Public** (no Docker Hub creds).
  - Secrets (RDS password, GCP SA key, Kaggle token) in **SSM Parameter Store**.
  - CPU-only: go-fitz extraction (~1ms/page), Document AI OCR (GCS-cached, async). No GPU.
    **Pipeline runner:** `cmd/pipeline` (no Temporal); calls activity methods directly.
  - Bulk embedding offloads to **Kaggle T4 GPU** (`embed.engine=kaggle`, free). Each Kaggle run
    gets a fresh GPU — no memory fragmentation.
- **DB — AWS RDS PostgreSQL 17 + pgvector/HNSW** (`ap-southeast-1`), **one database per country** on
  one instance (`banhmi`, `laksa`, `rendang`). TLS-required, password-gated.
- **Read path (current prod) — GCP Cloud Run** (`asia-southeast1`), one scale-to-zero service per
  country, in-process BGE-M3 query embedder (OpenVINO INT8). Firebase Hosting domains.
  **v0.3.0 migrates to AWS** (CloudFront + ECS on EC2) — see below.
- **Read path (v0.3.0) — AWS** (`ap-southeast-1`), CloudFront + ECS on EC2 (ARM64 Graviton),
  in-process Qwen3-Embedding ONNX FP16 query embedder. Always-on, same VPC as RDS.
- **Retrieval — hybrid** (single datastore): dense vectors + **BM25 sparse vectors** (pgvector
  `sparsevec`, `cmd/lexindex`) fused with RRF + a deterministic query router. No ParadeDB/`pg_search`.
- **GCP dependency (remaining):** Document AI OCR + its GCS cache bucket (`danny-banhmi-docai`,
  `BANHMI_DOCAI_BUCKET`) + current MCP read path (Cloud Run, until v0.3.0). Kaggle I/O is **Kaggle
  datasets, not GCS**. No GCP builds; no other GCP compute on the write path.

## Current state (live `corpus_status`)

**🇻🇳 VN (banhmi) `v0.1.0-20260704` (prod):** 1,608 docs total · **712 indexed** · **47,504 chunks** ·
**100% embedded** · 8,859 confirmed relation edges · `search_ready`. **Hybrid retrieval live**.
`bm25_score` per hit live. Mojibake remediated (doc 200 clean, 2026-07-04). Open gaps (via
`quality_gaps`): 964 unresolved relation targets (deliberate one-level crawl boundary), 83 needs-review
text docs, 27 indexed docs without binding text (badged), 4 docs without current validity. 887
relation-context docs deliberately unindexed. *(Local has a newer corpus: 723 indexed / 47,587 chunks
after the corpus gap fix + re-embed on Kaggle — awaiting next prod sync.)*
**Eval (54-case golden set, 2026-07-05):** recall 75.9%, MRR 60.0%, current-law 100%, abstention 100%.

**🇲🇾 MY (laksa) `v0.1.0-20260704` (prod):** 63 docs · 8,425 chunks · **100% embedded** ·
**100% sparse** · 62 in-force + 1 expired · `search_ready`. **Hybrid retrieval live**. `bm25_score`
per hit live. Remaining: 1,000 P.U. relation stubs (unresolved), 8 needs_review docs (agclom PDFs,
null markdown), layout-aware Section titles.
**Eval (51-case golden set, 2026-07-05):** recall 85.4%, MRR 73.6%, current-law 100%, abstention 98.0%.

**🇮🇩 ID (rendang) `v0.1.0-20260706`:** LIVE (2026-07-06). Sources: `bpk`
(peraturan.bpk.go.id; UU/PP/POJK/SEOJK) + `bi` (jdih.bi.go.id; PBI/PADG). `ParseIndonesianUU`
validated on UU 27/2022 (Pasal 1–76, 0 gaps). See [INDONESIA](docs/design/jurisdictions/INDONESIA.md).

## Roadmap

### v0.3.0 — AWS read path, Qwen3-Embedding, ONNX everywhere

**Status: Phase A DONE, Phase B in progress (step 16-iv).** Three changes:
1. **Read path migrates from GCP Cloud Run to AWS** — CloudFront + ECS on EC2 (ARM64 Graviton).
   Eliminates cross-cloud latency (GCP→AWS), cold starts, and Firebase Hosting dependency.
2. **Embedder switches from BGE-M3 to Qwen3-Embedding-0.6B** (`onnx-community/Qwen3-Embedding-0.6B-ONNX`,
   **FP16**, 1.2 GB). Same 1024 dims, 32K context (vs 8K). Full re-embed required.
3. **ONNX Runtime everywhere** (query + bulk). OpenVINO removed.

**Temporal removed** — `cmd/pipeline` calls activity methods directly (shipped 2026-07-06).

**Architecture:**
```
READ PATH — AWS (ap-southeast-1), always-on:
  User → CloudFront (3 distributions, ACM TLS, *.danny.vn)
           ├─ banhmi.danny.vn  → origin :8081
           ├─ laksa.danny.vn   → origin :8082
           ├─ rendang.danny.vn → origin :8083
           │
         EC2 t4g.medium (2 vCPU / 4 GB, Graviton ARM64, Elastic IP)
         ECS cluster (1 instance, host networking)
           ├─ banhmi-mcp  :8081  (BANHMI_JURISDICTION=vn)
           ├─ laksa-mcp   :8082  (BANHMI_JURISDICTION=my)
           ├─ rendang-mcp :8083  (BANHMI_JURISDICTION=id)
           │
         RDS PostgreSQL 17 + pgvector (ap-southeast-1)

  All 3 containers from one ARM64 image. In-process Qwen3-Embedding
  ONNX FP16 query embedder. Model uses external data format
  (model_fp16.onnx + model_fp16.onnx_data, 1.2 GB total); ORT
  mmap's the data file — 3 containers share the same physical
  pages via page cache. Budget ~1.2 GB shared + ~100 MB per
  container for Go runtime + inference spike. 4 GB suffices.

WRITE PATH — AWS EC2 per country (in-country IP) + Kaggle GPU:
  Pipeline (CPU) — self-terminating EC2 m7i.large per run:
    VN: ap-southeast-1-han-1a (Hanoi Local Zone) — VN IP
    MY: ap-southeast-5 (Malaysia)
    ID: ap-southeast-3 (Jakarta)
    cmd/pipeline -run-all (per jurisdiction)
    discover → fetch → extract → normalize → index → lexindex
    All CPU: go-fitz extraction (~1ms/page), Document AI OCR
    (GCS-cached, async). No GPU, no ONNX model baked in.
    File cache: per-region S3 (danny-banhmi-data-{vn,my,id}).
    Image: CodeBuild → ECR (ap-southeast-1) → replicated to -3/-5.
    Base images: ECR pull-through cache (ECR Public).
    Secrets: SSM Parameter Store (ap-southeast-1; read cross-region).

  Embedder (GPU) — Kaggle T4 (free):
    kernel_embed.py on Qwen3-Embedding ONNX FP16.
    Model dataset: danhsoftware/qwen3-embedding-06b-onnx-fp16.
    Dataset I/O: input JSONL uploaded as a Kaggle dataset →
    embed on T4 → kernel output vectors downloaded. No GCS.
    Each run gets a fresh GPU — no memory fragmentation.
```

**Key design decisions:**
- **Read path moves to AWS.** Cloud Run cold start (6–8s) and cross-cloud latency (GCP→AWS ~10–20ms)
  worse than always-on in the same VPC as RDS (<1ms). ~$25/mo for zero cold starts.
- **3 CloudFront distributions, custom origin ports.** Each domain routes to a different port on
  `origin.danny.vn` (A record → Elastic IP). Custom origin header (`X-Origin-Verify: <secret>`).
  Origin-response timeout 60s (default 30s too short for SSE streams).
- **ARM64 (Graviton).** ~20% better price/performance.
- **Qwen3-Embedding-0.6B replaces BGE-M3.** 0.6B params, 1024 dims, 32K context (4× BGE-M3).
  FP16 over INT8: ONNX INT8 dynamic quantization has no CUDA kernels. FP16 external data format;
  ORT mmap's weights so 3 ECS containers share physical pages.
- **Kaggle-only embedding.** Free T4 GPU, fresh GPU per run (no memory fragmentation),
  **dataset-based I/O** — input texts uploaded as a Kaggle dataset, vectors downloaded from the
  kernel, no GCS in the loop. Simpler than managed GPU services.
- **Write path in-country ×3.** One self-terminating EC2 pattern for VN/MY/ID; ID moves from
  Singapore to Jakarta (`ap-southeast-3`). In-country IP defeats geo-locks (VN today, others may
  follow) and keeps crawls in-region and polite.
- **Per-region S3 data buckets.** The fetch cache lives in the same region as its pipeline — no
  cross-region chatter. Keys are flat (`files/{name}`, no jurisdiction prefix), so seeding is a
  full mirror into each bucket (~$0.40/mo storage) — cheaper than building DB-derived per-country
  file lists. Seeded by a one-off **AWS Lambda** (GCS → S3, egress paid once ~$0.60), then
  server-side S3 sync to the other two buckets.
- **SSM Parameter Store for write-path secrets.** SecureString + EC2 instance role, free tier.
  Single-homed in `ap-southeast-1`; MY/ID instances read cross-region. GCP Secret Manager copy
  retires at the step 22 cutover.
- **CodeBuild → ECR is the only image build path.** GCP Cloud Build configs deleted (pipeline +
  embedder — the L4 GPU embedder was already dropped for Kaggle-only).

**Completed (steps 7–15, 16-i through 16-iii):**
- Stateless MCP, Qwen3 instruction prefix, Kaggle dataset + kernel.
- ARM64 Containerfiles, AWS infra IaC prep (`deploy/aws/`).
- Qwen3-Embedding ONNX integrated + validated, FP16 switch.
- GCP SA separation, IAM/auth docs, security hardening.
- WAF container testing (Cloudflare + AWS WAF minting in-container).
- Pipeline Containerfile (Chrome + Xvfb), debug logging, CGO MuPDF build.
- Write-path review + hardening (6 stages audited, 7 fixes).
- Content quality audit (9 zero-chunk docs — all expected edge cases).
- Config controls (article/rollup citation prefixes per jurisdiction).
- GCS fetch cache + Kaggle batch embedding integration.
- Discover limit filter (`-run-all -limit N`).
- Local VN test (15 docs, 3,103 chunks, Document AI OCR — all stages work).
  Fixed: MuPDF SIGSEGV under concurrency (serialize with mutex).
- RDS staging databases created (`banhmi_q3`, `laksa_q3`, `rendang_q3`).
- go-fitz: removed system libmupdf-dev, use bundled static libs (MuPDF 1.28).
- Parallel discover slices (concurrency 8), concurrent fetch (10×).
- `localConcurrency()` floor raised to 4, vbpl `sweepPageSize` 500→50.

**Step 16-iv — write path on AWS (in-country IP) + full pipeline runs (CURRENT FOCUS):**

Staging DB state:
- `laksa_q3` (MY): **complete** — 53 silver docs, 4,396 chunks, 4,396 embedded.
- `rendang_q3` (ID): **partial** — 75 silver, 6,425 chunks, 3,456 embedded (needs re-run).
- `banhmi_q3` (VN): **partial** — 1,130 fetched, 864 silver, 0 chunks (killed during extract).
  Needs re-run from Hanoi LZ EC2 (VN source geo-lock).

Ordered work — **caches into S3 first, then build in AWS, then run**. Order matters: caches first so
AWS runs start warm (VN reuses its 1,130 already-fetched files instead of re-crawling), code before
build so the image contains the S3 cache code, build before runs.

1. **AWS foundations (one-off) — DONE (2026-07-10).**
   - a. Regions `-3`/`-5` enabled; Hanoi LZ zone group was already opted in. LZ subnet
     `subnet-02eb0b494c042f84a` (172.31.96.0/24, `ap-southeast-1-han-1a`) in the RDS VPC
     (`vpc-0131627fca4bde433`), public IP on launch; main route table already routes `0.0.0.0/0`
     → IGW (no NAT anywhere — LZ egress keeps the VN IP).
   - b. Data buckets created, private + public access blocked: `danny-banhmi-data-vn` (`-1`),
     `-my` (`-5`), `-id` (`-3`).
   - c. SSM SecureStrings created + read-back verified: `/banhmi/db-password`, `/banhmi/gcp-sa-key`
     (Document AI + its GCS cache), `/banhmi/kaggle-token`, plus `/banhmi/gcs-hmac-access`/`-secret`
     — a dedicated **GCS HMAC key** so the seed Lambda reads GCS via the S3-interop API with plain
     boto3 (no Google SDK bundle). MY/ID instances read with explicit `--region ap-southeast-1`.
   - d. IAM: `banhmi-pipeline-ec2` role + instance profile (S3 RW on the 3 buckets, SSM `/banhmi/*`
     read, ECR pull, CloudWatch Logs write); `banhmi-seed-lambda` role. `banhmi-cli` gained the
     scoped inline policy `banhmi-write-path` (SSM `/banhmi/*` + Lambda `banhmi-*`). Self-terminate
     needs no IAM — instance-initiated shutdown behavior handles it.
   - e. ECR repo `banhmi-pipeline` + replication rules → `-3`/`-5` (prefix-filtered) + pull-through
     cache `ecr-public` → `public.ecr.aws` (no Docker Hub creds).
   - f. RDS SG untouched, as planned — stays `0.0.0.0/0` (TLS + password) while the GCP read path
     connects; **step 22 tightens it**.
2. **Seed caches into S3 — DONE (2026-07-10).**
   - a. `deploy/aws/seed-data-cache.{py,sh}` → Lambda `banhmi-seed-data-cache` (python3.13,
     boto3-only, resumable; GCS via S3-interop + HMAC from SSM). One invocation copied the full
     cache — **2,943 objects / 5,160,096,917 bytes, 0 errors, ~3 min**.
   - b. Server-side `aws s3 sync` → `-my` and `-id`. All three buckets verified **byte-exact**
     against GCS (2,943 / 5,160,096,917).
   - c. Build-deps cache: nothing needed for the pipeline image (no ONNX baked in);
     `banhmi-build-cache` (S3) already holds the read-path deps for step 18.
3. **Code changes (must land before the image build).**
   - a. **DONE (2026-07-10).** `FileStore` interface + S3 impl in `pkg/pipeline/s3_cache.go`
     (`aws-sdk-go-v2/s3`, key layout `files/{name}`, atomic temp+rename download); `gcs_cache.go`
     deleted; wired via `pipeline.BuildFileStore` at the composition root.
   - b. **DONE.** `BANHMI_S3_DATA_BUCKET` added; `BANHMI_GCS_DATA_BUCKET` +
     `BANHMI_EMBEDDER_URL`/`BANHMI_EMBED_TOKEN` retired (fed nothing after the split).
   - c. **DONE.** GCP build files deleted, `BUILD-CACHE.md` updated; `gcsbatch` engine code,
     the `/embed-batch` handler, and the dead `pkg/rag/embed/cloudrun.go` client removed.
     Verified: `go build ./...`, full `make test`, zero stale references.
   - d. Launch tooling `deploy/aws/run-pipeline.sh`: per-country launch (AL2023 x86_64 AMI,
     `m7i.large`, instance profile, `--instance-initiated-shutdown-behavior terminate`; LZ EBS may
     be gp2-only — verify, override volume type for VN if needed). User-data: read SSM secrets
     (SA key → `GOOGLE_APPLICATION_CREDENTIALS`) → ECR login + pull → `cmd/pipeline -run-all` →
     logs to CloudWatch → `shutdown -h now`. Full env per country: `BANHMI_JURISDICTION`, DB
     host/name/sslmode (host from SSM `/banhmi/db-host`), `BANHMI_S3_DATA_BUCKET`,
     `BANHMI_OCR_ENGINE=documentai` + `BANHMI_DOCAI_PROCESSOR` (SSM `/banhmi/docai-processor`) /
     `BANHMI_DOCAI_BUCKET`, `BANHMI_EMBED_ENGINE=kaggle`. **Hang watchdog:** schedule
     `shutdown -h +720` at boot, wrap the run in `timeout`, `trap` on exit — a wedged pipeline
     must still terminate.
4. **Build in AWS (CodeBuild → ECR).**
   - a. CodeBuild project `banhmi-pipeline` (`ap-southeast-1`, x86_64, `BUILD_GENERAL1_LARGE`),
     source GitHub, buildspec `deploy/aws/buildspec-pipeline.yml` (base-image refs rewritten to
     the pull-through cache URL). Service role: ECR push + `ecr:BatchImportUpstreamImage`/
     `ecr:CreateRepository` (first pull through the cache) + CloudWatch Logs. Push `:latest` +
     git-SHA tag; confirm replication landed in `-3`/`-5`.
5. **Per-country pipeline runs (staging DBs).**
   - a. **VN:** full run from Hanoi LZ into `banhmi_q3` (discover → lexindex) — first real test of
     geo-locked fetch from AWS. Single-zone LZ: on `InsufficientInstanceCapacity`, retry with
     backoff or fall back to `c7i.large`/`r7i.large` (both offered in han-1).
   - b. **ID:** re-run from Jakarta into `rendang_q3`, after VN completes.
   - c. **MY:** no re-run (`laksa_q3` complete); `ap-southeast-5` infra stands ready for future
     refresh runs.
6. **Embed + finish.**
   - a. Embeds run **inline on the EC2s** — `-run-all` includes EmbedAll (Kaggle engine, token
     from SSM). This item is the verification/backfill pass: `-embed-all` for `laksa_q3` gaps +
     any leftovers, run from a small EC2, never the WWAN laptop.
   - b. HNSW index rebuild after all embeds complete.
   - c. Investigate Document AI "no OCR output" for scanned PDFs (MY/ID).
   - d. Retire the `gs://danny-banhmi-data` bucket once the runs verify the S3 cache — only
     `files/` lives in it (the Document AI cache is the separate `danny-banhmi-docai` bucket,
     which stays).

**Step 17 — Eval.** Dump staging DBs from RDS, import to local Postgres, run `make eval-onnx` on
all 3 golden sets (VN, MY, ID). Must match or beat BGE-M3 baselines. *Gates everything downstream.*

**Step 18 — Code remaining read path.** X-Origin-Verify middleware in Go (currently only in
CloudFront config, not enforced server-side). Build and push the ARM64 MCP image to ECR
(CodeBuild, deps from `banhmi-build-cache`).

**Step 19 — Review.** Code + plan.

**Step 20 — Deploy AWS infra.** Provision EC2 + ECS + CloudFront from IaC prep (`deploy/aws/`).
**RDS snapshot** before corpus changes. Restore Qwen3 corpus into side-by-side databases
(`banhmi_q3`, `laksa_q3`, `rendang_q3`) — NOT into the live DBs (GCP still serves BGE-M3). Verify
all 3 endpoints via CloudFront domains.

**Step 21 — Profile memory on real EC2.** Measure peak RSS per container during ONNX inference.
Verify ORT mmap shares physical pages across 3 containers (`smem` / `/proc/PID/smaps`). If mmap
works: ~1.5 GB total, fits t4g.medium (4 GB). If ORT copies into arena: resize to t4g.large (8 GB).

**Step 22 — DNS cutover + bake.**
1. Update DNS: `*.danny.vn` CNAMEs from Firebase → CloudFront distribution domains.
2. Haiku-over-MCP smoke test against all 3 endpoints.
3. Bake period (24–48h) — monitor before teardown.
4. Tear down GCP Cloud Run services + Firebase Hosting sites.
5. Drop old BGE-M3 databases on RDS; rename `*_q3` databases to final names.
6. Retire the GCP Secret Manager copy (`banhmi-db-pw`) — SSM `/banhmi/db-password` already serves
   the write path; point the read path at it (or Secrets Manager, per `deploy/aws/`).
7. Tighten the RDS SG from `0.0.0.0/0` to the ECS instance SG + write-path launch IPs — possible
   now that no GCP service connects.

**After deploy — memory tuning + monitoring:**
- Container memory limits from step 21 data.
- Benchmark under load: concurrent MCP queries, RSS per container, p99 latency.
- CloudWatch alarms (replace GCP $5/mo budget alert).
- Cross-encoder reranker evaluation on expanded golden sets.

**Cost estimate (monthly, post-v0.3.0):**

| Component | Cost |
|---|---|
| EC2 t4g.medium (read path, always-on) | ~$25–30 |
| Elastic IP (IPv4) | ~$3.60 |
| EBS root volume (16 GB gp3) | ~$1.30 |
| CloudFront (3 distributions, low traffic) | ~$1–2 |
| RDS t4g.micro (3 DBs) | ~$13 |
| Write pipeline (EC2 m7i.large ×3, self-terminating; Hanoi LZ price premium) | ~$2–3 per full run |
| Embed GPU (Kaggle T4) | $0 (free) |
| OCR (Document AI) | ~$0.05 |
| S3 data buckets (3 × 4.8 GiB mirror) + GCS OCR cache | ~$0.50 |
| ECR (×3 replicas) + CodeBuild | ~$1 |
| **Total** | **~$50/mo** (drop to ~$41 with 1yr RI) |

If ORT mmap doesn't work: t4g.large (8 GB, ~$49/mo) — total ~$74/mo.

**Scaling path (future):** add ALB (~$16/mo) + switch to Fargate or add EC2 instances.

### Phase 0 — expansion pre-work

1. **Jurisdiction seam registry — DONE.** `pkg/base/jurisdiction` descriptor registry.
2. **VN prod data quality — DONE.** Mojibake remediated, corpus synced.
3. **MY (laksa) hybrid — DONE.** Hybrid retrieval deployed. Remaining: P.U. relation backfill,
   8 needs_review docs, layout-aware Section titles.
4. **Indonesia (rendang) — DONE, LIVE.** Sources, parser, registry, MCP brief, golden set.
5. **Validity/amendment refresh re-crawl.** Scheduled per-country status refresh.
6. **Eval as the permanent gate.** Grow per-jurisdiction golden sets; every change ships with eval delta.
7. **Drift & quality monitoring.** Track abstain rate, gaps, validity-unknown, embedding coverage.

### v0.3.1 — Indonesia expansion + Singapore (after v0.3.0)

After v0.3.0 lands the AWS read path (VN + MY + ID), expand Indonesia and add Singapore. Both
deploy on the same AWS infra (new ECS containers + CloudFront distributions).

**Discovery filtering model** (see [SOURCES.md](docs/design/SOURCES.md)): same two-category approach
as VN. Banking/financial-regulation sources sweep all + `scope.Match`; general national-law sources
use per-country keywords to avoid crawling irrelevant documents.

#### Indonesia (`rendang`) — source expansion

**Current state:** live with 2 sources (bpk + bi). OJK (`jdih.ojk.go.id`) now reachable (was
geo-fenced). `jdih.komdigi.go.id` also reachable. `peraturan.go.id` still blocked.

Work:
1. **Verify OJK site structure** — spike on `jdih.ojk.go.id`: API/HTML, doc counts, relations.
2. **Add OJK source** (`pkg/ingest/ojk`). Crawls POJK + SEOJK directly from the regulator. Sweep all.
3. **Add komdigi source** (`pkg/ingest/komdigi`). Telecom/data/electronic-system regs. Sweep all.
4. **Keywords for bpk UU/PP.** `discovery_keyword_id.csv` + extend `DiscoverSlices` for bpk.
5. **Update INDONESIA.md.** Remove stale geo-fence references.
6. **Grow golden set** and re-eval.

#### Singapore (`kaya`) — new jurisdiction

Follows the [playbook phase template](docs/design/jurisdictions/PLAYBOOK.md#phase-template-per-country).

- Sources: MAS (Notices + Guidelines), SSO (consolidated Acts in HTML), scoped PDPC/CSA. English
  corpus; MY citation family near-reuses. Gate: SSO bot-protection/ToS check.
- Discovery: evaluate per source. SSO (full statute database) likely needs keywords; MAS sweeps all.

#### Thailand (`tomyum`) — deferred

After SG. Sources: BOT notifications, Krisdika consolidated Acts, Royal Gazette. Thai corpus.
**Heaviest language work:** Thai word segmentation for BM25, B.E./C.E. date normalization, Thai numerals.

### MVP2 candidates (parked)

Gemma 4 E4B OCR enhancement · figure extraction · manual-folder source · crawl depth >1 ·
`sbv.gov.vn` extra source · Cloud Armor edge · cross-encoder reranker (eval-only today).

## Milestone history (compressed)

- **2026-05-30** — evidence-only pivot. MCP = product surface; embedder mandatory.
- **2026-05-31** — INPUT hardening. `RunAll` orchestrator + Kaggle batch; 572 docs / 62,350 chunks.
- **2026-06-01** — VN deployed. RDS + Cloud Run + Firebase → `banhmi.danny.vn/mcp`.
- **2026-06-10** — MVP1 completion pass. `doc_key`, `relation_context`, OCR-floor, validity honesty.
- **2026-06-13** — cost fix. Deleted Cloud Run NAT/router/static IP (~$35/mo).
- **2026-06-19/20** — vanban source. `vanban.chinhphu.vn` live-validated.
- **2026-06-21** — Malaysia built (local). Jurisdiction seam; agclom/bnm/sc; 63 docs / 8,425 chunks.
- **2026-06-22** — hybrid retrieval + Malaysia deployed. BM25 + RRF. `laksa.danny.vn/mcp`.
- **2026-07-02** — mojibake remediation. UTF-8-forced HTML extraction + Cyrillic gate.
- **2026-07-04** — Indonesia coded + Document AI OCR coded.
- **2026-07-05** — ONNX Phase A + retrieval quality. Golden sets expanded (VN 54, MY 51).
- **2026-07-06** — Indonesia LIVE + Temporal removed.
- **2026-07-08** — Document AI + go-fitz rolled out. Qwen3-Embedding FP16 replaces BGE-M3 INT8.
  Read path to AWS decided. Build cache seeded.
- **2026-07-09** — write path review + hardening. EC2 per-country write path (VN Hanoi LZ geo-lock).
  Kaggle-only embedding (Cloud Run L4 GPU dropped). go-fitz static libs fix. Pipeline concurrency
  improvements.
- **2026-07-10** — write path fully in-country decided **+ foundations & S3 seed executed** (LZ
  subnet, 3 buckets seeded byte-exact via Lambda, SSM secrets, IAM, ECR). ID → Jakarta
  (`ap-southeast-3`); per-region S3 file
  cache (seeded from GCS before builds); CodeBuild → ECR + replication + ECR Public pull-through;
  SSM write-path secrets; GCP Cloud Build configs dropped.

**Do not reopen (settled):** evidence-only surface; go-fitz extraction cascade with Document AI OCR
fallback; `doc_key = <TYPE>|<NUMBER>`; hybrid via native pgvector sparsevec (no ParadeDB); RDS as
single datastore; Temporal removed; MarkItDown and EasyOCR replaced.

## Deferred / dropped

- **Answer LLM / chat endpoint / web UI** — dropped; the user's model answers.
- **Watchdog reconcile** — fetch-lease recovery covers it.
- **phapluat.gov.vn** — dropped for MVP1.
- **Reranker** — eval-only; revisit on larger golden set.
- **`bronze.source_document_history`** — dropped; temporal model is silver validity + amendment.
- **English/multilingual experiment** — reverted; one language per country.
- **Temporal** — removed; `cmd/pipeline` calls activities directly.
- **MarkItDown** — replaced by go-fitz.
- **EasyOCR** — replaced by Document AI as default; available as offline fallback.
- **Cloud Run + Firebase (read path)** — replaced by CloudFront + ECS on EC2.
- **Cloud Run CPU Job (write path)** — replaced by self-terminating EC2 per country, in-country IP
  (VN Hanoi LZ, MY ap-southeast-5, ID ap-southeast-3).
- **Cloud Run L4 GPU embedder** — dropped. Kaggle T4 is simpler, free, and each run gets a fresh GPU
  (no memory fragmentation from concurrent multi-country embedding on a shared instance).
- **BGE-M3** — replaced by Qwen3-Embedding-0.6B.
- **`gcsbatch` / `embed.engine=cloudrun`** — dropped with the Cloud Run L4 GPU embedder; engine
  code removal is step 16-iv.3. Kaggle I/O is dataset-based, not GCS.
- **GCP Cloud Build configs** (`cloudbuild-pipeline.yaml`, `cloudbuild-embedder.yaml`,
  `Containerfile.embed-job.onnx`) — deleted; AWS CodeBuild → ECR is the only image build path.

## Decisions log

| Decision | Choice | Principle |
|----------|--------|-----------|
| **INPUT before OUTPUT** | corpus first, validated on real docs; then serving | data quality is the product |
| **Evidence-only; no answer LLM** | citations/validity/relations/gaps over MCP; user brings the model | we own the data, not the answer |
| **Multi-jurisdiction** | jurisdiction = config dimension; Postgres DB is the boundary; one image, N deployments | share the core, customize behind interfaces; never fork |
| **One language per country** | index/serve/search only the binding native language; never translate | translation risks legal error |
| **Deploy shape** | Write = self-terminating EC2 in-country ×3 (VN Hanoi LZ, MY ap-southeast-5, ID ap-southeast-3) + Kaggle T4 (embed), per-region S3 file cache → RDS ← ECS on EC2 (ONNX, ARM64) ← CloudFront | same-VPC read; in-country write (geo-locks, polite crawls); free GPU |
| **Per-region S3 data buckets** | `danny-banhmi-data-{vn,my,id}` beside each pipeline; flat keys, seeded once from GCS | cache next to the compute; no cross-region chatter |
| **Write-path secrets in SSM** | SecureString + EC2 instance role, single-homed `ap-southeast-1`; GCP SM retires at cutover | free tier; least moving parts |
| **Kaggle-only embedding** | Free T4 GPU, fresh GPU per run, dataset-based I/O. Cloud Run L4 GPU dropped | simpler, free, no memory fragmentation |
| **Temporal removed** | `cmd/pipeline` calls activity methods directly | simplify; no durable workflow needed |
| **Hybrid retrieval** | dense + native pgvector `sparsevec` BM25 + RRF + query router | beats vector-only on eval; single datastore; RDS-portable |
| **Qwen3-Embedding-0.6B FP16** | 1024 dims, 32K context, ONNX FP16 everywhere; ORT mmap's external data | FP16 for GPU compat; mmap for memory sharing |
| **Read path to AWS** | CloudFront + ECS on EC2 t4g.medium (ARM64), 3 containers, host networking | always-on; same VPC as RDS; scales to ALB+Fargate later |
| **No local bulk embed** | Kaggle GPU only — never on the dev laptop (8 GB) | protect the dev machine |
| PDF engine | go-fitz (MuPDF via purego). MarkItDown removed | zero-Python; 15–60× faster |
| OCR | Document AI Enterprise OCR (GCS-cached, default). EasyOCR as fallback | cleaner text, no local CPU |
| No composite primary keys | surrogate identity PKs; business keys `UNIQUE` | idempotent `ON CONFLICT` upserts |
| Pre-release migrations | mutable until first tagged release, then append-only | no fix-up migrations pre-v1 |
| Validity-aware retrieval | current-law leads; non-current appended, badged, capped | never present repealed law as current |
