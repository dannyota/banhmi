package temporal

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/activity"
	temporalsdk "go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

	"danny.vn/banhmi/internal/corpus/release"
	"danny.vn/banhmi/internal/corpus/run"
)

func TestCorpusRefreshOrdersFixedStages(t *testing.T) {
	harness := newWorkflowHarness()
	env := newWorkflowEnvironment(t, harness)
	env.ExecuteWorkflow(CorpusRefresh, testState())
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("CorpusRefresh() error = %v", err)
	}
	want := []run.Stage{
		run.Discover, run.Fetch, run.Extract, run.OCR, run.Normalize, run.Index,
		run.Dense, run.Lexical, run.Evaluate, run.Publish, run.Activate,
	}
	if got := harness.completed(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("completed stages = %v, want %v", got, want)
	}
}

func TestStartOptionsUsesDeterministicBuildIdentity(t *testing.T) {
	state := testState()
	options, err := StartOptions(state)
	if err != nil {
		t.Fatalf("StartOptions() error = %v", err)
	}
	want := "corpus-refresh/vn/20000000-0000-0000-0000-000000000002"
	if options.ID != want || options.TaskQueue != CoordinatorTaskQueue {
		t.Fatalf("StartOptions() = ID %q queue %q, want ID %q queue %q",
			options.ID, options.TaskQueue, want, CoordinatorTaskQueue)
	}
	if options.WorkflowIDReusePolicy != enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE ||
		options.WorkflowIDConflictPolicy != enumspb.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING {
		t.Fatalf("StartOptions() duplicate policies = %s/%s",
			options.WorkflowIDReusePolicy, options.WorkflowIDConflictPolicy)
	}
}

func TestStartOptionsRejectsInjectedProgress(t *testing.T) {
	state := testState()
	state.Phase = run.Fetch
	if _, err := StartOptions(state); err == nil {
		t.Fatal("StartOptions() error = nil, want injected progress rejection")
	}
}

func TestStartOptionsRejectsCrossBuildActivation(t *testing.T) {
	state := testState()
	state.Activation.BuildID = uuid.MustParse("30000000-0000-0000-0000-000000000003")
	if _, err := StartOptions(state); err == nil || !strings.Contains(err.Error(), "activation build ID") {
		t.Fatalf("StartOptions() error = %v, want cross-build activation rejection", err)
	}
}

func TestCorpusRefreshRejectsInjectedProgressOnInitialRun(t *testing.T) {
	harness := newWorkflowHarness()
	env := newWorkflowEnvironment(t, harness)
	state := testState()
	state.Phase = run.Fetch
	env.ExecuteWorkflow(CorpusRefresh, state)
	err := env.GetWorkflowError()
	if err == nil || !strings.Contains(err.Error(), "continuation progress") {
		t.Fatalf("CorpusRefresh() error = %v, want injected progress rejection", err)
	}
}

func TestCorpusRefreshRetriesLostActivityResponseWithSameManifest(t *testing.T) {
	harness := newWorkflowHarness()
	harness.responseLossStage = run.Fetch
	env := newWorkflowEnvironment(t, harness)
	env.ExecuteWorkflow(CorpusRefresh, testState())
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("CorpusRefresh() error = %v", err)
	}
	keys := harness.keys(run.Fetch)
	if len(keys) != 2 || keys[0] != keys[1] {
		t.Fatalf("fetch retry keys = %v, want one immutable key repeated", keys)
	}
}

func TestCorpusRefreshWaitsForLedgerEligibilityBeforeNewManifest(t *testing.T) {
	harness := newWorkflowHarness()
	harness.delayStage = run.Discover
	env := newWorkflowEnvironment(t, harness)
	env.ExecuteWorkflow(CorpusRefresh, testState())
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("CorpusRefresh() error = %v", err)
	}
	if scans := harness.scans(run.Discover); scans != 3 {
		t.Fatalf("discover scans = %d, want delayed scan, work scan, and exhaustion scan", scans)
	}
}

