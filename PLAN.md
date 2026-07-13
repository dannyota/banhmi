# banhmi plan

Living roadmap and progress tracker. Architecture detail in [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md);
conventions and the canonical agent guide in [`CLAUDE.md`](CLAUDE.md); the multi-country model in
[`docs/design/jurisdictions/`](docs/design/jurisdictions/). Last updated: 2026-07-11.

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
| 3 | 🇮🇩 Indonesia | `rendang` | rendang.danny.vn/mcp | **LIVE** (revived 2026-07-12) | [INDONESIA](docs/design/jurisdictions/INDONESIA.md) |
| 4 | 🇸🇬 Singapore | `kaya`* | kaya.danny.vn* | PROPOSED | [SINGAPORE](docs/design/jurisdictions/SINGAPORE.md) |
| 5 | 🇹🇭 Thailand | `tomyum`* | tomyum.danny.vn* | PROPOSED | [THAILAND](docs/design/jurisdictions/THAILAND.md) |

\* codename/domain proposed, **pending maintainer sign-off**. Recommended **build order: SG → TH**
— SG is the cheapest build (English, MY citation family, SSO HTML statute trees); TH last because it
carries the heaviest language work (Thai word segmentation for the lexical arm, Buddhist-Era dates,
Thai numerals). Final order is the maintainer's call.

## Deployment shape (current prod + v0.3.0 target)

- **Write path — PARKED for v0.3.0 (2026-07-11).** No further AWS write-path work now; v0.3.0
  corpora come from **local pipeline runs** (laptop egresses from a VN IP) dumped/restored to RDS.
  The validated infra stays dormant for future refresh runs:
  - **VN:** self-terminating EC2 `m7i.large`, **Hanoi Local Zone** `ap-southeast-1-han-1a` — VN IP.
    **Geo-lock:** `vbpl.vn`, `sbv.hanoi.gov.vn`, `vbpl-bientap-gateway.moj.gov.vn` block non-VN
    IPs. Stages 1–4 validated on real rows from the LZ (2026-07-10).
  - **MY:** same pattern, `ap-southeast-5` (Malaysia).
  - **ID:** decommissioned (2026-07-11) — bucket, ECR replica, read service, DBs removed.
  - File cache in **per-region S3 buckets** (`danny-banhmi-data-{vn,my}`), seeded from GCS.
  - Pipeline image in **ECR**, built by **AWS CodeBuild** (`ap-southeast-1`), **replicated** to
    `-5`; base images via **ECR pull-through cache from ECR Public** (no Docker Hub creds).
  - Secrets (RDS password, GCP SA key, Kaggle token) in **SSM Parameter Store**.
  - CPU-only: go-fitz extraction (~1ms/page), Document AI OCR (GCS-cached, async). No GPU.
    **Pipeline runner:** `cmd/pipeline` (no Temporal); calls activity methods directly.
  - Bulk embedding offloads to **Kaggle T4 GPU** (`embed.engine=kaggle`, free). Each Kaggle run
    gets a fresh GPU; kernel uses two-budget shape-bucketed batching (fixed 2026-07-11).
- **DB — AWS RDS PostgreSQL 17 + pgvector/HNSW** (`ap-southeast-1`, t4g.small), **one database per
  country** on one instance (`banhmi`, `laksa` — Qwen3 corpora). TLS-required, password-gated,
  deletion-protected, 7-day backups; **5432 reachable only from the read-path origin SG** (local
  pipeline runs temporarily allowlist the maintainer /32).
- **Read path (prod since 2026-07-12) — AWS** (`ap-southeast-1`): CloudFront (2 distributions, ACM
  TLS) → ECS on one EC2 t4g.large (ARM64 Graviton, 2 containers, host networking, X-Origin-Verify
  enforced) → RDS, in-process Qwen3-Embedding ONNX FP16 query embedder; `GET /` = per-jurisdiction
  landing page. GCP Cloud Run + Firebase torn down 2026-07-12.
- **Retrieval — hybrid** (single datastore): dense vectors + **BM25 sparse vectors** (pgvector
  `sparsevec`, `cmd/lexindex`) fused with RRF + a deterministic query router. No ParadeDB/`pg_search`.
- **GCP dependency (remaining):** Document AI OCR + its GCS cache bucket (`danny-banhmi-docai`,
  `BANHMI_DOCAI_BUCKET`) + the `banhmi-pipeline-dev` SA — nothing else. Kaggle I/O is **Kaggle
  datasets, not GCS**. All other GCP resources deleted 2026-07-12 (Cloud Run, Firebase sites,
  Secret Manager, old GCS caches, HMAC keys, Artifact Registry).

## Current state (live `corpus_status`)

