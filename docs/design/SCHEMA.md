# Data model (schema design)

banhmi's PostgreSQL schema, finalized from the source-enumeration + hotpot-pattern + schema-review
research. **Forward-compatible but MVP-shallow**: the structurally hard-to-migrate shapes (discovery
ledger, the reference graph, validity intervals, text authority) exist now; MVP1 populates them
shallowly. Conventions follow [`CLAUDE.md`](../../CLAUDE.md): sqlc, no cross-schema FKs (cross-layer
links are business-key `BIGINT`s), JSONB for non-queryable data, queryable arrays as child tables,
single-column surrogate primary keys (never a composite PRIMARY KEY — natural keys are composite
`UNIQUE`), named constraints/indexes ≤63 bytes.

Schemas: **`ingest`** (discovery/queue ledger — pipeline state) → **`bronze`** (raw as fetched) →
**`silver`** (one normalized logical document) → **`gold`** (chunks + embeddings), plus **`config`**
(tunable policy — scope/issuers/keywords; the "no hardcoded lists" rule).

## `ingest` — discovery/queue ledger (the completeness engine)

Discovery is keyword-filtered per source; one document → many artifacts; nothing may be silently
missed. Four tables; a document is **complete iff `artifacts_done == artifacts_expected`, recomputed
from child rows** (never a lying boolean).

| Table | Role | Key columns |
|-------|------|-------------|
| `discover_cursor` | Per **(source, keyword)** watermark | `watermark`, `expected_total` (snapshot of `data.total` → prove the whole keyword slice was enqueued), `last_run_at` |
| `fetch_doc` | Parent: one row per document | `(source, external_id)` UNIQUE · `state` (discovered→planning→fetching→partial\|complete\|error) · `plan_ready` · `artifacts_expected/done/failed` · `tree_recheck_after` (empty vbpl tree) · `content_recheck_after/count/reason` (placeholder source content) · `in_scope` (relation targets) · `content_hash` · `detail_url` (source landing page → MCP `source_url`) |
| `doc_discovery` | Append-only provenance | every (doc, keyword) hit **and** relation edge that surfaced it — dedup the doc, never lose *why* it's in scope (`''` sentinels for nullable key parts) |
| `fetch_artifact` | Child: one row per fetchable unit | `kind` ∈ body/tree/file/relation/appendix · `ref_key` (discriminates 2 PDFs / 2 edges) · `file_name` (source filename; local storage can be hash-named) · `url`+`url_expires_at`+`gateway_url` (re-resolve expiring g7 tokens / 24h presigned URLs) · `target_source/target_ext_id` (enqueue relation targets) · `lease_owner/lease_expires_at` (crash-safe `FOR UPDATE SKIP LOCKED`) · `max_attempts` (dead-letter) · `is_optional`/`skipped` |

