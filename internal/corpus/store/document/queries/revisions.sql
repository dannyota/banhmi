-- Every mutation runs in the owning adapter transaction. Parameters 1-10 are
-- epoch, writer, resource kind, document version, owner, fence, execution ID,
-- input digest, recipe digest and budget digest. Lock admission before lease;
-- materialize the lease before evaluating expiry. Only document_version owns
-- these writes. Cross-schema references are validated here, never by FKs.
-- An empty bulk result requires the phase 2 adapter to roll back the owning
-- transaction before any fresh read or retry, because partial writes may exist.
-- Phase 2 must validate field types and formats and enforce payload, item,
-- citation-byte and anchor bounds in the adapter.
-- READ COMMITTED ON CONFLICT may hide a concurrent winner. The adapter must
-- perform a fresh read before classifying an empty result as a payload conflict.

-- name: MintDocumentIdentity :one
WITH admission AS (
    SELECT a.id AS id, a.country_code AS country_code FROM ingest.country_admission a
    WHERE a.id = 1 AND a.admission_epoch = $1 AND a.admitted_writer = $2 AND a.enabled
    FOR SHARE
), lease AS MATERIALIZED (
    SELECT l.id AS id, l.expires_at AS expires_at, l.owner AS owner,
        l.attempt_fence AS attempt_fence, l.admission_epoch AS admission_epoch,
        l.cancelled_at AS cancelled_at
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
), observed AS (
    SELECT o.id AS id, o.observation_digest AS observation_digest FROM bronze.observation_revision o
    JOIN bronze.source_record s ON s.id = o.source_record_id
    WHERE o.observation_key = $12 AND o.document_version_key = $4
      AND s.source_id = $13 AND s.external_id = $14
), minted AS MATERIALIZED (
    SELECT silver.mint_document_identity($11, $12, $13, $14, $15, $16, $17,
        c.execution_key, observed.observation_digest, c.expires_at)::bigint AS id
    FROM current_execution c CROSS JOIN observed
    WHERE split_part($11::text, ':', 1) = c.country_code
      AND $11::text ~ '^[a-z]{2}:doc:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
)
SELECT m.id AS id FROM minted m WHERE m.id IS NOT NULL;

-- name: AppendIdentityAssertion :one
WITH admission AS (
    SELECT a.id AS id, a.country_code AS country_code FROM ingest.country_admission a
    WHERE a.id = 1 AND a.admission_epoch = $1 AND a.admitted_writer = $2 AND a.enabled
    FOR SHARE
), lease AS MATERIALIZED (
    SELECT l.id AS id, l.expires_at AS expires_at, l.owner AS owner,
        l.attempt_fence AS attempt_fence, l.admission_epoch AS admission_epoch,
        l.cancelled_at AS cancelled_at
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
), inserted AS (
    INSERT INTO silver.identity_assertion (document_identity_id, observation_key, source_id, external_id,
        normalized_number, document_type, title_hash, kind, related_identity_id, review_state,
        evidence_hash, intake_execution_key)
    SELECT $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, c.execution_key
    FROM current_execution c JOIN bronze.observation_revision o ON o.observation_key = $12
    JOIN bronze.source_record s ON s.id = o.source_record_id
    WHERE o.document_version_key = $4 AND s.source_id = $13 AND s.external_id = $14
      AND c.expires_at > clock_timestamp()
    ON CONFLICT ON CONSTRAINT identity_assertion_natural_key DO NOTHING
    RETURNING id AS id
)
SELECT i.id AS id FROM inserted i
UNION ALL
SELECT ia.id AS id FROM silver.identity_assertion ia CROSS JOIN current_execution c
WHERE ia.document_identity_id = $11 AND ia.observation_key = $12 AND ia.source_id = $13
  AND ia.external_id = $14 AND ia.normalized_number = $15 AND ia.document_type = $16
  AND ia.title_hash = $17 AND ia.kind = $18 AND ia.related_identity_id IS NOT DISTINCT FROM $19::bigint
  AND ia.review_state = $20 AND ia.evidence_hash = $21
  AND EXISTS (SELECT 1 FROM bronze.observation_revision o JOIN bronze.source_record s ON s.id = o.source_record_id
      WHERE o.observation_key = $12 AND o.document_version_key = $4 AND s.source_id = $13 AND s.external_id = $14)
  AND c.expires_at > clock_timestamp() AND NOT EXISTS (SELECT 1 FROM inserted);

