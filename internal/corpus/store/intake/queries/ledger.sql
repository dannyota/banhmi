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
    SELECT id FROM ingest.country_admission
    WHERE id = 1 AND admission_epoch = $1 AND admitted_writer = $2 AND enabled
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
    SELECT id FROM ingest.country_admission
    WHERE id = 1 AND admission_epoch = $1 AND admitted_writer = $2 AND enabled
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

-- name: InsertExecution :one
WITH admission AS (
    SELECT id FROM ingest.country_admission
    WHERE id = 1 AND admission_epoch = $1 AND admitted_writer = $2 AND enabled
    FOR SHARE
), lease AS (
    SELECT l.id FROM ingest.execution_lease l CROSS JOIN admission
    WHERE l.resource_kind = $3 AND l.resource_key = $4 AND l.owner = $5
      AND l.attempt_fence = $6 AND l.admission_epoch = $1
      AND l.cancelled_at IS NULL AND l.expires_at > clock_timestamp()
    FOR UPDATE OF l
)
INSERT INTO ingest.execution (execution_key, workflow_id, workflow_kind,
    processing_attempt_key, attempt_fence, admission_epoch, scope_kind,
    resource_kind, resource_key, input_digest, recipe_digest, budget_digest,
    budget, predecessor_id, run_receipts)
SELECT $7, $8, $9, $10, $6, $1, $11, $3, $4, $12, $13, $14, $15, $16, $17
FROM lease RETURNING *;

-- name: InsertExecutionResult :one
WITH admission AS (
    SELECT id FROM ingest.country_admission
    WHERE id = 1 AND admission_epoch = $1 AND admitted_writer = $2 AND enabled
    FOR SHARE
), lease AS (
    SELECT l.id FROM ingest.execution_lease l CROSS JOIN admission
    WHERE l.resource_kind = $3 AND l.resource_key = $4 AND l.owner = $5
      AND l.attempt_fence = $6 AND l.admission_epoch = $1
      AND l.cancelled_at IS NULL AND l.expires_at > clock_timestamp()
    FOR UPDATE OF l
), current_execution AS (
    SELECT e.id FROM ingest.execution e CROSS JOIN lease
    WHERE e.id = $7 AND e.resource_kind = $3 AND e.resource_key = $4
      AND e.attempt_fence = $6 AND e.admission_epoch = $1
      AND e.input_digest = $13 AND e.recipe_digest = $15 AND e.budget_digest = $16
), inserted AS (
    INSERT INTO ingest.execution_result (execution_id, result_key, stage, substep,
        shard, idempotency_key, input_digest, output_digest, recipe_digest,
        budget_digest, decision_revision, outcome, expected, processed, succeeded,
        pending_retry, terminal_gap, rejected, failed, accepted_gaps, cursor,
        next_eligible_at, gap_acceptance_ref, complete, payload)
    SELECT id, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18,
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
SELECT revision_key, processing_attempt_key, id, state, watermark
FROM ingest.revision_acceptance WHERE id = $1
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
SELECT * FROM ingest.delivery_record WHERE deduplication_key = $1
    AND ROW(delivery_kind, recipient_workflow_id, receipt_kind, receipt_key,
        expected_epoch, expected_hash, due_at) IS NOT DISTINCT FROM
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
