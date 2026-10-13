-- +goose Up

-- document_identity is the durable evidence identity. document_uuid is minted by
-- the caller once and does not depend on a title, number, or source alias.
CREATE TABLE silver.document_identity (
    id                  BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    document_uuid       UUID NOT NULL,
    evidence_key        TEXT NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_silver_document_identity_uuid UNIQUE (document_uuid),
    CONSTRAINT uq_silver_document_identity_key UNIQUE (evidence_key)
);

-- Identity decisions are append-only assertions. A merge or split correction
-- adds an assertion that names the prior assertion instead of rewriting history.
CREATE TABLE silver.identity_assertion (
    id                      BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    assertion_key           TEXT NOT NULL,
    assertion_kind          TEXT NOT NULL,
    subject_identity_id     BIGINT NOT NULL,
    object_identity_id      BIGINT,
    source_record_id        BIGINT,
    evidence_ref            TEXT NOT NULL,
    evidence_digest         TEXT NOT NULL,
    method                  TEXT NOT NULL,
    confidence              DOUBLE PRECISION NOT NULL,
    review_state            TEXT NOT NULL,
    replaces_assertion_id   BIGINT,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT fk_silver_identity_assertion_subject FOREIGN KEY (subject_identity_id)
        REFERENCES silver.document_identity (id),
    CONSTRAINT fk_silver_identity_assertion_object FOREIGN KEY (object_identity_id)
        REFERENCES silver.document_identity (id),
    CONSTRAINT fk_silver_identity_assertion_replaces FOREIGN KEY (replaces_assertion_id)
        REFERENCES silver.identity_assertion (id),
    CONSTRAINT uq_silver_identity_assertion_key UNIQUE (assertion_key),
    CONSTRAINT chk_silver_identity_assertion_kind CHECK (
        assertion_kind IN ('source_alias', 'same_document', 'merge', 'split', 'reject_match')
    ),
    CONSTRAINT chk_silver_identity_assertion_review CHECK (
        review_state IN ('unreviewed', 'approved', 'rejected')
    ),
    CONSTRAINT chk_silver_identity_assertion_confidence CHECK (
        confidence >= 0 AND confidence <= 1
    )
);

-- One historical id may have multiple explicit candidate rows. Callers must read
-- all rows and honor mapping_state; no query silently chooses one candidate.
CREATE TABLE silver.legacy_document_mapping (
    id                  BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    legacy_namespace    TEXT NOT NULL,
    legacy_document_id  TEXT NOT NULL,
    candidate_key       TEXT NOT NULL,
    document_identity_id BIGINT,
    mapping_state       TEXT NOT NULL,
    evidence_ref        TEXT NOT NULL DEFAULT '',
    review_state        TEXT NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT fk_silver_legacy_mapping_identity FOREIGN KEY (document_identity_id)
        REFERENCES silver.document_identity (id),
    CONSTRAINT uq_silver_legacy_mapping UNIQUE (
        legacy_namespace, legacy_document_id, candidate_key
    ),
    CONSTRAINT chk_silver_legacy_mapping_state CHECK (
        mapping_state IN ('resolved', 'ambiguous', 'unavailable')
    ),
    CONSTRAINT chk_silver_legacy_mapping_review CHECK (
        review_state IN ('unreviewed', 'approved', 'rejected')
    ),
    CONSTRAINT chk_silver_legacy_mapping_target CHECK (
        (mapping_state = 'resolved' AND document_identity_id IS NOT NULL)
        OR (mapping_state = 'ambiguous' AND candidate_key <> ''
            AND document_identity_id IS NOT NULL)
        OR (mapping_state = 'unavailable' AND document_identity_id IS NULL)
    )
);

CREATE INDEX idx_silver_legacy_mapping_lookup
    ON silver.legacy_document_mapping (legacy_namespace, legacy_document_id);

