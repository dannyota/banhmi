package main

import (
	"strings"
	"testing"

	"danny.vn/banhmi/pkg/eval"
	"danny.vn/banhmi/pkg/rag/retrieve"
)

func TestPreviewTextCompactsWhitespaceAndTruncates(t *testing.T) {
	got := previewText("  one\n\ntwo\tthree four  ", 12)
	if got != "one two thr…" {
		t.Fatalf("previewText = %q, want compact truncated preview", got)
	}
}

func TestReviewExpectation(t *testing.T) {
	vnM := eval.Matcher{ArticleKeyword: "điều", ClauseKeyword: "khoản", PointKeyword: "điểm"}
	myM := eval.Matcher{ArticleKeyword: "section", ClauseKeyword: "", PointKeyword: ""}
	idM := eval.Matcher{ArticleKeyword: "pasal", ClauseKeyword: "ayat", PointKeyword: "huruf"}

	t.Run("VN", func(t *testing.T) {
		got := reviewExpectation(eval.Case{
			ExpectedCitations: []eval.ExpectedCitation{{
				DocNumber: "50/2024/TT-NHNN",
				Article:   "7",
				Clause:    "2",
			}},
		}, vnM)
		if !strings.Contains(got, "50/2024/TT-NHNN điều 7 khoản 2") {
			t.Fatalf("reviewExpectation VN = %q", got)
		}
	})

	t.Run("MY bare-paren clause", func(t *testing.T) {
		got := reviewExpectation(eval.Case{
			ExpectedCitations: []eval.ExpectedCitation{{
				DocNumber: "ACT-701",
				Article:   "11",
				Clause:    "6",
			}},
		}, myM)
		if !strings.Contains(got, "ACT-701 section 11 (6)") {
			t.Fatalf("reviewExpectation MY = %q", got)
		}
	})

	t.Run("ID", func(t *testing.T) {
		got := reviewExpectation(eval.Case{
			ExpectedCitations: []eval.ExpectedCitation{{
				DocNumber: "4/2023",
				Article:   "49",
				Clause:    "3",
				Point:     "d",
			}},
		}, idM)
		if !strings.Contains(got, "4/2023 pasal 49 ayat 3 huruf d") {
			t.Fatalf("reviewExpectation ID = %q", got)
		}
	})

	t.Run("abstain", func(t *testing.T) {
		if got := reviewExpectation(eval.Case{ExpectAbstain: true}, vnM); got != "expected abstain" {
			t.Fatalf("reviewExpectation abstain = %q", got)
		}
	})
}

func TestMatcherMatchesAnyVN(t *testing.T) {
	m := eval.Matcher{ArticleKeyword: "điều", ClauseKeyword: "khoản", PointKeyword: "điểm"}
	c := eval.Case{ExpectedCitations: []eval.ExpectedCitation{{
		DocNumber: "50/2024/tt-nhnn",
		Article:   "7",
		Clause:    "2",
	}}}

	if !m.MatchesAny(c, retrieve.Hit{DocNumber: "50/2024/TT-NHNN", Citation: "Điều 7, Khoản 2"}) {
		t.Fatal("MatchesAny = false, want true")
	}
	if m.MatchesAny(c, retrieve.Hit{DocNumber: "50/2024/TT-NHNN", Citation: "Điều 8"}) {
		t.Fatal("MatchesAny = true for wrong article")
	}
	if m.MatchesAny(c, retrieve.Hit{DocNumber: "50/2024/TT-NHNN", Citation: "Điều 7, Khoản 3"}) {
		t.Fatal("MatchesAny = true for wrong clause")
	}
}

func TestRetrievalShouldAbstain(t *testing.T) {
	if !retrievalShouldAbstain(nil, 0) {
		t.Fatal("retrievalShouldAbstain(nil) = false, want true")
	}
	if retrievalShouldAbstain([]retrieve.Hit{{Score: 0.01}}, 0) {
		t.Fatal("retrievalShouldAbstain with disabled floor = true, want false")
	}
	if !retrievalShouldAbstain([]retrieve.Hit{{Score: 0.01}}, 0.02) {
		t.Fatal("retrievalShouldAbstain below floor = false, want true")
	}
	if retrievalShouldAbstain([]retrieve.Hit{{Score: 0.03}}, 0.02) {
		t.Fatal("retrievalShouldAbstain above floor = true, want false")
	}
}