-- name: ReadDocumentIdentity :one
SELECT d.id AS id, d.identity_key AS identity_key, d.entity_kind AS entity_kind,
    d.created_by_execution_key AS created_by_execution_key
FROM silver.document_identity d WHERE d.identity_key = $1;

-- name: ReadIdentityByAssertion :many
SELECT d.id AS id, d.identity_key AS identity_key, a.id AS assertion_id,
    a.kind AS kind, a.review_state AS review_state, a.related_identity_id AS related_identity_id
FROM silver.identity_assertion a JOIN silver.document_identity d ON d.id = a.document_identity_id
WHERE a.observation_key = $1 AND a.source_id = $2 AND a.external_id = $3
  AND a.normalized_number = $4 AND a.document_type = $5 AND a.title_hash = $6
ORDER BY a.id;

-- name: InsertLegacyIdentityMapping :one
WITH admission AS (
    SELECT a.id AS id, a.country_code AS country_code FROM ingest.country_admission a
    WHERE a.id = 1 AND a.admission_epoch = $1 AND a.admitted_writer = $2 AND a.enabled
    FOR SHARE
), lease AS MATERIALIZED (
    SELECT l.id AS id, l.expires_at AS expires_at, l.owner AS owner,
        l.attempt_fence AS attempt_fence, l.admission_epoch AS admission_epoch,
        l.cancelled_at AS cancelled_at
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
), inserted AS (
    INSERT INTO silver.legacy_identity_mapping (export_manifest_digest, entity_kind, legacy_id,
        state, document_identity_id, candidate_report_hash, importing_execution_key)
    SELECT $11, $12, $13, $14, $15, $16, c.execution_key FROM current_execution c
    WHERE c.expires_at > clock_timestamp()
      AND $17::text = c.country_code
      AND (($14::text = 'resolved') = ($15::bigint IS NOT NULL))
      AND (($14::text = 'ambiguous') = ($16::text IS NOT NULL))
    ON CONFLICT (export_manifest_digest, entity_kind, legacy_id) DO NOTHING
    RETURNING id AS id
)
SELECT i.id AS id FROM inserted i
UNION ALL
SELECT m.id AS id FROM silver.legacy_identity_mapping m CROSS JOIN current_execution c
WHERE m.export_manifest_digest = $11 AND m.entity_kind = $12 AND m.legacy_id = $13
  AND $17::text = c.country_code
  AND m.state = $14 AND m.document_identity_id IS NOT DISTINCT FROM $15::bigint
  AND m.candidate_report_hash IS NOT DISTINCT FROM $16::text
  AND c.expires_at > clock_timestamp() AND NOT EXISTS (SELECT 1 FROM inserted);

-- name: ResolveLegacyIdentity :one
SELECT d.id AS id, d.identity_key AS identity_key
FROM silver.document_identity d
JOIN (
    SELECT min(m.document_identity_id) AS identity_id FROM silver.legacy_identity_mapping m
    WHERE m.entity_kind = $1 AND m.legacy_id = $2
    HAVING bool_and(m.state = 'resolved') AND count(DISTINCT m.document_identity_id) = 1
) agreed ON agreed.identity_id = d.id;

-- name: ReadLegacyIdentityMappings :many
SELECT m.id AS id, m.export_manifest_digest AS export_manifest_digest,
    m.state AS state, m.document_identity_id AS document_identity_id,
    m.candidate_report_hash AS candidate_report_hash, m.importing_execution_key AS importing_execution_key
