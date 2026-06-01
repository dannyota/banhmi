package eval

import (
	"strings"
	"testing"
)

func TestSummarize(t *testing.T) {
	results := []CaseResult{
		{ // in-scope, recall 1/2, citations 1/2, in-force 2/2, abstain OK
			RecallAtK: 0.5, RecallHits: 1, RecallWant: 2, MRRAtK: 0.5, Rank: 2,
			CitationCorrectness: 0.5, CitationsGrounded: 1, CitationsMade: 2,
			InForcePrecision: 1, HitsInForce: 2, HitsTotal: 2,
			AbstainCorrect: true,
		},
		{ // in-scope, recall 2/2, citations 2/2, in-force 1/2 (leak), abstain OK
			RecallAtK: 1, RecallHits: 2, RecallWant: 2, MRRAtK: 1, Rank: 1,
			CitationCorrectness: 1, CitationsGrounded: 2, CitationsMade: 2,
			InForcePrecision: 0.5, HitsInForce: 1, HitsTotal: 2,
			AbstainCorrect: true,
		},
		{ // out-of-scope abstention: no recall/citation/hit denominators, abstain OK
			Abstained: true, AbstainCorrect: true,
		},
	}

	agg := Summarize(results)

	if agg.Cases != 3 {
		t.Errorf("Cases = %d, want 3", agg.Cases)
	}
	// Recall micro-average: (1+2)/(2+2) = 0.75 over 2 contributing cases.
	if agg.RecallCases != 2 || !approx(agg.RecallAtK, 0.75) {
		t.Errorf("recall = %v over %d cases, want 0.75 over 2", agg.RecallAtK, agg.RecallCases)
	}
	// MRR mean over two contributing cases: (0.5+1.0)/2 = 0.75.
	if agg.MRRCases != 2 || !approx(agg.MRRAtK, 0.75) {
		t.Errorf("mrr = %v over %d cases, want 0.75 over 2", agg.MRRAtK, agg.MRRCases)
	}
	// Citation micro-average: (1+2)/(2+2) = 0.75 over 2 cases.
	if agg.CitationCases != 2 || !approx(agg.CitationCorrectness, 0.75) {
		t.Errorf("citation = %v over %d cases, want 0.75 over 2", agg.CitationCorrectness, agg.CitationCases)
	}
	// In-force micro-average: (2+1)/(2+2) = 0.75 over 2 cases.
	if agg.InForceCases != 2 || !approx(agg.InForcePrecision, 0.75) {
		t.Errorf("in-force = %v over %d cases, want 0.75 over 2", agg.InForcePrecision, agg.InForceCases)
	}
	// Abstention accuracy: 3/3 = 1.0.
	if !approx(agg.AbstainAccuracy, 1) {
		t.Errorf("abstain accuracy = %v, want 1.0", agg.AbstainAccuracy)
	}
}

func TestSummarizeEmpty(t *testing.T) {
	agg := Summarize(nil)
	if agg.Cases != 0 || agg.RecallAtK != 0 || agg.RecallCases != 0 || agg.AbstainAccuracy != 0 {
		t.Errorf("empty Summarize = %+v, want all-zero", agg)
	}
}

