package temporal

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	temporalsdk "go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"danny.vn/banhmi/internal/corpus/run"
)

const shortActivityAttempt = time.Minute

type cleanupFailureDetail struct {
	Code    string
	Message string
}

// CorpusRefresh coordinates one country build. Durable activities own every
// database, filesystem, provider, clock, and random operation.
func CorpusRefresh(ctx workflow.Context, state State) (result WorkflowResult, err error) {
	if validateErr := state.Validate(); validateErr != nil {
		return WorkflowResult{}, workflowNonRetryable("invalid_workflow_state", "validate corpus refresh state", validateErr)
	}
	info := workflow.GetInfo(ctx)
	if info.ContinuedExecutionRunID == "" {
		if validateErr := state.validateNewStart(); validateErr != nil {
			return WorkflowResult{}, workflowNonRetryable("invalid_workflow_start", "validate new corpus refresh", validateErr)
		}
	}
	wantWorkflowID := WorkflowID(state.Country, state.BuildID)
	if got := info.WorkflowExecution.ID; got != wantWorkflowID {
		return WorkflowResult{}, workflowNonRetryable("invalid_workflow_identity", "validate workflow ID",
			fmt.Errorf("workflow ID %q does not match deterministic ID %q", got, wantWorkflowID))
	}
	_ = workflow.GetVersion(ctx, workflowChangeIDV1, workflow.DefaultVersion, workflow.Version(workflowVersionV1))
	if state.Phase == "" {
		state.Phase = fixedStages[0]
	}

	defer func() {
		if state.ActiveJob == nil || !temporalsdk.IsCanceledError(err) {
			return
		}
		cleanupCtx, cancel := workflow.NewDisconnectedContext(ctx)
		defer cancel()
		cleanupCtx = withActivityOptions(cleanupCtx, ExternalTaskQueue, time.Minute, time.Minute)
		request := CleanupRequest{
			Country:        state.Country,
			BuildID:        state.BuildID,
			Stage:          state.ActiveJob.Shard.Stage,
			SubmissionKey:  state.ActiveJob.SubmissionKey,
			ProviderJobRef: state.ActiveJob.ProviderJobRef,
			Reason:         "workflow canceled",
		}
		var cleanup CleanupResult
		cleanupErr := workflow.ExecuteActivity(cleanupCtx, CleanupExternalActivityName, request).Get(cleanupCtx, &cleanup)
		if cleanupErr != nil || !cleanup.CancellationRecorded {
			detail := cleanupFailureDetail{Code: "external_cancellation_not_recorded", Message: "cleanup did not record cancellation"}
			if cleanupErr != nil {
				detail = cleanupFailureDetail{Code: "external_cleanup_failed", Message: boundedErrorMessage(cleanupErr)}
			}
			err = temporalsdk.NewCanceledErrorWithOptions(temporalsdk.CanceledErrorOptions{
				Message: "corpus refresh canceled; external cleanup failed",
				Cause:   err,
				Details: []any{detail},
			})
		}
	}()

	for {
		if !workflow.Now(ctx).Before(state.Deadline) {
			return WorkflowResult{}, workflowNonRetryable("refresh_deadline_exceeded", "run corpus refresh",
				fmt.Errorf("deadline exceeded"))
		}
		if shouldContinueAsNew(ctx, state) {
			state.ScansThisRun = 0
			return WorkflowResult{}, workflow.NewContinueAsNewError(ctx, WorkflowNameV1, state)
		}
		if state.ActiveJob != nil {
			var complete bool
			var stageResult run.Result
			stageResult, complete, err = advanceExternal(ctx, &state)
			if err != nil {
				return WorkflowResult{}, err
			}
			if complete {
				if err = acceptResult(&state, stageResult); err != nil {
					return WorkflowResult{}, err
				}
			}
			continue
		}
		if state.Phase == run.Activate {
			var activation ActivationSummary
			activityCtx, optionsErr := newActivityContext(ctx, state, ExclusiveTaskQueue, true)
			if optionsErr != nil {
				return WorkflowResult{}, optionsErr
			}
			request := ActivationActivityRequest{Activation: state.Activation, MaxPayloadBytes: state.MaxPayloadBytes}
			if err = workflow.ExecuteActivity(activityCtx, ActivateActivityName, request).Get(activityCtx, &activation); err != nil {
				return WorkflowResult{}, err
			}
			if validateErr := validateActivationSummary(state.Activation, activation, state.MaxPayloadBytes); validateErr != nil {
				return WorkflowResult{}, workflowNonRetryable("invalid_activation_result", "validate activation result", validateErr)
			}
			return WorkflowResult{
				Country:    state.Country,
				RunID:      state.RunID,
				BuildID:    state.BuildID,
				Counts:     state.Counts,
				Activation: activation,
			}, nil
		}
		if state.Scan >= int64(state.MaxStageScans) {
			return WorkflowResult{}, workflowNonRetryable("stage_scan_budget_exhausted", "prepare corpus stage",
				fmt.Errorf("stage %s exceeded its durable scan budget", state.Phase))
		}

		prepared, prepareErr := prepareStage(ctx, state)
		if prepareErr != nil {
			return WorkflowResult{}, prepareErr
		}
		state.Scan++
		state.ScansThisRun++
		if prepared.BlockingGaps > 0 {
			return WorkflowResult{}, workflowNonRetryable(errorTypeBlockingGaps, "prepare corpus stage",
				fmt.Errorf("stage %s has %d unresolved blocking gaps: %w", state.Phase, prepared.BlockingGaps, ErrBlockingGaps))
		}
		if !prepared.HasShard {
			if prepared.NextEligible != nil {
				if err = waitUntil(ctx, *prepared.NextEligible, state.Deadline); err != nil {
					return WorkflowResult{}, err
				}
				state.ScansThisRun++
				continue
			}
			if prepared.Exhausted {
				if err = advanceStage(&state); err != nil {
					return WorkflowResult{}, err
				}
				continue
			}
			return WorkflowResult{}, workflowNonRetryable("stage_no_progress", "prepare corpus stage",
				fmt.Errorf("stage %s preparation made no progress", state.Phase))
		}

		state.ManifestRef = prepared.Shard.ManifestRef
		if isExternalStage(state.Phase) {
			if err = prepareExternal(ctx, &state, prepared.Shard); err != nil {
				return WorkflowResult{}, err
			}
			continue
		}
		stageResult, executeErr := executeDirect(ctx, state, prepared.Shard)
		state.ScansThisRun++
		if executeErr != nil {
			return WorkflowResult{}, executeErr
		}
		if err = acceptResult(&state, stageResult); err != nil {
			return WorkflowResult{}, err
		}
		if stageResult.NextEligible != nil {
			if err = waitUntil(ctx, *stageResult.NextEligible, state.Deadline); err != nil {
				return WorkflowResult{}, err
			}
			state.ScansThisRun++
		}
	}
}

