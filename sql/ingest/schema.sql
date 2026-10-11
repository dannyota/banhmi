CREATE EXTENSION IF NOT EXISTS vector;

CREATE SCHEMA IF NOT EXISTS ingest;

-- ingest is the discovery/queue ledger — the completeness engine. Discovery is
-- keyword-filtered per source; one document fans out into many fetchable
-- artifacts and nothing may be silently missed. A document is complete iff
-- artifacts_done == artifacts_expected, recomputed from fetch_artifact child
-- rows (never a lying boolean). Pipeline state lives here, never in bronze.

-- ingest.discover_cursor is the per-(source, keyword) incremental-discovery
-- watermark. Discovery is keyword-filtered, so the watermark is per keyword: a
-- slow keyword can never be skipped by a fast one. expected_total snapshots
-- data.total for the keyword slice to prove the whole slice was enqueued;
-- last_seen_total tracks drift between runs.
CREATE TABLE ingest.discover_cursor (
    id               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source           TEXT NOT NULL,
    keyword          TEXT NOT NULL,
    watermark        TEXT NOT NULL DEFAULT '',
    expected_total   BIGINT NOT NULL DEFAULT 0,
    last_seen_total  BIGINT NOT NULL DEFAULT 0,
    last_run_at      TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL,
    updated_at       TIMESTAMPTZ NOT NULL,
    CONSTRAINT uq_ingest_discover_cursor UNIQUE (source, keyword)
);

-- ingest.fetch_doc is the parent ledger row: one per document. state moves
-- discovered -> planning -> fetching -> partial|complete|error. plan_ready
-- marks that all artifacts have been enumerated, so completeness can be judged.
-- artifacts_expected/done/failed are the recomputed completeness counters.
-- in_scope distinguishes keyword hits from relation targets pulled in by the
-- graph. tree_recheck_after/count drive the empty-vbpl-tree re-check.
-- content_recheck_after/count/reason drive re-fetches when an official source
-- serves placeholder text such as "Đang cập nhật file đính kèm".
-- content_hash gates re-discovery: a doc re-opens only on a real change.
CREATE TABLE ingest.fetch_doc (
    id                   BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source               TEXT NOT NULL,
    external_id          TEXT NOT NULL,
    state                TEXT NOT NULL DEFAULT 'discovered',
    plan_ready           BOOLEAN NOT NULL DEFAULT FALSE,
    in_scope             BOOLEAN NOT NULL DEFAULT TRUE,
    provenance           TEXT NOT NULL DEFAULT '',
    artifacts_expected   INTEGER NOT NULL DEFAULT 0,
    artifacts_done       INTEGER NOT NULL DEFAULT 0,
    artifacts_failed     INTEGER NOT NULL DEFAULT 0,
    tree_recheck_after   TIMESTAMPTZ,
    tree_recheck_count   INTEGER NOT NULL DEFAULT 0,
    content_recheck_after TIMESTAMPTZ,
    content_recheck_count INTEGER NOT NULL DEFAULT 0,
    content_recheck_reason TEXT NOT NULL DEFAULT '',
    content_hash         TEXT,
    detail_url           TEXT,
    -- discovered_files holds the file references the source scraped at discovery
    -- time (authoritative download URLs from the listing/detail markup), as a
    -- JSONB array of ingest.FileRef. Fetch replays them into DetailRef so a source
    -- never has to re-derive or synthesize a download URL it already saw. Opaque
    -- to SQL by design — never queried, only round-tripped.
    discovered_files     JSONB,
    discovered_at        TIMESTAMPTZ NOT NULL,
    updated_at           TIMESTAMPTZ NOT NULL,
    CONSTRAINT uq_ingest_fetch_doc UNIQUE (source, external_id),
    CONSTRAINT chk_ingest_fetch_doc_state CHECK (
        state IN ('discovered', 'planning', 'fetching', 'partial', 'complete', 'error')
    )
);

CREATE INDEX idx_ingest_fetch_doc_state ON ingest.fetch_doc (state, updated_at);
CREATE INDEX idx_ingest_fetch_doc_recheck ON ingest.fetch_doc (tree_recheck_after);
CREATE INDEX idx_ingest_fetch_doc_content_recheck ON ingest.fetch_doc (content_recheck_after);
CREATE INDEX idx_ingest_fetch_doc_incomplete ON ingest.fetch_doc (plan_ready, state);

