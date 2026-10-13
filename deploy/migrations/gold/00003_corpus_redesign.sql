-- +goose Up

-- chunk_set freezes one build-local chunking result and its complete input
-- recipe. Existing gold.chunk remains available until callers migrate.
CREATE TABLE gold.chunk_set (
    id                      BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    build_id                BIGINT NOT NULL,
    chunk_set_key           TEXT NOT NULL,
    source_manifest_digest  TEXT NOT NULL,
    citation_recipe_digest  TEXT NOT NULL,
    chunking_recipe_digest  TEXT NOT NULL,
    tokenizer_digest        TEXT NOT NULL,
    preprocessing_digest    TEXT NOT NULL,
    writer_owner            TEXT NOT NULL,
    fence_token             BIGINT NOT NULL,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_gold_chunk_set_key UNIQUE (chunk_set_key),
    CONSTRAINT uq_gold_chunk_set_id_build UNIQUE (id, build_id),
    CONSTRAINT uq_gold_chunk_set_build_recipe UNIQUE (
        build_id, source_manifest_digest, citation_recipe_digest,
        chunking_recipe_digest, tokenizer_digest, preprocessing_digest
    )
);

-- chunk_local_id is meaningful only with chunk_set_id. The immutable revision
-- references preserve the exact evidence that produced a published citation.
CREATE TABLE gold.corpus_chunk (
    id                      BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    chunk_set_id            BIGINT NOT NULL,
    chunk_local_id          BIGINT NOT NULL,
    document_revision_id    BIGINT NOT NULL,
    provision_revision_id   BIGINT,
    native_citation_bytes   BYTEA NOT NULL,
    native_citation_text    TEXT NOT NULL,
    source_anchor           TEXT NOT NULL,
    context_prefix          TEXT NOT NULL DEFAULT '',
    content                 TEXT NOT NULL,
    content_sha256          TEXT NOT NULL,
    embedding_input_sha256  TEXT NOT NULL,
    ordinal                 INTEGER NOT NULL,
    token_count             INTEGER NOT NULL,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT fk_gold_corpus_chunk_set FOREIGN KEY (chunk_set_id)
        REFERENCES gold.chunk_set (id),
    CONSTRAINT uq_gold_corpus_chunk_set_id UNIQUE (chunk_set_id, id),
    CONSTRAINT uq_gold_corpus_chunk_document_id UNIQUE (id, document_revision_id),
    CONSTRAINT uq_gold_corpus_chunk_embedding_input UNIQUE (id, embedding_input_sha256),
    CONSTRAINT uq_gold_corpus_chunk_local UNIQUE (chunk_set_id, chunk_local_id),
    CONSTRAINT uq_gold_corpus_chunk_ordinal UNIQUE (chunk_set_id, document_revision_id, ordinal),
    CONSTRAINT chk_gold_corpus_chunk_hashes CHECK (
        content_sha256 ~ '^[0-9a-f]{64}$'
        AND embedding_input_sha256 ~ '^[0-9a-f]{64}$'
    ),
    CONSTRAINT chk_gold_corpus_chunk_citation CHECK (
        octet_length(native_citation_bytes) > 0
        AND native_citation_text <> '' AND source_anchor <> ''
    ),
    CONSTRAINT chk_gold_corpus_chunk_counts CHECK (chunk_local_id > 0 AND token_count >= 0)
);

CREATE INDEX idx_gold_corpus_chunk_document ON gold.corpus_chunk (chunk_set_id, document_revision_id);
CREATE INDEX idx_gold_corpus_chunk_provision ON gold.corpus_chunk (provision_revision_id);

-- Dense reuse is valid only when the exact input, model, tokenizer,
-- preprocessing, and dimensions all match.
CREATE TABLE gold.chunk_vector (
    id                      BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    corpus_chunk_id         BIGINT NOT NULL,
    embedding_input_sha256  TEXT NOT NULL,
    model_digest            TEXT NOT NULL,
    tokenizer_digest        TEXT NOT NULL,
    preprocessing_digest    TEXT NOT NULL,
    dimensions              INTEGER NOT NULL,
    embedding               vector(1024) NOT NULL,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT fk_gold_chunk_vector_chunk FOREIGN KEY (
        corpus_chunk_id, embedding_input_sha256
    ) REFERENCES gold.corpus_chunk (id, embedding_input_sha256),
    CONSTRAINT uq_gold_chunk_vector_recipe UNIQUE (
        corpus_chunk_id, embedding_input_sha256, model_digest,
        tokenizer_digest, preprocessing_digest, dimensions
    ),
    CONSTRAINT chk_gold_chunk_vector_hash CHECK (embedding_input_sha256 ~ '^[0-9a-f]{64}$'),
    CONSTRAINT chk_gold_chunk_vector_dims CHECK (dimensions = 1024)
);

