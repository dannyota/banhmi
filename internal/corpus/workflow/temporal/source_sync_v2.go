package temporal

import (
	"errors"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	temporalsdk "go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"danny.vn/banhmi/internal/corpus/run"
)

// SourceSyncV2 accounts bounded discovery pages and durable child intents.
// Success means source-window accounting, never document acceptance. A
// reconciliation-only execution cannot call discovery or fetch activities.
func SourceSyncV2(ctx workflow.Context, s IntakeStateV2) (receipt IntakeCompletionV2, err error) {
	if err = intakeBeginV2(ctx, &s, run.SourceSync); err != nil {
		return receipt, err
	}
	defer intakeCancellationV2(ctx, &s, &err)
	pages, starts := 0, 0
	progressed := false
	for {
		if ctx.Err() != nil {
			return receipt, ctx.Err()
		}
		if sourceDeadlineReachedV2(ctx, s) {
			_, err = intakeCallV2(ctx, s, "cancel", nil, nil, nil)
			if err != nil {
				return receipt, err
			}
			return receipt, intakeFailureV2("intake_deadline", false)
		}
		if intakeRotateV2(ctx, s, 0) || pages >= s.Config.Pages || starts >= s.Config.ChildStarts {
			return receipt, intakeContinueV2(ctx, s, SourceSyncNameV2, progressed)
		}
		if s.ReconcileOnly && s.Phase != "resume" && s.Phase != "control" {
			return receipt, intakeFailureV2("reconciliation_cannot_fetch", false)
		}
		frame, callErr := intakeCallLimitV2(ctx, s, s.Phase, s.Config.ChildStarts-starts)
		if callErr != nil {
			if sourceDeadlineReachedV2(ctx, s) {
				_, cleanupErr := intakeCallV2(ctx, s, "cancel", nil, nil, nil)
				if cleanupErr != nil {
					return receipt, cleanupErr
				}
			}
			return receipt, callErr
		}
		if len(frame.Notifications) != 0 || len(frame.Launches) > s.Config.ChildStarts-starts {
			return receipt, intakeFailureV2("source_command_limit", false)
		}
		s.Work, receipt = frame.Work, intakeCompletionV2(frame)
		progressed = true
		if sourceDeadlineReachedV2(ctx, s) {
			_, err = intakeCallV2(ctx, s, "cancel", nil, nil, nil)
			if err != nil {
				return receipt, err
			}
			return receipt, intakeFailureV2("intake_deadline", false)
		}
		if err = intakeStartGroupV2(ctx, &s, frame.Launches); err != nil {
			if sourceDeadlineReachedV2(ctx, s) {
				if _, cleanupErr := intakeCallV2(ctx, s, "cancel", nil, nil, nil); cleanupErr != nil {
					return receipt, cleanupErr
				}
			}
			return receipt, err
		}
		starts += len(frame.Launches)
		if s.Phase == "discovery" {
			pages++
		}
		if frame.Status == "done" || frame.Status == "terminal_gap" {
			return receipt, nil
		}
		if frame.Status == "wait" {
			if err = workflow.Sleep(ctx, intakeDelayV2(ctx, frame.WaitUntil, s.SourceDeadline)); err != nil {
				return receipt, err
			}
		} else if frame.Status != "progress" {
			return receipt, intakeFailureV2("invalid_source_transition", false)
		}
		if !sourceTransitionV2(s.Phase, frame.Next, s.ReconcileOnly) {
			return receipt, intakeFailureV2("invalid_source_transition", false)
		}
		s.Phase = frame.Next
	}
}

func sourceTransitionV2(from, to string, reconcile bool) bool {
	if reconcile {
		return to == "resume" || to == "control"
	}
	switch from {
	case "resume", "control":
		return to == "resume" || to == "discover" || to == "decode" || to == "discovery"
	case "discover":
		return to == "decode" || to == "resume"
	case "decode":
		return to == "discovery"
	case "discovery":
		return to == "resume" || to == "discover"
	default:
		return false
	}
}

