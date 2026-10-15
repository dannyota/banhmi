package run

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"danny.vn/banhmi/internal/corpus/model"
)

// ScopeKind separates intake lifetime from release assembly and maintenance.
type ScopeKind string

const (
	// ScopeIntake fences an intake subject.
	ScopeIntake ScopeKind = "intake"
	// ScopeBatch fences a frozen batch collection.
	ScopeBatch ScopeKind = "batch"
	// ScopeBuild fences a corpus build.
	ScopeBuild ScopeKind = "build"
	// ScopeControl fences country maintenance.
	ScopeControl ScopeKind = "control"
)

// Valid reports membership in the closed contract vocabulary.
func (k ScopeKind) Valid() bool {
	return k == ScopeIntake || k == ScopeBatch || k == ScopeBuild || k == ScopeControl
}

// WorkflowKind is the closed set of scheduled corpus workflows.
type WorkflowKind string

const (
	// SourceSync discovers and accounts a source window.
	SourceSync WorkflowKind = "SourceSync"
	// DocumentIngest processes a source document version.
	DocumentIngest WorkflowKind = "DocumentIngest"
	// OcrBatch processes a shared OCR collection.
	OcrBatch WorkflowKind = "OcrBatch"
	// EmbedBatch processes a shared embedding collection.
	EmbedBatch WorkflowKind = "EmbedBatch"
	// CorpusBuild assembles a corpus release.
	CorpusBuild WorkflowKind = "CorpusBuild"
	// Promote activates an approved release selection.
	Promote WorkflowKind = "Promote"
	// Reprocess processes a frozen selection of stored originals.
	Reprocess WorkflowKind = "Reprocess"
	// ControlReconcile reconciles bounded country maintenance work.
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
	// SubjectDocumentVersion selects a ledger document version.
	SubjectDocumentVersion IntakeSubjectKind = "document_version"
	// SubjectSourceWindow selects a frozen source window.
	SubjectSourceWindow IntakeSubjectKind = "source_window"
	// SubjectReprocessSelection selects a frozen reprocessing manifest.
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
	if err := validateWorkflowID(e.WorkflowID, e.WorkflowKind, e.Country); err != nil {
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

// validateWorkflowID checks the kind's namespace, layout and bounded keys.
// Scheduled IDs append a hyphen and Temporal's UTC RFC 3339 action timestamp
// to the final base segment, rather than adding a path segment.
func validateWorkflowID(id string, kind WorkflowKind, country string) error {
	if len(id) > 512 {
		return fmt.Errorf("workflow ID exceeds bound")
	}
	parts := strings.Split(id, "/")
	namespace, count := "", 0
	switch kind {
	case SourceSync:
		namespace, count = "source-sync", 4
		if len(parts) == 5 && parts[3] == "manual" {
			count = 5
		}
	case DocumentIngest:
		namespace, count = "document-ingest", 4
	case OcrBatch:
		namespace, count = "ocr-batch", 3
	case EmbedBatch:
		namespace, count = "embed-batch", 3
	case CorpusBuild:
		namespace, count = "corpus-build", 3
	case Promote:
		namespace, count = "promote", 4
	case Reprocess:
		namespace, count = "reprocess", 3
	case ControlReconcile:
		namespace, count = "corpus-control", 2
	default:
		return fmt.Errorf("unknown workflow kind")
	}
	if len(parts) != count || parts[0] != namespace {
		return fmt.Errorf("workflow ID must match kind namespace and layout")
	}
	if kind == ControlReconcile || (kind == SourceSync && count == 4) {
		last := parts[count-1]
		if strings.ContainsAny(last, "T:Z") {
			if len(last) < 22 || last[len(last)-21] != '-' {
				return fmt.Errorf("invalid scheduler timestamp suffix")
			}
			stamp := last[len(last)-20:]
			t, err := time.Parse(time.RFC3339, stamp)
			if err != nil || t.UTC().Format(time.RFC3339) != stamp {
				return fmt.Errorf("scheduler timestamp requires RFC 3339 UTC seconds")
			}
			parts[count-1] = last[:len(last)-21]
		}
	}
	if parts[1] != country {
		return fmt.Errorf("workflow country must match execution country")
	}
	for i, part := range parts {
		if err := model.ValidateKey(part); err != nil {
			return fmt.Errorf("segment %d: %w", i, err)
		}
	}
	return nil
}