-- document_revision keeps source observation time, processing time, and legal
-- effective time separate. text_blob_id and metadata bytes identify immutable
-- inputs without copying large source bytes into this layer.
CREATE TABLE silver.document_revision (
    id                      BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    document_identity_id    BIGINT NOT NULL,
    build_id                BIGINT NOT NULL,
    revision_key            TEXT NOT NULL,
    source_observation_id   BIGINT NOT NULL,
    source_observed_at      TIMESTAMPTZ NOT NULL,
    processed_at            TIMESTAMPTZ NOT NULL,
    legal_effective_from    TIMESTAMPTZ,
    legal_effective_to      TIMESTAMPTZ,
    text_blob_id            BIGINT NOT NULL,
    text_sha256             TEXT NOT NULL,
    metadata_bytes          BYTEA NOT NULL,
    metadata_sha256         TEXT NOT NULL,
    parser_recipe_digest    TEXT NOT NULL,
    policy_recipe_digest    TEXT NOT NULL,
    review_state            TEXT NOT NULL,
    writer_owner            TEXT NOT NULL,
    fence_token             BIGINT NOT NULL,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT fk_silver_document_revision_identity FOREIGN KEY (document_identity_id)
        REFERENCES silver.document_identity (id),
    CONSTRAINT uq_silver_document_revision_id_build UNIQUE (id, build_id),
    CONSTRAINT uq_silver_document_revision_key UNIQUE (document_identity_id, revision_key),
    CONSTRAINT uq_silver_document_revision_content UNIQUE (
        document_identity_id, source_observation_id, text_sha256,
        parser_recipe_digest, policy_recipe_digest
    ),
    CONSTRAINT chk_silver_document_revision_hashes CHECK (
        text_sha256 ~ '^[0-9a-f]{64}$' AND metadata_sha256 ~ '^[0-9a-f]{64}$'
    ),
    CONSTRAINT chk_silver_document_revision_legal_time CHECK (
        legal_effective_to IS NULL OR legal_effective_from IS NULL
        OR legal_effective_to > legal_effective_from
    ),
    CONSTRAINT chk_silver_document_revision_review CHECK (
        review_state IN ('unreviewed', 'accepted', 'rejected')
    )
);

CREATE INDEX idx_silver_document_revision_build ON silver.document_revision (build_id, id);
CREATE INDEX idx_silver_document_revision_identity ON silver.document_revision (document_identity_id, processed_at);

-- A provision revision always preserves its exact source citation bytes and
-- source anchor. Stable matching is optional and requires approved evidence.
CREATE TABLE silver.provision_revision (
    id                      BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    document_revision_id    BIGINT NOT NULL,
    build_id                BIGINT NOT NULL,
    provision_key           TEXT NOT NULL,
    stable_provision_uuid   UUID,
    stable_match_evidence_ref TEXT NOT NULL DEFAULT '',
    stable_match_review_state TEXT NOT NULL DEFAULT 'unmatched',
    parent_provision_id     BIGINT,
    ordinal                 INTEGER NOT NULL,
    native_citation_bytes   BYTEA NOT NULL,
    native_citation_text    TEXT NOT NULL,
    source_anchor           TEXT NOT NULL,
    content_blob_id         BIGINT NOT NULL,
    content_sha256          TEXT NOT NULL,
    source_observation_id   BIGINT NOT NULL,
    parser_recipe_digest    TEXT NOT NULL,
    policy_recipe_digest    TEXT NOT NULL,
    review_state            TEXT NOT NULL,
    writer_owner            TEXT NOT NULL,
    fence_token             BIGINT NOT NULL,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT fk_silver_provision_revision_document FOREIGN KEY (document_revision_id, build_id)
        REFERENCES silver.document_revision (id, build_id),
    CONSTRAINT uq_silver_provision_revision_key UNIQUE (document_revision_id, provision_key),
    CONSTRAINT uq_silver_provision_revision_ordinal UNIQUE (document_revision_id, ordinal),
    CONSTRAINT uq_silver_provision_revision_stable UNIQUE (
        document_revision_id, stable_provision_uuid
    ),
    CONSTRAINT uq_silver_provision_revision_document_id UNIQUE (document_revision_id, id),
    CONSTRAINT fk_silver_provision_revision_parent FOREIGN KEY (
        document_revision_id, parent_provision_id
    ) REFERENCES silver.provision_revision (document_revision_id, id),
    CONSTRAINT chk_silver_provision_revision_hash CHECK (content_sha256 ~ '^[0-9a-f]{64}$'),
    CONSTRAINT chk_silver_provision_revision_citation CHECK (
        octet_length(native_citation_bytes) > 0
        AND native_citation_text <> '' AND source_anchor <> ''
    ),
    CONSTRAINT chk_silver_provision_revision_match CHECK (
        stable_match_review_state IN ('unmatched', 'pending_review', 'approved', 'rejected')
        AND ((stable_provision_uuid IS NULL AND stable_match_review_state <> 'approved')
            OR (stable_provision_uuid IS NOT NULL
                AND stable_match_review_state = 'approved'
                AND stable_match_evidence_ref <> ''))
    ),
    CONSTRAINT chk_silver_provision_revision_review CHECK (
        review_state IN ('unreviewed', 'accepted', 'rejected')
    )
);

CREATE INDEX idx_silver_provision_revision_build ON silver.provision_revision (build_id, id);
CREATE INDEX idx_silver_provision_revision_document ON silver.provision_revision (document_revision_id, ordinal);
CREATE INDEX idx_silver_provision_revision_stable ON silver.provision_revision (stable_provision_uuid);