-- ingest.doc_discovery is the append-only provenance ledger: every (doc, keyword)
-- hit AND every relation edge that surfaced a doc. It dedups the document
-- (fetch_doc is one row) while never losing WHY a doc is in scope. Nullable key
-- parts use '' sentinels so the provenance tuple can carry a UNIQUE constraint.
CREATE TABLE ingest.doc_discovery (
    id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    fetch_doc_id      BIGINT NOT NULL,
    via               TEXT NOT NULL DEFAULT '',
    keyword           TEXT NOT NULL DEFAULT '',
    src_fetch_doc_id  BIGINT NOT NULL DEFAULT 0,
    relation_type     TEXT NOT NULL DEFAULT '',
    discovered_at     TIMESTAMPTZ NOT NULL,
    CONSTRAINT fk_ingest_doc_discovery_doc FOREIGN KEY (fetch_doc_id)
        REFERENCES ingest.fetch_doc (id) ON DELETE CASCADE,
    CONSTRAINT uq_ingest_doc_discovery UNIQUE (fetch_doc_id, via, keyword, src_fetch_doc_id, relation_type)
);

CREATE INDEX idx_ingest_doc_discovery_doc ON ingest.doc_discovery (fetch_doc_id);
CREATE INDEX idx_ingest_doc_discovery_keyword ON ingest.doc_discovery (keyword);

-- ingest.fetch_artifact is the child work queue: one row per fetchable unit of a
-- document. kind ∈ body/tree/file/relation/appendix; ref_key discriminates
-- siblings (2 PDFs, 2 edges). file_name keeps the source-provided filename even
-- when local storage uses a hash path. url+url_expires_at+gateway_url support re-resolving
-- expiring g7 tokens / 24h presigned URLs. target_source/target_ext_id let a
-- relation artifact enqueue its target doc. lease_owner/lease_expires_at make
-- FOR UPDATE SKIP LOCKED claims crash-safe; attempts/max_attempts/next_attempt_at
-- drive backoff and the dead-letter -> doc partial path. is_optional marks
-- artifacts whose absence does not block completeness.
CREATE TABLE ingest.fetch_artifact (
    id               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    fetch_doc_id     BIGINT NOT NULL,
    kind             TEXT NOT NULL,
    ref_key          TEXT NOT NULL DEFAULT '',
    file_kind        TEXT NOT NULL DEFAULT '',
    file_name        TEXT NOT NULL DEFAULT '',
    url              TEXT,
    url_expires_at   TIMESTAMPTZ,
    gateway_url      TEXT,
    target_source    TEXT NOT NULL DEFAULT '',
    target_ext_id    TEXT NOT NULL DEFAULT '',
    is_optional      BOOLEAN NOT NULL DEFAULT FALSE,
    state            TEXT NOT NULL DEFAULT 'pending',
    lease_owner      TEXT,
    lease_expires_at TIMESTAMPTZ,
    attempts         INTEGER NOT NULL DEFAULT 0,
    max_attempts     INTEGER NOT NULL DEFAULT 5,
    next_attempt_at  TIMESTAMPTZ,
    content_hash     TEXT,
    result_ref       TEXT,
    last_error       TEXT,
    created_at       TIMESTAMPTZ NOT NULL,
    updated_at       TIMESTAMPTZ NOT NULL,
    CONSTRAINT fk_ingest_fetch_artifact_doc FOREIGN KEY (fetch_doc_id)
        REFERENCES ingest.fetch_doc (id) ON DELETE CASCADE,
    CONSTRAINT uq_ingest_fetch_artifact UNIQUE (fetch_doc_id, kind, ref_key),
    CONSTRAINT chk_ingest_fetch_artifact_kind CHECK (
        kind IN ('body', 'tree', 'file', 'relation', 'appendix')
    ),
    CONSTRAINT chk_ingest_fetch_artifact_state CHECK (
        state IN ('pending', 'claimed', 'done', 'error', 'dead', 'skipped', 'superseded')
    )
);

CREATE INDEX idx_ingest_fetch_artifact_doc ON ingest.fetch_artifact (fetch_doc_id);
CREATE INDEX idx_ingest_fetch_artifact_claim ON ingest.fetch_artifact (state, next_attempt_at);
CREATE INDEX idx_ingest_fetch_artifact_lease ON ingest.fetch_artifact (lease_expires_at);
CREATE INDEX idx_ingest_fetch_artifact_expiry ON ingest.fetch_artifact (url_expires_at);

-- ingest.embedding_cache maps the sha256 of a chunk's exact embed text
-- (context_prefix + "\n" + content) to its vector, so silver/gold rebuilds
-- reuse embeddings for unchanged text instead of re-running the hour-scale
-- Kaggle batch. It lives in ingest — not gold — precisely so corpus rebuilds
-- never truncate it. Written through by EmbedAll; content-addressed, so rows
-- never go stale (a text change is a different hash).
CREATE TABLE ingest.embedding_cache (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    text_hash   TEXT NOT NULL,
    model       TEXT NOT NULL,
    dims        INTEGER NOT NULL,
    embedding   vector(1024) NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_ingest_embedding_cache UNIQUE (text_hash, model, dims)
);

