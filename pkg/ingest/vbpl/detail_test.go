package vbpl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"danny.vn/banhmi/pkg/ingest"
)

func TestFetchDetailMergesDiagramRelations(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/qtdc/public/doc/123", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{
			"success": true,
			"data": map[string]any{
				"id":         "123",
				"docNum":     "10/2026/TT-NHNN",
				"title":      "Thong tu so 10",
				"issueDate":  "2026-05-01T00:00:00",
				"effFrom":    "2026-06-01T00:00:00",
				"agencyName": "Ngân hàng Nhà nước",
				"effStatus":  map[string]any{"code": "CHL", "name": "Còn hiệu lực"},
				"docType":    map[string]any{"code": "TT", "name": "Thông tư"},
				"documentContent": map[string]any{
					"content": "<p>body</p>",
				},
				"references": []map[string]any{{
					"referenceType": 10,
					"targetDocument": map[string]any{
						"id":     "168220",
						"docNum": "27/2024/TT-NHNN",
						"title":  "Detail title wins",
					},
				}, {
					"referenceType": 10,
					"targetDocument": map[string]any{
						"id":     "168220",
						"docNum": "27/2024/TT-NHNN",
						"title":  "Duplicate detail reference",
					},
				}},
			},
		})
	})
	mux.HandleFunc("/api/qtdc/public/doc/123/diagram", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{
			"success": true,
			"data": map[string]any{
				"documentNamesByType": map[string]any{
					"1": []map[string]any{{
						"id":   "182432",
						"name": "Thông tư số 28/2025/TT-NHNN Sửa đổi, bổ sung một số điều",
					}},
					"3": []map[string]any{{
						"id":   "166170",
						"name": "Luật Các tổ chức tín dụng số 32/2024/QH15",
					}},
					"10": []map[string]any{{
						"id":   "168220",
						"name": "Thông tư số 27/2024/TT-NHNN Quy định...",
					}},
				},
				"documentNamesBySource": map[string]any{},
			},
		})
	})
	mux.HandleFunc("/api/qtdc/public/doc/minio/buckets/vbpl/folders/123/files", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"success": true, "data": []any{}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	doc, err := testSource(t, srv).FetchDetail(context.Background(), ingest.DetailRef{ExternalID: "123"})
	if err != nil {
		t.Fatalf("FetchDetail: %v", err)
	}

	if len(doc.Relations) != 3 {
		t.Fatalf("len(Relations) = %d, want 3: %#v", len(doc.Relations), doc.Relations)
	}
	byType := map[int]string{}
	titleByType := map[int]string{}
	for _, rel := range doc.Relations {
		byType[rel.TypeRaw] = rel.TargetNumber
		titleByType[rel.TypeRaw] = rel.TargetTitle
	}
	if byType[10] != "27/2024/TT-NHNN" || titleByType[10] != "Detail title wins" {
		t.Fatalf("type 10 = number %q title %q, want detail relation", byType[10], titleByType[10])
	}
	if byType[3] != "32/2024/QH15" {
		t.Fatalf("type 3 target = %q, want 32/2024/QH15", byType[3])
	}
	if byType[1] != "28/2025/TT-NHNN" {
		t.Fatalf("type 1 target = %q, want 28/2025/TT-NHNN", byType[1])
	}
}

func TestMergeVBPLRelationsKeepsSameNumberDifferentTargetIDs(t *testing.T) {
	relations := mergeVBPLRelations([]ingest.Relation{{
		TypeRaw:      13,
		TargetNumber: "04/2007/QH12",
		TargetID:     "12898",
		TargetTitle:  "Luật Thuế thu nhập cá nhân số 04/2007/QH12",
	}, {
		TypeRaw:      13,
		TargetNumber: "04/2007/QH12",
		TargetID:     "25400",
		TargetTitle:  "Nghị quyết số 04/2007/QH12",
	}, {
		TypeRaw:      13,
		TargetNumber: "04/2007/QH12",
		TargetID:     "25400",
		TargetTitle:  "Duplicate diagram row",
	}})

	if len(relations) != 2 {
		t.Fatalf("len(relations) = %d, want 2: %#v", len(relations), relations)
	}
	if relations[0].TargetID != "12898" || relations[1].TargetID != "25400" {
		t.Fatalf("target IDs = %q/%q, want 12898/25400", relations[0].TargetID, relations[1].TargetID)
	}
}

