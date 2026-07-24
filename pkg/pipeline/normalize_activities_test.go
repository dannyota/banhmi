package pipeline

import (
	"context"
	"strings"
	"testing"

	"danny.vn/banhmi/pkg/base/jurisdiction"
	"danny.vn/banhmi/pkg/extract"
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
	roots, stats, warnings := parseNormalizeSections(jurisdiction.ParserVNMarkdown, md)

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
	_, stats, warnings := parseNormalizeSections(jurisdiction.ParserVNMarkdown, "Số: 09/2020/TT-NHNN\nCăn cứ Luật Ngân hàng Nhà nước.\n")
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
	ctx := context.Background()

	t.Run("no_status_no_flag", func(t *testing.T) {
		// Zero-value jur (UnknownValidityInForce=false): unknown stays unknown.
		a := &Activities{}
		code, class := a.normalizeValidity(ctx, dbbronze.BronzeSourceDocument{})
		if code != "" || class != "unknown" {
			t.Fatalf("validity = %s/%s, want /unknown", code, class)
		}
	})

	t.Run("no_status_unknown_defaults_in_force", func(t *testing.T) {
		// VN descriptor: vanban/sbv_hanoi emit no status; UnknownValidityInForce
		// promotes unknown → in_force so these docs enter the current-law pass.
		a := &Activities{jur: jurisdiction.For("vn")}
		code, class := a.normalizeValidity(ctx, dbbronze.BronzeSourceDocument{})
		if code != "" || class != "in_force" {
			t.Fatalf("validity = %s/%s, want /in_force", code, class)
		}
	})

	t.Run("known_status_unaffected_by_flag", func(t *testing.T) {
		// A vbpl-sourced status must stay as-is even with UnknownValidityInForce on.
		a := &Activities{jur: jurisdiction.For("vn")}

		raw := "HHL"
		code, class := a.normalizeValidity(ctx, dbbronze.BronzeSourceDocument{StatusRaw: &raw})
		if code != "HHL" || class != "expired" {
			t.Fatalf("expired validity = %s/%s, want HHL/expired", code, class)
		}

		raw = " HHL1P "
		code, class = a.normalizeValidity(ctx, dbbronze.BronzeSourceDocument{StatusRaw: &raw})
		if code != "HHL1P" || class != "partial" {
			t.Fatalf("partial validity = %s/%s, want HHL1P/partial", code, class)
		}

		raw = " CCHL "
		code, class = a.normalizeValidity(ctx, dbbronze.BronzeSourceDocument{StatusRaw: &raw})
		if code != "CCHL" || class != "not_yet" {
			t.Fatalf("not-yet validity = %s/%s, want CCHL/not_yet", code, class)
		}
	})
}

// vnGate is the VN content gate: DefaultGate is language-neutral, so the
// Vietnamese mojibake markers come from the jurisdiction descriptor (the single
// source of truth) exactly as the pipeline's qualityGate() builds it.
func vnGate() extract.GateConfig {
	g := extract.DefaultGate()
	g.MojibakeMarkers = jurisdiction.For("vn").MojibakeMarkers
	return g
}

func TestBindingTextQualitySkipReason(t *testing.T) {
	if got := bindingTextQualitySkipReason(vnGate(), "**BÁO CÁO TÌNH HÌNH THỰC HIỆN CƠ CẤU LẠI THỜI HẠN TRẢ NỢ**\n\n1. Nội dung báo cáo."); got != "supplement_only_binding_text" {
		t.Fatalf("supplement skip reason = %q", got)
	}

	mojibake := "NG√ÇN H√ÄNG NH√Ä N∆Ø·ªöC VI·ªÜT NAM " +
		"NG√ÇN H√ÄNG NH√Ä N∆Ø·ªöC VI·ªÜT NAM " +
		"NG√ÇN H√ÄNG NH√Ä N∆Ø·ªöC VI·ªÜT NAM "
	got := bindingTextQualitySkipReason(vnGate(), mojibake)
	if got == "" || !strings.Contains(got, "mojibake") {
		t.Fatalf("mojibake skip reason = %q, want mojibake", got)
	}

	localized := strings.Repeat("Điều 1. Nội dung áp dụng cho tổ chức tín dụng và ngân hàng nước ngoài.\n", 80) +
		"NG√ÇN H√ÄNG NH√Ä N∆Ø·ªöC VI·ªÜT NAM\n" +
		strings.Repeat("Điều 2. Nội dung vẫn là tiếng Việt hợp lệ và có dấu đầy đủ.\n", 80)
	got = bindingTextQualitySkipReason(vnGate(), localized)
	if got != "localized_mojibake_binding_text" {
		t.Fatalf("localized mojibake skip reason = %q, want localized_mojibake_binding_text", got)
	}

	cyrillic := strings.Repeat("Дҗiб»Ғu 1. Hб»“ sЖЎ Д‘б»Ғ nghб»Ӣ cбәҘp GiбәҘy chб»©ng nhбәӯn dб»Ҝ liб»Үu cГЎ nhГўn.\n", 20)
	if got := bindingTextQualitySkipReason(vnGate(), cyrillic); got != "cyrillic_mojibake_binding_text" {
		t.Fatalf("cyrillic mojibake skip reason = %q, want cyrillic_mojibake_binding_text", got)
	}

	if got := bindingTextQualitySkipReason(vnGate(), "Điều 1. Quy định chung\n1. Nội dung áp dụng cho tổ chức tín dụng, chi nhánh ngân hàng nước ngoài và các đơn vị có liên quan."); got != "" {
		t.Fatalf("good legal text skip reason = %q, want empty", got)
	}
}

