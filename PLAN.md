# banhmi plan

Living roadmap and progress tracker. Architecture detail in [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md);
conventions and the canonical agent guide in [`CLAUDE.md`](CLAUDE.md); the multi-country model in
[`docs/design/jurisdictions/`](docs/design/jurisdictions/). Last updated: 2026-07-05.

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
| 3 | 🇮🇩 Indonesia | `rendang`* | rendang.danny.vn* | PROPOSED | [INDONESIA](docs/design/jurisdictions/INDONESIA.md) |
| 4 | 🇸🇬 Singapore | `kaya`* | kaya.danny.vn* | PROPOSED | [SINGAPORE](docs/design/jurisdictions/SINGAPORE.md) |
| 5 | 🇹🇭 Thailand | `tomyum`* | tomyum.danny.vn* | PROPOSED | [THAILAND](docs/design/jurisdictions/THAILAND.md) |

\* codename/domain proposed, **pending maintainer sign-off**. Recommended **build order: ID → SG → TH**
— ID is the largest market with a VN-like citation model (Pasal/ayat ≈ Điều/Khoản); SG is the cheapest
build (English, MY citation family, SSO HTML statute trees); TH last because it carries the heaviest
language work (Thai word segmentation for the lexical arm, Buddhist-Era dates, Thai numerals). Final
order is the maintainer's call.

## Deployment shape (shipped; repeats per country)

- **Worker — local**, one jurisdiction per run (`BANHMI_JURISDICTION`); bulk embedding offloads to a
  **Kaggle GPU** (`embed.engine auto/kaggle`); OCR batch local-CPU or Kaggle.
- **DB — AWS RDS PostgreSQL 17 + pgvector/HNSW** (`ap-southeast-1`), **one database per country** on
  one instance until load says otherwise (`banhmi`, `laksa`, …). TLS-required, password-gated.
- **MCP — GCP Cloud Run** (`asia-southeast1`), **one scale-to-zero service per country**, same image:
  single Go binary with the **in-process OpenVINO BGE-M3** query embedder (`-tags openvino`). ~$0 idle;
  $5/mo budget alert + `--max-instances=3` per service.
- **Domains — Firebase Hosting** (free Spark), one site per country in front of its service.
- **Retrieval — hybrid** (single datastore): dense BGE-M3 + **BM25 sparse vectors** (pgvector
  `sparsevec`, `cmd/lexindex`) fused with RRF + a deterministic query router. No ParadeDB/`pg_search`.

## Current state (live `corpus_status`, 2026-07-04)

**🇻🇳 VN (banhmi) `v0.1.0-20260704`:** 1,608 docs total · **712 indexed** · **47,504 chunks** ·
**100% embedded** · 8,859 confirmed relation edges · `search_ready`. **Hybrid retrieval live** (eval:
recall@k 85.7%, mrr 80.9%, current-law 100%). `bm25_score` per hit live. Mojibake remediated (doc 200
clean). Open gaps (disclosed via `quality_gaps`): 964 unresolved relation targets (deliberate
one-level crawl boundary), 83 needs-review text docs, 27 indexed docs without binding text (badged),
4 docs without current validity. 887 relation-context docs deliberately unindexed.

**🇲🇾 MY (laksa) `v0.1.0-20260704`:** 63 docs · 8,425 chunks · **100% embedded** · **100% sparse** ·
62 in-force + 1 expired · `search_ready`. **Hybrid retrieval live** (eval: recall 95%, mrr 82.1%,
current-law+abstention 100%). `bm25_score` per hit live. Remaining: 1,000 P.U. relation stubs
(unresolved), 8 needs_review docs (agclom PDFs, null markdown), layout-aware Section titles.

## Roadmap

### Phase 0 — expansion pre-work

Items 1–4 done. Of the rest, only **item 8 (RDS resize) hard-gates country #3** — items 5–7 improve
the live corpora and can run in parallel with country builds.

