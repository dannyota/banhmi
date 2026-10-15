-- Callers hold admission SHARE and all existing leases in byte order before
-- window/delivery locks, then capacity, then slot. Counts and selection MUST be
-- separate statements after capacity acquisition under READ COMMITTED.

-- name: LockAdmissionTargetLease :one
SELECT * FROM ingest.execution_lease
WHERE resource_kind = sqlc.arg(resource_kind) AND resource_key = sqlc.arg(resource_key)
FOR UPDATE;

-- name: AdmissionDatabaseTime :one
SELECT clock_timestamp()::timestamptz AS now;

-- Read-only validation adds no earlier lock after a wait. Mutation callers
-- already hold admission SHARE and the actor lease UPDATE lock.
-- name: AdmissionActorLive :one
SELECT EXISTS (SELECT 1 FROM ingest.country_admission a CROSS JOIN ingest.execution_lease l
WHERE a.id = 1 AND a.enabled AND a.country_code = sqlc.arg(country)::text AND a.admitted_writer = sqlc.arg(writer)::text
AND a.admission_epoch = sqlc.arg(epoch)::bigint AND l.admission_epoch = a.admission_epoch
AND l.resource_kind = sqlc.arg(resource_kind)::text AND l.resource_key = sqlc.arg(resource_key)::text
AND l.owner = sqlc.arg(owner)::text AND l.attempt_fence = sqlc.arg(fence)::bigint
AND l.expires_at > clock_timestamp() AND (l.cancelled_at IS NULL OR sqlc.arg(reconciliation)::boolean))::boolean;

-- name: AdmissionSourceCancelled :one
SELECT NOT EXISTS (SELECT 1 FROM ingest.execution_lease
WHERE resource_kind = 'source_window' AND resource_key = sqlc.arg(window_key)::text AND cancelled_at IS NULL)::boolean;

-- name: LockDocumentAdmissionLimit :one
SELECT * FROM ingest.document_admission_limit WHERE id = 1 FOR UPDATE;

-- name: ConfigureDocumentAdmissionLimit :one
UPDATE ingest.document_admission_limit
SET revision = revision + 1, processing_limit = sqlc.arg(processing_limit)::integer,
    open_limit = sqlc.arg(open_limit)::integer, receipt_json_bytes = sqlc.arg(receipt_json_bytes)::integer,
    quarantine_expiry_us = sqlc.arg(quarantine_expiry_us)::bigint, updated_at = clock_timestamp()
WHERE id = 1 AND revision = sqlc.arg(expected_revision)::bigint RETURNING *;

-- name: CountDocumentAdmissionSlots :one
SELECT count(*) FILTER (WHERE state IN ('reserved', 'processing'))::bigint AS processing,
    count(*) FILTER (WHERE state <> 'released')::bigint AS open
FROM ingest.document_admission_slot;

-- name: AdvanceAdmissionQueueTurn :execrows
UPDATE ingest.document_admission_limit
SET next_queue = CASE next_queue WHEN 'launch' THEN 'resume' ELSE 'launch' END
WHERE id = 1 AND next_queue = sqlc.arg(expected_queue)::text;

-- Resolve provenance without taking any locks. Repeat after prelocking the
-- returned source window and the exact delivery. A missing source binding is
-- deliberately ineligible, including unreviewed reprocess/recovery producers.
-- name: ResolveAdmissionLaunch :one
SELECT w.window_key, w.source_id, b.external_id, e.id AS origin_execution_id,
    e.execution_key, e.input_digest, e.recipe_digest, e.budget_digest, e.budget,
    e.admission_epoch AS origin_epoch, e.workflow_id, e.processing_attempt_key,
    e.resource_key AS document_version_key, d.id AS delivery_id,
    d.due_at, d.acknowledged_at, (launch.value->'Work')::jsonb AS work
FROM ingest.delivery_record d
JOIN ingest.execution e ON e.execution_key = d.receipt_key
JOIN ingest.source_child_binding b ON b.workflow_id = e.workflow_id
JOIN ingest.execution_result accepted ON accepted.id = b.acceptance_result_id
JOIN ingest.execution issuer ON issuer.id = accepted.execution_id AND issuer.resource_kind = 'source_window'
JOIN ingest.discovery_page p ON p.acceptance_result_id = accepted.id AND p.state = 'accepted'
JOIN ingest.source_window w ON w.id = p.source_window_id AND w.source_id = b.source_id AND w.window_key = issuer.resource_key
CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(accepted.payload->'SourcePayload'->'Launches') = 'array'
    THEN accepted.payload->'SourcePayload'->'Launches' ELSE '[]'::jsonb END) launch(value)