**🇻🇳 VN (banhmi) `v0.1.0-20260704` (prod):** 1,608 docs total · **712 indexed** · **47,504 chunks** ·
**100% embedded** · 8,859 confirmed relation edges · `search_ready`. **Hybrid retrieval live**.
`bm25_score` per hit live. Mojibake remediated (doc 200 clean, 2026-07-04). Open gaps (via
`quality_gaps`): 964 unresolved relation targets (deliberate one-level crawl boundary), 83 needs-review
text docs, 27 indexed docs without binding text (badged), 4 docs without current validity. 887
relation-context docs deliberately unindexed. *(Local has a newer corpus: 723 indexed / 47,587 chunks
after the corpus gap fix + re-embed on Kaggle; the "sync" is now the step 18 Qwen3 `_q3` restore — no further BGE-M3 prod sync.)*
**Eval (54-case golden set, 2026-07-05):** recall 75.9%, MRR 60.0%, current-law 100%, abstention 100%.

**🇲🇾 MY (laksa) `v0.1.0-20260704` (prod):** 63 docs · 8,425 chunks · **100% embedded** ·
**100% sparse** · 62 in-force + 1 expired · `search_ready`. **Hybrid retrieval live**. `bm25_score`
per hit live. Remaining: 1,000 P.U. relation stubs (unresolved), 8 needs_review docs (agclom PDFs,
null markdown), layout-aware Section titles.
**Eval (51-case golden set, 2026-07-05):** recall 85.4%, MRR 73.6%, current-law 100%, abstention 98.0%.

**🇮🇩 ID (rendang):** **DORMANT — decommissioned 2026-07-11** (maintainer call: no ID support now).
Was live 2026-07-06 → 2026-07-11. Removed: `rendang-mcp` Cloud Run service, `danny-rendang`
Firebase site, RDS `rendang` + `rendang_q3` DBs (preserved in manual RDS snapshot
`banhmi-pre-rendang-drop-20260711`), `danny-banhmi-data-id` S3 bucket, `ap-southeast-3` ECR
replica + replication rule. **Code stays dormant** (sources `bpk`/`bi`, parser, registry entry,
golden set) — revival = restore DB from snapshot (or re-crawl) + redeploy per the
[playbook](docs/design/jurisdictions/PLAYBOOK.md). See [INDONESIA](docs/design/jurisdictions/INDONESIA.md).

## Roadmap

### v0.3.0 — AWS read path, Qwen3-Embedding, ONNX everywhere

**Status: READ-PATH-FIRST (pivot 2026-07-11).** Write path on AWS is **parked** (validated, kept
dormant); ID is **decommissioned**. v0.3.0 finishes sooner by shipping the read path for **VN + MY**:
build + well-test locally against the local Qwen3 corpora → dump/restore to RDS `banhmi_q3`/
`laksa_q3` → deploy AWS read path → well-test → migrate DNS. Three changes as before:
1. **Read path migrates from GCP Cloud Run to AWS** — CloudFront + ECS on EC2 (ARM64 Graviton).
   Eliminates cross-cloud latency (GCP→AWS), cold starts, and Firebase Hosting dependency.
2. **Embedder switches from BGE-M3 to Qwen3-Embedding-0.6B** (`onnx-community/Qwen3-Embedding-0.6B-ONNX`,
   **FP16**, 1.2 GB). Same 1024 dims, 32K context (vs 8K). Full re-embed — **done locally
   (2026-07-11):** `banhmi_q3` 49,302 + `laksa_q3` 8,996 chunks, embedded + lexindexed.
3. **ONNX Runtime everywhere** (query + bulk). OpenVINO removed.

**Temporal removed** — `cmd/pipeline` calls activity methods directly (shipped 2026-07-06).

**Architecture:**
```
READ PATH — AWS (ap-southeast-1), always-on:
  User → CloudFront (2 distributions, ACM TLS, *.danny.vn)
           ├─ banhmi.danny.vn  → origin :8081
           ├─ laksa.danny.vn   → origin :8082
           │
         EC2 t4g.large (2 vCPU / 8 GB, Graviton ARM64, Elastic IP)
         ECS cluster (1 instance, host networking)
           ├─ banhmi-mcp  :8081  (BANHMI_JURISDICTION=vn)
           ├─ laksa-mcp   :8082  (BANHMI_JURISDICTION=my)
           │
         RDS PostgreSQL 17 + pgvector (ap-southeast-1)

  Both containers from one ARM64 image. In-process Qwen3-Embedding
  ONNX FP16 query embedder. MEASURED (2026-07-11, ARM64 t4g.medium):
  ORT copies the external-data weights into its private arena
  (anon-rss ~2.2-2.3 GB per process, file-rss 0; the onnx_data
  mappings are rw-p with Rss 0) — NO cross-process page sharing.
  Two servers OOM a 4 GB box → t4g.large (8 GB), ~2.6 GB memory
  limit per container.

WRITE PATH — PARKED (dormant, for future refresh runs):
  Self-terminating EC2 m7i.large per country:
    VN: ap-southeast-1-han-1a (Hanoi Local Zone) — VN IP
    MY: ap-southeast-5 (Malaysia)
    cmd/pipeline -run-all; file cache per-region S3
    (danny-banhmi-data-{vn,my}); image CodeBuild → ECR → -5;
    secrets SSM. v0.3.0 corpora instead come from LOCAL runs
    (laptop has a VN IP) dumped/restored to RDS.

  Embedder (GPU) — Kaggle T4 (free):
    kernel_embed.py on Qwen3-Embedding ONNX FP16.
    Model dataset: danhsoftware/qwen3-embedding-06b-onnx-fp16.
    Dataset I/O: input JSONL uploaded as a Kaggle dataset →
    embed on T4 → kernel output vectors downloaded. No GCS.
    Two-budget shape-bucketed batching (OOM fix, 2026-07-11).
```

