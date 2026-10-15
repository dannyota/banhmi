package temporal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/run"
)

func TestIntakeV2StopAndCompletionSerialization(t *testing.T) {
	ref := IntakeStopReferenceV2{Key: "admission-" + strings.Repeat("a", 32), Manifest: intakeTestStateV2().Work.Manifest}
	if _, err := json.Marshal(ref); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"result-", "slot-", "admission-A", "admission-"} {
		bad := ref
		bad.Key = prefix + strings.Repeat("a", 31)
		if _, err := json.Marshal(bad); err == nil {
			t.Fatal("invalid stop reference serialized")
		}
	}
	for _, outcome := range []string{"stopped", "cancelled", "idle"} {
		b, err := json.Marshal(IntakeCompletionV2{Outcome: outcome})
		if err != nil || strings.Contains(string(b), "result-") || strings.Contains(string(b), "Receipt") {
			t.Fatalf("invalid lifecycle completion %s: %v", b, err)
		}
	}
	if _, err := json.Marshal(IntakeCompletionV2{Outcome: "accepted"}); err == nil {
		t.Fatal("acceptance without genuine receipt serialized")
	}
}

type checkpointFakeV2 struct {
	current  IntakeLocalCheckpointV2
	ref      run.IntakeWorkEnvelope
	retained []IntakeLocalCheckpointV2
	err      error
	retain   func(IntakeLocalCheckpointV2) error
}

func (f *checkpointFakeV2) Resolve(context.Context, run.IntakeWorkEnvelope) (IntakeLocalCheckpointV2, error) {
	return f.current, f.err
}

func (f *checkpointFakeV2) Retain(_ context.Context, cp IntakeLocalCheckpointV2) (run.IntakeWorkEnvelope, error) {
	if f.retain != nil {
		if err := f.retain(cp); err != nil {
			return run.IntakeWorkEnvelope{}, err
		}
	}
	if f.err != nil {
		return run.IntakeWorkEnvelope{}, f.err
	}
	f.current = cp
	f.retained = append(f.retained, cp)
	return f.ref, nil
}

type admissionFakeV2 struct {
	r             run.AdmissionResolution
	calls         []string
	changes       []run.AdmissionChange
	reserve       func(run.AdmissionReserveRequest) (run.AdmissionResult, error)
	resume        func(run.AdmissionChange) (run.AdmissionResult, error)
	requestResume func(run.AdmissionChange) (run.AdmissionReceipt, error)
	resolve       func(run.AdmissionResolveRequest) (run.AdmissionResolution, error)
	mutation      func(string, run.AdmissionChange) error
	close         func(run.AdmissionCloseRequest) error
	refusal       run.AdmissionRefusal
	open          int64
	processing    int64
	items         []run.AdmissionItem
	scans         []run.AdmissionScan
}

func (f *admissionFakeV2) receipt() run.AdmissionReceipt {
	return run.AdmissionReceipt{Key: "admission-" + strings.Repeat("a", 32), SlotKey: f.r.SlotKey, Revision: f.r.Revision, State: f.r.State}
}

func (f *admissionFakeV2) Configure(context.Context, run.AdmissionConfigureRequest) (run.AdmissionReceipt, error) {
	f.calls = append(f.calls, "configure")
	return f.receipt(), nil
}

func (f *admissionFakeV2) Reserve(_ context.Context, r run.AdmissionReserveRequest) (run.AdmissionResult, error) {
	f.calls = append(f.calls, "reserve")
	if f.reserve != nil {
		return f.reserve(r)
	}
	if f.open >= 256 {
		return run.AdmissionResult{Refusal: run.AdmissionOpenFull, Open: f.open, Processing: f.processing}, nil
	}
	receipt := f.receipt()
	receipt.State = run.AdmissionReserved
	return run.AdmissionResult{Receipt: &receipt}, nil
}

func (f *admissionFakeV2) mutate(op string, c run.AdmissionChange) (run.AdmissionReceipt, error) {
	f.calls = append(f.calls, op)
	f.changes = append(f.changes, c)
	if f.mutation != nil {
		if err := f.mutation(op, c); err != nil {
			return run.AdmissionReceipt{}, err
		}
	}
	f.r.Revision++
	switch op {
	case "bind", "resume":
		f.r.State = run.AdmissionProcessing
		f.r.Binding = run.AdmissionBinding{ExecutionID: f.r.Binding.ExecutionID, Epoch: c.Actor.Execution.AdmissionEpoch, Fence: c.Actor.Execution.AttemptFence, Owner: c.Actor.Execution.Owner}
	}
	return f.receipt(), nil
}

