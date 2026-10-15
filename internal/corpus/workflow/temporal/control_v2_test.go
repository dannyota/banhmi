package temporal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"

	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/run"
)

func TestControlReconcileV2LostSignalAcknowledgementKeepsIntent(t *testing.T) {
	s := intakeTestStateV2()
	s.Kind, s.WorkflowID = run.ControlReconcile, "corpus-control/vn"
	delivered := false
	acknowledgements := 0
	for pass := 0; pass < 2; pass++ {
		env := intakeTestEnvironmentV2(s, func(cmd IntakeCommandV2) (IntakeFrameV2, error) {
			frame := intakeTestFrameV2(s, "done", "")
			if cmd.Operation == "control" && !delivered {
				frame.Notifications = []IntakeNotificationV2{{WorkflowID: "document-ingest/vn/version/attempt", Receipt: *frame.Receipt}}
			}
			if cmd.Operation == "delivered" {
				if cmd.Notification == nil {
					return frame, errors.New("missing intent")
				}
				if !delivered {
					acknowledgements++
				}
				delivered = true
			}
			return frame, nil
		})
		var signalErr error
		if pass == 0 {
			signalErr = errors.New("synthetic acknowledgement loss")
		}
		env.OnSignalExternalWorkflow(mock.Anything, "document-ingest/vn/version/attempt", "", IntakeReceiptSignalV2, mock.Anything).Return(signalErr).Once()
		env.ExecuteWorkflow(ControlReconcileV2, s)
		if pass == 0 && (env.GetWorkflowError() == nil || delivered) {
			t.Fatal("failed signal acknowledged its durable intent")
		}
		if pass == 1 && env.GetWorkflowError() != nil {
			t.Fatal(env.GetWorkflowError())
		}
		env.AssertExpectations(t)
	}
	if !delivered || acknowledgements != 1 {
		t.Fatalf("delivered %v acknowledgements %d", delivered, acknowledgements)
	}
}

type stopFakeV2 struct {
	local    IntakeLocalStopV2
	ref      IntakeStopReferenceV2
	err      error
	screened int
}

func (f *stopFakeV2) Screen(_ context.Context, _ run.IntakeWork, key string) (IntakeStopReferenceV2, error) {
	f.screened++
	if key != f.local.Receipt.Key {
		return IntakeStopReferenceV2{}, errors.New("wrong committed close")
	}
	return f.ref, f.err
}

func (f *stopFakeV2) Resolve(context.Context, IntakeStopReferenceV2) (IntakeLocalStopV2, error) {
	return f.local, f.err
}

type transportFakeV2 struct {
	starts int
	err    error
}

func (f *transportFakeV2) Reconnect(context.Context, string) error { return f.err }

func (f *transportFakeV2) Drain(context.Context, string, IntakeStopReferenceV2) error {
	f.starts++
	return f.err
}

type controlCheckpointFakeV2 struct {
	target IntakeLocalCheckpointV2
}

func (f controlCheckpointFakeV2) Slot(context.Context, run.IntakeWork, run.AdmissionItem) (IntakeLocalCheckpointV2, error) {
	return f.target, nil
}

func (f controlCheckpointFakeV2) Configuration(context.Context, run.IntakeWork) (*run.AdmissionConfigureRequest, error) {
	return nil, nil
}

