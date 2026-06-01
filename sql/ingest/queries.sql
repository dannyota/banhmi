-- name: GetDiscoverCursor :one
SELECT * FROM ingest.discover_cursor WHERE source = $1 AND keyword = $2;

-- name: UpsertDiscoverCursor :exec
-- Per-(source, keyword) watermark. expected_total snapshots data.total for the
-- slice; last_seen_total tracks drift so a slow keyword can't be silently skipped.
INSERT INTO ingest.discover_cursor (
    source, keyword, watermark, expected_total, last_seen_total, last_run_at,
    created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $7
)
ON CONFLICT (source, keyword) DO UPDATE SET
    watermark = EXCLUDED.watermark,
    expected_total = EXCLUDED.expected_total,
    last_seen_total = EXCLUDED.last_seen_total,
    last_run_at = EXCLUDED.last_run_at,
    updated_at = EXCLUDED.updated_at;

-- name: UpsertFetchDoc :one
-- Parent ledger row, idempotent on (source, external_id). Re-discovery converges
-- here. A changed discovery fingerprint re-opens the document so Fetch re-plans
-- the body/files; unchanged rows preserve their current fetch state.
INSERT INTO ingest.fetch_doc (
    source, external_id, state, in_scope, provenance, content_hash, detail_url,
    discovered_at, updated_at
) VALUES (
    $1, $2, COALESCE(sqlc.narg(state), 'discovered'), $3, $4, $5, $6, $7, $7
)
ON CONFLICT (source, external_id) DO UPDATE SET
    in_scope = ingest.fetch_doc.in_scope OR EXCLUDED.in_scope,
    provenance = CASE
        WHEN ingest.fetch_doc.provenance = '' THEN EXCLUDED.provenance
        WHEN ingest.fetch_doc.provenance = 'relation' AND EXCLUDED.provenance <> 'relation'
            THEN EXCLUDED.provenance
        ELSE ingest.fetch_doc.provenance
    END,
    state = CASE
        WHEN EXCLUDED.content_hash IS NOT NULL
         AND ingest.fetch_doc.content_hash IS DISTINCT FROM EXCLUDED.content_hash
            THEN COALESCE(sqlc.narg(state), 'discovered')
        ELSE ingest.fetch_doc.state
    END,
    plan_ready = CASE
        WHEN EXCLUDED.content_hash IS NOT NULL
         AND ingest.fetch_doc.content_hash IS DISTINCT FROM EXCLUDED.content_hash
            THEN FALSE
        ELSE ingest.fetch_doc.plan_ready
    END,
    artifacts_expected = CASE
        WHEN EXCLUDED.content_hash IS NOT NULL
         AND ingest.fetch_doc.content_hash IS DISTINCT FROM EXCLUDED.content_hash
            THEN 0
        ELSE ingest.fetch_doc.artifacts_expected
    END,
    artifacts_done = CASE
        WHEN EXCLUDED.content_hash IS NOT NULL
         AND ingest.fetch_doc.content_hash IS DISTINCT FROM EXCLUDED.content_hash
            THEN 0
        ELSE ingest.fetch_doc.artifacts_done
    END,
    artifacts_failed = CASE
        WHEN EXCLUDED.content_hash IS NOT NULL
         AND ingest.fetch_doc.content_hash IS DISTINCT FROM EXCLUDED.content_hash
            THEN 0
        ELSE ingest.fetch_doc.artifacts_failed
    END,
    tree_recheck_after = CASE
        WHEN EXCLUDED.content_hash IS NOT NULL
         AND ingest.fetch_doc.content_hash IS DISTINCT FROM EXCLUDED.content_hash
            THEN NULL
        ELSE ingest.fetch_doc.tree_recheck_after
    END,
    tree_recheck_count = CASE
        WHEN EXCLUDED.content_hash IS NOT NULL
         AND ingest.fetch_doc.content_hash IS DISTINCT FROM EXCLUDED.content_hash
            THEN 0
        ELSE ingest.fetch_doc.tree_recheck_count
    END,
    content_recheck_after = CASE
        WHEN EXCLUDED.content_hash IS NOT NULL
         AND ingest.fetch_doc.content_hash IS DISTINCT FROM EXCLUDED.content_hash
            THEN NULL
        ELSE ingest.fetch_doc.content_recheck_after
    END,
    content_recheck_count = CASE
        WHEN EXCLUDED.content_hash IS NOT NULL
         AND ingest.fetch_doc.content_hash IS DISTINCT FROM EXCLUDED.content_hash
            THEN 0
        ELSE ingest.fetch_doc.content_recheck_count
    END,
    content_recheck_reason = CASE
        WHEN EXCLUDED.content_hash IS NOT NULL
         AND ingest.fetch_doc.content_hash IS DISTINCT FROM EXCLUDED.content_hash
            THEN ''
        ELSE ingest.fetch_doc.content_recheck_reason
    END,
    content_hash = COALESCE(EXCLUDED.content_hash, ingest.fetch_doc.content_hash),
    detail_url = COALESCE(EXCLUDED.detail_url, ingest.fetch_doc.detail_url),
    updated_at = EXCLUDED.updated_at