func (f *admissionFakeV2) Bind(_ context.Context, c run.AdmissionChange) (run.AdmissionReceipt, error) {
	return f.mutate("bind", c)
}

func (f *admissionFakeV2) Settle(_ context.Context, c run.AdmissionChange) (run.AdmissionReceipt, error) {
	return f.mutate("settle", c)
}

func (f *admissionFakeV2) RequestResume(_ context.Context, c run.AdmissionChange) (run.AdmissionReceipt, error) {
	if f.requestResume != nil {
		f.calls = append(f.calls, "request_resume")
		f.changes = append(f.changes, c)
		return f.requestResume(c)
	}
	return f.mutate("request_resume", c)
}

func (f *admissionFakeV2) Resume(_ context.Context, c run.AdmissionChange) (run.AdmissionResult, error) {
	if f.resume != nil {
		f.calls = append(f.calls, "resume")
		f.changes = append(f.changes, c)
		return f.resume(c)
	}
	if f.refusal != "" {
		f.calls = append(f.calls, "resume")
		return run.AdmissionResult{Refusal: f.refusal}, nil
	}
	if f.processing >= 32 {
		return run.AdmissionResult{Refusal: run.AdmissionProcessingFull, Open: f.open, Processing: f.processing}, nil
	}
	f.processing++
	r, err := f.mutate("resume", c)
	return run.AdmissionResult{Receipt: &r}, err
}

func (f *admissionFakeV2) Close(_ context.Context, c run.AdmissionCloseRequest) (run.AdmissionReceipt, error) {
	f.calls = append(f.calls, "close")
	f.changes = append(f.changes, c.Change)
	if f.close != nil {
		if err := f.close(c); err != nil {
			return run.AdmissionReceipt{}, err
		}
	}
	f.r.Revision++
	f.r.State, f.r.TerminalReason = run.AdmissionClosing, c.Reason
	f.r.CloseReceipt = "admission-" + strings.Repeat("a", 32)
	return f.receipt(), nil
}

func (f *admissionFakeV2) Reconcile(_ context.Context, c run.AdmissionChange) (run.AdmissionReceipt, error) {
	return f.mutate("reconcile", c)
}

func (f *admissionFakeV2) Resolve(_ context.Context, r run.AdmissionResolveRequest) (run.AdmissionResolution, error) {
	f.calls = append(f.calls, "resolve")
	if f.resolve != nil {
		return f.resolve(r)
	}
	return f.r, nil
}

