package intake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"danny.vn/banhmi/internal/corpus/blob"
	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/run"
	"danny.vn/banhmi/internal/corpus/store/intake/intakedb"
)

type adapterFixture struct {
	ctx    context.Context
	conn   *pgx.Conn
	u      *url.URL
	ledger *Ledger
	r      run.IntakeApplyRequest
}

// Each adapter test owns a committed copy of the replayed hosted database.
// It reuses the schema suite's password-free URL guard and skips only if absent.
func newAdapterFixture(t *testing.T) adapterFixture {
	t.Helper()
	u := ledgerTestURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	adminURL := *u
	adminURL.User, adminURL.Path = url.User("postgres"), "/postgres"
	admin, err := pgx.Connect(ctx, adminURL.String())
	if err != nil {
		t.Fatal("connect disposable adapter administrator failed")
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	name := fmt.Sprintf("banhmi_adapter_%d_%d", os.Getpid(), time.Now().UnixNano())
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier+" TEMPLATE banhmi_corpus_writer_ci OWNER banhmi_corpus_writer_migrator"); err != nil {
		t.Fatalf("copy replayed adapter database: %v", err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP DATABASE "+identifier+" WITH (FORCE)"); err != nil {
			t.Errorf("drop disposable adapter database: %v", err)
		}
	})
	u.Path = "/" + name
	conn, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal("connect disposable adapter database failed")
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	if _, err := conn.Exec(ctx, `INSERT INTO ingest.country_admission
		(id, country_code, admission_epoch, admitted_writer, enabled) VALUES (1, 'vn', 1, 'writer-a', true)`); err != nil {
		t.Fatal(err)
	}
	l, err := NewLedger(conn, "writer-a", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	e := run.ScopedExecution{Country: "vn", WorkflowID: "document-ingest/vn/source-a/version-a",
		WorkflowKind: run.DocumentIngest, AttemptKey: "attempt-a", AdmissionEpoch: 1, Owner: "owner-a",
		Scope: run.ExecutionScope{Kind: run.ScopeIntake, Subject: run.IntakeSubject{Kind: run.SubjectDocumentVersion, DocumentVersion: "version-a"}}}
	lease, err := l.ClaimLease(ctx, 1, e.Scope, e.Owner, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	e.AttemptFence = lease.AttemptFence
	w := run.IntakeWork{Execution: e, Stage: run.Extract, Substep: "convert", Shard: "shard-a",
		InputDigest: fixtureDigest, RecipeDigest: fixtureDigest, BudgetDigest: fixtureDigest,
		Budget: run.Budget{MaxItems: 1, MaxInputBytes: 1024, MaxPages: 1, MaxElapsed: time.Second, MaxConcurrent: 1}}
	if _, err := l.RegisterExecution(ctx, w, "transport-run-a", nil); err != nil {
		t.Fatal(err)
	}
	r := run.IntakeApplyRequest{Work: w, IdempotencyKey: run.IntakeIdempotencyKey(w), OutputDigest: fixtureDigest,
		Outcome: run.OutcomeSucceeded, Counts: run.Counts{Expected: 1, Processed: 1, Succeeded: 1}, Complete: true}
	return adapterFixture{ctx: ctx, conn: conn, u: u, ledger: l, r: r}
}

func adapterCount(t *testing.T, f adapterFixture, table string) int64 {
	t.Helper()
	var count int64
	if err := f.conn.QueryRow(f.ctx, "SELECT count(*) FROM "+pgx.Identifier{"ingest", table}.Sanitize()).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func requireAdapterWrites(t *testing.T, f adapterFixture, results, acceptance, deliveries int64) {
	t.Helper()
	for table, want := range map[string]int64{"execution_result": results, "revision_acceptance": acceptance,
		"revision_acceptance_state": acceptance, "revision_receipt": acceptance, "delivery_record": deliveries} {
		if got := adapterCount(t, f, table); got != want {
			t.Fatalf("%s rows = %d, want %d", table, got, want)
		}
	}
}

func TestLedgerApplyReplayAndResponseLoss(t *testing.T) {
	f := newAdapterFixture(t)
	first, err := f.ledger.Apply(f.ctx, f.r)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Validate(); err != nil {
		t.Fatal(err)
	}
	if first.RunReceipt != "transport-run-a" {
		t.Fatal("result lost transport provenance")
	}
	// Treat the first response as lost. The caller retries only its request.
	recovered, err := f.ledger.Apply(f.ctx, f.r)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, recovered) {
		t.Fatal("replay changed the committed receipt")
	}
	requireAdapterWrites(t, f, 1, 0, 1)
	// Simulate a crash before acknowledgement: no acknowledgement write occurs.
	due, err := f.ledger.DueDeliveries(f.ctx, 1)
	if err != nil || len(due) != 1 {
		t.Fatalf("reconciliation must find one completion: %v", err)
	}
	if due[0].ReceiptKey != first.ResultID || due[0].AcknowledgedAt != nil || due[0].ExpectedEpoch != 1 || due[0].ExpectedHash != string(f.r.OutputDigest) {
		t.Fatal("completion intent does not bind the committed result")
	}
}

func TestLedgerChangedReplayPayload(t *testing.T) {
	for _, name := range []string{"outcome", "outputs", "output-digest", "cursor", "counts"} {
		t.Run(name, func(t *testing.T) {
			f := newAdapterFixture(t)
			if _, err := f.ledger.Apply(f.ctx, f.r); err != nil {
				t.Fatal(err)
			}
			changed := f.r
			switch name {
			case "outcome":
				changed.Outcome = run.OutcomeFailed
			case "outputs":
				changed.Outputs = []model.AssetRef{{Path: "staged/changed", SHA256: fixtureDigest, ByteSize: 1}}
			case "output-digest":
				changed.OutputDigest = model.Digest(strings.Repeat("b", 64))
			case "cursor":
				changed.Cursor = "changed"
			case "counts":
				changed.Counts, changed.Complete = run.Counts{Expected: 1}, false
			}
			if _, err := f.ledger.Apply(f.ctx, changed); !errors.Is(err, run.ErrPayloadConflict) {
				t.Fatalf("changed %s must return payload conflict: %v", name, err)
			}
			requireAdapterWrites(t, f, 1, 0, 1)
		})
	}
}

func TestLedgerStaleGrantsWriteNothing(t *testing.T) {
	for _, replay := range []bool{false, true} {
		for _, name := range []string{"epoch", "writer", "owner", "fence", "expired", "cancelled", "budget", "budget-digest", "input", "recipe"} {
			t.Run(fmt.Sprintf("%s/replay-%t", name, replay), func(t *testing.T) {
				f := newAdapterFixture(t)
				var expected int64
				if replay {
					if _, err := f.ledger.Apply(f.ctx, f.r); err != nil {
						t.Fatal(err)
					}
					expected = 1
				}
				query := ""
				switch name {
				case "epoch":
					if _, err := f.ledger.RotateAdmission(f.ctx, 1, "writer-a", true); err != nil {
						t.Fatal(err)
					}
				case "writer":
					f.ledger.writer = "writer-b"
				case "owner":
					query = `UPDATE ingest.execution_lease SET owner = 'owner-b'`
				case "fence":
					query = `UPDATE ingest.execution_lease SET attempt_fence = attempt_fence + 1`
				case "expired":
					query = `UPDATE ingest.execution_lease SET expires_at = clock_timestamp() - interval '1 second'`
				case "cancelled":
					query = `UPDATE ingest.execution_lease SET cancelled_at = clock_timestamp()`
				case "budget":
					f.r.Work.Budget.MaxPages++
				case "budget-digest":
					f.r.Work.BudgetDigest = model.Digest(strings.Repeat("b", 64))
				case "input":
					f.r.Work.InputDigest = model.Digest(strings.Repeat("b", 64))
				case "recipe":
					f.r.Work.RecipeDigest = model.Digest(strings.Repeat("b", 64))
				}
				if query != "" {
					if _, err := f.conn.Exec(f.ctx, query); err != nil {
						t.Fatal(err)
					}
				}
				f.r.IdempotencyKey = run.IntakeIdempotencyKey(f.r.Work)
				if _, err := f.ledger.Apply(f.ctx, f.r); !errors.Is(err, run.ErrStaleGrant) {
					t.Fatalf("stale grant must return typed error: %v", err)
				}
				requireAdapterWrites(t, f, expected, 0, expected)
			})
		}
	}
}

func TestLedgerReclaimReplayKeepsOriginalReceipt(t *testing.T) {
	f := newAdapterFixture(t)
	first, err := f.ledger.Apply(f.ctx, f.r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.conn.Exec(f.ctx, `UPDATE ingest.execution_lease SET cancelled_at = clock_timestamp()`); err != nil {
		t.Fatal(err)
	}
	lease, err := f.ledger.ReclaimLease(f.ctx, 1, f.r.Work.Execution.Scope, "owner-b", time.Now().Add(time.Hour))
	if err != nil || lease.AttemptFence != 2 {
		t.Fatalf("reclaim must allocate fence 2: %v", err)
	}
	f.r.Work.Execution.Owner, f.r.Work.Execution.AttemptFence = lease.Owner, lease.AttemptFence
	if _, err := f.ledger.RegisterExecution(f.ctx, f.r.Work, "transport-run-b", nil); err != nil {
		t.Fatal(err)
	}
	replayed, err := f.ledger.Apply(f.ctx, f.r)
	if err != nil || !reflect.DeepEqual(first, replayed) {
		t.Fatalf("new current owner must recover original receipt: %v", err)
	}
	requireAdapterWrites(t, f, 1, 0, 1)
}

type fixtureAcceptanceVerifier struct {
	err   error
	calls int
	gaps  []model.GapRef
}

func (v *fixtureAcceptanceVerifier) VerifyAcceptance(context.Context, pgx.Tx, run.IntakeApplyRequest, json.RawMessage) ([]model.GapRef, error) {
	v.calls++
	return v.gaps, v.err
}

func fixtureAcceptance(r run.IntakeApplyRequest) run.IntakeApplyRequest {
	r.Acceptance, r.RevisionKey = model.Accepted, fixtureRevision
	r.CandidateDigest, r.QualityReportHash, r.OriginalHash = fixtureDigest, fixtureDigest, fixtureDigest
	return r
}

func TestLedgerAcceptanceAtomicAndVerifiedOnReplay(t *testing.T) {
	f := newAdapterFixture(t)
	v := &fixtureAcceptanceVerifier{}
	f.ledger.verifier = v
	f.r = fixtureAcceptance(f.r)
	first, err := f.ledger.Apply(f.ctx, f.r)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.ledger.Apply(f.ctx, f.r)
	if err != nil || !reflect.DeepEqual(first, second) || v.calls != 2 {
		t.Fatalf("acceptance replay must recheck trusted gates: %v", err)
	}
	requireAdapterWrites(t, f, 1, 1, 1)
	eligible, err := f.ledger.EligibleAcceptance(f.ctx, 1<<62, 1)
	if err != nil || len(eligible) != 1 || eligible[0].RevisionKey != fixtureRevision || eligible[0].State != "accepted" {
		t.Fatalf("accepted projection must be build eligible: %v", err)
	}
	v.err = run.ErrStaleGrant
	if _, err := f.ledger.Apply(f.ctx, f.r); !errors.Is(err, run.ErrStaleGrant) {
		t.Fatalf("revoked verifier authority must reject replay: %v", err)
	}
	requireAdapterWrites(t, f, 1, 1, 1)
}

func TestLedgerAcceptanceFailureRollsBack(t *testing.T) {
	for _, name := range []string{"missing-verifier", "denied-gate", "projection-write", "delivery-write"} {
		t.Run(name, func(t *testing.T) {
			f := newAdapterFixture(t)
			f.r = fixtureAcceptance(f.r)
			if name != "missing-verifier" {
				f.ledger.verifier = &fixtureAcceptanceVerifier{}
			}
			if name == "denied-gate" {
				f.ledger.verifier = &fixtureAcceptanceVerifier{err: errors.New("mandatory gate failed")}
			}
			if name == "projection-write" || name == "delivery-write" {
				table := "revision_acceptance_state"
				if name == "delivery-write" {
					table = "delivery_record"
				}
				// Disposable database only: force failure after result insertion.
				if _, err := f.conn.Exec(f.ctx, "ALTER TABLE "+pgx.Identifier{"ingest", table}.Sanitize()+" ADD CONSTRAINT fixture_reject CHECK (false)"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.ledger.Apply(f.ctx, f.r); err == nil {
				t.Fatal("acceptance failure must reject the transaction")
			}
			requireAdapterWrites(t, f, 0, 0, 0)
		})
	}
}

func TestLedgerAcceptanceCannotDowngradeAttempt(t *testing.T) {
	f := newAdapterFixture(t)
	f.ledger.verifier = &fixtureAcceptanceVerifier{}
	f.r = fixtureAcceptance(f.r)
	if _, err := f.ledger.Apply(f.ctx, f.r); err != nil {
		t.Fatal(err)
	}
	f.r.Work.Stage, f.r.Acceptance = run.Normalize, model.Quarantined
	f.r.IdempotencyKey = run.IntakeIdempotencyKey(f.r.Work)
	if _, err := f.ledger.Apply(f.ctx, f.r); err == nil {
		t.Fatal("new stage cannot downgrade an accepted processing attempt")
	}
	requireAdapterWrites(t, f, 1, 1, 1)
	eligible, err := f.ledger.EligibleAcceptance(f.ctx, 1<<62, 1)
	if err != nil || len(eligible) != 1 || eligible[0].State != "accepted" {
		t.Fatalf("failed downgrade must preserve accepted state: %v", err)
	}
}

func TestLedgerDecisionBindingsRejectMissingRecord(t *testing.T) {
	f := newAdapterFixture(t)
	f.ledger.verifier = &fixtureAcceptanceVerifier{}
	f.r = fixtureAcceptance(f.r)
	f.r.Acceptance = model.Excluded
	f.r.Work.DecisionRevision = 1
	f.r.IdempotencyKey = run.IntakeIdempotencyKey(f.r.Work)
	f.r.Decision = &run.DecisionBinding{DecisionKey: "decision-a", ActorKey: "reviewer-a", Action: model.DecisionExclude,
		ExpiresAt: time.Now().Add(time.Hour), AttemptFence: 1, AdmissionEpoch: 1, DecisionRevision: 1,
		Scope: f.r.Work.Execution.Scope, RevisionKey: f.r.RevisionKey, CandidateDigest: f.r.CandidateDigest,
		ReportHash: f.r.QualityReportHash, OriginalHash: f.r.OriginalHash, OutputDigest: f.r.OutputDigest, ScopeManifest: fixtureDigest}
	if _, err := f.ledger.Apply(f.ctx, f.r); !errors.Is(err, run.ErrStaleGrant) {
		t.Fatalf("caller assertions without authoritative decision must fail: %v", err)
	}
	requireAdapterWrites(t, f, 0, 0, 0)
}

func TestLedgerDeliveryReplayAndPolicyLookup(t *testing.T) {
	f := newAdapterFixture(t)
	var executionKey string
	if err := f.conn.QueryRow(f.ctx, `SELECT execution_key FROM ingest.execution`).Scan(&executionKey); err != nil {
		t.Fatal(err)
	}
	p := intakedb.InsertDeliveryRecordParams{DeduplicationKey: "launch-a", DeliveryKind: "launch",
		RecipientWorkflowID: f.r.Work.Execution.WorkflowID, ReceiptKind: "execution", ReceiptKey: executionKey,
		ExpectedEpoch: 1, ExpectedHash: fixtureDigest, DueAt: time.Now().UTC().Truncate(time.Microsecond)}
	first, err := f.ledger.RecordDelivery(f.ctx, f.r.Work, p)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.ledger.RecordDelivery(f.ctx, f.r.Work, p)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("delivery replay must preserve intent: %v", err)
	}
	p.ExpectedHash = strings.Repeat("b", 64)
	if _, err := f.ledger.RecordDelivery(f.ctx, f.r.Work, p); !errors.Is(err, run.ErrPayloadConflict) {
		t.Fatalf("changed delivery must conflict: %v", err)
	}
	if _, err := f.conn.Exec(f.ctx, `INSERT INTO config.policy_bundle (digest, manifest) VALUES ($1, '{"fixture":true}')`, fixtureDigest); err != nil {
		t.Fatal(err)
	}
	policy, err := f.ledger.PolicyBundle(f.ctx, fixtureDigest)
	if err != nil || policy.Digest != fixtureDigest || !strings.Contains(string(policy.Manifest), "fixture") {
		t.Fatalf("policy lookup must return frozen bundle: %v", err)
	}
	if _, err := f.ledger.DueDeliveries(f.ctx, 0); err == nil {
		t.Fatal("unbounded delivery scan must fail")
	}
}

// Observe an actual database lock wait rather than assuming a sleep caused one.
func waitAdapterLock(t *testing.T, ctx context.Context, observer *pgx.Conn, pid uint32) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := observer.Exec(ctx, `SELECT pg_stat_clear_snapshot()`); err != nil {
			t.Fatal(err)
		}
		var waiting bool
		if err := observer.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE pid = $1 AND wait_event_type = 'Lock')`, int64(pid)).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("adapter never reached the expected database lock wait")
		case <-ticker.C:
		}
	}
}

type fixtureObjectStore struct {
	content string
}

func (s fixtureObjectStore) Open(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(s.content)), nil
}

func (fixtureObjectStore) Put(context.Context, blob.Metadata, io.Reader) (blob.Object, error) {
	return blob.Object{}, errors.New("fixture storage is read-only")
}

func (fixtureObjectStore) Stat(context.Context, string) (blob.Object, error) {
	return blob.Object{}, errors.New("fixture supports byte verification only")
}

func (fixtureObjectStore) Delete(context.Context, string) error {
	return errors.New("fixture storage is immutable")
}

func TestLedgerVerifiesStagedBytes(t *testing.T) {
	for _, name := range []string{"exact", "changed", "short", "long", "missing-store"} {
		t.Run(name, func(t *testing.T) {
			f := newAdapterFixture(t)
			content := "synthetic output"
			digest := sha256.Sum256([]byte(content))
			f.r.Outputs = []model.AssetRef{{Path: "staged/output", ByteSize: int64(len(content)), SHA256: model.Digest(hex.EncodeToString(digest[:]))}}
			switch name {
			case "changed":
				content = "different output"
			case "short":
				content = content[:1]
			case "long":
				content += "extra"
			}
			if name != "missing-store" {
				f.ledger.objects = fixtureObjectStore{content: content}
			}
			_, err := f.ledger.Apply(f.ctx, f.r)
			if name == "exact" {
				if err != nil {
					t.Fatal(err)
				}
				requireAdapterWrites(t, f, 1, 0, 1)
			} else {
				if err == nil {
					t.Fatal("unverified staged bytes must not commit")
				}
				requireAdapterWrites(t, f, 0, 0, 0)
			}
		})
	}
}

func TestLedgerLeaseExpiresDuringLockWait(t *testing.T) {
	f := newAdapterFixture(t)
	blocker, err := pgx.Connect(f.ctx, f.u.String())
	if err != nil {
		t.Fatal("connect lease lock holder failed")
	}
	t.Cleanup(func() { _ = blocker.Close(context.Background()) })
	if _, err := blocker.Exec(f.ctx, `UPDATE ingest.execution_lease SET expires_at = clock_timestamp() + interval '500 milliseconds'`); err != nil {
		t.Fatal(err)
	}
	tx, err := blocker.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	if _, err := tx.Exec(f.ctx, `SELECT id FROM ingest.execution_lease FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := f.ledger.Apply(f.ctx, f.r)
		done <- err
	}()
	waitAdapterLock(t, f.ctx, blocker, f.conn.PgConn().PID())
	// Hold the unchanged row until database time passes its expiry.
	if _, err := blocker.Exec(f.ctx, `SELECT pg_sleep(GREATEST(0, EXTRACT(EPOCH FROM (expires_at - clock_timestamp()))) + 0.01) FROM ingest.execution_lease`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, run.ErrStaleGrant) {
			t.Fatalf("expiry after unchanged-row wait must be stale: %v", err)
		}
	case <-f.ctx.Done():
		t.Fatal("expired lease apply did not finish")
	}
	requireAdapterWrites(t, f, 0, 0, 0)
}

