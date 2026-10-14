package intake

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"danny.vn/banhmi/internal/corpus/model"
)

//go:embed queries/sources.sql
var sourceQueries string

func sourceFixture(t *testing.T) pgx.Tx {
	t.Helper()
	if testing.Short() {
		t.Skip("source schema tests require hosted PostgreSQL")
	}
	return ledgerFixture(t)
}

func sourceQuery(t *testing.T, name string) string {
	t.Helper()
	_, tail, ok := strings.Cut(sourceQueries, "-- name: "+name+" :")
	if !ok {
		t.Fatal("missing source query " + name)
	}
	_, tail, ok = strings.Cut(tail, "\n")
	if !ok {
		t.Fatal("missing source query body " + name)
	}
	body, _, _ := strings.Cut(tail, "\n-- name:")
	return body
}

func sourceID(t *testing.T, tx pgx.Tx, name string, args ...any) (int64, error) {
	t.Helper()
	rows, err := tx.Query(context.Background(), sourceQuery(t, name), args...)
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
		t.Fatal("source receipt must start with bigint id")
	}
	if rows.Next() {
		t.Fatal("source operation returned multiple receipts")
	}
	return id, rows.Err()
}

func requireSourceID(t *testing.T, tx pgx.Tx, name string, args ...any) int64 {
	t.Helper()
	id, err := sourceID(t, tx, name, args...)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return id
}

func requireNoSourceRow(t *testing.T, tx pgx.Tx, name string, args ...any) {
	t.Helper()
	if _, err := sourceID(t, tx, name, args...); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("%s must change nothing, got %v", name, err)
	}
}

func sourceGrant(kind, key string) []any {
	return []any{int64(1), "writer-a", kind, key, "owner-a", int64(1)}
}

func seedSourceWindow(t *testing.T, tx pgx.Tx) int64 {
	t.Helper()
	return seedSourceWindowCursor(t, tx, "initial")
}

func seedSourceWindowCursor(t *testing.T, tx pgx.Tx, cursor string) int64 {
	t.Helper()
	return seedSourceWindowSliceCursor(t, tx, "keyword:0", cursor)
}

func seedSourceWindowSliceCursor(t *testing.T, tx pgx.Tx, slice, cursor string) int64 {
	t.Helper()
	requireLedgerID(t, tx, "ClaimExecutionLease", int64(1), "writer-a", "source_window", "window-a", "owner-a", time.Now().Add(time.Hour))
	args := append(sourceGrant("source_window", "window-a"), "source-a", slice, "incremental",
		time.Now().Add(-time.Hour), time.Now(), time.Now().Add(time.Hour), fixtureDigest, fixtureDigest,
		`{"max_items":10,"max_input_bytes":1048576,"max_pages":10,"max_elapsed":3600000000000,"max_concurrent":1}`,
		int32(10), int32(10), int32(5), pgtype.Interval{Microseconds: (24 * time.Hour).Microseconds(), Valid: true}, cursor)
	return requireSourceID(t, tx, "OpenSourceWindow", args...)
}

func TestSourceOpenWindowSliceGrammar(t *testing.T) {
	for _, slice := range []string{"keyword:0", "sweep", "rss", "central", "legal", strings.Repeat("a", 128)} {
		t.Run(slice, func(t *testing.T) {
			tx := sourceFixture(t)
			seedSourceWindowSliceCursor(t, tx, slice, "1")
			if got := ledgerCount(t, tx, `SELECT count(*) FROM ingest.source_window WHERE slice_key = $1`, slice); got != 1 {
				t.Fatal("planner slice did not open a source window")
			}
		})
	}
	tx := sourceFixture(t)
	seedSourceWindow(t, tx)
	for _, slice := range []string{"", "Keyword:0", ":0", "keyword/0", "keyword.0", "keyword_0", "keyword-0", strings.Repeat("a", 129)} {
		requireSQLState(t, tx, "23514", `INSERT INTO ingest.source_window
			(window_key, source_id, slice_key, mode, starts_at, cutoff_at, deadline_at,
			policy_digest, budget_digest, budget, page_budget, request_budget,
			source_attempt_limit, source_retry_interval, initial_cursor, committed_cursor, state)
			SELECT 'invalid-slice', source_id, $1, mode, starts_at, cutoff_at, deadline_at,
			policy_digest, budget_digest, budget, page_budget, request_budget,
			source_attempt_limit, source_retry_interval, initial_cursor, committed_cursor, state
			FROM ingest.source_window WHERE window_key = 'window-a'`, slice)
	}
}

func seedSourcePage(t *testing.T, tx pgx.Tx, ordinal int32) int64 {
	t.Helper()
	return seedSourcePageCursor(t, tx, ordinal, "request-cursor")
}

func seedSourcePageCursor(t *testing.T, tx pgx.Tx, ordinal int32, cursor string) int64 {
	t.Helper()
	requireSourceID(t, tx, "LockSourceWindowForPlanning", append(sourceGrant("source_window", "window-a"), "window-a")...)
	args := append(sourceGrant("source_window", "window-a"), fmt.Sprintf("page-%032x", ordinal+1), ordinal,
		"listing", cursor, `{}`, time.Now())
	return requireSourceID(t, tx, "RegisterDiscoveryPage", args...)
}

func seedSourceRequest(t *testing.T, tx pgx.Tx) int64 {
	t.Helper()
	seedExecution(t, tx)
	hostID := requireSourceID(t, tx, "RegisterSourceRateLimit", "host-a", time.Now().Add(-time.Hour), int32(1), pgtype.Interval{Valid: true})
	requireSourceID(t, tx, "LockSourceWindowForPlanning", append(sourceGrant("document_version", "version-a"), "window-a")...)
	args := append(sourceGrant("document_version", "version-a"), "request-00000000000000000000000000000001",
		"window-a", hostID, "observation-00000000000000000000000000000001", "artifact", "main", "", true,
		`{}`, "request-cursor", time.Now().Add(-time.Minute))
	return requireSourceID(t, tx, "RegisterArtifactRequest", args...)
}

func sourceResult(t *testing.T, tx pgx.Tx) int64 {
	t.Helper()
	args := executionArgs(1, "execution-00000000000000000000000000000002")
	args[2], args[3], args[7], args[8] = "source_window", "window-a", "source-sync/vn/source-a/window-a", "SourceSync"
	executionID := requireLedgerID(t, tx, "InsertExecution", args...)
	result := resultArgs(executionID)
	result[2], result[3], result[7] = "source_window", "window-a", "result-00000000000000000000000000000002"
	return requireLedgerID(t, tx, "InsertExecutionResult", result...)
}

func capturePage(t *testing.T, tx pgx.Tx, pageID int64, complete bool) {
	t.Helper()
	grant := sourceGrant("source_window", "window-a")
	requireSourceID(t, tx, "RecordDiscoveryPageCapture", append(grant, pageID,
		"capture-00000000000000000000000000000001", fixtureDigest, int64(10), `{}`, "request-00000000000000000000000000000002", int32(1))...)
	var cursor string
	if err := tx.QueryRow(context.Background(), `SELECT request_cursor FROM ingest.discovery_page WHERE id = $1`, pageID).Scan(&cursor); err != nil {
		t.Fatal(err)
	}
	next := cursor
	if complete {
		next = "next"
	}
	requireSourceID(t, tx, "RecordDiscoveryPageDecode", append(sourceGrant("source_window", "window-a"),
		pageID, fixtureDigest, cursor, cursor, next, int32(99), complete, `[]`)...)
}

