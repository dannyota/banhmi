// Package temporal coordinates bounded corpus refresh stages with the Temporal
// SDK. PostgreSQL remains authoritative for leases, retries, immutable outputs,
// external jobs, and completeness. Legacy synchronous provider paths are not
// wired into this pilot and remain explicitly nonresumable.
package temporal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"

	"danny.vn/banhmi/internal/corpus/release"
	"danny.vn/banhmi/internal/corpus/run"
)

const (
	WorkflowNameV1         = "corpus.refresh.v1"
	workflowChangeIDV1     = "corpus-refresh-contract-v1"
	workflowVersionV1      = 1
	maxPayloadStringBytes  = 4096
	maxFailureMessageBytes = 1024
	maxFailureDetailBytes  = 512
	maxOutputRefsHard      = 256
	maxPayloadBytesHard    = 1 << 20
	CoordinatorTaskQueue   = "corpus-coordinator-v1"
	NetworkTaskQueue       = "corpus-network-v1"
	CPUTaskQueue           = "corpus-cpu-v1"
	DatabaseTaskQueue      = "corpus-database-v1"
	ExternalTaskQueue      = "corpus-external-v1"
	ExclusiveTaskQueue     = "corpus-exclusive-v1"

	PrepareStageActivityName    = "corpus.prepare-stage.v1"
	ExecuteStageActivityName    = "corpus.execute-stage.v1"
	PrepareExternalActivityName = "corpus.prepare-external.v1"
	SubmitExternalActivityName  = "corpus.submit-external.v1"
	CheckExternalActivityName   = "corpus.check-external.v1"
	ApplyExternalActivityName   = "corpus.apply-external.v1"
	CleanupExternalActivityName = "corpus.cleanup-external.v1"
	ActivateActivityName        = "corpus.activate.v1"

	errorTypeUncertainSubmission = "uncertain_external_submission"
	errorTypeBlockingGaps        = "blocking_corpus_gaps"
)

var fixedStages = [...]run.Stage{
	run.Discover,
	run.Fetch,
	run.Extract,
	run.OCR,
	run.Normalize,
	run.Index,
	run.Dense,
	run.Lexical,
	run.Evaluate,
	run.Publish,
	run.Activate,
}

var (
	ErrUncertainSubmission = errors.New("external submission outcome is uncertain")
	ErrBlockingGaps        = errors.New("stage has unresolved blocking gaps")
)

// TransientError marks a returned durable-port error as safe for an activity
// retry. Worker loss and Temporal timeouts remain retryable without this type.
type TransientError struct {
	Cause error
}

func (e *TransientError) Error() string {
	if e == nil || e.Cause == nil {
		return "transient durable-port failure"
	}
	return e.Cause.Error()
}