func TestLedgerConcurrentApplyReturnsOneReceipt(t *testing.T) {
	f := newAdapterFixture(t)
	conn, err := pgx.Connect(f.ctx, f.u.String())
	if err != nil {
		t.Fatal("connect competing adapter failed")
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	other, err := NewLedger(conn, "writer-a", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	type response struct {
		receipt run.IntakeReceipt
		err     error
	}
	done := make(chan response, 2)
	start := make(chan struct{})
	for _, ledger := range []*Ledger{f.ledger, other} {
		go func(l *Ledger) {
			<-start
			r, err := l.Apply(f.ctx, f.r)
			done <- response{r, err}
		}(ledger)
	}
	close(start)
	var first run.IntakeReceipt
	for i := 0; i < 2; i++ {
		select {
		case got := <-done:
			if got.err != nil {
				t.Fatal(got.err)
			}
			if i == 0 {
				first = got.receipt
			} else if !reflect.DeepEqual(first, got.receipt) {
				t.Fatal("concurrent adapters returned different receipts")
			}
		case <-f.ctx.Done():
			t.Fatal("concurrent apply did not finish")
		}
	}
	requireAdapterWrites(t, f, 1, 0, 1)
}

func TestLedgerInvisibleConflictWinner(t *testing.T) {
	f := newAdapterFixture(t)
	emptyInsert := make(chan bool, 1)
	f.ledger.db = observingStarter{Conn: f.conn, empty: emptyInsert}
	winner, err := pgx.Connect(f.ctx, f.u.String())
	if err != nil {
		t.Fatal("connect conflict winner failed")
	}
	t.Cleanup(func() { _ = winner.Close(context.Background()) })
	tx, err := winner.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	var executionID int64
	if err := tx.QueryRow(f.ctx, `SELECT id FROM ingest.execution`).Scan(&executionID); err != nil {
		t.Fatal(err)
	}
	p, err := resultParams(f.r, "writer-a", executionID)
	if err != nil {
		t.Fatal(err)
	}
	// Fixture writer deliberately does not lock the lease: two conforming Apply
	// calls serialize on it and cannot exercise the SQL statement-snapshot hole.
	// Hold the competing key uncommitted while Apply takes its insert snapshot.
	if _, err := tx.Exec(f.ctx, `INSERT INTO ingest.execution_result
		(execution_id, result_key, stage, substep, shard, idempotency_key, input_digest,
		output_digest, recipe_digest, budget_digest, decision_revision, outcome,
		expected, processed, succeeded, pending_retry, terminal_gap, rejected, failed,
		accepted_gaps, cursor, next_eligible_at, gap_acceptance_ref, complete, payload)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25)`,
		executionID, p.ResultKey, p.Stage, p.Substep, p.Shard, p.IdempotencyKey, p.InputDigest,
		p.OutputDigest, p.RecipeDigest, p.BudgetDigest, p.DecisionRevision, p.Outcome,
		p.Expected, p.Processed, p.Succeeded, p.PendingRetry, p.TerminalGap, p.Rejected, p.Failed,
		p.AcceptedGaps, p.Cursor, p.NextEligibleAt, p.GapAcceptanceRef, p.Complete, p.Payload); err != nil {
		t.Fatal(err)
	}
	// The winner commits the same outbox intent a successful Apply would create.
	if _, err := tx.Exec(f.ctx, `INSERT INTO ingest.delivery_record
		(deduplication_key, delivery_kind, recipient_workflow_id, receipt_kind, receipt_key, expected_epoch, expected_hash, due_at)
		SELECT result_key, 'completion', $1, 'result', result_key, 1, output_digest, created_at
		FROM ingest.execution_result WHERE idempotency_key = $2`, f.r.Work.Execution.WorkflowID, f.r.IdempotencyKey); err != nil {
		t.Fatal(err)
	}
	stored, err := intakedb.New(tx).FindExecutionResult(f.ctx, f.r.IdempotencyKey)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := receiptFromResult(f.ctx, tx, stored)
	if err != nil {
		t.Fatal(err)
	}
	type applyResult struct {
		receipt run.IntakeReceipt
		err     error
	}
	done := make(chan applyResult, 1)
	go func() {
		r, err := f.ledger.Apply(f.ctx, f.r)
		done <- applyResult{r, err}
	}()
	// winner can observe the other connection while holding its transaction.
	waitAdapterLock(t, f.ctx, winner, f.conn.PgConn().PID())
	if err := tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err != nil || !reflect.DeepEqual(expected, got.receipt) {
			t.Fatalf("invisible winner must resolve to its stored receipt: %v", got.err)
		}
		select {
		case empty := <-emptyInsert:
			if !empty {
				t.Fatal("fixture did not exercise the empty insert statement snapshot")
			}
		case <-f.ctx.Done():
			t.Fatal("insert statement observation is missing")
		}
	case <-f.ctx.Done():
		t.Fatal("invisible winner replay did not finish")
	}
	// A later replay on the winner connection returns the same receipt too.
	other, err := NewLedger(winner, "writer-a", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := other.Apply(f.ctx, f.r)
	if err != nil || !reflect.DeepEqual(expected, replayed) {
		t.Fatalf("both connections must resolve one stored receipt: %v", err)
	}
	requireAdapterWrites(t, f, 1, 0, 1)
}

type observingStarter struct {
	*pgx.Conn
	empty chan bool
}

func (s observingStarter) BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error) {
	tx, err := s.Conn.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return observingTransaction{Tx: tx, empty: s.empty}, nil
}

type observingTransaction struct {
	pgx.Tx
	empty chan bool
}

func (tx observingTransaction) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	row := tx.Tx.QueryRow(ctx, query, args...)
	if strings.Contains(query, "INSERT INTO ingest.execution_result") {
		return observingRow{Row: row, empty: tx.empty}
	}
	return row
}

