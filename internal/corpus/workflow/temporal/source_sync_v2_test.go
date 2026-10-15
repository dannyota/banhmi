package temporal

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/converter"
	temporalsdk "go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"danny.vn/banhmi/internal/corpus/run"
)

func sourceTestStateV2() IntakeStateV2 {
	s := intakeTestStateV2()
	s.Kind, s.WorkflowID = run.SourceSync, "source-sync/vn/source/manual/request"
	return s
}

func TestSourceSyncV2ActivityQueueWaitUsesRemainingOriginalDeadline(t *testing.T) {
	s := sourceTestStateV2()
	env := intakeTestEnvironmentV2(s, func(IntakeCommandV2) (IntakeFrameV2, error) {
		t.Fatal("options inspection dispatched activity")
		return IntakeFrameV2{}, nil
	})
	env.ExecuteWorkflow(func(ctx workflow.Context, state IntakeStateV2) (IntakeCompletionV2, error) {
		state.SourceDeadline = workflow.Now(ctx).Add(time.Minute)
		opts, err := intakeActivityOptionsV2(ctx, state, "discover")
		if err != nil || opts.ScheduleToCloseTimeout != time.Minute || opts.StartToCloseTimeout != time.Minute {
			t.Fatalf("queue/running bound: %+v %v", opts, err)
		}
		if err := workflow.Sleep(ctx, 40*time.Second); err != nil {
			return IntakeCompletionV2{}, err
		}
		opts, err = intakeActivityOptionsV2(ctx, state, "resume")
		if err != nil || opts.ScheduleToCloseTimeout != 20*time.Second || opts.StartToCloseTimeout != 20*time.Second {
			t.Fatalf("remaining original deadline: %+v %v", opts, err)
		}
		if err := workflow.Sleep(ctx, 20*time.Second); err != nil {
			return IntakeCompletionV2{}, err
		}
		if _, err := intakeActivityOptionsV2(ctx, state, "discover"); err == nil {
			t.Fatal("expired source could schedule fresh work")
		}
		for _, cleanup := range []string{"cancel", "started", "reconnect", "checkpoint"} {
			opts, err := intakeActivityOptionsV2(ctx, state, cleanup)
			if err != nil || opts.ScheduleToCloseTimeout != state.Config.ControlAttempt*time.Duration(state.Config.InfrastructureTries) {
				t.Fatalf("expired source blocked cleanup %s: %v", cleanup, err)
			}
		}
		state.Kind, state.SourceDeadline = run.DocumentIngest, time.Time{}
		opts, err = intakeActivityOptionsV2(ctx, state, "fetch")
		if err != nil || opts.ScheduleToCloseTimeout != 0 || opts.StartToCloseTimeout != state.Config.WorkAttempt {
			t.Fatal("document inherited source activity deadline")
		}
		return IntakeCompletionV2{Outcome: "idle"}, nil
	}, s)
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
}

func TestSourceSyncV2DelayedResponseCannotLaunchAfterDeadline(t *testing.T) {
	s := sourceTestStateV2()
	s.Config.SourceDeadline = time.Minute
	cancelled, children := 0, 0
	env := intakeTestEnvironmentV2(s, func(cmd IntakeCommandV2) (IntakeFrameV2, error) {
		t.Fatalf("unexpected unmocked source operation %s", cmd.Operation)
		return IntakeFrameV2{}, nil
	})
	f := intakeTestFrameV2(s, "progress", "discover")
	f.Launches = []IntakeLaunchV2{{WorkflowID: "document-ingest/vn/version/attempt", VersionKey: "version", AttemptKey: "attempt", Work: s.Work}}
	env.OnActivity(IntakeAdvanceNameV2, mock.Anything, mock.MatchedBy(func(cmd IntakeCommandV2) bool {
		return cmd.Operation == "resume"
	})).After(2*time.Minute).Return(f, nil).Once()
	env.OnActivity(IntakeAdvanceNameV2, mock.Anything, mock.MatchedBy(func(cmd IntakeCommandV2) bool {
		return cmd.Operation == "cancel"
	})).Run(func(mock.Arguments) { cancelled++ }).Return(intakeTestFrameV2(s, "done", ""), nil).Once()
	env.RegisterWorkflowWithOptions(func(workflow.Context, IntakeStateV2) (IntakeCompletionV2, error) {
		children++
		return IntakeCompletionV2{Outcome: "stopped"}, nil
	}, workflow.RegisterOptions{Name: DocumentIngestNameV2})
	env.ExecuteWorkflow(SourceSyncV2, s)
	if env.GetWorkflowError() == nil || children != 0 || cancelled != 1 {
		t.Fatalf("late response launched: %v children %d cancellation %d", env.GetWorkflowError(), children, cancelled)
	}
	env.AssertExpectations(t)
}

