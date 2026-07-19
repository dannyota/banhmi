// Command wfscore scores agent-workflow eval transcripts against a workflow
// golden set and prints the per-case table plus the aggregate. Transcripts are
// one JSON file per case (eval.WorkflowTranscript) in -transcripts DIR.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"danny.vn/banhmi/pkg/eval"
)

func main() {
	golden := flag.String("golden", "deploy/eval/workflow_golden_vn.json", "workflow golden set")
	dir := flag.String("transcripts", "", "directory of per-case transcript JSON files")
	flag.Parse()
	cases, err := eval.LoadWorkflowGolden(*golden)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	byID := map[string]eval.WorkflowCase{}
	for _, c := range cases {
		byID[c.ID] = c
	}
	var results []eval.WorkflowResult
	fmt.Println("CASE                                CITE   ABSTAIN  RELATIONS")
	entries, _ := filepath.Glob(filepath.Join(*dir, "*.json"))
	for _, f := range entries {
		b, err := os.ReadFile(f)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			continue
		}
		var t eval.WorkflowTranscript
		if err := json.Unmarshal(b, &t); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", f, err)
			continue
		}
		c, ok := byID[t.CaseID]
		if !ok {
			fmt.Fprintf(os.Stderr, "unknown case %q\n", t.CaseID)
			continue
		}
		r := eval.ScoreWorkflow(c, t)
		results = append(results, r)
		fmt.Printf("%-35s %.2f   %-7v  %v\n", t.CaseID, r.CitationCorrect, r.AbstainCorrect, r.FollowedRelations)
	}
	agg := eval.SummarizeWorkflow(results, cases)
	fmt.Printf("\ncases scored: %d/%d\ncitation score: %.1f%%\nabstention: %.1f%%\nrelation-following: %.1f%%\n",
		len(results), len(cases), agg.MeanCitationScore*100, agg.AbstainAccuracy*100, agg.RelationCompliance*100)
}
