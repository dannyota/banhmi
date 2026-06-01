package pipeline

import (
	"strings"
	"testing"
)

// viSnippet is a realistic Vietnamese legal Markdown text covering the main
// structural levels (Chương, Điều, Khoản, Điểm) with body text in each.
// Blank lines before headings mirror what the DOCX extractor emits.
const viSnippet = `

Chương I
QUY ĐỊNH CHUNG

Điều 1. Phạm vi điều chỉnh
Thông tư này quy định về hoạt động cho vay.

Điều 2. Đối tượng áp dụng
Thông tư này áp dụng với các tổ chức tín dụng.

1. Ngân hàng thương mại nhà nước.

2. Ngân hàng thương mại cổ phần.

a) Ngân hàng trong nước.

b) Ngân hàng nước ngoài.

Chương II
QUY ĐỊNH CỤ THỂ

Điều 3. Nguyên tắc cho vay
Tổ chức tín dụng thực hiện theo nguyên tắc sau.

1. Nguyên tắc một: sử dụng vốn đúng mục đích.

2. Nguyên tắc hai: hoàn trả đầy đủ gốc và lãi.
`

func TestParseSections_markdownEmphasis(t *testing.T) {
	// MarkItDown wraps headings in bold ("**Điều 1. ...**") and renders GFM tables.
	// The parser must still recognise the Chương/Điều/Khoản structure, store clean
	// heading text (no ** markers), and not let table rows leak as sections.
	md := "\n**Chương I**\n\n**QUY ĐỊNH CHUNG**\n\n" +
		"**Điều 1. Phạm vi điều chỉnh**\n\nThông tư này quy định về hoạt động cho vay.\n\n" +
		"| **STT** | **Tên tổ chức** |\n| --- | --- |\n| 1 | Ngân hàng A |\n\n" +
		"**Điều 2. Đối tượng áp dụng**\n\n1. Ngân hàng thương mại nhà nước.\n\n2. Ngân hàng thương mại cổ phần.\n"

	roots := ParseSections(md)
	if len(roots) != 1 || roots[0].Kind != "chuong" {
		t.Fatalf("expected 1 Chương root, got %d: %v", len(roots), rootKinds(roots))
	}
	ch := roots[0]
	if ch.Ordinal != 1 || ch.Heading != "QUY ĐỊNH CHUNG" {
		t.Errorf("Chương: ordinal=%d heading=%q, want 1 / 'QUY ĐỊNH CHUNG'", ch.Ordinal, ch.Heading)
	}
	if len(ch.Children) != 2 {
		t.Fatalf("expected 2 Điều under Chương I, got %d", len(ch.Children))
	}
	d1 := ch.Children[0]
	if d1.Kind != "dieu" || d1.Ordinal != 1 {
		t.Errorf("Điều 1: kind=%q ordinal=%d, want dieu / 1", d1.Kind, d1.Ordinal)
	}
	if d1.Heading != "Phạm vi điều chỉnh" {
		t.Errorf("Điều 1 heading = %q, want 'Phạm vi điều chỉnh' (markdown ** must be stripped)", d1.Heading)
	}
	if d2 := ch.Children[1]; d2.Ordinal != 2 || len(d2.Children) != 2 {
		t.Errorf("Điều 2: ordinal=%d khoản=%d, want 2 / 2", d2.Ordinal, len(d2.Children))
	}
}

func TestParseSections_inlineMarkdownEmphasis(t *testing.T) {
	md := `
**Điều 1.** Ban hành kèm theo Quyết định này Quy chế thanh toán.

***Điều 2.*** Quyết định này có hiệu lực thi hành.
`

	roots := ParseSections(md)
	if len(roots) != 2 {
		t.Fatalf("roots = %d, want 2", len(roots))
	}
	if roots[0].CitationPath != "dieu-1" {
		t.Errorf("roots[0].CitationPath = %q, want dieu-1", roots[0].CitationPath)
	}
	if roots[0].Heading != "Ban hành kèm theo Quyết định này Quy chế thanh toán." {
		t.Errorf("roots[0].Heading = %q", roots[0].Heading)
	}
	if roots[1].CitationPath != "dieu-2" {
		t.Errorf("roots[1].CitationPath = %q, want dieu-2", roots[1].CitationPath)
	}
}