func TestIntakeV2ResumeRecoveryRetainsNormalizeBeforePendingClear(t *testing.T) {
	for _, failure := range []string{"resume_response", "post_resume_resolve", "phase_retention"} {
		t.Run(failure, func(t *testing.T) {
			s, cp, f, a := localFixtureV2()
			quarantineCheckpointV2(t, cp)
			cp.current.Phase = "quarantine"
			budget := cp.current.Work.Budget
			f.r.State, f.r.WaitReason, f.r.LifecycleReceipt = run.AdmissionOpenOnly, "quarantine", "admission-"+strings.Repeat("b", 32)
			decision := &IntakeDecisionV2{DecisionRef: *intakeTestFrameV2(s, "done", "").Receipt, ExpectedEpoch: 1, ReportHash: s.Work.Manifest}
			a.ResumeValidation = resumeFakeV2(func(run.IntakeWork, run.AdmissionResolution, *IntakeDecisionV2) error { return nil })
			committed, failed, calls := false, false, 0
			var resumeRevision int64
			var receipt run.AdmissionReceipt
			f.resume = func(change run.AdmissionChange) (run.AdmissionResult, error) {
				calls++
				if resumeRevision == 0 {
					resumeRevision = change.ExpectedRevision
				} else if change.ExpectedRevision != resumeRevision {
					t.Fatal("Resume replay changed immutable operation revision")
				}
				if !committed {
					committed = true
					f.r.State, f.r.Revision = run.AdmissionProcessing, f.r.Revision+1
					receipt = f.receipt()
				}
				if failure == "resume_response" && !failed {
					failed = true
					return run.AdmissionResult{}, &TransientError{Cause: errors.New("lost resume response")}
				}
				return run.AdmissionResult{Receipt: &receipt}, nil
			}
			f.resolve = func(run.AdmissionResolveRequest) (run.AdmissionResolution, error) {
				if failure == "post_resume_resolve" && committed && !failed {
					failed = true
					return run.AdmissionResolution{}, &TransientError{Cause: errors.New("post-resume resolve failed")}
				}
				return f.r, nil
			}
			cp.retain = func(next IntakeLocalCheckpointV2) error {
				if failure == "phase_retention" && committed && next.Pending == nil && next.Phase == "normalize" && !failed {
					failed = true
					return &TransientError{Cause: errors.New("post-resume checkpoint failed")}
				}
				return nil
			}
			cmd := IntakeCommandV2{State: s, Operation: "decision", Decision: decision}
			if _, err := a.advance(context.Background(), cmd); err == nil || !failed {
				t.Fatal("regression fixture did not stop at intended failure")
			}
			if cp.current.Pending == nil && cp.current.Phase != "normalize" {
				t.Fatal("committed resume lost mandatory next phase")
			}
			frame, err := a.advance(context.Background(), cmd)
			wantCalls := 2
			if failure == "post_resume_resolve" {
				wantCalls = 1
			}
			if err != nil || frame.Next != "normalize" || cp.current.Phase != "normalize" || cp.current.Pending != nil || calls != wantCalls || cp.current.Work.Budget != budget {
				t.Fatalf("resume recovery %+v checkpoint %+v calls %d error %v", frame, cp.current, calls, err)
			}
			f.r.Binding.Fence++
			if _, err := callLocalV2(a, s, "normalize"); err == nil {
				t.Fatal("retained phase bypassed current processing binding")
			}
		})
	}
}

func (f *admissionFakeV2) Scan(_ context.Context, r run.AdmissionScan) ([]run.AdmissionItem, error) {
	f.calls = append(f.calls, "scan")
	f.scans = append(f.scans, r)
	return f.items, nil
}

type domainFakeV2 func(IntakeCommandV2) (IntakeAdvanceResultV2, error)

func (f domainFakeV2) Advance(_ context.Context, c IntakeCommandV2) (IntakeAdvanceResultV2, error) {
	return f(c)
}

type resumeFakeV2 func(run.IntakeWork, run.AdmissionResolution, *IntakeDecisionV2) error

func (f resumeFakeV2) Validate(_ context.Context, w run.IntakeWork, r run.AdmissionResolution, d *IntakeDecisionV2) error {
	return f(w, r, d)
}

func localFixtureV2() (IntakeStateV2, *checkpointFakeV2, *admissionFakeV2, *IntakeActivitiesV2) {
	s := intakeTestStateV2()
	w := run.IntakeWork{Execution: run.ScopedExecution{Country: "vn", WorkflowID: s.WorkflowID, WorkflowKind: run.DocumentIngest,
		AttemptKey: "attempt", AttemptFence: 1, AdmissionEpoch: 1, Owner: "worker",
		Scope: run.ExecutionScope{Kind: run.ScopeIntake, Subject: run.IntakeSubject{Kind: run.SubjectDocumentVersion, DocumentVersion: "version"}}},
		Stage: run.Extract, Substep: "convert", Shard: "artifact", InputDigest: s.Work.Manifest, RecipeDigest: s.Work.Manifest,
		BudgetDigest: s.Work.Manifest, Budget: run.Budget{MaxItems: 100, MaxInputBytes: 100, MaxPages: 1, MaxElapsed: time.Second, MaxConcurrent: 1}}
	cp := &checkpointFakeV2{ref: s.Work, current: IntakeLocalCheckpointV2{Work: w, ExecutionID: 1}}
	f := &admissionFakeV2{r: run.AdmissionResolution{Found: true, SlotKey: "slot-" + strings.Repeat("a", 32), WorkflowID: s.WorkflowID,
		VersionKey: "version", AttemptKey: "attempt", Revision: 1, State: run.AdmissionProcessing,
		Binding: run.AdmissionBinding{ExecutionID: 1, Epoch: 1, Fence: 1, Owner: "worker"}}}
	a := &IntakeActivitiesV2{Queue: DatabaseTaskQueueV2, Checkpoints: cp, Admission: f}
	return s, cp, f, a
}

