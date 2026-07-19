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
	"regexp"
	"strings"
	"unicode"

	"danny.vn/banhmi/pkg/rag/retrieve"
)

// ExpectedCitation is one legal reference a golden case expects to be supported by
// retrieval and/or the answer. DocNumber is the document number (e.g.
// "50/2024/tt-nhnn"); Article/Clause/Point are the optional provision labels. Matching
// is case-insensitive on DocNumber and exact on the Article/Clause/Point labels when given.
type ExpectedCitation struct {
	DocNumber string `json:"doc_number"`
	Article   string `json:"article,omitempty"`
	Clause    string `json:"clause,omitempty"`
	Point     string `json:"point,omitempty"`

	// RelationOK credits a hit whose confirmed Relations name the expected
	// document — for relation-framed questions ("was X amended by anything?")
	// where a hit of the base document carrying the amends_supplements relation
	// IS the evidence an agent needs. Doc-level only: it is ignored when
	// Article/Clause/Point are set (a relation cannot evidence a provision).
	RelationOK bool `json:"relation_ok,omitempty"`

	// AltDocNumbers lists alternate document numbers that satisfy this
	// expectation — for documents that exist under more than one identity
	// (cross-source duplicates, superseding editions). A hit matching ANY of
	// DocNumber/AltDocNumbers counts; the citation still needs the same
	// article/clause/point when those are set.
	AltDocNumbers []string `json:"alt_doc_numbers,omitempty"`
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
	ExpectFail        bool               `json:"expect_fail,omitempty"`
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

	// InForcePrecision: fraction of returned hits that are current law. With the
	// current-law pre-filter on this should be 1.0; a value below surfaces a leak.
	// HitsInForce / HitsTotal are the numerator / denominator.
	InForcePrecision float64
	HitsInForce      int
	HitsTotal        int

	// AbstainCorrect: the answer's Abstained matched the case's ExpectAbstain.
	AbstainCorrect bool

	// Pool probe (optional; cmd/eval -pool-k): the same expectations scored against
	// a deep candidate retrieval (e.g. top-100 fused, per-doc cap lifted). PoolWant
	// == 0 means the case was not probed. A case with RecallHits < RecallWant but
	// PoolHits == PoolWant is a RANKING failure (a reranker could recover it); one
	// with PoolHits < PoolWant is a RETRIEVAL/coverage failure (it cannot).
	PoolHits int
	PoolWant int
	PoolRank int // 1-based rank of the first expected citation in the pool; 0 = absent
}

// InForceFn reports whether a retrieved hit is current law. cmd/eval supplies a
// DB-backed implementation (look up the hit's document validity);
// tests pass a synthetic predicate. It is separate from the metric so the metric
// stays pure and the database access lives in the command.
type InForceFn func(h retrieve.Hit) bool

// Matcher holds the jurisdiction-specific citation keywords used to match golden
// article/clause/point expectations against chunk citations. Fields are lower-case
// keywords (e.g. "điều", "section", "pasal"). An empty ClauseKeyword or
// PointKeyword means the jurisdiction cites those levels as bare parenthesized
// tokens (e.g. MY "(6)" for clause).
type Matcher struct {
	ArticleKeyword string
	ClauseKeyword  string
	PointKeyword   string
}

// Matches reports whether a retrieved hit matches the expected citation: same
// document number, and — when the expectation gives article/clause/point — a hit
// citation that names the same provision according to the jurisdiction's keywords.
func (m Matcher) Matches(ec ExpectedCitation, h retrieve.Hit) bool {
	if matchesAnyDocNumber(h.DocNumber, ec) {
		if ec.Article != "" && !m.citationHas(h.Citation, m.ArticleKeyword, ec.Article) {
			return false
		}
		if ec.Clause != "" && !m.citationHas(h.Citation, m.ClauseKeyword, ec.Clause) {
			return false
		}
		if ec.Point != "" && !m.citationHas(h.Citation, m.PointKeyword, ec.Point) {
			return false
		}
		return true
	}
	// Relation credit: a doc-level expectation flagged relation_ok is satisfied
	// by a hit whose confirmed relations name the expected document.
	if ec.RelationOK && ec.Article == "" && ec.Clause == "" && ec.Point == "" {
		for _, rel := range h.Relations {
			if matchesAnyDocNumber(rel.DocNumber, ec) {
				return true
			}
		}
	}
	return false
}

