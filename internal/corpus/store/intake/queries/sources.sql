-- All writes run inside the owning adapter transaction. Parameters 1 through 6
-- are admission epoch, writer, resource kind, resource key, owner and fence.
-- Lock admission before the lease. Materialize locks before testing time.
-- Empty conflict results require a fresh lookup, as in ledger.sql. A changed
-- replay is a payload conflict, never an UPDATE of immutable evidence.

-- name: OpenSourceWindow :one
WITH admission AS (
    SELECT a.id AS id FROM ingest.country_admission a
    WHERE a.id = 1 AND a.admission_epoch = $1 AND a.admitted_writer = $2 AND a.enabled FOR SHARE
), lease AS MATERIALIZED (
    SELECT l.id AS id, l.expires_at AS expires_at, l.owner AS owner,
        l.attempt_fence AS attempt_fence, l.admission_epoch AS admission_epoch, l.cancelled_at AS cancelled_at
    FROM ingest.execution_lease l CROSS JOIN admission
    WHERE l.resource_kind = $3 AND l.resource_key = $4 FOR UPDATE OF l
)
INSERT INTO ingest.source_window (window_key, source_id, slice_key, mode, starts_at, cutoff_at,
    deadline_at, policy_digest, budget_digest, budget, page_budget, request_budget,
    source_attempt_limit, source_retry_interval, initial_cursor, committed_cursor, state)
SELECT $4, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $20, 'frozen'
FROM lease
WHERE $3::text = 'source_window' AND lease.expires_at > clock_timestamp()
  AND lease.owner = $5::text AND lease.attempt_fence = $6::bigint
  AND lease.admission_epoch = $1::bigint AND lease.cancelled_at IS NULL
ON CONFLICT (window_key) DO NOTHING
RETURNING id AS id;

-- name: ReadSourceWindow :one
SELECT w.id AS id, w.window_key AS window_key, w.source_id AS source_id,
    w.slice_key AS slice_key, w.mode AS mode, w.starts_at AS starts_at,
    w.cutoff_at AS cutoff_at, w.deadline_at AS deadline_at,
    w.policy_digest AS policy_digest, w.budget_digest AS budget_digest, w.budget AS budget,
    w.page_budget AS page_budget, w.request_budget AS request_budget,
    w.source_attempt_limit AS source_attempt_limit, w.source_retry_interval AS source_retry_interval,
    w.initial_cursor AS initial_cursor, w.committed_cursor AS committed_cursor,
    w.state AS state, w.reason AS reason, w.created_at AS created_at
FROM ingest.source_window w WHERE w.window_key = $1;

-- SourceSync plans continuations against this same window. It never receives
-- a replacement budget from a follow-up. LockSourceWindowForPlanning must
-- precede registration in a separate statement, so counts use a fresh snapshot.
-- name: RegisterDiscoveryPage :one
WITH admission AS (
    SELECT a.id AS id FROM ingest.country_admission a
    WHERE a.id = 1 AND a.admission_epoch = $1 AND a.admitted_writer = $2 AND a.enabled FOR SHARE
), lease AS MATERIALIZED (
    SELECT l.id AS id, l.expires_at AS expires_at, l.owner AS owner,
        l.attempt_fence AS attempt_fence, l.admission_epoch AS admission_epoch, l.cancelled_at AS cancelled_at
    FROM ingest.execution_lease l CROSS JOIN admission
    WHERE l.resource_kind = $3 AND l.resource_key = $4 FOR UPDATE OF l
), frozen_window AS MATERIALIZED (
    SELECT w.id AS id, w.page_budget AS page_budget, w.source_attempt_limit AS source_attempt_limit,
        w.source_retry_interval AS source_retry_interval, w.deadline_at AS deadline_at
    FROM ingest.source_window w CROSS JOIN lease
    WHERE w.window_key = $4 AND $3::text = 'source_window'
      AND w.state IN ('frozen', 'progressing') AND w.deadline_at > clock_timestamp()
      AND lease.expires_at > clock_timestamp() AND lease.owner = $5::text
      AND lease.attempt_fence = $6::bigint AND lease.admission_epoch = $1::bigint AND lease.cancelled_at IS NULL
    FOR UPDATE OF w
)
INSERT INTO ingest.discovery_page (source_window_id, page_key, ordinal, purpose, request_cursor,
    request_plan, state, source_attempt_limit, retry_started_at, retry_deadline_at)
SELECT frozen_window.id, $7, $8, $9, $10, $11, 'requested', frozen_window.source_attempt_limit,
    $12, $12::timestamptz + frozen_window.source_retry_interval
