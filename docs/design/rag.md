# RAG evidence

Retrieval-augmented generation (RAG) is banhmi's evidence layer. banhmi returns source text,
citations, validity metadata, relation context, and data gaps. It does not contain an answer model.

## Current read path

Production search is hybrid retrieval over one PostgreSQL database per jurisdiction:

1. The scope gate checks the raw query against `config.scope_term` and known document references.
2. Query transforms add configured abbreviation expansions to both retrieval arms. Vietnam may also
   restore unaccented words for the dense arm and boost the lexical arm for unaccented or
   document-number queries.
3. The dense arm searches 1,024-dimension Qwen3-Embedding-0.6B vectors. The lexical arm searches
   BM25 sparse vectors stored as pgvector `sparsevec` values in `gold.chunk.content_sparse`.
4. Reciprocal rank fusion (RRF) combines the two ranked lists.
5. Provision roll-up, consolidation-family deduplication, and the per-document cap reduce duplicate
   results.
6. The retriever attaches validity, text provenance, and confirmed relations. MCP response shaping
   then applies the requested `detail` level.

The query model and indexed model must match. The canonical tag is
`qwen3-embedding-0.6b`, and every vector has 1,024 dimensions.

## Chunk and citation model

`Index` reads `silver.document_section` and writes provision-aware `gold.chunk` rows.

- Article-level units are `Điều`, `Section`, `Pasal`, and the equivalent jurisdiction-specific
  sections.
- A provision above the rough 512-token limit is split through its legal children before falling
  back to deterministic paragraph shards.
- Exact duplicate content within one document is dropped.
- Content shorter than 20 runes after label normalization is dropped.
- Appendix units (`Phụ lục`, `Schedule`, and `Lampiran`) are indexed. Explanatory nodes such as the
  Indonesian `penjelasan` are not part of the normal provision loop when primary provisions exist.
- Each embedding input is the deterministic context prefix plus chunk content. The prefix contains
  the document number, title, enclosing headings, and effective date or issue date. It is an
  embedding hint, not legal evidence.

Human citations preserve parser disambiguators when labels restart. Vietnam renders a duplicate
path suffix as `Đoạn N`; Malaysia and Singapore render it as `[N]`. Headings remain in the context
and content, not in the base legal citation.

Search ranks the fine-grained chunk. The retriever also assembles its enclosing article from all
matching chunks. MCP exposes that assembly as follows:

| `search.detail` | Returned context |
|---|---|
| `compact` | Metadata, snippet, citation, validity badge, scope, and gaps. No relations or provision pointer. |
| `standard` or omitted | Adds relations, related hits, full validity, and the enclosing-provision citation. It does not inline the full provision text. |
| `full` | Adds diagnostics, detailed provenance, and the full enclosing provision when it is at most 14,000 runes. Larger provisions return a pointer with `truncated=true`. |

Use `document` with `include=['chunks']` and a citation copied from search to read one full
provision without requesting a full search response.

## Ranking behavior and defaults

The split AWS server image does not include a config file, so it uses the compiled defaults. A local
deployment can override them in its own config file.

| Setting | Effective behavior |
|---|---|
| `top_k` | Compiled default: 8. |
| `vector_k`, `bm25_k` | Compiled default: 50 each. The retriever's internal fallback is 100 only when the corresponding config value is zero. |
| `rrf_k` | 60. |
| Lexical weight | Base weight 0.5; Vietnam's routed queries use 1.0. Other jurisdictions keep the base weight unless configured otherwise. |
| Roll-up | `khoan` by default: collapse finer siblings to their sub-article parent. Supported values are `khoan`, `dieu`, and `none`. |
| Per-document cap | Effective default: 3 primary hits per document. The current resolver treats zero as “use the default”; the comments that say zero disables the cap are stale. |
| Section aggregation | Disabled by the compiled application config. If `retrieve.section_aggregate` is enabled, up to two qualifying multi-fragment groups are appended after the natural top-k; existing top-k entries are not reordered or removed. |

When `in_force_only` is omitted, the primary pass filters to `in_force` and `partial` documents. A
vector-only secondary pass then appends at most three non-current hits, with no more than one hit per
document. This tail is skipped when the caller:

