package run

import (
	"testing"

	"github.com/google/uuid"
)

func TestExecutionScopeKeys(t *testing.T) {
	build := BuildKey(uuid.MustParse("11111111-1111-1111-1111-111111111111"))
	for _, s := range []ExecutionScope{
		{Kind: ScopeIntake, DocumentVersion: "version-1"},
		{Kind: ScopeBatch, BatchCollection: "collection-1"},
		{Kind: ScopeBuild, BuildID: build},
		{Kind: ScopeControl, CountryControl: "vn"},
	} {
		if err := s.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range []ExecutionScope{
		{Kind: ScopeIntake, DocumentVersion: "version-1", BuildID: build},
		{Kind: ScopeBuild},
		{Kind: ScopeBatch, BatchCollection: "collection-1", BuildID: build},
		{Kind: ScopeControl, CountryControl: "vn", BuildID: build},
		{Kind: "unknown"},
		{Kind: ScopeIntake},
	} {
		if err := s.Validate(); err == nil {
			t.Fatalf("accepted invalid scope %+v", s)
		}
	}
}

func TestScopedExecutionValidation(t *testing.T) {
	e := intakeFixture().Work.Execution
	for _, mutate := range []func(*ScopedExecution){
		func(e *ScopedExecution) { e.WorkflowID = "" },
		func(e *ScopedExecution) { e.AttemptKey = "" },
		func(e *ScopedExecution) { e.AttemptFence = 0 },
		func(e *ScopedExecution) { e.AdmissionEpoch = 0 },
		func(e *ScopedExecution) { e.Owner = "" },
		func(e *ScopedExecution) { e.Scope = ExecutionScope{Kind: ScopeControl, CountryControl: "my"} },
	} {
		changed := e
		mutate(&changed)
		if changed.Validate() == nil {
			t.Fatal("accepted invalid execution")
		}
	}
}