FROM silver.legacy_identity_mapping m WHERE m.entity_kind = $1 AND m.legacy_id = $2
ORDER BY m.id;

-- name: InsertDocumentRevision :one
WITH admission AS (
    SELECT a.id AS id, a.country_code AS country_code FROM ingest.country_admission a
    WHERE a.id = 1 AND a.admission_epoch = $1 AND a.admitted_writer = $2 AND a.enabled
    FOR SHARE
), lease AS MATERIALIZED (
    SELECT l.id AS id, l.expires_at AS expires_at, l.owner AS owner,
        l.attempt_fence AS attempt_fence, l.admission_epoch AS admission_epoch,
        l.cancelled_at AS cancelled_at
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
), inserted AS (
    INSERT INTO silver.document_revision (document_identity_id, revision_key, source_observation_set_hash,
        text_hash, metadata_hash, conversion_recipe_digest, normalization_recipe_digest,
        parser_recipe_digest, policy_recipe_digest, normalized_number, document_type,
        document_version_key, intake_execution_id, intake_execution_key)
    SELECT $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $4, c.id, c.execution_key
    FROM current_execution c WHERE c.expires_at > clock_timestamp()
    ON CONFLICT (document_identity_id, revision_key) DO NOTHING
    RETURNING id AS id
)
SELECT i.id AS id FROM inserted i
UNION ALL
SELECT r.id AS id FROM silver.document_revision r CROSS JOIN current_execution c
WHERE r.document_identity_id = $11 AND r.revision_key = $12
  AND r.document_version_key = $4::text
  AND r.source_observation_set_hash = $13 AND r.text_hash = $14 AND r.metadata_hash = $15
  AND r.conversion_recipe_digest = $16 AND r.normalization_recipe_digest = $17
  AND r.parser_recipe_digest = $18 AND r.policy_recipe_digest = $19
  AND r.normalized_number = $20 AND r.document_type = $21
  AND c.expires_at > clock_timestamp() AND NOT EXISTS (SELECT 1 FROM inserted);

-- name: ReadDocumentRevision :one
SELECT r.id AS id, r.document_identity_id AS document_identity_id, r.revision_key AS revision_key,
    r.source_observation_set_hash AS source_observation_set_hash, r.text_hash AS text_hash,
    r.metadata_hash AS metadata_hash, r.conversion_recipe_digest AS conversion_recipe_digest,
    r.normalization_recipe_digest AS normalization_recipe_digest, r.parser_recipe_digest AS parser_recipe_digest,
    r.policy_recipe_digest AS policy_recipe_digest, r.normalized_number AS normalized_number,
    r.document_type AS document_type, r.document_version_key AS document_version_key,
    r.intake_execution_id AS intake_execution_id, r.intake_execution_key AS intake_execution_key
FROM silver.document_revision r WHERE r.revision_key = $1;