-- corpus_run is the durable identity and accounting envelope for one requested
-- refresh. The country remains the database boundary and is not repeated here.
CREATE TABLE ingest.corpus_run (
    id                      BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    run_uuid                UUID NOT NULL,
    recipe_digest           TEXT NOT NULL,
    input_manifest_ref      TEXT NOT NULL,
    input_manifest_digest   TEXT NOT NULL,
    discovery_cutoff        TIMESTAMPTZ NOT NULL,
    relation_backfill_cutoff TIMESTAMPTZ NOT NULL,
    status                  TEXT NOT NULL DEFAULT 'pending',
    cancellation_requested_at TIMESTAMPTZ,
    cancellation_reason     TEXT NOT NULL DEFAULT '',
    succeeded_count         BIGINT NOT NULL DEFAULT 0,
    pending_retry_count     BIGINT NOT NULL DEFAULT 0,
    terminal_gap_count      BIGINT NOT NULL DEFAULT 0,
    accepted_gap_count      BIGINT NOT NULL DEFAULT 0,
    gap_acceptance_ref      TEXT NOT NULL DEFAULT '',
    rejected_input_count    BIGINT NOT NULL DEFAULT 0,
    failure_count           BIGINT NOT NULL DEFAULT 0,
    started_at              TIMESTAMPTZ,
    completed_at            TIMESTAMPTZ,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_ingest_corpus_run UNIQUE (run_uuid),
    CONSTRAINT chk_ingest_corpus_run_status CHECK (
        status IN ('pending', 'running', 'cancelling', 'cancelled', 'complete', 'failed')
    ),
    CONSTRAINT chk_ingest_corpus_run_counts CHECK (
        succeeded_count >= 0 AND pending_retry_count >= 0 AND terminal_gap_count >= 0
        AND accepted_gap_count >= 0 AND accepted_gap_count <= terminal_gap_count
        AND rejected_input_count >= 0 AND failure_count >= 0
    ),
    CONSTRAINT chk_ingest_corpus_run_complete CHECK (
        status <> 'complete'
        OR (completed_at IS NOT NULL AND pending_retry_count = 0
            AND rejected_input_count = 0 AND failure_count = 0
            AND terminal_gap_count = accepted_gap_count
            AND (terminal_gap_count = 0 OR gap_acceptance_ref <> ''))
    )
);

-- corpus_build closes its input at fixed cutoffs and records every recipe that
-- can change derived evidence. Manifest bounds are required so a worker cannot
-- treat an open-ended scan as a build input.
CREATE TABLE ingest.corpus_build (
    id                      BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    run_id                  BIGINT NOT NULL,
    build_uuid              UUID NOT NULL,
    discovery_cutoff        TIMESTAMPTZ NOT NULL,
    relation_backfill_cutoff TIMESTAMPTZ NOT NULL,
    source_manifest_ref     TEXT NOT NULL,
    source_manifest_digest  TEXT NOT NULL,
    max_manifest_items      BIGINT NOT NULL,
    max_manifest_bytes      BIGINT NOT NULL,
    config_digest           TEXT NOT NULL,
    parser_recipe_digest    TEXT NOT NULL,
    policy_recipe_digest    TEXT NOT NULL,
    citation_recipe_digest  TEXT NOT NULL,
    chunking_recipe_digest  TEXT NOT NULL,
    state                   TEXT NOT NULL DEFAULT 'open',
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at            TIMESTAMPTZ,
    CONSTRAINT fk_ingest_corpus_build_run FOREIGN KEY (run_id)
        REFERENCES ingest.corpus_run (id),
    CONSTRAINT uq_ingest_corpus_build UNIQUE (build_uuid),
    CONSTRAINT uq_ingest_corpus_build_run UNIQUE (run_id, build_uuid),
    CONSTRAINT chk_ingest_corpus_build_bounds CHECK (
        max_manifest_items > 0 AND max_manifest_bytes > 0
    ),
    CONSTRAINT chk_ingest_corpus_build_state CHECK (
        state IN ('open', 'running', 'ready', 'failed', 'cancelled')
    ),
    CONSTRAINT chk_ingest_corpus_build_complete CHECK (
        state NOT IN ('ready', 'failed', 'cancelled') OR completed_at IS NOT NULL
    )
);

