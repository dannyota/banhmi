package temporal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.temporal.io/sdk/activity"
	temporalsdk "go.temporal.io/sdk/temporal"

	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/run"
)

// IntakeStateV2 carries screened references and pinned numeric policy only.
// Only SourceSync has a source deadline. Document budgets remain ledger-owned.
type IntakeStateV2 struct {
	Country string
	WorkflowID string
	Kind run.WorkflowKind
	Work run.IntakeWorkEnvelope
	Config IntakeConfigV2
	Phase string
	SourceDeadline time.Time
	ReminderAt time.Time
	ExpiresAt time.Time
	DecisionEpoch int64
	ReportHash model.Digest
	ReconcileOnly bool
	Stop *IntakeStopReferenceV2 `json:",omitempty"`
}

// Validate checks the entire continuation without treating hints as authority.
func (s IntakeStateV2) Validate() error {
	if s.Config.Validate() != nil || s.Work.Validate() != nil || model.ValidateKey(s.Country) != nil {
		return fmt.Errorf("invalid intake state")
	}
	e := run.ScopedExecution{Country: s.Country, WorkflowID: s.WorkflowID, WorkflowKind: s.Kind,
		AttemptKey: "transport", AttemptFence: 1, AdmissionEpoch: 1, Owner: "transport"}
	switch s.Kind {
	case run.SourceSync:
		e.Scope = run.ExecutionScope{Kind: run.ScopeIntake, Subject: run.IntakeSubject{Kind: run.SubjectSourceWindow, SourceWindow: "transport"}}
	case run.DocumentIngest:
		e.Scope = run.ExecutionScope{Kind: run.ScopeIntake, Subject: run.IntakeSubject{Kind: run.SubjectDocumentVersion, DocumentVersion: "transport"}}
	case run.ControlReconcile:
		e.Scope = run.ExecutionScope{Kind: run.ScopeControl, CountryControl: run.CountryControlKey(s.Country)}
	default:
		return fmt.Errorf("unsupported intake kind")
	}
	if e.Validate() != nil || s.Phase != "" && !intakeOperationV2(s.Phase) || s.DecisionEpoch < 0 ||
		s.Kind != run.SourceSync && !s.SourceDeadline.IsZero() {
		return fmt.Errorf("invalid intake identity or progress")
	}
	if s.Stop != nil && (s.Kind != run.DocumentIngest || s.Stop.Validate() != nil) {
		return fmt.Errorf("invalid intake stop")
	}
	if !s.ExpiresAt.IsZero() {
		if s.DecisionEpoch <= 0 || s.ReportHash.Validate() != nil || s.ReminderAt.After(s.ExpiresAt) {
			return fmt.Errorf("invalid quarantine hints")
		}
	} else if !s.ReminderAt.IsZero() || s.DecisionEpoch != 0 || s.ReportHash != "" {
		return fmt.Errorf("quarantine metadata without hint")
	}
	return intakePayloadV2(s, s.Config)
}

// IntakeStopReferenceV2 screens an existing committed close receipt.
// The private manifest binds its country, workflow, version, attempt and slot.
type IntakeStopReferenceV2 struct {
	Key string
	Manifest model.Digest
}

// Validate checks syntax only. Local hash resolution and control authorize use.
func (r IntakeStopReferenceV2) Validate() error {
	if len(r.Key) != 42 || !strings.HasPrefix(r.Key, "admission-") || r.Manifest.Validate() != nil {
		return fmt.Errorf("invalid stop reference")
	}
	for _, c := range r.Key[10:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return fmt.Errorf("invalid stop reference")
		}
	}
	return nil
}

// MarshalJSON prevents invalid references from entering workflow history.
func (r IntakeStopReferenceV2) MarshalJSON() ([]byte, error) {
	if r.Validate() != nil {
		return nil, fmt.Errorf("invalid stop reference")
	}
	type projection IntakeStopReferenceV2
	return json.Marshal(projection(r))
}

// IntakeCompletionV2 distinguishes corpus receipts from lifecycle completion.
type IntakeCompletionV2 struct {
	Outcome string
	Reason string `json:",omitempty"`
	Receipt *run.IntakeReceiptEnvelope `json:",omitempty"`
}

