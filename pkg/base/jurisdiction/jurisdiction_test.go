package jurisdiction_test

import (
	"testing"

	"danny.vn/banhmi/pkg/base/jurisdiction"
)

func TestLookupVN(t *testing.T) {
	d, ok := jurisdiction.Lookup("vn")
	if !ok {
		t.Fatal("Lookup(vn) not found")
	}
	want := jurisdiction.Descriptor{
		Code:                      "vn",
		DBName:                    "banhmi",
		OCRLanguages:              "",
		DiacriticDensityGate:      true,
		HNSWCandidateMultiplier:   -1,
		MojibakeMarkers:           "√∆·ªƒ∫≠‚ÄØ",
		ParagraphLabel:            "Đoạn",
		EffectiveDateLabel:        "Có hiệu lực",
		ArticleCitationPrefix:     "điều ",
		SubArticleCitationPrefix:  "khoản ",
		StructureParser:           jurisdiction.ParserVNMarkdown,
		LexicalRouterBoost:        true,
		IdentifierScopedRetrieval: true,
		ScopeSeedFile:             "scope_term.csv",
		GoldenFile:                "deploy/eval/golden.json",
	}
	if d != want {
		t.Errorf("Lookup(vn) = %+v, want %+v", d, want)
	}
}

func TestLookupMY(t *testing.T) {
	d, ok := jurisdiction.Lookup("my")
	if !ok {
		t.Fatal("Lookup(my) not found")
	}
	want := jurisdiction.Descriptor{
		Code:                     "my",
		DBName:                   "laksa",
		OCRLanguages:             "en",
		ParagraphLabel:           "Paragraph",
		EffectiveDateLabel:       "Effective",
		ArticleCitationPrefix:    "section ",
		SubArticleCitationPrefix: "subsection ",
		StructureParser:          jurisdiction.ParserMYAct,
		UnknownValidityInForce:   true,
		ScopeSeedFile:            "scope_term_my.csv",
		GoldenFile:               "deploy/eval/golden_my.json",
	}
	if d != want {
		t.Errorf("Lookup(my) = %+v, want %+v", d, want)
	}
}

func TestLookupID(t *testing.T) {
	d, ok := jurisdiction.Lookup("id")
	if !ok {
		t.Fatal("Lookup(id) not found")
	}
	want := jurisdiction.Descriptor{
		Code:                     "id",
		DBName:                   "rendang",
		OCRLanguages:             "id",
		ParagraphLabel:           "Alinea",
		EffectiveDateLabel:       "Berlaku",
		ArticleCitationPrefix:    "pasal ",
		SubArticleCitationPrefix: "ayat ",
		StructureParser:          jurisdiction.ParserIDUU,
		UnknownValidityInForce:   true,
		ScopeSeedFile:            "scope_term_id.csv",
		GoldenFile:               "deploy/eval/golden_id.json",
	}
	if d != want {
		t.Errorf("Lookup(id) = %+v, want %+v", d, want)
	}
}

func TestLookupNormalizesCode(t *testing.T) {
	cases := []struct {
		code string
		want string
		ok   bool
	}{
		{"", "vn", true},      // absent env/config resolves to the compiled fallback
		{"  MY ", "my", true}, // trims and lower-cases
		{"VN", "vn", true},
		{"xx", "", false}, // unknown codes fail fast
	}
	for _, c := range cases {
		d, ok := jurisdiction.Lookup(c.code)
		if ok != c.ok || d.Code != c.want {
			t.Errorf("Lookup(%q) = (%q, %v), want (%q, %v)", c.code, d.Code, ok, c.want, c.ok)
		}
	}
}

func TestForFallsBackToVN(t *testing.T) {
	for _, code := range []string{"", "xx", "zz"} {
		if got := jurisdiction.For(code).Code; got != "vn" {
			t.Errorf("For(%q).Code = %q, want vn", code, got)
		}
	}
	if got := jurisdiction.For("my").Code; got != "my" {
		t.Errorf("For(my).Code = %q, want my", got)
	}
	if got := jurisdiction.For("id").Code; got != "id" {
		t.Errorf("For(id).Code = %q, want id", got)
	}
}

// TestAllComplete guards descriptor completeness: every registered jurisdiction
// must fill the fields the shared pipeline resolves at runtime, and codes must
// be unique with VN (the compiled fallback) first.
func TestAllComplete(t *testing.T) {
	all := jurisdiction.All()
	if len(all) == 0 || all[0].Code != jurisdiction.DefaultCode {
		t.Fatalf("All() must lead with the %q fallback, got %+v", jurisdiction.DefaultCode, all)
	}
	seen := map[string]bool{}
	for _, d := range all {
		if seen[d.Code] {
			t.Errorf("duplicate jurisdiction code %q", d.Code)
		}
		seen[d.Code] = true
		if d.Code == "" || d.DBName == "" || d.ParagraphLabel == "" || d.EffectiveDateLabel == "" ||
			d.ArticleCitationPrefix == "" || d.SubArticleCitationPrefix == "" ||
			d.StructureParser == "" || d.ScopeSeedFile == "" || d.GoldenFile == "" {
			t.Errorf("descriptor %q has unfilled required fields: %+v", d.Code, d)
		}
	}
	if !seen["my"] {
		t.Error("All() is missing the live my jurisdiction")
	}
	if !seen["id"] {
		t.Error("All() is missing the id jurisdiction")
	}
}
