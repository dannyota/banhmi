package temporal

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/activity"
	temporalsdk "go.temporal.io/sdk/temporal"

	"danny.vn/banhmi/internal/corpus/release"
	"danny.vn/banhmi/internal/corpus/run"
)

// Activities adapts durable application ports to Temporal activities. It does
// not own a database, provider client, or in-memory job store.
type Activities struct {
	Stages            StagePreparer
	Direct            *run.Direct
	External          ExternalJobs
	Activator         ActivationService
	HeartbeatInterval time.Duration
}

func (a *Activities) validate() error {
	if a == nil {
		return fmt.Errorf("activities are required")
	}
	if a.Stages == nil || a.Direct == nil || a.External == nil || a.Activator == nil {
		return fmt.Errorf("durable stage, direct runner, external job, and activation ports are required")
	}
	if a.HeartbeatInterval <= 0 {
		return fmt.Errorf("heartbeat interval must be positive")
	}
	return nil
}

func (a *Activities) PrepareStage(ctx context.Context, request PrepareRequest) (PreparedStage, error) {
	if err := a.validate(); err != nil {
		return PreparedStage{}, nonRetryableActivity("invalid_activity_configuration", "validate activities", err)
	}
	if err := validatePrepareRequest(request); err != nil {
		return PreparedStage{}, nonRetryableActivity("invalid_prepare_request", "validate stage preparation", err)
	}
	prepared, err := heartbeatCall(ctx, a.HeartbeatInterval, request.Stage, func() (PreparedStage, error) {
		return a.Stages.Prepare(ctx, request)
	})
	if err != nil {
		return PreparedStage{}, durableActivityError(fmt.Sprintf("prepare %s stage", request.Stage), err)
	}
	if err := validatePreparedStage(request, prepared); err != nil {
		return PreparedStage{}, nonRetryableActivity("invalid_prepared_stage", "validate prepared stage", err)
	}
	return prepared, nil
}

func validatePreparedStage(request PrepareRequest, prepared PreparedStage) error {
	if prepared.BlockingGaps < 0 {
		return fmt.Errorf("blocking gap count must not be negative")
	}
	if request.MaxPayloadBytes <= 0 || request.MaxPayloadBytes > maxPayloadBytesHard {
		return fmt.Errorf("invalid payload byte limit")
	}
	if prepared.Exhausted && prepared.HasShard {
		return fmt.Errorf("exhausted stage must not include a shard")
	}
	if prepared.Exhausted && prepared.NextEligible != nil {
		return fmt.Errorf("exhausted stage must not have future eligibility")
	}
	if prepared.HasShard && prepared.NextEligible != nil {
		return fmt.Errorf("prepared shard must not also defer eligibility")
	}
	if !prepared.HasShard {
		if prepared.Shard != (run.Shard{}) {
			return fmt.Errorf("preparation without work must not include shard data")
		}
		if !prepared.Exhausted && prepared.NextEligible == nil && prepared.BlockingGaps == 0 {
			return fmt.Errorf("empty preparation must report exhaustion, delay, or blocking gaps")
		}
		return nil
	}
	shard := prepared.Shard
	if err := validateFenceZeroShardPayload(shard, request.MaxPayloadBytes); err != nil {
		return fmt.Errorf("prepared shard: %w", err)
	}
	if shard.Execution.Country != request.Country || shard.Execution.RunID != request.RunID ||
		shard.Execution.BuildID != request.BuildID || shard.Stage != request.Stage ||
		shard.Recipe != request.Recipe || shard.Budget != request.Budget || shard.Cursor != request.Cursor {
		return fmt.Errorf("prepared shard identity differs from request")
	}
	if shard.ManifestRef == "" {
		return fmt.Errorf("prepared shard requires an immutable manifest reference")
	}
	if shard.ManifestRef == request.PreviousManifestRef {
		return fmt.Errorf("later eligibility scan must create a new immutable manifest")
	}
	return nil
}