// MarshalJSON rejects missing evidence and fabricated lifecycle results.
func (r IntakeCompletionV2) MarshalJSON() ([]byte, error) {
	switch r.Outcome {
	case "accepted", "done", "terminal_gap":
		if r.Receipt == nil || r.Receipt.Validate() != nil {
			return nil, fmt.Errorf("completion requires committed result")
		}
	case "stopped", "cancelled", "idle":
		if r.Receipt != nil {
			return nil, fmt.Errorf("lifecycle completion has no corpus result")
		}
	default:
		return nil, fmt.Errorf("invalid completion")
	}
	if r.Reason != "" && !run.AdmissionTerminalReason(r.Reason).Valid() {
		return nil, fmt.Errorf("invalid lifecycle reason")
	}
	type projection IntakeCompletionV2
	return json.Marshal(projection(r))
}

// IntakeDecisionV2 is a wake-up for a decision retained in Vietnam.
type IntakeDecisionV2 struct {
	DecisionRef run.IntakeReceiptEnvelope
	ExpectedEpoch int64
	ReportHash model.Digest
}

// IntakeLaunchV2 projects one committed, reserved source launch.
type IntakeLaunchV2 struct {
	WorkflowID string
	VersionKey string
	AttemptKey string
	Work run.IntakeWorkEnvelope
}

// IntakeNotificationV2 projects a durable notification, not its private contents.
type IntakeNotificationV2 struct {
	WorkflowID string
	Receipt run.IntakeReceiptEnvelope
	Decision *IntakeDecisionV2 `json:",omitempty"`
}

// IntakeFrameV2 is a screened bounded activity response, never an admission grant.
type IntakeFrameV2 struct {
	Work run.IntakeWorkEnvelope
	Receipt *run.IntakeReceiptEnvelope `json:",omitempty"`
	Status string
	Reason string `json:",omitempty"`
	Next string
	Launches []IntakeLaunchV2
	Notifications []IntakeNotificationV2
	WaitUntil time.Time
	DecisionEpoch int64
	ReportHash model.Digest
	QuarantineAt time.Time
}

// IntakeCommandV2 carries references to existing work, intents and decisions.
type IntakeCommandV2 struct {
	State IntakeStateV2
	Operation string
	LaunchLimit int
	Launch *IntakeLaunchV2 `json:",omitempty"`
	Notification *IntakeNotificationV2 `json:",omitempty"`
	Decision *IntakeDecisionV2 `json:",omitempty"`
	Refusal run.AdmissionRefusal `json:",omitempty"`
}

// IntakeChildBindingV2 remains local and binds an immutable source launch.
type IntakeChildBindingV2 struct {
	ObservationVersion string
	DeliveryKey string
	Work run.IntakeWork
	Envelope run.IntakeWorkEnvelope
}

// IntakeAdvanceResultV2 stays in Vietnam; the wrapper admits its children.
type IntakeAdvanceResultV2 struct {
	Frame IntakeFrameV2
	Children []IntakeChildBindingV2
}

// IntakeOperationsV2 owns ordinary domain operations and durable outbox accounting.
// It never owns admission, acceptance, resume authority or transport closure.
// admission-wait commits incomplete/admission_capacity without changing FIFO age,
// then emits country_admission_waiting from persisted eligible demands. Telemetry
// includes country/refusal class only, never subject or reference identifiers.
// Its inputs have already passed checkpoint, identity and processing checks.
type IntakeOperationsV2 interface {
	Advance(context.Context, IntakeCommandV2) (IntakeAdvanceResultV2, error)
}

// IntakeAcceptanceV2 prepares and projects genuine cross-store acceptance.
type IntakeAcceptanceV2 interface {
	Prepare(context.Context, IntakeCommandV2) (run.IntakeApplyRequest, error)
	Project(context.Context, IntakeCommandV2, run.IntakeReceipt) (IntakeFrameV2, error)
}

// IntakePendingChangeV2 is a private durable retry identity, never transport data.
type IntakePendingChangeV2 struct {
	Operation string
	Change run.AdmissionChange
	Resolution run.AdmissionResolution
	PostPhase string
	Reason run.AdmissionTerminalReason
	Cause string
}

// IntakeLocalCheckpointV2 retains actual receipts and work only in Vietnam.
// Frozen budget accounting and registered immutable identities survive rollover.
type IntakeLocalCheckpointV2 struct {
	Work run.IntakeWork
	ExecutionID int64
	Quarantine *run.IntakeReceipt
	Terminal *run.IntakeReceipt
	QuarantineAt time.Time
	DecisionEpoch int64
	Decision *IntakeDecisionV2
	Pending *IntakePendingChangeV2
	ScanTime time.Time
	ScanID int64
	Phase string
}

