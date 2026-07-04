# banhmi plan

Living roadmap and progress tracker. Architecture detail in [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md);
conventions and the canonical agent guide in [`CLAUDE.md`](CLAUDE.md); the multi-country model in
[`docs/design/jurisdictions/`](docs/design/jurisdictions/). Last updated: 2026-07-04.

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
EasyOCR `vi` over Tesseract/VLM parsers; BGE-M3 (OpenVINO INT8) as the embedder; extraction cascade
DOCX→HTML→DOC→PDF/OCR with batch-only OCR (never inline, no sidecar); in-process OpenVINO on Cloud Run
(no OVMS sidecar); RDS + Cloud Run + Firebase deploy shape; `doc_key = <TYPE>|<NUMBER>` identity;
hybrid via native pgvector sparsevec (no ParadeDB/`pg_search` — can't run on RDS); model-search stopped.

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
| **Deploy shape** (2026-06-01) | worker local → RDS Postgres → Cloud Run MCP (in-process OpenVINO) → Firebase domains | ~$0 idle; only DB + MCP public |
| **Hybrid retrieval (2026-06-22)** | dense BGE-M3 + native pgvector `sparsevec` BM25 + RRF + query router; no `pg_search` | beats vector-only on eval; single datastore; RDS-portable |
| **"Coded" ≠ "validated"** | tracked separately, always | never ship unvalidated extraction as done |
| No hardcoded policy lists | vocab in `config` schema, seeded from CSVs | edit CSV + re-seed, no code change |
| No AI as canonical parser | deterministic extraction; OCR batched, gated, never sole binding source | never generate legal text |
| PDF engine | MarkItDown (`pdfminer.six`) — no GPL/AGPL | one converter, one quality gate |
| OCR | EasyOCR, per-jurisdiction language, batch (`OcrAll`) | better diacritics; batch, not inline |
| Embedder | BGE-M3 (OpenVINO INT8) everywhere; queries in-process on Cloud Run | index/query parity |
| No composite primary keys | surrogate identity PKs; business keys `UNIQUE` | idempotent `ON CONFLICT` upserts |
| Containers | podman-first, `Containerfile` | no host installs |
| Pre-release migrations | mutable until first tagged release, then append-only | no fix-up migrations pre-v1 |
| Validity-aware retrieval | current-law leads; non-current appended, badged, capped | never present repealed law as current |
| Relation confidence split | confirmed structured relations ≠ weak text links; weak can't drive validity | evidence the agent can trust |