- sets `in_force_only=true` for a strict current-only search;
- sets `in_force_only=false` for an unfiltered search;
- supplies a date or metadata filter;
- uses a path without a query vector;
- searches a corpus whose validity data cannot support the current-law filter.

Vietnam also scopes a query to a known document when the query names its identifier. Unless the
caller explicitly requests strict current-only behavior, that document can surface with its real
validity badge even when it is not current.

Optional prefilters run before ranking:

- `as_of`: recorded effective window contains the date;
- `issued_from` and `issued_to`: document issue-date bounds;
- `issuer`: case-insensitive substring match;
- `doc_type`: case-insensitive exact match.

Document-level validity controls ranking. Clause-level validity is incomplete; a `partial` document
without a matching section-level validity row produces a `partial_validity_uncertain` gap.

## Query embedding service

The live AWS read path uses two ECS services on one host:

- one slim, multi-jurisdiction `cmd/server` process with database access and MCP logic;
- one `cmd/embedder` process on loopback port 8089 with the Qwen3 ONNX FP16 model and no database
  credentials.

`cmd/server` leaves `BANHMI_EMBED_QUERY` unset and calls the embedder through
`BANHMI_EMBED_ENDPOINT`. `BANHMI_EMBED_TOKEN` protects the endpoint. The server shares one HTTP
embedder client across all jurisdictions and verifies the model tag and dimensions. An unavailable
embedder returns an error; production does not fall back silently to BM25.

Local evaluation and `tools/mcpcall` use the in-process ONNX implementation through
`BANHMI_EMBED_QUERY=onnx` and the `onnx` build tag. The same process may also be used for a
single-container self-hosted deployment.

Qwen3 is asymmetric. Documents are embedded as raw context-prefix-plus-content text. Queries are
formatted by `embed.FormatQuery` as:

```text
Instruct: Given a legal or regulatory query, retrieve relevant provisions and regulatory text that address the query
Query:<query text>
```

Both the Kaggle document path and ONNX query path use last-token pooling followed by L2
normalization.

## Bulk embeddings

The active production batch path is Kaggle. Configure its token and engine before running it:

```bash
export BANHMI_EMBED_ENGINE=kaggle
export KAGGLE_API_TOKEN='<token>'
go run ./cmd/pipeline -embed-all
go run ./cmd/pipeline -embed-all -force -limit 1000 -embed-parallel 2
```

`EmbedAll` writes vectors under the canonical model tag. With the Kaggle engine and parallelism
greater than one, it:

1. copies unchanged vectors from the content-addressed `ingest.embedding_cache`;
2. splits cache misses into partitions of at most 25,000 chunks;
3. runs at most two Kaggle kernels concurrently;
4. upserts each vector and cache entry as it arrives.

Each kernel mounts the configured Qwen3 ONNX FP16 model dataset, reads `input.jsonl`, and emits
`vectors.jsonl.gz`. `KAGGLE_API_TOKEN` is the only required Kaggle credential; the owner is derived
from the token when omitted. Unless `KeepArtifacts` is set by a caller, the per-run dataset and
kernel are deleted on both success and failure.

Important engine boundaries:

- `cmd/pipeline -embed-all` implements `kaggle` and `sagemaker`; it does not implement a `local`
  branch. Only the Kaggle branch matches the current Qwen3 index. The wired SageMaker branch still
  embeds BGE-M3 and must not be used with the canonical index.
- `config.EmbedEngine()` resolves `auto` to `local` without a Kaggle token. Therefore `-embed-all`
  with the compiled default and no token returns an unsupported-engine error. Use Kaggle for the
  full pipeline, or use `cmd/embed-backfill` for local HTTP or ONNX maintenance embedding.
- `embed.kaggle.min_batch` is used by `cmd/embed-backfill`. `EmbedAll` does not use that threshold.
- The single-kernel Kaggle path does not use `ingest.embedding_cache`; the default parallel path
  does.

The S3 data bucket stores fetched files and the OCR cache. Kaggle embedding input and output use
Kaggle datasets, not S3 or Google Cloud Storage.

## Evidence and safety controls

