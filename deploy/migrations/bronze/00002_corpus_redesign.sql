-- +goose Up

-- source_record is the stable source-native identity. Legacy ids remain explicit
-- compatibility keys while callers move from bronze.source_document.
CREATE TABLE bronze.source_record (
    id                          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source                      TEXT NOT NULL,
    external_id                 TEXT NOT NULL,
    legacy_source_document_id   BIGINT,
    first_observed_at           TIMESTAMPTZ NOT NULL,
    created_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_bronze_source_record UNIQUE (source, external_id),
    CONSTRAINT uq_bronze_source_record_legacy UNIQUE (legacy_source_document_id)
);

-- observation_revision records exactly what a source asserted at one observation.
-- metadata_bytes preserves the source representation byte-for-byte. A corrected
-- observation is a new row and never changes an accepted revision.
CREATE TABLE bronze.observation_revision (
    id                  BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source_record_id    BIGINT NOT NULL,
    build_id            BIGINT NOT NULL,
    observation_key     TEXT NOT NULL,
    source_observed_at  TIMESTAMPTZ NOT NULL,
    captured_at         TIMESTAMPTZ NOT NULL,
    source_url          TEXT NOT NULL,
    metadata_bytes      BYTEA NOT NULL,
    metadata_sha256     TEXT NOT NULL,
    payload_blob_id     BIGINT,
    capture_recipe_digest TEXT NOT NULL,
    review_state        TEXT NOT NULL DEFAULT 'unreviewed',
    writer_owner        TEXT NOT NULL,
    fence_token         BIGINT NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT fk_bronze_observation_source_record FOREIGN KEY (source_record_id)
        REFERENCES bronze.source_record (id),
    CONSTRAINT uq_bronze_observation_build_id UNIQUE (build_id, id),
    CONSTRAINT uq_bronze_observation_revision UNIQUE (source_record_id, observation_key),
    CONSTRAINT uq_bronze_observation_content UNIQUE (source_record_id, metadata_sha256, payload_blob_id),
    CONSTRAINT chk_bronze_observation_hash CHECK (metadata_sha256 ~ '^[0-9a-f]{64}$'),
    CONSTRAINT chk_bronze_observation_review CHECK (
        review_state IN ('unreviewed', 'accepted', 'rejected')
    )
);

CREATE INDEX idx_bronze_observation_build ON bronze.observation_revision (build_id, id);
CREATE INDEX idx_bronze_observation_record_time ON bronze.observation_revision (source_record_id, source_observed_at);

-- artifact points to immutable content-addressed blob metadata in ingest by a
-- validated scalar id. Legacy payload and file ids remain independently mappable.
CREATE TABLE bronze.artifact (
    id                      BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    artifact_key            TEXT NOT NULL,
    build_id                BIGINT NOT NULL,
    blob_id                 BIGINT NOT NULL,
    artifact_kind           TEXT NOT NULL,
    source_url              TEXT,
    legacy_raw_payload_id   BIGINT,
    legacy_raw_file_id      BIGINT,
    writer_owner            TEXT NOT NULL,
    fence_token             BIGINT NOT NULL,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_bronze_artifact_key UNIQUE (artifact_key),
    CONSTRAINT uq_bronze_artifact_build_id UNIQUE (build_id, id),
    CONSTRAINT uq_bronze_artifact_legacy_payload UNIQUE (legacy_raw_payload_id),
    CONSTRAINT uq_bronze_artifact_legacy_file UNIQUE (legacy_raw_file_id),
    CONSTRAINT chk_bronze_artifact_legacy CHECK (
        legacy_raw_payload_id IS NULL OR legacy_raw_file_id IS NULL
    ),
    CONSTRAINT chk_bronze_artifact_kind CHECK (
        artifact_kind IN ('source_payload', 'source_file', 'extracted_text',
            'ocr_text', 'provider_manifest', 'provider_result')
    )
);

CREATE INDEX idx_bronze_artifact_build ON bronze.artifact (build_id, id);
CREATE INDEX idx_bronze_artifact_blob ON bronze.artifact (blob_id);

CREATE TABLE bronze.observation_artifact (
    id                      BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    build_id                BIGINT NOT NULL,
    observation_revision_id BIGINT NOT NULL,
    artifact_id             BIGINT NOT NULL,
    role                    TEXT NOT NULL,
    ordinal                 INTEGER NOT NULL DEFAULT 0,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT fk_bronze_observation_artifact_observation FOREIGN KEY (
        build_id, observation_revision_id
    ) REFERENCES bronze.observation_revision (build_id, id),
    CONSTRAINT fk_bronze_observation_artifact_artifact FOREIGN KEY (build_id, artifact_id)
        REFERENCES bronze.artifact (build_id, id),
    CONSTRAINT uq_bronze_observation_artifact UNIQUE (observation_revision_id, role, ordinal),
    CONSTRAINT uq_bronze_observation_artifact_id UNIQUE (observation_revision_id, artifact_id)
);

-- +goose StatementBegin
CREATE FUNCTION bronze.reject_immutable_change()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION '% is immutable', TG_TABLE_NAME USING ERRCODE = '55000';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER source_record_immutable
BEFORE UPDATE OR DELETE ON bronze.source_record
FOR EACH ROW EXECUTE FUNCTION bronze.reject_immutable_change();

CREATE TRIGGER observation_revision_immutable
BEFORE UPDATE OR DELETE ON bronze.observation_revision
FOR EACH ROW EXECUTE FUNCTION bronze.reject_immutable_change();

CREATE TRIGGER artifact_immutable
BEFORE UPDATE OR DELETE ON bronze.artifact
FOR EACH ROW EXECUTE FUNCTION bronze.reject_immutable_change();

CREATE TRIGGER observation_artifact_immutable
BEFORE UPDATE OR DELETE ON bronze.observation_artifact
FOR EACH ROW EXECUTE FUNCTION bronze.reject_immutable_change();
