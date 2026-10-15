package temporal

import (
	"encoding/json"
	"fmt"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"danny.vn/banhmi/internal/corpus/conversion/sandbox"
	"danny.vn/banhmi/internal/corpus/model"
)

const (
	// A conservative protocol floor covers bootstrap and checkpoint commands.
	// Runtime headroom checks still fail closed; M1.14 measures actual histories.
	intakeBootstrapEventFloorV2 = 16
	// SourceSyncNameV2 identifies the versioned discovery workflow.
	SourceSyncNameV2 = "corpus.source-sync.v2"
	// DocumentIngestNameV2 identifies the versioned document workflow.
	DocumentIngestNameV2 = "corpus.document-ingest.v2"
	// ControlReconcileNameV2 identifies the versioned country reconciler.
	ControlReconcileNameV2 = "corpus.control.v2"
	// CoordinatorTaskQueueV2 carries only workflow tasks.
	CoordinatorTaskQueueV2 = "corpus-coordinator-v2"
	// FetchTaskQueueV2 carries network-only discovery and capture work.
	FetchTaskQueueV2 = "corpus-fetch-v2"
	// ConverterTaskQueueV2 carries every operation that reads untrusted bytes.
	ConverterTaskQueueV2 = "corpus-converter-v2"
	// ParserTaskQueueV2 carries structure operations on validated blocks only.
	ParserTaskQueueV2 = "corpus-parser-v2"
	// OCRTaskQueueV2 carries provider controllers over prepared requests only.
	OCRTaskQueueV2 = "corpus-ocr-client-v2"
	// EmbeddingTaskQueueV2 carries bulk embedding provider controllers.
	EmbeddingTaskQueueV2 = "corpus-embedding-client-v2"
	// DatabaseTaskQueueV2 carries fenced ledger activities.
	DatabaseTaskQueueV2 = "corpus-database-v2"
	// IntakeAdvanceNameV2 identifies the reference-only activity binding.
	IntakeAdvanceNameV2 = "corpus.intake-advance.v2"
)

// IntakeConfigV2 pins positive bounds in workflow input. Every default returned
// by ProposedIntakeConfigV2 is proposed and unapproved for production. Changes
// apply to new attempts, never through mutable configuration reads on replay.
// Sandbox limits apply to all delegated byte readers, not just conversion.
type IntakeConfigV2 struct {
	ChildGroup           int
	ChildStarts          int
	Pages                int
	Processing           int
	Open                 int
	PendingActivities    int
	Notifications        int
	Signals              int
	HistoryEvents        int
	HistoryEventReserve  int
	HistoryBytes         int
	EnvelopeBytes        int
	References           int
	TransactionBytes     int
	CommandOverheadBytes int
	ActionsPerSecond     int
	StartsPerSecond      int
	Sandboxes            int
	OCRJobs              int
	EmbeddingJobs        int
	Builds               int
	Promotions           int
	SourceBurst          int
	SourceConnections    int
	SourceInFlight       int
	AdmissionScanItems   int
	TemporalMonthlyAlertCents int
	SourceAttempts       int
	InfrastructureTries  int
	CoordinatorSlots     int
	FetchSlots           int
	ConverterSlots       int
	ParserSlots          int
	OCRSlots             int
	EmbeddingSlots       int
	DatabaseSlots        int
	Reminder             time.Duration
	Expiry               time.Duration
	SourceDeadline       time.Duration
	DailySync            time.Duration
	WeeklyValidity       time.Duration
	Reconcile            time.Duration
	Catchup              time.Duration
	SourceInterval       time.Duration
	SourceRetryWindow    time.Duration
	ControlAttempt       time.Duration
	WorkAttempt          time.Duration
	Heartbeat            time.Duration
	HeartbeatTimeout     time.Duration
	RetryInitial         time.Duration
	RetryMaximum         time.Duration
	Poll                 time.Duration
	Sandbox              sandbox.Limits
}