FROM frozen_window CROSS JOIN lease
WHERE lease.expires_at > clock_timestamp()
  AND (SELECT count(*) FROM ingest.discovery_page p WHERE p.source_window_id = frozen_window.id) < frozen_window.page_budget
ON CONFLICT (source_window_id, page_key) DO NOTHING
RETURNING id AS id;

-- name: RecordDiscoveryPageCapture :one
WITH admission AS (
    SELECT a.id AS id FROM ingest.country_admission a
    WHERE a.id = 1 AND a.admission_epoch = $1 AND a.admitted_writer = $2 AND a.enabled FOR SHARE
), lease AS MATERIALIZED (
    SELECT l.id AS id, l.expires_at AS expires_at, l.owner AS owner,
        l.attempt_fence AS attempt_fence, l.admission_epoch AS admission_epoch, l.cancelled_at AS cancelled_at, l.next_eligible_at AS next_eligible_at
    FROM ingest.execution_lease l CROSS JOIN admission
    WHERE l.resource_kind = $3 AND l.resource_key = $4 FOR UPDATE OF l
)
UPDATE ingest.discovery_page p SET state = 'captured', capture_key = $8,
    capture_digest = $9, capture_bytes = $10, transport_receipt = $11,
    source_attempts = p.source_attempts + 1, last_attempt_key = $12
FROM lease, ingest.source_window w
WHERE p.id = $7 AND p.source_window_id = w.id AND w.window_key = $4
  AND $3::text = 'source_window' AND w.state IN ('frozen', 'progressing', 'incomplete')
  AND p.state = 'requested' AND p.source_attempts < p.source_attempt_limit
  AND p.source_attempts + 1 = $13::integer AND lease.next_eligible_at <= clock_timestamp()
  AND p.last_attempt_key IS DISTINCT FROM $12::text
  AND lease.expires_at > clock_timestamp() AND lease.owner = $5::text
  AND lease.attempt_fence = $6::bigint AND lease.admission_epoch = $1::bigint AND lease.cancelled_at IS NULL
RETURNING p.id AS id;

-- name: RecordDiscoveryPageDecode :one
WITH admission AS (
    SELECT a.id AS id FROM ingest.country_admission a
    WHERE a.id = 1 AND a.admission_epoch = $1 AND a.admitted_writer = $2 AND a.enabled FOR SHARE
), lease AS MATERIALIZED (
    SELECT l.id AS id, l.expires_at AS expires_at, l.owner AS owner,
        l.attempt_fence AS attempt_fence, l.admission_epoch AS admission_epoch, l.cancelled_at AS cancelled_at
    FROM ingest.execution_lease l CROSS JOIN admission
    WHERE l.resource_kind = $3 AND l.resource_key = $4 FOR UPDATE OF l
)
UPDATE ingest.discovery_page p SET state = 'decoded', decoded_digest = $8,
    cursor_initial = $9, cursor_committed = $10, cursor_next = $11,
    cursor_ordinal = $12, complete = $13, findings = $14
FROM lease, ingest.source_window w
WHERE p.id = $7 AND p.source_window_id = w.id AND w.window_key = $4
  AND $3::text = 'source_window' AND p.state = 'captured'
  AND $9::text = p.request_cursor AND $10::text = p.request_cursor
  AND lease.expires_at > clock_timestamp() AND lease.owner = $5::text
  AND lease.attempt_fence = $6::bigint AND lease.admission_epoch = $1::bigint AND lease.cancelled_at IS NULL
RETURNING p.id AS id;

