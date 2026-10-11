Proposal. Not approved, not implemented.

# Corpus database

Keep one writer database per country. Publish each serving release into a separate, immutable database. Serve Vietnam from VNGCloud. Keep account data outside every corpus snapshot.

## Goal and limits

Support more subjects without copying laws into subject databases. Preserve country isolation and exact citations through rebuilds and legal changes. A release identifies all evidence used by a request.

The owner has settled the product as Tra cứu quy định at `tracuuquydinh.vn`. Vietnam questions are Vietnamese with diacritics or a document number; the serving layer validates the question before any embedding or corpus query.
Questions stay in Vietnam and use only the self-hosted query embedder. English is interface text only. No answer model, generative model, or reranker runs on our servers. Retrieval uses dense plus lexical fusion. Public source
documents may use cloud batch processing. The five existing non-Vietnam services remain unchanged on AWS.

This proposal owns database storage and reads. The sibling designs own processing, model selection, document-part semantics, and infrastructure. The owner's VNGCloud decision supersedes the AWS-only transition wording in
[WEBSITE.md:139](../design/WEBSITE.md#L139) for this proposal.

## Current state and evidence

These are source facts or recorded observations, not a deployment audit. “Source-only” means execution remains unproved. The source plan leaves migrations, generated stores, and persistent adapters pending
([corpus-redesign-source.md:19](../plans/corpus-redesign-source.md#L19)).

| Area | Fact and evidence |
|---|---|
| Country layout | The documented deployment uses six databases on one PostgreSQL 17 instance. Country is a database boundary, not a column on every corpus row ([ARCHITECTURE.md:129](../ARCHITECTURE.md#L129), [ARCHITECTURE.md:71](../ARCHITECTURE.md#L71)). The registry selects a database name and serving supports a country-specific override ([jurisdiction.go:53](../../pkg/base/jurisdiction/jurisdiction.go#L53), [hosted.go:131](../../internal/app/serve/hosted.go#L131)). |
| `ingest` | Holds discovery, fetch leases, and embedding cache. Source-only additions include builds, stage results, releases, activation, and retention claims ([SCHEMA.md:14](../design/SCHEMA.md#L14), [schema.sql:162](../../sql/ingest/schema.sql#L162), [schema.sql:428](../../sql/ingest/schema.sql#L428)). |
| Intake constraint | Revisions, scheduler leases, stage results, and external jobs require build IDs ([silver/schema.sql:395](../../sql/silver/schema.sql#L395), [ingest/schema.sql:240](../../sql/ingest/schema.sql#L240), [ingest/schema.sql:265](../../sql/ingest/schema.sql#L265), [ingest/schema.sql:341](../../sql/ingest/schema.sql#L341)). Independent intake needs new persistence contracts. |
| `bronze` | Holds source metadata and inline payloads. Downloaded file bytes live outside PostgreSQL; rows retain hashes and byte sizes ([schema.sql:13](../../sql/bronze/schema.sql#L13), [schema.sql:53](../../sql/bronze/schema.sql#L53), [schema.sql:72](../../sql/bronze/schema.sql#L72)). |
| `silver` | Holds documents, sections, relation evidence, and validity periods. Source-only identity, immutable revision, and subject tables now coexist with these mutable tables ([SCHEMA.md:46](../design/SCHEMA.md#L46), [schema.sql:315](../../sql/silver/schema.sql#L315), [schema.sql:395](../../sql/silver/schema.sql#L395), [schema.sql:494](../../sql/silver/schema.sql#L494)). |
| `gold` | Existing chunks contain `sparsevec(1048576)` BM25 vectors. Dense storage is `vector(1024)` with cosine HNSW. Source-only chunk sets, vectors, lexical revisions, and published evidence follow them ([schema.sql:10](../../sql/gold/schema.sql#L10), [schema.sql:34](../../sql/gold/schema.sql#L34), [schema.sql:58](../../sql/gold/schema.sql#L58)). |
| `config` | Holds source and retrieval policy. Source-only taxonomy releases freeze a hierarchy and digest ([SCHEMA.md:119](../design/SCHEMA.md#L119), [schema.sql:157](../../sql/config/schema.sql#L157)). |
| Auth | The authenticated composition opens a separate pool. `auth.user_account` stores account status and authorization epoch. Auth uses its own migration command and version table ([hosted.go:69](../../internal/app/serve/hosted.go#L69), [schema.sql:20](../../sql/auth/schema.sql#L20), [main.go:49](../../cmd/auth-migrate/main.go#L49)). The repository records hosted auth checks, not an authenticated deployment ([PLAN.md:17](../../PLAN.md#L17)). |
| sqlc | Separate packages are `dbauth`, `dbbronze`, `dbsilver`, `dbgold`, `dbingest`, and `dbconfig`, under `pkg/store/`. Sources use pgx/v5; sparse vectors map to strings for generation and retrieval uses raw pgx ([sqlc.yaml:12](../../sqlc.yaml#L12), [sqlc.yaml:42](../../sqlc.yaml#L42), [sqlc.yaml:87](../../sqlc.yaml#L87), [sqlc.yaml:132](../../sqlc.yaml#L132), [sqlc.yaml:187](../../sqlc.yaml#L187), [sqlc.yaml:218](../../sqlc.yaml#L218)). |
| Migrations | `cmd/migrate` verifies Atlas checksums and applies embedded Goose chains for extensions, bronze, silver, gold, ingest, and config. Each has its own version table ([main.go:42](../../cmd/migrate/main.go#L42), [main.go:89](../../cmd/migrate/main.go#L89)). Canonical SQL is ahead of generated persistence artifacts ([PLAN.md:23](../../PLAN.md#L23)). |
| Retrieval | Vietnam selects exact dense scanning ([jurisdiction.go:178](../../pkg/base/jurisdiction/jurisdiction.go#L178)). The shared approximate path overfetches before outer filters, falls back on shortfall, and sets strict iterative scans/custom plans ([retrieve.go:901](../../pkg/rag/retrieve/retrieve.go#L901), [retrieve.go:1015](../../pkg/rag/retrieve/retrieve.go#L1015)). BM25 ranks sparse inner products; Gold declares no sparse approximate index ([retrieve.go:1280](../../pkg/rag/retrieve/retrieve.go#L1280), [gold schema](../../sql/gold/schema.sql#L1)). |
| Validity limits | Current schema has optional section/version fields, but the documented pipeline uses document-level validity. Amendment events are reserved ([SCHEMA.md:56](../design/SCHEMA.md#L56)). The provision repeal and relation-attribution defects belong to the corpus redesign ([schema.sql:192](../../sql/silver/schema.sql#L192), [SCHEMA.md:101](../design/SCHEMA.md#L101), [WEBSITE.md:210](../design/WEBSITE.md#L210)). |
| Physical size | The repository records an RDS `db.t4g.micro` and per-database logical backups, with automated RDS backups disabled ([ARCHITECTURE.md:192](../ARCHITECTURE.md#L192)). The owner reports 11.3 GB used of 20 GB across six country corpora and another service, with 1 GiB RAM and 3.5% average CPU; see the [hosting measurement record](vngcloud-hosting.md#workload-and-sizing). This is not a Vietnam release-snapshot measurement. |

Counts below are observations recorded in [PLAN.md:89](../../PLAN.md#L89), not live checks in this task. The owner's Vietnam MCP observation agrees on about 3,974 documents and 142,086 “provisions.” The repository calls the latter
chunks. Use chunks for sizing; do not equate them with distinct provisions.

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

Choose country writers plus country-release serving databases. Preserve the approved country boundary ([source plan:41](../plans/corpus-redesign-source.md#L41)). Database names are private handles; country-qualified keys are
public identities.

| Option | Isolation, migrations, backups, cost, and adding a country | Decision |
|---|---|---|
| One mutable database per country for writing and serving | Country restore and migration are independent. Few copies, but builds compete with reads and readers can see mixed stages. Adding a country creates and seeds one database. | Retain as legacy compatibility only. |
| One database with a country key | One migration chain and shared capacity. Every key, join, authorization rule, and index needs country scoping. Restore and failure affect all countries. Adding a country is cheap but this rewrites the existing isolation contract. | Reject. |
| Shared writer, country-release serving databases | Isolates serving and allows independent restore, but write migrations, backups, and mistakes still cross countries. Requires a country-key migration across the writer. | Reject. |
| One writer per country, separate database per serving release | Independent country migrations and backups. Snapshot storage multiplies; migration and restore automation repeats per country. Adding a country provisions a writer and release bundle without changing another country's tables. | Recommend. |

Corpus generation runs on AWS as the single writer per country dataset, orchestrated by Temporal Cloud; serving databases on VNGCloud and on AWS receive verified releases. The [write-path decision](corpus-write-path.md#decisions) approves Temporal Cloud Developer, pay as you go with no minimum, and the isolated AWS writer. The country writer alone authors revisions and desired selection. Existing AWS services remain unchanged until
opt-in. Keep a separate durable Vietnam control database for imported manifests, applied selection, pins, and local retention claims. It cannot author promotions. Serving requests need no writer connection. Hosting separates bulk
work from serving capacity. Databases sharing an instance share CPU, memory, disk failure, and physical recovery. Use distinct roles and revoke public connection grants. Never route Vietnam requests to an AWS database as a
fallback.

Keep `ingest`, `bronze`, `silver`, `gold`, and `config` on writers. A serving snapshot contains only the selected evidence closure, policy, identity mappings, and frozen manifest. Exclude job queues, embedding caches, mutable
activation records, account state, and unused historical parser outputs. The closure includes known historical legal text needed for date queries, not merely today's text. Writer control and local control stay outside snapshots.
Serving uses SELECT-only corpus roles; its local control role acquires and closes pins without storing question text.

Keep one primary-key column per table and natural-key UNIQUE constraints. Validate cross-schema business keys through publication anti-joins, following [engineering.md:25](../../instructions/engineering.md#L25) and
[SCHEMA.md:9](../design/SCHEMA.md#L9). Use sqlc for new storage adapters. Put queries under the owning SQL schema and generate on GitHub.

## Identity, revisions, and release membership

Mint universally unique identifiers (UUIDs) once in `silver.document_identity`. Use `vn:doc:<uuid>`. Keep country and entity kind, but exclude domain, brand, title, source number, and local sequence. Retain identity assertions and
the catalog through every writer rebuild and backup restore. Reject keys whose country differs from the selected database's singleton country metadata.

Use `vn:doc:<uuid>:rev:<digest>` for an immutable document revision. Compute the digest from canonical source-observation hashes, text/metadata hashes, and conversion/normalization/parser/policy recipes. Exclude local IDs and
processing timestamps. Identical inputs reuse revisions; metadata changes create distinct revisions.

Add `ingest.build_revision` membership with UNIQUE `(build_id, document_revision_id)` so builds can reuse immutable revisions. New revisions record `intake_execution_id` as processing provenance; retain legacy
`document_revision.build_id` only as nullable import provenance. It is neither required origin provenance nor membership. Publication validates selected acceptance receipts and membership, not origin-build equality. Include
metadata identity in the existing uniqueness rule through an append-only migration ([silver/schema.sql:419](../../sql/silver/schema.sql#L419)). Export stable keys; local IDs may change.

A provision key contains the document revision digest and a revision-local provision key. Preserve native citation bytes and source anchors separately. A stable provision UUID is optional and needs an approved continuity
assertion, as the source schema requires ([silver/schema.sql:478](../../sql/silver/schema.sql#L478)). Never infer continuity from matching text or renumbering. Chunk IDs remain local to a chunk set. Citations carry evidence key,
revision key, release ID, native citation, and source hash. The write path computes amendment differences in its relations and validity stage; the database stores them. The [website
design](../design/WEBSITE.md#phase-3-depends-on-the-corpus-redesign) owns redline display as a corpus-redesign dependency.

Extend legacy mappings to typed document, section, and chunk IDs. Namespace each mapping by country, accepted baseline/snapshot identity, entity kind, and old ID. Preserve the existing `resolved`, `ambiguous`, and `unavailable`
outcomes ([silver/schema.sql:360](../../sql/silver/schema.sql#L360)). A bare old ID resolves only when all retained mappings agree. Missing history never justifies guessing. Merges and splits append assertions and keep old keys
resolvable to their original evidence.

## Intake ledger and revision acceptance

`DocumentIngest`, `OcrBatch`, and `Reprocess` record work in an intake ledger with no build ID. Source and embedding work use the same execution contract. `CorpusBuild` selects build-eligible revisions into its manifest. Reject
fake builds for intake and a second acceptance ledger in Temporal.

| Proposed indexed record | Identity, provenance, and constraints |
|---|---|
| `ingest.country_admission` | Singleton country, monotonic admission epoch, admitted writer, and enabled state. Every mutation checks the current epoch; rotate it on writer replacement or recovery and revoke legacy scheduler write access. Never let a restored epoch reauthorize an old worker. |
| `ingest.execution` | Surrogate execution ID; UNIQUE `(workflow_id, attempt_fence)`; workflow kind, immutable processing-attempt key, country admission epoch, document version or batch collection key, input/recipe/budget digests, predecessor, and run receipts. No build ID. Temporal Run IDs are receipts, not identity. A separate build-execution link associates only `CorpusBuild` work with its build. |
| `ingest.execution_lease` | UNIQUE resource kind/key within the country: source, document attempt, batch, build, or country control. Store owner, monotonic fence, expiry, renewal, cancellation, and admission epoch. Claim/reclaim allocates the execution's `attempt_fence`; a build lease serializes assembly, not intake. Index expiry and next eligible work. |
| `ingest.execution_result` | Append-only receipt with execution ID, stage/substep, shard, immutable idempotency key, input/output hashes, recipe/budget, outcome/counts, cursor, and retry eligibility. UNIQUE idempotency key excludes mutable owner/fence. Changed inputs or recipes require a new processing attempt; identical replays return the committed receipt. |
| `ingest.batch_request`, `ingest.batch_execution` | Durable requests link waiters to one country/provider/recipe/collection; the batch links to an execution, never a required build. Freeze membership and per-item inputs. Persist submission intent/key, provider receipt, uncertainty, polling state, accepted item receipts, and waiter links. UNIQUE provider/submission key and batch/item; index pending requests and next poll. |
| `ingest.delivery_record` | Launch intents, decision delivery, and completion notifications with immutable deduplication key, recipient workflow ID, referenced receipt, expected epoch/hash, due time, and acknowledgement. Commit with source cursors or accepted results; index unacknowledged/due records for bounded reconciliation. Do not target a Temporal Run ID. |
| Observation and revision provenance | Captures link source window, document version, artifact hashes, and accepting execution/result. `document_revision.intake_execution_id` identifies its origin; child receipt links preserve later reuse attempts without rewriting the revision. Import legacy provenance explicitly; never invent an intake execution for an unknown historical attempt. |

Stage content-addressed blobs before acceptance. One transaction validates country admission epoch, execution, live lease owner/fence, cancellation, input/recipe/budget hashes, and decision revision; then commits output
references, acceptance state, result, and delivery records. Stale workers write nothing. A changed payload under an existing idempotency key fails. A crash after commit is recovered from the receipt and pending delivery records;
signals only wake readers. Uncommitted objects remain staging objects. Bounded reconciliation resumes launches and notifications idempotently.

Keep append-only `ingest.revision_acceptance` events and a current-state projection keyed by revision/processing attempt, indexed by state/watermark. States are `candidate`, `quarantined`, `accepted`, `accepted_with_gaps`, and
`excluded`. A revision is build-eligible only in state `accepted` or `accepted_with_gaps`. Each transition binds execution, candidate digest, quality report hash, decision revision, and any scoped gaps. OCR authority remains
non-binding. A reprocessed revision is a candidate until its before and after comparison passes and, when critical text changes, the owner adjudicates. Critical changes include legal numbers, dates, negations, citations, authority
flags, and part boundaries. `ApplyComparison` records the comparison before `ApplyEvidence` atomically validates prior revision, candidate/report hashes, comparison, and required adjudication with acceptance. Reusing blobs or a
revision never downgrades a previously accepted attempt or skips the new attempt's gates. Keep comparisons and exact changed spans.

`CorpusBuild` freezes acceptance receipts and the source-coverage watermark in one consistent snapshot. Base membership, replacements, additions, and tombstones remain separate from processing provenance.
Pending/quarantined/excluded items stay in coverage accounting; late acceptance needs a new build.

## Conversion provenance and decisions

Link every provenance record to its document revision, document version, processing execution, and candidate digest. Keep content immutable; append new reports and decisions. The database stores conversion's contract and validates
references; it does not choose converters or quality thresholds.

| Storage | Contract |
|---|---|
| Artifact blobs and indexed references | Retain original bytes, raw and normalized `ConvertedDocument` block artifacts, exact raw/normalized UTF-8 text, recipes, and output manifests by hash and byte size. Indexed revision/artifact-role links select the raw and normalized outputs without loading whole blobs. |
| Offset maps | Indexed mapping segments name source/destination artifact hashes, block IDs, coordinate spaces, and half-open `[start,end)` byte ranges; text ranges count UTF-8 bytes. Spaces are original bytes, decoded package member, raw converter text, and normalized text. Preserve many-to-many joins/splits/compositions, transform order, and page/object/member anchors. Store the complete map as a hashed blob; index revision/block and source/destination ranges for bounded lookup. |
| Reversible edit ledger | Immutable hashed blob records ordered operation, rule/version, input/output hashes and ranges, removed bytes, replacements, encoding/font evidence, and source anchors. Indexed revision and input/output hashes bind the ledger to its exact candidate. Reversal must reproduce raw converter text byte-for-byte; the original artifact separately preserves downloaded bytes. |
| Quality report | Immutable per-artifact reports and document roll-up blobs. Indexed rows retain revision, candidate/report hash, policy/threshold version, outcome, metric numerators/denominators/thresholds, and child reason/scope/gap rows. Non-queryable diagnostics stay in blobs. Preserve `accepted`, `accepted_with_gaps`, and `quarantined`; `needs_ocr` requests conversion, not acceptance. |
| Quarantine decision | Append-only indexed row: decision ID, document version ID, quality report hash, workflow fence token, actor, action, reason. Also bind revision/candidate digest, country, original/output hashes, expected decision epoch, scope/evidence, expiry, and replacement recipe/source if applicable. `workflow_fence_token` binds the execution's `attempt_fence`; index version/epoch and report hash. Actor is an opaque reviewer key linked to the Vietnam operator audit record, outside Temporal and serving snapshots. |

Validate map bounds against each hashed artifact. Decoded-member offsets are not ZIP offsets; derivative offsets are not original offsets. Unknown original-byte mappings remain null with `BYTE_MAPPING_UNAVAILABLE`, plus usable
page/object/member anchors. Never invent scan offsets. Decision acceptance checks actor authority, current fence/decision epoch, candidate/report hashes, and expiry in the fenced transaction. Duplicate delivery returns the prior
receipt; stale decisions cannot accept a new candidate. Public provenance exports decision references and scoped reasons, not operator identity. The [review API comes first](corpus-review.md#decisions), behind mutual TLS with a private CA; the owner reviews through AI agents. The human console is deferred; retain the audit journal.

Authority is the owner or reviewers the owner designates, governed by write-path owner question 3, [“Who may accept source gaps and promote a release?”](corpus-write-path.md#risks-and-owner-questions), which recommends owner-only
decisions initially. Actions are: retry with another converter; accept as non-binding OCR; accept with named gaps (state `accepted_with_gaps`); mark as a coverage gap and exclude; attach an alternate official source. Retries and
alternate sources create linked attempts. Named-gap acceptance reruns mandatory gates on exact retained/excluded ranges and excludes unsafe text from both indexes. No action bypasses integrity gates, reprocess comparison, or owner
adjudication, or makes OCR binding.

Retain originals, maps, edit ledgers, reports, comparisons, recipes, and decision references with every referencing release, pin, hold, or live intake attempt. Indexed artifact-reference edges prevent fenced garbage collection
from deleting that closure. Do not export mutable intake queues or private audit rows.

## Subjects

Use one immutable `config.taxonomy_release` per country taxonomy version. Keep labels and parent edges inside that version. Validate acyclicity and compute its digest before publication. Add an indexed ancestor/descendant closure
table for filter expansion. A rename, split, merge, or hierarchy change creates a new version; an explicit crosswalk relates versions without changing old releases. Reject unknown version/key pairs instead of mapping them to the
latest vocabulary.

Reuse the source-only document and provision assignment shapes. Require taxonomy version, subject, assignment method and recipe, evidence reference/hash, nullable confidence, review state, and context role. Methods initially mean
official classification, deterministic rule, or human review. Confidence describes that method's support, not legal truth. Corrections append a new assessment set; include its ID in assignment uniqueness and pin it in the release.
Never mutate an earlier assignment.

Publish chunk-subject child rows with B-trees on `(taxonomy_release_id, subject_key, chunk_id)` and `(chunk_id, taxonomy_release_id, subject_key)`. A provision assignment takes precedence over inherited document classification.
Broad-document assignments provide candidate scope, not proof that every clause belongs to the subject. Record inheritance and unresolved scope explicitly.

Maintain assessment rows even when no positive assignment exists. States are assigned, unknown, unreviewed, and reviewed not-applicable; extend the source coverage enum where needed. An absent row means unknown. Report
unknown/unreviewed counts, taxonomy version, and denominators through `corpus_status` and `quality_gaps`. Never report missing tags as zero coverage.

The default subject search retrieves two labelled sets: approved primary matches and possible matches whose classification is unknown or unreviewed. Apply the same eligibility predicate to both dense and lexical arms before their
candidate LIMIT. Fuse within each set using the embedding design's contract. Return possible matches separately with a coverage gap, not as approved subject evidence. An explicit strict subject filter excludes possible matches and
reports that recall is limited by classification coverage. Multiple selected subjects use union semantics, including descendants.

Fetch amendment, repeal, and validity context after primary selection regardless of its subject. Label context roles. Reject vector copies and partitions per subject because laws have multiple tags. Subject filtering must not add
hidden recall loss through fixed unfiltered top-k or missing labels. Measure approximate retrieval against exact eligible-set retrieval.

## Parts, validity, and relations

Add immutable Silver records with surrogate ID, stable key, intake or build execution/recipe provenance, source observation, evidence hash, and review state. The write path owns execution and legal-effect resolution. Conversion
owns extraction from bytes to blocks; parts identifies, cites, tracks, and displays parts.

Parts use a separate `silver.part_revision`, not `provision_revision`. Phụ lục, Biểu mẫu, Mẫu số, tables, and attached-file parts are first-class revisioned units. Artifact bytes remain separate. Part revision keys combine the
document revision key and a revision-local part key. Cross-revision continuity needs a reviewed identity assertion, as for provisions. Release membership includes both. Use `silver.evidence_target` as the common validity target
type: one surrogate ID, document revision, kind, and exactly one UNIQUE provision-revision or part-revision reference. Validate kind, same-document parentage, and acyclic containment. Amendment targets, validity, text periods,
subjects, and chunks use that target ID; never encode a polymorphic local ID without its type. Preserve legacy provision keys.

| Part storage | Contract and indexes |
|---|---|
| `part_revision` | Parent part, document revision, kind/role, exact label/code/title/citation bytes and components, source order, authority, confidence, and gaps. UNIQUE `(document_revision_id, part_key)`; index parent/order. Provision containment may reference a part without changing provision identity. |
| `part_block` | Ordered paragraphs, tables, and image regions; exactly one owner, a part revision or table cell. Store ordinal, exact NFC text, artifact/page/block anchors, authority, recipe, confidence, review state, and fidelity gaps. UNIQUE owner/ordinal; index owner/order for bounded paging. |
| `part_table`, `table_row`, `table_cell` | Table block, key, caption, ordered rows, cell coordinates, positive row/column spans, and explicit header links. Cell-owned ordered blocks allow nested tables. Preserve empty/absent/unreadable states, units, footnotes, note links, printed formulas and cached values separately, instructions, and per-table/cell evidence. UNIQUE table/row and row/column; reject out-of-grid or overlapping spans. |
| `table_rendering`, `rendering_cell_span` | Recipe-versioned deterministic search text with byte ranges mapped to source cells. Preserve printed labels/units/notes and rendering hash. Index rendering/range and cell; do not infer headers or compute values. |
| `part_artifact_span` | Many-to-many part revision/artifact hash, role, source observation, page/block anchors, and nullable half-open byte span over the hashed artifact. Require `0 <= start < end <= artifact.byte_size` when known; unknown byte mappings retain anchors and a gap, never invented offsets for scans or compressed files. Index part/order and artifact/part. |

Artifact records retain declared byte size, media type, original/derivative role, source filename/URL, capture provenance, and hash. Publication validates all block/cell/target/artifact references and includes their closure in the
manifest. The [parts design](document-parts.md#files-and-table-fidelity) consumes conversion blocks and owns identification, citations, tracking, and display. Conversion's bounded XLSX adapter supplies cell-text blocks for search;
parts presents a spreadsheet as a file part with metadata, search hits, and the official-source download link, without rich rendering in the first release. Qualified XLSX extraction does not authorize application downloads. Format
qualification belongs to [conversion owner question 1](document-conversion.md#risks-and-owner-questions). Presentations have metadata and source link only. Reject a JSON-only part tree because targets and paging need indexed rows.

| Record | Storage contract and main indexes |
|---|---|
| `relation_assertion` | Acting document/provision revision, operator, exact evidence span, source authority, confidence, and observation time. Index acting revision and source observation. Retain conflicting assertions. |
| `amendment_target` | Child of one assertion. Target document identity or unresolved reference; nullable `evidence_target_id` for a provision or part; exact target citation and target-text span; whole/partial extent; resolution state. Index target document, target ID, and assertion for incoming/outgoing lookup. |
| `amendment_difference` | Immutable before/after revision and target links, assertion, difference recipe/hash, exact changed spans, and difference artifact reference. Index assertion and target/revisions; the write path supplies computed differences and the website owns display. |
| `validity_resolution` | Immutable reviewed resolution set, policy digest, evidence links, and explicit uncertainty. A release selects exactly one resolution set per evidence target. |
| `evidence_validity_interval` | Resolution ID, `evidence_target_id`, legal `daterange`, status, start/end certainty, and cause links. Index target/resolution and use a GiST range index. Reject overlapping definitive intervals for the same target in the same resolution. Applies equally to provisions and parts. |
| `evidence_text_period` | Resolution ID, target ID, selected provision/part text revision, legal range, and continuity evidence. Validate matching type/document and reviewed continuity. Prevent two definitive text selections for one target/date. Unmatched historical text remains a gap. |

Use half-open legal date intervals `[start,end)`. An unknown start is not negative infinity; an unknown end is not proof of indefinite force. Store endpoint certainty explicitly. PostgreSQL supports range containment and exclusion
constraints; `btree_gist` can combine scalar equality with range overlap ([range documentation](https://www.postgresql.org/docs/17/rangetypes.html#RANGETYPES-CONSTRAINT)). Keep observation timestamps as `timestamptz`. Evaluate
legal dates in Vietnam's timezone. Preserve intra-day source precision and flag unsupported date-only resolution rather than rounding it.

`as_of=D` selects the pinned release's resolution and text period containing D. It returns definitive status only for supported intervals. Unknown dates, missing text, competing assertions, or unresolved targets return gaps.
“Exact” means exact interval evaluation over recorded evidence, not complete knowledge of all law. Corrected observations create a later release; old releases keep their answers.

The owner-supplied 09/2020 Điều 25 case must resolve to the acting khoản 6 Điều 22 of 50/2024 and the target Điều 25 separately. Its prior interval ends at 2025-01-01; a repealed interval begins then. Do not invent the prior start
date. Child provisions inherit a reviewed whole-article repeal through explicit resolved targets. A partial-document badge cannot override a resolved provision interval. The other five clauses must not appear as amendments to that
target without their own target evidence.

Reject a JSON-only target field and document-only validity inheritance. Both prevent indexed target lookups and hide precisely the ambiguity the reader must see. Keep verbatim replacement text and before/after revision links; do
not generate consolidated text at query time.

## Vector and lexical storage

Recommend cosine hierarchical navigable small world (HNSW) indexes for scaling, initially `m=16`, `ef_construction=64`. Preserve Vietnam's exact scan until HNSW passes every accepted golden case. Build indexes only in isolated
candidate projections, not over all historical writer vectors.

| Decision | Recommendation | Rejected or deferred option |
|---|---|---|
| Dense index | HNSW, one embedding recipe per release | IVFFlat adds list training/tuning; choose it only if measured build cost outweighs the accepted recall/latency result. |
| Precision | Float32 storage for the first parity release | `halfvec` needs a measured precision comparison. Binary quantization changes candidate quality and is not the baseline. |
| Dimensions | 1,024 until the embedding design approves another recipe | Do not truncate vectors merely to fit memory. If Matryoshka truncation is adopted, record full/output dimensions, normalization, and truncation recipe and rebuild a separate release index. |
| Partitioning | Country by database, release by database, subjects by child tables | No shared historical vector index with release filters. No subject partitions. Revisit writer partitioning only after measured retention growth. |
| Lexical | Exact sparse inner-product ranking over eligible rows | Sparse HNSW adds approximation and another recall gate. An external search engine adds operations without evidence of need. |

Support the embedding sibling's 768-dimensional candidate beside the 1,024-dimensional baseline. Writer storage uses dimension-checked `vector` plus recipe identity; each snapshot uses fixed `vector(D)` or approved `halfvec(D)`.
Replace hardcoded SQL and release validators together, including [types.go:28](../../internal/corpus/release/types.go#L28). Half precision remains a separate experiment.

The [pgvector documentation](https://github.com/pgvector/pgvector#hnsw) describes HNSW's higher memory and build cost relative to IVFFlat. HNSW supports `vector` to 2,000 dimensions and `halfvec` to 4,000; sparse HNSW permits
1,000 nonzero elements. These are index limits, not type limits. Approve a compatible dimension/precision before changing the schema. Never mix serving recipes.

Require pgvector 0.8 or later and pin an exact tested extension version. Iterative scans continue filtered approximate scans until enough rows or a scan limit; they do not guarantee exact recall ([iterative
scans](https://github.com/pgvector/pgvector#iterative-index-scans)). Use transaction-local `hnsw.iterative_scan=strict_order`, bounded scan/memory settings, and a tested custom-plan policy. Put subject/date eligibility inside the
ordered scan using indexed EXISTS predicates. Verify the actual plan: outer filtering after a fixed candidate LIMIT defeats iterative replenishment. Use an exact distance scan for small eligible sets or candidate shortfall. An
exact fallback that cannot finish within the deadline returns a retryable error, not a silently incomplete success.

Keep `gold.chunk_lexical_vector.content_sparse` with its lexical revision. Preserve the 2^20 hash space and deterministic tokenizer contract. Recompute BM25 weights for the entire selected chunk set; never combine old and new
corpus statistics. Query vectors represent term presence in the existing implementation ([bm25.go:1](../../pkg/rag/lexical/bm25.go#L1)). Persist corpus statistics and recipe digest for audit and rebuild, without loading all source
text at serving startup. Use chunk-identity B-trees and exact `<#>` ranking. No lexical approximate nearest neighbor (ANN) graph is budgeted. If scans miss budget, benchmark term postings before exact scoring and require parity.

The following are calculated payload sizes and planning estimates per million chunks. GiB means 2^30 bytes. Payload formulas are `4D+8` for vector, `2D+8` for halfvec, `D/8+8` for bit, and `8Z+16` for sparsevec, where D is
dimensions and Z is nonzero entries ([type reference](https://github.com/pgvector/pgvector#reference)). Heap, page, and index overhead are extra.

| Component per million chunks | Payload GiB | Planning allowance GiB |
|---|---:|---:|
| Float32, D=1,024 | 3.82 | Dense heap 4.5–6; HNSW separately 5–8 |
| Half precision, D=1,024, alternative | 1.91 | Heap 2.5–3.5; HNSW separately 3–5 |
| Binary, D=1,024, alternative | 0.13 | Graph and retained original vectors are additional; not selected |
| Sparse, Z=200 assumption | 1.51 | Heap 2–3; no ANN graph |
| Text, provenance, validity, subjects, B-trees | Unknown | 12–23, including selected historical evidence |
| Selected serving snapshot total | Not measured | 24–40 |

Measure database working set on the selected release; do not extrapolate a launch RAM reservation from a million-chunk estimate. Budget one query embedder separately. Bound `work_mem` and index-build concurrency together.

## Releases, delivery, and retention

Reuse `ingest.corpus_release`, immutable `release_manifest`, versioned activation, and fenced retention records. The coded resolver acquires a durable pin before a local pool lease
([resolver.go:60](../../internal/corpus/release/resolver.go#L60)). Implement its persistent contract; do not replace pins with a process-local reference count.

The manifest binds country, revisions, acceptance receipts, conversion provenance, validity resolutions, parts, blobs, schema, taxonomy, assessment set, retrieval recipes, and compatible images. Add precision, extension version,
and membership digests ([ingest/schema.sql:453](../../sql/ingest/schema.sql#L453)). Freeze evidence contents; lifecycle state and activation remain mutable control data. Never change a published database to repair evidence.

Recommend logical release snapshot restore. The write path supplies a sealed candidate projection. Export that isolated database using `pg_dump` custom format, then restore into a new allowlisted database name. PostgreSQL supports
consistent logical dumps and selective/parallel restore ([pg_dump](https://www.postgresql.org/docs/17/app-pgdump.html), [pg_restore](https://www.postgresql.org/docs/17/app-pgrestore.html)). This is a logical archive, not a
portable copy of data-directory or HNSW files. Restore rebuilds indexes and must budget that work. Use separate restore capacity and attach its verified endpoint, or throttle a same-instance restore only after a serving-load
rehearsal proves sufficient headroom. Hosting owns that capacity mechanism.

Before readiness, verify hashes, counts, cross-schema closure, migration/extension compatibility, permissions, query plans, and evidence parity. Analyze and warm the restored database. Verify all source-file references resolve
through the release artifact manifest. Register snapshot reference only after checks pass; never derive a database connection from client-supplied release text.

Reject continuous logical replication from the mutable writer: it transports partial work and needs an additional publication protocol. PostgreSQL also excludes schema DDL and sequence state from logical replication
([restrictions](https://www.postgresql.org/docs/17/logical-replication-restrictions.html)). Reject direct table-by-table bulk copy into an active database. Bounded COPY may construct a candidate, but the published delivery unit is
the verified snapshot and manifest.

Writer activation compares desired-selection version and records old/new release plus audit identity. Local control verifies the ordered record and readiness, atomically advances applied selection, and acknowledges its
version/digest. Reject stale delivery; reconcile lost acknowledgements. After any restore, serving reconciles its active release with the writer's ordered activation records before it accepts traffic. Keep traffic and retention
deletion fenced across restart until the latest writer-authorized selection, activated manifests, identity keys, pins, and acknowledged holds reconcile beyond the recovered WAL. Reverify every retained database plus query-recipe
bundle. Missing authority or continuity keeps both fences closed; restored readiness or deletion claims authorize neither rollback nor deletion. Hosting owns the restore procedure.

The database supplies append-only catalog recovery records: monotonic sequence, prior digest, mutation, entity key, and receipt. Each authority commits the mutation and recovery-delivery intent together; acknowledge catalog
changes only after their records and referenced manifests/keys are durable in hosting-provided recovery storage independent of the database backup. Reconcile incomplete delivery before proceeding. Replay the verified checkpoint
and ordered tail, then issue fresh deletion fences; never reset sequence high-water marks from an old backup.

There is no cross-cloud transaction. Pin one local bundle through serialization; explicit follow-ups retain it. Bind pins to immutable evidence and recipe artifacts, and record dense-search availability separately.
Only one embedding model runs on the serving host at a time, under the [embedding runtime policy](embedding-models.md#migration-and-rollout-slices).
Before replacing the runtime, re-embed every release that stays in dense search, or retire older releases from dense search.
Re-embedding creates a new immutable release and explicit predecessor mapping; never rewrite a published database or silently substitute a named release.
Retired releases remain available through `document` and lexical search, with a visible gap. Dense search requires a matching query runtime (same model, tokenizer, recipe and dimensions). No dual-runtime capacity is reserved.
Drain in-flight dense requests before replacing the runtime. Missing runtime readiness fails dense requests explicitly; it cannot silently select another recipe or lexical-only behavior.

Online retention is the active release plus at least two prior releases, kept for at least 30 days, plus any release under an explicit pin or hold.
Retain each complete database, evidence closure, and recipe artifacts for that window; retention does not require a loaded old runtime. Retain verified archives for one year initially.
Preserve identity mappings and release tombstones beyond snapshot expiry. An expired release returns an explicit unavailable error. A retained release retired from dense search remains readable and lexically searchable with a visible gap.
Local retention serializes pins and fenced deletion claims. Writer deletion needs durable destination tombstone acknowledgements that block new pins; unreachable destinations block deletion. Protect blobs.

For snapshot size S and N online releases, serving storage is approximately `N × S`; a new candidate adds S plus restore scratch and write-ahead log (WAL). Physical replicas multiply that footprint. The writer adds its retained
revisions, vector cache, and ledgers independently. Content-addressed source blobs can deduplicate; logical release databases do not share heap or index pages. Do not budget database deduplication or archive compression before
measuring it.

## Serving queries and accounts

Use a release-scoped operations object. Cache immutable summaries by release ID, never just country. Measure these proposed warm 95th-percentile database budgets at 2 requests per second, with bursts of 8, under concurrent
application and query-inference load matching the hosting and embedding contracts. They exclude authentication, inference, HTTP transfer, and client rendering; they are not service claims.

| Tool | Queries and indexes | Database budget |
|---|---|---:|
| `guide` | Release/startup metadata and taxonomy vocabulary, normally cached; primary-key lookup on cold load | 20 ms |
| `corpus_status` | Precomputed release counts, coverage denominators, taxonomy and source summaries; release/subject/source B-trees | 50 ms |
| `quality_gaps` | Keyset-paged gap projection; `(kind, subject_key, id)` and document-key lookup; no whole-text aggregation | 100 ms |
| `search` | One eligible-set selection, dense and sparse arms, bounded fusion, batch evidence/validity/target hydration; HNSW, subject B-trees, date GiST, document metadata indexes | 500 ms total |
| `document` | Evidence/legacy key resolution, ordered parts/chunks, dated text and validity, incoming/outgoing relations, artifact handles; unique keys, `(document_revision_id, ordinal)`, target/source indexes | 200 ms |

Use pgx pools initially, without PgBouncer or read replicas. Cap all release pools together at 16 connections per server process, including overlap; cap auth at 4 and control at 2. Reserve 8 operator connections per PostgreSQL
instance. Two processes therefore need at most 52 allocated connections on an instance hosting all roles. Keep idle minimums zero and lazy-open retained-release pools. Add admission and statement deadlines; reject overload before
unbounded queues accumulate.

Use transaction-local settings so released connections cannot leak query tuning. Add read replicas only when measured read demand warrants their cost. A replica must prove the requested manifest before receiving traffic; never
retry a partial request on another release. Snapshot restore remains the writer-to-serving transport even if physical replication later supplies serving failover.

Keep auth, revocations, consent, account state, and future dossiers in a separate mutable account database with its own roles, migrations, backup, and authoritative writer. Dossiers store evidence keys, release references, and
citations, not copied corpus rows or query history. Corpus rollback must never restore auth. Account checks use current authoritative state and fail closed. Keep SQL bind values, question text, query vectors, and account
identifiers out of corpus snapshots, batch artifacts, statement logs, and external telemetry. Hosting must keep Vietnam serving data, control traffic, and account backups within its approved residency boundary.

## Capacity assumptions

The owner-reported AWS baseline in [hosting](vngcloud-hosting.md#workload-and-sizing) is the starting evidence, not proof of the new release layout's size. Measure Vietnam snapshots, indexes, working set, restore scratch and WAL before sizing.
Use one `s2-general-4x8` (4 vCPU, 8 GiB) and resize only from measurement, on a dedicated small vServer for PostgreSQL, one embedder, and the Go server. Grow only from measurement.
The approved Vietnam-now starting scope resolves the launch growth-envelope question; do not provision the later subject or six-country envelopes. Original source files stay in object storage, not on the serving disk.

The following snapshot estimates remain unmeasured planning envelopes, not launch reservations. Three times the subjects does not imply three times the data; the second row assumes three times the unique documents/chunks. Historical legal text may increase that multiplier.

| Scenario | Documents / chunks | One snapshot S | Three retained releases plus candidate, 4S |
|---|---|---|---|
| Vietnam now | 3,974 / 142,086 | 3.4–5.7 GiB | 13.6–22.8 GiB |
| Vietnam, 3× subject envelope | 11,922 / 426,258 | 10.2–17.1 GiB | 40.8–68.4 GiB |
| Six countries, later planning only | 8,820 / 381,955 | 9.2–15.3 GiB aggregate | 36.8–61.2 GiB aggregate |

Longer retention, pins, holds, scratch and WAL need additional space. The six-country row authorizes no AWS changes or relocation. Hosting owns the 30 GB SSD root, 50 GB data SSD, floating IP and vStorage backup quote.
Measure p95 at 2 requests per second, bursts of 8, with concurrent PostgreSQL and application load and one embedder. Hosting must prove at least 25% free headroom; [embedding owner question 1](embedding-models.md#owner-questions) owns latency.
Forecast writer growth as `initial bytes + sum(new revision bytes + vectors + ledgers)`. Sparse vectors can change corpus-wide even when dense vectors are reused. Measure tables, indexes, out-of-line text, nonzero counts, subject fan-out, restore time, and artifacts. Add capacity before peak disk exceeds 75% or memory pressure breaches the read budget.

## Interfaces

| Sibling | Required contract |
|---|---|
| [Write path](corpus-write-path.md) | Intake acceptance independent of builds; closed build membership, immutable outputs, computed amendment differences, resolved validity/targets, accepted gaps, candidate projection, and publication checks. It owns retries, execution locations, promotion orchestration, and retention execution. Database adapters provide fenced transactions, snapshots, ordered activation, recovery reconciliation, and pin/deletion fencing. |
| [Corpus workflows](corpus-workflows.md) | Country admission epochs, intake/batch/build execution scopes, leases, fenced acceptance, acceptance receipts, immutable attempt inputs, and durable launch/notification records. Workflows own topology, signals, queues, limits, and reconciliation scheduling; signals never replace ledger receipts. |
| [Document conversion](document-conversion.md) | Raw/normalized blocks, offset maps, reversible edits, quality metrics/thresholds/outcomes/reasons, comparisons, and the shared quarantine decision contract. Every activity reading untrusted bytes uses the converter sandbox queue `corpus-converter-v2`, including LibreOffice, MuPDF text serialization, DOCX/HTML/XLSX parsers, image decoding, and OCR request building. Structure building over blocks without raw-byte reads may use plain CPU workers. Storage adapters accept validated references, not raw-file parsing work. |
| [Embedding models](embedding-models.md) | Exact model/tokenizer/preprocessing digests, dimensions, precision, distance, optional truncation, query/bulk parity, dense plus lexical fusion without a reranker, and quality floors. One recipe per release. Supply one matching ready runtime for dense search; re-embed retained dense-search releases or record explicit dense-search retirement before replacement. |
| [Document parts](document-parts.md) | Typed part revisions, parent links, exact citations, ordered blocks, table rows/cells/spans/header links, rendering-to-cell mappings, artifact byte spans, authority, gaps, and common validity targets. Parts share release membership and subject associations. This sibling consumes conversion blocks to identify, cite, track, and display parts; it owns no extraction from bytes or redlines. |
| [VNGCloud hosting](vngcloud-hosting.md) | PostgreSQL 17, tested pgvector, local control/account storage, authenticated sockets or TLS, separate runtime/restore roles, bounded restore capacity, archives, and off-host recovery. Reconcile ordered activation after restore before traffic. Prove CPU/memory headroom for one query runtime and measure extension support, disk latency, throughput, and cost. |

For downloads, the database supplies pinned artifact identity, declared bytes, hash, and membership. Hosting defines configurable positive limits for artifact bytes, per-client and overall concurrent streams, and stream timeout.
Serving checks declared size before streaming and enforces actual bytes while streaming.

## Migration slices and verification gates

Use GitHub-hosted isolated PostgreSQL with synthetic credentials. Extend the existing [generation lane:21](../../.github/workflows/corpus-source.yml#L21) and [replay checks:72](../../.github/workflows/corpus-source.yml#L72).

| Slice | Change | Independently verifiable exit |
|---|---|---|
| 1. Persistence baseline | Generate existing source-only migrations/stores before new adapters. Preserve all committed migration bytes and checksums. | Clean and upgrade replay match canonical tables, functions, triggers, indexes, and grants. Run schema tests against an isolated migrated database. |
| 2. Intake, identity, and membership | Add execution ledger, admission epochs, leases, receipts, revision acceptance, membership, revised uniqueness, and legacy maps. | Accept intake and OCR results without any release build; reject stale epoch/fence and changed replay payload. Recover commit-before-launch/notification crashes without duplicate acceptance. Reprocess candidates stay ineligible before comparison/adjudication. Preserve country isolation, durable keys, metadata identity, and ambiguous mappings. |
| 3. Conversion provenance | Add immutable artifact/map/ledger/report/decision storage and indexed reference closure. | Byte-exact reversal, many-to-many mapping round trips, explicit unknown original offsets, stale candidate/report/fence/decision rejection, and idempotent delivery. Named-gap acceptance preserves scope and OCR authority. Pins/holds and live attempts prevent deletion of referenced provenance artifacts. |
| 4. Parts, subjects, and temporal evidence | Add part/block/table/artifact storage, common targets, amendment differences, taxonomy closure, assessments, text periods, and validity resolutions. | Round-trip nested tables, merged cells, ordered blocks, and artifact byte spans; reject invalid references/ranges. Test a replaced appendix, one repealed form with unchanged siblings, date boundaries/conflicts, and exact stored before/after differences. Preserve multi-label/unknown, taxonomy rejection, and article-repeal fixtures. |
| 5. Release storage | Restore isolated candidates and implement durable activation/pin/retention adapters and catalog recovery records for complete bundles. | After model promotion, prove re-embedded dense search and old named-release `document`/lexical access with a visible dense-search gap. Missing matching runtime or insufficient single-runtime capacity blocks dense readiness. Restore a backup older than an activation, identity key, and acknowledged hold; traffic and deletion stay fenced until continuity and ordered reconciliation, including missing journal/writer cases. Reject restored deletion claims and interrupted journal acknowledgements. Pin/deletion races preserve retained evidence and recipe artifacts; rollback leaves auth epochs unchanged. |
| 6. Serving queries | Generate query adapters and release-scoped projections. | Exact eligible-set oracle versus filtered HNSW, lexical parity, bounded paging, deterministic ties, cross-subject context, and all five MCP tool contracts pass. Record EXPLAIN ANALYZE plans and memory/latency distributions. |
| 7. Measured Vietnam baseline | Use a protected hosted job to import an approved fixed corpus snapshot into isolated PostgreSQL. Do not connect the job to mutable production. | Exact citation/key mapping, dense/sparse counts, real-document validity/target checks, accepted retrieval floors, storage measurements, restore and rollback reports. Publish commit and workflow evidence. |

Append migrations; never edit a shipped migration to match canonical SQL. Generation changes that need explicit function/trigger replacement require a reviewed new migration. Do not rewrite live sections, chunks, citations, or
vectors during adoption. Preserve the accepted baseline and measure every rebuild against it. Synthetic CI proves storage behavior, not legal accuracy or VNGCloud capacity. Promotion and domain cutover require the write-path and
hosting gates separately.

## Risks and owner questions

Snapshots and BM25 revisions can dominate storage. Historical coverage and subject fan-out are unknown. HNSW row counts do not prove recall. Exact lexical scans may breach latency targets. Lost identity catalogs cannot be
reconstructed safely from titles. Schema cannot repair attribution. Local control failure can block pins despite healthy snapshots; include control in recovery planning.

1. Approve one year of archives beyond the online retention minimum? Recommend one year, including recipe artifacts needed for restore. Online retention remains active plus at least two prior releases for at least 30 days, plus explicit pins or holds. Price longer dossier guarantees separately.
2. Should subject filters include unclassified evidence? Recommend a separate possible-match set by default. Strict filtering must warn about coverage.
3. Which corpus recovery targets should hosting prove? Recommend no lost activated manifests, identity keys, or acknowledged holds, four-hour restore, and at most 24 hours of unpublished work lost. Auth recovery remains [hosting owner question 1](vngcloud-hosting.md#risks-and-owner-questions).

Unverified: per-release storage sizes, live extension versions, traffic, restore speed, hardware latency, VNGCloud limits and backup cost, historical coverage, and owner-supplied legal examples. No runtime or deployed checks ran. Add the
documentation-index link on adoption; this task permits only this file.
