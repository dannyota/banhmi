package intake

import (
	"errors"
	"testing"
	"time"

	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/run"
	"danny.vn/banhmi/internal/corpus/store/intake/intakedb"
)

func admissionAcceptanceRequest(t *testing.T, work run.IntakeWork, state model.AcceptanceState) run.IntakeApplyRequest {
	t.Helper()
	revision := fixtureRevision + ":rev:" + string(fixtureDigest)
	work.Stage, work.Shard, work.Substep = run.Normalize, revision, "evidence-acceptance"
	r := run.IntakeApplyRequest{Work: work, Outputs: []model.AssetRef{}, OutputDigest: fixtureDigest, Outcome: run.OutcomeSucceeded, Acceptance: state,
		RevisionKey: revision, CandidateDigest: fixtureDigest, QualityReportHash: fixtureDigest, OriginalHash: fixtureDigest,
		Counts: run.Counts{Expected: 1, Processed: 1, Succeeded: 1}, Complete: true}
	if state == model.Quarantined {
		r.Work.Substep, r.Outcome, r.Complete = "evidence-quarantine", run.OutcomeRejected, false
		r.Counts.Succeeded, r.Counts.Rejected = 0, 1
	}
	if state == model.Candidate {
		r.Work.Substep, r.Complete = "evidence-candidate", false
	}
	r.IdempotencyKey = run.IntakeIdempotencyKey(r.Work)
	if err := acceptanceShape(r); err != nil {
		t.Fatal("invalid synthetic acceptance request", err)
	}
	return r
}