-- The adapter proves exhaustion, all continuations/findings resolved and child
-- launch acknowledgements before asserting $9 (complete-window accounting).
-- Page ordinal $8 is ledger order, never the decoder's cursor ordinal.
-- name: AcceptDiscoveryPage :one
WITH admission AS (
    SELECT a.id AS id FROM ingest.country_admission a
    WHERE a.id = 1 AND a.admission_epoch = $1 AND a.admitted_writer = $2 AND a.enabled FOR SHARE
), lease AS MATERIALIZED (
    SELECT l.id AS id, l.expires_at AS expires_at, l.owner AS owner,
        l.attempt_fence AS attempt_fence, l.admission_epoch AS admission_epoch, l.cancelled_at AS cancelled_at
    FROM ingest.execution_lease l CROSS JOIN admission
    WHERE l.resource_kind = $3 AND l.resource_key = $4 FOR UPDATE OF l
), accepted AS (
    UPDATE ingest.discovery_page p SET state = 'accepted', acceptance_result_id = $10
    FROM lease, ingest.source_window w, ingest.execution_result r, ingest.execution e
    WHERE p.id = $7 AND p.ordinal = $8 AND p.source_window_id = w.id AND w.window_key = $4
      AND $3::text = 'source_window' AND w.state IN ('frozen', 'progressing', 'incomplete')
      AND p.state = 'decoded' AND p.cursor_initial = p.request_cursor AND p.cursor_committed = p.request_cursor
      AND r.id = $10 AND r.execution_id = e.id AND e.resource_kind = $3 AND e.resource_key = $4
      AND e.admission_epoch = $1 AND e.attempt_fence = $6
      AND lease.expires_at > clock_timestamp() AND lease.owner = $5::text
      AND lease.attempt_fence = $6::bigint AND lease.admission_epoch = $1::bigint AND lease.cancelled_at IS NULL
    RETURNING p.id AS id, p.source_window_id AS source_window_id, p.ordinal AS ordinal,
        p.complete AS complete, p.cursor_next AS cursor_next
), final_page AS (
    SELECT proposed.source_window_id AS source_window_id, proposed.complete AS complete, proposed.cursor_next AS cursor_next
    FROM (
        SELECT accepted.source_window_id AS source_window_id, accepted.ordinal AS ordinal,
            accepted.complete AS complete, accepted.cursor_next AS cursor_next FROM accepted
        UNION ALL
        SELECT p.source_window_id AS source_window_id, p.ordinal AS ordinal,
            p.complete AS complete, p.cursor_next AS cursor_next
        FROM ingest.discovery_page p CROSS JOIN accepted
        WHERE p.source_window_id = accepted.source_window_id AND p.id <> accepted.id
    ) proposed ORDER BY proposed.ordinal DESC LIMIT 1
), progressed AS (
    UPDATE ingest.source_window w
    SET committed_cursor = CASE WHEN $9::boolean AND final_page.complete AND w.committed_cursor = w.initial_cursor
          AND NOT EXISTS (SELECT 1 FROM ingest.discovery_page other
              WHERE other.source_window_id = w.id AND other.id <> accepted.id AND other.state <> 'accepted')
        THEN final_page.cursor_next ELSE w.committed_cursor END,
        state = CASE WHEN $9::boolean AND final_page.complete AND w.committed_cursor = w.initial_cursor
          AND NOT EXISTS (SELECT 1 FROM ingest.discovery_page other
              WHERE other.source_window_id = w.id AND other.id <> accepted.id AND other.state <> 'accepted')
        THEN 'complete' ELSE 'progressing' END, reason = NULL
    FROM accepted CROSS JOIN final_page WHERE w.id = accepted.source_window_id
    RETURNING w.id AS id
)
SELECT accepted.id AS id FROM accepted CROSS JOIN progressed;

-- The phase 2 adapter wraps this helper in the fenced acceptance transaction; never call it outside that transaction.
-- name: RegisterSourceRateLimit :one
INSERT INTO ingest.source_rate_limit (host_key, next_allowed_at, max_in_flight, min_interval)
VALUES ($1, $2, $3, $4)
ON CONFLICT (host_key) DO NOTHING
RETURNING id AS id;

-- Detail requests may finish after window expiry. Their own frozen retry
-- interval applies, while creating more discovery work requires a live window.
-- LockSourceWindowForPlanning precedes registration in this transaction.
-- name: RegisterArtifactRequest :one
WITH admission AS (
    SELECT a.id AS id FROM ingest.country_admission a
    WHERE a.id = 1 AND a.admission_epoch = $1 AND a.admitted_writer = $2 AND a.enabled FOR SHARE
), lease AS MATERIALIZED (
    SELECT l.id AS id, l.expires_at AS expires_at, l.owner AS owner,
        l.attempt_fence AS attempt_fence, l.admission_epoch AS admission_epoch, l.cancelled_at AS cancelled_at
    FROM ingest.execution_lease l CROSS JOIN admission
    WHERE l.resource_kind = $3 AND l.resource_key = $4 FOR UPDATE OF l
), frozen_window AS MATERIALIZED (
    SELECT w.id AS id, w.request_budget AS request_budget, w.source_attempt_limit AS source_attempt_limit,
        w.source_retry_interval AS source_retry_interval
    FROM ingest.source_window w CROSS JOIN lease WHERE w.window_key = $8
      AND $3::text = 'document_version' AND lease.expires_at > clock_timestamp() AND lease.owner = $5::text
      AND lease.attempt_fence = $6::bigint AND lease.admission_epoch = $1::bigint AND lease.cancelled_at IS NULL
    FOR UPDATE OF w
)
INSERT INTO ingest.artifact_request (request_key, source_window_id, source_rate_limit_id,
    document_version_key, owner_kind, owner_key, purpose, expected_role, expected_response_kind,
    required, request_plan, request_cursor, source_attempt_limit, retry_started_at, retry_deadline_at,
    next_eligible_at, state)