-- scheduler_lease serializes all writers for one build. fence_token increases
-- each time an expired lease is acquired, so an old worker cannot regain write
-- authority by presenting only its owner name.
CREATE TABLE ingest.scheduler_lease (
    id                  BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    build_id            BIGINT NOT NULL,
    owner               TEXT NOT NULL,
    fence_token         BIGINT NOT NULL,
    acquired_at         TIMESTAMPTZ NOT NULL,
    renewed_at          TIMESTAMPTZ NOT NULL,
    expires_at          TIMESTAMPTZ NOT NULL,
    released_at         TIMESTAMPTZ,
    CONSTRAINT fk_ingest_scheduler_lease_build FOREIGN KEY (build_id)
        REFERENCES ingest.corpus_build (id),
    CONSTRAINT uq_ingest_scheduler_lease_build UNIQUE (build_id),
    CONSTRAINT chk_ingest_scheduler_lease_owner CHECK (owner <> ''),
    CONSTRAINT chk_ingest_scheduler_lease_token CHECK (fence_token > 0),
    CONSTRAINT chk_ingest_scheduler_lease_expiry CHECK (expires_at > renewed_at),
    CONSTRAINT chk_ingest_scheduler_lease_release CHECK (
        released_at IS NULL OR released_at >= renewed_at
    )
);

CREATE INDEX idx_ingest_scheduler_lease_expiry ON ingest.scheduler_lease (expires_at);

-- stage_result is append-only. Its idempotency key identifies immutable inputs,
-- stage, shard, and recipe. Completion requires every item to be accounted for;
-- pending retries, rejected inputs, failures, and unaccepted gaps block success.
CREATE TABLE ingest.stage_result (
    id                      BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    build_id                BIGINT NOT NULL,
    stage                   TEXT NOT NULL,
    shard_key               TEXT NOT NULL,
    idempotency_key         TEXT NOT NULL,
    input_manifest_ref      TEXT NOT NULL,
    input_manifest_digest   TEXT NOT NULL,
    output_manifest_ref     TEXT NOT NULL DEFAULT '',
    output_manifest_digest  TEXT NOT NULL DEFAULT '',
    recipe_digest           TEXT NOT NULL,
    result_digest           TEXT NOT NULL,
    writer_owner            TEXT NOT NULL,
    fence_token             BIGINT NOT NULL,
    next_cursor             TEXT NOT NULL DEFAULT '',
    next_eligible_at        TIMESTAMPTZ,
    expected_count          BIGINT NOT NULL,
    processed_count         BIGINT NOT NULL,
    succeeded_count         BIGINT NOT NULL DEFAULT 0,
    pending_retry_count     BIGINT NOT NULL DEFAULT 0,
    terminal_gap_count      BIGINT NOT NULL DEFAULT 0,
    accepted_gap_count      BIGINT NOT NULL DEFAULT 0,
    rejected_input_count    BIGINT NOT NULL DEFAULT 0,
    failure_count           BIGINT NOT NULL DEFAULT 0,
    gap_acceptance_ref      TEXT NOT NULL DEFAULT '',
    complete                BOOLEAN NOT NULL,
    completion_state        TEXT NOT NULL,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT fk_ingest_stage_result_build FOREIGN KEY (build_id)
        REFERENCES ingest.corpus_build (id),
    CONSTRAINT uq_ingest_stage_result_idempotency UNIQUE (build_id, stage, idempotency_key),
    CONSTRAINT uq_ingest_stage_result_shard UNIQUE (build_id, stage, shard_key, recipe_digest),
    CONSTRAINT chk_ingest_stage_result_stage CHECK (
        stage IN ('discover', 'fetch', 'extract', 'normalize', 'ocr', 'index',
            'dense', 'lexical', 'evaluate', 'publish', 'activate')
    ),
    CONSTRAINT chk_ingest_stage_result_counts CHECK (
        expected_count >= 0 AND processed_count >= 0 AND processed_count <= expected_count
        AND succeeded_count >= 0 AND pending_retry_count >= 0 AND terminal_gap_count >= 0
        AND accepted_gap_count >= 0 AND accepted_gap_count <= terminal_gap_count
        AND rejected_input_count >= 0 AND failure_count >= 0
        AND processed_count = succeeded_count + pending_retry_count
            + terminal_gap_count + rejected_input_count + failure_count
    ),
    CONSTRAINT chk_ingest_stage_result_completion CHECK (
        completion_state IN ('incomplete', 'complete', 'failed', 'cancelled')
        AND (NOT complete OR (
            processed_count = expected_count
            AND pending_retry_count = 0 AND rejected_input_count = 0
            AND failure_count = 0 AND terminal_gap_count = accepted_gap_count
            AND (terminal_gap_count = 0 OR gap_acceptance_ref <> '')
        ))
        AND complete = (completion_state = 'complete')
    )
);

CREATE INDEX idx_ingest_stage_result_build_stage ON ingest.stage_result (build_id, stage, id);