// Synthetic evidence verification is deliberately injected into the existing
// ledger. These tests prove lifecycle accounting, not real-document quality.
func admissionAccept(t *testing.T, f *admissionFixture, state model.AcceptanceState) run.IntakeReceipt {
	t.Helper()
	r := admissionAcceptanceRequest(t, f.request.Child, state)
	f.source.ledger.verifier = &fixtureAcceptanceVerifier{}
	receipt, err := f.source.ledger.Apply(f.source.db.ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func TestAdmissionIdentityOnlyAcceptanceCannotSettle(t *testing.T) {
	f := newAdmissionFixture(t, true)
	reserved := f.reserve(t)
	r := admissionAcceptanceRequest(t, f.request.Child, model.Quarantined)
	r.RevisionKey, r.Work.Shard = fixtureRevision, fixtureRevision
	r.IdempotencyKey = run.IntakeIdempotencyKey(r.Work)
	f.source.ledger.verifier = &fixtureAcceptanceVerifier{}
	if _, err := f.source.ledger.Apply(f.source.db.ctx, r); err != nil {
		t.Fatal(err)
	}
	before := admissionRows(t, f)
	if _, err := f.adapter.Settle(f.source.db.ctx, admissionChange(f, reserved, false)); !errors.Is(err, run.ErrAdmissionIntegrity) {
		t.Fatal("identity-only acceptance settled", err)
	}
	requireAdmissionUnchanged(t, f, before)
}

func TestAdmissionSettlementRequiresCommittedEventAndRetainsOpen(t *testing.T) {
	f := newAdmissionFixture(t, true)
	ctx := f.source.db.ctx
	reserved := f.reserve(t)
	before := admissionRows(t, f)
	if _, err := f.adapter.Settle(ctx, admissionChange(f, reserved, false)); !errors.Is(err, run.ErrAdmissionUnchanged) {
		t.Fatal("uncommitted quarantine settled")
	}
	requireAdmissionUnchanged(t, f, before)
	admissionAccept(t, f, model.Candidate)
	before = admissionRows(t, f)
	if _, err := f.adapter.Settle(ctx, admissionChange(f, reserved, false)); !errors.Is(err, run.ErrAdmissionUnchanged) {
		t.Fatal("candidate recording changed slot accounting")
	}
	requireAdmissionUnchanged(t, f, before)
	admissionAccept(t, f, model.Quarantined)
	quarantine, err := f.adapter.Settle(ctx, admissionChange(f, reserved, false))
	if err != nil || quarantine.State != run.AdmissionOpenOnly {
		t.Fatalf("quarantine settlement: %+v %v", quarantine, err)
	}
	counts, err := intakedb.New(f.source.app).CountDocumentAdmissionSlots(ctx)
	if err != nil || counts.Processing != 0 || counts.Open != 1 {
		t.Fatal("quarantine did not retain exactly open capacity")
	}
	admissionAccept(t, f, model.Accepted)
	terminal, err := f.adapter.Settle(ctx, admissionChange(f, quarantine, false))
	if err != nil || terminal.State != run.AdmissionClosing {
		t.Fatalf("terminal acceptance: %+v %v", terminal, err)
	}
	before = admissionRows(t, f)
	stale, err := f.adapter.Settle(ctx, admissionChange(f, reserved, false))
	if err != nil || stale != quarantine {
		t.Fatal("old quarantine receipt was not retained as history")
	}
	if _, err := f.adapter.RequestResume(ctx, admissionChange(f, terminal, false)); !errors.Is(err, run.ErrAdmissionConflict) {
		t.Fatal("terminal acceptance resumed")
	}
	resolved, err := f.adapter.Resolve(ctx, run.AdmissionResolveRequest{Actor: f.control, WorkflowID: f.request.Child.Execution.WorkflowID})
	if err != nil || resolved.State != run.AdmissionClosing || resolved.TerminalResultID == 0 || resolved.SettledWatermark == 0 {
		t.Fatal("stale quarantine undid terminal acceptance")
	}
	requireAdmissionUnchanged(t, f, before)
	counts, err = intakedb.New(f.source.app).CountDocumentAdmissionSlots(ctx)
	if err != nil || counts.Processing != 0 || counts.Open != 1 {
		t.Fatal("terminal evidence released open before closure")
	}
}

func TestAdmissionRequestResumeDemandPreservesAge(t *testing.T) {
	f := newAdmissionFixture(t, true)
	ctx := f.source.db.ctx
	reserved := f.reserve(t)
	admissionAccept(t, f, model.Quarantined)
	quarantine, err := f.adapter.Settle(ctx, admissionChange(f, reserved, false))
	if err != nil {
		t.Fatal(err)
	}
	demand, err := f.adapter.RequestResume(ctx, admissionChange(f, quarantine, false))
	if err != nil {
		t.Fatal(err)
	}
	first := admissionCurrentSlot(t, f, demand)
	if first.ResumeRequestedAt == nil {
		t.Fatal("resume demand not durable")
	}
	before := admissionRows(t, f)
	replay, err := f.adapter.RequestResume(ctx, admissionChange(f, demand, false))
	if err != nil || replay != demand {
		t.Fatal("repeat demand did not reuse its effect receipt")
	}
	requireAdmissionUnchanged(t, f, before)
	resumed, err := f.adapter.Resume(ctx, admissionChange(f, demand, false))
	if err != nil || resumed.Receipt == nil || resumed.Receipt.State != run.AdmissionProcessing {
		t.Fatalf("resume: %+v %v", resumed, err)
	}
	before = admissionRows(t, f)
	if _, err := f.adapter.Settle(ctx, admissionChange(f, *resumed.Receipt, false)); err != nil {
		t.Fatal("settled event effect receipt unavailable")
	}
	requireAdmissionUnchanged(t, f, before)
	if s := admissionCurrentSlot(t, f, *resumed.Receipt); s.State != "processing" || s.ResumeRequestedAt != nil || s.SettledWatermark != first.SettledWatermark {
		t.Fatal("old quarantine settled twice or demand survived Resume")
	}
}

func TestAdmissionExpiryRetainsUnresolvedEvidence(t *testing.T) {
	f := newAdmissionFixture(t, false)
	ctx := f.source.db.ctx
	limits := run.DefaultDocumentAdmissionLimits()
	limits.QuarantineExpiry = time.Microsecond
	if _, err := f.adapter.Configure(ctx, run.AdmissionConfigureRequest{Actor: f.control, Limits: limits}); err != nil {
		t.Fatal(err)
	}
	reserved := f.reserve(t)
	event := admissionAccept(t, f, model.Quarantined)
	quarantine, err := f.adapter.Settle(ctx, admissionChange(f, reserved, false))
	if err != nil {
		t.Fatal(err)
	}
	before := admissionRows(t, f)
	if _, err := f.adapter.RequestResume(ctx, admissionChange(f, quarantine, false)); !errors.Is(err, run.ErrAdmissionConflict) {
		t.Fatal("expired quarantine queued demand")
	}
	requireAdmissionUnchanged(t, f, before)
	expired, err := f.adapter.Close(ctx, run.AdmissionCloseRequest{Change: admissionChange(f, quarantine, true), Reason: run.AdmissionExpired, CauseReceipt: event.ResultID})
	if err != nil || expired.State != run.AdmissionClosing {
		t.Fatalf("expiry: %+v %v", expired, err)
	}
	var gaps, accepted int64
	if err := f.source.app.QueryRow(ctx, `SELECT (SELECT count(*) FROM ingest.evidence_gap),
(SELECT count(*) FROM ingest.revision_acceptance WHERE state IN ('accepted','accepted_with_gaps','excluded'))`).Scan(&gaps, &accepted); err != nil || gaps != 0 || accepted != 0 {
		t.Fatal("expiry manufactured accepted evidence or approved gaps")
	}
}

func TestAdmissionReconcileSettlesApplyThatWonFirst(t *testing.T) {
	f := newAdmissionFixture(t, true)
	ctx := f.source.db.ctx
	reserved := f.reserve(t)
	admissionAccept(t, f, model.Accepted)
	f.status = run.DocumentExecutionTerminal
	closing, err := f.adapter.Reconcile(ctx, admissionChange(f, reserved, true))
	if err != nil {
		t.Fatal(err)
	}
	slot := admissionCurrentSlot(t, f, closing)
	if slot.TerminalReason == nil || *slot.TerminalReason != "completed" || slot.SettledWatermark == 0 {
		t.Fatal("failure observation overrode committed acceptance")
	}
}
