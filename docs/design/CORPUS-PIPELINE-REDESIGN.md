# Corpus and workflow redesign

Keep the lean read service. Redesign the write path around immutable evidence revisions, bounded
processing, and validated corpus releases. Pilot Temporal for durable coordination after those
boundaries exist. Vietnam goes first; subjects remain associations within each country's corpus.

The owner approved source implementation. The [implementation plan](../plans/corpus-redesign-source.md)
records the completed source and pending verification gates. Runtime, database, corpus, and
deployment claims still require the hosted gates below.

Status: source core complete; persistence and runtime verification pending.
The authenticated source passed hosted checks at `30b1e9f`, is merged into `master`, and remains
undeployed. This design changes no running
service, schema, citation, or ranking behavior. [Current architecture](../ARCHITECTURE.md) and
[current pipeline](PIPELINE.md) remain authoritative until implementation and verification.

Preserve the five evidence tools: `guide`, `corpus_status`, `quality_gaps`, `search`, and `document`.
The service supplies evidence; it does not add an answer model.

## Findings that drive the design

These findings come from source review. Performance impacts need hosted measurement.

| Finding | Evidence | Consequence |
|---|---|---|
| Normalization deletes sections; indexing replaces chunks and commits before optional embedding. | `sql/silver/queries.sql:136`, `sql/gold/queries.sql:25`, `pkg/pipeline/index_activities.go:401` | Stage completion does not define a consistent published corpus. |
| Source captures and selected text overwrite prior values. Document identity depends partly on normalized source metadata. | `sql/bronze/queries.sql`, `sql/silver/queries.sql:186`, `pkg/pipeline/process_activities.go:1400` | Current hashes do not reconstruct the complete lineage of previously served evidence. |
| Batch helpers log item failures and return success. The fetch ledger already owns leases and source retry timing. | `cmd/pipeline/main.go:648`, `cmd/pipeline/main.go:702`, `pkg/pipeline/fetch_activities.go:69` | A coordinator needs explicit completion results, not command exit status alone. |
| Parallel embedding accumulates chunk text; OCR loads the selected scans before processing. | `pkg/pipeline/embed_all.go:285`, `pkg/pipeline/ocr_all.go:73` | Streaming provider I/O alone does not bound worker memory. |
| Related search embeds the raw query after primary search embeds its formatted query. Positive result counts lack a central maximum. | `pkg/rag/retrieve/evidence.go:432`, `pkg/rag/retrieve/retrieve.go:625`, `pkg/mcp/mcp.go:513` | Standard search can perform duplicate inference; callers can request excessive work. |
| Serving composition imports pipeline and source packages. | `pkg/app/app.go:23`, `deploy/containerfiles/Containerfile.ecs.server:29` | Read builds inherit extraction dependencies even though read requests do not need extraction. |

The current shared ONNX embedder, inference semaphore, disabled CPU arena, indexed hybrid retrieval,
and absence of a request-time reranker are strengths. Preserve them. [Recorded measurements](../../PLAN.md)
include about 2.1 GB of model private resident memory. That historical value is not a VNGCloud
capacity estimate. A new country must not load another model copy.

## Architecture choice

| Option | Benefit | Cost | Decision |
|---|---|---|---|
| Strengthen the PostgreSQL ledger and direct cloud runner | Least operational change; reuses existing claims and retries. | Long waits, resumption, and run inspection still need custom coordination. | Keep as the baseline and recovery adapter. |
| Add bounded Temporal coordination over the ledger | Durable phase order, timers, cancellation, and worker routing. | Adds a service, replay compatibility, and operating cost. | Recommended pilot after stage and publication contracts exist. |
| Add a Temporal workflow for each artifact | Fine task visibility. | Duplicates existing task ownership and increases workflow history volume. | Do not adopt for this phase. |

The proposed runtime boundaries are:

```text
One domain -> website / MCP -> shared evidence service -> selected corpus release
                         |                          -> shared query embedder
                         -> separate mutable auth database

GitHub -> release checks and refresh trigger -> Temporal coordinator
                                               -> bounded cloud workers
                                               -> country writer database + artifact store
                                               -> candidate -> evaluate -> publish -> activate
```