func TestCorpusRefreshPendingRetryUsesNewImmutableManifest(t *testing.T) {
	harness := newWorkflowHarness()
	harness.pendingRetryStage = run.Discover
	env := newWorkflowEnvironment(t, harness)
	env.ExecuteWorkflow(CorpusRefresh, testState())
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("CorpusRefresh() error = %v", err)
	}
	keys := harness.keys(run.Discover)
	if len(keys) != 2 || keys[0] == keys[1] {
		t.Fatalf("discover attempt keys = %v, want two distinct immutable manifests", keys)
	}
}

func TestCorpusRefreshCompleteShardWithCursorDoesNotCompletePhase(t *testing.T) {
	harness := newWorkflowHarness()
	harness.nextCursorStage = run.Discover
	env := newWorkflowEnvironment(t, harness)
	env.ExecuteWorkflow(CorpusRefresh, testState())
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("CorpusRefresh() error = %v", err)
	}
	if keys := harness.keys(run.Discover); len(keys) != 2 {
		t.Fatalf("discover executions = %d, want 2 before ledger exhaustion", len(keys))
	}
}

func TestCorpusRefreshStopsAfterFenceLoss(t *testing.T) {
	harness := newWorkflowHarness()
	harness.fenceLossStage = run.Normalize
	env := newWorkflowEnvironment(t, harness)
	env.ExecuteWorkflow(CorpusRefresh, testState())
	if err := env.GetWorkflowError(); err == nil {
		t.Fatal("CorpusRefresh() error = nil, want fence loss")
	}
	if harness.wasCompleted(run.Index) {
		t.Fatal("index ran after the normalize fence was lost")
	}
}

func TestCorpusRefreshCancellationRecordsExternalCleanup(t *testing.T) {
	harness := newWorkflowHarness()
	harness.runningExternal = run.OCR
	env := newWorkflowEnvironment(t, harness)
	env.RegisterDelayedCallback(env.CancelWorkflow, time.Minute)
	env.ExecuteWorkflow(CorpusRefresh, testState())
	if err := env.GetWorkflowError(); err == nil {
		t.Fatal("CorpusRefresh() error = nil, want cancellation")
	}
	if !harness.cleanupRecorded() {
		t.Fatal("external cancellation record was not written")
	}
}

func TestCorpusRefreshCancellationPreservesCleanupFailure(t *testing.T) {
	harness := newWorkflowHarness()
	harness.runningExternal = run.OCR
	harness.cleanupErr = errors.New("cleanup store unavailable")
	env := newWorkflowEnvironment(t, harness)
	env.RegisterDelayedCallback(env.CancelWorkflow, time.Minute)
	env.ExecuteWorkflow(CorpusRefresh, testState())
	err := env.GetWorkflowError()
	if err == nil || !temporalsdk.IsCanceledError(err) {
		t.Fatalf("CorpusRefresh() error = %v, want canceled error", err)
	}
	if !strings.Contains(err.Error(), "cleanup failed") {
		t.Fatalf("CorpusRefresh() error = %v, want cleanup failure", err)
	}
}

func TestCorpusRefreshStopsOnUncertainExternalSubmission(t *testing.T) {
	harness := newWorkflowHarness()
	harness.uncertainStage = run.Dense
	env := newWorkflowEnvironment(t, harness)
	env.ExecuteWorkflow(CorpusRefresh, testState())
	err := env.GetWorkflowError()
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "uncertain") {
		t.Fatalf("CorpusRefresh() error = %v, want uncertain submission", err)
	}
	if harness.wasCompleted(run.Lexical) {
		t.Fatal("lexical ran after an uncertain dense submission")
	}
}

func TestCorpusRefreshReturnsActivationFailure(t *testing.T) {
	harness := newWorkflowHarness()
	harness.activationErr = errors.New("selection conflict")
	env := newWorkflowEnvironment(t, harness)
	env.ExecuteWorkflow(CorpusRefresh, testState())
	if err := env.GetWorkflowError(); err == nil || !strings.Contains(err.Error(), "selection conflict") {
		t.Fatalf("CorpusRefresh() error = %v, want activation failure", err)
	}
}

func TestCorpusRefreshRejectsActivationIdentityMismatch(t *testing.T) {
	harness := newWorkflowHarness()
	harness.activationCountry = "us"
	env := newWorkflowEnvironment(t, harness)
	env.ExecuteWorkflow(CorpusRefresh, testState())
	if err := env.GetWorkflowError(); err == nil || !strings.Contains(err.Error(), "activation selection identity") {
		t.Fatalf("CorpusRefresh() error = %v, want activation identity rejection", err)
	}
}