func TestChooseBindingTextFallsBackAfterBadCandidate(t *testing.T) {
	bad := "**BÁO CÁO TÌNH HÌNH THỰC HIỆN CƠ CẤU LẠI THỜI HẠN TRẢ NỢ**\n\n1. Nội dung báo cáo."
	good := "Điều 1. Quy định chung\n1. Nội dung áp dụng cho tổ chức tín dụng, chi nhánh ngân hàng nước ngoài và các đơn vị có liên quan."

	txt, skipReason, warnings := chooseBindingText(extract.DefaultGate(), []dbsilver.SilverDocumentText{
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
	_, skipReason, warnings := chooseBindingText(extract.DefaultGate(), []dbsilver.SilverDocumentText{
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

	_, skipReason, warnings := chooseBindingText(extract.DefaultGate(), []dbsilver.SilverDocumentText{
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

	_, skipReason, warnings := chooseBindingText(extract.DefaultGate(), []dbsilver.SilverDocumentText{
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

func TestChooseNonBindingFallbackPicksReadableTranscription(t *testing.T) {
	garbled := "NcArv uANc ivsn NUoc\n\nDi6u 1. C6c t6 chric tin dung."
	ocr := "Điều 1. Các tổ chức tín dụng, chi nhánh ngân hàng nước ngoài triển khai các giải pháp an toàn, bảo mật trong thanh toán trực tuyến và thanh toán thẻ ngân hàng theo quy định."

	fb := chooseNonBindingFallback(extract.DefaultGate(), []dbsilver.SilverDocumentText{
		// Gate-failed born-digital extraction (the reason OCR ran) is skipped
		// by the shared quality bar even though its authority ranks higher.
		{Authority: "gazette_borndigital", Source: "congbao", Markdown: &garbled, IsBinding: false, NeedsReview: true},
		{Authority: "ocr_extractive", Source: "congbao", Markdown: &ocr, IsBinding: false, NeedsReview: true},
	})

	if fb == nil {
		t.Fatal("chooseNonBindingFallback = nil, want the OCR transcription")
	}
	if fb.Authority != "ocr_extractive" {
		t.Fatalf("selected authority = %q, want ocr_extractive", fb.Authority)
	}
}

func TestChooseNonBindingFallbackSkipsDistrustedScanLayer(t *testing.T) {
	// A scan's embedded OCR layer reads as clean prose (passes every text-level
	// check) but was flagged needs_review by the file-level scan gate. It must
	// lose to the OcrAll transcription regardless of authority order.
	layer := "Pasal 69 Anggota Dewan Komisioner hanya dapat diberhentikan oleh Presiden apabila berhalangan tetap, masa jabatannya berakhir, mengundurkan diri, atau tidak lagi memenuhi persyaratan sebagai anggota."
	ocr := "Pasal 69 Anggota Dewan Komisioner hanya dapat diberhentikan oleh Presiden apabila berhalangan tetap, masa jabatannya berakhir, mengundurkan diri, atau tidak lagi memenuhi persyaratan keanggotaan Dewan Komisioner."

	fb := chooseNonBindingFallback(extract.DefaultGate(), []dbsilver.SilverDocumentText{
		{Authority: "gazette_borndigital", Source: "bpk", Markdown: &layer, IsBinding: false, NeedsReview: true},
		{Authority: "ocr_extractive", Source: "bpk", Markdown: &ocr, IsBinding: false, NeedsReview: false},
	})

	if fb == nil {
		t.Fatal("chooseNonBindingFallback = nil, want the OCR transcription")
	}
	if fb.Authority != "ocr_extractive" {
		t.Fatalf("selected authority = %q, want ocr_extractive", fb.Authority)
	}
}

func TestChooseNonBindingFallbackNeverPicksBindingOrEmpty(t *testing.T) {
	binding := "Điều 1. Quy định chung về an toàn hệ thống thông tin trong hoạt động ngân hàng."
	empty := "  "

	fb := chooseNonBindingFallback(extract.DefaultGate(), []dbsilver.SilverDocumentText{
		{Authority: "transcription_html", Source: "vbpl", Markdown: &binding, IsBinding: true},
		{Authority: "ocr_extractive", Source: "congbao", Markdown: &empty, IsBinding: false},
	})

	if fb != nil {
		t.Fatalf("chooseNonBindingFallback = %+v, want nil", fb)
	}
}

func TestChooseBindingTextStillSkipsEmptyNeedsReviewText(t *testing.T) {
	empty := " "

	_, skipReason, warnings := chooseBindingText(extract.DefaultGate(), []dbsilver.SilverDocumentText{
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

// TestParseSectionsRecoversAppendixAfterSignature pins the shape that loses
// appendices on tree-normalized docs (04/2025/TT-NHNN): a bare sentence-case
// "Phụ lục" label after the signature block, followed by table rows. The VN
// parser must emit a root-level phuluc section carrying that content.
func TestParseSectionsRecoversAppendixAfterSignature(t *testing.T) {
	md := `Điều 1. Phạm vi điều chỉnh
Thông tư này quy định thời hạn lưu trữ hồ sơ, tài liệu ngành Ngân hàng.
Điều 2. Hiệu lực thi hành
Thông tư này có hiệu lực từ ngày 01 tháng 7 năm 2025.
Nơi nhận:
- Ban lãnh đạo NHNN;
- Công báo;
THỐNG ĐỐC

Phụ lục
BẢNG THỜI HẠN LƯU TRỮ HỒ SƠ, TÀI LIỆU
NGÀNH NGÂN HÀNG
STT
Tên nhóm hồ sơ, tài liệu
Thời hạn lưu trữ
Hồ sơ xây dựng chiến lược phát triển ngành Ngân hàng.
Vĩnh viễn`
	roots := ParseSections(md)
	var phuLuc *Section
	for i := range roots {
		if roots[i].Kind == "phuluc" {
			phuLuc = &roots[i]
		}
	}
	if phuLuc == nil {
		t.Fatalf("no phuluc root parsed; roots kinds: %v", sectionKinds(roots))
	}
	joined := phuLuc.Content
	for _, c := range phuLuc.Children {
		joined += "\n" + c.Content
	}
	if !strings.Contains(joined, "Vĩnh viễn") {
		t.Fatalf("appendix content lost the retention table, got: %q", joined)
	}
}

func sectionKinds(roots []Section) []string {
	kinds := make([]string, len(roots))
	for i, r := range roots {
		kinds[i] = r.Kind
	}
	return kinds
}

// TestMergeAppendixRoots pins the tree-supplementation merge: appendices are
// appended with continued ordinals, and a tree that already carries a phuluc
// root keeps it and drops the text-parsed duplicates.
func TestMergeAppendixRoots(t *testing.T) {
	tree := []Section{{Kind: "dieu", Ordinal: 1}, {Kind: "dieu", Ordinal: 2}}
	apps := []Section{{Kind: "phuluc", Ordinal: 1, Label: "Phụ lục"}}

	merged := mergeAppendixRoots(tree, apps)
	if len(merged) != 3 || merged[2].Kind != "phuluc" || merged[2].Ordinal != 3 {
		t.Fatalf("merge failed: %+v", merged)
	}

	treeWithPl := []Section{{Kind: "dieu", Ordinal: 1}, {Kind: "phuluc", Ordinal: 2, Label: "Phụ lục 01"}}
	kept := mergeAppendixRoots(treeWithPl, apps)
	if len(kept) != 2 || kept[1].Label != "Phụ lục 01" {
		t.Fatalf("tree phuluc must win, got: %+v", kept)
	}
}
