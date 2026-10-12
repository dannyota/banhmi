Proposal. Not approved, not implemented.

# Corpus write path

## Goal and limits

Make poor source files into traceable evidence before assembling a release.
Use separate source, document, batch, build, promotion, and reprocessing workflows.
PostgreSQL owns accepted evidence, work ownership, source retries, and releases. Start with a Vietnam shadow.

Owner inputs: Tra cứu quy định at `tracuuquydinh.vn` serves Vietnam from VNGCloud.
The five other country services remain unchanged on AWS and their existing hostnames.
banhmi remains the engine and corpus name. Preserve all five MCP tools.
No answer model, generative model, or reranker belongs in this path or serving. Retrieval uses dense plus lexical fusion.
Only the query embedder runs as a served model, on our own host.
The write path accepts no user questions, accounts, dossiers, or query logs.
Public legal documents may enter approved cloud OCR and bulk embedding jobs.
Vietnam questions are Vietnamese with diacritics or a document number; the serving layer validates the question before any embedding or corpus query. English is interface text only.
Evaluation follows the same language rule. Never translate legal text.

This proposal owns stage contracts, placement, delivery, failure handling, and command migration.
[Corpus workflows](corpus-workflows.md) owns Temporal topology, queues, history, quarantine control, and cost.
Conversion internals belong to [document conversion](document-conversion.md).
The owner's VNGCloud decision follows the current [product boundaries](../../AGENTS.md#product-and-evidence-boundaries) and [website decisions](../design/website.md#decisions). This proposal does not authorize implementation.

## Current state and evidence

“Coded” means source exists, not execution proof. Recorded validation is not a deployment check for this proposal.