func stoppedFixtureV2(t *testing.T) (IntakeStateV2, *checkpointFakeV2, *admissionFakeV2, *IntakeActivitiesV2, *stopFakeV2, *transportFakeV2) {
	t.Helper()
	s, cp, f, a := localFixtureV2()
	control := cp.current.Work
	control.Execution.WorkflowID, control.Execution.WorkflowKind = "corpus-control/vn", run.ControlReconcile
	control.Execution.Scope = run.ExecutionScope{Kind: run.ScopeControl, CountryControl: "vn"}
	f.r.State, f.r.TerminalReason, f.r.CloseReceipt = run.AdmissionClosing, run.AdmissionAbandoned, "admission-"+strings.Repeat("a", 32)
	manifest := IntakeStopManifestV2{Country: "vn", WorkflowID: s.WorkflowID, VersionKey: "version", AttemptKey: "attempt", SlotKey: f.r.SlotKey, CloseReceipt: f.r.CloseReceipt}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	stop := &stopFakeV2{local: IntakeLocalStopV2{Control: control, Manifest: data, Country: "vn", WorkflowID: s.WorkflowID,
		VersionKey: "version", AttemptKey: "attempt", SlotKey: f.r.SlotKey, Receipt: f.receipt()},
		ref: IntakeStopReferenceV2{Key: f.r.CloseReceipt, Manifest: model.Digest(hex.EncodeToString(digest[:]))}}
	transport := &transportFakeV2{}
	a.Stops, a.Transport = stop, transport
	s.Stop = &stop.ref
	return s, cp, f, a, stop, transport
}

func TestIntakeV2DrainingChildPerformsNoProcessing(t *testing.T) {
	s, _, f, a, _, transport := stoppedFixtureV2(t)
	a.Operations = domainFakeV2(func(IntakeCommandV2) (IntakeAdvanceResultV2, error) {
		t.Fatal("draining child performed work")
		return IntakeAdvanceResultV2{}, nil
	})
	frame, err := a.advance(context.Background(), IntakeCommandV2{State: s, Operation: "drain"})
	if err != nil || frame.Status != "stopped" || frame.Receipt != nil || fmt.Sprint(f.calls) != "[resolve]" || transport.starts != 0 {
		t.Fatalf("drainer %+v calls %v error %v", frame, f.calls, err)
	}
	if _, err := json.Marshal(intakeCompletionV2(frame)); err != nil {
		t.Fatal(err)
	}
}

func TestIntakeV2StopRejectsHashTargetAndStateSubstitution(t *testing.T) {
	for _, kind := range []string{"hash", "target", "receipt", "actor", "country", "state", "reason", "kind", "slot", "manifest", "released"} {
		t.Run(kind, func(t *testing.T) {
			s, _, f, a, stop, _ := stoppedFixtureV2(t)
			switch kind {
			case "hash":
				stop.ref.Manifest = s.Work.Manifest
			case "target":
				stop.local.WorkflowID = "document-ingest/vn/other/attempt"
			case "receipt":
				f.r.CloseReceipt = "admission-" + strings.Repeat("b", 32)
			case "actor":
				stop.local.Control.Execution.WorkflowKind = run.DocumentIngest
			case "country":
				stop.local.Country = "my"
			case "state":
				f.r.State = run.AdmissionProcessing
			case "reason":
				f.r.TerminalReason = run.AdmissionCancelled
			case "kind":
				stop.ref.Key = "result-" + strings.Repeat("a", 32)
			case "slot":
				stop.local.SlotKey = "slot-" + strings.Repeat("b", 32)
			case "manifest":
				stop.local.Manifest = []byte("{}")
			case "released":
				f.r.State = run.AdmissionReleased
			}
			err := a.checkStop(context.Background(), stop.ref, stop.local, s.Country, s.WorkflowID, s.Config.EnvelopeBytes)
			if err == nil || kind == "released" && !errors.Is(err, run.ErrAdmissionUnchanged) {
				t.Fatalf("substituted stop %s: %v", kind, err)
			}
		})
	}
}