func TestSourceIncompletePageDoesNotAdvanceCursor(t *testing.T) {
	tx := sourceFixture(t)
	seedSourceWindow(t, tx)
	pageID := seedSourcePage(t, tx, 0)
	capturePage(t, tx, pageID, false)
	resultID := sourceResult(t, tx)
	requireSourceID(t, tx, "AcceptDiscoveryPage", append(sourceGrant("source_window", "window-a"), pageID, int32(0), true, resultID)...)
	if got := ledgerCount(t, tx, `SELECT count(*) FROM ingest.source_window WHERE committed_cursor = 'initial' AND state = 'progressing'`); got != 1 {
		t.Fatal("incomplete page advanced the complete-window checkpoint")
	}
	requireSQLState(t, tx, "23514", `UPDATE ingest.source_window SET committed_cursor = 'next'`)
	requireSQLState(t, tx, "55000", `UPDATE ingest.discovery_page SET cursor_next = 'changed' WHERE id = $1`, pageID)
}

func TestSourceCompleteWindowRequiresAllPages(t *testing.T) {
	tx := sourceFixture(t)
	seedSourceWindowCursor(t, tx, "1")
	first := seedSourcePageCursor(t, tx, 0, "1")
	last := seedSourcePageCursor(t, tx, 1, "2")
	capturePage(t, tx, last, true)
	resultID := sourceResult(t, tx)
	requireSourceID(t, tx, "AcceptDiscoveryPage", append(sourceGrant("source_window", "window-a"), last, int32(1), true, resultID)...)
	if got := ledgerCount(t, tx, `SELECT count(*) FROM ingest.source_window WHERE initial_cursor = '1' AND committed_cursor = initial_cursor`); got != 1 {
		t.Fatal("unaccounted earlier page allowed checkpoint advancement")
	}
	capturePage(t, tx, first, false)
	requireSourceID(t, tx, "AcceptDiscoveryPage", append(sourceGrant("source_window", "window-a"), first, int32(0), true, resultID)...)
	if got := ledgerCount(t, tx, `SELECT count(*) FROM ingest.source_window WHERE committed_cursor = 'next' AND state = 'complete'`); got != 1 {
		t.Fatal("fully accounted window did not commit final cursor")
	}
	if got := ledgerCount(t, tx, `SELECT count(*) FROM ingest.discovery_page WHERE state = 'accepted' AND cursor_initial = request_cursor AND cursor_committed = request_cursor`); got != 2 {
		t.Fatal("distinct continuation cursors were not accepted")
	}
}

func TestSourceDecodeRejectsWrongRequestCursor(t *testing.T) {
	tx := sourceFixture(t)
	seedSourceWindow(t, tx)
	pageID := seedSourcePageCursor(t, tx, 0, "2")
	requireSourceID(t, tx, "RecordDiscoveryPageCapture", append(sourceGrant("source_window", "window-a"), pageID,
		"capture-00000000000000000000000000000001", fixtureDigest, int64(10), `{}`, "request-00000000000000000000000000000002", int32(1))...)
	for _, cursors := range [][2]string{{"initial", "initial"}, {"2", "initial"}, {"initial", "2"}} {
		requireNoSourceRow(t, tx, "RecordDiscoveryPageDecode", append(sourceGrant("source_window", "window-a"),
			pageID, fixtureDigest, cursors[0], cursors[1], "next", int32(0), true, `[]`)...)
	}
	if got := ledgerCount(t, tx, `SELECT count(*) FROM ingest.discovery_page WHERE id = $1 AND state = 'captured' AND decoded_digest IS NULL`, pageID); got != 1 {
		t.Fatal("wrong cursor receipt changed the page")
	}
	// Inject a shaped but mismatched receipt to exercise acceptance independently.
	execLedger(t, tx, `UPDATE ingest.discovery_page SET state = 'decoded', decoded_digest = $2,
		cursor_initial = 'initial', cursor_committed = 'initial', cursor_next = 'next',
		cursor_ordinal = 0, complete = true, findings = '[]' WHERE id = $1`, pageID, fixtureDigest)
	resultID := sourceResult(t, tx)
	requireNoSourceRow(t, tx, "AcceptDiscoveryPage", append(sourceGrant("source_window", "window-a"), pageID, int32(0), true, resultID)...)
}

func TestSourceFollowUpPreservesFrozenAllowances(t *testing.T) {
	tx := sourceFixture(t)
	seedSourceWindow(t, tx)
	pageID := seedSourcePage(t, tx, 0)
	seedSourcePage(t, tx, 1)
	for _, assignment := range []string{
		"budget = '{}'", "budget_digest = repeat('b', 64)", "page_budget = 20", "request_budget = 20",
		"source_attempt_limit = 4", "source_retry_interval = interval '12 hours'", "initial_cursor = 'changed'",
		"deadline_at = deadline_at + interval '1 day'", "policy_digest = repeat('b', 64)",
	} {
		requireSQLState(t, tx, "55000", "UPDATE ingest.source_window SET "+assignment)
	}
	for _, assignment := range []string{"source_attempt_limit = 4", "retry_deadline_at = retry_deadline_at + interval '1 hour'", "request_cursor = 'changed'", "ordinal = 10"} {
		requireSQLState(t, tx, "55000", "UPDATE ingest.discovery_page SET "+assignment+" WHERE id = $1", pageID)
	}
	if got := ledgerCount(t, tx, `SELECT count(*) FROM ingest.discovery_page p JOIN ingest.source_window w ON w.id = p.source_window_id
		WHERE p.source_attempt_limit = w.source_attempt_limit AND p.retry_deadline_at - p.retry_started_at = w.source_retry_interval`); got != 2 {
		t.Fatal("continuation replaced its window allowance")
	}
}

func TestSourceStaleCaptureAndDuplicateReceipt(t *testing.T) {
	for _, stale := range []string{"fence", "epoch", "cancelled", "expired"} {
		t.Run(stale, func(t *testing.T) {
			tx := sourceFixture(t)
			seedSourceWindow(t, tx)
			pageID := seedSourcePage(t, tx, 0)
			args := append(sourceGrant("source_window", "window-a"), pageID,
				"capture-00000000000000000000000000000001", fixtureDigest, int64(10), `{}`, "request-00000000000000000000000000000002", int32(1))
			switch stale {
			case "fence":
				args[5] = int64(2)
			case "epoch":
				requireLedgerID(t, tx, "RotateAdmissionEpoch", int64(1), "writer-b", true)
			case "cancelled":
				execLedger(t, tx, `UPDATE ingest.execution_lease SET cancelled_at = clock_timestamp()`)
			case "expired":
				execLedger(t, tx, `UPDATE ingest.execution_lease SET expires_at = clock_timestamp() - interval '1 second'`)
			}
			requireNoSourceRow(t, tx, "RecordDiscoveryPageCapture", args...)
			if got := ledgerCount(t, tx, `SELECT count(*) FROM ingest.discovery_page WHERE state = 'requested' AND capture_key IS NULL AND source_attempts = 0`); got != 1 {
				t.Fatal("stale capture changed source evidence or accounting")
			}
		})
	}
	tx := sourceFixture(t)
	seedSourceWindow(t, tx)
	pageID := seedSourcePage(t, tx, 0)
	capturePage(t, tx, pageID, false)
	requireNoSourceRow(t, tx, "RecordDiscoveryPageCapture", append(sourceGrant("source_window", "window-a"), pageID,
		"capture-00000000000000000000000000000001", fixtureDigest, int64(10), `{}`, "request-00000000000000000000000000000002", int32(1))...)
	requireSQLState(t, tx, "55000", `UPDATE ingest.discovery_page SET transport_receipt = '{"changed":true}' WHERE id = $1`, pageID)
	requireSQLState(t, tx, "55000", `UPDATE ingest.discovery_page SET decoded_digest = repeat('b',64) WHERE id = $1`, pageID)
	requireSQLState(t, tx, "55000", `DELETE FROM ingest.discovery_page WHERE id = $1`, pageID)
	resultID := sourceResult(t, tx)
	args := append(sourceGrant("source_window", "window-a"), pageID, int32(0), false, resultID)
	requireSourceID(t, tx, "AcceptDiscoveryPage", args...)
	requireNoSourceRow(t, tx, "AcceptDiscoveryPage", args...)
	if got := ledgerCount(t, tx, `SELECT source_attempts FROM ingest.discovery_page WHERE id = $1`, pageID); got != 1 {
		t.Fatal("duplicate capture consumed another source attempt")
	}
	if got := ledgerCount(t, tx, `SELECT count(*) FROM ingest.discovery_page`); got != 1 {
		t.Fatal("duplicate receipt added evidence")
	}
}

