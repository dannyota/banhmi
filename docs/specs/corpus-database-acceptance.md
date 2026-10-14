Proposal, not implemented. This continuation of the [corpus database spec](corpus-database.md) owns the `ApplyEvidence` transaction for writer files `00001` to `00005`. The [Vietnam plan](../plans/vietnam.md#m16-fenced-cross-store-acceptance) owns delivery as slice M1.6.

# Corpus database acceptance

`ApplyEvidence` is the one transaction that accepts a gated document revision. It reads the intake ledger, Bronze capture, Silver identity and revisions, and Silver conversion provenance, then commits the acceptance event, its projection, the result receipt, scoped gaps, retention edges, and the completion delivery, or writes nothing. It adds no schema. It never writes a `silver` or `bronze` evidence row; those rows must already exist.

The frozen contract is [run.IntakeUnitOfWork](../../internal/corpus/run/intake.go) over [run.IntakeApplyRequest](../../internal/corpus/run/intake.go). The adapter is `Acceptance` in `internal/corpus/store/intake/acceptance.go`, with `NewAcceptance(db TransactionStarter, writer string, objects blob.Store, permits PermitCheck) (*Acceptance, error)` and `Apply(ctx, run.IntakeApplyRequest) (run.IntakeReceipt, error)`. `Acceptance` satisfies `run.IntakeUnitOfWork`. It reuses the unexported helpers of [ledger.go](../../internal/corpus/store/intake/ledger.go): `lockAdmission`, `lockLease`, `resolveExecution`, `resolveDecision`, `verifyOutputs`, `resultParams`, `classifyPayload`, `receiptFromResult`, `validatePriorAcceptance`, `recordAcceptance`, and `insertDelivery`. Its own SQL lives in `internal/corpus/store/intake/queries/acceptance.sql` under the existing `corpus_intake` sqlc entry.

The workflow activities `ApplyEvidence` and `ApplyQuarantine` in [corpus workflows](corpus-workflows.md#document-processing-and-quarantine) both call `Acceptance.Apply`. `ApplyEvidence` carries a terminal state. `ApplyQuarantine` carries `quarantined`. `Ledger.Apply` keeps serving results without an acceptance state and keeps failing closed on an acceptance state without a verifier; M1.6 does not implement `AcceptanceVerifier`, and M1.7 routes every request with a non-empty `Acceptance` to `Acceptance.Apply`.

## Input contract

**Decision:** Take exactly `run.IntakeApplyRequest`, with the field bindings below, and read every other fact from the database inside the transaction.
**Why:** The plan forbids M1.6 from changing `run` or `model`. The frozen request already carries the work grant, revision key, candidate digest, report hash, original hash, decision binding, comparison binding, gap acceptance reference, and staged outputs. Scoped gaps, actor authority, report outcome, provenance, and comparison eligibility are ledger facts; the [port comment](../../internal/corpus/run/intake.go) says the adapter resolves them rather than trusting the caller.
**Rejected:** A new request type or options struct beside the frozen one; caller-supplied gap lists; caller-supplied provenance row IDs.
**Detail:** Fixed bindings for every call:

| Field | Binding |
|---|---|
| `Work.Execution` | `WorkflowKind` is `DocumentIngest`, scope kind `intake`, subject kind `document_version`. Reprocess children are `DocumentIngest` executions and use the same lease. |
| `Work.Stage`, `Work.Substep`, `Work.Shard` | `normalize`, `evidence-acceptance`, and the full `RevisionKey`. The shard puts the revision into `IntakeIdempotencyKey`, so a different revision under the same attempt is a different result. |
| `Work.DecisionRevision` | `Decision.DecisionRevision`, or `0` without a decision. |
| `IdempotencyKey` | `run.IntakeIdempotencyKey(Work)`. |
| `Acceptance`, `RevisionKey` | A non-empty state and the 112-character revision key, which is the reserved key when no `silver.document_revision` row exists yet. |
| `CandidateDigest`, `QualityReportHash` | The candidate manifest digest and the `report_hash` of the document roll-up report (`silver.quality_report.artifact_hash IS NULL`) for that candidate. |
| `OriginalHash` | The Bronze original blob for the document version, reached through `bronze.observation_revision`, `observation_artifact`, `artifact`, and `blob`. Every `silver.conversion_artifact.original_hash` of the candidate must equal it. |
| `OutputDigest` | The `blob_hash` of the candidate's `output_manifest` conversion artifact at ordinal `0`. When the candidate has no conversion artifact, which only `quarantined` and `excluded` permit, it equals `QualityReportHash`. |
| `Outputs` | Staged `model.AssetRef` values of the protected closure. Required non-empty for `accepted` and `accepted_with_gaps`. Each must match a `bronze.blob` row by SHA-256 and size and be referenced by a provenance row of this candidate. |
| `Decision` | Required for `accepted_with_gaps` (`accept_with_named_gaps`) and `excluded` (`exclude_coverage_gap`), permitted for `accepted` only with `accept_non_binding_ocr`, forbidden otherwise. `retry_converter` and `attach_alternate_official_source` never accompany an acceptance; they start new attempts. |
| `Comparison` | Required when a build-eligible acceptance exists for the same document identity and the same `document_version_key` with a different candidate digest; forbidden otherwise. `ComparisonHash` is `silver.revision_comparison.result_hash`. |
| `GapAcceptanceRef` | `Decision.DecisionKey` exactly when `Counts.AcceptedGaps > 0`. |
| `Cursor`, `NextEligible` | Empty and nil. Acceptance never schedules a retry. |

One shard item is the candidate revision. `Counts` and `Outcome` per state:

| State | `Outcome` | `Counts` (`Expected`, `Processed`, then nonzero outcomes) | `Complete` |
|---|---|---|---|
| `candidate` | `succeeded` | 1, 1, `Succeeded` 1 | false |
| `quarantined` | `rejected` | 1, 1, `Rejected` 1 | false |
| `accepted` | `succeeded` | 1, 1, `Succeeded` 1 | true |
| `accepted_with_gaps` | `succeeded` | 1, 1, `TerminalGap` 1, `AcceptedGaps` 1 | true |
| `excluded` | `terminal_gap` | 1, 1, `TerminalGap` 1, `AcceptedGaps` 1 | true |

`Acceptance.Apply` rejects an empty `Acceptance` and any binding above that does not hold, before opening a transaction.

## Transaction composition

**Decision:** Validate document and provenance rows with read-only queries in `queries/acceptance.sql`, keyed by revision key, candidate digest, and hashes. Do not call the `document` adapters inside the transaction and do not add methods to them.
**Why:** The `document` adapters open their own transactions and commit; their contract forbids nesting, and their methods are writers, while acceptance needs reads. The [parent spec](corpus-database.md#topology-decision) assigns cross-schema validation at acceptance to the fenced adapter. One query file in one package keeps the review in one pass. The `corpus_intake` entry already reads the whole writer chain as schema input, so `silver` tables are visible to it.
**Rejected:** Document adapters over a savepoint-opening starter, because `Revisions` and `Conversion` call `BeginTx`, `Commit`, and `Rollback` on what they hold and document that they never receive a nested transaction; new transaction-scoped exported methods on the document adapters, because M1.6 does not own `internal/corpus/store/document/`; raw-SQL-only validation in Go, because the plan names `acceptance.sql` and the generated child package as owned paths.
**Detail:** `acceptance.sql` is the one place where the `intake` store reads `silver`. It never inserts into `silver`. Its queries, all `:many` or `:one` reads unless named otherwise:

| Query | Reads | Returns |
|---|---|---|
| `ReadAcceptanceRevision` | `silver.document_revision`, `document_identity`, provision count | Revision row, identity key, `intake_execution_key`, provision count, for `revision_key` and `document_version_key` |
| `ReadAcceptanceOriginal` | `bronze.observation_revision`, `observation_artifact`, `artifact`, `blob` | The original blob row linked to an observation of this `document_version_key` with the requested SHA-256 |
| `ReadAcceptanceProvenance` | `silver.conversion_artifact` joined to `bronze.blob` | Every artifact row of (`revision_key`, `candidate_digest`) with its blob size and `object_ref` |
| `ReadAcceptanceReport` | `silver.quality_report` joined to `bronze.blob` | The roll-up report row for (`revision_key`, `candidate_digest`, `report_hash`, `document_version_key`) |
| `ReadAcceptanceReportRecords` | `silver.quality_finding` | All records of that report, bounded by `record_count` plus one |
| `ReadAcceptanceComparison` | `silver.revision_comparison`, `ingest.revision_acceptance`, `revision_acceptance_state` | The comparison for (`prior_candidate_digest`, `candidate_digest`, `result_hash`) with the eligibility predicate of `ReadConversionComparisonEligibility` |
| `ReadIdentityAcceptance` | `ingest.revision_acceptance_state`, `revision_acceptance`, `silver.document_revision` | Build-eligible acceptances of the identity, with each revision's `document_version_key` and candidate digest, bounded |
| `ReadAcceptanceClosureBlobs` | `silver.conversion_artifact`, `offset_map_segment`, `edit_ledger`, `quality_report`, `revision_comparison`, `bronze.blob` | Distinct blob IDs and hashes of the candidate closure, bounded by `MaxAcceptanceBlobs` plus one |

`MaxAcceptanceBlobs` is a package constant of 1024; a closure above it rejects. Row locks and inserts reuse existing statements: the raw lock helpers in `ledger.go`, `InsertExecutionResult`, `InsertRevisionAcceptance`, `UpsertRevisionAcceptanceState`, `InsertDeliveryRecord`, and `AddBlobRetentionReference` from [sources.sql](../../internal/corpus/store/intake/queries/sources.sql). The two new lock statements (identity lock row, blob prelock) are raw queries in `acceptance.go` with the existing `RAW QUERY` comment convention.

The hosted generation job runs the store tests before it regenerates, so an adapter that calls a brand-new generated query cannot compile on the first run. Follow the M1.4 and M1.5 phase 2 precedent: push a queries-only branch with `acceptance.sql`, review and import its `generation` artifact into `intakedb`, then land queries, generated code, adapter, and tests as one pull request. One reviewer reads the queries, the adapter, and the tests in one pass; no split is needed.

## Lock order

**Decision:** Extend the [source accounting lock order](corpus-database-sources.md#discovery-claims-and-lock-order) as: country admission `FOR SHARE`; owning `document_version` lease `FOR UPDATE`; `ingest.acceptance_lock` identity row `FOR UPDATE`; `ingest.acceptance_lock` attempt row `FOR UPDATE`; `ingest.revision_acceptance_state` row of this attempt `FOR UPDATE`; `bronze.blob` rows of the closure by `sha256` in byte order `FOR UPDATE`, one statement each. Take no row lock on `silver` tables, `ingest.quarantine_decision`, `ingest.evidence_gap`, or Bronze rows other than `blob`.
**Why:** PostgreSQL requires `UPDATE` privilege on a table to use any locking clause on it ([SELECT](https://www.postgresql.org/docs/17/sql-select.html)). Writer `00004` revokes `UPDATE` on every `silver` table from the runtime role and `00005` grants `SELECT, INSERT` only, so a `silver` row lock is impossible without a grant change, which is a schema change. Those rows are immutable (`reject_immutable_change` triggers, no `DELETE` grant), so a row read in this transaction cannot change before commit; a key or digest that resolves now resolves at commit. `bronze.blob` has `GRANT UPDATE (id)` for exactly this lock, and `AddBlobRetentionReference` locks the blob again, so prelocking in sorted order prevents an upgrade deadlock after an earlier insert's `KEY SHARE`. The identity row serializes acceptances of one document identity so a comparison checked as eligible is still eligible at commit. `ApplyEvidence` touches no window, page, host, or request row, so it conflicts with source accounting only on admission, lease, and blobs, where both orders agree.
**Rejected:** `FOR SHARE` on `silver.document_revision`, blocked by privileges; advisory locks per identity, which the sources spec rejects for unrelated serialization; locking the blob only inside `AddBlobRetentionReference`, which orders blobs by insertion rather than by key.
**Detail:** The identity lock row is `(revision_key = <identity key>, processing_attempt_key = 'identity')` in `ingest.acceptance_lock`; its `revision_key` CHECK admits the identity key because the `:rev:` suffix is optional, and `'identity'` cannot collide with a real attempt key because `model.ValidateKey` keys never equal that reserved word by rule of this spec. Provision both lock rows with `INSERT ... ON CONFLICT DO NOTHING`, then lock each with a separate `SELECT ... FOR UPDATE`, as `Ledger.Apply` does for the attempt row. Discover the closure blob hashes without locks, sort, lock, then recheck the lease and clock. If validation later needs a blob that was not prelocked, abort with an error and let the caller retry; never lock out of order. Insert in this fixed order: `ingest.execution_result`, `ingest.revision_acceptance`, `ingest.revision_acceptance_state` (upsert), `ingest.revision_receipt`, `ingest.evidence_gap` sorted by `gap_key`, `bronze.blob_retention_reference` sorted by `sha256`, `ingest.delivery_record`. Each insert takes `KEY SHARE` only on parents locked or created earlier in this order.

## Validation sequence

Steps run in this order inside one `READ COMMITTED` transaction. "Adapter" means Go code or an `acceptance.sql` predicate; "database" means a constraint or trigger that exists today.

1. `request.Validate()` before the transaction; the binding table above (adapter).
2. Admission `FOR SHARE`: country, epoch, admitted writer, enabled (adapter; the database checks shape and monotonic rotation only).
3. Lease `FOR UPDATE` and a separate `clock_timestamp()` read: owner, `attempt_fence`, epoch, `cancelled_at IS NULL`, unexpired (adapter). Cancellation always blocks acceptance; there is no reconciliation mode for evidence.
4. `request.ValidateAt(now)`: decision and adjudication expiry (adapter).
5. Identity lock, attempt lock, prior state lock; then lease and time again (adapter).
6. `resolveExecution`: workflow identity, attempt key, fence, epoch, scope, resource, input, recipe, budget digests, and frozen budget JSON (adapter; the database enforces `UNIQUE (workflow_id, attempt_fence)`).
7. `resolveDecision` for `Decision` and for `Comparison.Adjudication`: all bound fields, `execution_id`, actor key, action, fence token, decision epoch, revision, scope, hashes, `expires_at > clock_timestamp()` (adapter query; the database enforces immutability and vocabularies). Then `permits.Check` when a `PermitCheck` is configured (see M5 below).
8. Revision: for `accepted` and `accepted_with_gaps`, `ReadAcceptanceRevision` must return the row with `document_version_key` equal to the lease key, identity country equal to the admission country, and `intake_execution_key` resolving to an `ingest.execution` row with `resource_kind = 'document_version'` and `resource_key` equal to the lease key (adapter; the database enforces the revision-key and identity-country triggers). `accepted` requires at least one `silver.provision_revision`; `accepted_with_gaps` with zero provisions requires a gap of kind `document_revision` naming `RevisionKey`. `candidate`, `quarantined`, and `excluded` accept an absent revision row.
9. Capture: `ReadAcceptanceOriginal` must return the `OriginalHash` blob linked to an observation of the lease key (adapter).
10. Provenance: for states with a revision row, `ReadAcceptanceProvenance` must return the five roles `raw_blocks`, `normalized_blocks`, `raw_text`, `normalized_text`, and `output_manifest` at ordinal `0`, every `blob_hash` and `manifest_hash` present in `bronze.blob` with equal size, every `original_hash` equal to `OriginalHash`, and `conversion_recipe_digest` and `normalization_recipe_digest` equal to the revision's columns; `OutputDigest` equals the `output_manifest` blob hash (adapter; the database enforces parent binding per row, not set completeness).
11. Report: `ReadAcceptanceReport` must return the roll-up report; its `outcome` must permit the state per the transition table; `ReadAcceptanceReportRecords` must return exactly `record_count` rows (adapter; the database enforces the deferred count trigger at insert time). For `accepted` and `accepted_with_gaps`: no `metric` record with `mandatory` and (`metric_state = 'unknown'` or `passed = false`), and no `blocking` finding outside the accepted gap scopes. For `accepted`: zero `gap` records.
12. Gaps: derive the scoped gaps as defined under [Decision evidence and gaps](#decision-evidence-and-gaps). Every gap's `ReportHash` is `QualityReportHash`; `accepted_with_gaps` and `excluded` require at least one gap (adapter; `Ledger.Apply` already enforces the same count rule).
13. Reprocessing: `ReadIdentityAcceptance` decides whether a comparison is required (binding table). When `Comparison` is present, `ReadAcceptanceComparison` must return an eligible row with `revision_key`, `document_version_key`, `prior_revision_key`, and `prior_candidate_digest` equal to the bindings; the prior revision's current state must be build-eligible with that candidate digest and `watermark` equal to the comparison's `acceptance_watermark`; `RequiresAdjudication` must be true and `Adjudication` present; the adjudication decision's `evidence.comparison_result_hash` must equal `ComparisonHash` (adapter; the database enforces comparison baseline shape at comparison insert, not at acceptance).
14. Prior state and transition: `validatePriorAcceptance` plus the transition table (adapter; the database enforces only the state vocabulary and the watermark-monotonic upsert).
15. Idempotency and outputs: `FindExecutionResult`, `classifyPayload`, lease and time again, `verifyOutputs` over the staged bytes, and the output-to-blob binding from the input contract (adapter).
16. Writes in the lock-order sequence, then lease and time one last time (adapter).

Transitions for one `(revision_key, processing_attempt_key)`:

| From | To | Rule |
|---|---|---|
| none or `candidate` | `candidate` | Report present with any outcome; identical replay only when a `candidate` event already exists. |
| none or `candidate` | `quarantined` | Report outcome `quarantined` or `needs_ocr`; no decision. |
| none, `candidate`, or `quarantined` | `accepted` | Report outcome `accepted`; from `quarantined` only with `accept_non_binding_ocr` and a report whose `report_hash` differs from the quarantined event's. |
| none, `candidate`, or `quarantined` | `accepted_with_gaps` | Report outcome `accepted_with_gaps`; decision `accept_with_named_gaps`. |
| none, `candidate`, or `quarantined` | `excluded` | Any report outcome; decision `exclude_coverage_gap`. |
| `accepted`, `accepted_with_gaps`, `excluded` | any different state | Rejected; terminal for this attempt. A new attempt key starts over. |
| any | same state, same payload | Replay returns the committed receipt and writes nothing. |

## Decision evidence and gaps

**Decision:** Scoped gaps come from the database, never the caller: the `gap` records of the roll-up report and, for `accepted_with_gaps` and `excluded`, the `gaps` array in `ingest.quarantine_decision.evidence`. The evidence object has the shape `{"gaps": [{"scope_kind", "scope_key", "reason"}], "comparison_result_hash": "<64 hex>"}`; `comparison_result_hash` is present only for an adjudication.
**Why:** The frozen port says exact gap scope is resolved from the ledger record inside `Apply`. `evidence` is `jsonb` with no agreed shape, and M1.6 is its first reader, so M1.6 must fix the minimal fields it reads. Field names mirror `silver.quality_finding` columns so one comparison rule serves both sources.
**Rejected:** Caller-supplied gap lists; free-text evidence; deriving gaps from `Counts`.
**Detail:** A gap record maps to `model.GapRef{Kind: scope_kind, Key: scope_key, ReasonCode: code, ReportHash: report_hash}`; an evidence gap maps the same way with `reason` as the code. For `accepted_with_gaps` the evidence set must equal the report's gap record set exactly. For `excluded` the evidence set must be non-empty and include every report gap record; it may add one `document_revision` gap naming `RevisionKey`. For `quarantined` and `candidate` the gaps are the report's gap records, possibly none. `evidence_gap.scope_key` for `document_revision` must be a full revision key; other kinds use `model.ValidateKey`. Reason codes are the [quarantine reason family](document-conversion.md#quarantine-and-review); the adapter checks non-empty text, not membership, until the conversion spec freezes the vocabulary as Go constants.

## Atomic commit set

**Decision:** One transaction creates exactly: one `ingest.execution_result`; one `ingest.revision_acceptance` event with `watermark = result.id`; the `ingest.revision_acceptance_state` upsert; one `ingest.revision_receipt`; one `ingest.evidence_gap` per scoped gap; for `accepted` and `accepted_with_gaps`, one `bronze.blob_retention_reference` edge per closure blob with `owner_kind = 'document_revision'` and `owner_key = RevisionKey`; one `ingest.delivery_record` of kind `completion` with `deduplication_key = result_key`, recipient `Work.Execution.WorkflowID`, `receipt_kind = 'result'`, `expected_hash = OutputDigest`, `due_at = result.created_at`.
**Why:** These are the rows the parent spec names for fenced acceptance and the rows `Ledger.Apply` already writes, plus the retention edge that the [sources spec](corpus-database-sources.md#blob-retention-owner-kind) assigns to `ApplyEvidence`. Immutable evidence rows are inputs, so staged objects a failed transaction leaves behind are unreferenced staging objects.
**Rejected:** Inserting or repairing `silver` or Bronze rows from acceptance; transferring or deleting `execution` retention edges here (the removal protocol is unresolved); decision or launch deliveries (M1.7 and M5 own those).
**Detail:** Must already exist: `bronze.blob` rows and objects, `source_record`, `observation_revision`, `artifact`, `observation_artifact` (`ApplyCapture`); `silver.document_identity`, `document_revision`, `provision_revision` (`Revisions`); `conversion_artifact`, `offset_map_segment`, `edit_ledger`, `quality_report`, `quality_finding` (`Conversion`); `revision_comparison` (`ApplyComparison`); `ingest.quarantine_decision` (`ApplyDecision`). The closure for retention edges is the `OriginalHash` blob, every `conversion_artifact.blob_hash` and `manifest_hash`, every `quality_report.report_hash` of the candidate, every distinct `offset_map_segment.map_hash` and `edit_ledger.ledger_hash` of the candidate, and the bound comparison `result_hash`. `quarantined`, `candidate`, and `excluded` add no `document_revision` edge; their blobs keep their `execution` edges.

## Replay, conflict, recovery, and late callers

The rules are those of `Ledger.Apply`, applied after every grant check, so a stale grant wins over a replay or a conflict:

- Identical payload under a current grant: steps 1 to 14 run again, `FindExecutionResult` finds the row, `classifyPayload` passes, and the committed receipt returns with its original execution and transport provenance. No row is written, including no retention edge.
- Changed payload under an existing idempotency key: `run.ErrPayloadConflict`, no writes. `SameImmutableIntakePayload` ignores owner and fence, so a reclaimed lease with the same immutable payload is a replay, not a conflict.
- Stale epoch, owner, fence, expired or cancelled lease, expired or mismatched decision or adjudication: `run.ErrStaleGrant`, no writes, even for a replay.
- Crash after commit or lost response: the caller re-sends the same request and receives the committed receipt; bounded reconciliation finds the unacknowledged `completion` delivery through `ListDueUnacknowledgedDelivery`. Signals only wake readers.
- Crash before commit: the transaction rolls back; staged objects stay staging objects; no event, projection, gap, edge, or delivery exists.
- Late caller with an old fence after a reclaim: stale. Late caller whose lease was cancelled: stale, including for replay. A fresh owner under a new fence may replay the identical payload and receives the stored receipt.

## Negatives

Each row writes nothing and returns the named error. Hosted `intake-postgres` tests in `acceptance_integration_test.go` cover every row and assert equal row counts in all written tables before and after.

| Negative | Where it fails | Error |
|---|---|---|
| Stale admission epoch, writer, or disabled | Step 2 | `ErrStaleGrant` |
| Stale fence, other owner, expired, or cancelled lease | Steps 3, 5, 15, 16 | `ErrStaleGrant` |
| Stale, expired, or unbound decision; unbound adjudication | Step 7 | `ErrStaleGrant` |
| Changed replay payload | Step 15 | `ErrPayloadConflict` |
| Crash or response loss after commit | Replay path | Committed receipt, no new rows |
| Missing gate: no roll-up report, wrong report hash, outcome not permitting the state, failed or unknown mandatory metric, blocking finding outside gaps | Step 11 | Rejection error, not a grant error |
| Missing provenance: no revision row where required, missing role, blob size mismatch, original or recipe mismatch, output not in closure | Steps 8 to 10, 15 | Rejection error |
| Missing or stale comparison; comparison required but absent; adjudication absent | Step 13 | Rejection error for absence, `ErrStaleGrant` for a stale comparison or adjudication |
| Downgrade of a build-eligible attempt or any transition out of a terminal state | Step 14 | Rejection error |
| Gap set mismatch between report and decision evidence; `accepted` with gap records | Step 12 | Rejection error |
| Closure above `MaxAcceptanceBlobs` or a blob discovered after prelock | Lock order | Rejection error; retry with a fresh transaction |

Cancellation cannot accept evidence because step 3 has no reconciliation mode. Reusing a blob or revision from an earlier attempt never skips a step, because every step reads by key and digest, not by attempt.

## TestWriterM1Prefix and hosted checks

`TestWriterM1Prefix` in `cmd/corpus-migrate/migration_integration_test.go` replays the writer chain with `-to 5` as the migration login, asserts the inventory at version 5, asserts that `silver.part_revision`, `silver.evidence_target`, `config.taxonomy_release`, `gold.chunk_set`, and `ingest.corpus_build` do not exist, and runs `assertWriterTableBoundaries`. It then connects as the runtime login provisioned by the CI bootstrap (`banhmi_corpus_writer_app`, trust authentication, the chain URL with the user replaced) and drives a synthetic acceptance through the real adapters: `intake.Ledger` for admission, lease, and execution; direct SQL for one `bronze.source_record`, the blobs, one `observation_revision`, one `artifact`, and one `observation_artifact`; `document.Revisions` for identity, revision, and two provisions; `document.Conversion` for the five conversion artifacts, one offset map segment, one edit ledger entry, and one roll-up report with one `provision` gap record; direct SQL for one `accept_with_named_gaps` decision whose evidence names that gap; then `intake.Acceptance.Apply` with `accepted_with_gaps`. It asserts one acceptance event and projection in state `accepted_with_gaps`, one revision receipt, one `evidence_gap` of kind `provision`, one `document_revision` retention edge per closure blob, one `completion` delivery, and that `ListBuildEligibleAcceptance` returns the revision. It replays the same request and asserts the same receipt with unchanged row counts, then replays with `AttemptFence + 1` and asserts `ErrStaleGrant` with unchanged row counts. Pinning `-to 5` keeps the test meaningful after `00006` lands. Running as the runtime login proves the privilege model: the test fails if any statement needs a lock or grant the runtime role lacks.

`acceptance_integration_test.go` in `internal/corpus/store/intake` covers the five states, every negative above, the identity lock (two concurrent acceptances for one identity serialize and the second sees the first's event), the blob prelock against a concurrent `Sources` capture registering the same blob, and a positive reprocess acceptance whose comparison and adjudication bind as specified. Both files run under the hosted `generation` job's `intake-postgres` lane; the generated `intakedb` methods come from the queries-only artifact described above. No legacy mutation is called, no review approval is granted, and nothing is built, published, or activated.

## Later slices

- M5 review: `PermitCheck` is an `intake` port, `Check(ctx, pgx.Tx, run.IntakeApplyRequest) error`, called at step 7 inside the transaction. M1.6 accepts a nil check and then trusts the bound ledger decision row alone; M5 supplies the permit verifier that consumes a fresh one-use permit, and M1.14 composition must pass it. `NewAcceptance` records whether a check is configured so M5 can make nil a construction error.
- Adjudication: `model.DecisionKind` has no adjudication action and `silver.revision_comparison` has no critical-change marker, so M1.6 requires adjudication for every comparison and binds it through `evidence.comparison_result_hash`. The slice that lands `Reprocess` (M2.1) must add an adjudication action and a marker before a clean reprocess can be accepted without a gap decision.
- `ApplyDecision` persistence: no adapter inserts `ingest.quarantine_decision`; M1 tests seed rows directly. The owning slice is unassigned between M1.7 and M5.
- Gap scopes of kind `part` are stored but not resolved to rows until M2 adds `silver.part_revision`; M1.6 validates their key grammar only.
- Retention edge removal and transfer of `execution` edges wait for the dependency-proof protocol in [sources open question 2](corpus-database-sources.md#open-questions).
- Build membership, `build` and `release` edges, and `CorpusBuild` snapshots arrive with writer `00010` in M3.

## Open items

1. Owner: confirm the count convention (one shard item is the candidate revision) and the `Outcome` mapping per state.
2. Owner: confirm the decision `evidence` shape above as the minimal contract the review spec extends, and the `'identity'` reserved attempt key in `ingest.acceptance_lock`.
3. Owner: assign the `ApplyDecision` producer and the adjudication action and marker to a named slice before M2.1.
4. Primary: add this file to the [documentation index](../README.md) row for the database specs; the index is outside M1.6's owned paths.
5. Contradiction: the parent spec's store table assigns `silver` queries to the `document` store; this spec makes `acceptance.sql` the one `intake` reader of `silver`. The parent's one-sentence link records the exception.
6. Contradiction: the `AcceptanceVerifier` port in `ledger.go` says the cross-store adapter supplies its checks; this spec supplies a separate `Apply` instead and leaves `Ledger.Apply` fail-closed for acceptance states. Retiring the port is a later `ledger.go` change.