Current controls expose uncertainty instead of turning it into prose:

| Control | Current behavior |
|---|---|
| Domain | A query outside configured scope and without a known reference returns an `out_of_domain` blocking gap. Hits remain visible for inspection. |
| Relevance floor | `retrieve.abstain.min_score` gates on top-hit cosine similarity, not the rank-derived RRF score. BM25-only hits have no cosine value and are not score-gated. |
| Retrieval gaps | Search can return `no_evidence`, `known_binding_text_gap`, `unresolved_relation_target`, `relation_target_text_gap`, `text_needs_review`, `validity_unknown`, and `partial_validity_uncertain`. |
| Provenance | Hits and documents expose binding state, review state, authority, source, engine, and confidence when available. Compact and standard detail retain the summary quality flags. |
| Validity contradiction | Source dates and confirmed superseding relations can produce `validity.warning`. banhmi reports the source values and does not rewrite them. |
| Relations | Confirmed relations appear on the first hit for a document. `related_hits[]` stays separate from primary rank. The `document` tool returns verbatim incoming amendment clauses and, for multi-hop lineages, `amendment_chain[]`. |
| Quality worklist | `quality_gaps` accepts `fetch`, `non_binding`, `mojibake`, `partial_validity`, `unresolved_relations`, and `relation_target_text`. |

Relation confidence describes the source assertion, not legal truth. `structured_relation` comes from
official source metadata and is not necessarily corroborated by document text. `weak_relation` is
derived from document wording and includes its stored location and snippet. Neither replaces review
of the cited clause.

## Retrieval evaluation

`pkg/eval` and `cmd/eval` score retrieval only against the golden file selected by the jurisdiction
registry. The Make targets run the in-process ONNX query embedder and write JSON under
`test/samples/eval/`:

```bash
make eval-vn
make eval-my
make eval-id
make eval-sg
make eval-th
make eval-kh
```

Run jurisdictions sequentially because each process loads the model. Use
`-retrieval-mode=vector`, `-retrieval-mode=bm25`, or `-retrieval-mode=hybrid` to compare arms.
`-review` adds a database quality audit and hit previews. `-pool-k N` retrieves a deep pool with the
document cap raised to N, which helps separate top-k ranking misses from candidate-coverage misses.

Metric aggregation is exact:

- recall@k is a micro-average over expected citations;
- MRR@k is the mean reciprocal rank over cases with expectations;
- current-law precision is a micro-average over scored hits after the disclosed trailing
  non-current run is removed;
- abstention accuracy is the fraction of scored cases with the expected abstention decision;
- `expect_fail` cases are excluded from all aggregate metrics and reported as `GAP-PASS` when their
  full expectation starts passing.

Golden document numbers are canonicalized for retrieval evaluation. `alt_doc_numbers` provides
additional valid identities, and a doc-level `relation_ok` expectation may be satisfied by a
confirmed relation on a hit. Provision fields use each jurisdiction's configured citation keywords.

Accepted metrics belong in [PLAN](../../PLAN.md#current-state). Gate floors belong in the Makefile.
Update both only after a deliberate accepted run; do not copy dated corpus measurements into this
design document.

## Unsupported, rejected, and deferred paths

| Status | Path |
|---|---|
| Rejected | Apply [model boundaries](../../AGENTS.md#product-and-evidence-boundaries). The experiment flags and `tools/rerankkernel` remain offline artifacts. |
| Unsupported with the canonical index | The wired SageMaker batch script still embeds BGE-M3 but stores vectors under the canonical Qwen3 tag. Do not use `embed.engine=sagemaker` until the recipe and model tag are corrected. |
| Legacy compatibility only | `BANHMI_EMBED_QUERY=openvino` loads BGE-M3 while the canonical index is Qwen3. It must not be used against the current index. |
| Deferred | Clause-level validity coverage and offline reranker-as-teacher distillation. Serving follows [model boundaries](../../AGENTS.md#product-and-evidence-boundaries). |

Known data limits include weak citations in some outline-only documents, incomplete clause validity,
source metadata typos, unresolved relation targets, unknown validity, and ambiguous document numbers.
The tools surface these conditions; they do not infer a legal correction.
