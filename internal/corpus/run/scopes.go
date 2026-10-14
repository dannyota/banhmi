package run

import (
	"fmt"
	"strings"

	"github.com/google/uuid"

	"danny.vn/banhmi/internal/corpus/model"
)

// ScopeKind separates intake lifetime from release assembly and maintenance.
type ScopeKind string

const (
	ScopeIntake  ScopeKind = "intake"
	ScopeBatch   ScopeKind = "batch"
	ScopeBuild   ScopeKind = "build"
	ScopeControl ScopeKind = "control"
)

// Valid reports membership in the closed contract vocabulary.
func (k ScopeKind) Valid() bool {
	return k == ScopeIntake || k == ScopeBatch || k == ScopeBuild || k == ScopeControl
}

// WorkflowKind is the closed set of scheduled corpus workflows.
type WorkflowKind string

const (
	SourceSync       WorkflowKind = "SourceSync"
	DocumentIngest   WorkflowKind = "DocumentIngest"
	OcrBatch         WorkflowKind = "OcrBatch"
	EmbedBatch       WorkflowKind = "EmbedBatch"
	CorpusBuild      WorkflowKind = "CorpusBuild"
	Promote          WorkflowKind = "Promote"
	Reprocess        WorkflowKind = "Reprocess"
	ControlReconcile WorkflowKind = "ControlReconcile"
)

// Permits reports the sole execution scope permitted for this workflow kind.
func (k WorkflowKind) Permits(scope ScopeKind) bool {
	switch k {
	case SourceSync, DocumentIngest, Reprocess:
		return scope == ScopeIntake
	case OcrBatch, EmbedBatch:
		return scope == ScopeBatch
	case CorpusBuild:
		return scope == ScopeBuild
	case Promote, ControlReconcile:
		return scope == ScopeControl
	default:
		return false
	}
}

// Valid reports membership in the closed workflow vocabulary.
func (k WorkflowKind) Valid() bool {
	return k.Permits(ScopeIntake) || k.Permits(ScopeBatch) || k.Permits(ScopeBuild) || k.Permits(ScopeControl)
}

// IntakeSubjectKind distinguishes intake lease resources.
type IntakeSubjectKind string

const (
	SubjectDocumentVersion    IntakeSubjectKind = "document_version"
	SubjectSourceWindow       IntakeSubjectKind = "source_window"
	SubjectReprocessSelection IntakeSubjectKind = "reprocess_selection"
)

// IntakeSubject binds one ledger resource to an intake execution and its lease.
// The selection's immutable manifest is pinned by IntakeWork.InputDigest.
type IntakeSubject struct {
	Kind               IntakeSubjectKind
	DocumentVersion    DocumentVersionKey
	SourceWindow       SourceWindowKey
	ReprocessSelection ReprocessSelectionKey
}

// Validate requires exactly the key selected by Kind.
func (s IntakeSubject) Validate() error {
	if s.Kind != SubjectDocumentVersion && s.Kind != SubjectSourceWindow && s.Kind != SubjectReprocessSelection {
		return fmt.Errorf("unknown intake subject")
	}
	if (s.Kind == SubjectDocumentVersion) != (s.DocumentVersion != "") ||
		(s.Kind == SubjectSourceWindow) != (s.SourceWindow != "") ||
		(s.Kind == SubjectReprocessSelection) != (s.ReprocessSelection != "") {
		return fmt.Errorf("intake subject requires exactly its typed key")
	}
	if err := model.ValidateKey(s.LeaseKey()); err != nil {
		return fmt.Errorf("subject key: %w", err)
	}
	return nil
}

// LeaseKey returns the resource key; lease identity also includes Kind and country.
func (s IntakeSubject) LeaseKey() string {
	switch s.Kind {
	case SubjectDocumentVersion:
		return string(s.DocumentVersion)
	case SubjectSourceWindow:
		return string(s.SourceWindow)
	case SubjectReprocessSelection:
		return string(s.ReprocessSelection)
	default:
		return ""
	}
}

// DocumentVersionKey identifies a ledger document version.
type DocumentVersionKey string
// SourceWindowKey identifies a frozen source window.
type SourceWindowKey string
// ReprocessSelectionKey identifies a frozen selection manifest.
type ReprocessSelectionKey string
// BatchCollectionKey identifies a frozen batch collection.
type BatchCollectionKey string
// BuildKey identifies a real corpus build by UUID.
type BuildKey uuid.UUID
// CountryControlKey identifies the country maintenance lease resource.
type CountryControlKey string