func callLocalV2(a *IntakeActivitiesV2, s IntakeStateV2, op string) (IntakeFrameV2, error) {
	a.Queue = intakeQueueV2(op)
	return a.advance(context.Background(), IntakeCommandV2{State: s, Operation: op})
}

func TestIntakeV2BindAndCurrentBindingBeforeProcessing(t *testing.T) {
	s, cp, f, a := localFixtureV2()
	f.r.State = run.AdmissionReserved
	frame, err := callLocalV2(a, s, "resume")
	if err != nil || frame.Next != "fetch" || fmt.Sprint(f.calls) != "[resolve bind resolve]" {
		t.Fatalf("frame %+v calls %v error %v", frame, f.calls, err)
	}
	f.r.Binding.Fence++
	a.Operations = domainFakeV2(func(IntakeCommandV2) (IntakeAdvanceResultV2, error) {
		t.Fatal("stale binding dispatched processing")
		return IntakeAdvanceResultV2{}, nil
	})
	if _, err := callLocalV2(a, s, "fetch"); err == nil {
		t.Fatal("stale binding permitted work")
	}
	if cp.current.Work.Budget != cp.retained[0].Work.Budget {
		t.Fatal("binding reset frozen budget")
	}
}

func TestIntakeV2InvalidResumeCauseCreatesNoDemand(t *testing.T) {
	for _, failure := range []string{"missing", "authority", "actor", "action", "fence", "epoch", "original", "candidate", "report", "scope", "expiry", "lifecycle"} {
		t.Run(failure, func(t *testing.T) {
			s, _, f, a := localFixtureV2()
			f.r.State, f.r.WaitReason, f.r.LifecycleReceipt = run.AdmissionOpenOnly, "worker_loss", "admission-"+strings.Repeat("b", 32)
			if failure != "missing" {
				a.ResumeValidation = resumeFakeV2(func(run.IntakeWork, run.AdmissionResolution, *IntakeDecisionV2) error { return errors.New(failure) })
			}
			_, _ = callLocalV2(a, s, "resume")
			for _, call := range f.calls {
				if call == "request_resume" || call == "resume" {
					t.Fatal("invalid cause created demand")
				}
			}
		})
	}
}

func TestIntakeV2ResumeRefusalAndSaturatedWorkerLossProgress(t *testing.T) {
	for _, refusal := range []run.AdmissionRefusal{run.AdmissionProcessingFull, run.AdmissionOpenFull, run.AdmissionBothFull, run.AdmissionResourceBusy, run.AdmissionNotTurn, ""} {
		s, cp, f, a := localFixtureV2()
		f.r.State, f.r.WaitReason, f.r.LifecycleReceipt = run.AdmissionOpenOnly, "worker_loss", "admission-"+strings.Repeat("b", 32)
		f.refusal = refusal
		a.ResumeValidation = resumeFakeV2(func(run.IntakeWork, run.AdmissionResolution, *IntakeDecisionV2) error {
			f.calls = append(f.calls, "validate")
			return nil
		})
		frame, err := callLocalV2(a, s, "resume")
		if err != nil {
			t.Fatal(err)
		}
		want := "progress"
		if refusal != "" {
			want = "wait"
		}
		if frame.Status != want || !strings.Contains(fmt.Sprint(f.calls), "validate request_resume resolve validate resume") || cp.current.Work.Budget != cp.retained[0].Work.Budget {
			t.Fatalf("resume %s calls %v frame %+v", refusal, f.calls, frame)
		}
	}
}