func TestCorpusRefreshRejectsActivationBuildMismatch(t *testing.T) {
	harness := newWorkflowHarness()
	harness.activationBuildID = uuid.MustParse("30000000-0000-0000-0000-000000000003")
	env := newWorkflowEnvironment(t, harness)
	env.ExecuteWorkflow(CorpusRefresh, testState())
	if err := env.GetWorkflowError(); err == nil || !strings.Contains(err.Error(), "activation selection identity") {
		t.Fatalf("CorpusRefresh() error = %v, want activation build rejection", err)
	}
}

func TestAdvanceExternalResumesCheckAndApplyWithoutSubmit(t *testing.T) {
	harness := newWorkflowHarness()
	state := testState()
	state.Phase = run.OCR
	state.ManifestRef = "manifest/ocr/resume"
	state.Fence = 11
	shard := run.Shard{
		Execution: run.Execution{Country: state.Country, Owner: "temporal-test", RunID: state.RunID, BuildID: state.BuildID, LeaseFence: state.Fence},
		Stage:     state.Phase, ManifestRef: state.ManifestRef, Recipe: state.Recipe, Budget: state.Budget,
	}
	state.ActiveJob = &ExternalJob{
		Shard: shard, SubmissionKey: SubmissionKey(shard), ProviderJobRef: "provider/ocr",
		State: ExternalRunning, NextCheck: state.Cutoff.Add(time.Second),
	}
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(resumeExternalTestWorkflow)
	env.RegisterActivityWithOptions(harness.submitExternal, activity.RegisterOptions{Name: SubmitExternalActivityName})
	env.RegisterActivityWithOptions(harness.checkExternal, activity.RegisterOptions{Name: CheckExternalActivityName})
	env.RegisterActivityWithOptions(harness.applyExternal, activity.RegisterOptions{Name: ApplyExternalActivityName})
	env.ExecuteWorkflow(resumeExternalTestWorkflow, state)
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("resumeExternalTestWorkflow() error = %v", err)
	}
	if got := harness.submissions(); got != 0 {
		t.Fatalf("external submissions = %d, want 0", got)
	}
}

func resumeExternalTestWorkflow(ctx workflow.Context, state State) (run.Result, error) {
	for {
		result, complete, err := advanceExternal(ctx, &state)
		if err != nil || complete {
			return result, err
		}
	}
}

func TestStateRejectsCarriedJobWithWrongSubmissionKey(t *testing.T) {
	state := testState()
	state.Phase = run.OCR
	state.ManifestRef = "manifest/ocr/resume"
	state.Fence = 11
	state.ActiveJob = &ExternalJob{
		Shard: run.Shard{
			Execution: run.Execution{Country: state.Country, Owner: "temporal-test", RunID: state.RunID, BuildID: state.BuildID, LeaseFence: state.Fence},
			Stage:     state.Phase, ManifestRef: state.ManifestRef, Recipe: state.Recipe, Budget: state.Budget,
		},
		SubmissionKey: "wrong", ProviderJobRef: "provider/ocr", State: ExternalRunning,
	}
	if err := state.Validate(); err == nil || !strings.Contains(err.Error(), "submission key") {
		t.Fatalf("State.Validate() error = %v, want submission key rejection", err)
	}
}

func TestStateRejectsCarriedJobWithOversizedOwner(t *testing.T) {
	state := testState()
	state.Phase = run.OCR
	state.ManifestRef = "manifest/ocr/resume"
	state.Fence = 11
	shard := run.Shard{
		Execution: run.Execution{
			Country: state.Country, Owner: strings.Repeat("x", maxPayloadStringBytes+1),
			RunID: state.RunID, BuildID: state.BuildID, LeaseFence: state.Fence,
		},
		Stage: state.Phase, ManifestRef: state.ManifestRef, Recipe: state.Recipe, Budget: state.Budget,
	}
	state.ActiveJob = &ExternalJob{
		Shard: shard, SubmissionKey: SubmissionKey(shard), ProviderJobRef: "provider/ocr", State: ExternalRunning,
	}
	if err := state.Validate(); err == nil || !strings.Contains(err.Error(), "oversized") {
		t.Fatalf("State.Validate() error = %v, want oversized owner rejection", err)
	}
}