func (a *Activities) ExecuteStage(ctx context.Context, request ExecuteStageRequest) (run.Result, error) {
	if err := a.validate(); err != nil {
		return run.Result{}, nonRetryableActivity("invalid_activity_configuration", "validate activities", err)
	}
	shard := request.Shard
	if err := validateFenceZeroShardPayload(shard, request.MaxPayloadBytes); err != nil {
		return run.Result{}, nonRetryableActivity("invalid_stage_shard", "execute stage", err)
	}
	if err := validateOutputLimit(request.MaxOutputRefs); err != nil {
		return run.Result{}, nonRetryableActivity("invalid_stage_shard", "execute stage", err)
	}
	result, err := heartbeatCall(ctx, a.HeartbeatInterval, shard.Stage, func() (run.Result, error) {
		result, err := a.Direct.Execute(ctx, shard)
		if err != nil {
			return run.Result{}, durableActivityError(fmt.Sprintf("execute %s stage", shard.Stage), err)
		}
		return result, nil
	})
	if err != nil {
		return run.Result{}, err
	}
	if err := validateResultPayload(result, request.MaxOutputRefs, request.MaxPayloadBytes); err != nil {
		return run.Result{}, nonRetryableActivity("invalid_stage_result", "validate stage result payload", err)
	}
	return result, nil
}

func (a *Activities) PrepareExternal(ctx context.Context, request ExternalPrepareRequest) (ExternalJob, error) {
	if err := a.validate(); err != nil {
		return ExternalJob{}, nonRetryableActivity("invalid_activity_configuration", "validate activities", err)
	}
	if err := validateFenceZeroShardPayload(request.Shard, request.MaxPayloadBytes); err != nil {
		return ExternalJob{}, nonRetryableActivity("invalid_external_job", "prepare external job", err)
	}
	if !isExternalStage(request.Shard.Stage) {
		return ExternalJob{}, nonRetryableActivity("invalid_external_job", "prepare external job",
			fmt.Errorf("stage %q is not external", request.Shard.Stage))
	}
	if request.SubmissionKey != SubmissionKey(request.Shard) {
		return ExternalJob{}, nonRetryableActivity("invalid_external_job", "prepare external job",
			fmt.Errorf("submission key does not match immutable shard"))
	}
	job, err := heartbeatCall(ctx, a.HeartbeatInterval, request.Shard.Stage, func() (ExternalJob, error) {
		return a.External.Prepare(ctx, request)
	})
	if err != nil {
		return ExternalJob{}, durableActivityError(fmt.Sprintf("prepare external %s job", request.Shard.Stage), err)
	}
	if err := validateExternalJob(request.Shard, request.SubmissionKey, job, request.MaxPayloadBytes); err != nil {
		return ExternalJob{}, nonRetryableActivity("invalid_external_job", "validate prepared external job", err)
	}
	if job.State != ExternalPrepared {
		return ExternalJob{}, nonRetryableActivity("invalid_external_job", "validate prepared external job",
			fmt.Errorf("state %q is not prepared", job.State))
	}
	return job, nil
}

func (a *Activities) SubmitExternal(ctx context.Context, request ExternalSubmitRequest) (ExternalJob, error) {
	if err := a.validate(); err != nil {
		return ExternalJob{}, nonRetryableActivity("invalid_activity_configuration", "validate activities", err)
	}
	job := request.Job
	if err := validateExternalJob(job.Shard, job.SubmissionKey, job, request.MaxPayloadBytes); err != nil {
		return ExternalJob{}, nonRetryableActivity("invalid_external_job", "validate external submission", err)
	}
	if job.State != ExternalPrepared {
		return ExternalJob{}, nonRetryableActivity("invalid_external_job", "validate external submission",
			fmt.Errorf("state %q is not prepared", job.State))
	}
	updated, err := heartbeatCall(ctx, a.HeartbeatInterval, job.Shard.Stage, func() (ExternalJob, error) {
		return a.External.SubmitOrReconcile(ctx, job)
	})
	if err != nil {
		return ExternalJob{}, durableActivityError(fmt.Sprintf("submit or reconcile external %s job", job.Shard.Stage), err)
	}
	if err := validateExternalJob(job.Shard, job.SubmissionKey, updated, request.MaxPayloadBytes); err != nil {
		return ExternalJob{}, nonRetryableActivity("invalid_external_job", "validate submitted external job", err)
	}
	if updated.State == ExternalPrepared {
		return ExternalJob{}, nonRetryableActivity("external_job_no_progress", "validate submitted external job",
			fmt.Errorf("submission remained prepared"))
	}
	return updated, nil
}

