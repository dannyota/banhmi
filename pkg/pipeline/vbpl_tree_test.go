package pipeline

import "testing"

const vbplTreeSample = `[
  {
    "id": "chapter-id",
    "key": "chapter-key",
    "title": "Chương I. QUY ĐỊNH CHUNG",
    "ptype": 2,
    "level": "Chapter",
    "orderIndex": 1,
    "content": {
      "title": "Chương I. QUY ĐỊNH CHUNG",
      "content": "Chương I. QUY ĐỊNH CHUNG<br/>Điều 1. Phạm vi điều chỉnh<br/>Nội dung riêng của điều.<br/>1. Khoản một.<br/>a) Điểm a."
    },
    "children": [
      {
        "id": "article-id",
        "key": "article-key",
        "title": "Điều 1. Phạm vi điều chỉnh",
        "ptype": 5,
        "level": "Article",
        "orderIndex": 1,
        "content": {
          "title": "Điều 1. Phạm vi điều chỉnh",
          "content": "Điều 1. Phạm vi điều chỉnh<br/>Nội dung riêng của điều.<br/>1. Khoản một.<br/>a) Điểm a."
        },
        "children": [
          {
            "id": "clause-id",
            "key": "clause-key",
            "title": "Khoản 1",
            "ptype": 6,
            "level": "Clause",
            "orderIndex": 1,
            "content": {
              "title": "Khoản 1",
              "content": "1. Khoản một.<br/>a) Điểm a."
            },
            "children": [
              {
                "id": "point-id",
                "key": "point-key",
                "title": "Điểm a",
                "ptype": 7,
                "level": "Point",
                "orderIndex": 1,
                "content": {
                  "title": "Điểm a",
                  "content": "a) Điểm a."
                },
                "children": []
              }
            ]
          }
        ]
      }
    ]
  }
]`

func TestParseVBPLProvisionTreePayload(t *testing.T) {
	roots, stats, warnings, ok := parseVBPLProvisionTreePayload(vbplTreeSample)
	if !ok {
		t.Fatalf("tree ok = false, warnings = %v", warnings)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if stats.Total != 4 || stats.Chuong != 1 || stats.Dieu != 1 || stats.Khoan != 1 || stats.Diem != 1 {
		t.Fatalf("stats = %+v, want 1 chapter/article/clause/point", stats)
	}
	if stats.Content != 3 {
		t.Fatalf("stats.Content = %d, want 3", stats.Content)
	}

	chapter := roots[0]
	if chapter.NodeKey != "chapter-key" || chapter.PType != 2 {
		t.Fatalf("chapter source ids = %q/%d, want chapter-key/2", chapter.NodeKey, chapter.PType)
	}
	if chapter.CitationPath != "chuong-1" {
		t.Fatalf("chapter path = %q, want chuong-1", chapter.CitationPath)
	}
	article := chapter.Children[0]
	if article.Label != "Điều 1" || article.Heading != "Phạm vi điều chỉnh" {
		t.Fatalf("article title = %q/%q, want Điều 1/Phạm vi điều chỉnh", article.Label, article.Heading)
	}
	if article.Content != "Nội dung riêng của điều." {
		t.Fatalf("article content = %q, want own article text only", article.Content)
	}
	clause := article.Children[0]
	if clause.CitationPath != "chuong-1/dieu-1/khoan-1" || clause.Content != "Khoản một." {
		t.Fatalf("clause = %q content %q, want path/content without point duplication", clause.CitationPath, clause.Content)
	}
	point := clause.Children[0]
	if point.CitationPath != "chuong-1/dieu-1/khoan-1/diem-a" || point.Content != "Điểm a." {
		t.Fatalf("point = %q content %q, want point path/content", point.CitationPath, point.Content)
	}
}

func TestParseVBPLProvisionTreePayloadEnvelope(t *testing.T) {
	_, stats, warnings, ok := parseVBPLProvisionTreePayload(`{"success":true,"data":` + vbplTreeSample + `}`)
	if !ok {
		t.Fatalf("envelope tree ok = false, warnings = %v", warnings)
	}
	if stats.Total != 4 {
		t.Fatalf("stats.Total = %d, want 4", stats.Total)
	}
}

func TestParseVBPLProvisionTreePayloadEmptyAndInvalid(t *testing.T) {
	if _, _, _, ok := parseVBPLProvisionTreePayload(`[]`); ok {
		t.Fatal("empty tree ok = true, want false")
	}
	noContent := `[{"title":"Điều 1. Không có nội dung","ptype":5,"level":"Article","content":{"title":"Điều 1. Không có nội dung","content":""},"children":[]}]`
	if _, _, warnings, ok := parseVBPLProvisionTreePayload(noContent); ok || !hasWarning(warnings, "no_section_content") {
		t.Fatalf("contentless tree ok=%v warnings=%v, want content warning", ok, warnings)
	}
	if _, _, warnings, ok := parseVBPLProvisionTreePayload(`not-json`); ok || !hasWarning(warnings, "invalid_vbpl_provision_tree") {
		t.Fatalf("invalid tree ok=%v warnings=%v, want invalid warning", ok, warnings)
	}
}