func transportArgs(requestID int64, attempt int, success bool, retryAt time.Time) []any {
	var capture, digest, size, receipt any
	if success {
		capture, digest, size, receipt = "capture-00000000000000000000000000000001", fixtureDigest, int64(10), `{}`
	}
	return append(sourceGrant("document_version", "version-a"), requestID, fmt.Sprintf("request-%032x", attempt),
		success, capture, digest, size, receipt, retryAt, retryAt, int32(attempt), time.Now())
}

func TestSourceRetryAccountingAndHostClaim(t *testing.T) {
	tx := sourceFixture(t)
	seedSourceWindow(t, tx)
	requestID := seedSourceRequest(t, tx)
	retryAt := time.Now().Add(-time.Second)
	execLedger(t, tx, `SET LOCAL ROLE banhmi_corpus_writer_runtime`)
	for attempt := 1; attempt <= 5; attempt++ {
		requireSourceID(t, tx, "ClaimArtifactRequest", append(sourceGrant("document_version", "version-a"), time.Now())...)
		requireNoSourceRow(t, tx, "ClaimArtifactRequest", append(sourceGrant("document_version", "version-a"), time.Now())...)
		if attempt > 1 {
			requireNoSourceRow(t, tx, "RecordArtifactTransport", transportArgs(requestID, attempt-1, false, retryAt)...)
		}
		args := transportArgs(requestID, attempt, false, retryAt)
		requireSourceID(t, tx, "RecordArtifactTransport", args...)
		requireNoSourceRow(t, tx, "RecordArtifactTransport", args...)
		if got := ledgerCount(t, tx, `SELECT in_flight FROM ingest.source_rate_limit`); got != 0 {
			t.Fatal("receipt replay released the host slot twice")
		}
	}
	requireNoSourceRow(t, tx, "ClaimArtifactRequest", append(sourceGrant("document_version", "version-a"), time.Now())...)
	if got := ledgerCount(t, tx, `SELECT count(*) FROM ingest.artifact_request WHERE state = 'exhausted' AND source_attempts = 5`); got != 1 {
		t.Fatal("five source attempts did not exhaust frozen allowance")
	}
	requireSQLState(t, tx, "23514", `INSERT INTO ingest.artifact_request
		(request_key, source_window_id, source_rate_limit_id, document_version_key, owner_kind, owner_key,
		purpose, expected_role, expected_response_kind, required, request_plan, request_cursor,
		source_attempt_limit, retry_started_at, retry_deadline_at, state, next_eligible_at)
		SELECT 'request-000000000000000000000000000000ff', source_window_id, source_rate_limit_id,
		document_version_key, owner_kind, owner_key, purpose, expected_role, expected_response_kind,
		required, request_plan, request_cursor, 5, retry_started_at, retry_started_at + interval '25 hours', 'due', clock_timestamp()
		FROM ingest.artifact_request WHERE id = $1`, requestID)
}

func TestSourceWorkerLossPreservesSourceAttempts(t *testing.T) {
	tx := sourceFixture(t)
	seedSourceWindow(t, tx)
	requestID := seedSourceRequest(t, tx)
	execLedger(t, tx, `SET LOCAL ROLE banhmi_corpus_writer_runtime`)
	for fence := int64(1); fence <= 3; fence++ {
		grant := sourceGrant("document_version", "version-a")
		grant[5] = fence
		requireSourceID(t, tx, "ClaimArtifactRequest", append(grant, time.Now())...)
		execLedger(t, tx, `UPDATE ingest.execution_lease SET expires_at = clock_timestamp() - interval '1 second' WHERE resource_kind = 'document_version'`)
		requireLedgerID(t, tx, "ReclaimExecutionLease", int64(1), "writer-a", "document_version", "version-a", "owner-a", time.Now().Add(time.Hour))
		grant[5] = fence + 1
		if fence < 3 {
			requireSourceID(t, tx, "RecoverArtifactRequest", append(grant, requestID, time.Now())...)
			requireNoSourceRow(t, tx, "RecoverArtifactRequest", append(grant, requestID, time.Now())...)
		} else {
			requireNoSourceRow(t, tx, "RecoverArtifactRequest", append(grant, requestID, time.Now())...)
			stale := append([]any(nil), grant...)
			stale[5] = fence
			requireNoSourceRow(t, tx, "ExpireArtifactRequest", append(stale, requestID, "infrastructure_retry_exhausted")...)
			requireSourceID(t, tx, "ExpireArtifactRequest", append(grant, requestID, "infrastructure_retry_exhausted")...)
			requireNoSourceRow(t, tx, "ExpireArtifactRequest", append(grant, requestID, "infrastructure_retry_exhausted")...)
		}
	}
	if got := ledgerCount(t, tx, `SELECT source_attempts FROM ingest.artifact_request WHERE id = $1`, requestID); got != 0 {
		t.Fatal("worker loss consumed a source attempt")
	}
	if got := ledgerCount(t, tx, `SELECT infrastructure_attempts FROM ingest.artifact_request WHERE id = $1`, requestID); got != 3 {
		t.Fatal("infrastructure recovery exceeded three attempts")
	}
	if got := ledgerCount(t, tx, `SELECT count(*) FROM ingest.artifact_request WHERE id = $1 AND state = 'expired' AND reason = 'infrastructure_retry_exhausted' AND next_eligible_at IS NULL`, requestID); got != 1 {
		t.Fatal("abandoned request did not close")
	}
	if got := ledgerCount(t, tx, `SELECT in_flight FROM ingest.source_rate_limit`); got != 0 {
		t.Fatal("closure or its replay stranded or released another host slot")
	}
}

func TestSourceRetryAfterAndMonotonicAccounting(t *testing.T) {
	tx := sourceFixture(t)
	seedSourceWindow(t, tx)
	requestID := seedSourceRequest(t, tx)
	requireSourceID(t, tx, "ClaimArtifactRequest", append(sourceGrant("document_version", "version-a"), time.Now())...)
	future := time.Now().Add(time.Hour)
	requireSourceID(t, tx, "RecordArtifactTransport", transportArgs(requestID, 1, false, future)...)
	requireNoSourceRow(t, tx, "ClaimArtifactRequest", append(sourceGrant("document_version", "version-a"), time.Now())...)
	for _, assignment := range []string{"source_attempt_limit = 4", "retry_deadline_at = retry_deadline_at + interval '1 hour'", "required = false", "request_plan = '{\"changed\":true}'"} {
		requireSQLState(t, tx, "55000", "UPDATE ingest.artifact_request SET "+assignment+" WHERE id = $1", requestID)
	}
	for _, assignment := range []string{"source_attempts = 0", "retry_after = NULL", "next_eligible_at = clock_timestamp()", "infrastructure_attempts = 0"} {
		requireSQLState(t, tx, "23514", "UPDATE ingest.artifact_request SET "+assignment+" WHERE id = $1", requestID)
	}
}