**Key design decisions:**
- **Read path moves to AWS.** Cloud Run cold start (6–8s) and cross-cloud latency (GCP→AWS ~10–20ms)
  worse than always-on in the same VPC as RDS (<1ms). ~$25/mo for zero cold starts.
- **2 CloudFront distributions, custom origin ports.** Each domain routes to a different port on
  `origin.danny.vn` (A record → Elastic IP). Custom origin header (`X-Origin-Verify: <secret>`).
  Origin-response timeout 60s (default 30s too short for SSE streams).
- **ARM64 (Graviton).** ~20% better price/performance.
- **Qwen3-Embedding-0.6B replaces BGE-M3.** 0.6B params, 1024 dims, 32K context (4× BGE-M3).
  FP16 over INT8: ONNX INT8 dynamic quantization has no CUDA kernels. FP16 external data format;
  ORT was expected to mmap-share weights across containers, but measurement (step 21, done
  early 2026-07-11) shows it copies them into a private arena per process → t4g.large.
- **Kaggle-only embedding.** Free T4 GPU, fresh GPU per run (no memory fragmentation),
  **dataset-based I/O** — input texts uploaded as a Kaggle dataset, vectors downloaded from the
  kernel, no GCS in the loop. Simpler than managed GPU services.
- **Write path in-country (parked).** One self-terminating EC2 pattern per country (VN Hanoi LZ,
  MY `ap-southeast-5`), validated 2026-07-10 then **parked 2026-07-11** — v0.3.0 corpora come from
  local runs; the infra stays dormant for future refresh runs. In-country IP defeats geo-locks
  (VN today, others may follow) and keeps crawls in-region and polite.
- **Per-region S3 data buckets.** The fetch cache lives in the same region as its pipeline — no
  cross-region chatter. Keys are flat (`files/{name}`, no jurisdiction prefix), so seeding is a
  full mirror into each bucket (~$0.40/mo storage) — cheaper than building DB-derived per-country
  file lists. Seeded by a one-off **AWS Lambda** (GCS → S3, egress paid once ~$0.60), then
  server-side S3 sync to the other buckets.
- **SSM Parameter Store for write-path secrets.** SecureString + EC2 instance role, free tier.
  Single-homed in `ap-southeast-1`; MY instances read cross-region. GCP Secret Manager copy
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

**Step 16-iv — write path on AWS (in-country IP) — CLOSED (parked 2026-07-11):**

Outcome: the in-country write path is **built and validated** (VN stages 1–4 on real rows from the
Hanoi LZ; embed kernel OOM fixed + validated locally), then **parked** — the maintainer dropped
further AWS write-path work to finish v0.3.0 sooner via the read path. ID was decommissioned the
same day (see Jurisdictions). The remaining EC2 items (RDS `banhmi_q3` embed backfill, ID Jakarta
re-run) are **superseded** by step 18's dump/restore.

