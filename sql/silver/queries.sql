-- name: UpsertDocument :one
-- Idempotent on the canonical doc_key so dedup re-runs converge on one logical doc.
INSERT INTO silver.document (
    doc_key, doc_number, doc_number_norm, title, doc_type, doc_type_code,
    issuer, issuer_code, issued_at, signer, is_consolidated, markdown,
    source_document_id, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6,
    $7, $8, $9, $10, $11, $12,
    $13, $14, $14
)
ON CONFLICT (doc_key) DO UPDATE SET
    doc_number = EXCLUDED.doc_number,
    doc_number_norm = EXCLUDED.doc_number_norm,
    title = EXCLUDED.title,
    doc_type = EXCLUDED.doc_type,
    doc_type_code = EXCLUDED.doc_type_code,
    issuer = EXCLUDED.issuer,
    issuer_code = EXCLUDED.issuer_code,
    issued_at = EXCLUDED.issued_at,
    signer = EXCLUDED.signer,
    is_consolidated = EXCLUDED.is_consolidated,
    markdown = COALESCE(EXCLUDED.markdown, silver.document.markdown),
    source_document_id = CASE
        WHEN EXCLUDED.markdown IS NOT NULL THEN EXCLUDED.source_document_id
        ELSE silver.document.source_document_id
    END,
    updated_at = EXCLUDED.updated_at
RETURNING id;

-- name: DocumentByKey :one
SELECT * FROM silver.document WHERE doc_key = $1;

-- name: DocumentByID :one
SELECT * FROM silver.document WHERE id = $1;

-- name: ListDocuments :many
SELECT * FROM silver.document
ORDER BY issued_at DESC NULLS LAST, id DESC
LIMIT $1 OFFSET $2;

-- name: UpsertDocRef :one
-- Idempotent on ref_key. Creating a reference target is safe whether or not the
-- target is ingested yet: document_id stays NULL (a stub) until ResolveDocRef
-- links it. COALESCE keeps an already-resolved ref from regressing to a stub.
INSERT INTO silver.doc_ref (
    ref_key, document_id, label, src_ref, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $5
)
ON CONFLICT (ref_key) DO UPDATE SET
    document_id = COALESCE(silver.doc_ref.document_id, EXCLUDED.document_id),
    label = COALESCE(EXCLUDED.label, silver.doc_ref.label),
    src_ref = CASE
        WHEN COALESCE(silver.doc_ref.src_ref->>'source', '') = COALESCE(EXCLUDED.src_ref->>'source', '')
         AND COALESCE(silver.doc_ref.src_ref->>'target_id', '') <> ''
         AND COALESCE(silver.doc_ref.src_ref->>'target_id', '') = COALESCE(EXCLUDED.src_ref->>'target_id', '')
            THEN EXCLUDED.src_ref
        WHEN COALESCE(silver.doc_ref.src_ref->>'target_id', '') = ''
         AND COALESCE(EXCLUDED.src_ref->>'target_id', '') <> ''
            THEN EXCLUDED.src_ref
        ELSE COALESCE(silver.doc_ref.src_ref, EXCLUDED.src_ref)
    END,
    updated_at = EXCLUDED.updated_at
RETURNING id;

-- name: ResolveDocRef :exec
-- Link a stub to a now-ingested document. Run when a new document lands: every ref
-- whose ref_key matches the new doc resolves with no rewrites to the edges that
-- already point at it.
UPDATE silver.doc_ref
SET document_id = $2,
    updated_at = $3
WHERE ref_key = $1 AND document_id IS NULL;

-- name: ResolveDocRefForUniqueNumber :exec
-- Link a number-keyed reference stub to a document, but only while exactly one
-- document carries that normalized number. A bare số ký hiệu cannot pick
-- between documents that share it (e.g. a Luật and a Nghị quyết numbered
-- 51/2005/QH11), so ambiguous references stay stubs.
UPDATE silver.doc_ref
SET document_id = $2,
    updated_at = $3
