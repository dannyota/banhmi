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

## Deployment shape (shipped; repeats per country)

- **Worker — local**, one jurisdiction per run (`BANHMI_JURISDICTION`); bulk embedding offloads to
  **Kaggle GPU** (`embed.engine=kaggle`; Cloud Run L4 embedder coded, pending deploy — see v0.3.0);
  OCR batch via **GCP Document AI** (`ocr.engine=documentai`, GCS-cached); extraction via **go-fitz**
  (MuPDF, zero-Python).
- **DB — AWS RDS PostgreSQL 17 + pgvector/HNSW** (`ap-southeast-1`), **one database per country** on
  one instance (`banhmi`, `laksa`, `rendang`). TLS-required, password-gated.
- **MCP — GCP Cloud Run** (`asia-southeast1`), **one scale-to-zero service per country**, same image:
  single Go binary with the **in-process BGE-M3** query embedder (OpenVINO INT8 on current deploy;
  ONNX INT8 validated, pending redeploy — see v0.3.0). ~$0 idle; $5/mo budget alert +
  `--max-instances=3` per service.
- **Domains — Firebase Hosting** (free Spark), one site per country in front of its service.
- **Retrieval — hybrid** (single datastore): dense BGE-M3 + **BM25 sparse vectors** (pgvector
  `sparsevec`, `cmd/lexindex`) fused with RRF + a deterministic query router. No ParadeDB/`pg_search`.
- **Pipeline runner — `cmd/pipeline`** (no Temporal). Calls pipeline activity methods directly:
  Discover, Fetch, Extract, Normalize, Index, EmbedAll, OcrAll, LexicalIndex, RunAll. Structured slog
  output goes to stdout (captured by GCP Cloud Logging when running as a Cloud Run Job).

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
   mrr 82.1%, current-law+abstention 100%), hybrid deployed to `laksa.danny.vn/mcp` with `bm25_score`
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
`pg_dump`/`pg_restore` to RDS + new Cloud Run service + Firebase domain (current flow; v0.3.0
replaces with direct writes).

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

### v0.3.0 — ONNX everywhere, Cloud Run L4 write path, no dump/restore

**Status: Phase A DONE (2026-07-05), Phase B in progress.** Switches query embedder from OpenVINO
to ONNX Runtime (lighter, faster cold start). **Write path** on GCP Cloud Run L4 (pipeline writes
directly to RDS). **Read path stays on Cloud Run** (scale-to-zero, $0 idle, zero maintenance —
decided 2026-07-06 after evaluating Lambda, Fargate, EC2, GKE Autopilot; all rejected on cost or
complexity). Cloud Run is **x86_64 only** (no ARM64 support). **Temporal removed** — `cmd/pipeline`
calls activity methods directly (shipped 2026-07-06).

**Architecture:**
```
READ PATH — GCP Cloud Run (asia-southeast1):
  User → Firebase Hosting (free Spark, *.danny.vn)
           ├── banhmi-mcp  (scale-to-zero)
           ├── laksa-mcp
           └── rendang-mcp
           ↓ (public endpoint, SCRAM-SHA-256 + TLS)
         RDS PostgreSQL 17 + pgvector (ap-southeast-1)

  Current deploy: OpenVINO INT8 embedder.
  Pending redeploy: ONNX INT8 embedder (validated, Phase A done).

WRITE PATH — GCP Cloud Run L4 (asia-southeast1), pending deploy:
  banhmi-writer (Cloud Run L4 Job, weekly per jurisdiction):
    Cloud Scheduler → cmd/pipeline -run-all
       ├── discover → fetch (files cached in GCS)
       ├── extract (go-fitz) → normalize → index
       ├── OCR → Document AI API (GCS-cached)
       ├── embed (BGE-M3 on L4 GPU, in-process ONNX)
       └── lexindex
       ↓ writes DIRECTLY to RDS (over TLS)

  banhmi-embedder (Cloud Run L4 HTTP Service, scale-to-zero):
    Local dev → POST /embed → ONNX BGE-M3 on L4 GPU → vectors
    (replaces Kaggle — local pipeline calls this for embed step)
```

**Key design decisions:**
- **Read path stays on Cloud Run.** Evaluated 2026-07-06: Lambda (1-req/instance, 20-29s search
  latency), Fargate ($39/mo), EC2 t4g ($31/mo always-on), GKE Autopilot ($74.40/mo cluster fee,
  KEDA needed). Cloud Run wins: native scale-to-zero ($0 idle), auto-scale, zero maintenance.
  ONNX Runtime cold start (~6-8s) is faster than OpenVINO (~12s). If cold start matters later,
  min-instances=1 (~$5-8/mo per jurisdiction) eliminates it.