func TestSourceSyncV2ExpiredGroupDoesNotStartFreshChildren(t *testing.T) {
	s := sourceTestStateV2()
	children := 0
	env := intakeTestEnvironmentV2(s, func(IntakeCommandV2) (IntakeFrameV2, error) {
		t.Fatal("expired empty group attempted acknowledgement")
		return IntakeFrameV2{}, nil
	})
	env.RegisterWorkflowWithOptions(func(workflow.Context, IntakeStateV2) (IntakeCompletionV2, error) {
		children++
		return IntakeCompletionV2{Outcome: "stopped"}, nil
	}, workflow.RegisterOptions{Name: DocumentIngestNameV2})
	env.ExecuteWorkflow(func(ctx workflow.Context, state IntakeStateV2) (IntakeCompletionV2, error) {
		state.SourceDeadline = workflow.Now(ctx)
		launch := IntakeLaunchV2{WorkflowID: "document-ingest/vn/version/attempt", VersionKey: "version", AttemptKey: "attempt", Work: state.Work}
		err := intakeStartGroupV2(ctx, &state, []IntakeLaunchV2{launch})
		if err == nil {
			t.Fatal("expired group returned permission")
		}
		return IntakeCompletionV2{Outcome: "stopped"}, nil
	}, s)
	if err := env.GetWorkflowError(); err != nil || children != 0 {
		t.Fatalf("expired group: %v children %d", err, children)
	}
}

func TestSourceSyncV2DeadlineWinsRolloverBoundary(t *testing.T) {
	s := sourceTestStateV2()
	s.Config.SourceDeadline = time.Minute
	var due time.Time
	cancelled, checkpoints := 0, 0
	env := intakeTestEnvironmentV2(s, func(cmd IntakeCommandV2) (IntakeFrameV2, error) {
		f := intakeTestFrameV2(s, "wait", "resume")
		f.WaitUntil = due
		if cmd.Operation == "cancel" {
			cancelled++
			f.Status = "done"
		} else if cmd.Operation == "checkpoint" {
			checkpoints++
			f.Status = "progress"
		}
		return f, nil
	})
	due = env.Now().Add(time.Minute)
	env.RegisterDelayedCallback(func() {
		env.SetCurrentHistoryLength(s.Config.HistoryEvents - s.Config.HistoryEventReserve)
	}, time.Minute)
	env.ExecuteWorkflow(SourceSyncV2, s)
	err := env.GetWorkflowError()
	if err == nil || !strings.Contains(err.Error(), "intake_deadline") || cancelled != 1 || checkpoints != 0 {
		t.Fatalf("deadline/rollover priority: %v cancel %d checkpoint %d", err, cancelled, checkpoints)
	}
}

