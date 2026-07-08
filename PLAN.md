# banhmi plan

Living roadmap and progress tracker. Architecture detail in [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md);
conventions and the canonical agent guide in [`CLAUDE.md`](CLAUDE.md); the multi-country model in
[`docs/design/jurisdictions/`](docs/design/jurisdictions/). Last updated: 2026-07-08.

## Vision

A self-hostable, **multi-country** platform for Southeast-Asian banking **digital/technology**
regulation: one codebase that crawls each country's official sources, builds a clean, citable corpus in
that country's binding legal language, and **serves it as evidence over MCP** — exact native citations
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

- **Write path — split CPU/GPU on GCP** (`asia-southeast1`), one jurisdiction per run
  (`BANHMI_JURISDICTION`). Pipeline steps (discover, fetch, extract, normalize, index, lexindex) are
  **CPU-only** — run locally or as a **Cloud Run CPU Job** (free tier). Bulk embedding offloads to
  the **Cloud Run L4 GPU `banhmi-embedder`** (`embed.engine=cloudrun`, Qwen3-Embedding-0.6B ONNX
  FP16, scale-to-zero ~$1/hr active) via `POST /embed`. Kaggle is the free GPU fallback
  (`embed.engine=kaggle`). CPU pipeline never needs a GPU — extraction is go-fitz (fast, zero-Python),
  OCR is Document AI (GCS-cached, async). **Pipeline runner:** `cmd/pipeline` (no Temporal); calls
  activity methods directly with structured slog output.
- **DB — AWS RDS PostgreSQL 17 + pgvector/HNSW** (`ap-southeast-1`), **one database per country** on
  one instance (`banhmi`, `laksa`, `rendang`). TLS-required, password-gated.
- **Read path (current prod) — GCP Cloud Run** (`asia-southeast1`), one scale-to-zero service per
  country, in-process BGE-M3 query embedder (OpenVINO INT8). Firebase Hosting domains.
  **v0.3.0 migrates to AWS** (CloudFront + ECS on EC2) — see below.
- **Read path (v0.3.0) — AWS** (`ap-southeast-1`), CloudFront + ECS on EC2 (ARM64 Graviton),
  in-process Qwen3-Embedding ONNX FP16 query embedder. Always-on, same VPC as RDS.
- **Retrieval — hybrid** (single datastore): dense vectors + **BM25 sparse vectors** (pgvector
  `sparsevec`, `cmd/lexindex`) fused with RRF + a deterministic query router. No ParadeDB/`pg_search`.

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

### Phase 0 — expansion pre-work

Items 1–4 done.