func TestValidateResultPayloadEnforcesHardCaps(t *testing.T) {
	state := testState()
	shard := run.Shard{
		Execution: run.Execution{Country: state.Country, Owner: "temporal-test", RunID: state.RunID, BuildID: state.BuildID, LeaseFence: 1},
		Stage:     run.Fetch, ManifestRef: "manifest/fetch/1", Recipe: state.Recipe, Budget: state.Budget,
	}
	result := completedResult(shard)
	result.Outputs = []run.OutputRef{
		{Ref: "object/1", Digest: "sha256:1", SizeBytes: 1},
		{Ref: "object/2", Digest: "sha256:2", SizeBytes: 1},
	}
	if err := validateResultPayload(result, 1, state.MaxPayloadBytes); err == nil {
		t.Fatal("validateResultPayload() error = nil, want output count rejection")
	}
	if err := validateResultPayload(result, 2, 8); err == nil {
		t.Fatal("validateResultPayload() error = nil, want aggregate byte rejection")
	}
}

func TestValidatePreparedStageRejectsInvalidOwnerBeforeExternalPrepare(t *testing.T) {
	state := testState()
	request := PrepareRequest{
		Country: state.Country, RunID: state.RunID, BuildID: state.BuildID, Stage: run.OCR,
		Recipe: state.Recipe, Cutoff: state.Cutoff, Budget: state.Budget, MaxPayloadBytes: state.MaxPayloadBytes,
	}
	prepared := PreparedStage{HasShard: true, Shard: run.Shard{
		Execution: run.Execution{Country: state.Country, RunID: state.RunID, BuildID: state.BuildID},
		Stage:     run.OCR, ManifestRef: "manifest/ocr/1", Recipe: state.Recipe, Budget: state.Budget,
	}}
	if err := validatePreparedStage(request, prepared); err == nil || !strings.Contains(err.Error(), "fence-zero shard") {
		t.Fatalf("validatePreparedStage() error = %v, want owner rejection", err)
	}
}

func TestValidatePreparedStageRejectsUnusedShardPayload(t *testing.T) {
	state := testState()
	request := PrepareRequest{
		Country: state.Country, RunID: state.RunID, BuildID: state.BuildID, Stage: run.OCR,
		Recipe: state.Recipe, Cutoff: state.Cutoff, Budget: state.Budget, MaxPayloadBytes: state.MaxPayloadBytes,
	}
	prepared := PreparedStage{Exhausted: true, Shard: run.Shard{ManifestRef: strings.Repeat("x", maxPayloadBytesHard)}}
	if err := validatePreparedStage(request, prepared); err == nil || !strings.Contains(err.Error(), "must not include shard data") {
		t.Fatalf("validatePreparedStage() error = %v, want unused shard rejection", err)
	}
}

func TestValidateCheckedExternalIdentityRejectsProviderSwitch(t *testing.T) {
	before := ExternalJob{ProviderJobRef: "provider/job-1", State: ExternalRunning}
	after := before
	after.ProviderJobRef = "provider/job-2"
	if err := validateCheckedExternalIdentity(before, after); err == nil {
		t.Fatal("validateCheckedExternalIdentity() error = nil, want provider identity rejection")
	}
}

func TestDurableActivityErrorRequiresExplicitTransientMarker(t *testing.T) {
	unknown := durableActivityError("mutate ledger", errors.New("adapter rejected input"))
	var application *temporalsdk.ApplicationError
	if !errors.As(unknown, &application) || !application.NonRetryable() {
		t.Fatalf("durableActivityError(unknown) = %T %v, want nonretryable application error", unknown, unknown)
	}
	retryable := durableActivityError("load ledger", MarkTransient(errors.New("connection reset")))
	application = nil
	if !errors.As(retryable, &application) || application.NonRetryable() {
		t.Fatalf("durableActivityError(transient) = %T %v, want retryable application error", retryable, retryable)
	}
	overlap := durableActivityError("renew lease", MarkTransient(fmt.Errorf("connection reset: %w", run.ErrLeaseLost)))
	application = nil
	if !errors.As(overlap, &application) || !application.NonRetryable() {
		t.Fatalf("durableActivityError(transient lease loss) = %T %v, want nonretryable application error", overlap, overlap)
	}
}

