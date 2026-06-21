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
        -- MY provision levels (Part/Chapter/Section/Subsection/Paragraph/Schedule).
        kind IN ('phan', 'chuong', 'muc', 'dieu', 'khoan', 'diem', 'phuluc',
                 'part', 'chapter', 'section', 'subsection', 'paragraph', 'schedule')
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
