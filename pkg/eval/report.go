package eval

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// Aggregate is the corpus-level roll-up of per-case results. Each rate is a
// micro-average (sum of numerators / sum of denominators across cases) so larger
// cases are not over- or under-weighted relative to a mean-of-means. Cases with no
// denominator for a metric (e.g. recall on an out-of-scope question) are excluded
// from that metric's average, not counted as zero.
type Aggregate struct {
	Cases           int // number of scored cases
	ExpectFailCases int // cases excluded from metrics (known coverage gaps)
	GapPassCases    int // expect_fail cases that now fully recall

	RecallAtK   float64 // sum of found / sum of want, over cases that expected citations
	RecallCases int     // cases that contributed to recall (had expected citations)

	MRRAtK   float64 // mean reciprocal rank over cases that expected citations
	MRRCases int     // cases that contributed to MRR (had expected citations)

	InForcePrecision float64 // sum of current-law hits / sum of hits, over cases that returned hits
	InForceCases     int     // cases that contributed (returned at least one hit)

	AbstainAccuracy float64 // fraction of cases whose abstention matched expectation

	PoolRecall float64 // sum of pool hits / sum of pool want, over pool-probed cases
	PoolCases  int     // cases that contributed to pool recall (probed, PoolWant > 0)
}

// Summarize folds per-case results into corpus metrics. It micro-averages each
// rate over the cases that have a denominator for it, so an empty input is well
// defined (all rates 0, all counts 0).
func Summarize(results []CaseResult) Aggregate {
	var agg Aggregate
	agg.Cases = len(results)

	var recallFound, recallWant int
	var inForceOK, inForceTotal int
	var abstainOK int
	var poolFound, poolWant int

	for _, r := range results {
		if r.Case.ExpectFail {
			agg.ExpectFailCases++
			if r.RecallWant > 0 && r.RecallHits == r.RecallWant {
				agg.GapPassCases++
			}
			continue
		}
		if r.RecallWant > 0 {
			recallFound += r.RecallHits
			recallWant += r.RecallWant
			agg.MRRAtK += r.MRRAtK
			agg.RecallCases++
			agg.MRRCases++
		}
		if r.PoolWant > 0 {
			poolFound += r.PoolHits
			poolWant += r.PoolWant
			agg.PoolCases++
		}
		if r.HitsTotal > 0 {
			inForceOK += r.HitsInForce
			inForceTotal += r.HitsTotal
			agg.InForceCases++
		}
		if r.AbstainCorrect {
			abstainOK++
		}
	}

	agg.RecallAtK = ratio(recallFound, recallWant)
	if agg.MRRCases > 0 {
		agg.MRRAtK /= float64(agg.MRRCases)
	}
	agg.InForcePrecision = ratio(inForceOK, inForceTotal)
	agg.AbstainAccuracy = ratio(abstainOK, len(results)-agg.ExpectFailCases)
	agg.PoolRecall = ratio(poolFound, poolWant)
	return agg
}

// ratio is num/den as a float, or 0 when den is 0 (no data for that metric).
func ratio(num, den int) float64 {
	if den == 0 {
		return 0
	}
	return float64(num) / float64(den)
}

// LoadGolden reads and validates a golden Q&A set from path. It rejects an empty
// set, a case missing an id or question, and an in-scope case (expect_abstain
// false) with no expected citations — those would silently never test recall. An
// out-of-scope case (expect_abstain true) is allowed to have no expected
// citations.
func LoadGolden(path string) ([]Case, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read golden set %s: %w", path, err)
	}
	return parseGolden(b, path)
}

// parseGolden decodes and validates golden JSON; split from LoadGolden so tests
// can validate in-memory bytes without a file.
func parseGolden(b []byte, src string) ([]Case, error) {
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	var cases []Case
	if err := dec.Decode(&cases); err != nil {
		return nil, fmt.Errorf("parse golden set %s: %w", src, err)
	}
	if len(cases) == 0 {
		return nil, fmt.Errorf("golden set %s is empty", src)
	}

	seen := make(map[string]bool, len(cases))
	for i, c := range cases {
		switch {
		case c.ID == "":
			return nil, fmt.Errorf("golden set %s: case %d has no id", src, i)
		case c.Question == "":
			return nil, fmt.Errorf("golden set %s: case %q has no question", src, c.ID)
		case seen[c.ID]:
			return nil, fmt.Errorf("golden set %s: duplicate case id %q", src, c.ID)
		case !c.ExpectAbstain && len(c.ExpectedCitations) == 0:
			return nil, fmt.Errorf("golden set %s: in-scope case %q has no expected_citations (set expect_abstain or add citations)", src, c.ID)
		}
		for j, ec := range c.ExpectedCitations {
			if ec.DocNumber == "" {
				return nil, fmt.Errorf("golden set %s: case %q expected_citation %d has no doc_number", src, c.ID, j)
			}
		}
		seen[c.ID] = true
	}
	return cases, nil
}

