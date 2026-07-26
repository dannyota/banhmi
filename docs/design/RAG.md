# RAG Evidence

RAG is the database evidence layer. Users may bring their own model. banhmi must expose faithful
chunks, citations, relation context, provenance, and gaps without hiding weak data behind prose.

## Current Shape

| Area | Current behavior | Limit |
|------|------------------|-------|
| **Chunks** | Gold chunks are provision-aware, normally by `Điều`; long articles split by `Khoản` / paragraph shard. Search keeps the fine-grained chunk as the ranked match but **re-attaches the full enclosing `Điều`** (all its `Khoản`, reassembled verbatim from its chunks, lead-in deduped) as `hit.provision` so a matched clause is never read out of context. A pathological oversized `Điều` (e.g. an amendment law whose `Điều 1` is the whole law, hundreds of chunks) returns a `provision` **pointer** (`truncated`, no inline text) rather than a truncated-from-start blob that could omit the match — the agent opens the `document` tool. **Phụ lục fold:** appendices parse as root-level `phuluc` sections; appendix tables/forms chunk under "Phụ lục N" and an attached Quy chế's Điều cite "Phụ lục X, Điều N". | Short but real legal provisions are kept by design (label-only chunks are filtered). **Index-time hygiene:** identical content inside one document is deduped (tabular docs — 1089/QĐ-NHNN 1,129 → 127 unique chunks), and chunks whose label-stripped content is under `minChunkContentLen` (20 runes) are dropped as form labels and table debris — calibrated as universally junk across VN/MY/ID. **The label-only guard compares the *parent* label** — comparing a chunk against its child (ayat/Khoản) citation instead let 2,709 ID heading-only "Pasal N" chunks through. **Non-binding section kinds are never chunked:** ID `penjelasan` is parsed and served as structure but excluded from the chunkable kinds. |
| **Citation** | Chunk citation is label-only, e.g. `Điều 7, Khoản 2`. **The uniqueness suffix survives into the citation:** the parser's unique `citation_path` suffix is rendered into the human cite — VN `, Đoạn N` (from the tree's `~N`), MY/SG `[N]` — because restart-numbered labels otherwise collide (Điều renumbered per Chương/Mục; a definition section restarting `(a)`/`(b)`, e.g. Act 758 s.2 ×24). Discarding it produced 2,601 duplicate VN and 1,355 duplicate MY citations; headings stay in content/context, not citation. | Legacy outline docs can still produce weak legal locations. |
| **Context prefix** | Prefix is deterministic: document number/title, chapter/section heading, effective date. Long fields are capped. | Prefix is an embedding hint, not evidence. |
| **Retrieval** | **Hybrid** (single datastore): dense Qwen3-Embedding vectors + **BM25 sparse vectors** (pgvector `sparsevec` in `gold.chunk.content_sparse`, built by `cmd/lexindex` / the `LexicalIndex` RunAll stage) fused with **RRF** and a **deterministic query router** — the lexical arm is boosted only for diacritic-less or số-ký-hiệu queries (a VN-shaped signal: `LexicalRouterBoost` in the jurisdiction registry; MY stays vector-primary, fusing the BM25 arm at the base lexical weight). Each hit carries both the dense **`score`** (cosine) and its **`bm25_score`**. Default: current law leads the primary pass (`in_force`/`partial`); a small secondary pass of non-current law (incl. `unknown`-validity docs) is appended **badged** after it — at most **one hit per document** and **min(3, top_k)** hits — so repealed/overlapping law stays findable without dwarfing a small top_k. `InForceOnly=true` → strict current-only; `false` → no filter. The abstain floor (`retrieve.abstain.min_score`) gates on the top hit's **cosine similarity** (RRF scores are rank-derived and carry no absolute meaning). Optional, **gated query-time pre-filters** narrow eligible documents without touching embeddings — **`as_of`** (point-in-time: law whose effective window contains the date), **issued-date range**, and **issuer / doc-type facets**; with no filter the path is byte-for-byte unchanged. Scoped queries skip the non-current pass. | Validity is document-level; clause-level validity is missing. `as_of` relies on recorded effective dates. |
| **Relations** | Each retrieved document carries up to eight confirmed incoming/outgoing `silver.document_relation` edges, listed on its **first (best-ranked) hit only** — sibling hits from the same document share them instead of repeating the array. Each relation whose resolved target is itself further amended carries **`target_amended_by`** (doc numbers, ≤8, one batch query, best-effort) — the citator-style currency warning that the amender the agent is about to trust has itself been amended. | Relations are not rank boosts and do not replace chunk evidence. |
| **Amendment chain** | The `document` tool walks the **transitive incoming amendment lineage** (recursive CTE, depth ≤4, cycle-safe; relation types from `config.relation_type WHERE is_amending` — jurisdiction-neutral): who amends this document, who amends those amenders. Emitted as **`amendment_chain[]`** under `include=['amendments']` **only when depth ≥ 2** (depth-1 is already in relations/incoming_amendments); each node carries depth, doc_number, relation_type, effective date, validity badge, indexed flag. A depth ≥ 2 lineage also emits an **`amendment_chain` gap** pointing at the newest treatment. VN: 340 docs on ≥2-hop chains; ID: 332 (after bi/bpk promotion — forward operators config-resolve to `amends`/`revokes` and promote at confidence 0.9 as `official_metadata`; reverse `*_by` operators stay weak); MY: inert (no is_amending edges). | Chain is metadata lineage only — banhmi never interprets what an amendment changed; the verbatim clauses stay in `incoming_amendments`. |
| **Per-document cap** | `defaultDocCap=3` (config `retrieve.doc_cap` overrides; 0 disables; 3 beat 4 on all six jurisdictions 2026-07-19: recall +0.9..+3.4pp, MRR flat+). When any single document occupies > N slots in the top-k, demoted hits backfill so results never shrink. Prevents one large document from crowding out cross-doc results. | Cannot fix same-document crowding (the crowding doc is itself the expected result). |
| **Section aggregation** | Promotion-only mode (config `retrieve.section_aggregate`, default ON). Freezes the natural top-k and appends up to 2 multi-fragment article groups (≥2 fragments, top-3-sum score above the weakest natural hit). Result set may exceed top_k by ≤2 trailing hits. | The full re-sort variant failed its eval gate (VN −5.6); append-only by design. |
| **Abbreviation expansion** | Query-time single-pass expansion of known abbreviations and phrases, applied to **both arms** (2026-07-19): the dense query bridges the embedding gap for bare acronyms; the sparse query anchors BM25 on the corpus's dominant vocabulary — regulations often abbreviate what queries spell out (PBI 23/6/PBI/2021 says `PJP` in 325 chunks, spells it out in 2; dense-only expansion measurably failed those cases). Entries include reverse mappings (phrase → abbreviation, e.g. `penyedia jasa pembayaran` → `PJP`) and statutory-term bridges (VN `hệ thống thông tin quan trọng quốc gia` → `… về an ninh quốc gia`). Dictionary loaded from `config.abbreviation_expand` per jurisdiction at retriever init (VN/MY/ID seeded). | Expansion is additive (appended), never replacing the original query term; the sparse query keeps the raw query's tokens (no diacritic restoration leaks in). |
| **Diacritic restoration** | Query-time restoration of unaccented Vietnamese syllables via `config.diacritic_restore` (698 unambiguous corpus-derived entries at ≥90% share; `cmd/dictgen` regenerates). Fires only when the query contains no diacritics. Ambiguous syllables are never restored. | Bigram-aware restoration landed (27,210 entries, VN recall +4.9pp). |
| **Retrieval defaults** | `VectorK=100`, `BM25K=100` (from 50, 2026-07-15 — VN MRR +0.7, MY/ID +1 case each, zero regressions). Config `retrieve.vector_k` / `retrieve.bm25_k` overrides. | Higher K widens the candidate pool before fusion; does not change top_k. |
| **Weak relations** | `silver.relation_evidence` weak rows are stored for review/classification. | Weak rows are not exposed as confirmed legal status. |
| **Relation trust (2026-07-26)** | `relation_evidence` fields carry jsonschema descriptions stating what each evidence kind is worth: `structured_relation` = the source's own metadata, **not** corroborated against the document text; `weak_relation` = derived from the amending document's wording, with `citation`/`snippet` pointing at it. `confidence` is documented as the strength of the source's assertion, never truth. | Agents must weigh edges, not trust them: vbpl stores wrong edges at `confidence=1, promoted=true`, and a doc number inside an amendment sentence is often a **recital** of the target's own history, not a second target. |
| **Amendment clauses** | Lead-verb vocabulary is **per-jurisdiction** (`setting_<code>.csv`, loaded before the shared defaults) and matches the verb **anywhere** in the clause — a prefix anchor is Vietnamese-only drafting and returned zero clauses for every English jurisdiction. Sets are bounded (1,500 chars/clause, 24,000/set) with an explicit note for anything dropped. | Verbatim instruction text, never interpreted; a truncated set must never read as complete. |
| **Surfaces** | MCP is the only query surface, exposing `guide`, `corpus_status`, `quality_gaps`, `search`, and `document`. Search returns `hits[]` (ranked, with source link, cite, validity badge, **issued date**, text provenance, confirmed relations, scope signals — plus a **`validity.warning`** when the source's own dates are internally inconsistent — and **`provision`**: the enclosing `Điều`, so `snippet` stays the precise matched clause) and `related_hits[]` — graph-adjacent chunks that **each carry their own `source_url` + `cite`** — plus `gaps[]`. **Progressive disclosure (v0.3.1):** search takes `detail` — `compact` (discovery: metadata + snippet + cite + badge only), `standard` (**default**: adds relations + related_hits + the provision *pointer*; `provision.text` is never inlined), `full` (legacy: inline full article text) — shaping response size only, never ranking; data-quality signals survive every level. `document` adds all official **`sources[]`** for the doc, **`files[]`** — the downloadable official artifacts (one entry per content hash across sources: label, kind, format, byte_size, sha256) with **`origin_urls[]`** durable direct download links where an official source serves one (expiring presigned links are never emitted; sha256 matches `text_provenance`, so a download is verifiable against the extracted text) and, for sources that only mint short-lived links (vbpl), a **`files_url`** on the matching `sources[]` entry — the stable official listing endpoint returning fresh ~24h download links (reachable from Vietnamese IPs only) — a chronological **`timeline`** (issued → effective → amended/replaced → expired), validity periods, chunks, relations, verbatim incoming amendments, and citation-miss gaps; its `include` param (`chunks`, `relations`, `amendments`, `timeline`, `provenance` — omitted = all but `provenance`) selects sections, and `include=['chunks']` + `citation` is the cheapest one-provision read. Measured (real VN rows): standard −54% vs full; two-pass workflow (compact search → one-provision document) −76%. | The user-owned agent/model decides how to use the evidence. |
| **Agent contract** | English-first tool/param/field descriptions; a server-level `instructions` brief — the **trust stance** (text extracted verbatim from official government sources VBPL / Công Báo / SBV, evidence-only, never synthesized), **live coverage counts** (documents/provisions, stamped at startup), when to reach for it, how to cite, and examples; and read-only tool annotations so hosts can auto-approve. Legal **data stays Vietnamese, verbatim**; only the contract is English. Queries work in English or Vietnamese (Qwen3-Embedding multilingual). | Returns **content + official source links only — never file bytes** (file download links always point at the official source, never at banhmi). The connecting model decides the answer. |

## Batch embedding (Kaggle)

Bulk embedding runs on **Kaggle T4 GPU** (`embed.engine=kaggle`, free) via **Kaggle dataset I/O**:
the pipeline uploads input texts as a Kaggle dataset, pushes the GPU kernel, polls to completion,
and downloads the output vectors. Each run gets a fresh GPU. No HTTP body limits or request
timeouts. (The Cloud Run L4 GPU / GCS-batch engine was dropped — Kaggle-only; see
[`PLAN.md`](../../PLAN.md).)

Large corpora run **partitioned + parallel** (`embedAllKaggleParallel`):

- **Partitions:** chunks split into parts of **≤25K** (`maxPartitionSize`); each part is one
  kernel run.
- **Concurrency:** **2 kernels in parallel** (`-embed-parallel`, default 2 — the Kaggle free-tier
  GPU session cap; `1` falls back to the single streaming kernel).
- **Timing:** one 25K part ≈ **10 min end-to-end** on dual T4 (~42s startup + ~9 min GPU);
  kernel timeout 20 min. A ~195K-chunk corpus ≈ 8 parts / 4 rounds ≈ **50–70 min wall clock**.
- **Failure isolation:** vectors upsert into `gold.chunk_embedding` **as each part arrives** —
  a failed part retries alone; completed parts survive.
- **Embedding cache:** `ingest.embedding_cache` (content-addressed: sha256 of the embed text +
  model + dims → vector; lives in `ingest` so gold rebuilds never truncate it). EmbedAll's
  **cache pre-pass** copies vectors for unchanged text straight from the cache and sends only
  misses to Kaggle; arriving vectors are **written through**. A rebuild that re-chunks mostly
  identical text embeds in minutes, not an hour. Parallel path only (the default).

- **Boundary — batch only:** Kaggle is never the query-time / serve-time embedder. The query path
  **always** stays the Qwen3-Embedding embedder — **in-process ONNX Runtime** (`-tags onnx`) on the
  standalone `cmd/embedder` service (or in-process in the MCP server for local dev/eval via the
  Makefile `eval`/`mcp-local` targets). `embed.engine` chooses only the **bulk** engine, never the
  query path. The standalone `cmd/embedder` service serves an OpenAI-compatible
  `POST /embeddings` endpoint for query-time embedding; it replaces the retired
  `cmd/pipeline -serve-embed`.
- **Per-run arena shrinkage (GPU):** the kernel sets `memory.enable_memory_arena_shrinkage=gpu:<dev>` as a **RunOptions** entry — as a SessionOptions config it is silently ignored, and cross-batch BFC buildup OOMs the T4. Batches are also shaped to repeat one `[count, pad]` geometry per pad step so the arena cannot fragment.
- **Chunking stays in Go:** deterministic chunking is **never** offloaded — only embedding.
- **Why the query embedder is its own service:** ORT **pre-packs** the FP16 weights into **private
  anonymous memory** — not shared mmap pages — so *every* process holding the model pays the full
  **~2.1 GB RSS** (measured on prod 2026-07-13). Hence the model lives in `cmd/embedder` and **one**
  multi-jurisdiction MCP process serves all six countries: per-country processes would each pay it
  again. The slim MCP image also bounces in seconds instead of reloading the model (~40 s) on every
  code deploy. The resulting topology is in
  [`ARCHITECTURE.md`](../ARCHITECTURE.md) / [`DEPLOYMENT.md`](../DEPLOYMENT.md) — this is the reason.

### Bucket layout

Per-jurisdiction S3 data buckets (`BANHMI_S3_DATA_BUCKET`: `danny-banhmi-data-<jur>`) hold
`files/{name}` — fetched source files (PDF, DOCX, HTML) — and `ocr/{sha256}.txt`, the S3 mirror
of the file-first OCR text cache (primary copy is local `data/<jur>/ocr/`). No GCS anywhere.
Kaggle embed I/O uses **Kaggle datasets**.

### Kaggle (`embed.engine=kaggle`)

Pipeline uploads the input JSONL as a **Kaggle dataset**, pushes the kernel (it reads
`/kaggle/input`), and downloads the kernel's output vectors. No GCP credentials involved.

- **Auth — one env var:** set `KAGGLE_API_TOKEN` (the `KGAT_...` token from Kaggle -> Settings -> API -> Create
  New Token). The Kaggle **owner is auto-derived from the token** (token introspection / `WhoAmI`) — there
  is **no `KAGGLE_USERNAME`** to set, and the token never lives in YAML.

**Config** — `config.yaml` `embed:` block (`EmbedConfig` in `pkg/base/config`):

| Key | Default | Meaning |
|-----|---------|---------|
| `engine` | `auto` | `auto` = kaggle when `KAGGLE_API_TOKEN` is set, else `local`; `local` forces the local ONNX embedder; `kaggle` forces Kaggle. |
| `kaggle.model_dataset` | `danhsoftware/qwen3-embedding-06b-onnx-fp16` | Qwen3-Embedding-0.6B ONNX FP16 (`model_fp16.onnx` + `model_fp16.onnx_data` + `tokenizer.json`), mounted **offline**. |
| `kaggle.accelerator` | `NvidiaTeslaT4` | Kaggle machine shape. |
| `kaggle.min_batch` | `500` | Below this many missing chunks, embedding stays local (cold start isn't worth it). |

**How to run:**

- `go run ./cmd/pipeline -embed-all` (missing chunks only) · `-embed-all -force` (re-embed ALL, overwrite) ·
  add `-limit N` · `-embed-parallel N` (concurrent Kaggle kernels, default 2, max 2 on free tier).
  Needs Postgres up and engine env vars set.

**Flow:** Index writes `gold.chunk` only — embedding is **deferred** (a nil embedder is
skipped, best-effort) -> **EmbedAll** writes `(chunk_id, text)` JSONL into a Kaggle input dataset
-> pushes the kernel -> polls to completion -> downloads the kernel's output
`vectors.jsonl.gz` -> upserts `gold.chunk_embedding` under the **canonical model tag**
(`config.EmbedModel`) so retrieval (`WHERE model = ...`) finds them regardless of engine.

- **Auto-cleanup:** on **success** the Kaggle embed kernel **and** input dataset are **auto-deleted** (no
  leftover notebooks); on **failure** both are **kept** for debugging.

**Vectors / parity:** Qwen3-Embedding-0.6B dense, **last-token pooling + L2-normalize, 1024-d** — the
same recipe as the in-process ONNX embedder. **Asymmetric model:** queries are prefixed with
`Instruct: <task>\nQuery:<text>` (see `embed.FormatQuery`); documents are embedded as raw text with no
prefix. The kernel embeds documents only, so no prefix is applied. All vectors are stored under the one
canonical model tag.

**Library:** banhmi imports **`danny.vn/kaggle`** — an unofficial Go port of Kaggle's Python `kagglesdk`
(Apache-2.0), in a separate repo wired via a `go.mod` `replace danny.vn/kaggle => ../kaggle-go` until
published.

**Key files:** `pkg/rag/embed/kagglebatch/` (orchestration) · `pkg/pipeline/embed_all.go`
(`EmbedAll` activity) · `cmd/pipeline -embed-all` ·
`pkg/base/config` (`EmbedConfig`/`EmbedKaggleConfig`, `EmbedEngine()`).

## Eval

The eval harness (`pkg/eval` + `cmd/eval`) scores **retrieval only** (banhmi has no answer model)
against a per-jurisdiction golden set, **locally against the podman dev DBs** — never against prod.

- **Run:** `make eval-vn | eval-my | eval-id` — sets `BANHMI_JURISDICTION`, loads the in-process
  ONNX FP16 query embedder (~2.3 GB RSS, CPU), runs hybrid mode (the production default), writes a
  JSON report to `test/samples/eval/`, and gates on the accepted-baseline floors. Run
  jurisdictions sequentially. `-retrieval-mode vector|bm25` compares single arms; `-review` prints
  the DB-quality audit (chunk shape, malformed/duplicated citations, embedding coverage,
  relation-graph health) plus top hits per case.
- **Metrics:** recall@k, MRR@k (both micro-averaged over cases with expectations), current-law
  precision (badged trailing non-current pass excluded; a non-current hit *above* current law is a
  leak), abstention accuracy (out-of-scope controls must abstain).
- **Golden sets** (`deploy/eval/golden*.json`, selected by the jurisdiction descriptor): realistic,
  scenario-based questions in the jurisdiction's binding legal language; never cross-language.
  Expectations are provision-level where verifiable: `article`/`clause`/`point` hold bare values
  matched against chunk citations with jurisdiction keywords from the descriptor — VN
  `Điều/Khoản/Điểm`, MY `Section` + bare `(n)`/`(a)` tokens, ID `Pasal/ayat/huruf`. Mechanical
  split labels (`Đoạn`, `Paragraph`, `Alinea`) are never expectations. `alt_doc_numbers` lists
  alternate document numbers that satisfy the same expectation (any-of match, still one recall unit)
  — for documents existing under more than one identity (cross-source duplicates, superseding
  editions). `expect_abstain` marks out-of-scope controls; `expect_fail` marks known corpus gaps
  (excluded from aggregates; the report flags a **GAP-PASS** when one starts passing so the flag
  gets removed).
- **`-pool-k N` splits ranking from retrieval failures:** deep-probes each recall case at candidate depth N. Gold **inside** the pool but outside top-k is a fusion/ranking problem (rerank or weight headroom); gold **absent** from the pool is a vocabulary/coverage problem (seed abbreviations/synonyms, or fix the chunk text). ID at `-pool-k 200`: 14 ranking vs 6 retrieval failures of 20 misses, pool recall 94.7%.
- **Wrong abstains are a scope-vocabulary gap first, retrieval second.** Every KH false abstain was the domain gate (`out_of_domain`), not ranking — 15 grounded `scope_term_kh` rows took abstention 75% → 100% with recall/MRR unchanged. Check the gate decision before tuning retrieval.
- **Matcher semantics:** doc numbers are **canonicalized on both sides** (verbose doc-type phrases → the corpus's short codes) — without it a verbose-vs-short mismatch scores a false miss that reads as a retrieval failure. `relation_ok` credits a hit whose confirmed relation *is* the expected document (the amender when the golden names the amended doc). Very large laws are expected at **doc level only** — pinpointing one provision inside a 300-article law is the connecting agent's job, not a retrieval expectation.
- **Baselines and floors:** current accepted numbers live in
  [`PLAN.md`](../../PLAN.md#current-state) (single source of truth); the
  `eval-*` Make targets carry floors tracking the last accepted baseline. Re-baseline (update
  PLAN.md + floors together) only on a deliberate, explained change.

## Safety Gates

These gates decide whether banhmi has trustworthy evidence to expose — not whether banhmi answers
(it never does). The user's model relies on the evidence; banhmi must not hide weak data behind prose.

| Gate | Required before the evidence is evidence-ready |
|------|-----------------------------------------------|
| **Domain gate** | Query must match scope vocabulary or a known document number/reference before evidence is served. |
| **Evidence gaps** | Missing/non-binding, unresolved relation, relation-target, validity-unknown, and partial-validity gaps must be explicit fields. They are context, not hidden ranking tweaks. |
| **Quality worklist** | MCP `quality_gaps` returns exact DB rows for fetch leftovers, OCR-only docs, mojibake-like chunks, partial validity, unresolved refs, and relation targets with no indexed binding text. |
| **Text provenance** | Every MCP hit/document should expose binding status, authority, extraction engine, confidence, and `needs_review` where known. |
| **Validity consistency** | When a document's recorded `effective_from` precedes its `issued_date` (impossible — a source-side data error), the MCP surfaces a `validity.warning` and does **not** correct the date. banhmi never invents a date; the connecting agent verifies against the enacting clause (Điều khoản thi hành). |
| **Relation context** | Amendment/repeal/status questions must consult confirmed relations before normal chunk ranking. |
| **Related hits** | Relation-expanded snippets are returned as `related_hits[]`, never folded into primary rank. |
| **Clause validity** | A repealed/superseded clause inside a partly-current document must not be presented as current. |
| **Golden set** | Expectations must cite exact `Điều/Khoản`, include relation cases, OCR gaps, and out-of-domain controls. |

## Rejected: query-time reranker (decided 2026-07-19)

**No cross-encoder reranker in the read path** — measured, not assumed. Offline experiment
(Qwen3-Reranker-0.6B FP16 on Kaggle T4, harness: `cmd/eval -rerank-dump`/`-rerank-scores` +
`tools/rerankkernel`):

- **Semantic reranking fights the current-law contract** — it promotes superseded versions of the
  right rules (naive pass: VN recall 92.7 → 82.9, current-law precision → 86.7%). Any deployment
  must rerank the strict current-law pool only, with the badged tail pinned.
- **Latency economics don't close**: 0.76 s/pair measured (CPU INT8, AVX-VNNI). VN/MY misses sit
  at pool ranks ≤16 (~12 s/query) but ID's sit at 22–79, needing N≈50 ≈ 38 s/query. No GPU quota;
  a GPU sidecar is ~$400+/mo for single-digit pp on already-passing floors.
- **Revisit triggers**: GPU quota granted, a materially better small multilingual reranker, or
  corpus growth pushing pool-recall down. The long-game alternative is reranker-as-teacher:
  distill its judgments into embedder fine-tuning (MVP2).

## Known Gaps

- **Legacy outline locations** can still produce weak legal citations on very old outline-only documents.
- **Source title/data typos** — VBPL titles occasionally carry typos that defeat exact scope matching
  ("thông tin khách **hành**", "không **dung** tiền mặt"). Primary corpus classification (`Match`) stays
  diacritic-exact by design. For relation-pulled documents, `MatchFolded` retries with diacritics
  stripped as a rescue before demoting to `relation_context` — this catches partial diacritics errors
  in source data without over-matching the primary discovery path.
- **Source validity typos** — rare VBPL `effFrom` data-entry errors (e.g. `77/2025/TT-NHNN` shows effective `2025-03-01`, *before* its `2025-12-31` issuance; the enacting Điều 12 says `2026-03-01`). The MCP **flags** these via `validity.warning` rather than correcting them — banhmi stays faithful to the source and lets the agent judge from the enacting clause.
- **Unknown validity** — portal-only documents (no source status) are classed `unknown`, excluded from
  the current-law pass, and badged "Validity unknown — verify against the official source". banhmi does
  not derive repeal from another document's title (e.g. 2872/QĐ-NHNN repealing 2345) — the agent judges
  from the surfaced documents.
- **VBHN family-derived validity** — consolidations (Văn bản hợp nhất) carry no status of their own, so
  the `vbhn_validity` post-normalize pass (VN-only) derives it from the base family: the **newest
  consolidation per base mirrors the base document's current status**, older consolidations are expired,
  and an unresolved base → `unknown`. Consolidations are indexed as primary but the base + amendments
  remain the binding originals (Pháp lệnh 01/2012/UBTVQH13). When the source carries no `consolidates`
  relation, the pass parses the base from the VBHN's own text (footnote "sửa đổi, bổ sung một số điều
  của <TYPE> số <NUM>" + the preamble before "được sửa đổi, bổ sung bởi:"; majority vote, applied only
  on an unambiguous corpus match; reason suffixed `_text`).
- **Consolidation-family collapse (retrieval)** — a consolidation and its base are both current law and
  carry the same provision under the same citation, so they'd fill two top-k slots with one provision.
  The primary ranking collapses same-family same-citation hits to the best-ranked twin (families =
  connected components of resolved `consolidates` relations, loaded at startup; empty map elsewhere =
  no-op). The dropped twin stays reachable via the kept hit's `consolidates` relation; eval goldens
  accept the consolidation identity via `alt_doc_numbers`.
- **Duplicate silver sections** — the `citation_path` uniqueness suffix is applied at index time only, so a normalize pass that emits the same section twice still yields duplicate citations (~36 VN pairs / 13 docs). The durable fix is applying uniqueness at section creation in Normalize.
- **Source status vs. forward relations — surfaced, not corrected.** A source can badge a document
  current while another document's confirmed relation replaces/repeals it (VN: 49 `in_force` + 64
  `partial` indexed docs; e.g. `101/2012/NĐ-CP` badged in force with `52/2024/NĐ-CP` replacing it).
  **VN-only in practice** (measured 2026-07-26): ID has 756 `revokes` edges but **zero** of their
  resolved targets are indexed or carry a validity row — repealed ID documents are unindexed
  relation-context stubs, so there is no contradiction to surface. The check is corpus-driven, so it
  activates automatically if that ever changes.
  banhmi emits a **`validity.warning`** naming the superseding documents and **never rewrites the
  badge** — deriving repeal would be banhmi asserting a legal conclusion, and a `replaces` can be
  partial in practice. The superseding type set is config-driven
  (`config.relation_type.is_superseding`: repeals/replaces/revokes; `partially_revokes` excluded).
- **The domain gate is topic-only** — it never tests the query's *jurisdiction*, so a question about a foreign regulator on an in-domain topic is served rather than abstained. Golden sets must not encode cross-jurisdiction abstain cases.
- **Bare-số ambiguity** — distinct documents can share a số ký hiệu; the `document` tool prefers the
  primary/indexed match and lists the rest in `also_matches`.
