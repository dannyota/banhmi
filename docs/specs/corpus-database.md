Proposal, not implemented. One decision is approved: the owner approved building the new Vietnam corpus schema on fresh migration chains and freezing the legacy chains in [product boundaries](../../AGENTS.md#product-and-evidence-boundaries). Every other contract here remains a proposal.

# Corpus database

Keep one writer database per country. Publish each serving release into a separate, immutable database. Keep a separate serving control database. Serve Vietnam from VNGCloud. Keep account data outside every corpus snapshot. Build all three database kinds on fresh Goose chains that never share a table with the legacy schema.

## Goal and limits

Support more subjects without copying laws into subject databases. Preserve country isolation and exact citations through rebuilds and legal changes. A release identifies all evidence used by a request.

The owner has settled the product as Tra cứu quy định at `tracuuquydinh.vn`. Vietnam questions are Vietnamese with diacritics or a document number; the serving layer validates the question before any embedding or corpus query.
Questions stay in Vietnam and use only the self-hosted query embedder. English is interface text only. No answer model, generative model, or reranker runs on our servers. Retrieval uses dense plus lexical fusion. Public source
documents may use cloud batch processing. The five existing non-Vietnam services remain unchanged on AWS. Keep exactly five MCP tools: `guide`, `corpus_status`, `quality_gaps`, `search`, and `document`.

This spec owns database storage, chains, generated stores, and reads. The sibling specs own processing, model selection, document-part semantics, review, and infrastructure. The owner's VNGCloud decision is in the [write-path hosting decision](corpus-write-path.md#decisions). Owner decisions O1-O6 in the [Vietnam plan](../plans/vietnam.md) are settled and not reopened here.

## Current state and evidence

These are source facts, not a deployment audit. Legacy means the v1 corpus code and schema named in [product boundaries](../../AGENTS.md#product-and-evidence-boundaries).

| Area | Fact and evidence |
|---|---|
| Legacy deployment | Six databases on one PostgreSQL 17 instance, one per country, each with the schemas `ingest`, `bronze`, `silver`, `gold`, and `config`. Country is a database boundary, not a column ([data architecture](../ARCHITECTURE.md#data-architecture)). The registry selects a database name and serving supports a country-specific override ([jurisdiction.go:53](../../pkg/base/jurisdiction/jurisdiction.go#L53), [hosted.go:131](../../internal/app/serve/hosted.go#L131)). |
| Legacy chains | Five Goose chains under `deploy/migrations/` plus `extensions`, applied by `cmd/migrate` with `public.goose_db_version` and five `public.goose_db_version_<schema>` tables ([main.go:39](../../cmd/migrate/main.go#L39)). Queries live under `sql/{bronze,silver,gold,ingest,config}` and generated stores under `pkg/store/` ([sqlc.yaml:37](../../sqlc.yaml#L37)). All of it is frozen and serves the five maintained countries and the current Vietnam service. |
| Undeployed redesign files | `bronze/00002`, `silver/00009`, `gold/00003`, `ingest/00006`, and `config/00008` `_corpus_redesign.sql` were appended during the [Goose transition](goose-migrations.md#migration-and-generation-contract). No deployed database contains them. They stay in place, are never deployed, and retire with the legacy chains. They are reference material for the new chains, not their basis. `internal/corpus/schematest` tests them and stays with legacy. |
| Auth | The authenticated composition opens a separate pool. `cmd/auth-migrate` applies the append-only chain under `deploy/authmigrations/` with the `goose_db_version_auth` table and a URL from one environment variable ([main.go:19](../../cmd/auth-migrate/main.go#L19), [main.go:47](../../cmd/auth-migrate/main.go#L47)). The new chains follow this command shape. |
| Retained Go contracts | `internal/corpus/run` (fences, atomic apply), `internal/corpus/blob` (object identity), and `internal/corpus/release` (manifest, pins, retention) are retained ports without a persistent adapter ([resolver.go:60](../../internal/corpus/release/resolver.go#L60), [types.go:28](../../internal/corpus/release/types.go#L28)). The new stores implement them. |
| Legacy retrieval | Vietnam selects exact dense scanning ([jurisdiction.go:178](../../pkg/base/jurisdiction/jurisdiction.go#L178)). The approximate path overfetches before outer filters and falls back on shortfall ([retrieve.go:901](../../pkg/rag/retrieve/retrieve.go#L901)). BM25 ranks `sparsevec(1048576)` inner products with no sparse approximate index ([retrieve.go:1280](../../pkg/rag/retrieve/retrieve.go#L1280)). Dense storage is `vector(1024)` with cosine HNSW. |
| Legacy validity limits | Document-level validity, reserved amendment events, and the provision repeal and relation-attribution defects motivate the new evidence model ([schema.md:56](../design/schema.md#L56), [schema.md:101](../design/schema.md#L101), [Phase 3](../design/website.md#phase-3-depends-on-the-corpus-redesign)). |
| Physical size | The owner reports 11.3 GB used of 20 GB across six country corpora and another service on an RDS `db.t4g.micro`; see the [hosting measurement record](vngcloud-hosting.md#workload-and-sizing). This is not a Vietnam release-snapshot measurement. |

Counts below are observations recorded in the [current state](../../PLAN.md#current-state). The repository calls the 142,086 Vietnam "provisions" chunks. Use chunks for sizing; do not equate them with distinct provisions.

| Country | Database | Documents | Indexed documents | Chunks |
|---|---|---:|---:|---:|
| Vietnam | `banhmi` | 3,974 | 2,853 | 142,086 |
| Malaysia | `laksa` | 109 | 109 | 11,304 |
| Indonesia | `rendang` | 2,371 | 2,340 | 160,142 |
| Singapore | `kaya` | 292 | 292 | 27,951 |
| Thailand | `tomyum` | 1,790 | 1,788 | 32,715 |
| Cambodia | `amok` | 284 | 244 | 7,757 |
| Total, calculated | Six databases | 8,820 | 7,626 | 381,955 |

## Topology decision

Choose country writers plus country-release serving databases. Preserve the approved country boundary ([source boundaries](../plans/corpus-redesign-source.md#boundaries)). Database names are private handles; country-qualified keys are
public identities.

| Option | Isolation, migrations, backups, cost, and adding a country | Decision |
|---|---|---|
| One mutable database per country for writing and serving | Country restore and migration are independent. Few copies, but builds compete with reads and readers can see mixed stages. | The legacy deployment. Frozen, not extended. |
| One database with a country key | One migration chain and shared capacity. Every key, join, authorization rule, and index needs country scoping. Restore and failure affect all countries. | Reject. |
| Shared writer, country-release serving databases | Isolates serving, but write migrations, backups, and mistakes still cross countries. | Reject. |
| One writer per country, separate database per serving release, separate serving control database | Independent country migrations and backups. Snapshot storage multiplies; migration and restore automation repeats per country. Adding a country provisions a writer and release bundle without changing another country's tables. | Recommend. |

Vietnam corpus generation uses one VNGCloud writer database in Vietnam, orchestrated by Temporal Cloud. The country writer alone authors revisions and desired selection. The serving control database holds imported manifests, applied selection, pins, and local retention claims; it cannot author promotions. Serving requests need no writer connection. Databases sharing an instance share CPU, memory, disk failure, and physical recovery, so writer and worker capacity stay separate from the serving host. Use distinct roles and revoke public connection grants. Never route Vietnam requests to an AWS database as a fallback. The serving PostgreSQL listener remains socket-only. The writer may use private TLS only after network, certificate, role, revocation, and fail-closed connectivity controls pass implementation gates.

A serving release contains only the selected evidence closure, policy, identity mappings, artifact handles, and frozen manifest. Exclude job queues, embedding caches, mutable activation records, account state, quarantine actors, and unused historical parser outputs. The closure includes known historical legal text needed for date queries. Serving uses SELECT-only corpus roles; its control role acquires and closes pins without storing question text.

Keep one primary-key column per table and natural-key UNIQUE constraints. Use no foreign keys across schemas; validate cross-schema business keys through publication anti-joins ([engineering rules](../../instructions/engineering.md#data-and-generated-code)).

## Migration chains and generated stores

**Decision:** Use three chains, one per database kind: writer, release, and control. Each chain owns every schema in its database and has one Goose version table.
**Why:** The three databases have different tables, roles, vector types, and indexes. A writer never holds a frozen manifest copy; a release never holds a job queue; control never holds evidence. One chain per database makes the migrator, the replay test, and the populated-upgrade check one unit per database and lets a slice edit one file set.
**Rejected:** One chain per schema as legacy did (five version tables per database with hand-ordered cross-schema dependencies and nothing to separate); one shared chain for writer and release (the release needs fixed `vector(D)` and HNSW, the writer needs recipe-checked vectors and no serving index, and empty work tables would still receive serving grants); appending to the legacy chains.
**Detail:** Writer schemas are `ingest`, `bronze`, `silver`, `gold`, and `config`. Release schemas are `silver`, `gold`, `config`, and `release`. The control schema is `control`. Schema names repeat the legacy names so sibling specs keep their table references; no table definition is shared with legacy. Each first migration runs `CREATE SCHEMA` without `IF NOT EXISTS`, so applying a chain to a database that already holds a legacy or other-kind schema fails. Extensions use `CREATE EXTENSION IF NOT EXISTS` so an operator may pre-create them when the migration role lacks the privilege.

**Decision:** Put the chains under `deploy/corpusmigrations/{writer,release,control}/` with version tables `public.goose_db_version_corpus_writer`, `public.goose_db_version_corpus_release`, and `public.goose_db_version_corpus_control`.
**Why:** The directory sits beside `deploy/authmigrations/` and outside the frozen `deploy/migrations/` tree, so the frozen-chain check stays a plain path rule. Goose creates its version table before the first migration runs, so a chain-owned schema cannot host it; `public` with distinct names matches the legacy and auth convention and keeps a wrong-database run visible.
**Rejected:** New directories under `deploy/migrations/`; schema-qualified version tables; timestamp file names (the chains are edited in place by one persistence lane, so five-digit sequence numbers read in dependency order).
**Detail:** `deploy/corpusmigrations/embed.go` embeds `writer/*.sql`, `release/*.sql`, and `control/*.sql`. File names are `NNNNN_<name>.sql`. Each chain's first file creates extensions, schemas, and NOLOGIN capability roles with an existence check; login roles are bound by a bootstrap file outside the embed glob, as `deploy/authmigrations/bootstrap/` does. Down sections are empty for immutable evidence.

| Writer chain file | Contents | First consumer |
|---|---|---|
| `00001_base.sql` | Extensions `vector` and `btree_gist`, five schemas, immutable-row trigger functions, roles `banhmi_corpus_writer_runtime` and `banhmi_corpus_writer_export` | M1 |
| `00002_admission_and_ledger.sql` | `ingest.country_admission`, `execution`, `execution_lease`, `execution_result`, `batch_request`, `batch_execution`, `delivery_record`, `revision_acceptance`, `revision_acceptance_state`, `quarantine_decision` | M1 |
| `00003_sources.sql` | `bronze.source_record`, `observation_revision`, `artifact`, `observation_artifact`, `blob`, `blob_retention_reference` | M1 |
| `00004_identity_and_revisions.sql` | `silver.document_identity`, `identity_assertion`, `legacy_identity_mapping`, `document_revision`, `provision_revision` | M1 |
| `00005_conversion_provenance.sql` | `silver.conversion_artifact`, `offset_map_segment`, `edit_ledger`, `quality_report`, `quality_finding`, `revision_comparison` | M1 |
| `00006_parts.sql` | `silver.part_revision`, `evidence_target`, `part_block`, `part_table`, `table_row`, `table_cell`, `table_rendering`, `rendering_cell_span`, `part_artifact_span` | M2 |
| `00007_relations_and_validity.sql` | `silver.relation_assertion`, `amendment_target`, `amendment_difference`, `validity_resolution`, `evidence_validity_interval`, `evidence_text_period` | M2 |
| `00008_subjects.sql` | `config.taxonomy_release`, `subject_taxon`, `subject_closure`, `taxonomy_crosswalk`; `silver.subject_assessment_set`, `document_subject_assignment`, `evidence_subject_assignment` | M2 |
| `00009_chunks_and_vectors.sql` | `gold.chunk_set`, `corpus_chunk`, `chunk_vector`, `lexical_revision`, `chunk_lexical_vector`, `lexical_statistics`, `chunk_subject` | M3 |
| `00010_releases.sql` | `ingest.corpus_build`, `build_revision`, `build_execution`, `corpus_release`, `release_manifest`, `desired_selection`, `release_activation`, `release_retention_hold`, `release_retention_claim`, `catalog_recovery_record` | M3 |

| Release chain file | Contents | First consumer |
|---|---|---|
| `00001_base.sql` | Extensions, schemas `silver`, `gold`, `config`, `release`, role `banhmi_corpus_release_reader` | M3 |
| `00002_evidence.sql` | Fixed-shape copies of the selected `silver` and `config` evidence tables, `gold.corpus_chunk`, `chunk_subject`, `chunk_lexical_vector`, `lexical_statistics`, `gold.coverage_summary`, `release.manifest`, `release.membership_digest`, `release.artifact` | M3 |
| `00003_vectors.sql` | `gold.chunk_vector_f32_1024` with `vector(1024)`; one table per approved dimension and precision pair, appended when a recipe is approved | M3 |
| `00004_indexes.sql` | HNSW and GiST indexes. The candidate build migrates `-to 3`, bulk-loads, then migrates to the end so indexes build after load | M3 |

| Control chain file | Contents | First consumer |
|---|---|---|
| `00001_base.sql` | Schema `control`, role `banhmi_corpus_control_runtime` | M4 |
| `00002_selection.sql` | `control.release_catalog`, `applied_selection`, `release_pin`, `retention_claim`, `recovery_record`, `country_admission_view` | M4 |

**Decision:** Add `cmd/corpus-migrate`, a thin command that applies one chain per invocation.
**Why:** `cmd/migrate` is frozen legacy and loads the legacy configuration file. The new command has no legacy import and one job.
**Rejected:** Extending `cmd/migrate`; a config-file database selector; one command that applies all three chains to one database.
**Detail:** The database URL comes from `BANHMI_CORPUS_MIGRATION_DATABASE_URL`. Flags are `-chain writer|release|control` (required), `-to <version>` (apply up to and including that version; default all), and `-status` (print applied versions and exit without changes). The command sets the postgres dialect, the chain's embedded sub-filesystem, and the chain's version table, then calls Goose `UpToContext`. Before applying, it lists `public` tables named `goose_db_version%` and fails when any table other than the selected chain's exists, so a legacy or other-kind database is refused. Errors return; the command never deletes or resets a version table.

**Decision:** Generate stores with sqlc entries that read a chain directory as schema input, with query directories and generated output beside the code that uses them under `internal/`.
**Why:** The engineering rule puts new queries and stores under `internal/`. Several entries can read one chain so each store package holds only its consumer's queries and models.
**Rejected:** New packages under `pkg/store/` or `sql/`; one generated package per chain (M1 would import release and vector models it cannot use); generated code in a shared `gen` package.
**Detail:** Each entry uses `engine: postgresql`, `sql_package: pgx/v5`, `emit_interface: true`, and the legacy type overrides for `jsonb`, `timestamptz`, nullable scalars, `vector`, and `sparsevec` as string. Handwritten adapter and transaction code shares the generated package. The hosted generation job regenerates every entry and fails on drift in generated files. Add an entry with its first query, in the order below.

| sqlc entry | Schema input | Queries | Package and output |
|---|---|---|---|
| Writer intake | `deploy/corpusmigrations/writer` | `internal/corpus/store/intake/queries` | `intake` at `internal/corpus/store/intake` |
| Writer document | `deploy/corpusmigrations/writer` | `internal/corpus/store/document/queries` | `document` at `internal/corpus/store/document` |
| Writer index | `deploy/corpusmigrations/writer` | `internal/corpus/store/index/queries` | `index` at `internal/corpus/store/index` |
| Writer catalog | `deploy/corpusmigrations/writer` | `internal/corpus/store/catalog/queries` | `catalog` at `internal/corpus/store/catalog` |
| Release projection | `deploy/corpusmigrations/release` | `internal/corpus/store/projection/queries` | `projection` at `internal/corpus/store/projection` |
| Release reader | `deploy/corpusmigrations/release` | `internal/evidence/store/reader/queries` | `reader` at `internal/evidence/store/reader` |
| Control | `deploy/corpusmigrations/control` | `internal/evidence/store/control/queries` | `control` at `internal/evidence/store/control` |

`intake` holds `ingest` ledger and `bronze` capture queries; `document` holds `silver` and `config` evidence queries; `index` holds `gold` chunk and vector queries; `catalog` holds build, release, manifest, activation, retention, and recovery queries; `projection` loads, seals, and verifies a candidate release database. Serving imports only `reader` and `control`. Writer code imports `intake`, `document`, `index`, `catalog`, and `projection`. Dense and sparse ranking statements stay raw pgx in `pkg/rag/retrieve` over the `reader` models, as the legacy retrieval does. Vector columns in writer tables carry a recipe identity and a dimension check; only the release chain declares fixed `vector(D)`.

## Identity, revisions, and release membership

Mint universally unique identifiers (UUIDs) once in `silver.document_identity`. Use `vn:doc:<uuid>`. Keep country and entity kind, but exclude domain, brand, title, source number, and local sequence. Retain identity assertions and the catalog through every writer rebuild and backup restore. Reject keys whose country differs from the writer's singleton country metadata in `ingest.country_admission`.

Use `vn:doc:<uuid>:rev:<digest>` for an immutable document revision. Compute the digest from canonical source-observation hashes, text and metadata hashes, and conversion, normalization, parser, and policy recipes. Exclude local IDs and processing timestamps. Identical inputs reuse revisions; metadata changes create distinct revisions. `silver.document_revision` has UNIQUE `(document_identity_id, revision_key)` with metadata identity inside the key, `intake_execution_id NOT NULL` as origin provenance, and no build column. Immutable-row triggers reject UPDATE and DELETE.

`ingest.build_revision` records membership with UNIQUE `(build_id, document_revision_id)` so builds reuse immutable revisions. Publication validates selected acceptance receipts and membership, not origin-build equality. Export stable keys; local IDs may change.

A provision key contains the document revision digest and a revision-local provision key. Preserve native citation bytes and source anchors separately. A stable provision UUID is optional and needs an approved continuity assertion. Never infer continuity from matching text or renumbering. Chunk IDs remain local to a chunk set. Citations carry evidence key, revision key, release ID, native citation, and source hash. The write path computes amendment differences; the database stores them. The [website design](../design/website.md#phase-3-depends-on-the-corpus-redesign) owns redline display.

Legacy evidence enters only through an import contract, never through a shared table. `silver.legacy_identity_mapping` maps `(country, export_manifest_digest, entity_kind, legacy_id)` to a candidate key and a document identity with state `resolved`, `ambiguous`, or `unavailable`. Its input is a hash-pinned identity export manifest listing legacy document ID, source, external ID, normalized document number, and title hash, produced by a read-only hosted export from the legacy `banhmi` database that owner question 4 approves. The import runs as an intake execution whose receipt records the manifest digest. Only document identities are mapped; legacy section and chunk IDs are not. A bare legacy ID resolves only when all retained mappings agree. The frozen Vietnam goldens in `deploy/eval/golden.json` cite document numbers and articles, so evaluation resolves them through identity and native citations and needs no ID mapping. Merges and splits append identity assertions and keep old keys resolvable.

## Intake ledger and revision acceptance

`DocumentIngest`, `OcrBatch`, and `Reprocess` record work in an intake ledger with no build ID. Source and embedding work use the same execution contract. `CorpusBuild` selects build-eligible revisions into its manifest. Reject fake builds for intake and a second acceptance ledger in Temporal.

| Record | Identity, provenance, and constraints |
|---|---|
| `ingest.country_admission` | Singleton country, monotonic admission epoch, admitted writer, and enabled state. Every mutation checks the current epoch; rotate it on writer replacement or recovery. Never let a restored epoch reauthorize an old worker. Legacy tools hold no role in this database. |
| `ingest.execution` | Surrogate execution ID; UNIQUE `(workflow_id, attempt_fence)`; workflow kind, immutable processing-attempt key, country admission epoch, document version or batch collection key, input, recipe, and budget digests, predecessor, and run receipts. No build ID. Temporal Run IDs are receipts, not identity. `ingest.build_execution` links only `CorpusBuild` work to its build. |
| `ingest.execution_lease` | UNIQUE resource kind and key: source, document attempt, batch, build, or country control. Owner, monotonic fence, expiry, renewal, cancellation, and admission epoch. Claim and reclaim allocate the execution's `attempt_fence`; a build lease serializes assembly, not intake. Index expiry and next eligible work. |
| `ingest.execution_result` | Append-only receipt with execution ID, stage and substep, shard, immutable idempotency key, input and output hashes, recipe and budget, outcome and counts, cursor, and retry eligibility. UNIQUE idempotency key excludes mutable owner and fence. Changed inputs or recipes require a new attempt; identical replays return the committed receipt. |
| `ingest.batch_request`, `ingest.batch_execution` | Durable requests link waiters to one country, provider, recipe, and collection; the batch links to an execution, never a build. Freeze membership and per-item inputs. Persist submission intent and key, provider receipt, uncertainty, polling state, accepted item receipts, and waiter links. UNIQUE provider submission key and batch item; index pending requests and next poll. |
| `ingest.delivery_record` | Launch intents, decision delivery, and completion notifications with immutable deduplication key, recipient workflow ID, referenced receipt, expected epoch and hash, due time, and acknowledgement. Commit with source cursors or accepted results; index unacknowledged and due records. Do not target a Temporal Run ID. |
| Observation and revision provenance | `bronze.observation_revision` links source window, document version, artifact hashes, and accepting execution and result. `document_revision.intake_execution_id` identifies its origin; child receipt links preserve later reuse attempts without rewriting the revision. Never invent an intake execution for an unknown historical attempt. |

Stage content-addressed blobs before acceptance. One transaction validates country admission epoch, execution, live lease owner and fence, cancellation, input, recipe, and budget hashes, and decision revision; then commits output references, acceptance state, result, and delivery records. Stale workers write nothing. A changed payload under an existing idempotency key fails. A crash after commit is recovered from the receipt and pending delivery records; signals only wake readers. Uncommitted objects remain staging objects. Bounded reconciliation resumes launches and notifications idempotently.

Keep append-only `ingest.revision_acceptance` events and the `ingest.revision_acceptance_state` projection keyed by revision and processing attempt, indexed by state and watermark. States are `candidate`, `quarantined`, `accepted`, `accepted_with_gaps`, and `excluded`. A revision is build-eligible only in `accepted` or `accepted_with_gaps`. Each transition binds execution, candidate digest, quality report hash, decision revision, and scoped gaps. OCR authority remains non-binding. A reprocessed revision is a candidate until its before-and-after comparison in `silver.revision_comparison` passes and, when critical text changes, the owner adjudicates. Critical changes include legal numbers, dates, negations, citations, authority flags, and part boundaries. `ApplyComparison` records the comparison before `ApplyEvidence` atomically validates prior revision, candidate and report hashes, comparison, and required adjudication with acceptance. Reusing blobs or a revision never downgrades a previously accepted attempt or skips the new attempt's gates.

`CorpusBuild` freezes acceptance receipts and the source-coverage watermark in one consistent snapshot. Base membership, replacements, additions, and tombstones remain separate from processing provenance. Pending, quarantined, and excluded items stay in coverage accounting; late acceptance needs a new build.

## Conversion provenance and decisions

Link every provenance record to its document revision, document version, processing execution, and candidate digest. Keep content immutable; append new reports and decisions. The database stores conversion's contract and validates references; it does not choose converters or quality thresholds.

| Storage | Contract |
|---|---|
| `bronze.artifact`, `bronze.blob`, `silver.conversion_artifact` | Retain original bytes, raw and normalized `ConvertedDocument` block artifacts, exact raw and normalized UTF-8 text, recipes, and output manifests by hash and byte size. Indexed revision and artifact-role links select the raw and normalized outputs without loading whole blobs. |
| `silver.offset_map_segment` | Indexed segments name source and destination artifact hashes, block IDs, coordinate spaces, and half-open `[start,end)` byte ranges; text ranges count UTF-8 bytes. Spaces are original bytes, decoded package member, raw converter text, and normalized text. Preserve many-to-many joins, splits, compositions, transform order, and page, object, and member anchors. Store the complete map as a hashed blob; index revision and block and source and destination ranges. |
| `silver.edit_ledger` | Immutable hashed blob of ordered operation, rule and version, input and output hashes and ranges, removed bytes, replacements, encoding and font evidence, and source anchors. Indexed revision and input and output hashes bind the ledger to its exact candidate. Reversal must reproduce raw converter text byte-for-byte. |
| `silver.quality_report`, `silver.quality_finding` | Immutable per-artifact reports and document roll-up blobs. Indexed rows retain revision, candidate and report hash, policy and threshold version, outcome, metric numerators, denominators, and thresholds, and child reason, scope, and gap rows. Preserve `accepted`, `accepted_with_gaps`, and `quarantined`; `needs_ocr` requests conversion, not acceptance. |
| `ingest.quarantine_decision` | Append-only: decision ID, document version ID, quality report hash, `workflow_fence_token` bound to the execution's `attempt_fence`, actor, action, reason, revision and candidate digest, original and output hashes, expected decision epoch, scope and evidence, expiry, and replacement recipe or source. Actor is an opaque reviewer key linked to the Vietnam operator audit record, outside Temporal and never exported to a release. |

Validate map bounds against each hashed artifact. Decoded-member offsets are not ZIP offsets; derivative offsets are not original offsets. Unknown original-byte mappings remain null with `BYTE_MAPPING_UNAVAILABLE`, plus usable page, object, and member anchors. Never invent scan offsets. Decision acceptance checks actor authority, current fence and decision epoch, candidate and report hashes, and expiry in the fenced transaction. Duplicate delivery returns the prior receipt; stale decisions cannot accept a new candidate. Public provenance exports decision references and scoped reasons, not operator identity. The [review API comes first](corpus-review.md#decisions), behind mutual TLS with a private CA.

Authority is the owner or reviewers the owner designates, governed by write-path owner question 3, ["Who may accept source gaps and promote a release?"](corpus-write-path.md#risks-and-owner-questions). Actions are: retry with another converter; accept as non-binding OCR; accept with named gaps (state `accepted_with_gaps`); mark as a coverage gap and exclude; attach an alternate official source. Retries and alternate sources create linked attempts. Named-gap acceptance reruns mandatory gates on exact retained and excluded ranges and excludes unsafe text from both indexes. No action bypasses integrity gates, reprocess comparison, or owner adjudication, or makes OCR binding.

Retain originals, maps, edit ledgers, reports, comparisons, recipes, and decision references with every referencing release, pin, hold, or live intake attempt. `bronze.blob_retention_reference` edges prevent fenced garbage collection from deleting that closure. Do not export mutable intake queues or private audit rows.

## Subjects

Use one immutable `config.taxonomy_release` per country taxonomy version. Keep labels and parent edges in `config.subject_taxon` inside that version. Validate acyclicity and compute its digest before publication. `config.subject_closure` holds the ancestor and descendant closure for filter expansion. A rename, split, merge, or hierarchy change creates a new version; `config.taxonomy_crosswalk` relates versions without changing old releases. Reject unknown version and key pairs instead of mapping them to the latest vocabulary.

`silver.document_subject_assignment` and `silver.evidence_subject_assignment` (keyed by `evidence_target_id`, so provisions and parts share one shape) require taxonomy version, subject, assignment method and recipe, evidence reference and hash, nullable confidence, review state, and context role. Methods initially mean official classification, deterministic rule, or human review. Confidence describes that method's support, not legal truth. Corrections append a new `silver.subject_assessment_set`; include its ID in assignment uniqueness and pin it in the release. Never mutate an earlier assignment.

Publish `gold.chunk_subject` child rows with B-trees on `(taxonomy_release_id, subject_key, chunk_id)` and `(chunk_id, taxonomy_release_id, subject_key)`. A provision assignment takes precedence over inherited document classification. Broad-document assignments provide candidate scope, not proof that every clause belongs to the subject. Record inheritance and unresolved scope explicitly.

Maintain assessment rows even when no positive assignment exists. States are assigned, unknown, unreviewed, and reviewed not-applicable. An absent row means unknown. Report unknown and unreviewed counts, taxonomy version, and denominators through `corpus_status` and `quality_gaps`. Never report missing tags as zero coverage.

The default subject search retrieves two labelled sets: approved primary matches and possible matches whose classification is unknown or unreviewed. Apply the same eligibility predicate to both dense and lexical arms before their candidate LIMIT. Fuse within each set using the embedding design's contract. Return possible matches separately with a coverage gap. An explicit strict subject filter excludes possible matches and reports that recall is limited by classification coverage. Multiple selected subjects use union semantics, including descendants.

Fetch amendment, repeal, and validity context after primary selection regardless of its subject. Label context roles. Reject vector copies and partitions per subject because laws have multiple tags. Subject filtering must not add hidden recall loss through fixed unfiltered top-k or missing labels. Measure approximate retrieval against exact eligible-set retrieval.

## Parts, validity, and relations

Silver records are immutable with surrogate ID, stable key, intake or build execution and recipe provenance, source observation, evidence hash, and review state. The write path owns execution and legal-effect resolution. Conversion owns extraction from bytes to blocks; parts identifies, cites, tracks, and displays parts.

Parts use a separate `silver.part_revision`, not `provision_revision`. Phụ lục, Biểu mẫu, Mẫu số, tables, and attached-file parts are first-class revisioned units. Artifact bytes remain separate. Part revision keys combine the document revision key and a revision-local part key. Cross-revision continuity needs a reviewed identity assertion, as for provisions. Release membership includes both. `silver.evidence_target` is the common validity target type: one surrogate ID, document revision, kind, and exactly one UNIQUE provision-revision or part-revision reference. Validate kind, same-document parentage, and acyclic containment. Amendment targets, validity, text periods, subjects, and chunks use that target ID; never encode a polymorphic local ID without its type.

| Part storage | Contract and indexes |
|---|---|
| `part_revision` | Parent part, document revision, kind and role, exact label, code, title, and citation bytes and components, source order, authority, confidence, and gaps. UNIQUE `(document_revision_id, part_key)`; index parent and order. Provision containment may reference a part without changing provision identity. |
| `part_block` | Ordered paragraphs, tables, and image regions; exactly one owner, a part revision or table cell. Ordinal, exact NFC text, artifact, page, and block anchors, authority, recipe, confidence, review state, and fidelity gaps. UNIQUE owner and ordinal; index owner and order for bounded paging. |
| `part_table`, `table_row`, `table_cell` | Table block, key, caption, ordered rows, cell coordinates, positive row and column spans, and explicit header links. Cell-owned ordered blocks allow nested tables. Preserve empty, absent, and unreadable states, units, footnotes, note links, printed formulas and cached values separately, instructions, and per-table and per-cell evidence. UNIQUE table and row and row and column; reject out-of-grid or overlapping spans. |
| `table_rendering`, `rendering_cell_span` | Recipe-versioned deterministic search text with byte ranges mapped to source cells. Preserve printed labels, units, notes, and rendering hash. Index rendering and range and cell; do not infer headers or compute values. |
| `part_artifact_span` | Many-to-many part revision and artifact hash, role, source observation, page and block anchors, and nullable half-open byte span. Require `0 <= start < end <= artifact.byte_size` when known; unknown byte mappings retain anchors and a gap. Index part and order and artifact and part. |

Artifact records retain declared byte size, media type, original or derivative role, source filename and URL, capture provenance, and hash. Publication validates all block, cell, target, and artifact references and includes their closure in the manifest. The [parts design](document-parts.md#files-and-table-fidelity) owns identification, citations, tracking, and display. Conversion's bounded XLSX adapter supplies cell-text blocks for search; parts presents a spreadsheet as a file part with metadata, search hits, and the official-source download link, without rich rendering in the first release. Format qualification belongs to [conversion owner question 1](document-conversion.md#risks-and-owner-questions). Presentations have metadata and source link only. Reject a JSON-only part tree because targets and paging need indexed rows.

| Record | Storage contract and main indexes |
|---|---|
| `relation_assertion` | Acting document or provision revision, operator, exact evidence span, source authority, confidence, and observation time. Index acting revision and source observation. Retain conflicting assertions. |
| `amendment_target` | Child of one assertion. Target document identity or unresolved reference; nullable `evidence_target_id`; exact target citation and target-text span; whole or partial extent; resolution state. Index target document, target ID, and assertion. |
| `amendment_difference` | Immutable before and after revision and target links, assertion, difference recipe and hash, exact changed spans, and difference artifact reference. Index assertion and target and revisions. |
| `validity_resolution` | Immutable reviewed resolution set, policy digest, evidence links, and explicit uncertainty. A release selects exactly one resolution set per evidence target. |
| `evidence_validity_interval` | Resolution ID, `evidence_target_id`, legal `daterange`, status, start and end certainty, and cause links. Index target and resolution; GiST range index. Reject overlapping definitive intervals for one target in one resolution. |
| `evidence_text_period` | Resolution ID, target ID, selected provision or part text revision, legal range, and continuity evidence. Validate matching type and document and reviewed continuity. Prevent two definitive text selections for one target and date. Unmatched historical text remains a gap. |

Use half-open legal date intervals `[start,end)`. An unknown start is not negative infinity; an unknown end is not proof of indefinite force. Store endpoint certainty explicitly. PostgreSQL supports range containment and exclusion constraints; `btree_gist` combines scalar equality with range overlap ([range documentation](https://www.postgresql.org/docs/17/rangetypes.html#RANGETYPES-CONSTRAINT)). Keep observation timestamps as `timestamptz`. Evaluate legal dates in Vietnam's timezone. Preserve intra-day source precision and flag unsupported date-only resolution rather than rounding it.

`as_of=D` selects the pinned release's resolution and text period containing D. It returns definitive status only for supported intervals. Unknown dates, missing text, competing assertions, or unresolved targets return gaps. "Exact" means exact interval evaluation over recorded evidence, not complete knowledge of all law. Corrected observations create a later release; old releases keep their answers.

The owner-supplied 09/2020 Điều 25 case must resolve to the acting khoản 6 Điều 22 of 50/2024 and the target Điều 25 separately. Its prior interval ends at 2025-01-01; a repealed interval begins then. Do not invent the prior start date. Child provisions inherit a reviewed whole-article repeal through explicit resolved targets. A partial-document badge cannot override a resolved provision interval. The other five clauses must not appear as amendments to that target without their own target evidence.

Reject a JSON-only target field and document-only validity inheritance. Keep verbatim replacement text and before-and-after revision links; do not generate consolidated text at query time.

## Vector and lexical storage

Recommend cosine hierarchical navigable small world (HNSW) indexes for scaling, initially `m=16`, `ef_construction=64`. Preserve Vietnam's exact scan until HNSW passes every accepted golden case. Build indexes only in release databases after bulk load, never over all historical writer vectors.

| Decision | Recommendation | Rejected or deferred option |
|---|---|---|
| Dense index | HNSW in the release chain, one embedding recipe per release | IVFFlat adds list training and tuning; choose it only if measured build cost outweighs the accepted recall and latency result. |
| Precision | Float32 storage for the first parity release | `halfvec` needs a measured precision comparison. Binary quantization changes candidate quality and is not the baseline. |
| Dimensions | 1,024 until the embedding design approves another recipe | Do not truncate vectors merely to fit memory. A Matryoshka recipe records full and output dimensions, normalization, and truncation recipe and gets its own release table. |
| Partitioning | Country by database, release by database, subjects by child tables | No shared historical vector index with release filters. No subject partitions. Revisit writer partitioning only after measured retention growth. |
| Lexical | Exact sparse inner-product ranking over eligible rows | Sparse HNSW adds approximation and another recall gate. An external search engine adds operations without evidence of need. |

Support the embedding sibling's 768-dimensional candidate beside the 1,024-dimensional baseline. `gold.chunk_vector` in the writer stores `vector` with a recipe identity and a dimension check; each release table is fixed `vector(D)` or approved `halfvec(D)`, one table per approved pair. Replace hardcoded dimensions and release validators together, including [types.go:28](../../internal/corpus/release/types.go#L28). Half precision remains a separate experiment.

The [pgvector documentation](https://github.com/pgvector/pgvector#hnsw) describes HNSW's higher memory and build cost relative to IVFFlat and recommends creating indexes after loading data. HNSW supports `vector` to 2,000 dimensions and `halfvec` to 4,000; sparse HNSW permits 1,000 nonzero elements. Approve a compatible dimension and precision before changing the chain. Never mix serving recipes.

Require pgvector 0.8 or later and pin an exact tested extension version. Iterative scans continue filtered approximate scans until enough rows or a scan limit; they do not guarantee exact recall ([iterative scans](https://github.com/pgvector/pgvector#iterative-index-scans)). Use transaction-local `hnsw.iterative_scan=strict_order`, bounded scan and memory settings, and a tested custom-plan policy. Put subject and date eligibility inside the ordered scan using indexed EXISTS predicates. Verify the actual plan: outer filtering after a fixed candidate LIMIT defeats iterative replenishment. Use an exact distance scan for small eligible sets or candidate shortfall. An exact fallback that cannot finish within the deadline returns a retryable error, not a silently incomplete success.

`gold.chunk_lexical_vector.content_sparse` is `sparsevec(1048576)` bound to a `gold.lexical_revision`. Preserve the 2^20 hash space and deterministic tokenizer contract. Recompute BM25 weights for the entire selected chunk set; never combine old and new corpus statistics. Query vectors represent term presence in the existing implementation ([bm25.go:1](../../pkg/rag/lexical/bm25.go#L1)). `gold.lexical_statistics` persists corpus statistics and recipe digest for audit and rebuild, so serving never loads all source text at startup. Use chunk-identity B-trees and exact `<#>` ranking. No lexical approximate nearest neighbor (ANN) graph is budgeted. If scans miss budget, benchmark term postings before exact scoring and require parity.

The following are calculated payload sizes and planning estimates per million chunks. GiB means 2^30 bytes. Payload formulas are `4D+8` for vector, `2D+8` for halfvec, `D/8+8` for bit, and `8Z+16` for sparsevec, where D is dimensions and Z is nonzero entries ([type reference](https://github.com/pgvector/pgvector#reference)). Heap, page, and index overhead are extra.

| Component per million chunks | Payload GiB | Planning allowance GiB |
|---|---:|---:|
| Float32, D=1,024 | 3.82 | Dense heap 4.5-6; HNSW separately 5-8 |
| Half precision, D=1,024, alternative | 1.91 | Heap 2.5-3.5; HNSW separately 3-5 |
| Binary, D=1,024, alternative | 0.13 | Graph and retained original vectors are additional; not selected |
| Sparse, Z=200 assumption | 1.51 | Heap 2-3; no ANN graph |
| Text, provenance, validity, subjects, B-trees | Unknown | 12-23, including selected historical evidence |
| Selected serving snapshot total | Not measured | 24-40 |

Measure database working set on the selected release; do not extrapolate a launch RAM reservation from a million-chunk estimate. Budget one query embedder separately. Bound `work_mem` and index-build concurrency together.

## Releases, delivery, and retention

`ingest.corpus_release`, immutable `ingest.release_manifest`, versioned `ingest.release_activation`, and fenced `ingest.release_retention_hold` and `release_retention_claim` live in the writer chain. The retained resolver acquires a durable pin before a local pool lease ([resolver.go:60](../../internal/corpus/release/resolver.go#L60)); `control.release_pin` is its persistent contract. Do not replace pins with a process-local reference count.

The manifest binds country, revisions, acceptance receipts, conversion provenance, validity resolutions, parts, blobs, release chain version, taxonomy, assessment set, retrieval recipes, vector table name, precision, extension version, membership digests, and compatible images. Freeze evidence contents; lifecycle state and activation remain mutable control data. Never change a published database to repair evidence. `release.manifest` in the release database is a frozen copy; `release.membership_digest` lets verification recompute closure digests without the writer.

Recommend logical release snapshot restore. The write path builds a candidate database with the release chain, bulk-loads the closure through the `projection` store, finishes the chain so indexes build after load, seals it, dumps it with `pg_dump` custom format, stores the dump in private Vietnam vStorage, then restores it into a new allowlisted database name on serving capacity. PostgreSQL supports consistent logical dumps and selective, parallel restore ([pg_dump](https://www.postgresql.org/docs/17/app-pgdump.html), [pg_restore](https://www.postgresql.org/docs/17/app-pgrestore.html)). This is a logical archive, not a portable copy of data-directory or HNSW files. Restore rebuilds indexes and must budget that work. Use separate restore capacity and attach its verified endpoint, or throttle a same-instance restore only after a serving-load rehearsal proves headroom. Hosting owns that capacity mechanism.

Before readiness, verify hashes, counts, cross-schema closure, release chain version and extension compatibility, permissions, query plans, and evidence parity. Analyze and warm the restored database. Verify all source-file references resolve through `release.artifact`. Register the snapshot in `control.release_catalog` only after checks pass; never derive a database connection from client-supplied release text.

Reject continuous logical replication from the mutable writer: it transports partial work and PostgreSQL excludes schema DDL and sequence state from logical replication ([restrictions](https://www.postgresql.org/docs/17/logical-replication-restrictions.html)). Reject direct table-by-table copy into an active database. Bounded COPY constructs a candidate; the published delivery unit is the verified snapshot and manifest.

Writer activation compares `ingest.desired_selection` version and records old and new release plus audit identity. `control.applied_selection` verifies the ordered record and readiness, atomically advances applied selection, and acknowledges its version and digest. Reject stale delivery; reconcile lost acknowledgements. After any restore, serving reconciles its applied selection with the writer's ordered activation records before it accepts traffic. Keep traffic and retention deletion fenced across restart until the latest writer-authorized selection, activated manifests, identity keys, pins, and acknowledged holds reconcile beyond the recovered WAL. Reverify every retained database plus query-recipe bundle. Missing authority or continuity keeps both fences closed. Hosting owns the restore procedure.

`ingest.catalog_recovery_record` and `control.recovery_record` are append-only: monotonic sequence, prior digest, mutation, entity key, and receipt. Each authority commits the mutation and recovery-delivery intent together; acknowledge catalog changes only after their records and referenced manifests and keys are durable in hosting-provided recovery storage independent of the database backup. Replay the verified checkpoint and ordered tail, then issue fresh deletion fences; never reset sequence high-water marks from an old backup.

There is no distributed transaction between writer and serving databases. Pin one local bundle through serialization; explicit follow-ups retain it. Bind pins to immutable evidence and recipe artifacts, and record dense-search availability separately. Only one embedding model runs on the serving host at a time, under the [embedding runtime policy](embedding-models.md#migration-and-rollout-slices). Before replacing the runtime, re-embed every release that stays in dense search, or retire older releases from dense search. Re-embedding creates a new immutable release and explicit predecessor mapping. Retired releases remain available through `document` and lexical search, with a visible gap. Dense search requires a matching query runtime (same model, tokenizer, recipe, and dimensions). Drain in-flight dense requests before replacing the runtime. Missing runtime readiness fails dense requests explicitly.

Online retention is the active release plus at least two prior releases, kept for at least 30 days, plus any release under an explicit pin or hold. Retain each complete database, evidence closure, and recipe artifacts for that window; retention does not require a loaded old runtime. Retain verified archives for one year initially. Preserve identity mappings and release tombstones beyond snapshot expiry. An expired release returns an explicit unavailable error. `control.retention_claim` serializes pins and fenced deletion claims. Writer deletion needs durable destination tombstone acknowledgements that block new pins; unreachable destinations block deletion. Protect blobs.

For snapshot size S and N online releases, serving storage is approximately `N × S`; a new candidate adds S plus restore scratch and write-ahead log (WAL). The writer adds its retained revisions, vector cache, and ledgers independently. Content-addressed source blobs can deduplicate; logical release databases do not share heap or index pages. Do not budget database deduplication or archive compression before measuring it.

## Serving queries and accounts

Use a release-scoped operations object over the `reader` store. Cache immutable summaries by release ID, never just country. Measure these proposed warm 95th-percentile database budgets at 2 requests per second, with bursts of 8, under concurrent application and query-inference load matching the hosting and embedding contracts. They exclude authentication, inference, HTTP transfer, and client rendering; they are not service claims.

| Tool | Queries and indexes | Database budget |
|---|---|---:|
| `guide` | Release and startup metadata and taxonomy vocabulary, normally cached; primary-key lookup on cold load | 20 ms |
| `corpus_status` | `gold.coverage_summary` counts, coverage denominators, taxonomy and source summaries; release, subject, and source B-trees | 50 ms |
| `quality_gaps` | Keyset-paged gap projection; `(kind, subject_key, id)` and document-key lookup; no whole-text aggregation | 100 ms |
| `search` | One eligible-set selection, dense and sparse arms, bounded fusion, batch evidence, validity, and target hydration; HNSW, subject B-trees, date GiST, document metadata indexes | 500 ms total |
| `document` | Evidence and legacy identity resolution, ordered parts and chunks, dated text and validity, incoming and outgoing relations, artifact handles; unique keys, `(document_revision_id, ordinal)`, target and source indexes | 200 ms |

Use pgx pools initially, without PgBouncer or read replicas. Cap all release pools together at 16 connections per server process, including overlap; cap auth at 4 and control at 2. Reserve 8 operator connections per PostgreSQL instance. Two processes therefore need at most 52 allocated connections on an instance hosting all roles. Keep idle minimums zero and lazy-open retained-release pools. Add admission and statement deadlines; reject overload before unbounded queues accumulate.

Use transaction-local settings so released connections cannot leak query tuning. Add read replicas only when measured read demand warrants their cost. A replica must prove the requested manifest before receiving traffic; never retry a partial request on another release. Snapshot restore remains the writer-to-serving transport even if physical replication later supplies serving failover.

Keep auth, revocations, consent, account state, and future dossiers in the separate account database with its own roles, chain, backup, and authoritative writer. Dossiers store evidence keys, release references, and citations, not copied corpus rows or query history. Corpus rollback must never restore auth. Account checks use current authoritative state and fail closed. Keep SQL bind values, question text, query vectors, and account identifiers out of corpus snapshots, batch artifacts, statement logs, and external telemetry. Hosting must keep Vietnam serving data, control traffic, and account backups within its approved residency boundary.

## Capacity assumptions

The owner-reported AWS baseline in [hosting](vngcloud-hosting.md#workload-and-sizing) is comparison evidence, not proof of the new release layout's size. Measure Vietnam writer and serving snapshots, indexes, working sets, restore scratch, and WAL before sizing either tier. Start from `a-general-4x8` (4 vCPU, 8 GiB), with `a1-standard-2x8` as the cheaper fallback if measurement allows, on a dedicated small serving vServer for PostgreSQL, one embedder, the Go server, auth, and the approval and audit service. Grow only from measurement. The approved Vietnam-now starting scope resolves the launch growth-envelope question; do not provision the later subject or six-country envelopes. Original source files stay in object storage, not on the serving disk.

The following snapshot estimates remain unmeasured planning envelopes, not launch reservations. Three times the subjects does not imply three times the data; the second row assumes three times the unique documents and chunks.

| Scenario | Documents / chunks | One snapshot S | Three retained releases plus candidate, 4S |
|---|---|---|---|
| Vietnam now | 3,974 / 142,086 | 3.4-5.7 GiB | 13.6-22.8 GiB |
| Vietnam, 3x subject envelope | 11,922 / 426,258 | 10.2-17.1 GiB | 40.8-68.4 GiB |
| Six countries, later planning only | 8,820 / 381,955 | 9.2-15.3 GiB aggregate | 36.8-61.2 GiB aggregate |

Longer retention, pins, holds, scratch, and WAL need additional space. The six-country row authorizes no AWS changes. Writer compute, worker compute, artifacts, backups, and vStorage costs remain unpriced until measured. Measure p95 at 2 requests per second, bursts of 8, with concurrent PostgreSQL, auth, approval, audit, application load, and one embedder. Hosting must prove at least 25% free headroom; [embedding owner question 1](embedding-models.md#owner-questions) owns latency. Forecast writer growth as `initial bytes + sum(new revision bytes + vectors + ledgers)`. Add capacity before peak disk exceeds 75% or memory pressure breaches the read budget.

## Interfaces

| Sibling | Required contract |
|---|---|
| [Write path](corpus-write-path.md) | Intake acceptance independent of builds; closed build membership, immutable outputs, computed amendment differences, resolved validity and targets, accepted gaps, candidate projection, and publication checks. It owns retries, execution locations, promotion orchestration, and retention execution. Database adapters provide fenced transactions, snapshots, ordered activation, recovery reconciliation, and pin and deletion fencing. |
| [Corpus workflows](corpus-workflows.md) | Country admission epochs, intake, batch, and build execution scopes, leases, fenced acceptance, acceptance receipts, immutable attempt inputs, and durable launch and notification records. Workflows own topology, signals, queues, limits, and reconciliation scheduling; signals never replace ledger receipts. |
| [Document conversion](document-conversion.md) | Raw and normalized blocks, offset maps, reversible edits, quality metrics, thresholds, outcomes, and reasons, comparisons, and the shared quarantine decision contract. Every activity reading untrusted bytes uses the converter sandbox queue `corpus-converter-v2`. Storage adapters accept validated references, not raw-file parsing work. |
| [Embedding models](embedding-models.md) | Exact model, tokenizer, and preprocessing digests, dimensions, precision, distance, optional truncation, query and bulk parity, dense plus lexical fusion without a reranker, and quality floors. One recipe per release. Supply one matching ready runtime for dense search; re-embed retained dense-search releases or record explicit dense-search retirement before replacement. |
| [Document parts](document-parts.md) | Typed part revisions, parent links, exact citations, ordered blocks, table rows, cells, spans, and header links, rendering-to-cell mappings, artifact byte spans, authority, gaps, and common validity targets. Parts share release membership and subject associations. |
| [Corpus review](corpus-review.md) | Indexed review extensions on `ingest.quarantine_decision` with opaque reviewer keys and a separate approval issuer. Reviewer keys stay in the writer and never enter a release. |
| [VNGCloud hosting](vngcloud-hosting.md) | PostgreSQL 17, tested pgvector, a separate Vietnam writer tier, local serving control and account storage, socket-only serving access, private TLS for remote writer access only after proof, separate migration, runtime, and restore roles, bounded restore capacity, vStorage artifacts and backups, and off-host recovery. Reconcile ordered activation after restore before traffic. |

For downloads, `release.artifact` supplies pinned artifact identity, declared bytes, hash, and membership. Hosting defines configurable positive limits for artifact bytes, per-client and overall concurrent streams, and stream timeout. Serving checks declared size before streaming and enforces actual bytes while streaming.

## Chain mutability and verification

The [chain rules](../../instructions/engineering.md#data-and-generated-code) apply to the three chains as follows. All of this is unimplemented.

| Rule | Application |
|---|---|
| Editable in place | Each chain is edited in place until a tagged release embeds it or an operator migrates a durable database with it. A slice may change any file in the chain while keeping the file set and order in the layout tables above. Hosted generation replays each chain from an empty database on every run. |
| Append-only trigger | The first tagged release that embeds the chain, or the first durable database an operator migrates with it: the M3.5 isolated writer for the writer chain, the M4 serving rehearsal for the release and control chains. From then on, add files numbered after the maximum and never change an existing file. The [Vietnam plan](../plans/vietnam.md) records when each chain turned append-only. One chain turning append-only does not change the others. |
| Populated upgrade | After a chain is append-only, the generation job also applies it to a database built from the previous tagged release and populated with that release's fixtures, then asserts retained rows, ledgers, and a no-op second run. |
| Frozen legacy | Any change under `deploy/migrations/` fails the generation job. `sql/`, `pkg/store/`, `cmd/migrate`, and `internal/corpus/schematest` change only for zero-drift regeneration or owner-approved maintenance. |
| Generation job | One disposable PostgreSQL 17 with pgvector replays the auth chain and the three corpus chains, regenerates every sqlc entry, runs the schema tests that exist for each chain, uploads the generated tree as the review artifact, and fails on drift, under the [hosted check matrix](../../instructions/verification.md#hosted-check-matrix). |

Schema tests live beside the stores they cover and use one database URL variable per chain: `BANHMI_CORPUS_WRITER_TEST_DATABASE_URL`, `BANHMI_CORPUS_RELEASE_TEST_DATABASE_URL`, and `BANHMI_CORPUS_CONTROL_TEST_DATABASE_URL`. Chain-level structural tests live in `cmd/corpus-migrate`; behavior tests live in the store package whose queries they exercise. Tests that commit fixed identities run alone on a fresh database, as the legacy race test does. The first writer-chain tests are:

| Test | Home | Proves |
|---|---|---|
| `TestWriterChainReplay` | `cmd/corpus-migrate` | Clean replay, no-op second run, `goose_db_version_corpus_writer` holds the maximum version, `-to` stops at the version, `-status` writes nothing, and the command refuses a database holding a legacy or other-chain version table or an existing `ingest` schema |
| `TestWriterSchemaBoundaries` | `cmd/corpus-migrate` | Single-column primary keys, no cross-schema foreign keys, no `country`, `country_code`, or `jurisdiction` column, and the `ingest.country_admission` singleton constraint |
| `TestAdmissionEpochFence` | `intake` | A write carrying a stale admission epoch changes nothing |
| `TestExecutionLeaseFence` | `intake` | Claim and reclaim allocate monotonic `attempt_fence`; an apply with a stale owner or fence writes nothing |
| `TestExecutionResultIdempotency` | `intake` | An identical replay returns the committed receipt; a changed payload under the same idempotency key fails |
| `TestRevisionAcceptanceEligibility` | `intake` | Only `accepted` and `accepted_with_gaps` are build-eligible; each transition binds execution, digests, and decision revision |
| `TestDeliveryRecordReconciliation` | `intake` | Unacknowledged due records are visible to bounded reconciliation; duplicate delivery returns the prior receipt |
| `TestImmutableRevision` | `document` | UPDATE and DELETE on `document_revision` and `provision_revision` fail; identical inputs reuse the revision through its UNIQUE key; metadata changes mint a new revision |
| `TestIdentityCountryGuard` | `document` | A key whose country differs from `ingest.country_admission` is rejected |
| `TestLegacyIdentityMappingOutcomes` | `document` | `resolved`, `ambiguous`, and `unavailable` rows keep their state constraints; a bare legacy ID resolves only when all retained mappings agree |

Synthetic CI proves storage behavior, not legal accuracy or VNGCloud capacity. Promotion and domain cutover require the write-path and hosting gates separately.

## Risks and owner questions

Snapshots and BM25 revisions can dominate storage. Historical coverage and subject fan-out are unknown. HNSW row counts do not prove recall. Exact lexical scans may breach latency targets. Lost identity catalogs cannot be reconstructed safely from titles. Schema cannot repair attribution. Control failure can block pins despite healthy snapshots; include control in recovery planning. Writer and release evidence tables are defined twice; the `projection` store's load tests must prove every release column has a writer source.

1. Approve one year of archives beyond the online retention minimum? Recommend one year, including recipe artifacts needed for restore. Online retention remains active plus at least two prior releases for at least 30 days, plus explicit pins or holds. Price longer dossier guarantees separately.
2. Should subject filters include unclassified evidence? Recommend a separate possible-match set by default. Strict filtering must warn about coverage.
3. Which corpus recovery targets should hosting prove? Recommend no lost activated manifests, identity keys, or acknowledged holds, four-hour restore, and at most 24 hours of unpublished work lost. Auth recovery remains [hosting owner question 1](vngcloud-hosting.md#risks-and-owner-questions).
4. Approve a one-time read-only hosted export of Vietnam document identities from the legacy `banhmi` database for `silver.legacy_identity_mapping`? Recommend yes, as a protected workflow with a hash-pinned manifest and no write access, before M4 serving rehearsal. Without it the `document` tool resolves no legacy IDs and reports that gap.

Unverified: per-release storage sizes, live extension versions, traffic, restore speed, hardware latency, VNGCloud limits and backup cost, historical coverage, owner-supplied legal examples, and whether the migration role may create extensions on VNGCloud PostgreSQL. No runtime or deployed checks ran.
