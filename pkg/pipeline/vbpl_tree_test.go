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

// TestRecoverEmptyNodeBody_HeadingIsBody reproduces the most common empty-body
// shape: a short article whose entire text lives in the node's title (and
// therefore in content.content), so stripTreePrefixes — which tries the full
// title as its first variant — strips the text entirely. The heading must be
// promoted to Content and the heading cleared to avoid duplication.
func TestRecoverEmptyNodeBody_HeadingIsBody(t *testing.T) {
	tree := `[{
		"id":"a1","key":"a1",
		"title":"Điều 1. Kể từ ngày 01 tháng 01 năm 2003, thống nhất dùng bộ mã.",
		"ptype":5,"level":"Article","orderIndex":1,
		"content":{
			"title":"Điều 1",
			"content":"Điều 1. Kể từ ngày 01 tháng 01 năm 2003, thống nhất dùng bộ mã."
		},
		"children":[]
	}]`
	roots, stats, warnings, ok := parseVBPLProvisionTreePayload(tree)
	if !ok {
		t.Fatalf("ok=false, warnings=%v", warnings)
	}
	if stats.Content != 1 {
		t.Fatalf("stats.Content=%d, want 1 (recovered)", stats.Content)
	}
	art := roots[0]
	if art.Heading != "" {
		t.Errorf("heading=%q, want empty (moved to Content)", art.Heading)
	}
	want := "Kể từ ngày 01 tháng 01 năm 2003, thống nhất dùng bộ mã."
	if art.Content != want {
		t.Errorf("content=%q, want %q", art.Content, want)
	}
}

// TestRecoverEmptyNodeBody_ContentBeyondHeading verifies the case where the
// node's content.content has body text after the heading (e.g. "Điều 3.
// Heading:<br>- Body text."). This is handled correctly by vbplOwnContent
// already; the test documents that recovery does NOT alter such sections.
func TestRecoverEmptyNodeBody_ContentBeyondHeading(t *testing.T) {
	tree := `[{
		"id":"a1","key":"a1",
		"title":"Điều 3. Giao Bộ Khoa học:",
		"ptype":5,"level":"Article","orderIndex":1,
		"content":{
			"title":"Điều 3",
			"content":"Điều 3. Giao Bộ Khoa học:<br>- Chủ trì và phối hợp triển khai."
		},
		"children":[]
	}]`
	roots, _, _, ok := parseVBPLProvisionTreePayload(tree)
	if !ok {
		t.Fatal("ok=false")
	}
	art := roots[0]
	if art.Heading != "Giao Bộ Khoa học:" {
		t.Errorf("heading=%q, want 'Giao Bộ Khoa học:'", art.Heading)
	}
	if art.Content != "- Chủ trì và phối hợp triển khai." {
		t.Errorf("content=%q, want body after heading", art.Content)
	}
}

// TestRecoverEmptyNodeBody_TrulyEmpty verifies a childless article with empty
// content.content stays empty (no false recovery).
func TestRecoverEmptyNodeBody_TrulyEmpty(t *testing.T) {
	tree := `[{
		"id":"a1","key":"a1",
		"title":"Điều 1. Phạm vi điều chỉnh",
		"ptype":5,"level":"Article","orderIndex":1,
		"content":{"title":"Điều 1","content":""},
		"children":[]
	}]`
	_, stats, warnings, ok := parseVBPLProvisionTreePayload(tree)
	// Empty content: the tree is rejected (stats.Content == 0).
	if ok {
		t.Fatalf("ok=true with empty content; stats=%+v warnings=%v", stats, warnings)
	}
	if !hasWarning(warnings, "no_section_content") {
		t.Fatalf("warnings=%v, want no_section_content", warnings)
	}
}