// Thresholds are the minimum aggregate metrics required to pass. A zero field
// imposes no floor for that metric, so cmd/eval can gate on a subset. CheckPasses
// only the metrics that had cases (no false failure on a metric the set never
// exercised).
type Thresholds struct {
	MinRecall  float64
	MinMRR     float64
	MinInForce float64
	MinAbstain float64
}

// Failure is one threshold that the aggregate did not meet.
type Failure struct {
	Metric string
	Got    float64
	Want   float64
}

// Check returns the thresholds the aggregate failed to meet. A metric with no
// contributing cases is skipped (it cannot pass or fail without data), except the
// abstention metric, which every case contributes to. An empty result slice means
// all thresholds passed (or none were set).
func (t Thresholds) Check(agg Aggregate) []Failure {
	var fails []Failure
	add := func(metric string, got, want float64, hasData bool) {
		if want > 0 && hasData && got < want {
			fails = append(fails, Failure{Metric: metric, Got: got, Want: want})
		}
	}
	add("recall@k", agg.RecallAtK, t.MinRecall, agg.RecallCases > 0)
	add("mrr@k", agg.MRRAtK, t.MinMRR, agg.MRRCases > 0)
	add("current-law-precision", agg.InForcePrecision, t.MinInForce, agg.InForceCases > 0)
	add("abstention-accuracy", agg.AbstainAccuracy, t.MinAbstain, agg.Cases > 0)
	return fails
}

// WriteReport renders a human-readable per-case table plus the aggregate summary
// to w. It is deterministic (cases in input order) so the output diffs cleanly in
// CI logs.
func WriteReport(w io.Writer, results []CaseResult, agg Aggregate) {
	_, _ = fmt.Fprintln(w, "ID                    ABSTAIN  RECALL@K   RANK  CURRENT   OK")
	_, _ = fmt.Fprintln(w, "--------------------  -------  ---------  ----  --------  --------")
	for _, r := range results {
		abst := boolMark(r.Abstained)
		okMark := passFail(r.AbstainCorrect)
		if r.Case.ExpectFail {
			okMark = "GAP"
			if r.RecallWant > 0 && r.RecallHits == r.RecallWant {
				okMark = "GAP-PASS"
			}
		}
		_, _ = fmt.Fprintf(w, "%-20s  %-7s  %4d/%-4d  %-4s  %5.0f%%     %s\n",
			truncate(r.Case.ID, 20),
			abst,
			r.RecallHits, r.RecallWant,
			rankMark(r.Rank),
			r.InForcePrecision*100,
			okMark,
		)
	}
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintf(w, "Cases: %d", agg.Cases)
	if agg.ExpectFailCases > 0 {
		_, _ = fmt.Fprintf(w, " (%d known-gap excluded)", agg.ExpectFailCases)
	}
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintf(w, "recall@k:              %s\n", pct(agg.RecallAtK, agg.RecallCases))
	_, _ = fmt.Fprintf(w, "mrr@k:                 %s\n", pct(agg.MRRAtK, agg.MRRCases))
	_, _ = fmt.Fprintf(w, "current-law-precision: %s\n", pct(agg.InForcePrecision, agg.InForceCases))
	_, _ = fmt.Fprintf(w, "abstention-accuracy:   %s\n", pct(agg.AbstainAccuracy, agg.Cases))
	if agg.PoolCases > 0 {
		_, _ = fmt.Fprintf(w, "pool-recall:           %s\n", pct(agg.PoolRecall, agg.PoolCases))
	}

	// Report expect_fail cases that now fully recall.
	if agg.GapPassCases > 0 {
		var ids []string
		for _, r := range results {
			if r.Case.ExpectFail && r.RecallWant > 0 && r.RecallHits == r.RecallWant {
				ids = append(ids, r.Case.ID)
			}
		}
		_, _ = fmt.Fprintf(w, "\n%d known-gap case(s) now pass — consider removing expect_fail: %s\n",
			agg.GapPassCases, strings.Join(ids, ", "))
	}
}

// pct formats a rate as a percentage, or "n/a (0 cases)" when no case fed the
// metric, so a missing-data zero is never mistaken for a real 0%.
func pct(v float64, cases int) string {
	if cases == 0 {
		return "n/a (0 cases)"
	}
	return fmt.Sprintf("%.1f%% (%d cases)", v*100, cases)
}