// IntakeCheckpointV2 resolves hash-verified private checkpoints and retains them.
// Resolve verifies the supplied immutable manifest and registered work identity,
// then reconciles its durable successor chain. Old reference retries see committed
// pending operations and quarantine receipts. Retain atomically preserves that
// chain before returning a screened reference; it never fabricates a ledger key.
// Resolve/Slot must recover actual committed acceptance receipts after a crash
// between Apply and Retain through the retained immutable work/result lookup.
// Admission snapshots and watermarks cannot substitute for that receipt lookup.
// Composition reacquires current grants without resetting any frozen budget.
type IntakeCheckpointV2 interface {
	Resolve(context.Context, run.IntakeWorkEnvelope) (IntakeLocalCheckpointV2, error)
	Retain(context.Context, IntakeLocalCheckpointV2) (run.IntakeWorkEnvelope, error)
}

// IntakeResumeValidationV2 resolves retained decision or worker-loss authority.
// Validate must check authority/action, current fence/epoch, original/candidate/
// report hashes, exact scope and expiry against authoritative local time. It
// rejects old decision bindings after reclaim and missing/stale lifecycle causes.
// A signal, caller boolean or possession of a receipt grants no authority.
type IntakeResumeValidationV2 interface {
	Validate(context.Context, run.IntakeWork, run.AdmissionResolution, *IntakeDecisionV2) error
}

// IntakeLocalStopV2 is a hash-verified private stop with current control work.
type IntakeLocalStopV2 struct {
	Control run.IntakeWork
	Manifest []byte
	Country string
	WorkflowID string
	VersionKey string
	AttemptKey string
	SlotKey string
	Receipt run.AdmissionReceipt
}

// IntakeStopManifestV2 is private, bounded retained data, never workflow input.
type IntakeStopManifestV2 struct {
	Country string
	WorkflowID string
	VersionKey string
	AttemptKey string
	SlotKey string
	CloseReceipt string
}

// IntakeStopProjectionV2 screens only committed closes and resolves their binding.
// Screen resolves the actual immutable close by key, not a reconstructed receipt
// from resolution fields. Resolve obtains live control work locally, never input.
type IntakeStopProjectionV2 interface {
	Screen(context.Context, run.IntakeWork, string) (IntakeStopReferenceV2, error)
	Resolve(context.Context, IntakeStopReferenceV2) (IntakeLocalStopV2, error)
}

// IntakeTransportV2 operates on the exact deterministic ID after authorization.
// Reconnect proves a start. Drain uses reject-duplicate/use-existing, passes the
// screened stop only, requests cancellation and observes the logical chain.
// No transport result itself grants a database release; Reconcile proves closure.
type IntakeTransportV2 interface {
	Reconnect(context.Context, string) error
	Drain(context.Context, string, IntakeStopReferenceV2) error
}

// IntakeControlCheckpointsV2 resolves private per-slot checkpoints for control.
// Slot verifies registered target identity and hash-verified receipt retention.
// Configuration supplies a stable local Configure request or nil for no change;
// uncertain Configure retries retain that identical expected revision and limits.
type IntakeControlCheckpointsV2 interface {
	Slot(context.Context, run.IntakeWork, run.AdmissionItem) (IntakeLocalCheckpointV2, error)
	Configuration(context.Context, run.IntakeWork) (*run.AdmissionConfigureRequest, error)
}

// IntakeActivitiesV2 binds screened transport to local neutral ports.
type IntakeActivitiesV2 struct {
	Queue string
	Operations IntakeOperationsV2
	Inputs IntakeAcceptanceV2
	Acceptance run.IntakeUnitOfWork
	Admission run.DocumentAdmission
	Checkpoints IntakeCheckpointV2
	ResumeValidation IntakeResumeValidationV2
	Stops IntakeStopProjectionV2
	Transport IntakeTransportV2
	ControlCheckpoints IntakeControlCheckpointsV2
}

