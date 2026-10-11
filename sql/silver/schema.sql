CREATE SCHEMA IF NOT EXISTS silver;

-- silver.document is one logical legal document, deduplicated across sources. The
-- canonical identity doc_key = "<TYPE>|<NUMBER>" (normalized loại văn bản +
-- normalized số ký hiệu; the type discriminates documents that share a number,
-- e.g. Luật vs Nghị quyết 51/2005/QH11), the number alone when the type is
-- missing, source:external_id when the number is missing. is_consolidated flags a
-- VBHN consolidation. markdown holds the denormalized display text. The link back
-- to bronze is a business key (source_document_id, int) — never a cross-schema FK.
CREATE TABLE silver.document (
    id                  BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    doc_key             TEXT NOT NULL,
    doc_number          TEXT,
    doc_number_norm     TEXT NOT NULL DEFAULT '',
    title               TEXT,
    doc_type            TEXT,
    doc_type_code       TEXT NOT NULL DEFAULT '',
    issuer              TEXT,
    issuer_code         TEXT NOT NULL DEFAULT '',
    issued_at           TIMESTAMPTZ,
    signer              TEXT,
    is_consolidated     BOOLEAN NOT NULL DEFAULT FALSE,
    markdown            TEXT,
    source_document_id  BIGINT,
    metadata_priority   SMALLINT NOT NULL DEFAULT 0,
    -- index_class is the Index-stage scope verdict: 'primary' documents carry
    -- chunks in the searchable corpus; 'relation_context' documents were pulled
    -- only by relation backfill and fail the configured scope vocabulary, so
    -- their text and relations stay served (document tool, amendment clauses)
    -- but no chunks are indexed. Index recomputes it every run.
    index_class         TEXT NOT NULL DEFAULT 'primary',
    created_at          TIMESTAMPTZ NOT NULL,
    updated_at          TIMESTAMPTZ NOT NULL,
    CONSTRAINT uq_silver_document_key UNIQUE (doc_key)
);

CREATE INDEX idx_silver_document_number ON silver.document (doc_number);
CREATE INDEX idx_silver_document_issuer_type ON silver.document (issuer, doc_type);
CREATE INDEX idx_silver_document_issued ON silver.document (issued_at);

-- silver.doc_ref is the referenceable identity, including out-of-corpus stubs.
-- ref_key is the normalized reference key; document_id is a business key that is
-- NULL while the target is a stub (referenced but not yet ingested) and resolves
-- automatically when the target is later ingested — with no row rewrites elsewhere.
-- Relations, amendments, and validity all target a doc_ref. src_ref keeps the raw
-- reference payload (citation text, source ids) for re-resolution and audit.
CREATE TABLE silver.doc_ref (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    ref_key      TEXT NOT NULL,
    document_id  BIGINT,
    label        TEXT,
    src_ref      JSONB,
    created_at   TIMESTAMPTZ NOT NULL,
    updated_at   TIMESTAMPTZ NOT NULL,
    CONSTRAINT uq_silver_doc_ref_key UNIQUE (ref_key)
);

CREATE INDEX idx_silver_doc_ref_document ON silver.doc_ref (document_id);
-- Partial index over the unresolved stubs the resolver sweeps when a new doc lands.
CREATE INDEX idx_silver_doc_ref_unresolved ON silver.doc_ref (ref_key)
    WHERE document_id IS NULL;

