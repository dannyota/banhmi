package run

import (
	"testing"

	"github.com/google/uuid"
)

func TestExecutionScopeKeys(t *testing.T) {
	build := BuildKey(uuid.MustParse("11111111-1111-1111-1111-111111111111"))
	for _, s := range []ExecutionScope{
		{Kind: ScopeIntake, Subject: IntakeSubject{Kind: SubjectDocumentVersion, DocumentVersion: "version-1"}},
		{Kind: ScopeBatch, BatchCollection: "collection-1"},
		{Kind: ScopeBuild, BuildID: build},
		{Kind: ScopeControl, CountryControl: "vn"},
	} {
		if err := s.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range []ExecutionScope{
		{Kind: ScopeIntake, Subject: IntakeSubject{Kind: SubjectDocumentVersion, DocumentVersion: "version-1"}, BuildID: build},
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

func TestWorkflowScopeMatrixAndIntakeSubjects(t *testing.T) {
	for kind, permitted := range map[WorkflowKind]ScopeKind{SourceSync: ScopeIntake, DocumentIngest: ScopeIntake, Reprocess: ScopeIntake, OcrBatch: ScopeBatch, EmbedBatch: ScopeBatch, CorpusBuild: ScopeBuild, Promote: ScopeControl, ControlReconcile: ScopeControl} {
		for _, scope := range []ScopeKind{ScopeIntake, ScopeBatch, ScopeBuild, ScopeControl, "unknown"} {
			if kind.Permits(scope) != (scope == permitted) {
				t.Fatalf("unexpected scope permission for %s/%s", kind, scope)
			}
		}
	}
	if WorkflowKind("unknown").Valid() || WorkflowKind("unknown").Permits(ScopeIntake) {
		t.Fatal("accepted invented workflow")
	}
	for _, subject := range []IntakeSubject{
		{Kind: SubjectDocumentVersion, DocumentVersion: "version"},
		{Kind: SubjectSourceWindow, SourceWindow: "window"},
		{Kind: SubjectReprocessSelection, ReprocessSelection: "selection"},
	} {
		r := intakeFixture()
		r.Work.Execution.Scope.Subject = subject
		switch subject.Kind {
		case SubjectSourceWindow:
			r.Work.Execution.WorkflowKind = SourceSync
		case SubjectReprocessSelection:
			r.Work.Execution.WorkflowKind = Reprocess
		}
		r.IdempotencyKey = IntakeIdempotencyKey(r.Work)
		if err := r.Validate(); err != nil {
			t.Fatal(err)
		}
		changed := r
		changed.Work.Execution.Scope.Subject = IntakeSubject{Kind: SubjectDocumentVersion, DocumentVersion: "other"}
		if changed.Work.Execution.Scope.Subject.LeaseKey() == subject.LeaseKey() || IntakeIdempotencyKey(changed.Work) == r.IdempotencyKey || SameImmutableIntakePayload(r, changed) {
			t.Fatal("subject did not bind lease and immutable identity")
		}
	}
	for _, subject := range []IntakeSubject{{}, {Kind: SubjectSourceWindow, SourceWindow: "window", DocumentVersion: "version"}, {Kind: SubjectReprocessSelection}, {Kind: "unknown", DocumentVersion: "version"}} {
		if subject.Validate() == nil {
			t.Fatal("accepted nonexclusive subject")
		}
	}
}
