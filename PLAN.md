# banhmi plan

Living roadmap and progress tracker. Architecture detail in [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md);
conventions and the canonical agent guide in [`CLAUDE.md`](CLAUDE.md); the multi-country model in
[`docs/design/jurisdictions/`](docs/design/jurisdictions/). Last updated: 2026-07-15.

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
| 4 | 🇸🇬 Singapore | `kaya` | kaya.danny.vn | **v0.4.0** | [SINGAPORE](docs/design/jurisdictions/SINGAPORE.md) |
| 5 | 🇹🇭 Thailand | `tomyum` | tomyum.danny.vn | **v0.5.0** | [THAILAND](docs/design/jurisdictions/THAILAND.md) |

**Build order: SG → TH** (confirmed). SG is the cheapest build (English, MY citation family, SSO
HTML statute trees); TH last — heaviest language work (Thai word segmentation for BM25,
Buddhist-Era dates, Thai numerals).

## Deployment shape

See the v0.3.0 architecture block below. Key points:
- **Read path:** CloudFront → ECS on EC2 t4g.large (ARM64) → RDS PostgreSQL 17 + pgvector.
  3 containers (VN :8081, MY :8082, ID :8083), in-process Qwen3 ONNX FP16 query embedder.
- **Write path:** local pipeline runs, dumped/restored to RDS. Bulk embed on Kaggle T4 (free).
  VN sources geo-locked (needs VN IP). ID OJK via GCE Jakarta proxy.
- **DB:** RDS `ap-southeast-1`, one DB per country (`banhmi`, `laksa`, `rendang`). Origin-SG-only;
  pipeline runs temporarily allowlist the maintainer /32.
- **GCP (remaining):** Vision OCR API (global endpoint) + the Jakarta GCE proxy only. Document AI
  dropped 2026-07-15 (its asia-southeast1 online quota is 5 pages/min; Vision allows 1,800
  req/min at the same price). GCS buckets deleted 2026-07-14; OCR cache is local files + S3
  mirror. Everything else deleted 2026-07-12.

## Current state (v0.3.1, 2026-07-15)

**Prod runs v0.3.1** (image `v0.3.1-20260715` deployed to ECS 2026-07-15; tag + GitHub release
published; `corpus_status` verified live).
All three corpora rebuilt and restored to RDS 2026-07-14 (dense 100% + BM25 sparse 100% each).
Large restores now go dump → S3 → disposable EC2 in the RDS VPC → `pg_restore -j8` (backbone
speed, survives flaky local links); the box self-terminates and temp key/SG are removed.

**🇻🇳 VN (banhmi):** 1,739 docs · 58,890 chunks (incl. OCR floor) · 100% embedded + sparse ·
RDS restored 2026-07-15. Metadata priority dedup (vbpl=10 wins metadata, best text wins content).
**Eval (2026-07-15 evening, local, 54 cases):** recall 81.5%, MRR 58.1%, current-law 100%,
abstention 100% — **accepted baseline** (provision matcher + golden fixes + K=100; not
comparable to the pre-matcher 83.3%). Residual failures map to v0.3.2 items 1–3. **Local DB and
prod RDS both repaired 2026-07-15** (validity-starvation regression — see milestone history).

**🇲🇾 MY (laksa):** 97 docs · 10,651 chunks (scope expanded 2026-07-15: SC recognized markets,
digital-asset/IEO terms) · 100% embedded + sparse · RDS restored 2026-07-15.
**Eval (2026-07-15 evening, local, 51 cases, 6 Section-level):** recall 80.9%, MRR 64.6%,
current-law 100%, abstention 100% — accepted post-expansion baseline (K=100 +1 case; fabricated
`dsa-intermediary` case converted to an abstain control — see item 5; prior doc-level 46-doc
corpus 87.5%/76.7% not comparable). Gaps driving misses: v0.3.2 items 4–5.

**🇮🇩 ID (rendang):** 1,618 docs · 98,050 chunks (citation-label fix + OCR floor, full re-embed) ·
redeploy in progress 2026-07-15. `ojkweb` source (full OJK POJK/SEOJK catalogue via GCE Jakarta proxy).
**Eval (2026-07-15 evening, local, 88 cases, 12 Pasal-level):** recall 69.4%, MRR 58.4%,
current-law 100%, **abstention 100%** (item 8 fixed: 11 false abstains were all scope-gate misses —
ID reference shapes Perpres/PMK/Perppu added to the known-reference detector, 16 scope terms
seeded; `padg-bilateral-revoked` promoted from expect_fail). Remaining gaps: items 6–7.