// ProposedIntakeConfigV2 returns unmeasured, proposed defaults. Production
// bundle values and schedule creation require the owner's O7/O8 approval.
func ProposedIntakeConfigV2() IntakeConfigV2 {
	return IntakeConfigV2{
		ChildGroup: 16, ChildStarts: 100, Pages: 20, Processing: 32, Open: 256,
		PendingActivities: 16, Notifications: 32, Signals: 500,
		HistoryEventReserve: 256, SourceInFlight: 1, AdmissionScanItems: 100, TemporalMonthlyAlertCents: 2500,
		HistoryEvents: 2000, HistoryBytes: 4 << 20, EnvelopeBytes: 64 << 10,
		References: 32, TransactionBytes: 3 << 20, CommandOverheadBytes: 16 << 10,
		ActionsPerSecond: 99, StartsPerSecond: 9, Sandboxes: 2, OCRJobs: 1,
		EmbeddingJobs: 1, Builds: 1, Promotions: 1, SourceBurst: 1, SourceConnections: 2,
		SourceAttempts: 5, InfrastructureTries: 3, CoordinatorSlots: 2, FetchSlots: 8,
		ConverterSlots: 1, ParserSlots: 2, OCRSlots: 2, EmbeddingSlots: 2, DatabaseSlots: 4,
		Reminder: 24 * time.Hour, Expiry: 7 * 24 * time.Hour, SourceDeadline: 24 * time.Hour,
		DailySync: 24 * time.Hour, WeeklyValidity: 7 * 24 * time.Hour,
		Reconcile: 15 * time.Minute, Catchup: 24 * time.Hour, SourceInterval: time.Second,
		SourceRetryWindow: 24 * time.Hour, ControlAttempt: time.Minute, WorkAttempt: 5 * time.Minute,
		Heartbeat: 10 * time.Second, HeartbeatTimeout: 30 * time.Second,
		RetryInitial: time.Second, RetryMaximum: 30 * time.Second, Poll: time.Minute,
		Sandbox: sandbox.PDFLimits(),
	}
}

// Validate rejects missing bounds, invalid relationships and values that reach
// published service boundaries. Configured limits may be stricter than defaults.
func (c IntakeConfigV2) Validate() error {
	ints := []int{c.ChildGroup, c.ChildStarts, c.Pages, c.Processing, c.Open, c.PendingActivities,
		c.Notifications, c.Signals, c.HistoryEvents, c.HistoryBytes, c.EnvelopeBytes, c.References,
		c.TransactionBytes, c.CommandOverheadBytes, c.ActionsPerSecond, c.StartsPerSecond,
		c.Sandboxes, c.OCRJobs, c.EmbeddingJobs, c.Builds, c.Promotions, c.SourceBurst,
		c.SourceConnections, c.SourceAttempts, c.InfrastructureTries, c.CoordinatorSlots,
		c.FetchSlots, c.ConverterSlots, c.ParserSlots, c.OCRSlots, c.EmbeddingSlots, c.DatabaseSlots}
	ints = append(ints, c.HistoryEventReserve, c.SourceInFlight, c.AdmissionScanItems, c.TemporalMonthlyAlertCents)
	for _, n := range ints {
		if n <= 0 {
			return fmt.Errorf("positive intake bounds required")
		}
	}
	for _, d := range []time.Duration{c.Reminder, c.Expiry, c.SourceDeadline, c.DailySync, c.WeeklyValidity,
		c.Reconcile, c.Catchup, c.SourceInterval, c.SourceRetryWindow, c.ControlAttempt,
		c.WorkAttempt, c.Heartbeat, c.HeartbeatTimeout, c.RetryInitial, c.RetryMaximum, c.Poll} {
		if d <= 0 {
			return fmt.Errorf("positive intake durations required")
		}
	}
	if c.HistoryEventReserve >= c.HistoryEvents || c.TransactionBytes >= c.HistoryBytes || c.AdmissionScanItems > 1000 ||
		c.SourceInFlight > c.SourceConnections || c.ChildGroup > c.PendingActivities || c.ChildGroup > c.ChildStarts || c.Processing > c.Open ||
		c.ChildGroup > c.References || c.PendingActivities >= 500 || c.ChildStarts >= 500 ||
		c.Notifications >= 500 || c.Signals >= 10000 || c.HistoryEvents >= 10240 ||
		c.HistoryBytes >= 10<<20 || c.EnvelopeBytes >= 2<<20 || c.TransactionBytes >= 4<<20 ||
		c.ActionsPerSecond >= 100 || c.StartsPerSecond >= 10 || c.Expiry <= c.Reminder ||
		c.HeartbeatTimeout <= c.Heartbeat || c.RetryMaximum < c.RetryInitial || c.InfrastructureTries > 3 ||
		c.SourceAttempts > 5 || c.SourceRetryWindow > 24*time.Hour || c.ControlAttempt > time.Minute {
		return fmt.Errorf("intake bounds violate service or retry relationships")
	}
	if c.HistoryEventReserve < intakeBootstrapEventFloorV2 || c.HistoryEvents-c.HistoryEventReserve < intakeBootstrapEventFloorV2 {
		return fmt.Errorf("insufficient bootstrap event headroom")
	}
	// Divide before multiplying so hostile configuration cannot overflow int.
	if c.CommandOverheadBytes >= c.TransactionBytes ||
		c.EnvelopeBytes >= c.TransactionBytes-c.CommandOverheadBytes ||
		c.ChildGroup+1 > (c.TransactionBytes-1)/(c.EnvelopeBytes+c.CommandOverheadBytes) ||
		c.Notifications+1 > (c.TransactionBytes-1)/(c.EnvelopeBytes+c.CommandOverheadBytes) {
		return fmt.Errorf("combined child commands exceed transaction reserve")
	}
	if c.HistoryBytes-c.TransactionBytes < c.EnvelopeBytes+c.CommandOverheadBytes {
		return fmt.Errorf("insufficient bootstrap byte headroom")
	}
	if err := c.Sandbox.Validate(); err != nil {
		return fmt.Errorf("sandbox bounds: %w", err)
	}
	return nil
}

