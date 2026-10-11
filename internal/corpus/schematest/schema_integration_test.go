package schematest

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const testDatabaseURL = "BANHMI_CORPUS_SCHEMA_TEST_DATABASE_URL"

func TestCorpusSchemaBoundaries(t *testing.T) {
	tx := fixtureTransaction(t)

	var crossSchemaForeignKeys int
	err := tx.QueryRow(t.Context(), `
		SELECT count(*)
		FROM pg_constraint constraint_record
		JOIN pg_class child ON child.oid = constraint_record.conrelid
		JOIN pg_namespace child_namespace ON child_namespace.oid = child.relnamespace
		JOIN pg_class parent ON parent.oid = constraint_record.confrelid
		JOIN pg_namespace parent_namespace ON parent_namespace.oid = parent.relnamespace
		WHERE constraint_record.contype = 'f'
		  AND child_namespace.nspname IN ('config', 'ingest', 'bronze', 'silver', 'gold')
		  AND child_namespace.nspname <> parent_namespace.nspname
	`).Scan(&crossSchemaForeignKeys)
	if err != nil {
		t.Fatalf("inspect foreign keys: %v", err)
	}
	if crossSchemaForeignKeys != 0 {
		t.Fatalf("cross-schema foreign keys = %d, want 0", crossSchemaForeignKeys)
	}

	var compositePrimaryKeys int
	err = tx.QueryRow(t.Context(), `
		SELECT count(*)
		FROM pg_constraint constraint_record
		JOIN pg_class table_record ON table_record.oid = constraint_record.conrelid
		JOIN pg_namespace namespace_record ON namespace_record.oid = table_record.relnamespace
		WHERE constraint_record.contype = 'p'
		  AND namespace_record.nspname IN ('config', 'ingest', 'bronze', 'silver', 'gold')
		  AND cardinality(constraint_record.conkey) <> 1
	`).Scan(&compositePrimaryKeys)
	if err != nil {
		t.Fatalf("inspect primary keys: %v", err)
	}
	if compositePrimaryKeys != 0 {
		t.Fatalf("composite primary keys = %d, want 0", compositePrimaryKeys)
	}

	var countrySelectorColumns int
	err = tx.QueryRow(t.Context(), `
		SELECT count(*)
		FROM information_schema.columns
		WHERE table_schema IN ('ingest', 'bronze', 'silver', 'gold')
		  AND column_name IN ('country', 'country_code', 'jurisdiction')
	`).Scan(&countrySelectorColumns)
	if err != nil {
		t.Fatalf("inspect country selector columns: %v", err)
	}
	if countrySelectorColumns != 0 {
		t.Fatalf("country selector columns = %d, want 0", countrySelectorColumns)
	}
}