**Kaggle embed fixed (root-caused):** dual-T4 OOMs came from BFC-arena region buildup across
different batch shapes (`kSameAsRequested` never returns regions to CUDA). Fix: per-run arena
shrinkage via `RunOptions` (each `sess.run` starts from a near-empty arena) + descending pad
order; TOKEN_BUDGET stays 128k. VN (50.6k) and ID (97.6k) embedded clean on first post-fix runs.

**OCR backfill complete (2026-07-15), engine now Vision:** the Document AI paths (GCS batch,
then sync ProcessDocument) died on the 5 pages/min asia-southeast1 quota; the engine was
rewritten around **Vision `images:annotate`** (page-per-request JPEG, builtin/latest,
1,800 req/min quota, same $1.50/1k price) with a file-first OCR cache (local + S3 mirror).
Backfill results: VN 106 docs → **+8,334 chunks** (58,890 total, redeployed); MY 8 docs
(97-doc expanded-scope corpus, 10,651 chunks, redeployed); ID full re-chunk+re-embed
(98,050 chunks — reconciles the citation-label fix) finishing, redeploy to follow.

## Roadmap

### v0.3.0 — AWS read path, Qwen3-Embedding, 3 jurisdictions — COMPLETE

**Shipped 2026-07-12; corpus rebuild 2026-07-14.** Read path migrated from GCP Cloud Run to AWS
(CloudFront + ECS on EC2 ARM64 Graviton). Embedder switched from BGE-M3 to Qwen3-Embedding-0.6B
ONNX FP16. GCP teardown complete (only Vision OCR API remains). ID revived 2026-07-12
with `ojkweb` source. All three jurisdictions live on one RDS instance.

**Architecture (prod):**
```
READ PATH — AWS (ap-southeast-1), always-on:
  CloudFront (3 distributions, ACM TLS, *.danny.vn)
    ├─ banhmi.danny.vn  :8081  (VN)
    ├─ laksa.danny.vn   :8082  (MY)
    ├─ rendang.danny.vn :8083  (ID)
    │
  EC2 t4g.large (ARM64, EIP, host networking, X-Origin-Verify)
  RDS PostgreSQL 17 + pgvector (t4g.small, 3 DBs)
  In-process Qwen3 ONNX FP16 query embedder (~2.3 GB/container)

WRITE PATH — local pipeline runs, dumped/restored to RDS.
  Embed: Kaggle T4 GPU (free, dual-T4 shape-bucketed batching).
  OCR: Vision images:annotate (file+S3 cached). Extract: go-fitz (MuPDF).
  VN: local (VN IP). ID: GCE Jakarta proxy for OJK.
```

**Cost:** ~$87/mo (EC2 $49 + RDS $26 + CloudFront/EIP/S3/ECR ~$12). Drop to ~$72 with 1yr RI.
Embed free (Kaggle T4). Lever: INT8 query model → t4g.medium (needs eval gate).

### v0.3.1 — MCP token optimization — DEPLOYED (2026-07-14)

**Goal:** let agents query exactly what they need per workflow phase (discovery → read → deep
read) instead of paying for the full evidence pack on every call. Detail levels shape response
size only — never ranking; data-quality signals (`needs_review`, `validity.warning`) survive
every level. Jurisdiction-neutral (same mechanics for VN/MY/ID/SG/TH).

**Deployed to prod 2026-07-14** (verified live — response-shape check on banhmi.danny.vn confirms
`standard` detail default and provision pointers without inline text):
1. **`search` `detail` param** — `compact` (discovery: metadata + snippet + cite + validity badge;
   no provisions/relations/related_hits; skips related retrieval), `standard` (**new default**:
   adds relations + related_hits + provision *pointer*; never inlines article text; trims
   provenance arrays + per-arm scores), `full` (legacy pack, inline `provision.text`).
   Unknown value → standard + warning.
2. **`document` `include` param** — `chunks | relations | amendments | timeline | provenance`;
   omitted = all but `provenance`. Metadata, validity, sources, text_summary, gaps always return.
   Timeline still folds amendment events when amendments aren't requested; the amended-by gap is
   never hidden. `include=['chunks']` + `citation` = cheapest one-provision read.
3. **Guide/brief updates (VN/MY/ID)** — two-pass pattern (compact search → scoped document),
   detail/include documented in tool descriptions + evidence contract.

