-- Phase 1 uses raw pgx tests; the generated adapter follows hosted generation.
-- Parameters 1-10 match revisions.sql. 11 is nullable revision ID (reports and
-- comparisons only), 12 reserved revision key, 13 candidate digest. Payloads
-- are trusted validated JSON projections, never untrusted converter bytes.
-- Reports and comparisons also accept zero for the absent revision ID; NULLIF
-- preserves a null stored parent with the generated nonnullable bigint argument.
-- Lock admission before materialized document_version lease. Recheck time at
-- insertion. ON CONFLICT winners can be invisible to a READ COMMITTED snapshot;
-- an empty result requires rollback and a fresh fenced read before retry.
-- Bulk preflight rejects every existing payload conflict before insertion.
-- Phase 2 must enforce payload/item bounds, verify full manifest membership,
-- and convert interchange coordinates. Blob catalog sizes bound every range.

-- name: RegisterConversionArtifact :one
WITH admission AS (
    SELECT a.id AS id, a.country_code AS country_code FROM ingest.country_admission a
    WHERE a.id = 1 AND a.admission_epoch = $1 AND a.admitted_writer = $2 AND a.enabled FOR SHARE
), lease AS MATERIALIZED (
    SELECT l.id AS id, l.expires_at AS expires_at, l.owner AS owner,
        l.attempt_fence AS attempt_fence, l.admission_epoch AS admission_epoch, l.cancelled_at AS cancelled_at
    FROM ingest.execution_lease l CROSS JOIN admission
    WHERE l.resource_kind = $3 AND l.resource_key = $4 FOR UPDATE OF l
), current_execution AS (
    SELECT e.id AS id, e.execution_key AS execution_key, lease.expires_at AS expires_at,
        admission.country_code AS country_code
    FROM ingest.execution e CROSS JOIN lease CROSS JOIN admission
    WHERE $3::text = 'document_version' AND lease.expires_at > clock_timestamp()
      AND lease.owner = $5::text AND lease.attempt_fence = $6::bigint
      AND lease.admission_epoch = $1::bigint AND lease.cancelled_at IS NULL
      AND e.id = $7 AND e.scope_kind = 'intake' AND e.resource_kind = $3 AND e.resource_key = $4
      AND e.attempt_fence = $6 AND e.admission_epoch = $1
      AND e.input_digest = $8 AND e.recipe_digest = $9 AND e.budget_digest = $10
), parent AS (
    SELECT r.id AS id, r.revision_key AS revision_key FROM silver.document_revision r
    CROSS JOIN current_execution c
    WHERE r.id = $11 AND r.revision_key = $12 AND r.document_version_key = $4::text
      AND split_part(r.revision_key, ':', 1) = c.country_code AND c.expires_at > clock_timestamp()
), items AS MATERIALIZED (
    SELECT ($14::jsonb->>'role')::text AS role,
        ($14::jsonb->>'ordinal')::integer AS ordinal,
        ($14::jsonb->>'blob_hash')::text AS blob_hash,
        ($14::jsonb->>'byte_size')::bigint AS byte_size,
        ($14::jsonb->>'schema_version')::text AS schema_version,
        ($14::jsonb->>'original_hash')::text AS original_hash,
        ($14::jsonb->>'conversion_recipe_digest')::text AS conversion_recipe_digest,
        ($14::jsonb->>'normalization_recipe_digest')::text AS normalization_recipe_digest,
        ($14::jsonb->>'detector_digest')::text AS detector_digest,
        ($14::jsonb->>'manifest_hash')::text AS manifest_hash,
        ($14::jsonb->>'manifest_byte_size')::bigint AS manifest_byte_size
), eligible AS (
    SELECT parent_row.id AS id, parent_row.revision_key AS revision_key FROM parent parent_row CROSS JOIN items x
    WHERE EXISTS (SELECT 1 FROM bronze.blob blob_0 WHERE blob_0.sha256 = x.blob_hash AND blob_0.byte_size = x.byte_size) AND
        EXISTS (SELECT 1 FROM bronze.blob blob_1 WHERE blob_1.sha256 = x.original_hash) AND
        EXISTS (SELECT 1 FROM bronze.blob blob_2 WHERE blob_2.sha256 = x.manifest_hash AND blob_2.byte_size = x.manifest_byte_size) AND NOT EXISTS (
        SELECT 1 FROM silver.conversion_artifact stored WHERE stored.revision_key = $12 AND stored.candidate_digest = $13
          AND stored.role = x.role AND stored.ordinal = x.ordinal AND ROW(stored.role, stored.ordinal, stored.blob_hash, stored.byte_size, stored.schema_version, stored.original_hash, stored.conversion_recipe_digest, stored.normalization_recipe_digest, stored.detector_digest, stored.manifest_hash, stored.manifest_byte_size) IS DISTINCT FROM ROW(x.role, x.ordinal, x.blob_hash, x.byte_size, x.schema_version, x.original_hash, x.conversion_recipe_digest, x.normalization_recipe_digest, x.detector_digest, x.manifest_hash, x.manifest_byte_size)
    )
), inserted AS (
    INSERT INTO silver.conversion_artifact (document_revision_id, revision_key, document_version_key, processing_execution_key, candidate_digest, role, ordinal, blob_hash, byte_size, schema_version, original_hash, conversion_recipe_digest, normalization_recipe_digest, detector_digest, manifest_hash, manifest_byte_size)
    SELECT eligible_row.id, eligible_row.revision_key, $4::text, c.execution_key, $13::text, x.role, x.ordinal, x.blob_hash, x.byte_size, x.schema_version, x.original_hash, x.conversion_recipe_digest, x.normalization_recipe_digest, x.detector_digest, x.manifest_hash, x.manifest_byte_size FROM eligible eligible_row CROSS JOIN items x CROSS JOIN current_execution c
    WHERE c.expires_at > clock_timestamp() ON CONFLICT DO NOTHING RETURNING id AS id
)
SELECT fresh.id AS id FROM inserted fresh
UNION ALL
SELECT stored.id AS id FROM silver.conversion_artifact stored CROSS JOIN eligible eligible_row CROSS JOIN items x
WHERE stored.revision_key = $12 AND stored.candidate_digest = $13 AND stored.role = x.role AND stored.ordinal = x.ordinal
  AND NOT EXISTS (SELECT 1 FROM inserted fresh);