func intakeOperationV2(op string) bool {
	switch op {
	case "resume", "discover", "decode", "discovery", "fetch", "capture", "detect", "convert",
		"normalize", "structure", "gate", "candidate", "quarantine", "evidence", "ocr-request",
		"decision", "reminder", "expiry", "cancel", "checkpoint", "started", "reconnect", "control", "delivered", "drain":
		return true
	default:
		return false
	}
}

func intakeQueueV2(op string) string {
	switch op {
	case "discover", "fetch":
		return FetchTaskQueueV2
	case "decode", "detect", "convert", "normalize", "gate":
		return ConverterTaskQueueV2
	case "structure":
		return ParserTaskQueueV2
	default:
		return DatabaseTaskQueueV2
	}
}

func validateChildExecutionV2(e run.ScopedExecution, version string) error {
	if e.Validate() != nil || e.WorkflowKind != run.DocumentIngest || version != e.Scope.Subject.LeaseKey() ||
		e.WorkflowID != "document-ingest/"+e.Country+"/"+version+"/"+e.AttemptKey {
		return fmt.Errorf("invalid child binding")
	}
	return nil
}

func validateLaunchV2(l IntakeLaunchV2, country string) error {
	if model.ValidateKey(l.VersionKey) != nil || model.ValidateKey(l.AttemptKey) != nil || l.Work.Validate() != nil ||
		l.WorkflowID != "document-ingest/"+country+"/"+l.VersionKey+"/"+l.AttemptKey {
		return fmt.Errorf("invalid launch envelope")
	}
	return nil
}

func validateFrameV2(f IntakeFrameV2, s IntakeStateV2) error {
	if f.Work.Validate() != nil || f.Receipt != nil && f.Receipt.Validate() != nil {
		return fmt.Errorf("invalid checkpoint or receipt")
	}
	switch f.Status {
	case "progress", "wait", "quarantined":
	case "done", "accepted", "terminal_gap":
		if f.Receipt == nil {
			return fmt.Errorf("missing committed result")
		}
	case "idle", "stopped", "cancelled":
		if f.Receipt != nil {
			return fmt.Errorf("unexpected lifecycle result")
		}
	default:
		return fmt.Errorf("invalid status")
	}
	if f.Reason != "" && !run.AdmissionTerminalReason(f.Reason).Valid() || f.Next != "" && !intakeOperationV2(f.Next) {
		return fmt.Errorf("invalid transition")
	}
	if len(f.Launches) > s.Config.ChildGroup || len(f.Notifications) > s.Config.Notifications ||
		len(f.Launches)+len(f.Notifications) > s.Config.References {
		return fmt.Errorf("reference ceiling")
	}
	seen := make(map[string]bool)
	for _, l := range f.Launches {
		if validateLaunchV2(l, s.Country) != nil || seen[l.WorkflowID] {
			return fmt.Errorf("invalid launch")
		}
		seen[l.WorkflowID] = true
	}
	for _, n := range f.Notifications {
		p := strings.Split(n.WorkflowID, "/")
		if len(p) != 4 || p[0] != "document-ingest" || p[1] != s.Country || model.ValidateKey(p[2]) != nil ||
			model.ValidateKey(p[3]) != nil || n.Receipt.Validate() != nil {
			return fmt.Errorf("invalid notification")
		}
		if n.Decision != nil && (n.Decision.DecisionRef != n.Receipt || n.Decision.ExpectedEpoch <= 0 || n.Decision.ReportHash.Validate() != nil) {
			return fmt.Errorf("invalid decision notification")
		}
	}
	if f.Status == "quarantined" && (f.DecisionEpoch <= 0 || f.ReportHash.Validate() != nil || f.QuarantineAt.IsZero()) ||
		f.Status == "wait" && f.WaitUntil.IsZero() {
		return fmt.Errorf("missing wait binding")
	}
	return intakePayloadV2(f, s.Config)
}

func intakeFailureV2(code string, retry bool) error {
	if retry {
		return temporalsdk.NewApplicationError("intake infrastructure unavailable", code)
	}
	return temporalsdk.NewNonRetryableApplicationError("intake operation rejected", code, nil)
}

