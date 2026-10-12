Proposal. Not approved, not implemented.

# Corpus workflows

## Goal and limits

Make ingestion and repair independent of release assembly, with source, document, job, release, destination, and reprocessing workflows.
The database ledger owns evidence and work state. Temporal executes work and records its progress.
[Corpus write path](corpus-write-path.md) owns stage contracts, writer placement, delivery, failures, and command migration.
[Document conversion](document-conversion.md) owns format conversion, normalization, structure rules, and gates.

Owner inputs: Tra cứu quy định at `tracuuquydinh.vn` serves Vietnam from VNGCloud in Vietnam.
The five other country services remain unchanged on AWS and their existing hostnames.
Keep `guide`, `corpus_status`, `quality_gaps`, `search`, and `document`.
No answer model, generative model, or reranker runs anywhere in this design. Retrieval uses dense plus lexical fusion.
Only the query embedder is a served model; it runs on our own host.
Public legal documents may enter approved cloud OCR and bulk embedding. User questions never enter this path.
Vietnam questions are Vietnamese with diacritics or a document number; the serving layer validates the question before any embedding or corpus query. English is interface text only.

Vietnam corpus generation runs on isolated VNGCloud writer and worker capacity in Vietnam, orchestrated by Temporal Cloud. The five existing country paths stay on AWS.
The [write-path decision](corpus-write-path.md#decisions) keeps one fenced writer per country and isolates Vietnam generation from the small serving host.
The owner's cross-design decisions remain binding. This file grants no execution or deployment approval.

## Current state with evidence

“Coded” means source inspected. Recorded validation is not a new real-document check. No deployed state was verified.

| State | Evidence | Consequence |
|---|---|---|
| Coded pilot | Eleven stages form one `CorpusRefresh`: [types.go:51](../../internal/corpus/workflow/temporal/types.go#L51), [workflow.go:24](../../internal/corpus/workflow/temporal/workflow.go#L24). Activation runs inside it: [workflow.go:97](../../internal/corpus/workflow/temporal/workflow.go#L97). | Replace topology, not the fenced domain contracts. Separate promotion. |
| Coded contracts | Execution and results require a build: [run/types.go:69](../../internal/corpus/run/types.go#L69), [ingest/schema.sql:240](../../sql/ingest/schema.sql#L240). Atomic apply checks the fence: [apply.go:44](../../internal/corpus/run/apply.go#L44). | Add document-attempt and batch ownership contracts before persistent intake. |
| Coded external-job port | Prepare, submit-or-reconcile, check, apply, cleanup: [types.go:377](../../internal/corpus/workflow/temporal/types.go#L377). | Reuse reconciliation semantics for shared batch jobs. |
| Coded workers and replay API | Resource queues exist: [worker.go:47](../../internal/corpus/workflow/temporal/worker.go#L47). Real SDK replay API exists: [replay.go:11](../../internal/corpus/workflow/temporal/replay.go#L11). | Registration is not a deployed worker or a captured history. |
| Pending persistence | Adapters and executable workers remain gated: [persistence rollout](../plans/corpus-redesign-source.md#task-7-generated-persistence-rollout-deferred), [Temporal pilot](../plans/corpus-redesign-source.md#task-8-actual-temporal-sdk-pilot-source). Hosted generation source exists: [corpus-source.yml:14](../../.github/workflows/corpus-source.yml#L14). | First slice must prove database writes in isolated hosted CI. |
| Coded legacy path | Direct sequencing remains in [main.go:528](../../cmd/pipeline/main.go#L528); section deletion in [queries.sql:136](../../sql/silver/queries.sql#L136); chunks commit before optional vectors in [index_activities.go:401](../../pkg/pipeline/index_activities.go#L401). | Do not call these mutations from new workflows as if already fenced. |
| Recorded validation | A forced VN rebuild changed chunks and reduced retrieval metrics in the [corpus quality record](../../PLAN.md#corpus-quality). Known validity and attribution defects are recorded in [Phase 3](../design/website.md#phase-3-depends-on-the-corpus-redesign). | Require real-document gates and preserve the accepted baseline. |
| Deployment record only | Direct AWS generation and serving are described in the [write path](../ARCHITECTURE.md#write-path). | Preserve running services until separate cutover proof. |

This proposed topology refines [input processing and Temporal ownership](../design/corpus-pipeline-redesign.md#input-processing-and-temporal-ownership); current source does not implement it.

## Decisions and ownership

| Decision | Recommendation | Rejected option and reason |
|---|---|---|
| Workflow boundary | Seven pipeline workflow types plus bounded country control reconciliation | A release-wide stage loop blocks intake on unrelated files and human decisions |
| Document ownership | Ledger owns version, attempt, retry eligibility, quarantine, accepted outputs, and completion | Workflow-local completion as corpus truth creates competing ledgers |
| External jobs | Shared `OcrBatch` and `EmbedBatch`, grouped by country, provider, recipe, and bounded manifest | A provider job per document or chunk wastes batch capacity |
| Long waits | Durable timers and decision signals carrying ledger references | Worker polling loops reserve capacity while no work can run |
| Deployment | Pinned Worker Deployment Versions with replay gates | Auto-upgrade without replay can break documents waiting for days |
| Temporal service | Approved Temporal Cloud Developer, pay as you go with no minimum, with reference-only payloads | Self-hosting adds service/database operations to the first rollout |

Per-document workflows schedule eligible activities and wait. Workflow success cannot establish document acceptance.
An activity first reads committed results, then claims eligible work, computes, and accepts atomically.
The transaction validates country admission epoch, execution identity, lease owner, fence, inputs,
recipe, and decision revision. A stale worker writes nothing. A replay returns the committed receipt.
No mutable lease owner or fence enters the immutable idempotency key.

Use the [database intake ledger and revision acceptance contract](corpus-database.md#intake-ledger-and-revision-acceptance):
`DocumentIngest`, `OcrBatch`, and `Reprocess` execution records keyed by workflow ID and attempt fence, with no build ID.
Accepted revision provenance uses `intake_execution_id`; legacy `document_revision.build_id` is nullable import provenance.
The database owns revision acceptance state and eligibility. Only `accepted` or `accepted_with_gaps` is build-eligible.
Require country admission epochs, fenced intake/batch/control leases, durable decisions, launch/notification records,
and consistent snapshot cutoffs before slice 1. A build lease serializes assembly, not intake; never mint fake builds.
Physical tables and SQL remain with the database sibling. Block slice 1 until both designs state these contracts.

All names below are proposed activity contracts. `Apply*` means fenced atomic ledger acceptance.
Blob-producing activities stage immutable objects first; they never write accepted evidence directly.
`Apply*` commits output references, outcome, and durable notifications in one transaction.
The writer reconciles unacknowledged launch/notification records after crashes. Signals are wake-ups,
not the sole record of work. Use ledger receipts after Temporal history retention expires.

## Workflow contracts

IDs contain allowlisted country/source codes and opaque ledger keys or digests, never titles or URLs.
Each explicit start uses reject-duplicate reuse and use-existing conflict handling where supported.
Ledger uniqueness outlives Temporal's duplicate-start retention window.
Changed inputs or recipes create a new processing attempt; transport retries retain the original key.

| Workflow | Input and deterministic ID | Parent, output, and fenced writes |
|---|---|---|
| `SourceSync` | Country, source descriptor digest, policy, mode, budgets. Schedule/action base ID `source-sync/<country>/<source>/<mode>`; Temporal appends its action timestamp. Ledger key is `(schedule ID, scheduled time)`. Manual ID `source-sync/<country>/<source>/manual/<request-key>`. | Schedule starts a root. Starts `DocumentIngest` children. `ApplyDiscoveryPage` records captured pages, observations, launch intents, and cursor atomically. Returns window/coverage receipt, not document acceptance. |
| `DocumentIngest` | Country, source version reference, processing recipe, attempt, original reference for reprocess. ID `document-ingest/<country>/<version-key>/<attempt-key>`. | Child of `SourceSync` or `Reprocess`; recovery can reconnect by ID. `ApplyCapture`, `ApplyConversion`, `ApplyQuarantine`, `ApplyDecision`, `ApplyEvidence` own commits. Returns accepted revision/quality receipt or explicit terminal gap. No release selection. |
| `OcrBatch` | Country, provider, OCR recipe, durable collection key and positive budgets. ID `ocr-batch/<country>/<collection-key>`. | Independent shared root started from durable request records. `Prepare`, `SubmitOrReconcile`, `Check`, `Apply` produce provider receipt, page coverage, non-binding OCR output, and notification records. |
| `EmbedBatch` | Country, provider, embedding recipe, collection key and budgets. ID `embed-batch/<country>/<collection-key>`. | Independent shared root. Same four operations; `Apply` verifies input IDs, hashes, vector dimensions, and coverage. Notifies waiting builds after durable acceptance. |
| `CorpusBuild` | Country, base release, accepted-ledger watermark, source-closure reference, all recipes, evaluation policy. ID `corpus-build/<country>/<build-key>`, allocated once per request. | Root, triggered after source-window accounting or by owner request. `ApplySnapshot`, `ApplyRelationsValidity`, `ApplySubjects`, `ApplyChunks`, `ApplyLexical`, `ApplyEvaluation`, `SealRelease` use build fencing. Requests `EmbedBatch`. Returns sealed immutable release or failure report. |
| `Promote` | Country, release ID/digest, destination, expected selection version, promotion request. ID `promote/<country>/<destination>/<request-key>`. | Separate root. Waits for owner approval. `ApplyApproval`, `RecordReadiness`, `CommitSelection`, `RecordApplied` bind decisions and receipts. Delivers, verifies, activates, reconciles. Returns applied selection receipt. Rollback uses a new request selecting a retained release. |
| `Reprocess` | Country, immutable selection manifest, stored-original hashes, old/new recipes, request. ID `reprocess/<country>/<request-key>`. | Root starts `DocumentIngest` children with fetch disabled. `ApplyReprocessPage` records attempts and launch intents; `ApplyComparison` records before/after quality and changed citations/parts. Returns comparison and completion manifest. Never promotes. |
| `ControlReconcile` | Country, budget, control cursor. Schedule/action base `corpus-control/<country>` plus scheduled timestamp. | Maintenance root, no source parent. Country control lease fences batch/build launches, decisions, notification receipts, and promotion acknowledgement reconciliation. Returns bounded progress; never accepts evidence or approves promotion itself. |

### Source intake and child lifetime

One discovery Schedule per enabled source uses `SKIP` overlap, a 24-hour catch-up window, and daily
execution initially. A ledger source lease also excludes manual or duplicate schedulers.
Use the Schedule-generated Workflow ID and scheduled-start metadata as transport identity;
do not derive a new discovery window from wall clock on a retry. The ledger resumes incomplete windows.
Use [Temporal Schedules](https://docs.temporal.io/schedule), not Cron workflows.

Discovery reads bounded pages, stages source responses, then commits page facts and child launch intents.
A listing fingerprint reserves a source version key. Capture binds that key to the immutable artifact
set and metadata hashes. Unchanged captures reuse accepted evidence; changed bytes create a new version.
Periodic bounded rechecks detect files changed without a listing change. Absence never proves deletion.

Both `SourceSync` and `Reprocess` start children with `ABANDON` and await `ChildWorkflowExecutionStarted` before
recording launch acknowledgement or closing the parent. This is the documented
[asynchronous child contract](https://docs.temporal.io/develop/go/workflows/child-workflows).
This policy lets quarantined children outlive discovery. It does not abandon ledger accounting.
After Continue-As-New, old children do not become children of the new run. The ledger links every
child to the source window and records completion. New runs consult those links, not old futures.
Reconcile a lost start response by deterministic child ID before starting again.
If a closed execution failed before acceptance, allocate a recovery attempt linked to its predecessor.

`SourceSync` completion means discovery and launch accounting closed. It does not mean all documents
passed. Builds inspect accepted revisions and every pending/gap outcome at their snapshot cutoff.
Canceling discovery stops new launches. Canceling children requires explicit durable per-attempt
cancellation; unrelated or shared work survives. A separate per-source Schedule starts `SourceSync`
in reconciliation mode every 15 minutes, with `SKIP` overlap and no source fetches.
Source reconciliation scans only source/document launch rows. Country `ControlReconcile` runs every
15 minutes under its own lease/cursor, even with sources disabled, for source-less jobs and decisions.
Schedules use deterministic phase offsets and jitter; start/control requests stay below 10/second,
the [Cloud Schedule limit](https://docs.temporal.io/evaluate/cloud/limits). Backfills share that limiter.
Enable only after shadow approval; updates record policy revisions; pause stops intake, not recovery.
Delete source schedules only after pending intents transfer to country control and children are accounted for.
`Reprocess` persists selection-item → attempt → child links across rollover; a launched child is not completion.
Reprocessed revisions remain candidates, never build-eligible until `ApplyComparison` records a passing before/after
comparison and the owner adjudicates critical text changes under [write-path question 3](corpus-write-path.md#risks-and-owner-questions). Only then may fenced `ApplyEvidence` accept them.
Children wait on durable comparison/decision receipts; the parent compares staged candidates without waiting for acceptance.
Close the selection only after every item has a terminal comparison and acceptance receipt or explicit gap.

### Document processing and quarantine

`DocumentIngest` fetches and captures originals, detects format, calls conversion, normalizes blocks,
builds structure, gates the candidate, then commits accepted evidence.
Each step exchanges manifest references. Required files and parts must be accounted for.
OCR eligibility creates an `OcrBatch` request and a durable wait; no inline OCR or held activity slot.
After OCR, run normalization, structure, and gates again. OCR stays non-binding.

Use the conversion sibling's `DetectFormat`, `Convert`, `Normalize`, `BuildStructure`, and `QualityGate`
activities. Each envelope carries `{input_manifest_ref, digest, recipe_ref, budget_ref}`.
The final output carries `{block_manifest_ref, structure_ref, provenance_ref, gate_report_ref, outcome_code}`.
Gate outcomes are conversion's `accepted`, `accepted_with_gaps`, and `quarantined`; `needs_ocr` is a
conversion request, not acceptance. Only `ApplyEvidence` commits passed candidates and scoped decisions.
The [database provenance contract](corpus-database.md#conversion-provenance-and-decisions) links raw to normalized offset maps, the reversible edit ledger, quality report
(metrics, thresholds, outcome, reason codes), and review decisions to each document revision. Missing output fails.

On a failed gate, `ApplyQuarantine` records reason, candidate hashes, previous revision, decision epoch,
and due time; releases the processing slot; then the workflow waits on a signal and timer.
Signal `quarantine.decision.v1` contains `{decision_ref, expected_epoch, expected_report_hash}` only.
The shared decision contract is: decision ID, document version ID, quality report hash, workflow fence token,
actor, action, reason. The workflow fence token binds the execution's `attempt_fence`. Bind country, original/candidate hashes, exact retained/excluded ranges, replacement
recipe/source reference, and expiry too. Authority is the owner or reviewers the owner designates, under
[write-path question 3](corpus-write-path.md#risks-and-owner-questions). Keep actor identity outside Temporal history.
Persist before dispatch. `ApplyDecision` checks authority, fence, epoch, hashes, scope, and expiry under a lease.
Duplicate signals return the existing receipt; stale or mismatched decisions cannot release a new attempt.
Signal delivery is not acceptance; the owner reviews through AI agents calling the [review API](corpus-review.md#decisions) behind mutual TLS with a private CA. The human console is deferred. The API preserves the decision contract, opaque reviewer key, audit journal, and ledger decision receipt.

| Owner decision | Required result |
|---|---|
| Retry with another converter | New processing attempt from the same original; record recipe change and rerun all gates |
| Accept as non-binding OCR | Require OCR output and page/provenance coverage; rerun mandatory gates before `ApplyEvidence`; never elevate authority |
| Accept with named gaps | State `accepted_with_gaps` only after mandatory gates rerun on the hashed candidate and exact retained/excluded ranges. `ApplyEvidence` accepts independently sound spans and attachments; exclude unsafe provisions and excluded ranges from both indexes. Preserve gap locations and expected coverage. |
| Mark as a coverage gap and exclude | Terminal missing-evidence gap; close attempt with reason and scope, no accepted revision or fabricated text |
| Attach an alternate official source | Start separate intake for an allowlisted official observation; verify identity and capture new hashes. Reprocess retains its original and gap; never silently refetch. |

The first action records a new attempt and start intent, rather than changing an old attempt's recipe.
The control reconciler dispatches follow-up attempts; they remain linked to the original document.
Recommend a reminder at 24 hours and expiry at seven days. On expiry, atomically mark an unresolved
gap and close the workflow. Never auto-accept. A late decision creates a new linked attempt.
The timer and a simultaneous decision compete through the expected ledger epoch; only one commits.
Waiting holds no parser slot, database connection, lease, or open database transaction.

Visibility uses Keyword attributes `Country`, `Source`, `State`, `Reason`, `VersionKey`, `BuildKey`,
and Datetime `DecisionDue`. Example: `Country = 'vn' AND State = 'quarantined'`.
Upsert after committed transitions. Temporal visibility is an eventually consistent worklist;
the review API reads authoritative ledger state. Reconciliation repairs stale attributes.
Publish approved missing-document/part, binding-text, OCR-review, unresolved-target, and stale-version
gaps through `quality_gaps`, `corpus_status`, and affected evidence. Publication does not erase them.
Open quarantine affects refresh health separately from the immutable active release's evidence gaps.

### Shared batches, builds, and promotion

OCR uses Google Cloud Vision for gated public-document payloads. Private Vietnam vStorage holds originals and OCR outputs after verified upload and retention checks. The [OCR bake-off is optional later work](document-conversion.md#decisions). Bulk document embedding runs on Hugging Face Jobs, pay as you go, with Runpod Flex as fallback. Kaggle and GitHub Actions free tiers serve evaluation and bake-offs only.
Requests persist before a batch starts. A collection key groups one country/provider/recipe and
ledger-assigned window. Close after five minutes or a positive item/byte/page limit, whichever comes first.
Launch a batch only when requests exist. Never mix recipes or countries to fill a batch.
Initial item ceilings are 100 OCR documents or 1,000 embedding chunks; smaller resource limits win.
Prepare freezes membership. Submit records intent before provider I/O. Reconcile by stable submission key.
Uncertain submission stops automatic resubmission. Check uses timers from one to fifteen minutes.
Apply validates every expected item and writes output/notification receipts in bounded pages.
Missing results fail coverage even if the provider reports success. Preserve verified reusable results.
Notify waiting workflow IDs without a Run ID so Continue-As-New does not strand a signal.
Waiters reread receipts after waking and on a 15-minute recovery timer. Duplicate notifications are harmless.
Canceling one waiter does not cancel a shared job. Cancel the provider job only when no live demand remains.

`CorpusBuild` selects build-eligible revisions and coverage at one consistent ledger cutoff into its immutable manifest.
It computes cross-document relations, amendment differences in the relations and validity stage,
provision/part validity, subjects, chunks, dense vectors through `EmbedBatch`, lexical index, evaluation,
and immutable release. Required late targets enter a later snapshot, never the already frozen build.
The database stores amendment differences; [the website design](../design/website.md) owns redline display.
Document parts are first-class revisioned units. Provisions and parts share one validity target type.
The database supplies ordered blocks, table rows/cells/spans, and part-to-artifact byte spans.
An unaccepted quarantine or source gap blocks publication when coverage policy requires that item.

`Promote` waits for `promote.decision.v1 {decision_ref}` through the same durable decision delivery.
The decision binds release, destination, digest, expected selection, approval expiry, and owner authority.
Recheck approval before delivery and selection commit. Rejection or expiry leaves selection unchanged.
Approval starts delivery; only verified destination readiness permits compare-and-set activation.
Lost acknowledgements reconcile desired/applied versions. Rollback is another approved selection.
Do not restart `CorpusBuild` to promote. Auth state never travels in the release bundle.
After any restore, serving reconciles its active release with the writer's ordered activation records before
accepting traffic. A hosting restore never reactivates stale release control state; `ControlReconcile` repairs receipts.

Online retention is the active release plus at least two prior releases, kept for at least 30 days, plus any release under an explicit pin or hold.
Only one embedding model runs on the serving host at a time, under the [embedding runtime policy](embedding-models.md#migration-and-rollout-slices).
Before replacing the runtime, re-embed every release that stays in dense search, or retire older releases from dense search.
Retired releases remain available through `document` and lexical search, with a visible gap. Retention, pins, and holds still protect their evidence and recipe artifacts.
Dense search requires a matching query runtime (same model, tokenizer, recipe and dimensions). No dual-runtime capacity is reserved.
Size from measurement for PostgreSQL, one embedder, the Go server, auth, and the approval and audit service on a dedicated small serving vServer.
Start from `a-general-4x8` (4 vCPU, 8 GiB), with `a1-standard-2x8` as the cheaper fallback if measurement allows, under the [hosting decision](vngcloud-hosting.md#workload-and-sizing).
Measure p95 at 2 requests per second, bursts of 8, with concurrent PostgreSQL, auth, approval, audit, and application load; grow only from measurement.
The p95 target is owned by [embedding question 1](embedding-models.md#owner-questions).

## Fan-out, history, and resource limits

These are proposed ceilings, not throughput claims. See [Temporal Cloud limits](https://docs.temporal.io/evaluate/cloud/limits).

| Resource | Published Cloud boundary | Proposed bound |
|---|---|---|
| Pending children, activities, external signals/cancels | 2,000 each; recommended at most 500 | 100 children per parent run; 16 pending activities; 32 external notifications/cancels |
| Signals received | 10,000 per execution | Continue-As-New at 500; ingress coalesces repeated wake-ups |
| Updates | 10 in flight; 2,000 total | Use decision signals; no Update API initially |
| History | Warning 10,240 events/10 MB; hard 51,200/50 MB | Continue-As-New at 2,000 events, 4 MiB, or SDK suggestion, whichever comes first |
| Payload / transaction | 2 MB / 4 MB | 64 KiB encoded envelope; 32 references, then manifest indirection. Keep aggregate serialized commands/transaction below 3 MiB, including metadata overhead. |
| Namespace actions | Default floor 500 per second | Admission target below 100 actions/second; throttle from measured usage |

Start children in groups of at most 16; await all start acknowledgements before starting the next group.
`SourceSync` and `Reprocess` rotate after 100 child starts or 20 discovery/selection pages, sooner
at common history limits. Admit at most 32 processing documents and 256 open document workflows
per country across all parents. Quarantine counts toward 256, not 32; at the ceiling stop launches,
retain pending intents, alert, and keep the source window incomplete. Slots live in the ledger.
`CorpusBuild` rotates between committed shard pages. Batches rotate between polls/apply pages with
the same external job reference. `DocumentIngest` and `Promote` rotate while waiting if thresholds hit.
`ControlReconcile` completes each bounded sweep or continues from its durable cursor at the same limits.
No workflow holds more than one active external-job reference; multi-job work pages through the ledger.

Before Continue-As-New, finish handlers, checkpoint accepted cursors, and complete/cancel current
activities. Await every child-start acknowledgement; do not wait for quarantined children to finish.
Carry IDs, recipe, manifest, ledger cursor, deadline, and decision epoch only. Drain queued signals
or leave their durable inbox entries unacknowledged so reconciliation redelivers to the new run.
Do not initiate rollover inside a signal handler. Follow [Continue-As-New guidance](https://docs.temporal.io/develop/go/workflows/continue-as-new).
Validate envelopes including errors, headers, heartbeats, and continuations. Never include document bytes,
vectors, signed URLs, raw provider errors, tokens, questions, or operator names. Allowlist error codes.

| Vietnam task queue | Isolated VNGCloud pool and initial slots per process | Global admission |
|---|---|---|
| `corpus-coordinator-v2` | Go workflow workers, 2 workflow-task slots | Ledger caps above; no source or database I/O in workflow code |
| `corpus-fetch-v2` | Fetch/discovery controllers, 8 activity slots | Per-source token bucket: initial 1 request/second, burst 1, 2 connections; descriptor may lower it |
| `corpus-converter-v2` | Trusted sandbox supervisor, 1 activity slot | Every activity reading untrusted bytes: LibreOffice, MuPDF including text serialization, DOCX/HTML/XLSX parsers, image decoding, OCR request building, and converter-output parsing. Shared ceiling: 2 sandboxes per country. |
| `corpus-parser-v2` | Trusted normalization/structure controllers, 2 slots | Block-only structure may use plain CPU workers; delegate content parsing and normalization to the same sandbox through `corpus-converter-v2`. No raw-byte parser or native MuPDF process here. |
| `corpus-ocr-client-v2` | Provider controllers, 2 slots | 1 external OCR job per country initially; transmit prepared opaque requests, never decode images or build content requests |
| `corpus-embedding-client-v2` | Provider controllers, 2 slots | 1 external embedding job per country initially |
| `corpus-database-v2` | Writer-side acceptance workers, 4 slots | Country pool budget; one release build and one promotion per destination |

A shared ledger limiter applies source quotas across every worker, including direct recovery.
Honor `Retry-After`; Temporal infrastructure retries must not bypass source eligibility.
Require positive compressed/decompressed bytes, pages, memory, CPU, elapsed, output, and temporary-disk limits.
Conversion chooses sandbox limits. The shared 2-sandbox ceiling covers all delegated conversion, normalization,
structure, parts parsing, and converter-output parsing. Isolate each native MuPDF process. Deny sandbox credentials,
network, database, and Temporal access. Kill over-budget process groups; trusted adapters validate staged receipts.
Control activities have a one-minute attempt ceiling. Work activities use their bounded stage budget.
Heartbeat every ten seconds while running, timeout thirty seconds; no heartbeat during workflow waits.
Retry worker/transient infrastructure failure at most three times, backoff 1 second doubling to 30 seconds.
Source retries use ledger eligibility, at most five over 24 hours. Failed gates do not retry unchanged input.
Cancellation/deadline records unfinished jobs; a reconciliation sweep handles termination and worker loss.

## Versioning and replay

Register new names `corpus.source-sync.v2`, `corpus.document-ingest.v2`, `corpus.ocr-batch.v2`,
`corpus.embed-batch.v2`, `corpus.build.v2`, `corpus.promote.v2`, `corpus.reprocess.v2`, and `corpus.control.v2`.
Do not change the eleven-stage v1 history in place. Retain v1 registration and compatible workers.
Use one Worker Deployment with all seven resource queues and pin each execution to its image version.
Pin converter/parser recipes separately from workflow code. No mutable config reads during replay.
Keep old workers for quarantined documents and pending promotions; a new current version affects new work.
Keep child and continuation queues inside the same deployment so version inheritance remains defined.
Cross-deployment activities otherwise version independently. See [Worker Versioning](https://docs.temporal.io/worker-versioning).

Replay captured histories in GitHub-hosted CI before deploying workflow changes. Include success,
worker loss, failed gates, seven-day timer, duplicate and late decisions, source rollover with live
children, external uncertainty, cancellation, and lost promotion acknowledgement.
Use `GetVersion` only for a replay-tested compatible branch. A converter change creates reprocessing,
not a workflow patch that rewrites an old recipe. Emergency migration needs an explicit version
override after replay or a fenced replacement attempt. Preserve old histories and images for recovery.
A replay fixture must come from a hosted Temporal execution, not handwritten history JSON.
Use time-skipping unit tests for timer edges and a hosted service for captured-history and routing proof.

## Cost estimates

Public PAYG pricing is $50/million actions, $0.042/GB-hour active storage, $0.00105/GB-hour retained
storage, no base fee, and 10% Developer support: [pricing](https://temporal.io/pricing).
Storage follows open versus closed executions: [billing](https://docs.temporal.io/cloud/pricing).
Choose 30-day closed-history retention. Workflow history retention is separate from evidence retention.

An action is not a history event. Starts, activity attempts, server-recorded heartbeats, timers,
signals, and search-attribute updates contribute. Child initiation and Schedule starts add overhead;
worker replay is not billed as new work: [action definitions](https://docs.temporal.io/evaluate/cloud/actions).

Planning assumptions: 4,000 documents/VN rebuild, 36 chunks/document, 10% OCR, 5% quarantined seven days, 100 listing pages, one promotion.
Incremental day: 20 changed documents, 20 pages; rebuild release-wide relations/lexical data even then.
Reprocess all 4,000 originals with a changed converter, no fetch, worst-case all chunk vectors change.
Six-country sizing assumes 24,000 documents, not current inventory. Byte/page limits may shrink batches. Provider compute, VNGCloud worker and storage costs, transfers, and CI are excluded.

Action allowance per ingested document is 40: 3 child/start overhead + 18 activity attempts +
8 server heartbeats + 5 attribute updates + 4 timers/signals + 2 retry reserve.
Reprocess uses 36, saving 4 fetch/capture calls. Batch allowance is 50: 1 start + 4 lifecycle activities +
10 checks + 10 timers + 5 heartbeats + 10 ledger calls + 10 retry/attribute operations.
Add one notification per OCR document or embedding batch, 4 actions per listing page,
2,000 for build shards/evaluation, 100 trigger/control actions, and 40 for promotion.
These coefficients must be replaced by measured action-category totals before unattended scheduling.

| Scenario | Action arithmetic | Actions | Action cost |
|---|---|---:|---:|
| VN full rebuild | `4000×40 + 100×4 + (4 OCR + 144 embed)×50 + 544 notifications + 2000 + 100 + 40` | 170,484 | $8.5242 |
| VN incremental day | `20×40 + 20×4 + (1 OCR + 1 embed)×50 + 3 + 2000 + 100 + 40` | 3,123 | $0.15615 |
| VN full reprocess | `4000×36 + 148×50 + 544 + 2000 + 100 + 40` | 154,084 | $7.7042 |
| Six full rebuilds | `6×170484` | 1,022,904 | $51.1452 |
| Six daily increments | `6×3123` | 18,738 | $0.9369 |

Storage uses decimal GB: 80 KB final history/document with overhead, plus 20 MB for release/batches; charge full final size while active.
Normal work lasts 24 hours for rebuild/reprocess, 2 hours for increments; quarantined work lasts 168 hours.
For D documents and normal duration t: active GB-hours = `D×0.00008×(0.95×t+0.05×168)+0.02×t`.
Retained GB-hours = `(D×0.00008+0.02)×720`. Every scenario includes full 30-day post-close retention.

| Scenario | Active GB-hours / cost | Retained GB-hours / cost | Total with 10% support |
|---|---|---|---:|
| VN full rebuild | 10.464 / $0.439488 | 244.8 / $0.25704 | $10.14 |
| VN incremental day | 0.05648 / $0.00237216 | 15.552 / $0.0163296 | $0.19 |
| VN full reprocess | 10.464 / $0.439488 | 244.8 / $0.25704 | $9.24 |
| Six full rebuilds | 62.784 / $2.636928 | 1468.8 / $1.54224 | $60.86 |

Reserve 0.01 GB active history/country for Schedules/control: `0.01×720×0.042×1.10 = $0.33264` per 30 days.
Reconciliation adds `4 sources×96 daily ticks×(3 Schedule/start actions+1 bounded activity) = 1536` actions/day, plus `96×4 = 384` for country control.
Reserve 2,000 with retries: `2000×30×0.00005×1.10 = $3.30`. More pages or sources increase that allowance.
One rebuild plus 30 increments is about $19.55/VN or $117.27/six countries, including reserves and retention tails; assume four sources/country.
This is workload-lifetime allocation, not a calendar invoice forecast. A reprocess adds about $9.24/VN.
Use the write-path $25/month alert initially; alert does not authorize more spend or cancel evidence work.
Longer waits, more heartbeats, retries, small batches, and larger histories can exceed these estimates.

## Observability and interfaces

Build each document timeline from ledger receipts: discovery, fetch hashes, converter/parser recipes,
gate reasons, OCR jobs, decisions, acceptance, release membership, and before/after reprocessing scores.
Link workflow/run IDs for execution detail. Never use workflow-local attempt counters as accepted totals.
Dashboards group source windows, reprocess requests, builds, and promotions by country and source.
Show queue age, missing pages, stage duration, retries, quarantine reason/age, vector coverage,
source freshness, action/storage cost, provider spend, and desired/applied destination versions.
Alert at 15 minutes without an eligible item's progress, source freshness over 24 hours, quarantine
over 5% or growing for three daily windows, and every seven-day expiry. Alert immediately on uncertain
submission, stale-fence writes attempted, failed integrity gates, or activation mismatch.
Exclude intentional future waits from stuck-work alerts. During writer outages, reconciled serving keeps its last verified release; restored serving stays fenced.

| Sibling | Required contract; no internal redesign here |
|---|---|
| [Write path](corpus-write-path.md) | Stage inputs/outputs, retry classes, closure policy, VNGCloud writer placement for Vietnam, signed delivery, promotion and rollback, jurisdiction/command cutover |
| [Database](corpus-database.md) | Intake execution provenance without build IDs; acceptance/eligibility, fenced intake/batch/control scopes, decisions and launch/notification ledger, consistent cutoffs, immutable manifests; revision-linked offset maps/edit ledgers/reports/decisions; shared provision/part targets, block/table/artifact spans, redline storage, snapshots, pins and retention |
| [Conversion](document-conversion.md) | Own extraction from bytes to the block model for every format, including appendices/forms; activity/gate contracts, sandbox limits, OCR eligibility, reprocess comparisons. Bounded XLSX adapter emits cell-text blocks for search; no Temporal or accepted-evidence writes from converters. |
| [Document parts](document-parts.md) | Consume blocks to identify, cite, track, and display parts; required-artifact inventory, authority and gaps. XLSX is a file part with metadata, search hits, and the official-source download link, without rich rendering initially. Presentations: metadata and source link only. |
| [Embedding](embedding-models.md) | Recipe identity, compatible bulk results and query runtimes, measured p95/burst target, evaluation and old-named-release CI case; no reranker |
| [Hosting](vngcloud-hosting.md) | Delivery/readiness/activation receipts, measured single-runtime capacity and local serving. Configurable positive artifact-byte, per-client/overall stream, and timeout limits; check declared size and enforce streamed bytes |

## Migration and GitHub-hosted verification

Each slice needs independent review and GitHub-hosted checks; PR jobs use disposable databases, pinned Temporal images, fake providers, and synthetic inputs.
Protected jobs use versioned public-document fixtures and archive digests, commit, reports, and histories.
Real conversion/evidence checks prove validation. Deployment needs a separate receipt and smoke check.

| Slice | Change and reuse | Hosted exit evidence |
|---|---|---|
| 1. VN shadow intake | Reconcile the database intake contract first; generate/review persistence; add fenced `SourceSync`/`DocumentIngest` with isolated PostgreSQL/blob adapters and sandboxed baseline converters. Reuse stage budgets, source adapters, atomic apply, and SDK registration. | Accept a document before any build exists; reject stale intake writes. Prove duplicate start, crash-after-commit, missing page, bounded children, no publication, real SDK replay. Against hosted Temporal, prove acknowledged groups of at most 16 with maximum-size envelopes and combined commands below the transaction budget, including metadata overhead. Prove isolation/limits for normalization, structure, parts, MuPDF serialization, and converter-output parsing, including the block-only CPU boundary. Fake persistence alone fails. |
| 2. Durable repair | Add decisions/timers, `OcrBatch`, `Reprocess`, and control reconciliation; reuse external-job lifecycle. | Duplicate/expired/late decisions, timeout race, commit-before-signal crash, uncertain job, partial pages, live/quarantined children across rollover, completion accounting, disabled-source recovery, seven-day deployment wait, no refetch. Prove a sound attachment survives a body gap, excluded text stays unsearchable, and reprocessed candidates cannot enter builds before comparison and required adjudication. |
| 3. Release assembly | Replace release-wide intake with `CorpusBuild` accepted snapshot. Reuse release validation ports. | Base-plus-delta and no-op closure; late target creates a new build; exact repeal/attribution cases, parts, subjects, immutable revisions. |
| 4. Batch/index completion | Add `EmbedBatch`, complete lexical index, golden evaluation, sealing. | Missing/duplicate/wrong-recipe vectors fail; full index coverage, unchanged floors, captured replay, measured actions/history/storage, protected real-document validation. |
| 5. Promotion rehearsal | Add separate `Promote`, destination simulator then inactive hosted destination. Reuse publication readiness and selection ports. | Approval after completed build, denied/stale approval, corrupt copy, lost acknowledgement, rollback, pins, old named-release search after re-embedding or explicit dense-search retirement with `document` and lexical access. Restore stale control state and prove traffic stays blocked until reconciliation with ordered writer activation records. |
| 6. Controlled cutover | Thin command clients and owner-approved VN schedules; revoke legacy VN write access. | Five-tool parity, Vietnamese gate before query work, no query egress, exact target receipt. Keep other five services unchanged; later countries repeat country-specific gates. |

## Risks and owner questions

Quarantine can exhaust open-document slots; backpressure and expiry preserve gaps without dropping work.
An unfenced old CLI can bypass workflow safety; migration must revoke its access to the new VN writer.
Pinned workers increase operational burden; retain capacity until waiting executions close or migrate safely.
Shared batch response loss can duplicate provider charges unless reconciliation is supported.
Source staleness, accepted counts, and successful workflows do not prove legal completeness.

1. Approve a 24-hour quarantine reminder and seven-day decision expiry? Recommend both, then close
   as an unresolved gap. Late decisions start linked attempts. Never accept bad evidence by timeout.
2. Approve the initial fan-out and source quotas? Recommend 32 processing documents, 256 open document
   workflows per country, 100 child starts per parent run, and 1 request/second per source until measured.

Cadence, alert budget, and decision authority remain [write-path questions](corpus-write-path.md#risks-and-owner-questions).
Retention: [database question 1](corpus-database.md#risks-and-owner-questions). Latency: [embedding question 1](embedding-models.md#owner-questions). Capacity follows [hosting measurements](vngcloud-hosting.md#workload-and-sizing).
Unverified: actual action counts, history sizes, source refresh volume, provider reconciliation support,
converter performance, deployed state, and target-host single-runtime capacity. These are launch gates.
