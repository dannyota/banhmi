-- name: UpsertSourceDocument :one
-- Idempotent on the (source, external_id) natural key so Discover/Fetch retries
-- converge. Returns the row id used as the business key from silver. The
-- discovery harness supplies the core columns + raw_meta (the full source record);
-- the resilient-dedup columns (doc_guid, *_norm, *_code, gazette*, flags) default
-- empty until Fetch enriches them. collected_at/first_collected_at anchor the
-- content_hash change-log.
INSERT INTO bronze.source_document (
    source, external_id, doc_number, title, doc_type, issuer,
    issued_at, effective_at, status_raw, detail_url, content_hash,
    raw_meta, discovered_at, fetched_at, collected_at, first_collected_at
) VALUES (
    $1, $2, $3, $4, $5, $6,
    $7, $8, $9, $10, $11,
    $12, $13, $14, COALESCE($14, $13::timestamptz), COALESCE($14, $13::timestamptz)
)
ON CONFLICT (source, external_id) DO UPDATE SET
    doc_number = COALESCE(EXCLUDED.doc_number, source_document.doc_number),
    title = COALESCE(EXCLUDED.title, source_document.title),
    doc_type = COALESCE(EXCLUDED.doc_type, source_document.doc_type),
    issuer = COALESCE(EXCLUDED.issuer, source_document.issuer),
    issued_at = COALESCE(EXCLUDED.issued_at, source_document.issued_at),
    effective_at = COALESCE(EXCLUDED.effective_at, source_document.effective_at),
    status_raw = COALESCE(EXCLUDED.status_raw, source_document.status_raw),
    detail_url = COALESCE(EXCLUDED.detail_url, source_document.detail_url),
    content_hash = COALESCE(EXCLUDED.content_hash, source_document.content_hash),
    -- raw_meta and fetched_at are phase-specific: Discover supplies raw_meta with a
    -- NULL fetched_at; Fetch supplies fetched_at. COALESCE so neither phase (nor an
    -- incremental re-discovery, which carries a NULL fetched_at) clobbers the other.
    raw_meta = COALESCE(EXCLUDED.raw_meta, source_document.raw_meta),
    fetched_at = COALESCE(EXCLUDED.fetched_at, source_document.fetched_at),
    collected_at = COALESCE(EXCLUDED.fetched_at, EXCLUDED.discovered_at)
RETURNING id;

-- name: EnrichSourceDocument :exec
-- Fetch-stage enrichment of the resilient-dedup and gazette columns, keyed by the
-- natural key. Kept separate from UpsertSourceDocument so the discovery harness
-- stays simple while Fetch fills doc_guid / *_norm / *_code / gazette / flags.
UPDATE bronze.source_document
SET doc_guid = $3,
    doc_number_norm = $4,
    doc_type_code = $5,
    issuer_code = $6,
    expire_at = $7,
    gazette_number = $8,
    gazette_date = $9,
    has_content = $10,
    is_consolidated = $11,
    collected_at = $12
WHERE source = $1 AND external_id = $2;

-- name: SourceDocumentByExternalID :one
SELECT * FROM bronze.source_document
WHERE source = $1 AND external_id = $2;

-- name: SourceDocumentByID :one
SELECT * FROM bronze.source_document WHERE id = $1;

-- name: SourceDocumentContentHash :one
SELECT content_hash FROM bronze.source_document
WHERE source = $1 AND external_id = $2;

-- name: ListSourceDocumentsBySource :many
SELECT * FROM bronze.source_document
WHERE source = $1
ORDER BY issued_at DESC NULLS LAST, id DESC
LIMIT $2 OFFSET $3;

-- name: UpsertRawPayload :one
-- Idempotent on (source_document_id, kind): a re-fetch overwrites the single body,
-- tree, references, or detail blob for the document instead of duplicating it.
INSERT INTO bronze.raw_payload (
    source_document_id, kind, content, content_hash, collected_at, first_collected_at
) VALUES (
    $1, $2, $3, $4, $5, $5
)
ON CONFLICT (source_document_id, kind) DO UPDATE SET
    content = EXCLUDED.content,
    content_hash = EXCLUDED.content_hash,
    collected_at = EXCLUDED.collected_at
RETURNING id;

-- name: DeleteRawPayloadByKind :exec
DELETE FROM bronze.raw_payload
WHERE source_document_id = $1 AND kind = $2;

-- name: ListRawPayloadsByDocument :many
SELECT * FROM bronze.raw_payload
WHERE source_document_id = $1
ORDER BY kind;

