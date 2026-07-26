# banhmi plan

Living roadmap and progress tracker. Architecture detail in [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md);
conventions and the canonical agent guide in [`CLAUDE.md`](CLAUDE.md); the multi-country model in
[`docs/design/jurisdictions/`](docs/design/jurisdictions/). Last updated: 2026-07-26.

## Vision

A self-hostable, **multi-country** platform for Southeast-Asian **banking & financial regulation** and
**cross-cutting technology law**: one codebase that crawls each country's official sources, builds a
clean, citable corpus in that country's binding legal language, and **serves it as evidence over MCP** — exact native citations
(Dieu/Khoan, Section/Subsection, Pasal/ayat), validity, relations, provenance, and explicit gaps.

- **One codebase, one corpus per country** — separate database, MCP service, and domain per
  jurisdiction ([playbook](docs/design/jurisdictions/PLAYBOOK.md)). Never a branch or fork.
- **The data is the product; the user brings the model.** No built-in answer LLM — hosted agents
  connect over MCP and reason over the evidence themselves.

> **Status convention:** "coded" = code written + unit/integration tests; "validated" = checked on real
> documents. Never report one as the other.

## Jurisdictions

| # | Country | Codename | Endpoint | Status | Design |
|---|---------|----------|----------|--------|--------|
| 1 | Vietnam | `banhmi` | banhmi.danny.vn/mcp | **LIVE** (2026-06-01) | [SOURCES](docs/design/SOURCES.md) |
| 2 | Malaysia | `laksa` | laksa.danny.vn/mcp | **LIVE** (2026-06-22) | [MALAYSIA](docs/design/jurisdictions/MALAYSIA.md) |
| 3 | Indonesia | `rendang` | rendang.danny.vn/mcp | **LIVE** (revived 2026-07-12) | [INDONESIA](docs/design/jurisdictions/INDONESIA.md) |
| 4 | Singapore | `kaya` | kaya.danny.vn/mcp | **LIVE** (2026-07-17) | [SINGAPORE](docs/design/jurisdictions/SINGAPORE.md) |
| 5 | Thailand | `tomyum` | tomyum.danny.vn/mcp | **LIVE** (2026-07-17) | [THAILAND](docs/design/jurisdictions/THAILAND.md) |
| 6 | Cambodia | `amok` | amok.danny.vn/mcp | **LIVE** (2026-07-18) | [CAMBODIA](docs/design/jurisdictions/CAMBODIA.md) |

All 6 jurisdictions shipped on one codebase, one ECS instance, one RDS.

## Deployment shape

- **Read path:** CloudFront (6 distributions, ACM TLS) → ECS on EC2 t4g.medium (ARM64 Graviton) → RDS PostgreSQL 17 + pgvector.
  **Two ECS services on the one host:** a slim MCP container serving all 6 jurisdictions (routed by
  `X-Banhmi-Jurisdiction` header, host networking) + the Qwen3 ONNX FP16 embedder sidecar on loopback
  `:8089` (`BANHMI_EMBED_CONCURRENCY=10`). Split at v0.4.0 — ORT pre-packs weights into private
  anonymous memory (~2.1 GB RSS per process), so one shared embedder, not one per jurisdiction.
- **Write path:** local pipeline runs, dumped/restored to RDS. Bulk embed on Kaggle T4 (free).
  VN sources geo-locked (needs VN IP; Hanoi Local Zone EC2 relay). ID OJK via GCE Jakarta proxy.
  TH SEC via AWS Bangkok proxy (ap-southeast-7, on-demand).
- **DB:** RDS `ap-southeast-1`, one DB per country (`banhmi`, `laksa`, `rendang`, `kaya`, `tomyum`, `amok`).
  Origin-SG-only; pipeline runs temporarily allowlist the maintainer /32.
- **GCP (remaining):** Vision OCR API (global endpoint) + Jakarta GCE proxy only.
- **S3 data buckets:** `danny-banhmi-data-{vn,my,id,th}` (file cache + OCR cache mirror).
- **Cost:** ~$65/mo (EC2 t4g.medium $25 + RDS $26 + CloudFront×6/EIP/S3/ECR ~$15).

## Current state

**Prod runs 6 jurisdictions**, all serving **`v0.4.7-20260725`** (verified live 2026-07-26). Corpus sizes
are the prod-verified `corpus_status` values; eval metrics are the accepted local baselines (floors in
the Makefile track these). All six were re-measured 2026-07-25/26; VN's MRR is the post-restore
figure (73.0, from the prod dump — see the force-rebuild note in Open/queued), TH's reflects the
SEC ingest plus the golden `alt_doc_numbers` correction:

| Jurisdiction | Docs | Chunks | Recall | MRR | In-force | Abstain | Cases | Floors (R/M/I/A) |
|---|---|---|---|---|---|---|---|---|
| VN (`banhmi`) | 3,974 | 130,707 | 93.8% | 73.0% | 100% | 100% | 93 | 0.90/0.66/0.99/0.95 |
| MY (`laksa`) | 109 | 11,304 | 94.3% | 79.6% | 100% | 100% | 72 | 0.92/0.77/0.99/0.95 |
| ID (`rendang`) | 2,371 | 160,142 | 79.8% | 62.4% | 100% | 100% | 110 | 0.78/0.60/0.99/0.98 |
| SG (`kaya`) | 292 | 27,951 | 93.5% | 76.9% | 100% | 98.7% | 76 | 0.90/0.75/0.99/0.90 |
| TH (`tomyum`) | 1,790 | 32,715 | 89.5% | 69.6% | 100% | 96.6% | 58 | 0.86/0.68/0.99/0.90 |
| KH (`amok`) | 284 | 7,757 | 94.4% | 72.7% | 100% | 100% | 42 | 0.90/0.70/0.99/0.95 |

**Workflow eval (agent contract):** 10-case VN pilot, Haiku stand-in agents over `tools/mcpcall`,
scored by `tools/wfscore`: citation 85.0%, abstention 100%, relation-following 83.3%.
See [`docs/design/WORKFLOW-EVAL.md`](docs/design/WORKFLOW-EVAL.md).

## Roadmap

### v0.4.8 — Relation evidence & amendment surface — CODED, NOT DEPLOYED (2026-07-26)

