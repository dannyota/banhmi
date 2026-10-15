package intake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"danny.vn/banhmi/internal/corpus/run"
	"danny.vn/banhmi/internal/corpus/store/intake/intakedb"
)

// Clone only synthetic, already accepted fixture rows. This isolates admission
// transactions from source fetching and gives every parent a different lease.
func admissionClone(t *testing.T, f *admissionFixture, table string, original int64, changes map[string]any) int64 {
	t.Helper()
	qualified := pgx.Identifier{"ingest", table}.Sanitize()
	var id int64
	statement := "INSERT INTO " + qualified + " SELECT (jsonb_populate_record(NULL::" + qualified +
		",to_jsonb(r)||$1::jsonb||jsonb_build_object('id',nextval(pg_get_serial_sequence('ingest." + table + "','id'))))).* FROM " + qualified + " r WHERE r.id=$2 RETURNING id"
	if err := f.source.app.QueryRow(f.source.db.ctx, statement, admissionJSON(t, changes), original).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func admissionSeedLaunch(t *testing.T, f *admissionFixture, ordinal int, due time.Time) run.AdmissionReserveRequest {
	t.Helper()
	ctx := f.source.db.ctx
	child, parent := f.request.Child, f.request.Actor
	version, window := fmt.Sprintf("version-%04d", ordinal), fmt.Sprintf("window-%04d", ordinal)
	child.Execution.Scope.Subject.DocumentVersion = run.DocumentVersionKey(version)
	child.Execution.WorkflowID = "document-ingest/vn/" + version + "/attempt-a"
	parent.Execution.Scope.Subject.SourceWindow = run.SourceWindowKey(window)
	parent.Execution.WorkflowID = "source-sync/vn/source-a/" + window
	var childReceipt string
	var parentID int64
	for i, work := range []*run.IntakeWork{&child, &parent} {
		lease, err := f.source.ledger.ClaimLease(ctx, 1, work.Execution.Scope, work.Execution.Owner, time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		work.Execution.AttemptFence = lease.AttemptFence
		e, err := f.source.ledger.RegisterExecution(ctx, *work, "synthetic-admission-run", nil)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			childReceipt = e.ExecutionKey
		} else {
			parentID = e.ID
		}
	}
	var originalWindow, originalResult, originalPage int64
	var payload json.RawMessage
	if err := f.source.app.QueryRow(ctx, `SELECT w.id,r.id,p.id,r.payload FROM ingest.source_child_binding b
JOIN ingest.execution_result r ON r.id=b.acceptance_result_id
JOIN ingest.discovery_page p ON p.acceptance_result_id=r.id
JOIN ingest.source_window w ON w.id=p.source_window_id WHERE b.workflow_id=$1`, f.request.Child.Execution.WorkflowID).
		Scan(&originalWindow, &originalResult, &originalPage, &payload); err != nil {
		t.Fatal(err)
	}
	windowID := admissionClone(t, f, "source_window", originalWindow, map[string]any{"window_key": window})
	var retained sourcePayload
	if err := json.Unmarshal(payload, &retained); err != nil {
		t.Fatal(err)
	}
	var acceptance DiscoveryAcceptance
	if err := json.Unmarshal(retained.SourcePayload, &acceptance); err != nil {
		t.Fatal(err)
	}
	// Keep generated launches outside the initial fixture's 0x400 key.
	delivery := fmt.Sprintf("request-%032x", ordinal+0x1000)
	acceptance.Page.PageKey = fmt.Sprintf("page-%032x", ordinal+1000)
	acceptance.Page.Cursor.WindowKey = window
	acceptance.Page.Observations[0].VersionKey, acceptance.Page.Observations[0].ExternalID = version, "native-"+version
	acceptance.Page.FollowUps[0].RequestKey, acceptance.Page.FollowUps[0].WindowKey = delivery, window
	acceptance.Launches = []SourceLaunch{{RequestKey: delivery, Work: child, DueAt: due}}
	retained.Work = sourceWork(parent, "accept", acceptance.Page.PageKey)
	retained.IdempotencyKey = run.IntakeIdempotencyKey(retained.Work)
	retained.SourcePayload = admissionJSON(t, acceptance)
	resultID := admissionClone(t, f, "execution_result", originalResult, map[string]any{
		"result_key": fmt.Sprintf("result-%032x", ordinal+1000), "idempotency_key": retained.IdempotencyKey,
		"execution_id": parentID, "shard": acceptance.Page.PageKey, "payload": retained})
	admissionClone(t, f, "discovery_page", originalPage, map[string]any{
		"source_window_id": windowID, "page_key": acceptance.Page.PageKey, "acceptance_result_id": resultID})
	if _, err := f.source.app.Exec(ctx, `INSERT INTO ingest.source_child_binding(workflow_id,source_id,external_id,acceptance_result_id)
VALUES ($1,'source-a',$2,$3)`, child.Execution.WorkflowID, "native-"+version, resultID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.source.ledger.RecordDelivery(ctx, child, intakedb.InsertDeliveryRecordParams{DeduplicationKey: delivery,
		DeliveryKind: "launch", RecipientWorkflowID: child.Execution.WorkflowID, ReceiptKind: "execution", ReceiptKey: childReceipt,
		ExpectedEpoch: 1, ExpectedHash: string(child.InputDigest), DueAt: due}); err != nil {
		t.Fatal(err)
	}
	return run.AdmissionReserveRequest{Actor: parent, Child: child, DeliveryKey: delivery}
}

func admissionUse(t *testing.T, f *admissionFixture, request run.AdmissionReserveRequest) run.AdmissionReceipt {
	t.Helper()
	result, err := f.adapter.Reserve(f.source.db.ctx, request)
	if err != nil || result.Receipt == nil {
		t.Fatalf("reserve %s: refusal=%s error=%v", request.Child.Execution.WorkflowID, result.Refusal, err)
	}
	return *result.Receipt
}

func admissionWorkerLoss(t *testing.T, f *admissionFixture, slot run.AdmissionReceipt, child run.IntakeWork) (run.AdmissionReceipt, run.IntakeWork) {
	t.Helper()
	bound, child := admissionLostBinding(t, f, slot, child)
	demand, err := f.adapter.RequestResume(f.source.db.ctx, run.AdmissionChange{Actor: child, SlotKey: bound.SlotKey, ExpectedRevision: bound.Revision})
	if err != nil {
		t.Fatal(err)
	}
	return demand, child
}

func admissionLostBinding(t *testing.T, f *admissionFixture, slot run.AdmissionReceipt, child run.IntakeWork) (run.AdmissionReceipt, run.IntakeWork) {
	t.Helper()
	ctx := f.source.db.ctx
	if _, err := f.source.app.Exec(ctx, `UPDATE ingest.execution_lease SET expires_at=clock_timestamp()-interval '1 second'
WHERE resource_kind='document_version' AND resource_key=$1`, child.Execution.Scope.Subject.LeaseKey()); err != nil {
		t.Fatal(err)
	}
	lost, err := f.adapter.Reconcile(ctx, run.AdmissionChange{Actor: f.control, SlotKey: slot.SlotKey, ExpectedRevision: slot.Revision})
	if err != nil || lost.State != run.AdmissionOpenOnly {
		t.Fatalf("worker loss: %+v %v", lost, err)
	}
	child.Execution.Owner = "owner-recovered"
	lease, err := f.source.ledger.ReclaimLease(ctx, 1, child.Execution.Scope, child.Execution.Owner, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	child.Execution.AttemptFence = lease.AttemptFence
	if _, err := f.source.ledger.RegisterExecution(ctx, child, "recovered-admission-run", nil); err != nil {
		t.Fatal(err)
	}
	bound, err := f.adapter.Bind(ctx, run.AdmissionChange{Actor: child, SlotKey: lost.SlotKey, ExpectedRevision: lost.Revision})
	if err != nil || bound.State != run.AdmissionOpenOnly {
		t.Fatalf("rebind granted processing: %+v %v", bound, err)
	}
	return bound, child
}

func TestAdmissionIndependentCeilingsAndFIFO(t *testing.T) {
	for _, ceiling := range []int32{1, 32} {
		t.Run(fmt.Sprint(ceiling), func(t *testing.T) {
			f := newAdmissionFixture(t, false)
			ctx := f.source.db.ctx
			limits := run.DefaultDocumentAdmissionLimits()
			limits.Processing, limits.Open = ceiling, max(ceiling, 2)
			if _, err := f.adapter.Configure(ctx, run.AdmissionConfigureRequest{Actor: f.control, Limits: limits}); err != nil {
				t.Fatal(err)
			}
			f.reserve(t)
			base := time.Now().Add(-time.Second).UTC().Truncate(time.Microsecond)
			var last run.AdmissionReserveRequest
			for i := 1; i <= int(ceiling); i++ {
				last = admissionSeedLaunch(t, f, i, base.Add(time.Duration(i)*time.Microsecond))
				if i < int(ceiling) {
					admissionUse(t, f, last)
				}
			}
			before := admissionRows(t, f)
			result, err := f.adapter.Reserve(ctx, last)
			want := run.AdmissionProcessingFull
			if ceiling == limits.Open {
				want = run.AdmissionBothFull
			}
			if err != nil || result.Refusal != want || result.Receipt != nil || result.Processing != int64(ceiling) {
				t.Fatalf("N+1: %+v %v", result, err)
			}
			requireAdmissionUnchanged(t, f, before)
		})
	}
}

func TestAdmissionSaturatedWorkerLossRecoversWithoutOpenIncrease(t *testing.T) {
	f := newAdmissionFixture(t, true)
	// This 256-slot case runs over 3,000 sequential ledger transactions. Give
	// the whole case one fixed budget beyond the ordinary 20-second fixture.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(f.source.db.ctx), 5*time.Minute)
	t.Cleanup(cancel)
	f.source.db.ctx = ctx
	var demands []run.AdmissionChange
	requests := []run.AdmissionReserveRequest{f.request}
	base := time.Now().Add(-time.Second).UTC().Truncate(time.Microsecond)
	for i := 1; i < 256; i++ {
		requests = append(requests, admissionSeedLaunch(t, f, i, base.Add(time.Duration(i)*time.Microsecond)))
	}
	for _, request := range requests {
		slot := admissionUse(t, f, request)
		demand, child := admissionLostBinding(t, f, slot, request.Child)
		demands = append(demands, run.AdmissionChange{Actor: child, SlotKey: demand.SlotKey, ExpectedRevision: demand.Revision})
	}
	for i, change := range demands {
		demand, err := f.adapter.RequestResume(ctx, change)
		if err != nil {
			t.Fatal(err)
		}
		demands[i].ExpectedRevision = demand.Revision
	}
	blocked := admissionSeedLaunch(t, f, 257, base.Add(-time.Hour))
	counts, err := intakedb.New(f.source.app).CountDocumentAdmissionSlots(ctx)
	if err != nil || counts.Open != 256 || counts.Processing != 0 {
		t.Fatal("saturation fixture differs from 256 open worker-loss slots")
	}
	before := admissionRows(t, f)
	result, err := f.adapter.Reserve(ctx, blocked)
	if err != nil || result.Refusal != run.AdmissionOpenFull {
		t.Fatal("older launch should be blocked only by open capacity")
	}
	requireAdmissionUnchanged(t, f, before)
	// Demand age survives process reconstruction and a no-op configuration.
	restarted, err := NewDocumentAdmission(f.source.app, "writer-a", f.adapter.observer)
	if err != nil {
		t.Fatal(err)
	}
	f.adapter = restarted
	if _, err := f.adapter.Configure(ctx, run.AdmissionConfigureRequest{Actor: f.control, ExpectedRevision: 1, Limits: run.DefaultDocumentAdmissionLimits()}); err != nil {
		t.Fatal(err)
	}
	var resumed []run.AdmissionReceipt
	for i, demand := range demands[:32] {
		if i == 0 {
			before = admissionRows(t, f)
			if later, err := f.adapter.Resume(ctx, demands[1]); err != nil || later.Refusal != run.AdmissionNotTurn {
				t.Fatal("resume FIFO was overtaken")
			}
			requireAdmissionUnchanged(t, f, before)
		}
		result, err := f.adapter.Resume(ctx, demand)
		if err != nil || result.Receipt == nil || result.Processing != int64(i+1) || result.Open != 256 {
			t.Fatalf("resume %d: %+v %v", i, result, err)
		}
		resumed = append(resumed, *result.Receipt)
	}
	before = admissionRows(t, f)
	result, err = f.adapter.Resume(ctx, demands[32])
	if err != nil || result.Refusal != run.AdmissionProcessingFull {
		t.Fatal("resume exceeded processing ceiling")
	}
	requireAdmissionUnchanged(t, f, before)
	// Confirmed completion opens both classes. The older launch becomes
	// feasible and competes under the turn preserved during saturation.
	admissionRelease(t, f, resumed[0])
	before = admissionRows(t, f)
	if result, err := f.adapter.Resume(ctx, demands[32]); err != nil || result.Refusal != run.AdmissionNotTurn {
		t.Fatal("resume bypassed the saved launch turn after release")
	}
	requireAdmissionUnchanged(t, f, before)
	admissionUse(t, f, blocked)
	admissionRelease(t, f, resumed[1])
	if result, err := f.adapter.Resume(ctx, demands[32]); err != nil || result.Receipt == nil || result.Processing != 32 || result.Open != 255 {
		t.Fatalf("recovery lost its turn after launch competition: %+v %v", result, err)
	}
}

func TestAdmissionLockedLaunchHeadCannotBeOvertaken(t *testing.T) {
	f := newAdmissionFixture(t, true)
	ctx := f.source.db.ctx
	head := f.request
	later := admissionSeedLaunch(t, f, 1, time.Now().Add(-time.Second))
	blocker := sourceAppConnection(t, f.source.db)
	tx, err := blocker.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, `SELECT id FROM ingest.execution_lease WHERE resource_kind='document_version' AND resource_key=$1 FOR UPDATE`, head.Child.Execution.Scope.Subject.LeaseKey()); err != nil {
		t.Fatal(err)
	}
	before := admissionRows(t, f)
	result, err := f.adapter.Reserve(ctx, later)
	if err != nil || result.Refusal != run.AdmissionNotTurn {
		t.Fatal("locked head was skipped")
	}
	requireAdmissionUnchanged(t, f, before)
}

func TestAdmissionAlternatesContestedQueues(t *testing.T) {
	f := newAdmissionFixture(t, true)
	ctx := f.source.db.ctx
	first := f.reserve(t)
	demand, child := admissionWorkerLoss(t, f, first, f.request.Child)
	base := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
	launch := admissionSeedLaunch(t, f, 1, base)
	for i := 1; i <= 6; i++ {
		before := admissionRows(t, f)
		if refused, err := f.adapter.Resume(ctx, run.AdmissionChange{Actor: child, SlotKey: demand.SlotKey, ExpectedRevision: demand.Revision}); err != nil || refused.Refusal != run.AdmissionNotTurn {
			t.Fatal("launch turn was not preserved")
		}
		requireAdmissionUnchanged(t, f, before)
		admitted := admissionUse(t, f, launch)
		before = admissionRows(t, f)
		if replay, err := f.adapter.Reserve(ctx, launch); err != nil || replay.Receipt == nil || *replay.Receipt != admitted {
			t.Fatal("launch replay changed its receipt")
		}
		requireAdmissionUnchanged(t, f, before)
		// Backdated arrivals cannot consume the resume turn.
		nextLaunch := admissionSeedLaunch(t, f, i+100, base.Add(-24*time.Hour))
		before = admissionRows(t, f)
		if refused, err := f.adapter.Reserve(ctx, nextLaunch); err != nil || refused.Refusal != run.AdmissionNotTurn {
			t.Fatal("backdated launch starved a waiting resume")
		}
		requireAdmissionUnchanged(t, f, before)
		resumed, err := f.adapter.Resume(ctx, run.AdmissionChange{Actor: child, SlotKey: demand.SlotKey, ExpectedRevision: demand.Revision})
		if err != nil || resumed.Receipt == nil {
			t.Fatal(err)
		}
		// Release the unrelated launch before the next contested admission.
		admissionRelease(t, f, admitted)
		demand, child = admissionWorkerLoss(t, f, *resumed.Receipt, child)
		launch = nextLaunch
	}
}

func TestAdmissionCapacityWaitUsesFreshSnapshot(t *testing.T) {
	f := newAdmissionFixture(t, false)
	ctx := f.source.db.ctx
	limits := run.DefaultDocumentAdmissionLimits()
	limits.Processing, limits.Open = 1, 1
	if _, err := f.adapter.Configure(ctx, run.AdmissionConfigureRequest{Actor: f.control, Limits: limits}); err != nil {
		t.Fatal(err)
	}
	other := admissionSeedLaunch(t, f, 1, time.Now().Add(-time.Second))
	blocker := sourceAppConnection(t, f.source.db)
	tx, err := blocker.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, `SELECT id FROM ingest.document_admission_limit WHERE id=1 FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	conn := sourceAppConnection(t, f.source.db)
	adapter, err := NewDocumentAdmission(conn, "writer-a", f.adapter.observer)
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		result run.AdmissionResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := adapter.Reserve(ctx, other)
		done <- outcome{result, err}
	}()
	waitAdapterBlockedBy(t, ctx, f.source.db.conn, conn.PgConn().PID(), blocker.PgConn().PID())
	// A committed row inserted by the capacity owner must be visible in a new
	// statement after the waiter acquires capacity, despite its earlier arrival.
	var execution, delivery int64
	if err := tx.QueryRow(ctx, `SELECT e.id,d.id FROM ingest.execution e JOIN ingest.delivery_record d ON d.receipt_key=e.execution_key WHERE d.deduplication_key=$1`, f.request.DeliveryKey).Scan(&execution, &delivery); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ingest.document_admission_slot(slot_key,delivery_id,workflow_id,document_version_key,processing_attempt_key,
origin_execution_id,current_execution_id,admission_epoch,lease_owner,attempt_fence,revision,quarantine_expiry_us,state)
VALUES ('slot-00000000000000000000000000000001',$1,$2,'version-a','attempt-a',$3,$3,1,'owner-a',1,1,604800000000,'reserved')`, delivery, f.request.Child.Execution.WorkflowID, execution); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err != nil || got.result.Refusal != run.AdmissionBothFull || got.result.Processing != 1 || got.result.Open != 1 {
			t.Fatalf("stale pre-wait count: %+v %v", got.result, got.err)
		}
	case <-ctx.Done():
		t.Fatal("capacity waiter did not finish")
	}
	before := admissionRows(t, f)
	if _, err := adapter.Reserve(ctx, other); err != nil && !errors.Is(err, run.ErrAdmissionConflict) {
		t.Fatal(err)
	}
	requireAdmissionUnchanged(t, f, before)
}