-- Invalid parent/key relationships, continuity and duplicate keys or ordinals
-- return no row before insertion, as do conflicts with stored immutable payloads.
-- Malformed transport or field values may still raise SQL errors; the adapter
-- must validate them. Direct inserts remain subject to schema guards.
-- name: InsertProvisionRevisions :one
WITH admission AS (
    SELECT a.id AS id, a.country_code AS country_code FROM ingest.country_admission a
    WHERE a.id = 1 AND a.admission_epoch = $1 AND a.admitted_writer = $2 AND a.enabled
    FOR SHARE
), lease AS MATERIALIZED (
    SELECT l.id AS id, l.expires_at AS expires_at, l.owner AS owner,
        l.attempt_fence AS attempt_fence, l.admission_epoch AS admission_epoch,
        l.cancelled_at AS cancelled_at
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
), items AS MATERIALIZED (
    -- Citation bytes use hex in this transport only; storage keeps exact bytea.
    SELECT (j.value->>'provision_key')::text AS provision_key,
        (j.value->>'local_key')::text AS local_key, (j.value->>'ordinal')::integer AS ordinal,
        decode((j.value->>'native_citation_hex')::text, 'hex') AS native_citation,
        (j.value->'source_anchors')::jsonb AS source_anchors,
        (j.value->>'text_hash')::text AS text_hash,
        (j.value->>'stable_provision_uuid')::uuid AS stable_provision_uuid,
        (j.value->>'continuity_assertion_id')::bigint AS continuity_assertion_id
    FROM jsonb_array_elements($12::jsonb) AS j(value)
), parent AS (
    SELECT r.id AS id FROM silver.document_revision r CROSS JOIN current_execution c
    WHERE r.id = $11 AND r.document_version_key = $4::text AND c.expires_at > clock_timestamp()
      AND NOT EXISTS (
          SELECT 1 FROM items x
          WHERE x.provision_key IS DISTINCT FROM r.revision_key || ':provision:' || x.local_key
             OR (x.stable_provision_uuid IS NOT NULL) <> (x.continuity_assertion_id IS NOT NULL)
             OR (x.continuity_assertion_id IS NOT NULL AND NOT EXISTS (
                 SELECT 1 FROM silver.identity_assertion ia WHERE ia.id = x.continuity_assertion_id
                   AND ia.kind = 'continuity' AND ia.review_state = 'confirmed'
                   AND ia.document_identity_id = r.document_identity_id
             ))
      )
      AND NOT EXISTS (
          SELECT 1 FROM items x JOIN silver.provision_revision p
              ON p.document_revision_id = r.id
                 AND (p.provision_key = x.provision_key OR p.local_key = x.local_key OR p.ordinal = x.ordinal)
          WHERE ROW(p.provision_key, p.local_key, p.ordinal, p.native_citation, p.source_anchors, p.text_hash,
                    p.stable_provision_uuid, p.continuity_assertion_id)
              IS DISTINCT FROM ROW(x.provision_key, x.local_key, x.ordinal, x.native_citation, x.source_anchors, x.text_hash,
                    x.stable_provision_uuid, x.continuity_assertion_id)
      )
      AND (SELECT count(*) = count(DISTINCT x.local_key)
           AND count(*) = count(DISTINCT x.ordinal) AND count(*) = count(DISTINCT x.provision_key) FROM items x)
), inserted AS (
    INSERT INTO silver.provision_revision (document_revision_id, provision_key, local_key, ordinal,
        native_citation, source_anchors, text_hash, stable_provision_uuid, continuity_assertion_id)
    SELECT parent_revision.id, x.provision_key, x.local_key, x.ordinal, x.native_citation, x.source_anchors,
        x.text_hash, x.stable_provision_uuid, x.continuity_assertion_id
    FROM parent parent_revision CROSS JOIN items x CROSS JOIN current_execution c
    WHERE c.expires_at > clock_timestamp()
    ON CONFLICT DO NOTHING
    RETURNING id AS id
)
SELECT parent_revision.id AS id, (SELECT count(*) FROM items) AS provision_count,
    (SELECT count(*) FROM inserted) AS inserted_count FROM parent parent_revision
WHERE (SELECT count(*) FROM inserted) + (SELECT count(*) FROM items x
    JOIN silver.provision_revision pr ON pr.document_revision_id = parent_revision.id AND pr.provision_key = x.provision_key)
    = (SELECT count(*) FROM items);

-- name: ReadProvisionRevisions :many
SELECT p.id AS id, p.provision_key AS provision_key, p.local_key AS local_key,
    p.ordinal AS ordinal, p.native_citation AS native_citation, p.source_anchors AS source_anchors,
    p.text_hash AS text_hash, p.stable_provision_uuid AS stable_provision_uuid,
    p.continuity_assertion_id AS continuity_assertion_id
FROM silver.provision_revision p WHERE p.document_revision_id = $1 ORDER BY p.ordinal;