**Measured (8-hit search, real Điều text; largest local doc):**
| Call | Bytes | ~Tokens | vs full |
|---|---|---|---|
| search full (old default) | 47,295 | ~11.8K | — |
| search standard (new default) | 21,655 | ~5.4K | **−54%** |
| search compact | 17,670 | ~4.4K | **−63%** |
| document full doc (default include) | 40,478 | ~10.1K | — |
| document one provision (`citation` + `chunks`) | 3,277 | ~0.8K | **−92%** |
| **Two-pass workflow (compact + one provision)** | | **~5.2K** | **−76%** vs 21.9K |

**Remaining:** observe real agent sessions (Claude/ChatGPT) adopting the two-pass pattern via
the updated guide. (Detail rank-invariance verified 2026-07-15 by the local MCP smoke: compact
and standard return identical orderings on all three jurisdictions.)

### v0.3.1b — Amendment-chain awareness — DEPLOYED (2026-07-14)

**Goal:** agents must never rely on stale amendment text. VN pattern (Circular 09 ← 50 ← 77):
an amender is itself amended. 340 VN + 332 ID documents sit on ≥2-hop chains. Citator model
(KeyCite/Shepard's): per-document treatment lineage + currency warnings, evidence-only (banhmi
never interprets what changed). Config-driven (`config.relation_type.is_amending`), so mechanics
are jurisdiction-neutral; MY inert (0 amending edges — consolidated Acts).

**Deployed to prod 2026-07-14** (rendang dump/restored to RDS tonight; 1,198 promoted ID
relations live; `target_amended_by` citator fields verified on banhmi.danny.vn):
1. **ID relation promotion (write path)** — bi/bpk structured status metadata (Mengubah/Mencabut)
   now resolves via `config.relation_type` and promotes to confirmed `document_relation`
   (`official_metadata`, confidence 0.9). Reverse operators (Diubah dengan → amended_by,
   is_amending=false) stay weak — the forward edge comes from the amender's own page. Backfill
   ran (1,618 docs, 0 failures): rendang 0 → **1,198 confirmed relations** (719 revokes,
   479 amends); related_hits now seed for ID too. VN/MY write path byte-identical (pinned by tests).
2. **`amendment_chain` (document tool)** — recursive lineage walk (depth ≤4, cycle-safe,
   is_amending types only), emitted under `include=['amendments']` **only when depth ≥ 2**;
   nodes carry depth/doc_number/relation_type/effective_from/validity badge/indexed. New
   `amendment_chain` gap points the agent at the newest treatment. Walk costs **1.6 ms** on the
   deepest real VN chain.
3. **`target_amended_by` (search + document relations)** — citator-style currency warning on any
   relation whose target is itself further amended (one batch query per search, best-effort;
   base doc excluded; ≤8 doc numbers, omitempty).
4. **Briefs (VN/ID)** — evidence-contract + guide lines teaching chains; MY brief untouched.

**Remaining:** none — ID baselined 2026-07-15 (see Current state).

### v0.3.2 — Eval-driven corpus & retrieval fixes — IN PROGRESS

**Source:** 2026-07-15 local baselines (JSON reports in `test/samples/eval/`) + local MCP smoke
+ agent investigations (all findings DB-verified). Eval exists to show what to improve; every
item carries its evidence and names its path — **WRITE = pipeline/index (local run + RDS
redeploy), READ = MCP server (code deploy only).** MCP contract is healthy (smoke passes on
VN/MY/ID; detail levels rank-invariant).

**Done 2026-07-15** (details in git history): VN validity-starvation root-caused → normalize
selector fixed (priority pick + reopen) → local + prod corpora repaired (recall 61.1→79.6);
VectorK/BM25K 50→100 shipped (MY/ID +1 case each, VN MRR +0.7, no regressions); ID abstention
87.5→100% (Perpres/PMK/Perppu reference shapes + 16 scope terms — **prod needs `cmd/seed` on
next deploy**); provision-level golden sets for VN/MY/ID; fabricated MY "DSA" case converted to
a hallucination-resistance control; golden housekeeping (padg expect_fail dropped, 2 VN fixes).

**Open items:**