func (a *Activities) CheckExternal(ctx context.Context, request ExternalCheckRequest) (ExternalJob, error) {
	if err := a.validate(); err != nil {
		return ExternalJob{}, nonRetryableActivity("invalid_activity_configuration", "validate activities", err)
	}
	if err := validateExternalJob(request.Job.Shard, request.Job.SubmissionKey, request.Job, request.MaxPayloadBytes); err != nil {
		return ExternalJob{}, nonRetryableActivity("invalid_external_job", "validate external status request", err)
	}
	if request.Job.State != ExternalSubmitted && request.Job.State != ExternalRunning {
		return ExternalJob{}, nonRetryableActivity("invalid_external_job", "validate external status request",
			fmt.Errorf("state %q cannot be checked", request.Job.State))
	}
	updated, err := heartbeatCall(ctx, a.HeartbeatInterval, request.Job.Shard.Stage, func() (ExternalJob, error) {
		return a.External.Check(ctx, request)
	})
	if err != nil {
		return ExternalJob{}, durableActivityError(fmt.Sprintf("check external %s job", request.Job.Shard.Stage), err)
	}
	if err := validateExternalJob(request.Job.Shard, request.Job.SubmissionKey, updated, request.MaxPayloadBytes); err != nil {
		return ExternalJob{}, nonRetryableActivity("invalid_external_job", "validate checked external job", err)
	}
	if err := validateCheckedExternalIdentity(request.Job, updated); err != nil {
		return ExternalJob{}, nonRetryableActivity("invalid_external_job", "validate checked external job",
			err)
	}
	if updated.State == ExternalPrepared ||
		(request.Job.State == ExternalRunning && updated.State == ExternalSubmitted) {
		return ExternalJob{}, nonRetryableActivity("invalid_external_job", "validate checked external job",
			fmt.Errorf("external job state moved backward from %q to %q", request.Job.State, updated.State))
	}
	return updated, nil
}

func (a *Activities) ApplyExternal(ctx context.Context, request ExternalApplyRequest) (run.Result, error) {
	if err := a.validate(); err != nil {
		return run.Result{}, nonRetryableActivity("invalid_activity_configuration", "validate activities", err)
	}
	if err := validateExternalJob(request.Job.Shard, request.Job.SubmissionKey, request.Job, request.MaxPayloadBytes); err != nil {
		return run.Result{}, nonRetryableActivity("invalid_external_job", "validate external apply", err)
	}
	if request.Job.State != ExternalSucceeded {
		return run.Result{}, nonRetryableActivity("invalid_external_job", "apply external job",
			fmt.Errorf("state %q is not successful", request.Job.State))
	}
	if err := validateOutputLimit(request.MaxOutputRefs); err != nil {
		return run.Result{}, nonRetryableActivity("invalid_stage_result", "apply external job", err)
	}
	result, err := heartbeatCall(ctx, a.HeartbeatInterval, request.Job.Shard.Stage, func() (run.Result, error) {
		return a.External.Apply(ctx, request)
	})
	if err != nil {
		return run.Result{}, durableActivityError(fmt.Sprintf("apply external %s job", request.Job.Shard.Stage), err)
	}
	if err := validateResultPayload(result, request.MaxOutputRefs, request.MaxPayloadBytes); err != nil {
		return run.Result{}, nonRetryableActivity("invalid_stage_result", "validate external result", err)
	}
	if result.Shard != request.Job.Shard {
		return run.Result{}, nonRetryableActivity("invalid_stage_result", "validate external result",
			fmt.Errorf("external apply returned another shard"))
	}
	return result, nil
}

func (a *Activities) CleanupExternal(ctx context.Context, request CleanupRequest) (CleanupResult, error) {
	if err := a.validate(); err != nil {
		return CleanupResult{}, nonRetryableActivity("invalid_activity_configuration", "validate activities", err)
	}
	if !validCountry(request.Country) || request.BuildID == uuid.Nil || !isExternalStage(request.Stage) ||
		request.SubmissionKey == "" || request.Reason == "" ||
		len(request.SubmissionKey)+len(request.ProviderJobRef)+len(request.Reason) > maxPayloadBytesHard {
		return CleanupResult{}, nonRetryableActivity("invalid_cleanup_request", "validate external cleanup",
			fmt.Errorf("cleanup identity or payload is invalid"))
	}
	result, err := heartbeatCall(ctx, a.HeartbeatInterval, request.Stage, func() (CleanupResult, error) {
		return a.External.Cleanup(ctx, request)
	})
	if err != nil {
		return result, durableActivityError("record cancellation and attempt provider cancellation", err)
	}
	if !result.CancellationRecorded {
		return result, nonRetryableActivity("invalid_cleanup_result", "validate external cleanup",
			fmt.Errorf("cancellation was not recorded"))
	}
	return result, nil
}

