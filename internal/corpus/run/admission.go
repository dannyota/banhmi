package run

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"danny.vn/banhmi/internal/corpus/model"
)

var (
	// ErrInvalidAdmission identifies an invalid request shape.
	ErrInvalidAdmission = errors.New("invalid document admission")
	// ErrAdmissionUnconfigured means no effective limits have been committed.
	ErrAdmissionUnconfigured = errors.New("document admission is unconfigured")
	// ErrAdmissionConflict identifies an obsolete revision or unproved transition.
	ErrAdmissionConflict = errors.New("document admission conflict")
	// ErrAdmissionIntegrity identifies corrupt retained bindings or receipts.
	ErrAdmissionIntegrity = errors.New("document admission integrity failure")
	// ErrAdmissionUnchanged means reconciliation found no applicable transition.
	ErrAdmissionUnchanged = errors.New("document admission unchanged")
)

// DocumentAdmissionLimits bounds durable country occupancy and local receipts.
type DocumentAdmissionLimits struct {
	Processing       int32
	Open             int32
	ReceiptJSONBytes int32
	QuarantineExpiry time.Duration
}

// DefaultDocumentAdmissionLimits returns proposed defaults, not production approval.
func DefaultDocumentAdmissionLimits() DocumentAdmissionLimits {
	return DocumentAdmissionLimits{Processing: 32, Open: 256, ReceiptJSONBytes: 65536, QuarantineExpiry: 7 * 24 * time.Hour}
}

// Validate rejects disabled sentinels, reversed bounds and imprecise expiry.
func (l DocumentAdmissionLimits) Validate() error {
	if l.Processing < 1 || l.Open < l.Processing || l.ReceiptJSONBytes < 1 || l.QuarantineExpiry <= 0 || l.QuarantineExpiry%time.Microsecond != 0 {
		return fmt.Errorf("limits or quarantine expiry: %w", ErrInvalidAdmission)
	}
	return nil
}

// AdmissionRefusal is a normal capacity or queue refusal, never a commit error.
type AdmissionRefusal string

const (
	// AdmissionProcessingFull means processing occupancy prevents an increase.
	AdmissionProcessingFull AdmissionRefusal = "processing_full"
	// AdmissionOpenFull means open occupancy prevents a new launch.
	AdmissionOpenFull AdmissionRefusal = "open_full"
	// AdmissionBothFull means both ceilings prevent a new launch.
	AdmissionBothFull AdmissionRefusal = "both_full"
	// AdmissionResourceBusy means another live attempt owns the version.
	AdmissionResourceBusy AdmissionRefusal = "resource_busy"
	// AdmissionNotTurn means another feasible FIFO head has priority.
	AdmissionNotTurn AdmissionRefusal = "not_turn"
)

// Valid reports membership in the closed refusal vocabulary.
func (r AdmissionRefusal) Valid() bool {
	return r == AdmissionProcessingFull || r == AdmissionOpenFull || r == AdmissionBothFull || r == AdmissionResourceBusy || r == AdmissionNotTurn
}

// AdmissionState describes durable accounting, independently of lease liveness.
type AdmissionState string

const (
	// AdmissionReserved counts an uncertain start in both classes.
	AdmissionReserved AdmissionState = "reserved"
	// AdmissionProcessing counts active processing in both classes.
	AdmissionProcessing AdmissionState = "processing"
	// AdmissionOpenOnly retains open capacity during quarantine or worker loss.
	AdmissionOpenOnly AdmissionState = "open_only"
	// AdmissionClosing retains open capacity until trusted chain closure.
	AdmissionClosing AdmissionState = "closing"
	// AdmissionReleased is a permanent terminal tombstone.
	AdmissionReleased AdmissionState = "released"
)

// Valid reports membership in the closed state vocabulary.
func (s AdmissionState) Valid() bool {
	return s == AdmissionReserved || s == AdmissionProcessing || s == AdmissionOpenOnly || s == AdmissionClosing || s == AdmissionReleased
}

// AdmissionTerminalReason names a verified lifecycle cause, not accepted evidence.
type AdmissionTerminalReason string

const (
	// AdmissionCompleted follows terminal accepted evidence.
	AdmissionCompleted AdmissionTerminalReason = "completed"
	// AdmissionTerminalGap retains an unresolved terminal gap.
	AdmissionTerminalGap AdmissionTerminalReason = "terminal_gap"
	// AdmissionCancelled records a durable cancellation instruction.
	AdmissionCancelled AdmissionTerminalReason = "cancelled"
	// AdmissionExpired records expiry of frozen quarantine time.
	AdmissionExpired AdmissionTerminalReason = "expired"
	// AdmissionAbandoned records a durable stop before uncertain-start draining.
	AdmissionAbandoned AdmissionTerminalReason = "abandoned"
	// AdmissionExecutionFailed records trusted terminal-chain failure.
	AdmissionExecutionFailed AdmissionTerminalReason = "execution_failed"
)