-- name: AppendEditLedgerEntry :one
WITH admission AS (
    SELECT a.id AS id, a.country_code AS country_code FROM ingest.country_admission a
    WHERE a.id = 1 AND a.admission_epoch = $1::bigint AND a.admitted_writer = $2::text AND a.enabled FOR SHARE
), lease AS MATERIALIZED (
    SELECT l.id AS id, l.expires_at AS expires_at, l.owner AS owner,
        l.attempt_fence AS attempt_fence, l.admission_epoch AS admission_epoch, l.cancelled_at AS cancelled_at
    FROM ingest.execution_lease l CROSS JOIN admission
    WHERE l.resource_kind = $3::text AND l.resource_key = $4::text FOR UPDATE OF l
), current_execution AS (
    SELECT e.id AS id, e.execution_key AS execution_key, lease.expires_at AS expires_at,
        admission.country_code AS country_code
    FROM ingest.execution e CROSS JOIN lease CROSS JOIN admission
    WHERE $3::text = 'document_version' AND lease.expires_at > clock_timestamp()
      AND lease.owner = $5::text AND lease.attempt_fence = $6::bigint
      AND lease.admission_epoch = $1::bigint AND lease.cancelled_at IS NULL
      AND e.id = $7::bigint AND e.scope_kind = 'intake' AND e.resource_kind = $3::text AND e.resource_key = $4::text
      AND e.attempt_fence = $6::bigint AND e.admission_epoch = $1::bigint
      AND e.input_digest = $8::text AND e.recipe_digest = $9::text AND e.budget_digest = $10::text
), parent AS (
    SELECT r.id AS id, r.revision_key AS revision_key FROM silver.document_revision r
    CROSS JOIN current_execution c
    WHERE r.id = $11::bigint AND r.revision_key = $12::text AND r.document_version_key = $4::text
      AND split_part(r.revision_key, ':', 1) = c.country_code AND c.expires_at > clock_timestamp()
), items AS MATERIALIZED (
    SELECT ($14::jsonb->>'ledger_hash')::text AS ledger_hash,
        ($14::jsonb->>'operation_ordinal')::integer AS operation_ordinal,
        ($14::jsonb->>'operation')::text AS operation,
        ($14::jsonb->>'rule')::text AS rule,
        ($14::jsonb->>'rule_version')::text AS rule_version,
        ($14::jsonb->>'input_hash')::text AS input_hash,
        ($14::jsonb->>'output_hash')::text AS output_hash,
        ($14::jsonb->>'input_start')::bigint AS input_start,
        ($14::jsonb->>'input_end')::bigint AS input_end,
        ($14::jsonb->>'output_start')::bigint AS output_start,
        ($14::jsonb->>'output_end')::bigint AS output_end,
        decode(($14::jsonb->>'removed_bytes')::text, 'hex') AS removed_bytes,
        decode(($14::jsonb->>'replacement_bytes')::text, 'hex') AS replacement_bytes,
        ($14::jsonb->'encoding_evidence')::jsonb AS encoding_evidence,
        ($14::jsonb->'font_evidence')::jsonb AS font_evidence,
        ($14::jsonb->'source_anchors')::jsonb AS source_anchors
), eligible AS (
    SELECT parent_row.id AS id, parent_row.revision_key AS revision_key FROM parent parent_row CROSS JOIN items x
    WHERE EXISTS (SELECT 1 FROM bronze.blob blob_0 WHERE blob_0.sha256 = x.ledger_hash) AND
        EXISTS (SELECT 1 FROM bronze.blob blob_1 WHERE blob_1.sha256 = x.input_hash AND x.input_end <= blob_1.byte_size) AND
        EXISTS (SELECT 1 FROM bronze.blob blob_2 WHERE blob_2.sha256 = x.output_hash AND x.output_end <= blob_2.byte_size) AND NOT EXISTS (
        SELECT 1 FROM silver.edit_ledger stored WHERE stored.revision_key = $12::text AND stored.candidate_digest = $13::text
          AND stored.ledger_hash = x.ledger_hash AND stored.operation_ordinal = x.operation_ordinal AND ROW(stored.ledger_hash, stored.operation_ordinal, stored.operation, stored.rule, stored.rule_version, stored.input_hash, stored.output_hash, stored.input_start, stored.input_end, stored.output_start, stored.output_end, stored.removed_bytes, stored.replacement_bytes, stored.encoding_evidence, stored.font_evidence, stored.source_anchors) IS DISTINCT FROM ROW(x.ledger_hash, x.operation_ordinal, x.operation, x.rule, x.rule_version, x.input_hash, x.output_hash, x.input_start, x.input_end, x.output_start, x.output_end, x.removed_bytes, x.replacement_bytes, x.encoding_evidence, x.font_evidence, x.source_anchors)
    )
), inserted AS (
    INSERT INTO silver.edit_ledger (document_revision_id, revision_key, document_version_key, processing_execution_key, candidate_digest, ledger_hash, operation_ordinal, operation, rule, rule_version, input_hash, output_hash, input_start, input_end, output_start, output_end, removed_bytes, replacement_bytes, encoding_evidence, font_evidence, source_anchors)
    SELECT eligible_row.id, eligible_row.revision_key, $4::text, c.execution_key, $13::text, x.ledger_hash, x.operation_ordinal, x.operation, x.rule, x.rule_version, x.input_hash, x.output_hash, x.input_start, x.input_end, x.output_start, x.output_end, x.removed_bytes, x.replacement_bytes, x.encoding_evidence, x.font_evidence, x.source_anchors FROM eligible eligible_row CROSS JOIN items x CROSS JOIN current_execution c
    WHERE c.expires_at > clock_timestamp() ON CONFLICT DO NOTHING RETURNING id AS id
)
SELECT fresh.id AS id FROM inserted fresh
UNION ALL
SELECT stored.id AS id FROM silver.edit_ledger stored CROSS JOIN eligible eligible_row CROSS JOIN items x
WHERE stored.revision_key = $12::text AND stored.candidate_digest = $13::text AND stored.ledger_hash = x.ledger_hash AND stored.operation_ordinal = x.operation_ordinal
  AND NOT EXISTS (SELECT 1 FROM inserted fresh);

