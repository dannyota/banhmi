package intake

import (
	"context"
	_ "embed"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

//go:embed queries/ledger.sql
var ledgerQueries string

const fixtureDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const fixtureRevision = "vn:doc:00000000-0000-0000-0000-000000000001"

// The hosted runner replays the writer chain before these tests. Each test
// rolls back its fixed identities; none commits into the shared replay database.
func ledgerFixture(t *testing.T) pgx.Tx {
	t.Helper()
	value := os.Getenv("BANHMI_CORPUS_WRITER_TEST_DATABASE_URL")
	if value == "" {
		t.Skip("BANHMI_CORPUS_WRITER_TEST_DATABASE_URL is absent")
	}
	u, err := url.Parse(value)
	if err != nil || u == nil || u.User == nil {
		t.Fatal("invalid synthetic writer test URL")
	}
	_, password := u.User.Password()
	if (u.Scheme != "postgres" && u.Scheme != "postgresql") ||
		(u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1") ||
		u.Port() != "5432" || u.Path != "/banhmi_corpus_writer_ci" ||
		u.User.Username() != "banhmi_corpus_writer_migrator" || password ||
		u.Fragment != "" || len(u.Query()) != 1 || u.Query().Get("sslmode") != "disable" {
		t.Fatal("schema tests require the isolated password-free CI writer service")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, value)
	if err != nil {
		t.Fatal("connect synthetic writer database failed")
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal("begin schema fixture failed")
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	execLedger(t, tx, `INSERT INTO ingest.country_admission
		(id, country_code, admission_epoch, admitted_writer, enabled)
		VALUES (1, 'vn', 1, 'writer-a', true)`)
	return tx
}

func ledgerQuery(t *testing.T, name string) string {
	t.Helper()
	marker := "-- name: " + name + " :"
	_, tail, ok := strings.Cut(ledgerQueries, marker)
	if !ok {
		t.Fatal("missing ledger query " + name)
	}
	_, tail, ok = strings.Cut(tail, "\n")
	if !ok {
		t.Fatal("missing query body " + name)
	}
	body, _, _ := strings.Cut(tail, "\n-- name:")
	return body
}

func execLedger(t *testing.T, tx pgx.Tx, query string, args ...any) {
	t.Helper()
	if _, err := tx.Exec(context.Background(), query, args...); err != nil {
		t.Fatalf("schema fixture SQL failed: %v", err)
	}
}

// All ledger RETURNING projections start with the surrogate bigint id.
func ledgerID(t *testing.T, tx pgx.Tx, name string, args ...any) (int64, error) {
	t.Helper()
	rows, err := tx.Query(context.Background(), ledgerQuery(t, name), args...)
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
		t.Fatal("ledger query must return a bigint id first")
	}
	if rows.Next() {
		t.Fatal("ledger query returned multiple receipts")
	}
	return id, rows.Err()
}

func requireLedgerID(t *testing.T, tx pgx.Tx, name string, args ...any) int64 {
	t.Helper()
	id, err := ledgerID(t, tx, name, args...)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return id
}

func requireNoLedgerRow(t *testing.T, tx pgx.Tx, name string, args ...any) {
	t.Helper()
	if _, err := ledgerID(t, tx, name, args...); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("%s must return no receipt, got %v", name, err)
	}
}

func requireSQLState(t *testing.T, tx pgx.Tx, state, query string, args ...any) {
	t.Helper()
	execLedger(t, tx, "SAVEPOINT negative_case")
	_, err := tx.Exec(context.Background(), query, args...)
	var databaseError *pgconn.PgError
	if !errors.As(err, &databaseError) || databaseError.Code != state {
		t.Fatalf("operation must fail with PostgreSQL state %s: %v", state, err)
	}
	execLedger(t, tx, "ROLLBACK TO SAVEPOINT negative_case")
	execLedger(t, tx, "RELEASE SAVEPOINT negative_case")
}

func executionArgs(fence int64, key string) []any {
	return []any{int64(1), "writer-a", "document_version", "version-a", "owner-a",
		fence, key, "document-ingest/vn/source-a/version-a", "DocumentIngest",
		"attempt-a", "intake", fixtureDigest, fixtureDigest, fixtureDigest,
		`{"max_items":1,"max_input_bytes":1024,"max_pages":1,"max_elapsed":1000000000,"max_concurrent":1}`, nil, `[]`}
}

func seedExecution(t *testing.T, tx pgx.Tx) int64 {
	t.Helper()
	requireLedgerID(t, tx, "ClaimExecutionLease", int64(1), "writer-a",
		"document_version", "version-a", "owner-a", time.Now().Add(time.Hour))
	return requireLedgerID(t, tx, "InsertExecution",
		executionArgs(1, "execution-00000000000000000000000000000001")...)
}

func resultArgs(executionID int64) []any {
	return []any{int64(1), "writer-a", "document_version", "version-a", "owner-a", int64(1),
		executionID, "result-00000000000000000000000000000001", "extract", "convert", "shard-a",
		"sha256:" + fixtureDigest, fixtureDigest, fixtureDigest, fixtureDigest, fixtureDigest,
		int64(0), "succeeded", int64(1), int64(1), int64(1), int64(0), int64(0), int64(0),
		int64(0), int64(0), "cursor-a", nil, nil, true, `{"outputs":[]}`}
}

func ledgerCount(t *testing.T, tx pgx.Tx, query string, args ...any) int64 {
	t.Helper()
	var count int64
	if err := tx.QueryRow(context.Background(), query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestAdmissionEpochFence(t *testing.T) {
	tx := ledgerFixture(t)
	executionID := seedExecution(t, tx)
	requireLedgerID(t, tx, "RotateAdmissionEpoch", int64(1), "writer-b", true)
	requireNoLedgerRow(t, tx, "RotateAdmissionEpoch", int64(1), "writer-c", true)
	requireNoLedgerRow(t, tx, "ClaimExecutionLease", int64(1), "writer-a",
		"document_version", "version-b", "owner-a", time.Now().Add(time.Hour))
	requireNoLedgerRow(t, tx, "InsertExecutionResult", resultArgs(executionID)...)
	if got := ledgerCount(t, tx, "SELECT count(*) FROM ingest.execution_lease"); got != 1 {
		t.Fatal("stale admission created a lease")
	}
	if got := ledgerCount(t, tx, "SELECT count(*) FROM ingest.execution_result"); got != 0 {
		t.Fatal("stale admission wrote a result")
	}
	if got := ledgerCount(t, tx, "SELECT admission_epoch FROM ingest.country_admission"); got != 2 {
		t.Fatal("stale rotation changed admission")
	}
	requireSQLState(t, tx, "23514", "UPDATE ingest.country_admission SET admission_epoch = 1")
	requireSQLState(t, tx, "23514", `INSERT INTO ingest.country_admission
		(id, country_code, admission_epoch, admitted_writer, enabled) VALUES (2, 'vn', 1, 'writer-a', true)`)
	// Phase 2 TestAdmissionEpochFence must also reject stale Apply and replay
	// before any output, acceptance, receipt or delivery write.
}

func TestExecutionLeaseFence(t *testing.T) {
	tx := ledgerFixture(t)
	executionID := seedExecution(t, tx)
	args := resultArgs(executionID)
	args[4] = "owner-b"
	requireNoLedgerRow(t, tx, "InsertExecutionResult", args...)
	args[4], args[5] = "owner-a", int64(2)
	requireNoLedgerRow(t, tx, "InsertExecutionResult", args...)
	execLedger(t, tx, "UPDATE ingest.execution_lease SET expires_at = clock_timestamp() - interval '1 second'")
	requireLedgerID(t, tx, "ReclaimExecutionLease", int64(1), "writer-a",
		"document_version", "version-a", "owner-b", time.Now().Add(time.Hour))
	if got := ledgerCount(t, tx, "SELECT attempt_fence FROM ingest.execution_lease"); got != 2 {
		t.Fatal("reclaim did not increment the fence")
	}
	requireNoLedgerRow(t, tx, "InsertExecutionResult", resultArgs(executionID)...)
	if got := ledgerCount(t, tx, "SELECT count(*) FROM ingest.execution_result"); got != 0 {
		t.Fatal("stale owner or fence wrote a result")
	}
	execLedger(t, tx, "UPDATE ingest.execution_lease SET cancelled_at = clock_timestamp()")
	requireLedgerID(t, tx, "ReclaimExecutionLease", int64(1), "writer-a",
		"document_version", "version-a", "owner-a", time.Now().Add(time.Hour))
	if got := ledgerCount(t, tx, "SELECT attempt_fence FROM ingest.execution_lease"); got != 3 {
		t.Fatal("second reclaim reused a fence")
	}
	// Phase 2 TestExecutionLeaseFence covers atomic Apply, including stale
	// replay and cancellation, across every store participating in acceptance.
}

func TestExecutionResultIdempotency(t *testing.T) {
	tx := ledgerFixture(t)
	executionID := seedExecution(t, tx)
	args := resultArgs(executionID)
	id := requireLedgerID(t, tx, "InsertExecutionResult", args...)
	if replay := requireLedgerID(t, tx, "InsertExecutionResult", args...); replay != id {
		t.Fatal("identical replay did not return the committed receipt")
	}
	args[30] = `{"outputs":["changed"]}`
	requireNoLedgerRow(t, tx, "InsertExecutionResult", args...)
	requireSQLState(t, tx, "23505", `INSERT INTO ingest.execution_result
		(execution_id, result_key, stage, substep, shard, idempotency_key,
		input_digest, output_digest, recipe_digest, budget_digest, decision_revision,
		outcome, expected, processed, succeeded, pending_retry, terminal_gap, rejected,
		failed, accepted_gaps, cursor, next_eligible_at, gap_acceptance_ref, complete, payload)
		SELECT execution_id, 'result-00000000000000000000000000000002', stage, substep,
		shard, idempotency_key, input_digest, output_digest, recipe_digest, budget_digest,
		decision_revision, outcome, expected, processed, succeeded, pending_retry,
		terminal_gap, rejected, failed, accepted_gaps, cursor, next_eligible_at,
		gap_acceptance_ref, complete, '{"outputs":["changed"]}'::jsonb
		FROM ingest.execution_result WHERE id = $1`, id)
	requireSQLState(t, tx, "55000", "UPDATE ingest.execution_result SET payload = '{}' WHERE id = $1", id)
	requireSQLState(t, tx, "55000", "DELETE FROM ingest.execution_result WHERE id = $1", id)
	if got := ledgerCount(t, tx, "SELECT count(*) FROM ingest.execution_result"); got != 1 {
		t.Fatal("changed replay created another receipt")
	}
	if got := requireLedgerID(t, tx, "FindExecutionResult", "sha256:"+fixtureDigest); got != id {
		t.Fatal("response loss lookup did not recover the committed receipt")
	}
	var payload string
	if err := tx.QueryRow(context.Background(), "SELECT payload::text FROM ingest.execution_result WHERE id = $1", id).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, "changed") {
		t.Fatal("changed replay replaced the immutable payload")
	}
	// Phase 2 TestExecutionResultIdempotency maps a changed replay to
	// ErrPayloadConflict and checks every neutral immutable payload field.
}

func TestRevisionAcceptanceEligibility(t *testing.T) {
	tx := ledgerFixture(t)
	executionID := seedExecution(t, tx)
	resultID := requireLedgerID(t, tx, "InsertExecutionResult", resultArgs(executionID)...)
	states := []string{"candidate", "quarantined", "accepted", "accepted_with_gaps", "excluded"}
	var eventID int64
	for i, state := range states {
		attempt := "attempt-" + state
		eventID = requireLedgerID(t, tx, "InsertRevisionAcceptance", fixtureRevision,
			attempt, executionID, resultID, fixtureDigest, fixtureDigest, int64(i), state, `[]`, int64(i+1))
		requireLedgerID(t, tx, "UpsertRevisionAcceptanceState", eventID)
	}
	rows, err := tx.Query(context.Background(), ledgerQuery(t, "ListBuildEligibleAcceptance"), int64(5), int32(10))
	if err != nil {
		t.Fatal(err)
	}
	var eligible int
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			t.Fatal(err)
		}
		state := values[4].(string)
		if state != "accepted" && state != "accepted_with_gaps" {
			t.Fatal("non-eligible state entered build selection")
		}
		eligible++
	}
	rows.Close()
	if rows.Err() != nil || eligible != 2 {
		t.Fatal("build eligibility must contain exactly the two accepted states")
	}
	requireNoLedgerRow(t, tx, "UpsertRevisionAcceptanceState", eventID)
	if got := ledgerCount(t, tx, `SELECT count(*) FROM ingest.revision_acceptance a
		JOIN ingest.revision_acceptance_state s ON s.acceptance_id = a.id
		WHERE a.execution_id = $1 AND a.execution_result_id = $2
		AND a.candidate_digest = $3 AND a.quality_report_hash = $3
		AND a.state = s.state AND a.watermark = s.watermark
		AND a.decision_revision = a.watermark - 1`, executionID, resultID, fixtureDigest); got != 5 {
		t.Fatal("acceptance projection lost its event provenance")
	}
	requireSQLState(t, tx, "55000", "DELETE FROM ingest.revision_acceptance WHERE id = $1", eventID)
	requireSQLState(t, tx, "23514", `INSERT INTO ingest.revision_acceptance
		(revision_key, processing_attempt_key, execution_id, execution_result_id, candidate_digest,
		quality_report_hash, decision_revision, state, scoped_gaps, watermark)
		VALUES ($1, 'invalid', $2, $3, $4, $4, 0, 'unknown', '[]', 6)`,
		fixtureRevision, executionID, resultID, fixtureDigest)
	requireSQLState(t, tx, "23514", `INSERT INTO ingest.revision_acceptance
		(revision_key, processing_attempt_key, execution_id, execution_result_id, candidate_digest,
		quality_report_hash, decision_revision, state, scoped_gaps, watermark)
		VALUES ($1, 'invalid', $2, $3, 'invalid-digest', $4, 0, 'candidate', '[]', 6)`,
		fixtureRevision, executionID, resultID, fixtureDigest)
	// Phase 2 TestRevisionAcceptanceEligibility validates event/projection
	// bindings, decision authority, named gaps, report gates and comparison.
}