Authoritative Qwen3 corpora (local podman, 2026-07-11): `banhmi_q3` **49,302/49,302 embedded +
lexindexed**; `laksa_q3` **8,996/8,996 embedded + lexindexed**. RDS `banhmi_q3`/`laksa_q3` will be
**overwritten** by these via dump/restore in step 18. RDS staging leftovers (`banhmi_q3` LZ rows,
old `laksa_q3`) carry no value beyond the snapshot.

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
4. **Build in AWS (CodeBuild → ECR) — DONE (2026-07-10).**
   - a. Project `banhmi-pipeline` (GitHub source, `BUILD_GENERAL1_LARGE`, privileged; service role
     `banhmi-codebuild`), buildspec `deploy/aws/buildspec-pipeline.yml`. First attempt failed on a
     bashism (CodeBuild's default shell is dash) — fixed with `env: shell: bash` + POSIX tag.
     Green build in ~3 min; image 587 MB, tags `latest` + git SHA; **replication verified** in
     `-3`/`-5`.
5. **Per-country pipeline runs — CLOSED (2026-07-11).**
   - a. **VN:** stages 1–4 **validated on real rows from the LZ** (2026-07-10): geo-locked
     vbpl/sbv fetch from the VN IP, S3 file cache live, Document AI OCR (104 scans), index →
     1,741 silver docs, 50,944 chunks in RDS `banhmi_q3` (superseded by step 18 restore).
     Launcher fixes committed (docker image ref, SA key uid 1000, kaggle model-dataset default).
     **Stage 5 (embed) OOM — SOLVED (2026-07-11), commit `8266f0b`.** Root cause (two decoded
     OOMs, incl. an exact 1,879,048,192-byte allocation = packed FP16 QKV + **FP32** attention
     scores for a [256,256] batch): ORT's unfused MHA allocates one contiguous per-layer workspace
     of `count·pad·12,288 + count·16·pad²·4` bytes, and the model's present-KV graph outputs pin
     ~96 KB/token for all 24 layers — plus CUDA-arena fragmentation from hundreds of distinct
     input shapes under the old ad-hoc batching. **Fix: dynamic two-budget shape-bucketed
     batching** — pads quantized to 128-multiples, one exact shape `[count_for(pad), pad]` per pad
     step (dummy-row padding), budgets `count·pad ≤ 32,768` (retained KV ≤ 3.2 GB) and
     `count·pad² ≤ 8M` (FP32 scores ≤ 512 MB); GPU-only guards + ORT unfused-attention pins.
     **Validated end-to-end locally:** `banhmi_q3` 49,302/49,302 (one ~30-min T4 kernel, no OOM),
     `laksa_q3` 8,996/8,996; vectors unit-norm 1024-d, sane same-doc vs cross-doc separation.
     Single-zone LZ note (future runs): on `InsufficientInstanceCapacity`, retry or fall back to
     `c7i.large`/`r7i.large`.
   - b. **ID:** dropped — jurisdiction decommissioned.
   - c. **MY:** no EC2 run needed — local `laksa_q3` is the corpus.
6. **Write-path leftovers (parked with it).** Document AI "no OCR output" investigation for
   scanned MY PDFs; retire the `gs://danny-banhmi-data` GCS bucket (S3 cache verified; the
   Document AI cache `danny-banhmi-docai` stays); HNSW rebuild folds into step 18's restore.

**Step 17 — Local read-path build + well-test (VN + MY) — CURRENT FOCUS.**
1. **Eval gate — PASSED (2026-07-11), Qwen3 beats BGE-M3 on both corpora.** Ran on a disposable
   dev EC2 against the RDS staging DBs (never the laptop; laptop dumps restored to RDS first).
   Official numbers, hybrid mode (production default):
   - **VN** `banhmi_q3` (54 cases): **recall 83.3% / MRR 60.6%** vs baseline 75.9 / 60.0;
     current-law 100%, abstention 100%. Required fixes along the way: eval measured vector mode
     while prod serves hybrid (`caa0249`); identifier-scoped retrieval, VN-gated (`0a5460b`);
     golden refresh — 4 cases re-pointed to text-verified current law (`9e5f192`); golden language
     policy — cross-lingual cases converted to Vietnamese (`027c60f`). 7 honest failures remain
     (2 article-level misses, 2 Luật ANM 2025 ranking gaps, 1 hybrid-vs-vector case, 1 OCR-only
     doc, 1 no-diacritics payment) — tracked for retrieval work, not test-tuned away.
   - **MY** `laksa_q3` (51 cases): **recall 87.5% / MRR 76.7%** vs baseline 85.4 / 73.6;
     current-law 100%, abstention 98%. Identifier scoping OFF (VN-only) verified by byte-identical
     re-runs.
   - Infra note: the eval OOM-crashed RDS t4g.micro → **upsized to t4g.small** (2026-07-11);
     VN full set then ran in 3m17s.
2. **X-Origin-Verify middleware — DONE (2026-07-11, `800508b`).** Enforced server-side when
   `BANHMI_ORIGIN_VERIFY_SECRET` is set (constant-time, comma-separated rotation, /healthz
   exempt); `banhmi-origin-verify` secret wired into the checklist + both ECS containers.
3. **MCP well-test — DONE (2026-07-11).** Haiku-over-MCP stand-in agents drove `cmd/server`
   (on the ARM64 dev box, tunnel over SSH, against RDS `banhmi_q3`/`laksa_q3` with in-process
   Qwen3): **VN 7/8 → 8/8** after fixing the bug it found — `quality_gaps` non_binding SQL had an
   ambiguous `doc_number` (SQLSTATE 42702, `1210cea`; integration test now runs every category);
   **MY 8/8**. Verified: evidence contract usable through MCP alone (guide → status → search →
   document → gaps), identifier lookup serves the named doc, out-of-scope queries abstain, badges/
   source_urls/ready-to-paste cites on every hit. Dev box terminated after (key + SG deleted).

**Step 18 — Dump/restore corpora to RDS — DONE (2026-07-11, ahead of order).** Snapshot
`banhmi-pre-rendang-drop-20260711` exists; local dumps (376 MB + 53 MB) rsynced to the dev box and
restored into RDS `banhmi_q3`/`laksa_q3`; counts + hybrid search verified (the step-17 eval gate
ran against these). Original plan text below for reference. `pg_dump` local `banhmi_q3` +
`laksa_q3` → restore into RDS `banhmi_q3` + `laksa_q3` (side-by-side; GCP still serves BGE-M3 from
`banhmi`/`laksa`). Restore rebuilds HNSW + sparse indexes. Verify row counts + a smoke search on
RDS.

**Step 19 — Review + ARM64 image.** Code + plan review; build and push the ARM64 MCP image to ECR
(CodeBuild, deps from `banhmi-build-cache`).

**Step 20 — Deploy AWS read path — ORIGIN LIVE (2026-07-11); CloudFront blocked on cert.**
- **Steps 19+20a DONE:** ARM64 image `banhmi-mcp:latest` built via CodeBuild project `banhmi-mcp`
  (ARM_CONTAINER; private build-cache deps served to docker build from a docker container —
  CodeBuild reaps shell-backgrounded processes; SHAs pinned). ECS cluster `banhmi`, task def rev 3
  (discrete `BANHMI_DATABASE_*` envs + SSM password — **`BANHMI_DATABASE_URL` never existed in
  code**; full secret ARNs required), service on a `t4g.large` origin EC2 (`i-0a5822fd1a2464150`,
  EIP `3.0.173.179`, SG: 8081-8082 from the CloudFront origin-facing prefix list + temporary
  maintainer test rule). **Verified end-to-end:** both healthz 200; direct `/mcp` without
  `X-Origin-Verify` → 403; real searches served from RDS `banhmi_q3` (Điều-level cites) and
  `laksa_q3` (BNM sections) with in-process Qwen3 query embedding.
- **20b DONE (2026-07-12):** ACM validation CNAMEs added via the Cloudflare CLI (danny.vn zone is
  on Cloudflare); cert issued. CloudFront distributions live: banhmi.danny.vn →
  `d1uhmwyioi3fv7.cloudfront.net` (E19NVYOR5QQJKH), laksa.danny.vn → `d28zp32ytzu365.cloudfront.net`
  (E1XHTEBY92W3XQ); origin = the EC2 public DNS behind the EIP, origin-verify header injected.
  **Haiku-over-MCP well-test through CloudFront: both endpoints PASS** (VN 724 docs/49,302 chunks;
  MY 71/8,996; searches with citations+badges; TLS 1.3; VN ~3.5s, MY ~1.4s from SGN POP).

**Step 22 — DNS cutover — STARTED (2026-07-12): bake period running.**
- Items 1–3 DONE: `banhmi.danny.vn` + `laksa.danny.vn` CNAMEs flipped from Firebase to the
  CloudFront domains (Cloudflare, TTL 300, DNS-only); live smoke green on both (healthz 200, MCP
  initialize 200 over the real domains). **Rollback = flip the CNAMEs back** (GCP Cloud Run +
  Firebase still fully running as fallback).
- **Landing pages shipped (2026-07-12, `7d7b95b`):** GET / on both domains serves a static guide
  page — one embedded template + per-jurisdiction data (VN fallback, MY entry; future countries
  add a data entry), rendered at startup by `cmd/server`. SEO: meta/OG/canonical + JSON-LD
  (WebSite, WebAPI with the MCP endpointUrl, FAQPage) + robots.txt + sitemap.xml + native-language
  intro. GEO: /llms.txt machine brief. Deploys with the image; CloudFront caches at the edge
  (invalidated on deploy). ECS deployment config set to max=100%/min=0% + AZ-rebalancing off — a
  single-instance cluster cannot host old+new task sets simultaneously (2×2.6 GB × 2 > 8 GB).
- **Security review PASSED (2026-07-12).** Verified: IMDSv2 required on the origin; RDS storage
  encrypted + `force_ssl=1`; containers distroless/nonroot/read-only/no-caps; origin-verify 403 on
  direct hits; secrets scoped. Fixed in the review: RDS deletion protection ON, backups 1→7 days,
  dead SSH rule + temporary maintainer test rule removed (origin SG is now ONLY 8081-8082 from the
  CloudFront origin-facing prefix list — direct access verified blocked), SSM Session Manager
  attached for keyless debugging, CodeBuild log retention 30 d. **RDS stays public during the
  bake** — GCP Cloud Run (old prod) has no stable egress IPs; the write path (local pipeline runs)
  also connects from a dynamic VN IP. At teardown (below): SG 0.0.0.0/0 → origin-SG only;
  keep `PubliclyAccessible` + TLS + password so local pipeline/dump-restore runs can temporarily
  allowlist the maintainer /32 per run (scriptable); optionally flip fully private later and
  tunnel local access through SSM.
- **Teardown COMPLETE (2026-07-12, bake ended early — maintainer call):** GCP Cloud Run services +
  `danny-laksa` Firebase site deleted (`danny-banhmi` is the project-default site — disabled),
  `banhmi-db-pw` secret deleted, old GCS caches (`danny-banhmi-data`, `danny-banhmi-build-cache`,
  cloudbuild scratch) deleted, all 4 GCS HMAC keys purged, Artifact Registry repos deleted; AWS
  side: seed Lambda + `/banhmi/gcs-hmac-*` SSM params removed. **DB endgame:** safety snapshot
  `banhmi-pre-gcp-teardown-20260712` → old BGE-M3 `banhmi`/`laksa` dropped → `*_q3` renamed to
  canonical `banhmi`/`laksa` → task-def rev 4 → ~3 min downtime → verified (corpus_status 49,302
  chunks; laksa e-KYC search over the locked-down path). **RDS SG now origin-SG-only.**
  **v0.3.0 read-path migration is COMPLETE.**

**Step 21 — Profile memory on real EC2 — DONE EARLY (2026-07-11), measured on the ARM64 dev
box.** ORT does NOT share model pages across processes: each `cmd/server` holds ~2.2-2.3 GB
anon-rss (file-rss 0; `model_fp16.onnx_data` mappings are `rw-p`, Rss 0 — weights are copied into
the per-process arena). A second server OOM-killed a 4 GB box. Consequence: **read path = t4g.large
(8 GB)**, ECS memory limit 2600 MB per container (task def updated). Future cost lever: an INT8
query model (~600 MB) on CPU — needs an eval gate first (breaks FP16 index/query parity).

**Step 22 — DNS cutover + bake.**
1. Update DNS: `banhmi.danny.vn` + `laksa.danny.vn` CNAMEs from Firebase → CloudFront domains.
2. Haiku-over-MCP smoke test against both endpoints.
3. Bake period (24–48h) — monitor before teardown.
4. Tear down GCP Cloud Run services + Firebase Hosting sites (VN, MY — rendang already gone).
5. Drop old BGE-M3 databases on RDS; rename `*_q3` databases to final names.
6. Retire the GCP Secret Manager copy (`banhmi-db-pw`) — SSM `/banhmi/db-password` already serves
   the write path; point the read path at it (or Secrets Manager, per `deploy/aws/`).
7. Tighten the RDS SG from `0.0.0.0/0` to the ECS instance SG — possible now that no GCP service
   connects.

**After deploy — memory tuning + monitoring:**
- Container memory limits from step 21 data.
- Benchmark under load: concurrent MCP queries, RSS per container, p99 latency.
- CloudWatch alarms (replace GCP $5/mo budget alert).
- Cross-encoder reranker evaluation on expanded golden sets.

**Cost estimate (monthly, post-v0.3.0):**

| Component | Cost |
|---|---|
| EC2 t4g.large (read path, always-on; mmap sharing disproven — see step 21) | ~$49 |
| Elastic IP (IPv4) | ~$3.60 |
| EBS root volume (16 GB gp3) | ~$1.30 |
| CloudFront (2 distributions, low traffic) | ~$1–2 |
| RDS t4g.small (2 DBs; upsized from micro 2026-07-11 — micro OOM-crashed under eval load) | ~$26 |
| Write pipeline (parked; EC2 m7i.large per refresh run, Hanoi LZ premium) | ~$1–2 per run |
| Embed GPU (Kaggle T4) | $0 (free) |
| OCR (Document AI) | ~$0.05 |
| S3 data buckets (2 × 4.8 GiB mirror) + GCS OCR cache | ~$0.40 |
| ECR (×1 replica, `-5`) + CodeBuild | ~$1 |
| RDS manual snapshots | $0 — both deleted 2026-07-13 (automated backups cover DR) |
| **Total** | **~$87/mo** (drop to ~$72 with 1yr RI) |

Cost lever (future): INT8 query model on CPU (~600 MB/container) could drop the read path back
to t4g.medium — requires an eval pass first (index/query parity).

**Scaling path (future):** add ALB (~$16/mo) + switch to Fargate or add EC2 instances.

### Phase 0 — expansion pre-work

1. **Jurisdiction seam registry — DONE.** `pkg/base/jurisdiction` descriptor registry.
2. **VN prod data quality — DONE.** Mojibake remediated, corpus synced.
3. **MY (laksa) hybrid — DONE.** Hybrid retrieval deployed. Remaining: P.U. relation backfill,
   8 needs_review docs, layout-aware Section titles.
4. **Indonesia (rendang) — built, then decommissioned 2026-07-11.** Sources, parser, registry,
   MCP brief, golden set all remain in the repo, dormant.
5. **Validity/amendment refresh re-crawl.** Scheduled per-country status refresh.
6. **Eval as the permanent gate.** Grow per-jurisdiction golden sets; every change ships with eval delta.
7. **Drift & quality monitoring.** Track abstain rate, gaps, validity-unknown, embedding coverage.

### v0.3.1 — Singapore (after v0.3.0)

After v0.3.0 lands the AWS read path (VN + MY), add Singapore on the same infra (new ECS
container + CloudFront distribution).

**Discovery filtering model** (see [SOURCES.md](docs/design/SOURCES.md)): same two-category approach
as VN. Banking/financial-regulation sources sweep all + `scope.Match`; general national-law sources
use per-country keywords to avoid crawling irrelevant documents.

#### Indonesia (`rendang`) — REVIVED & LIVE (2026-07-12)

**Shipped:** `ojk` source (all 979 docs discovered on the first sweep; 285 in scope) + bpk keyword
slices (21 Indonesian terms) → fresh local crawl **2,555 silver docs / 106,385 chunks** (8× the old
corpus) → dual-GPU Kaggle embed (~40 min kernel on T4×2 — the new two-worker kernel halves time and
quota) → RDS restore → **eval 75.0% recall / 62.4 MRR / 100% current-law / 93.5% abstention** (31
all-Indonesian cases; no recorded pre-revival baseline — directional gate) → third ECS container +
CloudFront `E13YTOQ72099BJ` → `rendang.danny.vn` cut over → Haiku well-test PASS (language rule
verified live, POJK rank-1 hits, out-of-scope abstains). **Known issues (tracked):** search latency
11–15 s vs VN ~4 s (2.2× corpus BM25 seq scan + shared t4g.small RDS + 3-way origin CPU sharing —
perf pass planned); PADG 15/2024 (revoked) missing from corpus (BI delisted it; 1 recall + 1
abstention golden failure); 4 further honest golden failures (2 PBI payment ranking, p2sk, cloud
outsourcing); 61 mojibake chunks + 5 needs-review docs via quality_gaps. Origin SG now one
8081-8083 prefix-list rule (per-rule quota: a second prefix-list rule exceeds the SG's 60-entry
limit). Original revival plan below.

#### ID discovery + scope rebuild — SUPERSEDES the keyword model (2026-07-13)

The revival's bpk keyword slices (above) were **wrong** and silently corrupted the ID corpus. Root
causes, all verified live against `peraturan.bpk.go.id` and the `rendang` DB:

1. **Wrong param name.** We sent `&keyword=`; BPK's fields are `&keywords=` (full text) and
   `&tentang=` (title). BPK **ignores an unrecognized param and returns the FULL listing** —
   `jenis=8&keyword=bank%20indonesia` → 1,926 (every UU), `&keywords=` → 573.
2. **Keyword slices bypass `scope.Match`** (the pipeline trusts the server to have filtered). With (1)
   this enqueued the entire UU/PP/Perpres/PMK catalogue as in-scope → the corpus became **68% irrelevant
   national law** (3,533 UU / 77,627 chunks, incl. 365 laws that merely create regencies), while serving
   only **112 of BPK's 503 POJK**.
3. **The tech-heavy scope vocabulary gutted the regulators.** No bare `bank`, no `keuangan` → it
   discarded POJK 44/2024 *Rahasia Bank* and POJK 30/2024 *Konglomerasi Keuangan*. Bank Indonesia:
   **899 regulations discovered, 78 kept**.

**Shipped (this rebuild):**
- **bpk is sweep-only** — one pass over all 12 jenis; `Discover` now **rejects** a keyword. Measured:
  one sweep ≈ 1.4k listing pages vs ≈ 9k across the overlapping keyword slices, so it is cheaper *and*
  complete. All bpk rows removed from `discovery_keyword.csv`.
- **Scope by issuer mandate** (config, not code): OJK/BI/LPS/PPATK/BSSN regulate nothing outside our
  domain → in scope by construction, via issuer codes as `strong` terms in `scope_term_id.csv`
  (matched on the doc number). Broad-mandate issuers (UU/PP/Perpres/PMK/Kominfo/Komdigi — also
  agriculture, customs, broadcast) stay vocabulary-filtered. Locked by `pkg/scope/vocabulary_id_test.go`.
- **Partial-failure contract** — bpk/bnm/sc `Discover` now return an error if ANY sub-unit fails, so the
  cursor never advances over unseen docs (a swallowed jenis failure had cost 304 POJK).
- **`-discover -force`** — full rescan ignoring the stored watermark.
- **Corpus purged and rebuilt** (config preserved). Post-rebuild numbers + re-eval pending; the
  75.0%/62.4 figures above were measured on the polluted corpus and must be re-baselined.

**Open:** VN/MY use the same "regulator sweep → tech vocabulary" model, so they may drop non-tech
banking regulation too (VN evals 83.3% only because its golden set is tech-focused). Both are LIVE —
do not change without sign-off; assess separately.

#### (original revival plan, 2026-07-12 — keyword model superseded, see above)

Maintainer call: improve ID sources and revive. Structure spikes live-verified 2026-07-12
(details in [INDONESIA.md](docs/design/jurisdictions/INDONESIA.md)):
1. **Add `ojk` source** — `jdih.ojk.go.id` DataTables JSON API; 560 POJK + 407 SEOJK + 12 UU;
   explicit repeal graph + partial-repeal status; born-digital ungated PDFs; no robots
   restrictions. Authoritative origin, richer than the bpk mirror. Sweep-all.
2. **bpk keyword slices** — generalize `DiscoverSlices` beyond vbpl; Indonesian
   `discovery_keyword` seed terms for the UU/PP/Permen national sweep.
3. **komdigi rejected** (robots disallows downloads + blocks AI crawlers; thin relevant volume;
   its Permen arrive via bpk keywords). `peraturan.go.id` still blocked.
4. Then: fresh local rendang crawl (local podman `rendang` DB retains 315 silver docs / 27,419
   chunks as warm file-cache start; embeddings are old BGE → full Qwen3 re-embed on Kaggle) →
   `golden_id.json` eval on the dev-EC2 pattern → third ECS container + CloudFront distribution.
Corpus archive snapshot `banhmi-pre-rendang-drop-20260711` was DELETED 2026-07-13 — the ID corpus
was rebuilt from source (the archived one was 68% junk), and DR is covered by RDS automated
backups (7-day retention + point-in-time recovery).

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
  SSM write-path secrets; GCP Cloud Build configs dropped. VN LZ run validated stages 1–4 on real
  rows; embed OOM.
- **2026-07-11** — **embed kernel fixed** (two-budget shape-bucketed batching, `8266f0b`); local
  Qwen3 corpora built (`banhmi_q3` 49,302, `laksa_q3` 8,996 — embedded + lexindexed). **ID
  decommissioned** (service, hosting site, DBs — archived in snapshot — bucket, ECR replica).
  **Pivot: write path parked, read-path-first** — v0.3.0 finishes via local test → dump/restore →
  AWS deploy → migrate.

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
  (VN Hanoi LZ, MY ap-southeast-5); that EC2 write path is itself **parked** since 2026-07-11
  (local runs + dump/restore serve v0.3.0).
- **Indonesia (rendang)** — decommissioned 2026-07-11 (maintainer call: no ID support now). Code
  dormant; archive snapshot deleted 2026-07-13 (corpus rebuilt from source; automated backups cover DR).
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
| **Deploy shape** | Write = local runs now (EC2 in-country VN/MY parked) + Kaggle T4 (embed), per-region S3 file cache → RDS ← ECS on EC2 (ONNX, ARM64) ← CloudFront | same-VPC read; in-country write (geo-locks, polite crawls); free GPU |
| **Read-path-first (2026-07-11)** | park AWS write path; ID decommissioned; local corpora dump/restore to RDS; ship read path VN+MY | finish v0.3.0 sooner; effort where the product is |
| **Per-region S3 data buckets** | `danny-banhmi-data-{vn,my}` beside each pipeline; flat keys, seeded once from GCS | cache next to the compute; no cross-region chatter |
| **Write-path secrets in SSM** | SecureString + EC2 instance role, single-homed `ap-southeast-1`; GCP SM retires at cutover | free tier; least moving parts |
| **Kaggle-only embedding** | Free T4 GPU, fresh GPU per run, dataset-based I/O. Cloud Run L4 GPU dropped | simpler, free, no memory fragmentation |
| **Temporal removed** | `cmd/pipeline` calls activity methods directly | simplify; no durable workflow needed |
| **Hybrid retrieval** | dense + native pgvector `sparsevec` BM25 + RRF + query router | beats vector-only on eval; single datastore; RDS-portable |
| **Qwen3-Embedding-0.6B FP16** | 1024 dims, 32K context, ONNX FP16 everywhere; ~2.3 GB private RAM per serving process (no cross-process sharing — measured) | FP16 for GPU compat; index/query parity |
| **Read path to AWS** | CloudFront + ECS on EC2 t4g.large (ARM64), 2 containers, host networking | always-on; same VPC as RDS; scales to ALB+Fargate later |
| **No local bulk embed** | Kaggle GPU only — never on the dev laptop (8 GB) | protect the dev machine |
| PDF engine | go-fitz (MuPDF via purego). MarkItDown removed | zero-Python; 15–60× faster |
| OCR | Document AI Enterprise OCR (GCS-cached, default). EasyOCR as fallback | cleaner text, no local CPU |
| No composite primary keys | surrogate identity PKs; business keys `UNIQUE` | idempotent `ON CONFLICT` upserts |
| Pre-release migrations | mutable until first tagged release, then append-only | no fix-up migrations pre-v1 |
| Validity-aware retrieval | current-law leads; non-current appended, badged, capped | never present repealed law as current |