WHERE d.deduplication_key = sqlc.arg(delivery_key) AND launch.value->>'RequestKey' = d.deduplication_key
AND accepted.payload->>'SourceOperation' = 'accept_page' AND d.delivery_kind = 'launch' AND d.receipt_kind = 'execution'
AND e.workflow_kind = 'DocumentIngest' AND e.resource_kind = 'document_version' AND e.scope_kind = 'intake'
AND d.recipient_workflow_id = e.workflow_id AND d.expected_epoch = e.admission_epoch AND d.expected_hash = e.input_digest;

-- name: LockAdmissionDelivery :one
SELECT * FROM ingest.delivery_record WHERE deduplication_key = sqlc.arg(delivery_key) FOR UPDATE;

-- name: ReadAdmissionQueueHeads :one
WITH launch AS (
    SELECT d.deduplication_key FROM ingest.delivery_record d
    JOIN ingest.execution e ON e.execution_key = d.receipt_key AND e.workflow_kind = 'DocumentIngest'
    JOIN ingest.source_child_binding b ON b.workflow_id = e.workflow_id
    JOIN ingest.execution_result accepted ON accepted.id = b.acceptance_result_id
    JOIN ingest.execution issuer ON issuer.id = accepted.execution_id
    JOIN ingest.execution_lease parent ON parent.resource_kind = issuer.resource_kind AND parent.resource_key = issuer.resource_key
    WHERE d.delivery_kind = 'launch' AND d.receipt_kind = 'execution'
    AND d.recipient_workflow_id = e.workflow_id AND d.expected_epoch = e.admission_epoch AND d.expected_hash = e.input_digest
    AND d.due_at <= clock_timestamp() AND d.acknowledged_at IS NULL AND parent.cancelled_at IS NULL
    AND NOT EXISTS (SELECT 1 FROM ingest.document_admission_slot s WHERE s.delivery_id = d.id OR
        (s.document_version_key = e.resource_key AND s.state <> 'released'))
    ORDER BY d.due_at, d.deduplication_key COLLATE "C" LIMIT 1
), resume AS (
    SELECT s.slot_key FROM ingest.document_admission_slot s
    JOIN ingest.execution_lease l ON l.resource_kind = 'document_version' AND l.resource_key = s.document_version_key
    JOIN ingest.country_admission a ON a.id = 1
    WHERE s.state = 'open_only' AND s.resume_requested_at IS NOT NULL
    AND l.owner = s.lease_owner AND l.attempt_fence = s.attempt_fence AND l.admission_epoch = s.admission_epoch
    AND l.admission_epoch = a.admission_epoch AND l.cancelled_at IS NULL AND l.expires_at > clock_timestamp()
    AND (s.wait_reason <> 'quarantine' OR EXISTS (
        SELECT 1 FROM ingest.revision_acceptance r WHERE r.watermark = s.settled_watermark
        AND r.created_at + (s.quarantine_expiry_us / 1000000) * interval '1 second'
            + (s.quarantine_expiry_us % 1000000) * interval '1 microsecond' > clock_timestamp()))
    ORDER BY s.resume_requested_at, s.slot_key COLLATE "C" LIMIT 1
)
SELECT COALESCE((SELECT deduplication_key FROM launch), '')::text AS launch,
    COALESCE((SELECT slot_key FROM resume), '')::text AS resume;

-- name: ReadDocumentAdmissionSlot :one
SELECT * FROM ingest.document_admission_slot WHERE slot_key = sqlc.arg(slot_key);

-- name: LockDocumentAdmissionSlot :one
SELECT * FROM ingest.document_admission_slot WHERE slot_key = sqlc.arg(slot_key) FOR UPDATE;

-- name: FindAdmissionDeliverySlot :one
SELECT * FROM ingest.document_admission_slot WHERE delivery_id = sqlc.arg(delivery_id);

-- name: AdmissionVersionBusy :one
SELECT EXISTS (SELECT 1 FROM ingest.document_admission_slot
WHERE document_version_key = sqlc.arg(document_version_key) AND state <> 'released')::boolean;

-- name: InsertDocumentAdmissionSlot :one
INSERT INTO ingest.document_admission_slot (slot_key, delivery_id, workflow_id, document_version_key,
    processing_attempt_key, origin_execution_id, current_execution_id, admission_epoch,
    lease_owner, attempt_fence, revision, quarantine_expiry_us, state)
VALUES (sqlc.arg(slot_key), sqlc.arg(delivery_id), sqlc.arg(workflow_id), sqlc.arg(document_version_key),
    sqlc.arg(processing_attempt_key), sqlc.arg(origin_execution_id), sqlc.arg(current_execution_id),
    sqlc.arg(admission_epoch), sqlc.arg(lease_owner), sqlc.arg(attempt_fence), 1,
    sqlc.arg(quarantine_expiry_us), 'reserved')
RETURNING *;

