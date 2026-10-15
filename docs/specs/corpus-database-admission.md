Admission persistence is coded; hosted checks and independent code review remain pending. The [Vietnam plan](../plans/vietnam.md#m116-country-admission-slots) owns delivery.
This continuation of the [corpus database spec](corpus-database.md) defines the country slot contract.
The configurable-bound approach and proposed defaults are approved design for implementation under
[O7](corpus-workflows.md#o7-m1-configurable-bounds). The persistence choices below are merged and
independently reviewed design; O7 does not approve production values, new review authority, or deployment.

# Country admission slots

## Boundary and decisions

Count admitted `DocumentIngest` attempts across every source and reprocess parent in one country
writer. Source, batch, build, promotion, and control executions consume neither document class.
There is no country column outside `ingest.country_admission`. A slot is durable work accounting,
not a parser semaphore, an activity slot, or a live database connection.

**Decision:** Reserve processing and open capacity together when dispatching an existing launch
intent, before returning permission to start. Count persisted slot rows under one capacity row lock.
**Why:** A start acknowledgement is too late to exclude concurrent parents. Counted rows cannot
drift from separately maintained counters. The capacity lock also serializes absent-row insertion.
**Rejected:** Workflow-local counters; capacity in `ClaimLease`; reserving at discovery; admitting
first and compensating after start; counting execution rows or Temporal visibility.
**Detail:** `RegisterExecution` remains input registration under a resource lease. Existing source
discovery requires that registration before committing a launch intent. Registration alone grants
no permission to launch or process. Pending intents consume neither class. Reserved but unconfirmed
starts consume both. No slot is allocated by an acknowledgement. Use the frozen workflow ID
`document-ingest/<country>/<version-key>/<attempt-key>` for slots and resolution. The merged child-ID slice ([PR #73](https://github.com/dannyota/banhmi/pull/73)) supplies source child binding.
Source transfer receipts, `ScopedExecution`, and `IntakeUnitOfWork` remain unchanged. The
[recovery continuation](corpus-database-admission-recovery.md) owns identity and recovery details.

**Decision:** Settle slots in a separate fenced transaction after acceptance commits.
**Why:** The merged acceptance transaction owns evidence and its locks. A crash before settlement
retains excess capacity; it cannot admit excess work or lose an acceptance receipt.
**Rejected:** Releasing before acceptance; changing acceptance states or `Complete` to close a
workflow; calling an independently committing adapter from inside another transaction.
**Detail:** M1.7 calls settlement before entering a quarantine wait or returning a terminal result.
Control reconciliation repeats it after response loss. Accepted evidence releases processing;
confirmed workflow closure releases open capacity. The closing interval still counts as open.
An unresolved expiry gap is lifecycle accounting, never an accepted gap or build eligibility.

## Configuration

`ingest.document_admission_limit` is a singleton created by migration, initially revision zero with
no configured limits. Missing configuration fails closed. `Configure` requires a live country
`ControlReconcile` execution, expected revision, and a locally supplied validated configuration.
It commits all bounds, revision + 1, and an immutable receipt together. Identical replay returns
the original receipt. Ordinary reservations never supply an effective limit or overwrite it.

`DocumentAdmissionLimits` has `Processing int32`, `Open int32`, `ReceiptJSONBytes int32` and
`QuarantineExpiry time.Duration`. Defaults are 32, 256, 65536 bytes and seven days.
Validate `1 <= ReceiptJSONBytes <= 2147483647`; this is a representation range, not a recommended ceiling.
Expiry must be positive, a whole microsecond and fit a Go duration.
Store expiry in microseconds; freeze it in each slot at reservation. Later configuration changes
affect new slots only. Quarantine due time is its committed event time plus that frozen duration.
Validate `1 <= Processing <= Open <= 2147483647`; zero, negatives, overflow and reversed bounds
reject without writes. This representational maximum is not a capacity recommendation. Disable
new work through existing admission/cancellation controls, never a zero or negative sentinel.
Configuration is local to the writer; only its revision and aggregate values may enter telemetry.
Production configuration remains owner-gated under O7. M1.14 binds the approved configuration
source; workflow replay never reads mutable configuration directly.

Lowering a limit never evicts admitted work, changes its receipt, or revokes its lease. The next
reservation or open-only resumption compares current occupancy against the current limits. Admit
only if each increased class fits after the operation. Rebinding an occupied slot does not increase
occupancy and remains legal above a lowered ceiling. Release and reconciliation remain legal.
No grandfathered workflow configuration can raise the database ceiling.

## Schema in writer 00002

Edit `deploy/corpusmigrations/writer/00002_admission_and_ledger.sql` in place, after its existing
ledger tables. The writer chain is editable under the [chain rule](corpus-database.md#chain-mutability-and-verification);
disposable hosted replay does not make it append-only. If a tag or durable deployment appears before
implementation, stop and hand the changed baseline to the integrator; do not silently choose a new
migration. Update the writer object inventory in that same implementation slice through its owner.

Every new table has `id bigint GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY`. All columns below
are `NOT NULL` except those marked nullable. Use text CHECK vocabularies and matching typed Go
constants with `Valid()`. Keys use the existing 1-128-character `model.ValidateKey` grammar;
digests are 64 lowercase hex characters. Times are `timestamptz`, normalized to UTC microseconds.
New slots start at revision 1; keys use 16 random bytes as lowercase hex after their prefix.
Timestamp defaults are `clock_timestamp()`; updates use database time. Seed only the limit singleton.
Same-schema foreign keys use single IDs without cascading deletion. No new cross-schema key or
reference to writer files 00003 and later is allowed in 00002.

| Table | Columns and constraints beyond `id` |
|---|---|
| `ingest.document_admission_limit` | `CHECK (id = 1)`; `revision bigint CHECK (revision >= 0)`; `next_queue text DEFAULT 'launch' CHECK (next_queue IN ('launch', 'resume'))`; nullable `processing_limit integer`, `open_limit integer`, `receipt_json_bytes integer`, `quarantine_expiry_us bigint`; `updated_at timestamptz`. CHECK explicitly requires all four NULL at revision zero, otherwise all four non-NULL, `1 <= processing_limit <= open_limit`, `receipt_json_bytes > 0` and `1 <= quarantine_expiry_us <= 9223372036854775`. No defaults for configured values. |
| `ingest.document_admission_slot` | `slot_key text UNIQUE` with `^slot-[0-9a-f]{32}$`; `delivery_id bigint UNIQUE REFERENCES ingest.delivery_record(id)`; `workflow_id text UNIQUE` length 1-512; `document_version_key text`; `processing_attempt_key text`; `origin_execution_id bigint REFERENCES ingest.execution(id)`; `current_execution_id bigint REFERENCES ingest.execution(id)`; `admission_epoch bigint > 0`; `lease_owner text`; `attempt_fence bigint > 0`; `revision bigint > 0`; `quarantine_expiry_us bigint` with the limit table's range; `state text`; nullable `wait_reason text`, `resume_requested_at timestamptz`, `terminal_reason text`, `terminal_result_id bigint REFERENCES ingest.execution_result(id)`; `created_at`, `updated_at timestamptz`. |
| `ingest.document_admission_receipt` | `receipt_key text UNIQUE` with `^admission-[0-9a-f]{32}$`; `operation_key text UNIQUE` with `^sha256:[0-9a-f]{64}$`; nullable `slot_id bigint REFERENCES ingest.document_admission_slot(id)`; `actor_execution_id bigint REFERENCES ingest.execution(id)`; `operation text`; `payload_digest text`; `limits_revision bigint > 0`; `receipt_json_bytes integer > 0`; `request jsonb` and `response jsonb`, each CHECK object; `created_at timestamptz`. Immutable UPDATE/DELETE trigger. |

Slot states: `reserved`, `processing`, `open_only`, `closing`, `released`.
`wait_reason` is present exactly for `open_only`, with `quarantine` or `worker_loss`.
`resume_requested_at` is allowed only for `open_only`. `terminal_reason` is present exactly for
`closing` and `released`, with `completed`, `terminal_gap`, `cancelled`, `expired`, `abandoned`,
or `execution_failed`. Add `settled_watermark bigint NOT NULL DEFAULT 0 CHECK (settled_watermark >= 0)`
to slots; it advances only with settlement and never decreases. `terminal_result_id` is optional.
The adapter verifies result ownership and meaning. A terminal acceptance supplies its actual ID.
Released rows remain as tombstones. No deletion, resurrection, or identity rebinding is permitted.
UNIQUE `(document_version_key, processing_attempt_key)` prevents a second slot for one attempt.
A partial UNIQUE `(document_version_key) WHERE state <> 'released'` prevents overlapping live
attempts on the same resource. A successor waits until the predecessor closes and releases.

Receipt operations: `configure`, `reserve`, `bind`, `settle`, `request_resume`, `resume`,
`worker_loss`, `close`, `release`. A configure receipt alone has NULL `slot_id`.
Request/response JSON is local. Bound each canonical UTF-8 JSON encoding by `ReceiptJSONBytes`
before insertion. Each receipt freezes the enforcing limits revision and byte ceiling.
New mutations use the locked configuration; Configure uses its validated new revision and ceiling,
including initial configuration. If either encoding exceeds the ceiling, roll back with no writes.
Lowering never invalidates committed receipts. For authorized replay, compare typed fields first
and validate frozen ceiling L in `1..2147483647`, promoted to int64. Bound each raw JSONB encoding at
twice L before decoding: with int64 byte length n, reject if `n > L && n - L > L`. Never multiply
in int32. This permits PostgreSQL separator spaces, following `conversion_catalog.go` replay.
After bounded decoding, re-encode the retained typed request and response canonically and require
each size <= L. Use the frozen bindings and ceiling, never current Actor or configuration values.
Malformed JSON, invalid frozen bounds or either size violation returns `ErrAdmissionIntegrity`
without writes. Do not reject an existing operation under the new ceiling. JSON contains only the
neutral fields below, retained execution bindings, and transport status references.
It contains no document bytes, actor names, URLs, provider error strings, or secret material.
The payload digest hashes canonical request fields in declaration order with length-prefixed UTF-8
strings, big-endian signed-int64 values, and UTC microsecond timestamps; nil has a separate tag.
Hash operation keys the same way with domain `country-admission-v1`, country, method, stable
target (delivery key, slot key, or `limits`), and expected revision. Prefix the SHA-256 hex with
`sha256:`. Exclude Actor from payload equality and exclude mutable child owner, fence and epoch.
Actor authorizes the call; original grants remain in the receipt for audit. Compare all other fields.
Changing any immutable operation field at an existing key returns `ErrPayloadConflict`.

Required indexes: slot `(updated_at, id) WHERE state <> 'released'` for reconciliation; partial slot
`(resume_requested_at, slot_key COLLATE "C") WHERE resume_requested_at IS NOT NULL`;
slot `(origin_execution_id, id)`, `(current_execution_id, id)`, and `(terminal_result_id, id)`;
receipt `(slot_id, id)` and `(actor_execution_id, id)`. Existing UNIQUE indexes cover delivery,
workflow, attempt and operation lookups. Add delivery `(due_at, deduplication_key COLLATE "C")
WHERE delivery_kind = 'launch' AND acknowledged_at IS NULL` for ordered admission selection.
The singleton needs no additional index.

Runtime gets SELECT/INSERT and sequence USAGE. Limit UPDATE grants cover configuration columns
and `next_queue`; slot UPDATE grants cover only current binding, revision, state, settled watermark, wait/terminal fields and
updated time. Triggers reject DELETE, changes to frozen slot identity, decreasing revision, and
any update to a released row. Every state update must advance revision by exactly one. Configuration
updates advance revision by exactly one and preserve `next_queue`. Queue-turn updates change only
`next_queue`, preserving configuration, revision and updated time; enforce both update shapes by trigger.
The [queue rule](corpus-database-admission-recovery.md#capacity-feasible-fifo-heads) owns turn changes.
Grant no export access to these three private tables.
Reuse the immutable trigger for receipts. Revoke PUBLIC function execution as in existing 00002.

## Ownership, locks and atomic reservations

All transactions use `READ COMMITTED`. The adapter resolves identities without locks, then locks
and rechecks them. Never add a newly discovered earlier lock after beginning mutation; roll back
and restart. Each call handles one slot, not an unbounded batch.

1. Country admission `FOR SHARE`; validate enabled country, admitted writer and current epoch.
2. All needed existing resource leases `FOR UPDATE`, sorted by `(resource_kind, resource_key)` in
   byte order. Include actor and target document lease, deduplicated. A control reconciler needs
   its control lease and the target lease. No admission operation creates a lease while holding
   capacity. Missing leases reject; claim/reclaim and execution registration happen beforehand.
3. Source window, when checking source authority, then existing delivery rows in deduplication-key
   byte order, each `FOR UPDATE`. Use the existing source ownership/transfer checks. No invented
   source window. Reprocess reservation stays disabled until a reprocess intent producer exists.
4. Capacity singleton `FOR UPDATE`, then the slot if present `FOR UPDATE`.
5. Admission operations acquire no acceptance, host, page, request, catalog guard, or blob lock.
   They read immutable results under the target lease. Acceptance and source transactions retain
   their existing lock orders and never call this adapter inside their transaction.

The merged source order is admission, lease, window, then delivery for launch acknowledgement.
The merged acceptance order is admission, document lease, identity/attempt synchronization,
projection, then blobs. The new capacity suffix cannot form a cycle with either: a capacity
holder never asks for another lease, delivery, or acceptance lock. Do not add a capacity read lock
inside acceptance. All multi-row inserts use fixed table order and byte-ordered UNIQUE keys:
slot first, then admission receipt. Existing delivery creation keeps its own sorted insertion.

After the capacity lock wait, use a new statement to count slots: processing is `reserved` plus
`processing`; open is every state except `released`. Do not combine waiting lock acquisition and
counting in one statement snapshot. Count all epochs, owners, parents and workflows, including
expired leases and uncertain starts. Check both limits and insert one reserved slot plus receipt
in the same transaction. A concurrent N+1 request sees the committed Nth row. No counters exist.
Recheck actor grant and database time after every wait and immediately before commit.

Reserve validates the exact delivery-to-origin-execution binding, immutable child inputs, country,
version, attempt, workflow and original epoch/hash. Its current document grant must match those
immutable inputs but may have a newer fence/epoch after recovery. Keep the original execution and
delivery unchanged. Current binding changes only through `Bind`, under a live document grant.
Source transfer changes the allowed dispatcher, never the child recipient or capacity identity.
An expired originating execution does not erase an intent or authorize the old worker. Reserve resolves the exact delivery and child work through their committed accepted discovery page, checks that page observation against the retained source/external identity, and preserves source-window or transfer authorization. The page need not be the first acceptance retained by the immutable source-child binding. FIFO heads use the same eligibility predicate.

## Transitions and receipts

| Operation | Required condition and transition | Processing / open change |
|---|---|---|
| Reserve launch | Due, authorized pending delivery and both limits permit; no slot to `reserved` | +1 / +1 |
| Bind start or recovery | Matching registered current document grant; `reserved` to `processing`, or `processing`/`open_only` to itself with a new binding | 0 / 0 |
| Start acknowledgement | Existing source acknowledgement after confirmed start; does not allocate or release slots | 0 / 0 |
| Settle quarantine | Retained latest attempt acceptance is `quarantined`; `reserved` or `processing` to `open_only/quarantine` | -1 / 0 |
| Request resume | Current document owner records processing demand after a durable wake-up; `open_only` remains so | 0 / 0 |
| Resume | Earliest eligible processing demand and processing count below limit; `open_only` to `processing`, clear wait/demand | +1 / 0 |
| Settle terminal acceptance | Latest attempt event is `accepted`, `accepted_with_gaps`, or `excluded`; active state to `closing`, retain result | -1 if held / 0 |
| Terminal gap, cancellation, expiry, abandonment, failure | Verified lifecycle cause; active state to `closing`, preserve unresolved reason and receipt | -1 if held / 0 |
| Worker loss | Control proves target lease expired, cancelled or older-epoch; reconcile receipts first; `reserved` or `processing` to `open_only/worker_loss` | -1 / 0 |
| Confirm closure | Trusted observation proves terminal logical execution chain; `closing` to `released` | 0 / -1 |
| Identical replay | Current authorized caller receives original committed receipt; no state regression | 0 / 0 |

Reject unlisted transitions. Settlement chooses the latest committed acceptance event for this
version/attempt, not a caller-selected old quarantine. Ignore events at or below settled_watermark;
advance it with the settlement receipt. Resumption cannot settle the same quarantine twice.
An excluded terminal acceptance closes with `terminal_gap`, retaining its real result ID without creating new gap authority.
Candidate recording has no slot effect. Each processing entry requires the current slot binding.
Control may settle receipts without replaying acceptance or assuming an old decision is still valid.
It validates the immutable commit set and origin; it never manufactures evidence or approval.
An already `closing` slot cannot resume. `Bind` on `open_only` may update its grant but does not
give processing permission; `Resume` must succeed first. New attempts use new delivery/workflow
identities and new capacity, linked through existing predecessor provenance.

No lease lives for a quarantine wait. Losing a lease does not prove the workflow closed. Worker-loss
reclaim frees processing only and retains open capacity, including an uncertain start. Rebinding
that slot to a fresh document execution preserves its identity and accounting. RequestResume makes
recovery compete with other pending work. Control must not cancel a live unrelated document lease.
For `execution_failed`, before releasing processing Reconcile compares the slot binding's
epoch, owner and fence under the target lease lock and cancels the still-matching lease atomically
with the closing receipt. Preserve an unrelated reclaimed grant. Then release open once after
closure proof. Leave the original item unresolved until linked recovery or an explicit gap accounts
for it. The [recovery rules](corpus-database-admission-recovery.md#failure-and-uncertain-start-fencing)
define the receipt, stale-apply race and control-authorized abandonment.

Cancellation of a parent stops fresh reservations but never releases its ABANDON children.
Close cancels only the still-matching bound lease under the same comparison rule; stale activities
then fail existing store fences. Final grant validation permits its own cancellation. Quarantine expiry
requires `open_only/quarantine`, database time past due, and the same latest result and slot revision.
Resume before due and expiry serialize on the target lease; a terminal resolution is settled first.
RequestResume rejects past due for quarantine. A persisted decision alone is not applied resolution. Expiry closes as an
unresolved lifecycle gap. Do not insert an `evidence_gap` requiring fabricated acceptance, set
`AcceptedGaps`, or use `Complete=true` for an unapproved gap. Source coverage stays unresolved.

Every successful mutation stores its receipt with the state change. An operation that finds its
effect already recorded under another key returns that effect's receipt without another mutation.
Check current admission and actor grant before receipt replay; stale epoch, owner, fence, expiry,
or cancellation writes nothing. Reconciliation permits a cancelled actor only for settlement and
closure, following existing source reconciliation rules. Refusals create no receipt or slot.
For a UNIQUE conflict, reread the winner in a fresh statement, validate the payload and grant, and
return its receipt. Never infer a conflict from an invisible `ON CONFLICT` winner alone.

## Recovery, transport and backpressure

The [recovery continuation](corpus-database-admission-recovery.md) defines trusted closure,
control abandonment before transport draining, separate capacity-feasible launch/resume FIFOs,
authorized resolution, linked recovery ownership, and their required negatives. These are part of
the M1.16 contract; M1.7 must not replace them with workflow-local admission or slot SQL.

## Neutral port and ledger queries

Add `internal/corpus/run/admission.go` and matching tests. Keep existing run types and methods
unchanged. Values below are Vietnam-local port values, not Temporal envelopes. M1.7 transports
screened references and allowlisted codes, including the [consumer stop reference](corpus-database-admission-recovery.md#consumer-stop-reference).
M1.16 owns persistence; M1.7 owns calls and timers; M1.14 owns observer and executable composition.

```go
type DocumentAdmissionLimits struct {
    Processing, Open int32
    ReceiptJSONBytes int32
    QuarantineExpiry time.Duration
}
type AdmissionRefusal string
type AdmissionState string
type AdmissionTerminalReason string
type AdmissionReceipt struct {
    Key, SlotKey string
    Revision int64
    State AdmissionState
}
type AdmissionResult struct {
    Receipt *AdmissionReceipt // non-nil exactly on success
    Refusal AdmissionRefusal // empty exactly on success
    LimitsRevision, Processing, Open int64
}
type AdmissionReserveRequest struct {
    Actor, Child IntakeWork
    DeliveryKey string
}
type AdmissionChange struct {
    Actor IntakeWork
    SlotKey string
    ExpectedRevision int64
}
type AdmissionConfigureRequest struct {
    Actor IntakeWork
    ExpectedRevision int64
    Limits DocumentAdmissionLimits
}
type AdmissionCloseRequest struct {
    Change AdmissionChange
    Reason AdmissionTerminalReason
    CauseReceipt string
}
type AdmissionScan struct {
    Actor IntakeWork
    AfterUpdatedAt time.Time
    AfterID int64
    Limit int32
}
type AdmissionItem struct {
    SlotKey string
    Revision int64
    UpdatedAt time.Time
    ID int64
}
type DocumentAdmission interface {
    Configure(context.Context, AdmissionConfigureRequest) (AdmissionReceipt, error)
    Reserve(context.Context, AdmissionReserveRequest) (AdmissionResult, error)
    Bind(context.Context, AdmissionChange) (AdmissionReceipt, error)
    Settle(context.Context, AdmissionChange) (AdmissionReceipt, error)
    RequestResume(context.Context, AdmissionChange) (AdmissionReceipt, error)
    Resume(context.Context, AdmissionChange) (AdmissionResult, error)
    Close(context.Context, AdmissionCloseRequest) (AdmissionReceipt, error)
    Reconcile(context.Context, AdmissionChange) (AdmissionReceipt, error)
    Resolve(context.Context, AdmissionResolveRequest) (AdmissionResolution, error)
    Scan(context.Context, AdmissionScan) ([]AdmissionItem, error)
}
type DocumentExecutionStatus string // running, terminal, unknown
type DocumentExecutionObservation struct {
    WorkflowID, ChainReceipt string
    Status DocumentExecutionStatus
}
type DocumentExecutionObserver interface {
    Observe(context.Context, string) (DocumentExecutionObservation, error)
}
```

Configure returns empty SlotKey/State and its configuration revision. Reserve uses revision zero
in its operation key. Other keys use ExpectedRevision. Bind's Actor is the new document work;
RequestResume/Resume require that same live document owner. Reserve accepts a source or
transferred control dispatcher. A reprocess dispatcher gets `ErrInvalidAdmission` with no write
until a separately reviewed producer freezes an immutable selection-item-to-child authorization.
Settle/Close accept the document or country control owner.
Bind returns a replay receipt as history, not a new grant; processing must match the slot's current
owner/fence/epoch. Resolve supplies the current revision and binding after receipt loss or conflict;
its [neutral types and authorization](corpus-database-admission-recovery.md#authorized-resolution)
are part of this port. Resolve never grants processing permission.
Scan/Reconcile require country control. Reconcile first settles retained results, then records loss
or closure, one transition per call. A subsequent call uses the returned revision. No applicable
transition returns `ErrAdmissionUnchanged`, writes nothing, and preserves the scan cursor's progress.
Close accepts `cancelled` or `abandoned` from the current document owner as a durable stop instruction;
CauseReceipt is empty and the close receipt records that instruction. Control may use `abandoned`
only under the continuation's lost-grant stop rule, and `cancelled` only when the locked target lease
is cancelled. `expired` requires CauseReceipt to name the latest
quarantine result and database time past its frozen due time. `terminal_gap` requires a matching
immutable execution result with that outcome; no accepted-gap authority is inferred. Other reasons
come only from Settle or Reconcile. No generic caller-supplied lifecycle proof is accepted.

Add `DefaultDocumentAdmissionLimits() DocumentAdmissionLimits` and `Validate() error` on limits
and requests. Invalid shape wraps `ErrInvalidAdmission`; missing configuration wraps
`ErrAdmissionUnconfigured`; wrong transition, stale revision or missing lifecycle proof wraps
`ErrAdmissionConflict`; corrupt retained bindings/commit set wraps `ErrAdmissionIntegrity`.
Reuse `ErrStaleGrant` and `ErrPayloadConflict` with their existing priority and no-write semantics.
Preserve context cancellation and database causes with wrapping; transport logs only allowlisted
codes. Never classify an uncertain commit as a capacity refusal; retry its identical operation.

Implement `NewDocumentAdmission(db TransactionStarter, writer string, observer run.DocumentExecutionObserver)`
`(*DocumentAdmission, error)` in `intake/admission.go`, with no constructor I/O. Require observer;
hosted store tests inject a synthetic one. It uses `ledgerTransaction` and private shared grant helpers.
Put admission mutation and queue queries in `intake/queries/admission.sql`: `LockDocumentAdmissionLimit`,
`ConfigureDocumentAdmissionLimit`, `CountDocumentAdmissionSlots`, `ReadAdmissionQueueHeads`, `AdvanceAdmissionQueueTurn`,
`ResolveAdmissionLaunch`, `LockDocumentAdmissionSlot`, `InsertDocumentAdmissionSlot`,
`UpdateDocumentAdmissionSlot`, `CancelAdmissionLease`, `FindDocumentAdmissionReceipt`, `InsertDocumentAdmissionReceipt`,
`ReadAdmissionAcceptance` and `ScanDocumentAdmissionSlots`. Keep `ResolveDocumentAdmission` in
`intake/queries/ledger.sql`; resolution SQL and its authorization stay in M1.16. Update queries require expected
revision/state. Resolve launch uses original source provenance/transfer rules, not recipient alone.
No generated type crosses the neutral port. Import only reviewed hosted-generated `intakedb` output.

## Required hosted checks and review

G `generation` proves prefix replay, inventory, grants, constraints and zero drift. I
`intake-postgres` proves real transactions under the runtime role. Default Go short tests prove
neutral validation and response classification. I remains an evidence lane, not a renamed G job.
M1.14 adds hosted transport closure, cancellation and captured-history replay. No local execution.

- Barrier-controlled reservations at configured N and N+1, including defaults 32 and 256, from
  different parent leases; one winner for the final slot and no transient excess. Exercise both
  ceilings independently, quarantine-to-open-only, recovery resumption, and opposite key order.
- Stale admission epoch, writer, owner, fence, cancellation and expiry after every lock wait change
  no row. Counts use a fresh snapshot. Race rotation, configuration lowering, reserve and release.
- Crash before commit, response loss after commit, replay after later transitions, changed payload,
  invisible UNIQUE winner and replay under a fresh grant return the correct immutable receipt.
- Worker loss preserves open capacity; confirmed closed chains release exactly once. Repeat and
  race reconciliation, settlement, close and release. Unknown start and closed pre-rollover run
  retain capacity; a delayed start cannot bind a released slot. Never clear slots on epoch rotation.
- Quarantine settlement cannot precede committed quarantine; stale quarantine cannot undo terminal
  acceptance. Expiry versus decision preserves unresolved gaps without accepted evidence. Terminal
  acceptance releases processing before transport closure, and open only after trusted closure.
- Lowered limits preserve existing work, deny increases, permit releases and rebinds, and take effect
  across stale workflow configurations. Invalid values and unconfigured startup fail closed.
- FIFO survives refusal, restart and source transfer; no acknowledgement on refusal; window remains
  incomplete. Alerts aggregate pending items without subject keys. Counters never count retries.
- The continuation's [recovery negatives](corpus-database-admission-recovery.md#required-negatives)
  cover failed-grant revocation, stop-before-drain, feasible FIFO heads, resolution and identity.
- Receipt bounds reject invalid values and oversized writes. Boundary-size Configure (including
  initial) and ordinary receipts replay before and after lowering: canonical size equals frozen L,
  raw JSONB exceeds L but fits twice L. Reject raw size above twice L, malformed JSON and canonical L+1
  within the raw bound without writes; check maximum-int32 bound arithmetic without large allocation.

Independent adversarial review covers every mutation, grant check, queue predicate, lock, receipt,
closure proof and source-transfer boundary. Review conservative settlement and uncertain-start
draining first. Production values and transport evidence remain gates;
this document claims no implemented, hosted-validated, real-document or deployed slot behavior.
