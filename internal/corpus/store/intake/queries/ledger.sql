-- Mutations run within the adapter's transaction. Admission locks precede
-- lease locks so rotation cannot interleave with a guarded write. The adapter
-- rechecks grants and resolves an empty RETURNING result with a fresh lookup.
-- Under READ COMMITTED a concurrent ON CONFLICT winner may be invisible to
-- the statement snapshot. A new statement sees its committed receipt. Never
-- report a payload conflict merely because that snapshot returned no row.

-- name: ReadAdmission :one
SELECT * FROM ingest.country_admission WHERE id = 1;

-- name: RotateAdmissionEpoch :one
UPDATE ingest.country_admission
SET admission_epoch = admission_epoch + 1, admitted_writer = $2,
    enabled = $3, rotated_at = clock_timestamp()
WHERE id = 1 AND admission_epoch = $1
RETURNING *;

-- name: ClaimExecutionLease :one
WITH admission AS (
    SELECT a.id FROM ingest.country_admission a
    WHERE a.id = 1 AND a.admission_epoch = $1 AND a.admitted_writer = $2 AND a.enabled
    FOR SHARE
)
INSERT INTO ingest.execution_lease
    (resource_kind, resource_key, owner, attempt_fence, admission_epoch, expires_at)
SELECT $3, $4, $5, 1, $1, $6 FROM admission
WHERE $6::timestamptz > clock_timestamp()
ON CONFLICT (resource_kind, resource_key) DO NOTHING
RETURNING *;

-- name: ReclaimExecutionLease :one
WITH admission AS (
    SELECT a.id FROM ingest.country_admission a
    WHERE a.id = 1 AND a.admission_epoch = $1 AND a.admitted_writer = $2 AND a.enabled
    FOR SHARE
)
UPDATE ingest.execution_lease l
SET owner = $5, attempt_fence = l.attempt_fence + 1, admission_epoch = $1,
    expires_at = $6, renewed_at = clock_timestamp(), cancelled_at = NULL
FROM admission
WHERE l.resource_kind = $3 AND l.resource_key = $4
  AND (l.expires_at <= clock_timestamp() OR l.cancelled_at IS NOT NULL OR l.admission_epoch < $1)
  AND l.next_eligible_at <= clock_timestamp() AND $6::timestamptz > clock_timestamp()
RETURNING l.*;

-- Materialize the locked lease before checking its grant. An unchanged row
-- can remain locked past expiry without PostgreSQL rechecking a pre-lock filter.
-- Cast grant parameters because sqlc 1.31.1 does not index CTE tables when
-- inferring parameter types from comparisons against their columns.
-- name: InsertExecution :one
WITH admission AS (
    SELECT a.id FROM ingest.country_admission a
    WHERE a.id = 1 AND a.admission_epoch = $1 AND a.admitted_writer = $2 AND a.enabled
    FOR SHARE
), lease AS MATERIALIZED (
    SELECT l.id, l.expires_at, l.owner, l.attempt_fence, l.admission_epoch,
        l.cancelled_at
    FROM ingest.execution_lease l CROSS JOIN admission
    WHERE l.resource_kind = $3 AND l.resource_key = $4
    FOR UPDATE OF l
)
INSERT INTO ingest.execution (execution_key, workflow_id, workflow_kind,
    processing_attempt_key, attempt_fence, admission_epoch, scope_kind,
    resource_kind, resource_key, input_digest, recipe_digest, budget_digest,
    budget, predecessor_id, run_receipts)
SELECT $7, $8, $9, $10, $6, $1, $11, $3, $4, $12, $13, $14, $15, $16, $17
FROM lease
WHERE lease.expires_at > clock_timestamp() AND lease.owner = $5::text
  AND lease.attempt_fence = $6::bigint AND lease.admission_epoch = $1::bigint
  AND lease.cancelled_at IS NULL
RETURNING *;

