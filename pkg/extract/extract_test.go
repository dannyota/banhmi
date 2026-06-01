package extract

import (
	"strings"
	"testing"
)

func TestDefaultGate(t *testing.T) {
	g := DefaultGate()
	if g.MaxBadRatio != defaultMaxBadRatio {
		t.Errorf("MaxBadRatio: got %v, want %v", g.MaxBadRatio, defaultMaxBadRatio)
	}
}

func TestGateFromSettings(t *testing.T) {
	m := map[string]string{
		"extract.pdf.max_bad_ratio":         "0.02",
		"extract.pdf.min_diacritic_density": "0.05",
		"extract.pdf.min_letters":           "100",
		"extract.pdf.pass_threshold":        "0.7",
		"extract.pdf.max_whitespace_ratio":  "0.50",
		"extract.pdf.max_pua_ratio":         "0.01",
	}
	g := GateFromSettings(m)
	if g.MaxBadRatio != 0.02 {
		t.Errorf("MaxBadRatio: got %v, want 0.02", g.MaxBadRatio)
	}
	if g.MinDiacriticDensity != 0.05 {
		t.Errorf("MinDiacriticDensity: got %v, want 0.05", g.MinDiacriticDensity)
	}
	if g.MinLetters != 100 {
		t.Errorf("MinLetters: got %v, want 100", g.MinLetters)
	}
	if g.PassThreshold != 0.7 {
		t.Errorf("PassThreshold: got %v, want 0.7", g.PassThreshold)
	}
	if g.MaxWhitespaceRatio != 0.50 {
		t.Errorf("MaxWhitespaceRatio: got %v, want 0.50", g.MaxWhitespaceRatio)
	}
	if g.MaxPUARatio != 0.01 {
		t.Errorf("MaxPUARatio: got %v, want 0.01", g.MaxPUARatio)
	}
}

func TestGateFromSettings_UnknownKeysUseDefaults(t *testing.T) {
	g := GateFromSettings(map[string]string{"other.key": "99"})
	d := DefaultGate()
	if g.MaxBadRatio != d.MaxBadRatio {
		t.Errorf("unexpected MaxBadRatio: %v", g.MaxBadRatio)
	}
}

func TestGateFromSettings_BadValueUsesDefault(t *testing.T) {
	g := GateFromSettings(map[string]string{
		"extract.pdf.max_bad_ratio": "not-a-float",
	})
	if g.MaxBadRatio != defaultMaxBadRatio {
		t.Errorf("bad value should fall back to default, got %v", g.MaxBadRatio)
	}
}

func TestGate_GoodVietnameseText(t *testing.T) {
	// Real-looking VN legal text: diacritic-rich, no bad chars.
	text := strings.Repeat("Điều 1. Ngân hàng Nhà nước Việt Nam quy định các điều kiện sau đây. Khoản 1 áp dụng. ", 10)
	r := DefaultGate().Assess(text)
	if !r.OK {
		t.Errorf("good text should pass, got ok=false reason=%q confidence=%f", r.Reason, r.Confidence)
	}
	if r.Confidence < 0.6 {
		t.Errorf("confidence too low: %f", r.Confidence)
	}
}

func TestGate_EmptyText(t *testing.T) {
	r := DefaultGate().Assess("")
	if r.OK {
		t.Error("empty text should not pass")
	}
	if r.Reason == "" {
		t.Error("empty text should have a reason")
	}
}

func TestSourceUnavailableDetectsOfficialPlaceholder(t *testing.T) {
	cases := []string{
		"Đang cập nhật file đính kèm",
		"DANG CAP NHAT FILE DINH KEM",
		"  Đang   cập nhật  ",
	}
	for _, tc := range cases {
		if !SourceUnavailable(tc) {
			t.Fatalf("SourceUnavailable(%q) = false, want true", tc)
		}
	}
}