Five fixes from the relation-integrity investigation (findings A–E in [Open / queued](#open--queued)).
All read-path or seed-only: **no re-index, no re-embed, no citation bytes changed.**

1. **National Assembly law numbers were invisible to text-derived relations.** `docNumberMentionRe`
   required a hyphenated suffix, so `TT-NHNN`/`ND-CP` matched but `QH13/QH14/QH15` never did — every
   Luật and Nghị quyết was skipped. `116/2025/QH15` records no outbound relations despite Điều 43–44
   amending and repealing ~13 laws. Bare dates (`06/10/2011`) still correctly do not match.
2. **`amendment.lead_verbs` was Vietnamese in every jurisdiction's database** (`config.setting` has no
   jurisdiction column), so `document(include=['amendments'])` returned an empty set for ID/MY/SG/TH/KH.
   Per-jurisdiction `setting_<code>.csv` now loads **before** the defaults (`ON CONFLICT DO NOTHING`,
   first write wins). All six DBs verified.
3. **The lead verb was anchored as a prefix**, which only matches Vietnamese drafting; English reads
   *"The principal Act is amended by…"*. Measured on MY: every verb matched **zero** sections as a
   prefix. Matching anywhere takes VN from **839 → 1,430** documents with amendment clauses.
4. **Amendment sets are now bounded and the truncation is disclosed.** `54/2014/QH13` would have
   returned **911 clauses / 471,913 chars** (~157K tokens) in one call; clause size varies ~10× across
   jurisdictions (VN Khoản ~650, ID Pasal ~7,300). Per-clause 1,500 + set budget 24,000, verified over
   MCP at 36 clauses / 24,113 chars plus an explicit "64 further … omitted" note.
5. **Straight-quoted replacement blocks leaked fake structure.** `startsWithOpeningQuote` accepts `"`
   and `“`, but `updateQuotedBlock` counted curly quotes only, so a straight-quoted block was suppressed
   on its first line and leaked afterwards. Affects future normalize runs only; existing sections
   untouched.

Also seeded: `chứng khoán`, `ủy ban chứng khoán nhà nước`, `đầu tư` (`strong_title`). Relation-backfilled
documents are scope-matched on **title alone**, so Luật Chứng khoán `54/2019/QH14` and Luật Đầu tư
`143/2025/QH15` were `relation_context` with 0 chunks while the laws amending them were indexed.
**Not yet applied** — flipping the 8 securities + 72 investment documents needs re-index + Kaggle embedding.

### v0.3.2 — Eval-driven corpus & retrieval fixes — COMPLETE (final deploys 2026-07-19)

An eval-driven pass over every corpus: retrieval tuning, chunk/citation hygiene, source-parser and
`doc_key` convergence repairs, and the scan-layer/OCR gate. The mechanisms and traps now live in the
design docs — this entry keeps the outcome only.

- **Retrieval** — VectorK/BM25K 50 → 100, `doc_cap` 3, promotion-only section aggregation,
  abbreviation expansion on both arms, VN diacritic restoration (incl. bigrams):
  [`RAG.md`](docs/design/RAG.md).
- **Corpus quality** — duplicate-citation fix (VN `Đoạn N` / MY `[N]`), intra-doc dedup + 20-rune
  minimum, `penjelasan` excluded from chunking, cross-source `doc_key` canonicalization,
  relation-type seeding: [`RAG.md`](docs/design/RAG.md), [`SCHEMA.md`](docs/design/SCHEMA.md).
- **Extraction** — scan-layer gate, alias-wide OCR selection, Vision `images:annotate` + file-first
  cache, page-stamp stripping on both text paths: [`EXTRACTION.md`](docs/design/EXTRACTION.md).
- **Sources** — MY marginal-note pre-split, BNM/SC fixes, OJK/BI/BPK normalizers, KH NBC fetch
  repair: [`SOURCES.md`](docs/design/SOURCES.md),
  [`jurisdictions/`](docs/design/jurisdictions/README.md).

**Outcome:** baselines accepted 2026-07-19 on all six local corpora with the `eval-*` floors raised to
track them (numbers in [Current state](#current-state)); current-law and abstention 100% everywhere.
Remaining misses are ranking-side (pool-recall ≥98%) — see the reranker decision below.

### Reranker experiment — CONCLUDED: NOT DEPLOYING (2026-07-19)

**Decision: keep current retrieval; no reranker.** The naive first pass (rerank all deep
candidates) measurably HURT all three corpora — VN 92.7→82.9, MY 94.3→92.9, ID 79.8→76.3 —
because a semantic reranker eagerly promotes superseded versions of the right rules
(current-law precision fell to 86.7% on VN, whose supersession chains are longest). A
production-shaped round 2 (rerank strict current-law candidates only, badged tail pinned)
was prepared but cancelled: MY's flat result and the latency economics (measured 0.76 s/pair
CPU INT8 ⇒ top-16 ≈ 12 s/query; ID's misses hide at pool ranks 22–79 needing N≈50 ≈ 38 s)
made the ceiling not worth the read-path cost at current quality. Current baselines accepted.

**Retained for a future revisit** (bigger reranker, grown corpora, or GPU quota): the full
offline harness — `cmd/eval -rerank-dump` / `-rerank-scores` (candidates out, scores
replayed through the standard matcher with the non-current tail preserved) and
`tools/rerankkernel` (T4-pinned Kaggle scoring with dataset readiness gating; `-cleanup`
removes the kernel+dataset). The long-game alternative stays reranker-as-teacher:
distill its judgments into embedder fine-tuning (MVP2).

### v0.3.3 — Post-v0.3.2 hardening — TAGGED (2026-07-19)

Tags the 23 commits since `v0.3.2` (the code behind the 2026-07-19 deploys and baselines above):

1. **Extraction trust:** scan-layer gate (`fitz.ScanStats` + `extract.pdf.max_scan_image_ratio`),
   alias-wide OCR selection in `OcrAll`, ojkweb FAQ demotion, `needs_review` fallback rule.
2. **Retrieval:** sparse-arm abbreviation expansion; `doc_cap=3` default; eval-matcher canonicalization.
3. **Identity:** docKey canonicalization + dup merges (VN/MY/ID), BNM file-stem URL-decode + MY rename,
   sbvhanoi zero-pad + type-from-suffix, OJK number-infix type override, Setneg stamp stripping.
4. **Eval:** floors raised to the accepted baselines (all six), KH scope vocabulary, reranker
   experiment harness + the NOT-DEPLOYING decision (section above).
5. **Code review (4 Opus agents over the full v0.3.2..HEAD diff, 2026-07-19):** no critical or major
   defects; minors fixed in the tag — stale dense-arm-only comments in `pkg/rag/retrieve`,
   `hasNearbyGazetteHeader` rename, rerank-dump close/flush error handling, rerankkernel glob error
   split, `max_scan_image_ratio` pinned in `TestGateFromSettings`. Noted, not acted on: BNM literal-`+`
   filename edge (no real-world trigger), pre-existing TTLT suffix gap, ScanStats per-page HTML-error
   skew (PLAUSIBLE, low probability).

> **Version renumbering:** releases are git-tagged `v0.3.x`; the "v0.4.0/v0.5.0" labels previously
> attached to the SG/TH rollouts below were plan labels, not tags. From here, v0.4.0 = the embedder
> split (next section).

### TH SEC ingest — DEPLOYED (2026-07-26, corpus only)

SEC is live: **1,786 → 1,790 docs, 32,627 → 32,715 chunks = embeddings = sparse**, prod verified
(`ประกาศคณะกรรมการ ก.ล.ต. ที่ กธ. 35/2563` opens with real provision text, badged *In force*).
Corpus-only restore — the fix is write-path (`pkg/ingest/sec`), so no image rebuild and **no service
bounce**; `corpus_status` still reports `v0.4.7-20260725`, which is accurate for the code. Rollback
`tomyum_old20260726` dropped after verification; S3 mirror re-synced zero-diff (files 8,841 / ocr 1,424).

**It was never a geo-block** — see [THAILAND.md](docs/design/jurisdictions/THAILAND.md). The F5
rejects non-browser clients; the source sent a bot UA. A Bangkok proxy was launched and torn down on
that wrong assumption, and **curl misled the diagnosis twice** (it 403s where Go succeeds, just as it
silently normalized the BOT backslash URLs) — the maintainer's "I can download it in a browser"
is what corrected it. Verify with the client that actually runs, not curl.

**Known residue (small, low value):** 13 of the 16 fetched SEC rows are attachment/form entries
(`แบบรายงาน/แบบฟอร์ม`, ~5 chunks each) rather than regulations — the NRS listing carries forms as
their own rows. The 3 real notifications (กม. 17/2561, กธ. 35/2563, สธ. 64/2563) index at 10/39/34
chunks. A scope refinement could drop form rows; not worth a re-crawl on its own.

### v0.4.7 — Supersession warning + TH BOT recovery — DEPLOYED (2026-07-25)

Tag `v0.4.7`, image `e965fa87ccbe`, task-def `banhmi-mcp:23`. All six jurisdictions verified live on
`v0.4.7-20260725`.

- **Supersession warning (all six):** `validity.warning` gains a second kind — a document the source
  still badges current while a **promoted, official `replaces`/`repeals` relation** targets it names
  its superseders; banhmi never overrides the badge. Type set is config
  (`config.relation_type.is_superseding`). Verified live: `101/2012/NĐ-CP` warns of `52/2024/NĐ-CP`.
- **TH corpus:** dump → S3 → disposable EC2 → `tomyum_v2` → swap; prod verified **1,786 docs /
  32,627 chunks = embeddings = sparse**, IT-risk search serving the recovered instruments. Rollback
  `tomyum_old20260725` dropped 2026-07-25 after live verification (recovered docs retrievable,
  pre-existing corpus unharmed); safety net = the S3 dump + PITR.
- **Migrations on all six DBs** (`config/00007` is_superseding, `ingest/00005` discovered_files):
  five via SSM tunnel, tomyum via the restore itself. **Full `cmd/seed` does NOT fit through the
  tunnel** (27,908-row dictionary; transactional abort). **Reconciled same day via the maintainer's
  temp-allowlist pattern:** the RDS SG allowed the current /32 (`temp-seed-20260725`), all five DBs
  full-seeded directly (~25 min each — `cmd/seed` is one INSERT per row, so WAN RTT dominates), and
  the rule was revoked immediately after, with the closed state verified by a failing direct connect.
  Every prod DB now carries identical current seed state. **Follow-up DONE 2026-07-26:** the
  27,908-row dictionary now loads via `COPY` into a temp table + `INSERT..SELECT ON CONFLICT DO
  NOTHING` (COPY cannot express conflict handling, and operator `origin='user'` rows must survive —
  verified by test). Local seed is now sub-second; a remote seed no longer needs the temp-allowlist
  detour. The temp table is shaped `AS SELECT <cols> ... WITH NO DATA`, not `LIKE`, because `LIKE`
  carries the identity column's NOT NULL without its generator and COPY then rejects every row.
- **S3 mirror (maintainer request):** TH fetched PDFs + OCR text synced to `danny-banhmi-data-th`
  (`files/` 8,825, `ocr/` 1,422; zero-diff verified). `banhmi-ops` policy extended permanently to the
  `th`/`sg`/`kh` data buckets (they postdated the policy — this is why the 2026-07-20 sync memory did
  not reproduce).

### v0.4.6 — VBHN consolidations, vocabulary, data quality, plan-cache fix — DEPLOYED (2026-07-25)

Follow-ups 2-5 of v0.4.5 executed as one locally-tested batch (multi-agent research + review):

1. **Query-scope vocabulary**: 27 new terms (10 strong / 17 weak signal-gated) covering the take-all
   topics (retention, prudential, FX, gold, licensing, audit, accounting, special lending, branch
   network); regression test pins 20 in-scope + 16 must-abstain queries incl. diacritic-free and
   verb-object-split forms.
2. **Golden set 80 → 93**: 13 verified sweep-topic cases (incl. a Phụ lục annex case and the
   37/2026 amendment relation); the two obsolete abstain controls converted to positive CAR /
   lending-rate cases; 3 cases accept the current consolidation via alt_doc_numbers.
3. **Data quality**: 2 wrong issued_at years backfilled; 6 duplicate identities merged (QH pairs
   preserved); guards landed — doc_key type-from-suffix, slash-anchored issued_at year cross-check,
   VN spaced-diacritic cleanup (parser + vbpl tree path; 38 artifact chunks → 0).
4. **VBHN phase 2 SHIPPED**: 344 consolidations indexed as primary (2,460-doc feed → 361 identities
   → 344 with usable artifacts; placeholder/no-date rest gated). Pilot caught and fixed three design
   bugs: selector reopen loop (vbhn validity source now ranks with vbpl), validity-race duplicates
   (advisory-locked recompute + convergence guard), and the year-collision identity flaw
   (07/VBHN-NHNN exists in 13 years → doc_key now VBHN|num|year, mirrored in the extract-selector
   SQL). Family-derived validity live: newest-per-base mirrors base status (17 in_force / 106
   partial), older consolidations auto-expire (206), unresolved unknown (13); consolidates edges
   371/375 resolved; vbpl download timeout 60s → 300s (CDN caps ~4 MB/s per IP — measured).
5. **Plan-cache retrieval fix (prod-relevant)**: Postgres generic plans after ~5 statement
   executions degraded ANN recall order-dependently; pinning force_custom_plan per retrieval
   connection restored determinism and lifted eval — **recall 90.6 / MRR 69.8 / in-force 100 /
   abstain 100 on the 93-case set; floors UNCHANGED (0.90/0.66/0.99/0.95) and passing, twice
   reproduced**. Long-lived prod pools were permanently in the generic regime — deploying this
   improves production retrieval.

Corpus: 3,974 docs / 130,762 chunks (= embeddings = sparse). **DEPLOYED 2026-07-25**: RDS restore
+ swap (rollback parked as banhmi_old20260725, drop after burn-in) and MCP image v0.4.5 on ECS —
required a task-definition revision (the task def pins the image by sha tag; force-new-deployment
alone redeploys the old pin). Prod verified: corpus_status v0.4.5-20260725, retention query
in-domain with the Phụ lục rank 1, VBHN family serving with derived badges (current consolidation
rank 1, superseded one Expired). Post-deploy follow-ups (2026-07-25): rollback DB dropped;
vbpl scan artifacts fully recovered under the 300s timeout (no dead artifacts remain; 27
discovered + 5 error docs are the permanent broken-detail classes); vbpl's badly-encoded
consolidation footnote glyph (lone U+FFFD) now stripped by the VN cleanup — mojibake chunks
177 → 4 locally and on prod (scoped SQL delta, 18 docs; prod verified 130,707 = emb = sparse).
Remaining tracked: ~8 hard golden cases (deep-ranked targets — future retrieval work), the 4
preserved genuine-mojibake chunks, VBHN phase-2 leftovers (17 unresolved bases in quality_gaps).

**Follow-up batch 2 (2026-07-25, local only — not yet deployed):**

1. **Mojibake 4 → 1.** Two more fixable classes landed in the VN cleanup: single U+FFFD glued to a
   word edge ("�Cụm từ", "như sau:�" — the same vbpl footnote glyph) is stripped, and Cyrillic
   letters pixel-identical to Latin (source typo "а.2)" in 329/2025/NĐ-CP) are latinized;
   ambiguous shapes (И) stay visible. 3 docs re-normalized/indexed via top-priority aliases,
   embed delta (721/724 cache hits, 3 via Kaggle), lexindex; local invariants 130,707 = emb =
   sparse. Residue: 1 chunk (OCR "AИ" in a scanned form table, 13/VBHN-NHNN|2020) — honest.
2. **VBHN unresolved bases classified (11 unknown today, not 17).** 5 are unindexed 2017
   placeholders (no chunks — no retrieval impact). 6 indexed ones have ZERO relation rows in
   bronze (vbpl detail pages carry no relation data; 1 more points at an out-of-corpus vbpl id).
   The named bases mostly ARE in corpus (43/2015, 11/2013, 22/2014, 479/2004, 24/2012) — a
   deterministic footnote-text base parser ("sửa đổi, bổ sung một số điều của <BASE>") could
   resolve them; NOT built (new inference path, needs maintainer sign-off). Honest unknown until then.
3. **Hard-case decomposition (pool-k 200 probe, post plan-cache fix: 4 misses, all in pool):**
   340/2025 pool #7 (doc-cap self-crowding + stale golden — retargeted, see 4), 58/2021 pool #7
   (consolidation-family crowding: 41/VBHN + base 21/2024 hold ranks 1 and 4 with identical text),
   39/2016 pool #15 and 83/2025 pool #18 (true ranking failures; 39/2016 also family-split with
   06/VBHN-NHNN|2026). **Corpus-wide: 324 indexed consolidations have their base indexed too,
   ~127 current — near-identical text competes twice in the current-law pass.** Proposed (pending
   sign-off): consolidation-family-aware fusion/dedup in retrieval — collapse same-family
   same-provision hits to the best-ranked representative, keep both citations in the hit.
4. **Golden retarget (data-verified):** new-penalty-it-safety-340-2025 → Điều 61 (the substantive
   penalty article; old target Điều 1 Khoản 2 Điểm q is the scope list — retrieval already ranked
   Điều 61 at 1-3, the miss was a golden artifact).
5. **Consolidation-family collapse SHIPPED (local) + VBHN text-derived bases.** Retrieval now
   collapses same-family same-citation hits to the best-ranked twin (families = connected
   components of resolved `consolidates` relations, loaded at startup; empty elsewhere = no-op;
   the current — usually consolidation — text wins, base stays reachable via the relation).
   VBHN validity: sourceless consolidations parse their base from their own gazette text
   (footnote + preamble markers, majority vote, unambiguous-match gate, reason `_text`) —
   5 of 6 resolved (unknown 11 → 6; 5 of those are unindexed placeholders, 1 honest residue).
   5 goldens gained the current consolidation as `alt_doc_numbers` (established pattern — the
   provision now surfaces under the consolidation identity). **Eval: recall 93.8 / MRR 73.6 /
   in-force 100 / abstain 100** (from 90.6/69.8; floors unchanged 0.90/0.66/0.99/0.95, full
   suite + DB integration tests green). Remaining misses: 2 true deep-ranking failures
   (39/2016 pool #14, 83/2025 pool #18).

**DEPLOYED 2026-07-25 (tag `v0.4.6`, image `e2449c36c68a`, task-def `banhmi-mcp:22`):**

- **Data delta via scoped SQL** (SSM tunnel, two transactions): 3 docs full doc-scoped replace
  (mojibake round 2) + 7 docs validity-only (VBHN text-derived bases) + 2 stub `doc_ref` rows
  resolved to documents prod already carried (they were what left 35/VBHN-NHNN|2024 unresolvable).
  Prod verified: 130,707 chunks = embeddings = sparse, **mojibake 4 → 1**, VBHN unknown 8 → 7.
- **Image**: CodeBuild from a local `git archive` (S3 source override — no push), MCP image only
  (embedder unchanged, so `embedder-latest` untouched); VERSION passed explicitly because an
  S3-source build has no `.git` for `git describe`. Task-def revision required — the def pins the
  image by sha tag.
- **Prod verified live**: `corpus_status` reports **v0.4.6-20260725**; the credit-info query returns
  35/VBHN-NHNN Điều 20 badged *Partially in force* with **no duplicate base hit** (collapse working),
  41/VBHN leads its family alone, and 37/VBHN-NHNN badges partial with the transparent reason
  `consolidates_base_status_text:THÔNG TƯ|22/2014/TT-NHNN`.
- **Known prod/local drift (accepted, honest):** local carries 2 extra unindexed documents from the
  07:56 drain (69/2025/NĐ-CP, 28/2015/QĐ-TTg — 0 chunks each) and 4 `doc_ref` rows that resolve only
  to those. Consequence: prod has 3,974 docs vs local 3,976, and 09/VBHN-NHNN|2022 stays `unknown`
  on prod (its base 28/2015/QĐ-TTg is not in the prod corpus) vs `partial` locally. Zero retrieval
  impact — the two documents hold no chunks. Closes on the next full corpus restore.

### v0.4.5 — VN SBV sweep take-all — DEPLOYED (2026-07-24, corpus only)

The vbpl SBV agency sweep (`agencyIds: [62, 908]`) became **pre-scoped**: everything the SBV issues
is banking regulation, so the feed enters without `scope.Match`, and the vocabulary precision
boundary now applies to non-SBV paths only. Policy + mechanism:
[`SOURCES.md`](docs/design/SOURCES.md). Trigger: 04/2025/TT-NHNN (records retention) and ~54 other
2025 TT-NHNN circulars had been vocabulary-dropped.

- **Corpus:** re-swept locally and swapped into prod RDS 2026-07-24 — VN 1,781 → 3,627 docs,
  52.5K → 93.3K chunks; accepted VN baseline at the time recall 90.2 / MRR 67.6. The **MCP image was
  deliberately not redeployed** (no retrieval code changed) — it shipped later with v0.4.6.
- **Also shipped:** appendix recovery on the vbpl tree path and the OCR straggler drain (6/6 — the
  failure was a missing Vision service-account key, not a Google block). Mechanisms:
  [`PIPELINE.md`](docs/design/PIPELINE.md), [`EXTRACTION.md`](docs/design/EXTRACTION.md).
- **Follow-ups all shipped in v0.4.6** above, which is the authoritative account of this work stream.

### v0.4.4 — Document file download links — DEPLOYED (2026-07-25, with the v0.4.6 image)

`document` returns **`files[]`** / **`origin_urls[]`** / vbpl `files_url` — surface documented in
[`RAG.md`](docs/design/RAG.md); vbpl's presigned-vs-gateway download mechanics in
[`SOURCES.md`](docs/design/SOURCES.md). Decision recorded in [Decisions](#decisions-settled).

### v0.4.3 — Language-rule and jurisdiction-neutral schema copy — DEPLOYED (2026-07-20)

- Shared tool-schema descriptions no longer leak cross-jurisdiction examples or the false
  "index is multilingual" claim: the search query description now states the native-language
  rule; doc_number/citation/location/source/doc_type descriptions are jurisdiction-neutral.
- VN/ID/TH guide texts drop "English works via cross-lingual matching" — native language ONLY,
  consistent with the search tool descriptions.

### v0.4.2 — Issuer filter fix + listing surface — DEPLOYED (2026-07-19)

**Problem (found via ChatGPT app testing):** a search with an `issuer` filter returned zero hits
unless the string matched stored metadata exactly — and MY/SG/TH/KH have no issuer metadata at
all (VN full, ID 289/2,371 empty), so any issuer filter blanked those corpora.

- **Fix:** issuer pre-filter is now case-insensitive **substring** match (LIKE, escaped); the
  search schema's issuer description carries the corpus's real issuer vocabulary (read at
  startup) or an "omit this filter" warning when the corpus has none; the no-evidence gap
  names the filters as a likely cause. Backfilling issuer metadata for MY/SG/TH/KH is a
  separate, future normalize+backfill decision.
- **Also in v0.4.x line (2026-07-19):** directory-listing surface (/privacy, /terms, /support,
  /demo.mp4 → public S3, S3-backed openai-apps-challenge), explicit destructiveHint on all
  tools, footer links, docs/PUBLISHING.md.

### v0.4.1 — Concurrent query embedding — DEPLOYED (2026-07-19)

**Problem:** the onnx embedder held one mutex across tokenize + inference + pooling — every search
across all six jurisdictions queued behind a single run (head-of-line blocking).

- **Fix:** bounded semaphore instead of the mutex (`BANHMI_EMBED_CONCURRENCY`, default NumCPU);
  ORT `Session.Run` is concurrency-safe on one session (weights shared, activations per-run).
  Tokenizer FFI stays behind its own sub-ms lock. Commit `a11f242`.
- **Memory model:** ~23 MB per in-flight run (measured locally and confirmed in prod: 8-burst
  cost ~190 MB). Prod cap `BANHMI_EMBED_CONCURRENCY=10` ⇒ ≤ ~350 MB burst against ~1.1 GB
  host MemAvailable with a 300 MB floor.
- **Hard cgroup limits dropped** (`banhmi-embedder:3`, `banhmi-mcp:15`): both services keep only
  `memoryReservation` (2400/300). Rationale: MCP + embedder are one serving chain — killing either
  kills `search`; the semaphore is the real memory guard, and the old 2900 MB hard limit would have
  OOM-killed the embedder (~4 min cold start) while the host still had ~1 GB free. AZ rebalancing
  disabled on both services (single-instance cluster; it blocks `maximumPercent=100` deploys).
- **Verified in prod:** 8 parallel MCP sessions → 8 `embeddings: start` within 46 ms, all done
  ~5.2 s (2 vCPU time-slicing; solo run ~0.6 s); correct evidence packs from VN + MY corpora.

### v0.4.0 — Read-path embedder split: two ECS services — DEPLOYED (2026-07-19)

Query-time embedding moved out of the MCP process into its own ECS service on the **same
t4g.medium** — no new infra, same cost. VN eval through the split stack reproduced the accepted
baseline exactly.

- **Shape:** `cmd/embedder` (`-tags onnx`, model baked; replaced the retired
  `cmd/pipeline -serve-embed`) on loopback `127.0.0.1:8089`, OpenAI-compatible `POST /embeddings`
  + Bearer; the slim MCP image drops the onnx tag and model files. **Why the model needs its own
  process:** [`RAG.md`](docs/design/RAG.md); operating it: [`DEPLOYMENT.md`](docs/DEPLOYMENT.md).
- **Settled:** embedder-down = hard retryable error (no BM25-only fallback); the MCP parity probe
  (dims + model tag) gates readiness; a second ECS service, **not** k3s/EKS — which this split is
  the prerequisite for anyway. compliary sharing deferred until compliary consumes it.
- **Cutover cost:** 2 rollbacks (~13 min) from two image traps — both now pinned as comments in
  `deploy/containerfiles/Containerfile.ecs.server` and in the cutover runbook
  (`deploy/aws/setup-checklist.md`), which also carries the sha-pin and host-memory ordering rules.

### Singapore (`kaya`) — DEPLOYED (2026-07-17)

Fourth jurisdiction. English corpus, MY citation family; sources **sso · mas · pdpc · csa** (no
proxy); new `sg-act` structure parser (em-dash dispatch, case-sensitive Schedule gate). Corpus size
and accepted baseline in [Current state](#current-state); floors in `make eval-sg`. Source contracts,
parser rules, and known gaps: [SINGAPORE](docs/design/jurisdictions/SINGAPORE.md).

### Thailand (`tomyum`) — DEPLOYED (2026-07-17)

Fifth jurisdiction. Thai corpus: **TCC word segmentation** (pure Go, no FFI) + the `th-act` parser
(มาตรา/ข้อ/วรรค). Sources live: **OCS** (Acts via the `getLawDoc` JSON API), **BOT** (notifications,
mostly Vision-OCR'd), **ETDA**; **SEC deferred** — needs the Bangkok proxy. Corpus size and accepted
baseline in [Current state](#current-state); floors in `make eval-th`. Crawl contracts, corpus state,
and open gaps: [THAILAND](docs/design/jurisdictions/THAILAND.md).

### MVP2 candidates (parked)

Gemma 4 E4B OCR enhancement, figure extraction, manual-folder source, crawl depth >1,
`sbv.gov.vn` extra source, reranker-as-teacher embedder distillation (serving reranker
rejected 2026-07-19), validity/amendment refresh re-crawl, drift & quality monitoring.

## Open / queued

The live work queue. Shipped work moves into the release entries below; mechanisms live in the design docs.

**Retrieval**
0. **ID retrieval quality — DIAGNOSED 2026-07-26 (read-only); the obvious lever is REFUTED.**
   `-pool-k 200` splits ID's 16 misses into **14 ranking failures and 2 coverage failures**, with
   **pool-recall 98.2%** — retrieval finds almost everything, ordering is the whole gap. Gold sits at
   pool ranks 15–110. Reviewing five cases shows one dominant shape: **the right document takes ranks
   1–3 but the wrong Pasal**, then `doc_cap=3` blocks the correct article (POJK 21/2023 → Pasal 32/13/20
   instead of 3; PBI 23/6/PBI/2021 → Pasal 72/47/72 instead of 101) — the same intra-document crowding
   as VN's 83/2025 case.
   **Raising the cap makes it worse, measured, not assumed:** `doc_cap=5` → recall **78.1%** vs 79.8%
   at cap 3. Giving one document more slots evicts other documents that were supplying correct answers,
   so the cap is not the lever.
   **Second lever also REFUTED — vocabulary.** The 2 coverage failures were traced to a real corpus
   quirk: `POJK 11/POJK.03/2022` Pasal 60 writes "insiden **TI**" while the golden asks "insiden
   siber", and the existing `abbreviation_expand` row maps siber → "insiden teknologi informasi" —
   the long form the provision never uses (the ID corpus writes bare `TI` in 962 chunks). Adding the
   abbreviated surface form to the expansion **made it worse**: recall flat at 79.8%, MRR **62.4 →
   59.9**, because the two-letter token `TI` matches broadly and drags noise up. Reverted; baseline
   reproduced exactly. Both provisions are indexed, `in_force`, and on-topic — they simply score low.
   **ADJUDICATED 2026-07-26 — the goldens are RIGHT; my own hypothesis was wrong.** Relaxing the 23
   provision-level cases to doc level lifts ID to **86.8% / 68.4%** (from 79.8 / 62.4), so **7pp of
   the deficit is right-document/wrong-Pasal**. But reading the expected articles kills the
   "over-specific golden" theory: they are **anchor obligation provisions** — POJK 21/2023 Pasal 3
   ("Bank yang menyelenggarakan Layanan Digital harus memiliki infrastruktur TI…"), POJK
   18/POJK.03/2016 Pasal 2 ("Bank wajib menerapkan Manajemen Risiko secara efektif…"), POJK
   4/POJK.05/2021 Pasal 3 ("LJKNB wajib menerapkan manajemen risiko … Teknologi Informasi"). Those are
   exactly the citations an agent should get. **So: do NOT relax these goldens and do NOT lower the
   floor** — ID's 7pp is a genuine retrieval gap, not convention drift.
   **The shape of the gap:** for "what must X do about Y" questions, the correct answer is an early,
   short `X wajib …` duty article, while retrieval prefers longer later articles that repeat the topic
   vocabulary more often. A principled fix (obligation-anchor preference, or intra-document diversity
   that reserves a slot for the earliest matching article) needs design + measurement — it would also
   lift VN's 83/2025 and TH, which show the same shape.
   **Two more hypotheses eliminated:** chunk granularity is not the cause (MY has the *most* chunks per
   doc at 103.7 and the *best* recall; ID sits mid-range at 68.4), and page furniture is real but
   marginal and not anchor-biased (`www.peraturan.go.id … -38-` spliced mid-sentence in ~2,355 chunks /
   90 docs, 1.5% of the corpus; 1.6% of Pasal 1-5 chunks vs 1.3% of Pasal 20+ — worth cleaning for
   citation hygiene, not a ranking lever). (e.g. "Apa persyaratan OJK untuk bank
   yang ingin menyelenggarakan layanan digital?" against POJK 21/2023 Pasal 3, where Pasal 13/20/32 are
   also on-topic). That is reading work per case, not a knob — and it must not become goalpost-moving:
   only relax a golden where the evidence says several provisions genuinely answer it. The 2 coverage
   failures (`cyber-incident-bank`, `new-pjp-security-requirements`) are separate and worth checking
   first, since gold absent from a 200-deep pool usually means a vocabulary or chunk-text problem.
1. **Two hard VN golden cases — DIAGNOSED 2026-07-25, both root-caused; no fix applied yet.**
   *39/2016 (lending-rate ceiling)* is **not** a ranking bug: the 1996 decisions that outrank it
   (`225/QĐ-NH1`, `266/QĐ-NH1`) are badged `in_force` by vbpl itself while carrying confirmed
   `replaces` relations — the same defect as item 6, which is the real fix.
   *83/2025 (ICS senior management)* is **intra-document crowding**: the right document takes ranks
   2-4, but Điều 12 is a single 1,013-char chunk competing with 392 siblings, and `doc_cap=3` fills
   the quota with them. `doc_cap` cannot fix this by design (the crowding document *is* the expected
   result). Options: accept as an honest doc-level hit, or re-target the case doc-level per the
   large-law convention — not moving the goalposts without a decision.
2. **Wrong-jurisdiction abstention** — the domain gate tests topic, never jurisdiction. A
   negative-jurisdiction signal (detect foreign regulator names) is the candidate fix.
3. **Regulatory-hierarchy boosting** — proposal only.
4. **Reranker** — measured and rejected 2026-07-19; revisit only on the recorded triggers.

**Relation & validity integrity — investigated 2026-07-26**

A. **Source metadata is not ground truth for validity; the document's text is.** vbpl still returned
   `CHL "Còn hiệu lực"` from the live API on 2026-07-26 for `24/2018/QH14` (repealed 2026-07-01 by
   `116/2025/QH15` Điều 44) and `22/2020/TT-BTTTT` (repealed by `15/2025/TT-BKHCN` Điều 10); its record
   for 24/2018 was last touched 2026-04-06 and carries **empty `references` and `documentRelatedList`**.
   `86/2015/QH13` shows `partial` where the same clause lapses it fully. All three are served as current
   with 405 chunks. Refreshing metadata cannot fix this — only the text reveals it.
B. **vbpl asserts amendment edges its own documents do not support.** `143/2025/QH15` (Luật Đầu tư)
   asserts 7 `amends_supplements` edges; only 3 (`105/2016`, `67/2025`, `95/2025`) are real targets of
   Điều 50. The other 4 (`116/2025`, `127/2025`, `44/2024`, `28/2018`) appear **only in recital position**
   — `"… của Luật X đã được sửa đổi … theo Luật Y …"` names X as target and Y as history. Stored at
   `confidence=1, promoted=true`. A clause-grammar classifier (target = first number after the operator;
   numbers after `theo` = recital; numbers inside `như sau:` = quoted) scored **7/7** on this document,
   but a naive "is the number present?" test confirms all 7 and must not be used.
   **Do not attach a scavenged clause to structured edges** — every one of the 7 is named in exactly one
   section, so attaching would launder the artifacts behind an authoritative-looking quote. Shipped
   instead: schema descriptions telling the agent what each evidence kind is worth.
C. **VBPL's provision tree drops khoản; banhmi reproduces it faithfully.** 129 of 129 missing khoản are
   absent from the raw `provision_tree_json`; zero were lost by our tree builder. Blast radius (VN only —
   no other jurisdiction has `node_key` sections): **112 articles / 84 docs with interior ordinal gaps**,
   of which **73 are wrong citations and 15 are provisions absent from the index entirely**. Separately,
   **17,681 articles across 951 docs** arrive as leaf nodes with all khoản inline (~67,694 khoản of lost
   citation granularity). Fix requires re-normalize → re-index → re-embed; not parity-safe, needs sign-off.
D. **ID `doc_ref.document_id` is fully dangling locally** — 660 of 660, with disjoint ranges (refs
   12–10,392 vs documents 14,295–24,448); VN has zero. Signature of a rebuild without re-resolving refs.
   **Check whether prod ID shares this**: if so, ID amendment/relation surfaces are broken there.
E. **TH/KH/SG carry zero amendment relations at all** — a coverage gap, not a vocabulary one.

**Corpus quality**
5. **Duplicate citations — investigated 2026-07-26, fix NOT shipped (measured cost > benefit).**
   The premise was stale: silver has **zero** duplicate `citation_path` rows, so "apply uniqueness at
   section creation" was solving a problem that no longer exists. Real cause: `enclosing()` in
   `index_activities.go` recognises `chuong`/`muc` but **not `phan`** (416 VN sections), so five
   distinct `Điều 1` under five `Phần` all render as bare "Điều 1". A full ancestor-chain walk in the
   collision branch fixes it — but only **222 → 206** duplicate groups. The remainder are Khoản-level
   citations inside malformed parses (labels like "Phần THỨ", "Chương c"), concentrated in a few
   documents: a source-quality problem, not a rendering one.
   **Why it is not shipped:** the fix can only reach prod via a full re-index, and that costs more
   than it gains (next item).
6. **`-index-all -force` does NOT reproduce the incrementally-built corpus — measured 2026-07-26.**
   Rebuilding VN from scratch yields **131,486 chunks vs the deployed 130,707**, and eval drops
   **recall 93.8 → 91.7, MRR 73.6 → 68.7**. Confirmed to be the rebuild itself, not any code change:
   reverting the citation fix and re-running the identical rebuild reproduced 91.7 / 68.7 exactly.
   There is no duplicate *content* (0 groups), so the extra chunks are split differently rather than
   junk — but they retrieve measurably worse. **Never force-rebuild a live corpus expecting parity**,
   and treat a rebuild as a re-baselining event. Also note `-force` re-creates chunks with new ids,
   orphaning every embedding (a full re-embed followed; the cache covered only 67%).
   **Local VN was restored from the prod dump** and reproduces the deployed baseline
   (3,974 docs / 130,707 = emb = sparse, mojibake 1, **recall 93.8 / MRR 73.0**, floors pass) — so the
   eval instrument is trustworthy again. Restoring took more than a `pg_restore`: the S3 dump predates
   both mojibake rounds, so 15 documents had to be re-normalized/re-indexed through their vbpl alias
   to get back to 130,707 and mojibake 1. **A dump is only as current as its timestamp** — check it
   against the live corpus before treating a restore as a rollback.
6. **Source status contradicted by confirmed relations — SHIPPED locally 2026-07-25, not deployed.**
   113 indexed VN documents are served as current law while a promoted, official, confidence-1.0
   `replaces`/`repeals` relation targets them (49 `in_force` + 64 `partial`; e.g. `101/2012/NĐ-CP`
   badged *In force* with `52/2024/NĐ-CP` replacing it). Decision taken: **warn, never override** —
   `validity.warning` gained a second kind naming the superseding documents, on both `search` hits
   and `document`. The superseding set is config-driven (`config.relation_type.is_superseding`, new
   column + migration `config/00007`) so it stays operator-tunable; `partially_revokes` is excluded
   because a partial repeal leaves the rest in force. **Deploying it needs `cmd/migrate` + `cmd/seed`
   against each prod DB before the new image** — the query joins the new column.
9. **TH BOT — 279 of 280 recovered, OCR'd and indexed; TH eval MRR under floor (golden staleness).**
   Two real defects, fixed with tests: the synthesized URL hardcoded path group `FPG` (documents live
   under `DDD`/`DMG`/`FOG`), and the listing's hrefs use Windows-style backslashes that Go
   percent-encodes into a 404 — curl normalizes them silently, which misled the first diagnosis.
   Discovery-time file refs now persist (`ingest.fetch_doc.discovered_files`) and replay via
   `ingest.DetailRef`. **Serial fetch (`-max 1`) is the operational lesson:** concurrency 3 drew mass
   403s and got us throttled ~an hour; `-max 1` recovered 279 in one clean pass. The 1 straggler
   (25650005) is a dead link at the source. **Vision OCR run with the SA key: 221 scans / 1,733 pages,
   0 failures**; output verified before indexing (212 Thai-dominant, 8 English bond-coupon forms,
   0 soup — unlike the KH episode). Corpus: **1,551 → 1,786 docs, 29,736 → 32,627 chunks =
   embeddings = sparse**.
   **Golden staleness resolved 2026-07-25 — floors pass again (MRR 69.6 ≥ 0.68, recall 89.5).**
   Each of the 4 rank-slipped cases was verified individually, not batch-fixed: `bot-it-risk` and
   `bot-sandbox` gained the recovered newer instrument as `alt_doc_numbers` (สกช. 5/2566, the 2023
   IT-risk notification; the 15 Mar 2019 sandbox guideline); `multi-cyber-and-it-risk` recovered to
   rank 1 by itself once the OCR text landed. **`ctf-sanctions-list` was deliberately NOT altered**:
   what outranks it is the 2013 CTF Act (`ป0045-1B-0001`) whose มาตรา 4 does answer the question, but
   the expected `ป0054-1B-0001` is its 2016 successor — OCS badges BOTH in force with no relation
   edge between them, so crediting the 2013 Act would teach the eval to accept likely-superseded law.
   It stays an honest rank-2. Residue: the ป0045→ป0054 succession is invisible to the corpus (OCS
   carries no repeal relation) — same class as VN item 6, but with no relation data to warn from.
10. **TH other coverage gaps** — SG subsidiary legislation remains unbuilt.
    **ETDA — site recovered; ingest ATTEMPTED and REVERTED 2026-07-26 (measured cost).** The outage
    below cleared, and the vocabulary theory was right: the 46 rejected documents are ETDA's
    recommended-standards series, titled as bare designations (`ขมธอ-39-2568`) with no topical words
    for `scope.Match` to see. Seeding `ขมธอ`/`สพธอ` as `strong_title` (the `tt-nhnn` pattern) took
    in-scope **1 → 43**, and all 43 fetched, OCR'd and indexed cleanly (41/45 Thai-dominant, no soup).
    **Reverted anyway:** TH eval fell to recall 87.7 / **MRR 67.9, below the 0.68 floor**, because
    42 recommendation standards outrank the ETDA Act on "what are ETDA's duties" (`etda-mandate`
    rank 10 → miss) — worse for an agent, not just for the metric. A second defect surfaced en route
    and is FIXED in code (kept): ETDA documents arrived with **empty doc_number**, so every hit was
    uncitable and unopenable via the `document` tool; `etdaDocNumber` now derives `ขมธอ. N-YYYY`
    from the filename/title. **To land this properly**, the Act needs to outrank the guidance — both
    are fulltext-chunked (`Full text, วรรค N`), so parsing มาตรา structure for OCS Acts is the real
    prerequisite, not more vocabulary. Cleanup cost recorded: the surgical delete removed aliases
    before document rows and orphaned 38 documents / 1,890 chunks, then over-deleted by one — local
    tomyum was **restored from the prod dump** rather than patched, and reproduces prod exactly again
    (1,790 / 32,715 = emb = sparse, eval 89.5 / 69.6). **Delete document rows before their aliases**,
    or the join that identifies them is already gone.
    **Earlier note (outage, now resolved):** `www.etda.or.th` 301s *every*
    path to `static-etda.etda-thailand.workers.dev`, which serves a **Cloudflare "Always Online"
    error placeholder** (`cf-error-banner`, `cf_use_ob` cookie), so discovery correctly returns 0.
    The earlier "1 in-scope of 46" reading predates the outage. Re-check the site before touching
    `scope_term_th.csv` — the vocabulary theory is untestable while the source is down, and the
    3 shipped listing paths may or may not survive whatever comes back.
    **SEC — SOLVED 2026-07-26, no proxy needed: 16 docs live.** It was never a geo-block. The F5
    rejects non-browser clients: the source sent a bot UA, which 403s from anywhere; browser headers
    download fine **direct from Vietnam**. Two things disguised it — curl 403s even WITH those
    headers (its TLS stack differs from Go's, so my curl probes "confirmed" a block that Go never
    hit), and a flat 403 looks exactly like a geo-fence. The Bangkok AWS proxy was launched and torn
    down for nothing; the maintainer's "I can download it in a browser" is what broke the wrong
    assumption. Also fixed: the NRS parser read the wrong columns (designation as title), so the
    scope gate rejected all 56 docs — now 21 in scope, **16 fetched/OCR'd/indexed** (5 are source
    404s), TH corpus 32,627 → **32,715 chunks = embeddings = sparse**, eval floors still pass
    (89.5 / 69.6).
11. **Per-jurisdiction residue** — ID jdih 59 stragglers (manual runner exists); KH cross-source dedup
    (TRM `odc` 1754 = `nbc` 2520) and TCRMG-2026-supersedes-2019; ID 30 / TH 19 docs with no PDF
    artifact; 4 preserved genuine-mojibake chunks; 17 VN relation targets without text.

**Ops**
10. **Restore procedure needs one ruling** — 2026-07-17 recorded direct in-place restore into the live
    DB; 2026-07-19 onward used `*_v2` + rename-swap. Whichever is current belongs once in
    [Deployment shape](#deployment-shape) / [`DEPLOYMENT.md`](docs/DEPLOYMENT.md), not per release.

## Milestone history

- **2026-06-01** — VN deployed (Cloud Run + Firebase).
- **2026-06-22** — Malaysia deployed. Hybrid retrieval (BM25 + RRF).
- **2026-07-06** — Indonesia LIVE. Temporal removed.
- **2026-07-08** — Qwen3-Embedding FP16, go-fitz, Document AI OCR.
- **2026-07-12** — **v0.3.0 shipped.** AWS read path (CloudFront + ECS ARM64). GCP teardown. ID revived.
- **2026-07-13** — ID scope rebuild (bpk sweep-only, issuer-mandate scope, ojkweb source).
- **2026-07-14** — Corpus rebuild + RDS restore (all 3). Kaggle dual-T4 OOM root-caused (per-run arena
  shrinkage). **v0.3.1 + v0.3.1b deployed** (MCP token optimization + amendment-chain awareness).
- **2026-07-15** — OCR engine -> Vision `images:annotate`; OCR backfill (VN +8,334 chunks); MY scope
  expansion (46 -> 97 docs); eval overhaul (jurisdiction-aware provision matcher, local eval targets,
  VN validity regression fixed, K=100, ID abstention 100%). **v0.3.1 tagged + image deployed.**
- **2026-07-16** — v0.3.2 bulk: diacritics seam + restoration, MY parser overhaul + SC dedup + BNM
  Liferay fix + Online Safety Act 866, VN label-only chunk fix + vbpl empty-body recovery, ID parser
  fixes + OJK source wiring + BI terminal-state fix, chunk dedup, abbreviation expansion,
  promotion-only aggregation, per-doc cap, golden expansion (VN 80/MY 73/ID 110), workflow eval
  harness + VN pilot (85/100/83.3). All 3 corpora redeployed. First agent-workflow baseline recorded.
- **2026-07-17** — **v0.4.0 + v0.5.0 shipped.** Singapore (kaya) + Thailand (tomyum) deployed as
  4th + 5th jurisdictions. 8 new source packages (SSO, MAS, PDPC, CSA for SG; OCS, BOT, ETDA, SEC
  for TH). sg-act parser (em-dash sections, MAS paragraph numbering, case-sensitive Schedule gate).
  th-act parser (มาตรา/หมวด/ส่วนที่ for Acts, ข้อ for BOT). TCC Thai word segmenter (pure Go, ~25
  regex rules). Vision OCR for 1,204 scanned BOT docs. Lexindex jurisdiction-aware normalizer.
  VN citation dedup (2,601→72 duplicates, recall 82.9→86.6%). VN+MY code review (11 items fixed,
  deployed). SG: 292 docs / 27,951 chunks / recall 84.8%. TH: 1,551 docs / 29,736 chunks /
  recall 80.7%. All 5 live: banhmi/laksa/rendang/kaya/tomyum.danny.vn. Total ~298K chunks.
- **2026-07-18** — **Cambodia (amok) deployed** as 6th jurisdiction (NBC via residential SOCKS5 proxy,
  282 docs / 2,609 chunks). ID corpus rebuilt with normalizer consolidation + cross-source dedup.
- **2026-07-19** — **v0.3.2 final deploys + v0.3.3 tagged.** All six corpora redeployed as
  `v0.3.2-20260719` (doc_cap=3, dedup, seeds, MY rename, KH scope). Scan-layer gate + P2SK rebuild
  (ID 79.8%). Sparse-arm abbreviation expansion. Eval floors raised to accepted baselines.
  Reranker experiment concluded — NOT deploying. Full-diff code review (4 agents, no critical/major
  findings); minors fixed and tagged `v0.3.3`.
- **2026-07-20** — **KH corpus rebuilt + deployed `v0.4.3-20260720`** (31 → 244 indexed docs,
  2,609 → 7,757 chunks; recall 93.1 → 94.4% on a 36-case scored pool, 8 former known-gap cases now
  answering). Root cause of the thin corpus was a fetch bug (nbc/cdc planned listing pages as the
  document PDF), not missing OCR. NBC re-crawled via /english/ pages only (+TCRMG 2026, TRM 2019,
  banking codes). 40 Khmer-only scans quarantined as explicit gaps. Eval `alt_doc_numbers` added.
  Per-jurisdiction storage layout restored; all six S3 data mirrors synced (sg/kh buckets created).

## Decisions (settled)

| Decision | Choice |
|----------|--------|
| Evidence-only; no answer LLM | citations/validity/relations/gaps over MCP; user brings the model |
| One language per country | index/serve/search the binding native language only; never translate |
| Deploy shape | local pipeline -> Kaggle T4 embed -> dump/restore RDS <- ECS ARM64 <- CloudFront |
| Hybrid retrieval | dense + pgvector `sparsevec` BM25 + RRF; no ParadeDB |
| Qwen3-Embedding-0.6B FP16 | 1024 dims, 32K context, ONNX FP16 everywhere; ~2.3 GB/process |
| go-fitz + Vision OCR | zero-Python extraction; `images:annotate` page-per-request, file-first cache (local + S3) |
| Kaggle-only embedding | free T4 GPU, fresh per run, dataset I/O |
| No composite PKs | surrogate identity + UNIQUE business keys |
| No query-time reranker | measured 2026-07-19: promotes superseded law, 12–38 s/query on CPU; revisit only with new triggers (bigger model, GPU quota, grown corpora) |
| Original download links only | `document` links the official source's own artifacts; banhmi never self-hosts or re-serves document files (2026-07-21) |