RETURNING *;

-- name: GetFetchDoc :one
SELECT * FROM ingest.fetch_doc WHERE source = $1 AND external_id = $2;

-- name: GetFetchDocByID :one
-- Resolve a claimed artifact's parent document (Fetch needs the source +
-- external_id + detail_url to fetch and to link bronze rows).
SELECT * FROM ingest.fetch_doc WHERE id = $1;

-- name: ListCompleteFetchDocs :many
-- Backfill/dev helper: enumerate completed fetch docs so Extract can be
-- triggered for already-fetched corpus rows without running Normalize/Index.
SELECT * FROM ingest.fetch_doc
WHERE state = 'complete'
  AND in_scope
ORDER BY id
LIMIT sqlc.arg(row_limit);

-- name: ListCompleteFetchDocIDsAfter :many
-- Paged ExtractAll helper: enumerate only the next completed, in-scope
-- fetch_doc IDs so the workflow never loads or schedules the whole corpus at
-- once.
SELECT id FROM ingest.fetch_doc
WHERE state = 'complete'
  AND in_scope
  AND id > sqlc.arg(after_id)
ORDER BY id
LIMIT sqlc.arg(row_limit);

-- name: RecordDocDiscovery :exec
-- Append-only provenance: each (doc, keyword) hit and each relation edge that
-- surfaced the doc. '' sentinels keep the UNIQUE tuple total; re-recording is a
-- no-op so the ledger never loses (or duplicates) a reason a doc is in scope.
INSERT INTO ingest.doc_discovery (
    fetch_doc_id, via, keyword, src_fetch_doc_id, relation_type, discovered_at
) VALUES (
    $1, $2, $3, $4, $5, $6
)
ON CONFLICT (fetch_doc_id, via, keyword, src_fetch_doc_id, relation_type) DO NOTHING;

