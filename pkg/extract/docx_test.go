package extract

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"strings"
	"testing"
)

func TestDOCX_TablesAndHeadings(t *testing.T) {
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

	got, err := DOCX(makeCompatibilityDOCX(t, body))
	if err != nil {
		t.Fatalf("DOCX: %v", err)
	}
	for _, want := range []string{
		"| STT | Nhiệm vụ |",
		"| --- | --- |",
		"| 1 | Báo cáo |",
		"\n\nĐiều 1.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q in:\n%s", want, got)
		}
	}
	const want = "Quy định chung\n\nĐiều 1. Phạm vi điều chỉnh\n\n| STT | Nhiệm vụ |\n| --- | --- |\n| 1 | Báo cáo |\n\n"
	if got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
}

// gridSpan must widen a header cell so it aligns with the data columns below it.
func TestDOCX_GridSpan(t *testing.T) {
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

	got, err := DOCX(makeCompatibilityDOCX(t, body))
	if err != nil {
		t.Fatalf("DOCX: %v", err)
	}
	if !strings.Contains(got, "| Tiêu đề |  |") {
		t.Errorf("gridSpan=2 header not padded to 2 columns:\n%q", got)
	}
	if !strings.Contains(got, "| A | B |") {
		t.Errorf("missing data row:\n%q", got)
	}
	const want = "\n| Tiêu đề |  |\n| --- | --- |\n| A | B |\n\n"
	if got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
}

// Plain prose without tables still extracts as before (newline per paragraph).
func TestDOCX_PlainParagraphs(t *testing.T) {
	const body = `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>
<w:p><w:r><w:t>Dòng một</w:t></w:r></w:p>
<w:p><w:r><w:t>Dòng hai</w:t></w:r></w:p>
</w:body></w:document>`

	got, err := DOCX(makeCompatibilityDOCX(t, body))
	if err != nil {
		t.Fatalf("DOCX: %v", err)
	}
	if !strings.Contains(got, "Dòng một\n") || !strings.Contains(got, "Dòng hai\n") {
		t.Errorf("plain paragraphs not preserved:\n%q", got)
	}
	const want = "Dòng một\nDòng hai\n"
	if got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
}

func makeCompatibilityDOCX(t *testing.T, body string) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	w, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestDOCXHeadingCompatibility(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{"vn", "Điều 1", "\nĐiều 1\n"},
		{"my", "Section 1", "Section 1\n"},
		{"sg", "Section 2", "Section 2\n"},
		{"id", "Pasal 1", "Pasal 1\n"},
		{"th", "มาตรา 1", "มาตรา 1\n"},
		{"kh", "Article 1", "Article 1\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := "<document><body><p><r><t>" + tc.text + "</t></r></p></body></document>"
			got, err := DOCX(makeCompatibilityDOCX(t, body))
			if err != nil || got != tc.want {
				t.Fatalf("DOCX = (%q, %v), want (%q, nil)", got, err, tc.want)
			}
		})
	}
}

func TestDOCXExactParagraphs(t *testing.T) {
	cases := []struct {
		name   string
		first  string
		second string
		want   string
	}{
		{"heading", "Quy định chung", "Điều 1. Phạm vi điều chỉnh", "Quy định chung\n\nĐiều 1. Phạm vi điều chỉnh\n"},
		{"normalized-heading", "A", " \tĐie\u0302\u0300u 1", "A\n\n \tĐiều 1\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := "<document><body><p><r><t>" + tc.first + "</t></r></p><p><r><t>" + tc.second + "</t></r></p></body></document>"
			got, err := DOCX(makeCompatibilityDOCX(t, body))
			if err != nil || got != tc.want {
				t.Fatalf("DOCX = (%q, %v), want (%q, nil)", got, err, tc.want)
			}
		})
	}
}

func TestDOCXErrors(t *testing.T) {
	t.Run("invalid-zip", func(t *testing.T) {
		got, err := DOCX([]byte("invalid"))
		if got != "" || !errors.Is(err, zip.ErrFormat) || !strings.HasPrefix(err.Error(), "open docx zip: ") {
			t.Fatalf("DOCX = (%q, %v), want wrapped zip.ErrFormat", got, err)
		}
	})
	t.Run("missing-body", func(t *testing.T) {
		var b bytes.Buffer
		zw := zip.NewWriter(&b)
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		got, err := DOCX(b.Bytes())
		if got != "" || err == nil || err.Error() != "docx: word/document.xml not found" {
			t.Fatalf("DOCX = (%q, %v), want missing-body error", got, err)
		}
	})
	for _, tc := range []struct {
		name   string
		body   string
		prefix string
	}{
		{"malformed-body", "<document><body><p>", "parse docx body: "},
		{"malformed-text", "<document><body><p><t>", "decode w:t: "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DOCX(makeCompatibilityDOCX(t, tc.body))
			var syntaxErr *xml.SyntaxError
			if got != "" || !errors.As(err, &syntaxErr) || !strings.HasPrefix(err.Error(), tc.prefix) {
				t.Fatalf("DOCX = (%q, %v), want %q and XML syntax error", got, err, tc.prefix)
			}
		})
	}
}