func TestIntakeV2ControlCloseBeforeDrainAndUncertainCommit(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		s, cp, f, a, stop, transport := stoppedFixtureV2(t)
		target := cp.current
		cp.current.Work = stop.local.Control
		s.Kind, s.WorkflowID, s.Stop = run.ControlReconcile, cp.current.Work.Execution.WorkflowID, nil
		a.ControlCheckpoints = controlCheckpointFakeV2{target: target}
		f.r.State, f.r.TerminalReason, f.r.CloseReceipt = run.AdmissionReserved, "", ""
		f.items = []run.AdmissionItem{{SlotKey: f.r.SlotKey, Revision: 1, ID: 1, UpdatedAt: time.Unix(2000000000, 0).UTC()}}
		f.close = func(r run.AdmissionCloseRequest) error {
			if stop.screened != 0 || transport.starts != 0 || r.Reason != run.AdmissionAbandoned {
				t.Fatal("transport preceded committed close")
			}
			if uncertain {
				return &TransientError{Cause: errors.New("unknown commit")}
			}
			return nil
		}
		err := a.reconcileSweep(context.Background(), IntakeCommandV2{State: s}, &cp.current)
		if uncertain {
			if err == nil || stop.screened != 0 || transport.starts != 0 || cp.current.Pending == nil {
				t.Fatal("uncertain close drained transport")
			}
			old := cp.current.Pending.Change.ExpectedRevision
			uncertain = false
			if err := a.reconcileSweep(context.Background(), IntakeCommandV2{State: s}, &cp.current); err != nil || stop.screened != 1 || transport.starts != 1 {
				t.Fatalf("lost stop did not recover: %v", err)
			}
			// Reconcile is first; the failed and replayed Close use one key.
			if f.changes[1].ExpectedRevision != old || f.changes[2].ExpectedRevision != old {
				t.Fatal("lost Close receipt changed operation revision")
			}
		} else if err != nil || stop.screened != 1 || transport.starts != 1 {
			t.Fatalf("committed stop not drained: %v screen %d start %d calls %v", err, stop.screened, transport.starts, f.calls)
		}
	}
}

func TestIntakeV2ControlScanBoundsAndWrap(t *testing.T) {
	s, cp, f, a, stop, _ := stoppedFixtureV2(t)
	cp.current.Work = stop.local.Control
	cp.current.Work.Budget.MaxItems = 2
	cp.current.ScanTime, cp.current.ScanID = time.Unix(2000000000, 0).UTC(), 7
	s.Kind, s.WorkflowID, s.Stop = run.ControlReconcile, cp.current.Work.Execution.WorkflowID, nil
	a.ControlCheckpoints = controlCheckpointFakeV2{}
	if err := a.reconcileSweep(context.Background(), IntakeCommandV2{State: s}, &cp.current); err != nil {
		t.Fatal(err)
	}
	if len(f.scans) != 1 || f.scans[0].Limit != 2 || f.scans[0].AfterID != 7 || !cp.current.ScanTime.IsZero() || cp.current.ScanID != 0 {
		t.Fatalf("scan cursor/limit: %+v checkpoint %+v", f.scans, cp.current)
	}
	f.items = make([]run.AdmissionItem, 3)
	if err := a.reconcileSweep(context.Background(), IntakeCommandV2{State: s}, &cp.current); err == nil {
		t.Fatal("oversized scan entered unbounded loop")
	}
}

func TestIntakeV2BindCloseRaceNeverStartsClosedWork(t *testing.T) {
	for _, bindFirst := range []bool{false, true} {
		s, _, f, a, stop, _ := stoppedFixtureV2(t)
		if bindFirst {
			f.r.State, f.r.TerminalReason, f.r.CloseReceipt = run.AdmissionProcessing, "", ""
			if err := a.checkStop(context.Background(), stop.ref, stop.local, s.Country, s.WorkflowID, s.Config.EnvelopeBytes); err == nil {
				t.Fatal("fresh Bind did not invalidate stale stop")
			}
		} else {
			s.Stop = nil // ordinary delayed start, not the specialized drainer
			frame, err := callLocalV2(a, s, "resume")
			if err != nil || frame.Status != "stopped" || strings.Contains(fmt.Sprint(f.calls), "bind") {
				t.Fatalf("delayed start processed closed slot: %v %v", f.calls, err)
			}
		}
	}
}

