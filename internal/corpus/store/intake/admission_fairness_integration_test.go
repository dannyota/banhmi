package intake

import (
	"context"
	"testing"
	"time"

	"danny.vn/banhmi/internal/corpus/run"
)

func admissionRelease(t *testing.T, f *admissionFixture, receipt run.AdmissionReceipt) {
	t.Helper()
	f.status = run.DocumentExecutionTerminal
	defer func() { f.status = run.DocumentExecutionUnknown }()
	closing, err := f.adapter.Reconcile(f.source.db.ctx, run.AdmissionChange{Actor: f.control, SlotKey: receipt.SlotKey, ExpectedRevision: receipt.Revision})
	if err != nil || closing.State != run.AdmissionClosing {
		t.Fatalf("close failed chain: %+v %v", closing, err)
	}
	if released, err := f.adapter.Reconcile(f.source.db.ctx, run.AdmissionChange{Actor: f.control, SlotKey: closing.SlotKey, ExpectedRevision: closing.Revision}); err != nil || released.State != run.AdmissionReleased {
		t.Fatalf("release terminal chain: %+v %v", released, err)
	}
}

func TestAdmissionLockedResumeHeadCannotBeOvertaken(t *testing.T) {
	f := newAdmissionFixture(t, true)
	ctx := f.source.db.ctx
	first, child := admissionWorkerLoss(t, f, f.reserve(t), f.request.Child)
	launch := admissionSeedLaunch(t, f, 1, time.Now().Add(-time.Second))
	second, later := admissionWorkerLoss(t, f, admissionUse(t, f, launch), launch.Child)
	blocker := sourceAppConnection(t, f.source.db)
	tx, err := blocker.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, `SELECT id FROM ingest.execution_lease WHERE resource_kind='document_version' AND resource_key=$1 FOR UPDATE`, child.Execution.Scope.Subject.LeaseKey()); err != nil {
		t.Fatal(err)
	}
	before := admissionRows(t, f)
	if result, err := f.adapter.Resume(ctx, run.AdmissionChange{Actor: later, SlotKey: second.SlotKey, ExpectedRevision: second.Revision}); err != nil || result.Refusal != run.AdmissionNotTurn {
		t.Fatal("locked resume head was skipped")
	}
	requireAdmissionUnchanged(t, f, before)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if result, err := f.adapter.Resume(ctx, run.AdmissionChange{Actor: child, SlotKey: first.SlotKey, ExpectedRevision: first.Revision}); err != nil || result.Receipt == nil {
		t.Fatal("unlocked resume head did not progress", err)
	}
}

func TestAdmissionSustainedResumeArrivalsCannotStarveLaunches(t *testing.T) {
	f := newAdmissionFixture(t, true)
	ctx := f.source.db.ctx
	base := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
	var ready []run.AdmissionChange
	// First create recoverable occupancy without demand. Demand arrives during
	// each contested round, rather than being a fixed initial resume queue.
	for i := 0; i < 8; i++ {
		r := f.request
		if i > 0 {
			r = admissionSeedLaunch(t, f, i, base.Add(time.Duration(i)*time.Microsecond))
		}
		bound, child := admissionLostBinding(t, f, admissionUse(t, f, r), r.Child)
		ready = append(ready, run.AdmissionChange{Actor: child, SlotKey: bound.SlotKey, ExpectedRevision: bound.Revision})
	}
	launch := admissionSeedLaunch(t, f, 100, base)
	for i := 0; i < 4; i++ {
		var demands []run.AdmissionChange
		for _, request := range ready[2*i : 2*i+2] {
			demand, err := f.adapter.RequestResume(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			request.ExpectedRevision = demand.Revision
			demands = append(demands, request)
		}
		before := admissionRows(t, f)
		if result, err := f.adapter.Resume(ctx, demands[0]); err != nil || result.Refusal != run.AdmissionNotTurn {
			t.Fatal("resume arrivals stole launch turn")
		}
		requireAdmissionUnchanged(t, f, before)
		admitted := admissionUse(t, f, launch)
		next := admissionSeedLaunch(t, f, 101+i, base.Add(time.Duration(i+1)*time.Microsecond))
		before = admissionRows(t, f)
		if result, err := f.adapter.Resume(ctx, demands[1]); err != nil || result.Refusal != run.AdmissionNotTurn {
			t.Fatal("later resume overtook its FIFO head")
		}
		if result, err := f.adapter.Reserve(ctx, next); err != nil || result.Refusal != run.AdmissionNotTurn {
			t.Fatal("launch overtook resume turn")
		}
		requireAdmissionUnchanged(t, f, before)
		first, err := f.adapter.Resume(ctx, demands[0])
		if err != nil || first.Receipt == nil {
			t.Fatal("earliest resume did not progress", err)
		}
		admissionRelease(t, f, admitted)
		admissionRelease(t, f, *first.Receipt)
		// Both heads remain feasible. The waiting launch must receive its turn
		// even while another resume remains continuously eligible.
		admitted = admissionUse(t, f, next)
		launch = admissionSeedLaunch(t, f, 200+i, base.Add(-time.Duration(i+1)*time.Hour))
		second, err := f.adapter.Resume(ctx, demands[1])
		if err != nil || second.Receipt == nil {
			t.Fatal("second resume did not progress", err)
		}
		admissionRelease(t, f, admitted)
		admissionRelease(t, f, *second.Receipt)
	}
}
