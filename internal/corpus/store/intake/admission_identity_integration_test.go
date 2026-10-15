package intake

import (
	"errors"
	"strings"
	"testing"
	"time"

	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/run"
)

func admissionReclaimDocument(t *testing.T, f *admissionFixture, change string) run.IntakeWork {
	t.Helper()
	ctx := f.source.db.ctx
	work := f.request.Child
	if _, err := f.source.app.Exec(ctx, `UPDATE ingest.execution_lease SET expires_at=clock_timestamp()-interval '1 second' WHERE resource_kind='document_version'`); err != nil {
		t.Fatal(err)
	}
	work.Execution.Owner = "owner-recovered"
	admission, err := f.source.ledger.Admission(ctx)
	if err != nil {
		t.Fatal(err)
	}
	work.Execution.AdmissionEpoch = admission.AdmissionEpoch
	lease, err := f.source.ledger.ReclaimLease(ctx, admission.AdmissionEpoch, work.Execution.Scope, work.Execution.Owner, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	work.Execution.AttemptFence = lease.AttemptFence
	switch change {
	case "input":
		work.InputDigest = model.Digest(strings.Repeat("b", 64))
	case "recipe":
		work.RecipeDigest = model.Digest(strings.Repeat("b", 64))
	case "budget-digest":
		work.BudgetDigest = model.Digest(strings.Repeat("b", 64))
	case "budget":
		work.Budget.MaxItems++
	}
	if _, err := f.source.ledger.RegisterExecution(ctx, work, "recovered-identity-run", nil); err != nil {
		t.Fatal(err)
	}
	return work
}

func TestAdmissionChangedRegisteredWorkCannotSettleOrResolveOldSlot(t *testing.T) {
	for _, changed := range []string{"input", "recipe", "budget-digest", "budget"} {
		t.Run(changed, func(t *testing.T) {
			f := newAdmissionFixture(t, true)
			ctx := f.source.db.ctx
			reserved := f.reserve(t)
			fresh := admissionReclaimDocument(t, f, changed)
			before := admissionRows(t, f)
			if _, err := f.adapter.Bind(ctx, run.AdmissionChange{Actor: fresh, SlotKey: reserved.SlotKey, ExpectedRevision: reserved.Revision}); !errors.Is(err, run.ErrAdmissionIntegrity) {
				t.Fatal("changed registration bound the old slot", err)
			}
			for _, selector := range []run.AdmissionResolveRequest{
				{Actor: fresh, DocumentWork: &fresh}, {Actor: fresh, WorkflowID: fresh.Execution.WorkflowID},
				{Actor: fresh, DocumentWork: &f.request.Child},
			} {
				if _, err := f.adapter.Resolve(ctx, selector); err == nil {
					t.Fatal("changed registered document inspected the old slot")
				}
			}
			requireAdmissionUnchanged(t, f, before)
			// The ledger can register and accept this changed work after reclaim.
			// Admission must still preserve the old slot's immutable inputs.
			f.request.Child = fresh
			admissionAccept(t, f, model.Accepted)
			f.status = run.DocumentExecutionTerminal
			before = admissionRows(t, f)
			if _, err := f.adapter.Settle(ctx, admissionChange(f, reserved, true)); !errors.Is(err, run.ErrAdmissionIntegrity) {
				t.Fatal("control settled changed work into the old slot", err)
			}
			requireAdmissionUnchanged(t, f, before)
			if _, err := f.adapter.Reconcile(ctx, admissionChange(f, reserved, true)); !errors.Is(err, run.ErrAdmissionIntegrity) {
				t.Fatal("control reconciled changed work into the old slot", err)
			}
			requireAdmissionUnchanged(t, f, before)
			if snapshot, err := f.adapter.Resolve(ctx, run.AdmissionResolveRequest{Actor: f.control, WorkflowID: fresh.Execution.WorkflowID}); err != nil || snapshot.Revision != reserved.Revision || snapshot.State != run.AdmissionReserved {
				t.Fatalf("control could not inspect unchanged slot: %+v %v", snapshot, err)
			}
			requireAdmissionUnchanged(t, f, before)
		})
	}
}

func TestAdmissionChangedRegisteredWorkCannotSupplyTerminalGap(t *testing.T) {
	for _, changed := range []string{"input", "recipe", "budget-digest", "budget"} {
		t.Run(changed, func(t *testing.T) {
			f := newAdmissionFixture(t, true)
			ctx := f.source.db.ctx
			reserved := f.reserve(t)
			fresh := admissionReclaimDocument(t, f, changed)
			fresh.Substep = "terminal-gap"
			r := run.IntakeApplyRequest{Work: fresh, IdempotencyKey: run.IntakeIdempotencyKey(fresh), OutputDigest: fixtureDigest,
				Outcome: run.OutcomeTerminalGap, Counts: run.Counts{Expected: 1, Processed: 1, TerminalGap: 1}}
			gap, err := f.source.ledger.Apply(ctx, r)
			if err != nil {
				t.Fatal(err)
			}
			before := admissionRows(t, f)
			if _, err := f.adapter.Close(ctx, run.AdmissionCloseRequest{Change: admissionChange(f, reserved, true), Reason: run.AdmissionTerminalGap, CauseReceipt: gap.ResultID}); !errors.Is(err, run.ErrAdmissionIntegrity) {
				t.Fatal("changed work supplied terminal-gap authority", err)
			}
			requireAdmissionUnchanged(t, f, before)
		})
	}
}

func TestAdmissionSameInputsAfterReclaimCanSettle(t *testing.T) {
	for _, rotate := range []bool{false, true} {
		t.Run(map[bool]string{false: "fence", true: "epoch"}[rotate], func(t *testing.T) {
			f := newAdmissionFixture(t, true)
			ctx := f.source.db.ctx
			reserved := f.reserve(t)
			if rotate {
				admission, err := f.source.ledger.RotateAdmission(ctx, 1, "writer-a", true)
				if err != nil {
					t.Fatal(err)
				}
				f.control.Execution.AdmissionEpoch = admission.AdmissionEpoch
				lease, err := f.source.ledger.ReclaimLease(ctx, admission.AdmissionEpoch, f.control.Execution.Scope, f.control.Execution.Owner, time.Now().Add(time.Hour))
				if err != nil {
					t.Fatal(err)
				}
				f.control.Execution.AttemptFence = lease.AttemptFence
				if _, err := f.source.ledger.RegisterExecution(ctx, f.control, "recovered-control-run", nil); err != nil {
					t.Fatal(err)
				}
			}
			fresh := admissionReclaimDocument(t, f, "same")
			before := admissionRows(t, f)
			if snapshot, err := f.adapter.Resolve(ctx, run.AdmissionResolveRequest{Actor: fresh, DocumentWork: &fresh}); err != nil || snapshot.Revision != reserved.Revision {
				t.Fatalf("same-work reclaim could not resolve before Bind: %+v %v", snapshot, err)
			}
			requireAdmissionUnchanged(t, f, before)
			f.request.Child = fresh
			admissionAccept(t, f, model.Accepted)
			if settled, err := f.adapter.Settle(ctx, admissionChange(f, reserved, true)); err != nil || settled.State != run.AdmissionClosing {
				t.Fatalf("new fence with original inputs could not settle: %+v %v", settled, err)
			}
		})
	}
}