func TestImmutableEvidenceAndReviewConstraints(t *testing.T) {
	tx := fixtureTransaction(t)

	var sourceRecordID int64
	err := tx.QueryRow(t.Context(), `
		INSERT INTO bronze.source_record (
			source, external_id, first_observed_at
		) VALUES ('official', 'doc-1', now())
		RETURNING id
	`).Scan(&sourceRecordID)
	if err != nil {
		t.Fatalf("insert source record: %v", err)
	}

	var observationID int64
	err = tx.QueryRow(t.Context(), `
		INSERT INTO bronze.observation_revision (
			source_record_id, build_id, observation_key, source_observed_at,
			captured_at, source_url, metadata_bytes, metadata_sha256,
			capture_recipe_digest, review_state, writer_owner, fence_token
		) VALUES (
			$1, 1, 'observation-1', now(), now(), 'https://official.invalid/doc-1',
			convert_to('{"title":"exact"}', 'UTF8'), repeat('a', 64),
			'capture-v1', 'accepted', 'worker-a', 1
		) RETURNING id
	`, sourceRecordID).Scan(&observationID)
	if err != nil {
		t.Fatalf("insert observation revision: %v", err)
	}
	assertSQLState(t, tx, "55000", func() error {
		_, updateErr := tx.Exec(t.Context(), `
			UPDATE bronze.observation_revision
			SET metadata_bytes = convert_to('{}', 'UTF8')
			WHERE id = $1
		`, observationID)
		return updateErr
	})

	var identityID int64
	err = tx.QueryRow(t.Context(), `
		INSERT INTO silver.document_identity (document_uuid, evidence_key)
		VALUES ('11111111-1111-1111-1111-111111111111', 'vn:official:doc-1')
		RETURNING id
	`).Scan(&identityID)
	if err != nil {
		t.Fatalf("insert document identity: %v", err)
	}
	var secondIdentityID int64
	err = tx.QueryRow(t.Context(), `
		INSERT INTO silver.document_identity (document_uuid, evidence_key)
		VALUES ('33333333-3333-3333-3333-333333333333', 'vn:official:doc-1-candidate')
		RETURNING id
	`).Scan(&secondIdentityID)
	if err != nil {
		t.Fatalf("insert second document identity: %v", err)
	}
	_, err = tx.Exec(t.Context(), `
		INSERT INTO silver.legacy_document_mapping (
			legacy_namespace, legacy_document_id, candidate_key,
			document_identity_id, mapping_state, review_state
		) VALUES
			('silver.document', '41', 'candidate-a', $1, 'ambiguous', 'unreviewed'),
			('silver.document', '41', 'candidate-b', $2, 'ambiguous', 'unreviewed')
	`, identityID, secondIdentityID)
	if err != nil {
		t.Fatalf("insert ambiguous legacy mapping: %v", err)
	}
	legacyLookup := namedQuery(t, "sql/silver/queries.sql", "ListLegacyDocumentMappings")
	rows, err := tx.Query(t.Context(), legacyLookup, pgx.NamedArgs{
		"legacy_namespace": "silver.document", "legacy_document_id": "41",
	})
	if err != nil {
		t.Fatalf("list ambiguous legacy mapping: %v", err)
	}
	legacyCandidates := 0
	for rows.Next() {
		legacyCandidates++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read ambiguous legacy mapping: %v", err)
	}
	rows.Close()
	if legacyCandidates != 2 {
		t.Fatalf("legacy mapping candidates = %d, want 2", legacyCandidates)
	}

	var documentRevisionID int64
	err = tx.QueryRow(t.Context(), `
		INSERT INTO silver.document_revision (
			document_identity_id, build_id, revision_key, source_observation_id,
			source_observed_at, processed_at, text_blob_id, text_sha256,
			metadata_bytes, metadata_sha256, parser_recipe_digest,
			policy_recipe_digest, review_state, writer_owner, fence_token
		) VALUES (
			$1, 1, 'revision-1', $2, now(), now(), 10, repeat('b', 64),
			convert_to('{"status":"source assertion"}', 'UTF8'), repeat('c', 64),
			'parser-v1', 'policy-v1', 'accepted', 'worker-a', 1
		) RETURNING id
	`, identityID, observationID).Scan(&documentRevisionID)
	if err != nil {
		t.Fatalf("insert document revision: %v", err)
	}

	assertSQLState(t, tx, "23514", func() error {
		_, insertErr := tx.Exec(t.Context(), `
			INSERT INTO silver.provision_revision (
				document_revision_id, build_id, provision_key, stable_provision_uuid,
				stable_match_review_state, ordinal, native_citation_bytes,
				native_citation_text, source_anchor, content_blob_id, content_sha256,
				source_observation_id, parser_recipe_digest, policy_recipe_digest,
				review_state, writer_owner, fence_token
			) VALUES (
				$1, 1, 'article-1', '22222222-2222-2222-2222-222222222222',
				'pending_review', 1, convert_to('Điều 1', 'UTF8'), 'Điều 1',
				'#article-1', 11, repeat('d', 64), $2, 'parser-v1', 'policy-v1',
				'accepted', 'worker-a', 1
			)
		`, documentRevisionID, observationID)
		return insertErr
	})
	assertSQLState(t, tx, "23514", func() error {
		_, insertErr := tx.Exec(t.Context(), `
			INSERT INTO silver.provision_revision (
				document_revision_id, build_id, provision_key,
				stable_match_evidence_ref, stable_match_review_state, ordinal,
				native_citation_bytes, native_citation_text, source_anchor,
				content_blob_id, content_sha256, source_observation_id,
				parser_recipe_digest, policy_recipe_digest, review_state,
				writer_owner, fence_token
			) VALUES (
				$1, 1, 'article-approved-without-id',
				'cas://stable-match-review', 'approved', 2,
				convert_to('Điều 2', 'UTF8'), 'Điều 2', '#article-2',
				12, repeat('e', 64), $2, 'parser-v1', 'policy-v1',
				'accepted', 'worker-a', 1
			)
		`, documentRevisionID, observationID)
		return insertErr
	})

	var provisionID int64
	err = tx.QueryRow(t.Context(), `
		INSERT INTO silver.provision_revision (
			document_revision_id, build_id, provision_key, stable_provision_uuid,
			stable_match_evidence_ref, stable_match_review_state, ordinal,
			native_citation_bytes, native_citation_text, source_anchor,
			content_blob_id, content_sha256, source_observation_id,
			parser_recipe_digest, policy_recipe_digest, review_state,
			writer_owner, fence_token
		) VALUES (
			$1, 1, 'article-1', '22222222-2222-2222-2222-222222222222',
			'cas://stable-match-review', 'approved', 1,
			convert_to('Điều 1', 'UTF8'), 'Điều 1',
			'#article-1', 11, repeat('d', 64), $2, 'parser-v1', 'policy-v1',
			'accepted', 'worker-a', 1
		) RETURNING id
	`, documentRevisionID, observationID).Scan(&provisionID)
	if err != nil {
		t.Fatalf("insert provision revision: %v", err)
	}
	assertSQLState(t, tx, "23505", func() error {
		_, insertErr := tx.Exec(t.Context(), `
			INSERT INTO silver.provision_revision (
				document_revision_id, build_id, provision_key, stable_provision_uuid,
				stable_match_evidence_ref, stable_match_review_state, ordinal,
				native_citation_bytes, native_citation_text, source_anchor,
				content_blob_id, content_sha256, source_observation_id,
				parser_recipe_digest, policy_recipe_digest, review_state,
				writer_owner, fence_token
			) VALUES (
				$1, 1, 'article-2', '22222222-2222-2222-2222-222222222222',
				'cas://stable-match-review', 'approved', 2,
				convert_to('Điều 2', 'UTF8'), 'Điều 2', '#article-2',
				12, repeat('e', 64), $2, 'parser-v1', 'policy-v1',
				'accepted', 'worker-a', 1
			)
		`, documentRevisionID, observationID)
		return insertErr
	})
	assertSQLState(t, tx, "55000", func() error {
		_, updateErr := tx.Exec(t.Context(), `
			UPDATE silver.provision_revision
			SET native_citation_bytes = convert_to('Article 1', 'UTF8')
			WHERE id = $1
		`, provisionID)
		return updateErr
	})
}