func TestDurableActivityErrorBoundsFailureHistory(t *testing.T) {
	long := errors.New(strings.Repeat("provider-response-", maxPayloadStringBytes))
	err := durableActivityError("submit external job", long)
	if strings.Contains(err.Error(), long.Error()) || len(err.Error()) > maxFailureMessageBytes {
		t.Fatalf("durableActivityError() retained unbounded error: %d bytes", len(err.Error()))
	}
}

func TestDurableActivityErrorPreservesValidUTF8AtBoundary(t *testing.T) {
	cause := errors.New(strings.Repeat("x", maxFailureDetailBytes-1) + "界")
	err := durableActivityError("submit external job", cause)
	if !utf8.ValidString(err.Error()) || len(err.Error()) > maxFailureMessageBytes {
		t.Fatalf("durableActivityError() produced invalid or oversized UTF-8: %q", err.Error())
	}
}

func TestTotalsAddOverflowDoesNotMutate(t *testing.T) {
	totals := Totals{Processed: math.MaxInt64, Succeeded: 4}
	want := totals
	err := totals.add(run.Counts{Expected: 1, Processed: 1, Succeeded: 1})
	if err == nil {
		t.Fatal("Totals.add() error = nil, want overflow")
	}
	if totals != want {
		t.Fatalf("Totals.add() mutated totals to %+v, want %+v", totals, want)
	}
}

func TestCorpusRefreshContinuesAsNewAtScanLimit(t *testing.T) {
	harness := newWorkflowHarness()
	env := newWorkflowEnvironment(t, harness)
	state := testState()
	state.MaxScansPerRun = 1
	env.ExecuteWorkflow(CorpusRefresh, state)
	err := env.GetWorkflowError()
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "continue") {
		t.Fatalf("CorpusRefresh() error = %v, want Continue-As-New", err)
	}
}

func TestReplayHostedHistoryFixtures(t *testing.T) {
	fixtures, err := filepath.Glob("testdata/history/*.json")
	if err != nil {
		t.Fatalf("glob history fixtures: %v", err)
	}
	if len(fixtures) == 0 {
		t.Skip("hosted Temporal history capture is pending")
	}
	for _, fixture := range fixtures {
		if err := ReplayHistoryFile(nil, fixture); err != nil {
			t.Errorf("ReplayHistoryFile(%q): %v", fixture, err)
		}
	}
}

type workflowHarness struct {
	mu                sync.Mutex
	prepareCalls      map[run.Stage]int
	executeKeys       map[run.Stage][]string
	completedStages   []run.Stage
	responseLossStage run.Stage
	delayStage        run.Stage
	pendingRetryStage run.Stage
	nextCursorStage   run.Stage
	fenceLossStage    run.Stage
	runningExternal   run.Stage
	uncertainStage    run.Stage
	activationErr     error
	activationCountry string
	activationBuildID uuid.UUID
	cleanupErr        error
	cleaned           bool
	submitCalls       int
}

func newWorkflowHarness() *workflowHarness {
	return &workflowHarness{
		prepareCalls: make(map[run.Stage]int),
		executeKeys:  make(map[run.Stage][]string),
	}
}

