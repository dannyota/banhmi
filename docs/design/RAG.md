# RAG Evidence

RAG is the database evidence layer. Users may bring their own model. banhmi must expose faithful
chunks, citations, relation context, provenance, and gaps without hiding weak data behind prose.

## Current Shape

| Area | Current behavior | Limit |
|------|------------------|-------|
| **Chunks** | Gold chunks are provision-aware, normally by `Điều`; long articles split by `Khoản` / paragraph shard. Search keeps the fine-grained chunk as the ranked match but **re-attaches the full enclosing `Điều`** (all its `Khoản`, reassembled verbatim from its chunks, lead-in deduped) as `hit.provision` so a matched clause is never read out of context. A pathological oversized `Điều` (e.g. an amendment law whose `Điều 1` is the whole law, hundreds of chunks) returns a `provision` **pointer** (`truncated`, no inline text) rather than a truncated-from-start blob that could omit the match — the agent opens the `document` tool. | Short but real legal provisions are kept; appendix/form folding still needs cleanup. |
| **Citation** | Chunk citation is label-only, e.g. `Điều 7, Khoản 2`; headings stay in content/context, not citation. | Legacy outline docs can still produce weak legal locations. |
| **Context prefix** | Prefix is deterministic: document number/title, chapter/section heading, effective date. Long fields are capped. | Prefix is an embedding hint, not evidence. |
| **Retrieval** | Vector-only (BGE-M3 over pgvector) over `gold.chunk`. Default: current law leads the primary pass (`in_force`/`partial`); a small secondary pass of non-current law is appended **badged** after it so repealed/overlapping law stays findable, not excluded. `InForceOnly=true` → strict current-only; `false` → no filter. Optional, **gated query-time pre-filters** narrow eligible documents without touching embeddings — **`as_of`** (point-in-time: law whose effective window contains the date), **issued-date range**, and **issuer / doc-type facets**; with no filter the path is byte-for-byte unchanged. Scoped queries skip the non-current pass. | Validity is document-level; clause-level validity is missing. `as_of` relies on recorded effective dates. |
| **Relations** | Each retrieved hit carries up to eight confirmed incoming/outgoing `silver.document_relation` edges. | Relations are not rank boosts and do not replace chunk evidence. |
| **Weak relations** | `silver.relation_evidence` weak rows are stored for review/classification. | Weak rows are not exposed as confirmed legal status. |
| **Surfaces** | MCP is the only query surface, exposing `guide`, `corpus_status`, `quality_gaps`, `search`, and `document`. Search returns `hits[]` (ranked, with source link, cite, validity badge, **issued date**, text provenance, confirmed relations, scope signals — plus a **`validity.warning`** when the source's own dates are internally inconsistent — and **`provision`**: the full enclosing `Điều` verbatim, so `snippet` stays the precise matched clause while `provision.text` gives the whole article) and `related_hits[]` — graph-adjacent chunks that **each carry their own `source_url` + `cite`** — plus `gaps[]`. `document` adds all official **`sources[]`** for the doc, a chronological **`timeline`** (issued → effective → amended/replaced → expired), validity periods, chunks, relations, verbatim incoming amendments, and citation-miss gaps. | The user-owned agent/model decides how to use the evidence. |
| **Agent contract** | English-first tool/param/field descriptions; a server-level `instructions` brief — the **trust stance** (text extracted verbatim from official government sources VBPL / Công Báo / SBV, evidence-only, never synthesized), **live coverage counts** (documents/provisions, stamped at startup), when to reach for it, how to cite, and examples; and read-only tool annotations so hosts can auto-approve. Legal **data stays Vietnamese, verbatim**; only the contract is English. Queries work in English or Vietnamese (BGE-M3 multilingual). | Returns **content + official source links only — never files**. The connecting model decides the answer. |

## Kaggle batch embedding (optional bulk engine)

An **optional** engine that offloads **bulk/backfill** embedding to a **Kaggle GPU** (2× Tesla T4) so the
local GPU/laptop isn't doing the heavy work. The 43,510-chunk corpus embeds in **< 2 min of GPU
compute**; a full reindex was validated end-to-end (2026-05-30).

- **Boundary — batch only:** Kaggle is **never** the query-time / serve-time embedder. The query path
  **always** stays the BGE-M3 embedder (OpenVINO, served by the local OVMS container during development or
  in-process on Cloud Run); serving from Kaggle is ToS-prohibited and has no live endpoint. `embed.engine`
  chooses only the **bulk** engine, never the query path.
- **Chunking stays in Go:** deterministic chunking is **never** ported to Kaggle — only embedding offloads.
- **Auth — one env var:** set `KAGGLE_API_TOKEN` (the `KGAT_…` token from Kaggle → Settings → API → Create
  New Token). The Kaggle **owner is auto-derived from the token** (token introspection / `WhoAmI`) — there
  is **no `KAGGLE_USERNAME`** to set, and the token never lives in YAML.

**Config** — `config.yaml` `embed:` block (`EmbedConfig` in `pkg/base/config`):

| Key | Default | Meaning |
|-----|---------|---------|
| `engine` | `auto` | `auto` = kaggle when `KAGGLE_API_TOKEN` is set, else `local`; `local` forces the local OpenVINO endpoint; `kaggle` forces Kaggle. |
| `kaggle.model_dataset` | `danhsoftware/bge-m3-banhmi` | Public, unmodified `BAAI/bge-m3` mirror, mounted **offline** (no HuggingFace download). Empty = pull `BAAI/bge-m3` from HuggingFace with internet on. |
| `kaggle.accelerator` | `NvidiaTeslaT4` | Kaggle machine shape → 2× T4. |
| `kaggle.min_batch` | `500` | Below this many missing chunks, `embed-backfill` stays local (cold start isn't worth it). |

**How to run:**

- **Temporal workflow** (observable in the Temporal UI; external activity queue with heartbeats):
  `go run ./cmd/worker -embed-all` (missing chunks only) · `-embed-all -force` (re-embed ALL, overwrite) ·
  add `-limit N`. Needs Temporal + Postgres up and `KAGGLE_API_TOKEN` set.
- **Non-Temporal CLI escape hatch:** `go run ./cmd/embed-backfill -force [-limit N]`.

**Flow (kaggle engine):** Index writes `gold.chunk` only — embedding is **deferred** (a nil embedder is
skipped, best-effort) → **EmbedAll** uploads `(chunk_id, text)` as a Kaggle dataset (`banhmi-embed-input`)
→ pushes a GPU kernel mounting the model mirror → polls to completion → downloads vectors → upserts
`gold.chunk_embedding` under the **canonical model tag** (`config.EmbedModel`) so retrieval
(`WHERE model = …`) finds them regardless of engine.

- **Auto-cleanup:** on **success** the embed kernel **and** the input dataset are **auto-deleted** (no
  leftover notebooks); on **failure** both are **kept** for debugging.

**Vectors / parity:** BGE-M3 dense, **CLS pooling + L2-normalize, 1024-d** — the same recipe as the local
OVMS embedder. Kaggle (FP16) vs local OVMS (INT8) are **~0.998 cosine-aligned**, so corpus-compatible; all
vectors are stored under the one canonical model tag.

**Library:** banhmi imports **`danny.vn/kaggle`** — an unofficial Go port of Kaggle's Python `kagglesdk`
(Apache-2.0), in a separate repo wired via a `go.mod` `replace danny.vn/kaggle => ../kaggle-go` until
published.

**Key files:** `pkg/rag/embed/kagglebatch/` (orchestration) · `pkg/pipeline/embed_all.go`
(`EmbedAllWorkflow` + `EmbedAll` activity) · `cmd/embed-backfill` · `cmd/worker -embed-all` ·
`pkg/base/config` (`EmbedConfig`/`EmbedKaggleConfig`, `EmbedEngine()`).

## Eval

Use DB-only retrieval review to check the evidence is sound before relying on it. Production retrieval
is vector-only; `pg_search`/BM25 and hybrid exist only here, behind the eval harness, for comparison:

```bash
go run ./cmd/eval -retrieval-only -retrieval-mode vector -review
```

As of 2026-05-30 after forced reindex:

| Check | Result |
|-------|--------|
| **Corpus** | 401 Silver docs, 386 indexed docs, 43,510 chunks |
| **Embeddings** | 43,510/43,510 configured-model chunks embedded |
| **Citation shape** | 0 overlong citations over 120 chars |
| **Strict golden recall** | 62.5% recall@k, 58.3% MRR@k |
| **Current-law precision** | 100% on returned eval hits |
| **Evidence gate** | Out-of-domain/no-evidence cases return no evidence; OCR binding gaps are exposed as `gaps[]` context |
| **Binding safety** | 15 non-binding-only OCR docs remain unindexed; 0 indexed docs are non-binding-only |

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

## Known Gaps

- **TT 50/2024** safety/security query still retrieves `Điều 1` before the specific security provisions.
- **Payment intermediary conditions** still rank old/current-related payment decree text before
  `52/2024/NĐ-CP Điều 22 Khoản 2`.
- **Biometric payment limit** surfaces the non-binding OCR decisions as data gaps while chunk search
  still finds adjacent indexed security material. The user-owned agent needs both signals.
- **Mojibake** still appears in five indexed chunks; normalize now has a localized binding-text guard,
  but existing rows need targeted re-normalize after source review.
- **Appendix/form tails** and **legacy outline locations** still need deterministic cleanup.
- **Source validity typos** — rare VBPL `effFrom` data-entry errors (e.g. `77/2025/TT-NHNN` shows effective `2025-03-01`, *before* its `2025-12-31` issuance; the enacting Điều 12 says `2026-03-01`). The MCP **flags** these via `validity.warning` rather than correcting them — banhmi stays faithful to the source and lets the agent judge from the enacting clause.