func TestStageIdempotencyCompletionAndFence(t *testing.T) {
	tx := fixtureTransaction(t)
	buildID := insertRunAndBuild(t, tx)
	assertSQLState(t, tx, "55000", func() error {
		_, updateErr := tx.Exec(t.Context(), `
			UPDATE ingest.corpus_build
			SET discovery_cutoff = discovery_cutoff + interval '1 second'
			WHERE id = $1
		`, buildID)
		return updateErr
	})

	now := time.Now().UTC()
	lease := namedQuery(t, "sql/ingest/queries.sql", "AcquireSchedulerLease")
	row := tx.QueryRow(t.Context(), lease, pgx.NamedArgs{
		"build_id": buildID, "owner": "worker-a",
		"expires_at": now.Add(time.Hour),
	})
	var fence int64
	var leaseID, leaseBuildID int64
	var owner string
	var acquiredAt, renewedAt, expiresAt time.Time
	var releasedAt *time.Time
	if err := row.Scan(&leaseID, &leaseBuildID, &owner, &fence, &acquiredAt, &renewedAt, &expiresAt, &releasedAt); err != nil {
		t.Fatalf("acquire first lease: %v", err)
	}

	_, err := tx.Exec(t.Context(), `
		UPDATE ingest.scheduler_lease
		SET renewed_at = $2, expires_at = $3
		WHERE build_id = $1
	`, buildID, now.Add(-2*time.Hour), now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("expire first lease fixture: %v", err)
	}
	row = tx.QueryRow(t.Context(), lease, pgx.NamedArgs{
		"build_id": buildID, "owner": "worker-b",
		"expires_at": now.Add(time.Hour),
	})
	if err := row.Scan(&leaseID, &leaseBuildID, &owner, &fence, &acquiredAt, &renewedAt, &expiresAt, &releasedAt); err != nil {
		t.Fatalf("acquire expired lease: %v", err)
	}
	if owner != "worker-b" || fence != 2 {
		t.Fatalf("reacquired lease = owner %q fence %d, want worker-b fence 2", owner, fence)
	}

	insertResult := namedQuery(t, "sql/ingest/queries.sql", "InsertStageResult")
	args := completeStageArgs(buildID, "worker-a", 1, now)
	rows, err := tx.Query(t.Context(), insertResult, args)
	if err != nil {
		t.Fatalf("execute stale fenced insert: %v", err)
	}
	if rows.Next() {
		t.Fatal("stale fence inserted a stage result")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read stale fenced insert: %v", err)
	}
	rows.Close()

	args["writer_owner"] = "worker-b"
	args["fence_token"] = int64(2)
	rows, err = tx.Query(t.Context(), insertResult, args)
	if err != nil {
		t.Fatalf("insert stage result: %v", err)
	}
	if !rows.Next() {
		t.Fatal("current fence did not insert a stage result")
	}
	values, err := rows.Values()
	if err != nil {
		t.Fatalf("read stage result: %v", err)
	}
	stageResultID := values[0].(int64)
	rows.Close()

	args["result_digest"] = "different-result"
	rows, err = tx.Query(t.Context(), insertResult, args)
	if err != nil {
		t.Fatalf("retry stage result: %v", err)
	}
	if rows.Next() {
		t.Fatal("duplicate idempotency key replaced its stored payload")
	}
	rows.Close()

	var storedDigest string
	err = tx.QueryRow(t.Context(), `
		SELECT result_digest FROM ingest.stage_result WHERE id = $1
	`, stageResultID).Scan(&storedDigest)
	if err != nil {
		t.Fatalf("read stored result digest: %v", err)
	}
	if storedDigest != "result-v1" {
		t.Fatalf("stored result digest = %q, want result-v1", storedDigest)
	}

	_, err = tx.Exec(t.Context(), `
		INSERT INTO ingest.stage_result_output (
			stage_result_id, ordinal, object_ref, digest, size_bytes, media_type
		) VALUES ($1, 0, 'cas://output-1', repeat('e', 64), 12, 'application/json')
		ON CONFLICT (stage_result_id, ordinal) DO NOTHING
	`, stageResultID)
	if err != nil {
		t.Fatalf("insert result output: %v", err)
	}
	_, err = tx.Exec(t.Context(), `
		INSERT INTO ingest.stage_result_output (
			stage_result_id, ordinal, object_ref, digest, size_bytes, media_type
		) VALUES ($1, 0, 'cas://different', repeat('f', 64), 99, 'text/plain')
		ON CONFLICT (stage_result_id, ordinal) DO NOTHING
	`, stageResultID)
	if err != nil {
		t.Fatalf("retry result output: %v", err)
	}
	var objectRef string
	err = tx.QueryRow(t.Context(), `
		SELECT object_ref FROM ingest.stage_result_output
		WHERE stage_result_id = $1 AND ordinal = 0
	`, stageResultID).Scan(&objectRef)
	if err != nil {
		t.Fatalf("read result output: %v", err)
	}
	if objectRef != "cas://output-1" {
		t.Fatalf("stored output ref = %q, want cas://output-1", objectRef)
	}

	var externalJobID int64
	err = tx.QueryRow(t.Context(), `
		INSERT INTO ingest.external_job (
			build_id, stage, provider, submission_key, request_manifest_ref,
			request_manifest_digest, writer_owner, fence_token
		) VALUES (
			$1, 'ocr', 'provider-a', 'submission-1', 'cas://request',
			repeat('1', 64), 'worker-b', 2
		) RETURNING id
	`, buildID).Scan(&externalJobID)
	if err != nil {
		t.Fatalf("insert prepared external job: %v", err)
	}
	setExternalJobState := namedQuery(t, "sql/ingest/queries.sql", "SetExternalJobState")
	externalJobArgs := pgx.NamedArgs{
		"id": externalJobID, "build_id": buildID, "state": "submitted",
		"provider_job_id": nil, "result_manifest_ref": nil,
		"result_manifest_digest": nil, "writer_owner": "worker-b",
		"fence_token": int64(2), "next_eligible_at": nil,
		"cancellation_requested_at": nil, "last_error": "",
	}
	rows, err = tx.Query(t.Context(), setExternalJobState, externalJobArgs)
	if err != nil {
		t.Fatalf("execute providerless external-job transition: %v", err)
	}
	if rows.Next() {
		t.Fatal("external job reached submitted without a provider id")
	}
	rows.Close()

	externalJobArgs["provider_job_id"] = "provider-job-1"
	rows, err = tx.Query(t.Context(), setExternalJobState, externalJobArgs)
	if err != nil {
		t.Fatalf("submit external job: %v", err)
	}
	if !rows.Next() {
		t.Fatal("external job with a provider id did not reach submitted")
	}
	rows.Close()

	externalJobArgs["state"] = "succeeded"
	rows, err = tx.Query(t.Context(), setExternalJobState, externalJobArgs)
	if err != nil {
		t.Fatalf("execute resultless external-job completion: %v", err)
	}
	if rows.Next() {
		t.Fatal("external job succeeded without a result manifest")
	}
	rows.Close()

	externalJobArgs["result_manifest_ref"] = "cas://external-result"
	externalJobArgs["result_manifest_digest"] = strings.Repeat("2", 64)
	rows, err = tx.Query(t.Context(), setExternalJobState, externalJobArgs)
	if err != nil {
		t.Fatalf("complete external job: %v", err)
	}
	if !rows.Next() {
		t.Fatal("external job with a result manifest did not reach succeeded")
	}
	rows.Close()

	externalJobArgs["provider_job_id"] = "provider-job-replacement"
	externalJobArgs["result_manifest_ref"] = "cas://replacement"
	externalJobArgs["result_manifest_digest"] = strings.Repeat("3", 64)
	rows, err = tx.Query(t.Context(), setExternalJobState, externalJobArgs)
	if err != nil {
		t.Fatalf("replay external-job completion: %v", err)
	}
	if !rows.Next() {
		t.Fatal("exact-state external-job replay returned no row")
	}
	rows.Close()
	var providerJobID, resultManifestRef string
	err = tx.QueryRow(t.Context(), `
		SELECT provider_job_id, result_manifest_ref
		FROM ingest.external_job WHERE id = $1
	`, externalJobID).Scan(&providerJobID, &resultManifestRef)
	if err != nil {
		t.Fatalf("read external-job identities: %v", err)
	}
	if providerJobID != "provider-job-1" || resultManifestRef != "cas://external-result" {
		t.Fatalf("external-job identities changed to provider %q result %q", providerJobID, resultManifestRef)
	}

	assertSQLState(t, tx, "23514", func() error {
		_, insertErr := tx.Exec(t.Context(), `
			INSERT INTO ingest.stage_result (
				build_id, stage, shard_key, idempotency_key, input_manifest_ref,
				input_manifest_digest, recipe_digest, result_digest, writer_owner,
				fence_token, expected_count, processed_count, succeeded_count,
				pending_retry_count, terminal_gap_count, accepted_gap_count,
				rejected_input_count, failure_count, complete, completion_state
			) VALUES (
				$1, 'fetch', 'bad', 'bad-accounting', 'cas://input', repeat('a', 64),
				'recipe-v1', 'bad-result', 'worker-b', 2, 1, 1, 0, 0, 0, 0, 1, 0,
				TRUE, 'complete'
			)
		`, buildID)
		return insertErr
	})

	release := namedQuery(t, "sql/ingest/queries.sql", "ReleaseSchedulerLease")
	err = tx.QueryRow(t.Context(), release, pgx.NamedArgs{
		"build_id": buildID, "owner": "worker-b", "fence_token": int64(2),
	}).Scan(&leaseID, &leaseBuildID, &owner, &fence, &acquiredAt, &renewedAt, &expiresAt, &releasedAt)
	if err != nil {
		t.Fatalf("release current lease: %v", err)
	}
	if releasedAt == nil {
		t.Fatal("released lease did not retain a release time")
	}

	row = tx.QueryRow(t.Context(), lease, pgx.NamedArgs{
		"build_id": buildID, "owner": "worker-c",
		"expires_at": time.Now().UTC().Add(time.Hour),
	})
	if err := row.Scan(&leaseID, &leaseBuildID, &owner, &fence, &acquiredAt, &renewedAt, &expiresAt, &releasedAt); err != nil {
		t.Fatalf("reacquire released lease: %v", err)
	}
	if owner != "worker-c" || fence != 3 {
		t.Fatalf("released lease reacquisition = owner %q fence %d, want worker-c fence 3", owner, fence)
	}
}