func prepareStage(ctx workflow.Context, state State) (PreparedStage, error) {
	request := PrepareRequest{
		Country:             state.Country,
		RunID:               state.RunID,
		BuildID:             state.BuildID,
		Stage:               state.Phase,
		Recipe:              state.Recipe,
		Cutoff:              state.Cutoff,
		Cursor:              state.Cursor,
		PreviousManifestRef: state.ManifestRef,
		Scan:                state.Scan,
		Budget:              state.Budget,
		MaxPayloadBytes:     state.MaxPayloadBytes,
	}
	activityCtx, err := newActivityContext(ctx, state, taskQueue(state.Phase), true)
	if err != nil {
		return PreparedStage{}, err
	}
	var prepared PreparedStage
	if err := workflow.ExecuteActivity(activityCtx, PrepareStageActivityName, request).Get(activityCtx, &prepared); err != nil {
		return PreparedStage{}, err
	}
	return prepared, nil
}

func executeDirect(ctx workflow.Context, state State, shard run.Shard) (run.Result, error) {
	activityCtx, err := newActivityContext(ctx, state, taskQueue(state.Phase), false)
	if err != nil {
		return run.Result{}, err
	}
	request := ExecuteStageRequest{Shard: shard, MaxOutputRefs: state.MaxOutputRefs, MaxPayloadBytes: state.MaxPayloadBytes}
	var result run.Result
	if err := workflow.ExecuteActivity(activityCtx, ExecuteStageActivityName, request).Get(activityCtx, &result); err != nil {
		return run.Result{}, err
	}
	return result, nil
}