// Valid reports membership in the closed lifecycle vocabulary.
func (r AdmissionTerminalReason) Valid() bool {
	return r == AdmissionCompleted || r == AdmissionTerminalGap || r == AdmissionCancelled || r == AdmissionExpired || r == AdmissionAbandoned || r == AdmissionExecutionFailed
}

// AdmissionReceipt is immutable history, never permission to process.
type AdmissionReceipt struct {
	Key      string
	SlotKey  string
	Revision int64
	State    AdmissionState
}

// AdmissionResult contains exactly one receipt or normal refusal.
type AdmissionResult struct {
	Receipt        *AdmissionReceipt
	Refusal        AdmissionRefusal
	LimitsRevision int64
	Processing     int64
	Open           int64
}

// AdmissionReserveRequest dispatches one already committed source launch intent.
type AdmissionReserveRequest struct {
	Actor       IntakeWork
	Child       IntakeWork
	DeliveryKey string
}

// Validate refuses reprocess dispatchers until a reviewed intent producer exists.
func (r AdmissionReserveRequest) Validate() error {
	if err := admissionActor(r.Actor); err != nil {
		return err
	}
	if r.Actor.Execution.WorkflowKind != SourceSync && r.Actor.Execution.WorkflowKind != ControlReconcile {
		return fmt.Errorf("dispatcher kind: %w", ErrInvalidAdmission)
	}
	if err := admissionDocument(r.Child); err != nil {
		return err
	}
	if r.Actor.Execution.Country != r.Child.Execution.Country || model.ValidateKey(r.DeliveryKey) != nil {
		return fmt.Errorf("launch country or delivery: %w", ErrInvalidAdmission)
	}
	return nil
}

// AdmissionChange selects a slot and fences its current revision.
type AdmissionChange struct {
	Actor            IntakeWork
	SlotKey          string
	ExpectedRevision int64
}

// Validate checks the actor and exact slot/revision shape.
func (r AdmissionChange) Validate() error {
	if err := admissionActor(r.Actor); err != nil {
		return err
	}
	if r.ExpectedRevision < 1 || !admissionSlotKey(r.SlotKey) {
		return fmt.Errorf("slot or revision: %w", ErrInvalidAdmission)
	}
	return nil
}

// AdmissionConfigureRequest changes all bounds under a country control grant.
type AdmissionConfigureRequest struct {
	Actor            IntakeWork
	ExpectedRevision int64
	Limits           DocumentAdmissionLimits
}

// Validate checks the control actor, revision and complete configuration.
func (r AdmissionConfigureRequest) Validate() error {
	if err := admissionActor(r.Actor); err != nil {
		return err
	}
	if r.Actor.Execution.WorkflowKind != ControlReconcile || r.ExpectedRevision < 0 {
		return fmt.Errorf("configuration actor or revision: %w", ErrInvalidAdmission)
	}
	return r.Limits.Validate()
}

// AdmissionCloseRequest supplies only the allowlisted durable lifecycle cause.
type AdmissionCloseRequest struct {
	Change       AdmissionChange
	Reason       AdmissionTerminalReason
	CauseReceipt string
}

// Validate rejects caller assertions of completion or execution failure.
func (r AdmissionCloseRequest) Validate() error {
	if err := r.Change.Validate(); err != nil {
		return err
	}
	switch r.Reason {
	case AdmissionCancelled, AdmissionAbandoned:
		if r.CauseReceipt == "" {
			return nil
		}
	case AdmissionExpired, AdmissionTerminalGap:
		if model.ValidateEnvelopeKey(r.CauseReceipt, "result") == nil {
			return nil
		}
	}
	return fmt.Errorf("close cause: %w", ErrInvalidAdmission)
}

// AdmissionScan is a bounded control sweep in updated-time/ID order.
type AdmissionScan struct {
	Actor          IntakeWork
	AfterUpdatedAt time.Time
	AfterID        int64
	Limit          int32
}

// Validate checks the control grant shape, paired cursor and frozen item budget.
func (r AdmissionScan) Validate() error {
	if err := admissionActor(r.Actor); err != nil {
		return err
	}
	if r.Actor.Execution.WorkflowKind != ControlReconcile || r.Limit < 1 || r.Limit > 1000 || int64(r.Limit) > int64(r.Actor.Budget.MaxItems) || r.AfterID < 0 || (r.AfterID == 0) != r.AfterUpdatedAt.IsZero() || r.AfterUpdatedAt.Nanosecond()%1000 != 0 {
		return fmt.Errorf("scan actor, bound or cursor: %w", ErrInvalidAdmission)
	}
	return nil
}

// AdmissionItem carries one cursor and revision for an independent transaction.
type AdmissionItem struct {
	SlotKey   string
	Revision  int64
	UpdatedAt time.Time
	ID        int64
}

// AdmissionResolveRequest has exactly one registered-work or workflow selector.
type AdmissionResolveRequest struct {
	Actor        IntakeWork
	DocumentWork *IntakeWork
	WorkflowID   string
}