func TestReleaseActivationCompareAndSet(t *testing.T) {
	tx := fixtureTransaction(t)
	buildID := insertRunAndBuild(t, tx)

	readyReleaseID := insertRelease(t, tx, buildID, "release-ready", "ready", true)
	blockedReleaseID := insertRelease(t, tx, buildID, "release-blocked", "candidate", false)
	missingManifestReleaseID := insertRelease(t, tx, buildID, "release-no-manifest", "ready", false)
	assertSQLState(t, tx, "55000", func() error {
		_, updateErr := tx.Exec(t.Context(), `
			UPDATE ingest.corpus_release
			SET snapshot_ref = 'snapshot://retargeted'
			WHERE id = $1
		`, readyReleaseID)
		return updateErr
	})
	_, err := tx.Exec(t.Context(), `
		INSERT INTO ingest.release_activation (selection_key, selection_version)
		VALUES ('serving', 0)
	`)
	if err != nil {
		t.Fatalf("initialize activation: %v", err)
	}
	assertSQLState(t, tx, "23514", func() error {
		_, insertErr := tx.Exec(t.Context(), `
			INSERT INTO ingest.release_activation (selection_key, selection_version)
			VALUES ('alternate', 0)
		`)
		return insertErr
	})

	activate := namedQuery(t, "sql/ingest/queries.sql", "ActivateCorpusRelease")
	now := time.Now().UTC()
	rows, err := tx.Query(t.Context(), activate, pgx.NamedArgs{
		"release_id": readyReleaseID, "selection_key": "serving",
		"expected_selection_version": int64(0), "activated_at": now,
		"activated_by": "publisher-a",
	})
	if err != nil {
		t.Fatalf("activate ready release: %v", err)
	}
	if !rows.Next() {
		t.Fatal("ready validated release was not activated")
	}
	rows.Close()

	rows, err = tx.Query(t.Context(), activate, pgx.NamedArgs{
		"release_id": missingManifestReleaseID, "selection_key": "serving",
		"expected_selection_version": int64(1), "activated_at": now.Add(90 * time.Second),
		"activated_by": "publisher-b",
	})
	if err != nil {
		t.Fatalf("execute missing-manifest activation: %v", err)
	}
	if rows.Next() {
		t.Fatal("ready release without a manifest changed the active selection")
	}
	rows.Close()

	rows, err = tx.Query(t.Context(), activate, pgx.NamedArgs{
		"release_id": blockedReleaseID, "selection_key": "serving",
		"expected_selection_version": int64(0), "activated_at": now.Add(time.Minute),
		"activated_by": "publisher-b",
	})
	if err != nil {
		t.Fatalf("execute stale activation: %v", err)
	}
	if rows.Next() {
		t.Fatal("stale selection version changed the active release")
	}
	rows.Close()

	rows, err = tx.Query(t.Context(), activate, pgx.NamedArgs{
		"release_id": blockedReleaseID, "selection_key": "serving",
		"expected_selection_version": int64(1), "activated_at": now.Add(2 * time.Minute),
		"activated_by": "publisher-b",
	})
	if err != nil {
		t.Fatalf("execute unready activation: %v", err)
	}
	if rows.Next() {
		t.Fatal("unready release changed the active selection")
	}
	rows.Close()

	var version, currentReleaseID, activeHolds int64
	err = tx.QueryRow(t.Context(), `
		SELECT activation.selection_version, activation.current_release_id,
		       count(hold.id) FILTER (WHERE hold.hold_kind = 'active')
		FROM ingest.release_activation activation
		LEFT JOIN ingest.release_retention_hold hold
		  ON hold.release_id = activation.current_release_id
		WHERE activation.selection_key = 'serving'
		GROUP BY activation.selection_version, activation.current_release_id
	`).Scan(&version, &currentReleaseID, &activeHolds)
	if err != nil {
		t.Fatalf("read active selection: %v", err)
	}
	if version != 1 || currentReleaseID != readyReleaseID || activeHolds != 1 {
		t.Fatalf("selection = version %d release %d active holds %d", version, currentReleaseID, activeHolds)
	}

	assertSQLState(t, tx, "23514", func() error {
		_, insertErr := tx.Exec(t.Context(), `
			INSERT INTO ingest.release_retention_hold (
				release_id, hold_kind, hold_key
			) VALUES ($1, 'in_flight', 'request-1')
		`, readyReleaseID)
		return insertErr
	})
	_, err = tx.Exec(t.Context(), `
		INSERT INTO ingest.release_retention_hold (
			release_id, hold_kind, hold_key, expires_at
		) VALUES ($1, 'in_flight', 'request-1', now() + interval '5 minutes')
	`, readyReleaseID)
	if err != nil {
		t.Fatalf("insert expiring request pin: %v", err)
	}

	retiredReleaseID := insertRelease(t, tx, buildID, "release-retired", "retired", false)
	_, err = tx.Exec(t.Context(), `
		INSERT INTO ingest.release_retention_hold (
			release_id, hold_kind, hold_key, expires_at
		) VALUES ($1, 'pinned', 'expired-request', now() - interval '1 minute')
	`, retiredReleaseID)
	if err != nil {
		t.Fatalf("insert expired request pin: %v", err)
	}
	eligible := namedQuery(t, "sql/ingest/queries.sql", "ListReleasesEligibleForRetention")
	rows, err = tx.Query(t.Context(), eligible, pgx.NamedArgs{
		"row_limit": int32(10),
	})
	if err != nil {
		t.Fatalf("list releases eligible for retention: %v", err)
	}
	foundRetired := false
	for rows.Next() {
		values, valuesErr := rows.Values()
		if valuesErr != nil {
			t.Fatalf("read retention candidate: %v", valuesErr)
		}
		if values[0].(int64) == retiredReleaseID {
			foundRetired = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read retention candidates: %v", err)
	}
	rows.Close()
	if !foundRetired {
		t.Fatal("expired request pin kept a retired release from retention")
	}

	claimNow := time.Now().UTC()
	lockEligible := namedQuery(t, "sql/ingest/queries.sql", "LockEligibleReleasesForRetention")
	rows, err = tx.Query(t.Context(), lockEligible, pgx.NamedArgs{
		"owner": "gc-a", "batch_key": "batch-1", "row_limit": int32(10),
	})
	if err != nil {
		t.Fatalf("lock releases eligible for retention: %v", err)
	}
	lockedRetired := false
	for rows.Next() {
		values, valuesErr := rows.Values()
		if valuesErr != nil {
			t.Fatalf("read locked retention candidate: %v", valuesErr)
		}
		if values[0].(int64) == retiredReleaseID {
			lockedRetired = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read locked retention candidates: %v", err)
	}
	rows.Close()
	if !lockedRetired {
		t.Fatal("expired pin prevented retention claim locking")
	}

	claimRelease := namedQuery(t, "sql/ingest/queries.sql", "ClaimReleaseForRetention")
	rows, err = tx.Query(t.Context(), claimRelease, pgx.NamedArgs{
		"release_id": retiredReleaseID, "owner": "gc-a", "batch_key": "batch-1",
		"ttl_milliseconds": int64(5 * time.Minute / time.Millisecond),
	})
	if err != nil {
		t.Fatalf("claim release for retention: %v", err)
	}
	if !rows.Next() {
		t.Fatal("eligible release was not claimed")
	}
	claimValues, err := rows.Values()
	if err != nil {
		t.Fatalf("read retention claim: %v", err)
	}
	claimKey := claimValues[2].(string)
	claimFence := claimValues[5].(int64)
	rows.Close()

	addHold := namedQuery(t, "sql/ingest/queries.sql", "AddReleaseRetentionHold")
	rows, err = tx.Query(t.Context(), addHold, pgx.NamedArgs{
		"release_id": retiredReleaseID, "hold_kind": "in_flight",
		"hold_key": "late-request", "expires_at": claimNow.Add(time.Minute),
		"created_at": claimNow,
	})
	if err != nil {
		t.Fatalf("execute pin against claimed release: %v", err)
	}
	if rows.Next() {
		t.Fatal("claimed release accepted a new request pin")
	}
	rows.Close()
	releaseClaim := namedQuery(t, "sql/ingest/queries.sql", "ReleaseReleaseRetentionClaim")
	_, err = tx.Exec(t.Context(), `
		UPDATE ingest.release_retention_claim
		SET renewed_at = clock_timestamp() - interval '2 hours',
		    expires_at = clock_timestamp() - interval '1 hour'
		WHERE release_id = $1
	`, retiredReleaseID)
	if err != nil {
		t.Fatalf("expire retention-claim fixture: %v", err)
	}
	rows, err = tx.Query(t.Context(), addHold, pgx.NamedArgs{
		"release_id": retiredReleaseID, "hold_kind": "in_flight",
		"hold_key": "expired-claim-request", "expires_at": claimNow.Add(time.Minute),
		"created_at": claimNow,
	})
	if err != nil {
		t.Fatalf("execute pin against expired unreleased claim: %v", err)
	}
	if rows.Next() {
		t.Fatal("expired unreleased claim accepted a new request pin")
	}
	rows.Close()
	rows, err = tx.Query(t.Context(), releaseClaim, pgx.NamedArgs{
		"release_id": retiredReleaseID, "claim_key": claimKey, "owner": "gc-a",
		"fence_token": claimFence,
	})
	if err != nil {
		t.Fatalf("execute expired retention-claim release: %v", err)
	}
	if rows.Next() {
		t.Fatal("expired retention claim released without a new fence")
	}
	rows.Close()

	rows, err = tx.Query(t.Context(), claimRelease, pgx.NamedArgs{
		"release_id": retiredReleaseID, "owner": "gc-b", "batch_key": "batch-2",
		"ttl_milliseconds": int64(5 * time.Minute / time.Millisecond),
	})
	if err != nil {
		t.Fatalf("reclaim expired release: %v", err)
	}
	if !rows.Next() {
		t.Fatal("expired retention claim was not reclaimed")
	}
	reclaimedValues, err := rows.Values()
	if err != nil {
		t.Fatalf("read reclaimed retention claim: %v", err)
	}
	claimKey = reclaimedValues[2].(string)
	claimFence = reclaimedValues[5].(int64)
	rows.Close()
	if claimFence != 2 {
		t.Fatalf("reclaimed retention fence = %d, want 2", claimFence)
	}

	rows, err = tx.Query(t.Context(), releaseClaim, pgx.NamedArgs{
		"release_id": retiredReleaseID, "claim_key": claimKey, "owner": "gc-b",
		"fence_token": claimFence - 1,
	})
	if err != nil {
		t.Fatalf("execute stale retention-claim release: %v", err)
	}
	if rows.Next() {
		t.Fatal("stale retention fence released the claim")
	}
	rows.Close()

	finalizeClaim := namedQuery(t, "sql/ingest/queries.sql", "FinalizeReleaseRetentionClaim")
	rows, err = tx.Query(t.Context(), finalizeClaim, pgx.NamedArgs{
		"release_id": retiredReleaseID, "claim_key": claimKey, "owner": "gc-b",
		"fence_token": claimFence,
	})
	if err != nil {
		t.Fatalf("finalize retention claim: %v", err)
	}
	if !rows.Next() {
		t.Fatal("current retention claim did not finalize")
	}
	rows.Close()

	rows, err = tx.Query(t.Context(), addHold, pgx.NamedArgs{
		"release_id": retiredReleaseID, "hold_kind": "in_flight",
		"hold_key": "post-finalize-request", "expires_at": claimNow.Add(time.Minute),
		"created_at": claimNow,
	})
	if err != nil {
		t.Fatalf("execute pin against finalized release: %v", err)
	}
	if rows.Next() {
		t.Fatal("finalized release accepted a new request pin")
	}
	rows.Close()
}

func TestReleaseActivationRace(t *testing.T) {
	if testing.Short() {
		t.Skip("corpus schema PostgreSQL integration is disabled in short mode")
	}
	databaseURL := strings.TrimSpace(os.Getenv(testDatabaseURL))
	if databaseURL == "" {
		t.Skip(testDatabaseURL + " is not set")
	}
	pool, err := pgxpool.New(t.Context(), databaseURL)
	if err != nil {
		t.Fatalf("open disposable PostgreSQL database: %v", err)
	}
	t.Cleanup(pool.Close)

	setup, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin activation-race setup: %v", err)
	}
	buildID := insertRunAndBuild(t, setup)
	firstReleaseID := insertRelease(t, setup, buildID, "release-race-a", "ready", true)
	secondReleaseID := insertRelease(t, setup, buildID, "release-race-b", "ready", true)
	_, err = setup.Exec(t.Context(), `
		INSERT INTO ingest.release_activation (selection_key, selection_version)
		VALUES ('serving', 0)
	`)
	if err != nil {
		_ = setup.Rollback(t.Context())
		t.Fatalf("initialize activation race: %v", err)
	}
	if err := setup.Commit(t.Context()); err != nil {
		t.Fatalf("commit activation-race setup: %v", err)
	}

	first, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin first activation: %v", err)
	}
	t.Cleanup(func() { _ = first.Rollback(t.Context()) })
	second, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin second activation: %v", err)
	}
	t.Cleanup(func() { _ = second.Rollback(t.Context()) })
	var secondPID int32
	if err := second.QueryRow(t.Context(), "SELECT pg_backend_pid()").Scan(&secondPID); err != nil {
		t.Fatalf("read second activation backend: %v", err)
	}

	lockActivation := namedQuery(t, "sql/ingest/queries.sql", "LockCorpusReleaseForActivation")
	if err := first.QueryRow(t.Context(), lockActivation, pgx.NamedArgs{
		"release_id": firstReleaseID,
	}).Scan(&firstReleaseID); err != nil {
		t.Fatalf("lock first activation candidate: %v", err)
	}
	if err := second.QueryRow(t.Context(), lockActivation, pgx.NamedArgs{
		"release_id": secondReleaseID,
	}).Scan(&secondReleaseID); err != nil {
		t.Fatalf("lock second activation candidate: %v", err)
	}

	activate := namedQuery(t, "sql/ingest/queries.sql", "ActivateCorpusRelease")
	firstArgs := pgx.NamedArgs{
		"release_id": firstReleaseID, "selection_key": "serving",
		"expected_selection_version": int64(0), "activated_at": time.Now().UTC(),
		"activated_by": "publisher-race-a",
	}
	secondArgs := pgx.NamedArgs{
		"release_id": secondReleaseID, "selection_key": "serving",
		"expected_selection_version": int64(0), "activated_at": time.Now().UTC(),
		"activated_by": "publisher-race-b",
	}
	rows, err := first.Query(t.Context(), activate, firstArgs)
	if err != nil {
		t.Fatalf("execute first activation: %v", err)
	}
	if !rows.Next() {
		rows.Close()
		t.Fatal("first activation returned no row")
	}
	rows.Close()

	type activationOutcome struct {
		won bool
		err error
	}
	outcomes := make(chan activationOutcome, 1)
	go func() {
		secondRows, queryErr := second.Query(t.Context(), activate, secondArgs)
		if queryErr != nil {
			outcomes <- activationOutcome{err: queryErr}
			return
		}
		won := secondRows.Next()
		rowErr := secondRows.Err()
		secondRows.Close()
		outcomes <- activationOutcome{won: won, err: rowErr}
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		var waitingOnLock bool
		err = pool.QueryRow(t.Context(), `
			SELECT COALESCE(wait_event_type = 'Lock', FALSE)
			FROM pg_stat_activity WHERE pid = $1
		`, secondPID).Scan(&waitingOnLock)
		if err != nil {
			t.Fatalf("inspect competing activation: %v", err)
		}
		if waitingOnLock {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("competing activation did not block on the release lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := first.Commit(t.Context()); err != nil {
		t.Fatalf("commit first activation: %v", err)
	}

	select {
	case outcome := <-outcomes:
		if outcome.err != nil {
			t.Fatalf("execute competing activation: %v", outcome.err)
		}
		if outcome.won {
			t.Fatal("both concurrent activations changed the selection")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("competing activation did not finish after the winner committed")
	}
	if err := second.Commit(t.Context()); err != nil {
		t.Fatalf("commit losing activation: %v", err)
	}

	var version, activeReleaseID int64
	err = pool.QueryRow(t.Context(), `
		SELECT selection_version, current_release_id
		FROM ingest.release_activation WHERE selection_key = 'serving'
	`).Scan(&version, &activeReleaseID)
	if err != nil {
		t.Fatalf("read activation-race winner: %v", err)
	}
	if version != 1 || activeReleaseID != firstReleaseID {
		t.Fatalf("activation race selected version %d release %d", version, activeReleaseID)
	}
}

func TestReleaseManifestRequiresEvidence(t *testing.T) {
	tx := fixtureTransaction(t)
	buildID := insertRunAndBuild(t, tx)

	testCases := []struct {
		name       string
		sources    int64
		documents  int64
		provisions int64
		chunks     int64
	}{
		{name: "all-zero"},
		{name: "zero-source-observations", documents: 1, provisions: 1, chunks: 1},
		{name: "zero-document-revisions", sources: 1, provisions: 1, chunks: 1},
		{name: "zero-provision-revisions", sources: 1, documents: 1, chunks: 1},
		{name: "zero-chunks", sources: 1, documents: 1, provisions: 1},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			releaseID := insertRelease(
				t, tx, buildID, "release-empty-"+testCase.name, "candidate", false,
			)
			assertSQLState(t, tx, "23514", func() error {
				return insertReleaseManifestCounts(
					t, tx, releaseID, testCase.sources, testCase.documents,
					testCase.provisions, testCase.chunks,
				)
			})
		})
	}
}