func (e *TransientError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// MarkTransient opts a durable-port failure into Temporal activity retry.
func MarkTransient(cause error) error {
	if cause == nil {
		return nil
	}
	return &TransientError{Cause: cause}
}

var _ ActivationService = (*release.PublicationService)(nil)

// State is the complete bounded payload carried across Continue-As-New.
// Source bytes, document text, vectors, credentials, and provider responses
// belong in durable stores referenced by these fields.
type State struct {
	Country         string
	RunID           uuid.UUID
	BuildID         uuid.UUID
	Recipe          string
	Cutoff          time.Time
	Deadline        time.Time
	Budget          run.Budget
	Activation      release.ActivationRequest
	Phase           run.Stage
	Cursor          string
	ManifestRef     string
	Fence           int64
	Counts          Totals
	OutputRefs      []run.OutputRef
	Scan            int64
	ScansThisRun    int
	MaxScansPerRun  int
	MaxStageScans   int
	MaxOutputRefs   int
	MaxPayloadBytes int
	PollInterval    time.Duration
	ActiveJob       *ExternalJob
}

// Totals contains small counters only. Durable stage rows remain the source of
// truth for per-artifact outcomes and completeness.
type Totals struct {
	Processed    int64
	Succeeded    int64
	PendingRetry int64
	TerminalGap  int64
	AcceptedGaps int64
	Rejected     int64
	Failed       int64
}

func (t *Totals) add(counts run.Counts) error {
	values := []struct {
		name string
		dst  *int64
		add  int64
	}{
		{name: "processed", dst: &t.Processed, add: int64(counts.Processed)},
		{name: "succeeded", dst: &t.Succeeded, add: int64(counts.Succeeded)},
		{name: "pending retry", dst: &t.PendingRetry, add: int64(counts.PendingRetry)},
		{name: "terminal gap", dst: &t.TerminalGap, add: int64(counts.TerminalGap)},
		{name: "accepted gaps", dst: &t.AcceptedGaps, add: int64(counts.AcceptedGaps)},
		{name: "rejected", dst: &t.Rejected, add: int64(counts.Rejected)},
		{name: "failed", dst: &t.Failed, add: int64(counts.Failed)},
	}
	for _, value := range values {
		if value.add < 0 || *value.dst > math.MaxInt64-value.add {
			return fmt.Errorf("%s count overflows", value.name)
		}
	}
	for _, value := range values {
		*value.dst += value.add
	}
	return nil
}

// WorkflowID is stable across trigger retries and Continue-As-New runs.
func WorkflowID(country string, buildID uuid.UUID) string {
	return fmt.Sprintf("corpus-refresh/%s/%s", country, buildID)
}

// Validate rejects state that would make a refresh unbounded or ambiguous.
func (s State) Validate() error {
	switch {
	case !validCountry(s.Country):
		return fmt.Errorf("country must be a lowercase two-letter code")
	case s.RunID == uuid.Nil:
		return fmt.Errorf("run ID is required")
	case s.BuildID == uuid.Nil:
		return fmt.Errorf("build ID is required")
	case s.Recipe == "":
		return fmt.Errorf("recipe is required")
	case s.Cutoff.IsZero():
		return fmt.Errorf("cutoff is required")
	case s.Deadline.IsZero() || !s.Deadline.After(s.Cutoff):
		return fmt.Errorf("deadline must be after cutoff")
	case s.MaxScansPerRun <= 0:
		return fmt.Errorf("max scans per run must be positive")
	case s.MaxStageScans <= 0:
		return fmt.Errorf("max stage scans must be positive")
	case s.MaxOutputRefs <= 0 || s.MaxOutputRefs > maxOutputRefsHard:
		return fmt.Errorf("max output references must be between 1 and %d", maxOutputRefsHard)
	case s.MaxPayloadBytes <= 0 || s.MaxPayloadBytes > maxPayloadBytesHard:
		return fmt.Errorf("max payload bytes must be between 1 and %d", maxPayloadBytesHard)
	case s.PollInterval <= 0:
		return fmt.Errorf("poll interval must be positive")
	case s.Scan < 0 || s.ScansThisRun < 0:
		return fmt.Errorf("scan counters must not be negative")
	case s.Fence < 0:
		return fmt.Errorf("fence must not be negative")
	}
	if s.Counts.Processed < 0 || s.Counts.Succeeded < 0 || s.Counts.PendingRetry < 0 ||
		s.Counts.TerminalGap < 0 || s.Counts.AcceptedGaps < 0 || s.Counts.Rejected < 0 || s.Counts.Failed < 0 {
		return fmt.Errorf("workflow counters must not be negative")
	}
	if err := s.Budget.Validate(); err != nil {
		return fmt.Errorf("budget: %w", err)
	}
	if s.Phase != "" && !s.Phase.Valid() {
		return fmt.Errorf("unknown phase %q", s.Phase)
	}
	if len(s.OutputRefs) > s.MaxOutputRefs {
		return fmt.Errorf("output reference count exceeds limit")
	}
	for _, value := range []struct {
		name  string
		value string
	}{
		{name: "recipe", value: s.Recipe},
		{name: "cursor", value: s.Cursor},
		{name: "manifest reference", value: s.ManifestRef},
		{name: "activation release ID", value: s.Activation.ReleaseID},
		{name: "activation prior release ID", value: s.Activation.ExpectedCurrentReleaseID},
		{name: "activation actor", value: s.Activation.ActivatedBy},
	} {
		if len(value.value) > maxPayloadStringBytes || len(value.value) > s.MaxPayloadBytes {
			return fmt.Errorf("%s exceeds workflow payload string limit", value.name)
		}
	}
	if payloadBytes(s.OutputRefs) > s.MaxPayloadBytes {
		return fmt.Errorf("output references exceed workflow payload byte limit")
	}
	for i, output := range s.OutputRefs {
		if err := output.Validate(); err != nil {
			return fmt.Errorf("output reference %d: %w", i, err)
		}
		if len(output.Ref) > maxPayloadStringBytes || len(output.Digest) > maxPayloadStringBytes ||
			len(output.MediaType) > maxPayloadStringBytes {
			return fmt.Errorf("output reference %d exceeds workflow payload string limit", i)
		}
	}
	if s.Activation.Country != s.Country {
		return fmt.Errorf("activation country %q does not match workflow country %q", s.Activation.Country, s.Country)
	}
	if s.Activation.BuildID == uuid.Nil || s.Activation.BuildID != s.BuildID {
		return fmt.Errorf("activation build ID does not match workflow build ID")
	}
	if s.Activation.ReleaseID == "" || s.Activation.ActivatedBy == "" {
		return fmt.Errorf("activation release ID and actor are required")
	}
	if s.Activation.ExpectedSelectionVersion < 0 || s.Activation.ExpectedSelectionVersion == math.MaxInt64 {
		return fmt.Errorf("activation expected selection version is out of range")
	}
	if s.ActiveJob != nil {
		if err := validateCarriedJob(s); err != nil {
			return err
		}
	}
	if statePayloadBytes(s) > s.MaxPayloadBytes {
		return fmt.Errorf("workflow state exceeds payload byte limit")
	}
	return nil
}

func (s State) validateNewStart() error {
	if s.Phase != "" || s.Cursor != "" || s.ManifestRef != "" || s.Fence != 0 ||
		s.Counts != (Totals{}) || len(s.OutputRefs) != 0 || s.Scan != 0 || s.ScansThisRun != 0 || s.ActiveJob != nil {
		return fmt.Errorf("new workflow start contains continuation progress")
	}
	return nil
}

// PrepareRequest names one durable eligibility scan. A retry of this request
// must return the same immutable shard. A later Scan must create a distinct
// input or attempt manifest before pending input can receive another outcome.
type PrepareRequest struct {
	Country             string
	RunID               uuid.UUID
	BuildID             uuid.UUID
	Stage               run.Stage
	Recipe              string
	Cutoff              time.Time
	Cursor              string
	PreviousManifestRef string
	Scan                int64
	Budget              run.Budget
	MaxPayloadBytes     int
}

// PreparedStage is the ledger's bounded view of one stage. Complete shard
// results never imply stage exhaustion. The workflow advances only after a
// later preparation reports Exhausted with no delay or blocking gaps.
type PreparedStage struct {
	HasShard     bool
	Shard        run.Shard
	Exhausted    bool
	BlockingGaps int
	NextEligible *time.Time
}

type ExecuteStageRequest struct {
	Shard           run.Shard
	MaxOutputRefs   int
	MaxPayloadBytes int
}

// StagePreparer owns durable input manifests and completeness decisions.
type StagePreparer interface {
	Prepare(context.Context, PrepareRequest) (PreparedStage, error)
}

type ExternalState string

const (
	ExternalPrepared  ExternalState = "prepared"
	ExternalSubmitted ExternalState = "submitted"
	ExternalRunning   ExternalState = "running"
	ExternalSucceeded ExternalState = "succeeded"
	ExternalFailed    ExternalState = "failed"
	ExternalUncertain ExternalState = "uncertain"
)

// ExternalJob contains only durable identities and object references.
type ExternalJob struct {
	Shard          run.Shard
	SubmissionKey  string
	ProviderJobRef string
	ResultRef      string
	State          ExternalState
	NextCheck      time.Time
}

// SubmissionKey excludes mutable scheduler owner and fence through
// run.IdempotencyKey. A new immutable attempt manifest produces a new key.
func SubmissionKey(shard run.Shard) string {
	digest := sha256.Sum256([]byte("corpus-external-v1\x00" + run.IdempotencyKey(shard)))
	return "sha256:" + hex.EncodeToString(digest[:])
}

type ExternalPrepareRequest struct {
	Shard           run.Shard
	SubmissionKey   string
	MaxPayloadBytes int
}

type ExternalCheckRequest struct {
	Job             ExternalJob
	MaxPayloadBytes int
}

type ExternalSubmitRequest struct {
	Job             ExternalJob
	MaxPayloadBytes int
}

type ExternalApplyRequest struct {
	Job             ExternalJob
	MaxOutputRefs   int
	MaxPayloadBytes int
}

type CleanupRequest struct {
	Country        string
	BuildID        uuid.UUID
	Stage          run.Stage
	SubmissionKey  string
	ProviderJobRef string
	Reason         string
}

// CleanupResult reports bounded cleanup facts. Provider cancellation is an
// attempt and never states that a completed external operation was undone.
type CleanupResult struct {
	CancellationRecorded    bool
	ProviderCancelAttempted bool
	ProviderCancelAccepted  bool
}

// ExternalJobs persists each transition. Each mutating call acquires or renews
// durable scheduler ownership and returns its current positive fence. Apply
// validates that fence and atomically commits immutable output and stage
// result. SubmitOrReconcile first reads the durable submission record and
// reconciles by SubmissionKey. It may call a provider only after recording
// intent. When reconciliation is unsupported or inconclusive, it returns
// ExternalUncertain and must not submit again.
type ExternalJobs interface {
	Prepare(context.Context, ExternalPrepareRequest) (ExternalJob, error)
	SubmitOrReconcile(context.Context, ExternalJob) (ExternalJob, error)
	Check(context.Context, ExternalCheckRequest) (ExternalJob, error)
	Apply(context.Context, ExternalApplyRequest) (run.Result, error)
	Cleanup(context.Context, CleanupRequest) (CleanupResult, error)
}

// ActivationService is satisfied by release.PublicationService. The activity
// converts its local reconciliation error into a bounded status field before
// returning workflow data.
type ActivationService interface {
	Activate(context.Context, release.ActivationRequest) (release.ActivationResult, error)
}

type ActivationSummary struct {
	Country                  string
	BuildID                  uuid.UUID
	Outcome                  release.ActivationOutcome
	SelectionVersion         int64
	CurrentReleaseID         string
	PreviousReleaseID        string
	LocalReconciliationIssue bool
}

type ActivationActivityRequest struct {
	Activation      release.ActivationRequest
	MaxPayloadBytes int
}

type WorkflowResult struct {
	Country    string
	RunID      uuid.UUID
	BuildID    uuid.UUID
	Counts     Totals
	Activation ActivationSummary
}

func stageIndex(stage run.Stage) int {
	if stage == "" {
		return 0
	}
	for i, candidate := range fixedStages {
		if candidate == stage {
			return i
		}
	}
	return -1
}

func isExternalStage(stage run.Stage) bool {
	return stage == run.OCR || stage == run.Dense
}

func taskQueue(stage run.Stage) string {
	switch stage {
	case run.Discover, run.Fetch:
		return NetworkTaskQueue
	case run.Extract, run.Normalize, run.Index:
		return CPUTaskQueue
	case run.OCR, run.Dense:
		return ExternalTaskQueue
	case run.Lexical, run.Publish, run.Activate:
		return ExclusiveTaskQueue
	case run.Evaluate:
		return DatabaseTaskQueue
	default:
		return DatabaseTaskQueue
	}
}

func validateCarriedJob(state State) error {
	job := *state.ActiveJob
	if !isExternalStage(state.Phase) || job.Shard.Stage != state.Phase {
		return fmt.Errorf("active external job does not match workflow phase")
	}
	if job.Shard.Execution.Country != state.Country || job.Shard.Execution.RunID != state.RunID ||
		job.Shard.Execution.BuildID != state.BuildID || job.Shard.Recipe != state.Recipe ||
		job.Shard.Budget != state.Budget || job.Shard.ManifestRef != state.ManifestRef ||
		job.Shard.Cursor != state.Cursor {
		return fmt.Errorf("active external job identity differs from workflow")
	}
	if job.Shard.Execution.LeaseFence <= 0 || state.Fence != job.Shard.Execution.LeaseFence {
		return fmt.Errorf("active external job fence differs from workflow")
	}
	if err := job.Shard.Validate(); err != nil {
		return fmt.Errorf("active external job shard: %w", err)
	}
	if err := validateShardPayload(job.Shard, state.MaxPayloadBytes); err != nil {
		return fmt.Errorf("active external job shard: %w", err)
	}
	if job.SubmissionKey != SubmissionKey(job.Shard) {
		return fmt.Errorf("active external job submission key differs")
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
	default:
		return fmt.Errorf("active external job has nonresumable state %q", job.State)
	}
	if externalJobPayloadBytes(job) > state.MaxPayloadBytes {
		return fmt.Errorf("active external job exceeds workflow payload byte limit")
	}
	return nil
}

func payloadBytes(outputs []run.OutputRef) int {
	total := 0
	for _, output := range outputs {
		total += len(output.Ref) + len(output.Digest) + len(output.MediaType)
	}
	return total
}

func externalJobPayloadBytes(job ExternalJob) int {
	return len(job.Shard.Execution.Country) + len(job.Shard.Execution.Owner) +
		len(job.Shard.Stage) + len(job.Shard.Cursor) + len(job.Shard.ManifestRef) +
		len(job.Shard.Recipe) + len(job.SubmissionKey) + len(job.ProviderJobRef) + len(job.ResultRef)
}

func statePayloadBytes(state State) int {
	total := len(state.Country) + len(state.Recipe) + len(state.Phase) + len(state.Cursor) +
		len(state.ManifestRef) + len(state.Activation.Country) + len(state.Activation.ReleaseID) +
		len(state.Activation.ExpectedCurrentReleaseID) + len(state.Activation.ActivatedBy) + payloadBytes(state.OutputRefs)
	if state.ActiveJob != nil {
		total += externalJobPayloadBytes(*state.ActiveJob)
	}
	return total
}

func validCountry(country string) bool {
	return len(country) == 2 && country[0] >= 'a' && country[0] <= 'z' &&
		country[1] >= 'a' && country[1] <= 'z'
}
