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
    discovered_files, discovered_at, updated_at
) VALUES (
    $1, $2, COALESCE(sqlc.narg(state), 'discovered'), $3, $4, $5, $6, $8, $7, $7
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
    discovered_files = COALESCE(EXCLUDED.discovered_files, ingest.fetch_doc.discovered_files),
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
-- Includes 'partial' (some artifacts dead-lettered) so docs with body-only
-- text still flow through extract/normalize.
SELECT * FROM ingest.fetch_doc
WHERE state IN ('complete', 'partial')
  AND in_scope
ORDER BY id
LIMIT sqlc.arg(row_limit);

-- name: ListCompleteFetchDocIDsAfter :many
-- Paged ExtractAll helper: enumerate only the next completed, in-scope
-- fetch_doc IDs so the workflow never loads or schedules the whole corpus at
-- once. Includes 'partial' so body-only docs are processed.
SELECT id FROM ingest.fetch_doc
WHERE state IN ('complete', 'partial')
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
-- dead-lettered AND no retryable artifacts remain. Returns the resulting state so
-- the caller can react. A doc in 'partial' still flows through extract/normalize
-- with whatever artifacts succeeded.
UPDATE ingest.fetch_doc d
SET state = CASE
        WHEN agg.dead > 0 AND agg.pending = 0 THEN 'partial'
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
-- 'partial' is terminal (all artifacts resolved, some dead-lettered), not incomplete.
SELECT * FROM ingest.fetch_doc
WHERE plan_ready AND state NOT IN ('complete', 'partial', 'error')
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

-- name: UpsertEmbeddingCache :exec
-- Content-addressed, so an existing row is already correct — DO NOTHING.
INSERT INTO ingest.embedding_cache (text_hash, model, dims, embedding)
VALUES ($1, $2, $3, $4)
ON CONFLICT (text_hash, model, dims) DO NOTHING;

-- name: LookupEmbeddingCache :many
-- Batch lookup for EmbedAll's cache pre-pass; callers chunk text_hash lists
-- to a few thousand per call.
SELECT text_hash, embedding FROM ingest.embedding_cache
WHERE model = $1 AND dims = $2 AND text_hash = ANY(@text_hashes::TEXT[]);

-- name: CreateCorpusRun :one
INSERT INTO ingest.corpus_run (
    run_uuid, recipe_digest, input_manifest_ref, input_manifest_digest,
    discovery_cutoff, relation_backfill_cutoff, status, created_at
) VALUES (
    @run_uuid, @recipe_digest, @input_manifest_ref, @input_manifest_digest,
    @discovery_cutoff, @relation_backfill_cutoff, 'pending', @created_at
)
ON CONFLICT (run_uuid) DO NOTHING
RETURNING *;

-- name: CorpusRunByKey :one
SELECT * FROM ingest.corpus_run WHERE run_uuid = @run_uuid;

-- name: SetCorpusRunStatus :one
UPDATE ingest.corpus_run
SET status = @status,
    started_at = COALESCE(started_at, @started_at),
    completed_at = @completed_at,
    succeeded_count = @succeeded_count,
    pending_retry_count = @pending_retry_count,
    terminal_gap_count = @terminal_gap_count,
    accepted_gap_count = @accepted_gap_count,
    gap_acceptance_ref = @gap_acceptance_ref,
    rejected_input_count = @rejected_input_count,
    failure_count = @failure_count
WHERE id = @id
  AND (
      (status = 'pending' AND @status IN ('running', 'cancelling', 'cancelled', 'failed'))
      OR (status = 'running' AND @status IN ('cancelling', 'complete', 'failed'))
      OR (status = 'cancelling' AND @status IN ('cancelled', 'failed'))
  )
RETURNING *;

-- name: RequestCorpusRunCancellation :one
UPDATE ingest.corpus_run
SET status = CASE WHEN status IN ('pending', 'running') THEN 'cancelling' ELSE status END,
    cancellation_requested_at = COALESCE(cancellation_requested_at, @requested_at),
    cancellation_reason = @reason
WHERE id = @id AND status IN ('pending', 'running', 'cancelling')
RETURNING *;

-- name: CreateCorpusBuild :one
INSERT INTO ingest.corpus_build (
    run_id, build_uuid, discovery_cutoff, relation_backfill_cutoff,
    source_manifest_ref, source_manifest_digest, max_manifest_items,
    max_manifest_bytes, config_digest, parser_recipe_digest,
    policy_recipe_digest, citation_recipe_digest, chunking_recipe_digest,
    state, created_at
) VALUES (
    @run_id, @build_uuid, @discovery_cutoff, @relation_backfill_cutoff,
    @source_manifest_ref, @source_manifest_digest, @max_manifest_items,
    @max_manifest_bytes, @config_digest, @parser_recipe_digest,
    @policy_recipe_digest, @citation_recipe_digest, @chunking_recipe_digest,
    'open', @created_at
)
ON CONFLICT (build_uuid) DO NOTHING
RETURNING *;

-- name: CorpusBuildByKey :one
SELECT * FROM ingest.corpus_build WHERE build_uuid = @build_uuid;

-- name: SetCorpusBuildState :one
UPDATE ingest.corpus_build
SET state = @state, completed_at = @completed_at
WHERE id = @id
  AND (
      (state = 'open' AND @state IN ('running', 'failed', 'cancelled'))
      OR (state = 'running' AND @state IN ('ready', 'failed', 'cancelled'))
  )
RETURNING *;

-- name: AcquireSchedulerLease :one
INSERT INTO ingest.scheduler_lease (
    build_id, owner, fence_token, acquired_at, renewed_at, expires_at, released_at
) VALUES (
    @build_id, @owner, 1, clock_timestamp(), clock_timestamp(), @expires_at, NULL
)
ON CONFLICT (build_id) DO UPDATE SET
    owner = EXCLUDED.owner,
    fence_token = ingest.scheduler_lease.fence_token + 1,
    acquired_at = EXCLUDED.acquired_at,
    renewed_at = EXCLUDED.renewed_at,
    expires_at = EXCLUDED.expires_at,
    released_at = NULL
WHERE ingest.scheduler_lease.released_at IS NOT NULL
   OR ingest.scheduler_lease.expires_at <= clock_timestamp()
RETURNING *;

-- name: RenewSchedulerLease :one
UPDATE ingest.scheduler_lease
SET renewed_at = clock_timestamp(), expires_at = @expires_at
WHERE build_id = @build_id
  AND owner = @owner
  AND fence_token = @fence_token
  AND released_at IS NULL
  AND expires_at > clock_timestamp()
RETURNING *;

-- name: ReleaseSchedulerLease :one
UPDATE ingest.scheduler_lease
SET released_at = clock_timestamp()
WHERE build_id = @build_id
  AND owner = @owner
  AND fence_token = @fence_token
  AND released_at IS NULL
  AND expires_at > clock_timestamp()
RETURNING *;

-- name: ValidateSchedulerFence :one
SELECT EXISTS (
    SELECT 1
    FROM ingest.scheduler_lease lease
    JOIN ingest.corpus_build build ON build.id = lease.build_id
    JOIN ingest.corpus_run run ON run.id = build.run_id
    WHERE build.build_uuid = @build_uuid
      AND run.run_uuid = @run_uuid
      AND lease.owner = @owner
      AND lease.fence_token = @fence_token
      AND lease.released_at IS NULL
      AND lease.expires_at > clock_timestamp()
) AS valid;

-- name: LockSchedulerFence :one
-- Call inside the unit-of-work transaction before inserting any cross-schema
-- outputs. The row lock prevents lease takeover until outputs and stage_result
-- commit together. The UUID joins prove the run/build association carried by the
-- shard. A missing row is a lost lease and accepts no writes.
SELECT lease.build_id, build.run_id, lease.fence_token
FROM ingest.scheduler_lease lease
JOIN ingest.corpus_build build ON build.id = lease.build_id
JOIN ingest.corpus_run run ON run.id = build.run_id
WHERE build.build_uuid = @build_uuid
  AND run.run_uuid = @run_uuid
  AND lease.owner = @owner
  AND lease.fence_token = @fence_token
  AND lease.released_at IS NULL
  AND lease.expires_at > clock_timestamp()
FOR UPDATE OF lease;

-- name: InsertStageResult :one
INSERT INTO ingest.stage_result (
    build_id, stage, shard_key, idempotency_key, input_manifest_ref,
    input_manifest_digest, output_manifest_ref, output_manifest_digest,
    recipe_digest, result_digest, writer_owner, fence_token, next_cursor, next_eligible_at,
    expected_count, processed_count,
    succeeded_count, pending_retry_count, terminal_gap_count, accepted_gap_count,
    rejected_input_count, failure_count, gap_acceptance_ref, complete,
    completion_state, created_at
)
SELECT
    @build_id, @stage, @shard_key, @idempotency_key, @input_manifest_ref,
    @input_manifest_digest, @output_manifest_ref, @output_manifest_digest,
    @recipe_digest, @result_digest, @writer_owner, @fence_token, @next_cursor, @next_eligible_at,
    @expected_count, @processed_count,
    @succeeded_count, @pending_retry_count, @terminal_gap_count, @accepted_gap_count,
    @rejected_input_count, @failure_count, @gap_acceptance_ref, @complete,
    @completion_state, @created_at
FROM (
    SELECT * FROM ingest.scheduler_lease
    WHERE build_id = @build_id
      AND owner = @writer_owner
      AND fence_token = @fence_token
      AND released_at IS NULL
      AND expires_at > clock_timestamp()
    FOR UPDATE
) lease
WHERE lease.build_id = @build_id
ON CONFLICT (build_id, stage, idempotency_key) DO NOTHING
RETURNING *;

-- name: StageResultByIdempotencyKey :one
SELECT * FROM ingest.stage_result
WHERE build_id = @build_id AND stage = @stage AND idempotency_key = @idempotency_key;

-- name: StageResultByIdempotencyKeyForUpdate :one
SELECT * FROM ingest.stage_result
WHERE build_id = @build_id AND stage = @stage AND idempotency_key = @idempotency_key
FOR UPDATE;

-- name: InsertStageResultOutput :one
INSERT INTO ingest.stage_result_output (
    stage_result_id, ordinal, object_ref, digest, size_bytes, media_type
) VALUES (
    @stage_result_id, @ordinal, @object_ref, @digest, @size_bytes, @media_type
)
ON CONFLICT (stage_result_id, ordinal) DO NOTHING
RETURNING *;

-- name: ListStageResultOutputs :many
SELECT * FROM ingest.stage_result_output
WHERE stage_result_id = @stage_result_id
ORDER BY ordinal;

-- name: ListStageResults :many
SELECT * FROM ingest.stage_result
WHERE build_id = @build_id AND stage = @stage
ORDER BY id;

-- name: CreateExternalJob :one
INSERT INTO ingest.external_job (
    build_id, stage, provider, submission_key, request_manifest_ref,
    request_manifest_digest, state, writer_owner, fence_token,
    next_eligible_at, created_at, updated_at
)
SELECT
    @build_id, @stage, @provider, @submission_key, @request_manifest_ref,
    @request_manifest_digest, 'prepared', @writer_owner, @fence_token,
    @next_eligible_at, clock_timestamp(), clock_timestamp()
FROM (
    SELECT * FROM ingest.scheduler_lease
    WHERE build_id = @build_id
      AND owner = @writer_owner
      AND fence_token = @fence_token
      AND released_at IS NULL
      AND expires_at > clock_timestamp()
    FOR UPDATE
) lease
WHERE lease.build_id = @build_id
ON CONFLICT (build_id, stage, provider, submission_key) DO NOTHING
RETURNING *;

-- name: ExternalJobBySubmissionKey :one
SELECT * FROM ingest.external_job
WHERE build_id = @build_id
  AND stage = @stage
  AND provider = @provider
  AND submission_key = @submission_key;

-- name: SetExternalJobState :one
UPDATE ingest.external_job job
SET provider_job_id = COALESCE(job.provider_job_id, @provider_job_id),
    result_manifest_ref = COALESCE(job.result_manifest_ref, @result_manifest_ref),
    result_manifest_digest = COALESCE(job.result_manifest_digest, @result_manifest_digest),
    state = @state,
    writer_owner = @writer_owner,
    fence_token = @fence_token,
    next_eligible_at = @next_eligible_at,
    cancellation_requested_at = @cancellation_requested_at,
    last_error = @last_error,
    updated_at = clock_timestamp()
FROM (
    SELECT * FROM ingest.scheduler_lease
    WHERE build_id = @build_id
      AND owner = @writer_owner
      AND fence_token = @fence_token
      AND released_at IS NULL
      AND expires_at > clock_timestamp()
    FOR UPDATE
) lease
WHERE job.id = @id
  AND job.build_id = @build_id
  AND lease.build_id = job.build_id
  AND job.state <> 'uncertain_submission'
  AND (
      @state NOT IN ('submitted', 'running', 'succeeded')
      OR (COALESCE(job.provider_job_id, @provider_job_id) IS NOT NULL
          AND COALESCE(job.provider_job_id, @provider_job_id) <> '')
  )
  AND (
      @state <> 'succeeded'
      OR (COALESCE(job.result_manifest_ref, @result_manifest_ref) IS NOT NULL
          AND COALESCE(job.result_manifest_ref, @result_manifest_ref) <> ''
          AND COALESCE(job.result_manifest_digest, @result_manifest_digest) IS NOT NULL
          AND COALESCE(job.result_manifest_digest, @result_manifest_digest) <> '')
  )
  AND (
      (job.state = 'prepared' AND @state IN (
          'submitted', 'uncertain_submission', 'cancel_requested', 'cancelled'
      ))
      OR (job.state = 'submitted' AND @state IN (
          'running', 'succeeded', 'failed', 'uncertain_submission',
          'cancel_requested', 'cancelled'
      ))
      OR (job.state = 'running' AND @state IN (
          'succeeded', 'failed', 'cancel_requested', 'cancelled'
      ))
      OR (job.state = 'cancel_requested' AND @state IN ('cancelled', 'failed'))
      OR job.state = @state
  )
RETURNING job.*;

-- name: ReconcileUncertainExternalJob :one
-- An uncertain submission can leave that state only through provider
-- reconciliation with a durable provider id while the current fence is locked.
UPDATE ingest.external_job job
SET provider_job_id = COALESCE(job.provider_job_id, @provider_job_id),
    result_manifest_ref = COALESCE(job.result_manifest_ref, @result_manifest_ref),
    result_manifest_digest = COALESCE(job.result_manifest_digest, @result_manifest_digest),
    state = @state,
    writer_owner = @writer_owner,
    fence_token = @fence_token,
    next_eligible_at = @next_eligible_at,
    last_error = @last_error,
    updated_at = clock_timestamp()
FROM (
    SELECT * FROM ingest.scheduler_lease
    WHERE build_id = @build_id
      AND owner = @writer_owner
      AND fence_token = @fence_token
      AND released_at IS NULL
      AND expires_at > clock_timestamp()
    FOR UPDATE
) lease
WHERE job.id = @id
  AND job.build_id = @build_id
  AND lease.build_id = job.build_id
  AND job.state = 'uncertain_submission'
  AND @provider_job_id IS NOT NULL
  AND @provider_job_id <> ''
  AND (
      @state <> 'succeeded'
      OR (@result_manifest_ref IS NOT NULL AND @result_manifest_ref <> ''
          AND @result_manifest_digest IS NOT NULL AND @result_manifest_digest <> '')
  )
  AND @state IN ('submitted', 'running', 'succeeded', 'failed', 'cancelled')
RETURNING job.*;

-- name: InsertContentBlob :one
INSERT INTO ingest.content_blob (
    sha256, byte_size, media_type, object_ref, capture_source,
    capture_ref, captured_at, created_at
) VALUES (
    @sha256, @byte_size, @media_type, @object_ref, @capture_source,
    @capture_ref, @captured_at, @created_at
)
ON CONFLICT (sha256) DO NOTHING
RETURNING *;

-- name: ContentBlobBySHA256 :one
SELECT * FROM ingest.content_blob WHERE sha256 = @sha256;

-- name: AddBlobRetentionReference :one
INSERT INTO ingest.blob_retention_reference (
    blob_id, reference_kind, reference_key, expires_at, created_at
) VALUES (
    @blob_id, @reference_kind, @reference_key, @expires_at, @created_at
)
ON CONFLICT (blob_id, reference_kind, reference_key) DO UPDATE SET
    expires_at = EXCLUDED.expires_at
RETURNING *;

-- name: RemoveBlobRetentionReference :execrows
DELETE FROM ingest.blob_retention_reference
WHERE blob_id = @blob_id
  AND reference_kind = @reference_kind
  AND reference_key = @reference_key;

-- name: ListBlobRetentionReferences :many
SELECT * FROM ingest.blob_retention_reference
WHERE blob_id = @blob_id
ORDER BY reference_kind, reference_key;

-- name: CreateCorpusRelease :one
INSERT INTO ingest.corpus_release (
    build_id, release_key, snapshot_ref, state, created_at
) VALUES (
    @build_id, @release_key, @snapshot_ref, 'candidate', @created_at
)
ON CONFLICT (release_key) DO NOTHING
RETURNING *;

-- name: CorpusReleaseByKey :one
SELECT * FROM ingest.corpus_release WHERE release_key = @release_key;

-- name: SetCorpusReleaseReadiness :one
UPDATE ingest.corpus_release
SET state = @state,
    validated_at = @validated_at,
    ready_at = @ready_at,
    validation_error = @validation_error
WHERE id = @id
  AND (
      (state = 'candidate' AND @state IN ('validated', 'failed'))
      OR (state = 'validated' AND @state IN ('ready', 'failed'))
  )
RETURNING *;

-- name: InsertReleaseManifest :one
INSERT INTO ingest.release_manifest (
    release_id, corpus_hash, blob_set_digest, source_manifest_digest,
    schema_migration_digest,
    citation_recipe_digest, chunking_recipe_digest, embedding_model_digest,
    embedding_tokenizer_digest, embedding_preprocessing_digest,
    embedding_dimensions, lexical_revision, lexical_revision_digest,
    lexical_recipe_digest, lexical_tokenizer_digest,
    lexical_preprocessing_digest, lexical_dimensions, taxonomy_release_id,
    taxonomy_content_digest, config_digest,
    evaluation_artifact_ref, evaluation_artifact_digest,
    compatible_server_image_digest, compatible_embedder_image_digest,
    source_observation_count, document_revision_count, provision_revision_count,
    chunk_count, citation_count, provenance_count, dense_vector_count,
    lexical_chunk_count, gap_count, accepted_gap_count, gap_acceptance_ref, created_at
) VALUES (
    @release_id, @corpus_hash, @blob_set_digest, @source_manifest_digest,
    @schema_migration_digest,
    @citation_recipe_digest, @chunking_recipe_digest, @embedding_model_digest,
    @embedding_tokenizer_digest, @embedding_preprocessing_digest,
    @embedding_dimensions, @lexical_revision, @lexical_revision_digest,
    @lexical_recipe_digest, @lexical_tokenizer_digest,
    @lexical_preprocessing_digest, @lexical_dimensions, @taxonomy_release_id,
    @taxonomy_content_digest, @config_digest,
    @evaluation_artifact_ref, @evaluation_artifact_digest,
    @compatible_server_image_digest, @compatible_embedder_image_digest,
    @source_observation_count, @document_revision_count, @provision_revision_count,
    @chunk_count, @citation_count, @provenance_count, @dense_vector_count,
    @lexical_chunk_count, @gap_count, @accepted_gap_count, @gap_acceptance_ref, @created_at
)
ON CONFLICT (release_id) DO NOTHING
RETURNING *;

-- name: ReleaseManifestByRelease :one
SELECT * FROM ingest.release_manifest WHERE release_id = @release_id;

-- name: InitializeReleaseActivation :one
INSERT INTO ingest.release_activation (selection_key, selection_version)
VALUES (@selection_key, 0)
ON CONFLICT (selection_key) DO NOTHING
RETURNING *;

-- name: ActivateCorpusRelease :one
-- The adapter must first call LockCorpusReleaseForActivation in the same
-- transaction so a concurrent retention claim cannot appear after validation.
WITH candidate AS (
    SELECT release.id
    FROM ingest.corpus_release release
    WHERE release.id = @release_id
      AND release.state IN ('ready', 'active', 'retired')
      AND release.validated_at IS NOT NULL
      AND release.ready_at IS NOT NULL
      AND EXISTS (
          SELECT 1 FROM ingest.release_manifest manifest
          WHERE manifest.release_id = release.id
      )
      AND NOT EXISTS (
          SELECT 1 FROM ingest.release_retention_claim claim
          WHERE claim.release_id = release.id
            AND (claim.finalized_at IS NOT NULL
                 OR claim.released_at IS NULL)
      )
    FOR UPDATE OF release
), switched AS (
    UPDATE ingest.release_activation activation
    SET previous_release_id = activation.current_release_id,
        current_release_id = candidate.id,
        selection_version = activation.selection_version + 1,
        activated_at = @activated_at,
        activated_by = @activated_by
    FROM candidate
    WHERE activation.selection_key = @selection_key
      AND activation.selection_version = @expected_selection_version
      AND activation.current_release_id IS DISTINCT FROM candidate.id
    RETURNING activation.*
), retire_previous AS (
    UPDATE ingest.corpus_release release
    SET state = 'retired'
    FROM switched
    WHERE release.id = switched.previous_release_id
      AND release.id IS DISTINCT FROM switched.current_release_id
), mark_current AS (
    UPDATE ingest.corpus_release release
    SET state = 'active'
    FROM switched
    WHERE release.id = switched.current_release_id
), clear_old_active_hold AS (
    DELETE FROM ingest.release_retention_hold hold
    USING switched
    WHERE hold.hold_kind = 'active'
      AND hold.release_id IS DISTINCT FROM switched.current_release_id
), clear_old_rollback_hold AS (
    DELETE FROM ingest.release_retention_hold hold
    USING switched
    WHERE hold.hold_kind = 'rollback'
      AND hold.release_id IS DISTINCT FROM switched.previous_release_id
), add_active_hold AS (
    INSERT INTO ingest.release_retention_hold (
        release_id, hold_kind, hold_key, expires_at, created_at
    )
    SELECT current_release_id, 'active', selection_key, NULL, @activated_at
    FROM switched
    ON CONFLICT (release_id, hold_kind, hold_key) DO UPDATE SET expires_at = NULL
), add_rollback_hold AS (
    INSERT INTO ingest.release_retention_hold (
        release_id, hold_kind, hold_key, expires_at, created_at
    )
    SELECT previous_release_id, 'rollback', selection_key, NULL, @activated_at
    FROM switched
    WHERE previous_release_id IS NOT NULL
    ON CONFLICT (release_id, hold_kind, hold_key) DO UPDATE SET expires_at = NULL
)
SELECT * FROM switched;

-- name: LockCorpusReleaseForActivation :one
-- Call before ActivateCorpusRelease and keep the transaction open. Retention
-- claiming and pin acquisition use the same corpus_release row lock.
SELECT id FROM ingest.corpus_release
WHERE id = @release_id
FOR UPDATE;

-- name: ActiveCorpusRelease :one
SELECT activation.selection_version, activation.activated_at,
       release.*, manifest.*
FROM ingest.release_activation activation
JOIN ingest.corpus_release release ON release.id = activation.current_release_id
JOIN ingest.release_manifest manifest ON manifest.release_id = release.id
WHERE activation.selection_key = @selection_key
  AND release.state = 'active'
  AND release.validated_at IS NOT NULL
  AND release.ready_at IS NOT NULL;

-- name: LockCorpusReleaseForPin :one
-- PinStore.Acquire must call this first and keep the transaction open through
-- AddReleaseRetentionHold. ClaimEligible uses the same release row lock.
SELECT id FROM ingest.corpus_release
WHERE id = @release_id
FOR UPDATE;

-- name: AddReleaseRetentionHold :one
INSERT INTO ingest.release_retention_hold (
    release_id, hold_kind, hold_key, expires_at, created_at
) SELECT
    @release_id, @hold_kind, @hold_key, @expires_at, @created_at
WHERE @hold_kind IN ('pinned', 'in_flight', 'backup')
  AND (@hold_kind = 'backup' OR @expires_at > clock_timestamp())
  AND NOT EXISTS (
      SELECT 1 FROM ingest.release_retention_claim claim
      WHERE claim.release_id = @release_id
        AND (claim.finalized_at IS NOT NULL
             OR claim.released_at IS NULL)
  )
ON CONFLICT (release_id, hold_kind, hold_key) DO UPDATE SET
    expires_at = EXCLUDED.expires_at
RETURNING *;

-- name: RemoveReleaseRetentionHold :execrows
DELETE FROM ingest.release_retention_hold
WHERE release_id = @release_id
  AND hold_kind = @hold_kind
  AND hold_key = @hold_key
  AND hold_kind IN ('pinned', 'in_flight', 'backup');

-- name: ListReleaseRetentionHolds :many
SELECT * FROM ingest.release_retention_hold
WHERE release_id = @release_id
  AND (expires_at IS NULL OR expires_at > clock_timestamp())
ORDER BY hold_kind, hold_key;

-- name: ListReleasesEligibleForRetention :many
SELECT release.* FROM ingest.corpus_release release
WHERE release.state IN ('retired', 'failed')
  AND NOT EXISTS (
      SELECT 1 FROM ingest.release_activation activation
      WHERE activation.current_release_id = release.id
         OR activation.previous_release_id = release.id
  )
  AND NOT EXISTS (
      SELECT 1 FROM ingest.release_retention_hold hold
      WHERE hold.release_id = release.id
        AND (hold.expires_at IS NULL OR hold.expires_at > clock_timestamp())
  )
  AND NOT EXISTS (
      SELECT 1 FROM ingest.release_retention_claim claim
      WHERE claim.release_id = release.id
        AND (claim.finalized_at IS NOT NULL
             OR claim.released_at IS NULL)
  )
ORDER BY release.id
LIMIT @row_limit;

-- name: LockEligibleReleasesForRetention :many
-- ClaimEligible calls this and ClaimReleaseForRetention in one transaction.
-- Pin acquisition locks the same corpus_release row before adding a hold.
SELECT release.*
FROM ingest.corpus_release release
LEFT JOIN ingest.release_retention_claim claim ON claim.release_id = release.id
WHERE release.state IN ('retired', 'failed')
  AND NOT EXISTS (
      SELECT 1 FROM ingest.release_activation activation
      WHERE activation.current_release_id = release.id
         OR activation.previous_release_id = release.id
  )
  AND NOT EXISTS (
      SELECT 1 FROM ingest.release_retention_hold hold
      WHERE hold.release_id = release.id
        AND (hold.expires_at IS NULL OR hold.expires_at > clock_timestamp())
  )
  AND (
      claim.id IS NULL
      OR (claim.finalized_at IS NULL AND (
          (claim.owner = @owner AND claim.batch_key = @batch_key
              AND claim.released_at IS NULL AND claim.expires_at > clock_timestamp())
          OR claim.released_at IS NOT NULL
          OR claim.expires_at <= clock_timestamp()
      ))
  )
ORDER BY release.id
LIMIT @row_limit
FOR UPDATE OF release SKIP LOCKED;

-- name: ClaimReleaseForRetention :one
-- The caller holds the corpus_release row lock from
-- LockEligibleReleasesForRetention in the same transaction.
INSERT INTO ingest.release_retention_claim (
    release_id, claim_key, owner, batch_key, fence_token,
    claimed_at, renewed_at, expires_at, released_at, finalized_at
)
SELECT @release_id, @owner || ':' || @batch_key || ':' || @release_id::TEXT,
       @owner, @batch_key, 1, clock_timestamp(), clock_timestamp(),
       clock_timestamp() + @ttl_milliseconds::BIGINT * interval '1 millisecond',
       NULL, NULL
WHERE EXISTS (
    SELECT 1 FROM ingest.corpus_release release
    WHERE release.id = @release_id AND release.state IN ('retired', 'failed')
)
AND @ttl_milliseconds::BIGINT > 0
AND NOT EXISTS (
    SELECT 1 FROM ingest.release_activation activation
    WHERE activation.current_release_id = @release_id
       OR activation.previous_release_id = @release_id
)
AND NOT EXISTS (
    SELECT 1 FROM ingest.release_retention_hold hold
    WHERE hold.release_id = @release_id
      AND (hold.expires_at IS NULL OR hold.expires_at > clock_timestamp())
)
ON CONFLICT (release_id) DO UPDATE SET
    claim_key = CASE
        WHEN ingest.release_retention_claim.owner = @owner
         AND ingest.release_retention_claim.batch_key = @batch_key
         AND ingest.release_retention_claim.released_at IS NULL
         AND ingest.release_retention_claim.expires_at > clock_timestamp()
            THEN ingest.release_retention_claim.claim_key
        ELSE @owner || ':' || @batch_key || ':' || EXCLUDED.release_id::TEXT
    END,
    owner = @owner,
    batch_key = @batch_key,
    fence_token = CASE
        WHEN ingest.release_retention_claim.owner = @owner
         AND ingest.release_retention_claim.batch_key = @batch_key
         AND ingest.release_retention_claim.released_at IS NULL
         AND ingest.release_retention_claim.expires_at > clock_timestamp()
            THEN ingest.release_retention_claim.fence_token
        ELSE ingest.release_retention_claim.fence_token + 1
    END,
    claimed_at = CASE
        WHEN ingest.release_retention_claim.owner = @owner
         AND ingest.release_retention_claim.batch_key = @batch_key
         AND ingest.release_retention_claim.released_at IS NULL
         AND ingest.release_retention_claim.expires_at > clock_timestamp()
            THEN ingest.release_retention_claim.claimed_at
        ELSE clock_timestamp()
    END,
    renewed_at = CASE
        WHEN ingest.release_retention_claim.owner = @owner
         AND ingest.release_retention_claim.batch_key = @batch_key
         AND ingest.release_retention_claim.released_at IS NULL
         AND ingest.release_retention_claim.expires_at > clock_timestamp()
            THEN ingest.release_retention_claim.renewed_at
        ELSE clock_timestamp()
    END,
    expires_at = CASE
        WHEN ingest.release_retention_claim.owner = @owner
         AND ingest.release_retention_claim.batch_key = @batch_key
         AND ingest.release_retention_claim.released_at IS NULL
         AND ingest.release_retention_claim.expires_at > clock_timestamp()
            THEN ingest.release_retention_claim.expires_at
        ELSE clock_timestamp() + @ttl_milliseconds::BIGINT * interval '1 millisecond'
    END,
    released_at = NULL,
    finalized_at = NULL
WHERE ingest.release_retention_claim.finalized_at IS NULL
  AND (
      (ingest.release_retention_claim.owner = @owner
          AND ingest.release_retention_claim.batch_key = @batch_key
          AND ingest.release_retention_claim.released_at IS NULL
          AND ingest.release_retention_claim.expires_at > clock_timestamp())
      OR ingest.release_retention_claim.released_at IS NOT NULL
      OR ingest.release_retention_claim.expires_at <= clock_timestamp()
  )
RETURNING *;

-- name: RenewReleaseRetentionClaim :one
UPDATE ingest.release_retention_claim
SET renewed_at = clock_timestamp(),
    expires_at = clock_timestamp() + @ttl_milliseconds::BIGINT * interval '1 millisecond'
WHERE release_id = @release_id
  AND @ttl_milliseconds::BIGINT > 0
  AND claim_key = @claim_key
  AND owner = @owner
  AND fence_token = @fence_token
  AND released_at IS NULL
  AND finalized_at IS NULL
  AND expires_at > clock_timestamp()
RETURNING *;

-- name: ReleaseReleaseRetentionClaim :one
UPDATE ingest.release_retention_claim
SET released_at = clock_timestamp()
WHERE release_id = @release_id
  AND claim_key = @claim_key
  AND owner = @owner
  AND fence_token = @fence_token
  AND released_at IS NULL
  AND finalized_at IS NULL
  AND expires_at > clock_timestamp()
RETURNING *;

-- name: FinalizeReleaseRetentionClaim :one
-- The caller first locks the release with LockCorpusReleaseForRetentionFinalize
-- in the same transaction, then this query rechecks every retention blocker.
UPDATE ingest.release_retention_claim
SET finalized_at = clock_timestamp()
WHERE release_id = @release_id
  AND claim_key = @claim_key
  AND owner = @owner
  AND fence_token = @fence_token
  AND released_at IS NULL
  AND finalized_at IS NULL
  AND expires_at > clock_timestamp()
  AND NOT EXISTS (
      SELECT 1 FROM ingest.release_activation activation
      WHERE activation.current_release_id = @release_id
         OR activation.previous_release_id = @release_id
  )
  AND NOT EXISTS (
      SELECT 1 FROM ingest.release_retention_hold hold
      WHERE hold.release_id = @release_id
        AND (hold.expires_at IS NULL OR hold.expires_at > clock_timestamp())
  )
RETURNING *;

-- name: LockCorpusReleaseForRetentionFinalize :one
SELECT id FROM ingest.corpus_release
WHERE id = @release_id
FOR UPDATE;
