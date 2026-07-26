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
		TextNormalizer:            "vn-fold",
		ParagraphLabel:            "Đoạn",
		EffectiveDateLabel:        "Có hiệu lực",
		ArticleCitationPrefix:     "điều ",
		SubArticleCitationPrefix:  "khoản ",
		StructureParser:           jurisdiction.ParserVNMarkdown,
		UnknownValidityInForce:    true,
		LexicalRouterBoost:        true,
		IdentifierScopedRetrieval: true,
		ScopeSeedFile:             "scope_term.csv",
		GoldenFile:                "deploy/eval/golden.json",
		EvalArticleKeyword:        "điều",
		EvalClauseKeyword:         "khoản",
		EvalPointKeyword:          "điểm",
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
		TextNormalizer:           "vn-fold",
		ParagraphLabel:           "Paragraph",
		EffectiveDateLabel:       "Effective",
		ArticleCitationPrefix:    "section ",
		SubArticleCitationPrefix: "subsection ",
		StructureParser:          jurisdiction.ParserMYAct,
		UnknownValidityInForce:   true,
		ScopeSeedFile:            "scope_term_my.csv",
		GoldenFile:               "deploy/eval/golden_my.json",
		EvalArticleKeyword:       "section",
		EvalClauseKeyword:        "",
		EvalPointKeyword:         "",
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
		TextNormalizer:           "vn-fold",
		ParagraphLabel:           "Alinea",
		EffectiveDateLabel:       "Berlaku",
		ArticleCitationPrefix:    "pasal ",
		SubArticleCitationPrefix: "ayat ",
		StructureParser:          jurisdiction.ParserIDUU,
		UnknownValidityInForce:   true,
		DocRefCanonicalizer:      jurisdiction.RefCanonIDForms,
		ScopeSeedFile:            "scope_term_id.csv",
		GoldenFile:               "deploy/eval/golden_id.json",
		EvalArticleKeyword:       "pasal",
		EvalClauseKeyword:        "ayat",
		EvalPointKeyword:         "huruf",
	}
	if d != want {
		t.Errorf("Lookup(id) = %+v, want %+v", d, want)
	}
}

func TestLookupSG(t *testing.T) {
	d, ok := jurisdiction.Lookup("sg")
	if !ok {
		t.Fatal("Lookup(sg) not found")
	}
	want := jurisdiction.Descriptor{
		Code:                     "sg",
		DBName:                   "kaya",
		OCRLanguages:             "en",
		TextNormalizer:           "vn-fold",
		ParagraphLabel:           "Paragraph",
		EffectiveDateLabel:       "Effective",
		ArticleCitationPrefix:    "section ",
		SubArticleCitationPrefix: "subsection ",
		StructureParser:          jurisdiction.ParserSGAct,
		UnknownValidityInForce:   true,
		ScopeSeedFile:            "scope_term_sg.csv",
		GoldenFile:               "deploy/eval/golden_sg.json",
		EvalArticleKeyword:       "section",
		EvalClauseKeyword:        "",
		EvalPointKeyword:         "",
	}
	if d != want {
		t.Errorf("Lookup(sg) = %+v, want %+v", d, want)
	}
}

func TestLookupTH(t *testing.T) {
	d, ok := jurisdiction.Lookup("th")
	if !ok {
		t.Fatal("Lookup(th) not found")
	}
	want := jurisdiction.Descriptor{
		Code:                     "th",
		DBName:                   "tomyum",
		OCRLanguages:             "th",
		TextNormalizer:           "th",
		ParagraphLabel:           "วรรค",
		EffectiveDateLabel:       "มีผลบังคับใช้",
		ArticleCitationPrefix:    "มาตรา ",
		SubArticleCitationPrefix: "วรรค ",
		StructureParser:          jurisdiction.ParserTHAct,
		UnknownValidityInForce:   true,
		ScopeSeedFile:            "scope_term_th.csv",
		GoldenFile:               "deploy/eval/golden_th.json",
		EvalArticleKeyword:       "มาตรา",
		EvalClauseKeyword:        "วรรค",
		EvalPointKeyword:         "ข้อ",
	}
	if d != want {
		t.Errorf("Lookup(th) = %+v, want %+v", d, want)
	}
}

func TestLookupKH(t *testing.T) {
	d, ok := jurisdiction.Lookup("kh")
	if !ok {
		t.Fatal("Lookup(kh) not found")
	}
	want := jurisdiction.Descriptor{
		Code:                     "kh",
		DBName:                   "amok",
		OCRLanguages:             "en",
		TextNormalizer:           "vn-fold",
		ParagraphLabel:           "Paragraph",
		EffectiveDateLabel:       "Effective",
		ArticleCitationPrefix:    "article ",
		SubArticleCitationPrefix: "clause ",
		StructureParser:          jurisdiction.ParserKHAct,
		UnknownValidityInForce:   true,
		ScopeSeedFile:            "scope_term_kh.csv",
		GoldenFile:               "deploy/eval/golden_kh.json",
		EvalArticleKeyword:       "article",
		EvalClauseKeyword:        "",
		EvalPointKeyword:         "",
	}
	if d != want {
		t.Errorf("Lookup(kh) = %+v, want %+v", d, want)
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
	if got := jurisdiction.For("sg").Code; got != "sg" {
		t.Errorf("For(sg).Code = %q, want sg", got)
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
			d.StructureParser == "" || d.TextNormalizer == "" || d.ScopeSeedFile == "" || d.GoldenFile == "" ||
			d.EvalArticleKeyword == "" {
			t.Errorf("descriptor %q has unfilled required fields: %+v", d.Code, d)
		}
	}
	if !seen["my"] {
		t.Error("All() is missing the live my jurisdiction")
	}
	if !seen["id"] {
		t.Error("All() is missing the id jurisdiction")
	}
	if !seen["sg"] {
		t.Error("All() is missing the sg jurisdiction")
	}
	if !seen["th"] {
		t.Error("All() is missing the th jurisdiction")
	}
	if !seen["kh"] {
		t.Error("All() is missing the kh jurisdiction")
	}
}
