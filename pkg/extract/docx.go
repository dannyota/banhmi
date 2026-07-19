package extract

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// docxBodyPath is the main document part inside a .docx (OOXML) package.
const docxBodyPath = "word/document.xml"

// DOCX extracts text from a .docx file's bytes using only the standard library
// (plus golang.org/x/text for NFC). A .docx is a zip whose word/document.xml holds
// the body as WordprocessingML: <w:p> paragraphs of <w:r> runs of <w:t> text, and
// <w:tbl> tables of <w:tr> rows of <w:tc> cells. This is the exact source text —
// no layout reconstruction, no OCR. Paragraphs become lines; tables become
// Markdown pipe-tables (so schedule/fee tables stay legible); <w:tab/> and
// <w:br/>/<w:cr/> are preserved. A blank line precedes Điều/Khoản/Chương/… headings
// so the downstream chunker can split on article boundaries.
func DOCX(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("open docx zip: %w", err)
	}
	var body *zip.File
	for _, f := range zr.File {
		// Some generators (seen in VN gazette DOCX) use backslash path separators,
		// which violate the OPC/zip spec; normalize before matching.
		if strings.ReplaceAll(f.Name, `\`, "/") == docxBodyPath {
			body = f
			break
		}
	}
	if body == nil {
		return "", fmt.Errorf("docx: %s not found", docxBodyPath)
	}
	rc, err := body.Open()
	if err != nil {
		return "", fmt.Errorf("open %s: %w", docxBodyPath, err)
	}
	defer func() { _ = rc.Close() }()
	return parseDocxBody(rc)
}

// legalHeading matches a paragraph opening a legal structural unit, so a blank
// line can be emitted before it for the chunker. Tested on NFC text.
var legalHeading = regexp.MustCompile(`^(Điều|Khoản|Điểm|Mục|Chương|Phần)\s+[\dIVXLC]`)

// docxTable accumulates one (non-nested) table's cells, row by row.
type docxTable struct {
	rows [][]string
	row  []string
}

// parseDocxBody streams the WordprocessingML body. Top-level paragraphs become
// lines (with a blank line before legal headings); <w:tbl> blocks become Markdown
// pipe-tables. Elements are matched by local name (the body is all `w:` namespace).
// Nested tables (rare in legal docs) flatten into the enclosing cell's text.
func parseDocxBody(r io.Reader) (string, error) {
	dec := xml.NewDecoder(r)
	var out strings.Builder  // finished document text
	var para strings.Builder // current top-level paragraph (outside any table)

	var tbl *docxTable       // current top-level table (nil when outside one)
	var cell strings.Builder // current cell's text
	inCell := false
	cellSpan := 1
	depth := 0 // table-nesting depth

	writeText := func(s string) {
		switch {
		case inCell:
			cell.WriteString(s)
		case depth == 0:
			para.WriteString(s)
		}
	}
	flushPara := func() {
		s := norm.NFC.String(para.String())
		para.Reset()
		if strings.TrimSpace(s) == "" {
			out.WriteByte('\n')
			return
		}
		if legalHeading.MatchString(strings.TrimLeft(s, " \t")) {
			out.WriteByte('\n') // blank line before Điều/Khoản/… for the chunker
		}
		out.WriteString(s)
		out.WriteByte('\n')
	}

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("parse docx body: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "tbl":
				depth++
				if depth == 1 {
					tbl = &docxTable{}
				}
			case "tr":
				if depth == 1 && tbl != nil {
					tbl.row = nil
				}
			case "tc":
				if depth == 1 {
					inCell = true
					cellSpan = 1
					cell.Reset()
				}
			case "gridSpan": // <w:gridSpan w:val="N"/> in <w:tcPr>: column span
				if inCell {
					if n, e := strconv.Atoi(attrVal(t, "val")); e == nil && n > 1 {
						cellSpan = n
					}
				}
			case "t":
				var s string
				if err := dec.DecodeElement(&s, &t); err != nil {
					return "", fmt.Errorf("decode w:t: %w", err)
				}
				writeText(s)
			case "tab":
				writeText("\t")
			case "br", "cr":
				writeText("\n")
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "p":
				if inCell {
					cell.WriteByte(' ') // a cell's paragraphs join onto one line
				} else if depth == 0 {
					flushPara()
				}
			case "tc":
				if depth == 1 && tbl != nil {
					txt := strings.Join(strings.Fields(cell.String()), " ")
					tbl.row = append(tbl.row, txt)
					for i := 1; i < cellSpan; i++ {
						tbl.row = append(tbl.row, "") // colspan: trailing empty cells
					}
					inCell = false
				}
			case "tr":
				if depth == 1 && tbl != nil {
					tbl.rows = append(tbl.rows, tbl.row)
					tbl.row = nil
				}
			case "tbl":
				depth--
				if depth == 0 && tbl != nil {
					out.WriteString(renderMarkdownTable(tbl.rows))
					tbl = nil
				}
			}
		}
	}
	if strings.TrimSpace(para.String()) != "" {
		flushPara()
	}
	return out.String(), nil
}

// attrVal returns the value of the attribute with the given local name, or "".
func attrVal(e xml.StartElement, local string) string {
	for _, a := range e.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

// renderMarkdownTable renders rows as a GitHub-flavored pipe table (first row is
// the header). Rows are padded to the widest row and `|` is escaped. Empty → "".
func renderMarkdownTable(rows [][]string) string {
	cols := 0
	for _, r := range rows {
		if len(r) > cols {
			cols = len(r)
		}
	}
	if cols == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteByte('\n')
	writeRow := func(cells []string) {
		b.WriteByte('|')
		for c := 0; c < cols; c++ {
			v := ""
			if c < len(cells) {
				v = strings.ReplaceAll(cells[c], "|", `\|`)
			}
			b.WriteByte(' ')
			b.WriteString(v)
			b.WriteString(" |")
		}
		b.WriteByte('\n')
	}
	writeRow(rows[0])
	b.WriteByte('|')
	for c := 0; c < cols; c++ {
		b.WriteString(" --- |")
	}
	b.WriteByte('\n')
	for _, r := range rows[1:] {
		writeRow(r)
	}
	b.WriteByte('\n')
	return b.String()
}