-- name: EnqueueArtifact :one
-- Child work item, idempotent on (fetch_doc_id, kind, ref_key) so re-planning a
-- doc converges. Re-pointing url/gateway_url and resetting a non-terminal row to
-- pending lets a re-plan pick up moved URLs without losing terminal results.
INSERT INTO ingest.fetch_artifact (
    fetch_doc_id, kind, ref_key, file_kind, file_name, url, url_expires_at, gateway_url,
    target_source, target_ext_id, is_optional, state, max_attempts,
    next_attempt_at, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 'pending', $12, $13, $14, $14
)
ON CONFLICT (fetch_doc_id, kind, ref_key) DO UPDATE SET
    file_kind = EXCLUDED.file_kind,
    file_name = EXCLUDED.file_name,
    url = EXCLUDED.url,
    url_expires_at = EXCLUDED.url_expires_at,
    gateway_url = EXCLUDED.gateway_url,
    target_source = EXCLUDED.target_source,
    target_ext_id = EXCLUDED.target_ext_id,
    is_optional = EXCLUDED.is_optional,
    max_attempts = EXCLUDED.max_attempts,
    state = CASE
        WHEN ingest.fetch_artifact.state IN ('done', 'skipped', 'superseded', 'dead')
         AND EXISTS (
            SELECT 1
            FROM ingest.fetch_doc d
            WHERE d.id = ingest.fetch_artifact.fetch_doc_id
              AND d.state = 'discovered'
              AND d.plan_ready = FALSE
         )
            THEN 'pending'
        WHEN ingest.fetch_artifact.state IN ('done', 'skipped', 'superseded', 'dead')
            THEN ingest.fetch_artifact.state
        ELSE 'pending'
    END,
    lease_owner = CASE
        WHEN ingest.fetch_artifact.state IN ('done', 'skipped', 'superseded', 'dead')
         AND NOT EXISTS (
            SELECT 1
            FROM ingest.fetch_doc d
            WHERE d.id = ingest.fetch_artifact.fetch_doc_id
              AND d.state = 'discovered'
              AND d.plan_ready = FALSE
         )
            THEN ingest.fetch_artifact.lease_owner
        ELSE NULL
    END,
    lease_expires_at = CASE
        WHEN ingest.fetch_artifact.state IN ('done', 'skipped', 'superseded', 'dead')
         AND NOT EXISTS (
            SELECT 1
            FROM ingest.fetch_doc d
            WHERE d.id = ingest.fetch_artifact.fetch_doc_id
              AND d.state = 'discovered'
              AND d.plan_ready = FALSE
         )
            THEN ingest.fetch_artifact.lease_expires_at
        ELSE NULL
    END,
    attempts = CASE
        WHEN ingest.fetch_artifact.state IN ('done', 'skipped', 'superseded', 'dead')
         AND EXISTS (
            SELECT 1
            FROM ingest.fetch_doc d
            WHERE d.id = ingest.fetch_artifact.fetch_doc_id
              AND d.state = 'discovered'
              AND d.plan_ready = FALSE
         )
            THEN 0
        ELSE ingest.fetch_artifact.attempts
    END,
    next_attempt_at = CASE
        WHEN ingest.fetch_artifact.state IN ('done', 'skipped', 'superseded', 'dead')
         AND NOT EXISTS (
            SELECT 1
            FROM ingest.fetch_doc d
            WHERE d.id = ingest.fetch_artifact.fetch_doc_id
              AND d.state = 'discovered'
              AND d.plan_ready = FALSE
         )
            THEN ingest.fetch_artifact.next_attempt_at
        ELSE NULL
    END,
    content_hash = CASE
        WHEN ingest.fetch_artifact.state IN ('done', 'skipped', 'superseded', 'dead')
         AND EXISTS (
            SELECT 1
            FROM ingest.fetch_doc d
            WHERE d.id = ingest.fetch_artifact.fetch_doc_id
              AND d.state = 'discovered'
              AND d.plan_ready = FALSE
         )
            THEN NULL
        ELSE ingest.fetch_artifact.content_hash
    END,
    result_ref = CASE
        WHEN ingest.fetch_artifact.state IN ('done', 'skipped', 'superseded', 'dead')
         AND EXISTS (
            SELECT 1
            FROM ingest.fetch_doc d
            WHERE d.id = ingest.fetch_artifact.fetch_doc_id
              AND d.state = 'discovered'
              AND d.plan_ready = FALSE
         )
            THEN NULL
        ELSE ingest.fetch_artifact.result_ref
    END,
    last_error = CASE
        WHEN ingest.fetch_artifact.state IN ('done', 'skipped', 'superseded', 'dead')
         AND EXISTS (
            SELECT 1
            FROM ingest.fetch_doc d
            WHERE d.id = ingest.fetch_artifact.fetch_doc_id
              AND d.state = 'discovered'
              AND d.plan_ready = FALSE
         )
            THEN NULL
        ELSE ingest.fetch_artifact.last_error
    END,
    updated_at = EXCLUDED.updated_at
RETURNING id;

-- name: SupersedeMissingFileArtifacts :exec
-- After a fresh detail fetch, retire stale file artifacts that the source no
-- longer advertises. This keeps refetch completeness from being blocked by
-- files that disappeared from the official detail response.
UPDATE ingest.fetch_artifact
SET state = 'superseded',
    lease_owner = NULL,
    lease_expires_at = NULL,
    next_attempt_at = NULL,
    updated_at = @updated_at
WHERE fetch_doc_id = @fetch_doc_id
  AND kind = 'file'
  AND NOT (ref_key = ANY(sqlc.arg(active_ref_keys)::text[]));

-- name: SetDocPlanReady :exec
-- Mark the doc's artifacts fully enumerated and snapshot the expected count, so
-- completeness becomes a compare against artifacts_done. Moves discovered/planning
-- into fetching.
UPDATE ingest.fetch_doc
SET plan_ready = TRUE,
    artifacts_expected = $2,
    state = CASE WHEN state IN ('discovered', 'planning') THEN 'fetching' ELSE state END,
    content_recheck_after = NULL,
    updated_at = $3
WHERE id = $1;

-- name: ClaimArtifacts :many
-- Crash-safe lease: take up to claim_limit due body/tree/file artifacts for one
-- source. Includes lease-expired claimed rows so a crashed worker cannot wedge
-- the queue forever. Relation artifacts are intentionally left for the later
-- enrichment fetcher rather than being claimed by this drainer.
UPDATE ingest.fetch_artifact
SET state = 'claimed',
    lease_owner = @lease_owner,
    lease_expires_at = @lease_expires_at,
    attempts = attempts + 1,
    updated_at = @now