CREATE INDEX idx_gold_chunk_vector_chunk ON gold.chunk_vector (corpus_chunk_id);
CREATE INDEX idx_gold_chunk_vector_hnsw ON gold.chunk_vector
    USING hnsw (embedding vector_cosine_ops);

-- One lexical revision covers the whole chunk set, which keeps publication from
-- mixing sparse recipes inside one release.
CREATE TABLE gold.lexical_revision (
    id                      BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    chunk_set_id            BIGINT NOT NULL,
    lexical_revision_key    TEXT NOT NULL,
    lexical_model_digest    TEXT NOT NULL,
    tokenizer_digest        TEXT NOT NULL,
    preprocessing_digest    TEXT NOT NULL,
    dimensions              INTEGER NOT NULL,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT fk_gold_lexical_revision_chunk_set FOREIGN KEY (chunk_set_id)
        REFERENCES gold.chunk_set (id),
    CONSTRAINT uq_gold_lexical_revision_set_id UNIQUE (chunk_set_id, id),
    CONSTRAINT uq_gold_lexical_revision_key UNIQUE (lexical_revision_key),
    CONSTRAINT uq_gold_lexical_revision_set UNIQUE (chunk_set_id),
    CONSTRAINT chk_gold_lexical_revision_dims CHECK (dimensions = 1048576)
);

CREATE TABLE gold.chunk_lexical_vector (
    id                      BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    chunk_set_id            BIGINT NOT NULL,
    lexical_revision_id     BIGINT NOT NULL,
    corpus_chunk_id         BIGINT NOT NULL,
    content_sparse          sparsevec(1048576) NOT NULL,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT fk_gold_chunk_lexical_revision FOREIGN KEY (chunk_set_id, lexical_revision_id)
        REFERENCES gold.lexical_revision (chunk_set_id, id),
    CONSTRAINT fk_gold_chunk_lexical_chunk FOREIGN KEY (chunk_set_id, corpus_chunk_id)
        REFERENCES gold.corpus_chunk (chunk_set_id, id),
    CONSTRAINT uq_gold_chunk_lexical UNIQUE (lexical_revision_id, corpus_chunk_id)
);

-- published_evidence is the immutable serving projection. It carries exact
-- citation bytes, validity, provenance, and explicit known gaps.
CREATE TABLE gold.published_evidence (
    id                      BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    build_id                BIGINT NOT NULL,
    chunk_set_id            BIGINT NOT NULL,
    corpus_chunk_id         BIGINT NOT NULL,
    document_uuid           UUID NOT NULL,
    document_revision_id    BIGINT NOT NULL,
    provision_revision_id   BIGINT,
    native_citation_bytes   BYTEA NOT NULL,
    native_citation_text    TEXT NOT NULL,
    source_anchor           TEXT NOT NULL,
    source_url              TEXT NOT NULL,
    validity_status         TEXT NOT NULL,
    legal_effective_from    TIMESTAMPTZ,
    legal_effective_to      TIMESTAMPTZ,
    provenance              JSONB NOT NULL,
    known_gaps              JSONB NOT NULL,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT fk_gold_published_evidence_chunk FOREIGN KEY (chunk_set_id, corpus_chunk_id)
        REFERENCES gold.corpus_chunk (chunk_set_id, id),
    CONSTRAINT fk_gold_published_evidence_build FOREIGN KEY (chunk_set_id, build_id)
        REFERENCES gold.chunk_set (id, build_id),
    CONSTRAINT fk_gold_published_evidence_document FOREIGN KEY (
        corpus_chunk_id, document_revision_id
    ) REFERENCES gold.corpus_chunk (id, document_revision_id),
    CONSTRAINT uq_gold_published_evidence_chunk UNIQUE (build_id, corpus_chunk_id),
    CONSTRAINT chk_gold_published_evidence_validity CHECK (
        validity_status IN ('in_force', 'expired', 'partial', 'not_yet',
            'suspended', 'inappropriate', 'unknown')
    ),
    CONSTRAINT chk_gold_published_evidence_legal_time CHECK (
        legal_effective_to IS NULL OR legal_effective_from IS NULL
        OR legal_effective_to > legal_effective_from
    )
);