func TestParseSectionsWhitespaceInsensitiveArticleLabel(t *testing.T) {
	md := `
Điều  1.  Sửa đổi, bổ sung một số điều

1. Sửa đổi tên văn bản.
`

	roots := ParseSections(md)
	if len(roots) != 1 {
		t.Fatalf("roots = %d, want 1", len(roots))
	}
	if roots[0].Heading != "Sửa đổi, bổ sung một số điều" {
		t.Fatalf("heading = %q, want stripped article label", roots[0].Heading)
	}
	if strings.Contains(roots[0].Heading, "Điều") {
		t.Fatalf("heading still contains article label: %q", roots[0].Heading)
	}
	if len(roots[0].Children) != 1 || roots[0].Children[0].Content != "Sửa đổi tên văn bản." {
		t.Fatalf("children = %#v, want one clean clause", roots[0].Children)
	}
}

func TestParseSectionsBoldNumberedLinesInsideArticleAreClauses(t *testing.T) {
	md := `
**Điều 1.** Phân công công tác

**1. Thống đốc.**

Chỉ đạo chung.

**2. Phó Thống đốc.**

Giúp Thống đốc.
`

	roots := ParseSections(md)
	if len(roots) != 1 {
		t.Fatalf("roots = %d, want 1", len(roots))
	}
	if len(roots[0].Children) != 2 {
		t.Fatalf("children = %d, want 2 clauses: %#v", len(roots[0].Children), roots[0].Children)
	}
	for i, child := range roots[0].Children {
		if child.Kind != "khoan" {
			t.Fatalf("child %d kind = %q, want khoan", i, child.Kind)
		}
	}
}

func TestParseSectionsKeepsQuotedAmendmentInsideSourceClause(t *testing.T) {
	md := `
Điều 1. Sửa đổi, bổ sung một số điều

1. Bổ sung Điều 4a vào sau Điều 4 như sau:

“Điều 4a. Áp dụng biện pháp

1. Việc áp dụng biện pháp thực hiện theo quy định.

a) Trường hợp thứ nhất;

b) Trường hợp thứ hai.”.

2. Sửa đổi khoản 1 Điều 5.
`

	roots := ParseSections(md)
	if len(roots) != 1 {
		t.Fatalf("roots = %d, want source document article only", len(roots))
	}
	if roots[0].Heading != "Sửa đổi, bổ sung một số điều" {
		t.Fatalf("heading = %q", roots[0].Heading)
	}
	if len(roots[0].Children) != 2 {
		t.Fatalf("source clauses = %d, want 2: %#v", len(roots[0].Children), roots[0].Children)
	}
	first := roots[0].Children[0]
	if first.Kind != "khoan" || first.Ordinal != 1 {
		t.Fatalf("first child = %#v, want source clause 1", first)
	}
	for _, want := range []string{"Điều 4a. Áp dụng biện pháp", "1. Việc áp dụng", "a) Trường hợp thứ nhất"} {
		if !strings.Contains(first.Content, want) {
			t.Fatalf("quoted amendment content missing %q in %q", want, first.Content)
		}
	}
}

func TestParseSectionsDoesNotPromoteWrappedArticleReferences(t *testing.T) {
	md := `
Điều 3. Bãi bỏ một số điều

Bãi bỏ Điều 31; Điều 32;

Điều 33; khoản 1 Điều 34; các khoản 1 và 2 Điều 35;

Điều 103 Nghị định số 15/2020/NĐ-CP.
`

	roots := ParseSections(md)
	if len(roots) != 1 {
		t.Fatalf("roots = %d, want only source article: %#v", len(roots), roots)
	}
	if roots[0].Label != "Điều 3" {
		t.Fatalf("label = %q, want Điều 3", roots[0].Label)
	}
	for _, want := range []string{"Điều 33; khoản 1", "Điều 103 Nghị định"} {
		if !strings.Contains(roots[0].Content, want) {
			t.Fatalf("article reference missing %q in %q", want, roots[0].Content)
		}
	}
}