SELECT $7, frozen_window.id, $9, $4, 'observation', $10, $11, $12, $13, $14, $15, $16,
    frozen_window.source_attempt_limit, $17, $17::timestamptz + frozen_window.source_retry_interval, $17, 'due'
FROM frozen_window CROSS JOIN lease
WHERE lease.expires_at > clock_timestamp()
  AND (SELECT count(*) FROM ingest.artifact_request r WHERE r.source_window_id = frozen_window.id) < frozen_window.request_budget
ON CONFLICT (request_key) DO NOTHING
RETURNING id AS id;

-- Lock order: admission, lease, request, host. Eligibility is checked after
-- locking, so a concurrent claimant cannot spend an already claimed host slot.
-- $7 is the scheduling clock; lease validity always uses the database clock.
-- name: ClaimArtifactRequest :one
WITH admission AS (
    SELECT a.id AS id FROM ingest.country_admission a
    WHERE a.id = 1 AND a.admission_epoch = $1 AND a.admitted_writer = $2 AND a.enabled FOR SHARE
), lease AS MATERIALIZED (
    SELECT l.id AS id, l.expires_at AS expires_at, l.owner AS owner,
        l.attempt_fence AS attempt_fence, l.admission_epoch AS admission_epoch, l.cancelled_at AS cancelled_at
    FROM ingest.execution_lease l CROSS JOIN admission
    WHERE l.resource_kind = $3 AND l.resource_key = $4 FOR UPDATE OF l
), candidate AS MATERIALIZED (
    SELECT r.id AS id, r.source_rate_limit_id AS source_rate_limit_id,
        r.next_eligible_at AS next_eligible_at, r.retry_after AS retry_after,
        r.retry_deadline_at AS retry_deadline_at, r.source_attempts AS source_attempts,
        r.source_attempt_limit AS source_attempt_limit
    FROM ingest.artifact_request r
    JOIN ingest.source_rate_limit pacing ON pacing.id = r.source_rate_limit_id
    CROSS JOIN lease
    WHERE r.state = 'due' AND r.next_eligible_at <= $7::timestamptz
      AND r.retry_deadline_at > $7::timestamptz AND r.source_attempts < r.source_attempt_limit
      AND (r.retry_after IS NULL OR r.retry_after <= $7::timestamptz)
      AND pacing.next_allowed_at <= $7::timestamptz AND pacing.in_flight < pacing.max_in_flight
      AND r.document_version_key = $4 AND $3::text = 'document_version'
      AND lease.expires_at > clock_timestamp() AND lease.owner = $5::text
      AND lease.attempt_fence = $6::bigint AND lease.admission_epoch = $1::bigint AND lease.cancelled_at IS NULL
    ORDER BY r.next_eligible_at, r.id LIMIT 1 FOR UPDATE OF r SKIP LOCKED
), host AS MATERIALIZED (
    SELECT h.id AS id, h.next_allowed_at AS next_allowed_at, h.in_flight AS in_flight,
        h.max_in_flight AS max_in_flight, h.min_interval AS min_interval
    FROM ingest.source_rate_limit h JOIN candidate c ON c.source_rate_limit_id = h.id FOR UPDATE OF h
), slot AS (
    UPDATE ingest.source_rate_limit h SET in_flight = h.in_flight + 1,
        next_allowed_at = $7::timestamptz + host.min_interval
    FROM host, candidate, lease WHERE h.id = host.id
      AND host.next_allowed_at <= $7::timestamptz AND host.in_flight < host.max_in_flight
      AND candidate.next_eligible_at <= $7::timestamptz
      AND (candidate.retry_after IS NULL OR candidate.retry_after <= $7::timestamptz)
      AND candidate.retry_deadline_at > $7::timestamptz AND candidate.source_attempts < candidate.source_attempt_limit
      AND lease.expires_at > clock_timestamp()
    RETURNING h.id AS id
)
UPDATE ingest.artifact_request r SET state = 'leased', next_eligible_at = NULL,
    claimed_epoch = $1, claimed_fence = $6
FROM candidate, slot WHERE r.id = candidate.id AND slot.id = candidate.source_rate_limit_id
RETURNING r.id AS id, r.request_key AS request_key, r.request_plan AS request_plan,
    r.request_cursor AS request_cursor, r.source_attempts AS source_attempts,
    r.source_attempts + 1 AS next_attempt;

