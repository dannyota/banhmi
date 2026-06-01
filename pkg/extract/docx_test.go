package extract

import (
	"strings"
	"testing"
)

func TestParseDocxBody_TablesAndHeadings(t *testing.T) {
	const body = `<?xml version="1.0"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>
<w:p><w:r><w:t>Quy định chung</w:t></w:r></w:p>
<w:p><w:r><w:t xml:space="preserve">Điều 1. Phạm vi điều chỉnh</w:t></w:r></w:p>
<w:tbl>
  <w:tr>
    <w:tc><w:p><w:r><w:t>STT</w:t></w:r></w:p></w:tc>
    <w:tc><w:p><w:r><w:t>Nhiệm vụ</w:t></w:r></w:p></w:tc>
  </w:tr>
  <w:tr>
    <w:tc><w:p><w:r><w:t>1</w:t></w:r></w:p></w:tc>
    <w:tc><w:p><w:r><w:t>Báo cáo</w:t></w:r></w:p></w:tc>
  </w:tr>
</w:tbl>
</w:body></w:document>`

	got, err := parseDocxBody(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parseDocxBody: %v", err)
	}
	for _, want := range []string{
		"| STT | Nhiệm vụ |", // table header rendered as Markdown
		"| --- | --- |",      // separator row
		"| 1 | Báo cáo |",    // data row
		"\n\nĐiều 1.",        // blank line before the Điều heading (chunk boundary)
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q in:\n%s", want, got)
		}
	}
}

// gridSpan must widen a header cell so it aligns with the data columns below it.
func TestParseDocxBody_GridSpan(t *testing.T) {
	const body = `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>
<w:tbl>
  <w:tr>
    <w:tc><w:tcPr><w:gridSpan w:val="2"/></w:tcPr><w:p><w:r><w:t>Tiêu đề</w:t></w:r></w:p></w:tc>
  </w:tr>
  <w:tr>
    <w:tc><w:p><w:r><w:t>A</w:t></w:r></w:p></w:tc>
    <w:tc><w:p><w:r><w:t>B</w:t></w:r></w:p></w:tc>
  </w:tr>
</w:tbl>
</w:body></w:document>`

	got, err := parseDocxBody(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parseDocxBody: %v", err)
	}
	if !strings.Contains(got, "| Tiêu đề |  |") {
		t.Errorf("gridSpan=2 header not padded to 2 columns:\n%q", got)
	}
	if !strings.Contains(got, "| A | B |") {
		t.Errorf("missing data row:\n%q", got)
	}
}

// Plain prose without tables still extracts as before (newline per paragraph).
func TestParseDocxBody_PlainParagraphs(t *testing.T) {
	const body = `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>
<w:p><w:r><w:t>Dòng một</w:t></w:r></w:p>
<w:p><w:r><w:t>Dòng hai</w:t></w:r></w:p>
</w:body></w:document>`

	got, err := parseDocxBody(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parseDocxBody: %v", err)
	}
	if !strings.Contains(got, "Dòng một\n") || !strings.Contains(got, "Dòng hai\n") {
		t.Errorf("plain paragraphs not preserved:\n%q", got)
	}
}