func (a *Activities) Activate(ctx context.Context, input ActivationActivityRequest) (ActivationSummary, error) {
	if err := a.validate(); err != nil {
		return ActivationSummary{}, nonRetryableActivity("invalid_activity_configuration", "validate activities", err)
	}
	request := input.Activation
	if !validCountry(request.Country) || request.BuildID == uuid.Nil || request.ReleaseID == "" || request.ActivatedBy == "" ||
		request.ExpectedSelectionVersion < 0 || request.ExpectedSelectionVersion == math.MaxInt64 ||
		input.MaxPayloadBytes <= 0 || input.MaxPayloadBytes > maxPayloadBytesHard ||
		len(request.Country)+len(request.ReleaseID)+len(request.ExpectedCurrentReleaseID)+len(request.ActivatedBy) > input.MaxPayloadBytes {
		return ActivationSummary{}, nonRetryableActivity("invalid_activation_request", "validate activation request",
			fmt.Errorf("activation identity, version, or payload is invalid"))
	}
	result, err := heartbeatCall(ctx, a.HeartbeatInterval, request.ReleaseID, func() (release.ActivationResult, error) {
		return a.Activator.Activate(ctx, request)
	})
	if err != nil {
		return ActivationSummary{}, durableActivityError("activate release", err)
	}
	summary := ActivationSummary{
		Country:                  result.Selection.Country,
		BuildID:                  result.BuildID,
		Outcome:                  result.Outcome,
		SelectionVersion:         result.Selection.Version,
		CurrentReleaseID:         result.Selection.CurrentReleaseID,
		PreviousReleaseID:        result.Selection.PreviousReleaseID,
		LocalReconciliationIssue: result.LocalReconciliationError != nil,
	}
	if err := validateActivationSummary(request, summary, input.MaxPayloadBytes); err != nil {
		return ActivationSummary{}, nonRetryableActivity("invalid_activation_result", "validate activation result", err)
	}
	return summary, nil
}

func validateExternalJob(shard run.Shard, submissionKey string, job ExternalJob, maxPayloadBytes int) error {
	if maxPayloadBytes <= 0 || maxPayloadBytes > maxPayloadBytesHard {
		return fmt.Errorf("invalid payload byte limit")
	}
	if submissionKey != SubmissionKey(shard) {
		return fmt.Errorf("external job submission key is not derived from immutable shard")
	}
	if submissionKey == "" || job.SubmissionKey != submissionKey {
		return fmt.Errorf("external job submission key differs")
	}
	if run.IdempotencyKey(job.Shard) != run.IdempotencyKey(shard) {
		return fmt.Errorf("external job immutable shard differs")
	}
	if job.Shard.Execution.LeaseFence <= 0 {
		return fmt.Errorf("external job fence must be positive")
	}
	if err := job.Shard.Validate(); err != nil {
		return fmt.Errorf("external job shard: %w", err)
	}
	if err := validateShardPayload(job.Shard, maxPayloadBytes); err != nil {
		return fmt.Errorf("external job shard: %w", err)
	}
	if !validExternalState(job.State) {
		return fmt.Errorf("external job has unknown state %q", job.State)
	}
	switch job.State {
	case ExternalPrepared:
		if job.ProviderJobRef != "" || job.ResultRef != "" {
			return fmt.Errorf("prepared external job has provider output")
		}
	case ExternalSubmitted, ExternalRunning:
		if job.ProviderJobRef == "" || job.ResultRef != "" {
			return fmt.Errorf("pending external job has invalid provider references")
		}
	case ExternalSucceeded:
		if job.ProviderJobRef == "" || job.ResultRef == "" {
			return fmt.Errorf("successful external job requires provider and result references")
		}
	}
	if len(job.SubmissionKey) > maxPayloadStringBytes || len(job.ProviderJobRef) > maxPayloadStringBytes ||
		len(job.ResultRef) > maxPayloadStringBytes {
		return fmt.Errorf("external job contains an oversized workflow payload string")
	}
	if externalJobPayloadBytes(job) > maxPayloadBytes {
		return fmt.Errorf("external job exceeds workflow payload byte limit")
	}
	return nil
}