- **Stateless MCP.** Each request is self-contained — no in-memory session map.
- **RDS with SCRAM+TLS.** Public endpoint, SCRAM-SHA-256 + TLS-required. Cloud Run connects
  over the internet (cross-cloud GCP→AWS ~10-20ms, acceptable for low QPS).
- **Firebase Hosting for custom domains.** Free Spark tier, one site per country.
- **Direct writes, no dump/restore.** The pipeline writes directly to RDS — no intermediate local
  database, no pg_dump/pg_restore cycle. PostgreSQL MVCC handles concurrent reads + writes.

**Phase A — local validation — DONE (2026-07-05):**
1. **ONNX build + server startup — DONE.** `-tags onnx` compiles clean. Server starts and loads
   the ONNX INT8 model (~544 MB) in-process.
2. **ONNX embedder code fixed — DONE.** Output name `dense_vecs` → `last_hidden_state`, added
   CLS pooling + L2 norm in Go.
3. **ONNX model exported + quantized — DONE.** INT8 dynamic quantization, 544 MB.
4. **Eval (hybrid) — DONE, no recall regression.** Recall matches OV baseline exactly
   (VN 85.7%, MY 95-100%). MRR ±5% from index-query embedder mismatch — expected to converge
   after re-embedding with the same ONNX model.
5. **Makefile targets — DONE.** `make eval-onnx` and `make mcp-onnx` for local testing.
6. **Temporal removed — DONE.** `cmd/pipeline` replaces `cmd/worker`; calls activity methods
   directly with structured slog output.

**Phase B — deploy write path + redeploy read path with ONNX:**
7. **Stateless MCP — DONE.** Each request is self-contained; no in-memory session map.
8. Deploy `banhmi-embedder` (Cloud Run L4 HTTP Service, scale-to-zero). `embed.engine=cloudrun`
   in local pipeline to call it (replaces Kaggle). Containerfile shipped
   (`Containerfile.embed-job.onnx`).
9. Deploy `banhmi-writer` (Cloud Run L4 Job). Test full pipeline against RDS + `make eval`.
10. Redeploy Cloud Run MCP services with ONNX image (`Containerfile.cloudrun.onnx`).
11. Verify all `*.danny.vn/mcp` endpoints serve correctly with ONNX embedder.
12. Run the Haiku-over-MCP smoke test.

**Retrieval quality improvements — DONE (completed 2026-07-05):**
- **Bilingual MCP scope (VN, ID).** English scope terms added; MCP API fields renamed to English.
- **Native-language MCP guidance.** VN and ID briefs instruct agents to translate queries.
- **Golden sets expanded.** VN: 54 cases, MY: 51 cases.
- **Re-embed all corpora.** VN 47,587 + MY 8,425 chunks re-embedded on Kaggle T4 (PyTorch FP16
  BGE-M3, CLS+L2). Cloud Run L4 embed job Containerfile also shipped.
- **Golden set + scope term corrections.** Fixed 12 wrong article expectations in VN golden set.
  Post-fix baselines: VN recall 75.9% / MRR 60.0%, MY recall 85.4% / MRR 73.6% (both current-law
  100%).
- **Corpus gap fix.** `MatchFolded` diacritics-folded rescue in the scope gate. 721→723 primary
  docs; `52/2024/NĐ-CP` and `15/2020/NĐ-CP` now have chunks.

**After Phase B:** cross-encoder reranker evaluation on the expanded golden sets.

**Cost estimate (monthly, post-v0.3.0):**

| Component | Cost |
|---|---|
| MCP compute (Cloud Run, ONNX, ×3 services) | ~$0-5 each |
| CDN / custom domains (Firebase, free Spark) | $0 |
| DB (RDS t4g.micro, 3 DBs; upgrade to t4g.small ~$26 if needed) | ~$13 |
| Write pipeline + embed (Cloud Run L4 writer) | ~$1.40 |
| Local dev embed (Cloud Run L4 embedder) | usage-based (~$1/hr active) |
| OCR (Document AI) | ~$0.05 |
| File cache (GCS) | ~$0.03 |
| **Total** | **~$17/mo** |

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
  replaces `cmd/worker` (no Temporal dependency). Read path stays Cloud Run (Lambda/Fargate/EC2/GKE
  rejected).
- **2026-07-08 — Document AI OCR + go-fitz rolled out.** Document AI replaces EasyOCR as default OCR
  engine (GCS-cached, all jurisdictions). go-fitz (MuPDF) replaces MarkItDown — Python eliminated from
  the extraction hot path (15-60× faster).