type observingRow struct {
	pgx.Row
	empty chan bool
}

func (r observingRow) Scan(dest ...any) error {
	err := r.Row.Scan(dest...)
	r.empty <- errors.Is(err, pgx.ErrNoRows)
	return err
}

func TestLedgerBoundDecisionAndGapAcceptance(t *testing.T) {
	for _, name := range []string{"exact", "actor", "action", "fence", "epoch", "revision", "candidate", "report", "original", "output", "scope", "manifest", "expired"} {
		t.Run(name, func(t *testing.T) {
			f := newAdapterFixture(t)
			f.r = fixtureAcceptance(f.r)
			f.r.Acceptance, f.r.Outcome = model.AcceptedWithGaps, run.OutcomeTerminalGap
			f.r.Counts = run.Counts{Expected: 1, Processed: 1, TerminalGap: 1, AcceptedGaps: 1}
			f.r.GapAcceptanceRef, f.r.Work.DecisionRevision = "decision-a", 1
			f.r.IdempotencyKey = run.IntakeIdempotencyKey(f.r.Work)
			d := run.DecisionBinding{DecisionKey: "decision-a", ActorKey: "reviewer-a", Action: model.DecisionAcceptGaps,
				ExpiresAt: time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond), AttemptFence: 1, AdmissionEpoch: 1,
				DecisionRevision: 1, Scope: f.r.Work.Execution.Scope, RevisionKey: f.r.RevisionKey, CandidateDigest: fixtureDigest,
				ReportHash: fixtureDigest, OriginalHash: fixtureDigest, OutputDigest: fixtureDigest, ScopeManifest: fixtureDigest}
			f.r.Decision = &d
			f.ledger.verifier = &fixtureAcceptanceVerifier{gaps: []model.GapRef{{Kind: model.GapDocumentRevision,
				Key: fixtureRevision, ReasonCode: "FIXTURE_GAP", ReportHash: fixtureDigest}}}
			var executionID int64
			if err := f.conn.QueryRow(f.ctx, `SELECT id FROM ingest.execution`).Scan(&executionID); err != nil {
				t.Fatal(err)
			}
			args := []any{d.DecisionKey, executionID, string(d.ReportHash), d.AttemptFence, d.ActorKey, string(d.Action),
				d.RevisionKey, string(d.CandidateDigest), string(d.OriginalHash), string(d.OutputDigest), d.AdmissionEpoch,
				d.DecisionRevision, "intake", "document_version", "version-a", string(d.ScopeManifest), d.ExpiresAt}
			switch name {
			case "actor":
				args[4] = "reviewer-b"
			case "action":
				args[5] = string(model.DecisionExclude)
			case "fence":
				args[3] = int64(2)
			case "epoch":
				args[10] = int64(2)
			case "revision":
				args[11] = int64(2)
			case "candidate":
				args[7] = strings.Repeat("b", 64)
			case "report":
				args[2] = strings.Repeat("b", 64)
			case "original":
				args[8] = strings.Repeat("b", 64)
			case "output":
				args[9] = strings.Repeat("b", 64)
			case "scope":
				args[14] = "version-b"
			case "manifest":
				args[15] = strings.Repeat("b", 64)
			case "expired":
				args[16] = time.Now().Add(-time.Hour)
			}
			if _, err := f.conn.Exec(f.ctx, `INSERT INTO ingest.quarantine_decision
				(decision_key, document_version_key, execution_id, quality_report_hash, workflow_fence_token,
				actor, action, reason, revision_key, candidate_digest, original_hash, output_hash, expected_decision_epoch,
				decision_revision, scope_kind, resource_kind, scope_key, scope_manifest_digest, evidence, expires_at)
				VALUES ($1,'version-a',$2,$3,$4,$5,$6,'fixture',$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,'{}',$17)`, args...); err != nil {
				t.Fatal(err)
			}
			_, err := f.ledger.Apply(f.ctx, f.r)
			if name == "exact" {
				if err != nil {
					t.Fatal(err)
				}
				requireAdapterWrites(t, f, 1, 1, 1)
				if got := adapterCount(t, f, "evidence_gap"); got != 1 {
					t.Fatal("named gap must commit with acceptance")
				}
			} else {
				if !errors.Is(err, run.ErrStaleGrant) {
					t.Fatalf("wrong stored decision %s must return stale grant: %v", name, err)
				}
				requireAdapterWrites(t, f, 0, 0, 0)
			}
		})
	}
}