// fixtureTransaction requires a freshly migrated disposable database. Each test
// rolls back its synthetic fixtures and never reapplies canonical schema DDL.
func fixtureTransaction(t *testing.T) pgx.Tx {
	t.Helper()
	if testing.Short() {
		t.Skip("corpus schema PostgreSQL integration is disabled in short mode")
	}
	databaseURL := strings.TrimSpace(os.Getenv(testDatabaseURL))
	if databaseURL == "" {
		t.Skip(testDatabaseURL + " is not set")
	}

	pool, err := pgxpool.New(t.Context(), databaseURL)
	if err != nil {
		t.Fatalf("open disposable PostgreSQL database: %v", err)
	}
	t.Cleanup(pool.Close)
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin schema transaction: %v", err)
	}
	t.Cleanup(func() {
		_ = tx.Rollback(t.Context())
	})

	return tx
}

func insertRunAndBuild(t *testing.T, tx pgx.Tx) int64 {
	t.Helper()
	var runID int64
	err := tx.QueryRow(t.Context(), `
		INSERT INTO ingest.corpus_run (
			run_uuid, recipe_digest, input_manifest_ref, input_manifest_digest,
			discovery_cutoff, relation_backfill_cutoff
		) VALUES (
			'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa', 'run-recipe',
			'cas://run-input', repeat('1', 64), now(), now()
		) RETURNING id
	`).Scan(&runID)
	if err != nil {
		t.Fatalf("insert corpus run: %v", err)
	}

	var buildID int64
	err = tx.QueryRow(t.Context(), `
		INSERT INTO ingest.corpus_build (
			run_id, build_uuid, discovery_cutoff, relation_backfill_cutoff,
			source_manifest_ref, source_manifest_digest, max_manifest_items,
			max_manifest_bytes, config_digest, parser_recipe_digest,
			policy_recipe_digest, citation_recipe_digest, chunking_recipe_digest
		) VALUES (
			$1, 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb', now(), now(),
			'cas://source-manifest', repeat('2', 64), 100, 1048576,
			'config-v1', 'parser-v1', 'policy-v1', 'citation-v1', 'chunk-v1'
		) RETURNING id
	`, runID).Scan(&buildID)
	if err != nil {
		t.Fatalf("insert corpus build: %v", err)
	}
	return buildID
}