func TestDeliveryRecordReconciliation(t *testing.T) {
	tx := ledgerFixture(t)
	seedExecution(t, tx)
	args := []any{"delivery-a", "launch", "document-ingest/vn/source-a/version-a",
		"execution", "execution-00000000000000000000000000000001", int64(1), fixtureDigest,
		time.Now().Add(-time.Hour)}
	id := requireLedgerID(t, tx, "InsertDeliveryRecord", args...)
	if replay := requireLedgerID(t, tx, "InsertDeliveryRecord", args...); replay != id {
		t.Fatal("duplicate delivery did not return the prior receipt")
	}
	args[2] = "document-ingest/vn/source-a/version-b"
	requireNoLedgerRow(t, tx, "InsertDeliveryRecord", args...)
	if prior := requireLedgerID(t, tx, "FindDeliveryRecord", "delivery-a"); prior != id {
		t.Fatal("delivery lookup did not recover the prior receipt")
	}
	args[2] = "document-ingest/vn/source-a/version-a"
	args[0], args[7] = "delivery-future", time.Now().Add(time.Hour)
	requireLedgerID(t, tx, "InsertDeliveryRecord", args...)
	if due := requireLedgerID(t, tx, "ListDueUnacknowledgedDelivery", int32(1)); due != id {
		t.Fatal("bounded reconciliation selected the wrong due record")
	}
	execLedger(t, tx, "UPDATE ingest.delivery_record SET acknowledged_at = clock_timestamp() WHERE id = $1", id)
	requireNoLedgerRow(t, tx, "ListDueUnacknowledgedDelivery", int32(10))
	// Phase 2 TestDeliveryRecordReconciliation checks atomic result/outbox
	// commit, crash recovery and exact immutable duplicate-delivery bindings.
}