func TestIntakeV2RetainedDecisionValidatedBeforeDemand(t *testing.T) {
	s, cp, f, a := localFixtureV2()
	quarantineCheckpointV2(t, cp)
	f.r.State, f.r.WaitReason, f.r.LifecycleReceipt = run.AdmissionOpenOnly, "quarantine", "admission-"+strings.Repeat("b", 32)
	decision := &IntakeDecisionV2{DecisionRef: *intakeTestFrameV2(s, "done", "").Receipt, ExpectedEpoch: 1, ReportHash: s.Work.Manifest}
	a.ResumeValidation = resumeFakeV2(func(w run.IntakeWork, r run.AdmissionResolution, d *IntakeDecisionV2) error {
		if d == nil || *d != *decision || w.Execution != cp.current.Work.Execution || r.LifecycleReceipt == "" {
			return errors.New("retained cause mismatch")
		}
		f.calls = append(f.calls, "validate")
		return nil
	})
	frame, err := a.advance(context.Background(), IntakeCommandV2{State: s, Operation: "decision", Decision: decision})
	if err != nil || frame.Next != "normalize" || !strings.Contains(fmt.Sprint(f.calls), "validate request_resume resolve validate resume") || cp.current.Decision == nil {
		t.Fatalf("decision %+v calls %v error %v", frame, f.calls, err)
	}
}

func TestIntakeV2TerminalAcceptanceWinsExpiryRace(t *testing.T) {
	s, cp, f, a := localFixtureV2()
	quarantineCheckpointV2(t, cp)
	f.r.State, f.r.WaitReason = run.AdmissionOpenOnly, "quarantine"
	f.mutation = func(op string, _ run.AdmissionChange) error {
		if op == "settle" {
			f.r.State, f.r.TerminalReason = run.AdmissionClosing, run.AdmissionCompleted
		}
		return nil
	}
	frame, err := callLocalV2(a, s, "expiry")
	if err != nil || frame.Status != "stopped" || strings.Contains(fmt.Sprint(f.calls), "close") {
		t.Fatalf("stale quarantine closed terminal acceptance: %+v %v %v", frame, f.calls, err)
	}
}

func TestIntakeV2UnverifiedCheckpointGrantsNoAuthority(t *testing.T) {
	s, cp, f, a := localFixtureV2()
	cp.err = errors.New("checkpoint hash mismatch")
	if _, err := callLocalV2(a, s, "resume"); err == nil || len(f.calls) != 0 {
		t.Fatalf("unverified checkpoint reached admission: %v %v", f.calls, err)
	}
}

func TestIntakeV2LostReceiptReplaysOriginalRevision(t *testing.T) {
	s, cp, f, a := localFixtureV2()
	f.r.State = run.AdmissionReserved
	attempts := 0
	f.mutation = func(op string, _ run.AdmissionChange) error {
		if op == "bind" {
			attempts++
			if attempts == 1 {
				f.r.Revision = 4 // committed, transport response lost
				return &TransientError{Cause: errors.New("lost response")}
			}
		}
		return nil
	}
	if _, err := callLocalV2(a, s, "resume"); err == nil || cp.current.Pending == nil {
		t.Fatal("uncertain operation not retained")
	}
	if _, err := callLocalV2(a, s, "resume"); err != nil {
		t.Fatal(err)
	}
	if len(f.changes) != 2 || f.changes[0].ExpectedRevision != f.changes[1].ExpectedRevision || cp.current.Pending != nil {
		t.Fatalf("changed retry revisions: %+v", f.changes)
	}
}

func TestIntakeV2ConflictDoesNotBlindlyRetry(t *testing.T) {
	s, cp, f, a := localFixtureV2()
	f.r.State = run.AdmissionReserved
	f.mutation = func(string, run.AdmissionChange) error {
		f.r.State, f.r.TerminalReason = run.AdmissionClosing, run.AdmissionAbandoned
		return run.ErrAdmissionConflict
	}
	if frame, err := callLocalV2(a, s, "resume"); err != nil || frame.Status != "wait" {
		t.Fatalf("conflict did not return bounded wait: %+v %v", frame, err)
	}
	frame, err := callLocalV2(a, s, "resume")
	if err != nil || frame.Status != "stopped" || len(f.changes) != 1 || cp.current.Pending != nil {
		t.Fatalf("stale permission retried: %+v %v", frame, err)
	}
}