func TestSourceSyncV2CancellationDoesNotPropagateToChildren(t *testing.T) {
	s := sourceTestStateV2()
	started, cancelled, acknowledgements := 0, 0, 0
	env := intakeTestEnvironmentV2(s, func(cmd IntakeCommandV2) (IntakeFrameV2, error) {
		f := intakeTestFrameV2(s, "progress", "")
		switch cmd.Operation {
		case "resume":
			f.Next = "discover"
		case "discover":
			f.Next = "decode"
		case "decode":
			f.Next = "discovery"
		case "discovery":
			for i := 0; i < 16; i++ {
				version := fmt.Sprintf("version%d", i)
				f.Launches = append(f.Launches, IntakeLaunchV2{WorkflowID: "document-ingest/vn/" + version + "/attempt", VersionKey: version, AttemptKey: "attempt", Work: s.Work})
			}
			f.Next = "resume"
		case "started":
			acknowledgements++
			f.Status = "done"
		case "cancel":
			f.Status = "done"
		default:
			t.Fatalf("unexpected operation %s", cmd.Operation)
		}
		return f, nil
	})
	env.RegisterWorkflowWithOptions(func(ctx workflow.Context, _ IntakeStateV2) (IntakeCompletionV2, error) {
		err := workflow.Sleep(ctx, 8*24*time.Hour)
		if temporalsdk.IsCanceledError(err) {
			cancelled++
		}
		return IntakeCompletionV2{Outcome: "stopped"}, err
	}, workflow.RegisterOptions{Name: DocumentIngestNameV2})
	env.SetOnChildWorkflowStartedListener(func(_ *workflow.Info, _ workflow.Context, _ converter.EncodedValues) {
		started++
		if started == 1 {
			env.CancelWorkflow()
		}
	})
	env.ExecuteWorkflow(SourceSyncV2, s)
	if env.GetWorkflowError() == nil || started == 0 || acknowledgements != started || cancelled != 0 {
		t.Fatalf("cancellation: %v started %d ack %d cancelled children %d", env.GetWorkflowError(), started, acknowledgements, cancelled)
	}
}

func TestSourceSyncV2DuplicateStartReconcilesExactID(t *testing.T) {
	s := sourceTestStateV2()
	acks, reconnects := 0, 0
	env := intakeTestEnvironmentV2(s, func(cmd IntakeCommandV2) (IntakeFrameV2, error) {
		f := intakeTestFrameV2(s, "progress", "")
		switch cmd.Operation {
		case "resume":
			if acks == 2 {
				f.Status = "done"
			} else {
				f.Next = "discover"
			}
		case "discover":
			f.Next = "decode"
		case "decode":
			f.Next = "discovery"
		case "discovery":
			f.Launches = []IntakeLaunchV2{{WorkflowID: "document-ingest/vn/version/attempt", VersionKey: "version", AttemptKey: "attempt", Work: s.Work}}
			f.Next = "resume"
		case "reconnect":
			reconnects++
			if cmd.Launch.WorkflowID != "document-ingest/vn/version/attempt" {
				t.Fatal("lost start recovery changed ID")
			}
			if reconnects == 1 {
				return f, temporalsdk.NewApplicationError("synthetic lost start receipt", "infrastructure")
			}
			f.Status = "done"
		case "started":
			acks++
			f.Status = "done"
		default:
			t.Fatalf("unexpected operation %s", cmd.Operation)
		}
		return f, nil
	})
	env.RegisterWorkflowWithOptions(func(ctx workflow.Context, _ IntakeStateV2) (IntakeCompletionV2, error) {
		return IntakeCompletionV2{Outcome: "stopped"}, workflow.Sleep(ctx, 8*24*time.Hour)
	}, workflow.RegisterOptions{Name: DocumentIngestNameV2})
	env.ExecuteWorkflow(SourceSyncV2, s)
	if err := env.GetWorkflowError(); err != nil || acks != 2 || reconnects != 2 {
		t.Fatalf("duplicate start: %v acks %d reconnects %d", err, acks, reconnects)
	}
}

