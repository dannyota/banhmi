package mcp

import (
	"strings"
	"testing"
)

func TestBriefForSelectsJurisdictionWithVNFallback(t *testing.T) {
	cases := []struct {
		jurisdiction string
		wantName     string
	}{
		{"my", "laksa"},
		{"id", "rendang"},
		{"vn", "banhmi"},
		{"", "banhmi"},   // unset → VN default
		{"xx", "banhmi"}, // unknown → VN fallback
	}
	for _, c := range cases {
		if got := briefFor(c.jurisdiction).name; got != c.wantName {
			t.Errorf("briefFor(%q).name = %q, want %q", c.jurisdiction, got, c.wantName)
		}
	}
}

// TestBriefsSatisfyGuideContract guards the invariants the guide tool and its tests
// rely on, for every jurisdiction's brief.
func TestBriefsSatisfyGuideContract(t *testing.T) {
	for _, b := range []brief{vnBrief, myBrief, idBrief} {
		if !strings.Contains(b.guide.Purpose, "database evidence") {
			t.Errorf("%s guide.Purpose missing evidence boundary: %q", b.name, b.guide.Purpose)
		}
		if len(b.guide.Tools) < 3 || len(b.guide.RecommendedFlow) == 0 || len(b.guide.EvidenceContract) == 0 {
			t.Errorf("%s guide payload incomplete: tools=%d flow=%d contract=%d",
				b.name, len(b.guide.Tools), len(b.guide.RecommendedFlow), len(b.guide.EvidenceContract))
		}
		for _, s := range []string{b.instructions, b.guideDesc, b.searchDesc, b.documentDesc, b.coverageFmt} {
			if strings.TrimSpace(s) == "" {
				t.Errorf("%s brief has an empty required field", b.name)
			}
		}
	}
}

// TestMYBriefIsEnglishOnly checks the Malaysia brief does not leak Vietnamese
// provision vocabulary, and the VN brief does not leak the laksa product name —
// the one-language-per-country boundary.
func TestMYBriefIsEnglishOnly(t *testing.T) {
	myText := myBrief.instructions + myBrief.searchDesc + myBrief.documentDesc +
		myBrief.guide.Purpose + strings.Join(myBrief.guide.RecommendedFlow, " ")
	for _, vn := range []string{"Điều", "Khoản", "Điểm", "số ký hiệu", "Đoạn", "Vietnamese", "banhmi"} {
		if strings.Contains(myText, vn) {
			t.Errorf("MY brief leaks VN/foreign token %q", vn)
		}
	}
	if !strings.Contains(myText, "Section") || !strings.Contains(myText, "Malaysia") {
		t.Error("MY brief should reference Section / Malaysia")
	}

	vnText := vnBrief.instructions + vnBrief.searchDesc + vnBrief.guide.Purpose
	if strings.Contains(vnText, "laksa") || strings.Contains(vnText, "Malaysia") {
		t.Error("VN brief leaks Malaysia/laksa")
	}
	if strings.Contains(vnText, "rendang") || strings.Contains(vnText, "Pasal") {
		t.Error("VN brief leaks Indonesia/rendang")
	}
}

// TestIDBriefLanguageBoundary checks the Indonesia brief does not leak Vietnamese
// or Malaysian provision vocabulary, and vice versa.
func TestIDBriefLanguageBoundary(t *testing.T) {
	idText := idBrief.instructions + idBrief.searchDesc + idBrief.documentDesc +
		idBrief.guide.Purpose + strings.Join(idBrief.guide.RecommendedFlow, " ")
	for _, vn := range []string{"Điều", "Khoản", "Điểm", "số ký hiệu", "Đoạn", "Vietnamese", "banhmi"} {
		if strings.Contains(idText, vn) {
			t.Errorf("ID brief leaks VN token %q", vn)
		}
	}
	for _, my := range []string{"laksa", "Section", "Subsection"} {
		if strings.Contains(idText, my) {
			t.Errorf("ID brief leaks MY token %q", my)
		}
	}
	if !strings.Contains(idText, "Pasal") || !strings.Contains(idText, "Indonesia") {
		t.Error("ID brief should reference Pasal / Indonesia")
	}

	// VN must not contain Indonesian provision vocabulary.
	vnText := vnBrief.instructions + vnBrief.searchDesc + vnBrief.guide.Purpose
	if strings.Contains(vnText, "Pasal") {
		t.Error("VN brief leaks Indonesian token 'Pasal'")
	}

	// MY must not contain Indonesian provision vocabulary.
	myText := myBrief.instructions + myBrief.searchDesc + myBrief.guide.Purpose
	if strings.Contains(myText, "Pasal") || strings.Contains(myText, "rendang") {
		t.Error("MY brief leaks Indonesian tokens")
	}
}