func TestParseSections_atxMarkdownHeadings(t *testing.T) {
	md := `
# Chương I QUY ĐỊNH CHUNG

### Điều 1. Phạm vi điều chỉnh

1. Khoản một.
`

	roots := ParseSections(md)
	if len(roots) != 1 || roots[0].CitationPath != "chuong-I" {
		t.Fatalf("roots = %#v, want one chuong-I", roots)
	}
	if len(roots[0].Children) != 1 {
		t.Fatalf("children = %d, want 1", len(roots[0].Children))
	}
	dieu := roots[0].Children[0]
	if dieu.CitationPath != "chuong-I/dieu-1" {
		t.Errorf("dieu path = %q, want chuong-I/dieu-1", dieu.CitationPath)
	}
	if len(dieu.Children) != 1 || dieu.Children[0].Kind != "khoan" {
		t.Fatalf("dieu children = %#v, want one khoan", dieu.Children)
	}
}

func TestParseSections_numericBoldHeadingsAsArticles(t *testing.T) {
	md := `
**Chương I**

**QUY ĐỊNH CHUNG**

1. **Phạm vi điều chỉnh**

1. Thông tư này quy định một nội dung.

a) Một điểm.

1. **Đối tượng áp dụng**

1. Tổ chức tín dụng.
`

	roots := ParseSections(md)
	if len(roots) != 1 {
		t.Fatalf("roots = %d, want 1", len(roots))
	}
	if len(roots[0].Children) != 2 {
		t.Fatalf("articles = %d, want 2", len(roots[0].Children))
	}
	d1 := roots[0].Children[0]
	if d1.CitationPath != "chuong-I/dieu-1" || d1.Heading != "Phạm vi điều chỉnh" {
		t.Fatalf("first article path/heading = %q/%q", d1.CitationPath, d1.Heading)
	}
	if len(d1.Children) != 1 || d1.Children[0].Kind != "khoan" {
		t.Fatalf("first article children = %#v, want one khoan", d1.Children)
	}
	d2 := roots[0].Children[1]
	if d2.CitationPath != "chuong-I/dieu-2" || d2.Heading != "Đối tượng áp dụng" {
		t.Fatalf("second article path/heading = %q/%q", d2.CitationPath, d2.Heading)
	}
}

func TestParseSections_numericPlainArticleHeadings(t *testing.T) {
	md := `
**Chương I**

**NHỮNG QUY ĐỊNH CHUNG**

1. Phạm vi điều chỉnh

Nghị định này quy định về giao dịch điện tử.

1. Đối tượng áp dụng

Nghị định này áp dụng đối với cơ quan, tổ chức, cá nhân.

1. Giải thích từ ngữ

Trong Nghị định này, các từ ngữ dưới đây được hiểu như sau:

1. Quản trị nội bộ trên môi trường điện tử là việc xử lý công việc nội bộ.
`

	roots := ParseSections(md)
	if len(roots) != 1 {
		t.Fatalf("roots = %d, want 1", len(roots))
	}
	if len(roots[0].Children) != 3 {
		t.Fatalf("articles = %d, want 3", len(roots[0].Children))
	}
	if roots[0].Children[0].Heading != "Phạm vi điều chỉnh" {
		t.Errorf("first heading = %q", roots[0].Children[0].Heading)
	}
	if roots[0].Children[2].Heading != "Giải thích từ ngữ" {
		t.Errorf("third heading = %q", roots[0].Children[2].Heading)
	}
	if len(roots[0].Children[2].Children) != 1 || roots[0].Children[2].Children[0].Kind != "khoan" {
		t.Fatalf("definition clause = %#v, want one khoan", roots[0].Children[2].Children)
	}
}