-- name: UpdateDocumentAdmissionSlot :one
UPDATE ingest.document_admission_slot SET current_execution_id = sqlc.arg(current_execution_id),
    admission_epoch = sqlc.arg(admission_epoch), lease_owner = sqlc.arg(lease_owner), attempt_fence = sqlc.arg(attempt_fence),
    revision = revision + 1, state = sqlc.arg(state), settled_watermark = sqlc.arg(settled_watermark),
    wait_reason = sqlc.narg(wait_reason), resume_requested_at = sqlc.narg(resume_requested_at),
    terminal_reason = sqlc.narg(terminal_reason), terminal_result_id = sqlc.narg(terminal_result_id), updated_at = clock_timestamp()
WHERE id = sqlc.arg(id) AND revision = sqlc.arg(expected_revision) AND state = sqlc.arg(expected_state)
RETURNING *;

-- name: CancelAdmissionLease :execrows
UPDATE ingest.execution_lease SET cancelled_at = clock_timestamp()
WHERE resource_kind = 'document_version' AND resource_key = sqlc.arg(document_version_key)
AND admission_epoch = sqlc.arg(admission_epoch) AND owner = sqlc.arg(owner) AND attempt_fence = sqlc.arg(attempt_fence)
AND cancelled_at IS NULL;

-- name: FindDocumentAdmissionReceipt :one
SELECT * FROM ingest.document_admission_receipt WHERE operation_key = sqlc.arg(operation_key);

-- name: InsertDocumentAdmissionReceipt :one
INSERT INTO ingest.document_admission_receipt (receipt_key, operation_key, slot_id, actor_execution_id,
    operation, payload_digest, limits_revision, receipt_json_bytes, request, response)
VALUES (sqlc.arg(receipt_key), sqlc.arg(operation_key), sqlc.narg(slot_id), sqlc.arg(actor_execution_id),
    sqlc.arg(operation), sqlc.arg(payload_digest), sqlc.arg(limits_revision), sqlc.arg(receipt_json_bytes),
    sqlc.arg(request), sqlc.arg(response))
ON CONFLICT (operation_key) DO NOTHING RETURNING *;

-- name: ReadAdmissionAcceptance :one
SELECT r.* FROM ingest.revision_acceptance r
JOIN ingest.execution e ON e.id = r.execution_id
WHERE e.workflow_id = sqlc.arg(workflow_id) AND e.resource_kind = 'document_version'
AND e.resource_key = sqlc.arg(document_version_key) AND r.processing_attempt_key = sqlc.arg(processing_attempt_key)
ORDER BY r.watermark DESC, r.id DESC LIMIT 1;

-- name: ReadAdmissionResult :one
SELECT * FROM ingest.execution_result WHERE id = sqlc.arg(id);

-- No projection lock: admission holds the version lease and reads immutable
-- events. It must never call the acceptance adapter's FOR UPDATE query.
-- name: AdmissionAcceptanceProjectionValid :one
SELECT EXISTS (SELECT 1 FROM ingest.revision_acceptance_state s
JOIN ingest.revision_acceptance a ON a.id = s.acceptance_id
JOIN ingest.execution_result r ON r.id = a.execution_result_id
WHERE s.revision_key = sqlc.arg(revision_key)::text AND s.processing_attempt_key = sqlc.arg(processing_attempt_key)::text
AND s.revision_key = a.revision_key AND s.processing_attempt_key = a.processing_attempt_key
AND s.state = a.state AND s.watermark = a.watermark AND a.watermark = r.id AND a.execution_id = r.execution_id)::boolean;

-- name: ReadAdmissionResultByKey :one
SELECT * FROM ingest.execution_result WHERE result_key = sqlc.arg(result_key);

-- name: ReadAdmissionExecution :one
SELECT * FROM ingest.execution WHERE id = sqlc.arg(id);

-- name: ReadAdmissionDelivery :one
SELECT * FROM ingest.delivery_record WHERE id = sqlc.arg(id);

-- name: AdmissionSlotReceipts :many
SELECT * FROM ingest.document_admission_receipt WHERE slot_id = sqlc.arg(slot_id) ORDER BY id;

-- name: FindAdmissionEffect :one
SELECT * FROM ingest.document_admission_receipt
WHERE slot_id = sqlc.narg(slot_id) AND operation = sqlc.arg(operation) ORDER BY id DESC LIMIT 1;

-- name: ScanDocumentAdmissionSlots :many
SELECT slot_key, revision, updated_at, id FROM ingest.document_admission_slot
WHERE state <> 'released' AND (sqlc.arg(after_id)::bigint = 0 OR (updated_at, id) > (sqlc.arg(after_updated_at)::timestamptz, sqlc.arg(after_id)))
ORDER BY updated_at, id LIMIT sqlc.arg(row_limit)::integer;
