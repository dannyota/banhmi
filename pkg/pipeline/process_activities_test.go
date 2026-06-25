package pipeline

import (
	"context"
	"os"
	"path/filepath"
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

func TestPDFToMarkdownUsesMarkItDownPath(t *testing.T) {
	dir := t.TempDir()
	pdfPath := filepath.Join(dir, "doc.pdf")
	if err := os.WriteFile(pdfPath, []byte("%PDF fake"), 0o600); err != nil {
		t.Fatalf("write temp pdf: %v", err)
	}

	script := filepath.Join(dir, "fake_markitdown.py")
	if err := os.WriteFile(script, []byte(`#!/usr/bin/env python3
import json
import pathlib
import sys
pathlib.Path(sys.argv[1] + ".seen").write_text(sys.argv[1], encoding="utf-8")
print(json.dumps({"markdown": "Điều 1. Quy định", "title": None}, ensure_ascii=False))
`), 0o700); err != nil {
		t.Fatalf("write fake script: %v", err)
	}

	a := &Activities{markitdown: extract.NewMarkItDownClient("python3", script)}
	text, engine, err := a.pdfToMarkdown(context.Background(), "doc-1", pdfPath)
	if err != nil {
		t.Fatalf("pdfToMarkdown: %v", err)
	}
	seen, err := os.ReadFile(pdfPath + ".seen")
	if err != nil {
		t.Fatalf("read seen file: %v", err)
	}
	if string(seen) != pdfPath {
		t.Fatalf("path = %q, want %q", string(seen), pdfPath)
	}
	if engine != "markitdown/1" {
		t.Fatalf("engine = %q, want markitdown/1", engine)
	}
	if text != "Điều 1. Quy định" {
		t.Fatalf("text = %q, want NFC-normalized markdown", text)
	}
}

func TestAssessPDFExtractionUsesGoGate(t *testing.T) {
	dir := t.TempDir()
	pdfPath := filepath.Join(dir, "doc.pdf")
	if err := os.WriteFile(pdfPath, []byte("%PDF fake"), 0o600); err != nil {
		t.Fatalf("write temp pdf: %v", err)
	}

	script := filepath.Join(dir, "fake_markitdown.py")
	if err := os.WriteFile(script, []byte(`#!/usr/bin/env python3
import json
print(json.dumps({"markdown": "Điều 1. Ngân hàng Nhà nước Việt Nam quy định điều kiện áp dụng. " * 12, "title": None}, ensure_ascii=False))
`), 0o700); err != nil {
		t.Fatalf("write fake script: %v", err)
	}

	a := &Activities{markitdown: extract.NewMarkItDownClient("python3", script)}
	got := a.assessPDFExtraction(context.Background(), "doc-1", pdfPath, extract.DefaultGate())
	if !got.extractable {
		t.Fatalf("assessPDFExtraction extractable=false reason=%q confidence=%f", got.reason, got.gate.Confidence)
	}
	if got.engine != "markitdown/1" {
		t.Fatalf("engine = %q, want markitdown/1", got.engine)
	}
	if got.gate.Confidence < 0.6 {
		t.Fatalf("confidence = %f, want passing gate", got.gate.Confidence)
	}
}

func TestAssessPDFExtractionRoutesGateFailureToOCR(t *testing.T) {
	dir := t.TempDir()
	pdfPath := filepath.Join(dir, "doc.pdf")
	if err := os.WriteFile(pdfPath, []byte("%PDF fake"), 0o600); err != nil {
		t.Fatalf("write temp pdf: %v", err)
	}

	script := filepath.Join(dir, "fake_markitdown.py")
	if err := os.WriteFile(script, []byte(`#!/usr/bin/env python3
import json
print(json.dumps({"markdown": "abc def ghi jkl mno pqr stu vwx " * 20, "title": None}, ensure_ascii=False))
`), 0o700); err != nil {
		t.Fatalf("write fake script: %v", err)
	}

	a := &Activities{markitdown: extract.NewMarkItDownClient("python3", script)}
	got := a.assessPDFExtraction(context.Background(), "doc-1", pdfPath, extract.DefaultGate())
	if got.extractable {
		t.Fatal("assessPDFExtraction extractable=true, want OCR route")
	}
	if !strings.Contains(got.reason, "low diacritic density") {
		t.Fatalf("reason = %q, want low diacritic density", got.reason)
	}
}

func TestDOCToMarkdownUsesLegacyPDFBridge(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake_markitdown.py")
	if err := os.WriteFile(script, []byte(`#!/usr/bin/env python3
import json
import pathlib
import sys
path = pathlib.Path(sys.argv[1])
if path.suffix != ".doc":
    raise SystemExit(2)
print(json.dumps({"markdown": "Điều 1. Nội dung từ DOC", "title": None}, ensure_ascii=False))
`), 0o700); err != nil {
		t.Fatalf("write fake script: %v", err)
	}

	a := &Activities{markitdown: extract.NewMarkItDownClient("python3", script)}
	text, engine, err := a.docToMarkdown(context.Background(), "doc-1", []byte("legacy doc bytes"))
	if err != nil {
		t.Fatalf("docToMarkdown: %v", err)
	}
	if engine != "libreoffice-pdf/1+markitdown/1" {
		t.Fatalf("engine = %q, want libreoffice-pdf/1+markitdown/1", engine)
	}
	if text != "Điều 1. Nội dung từ DOC" {
		t.Fatalf("text = %q, want NFC-normalized markdown", text)
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

func TestHTMLToMarkdownPassesBodyThrough(t *testing.T) {
	// htmlToMarkdown hands the UTF-8 body to the helper unchanged; charset forcing
	// for HTML lives in tools/markitdown_convert.py (StreamInfo(charset="utf-8")),
	// not in Go. The fake helper echoes its input so we can assert the round-trip.
	dir := t.TempDir()
	script := filepath.Join(dir, "fake_markitdown.py")
	if err := os.WriteFile(script, []byte(`#!/usr/bin/env python3
import json
import pathlib
import sys
body = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8")
print(json.dumps({"markdown": body, "title": None}, ensure_ascii=False))
`), 0o700); err != nil {
		t.Fatalf("write fake script: %v", err)
	}

	a := &Activities{markitdown: extract.NewMarkItDownClient("python3", script)}
	body := `<html><head></head><body>Bộ Tư pháp</body></html>`
	text, engine, err := a.htmlToMarkdown(context.Background(), "doc-1", body)
	if err != nil {
		t.Fatalf("htmlToMarkdown: %v", err)
	}
	if engine != "markitdown/1" {
		t.Fatalf("engine = %q, want markitdown/1", engine)
	}
	if text != body {
		t.Fatalf("htmlToMarkdown altered the body: %q", text)
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