1. **Jurisdiction seam registry — DONE.** `pkg/base/jurisdiction` replaces the scattered 2-way
   `vn`/`my` switches with one `Descriptor` registry (sources, parser, OCR languages, validity default,
   router profile, seed/golden files, DB name); VN is the compiled fallback. See
   [playbook](docs/design/jurisdictions/PLAYBOOK.md#seam-registry--shipped).
2. **VN prod data quality — DONE.** Mojibake re-processed locally (doc 200 clean, 47,504 chunks),
   corpus synced to RDS, MCP redeployed with `bm25_score` + version tracking (`v0.1.0-20260704`).
3. **MY (laksa) hybrid — DONE.** `lexindex` built 8,425 sparse vectors, eval passes (recall 95%,
   mrr 82.1%, current-law+abstention 100% — pre-expansion golden set), hybrid deployed to `laksa.danny.vn/mcp` with `bm25_score`
   + version. **Remaining:** P.U. relation-target backfill (1,000 stubs), 8 needs_review docs
   (agclom PDFs with null markdown), layout-aware Section titles.
4. **Indonesia (rendang) — DONE, LIVE.** Sources, parser, registry entry, scope vocabulary, MCP brief,
   golden set (31 cases) — all coded and deployed. See
   [INDONESIA](docs/design/jurisdictions/INDONESIA.md).
5. **Validity/amendment refresh re-crawl.** Scheduled per-country status refresh so
   replaced/amended docs can't keep a stale `in_force`.
6. **Eval as the permanent gate.** Grow per-jurisdiction golden sets (`golden.json`, `golden_my.json`,
   `golden_id.json`); every retrieval/ingestion change ships with an eval delta. Realistic user
   phrasing only.
7. **Drift & quality monitoring.** Track abstain rate, gaps, validity-unknown, embedding coverage,
   corpus counts over time; alert on regression.

### Countries #4–#5 — sequential, one at a time

**Ingest is the only per-country work** — source crawlers, structure parser (if the citation model
differs), scope vocabulary, MCP brief, registry entry, and eval golden file. Everything downstream is
shared: extract → normalize → index → embed → lexindex → MCP serve all run unchanged through
`cmd/pipeline -run-all` on the same codebase. After ingest validates locally, deploy is:
pipeline writes directly to RDS (v0.3.0) + new ECS container on EC2 + CloudFront distribution.

Each country follows the [playbook phase template](docs/design/jurisdictions/PLAYBOOK.md#phase-template-per-country).
Build order: **SG → TH** (recommended; maintainer's call).

- **#4 🇸🇬 Singapore (`kaya`, proposed).** Sources (candidates): MAS (Notices binding + Guidelines),
  SSO (consolidated Acts in **HTML** — best structure since VBPL), scoped PDPC/CSA. English corpus;
  MY citation family near-reuses. Gate: SSO bot-protection/ToS compliance check. Instrument-class
  badging (Notice vs Guideline) must be explicit.
- **#5 🇹🇭 Thailand (`tomyum`, proposed).** Sources (candidates): BOT notifications, Krisdika
  consolidated Acts, Royal Gazette signal (+ scoped PDPC/ETDA/SEC). Thai corpus; มาตรา/วรรค + ข้อ
  models. **Heaviest language work:** Thai has no word spaces → the BM25 hashing tokenizer needs a
  segmentation decision (dictionary segmenter vs char-n-grams vs vector-primary interim); B.E.↔C.E.
  date normalization; Thai numerals. Last because of the language complexity.

### Phase 0.3 — Document AI OCR — DONE

**Status: DONE, rolled out.** GCP Document AI Enterprise OCR (`banhmi-ocr` processor,
`asia-southeast1`) replaces EasyOCR as the default OCR engine for all jurisdictions.
`pkg/extract/docai/` — GCS-cached (bucket `gs://danny-banhmi-docai`). Config:
`extract.ocr.engine: documentai`. EasyOCR remains available as `auto`/`local`/`kaggle` fallback
for offline setups.

**Design:** go-fitz first (local, free) → content gate → gate fails → Document AI OCR via GCS
cache (check silver → check GCS output → upload → batchProcess LRO → write silver). $1.50/1K pages.

### Phase 0.4 — go-fitz (MuPDF) replaces MarkItDown — DONE

**Status: DONE, rolled out.** go-fitz (`github.com/gen2brain/go-fitz`, purego, no CGO) replaces
MarkItDown for all document formats. Python eliminated from the extraction hot path.

**Extraction cascade (zero-Python):**
1. **PDF** → go-fitz `Text()` per page (~1ms/page) → content gate
2. **DOCX** → go-fitz `Text()` → content gate
3. **HTML** → go-fitz `Text()` → content gate
4. **DOC** → LibreOffice `soffice --headless --convert-to docx` → go-fitz on the DOCX → content gate
5. Gate fails → Document AI OCR (GCS-cached)

**Notes:** go-fitz returns 0 chars on scanner+signature-only PDFs — correctly detected by the gate
and routed to Document AI OCR. Pure Go DOC parsers (`doc2txt`, `cat`) produce garbage on real VN
government DOC files; LibreOffice remains the only reliable DOC reader (DOC→DOCX, not DOC→PDF).

### v0.3.0 — AWS read path, Qwen3-Embedding, ONNX everywhere

**Status: Phase A DONE (2026-07-05), Phase B redesigned (2026-07-08).** Three changes:
1. **Read path migrates from GCP Cloud Run to AWS** — CloudFront + ECS on EC2 (ARM64 Graviton).
   Eliminates cross-cloud latency (GCP→AWS), cold starts, and Firebase Hosting dependency.
2. **Embedder switches from BGE-M3 to Qwen3-Embedding-0.6B** (`onnx-community/Qwen3-Embedding-0.6B-ONNX`,
   **FP16**, 1.2 GB). Same 1024 dims (pgvector schema unchanged), 32K context (vs 8K). Full
   re-embed of all corpora required (vectors are incompatible). FP16 chosen over INT8 because
   ONNX INT8 dynamic quantization (`MatMulInteger`/`DynamicQuantizeLinear`) has no CUDA kernels
   — falls back to CPU with 420 memcpy nodes, making GPU useless. FP16 has full CUDA support.
3. **ONNX Runtime everywhere** (query + bulk). OpenVINO removed.

**Temporal removed** — `cmd/pipeline` calls activity methods directly (shipped 2026-07-06).

**Architecture:**
```
READ PATH — AWS (ap-southeast-1), always-on:
  User → CloudFront (3 distributions, ACM TLS, *.danny.vn)
           ├── banhmi.danny.vn  → origin :8081
           ├── laksa.danny.vn   → origin :8082
           └── rendang.danny.vn → origin :8083
           ↓
         EC2 t4g.medium (2 vCPU / 4 GB, Graviton ARM64, Elastic IP)
         ECS cluster (1 instance, host networking)
           ├── banhmi-mcp  :8081  (BANHMI_JURISDICTION=vn)
           ├── laksa-mcp   :8082  (BANHMI_JURISDICTION=my)
           └── rendang-mcp :8083  (BANHMI_JURISDICTION=id)
           ↓ (same VPC, SCRAM-SHA-256 + TLS)
         RDS PostgreSQL 17 + pgvector (ap-southeast-1)

  All 3 containers from one ARM64 image. In-process Qwen3-Embedding
  ONNX FP16 query embedder. Model uses external data format
  (model_fp16.onnx + model_fp16.onnx_data, 1.2 GB total); ORT
  mmap's the data file — 3 containers share the same physical
  pages via page cache. Budget ~1.2 GB shared + ~100 MB per
  container for Go runtime + inference spike. 4 GB suffices.

WRITE PATH — GCP (asia-southeast1), CPU/GPU split:
  Pipeline (CPU) — local or Cloud Run CPU Job (free tier):
    cmd/pipeline -run-all (per jurisdiction)
    discover → fetch → extract → normalize → index → lexindex
    All CPU: go-fitz extraction (~1ms/page), Document AI OCR
    (GCS-cached, async). No GPU, no ONNX model baked in.

  Embedder (GPU) — Cloud Run L4, scale-to-zero:
    banhmi-embedder service, cmd/pipeline -serve-embed :8080
    Qwen3-Embedding ONNX FP16 + CUDA on L4 GPU.
    Pipeline calls POST /embed via embed.engine=cloudrun.
    ~$1/hr active, $0 idle. Same model as read path.
    Containerfile: Containerfile.embed-job.onnx.

  Embed fallback — Kaggle T4 (free GPU):
    Python kernel (kernel_embed.py), embed.engine=kaggle.
    Same Qwen3-Embedding ONNX FP16, onnxruntime-gpu.
    Kaggle dataset: danhsoftware/qwen3-embedding-06b-onnx-fp16.
```

**Key design decisions:**
- **Read path moves to AWS (2026-07-08).** Re-evaluated: Cloud Run's cold start (6-8s per
  request after idle) and cross-cloud latency (GCP→AWS ~10-20ms) are worse than always-on in the
  same VPC as RDS (<1ms). $25/mo for always-on with zero cold starts is worth it. CloudFront +
  ECS on EC2 (no ALB) is the simplest path; scales later by adding ALB + Fargate.
- **3 CloudFront distributions, custom origin ports.** Each domain routes to a different port on
  the origin DNS name (`origin.danny.vn` A record → Elastic IP; CloudFront requires a DNS
  name, not a raw IP). Custom origin header (`X-Origin-Verify: <secret>`) — the prefix list
  alone admits any AWS customer's distribution. Server-side enforcement planned (step 18).
  Origin-response timeout 60s (default 30s too short for SSE streams).
- **ARM64 (Graviton).** ~20% better price/performance. The Containerfile downloads `aarch64` ONNX
  Runtime and `arm64` tokenizer libs. Same distroless base (has arm64 variants).
- **Qwen3-Embedding-0.6B replaces BGE-M3 (2026-07-08).** 0.6B params, 1024 dims (same as BGE-M3),
  32K context (4× BGE-M3). **FP16** model 1.2 GB (external data format: `model_fp16.onnx` 584 KB +
  `model_fp16.onnx_data` 1.2 GB). FP16 over INT8: ONNX INT8 dynamic quantization has no CUDA
  kernels, GPU unusable; FP16 has full CUDA support on T4/L4. ORT mmap's the external data file,
  so 3 ECS containers share physical pages — 4 GB t4g.medium suffices. Full re-embed required.
- **XFF compatibility.** CloudFront appends client IP as the last X-Forwarded-For entry — same
  convention as Cloud Run. `BANHMI_TRUST_PROXY=true` works without code changes.
- **Security group.** Ports 8081-8083 open to CloudFront managed prefix list only
  (`com.amazonaws.global.cloudfront.origin-facing`). SSH (22) from maintainer IP.
- **Stateless MCP.** Each request is self-contained — no in-memory session map.
- **RDS with SCRAM+TLS.** ECS connects within the same VPC (private subnet → RDS, no internet hop).
- **Direct writes, no dump/restore.** The pipeline writes directly to RDS — no intermediate local
  database, no pg_dump/pg_restore cycle. PostgreSQL MVCC handles concurrent reads + writes.
- **DB password.** Move from GCP Secret Manager to AWS Secrets Manager (or SSM Parameter Store) —
  ECS can inject secrets natively. Decide during implementation.

**Phase A — local validation — DONE (2026-07-05):**
1. **ONNX build + server startup — DONE.** `-tags onnx` compiles clean. Server starts and loads
   the ONNX INT8 model in-process.
2. **ONNX embedder code fixed — DONE.** Output name `dense_vecs` → `last_hidden_state`, added
   CLS pooling + L2 norm in Go.
3. **ONNX model exported + quantized — DONE.** INT8 dynamic quantization.
4. **Eval (hybrid, BGE-M3) — DONE, no recall regression.** Recall matches OV baseline exactly
   (VN 85.7%, MY 95-100%). MRR ±5% from index-query embedder mismatch.
5. **Makefile targets — DONE.** `make eval-onnx` and `make mcp-onnx` for local testing.
6. **Temporal removed — DONE.** `cmd/pipeline` replaces `cmd/worker`; calls activity methods
   directly with structured slog output.

**Retrieval quality improvements — DONE (completed 2026-07-05):**
- **Bilingual MCP scope (VN, ID).** English scope terms added; MCP API fields renamed to English.
- **Native-language MCP guidance.** VN and ID briefs instruct agents to translate queries.
- **Golden sets expanded.** VN: 54 cases, MY: 51 cases.
- **Re-embed all corpora (BGE-M3).** VN 47,587 + MY 8,425 chunks re-embedded on Kaggle T4
  (PyTorch FP16 BGE-M3, CLS+L2). Cloud Run L4 embed job Containerfile also shipped.
- **Golden set + scope term corrections.** Fixed 12 wrong article expectations in VN golden set.
  Post-fix baselines: VN recall 75.9% / MRR 60.0%, MY recall 85.4% / MRR 73.6% (both current-law
  100%).
- **Corpus gap fix.** `MatchFolded` diacritics-folded rescue in the scope gate. 721→723 primary
  docs; `52/2024/NĐ-CP` and `15/2020/NĐ-CP` now have chunks.

**Phase B — Qwen3-Embedding + AWS deploy:**

*Completed (steps 7–12):*

7. **Stateless MCP — DONE.** Each request is self-contained; no in-memory session map.
8. **Qwen3 instruction prefix research — DONE.** Qwen3-Embedding is asymmetric: queries get
   `Instruct: <task>\nQuery:<text>`, documents get no prefix. `embed.FormatQuery()` and
   `embed.Qwen3QueryPrefix` added to `pkg/rag/embed/embed.go`. Pooling: last-token (not CLS),
   then L2 normalize. Max 32K tokens (query capped at 512).
9. **Kaggle dataset + kernel — DONE.** `kernel_embed.py` rewritten from PyTorch/BGE-M3 to
   `onnxruntime`/Qwen3-Embedding-0.6B ONNX FP16. Last-token pooling + L2 normalize. No
   instruction prefix (documents only). Kaggle dataset
   `danhsoftware/qwen3-embedding-06b-onnx-fp16` (FP16: `model_fp16.onnx` +
   `model_fp16.onnx_data` ~1.2 GB + `tokenizer.json`). Dataset created via mirror kernel.
10. **ARM64 Containerfile + cache scripts — DONE.** `Containerfile.ecs.onnx` (ARM64
    Graviton): downloads `onnxruntime-linux-aarch64`, `libtokenizers.linux-arm64`,
    Qwen3-Embedding model. Includes `/healthcheck` binary for ECS (distroless, no shell).
    `Containerfile.cloudrun.onnx` and `Containerfile.embed-job.onnx` also updated for
    Qwen3 + cache support. All Containerfiles accept `CACHE_BASE` build arg to pull
    dependencies from cloud cache instead of upstream. ORT 1.26.0 (1.27+ needs CUDA 13).
    **Cache scripts:** `deploy/aws/cache-build-deps.sh` (S3, both aarch64+x64),
    `deploy/gcp/cache-build-deps.sh` (GCS, x64 only). Run once to seed the cache;
    Containerfile builds then use `--build-arg CACHE_BASE=<url>`.
11. **AWS infra IaC prep — DONE.** `deploy/aws/` contains ready-to-apply configs:
    `ecs-task-definition.json` (3 containers, host networking, Secrets Manager injection),
    `cloudfront-config.json` (template: custom origin, X-Origin-Verify, 60s timeout, no cache),
    `security-group.json` (8081-8083 from CF prefix list, SSH from maintainer IP),
    `create-distributions.sh` (creates all 3 distributions from template),
    `setup-checklist.md` (13-step provisioning guide with CLI commands, costs, rollback).
    All use `YOUR_*` placeholders — no secrets committed.
12. **Integrate Qwen3-Embedding-0.6B ONNX — DONE (coded, 2026-07-08).** Rewrote
    `pkg/rag/embed/onnxembed/` to use `github.com/dannyota/onnxruntime/go` (tag `go/v1.28.1`,
    forked from `microsoft/onnxruntime`; CGO bindings, API fallback 27→17).
    - `go.mod`: `require github.com/microsoft/onnxruntime/go v1.28.1` +
      `replace => github.com/dannyota/onnxruntime/go v1.28.1`.
    - **Decoder model**: 59 inputs (`input_ids` + `attention_mask` + `position_ids` + 28×2
      empty KV cache tensors); output `last_hidden_state` [1, seq, 1024].
    - **FP16 model** (`model_fp16.onnx` + `model_fp16.onnx_data`, 1.2 GB). Switched from
      INT8: ONNX INT8 dynamic quantization (`MatMulInteger`/`DynamicQuantizeLinear`) has no
      CUDA kernels — falls back to CPU with 420 memcpy nodes. FP16 has full CUDA support.
      Same 59 inputs, same output shape. KV cache dtype FP16.
    - **Last-token pooling** (not CLS) + L2 normalize; context support for cancellation.
    - **Tokenizer** (`daulet/tokenizers`) unchanged — BPE, `tokenizer.json`, EOS auto-appended.
    - **Instruction prefix** from step 8 (`embed.FormatQuery`).
    - **ORT 1.26.0** (`libonnxruntime.so`). ORT 1.27+ GPU requires CUDA 13; 1.26.0 is the
      last CUDA 12 version. Go bindings v1.28.1 fall back gracefully (API 27→17).
    - ORT mmap's the external data file — 3 ECS containers share physical pages via page
      cache. t4g.medium (4 GB) suffices.

*Completed (steps 13–15b):*

13. **Validate Qwen3 Go embedder — DONE.** FP16 smoke test passed: 1024-dim, L2=1.0,
    deterministic, relevance correct. ORT 1.26.0 CPU.
14. **Kaggle dataset + kernel — DONE.** Padding bug fixed; FP16 dataset created
    (`danhsoftware/qwen3-embedding-06b-onnx-fp16`, mirror kernel, zero local bandwidth).
15. **INT8 → FP16 switch — DONE.** INT8 dynamic quantization has no CUDA kernels (420
    memcpy nodes, 65s/batch = ~7h). FP16 has full CUDA support. All code, Containerfiles,
    cache scripts, ECS task definition updated. Model cached server-side (S3 via Lambda,
    GCS via Cloud Run Job).
15b. **Cloud Run L4 embedder redeployed — DONE.** `banhmi-embedder` rebuilt with FP16
    (`Containerfile.embed-job.onnx`, Cloud Build), Artifact Registry `embedder:fp16`.
    `EmbedModel` updated to `qwen3-embedding-0.6b`. Security hardened: `MaxBytesReader`
    (10 MB), `WriteTimeout`, per-text 32K limit, sanitized errors. Cloud Run:
    `--concurrency=1`, `--max-instances=2`, `--min-instances=0`, scale-to-zero.
    Build cache docs: `deploy/BUILD-CACHE.md`.

*Completed (steps 15c–15h):*

15c. **GCP service account separation — DONE.** Created `banhmi-pipeline-dev` SA
    (least privilege: `run.invoker` on embedder + `documentai.apiUser` +
    `storage.objectAdmin`). Migrated all roles from `banhmi-cli` GCP SA (now
    inert, zero roles). Deleted `banhmi-cli`'s never-expiring key. SA key in
    `.claude/pipeline-dev-sa.json` (gitignored). Pattern: IAM binding for
    cloud-to-cloud (no keys), SA key only for local/off-GCP testing.
15d. **IAM/auth documentation — DONE.** `docs/DEPLOYMENT.md` and
    `docs/DEVELOPMENT.md` rewritten for current architecture (Qwen3, go-fitz,
    `cmd/pipeline`, no Temporal). Added "IAM and service accounts" section
    covering both GCP SAs and AWS IAM roles with least-privilege rules.
    `deploy/aws/setup-checklist.md` expanded: EC2 instance role, explicit
    "no task role" rationale.
15e. **Security hardening — DONE.** Fable review (H1–H3, M1–M6, L1–L3):
    DNS fallback resolver (`pkg/base/dns`), Cloud Run embed retry+timeout,
    `.containerignore` secrets exclusion, fetch transport error retry, TLS
    connection leak fix, streaming embed batches, continue-on-batch-failure,
    `BANHMI_EMBED_TOKEN` app-level auth, Containerfile checksum ARGs,
    ECS readonlyRootFilesystem + drop ALL capabilities.
15f. **WAF container testing — DONE.** Tested Cloudflare (BPK) and AWS WAF
    (BNM) minting in container. Results: Debian `chromium` crashes without
    dbus (exit 133); Google Chrome works. Headless: BNM passes, BPK fails
    (Cloudflare detects `--headless=new`). Xvfb + Google Chrome: both pass.
    Solution: `google-chrome-stable` + `xvfb` + `DISPLAY=:99` in pipeline
    Containerfile.
15g. **Pipeline Containerfile updated — DONE.** Google Chrome (from Google apt
    repo) + Xvfb + xauth added. `BANHMI_CHROME_PATH` env set. chromedp
    `DBUS_SESSION_BUS_ADDRESS=disabled:` env + container-safe Chrome flags.
15h. **Debug logging — DONE.** Stage entry/progress logging in `run-all`
    (stage 1/6–6/6), batch progress in `countStageAll`, debug logging in
    Cloudflare/AWS WAF minters (Chrome startup, cookie wait), embed batch
    progress. Controlled via `BANHMI_LOG_LEVEL=debug`.
15i. **Containerfile libmupdf fix — DONE.** Debian ships MuPDF as static `.a`
    only; go-fitz purego needs `.so`. Added `mupdf` build stage that creates
    `libmupdf.so` from the static archives with `gcc -shared`. Also fixed
    Xvfb `/tmp/.X11-unix` directory creation.

*Remaining — container test → GCS cache → re-embed → eval → deploy:*

15j. **GCS fetch cache.** Cache downloaded source files (PDFs/DOCX) in GCS
    (`gs://danny-banhmi-docai/fetch/` or a dedicated bucket). During fetch,
    check GCS by content hash before downloading from the source site. On
    miss, download from source and upload to GCS. Fresh staging DBs skip
    most network crawling because the files are already cached. Saves
    bandwidth and time on re-runs and container deploys.
16. **Re-embed all corpora** (VN + MY + ID) with Qwen3-Embedding FP16. Full
    pipeline in container against staging DBs. Embedding offloads to
    **Cloud Run L4** (`embed.engine=cloudrun`) via `POST /embed` on the
    redeployed `banhmi-embedder` (with `BANHMI_EMBED_TOKEN` auth). Write
    to staging DBs only. **HNSW index rebuild** after re-embed.
17. **Eval on all 3 golden sets** against the Qwen3 local corpus — `make eval-onnx` (VN, MY,
    ID). Must match or beat BGE-M3 baselines. Record deltas. *Gates everything downstream.*
18. **Code remaining read path.** X-Origin-Verify middleware in Go (currently only in
    CloudFront config, not enforced server-side). Update doc comments. Build and push ARM64
    image to ECR.
19. **Review** — code + plan.
20. **Deploy AWS infra.** Provision EC2 + ECS + CloudFront from step 11 IaC prep. **RDS
    snapshot** before any corpus changes. Restore Qwen3 corpus into **side-by-side databases**
    (`banhmi_q3`, `laksa_q3`, `rendang_q3`) — NOT into the live DBs (GCP still serves BGE-M3).
    Point ECS task definitions at the new DBs. Verify all 3 endpoints via CloudFront domains.
21. **Profile memory on the real EC2** — before DNS switch, zero user traffic. Measure peak
    RSS per container during ONNX inference (decoder model with KV cache outputs). FP16
    external data format (`model_fp16.onnx_data`, 1.2 GB) should be mmap'd by ORT — verify
    that 3 containers share physical pages via page cache (check with `smem` or
    `/proc/PID/smaps`). If mmap works: ~1.2 GB shared + ~100 MB per container = ~1.5 GB
    total, well within t4g.medium (4 GB). If ORT copies into arena instead: 3 × 1.2 GB
    won't fit — resize to t4g.large (8 GB, ~$49/mo).
22. **DNS cutover + bake.**
    a. Update DNS: `*.danny.vn` CNAMEs from Firebase → CloudFront distribution domains.
    b. Run Haiku-over-MCP smoke test against all 3 endpoints.
    c. **Bake period** (24-48h) — monitor before teardown.
    d. Tear down GCP Cloud Run services + Firebase Hosting sites.
    e. Drop old BGE-M3 databases on RDS; rename `*_q3` databases to final names.
    f. Move DB password from GCP Secret Manager to AWS Secrets Manager / SSM.

*Docs:*
- `docs/DEPLOYMENT.md` — **DONE.** Rewritten: pipeline/DB/MCP parts, Qwen3, go-fitz, IAM/SA section.
- `docs/DEVELOPMENT.md` — **DONE.** Rewritten: cmd/pipeline, ONNX embedder, SA setup, no Temporal.
- `docs/ARCHITECTURE.md` — update deployment shape, embedder references (in progress).
- `CLAUDE.md` — update deployment shape, embedder, ARM64 notes.
- `README.md` — update any Cloud Run / Firebase / BGE-M3 references.
- `cmd/server/main.go` — update doc comment (no longer Cloud Run).

**After deploy — memory tuning + monitoring:**

- **Container memory limits**: set hard ECS `memory` per container from step 21 data.
- **Concurrency cap**: the Go embedder serializes with `sync.Mutex` (1 concurrent inference).
  Keep it unless profiling shows headroom for bounded concurrency.
- **Benchmark under load**: simulate concurrent MCP queries, monitor RSS per container,
  measure p99 latency. Tune ORT arena size and container limits from real data.
- **CloudWatch alarms**: replace GCP $5/mo budget alert. Memory/CPU alarms on the EC2 instance.

**After memory tuning:** cross-encoder reranker evaluation on the expanded golden sets.

**Cost estimate (monthly, post-v0.3.0):**

| Component | Cost |
|---|---|
| EC2 t4g.medium (2 vCPU/4 GB, on-demand) | ~$25 |
| Elastic IP (IPv4, attached) | ~$3.60 |
| EBS root volume (~8 GB gp3) | ~$3 |
| CloudFront (3 distributions, low traffic) | ~$1-2 |
| ECR image storage | ~$0.10 |
| ACM certificate | $0 |
| DB (RDS t4g.micro, 3 DBs) | ~$13 |
| Write pipeline CPU (Cloud Run free tier or local) | $0 |
| Embed GPU (Cloud Run L4, scale-to-zero, ~1hr/re-embed) | ~$1 |
| OCR (Document AI) | ~$0.05 |
| File cache (GCS) | ~$0.03 |
| **Total** | **~$47/mo** (drop to ~$37 with 1yr RI) |

FP16 model (1.2 GB) uses external data format; ORT mmap's the weights file so 3 containers
share physical pages. Profile (step 21) confirms fit. If mmap doesn't work: t4g.large
(2 vCPU/8 GB, ~$49/mo) — total ~$71/mo, ~$61 with RI.

**Scaling path (future):** add ALB (~$16/mo) + switch to Fargate or add EC2 instances.
ECS auto-scaling on CPU. Container image unchanged.

### MVP2 candidates (unchanged, deliberately parked)

Gemma 4 E4B OCR enhancement · figure extraction · manual-folder source · crawl depth >1 (scope
decision — would expand toward the whole legal corpus) · `sbv.gov.vn` extra source · Cloud Armor edge
(needs an ~$18/mo LB — only when abuse justifies) · cross-encoder reranker (eval-only today).

## Milestone history (compressed; full detail in git history)

- **2026-05-30 — evidence-only pivot.** Removed the answer LLM and all answer surfaces; MCP = the
  product surface; embedder mandatory. Điểm-aware chunking + hierarchical roll-up; EasyOCR (`vi`)
  replaced Tesseract, batched.
- **2026-05-31 — INPUT hardening.** Priority-0 ingest-flow audit; `RunAll` one-shot orchestrator +
  streaming Kaggle batch; full re-crawl validated (572 docs / 62,350 chunks / 100% embedded);
  deploy-readiness gate MET.
- **2026-06-01 — VN deployed.** AWS RDS (PG17+pgvector, Singapore) + Cloud Run (in-process BGE-M3,
  OpenVINO, distroless) + Firebase Hosting → `banhmi.danny.vn/mcp` live. Deviations: RDS replaced Neon
  (512 MB free cap overflowed); in-process OpenVINO replaced the OVMS CPU sidecar.
- **2026-06-10 — MVP1 completion pass.** `doc_key = <TYPE>|<NUMBER>` identity fix; `relation_context`
  scope gate; OCR-floor serving (badged non-binding); Phụ lục chunking; validity honesty (`unknown`
  class).
- **2026-06-13 — cost fix.** Deleted Cloud Run NAT/router/static IP (~$35/mo); RDS SG opened to
  `0.0.0.0/0` with TLS+password. GCP idle ~$0.
- **2026-06-19/20 — vanban source.** `vanban.chinhphu.vn` built + live-validated (freshest central-law
  feed; caught `134/2025/QH15` AI Law).
- **2026-06-21 — Malaysia built (local).** Jurisdiction seam; agclom/bnm/sc sources; MY PDF Section-tree
  parser; EN OCR; 63 docs · 8,425 chunks · 100% embedded.
- **2026-06-22 — hybrid retrieval + Malaysia deployed.** pgvector `sparsevec` BM25 + RRF + query
  router — recall@k 89.3% / mrr 84.6% / current-law 100%. laksa deployed → `laksa.danny.vn/mcp`.
- **2026-07-02 — mojibake remediation.** UTF-8-forced HTML extraction + Cyrillic mojibake gate. Prod
  remediated 2026-07-04 (doc 200 clean).
- **2026-07-04 — Indonesia coded + Document AI OCR coded.** `rendang` phases 1–6 coded. Document AI OCR
  engine (`pkg/extract/docai/`) coded.
- **2026-07-05 — ONNX Phase A + retrieval quality.** ONNX INT8 query embedder validated (recall
  matches OV baseline). Golden sets expanded (VN 54, MY 51). Corpus gap fix (`MatchFolded`). Re-embed
  on Kaggle T4.
- **2026-07-06 — Indonesia LIVE + Temporal removed.** `rendang.danny.vn/mcp` live. `cmd/pipeline`
  replaces `cmd/worker` (no Temporal dependency).
- **2026-07-08 — Document AI OCR + go-fitz rolled out.** Document AI replaces EasyOCR as default OCR
  engine (GCS-cached, all jurisdictions). go-fitz (MuPDF) replaces MarkItDown — Python eliminated from
  the extraction hot path (15-60× faster). v0.3.0 Phase B: Qwen3-Embedding-0.6B FP16 replaces BGE-M3
  INT8 (FP16 for CUDA compat). Cloud Run L4 embedder redeployed with FP16 + security hardening.
  CPU/GPU split write path (Cloud Run CPU pipeline + L4 embedder). Build cache (S3 + GCS) seeded
  server-side (Lambda + Cloud Run Job). Read path to AWS decided (CloudFront + ECS).

**Do not reopen (settled by bake-offs / paid lessons):** evidence-only surface (no answer LLM);
go-fitz extraction cascade (DOCX→HTML→DOC→PDF) with Document AI OCR fallback (batch-only,
GCS-cached, never inline); `doc_key = <TYPE>|<NUMBER>` identity; hybrid via native pgvector
sparsevec (no ParadeDB/`pg_search` — can't run on RDS); RDS PostgreSQL as the single datastore;
Temporal removed (replaced by direct `cmd/pipeline`); MarkItDown and EasyOCR replaced.

## Deferred / dropped

- **Answer LLM / chat endpoint / web "ask" UI** — dropped; the user's model answers.
- **Watchdog reconcile half** — fetch-lease recovery covers it; resolve-references folds into relations.
- **phapluat.gov.vn** source — dropped for MVP1.
- **Reranker** — eval-only; local rerankers lost to vector-only; revisit on a larger golden set.
- **`bronze.source_document_history`** — dropped; the temporal model is silver `validity_period` +
  `amendment_event`.
- **English/`provision_level` multilingual experiment** — reverted; one language per country.
- **Temporal** — removed (2026-07-06); `cmd/pipeline` calls activities directly.
- **MarkItDown** — replaced by go-fitz (2026-07-08); Python removed from extraction.
- **EasyOCR** — replaced by Document AI as default (2026-07-08); available as offline fallback.
- **Cloud Run + Firebase (read path)** — replaced by CloudFront + ECS on EC2 (v0.3.0, 2026-07-08).
- **BGE-M3** — replaced by Qwen3-Embedding-0.6B (v0.3.0, 2026-07-08).

## Decisions log

| Decision | Choice | Principle |
|----------|--------|-----------|
| **INPUT before OUTPUT** | corpus first, validated on real docs; then the serving surface | data quality is the product |
| **Evidence-only; no answer LLM** | citations/validity/relations/gaps over MCP; user brings the model | we own the data, not the answer |
| **Multi-jurisdiction (2026-06-21)** | jurisdiction = config dimension; **the Postgres DB is the boundary** (one DB per country, same instance until it contends); one image, N deployments | share the core, customize behind interfaces; never fork |
| **One language per country (2026-06-21)** | index/serve/search only the binding native language; never translate; non-binding translations never indexed | translation risks legal error |
| **Food-dish codenames (2026-07-02)** | `banhmi` · `laksa` · `rendang` · proposed `tomyum`/`kaya` (+ domains) | consistent, memorable, per-country identity |
| **Seam registry before #3 (2026-07-02)** | consolidate the 2-way `vn`/`my` switches into one jurisdiction descriptor before adding a third | prevent N-way `case` drift |
| **Deploy shape** (2026-06-01→v0.3.0) | *Current:* worker local → RDS → Cloud Run (OpenVINO) → Firebase. *v0.3.0:* write = Cloud Run CPU (pipeline, free) + Cloud Run L4 (embed, scale-to-zero) → RDS ← ECS on EC2 (ONNX, ARM64) ← CloudFront. Read path to AWS; write stays GCP (GPU + free CPU tier) | same-VPC read; CPU/GPU split write; cost-optimal |
| **Temporal removed (2026-07-06)** | `cmd/pipeline` calls activity methods directly; structured slog output; no Temporal server needed | simplify; Cloud Run Jobs don't need durable workflows |
| **Hybrid retrieval (2026-06-22)** | dense vectors + native pgvector `sparsevec` BM25 + RRF + query router; no `pg_search` | beats vector-only on eval; single datastore; RDS-portable |
| **"Coded" ≠ "validated"** | tracked separately, always | never ship unvalidated extraction as done |
| No hardcoded policy lists | vocab in `config` schema, seeded from CSVs | edit CSV + re-seed, no code change |
| No AI as canonical parser | deterministic extraction; OCR batched, gated, never sole binding source | never generate legal text |
| PDF engine | go-fitz (MuPDF via purego, no CGO). MarkItDown removed. | zero-Python extraction; 15-60× faster |
| OCR | Document AI Enterprise OCR (GCS-cached, default). EasyOCR available as offline fallback. | cleaner text, no local CPU |
| Embedder | *Current deploy:* BGE-M3 OpenVINO INT8 (query) + Kaggle (bulk). *v0.3.0:* **Qwen3-Embedding-0.6B ONNX FP16** everywhere — query (in-process ONNX on ECS), bulk (Cloud Run L4 `banhmi-embedder`), fallback (Kaggle T4). 1024 dims, 32K context, 1.2 GB FP16 (external data, mmap shared). Full re-embed required | better model; FP16 for GPU compat; mmap for memory sharing |
| **FP16 over INT8 (2026-07-08)** | ONNX INT8 dynamic quantization (`MatMulInteger`/`DynamicQuantizeLinear`) has no CUDA kernels — falls back to CPU with 420 memcpy nodes (65s/batch, ~7h for 48K chunks). FP16 has full CUDA support on T4. ORT mmap's external data file — 3 containers share pages | GPU must actually work; mmap solves the memory cost |
| **Read path to AWS (2026-07-08)** | CloudFront + ECS on EC2 t4g.medium (ARM64, 2 vCPU/4 GB), 3 containers, host networking, no ALB. Replaces Cloud Run + Firebase. ~$25/mo compute (vs ~$0 idle Cloud Run) — eliminates cold starts and cross-cloud latency | always-on; same VPC as RDS; scales to ALB+Fargate later |
| **No local bulk embed** | Bulk embedding on Kaggle GPU or Cloud Run L4 only — never on the dev laptop (8 GB, would OOM/overheat) | protect the dev machine |
| No composite primary keys | surrogate identity PKs; business keys `UNIQUE` | idempotent `ON CONFLICT` upserts |
| Containers | podman-first, `Containerfile` | no host installs |
| Pre-release migrations | mutable until first tagged release, then append-only | no fix-up migrations pre-v1 |
| Validity-aware retrieval | current-law leads; non-current appended, badged, capped | never present repealed law as current |
| Relation confidence split | confirmed structured relations ≠ weak text links; weak can't drive validity | evidence the agent can trust |