WHERE ref_key = $1
  AND document_id IS NULL
  AND (
      SELECT count(*) FROM silver.document d
      WHERE d.doc_number_norm = $4 AND d.doc_number_norm <> ''
  ) = 1;

-- name: SetDocumentIndexClass :exec
-- Records the Index-stage scope verdict ('primary' | 'relation_context').
UPDATE silver.document
SET index_class = $2,
    updated_at = $3
WHERE id = $1;

-- name: DocumentIDsByNumberNorm :many
-- Every document carrying a normalized số ký hiệu. More than one row means the
-- bare number is ambiguous (distinct documents share it) and a number-only
-- reference must not resolve.
SELECT id FROM silver.document
WHERE doc_number_norm = $1 AND doc_number_norm <> ''
ORDER BY id;

-- name: DocRefByKey :one
SELECT * FROM silver.doc_ref WHERE ref_key = $1;

-- name: ListUnresolvedStubs :many
-- The out-of-corpus reference targets not yet ingested (the partial-index sweep).
SELECT * FROM silver.doc_ref
WHERE document_id IS NULL
ORDER BY id
LIMIT $1;

-- name: DeleteSectionsByDocument :execrows
-- Normalize replaces the parsed provision tree on each run. ON DELETE CASCADE
-- removes child sections under this document's roots.
DELETE FROM silver.document_section
WHERE document_id = $1;

-- name: UpsertSection :one
-- Idempotent on (document_id, citation_path) so re-parsing the provision tree
-- converges. parent_id is resolved by the caller before insert.
INSERT INTO silver.document_section (
    document_id, parent_id, node_key, ptype, kind, ordinal,
    label, heading, citation_path, content
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10
)
ON CONFLICT (document_id, citation_path) DO UPDATE SET
    parent_id = EXCLUDED.parent_id,
    node_key = EXCLUDED.node_key,
    ptype = EXCLUDED.ptype,
    kind = EXCLUDED.kind,
    ordinal = EXCLUDED.ordinal,
    label = EXCLUDED.label,
    heading = EXCLUDED.heading,
    content = EXCLUDED.content
RETURNING id;

-- name: ListSectionsByDocument :many
WITH RECURSIVE ordered AS (
    SELECT
        s.*,
        ARRAY[LPAD(s.ordinal::text, 8, '0') || ':' || LPAD(s.id::text, 20, '0')] AS sort_path
    FROM silver.document_section s
    WHERE s.document_id = $1
      AND s.parent_id IS NULL

    UNION ALL

    SELECT
        child.*,
        parent.sort_path || ARRAY[LPAD(child.ordinal::text, 8, '0') || ':' || LPAD(child.id::text, 20, '0')]
    FROM silver.document_section child
    JOIN ordered parent ON parent.id = child.parent_id
    WHERE child.document_id = $1
)
SELECT
    id, document_id, parent_id, node_key, ptype, kind, ordinal,
    label, heading, citation_path, content
FROM ordered
ORDER BY sort_path;

-- name: UpsertDocumentText :one
-- Idempotent on (document_id, authority, source) so re-extraction with the same
-- engine/source converges instead of stacking text rows.
INSERT INTO silver.document_text (
    document_id, authority, source, raw_file_id, markdown,
    source_file_sha256, verbatim_sha256, is_binding,
    extract_engine, extract_confidence, needs_review, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $12
)
ON CONFLICT (document_id, authority, source) DO UPDATE SET
    raw_file_id = EXCLUDED.raw_file_id,
    markdown = EXCLUDED.markdown,
    source_file_sha256 = EXCLUDED.source_file_sha256,
    verbatim_sha256 = EXCLUDED.verbatim_sha256,
    is_binding = EXCLUDED.is_binding,
    extract_engine = EXCLUDED.extract_engine,
    extract_confidence = EXCLUDED.extract_confidence,
    needs_review = EXCLUDED.needs_review,
    updated_at = EXCLUDED.updated_at
RETURNING id;