**VN:**
1. **Label-only Điều chunks — WRITE. Investigated 2026-07-15:** 1,007 true label-only chunks /
   196 docs, three strata. (A) 630: `splitLongChunkContent` heading orphans — body exists in
   sibling Đoạn chunks; fix = add `label + ". " + heading` to the `labelOnlyChunk` candidates
   (index_activities.go:629). (B) 365: vbpl provision tree delivers EMPTY bodies for short
   articles (text exists in document_text markdown) — fix = normalize-stage markdown fallback;
   these are genuinely missing provisions. (C) 12 parser edge cases, low priority. Blast
   radius: ~196 docs re-chunk → ~15k chunks re-embed (Kaggle). No golden case blocked — this is
   evidence quality, not recall.
2. **Large-law × sector queries — DECISION NEEDED:** 5 cases (116/2025 ×2, 91/2025, 94/2025,
   infosec-general) where the expected national-law Điều lacks the query's banking terms;
   K-depth doesn't reach them (proven). Either accept as the connecting model's bridging job
   (relax goldens to doc-level) or find a query-side lever. `ekyc-17-2024` amendment citation
   stays an honest failure (rank-1 hit carries the `amends` relation).
3. **Diacritics / script normalizer — READ+WRITE, jurisdiction-neutral seam (TH blocker):**
   READ: deterministic query diacritic restoration (corpus-derived, no LLM) before dense
   embedding when the router flags a diacritic-free query (evidence:
   `edge-no-diacritics-payment`). WRITE: move the hardcoded VN fold out of `pkg/rag/lexical`
   into a descriptor-selected normalizer (TH needs Thai normalization + word segmentation;
   NFD-strip would destroy Thai). Normalizer changes re-run `cmd/lexindex` only.

**MY (diagnosis 2026-07-15 — path to ~94–98% recall):**
4. **BNM PD body-extraction gap — WRITE, highest MY leverage (3 cases).** The AML PD (394 KB
   markdown → only ~42 KB in sections) and e-money PD parse into appendix items only; the
   S/G-numbered body (STR obligations, fund safeguarding) never becomes sections. Suspected:
   the MY parser doesn't treat "S 8.1"/"G 9.1" markers as section boundaries; also ensure the
   full-text-paragraph fallback fires when no body sections parse. Pairs with:
5. **FSA/IFSA body extraction — WRITE (2 cases).** Acts 758/759: Schedule-only chunks, zero
   body Sections (investigation running). Also PDPA: corpus holds the 2024 Amendment Act, not
   the consolidated 2010 Act users cite.
6. **SC empty-doc_number noise — WRITE (corpus quality).** 2,679 chunks (25.2% of laksa!) from
   36 SC docs have blank doc_number — dedup (keyed on doc_number) never fires: 12 duplicate
   copies of "Guidelines on Recognized Markets" (~1,600 chunks of noise) pollute ranking and
   push expected BNM PDs down. Fix: derive/assign SC doc identifiers + dedup + re-index.
7. **Discovery — WRITE (2 cases).** Absent: BNM/PD-OUTSRCE, BNM/PD-IOP (real, published PDs).
   Candidate to add to scope: **Online Safety Act** (passed Dec 2024; verify gazettal + act
   number — Malaysia has NO "Digital Services Act"; Act 847 is the death-sentence-revision act).

**Shared retrieval:**
8. **Per-document cap in the primary pass — DECISION NEEDED (predicted 2–3 MY cases + VN
   multi-doc cases + ID cross-doc cases).** Evidence: Act 854's 174 chunks consume all top-8
   slots (Section 22 diluted across 8 subsection fragments); Labuan Acts 704/705 (1,445 chunks)
   outrank the mainland Act 627 equivalent. Risk to check: must not regress cases where one doc
   legitimately dominates (RMiT). Eval-gated on all three jurisdictions.
9. **Reranker (MRR lever) — gated experiment, not committed.** Candidate: Qwen3-Reranker-0.6B
   (family match; eval harness already has `-rerank-*` + Qwen3 template). Step 1: measure MRR
   ceiling locally over VN/MY goldens; abandon if < +5 pts. Step 2 (only if real): INT8 CPU
   top-12 rescoring on the read path (~2–4 s/query — benchmark on Graviton first; no GPU).
   ONNX availability research running.

**ID (parked until VN/MY land):**
10. **Extraction truncation — WRITE.** UU 27/2022 (PDP) indexed only to Pasal 22 of 76 — breach
    notification + sanctions chapters missing. UU 4/2023 (P2SK omnibus) buried under
    "Pasal 10, ayat (N)".
11. **Discovery — WRITE.** POJK 40/2024 (only its revoked predecessor present) and
    SEOJK 29/SEOJK.03/2022 absent (newest SEOJK in corpus is 2020).

### v0.4.0 — Singapore (`kaya`)