func TestSourceClosureRejectsStaleOwner(t *testing.T) {
	for _, stale := range []string{"epoch", "writer", "owner", "fence", "expired", "cancelled"} {
		t.Run(stale, func(t *testing.T) {
			tx := sourceFixture(t)
			seedSourceWindow(t, tx)
			requestID := seedSourceRequest(t, tx)
			execLedger(t, tx, `UPDATE ingest.artifact_request SET infrastructure_attempts = 3 WHERE id = $1`, requestID)
			execLedger(t, tx, `SET LOCAL ROLE banhmi_corpus_writer_runtime`)
			grant := sourceGrant("document_version", "version-a")
			requireSourceID(t, tx, "ClaimArtifactRequest", append(grant, time.Now())...)
			requireNoSourceRow(t, tx, "ExpireArtifactRequest", append(grant, requestID, "infrastructure_retry_exhausted")...)
			execLedger(t, tx, `UPDATE ingest.execution_lease SET expires_at = clock_timestamp() - interval '1 second' WHERE resource_kind = 'document_version'`)
			requireLedgerID(t, tx, "ReclaimExecutionLease", int64(1), "writer-a", "document_version", "version-a", "owner-a", time.Now().Add(time.Hour))
			grant[5] = int64(2)
			switch stale {
			case "epoch":
				requireLedgerID(t, tx, "RotateAdmissionEpoch", int64(1), "writer-b", true)
			case "writer":
				grant[1] = "writer-b"
			case "owner":
				grant[4] = "owner-b"
			case "fence":
				grant[5] = int64(1)
			case "expired":
				execLedger(t, tx, `UPDATE ingest.execution_lease SET expires_at = clock_timestamp() - interval '1 second' WHERE resource_kind = 'document_version'`)
			case "cancelled":
				execLedger(t, tx, `UPDATE ingest.execution_lease SET cancelled_at = clock_timestamp() WHERE resource_kind = 'document_version'`)
			}
			requireNoSourceRow(t, tx, "ExpireArtifactRequest", append(grant, requestID, "infrastructure_retry_exhausted")...)
			if got := ledgerCount(t, tx, `SELECT count(*) FROM ingest.artifact_request WHERE id = $1 AND state = 'leased' AND source_attempts = 0 AND infrastructure_attempts = 3`, requestID); got != 1 {
				t.Fatal("stale closure changed request accounting")
			}
			if got := ledgerCount(t, tx, `SELECT in_flight FROM ingest.source_rate_limit`); got != 1 {
				t.Fatal("stale closure released the abandoned slot")
			}
		})
	}
}

func TestSourceEligibilityWatermarkSurvivesClaim(t *testing.T) {
	tx := sourceFixture(t)
	seedSourceWindow(t, tx)
	requestID := seedSourceRequest(t, tx)
	watermark := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
	requireSourceID(t, tx, "ClaimArtifactRequest", append(sourceGrant("document_version", "version-a"), time.Now())...)
	args := transportArgs(requestID, 1, false, watermark)
	args[14] = nil // Isolate eligibility from Retry-After.
	requireSourceID(t, tx, "RecordArtifactTransport", args...)
	requireSourceID(t, tx, "ClaimArtifactRequest", append(sourceGrant("document_version", "version-a"), watermark)...)
	requireSQLState(t, tx, "23514", `UPDATE ingest.artifact_request SET state = 'due', next_eligible_at = $2 WHERE id = $1`, requestID, watermark.Add(-time.Second))
	requireSQLState(t, tx, "23514", `UPDATE ingest.artifact_request SET eligibility_watermark = NULL WHERE id = $1`, requestID)
	requireSQLState(t, tx, "23514", `UPDATE ingest.artifact_request SET eligibility_watermark = $2 WHERE id = $1`, requestID, watermark.Add(-time.Second))
	args = transportArgs(requestID, 2, false, watermark.Add(-time.Minute))
	args[14] = nil
	args[16] = watermark
	requireSourceID(t, tx, "RecordArtifactTransport", args...)
	if got := ledgerCount(t, tx, `SELECT count(*) FROM ingest.artifact_request WHERE id = $1 AND state = 'due' AND next_eligible_at = $2 AND eligibility_watermark = $2`, requestID, watermark); got != 1 {
		t.Fatal("claim cleared the eligibility watermark or retry moved it backwards")
	}
	requireNoSourceRow(t, tx, "ClaimArtifactRequest", append(sourceGrant("document_version", "version-a"), watermark.Add(-time.Microsecond))...)
	requireSourceID(t, tx, "ClaimArtifactRequest", append(sourceGrant("document_version", "version-a"), watermark)...)
	execLedger(t, tx, `UPDATE ingest.execution_lease SET expires_at = clock_timestamp() - interval '1 second' WHERE resource_kind = 'document_version'`)
	requireLedgerID(t, tx, "ReclaimExecutionLease", int64(1), "writer-a", "document_version", "version-a", "owner-a", time.Now().Add(time.Hour))
	grant := sourceGrant("document_version", "version-a")
	grant[5] = int64(2)
	requireSourceID(t, tx, "RecoverArtifactRequest", append(grant, requestID, watermark.Add(-time.Minute))...)
	if got := ledgerCount(t, tx, `SELECT count(*) FROM ingest.artifact_request WHERE id = $1 AND state = 'due' AND next_eligible_at = $2 AND eligibility_watermark = $2`, requestID, watermark); got != 1 {
		t.Fatal("recovery moved eligibility backwards")
	}
}

func TestSourceRetryDeadlineBoundary(t *testing.T) {
	tx := sourceFixture(t)
	seedSourceWindow(t, tx)
	requestID := seedSourceRequest(t, tx)
	var deadline time.Time
	if err := tx.QueryRow(context.Background(), `SELECT retry_deadline_at FROM ingest.artifact_request WHERE id = $1`, requestID).Scan(&deadline); err != nil {
		t.Fatal(err)
	}
	requireNoSourceRow(t, tx, "ClaimArtifactRequest", append(sourceGrant("document_version", "version-a"), deadline)...)
	requireSourceID(t, tx, "ClaimArtifactRequest", append(sourceGrant("document_version", "version-a"), deadline.Add(-time.Microsecond))...)
	args := transportArgs(requestID, 1, false, deadline)
	args[16] = deadline
	requireSourceID(t, tx, "RecordArtifactTransport", args...)
	if got := ledgerCount(t, tx, `SELECT count(*) FROM ingest.artifact_request WHERE id = $1 AND state = 'exhausted' AND source_attempts = 1`, requestID); got != 1 {
		t.Fatal("deadline boundary did not exhaust the request's frozen time allowance")
	}
}

func seedSharedHostRequest(t *testing.T, tx pgx.Tx, hostID int64, version string, ordinal int) int64 {
	t.Helper()
	requireLedgerID(t, tx, "ClaimExecutionLease", int64(1), "writer-a", "document_version", version, "owner-a", time.Now().Add(time.Hour))
	requireSourceID(t, tx, "LockSourceWindowForPlanning", append(sourceGrant("document_version", version), "window-a")...)
	return requireSourceID(t, tx, "RegisterArtifactRequest", append(sourceGrant("document_version", version),
		fmt.Sprintf("request-%032x", ordinal), "window-a", hostID, fmt.Sprintf("observation-%032x", ordinal),
		"artifact", "main", "", true, `{}`, "request-cursor", time.Now().Add(-time.Minute))...)
}

