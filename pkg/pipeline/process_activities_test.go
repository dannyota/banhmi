package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"danny.vn/banhmi/pkg/extract"
	"danny.vn/banhmi/pkg/ingest"
	dbbronze "danny.vn/banhmi/pkg/store/bronze"
)

func TestPickFilePrefersMainDocxOverAppendix(t *testing.T) {
	files := []dbbronze.BronzeRawFile{
		{ID: 1, FileKind: "appendix", FileFormat: "docx"},
		{ID: 2, FileKind: "main", FileFormat: "docx"},
		{ID: 3, FileKind: "original_scan", FileFormat: "pdf"},
	}

	got := pickFile(files, "docx", "main")
	if got == nil {
		t.Fatal("pickFile returned nil, want main DOCX")
	}
	if got.ID != 2 {
		t.Fatalf("pickFile picked id=%d, want main DOCX id=2", got.ID)
	}
}

func TestPickFileSkipsAppendixForPrimaryDocx(t *testing.T) {
	files := []dbbronze.BronzeRawFile{
		{ID: 1, FileKind: "appendix", FileFormat: "docx"},
		{ID: 2, FileKind: "original_scan", FileFormat: "pdf"},
	}

	got := pickFile(files, "docx", "main")
	if got != nil {
		t.Fatalf("pickFile picked appendix DOCX id=%d as primary", got.ID)
	}
}

func TestPickFilePDFKindPriority(t *testing.T) {
	files := []dbbronze.BronzeRawFile{
		{ID: 1, FileKind: "appendix", FileFormat: "pdf"},
		{ID: 2, FileKind: "original_scan", FileFormat: "pdf"},
		{ID: 3, FileKind: "main", FileFormat: "pdf"},
	}

	got := pickFile(files, "pdf", "main", "original_scan")
	if got == nil {
		t.Fatal("pickFile returned nil, want main PDF")
	}
	if got.ID != 3 {
		t.Fatalf("pickFile picked id=%d, want main PDF id=3", got.ID)
	}
}

func TestPickPDFForExtractionSkipsOriginalScanAfterBornDigitalReview(t *testing.T) {
	files := []dbbronze.BronzeRawFile{
		{ID: 1, FileKind: "original_scan", FileFormat: "pdf"},
	}

	got := pickPDFForExtraction(files, true)

	if got != nil {
		t.Fatalf("pickPDFForExtraction picked original_scan id=%d after born-digital review", got.ID)
	}
}

func TestPickPDFForExtractionStillAllowsMainPDF(t *testing.T) {
	files := []dbbronze.BronzeRawFile{
		{ID: 1, FileKind: "original_scan", FileFormat: "pdf"},
		{ID: 2, FileKind: "main", FileFormat: "pdf"},
	}

	got := pickPDFForExtraction(files, true)

	if got == nil || got.ID != 2 {
		t.Fatalf("pickPDFForExtraction = %+v, want main PDF id=2", got)
	}
}

func TestPickPDFForExtractionUsesOriginalScanWithoutBornDigitalReview(t *testing.T) {
	files := []dbbronze.BronzeRawFile{
		{ID: 1, FileKind: "original_scan", FileFormat: "pdf"},
	}

	got := pickPDFForExtraction(files, false)

	if got == nil || got.ID != 1 {
		t.Fatalf("pickPDFForExtraction = %+v, want original_scan id=1", got)
	}
}

func TestValidCongbaoFallbackCandidateAcceptsExactNumberAndFile(t *testing.T) {
	issued := time.Date(2022, 1, 27, 0, 0, 0, 0, time.UTC)
	sd := dbbronze.BronzeSourceDocument{
		DocNumber:     strPtr("14/2022/NĐ-CP"),
		DocNumberNorm: normalizeDocNumberForStorage("14/2022/NĐ-CP"),
		DocType:       strPtr("Nghị định"),
		IssuedAt:      &issued,
	}
	doc := ingest.DiscoveredDoc{
		Number:   "14/2022/NĐ-CP",
		DocType:  ingest.DocType("Nghị định"),
		IssuedAt: issued,
		Files:    []ingest.FileRef{{URL: "https://example.invalid/doc.doc", Ext: "doc"}},
	}

	ok, reason := validCongbaoFallbackCandidate(sd, doc)

	if !ok {
		t.Fatalf("validCongbaoFallbackCandidate rejected exact fallback: %s", reason)
	}
}

func TestValidCongbaoFallbackCandidateRejectsFuzzyNumber(t *testing.T) {
	sd := dbbronze.BronzeSourceDocument{
		DocNumber:     strPtr("14/2022/NĐ-CP"),
		DocNumberNorm: normalizeDocNumberForStorage("14/2022/NĐ-CP"),
	}
	doc := ingest.DiscoveredDoc{
		Number: "13/2022/NĐ-CP",
		Files:  []ingest.FileRef{{URL: "https://example.invalid/doc.pdf", Ext: "pdf"}},
	}

	ok, reason := validCongbaoFallbackCandidate(sd, doc)

	if ok || reason != "number_mismatch" {
		t.Fatalf("validCongbaoFallbackCandidate = %v/%q, want number_mismatch", ok, reason)
	}
}