-- Output references are immutable and ordered because ApplyRequest compares the
-- complete stored payload when an idempotency key is retried.
CREATE TABLE ingest.stage_result_output (
    id                  BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    stage_result_id     BIGINT NOT NULL,
    ordinal             INTEGER NOT NULL,
    object_ref          TEXT NOT NULL,
    digest              TEXT NOT NULL,
    size_bytes          BIGINT NOT NULL,
    media_type          TEXT NOT NULL,
    CONSTRAINT fk_ingest_stage_result_output_result FOREIGN KEY (stage_result_id)
        REFERENCES ingest.stage_result (id),
    CONSTRAINT uq_ingest_stage_result_output UNIQUE (stage_result_id, ordinal),
    CONSTRAINT chk_ingest_stage_result_output_size CHECK (size_bytes >= 0)
);

-- external_job separates durable preparation, provider submission, polling, and
-- result application. An uncertain submission remains explicit for review.
CREATE TABLE ingest.external_job (
    id                      BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    build_id                BIGINT NOT NULL,
    stage                   TEXT NOT NULL,
    provider                TEXT NOT NULL,
    submission_key          TEXT NOT NULL,
    request_manifest_ref    TEXT NOT NULL,
    request_manifest_digest TEXT NOT NULL,
    provider_job_id         TEXT,
    result_manifest_ref     TEXT,
    result_manifest_digest  TEXT,
    state                   TEXT NOT NULL DEFAULT 'prepared',
    writer_owner            TEXT NOT NULL,
    fence_token             BIGINT NOT NULL,
    next_eligible_at        TIMESTAMPTZ,
    cancellation_requested_at TIMESTAMPTZ,
    last_error              TEXT NOT NULL DEFAULT '',
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT fk_ingest_external_job_build FOREIGN KEY (build_id)
        REFERENCES ingest.corpus_build (id),
    CONSTRAINT uq_ingest_external_job_submission UNIQUE (
        build_id, stage, provider, submission_key
    ),
    CONSTRAINT chk_ingest_external_job_request_identity CHECK (
        provider <> '' AND submission_key <> ''
        AND request_manifest_ref <> '' AND request_manifest_digest <> ''
    ),
    CONSTRAINT chk_ingest_external_job_stage CHECK (stage IN ('ocr', 'dense')),
    CONSTRAINT chk_ingest_external_job_state CHECK (
        state IN ('prepared', 'submitted', 'running', 'succeeded', 'failed',
            'cancel_requested', 'cancelled', 'uncertain_submission')
    ),
    CONSTRAINT chk_ingest_external_job_provider_identity CHECK (
        state NOT IN ('submitted', 'running', 'succeeded')
        OR (provider_job_id IS NOT NULL AND provider_job_id <> '')
    ),
    CONSTRAINT chk_ingest_external_job_result_identity CHECK (
        (result_manifest_ref IS NULL AND result_manifest_digest IS NULL)
        OR (result_manifest_ref IS NOT NULL AND result_manifest_ref <> ''
            AND result_manifest_digest IS NOT NULL AND result_manifest_digest <> '')
    ),
    CONSTRAINT chk_ingest_external_job_succeeded CHECK (
        state <> 'succeeded'
        OR (result_manifest_ref IS NOT NULL AND result_manifest_ref <> ''
            AND result_manifest_digest IS NOT NULL AND result_manifest_digest <> '')
    )
);

CREATE INDEX idx_ingest_external_job_poll ON ingest.external_job (state, next_eligible_at);

-- content_blob stores immutable metadata for bytes held in object storage. The
-- sha256 and byte count identify the bytes; PostgreSQL never stores the payload.
CREATE TABLE ingest.content_blob (
    id                  BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    sha256              TEXT NOT NULL,
    byte_size           BIGINT NOT NULL,
    media_type          TEXT NOT NULL,
    object_ref          TEXT NOT NULL,
    capture_source      TEXT NOT NULL,
    capture_ref         TEXT NOT NULL,
    captured_at         TIMESTAMPTZ NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_ingest_content_blob_hash UNIQUE (sha256),
    CONSTRAINT uq_ingest_content_blob_object UNIQUE (object_ref),
    CONSTRAINT chk_ingest_content_blob_hash CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    CONSTRAINT chk_ingest_content_blob_size CHECK (byte_size >= 0)
);

CREATE TABLE ingest.blob_retention_reference (
    id                  BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    blob_id             BIGINT NOT NULL,
    reference_kind      TEXT NOT NULL,
    reference_key       TEXT NOT NULL,
    expires_at          TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT fk_ingest_blob_retention_blob FOREIGN KEY (blob_id)
        REFERENCES ingest.content_blob (id),
    CONSTRAINT uq_ingest_blob_retention_reference UNIQUE (blob_id, reference_kind, reference_key),
    CONSTRAINT chk_ingest_blob_retention_kind CHECK (
        reference_kind IN ('build', 'release', 'active', 'rollback', 'pinned', 'in_flight', 'backup')
    )
);