func prepareExternal(ctx workflow.Context, state *State, shard run.Shard) error {
	activityCtx, err := newActivityContext(ctx, *state, ExternalTaskQueue, true)
	if err != nil {
		return err
	}
	request := ExternalPrepareRequest{
		Shard: shard, SubmissionKey: SubmissionKey(shard),
		MaxPayloadBytes: state.MaxPayloadBytes,
	}
	var job ExternalJob
	if err := workflow.ExecuteActivity(activityCtx, PrepareExternalActivityName, request).Get(activityCtx, &job); err != nil {
		return err
	}
	state.ActiveJob = &job
	state.Fence = job.Shard.Execution.LeaseFence
	state.ScansThisRun++
	if err := validateCarriedJob(*state); err != nil {
		return workflowNonRetryable("invalid_external_job", "validate prepared external job", err)
	}
	return nil
}

func advanceExternal(ctx workflow.Context, state *State) (run.Result, bool, error) {
	job := *state.ActiveJob
	switch job.State {
	case ExternalPrepared:
		activityCtx, err := newActivityContext(ctx, *state, ExternalTaskQueue, true)
		if err != nil {
			return run.Result{}, false, err
		}
		request := ExternalSubmitRequest{Job: job, MaxPayloadBytes: state.MaxPayloadBytes}
		if err := workflow.ExecuteActivity(activityCtx, SubmitExternalActivityName, request).Get(activityCtx, &job); err != nil {
			return run.Result{}, false, err
		}
		state.ActiveJob = &job
		state.Fence = job.Shard.Execution.LeaseFence
		state.ScansThisRun++
		return run.Result{}, false, validateActiveJobWorkflow(*state)
	case ExternalSubmitted, ExternalRunning:
		if err := waitForExternalPoll(ctx, *state, job); err != nil {
			return run.Result{}, false, err
		}
		state.ScansThisRun++
		activityCtx, err := newActivityContext(ctx, *state, ExternalTaskQueue, true)
		if err != nil {
			return run.Result{}, false, err
		}
		request := ExternalCheckRequest{Job: job, MaxPayloadBytes: state.MaxPayloadBytes}
		if err := workflow.ExecuteActivity(activityCtx, CheckExternalActivityName, request).Get(activityCtx, &job); err != nil {
			return run.Result{}, false, err
		}
		state.ActiveJob = &job
		state.Fence = job.Shard.Execution.LeaseFence
		state.ScansThisRun++
		return run.Result{}, false, validateActiveJobWorkflow(*state)
	case ExternalSucceeded:
		activityCtx, err := newActivityContext(ctx, *state, ExternalTaskQueue, false)
		if err != nil {
			return run.Result{}, false, err
		}
		request := ExternalApplyRequest{Job: job, MaxOutputRefs: state.MaxOutputRefs, MaxPayloadBytes: state.MaxPayloadBytes}
		var result run.Result
		if err := workflow.ExecuteActivity(activityCtx, ApplyExternalActivityName, request).Get(activityCtx, &result); err != nil {
			return run.Result{}, false, err
		}
		state.ActiveJob = nil
		state.ScansThisRun++
		return result, true, nil
	case ExternalUncertain:
		return run.Result{}, false, workflowNonRetryable(errorTypeUncertainSubmission, "resume external job", ErrUncertainSubmission)
	case ExternalFailed:
		return run.Result{}, false, workflowNonRetryable("external_job_failed", "resume external job",
			fmt.Errorf("external %s job failed", job.Shard.Stage))
	default:
		return run.Result{}, false, workflowNonRetryable("invalid_external_job", "resume external job",
			fmt.Errorf("external %s job has state %q", job.Shard.Stage, job.State))
	}
}

func validateActiveJobWorkflow(state State) error {
	if state.ActiveJob != nil && (state.ActiveJob.State == ExternalUncertain || state.ActiveJob.State == ExternalFailed) {
		if state.ActiveJob.State == ExternalUncertain {
			return workflowNonRetryable(errorTypeUncertainSubmission, "reconcile external submission", ErrUncertainSubmission)
		}
		return workflowNonRetryable("external_job_failed", "check external job",
			fmt.Errorf("external %s job failed", state.ActiveJob.Shard.Stage))
	}
	if err := validateCarriedJob(state); err != nil {
		return workflowNonRetryable("invalid_external_job", "validate external job continuation", err)
	}
	return nil
}

func waitForExternalPoll(ctx workflow.Context, state State, job ExternalJob) error {
	next := job.NextCheck
	minimum := workflow.Now(ctx).Add(state.PollInterval)
	if next.IsZero() || next.Before(minimum) {
		next = minimum
	}
	return waitUntil(ctx, next, state.Deadline)
}

