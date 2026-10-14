package run

import (
	"strings"
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
			r.Work.Execution.WorkflowID = "source-sync/vn/source/manual/request"
		case SubjectReprocessSelection:
			r.Work.Execution.WorkflowKind = Reprocess
			r.Work.Execution.WorkflowID = "reprocess/vn/request"
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

func TestWorkflowIDNamespacesAndScheduledSuffixes(t *testing.T) {
	for kind, ids := range map[WorkflowKind][]string{
		SourceSync:       {"source-sync/vn/source/incremental", "source-sync/vn/source/incremental-2026-10-14T00:00:00Z", "source-sync/vn/source/manual/request"},
		DocumentIngest:   {"document-ingest/vn/version/attempt"},
		OcrBatch:         {"ocr-batch/vn/collection"},
		EmbedBatch:       {"embed-batch/vn/collection"},
		CorpusBuild:      {"corpus-build/vn/build"},
		Promote:          {"promote/vn/destination/request"},
		Reprocess:        {"reprocess/vn/request"},
		ControlReconcile: {"corpus-control/vn", "corpus-control/vn-2026-10-14T00:00:00Z"},
	} {
		for _, id := range ids {
			if err := validateWorkflowID(id, kind, "vn"); err != nil {
				t.Fatalf("%s: %v", id, err)
			}
			if validateWorkflowID(id+"/extra", kind, "vn") == nil || validateWorkflowID("wrong/"+strings.SplitN(id, "/", 2)[1], kind, "vn") == nil {
				t.Fatal("accepted extra segments or wrong namespace")
			}
		}
	}
	for _, id := range []string{
		"source-sync/vn/" + strings.Repeat("a", 129) + "/incremental-2026-10-14T00:00:00Z",
		"source-sync/vn/source/" + strings.Repeat("a", 129) + "-2026-10-14T00:00:00Z",
		"source-sync/vn/source/incremental-2026-02-30T00:00:00Z",
		"source-sync/vn/source/incremental-2026-10-14T00:00:00+00:00",
		"source-sync/vn/source/incremental-2026-10-14T00:00:00.123Z",
		"source-sync/vn/source/manual/request-2026-10-14T00:00:00Z",
		"source-sync/my/source/incremental-2026-10-14T00:00:00Z",
		"source-sync/vn/source/incremental/2026-10-14T00:00:00Z",
		"source-sync/vn/source/" + strings.Repeat("a", 513),
	} {
		if validateWorkflowID(id, SourceSync, "vn") == nil {
			t.Fatalf("accepted invalid scheduled ID %q", id)
		}
	}
	for _, id := range []string{"corpus-control/my-2026-10-14T00:00:00Z", "corpus-control/vn-2026-02-30T00:00:00Z", "corpus-control/vn-2026-10-14T00:00:00+00:00"} {
		if validateWorkflowID(id, ControlReconcile, "vn") == nil {
			t.Fatal("accepted invalid scheduled control ID")
		}
	}
	for _, kind := range []WorkflowKind{SourceSync, ControlReconcile} {
		e := intakeFixture().Work.Execution
		e.WorkflowKind = kind
		if kind == SourceSync {
			e.WorkflowID = "source-sync/vn/source/incremental-2026-10-14T00:00:00Z"
			e.Scope = ExecutionScope{Kind: ScopeIntake, Subject: IntakeSubject{Kind: SubjectSourceWindow, SourceWindow: "window"}}
		} else {
			e.WorkflowID = "corpus-control/vn-2026-10-14T00:00:00Z"
			e.Scope = ExecutionScope{Kind: ScopeControl, CountryControl: "vn"}
		}
		if err := e.Validate(); err != nil {
			t.Fatal(err)
		}
	}
}