-- name: InsertExecutionResult :one
WITH admission AS (
    SELECT a.id FROM ingest.country_admission a
    WHERE a.id = 1 AND a.admission_epoch = $1 AND a.admitted_writer = $2 AND a.enabled
    FOR SHARE
), lease AS MATERIALIZED (
    SELECT l.id, l.expires_at, l.owner, l.attempt_fence, l.admission_epoch,
        l.cancelled_at
    FROM ingest.execution_lease l CROSS JOIN admission
    WHERE l.resource_kind = $3 AND l.resource_key = $4
    FOR UPDATE OF l
), current_execution AS (
    SELECT e.id FROM ingest.execution e CROSS JOIN lease
    WHERE lease.expires_at > clock_timestamp() AND lease.owner = $5::text
      AND lease.attempt_fence = $6::bigint AND lease.admission_epoch = $1::bigint
      AND lease.cancelled_at IS NULL
      AND e.id = $7 AND e.resource_kind = $3 AND e.resource_key = $4
      AND e.attempt_fence = $6 AND e.admission_epoch = $1
      AND e.input_digest = $13 AND e.recipe_digest = $15 AND e.budget_digest = $16
), inserted AS (
    INSERT INTO ingest.execution_result (execution_id, result_key, stage, substep,
        shard, idempotency_key, input_digest, output_digest, recipe_digest,
        budget_digest, decision_revision, outcome, expected, processed, succeeded,
        pending_retry, terminal_gap, rejected, failed, accepted_gaps, cursor,
        next_eligible_at, gap_acceptance_ref, complete, payload)
    SELECT current_execution.id, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18,
        $19, $20, $21, $22, $23, $24, $25, $26, $27, $28, $29, $30, $31
    FROM current_execution
    ON CONFLICT (idempotency_key) DO NOTHING
    RETURNING *
)
SELECT * FROM inserted
UNION ALL
SELECT r.* FROM ingest.execution_result r CROSS JOIN current_execution
WHERE r.idempotency_key = $12
  AND ROW(r.stage, r.substep, r.shard, r.input_digest, r.output_digest,
      r.recipe_digest, r.budget_digest, r.decision_revision, r.outcome, r.expected,
      r.processed, r.succeeded, r.pending_retry, r.terminal_gap, r.rejected,
      r.failed, r.accepted_gaps, r.cursor, r.next_eligible_at,
      r.gap_acceptance_ref, r.complete, r.payload)
      IS NOT DISTINCT FROM ROW($9::text, $10::text, $11::text, $13::text, $14::text,
      $15::text, $16::text, $17::bigint, $18::text, $19::bigint, $20::bigint,
      $21::bigint, $22::bigint, $23::bigint, $24::bigint, $25::bigint,
      $26::bigint, $27::text, $28::timestamptz, $29::text, $30::boolean, $31::jsonb)
  AND NOT EXISTS (SELECT 1 FROM inserted);

-- name: FindExecutionResult :one
SELECT * FROM ingest.execution_result WHERE idempotency_key = $1;

-- These two queries consume a result already accepted in the same fenced
-- transaction. Phase 2 validates decision authority and cross-store bindings.
-- name: InsertRevisionAcceptance :one
INSERT INTO ingest.revision_acceptance (revision_key, processing_attempt_key,
    execution_id, execution_result_id, candidate_digest, quality_report_hash,
    decision_revision, state, scoped_gaps, watermark)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: UpsertRevisionAcceptanceState :one
INSERT INTO ingest.revision_acceptance_state (revision_key, processing_attempt_key,
    acceptance_id, state, watermark)
SELECT a.revision_key, a.processing_attempt_key, a.id, a.state, a.watermark
FROM ingest.revision_acceptance a WHERE a.id = $1
ON CONFLICT (revision_key, processing_attempt_key) DO UPDATE
SET acceptance_id = EXCLUDED.acceptance_id, state = EXCLUDED.state,
    watermark = EXCLUDED.watermark
WHERE ingest.revision_acceptance_state.watermark < EXCLUDED.watermark
RETURNING *;

-- name: ListBuildEligibleAcceptance :many
SELECT * FROM ingest.revision_acceptance_state
WHERE state IN ('accepted', 'accepted_with_gaps') AND watermark <= $1
ORDER BY watermark, id LIMIT $2;