// Advance validates transport and owns admission before dispatching domain work.
func (a *IntakeActivitiesV2) Advance(ctx context.Context, cmd IntakeCommandV2) (IntakeFrameV2, error) {
	if a == nil || cmd.State.Validate() != nil || !intakeOperationV2(cmd.Operation) || a.Queue != intakeQueueV2(cmd.Operation) ||
		intakePayloadV2(cmd, cmd.State.Config) != nil || !intakePermitsV2(cmd.State.Kind, cmd.Operation) ||
		cmd.State.ReconcileOnly && (cmd.Operation == "discover" || cmd.Operation == "fetch") {
		return IntakeFrameV2{}, intakeFailureV2("invalid_intake_command", false)
	}
	if ((cmd.Operation == "started" || cmd.Operation == "reconnect") != (cmd.Launch != nil)) ||
		(cmd.Operation == "delivered") != (cmd.Notification != nil) || (cmd.Operation == "decision") != (cmd.Decision != nil) ||
		(cmd.Operation == "drain") != (cmd.State.Stop != nil) ||
		cmd.Refusal != "" || cmd.LaunchLimit < 0 || cmd.LaunchLimit > cmd.State.Config.ChildGroup ||
		cmd.Launch != nil && validateLaunchV2(*cmd.Launch, cmd.State.Country) != nil ||
		cmd.Decision != nil && (cmd.Decision.DecisionRef.Validate() != nil || cmd.Decision.ExpectedEpoch <= 0 || cmd.Decision.ReportHash.Validate() != nil || cmd.Decision.ExpectedEpoch != cmd.State.DecisionEpoch || cmd.Decision.ReportHash != cmd.State.ReportHash) {
		return IntakeFrameV2{}, intakeFailureV2("invalid_intake_fields", false)
	}
	if cmd.Notification != nil && validateFrameV2(IntakeFrameV2{Work: cmd.State.Work, Status: "progress", Notifications: []IntakeNotificationV2{*cmd.Notification}}, cmd.State) != nil {
		return IntakeFrameV2{}, intakeFailureV2("invalid_intake_notification", false)
	}
	if a.Admission == nil || a.Checkpoints == nil {
		return IntakeFrameV2{}, intakeFailureV2("missing_admission_binding", false)
	}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		ticker := time.NewTicker(cmd.State.Config.Heartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				activity.RecordHeartbeat(ctx)
			case <-stop:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	frame, err := a.advance(ctx, cmd)
	if err != nil {
		if ctx.Err() != nil {
			return IntakeFrameV2{}, temporalsdk.NewCanceledError()
		}
		var transient *TransientError
		return IntakeFrameV2{}, intakeFailureV2("intake_port_failure", errors.As(err, &transient))
	}
	if validateFrameV2(frame, cmd.State) != nil {
		return IntakeFrameV2{}, intakeFailureV2("invalid_intake_response", false)
	}
	return frame, nil
}

func (a *IntakeActivitiesV2) advance(ctx context.Context, cmd IntakeCommandV2) (IntakeFrameV2, error) {
	if cmd.Operation == "drain" {
		return a.drain(ctx, cmd)
	}
	cp, err := a.Checkpoints.Resolve(ctx, cmd.State.Work)
	if err != nil {
		return IntakeFrameV2{}, err
	}
	e := cp.Work.Execution
	if cp.ExecutionID < 1 || cp.Work.Validate() != nil || e.Country != cmd.State.Country || e.WorkflowID != cmd.State.WorkflowID || e.WorkflowKind != cmd.State.Kind {
		return IntakeFrameV2{}, fmt.Errorf("checkpoint identity mismatch")
	}
	if cmd.Operation == "checkpoint" {
		ref, err := a.Checkpoints.Retain(ctx, cp)
		return IntakeFrameV2{Work: ref, Status: "progress"}, err
	}
	if cmd.State.Kind == run.ControlReconcile && cmd.Operation == "control" {
		if err := a.reconcileSweep(ctx, cmd, &cp); err != nil {
			return IntakeFrameV2{}, err
		}
	}
	if cmd.State.Kind == run.DocumentIngest {
		frame, handled, err := a.documentAdmission(ctx, cmd, &cp)
		if errors.Is(err, run.ErrAdmissionConflict) || errors.Is(err, run.ErrAdmissionUnchanged) {
			return a.waiting(ctx, cmd, cp)
		}
		if err != nil || handled {
			return frame, err
		}
	}
	if cmd.Operation == "reconnect" {
		if a.Transport == nil {
			return IntakeFrameV2{}, fmt.Errorf("missing transport")
		}
		if err := a.Transport.Reconnect(ctx, cmd.Launch.WorkflowID); err != nil {
			return IntakeFrameV2{}, err
		}
	}
	if cmd.Operation == "candidate" || cmd.Operation == "quarantine" || cmd.Operation == "evidence" {
		return a.apply(ctx, cmd, &cp)
	}
	return a.domain(ctx, cmd, cp)
}

func (a *IntakeActivitiesV2) domain(ctx context.Context, cmd IntakeCommandV2, cp IntakeLocalCheckpointV2) (IntakeFrameV2, error) {
	if a.Operations == nil {
		return IntakeFrameV2{}, fmt.Errorf("missing domain binding")
	}
	result, err := a.Operations.Advance(ctx, cmd)
	if err != nil {
		return IntakeFrameV2{}, err
	}
	f := result.Frame
	if f.Status == "accepted" || f.Status == "quarantined" || f.Status == "terminal_gap" || len(f.Launches) != 0 ||
		cmd.State.Kind == run.DocumentIngest && (f.Status == "done" || f.Status == "idle" || f.Status == "stopped" || f.Status == "cancelled") ||
		len(result.Children) > cmd.LaunchLimit || len(result.Children) != 0 && cmd.State.Kind == run.DocumentIngest {
		return IntakeFrameV2{}, fmt.Errorf("domain bypass")
	}
	for _, child := range result.Children {
		e := child.Work.Execution
		if child.Work.Validate() != nil || validateChildExecutionV2(e, child.ObservationVersion) != nil || e.Country != cmd.State.Country ||
			child.Envelope.Validate() != nil || model.ValidateKey(child.DeliveryKey) != nil || strings.HasPrefix(e.AttemptKey, "recovery-") {
			return IntakeFrameV2{}, fmt.Errorf("invalid or disabled child binding")
		}
		admitted, err := a.Admission.Reserve(ctx, run.AdmissionReserveRequest{Actor: cp.Work, Child: child.Work, DeliveryKey: child.DeliveryKey})
		if err != nil {
			return IntakeFrameV2{}, err
		}
		if admitted.Refusal != "" {
			if !admitted.Refusal.Valid() || admitted.Receipt != nil {
				return IntakeFrameV2{}, fmt.Errorf("invalid refusal")
			}
			wait := cmd
			wait.Operation = "admission-wait"
			wait.Refusal = admitted.Refusal
			accounted, err := a.Operations.Advance(ctx, wait)
			if err != nil || len(accounted.Children) != 0 || len(accounted.Frame.Launches) != 0 {
				return IntakeFrameV2{}, fmt.Errorf("incomplete accounting: %w", err)
			}
			// Keep already reserved children in this response; refused and later
			// intents remain durable and unacknowledged. Never lose a reservation.
			f.Status, f.Next, f.WaitUntil = "wait", "resume", time.Now().UTC().Add(cmd.State.Config.Reconcile)
			break
		}
		if admitted.Receipt == nil || admitted.Receipt.State != run.AdmissionReserved {
			// A replay can return the original reserve receipt after later state;
			// its reserved state is history, not processing permission.
			return IntakeFrameV2{}, fmt.Errorf("missing reservation")
		}
		f.Launches = append(f.Launches, IntakeLaunchV2{WorkflowID: e.WorkflowID, VersionKey: child.ObservationVersion, AttemptKey: e.AttemptKey, Work: child.Envelope})
	}
	if cmd.State.Kind == run.DocumentIngest && f.Status == "progress" {
		cp.Phase = f.Next
		return a.retain(ctx, cp, f)
	}
	return f, nil
}

func intakePermitsV2(kind run.WorkflowKind, op string) bool {
	if op == "resume" || op == "checkpoint" || op == "cancel" {
		return true
	}
	switch kind {
	case run.SourceSync:
		return op == "discover" || op == "decode" || op == "discovery" || op == "started" || op == "reconnect" || op == "control"
	case run.ControlReconcile:
		return op == "control" || op == "started" || op == "reconnect" || op == "delivered"
	case run.DocumentIngest:
		return op == "fetch" || op == "capture" || op == "detect" || op == "convert" || op == "normalize" || op == "structure" ||
			op == "gate" || op == "candidate" || op == "quarantine" || op == "evidence" || op == "ocr-request" || op == "decision" || op == "reminder" || op == "expiry" || op == "drain"
	default:
		return false
	}
}
