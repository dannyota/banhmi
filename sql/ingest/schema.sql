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