Search and authentication do not depend on Temporal availability. Corpus workers have no auth
credentials. Serving has no corpus write credentials. Temporal persistence, candidate processing,
and bulk inference stay off the constrained serving host. Keep AWS during the VNGCloud transition,
with one authoritative writer for each corpus and for auth.

## Schema: identity, revisions, and publication

Keep `ingest`, `bronze`, `silver`, `gold`, and `config`. Add missing entities within those boundaries
instead of renaming every schema. Names below are proposed responsibilities, not migration-ready DDL.
Keep one primary-key column per table, natural uniqueness constraints, append-only migrations,
queryable child tables, and the existing rule against cross-schema foreign keys. Publication checks
must verify every cross-schema reference.

| Layer | Proposed records | Invariant |
|---|---|---|
| `ingest` | Run, build, stage result, external job; existing fetch ledger | Work has a bounded input manifest, recipe, outcome, and retry owner. |
| `bronze` | Source record, observation revision, artifact and observation-artifact links | A new observation never overwrites the bytes or metadata used by an accepted revision. |
| `silver` | Stable document identity, identity assertions, legacy mapping | Titles, document numbers, source aliases, and merge decisions can change without reminting identity. |
| `silver` | Document revision, provision revision, validity and relation evidence | Each derived result identifies its immutable inputs and parser and policy versions. |
| `config` and `silver` | Taxonomy release; document and provision assignments | Assignments record taxonomy, method, evidence, confidence, and review state. |
| `gold` | Chunk set, vectors, lexical revision, published evidence projection | Every row belongs to one build and a declared retrieval recipe. |
| `ingest` control records | Corpus release, release manifest, activation record | Only a validated release can become the serving selection. |

Mint a durable document UUID once and expose a country-qualified evidence key. Keep existing numeric
IDs through explicit compatibility mappings; do not replace MCP fields silently. Seed mappings from
the accepted baseline and known historical mappings. A reused or ambiguous historical ID must return
an explicit ambiguity or unavailable result, not silently resolve to a different document.

Separate three concepts: source observation time, processing revision, and legal effective time.
Fetching a changed page or changing a parser creates a new revision, not a new law. A source assertion
does not prove legal validity. Record identity merges and splits with evidence and review history.

Every provision revision retains its exact native citation and source anchor. A stable provision
identity across revisions is optional and requires source evidence or a reviewed match. Renumbering
or similar text alone cannot establish continuity. Chunk IDs remain build-local. Keep an immutable
revision reference so an old citation can still identify the text that produced it.

Content-addressed blobs hold original files, payloads, extraction results, and external-job manifests.
Record hash, size, media type, capture provenance, and retention references in PostgreSQL. Repeated
observations can reuse a blob. Stage outputs use an idempotency key derived from immutable inputs,
stage, and recipe version. The recipe includes relevant configuration and code versions.

Embedding reuse requires the exact embedding input hash and model, tokenizer, preprocessing recipe,
and dimensions. An old cache entry with incomplete provenance is reusable only after compatibility
is demonstrated. Build sparse vectors against one recorded lexical model for the entire release.

Subjects do not create copied texts or separate databases. Assign at document level first, then at
provision level for broad laws. Publish indexed chunk-subject associations for filtering. Keep unknown
and unreviewed coverage visible. Validity and amendment context may cross subjects with an explicit
context role. Preserve the banking scope in the [product proposal](ASEAN-BANKING-MCP.md).

## Input processing and Temporal ownership

Use this data flow: discover, capture, deterministic extraction, normalize, chunk, dense and lexical
indexing, evaluate, publish. OCR remains a separate fallback batch and always produces non-binding
evidence. Reuse existing source gates, source priorities, relation backfill bounds, and citation rules.

Each stage accepts IDs or a manifest reference plus a recipe. It commits outputs and completion state
together, and returns counts for success, pending retry, terminal gap, rejected input, and failure.
Zero claims does not mean complete when leases or future retries remain. Every discovered item must
be accounted for. A release may retain reviewed source gaps; a failed processing stage cannot silently
become successful coverage.

