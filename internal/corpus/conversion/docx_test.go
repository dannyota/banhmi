package conversion

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"strings"
	"testing"
)

func TestParseDocxBody_TablesAndHeadings(t *testing.T) {
	const body = `<?xml version="1.0"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>
<w:p><w:r><w:t>Quy định chung</w:t></w:r></w:p>
<w:p><w:r><w:t xml:space="preserve">HEADER 1. Scope</w:t></w:r></w:p>
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

	got, err := parseDocxBody(strings.NewReader(body), syntheticHeading)
	if err != nil {
		t.Fatalf("parseDocxBody: %v", err)
	}
	for _, want := range []string{
		"| STT | Nhiệm vụ |",
		"| --- | --- |",
		"| 1 | Báo cáo |",
		"\n\nHEADER 1.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q in:\n%s", want, got)
		}
	}
	const want = "Quy định chung\n\nHEADER 1. Scope\n\n| STT | Nhiệm vụ |\n| --- | --- |\n| 1 | Báo cáo |\n\n"
	if got != want {
		t.Fatalf("text = %q, want %q", got, want)
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

	got, err := parseDocxBody(strings.NewReader(body), syntheticHeading)
	if err != nil {
		t.Fatalf("parseDocxBody: %v", err)
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
func TestParseDocxBody_PlainParagraphs(t *testing.T) {
	const body = `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>
<w:p><w:r><w:t>Dòng một</w:t></w:r></w:p>
<w:p><w:r><w:t>Dòng hai</w:t></w:r></w:p>
</w:body></w:document>`

	got, err := parseDocxBody(strings.NewReader(body), syntheticHeading)
	if err != nil {
		t.Fatalf("parseDocxBody: %v", err)
	}
	if !strings.Contains(got, "Dòng một\n") || !strings.Contains(got, "Dòng hai\n") {
		t.Errorf("plain paragraphs not preserved:\n%q", got)
	}
	const want = "Dòng một\nDòng hai\n"
	if got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
}

func syntheticHeading(s string) bool {
	return strings.HasPrefix(s, "HEADER ")
}

func TestParseDocxBodyHeadingPolicy(t *testing.T) {
	body := "<document><body><p><t>A</t></p><p><t>HEADER 1</t></p></body></document>"
	for _, tc := range []struct {
		name    string
		matcher func(string) bool
		want    string
	}{
		{"nil", nil, "A\nHEADER 1\n"},
		{"injected", syntheticHeading, "A\n\nHEADER 1\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseDocxBody(strings.NewReader(body), tc.matcher)
			if err != nil || got != tc.want {
				t.Fatalf("parseDocxBody = (%q, %v), want (%q, nil)", got, err, tc.want)
			}
		})
	}
}

func TestParseDocxBodyNormalizedHeadingInput(t *testing.T) {
	body := "<document><body><p><t>A</t></p><p><t> \te\u0301 1</t></p></body></document>"
	var seen []string
	matcher := func(s string) bool {
		seen = append(seen, s)
		return s == "é 1"
	}
	got, err := parseDocxBody(strings.NewReader(body), matcher)
	const want = "A\n\n \té 1\n"
	if err != nil || got != want {
		t.Fatalf("parseDocxBody = (%q, %v), want (%q, nil)", got, err, want)
	}
	if len(seen) != 2 || seen[0] != "A" || seen[1] != "é 1" {
		t.Fatalf("heading inputs = %q, want [A é 1]", seen)
	}
}

func TestParseDocxBodyTextAndNestedTable(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"tabs-and-breaks", `<document><body><p><t>A</t><tab/><t>B</t><br/><t>C</t><cr/><t>D</t></p></body></document>`, "A\tB\nC\nD\n"},
		{"nested-table", `<document><body><tbl><tr><tc><p><t>A</t></p><tbl><tr><tc><p><t>B</t></p></tc></tr></tbl><p><t>C</t></p></tc></tr></tbl></body></document>`, "\n| A B C |\n| --- |\n\n"},
		{"empty-paragraph", `<document><body><p/><p><t>A</t></p></body></document>`, "\nA\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseDocxBody(strings.NewReader(tc.body), nil)
			if err != nil || got != tc.want {
				t.Fatalf("parseDocxBody = (%q, %v), want (%q, nil)", got, err, tc.want)
			}
		})
	}
}

func TestRenderMarkdownTablePaddingAndEscapes(t *testing.T) {
	got := renderMarkdownTable([][]string{{"A|B"}, {"C", "D"}})
	const want = "\n| A\\|B |  |\n| --- | --- |\n| C | D |\n\n"
	if got != want {
		t.Fatalf("table = %q, want %q", got, want)
	}
	if got := renderMarkdownTable(nil); got != "" {
		t.Fatalf("empty table = %q, want empty", got)
	}
}

type docxMember struct {
	name string
	body string
}

func makeDOCX(t *testing.T, members ...docxMember) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for _, member := range members {
		w, err := zw.Create(member.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(member.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestDOCXBodySelection(t *testing.T) {
	first := `<document><body><p><t>First</t></p></body></document>`
	second := `<document><body><p><t>Second</t></p></body></document>`
	for _, tc := range []struct {
		name    string
		members []docxMember
	}{
		{"forward-slash", []docxMember{{"word/document.xml", first}}},
		{"backslash", []docxMember{{`word\document.xml`, first}}},
		{"first-match", []docxMember{{`word\document.xml`, first}, {"word/document.xml", second}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DOCX(makeDOCX(t, tc.members...), nil)
			if err != nil || got != "First\n" {
				t.Fatalf("DOCX = (%q, %v), want (First\\n, nil)", got, err)
			}
		})
	}
}

func TestDOCXNativeText(t *testing.T) {
	for _, tc := range []struct {
		country string
		text    string
	}{
		{"vn", "Điều 1"},
		{"my", "Section 1"},
		{"sg", "Section 2"},
		{"id", "Pasal 1"},
		{"th", "มาตรา 1"},
		{"kh", "Article 1"},
	} {
		t.Run(tc.country, func(t *testing.T) {
			body := "<document><body><p><t>" + tc.text + "</t></p></body></document>"
			got, err := DOCX(makeDOCX(t, docxMember{"word/document.xml", body}), nil)
			want := tc.text + "\n"
			if err != nil || got != want {
				t.Fatalf("DOCX = (%q, %v), want (%q, nil)", got, err, want)
			}
		})
	}
}

func TestDOCXErrors(t *testing.T) {
	t.Run("invalid-zip", func(t *testing.T) {
		got, err := DOCX([]byte("invalid"), nil)
		if got != "" || !errors.Is(err, zip.ErrFormat) || !strings.HasPrefix(err.Error(), "open docx zip: ") {
			t.Fatalf("DOCX = (%q, %v), want wrapped zip.ErrFormat", got, err)
		}
	})
	t.Run("missing-body", func(t *testing.T) {
		got, err := DOCX(makeDOCX(t), nil)
		if got != "" || err == nil || err.Error() != "docx: word/document.xml not found" {
			t.Fatalf("DOCX = (%q, %v), want missing-body error", got, err)
		}
	})
	t.Run("unsupported-compression", func(t *testing.T) {
		var b bytes.Buffer
		zw := zip.NewWriter(&b)
		if _, err := zw.CreateRaw(&zip.FileHeader{Name: "word/document.xml", Method: 99}); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		got, err := DOCX(b.Bytes(), nil)
		if got != "" || !errors.Is(err, zip.ErrAlgorithm) || !strings.HasPrefix(err.Error(), "open word/document.xml: ") {
			t.Fatalf("DOCX = (%q, %v), want wrapped zip.ErrAlgorithm", got, err)
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
			got, err := DOCX(makeDOCX(t, docxMember{"word/document.xml", tc.body}), nil)
			var syntaxErr *xml.SyntaxError
			if got != "" || !errors.As(err, &syntaxErr) || !strings.HasPrefix(err.Error(), tc.prefix) {
				t.Fatalf("DOCX = (%q, %v), want %q and XML syntax error", got, err, tc.prefix)
			}
		})
	}
}

func TestDOCXHeadingPolicy(t *testing.T) {
	body := `<document><body><p><t>A</t></p><p><t>HEADER 1</t></p></body></document>`
	got, err := DOCX(makeDOCX(t, docxMember{"word/document.xml", body}), syntheticHeading)
	const want = "A\n\nHEADER 1\n"
	if err != nil || got != want {
		t.Fatalf("DOCX = (%q, %v), want (%q, nil)", got, err, want)
	}
}