// TestRecoverEmptyNodeBody_NormalTreeUnchanged is a regression guard: a normal
// article (with children and own-content) must be byte-identical regardless of
// the recovery path.
func TestRecoverEmptyNodeBody_NormalTreeUnchanged(t *testing.T) {
	// Re-parse the golden tree and verify the article section is unchanged.
	roots, _, _, ok := parseVBPLProvisionTreePayload(vbplTreeSample)
	if !ok {
		t.Fatal("ok=false")
	}
	art := roots[0].Children[0]
	if art.Label != "Điều 1" || art.Heading != "Phạm vi điều chỉnh" {
		t.Fatalf("label=%q heading=%q, want Điều 1 / Phạm vi điều chỉnh", art.Label, art.Heading)
	}
	if art.Content != "Nội dung riêng của điều." {
		t.Fatalf("content=%q, want own-content text (no recovery needed)", art.Content)
	}
}

// TestRecoverEmptyNodeBody_MultiArticleMixed tests a chapter with a mix of
// normal and heading-only articles to verify recovery doesn't disturb siblings.
func TestRecoverEmptyNodeBody_MultiArticleMixed(t *testing.T) {
	tree := `[{
		"id":"ch1","key":"ch1",
		"title":"Chương I QUY ĐỊNH CHUNG",
		"ptype":2,"level":"Chapter","orderIndex":1,
		"content":{
			"title":"Chương I",
			"content":"Chương I QUY ĐỊNH CHUNG<br>Điều 1. Phạm vi điều chỉnh<br>Điều 2. Thông tư này có hiệu lực từ ngày 01 tháng 7 năm 2024.<br>Điều 3. Đối tượng áp dụng<br>1. Tổ chức tín dụng."
		},
		"children":[
			{
				"id":"a1","key":"a1",
				"title":"Điều 1. Phạm vi điều chỉnh",
				"ptype":5,"level":"Article","orderIndex":1,
				"content":{"title":"Điều 1","content":"Điều 1. Phạm vi điều chỉnh"},
				"children":[]
			},
			{
				"id":"a2","key":"a2",
				"title":"Điều 2. Thông tư này có hiệu lực từ ngày 01 tháng 7 năm 2024.",
				"ptype":5,"level":"Article","orderIndex":2,
				"content":{"title":"Điều 2","content":"Điều 2. Thông tư này có hiệu lực từ ngày 01 tháng 7 năm 2024."},
				"children":[]
			},
			{
				"id":"a3","key":"a3",
				"title":"Điều 3. Đối tượng áp dụng",
				"ptype":5,"level":"Article","orderIndex":3,
				"content":{"title":"Điều 3","content":"Điều 3. Đối tượng áp dụng<br>1. Tổ chức tín dụng."},
				"children":[]
			}
		]
	}]`

	roots, stats, warnings, ok := parseVBPLProvisionTreePayload(tree)
	if !ok {
		t.Fatalf("ok=false, warnings=%v", warnings)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings=%v, want none", warnings)
	}

	arts := roots[0].Children
	if len(arts) != 3 {
		t.Fatalf("articles=%d, want 3", len(arts))
	}

	// Điều 1: heading-only, short label heading → heading promoted to Content.
	a1 := arts[0]
	if a1.Content != "Phạm vi điều chỉnh" {
		t.Errorf("a1.Content=%q, want 'Phạm vi điều chỉnh'", a1.Content)
	}
	if a1.Heading != "" {
		t.Errorf("a1.Heading=%q, want empty (moved to Content)", a1.Heading)
	}

	// Điều 2: heading IS the body text → must recover.
	a2 := arts[1]
	if a2.Content != "Thông tư này có hiệu lực từ ngày 01 tháng 7 năm 2024." {
		t.Errorf("a2.Content=%q, want recovered body", a2.Content)
	}
	if a2.Heading != "" {
		t.Errorf("a2.Heading=%q, want empty (moved to Content)", a2.Heading)
	}

	// Điều 3: content beyond heading → normal path, no recovery.
	a3 := arts[2]
	if a3.Heading != "Đối tượng áp dụng" {
		t.Errorf("a3.Heading=%q, want 'Đối tượng áp dụng'", a3.Heading)
	}
	if a3.Content != "1. Tổ chức tín dụng." {
		t.Errorf("a3.Content=%q, want body text", a3.Content)
	}

	if stats.Content != 3 {
		t.Errorf("stats.Content=%d, want 3", stats.Content)
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