-- corpus_release names a candidate or published snapshot in this country's
-- writer database. snapshot_ref is an operator-controlled lookup key, not a
-- database name derived from request input.
CREATE TABLE ingest.corpus_release (
    id                  BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    build_id            BIGINT NOT NULL,
    release_key         TEXT NOT NULL,
    snapshot_ref        TEXT NOT NULL,
    state               TEXT NOT NULL DEFAULT 'candidate',
    validated_at        TIMESTAMPTZ,
    ready_at            TIMESTAMPTZ,
    validation_error    TEXT NOT NULL DEFAULT '',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT fk_ingest_corpus_release_build FOREIGN KEY (build_id)
        REFERENCES ingest.corpus_build (id),
    CONSTRAINT uq_ingest_corpus_release_key UNIQUE (release_key),
    CONSTRAINT uq_ingest_corpus_release_snapshot UNIQUE (snapshot_ref),
    CONSTRAINT chk_ingest_corpus_release_state CHECK (
        state IN ('candidate', 'validated', 'ready', 'active', 'retired', 'failed')
    ),
    CONSTRAINT chk_ingest_corpus_release_ready CHECK (
        state NOT IN ('ready', 'active')
        OR (validated_at IS NOT NULL AND ready_at IS NOT NULL AND validation_error = '')
    )
);

-- release_manifest is immutable and complete enough to reject a mismatched
-- database, model, taxonomy, server, or evaluation before activation.
CREATE TABLE ingest.release_manifest (
    id                          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    release_id                  BIGINT NOT NULL,
    corpus_hash                 TEXT NOT NULL,
    blob_set_digest             TEXT NOT NULL,
    source_manifest_digest      TEXT NOT NULL,
    schema_migration_digest     TEXT NOT NULL,
    citation_recipe_digest      TEXT NOT NULL,
    chunking_recipe_digest      TEXT NOT NULL,
    embedding_model_digest      TEXT NOT NULL,
    embedding_tokenizer_digest  TEXT NOT NULL,
    embedding_preprocessing_digest TEXT NOT NULL,
    embedding_dimensions        INTEGER NOT NULL,
    lexical_revision            TEXT NOT NULL,
    lexical_revision_digest     TEXT NOT NULL,
    lexical_recipe_digest       TEXT NOT NULL,
    lexical_tokenizer_digest    TEXT NOT NULL,
    lexical_preprocessing_digest TEXT NOT NULL,
    lexical_dimensions          INTEGER NOT NULL,
    taxonomy_release_id         BIGINT NOT NULL,
    taxonomy_content_digest     TEXT NOT NULL,
    config_digest               TEXT NOT NULL,
    evaluation_artifact_ref     TEXT NOT NULL,
    evaluation_artifact_digest  TEXT NOT NULL,
    compatible_server_image_digest TEXT NOT NULL,
    compatible_embedder_image_digest TEXT NOT NULL,
    source_observation_count    BIGINT NOT NULL,
    document_revision_count     BIGINT NOT NULL,
    provision_revision_count    BIGINT NOT NULL,
    chunk_count                 BIGINT NOT NULL,
    citation_count              BIGINT NOT NULL,
    provenance_count            BIGINT NOT NULL,
    dense_vector_count          BIGINT NOT NULL,
    lexical_chunk_count         BIGINT NOT NULL,
    gap_count                   BIGINT NOT NULL,
    accepted_gap_count          BIGINT NOT NULL,
    gap_acceptance_ref          TEXT NOT NULL DEFAULT '',
    created_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT fk_ingest_release_manifest_release FOREIGN KEY (release_id)
        REFERENCES ingest.corpus_release (id),
    CONSTRAINT uq_ingest_release_manifest_release UNIQUE (release_id),
    CONSTRAINT chk_ingest_release_manifest_dims CHECK (embedding_dimensions = 1024),
    CONSTRAINT chk_ingest_release_manifest_lexical_dims CHECK (lexical_dimensions = 1048576),
    CONSTRAINT chk_ingest_release_manifest_identity CHECK (
        corpus_hash <> '' AND blob_set_digest <> '' AND source_manifest_digest <> ''
        AND schema_migration_digest <> ''
        AND citation_recipe_digest <> '' AND chunking_recipe_digest <> ''
        AND embedding_model_digest <> '' AND embedding_tokenizer_digest <> ''
        AND embedding_preprocessing_digest <> '' AND lexical_revision <> ''
        AND lexical_revision_digest <> '' AND lexical_recipe_digest <> ''
        AND lexical_tokenizer_digest <> '' AND lexical_preprocessing_digest <> ''
        AND taxonomy_content_digest <> '' AND config_digest <> ''
        AND evaluation_artifact_ref <> ''
        AND evaluation_artifact_digest <> '' AND compatible_server_image_digest <> ''
        AND compatible_embedder_image_digest <> ''
    ),
    CONSTRAINT chk_ingest_release_manifest_counts CHECK (
        source_observation_count > 0 AND document_revision_count > 0
        AND provision_revision_count > 0 AND chunk_count > 0
        AND citation_count = chunk_count AND provenance_count = chunk_count
        AND dense_vector_count = chunk_count AND lexical_chunk_count = chunk_count
        AND gap_count >= 0 AND accepted_gap_count = gap_count
        AND (gap_count = 0 OR gap_acceptance_ref <> '')
    )
);

