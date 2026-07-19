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
		{"sg", "kaya"},
		{"kh", "amok"},
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
	for _, b := range []brief{vnBrief, myBrief, idBrief, sgBrief, thBrief, khBrief} {
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

// TestSGBriefLanguageBoundary checks the Singapore brief does not leak Vietnamese,
// Malaysian, or Indonesian provision vocabulary, and vice versa.
func TestSGBriefLanguageBoundary(t *testing.T) {
	sgText := sgBrief.instructions + sgBrief.searchDesc + sgBrief.documentDesc +
		sgBrief.guide.Purpose + strings.Join(sgBrief.guide.RecommendedFlow, " ")
	for _, vn := range []string{"Điều", "Khoản", "Điểm", "số ký hiệu", "Đoạn", "Vietnamese", "banhmi"} {
		if strings.Contains(sgText, vn) {
			t.Errorf("SG brief leaks VN token %q", vn)
		}
	}
	for _, id := range []string{"Pasal", "ayat", "huruf", "rendang", "Indonesian"} {
		if strings.Contains(sgText, id) {
			t.Errorf("SG brief leaks ID token %q", id)
		}
	}
	for _, my := range []string{"laksa", "Malaysia", "BNM", "Bank Negara"} {
		if strings.Contains(sgText, my) {
			t.Errorf("SG brief leaks MY token %q", my)
		}
	}
	if !strings.Contains(sgText, "Section") || !strings.Contains(sgText, "Singapore") {
		t.Error("SG brief should reference Section / Singapore")
	}
	if !strings.Contains(sgText, "kaya") {
		t.Error("SG brief should reference kaya product name")
	}

	// Other briefs must not contain SG tokens.
	vnText := vnBrief.instructions + vnBrief.searchDesc + vnBrief.guide.Purpose
	if strings.Contains(vnText, "kaya") || strings.Contains(vnText, "Singapore") {
		t.Error("VN brief leaks Singapore/kaya")
	}
	myText2 := myBrief.instructions + myBrief.searchDesc + myBrief.guide.Purpose
	if strings.Contains(myText2, "kaya") || strings.Contains(myText2, "Singapore") {
		t.Error("MY brief leaks Singapore/kaya")
	}
	idText := idBrief.instructions + idBrief.searchDesc + idBrief.guide.Purpose
	if strings.Contains(idText, "kaya") || strings.Contains(idText, "Singapore") {
		t.Error("ID brief leaks Singapore/kaya")
	}
}

// TestKHBriefLanguageBoundary checks the Cambodia brief does not leak vocabulary
// from other jurisdictions, and vice versa.
func TestKHBriefLanguageBoundary(t *testing.T) {
	khText := khBrief.instructions + khBrief.searchDesc + khBrief.documentDesc +
		khBrief.guide.Purpose + strings.Join(khBrief.guide.RecommendedFlow, " ")
	for _, vn := range []string{"Điều", "Khoản", "Điểm", "số ký hiệu", "Đoạn", "Vietnamese", "banhmi"} {
		if strings.Contains(khText, vn) {
			t.Errorf("KH brief leaks VN token %q", vn)
		}
	}
	for _, id := range []string{"Pasal", "ayat", "huruf", "rendang", "Indonesian"} {
		if strings.Contains(khText, id) {
			t.Errorf("KH brief leaks ID token %q", id)
		}
	}
	for _, my := range []string{"laksa", "Malaysia", "BNM", "Bank Negara"} {
		if strings.Contains(khText, my) {
			t.Errorf("KH brief leaks MY token %q", my)
		}
	}
	for _, sg := range []string{"kaya", "Singapore", "MAS", "SSO"} {
		if strings.Contains(khText, sg) {
			t.Errorf("KH brief leaks SG token %q", sg)
		}
	}
	for _, th := range []string{"tomyum", "มาตรา", "วรรค", "BOT"} {
		if strings.Contains(khText, th) {
			t.Errorf("KH brief leaks TH token %q", th)
		}
	}
	if !strings.Contains(khText, "Article") || !strings.Contains(khText, "Cambodia") {
		t.Error("KH brief should reference Article / Cambodia")
	}
	if !strings.Contains(khText, "amok") {
		t.Error("KH brief should reference amok product name")
	}

	// Other briefs must not contain KH tokens.
	vnText := vnBrief.instructions + vnBrief.searchDesc + vnBrief.guide.Purpose
	if strings.Contains(vnText, "amok") || strings.Contains(vnText, "Cambodia") {
		t.Error("VN brief leaks Cambodia/amok")
	}
	myText := myBrief.instructions + myBrief.searchDesc + myBrief.guide.Purpose
	if strings.Contains(myText, "amok") || strings.Contains(myText, "Cambodia") {
		t.Error("MY brief leaks Cambodia/amok")
	}
	idText2 := idBrief.instructions + idBrief.searchDesc + idBrief.guide.Purpose
	if strings.Contains(idText2, "amok") || strings.Contains(idText2, "Cambodia") {
		t.Error("ID brief leaks Cambodia/amok")
	}
	sgText := sgBrief.instructions + sgBrief.searchDesc + sgBrief.guide.Purpose
	if strings.Contains(sgText, "amok") || strings.Contains(sgText, "Cambodia") {
		t.Error("SG brief leaks Cambodia/amok")
	}
}