func TestPreferredFiles_keepsDocxAndPDFWithMainBodyFirst(t *testing.T) {
	// Files as the vbpl API lists them for 09/2020/TT-NHNN: html body, the
	// appendix docx, the main-body docx, and the scanned original pdf.
	entries := []fileEntry{
		{FileName: "144532_body_content.html", PresignedURL: "u-html"},
		{FileName: "Phụ lục đính kèm 09-2020-TT-NHNN.docx", PresignedURL: "u-appendix"},
		{FileName: "Thông tư 09-2020-TT-NHNN.docx", PresignedURL: "u-main"},
		{FileName: "VanBanGoc_09.2020.TT.NHNN.pdf", PresignedURL: "u-scan"},
	}
	got := preferredFiles(entries)

	if len(got) != 3 {
		t.Fatalf("want 2 docx + 1 pdf (html dropped), got %d: %v", len(got), refNames(got))
	}
	if got[0].Name != "Thông tư 09-2020-TT-NHNN.docx" {
		t.Errorf("main body must be ordinal 0, got %q (%v)", got[0].Name, refNames(got))
	}
	if got[0].Kind != "main" || got[1].Kind != "appendix" || got[2].Kind != "original_scan" {
		t.Errorf("file kinds = %q, %q, %q; want main, appendix, original_scan", got[0].Kind, got[1].Kind, got[2].Kind)
	}
	if got[2].Ext != "pdf" {
		t.Errorf("scanned source pdf must be preserved after docx files, got %q", got[2].Name)
	}
}

func TestPreferredFilesClassifiesUnaccentedAppendixDocx(t *testing.T) {
	got := preferredFiles([]fileEntry{
		{FileName: "Phu luc kem TT15.2024.TT.NHNN.docx", PresignedURL: "u-appendix"},
		{FileName: "VanBanGoc_15.2024.TT.NHNN.pdf", PresignedURL: "u-scan", RelatedType: 1},
	})

	if len(got) != 2 {
		t.Fatalf("want appendix docx + original scan pdf, got %d: %v", len(got), refNames(got))
	}
	if got[0].Kind != "appendix" {
		t.Fatalf("unaccented Phu luc file kind = %q, want appendix", got[0].Kind)
	}
	if got[1].Kind != "original_scan" {
		t.Fatalf("relatedType=1 pdf kind = %q, want original_scan", got[1].Kind)
	}
}

func TestPreferredFilesKeepsLegacyDocAsEvidence(t *testing.T) {
	got := preferredFiles([]fileEntry{
		{FileName: "08-2024-TT-NHNN.doc", PresignedURL: "u-doc"},
		{FileName: "VanBanGoc_08-2024-TT-NHNN.pdf", PresignedURL: "u-scan", RelatedType: 1},
	})

	if len(got) != 2 {
		t.Fatalf("want legacy doc + original scan pdf, got %d: %v", len(got), refNames(got))
	}
	if got[0].Ext != "doc" || got[0].Kind != "main" {
		t.Fatalf("legacy doc = ext %q kind %q, want doc main", got[0].Ext, got[0].Kind)
	}
}

func TestPreferredFilesUsesRelatedType1DocxBeforePDF(t *testing.T) {
	got := preferredFiles([]fileEntry{
		{FileName: "Thông tư 11-2026-TT-NHNN.docx", PresignedURL: "u-docx", RelatedType: 1},
		{FileName: "VanBanGoc_11-2026-TT-NHNN.pdf", PresignedURL: "u-pdf", RelatedType: 1},
	})

	if len(got) != 2 {
		t.Fatalf("want docx + pdf, got %d: %v", len(got), refNames(got))
	}
	if got[0].Ext != "docx" || got[0].Kind != "main" {
		t.Fatalf("relatedType=1 docx = ext %q kind %q, want main docx", got[0].Ext, got[0].Kind)
	}
	if got[1].Ext != "pdf" || got[1].Kind != "original_scan" {
		t.Fatalf("relatedType=1 pdf = ext %q kind %q, want original_scan pdf", got[1].Ext, got[1].Kind)
	}
}

