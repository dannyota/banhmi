# banhmi plan

Living roadmap and progress tracker. Architecture detail in [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md);
conventions and the canonical agent guide in [`CLAUDE.md`](CLAUDE.md); the multi-country model in
[`docs/design/jurisdictions/`](docs/design/jurisdictions/). Last updated: 2026-07-02.

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
| 4 | 🇹🇭 Thailand | `tomyum`* | tomyum.danny.vn* | PROPOSED | [THAILAND](docs/design/jurisdictions/THAILAND.md) |
| 5 | 🇸🇬 Singapore | `kaya`* | kaya.danny.vn* | PROPOSED | [SINGAPORE](docs/design/jurisdictions/SINGAPORE.md) |

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

## Current state (live `corpus_status`, 2026-07-02)

**🇻🇳 VN (banhmi):** 1,608 docs total · **712 indexed** · 47,445 chunks · **100% embedded** · 8,859
confirmed relation edges · `search_ready`. **Hybrid retrieval live in prod** (eval: recall@k
85.7%→**89.3%**, mrr 78.6%→**84.6%**, current-law 100% vs vector-only). Open gaps (disclosed via
`quality_gaps`): 964 unresolved relation targets (mostly the deliberate one-level crawl boundary),
83 needs-review text docs, 27 indexed docs without binding text (badged), 4 docs without current
validity. 887 relation-context docs are deliberately unindexed (text+relations still served).

**🇲🇾 MY (laksa):** 63 docs · 8,425 chunks · **100% embedded** · 62 in-force + 1 expired ·
`search_ready` · MY golden set: **abstention 100%, recall 95%**. 1,000 P.U. relation edges are all
**stubs** (target backfill pending); 10 chunks flag mojibake-like text (review pending). Live image is
**vector-only** — the hybrid rollout for MY is pending (see Phase 0).

**Pending redeploys (coded, not yet live):** per-hit `bm25_score` MCP field (committed 2026-06-22);
mojibake remediation (UTF-8-forced HTML extract + Cyrillic gate + re-process harness, commits
14006b0/bd565ae/936be2c) — the known undetected case is doc `356/2025/NĐ-CP` (CP1251 mojibake +
collapsed structure passed the old gates); prod re-process + validation still to run.

## Roadmap

### Phase 0 — expansion pre-work (before country #3 starts)

1. **Jurisdiction seam registry — CODED.** `pkg/base/jurisdiction` replaces the scattered 2-way
   `vn`/`my` switches with one `Descriptor` registry (sources, parser, OCR languages, validity default,
   router profile, seed/golden files, DB name); VN is the compiled fallback. All switch points folded:
   config, pipeline (parser, gate, para-label, validity), retrieval (router), app wiring, cmd/seed,
   cmd/eval. Guarded by `TestSourceBuildersCoverRegistry`, `TestAllComplete`, and the per-jurisdiction
   golden-citation regression tests; zero byte changes to live corpora. MCP brief remains a `case`
   switch (irreducible: each brief is large custom text, not a field). See
   [playbook](docs/design/jurisdictions/PLAYBOOK.md#seam-registry--shipped).
2. **VN prod data quality.** Run the mojibake re-process against prod (`356/2025/NĐ-CP` + sweep),
   validate, redeploy MCP with `bm25_score`. "No error" ≠ fixed — verify the served chunks.
3. **MY (laksa) parity.** `lexindex` + hybrid rollout for laksa; P.U. relation-target backfill (1,000
   stubs → resolved); the 10 flagged chunks; layout-aware Section titles (deferred from the MY build).
4. **Freshness engine (highest leverage).** Scheduled daily discovery on the existing per-source
   cursors/watermarks → auto fetch→extract→normalize→index→embed→`-drain`, per jurisdiction. A legal
   corpus that does not self-update serves stale law. Operationalize (schedule + monitor + alert),
   don't re-architect.
5. **Validity/amendment refresh re-crawl.** Scheduled VBPL (and per-country analog) status refresh so
   replaced/amended docs can't keep a stale `in_force` (the `101/2012` gap).
6. **Eval as the permanent gate.** Grow per-jurisdiction golden sets (`golden.json`, `golden_my.json`,
   then `golden_<cc>.json`); every retrieval/ingestion change ships with an eval delta. Realistic user
   phrasing only.
7. **Drift & quality monitoring.** Track abstain rate, gaps, validity-unknown, embedding coverage,
   corpus counts over time; alert on regression.
8. **Infra gate.** RDS `db.t4g.micro` is already tight with VN + MY + Temporal — size to `t4g.small`
   (or split instances) **before** loading country #3; consider the 1-yr reserved instance once
   free-tier eligibility lapses.

### Countries #3–#5 (each follows the [playbook phase template](docs/design/jurisdictions/PLAYBOOK.md#phase-template-per-country))

- **#3 🇮🇩 Indonesia (`rendang`, proposed).** Sources (candidates): OJK (POJK/SEOJK — bank IT),
  Bank Indonesia (PBI/PADG — payments/QRIS/SNAP), peraturan.go.id (UU/PP + structured status
  relations). Indonesian corpus; Pasal/ayat/huruf model ≈ VN's walk. Main risks: JDIH portal
  fragmentation, scan share in older regs. First step: live source-verification spike (the MY bar).
- **#4 🇹🇭 Thailand (`tomyum`, proposed).** Sources (candidates): BOT notifications, Krisdika
  consolidated Acts, Royal Gazette signal (+ scoped PDPC/ETDA/SEC). Thai corpus; มาตรา/วรรค + ข้อ
  models. **Heaviest language work:** Thai has no word spaces → the BM25 hashing tokenizer needs a
  segmentation decision (dictionary segmenter vs char-n-grams vs vector-primary interim); B.E.↔C.E.
  date normalization; Thai numerals.
- **#5 🇸🇬 Singapore (`kaya`, proposed).** Sources (candidates): MAS (Notices binding + Guidelines),
  SSO (consolidated Acts in **HTML** — best structure since VBPL), scoped PDPC/CSA. English corpus;
  MY citation family near-reuses. Gate: SSO bot-protection/ToS compliance check. Instrument-class
  badging (Notice vs Guideline) must be explicit.

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