-- One activation row is the serialized release selection for this country.
-- selection_version is the compare-and-set token. previous_release_id preserves
-- the immediate rollback target. The fixed key enforces that singleton boundary.
CREATE TABLE ingest.release_activation (
    id                  BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    selection_key       TEXT NOT NULL,
    selection_version   BIGINT NOT NULL DEFAULT 0,
    current_release_id  BIGINT,
    previous_release_id BIGINT,
    activated_at        TIMESTAMPTZ,
    activated_by        TEXT NOT NULL DEFAULT '',
    CONSTRAINT fk_ingest_release_activation_current FOREIGN KEY (current_release_id)
        REFERENCES ingest.corpus_release (id),
    CONSTRAINT fk_ingest_release_activation_previous FOREIGN KEY (previous_release_id)
        REFERENCES ingest.corpus_release (id),
    CONSTRAINT uq_ingest_release_activation_key UNIQUE (selection_key),
    CONSTRAINT chk_ingest_release_activation_singleton CHECK (selection_key = 'serving'),
    CONSTRAINT chk_ingest_release_activation_version CHECK (selection_version >= 0),
    CONSTRAINT chk_ingest_release_activation_distinct CHECK (
        current_release_id IS NULL OR current_release_id IS DISTINCT FROM previous_release_id
    )
);

-- Explicit holds protect active, rollback, pinned, in-flight, and backup users.
-- Request pins carry both the selected release and an expiry.
CREATE TABLE ingest.release_retention_hold (
    id                  BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    release_id          BIGINT NOT NULL,
    hold_kind           TEXT NOT NULL,
    hold_key            TEXT NOT NULL,
    expires_at          TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT fk_ingest_release_hold_release FOREIGN KEY (release_id)
        REFERENCES ingest.corpus_release (id),
    CONSTRAINT uq_ingest_release_hold UNIQUE (release_id, hold_kind, hold_key),
    CONSTRAINT chk_ingest_release_hold_kind CHECK (
        hold_kind IN ('active', 'rollback', 'pinned', 'in_flight', 'backup')
    ),
    CONSTRAINT chk_ingest_release_pin_expiry CHECK (
        hold_kind NOT IN ('pinned', 'in_flight') OR expires_at IS NOT NULL
    )
);

CREATE INDEX idx_ingest_release_hold_expiry ON ingest.release_retention_hold (release_id, expires_at);

-- A retention claim is a durable tombstone for one release. The row survives
-- release or finalization so each reclaim receives a higher fence token and a
-- finalized release can never accept a new request pin.
CREATE TABLE ingest.release_retention_claim (
    id                  BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    release_id          BIGINT NOT NULL,
    claim_key           TEXT NOT NULL,
    owner               TEXT NOT NULL,
    batch_key           TEXT NOT NULL,
    fence_token         BIGINT NOT NULL,
    claimed_at          TIMESTAMPTZ NOT NULL,
    renewed_at          TIMESTAMPTZ NOT NULL,
    expires_at          TIMESTAMPTZ NOT NULL,
    released_at         TIMESTAMPTZ,
    finalized_at        TIMESTAMPTZ,
    CONSTRAINT fk_ingest_release_claim_release FOREIGN KEY (release_id)
        REFERENCES ingest.corpus_release (id),
    CONSTRAINT uq_ingest_release_claim_release UNIQUE (release_id),
    CONSTRAINT uq_ingest_release_claim_key UNIQUE (claim_key, fence_token),
    CONSTRAINT chk_ingest_release_claim_identity CHECK (
        claim_key <> '' AND owner <> '' AND batch_key <> '' AND fence_token > 0
    ),
    CONSTRAINT chk_ingest_release_claim_expiry CHECK (expires_at > renewed_at),
    CONSTRAINT chk_ingest_release_claim_terminal CHECK (
        (released_at IS NULL OR released_at >= renewed_at)
        AND (finalized_at IS NULL OR finalized_at >= claimed_at)
        AND NOT (released_at IS NOT NULL AND finalized_at IS NOT NULL)
    )
);

CREATE INDEX idx_ingest_release_claim_expiry
    ON ingest.release_retention_claim (expires_at, finalized_at, released_at);