func TestThresholdsCheck(t *testing.T) {
	agg := Aggregate{
		RecallAtK: 0.6, RecallCases: 4,
		MRRAtK: 0.7, MRRCases: 4,
		CitationCorrectness: 0.9, CitationCases: 4,
		InForcePrecision: 1.0, InForceCases: 4,
		AbstainAccuracy: 0.8, Cases: 5,
	}

	t.Run("all pass", func(t *testing.T) {
		fails := Thresholds{MinRecall: 0.5, MinCitation: 0.8, MinInForce: 1.0, MinAbstain: 0.7}.Check(agg)
		if len(fails) != 0 {
			t.Errorf("got failures %+v, want none", fails)
		}
	})

	t.Run("recall below floor fails", func(t *testing.T) {
		fails := Thresholds{MinRecall: 0.7}.Check(agg)
		if len(fails) != 1 || fails[0].Metric != "recall@k" {
			t.Errorf("got %+v, want one recall@k failure", fails)
		}
	})

	t.Run("mrr below floor fails", func(t *testing.T) {
		fails := Thresholds{MinMRR: 0.8}.Check(agg)
		if len(fails) != 1 || fails[0].Metric != "mrr@k" {
			t.Errorf("got %+v, want one mrr@k failure", fails)
		}
	})

	t.Run("zero threshold imposes no floor", func(t *testing.T) {
		fails := Thresholds{}.Check(agg)
		if len(fails) != 0 {
			t.Errorf("got %+v, want none (no thresholds set)", fails)
		}
	})

	t.Run("metric with no data is skipped", func(t *testing.T) {
		// Recall has a floor but no contributing cases → cannot fail.
		empty := Aggregate{RecallCases: 0, Cases: 0}
		fails := Thresholds{MinRecall: 0.9, MinAbstain: 0.9}.Check(empty)
		if len(fails) != 0 {
			t.Errorf("got %+v, want none (no data for any metric)", fails)
		}
	})
}

func TestParseGoldenValid(t *testing.T) {
	in := `[
		{"id":"a","question":"q1?","expected_citations":[{"so_ky_hieu":"50/2024/tt-nhnn","dieu":"7"}]},
		{"id":"b","question":"q2?","expect_abstain":true}
	]`
	cases, err := parseGolden([]byte(in), "test")
	if err != nil {
		t.Fatalf("parseGolden: %v", err)
	}
	if len(cases) != 2 {
		t.Fatalf("len = %d, want 2", len(cases))
	}
	if cases[0].ExpectedCitations[0].SoKyHieu != "50/2024/tt-nhnn" || cases[0].ExpectedCitations[0].Dieu != "7" {
		t.Errorf("case a citation = %+v", cases[0].ExpectedCitations[0])
	}
	if !cases[1].ExpectAbstain {
		t.Error("case b ExpectAbstain = false, want true")
	}
}

func TestParseGoldenRejects(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"empty array", `[]`},
		{"missing id", `[{"question":"q?","expect_abstain":true}]`},
		{"missing question", `[{"id":"a","expect_abstain":true}]`},
		{"duplicate id", `[{"id":"a","question":"q?","expect_abstain":true},{"id":"a","question":"q2?","expect_abstain":true}]`},
		{"in-scope without citations", `[{"id":"a","question":"q?"}]`},
		{"citation without so_ky_hieu", `[{"id":"a","question":"q?","expected_citations":[{"dieu":"7"}]}]`},
		{"unknown field", `[{"id":"a","question":"q?","expect_abstain":true,"bogus":1}]`},
		{"not json", `not json`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseGolden([]byte(tt.in), "test"); err == nil {
				t.Errorf("parseGolden(%s) = nil error, want rejection", tt.in)
			}
		})
	}
}

func TestWriteReport(t *testing.T) {
	results := []CaseResult{
		{
			Case:       Case{ID: "q-in-scope"},
			RecallHits: 1, RecallWant: 2,
			MRRAtK: 0.5, Rank: 2,
			CitationsGrounded: 1, CitationsMade: 2,
			InForcePrecision: 1.0, HitsTotal: 2,
			AbstainCorrect: true,
		},
		{
			Case:      Case{ID: "q-out-of-scope"},
			Abstained: true, AbstainCorrect: true,
		},
	}
	agg := Summarize(results)

	var sb strings.Builder
	WriteReport(&sb, results, agg)
	out := sb.String()

	for _, want := range []string{"q-in-scope", "q-out-of-scope", "recall@k", "mrr@k", "abstention-accuracy", "Cases: 2"} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q\n%s", want, out)
		}
	}
}

// approx compares floats within a small epsilon (micro-averages aren't exact).
func approx(a, b float64) bool {
	const eps = 1e-9
	d := a - b
	return d < eps && d > -eps
}