Add Singapore as the fourth jurisdiction. English corpus; MY citation family near-reuses.
Follows the [playbook](docs/design/jurisdictions/PLAYBOOK.md) and
[SINGAPORE design](docs/design/jurisdictions/SINGAPORE.md).

1. **Sources:** MAS (Notices + Guidelines, sweep-all), SSO (consolidated Acts in HTML,
   keyword-filtered), scoped PDPC/CSA. Gate: SSO bot-protection/ToS check.
2. **Structure parser:** Section/Part/Chapter hierarchy (MY-family, near-reuse).
3. **Scope vocabulary:** `scope_term_sg.csv` + `discovery_keyword_sg.csv` seed.
4. **Golden set:** `golden_sg.json` — practical scenario-based questions in English.
5. **Deploy:** fourth ECS container `:8084`, CloudFront distribution, RDS `kaya` DB.
6. **Eval gate:** recall/MRR baseline before going live.

### v0.5.0 — Thailand (`tomyum`)

Add Thailand as the fifth jurisdiction. Thai corpus — **heaviest language work** in the roadmap.
Follows the [playbook](docs/design/jurisdictions/PLAYBOOK.md) and
[THAILAND design](docs/design/jurisdictions/THAILAND.md).

1. **Sources:** BOT notifications, Krisdika consolidated Acts, Royal Gazette.
2. **Language work:** Thai word segmentation for BM25, B.E./C.E. date normalization, Thai numerals.
3. **Structure parser:** มาตรา (section) hierarchy.
4. **Deploy:** fifth ECS container `:8085`, CloudFront distribution, RDS `tomyum` DB.

### MVP2 candidates (parked)

Gemma 4 E4B OCR enhancement · figure extraction · manual-folder source · crawl depth >1 ·
`sbv.gov.vn` extra source · cross-encoder reranker · validity/amendment refresh re-crawl ·
drift & quality monitoring.

## Milestone history

- **2026-06-01** — VN deployed (Cloud Run + Firebase).
- **2026-06-22** — Malaysia deployed. Hybrid retrieval (BM25 + RRF).
- **2026-07-06** — Indonesia LIVE. Temporal removed.
- **2026-07-08** — Qwen3-Embedding FP16, go-fitz, Document AI OCR.
- **2026-07-12** — **v0.3.0 shipped.** AWS read path (CloudFront + ECS ARM64). GCP teardown. ID revived.
- **2026-07-13** — ID scope rebuild (bpk sweep-only, issuer-mandate scope, ojkweb source).
- **2026-07-15** — Eval overhaul day: jurisdiction-aware provision matcher + local eval targets;
  VN validity-starvation regression root-caused, selector fixed, local+prod corpora repaired;
  K=100; ID abstention 100%; all three jurisdictions baselined with floors.
- **2026-07-14** — Corpus rebuild + RDS restore, all 3 jurisdictions (metadata priority,
  Kaggle dual-T4 OOM root-caused: per-run arena shrinkage, ojkweb SharePoint scraper,
  per-jurisdiction cache dirs, inline-Pasal parser fix). S3→EC2 restore pattern.
  **v0.3.1 + v0.3.1b deployed** (MCP token optimization + amendment-chain awareness).
- **2026-07-15** — OCR engine → **Vision images:annotate** (Document AI quota-blocked at
  5 pages/min); file-first OCR cache; OCR backfill (VN +8,334 chunks); MY scope expansion
  live (46 → 97 docs); ID citation fix + full re-embed. **v0.3.1 tagged, released, and
  image `v0.3.1-20260715` deployed to ECS.**

## Decisions (settled)

| Decision | Choice |
|----------|--------|
| Evidence-only; no answer LLM | citations/validity/relations/gaps over MCP; user brings the model |
| One language per country | index/serve/search the binding native language only; never translate |
| Deploy shape | local pipeline → Kaggle T4 embed → dump/restore RDS ← ECS ARM64 ← CloudFront |
| Hybrid retrieval | dense + pgvector `sparsevec` BM25 + RRF; no ParadeDB |
| Qwen3-Embedding-0.6B FP16 | 1024 dims, 32K context, ONNX FP16 everywhere; ~2.3 GB/process |
| go-fitz + Document AI OCR | zero-Python extraction; sync ProcessDocument, S3-cached OCR |
| Kaggle-only embedding | free T4 GPU, fresh per run, dataset I/O |
| No composite PKs | surrogate identity + UNIQUE business keys |
