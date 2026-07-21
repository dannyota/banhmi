package mcp

import (
	"reflect"
	"testing"
)

// The sample shape is Thông tư 64/2024/TT-NHNN: vbpl holds five files behind
// expiring presigned URLs, while vanban and sbv_hanoi serve two of the same
// byte-identical PDFs (matching sha256) behind durable direct links.
func TestMergeDocumentFiles(t *testing.T) {
	rows := []documentFileRow{
		{Source: "sbv_hanoi", Label: "120250123161239_64.pdf", Kind: "main", Format: "pdf", ByteSize: 3786318, SHA256: "sha-scan", URL: "https://sbv.hanoi.gov.vn/documents/a/64.pdf"},
		{Source: "vanban", Label: "64-nhnn.pdf", Kind: "main", Format: "pdf", ByteSize: 3786318, SHA256: "sha-scan", URL: "https://datafiles.chinhphu.vn/cpp/files/vbpq/2025/01/64-nhnn.pdf"},
		{Source: "vbpl", IsPrimary: true, Label: "Thông tư 64-2024-TT-NHNN.doc", Kind: "main", Format: "doc", ByteSize: 138752, SHA256: "sha-main-doc", URL: "https://s3.example.com/a.doc?X-Amz-Algorithm=AWS4-HMAC-SHA256"},
		{Source: "vbpl", IsPrimary: true, Label: "Phụ lục 1 đính kèm TT 64-2024-TT-NHNN.docx", Kind: "appendix", Format: "docx", ByteSize: 876790, SHA256: "sha-pl1", URL: "https://s3.example.com/pl1.docx?X-Amz-Algorithm=AWS4-HMAC-SHA256"},
		{Source: "vbpl", IsPrimary: true, Label: "VanBanGoc_Thông tư 64-2024-TT-NHNN.pdf", Kind: "original_scan", Format: "pdf", ByteSize: 3786318, SHA256: "sha-scan", URL: "https://s3.example.com/scan.pdf?X-Amz-Algorithm=AWS4-HMAC-SHA256"},
	}

	got := mergeDocumentFiles(rows)
	if len(got) != 3 {
		t.Fatalf("merged files = %d, want 3 (scan deduped across three sources): %+v", len(got), got)
	}

	// Main-first ordering; scan last.
	if got[0].SHA256 != "sha-main-doc" || got[1].SHA256 != "sha-pl1" || got[2].SHA256 != "sha-scan" {
		t.Fatalf("order = %q, %q, %q; want main, appendix, original_scan", got[0].SHA256, got[1].SHA256, got[2].SHA256)
	}

	// The primary source's row wins the metadata even when a sibling row was
	// scanned first.
	scan := got[2]
	if scan.Label != "VanBanGoc_Thông tư 64-2024-TT-NHNN.pdf" || scan.Kind != "original_scan" {
		t.Errorf("scan metadata = %q/%q, want the primary (vbpl) row's label and kind", scan.Label, scan.Kind)
	}

	// Durable links collected from both siblings; the presigned vbpl URL never
	// appears.
	wantOrigins := []docFileOrigin{
		{Source: "sbv_hanoi", URL: "https://sbv.hanoi.gov.vn/documents/a/64.pdf"},
		{Source: "vanban", URL: "https://datafiles.chinhphu.vn/cpp/files/vbpq/2025/01/64-nhnn.pdf"},
	}
	if !reflect.DeepEqual(scan.OriginURLs, wantOrigins) {
		t.Errorf("scan origin_urls = %+v, want %+v", scan.OriginURLs, wantOrigins)
	}
	if len(got[0].OriginURLs) != 0 || len(got[1].OriginURLs) != 0 {
		t.Errorf("vbpl-only files must carry no origin_urls, got %+v / %+v", got[0].OriginURLs, got[1].OriginURLs)
	}
}

func TestMergeDocumentFilesKeepsHashlessRowsSeparate(t *testing.T) {
	rows := []documentFileRow{
		{Source: "congbao", Label: "a.pdf", Kind: "main", Format: "pdf", URL: "https://congbao.example.vn/a.pdf"},
		{Source: "congbao", Label: "b.pdf", Kind: "main", Format: "pdf", URL: "https://congbao.example.vn/b.pdf"},
	}
	got := mergeDocumentFiles(rows)
	if len(got) != 2 {
		t.Fatalf("hashless rows must not merge, got %d entries: %+v", len(got), got)
	}
}

func TestDurableFileURL(t *testing.T) {
	for _, tc := range []struct {
		url  string
		want bool
	}{
		{"", false},
		{"https://s3-han02.fptcloud.com/nts-vbpl/1/a.doc?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Expires=86400", false},
		{"https://datafiles.chinhphu.vn/cpp/files/vbpq/2025/01/64-nhnn.pdf", true},
	} {
		if got := durableFileURL(tc.url); got != tc.want {
			t.Errorf("durableFileURL(%q) = %v, want %v", tc.url, got, tc.want)
		}
	}
}