-- One durable source attempt key is allocated before Fetch and reused after
-- response loss. Replays and worker-loss recovery never increment source count.
-- $9 is successful Fetch; $10 is its capture key, or NULL for a source failure.
-- $14 is recorded backoff including jitter; $15 preserves Retry-After.
-- $16 is the durable attempt ordinal returned by the claim; old attempts
-- cannot be replayed into a later claim even under the same lease fence.
-- $17 is the scheduling clock; lease validity always uses the database clock.
-- name: RecordArtifactTransport :one
WITH admission AS (
    SELECT a.id AS id FROM ingest.country_admission a
    WHERE a.id = 1 AND a.admission_epoch = $1 AND a.admitted_writer = $2 AND a.enabled FOR SHARE
), lease AS MATERIALIZED (
    SELECT l.id AS id, l.expires_at AS expires_at, l.owner AS owner,
        l.attempt_fence AS attempt_fence, l.admission_epoch AS admission_epoch, l.cancelled_at AS cancelled_at
    FROM ingest.execution_lease l CROSS JOIN admission
    WHERE l.resource_kind = $3 AND l.resource_key = $4 FOR UPDATE OF l
), receipt AS (
    UPDATE ingest.artifact_request r SET source_attempts = r.source_attempts + 1,
        last_attempt_key = $8, capture_key = $10, capture_digest = $11,
        capture_bytes = $12, transport_receipt = $13,
        state = CASE WHEN $9::boolean THEN 'fetched'
            WHEN r.source_attempts + 1 >= r.source_attempt_limit OR r.retry_deadline_at <= $17::timestamptz THEN 'exhausted'
            ELSE 'due' END,
        reason = CASE WHEN NOT $9::boolean AND
            (r.source_attempts + 1 >= r.source_attempt_limit OR r.retry_deadline_at <= $17::timestamptz) THEN 'source_retry_exhausted' ELSE NULL END,
        next_eligible_at = CASE WHEN NOT $9::boolean AND r.source_attempts + 1 < r.source_attempt_limit
            AND r.retry_deadline_at > $17::timestamptz THEN GREATEST($14::timestamptz, $15::timestamptz, r.retry_after, r.eligibility_watermark) ELSE NULL END,
        retry_after = GREATEST(r.retry_after, $15::timestamptz)
    FROM lease
    WHERE r.id = $7 AND r.document_version_key = $4 AND $3::text = 'document_version'
      AND r.state = 'leased' AND r.claimed_epoch = $1 AND r.claimed_fence = $6
      AND r.last_attempt_key IS DISTINCT FROM $8::text AND r.source_attempts < r.source_attempt_limit
      AND r.source_attempts + 1 = $16::integer
      AND $9::boolean = ($10::text IS NOT NULL)
      AND lease.expires_at > clock_timestamp() AND lease.owner = $5::text
      AND lease.attempt_fence = $6::bigint AND lease.admission_epoch = $1::bigint AND lease.cancelled_at IS NULL
    RETURNING r.id AS id, r.source_rate_limit_id AS source_rate_limit_id
), released AS (
    UPDATE ingest.source_rate_limit h SET in_flight = h.in_flight - 1,
        next_allowed_at = GREATEST(h.next_allowed_at, $15::timestamptz)
    FROM receipt WHERE h.id = receipt.source_rate_limit_id
    RETURNING h.id AS id
)
SELECT receipt.id AS id FROM receipt CROSS JOIN released;

-- Current owner recovery releases the abandoned slot once. Fetched receipts
-- are reconciled in place, never returned to due or downloaded again.
-- $8 is the scheduling clock; lease validity always uses the database clock.
-- name: RecoverArtifactRequest :one
WITH admission AS (
    SELECT a.id AS id FROM ingest.country_admission a
    WHERE a.id = 1 AND a.admission_epoch = $1 AND a.admitted_writer = $2 AND a.enabled FOR SHARE
), lease AS MATERIALIZED (
    SELECT l.id AS id, l.expires_at AS expires_at, l.owner AS owner,
        l.attempt_fence AS attempt_fence, l.admission_epoch AS admission_epoch, l.cancelled_at AS cancelled_at
    FROM ingest.execution_lease l CROSS JOIN admission
    WHERE l.resource_kind = $3 AND l.resource_key = $4 FOR UPDATE OF l
), recovered AS (
    UPDATE ingest.artifact_request r SET state = 'due',
        next_eligible_at = GREATEST($8::timestamptz, r.retry_after, r.eligibility_watermark),
        infrastructure_attempts = r.infrastructure_attempts + 1
    FROM lease WHERE r.id = $7 AND r.document_version_key = $4 AND $3::text = 'document_version'
      AND r.state = 'leased' AND r.infrastructure_attempts < 3
      AND (r.claimed_epoch < $1 OR (r.claimed_epoch = $1 AND r.claimed_fence < $6))
      AND lease.expires_at > clock_timestamp() AND lease.owner = $5::text
      AND lease.attempt_fence = $6::bigint AND lease.admission_epoch = $1::bigint AND lease.cancelled_at IS NULL
    RETURNING r.id AS id, r.source_rate_limit_id AS source_rate_limit_id
), released AS (
    UPDATE ingest.source_rate_limit h SET in_flight = h.in_flight - 1
    FROM recovered WHERE h.id = recovered.source_rate_limit_id RETURNING h.id AS id
)
SELECT recovered.id AS id FROM recovered CROSS JOIN released;

