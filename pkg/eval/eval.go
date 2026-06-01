// Package eval is banhmi's retrieval-quality eval harness. It scores the retriever
// (pkg/rag/retrieve) against a golden Q&A set with deterministic metrics — recall@k,
// MRR@k, current-law precision, and abstention correctness — so changes to chunking
// or retrieval can be gated before they lock in defaults (see PLAN.md and CLAUDE.md
// "accuracy first"). banhmi is evidence-only; there is no answer model to score.
//
// The metric functions here are pure: they take a golden Case and the actual
// []retrieve.Hit + an abstain flag and return numbers, with no database, so they are
// unit-tested with synthetic cases. cmd/eval wires the live retriever, runs each
// case, and aggregates these per-case scores into a report + CI gate.
package eval

import (
	"strings"

	"danny.vn/banhmi/pkg/rag/retrieve"
)

// ExpectedCitation is one legal reference a golden case expects to be supported by
// retrieval and/or the answer. SoKyHieu is the số ký hiệu (document number, e.g.
// "50/2024/tt-nhnn"); Dieu/Khoan/Diem are the optional Điều/Khoản/điểm. Matching
// is case-insensitive on SoKyHieu and exact on the Điều/Khoản/điểm labels when given.
type ExpectedCitation struct {
	SoKyHieu string `json:"so_ky_hieu"`
	Dieu     string `json:"dieu,omitempty"`
	Khoan    string `json:"khoan,omitempty"`
	Diem     string `json:"diem,omitempty"`
}

// Case is one golden question with its expectations. ID is a stable identifier for
// the report. ExpectedCitations lists the references a good answer should rest on
// (empty for an out-of-scope question). ExpectAbstain is true when the correct
// behavior is to abstain (out of scope / not in the corpus). Notes is free-form
// context for humans, ignored by the metrics.
type Case struct {
	ID                string             `json:"id"`
	Question          string             `json:"question"`
	ExpectedCitations []ExpectedCitation `json:"expected_citations"`
	ExpectAbstain     bool               `json:"expect_abstain"`
	Notes             string             `json:"notes,omitempty"`
}

// CaseResult is the scored outcome of one Case against a live answer + hits. The
// metric fields are per-case; cmd/eval aggregates them. Counts (denominators) are
// kept so aggregation can be a true micro-average rather than a mean-of-means.
type CaseResult struct {
	Case      Case
	Abstained bool // the answer abstained

	// RecallAtK: fraction of ExpectedCitations found among the retrieved hits.
	// RecallHits / RecallWant are the numerator / denominator.
	RecallAtK  float64
	RecallHits int
	RecallWant int

	// MRRAtK: reciprocal rank of the first expected citation found in the hit list.
	// Rank is 1-based; 0 means no expected citation was retrieved.
	MRRAtK float64
	Rank   int

	// CitationCorrectness: of the citations the answer made, the fraction that are
	// Grounded in the retrieved hit set (the answer core's grounded flag). Made is
	// the denominator (citations the model wrote); Grounded the numerator.
	CitationCorrectness float64
	CitationsMade       int
	CitationsGrounded   int

	// InForcePrecision: fraction of returned hits that are current law. With the
	// current-law pre-filter on this should be 1.0; a value below surfaces a leak.
	// HitsInForce / HitsTotal are the numerator / denominator.
	InForcePrecision float64
	HitsInForce      int
	HitsTotal        int

	// AbstainCorrect: the answer's Abstained matched the case's ExpectAbstain.
	AbstainCorrect bool
}

// InForceFn reports whether a retrieved hit is current law. cmd/eval supplies a
// DB-backed implementation (look up the hit's document validity);
// tests pass a synthetic predicate. It is separate from the metric so the metric
// stays pure and the database access lives in the command.
type InForceFn func(h retrieve.Hit) bool

// Recall computes recall@k for one case: the fraction of expected citations whose
// số ký hiệu, Điều, and Khoản appear among the retrieved hits when the golden case
// names them. An out-of-scope case (no expected citations) has no recall
// denominator and returns (0, 0, 0).
func Recall(c Case, hits []retrieve.Hit) (frac float64, found, want int) {
	want = len(c.ExpectedCitations)
	if want == 0 {
		return 0, 0, 0
	}
	for _, ec := range c.ExpectedCitations {
		if expectedInHits(ec, hits) {
			found++
		}
	}
	return float64(found) / float64(want), found, want
}