func completeStageArgs(buildID int64, owner string, fence int64, now time.Time) pgx.NamedArgs {
	return pgx.NamedArgs{
		"build_id": buildID, "stage": "fetch", "shard_key": "shard-1",
		"idempotency_key": "fetch:shard-1:recipe-v1", "input_manifest_ref": "cas://input",
		"input_manifest_digest": strings.Repeat("a", 64), "output_manifest_ref": "cas://output",
		"output_manifest_digest": strings.Repeat("b", 64), "recipe_digest": "recipe-v1",
		"result_digest": "result-v1", "writer_owner": owner, "fence_token": fence,
		"next_cursor": "", "next_eligible_at": nil, "expected_count": int64(1),
		"processed_count": int64(1), "succeeded_count": int64(1),
		"pending_retry_count": int64(0), "terminal_gap_count": int64(0),
		"accepted_gap_count": int64(0), "rejected_input_count": int64(0),
		"failure_count": int64(0), "gap_acceptance_ref": "", "complete": true,
		"completion_state": "complete", "created_at": now,
	}
}

func insertRelease(t *testing.T, tx pgx.Tx, buildID int64, key, state string, manifest bool) int64 {
	t.Helper()
	var releaseID int64
	var readyAt any
	if state == "ready" {
		readyAt = time.Now().UTC()
	}
	err := tx.QueryRow(t.Context(), `
		INSERT INTO ingest.corpus_release (
			build_id, release_key, snapshot_ref, state, validated_at, ready_at
		) VALUES ($1, $2, $3, $4, $5, $5)
		RETURNING id
	`, buildID, key, "snapshot://"+key, state, readyAt).Scan(&releaseID)
	if err != nil {
		t.Fatalf("insert corpus release %s: %v", key, err)
	}
	if !manifest {
		return releaseID
	}
	err = insertReleaseManifestCounts(t, tx, releaseID, 1, 1, 1, 1)
	if err != nil {
		t.Fatalf("insert release manifest %s: %v", key, err)
	}
	return releaseID
}