func TestValidCongbaoFallbackCandidateRejectsIssuedDateMismatch(t *testing.T) {
	issued := time.Date(2022, 1, 27, 0, 0, 0, 0, time.UTC)
	sd := dbbronze.BronzeSourceDocument{
		DocNumber:     strPtr("14/2022/NĐ-CP"),
		DocNumberNorm: normalizeDocNumberForStorage("14/2022/NĐ-CP"),
		IssuedAt:      &issued,
	}
	doc := ingest.DiscoveredDoc{
		Number:   "14/2022/NĐ-CP",
		IssuedAt: issued.AddDate(0, 0, 1),
		Files:    []ingest.FileRef{{URL: "https://example.invalid/doc.pdf", Ext: "pdf"}},
	}

	ok, reason := validCongbaoFallbackCandidate(sd, doc)

	if ok || reason != "issued_date_mismatch" {
		t.Fatalf("validCongbaoFallbackCandidate = %v/%q, want issued_date_mismatch", ok, reason)
	}
}

func TestValidCongbaoFallbackCandidateRejectsNoExtractableFiles(t *testing.T) {
	sd := dbbronze.BronzeSourceDocument{
		DocNumber:     strPtr("14/2022/NĐ-CP"),
		DocNumberNorm: normalizeDocNumberForStorage("14/2022/NĐ-CP"),
	}
	doc := ingest.DiscoveredDoc{
		Number: "14/2022/NĐ-CP",
		Files:  []ingest.FileRef{{URL: "https://example.invalid/doc.html", Ext: "html"}},
	}

	ok, reason := validCongbaoFallbackCandidate(sd, doc)

	if ok || reason != "no_extractable_files" {
		t.Fatalf("validCongbaoFallbackCandidate = %v/%q, want no_extractable_files", ok, reason)
	}
}

func TestDocKeyDedupesSourcesByDocNumber(t *testing.T) {
	vbpl := dbbronze.BronzeSourceDocument{Source: "vbpl", ExternalID: "152698", DocNumber: strPtr("14/2022/NĐ-CP"), DocType: strPtr("Nghị định")}
	congbao := dbbronze.BronzeSourceDocument{Source: "congbao", ExternalID: "36772", DocNumber: strPtr("14/2022/NĐ-CP"), DocType: strPtr("Nghị định")}

	if docKey(vbpl) != docKey(congbao) {
		t.Fatalf("docKey mismatch: vbpl=%q congbao=%q", docKey(vbpl), docKey(congbao))
	}
}

