Not implemented. The [Vietnam plan](../plans/vietnam.md#m116-country-admission-slots) owns delivery.
This continuation of [country admission slots](corpus-database-admission.md) defines recovery,
caller resolution and queue arbitration. Persistence choices remain proposed for design review.
The parent owns configuration, schema, lock order, transactions and the neutral admission port.

# Admission recovery and dispatch

## Workflow and recovery identity

Use `document-ingest/<country>/<version-key>/<attempt-key>` exactly, as frozen in the
[DocumentIngest contract](corpus-workflows.md#workflow-contracts). Validate the country, version
and attempt against registered document work before reservation, binding or resolution. Never
interpret the third component as a source. Slot identity is the immutable workflow ID plus its
registered version/attempt; mutable lease owner, epoch, fence and Temporal Run ID are not identity.

The prerequisite `codex/intake-child-ids` slice is in progress, not merged. It changes
`internal/corpus/store/intake/sources_launches.go` to require this layout and adds immutable
`ingest.source_child_binding` in writer `00003_sources.sql`. Its `workflow_id` is UNIQUE; it
retains `source_id`, `external_id` and `acceptance_result_id`, inserted with page acceptance.
One child binds to one source record. M1.16 implementation starts only after that slice merges.
Admission resolves source provenance through that binding and the immutable launch delivery.
M1.16 adds no forward reference from `00002` to `00003`. Source transfers cannot change the binding.

Reclaim of a still-open logical workflow retains its workflow ID, attempt and slot. A linked
recovery after terminal failure is a new attempt, never a reuse of the failed workflow ID.
Its attempt key is `recovery-` plus lowercase SHA-256 of length-prefixed UTF-8 fields in this order:
domain `document-recovery-v1`, country, version key, failed attempt key and failed slot key.
Its workflow ID uses the same frozen layout with that recovery attempt key. Its launch delivery
deduplication key is `recovery-` plus the same 64 lowercase hex characters, matching the existing
delivery-key CHECK and `model.ValidateKey`. These keys do not contain owner, fence, epoch,
run, retry count or mutable configuration. Changed inputs or recipes require a separate reprocess
request, not reinterpretation of this recovery key.

The M1.7 recovery-intent persistence companion slice owns the intent producer, separately from
workflow consumption. It owns `internal/corpus/run/recovery.go`,
`internal/corpus/store/intake/recovery_launches.go`, matching tests, and recovery queries in
`intake/queries/ledger.sql` plus reviewed hosted `intakedb` output after M1.16 handoff.
The trusted `ControlReconcile` activity calls that producer under its current control grant.
After acquiring/registering the successor document grant, the producer validates a released
`execution_failed` predecessor and its closure receipt, preserves original inputs/recipe/budget
digests, and atomically records the immutable recovery intent and launch delivery. It links the
successor registration's predecessor to the failed execution and retains the failed slot/receipt
references. A replay reuses the same child and intent; changed payload fails without writes.
The producer must reuse existing ledger/delivery storage or obtain a separate reviewed schema
contract before implementation; M1.16 does not add recovery-intent schema or enable those launches.

M1.7 owns dispatch and calls Reserve on that durable intent. The control owner may dispatch only
its verified recovery intent, never an arbitrary child or source launch. M1.14 owns composition
and end-to-end evidence. Linked recovery launches remain disabled until the companion's neutral
producer API, storage mapping, authorization and atomicity pass independent review and hosted
checks. Reclaim/resume of an existing slot does not depend on this companion. No producer or
workflow may mark the original source item resolved merely because recovery was scheduled.

## Failure and uncertain-start fencing

Reconcile first settles retained acceptance events under the parent contract. For terminal failure,
the trusted observer must identify the slot's current logical workflow chain. Under the target
lease lock, compare the lease's epoch, owner and fence with the bound slot tuple. If all match,
cancel that lease in the transaction that records `closing/execution_failed` and releases
processing. This fence must commit before processing capacity is available to another slot.
If a reclaimed tuple differs, leave it unchanged; the old tuple already fails store fences.
Revalidate the observed slot revision and binding under the lock. A changed slot forces a fresh
observation and retry. Never cancel a replacement grant based on an old observation.

The immutable close receipt retains the prior binding, observed chain/status reference, terminal
cause, resulting revision and lease disposition (`cancelled_matching`, `already_cancelled`, or
`superseded`). A superseded disposition retains the compared tuple as local audit data, never
as authority over that grant. Release open capacity only in a subsequent confirmed-closure
transition. A late orphaned Apply after the fence commits returns `ErrStaleGrant` and writes
nothing. If Apply wins the target lease lock first, Reconcile sees and settles its committed
acceptance before deciding whether a failure transition still applies.

**Decision:** A live country control owner may durably abandon an uncertain reservation whose
bound grant is lost before transport attempts to drain that workflow ID.
**Why:** A returned start command may escape even when its acknowledgement is lost. Transport
cancellation alone cannot revoke database authority or prevent a concurrent successful Bind.
**Rejected:** Start then cancel before committing a stop; release on not-found; cancel an unrelated
reclaimed grant; treat timeout as closure.
**Detail:** `Close(abandoned)` accepts a live registered `ControlReconcile` actor for the same
country. Under the prescribed locks, require the expected revision, unacknowledged bound launch,
and `reserved` or `open_only/worker_loss`. The bound grant must be expired by database time,
cancelled, older-epoch, or superseded. A live matching grant rejects abandonment. If a retained
acceptance needs settlement, Close returns conflict without writes; control calls Settle, then
Resolve before retrying. No caller boolean or transport failure grants this authority.

Close atomically commits `closing/abandoned`, clears demand, releases processing if held, and
fences the still-matching lease using the failure comparison above. It preserves a different
reclaimed grant. CauseReceipt is empty: the close receipt is the durable control stop instruction.
It records the actor, prior binding, lost-grant cause, lease disposition, workflow ID and resulting
revision. Commit uncertainty requires identical Close replay, never speculative transport draining.

Only after obtaining that committed receipt may the M1.7 drain activity resolve or start the exact
reserved ID with duplicate rejection and use-existing conflict handling. It must not allocate a
new ID to clear uncertainty. The control-owned drain activity uses Resolve under its live control
grant and passes only the screened stop receipt reference to the draining execution. The execution
confirms closing through that control-authorized activity path, skips Bind and all processing,
and terminates. Do not send the control grant through Temporal or authorize a read with the
cancelled document grant. An already running execution must also respect
the stop; a delayed start's Bind rejects closing/released. A current matching database grant was
cancelled before drain, so orphan activities cannot write. A replacement grant cannot process
this slot without a successful Bind, which closing/released forbids.

The drain activity requests cancellation and obtains trusted terminal logical-chain proof before
Reconcile releases open capacity. Failure to prove closure keeps the slot open. Crash after the
stop commit but before drain is recoverable from the close receipt and Scan. Bind and Close
serialize on the target lease then capacity/slot. If fresh Bind wins, revision or lost-grant
validation rejects the stale Close. If Close wins, Bind rejects and processing never begins.
An ordinary start command already in flight cannot bypass this check.

## Trusted closure and backpressure

The observer is injected by composition, not a caller-supplied boolean. Observe outside database
transactions, then lock and compare the expected slot revision, workflow identity and binding.
Terminal proof covers the logical chain including Continue-As-New successors; closure of an old
Run ID is insufficient. Receipts retain opaque run/chain references and status, never payloads.
Unknown/not-found, timeout, lease expiry, missing heartbeat and old visibility results never
release open capacity. Continue-As-New keeps one slot and never repeats reservation. No new
attempt reuses a workflow ID, including after Temporal history retention.

Reserve and Resume return normal refusals: `processing_full`, `open_full`, `both_full`,
`resource_busy` for another live version attempt, or `not_turn`. Return the configuration revision
and occupancy. Neither refusal acknowledges a launch or changes its immutable due time/key.
Existing unacknowledged-launch accounting prevents source completion. M1.7 records `incomplete`
with `admission_capacity` through normal window closure before waiting. A crash between commits
can leave a progressing window, never a complete one. Recovery does not refetch or reset budgets.

## Capacity-feasible FIFO heads

**Decision:** Maintain separate launch and resume FIFOs with a persisted alternating queue turn.
**Why:** Resumes already own open capacity. Bypass an infeasible head without letting sustained
arrivals in either queue starve the other feasible head.
**Rejected:** Cross-queue timestamp arbitration; one merged FIFO blocked by an infeasible launch;
skipping locked heads; resetting age on retry or rebind.
**Detail:** Launch order is `(due_at, deduplication_key COLLATE "C")`. Resume order is
`(resume_requested_at, slot_key COLLATE "C")`. A repeated RequestResume preserves its first demand
time until Resume or closure clears it. Rebind and retry preserve that time.

After acquiring capacity, use new statements for counts and head selection among committed rows.
Exclude future launches, cancelled launch owners, already reserved intents, versions with another
live slot, and closing/released slots. Resume eligibility requires open-only state, persisted
demand and a valid bound document grant; expired quarantine is ineligible. RequestResume records
durable demand only under that live bound grant; it stores no wake-up proof. M1.7 validates the
durable wake-up (decision signal or worker-loss recovery) before calling it. Reconciliation, Bind, settlement and releases bypass both
queues. Dispatcher authority does not exclude a head; its source owner or authorized control
dispatcher must dispatch it. A locked head remains a head, never a reason to select a later row.

The launch head is feasible only when both classes fit after +1. The resume head is feasible
when processing fits after +1; it adds no open occupancy, including above a lowered open ceiling.
Under the capacity lock, read the singleton's `next_queue`, seeded as `launch`. When both heads
are feasible, select that queue's head. Only its successful Reserve or Resume flips `next_queue`
to the other queue, atomically with the slot change and receipt. When only one head is feasible,
select it without consuming the turn. The infeasible head keeps its place within its own queue.
If neither fits, return the capacity refusal; an eligible feasible request that is not the selected
head returns `not_turn`. Refusals, rollback, receipt replay and all non-increasing operations leave
the turn unchanged. Configure, restart, rebind and source transfer never reset it. No timestamp
comparison crosses queues. While both heads stay feasible, at most one committed admission from
one queue can precede the other's turn, even with continuously arriving backdated launches.

Do not use SKIP LOCKED, transaction arrival order, lock acquisition order, or caller authority to
overtake the selected head. Acquire all needed earlier locks before capacity. If selection reveals
a need for an earlier lock, roll back and restart; never acquire it under capacity. A waiter that
arrives first at capacity must still reselect and yield to the selected head. Counts and
selection cannot reuse the snapshot from before the capacity wait.

With defaults 32 processing and 256 open, a fixture has 256 open `worker_loss` slots, zero
processing, and an older launch blocked on open capacity. Rebind and queue resumes, then admit
them in resume order up to processing capacity. Completion and confirmed closure free open
capacity; the older launch becomes feasible and competes under the persisted turn. Recovery must
succeed without forced cancellation, artificial expiry, limit increases or deleting intents.

Use a durable timer at the configured control-reconciliation interval, proposed 15 minutes, or
a release wake-up; the timer remains mandatory because wake-ups can be lost. Control Scan uses
`(updated_at, id)` keyset order, limit `1..1000` additionally capped by `Work.Budget.MaxItems`, and
wraps at sweep end. Each slot uses its own transaction. Emit `country_admission_waiting` as the
aggregate number of eligible launch and resume demands, with country and refusal class only.
Alert when demand exists and a ceiling prevents either queue head. Never count retries as new
work or expose subject, version, workflow, source-window, attempt, title, URL or receipt keys.

## Authorized resolution

Extend the parent's neutral port with `Resolve`. These types live in M1.16's `run/admission.go`:

```go
type AdmissionResolveRequest struct {
    Actor IntakeWork
    DocumentWork *IntakeWork // exactly one selector
    WorkflowID string
}
type AdmissionBinding struct {
    ExecutionID, Epoch, Fence int64
    Owner string
}
type AdmissionResolution struct {
    Found bool
    SlotKey, WorkflowID, VersionKey, AttemptKey string
    Revision int64
    Binding AdmissionBinding
    State AdmissionState
    WaitReason string
    TerminalReason AdmissionTerminalReason
    LifecycleReceipt, SettlementReceipt, CloseReceipt, ReleaseReceipt string
    TerminalResultID, SettledWatermark int64
    ChainReceipt string
}
```

Resolve is read-only: no slot, receipt, lease claim, bind, revision increment or transport call.
Authorize a live registered document actor for its own workflow/version/attempt, or a live country
control actor for that country. Source and arbitrary document actors cannot inspect another child.
DocumentWork must resolve to registered immutable inputs; WorkflowID must satisfy the frozen
layout and resolve to registered work. A control actor can inspect a tombstone or lost binding.
A freshly reclaimed document actor for the same work can resolve before Bind. Its own live grant
authorizes the read even when the slot still records the lost grant. No key possession grants access.

Use one consistent SQL statement snapshot for admission, actor grant, registered target, slot and
receipt references; validate current epoch/writer, owner/fence, cancellation and database expiry
time. Missing registered work rejects; a registered target without a slot returns Found=false.
Found=true returns the current binding, revision, state and lifecycle references listed above.
LifecycleReceipt names the receipt that established the current state/cause, including worker loss;
absent causes use empty references, not fabricated evidence. The result is a snapshot, not a grant.
M1.16 owns `ResolveDocumentAdmission` in `intake/queries/ledger.sql`, adapter authorization and
runtime-role tests. M1.7 owns only calls and screened reference transport.

Before initial Bind, resolve by registered document work. On revision conflict, resolve again,
revalidate the intended action against current state/causes, and retry with that revision only if
still applicable. Never blindly replace ExpectedRevision on a stop or other mutation. After
receipt loss, first retry the identical operation for its immutable receipt; Resolve then supplies
current state even if later transitions occurred. If the caller also lost its old revision/key,
Resolve and its cause references recover progress without inventing a new mutation to fetch a
receipt. Released means terminal accounting, not permission to recreate the slot.

## Required negatives

M1.16's hosted runtime-role transaction checks must cover:

- Failed-chain reconciliation fences a matching live orphan grant before processing release;
  late Apply returns `ErrStaleGrant` and all evidence/result/delivery rows remain unchanged.
  A reclaimed unrelated grant survives. An Apply that wins first is settled before failure.
- Lost-grant control abandonment commits its stop and lease fence before any drain start/resolve.
  A live matching grant, wrong actor/country, changed revision or acknowledged launch rejects.
  Crash/response loss replays the stop receipt; no transport drain runs on uncertain commit.
- Start-before-stop, Bind-before-Close and Close-before-Bind barriers preserve the stop contract.
  A superseded lease survives but cannot Bind the closed slot. A draining start does no processing;
  not-found and stale Run ID closure retain open capacity. M1.14 proves actual transport ordering.
- The 256-slot worker-loss fixture recovers with zero initial processing and an older blocked
  launch. Independent FIFO order survives retries, rebind, restart and source transfer. A locked
  head cannot be overtaken; once open capacity is free, the launch competes under the saved turn.
- Release enough processing/open capacity before each selection to keep both heads feasible.
  Continuously enqueue launches, including backdated `due_at` values older than waiting resume
  demand; assert resumes receive every other contested admission. Reverse the fixture with sustained
  resumes and assert the same bound for launches. Preserve each queue's stated order. Refusals,
  rollback, identical replay and uncontested progress cannot consume the turn. Restart, Configure,
  rebind and transfer preserve it.
- Resolve before Bind, after receipt loss, after later transitions and after revision conflict
  returns coherent current state with zero writes. Wrong actor, country, workflow or registered
  inputs fails. Old resolution cannot authorize a mutation after rebind, cancellation or closure.
- Reject source-based workflow layout and version/attempt mismatch. The prerequisite's immutable
  source-child binding prevents a second source record for one child. Same-work reclaim keeps its
  ID; terminal recovery uses the new deterministic attempt ID. Companion short checks validate
  `recovery-<64 lowercase hex>` and reject the colon form. Companion runtime-role database checks
  insert that delivery through the unchanged delivery-key CHECK, then prove identical intent and
  delivery replay adds no row. Changed payload rejects; no launch enables before producer readiness.

M1.14 measures the actual admission transaction mix at 100 transactions per second, separately
from Temporal action throughput. Record capacity-lock wait, mean hold time and settlement delay.
Mean hold must remain below 10 ms to avoid sustained queue growth at that target; also record
tail hold times and observed queue growth. An unmeasured or failing target blocks throughput claims.
Independent adversarial fix confirmation and hosted evidence remain open; this design claims
no implemented, real-document or deployed recovery behavior.