func TestSourceSharedHostCapacityAndPacing(t *testing.T) {
	tx := sourceFixture(t)
	seedSourceWindow(t, tx)
	hostID := requireSourceID(t, tx, "RegisterSourceRateLimit", "host-a", time.Now().Add(-time.Hour), int32(1), pgtype.Interval{Valid: true})
	first := seedSharedHostRequest(t, tx, hostID, "version-a", 1)
	seedSharedHostRequest(t, tx, hostID, "version-b", 2)
	now := time.Now().UTC().Truncate(time.Microsecond)
	requireSourceID(t, tx, "ClaimArtifactRequest", append(sourceGrant("document_version", "version-a"), now)...)
	requireNoSourceRow(t, tx, "ClaimArtifactRequest", append(sourceGrant("document_version", "version-b"), now)...)
	if got := ledgerCount(t, tx, `SELECT in_flight FROM ingest.source_rate_limit WHERE id = $1`, hostID); got != 1 {
		t.Fatal("rejected second claim changed host occupancy")
	}
	allowed := now.Add(time.Minute)
	args := transportArgs(first, 1, true, allowed)
	args[16] = now
	requireSourceID(t, tx, "RecordArtifactTransport", args...)
	if got := ledgerCount(t, tx, `SELECT in_flight FROM ingest.source_rate_limit WHERE id = $1`, hostID); got != 0 {
		t.Fatal("transport did not release host capacity")
	}
	requireNoSourceRow(t, tx, "ClaimArtifactRequest", append(sourceGrant("document_version", "version-b"), allowed.Add(-time.Microsecond))...)
	requireSourceID(t, tx, "ClaimArtifactRequest", append(sourceGrant("document_version", "version-b"), allowed)...)
	if got := ledgerCount(t, tx, `SELECT in_flight FROM ingest.source_rate_limit WHERE id = $1`, hostID); got != 1 {
		t.Fatal("shared host did not retain exactly one occupied slot")
	}
}

func TestSourceConcurrentHostClaimsAdmitOne(t *testing.T) {
	if testing.Short() {
		t.Skip("source schema tests require hosted PostgreSQL")
	}
	f := newAdapterFixture(t)
	tx, err := f.conn.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	seedSourceWindow(t, tx)
	hostID := requireSourceID(t, tx, "RegisterSourceRateLimit", "host-a", time.Now().Add(-time.Hour), int32(1), pgtype.Interval{Valid: true})
	seedSharedHostRequest(t, tx, hostID, "version-b", 2)
	seedSharedHostRequest(t, tx, hostID, "version-c", 3)
	if err := tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	// Hold the host until both claim statements reach its lock. Both candidates
	// must observe the original capacity and recheck after the lock is acquired.
	blocker, err := f.conn.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(context.Background()) }()
	if _, err := blocker.Exec(f.ctx, `SELECT id FROM ingest.source_rate_limit WHERE id = $1 FOR UPDATE`, hostID); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	now := time.Now()
	query := sourceQuery(t, "ClaimArtifactRequest")
	var pids []int32
	for _, version := range []string{"version-b", "version-c"} {
		conn := adapterConnection(t, f)
		pids = append(pids, int32(conn.PgConn().PID()))
		if _, err := conn.Exec(f.ctx, `SET ROLE banhmi_corpus_writer_runtime`); err != nil {
			t.Fatal(err)
		}
		go func(conn *pgx.Conn, version string) {
			<-start
			var id int64
			var key, cursor string
			var plan []byte
			var attempts, next int32
			err := conn.QueryRow(f.ctx, query, append(sourceGrant("document_version", version), now)...).Scan(&id, &key, &plan, &cursor, &attempts, &next)
			results <- err
		}(conn, version)
	}
	close(start)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting int
		if err := blocker.QueryRow(f.ctx, `SELECT count(*) FROM unnest($1::integer[]) AS claimant(pid)
			WHERE cardinality(pg_blocking_pids(claimant.pid)) > 0`, pids).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting == 2 {
			break
		}
		select {
		case <-ticker.C:
		case <-f.ctx.Done():
			t.Fatal("concurrent claimants did not both reach the host lock")
		}
	}
	if err := blocker.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	winners := 0
	for i := 0; i < 2; i++ {
		err := <-results
		if err == nil {
			winners++
		} else if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatalf("concurrent claims admitted %d requests, want one", winners)
	}
	var occupied int64
	if err := f.conn.QueryRow(f.ctx, `SELECT in_flight FROM ingest.source_rate_limit WHERE id = $1`, hostID).Scan(&occupied); err != nil || occupied != 1 {
		t.Fatalf("concurrent host occupancy = %d, error = %v", occupied, err)
	}
}

func seedBronzeBlob(t *testing.T, tx pgx.Tx) int64 {
	t.Helper()
	return requireSourceID(t, tx, "RegisterSourceBlob", fixtureDigest, int64(10), "text/html", "objects/a", "source-a", "capture-a", time.Now())
}

func TestSourceBlobRegistrationAndImmutableEvidence(t *testing.T) {
	tx := sourceFixture(t)
	blobID := seedBronzeBlob(t, tx)
	if got := requireSourceID(t, tx, "RegisterSourceBlob", fixtureDigest, int64(10), "text/html", "objects/other", "source-b", "capture-b", time.Now()); got != blobID {
		t.Fatal("same hash and length registered another blob")
	}
	requireNoSourceRow(t, tx, "RegisterSourceBlob", fixtureDigest, int64(11), "text/html", "objects/a", "source-a", "capture-a", time.Now())
	recordID := requireSourceID(t, tx, "InsertSourceRecord", "source-a", "native-identifier")
	observationID := requireSourceID(t, tx, "InsertObservationRevision", "observation-00000000000000000000000000000001", recordID,
		fixtureDigest, fixtureDigest, fixtureDigest, fixtureDigest, "window-a", "version-a",
		"execution-00000000000000000000000000000001", "result-00000000000000000000000000000001", blobID, time.Now())
	artifactID := requireSourceID(t, tx, "InsertSourceArtifact", "artifact-a", blobID, "main", "", "", "https://example.com/a", "https://example.com/a")
	requireSourceID(t, tx, "LinkObservationArtifact", observationID, artifactID, "main", int32(0), true)
	requireNoSourceRow(t, tx, "LinkObservationArtifact", observationID, artifactID, "appendix", int32(1), true)
	for _, table := range []string{"blob", "artifact", "observation_revision", "observation_artifact"} {
		requireSQLState(t, tx, "55000", "UPDATE bronze."+table+" SET id = id")
		requireSQLState(t, tx, "55000", "DELETE FROM bronze."+table)
	}
	ref := requireSourceID(t, tx, "AddBlobRetentionReference", blobID, "observation_revision", "observation-00000000000000000000000000000001")
	if got := requireSourceID(t, tx, "AddBlobRetentionReference", blobID, "observation_revision", "observation-00000000000000000000000000000001"); got != ref {
		t.Fatal("identical retention edge added another dependency")
	}
	execLedger(t, tx, `SET LOCAL ROLE banhmi_corpus_writer_runtime`)
	requireSQLState(t, tx, "42501", `DELETE FROM bronze.blob_retention_reference WHERE id = $1`, ref)
	if got := ledgerCount(t, tx, `SELECT count(*) FROM bronze.blob_retention_reference WHERE id = $1`, ref); got != 1 {
		t.Fatal("runtime deleted protected retention edge")
	}
}

const sourcePageShapeSQL = `INSERT INTO ingest.discovery_page
	(source_window_id, page_key, ordinal, purpose, request_cursor, request_plan, state, reason,
	 source_attempt_limit, retry_started_at, retry_deadline_at, capture_key, capture_digest,
	 capture_bytes, transport_receipt, decoded_digest, cursor_initial, cursor_committed,
	 cursor_next, cursor_ordinal, complete, findings, acceptance_result_id)
	VALUES ($1, $2, $3, 'listing', 'request-cursor', '{}', $4, $5, 5,
	 clock_timestamp(), clock_timestamp() + interval '23 hours',
	 CASE WHEN $6::boolean THEN 'capture-00000000000000000000000000000001' END,
	 CASE WHEN $6::boolean THEN repeat('a',64) END, CASE WHEN $6::boolean THEN 10 END,
	 CASE WHEN $6::boolean THEN '{}'::jsonb END,
	 CASE WHEN $7::boolean THEN repeat('a',64) END,
	 CASE WHEN $7::boolean THEN 'initial' END, CASE WHEN $7::boolean THEN 'initial' END,
	 CASE WHEN $7::boolean THEN $10::text END, CASE WHEN $7::boolean THEN 0 END,
	 $9::boolean, CASE WHEN $7::boolean THEN '[]'::jsonb END, $8::bigint)`