| State | Fact and evidence | Design consequence |
|---|---|---|
| Coded | `cmd/pipeline` composes direct activities and dispatches stage flags at [`main.go:106`](../../cmd/pipeline/main.go#L106). Its orchestration starts at [`main.go:528`](../../cmd/pipeline/main.go#L528). Discovery failures can be logged and skipped at lines 558–583. | Replace CLI sequencing and success inference. Reuse source and stage logic. |
| Coded | `cmd/ingest` is a crawler harness, not Temporal orchestration, at [`main.go:1`](../../cmd/ingest/main.go#L1). Its optional direct Bronze writer starts at line 76; detail failures continue at line 154. | Remove this alternate production write path after a shadow probe replaces it. |
| Coded | Normalize deletes provision rows at [`silver/queries.sql:136`](../../sql/silver/queries.sql#L136). Index commits chunks before best-effort embedding at [`index_activities.go:401`](../../pkg/pipeline/index_activities.go#L401). | Never wrap legacy mutations and call them immutable or fenced. |
| Coded | Temporal orders eleven stages at [`temporal/types.go:51`](../../internal/corpus/workflow/temporal/types.go#L51). Its stable workflow ID is defined at line 165. [`workflow.go:24`](../../internal/corpus/workflow/temporal/workflow.go#L24) coordinates activities, timers, and history rollover. | Reuse domain ports; replace the linear workflow with separately versioned workflow types. |
| Coded | [`run/types.go:104`](../../internal/corpus/run/types.go#L104) defines bounded shards; [`run/types.go:131`](../../internal/corpus/run/types.go#L131) defines outcome counts. Resource budgets start at [`run/types.go:42`](../../internal/corpus/run/types.go#L42). [`run/apply.go:44`](../../internal/corpus/run/apply.go#L44) requires atomic fenced output and result commits. | Persistence must prove these contracts before production workers use them. |
| Coded | [`release/validation.go:5`](../../internal/corpus/release/validation.go#L5) checks manifest identity, coverage, and gaps. [`release/publication.go:173`](../../internal/corpus/release/publication.go#L173) checks readiness and compare-and-set activation. | Preserve these domain ports and add target delivery around them. |
| Coded, execution pending | Canonical build, lease, result, job, and release tables start at [`ingest/schema.sql:204`](../../sql/ingest/schema.sql#L204). The hosted generation lane exists at [`corpus-source.yml:14`](../../.github/workflows/corpus-source.yml#L14), with tooling at [`generate_corpus.sh:50`](../../tools/ci/generate_corpus.sh#L50). Generated package destinations are in [`sqlc.yaml:1`](../../sqlc.yaml#L1). | Import reviewed hosted output before writing durable adapters. Lane source is not a successful run. |
| Pending | The source plan leaves adapters, executable workers, replay histories, corpus checks, and deployment open under [persistence rollout](../plans/corpus-redesign-source.md#task-7-generated-persistence-rollout-deferred) and [remaining gates](../plans/corpus-redesign-source.md#current-state-and-remaining-gates). | Do not describe the Temporal pilot as an operating service. |
| Recorded validation | A forced Vietnam rebuild changed chunks and reduced retrieval metrics in the [corpus quality record](../../PLAN.md#corpus-quality). The accepted Vietnam floors are encoded at [`Makefile:69`](../../Makefile#L69). | Measure candidate parity and regressions. Never rebuild the live corpus to test a design. |
| Recorded defects | Provision validity, amendment targets, and relation attribution are described in [Phase 3](../design/website.md#phase-3-depends-on-the-corpus-redesign). | Require explicit outputs and real-document regression gates below. |
| Deployment record, not verified here | AWS serving, operator-triggered generation, and dump transfer are recorded in the [write path](../ARCHITECTURE.md#write-path) and [production deployment](../ARCHITECTURE.md#production-deployment-and-security). An EC2 pipeline launcher is coded at [`run-pipeline.sh:4`](../../deploy/aws/run-pipeline.sh#L4). | Keep current services untouched. Use isolated generation capacity. |

## Decisions

| Decision | Recommendation | Rejected option and reason |
|---|---|---|
| Coordination | `SourceSync`, `DocumentIngest`, `OcrBatch`, `EmbedBatch`, `CorpusBuild`, `Promote`, and `Reprocess` at real work boundaries; country `ControlReconcile` repairs delivery and job state. | One release workflow couples discovery, document failures, provider waits, and publication. A workflow per chunk adds cost without an independent lifecycle. |
| Ownership | Workflows execute ledger work; only fenced activities accept evidence and update durable state. | A second completion ledger in workflow history would split authority. Per-document execution itself does not duplicate ownership. |
| Correctness | Immutable inputs plus fenced, atomic stage acceptance. | Temporal completion alone cannot prove a database commit or prevent stale writers. |
| Generation | Run the authoritative Vietnam writer and workers on isolated VNGCloud capacity in Vietnam. | Sharing the small serving host couples bulk work, recovery, and user traffic. Keeping the proposed writer on AWS violates the owner's placement decision. |
| Temporal service | Temporal Cloud Developer, pay as you go with no minimum, with only non-personal corpus references in histories. | Self-hosting adds persistence, upgrades, backups, and incident work to the initial rollout. |
| Publication | Transfer an immutable candidate, verify it at the destination, then change selection. | Writing directly into an active serving database exposes partial results and weakens rollback. |
| Legal processing | Deterministic rules and reviewed assertions produce evidence with unknown states. | Generated legal corrections or automatic confidence-based acceptance hide unsupported claims. |

**Decision:** Place the new Vietnam corpus writer and database, capture and converter workers, private agent review API, artifacts, and backups on isolated VNGCloud generation capacity in Vietnam. Keep the approval and audit service beside live auth on the separate VNGCloud serving host.
**Why:** One Vietnam boundary removes the proposed foreign writer and approval-export path while preserving one fenced writer and immutable release delivery.
**Rejected:** An AWS Vietnam writer, AWS S3 for new Vietnam artifacts, and generation on the small serving host.
**Detail:** Private Vietnam vStorage replaces planned AWS S3. The writer and workers receive no serving query or account access. The approval service has no corpus write authority. Provider API, IAM, signing, retention, recovery, network, capacity, and cost claims need implementation evidence. Temporal Cloud orchestration, Google Cloud Vision OCR, and Hugging Face batch embedding retain their approved public-payload gates. Existing AWS services for the other five countries remain unchanged.

Run the new Vietnam writer, database, capture workers, converter workers, review API, artifacts, and backups on VNGCloud in Vietnam. Keep writer and worker capacity isolated from the small serving host.
Temporal Cloud remains approved for orchestration and carries only bounded non-personal references. Google Cloud Vision OCR and Hugging Face batch embedding remain approved for gated public-document payloads.
Bulk document embedding runs on Hugging Face Jobs, pay as you go, with Runpod Flex as fallback.
Kaggle and GitHub Actions free tiers serve evaluation and bake-offs only.
The owner's topology replaces the single-workflow direction in
[input processing and Temporal ownership](../design/corpus-pipeline-redesign.md#input-processing-and-temporal-ownership) for this proposal.
It does not change the existing pilot or authorize its execution.

## Stage contract

Keep the existing `run.Stage` vocabulary as stage labels, not a global execution order.
`SourceSync` discovers. `DocumentIngest` fetches, captures, converts, normalizes, structures, and gates.
Fenced acceptance follows the gate; reprocess mode also requires comparison and any critical-change adjudication.
`OcrBatch` and `EmbedBatch` serve durable requests from documents and builds.
`CorpusBuild` selects build-eligible revisions into its manifest, resolves relations and validity, assigns subjects, chunks,
indexes, evaluates, and seals. `Promote` delivers and activates. `Reprocess` selects stored originals.
Capture is Fetch; conversion is Extract; local structure, relations, validity, and subjects are Normalize.
Chunking is Index; sealing and delivery are Publish. Record distinct substeps in immutable manifests.
Do not add SQL stage values or counterfeit release builds to represent document processing attempts.
Prefer official DOCX, text-bearing HTML, legacy DOC, then born-digital PDF. OCR never silently replaces rejected binding text.

For build work, `K` means the existing `run.IdempotencyKey(shard)`, defined at
[`run/types.go:279`](../../internal/corpus/run/types.go#L279): country, run, build, stage, cursor,
manifest reference, recipe, and budgets. Mutable owner and fence do not enter K.
The table adds each stage's manifest inputs. One reference always resolves to the same bytes/digest.
Hash manifest bytes, not reference strings: [persistence identity contract](../plans/corpus-redesign-source.md#task-7-generated-persistence-rollout-deferred).
Recipe digests bind code, policy, configuration, runtime image, and applicable model or parser versions.
A new outcome after a source retry uses a new immutable attempt manifest. A transport replay reuses K.

`DocumentIngest`, `OcrBatch`, and `Reprocess` record work in an intake ledger: execution records keyed
by workflow ID and attempt fence, with no build ID. Batch executions bind the collection key and frozen
request membership; reprocess executions bind their selection manifest and child attempt references.
The database owns intake and batch execution identities, revision acceptance state, and eligibility.
Its apply contract validates country admission epoch, execution ID, document version or batch collection,
stage/substep, input manifest digest, recipe, budget, live lease owner, and attempt fence.
Country admission excludes legacy schedulers; document and batch leases allow bounded parallel work.
The immutable attempt key excludes mutable owner/fence. Fence changes cannot change accepted payloads.
Commit output references, outcome receipt, acceptance state, and required launch/notification records
atomically. Reconcile unacknowledged records after response loss; signals alone never prove acceptance.
Existing `Execution` requires a build at [`run/types.go:69`](../../internal/corpus/run/types.go#L69),
and `document_revision.build_id` is required at [`silver/schema.sql:398`](../../sql/silver/schema.sql#L398).
Replace required origin-build provenance with `document_revision.intake_execution_id`; keep legacy `build_id`
nullable. Build membership references revisions separately and does not own their lifetime.

Retry codes below apply to every stage. Retry ownership is exclusive:

- **I:** Temporal retries worker loss and classified transient infrastructure failures, at most three
  attempts with 1-second initial backoff, doubling to a 30-second cap. This preserves
  [`workflow.go:394`](../../internal/corpus/workflow/temporal/workflow.go#L394). Contract errors stop.
- **S:** The ledger owns source retries and `NextEligible`. Start with five attempts over 24 hours,
  respect `Retry-After`, and use bounded exponential backoff with jitter recorded by an activity.
  A source descriptor may require stricter pacing. No overlapping Temporal retry of the source row.
- **X:** Prepare, submit-or-reconcile, check, and apply. Retry polling with I; resubmit only after a
  confirmed failed attempt and a new manifest. An uncertain submission stops for review.
- **Q:** Do not retry unchanged bad input or a failed quality gate. Record a blocking gap.
  A corrected recipe, source observation, or reviewed decision creates new work.

“Yes” means deterministic for fixed bytes and recipes. External observation, inference, and delivery
need not reproduce bytes; preserve their accepted outputs and replay them by reference.

| Stage / run stage | Input → output | Immutable work identity | Retry | Side effects | Deterministic? |
|---|---|---|---|---|---|
| Discover / Discover | Source descriptor, window, cursor, scope → captured listing pages, candidate IDs, discovery reasons, completeness receipt | Source, slice, window, page cursor, observation attempt, scope digest | I + S | Read official site; stage listing blobs; commit ledger and cursor only for a completed page/slice | No, live source |
| Fetch / Fetch | Artifact plan → staged response/file blob and transport receipt | Source record, artifact role, locator revision, attempt | I + S | Network GET; bounded temporary/object writes; rate-limit claim | No, live source |
| Capture / Fetch | Staged blobs and receipt → immutable Bronze observation and artifact links | Fetch receipt, hashes, role ordinals, metadata digest | I; Q on mismatch | Verify object length/hash; atomically register Bronze and result | Yes, given receipt |
| Detect and convert / Extract | Immutable original manifest → detected format, ordered block manifest, artifact map, diagnostics, OCR request or conversion result | Original hashes, converter image/config, format and block-schema versions | I + Q | Converter sandbox stages outputs only; trusted activity validates and registers receipt | Deterministic contract where converter certifies it; preserve exact output otherwise |
| OCR batch / OCR | Failed/scanned file manifest → OCR text, confidence, page coverage, review state | Ordered file hashes/pages, engine, language, rendering recipe | I + X + Q | External batch charges and result blobs; always non-binding | No; retain exact result |
| Normalize, structure, gate / Normalize | Converted blocks, official structure, identity assertions; in reprocess mode, prior accepted output, comparison report, required adjudication references → candidate document/provision/part revisions, blocks, tables, artifact spans, gate report | Original/block hashes, normalization, parser, part and gate recipes; prior/comparison/adjudication hashes in reprocess mode | I + Q | Stage candidates; fenced acceptance validates the gate, comparison, and required adjudication before eligibility | Yes |
| Snapshot / Normalize | Build-eligible revision ledger, base release, source closure, cutoff → fixed membership plus explicit exclusions and gaps | Accepted watermark, base/delta digests, closure and policy | I + Q | Select only `accepted` or `accepted_with_gaps`; commit immutable build inputs without waiting inside a transaction | Yes for the recorded snapshot |
| Relations and validity / Normalize | Provisions, parts, metadata assertions, target revisions → attributed relations, amendment targets, provision/part validity, amendment differences, unresolved targets | Complete evidence set, target-resolution set, legal-policy and difference recipes | I + Q | Commit assertions, amendment differences, and gaps; enqueue bounded target discovery | Yes; review decisions are explicit inputs |
| Subject assignment / Normalize | Revisions, taxonomy, configured rules, review decisions → assignments, confidence, unknowns, context roles | Revision set, taxonomy digest, assignment recipe and review refs | I + Q | Commit assignments; no copied document or per-subject crawl | Yes |
| Chunking / Index | Closed provision/part set and assignments → chunks, citations, source spans, embedding inputs | Revision closure, citation/chunk recipe, part policy | I + Q | Commit build-scoped chunk set; no inline embedding | Yes |
| Bulk embedding / Dense | Chunk input hashes and model recipe → complete vector manifest and coverage | Exact input bytes, model/tokenizer/preprocessing/runtime digest, dimensions | I + X + Q | Batch compute, cache lookup, vector application | No bitwise promise across hardware |
| Lexical indexing / Lexical | Entire frozen chunk set → lexical model and vectors under one revision | Ordered chunk set, tokenizer, preprocessing and lexical recipe | I + Q | Exclusive candidate index build; bounded streaming writes | Yes for pinned recipe and ordering |
| Evaluation gate / Evaluate | Frozen candidate, goldens, baseline, gap policy → signed-off pass/fail artifact and per-case differences | Candidate digest, evaluation code/data, thresholds, runtime recipe | I + Q | Read-only candidate queries; report object; no user questions | Scores replayable with fixed query vectors; runtime probes measured |
| Release build / Publish | Passed candidate and report → sealed release bundle and manifest | Candidate digest, report digest, schemas, all recipe/image digests | I + Q | Export immutable artifacts; register candidate release | Logical contents yes; archive bytes fixed after first export |
| Publication / Publish | Sealed bundle → transferred and verified destination-ready receipt | Release ID, bundle digest, target, adapter version | I; Q on mismatch | Upload, import into inactive target, verify; mark ready | No; idempotent delivery |
| Promotion / Activate | Ready receipt and owner decision → selection version and applied receipt | Country, build, release, target, expected prior selection/version | I with reconciliation; Q on conflict | Compare-and-set selection; notify serving; retain prior release | Conditional state change, not pure |

### Closure, accounting, and evidence

A revision is build-eligible only in state `accepted` or `accepted_with_gaps`.
A reprocessed revision remains a candidate until its before and after comparison passes and, when
critical text changes, the owner adjudicates. Critical changes include legal numbers, dates, negations,
citations, authority flags, and part boundaries under the conversion contract.
`DocumentIngest` in reprocess mode stages a candidate and waits on durable comparison/decision receipts.
`Reprocess` compares staged candidates without waiting for acceptance and records `ApplyComparison`
before the child's fenced `ApplyEvidence` may accept the candidate.
Acceptance validates the prior accepted revision, candidate and quality report hashes, comparison
outcome, and required adjudication references under the current execution fence in one transaction.
A concurrent build before that commit preserves the prior accepted revision and records pending
reprocessing. A later acceptance cannot change an existing build manifest.

Each source window fixes its enabled slices, cutoff, page budget, and deadline.
Record actual observation times separately. A cutoff cannot reconstruct an earlier live source.
Stage captured listing bytes before advancing cursors. Missing pages leave incomplete coverage.
`DocumentIngest` may finish after its source window; its accepted revision enters a later snapshot.

`CorpusBuild` first snapshots build-eligible document revisions and the source-coverage ledger in one
consistent read. Pin a base release and its digest; initial import uses a full-baseline manifest.
Membership is the base plus explicit replacements, additions, and evidenced tombstones.
No-op refresh preserves all members. Absence from a feed is not deletion.
A rejected new version leaves the older accepted version visible with an explicit stale-version gap.
An unseen document leaves a missing-document gap. Neither is counted as newly accepted evidence.
A build must account for pending, quarantined, and failed intake before it can pass coverage policy.
Only enumerated owner-accepted gaps may permit release; an incomplete index cannot become a gap waiver.

Resolve cross-document relations against that snapshot, including provision and part targets.
If a required target is absent, enqueue a durable source request and leave an unresolved-target gap.
Do not add a late document to the frozen build. After ingestion, start a new build with a new snapshot.
Bound relation intake to one target hop and three request rounds per source window.
Assign subjects to the closed set, then chunk and embed. Recompute target validity when acting text changes.
Reuse unchanged evidence and recipe-compatible vectors; build one lexical revision over all membership.
Bind base, delta, coverage, and recipe digests to the candidate. No claims is not proof of completion.

Use `run.Counts` for each shard. Compute release totals from unique ledger item outcomes, not the
pilot's cumulative attempt counters. Every expected item must resolve to success or an accepted gap.
Place bytes in content-addressed storage first. Build `UnitOfWork.Apply` validates the live lease,
country, run, build, owner, and fence while atomically accepting references and the stage result.
Intake and batch apply use their execution scopes and the same atomic fencing rule.
Objects left by a failed transaction are unreferenced staging objects, never accepted evidence.
A lost completion response returns the committed result. Changed payload under K is an error.
Renew leases during work; cancellation or lease loss prevents acceptance even if compute finishes.

Treat Phụ lục, Biểu mẫu, Mẫu số, tables, and attached files as first-class revisioned parts.
Provisions and parts share one validity target type; amendment targets and intervals can address either.
Relation outputs name acting document/provision revision, exact evidence span, target document,
target provision/part or unresolved target, operation, effective interval, authority, confidence, and review state.
Keep document, provision, and part status separate; retain causal assertions, conflicts, and unknown applicability.
Use the rules in [`instructions/corpus.md:30`](../../instructions/corpus.md#L30); do not redefine grammar here.
The known 09/2020/TT-NHNN Điều 25 case must show repeal from 50/2024/TT-NHNN khoản 6 Điều 22,
effective 2025-01-01, without attaching the other five clauses or repealing the whole document.
Treat that owner-supplied case as an acceptance requirement until official-document checks prove it.
The relations and validity stage computes amendment differences from exact old/new revision and target
references. The database stores those differences; [the website design](../design/website.md) owns redline display.

Conversion owns extraction from bytes to the block model for every format, including appendices and
forms inside a file. Parts consumes the block model to identify, cite, and track parts, and owns display.
Conversion's bounded XLSX adapter emits cell-text blocks for search. Parts shows a qualified spreadsheet as a
file part with metadata, search hits, and the official-source download link, without rich rendering in the first release.
Presentations receive metadata and source link only. No write-path option overrides those boundaries.

## Workflow boundary

[Corpus workflows](corpus-workflows.md) defines the pipeline and control workflow types, deterministic IDs,
parent/child lifetime, bounded fan-out, quarantine decisions, queues, replay, schedules, and cost.
Use that topology; do not run this stage table as one ordered release workflow.
The database ledger remains authoritative even when a document waits in Temporal for days.

Conversion and workflows share one quarantine decision contract: decision ID, document version ID,
quality report hash, workflow fence token, actor, action, reason. Store the decision before notification.
Also bind country, candidate digest, expected decision epoch, expiry, original/output hashes, exact scope,
evidence references, replacement recipe/source reference, and owner authorization.
Authority is the owner or reviewers the owner designates, governed by owner question 3 below.
Keep actor identity in the Vietnam operator audit store; the writer receives an opaque reviewer key and Temporal receives a decision reference only.
Build the [review API first](corpus-review.md#decisions), behind mutual TLS with a private CA; the owner reviews through AI agents. The human console is deferred.
Fenced application checks authority, the current workflow fence token, and the exact quality report hash.
Duplicate delivery returns the receipt; stale decisions cannot accept a different report or attempt.

| Action | Required result |
|---|---|
| Retry with another converter | New attempt and recipe; rerun all gates from the same stored original |
| Accept as non-binding OCR | Require OCR output and passing integrity gates; preserve non-binding authority |
| Accept with named gaps | Rerun mandatory gates on exact retained/excluded ranges; accept sound spans as `accepted_with_gaps`, exclude unsafe ranges from both indexes, and record scoped gaps |
| Mark as a coverage gap and exclude | Record missing scope; leave the candidate ineligible for builds |
| Attach an alternate official source | Separate intake with verified identity and new observation hashes; preserve the original |

None of these actions waives reprocess comparison or required critical-change adjudication.

## Hosting and delivery

| Temporal option | Cost basis | Operational load | Decision |
|---|---|---|---|
| Temporal Cloud Developer, pay as you go with no minimum | Actions and history storage, plus support; [public pricing](https://temporal.io/pricing) | We run workers, enforce payload policy, rotate access, replay code, and monitor spend; vendor runs the service | Approved for orchestration; implementation remains proposed |
| Self-host on isolated VNGCloud infrastructure | Compute, persistence/visibility database, storage, backups, monitoring, network, and operator time; no measured quote | We own service/database upgrades, availability, restore drills, security, and incidents | Reconsider only if measured cost justifies it; never share the serving host |

[Corpus workflows](corpus-workflows.md#cost-estimates) owns workload assumptions, public pricing,
action arithmetic, history storage, and the proposed $25/month Temporal-only alert budget.
Choose 30-day workflow history retention initially; evidence retention is a separate database contract.
Temporal Cloud runs coordination; isolated VNGCloud pools run Vietnam generation. Workers authenticate at runtime.
No secrets, account data, question text, or public-document bytes enter workflow payloads.

| Work | Recommended execution location |
|---|---|
| Discover and fetch | Isolated VNGCloud capture workers with measured source access and bounded egress |
| Untrusted-byte processing | Isolated VNGCloud converter sandboxes on `corpus-converter-v2` |
| Capture registration, structure over blocks, relations, validity, subjects, chunks, lexical build | Isolated VNGCloud CPU workers and writer; no raw-file parsing on plain CPU workers |
| OCR | Google Cloud Vision for gated public-document payloads; store originals and OCR outputs in private Vietnam vStorage under the [conversion decision](document-conversion.md#decisions) |
| Bulk embedding | Hugging Face Jobs, pay as you go, with Runpod Flex as fallback; VNGCloud controllers accept outputs from pinned public-document recipes |
| Evaluation and release build | Isolated VNGCloud candidate environment; GitHub-hosted checks verify reports and reproducible fixtures |
| Vietnam delivery/readiness | Private Vietnam-local release transfer and readiness endpoint supplied by the hosting design |
| Other countries | Existing AWS generation and serving unchanged; later opt-in uses the same release contract with an AWS destination |

Prove batch reconciliation before unattended use. Embedding model/runtime selection belongs to its sibling.
No bulk work runs on the Vietnam serving host. No Vietnam serving query travels to a writer, Temporal, or external batch service.
Every activity that reads untrusted bytes runs on the converter sandbox queue: LibreOffice, MuPDF
including text serialization, DOCX, HTML and XLSX parsers, image decoding and OCR request building.
Structure building over the block model, which reads no raw bytes, may run on plain CPU workers.
Provider controllers submit prepared bounded requests; they do not decode or parse source files.

1. Freeze candidate data before evaluation. Bind its digest to the full evaluation report.
2. Seal the release only after all gates pass. Register the bundle hash, source closure, recipes,
   taxonomy, schema, evaluation, gaps, and compatible serving/embedder image digests.
   `Promote` then waits for an owner decision bound to that release, destination, and expected selection.
3. Copy only corpus artifacts to the destination under an immutable release ID. Verify origin,
   signed manifest, digest, completeness, country, and schema allowlist. Import into an inactive instance.
4. Re-run destination integrity and read-only smoke checks. Return a readiness receipt bound to
   release, build, target, bundle digest, and runtime identity. Recheck freshness before activation.
5. The authoritative writer records promotion by compare-and-set against the expected selection.
   Deliver an ordered activation record to the target. The target applies it idempotently after
   verifying local readiness; it rejects stale versions. Record desired and applied selection separately.
6. Serving pins a complete local bundle for each request. It needs no writer connection on the request
   path. A lost acknowledgement triggers reconciliation, not a second promotion or an assumed failure.
7. Rollback is a new ordered selection of a retained compatible release. A failed copy or readiness
   check leaves the prior selection serving. Preserve all pins and holds.
8. After any restore, keep serving traffic fenced until the target reconciles its active release with
   the writer's ordered activation records and verifies the selected bundle and matching query runtime.
   A hosting restore never reactivates stale release control state. If reconciliation is unavailable,
   keep the restore fenced; routine serving after reconciliation still needs no writer request-path call.

Online retention is the active release plus at least two prior releases, kept for at least 30 days, plus any release under an explicit pin or hold.
Retain each release's database and query-recipe bundle together.
Only one embedding model runs on the serving host at a time, under the [embedding runtime policy](embedding-models.md#migration-and-rollout-slices).
Before replacing the runtime, re-embed every release that stays in dense search, or retire older releases from dense search.
Retired releases remain available through `document` and lexical search, with a visible gap. Retention, pins, and holds still protect their evidence and recipe artifacts.
Dense search requires a matching query runtime (same model, tokenizer, recipe and dimensions). No dual-runtime capacity is reserved.

There is no distributed transaction across the isolated writer and serving systems. A committed desired selection may await target
application; report promotion complete only after the target acknowledges the same version and digest.
The selected country writer alone authors revisions and desired selection. A destination writes only
delivery receipts and local applied state, never competing corpus evidence or independent promotions.
The database sibling defines replication and local pin persistence. Export no authentication state,
account identifiers, or query data. Private capture blobs are not a new public file-serving feature.

## Jurisdictions, subjects, and command migration

Use descriptors for sources, parsers, citations, OCR languages, relation policy, scope, and goldens:
[`jurisdiction.go:47`](../../pkg/base/jurisdiction/jurisdiction.go#L47), [`app.go:198`](../../pkg/app/app.go#L198).
Resolve the country through an allowlisted registry; reject an invalid explicit country.
Freeze descriptor/config digests per build. A subject changes assignments and scope policy, not the
country writer, workflow type, or document identity. Cross-subject legal context stays explicit.
Enable only Vietnam for the new path. Later countries require their own fixtures and promotion gates.

| Existing code | Reuse or replace | Removal gate |
|---|---|---|
| `pkg/ingest`, `pkg/fetch`, `pkg/extract`, jurisdiction parsers | Reuse transports and pure extraction/parsing logic. Adapt discovery to bounded page manifests; retain source priority and citation rules. | Remove old adapters only after jurisdiction parity checks |
| `pkg/pipeline` stage mutations | Split computation from persistence; route new writes through generated adapters and `UnitOfWork` | No new Temporal worker may call legacy unfenced mutations |
| OCR/embed batch helpers | Reuse input gates and recipe-compatible caches; replace synchronous submission orchestration with durable job ports | Reconciliation and partial-result tests pass |
| `cmd/pipeline` | Thin Vietnam start/status/cancel/backfill/promote client; isolated direct recovery uses the same ports | Disable legacy VN mutation at cutover; remove shared loops/flags only after every country's callers and repair flows migrate |
| `cmd/ingest` | Thin Vietnam shadow probe using bounded activities and an isolated output target | Deny direct Bronze writes to the new VN writer; remove shared `-persist` only after all dependent callers migrate |
| `pkg/app` corpus composition | Add `internal/app/corpus` and `cmd/corpus-worker` only after durable adapters exist | Remove the no-op `WithoutTemporal` at [`app.go:77`](../../pkg/app/app.go#L77) when callers migrate |
| AWS pipeline scripts | Remove Vietnam from these triggers after the VNGCloud path passes cutover gates | Preserve other-country scripts and runtimes until each country opts in |

## Interfaces

| Sibling | Required contract; internal design stays with sibling |
|---|---|
| [Corpus workflows](corpus-workflows.md) | Execute stage contracts, including candidate → comparison → required adjudication → fenced acceptance in reprocess mode. Own bounded concurrency, shared quarantine decision delivery, timers, replay, and cost. Route every untrusted-byte activity to `corpus-converter-v2`; country control repairs job and delivery state. Workflow success never proves corpus completion. |
| [Document conversion](document-conversion.md) | Own extraction from bytes to blocks for every format, normalization, body structure, quality, provenance, OCR eligibility, comparison reports, converter isolation and evaluation. Use the shared quarantine decision contract. No database or Temporal access inside the sandbox. |
| [Corpus database](corpus-database.md) | Own the intake ledger, execution IDs and attempt fences, admission/leases, atomic receipts with launch/notification records, intake revision provenance, acceptance state and build eligibility; separate build membership; shared provision/part validity targets; blocks, table rows/cells/spans and artifact byte spans; amendment differences; taxonomy and index coverage; export/import, desired/applied selection, pins and retention for database plus query-recipe bundles. No caller-supplied database names. |
| [Corpus database: conversion provenance](corpus-database.md) | Persist raw to normalized offset maps, the reversible edit ledger, the quality report (metrics, thresholds, outcome, reason codes) and review decisions, each linked to the document revision. |
| [Embedding models](embedding-models.md) | Immutable recipe/cache identity; bounded public-input manifests; output ID/count checks; resumable receipts; query/bulk parity; evaluation artifact; one matching query runtime for dense search, with explicit dense-search retirement for older releases. No reranker or third-party query inference. |
| [Document parts](document-parts.md) | Consume blocks to identify, cite, track, and display revisioned parts. Supply required/optional artifact inventory, part hierarchy, authority, part gates, validity targets, and gaps. Conversion owns byte extraction. Completeness includes every required part; publication binds parts, files, relations, and indexes. |
| [VNGCloud hosting](vngcloud-hosting.md) | Private delivery, inactive import capacity, measured single-runtime capacity, ordered activation and restore reconciliation before traffic, local availability, observability, optional isolated crawler egress. Own configurable positive maximum artifact bytes, concurrent download streams per client and overall, and stream timeout. Serving checks declared size before streaming and enforces actual bytes while streaming. Generation receives no query/account data. |

Size from measurement for PostgreSQL, one embedder, the Go server, auth, and the approval and audit service on a dedicated small serving vServer.
Start from `a-general-4x8` (4 vCPU, 8 GiB), with `a1-standard-2x8` as the cheaper fallback if measurement allows, under the [hosting decision](vngcloud-hosting.md#workload-and-sizing).
Measure p95 at 2 requests per second, bursts of 8, with concurrent PostgreSQL, auth, approval, audit, and application load; grow only from measurement.
The latency target remains [embedding owner question 1](embedding-models.md#owner-questions).

## Failure policy and rollout

| Failure | Required outcome |
|---|---|
| Partial source outage or missing listing page | Preserve successful captures and old published evidence; hold cursor; show stale/incomplete coverage; block promotion until repaired or specifically accepted |
| Bad parse, missing provision, ambiguous target | Quarantine the new revision; retain provenance and explicit gaps; block affected candidate, never silently substitute plausible text |
| OCR failure or missing pages | Retain scan and job receipt; mark non-binding/unavailable coverage; no successful zero-page result |
| Embedding failure, missing vector, recipe mismatch | Reject incomplete Dense stage; retain reusable successes; block promotion; planned dense-search retirement of older releases is a separate explicit transition |
| Evaluation regression or missing cases | Store failed report and block publication; do not lower floors or remove cases |
| Destination mismatch or lost activation response | Keep prior ready bundle; reconcile desired/applied selection and alert |

Only reviewed, enumerated source-availability or known-evidence gaps may receive acceptance.
Intake decisions use the shared quarantine contract. Release gap acceptance separately binds build,
item IDs, reason code, expiry, coverage impact, and intake decision references. It cannot waive broken
references, failed evaluation, incomplete index coverage, corrupt artifacts, or an uncertain job.
Failed-build reports expose all gaps to operators. A published release exposes its accepted gaps
through `corpus_status`, `quality_gaps`, and affected evidence. Separately label refresh failure and
staleness without mutating the immutable evidence of the last active release.

Each slice requires hosted CI and independent review. Use synthetic fixtures, disposable PostgreSQL,
pinned Temporal images, and fake providers without production access in pull requests.
Protected runs use versioned public documents; CI archives commits, input digests, reports, and histories. Skips are not passes.
Slice 1 cannot be accepted until the database sibling supplies the intake and batch persistence contract
above and hosted PostgreSQL checks prove intake without a build, nullable legacy origin-build provenance,
admission/lease fencing, and atomic receipts with launch/notification recovery after commit-response loss.
Later slices also require matching workflow, conversion, parts, and hosting contracts before execution.

| Slice | Deliverable | GitHub-hosted verification and exit gate |
|---|---|---|
| 1. Vietnam shadow intake | Hosted generation, minimal intake adapters and sandboxed baseline converters; `SourceSync` plus `DocumentIngest` against isolated PostgreSQL and artifact storage | GitHub-hosted Temporal service and fixed public VN fixtures. Prove accepted revisions reach the isolated database, no converter egress, no production or publication access, start-response loss, stale fences, bounded pages, and replay. Fakes alone do not pass. |
| 2. Quarantine and reprocessing | Shared decision contract, conversion provenance storage, sandbox contract, `OcrBatch`, `Reprocess` | Hosted race: a build before adjudication preserves the prior accepted revision; only a later build may select the newly accepted revision. Reject missing/stale comparison or adjudication references. Check all five decisions, expiry, duplicate/late delivery, uncertain submission, crash-after-apply, provenance round trips, and sandbox routing for every byte reader. Protected real-file checks prove conversion separately. |
| 3. Evidence closure | `CorpusBuild` snapshots eligible revisions; relations, amendment differences, parts, provision validity, subjects, chunks | Known repeal/misattribution fixtures, exact before/after amendment references, missing parts, XLSX cell-text search and file-only display, presentation metadata only, late target requires a new build, no-op and one-change snapshots, parser revision immutability. |
| 4. Complete candidate | `EmbedBatch`, lexical revision, evaluation, sealed release | Provider faults, complete vector/lexical coverage, all goldens and unchanged floors, real public-document sample, measured cost and resource report. |
| 5. Delivery rehearsal | `Promote`, inactive Vietnam destination, ordered activation, rollback, restore reconciliation | Duplicate approvals, CAS races, lost acknowledgement, offline serving, pins, and search of an old named release after model promotion. Prove re-embedded dense search or explicit dense-search retirement with `document` and lexical access; reject a mismatched runtime or failed single-runtime capacity. Restore stale control state after a newer activation; deny traffic until writer reconciliation and readiness agree. Unavailable reconciliation keeps traffic fenced. |
| 6. Controlled Vietnam promotion | Owner-approved release and source schedules; thin command cutover | Exact destination receipt, five-tool evidence parity, language validation before queries, no query egress, and hosted smoke checks. Launch is a separate gate. Other countries stay unchanged. |

## Risks and owner questions

Capture inputs and review states against source drift and incorrect assertions. Reconcile jobs and delivery receipts.
Measure costs and check histories for personal-data leakage with sentinel strings. Freshness does not prove legal coverage.

1. Approve a $25/month Temporal-only alert budget? Recommend this alert and no production scheduling
   before measured shadow costs. The orchestration service and writer placement are settled above.
2. Approve daily source sync, weekly validity rebuilds, and a 24-hour source/build deadline?
   Recommend these values. Quarantine has a separate seven-day decision timer, owned by
   [workflow owner question 1](corpus-workflows.md#risks-and-owner-questions), and does not hold a build open.
3. Who may accept source gaps and promote a release? Recommend owner-only decisions initially;
   the owner may designate reviewers for gap acceptance. Keep critical-change adjudication and manual
   promotion owner-only. Store decisions outside workflow history, bound to exact reports and inputs.
   Technical review requirements belong to [conversion owner question 3](document-conversion.md#risks-and-owner-questions).

Retention and recovery approval belongs to [corpus-database.md](corpus-database.md#risks-and-owner-questions), owner questions 1 and 3.

Unverified: live deployment state, latest accepted corpus snapshot, provider reconciliation support,
actual refresh volume and cost, VNGCloud transfer/readiness behavior, and the official-document
proof of the supplied repeal example. No build, test, generator, corpus job, or cloud action ran
for this proposal. Placement-dependent adapters still require implementation and hosted evidence.
