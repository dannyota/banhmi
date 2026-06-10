package eval

import (
	"testing"

	"danny.vn/banhmi/pkg/rag/retrieve"
)

// hit is a tiny constructor for a retrieve.Hit with the fields the metrics read.
func hit(docNumber, citation string) retrieve.Hit {
	return retrieve.Hit{DocNumber: docNumber, Citation: citation}
}

func TestRecall(t *testing.T) {
	tests := []struct {
		name      string
		expected  []ExpectedCitation
		hits      []retrieve.Hit
		wantFrac  float64
		wantFound int
		wantWant  int
	}{
		{
			name:     "no expected citations (out of scope) has no denominator",
			expected: nil,
			hits:     []retrieve.Hit{hit("50/2024/tt-nhnn", "Điều 7")},
			wantFrac: 0, wantFound: 0, wantWant: 0,
		},
		{
			name:     "doc-only expectation matched case-insensitively",
			expected: []ExpectedCitation{{SoKyHieu: "50/2024/TT-NHNN"}},
			hits:     []retrieve.Hit{hit("50/2024/tt-nhnn", "Điều 7, Khoản 2")},
			wantFrac: 1, wantFound: 1, wantWant: 1,
		},
		{
			name:     "expectation with Điều matched when a hit names it",
			expected: []ExpectedCitation{{SoKyHieu: "09/2020/tt-nhnn", Dieu: "4"}},
			hits:     []retrieve.Hit{hit("09/2020/tt-nhnn", "Điều 4")},
			wantFrac: 1, wantFound: 1, wantWant: 1,
		},
		{
			name:     "expectation with Điều missed when no hit names that Điều",
			expected: []ExpectedCitation{{SoKyHieu: "09/2020/tt-nhnn", Dieu: "4"}},
			hits:     []retrieve.Hit{hit("09/2020/tt-nhnn", "Điều 9")},
			wantFrac: 0, wantFound: 0, wantWant: 1,
		},
		{
			name: "two expected, one found → 0.5",
			expected: []ExpectedCitation{
				{SoKyHieu: "50/2024/tt-nhnn"},
				{SoKyHieu: "91/2025/qh15"},
			},
			hits:     []retrieve.Hit{hit("50/2024/tt-nhnn", "Điều 7")},
			wantFrac: 0.5, wantFound: 1, wantWant: 2,
		},
		{
			name:     "expectation with Khoan requires matching Khoan",
			expected: []ExpectedCitation{{SoKyHieu: "50/2024/tt-nhnn", Dieu: "7", Khoan: "99"}},
			hits:     []retrieve.Hit{hit("50/2024/tt-nhnn", "Điều 7, Khoản 2")},
			wantFrac: 0, wantFound: 0, wantWant: 1,
		},
		{
			name:     "expectation with Khoan matched when a hit names it",
			expected: []ExpectedCitation{{SoKyHieu: "50/2024/tt-nhnn", Dieu: "7", Khoan: "2"}},
			hits:     []retrieve.Hit{hit("50/2024/tt-nhnn", "Điều 7, Khoản 2")},
			wantFrac: 1, wantFound: 1, wantWant: 1,
		},
		{
			name:     "wrong document → miss",
			expected: []ExpectedCitation{{SoKyHieu: "50/2024/tt-nhnn"}},
			hits:     []retrieve.Hit{hit("09/2020/tt-nhnn", "Điều 4")},
			wantFrac: 0, wantFound: 0, wantWant: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Case{ExpectedCitations: tt.expected}
			frac, found, want := Recall(c, tt.hits)
			if frac != tt.wantFrac || found != tt.wantFound || want != tt.wantWant {
				t.Errorf("Recall = (%v, %d, %d), want (%v, %d, %d)",
					frac, found, want, tt.wantFrac, tt.wantFound, tt.wantWant)
			}
		})
	}
}

func TestReciprocalRank(t *testing.T) {
	tests := []struct {
		name     string
		expected []ExpectedCitation
		hits     []retrieve.Hit
		wantRR   float64
		wantRank int
	}{
		{
			name:     "no expected citations has no denominator",
			expected: nil,
			hits:     []retrieve.Hit{hit("50/2024/tt-nhnn", "Điều 7")},
			wantRR:   0, wantRank: 0,
		},
		{
			name:     "first hit",
			expected: []ExpectedCitation{{SoKyHieu: "50/2024/tt-nhnn"}},
			hits:     []retrieve.Hit{hit("50/2024/tt-nhnn", "Điều 7")},
			wantRR:   1, wantRank: 1,
		},
		{
			name:     "third hit",
			expected: []ExpectedCitation{{SoKyHieu: "50/2024/tt-nhnn"}},
			hits: []retrieve.Hit{
				hit("09/2020/tt-nhnn", "Điều 4"),
				hit("17/2024/tt-nhnn", "Điều 1"),
				hit("50/2024/tt-nhnn", "Điều 7"),
			},
			wantRR: 1.0 / 3.0, wantRank: 3,
		},
		{
			name:     "missing expected citation",
			expected: []ExpectedCitation{{SoKyHieu: "50/2024/tt-nhnn"}},
			hits:     []retrieve.Hit{hit("09/2020/tt-nhnn", "Điều 4")},
			wantRR:   0, wantRank: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotRR, gotRank := ReciprocalRank(Case{ExpectedCitations: tt.expected}, tt.hits)
			if gotRR != tt.wantRR || gotRank != tt.wantRank {
				t.Errorf("ReciprocalRank = (%v, %d), want (%v, %d)", gotRR, gotRank, tt.wantRR, tt.wantRank)
			}
		})
	}
}

