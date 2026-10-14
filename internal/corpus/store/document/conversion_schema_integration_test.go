package document

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"danny.vn/banhmi/internal/corpus/model"
)

//go:embed queries/conversion.sql
var conversionQueries string

const conversionOtherDigest = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
const conversionNewDigest = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

type conversionSeed struct {
	execution int64
	revision  int64
}

func conversionExec(t *testing.T, ctx context.Context, tx pgx.Tx, query string, args ...any) {
	t.Helper()
	if _, err := tx.Exec(ctx, query, args...); err != nil {
		t.Fatalf("conversion fixture SQL: %v", err)
	}
}

func conversionState(t *testing.T, ctx context.Context, tx pgx.Tx, state, query string, args ...any) {
	t.Helper()
	conversionExec(t, ctx, tx, "SAVEPOINT conversion_negative")
	_, err := tx.Exec(ctx, query, args...)
	var pgerr *pgconn.PgError
	if !errors.As(err, &pgerr) || pgerr.Code != state {
		t.Fatalf("want PostgreSQL state %s, got %v", state, err)
	}
	conversionExec(t, ctx, tx, "ROLLBACK TO SAVEPOINT conversion_negative")
	conversionExec(t, ctx, tx, "RELEASE SAVEPOINT conversion_negative")
}

func conversionQuery(t *testing.T, name string) string {
	t.Helper()
	_, tail, ok := strings.Cut(conversionQueries, "-- name: "+name+" :")
	if !ok {
		t.Fatal("missing conversion query " + name)
	}
	_, tail, ok = strings.Cut(tail, "\n")
	if !ok {
		t.Fatal("missing conversion query body " + name)
	}
	body, _, _ := strings.Cut(tail, "\n-- name:")
	return body
}

// Read the first bigint without depending on the phase 2 generated package.
func conversionID(t *testing.T, ctx context.Context, tx pgx.Tx, name string, args ...any) (int64, error) {
	t.Helper()
	rows, err := tx.Query(ctx, conversionQuery(t, name), args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return 0, err
		}
		return 0, pgx.ErrNoRows
	}
	values, err := rows.Values()
	if err != nil {
		return 0, err
	}
	id, ok := values[0].(int64)
	if !ok {
		t.Fatal("conversion mutation must return bigint id first")
	}
	if rows.Next() {
		t.Fatal("conversion mutation returned multiple rows")
	}
	return id, rows.Err()
}

func conversionRequireID(t *testing.T, ctx context.Context, tx pgx.Tx, name string, args ...any) int64 {
	t.Helper()
	id, err := conversionID(t, ctx, tx, name, args...)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return id
}

func conversionNoRow(t *testing.T, ctx context.Context, tx pgx.Tx, name string, args ...any) {
	t.Helper()
	if _, err := conversionID(t, ctx, tx, name, args...); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("%s: want no row, got %v", name, err)
	}
}