-- Duplicate delivery reads the prior intent without updating immutable fields.
-- name: InsertDeliveryRecord :one
WITH inserted AS (
    INSERT INTO ingest.delivery_record (deduplication_key, delivery_kind,
        recipient_workflow_id, receipt_kind, receipt_key, expected_epoch,
        expected_hash, due_at)
    VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
    ON CONFLICT (deduplication_key) DO NOTHING RETURNING *
)
SELECT * FROM inserted
UNION ALL
SELECT d.* FROM ingest.delivery_record d WHERE d.deduplication_key = $1
    AND ROW(d.delivery_kind, d.recipient_workflow_id, d.receipt_kind, d.receipt_key,
        d.expected_epoch, d.expected_hash, d.due_at) IS NOT DISTINCT FROM
        ROW($2::text, $3::text, $4::text, $5::text, $6::bigint, $7::text, $8::timestamptz)
    AND NOT EXISTS (SELECT 1 FROM inserted);

-- name: FindDeliveryRecord :one
SELECT * FROM ingest.delivery_record WHERE deduplication_key = $1;

-- name: ListDueUnacknowledgedDelivery :many
SELECT * FROM ingest.delivery_record
WHERE acknowledged_at IS NULL AND due_at <= clock_timestamp()
ORDER BY due_at, id LIMIT $1;

-- name: FindPolicyBundle :one
SELECT * FROM config.policy_bundle WHERE digest = $1;