func intakePayloadV2(value any, c IntakeConfigV2) error {
	b, err := json.Marshal(value)
	if err != nil || len(b) > c.EnvelopeBytes {
		return fmt.Errorf("invalid or oversized intake envelope")
	}
	return nil
}

// IntakeStartOptionsV2 configures deterministic duplicate-safe root starts.
// Ledger uniqueness remains authoritative after Temporal history expires.
func IntakeStartOptionsV2(s IntakeStateV2) (client.StartWorkflowOptions, error) {
	if err := s.Validate(); err != nil {
		return client.StartWorkflowOptions{}, err
	}
	return client.StartWorkflowOptions{ID: s.WorkflowID, TaskQueue: CoordinatorTaskQueueV2,
		WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
		WorkflowIDConflictPolicy: enumspb.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING}, nil
}

// RegisterIntakeWorkflowsV2 adds pinned workflows without replacing v1 names.
func RegisterIntakeWorkflowsV2(r worker.WorkflowRegistry) {
	r.RegisterWorkflowWithOptions(SourceSyncV2, workflow.RegisterOptions{Name: SourceSyncNameV2, VersioningBehavior: workflow.VersioningBehaviorPinned})
	r.RegisterWorkflowWithOptions(DocumentIngestV2, workflow.RegisterOptions{Name: DocumentIngestNameV2, VersioningBehavior: workflow.VersioningBehaviorPinned})
	r.RegisterWorkflowWithOptions(ControlReconcileV2, workflow.RegisterOptions{Name: ControlReconcileNameV2, VersioningBehavior: workflow.VersioningBehaviorPinned})
}

// RegisterIntakeActivitiesV2 binds the activity on a single resource worker.
// Composition supplies that worker's immutable queue binding, never a caller.
func RegisterIntakeActivitiesV2(r worker.ActivityRegistry, a *IntakeActivitiesV2) {
	r.RegisterActivityWithOptions(a.Advance, activity.RegisterOptions{Name: IntakeAdvanceNameV2})
}

// IntakeQueueBindingV2 is a resource queue's proposed process configuration.
// All seven queues must use the identical deployment and immutable image ID.
// Composition owns worker creation and retains old images for durable waits.
type IntakeQueueBindingV2 struct {
	Queue   string
	Options worker.Options
}

// IntakeQueueBindingsV2 returns pinned deployment options without starting a
// worker or connecting to Temporal. It does not create schedules or workers.
// Converter and provider limits also require global ledger admission; process
// slots alone cannot enforce a shared country ceiling across worker replicas.
func IntakeQueueBindingsV2(c IntakeConfigV2, deployment, image string) ([]IntakeQueueBindingV2, error) {
	if c.Validate() != nil || model.ValidateKey(deployment) != nil || model.ValidateKey(image) != nil {
		return nil, fmt.Errorf("valid bounds, deployment and immutable image ID required")
	}
	version := worker.DeploymentOptions{UseVersioning: true,
		Version:                   worker.WorkerDeploymentVersion{DeploymentName: deployment, BuildID: image},
		DefaultVersioningBehavior: workflow.VersioningBehaviorPinned}
	bindings := []IntakeQueueBindingV2{{Queue: CoordinatorTaskQueueV2,
		Options: worker.Options{MaxConcurrentWorkflowTaskExecutionSize: c.CoordinatorSlots, DeploymentOptions: version}}}
	for _, pool := range []struct {
		queue string
		slots int
	}{
		{FetchTaskQueueV2, c.FetchSlots}, {ConverterTaskQueueV2, c.ConverterSlots},
		{ParserTaskQueueV2, c.ParserSlots}, {OCRTaskQueueV2, c.OCRSlots},
		{EmbeddingTaskQueueV2, c.EmbeddingSlots}, {DatabaseTaskQueueV2, c.DatabaseSlots},
	} {
		bindings = append(bindings, IntakeQueueBindingV2{Queue: pool.queue,
			Options: worker.Options{MaxConcurrentActivityExecutionSize: pool.slots, DeploymentOptions: version}})
	}
	return bindings, nil
}
