package temporal

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	temporalsdk "go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"

	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/run"
)

func intakeTestFrameV2(s IntakeStateV2, status, next string) IntakeFrameV2 {
	return IntakeFrameV2{Work: s.Work, Receipt: &run.IntakeReceiptEnvelope{
		Key: "result-" + strings.Repeat("a", 32), Manifest: model.Digest(strings.Repeat("a", 64))}, Status: status, Next: next}
}

func intakeTestEnvironmentV2(s IntakeStateV2, advance func(IntakeCommandV2) (IntakeFrameV2, error)) *testsuite.TestWorkflowEnvironment {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.SetStartWorkflowOptions(client.StartWorkflowOptions{ID: s.WorkflowID, TaskQueue: CoordinatorTaskQueueV2})
	env.RegisterActivityWithOptions(func(_ context.Context, cmd IntakeCommandV2) (IntakeFrameV2, error) {
		return advance(cmd)
	}, activity.RegisterOptions{Name: IntakeAdvanceNameV2})
	return env
}

func TestDocumentIngestV2OrdersGatesBeforeAcceptance(t *testing.T) {
	s := intakeTestStateV2()
	next := map[string]string{"resume": "fetch", "fetch": "capture", "capture": "detect", "detect": "convert",
		"convert": "normalize", "normalize": "structure", "structure": "gate", "gate": "candidate", "candidate": "evidence"}
	var operations []string
	env := intakeTestEnvironmentV2(s, func(cmd IntakeCommandV2) (IntakeFrameV2, error) {
		operations = append(operations, cmd.Operation)
		if cmd.Operation == "evidence" {
			return intakeTestFrameV2(s, "accepted", ""), nil
		}
		return intakeTestFrameV2(s, "progress", next[cmd.Operation]), nil
	})
	env.ExecuteWorkflow(DocumentIngestV2, s)
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	want := "[resume fetch capture detect convert normalize structure gate candidate evidence]"
	if fmt.Sprint(operations) != want {
		t.Fatalf("operations %v, want %s", operations, want)
	}
}

func TestDocumentIngestV2QuarantineReminderAndExpiryNeverAccept(t *testing.T) {
	s := intakeTestStateV2()
	s.Config.Reconcile = 24 * time.Hour
	var quarantined time.Time
	reminders, expiries, evidence := 0, 0, 0
	env := intakeTestEnvironmentV2(s, func(cmd IntakeCommandV2) (IntakeFrameV2, error) {
		frame := intakeTestFrameV2(s, "progress", "")
		switch cmd.Operation {
		case "resume":
			frame.Next = "gate"
		case "gate":
			frame.Next = "quarantine"
		case "quarantine":
			frame.Status, frame.DecisionEpoch = "quarantined", 1
			frame.QuarantineAt, frame.ReportHash = quarantined, s.Work.Manifest
		case "reminder":
			reminders++
		case "expiry":
			expiries++
			if envNowV2(quarantined, expiries, s.Config.Reconcile).Sub(quarantined) >= s.Config.Expiry {
				frame.Status, frame.Reason, frame.Receipt = "stopped", "expired", nil
			} else {
				frame.Status, frame.Next, frame.WaitUntil = "wait", "resume", quarantined.Add(s.Config.Reconcile)
			}
		case "evidence":
			evidence++
		default:
			return frame, intakeFailureV2("unexpected_operation", false)
		}
		return frame, nil
	})
	quarantined = env.Now()
	env.ExecuteWorkflow(DocumentIngestV2, s)
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if reminders != 1 || expiries != int(s.Config.Expiry/s.Config.Reconcile) || evidence != 0 || env.Now().Sub(quarantined) != s.Config.Expiry {
		t.Fatalf("reminders %d expiries %d evidence %d elapsed %s", reminders, expiries, evidence, env.Now().Sub(quarantined))
	}
}

func envNowV2(start time.Time, ticks int, interval time.Duration) time.Time {
	return start.Add(time.Duration(ticks) * interval)
}