// matchesAnyDocNumber reports whether got matches the expectation's DocNumber or
// any of its AltDocNumbers.
func matchesAnyDocNumber(got string, ec ExpectedCitation) bool {
	if sameDocNumber(got, ec.DocNumber) {
		return true
	}
	for _, alt := range ec.AltDocNumbers {
		if sameDocNumber(got, alt) {
			return true
		}
	}
	return false
}

// MatchesAny reports whether a retrieved hit matches any of the case's expected
// citations.
func (m Matcher) MatchesAny(c Case, h retrieve.Hit) bool {
	for _, ec := range c.ExpectedCitations {
		if m.Matches(ec, h) {
			return true
		}
	}
	return false
}

// citationHas checks whether a citation string contains a provision value for the
// given keyword. When keyword is non-empty, it looks for a token equaling the
// keyword (case-insensitive) followed by a token whose parentheses-trimmed value
// equals want. When keyword is empty, it looks for a bare parenthesized token
// "(value)" — matching jurisdictions like MY that cite clauses as "(6)".
func (m Matcher) citationHas(citation, keyword, want string) bool {
	fields := tokenizeCitation(citation)
	if keyword != "" {
		for i := 0; i < len(fields)-1; i++ {
			if strings.EqualFold(fields[i], keyword) {
				trimmed := strings.TrimFunc(fields[i+1], func(r rune) bool {
					return r == '(' || r == ')' || r == '.'
				})
				if strings.EqualFold(trimmed, want) {
					return true
				}
			}
		}
		return false
	}
	// Empty keyword: look for a bare "(value)" token.
	target := "(" + want + ")"
	for _, f := range fields {
		if strings.EqualFold(f, target) {
			return true
		}
	}
	return false
}

// tokenizeCitation splits a citation string on commas and spaces.
func tokenizeCitation(citation string) []string {
	return strings.FieldsFunc(citation, func(r rune) bool {
		return r == ',' || unicode.IsSpace(r)
	})
}

// Recall computes recall@k for one case: the fraction of expected citations whose
// document number, article, clause, and point appear among the retrieved hits when
// the golden case names them. An out-of-scope case (no expected citations) has no
// recall denominator and returns (0, 0, 0).
func Recall(c Case, hits []retrieve.Hit, m Matcher) (frac float64, found, want int) {
	want = len(c.ExpectedCitations)
	if want == 0 {
		return 0, 0, 0
	}
	for _, ec := range c.ExpectedCitations {
		if expectedInHits(ec, hits, m) {
			found++
		}
	}
	return float64(found) / float64(want), found, want
}

// expectedInHits reports whether some retrieved hit matches the expected citation.
func expectedInHits(ec ExpectedCitation, hits []retrieve.Hit, m Matcher) bool {
	for _, h := range hits {
		if m.Matches(ec, h) {
			return true
		}
	}
	return false
}

// ReciprocalRank computes reciprocal rank for one case: 1/rank of the first
// retrieved hit matching any expected citation. Missing expected citations
// contribute 0. Out-of-scope cases have no denominator and return (0, 0).
func ReciprocalRank(c Case, hits []retrieve.Hit, m Matcher) (rr float64, rank int) {
	if len(c.ExpectedCitations) == 0 {
		return 0, 0
	}
	for i, h := range hits {
		for _, ec := range c.ExpectedCitations {
			if m.Matches(ec, h) {
				rank = i + 1
				return 1.0 / float64(rank), rank
			}
		}
	}
	return 0, 0
}

