package eval

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadWorkflowGolden(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "golden.json")
		data := `[
			{"id":"a","task":"do A","expected":{"final_citations":[],"must_check_relations":false,"expect_abstain":true},"scoring_notes":"n"},
			{"id":"b","task":"do B","expected":{"final_citations":[{"doc_number":"x"}],"must_check_relations":true,"expect_abstain":false},"scoring_notes":"n"}
		]`
		os.WriteFile(path, []byte(data), 0o644)
		cases, err := LoadWorkflowGolden(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(cases) != 2 {
			t.Fatalf("got %d cases, want 2", len(cases))
		}
	})

	t.Run("empty", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "golden.json")
		os.WriteFile(path, []byte("[]"), 0o644)
		_, err := LoadWorkflowGolden(path)
		if err == nil {
			t.Fatal("expected error for empty case set")
		}
	})

	t.Run("duplicate_ids", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "golden.json")
		data := `[
			{"id":"dup","task":"t1","expected":{"final_citations":[],"must_check_relations":false,"expect_abstain":false},"scoring_notes":"n"},
			{"id":"dup","task":"t2","expected":{"final_citations":[],"must_check_relations":false,"expect_abstain":false},"scoring_notes":"n"}
		]`
		os.WriteFile(path, []byte(data), 0o644)
		_, err := LoadWorkflowGolden(path)
		if err == nil {
			t.Fatal("expected error for duplicate ids")
		}
	})

	t.Run("missing_task", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "golden.json")
		data := `[{"id":"x","task":"","expected":{"final_citations":[],"must_check_relations":false,"expect_abstain":false},"scoring_notes":"n"}]`
		os.WriteFile(path, []byte(data), 0o644)
		_, err := LoadWorkflowGolden(path)
		if err == nil {
			t.Fatal("expected error for missing task")
		}
	})

	t.Run("missing_id", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "golden.json")
		data := `[{"id":"","task":"do something","expected":{"final_citations":[],"must_check_relations":false,"expect_abstain":false},"scoring_notes":"n"}]`
		os.WriteFile(path, []byte(data), 0o644)
		_, err := LoadWorkflowGolden(path)
		if err == nil {
			t.Fatal("expected error for missing id")
		}
	})
}