-- The live current owner closes an abandoned third infrastructure attempt.
-- $8 is a non-empty closure reason. Source attempts and receipts are preserved.
-- Only the leased-to-expired transition releases a slot; replay returns no row.
-- name: ExpireArtifactRequest :one
WITH admission AS (
    SELECT a.id AS id FROM ingest.country_admission a
    WHERE a.id = 1 AND a.admission_epoch = $1 AND a.admitted_writer = $2 AND a.enabled FOR SHARE
), lease AS MATERIALIZED (
    SELECT l.id AS id, l.expires_at AS expires_at, l.owner AS owner,
        l.attempt_fence AS attempt_fence, l.admission_epoch AS admission_epoch, l.cancelled_at AS cancelled_at
    FROM ingest.execution_lease l CROSS JOIN admission
    WHERE l.resource_kind = $3 AND l.resource_key = $4 FOR UPDATE OF l
), expired AS (
    UPDATE ingest.artifact_request r SET state = 'expired', reason = $8, next_eligible_at = NULL
    FROM lease WHERE r.id = $7 AND r.document_version_key = $4 AND $3::text = 'document_version'
      AND r.state = 'leased' AND r.infrastructure_attempts = 3
      AND (r.claimed_epoch < $1 OR (r.claimed_epoch = $1 AND r.claimed_fence < $6))
      AND $8::text <> ''
      AND lease.expires_at > clock_timestamp() AND lease.owner = $5::text
      AND lease.attempt_fence = $6::bigint AND lease.admission_epoch = $1::bigint AND lease.cancelled_at IS NULL
    RETURNING r.id AS id, r.source_rate_limit_id AS source_rate_limit_id
), released AS (
    UPDATE ingest.source_rate_limit h SET in_flight = h.in_flight - 1
    FROM expired WHERE h.id = expired.source_rate_limit_id RETURNING h.id AS id
)
SELECT expired.id AS id FROM expired CROSS JOIN released;

-- Bronze operations below participate in the same fenced acceptance transaction.
-- The adapter locks and validates the owning lease and all cross-schema keys
-- before calling them, then rechecks expiry before commit. No stand-alone writes.
-- The phase 2 adapter wraps this helper in the fenced acceptance transaction; never call it outside that transaction.
-- name: RegisterSourceBlob :one
WITH inserted AS (
    INSERT INTO bronze.blob (sha256, byte_size, media_type, object_ref, capture_source, capture_ref, captured_at)
    VALUES ($1, $2, $3, $4, $5, $6, $7)
    ON CONFLICT (sha256) DO NOTHING RETURNING id AS id
)
SELECT inserted.id AS id FROM inserted
UNION ALL
SELECT b.id AS id FROM bronze.blob b WHERE b.sha256 = $1 AND b.byte_size = $2
  AND NOT EXISTS (SELECT 1 FROM inserted);

-- name: ReadSourceBlob :one
SELECT b.id AS id, b.sha256 AS sha256, b.byte_size AS byte_size,
    b.media_type AS media_type, b.object_ref AS object_ref, b.capture_source AS capture_source,
    b.capture_ref AS capture_ref, b.captured_at AS captured_at FROM bronze.blob b WHERE b.sha256 = $1;

-- The phase 2 adapter wraps this helper in the fenced acceptance transaction; never call it outside that transaction.
-- name: InsertSourceRecord :one
WITH inserted AS (
    INSERT INTO bronze.source_record (source_id, external_id) VALUES ($1, $2)
    ON CONFLICT (source_id, external_id) DO NOTHING RETURNING id AS id
)
SELECT inserted.id AS id FROM inserted
UNION ALL
SELECT s.id AS id FROM bronze.source_record s WHERE s.source_id = $1 AND s.external_id = $2
  AND NOT EXISTS (SELECT 1 FROM inserted);