-- name: InsertOffsetMapSegments :one
WITH admission AS (
    SELECT a.id AS id, a.country_code AS country_code FROM ingest.country_admission a
    WHERE a.id = 1 AND a.admission_epoch = $1 AND a.admitted_writer = $2 AND a.enabled FOR SHARE
), lease AS MATERIALIZED (
    SELECT l.id AS id, l.expires_at AS expires_at, l.owner AS owner,
        l.attempt_fence AS attempt_fence, l.admission_epoch AS admission_epoch, l.cancelled_at AS cancelled_at
    FROM ingest.execution_lease l CROSS JOIN admission
    WHERE l.resource_kind = $3 AND l.resource_key = $4 FOR UPDATE OF l
), current_execution AS (
    SELECT e.id AS id, e.execution_key AS execution_key, lease.expires_at AS expires_at,
        admission.country_code AS country_code
    FROM ingest.execution e CROSS JOIN lease CROSS JOIN admission
    WHERE $3::text = 'document_version' AND lease.expires_at > clock_timestamp()
      AND lease.owner = $5::text AND lease.attempt_fence = $6::bigint
      AND lease.admission_epoch = $1::bigint AND lease.cancelled_at IS NULL
      AND e.id = $7 AND e.scope_kind = 'intake' AND e.resource_kind = $3 AND e.resource_key = $4
      AND e.attempt_fence = $6 AND e.admission_epoch = $1
      AND e.input_digest = $8 AND e.recipe_digest = $9 AND e.budget_digest = $10
), parent AS (
    SELECT r.id AS id, r.revision_key AS revision_key FROM silver.document_revision r
    CROSS JOIN current_execution c
    WHERE r.id = $11 AND r.revision_key = $12 AND r.document_version_key = $4::text
      AND split_part(r.revision_key, ':', 1) = c.country_code AND c.expires_at > clock_timestamp()
), items AS MATERIALIZED (
    SELECT (j.value->>'map_hash')::text AS map_hash,
        (j.value->>'source_block_id')::text AS source_block_id,
        (j.value->>'destination_block_id')::text AS destination_block_id,
        (j.value->>'source_space')::text AS source_space,
        (j.value->>'destination_space')::text AS destination_space,
        (j.value->>'source_start')::bigint AS source_start,
        (j.value->>'source_end')::bigint AS source_end,
        (j.value->>'destination_start')::bigint AS destination_start,
        (j.value->>'destination_end')::bigint AS destination_end,
        (j.value->>'transform_order')::integer AS transform_order,
        (j.value->>'segment_ordinal')::integer AS segment_ordinal,
        (j.value->>'transform')::text AS transform,
        (j.value->>'source_page')::integer AS source_page,
        (j.value->>'source_object')::text AS source_object,
        (j.value->>'source_member')::text AS source_member,
        (j.value->>'source_reason')::text AS source_reason,
        (j.value->>'destination_page')::integer AS destination_page,
        (j.value->>'destination_object')::text AS destination_object,
        (j.value->>'destination_member')::text AS destination_member,
        (j.value->>'destination_reason')::text AS destination_reason
    FROM jsonb_array_elements($16::jsonb) AS j(value)
), eligible AS (
    SELECT parent_row.id AS id, parent_row.revision_key AS revision_key FROM parent parent_row
    JOIN bronze.blob source_blob ON source_blob.sha256 = $14
    JOIN bronze.blob destination_blob ON destination_blob.sha256 = $15
    -- Member and intermediate transform blobs have their own hashes and sizes.
    -- They need not be one of the five top-level conversion output roles.
    WHERE NOT EXISTS (SELECT 1 FROM items x WHERE
          (x.source_end IS NOT NULL AND x.source_end > source_blob.byte_size)
          OR (x.destination_end IS NOT NULL AND x.destination_end > destination_blob.byte_size)
          OR NOT EXISTS (SELECT 1 FROM bronze.blob map_blob WHERE map_blob.sha256 = x.map_hash))
      AND (SELECT count(*) = count(DISTINCT ROW(x.transform_order, x.segment_ordinal)) FROM items x)
      AND NOT EXISTS (SELECT 1 FROM items x JOIN silver.offset_map_segment stored
          ON stored.revision_key = $12 AND stored.candidate_digest = $13
          AND stored.source_hash = $14 AND stored.destination_hash = $15
          AND stored.transform_order = x.transform_order AND stored.segment_ordinal = x.segment_ordinal
          WHERE ROW(stored.map_hash, stored.source_block_id, stored.destination_block_id, stored.source_space, stored.destination_space, stored.source_start, stored.source_end, stored.destination_start, stored.destination_end, stored.transform_order, stored.segment_ordinal, stored.transform, stored.source_page, stored.source_object, stored.source_member, stored.source_reason, stored.destination_page, stored.destination_object, stored.destination_member, stored.destination_reason) IS DISTINCT FROM ROW(x.map_hash, x.source_block_id, x.destination_block_id, x.source_space, x.destination_space, x.source_start, x.source_end, x.destination_start, x.destination_end, x.transform_order, x.segment_ordinal, x.transform, x.source_page, x.source_object, x.source_member, x.source_reason, x.destination_page, x.destination_object, x.destination_member, x.destination_reason))
), inserted AS (
    INSERT INTO silver.offset_map_segment (document_revision_id, revision_key, document_version_key, processing_execution_key, candidate_digest, map_hash, source_block_id, destination_block_id, source_space, destination_space, source_start, source_end, destination_start, destination_end, transform_order, segment_ordinal, transform, source_page, source_object, source_member, source_reason, destination_page, destination_object, destination_member, destination_reason, source_hash, destination_hash)
    SELECT eligible_row.id, eligible_row.revision_key, $4::text, c.execution_key, $13::text, x.map_hash, x.source_block_id, x.destination_block_id, x.source_space, x.destination_space, x.source_start, x.source_end, x.destination_start, x.destination_end, x.transform_order, x.segment_ordinal, x.transform, x.source_page, x.source_object, x.source_member, x.source_reason, x.destination_page, x.destination_object, x.destination_member, x.destination_reason, $14::text, $15::text
    FROM eligible eligible_row CROSS JOIN items x CROSS JOIN current_execution c
    WHERE c.expires_at > clock_timestamp() ON CONFLICT DO NOTHING RETURNING id AS id
)
SELECT eligible_row.id AS id, (SELECT count(*) FROM items) AS segment_count,
    (SELECT count(*) FROM inserted) AS inserted_count FROM eligible eligible_row
WHERE (SELECT count(*) FROM inserted) + (SELECT count(*) FROM items x
    JOIN silver.offset_map_segment stored ON stored.revision_key = $12 AND stored.candidate_digest = $13
      AND stored.source_hash = $14 AND stored.destination_hash = $15
      AND stored.transform_order = x.transform_order AND stored.segment_ordinal = x.segment_ordinal)
    = (SELECT count(*) FROM items);

