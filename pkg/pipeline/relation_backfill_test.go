package pipeline

import (
	"testing"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"
)

func TestParseRelationBackfillRef(t *testing.T) {
	ref, ok := parseRelationBackfillRef([]byte(`{
		"source":" vbpl ",
		"target_id":" 140561 ",
		"target_number":" 15/2020/NĐ-CP ",
		"target_title":" Nghị định 15 "
	}`))
	if !ok {
		t.Fatal("parseRelationBackfillRef ok = false, want true")
	}
	if ref.Source != "vbpl" || ref.TargetID != "140561" ||
		ref.TargetNumber != "15/2020/NĐ-CP" || ref.TargetTitle != "Nghị định 15" {
		t.Fatalf("ref = %+v", ref)
	}
}

func TestIsVBPLTranslationTarget(t *testing.T) {
	tests := []struct {
		name     string
		targetID string
		want     bool
	}{
		{"english rendition", "vbpqta_11014", true},
		{"english rendition with spaces", "  vbpqta_7382 ", true},
		{"english rendition uppercase", "VBPQTA_3038", true},
		{"normal numeric target", "18654", false},
		{"empty", "", false},
		{"unrelated prefix", "vbpq_1", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isVBPLTranslationTarget(tt.targetID); got != tt.want {
				t.Fatalf("isVBPLTranslationTarget(%q) = %v, want %v", tt.targetID, got, tt.want)
			}
		})
	}
}

func TestBackfillRelationsWorkflow(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()

	params := BackfillRelationTargetsParams{Limit: 12}
	want := BackfillRelationTargetsResult{Candidates: 3, Enqueued: 2, Skipped: 1}
	var a *Activities
	env.OnActivity(a.BackfillRelationTargets, mock.Anything, params).
		Return(want, nil).
		Once()

	env.ExecuteWorkflow(BackfillRelationsWorkflow, params)

	if !env.IsWorkflowCompleted() {
		t.Fatal("BackfillRelationsWorkflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("BackfillRelationsWorkflow error: %v", err)
	}
	var got BackfillRelationTargetsResult
	if err := env.GetWorkflowResult(&got); err != nil {
		t.Fatalf("workflow result: %v", err)
	}
	if got != want {
		t.Fatalf("result = %+v, want %+v", got, want)
	}
	env.AssertExpectations(t)
}