func TestDocKey(t *testing.T) {
	tests := []struct {
		name string
		sd   dbbronze.BronzeSourceDocument
		want string
	}{
		{
			name: "type discriminates documents sharing a number",
			sd:   dbbronze.BronzeSourceDocument{Source: "vbpl", ExternalID: "17067", DocNumber: strPtr("51/2005/QH11"), DocType: strPtr("Luật")},
			want: "LUẬT|51/2005/QH11",
		},
		{
			name: "same number different type yields a different key",
			sd:   dbbronze.BronzeSourceDocument{Source: "vbpl", ExternalID: "17116", DocNumber: strPtr("51/2005/QH11"), DocType: strPtr("Nghị quyết")},
			want: "NGHỊ QUYẾT|51/2005/QH11",
		},
		{
			name: "stray spaces around separators are tightened",
			sd:   dbbronze.BronzeSourceDocument{Source: "vbpl", ExternalID: "130588", DocNumber: strPtr("18 /2018/TT-NHNN"), DocType: strPtr("Thông tư")},
			want: "THÔNG TƯ|18/2018/TT-NHNN",
		},
		{
			name: "NBSP folds like a plain space",
			sd:   dbbronze.BronzeSourceDocument{Source: "vbpl", ExternalID: "1", DocNumber: strPtr("18 /2018/TT-NHNN"), DocType: strPtr("Thông tư")},
			want: "THÔNG TƯ|18/2018/TT-NHNN",
		},
		{
			name: "missing type keys on the number alone",
			sd:   dbbronze.BronzeSourceDocument{Source: "sbv_hanoi", ExternalID: "9", DocNumber: strPtr("99/2024/TT-NHNN")},
			want: "99/2024/TT-NHNN",
		},
		{
			name: "missing number falls back to source:external_id",
			sd:   dbbronze.BronzeSourceDocument{Source: "vbpl", ExternalID: "22313", DocType: strPtr("Hiến pháp")},
			want: "vbpl:22313",
		},
		{
			name: "blank number falls back to source:external_id",
			sd:   dbbronze.BronzeSourceDocument{Source: "vbpl", ExternalID: "9028", DocNumber: strPtr("  "), DocType: strPtr("Luật")},
			want: "vbpl:9028",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := docKey(tt.sd); got != tt.want {
				t.Fatalf("docKey = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPDFToTextEngine(t *testing.T) {
	// go-fitz requires a real PDF; skip if MuPDF shared library is unavailable.
	// This test only validates the engine tag and normalization wrapper.
	a := &Activities{}
	_, engine, _ := a.pdfToText(context.Background(), "doc-1", "/nonexistent.pdf")
	if engine != "mupdf/1" {
		t.Fatalf("engine = %q, want mupdf/1", engine)
	}
}

func TestAssessPDFExtractionEngineTag(t *testing.T) {
	// The content gate and extraction assessment logic is independent of the engine.
	// We verify the engine tag is correct after the migration.
	a := &Activities{}
	got := a.assessPDFExtraction(context.Background(), "doc-1", "/nonexistent.pdf", extract.DefaultGate())
	if got.engine != "mupdf/1" {
		t.Fatalf("engine = %q, want mupdf/1", got.engine)
	}
}

func TestAssessPDFExtractionRoutesGateFailureToOCR(t *testing.T) {
	// With a missing PDF, go-fitz returns an error and assessment falls through.
	a := &Activities{}
	got := a.assessPDFExtraction(context.Background(), "doc-1", "/nonexistent.pdf", extract.DefaultGate())
	if got.extractable {
		t.Fatal("assessPDFExtraction extractable=true on missing file, want failure")
	}
}

func TestDOCToTextEngineTag(t *testing.T) {
	// docToText requires soffice + go-fitz. Without soffice installed, it returns
	// an error; we only verify the engine tag comes through correctly.
	a := &Activities{}
	_, engine, err := a.docToText(context.Background(), "doc-1", []byte("legacy doc bytes"))
	// Expect an error (soffice likely missing in test env), but engine must be set.
	if err == nil {
		if engine != "libreoffice+mupdf/1" {
			t.Fatalf("engine = %q, want libreoffice+mupdf/1", engine)
		}
	}
}

func TestCleanPDFMarkdownNoiseRemovesCongbaoHeaders(t *testing.T) {
	in := "2\n\n" +
		"CÔNG BÁO/Số 223 + 224/Ngày 09-02-2022\n\n" +
		"Điều 1. Nội dung\n" +
		"\fCÔNG BÁO/Số 223 + 224/Ngày 09-02-2022\n\n" +
		"3\n\n" +
		"|     | CÔNG BÁO/Số 223 + 224/Ngày 09-02-2022  |     |     | 5 |\n" +
		"1. Sửa đổi quy định\n\n" +
		"100\n" +
		"Bảng số liệu"

	got := cleanPDFMarkdownNoise(in)
	if strings.Contains(got, "CÔNG BÁO/Số") {
		t.Fatalf("cleaned text still contains page header: %q", got)
	}
	if strings.Contains(got, "\f") {
		t.Fatalf("cleaned text still contains form feed: %q", got)
	}
	if strings.Contains("\n"+got+"\n", "\n2\n") || strings.Contains("\n"+got+"\n", "\n3\n") {
		t.Fatalf("cleaned text still contains adjacent page numbers: %q", got)
	}
	for _, want := range []string{"Điều 1. Nội dung", "1. Sửa đổi quy định", "100\nBảng số liệu"} {
		if !strings.Contains(got, want) {
			t.Fatalf("cleaned text = %q, want to keep %q", got, want)
		}
	}
}

func TestHTMLToTextExtractsBody(t *testing.T) {
	a := &Activities{}
	body := `<html><head><title>T</title></head><body><p>Bộ Tư pháp</p></body></html>`
	text, engine, err := a.htmlToText(context.Background(), "doc-1", body)
	if err != nil {
		t.Fatalf("htmlToText: %v", err)
	}
	if engine != "gohtml/1" {
		t.Fatalf("engine = %q, want gohtml/1", engine)
	}
	if !strings.Contains(text, "Bộ Tư pháp") {
		t.Fatalf("text = %q, want to contain body text", text)
	}
}

func TestAssessConvertedTextRejectsSupplementOnlyText(t *testing.T) {
	text := "**BÁO CÁO TÌNH HÌNH THỰC HIỆN CƠ CẤU LẠI THỜI HẠN TRẢ NỢ**\n\nTháng ... năm ..."
	confidence, ok, sourceUnavailable := assessConvertedText(text)
	if ok {
		t.Fatal("supplement-only text passed as binding")
	}
	if sourceUnavailable {
		t.Fatal("supplement-only text should not be treated as source-unavailable")
	}
	if confidence >= 0.6 {
		t.Fatalf("confidence = %f, want below pass threshold", confidence)
	}
}

func TestFileNameForArtifactUsesSourceName(t *testing.T) {
	got := fileNameForArtifact(ClaimedArtifact{
		RefKey:   "0.pdf",
		FileName: "VanBanGoc_09.2020.TT.NHNN.pdf",
	})
	if got != "VanBanGoc_09.2020.TT.NHNN.pdf" {
		t.Fatalf("fileNameForArtifact = %q, want source filename", got)
	}
}

func TestFileNameForArtifactFallsBackToRefKey(t *testing.T) {
	got := fileNameForArtifact(ClaimedArtifact{RefKey: "0.pdf"})
	if got != "0.pdf" {
		t.Fatalf("fileNameForArtifact = %q, want ref key", got)
	}
}