-- name: RecordConversionQualityReport :one
WITH admission AS (
    SELECT a.id AS id, a.country_code AS country_code FROM ingest.country_admission a
    WHERE a.id = 1 AND a.admission_epoch = $1 AND a.admitted_writer = $2 AND a.enabled FOR SHARE
), lease AS MATERIALIZED (
    SELECT l.id AS id, l.expires_at AS expires_at, l.owner AS owner,
        l.attempt_fence AS attempt_fence, l.admission_epoch AS admission_epoch, l.cancelled_at AS cancelled_at
    FROM ingest.execution_lease l CROSS JOIN admission
    WHERE l.resource_kind = $3 AND l.resource_key = $4 FOR UPDATE OF l
), current_execution AS (
    SELECT e.id AS id, e.execution_key AS execution_key, lease.expires_at AS expires_at,
        admission.country_code AS country_code
    FROM ingest.execution e CROSS JOIN lease CROSS JOIN admission
    WHERE $3::text = 'document_version' AND lease.expires_at > clock_timestamp()
      AND lease.owner = $5::text AND lease.attempt_fence = $6::bigint
      AND lease.admission_epoch = $1::bigint AND lease.cancelled_at IS NULL
      AND e.id = $7 AND e.scope_kind = 'intake' AND e.resource_kind = $3 AND e.resource_key = $4
      AND e.attempt_fence = $6 AND e.admission_epoch = $1
      AND e.input_digest = $8 AND e.recipe_digest = $9 AND e.budget_digest = $10
), parent AS (
    SELECT NULLIF($11::bigint, 0) AS id, $12::text AS revision_key FROM current_execution c
    WHERE split_part($12::text, ':', 1) = c.country_code AND c.expires_at > clock_timestamp()
      AND ((NULLIF($11::bigint, 0) IS NOT NULL AND EXISTS (
          SELECT 1 FROM silver.document_revision r WHERE r.id = NULLIF($11::bigint, 0) AND r.revision_key = $12
            AND r.document_version_key = $4::text
      )) OR (NULLIF($11::bigint, 0) IS NULL AND NOT EXISTS (
          SELECT 1 FROM silver.document_revision r WHERE r.revision_key = $12
      )))
), items AS MATERIALIZED (
    SELECT ($14::jsonb->>'report_hash')::text AS report_hash,
        ($14::jsonb->>'report_byte_size')::bigint AS report_byte_size,
        ($14::jsonb->>'artifact_hash')::text AS artifact_hash,
        ($14::jsonb->>'policy_digest')::text AS policy_digest,
        ($14::jsonb->>'threshold_version')::text AS threshold_version,
        ($14::jsonb->>'outcome')::text AS outcome,
        jsonb_array_length(($14::jsonb)->'records')::integer AS record_count
), records AS MATERIALIZED (
    SELECT (j.value->>'ordinal')::integer AS ordinal,
        (j.value->>'record_kind')::text AS record_kind,
        (j.value->>'code')::text AS code,
        (j.value->>'severity')::text AS severity,
        (j.value->>'scope_kind')::text AS scope_kind,
        (j.value->>'scope_key')::text AS scope_key,
        (j.value->>'reason')::text AS reason,
        (j.value->>'metric_kind')::text AS metric_kind,
        (j.value->>'metric_state')::text AS metric_state,
        (j.value->>'numerator')::bigint AS numerator,
        (j.value->>'denominator')::bigint AS denominator,
        (j.value->>'threshold')::text AS threshold,
        (j.value->>'mandatory')::boolean AS mandatory,
        (j.value->>'passed')::boolean AS passed,
        (j.value->'evidence')::jsonb AS evidence,
        (j.value->'source_spans')::jsonb AS source_spans
    FROM jsonb_array_elements(($14::jsonb)->'records') AS j(value)
), eligible AS (
    SELECT parent_row.id AS id, parent_row.revision_key AS revision_key FROM parent parent_row CROSS JOIN items x
    JOIN bronze.blob report_blob ON report_blob.sha256 = x.report_hash AND report_blob.byte_size = x.report_byte_size
    WHERE (x.artifact_hash IS NULL OR EXISTS (SELECT 1 FROM bronze.blob artifact_blob WHERE artifact_blob.sha256 = x.artifact_hash))
      AND jsonb_typeof(($14::jsonb)->'records') = 'array'
      AND (SELECT count(*) = count(DISTINCT entry.ordinal) FROM records entry)
      AND NOT EXISTS (SELECT 1 FROM records entry WHERE entry.ordinal < 0 OR entry.ordinal >= x.record_count)
      AND NOT EXISTS (SELECT 1 FROM silver.quality_report stored
          WHERE stored.revision_key = $12 AND stored.candidate_digest = $13 AND stored.report_hash = x.report_hash
            AND (stored.document_version_key <> $4::text OR ROW(stored.report_hash, stored.report_byte_size, stored.artifact_hash, stored.policy_digest, stored.threshold_version, stored.outcome, stored.record_count) IS DISTINCT FROM ROW(x.report_hash, x.report_byte_size, x.artifact_hash, x.policy_digest, x.threshold_version, x.outcome, x.record_count)
              OR (SELECT count(*) FROM silver.quality_finding existing WHERE existing.quality_report_id = stored.id)
                 <> (SELECT count(*) FROM records)
              OR EXISTS (SELECT 1 FROM records entry LEFT JOIN silver.quality_finding existing
                  ON existing.quality_report_id = stored.id AND existing.ordinal = entry.ordinal
                  WHERE existing.id IS NULL OR ROW(existing.ordinal, existing.record_kind, existing.code, existing.severity, existing.scope_kind, existing.scope_key, existing.reason, existing.metric_kind, existing.metric_state, existing.numerator, existing.denominator, existing.threshold, existing.mandatory, existing.passed, existing.evidence, existing.source_spans) IS DISTINCT FROM ROW(entry.ordinal, entry.record_kind, entry.code, entry.severity, entry.scope_kind, entry.scope_key, entry.reason, entry.metric_kind, entry.metric_state, entry.numerator, entry.denominator, entry.threshold, entry.mandatory, entry.passed, entry.evidence, entry.source_spans))))
), inserted AS (
    INSERT INTO silver.quality_report (document_revision_id, revision_key, document_version_key, processing_execution_key, candidate_digest, report_hash, report_byte_size, artifact_hash, policy_digest, threshold_version, outcome, record_count)
    SELECT eligible_row.id, eligible_row.revision_key, $4::text, c.execution_key, $13::text, x.report_hash, x.report_byte_size, x.artifact_hash, x.policy_digest, x.threshold_version, x.outcome, x.record_count FROM eligible eligible_row CROSS JOIN items x CROSS JOIN current_execution c
    WHERE c.expires_at > clock_timestamp() ON CONFLICT DO NOTHING
    RETURNING id AS id
), findings AS (
    INSERT INTO silver.quality_finding (document_revision_id, revision_key, document_version_key, processing_execution_key, candidate_digest, ordinal, record_kind, code, severity, scope_kind, scope_key, reason, metric_kind, metric_state, numerator, denominator, threshold, mandatory, passed, evidence, source_spans, quality_report_id, report_hash)
    SELECT eligible_row.id, eligible_row.revision_key, $4::text, c.execution_key, $13::text, entry.ordinal, entry.record_kind, entry.code, entry.severity, entry.scope_kind, entry.scope_key, entry.reason, entry.metric_kind, entry.metric_state, entry.numerator, entry.denominator, entry.threshold, entry.mandatory, entry.passed, entry.evidence, entry.source_spans, fresh.id, x.report_hash
    FROM inserted fresh CROSS JOIN eligible eligible_row CROSS JOIN items x CROSS JOIN records entry CROSS JOIN current_execution c
    RETURNING id AS id
)
SELECT fresh.id AS id, (SELECT count(*) FROM findings) AS finding_count FROM inserted fresh
UNION ALL
SELECT stored.id AS id, (SELECT count(*) FROM records) AS finding_count
FROM silver.quality_report stored CROSS JOIN eligible eligible_row CROSS JOIN items x
WHERE stored.revision_key = $12 AND stored.candidate_digest = $13 AND stored.report_hash = x.report_hash
  AND NOT EXISTS (SELECT 1 FROM inserted fresh);