-- silver.document_section is the provision tree (Phần/Chương/Mục/Điều/Khoản/Điểm).
-- parent_id self-references for the hierarchy. node_key is the source's stable node
-- id (vbpl UUID) for idempotent re-parse — NULL when the tree is parsed from text
-- (congbao/DOCX), which has no source node id; ptype keeps the raw source type code so
-- a re-map is a pure recompute; kind is the normalized level. citation_path is the
-- stable path used as the chunk citation, unique within a document. The UNIQUE on
-- node_key admits many NULLs (Postgres NULLS DISTINCT), so text-parsed sections do not
-- collide; real vbpl node ids stay unique.
CREATE TABLE silver.document_section (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    document_id     BIGINT NOT NULL,
    parent_id       BIGINT,
    node_key        TEXT,
    ptype           SMALLINT,
    kind            TEXT NOT NULL,
    ordinal         INTEGER NOT NULL,
    label           TEXT,
    heading         TEXT,
    citation_path   TEXT NOT NULL,
    content         TEXT,
    CONSTRAINT fk_silver_section_document FOREIGN KEY (document_id)
        REFERENCES silver.document (id) ON DELETE CASCADE,
    CONSTRAINT fk_silver_section_parent FOREIGN KEY (parent_id)
        REFERENCES silver.document_section (id) ON DELETE CASCADE,
    CONSTRAINT uq_silver_section_citation UNIQUE (document_id, citation_path),
    CONSTRAINT uq_silver_section_node UNIQUE (document_id, node_key),
    CONSTRAINT chk_silver_section_kind CHECK (
        -- VN provision levels (Phần/Chương/Mục/Điều/Khoản/Điểm/Phụ lục) +
        -- MY provision levels (Part/Chapter/Section/Subsection/Paragraph/Schedule) +
        -- ID provision levels (BAB/Bagian/Paragraf/Pasal/Ayat/Huruf/Penjelasan/Lampiran) +
        -- SG extras (Division for SSO Acts, Heading for MAS Notice section headings).
        kind IN ('phan', 'chuong', 'muc', 'dieu', 'khoan', 'diem', 'phuluc',
                 'part', 'chapter', 'section', 'subsection', 'paragraph', 'schedule',
                 'division', 'heading',
                 'bab', 'bagian', 'paragraf', 'pasal', 'ayat', 'huruf', 'penjelasan', 'lampiran')
    )
);

CREATE INDEX idx_silver_section_document ON silver.document_section (document_id);
CREATE INDEX idx_silver_section_parent ON silver.document_section (parent_id);

-- silver.document_relation is the reference graph ingested directly from the
-- sources (vbpl references[], phapluat docRelateEffects[]). The target is a
-- doc_ref so an out-of-corpus document is still a first-class edge endpoint;
-- to_citation pins a clause-level target on a stub. relation_type_raw keeps the
-- source's integer code so re-mapping relation_type is a pure recompute.
-- from_section_id is nullable for forward-compatible clause-level relations.
CREATE TABLE silver.document_relation (
    id                 BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    from_document_id   BIGINT NOT NULL,
    to_ref_id          BIGINT NOT NULL,
    relation_type      TEXT NOT NULL,
    relation_type_raw  INTEGER,
    from_section_id    BIGINT,
    to_citation        TEXT,
    source             TEXT,
    CONSTRAINT fk_silver_relation_from_doc FOREIGN KEY (from_document_id)
        REFERENCES silver.document (id) ON DELETE CASCADE,
    CONSTRAINT fk_silver_relation_to_ref FOREIGN KEY (to_ref_id)
        REFERENCES silver.doc_ref (id) ON DELETE CASCADE,
    CONSTRAINT fk_silver_relation_from_sec FOREIGN KEY (from_section_id)
        REFERENCES silver.document_section (id) ON DELETE SET NULL,
    CONSTRAINT uq_silver_relation UNIQUE (from_document_id, to_ref_id, relation_type)
);

CREATE INDEX idx_silver_relation_from ON silver.document_relation (from_document_id);
CREATE INDEX idx_silver_relation_to ON silver.document_relation (to_ref_id);

-- silver.relation_evidence is the evidence layer behind the relation graph.
-- VBPL structured references are authoritative. Text/fallback exact-number
-- references stay weak until a model classifier produces an explicit
-- model_classification row. Only promoted rows may create document_relation /
-- amendment / validity effects. evidence_key is caller-built from source /
-- citation / target so re-runs are idempotent without a wide nullable UNIQUE.
CREATE TABLE silver.relation_evidence (
    id                 BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    from_document_id   BIGINT NOT NULL,
    from_section_id    BIGINT,
    target_ref_id      BIGINT NOT NULL,
    evidence_key       TEXT NOT NULL,
    evidence_kind      TEXT NOT NULL,
    relation_type      TEXT NOT NULL,
    relation_type_raw  INTEGER,
    operator           TEXT NOT NULL DEFAULT '',
    target_text        TEXT NOT NULL,
    target_citation    TEXT,
    citation           TEXT NOT NULL DEFAULT '',
    snippet            TEXT NOT NULL DEFAULT '',
    source             TEXT NOT NULL DEFAULT '',
    source_authority   TEXT NOT NULL DEFAULT '',
    confidence         DOUBLE PRECISION NOT NULL DEFAULT 0,
    promoted           BOOLEAN NOT NULL DEFAULT FALSE,
    created_at         TIMESTAMPTZ NOT NULL,
    updated_at         TIMESTAMPTZ NOT NULL,
    CONSTRAINT fk_silver_relation_ev_from_doc FOREIGN KEY (from_document_id)
        REFERENCES silver.document (id) ON DELETE CASCADE,
    CONSTRAINT fk_silver_relation_ev_from_sec FOREIGN KEY (from_section_id)
        REFERENCES silver.document_section (id) ON DELETE SET NULL,
    CONSTRAINT fk_silver_relation_ev_target FOREIGN KEY (target_ref_id)
        REFERENCES silver.doc_ref (id) ON DELETE CASCADE,
    CONSTRAINT uq_silver_relation_ev UNIQUE (from_document_id, evidence_key),
    CONSTRAINT chk_silver_relation_ev_kind CHECK (
        evidence_kind IN ('structured_relation', 'model_classification', 'weak_relation')
    )
);

