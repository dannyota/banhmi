package eval

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestSummarize(t *testing.T) {
	results := []CaseResult{
		{ // in-scope, recall 1/2, in-force 2/2, abstain OK
			RecallAtK: 0.5, RecallHits: 1, RecallWant: 2, MRRAtK: 0.5, Rank: 2,
			InForcePrecision: 1, HitsInForce: 2, HitsTotal: 2,
			AbstainCorrect: true,
		},
		{ // in-scope, recall 2/2, in-force 1/2 (leak), abstain OK
			RecallAtK: 1, RecallHits: 2, RecallWant: 2, MRRAtK: 1, Rank: 1,
			InForcePrecision: 0.5, HitsInForce: 1, HitsTotal: 2,
			AbstainCorrect: true,
		},
		{ // out-of-scope abstention: no recall/hit denominators, abstain OK
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
		InForcePrecision: 1.0, InForceCases: 4,
		AbstainAccuracy: 0.8, Cases: 5,
	}

	t.Run("all pass", func(t *testing.T) {
		fails := Thresholds{MinRecall: 0.5, MinInForce: 1.0, MinAbstain: 0.7}.Check(agg)
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
		{"id":"a","question":"q1?","expected_citations":[{"doc_number":"50/2024/tt-nhnn","article":"7"}]},
		{"id":"b","question":"q2?","expect_abstain":true}
	]`
	cases, err := parseGolden([]byte(in), "test")
	if err != nil {
		t.Fatalf("parseGolden: %v", err)
	}
	if len(cases) != 2 {
		t.Fatalf("len = %d, want 2", len(cases))
	}
	if cases[0].ExpectedCitations[0].DocNumber != "50/2024/tt-nhnn" || cases[0].ExpectedCitations[0].Article != "7" {
		t.Errorf("case a citation = %+v", cases[0].ExpectedCitations[0])
	}
	if !cases[1].ExpectAbstain {
		t.Error("case b ExpectAbstain = false, want true")
	}
}

func TestParseGoldenExpectFail(t *testing.T) {
	in := `[
		{"id":"a","question":"q1?","expected_citations":[{"doc_number":"DOC-1"}]},
		{"id":"b","question":"q2?","expected_citations":[{"doc_number":"DOC-2"}],"expect_fail":true},
		{"id":"c","question":"q3?","expect_abstain":true}
	]`
	cases, err := parseGolden([]byte(in), "test")
	if err != nil {
		t.Fatalf("parseGolden: %v", err)
	}
	if !cases[1].ExpectFail {
		t.Error("case b ExpectFail = false, want true")
	}

	results := []CaseResult{
		{Case: cases[0], RecallHits: 1, RecallWant: 1, MRRAtK: 1, AbstainCorrect: true},
		{Case: cases[1], RecallHits: 0, RecallWant: 1, MRRAtK: 0, AbstainCorrect: true},
		{Case: cases[2], Abstained: true, AbstainCorrect: true},
	}
	agg := Summarize(results)
	if agg.ExpectFailCases != 1 {
		t.Errorf("ExpectFailCases = %d, want 1", agg.ExpectFailCases)
	}
	if agg.RecallCases != 1 {
		t.Errorf("RecallCases = %d, want 1 (expect_fail should be excluded)", agg.RecallCases)
	}
	if agg.RecallAtK != 1.0 {
		t.Errorf("RecallAtK = %.2f, want 1.0 (only non-gap case)", agg.RecallAtK)
	}
	// AbstainAccuracy denominator must exclude expect_fail cases: 2/2 = 1.0.
	if !approx(agg.AbstainAccuracy, 1.0) {
		t.Errorf("AbstainAccuracy = %.2f, want 1.0 (expect_fail excluded from denominator)", agg.AbstainAccuracy)
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
		{"citation without doc_number", `[{"id":"a","question":"q?","expected_citations":[{"article":"7"}]}]`},
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
	// Citation-correctness column must be gone.
	if strings.Contains(out, "CITES") || strings.Contains(out, "citation-correctness") {
		t.Errorf("report still contains citation-correctness column:\n%s", out)
	}
}

func TestGapPassReporting(t *testing.T) {
	results := []CaseResult{
		{ // normal case
			Case:       Case{ID: "q-normal"},
			RecallHits: 1, RecallWant: 1, AbstainCorrect: true,
		},
		{ // expect_fail that now fully recalls → GAP-PASS
			Case:       Case{ID: "q-gap-pass", ExpectFail: true},
			RecallHits: 2, RecallWant: 2, AbstainCorrect: true,
		},
		{ // expect_fail that does not fully recall → GAP
			Case:       Case{ID: "q-gap-fail", ExpectFail: true},
			RecallHits: 0, RecallWant: 1, AbstainCorrect: true,
		},
	}
	agg := Summarize(results)

	if agg.GapPassCases != 1 {
		t.Errorf("GapPassCases = %d, want 1", agg.GapPassCases)
	}

	var sb strings.Builder
	WriteReport(&sb, results, agg)
	out := sb.String()

	if !strings.Contains(out, "GAP-PASS") {
		t.Errorf("report missing GAP-PASS marker:\n%s", out)
	}
	// The gap-fail case should show GAP, not GAP-PASS.
	lines := strings.Split(out, "\n")
	for _, line := range lines {
		if strings.Contains(line, "q-gap-fail") {
			if strings.Contains(line, "GAP-PASS") {
				t.Errorf("q-gap-fail line should show GAP not GAP-PASS: %s", line)
			}
			if !strings.Contains(line, "GAP") {
				t.Errorf("q-gap-fail line missing GAP marker: %s", line)
			}
		}
	}
	if !strings.Contains(out, "1 known-gap case(s) now pass") {
		t.Errorf("report missing gap-pass summary line:\n%s", out)
	}
	if !strings.Contains(out, "q-gap-pass") {
		t.Errorf("report gap-pass summary missing case id:\n%s", out)
	}
}

func TestWriteJSONReport(t *testing.T) {
	results := []CaseResult{
		{
			Case:       Case{ID: "q-1"},
			RecallHits: 1, RecallWant: 2, Rank: 1,
			InForcePrecision: 1.0, HitsInForce: 2, HitsTotal: 2,
			AbstainCorrect: true,
		},
		{
			Case:       Case{ID: "q-gap", ExpectFail: true},
			RecallHits: 1, RecallWant: 1, Rank: 1,
			AbstainCorrect: true,
		},
	}
	agg := Summarize(results)
	meta := JSONReportMeta{
		Jurisdiction:  "vn",
		RetrievalMode: "hybrid",
		TopK:          8,
		GeneratedAt:   "2026-07-15T10:00:00Z",
		Chunks:        1234,
	}

	var buf bytes.Buffer
	if err := WriteJSONReport(&buf, meta, results, agg); err != nil {
		t.Fatalf("WriteJSONReport: %v", err)
	}

	var report JSONReport
	if err := json.Unmarshal(buf.Bytes(), &report); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}

	if report.SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d, want 1", report.SchemaVersion)
	}
	if report.Jurisdiction != "vn" {
		t.Errorf("Jurisdiction = %q, want vn", report.Jurisdiction)
	}
	if report.Corpus.Chunks != 1234 {
		t.Errorf("Corpus.Chunks = %d, want 1234", report.Corpus.Chunks)
	}
	if len(report.Cases) != 2 {
		t.Fatalf("Cases = %d, want 2", len(report.Cases))
	}
	if !report.Cases[1].ExpectFail {
		t.Error("Cases[1].ExpectFail = false, want true")
	}
	if !report.Cases[1].GapPass {
		t.Error("Cases[1].GapPass = false, want true (fully recalled expect_fail)")
	}
	if report.Aggregate.GapPassCases != 1 {
		t.Errorf("Aggregate.GapPassCases = %d, want 1", report.Aggregate.GapPassCases)
	}
}

// approx compares floats within a small epsilon (micro-averages aren't exact).
func approx(a, b float64) bool {
	const eps = 1e-9
	d := a - b
	return d < eps && d > -eps
}