CREATE FUNCTION ingest.reject_immutable_change()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION '% is immutable', TG_TABLE_NAME USING ERRCODE = '55000';
END;
$$;

CREATE FUNCTION ingest.protect_corpus_run_inputs()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.run_uuid IS DISTINCT FROM OLD.run_uuid
        OR NEW.recipe_digest IS DISTINCT FROM OLD.recipe_digest
        OR NEW.input_manifest_ref IS DISTINCT FROM OLD.input_manifest_ref
        OR NEW.input_manifest_digest IS DISTINCT FROM OLD.input_manifest_digest
        OR NEW.discovery_cutoff IS DISTINCT FROM OLD.discovery_cutoff
        OR NEW.relation_backfill_cutoff IS DISTINCT FROM OLD.relation_backfill_cutoff
    THEN
        RAISE EXCEPTION 'corpus_run immutable inputs cannot change' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

CREATE FUNCTION ingest.protect_corpus_build_inputs()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.run_id IS DISTINCT FROM OLD.run_id
        OR NEW.build_uuid IS DISTINCT FROM OLD.build_uuid
        OR NEW.discovery_cutoff IS DISTINCT FROM OLD.discovery_cutoff
        OR NEW.relation_backfill_cutoff IS DISTINCT FROM OLD.relation_backfill_cutoff
        OR NEW.source_manifest_ref IS DISTINCT FROM OLD.source_manifest_ref
        OR NEW.source_manifest_digest IS DISTINCT FROM OLD.source_manifest_digest
        OR NEW.max_manifest_items IS DISTINCT FROM OLD.max_manifest_items
        OR NEW.max_manifest_bytes IS DISTINCT FROM OLD.max_manifest_bytes
        OR NEW.config_digest IS DISTINCT FROM OLD.config_digest
        OR NEW.parser_recipe_digest IS DISTINCT FROM OLD.parser_recipe_digest
        OR NEW.policy_recipe_digest IS DISTINCT FROM OLD.policy_recipe_digest
        OR NEW.citation_recipe_digest IS DISTINCT FROM OLD.citation_recipe_digest
        OR NEW.chunking_recipe_digest IS DISTINCT FROM OLD.chunking_recipe_digest
    THEN
        RAISE EXCEPTION 'corpus_build immutable inputs cannot change' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

CREATE FUNCTION ingest.protect_corpus_release_identity()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.build_id IS DISTINCT FROM OLD.build_id
        OR NEW.release_key IS DISTINCT FROM OLD.release_key
        OR NEW.snapshot_ref IS DISTINCT FROM OLD.snapshot_ref
    THEN
        RAISE EXCEPTION 'corpus_release immutable identity cannot change' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER corpus_run_inputs_immutable
BEFORE UPDATE ON ingest.corpus_run
FOR EACH ROW EXECUTE FUNCTION ingest.protect_corpus_run_inputs();

CREATE TRIGGER corpus_run_delete_rejected
BEFORE DELETE ON ingest.corpus_run
FOR EACH ROW EXECUTE FUNCTION ingest.reject_immutable_change();

CREATE TRIGGER corpus_build_inputs_immutable
BEFORE UPDATE ON ingest.corpus_build
FOR EACH ROW EXECUTE FUNCTION ingest.protect_corpus_build_inputs();

CREATE TRIGGER corpus_build_delete_rejected
BEFORE DELETE ON ingest.corpus_build
FOR EACH ROW EXECUTE FUNCTION ingest.reject_immutable_change();

CREATE TRIGGER corpus_release_identity_immutable
BEFORE UPDATE ON ingest.corpus_release
FOR EACH ROW EXECUTE FUNCTION ingest.protect_corpus_release_identity();

CREATE TRIGGER corpus_release_delete_rejected
BEFORE DELETE ON ingest.corpus_release
FOR EACH ROW EXECUTE FUNCTION ingest.reject_immutable_change();

CREATE TRIGGER stage_result_immutable
BEFORE UPDATE OR DELETE ON ingest.stage_result
FOR EACH ROW EXECUTE FUNCTION ingest.reject_immutable_change();

CREATE TRIGGER stage_result_output_immutable
BEFORE UPDATE OR DELETE ON ingest.stage_result_output
FOR EACH ROW EXECUTE FUNCTION ingest.reject_immutable_change();

CREATE TRIGGER content_blob_immutable
BEFORE UPDATE OR DELETE ON ingest.content_blob
FOR EACH ROW EXECUTE FUNCTION ingest.reject_immutable_change();

CREATE TRIGGER release_manifest_immutable
BEFORE UPDATE OR DELETE ON ingest.release_manifest
FOR EACH ROW EXECUTE FUNCTION ingest.reject_immutable_change();