-- name: RecordRevisionComparison :one
WITH admission AS (
    SELECT a.id AS id, a.country_code AS country_code FROM ingest.country_admission a
    WHERE a.id = 1 AND a.admission_epoch = $1::bigint AND a.admitted_writer = $2::text AND a.enabled FOR SHARE
), lease AS MATERIALIZED (
    SELECT l.id AS id, l.expires_at AS expires_at, l.owner AS owner,
        l.attempt_fence AS attempt_fence, l.admission_epoch AS admission_epoch, l.cancelled_at AS cancelled_at
    FROM ingest.execution_lease l CROSS JOIN admission
    WHERE l.resource_kind = $3::text AND l.resource_key = $4::text FOR UPDATE OF l
), current_execution AS (
    SELECT e.id AS id, e.execution_key AS execution_key, lease.expires_at AS expires_at,
        admission.country_code AS country_code
    FROM ingest.execution e CROSS JOIN lease CROSS JOIN admission
    WHERE $3::text = 'document_version' AND lease.expires_at > clock_timestamp()
      AND lease.owner = $5::text AND lease.attempt_fence = $6::bigint
      AND lease.admission_epoch = $1::bigint AND lease.cancelled_at IS NULL
      AND e.id = $7::bigint AND e.scope_kind = 'intake' AND e.resource_kind = $3::text AND e.resource_key = $4::text
      AND e.attempt_fence = $6::bigint AND e.admission_epoch = $1::bigint
      AND e.input_digest = $8::text AND e.recipe_digest = $9::text AND e.budget_digest = $10::text
), parent AS (
    SELECT NULLIF($11::bigint, 0) AS id, $12::text AS revision_key FROM current_execution c
    WHERE split_part($12::text, ':', 1) = c.country_code AND c.expires_at > clock_timestamp()
      AND ((NULLIF($11::bigint, 0) IS NOT NULL AND EXISTS (
          SELECT 1 FROM silver.document_revision r WHERE r.id = NULLIF($11::bigint, 0) AND r.revision_key = $12::text
            AND r.document_version_key = $4::text
      )) OR (NULLIF($11::bigint, 0) IS NULL AND NOT EXISTS (
          SELECT 1 FROM silver.document_revision r WHERE r.revision_key = $12::text
      )))
), items AS MATERIALIZED (
    SELECT ($14::jsonb->>'prior_report_id')::bigint AS prior_report_id,
        ($14::jsonb->>'candidate_report_id')::bigint AS candidate_report_id,
        ($14::jsonb->>'prior_revision_key')::text AS prior_revision_key,
        ($14::jsonb->>'prior_candidate_digest')::text AS prior_candidate_digest,
        ($14::jsonb->>'comparison_recipe_digest')::text AS comparison_recipe_digest,
        ($14::jsonb->>'result_hash')::text AS result_hash,
        ($14::jsonb->>'result_byte_size')::bigint AS result_byte_size,
        ($14::jsonb->>'acceptance_watermark')::bigint AS acceptance_watermark
), eligible AS (
    SELECT parent_row.id AS id, parent_row.revision_key AS revision_key FROM parent parent_row CROSS JOIN items x
    JOIN silver.quality_report prior_report ON prior_report.id = x.prior_report_id
    JOIN silver.quality_report candidate_report ON candidate_report.id = x.candidate_report_id
    JOIN bronze.blob result_blob ON result_blob.sha256 = x.result_hash AND result_blob.byte_size = x.result_byte_size
    WHERE prior_report.revision_key = x.prior_revision_key AND prior_report.candidate_digest = x.prior_candidate_digest
      AND candidate_report.revision_key = $12::text AND candidate_report.candidate_digest = $13::text
      AND candidate_report.document_version_key = $4::text
      AND (candidate_report.document_revision_id IS NULL OR candidate_report.document_revision_id = parent_row.id)
      AND split_part(prior_report.revision_key, ':rev:', 1) = split_part(parent_row.revision_key, ':rev:', 1)
      AND x.acceptance_watermark >= 0
      AND NOT EXISTS (SELECT 1 FROM ingest.revision_acceptance newer_event
          JOIN ingest.revision_acceptance_state newer_state
            ON newer_state.acceptance_id = newer_event.id
           AND newer_state.revision_key = newer_event.revision_key
           AND newer_state.processing_attempt_key = newer_event.processing_attempt_key
           AND newer_state.state = newer_event.state
           AND newer_state.watermark = newer_event.watermark
          WHERE newer_state.state IN ('accepted', 'accepted_with_gaps')
            AND split_part(newer_event.revision_key, ':rev:', 1) = split_part(parent_row.revision_key, ':rev:', 1)
            AND newer_event.watermark > x.acceptance_watermark
            AND newer_event.candidate_digest NOT IN (x.prior_candidate_digest, $13::text))
      AND (EXISTS (SELECT 1 FROM silver.revision_comparison replay_baseline
          WHERE replay_baseline.prior_candidate_digest = x.prior_candidate_digest
            AND replay_baseline.candidate_digest = $13::text
            AND replay_baseline.comparison_recipe_digest = x.comparison_recipe_digest)
        OR (x.acceptance_watermark = 0 AND NOT EXISTS (
          SELECT 1 FROM ingest.revision_acceptance initial_event
          JOIN ingest.revision_acceptance_state initial_state
            ON initial_state.acceptance_id = initial_event.id
           AND initial_state.revision_key = initial_event.revision_key
           AND initial_state.processing_attempt_key = initial_event.processing_attempt_key
           AND initial_state.state = initial_event.state
           AND initial_state.watermark = initial_event.watermark
          WHERE initial_state.state IN ('accepted', 'accepted_with_gaps')
            AND split_part(initial_event.revision_key, ':rev:', 1) = split_part(parent_row.revision_key, ':rev:', 1)))
        OR EXISTS (SELECT 1 FROM ingest.revision_acceptance baseline_event
          JOIN ingest.revision_acceptance_state baseline_state
            ON baseline_state.acceptance_id = baseline_event.id
           AND baseline_state.revision_key = baseline_event.revision_key
           AND baseline_state.processing_attempt_key = baseline_event.processing_attempt_key
           AND baseline_state.state = baseline_event.state
           AND baseline_state.watermark = baseline_event.watermark
          WHERE baseline_state.state IN ('accepted', 'accepted_with_gaps')
            AND baseline_event.revision_key = x.prior_revision_key
            AND baseline_event.candidate_digest = x.prior_candidate_digest
            AND baseline_event.watermark = x.acceptance_watermark))
      AND NOT EXISTS (SELECT 1 FROM silver.revision_comparison stored
          WHERE stored.prior_candidate_digest = x.prior_candidate_digest AND stored.candidate_digest = $13::text
            AND stored.comparison_recipe_digest = x.comparison_recipe_digest
            AND (stored.acceptance_watermark <> x.acceptance_watermark OR stored.revision_key <> $12::text OR stored.document_version_key <> $4::text
                OR ROW(stored.prior_report_id, stored.candidate_report_id, stored.prior_revision_key, stored.prior_candidate_digest, stored.comparison_recipe_digest, stored.result_hash, stored.result_byte_size) IS DISTINCT FROM ROW(x.prior_report_id, x.candidate_report_id, x.prior_revision_key, x.prior_candidate_digest, x.comparison_recipe_digest, x.result_hash, x.result_byte_size)))
), inserted AS (
    INSERT INTO silver.revision_comparison (document_revision_id, revision_key, document_version_key, processing_execution_key, candidate_digest, prior_report_id, candidate_report_id, prior_revision_key, prior_candidate_digest, comparison_recipe_digest, result_hash, result_byte_size, acceptance_watermark)
    SELECT eligible_row.id, eligible_row.revision_key, $4::text, c.execution_key, $13::text, x.prior_report_id, x.candidate_report_id, x.prior_revision_key, x.prior_candidate_digest, x.comparison_recipe_digest, x.result_hash, x.result_byte_size,
        x.acceptance_watermark
    FROM eligible eligible_row CROSS JOIN items x CROSS JOIN current_execution c
    WHERE c.expires_at > clock_timestamp()
      AND NOT EXISTS (SELECT 1 FROM silver.revision_comparison existing_comparison
          WHERE existing_comparison.prior_candidate_digest = x.prior_candidate_digest
            AND existing_comparison.candidate_digest = $13::text
            AND existing_comparison.comparison_recipe_digest = x.comparison_recipe_digest)
    ON CONFLICT DO NOTHING RETURNING id AS id
)
SELECT fresh.id AS id FROM inserted fresh
UNION ALL
SELECT stored.id AS id FROM silver.revision_comparison stored CROSS JOIN eligible eligible_row CROSS JOIN items x
WHERE stored.prior_candidate_digest = x.prior_candidate_digest AND stored.candidate_digest = $13::text
  AND stored.comparison_recipe_digest = x.comparison_recipe_digest AND NOT EXISTS (SELECT 1 FROM ingest.revision_acceptance replay_event
          JOIN ingest.revision_acceptance_state replay_state
            ON replay_state.acceptance_id = replay_event.id
           AND replay_state.revision_key = replay_event.revision_key
           AND replay_state.processing_attempt_key = replay_event.processing_attempt_key
           AND replay_state.state = replay_event.state
           AND replay_state.watermark = replay_event.watermark
          WHERE replay_state.state IN ('accepted', 'accepted_with_gaps')
            AND split_part(replay_event.revision_key, ':rev:', 1) = split_part(stored.revision_key, ':rev:', 1)
            AND replay_event.watermark > stored.acceptance_watermark
            AND replay_event.candidate_digest NOT IN (stored.prior_candidate_digest, stored.candidate_digest))
  AND NOT EXISTS (SELECT 1 FROM inserted fresh);