func TestParseSections_legacyOutlineHeadings(t *testing.T) {
	md := `
**I - VẬN DỤNG CÁC TIÊU CHUẨN ĐỂ PHÂN LOẠI XÍ NGHIỆP**

**A. Việc tổ chức thực hiện thanh toán và cho vay**

1. Các cơ quan ngân hàng thực hiện nghiệp vụ theo đúng chế độ.

a) Tiêu chuẩn 1.

b/ Tiêu chuẩn 2.
`

	roots := ParseSections(md)
	if len(roots) != 1 || roots[0].Kind != "chuong" {
		t.Fatalf("roots = %#v, want one legacy chuong", roots)
	}
	if len(roots[0].Children) != 1 || roots[0].Children[0].Kind != "muc" {
		t.Fatalf("legacy children = %#v, want one muc", roots[0].Children)
	}
	muc := roots[0].Children[0]
	if len(muc.Children) != 1 || muc.Children[0].Kind != "khoan" {
		t.Fatalf("muc children = %#v, want one khoan", muc.Children)
	}
	if len(muc.Children[0].Children) != 2 {
		t.Fatalf("points = %d, want 2", len(muc.Children[0].Children))
	}
	if muc.Children[0].Children[1].CitationPath != "chuong-I/muc-A/khoan-1/diem-b" {
		t.Errorf("second point path = %q", muc.Children[0].Children[1].CitationPath)
	}
}

func TestParseSections_fullyWrappedNumericHeadings(t *testing.T) {
	md := `
**1. Về mở và sử dụng tài khoản (điều 34):**

- Các xí nghiệp quốc doanh có quyền lựa chọn ngân hàng.

**2. Về quan hệ tín dụng:**

- Ngân hàng thực hiện theo quy định.
`

	roots := ParseSections(md)
	if len(roots) != 2 {
		t.Fatalf("roots = %d, want 2", len(roots))
	}
	if roots[0].CitationPath != "dieu-1" || roots[1].CitationPath != "dieu-2" {
		t.Fatalf("paths = %q/%q, want dieu-1/dieu-2", roots[0].CitationPath, roots[1].CitationPath)
	}
}

func TestParseSections_repeatedNumberedClausesUnderChapterStayClauses(t *testing.T) {
	md := `
Chương I
QUY ĐỊNH CHUNG

Điều 1. Sửa đổi

1. Sửa đổi một nội dung.

2. Sửa đổi nội dung khác.

1. Trong nội dung được thay thế, khoản này xuất hiện lại.

**1. Khoản in đậm vẫn là khoản.**
`

	roots := ParseSections(md)
	if len(roots) != 1 || len(roots[0].Children) != 1 {
		t.Fatalf("tree = %#v, want one article under chapter", roots)
	}
	dieu := roots[0].Children[0]
	if len(dieu.Children) != 4 {
		t.Fatalf("clauses = %d, want 4", len(dieu.Children))
	}
	for _, child := range dieu.Children {
		if child.Kind != "khoan" {
			t.Fatalf("child kind = %q, want khoan", child.Kind)
		}
	}
	if dieu.Children[2].CitationPath != "chuong-I/dieu-1/khoan-1~2" {
		t.Errorf("third clause path = %q", dieu.Children[2].CitationPath)
	}
	if dieu.Children[3].Heading != "" || dieu.Children[3].Content != "Khoản in đậm vẫn là khoản." {
		t.Errorf("bold clause heading/content = %q/%q", dieu.Children[3].Heading, dieu.Children[3].Content)
	}
}

func TestParseSections_numberedOutlineFallback(t *testing.T) {
	md := `
NGÂN HÀNG NHÀ NƯỚC

THÔNG TƯ

Ngày 25-11-1993, Chính phủ đã ban hành Nghị định.

1. Số liệu ở tài khoản được cung cấp trong các trường hợp sau đây:

1.1. Theo yêu cầu của chủ tài khoản.

1.2. Theo quy định của cơ quan có thẩm quyền.

2. Thông tư này có hiệu lực thi hành kể từ ngày ký.
`

	roots := ParseSections(md)
	if len(roots) != 2 {
		t.Fatalf("roots = %d, want 2 numbered outline roots", len(roots))
	}
	if roots[0].Kind != "dieu" || roots[0].Label != "1." {
		t.Fatalf("first root = %s/%s, want dieu/1.", roots[0].Kind, roots[0].Label)
	}
	if len(roots[0].Children) != 2 {
		t.Fatalf("first root children = %d, want 2", len(roots[0].Children))
	}
	if roots[0].Children[1].CitationPath != "dieu-outline-1/khoan-outline-1-2" {
		t.Fatalf("second child path = %q", roots[0].Children[1].CitationPath)
	}
}