**Do not reopen (settled by bake-offs / paid lessons):** evidence-only surface (no answer LLM);
BGE-M3 as the embedding model; go-fitz extraction cascade (DOCX→HTML→DOC→PDF) with Document AI OCR
fallback (batch-only, GCS-cached, never inline); `doc_key = <TYPE>|<NUMBER>` identity; hybrid via
native pgvector sparsevec (no ParadeDB/`pg_search` — can't run on RDS); model-search stopped; RDS
PostgreSQL as the single datastore; Cloud Run+Firebase for read path (decided 2026-07-06); Temporal
removed (replaced by direct `cmd/pipeline`); MarkItDown and EasyOCR replaced.

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

## Decisions log

| Decision | Choice | Principle |
|----------|--------|-----------|
| **INPUT before OUTPUT** | corpus first, validated on real docs; then the serving surface | data quality is the product |
| **Evidence-only; no answer LLM** | citations/validity/relations/gaps over MCP; user brings the model | we own the data, not the answer |
| **Multi-jurisdiction (2026-06-21)** | jurisdiction = config dimension; **the Postgres DB is the boundary** (one DB per country, same instance until it contends); one image, N deployments | share the core, customize behind interfaces; never fork |
| **One language per country (2026-06-21)** | index/serve/search only the binding native language; never translate; non-binding translations never indexed | translation risks legal error |
| **Food-dish codenames (2026-07-02)** | `banhmi` · `laksa` · `rendang` · proposed `tomyum`/`kaya` (+ domains) | consistent, memorable, per-country identity |
| **Seam registry before #3 (2026-07-02)** | consolidate the 2-way `vn`/`my` switches into one jurisdiction descriptor before adding a third | prevent N-way `case` drift |
| **Deploy shape** (2026-06-01→v0.3.0) | *Current:* worker local → RDS → Cloud Run (OpenVINO) → Firebase. *v0.3.0:* Cloud Run Job (L4, ONNX) → RDS ← Cloud Run (ONNX) ← Firebase. Read path stays Cloud Run (decided 2026-07-06) | scale-to-zero; ONNX everywhere; zero maintenance |
| **Temporal removed (2026-07-06)** | `cmd/pipeline` calls activity methods directly; structured slog output; no Temporal server needed | simplify; Cloud Run Jobs don't need durable workflows |
| **Hybrid retrieval (2026-06-22)** | dense BGE-M3 + native pgvector `sparsevec` BM25 + RRF + query router; no `pg_search` | beats vector-only on eval; single datastore; RDS-portable |
| **"Coded" ≠ "validated"** | tracked separately, always | never ship unvalidated extraction as done |
| No hardcoded policy lists | vocab in `config` schema, seeded from CSVs | edit CSV + re-seed, no code change |
| No AI as canonical parser | deterministic extraction; OCR batched, gated, never sole binding source | never generate legal text |
| PDF engine | go-fitz (MuPDF via purego, no CGO). MarkItDown removed. | zero-Python extraction; 15-60× faster |
| OCR | Document AI Enterprise OCR (GCS-cached, default). EasyOCR available as offline fallback. | cleaner text, no local CPU |
| Embedder | BGE-M3 ONNX INT8 everywhere (`gpahal/bge-m3-onnx-int8`). *Current deploy:* OpenVINO (query) + Kaggle (bulk). *v0.3.0:* ONNX Runtime for both query (Cloud Run CPU) and bulk (L4 GPU) | index/query parity; one model |
| **ONNX validated (2026-07-05)** | ONNX INT8 (544 MB) query embedder: hybrid recall matches OV baseline exactly (VN 85.7%, MY 95-100%); MRR ±5% from index-query mismatch, converges after re-embed | INT8 preferred over FP32 (2.2 GB) — same recall, 4× smaller |
| **No local bulk embed** | Bulk embedding on Kaggle GPU or Cloud Run L4 only — never on the dev laptop (8 GB, would OOM/overheat) | protect the dev machine |
| No composite primary keys | surrogate identity PKs; business keys `UNIQUE` | idempotent `ON CONFLICT` upserts |
| Containers | podman-first, `Containerfile` | no host installs |
| Pre-release migrations | mutable until first tagged release, then append-only | no fix-up migrations pre-v1 |
| Validity-aware retrieval | current-law leads; non-current appended, badged, capped | never present repealed law as current |
| Relation confidence split | confirmed structured relations ≠ weak text links; weak can't drive validity | evidence the agent can trust |