Close each build's input set at a recorded discovery and relation-backfill cutoff. Later observations
belong to another build. Bound the refresh deadline and retry budget; unresolved work leaves the
build incomplete unless its gaps receive explicit acceptance under the coverage policy.

Bound every batch by item count, input bytes, document pages, elapsed time, and concurrent operations.
Acquire capacity before downloading or allocating large buffers. Page database rows with durable
cursors, spool bounded manifests to object storage, and stream result application. Enforce parser
timeouts, decompressed-size limits, and temporary-storage limits. Record oversized or malformed files
as explicit gaps. Preserve MuPDF serialization. Expensive extraction runs in constrained worker
processes so one file cannot exhaust serving resources.

Start with one `CorpusRefresh(country, run_id, build_id, recipe)` workflow and bounded stage activities.
Do not create a workflow for every chunk or artifact. The following split has one owner for each
retry decision:

| Owner | Responsibility |
|---|---|
| Temporal | Phase order, refresh schedule, durable waits, bounded activity retries for worker or infrastructure failure, cancellation, routing. |
| PostgreSQL ledger | Artifact claims, lease fencing, source retry eligibility, dead letters, immutable outputs, external-job identity, completeness. |
| Stage worker | Process a bounded shard, renew valid leases, commit conditional on the current fencing token, report outcomes. |
| Publication controller | Validate the manifest and compatibility, serialize activation, retain rollback evidence. |

For example, a source throttle updates the ledger's next eligible time. `DrainFetchShard` returns the
pending count and next eligible time. Temporal waits and schedules another bounded drain. It does
not independently retry that source row. An activity retry after a worker crash reclaims only eligible
work and reuses committed outputs. A stale worker cannot commit after another attempt owns its lease.

Commit outputs before reporting success. If the response is lost, retry returns the existing result.
Use an idempotent run-start record and a deterministic Workflow ID to reconcile a trigger that loses
its response. Fence the direct runner before Temporal owns a refresh. Keep the direct runner for
isolated repair or recovery, never as a concurrent scheduler for the same build.

Long OCR and embedding work uses prepare, submit, check completion, and apply steps. Persist a stable
submission key before calling the provider. After a crash, reconcile the provider job using that key
before submitting again. If the provider cannot support reconciliation, mark the submission uncertain
for operator review. Cancellation records outstanding jobs and attempts provider cancellation; it
does not promise to undo a completed external operation.