CREATE TABLE silver.document_subject_assignment (
    id                      BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    document_revision_id    BIGINT NOT NULL,
    taxonomy_release_id     BIGINT NOT NULL,
    subject_key             TEXT NOT NULL,
    assignment_method       TEXT NOT NULL,
    evidence_ref            TEXT NOT NULL DEFAULT '',
    confidence              DOUBLE PRECISION,
    review_state            TEXT NOT NULL,
    context_role            TEXT NOT NULL,
    coverage_state          TEXT NOT NULL,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT fk_silver_document_subject_revision FOREIGN KEY (document_revision_id)
        REFERENCES silver.document_revision (id),
    CONSTRAINT uq_silver_document_subject UNIQUE (
        document_revision_id, taxonomy_release_id, subject_key, context_role
    ),
    CONSTRAINT chk_silver_document_subject_confidence CHECK (
        confidence IS NULL OR (confidence >= 0 AND confidence <= 1)
    ),
    CONSTRAINT chk_silver_document_subject_review CHECK (
        review_state IN ('unreviewed', 'approved', 'rejected')
    ),
    CONSTRAINT chk_silver_document_subject_role CHECK (
        context_role IN ('primary', 'validity_context', 'amendment_context', 'relation_context')
    ),
    CONSTRAINT chk_silver_document_subject_coverage CHECK (
        coverage_state IN ('assigned', 'unknown', 'not_applicable')
    )
);

CREATE TABLE silver.provision_subject_assignment (
    id                      BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    provision_revision_id   BIGINT NOT NULL,
    taxonomy_release_id     BIGINT NOT NULL,
    subject_key             TEXT NOT NULL,
    assignment_method       TEXT NOT NULL,
    evidence_ref            TEXT NOT NULL DEFAULT '',
    confidence              DOUBLE PRECISION,
    review_state            TEXT NOT NULL,
    context_role            TEXT NOT NULL,
    coverage_state          TEXT NOT NULL,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT fk_silver_provision_subject_revision FOREIGN KEY (provision_revision_id)
        REFERENCES silver.provision_revision (id),
    CONSTRAINT uq_silver_provision_subject UNIQUE (
        provision_revision_id, taxonomy_release_id, subject_key, context_role
    ),
    CONSTRAINT chk_silver_provision_subject_confidence CHECK (
        confidence IS NULL OR (confidence >= 0 AND confidence <= 1)
    ),
    CONSTRAINT chk_silver_provision_subject_review CHECK (
        review_state IN ('unreviewed', 'approved', 'rejected')
    ),
    CONSTRAINT chk_silver_provision_subject_role CHECK (
        context_role IN ('primary', 'validity_context', 'amendment_context', 'relation_context')
    ),
    CONSTRAINT chk_silver_provision_subject_coverage CHECK (
        coverage_state IN ('assigned', 'unknown', 'not_applicable')
    )
);

CREATE INDEX idx_silver_document_subject_taxonomy
    ON silver.document_subject_assignment (taxonomy_release_id, subject_key, coverage_state);
CREATE INDEX idx_silver_provision_subject_taxonomy
    ON silver.provision_subject_assignment (taxonomy_release_id, subject_key, coverage_state);

-- +goose StatementBegin
CREATE FUNCTION silver.reject_immutable_change()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION '% is immutable', TG_TABLE_NAME USING ERRCODE = '55000';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER document_identity_immutable
BEFORE UPDATE OR DELETE ON silver.document_identity
FOR EACH ROW EXECUTE FUNCTION silver.reject_immutable_change();

CREATE TRIGGER identity_assertion_immutable
BEFORE UPDATE OR DELETE ON silver.identity_assertion
FOR EACH ROW EXECUTE FUNCTION silver.reject_immutable_change();

CREATE TRIGGER legacy_document_mapping_immutable
BEFORE UPDATE OR DELETE ON silver.legacy_document_mapping
FOR EACH ROW EXECUTE FUNCTION silver.reject_immutable_change();

CREATE TRIGGER document_revision_immutable
BEFORE UPDATE OR DELETE ON silver.document_revision
FOR EACH ROW EXECUTE FUNCTION silver.reject_immutable_change();

CREATE TRIGGER provision_revision_immutable
BEFORE UPDATE OR DELETE ON silver.provision_revision
FOR EACH ROW EXECUTE FUNCTION silver.reject_immutable_change();

CREATE TRIGGER document_subject_assignment_immutable
BEFORE UPDATE OR DELETE ON silver.document_subject_assignment
FOR EACH ROW EXECUTE FUNCTION silver.reject_immutable_change();

CREATE TRIGGER provision_subject_assignment_immutable
BEFORE UPDATE OR DELETE ON silver.provision_subject_assignment
FOR EACH ROW EXECUTE FUNCTION silver.reject_immutable_change();