-- name: ListTextsByDocument :many
-- Texts for a document, best authority first; retrieval restricts binding answers
-- to is_binding rows.
SELECT * FROM silver.document_text
WHERE document_id = $1
ORDER BY
    is_binding DESC,
    CASE authority
        WHEN 'human_verified' THEN 1
        WHEN 'gazette_borndigital' THEN 2
        WHEN 'transcription_html' THEN 3
        WHEN 'ocr_extractive' THEN 4
        WHEN 'ocr_generative' THEN 5
        ELSE 99
    END,
    needs_review ASC,
    id;

-- name: InsertValidityPeriod :one
INSERT INTO silver.validity_period (
    document_id, section_id, version_id, status_code, status_class,
    eff_from, eff_to, reason, caused_by_ref_id, source, observed_at, superseded_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12
)
RETURNING id;

-- name: CurrentValidityByDocument :one
-- The latest non-superseded document-level validity record — the status the
-- in-force pre-filter reads in MVP1.
SELECT * FROM silver.validity_period
WHERE document_id = $1 AND section_id IS NULL AND superseded_at IS NULL
ORDER BY observed_at DESC
LIMIT 1;

-- name: CurrentValidityAsOf :one
-- Bitemporal point query: the validity in legal effect on @as_of that the system
-- believed at observation time (not superseded). eff_from/eff_to open ends are
-- treated as unbounded so an open-ended in-force record still matches.
SELECT * FROM silver.validity_period
WHERE document_id = @document_id
  AND section_id IS NULL
  AND superseded_at IS NULL
  AND (eff_from IS NULL OR eff_from <= @as_of)
  AND (eff_to IS NULL OR eff_to > @as_of)
ORDER BY observed_at DESC
LIMIT 1;

-- name: SupersedeValidityPeriods :exec
UPDATE silver.validity_period
SET superseded_at = $2
WHERE document_id = $1 AND section_id IS NULL AND superseded_at IS NULL;

-- name: InsertAmendmentEvent :one
INSERT INTO silver.amendment_event (
    acting_document_id, target_ref_id, change_op, effective_date, source
) VALUES (
    $1, $2, $3, $4, $5
)
RETURNING id;

-- name: ListAmendmentsTargeting :many
SELECT * FROM silver.amendment_event
WHERE target_ref_id = $1
ORDER BY effective_date DESC NULLS LAST;

-- name: UpsertDocumentRelation :one
-- Idempotent on (from_document_id, to_ref_id, relation_type) so the ingested
-- reference graph converges. The target is a doc_ref (resolved or stub).
INSERT INTO silver.document_relation (
    from_document_id, to_ref_id, relation_type, relation_type_raw,
    from_section_id, to_citation, source
) VALUES (
    $1, $2, $3, $4, $5, $6, $7
)
ON CONFLICT (from_document_id, to_ref_id, relation_type) DO UPDATE SET
    relation_type_raw = EXCLUDED.relation_type_raw,
    from_section_id = EXCLUDED.from_section_id,
    to_citation = EXCLUDED.to_citation,
    source = EXCLUDED.source
RETURNING id;

-- name: ListRelationsFrom :many
SELECT * FROM silver.document_relation
WHERE from_document_id = $1;

-- name: ListRelationsTo :many
SELECT * FROM silver.document_relation
WHERE to_ref_id = $1;

-- name: DeleteDocumentRelationsByDocument :exec
DELETE FROM silver.document_relation
WHERE from_document_id = $1;

-- name: DeleteDocumentRelationsByDocumentSource :exec
DELETE FROM silver.document_relation
WHERE from_document_id = $1
  AND source = $2;

-- name: DeleteRelationEvidenceByDocument :exec
DELETE FROM silver.relation_evidence
WHERE from_document_id = $1;

-- name: DeleteRelationEvidenceByDocumentSource :exec
DELETE FROM silver.relation_evidence
WHERE from_document_id = $1
  AND source = $2;

