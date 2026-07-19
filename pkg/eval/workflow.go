package eval

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// WorkflowCase is a single multi-step agent task from the workflow golden set.
type WorkflowCase struct {
	ID           string           `json:"id"`
	Task         string           `json:"task"`
	Expected     WorkflowExpected `json:"expected"`
	ScoringNotes string           `json:"scoring_notes"`
}

// WorkflowExpected defines the correct outcome of a workflow case.
type WorkflowExpected struct {
	FinalCitations     []ExpectedCitation `json:"final_citations"`
	MustCheckRelations bool               `json:"must_check_relations"`
	ExpectAbstain      bool               `json:"expect_abstain"`
}

// WorkflowTranscript is the agent's output for a single workflow case.
type WorkflowTranscript struct {
	CaseID    string               `json:"case_id"`
	Cited     []TranscriptCitation `json:"cited"`
	Abstained bool                 `json:"abstained"`
	ToolCalls []ToolCall           `json:"tool_calls"`
}

// TranscriptCitation is a citation the agent reported in its final answer.
type TranscriptCitation struct {
	DocNumber string `json:"doc_number"`
	Article   string `json:"article,omitempty"`
}

// ToolCall records one MCP tool invocation from the agent's transcript.
type ToolCall struct {
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args"`
}

// WorkflowResult is the scored outcome of one workflow case.
type WorkflowResult struct {
	CaseID            string  `json:"case_id"`
	CitationCorrect   float64 `json:"citation_correct"`
	AbstainCorrect    bool    `json:"abstain_correct"`
	FollowedRelations bool    `json:"followed_relations"`
	ToolCallCount     int     `json:"tool_call_count"`
	SearchCount       int     `json:"search_count"`
	DocumentCount     int     `json:"document_count"`
}

// WorkflowAggregate summarizes all workflow case results.
type WorkflowAggregate struct {
	Cases              int     `json:"cases"`
	MeanCitationScore  float64 `json:"mean_citation_score"`
	AbstainAccuracy    float64 `json:"abstain_accuracy"`
	RelationCompliance float64 `json:"relation_compliance"`
	MeanToolCalls      float64 `json:"mean_tool_calls"`
}

// LoadWorkflowGolden loads and validates the workflow golden JSON file.
// Rejects: empty, missing id/task, duplicate ids.
func LoadWorkflowGolden(path string) ([]WorkflowCase, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read workflow golden: %w", err)
	}
	var cases []WorkflowCase
	if err := json.Unmarshal(data, &cases); err != nil {
		return nil, fmt.Errorf("parse workflow golden: %w", err)
	}
	if len(cases) == 0 {
		return nil, fmt.Errorf("parse workflow golden: empty case set")
	}
	seen := make(map[string]struct{}, len(cases))
	for i, c := range cases {
		if c.ID == "" {
			return nil, fmt.Errorf("validate workflow golden: case %d has empty id", i)
		}
		if c.Task == "" {
			return nil, fmt.Errorf("validate workflow golden: case %q has empty task", c.ID)
		}
		if _, dup := seen[c.ID]; dup {
			return nil, fmt.Errorf("validate workflow golden: duplicate id %q", c.ID)
		}
		seen[c.ID] = struct{}{}
	}
	return cases, nil
}

// ScoreWorkflow scores a single workflow transcript against its case.
func ScoreWorkflow(c WorkflowCase, t WorkflowTranscript) WorkflowResult {
	r := WorkflowResult{
		CaseID:        c.ID,
		ToolCallCount: len(t.ToolCalls),
	}

	// Count tool call types.
	for _, tc := range t.ToolCalls {
		switch tc.Tool {
		case "search":
			r.SearchCount++
		case "document":
			r.DocumentCount++
		}
	}

	// Citation scoring.
	r.CitationCorrect = citationScore(c.Expected.FinalCitations, t.Cited)

	// Abstain scoring.
	r.AbstainCorrect = t.Abstained == c.Expected.ExpectAbstain

	// Relation compliance.
	if !c.Expected.MustCheckRelations {
		r.FollowedRelations = true
	} else {
		r.FollowedRelations = r.DocumentCount > 0
	}

	return r
}

// citationScore computes the fraction of expected citations found in the cited list.
func citationScore(expected []ExpectedCitation, cited []TranscriptCitation) float64 {
	if len(expected) == 0 {
		if len(cited) == 0 {
			return 1.0
		}
		return 0.0
	}
	found := 0
	for _, ec := range expected {
		if transcriptHasCitation(ec, cited) {
			found++
		}
	}
	return float64(found) / float64(len(expected))
}

// transcriptHasCitation reports whether the cited list contains a match for the
// expected citation. DocNumber is compared case-insensitively. When the expected
// citation specifies an article, the cited entry must match it exactly
// (case-insensitive); when no article is expected, any matching doc_number suffices.
func transcriptHasCitation(ec ExpectedCitation, cited []TranscriptCitation) bool {
	for _, c := range cited {
		if !strings.EqualFold(strings.TrimSpace(c.DocNumber), strings.TrimSpace(ec.DocNumber)) {
			continue
		}
		if ec.Article == "" {
			return true
		}
		if strings.EqualFold(strings.TrimSpace(c.Article), strings.TrimSpace(ec.Article)) {
			return true
		}
	}
	return false
}

// SummarizeWorkflow aggregates results across all cases.
func SummarizeWorkflow(results []WorkflowResult, cases []WorkflowCase) WorkflowAggregate {
	n := len(results)
	if n == 0 {
		return WorkflowAggregate{}
	}

	agg := WorkflowAggregate{Cases: n}

	var citSum float64
	var abstainOK int
	var relationCases, relationOK int
	var toolSum int

	// Index cases by ID for relation-requirement lookup.
	caseMap := make(map[string]WorkflowCase, len(cases))
	for _, c := range cases {
		caseMap[c.ID] = c
	}

	for _, r := range results {
		citSum += r.CitationCorrect
		if r.AbstainCorrect {
			abstainOK++
		}
		toolSum += r.ToolCallCount

		if c, ok := caseMap[r.CaseID]; ok && c.Expected.MustCheckRelations {
			relationCases++
			if r.FollowedRelations {
				relationOK++
			}
		}
	}

	agg.MeanCitationScore = citSum / float64(n)
	agg.AbstainAccuracy = float64(abstainOK) / float64(n)
	if relationCases > 0 {
		agg.RelationCompliance = float64(relationOK) / float64(relationCases)
	} else {
		agg.RelationCompliance = 1.0 // vacuously compliant
	}
	agg.MeanToolCalls = float64(toolSum) / float64(n)

	return agg
}