func TestParseSections_supplementOnlyDoesNotUseNumberedFallback(t *testing.T) {
	md := `
**BÁO CÁO TÌNH HÌNH THỰC HIỆN CƠ CẤU LẠI THỜI HẠN TRẢ NỢ**

1. Tình hình thực hiện cơ cấu lại thời hạn trả nợ.

2. Tổng dư nợ không bị chuyển sang nhóm nợ xấu.
`

	if roots := ParseSections(md); len(roots) != 0 {
		t.Fatalf("roots = %#v, want no synthetic legal sections for supplement text", roots)
	}
}

func TestParseSections_wholeDocumentFallback(t *testing.T) {
	md := `
NGÂN HÀNG NHÀ NƯỚC VIỆT NAM

THÔNG TƯ LIÊN BỘ

Về việc hướng dẫn một số nội dung nghiệp vụ ngân hàng trong thời kỳ chuyển tiếp.

Căn cứ chức năng, nhiệm vụ của các cơ quan quản lý nhà nước, văn bản này hướng dẫn
các ngân hàng thương mại, tổ chức tín dụng và các đơn vị có liên quan thực hiện
thống nhất việc mở tài khoản, hạch toán, thanh toán và báo cáo định kỳ.

Các đơn vị phải tổ chức thực hiện nghiêm túc, kịp thời phản ánh khó khăn vướng mắc
về Ngân hàng Nhà nước Việt Nam để tổng hợp, xem xét và xử lý theo thẩm quyền.
`

	roots := ParseSections(md)
	if len(roots) != 1 {
		t.Fatalf("roots = %d, want one whole-document fallback", len(roots))
	}
	if roots[0].Kind != "dieu" || roots[0].Label != "Toàn văn" || roots[0].CitationPath != "toan-van" {
		t.Fatalf("fallback root = %s/%s/%s", roots[0].Kind, roots[0].Label, roots[0].CitationPath)
	}
	if !strings.Contains(roots[0].Content, "hạch toán, thanh toán") {
		t.Fatalf("fallback content missing body: %q", roots[0].Content)
	}
}