func TestSourceSyncV2ChildStartAcknowledgementRequiresConfirmation(t *testing.T) {
	childDuplicate := &temporalsdk.ChildWorkflowExecutionAlreadyStartedError{}
	serviceDuplicate := serviceerror.NewWorkflowExecutionAlreadyStarted("Workflow execution already started", "", "existing-run")
	for _, tc := range []struct {
		name            string
		startErr        error
		reconnectStatus string
		reconnectErr    error
		wantOperations  string
		wantError       string
	}{
		{name: "started", wantOperations: "[started]"},
		{name: "child_duplicate", startErr: childDuplicate, wantOperations: "[reconnect started]"},
		{name: "wrapped_child_duplicate", startErr: fmt.Errorf("start receipt: %w", childDuplicate), wantOperations: "[reconnect started]"},
		{name: "service_duplicate", startErr: serviceDuplicate, wantOperations: "[reconnect started]"},
		{name: "wrapped_service_duplicate", startErr: fmt.Errorf("start receipt: %w", serviceDuplicate), wantOperations: "[reconnect started]"},
		{name: "unrelated_error", startErr: errors.New("Workflow execution already started"), wantOperations: "[]", wantError: "Workflow execution already started"},
		{name: "application_error", startErr: temporalsdk.NewApplicationError("Workflow execution already started", "infrastructure"), wantOperations: "[]", wantError: "Workflow execution already started"},
		{name: "unconfirmed_duplicate", startErr: serviceDuplicate, reconnectStatus: "progress", wantOperations: "[reconnect]", wantError: "unconfirmed_child_start"},
		{name: "failed_reconnect", startErr: childDuplicate, reconnectErr: intakeFailureV2("reconnect_unavailable", false), wantOperations: "[reconnect]", wantError: "reconnect_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := sourceTestStateV2()
			launch := IntakeLaunchV2{WorkflowID: "document-ingest/vn/version/attempt", VersionKey: "version", AttemptKey: "attempt", Work: s.Work}
			var operations []string
			env := intakeTestEnvironmentV2(s, func(cmd IntakeCommandV2) (IntakeFrameV2, error) {
				if cmd.Launch == nil || *cmd.Launch != launch {
					t.Fatal("child start acknowledgement changed launch identity")
				}
				operations = append(operations, cmd.Operation)
				frame := intakeTestFrameV2(s, "done", "")
				switch cmd.Operation {
				case "reconnect":
					if tc.reconnectStatus != "" {
						frame.Status = tc.reconnectStatus
					}
					return frame, tc.reconnectErr
				case "started":
					return frame, nil
				default:
					t.Fatalf("unexpected child start operation %s", cmd.Operation)
					return IntakeFrameV2{}, nil
				}
			})
			env.ExecuteWorkflow(func(ctx workflow.Context, state IntakeStateV2) (IntakeCompletionV2, error) {
				if err := intakeBeginV2(ctx, &state, run.SourceSync); err != nil {
					return IntakeCompletionV2{}, err
				}
				return IntakeCompletionV2{Outcome: "stopped"}, intakeRecordChildStartV2(ctx, &state, launch, tc.startErr)
			}, s)
			err := env.GetWorkflowError()
			if (err != nil) != (tc.wantError != "") || (err != nil && !strings.Contains(err.Error(), tc.wantError)) || fmt.Sprint(operations) != tc.wantOperations {
				t.Fatalf("child start: error %v operations %v, want error %q operations %s", err, operations, tc.wantError, tc.wantOperations)
			}
		})
	}
}