-- name: UpsertRawFile :one
-- Idempotent on the (source_document_id, file_kind, ordinal, file_format) natural
-- key so re-downloading the same role+format file converges instead of duping.
INSERT INTO bronze.raw_file (
    source_document_id, file_kind, file_format, is_authoritative, ordinal,
    label, lang, url, storage_path, sha256, byte_size, content_hash,
    collected_at, first_collected_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $13
)
ON CONFLICT (source_document_id, file_kind, ordinal, file_format) DO UPDATE SET
    is_authoritative = EXCLUDED.is_authoritative,
    label = EXCLUDED.label,
    lang = EXCLUDED.lang,
    url = EXCLUDED.url,
    storage_path = EXCLUDED.storage_path,
    sha256 = EXCLUDED.sha256,
    byte_size = EXCLUDED.byte_size,
    content_hash = EXCLUDED.content_hash,
    collected_at = EXCLUDED.collected_at
RETURNING id;

-- name: ListRawFilesByDocument :many
SELECT * FROM bronze.raw_file
WHERE source_document_id = $1
ORDER BY
    CASE file_kind
        WHEN 'main' THEN 0
        WHEN 'appendix' THEN 1
        WHEN 'attachment' THEN 2
        WHEN 'version_snapshot' THEN 3
        WHEN 'original_scan' THEN 4
        ELSE 9
    END,
    ordinal,
    file_format;

-- name: UpdateRawFileLabel :exec
-- Source detail re-planning may learn a better source-provided filename after a
-- file was already downloaded. Update the display label by the same natural key
-- used by UpsertRawFile; local storage remains hash-addressed.
UPDATE bronze.raw_file
SET label = $5
WHERE source_document_id = $1
  AND file_kind = $2
  AND ordinal = $3
  AND file_format = $4
  AND label IS DISTINCT FROM $5;

-- name: InsertSourceRecord :one
INSERT INTO bronze.source_record (
    source, external_id, legacy_source_document_id, first_observed_at, created_at
) VALUES (
    @source, @external_id, @legacy_source_document_id, @first_observed_at, @created_at
)
ON CONFLICT (source, external_id) DO NOTHING
RETURNING *;

-- name: SourceRecordByNaturalKey :one
SELECT * FROM bronze.source_record
WHERE source = @source AND external_id = @external_id;

-- name: SourceRecordByLegacyID :one
SELECT * FROM bronze.source_record
WHERE legacy_source_document_id = @legacy_source_document_id;

-- name: InsertObservationRevision :one
INSERT INTO bronze.observation_revision (
    source_record_id, build_id, observation_key, source_observed_at, captured_at,
    source_url, metadata_bytes, metadata_sha256, payload_blob_id,
    capture_recipe_digest, review_state, writer_owner, fence_token, created_at
) VALUES (
    @source_record_id, @build_id, @observation_key, @source_observed_at, @captured_at,
    @source_url, @metadata_bytes, @metadata_sha256, @payload_blob_id,
    @capture_recipe_digest, @review_state, @writer_owner, @fence_token, @created_at
)
ON CONFLICT (source_record_id, observation_key) DO NOTHING
RETURNING *;

-- name: ObservationRevisionByKey :one
SELECT * FROM bronze.observation_revision
WHERE source_record_id = @source_record_id AND observation_key = @observation_key;

-- name: ListObservationRevisions :many
SELECT * FROM bronze.observation_revision
WHERE source_record_id = @source_record_id
ORDER BY source_observed_at, id;

-- name: InsertArtifact :one
INSERT INTO bronze.artifact (
    artifact_key, build_id, blob_id, artifact_kind, source_url,
    legacy_raw_payload_id, legacy_raw_file_id, writer_owner, fence_token, created_at
) VALUES (
    @artifact_key, @build_id, @blob_id, @artifact_kind, @source_url,
    @legacy_raw_payload_id, @legacy_raw_file_id, @writer_owner, @fence_token, @created_at
)
ON CONFLICT (artifact_key) DO NOTHING
RETURNING *;

-- name: ArtifactByKey :one
SELECT * FROM bronze.artifact WHERE artifact_key = @artifact_key;

-- name: ArtifactByLegacyPayloadID :one
SELECT * FROM bronze.artifact WHERE legacy_raw_payload_id = @legacy_raw_payload_id;

-- name: ArtifactByLegacyFileID :one
SELECT * FROM bronze.artifact WHERE legacy_raw_file_id = @legacy_raw_file_id;

-- name: LinkObservationArtifact :one
INSERT INTO bronze.observation_artifact (
    build_id, observation_revision_id, artifact_id, role, ordinal, created_at
) VALUES (
    @build_id, @observation_revision_id, @artifact_id, @role, @ordinal, @created_at
)
ON CONFLICT (observation_revision_id, role, ordinal) DO NOTHING
RETURNING *;

-- name: ListObservationArtifacts :many
SELECT artifact.*, link.role, link.ordinal
FROM bronze.observation_artifact link
JOIN bronze.artifact artifact ON artifact.id = link.artifact_id
WHERE link.observation_revision_id = @observation_revision_id
ORDER BY link.role, link.ordinal;