func acceptResult(state *State, result run.Result) error {
	if err := validateResultPayload(result, state.MaxOutputRefs, state.MaxPayloadBytes); err != nil {
		return workflowNonRetryable("invalid_stage_result", "validate stage result", err)
	}
	if result.Shard.Execution.Country != state.Country || result.Shard.Execution.RunID != state.RunID ||
		result.Shard.Execution.BuildID != state.BuildID || result.Shard.Stage != state.Phase ||
		result.Shard.Recipe != state.Recipe || result.Shard.Budget != state.Budget ||
		result.Shard.Cursor != state.Cursor {
		return workflowNonRetryable("invalid_stage_result", "validate stage result",
			fmt.Errorf("stage result identity differs from workflow"))
	}
	if result.Shard.ManifestRef != state.ManifestRef {
		return workflowNonRetryable("invalid_stage_result", "validate stage result",
			fmt.Errorf("stage result manifest differs from prepared input"))
	}
	if !result.Complete && result.NextEligible == nil && result.NextCursor == "" {
		return workflowNonRetryable("stage_no_progress", "validate stage result",
			fmt.Errorf("incomplete stage result has no continuation or eligibility time"))
	}
	updated := state.Counts
	if err := updated.add(result.Counts); err != nil {
		return workflowNonRetryable("stage_count_overflow", "add stage result counts", err)
	}
	state.Fence = result.Shard.Execution.LeaseFence
	state.Cursor = result.NextCursor
	state.ManifestRef = result.Shard.ManifestRef
	state.OutputRefs = append([]run.OutputRef(nil), result.Outputs...)
	state.Counts = updated
	return nil
}

func advanceStage(state *State) error {
	index := stageIndex(state.Phase)
	if index < 0 || index+1 >= len(fixedStages) {
		return workflowNonRetryable("invalid_workflow_phase", "advance corpus stage",
			fmt.Errorf("cannot advance unknown or final stage %q", state.Phase))
	}
	state.Phase = fixedStages[index+1]
	state.Cursor = ""
	state.ManifestRef = ""
	state.Fence = 0
	state.OutputRefs = nil
	state.Scan = 0
	return nil
}

func shouldContinueAsNew(ctx workflow.Context, state State) bool {
	if state.ScansThisRun == 0 {
		return false
	}
	return state.ScansThisRun >= state.MaxScansPerRun || workflow.GetInfo(ctx).GetContinueAsNewSuggested()
}

func waitUntil(ctx workflow.Context, when, deadline time.Time) error {
	if !when.Before(deadline) {
		return workflowNonRetryable("refresh_deadline_exceeded", "wait for durable eligibility",
			fmt.Errorf("next eligibility is outside the refresh deadline"))
	}
	delay := when.Sub(workflow.Now(ctx))
	if delay <= 0 {
		return nil
	}
	return workflow.NewTimer(ctx, delay).Get(ctx, nil)
}

func newActivityContext(ctx workflow.Context, state State, queue string, short bool) (workflow.Context, error) {
	remaining := state.Deadline.Sub(workflow.Now(ctx))
	if remaining <= 0 {
		return nil, workflowNonRetryable("refresh_deadline_exceeded", "schedule activity",
			fmt.Errorf("no refresh time remains"))
	}
	schedule := min(remaining, state.Budget.MaxElapsed)
	start := schedule
	if short {
		start = min(start, shortActivityAttempt)
	}
	return withActivityOptions(ctx, queue, schedule, start), nil
}

func withActivityOptions(ctx workflow.Context, queue string, schedule, start time.Duration) workflow.Context {
	return workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		TaskQueue:              queue,
		ScheduleToCloseTimeout: schedule,
		StartToCloseTimeout:    start,
		HeartbeatTimeout:       min(start, 30*time.Second),
		WaitForCancellation:    true,
		RetryPolicy: &temporalsdk.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2,
			MaximumInterval:    30 * time.Second,
			MaximumAttempts:    3,
		},
	})
}

func workflowNonRetryable(errorType, operation string, cause error) error {
	return temporalsdk.NewNonRetryableApplicationError(boundedFailureMessage(operation, cause), errorType, nil)
}

func boundedFailureMessage(operation string, err error) string {
	message := operation + ": " + boundedErrorMessage(err)
	return boundedUTF8(message, maxFailureMessageBytes)
}

func boundedErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	return boundedUTF8(err.Error(), maxFailureDetailBytes)
}

func boundedUTF8(value string, limit int) string {
	value = strings.ToValidUTF8(value, "\uFFFD")
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