func TestControlReconcileV2RejectsCrossCountryNotification(t *testing.T) {
	s := intakeTestStateV2()
	s.Kind, s.WorkflowID = run.ControlReconcile, "corpus-control/vn"
	env := intakeTestEnvironmentV2(s, func(IntakeCommandV2) (IntakeFrameV2, error) {
		frame := intakeTestFrameV2(s, "done", "")
		frame.Notifications = []IntakeNotificationV2{{WorkflowID: "document-ingest/my/version/attempt", Receipt: *frame.Receipt}}
		return frame, nil
	})
	env.ExecuteWorkflow(ControlReconcileV2, s)
	if env.GetWorkflowError() == nil {
		t.Fatal("cross-country signal accepted")
	}
}

func TestIntakeV2DistinctSaturatedSlotsFollowAuthoritativeResumeFIFO(t *testing.T) {
	// This is a scripted persistence boundary for orchestration, not database
	// arbitration/load proof. Every retained slot and checkpoint has its own ID.
	type retainedSlot struct {
		state      IntakeStateV2
		checkpoint *checkpointFakeV2
		resolution run.AdmissionResolution
	}
	byWorkflow := make(map[string]*retainedSlot)
	bySlot := make(map[string]*retainedSlot)
	var slots []*retainedSlot
	var queue []string
	for i := 0; i < 256; i++ {
		s, cp, initial, _ := localFixtureV2()
		version := fmt.Sprintf("version%d", i)
		s.WorkflowID = "document-ingest/vn/" + version + "/attempt"
		s.Work.Key = fmt.Sprintf("execution-%032x", i+1)
		cp.ref, cp.current.ExecutionID, cp.current.Phase = s.Work, int64(i+1), "convert"
		cp.current.Work.Execution.WorkflowID = s.WorkflowID
		cp.current.Work.Execution.Scope.Subject.DocumentVersion = run.DocumentVersionKey(version)
		r := initial.r
		r.WorkflowID, r.VersionKey, r.SlotKey = s.WorkflowID, version, fmt.Sprintf("slot-%032x", i+1)
		r.Binding.ExecutionID = cp.current.ExecutionID
		r.State, r.WaitReason, r.LifecycleReceipt = run.AdmissionOpenOnly, "worker_loss", fmt.Sprintf("admission-%032x", i+1)
		slot := &retainedSlot{state: s, checkpoint: cp, resolution: r}
		slots, queue = append(slots, slot), append(queue, r.SlotKey)
		byWorkflow[r.WorkflowID], bySlot[r.SlotKey] = slot, slot
	}
	processing := 0
	var admitted []string
	receipt := func(r run.AdmissionResolution) run.AdmissionReceipt {
		return run.AdmissionReceipt{Key: r.LifecycleReceipt, SlotKey: r.SlotKey, Revision: r.Revision, State: r.State}
	}
	f := &admissionFakeV2{}
	f.resolve = func(r run.AdmissionResolveRequest) (run.AdmissionResolution, error) {
		if r.DocumentWork == nil {
			return run.AdmissionResolution{}, run.ErrInvalidAdmission
		}
		slot := byWorkflow[r.DocumentWork.Execution.WorkflowID]
		if slot == nil || r.Actor != slot.checkpoint.current.Work {
			return run.AdmissionResolution{}, run.ErrStaleGrant
		}
		return slot.resolution, nil
	}
	f.requestResume = func(c run.AdmissionChange) (run.AdmissionReceipt, error) {
		slot := bySlot[c.SlotKey]
		if slot == nil || c.Actor != slot.checkpoint.current.Work || c.ExpectedRevision != slot.resolution.Revision {
			return run.AdmissionReceipt{}, run.ErrAdmissionConflict
		}
		// Demand already exists for all 256 slots. Identical demand does not
		// change the persisted queue or overtake its head.
		return receipt(slot.resolution), nil
	}
	f.reserve = func(r run.AdmissionReserveRequest) (run.AdmissionResult, error) {
		if err := r.Validate(); err != nil {
			return run.AdmissionResult{}, err
		}
		return run.AdmissionResult{Refusal: run.AdmissionOpenFull, Open: 256, Processing: int64(processing)}, nil
	}
	f.resume = func(c run.AdmissionChange) (run.AdmissionResult, error) {
		slot := bySlot[c.SlotKey]
		if slot == nil || c.Actor != slot.checkpoint.current.Work || c.ExpectedRevision != slot.resolution.Revision {
			return run.AdmissionResult{}, run.ErrAdmissionConflict
		}
		if processing == 32 {
			return run.AdmissionResult{Refusal: run.AdmissionProcessingFull, Open: 256, Processing: 32}, nil
		}
		if len(queue) == 0 || queue[0] != c.SlotKey {
			return run.AdmissionResult{Refusal: run.AdmissionNotTurn, Open: 256, Processing: int64(processing)}, nil
		}
		queue, processing = queue[1:], processing+1
		slot.resolution.State, slot.resolution.Revision = run.AdmissionProcessing, slot.resolution.Revision+1
		slot.resolution.WaitReason = ""
		admitted = append(admitted, c.SlotKey)
		r := receipt(slot.resolution)
		return run.AdmissionResult{Receipt: &r, Open: 256, Processing: int64(processing)}, nil
	}
	resume := func(slot *retainedSlot) IntakeFrameV2 {
		a := &IntakeActivitiesV2{Admission: f, Checkpoints: slot.checkpoint,
			ResumeValidation: resumeFakeV2(func(w run.IntakeWork, r run.AdmissionResolution, _ *IntakeDecisionV2) error {
				if w != slot.checkpoint.current.Work || r.LifecycleReceipt != slot.resolution.LifecycleReceipt {
					return errors.New("worker-loss cause mismatch")
				}
				return nil
			})}
		frame, err := callLocalV2(a, slot.state, "resume")
		if err != nil {
			t.Fatal(err)
		}
		return frame
	}
	if frame := resume(slots[1]); frame.Status != "wait" || processing != 0 || queue[0] != slots[0].resolution.SlotKey {
		t.Fatal("out-of-order resume bypassed persisted FIFO")
	}
	_, sourceCP, _, _ := localFixtureV2()
	child := sourceCP.current.Work
	sourceCP.current.Work.Execution.WorkflowID, sourceCP.current.Work.Execution.WorkflowKind = sourceTestStateV2().WorkflowID, run.SourceSync
	sourceCP.current.Work.Execution.Scope.Subject = run.IntakeSubject{Kind: run.SubjectSourceWindow, SourceWindow: "window"}
	for i := 0; i < 32; i++ {
		launch, err := f.Reserve(context.Background(), run.AdmissionReserveRequest{Actor: sourceCP.current.Work, Child: child, DeliveryKey: "older-launch"})
		if err != nil || launch.Refusal != run.AdmissionOpenFull {
			t.Fatal("open-full launch was not retained")
		}
		if frame := resume(slots[i]); frame.Status != "progress" || frame.Next != "convert" || admitted[i] != slots[i].resolution.SlotKey {
			t.Fatal("existing-slot resume did not follow authoritative FIFO")
		}
	}
	if frame := resume(slots[32]); frame.Status != "wait" || len(bySlot) != 256 || len(queue) != 224 || processing != 32 {
		t.Fatalf("saturation accounting: frame %+v slots %d queue %d processing %d", frame, len(bySlot), len(queue), processing)
	}
	for i, slot := range slots {
		want := run.AdmissionOpenOnly
		if i < 32 {
			want = run.AdmissionProcessing
		}
		if slot.resolution.State != want || slot.checkpoint.current.Work.Execution.WorkflowID != slot.resolution.WorkflowID {
			t.Fatalf("distinct retained slot %d lost identity or accounting", i)
		}
	}
}
