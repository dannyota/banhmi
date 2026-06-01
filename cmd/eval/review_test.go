package main

import (
	"strings"
	"testing"

	"danny.vn/banhmi/pkg/eval"
	"danny.vn/banhmi/pkg/rag/retrieve"
)

func TestPreviewTextCompactsWhitespaceAndTruncates(t *testing.T) {
	got := previewText("  một\n\nhai\tba bốn  ", 9)
	if got != "một hai …" {
		t.Fatalf("previewText = %q, want compact truncated preview", got)
	}
}

func TestReviewExpectation(t *testing.T) {
	got := reviewExpectation(eval.Case{
		ExpectedCitations: []eval.ExpectedCitation{{
			SoKyHieu: "50/2024/TT-NHNN",
			Dieu:     "7",
			Khoan:    "2",
		}},
	})
	if !strings.Contains(got, "50/2024/TT-NHNN Điều 7 Khoản 2") {
		t.Fatalf("reviewExpectation = %q", got)
	}

	if got := reviewExpectation(eval.Case{ExpectAbstain: true}); got != "expected abstain" {
		t.Fatalf("reviewExpectation abstain = %q", got)
	}
}

func TestHitMatchesAnyExpected(t *testing.T) {
	c := eval.Case{ExpectedCitations: []eval.ExpectedCitation{{
		SoKyHieu: "50/2024/tt-nhnn",
		Dieu:     "7",
		Khoan:    "2",
	}}}

	if !hitMatchesAnyExpected(c, retrieve.Hit{DocNumber: "50/2024/TT-NHNN", Citation: "Điều 7, Khoản 2"}) {
		t.Fatal("hitMatchesAnyExpected = false, want true")
	}
	if hitMatchesAnyExpected(c, retrieve.Hit{DocNumber: "50/2024/TT-NHNN", Citation: "Điều 8"}) {
		t.Fatal("hitMatchesAnyExpected = true for wrong article")
	}
	if hitMatchesAnyExpected(c, retrieve.Hit{DocNumber: "50/2024/TT-NHNN", Citation: "Điều 7, Khoản 3"}) {
		t.Fatal("hitMatchesAnyExpected = true for wrong clause")
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