// InForcePrecision computes the fraction of returned hits that are current law, using
// the supplied predicate. The default search deliberately APPENDS a small badged pass
// of non-current law after the current results, so the trailing run of non-current
// hits is excluded first — it is disclosed evidence, not a leak. Any non-current hit
// ABOVE the last current hit still counts against precision (a real leak: stale
// validity or filter failure). With the current-law pre-filter this should be 1.0.
// No hits returns (0, 0, 0). A nil predicate means "cannot tell" and counts every
// hit as not-current (precision 0) rather than silently passing.
func InForcePrecision(hits []retrieve.Hit, inForce InForceFn) (frac float64, ok, total int) {
	// Trim the trailing badged non-current run.
	end := len(hits)
	for end > 0 && (inForce == nil || !inForce(hits[end-1])) {
		end--
	}
	if end == 0 && len(hits) > 0 {
		// Nothing current at all: score over everything rather than vacuously pass.
		end = len(hits)
	}
	scored := hits[:end]
	total = len(scored)
	if total == 0 {
		return 0, 0, 0
	}
	for _, h := range scored {
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
// metric. m is the jurisdiction-aware provision matcher.
func Score(c Case, hits []retrieve.Hit, abstained bool, inForce InForceFn, m Matcher) CaseResult {
	r := CaseResult{Case: c, Abstained: abstained}
	r.RecallAtK, r.RecallHits, r.RecallWant = Recall(c, hits, m)
	r.MRRAtK, r.Rank = ReciprocalRank(c, hits, m)
	r.InForcePrecision, r.HitsInForce, r.HitsTotal = InForcePrecision(hits, inForce)
	r.AbstainCorrect = AbstainCorrect(c, abstained)
	return r
}

// SameDocNumber reports whether two document-number strings identify the same
// document after canonicalization. Exported for cmd/eval's corpus-coverage
// reporter so it agrees with the matcher instead of exact-matching (canonical
// short-code corpora vs verbose golden forms produced false "missing" warnings).
func SameDocNumber(a, b string) bool { return sameDocNumber(a, b) }

// sameDocNumber compares two document numbers after canonicalizing both:
// verbose Indonesian type phrases fold to their short codes and the filler
// words NOMOR/TAHUN/NO drop, then both sides strip to uppercase alphanumerics
// and match on equality or containment. This bridges format divergence across
// sources and goldens — "Peraturan Otoritas Jasa Keuangan Nomor 21 Tahun 2023"
// and "POJK 21/2023" both canonicalize to "POJK212023".
func sameDocNumber(a, b string) bool {
	if strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b)) {
		return true
	}
	na := docNumberNorm(a)
	nb := docNumberNorm(b)
	return na == nb || strings.Contains(na, nb) || strings.Contains(nb, na)
}

// idVerboseDocTypes folds verbose Indonesian regulation-type phrases to the
// short codes silver stores after source normalization. Mirrors
// idDocTypeShortCodes in pkg/pipeline (eval does not import pipeline).
// Longer phrases first so PERPPU never half-matches as PP + UU.
var idVerboseDocTypes = []struct{ verbose, short string }{
	{"PERATURAN PEMERINTAH PENGGANTI UNDANG-UNDANG", "PERPPU"},
	{"PERATURAN KEPALA PUSAT PELAPORAN DAN ANALISIS TRANSAKSI KEUANGAN", "PPATK"},
	{"PERATURAN PUSAT PELAPORAN DAN ANALISIS TRANSAKSI KEUANGAN", "PPATK"},
	{"PERATURAN MENTERI KOMUNIKASI DAN INFORMATIKA", "KOMINFO"},
	{"PERATURAN MENTERI KOMUNIKASI DAN DIGITAL", "KOMDIGI"},
	{"PERATURAN BADAN SIBER DAN SANDI NEGARA", "BSSN"},
	{"SURAT EDARAN OTORITAS JASA KEUANGAN", "SEOJK"},
	{"PERATURAN OTORITAS JASA KEUANGAN", "POJK"},
	{"PERATURAN ANGGOTA DEWAN GUBERNUR", "PADG"},
	{"PERATURAN LEMBAGA PENJAMIN SIMPANAN", "LPS"},
	{"SURAT EDARAN BANK INDONESIA", "SEBI"},
	{"PERATURAN BANK INDONESIA", "PBI"},
	{"PERATURAN MENTERI KEUANGAN", "PMK"},
	{"PERATURAN PEMERINTAH", "PP"},
	{"PERATURAN PRESIDEN", "PERPRES"},
	{"UNDANG-UNDANG", "UU"},
}

// idFillerWords are number-format filler tokens (NOMOR/TAHUN/NO.) that carry
// no identity; they only appear in Indonesian and gazette-style numbers, never
// inside another jurisdiction's identifiers (\b guards partial words).
var idFillerWords = regexp.MustCompile(`\b(NOMOR|TAHUN|NO)\b\.?`)

var nonAlphaNum = regexp.MustCompile(`[^a-zA-Z0-9]`)

func docNumberNorm(s string) string {
	u := strings.ToUpper(s)
	for _, t := range idVerboseDocTypes {
		u = strings.ReplaceAll(u, t.verbose, t.short)
	}
	u = idFillerWords.ReplaceAllString(u, "")
	return nonAlphaNum.ReplaceAllString(u, "")
}