func TestSourceSyncV2AcknowledgesGroupsWithoutWaitingForChildren(t *testing.T) {
	s := sourceTestStateV2()
	launched, acknowledged, discovered, largest, completed := 0, 0, 0, 0, 0
	env := intakeTestEnvironmentV2(s, func(cmd IntakeCommandV2) (IntakeFrameV2, error) {
		frame := intakeTestFrameV2(s, "progress", "")
		switch cmd.Operation {
		case "resume":
			if discovered == 35 {
				frame.Status = "done"
			} else {
				frame.Next = "discover"
			}
		case "discover":
			if acknowledged != discovered {
				return frame, intakeFailureV2("previous_group_unacknowledged", false)
			}
			frame.Next = "decode"
		case "decode":
			frame.Next = "discovery"
		case "discovery":
			count := min(s.Config.ChildGroup, 35-discovered)
			largest = max(largest, count)
			for i := 0; i < count; i++ {
				version := fmt.Sprintf("version%d", discovered+i)
				frame.Launches = append(frame.Launches, IntakeLaunchV2{WorkflowID: "document-ingest/vn/" + version + "/attempt",
					VersionKey: version, AttemptKey: "attempt", Work: s.Work})
			}
			discovered += count
			frame.Next = "resume"
		case "started":
			if launched <= acknowledged || cmd.Launch == nil {
				return frame, intakeFailureV2("ack_before_start", false)
			}
			acknowledged++
			frame.Status = "done"
		default:
			return frame, intakeFailureV2("unexpected_source_operation", false)
		}
		return frame, nil
	})
	// Measure parent completion; the SDK otherwise waits for detached children.
	env.SetDetachedChildWait(false)
	env.RegisterWorkflowWithOptions(func(ctx workflow.Context, _ IntakeStateV2) (IntakeCompletionV2, error) {
		err := workflow.Sleep(ctx, 8*24*time.Hour)
		completed++
		return IntakeCompletionV2{Outcome: "stopped"}, err
	}, workflow.RegisterOptions{Name: DocumentIngestNameV2})
	env.SetOnChildWorkflowStartedListener(func(_ *workflow.Info, _ workflow.Context, _ converter.EncodedValues) {
		launched++
	})
	start := env.Now()
	env.ExecuteWorkflow(SourceSyncV2, s)
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if launched != 35 || acknowledged != 35 || largest != 16 || completed != 0 || env.Now().Sub(start) >= 24*time.Hour {
		t.Fatalf("launched %d acknowledged %d group %d completed %d elapsed %s", launched, acknowledged, largest, completed, env.Now().Sub(start))
	}
}

func TestSourceSyncV2RolloverCheckpointsCommittedPage(t *testing.T) {
	s := sourceTestStateV2()
	s.Config.Pages = 1
	checkpoints, pages := 0, 0
	var deadline time.Time
	env := intakeTestEnvironmentV2(s, func(cmd IntakeCommandV2) (IntakeFrameV2, error) {
		frame := intakeTestFrameV2(s, "progress", "")
		switch cmd.Operation {
		case "resume":
			frame.Next = "discover"
		case "discover":
			frame.Next = "decode"
		case "decode":
			frame.Next = "discovery"
		case "discovery":
			pages++
			frame.Next = "resume"
		case "checkpoint":
			checkpoints++
			if !cmd.State.SourceDeadline.Equal(deadline) {
				t.Fatal("source continuation reset deadline")
			}
		default:
			return frame, intakeFailureV2("unexpected_operation", false)
		}
		return frame, nil
	})
	deadline = env.Now().Add(s.Config.SourceDeadline)
	env.ExecuteWorkflow(SourceSyncV2, s)
	var continuation *workflow.ContinueAsNewError
	if !errors.As(env.GetWorkflowError(), &continuation) || checkpoints != 1 || pages != 1 {
		t.Fatalf("error %v checkpoints %d pages %d", env.GetWorkflowError(), checkpoints, pages)
	}
	var continued IntakeStateV2
	if err := converter.GetDefaultDataConverter().FromPayloads(continuation.Input, &continued); err != nil {
		t.Fatal(err)
	}
	if !continued.SourceDeadline.Equal(deadline) || continued.Phase != "resume" {
		t.Fatal("source continuation changed committed page progress or original deadline")
	}
}

func TestSourceSyncV2ReconcilerCannotFetch(t *testing.T) {
	s := sourceTestStateV2()
	s.ReconcileOnly = true
	fetched := 0
	env := intakeTestEnvironmentV2(s, func(cmd IntakeCommandV2) (IntakeFrameV2, error) {
		if cmd.Operation == "discover" {
			fetched++
		}
		return intakeTestFrameV2(s, "progress", "discover"), nil
	})
	env.ExecuteWorkflow(SourceSyncV2, s)
	if env.GetWorkflowError() == nil || fetched != 0 {
		t.Fatalf("error %v fetches %d", env.GetWorkflowError(), fetched)
	}
}