func TestIntakeV2ReserveBeforeLaunchAndRefusalRetainsWindow(t *testing.T) {
	for _, refusal := range []run.AdmissionRefusal{"", run.AdmissionProcessingFull, run.AdmissionOpenFull, run.AdmissionBothFull, run.AdmissionResourceBusy, run.AdmissionNotTurn} {
		s, cp, f, a := localFixtureV2()
		child := cp.current.Work
		s = sourceTestStateV2()
		cp.current.Work.Execution.WorkflowKind, cp.current.Work.Execution.WorkflowID = run.SourceSync, s.WorkflowID
		cp.current.Work.Execution.Scope.Subject = run.IntakeSubject{Kind: run.SubjectSourceWindow, SourceWindow: "window"}
		accounted := false
		a.Operations = domainFakeV2(func(cmd IntakeCommandV2) (IntakeAdvanceResultV2, error) {
			if cmd.Operation == "admission-wait" {
				accounted = true
				return IntakeAdvanceResultV2{}, nil
			}
			return IntakeAdvanceResultV2{Frame: intakeTestFrameV2(s, "progress", "resume"), Children: []IntakeChildBindingV2{
				{ObservationVersion: "version", DeliveryKey: "delivery", Work: child, Envelope: s.Work}}}, nil
		})
		f.reserve = func(r run.AdmissionReserveRequest) (run.AdmissionResult, error) {
			if r.DeliveryKey != "delivery" || r.Child != child || r.Actor != cp.current.Work {
				t.Fatal("reservation binding changed")
			}
			if refusal != "" {
				return run.AdmissionResult{Refusal: refusal, Open: 256}, nil
			}
			receipt := f.receipt()
			receipt.State = run.AdmissionReserved
			return run.AdmissionResult{Receipt: &receipt}, nil
		}
		frame, err := a.advance(context.Background(), IntakeCommandV2{State: s, Operation: "discovery", LaunchLimit: 16})
		if err != nil || fmt.Sprint(f.calls) != "[reserve]" {
			t.Fatalf("reserve calls %v error %v", f.calls, err)
		}
		if refusal == "" && (len(frame.Launches) != 1 || accounted) || refusal != "" && (len(frame.Launches) != 0 || !accounted || frame.Status != "wait") {
			t.Fatalf("invalid refusal handling %+v accounted %v", frame, accounted)
		}
	}
}

func quarantineCheckpointV2(t *testing.T, cp *checkpointFakeV2) {
	t.Helper()
	w := cp.current.Work
	w.Stage, w.Substep, w.Shard = run.Normalize, "evidence-quarantine", "vn:doc:11111111-1111-1111-1111-111111111111:rev:"+strings.Repeat("a", 64)
	d := w.InputDigest
	r := run.IntakeApplyRequest{Work: w, IdempotencyKey: run.IntakeIdempotencyKey(w), OutputDigest: d,
		Outcome: run.OutcomeRejected, Acceptance: model.Quarantined, RevisionKey: w.Shard, CandidateDigest: d,
		QualityReportHash: d, OriginalHash: d, Counts: run.Counts{Expected: 1, Processed: 1, Rejected: 1}}
	receipt := run.IntakeReceipt{ExecutionID: cp.ref.Key, ResultID: "result-" + strings.Repeat("c", 32), Result: r}
	if err := receipt.Validate(); err != nil {
		t.Fatal(err)
	}
	cp.current.Quarantine, cp.current.QuarantineAt, cp.current.DecisionEpoch = &receipt, time.Unix(2000000000, 0).UTC(), 1
}

func TestIntakeV2ExpiryUsesRetainedResultAndFrozenDatabaseDue(t *testing.T) {
	for _, frozen := range []time.Duration{2 * 24 * time.Hour, 10 * 24 * time.Hour} {
		s, cp, f, a := localFixtureV2()
		quarantineCheckpointV2(t, cp)
		original := cp.current.Quarantine.ResultID
		f.r.State, f.r.WaitReason = run.AdmissionOpenOnly, "quarantine"
		now := cp.current.QuarantineAt.Add(24 * time.Hour)
		f.close = func(r run.AdmissionCloseRequest) error {
			if r.CauseReceipt != original || r.Reason != run.AdmissionExpired {
				t.Fatal("derived or changed expiry cause")
			}
			if now.Before(cp.current.QuarantineAt.Add(frozen)) {
				return run.ErrAdmissionConflict
			}
			return nil
		}
		frame, err := callLocalV2(a, s, "expiry")
		if err != nil || frame.Status != "wait" {
			t.Fatalf("early close did not wait: %+v %v", frame, err)
		}
		// The same original reference reloads durable private receipt state after
		// continuation/lost wake-up; no admission result/watermark becomes a cause.
		now = cp.current.QuarantineAt.Add(frozen)
		frame, err = callLocalV2(a, s, "expiry")
		if err != nil || frame.Status != "stopped" || frame.Reason != "expired" || frame.Receipt != nil || cp.current.Quarantine.ResultID != original {
			t.Fatalf("expiry %+v error %v", frame, err)
		}
	}
}