// expectedInHits reports whether some retrieved hit matches the expected citation:
// same số ký hiệu, and — when the expectation gives Điều/Khoản — a hit citation
// that names the same provision.
func expectedInHits(ec ExpectedCitation, hits []retrieve.Hit) bool {
	for _, h := range hits {
		if expectedMatchesHit(ec, h) {
			return true
		}
	}
	return false
}

func expectedMatchesHit(ec ExpectedCitation, h retrieve.Hit) bool {
	if !sameDocNumber(h.DocNumber, ec.SoKyHieu) {
		return false
	}
	if ec.Dieu != "" && !citationHasNumber(h.Citation, "điều", ec.Dieu) {
		return false
	}
	if ec.Khoan != "" && !citationHasNumber(h.Citation, "khoản", ec.Khoan) {
		return false
	}
	return true
}

// ReciprocalRank computes reciprocal rank for one case: 1/rank of the first
// retrieved hit matching any expected citation. Missing expected citations
// contribute 0. Out-of-scope cases have no denominator and return (0, 0).
func ReciprocalRank(c Case, hits []retrieve.Hit) (rr float64, rank int) {
	if len(c.ExpectedCitations) == 0 {
		return 0, 0
	}
	for i, h := range hits {
		for _, ec := range c.ExpectedCitations {
			if expectedMatchesHit(ec, h) {
				rank = i + 1
				return 1.0 / float64(rank), rank
			}
		}
	}
	return 0, 0
}

// InForcePrecision computes the fraction of returned hits that are current law, using
// the supplied predicate. With the current-law pre-filter on this should be 1.0; any
// lower value means repealed/expired/not-yet-effective law leaked into retrieval.
// No hits returns (0, 0, 0). A nil predicate means "cannot tell" and counts every
// hit as not-current (precision 0) rather than silently passing.
func InForcePrecision(hits []retrieve.Hit, inForce InForceFn) (frac float64, ok, total int) {
	total = len(hits)
	if total == 0 {
		return 0, 0, 0
	}
	for _, h := range hits {
		if inForce != nil && inForce(h) {
			ok++
		}
	}
	return float64(ok) / float64(total), ok, total
}

// AbstainCorrect reports whether the run's abstention matched the case's
// expectation: an out-of-scope case should abstain, an in-scope one should not.
func AbstainCorrect(c Case, abstained bool) bool {
	return abstained == c.ExpectAbstain
}

// Score runs every retrieval metric for one case and returns the combined
// CaseResult. hits is the retrieved evidence; abstained is whether the run decided to
// abstain (no hits / below the score floor). inForce backs the current-law precision
// metric.
func Score(c Case, hits []retrieve.Hit, abstained bool, inForce InForceFn) CaseResult {
	r := CaseResult{Case: c, Abstained: abstained}
	r.RecallAtK, r.RecallHits, r.RecallWant = Recall(c, hits)
	r.MRRAtK, r.Rank = ReciprocalRank(c, hits)
	r.InForcePrecision, r.HitsInForce, r.HitsTotal = InForcePrecision(hits, inForce)
	r.AbstainCorrect = AbstainCorrect(c, abstained)
	return r
}

// sameDocNumber compares two số ký hiệu, ignoring case and surrounding whitespace.
// Vietnamese legal numbers are ASCII apart from Đ, which upper-casing folds, so
// "50/2024/tt-nhnn" and "50/2024/TT-NHNN" compare equal.
func sameDocNumber(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// citationHasNumber reports whether a hit citation ("Điều 7, Khoản 2") names the
// given number after the given keyword ("điều" or "khoản"). It scans the
// comma/space-separated parts and matches the token after a case-insensitive
// keyword against want (case-insensitive so "7a" works).
func citationHasNumber(citation, keyword, want string) bool {
	fields := strings.FieldsFunc(citation, func(r rune) bool {
		return r == ',' || r == ' '
	})
	for i := 0; i < len(fields)-1; i++ {
		if strings.EqualFold(fields[i], keyword) && strings.EqualFold(fields[i+1], want) {
			return true
		}
	}
	return false
}