func TestSourceSyncV2DeadlinePreservesAbandonChild(t *testing.T) {
	s := sourceTestStateV2()
	s.Config.SourceDeadline = time.Minute
	started, acknowledged, cancels := 0, 0, 0
	var due time.Time
	env := intakeTestEnvironmentV2(s, func(cmd IntakeCommandV2) (IntakeFrameV2, error) {
		f := intakeTestFrameV2(s, "progress", "")
		switch cmd.Operation {
		case "resume":
			if acknowledged == 0 {
				f.Next = "discover"
			} else {
				f.Status, f.Next, f.WaitUntil = "wait", "resume", due
			}
		case "discover":
			f.Next = "decode"
		case "decode":
			f.Next = "discovery"
		case "discovery":
			f.Launches = []IntakeLaunchV2{{WorkflowID: "document-ingest/vn/version/attempt", VersionKey: "version", AttemptKey: "attempt", Work: s.Work}}
			f.Next = "resume"
		case "started":
			acknowledged++
			f.Status = "done"
		case "cancel":
			cancels++
			f.Status = "done"
		default:
			t.Fatalf("unexpected source operation %s", cmd.Operation)
		}
		return f, nil
	})
	due = env.Now().Add(24 * time.Hour)
	env.RegisterWorkflowWithOptions(func(ctx workflow.Context, child IntakeStateV2) (IntakeCompletionV2, error) {
		if !child.SourceDeadline.IsZero() {
			t.Fatal("ABANDON child inherited source deadline")
		}
		return IntakeCompletionV2{Outcome: "stopped"}, workflow.Sleep(ctx, 8*24*time.Hour)
	}, workflow.RegisterOptions{Name: DocumentIngestNameV2})
	env.SetOnChildWorkflowStartedListener(func(_ *workflow.Info, _ workflow.Context, _ converter.EncodedValues) { started++ })
	env.ExecuteWorkflow(SourceSyncV2, s)
	if env.GetWorkflowError() == nil || started != 1 || acknowledged != 1 || cancels != 1 {
		t.Fatalf("source expiry: error %v started %d acknowledged %d cancel %d", env.GetWorkflowError(), started, acknowledged, cancels)
	}
}

func TestSourceSyncV2HundredStartsRotateAfterAllAcknowledgements(t *testing.T) {
	s := sourceTestStateV2()
	issued, acks, checkpoints := 0, 0, 0
	env := intakeTestEnvironmentV2(s, func(cmd IntakeCommandV2) (IntakeFrameV2, error) {
		f := intakeTestFrameV2(s, "progress", "")
		switch cmd.Operation {
		case "resume":
			f.Next = "discover"
		case "discover":
			if issued != acks {
				t.Fatal("new group before prior acknowledgement")
			}
			f.Next = "decode"
		case "decode":
			f.Next = "discovery"
		case "discovery":
			for i := 0; i < cmd.LaunchLimit; i++ {
				version := fmt.Sprintf("version%d", issued)
				f.Launches = append(f.Launches, IntakeLaunchV2{WorkflowID: "document-ingest/vn/" + version + "/attempt", VersionKey: version, AttemptKey: "attempt", Work: s.Work})
				issued++
			}
			f.Next = "resume"
		case "started":
			acks++
			f.Status = "done"
		case "checkpoint":
			checkpoints++
			if issued != 100 || acks != 100 {
				t.Fatal("rollover lost pending start acknowledgement")
			}
		default:
			t.Fatalf("unexpected operation %s", cmd.Operation)
		}
		return f, nil
	})
	env.RegisterWorkflowWithOptions(func(ctx workflow.Context, _ IntakeStateV2) (IntakeCompletionV2, error) {
		return IntakeCompletionV2{Outcome: "stopped"}, workflow.Sleep(ctx, 8*24*time.Hour)
	}, workflow.RegisterOptions{Name: DocumentIngestNameV2})
	env.ExecuteWorkflow(SourceSyncV2, s)
	if env.GetWorkflowError() == nil || issued != 100 || acks != 100 || checkpoints != 1 {
		t.Fatalf("start rollover: error %v issued %d ack %d checkpoint %d", env.GetWorkflowError(), issued, acks, checkpoints)
	}
}