CREATE INDEX idx_silver_relation_ev_from ON silver.relation_evidence (from_document_id);
CREATE INDEX idx_silver_relation_ev_target ON silver.relation_evidence (target_ref_id);
CREATE INDEX idx_silver_relation_ev_type ON silver.relation_evidence (relation_type, promoted);

-- silver.amendment_event is a first-class amendment event: acting_document acts on
-- a target doc_ref (amends / replaces / abrogates / suspends ...). The target is a
-- doc_ref so amendments of out-of-corpus documents are captured; versioning-ready.
CREATE TABLE silver.amendment_event (
    id                  BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    acting_document_id  BIGINT NOT NULL,
    target_ref_id       BIGINT NOT NULL,
    change_op           TEXT NOT NULL,
    effective_date      TIMESTAMPTZ,
    source              TEXT,
    CONSTRAINT fk_silver_amendment_acting FOREIGN KEY (acting_document_id)
        REFERENCES silver.document (id) ON DELETE CASCADE,
    CONSTRAINT fk_silver_amendment_target FOREIGN KEY (target_ref_id)
        REFERENCES silver.doc_ref (id) ON DELETE CASCADE
);

CREATE INDEX idx_silver_amendment_acting ON silver.amendment_event (acting_document_id);
CREATE INDEX idx_silver_amendment_target ON silver.amendment_event (target_ref_id);

-- silver.validity_period is the bitemporal validity record: legal time
-- (eff_from/eff_to) plus system time (observed_at/superseded_at) so the system can
-- answer "what was in force on date D?" and never present repealed/superseded/
-- not-yet-effective text as current. status_class collapses status_code into the
-- in-force test class; partial (HHL1P/TNHL1P) is never a hard exclusion.
-- section_id/version_id are nullable forward-compat business keys for later
-- clause-/version-level validity; caused_by_ref_id is the doc_ref that caused the
-- change. version_id and caused_by_ref_id are FK-free business keys by design.
CREATE TABLE silver.validity_period (
    id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    document_id       BIGINT NOT NULL,
    section_id        BIGINT,
    version_id        BIGINT,
    status_code       TEXT NOT NULL,
    status_class      TEXT NOT NULL,
    eff_from          TIMESTAMPTZ,
    eff_to            TIMESTAMPTZ,
    reason            TEXT,
    caused_by_ref_id  BIGINT,
    source            TEXT,
    observed_at       TIMESTAMPTZ NOT NULL,
    superseded_at     TIMESTAMPTZ,
    CONSTRAINT fk_silver_validity_document FOREIGN KEY (document_id)
        REFERENCES silver.document (id) ON DELETE CASCADE,
    CONSTRAINT fk_silver_validity_section FOREIGN KEY (section_id)
        REFERENCES silver.document_section (id) ON DELETE CASCADE,
    CONSTRAINT chk_silver_validity_class CHECK (
        status_class IN ('in_force', 'expired', 'partial', 'not_yet', 'suspended', 'inappropriate', 'unknown')
    )
);

CREATE INDEX idx_silver_validity_document ON silver.validity_period (document_id);
CREATE INDEX idx_silver_validity_section ON silver.validity_period (section_id);
CREATE INDEX idx_silver_validity_current ON silver.validity_period (document_id, superseded_at);