func validateCheckedExternalIdentity(before, after ExternalJob) error {
	if after.ProviderJobRef != before.ProviderJobRef ||
		(before.ResultRef != "" && after.ResultRef != before.ResultRef) {
		return fmt.Errorf("external provider job identity changed")
	}
	return nil
}

func validateResultPayload(result run.Result, maxOutputRefs, maxPayloadBytes int) error {
	if err := validateOutputLimit(maxOutputRefs); err != nil {
		return err
	}
	if maxPayloadBytes <= 0 || maxPayloadBytes > maxPayloadBytesHard {
		return fmt.Errorf("invalid payload byte limit")
	}
	if err := result.Validate(); err != nil {
		return err
	}
	if len(result.Outputs) > maxOutputRefs {
		return fmt.Errorf("output reference count exceeds limit")
	}
	if len(result.NextCursor) > maxPayloadStringBytes || len(result.GapAcceptanceRef) > maxPayloadStringBytes {
		return fmt.Errorf("stage result contains an oversized workflow payload string")
	}
	for i, output := range result.Outputs {
		if len(output.Ref) > maxPayloadStringBytes || len(output.Digest) > maxPayloadStringBytes ||
			len(output.MediaType) > maxPayloadStringBytes {
			return fmt.Errorf("output reference %d exceeds workflow payload string limit", i)
		}
	}
	bytes := shardPayloadBytes(result.Shard) + len(result.NextCursor) + len(result.GapAcceptanceRef) + payloadBytes(result.Outputs)
	if bytes > maxPayloadBytes {
		return fmt.Errorf("stage result exceeds workflow payload byte limit")
	}
	return nil
}

func validateOutputLimit(maxOutputRefs int) error {
	if maxOutputRefs <= 0 || maxOutputRefs > maxOutputRefsHard {
		return fmt.Errorf("invalid output reference limit")
	}
	return nil
}

func validatePrepareRequest(request PrepareRequest) error {
	if !validCountry(request.Country) || request.RunID == uuid.Nil || request.BuildID == uuid.Nil ||
		!request.Stage.Valid() || request.Recipe == "" || request.Cutoff.IsZero() || request.Scan < 0 {
		return fmt.Errorf("prepare identity is invalid")
	}
	if err := request.Budget.Validate(); err != nil {
		return fmt.Errorf("prepare budget: %w", err)
	}
	if request.MaxPayloadBytes <= 0 || request.MaxPayloadBytes > maxPayloadBytesHard ||
		len(request.Country)+len(request.Stage)+len(request.Recipe)+len(request.Cursor)+len(request.PreviousManifestRef) > request.MaxPayloadBytes {
		return fmt.Errorf("prepare payload limit is invalid or exceeded")
	}
	return nil
}

func validateFenceZeroShard(shard run.Shard) error {
	if !validCountry(shard.Execution.Country) || shard.Execution.Owner == "" ||
		shard.Execution.RunID == uuid.Nil || shard.Execution.BuildID == uuid.Nil ||
		shard.Execution.LeaseFence != 0 || !shard.Stage.Valid() || shard.Recipe == "" || shard.ManifestRef == "" {
		return fmt.Errorf("fence-zero shard identity is invalid")
	}
	if err := shard.Budget.Validate(); err != nil {
		return fmt.Errorf("shard budget: %w", err)
	}
	return nil
}

func validateFenceZeroShardPayload(shard run.Shard, maxPayloadBytes int) error {
	if err := validateFenceZeroShard(shard); err != nil {
		return err
	}
	return validateShardPayload(shard, maxPayloadBytes)
}