func intakeStartGroupV2(ctx workflow.Context, s *IntakeStateV2, launches []IntakeLaunchV2) error {
	if len(launches) > s.Config.ChildGroup {
		return intakeFailureV2("child_group_limit", false)
	}
	futures := make([]workflow.ChildWorkflowFuture, 0, len(launches))
	// ABANDON alone does not stop SDK context cancellation propagation.
	// Durable per-attempt controls, not parent context cancellation, stop children.
	launchCtx, _ := workflow.NewDisconnectedContext(ctx)
	for _, launch := range launches {
		if ctx.Err() != nil || sourceDeadlineReachedV2(ctx, *s) {
			break
		}
		child := IntakeStateV2{Country: s.Country, WorkflowID: launch.WorkflowID, Kind: run.DocumentIngest,
			Work: launch.Work, Config: s.Config}
		childCtx := workflow.WithChildOptions(launchCtx, workflow.ChildWorkflowOptions{
			WorkflowID: launch.WorkflowID, TaskQueue: CoordinatorTaskQueueV2,
			ParentClosePolicy: enumspb.PARENT_CLOSE_POLICY_ABANDON,
			WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
		})
		futures = append(futures, workflow.ExecuteChildWorkflow(childCtx, DocumentIngestNameV2, child))
	}
	ackCtx, cancel := workflow.NewDisconnectedContext(ctx)
	defer cancel()
	// Await every start before acknowledging any group completion or rolling
	// over. Never wait for child completion, including quarantined children.
	for i, future := range futures {
		var execution workflow.Execution
		if err := future.GetChildWorkflowExecution().Get(ackCtx, &execution); err != nil {
			var duplicate *temporalsdk.ChildWorkflowExecutionAlreadyStartedError
			if !errors.As(err, &duplicate) {
				return err
			}
			confirmed, reconnectErr := intakeCallV2(ackCtx, *s, "reconnect", &launches[i], nil, nil)
			if reconnectErr != nil {
				return reconnectErr
			}
			if confirmed.Status != "done" {
				return intakeFailureV2("unconfirmed_child_start", false)
			}
		}
		frame, err := intakeCallV2(ackCtx, *s, "started", &launches[i], nil, nil)
		if err != nil {
			return err
		}
		s.Work = frame.Work
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if sourceDeadlineReachedV2(ctx, *s) {
		return intakeFailureV2("intake_deadline", false)
	}
	return nil
}

func intakeBeginV2(ctx workflow.Context, s *IntakeStateV2, kind run.WorkflowKind) error {
	if s.Validate() != nil || s.Kind != kind || workflow.GetInfo(ctx).WorkflowExecution.ID != s.WorkflowID {
		return intakeFailureV2("invalid_intake_workflow", false)
	}
	if workflow.GetInfo(ctx).ContinuedExecutionRunID == "" && (s.Phase != "" || !s.ExpiresAt.IsZero() || !s.SourceDeadline.IsZero()) {
		return intakeFailureV2("injected_intake_progress", false)
	}
	if s.Phase == "" {
		s.Phase = "resume"
	}
	if kind == run.SourceSync && s.SourceDeadline.IsZero() {
		s.SourceDeadline = workflow.Now(ctx).Add(s.Config.SourceDeadline)
	}
	return nil
}

func intakeCallV2(ctx workflow.Context, s IntakeStateV2, op string, launch *IntakeLaunchV2, notification *IntakeNotificationV2, decision *IntakeDecisionV2) (IntakeFrameV2, error) {
	return intakeExecuteV2(ctx, IntakeCommandV2{State: s, Operation: op, Launch: launch, Notification: notification, Decision: decision})
}

func intakeCallLimitV2(ctx workflow.Context, s IntakeStateV2, op string, remaining int) (IntakeFrameV2, error) {
	if remaining > s.Config.ChildGroup {
		remaining = s.Config.ChildGroup
	}
	return intakeExecuteV2(ctx, IntakeCommandV2{State: s, Operation: op, LaunchLimit: remaining})
}

func intakeExecuteV2(ctx workflow.Context, cmd IntakeCommandV2) (IntakeFrameV2, error) {
	s := cmd.State
	if intakePayloadV2(cmd, s.Config) != nil {
		return IntakeFrameV2{}, intakeFailureV2("intake_payload_limit", false)
	}
	opts, err := intakeActivityOptionsV2(ctx, s, cmd.Operation)
	if err != nil {
		return IntakeFrameV2{}, err
	}
	ctx = workflow.WithActivityOptions(ctx, opts)
	var frame IntakeFrameV2
	err = workflow.ExecuteActivity(ctx, IntakeAdvanceNameV2, cmd).Get(ctx, &frame)
	if err != nil {
		return frame, err
	}
	if validateFrameV2(frame, s) != nil {
		return IntakeFrameV2{}, intakeFailureV2("invalid_intake_response", false)
	}
	return frame, nil
}

func sourceDeadlineReachedV2(ctx workflow.Context, s IntakeStateV2) bool {
	return s.Kind == run.SourceSync && !s.SourceDeadline.IsZero() && !workflow.Now(ctx).Before(s.SourceDeadline)
}

func intakeActivityOptionsV2(ctx workflow.Context, s IntakeStateV2, op string) (workflow.ActivityOptions, error) {
	attempt := s.Config.WorkAttempt
	if intakeQueueV2(op) == DatabaseTaskQueueV2 {
		attempt = s.Config.ControlAttempt
	}
	opts := workflow.ActivityOptions{TaskQueue: intakeQueueV2(op), StartToCloseTimeout: attempt,
		HeartbeatTimeout: s.Config.HeartbeatTimeout, WaitForCancellation: true,
		RetryPolicy: &temporalsdk.RetryPolicy{InitialInterval: s.Config.RetryInitial,
			BackoffCoefficient: 2, MaximumInterval: s.Config.RetryMaximum, MaximumAttempts: int32(s.Config.InfrastructureTries)}}
	cleanup := op == "cancel" || op == "started" || op == "reconnect" || op == "checkpoint"
	if s.Kind == run.SourceSync && cleanup {
		// Cleanup may outlive the original source deadline, but queue wait is
		// still bounded by its configured infrastructure attempt allowance.
		opts.ScheduleToCloseTimeout = s.Config.ControlAttempt*time.Duration(s.Config.InfrastructureTries)
	} else if s.Kind == run.SourceSync {
		remaining := s.SourceDeadline.Sub(workflow.Now(ctx))
		if remaining <= 0 {
			return workflow.ActivityOptions{}, intakeFailureV2("intake_deadline", false)
		}
		// ScheduleToClose includes queue wait and every infrastructure retry.
		opts.ScheduleToCloseTimeout = remaining
		opts.StartToCloseTimeout = min(attempt, remaining)
		opts.HeartbeatTimeout = min(opts.HeartbeatTimeout, opts.StartToCloseTimeout)
	}
	return opts, nil
}

func intakeRotateV2(ctx workflow.Context, s IntakeStateV2, signals int) bool {
	i := workflow.GetInfo(ctx)
	// Reserve headroom for one bounded activity/command group and checkpoint.
	// Actual SDK overhead remains an M1.14 captured-history gate.
	return i.GetContinueAsNewSuggested() || i.GetCurrentHistoryLength() >= s.Config.HistoryEvents-s.Config.HistoryEventReserve ||
		i.GetCurrentHistorySize() >= s.Config.HistoryBytes-s.Config.TransactionBytes || signals >= s.Config.Signals
}

func intakeContinueV2(ctx workflow.Context, s IntakeStateV2, name string, progressed bool) error {
	if !progressed {
		return intakeFailureV2("intake_bootstrap_history_limit", false)
	}
	frame, err := intakeCallV2(ctx, s, "checkpoint", nil, nil, nil)
	if err != nil {
		return err
	}
	s.Work = frame.Work
	if s.Validate() != nil {
		return intakeFailureV2("invalid_intake_continuation", false)
	}
	// No signal handlers run concurrently. Unconsumed inbox entries remain
	// unacknowledged in the ledger for redelivery to the next run.
	return workflow.NewContinueAsNewError(ctx, name, s)
}

func intakeCancellationV2(ctx workflow.Context, s *IntakeStateV2, result *error) {
	if *result == nil || (ctx.Err() == nil && !temporalsdk.IsCanceledError(*result)) {
		return
	}
	cleanup, cancel := workflow.NewDisconnectedContext(ctx)
	defer cancel()
	if _, err := intakeCallV2(cleanup, *s, "cancel", nil, nil, nil); err != nil {
		*result = intakeFailureV2("intake_cancel_reconciliation_required", false)
	}
}

func intakeDelayV2(ctx workflow.Context, due, deadline time.Time) time.Duration {
	if !deadline.IsZero() && due.After(deadline) {
		due = deadline
	}
	delay := due.Sub(workflow.Now(ctx))
	if delay < 0 {
		return 0
	}
	return delay
}

func intakeCompletionV2(f IntakeFrameV2) IntakeCompletionV2 {
	return IntakeCompletionV2{Outcome: f.Status, Reason: f.Reason, Receipt: f.Receipt}
}