-- name: ReadConversionArtifacts :many
SELECT stored.id AS id,
    stored.document_revision_id AS document_revision_id,
    stored.revision_key AS revision_key,
    stored.document_version_key AS document_version_key,
    stored.processing_execution_key AS processing_execution_key,
    stored.candidate_digest AS candidate_digest,
    stored.role AS role,
    stored.ordinal AS ordinal,
    stored.blob_hash AS blob_hash,
    stored.byte_size AS byte_size,
    stored.schema_version AS schema_version,
    stored.original_hash AS original_hash,
    stored.conversion_recipe_digest AS conversion_recipe_digest,
    stored.normalization_recipe_digest AS normalization_recipe_digest,
    stored.detector_digest AS detector_digest,
    stored.manifest_hash AS manifest_hash,
    stored.manifest_byte_size AS manifest_byte_size
FROM silver.conversion_artifact stored WHERE stored.revision_key = $1 AND stored.role = $2 ORDER BY stored.candidate_digest, stored.ordinal;

-- name: ReadOffsetMapSegments :many
SELECT stored.id AS id,
    stored.document_revision_id AS document_revision_id,
    stored.revision_key AS revision_key,
    stored.document_version_key AS document_version_key,
    stored.processing_execution_key AS processing_execution_key,
    stored.candidate_digest AS candidate_digest,
    stored.map_hash AS map_hash,
    stored.source_hash AS source_hash,
    stored.destination_hash AS destination_hash,
    stored.source_block_id AS source_block_id,
    stored.destination_block_id AS destination_block_id,
    stored.source_space AS source_space,
    stored.destination_space AS destination_space,
    stored.source_start AS source_start,
    stored.source_end AS source_end,
    stored.destination_start AS destination_start,
    stored.destination_end AS destination_end,
    stored.transform_order AS transform_order,
    stored.segment_ordinal AS segment_ordinal,
    stored.transform AS transform,
    stored.source_page AS source_page,
    stored.source_object AS source_object,
    stored.source_member AS source_member,
    stored.source_reason AS source_reason,
    stored.destination_page AS destination_page,
    stored.destination_object AS destination_object,
    stored.destination_member AS destination_member,
    stored.destination_reason AS destination_reason
FROM silver.offset_map_segment stored WHERE stored.revision_key = $1 AND (stored.source_block_id = $2 OR stored.destination_block_id = $2) ORDER BY stored.transform_order, stored.segment_ordinal;

-- name: ReadEditLedgerByHashes :many
SELECT stored.id AS id,
    stored.document_revision_id AS document_revision_id,
    stored.revision_key AS revision_key,
    stored.document_version_key AS document_version_key,
    stored.processing_execution_key AS processing_execution_key,
    stored.candidate_digest AS candidate_digest,
    stored.ledger_hash AS ledger_hash,
    stored.operation_ordinal AS operation_ordinal,
    stored.operation AS operation,
    stored.rule AS rule,
    stored.rule_version AS rule_version,
    stored.input_hash AS input_hash,
    stored.output_hash AS output_hash,
    stored.input_start AS input_start,
    stored.input_end AS input_end,
    stored.output_start AS output_start,
    stored.output_end AS output_end,
    stored.removed_bytes AS removed_bytes,
    stored.replacement_bytes AS replacement_bytes,
    stored.encoding_evidence AS encoding_evidence,
    stored.font_evidence AS font_evidence,
    stored.source_anchors AS source_anchors
FROM silver.edit_ledger stored WHERE stored.revision_key = $1 AND stored.candidate_digest = $2 AND stored.input_hash = $3 AND stored.output_hash = $4 ORDER BY stored.operation_ordinal;

-- name: ReadConversionQualityReports :many
SELECT stored.id AS id,
    stored.document_revision_id AS document_revision_id,
    stored.revision_key AS revision_key,
    stored.document_version_key AS document_version_key,
    stored.processing_execution_key AS processing_execution_key,
    stored.candidate_digest AS candidate_digest,
    stored.report_hash AS report_hash,
    stored.report_byte_size AS report_byte_size,
    stored.artifact_hash AS artifact_hash,
    stored.policy_digest AS policy_digest,
    stored.threshold_version AS threshold_version,
    stored.outcome AS outcome,
    stored.record_count AS record_count
FROM silver.quality_report stored WHERE stored.revision_key = $1 AND stored.candidate_digest = $2 ORDER BY stored.id;

-- name: ReadConversionQualityFindings :many
SELECT stored.id AS id,
    stored.document_revision_id AS document_revision_id,
    stored.revision_key AS revision_key,
    stored.document_version_key AS document_version_key,
    stored.processing_execution_key AS processing_execution_key,
    stored.candidate_digest AS candidate_digest,
    stored.quality_report_id AS quality_report_id,
    stored.report_hash AS report_hash,
    stored.ordinal AS ordinal,
    stored.record_kind AS record_kind,
    stored.code AS code,
    stored.severity AS severity,
    stored.scope_kind AS scope_kind,
    stored.scope_key AS scope_key,
    stored.reason AS reason,
    stored.metric_kind AS metric_kind,
    stored.metric_state AS metric_state,
    stored.numerator AS numerator,
    stored.denominator AS denominator,
    stored.threshold AS threshold,
    stored.mandatory AS mandatory,
    stored.passed AS passed,
    stored.evidence AS evidence,
    stored.source_spans AS source_spans
FROM silver.quality_finding stored WHERE stored.quality_report_id = $1 ORDER BY stored.ordinal;

-- name: ReadRevisionComparisons :many
SELECT stored.id AS id,
    stored.document_revision_id AS document_revision_id,
    stored.revision_key AS revision_key,
    stored.document_version_key AS document_version_key,
    stored.processing_execution_key AS processing_execution_key,
    stored.candidate_digest AS candidate_digest,
    stored.prior_report_id AS prior_report_id,
    stored.candidate_report_id AS candidate_report_id,
    stored.prior_revision_key AS prior_revision_key,
    stored.prior_candidate_digest AS prior_candidate_digest,
    stored.comparison_recipe_digest AS comparison_recipe_digest,
    stored.result_hash AS result_hash,
    stored.result_byte_size AS result_byte_size,
    stored.acceptance_watermark AS acceptance_watermark
FROM silver.revision_comparison stored
WHERE stored.prior_candidate_digest = $1::text AND stored.candidate_digest = $2::text
  AND NOT EXISTS (SELECT 1 FROM ingest.revision_acceptance read_event
          JOIN ingest.revision_acceptance_state read_state
            ON read_state.acceptance_id = read_event.id
           AND read_state.revision_key = read_event.revision_key
           AND read_state.processing_attempt_key = read_event.processing_attempt_key
           AND read_state.state = read_event.state
           AND read_state.watermark = read_event.watermark
          WHERE read_state.state IN ('accepted', 'accepted_with_gaps')
            AND split_part(read_event.revision_key, ':rev:', 1) = split_part(stored.revision_key, ':rev:', 1)
            AND read_event.watermark > stored.acceptance_watermark
            AND read_event.candidate_digest NOT IN (stored.prior_candidate_digest, stored.candidate_digest))