CREATE TABLE gold.published_evidence_subject (
    id                      BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    published_evidence_id   BIGINT NOT NULL,
    taxonomy_release_id     BIGINT NOT NULL,
    subject_key             TEXT NOT NULL,
    context_role            TEXT NOT NULL,
    coverage_state          TEXT NOT NULL,
    CONSTRAINT fk_gold_evidence_subject_evidence FOREIGN KEY (published_evidence_id)
        REFERENCES gold.published_evidence (id),
    CONSTRAINT uq_gold_evidence_subject UNIQUE (
        published_evidence_id, taxonomy_release_id, subject_key, context_role
    ),
    CONSTRAINT chk_gold_evidence_subject_role CHECK (
        context_role IN ('primary', 'validity_context', 'amendment_context', 'relation_context')
    ),
    CONSTRAINT chk_gold_evidence_subject_coverage CHECK (
        coverage_state IN ('assigned', 'unknown', 'not_applicable')
    )
);

CREATE INDEX idx_gold_published_evidence_build ON gold.published_evidence (build_id, id);
CREATE INDEX idx_gold_evidence_subject_filter
    ON gold.published_evidence_subject (taxonomy_release_id, subject_key, coverage_state);

-- +goose StatementBegin
CREATE FUNCTION gold.reject_immutable_change()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION '% is immutable', TG_TABLE_NAME USING ERRCODE = '55000';
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION gold.validate_published_evidence_scope()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM gold.corpus_chunk chunk
        JOIN gold.chunk_set chunk_set ON chunk_set.id = chunk.chunk_set_id
        WHERE chunk.id = NEW.corpus_chunk_id
          AND chunk.chunk_set_id = NEW.chunk_set_id
          AND chunk_set.build_id = NEW.build_id
          AND chunk.document_revision_id = NEW.document_revision_id
          AND chunk.provision_revision_id IS NOT DISTINCT FROM NEW.provision_revision_id
          AND chunk.native_citation_bytes = NEW.native_citation_bytes
          AND chunk.native_citation_text = NEW.native_citation_text
          AND chunk.source_anchor = NEW.source_anchor
    ) THEN
        RAISE EXCEPTION 'published evidence must preserve its chunk scope and citation'
            USING ERRCODE = '23503';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER published_evidence_scope
BEFORE INSERT ON gold.published_evidence
FOR EACH ROW EXECUTE FUNCTION gold.validate_published_evidence_scope();

CREATE TRIGGER chunk_set_immutable
BEFORE UPDATE OR DELETE ON gold.chunk_set
FOR EACH ROW EXECUTE FUNCTION gold.reject_immutable_change();

CREATE TRIGGER corpus_chunk_immutable
BEFORE UPDATE OR DELETE ON gold.corpus_chunk
FOR EACH ROW EXECUTE FUNCTION gold.reject_immutable_change();

CREATE TRIGGER chunk_vector_immutable
BEFORE UPDATE OR DELETE ON gold.chunk_vector
FOR EACH ROW EXECUTE FUNCTION gold.reject_immutable_change();

CREATE TRIGGER lexical_revision_immutable
BEFORE UPDATE OR DELETE ON gold.lexical_revision
FOR EACH ROW EXECUTE FUNCTION gold.reject_immutable_change();

CREATE TRIGGER chunk_lexical_vector_immutable
BEFORE UPDATE OR DELETE ON gold.chunk_lexical_vector
FOR EACH ROW EXECUTE FUNCTION gold.reject_immutable_change();

CREATE TRIGGER published_evidence_immutable
BEFORE UPDATE OR DELETE ON gold.published_evidence
FOR EACH ROW EXECUTE FUNCTION gold.reject_immutable_change();

CREATE TRIGGER published_evidence_subject_immutable
BEFORE UPDATE OR DELETE ON gold.published_evidence_subject
FOR EACH ROW EXECUTE FUNCTION gold.reject_immutable_change();
