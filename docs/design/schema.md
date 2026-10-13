# Data model

banhmi stores each jurisdiction in a separate PostgreSQL database. Within that database, data moves
through `ingest` → `bronze` → `silver` → `gold`. The `config` schema holds crawl, validity, relation,
and query policy.

This model is the legacy schema. Its five chains under `deploy/migrations/` are frozen under
[product boundaries](../../AGENTS.md#product-and-evidence-boundaries): they serve the five maintained
countries and the current Vietnam service and receive no new migrations. The new Vietnam writer and
release databases use the chains named in the [corpus database spec](../specs/corpus-database.md).

Handwritten Goose migrations under `deploy/migrations/` and `deploy/authmigrations/` are the only
SQL schema source. sqlc reads each corresponding migration directory; queries remain under `sql/`.
Migration-backed sqlc configuration and trusted-history checks are coded and independently reviewed.
The [Goose migration plan](../plans/goose-migrations.md#evidence-and-remaining-gates) owns hosted
generation and replay evidence. The design uses sqlc, single-column surrogate primary keys,
composite `UNIQUE` constraints for natural keys, and no foreign keys across schemas. Foreign keys
inside one schema are used where they help integrity.

## `ingest`: discovery and work state

| Table | Current role | Main keys and state |
|---|---|---|
| `discover_cursor` | Incremental cursor for one discovery slice | Unique `(source, keyword)`; `watermark`, `expected_total`, `last_seen_total`, `last_run_at` |
| `fetch_doc` | One source document in the fetch ledger | Unique `(source, external_id)`; state is `discovered`, `planning`, `fetching`, `partial`, `complete`, or `error` |
| `doc_discovery` | Append-only reasons a document entered the ledger | Unique provenance tuple over document, path, keyword, source document, and relation type |
| `fetch_artifact` | Retryable body, tree, file, relation, or appendix work | Unique `(fetch_doc_id, kind, ref_key)`; lease, attempt, retry, and terminal state columns |
| `embedding_cache` | Content-addressed dense-vector cache | Unique `(text_hash, model, dims)`; survives Gold rebuilds |

`fetch_doc.plan_ready` means Fetch has enumerated the document's artifacts. Completion is derived from
child rows and recorded in `artifacts_expected`, `artifacts_done`, and `artifacts_failed`. A document
becomes `partial` when every artifact is terminal but at least one is dead-lettered. It becomes
`complete` when every required artifact succeeds or is otherwise resolved.

`fetch_artifact.state` includes `pending`, `claimed`, `done`, `error`, `dead`, `skipped`, and
`superseded`. There is no `skipped` column. `is_optional` marks optional work.

Other recovery fields cover expired leases, empty VBPL trees, placeholder source content, expiring
URLs, and changed discovery fingerprints. Re-discovery reopens a fetched document only when its
`content_hash` changes.

## `bronze`: source observations

| Table | Current role | Important details |
|---|---|---|
| `source_document` | One document as one source observed it | Unique `(source, external_id)`; source metadata, normalized identity fields, status, dates, `raw_meta`, and collection timestamps |
| `raw_payload` | Inline HTML or JSON | Kinds: `content_html`, `provision_tree_json`, `references_json`, `detail_json`; unique `(source_document_id, kind)` |
| `raw_file` | Downloaded file metadata; bytes stay outside PostgreSQL | Role × format × ordinal identity; storage path, URL, SHA-256, language, byte size, and authority flag |

Bronze does not merge sources. Cross-source identity is resolved in Silver.

## `silver`: normalized documents and legal evidence

| Table | Current role | Important details |
|---|---|---|
| `document` | One logical document | Unique `doc_key`; display metadata, selected source observation, metadata priority, `is_consolidated`, and `index_class` |
| `document_alias` | Source observation → logical document | Unique `(source, external_id)` |
| `document_text` | Extracted text and its authority | Unique `(document_id, authority, source)`; binding flag, review flag, engine, confidence, and source/verbatim hashes |
| `document_section` | Parsed provision tree | Unique `(document_id, citation_path)` and `(document_id, node_key)`; normalized jurisdiction-specific `kind` plus raw `ptype` |
| `validity_period` | Document-level validity now; clause/version shape later | Legal time, observation time, source status, normalized status class, and optional cause reference |
| `doc_ref` | Resolved relation target or unresolved stub | Unique `ref_key`; nullable `document_id`; raw source reference in `src_ref` |
| `relation_evidence` | Evidence offered for a relation | Structured, weak, or future model evidence; exact source wording, source authority, confidence, and `promoted` verdict |
| `document_relation` | Confirmed graph edge | Unique `(from_document_id, to_ref_id, relation_type)` |
| `amendment_event` | Reserved first-class amendment event | Table and queries exist, but the current pipeline does not populate it |
| `document_topic` | Reserved topic tags | Table and queries exist, but the current pipeline does not populate it |
| `document_gazette` | Reserved document ↔ gazette links | Table and queries exist, but the current pipeline does not populate it |

### Document identity

The shared `docKey` function normally returns `<TYPE>|<NUMBER>`. It falls back to the normalized number
when type is missing, and to `source:external_id` when number is missing.

The function also contains current jurisdiction-specific repairs:

- Indonesian OJK-family infixes can override a wrong source type and normalize prefixed or bare forms.
- Vietnamese number suffixes can override a wrong source type.
- A Vietnamese VBHN number is keyed as `VBHN|<NUMBER>|<ISSUED_YEAR>` when the issue date exists,
  because the State Bank reuses some VBHN numbers across years.

`doc_key` changes are not retroactive. Existing Bronze/Silver rows must be re-extracted or rebuilt after
identity logic changes.

### Relations

`doc_ref` lets the graph retain a target before that target is ingested. A source target ID is preferred
when the source supplies one. Normalized document-number text is the fallback.

Only promoted `relation_evidence` creates `document_relation` rows. `confidence` records how strongly
the source asserts an edge; it is not a truth score. Structured metadata can still be wrong. For
example, a 2026-07-26 audit found that four of seven VBPL `amends_supplements` targets attached to
`143/2025/QH15` appeared only in recitals.

VBPL and AGC LOM structured relation feeds promote directly. Other source metadata promotes only through
an exact `config.relation_type` row marked `is_amending`. Source wording and direction therefore matter
for those sources. Missing Indonesian OJK forward operators remain an open seed gap. Relation labels and
the `is_superseding` flag also drive read-path amendment and contradiction evidence.

### Validity and text authority

`validity_period.status_class` allows `in_force`, `expired`, `partial`, `not_yet`, `suspended`,
`inappropriate`, and `unknown`. A source status is preserved in `status_code`. A missing source status
uses the jurisdiction's fallback policy; it does not always remain `unknown`. VBHN is the exception in
Vietnam: its family pass derives validity from the base document and older consolidations.

Normalize prefers usable binding text. If no binding text exists, it may parse a usable non-binding OCR
transcription as explicitly non-binding, reviewable evidence. If binding candidates exist but fail the
quality gate, Normalize fails closed and writes no sections from a weaker fallback.

The schema has nullable `version_id` and `document_version_id` business keys, but there is no
`silver.document_version` table yet. Clause-level validity and dated document versions are deferred.

## `gold`: retrieval records

| Table | Current role | Important details |
|---|---|---|
| `chunk` | Structure-aware retrieval chunk | Citation, context prefix, content, ordinal, token count, and optional BM25 `sparsevec` |
| `chunk_embedding` | Dense vector for one chunk/model/dimension tuple | Qwen3-Embedding-0.6B, 1,024 dimensions; HNSW cosine index |
| `document_summary` | Reserved document summary | Table and queries exist, but no current pipeline stage populates it |

Index chunks at an article-like level for each jurisdiction and splits long provisions into smaller
clause or paragraph shards. `LexicalIndex` writes `chunk.content_sparse`. `Index` can write dense vectors
through its configured query embedder, but that write is best-effort. `EmbedAll` is the bulk completion
path and uses `ingest.embedding_cache` for unchanged text.

## `config`: runtime policy

| Table | Drives | Natural key |
|---|---|---|
| `scope_term` | Jurisdiction scope matcher | `(jurisdiction, term_class, term)` |
| `issuer_code` | Source issuer selection, including VBPL SBV and non-SBV groups | `(source, code)` |
| `discovery_keyword` | Trusted server-side keyword slices | `(source, term)` |
| `setting` | Extraction, discovery, amendment, and retrieval settings | `(key)` |
| `validity_status` | Source status → normalized class and current-law flag | `(source, code)` |
| `relation_type` | Source relation code → label and amendment/supersession flags | `(source, code)` |
| `diacritic_restore` | Query-time Vietnamese token restoration | `(jurisdiction, folded_token)` |
| `abbreviation_expand` | Jurisdiction-specific query expansion | `(jurisdiction, abbreviation)` |

Only `scope_term` and `discovery_keyword` have an `enabled` column. `relation_type` has both
`is_amending` and `is_superseding`. The config-level validity constraint does not include `unknown`;
`unknown` is the application's fallback when no configured source code matches.

### Seed and operator rows

Every config row has `origin='seed'` or `origin='user'`. `cmd/seed` deletes seed rows and reloads the
embedded CSV defaults in one transaction. Rows marked `user` survive. Inserts use
`ON CONFLICT DO NOTHING`, so a user row on the same natural key blocks the seed default.

To turn a shipped row into a persistent override, update that row's values and set `origin='user'`.
For `scope_term` or `discovery_keyword`, an operator can also set `enabled=false` in the same update.
Changing only `enabled` on a row that remains `origin='seed'` is temporary; the next seed run recreates
the default.

```bash
go run ./cmd/migrate
go run ./cmd/seed
```

The seed binary embeds `deploy/seed/*.csv`. Per-jurisdiction setting files load before shared settings,
so the first insert for a key wins within that jurisdiction's database.

## Current and deferred shapes

The current pipeline populates the ingest ledger, Bronze tables, Silver documents, aliases, texts,
sections, validity periods, relation evidence, confirmed relations, Gold chunks, dense embeddings, and
sparse vectors.

The following shapes exist but are not populated by the current pipeline: `amendment_event`,
`document_topic`, `document_gazette`, and `document_summary`. Dated document versions, clause-level
validity, and section-level relation targets remain deferred. Relation-target fetch is implemented for
allowlisted sources; it is not deferred.

See [sources.md](sources.md) for discovery and source contracts, [pipeline.md](pipeline.md) for the
writers, and [ARCHITECTURE.md](../ARCHITECTURE.md) for the system boundary.