func TestSourceWindowAndPageVocabularyShapes(t *testing.T) {
	tx := sourceFixture(t)
	windowID := seedSourceWindow(t, tx)
	resultID := sourceResult(t, tx)
	for i, state := range []model.SourceWindowState{model.SourceWindowFrozen, model.SourceWindowProgressing, model.SourceWindowComplete, model.SourceWindowIncomplete, model.SourceWindowFailed} {
		var reason any
		if state == model.SourceWindowIncomplete || state == model.SourceWindowFailed {
			reason = "coverage_blocker"
		}
		query := `INSERT INTO ingest.source_window
			(window_key, source_id, slice_key, mode, starts_at, cutoff_at, deadline_at,
			policy_digest, budget_digest, budget, page_budget, request_budget, source_attempt_limit,
			source_retry_interval, initial_cursor, committed_cursor, state, reason)
			SELECT $1, source_id, slice_key, mode, starts_at, cutoff_at, deadline_at,
			policy_digest, budget_digest, budget, page_budget, request_budget, source_attempt_limit,
			source_retry_interval, initial_cursor, committed_cursor, $2, $3 FROM ingest.source_window WHERE id = $4`
		execLedger(t, tx, query, fmt.Sprintf("vocab-window-%d", i), string(state), reason, windowID)
		wrongReason := any("unexpected")
		if reason != nil {
			wrongReason = nil
		}
		requireSQLState(t, tx, "23514", query, fmt.Sprintf("bad-window-%d", i), string(state), wrongReason, windowID)
		if reason != nil {
			requireSQLState(t, tx, "23514", query, fmt.Sprintf("empty-window-%d", i), string(state), "", windowID)
		}
	}
	requireSQLState(t, tx, "23514", `INSERT INTO ingest.source_window
		(window_key, source_id, slice_key, mode, starts_at, cutoff_at, deadline_at, policy_digest,
		 budget_digest, budget, page_budget, request_budget, source_attempt_limit, source_retry_interval,
		 initial_cursor, committed_cursor, state)
		SELECT 'bad-window', source_id, slice_key, mode, starts_at, cutoff_at, deadline_at, policy_digest,
		 budget_digest, budget, page_budget, request_budget, source_attempt_limit, source_retry_interval,
		 initial_cursor, committed_cursor, 'outside' FROM ingest.source_window WHERE id = $1`, windowID)
	for i, state := range []model.DiscoveryPageState{model.DiscoveryPageRequested, model.DiscoveryPageCaptured, model.DiscoveryPageDecoded, model.DiscoveryPageAccepted, model.DiscoveryPageFailed} {
		captured := state == model.DiscoveryPageCaptured || state == model.DiscoveryPageDecoded || state == model.DiscoveryPageAccepted
		decoded := state == model.DiscoveryPageDecoded || state == model.DiscoveryPageAccepted
		var reason, acceptance, complete any
		if state == model.DiscoveryPageFailed {
			reason = "source_failure"
		}
		if state == model.DiscoveryPageAccepted {
			acceptance = resultID
		}
		if decoded {
			complete = false
		}
		args := []any{windowID, fmt.Sprintf("page-%032x", i+100), int32(i+100), string(state), reason, captured, decoded, acceptance, complete, "initial"}
		execLedger(t, tx, sourcePageShapeSQL, args...)
		// Flip each receipt group or acceptance reference against the state.
		if state != model.DiscoveryPageFailed {
			bad := append([]any(nil), args...)
			bad[1], bad[2], bad[5] = fmt.Sprintf("page-%032x", i+200), int32(i+200), !captured
			requireSQLState(t, tx, "23514", sourcePageShapeSQL, bad...)
			bad = append([]any(nil), args...)
			bad[1], bad[2], bad[6], bad[8] = fmt.Sprintf("page-%032x", i+300), int32(i+300), !decoded, nil
			if !decoded {
				bad[8] = false
			}
			requireSQLState(t, tx, "23514", sourcePageShapeSQL, bad...)
		}
		bad := append([]any(nil), args...)
		bad[1], bad[2] = fmt.Sprintf("page-%032x", i+400), int32(i+400)
		if acceptance == nil {
			bad[7] = resultID
		} else {
			bad[7] = nil
		}
		requireSQLState(t, tx, "23514", sourcePageShapeSQL, bad...)
		bad = append([]any(nil), args...)
		bad[1], bad[2], bad[4] = fmt.Sprintf("page-%032x", i+500), int32(i+500), "unexpected"
		if reason != nil {
			bad[4] = nil
		}
		requireSQLState(t, tx, "23514", sourcePageShapeSQL, bad...)
		if reason != nil {
			bad = append([]any(nil), args...)
			bad[1], bad[2], bad[4] = fmt.Sprintf("page-%032x", i+800), int32(i+800), ""
			requireSQLState(t, tx, "23514", sourcePageShapeSQL, bad...)
		}
		if decoded {
			bad = append([]any(nil), args...)
			bad[1], bad[2], bad[8] = fmt.Sprintf("page-%032x", i+600), int32(i+600), nil
			requireSQLState(t, tx, "23514", sourcePageShapeSQL, bad...)
			bad[8], bad[9] = false, "advanced"
			requireSQLState(t, tx, "23514", sourcePageShapeSQL, bad...)
		}
	}
	requireSQLState(t, tx, "23514", sourcePageShapeSQL, windowID, fmt.Sprintf("page-%032x", 999), int32(999), "outside", nil, false, false, nil, nil, "initial")
	partialCapture := strings.Replace(sourcePageShapeSQL, "CASE WHEN $6::boolean THEN 10 END", "NULL::bigint", 1)
	requireSQLState(t, tx, "23514", partialCapture, windowID, fmt.Sprintf("page-%032x", 1100), int32(1100), "captured", nil, true, false, nil, nil, "initial")
	requireSQLState(t, tx, "23514", sourcePageShapeSQL, windowID, fmt.Sprintf("page-%032x", 1101), int32(1101), "decoded", nil, true, true, nil, false, nil)
	// A failed page preserves zero, capture-only or capture-plus-decode receipts.
	for i := 0; i < 3; i++ {
		var complete any
		if i == 2 {
			complete = false
		}
		execLedger(t, tx, sourcePageShapeSQL, windowID, fmt.Sprintf("page-%032x", i+700), int32(i+700),
			string(model.DiscoveryPageFailed), "window_closed", i > 0, i == 2, nil, complete, "initial")
	}
}

const sourceRequestShapeSQL = `INSERT INTO ingest.artifact_request
	(request_key, source_window_id, source_rate_limit_id, document_version_key, owner_kind, owner_key,
	 purpose, expected_role, expected_response_kind, required, request_plan, request_cursor,
	 source_attempt_limit, retry_started_at, retry_deadline_at, state, reason, next_eligible_at,
	 capture_key, capture_digest, capture_bytes, transport_receipt, claimed_epoch, claimed_fence)
	SELECT $2, source_window_id, source_rate_limit_id, document_version_key, owner_kind, owner_key,
	 $8, $9, $10, required, request_plan, request_cursor, source_attempt_limit, retry_started_at,
	 retry_deadline_at, $3, $4, CASE WHEN $5::boolean THEN clock_timestamp() END,
	 CASE WHEN $6::boolean THEN 'capture-00000000000000000000000000000001' END,
	 CASE WHEN $6::boolean THEN repeat('a',64) END, CASE WHEN $6::boolean THEN 10 END,
	 CASE WHEN $6::boolean THEN '{}'::jsonb END, $7::bigint, $7::bigint
	FROM ingest.artifact_request WHERE id = $1`