-- name: UpsertRelationEvidence :one
INSERT INTO silver.relation_evidence (
    from_document_id, from_section_id, target_ref_id, evidence_key,
    evidence_kind, relation_type, relation_type_raw, operator,
    target_text, target_citation, citation, snippet, source,
    source_authority, confidence, promoted, created_at, updated_at
) VALUES (
    $1, $2, $3, $4,
    $5, $6, $7, $8,
    $9, $10, $11, $12, $13,
    $14, $15, $16, $17, $17
)
ON CONFLICT (from_document_id, evidence_key) DO UPDATE SET
    from_section_id = EXCLUDED.from_section_id,
    target_ref_id = EXCLUDED.target_ref_id,
    evidence_kind = EXCLUDED.evidence_kind,
    relation_type = EXCLUDED.relation_type,
    relation_type_raw = EXCLUDED.relation_type_raw,
    operator = EXCLUDED.operator,
    target_text = EXCLUDED.target_text,
    target_citation = EXCLUDED.target_citation,
    citation = EXCLUDED.citation,
    snippet = EXCLUDED.snippet,
    source = EXCLUDED.source,
    source_authority = EXCLUDED.source_authority,
    confidence = EXCLUDED.confidence,
    promoted = EXCLUDED.promoted,
    updated_at = EXCLUDED.updated_at
RETURNING id;

-- name: ListRelationEvidenceByDocument :many
SELECT * FROM silver.relation_evidence
WHERE from_document_id = $1
ORDER BY promoted DESC, evidence_kind, relation_type, id;

-- name: ListRelationEvidenceTo :many
SELECT * FROM silver.relation_evidence
WHERE target_ref_id = $1
ORDER BY promoted DESC, confidence DESC, id;

-- name: UpsertDocumentTopic :exec
INSERT INTO silver.document_topic (
    document_id, topic, topic_source, matched_keyword, confidence
) VALUES (
    $1, $2, $3, $4, $5
)
ON CONFLICT (document_id, topic, topic_source) DO UPDATE SET
    matched_keyword = EXCLUDED.matched_keyword,
    confidence = EXCLUDED.confidence;

-- name: ListTopicsByDocument :many
SELECT * FROM silver.document_topic
WHERE document_id = $1
ORDER BY topic;

-- name: UpsertDocumentGazette :one
-- Idempotent on (document_id, gazette_number); a doc may appear in many issues.
INSERT INTO silver.document_gazette (
    document_id, gazette_number, gazette_date, source_document_id
) VALUES (
    $1, $2, $3, $4
)
ON CONFLICT (document_id, gazette_number) DO UPDATE SET
    gazette_date = EXCLUDED.gazette_date,
    source_document_id = EXCLUDED.source_document_id
RETURNING id;

-- name: ListGazettesByDocument :many
SELECT * FROM silver.document_gazette
WHERE document_id = $1
ORDER BY gazette_date NULLS LAST, gazette_number;

-- name: UpsertDocumentAlias :one
-- Maps a source's (source, external_id) to the merged document; idempotent so the
-- cross-source identity map can be rebuilt safely. match_method + confidence make
-- the merge auditable.
INSERT INTO silver.document_alias (
    source, external_id, document_id, match_method, confidence
) VALUES (
    $1, $2, $3, $4, $5
)
ON CONFLICT (source, external_id) DO UPDATE SET
    document_id = EXCLUDED.document_id,
    match_method = EXCLUDED.match_method,
    confidence = EXCLUDED.confidence
RETURNING id;

-- name: DocumentIDByAlias :one
SELECT document_id FROM silver.document_alias
WHERE source = $1 AND external_id = $2;

-- name: GetBindingText :one
-- Return the highest-authority binding text for a document (used by Normalize).
SELECT * FROM silver.document_text
WHERE document_id = $1 AND is_binding = TRUE
ORDER BY
    CASE authority
        WHEN 'human_verified' THEN 1
        WHEN 'gazette_borndigital' THEN 2
        WHEN 'transcription_html' THEN 3
        WHEN 'ocr_extractive' THEN 4
        WHEN 'ocr_generative' THEN 5
        ELSE 99
    END,
    needs_review ASC,
    id
LIMIT 1;