Use short status activities and durable workflow timers for long provider waits. Heartbeat bounded
activities while they perform work. Workflows carry only IDs, hashes, cursors, counts, and object
references. Keep source text, vectors, credentials, and query histories out of Temporal history.
Temporal records activity payloads and expects retry-safe implementations; these boundaries are our
application of its [Activity guidance](https://docs.temporal.io/develop/go/activities/basics).

Separate worker capacity for network fetch, CPU extraction, database writes, external OCR, bulk
embedding, and exclusive lexical build or publication. Per-source limits apply across all workers,
not just one process. Start with measured fixed slots; resource-based tuning is not a hard memory
limit. [Temporal worker controls](https://docs.temporal.io/develop/worker-performance) supplement
container limits and bounded stage code.

Workflow code performs no direct database, filesystem, or network I/O. Use workflow timers and SDK
primitives. Checkpoint with Continue-As-New before history grows large, after active work and message
handlers finish. Keep the same business run and build IDs across the chain. See
[Continue-As-New](https://docs.temporal.io/develop/go/workflows/continue-as-new). Pin worker versions,
replay saved synthetic histories in CI, and use a reviewed versioning or patching strategy for changes
to running workflows. See [safe deployment guidance](https://docs.temporal.io/develop/safe-deployments).

Temporal previously left the project in commit `a1a0b40` when direct batch jobs became the deployment
target. The record does not establish a Temporal performance failure. Reintroduction must earn its
operational cost. The pilot compares recovery effort, refresh duration, history growth, worker usage,
and provider charges against the improved direct runner. Choose managed or separately hosted Temporal
only after checking cost, network access, retention, recovery, and provider availability.

## Immutable serving releases

Start with a separate serving database snapshot per Vietnam release. Keep one logical corpus per
country and one country writer database; candidate and serving copies are release instances of that
corpus. This preserves the current query and HNSW index shape during the first migration. It costs
extra database storage and promotion time. Measure both before adoption. Do not put all historical
vectors in one active index and assume a release filter is cheap.

The durable identity catalog and release control records live outside replaceable serving snapshots.
A release freezes text, validity evidence, relations, source URLs, coverage gaps, subjects, dense
vectors, and the lexical model together. Evaluate effective dates at the requested legal date using
the release's recorded evidence; do not freeze a permanently stale `is_current` flag.

Build a manifest that binds corpus hash, source artifacts, schema migrations, citation and chunking
recipes, model and tokenizer digests, dimensions, lexical revision, taxonomy, configuration, evaluation
artifacts, and compatible server and embedder images. Prepare and verify the complete serving bundle
before activation. Fail readiness on a mismatched bundle.

Releases with the same model recipe reuse one embedder. A model change needs either measured capacity
for temporary model overlap or a planned drain and restart. Do not assume the serving host can hold
two model copies. Retained snapshots need not all have open pools or loaded lexical models.

Activation uses a serialized, compare-and-set release selection and records the previous release.
Each server resolves that selection to a ready bundle and pins one bundle for the entire request,
including search, relation hydration, and document lookup. Drain old pools after in-flight requests.
During rollout, requests may use either complete release; do not claim an instantaneous switch across
all servers. If readiness fails, keep the previous selection. A failed activation needs reconciliation,
not a half-updated model or database.

Return an additive release identifier with evidence. Follow-up document requests can name the same
release. Retain referenced releases for a defined period, and report expiration explicitly instead of
substituting different text. Retention and garbage collection must protect active, rollback, pinned,
and in-flight references and coordinate with backups. Measure the peak cost of overlapping releases.

Later, project frequently read evidence fields into Gold so serving need not aggregate Bronze and
Silver text. Preserve unknown validity, provenance, and gap details in that projection. Approve each
query change through evidence parity and query-plan measurement. Database snapshots come first;
generation partitions are a later option only if measured storage or promotion cost justifies them.

Auth has its own database, backup lifecycle, and authoritative writer. Corpus promotion or rollback
never restores old sessions, grants, blocks, or authorization epochs. Auth failure stays fail-closed.
Current account-state checks remain independent of the selected evidence release. A rollback server
must support the current auth schema and enforcement rules before it receives traffic.

## Preserve and improve read efficiency

Use one formatted query vector across primary, non-current, and related retrieval. The related path
currently uses different input text, so validate ranking and evidence effects before accepting the
change. Preserve explicit embedder failure handling and the existing retrieval quality floors.

Add shared limits before retrieval for query length, result counts, related counts, filter counts,
and filter lengths. Give serving inference a single-query interface and acquire admission before
tokenization and tensor allocation. Bound the waiting queue and return a retryable overload response.
Batch inference belongs to write workers. Set limits from observed clients and hosted measurements.

Set process-wide connection budgets for corpus and auth pools, including temporary release overlap.
Measure auth lock waits before changing introspection transactions. Immediate blocks and revocation
must survive any optimization. Avoid broad text aggregation for a few selected hits where a validated
published projection can supply the same evidence.

Measure cold and warm latency, CPU time, Go allocation, native resident memory, inference queue wait,
pool wait, database buffers, temporary spill, and lock wait. Test steady traffic, bursts, limit
boundaries, auth delay, embedder failure, and a simultaneous candidate build. Use fixed snapshots and
the same target hardware. Shared GitHub runners provide regression evidence, not a capacity promise.
Choose resource ceilings after baseline runs; do not trade citation accuracy or quality floors for
lower resource use.

## Repository boundaries

Keep one Go module and thin commands. Move packages when their responsibility changes, not as a
standalone mass rename. Proposed destination:

```text
cmd/{server,embedder,pipeline,corpus-worker}/
internal/app/{serve,embed,corpus}/
internal/access/{identity,oauth,web}/
internal/evidence/{api,service,mcp,postgres}/
internal/corpus/
  run/                         direct runner and stage result contract
  stage/                       discover, fetch, extract, normalize, index, publish
  source/<source>/             transport and source-native decoding
  jurisdiction/<country>/      identity, citations, validity, relation rules
  workflow/temporal/           coordinator and activity adapters
  blob/                        artifact interfaces and storage adapters
internal/store/generated/      sqlc output behind consumer-owned adapters
internal/platform/             shared config, database, logging
sql/{auth,config,ingest,bronze,silver,gold}/
deploy/                        images and provider-specific infrastructure
```

Website and MCP adapters depend on the evidence API. Evidence code cannot import workflow, source,
extraction, or bulk embedding packages. The shared stage code calls source and jurisdiction interfaces;
it does not compare country names to select parsing rules. Temporal SDK types stay in its adapter.
Serve, embedder, and corpus workers get separate composition roots and images.

Move generated code only through sqlc configuration and hosted regeneration. Retain a public Go
package only for an explicitly supported Go API; preserve HTTP and MCP compatibility throughout.
Remove `WithoutTemporal`, unused orchestration types, and stale example configuration as their callers
move. Keep the product rename, module path change, and domain cutover separate from structural work.

## Hosted proof and delivery order

GitHub owns builds, generation, tests, evaluation dispatch, images, and release evidence. Cloud workers
own corpus execution. GitHub triggers durable refreshes and links their outcomes; a long-running CI
job is not the corpus coordinator. Reuse jobs with workflow calls and repeated setup with composite
actions. Keep an always-reported merge gate and separate release and refresh privileges.

| Phase | Deliverable | Exit evidence |
|---|---|---|
| 1. Baseline | Keep the reviewed Google-access change separate; add hosted corpus migration, generation, replay, and performance lanes. | Clean and upgrade migration replay, exact citation fixtures, current query plans, resource baseline. |
| 2. Boundaries | Split composition; bound stages and requests; reuse one query vector after evaluation. | No worker dependency in serving, retry-safe synthetic runs, explicit completion results, read measurements and evidence parity. |
| 3. Revisions | Add durable identities, immutable observations and derived revisions, build manifests, legacy mapping. | Every accepted reference resolves; ambiguous mappings remain explicit; no live citation rewrite. |
| 4. Publication | Build an isolated Vietnam candidate and activate an immutable serving bundle. | Dense and lexical coverage, provenance and gap parity, unchanged evaluation floors, failed-promotion and rollback rehearsals. |
| 5. Temporal pilot | Coordinate those same stages in an isolated build; test retries, leases, cancellation, external-job recovery, and history rollover. | No duplicate accepted output, no dual writer, replay compatibility, bounded resource and cost report. |
| 6. Subjects and rollout | Publish reviewed assignments and measure subject filters; repeat country gates before expansion. | Unknown coverage visible, cross-subject context correct, country-specific golden evaluations pass. |

PR checks use synthetic inputs and disposable PostgreSQL. Temporal integration uses a pinned hosted
test service. Real-corpus checks use protected workflows and isolated versioned data, never mutable
production inputs. Fault tests must include a crash after database commit, lease expiry while a worker
is alive, uncertain external submission, a delayed retry, and failure during activation.

Each phase follows [AGENTS.md roles and dispatch](../../AGENTS.md#roles-and-dispatch) and
[Git rules](../../AGENTS.md#git). Update `AGENTS.md`, `CLAUDE.md`, topic instructions,
architecture, configuration examples, and runbooks in the phase that changes their facts. Current
package instructions remain in force until then.

The owner must settle the schema and publication pattern before implementation. A later pilot must
settle Temporal hosting, numeric resource budgets, release retention, and recovery targets. No current
evidence justifies a full rewrite, a live reindex, or a new serving capacity claim.