func TestIntakeV2MissingQuarantineEvidenceCannotClose(t *testing.T) {
	s, _, f, a := localFixtureV2()
	f.r.State, f.r.WaitReason = run.AdmissionOpenOnly, "quarantine"
	if _, err := callLocalV2(a, s, "expiry"); err == nil || strings.Contains(fmt.Sprint(f.calls), "close") {
		t.Fatalf("missing cause reached close: %v %v", f.calls, err)
	}
}

type acceptanceFakeV2 struct {
	request run.IntakeApplyRequest
	apply   func(run.IntakeApplyRequest) (run.IntakeReceipt, error)
	project func(IntakeCommandV2, run.IntakeReceipt) (IntakeFrameV2, error)
}

func (f *acceptanceFakeV2) Prepare(context.Context, IntakeCommandV2) (run.IntakeApplyRequest, error) {
	return f.request, nil
}

func (f *acceptanceFakeV2) Apply(_ context.Context, r run.IntakeApplyRequest) (run.IntakeReceipt, error) {
	return f.apply(r)
}

func (f *acceptanceFakeV2) Project(_ context.Context, cmd IntakeCommandV2, r run.IntakeReceipt) (IntakeFrameV2, error) {
	return f.project(cmd, r)
}

func TestIntakeV2QuarantineApplyCheckpointThenSettle(t *testing.T) {
	s, cp, f, a := localFixtureV2()
	quarantineCheckpointV2(t, cp)
	receipt, at := *cp.current.Quarantine, cp.current.QuarantineAt
	cp.current.Quarantine = nil
	accepted := &acceptanceFakeV2{request: receipt.Result}
	accepted.apply = func(run.IntakeApplyRequest) (run.IntakeReceipt, error) {
		f.calls = append(f.calls, "apply")
		return receipt, nil
	}
	accepted.project = func(IntakeCommandV2, run.IntakeReceipt) (IntakeFrameV2, error) {
		frame := intakeTestFrameV2(s, "quarantined", "")
		frame.Receipt.Key, frame.QuarantineAt, frame.DecisionEpoch, frame.ReportHash = receipt.ResultID, at, 1, s.Work.Manifest
		return frame, nil
	}
	f.mutation = func(op string, _ run.AdmissionChange) error {
		if op == "settle" {
			if cp.current.Quarantine == nil || cp.current.Quarantine.ResultID != receipt.ResultID || !strings.Contains(fmt.Sprint(f.calls), "apply") {
				t.Fatal("settlement before actual quarantine checkpoint")
			}
			f.r.State, f.r.WaitReason = run.AdmissionOpenOnly, "quarantine"
		}
		return nil
	}
	a.Inputs, a.Acceptance = accepted, accepted
	frame, err := callLocalV2(a, s, "quarantine")
	if err != nil || frame.Status != "quarantined" || fmt.Sprint(f.calls) != "[resolve apply resolve settle]" {
		t.Fatalf("quarantine %+v calls %v error %v", frame, f.calls, err)
	}
}