func TestParseSections_tree(t *testing.T) {
	roots := ParseSections(viSnippet)

	// Top level: two Chương.
	if len(roots) != 2 {
		t.Fatalf("expected 2 root Chương, got %d: %v", len(roots), rootKinds(roots))
	}

	ch1 := roots[0]
	if ch1.Kind != "chuong" {
		t.Errorf("roots[0].Kind = %q, want chuong", ch1.Kind)
	}
	if ch1.Ordinal != 1 {
		t.Errorf("roots[0].Ordinal = %d, want 1", ch1.Ordinal)
	}
	if ch1.CitationPath != "chuong-I" {
		t.Errorf("roots[0].CitationPath = %q, want chuong-I", ch1.CitationPath)
	}
	if ch1.Heading != "QUY ĐỊNH CHUNG" {
		t.Errorf("roots[0].Heading = %q, want QUY ĐỊNH CHUNG", ch1.Heading)
	}

	// Chương I has 2 Điều.
	if len(ch1.Children) != 2 {
		t.Fatalf("Chương I: expected 2 Điều, got %d", len(ch1.Children))
	}

	d1 := ch1.Children[0]
	if d1.Kind != "dieu" {
		t.Errorf("Điều 1.Kind = %q, want dieu", d1.Kind)
	}
	if d1.Ordinal != 1 {
		t.Errorf("Điều 1.Ordinal = %d, want 1", d1.Ordinal)
	}
	if d1.CitationPath != "chuong-I/dieu-1" {
		t.Errorf("Điều 1.CitationPath = %q, want chuong-I/dieu-1", d1.CitationPath)
	}
	if d1.Heading != "Phạm vi điều chỉnh" {
		t.Errorf("Điều 1.Heading = %q, want 'Phạm vi điều chỉnh'", d1.Heading)
	}

	d2 := ch1.Children[1]
	if d2.Kind != "dieu" {
		t.Errorf("Điều 2.Kind = %q, want dieu", d2.Kind)
	}
	if d2.CitationPath != "chuong-I/dieu-2" {
		t.Errorf("Điều 2.CitationPath = %q, want chuong-I/dieu-2", d2.CitationPath)
	}

	// Điều 2 has 2 Khoản.
	if len(d2.Children) != 2 {
		t.Fatalf("Điều 2: expected 2 Khoản, got %d", len(d2.Children))
	}
	k1 := d2.Children[0]
	if k1.Kind != "khoan" {
		t.Errorf("Khoản 1.Kind = %q, want khoan", k1.Kind)
	}
	if k1.Ordinal != 1 {
		t.Errorf("Khoản 1.Ordinal = %d, want 1", k1.Ordinal)
	}
	if k1.CitationPath != "chuong-I/dieu-2/khoan-1" {
		t.Errorf("Khoản 1.CitationPath = %q, want chuong-I/dieu-2/khoan-1", k1.CitationPath)
	}

	k2 := d2.Children[1]
	if k2.CitationPath != "chuong-I/dieu-2/khoan-2" {
		t.Errorf("Khoản 2.CitationPath = %q, want chuong-I/dieu-2/khoan-2", k2.CitationPath)
	}

	// Khoản 2 has 2 Điểm.
	if len(k2.Children) != 2 {
		t.Fatalf("Khoản 2: expected 2 Điểm, got %d", len(k2.Children))
	}
	da := k2.Children[0]
	if da.Kind != "diem" {
		t.Errorf("Điểm a.Kind = %q, want diem", da.Kind)
	}
	if da.CitationPath != "chuong-I/dieu-2/khoan-2/diem-a" {
		t.Errorf("Điểm a.CitationPath = %q, want chuong-I/dieu-2/khoan-2/diem-a", da.CitationPath)
	}
	db := k2.Children[1]
	if db.CitationPath != "chuong-I/dieu-2/khoan-2/diem-b" {
		t.Errorf("Điểm b.CitationPath = %q, want chuong-I/dieu-2/khoan-2/diem-b", db.CitationPath)
	}

	// Chương II.
	ch2 := roots[1]
	if ch2.CitationPath != "chuong-II" {
		t.Errorf("Chương II.CitationPath = %q, want chuong-II", ch2.CitationPath)
	}
	if ch2.Ordinal != 2 {
		t.Errorf("Chương II.Ordinal = %d, want 2", ch2.Ordinal)
	}

	// Điều 3 under Chương II.
	if len(ch2.Children) != 1 {
		t.Fatalf("Chương II: expected 1 Điều, got %d", len(ch2.Children))
	}
	d3 := ch2.Children[0]
	if d3.CitationPath != "chuong-II/dieu-3" {
		t.Errorf("Điều 3.CitationPath = %q, want chuong-II/dieu-3", d3.CitationPath)
	}
	if len(d3.Children) != 2 {
		t.Fatalf("Điều 3: expected 2 Khoản, got %d", len(d3.Children))
	}
}

// TestParseSections_citationPaths verifies that every section in the tree has
// a unique citation_path (the chunk dedup key).
func TestParseSections_citationPaths(t *testing.T) {
	roots := ParseSections(viSnippet)
	seen := make(map[string]bool)
	var walk func([]Section)
	walk = func(sections []Section) {
		for _, s := range sections {
			if seen[s.CitationPath] {
				t.Errorf("duplicate citation_path: %q", s.CitationPath)
			}
			seen[s.CitationPath] = true
			walk(s.Children)
		}
	}
	walk(roots)
}