1. **Jurisdiction seam registry — CODED.** `pkg/base/jurisdiction` replaces the scattered 2-way
   `vn`/`my` switches with one `Descriptor` registry (sources, parser, OCR languages, validity default,
   router profile, seed/golden files, DB name); VN is the compiled fallback. All switch points folded:
   config, pipeline (parser, gate, para-label, validity), retrieval (router), app wiring, cmd/seed,
   cmd/eval. Guarded by `TestSourceBuildersCoverRegistry`, `TestAllComplete`, and the per-jurisdiction
   golden-citation regression tests; zero byte changes to live corpora. MCP brief remains a `case`
   switch (irreducible: each brief is large custom text, not a field). See
   [playbook](docs/design/jurisdictions/PLAYBOOK.md#seam-registry--shipped).
2. **VN prod data quality — DONE.** Mojibake re-processed locally (doc 200 clean, 47,504 chunks),
   corpus synced to RDS, MCP redeployed with `bm25_score` + version tracking (`v0.1.0-20260704`).
3. **MY (laksa) hybrid — DONE.** `lexindex` built 8,425 sparse vectors, eval passes (recall 95%,
   mrr 82.1%, current-law+abstention 100%), hybrid deployed to `laksa.danny.vn/mcp` with `bm25_score`
   + version. **Remaining:** P.U. relation-target backfill (1,000 stubs), 8 needs_review docs
   (agclom PDFs with null markdown), layout-aware Section titles.
4. **Freshness engine — DONE.** `RunAllWorkflow` now runs the full pipeline end to end including
   `lexindex` (BM25 sparse rebuild) as step 6 after embed. `RunAllParamsFromConfig` is
   jurisdiction-aware (takes the wired source list, not hardcoded VN names). Worker has `-lexindex`
   one-shot flag. The `pipeline:run-all` schedule (daily, `PauseOnFailure`) is ready to unpause.
5. **Validity/amendment refresh re-crawl.** Scheduled VBPL (and per-country analog) status refresh so
   replaced/amended docs can't keep a stale `in_force` (the `101/2012` gap).
6. **Eval as the permanent gate.** Grow per-jurisdiction golden sets (`golden.json`, `golden_my.json`,
   then `golden_<cc>.json`); every retrieval/ingestion change ships with an eval delta. Realistic user
   phrasing only.
7. **Drift & quality monitoring.** Track abstain rate, gaps, validity-unknown, embedding coverage,
   corpus counts over time; alert on regression.
8. **Infra gate.** Upgrade RDS from `db.t4g.micro` (1 GB) to `db.t4g.small` (2 GB) **before** loading
   country #3. `t4g.small` should serve all 5 countries — this is a law MCP server with low QPS
   (a few queries per agent session, not sustained traffic), and ~250K total chunks with vectors fits
   comfortably in 2 GB. No need to split instances. Consider a 1-yr reserved instance once free-tier
   eligibility lapses.

### Countries #3–#5 — sequential, one at a time

Once Phase 0 is stable, start countries one by one. **Ingest is the only per-country work** — source
crawlers, structure parser (if the citation model differs), scope vocabulary, MCP brief, registry
entry, and eval golden file. Everything downstream is shared: extract → normalize → index → embed →
lexindex → MCP serve all run unchanged through `RunAllWorkflow` on the same codebase. After ingest
validates locally, deploy is: `pg_dump`/`pg_restore` to RDS + new Cloud Run service + Firebase domain.

Each country follows the [playbook phase template](docs/design/jurisdictions/PLAYBOOK.md#phase-template-per-country).
Build order: **ID → SG → TH** (recommended; maintainer's call).

- **#3 🇮🇩 Indonesia (`rendang`).** **Phases 1–6 CODED (2026-07-04), not yet validated on a full
  corpus run.** Sources: `bpk` (peraturan.bpk.go.id; UU/PP/POJK/SEOJK; Cloudflare mint-and-reuse via
  `pkg/fetch`) + `bi` (jdih.bi.go.id; PBI/PADG; JSON API, forward-edge relations only) — OJK +
  peraturan.go.id are geo-fenced, BPK replaces both. Coded same-day: `ParseIndonesianUU` (validated
  on UU 27/2022: Pasal 1–76, 0 gaps), registry entry `id`/`rendang`, `scope_term_id.csv` (120 terms),
  validity/relation seeds, silver kind migration, `rendang` MCP brief, `golden_id.json` (31 cases).
  Discovery validated live: bi (58 in-scope) + bpk (258 in-scope, tahun-windowed incremental ≈ 48s).
  Bugs found and fixed during validation: `SourceIDs` Temporal registration panic, bpk abstract
  shadowing PDF via HTML field, discover activity 10-min timeout, `markitdown[pdf]` missing dep.
  See [INDONESIA](docs/design/jurisdictions/INDONESIA.md).
  **Remaining (after Phase 0.3 Document AI OCR lands):**
  1. Local pipeline validation: `rendang` DB → discover → drain (with Document AI OCR for scanned
     PDFs) → index-all → lexindex → embed → `make eval` → MCP smoke test (Haiku stand-in).
  2. Reconcile `golden_id.json` so_ky_hieu values against real `gold.document` rows.
  3. Deploy: `pg_dump`/`pg_restore` to RDS → Cloud Run service (`rendang`) → Firebase domain
     (`rendang.danny.vn`) → validate over live MCP.
- **#4 🇸🇬 Singapore (`kaya`, proposed).** Sources (candidates): MAS (Notices binding + Guidelines),
  SSO (consolidated Acts in **HTML** — best structure since VBPL), scoped PDPC/CSA. English corpus;
  MY citation family near-reuses. Gate: SSO bot-protection/ToS compliance check. Instrument-class
  badging (Notice vs Guideline) must be explicit.
- **#5 🇹🇭 Thailand (`tomyum`, proposed).** Sources (candidates): BOT notifications, Krisdika
  consolidated Acts, Royal Gazette signal (+ scoped PDPC/ETDA/SEC). Thai corpus; มาตรา/วรรค + ข้อ
  models. **Heaviest language work:** Thai has no word spaces → the BM25 hashing tokenizer needs a
  segmentation decision (dictionary segmenter vs char-n-grams vs vector-primary interim); B.E.↔C.E.
  date normalization; Thai numerals. Last because of the language complexity.

### Phase 0.3 — GCP Document AI OCR engine (replaces EasyOCR for all countries)

**Status: CODED (2026-07-04).** Replace the EasyOCR fallback (local CPU / Kaggle GPU) with GCP
Document AI Enterprise OCR (`banhmi-ocr` processor, `asia-southeast1`, processor ID
`1394aeaa71309925`). Applies to **all jurisdictions** — VN, MY, ID, and future countries.

**Why:** Document AI OCR produces dramatically cleaner text on scanned PDFs (tested on UU 27/2022: 5
of 6 OCR noise patterns eliminated vs pdftotext/MarkItDown), with no local CPU cost and no Kaggle
dependency. $1.50/1K pages (~$4.50 for a full jurisdiction corpus). GCS caches both input PDFs and
output JSON keyed by `content_hash` — survives DB rebuilds, no re-OCR/re-charge.

**Design — hybrid MarkItDown + Document AI:**
1. MarkItDown first (local, free) → content gate
2. Gate passes → done (free, handles born-digital PDFs)
3. Gate fails → Document AI OCR via GCS cache:
   - Check `silver.document_text` (`authority='documentai'`) → skip if exists
   - Check GCS `output/{content_hash}/` → cache hit → download JSON, extract text, write silver (free)
   - Upload PDF to GCS `input/{content_hash}.pdf` (skip if exists)
   - Call `batchProcess` (single doc per call, no page limit, async LRO)
   - Record LRO operation name for retry idempotency
   - Poll LRO → download result JSON → write `silver.document_text`
4. GCS bucket `gs://danny-banhmi-docai` in `asia-southeast1` — never deleted, serves as durable cache.
   ~1.1 GB across all 5 ASEAN countries, $0.03/month (no free tier in this region).

**Config:** `extract.ocr.engine: documentai` (new value alongside `auto`/`local`/`kaggle`). Env:
`BANHMI_DOCAI_PROCESSOR` (processor resource name). GCS bucket via `BANHMI_DOCAI_BUCKET`.

**Implementation:**
- `pkg/extract/docai/` — Document AI client (GCS upload, batchProcess, LRO poll, result download, cache check)
- Extend `runOCR` dispatch in `pkg/pipeline/ocr_all.go` with `case "documentai":`
- Go SDK: `cloud.google.com/go/documentai/apiv1` + `cloud.google.com/go/storage`
- Auth: existing `danh.software@gmail.com` service account via ADC

**Rollout — re-OCR existing jurisdictions after ID validates:**
1. 🇮🇩 **ID (rendang)** — first validation of Document AI OCR on real corpus (running now, 2026-07-04).
   If quality + cost confirmed: proceed to VN and MY.
2. 🇻🇳 **VN (banhmi)** — re-OCR the 83 `needs_review` docs with Document AI (`-ocr-all -force`),
   then re-normalize → re-index → eval regression check → `pg_dump`/`pg_restore` to RDS → redeploy.
   Expected cost: ~83 docs × ~15 pages avg ≈ 1,250 pages = **$1.88**.
3. 🇲🇾 **MY (laksa)** — re-OCR the 8 `needs_review` agclom PDFs, same flow.
   Expected cost: ~8 docs × ~20 pages avg ≈ 160 pages = **$0.24**.
4. After all three validated: switch default `extract.ocr.engine` from `auto` (EasyOCR) to
   `documentai` and remove EasyOCR from the worker container. Keep `auto`/`local` as a fallback
   for offline/air-gapped setups.

### Phase 0.4 — go-fitz (MuPDF) replaces MarkItDown (eliminates Python from extraction)

**Status: PLANNED (2026-07-04).** Replace MarkItDown (Python: pdfminer + mammoth + beautifulsoup)
with go-fitz (Go/C: MuPDF engine via purego, no CGO needed) for all document formats. Eliminates
Python from the extraction hot path. Applies to all jurisdictions.

**Why:** MarkItDown is pure Python — pdfminer interprets PDF bytecode in a Python loop at ~60ms/page.
go-fitz wraps MuPDF (C engine) and runs at ~1ms/page — **15-60× faster**. Tested on real corpus:
314 PDF extractions drop from ~30 min to ~2 min. DOCX and HTML also 10× faster. Same text quality.
AGPL-3.0 license is fine (batch worker, not a network service; repo is public).

**Tested on real corpus files (2026-07-04):**

| Format | go-fitz | MarkItDown | Quality |
|---|---|---|---|
| PDF (born-digital) | 40ms | 1-12s | Same |
| DOCX | 45ms | 450ms | Same |
| HTML | 33ms | 468ms | Better (no messy markdown tables) |
| DOC (legacy binary) | N/A | 2-5s (LibreOffice) | N/A |

**Design — the zero-Python extraction cascade:**
1. **PDF** → go-fitz `Text()` per page (Go/C, ~1ms/page) → content gate
2. **DOCX** → go-fitz `Text()` (Go/C, native OOXML reader) → content gate
3. **HTML** → go-fitz `Text()` (Go/C, MuPDF HTML renderer) → content gate
4. **DOC** → LibreOffice `soffice --headless --convert-to docx` (C++ subprocess, ~333ms)
   → go-fitz on the DOCX (Go/C, ~34ms) → content gate
5. Gate fails → Document AI OCR (Phase 0.3, GCS-cached)

**go-fitz zero-char on scanner+signature PDFs:** go-fitz returns 0 chars on PDFs with only
digital-signature overlays (Kodak/HP scanners + iTextSharp/VGCA). These are correctly detected
by the content gate and routed to Document AI OCR. Not a quality issue — pdfminer/MarkItDown
also returns only ~170 chars of signature metadata on these files.

**Pure Go DOC parsers evaluated and rejected:** `EndFirstCorp/doc2txt` and `lu4p/cat` both
produce binary garbage on real VN government DOC files. LibreOffice remains the only reliable
DOC reader. Converting DOC→DOCX (not DOC→PDF) gives the best quality (preserves Word structure).

**Implementation:**
1. Add `github.com/gen2brain/go-fitz` dependency (purego, no CGO — `CGO_ENABLED=0` stays)
2. New `pkg/extract/fitz/` — wraps go-fitz: `ExtractPDF(path) (text, error)`,
   `ExtractDOCX(path)`, `ExtractHTML(body)`, `ConvertDOC(path) (docxPath, error)` (soffice call)
3. Replace in `pkg/pipeline/process_activities.go`:
   - `pdfToMarkdown` → go-fitz `ExtractPDF`
   - `docxToMarkdown` → go-fitz `ExtractDOCX`
   - `htmlToMarkdown` → go-fitz `ExtractHTML`
   - `docToMarkdown` → soffice→DOCX→go-fitz (no more PDF intermediate)
4. Remove: `requireMarkItDown()`, `tools/markitdown_convert.py`,
   `deploy/containerfiles/markitdown-requirements.txt`
5. Update `Containerfile`: base image `python:3.12-slim` → `debian:bookworm-slim`;
   remove pip/MarkItDown/pdfminer/tesseract; keep `libreoffice-writer` (for DOC)
6. Update docs (EXTRACTION.md, ARCHITECTURE.md)

**Corpus-wide re-extract after rollout:** since go-fitz produces identical or better text than
MarkItDown on born-digital files, and Document AI replaces EasyOCR on scans, re-run
`-extract-all -force` + `-normalize-all -force` + `-index-all -force` for all three jurisdictions
(VN, MY, ID) to get the improved text. One-time cost: Document AI OCR on the ~305 scanned VN
PDFs ≈ $4.50. Born-digital re-extract is free and takes ~2 min per jurisdiction.

### v0.3.0 — infrastructure migration: Lambda + ONNX serves, GCP builds, no dump/restore

**Status: PLANNED (next after v0.2.1).** Clean split: **read path** on AWS Lambda (ONNX embedder,
stateless MCP), **write path** on GCP Cloud Run Jobs (L4 GPU). Pipeline writes **directly** to the
production DB — no more pg_dump/pg_restore. No local worker machine. Everything automated.

**Architecture:**
```
READ PATH — AWS Singapore:
  User → CloudFront (ACM cert, *.danny.vn) → Lambda Function URLs
           ├── banhmi-mcp  (ONNX BGE-M3, stateless MCP, ~1-3s cold start)
           ├── laksa-mcp
           └── rendang-mcp
           ↓ (public endpoint, SCRAM-SHA-256 + TLS)
         RDS PostgreSQL 17 + pgvector (ap-southeast-1)

WRITE PATH — GCP Cloud Run Jobs (weekly):
  Cloud Scheduler (weekly cron per jurisdiction)
    → Cloud Run Job (L4 GPU, asia-southeast1)
       ├── discover → fetch (downloaded files cached in GCS)
       ├── extract (go-fitz) → normalize → index
       ├── OCR → Document AI API (GCS-cached)
       ├── embed (BGE-M3 on L4 GPU, in-process — replaces Kaggle)
       └── lexindex
       ↓ writes DIRECTLY to RDS (over TLS, ~2-5ms per query)
```

**Key design decisions:**
- **Stateless MCP.** Each request is self-contained — no in-memory session map. Lambda processes
  one request, returns results, dies. Fits the evidence-only model (search/document are stateless
  lookups; no conversational state needed). Code change: disable session tracking in
  `NewStreamableHTTPHandler` or use the stateless HTTP transport mode.
- **RDS with SCRAM+TLS (no VPC for Lambda).** Keep the existing RDS instance (managed backups,
  no server management). Public endpoint, SCRAM-SHA-256 + TLS-required. Lambda does NOT need VPC
  attachment — simpler, no ENI cold-start penalty (~1-2s saved), no NAT Gateway.
- **CloudFront for custom domains.** 3 CloudFront distributions (one per jurisdiction) — each
  maps one domain to one Lambda Function URL origin. ACM certificate in **us-east-1** (required
  by CloudFront, regardless of edge location). SEA edge PoPs: Singapore, HCMC, KL, Jakarta,
  Bangkok — TLS terminates at nearest edge for Claude Code users; hosted agents (Claude.ai,
  ChatGPT, Grok) traverse Pacific to Singapore Lambda regardless. Free tier covers all traffic.

**Direct writes, no dump/restore.** The pipeline writes directly to RDS — no intermediate local
database, no pg_dump/pg_restore cycle. PostgreSQL MVCC handles concurrent reads (Lambda) + writes
(pipeline) — MCP queries see a consistent snapshot while the pipeline upserts. The corpus is
reproducible from official government sources (cached in GCS), not user-generated data.

**Read path — CloudFront → Lambda → ONNX → RDS:**
- **CloudFront (3 distributions, one per jurisdiction)** — SEA edge PoPs (Singapore, HCMC, KL,
  Jakarta, Bangkok) terminate TLS at the nearest edge. Persistent keep-alive from edge to Lambda
  origin skips TCP+TLS on subsequent requests. Each distribution maps one custom domain
  (`banhmi/laksa/rendang.danny.vn`) to one Lambda Function URL. ACM certs in us-east-1
  (CloudFront requirement). Free tier (1 TB/mo + 10M req). Main latency benefit: Claude Code
  users in SEA (~2ms to local edge); hosted agents (Claude.ai/ChatGPT/Grok) originate from US
  infra and traverse Pacific (~150ms) to reach Singapore Lambda regardless.
- **Lambda (arm64, al2023)** — stateless compute in `ap-southeast-1`. One function per
  jurisdiction, same container image, different `BANHMI_JURISDICTION` env. No VPC attachment.
  Function URLs as CloudFront origins. Scale to zero, pay per request.
- **ONNX Runtime** — query-time BGE-M3 embedding in-process. ~80 MB (binary + libonnxruntime).
  Cold start ~1-3s (vs 10-15s OpenVINO). Same model, same vectors. Build path already exists
  (`-tags onnx`, `pkg/rag/embed/onnxembed/`).
- **Stateless MCP** — each HTTP request self-contained: embed query → hybrid search → return
  evidence. No session map, no SSE streams. Request in, response out, Lambda dies.
- **RDS hybrid search** — dense BGE-M3 vectors (HNSW) + BM25 sparse vectors (`sparsevec`)
  fused with RRF + deterministic query router. Single DB round-trip per search.
- **Latency (warm):** user (HCMC) → CloudFront edge (~2ms) → Lambda (Singapore, ~30ms) →
  ONNX embed (~50ms) → RDS search (~20ms) → response. **~100-150ms warm, ~1-3s cold.**
- **Cost:** ~100 queries/day × 250ms × $0.0000167/GB-s ≈ **$0.50/month** for all 3.

**Write path — GCP Cloud Run Jobs + L4 GPU:**
- One Cloud Run Job per jurisdiction, triggered weekly by Cloud Scheduler.
- L4 GPU (24 GB VRAM): extraction (go-fitz, CPU) + embedding (BGE-M3, GPU in-process) + OCR
  (Document AI API). **Replaces Kaggle entirely** — no more batch API overhead or token management.
- **Downloaded files cached in GCS** — fetched PDFs/DOCX/HTML stored in a GCS bucket during
  ingest, keyed by content hash. Survives across runs; no re-download on re-process.
- **Writes directly to RDS** over TLS (public endpoint, SCRAM). GCP Singapore → AWS Singapore:
  **~2-5ms per DB query** (same-city fiber). No local/intermediate DB.
- **Cost:** L4 ~$0.67/hr × ~10 min/jurisdiction ≈ $0.11/run, **~$1.40/month** for all 3 weekly.

**Database — RDS PostgreSQL 17 + pgvector (kept as-is):**
- Keep the existing RDS `db.t4g.micro` (2 vCPU, 1 GB). Actual usage: 3% CPU, 0.03 avg
  connections, 3 GB of 20 GB storage. No migration needed — same instance, same endpoint.
- **Public endpoint, SCRAM-SHA-256 + TLS-required.** Both Lambda and Cloud Run Jobs connect
  over the internet, authenticated by password + TLS.
- **RDS automated backup, 7-day retention** (free on RDS — no extra cost vs 1 day). AWS manages
  the backup; no pg_dump, no S3 bucket, no restore scripts. If the DB needs recovery, restore
  from RDS snapshot. 7 days gives time to notice corruption before the clean backup rotates out.
  The corpus is also reproducible (re-run the pipeline from GCS-cached sources).
- Stays on db.t4g.micro (~$13/mo in ap-southeast-1) for VN + MY. Upgrade to t4g.small (2 GB,
  ~$26/mo) **before loading country #3** (rendang) per Phase 0 item 8.

**Cost estimate (monthly):**

| Component | Current (v0.2.1) | Target (v0.3.0) |
|---|---|---|
| MCP compute | Cloud Run ~$0-5 × 3 | **Lambda ~$0.50** |
| CDN / custom domains | Firebase (free) | **CloudFront $0** (free tier) |
| DB | RDS ~$13 | **RDS ~$13** (t4g.micro; ~$26 after rendang upgrade) |
| Backup | RDS automated | **RDS automated** (7-day retention, free) |
| Write pipeline + embed | Local machine + Kaggle (free) | **Cloud Run Jobs L4 ~$1.40** |
| OCR | Document AI ~$0.05 | Document AI ~$0.05 |
| File cache | Local disk | **GCS ~$0.03** |
| **Total** | **~$15-30** | **~$17/mo** |

**Migration steps — test everything BEFORE switching `*.danny.vn`:**

**Phase A — local validation (no cloud cost) — DONE (2026-07-05):**
1. **ONNX build + server startup — DONE.** `-tags onnx` compiles clean (CGO + `libtokenizers` +
   `libonnxruntime`). Server starts and loads the ONNX INT8 model (~544 MB) in-process; MCP
   initializes, serves the brief. ORT v1.27+ required (`onnxruntime_go` v1.30.1 needs API v25).
2. **ONNX embedder code fixed — DONE.** The ONNX export produces `last_hidden_state` (token-level,
   `[batch, seq, 1024]`), not a pre-pooled vector. Fixed `onnxembed_onnx.go`: output name
   `dense_vecs` → `last_hidden_state`, added CLS pooling (first token) + L2 norm in Go. Both
   the self-exported INT8 and the third-party `gpahal/bge-m3-onnx-int8` model use this output.
3. **ONNX model exported + quantized — DONE.** Exported BGE-M3 to ONNX via `optimum` from the
   local PyTorch checkpoint. INT8 dynamic quantization (avx512_vnni). FP32 2.2 GB, INT8 544 MB.
   Cosine vs OV INT8: FP32 ~0.999, INT8 ~0.989 (different quantization schemes).
4. **Eval (hybrid) — DONE, no recall regression.** Querying the OV-INT8-indexed corpus with ONNX:

   | | VN recall | VN mrr | MY recall | MY mrr |
   |---|---|---|---|---|
   | **OV INT8 (baseline)** | 85.7% | 80.9% | 95.0% | 82.1% |
   | **ONNX FP32 hybrid** | **85.7%** | 77.8% | **100%** | 82.2% |
   | **ONNX INT8 hybrid** | **85.7%** | 75.8% | **95.0%** | 81.4% |

   Recall matches baseline exactly. MRR has ±5% rank shifts from the index-query embedder
   mismatch (OV INT8 index, ONNX query) — expected to converge after re-embedding the corpus
   with the same ONNX model on the Cloud Run L4 write path.
5. **Makefile targets added — DONE.** `make eval-onnx` and `make mcp-onnx` for local ONNX
   testing. Containerfile ORT version bumped to 1.27.0.
6. Stateless MCP mode — pending (next step).

**Phase B — deploy new infra alongside existing (both stacks live):**
5. **AWS:** create 3 Lambda functions (ECR image with `-tags onnx`), each with Function URL.
   No VPC attachment. Env: `BANHMI_JURISDICTION`, DB host = existing RDS endpoint.
6. **AWS:** 3 CloudFront distributions (one per jurisdiction), each with its own ACM cert in
   us-east-1 and one Lambda Function URL as origin. No host-based routing needed.
7. **AWS:** set RDS backup retention to 7 days (free, already the default).
8. **GCP:** create Cloud Run Job (`banhmi-pipeline`) with L4 GPU. Env: DB host = RDS endpoint.
   Run pipeline for all 3 jurisdictions → writes directly to RDS.

**Phase C — test new stack via temporary URLs (production DNS unchanged):**
9. Test each Lambda Function URL directly:
   - `https://{function-url}.lambda-url.ap-southeast-1.on.aws/mcp` — MCP search, document,
     corpus_status on all 3 jurisdictions (stateless, no session init needed).
   - Verify: correct brief (banhmi/laksa/rendang), correct version, search returns hits.
10. Test via CloudFront distribution domain (`d1234.cloudfront.net`) with Host header override —
    verify routing to correct Lambda per jurisdiction.
11. Test cold start: wait 5 min (no keep-alive), hit each Function URL — measure time to first
    response. Must be ≤3s.
12. Test pipeline re-run: trigger Cloud Run Job manually → verify it writes to RDS →
    verify Lambda sees the new data.
13. Run eval against the new stack (Lambda → RDS) — compare with v0.2.1 eval results.
    No recall/mrr regression allowed.
14. Run the Haiku-over-MCP smoke test against each Lambda Function URL — the stand-in agent
    pattern from CLAUDE.md, proving the MCP contract works end-to-end.

**Phase D — DNS cutover (only after Phase C passes):**
15. Update Cloudflare DNS: `banhmi/laksa/rendang.danny.vn` CNAME → CloudFront distribution.
    Keep old GCP Cloud Run services running for 24h as rollback.
16. Verify `*.danny.vn/mcp` endpoints serve from Lambda (check version string, response time).
17. Monitor for 24h: no errors, no timeouts, no stale data.

**Phase E — decommission old infra (after 24h soak):**
18. Delete: GCP Cloud Run MCP services (`banhmi-mcp`, `laksa-mcp`, `rendang-mcp`).
19. Delete: Firebase Hosting sites (`danny-banhmi`, `danny-laksa`, `danny-rendang`).
20. Keep: RDS (same instance, now written directly by pipeline), GCP Cloud Run Jobs, Document AI,
    GCS cache, Artifact Registry.
21. Update: CLAUDE.md, DEPLOYMENT.md, ARCHITECTURE.md — new deploy workflow.

**Rollback plan:** if any Phase C test fails or Phase D monitoring shows issues, revert
Cloudflare DNS to the old GCP Cloud Run endpoints (still running for 24h). Investigate and
retry. The old stack is untouched until Phase E.

**Dependencies:** code is cloud-agnostic (env vars). ONNX build path validated locally (Phase A
done). Remaining code changes: stateless MCP mode (disable session map), Lambda runtime adapter
(aws-lambda-go or Lambda Web Adapter), L4 GPU embed engine (in-process BGE-M3 on CUDA),
Temporal-free pipeline runner (cmd/pipeline), Containerfile for Lambda packaging (arm64, al2023).

**Retrieval quality improvement track (parallel with infra migration):**

0. **Bilingual MCP scope (VN, ID) — DONE (2026-07-05).** Added 53 English scope terms to VN seed
   CSV (ID already had them). English queries now pass in-domain check. MCP API fields renamed
   to English (`so_ky_hieu` → `doc_number`, `dieu` → `article`, `khoan` → `clause`, `diem` →
   `point`). MCP guide (VN/ID) updated: instruct agents to query in native language for best
   precision; cross-lingual works via BGE-M3 dense but misses BM25 lexical matches.
1. **Native-language MCP guidance — DONE (2026-07-05).** VN and ID briefs instruct agents to
   translate English queries to the native language before calling search. Language/translation
   contract added to evidence contract. MY unchanged (English-only).
2. **Grow golden sets to 50+ — DONE (2026-07-05).** VN: 54 cases, MY: 51 cases.
3. **Re-embed all corpora — DONE (2026-07-05).** VN 47,587 chunks + MY 8,425 chunks re-embedded
   on Kaggle T4 (PyTorch FP16 BGE-M3, CLS+L2). Cloud Run L4 embed job Containerfile also shipped
   (`Containerfile.embed-job.onnx`, in-process ONNX INT8, `embed.engine=onnx`).
   **Never bulk-embed on the local laptop.**
4. **Golden set + scope term corrections — DONE (2026-07-05).** Fixed 12 wrong article expectations
   in VN golden set (all pointed at wrong Điều within the right document). Added missing scope
   terms for VN (bảo mật, mã QR, QR code) and MY (11 terms: access control, SOC, shariah, etc.).
   Post-fix baselines:
   - **VN: recall 75.9%, MRR 60.0%, current-law 100%, abstention 100%** (54 cases)
   - **MY: recall 85.4%, MRR 73.6%, current-law 100%, abstention 98.0%** (51 cases)
   Remaining gaps: VN 5 cases from 52/2024/NĐ-CP (0 chunks, extraction gap), 2 cybersec ranking
   misses, 2 edge cases. MY 4 missing corpus docs, 3 retrieval ranking misses.
5. **Corpus gap investigation — IN PROGRESS.** Check whether filtered ingest or extraction issues
   are blocking documents that should be indexed (52/2024/NĐ-CP, MY missing docs). Fix pipeline
   gaps before reranker evaluation.
6. **Cross-encoder reranker evaluation** — after corpus gaps are closed, test reranker on the
   expanded golden sets to push remaining ranking misses into top-k.

**Future: US-region Lambda for hosted agents.** After migration stabilizes, consider deploying
a second set of Lambda functions in us-east-1 (or us-west-2) with a read replica or cross-region
RDS — reduces ~150ms Pacific latency for Claude.ai/ChatGPT/Grok. Evaluate based on actual usage
patterns post-launch. Same image, different region + DB endpoint env var.

### MVP2 candidates (unchanged, deliberately parked)

Gemma 4 E4B OCR enhancement · figure extraction · manual-folder source · crawl depth >1 (scope
decision — would expand toward the whole legal corpus) · `sbv.gov.vn` extra source · Cloud Armor edge
(needs an ~$18/mo LB — only when abuse justifies) · cross-encoder reranker (eval-only today).

## Milestone history (compressed; full detail in git history)

- **2026-05-30 — evidence-only pivot.** Removed the answer LLM and all answer surfaces (`ask`,
  `pkg/llm`, chat endpoint, web UI); MCP = the product surface; embedder mandatory. Điểm-aware
  chunking + hierarchical roll-up; clause-level currency served as verbatim `incoming_amendments[]`
  evidence (never derived); EasyOCR (`vi`) replaced Tesseract, batched (`OcrAll`).
- **2026-05-31 — INPUT hardening.** Priority-0 ingest-flow audit fixed silent gazette-text misses
  (congbao search recall, drain orchestration, OCR-on-stub); `RunAll` one-shot orchestrator +
  streaming Kaggle batch (OOM-proof); full re-crawl validated (572 docs / 62,350 chunks / 100%
  embedded); deploy-readiness gate MET; 4-reviewer pre-deploy code review (DB-layer fixes landed).
- **2026-06-01 — VN deployed (Track B).** AWS RDS (PG17+pgvector, Singapore) + Cloud Run (in-process
  OpenVINO BGE-M3, distroless, 0 HIGH/CRIT CVEs) + Firebase Hosting → `banhmi.danny.vn/mcp` live.
  Deviations from plan, with reasons: RDS replaced Neon (512 MB free cap overflowed mid-restore);
  in-process OpenVINO replaced the OVMS CPU sidecar (one image, exact parity).
- **2026-06-10 — MVP1 completion pass.** P0 identity fix — `doc_key` = `<TYPE>|<NUMBER>` (số-only keys
  had merged distinct documents); scope gate introduced `relation_context` (out-of-domain
  relation-pulled docs keep text/relations, no chunks); OCR-floor serving decision (badged
  non-binding); Phụ lục chunking; validity honesty (`unknown` class — status-less sources no longer
  default `in_force`); RDS corpus reconciled by wholesale dump/restore of the validated local corpus.
- **2026-06-13 — cost fix.** Deleted the Cloud Run NAT/router/static IP (~$35/mo, defeated
  scale-to-zero); RDS SG opened to `0.0.0.0/0` with TLS-required + password. GCP idle ~$0.
- **2026-06-19/20 — vanban source #2.** `vanban.chinhphu.vn` built + live-validated (freshest
  central-law feed; caught `134/2025/QH15` AI Law that vbpl lagged); backfill deployed to RDS
  (586 docs, 20,373 chunks then); AI scope terms seeded; normalize-selector fix.
  Lesson paid: never `-force` whole-corpus stages against the live DB — use targeted selectors.
- **2026-06-21 — Malaysia built (phases A–E + quality pass, local).** Jurisdiction seam
  (config dimension, DB boundary, VN-safe); agclom/bnm/sc sources (BNM AWS-WAF mint via chromedp);
  MY PDF Section-tree parser; EN OCR; derived validity; 1,000 P.U. relations promoted; MCP brief per
  jurisdiction. 63 docs · 8,425 chunks · 100% embedded.
- **2026-06-22 — hybrid retrieval + Malaysia deployed.** VN hybrid shipped to prod: pgvector
  `sparsevec` BM25 (IDF baked into doc vectors, hashing trick) + RRF + deterministic query router —
  recall@k 89.3% / mrr 84.6% / current-law 100%; naive equal-weight RRF had regressed, hence the
  router. laksa deployed → `laksa.danny.vn/mcp` (multi-jurisdiction launch); MY scope-vocabulary fix +
  `golden_my.json` (abstention 100%, recall 95%). `bm25_score` per hit committed (redeploy pending).
- **2026-06-22→07-02 — mojibake remediation (coded).** UTF-8-forced HTML extraction + Cyrillic
  mojibake gate + local re-process/embed harness + low-memory OpenVINO tuning; prod re-process
  pending (see Phase 0.2).

**Do not reopen (settled by bake-offs / paid lessons):** evidence-only surface (no answer LLM);
BGE-M3 as the embedding model; extraction cascade DOCX→HTML→DOC→PDF/OCR with batch-only OCR (never
inline, no sidecar); `doc_key = <TYPE>|<NUMBER>` identity; hybrid via native pgvector sparsevec
(no ParadeDB/`pg_search` — can't run on RDS); model-search stopped; RDS PostgreSQL as the single
datastore (kept through v0.3.0). *(Deploy shape evolving: Cloud Run+Firebase+OpenVINO → Lambda+CloudFront+ONNX per v0.3.0; EasyOCR → Document AI per Phase 0.3.)*

## Deferred / dropped

- **Answer LLM / chat endpoint / web "ask" UI** — dropped; the user's model answers.
- **Watchdog reconcile half** — fetch-lease recovery covers it; resolve-references folds into relations.
- **phapluat.gov.vn** source — dropped for MVP1.
- **Reranker** — eval-only; local rerankers lost to vector-only; revisit on a larger golden set.
- **`bronze.source_document_history`** — dropped; the temporal model is silver `validity_period` +
  `amendment_event`.
- **English/`provision_level` multilingual experiment** — reverted; one language per country.

## Decisions log

| Decision | Choice | Principle |
|----------|--------|-----------|
| **INPUT before OUTPUT** | corpus first, validated on real docs; then the serving surface | data quality is the product |
| **Evidence-only; no answer LLM** | citations/validity/relations/gaps over MCP; user brings the model | we own the data, not the answer |
| **Multi-jurisdiction (2026-06-21)** | jurisdiction = config dimension; **the Postgres DB is the boundary** (one DB per country, same instance until it contends); one image, N deployments | share the core, customize behind interfaces; never fork |
| **One language per country (2026-06-21)** | index/serve/search only the binding native language; never translate; non-binding translations never indexed | translation risks legal error |
| **Food-dish codenames (2026-07-02)** | `banhmi` · `laksa` · proposed `rendang`/`tomyum`/`kaya` (+ domains) — pending sign-off | consistent, memorable, per-country identity |
| **Seam registry before #3 (2026-07-02)** | consolidate the 2-way `vn`/`my` switches into one jurisdiction descriptor before adding a third | prevent N-way `case` drift |
| **Deploy shape** (2026-06-01→v0.3.0) | *Current:* worker local → RDS → Cloud Run (OpenVINO) → Firebase. *v0.3.0:* Cloud Run Job (L4) → RDS ← Lambda (ONNX) ← CloudFront | scale-to-zero; no local worker |
| **Hybrid retrieval (2026-06-22)** | dense BGE-M3 + native pgvector `sparsevec` BM25 + RRF + query router; no `pg_search` | beats vector-only on eval; single datastore; RDS-portable |
| **"Coded" ≠ "validated"** | tracked separately, always | never ship unvalidated extraction as done |
| No hardcoded policy lists | vocab in `config` schema, seeded from CSVs | edit CSV + re-seed, no code change |
| No AI as canonical parser | deterministic extraction; OCR batched, gated, never sole binding source | never generate legal text |
| PDF engine | *Current:* MarkItDown. *v0.4:* go-fitz (MuPDF) | zero-Python extraction |
| OCR | *Current:* EasyOCR. *Phase 0.3:* Document AI (GCS-cached) | cleaner text, no local CPU |
| Embedder | BGE-M3 everywhere. *Current:* OpenVINO (query) + Kaggle (bulk). *v0.3.0:* ONNX (query on Lambda) + L4 GPU (bulk in Cloud Run Job) | index/query parity |
| **ONNX validated (2026-07-05)** | ONNX INT8 (544 MB) query embedder: hybrid recall matches OV baseline exactly (VN 85.7%, MY 95-100%); MRR ±5% from index-query mismatch, converges after re-embed | INT8 preferred over FP32 (2.2 GB) — same recall, 4× smaller |
| **No local bulk embed** | Bulk embedding on Kaggle GPU or Cloud Run L4 only — never on the dev laptop (8 GB, would OOM/overheat) | protect the dev machine |
| No composite primary keys | surrogate identity PKs; business keys `UNIQUE` | idempotent `ON CONFLICT` upserts |
| Containers | podman-first, `Containerfile` | no host installs |
| Pre-release migrations | mutable until first tagged release, then append-only | no fix-up migrations pre-v1 |
| Validity-aware retrieval | current-law leads; non-current appended, badged, capped | never present repealed law as current |
| Relation confidence split | confirmed structured relations ≠ weak text links; weak can't drive validity | evidence the agent can trust |