ORDER BY stored.id;

-- Exact immutable receipts include stale comparisons for conflict classification.
-- RecordLimit bounds the report child projection. The adapter also compares
-- record_count, so an over-limit stored report cannot masquerade as a replay.
-- name: ReadConversionReceipt :one
SELECT receipt_row.id AS id, receipt_row.payload::jsonb AS payload FROM (
    SELECT artifact_receipt.id AS id, to_jsonb(artifact_receipt) AS payload
    FROM silver.conversion_artifact artifact_receipt
    WHERE sqlc.arg(record_kind)::text = 'artifact'
      AND artifact_receipt.revision_key = sqlc.arg(revision_key)::text
      AND artifact_receipt.candidate_digest = sqlc.arg(candidate_digest)::text
      AND artifact_receipt.role = sqlc.arg(receipt_key)::jsonb->>'role'
      AND artifact_receipt.ordinal = (sqlc.arg(receipt_key)::jsonb->>'ordinal')::integer
    UNION ALL
    SELECT ledger_receipt.id AS id, to_jsonb(ledger_receipt) || jsonb_build_object(
        'removed_bytes', encode(ledger_receipt.removed_bytes, 'hex'),
        'replacement_bytes', encode(ledger_receipt.replacement_bytes, 'hex')) AS payload
    FROM silver.edit_ledger ledger_receipt
    WHERE sqlc.arg(record_kind)::text = 'ledger'
      AND ledger_receipt.revision_key = sqlc.arg(revision_key)::text
      AND ledger_receipt.candidate_digest = sqlc.arg(candidate_digest)::text
      AND ledger_receipt.ledger_hash = sqlc.arg(receipt_key)::jsonb->>'ledger_hash'
      AND ledger_receipt.operation_ordinal = (sqlc.arg(receipt_key)::jsonb->>'operation_ordinal')::integer
    UNION ALL
    SELECT report_receipt.id AS id, to_jsonb(report_receipt) || jsonb_build_object('records',
        COALESCE((SELECT jsonb_agg(to_jsonb(child_receipt) ORDER BY child_receipt.ordinal)
            FROM (SELECT finding_receipt.* FROM silver.quality_finding finding_receipt
                WHERE finding_receipt.quality_report_id = report_receipt.id
                ORDER BY finding_receipt.ordinal LIMIT sqlc.arg(record_limit)::integer) child_receipt), '[]'::jsonb)) AS payload
    FROM silver.quality_report report_receipt
    WHERE sqlc.arg(record_kind)::text = 'report'
      AND report_receipt.revision_key = sqlc.arg(revision_key)::text
      AND report_receipt.candidate_digest = sqlc.arg(candidate_digest)::text
      AND report_receipt.report_hash = sqlc.arg(receipt_key)::jsonb->>'report_hash'
    UNION ALL
    SELECT comparison_receipt.id AS id, to_jsonb(comparison_receipt) AS payload
    FROM silver.revision_comparison comparison_receipt
    WHERE sqlc.arg(record_kind)::text = 'comparison'
      AND comparison_receipt.prior_candidate_digest = sqlc.arg(receipt_key)::jsonb->>'prior_candidate_digest'
      AND comparison_receipt.candidate_digest = sqlc.arg(candidate_digest)::text
      AND comparison_receipt.comparison_recipe_digest = sqlc.arg(receipt_key)::jsonb->>'comparison_recipe_digest'
) receipt_row;

-- Exact requested segment keys bound this read by the validated batch size.
-- name: ReadConversionSegmentReceipts :many
SELECT segment_receipt.id AS id, to_jsonb(segment_receipt)::jsonb AS payload
FROM silver.offset_map_segment segment_receipt
WHERE segment_receipt.revision_key = sqlc.arg(revision_key)::text
  AND segment_receipt.candidate_digest = sqlc.arg(candidate_digest)::text
  AND segment_receipt.source_hash = sqlc.arg(source_hash)::text
  AND segment_receipt.destination_hash = sqlc.arg(destination_hash)::text
  AND EXISTS (SELECT 1 FROM jsonb_array_elements(sqlc.arg(segment_keys)::jsonb) requested_segment(value)
      WHERE segment_receipt.transform_order = (requested_segment.value->>'transform_order')::integer
        AND segment_receipt.segment_ordinal = (requested_segment.value->>'segment_ordinal')::integer);

-- name: ReadConversionArtifactsPage :many
SELECT stored.id AS id,
    stored.document_revision_id AS document_revision_id,
    stored.revision_key AS revision_key,
    stored.document_version_key AS document_version_key,
    stored.processing_execution_key AS processing_execution_key,
    stored.candidate_digest AS candidate_digest,
    stored.role AS role,
    stored.ordinal AS ordinal,
    stored.blob_hash AS blob_hash,
    stored.byte_size AS byte_size,
    stored.schema_version AS schema_version,
    stored.original_hash AS original_hash,
    stored.conversion_recipe_digest AS conversion_recipe_digest,
    stored.normalization_recipe_digest AS normalization_recipe_digest,
    stored.detector_digest AS detector_digest,
    stored.manifest_hash AS manifest_hash,
    stored.manifest_byte_size AS manifest_byte_size
FROM silver.conversion_artifact stored WHERE stored.revision_key = sqlc.arg(revision_key)::text AND stored.role = sqlc.arg(role)::text AND stored.document_version_key = sqlc.arg(document_version_key)::text AND stored.id > sqlc.arg(after_id)::bigint ORDER BY stored.id LIMIT sqlc.arg(page_limit)::integer;

-- name: ReadOffsetMapSegmentsPage :many
SELECT stored.id AS id,
    stored.document_revision_id AS document_revision_id,
    stored.revision_key AS revision_key,
    stored.document_version_key AS document_version_key,
    stored.processing_execution_key AS processing_execution_key,
    stored.candidate_digest AS candidate_digest,
    stored.map_hash AS map_hash,
    stored.source_hash AS source_hash,
    stored.destination_hash AS destination_hash,
    stored.source_block_id AS source_block_id,
    stored.destination_block_id AS destination_block_id,
    stored.source_space AS source_space,
    stored.destination_space AS destination_space,
    stored.source_start AS source_start,
    stored.source_end AS source_end,
    stored.destination_start AS destination_start,
    stored.destination_end AS destination_end,
    stored.transform_order AS transform_order,
    stored.segment_ordinal AS segment_ordinal,
    stored.transform AS transform,
    stored.source_page AS source_page,
    stored.source_object AS source_object,
    stored.source_member AS source_member,
    stored.source_reason AS source_reason,
    stored.destination_page AS destination_page,
    stored.destination_object AS destination_object,
    stored.destination_member AS destination_member,
    stored.destination_reason AS destination_reason
FROM silver.offset_map_segment stored WHERE stored.revision_key = sqlc.arg(revision_key)::text AND (stored.source_block_id = sqlc.arg(block_id)::text OR stored.destination_block_id = sqlc.arg(block_id)::text) AND stored.document_version_key = sqlc.arg(document_version_key)::text AND stored.id > sqlc.arg(after_id)::bigint ORDER BY stored.id LIMIT sqlc.arg(page_limit)::integer;