-- The phase 2 adapter wraps this helper in the fenced acceptance transaction; never call it outside that transaction.
-- name: InsertObservationRevision :one
INSERT INTO bronze.observation_revision (observation_key, source_record_id, observation_digest,
    metadata_digest, source_tree_digest, relations_digest, source_window_key, document_version_key,
    execution_key, result_key, capture_blob_id, observed_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
ON CONFLICT DO NOTHING
RETURNING id AS id;

-- name: ReadObservationRevision :one
SELECT o.id AS id, o.observation_key AS observation_key, o.source_record_id AS source_record_id,
    o.observation_digest AS observation_digest, o.metadata_digest AS metadata_digest,
    o.source_tree_digest AS source_tree_digest, o.relations_digest AS relations_digest,
    o.source_window_key AS source_window_key, o.document_version_key AS document_version_key,
    o.execution_key AS execution_key, o.result_key AS result_key,
    o.capture_blob_id AS capture_blob_id, o.observed_at AS observed_at
FROM bronze.observation_revision o WHERE o.source_record_id = $1 AND o.observation_digest = $2;

-- The phase 2 adapter wraps this helper in the fenced acceptance transaction; never call it outside that transaction.
-- name: InsertSourceArtifact :one
INSERT INTO bronze.artifact (artifact_key, blob_id, role, declared_filename, format, source_url, capture_url)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (artifact_key) DO NOTHING RETURNING id AS id;

-- The phase 2 adapter wraps this helper in the fenced acceptance transaction; never call it outside that transaction.
-- name: LinkObservationArtifact :one
INSERT INTO bronze.observation_artifact (observation_revision_id, artifact_id, role, ordinal, required)
SELECT $1, a.id AS artifact_id, $3, $4, $5 FROM bronze.artifact a WHERE a.id = $2 AND a.role = $3
ON CONFLICT (observation_revision_id, role, ordinal) DO NOTHING RETURNING id AS id;

-- Lock the catalog row before adding, transferring or removing edges. Cleanup
-- and object GC must acquire this same lock and prove the protected closure.
-- The phase 2 adapter wraps this helper in the fenced acceptance transaction; never call it outside that transaction.
-- name: AddBlobRetentionReference :one
WITH blob AS MATERIALIZED (
    SELECT b.id AS id FROM bronze.blob b WHERE b.id = $1 FOR UPDATE
), inserted AS (
    INSERT INTO bronze.blob_retention_reference (blob_id, owner_kind, owner_key)
    SELECT blob.id AS blob_id, $2, $3 FROM blob ON CONFLICT (blob_id, owner_kind, owner_key) DO NOTHING
    RETURNING id AS id
)
SELECT inserted.id AS id FROM inserted
UNION ALL
SELECT r.id AS id FROM bronze.blob_retention_reference r CROSS JOIN blob
WHERE r.blob_id = blob.id AND r.owner_kind = $2 AND r.owner_key = $3 AND NOT EXISTS (SELECT 1 FROM inserted);

-- Only the source ledger schedules discovery retries. Infrastructure retries
-- never call this operation without a new durable source-attempt receipt.
-- Keep eligibility on the window lease, not on an independent page clock.
-- name: RecordDiscoveryPageSourceRetry :one
WITH admission AS (
    SELECT a.id AS id FROM ingest.country_admission a
    WHERE a.id = 1 AND a.admission_epoch = $1 AND a.admitted_writer = $2 AND a.enabled FOR SHARE
), lease AS MATERIALIZED (
    SELECT l.id AS id, l.expires_at AS expires_at, l.owner AS owner,
        l.attempt_fence AS attempt_fence, l.admission_epoch AS admission_epoch, l.cancelled_at AS cancelled_at
    FROM ingest.execution_lease l CROSS JOIN admission
    WHERE l.resource_kind = $3 AND l.resource_key = $4 FOR UPDATE OF l
), receipt AS (
    UPDATE ingest.discovery_page p SET source_attempts = p.source_attempts + 1,
        last_attempt_key = $8, retry_after = GREATEST(p.retry_after, $11::timestamptz),
        state = CASE WHEN w.deadline_at > clock_timestamp() AND
            (p.source_attempts + 1 >= p.source_attempt_limit OR p.retry_deadline_at <= clock_timestamp())
            THEN 'failed' ELSE 'requested' END,
        reason = CASE WHEN w.deadline_at > clock_timestamp() AND
            (p.source_attempts + 1 >= p.source_attempt_limit OR p.retry_deadline_at <= clock_timestamp())
            THEN 'source_retry_exhausted' ELSE NULL END
    FROM lease, ingest.source_window w
    WHERE p.id = $7 AND p.source_window_id = w.id AND w.window_key = $4 AND $3::text = 'source_window'
      AND w.state IN ('frozen', 'progressing') AND p.state = 'requested'
      AND p.source_attempts + 1 = $9::integer AND p.source_attempts < p.source_attempt_limit
      AND p.last_attempt_key IS DISTINCT FROM $8::text
      AND lease.expires_at > clock_timestamp() AND lease.owner = $5::text
      AND lease.attempt_fence = $6::bigint AND lease.admission_epoch = $1::bigint AND lease.cancelled_at IS NULL
    RETURNING p.id AS id, p.source_window_id AS source_window_id, p.state AS state
), scheduled AS (
    UPDATE ingest.execution_lease l SET next_eligible_at = GREATEST(l.next_eligible_at, $10::timestamptz, $11::timestamptz)
    FROM lease, receipt WHERE l.id = lease.id RETURNING l.id AS id
), accounted AS (
    UPDATE ingest.source_window w SET
        state = CASE WHEN receipt.state = 'failed' THEN 'failed'
            WHEN w.deadline_at <= clock_timestamp() THEN 'incomplete' ELSE w.state END,
        reason = CASE WHEN receipt.state = 'failed' THEN 'source_retry_exhausted'
            WHEN w.deadline_at <= clock_timestamp() THEN 'window_deadline' ELSE w.reason END
    FROM receipt WHERE w.id = receipt.source_window_id RETURNING w.id AS id, w.state AS state
), closed_pages AS (
    UPDATE ingest.discovery_page p SET state = 'failed', reason = 'window_closed'
    FROM accounted, receipt WHERE accounted.state = 'failed' AND p.source_window_id = accounted.id
      AND p.id <> receipt.id AND p.state IN ('requested', 'captured', 'decoded')
    RETURNING p.id AS id
)
SELECT receipt.id AS id FROM receipt CROSS JOIN scheduled CROSS JOIN accounted;

-- name: ReadArtifactRequest :one
SELECT r.id AS id, r.request_key AS request_key, r.source_window_id AS source_window_id,
    r.source_rate_limit_id AS source_rate_limit_id, r.document_version_key AS document_version_key,
    r.owner_kind AS owner_kind, r.owner_key AS owner_key, r.purpose AS purpose,
    r.expected_role AS expected_role, r.expected_response_kind AS expected_response_kind,
    r.required AS required, r.request_plan AS request_plan, r.request_cursor AS request_cursor,
    r.source_attempt_limit AS source_attempt_limit, r.retry_started_at AS retry_started_at,
    r.retry_deadline_at AS retry_deadline_at, r.source_attempts AS source_attempts,
    r.infrastructure_attempts AS infrastructure_attempts, r.next_eligible_at AS next_eligible_at,
    r.eligibility_watermark AS eligibility_watermark,
    r.retry_after AS retry_after, r.last_attempt_key AS last_attempt_key,
    r.claimed_epoch AS claimed_epoch, r.claimed_fence AS claimed_fence,
    r.state AS state, r.reason AS reason, r.capture_key AS capture_key,
    r.capture_digest AS capture_digest, r.capture_bytes AS capture_bytes, r.transport_receipt AS transport_receipt
FROM ingest.artifact_request r WHERE r.request_key = $1;

-- Call in a separate statement before RegisterDiscoveryPage or
-- RegisterArtifactRequest, in the same READ COMMITTED transaction. A statement
-- that waits for a window lock keeps its old snapshot of the child rows; the
-- next statement must count with a fresh snapshot while holding this lock.
-- name: LockSourceWindowForPlanning :one
WITH admission AS (
    SELECT a.id AS id FROM ingest.country_admission a
    WHERE a.id = 1 AND a.admission_epoch = $1 AND a.admitted_writer = $2 AND a.enabled FOR SHARE
), lease AS MATERIALIZED (
    SELECT l.id AS id, l.expires_at AS expires_at, l.owner AS owner,
        l.attempt_fence AS attempt_fence, l.admission_epoch AS admission_epoch, l.cancelled_at AS cancelled_at
    FROM ingest.execution_lease l CROSS JOIN admission
    WHERE l.resource_kind = $3 AND l.resource_key = $4 FOR UPDATE OF l
), frozen_window AS MATERIALIZED (
    SELECT w.id AS id FROM ingest.source_window w CROSS JOIN lease
    WHERE w.window_key = $7 AND $3::text IN ('source_window', 'document_version')
      AND ($3::text = 'document_version' OR w.window_key = $4)
      AND lease.expires_at > clock_timestamp() AND lease.owner = $5::text
      AND lease.attempt_fence = $6::bigint AND lease.admission_epoch = $1::bigint AND lease.cancelled_at IS NULL
    FOR UPDATE OF w
)
SELECT frozen_window.id AS id FROM frozen_window CROSS JOIN lease WHERE lease.expires_at > clock_timestamp();
