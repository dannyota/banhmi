CREATE SCHEMA IF NOT EXISTS bronze;

-- bronze.source_document is one row per document as observed at a single source
-- (congbao, vbpl, phapluat). It is the raw, source-of-truth record discovered by
-- the Discover stage and enriched by Fetch. Cross-source identity is resolved
-- later in silver; here a document is keyed by (source, external_id).
--
-- doc_guid is the vbpl≡phapluat join key (same opaque id across the two sites).
-- doc_number_norm + issuer_code + doc_type_code give a resilient congbao↔vbpl
-- dedup tuple that survives display-string drift. has_content drives OCR routing;
-- is_consolidated flags VBHN consolidations. raw_meta holds the non-column source
-- payload. collected_at/first_collected_at are the content_hash change-log anchors.
CREATE TABLE bronze.source_document (
    id                  BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source              TEXT NOT NULL,
    external_id         TEXT NOT NULL,
    doc_guid            TEXT NOT NULL DEFAULT '',
    doc_number          TEXT,
    doc_number_norm     TEXT NOT NULL DEFAULT '',
    doc_type            TEXT,
    doc_type_code       TEXT NOT NULL DEFAULT '',
    issuer              TEXT,
    issuer_code         TEXT NOT NULL DEFAULT '',
    title               TEXT,
    issued_at           TIMESTAMPTZ,
    effective_at        TIMESTAMPTZ,
    expire_at           TIMESTAMPTZ,
    status_raw          TEXT,
    gazette_number      TEXT NOT NULL DEFAULT '',
    gazette_date        TIMESTAMPTZ,
    has_content         BOOLEAN NOT NULL DEFAULT FALSE,
    is_consolidated     BOOLEAN NOT NULL DEFAULT FALSE,
    detail_url          TEXT,
    content_hash        TEXT,
    raw_meta            JSONB,
    discovered_at       TIMESTAMPTZ NOT NULL,
    fetched_at          TIMESTAMPTZ,
    collected_at        TIMESTAMPTZ NOT NULL,
    first_collected_at  TIMESTAMPTZ NOT NULL,
    CONSTRAINT uq_bronze_source_document UNIQUE (source, external_id)
);

CREATE INDEX idx_bronze_source_document_guid ON bronze.source_document (doc_guid);
CREATE INDEX idx_bronze_source_document_dedup ON bronze.source_document (doc_number_norm, issuer_code, issued_at);
CREATE INDEX idx_bronze_source_document_status ON bronze.source_document (source, status_raw);
CREATE INDEX idx_bronze_source_document_issued ON bronze.source_document (issued_at);
CREATE INDEX idx_bronze_source_document_hash ON bronze.source_document (content_hash);

-- bronze.raw_payload stores the raw inline body a source serves (vbpl/phapluat
-- HTML or JSON). Large blobs (PDF/DOCX) live on disk and are tracked in raw_file.
-- UNIQUE (source_document_id, kind) makes a re-fetch idempotent (one body, one
-- tree, one references blob, one detail blob per document).
CREATE TABLE bronze.raw_payload (
    id                  BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source_document_id  BIGINT NOT NULL,
    kind                TEXT NOT NULL,
    content             TEXT,
    content_hash        TEXT,
    collected_at        TIMESTAMPTZ NOT NULL,
    first_collected_at  TIMESTAMPTZ NOT NULL,
    CONSTRAINT fk_bronze_raw_payload_document FOREIGN KEY (source_document_id)
        REFERENCES bronze.source_document (id) ON DELETE CASCADE,
    CONSTRAINT uq_bronze_raw_payload UNIQUE (source_document_id, kind),
    CONSTRAINT chk_bronze_raw_payload_kind CHECK (
        kind IN ('content_html', 'provision_tree_json', 'references_json', 'detail_json')
    )
);

CREATE INDEX idx_bronze_raw_payload_document ON bronze.raw_payload (source_document_id);
CREATE INDEX idx_bronze_raw_payload_hash ON bronze.raw_payload (content_hash);

-- bronze.raw_file references a downloaded PDF/DOCX kept in object storage / a
-- volume. The bytes never live in Postgres: storage_path + sha256 are the handle.
-- file_kind (role) × file_format × is_authoritative + the natural-key UNIQUE make
-- one-doc-many-files idempotent and fix a real re-fetch duplicate bug. congbao
-- born-digital files are authoritative; ordinal/label order siblings of a role.
-- label is the source-provided filename; local files may still use hash paths.
CREATE TABLE bronze.raw_file (
    id                  BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source_document_id  BIGINT NOT NULL,
    file_kind           TEXT NOT NULL,
    file_format         TEXT NOT NULL,
    is_authoritative    BOOLEAN NOT NULL DEFAULT FALSE,
    ordinal             INTEGER NOT NULL DEFAULT 0,
    label               TEXT NOT NULL DEFAULT '',
    lang                TEXT NOT NULL DEFAULT '',
    url                 TEXT,
    storage_path        TEXT,
    sha256              TEXT,
    byte_size           BIGINT,
    content_hash        TEXT,
    collected_at        TIMESTAMPTZ NOT NULL,
    first_collected_at  TIMESTAMPTZ NOT NULL,
    CONSTRAINT fk_bronze_raw_file_document FOREIGN KEY (source_document_id)
        REFERENCES bronze.source_document (id) ON DELETE CASCADE,
    CONSTRAINT uq_bronze_raw_file UNIQUE (source_document_id, file_kind, ordinal, file_format),
    CONSTRAINT chk_bronze_raw_file_kind CHECK (
        file_kind IN ('main', 'appendix', 'version_snapshot', 'original_scan', 'attachment')
    ),
    CONSTRAINT chk_bronze_raw_file_format CHECK (
        file_format IN ('pdf', 'docx', 'doc', 'html')
    )
);

CREATE INDEX idx_bronze_raw_file_document ON bronze.raw_file (source_document_id);
CREATE INDEX idx_bronze_raw_file_sha256 ON bronze.raw_file (sha256);