func insertReleaseManifestCounts(
	t *testing.T,
	tx pgx.Tx,
	releaseID, sourceCount, documentCount, provisionCount, chunkCount int64,
) error {
	t.Helper()
	_, err := tx.Exec(t.Context(), `
		INSERT INTO ingest.release_manifest (
			release_id, corpus_hash, blob_set_digest, source_manifest_digest,
			schema_migration_digest,
			citation_recipe_digest, chunking_recipe_digest, embedding_model_digest,
			embedding_tokenizer_digest, embedding_preprocessing_digest,
			embedding_dimensions, lexical_revision, lexical_revision_digest,
			lexical_recipe_digest, lexical_tokenizer_digest,
			lexical_preprocessing_digest, lexical_dimensions, taxonomy_release_id,
			taxonomy_content_digest, config_digest,
			evaluation_artifact_ref, evaluation_artifact_digest,
			compatible_server_image_digest, compatible_embedder_image_digest,
			source_observation_count, document_revision_count, provision_revision_count,
			chunk_count, citation_count, provenance_count, dense_vector_count,
			lexical_chunk_count, gap_count, accepted_gap_count, gap_acceptance_ref
		) VALUES (
			$1, repeat('1', 64), repeat('2', 64), repeat('3', 64), repeat('4', 64),
			repeat('5', 64), repeat('6', 64), repeat('7', 64),
			repeat('8', 64), repeat('9', 64), 1024, 'lexical-v1', repeat('a', 64),
			repeat('b', 64), repeat('c', 64), repeat('d', 64), 1048576, 1,
			repeat('e', 64), repeat('f', 64), 'cas://evaluation', repeat('0', 64),
			'sha256:server', 'sha256:embedder', $2, $3, $4, $5,
			$5, $5, $5, $5, 0, 0, ''
		)
	`, releaseID, sourceCount, documentCount, provisionCount, chunkCount)
	return err
}