// ExecutionScope is a tagged key. Exactly the key selected by Kind is present.
// Intake never has a build; build existence is checked by the transaction adapter.
type ExecutionScope struct {
	Kind            ScopeKind
	Subject         IntakeSubject
	BatchCollection BatchCollectionKey
	BuildID         BuildKey
	CountryControl  CountryControlKey
}

// Validate requires exactly the typed resource selected by the scope kind.
func (s ExecutionScope) Validate() error {
	if !s.Kind.Valid() {
		return fmt.Errorf("unknown execution scope %q", s.Kind)
	}
	if (s.Kind == ScopeIntake) != (s.Subject != (IntakeSubject{})) ||
		(s.Kind == ScopeBatch) != (s.BatchCollection != "") ||
		(s.Kind == ScopeBuild) != (uuid.UUID(s.BuildID) != uuid.Nil) ||
		(s.Kind == ScopeControl) != (s.CountryControl != "") {
		return fmt.Errorf("execution scope requires exactly its typed key")
	}
	var key string
	switch s.Kind {
	case ScopeIntake:
		if err := s.Subject.Validate(); err != nil {
			return fmt.Errorf("intake subject: %w", err)
		}
		key = s.Subject.LeaseKey()
	case ScopeBatch:
		key = string(s.BatchCollection)
	case ScopeControl:
		key = string(s.CountryControl)
	case ScopeBuild:
		key = uuid.UUID(s.BuildID).String()
	}
	if err := model.ValidateKey(key); err != nil {
		return fmt.Errorf("scope key: %w", err)
	}
	return nil
}

// ScopedExecution identifies a workflow attempt under a country admission epoch.
// Claim and reclaim allocate a monotonic AttemptFence; validators cannot prove
// monotonicity or lease liveness, which the adapter must check transactionally.
type ScopedExecution struct {
	Country        string
	WorkflowID     string
	WorkflowKind   WorkflowKind
	AttemptKey     string
	AttemptFence   int64
	AdmissionEpoch int64
	Owner          string
	Scope          ExecutionScope
}

// Validate checks workflow/scope/subject compatibility, epoch and attempt fence.
func (e ScopedExecution) Validate() error {
	if e.Country == "" || e.WorkflowID == "" || e.WorkflowKind == "" || e.AttemptKey == "" || e.Owner == "" {
		return fmt.Errorf("country, workflow identity, attempt key and owner are required")
	}
	if e.AttemptFence <= 0 || e.AdmissionEpoch <= 0 {
		return fmt.Errorf("attempt fence and admission epoch must be positive")
	}
	for name, key := range map[string]string{"country": e.Country, "attempt key": e.AttemptKey, "owner": e.Owner} {
		if err := model.ValidateKey(key); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	if err := validateWorkflowID(e.WorkflowID); err != nil {
		return fmt.Errorf("workflow ID: %w", err)
	}
	if !e.WorkflowKind.Permits(e.Scope.Kind) {
		return fmt.Errorf("workflow kind does not permit execution scope")
	}
	if err := e.Scope.Validate(); err != nil {
		return fmt.Errorf("scope: %w", err)
	}
	if e.Scope.Kind == ScopeIntake {
		expected := map[WorkflowKind]IntakeSubjectKind{SourceSync: SubjectSourceWindow, DocumentIngest: SubjectDocumentVersion, Reprocess: SubjectReprocessSelection}
		if e.Scope.Subject.Kind != expected[e.WorkflowKind] {
			return fmt.Errorf("workflow kind does not permit intake subject")
		}
	}
	if e.Scope.Kind == ScopeControl && string(e.Scope.CountryControl) != e.Country {
		return fmt.Errorf("control key must match country")
	}
	return nil
}

// validateWorkflowID checks bounded path segments, never arbitrary URLs or text.
func validateWorkflowID(id string) error {
	if len(id) > 512 {
		return fmt.Errorf("workflow ID exceeds bound")
	}
	parts := strings.Split(id, "/")
	if len(parts) < 3 {
		return fmt.Errorf("workflow ID requires namespace, country and ledger identity")
	}
	for i, part := range parts {
		if err := model.ValidateKey(part); err != nil {
			return fmt.Errorf("segment %d: %w", i, err)
		}
	}
	return nil
}
