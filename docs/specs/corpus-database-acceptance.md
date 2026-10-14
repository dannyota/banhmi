Proposal, not implemented. This continuation of the [corpus database spec](corpus-database.md) owns fenced acceptance on writer files `00001` to `00005`. The [Vietnam plan](../plans/vietnam.md#m16-fenced-cross-store-acceptance) owns delivery as M1.6. The open contracts below block dependent acceptance paths; synthetic fixtures do not settle them.

# Corpus database acceptance

`ApplyEvidence` accepts a gated candidate in one `READ COMMITTED` transaction. It validates intake grants, captured originals, immutable revisions, conversion evidence, quality policy, and any decision. It commits the result, acceptance event and projection, revision receipt, scoped gaps, retention edges, and completion intent together. It creates no Bronze or Silver evidence and adds no schema.

The contract is [run.IntakeUnitOfWork](../../internal/corpus/run/intake.go). Implement `Acceptance.Apply(context.Context, run.IntakeApplyRequest) (run.IntakeReceipt, error)` in `internal/corpus/store/intake/acceptance.go`. Keep the frozen `run` and `model` types unchanged. M1.7 routes nonempty acceptance states here: `ApplyQuarantine` supplies `quarantined`; `ApplyEvidence` supplies a terminal state. Candidate recording uses the same adapter without implying workflow completion.

## Composition and existing primitives

**Decision:** Give `Acceptance` its own transaction body and a private `*Ledger`, constructed with a nil `AcceptanceVerifier`. Use `ledgerTransaction`; never call `Ledger.Apply` inside it.
**Why:** Acceptance needs locks and retention writes that the read-only verifier port cannot supply. Document adapters start and commit independent transactions. Nesting either adapter breaks the atomic boundary.
**Rejected:** A verifier that mutates or commits; nested document adapters; new exported transaction methods in the document store.
**Detail:** `NewAcceptance(db TransactionStarter, writer string, objects blob.Store, permits PermitCheck) (*Acceptance, error)` validates dependencies without I/O. Require object storage. The permit boundary below defines when nil is safe. Composition keeps ordinary `Ledger.Apply` configured with a nil verifier, so acceptance requests there fail closed. The existing port supports other trusted verifiers; it does not globally disable that route. M1.6 does not remove the port or change `ledger.go`.

The checked helper inventory below is current code, not proposed API. In signatures, `ctx`, `tx`, `q`, `r`, and `e` mean `context.Context`, `pgx.Tx`, `intakedb.Querier`, `run.IntakeApplyRequest`, and `run.ScopedExecution`. `Result` means `intakedb.IngestExecutionResult`.

| Existing helper | Signature after the name | Use and limit |
|---|---|---|
| `ledgerTransaction[T]` | `(ctx, *Ledger, func(pgx.Tx, intakedb.Querier) (T, error)) (T, error)` | Opens, commits, and rolls back one independent transaction. |
| `(*Ledger).lockAdmission` | `(ctx, tx, e) error` | Method, not a free function; compares the locked country, writer, epoch, and enabled flag. |
| `lockLease` | `(ctx, tx, e) (time.Time, error)` | Locks then reads database time separately; cancellation rejects. |
| `resolveExecution` | `(ctx, tx, run.IntakeWork) (int64, string, error)` | Returns execution ID and key, not a receipt; verifies frozen budget equality. |
| `resolveDecision` | `(ctx, tx, int64, *run.DecisionBinding) (json.RawMessage, error)` | Matches the current execution ID and all binding fields. Does not establish actor authority. |
| `(*Ledger).verifyOutputs` | `(ctx, []model.AssetRef) error` | Verifies each supplied object's size/hash with cancellable bounded reads. Does not discover missing outputs or bind paths to catalog rows. |
| `resultParams` | `(r, string, int64) (intakedb.InsertExecutionResultParams, error)` | Allocates a result key and encodes the immutable request. |
| `classifyPayload` | `(json.RawMessage, r) error` | Normalizes JSON times and uses the frozen replay comparator. |
| `receiptFromResult` | `(ctx, tx, Result) (run.IntakeReceipt, error)` | Returns original execution and transport provenance. |
| `validatePriorAcceptance` | `(ctx, tx, r) error` | Locks only this attempt's projection and rejects eligible-to-ineligible downgrade. It neither returns the state nor enforces the full transition table. |
| `recordAcceptance` | `(ctx, tx, q, r, Result, []model.GapRef) error` | Writes event, projection, revision receipt, and sorted allocated gap keys. Does not add retention or delivery. |
| `insertDelivery` | `(ctx, q, intakedb.InsertDeliveryRecordParams) (intakedb.IngestDeliveryRecord, error)` | Lives in `sources_launches.go`, not `ledger.go`; resolves a conflict winner with a fresh read. |

[ledger.sql](../../internal/corpus/store/intake/queries/ledger.sql) and its generated [ledger methods](../../internal/corpus/store/intake/intakedb/ledger.sql.go) contain `FindExecutionResult`, `InsertExecutionResult`, `InsertRevisionAcceptance`, `UpsertRevisionAcceptanceState`, `InsertDeliveryRecord`, `FindDeliveryRecord`, `ListBuildEligibleAcceptance`, `ListDueUnacknowledgedDelivery`, and `FindPolicyBundle`. `InsertExecutionResult` and `InsertDeliveryRecord` return their generated `...Row` types; the corresponding find methods return table models.

Only `AddBlobRetentionReference` comes from [sources.sql](../../internal/corpus/store/intake/queries/sources.sql). Its generated parameters are `ID` (blob ID), `OwnerKind`, and `OwnerKey`; it returns `(int64, error)`. There is no generated `InsertRevisionReceipt`, gap insert, acceptance-lock helper, or `ReadConversionComparisonEligibility` in `intakedb`. The first two are raw SQL inside `recordAcceptance`; comparison eligibility belongs to `documentdb` and must be expressed anew in acceptance SQL.

**Decision:** Keep cross-schema validation reads in `intake/queries/acceptance.sql` under the existing `corpus_intake` entry.
**Why:** M1.6 owns that file and needs one transaction over all stores. The entry already reads the whole writer migration chain.
**Rejected:** Importing document adapters or their generated package; changing the general ownership of Silver writes.
**Detail:** This file is the narrow exception to the parent spec's store ownership table. Use generated statements for new reads and locks. No new raw SQL is needed merely to lock a row. Reuse existing raw helpers where listed above.

## Request and state contract

All values supplied by the caller are claims to compare with retained evidence. Only syntax and internally computable bindings can be checked before opening the transaction.

| Field | Required binding |
|---|---|
| `Work.Execution` | `DocumentIngest`, `intake`, `document_version`; country and version match admission and the lease. Reprocess children also use this workflow kind. |
| `Work.Stage`, `Work.Shard` | `normalize`, full 112-character `RevisionKey`. Reject identity-only keys even though `ValidateRevisionKey` admits them. |
| `Work.Substep` | `evidence-candidate` for candidate, `evidence-quarantine` for quarantine, `evidence-acceptance` for all terminal states. |
| `Work.DecisionRevision` | Bound decision revision, otherwise zero. |
| `IdempotencyKey` | Exactly `run.IntakeIdempotencyKey(Work)`. Do not add output fields to this key to escape conflicts. |
| `CandidateDigest`, `QualityReportHash` | Hash-pinned candidate manifest and selected document roll-up report. A matching digest column alone does not prove manifest membership. |
| `OriginalHash` | Selected original in the frozen input/candidate manifest, linked to this version's captured observation/artifact. Other attachments may have different originals. |
| `OutputDigest` | The candidate's selected `output_manifest` artifact hash. For a pre-conversion candidate, quarantine, or exclusion with no output manifest, use the selected roll-up report hash. |
| `Outputs` | Complete `(Path, SHA256, ByteSize)` set for the selected protected closure, sorted by hash in byte order with no duplicate hash or path. Match paths to `bronze.blob.object_ref`, not just hash and size. |
| `Decision` | Required for `accepted_with_gaps` with `accept_with_named_gaps`, and `excluded` with `exclude_coverage_gap`. `accepted` may carry only `accept_non_binding_ocr`. None for candidate/quarantine. |
| `Comparison` | Resolve reprocess mode and prior baseline from retained inputs and acceptance history. Never infer their absence from a nil caller field; see comparison restrictions below. |
| `GapAcceptanceRef` | Bound decision key exactly when `Counts.AcceptedGaps > 0`. |
| `Cursor`, `NextEligible` | Empty and nil. This transaction does not schedule retries. |

Separate substeps are necessary: acceptance state and report hash are absent from `IntakeIdempotencyKey`. One fixed substep would make candidate-to-quarantine conflict at decision revision zero. A changed report for the same state under the same key remains a conflict; create linked work or a newly authorized decision, not an arbitrary new shard.

**Decision:** Count one candidate revision as one shard item; derive the following values and reject caller disagreement.
**Why:** [Closure accounting](corpus-write-path.md#closure-accounting-and-evidence) counts unique item outcomes, not findings, blobs, or attempts. `run.Counts` permits this mapping; `Complete` closes work, not legal coverage.
**Rejected:** Counting each gap as another expected item; marking quarantine complete; counting one item in both success and terminal-gap buckets.
**Detail:** Every row has `Expected = Processed = 1`; unlisted counts are zero. These are proposed adapter conventions, not changes to frozen contracts or measured corpus totals.

| State | Outcome | Nonzero outcome counts | Complete |
|---|---|---|---|
| `candidate` | `succeeded` | `Succeeded = 1` | false |
| `quarantined` | `rejected` | `Rejected = 1` | false |
| `accepted` | `succeeded` | `Succeeded = 1` | true |
| `accepted_with_gaps` | `succeeded` | `TerminalGap = AcceptedGaps = 1` | true |
| `excluded` | `terminal_gap` | `TerminalGap = AcceptedGaps = 1` | true |

This mapping settles the mechanical count question within the proposal. Approval of this design approves the convention; it does not approve gaps or owner gates O7/O8.

| Prior state for this revision and attempt | New state | Additional requirement |
|---|---|---|
| none | candidate or quarantined | Retained report; quarantine requires `quarantined` or `needs_ocr`. |
| candidate | quarantined | Same candidate; quarantining report. |
| none, candidate, quarantined | accepted | Report outcome `accepted`, full gates, and no conversion gaps; a quarantined attempt also needs the exact authorized resolution. |
| none, candidate, quarantined | accepted_with_gaps | Report outcome `accepted_with_gaps`, fresh gate over exact retained/excluded ranges, and authorized named gaps. |
| none, candidate, quarantined | excluded | Authorized nonempty coverage gap; accepts no text. |
| accepted, accepted_with_gaps, excluded | any new event | Reject. A new attempt cannot rewrite this attempt's terminal event. |
| any | identical prior result | Replay path only, without another event or projection change. |

Reject unlisted transitions. Pin candidate identity within an attempt. Retry converter and alternate source actions start new attempts. Do not require a fabricated provision to accept a qualified narrative profile. Zero-body coverage must be explicit; a missing body is not an empty-text success. Whether OCR's permanent binding-text gap fits `accepted` needs the contract resolution below.

## Database guarantees and new reads

| Migration | Existing guarantee | Adapter responsibility |
|---|---|---|
| [00002](../../deploy/corpusmigrations/writer/00002_admission_and_ledger.sql) | Result idempotency UNIQUE; acceptance event UNIQUE `(revision_key, processing_attempt_key, watermark)`; projection UNIQUE `(revision_key, processing_attempt_key)`; receipt UNIQUE `(revision_key, execution_result_id)`; immutable event/result/decision/gap triggers. | Exact grants, event/projection consistency, state transitions, authority, and atomic closure. Projection monotonicity is in its upsert query, not a table trigger. |
| [00003](../../deploy/corpusmigrations/writer/00003_sources.sql) | Blob hash UNIQUE; immutable blob, observation, artifact, and observation-artifact rows; same-schema foreign keys; retention UNIQUE `(blob_id, owner_kind, owner_key)`. | Resolve cross-schema execution/result keys, original membership, retained bytes, and document-revision owner. `source_record` has no immutable trigger, but runtime has no update/delete grant. |
| [00004](../../deploy/corpusmigrations/writer/00004_identity_and_revisions.sql) | Identity-country, revision-identity, and provision-key triggers; immutable Silver rows; revision stores both `intake_execution_id` and `intake_execution_key`. | Resolve both origin fields to the same execution and version. No `country` column exists on identity; derive it from `identity_key`. |
| [00005](../../deploy/corpusmigrations/writer/00005_conversion_provenance.sql) | Parent/version triggers; immutable provenance; complete report child count at deferred commit; comparison report/identity and baseline guards. | Complete candidate closure, expected metrics, policy selection, bytes, passing comparison, authority, and live baseline. No comparison outcome or critical-change column exists. |

No cross-schema foreign key proves these application checks. A quality report may have a null `document_revision_id`, including when produced before its revision. Never reject it merely for that null or attach a fake parent. Non-null parents must match exactly. Conversion artifacts, maps, and edit rows require an existing revision under `00005`.

New statement names below are proposed, not generated methods already available. Select explicit columns. Return counts or existence predicates for large sets; page row sets by stable ID with a limit, never an unbounded JSON aggregate. Reject duplicates and missing rows before accepting a join result. Use left joins or anti-joins so an absent blob cannot disappear from closure discovery.

| Proposed statements | Required result |
|---|---|
| `ProvisionAcceptanceLock`, `LockAcceptanceKey`, `ReadAcceptanceAttempt` | Synchronization row, locked ID, and this attempt's projection joined to its exact event/result. Lock projection after synchronization. |
| `ReadAcceptanceRevision`, `ReadAcceptanceOrigin` | Revision/identity and both origin execution fields resolved to the version. A reserved key can have no revision only for ineligible states. |
| `ReadAcceptanceCapture` | Selected observation, source, artifact/link role, original blob, capture/result provenance; match version and pinned observation membership. |
| `ReadAcceptanceArtifacts`, `ReadAcceptanceMaps`, `ReadAcceptanceEdits` | Candidate/version-bound provenance with parent, execution, recipes, hashes, sizes, coordinates, and complete ordinals. |
| `ReadAcceptanceReports`, `ReadAcceptanceReportRecords` | Exact selected roll-up and per-artifact reports, their blob metadata, policy/threshold version, and complete children. |
| `ReadAcceptanceBaseline`, `ReadAcceptanceComparison` | Latest eligible event by `(watermark, id)` for the identity and exact comparison/report pair; use existence checks for newer competing candidates. |
| `ReadAcceptanceClosureBlobs`, `LockAcceptanceBlob` | Full selected dependency set with missing-reference indicators, then each catalog row locked by hash. |

`MaxAcceptanceBlobs = 1024` bounds distinct closure blobs. Reject larger closures rather than truncate. Page other sets at at most 1000 rows and enforce total row, JSON-byte, object-byte, and elapsed limits from the pinned acceptance policy and frozen work budget. Read at most limit plus one to detect overflow. Missing policy limits reject. These bounds include report evidence and source spans, not only top-level rows.

## Lock order and concurrency proof

**Decision:** Acquire admission `FOR SHARE`, the owning document-version lease `FOR UPDATE`, identity synchronization row, attempt synchronization row, this attempt's projection, then all existing closure blobs by SHA-256 in byte order. All locks after admission use `FOR UPDATE`.
**Why:** The [source lock order](corpus-database-sources.md#discovery-claims-and-lock-order) has the same admission/lease prefix and blob suffix. Acceptance touches no window, page, host, or request locks. Identity serialization protects acceptance history across different version leases.
**Rejected:** Silver row locks; advisory identity locks; taking a second document-version lease; blob locks acquired only after referencing inserts.
**Detail:** Provision each synchronization row with `INSERT ... ON CONFLICT DO NOTHING`, then lock it in a separate statement. The identity row is `(identity prefix without :rev:, 'identity')`; the attempt row is `(full revision key, actual attempt key)`. The tuples cannot collide because their first components have different grammar. `model.ValidateKey("identity")` succeeds: no global reserved attempt-key rule or owner decision is needed.

Runtime has column-level update grants on admission, leases, acceptance locks, projections, and blobs. It has only select/insert on Silver; [PostgreSQL locking clauses require an update grant](https://www.postgresql.org/docs/17/sql-select.html). Immutable-row triggers permit locking but reject actual blob updates. Do not acquire locks on immutable decisions, gaps, or Silver rows.

Row immutability does not prevent new child inserts. Existing `Revisions` and `Conversion` writers all take the same document-version lease, so holding it freezes this version's candidate/provision/report membership during acceptance. Verify every selected row's version. Capture membership is the exact hashed observation set, not every observation currently visible. Identity locking protects eligible acceptance membership only when every acceptance writer participates. The nil-verifier composition rule excludes `Ledger.Apply` as a competing acceptance writer; future review application must use this adapter.

| Existing paths | Locks shared with acceptance and absence of a reverse edge |
|---|---|
| `Ledger.RotateAdmission`; `ClaimLease`, `ReclaimLease`, `RegisterExecution`, ordinary `Apply`, `RecordDelivery` | Rotation takes admission only; others take admission before one lease. Results and deliveries follow it. None waits for identity after holding a blob. |
| `Sources.OpenWindow`, `RegisterPage`, `ClaimPage`, `RecoverPage`, `RecordPageCapture`, `RecordPageRetry`, `RecordPageFailure`, `RecordPageDecode`, `ApplyDiscoveryPage`, `ReconcileWindow`, `CloseWindow`, `ResumeWindow` | `sourceTransactionMode` and `prelockSourceAccounting` order window/pages/hosts/requests/blobs after the source lease. Acceptance never waits for that lease or those intermediate rows. |
| `Sources.RegisterHost`, `RegisterRequests`, `ClaimRequest`, `RecordTransport`, `RecoverRequest`, `ExpireRequest`, `CloseRequest`, `ApplyCapture`, `AcceptDetail` | Same prefix; capture may share the document lease or sorted blobs. It takes no acceptance synchronization lock. |
| `Sources.TransferLaunch`, `AcknowledgeLaunch`; `Ledger.RecordDelivery` | Delivery locks/inserts follow their grants. Child execution resolution is a plain read, not a second lease lock. Acceptance's result-key completion cannot become a launch intent. |
| `Revisions.MintDocumentIdentity`, `AppendIdentityAssertion`, `InsertLegacyIdentityMapping`, `InsertDocumentRevision`, `InsertProvisionRevisions` | Admission then document lease, followed by Silver uniqueness/foreign-key locks. Mint additionally takes its observed-assertion advisory lock. Acceptance takes neither that advisory lock nor a Silver row lock. |
| `Conversion.RegisterConversionArtifact`, `AppendEditLedgerEntry`, `RecordConversionQualityReport`, `RecordRevisionComparison`, `InsertOffsetMapSegments` | Same document lease before Silver writes. Blob and acceptance reads are nonlocking; comparison triggers read the projection without locking it. No reverse blob-to-lease or identity edge. |

Read methods in those adapters take their wrapper's prefix or no row locks and add no reverse edge. This proof covers the named fenced adapters, not arbitrary SQL under the runtime role. Future cleanup and permit writers must follow this order and receive adversarial review.

Discover the complete blob set before blob locks. Acquire every blob lock before any retention insert can take a foreign-key `KEY SHARE` lock. Recheck time after waits. If a later validation discovers an earlier lock or another blob is needed, roll back; never extend the set out of order. All same-schema parents referenced by acceptance writes are prelocked blobs, immutable existing executions/results, or rows created earlier in this transaction; none requires an upgrade after taking a child lock.

## Evidence validation

Apply these checks inside the transaction, using database rows and hash-verified bytes. Do not trust caller `RequiresAdjudication`, gap counts, profile flags, or a boolean hidden in decision evidence. Complete conversion membership and passing gates are required only for build eligibility. Candidate, quarantine, and exclusion validate the evidence that exists and record missing conversion explicitly; they may have no revision or converted artifacts, but still require a captured original and bound report. Absence of conversion must not prevent recording its quarantine.

1. Resolve `Work` to its immutable execution, including workflow/attempt, scope, version, fence, epoch, all three digests, and budget JSON. Resolve the frozen input and recipe manifests through those digests. They select the candidate, originals, policy, processing mode, and predecessor; no field in `Outputs` can select policy.
2. Resolve revision and origin. Both origin ID and key must name one registered document-version execution for this version, but need not equal the current execution: reuse preserves origin. Verify identity prefix and country. Read required provisions from the selected candidate manifest, not a mere nonzero row count.
3. Resolve each selected original through Bronze observations and artifacts, including role agreement and execution/result provenance. `OriginalHash` must be the selected original. Per-artifact conversion rows may name other captured originals in the same frozen manifest; never require all attachments to share one hash.
4. Check conversion role/ordinal membership against the candidate manifest. Require the five roles for each selected converted artifact set and exactly the manifest-selected output root. Verify recipe bindings, byte sizes, blob catalog paths, source observations, revision text/metadata hashes, and candidate hash. No schema constraint equates `candidate_digest`, `manifest_hash`, and `blob_hash`; never silently treat them as synonyms.
5. Verify selected offset maps and edit ledgers, including map/source/destination and ledger/input/output hashes, sizes, range bounds, and usable anchors for unavailable byte mappings. No edits may mean an empty retained ledger, not a fabricated edit row. Manifest membership proves completeness; presence of one segment or edit is insufficient.
6. Select the roll-up by revision, version, candidate and report hash with `artifact_hash IS NULL`. Resolve its selected per-artifact reports. Require exact report byte sizes and parent/execution bindings. Check complete ordinals `[0, record_count)` and compare the indexed projection with hash-verified report bytes. Reports are append-only; do not select an arbitrary latest report.
7. Resolve trusted `model.QualityMetricProfile` from the pinned policy, never from report-supplied `mandatory` fields. Require every expected metric exactly once, exact mandatory flags and thresholds, matching policy and threshold version. Use the rules in [QualityReport.Validate](../../internal/corpus/model/conversion.go): mandatory unknown or measured-failed metrics reject; not-applicable requires policy permission and evidence, even though `passed` is false. Reject empty, missing, duplicate, or extra metric sets.
8. For build-eligible states require a passing roll-up and all applicable artifact gates over the selected retained ranges. Unresolved blocking findings reject; a same-key gap does not waive them. Gap acceptance needs a rerun on the exact exclusion manifest. A successful workflow, report outcome string, or approval cannot substitute for the gate.
9. Derive scoped gaps from report records and bound decision evidence as below. Preserve non-binding OCR authority in verified artifacts; this transaction cannot elevate it. Exclusion accepts no searchable text and may retain a failing report.
10. Resolve reprocess requirements and baseline as below. Then verify the complete object closure and current decision authority, not merely the supplied output subset.

The producer-owned wire contracts needed for steps 1 and 4 through 9 are open below. Missing or unsupported contracts reject. M1.6 must not invent a permissive decoder or certify a zero-record `accepted` report.

## Decision evidence, permits, and comparison

**Decision:** Nil authorization permits only work that requires no review decision. Any request carrying a decision or adjudication fails closed without the authority dependency, including on replay.
**Why:** `resolveDecision` proves an immutable row matches, not that its actor still has authority. [Review application](corpus-review.md#application-fencing-and-residency) requires fresh permits; the [Vietnam plan](../plans/vietnam.md#m2-real-vietnam-evidence) explicitly blocks review-backed acceptance until M5.
**Rejected:** Trusting any seeded decision row; an owner allowlist supplied by the caller; nil meaning approved; claiming M1.14 already has M5 authorization.
**Detail:** The proposed `PermitCheck.Check(context.Context, pgx.Tx, run.IntakeApplyRequest) error` is a read-only validation hook, not permit consumption. It resolves authority against exact database decision bindings and has no network call or new locks. M1 may supply a strict synthetic implementation only in tests. M5 must extend the application boundary for transactional one-use consumption and committed-receipt replay before enabling reviewed acceptance. This proposal does not freeze that future consumption API.

`resolveDecision` requires the decision's `execution_id` to equal the resolved current execution. A reclaimed fence requires a newly registered execution and, for decision-backed work, newly bound approval. Changing a binding changes the immutable payload. Only decision-free work can replay with just owner/fence changes under the current helpers. Do not claim that old decision rows authorize a reclaimed owner.

**Decision:** Propose versioned decision evidence for gap projection, with exact ranges in the already hash-bound scope manifest.
**Why:** Scope keys and reason codes describe a gap but cannot prove retained/excluded byte ranges or reviewer authority.
**Rejected:** Equating a gap list with a gate rerun; using a free-text reason as a public code; accepting caller-supplied gaps.
**Detail:** Proposed minimum JSON is `{"schema_version":1,"gaps":[{"scope_kind":"provision","scope_key":"article-1","reason_code":"TEXT_CORRUPT"}]}`. Resolve `scope_manifest_digest` from the decision row. Its versioned bytes must bind candidate, original/output hashes, and exact retained/excluded `model.Span` values. The report must prove the same selection. This shape remains open pending the producer handoff; no existing decision producer implements it.

Map report gap `code`, not free-text `reason`, to `GapRef.ReasonCode`. Compare canonical sets of `(kind, key, reason code, report hash)` and reject duplicates. Named-gap decision sets must equal the selected report gap set; exclusion must cover the whole excluded candidate and include every selected report gap. `GapRef.Validate` checks neither key grammar nor membership, so acceptance must do both. A provision scope uses its revision-local `local_key`; the full `provision_key` contains colons and fails `00002`'s non-document gap grammar. Missing expected items need no fabricated row. Resolve them through the frozen inventory; do not demand M2 part tables.

**Decision:** Reject comparison-dependent build eligibility in M1 until a trusted passing-result and adjudication contract exists. Continue to record ineligible candidate/quarantine evidence.
**Why:** `revision_comparison` records hashes, report IDs, recipe, and watermark, but no passing outcome or critical marker. `model.DecisionKind` and the `00002` CHECK admit no adjudication action. Requiring any existing action for every comparison would authorize the wrong act and still prove no pass.
**Rejected:** Treating a comparison row as passed; trusting `RequiresAdjudication`; treating `accept_with_named_gaps` as owner adjudication; claiming M2.1 already owns an action/schema change.
**Detail:** A retained reprocess input always requires comparison, including unchanged-output reuse. A new candidate differing from an eligible candidate of the same identity must not bypass that check by changing source version. Use identity-wide acceptance history, not only the current `document_version_key`. Baseline and policy selection across source versions must be explicit in the frozen input contract.

For future eligibility, require both exact report bindings: comparison candidate report hash equals `QualityReportHash`; prior report hash equals the prior accepted event's report. Require prior revision/candidate and a currently eligible event/projection joined on acceptance ID, revision, attempt, state, and watermark, with a nonzero matching `acceptance_watermark`. Derive competing-newer-candidate staleness using the predicate in [conversion.sql](../../internal/corpus/store/document/queries/conversion.sql): same identity, greater watermark, digest outside both the prior and candidate digest. It intentionally excludes the newly accepted candidate itself so response-loss replay does not invalidate itself. That predicate proves freshness only, not a passing comparison. The identity lock holds through commit.

## Transaction sequence and replay

1. Before `BeginTx`, run `request.Validate()` and the syntax/count/substep checks above. Reject an empty acceptance state. Apply no database-derived checks here.
2. In `ledgerTransaction`, lock admission, then lease; run `ValidateAt` using the returned database time. Resolve execution and look up the result key. For a pre-existing result, lock existing identity and attempt keys without provisioning; missing synchronization rows reject as an integrity error. Otherwise provision/lock identity then attempt keys. Lock/read prior projection with its event. Recheck lease and decision expiry after waits.
3. Resolve all supplied decisions with `resolveDecision`, and perform the configured read-only authority check when required. Check for a committed result with `FindExecutionResult`. Recheck grant time before classifying its payload. Valid changed payload returns `ErrPayloadConflict` before object reads or state-transition validation.
4. For an identical result, validate retained evidence and current grants, but do not apply new-event transition rules or recompute the need for a comparison from the result's own acceptance. Use its stored event and originally bound baseline. Require exactly its original acceptance event and revision receipt, matching scoped gaps and completion intent, and existing retention edges where required; never repair a partial commit on replay. A later projection may supersede that event. A candidate/quarantine replay after later terminal acceptance returns the old receipt without downgrading the projection. A truly stale comparison still rejects where the comparison contract requires it.
5. For new work only, enforce transitions and candidate binding. `validatePriorAcceptance` is only an additional downgrade guard. For both paths, perform the evidence checks, discover the complete closure, lock all blobs in order, and recheck time. Derive `AssetRef` values from the catalog and compare `Outputs` to the exact set. Missing, extra, or aliased references reject.
6. For both paths, use `verifyOutputs` on the database-derived references after locks, including on replay; corrupt bytes reject. For a replay, return `receiptFromResult` after the final grant check; perform no inserts, including no new retention edge. Existing synchronization rows must already exist for receipts issued by this adapter.
7. For new work, call `resultParams` and `InsertExecutionResult`. Always follow it with a new `FindExecutionResult` statement. `ON CONFLICT` may wait for a winner invisible to its statement snapshot. Recheck grants and classify again. Only the winner whose allocated `ResultKey` matches writes the commit set. A loser validates the existing commit set and returns its original receipt without changing it; if this transaction provisioned synchronization rows, roll it back and retry the read path rather than commit those rows on replay.
8. Write the commit set below. Read the receipt; recheck lease and `ValidateAt` immediately before returning to `ledgerTransaction` for commit. A business rejection/conflict after a wait also gets a final time check so expiry takes precedence. An aborted SQL transaction or cancellation returns its preserved cause and rolls back; never issue more statements on an aborted transaction.

Admission and lease locks prevent concurrent rotation, reclaim, or cancellation during the transaction; wall time can still expire. `ErrStaleGrant` wins over replay/conflict after syntactic validation. SQL, object, decoding, and rejection errors retain typed causes without exposing document bytes, decision JSON, paths, or driver detail in logs. No helper redaction guarantee should be inferred from `ledger.go`'s generic error wrapper.

## Atomic commit and protected closure

Insert in this order: execution result; acceptance event with `watermark = result.id`; projection; revision receipt; gaps sorted by allocated `gap_key`; retention edges sorted by blob hash; completion delivery. `recordAcceptance` handles the middle four operations. Verify after its upsert that the projection points to this event with matching state/watermark; the helper currently tolerates an empty upsert result and is not itself that proof.

Protect accepted and accepted-with-gaps closures with `owner_kind = 'document_revision'`, `owner_key = RevisionKey`. Pre-existing edges are reused. Candidate, quarantine, and exclusion require existing protection for every retained dependency through an execution or observation edge whose owner resolves to the selected provenance; never delete or transfer it here. Check those edges while holding the blob locks. `Conversion` currently registers provenance but adds no retention edges, so its future `ApplyConversion` composition must provide that protection; do not assume the metadata adapter already does it. An absent revision cannot receive a document-revision edge. The result-key delivery uses kind `completion`, recipient workflow ID, receipt kind/key `result`/result key, expected admission epoch, `OutputDigest`, and `due_at = result.created_at`. `Complete = false` still permits a result notification; it does not terminate the workflow.

The closure starts with selected inputs, candidate/output manifests, originals and source captures, conversion blobs/manifests, roll-up and artifact report blobs, maps, edit ledgers, and any comparison result. Include map source/destination hashes, edit input/output hashes, report `evidence` assets and `source_spans` artifact hashes, decision scope manifest, and referenced source-observation, text, metadata, recipe and policy assets. Traverse only the versioned manifest formats, deduplicate by hash, and apply bounds before allocation. Inline policy JSON remains a retained database dependency; a digest is not automatically a Bronze blob. Each required object must resolve to a catalog row and verified bytes. A union of only `conversion_artifact.blob_hash`, `map_hash`, and `ledger_hash` is not this closure.

A failed transaction adds no result, event, projection update, receipt, gap, edge, delivery, or synchronization row; pre-existing rows remain unchanged. Previously staged objects remain under their existing protection. Sequence values may advance on rollback; do not assert otherwise. Crash after commit returns the stored receipt on a current-grant retry; `ListDueUnacknowledgedDelivery` supports bounded notification recovery. Signals never create acceptance.

## Hosted verification and slice boundary

M1.6 owns only the acceptance adapter/test, `queries/acceptance.sql`, reviewed generated `intakedb` output, and the prefix test in `cmd/corpus-migrate/migration_integration_test.go`. Keep schema, `run`, `model`, document adapters, workflows, review services, and producer implementation outside this PR. Resolve the consuming wire-contract handoff before implementation; do not expand M1.6 into a gate engine or approval service.

The existing `generation` job runs chain/store tests before `sqlc generate`. The primary first obtains a queries-only hosted artifact, reviews/imports it, then integrates the adapter and tests in one reviewable PR. No local generation or tests. G `generation` proves schema replay and drift; I `intake-postgres` is the intake evidence lane in the owning plan, not a subjob of `generation`. Require independent adversarial review of grants, mutation order, closure, replay, and every rejection path.

`TestWriterM1Prefix` applies `-to 5`, checks the version-5 inventory and table boundaries, and asserts absence of `silver.part_revision`, `silver.evidence_target`, `config.taxonomy_release`, `gold.chunk_set`, and `ingest.corpus_build`. Connect all adapter calls as the CI-provisioned runtime login, never the migrator. Use `Ledger` for admission/lease/execution, synthetic captured blobs/observations, `Revisions` and `Conversion` for evidence, and `Acceptance` for a gated revision with a scoped gap. Seed a synthetic decision and explicitly injected test authority only for this fixture. It proves transaction mechanics, not real approval or conversion quality. Preserve M1's required scoped-gap fixture; missing contracts cannot be replaced by skipping it.

Assert the complete commit set and eligible projection, exact receipt replay, unchanged row sets on failure, and byte verification using synthetic object storage. Use barriers and bounded two-connection waits for races. Required cases:

- All five states, candidate-to-quarantine-to-resolution with distinct keys, terminal-state rejection, historical nonterminal replay, and metadata-only gaps without fabricated provisions.
- Stale admission/writer/fence/owner, cancellation, and expiry after identity/blob/object waits; changed payload conflicts before opening the changed object; stale grant takes precedence.
- Missing or foreign revision/origin/capture, wrong path/size/hash, incomplete role/map/report closure, missing manifest member, and missing nested report evidence; multi-original attachments remain supported.
- Empty/duplicate/missing metrics, forged mandatory flags/thresholds, missing policy, invalid not-applicable evidence, unknown mandatory geometry/fidelity, unresolved blocking findings, and mismatched retained/excluded ranges.
- Missing authority, revoked/expired or wrong-execution decision, action mismatch, gap-scope substitution, malformed evidence, and decision-backed reclaim that cannot replay with an old binding.
- Required comparison omitted, cross-version baseline bypass, wrong prior/candidate report, stale third candidate, and no trusted pass/adjudication contract: all fail closed. No positive adjudication test may substitute a gap decision.
- Two acceptances for different versions of one identity serialize; the waiter rereads history. Race document child insertion under the same lease, source capture with shared blobs, and opposite input blob order. Verify no partial commit or lock cycle.
- Crash before commit, lost response after commit, no replay edge/delivery duplication, fresh-snapshot conflict winners, cancellation during object reads, rollback errors, and closure overflow.

## Open contracts and external follow-ups

1. **Open, owner/primary scope decision before M1.6 implementation:** assign and freeze the persisted input/candidate/report/policy wire contract jointly with M1.12/M1.13. Existing types define metric validation but not how `config.policy_bundle.manifest`, roll-up bytes, candidate membership, recipe selection, processing mode, and exact retained ranges encode it. Define versioning, canonical hash rules, selected report/role ordinals, bounds, closure references, and upstream execution-retention insertion. Recommend a separate contract handoff, without changing `run.IntakeApplyRequest`; do not leave implementers to invent these formats.
2. **Open, owner before reviewed acceptance:** approve the minimal decision evidence/scope-manifest shape and resolve OCR `accepted` with a permanent binding-text gap versus `model.Accepted` meaning no accepted gaps and the parent's public-gap projection admitting only accepted-with-gaps/excluded events. Do not silently drop the OCR gap or relabel its authority. The count mapping above does not settle this semantic conflict.
3. **Open, owner/primary assignment for reprocessing:** freeze a trusted comparison-result schema with outcome and critical-change classification, exact owner adjudication action/binding, and unchanged-output comparison semantics. `00005` forbids equal prior/candidate digests; the frozen request has no reprocess-mode field. M2.1 produces reprocess work, M2.4 owns comparison persistence, and M5 owns approval application; their current scopes do not assign all needed `run`/`model`/`00002` changes. Choose a consuming contract slice; M1.6 cannot make those changes.
4. **Resolved producer ownership:** M5.1 owns writer application/workflow files; M5.2 owns approval and audit; M5.3 hands correction-loop wiring to M5.1. Use that existing handoff for authorized `ApplyDecision` rows and permit consumption. M1.7 binds activities but does not gain permission to implement review persistence. No current adapter inserts decisions; M1 fixtures seed them. Freeze the exact producer API in the M5 brief.
5. **External documentation correction:** the parent prose says every newer candidate invalidates a comparison, while the implemented predicate excludes both the prior and candidate digests. Preserve the implemented replay-safe predicate and reconcile that parent sentence in its owner's next edit. Only the acceptance linking sentence is owned here. The documentation index already links this file; no index addition is needed.
6. **Later work, not a waiver:** M5 must reconcile consumed-permit replay with the frozen intake port's current-grant/decision-expiry rules before enabling review-backed calls. Retention transfer/removal waits for [sources open question 2](corpus-database-sources.md#open-questions). Build/release membership arrives in M3. None is implemented or validated by this design.
