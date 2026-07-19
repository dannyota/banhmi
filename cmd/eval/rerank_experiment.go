package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"time"

	"danny.vn/banhmi/pkg/base/config"
	"danny.vn/banhmi/pkg/eval"
	"danny.vn/banhmi/pkg/rag/retrieve"
)

// The offline reranker experiment (PLAN.md "Reranker experiment"): -rerank-dump
// writes each recall case's query + deep candidate list as JSONL for external
// (Kaggle GPU) scoring; -rerank-scores replays those candidates reordered by
// the returned scores through the standard matcher, so the reranker's effect on
// recall@k/MRR is measured with zero retrieval or prod changes.

// rerankDumpMaxRunes bounds a candidate's text so the scoring prompt stays
// within a comfortable token budget on the GPU side.
const rerankDumpMaxRunes = 2400

type rerankDumpCase struct {
	Jurisdiction string            `json:"jurisdiction"`
	CaseID       string            `json:"case_id"`
	Query        string            `json:"query"`
	Candidates   []rerankCandidate `json:"candidates"`
	// Appendix is the production search's badged non-current tail. It is never
	// reranked — production pins it after the primary ranking, and letting the
	// reranker interleave superseded law into the top-k both breaks the
	// current-law contract and (measured) craters recall on corpora with long
	// supersession chains.
	Appendix []rerankCandidate `json:"appendix,omitempty"`
}

type rerankCandidate struct {
	ChunkID    int64    `json:"chunk_id"`
	DocumentID int64    `json:"document_id"`
	DocNumber  string   `json:"doc_number"`
	Citation   string   `json:"citation"`
	Relations  []string `json:"relations,omitempty"`
	Text       string   `json:"text"`
}

type rerankScoreLine struct {
	CaseID string    `json:"case_id"`
	Scores []float64 `json:"scores"`
}

func newRerankDumpCase(jurisdiction string, c eval.Case, hits []retrieve.Hit) rerankDumpCase {
	d := rerankDumpCase{Jurisdiction: jurisdiction, CaseID: c.ID, Query: c.Question}
	for _, h := range hits {
		text := rerankDocument(h)
		if rs := []rune(text); len(rs) > rerankDumpMaxRunes {
			text = string(rs[:rerankDumpMaxRunes])
		}
		cand := rerankCandidate{
			ChunkID:    h.ChunkID,
			DocumentID: h.DocumentID,
			DocNumber:  h.DocNumber,
			Citation:   h.Citation,
			Text:       text,
		}
		for _, rel := range h.Relations {
			if rel.DocNumber != "" {
				cand.Relations = append(cand.Relations, rel.DocNumber)
			}
		}
		d.Candidates = append(d.Candidates, cand)
	}
	return d
}

// hitFromCandidate rebuilds the minimal Hit the matcher and current-law checker
// read. Text is not needed for scoring.
func hitFromCandidate(c rerankCandidate) retrieve.Hit {
	h := retrieve.Hit{
		ChunkID:    c.ChunkID,
		DocumentID: c.DocumentID,
		DocNumber:  c.DocNumber,
		Citation:   c.Citation,
	}
	for _, rel := range c.Relations {
		h.Relations = append(h.Relations, retrieve.Relation{DocNumber: rel})
	}
	return h
}

// rerankAndCap orders candidates by descending score (stable on ties) and
// applies the production shape: at most docCap hits per document, topK total.
func rerankAndCap(cands []rerankCandidate, scores []float64, docCap, topK int) []retrieve.Hit {
	idx := make([]int, len(cands))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		var sa, sb float64
		if idx[a] < len(scores) {
			sa = scores[idx[a]]
		}
		if idx[b] < len(scores) {
			sb = scores[idx[b]]
		}
		return sa > sb
	})
	perDoc := make(map[int64]int)
	out := make([]retrieve.Hit, 0, topK)
	for _, i := range idx {
		if len(out) >= topK {
			break
		}
		c := cands[i]
		if docCap > 0 && perDoc[c.DocumentID] >= docCap {
			continue
		}
		perDoc[c.DocumentID]++
		out = append(out, hitFromCandidate(c))
	}
	return out
}

func writeRerankDump(path string, dumps []rerankDumpCase) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create rerank dump: %w", err)
	}
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for _, d := range dumps {
		if err := enc.Encode(d); err != nil {
			_ = f.Close()
			return fmt.Errorf("encode rerank dump case %q: %w", d.CaseID, err)
		}
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		return fmt.Errorf("flush rerank dump: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close rerank dump: %w", err)
	}
	return nil
}

func readRerankDump(path string) (map[string]rerankDumpCase, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open rerank dump: %w", err)
	}
	defer func() { _ = f.Close() }()
	out := make(map[string]rerankDumpCase)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
	for sc.Scan() {
		var d rerankDumpCase
		if err := json.Unmarshal(sc.Bytes(), &d); err != nil {
			return nil, fmt.Errorf("parse rerank dump line: %w", err)
		}
		out[d.CaseID] = d
	}
	return out, sc.Err()
}

// evaluateRerankScores replays externally-scored candidates: for every golden
// case present in the dump, candidates are reordered by the external scores,
// capped to the production shape, and scored with the standard matcher. Only
// dumped (recall) cases contribute — abstain cases are not part of the
// experiment.
func evaluateRerankScores(
	cfg *config.Config,
	o opts,
	cases []eval.Case,
	matcher eval.Matcher,
	inForce eval.InForceFn,
	log *slog.Logger,
) error {
	dumps, err := readRerankDump(o.rerankDump)
	if err != nil {
		return err
	}
	scores, err := readRerankScores(o.rerankScores)
	if err != nil {
		return err
	}
	docCap := o.docCap
	if docCap == 0 {
		if cfg.Retrieve.DocCap > 0 {
			docCap = cfg.Retrieve.DocCap
		} else {
			docCap = 3
		}
	}
	topK := effectiveTopK(cfg, o.topK)

	var results []eval.CaseResult
	missing := 0
	for _, c := range cases {
		d, ok := dumps[c.ID]
		if !ok {
			continue
		}
		s, ok := scores[c.ID]
		if !ok {
			missing++
			continue
		}
		hits := rerankAndCap(d.Candidates, s, docCap, topK)
		seen := make(map[int64]bool, len(hits))
		for _, h := range hits {
			seen[h.ChunkID] = true
		}
		for _, a := range d.Appendix {
			if !seen[a.ChunkID] {
				hits = append(hits, hitFromCandidate(a))
			}
		}
		results = append(results, eval.Score(c, hits, false, inForce, matcher))
	}
	if missing > 0 {
		log.Warn("rerank scores missing for some dumped cases", "missing", missing)
	}
	agg := eval.Summarize(results)
	eval.WriteReport(os.Stdout, results, agg)
	if o.outPath != "" {
		meta := eval.JSONReportMeta{
			Jurisdiction:  cfg.Jurisdiction,
			RetrievalMode: "rerank-experiment",
			TopK:          topK,
			DocCap:        docCap,
			GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		}
		if err := writeJSONReportFile(o.outPath, meta, results, agg); err != nil {
			return err
		}
		log.Info("wrote JSON report", "path", o.outPath)
	}
	return nil
}

func readRerankScores(path string) (map[string][]float64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open rerank scores: %w", err)
	}
	defer func() { _ = f.Close() }()
	out := make(map[string][]float64)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
	for sc.Scan() {
		var l rerankScoreLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			return nil, fmt.Errorf("parse rerank score line: %w", err)
		}
		out[l.CaseID] = l.Scores
	}
	return out, sc.Err()
}