WHERE id IN (
    SELECT a.id
    FROM ingest.fetch_artifact a
    JOIN ingest.fetch_doc d ON d.id = a.fetch_doc_id
    WHERE d.source = @source
      AND a.kind IN ('body', 'tree', 'file')
      AND NOT (a.kind = 'file' AND d.content_recheck_after IS NOT NULL)
      AND (
        (
          a.state IN ('pending', 'error')
          AND (a.next_attempt_at IS NULL OR a.next_attempt_at <= @now)
          AND (a.lease_expires_at IS NULL OR a.lease_expires_at <= @now)
        )
        OR (
          a.state = 'claimed'
          AND a.lease_expires_at IS NOT NULL
          AND a.lease_expires_at <= @now
        )
        OR (
          a.kind = 'tree'
          AND d.tree_recheck_after IS NOT NULL
          AND d.tree_recheck_after <= @now
          AND a.state IN ('skipped', 'superseded')
        )
      )
    -- Complete already-planned documents before planning more bodies. Without
    -- this, a capped run over a large discovery backlog can leave newly enqueued
    -- file artifacts behind hundreds of older body artifacts. A source-content
    -- recheck claims its body before files so detail URLs are fresh first.
    ORDER BY
      CASE
        WHEN a.kind = 'body' AND d.content_recheck_after IS NOT NULL THEN 0
        WHEN a.kind = 'tree' AND d.tree_recheck_after IS NOT NULL THEN 1
        WHEN a.kind = 'file' THEN 2
        WHEN a.kind = 'tree' THEN 3
        WHEN a.kind = 'body' THEN 4
        ELSE 5
      END,
      a.next_attempt_at NULLS FIRST,
      a.id
    LIMIT sqlc.arg(claim_limit)
    FOR UPDATE SKIP LOCKED
)
RETURNING *;

-- name: MarkArtifactDone :exec
-- Terminal success: record the content_hash + result_ref handle and clear the lease.
UPDATE ingest.fetch_artifact
SET state = 'done',
    content_hash = $2,
    result_ref = $3,
    last_error = NULL,
    lease_owner = NULL,
    lease_expires_at = NULL,
    next_attempt_at = NULL,
    updated_at = $4
WHERE id = $1;

-- name: MarkArtifactSkipped :exec
-- Terminal skip for an optional artifact that does not exist at the source; it
-- does not count against completeness.
UPDATE ingest.fetch_artifact
SET state = 'skipped',
    last_error = NULL,
    lease_owner = NULL,
    lease_expires_at = NULL,
    next_attempt_at = NULL,
    updated_at = $2
WHERE id = $1;

-- name: MarkArtifactError :exec
-- Retryable failure until attempts reach max_attempts, then dead-letter ('dead').
-- next_attempt_at schedules the backoff; the lease is cleared for re-claim.
UPDATE ingest.fetch_artifact
SET state = CASE WHEN attempts >= max_attempts THEN 'dead' ELSE 'error' END,
    last_error = $2,
    next_attempt_at = $3,
    lease_owner = NULL,
    lease_expires_at = NULL,
    updated_at = $4
WHERE id = $1;

-- name: ReResolveArtifactURL :exec
-- Refresh an expiring presigned/gateway URL in place (g7 tokens, 24h S3 links)
-- and re-arm the artifact for fetching without losing its identity or attempts.
UPDATE ingest.fetch_artifact
SET url = $2,
    url_expires_at = $3,
    gateway_url = $4,
    state = CASE WHEN state IN ('dead', 'done', 'skipped', 'superseded') THEN state ELSE 'pending' END,
    updated_at = $5
WHERE id = $1;

-- name: SupersedeEmptyTree :exec
-- An empty vbpl provision tree is often eventually-consistent. Mark the tree
-- artifact superseded (so it stops counting toward completeness) and schedule a
-- doc-level re-check; tree_recheck_count bounds the retries.
UPDATE ingest.fetch_artifact
SET state = 'superseded',
    lease_owner = NULL,
    lease_expires_at = NULL,
    updated_at = $2
WHERE fetch_doc_id = $1 AND kind = 'tree';

-- name: ScheduleTreeRecheck :exec
UPDATE ingest.fetch_doc
SET tree_recheck_after = $2,
    tree_recheck_count = tree_recheck_count + 1,
    updated_at = $3
WHERE id = $1;

-- name: ClearTreeRecheck :exec
UPDATE ingest.fetch_doc
SET tree_recheck_after = NULL,
    updated_at = $2
WHERE id = $1;