func TestSourceArtifactRequestVocabularyShapes(t *testing.T) {
	tx := sourceFixture(t)
	seedSourceWindow(t, tx)
	requestID := seedSourceRequest(t, tx)
	for i, state := range []model.ArtifactRequestState{model.ArtifactRequestDue, model.ArtifactRequestLeased, model.ArtifactRequestFetched, model.ArtifactRequestVerified, model.ArtifactRequestFailed, model.ArtifactRequestExhausted, model.ArtifactRequestExpired} {
		var reason any
		if state == model.ArtifactRequestFailed || state == model.ArtifactRequestExhausted || state == model.ArtifactRequestExpired {
			reason = "source_outcome"
		}
		captured := state == model.ArtifactRequestFetched || state == model.ArtifactRequestVerified
		args := []any{requestID, fmt.Sprintf("request-%032x", i+100), string(state), reason,
			state == model.ArtifactRequestDue, captured, int64(1), "artifact", "main", ""}
		execLedger(t, tx, sourceRequestShapeSQL, args...)
		bad := append([]any(nil), args...)
		bad[1], bad[4] = fmt.Sprintf("request-%032x", i+200), state != model.ArtifactRequestDue
		requireSQLState(t, tx, "23514", sourceRequestShapeSQL, bad...)
		bad = append([]any(nil), args...)
		bad[1], bad[3] = fmt.Sprintf("request-%032x", i+300), "unexpected"
		if reason != nil {
			bad[3] = nil
		}
		requireSQLState(t, tx, "23514", sourceRequestShapeSQL, bad...)
		if reason != nil {
			bad = append([]any(nil), args...)
			bad[1], bad[3] = fmt.Sprintf("request-%032x", i+700), ""
			requireSQLState(t, tx, "23514", sourceRequestShapeSQL, bad...)
		}
		if state != model.ArtifactRequestFailed && state != model.ArtifactRequestExpired {
			bad = append([]any(nil), args...)
			bad[1], bad[5] = fmt.Sprintf("request-%032x", i+400), !captured
			requireSQLState(t, tx, "23514", sourceRequestShapeSQL, bad...)
		}
		if state == model.ArtifactRequestLeased {
			bad = append([]any(nil), args...)
			bad[1], bad[6] = fmt.Sprintf("request-%032x", i+500), nil
			requireSQLState(t, tx, "23514", sourceRequestShapeSQL, bad...)
		}
		if state == model.ArtifactRequestFailed || state == model.ArtifactRequestExpired {
			args[1], args[5] = fmt.Sprintf("request-%032x", i+600), true
			execLedger(t, tx, sourceRequestShapeSQL, args...)
		}
	}
	requireSQLState(t, tx, "23514", sourceRequestShapeSQL, requestID, fmt.Sprintf("request-%032x", 999), "outside", nil, false, false, nil, "artifact", "main", "")
	partialCapture := strings.Replace(sourceRequestShapeSQL, "CASE WHEN $6::boolean THEN 10 END", "NULL::bigint", 1)
	requireSQLState(t, tx, "23514", partialCapture, requestID, fmt.Sprintf("request-%032x", 899), "fetched", nil, false, true, int64(1), "artifact", "main", "")
	for i, role := range []model.ArtifactRole{model.ArtifactRoleMain, model.ArtifactRoleAppendix, model.ArtifactRoleOriginalScan} {
		execLedger(t, tx, sourceRequestShapeSQL, requestID, fmt.Sprintf("request-%032x", i+900), "due", nil, true, false, nil, "artifact", string(role), "")
	}
	for i, values := range []struct {
		purpose string
		role    any
		kind    string
		valid   bool
	}{
		{"artifact", nil, "", false}, {"detail", "main", "", false},
		{"artifact", "main", "metadata", false}, {"artifact", "outside", "", false},
		{"detail", nil, "outside", false}, {"listing", nil, "", false},
		{"detail", nil, "", true}, {"detail", nil, "metadata", true},
		{"detail", nil, "files", true}, {"detail", nil, "tree", true}, {"detail", nil, "diagram", true},
	} {
		args := []any{requestID, fmt.Sprintf("request-%032x", i+1000), "due", nil, true, false, nil, values.purpose, values.role, values.kind}
		if values.valid {
			execLedger(t, tx, sourceRequestShapeSQL, args...)
		} else {
			requireSQLState(t, tx, "23514", sourceRequestShapeSQL, args...)
		}
	}
}

func TestSourceArtifactRolesFormatsAndRetentionOwners(t *testing.T) {
	tx := sourceFixture(t)
	blobID := seedBronzeBlob(t, tx)
	recordID := requireSourceID(t, tx, "InsertSourceRecord", "source-a", "native-a")
	observationID := requireSourceID(t, tx, "InsertObservationRevision", "observation-00000000000000000000000000000001", recordID,
		fixtureDigest, fixtureDigest, fixtureDigest, fixtureDigest, "window-a", "version-a",
		"execution-00000000000000000000000000000001", "result-00000000000000000000000000000001", blobID, time.Now())
	for i, role := range []model.ArtifactRole{model.ArtifactRoleMain, model.ArtifactRoleAppendix, model.ArtifactRoleOriginalScan} {
		artifactID := requireSourceID(t, tx, "InsertSourceArtifact", fmt.Sprintf("artifact-%d", i), blobID, string(role), "", "", "https://example.com/a", "https://example.com/a")
		requireSourceID(t, tx, "LinkObservationArtifact", observationID, artifactID, string(role), int32(i), true)
	}
	requireSQLState(t, tx, "23514", `INSERT INTO bronze.artifact (artifact_key, blob_id, role, declared_filename, format, source_url, capture_url)
		VALUES ('outside-role', $1, 'outside', '', '', 'https://example.com/a', 'https://example.com/a')`, blobID)
	requireSQLState(t, tx, "23514", `INSERT INTO bronze.observation_artifact (observation_revision_id, artifact_id, role, ordinal, required)
		SELECT $1, id, 'outside', 99, true FROM bronze.artifact LIMIT 1`, observationID)
	for i, format := range []string{"", "unknown", "doc", "docx", "html", "pdf", "png", "jpeg", "tiff", "xlsx", "ppt", "pptx", "odp"} {
		if !model.ArtifactFormatValid(format) {
			t.Fatalf("fixture format %q left the model vocabulary", format)
		}
		requireSourceID(t, tx, "InsertSourceArtifact", fmt.Sprintf("format-%d", i), blobID, "main", "", format, "https://example.com/a", "https://example.com/a")
	}
	requireSQLState(t, tx, "23514", `INSERT INTO bronze.artifact (artifact_key, blob_id, role, declared_filename, format, source_url, capture_url)
		VALUES ('outside-format', $1, 'main', '', 'outside', 'https://example.com/a', 'https://example.com/a')`, blobID)
	key, role, locator := "large-filename", "main", "https://example.com/a"
	filename := strings.Repeat("x", model.MaxArtifactValueBytes-len(key)-len(role)-len(locator))
	requireSourceID(t, tx, "InsertSourceArtifact", key, blobID, role, filename, "", locator, locator)
	requireSQLState(t, tx, "23514", `INSERT INTO bronze.artifact (artifact_key, blob_id, role, declared_filename, format, source_url, capture_url)
		VALUES ($1, $2, $3, $4, '', $5, $5)`, "large-filenamf", blobID, role, filename+"x", locator)
	owners := []struct {
		kind model.RetentionOwnerKind
		key  string
	}{
		{model.RetentionOwnerExecution, "execution-00000000000000000000000000000001"},
		{model.RetentionOwnerObservationRevision, "observation-00000000000000000000000000000001"},
		{model.RetentionOwnerDocumentRevision, fixtureRevision + ":rev:" + fixtureDigest},
		{model.RetentionOwnerBuild, "00000000-0000-0000-0000-000000000001"},
		{model.RetentionOwnerRelease, "release-a"},
	}
	for _, owner := range owners {
		requireSourceID(t, tx, "AddBlobRetentionReference", blobID, string(owner.kind), owner.key)
		requireSQLState(t, tx, "23514", `INSERT INTO bronze.blob_retention_reference (blob_id, owner_kind, owner_key) VALUES ($1, $2, '')`, blobID, string(owner.kind))
	}
	for _, owner := range []struct{ kind, key string }{
		{"outside", "release-b"}, {"document_revision", fixtureRevision},
		{"build", "00000000-0000-0000-0000-000000000000"}, {"observation_revision", "observation-not-issued"},
	} {
		requireSQLState(t, tx, "23514", `INSERT INTO bronze.blob_retention_reference (blob_id, owner_kind, owner_key) VALUES ($1, $2, $3)`, blobID, owner.kind, owner.key)
	}
	requireSQLState(t, tx, "23514", `INSERT INTO bronze.observation_revision
		(observation_key, source_record_id, observation_digest, metadata_digest, source_tree_digest, relations_digest,
		 source_window_key, document_version_key, execution_key, result_key, capture_blob_id, observed_at)
		SELECT 'observation-not-issued', source_record_id, repeat('b',64), metadata_digest, source_tree_digest,
		 relations_digest, source_window_key, document_version_key, execution_key, result_key, capture_blob_id, observed_at
		FROM bronze.observation_revision WHERE id = $1`, observationID)
}

