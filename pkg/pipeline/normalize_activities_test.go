package pipeline

import (
	"context"
	"strings"
	"testing"

	dbbronze "danny.vn/banhmi/pkg/store/bronze"
	dbsilver "danny.vn/banhmi/pkg/store/silver"
)

func TestParseNormalizeSectionsStats(t *testing.T) {
	md := `
Chương I
QUY ĐỊNH CHUNG

Điều 1. Phạm vi điều chỉnh
Nội dung điều một.

1. Khoản một.

a) Điểm a.

Điều 2. Đối tượng áp dụng
Nội dung điều hai.
`
	roots, stats, warnings := parseNormalizeSections(md)

	if len(roots) != 1 {
		t.Fatalf("roots = %d, want 1", len(roots))
	}
	if stats.Total != 5 {
		t.Errorf("Total = %d, want 5", stats.Total)
	}
	if stats.Chuong != 1 {
		t.Errorf("Chuong = %d, want 1", stats.Chuong)
	}
	if stats.Dieu != 2 {
		t.Errorf("Dieu = %d, want 2", stats.Dieu)
	}
	if stats.Khoan != 1 {
		t.Errorf("Khoan = %d, want 1", stats.Khoan)
	}
	if stats.Diem != 1 {
		t.Errorf("Diem = %d, want 1", stats.Diem)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
}

func TestValidateSectionTreeWarnings(t *testing.T) {
	_, stats, warnings := parseNormalizeSections("Số: 09/2020/TT-NHNN\nCăn cứ Luật Ngân hàng Nhà nước.\n")
	if stats.Total != 0 {
		t.Fatalf("Total = %d, want 0", stats.Total)
	}
	if !hasWarning(warnings, "no_sections_parsed") {
		t.Errorf("warnings = %v, want no_sections_parsed", warnings)
	}
	if !hasWarning(warnings, "no_article_sections_parsed") {
		t.Errorf("warnings = %v, want no_article_sections_parsed", warnings)
	}

	dupes := []Section{
		{Kind: "dieu", CitationPath: "dieu-1"},
		{Kind: "dieu", CitationPath: "dieu-1"},
	}
	warnings = validateSectionTree(dupes, sectionStatsFor(dupes))
	if !hasWarning(warnings, "duplicate_citation_path:dieu-1") {
		t.Errorf("warnings = %v, want duplicate citation warning", warnings)
	}
}

func TestNormalizeValidity(t *testing.T) {
	// nil configQ → statusClassForCode falls back to the built-in defaults.
	a := &Activities{}
	ctx := context.Background()
	code, class := a.normalizeValidity(ctx, dbbronze.BronzeSourceDocument{})
	if code != "CHL" || class != "in_force" {
		t.Fatalf("default validity = %s/%s, want CHL/in_force", code, class)
	}

	raw := " HHL1P "
	code, class = a.normalizeValidity(ctx, dbbronze.BronzeSourceDocument{StatusRaw: &raw})
	if code != "HHL1P" || class != "partial" {
		t.Fatalf("partial validity = %s/%s, want HHL1P/partial", code, class)
	}

	raw = " CCHL "
	code, class = a.normalizeValidity(ctx, dbbronze.BronzeSourceDocument{StatusRaw: &raw})
	if code != "CCHL" || class != "not_yet" {
		t.Fatalf("not-yet validity = %s/%s, want CCHL/not_yet", code, class)
	}
}

func TestBindingTextQualitySkipReason(t *testing.T) {
	if got := bindingTextQualitySkipReason("**BÁO CÁO TÌNH HÌNH THỰC HIỆN CƠ CẤU LẠI THỜI HẠN TRẢ NỢ**\n\n1. Nội dung báo cáo."); got != "supplement_only_binding_text" {
		t.Fatalf("supplement skip reason = %q", got)
	}

	mojibake := "NG√ÇN H√ÄNG NH√Ä N∆Ø·ªöC VI·ªÜT NAM " +
		"NG√ÇN H√ÄNG NH√Ä N∆Ø·ªöC VI·ªÜT NAM " +
		"NG√ÇN H√ÄNG NH√Ä N∆Ø·ªöC VI·ªÜT NAM "
	got := bindingTextQualitySkipReason(mojibake)
	if got == "" || !strings.Contains(got, "mojibake") {
		t.Fatalf("mojibake skip reason = %q, want mojibake", got)
	}

	localized := strings.Repeat("Điều 1. Nội dung áp dụng cho tổ chức tín dụng và ngân hàng nước ngoài.\n", 80) +
		"NG√ÇN H√ÄNG NH√Ä N∆Ø·ªöC VI·ªÜT NAM\n" +
		strings.Repeat("Điều 2. Nội dung vẫn là tiếng Việt hợp lệ và có dấu đầy đủ.\n", 80)
	got = bindingTextQualitySkipReason(localized)
	if got != "localized_mojibake_binding_text" {
		t.Fatalf("localized mojibake skip reason = %q, want localized_mojibake_binding_text", got)
	}

	if got := bindingTextQualitySkipReason("Điều 1. Quy định chung\n1. Nội dung áp dụng cho tổ chức tín dụng, chi nhánh ngân hàng nước ngoài và các đơn vị có liên quan."); got != "" {
		t.Fatalf("good legal text skip reason = %q, want empty", got)
	}
}

func TestChooseBindingTextFallsBackAfterBadCandidate(t *testing.T) {
	bad := "**BÁO CÁO TÌNH HÌNH THỰC HIỆN CƠ CẤU LẠI THỜI HẠN TRẢ NỢ**\n\n1. Nội dung báo cáo."
	good := "Điều 1. Quy định chung\n1. Nội dung áp dụng cho tổ chức tín dụng, chi nhánh ngân hàng nước ngoài và các đơn vị có liên quan."

	txt, skipReason, warnings := chooseBindingText([]dbsilver.SilverDocumentText{
		{Authority: "gazette_borndigital", Source: "docx", Markdown: &bad, IsBinding: true},
		{Authority: "transcription_html", Source: "html", Markdown: &good, IsBinding: true},
	})

	if skipReason != "" {
		t.Fatalf("skipReason = %q, want empty", skipReason)
	}
	if txt.Authority != "transcription_html" {
		t.Fatalf("selected authority = %q, want transcription_html", txt.Authority)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "supplement_only_binding_text") {
		t.Fatalf("warnings = %v, want skipped supplement candidate", warnings)
	}
}

func TestChooseBindingTextReportsNoUsableCandidate(t *testing.T) {
	empty := " "
	_, skipReason, warnings := chooseBindingText([]dbsilver.SilverDocumentText{
		{Authority: "gazette_borndigital", Source: "docx", Markdown: &empty, IsBinding: true},
	})

	if skipReason != "no_usable_binding_text" {
		t.Fatalf("skipReason = %q, want no_usable_binding_text", skipReason)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "empty_binding_text") {
		t.Fatalf("warnings = %v, want empty candidate warning", warnings)
	}
}

func TestChooseBindingTextSkipsNeedsReviewTextWhenNoBinding(t *testing.T) {
	review := "NcArv uANc ivsn NUoc\n\nDi6u 1. C6c t6 chric tin dung phai thuc hien xac thuc."

	_, skipReason, warnings := chooseBindingText([]dbsilver.SilverDocumentText{
		{
			Authority:   "gazette_borndigital",
			Source:      "sbv_hanoi",
			Markdown:    &review,
			IsBinding:   false,
			NeedsReview: true,
		},
	})

	if skipReason != "no_binding_text" {
		t.Fatalf("skipReason = %q, want no_binding_text", skipReason)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "skipped_non_binding_text") {
		t.Fatalf("warnings = %v, want skipped non-binding warning", warnings)
	}
}

func TestChooseBindingTextDoesNotUseCleanNonBindingText(t *testing.T) {
	review := "NcArv uANc ivsn NUoc\n\nDi6u 1. C6c t6 chric tin dung phai thuc hien xac thuc."
	transcription := "Điều 1. Các tổ chức tín dụng, chi nhánh ngân hàng nước ngoài, tổ chức cung ứng dịch vụ trung gian thanh toán triển khai giải pháp an toàn, bảo mật trong thanh toán trực tuyến."

	_, skipReason, warnings := chooseBindingText([]dbsilver.SilverDocumentText{
		{
			Authority:   "gazette_borndigital",
			Source:      "sbv_hanoi",
			Markdown:    &review,
			IsBinding:   false,
			NeedsReview: true,
		},
		{
			Authority:   "transcription_html",
			Source:      "official_html",
			Markdown:    &transcription,
			IsBinding:   false,
			NeedsReview: false,
		},
	})

	if skipReason != "no_binding_text" {
		t.Fatalf("skipReason = %q, want no_binding_text", skipReason)
	}
	if len(warnings) != 2 {
		t.Fatalf("warnings = %v, want two skipped non-binding warnings", warnings)
	}
}

func TestChooseBindingTextStillSkipsEmptyNeedsReviewText(t *testing.T) {
	empty := " "

	_, skipReason, warnings := chooseBindingText([]dbsilver.SilverDocumentText{
		{
			Authority:   "gazette_borndigital",
			Source:      "sbv_hanoi",
			Markdown:    &empty,
			IsBinding:   false,
			NeedsReview: true,
		},
	})

	if skipReason != "no_binding_text" {
		t.Fatalf("skipReason = %q, want no_binding_text", skipReason)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "skipped_non_binding_text") {
		t.Fatalf("warnings = %v, want skipped non-binding warning", warnings)
	}
}

func hasWarning(warnings []string, want string) bool {
	for _, warning := range warnings {
		if warning == want {
			return true
		}
	}
	return false
}