-- name: ScheduleSourceContentRecheck :exec
-- Re-open a completed document whose official body/file currently contains only
-- placeholder text. Body is due first so Fetch refreshes detail URLs before file
-- downloads are re-attempted. max_rechecks bounds eventually-missing source data.
WITH bumped AS (
    UPDATE ingest.fetch_doc d
    SET state = 'discovered',
        plan_ready = FALSE,
        artifacts_expected = 0,
        artifacts_done = 0,
        artifacts_failed = 0,
        content_recheck_after = @body_next_attempt_at,
        content_recheck_count = d.content_recheck_count + 1,
        content_recheck_reason = @reason,
        updated_at = @updated_at
    WHERE d.id = @fetch_doc_id
      AND d.content_recheck_count < @max_rechecks
      AND NOT (d.state = 'discovered' AND d.plan_ready = FALSE AND d.content_recheck_after IS NOT NULL)
    RETURNING d.id
)
UPDATE ingest.fetch_artifact a
SET state = 'pending',
    lease_owner = NULL,
    lease_expires_at = NULL,
    attempts = 0,
    next_attempt_at = CASE
        WHEN a.kind = 'body' THEN @body_next_attempt_at
        ELSE @file_next_attempt_at
    END,
    last_error = @reason,
    updated_at = @updated_at
FROM bumped
WHERE a.fetch_doc_id = bumped.id
  AND a.kind IN ('body', 'file');

-- name: RecomputeDocCounters :exec
-- Recompute the completeness counters from child rows — the single source of
-- truth. done = terminal-good (done/skipped/superseded); failed = dead.
UPDATE ingest.fetch_doc d
SET artifacts_done = c.done,
    artifacts_failed = c.failed,
    updated_at = $2
FROM (
    SELECT
        fetch_doc_id,
        COUNT(*) FILTER (WHERE NOT is_optional AND state IN ('done', 'skipped', 'superseded')) AS done,
        COUNT(*) FILTER (WHERE NOT is_optional AND state = 'dead') AS failed
    FROM ingest.fetch_artifact
    WHERE fetch_doc_id = $1
    GROUP BY fetch_doc_id
) c
WHERE d.id = $1 AND c.fetch_doc_id = d.id;

-- name: MarkDocCompleteIfDone :one
-- Compare-and-set completeness from child rows (never a lying flag): complete when
-- every non-optional artifact is terminal-good and none dead; partial when some
-- dead-lettered. Returns the resulting state so the caller can react.
UPDATE ingest.fetch_doc d
SET state = CASE
        WHEN agg.dead > 0 THEN 'partial'
        WHEN agg.total > 0 AND agg.pending = 0 THEN 'complete'
        ELSE d.state
    END,
    artifacts_done = agg.good,
    artifacts_failed = agg.dead,
    updated_at = @now
FROM (
    SELECT
        COUNT(*) FILTER (WHERE NOT is_optional) AS total,
        COUNT(*) FILTER (WHERE NOT is_optional AND state IN ('done', 'skipped', 'superseded')) AS good,
        COUNT(*) FILTER (WHERE NOT is_optional AND state = 'dead') AS dead,
        COUNT(*) FILTER (WHERE NOT is_optional AND state IN ('pending', 'claimed', 'error')) AS pending
    FROM ingest.fetch_artifact
    WHERE fetch_doc_id = @fetch_doc_id
) agg
WHERE d.id = @fetch_doc_id AND d.plan_ready
RETURNING d.state;

-- name: ListIncompleteDocs :many
-- Planned docs not yet complete — the work the completeness sweeper drives to done.
SELECT * FROM ingest.fetch_doc
WHERE plan_ready AND state NOT IN ('complete', 'error')
ORDER BY updated_at
LIMIT $1;

-- name: ListDocsNeedingTreeRecheck :many
-- Docs with a due empty-tree re-check (bounded by tree_recheck_count in app logic).
SELECT * FROM ingest.fetch_doc
WHERE tree_recheck_after IS NOT NULL AND tree_recheck_after <= $1
ORDER BY tree_recheck_after
LIMIT $2;

-- name: ListExpiringArtifacts :many
-- Live artifacts whose URL is about to expire — re-resolve before fetching.
SELECT * FROM ingest.fetch_artifact
WHERE state IN ('pending', 'error')
  AND url_expires_at IS NOT NULL
  AND url_expires_at <= $1
ORDER BY url_expires_at
LIMIT $2;

-- name: ListArtifactsByDoc :many
SELECT * FROM ingest.fetch_artifact
WHERE fetch_doc_id = $1
ORDER BY kind, ref_key;
