package run

import (
	"fmt"

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

func (k ScopeKind) Valid() bool {
	return k == ScopeIntake || k == ScopeBatch || k == ScopeBuild || k == ScopeControl
}

type DocumentVersionKey string
type BatchCollectionKey string
type BuildKey uuid.UUID
type CountryControlKey string

// ExecutionScope is a tagged key. Exactly the key selected by Kind is present.
// Intake never has a build; build existence is checked by the transaction adapter.
type ExecutionScope struct {
	Kind            ScopeKind
	DocumentVersion DocumentVersionKey
	BatchCollection BatchCollectionKey
	BuildID         BuildKey
	CountryControl  CountryControlKey
}

func (s ExecutionScope) Validate() error {
	if !s.Kind.Valid() {
		return fmt.Errorf("unknown execution scope %q", s.Kind)
	}
	if (s.Kind == ScopeIntake) != (s.DocumentVersion != "") ||
		(s.Kind == ScopeBatch) != (s.BatchCollection != "") ||
		(s.Kind == ScopeBuild) != (uuid.UUID(s.BuildID) != uuid.Nil) ||
		(s.Kind == ScopeControl) != (s.CountryControl != "") {
		return fmt.Errorf("execution scope requires exactly its typed key")
	}
	var key string
	switch s.Kind {
	case ScopeIntake:
		key = string(s.DocumentVersion)
	case ScopeBatch:
		key = string(s.BatchCollection)
	case ScopeControl:
		key = string(s.CountryControl)
	case ScopeBuild:
		key = uuid.UUID(s.BuildID).String()
	}
	if err := model.ValidateKey(key); err != nil {
		return err
	}
	return nil
}

// ScopedExecution identifies a workflow attempt under a country admission epoch.
// Claim and reclaim allocate a monotonic AttemptFence; validators cannot prove
// monotonicity or lease liveness, which the adapter must check transactionally.
type ScopedExecution struct {
	Country        string
	WorkflowID     string
	WorkflowKind   string
	AttemptKey     string
	AttemptFence   int64
	AdmissionEpoch int64
	Owner          string
	Scope          ExecutionScope
}

func (e ScopedExecution) Validate() error {
	if e.Country == "" || e.WorkflowID == "" || e.WorkflowKind == "" || e.AttemptKey == "" || e.Owner == "" {
		return fmt.Errorf("country, workflow identity, attempt key and owner are required")
	}
	if e.AttemptFence <= 0 || e.AdmissionEpoch <= 0 {
		return fmt.Errorf("attempt fence and admission epoch must be positive")
	}
	for _, key := range []string{e.Country, e.WorkflowID, e.WorkflowKind, e.AttemptKey, e.Owner} {
		if err := model.ValidateKey(key); err != nil {
			return err
		}
	}
	if err := e.Scope.Validate(); err != nil {
		return err
	}
	if e.Scope.Kind == ScopeControl && string(e.Scope.CountryControl) != e.Country {
		return fmt.Errorf("control key must match country")
	}
	return nil
}
