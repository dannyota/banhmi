package intake

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
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

func ledgerTestURL(t *testing.T) *url.URL {
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
	return u
}

// The hosted runner replays the writer chain before these tests. Shared fixtures
// roll back their fixed identities; the lock-wait test commits only in its copy.
func ledgerFixture(t *testing.T) pgx.Tx {
	t.Helper()
	value := ledgerTestURL(t).String()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, value)
	if err != nil {
		t.Fatal("connect synthetic writer database failed")
	}
	t.Cleanup(func() { sourceCleanup(t, ctx, conn.Close) })
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal("begin schema fixture failed")
	}
	t.Cleanup(func() { sourceCleanup(t, ctx, tx.Rollback) })
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

// Each case copies the replayed database and uses only its own identities.
// No parallel case or shared rollback fixture participates in the lock wait.
func TestGuardedInsertRejectsLeaseExpiredDuringLockWait(t *testing.T) {
	for _, name := range []string{"InsertExecution", "InsertExecutionResult"} {
		t.Run(name, func(t *testing.T) {
			u := ledgerTestURL(t)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			t.Cleanup(cancel)
			adminURL := *u
			adminURL.User = url.User("postgres")
			adminURL.Path = "/postgres"
			admin, err := pgx.Connect(ctx, adminURL.String())
			if err != nil {
				t.Fatal("connect disposable database administrator failed")
			}
			t.Cleanup(func() { sourceCleanup(t, ctx, admin.Close) })
			database := fmt.Sprintf("banhmi_ledger_lock_wait_%d_%d", os.Getpid(), time.Now().UnixNano())
			identifier := pgx.Identifier{database}.Sanitize()
			if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier+
				" TEMPLATE banhmi_corpus_writer_ci OWNER banhmi_corpus_writer_migrator"); err != nil {
				t.Fatalf("copy replayed writer database: %v", err)
			}
			t.Cleanup(func() {
				sourceCleanup(t, ctx, func(cleanup context.Context) error {
					_, err := admin.Exec(cleanup, "DROP DATABASE "+identifier+" WITH (FORCE)")
					return err
				})
			})
			u.Path = "/" + database
			a, err := pgx.Connect(ctx, u.String())
			if err != nil {
				t.Fatal("connect lock holder failed")
			}
			t.Cleanup(func() { sourceCleanup(t, ctx, a.Close) })
			b, err := pgx.Connect(ctx, u.String())
			if err != nil {
				t.Fatal("connect guarded writer failed")
			}
			t.Cleanup(func() { sourceCleanup(t, ctx, b.Close) })
			seed, err := a.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { sourceCleanup(t, ctx, seed.Rollback) })
			execLedger(t, seed, `INSERT INTO ingest.country_admission
				(id, country_code, admission_epoch, admitted_writer, enabled)
				VALUES (1, 'vn', 1, 'writer-lock-wait', true)`)
			requireLedgerID(t, seed, "ClaimExecutionLease", int64(1), "writer-lock-wait",
				"document_version", "version-lock-wait", "owner-lock-wait", time.Now().Add(time.Hour))
			args := executionArgs(1, "execution-00000000000000000000000000000011")
			args[1], args[3], args[4] = "writer-lock-wait", "version-lock-wait", "owner-lock-wait"
			args[7], args[9] = "document-ingest/vn/lock-wait/seed", "attempt-lock-wait"
			executionID := requireLedgerID(t, seed, "InsertExecution", args...)
			if err := seed.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			// Commit the synthetic expiry before A locks the row. A never updates
			// it, which preserves the unchanged-row predicate-before-lock race.
			if _, err := a.Exec(ctx, `UPDATE ingest.execution_lease
				SET expires_at = clock_timestamp() + interval '2 seconds'
				WHERE resource_kind = 'document_version' AND resource_key = 'version-lock-wait'`); err != nil {
				t.Fatal(err)
			}
			holder, err := a.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { sourceCleanup(t, ctx, holder.Rollback) })
			var expiresAt time.Time
			if err := holder.QueryRow(ctx, `SELECT expires_at FROM ingest.execution_lease
				WHERE resource_kind = 'document_version' AND resource_key = 'version-lock-wait'
				FOR UPDATE`).Scan(&expiresAt); err != nil {
				t.Fatal(err)
			}
			writer, err := b.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { sourceCleanup(t, ctx, writer.Rollback) })
			args[6], args[7] = "execution-00000000000000000000000000000012", "document-ingest/vn/lock-wait/blocked"
			if name == "InsertExecutionResult" {
				args = resultArgs(executionID)
				args[1], args[3], args[4] = "writer-lock-wait", "version-lock-wait", "owner-lock-wait"
				args[7] = "result-00000000000000000000000000000011"
				args[11] = "sha256:" + strings.Repeat("b", 64)
			}
			query := ledgerQuery(t, name)
			type insertOutcome struct {
				rows int64
				err  error
			}
			done := make(chan insertOutcome, 1)
			go func() {
				tag, err := writer.Exec(ctx, query, args...)
				done <- insertOutcome{rows: tag.RowsAffected(), err: err}
				close(done)
			}()
			t.Cleanup(func() {
				cancel()
				<-done
			})
			// Observe B waiting on A while the grant is still live. A fixed sleep
			// alone could pass without ever exercising the blocking lock.
			for {
				var blocked, live bool
				if err := holder.QueryRow(ctx, `SELECT $1::integer = ANY(pg_blocking_pids($2::integer)),
					clock_timestamp() < expires_at FROM ingest.execution_lease
					WHERE resource_kind = 'document_version' AND resource_key = 'version-lock-wait'`,
					int32(a.PgConn().PID()), int32(b.PgConn().PID())).Scan(&blocked, &live); err != nil {
					t.Fatal(err)
				}
				if !live {
					t.Fatal("lease expired before observing the guarded insert blocked on A")
				}
				if blocked {
					break
				}
				select {
				case outcome := <-done:
					t.Fatalf("guarded insert did not wait on A: %+v", outcome)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(10 * time.Millisecond):
				}
			}
			// Use the database clock and release 50 ms beyond the stored expiry.
			if _, err := holder.Exec(ctx, `SELECT pg_sleep(
				GREATEST(0, extract(epoch FROM ($1::timestamptz - clock_timestamp()))) + 0.05)`, expiresAt); err != nil {
				t.Fatal(err)
			}
			if err := holder.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case outcome := <-done:
				if outcome.err != nil || outcome.rows != 0 {
					t.Fatalf("expired grant returned a receipt: rows=%d err=%v", outcome.rows, outcome.err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if got := ledgerCount(t, writer, "SELECT count(*) FROM ingest.execution"); got != 1 {
				t.Fatal("expired grant inserted an execution beyond the seed")
			}
			if got := ledgerCount(t, writer, "SELECT count(*) FROM ingest.execution_result"); got != 0 {
				t.Fatal("expired grant inserted a result")
			}
		})
	}
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
	args[0], args[7] = "delivery-later-due", time.Now().Add(-30*time.Minute)
	laterID := requireLedgerID(t, tx, "InsertDeliveryRecord", args...)
	args[0], args[7] = "delivery-future", time.Now().Add(time.Hour)
	requireLedgerID(t, tx, "InsertDeliveryRecord", args...)
	if due := requireLedgerID(t, tx, "ListDueUnacknowledgedDelivery", int32(1)); due != id {
		t.Fatal("bounded reconciliation selected the wrong due record")
	}
	execLedger(t, tx, "UPDATE ingest.delivery_record SET acknowledged_at = clock_timestamp() WHERE id = $1", id)
	if due := requireLedgerID(t, tx, "ListDueUnacknowledgedDelivery", int32(10)); due != laterID {
		t.Fatal("acknowledgement hid the other due record")
	}
	execLedger(t, tx, "UPDATE ingest.delivery_record SET acknowledged_at = clock_timestamp() WHERE id = $1", laterID)
	requireNoLedgerRow(t, tx, "ListDueUnacknowledgedDelivery", int32(10))
	// Phase 2 TestDeliveryRecordReconciliation checks atomic result/outbox
	// commit, crash recovery and exact immutable duplicate-delivery bindings.
}