-- A single statement snapshot authorizes the actor, resolves immutable target
-- registration and projects current accounting. No lock or transport call.
-- name: ResolveDocumentAdmission :one
WITH actor AS (
    SELECT e.* FROM ingest.execution e
    JOIN ingest.execution_lease l ON l.resource_kind = e.resource_kind AND l.resource_key = e.resource_key
    CROSS JOIN ingest.country_admission a
    WHERE a.id = 1 AND a.enabled AND a.country_code = sqlc.arg(actor)::jsonb->'Execution'->>'Country'
    AND a.admitted_writer = sqlc.arg(writer)::text
    AND a.admission_epoch = (sqlc.arg(actor)::jsonb->'Execution'->>'AdmissionEpoch')::bigint
    AND e.admission_epoch = a.admission_epoch AND l.admission_epoch = a.admission_epoch
    AND l.cancelled_at IS NULL AND l.expires_at > clock_timestamp()
    AND l.owner = sqlc.arg(actor)::jsonb->'Execution'->>'Owner'
    AND l.attempt_fence = (sqlc.arg(actor)::jsonb->'Execution'->>'AttemptFence')::bigint
    AND e.attempt_fence = l.attempt_fence
    AND e.workflow_id = sqlc.arg(actor)::jsonb->'Execution'->>'WorkflowID'
    AND e.workflow_kind = sqlc.arg(actor)::jsonb->'Execution'->>'WorkflowKind'
    AND e.processing_attempt_key = sqlc.arg(actor)::jsonb->'Execution'->>'AttemptKey'
    AND e.input_digest = sqlc.arg(actor)::jsonb->>'InputDigest'
    AND e.recipe_digest = sqlc.arg(actor)::jsonb->>'RecipeDigest'
    AND e.budget_digest = sqlc.arg(actor)::jsonb->>'BudgetDigest'
    AND e.budget = sqlc.arg(actor)::jsonb->'Budget'
    AND ((e.workflow_kind = 'ControlReconcile' AND e.scope_kind = 'control' AND e.resource_kind = 'control' AND e.resource_key = a.country_code)
      OR (e.workflow_kind = 'DocumentIngest' AND e.scope_kind = 'intake' AND e.resource_kind = 'document_version'
        AND e.resource_key = sqlc.arg(actor)::jsonb->'Execution'->'Scope'->'Subject'->>'DocumentVersion'))
), target AS (
    SELECT e.* FROM ingest.execution e
    LEFT JOIN ingest.document_admission_slot admitted ON admitted.workflow_id = e.workflow_id
    LEFT JOIN ingest.execution original ON original.id = admitted.origin_execution_id
    CROSS JOIN actor
    WHERE e.workflow_id = sqlc.arg(workflow_id)::text AND e.workflow_kind = 'DocumentIngest'
    AND e.scope_kind = 'intake' AND e.resource_kind = 'document_version'
    AND e.workflow_id = 'document-ingest/' || (sqlc.arg(actor)::jsonb->'Execution'->>'Country') || '/' || e.resource_key || '/' || e.processing_attempt_key
    AND (actor.workflow_kind = 'ControlReconcile' OR (actor.workflow_id = e.workflow_id AND actor.resource_key = e.resource_key AND actor.processing_attempt_key = e.processing_attempt_key))
    AND (actor.workflow_kind = 'ControlReconcile' OR admitted.id IS NULL OR (
        actor.input_digest = original.input_digest AND actor.recipe_digest = original.recipe_digest
        AND actor.budget_digest = original.budget_digest AND actor.budget = original.budget))
    AND (sqlc.arg(document_work)::jsonb = 'null'::jsonb OR (
        e.input_digest = sqlc.arg(document_work)::jsonb->>'InputDigest' AND e.recipe_digest = sqlc.arg(document_work)::jsonb->>'RecipeDigest'
        AND e.budget_digest = sqlc.arg(document_work)::jsonb->>'BudgetDigest' AND e.budget = sqlc.arg(document_work)::jsonb->'Budget'
        AND e.admission_epoch = (sqlc.arg(document_work)::jsonb->'Execution'->>'AdmissionEpoch')::bigint
        AND e.attempt_fence = (sqlc.arg(document_work)::jsonb->'Execution'->>'AttemptFence')::bigint
        AND e.processing_attempt_key = sqlc.arg(document_work)::jsonb->'Execution'->>'AttemptKey'
        AND e.resource_key = sqlc.arg(document_work)::jsonb->'Execution'->'Scope'->'Subject'->>'DocumentVersion'))
    ORDER BY e.id DESC LIMIT 1
)
SELECT jsonb_build_object('Resolution', jsonb_build_object('Found', s.id IS NOT NULL, 'SlotKey', COALESCE(s.slot_key, ''),
    'WorkflowID', target.workflow_id, 'VersionKey', target.resource_key, 'AttemptKey', target.processing_attempt_key,
    'Revision', COALESCE(s.revision, 0), 'State', COALESCE(s.state, ''), 'WaitReason', COALESCE(s.wait_reason, ''),
    'TerminalReason', COALESCE(s.terminal_reason, ''), 'TerminalResultID', COALESCE(s.terminal_result_id, 0),
    'SettledWatermark', COALESCE(s.settled_watermark, 0),
    'Binding', jsonb_build_object('ExecutionID', COALESCE(s.current_execution_id, 0), 'Epoch', COALESCE(s.admission_epoch, 0),
        'Fence', COALESCE(s.attempt_fence, 0), 'Owner', COALESCE(s.lease_owner, '')),
    'LifecycleReceipt', COALESCE(lifecycle.receipt_key, ''), 'SettlementReceipt', COALESCE(settlement.receipt_key, ''),
    'CloseReceipt', COALESCE(closing.receipt_key, ''), 'ReleaseReceipt', COALESCE(release.receipt_key, ''),
    'ChainReceipt', COALESCE(release.response->'Observation'->>'ChainReceipt', closing.response->'Observation'->>'ChainReceipt', '')),
    'Receipts', (SELECT COALESCE(jsonb_agg(to_jsonb(r)), '[]'::jsonb) FROM ingest.document_admission_receipt r
        WHERE r.receipt_key IN (lifecycle.receipt_key, settlement.receipt_key, closing.receipt_key, release.receipt_key)))::jsonb AS resolution
FROM target LEFT JOIN ingest.document_admission_slot s ON s.workflow_id = target.workflow_id
LEFT JOIN LATERAL (SELECT receipt_key FROM ingest.document_admission_receipt WHERE slot_id = s.id
    AND (operation NOT IN ('bind', 'request_resume') OR (operation = 'bind' AND response->>'PriorState' = 'reserved'))
    ORDER BY id DESC LIMIT 1) lifecycle ON true
LEFT JOIN LATERAL (SELECT receipt_key FROM ingest.document_admission_receipt WHERE slot_id = s.id AND operation = 'settle' ORDER BY id DESC LIMIT 1) settlement ON true
LEFT JOIN LATERAL (SELECT receipt_key, response FROM ingest.document_admission_receipt WHERE slot_id = s.id AND operation = 'close' ORDER BY id DESC LIMIT 1) closing ON true
LEFT JOIN LATERAL (SELECT receipt_key, response FROM ingest.document_admission_receipt WHERE slot_id = s.id AND operation = 'release' ORDER BY id DESC LIMIT 1) release ON true;