func newWorkflowEnvironment(t *testing.T, harness *workflowHarness) *testsuite.TestWorkflowEnvironment {
	t.Helper()
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	start, err := StartOptions(testState())
	if err != nil {
		t.Fatalf("StartOptions(): %v", err)
	}
	env.SetStartWorkflowOptions(start)
	env.RegisterWorkflowWithOptions(CorpusRefresh, workflow.RegisterOptions{Name: WorkflowNameV1})
	env.RegisterActivityWithOptions(harness.prepare,
		activity.RegisterOptions{Name: PrepareStageActivityName})
	env.RegisterActivityWithOptions(harness.execute,
		activity.RegisterOptions{Name: ExecuteStageActivityName})
	env.RegisterActivityWithOptions(harness.prepareExternal,
		activity.RegisterOptions{Name: PrepareExternalActivityName})
	env.RegisterActivityWithOptions(harness.submitExternal,
		activity.RegisterOptions{Name: SubmitExternalActivityName})
	env.RegisterActivityWithOptions(harness.checkExternal,
		activity.RegisterOptions{Name: CheckExternalActivityName})
	env.RegisterActivityWithOptions(harness.applyExternal,
		activity.RegisterOptions{Name: ApplyExternalActivityName})
	env.RegisterActivityWithOptions(harness.cleanupExternal,
		activity.RegisterOptions{Name: CleanupExternalActivityName})
	env.RegisterActivityWithOptions(harness.activate,
		activity.RegisterOptions{Name: ActivateActivityName})
	return env
}

func testState() State {
	return State{
		Country:  "vn",
		RunID:    uuid.MustParse("10000000-0000-0000-0000-000000000001"),
		BuildID:  uuid.MustParse("20000000-0000-0000-0000-000000000002"),
		Recipe:   "recipe:1",
		Cutoff:   time.Date(2026, time.October, 11, 0, 0, 0, 0, time.UTC),
		Deadline: time.Date(2030, time.October, 11, 0, 0, 0, 0, time.UTC),
		Budget: run.Budget{
			MaxItems:      10,
			MaxInputBytes: 1 << 20,
			MaxPages:      100,
			MaxElapsed:    time.Minute,
			MaxConcurrent: 2,
		},
		Activation: release.ActivationRequest{
			Country:                  "vn",
			BuildID:                  uuid.MustParse("20000000-0000-0000-0000-000000000002"),
			ReleaseID:                "release-1",
			ExpectedSelectionVersion: 1,
			ExpectedCurrentReleaseID: "release-0",
			ActivatedBy:              "workflow-test",
		},
		MaxScansPerRun:  100,
		MaxStageScans:   10,
		MaxOutputRefs:   10,
		MaxPayloadBytes: 64 << 10,
		PollInterval:    10 * time.Minute,
	}
}

func (h *workflowHarness) prepare(_ context.Context, request PrepareRequest) (PreparedStage, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.prepareCalls[request.Stage]++
	call := h.prepareCalls[request.Stage]
	if request.Stage == h.delayStage && call == 1 {
		next := time.Now().Add(time.Minute)
		return PreparedStage{NextEligible: &next}, nil
	}
	workCall := call
	if request.Stage == h.delayStage {
		workCall--
	}
	maxWorkCalls := 1
	if request.Stage == h.pendingRetryStage || request.Stage == h.nextCursorStage {
		maxWorkCalls = 2
	}
	if workCall > maxWorkCalls {
		return PreparedStage{Exhausted: true}, nil
	}
	return PreparedStage{HasShard: true, Shard: run.Shard{
		Execution: run.Execution{
			Country: request.Country,
			Owner:   "temporal-test",
			RunID:   request.RunID,
			BuildID: request.BuildID,
		},
		Stage:       request.Stage,
		Cursor:      request.Cursor,
		ManifestRef: fmt.Sprintf("manifest/%s/%d", request.Stage, request.Scan),
		Recipe:      request.Recipe,
		Budget:      request.Budget,
	}}, nil
}

func (h *workflowHarness) execute(_ context.Context, request ExecuteStageRequest) (run.Result, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	shard := request.Shard
	key := run.IdempotencyKey(shard)
	h.executeKeys[shard.Stage] = append(h.executeKeys[shard.Stage], key)
	if shard.Stage == h.responseLossStage && len(h.executeKeys[shard.Stage]) == 1 {
		return run.Result{}, MarkTransient(errors.New("activity response lost"))
	}
	if shard.Stage == h.fenceLossStage {
		return run.Result{}, temporalsdk.NewNonRetryableApplicationError(
			"scheduler lease lost", "scheduler_lease_lost", run.ErrLeaseLost)
	}
	shard.Execution.LeaseFence = 7
	if shard.Stage == h.pendingRetryStage && len(h.executeKeys[shard.Stage]) == 1 {
		next := time.Now().Add(time.Minute)
		return run.Result{
			Shard:        shard,
			Counts:       run.Counts{Expected: 1, Processed: 1, PendingRetry: 1},
			NextEligible: &next,
		}, nil
	}
	h.completedStages = append(h.completedStages, shard.Stage)
	result := completedResult(shard)
	if shard.Stage == h.nextCursorStage && len(h.executeKeys[shard.Stage]) == 1 {
		result.NextCursor = "next"
	}
	return result, nil
}

