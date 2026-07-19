package eval

import (
	"testing"

	"danny.vn/banhmi/pkg/rag/retrieve"
)

// hit is a tiny constructor for a retrieve.Hit with the fields the metrics read.
func hit(docNumber, citation string) retrieve.Hit {
	return retrieve.Hit{DocNumber: docNumber, Citation: citation}
}

// Jurisdiction matchers used across tests.
var (
	vnMatcher = Matcher{ArticleKeyword: "điều", ClauseKeyword: "khoản", PointKeyword: "điểm"}
	myMatcher = Matcher{ArticleKeyword: "section", ClauseKeyword: "", PointKeyword: ""}
	idMatcher = Matcher{ArticleKeyword: "pasal", ClauseKeyword: "ayat", PointKeyword: "huruf"}
)

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
			expected: []ExpectedCitation{{DocNumber: "50/2024/TT-NHNN"}},
			hits:     []retrieve.Hit{hit("50/2024/tt-nhnn", "Điều 7, Khoản 2")},
			wantFrac: 1, wantFound: 1, wantWant: 1,
		},
		{
			name: "alt doc number satisfies the expectation",
			expected: []ExpectedCitation{{
				DocNumber:     "technology-risk-management-guidelines",
				AltDocNumbers: []string{"NBC-Risk-Management-Guidelines-July 2019"},
			}},
			hits:     []retrieve.Hit{hit("NBC-Risk-Management-Guidelines-July 2019", "Chapter 3")},
			wantFrac: 1, wantFound: 1, wantWant: 1,
		},
		{
			name: "alt doc numbers still count the expectation once",
			expected: []ExpectedCitation{{
				DocNumber:     "technology-risk-management-guidelines",
				AltDocNumbers: []string{"NBC-Risk-Management-Guidelines-July 2019"},
			}},
			hits: []retrieve.Hit{
				hit("technology-risk-management-guidelines", "Chapter 3"),
				hit("NBC-Risk-Management-Guidelines-July 2019", "Chapter 3"),
			},
			wantFrac: 1, wantFound: 1, wantWant: 1,
		},
		{
			name:     "expectation with Điều matched when a hit names it",
			expected: []ExpectedCitation{{DocNumber: "09/2020/tt-nhnn", Article: "4"}},
			hits:     []retrieve.Hit{hit("09/2020/tt-nhnn", "Điều 4")},
			wantFrac: 1, wantFound: 1, wantWant: 1,
		},
		{
			name:     "expectation with Điều missed when no hit names that Điều",
			expected: []ExpectedCitation{{DocNumber: "09/2020/tt-nhnn", Article: "4"}},
			hits:     []retrieve.Hit{hit("09/2020/tt-nhnn", "Điều 9")},
			wantFrac: 0, wantFound: 0, wantWant: 1,
		},
		{
			name: "two expected, one found → 0.5",
			expected: []ExpectedCitation{
				{DocNumber: "50/2024/tt-nhnn"},
				{DocNumber: "91/2025/qh15"},
			},
			hits:     []retrieve.Hit{hit("50/2024/tt-nhnn", "Điều 7")},
			wantFrac: 0.5, wantFound: 1, wantWant: 2,
		},
		{
			name:     "expectation with Clause requires matching Clause",
			expected: []ExpectedCitation{{DocNumber: "50/2024/tt-nhnn", Article: "7", Clause: "99"}},
			hits:     []retrieve.Hit{hit("50/2024/tt-nhnn", "Điều 7, Khoản 2")},
			wantFrac: 0, wantFound: 0, wantWant: 1,
		},
		{
			name:     "expectation with Clause matched when a hit names it",
			expected: []ExpectedCitation{{DocNumber: "50/2024/tt-nhnn", Article: "7", Clause: "2"}},
			hits:     []retrieve.Hit{hit("50/2024/tt-nhnn", "Điều 7, Khoản 2")},
			wantFrac: 1, wantFound: 1, wantWant: 1,
		},
		{
			name:     "wrong document → miss",
			expected: []ExpectedCitation{{DocNumber: "50/2024/tt-nhnn"}},
			hits:     []retrieve.Hit{hit("09/2020/tt-nhnn", "Điều 4")},
			wantFrac: 0, wantFound: 0, wantWant: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Case{ExpectedCitations: tt.expected}
			frac, found, want := Recall(c, tt.hits, vnMatcher)
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
			expected: []ExpectedCitation{{DocNumber: "50/2024/tt-nhnn"}},
			hits:     []retrieve.Hit{hit("50/2024/tt-nhnn", "Điều 7")},
			wantRR:   1, wantRank: 1,
		},
		{
			name:     "third hit",
			expected: []ExpectedCitation{{DocNumber: "50/2024/tt-nhnn"}},
			hits: []retrieve.Hit{
				hit("09/2020/tt-nhnn", "Điều 4"),
				hit("17/2024/tt-nhnn", "Điều 1"),
				hit("50/2024/tt-nhnn", "Điều 7"),
			},
			wantRR: 1.0 / 3.0, wantRank: 3,
		},
		{
			name:     "missing expected citation",
			expected: []ExpectedCitation{{DocNumber: "50/2024/tt-nhnn"}},
			hits:     []retrieve.Hit{hit("09/2020/tt-nhnn", "Điều 4")},
			wantRR:   0, wantRank: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotRR, gotRank := ReciprocalRank(Case{ExpectedCitations: tt.expected}, tt.hits, vnMatcher)
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
// case with one leaked repealed hit.
func TestScore(t *testing.T) {
	c := Case{
		ID:       "q-test",
		Question: "Yêu cầu xác thực giao dịch điện tử?",
		ExpectedCitations: []ExpectedCitation{
			{DocNumber: "50/2024/tt-nhnn", Article: "7"},
			{DocNumber: "missing/2024/tt-nhnn"},
		},
	}
	hits := []retrieve.Hit{
		{DocumentID: 1, DocNumber: "50/2024/tt-nhnn", Citation: "Điều 7, Khoản 2"},
		{DocumentID: 2, DocNumber: "13/2023/nđ-cp", Citation: "Điều 1"}, // leak above current law
		{DocumentID: 3, DocNumber: "91/2025/qh15", Citation: "Điều 3"},
	}
	inForce := func(h retrieve.Hit) bool { return h.DocumentID != 2 }

	r := Score(c, hits, false, inForce, vnMatcher)

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

// TestMatcher tests the jurisdiction-aware provision matcher across VN, MY, and ID.
func TestMatcher(t *testing.T) {
	tests := []struct {
		name    string
		matcher Matcher
		ec      ExpectedCitation
		hit     retrieve.Hit
		want    bool
	}{
		// --- VN regression (keyword-based article/clause/point) ---
		{
			name:    "VN article match",
			matcher: vnMatcher,
			ec:      ExpectedCitation{DocNumber: "09/2020/tt-nhnn", Article: "4"},
			hit:     hit("09/2020/tt-nhnn", "Điều 4"),
			want:    true,
		},
		// --- relation_ok credit (doc-level relation-framed expectations) ---
		{
			name:    "relation_ok credits hit carrying the expected relation",
			matcher: vnMatcher,
			ec:      ExpectedCitation{DocNumber: "83/2025/tt-nhnn", RelationOK: true},
			hit: retrieve.Hit{DocNumber: "09/2024/TT-NHNN", Citation: "Điều 3",
				Relations: []retrieve.Relation{{Direction: "incoming", RelationType: "amends_supplements", DocNumber: "83/2025/TT-NHNN"}}},
			want: true,
		},
		{
			name:    "relation_ok without matching relation → miss",
			matcher: vnMatcher,
			ec:      ExpectedCitation{DocNumber: "83/2025/tt-nhnn", RelationOK: true},
			hit: retrieve.Hit{DocNumber: "09/2024/TT-NHNN", Citation: "Điều 3",
				Relations: []retrieve.Relation{{DocNumber: "13/2018/TT-NHNN"}}},
			want: false,
		},
		{
			name:    "relation_ok ignored when expectation names a provision",
			matcher: vnMatcher,
			ec:      ExpectedCitation{DocNumber: "83/2025/tt-nhnn", Article: "4", RelationOK: true},
			hit: retrieve.Hit{DocNumber: "09/2024/TT-NHNN", Citation: "Điều 3",
				Relations: []retrieve.Relation{{DocNumber: "83/2025/TT-NHNN"}}},
			want: false,
		},
		{
			name:    "no relation_ok → relation alone does not count",
			matcher: vnMatcher,
			ec:      ExpectedCitation{DocNumber: "83/2025/tt-nhnn"},
			hit: retrieve.Hit{DocNumber: "09/2024/TT-NHNN", Citation: "Điều 3",
				Relations: []retrieve.Relation{{DocNumber: "83/2025/TT-NHNN"}}},
			want: false,
		},
		{
			name:    "VN article+clause match",
			matcher: vnMatcher,
			ec:      ExpectedCitation{DocNumber: "50/2024/tt-nhnn", Article: "7", Clause: "2"},
			hit:     hit("50/2024/tt-nhnn", "Điều 7, Khoản 2"),
			want:    true,
		},
		{
			name:    "VN wrong clause → miss",
			matcher: vnMatcher,
			ec:      ExpectedCitation{DocNumber: "50/2024/tt-nhnn", Article: "7", Clause: "99"},
			hit:     hit("50/2024/tt-nhnn", "Điều 7, Khoản 2"),
			want:    false,
		},
		{
			name:    "VN article+clause+point match",
			matcher: vnMatcher,
			ec:      ExpectedCitation{DocNumber: "50/2024/tt-nhnn", Article: "7", Clause: "2", Point: "a"},
			hit:     hit("50/2024/tt-nhnn", "Điều 7, Khoản 2, Điểm a"),
			want:    true,
		},
		{
			name:    "VN wrong point → miss",
			matcher: vnMatcher,
			ec:      ExpectedCitation{DocNumber: "50/2024/tt-nhnn", Article: "7", Clause: "2", Point: "b"},
			hit:     hit("50/2024/tt-nhnn", "Điều 7, Khoản 2, Điểm a"),
			want:    false,
		},
		{
			name:    "VN case-insensitive article suffix",
			matcher: vnMatcher,
			ec:      ExpectedCitation{DocNumber: "09/2020/tt-nhnn", Article: "7A"},
			hit:     hit("09/2020/tt-nhnn", "Điều 7a"),
			want:    true,
		},
		{
			name:    "VN with chapter prefix",
			matcher: vnMatcher,
			ec:      ExpectedCitation{DocNumber: "50/2024/tt-nhnn", Article: "7"},
			hit:     hit("50/2024/tt-nhnn", "Chương I, Mục A, Điều 7, Khoản 2"),
			want:    true,
		},
		// --- ID (keyword-based article/clause/point with parens) ---
		{
			name:    "ID Pasal match",
			matcher: idMatcher,
			ec:      ExpectedCitation{DocNumber: "4/2023", Article: "49"},
			hit:     hit("4/2023", "Pasal 49"),
			want:    true,
		},
		{
			name:    "ID Pasal+ayat match with parens",
			matcher: idMatcher,
			ec:      ExpectedCitation{DocNumber: "4/2023", Article: "49", Clause: "3"},
			hit:     hit("4/2023", "Pasal 49, ayat (3)"),
			want:    true,
		},
		{
			name:    "ID full Pasal+ayat+huruf match",
			matcher: idMatcher,
			ec:      ExpectedCitation{DocNumber: "4/2023", Article: "49", Clause: "3", Point: "d"},
			hit:     hit("4/2023", "Pasal 49, ayat (3), huruf d"),
			want:    true,
		},
		{
			name:    "ID wrong huruf → miss",
			matcher: idMatcher,
			ec:      ExpectedCitation{DocNumber: "4/2023", Article: "49", Clause: "3", Point: "e"},
			hit:     hit("4/2023", "Pasal 49, ayat (3), huruf d"),
			want:    false,
		},
		{
			name:    "ID with BAB prefix",
			matcher: idMatcher,
			ec:      ExpectedCitation{DocNumber: "4/2023", Article: "49", Clause: "3"},
			hit:     hit("4/2023", "BAB IV, Bagian Kesatu, Pasal 49, ayat (3)"),
			want:    true,
		},
		// --- MY (keyword article, bare-paren clause/point) ---
		{
			name:    "MY section match",
			matcher: myMatcher,
			ec:      ExpectedCitation{DocNumber: "ACT-701", Article: "14"},
			hit:     hit("ACT-701", "Section 14, Paragraph 62"),
			want:    true,
		},
		{
			name:    "MY section+clause match (bare paren)",
			matcher: myMatcher,
			ec:      ExpectedCitation{DocNumber: "ACT-701", Article: "11", Clause: "6"},
			hit:     hit("ACT-701", "Section 11, (6), (b), Paragraph 1"),
			want:    true,
		},
		{
			name:    "MY section+clause+point match (bare parens)",
			matcher: myMatcher,
			ec:      ExpectedCitation{DocNumber: "ACT-701", Article: "11", Clause: "6", Point: "b"},
			hit:     hit("ACT-701", "Section 11, (6), (b), Paragraph 1"),
			want:    true,
		},
		{
			name:    "MY clause 62 does NOT match Paragraph 62 (negative)",
			matcher: myMatcher,
			ec:      ExpectedCitation{DocNumber: "ACT-701", Article: "14", Clause: "62"},
			hit:     hit("ACT-701", "Section 14, Paragraph 62"),
			want:    false,
		},
		{
			name:    "MY wrong doc → miss",
			matcher: myMatcher,
			ec:      ExpectedCitation{DocNumber: "ACT-701", Article: "14"},
			hit:     hit("ACT-999", "Section 14"),
			want:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.matcher.Matches(tt.ec, tt.hit)
			if got != tt.want {
				t.Errorf("Matcher.Matches(%+v, %q) = %v, want %v",
					tt.ec, tt.hit.Citation, got, tt.want)
			}
		})
	}
}

func TestSameDocNumberIDCanonicalization(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{"verbose old-style POJK vs short", "Peraturan Otoritas Jasa Keuangan Nomor 11/POJK.03/2022 Tahun 2022", "POJK 11/POJK.03/2022", true},
		{"verbose new-style POJK vs short", "Peraturan Otoritas Jasa Keuangan Nomor 21 Tahun 2023", "POJK 21/2023", true},
		{"verbose SEOJK vs short", "Surat Edaran Otoritas Jasa Keuangan Nomor 24/SEOJK.03/2021", "SEOJK NOMOR 24/SEOJK.03/2021", true},
		{"PBI No. form vs short", "PBI No.10 Tahun 2025", "PBI 10/2025", true},
		{"verbose UU with parenthetical vs short", "Undang-undang (UU) Nomor 27 Tahun 2022", "UU 27/2022", true},
		{"verbose LPS vs short", "PERATURAN LEMBAGA PENJAMIN SIMPANAN 1/2023", "LPS 1/2023", true},
		{"different POJK number → miss", "POJK 21/2023", "POJK 22/2023", false},
		{"PPATK 1 vs 11 → miss", "PPATK 1/2021", "PPATK 11/2021", false},
		{"Perppu is not PP", "Peraturan Pemerintah Pengganti Undang-Undang 2/2022", "PP 2/2022", false},
		{"MY Act untouched", "ACT-701", "ACT-999", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sameDocNumber(tt.a, tt.b); got != tt.want {
				t.Errorf("sameDocNumber(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}