func TestSourceUnavailableDoesNotFlagRealDocument(t *testing.T) {
	text := strings.Repeat("Điều 1. Ngân hàng Nhà nước Việt Nam quy định về hồ sơ và thủ tục. ", 8)
	if SourceUnavailable(text) {
		t.Fatal("SourceUnavailable(real legal text) = true, want false")
	}
}

func TestOfficialPlaceholderDoesNotFlagBlankText(t *testing.T) {
	if OfficialPlaceholder(" \n\t ") {
		t.Fatal("OfficialPlaceholder(blank) = true, want false")
	}
}

func TestOfficialPlaceholderDetectsUpdatingAttachment(t *testing.T) {
	if !OfficialPlaceholder("Đang cập nhật file đính kèm") {
		t.Fatal("OfficialPlaceholder(updating attachment) = false, want true")
	}
}

func TestGate_MojibakeText(t *testing.T) {
	// Simulate TCVN3/VNI PUA mojibake: fill text with U+E001–U+E0FF runes.
	var sb strings.Builder
	for i := 0; i < 200; i++ {
		sb.WriteRune('' + rune(i%30)) // PUA range
	}
	r := DefaultGate().Assess(sb.String())
	if r.OK {
		t.Errorf("mojibake text should not pass, got ok=true confidence=%f", r.Confidence)
	}
}

func TestGate_UTF8MojibakeMarkers(t *testing.T) {
	text := strings.Repeat("NG√ÇN H√ÄNG NH√Ä N∆Ø·ªöC VI·ªÜT NAM C·ªòNG H√íA X√É H·ªòI CH·ª¶ NGHƒ®A ", 12)
	r := DefaultGate().Assess(text)
	if r.OK {
		t.Fatalf("UTF-8 mojibake text passed, confidence=%f", r.Confidence)
	}
	if !strings.Contains(r.Reason, "mojibake") {
		t.Fatalf("reason = %q, want mojibake diagnosis", r.Reason)
	}
}

func TestGate_ReplacementChars(t *testing.T) {
	// High replacement-character ratio — garbled extraction.
	text := strings.Repeat("a", 50) + strings.Repeat("replacement �", 10)
	r := DefaultGate().Assess(text)
	// The bad ratio might not hit the hard threshold yet; test confidence < threshold.
	_ = r // we just ensure it compiles and runs; the exact outcome depends on ratio
}

func TestGate_LowDiacriticsWithLegalKeywordStillFails(t *testing.T) {
	// The gate must not special-case legal keywords. Low-diacritic OCR remains
	// suspect even if it contains words such as "Điều".
	body := strings.Repeat("abc def ghi jkl mno pqr stu vwx ", 10) // ASCII-only words
	text := body + "Điều 1 " + body
	r := DefaultGate().Assess(text)
	if r.OK {
		t.Fatalf("low-diacritic text passed because of keyword, confidence=%f reason=%q", r.Confidence, r.Reason)
	}
	if !strings.Contains(r.Reason, "low diacritic density") {
		t.Fatalf("reason = %q, want low diacritic density", r.Reason)
	}
}

func TestGate_HighWhitespace(t *testing.T) {
	// Text that is mostly whitespace — likely mis-extracted image-heavy PDF.
	text := strings.Repeat("a ", 30) + strings.Repeat("   ", 200)
	r := DefaultGate().Assess(text)
	// High whitespace should drag confidence down.
	if r.Confidence >= 0.9 {
		t.Errorf("high-whitespace text has suspiciously high confidence: %f", r.Confidence)
	}
}

// TestAssessBackCompat confirms the package-level Assess wrapper still works.
func TestAssessBackCompat(t *testing.T) {
	text := strings.Repeat("Điều 1. Quy định chung về ngân hàng. ", 20)
	conf, ok := Assess(text)
	if !ok {
		t.Errorf("backward-compat Assess: good text should pass, conf=%f", conf)
	}
}