func (h *workflowHarness) prepareExternal(_ context.Context, request ExternalPrepareRequest) (ExternalJob, error) {
	request.Shard.Execution.LeaseFence = 11
	return ExternalJob{
		Shard:         request.Shard,
		SubmissionKey: request.SubmissionKey,
		State:         ExternalPrepared,
	}, nil
}

func (h *workflowHarness) submitExternal(_ context.Context, request ExternalSubmitRequest) (ExternalJob, error) {
	h.mu.Lock()
	h.submitCalls++
	h.mu.Unlock()
	job := request.Job
	if job.Shard.Stage == h.uncertainStage {
		job.State = ExternalUncertain
		return job, nil
	}
	job.ProviderJobRef = "provider/" + string(job.Shard.Stage)
	if job.Shard.Stage == h.runningExternal {
		job.State = ExternalRunning
		job.NextCheck = time.Now().Add(time.Hour)
		return job, nil
	}
	job.State = ExternalSucceeded
	job.ResultRef = "result/" + string(job.Shard.Stage)
	return job, nil
}

func (h *workflowHarness) checkExternal(_ context.Context, request ExternalCheckRequest) (ExternalJob, error) {
	request.Job.State = ExternalSucceeded
	request.Job.ResultRef = "result/" + string(request.Job.Shard.Stage)
	return request.Job, nil
}

func (h *workflowHarness) applyExternal(_ context.Context, request ExternalApplyRequest) (run.Result, error) {
	h.mu.Lock()
	h.completedStages = append(h.completedStages, request.Job.Shard.Stage)
	h.mu.Unlock()
	return completedResult(request.Job.Shard), nil
}

func (h *workflowHarness) cleanupExternal(_ context.Context, _ CleanupRequest) (CleanupResult, error) {
	h.mu.Lock()
	h.cleaned = true
	h.mu.Unlock()
	if h.cleanupErr != nil {
		return CleanupResult{}, h.cleanupErr
	}
	return CleanupResult{CancellationRecorded: true, ProviderCancelAttempted: true}, nil
}

func (h *workflowHarness) activate(_ context.Context, input ActivationActivityRequest) (ActivationSummary, error) {
	if h.activationErr != nil {
		return ActivationSummary{}, h.activationErr
	}
	request := input.Activation
	h.mu.Lock()
	h.completedStages = append(h.completedStages, run.Activate)
	h.mu.Unlock()
	country := request.Country
	if h.activationCountry != "" {
		country = h.activationCountry
	}
	buildID := request.BuildID
	if h.activationBuildID != uuid.Nil {
		buildID = h.activationBuildID
	}
	return ActivationSummary{
		Country:           country,
		BuildID:           buildID,
		Outcome:           release.ActivationApplied,
		SelectionVersion:  request.ExpectedSelectionVersion + 1,
		CurrentReleaseID:  request.ReleaseID,
		PreviousReleaseID: request.ExpectedCurrentReleaseID,
	}, nil
}

func completedResult(shard run.Shard) run.Result {
	return run.Result{
		Shard:    shard,
		Counts:   run.Counts{Expected: 1, Processed: 1, Succeeded: 1},
		Complete: true,
	}
}

func (h *workflowHarness) completed() []run.Stage {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]run.Stage(nil), h.completedStages...)
}

func (h *workflowHarness) keys(stage run.Stage) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.executeKeys[stage]...)
}

func (h *workflowHarness) scans(stage run.Stage) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.prepareCalls[stage]
}

func (h *workflowHarness) wasCompleted(stage run.Stage) bool {
	for _, completed := range h.completed() {
		if completed == stage {
			return true
		}
	}
	return false
}

func (h *workflowHarness) cleanupRecorded() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cleaned
}

func (h *workflowHarness) submissions() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.submitCalls
}