func conversionCount(t *testing.T, ctx context.Context, tx pgx.Tx, query string, args ...any) int64 {
	t.Helper()
	var count int64
	if err := tx.QueryRow(ctx, query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func conversionJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func seedConversion(t *testing.T, ctx context.Context, tx pgx.Tx) conversionSeed {
	t.Helper()
	conversionExec(t, ctx, tx, `INSERT INTO ingest.country_admission
		(id, country_code, admission_epoch, admitted_writer, enabled) VALUES (1, 'vn', 1, 'writer-a', true)`)
	conversionExec(t, ctx, tx, `INSERT INTO ingest.execution_lease
		(resource_kind, resource_key, owner, attempt_fence, admission_epoch, expires_at)
		VALUES ('document_version', 'version-a', 'owner-a', 1, 1, clock_timestamp() + interval '1 hour')`)
	var seed conversionSeed
	if err := tx.QueryRow(ctx, `INSERT INTO ingest.execution
		(execution_key, workflow_id, workflow_kind, processing_attempt_key, attempt_fence,
		 admission_epoch, scope_kind, resource_kind, resource_key, input_digest, recipe_digest,
		 budget_digest, budget, run_receipts)
		VALUES ($1, 'document-ingest/vn/source-a/version-a', 'DocumentIngest', 'attempt-a', 1,
		 1, 'intake', 'document_version', 'version-a', $2, $2, $2, '{}', '[]') RETURNING id`,
		executionKey, identityDigest).Scan(&seed.execution); err != nil {
		t.Fatal(err)
	}
	for _, hash := range []string{identityDigest, conversionOtherDigest, conversionNewDigest} {
		conversionExec(t, ctx, tx, `INSERT INTO bronze.blob
			(sha256, byte_size, media_type, object_ref, capture_source, capture_ref, captured_at)
			VALUES ($1, 16, 'application/octet-stream', 'synthetic-object', 'source-a',
			'capture-00000000000000000000000000000001', clock_timestamp())`, hash)
	}
	conversionExec(t, ctx, tx, `INSERT INTO silver.document_identity
		(identity_key, entity_kind, created_by_execution_key) VALUES ($1, 'document', $2)`, identityKey, executionKey)
	if err := tx.QueryRow(ctx, `INSERT INTO silver.document_revision
		(document_identity_id, revision_key, source_observation_set_hash, text_hash, metadata_hash,
		 conversion_recipe_digest, normalization_recipe_digest, parser_recipe_digest, policy_recipe_digest,
		 normalized_number, document_type, document_version_key, intake_execution_id, intake_execution_key)
		SELECT d.id, $1, $2, $2, $2, $2, $2, $2, $2, '50/2024/TT-NHNN', 'thong-tu', 'version-a', $3, $4
		FROM silver.document_identity d RETURNING id`, revisionKey, identityDigest, seed.execution, executionKey).Scan(&seed.revision); err != nil {
		t.Fatal(err)
	}
	return seed
}

func conversionArgs(seed conversionSeed, candidate string, payload ...any) []any {
	return identityFence(seed.execution, append([]any{seed.revision, revisionKey, candidate}, payload...)...)
}

func conversionArtifact(role string, ordinal int) map[string]any {
	return map[string]any{
		"role": role, "ordinal": ordinal, "blob_hash": identityDigest, "byte_size": 16,
		"schema_version": "converted-v1", "original_hash": identityDigest,
		"conversion_recipe_digest": identityDigest, "normalization_recipe_digest": identityDigest,
		"detector_digest": identityDigest, "manifest_hash": identityDigest, "manifest_byte_size": 16,
	}
}

func conversionSegment(ordinal int) map[string]any {
	return map[string]any{
		"map_hash": identityDigest, "source_block_id": "block-a", "destination_block_id": "block-b",
		"source_space": "raw_text", "destination_space": "normalized_text",
		"source_start": 0, "source_end": 1, "destination_start": 0, "destination_end": 1,
		"transform_order": 0, "segment_ordinal": ordinal, "transform": "nfc",
	}
}

func conversionLedger() map[string]any {
	return map[string]any{
		"ledger_hash": identityDigest, "operation_ordinal": 0, "operation": "replace", "rule": "nfc", "rule_version": "1",
		"input_hash": identityDigest, "output_hash": identityDigest,
		"input_start": 0, "input_end": 1, "output_start": 0, "output_end": 1,
		"removed_bytes": "61", "replacement_bytes": "61", "encoding_evidence": map[string]any{},
		"font_evidence": map[string]any{}, "source_anchors": []any{},
	}
}

func conversionFinding(ordinal int, severity string) map[string]any {
	return map[string]any{
		"ordinal": ordinal, "record_kind": "finding", "code": "BYTE_MAPPING_UNAVAILABLE",
		"severity": severity, "scope_kind": "artifact", "scope_key": "artifact-a",
		"reason": "BYTE_MAPPING_UNAVAILABLE", "evidence": []any{}, "source_spans": []any{},
	}
}

func conversionReport(outcome string, records ...map[string]any) map[string]any {
	if records == nil {
		records = []map[string]any{}
	}
	return map[string]any{
		"report_hash": identityDigest, "report_byte_size": 16, "artifact_hash": identityDigest,
		"policy_digest": identityDigest, "threshold_version": "1", "outcome": outcome, "records": records,
	}
}

func conversionComparison(prior, candidate int64) map[string]any {
	return map[string]any{
		"prior_report_id": prior, "candidate_report_id": candidate, "prior_revision_key": revisionKey,
		"prior_candidate_digest": identityDigest, "comparison_recipe_digest": identityDigest,
		"result_hash": identityDigest, "result_byte_size": 16, "acceptance_watermark": 0,
	}
}

// The shared fixture validates the hosted URL and copies the replayed database.
// Every SQL helper in this file uses that fixture's bounded context.
func TestConversionProvenanceSchema(t *testing.T) {
	f := identityFixture(t)
	tx := identityTx(t, f, false)
	seed := seedConversion(t, f.ctx, tx)
	conversionRequireID(t, f.ctx, tx, "RegisterConversionArtifact", conversionArgs(seed, identityDigest,
		conversionJSON(t, conversionArtifact("raw_text", 0)))...)
	conversionRequireID(t, f.ctx, tx, "InsertOffsetMapSegments", conversionArgs(seed, identityDigest,
		identityDigest, identityDigest, conversionJSON(t, []any{conversionSegment(0)}))...)
	conversionRequireID(t, f.ctx, tx, "AppendEditLedgerEntry", conversionArgs(seed, identityDigest,
		conversionJSON(t, conversionLedger()))...)
	prior := conversionRequireID(t, f.ctx, tx, "RecordConversionQualityReport", conversionArgs(seed, identityDigest,
		conversionJSON(t, conversionReport("accepted", conversionFinding(0, "info"))))...)
	candidate := conversionRequireID(t, f.ctx, tx, "RecordConversionQualityReport", conversionArgs(seed, conversionOtherDigest,
		conversionJSON(t, conversionReport("quarantined")))...)
	conversionRequireID(t, f.ctx, tx, "RecordRevisionComparison", conversionArgs(seed, conversionOtherDigest,
		conversionJSON(t, conversionComparison(prior, candidate)))...)
	for _, table := range []string{"conversion_artifact", "offset_map_segment", "edit_ledger", "quality_report", "quality_finding", "revision_comparison"} {
		conversionState(t, f.ctx, tx, "55000", "UPDATE silver."+table+" SET candidate_digest = candidate_digest")
		conversionState(t, f.ctx, tx, "55000", "DELETE FROM silver."+table)
		if conversionCount(t, f.ctx, tx, "SELECT count(*) FROM silver."+table) == 0 {
			t.Fatalf("immutable %s lost evidence", table)
		}
	}
	for _, read := range []struct {
		name string
		args []any
	}{
		{"ReadConversionArtifacts", []any{revisionKey, "raw_text"}},
		{"ReadOffsetMapSegments", []any{revisionKey, "block-a"}},
		{"ReadEditLedgerByHashes", []any{revisionKey, identityDigest, identityDigest, identityDigest}},
		{"ReadConversionQualityReports", []any{revisionKey, identityDigest}},
		{"ReadConversionQualityFindings", []any{prior}},
		{"ReadRevisionComparisons", []any{identityDigest, conversionOtherDigest}},
	} {
		conversionRequireID(t, f.ctx, tx, read.name, read.args...)
	}
}

func TestConversionBindingsAndReplay(t *testing.T) {
	f := identityFixture(t)
	tx := identityTx(t, f, false)
	seed := seedConversion(t, f.ctx, tx)
	artifact := conversionJSON(t, conversionArtifact("raw_text", 0))
	args := conversionArgs(seed, identityDigest, artifact)
	id := conversionRequireID(t, f.ctx, tx, "RegisterConversionArtifact", args...)
	if replay := conversionRequireID(t, f.ctx, tx, "RegisterConversionArtifact", args...); replay != id {
		t.Fatal("artifact replay changed identity")
	}
	changed := conversionArtifact("raw_text", 0)
	changed["blob_hash"] = conversionOtherDigest
	conversionNoRow(t, f.ctx, tx, "RegisterConversionArtifact", conversionArgs(seed, identityDigest, conversionJSON(t, changed))...)
	if conversionCount(t, f.ctx, tx, "SELECT count(*) FROM silver.conversion_artifact") != 1 {
		t.Fatal("digest mismatch wrote an artifact")
	}
	for _, change := range []struct {
		index int
		value any
	}{
		{0, int64(2)}, {1, "writer-other"}, {2, "source_window"}, {3, "wrong-version"},
		{4, "owner-other"}, {5, int64(2)}, {6, int64(9999)}, {7, conversionOtherDigest},
		{8, conversionOtherDigest}, {9, conversionOtherDigest}, {10, int64(9999)},
		{11, otherIdentityKey + ":rev:" + identityDigest},
	} {
		bad := append([]any{}, args...)
		bad[change.index] = change.value
		conversionNoRow(t, f.ctx, tx, "RegisterConversionArtifact", bad...)
	}
	changed["blob_hash"], changed["byte_size"] = identityDigest, 17
	conversionNoRow(t, f.ctx, tx, "RegisterConversionArtifact", conversionArgs(seed, identityDigest, conversionJSON(t, changed))...)
	conversionState(t, f.ctx, tx, "23514", `INSERT INTO silver.conversion_artifact
		(document_revision_id, revision_key, document_version_key, processing_execution_key, candidate_digest,
		 role, ordinal, blob_hash, byte_size, schema_version, original_hash, conversion_recipe_digest,
		 normalization_recipe_digest, detector_digest, manifest_hash, manifest_byte_size)
		VALUES ($1, $2, 'wrong-version', $3, $4, 'raw_text', 1, $4, 16, 'v1', $4, $4, $4, $4, $4, 16)`,
		seed.revision, revisionKey, executionKey, identityDigest)
	ledgerArgs := conversionArgs(seed, identityDigest, conversionJSON(t, conversionLedger()))
	ledgerID := conversionRequireID(t, f.ctx, tx, "AppendEditLedgerEntry", ledgerArgs...)
	if replay := conversionRequireID(t, f.ctx, tx, "AppendEditLedgerEntry", ledgerArgs...); replay != ledgerID {
		t.Fatal("ledger replay changed identity")
	}
	changedLedger := conversionLedger()
	changedLedger["rule_version"] = "2"
	conversionNoRow(t, f.ctx, tx, "AppendEditLedgerEntry", conversionArgs(seed, identityDigest, conversionJSON(t, changedLedger))...)
	secondLedger := conversionLedger()
	secondLedger["operation_ordinal"] = 1
	secondLedger["input_start"], secondLedger["input_end"] = 2, 3
	secondLedger["output_start"], secondLedger["output_end"] = 2, 3
	secondArgs := conversionArgs(seed, identityDigest, conversionJSON(t, secondLedger))
	secondID := conversionRequireID(t, f.ctx, tx, "AppendEditLedgerEntry", secondArgs...)
	if secondID == ledgerID || conversionCount(t, f.ctx, tx, "SELECT count(*) FROM silver.edit_ledger") != 2 {
		t.Fatal("same artifact pair did not retain both operations")
	}
	if replay := conversionRequireID(t, f.ctx, tx, "AppendEditLedgerEntry", secondArgs...); replay != secondID {
		t.Fatal("second ledger operation replay changed identity")
	}
	secondLedger["replacement_bytes"] = "62"
	conversionNoRow(t, f.ctx, tx, "AppendEditLedgerEntry", conversionArgs(seed, identityDigest, conversionJSON(t, secondLedger))...)
	conversionExec(t, f.ctx, tx, "UPDATE ingest.execution_lease SET cancelled_at = clock_timestamp()")
	conversionNoRow(t, f.ctx, tx, "RegisterConversionArtifact", args...)
	conversionExec(t, f.ctx, tx, "UPDATE ingest.execution_lease SET cancelled_at = NULL, expires_at = clock_timestamp() - interval '1 second'")
	conversionNoRow(t, f.ctx, tx, "RegisterConversionArtifact", args...)
}

func TestConversionMapBoundsAndBulkConflict(t *testing.T) {
	f := identityFixture(t)
	tx := identityTx(t, f, false)
	seed := seedConversion(t, f.ctx, tx)
	conversionRequireID(t, f.ctx, tx, "RegisterConversionArtifact", conversionArgs(seed, identityDigest,
		conversionJSON(t, conversionArtifact("raw_text", 0)))...)
	insert := func(items ...map[string]any) []any {
		return conversionArgs(seed, identityDigest, identityDigest, identityDigest, conversionJSON(t, items))
	}
	conversionRequireID(t, f.ctx, tx, "InsertOffsetMapSegments", insert(conversionSegment(0))...)
	conflict := conversionSegment(0)
	conflict["source_end"] = 2
	conversionNoRow(t, f.ctx, tx, "InsertOffsetMapSegments", insert(conflict, conversionSegment(1))...)
	if conversionCount(t, f.ctx, tx, "SELECT count(*) FROM silver.offset_map_segment") != 1 {
		t.Fatal("conflicting bulk input partially inserted")
	}
	for _, patch := range []map[string]any{
		{"source_start": -1}, {"source_start": 2, "source_end": 1},
		{"destination_start": -1}, {"destination_start": 2, "destination_end": 1},
		{"source_start": nil}, {"source_start": nil, "source_end": nil},
		{"source_start": nil, "source_end": nil, "source_reason": "BYTE_MAPPING_UNAVAILABLE"},
	} {
		bad := conversionSegment(1)
		for key, value := range patch {
			bad[key] = value
		}
		conversionState(t, f.ctx, tx, "23514", conversionQuery(t, "InsertOffsetMapSegments"), insert(bad)...)
	}
	oversize := conversionSegment(1)
	oversize["source_end"] = 17
	conversionNoRow(t, f.ctx, tx, "InsertOffsetMapSegments", insert(oversize)...)
	oversize = conversionSegment(1)
	oversize["destination_end"] = 17
	conversionNoRow(t, f.ctx, tx, "InsertOffsetMapSegments", insert(oversize)...)
	unavailable := conversionSegment(1)
	unavailable["source_start"], unavailable["source_end"] = nil, nil
	unavailable["source_reason"], unavailable["source_page"] = "BYTE_MAPPING_UNAVAILABLE", 1
	conversionRequireID(t, f.ctx, tx, "InsertOffsetMapSegments", insert(unavailable)...)
	empty := conversionSegment(2)
	empty["source_end"], empty["destination_end"] = 0, 0
	conversionRequireID(t, f.ctx, tx, "InsertOffsetMapSegments", insert(empty)...)
	member := conversionSegment(0)
	member["source_space"], member["source_member"] = "member_bytes", "word/document.xml"
	conversionRequireID(t, f.ctx, tx, "InsertOffsetMapSegments", conversionArgs(seed, identityDigest,
		conversionOtherDigest, identityDigest, conversionJSON(t, []any{member}))...)
}

func TestConversionQualityAtomicAndReserved(t *testing.T) {
	f := identityFixture(t)
	tx := identityTx(t, f, false)
	seed := seedConversion(t, f.ctx, tx)
	bad := conversionJSON(t, conversionReport("quarantined", conversionFinding(0, "outsider")))
	conversionState(t, f.ctx, tx, "23514", conversionQuery(t, "RecordConversionQualityReport"), conversionArgs(seed, identityDigest, bad)...)
	if conversionCount(t, f.ctx, tx, "SELECT count(*) FROM silver.quality_report") != 0 ||
		conversionCount(t, f.ctx, tx, "SELECT count(*) FROM silver.quality_finding") != 0 {
		t.Fatal("bad finding left a report or finding")
	}
	good := conversionJSON(t, conversionReport("quarantined", conversionFinding(0, "blocking")))
	id := conversionRequireID(t, f.ctx, tx, "RecordConversionQualityReport", conversionArgs(seed, identityDigest, good)...)
	if replay := conversionRequireID(t, f.ctx, tx, "RecordConversionQualityReport", conversionArgs(seed, identityDigest, good)...); replay != id {
		t.Fatal("report replay changed identity")
	}
	conversionState(t, f.ctx, tx, "23514", `INSERT INTO silver.quality_finding
		(document_revision_id, revision_key, document_version_key, processing_execution_key, candidate_digest,
		 quality_report_id, report_hash, ordinal, record_kind, code, severity, scope_kind, scope_key, reason, evidence, source_spans)
		SELECT document_revision_id, revision_key, document_version_key, processing_execution_key, candidate_digest,
		 quality_report_id, report_hash, 1, record_kind, code, severity, scope_kind, scope_key, reason, evidence, source_spans
		FROM silver.quality_finding WHERE quality_report_id = $1`, id)
	changed := conversionJSON(t, conversionReport("quarantined", conversionFinding(0, "warning")))
	conversionNoRow(t, f.ctx, tx, "RecordConversionQualityReport", conversionArgs(seed, identityDigest, changed)...)
	rollup := conversionReport("accepted_with_gaps")
	rollup["artifact_hash"], rollup["report_hash"] = nil, conversionOtherDigest
	conversionRequireID(t, f.ctx, tx, "RecordConversionQualityReport", conversionArgs(seed, identityDigest, conversionJSON(t, rollup))...)
	reservedKey := identityKey + ":rev:" + conversionOtherDigest
	reserved := identityFence(seed.execution, nil, reservedKey, conversionOtherDigest,
		conversionJSON(t, conversionReport("needs_ocr")))
	reservedReport := conversionRequireID(t, f.ctx, tx, "RecordConversionQualityReport", reserved...)
	otherReserved := append([]any{}, reserved...)
	otherReserved[12] = conversionNewDigest
	otherReservedReport := conversionRequireID(t, f.ctx, tx, "RecordConversionQualityReport", otherReserved...)
	comparison := conversionComparison(reservedReport, otherReservedReport)
	comparison["prior_revision_key"], comparison["prior_candidate_digest"] = reservedKey, conversionOtherDigest
	comparisonArgs := identityFence(seed.execution, nil, reservedKey, conversionNewDigest, conversionJSON(t, comparison))
	conversionRequireID(t, f.ctx, tx, "RecordRevisionComparison", comparisonArgs...)
	if conversionCount(t, f.ctx, tx, "SELECT count(*) FROM silver.document_revision") != 1 {
		t.Fatal("reserved report fabricated a revision")
	}
	reserved[11] = revisionKey
	conversionNoRow(t, f.ctx, tx, "RecordConversionQualityReport", reserved...)
	wrong := conversionArgs(seed, conversionNewDigest, good)
	wrong[10] = int64(9999)
	conversionNoRow(t, f.ctx, tx, "RecordConversionQualityReport", wrong...)
}

func TestConversionClosedVocabularies(t *testing.T) {
	f := identityFixture(t)
	tx := identityTx(t, f, false)
	seed := seedConversion(t, f.ctx, tx)
	conversionRequireID(t, f.ctx, tx, "RegisterConversionArtifact", conversionArgs(seed, identityDigest,
		conversionJSON(t, conversionArtifact("raw_text", 0)))...)
	check := func(name string, values []string, args func(string) []any) {
		t.Helper()
		for _, value := range values {
			conversionExec(t, f.ctx, tx, "SAVEPOINT vocabulary")
			conversionRequireID(t, f.ctx, tx, name, args(value)...)
			conversionExec(t, f.ctx, tx, "ROLLBACK TO SAVEPOINT vocabulary")
			conversionExec(t, f.ctx, tx, "RELEASE SAVEPOINT vocabulary")
		}
		conversionState(t, f.ctx, tx, "23514", conversionQuery(t, name), args("outsider")...)
	}
	check("RegisterConversionArtifact", []string{
		string(model.ConversionRawBlocks), string(model.ConversionNormalizedBlocks), string(model.ConversionRawText),
		string(model.ConversionNormalizedText), string(model.ConversionManifest),
	}, func(value string) []any {
		return conversionArgs(seed, identityDigest, conversionJSON(t, conversionArtifact(value, 1)))
	})
	spaces := []string{string(model.ProvenanceOriginalBytes), string(model.ProvenanceMemberBytes),
		string(model.ProvenanceRawText), string(model.ProvenanceNormalizedText)}
	for _, side := range []string{"source_space", "destination_space"} {
		check("InsertOffsetMapSegments", spaces, func(value string) []any {
			segment := conversionSegment(0)
			segment[side] = value
			return conversionArgs(seed, identityDigest, identityDigest, identityDigest, conversionJSON(t, []any{segment}))
		})
	}
	check("RecordConversionQualityReport", []string{string(model.QualityAccepted), string(model.QualityAcceptedWithGaps),
		string(model.QualityQuarantined), string(model.QualityNeedsOCR)}, func(value string) []any {
		return conversionArgs(seed, identityDigest, conversionJSON(t, conversionReport(value)))
	})
	check("RecordConversionQualityReport", []string{string(model.SeverityInfo), string(model.SeverityWarning), string(model.SeverityBlocking)}, func(value string) []any {
		return conversionArgs(seed, identityDigest, conversionJSON(t, conversionReport("quarantined", conversionFinding(0, value))))
	})
	check("RecordConversionQualityReport", []string{string(model.GapSourceWindow), string(model.GapArtifact), string(model.GapDocumentRevision),
		string(model.GapProvision), string(model.GapPart)}, func(value string) []any {
		finding := conversionFinding(0, "warning")
		finding["scope_kind"] = value
		return conversionArgs(seed, identityDigest, conversionJSON(t, conversionReport("quarantined", finding)))
	})
	metric := func(kind, state string) map[string]any {
		entry := map[string]any{
			"ordinal": 0, "record_kind": "metric", "code": kind, "metric_kind": kind, "metric_state": state,
			"numerator": 1, "denominator": 1, "threshold": "1", "mandatory": true, "passed": true,
			"evidence": []any{}, "source_spans": []any{},
		}
		if state != "measured" {
			entry["numerator"], entry["denominator"], entry["passed"], entry["reason"] = 0, 0, false, "NO_APPLICABLE_INPUT"
		}
		return entry
	}
	check("RecordConversionQualityReport", []string{string(model.MetricPageAccounting), string(model.MetricTextCoverage),
		string(model.MetricDiacriticValidity), string(model.MetricCorruption), string(model.MetricStructureCompleteness),
		string(model.MetricCitationParse), string(model.MetricTableIntegrity), string(model.MetricMetadataAgreement),
		string(model.MetricProvenanceCompleteness), string(model.MetricGeometry), string(model.MetricFidelity)}, func(value string) []any {
		return conversionArgs(seed, identityDigest, conversionJSON(t, conversionReport("quarantined", metric(value, "measured"))))
	})
	check("RecordConversionQualityReport", []string{string(model.MetricMeasured), string(model.MetricUnknown), string(model.MetricNotApplicable)}, func(value string) []any {
		return conversionArgs(seed, identityDigest, conversionJSON(t, conversionReport("quarantined", metric("page_accounting", value))))
	})
	check("RecordConversionQualityReport", []string{string(model.QualityRecordMetric), string(model.QualityRecordFinding), string(model.QualityRecordGap)}, func(value string) []any {
		entry := conversionFinding(0, "warning")
		if value == "metric" {
			entry = metric("page_accounting", "measured")
		}
		entry["record_kind"] = value
		return conversionArgs(seed, identityDigest, conversionJSON(t, conversionReport("quarantined", entry)))
	})
}

func seedConversionAcceptance(t *testing.T, ctx context.Context, tx pgx.Tx, seed conversionSeed, digest string, watermark int64) int64 {
	t.Helper()
	var result int64
	if err := tx.QueryRow(ctx, `INSERT INTO ingest.execution_result
		(result_key, execution_id, stage, substep, shard, idempotency_key, input_digest, output_digest,
		 recipe_digest, budget_digest, decision_revision, outcome, expected, processed, succeeded,
		 pending_retry, terminal_gap, rejected, failed, accepted_gaps, cursor, complete, payload)
		VALUES ($1, $2, 'normalize', 'quality', 'document', $3, $4, $4, $4, $4, 0, 'succeeded',
		 1, 1, 1, 0, 0, 0, 0, 0, '', true, '{}') RETURNING id`,
		fmt.Sprintf("result-%032x", watermark), seed.execution, fmt.Sprintf("sha256:%064x", watermark), digest).Scan(&result); err != nil {
		t.Fatal(err)
	}
	var acceptance int64
	if err := tx.QueryRow(ctx, `INSERT INTO ingest.revision_acceptance
		(revision_key, processing_attempt_key, execution_id, execution_result_id, candidate_digest,
		 quality_report_hash, decision_revision, state, scoped_gaps, watermark)
		VALUES ($1, 'attempt-a', $2, $3, $4, $5, 0, 'accepted', '[]', $6) RETURNING id`,
		revisionKey, seed.execution, result, digest, identityDigest, watermark).Scan(&acceptance); err != nil {
		t.Fatal(err)
	}
	conversionExec(t, ctx, tx, `INSERT INTO ingest.revision_acceptance_state
		(revision_key, processing_attempt_key, acceptance_id, state, watermark)
		VALUES ($1, 'attempt-a', $2, 'accepted', $3)
		ON CONFLICT (revision_key, processing_attempt_key) DO UPDATE
		SET acceptance_id = EXCLUDED.acceptance_id, state = EXCLUDED.state, watermark = EXCLUDED.watermark`,
		revisionKey, acceptance, watermark)
	return acceptance
}

func TestConversionComparisonDerivedStalenessAndRuntimePrivileges(t *testing.T) {
	f := identityFixture(t)
	setup := identityTx(t, f, false)
	seed := seedConversion(t, f.ctx, setup)
	if err := setup.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	// Connect as the actual app login after committing cross-schema fixtures.
	tx := identityTx(t, f, true)
	conversionRequireID(t, f.ctx, tx, "RegisterConversionArtifact", conversionArgs(seed, identityDigest,
		conversionJSON(t, conversionArtifact("raw_text", 0)))...)
	conversionRequireID(t, f.ctx, tx, "InsertOffsetMapSegments", conversionArgs(seed, identityDigest,
		identityDigest, identityDigest, conversionJSON(t, []any{conversionSegment(0)}))...)
	conversionRequireID(t, f.ctx, tx, "AppendEditLedgerEntry", conversionArgs(seed, identityDigest,
		conversionJSON(t, conversionLedger()))...)
	prior := conversionRequireID(t, f.ctx, tx, "RecordConversionQualityReport", conversionArgs(seed, identityDigest,
		conversionJSON(t, conversionReport("accepted", conversionFinding(0, "info"))))...)
	seedConversionAcceptance(t, f.ctx, tx, seed, identityDigest, 1)
	candidate := conversionRequireID(t, f.ctx, tx, "RecordConversionQualityReport", conversionArgs(seed, conversionOtherDigest,
		conversionJSON(t, conversionReport("quarantined")))...)
	comparisonPayload := conversionComparison(prior, candidate)
	comparisonPayload["acceptance_watermark"] = 1
	payload := conversionJSON(t, comparisonPayload)
	args := conversionArgs(seed, conversionOtherDigest, payload)
	for _, invalidWatermark := range []any{nil, -1, 0, 99} {
		invalid := conversionComparison(prior, candidate)
		invalid["acceptance_watermark"] = invalidWatermark
		conversionNoRow(t, f.ctx, tx, "RecordRevisionComparison", conversionArgs(seed, conversionOtherDigest,
			conversionJSON(t, invalid))...)
	}
	comparison := conversionRequireID(t, f.ctx, tx, "RecordRevisionComparison", args...)
	if replay := conversionRequireID(t, f.ctx, tx, "RecordRevisionComparison", args...); replay != comparison {
		t.Fatal("comparison replay changed identity")
	}
	conversionRequireID(t, f.ctx, tx, "ReadRevisionComparisons", identityDigest, conversionOtherDigest)
	for _, table := range []string{"conversion_artifact", "offset_map_segment", "edit_ledger", "quality_report", "quality_finding", "revision_comparison"} {
		conversionState(t, f.ctx, tx, "42501", "UPDATE silver."+table+" SET id = id")
		conversionState(t, f.ctx, tx, "42501", "DELETE FROM silver."+table)
		conversionState(t, f.ctx, tx, "42501", "TRUNCATE silver."+table)
		if conversionCount(t, f.ctx, tx, "SELECT count(*) FROM silver."+table) == 0 {
			t.Fatalf("runtime did not read %s", table)
		}
		var allowed bool
		if err := tx.QueryRow(f.ctx, "SELECT has_sequence_privilege(current_user, $1, 'USAGE')", "silver."+table+"_id_seq").Scan(&allowed); err != nil || !allowed {
			t.Fatalf("runtime lacks identity sequence usage for %s: %v", table, err)
		}
	}
	// Fenced comparison replay remains protected after removal of the definer.
	for _, change := range []struct {
		index int
		value any
	}{
		{0, int64(2)}, {1, "writer-other"}, {2, "source_window"}, {3, "wrong-version"}, {4, "owner-other"},
		{5, int64(2)}, {6, int64(9999)}, {7, conversionOtherDigest},
		{8, conversionOtherDigest}, {9, conversionOtherDigest}, {10, int64(9999)},
		{11, otherIdentityKey + ":rev:" + identityDigest},
	} {
		bad := append([]any{}, args...)
		bad[change.index] = change.value
		conversionNoRow(t, f.ctx, tx, "RecordRevisionComparison", bad...)
	}
	for index := range args {
		bad := append([]any{}, args...)
		bad[index] = nil
		conversionNoRow(t, f.ctx, tx, "RecordRevisionComparison", bad...)
	}
	conversionExec(t, f.ctx, tx, "SAVEPOINT comparison_lease")
	conversionExec(t, f.ctx, tx, "UPDATE ingest.execution_lease SET cancelled_at = clock_timestamp()")
	conversionNoRow(t, f.ctx, tx, "RecordRevisionComparison", args...)
	conversionExec(t, f.ctx, tx, "ROLLBACK TO SAVEPOINT comparison_lease")
	conversionExec(t, f.ctx, tx, "UPDATE ingest.execution_lease SET expires_at = clock_timestamp() - interval '1 second'")
	conversionNoRow(t, f.ctx, tx, "RecordRevisionComparison", args...)
	conversionExec(t, f.ctx, tx, "ROLLBACK TO SAVEPOINT comparison_lease")
	conversionExec(t, f.ctx, tx, "RELEASE SAVEPOINT comparison_lease")
	if conversionCount(t, f.ctx, tx, "SELECT count(*) FROM silver.revision_comparison") != 1 {
		t.Fatal("rejected comparison replay changed rows")
	}
	conversionExec(t, f.ctx, tx, `INSERT INTO ingest.execution_lease
		(resource_kind, resource_key, owner, attempt_fence, admission_epoch, expires_at)
		VALUES ('document_version', 'version-b', 'owner-a', 1, 1, clock_timestamp() + interval '1 hour')`)
	var secondExecution int64
	if err := tx.QueryRow(f.ctx, `INSERT INTO ingest.execution
		(execution_key, workflow_id, workflow_kind, processing_attempt_key, attempt_fence,
		 admission_epoch, scope_kind, resource_kind, resource_key, input_digest, recipe_digest,
		 budget_digest, budget, run_receipts)
		VALUES ($1, 'document-ingest/vn/source-a/version-b', 'DocumentIngest', 'attempt-b', 1,
		 1, 'intake', 'document_version', 'version-b', $2, $2, $2, '{}', '[]') RETURNING id`,
		"execution-"+strings.Repeat("b", 32), identityDigest).Scan(&secondExecution); err != nil {
		t.Fatal(err)
	}
	secondVersion := append([]any{}, args...)
	secondVersion[3], secondVersion[6] = "version-b", secondExecution
	conversionNoRow(t, f.ctx, tx, "RecordRevisionComparison", secondVersion...)
	if conversionCount(t, f.ctx, tx, "SELECT count(*) FROM silver.revision_comparison WHERE id = $1 AND acceptance_watermark = 1", comparison) != 1 {
		t.Fatal("second-version lease changed first-version comparison")
	}
	// Neither compared candidate invalidates the captured baseline.
	seedConversionAcceptance(t, f.ctx, tx, seed, identityDigest, 2)
	conversionRequireID(t, f.ctx, tx, "ReadRevisionComparisons", identityDigest, conversionOtherDigest)
	conversionRequireID(t, f.ctx, tx, "RecordRevisionComparison", args...)
	seedConversionAcceptance(t, f.ctx, tx, seed, conversionOtherDigest, 3)
	conversionRequireID(t, f.ctx, tx, "ReadRevisionComparisons", identityDigest, conversionOtherDigest)
	conversionRequireID(t, f.ctx, tx, "RecordRevisionComparison", args...)
	// A newer acceptance for another identity has no effect.
	conversionExec(t, f.ctx, tx, "SAVEPOINT unrelated_acceptance")
	unrelated := seedConversionAcceptance(t, f.ctx, tx, seed, conversionNewDigest, 4)
	// The event is immutable, so create the unrelated event from its fixture data.
	conversionExec(t, f.ctx, tx, `INSERT INTO ingest.revision_acceptance
		(revision_key, processing_attempt_key, execution_id, execution_result_id, candidate_digest,
		 quality_report_hash, decision_revision, state, scoped_gaps, watermark)
		SELECT $1, processing_attempt_key, execution_id, execution_result_id, candidate_digest,
		 quality_report_hash, decision_revision, state, scoped_gaps, watermark
		FROM ingest.revision_acceptance WHERE id = $2`, otherIdentityKey+":rev:"+identityDigest, unrelated)
	conversionExec(t, f.ctx, tx, `UPDATE ingest.revision_acceptance_state SET state = 'excluded'
		WHERE acceptance_id = $1`, unrelated)
	conversionExec(t, f.ctx, tx, `INSERT INTO ingest.revision_acceptance_state
		(revision_key, processing_attempt_key, acceptance_id, state, watermark)
		SELECT revision_key, processing_attempt_key, id, state, watermark FROM ingest.revision_acceptance
		WHERE revision_key = $1`, otherIdentityKey+":rev:"+identityDigest)
	conversionRequireID(t, f.ctx, tx, "ReadRevisionComparisons", identityDigest, conversionOtherDigest)
	conversionRequireID(t, f.ctx, tx, "RecordRevisionComparison", args...)
	conversionExec(t, f.ctx, tx, "ROLLBACK TO SAVEPOINT unrelated_acceptance")
	conversionExec(t, f.ctx, tx, "RELEASE SAVEPOINT unrelated_acceptance")
	conversionRequireID(t, f.ctx, tx, "RecordConversionQualityReport", conversionArgs(seed, conversionNewDigest,
		conversionJSON(t, conversionReport("accepted")))...)
	seedConversionAcceptance(t, f.ctx, tx, seed, conversionNewDigest, 4)
	conversionNoRow(t, f.ctx, tx, "ReadRevisionComparisons", identityDigest, conversionOtherDigest)
	conversionNoRow(t, f.ctx, tx, "RecordRevisionComparison", args...)
	// A new recipe cannot absorb C's watermark or record an obsolete baseline.
	for _, watermark := range []any{1, 4} {
		obsolete := conversionComparison(prior, candidate)
		obsolete["comparison_recipe_digest"] = conversionOtherDigest
		obsolete["acceptance_watermark"] = watermark
		conversionNoRow(t, f.ctx, tx, "RecordRevisionComparison", conversionArgs(seed, conversionOtherDigest,
			conversionJSON(t, obsolete))...)
	}
	conversionState(t, f.ctx, tx, "42501", "UPDATE silver.revision_comparison SET acceptance_watermark = 4 WHERE id = $1", comparison)
	conversionState(t, f.ctx, tx, "42501", "DELETE FROM silver.revision_comparison WHERE id = $1", comparison)
	if conversionCount(t, f.ctx, tx, "SELECT count(*) FROM silver.revision_comparison WHERE id = $1 AND acceptance_watermark = 1", comparison) != 1 ||
		conversionCount(t, f.ctx, tx, "SELECT count(*) FROM silver.revision_comparison") != 1 {
		t.Fatal("derived staleness changed immutable comparison content")
	}
}

func TestConversionWrongRevisionVersion(t *testing.T) {
	f := identityFixture(t)
	tx := identityTx(t, f, false)
	seed := seedConversion(t, f.ctx, tx)
	otherKey := identityKey + ":rev:" + conversionOtherDigest
	var other int64
	if err := tx.QueryRow(f.ctx, `INSERT INTO silver.document_revision
		(document_identity_id, revision_key, source_observation_set_hash, text_hash, metadata_hash,
		 conversion_recipe_digest, normalization_recipe_digest, parser_recipe_digest, policy_recipe_digest,
		 normalized_number, document_type, document_version_key, intake_execution_id, intake_execution_key)
		SELECT r.document_identity_id, $1, r.source_observation_set_hash, r.text_hash, r.metadata_hash,
		 r.conversion_recipe_digest, r.normalization_recipe_digest, r.parser_recipe_digest, r.policy_recipe_digest,
		 r.normalized_number, r.document_type, 'version-b', r.intake_execution_id, r.intake_execution_key
		FROM silver.document_revision r WHERE r.id = $2 RETURNING id`, otherKey, seed.revision).Scan(&other); err != nil {
		t.Fatal(err)
	}
	prior := conversionRequireID(t, f.ctx, tx, "RecordConversionQualityReport", conversionArgs(seed, identityDigest,
		conversionJSON(t, conversionReport("accepted")))...)
	candidate := conversionRequireID(t, f.ctx, tx, "RecordConversionQualityReport", conversionArgs(seed, conversionOtherDigest,
		conversionJSON(t, conversionReport("quarantined")))...)
	for _, mutation := range []struct {
		name    string
		payload []any
	}{
		{"RegisterConversionArtifact", []any{conversionJSON(t, conversionArtifact("raw_text", 0))}},
		{"InsertOffsetMapSegments", []any{identityDigest, identityDigest, conversionJSON(t, []any{conversionSegment(0)})}},
		{"AppendEditLedgerEntry", []any{conversionJSON(t, conversionLedger())}},
		{"RecordConversionQualityReport", []any{conversionJSON(t, conversionReport("accepted"))}},
		{"RecordRevisionComparison", []any{conversionJSON(t, conversionComparison(prior, candidate))}},
	} {
		wrong := conversionArgs(seed, conversionOtherDigest, mutation.payload...)
		wrong[10], wrong[11] = other, otherKey
		conversionNoRow(t, f.ctx, tx, mutation.name, wrong...)
		wrong[10] = int64(9999)
		conversionNoRow(t, f.ctx, tx, mutation.name, wrong...)
	}
	badComparison := conversionComparison(prior, candidate)
	badComparison["prior_candidate_digest"] = conversionNewDigest
	conversionNoRow(t, f.ctx, tx, "RecordRevisionComparison", conversionArgs(seed, conversionOtherDigest,
		conversionJSON(t, badComparison))...)
	if conversionCount(t, f.ctx, tx, "SELECT count(*) FROM silver.conversion_artifact") != 0 ||
		conversionCount(t, f.ctx, tx, "SELECT count(*) FROM silver.offset_map_segment") != 0 ||
		conversionCount(t, f.ctx, tx, "SELECT count(*) FROM silver.edit_ledger") != 0 ||
		conversionCount(t, f.ctx, tx, "SELECT count(*) FROM silver.revision_comparison") != 0 ||
		conversionCount(t, f.ctx, tx, "SELECT count(*) FROM silver.quality_report") != 2 {
		t.Fatal("wrong-version or dangling provenance wrote evidence")
	}
}

func TestConversionReportCommitCompleteness(t *testing.T) {
	f := identityFixture(t)
	setup := identityTx(t, f, false)
	seed := seedConversion(t, f.ctx, setup)
	if err := setup.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	for _, children := range []int{0, 1} {
		t.Run(fmt.Sprintf("underfilled-%d", children), func(t *testing.T) {
			tx := identityTx(t, f, true)
			var report int64
			if err := tx.QueryRow(f.ctx, `INSERT INTO silver.quality_report
				(document_revision_id, revision_key, document_version_key, processing_execution_key,
				 candidate_digest, report_hash, report_byte_size, artifact_hash, policy_digest,
				 threshold_version, outcome, record_count)
				VALUES ($1, $2, 'version-a', $3, $4, $4, 16, $4, $4, '1', 'quarantined', 2)
				RETURNING id`, seed.revision, revisionKey, executionKey, identityDigest).Scan(&report); err != nil {
				t.Fatal(err)
			}
			if children == 1 {
				conversionExec(t, f.ctx, tx, `INSERT INTO silver.quality_finding
					(document_revision_id, revision_key, document_version_key, processing_execution_key,
					 candidate_digest, quality_report_id, report_hash, ordinal, record_kind, code,
					 severity, scope_kind, scope_key, reason, evidence, source_spans)
					SELECT document_revision_id, revision_key, document_version_key, processing_execution_key,
					 candidate_digest, id, report_hash, 0, 'finding', 'BYTE_MAPPING_UNAVAILABLE',
					 'warning', 'artifact', 'artifact-a', 'BYTE_MAPPING_UNAVAILABLE', '[]', '[]'
					FROM silver.quality_report WHERE id = $1`, report)
			}
			var pgerr *pgconn.PgError
			if err := tx.Commit(f.ctx); !errors.As(err, &pgerr) || pgerr.Code != "23514" {
				t.Fatalf("underfilled report commit: want PostgreSQL state 23514, got %v", err)
			}
			check := identityTx(t, f, true)
			if conversionCount(t, f.ctx, check, "SELECT count(*) FROM silver.quality_report") != 0 ||
				conversionCount(t, f.ctx, check, "SELECT count(*) FROM silver.quality_finding") != 0 {
				t.Fatal("failed commit retained report content")
			}
		})
	}
	complete := identityTx(t, f, true)
	report := conversionRequireID(t, f.ctx, complete, "RecordConversionQualityReport", conversionArgs(seed, identityDigest,
		conversionJSON(t, conversionReport("quarantined", conversionFinding(0, "warning"), conversionFinding(1, "info"))))...)
	if err := complete.Commit(f.ctx); err != nil {
		t.Fatalf("complete report commit: %v", err)
	}
	late := identityTx(t, f, true)
	insert := `INSERT INTO silver.quality_finding
		(document_revision_id, revision_key, document_version_key, processing_execution_key, candidate_digest,
		 quality_report_id, report_hash, ordinal, record_kind, code, severity, scope_kind, scope_key, reason, evidence, source_spans)
		SELECT document_revision_id, revision_key, document_version_key, processing_execution_key, candidate_digest,
		 quality_report_id, report_hash, $2, record_kind, code, severity, scope_kind, scope_key, reason, evidence, source_spans
		FROM silver.quality_finding WHERE quality_report_id = $1 AND ordinal = 0`
	conversionState(t, f.ctx, late, "23505", insert, report, 0)
	conversionState(t, f.ctx, late, "23514", insert, report, 2)
	if conversionCount(t, f.ctx, late, "SELECT count(*) FROM silver.quality_finding WHERE quality_report_id = $1", report) != 2 {
		t.Fatal("late insert changed committed report content")
	}
	if err := late.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
}