func namedQuery(t *testing.T, relativePath, name string) string {
	t.Helper()
	data, err := os.ReadFile(repositoryPath(t, relativePath))
	if err != nil {
		t.Fatalf("read %s: %v", relativePath, err)
	}
	marker := "-- name: " + name + " "
	text := string(data)
	start := strings.Index(text, marker)
	if start < 0 {
		t.Fatalf("query %s not found in %s", name, relativePath)
	}
	start = strings.IndexByte(text[start:], '\n') + start + 1
	endOffset := strings.Index(text[start:], "\n-- name: ")
	if endOffset < 0 {
		return strings.TrimSpace(text[start:])
	}
	return strings.TrimSpace(text[start : start+endOffset])
}

func repositoryPath(t *testing.T, relativePath string) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve schema test source path")
	}
	return filepath.Join(filepath.Dir(filename), "..", "..", "..", relativePath)
}

func assertSQLState(t *testing.T, tx pgx.Tx, want string, fn func() error) {
	t.Helper()
	if _, err := tx.Exec(t.Context(), "SAVEPOINT expected_error"); err != nil {
		t.Fatalf("create expected-error savepoint: %v", err)
	}
	err := fn()
	if err == nil {
		t.Fatalf("operation succeeded, want PostgreSQL state %s", want)
	}
	if _, rollbackErr := tx.Exec(t.Context(), "ROLLBACK TO SAVEPOINT expected_error"); rollbackErr != nil {
		t.Fatalf("rollback expected-error savepoint: %v", rollbackErr)
	}
	if _, releaseErr := tx.Exec(t.Context(), "RELEASE SAVEPOINT expected_error"); releaseErr != nil {
		t.Fatalf("release expected-error savepoint: %v", releaseErr)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("error = %T %v, want PostgreSQL state %s", err, err, want)
	}
	if pgErr.Code != want {
		t.Fatalf("PostgreSQL state = %s, want %s: %v", pgErr.Code, want, err)
	}
}