func TestPreferredFilesClassifiesTT152024ByNameNotRelatedType(t *testing.T) {
	got := preferredFiles([]fileEntry{
		{FileName: "Phu luc kem TT15.2024.TT.NHNN.docx", PresignedURL: "u-appendix", RelatedType: 2},
		{FileName: "Thông tư 15.2024.TT.NHNN.doc", PresignedURL: "u-main-doc", RelatedType: 2},
		{FileName: "Thong tu 15.2024.TT.NHNN.pdf", PresignedURL: "u-pdf", RelatedType: 1},
		{FileName: "VanBanGoc_Thong tu 15.2024.TT.NHNN.pdf", PresignedURL: "u-scan", RelatedType: 1},
	})

	if len(got) != 4 {
		t.Fatalf("want doc, appendix docx, and two original scans; got %d: %v", len(got), refNames(got))
	}
	byName := map[string]ingest.FileRef{}
	for _, f := range got {
		byName[f.Name] = f
	}
	if byName["Thông tư 15.2024.TT.NHNN.doc"].Kind != "main" {
		t.Fatalf("relatedType=2 main .doc kind = %q, want main", byName["Thông tư 15.2024.TT.NHNN.doc"].Kind)
	}
	if byName["Phu luc kem TT15.2024.TT.NHNN.docx"].Kind != "appendix" {
		t.Fatalf("relatedType=2 appendix .docx kind = %q, want appendix", byName["Phu luc kem TT15.2024.TT.NHNN.docx"].Kind)
	}
	if byName["Thong tu 15.2024.TT.NHNN.pdf"].Kind != "original_scan" ||
		byName["VanBanGoc_Thong tu 15.2024.TT.NHNN.pdf"].Kind != "original_scan" {
		t.Fatalf("relatedType=1 PDF kinds = %q, %q; want original_scan",
			byName["Thong tu 15.2024.TT.NHNN.pdf"].Kind,
			byName["VanBanGoc_Thong tu 15.2024.TT.NHNN.pdf"].Kind)
	}
	if got[0].Name != "Thông tư 15.2024.TT.NHNN.doc" {
		t.Fatalf("main text evidence must sort before appendix, got %v", refNames(got))
	}
}

func TestPreferredFilesSkipsVBPLHTMLDescriptors(t *testing.T) {
	got := preferredFiles([]fileEntry{
		{FileName: "168089_body_content.html", PresignedURL: "u-body", RelatedType: 4},
		{FileName: "168089_content.html", PresignedURL: "u-content", RelatedType: 5},
	})

	if len(got) != 0 {
		t.Fatalf("html descriptors must be skipped as raw files, got %v", refNames(got))
	}
}

func TestPreferredFiles_scannedOnlyKeepsPDF(t *testing.T) {
	// A scanned-only doc has no docx — keep the pdf so the OCR path runs.
	got := preferredFiles([]fileEntry{
		{FileName: "144999_content.html", PresignedURL: "u-html"},
		{FileName: "VanBanGoc.pdf", PresignedURL: "u-scan"},
	})
	if len(got) != 1 || got[0].Ext != "pdf" {
		t.Fatalf("scanned-only doc must keep the pdf, got %v", refNames(got))
	}
	if got[0].Kind != "original_scan" {
		t.Fatalf("scanned-only pdf kind = %q, want original_scan", got[0].Kind)
	}
}

func TestDocIDPrefersDiscoveredExternalID(t *testing.T) {
	const uuid = "a96135c0-54da-11f1-b33d-e72bd5f85c26"
	got, err := docID(ingest.DetailRef{
		ExternalID: uuid,
		DetailURL:  "https://vbpl.vn/van-ban/chi-tiet/26",
	})
	if err != nil {
		t.Fatalf("docID: %v", err)
	}
	if got != uuid {
		t.Fatalf("docID = %q, want discovered external id %q", got, uuid)
	}
}

func TestParseDocIDFallback(t *testing.T) {
	cases := []struct {
		name, in, want string
		wantErr        bool
	}{
		{name: "numeric ItemID", in: "https://vbpl.vn/van-ban/chi-tiet/144532", want: "144532"},
		// Regression: a UUID ending in digits must NOT be reduced to a trailing
		// numeric run — "…f85c26" once resolved to doc/26, a different document.
		{name: "uuid ending in digits", in: "https://vbpl.vn/van-ban/chi-tiet/a96135c0-54da-11f1-b33d-e72bd5f85c26", want: "a96135c0-54da-11f1-b33d-e72bd5f85c26"},
		{name: "uuid ending in letter", in: "https://vbpl.vn/van-ban/chi-tiet/835e3190-54dd-11f1-99b0-7968d7bd8bcd", want: "835e3190-54dd-11f1-99b0-7968d7bd8bcd"},
		{name: "trailing slash", in: "https://vbpl.vn/van-ban/chi-tiet/9712/", want: "9712"},
		{name: "query stripped", in: "https://vbpl.vn/van-ban/chi-tiet/9712?x=1", want: "9712"},
		{name: "bare id", in: "144532", want: "144532"},
		{name: "empty", in: "  ", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseDocID(c.in)
			if c.wantErr {
				if err == nil {
					t.Fatalf("want error for %q, got %q", c.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseDocID(%q): %v", c.in, err)
			}
			if got != c.want {
				t.Fatalf("parseDocID(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func refNames(fs []ingest.FileRef) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.Name
	}
	return out
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}