func TestParseSections_duplicateCitationPathsAreDisambiguated(t *testing.T) {
	roots := ParseSections(`
Điều 1. Sửa đổi
1. Sửa đổi một nội dung.
2. Sửa đổi nội dung khác.
1. Trong nội dung được thay thế, khoản này xuất hiện lại.
a) Điểm trong phần được thay thế.
a) Điểm lặp lại trong phần được thay thế.
`)

	if len(roots) != 1 {
		t.Fatalf("roots = %d, want 1", len(roots))
	}
	var paths []string
	var walk func([]Section)
	walk = func(sections []Section) {
		for _, s := range sections {
			paths = append(paths, s.CitationPath)
			walk(s.Children)
		}
	}
	walk(roots)

	want := []string{
		"dieu-1",
		"dieu-1/khoan-1",
		"dieu-1/khoan-2",
		"dieu-1/khoan-1~2",
		"dieu-1/khoan-1~2/diem-a",
		"dieu-1/khoan-1~2/diem-a~2",
	}
	if len(paths) != len(want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Errorf("paths[%d] = %q, want %q", i, paths[i], want[i])
		}
	}
}

func TestDiemLetterOrdinalVietnameseOrder(t *testing.T) {
	tests := map[string]int{
		"d": 4,
		"đ": 5,
		"e": 6,
		"g": 7,
	}
	for letter, want := range tests {
		if got := diemLetterOrdinal(letter); got != want {
			t.Fatalf("diemLetterOrdinal(%q) = %d, want %d", letter, got, want)
		}
	}
}

// TestParseSections_flatDoc tests a document with no Chương hierarchy —
// just top-level Điều (common in shorter thông tư).
func TestParseSections_flatDoc(t *testing.T) {
	text := `

Điều 1. Mục đích
Quy định mục đích sử dụng.

Điều 2. Phạm vi
Áp dụng cho toàn quốc.
`
	roots := ParseSections(text)
	if len(roots) != 2 {
		t.Fatalf("flat doc: expected 2 Điều, got %d", len(roots))
	}
	if roots[0].CitationPath != "dieu-1" {
		t.Errorf("roots[0].CitationPath = %q, want dieu-1", roots[0].CitationPath)
	}
	if roots[1].CitationPath != "dieu-2" {
		t.Errorf("roots[1].CitationPath = %q, want dieu-2", roots[1].CitationPath)
	}
}

// countAllKinds counts nodes by Kind across the whole tree.
func countAllKinds(secs []Section, m map[string]int) {
	for _, s := range secs {
		m[s.Kind]++
		countAllKinds(s.Children, m)
	}
}