func TestDocumentIngestV2CancellationAddsNoEvidence(t *testing.T) {
	s := intakeTestStateV2()
	cancels, evidence := 0, 0
	var due time.Time
	env := intakeTestEnvironmentV2(s, func(cmd IntakeCommandV2) (IntakeFrameV2, error) {
		frame := intakeTestFrameV2(s, "wait", "fetch")
		frame.WaitUntil = due
		if cmd.Operation == "cancel" {
			cancels++
			frame.Status = "done"
		}
		if cmd.Operation == "evidence" {
			evidence++
		}
		return frame, nil
	})
	due = env.Now().Add(time.Hour)
	env.RegisterDelayedCallback(env.CancelWorkflow, time.Minute)
	env.ExecuteWorkflow(DocumentIngestV2, s)
	if env.GetWorkflowError() == nil || cancels != 1 || evidence != 0 {
		t.Fatalf("error %v cancels %d evidence %d", env.GetWorkflowError(), cancels, evidence)
	}
}

func TestDocumentIngestV2WorkerLossRetriesSameEnvelope(t *testing.T) {
	s := intakeTestStateV2()
	attempts := 0
	env := intakeTestEnvironmentV2(s, func(cmd IntakeCommandV2) (IntakeFrameV2, error) {
		if cmd.State.Work != s.Work || cmd.Operation != "resume" {
			return IntakeFrameV2{}, intakeFailureV2("changed_retry", false)
		}
		attempts++
		if attempts < s.Config.InfrastructureTries {
			return IntakeFrameV2{}, temporalsdk.NewApplicationError("synthetic worker loss", "infrastructure")
		}
		frame := intakeTestFrameV2(s, "stopped", "")
		frame.Receipt, frame.Reason = nil, "execution_failed"
		return frame, nil
	})
	env.ExecuteWorkflow(DocumentIngestV2, s)
	if err := env.GetWorkflowError(); err != nil || attempts != s.Config.InfrastructureTries {
		t.Fatalf("error %v attempts %d", err, attempts)
	}
}

func TestDocumentIngestV2RejectsGateBypass(t *testing.T) {
	s := intakeTestStateV2()
	env := intakeTestEnvironmentV2(s, func(IntakeCommandV2) (IntakeFrameV2, error) {
		return intakeTestFrameV2(s, "accepted", ""), nil
	})
	env.ExecuteWorkflow(DocumentIngestV2, s)
	if env.GetWorkflowError() == nil {
		t.Fatal("workflow accepted evidence without ApplyEvidence")
	}
}

func TestDocumentIngestV2DayTwoDecisionHasNoSourceDeadline(t *testing.T) {
	s := intakeTestStateV2()
	s.Config.Reconcile = 12 * time.Hour
	var start time.Time
	resolved, gates := false, 0
	env := intakeTestEnvironmentV2(s, func(cmd IntakeCommandV2) (IntakeFrameV2, error) {
		f := intakeTestFrameV2(s, "progress", "")
		switch cmd.Operation {
		case "resume":
			f.Next = "gate"
		case "gate":
			gates++
			if resolved {
				f.Next = "candidate"
			} else {
				f.Next = "quarantine"
			}
		case "quarantine":
			f.Status, f.DecisionEpoch, f.ReportHash, f.QuarantineAt = "quarantined", 1, s.Work.Manifest, start
		case "expiry", "reminder":
			f.Status, f.Next, f.WaitUntil = "wait", "resume", start.Add(3*24*time.Hour)
		case "decision":
			resolved, f.Next = true, "normalize"
		case "normalize":
			f.Next = "structure"
		case "structure":
			f.Next = "gate"
		case "candidate":
			f.Next = "evidence"
		case "evidence":
			f.Status = "accepted"
		case "cancel":
			t.Fatal("source deadline cancelled day-two document")
		default:
			t.Fatalf("unexpected operation %s", cmd.Operation)
		}
		if !cmd.State.SourceDeadline.IsZero() {
			t.Fatal("document inherited source deadline")
		}
		return f, nil
	})
	start = env.Now()
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(QuarantineDecisionSignalV2, IntakeDecisionV2{DecisionRef: *intakeTestFrameV2(s, "done", "").Receipt,
			ExpectedEpoch: 1, ReportHash: s.Work.Manifest})
	}, 2*24*time.Hour)
	env.ExecuteWorkflow(DocumentIngestV2, s)
	if err := env.GetWorkflowError(); err != nil || !resolved || gates != 2 {
		t.Fatalf("day-two decision: %v resolved %v gates %d", err, resolved, gates)
	}
}