func TestScoreWorkflow(t *testing.T) {
	tests := []struct {
		name       string
		c          WorkflowCase
		transcript WorkflowTranscript
		wantCit    float64
		wantAbst   bool
		wantRel    bool
	}{
		{
			name: "perfect_score",
			c: WorkflowCase{
				ID: "perfect",
				Expected: WorkflowExpected{
					FinalCitations: []ExpectedCitation{
						{DocNumber: "50/2024/tt-nhnn", Article: "11"},
						{DocNumber: "77/2025/tt-nhnn"},
					},
					MustCheckRelations: true,
				},
			},
			transcript: WorkflowTranscript{
				CaseID: "perfect",
				Cited: []TranscriptCitation{
					{DocNumber: "50/2024/TT-NHNN", Article: "11"},
					{DocNumber: "77/2025/TT-NHNN", Article: "4"},
				},
				ToolCalls: []ToolCall{
					{Tool: "search", Args: json.RawMessage(`{}`)},
					{Tool: "document", Args: json.RawMessage(`{}`)},
				},
			},
			wantCit:  1.0,
			wantAbst: true,
			wantRel:  true,
		},
		{
			name: "partial_citation",
			c: WorkflowCase{
				ID: "partial",
				Expected: WorkflowExpected{
					FinalCitations: []ExpectedCitation{
						{DocNumber: "a"},
						{DocNumber: "b"},
						{DocNumber: "c"},
					},
					MustCheckRelations: false,
				},
			},
			transcript: WorkflowTranscript{
				CaseID: "partial",
				Cited: []TranscriptCitation{
					{DocNumber: "a"},
					{DocNumber: "c"},
				},
				ToolCalls: []ToolCall{{Tool: "search", Args: json.RawMessage(`{}`)}},
			},
			wantCit:  2.0 / 3.0,
			wantAbst: true,
			wantRel:  true,
		},
		{
			name: "abstain_correct",
			c: WorkflowCase{
				ID: "abstain_ok",
				Expected: WorkflowExpected{
					FinalCitations: []ExpectedCitation{},
					ExpectAbstain:  true,
				},
			},
			transcript: WorkflowTranscript{
				CaseID:    "abstain_ok",
				Abstained: true,
				ToolCalls: []ToolCall{{Tool: "search", Args: json.RawMessage(`{}`)}},
			},
			wantCit:  1.0,
			wantAbst: true,
			wantRel:  true,
		},
		{
			name: "abstain_incorrect",
			c: WorkflowCase{
				ID: "abstain_bad",
				Expected: WorkflowExpected{
					FinalCitations: []ExpectedCitation{},
					ExpectAbstain:  true,
				},
			},
			transcript: WorkflowTranscript{
				CaseID:    "abstain_bad",
				Cited:     []TranscriptCitation{{DocNumber: "fake/doc"}},
				ToolCalls: []ToolCall{{Tool: "search", Args: json.RawMessage(`{}`)}},
			},
			wantCit:  0.0,
			wantAbst: false,
			wantRel:  true,
		},
		{
			name: "relations_required_not_checked",
			c: WorkflowCase{
				ID: "rel_miss",
				Expected: WorkflowExpected{
					FinalCitations:     []ExpectedCitation{{DocNumber: "x"}},
					MustCheckRelations: true,
				},
			},
			transcript: WorkflowTranscript{
				CaseID:    "rel_miss",
				Cited:     []TranscriptCitation{{DocNumber: "x"}},
				ToolCalls: []ToolCall{{Tool: "search", Args: json.RawMessage(`{}`)}},
			},
			wantCit:  1.0,
			wantAbst: true,
			wantRel:  false,
		},
		{
			name: "relations_not_required",
			c: WorkflowCase{
				ID: "rel_vac",
				Expected: WorkflowExpected{
					FinalCitations:     []ExpectedCitation{{DocNumber: "x"}},
					MustCheckRelations: false,
				},
			},
			transcript: WorkflowTranscript{
				CaseID:    "rel_vac",
				Cited:     []TranscriptCitation{{DocNumber: "x"}},
				ToolCalls: []ToolCall{{Tool: "search", Args: json.RawMessage(`{}`)}},
			},
			wantCit:  1.0,
			wantAbst: true,
			wantRel:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := ScoreWorkflow(tt.c, tt.transcript)
			if diff := r.CitationCorrect - tt.wantCit; diff > 0.001 || diff < -0.001 {
				t.Errorf("CitationCorrect = %f, want %f", r.CitationCorrect, tt.wantCit)
			}
			if r.AbstainCorrect != tt.wantAbst {
				t.Errorf("AbstainCorrect = %v, want %v", r.AbstainCorrect, tt.wantAbst)
			}
			if r.FollowedRelations != tt.wantRel {
				t.Errorf("FollowedRelations = %v, want %v", r.FollowedRelations, tt.wantRel)
			}
		})
	}
}

func TestSummarizeWorkflow(t *testing.T) {
	cases := []WorkflowCase{
		{ID: "a", Expected: WorkflowExpected{MustCheckRelations: true}},
		{ID: "b", Expected: WorkflowExpected{MustCheckRelations: false}},
		{ID: "c", Expected: WorkflowExpected{MustCheckRelations: true}},
	}
	results := []WorkflowResult{
		{CaseID: "a", CitationCorrect: 1.0, AbstainCorrect: true, FollowedRelations: true, ToolCallCount: 3},
		{CaseID: "b", CitationCorrect: 0.5, AbstainCorrect: true, FollowedRelations: true, ToolCallCount: 2},
		{CaseID: "c", CitationCorrect: 0.0, AbstainCorrect: false, FollowedRelations: false, ToolCallCount: 1},
	}

	agg := SummarizeWorkflow(results, cases)

	if agg.Cases != 3 {
		t.Errorf("Cases = %d, want 3", agg.Cases)
	}
	if diff := agg.MeanCitationScore - 0.5; diff > 0.001 || diff < -0.001 {
		t.Errorf("MeanCitationScore = %f, want 0.5", agg.MeanCitationScore)
	}
	if diff := agg.AbstainAccuracy - 2.0/3.0; diff > 0.001 || diff < -0.001 {
		t.Errorf("AbstainAccuracy = %f, want %f", agg.AbstainAccuracy, 2.0/3.0)
	}
	// RelationCompliance: 2 relation cases (a, c); 1 OK (a) -> 0.5
	if diff := agg.RelationCompliance - 0.5; diff > 0.001 || diff < -0.001 {
		t.Errorf("RelationCompliance = %f, want 0.5", agg.RelationCompliance)
	}
	if diff := agg.MeanToolCalls - 2.0; diff > 0.001 || diff < -0.001 {
		t.Errorf("MeanToolCalls = %f, want 2.0", agg.MeanToolCalls)
	}
}