Built-in safety nets: per-keyword cursor (a slow keyword can't be skipped), `expected_total` drift
check, empty-tree re-check, expiry re-resolve, relation-target enqueue with cycle bounding, lease
recovery, source-content recheck, dead-letter → doc `partial`, re-discovery re-opens a doc **only** on a
real `content_hash` change.

## `bronze` — raw capture (one doc → many files, done right)

| Table | Role | Notes |
|-------|------|-------|
| `source_document` | One row per source observation | `doc_guid` (vbpl UUID / source id), `doc_number_norm` + `issuer_code` + `doc_type_code` (resilient congbao↔vbpl dedup), `detail_url` (official source landing page; exposed as MCP `source_url`), `expire_at`, `gazette_number/date`, `has_content` (OCR routing), `is_consolidated` (VBHN), `content_hash`, `raw_meta` JSONB |
| `raw_payload` | Inline bodies/JSON | `kind` ∈ content_html / provision_tree_json / references_json / detail_json · **UNIQUE (source_document_id, kind)** (idempotent re-fetch) |
| `raw_file` | Downloaded files | `file_kind` (role: main/appendix/attachment/version_snapshot/original_scan) **×** `file_format` (pdf/docx/doc/html) **×** `is_authoritative` (official congbao/vbpl files = true) · `ordinal`, `label` (source filename), `storage_path`/`sha256` (hash-addressed local file) · **UNIQUE (source_document_id, file_kind, ordinal, file_format)** (fixes a real re-fetch dup bug) |

## `silver` — normalized legal documents + the reference graph

| Table | Role | Notes |
|-------|------|-------|
| `document` | One logical doc (deduped across sources) | `doc_key` = **`<TYPE>\|<NUMBER>`** (normalized loại văn bản + số ký hiệu — the type discriminates documents sharing a số, e.g. Luật vs Nghị quyết 51/2005/QH11; number-only when the type is missing; `source:external_id` when the number is missing or the VBPL "KHÔNG SỐ" sentinel) · `index_class` (`primary` = searchable corpus; `relation_context` = relation-pulled, out of scope — text/relations served, no chunks) · `is_consolidated` · denormalized display `markdown` |
| **`doc_ref`** | **Referenceable identity (incl. out-of-corpus stubs)** | `ref_key` UNIQUE · source-structured targets use `source:external_id` (e.g. VBPL ID); weak text refs fall back to normalized `số hiệu` · `document_id` business key (NULL = stub not yet ingested). Relations/amendments/validity target this; it resolves automatically when the target is later ingested |
| `document_relation` | Confirmed edges | `from_document_id` → `to_ref_id` (`doc_ref`) · `relation_type` + `relation_type_raw` (VBPL int, so re-map is a pure recompute) · only VBPL structured rows are promoted today |
| `relation_evidence` | Evidence behind edges | `structured_relation`, future `model_classification`, or `weak_relation`; exact `số hiệu văn bản` target + raw operator/snippet/citation/source authority/confidence; only promoted rows create confirmed graph/validity effects |
| `amendment_event` | First-class amendment events | `acting_document_id` → `target_ref_id` (`doc_ref`) · versioning-ready |
| `validity_period` | Bitemporal validity | `eff_from/eff_to` + `observed_at/superseded_at` · `status_code` (CHL/HHL/HHL1P/…; empty when the source gave none) + `status_class` (in_force/expired/partial/not_yet/suspended/**unknown** — a source that says nothing never defaults to in_force) · `caused_by_ref_id` · nullable `section_id`/`version_id` for later clause/version granularity |
| `document_text` | Per-text binding authority | `authority` (human_verified > gazette_borndigital > transcription_html > ocr_*) · `is_binding` · `source_file_sha256` + `verbatim_sha256` (congbao↔vbpl reconcile) · `needs_review`. Retrieval restricts binding-text evidence to `is_binding` |
| `document_section` | Provision tree | `node_key` (vbpl UUID, idempotent re-parse) · `ptype` + `kind` (phan/chuong/muc/dieu/khoan/diem) · `citation_path` UNIQUE (the chunk citation key) |
| `document_topic` | Tags | `topic` (lĩnh vực vocab) · `topic_source` (linhvuc_source/classifier/keyword_match) · `matched_keyword` · `confidence` |
| `document_gazette` | Doc ↔ gazette issues (many) | a doc can appear in multiple công báo issues (incl. corrigenda) |
| `document_alias` | Source-observation → logical doc | `match_method` (docguid/sokyhieu_issuer_date/manual) + `confidence` (auditable, reversible merges) |

`document_version` / `amendment_event` / `validity_period` form a deliberately FK-free triangle
(business keys), resolved at the app layer.

## `gold` — chunks + embeddings

| Table | Role | Notes |
|-------|------|-------|
| `chunk` | Article-level chunk (one Điều → one chunk) | `citation_path` (the cite key) · `contextual_prefix` · `text_markdown` |
| `chunk_embedding` | pgvector embedding | BGE-M3; one row per chunk; HNSW index |
| `document_summary` | Doc-level summary | schema placeholder; deferred |

## `config` — tunable policy (seed + operator overrides)

Tunable policy — **what's in scope, which issuers count, what we search for** — lives in the `config`
schema, **not** in Go. Defaults ship as CSVs and load into the database; operators override them without
forking. This is banhmi's "no hardcoded lists" rule. Schema lives in `sql/config/schema.sql`.

| Table | Drives | Key columns | Unique key |
|-------|--------|-------------|------------|
| `scope_term` | scope matcher vocabulary | `term`, `term_class` (`strong`/`strong_title`/`weak`/`signal`), `theme` | `(term_class, term)` |
| `issuer_code` | per-source issuer filter + SBV agency ids | `source`, `code`, `in_scope`, `is_sbv` | `(source, code)` |
| `discovery_keyword` | keyword-search discovery queries | `term`, `source` | `(source, term)` |

See [SOURCES.md](SOURCES.md) for how the matcher uses terms and how discovery keywords + issuer codes
drive each source.

### Seed vs. user — the `origin` column

Every row is `origin='seed'` (shipped default) or `origin='user'` (operator's own). The split is what
makes re-seeding safe:

- **Re-seed replaces defaults:** `cmd/seed` deletes all `origin='seed'` rows, then re-inserts from the CSV.
- **It never touches `origin='user'` rows:** inserts use `ON CONFLICT DO NOTHING`, so a user row on the
  same unique key wins — the default is skipped, the customization survives.
- Every row also has `enabled` (default true); set it `false` to mute a term without deleting it.

### Seeding

```bash
go run ./cmd/migrate up   # create the config schema (once)
go run ./cmd/seed         # load defaults from the embedded deploy/seed/*.csv
```

`cmd/seed` embeds `deploy/seed/*.csv`, so the binary needs no external files, and it is **re-runnable** —
run it again any time the CSVs change.

### Customizing

- **Change a default for everyone:** edit the CSV in `deploy/seed/` and re-seed. Keep terms
  NFC-normalized and lowercase; never diacritic-fold.
- **Add something only your deployment needs:** insert a row with `origin='user'` (use `source=''` for all
  keyword-search sources, or `source='vbpl'` for one source). It survives every future re-seed.
- **Drop a shipped default locally:** set `enabled=false` on that `origin='seed'` row, or add a colliding
  `origin='user'` row — don't delete it, or the next re-seed brings it back.

### Maintaining the seed CSVs

The CSVs are the source of truth for defaults; grow them with research sub-agents (see the seeding rule
in [CLAUDE.md](../../CLAUDE.md#data-and-sources)). When adding rows: dedup on the unique key, stay NFC +
lowercase, and prefer discriminating keyword phrases — vbpl matches on `all_words`, so a tighter phrase
trades recall for precision.

## Key decisions

1. **Discovery ledger = parent + child** (`fetch_doc` + `fetch_artifact`); completeness is a
   compare-and-set recomputed from child rows, not a flag.
2. **`doc_ref` stub** for the reference graph — source IDs win over `số hiệu` when the source gives a
   target ID, so different VBPL entities with the same number do not collapse. Stubs resolve later
   with no row rewrites; `silver.document` still means "a doc we actually hold."
3. **`document_text`** holds per-(authority, source) binding provenance + the congbao↔vbpl reconcile.
4. **`raw_file` role × format × authority** + a natural-key UNIQUE — one-doc-many-files, idempotent.
5. **Keep raw source codes** (`relation_type_raw`, `ptype`, `*_code`) so re-mapping is a pure recompute.
6. **`status_class`** so the in-force filter is a class test; `partial` (HHL1P/TNHL1P) is **never** a
   hard exclusion (don't hide still-valid articles).
7. **Config is data, not code** — `origin='seed'`/`'user'` makes defaults re-seedable without clobbering
   operator overrides.

## MVP-shallow vs forward-compatible

- **Populated in MVP1:** the full `ingest` ledger; `bronze`; `silver.document` + `document_section`
  (from vbpl tree) + `relation_evidence` + `document_relation`/`amendment_event` (via `doc_ref`) + `document_topic` +
  `document_text` + **document-level** `validity_period` + `document_gazette` + `document_alias` +
  `gold.chunk` + `gold.chunk_embedding`; the `config` vocabularies.
- **Schema-ready, deferred:** clause-level validity/section-level relations, multi-row
  `document_version`, VBHN consolidated-text reconstruction, the relation-target second-wave fetch.

See [`SOURCES.md`](SOURCES.md) for per-source discovery/filter/download and the scope matcher,
[`PIPELINE.md`](PIPELINE.md) for the workflows that populate this model, and
[`ARCHITECTURE.md`](../ARCHITECTURE.md) for the system overview.