func validateShardPayload(shard run.Shard, maxPayloadBytes int) error {
	if maxPayloadBytes <= 0 || maxPayloadBytes > maxPayloadBytesHard {
		return fmt.Errorf("invalid payload byte limit")
	}
	if len(shard.Execution.Country) > maxPayloadStringBytes || len(shard.Execution.Owner) > maxPayloadStringBytes ||
		len(shard.Stage) > maxPayloadStringBytes || len(shard.Cursor) > maxPayloadStringBytes ||
		len(shard.ManifestRef) > maxPayloadStringBytes || len(shard.Recipe) > maxPayloadStringBytes {
		return fmt.Errorf("shard contains an oversized workflow payload string")
	}
	if shardPayloadBytes(shard) > maxPayloadBytes {
		return fmt.Errorf("shard exceeds workflow payload byte limit")
	}
	return nil
}

func validateActivationSummary(request release.ActivationRequest, summary ActivationSummary, maxPayloadBytes int) error {
	if summary.Outcome != release.ActivationApplied && summary.Outcome != release.ActivationReconciled {
		return fmt.Errorf("unknown activation outcome %q", summary.Outcome)
	}
	if summary.Country != request.Country || summary.BuildID != request.BuildID ||
		summary.CurrentReleaseID != request.ReleaseID ||
		summary.PreviousReleaseID != request.ExpectedCurrentReleaseID {
		return fmt.Errorf("activation selection identity differs from request")
	}
	if request.ExpectedSelectionVersion == math.MaxInt64 ||
		summary.SelectionVersion != request.ExpectedSelectionVersion+1 {
		return fmt.Errorf("activation selection version does not increment expected version")
	}
	bytes := len(summary.Country) + len(summary.Outcome) + len(summary.CurrentReleaseID) + len(summary.PreviousReleaseID)
	if bytes > maxPayloadBytes || len(summary.CurrentReleaseID) > maxPayloadStringBytes ||
		len(summary.PreviousReleaseID) > maxPayloadStringBytes {
		return fmt.Errorf("activation result exceeds workflow payload limit")
	}
	return nil
}

func validExternalState(state ExternalState) bool {
	switch state {
	case ExternalPrepared, ExternalSubmitted, ExternalRunning, ExternalSucceeded, ExternalFailed, ExternalUncertain:
		return true
	default:
		return false
	}
}

func shardPayloadBytes(shard run.Shard) int {
	return len(shard.Execution.Country) + len(shard.Execution.Owner) + len(shard.Stage) +
		len(shard.Cursor) + len(shard.ManifestRef) + len(shard.Recipe)
}

func nonRetryableActivity(errorType, operation string, cause error) error {
	return temporalsdk.NewNonRetryableApplicationError(boundedFailureMessage(operation, cause), errorType, nil)
}

func durableActivityError(operation string, cause error) error {
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return cause
	}
	if errors.Is(cause, run.ErrLeaseIdentity) || errors.Is(cause, run.ErrLeaseLost) ||
		errors.Is(cause, run.ErrStaleResult) || errors.Is(cause, run.ErrNoProgress) ||
		errors.Is(cause, release.ErrSelectionConflict) || errors.Is(cause, release.ErrWrongCountry) ||
		errors.Is(cause, release.ErrWrongBuild) || errors.Is(cause, release.ErrRetentionConflict) ||
		errors.Is(cause, release.ErrIncompleteManifest) || errors.Is(cause, release.ErrCoverageMismatch) ||
		errors.Is(cause, release.ErrUnacceptedGaps) || errors.Is(cause, release.ErrIncompatibleRuntime) {
		return nonRetryableActivity("durable_contract_rejected", operation, cause)
	}
	var transient *TransientError
	if errors.As(cause, &transient) {
		return temporalsdk.NewApplicationError(boundedFailureMessage(operation, transient),
			"transient_durable_port")
	}
	return nonRetryableActivity("durable_port_rejected", operation, cause)
}

type heartbeatResponse[T any] struct {
	value T
	err   error
}

func heartbeatCall[T any](ctx context.Context, interval time.Duration, detail any, call func() (T, error)) (T, error) {
	responses := make(chan heartbeatResponse[T], 1)
	go func() {
		value, err := call()
		responses <- heartbeatResponse[T]{value: value, err: err}
	}()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	activity.RecordHeartbeat(ctx, detail)
	for {
		select {
		case response := <-responses:
			activity.RecordHeartbeat(ctx, detail)
			return response.value, response.err
		case <-ticker.C:
			activity.RecordHeartbeat(ctx, detail)
		case <-ctx.Done():
			var zero T
			return zero, ctx.Err()
		}
	}
}