// TestParseSections_realFailureModes pins the failure modes found on real SBV
// circulars (09/2020/TT-NHNN). Each case is a regression guard for the
// line-level, engine-uniform rewrite. Counts are checked exactly (default 0).
func TestParseSections_realFailureModes(t *testing.T) {
	tests := []struct {
		name                      string
		md                        string
		chuong, dieu, khoan, diem int
		check                     func(t *testing.T, roots []Section)
	}{
		{
			// The headline bug: clauses are written "1."/"2." (not "Khoản 1") and
			// points "a)"/"b)" (not "Điểm a") with no blank line — these were all
			// swallowed by the old block parser.
			name: "clauses_as_N_dot_and_points_as_letter",
			md:   "Điều 5. Phân loại\n1. Nhóm một.\n2. Nhóm hai.\na) Điểm a.\nb) Điểm b.\n",
			dieu: 1, khoan: 2, diem: 2,
		},
		{
			// PDF extraction emits no boundary blank lines at all.
			name: "no_blank_lines_pdf_style",
			md:   "Điều 1. A\nNội dung.\nĐiều 2. B\n1. x\n2. y\nĐiều 3. C\n",
			dieu: 3, khoan: 2,
		},
		{
			// Real DOCX writes "Chương<NBSP>II" with a no-break space.
			name:   "nbsp_in_chuong_heading",
			md:     "Chương\u00a0II\nQUY ĐỊNH\nĐiều 1. A\n",
			chuong: 1, dieu: 1,
		},
		{
			// "Chương trình" (a programme) must not be parsed as a Chương.
			name: "chuong_trinh_is_not_a_chuong",
			md:   "Điều 1. Phê duyệt\nChương trình được triển khai theo kế hoạch.\n",
			dieu: 1,
		},
		{
			// A mid-sentence cross-reference must not create an Điều node.
			name: "cross_reference_not_a_heading",
			md:   "Điều 1. Sửa đổi\nbãi bỏ khoản 3 Điều 12 của Nghị định.\n",
			dieu: 1,
		},
		{
			// An inserted article keeps its letter suffix in the citation path.
			name: "dieu_letter_suffix",
			md:   "Điều 21b. Bổ sung\n1. Nội dung mới.\n",
			dieu: 1, khoan: 1,
			check: func(t *testing.T, roots []Section) {
				if roots[0].CitationPath != "dieu-21b" {
					t.Errorf("CitationPath = %q, want dieu-21b", roots[0].CitationPath)
				}
			},
		},
		{
			// A "1." outside any Điều (preamble) is body text, not a Khoản.
			name: "numbered_line_outside_dieu_is_not_khoan",
			md:   "Số: 09/2020/TT-NHNN\n1. Căn cứ Luật Ngân hàng Nhà nước.\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			roots := ParseSections(tt.md)
			m := map[string]int{}
			countAllKinds(roots, m)
			if m["chuong"] != tt.chuong {
				t.Errorf("chuong = %d, want %d", m["chuong"], tt.chuong)
			}
			if m["dieu"] != tt.dieu {
				t.Errorf("dieu = %d, want %d", m["dieu"], tt.dieu)
			}
			if m["khoan"] != tt.khoan {
				t.Errorf("khoan = %d, want %d", m["khoan"], tt.khoan)
			}
			if m["diem"] != tt.diem {
				t.Errorf("diem = %d, want %d", m["diem"], tt.diem)
			}
			if tt.check != nil {
				tt.check(t, roots)
			}
		})
	}
}

// TestStatusCodeToClass verifies the status code → class mapping.
func TestStatusCodeToClass(t *testing.T) {
	cases := []struct {
		code  string
		class string
	}{
		{"CHL", "in_force"},
		{"chl", "in_force"},
		{"HHL", "expired"},
		{"HHL1P", "partial"},
		{"CCHL", "not_yet"},
		{"TNHL", "not_yet"},
		{"TDHL", "suspended"},
		{"", "in_force"},
		{"UNKNOWN", "in_force"},
	}
	for _, tc := range cases {
		got := statusCodeToClass(tc.code)
		if got != tc.class {
			t.Errorf("statusCodeToClass(%q) = %q, want %q", tc.code, got, tc.class)
		}
	}
}

// TestBuildPrefix verifies the contextual prefix format.
func TestBuildPrefix(t *testing.T) {
	p := buildPrefix("11/2026/TT-NHNN", "Thông tư 11", "Chương I QUY ĐỊNH CHUNG", "", "01/01/2026")
	if p == "" {
		t.Fatal("buildPrefix returned empty string")
	}
	// Must contain the document number.
	if !contains(p, "11/2026/TT-NHNN") {
		t.Errorf("prefix missing doc number: %q", p)
	}
	// Must contain the effective date.
	if !contains(p, "01/01/2026") {
		t.Errorf("prefix missing effective date: %q", p)
	}
}

// TestRoughTokenCount sanity-checks the estimator.
func TestRoughTokenCount(t *testing.T) {
	if roughTokenCount("") != 0 {
		t.Error("empty string should give 0 tokens")
	}
	// A non-empty string should give at least 1.
	if roughTokenCount("a") < 1 {
		t.Error("single char should give >= 1 token")
	}
	// More chars → more tokens.
	short := roughTokenCount("Điều 1")
	long := roughTokenCount("Điều 1 Phạm vi điều chỉnh Thông tư này quy định")
	if long <= short {
		t.Errorf("longer text should give more tokens: short=%d, long=%d", short, long)
	}
}

// ---- helpers ----------------------------------------------------------------

func rootKinds(secs []Section) []string {
	out := make([]string, len(secs))
	for i, s := range secs {
		out[i] = s.Kind
	}
	return out
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