func TestSourceFetchedReceiptSurvivesRecovery(t *testing.T) {
	tx := sourceFixture(t)
	seedSourceWindow(t, tx)
	requestID := seedSourceRequest(t, tx)
	requireSourceID(t, tx, "ClaimArtifactRequest", append(sourceGrant("document_version", "version-a"), time.Now())...)
	args := transportArgs(requestID, 1, true, time.Now())
	stale := append([]any(nil), args...)
	stale[5] = int64(2)
	requireNoSourceRow(t, tx, "RecordArtifactTransport", stale...)
	if got := ledgerCount(t, tx, `SELECT in_flight FROM ingest.source_rate_limit`); got != 1 {
		t.Fatal("stale transport released another owner's host slot")
	}
	requireSourceID(t, tx, "RecordArtifactTransport", args...)
	requireNoSourceRow(t, tx, "RecordArtifactTransport", args...)
	execLedger(t, tx, `UPDATE ingest.execution_lease SET expires_at = clock_timestamp() - interval '1 second' WHERE resource_kind = 'document_version'`)
	requireLedgerID(t, tx, "ReclaimExecutionLease", int64(1), "writer-a", "document_version", "version-a", "owner-a", time.Now().Add(time.Hour))
	grant := sourceGrant("document_version", "version-a")
	grant[5] = int64(2)
	requireNoSourceRow(t, tx, "RecoverArtifactRequest", append(grant, requestID, time.Now())...)
	requireNoSourceRow(t, tx, "ClaimArtifactRequest", append(grant, time.Now())...)
	if got := ledgerCount(t, tx, `SELECT count(*) FROM ingest.artifact_request WHERE state = 'fetched' AND capture_digest = $1 AND source_attempts = 1`, fixtureDigest); got != 1 {
		t.Fatal("recovery erased a fetched receipt or spent another source attempt")
	}
	requireSQLState(t, tx, "55000", `UPDATE ingest.artifact_request SET transport_receipt = '{"changed":true}' WHERE id = $1`, requestID)
}

func TestSourcePageRetryUsesLeaseEligibilityAndPreservesEvidence(t *testing.T) {
	tx := sourceFixture(t)
	seedSourceWindow(t, tx)
	accepted := seedSourcePage(t, tx, 0)
	capturePage(t, tx, accepted, false)
	resultID := sourceResult(t, tx)
	requireSourceID(t, tx, "AcceptDiscoveryPage", append(sourceGrant("source_window", "window-a"), accepted, int32(0), false, resultID)...)
	failing := seedSourcePage(t, tx, 1)
	pending := seedSourcePage(t, tx, 2)
	retryAt := time.Now().Add(-time.Second)
	for attempt := 1; attempt <= 5; attempt++ {
		args := append(sourceGrant("source_window", "window-a"), failing,
			fmt.Sprintf("request-%032x", attempt+100), int32(attempt), retryAt, retryAt)
		requireSourceID(t, tx, "RecordDiscoveryPageSourceRetry", args...)
		requireNoSourceRow(t, tx, "RecordDiscoveryPageSourceRetry", args...)
	}
	if got := ledgerCount(t, tx, `SELECT count(*) FROM ingest.discovery_page WHERE id = $1 AND state = 'accepted'`, accepted); got != 1 {
		t.Fatal("window failure erased accepted page evidence")
	}
	if got := ledgerCount(t, tx, `SELECT count(*) FROM ingest.discovery_page WHERE id = $1 AND state = 'failed' AND reason = 'source_retry_exhausted' AND source_attempts = 5`, failing); got != 1 {
		t.Fatal("request allowance exhaustion did not fail its page")
	}
	if got := ledgerCount(t, tx, `SELECT count(*) FROM ingest.discovery_page WHERE id = $1 AND state = 'failed' AND reason = 'window_closed'`, pending); got != 1 {
		t.Fatal("window failure did not close its nonterminal page")
	}
	if got := ledgerCount(t, tx, `SELECT count(*) FROM ingest.source_window WHERE state = 'failed' AND committed_cursor = 'initial'`); got != 1 {
		t.Fatal("page failure changed the checkpoint or failed to close the window")
	}
}

func TestSourcePageRetryAfterBlocksCapture(t *testing.T) {
	tx := sourceFixture(t)
	seedSourceWindow(t, tx)
	pageID := seedSourcePage(t, tx, 0)
	future := time.Now().Add(time.Hour)
	requireSourceID(t, tx, "RecordDiscoveryPageSourceRetry", append(sourceGrant("source_window", "window-a"), pageID,
		"request-00000000000000000000000000000010", int32(1), future, future)...)
	requireNoSourceRow(t, tx, "RecordDiscoveryPageCapture", append(sourceGrant("source_window", "window-a"), pageID,
		"capture-00000000000000000000000000000001", fixtureDigest, int64(10), `{}`,
		"request-00000000000000000000000000000011", int32(2))...)
	if got := ledgerCount(t, tx, `SELECT count(*) FROM ingest.execution_lease WHERE resource_kind = 'source_window' AND next_eligible_at = $1`, future); got != 1 {
		t.Fatal("source retry did not persist lease eligibility")
	}
	if got := ledgerCount(t, tx, `SELECT count(*) FROM ingest.discovery_page WHERE state = 'requested' AND source_attempts = 1 AND capture_key IS NULL`); got != 1 {
		t.Fatal("retry eligibility allowed a premature capture")
	}
	requireSQLState(t, tx, "23514", `UPDATE ingest.discovery_page SET source_attempts = 0 WHERE id = $1`, pageID)
	requireSQLState(t, tx, "23514", `UPDATE ingest.discovery_page SET infrastructure_attempts = 4 WHERE id = $1`, pageID)
	requireSQLState(t, tx, "23514", `UPDATE ingest.discovery_page SET source_attempts = 6 WHERE id = $1`, pageID)
	requireSQLState(t, tx, "23514", `UPDATE ingest.discovery_page SET retry_after = NULL WHERE id = $1`, pageID)
}