// boolMark renders a yes/no for the abstain column.
func boolMark(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// passFail renders the per-case abstention-correctness check.
func passFail(b bool) string {
	if b {
		return "OK"
	}
	return "XX"
}

func rankMark(rank int) string {
	if rank <= 0 {
		return "-"
	}
	return fmt.Sprintf("%d", rank)
}

// truncate caps s at n runes so the table column stays aligned.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// --- JSON report artifact ---

// JSONReport is the schema for the machine-readable eval output file.
type JSONReport struct {
	SchemaVersion int              `json:"schema_version"`
	Jurisdiction  string           `json:"jurisdiction"`
	RetrievalMode string           `json:"retrieval_mode"`
	TopK          int              `json:"top_k"`
	PoolK         int              `json:"pool_k,omitempty"`
	DocCap        int              `json:"doc_cap,omitempty"`
	GeneratedAt   string           `json:"generated_at"`
	Corpus        JSONReportCorpus `json:"corpus"`
	Aggregate     JSONReportAgg    `json:"aggregate"`
	Cases         []JSONReportCase `json:"cases"`
}

// JSONReportCorpus holds corpus-level metadata.
type JSONReportCorpus struct {
	Chunks int64 `json:"chunks"`
}

// JSONReportAgg holds the aggregate metrics.
type JSONReportAgg struct {
	RecallAtK        float64 `json:"recall_at_k"`
	RecallCases      int     `json:"recall_cases"`
	MRRAtK           float64 `json:"mrr_at_k"`
	MRRCases         int     `json:"mrr_cases"`
	InForcePrecision float64 `json:"in_force_precision"`
	InForceCases     int     `json:"in_force_cases"`
	AbstainAccuracy  float64 `json:"abstain_accuracy"`
	Cases            int     `json:"cases"`
	ExpectFailCases  int     `json:"expect_fail_cases"`
	GapPassCases     int     `json:"gap_pass_cases"`
	PoolRecall       float64 `json:"pool_recall,omitempty"`
	PoolCases        int     `json:"pool_cases,omitempty"`
}

// JSONReportCase holds per-case metrics.
type JSONReportCase struct {
	ID               string  `json:"id"`
	RecallHits       int     `json:"recall_hits"`
	RecallWant       int     `json:"recall_want"`
	Rank             int     `json:"rank"`
	InForcePrecision float64 `json:"in_force_precision"`
	Abstained        bool    `json:"abstained"`
	AbstainCorrect   bool    `json:"abstain_correct"`
	ExpectFail       bool    `json:"expect_fail"`
	GapPass          bool    `json:"gap_pass"`
	PoolHits         int     `json:"pool_hits,omitempty"`
	PoolWant         int     `json:"pool_want,omitempty"`
	PoolRank         int     `json:"pool_rank,omitempty"`
}

// JSONReportMeta carries the non-metric metadata that cmd/eval knows (jurisdiction,
// retrieval mode, top-k, timestamp, corpus size) so WriteJSONReport stays pure.
type JSONReportMeta struct {
	Jurisdiction  string
	RetrievalMode string
	TopK          int
	PoolK         int    // deep-probe candidate depth; 0 = probe off
	DocCap        int    // per-document cap override used for the run; 0 = config default
	GeneratedAt   string // RFC 3339
	Chunks        int64
}

// WriteJSONReport writes a machine-readable JSON eval report to w. It is pure:
// all data comes from the meta, results, and aggregate parameters.
func WriteJSONReport(w io.Writer, meta JSONReportMeta, results []CaseResult, agg Aggregate) error {
	report := JSONReport{
		SchemaVersion: 1,
		Jurisdiction:  meta.Jurisdiction,
		RetrievalMode: meta.RetrievalMode,
		TopK:          meta.TopK,
		PoolK:         meta.PoolK,
		DocCap:        meta.DocCap,
		GeneratedAt:   meta.GeneratedAt,
		Corpus:        JSONReportCorpus{Chunks: meta.Chunks},
		Aggregate: JSONReportAgg{
			RecallAtK:        agg.RecallAtK,
			RecallCases:      agg.RecallCases,
			MRRAtK:           agg.MRRAtK,
			MRRCases:         agg.MRRCases,
			InForcePrecision: agg.InForcePrecision,
			InForceCases:     agg.InForceCases,
			AbstainAccuracy:  agg.AbstainAccuracy,
			Cases:            agg.Cases,
			ExpectFailCases:  agg.ExpectFailCases,
			GapPassCases:     agg.GapPassCases,
			PoolRecall:       agg.PoolRecall,
			PoolCases:        agg.PoolCases,
		},
	}
	report.Cases = make([]JSONReportCase, len(results))
	for i, r := range results {
		gapPass := r.Case.ExpectFail && r.RecallWant > 0 && r.RecallHits == r.RecallWant
		report.Cases[i] = JSONReportCase{
			ID:               r.Case.ID,
			RecallHits:       r.RecallHits,
			RecallWant:       r.RecallWant,
			Rank:             r.Rank,
			InForcePrecision: r.InForcePrecision,
			Abstained:        r.Abstained,
			AbstainCorrect:   r.AbstainCorrect,
			ExpectFail:       r.Case.ExpectFail,
			GapPass:          gapPass,
			PoolHits:         r.PoolHits,
			PoolWant:         r.PoolWant,
			PoolRank:         r.PoolRank,
		}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}