func TestIntakeV2TerminalSettlementResponseLossKeepsGenuineResult(t *testing.T) {
	s, cp, f, a := localFixtureV2()
	quarantineCheckpointV2(t, cp)
	receipt := *cp.current.Quarantine
	cp.current.Quarantine = nil
	r := &receipt.Result
	r.Acceptance, r.Outcome, r.Complete = model.Accepted, run.OutcomeSucceeded, true
	r.Work.Substep = "evidence-acceptance"
	r.IdempotencyKey = run.IntakeIdempotencyKey(r.Work)
	r.Counts = run.Counts{Expected: 1, Processed: 1, Succeeded: 1}
	if err := receipt.Validate(); err != nil {
		t.Fatal(err)
	}
	applies, settlements := 0, 0
	accepted := &acceptanceFakeV2{request: *r}
	accepted.apply = func(run.IntakeApplyRequest) (run.IntakeReceipt, error) {
		applies++
		return receipt, nil
	}
	accepted.project = func(IntakeCommandV2, run.IntakeReceipt) (IntakeFrameV2, error) {
		frame := intakeTestFrameV2(s, "accepted", "")
		frame.Receipt.Key = receipt.ResultID
		return frame, nil
	}
	a.Inputs, a.Acceptance = accepted, accepted
	f.mutation = func(op string, _ run.AdmissionChange) error {
		if op == "settle" {
			settlements++
			f.r.State, f.r.TerminalReason = run.AdmissionClosing, run.AdmissionCompleted
			if settlements == 1 {
				return &TransientError{Cause: errors.New("lost settlement receipt")}
			}
		}
		return nil
	}
	if _, err := callLocalV2(a, s, "evidence"); err == nil || cp.current.Terminal == nil || cp.current.Pending == nil {
		t.Fatal("terminal result or retry identity lost")
	}
	frame, err := callLocalV2(a, s, "evidence")
	if err != nil || frame.Status != "accepted" || frame.Receipt.Key != receipt.ResultID || applies != 1 || settlements != 2 || f.changes[0].ExpectedRevision != f.changes[1].ExpectedRevision {
		t.Fatalf("terminal replay %+v applies %d settlements %d error %v", frame, applies, settlements, err)
	}
}

func intakeTestStateV2() IntakeStateV2 {
	return IntakeStateV2{Country: "vn", WorkflowID: "document-ingest/vn/version/attempt", Kind: run.DocumentIngest,
		Work: run.IntakeWorkEnvelope{Key: "execution-" + strings.Repeat("a", 32), Manifest: model.Digest(strings.Repeat("a", 64))}, Config: ProposedIntakeConfigV2()}
}

func TestIntakeV2ChildIdentityBindsVersionAndAttempt(t *testing.T) {
	e := run.ScopedExecution{Country: "vn", WorkflowID: "document-ingest/vn/version/attempt", WorkflowKind: run.DocumentIngest,
		AttemptKey: "attempt", AttemptFence: 1, AdmissionEpoch: 1, Owner: "worker",
		Scope: run.ExecutionScope{Kind: run.ScopeIntake, Subject: run.IntakeSubject{Kind: run.SubjectDocumentVersion, DocumentVersion: "version"}}}
	if err := validateChildExecutionV2(e, "version"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"document-ingest/vn/source/version", "document-ingest/vn/version/other", "document-ingest/my/version/attempt"} {
		bad := e
		bad.WorkflowID = id
		if validateChildExecutionV2(bad, "version") == nil {
			t.Fatal("unbound child accepted")
		}
	}
	if validateChildExecutionV2(e, "other") == nil {
		t.Fatal("observation substitution accepted")
	}
}

func TestIntakeV2TransportRejectsContent(t *testing.T) {
	s := intakeTestStateV2()
	s.Work.Key = "https://example.invalid/document"
	if s.Validate() == nil {
		t.Fatal("URL accepted in execution envelope")
	}
	s = intakeTestStateV2()
	s.WorkflowID = "document-ingest/vn/version/https://example.invalid"
	if s.Validate() == nil {
		t.Fatal("URL accepted in workflow identity")
	}
}

type failingIntakePortV2 struct{}

func (failingIntakePortV2) Advance(context.Context, IntakeCommandV2) (IntakeAdvanceResultV2, error) {
	return IntakeAdvanceResultV2{}, errors.New("raw provider body must never leave Vietnam")
}

func TestIntakeV2ActivityRedactsPortErrors(t *testing.T) {
	s, _, _, a := localFixtureV2()
	a.Operations = failingIntakePortV2{}
	_, err := a.Advance(context.Background(), IntakeCommandV2{State: s, Operation: "reminder"})
	if err == nil || strings.Contains(err.Error(), "provider body") {
		t.Fatal("activity did not sanitize its failure")
	}
}