func TestInForcePrecision(t *testing.T) {
	hits := []retrieve.Hit{
		{DocumentID: 1, DocNumber: "50/2024/tt-nhnn"},
		{DocumentID: 2, DocNumber: "13/2023/nđ-cp"}, // repealed in this scenario
		{DocumentID: 3, DocNumber: "91/2025/qh15"},
	}

	t.Run("all in force → 1.0", func(t *testing.T) {
		frac, ok, total := InForcePrecision(hits, func(retrieve.Hit) bool { return true })
		if frac != 1 || ok != 3 || total != 3 {
			t.Errorf("got (%v, %d, %d), want (1, 3, 3)", frac, ok, total)
		}
	})

	t.Run("repealed leak ABOVE current law → 2/3", func(t *testing.T) {
		// The non-current hit sits between current hits, so it cannot be the
		// badged trailing pass — it is a real leak and counts.
		frac, ok, total := InForcePrecision(hits, func(h retrieve.Hit) bool { return h.DocumentID != 2 })
		want := 2.0 / 3.0
		if frac != want || ok != 2 || total != 3 {
			t.Errorf("got (%v, %d, %d), want (%v, 2, 3)", frac, ok, total, want)
		}
	})

	t.Run("trailing non-current run is the badged pass → excluded", func(t *testing.T) {
		frac, ok, total := InForcePrecision(hits, func(h retrieve.Hit) bool { return h.DocumentID == 1 })
		if frac != 1 || ok != 1 || total != 1 {
			t.Errorf("got (%v, %d, %d), want (1, 1, 1)", frac, ok, total)
		}
	})

	t.Run("nothing current at all → scored over everything, 0", func(t *testing.T) {
		frac, ok, total := InForcePrecision(hits, func(retrieve.Hit) bool { return false })
		if frac != 0 || ok != 0 || total != 3 {
			t.Errorf("got (%v, %d, %d), want (0, 0, 3)", frac, ok, total)
		}
	})

	t.Run("no hits → no denominator", func(t *testing.T) {
		frac, ok, total := InForcePrecision(nil, func(retrieve.Hit) bool { return true })
		if frac != 0 || ok != 0 || total != 0 {
			t.Errorf("got (%v, %d, %d), want (0, 0, 0)", frac, ok, total)
		}
	})

	t.Run("nil predicate counts none in force (cannot tell)", func(t *testing.T) {
		frac, ok, total := InForcePrecision(hits, nil)
		if frac != 0 || ok != 0 || total != 3 {
			t.Errorf("got (%v, %d, %d), want (0, 0, 3)", frac, ok, total)
		}
	})
}

func TestAbstainCorrect(t *testing.T) {
	tests := []struct {
		name          string
		expectAbstain bool
		abstained     bool
		want          bool
	}{
		{"in-scope answered correctly", false, false, true},
		{"in-scope wrongly abstained", false, true, false},
		{"out-of-scope correctly abstained", true, true, true},
		{"out-of-scope wrongly answered", true, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Case{ExpectAbstain: tt.expectAbstain}
			if got := AbstainCorrect(c, tt.abstained); got != tt.want {
				t.Errorf("AbstainCorrect = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestScore checks that Score wires every metric together for a realistic in-scope
// case with a partially-grounded answer and one leaked repealed hit.
func TestScore(t *testing.T) {
	c := Case{
		ID:       "q-test",
		Question: "Yêu cầu xác thực giao dịch điện tử?",
		ExpectedCitations: []ExpectedCitation{
			{SoKyHieu: "50/2024/tt-nhnn", Dieu: "7"},
			{SoKyHieu: "missing/2024/tt-nhnn"},
		},
	}
	hits := []retrieve.Hit{
		{DocumentID: 1, DocNumber: "50/2024/tt-nhnn", Citation: "Điều 7, Khoản 2"},
		{DocumentID: 2, DocNumber: "13/2023/nđ-cp", Citation: "Điều 1"}, // leak above current law
		{DocumentID: 3, DocNumber: "91/2025/qh15", Citation: "Điều 3"},
	}
	inForce := func(h retrieve.Hit) bool { return h.DocumentID != 2 }

	r := Score(c, hits, false, inForce)

	if r.RecallHits != 1 || r.RecallWant != 2 || r.RecallAtK != 0.5 {
		t.Errorf("recall = %d/%d (%v), want 1/2 (0.5)", r.RecallHits, r.RecallWant, r.RecallAtK)
	}
	if r.Rank != 1 || r.MRRAtK != 1 {
		t.Errorf("mrr = rank %d rr %v, want rank 1 rr 1", r.Rank, r.MRRAtK)
	}
	if r.HitsInForce != 2 || r.HitsTotal != 3 || r.InForcePrecision != 2.0/3.0 {
		t.Errorf("in-force = %d/%d (%v), want 2/3", r.HitsInForce, r.HitsTotal, r.InForcePrecision)
	}
	if !r.AbstainCorrect {
		t.Error("AbstainCorrect = false, want true (in-scope, answered)")
	}
}

func TestCitationHasNumber(t *testing.T) {
	tests := []struct {
		citation, keyword, want string
		expect                  bool
	}{
		{"Điều 7, Khoản 2", "điều", "7", true},
		{"Điều 7, Khoản 2", "khoản", "2", true},
		{"Điều 7, Khoản 2", "điều", "2", false}, // 2 is the khoản, not the điều
		{"Điều 7", "khoản", "2", false},
		{"Điều 7a", "điều", "7A", true}, // case-insensitive on the suffix
		{"", "điều", "7", false},
	}
	for _, tt := range tests {
		if got := citationHasNumber(tt.citation, tt.keyword, tt.want); got != tt.expect {
			t.Errorf("citationHasNumber(%q, %q, %q) = %v, want %v",
				tt.citation, tt.keyword, tt.want, got, tt.expect)
		}
	}
}