// Validate checks selector syntax; registration and read authority remain transactional.
func (r AdmissionResolveRequest) Validate() error {
	if err := admissionActor(r.Actor); err != nil {
		return err
	}
	if r.Actor.Execution.WorkflowKind != DocumentIngest && r.Actor.Execution.WorkflowKind != ControlReconcile || (r.DocumentWork == nil) == (r.WorkflowID == "") {
		return fmt.Errorf("resolution actor or selector: %w", ErrInvalidAdmission)
	}
	if r.DocumentWork != nil {
		if err := admissionDocument(*r.DocumentWork); err != nil {
			return err
		}
		if r.DocumentWork.Execution.Country != r.Actor.Execution.Country {
			return fmt.Errorf("resolution country: %w", ErrInvalidAdmission)
		}
		return nil
	}
	parts := strings.Split(r.WorkflowID, "/")
	if len(parts) != 4 || parts[0] != "document-ingest" || parts[1] != r.Actor.Execution.Country || model.ValidateKey(parts[2]) != nil || model.ValidateKey(parts[3]) != nil {
		return fmt.Errorf("resolution workflow: %w", ErrInvalidAdmission)
	}
	return nil
}

// AdmissionBinding records the current grant tuple as audit data.
type AdmissionBinding struct {
	ExecutionID int64
	Epoch       int64
	Fence       int64
	Owner       string
}

// AdmissionResolution is a consistent read snapshot, never a grant.
type AdmissionResolution struct {
	Found             bool
	SlotKey           string
	WorkflowID        string
	VersionKey        string
	AttemptKey        string
	Revision          int64
	Binding           AdmissionBinding
	State             AdmissionState
	WaitReason        string
	TerminalReason    AdmissionTerminalReason
	LifecycleReceipt  string
	SettlementReceipt string
	CloseReceipt      string
	ReleaseReceipt    string
	TerminalResultID  int64
	SettledWatermark  int64
	ChainReceipt      string
}

// DocumentAdmission owns independent fenced admission transactions. Every replay
// requires current authority. Acceptance transactions never call this port.
type DocumentAdmission interface {
	Configure(context.Context, AdmissionConfigureRequest) (AdmissionReceipt, error)
	Reserve(context.Context, AdmissionReserveRequest) (AdmissionResult, error)
	Bind(context.Context, AdmissionChange) (AdmissionReceipt, error)
	Settle(context.Context, AdmissionChange) (AdmissionReceipt, error)
	RequestResume(context.Context, AdmissionChange) (AdmissionReceipt, error)
	Resume(context.Context, AdmissionChange) (AdmissionResult, error)
	Close(context.Context, AdmissionCloseRequest) (AdmissionReceipt, error)
	Reconcile(context.Context, AdmissionChange) (AdmissionReceipt, error)
	Resolve(context.Context, AdmissionResolveRequest) (AdmissionResolution, error)
	Scan(context.Context, AdmissionScan) ([]AdmissionItem, error)
}

// DocumentExecutionStatus classifies a trusted logical-chain observation.
type DocumentExecutionStatus string

const (
	// DocumentExecutionRunning means the logical chain remains open.
	DocumentExecutionRunning DocumentExecutionStatus = "running"
	// DocumentExecutionTerminal proves closure including all successors.
	DocumentExecutionTerminal DocumentExecutionStatus = "terminal"
	// DocumentExecutionUnknown supplies no closure proof.
	DocumentExecutionUnknown DocumentExecutionStatus = "unknown"
)

// Valid reports membership in the closed observer vocabulary.
func (s DocumentExecutionStatus) Valid() bool {
	return s == DocumentExecutionRunning || s == DocumentExecutionTerminal || s == DocumentExecutionUnknown
}

// DocumentExecutionObservation retains opaque chain/status references only.
type DocumentExecutionObservation struct {
	WorkflowID   string
	ChainReceipt string
	Status       DocumentExecutionStatus
}

// DocumentExecutionObserver is injected by trusted composition. Observation runs
// outside transactions; the adapter rechecks revision and binding under locks.
type DocumentExecutionObserver interface {
	Observe(context.Context, string) (DocumentExecutionObservation, error)
}

func admissionActor(w IntakeWork) error {
	if err := w.Validate(); err != nil {
		return fmt.Errorf("admission work: %w: %w", ErrInvalidAdmission, err)
	}
	return nil
}

func admissionDocument(w IntakeWork) error {
	if err := admissionActor(w); err != nil {
		return err
	}
	e := w.Execution
	if e.WorkflowKind != DocumentIngest || e.WorkflowID != "document-ingest/"+e.Country+"/"+e.Scope.Subject.LeaseKey()+"/"+e.AttemptKey {
		return fmt.Errorf("document workflow identity: %w", ErrInvalidAdmission)
	}
	return nil
}

func admissionSlotKey(s string) bool {
	if len(s) != 37 || !strings.HasPrefix(s, "slot-") {
		return false
	}
	for _, c := range s[5:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