-- silver.document_text holds per-(authority, source) binding provenance: which
-- text wording is authoritative and where it came from. authority ranks the text
-- (human-verified > gazette born-digital > transcription HTML > extractive OCR >
-- generative OCR); is_binding gates whether retrieval may quote it as legal
-- text. source_file_sha256 + verbatim_sha256 back the congbao↔vbpl reconcile.
-- raw_file_id is a business key into bronze.raw_file (no cross-schema FK).
CREATE TABLE silver.document_text (
    id                  BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    document_id         BIGINT NOT NULL,
    authority           TEXT NOT NULL,
    source              TEXT NOT NULL DEFAULT '',
    raw_file_id         BIGINT,
    markdown            TEXT,
    source_file_sha256  TEXT,
    verbatim_sha256     TEXT,
    is_binding          BOOLEAN NOT NULL DEFAULT FALSE,
    extract_engine      TEXT,
    extract_confidence  DOUBLE PRECISION,
    needs_review        BOOLEAN NOT NULL DEFAULT FALSE,
    created_at          TIMESTAMPTZ NOT NULL,
    updated_at          TIMESTAMPTZ NOT NULL,
    CONSTRAINT fk_silver_text_document FOREIGN KEY (document_id)
        REFERENCES silver.document (id) ON DELETE CASCADE,
    CONSTRAINT uq_silver_document_text UNIQUE (document_id, authority, source),
    CONSTRAINT chk_silver_text_authority CHECK (
        authority IN ('gazette_borndigital', 'transcription_html', 'ocr_extractive',
            'ocr_generative', 'human_verified')
    )
);

CREATE INDEX idx_silver_text_document ON silver.document_text (document_id);

-- silver.document_topic tags a document with a lĩnh vực topic (one row per topic).
-- topic_source records how the tag was derived; matched_keyword + confidence make
-- keyword-match tags auditable.
CREATE TABLE silver.document_topic (
    id               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    document_id      BIGINT NOT NULL,
    topic            TEXT NOT NULL,
    topic_source     TEXT NOT NULL,
    matched_keyword  TEXT,
    confidence       DOUBLE PRECISION,
    CONSTRAINT fk_silver_topic_document FOREIGN KEY (document_id)
        REFERENCES silver.document (id) ON DELETE CASCADE,
    CONSTRAINT uq_silver_document_topic UNIQUE (document_id, topic, topic_source),
    CONSTRAINT chk_silver_topic_source CHECK (
        topic_source IN ('linhvuc_source', 'classifier', 'keyword_match')
    )
);

CREATE INDEX idx_silver_topic_document ON silver.document_topic (document_id);
CREATE INDEX idx_silver_topic_topic ON silver.document_topic (topic);

-- silver.document_gazette links a document to the công báo issue(s) it appears in.
-- A document can appear in multiple issues (including corrigenda), so this is a
-- child table. source_document_id is a business key into bronze (no cross-schema FK).
CREATE TABLE silver.document_gazette (
    id                  BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    document_id         BIGINT NOT NULL,
    gazette_number      TEXT NOT NULL,
    gazette_date        TIMESTAMPTZ,
    source_document_id  BIGINT,
    CONSTRAINT fk_silver_gazette_document FOREIGN KEY (document_id)
        REFERENCES silver.document (id) ON DELETE CASCADE,
    CONSTRAINT uq_silver_document_gazette UNIQUE (document_id, gazette_number)
);

CREATE INDEX idx_silver_gazette_document ON silver.document_gazette (document_id);

-- silver.document_alias maps each source observation (source, external_id) to the
-- merged logical document — the cross-source identity map that backs dedup.
-- match_method + confidence make every merge auditable and reversible.
CREATE TABLE silver.document_alias (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source        TEXT NOT NULL,
    external_id   TEXT NOT NULL,
    document_id   BIGINT NOT NULL,
    match_method  TEXT NOT NULL DEFAULT '',
    confidence    DOUBLE PRECISION,
    CONSTRAINT fk_silver_alias_document FOREIGN KEY (document_id)
        REFERENCES silver.document (id) ON DELETE CASCADE,
    CONSTRAINT uq_silver_document_alias UNIQUE (source, external_id)
);

CREATE INDEX idx_silver_alias_document ON silver.document_alias (document_id);

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

CREATE FUNCTION silver.reject_immutable_change()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION '% is immutable', TG_TABLE_NAME USING ERRCODE = '55000';
END;
$$;

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