func TestDocumentIngestV2ContinuationCarriesOnlyFrozenReference(t *testing.T) {
	s := intakeTestStateV2()
	s.Config.Signals = 1
	checkpoints := 0
	var due time.Time
	env := intakeTestEnvironmentV2(s, func(cmd IntakeCommandV2) (IntakeFrameV2, error) {
		if cmd.State.Work != s.Work || !cmd.State.SourceDeadline.IsZero() {
			t.Fatal("document continuation changed budget reference or inherited deadline")
		}
		if cmd.Operation == "checkpoint" {
			checkpoints++
			return intakeTestFrameV2(s, "progress", ""), nil
		}
		f := intakeTestFrameV2(s, "wait", "resume")
		f.WaitUntil = due
		return f, nil
	})
	due = env.Now().Add(time.Hour)
	env.RegisterDelayedCallback(func() { env.SignalWorkflow(IntakeReceiptSignalV2, *intakeTestFrameV2(s, "done", "").Receipt) }, time.Minute)
	env.ExecuteWorkflow(DocumentIngestV2, s)
	if env.GetWorkflowError() == nil || checkpoints != 1 {
		t.Fatalf("continuation error %v checkpoints %d", env.GetWorkflowError(), checkpoints)
	}
}

func TestDocumentIngestV2RetainedDecisionResumesAfterRefusalWithoutSignal(t *testing.T) {
	for _, refusal := range []run.AdmissionRefusal{run.AdmissionNotTurn, run.AdmissionProcessingFull} {
		t.Run(string(refusal), func(t *testing.T) {
			s, cp, admission, activities := localFixtureV2()
			quarantineCheckpointV2(t, cp)
			admission.r.State, admission.r.WaitReason, admission.r.LifecycleReceipt = run.AdmissionOpenOnly, "quarantine", "admission-"+strings.Repeat("b", 32)
			admission.refusal = refusal
			resumes, validations, gates, expiryChecks := 0, 0, 0, 0
			activities.ResumeValidation = resumeFakeV2(func(run.IntakeWork, run.AdmissionResolution, *IntakeDecisionV2) error {
				validations++
				return nil
			})
			admission.close = func(run.AdmissionCloseRequest) error {
				expiryChecks++
				return run.ErrAdmissionConflict
			}
			activities.Operations = domainFakeV2(func(cmd IntakeCommandV2) (IntakeAdvanceResultV2, error) {
				f := intakeTestFrameV2(s, "progress", "")
				switch cmd.Operation {
				case "normalize":
					f.Next = "structure"
				case "structure":
					f.Next = "gate"
				case "gate":
					gates++
					f.Next = "candidate"
				default:
					t.Fatalf("unexpected processing operation %s", cmd.Operation)
				}
				return IntakeAdvanceResultV2{Frame: f}, nil
			})
			var start time.Time
			env := intakeTestEnvironmentV2(s, func(cmd IntakeCommandV2) (IntakeFrameV2, error) {
				if cmd.Operation == "candidate" {
					return intakeTestFrameV2(s, "progress", "evidence"), nil
				}
				if cmd.Operation == "evidence" {
					return intakeTestFrameV2(s, "accepted", ""), nil
				}
				// Use the real admission wrapper for decision, refusal and periodic expiry.
				f, err := activities.advance(context.Background(), cmd)
				if cmd.Operation == "decision" || cmd.Operation == "expiry" {
					resumes++
					if f.Status == "wait" {
						f.WaitUntil = start.Add(s.Config.Reconcile)
					}
				}
				return f, err
			})
			start = env.Now()
			cp.current.QuarantineAt = start
			env.RegisterDelayedCallback(func() {
				env.SignalWorkflow(QuarantineDecisionSignalV2, IntakeDecisionV2{DecisionRef: *intakeTestFrameV2(s, "done", "").Receipt,
					ExpectedEpoch: 1, ReportHash: s.Work.Manifest})
			}, time.Minute)
			env.RegisterDelayedCallback(func() { admission.refusal = "" }, 2*time.Minute)
			env.ExecuteWorkflow(DocumentIngestV2, s)
			if err := env.GetWorkflowError(); err != nil || resumes != 2 || validations != 4 || gates != 1 || expiryChecks != 1 {
				t.Fatalf("periodic resume: %v resumes %d validations %d gates %d expiry %d", err, resumes, validations, gates, expiryChecks)
			}
		})
	}
}