-- name: ReadEditLedgerByHashesPage :many
SELECT stored.id AS id,
    stored.document_revision_id AS document_revision_id,
    stored.revision_key AS revision_key,
    stored.document_version_key AS document_version_key,
    stored.processing_execution_key AS processing_execution_key,
    stored.candidate_digest AS candidate_digest,
    stored.ledger_hash AS ledger_hash,
    stored.operation_ordinal AS operation_ordinal,
    stored.operation AS operation,
    stored.rule AS rule,
    stored.rule_version AS rule_version,
    stored.input_hash AS input_hash,
    stored.output_hash AS output_hash,
    stored.input_start AS input_start,
    stored.input_end AS input_end,
    stored.output_start AS output_start,
    stored.output_end AS output_end,
    stored.removed_bytes AS removed_bytes,
    stored.replacement_bytes AS replacement_bytes,
    stored.encoding_evidence AS encoding_evidence,
    stored.font_evidence AS font_evidence,
    stored.source_anchors AS source_anchors
FROM silver.edit_ledger stored WHERE stored.revision_key = sqlc.arg(revision_key)::text AND stored.candidate_digest = sqlc.arg(candidate_digest)::text AND stored.input_hash = sqlc.arg(input_hash)::text AND stored.output_hash = sqlc.arg(output_hash)::text AND stored.document_version_key = sqlc.arg(document_version_key)::text AND stored.id > sqlc.arg(after_id)::bigint ORDER BY stored.id LIMIT sqlc.arg(page_limit)::integer;

-- name: ReadConversionQualityReportsPage :many
SELECT stored.id AS id,
    stored.document_revision_id AS document_revision_id,
    stored.revision_key AS revision_key,
    stored.document_version_key AS document_version_key,
    stored.processing_execution_key AS processing_execution_key,
    stored.candidate_digest AS candidate_digest,
    stored.report_hash AS report_hash,
    stored.report_byte_size AS report_byte_size,
    stored.artifact_hash AS artifact_hash,
    stored.policy_digest AS policy_digest,
    stored.threshold_version AS threshold_version,
    stored.outcome AS outcome,
    stored.record_count AS record_count
FROM silver.quality_report stored WHERE stored.revision_key = sqlc.arg(revision_key)::text AND stored.candidate_digest = sqlc.arg(candidate_digest)::text AND stored.document_version_key = sqlc.arg(document_version_key)::text AND stored.id > sqlc.arg(after_id)::bigint ORDER BY stored.id LIMIT sqlc.arg(page_limit)::integer;

-- name: ReadConversionQualityFindingsPage :many
SELECT stored.id AS id,
    stored.document_revision_id AS document_revision_id,
    stored.revision_key AS revision_key,
    stored.document_version_key AS document_version_key,
    stored.processing_execution_key AS processing_execution_key,
    stored.candidate_digest AS candidate_digest,
    stored.quality_report_id AS quality_report_id,
    stored.report_hash AS report_hash,
    stored.ordinal AS ordinal,
    stored.record_kind AS record_kind,
    stored.code AS code,
    stored.severity AS severity,
    stored.scope_kind AS scope_kind,
    stored.scope_key AS scope_key,
    stored.reason AS reason,
    stored.metric_kind AS metric_kind,
    stored.metric_state AS metric_state,
    stored.numerator AS numerator,
    stored.denominator AS denominator,
    stored.threshold AS threshold,
    stored.mandatory AS mandatory,
    stored.passed AS passed,
    stored.evidence AS evidence,
    stored.source_spans AS source_spans
FROM silver.quality_finding stored WHERE stored.quality_report_id = sqlc.arg(quality_report_id)::bigint AND stored.revision_key = sqlc.arg(revision_key)::text AND stored.candidate_digest = sqlc.arg(candidate_digest)::text AND stored.document_version_key = sqlc.arg(document_version_key)::text AND stored.id > sqlc.arg(after_id)::bigint ORDER BY stored.id LIMIT sqlc.arg(page_limit)::integer;

-- name: ReadRevisionComparisonsPage :many
SELECT stored.id AS id,
    stored.document_revision_id AS document_revision_id,
    stored.revision_key AS revision_key,
    stored.document_version_key AS document_version_key,
    stored.processing_execution_key AS processing_execution_key,
    stored.candidate_digest AS candidate_digest,
    stored.prior_report_id AS prior_report_id,
    stored.candidate_report_id AS candidate_report_id,
    stored.prior_revision_key AS prior_revision_key,
    stored.prior_candidate_digest AS prior_candidate_digest,
    stored.comparison_recipe_digest AS comparison_recipe_digest,
    stored.result_hash AS result_hash,
    stored.result_byte_size AS result_byte_size,
    stored.acceptance_watermark AS acceptance_watermark
FROM silver.revision_comparison stored
WHERE stored.prior_candidate_digest = sqlc.arg(prior_candidate_digest)::text AND stored.candidate_digest = sqlc.arg(candidate_digest)::text
  AND NOT EXISTS (SELECT 1 FROM ingest.revision_acceptance read_event
          JOIN ingest.revision_acceptance_state read_state
            ON read_state.acceptance_id = read_event.id
           AND read_state.revision_key = read_event.revision_key
           AND read_state.processing_attempt_key = read_event.processing_attempt_key
           AND read_state.state = read_event.state
           AND read_state.watermark = read_event.watermark
          WHERE read_state.state IN ('accepted', 'accepted_with_gaps')
            AND split_part(read_event.revision_key, ':rev:', 1) = split_part(stored.revision_key, ':rev:', 1)
            AND read_event.watermark > stored.acceptance_watermark
            AND read_event.candidate_digest NOT IN (stored.prior_candidate_digest, stored.candidate_digest))
AND stored.document_version_key = sqlc.arg(document_version_key)::text AND stored.id > sqlc.arg(after_id)::bigint ORDER BY stored.id LIMIT sqlc.arg(page_limit)::integer;

-- The adapter bounds the input list and verifies every hash, size and object ref.
-- name: ReadConversionBlobReferences :many
SELECT referenced_blob.sha256 AS sha256, referenced_blob.byte_size AS byte_size,
    referenced_blob.object_ref AS object_ref
FROM bronze.blob referenced_blob
WHERE EXISTS (SELECT 1 FROM jsonb_array_elements(sqlc.arg(blob_references)::jsonb) requested_blob(value)
    WHERE referenced_blob.sha256 = requested_blob.value->>'hash');

-- Exact replay eligibility uses the same derived staleness as the digest page.
-- Immutable payload classification happens before this query in the adapter.
-- name: ReadConversionComparisonEligibility :one
SELECT EXISTS (
    SELECT 1 FROM silver.revision_comparison eligible_comparison
    WHERE eligible_comparison.id = sqlc.arg(comparison_id)::bigint
      AND NOT EXISTS (
          SELECT 1 FROM ingest.revision_acceptance eligible_event
          JOIN ingest.revision_acceptance_state eligible_state
            ON eligible_state.acceptance_id = eligible_event.id
           AND eligible_state.revision_key = eligible_event.revision_key
           AND eligible_state.processing_attempt_key = eligible_event.processing_attempt_key
           AND eligible_state.state = eligible_event.state
           AND eligible_state.watermark = eligible_event.watermark
          WHERE eligible_state.state IN ('accepted', 'accepted_with_gaps')
            AND split_part(eligible_event.revision_key, ':rev:', 1) = split_part(eligible_comparison.revision_key, ':rev:', 1)
            AND eligible_event.watermark > eligible_comparison.acceptance_watermark
            AND eligible_event.candidate_digest NOT IN (eligible_comparison.prior_candidate_digest, eligible_comparison.candidate_digest)
      )
) AS eligible;
