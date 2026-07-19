package vanban

import (
	"testing"
)

// listFixture mirrors the live grvDocument table structure (verified 2026-06):
// each row has span.code (số ký hiệu, sometimes with a "NN." grid-sequence prefix),
// span.issued-date, span.substract (trích yếu), the detail docid in the anchor, and
// an optional CDN file link. A trailing pager exposes Page$2.
const listFixture = `
<table id="ctrl_191017_163_grvDocument" class="grid">
<tr class="grid-header"><th>Số ký hiệu</th><th>Ngày</th><th>Trích yếu</th></tr>
<tr>
 <td><a href='/?pageid=27160&docid=216334&classid=1'><span class="code">134/2025/QH15</span><span class="issue-v2">10/12/2025</span></a></td>
 <td><span class="issued-date">10/12/2025</span></td>
 <td><a href='/?pageid=27160&docid=216334&classid=1'><span class="substract">Luật Trí tuệ nhân tạo</span></a>
   <div class="bl-doc-files"><div class="bl-doc-file"><a href="https://datafiles.chinhphu.vn/cpp/files/vbpq/2026/01/luat134.signed.pdf" target="_blank" download>Tài liệu đính kèm</a></div></div></td>
</tr>
<tr>
 <td><a href='/?pageid=27160&docid=216499&classid=1'><span class="code">66.116/2025/QH15</span><span class="issue-v2">10/12/2025</span></a></td>
 <td><span class="issued-date">10/12/2025</span></td>
 <td><a href='/?pageid=27160&docid=216499&classid=1'><span class="substract">Luật An ninh mạng</span></a></td>
</tr>
</table>
<tr class="grid-pager"><td>
 <span>1</span>
 <a href="javascript:__doPostBack(&#39;ctrl_191017_163$grvDocument&#39;,&#39;Page$2&#39;)">2</a>
 <a href="javascript:__doPostBack(&#39;ctrl_191017_163$grvDocument&#39;,&#39;Page$3&#39;)">3</a>
</td></tr>`

func TestParseListRows(t *testing.T) {
	rows := parseListRows(listFixture, "https://vanban.chinhphu.vn")
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}

	ai := rows[0]
	if ai.Number != "134/2025/QH15" {
		t.Errorf("number = %q, want 134/2025/QH15", ai.Number)
	}
	if ai.Title != "Luật Trí tuệ nhân tạo" {
		t.Errorf("title = %q, want Luật Trí tuệ nhân tạo", ai.Title)
	}
	if ai.ExternalID != "216334" {
		t.Errorf("externalID = %q, want 216334", ai.ExternalID)
	}
	if ai.DocType != "Luật" {
		t.Errorf("docType = %q, want Luật", ai.DocType)
	}
	if got := dateString(ai.IssuedAt); got != "2025-12-10" {
		t.Errorf("issuedAt = %q, want 2025-12-10", got)
	}
	if ai.DetailURL != "https://vanban.chinhphu.vn/?pageid=27160&docid=216334&classid=1" {
		t.Errorf("detailURL = %q", ai.DetailURL)
	}

	// The "66." grid-sequence prefix must be stripped from the số ký hiệu.
	if rows[1].Number != "116/2025/QH15" {
		t.Errorf("row1 number = %q, want 116/2025/QH15 (prefix stripped)", rows[1].Number)
	}
}

func TestNextPage(t *testing.T) {
	target, next, ok := nextPage(listFixture, 1)
	if !ok {
		t.Fatal("nextPage(1) ok = false, want true")
	}
	if target != "ctrl_191017_163$grvDocument" {
		t.Errorf("target = %q", target)
	}
	if next != 2 {
		t.Errorf("next = %d, want 2", next)
	}
	// A page beyond the pager window stops the walk.
	if _, _, ok := nextPage(listFixture, 9); ok {
		t.Error("nextPage(9) ok = true, want false (no Page$10 offered)")
	}
}

const detailFixture = `
<div class="header">irrelevant <table><tr><td>nav</td><td>x</td></tr></table></div>
<div id="ctrl_190596_91_Content" class="Content">
 <table style="width: 100%">
  <tr><td class="col1" style="width: 135px">Số ký hiệu</td><td> 134/2025/QH15</td></tr>
  <tr><td class="col1">Ngày ban hành</td><td> 10-12-2025</td></tr>
  <tr id="ctrl_190596_91_tr_ngaycohieuluc"><td class="col1">Ngày có hiệu lực</td><td> 01-03-2026</td></tr>
  <tr><td class="col1">Loại văn bản</td><td> Luật</td></tr>
  <tr><td class="col1">Cơ quan ban hành</td><td> Quốc hội</td></tr>
  <tr><td class="col1">Người ký</td><td> Trần Thanh Mẫn</td></tr>
  <tr><td class="col1">Trích yếu</td><td> Luật Trí tuệ nhân tạo</td></tr>
  <tr><td class="col1">Tài liệu đính kèm</td><td><a href="https://datafiles.chinhphu.vn/cpp/files/vbpq/2026/01/luat134.signed.pdf" target="_blank" download>luat134.signed.pdf</a></td></tr>
 </table>
</div>`

func TestParseDetailPage(t *testing.T) {
	doc := parseDetailPage(detailFixture, "https://vanban.chinhphu.vn", "216334",
		"https://vanban.chinhphu.vn/?pageid=27160&docid=216334&classid=1")

	if doc.Number != "134/2025/QH15" {
		t.Errorf("number = %q", doc.Number)
	}
	if doc.DocType != "Luật" {
		t.Errorf("docType = %q, want Luật", doc.DocType)
	}
	if doc.Issuer != "Quốc hội" {
		t.Errorf("issuer = %q, want Quốc hội", doc.Issuer)
	}
	if doc.Signer != "Trần Thanh Mẫn" {
		t.Errorf("signer = %q", doc.Signer)
	}
	if doc.Title != "Luật Trí tuệ nhân tạo" {
		t.Errorf("title = %q", doc.Title)
	}
	if got := dateString(doc.IssuedAt); got != "2025-12-10" {
		t.Errorf("issuedAt = %q, want 2025-12-10", got)
	}
	if got := dateString(doc.EffectiveAt); got != "2026-03-01" {
		t.Errorf("effectiveAt = %q, want 2026-03-01", got)
	}
	if len(doc.Files) != 1 {
		t.Fatalf("files = %d, want 1", len(doc.Files))
	}
	f := doc.Files[0]
	if f.URL != "https://datafiles.chinhphu.vn/cpp/files/vbpq/2026/01/luat134.signed.pdf" {
		t.Errorf("file url = %q", f.URL)
	}
	if f.Ext != "pdf" || f.Name != "luat134.signed.pdf" {
		t.Errorf("file = %+v", f)
	}
}

func TestNormalizeLabelAndDocNumber(t *testing.T) {
	if got := normalizeLabel("Số ký hiệu"); got != "so ky hieu" {
		t.Errorf("normalizeLabel = %q, want so ky hieu", got)
	}
	if got := normalizeLabel("Cơ quan ban hành"); got != "co quan ban hanh" {
		t.Errorf("normalizeLabel = %q, want co quan ban hanh", got)
	}
	if got := cleanDocNumber("66.116/2025/QH15"); got != "116/2025/QH15" {
		t.Errorf("cleanDocNumber = %q, want 116/2025/QH15", got)
	}
	if got := cleanDocNumber("215/2026/NĐ-CP"); got != "215/2026/NĐ-CP" {
		t.Errorf("cleanDocNumber = %q, want unchanged", got)
	}
}
