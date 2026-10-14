package conversion

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strconv"
	"strings"

	"golang.org/x/text/unicode/norm"

	"danny.vn/banhmi/internal/corpus/model"
)

// docxBodyPath is the main document part inside a .docx (OOXML) package.
const docxBodyPath = "word/document.xml"

// DOCX extracts NFC paragraph text and Markdown tables from an OOXML package.
// Tabs and breaks are preserved. A non-nil isHeading predicate receives NFC
// paragraph text with leading spaces and tabs removed. Matching paragraphs gain
// a preceding blank line; their emitted text retains its leading whitespace.
// A nil predicate adds no heading-specific spacing.
func DOCX(data []byte, isHeading func(string) bool) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("open docx zip: %w", err)
	}
	var body *zip.File
	for _, f := range zr.File {
		// Some generators use backslash path separators; normalize before matching.
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
	return parseDocxBody(rc, isHeading)
}

// docxTable accumulates one (non-nested) table's cells, row by row.
type docxTable struct {
	rows [][]string
	row  []string
}

// parseDocxBody streams the WordprocessingML body. Top-level paragraphs become
// lines (with a blank line before injected headings); <w:tbl> blocks become Markdown
// pipe-tables. Elements are matched by local name (the body is all `w:` namespace).
// Nested tables (rare in legal docs) flatten into the enclosing cell's text.
func parseDocxBody(r io.Reader, isHeading func(string) bool) (string, error) {
	dec := xml.NewDecoder(r)
	var out strings.Builder
	var para strings.Builder

	var tbl *docxTable
	var cell strings.Builder
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
		if isHeading != nil && isHeading(strings.TrimLeft(s, " \t")) {
			out.WriteByte('\n') // Blank line before a matching heading.
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

// docxBlocks keeps decoded-member anchors distinct from original ZIP bytes.
// The baseline preserves paragraph text and reports tables, revisions, style,
// numbering, drawings and additional package parts as unresolved fidelity.
func docxBlocks(data []byte, b *blockBuilder, expanded int64, members, depthLimit int) error {
	return docxBlocksBounded(data, b, expanded, members, depthLimit, 100)
}

func docxBlocksBounded(data []byte, b *blockBuilder, expanded int64, members, depthLimit int, ratio int64) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("docx: INVALID_PACKAGE")
	}
	if len(zr.File) > members {
		return resourceFailure("items")
	}
	memberLimit := expanded
	remainingTokens := b.manifest.Request.Budget.MaxItems
	var body []byte
	seen := map[string]bool{}
	for _, file := range zr.File {
		if !fs.ValidPath(strings.TrimSuffix(file.Name, "/")) || strings.ContainsAny(file.Name, "\\:") || seen[strings.TrimSuffix(file.Name, "/")] || file.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("docx: UNSAFE_PACKAGE")
		}
		seen[strings.TrimSuffix(file.Name, "/")] = true
		if file.FileInfo().IsDir() {
			continue
		}
		if !file.Mode().IsRegular() {
			return fmt.Errorf("docx: UNSAFE_PACKAGE")
		}
		if file.UncompressedSize64 > uint64(min(expanded, memberLimit)) {
			return resourceFailure("expanded")
		}
		if file.UncompressedSize64 > 0 && (file.CompressedSize64 == 0 || (file.UncompressedSize64-1)/file.CompressedSize64 >= uint64(ratio)) {
			return resourceFailure("ratio")
		}
		r, err := file.Open()
		if err != nil {
			return fmt.Errorf("docx: MEMBER_UNAVAILABLE")
		}
		member, readErr := io.ReadAll(io.LimitReader(r, expanded+1))
		closeErr := r.Close()
		if int64(len(member)) > expanded {
			return resourceFailure("expanded")
		}
		if readErr != nil || closeErr != nil {
			return fmt.Errorf("docx: MEMBER_INVALID")
		}
		expanded -= int64(len(member))
		if _, err := zip.NewReader(bytes.NewReader(member), int64(len(member))); err == nil {
			return fmt.Errorf("docx: NESTED_ARCHIVE_FORBIDDEN")
		}
		if strings.HasSuffix(strings.ToLower(file.Name), ".xml") || strings.HasSuffix(strings.ToLower(file.Name), ".rels") {
			if err := checkDOCXXML(member, depthLimit, &remainingTokens); err != nil {
				return err
			}
		}
		if file.Name == docxBodyPath {
			body = member
		} else {
			span := model.Span{ArtifactHash: b.manifest.Request.Artifact.Asset.SHA256, Space: model.OriginalBytes, Member: file.Name, ReasonCode: "BYTE_MAPPING_UNAVAILABLE"}
			if err := b.finding("UNQUALIFIED_PACKAGE_MEMBER", span); err != nil {
				return err
			}
		}
	}
	if len(body) == 0 {
		return fmt.Errorf("docx: MAIN_MEMBER_MISSING")
	}
	memberRef, err := b.assets.object(body)
	if err != nil {
		return err
	}
	if b.assets.types == nil {
		b.assets.types = map[string]string{}
	}
	b.assets.types[memberRef.Path] = "xml"
	const wordNS = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	dec := xml.NewDecoder(bytes.NewReader(body))
	var paragraph strings.Builder
	var start int64
	depth, pDepth, textDepth, paragraphs, pToken := 0, 0, 0, 0, 0
	documentSeen := false
	for count := 0; ; count++ {
		if count >= b.manifest.Request.Budget.MaxItems {
			return resourceFailure("items")
		}
		before := dec.InputOffset()
		token, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("docx: XML_INVALID")
		}
		anchor := model.Span{ArtifactHash: memberRef.SHA256, Space: model.DecodedMember, Member: docxBodyPath, Range: &model.ByteRange{Start: before, End: dec.InputOffset()}}
		switch t := token.(type) {
		case xml.StartElement:
			depth++
			if depth > depthLimit {
				return resourceFailure("depth")
			}
			if t.Name.Space != wordNS {
				if err := b.finding("UNSUPPORTED_XML_NAMESPACE", anchor); err != nil {
					return err
				}
				continue
			}
			switch t.Name.Local {
			case "document":
				if depth != 1 || documentSeen {
					return fmt.Errorf("docx: DOCUMENT_INVALID")
				}
				documentSeen = true
			case "body", "r":
			case "p":
				if pDepth != 0 {
					return fmt.Errorf("docx: UNSUPPORTED_NESTED_PARAGRAPH")
				}
				start, pDepth, pToken = before, depth, count
				paragraph.Reset()
			case "t":
				if pDepth == 0 || textDepth != 0 {
					return fmt.Errorf("docx: TEXT_OUTSIDE_PARAGRAPH")
				}
				textDepth = depth
			case "tab", "br", "cr":
				if attrVal(t, "type") != "" || attrVal(t, "clear") != "" {
					if err := b.finding("BREAK_GEOMETRY_UNKNOWN", anchor); err != nil {
						return err
					}
				}
				if pDepth == 0 {
					return fmt.Errorf("docx: LOST_ANCHOR")
				}
				if t.Name.Local == "tab" {
					paragraph.WriteByte('\t')
				} else {
					paragraph.WriteByte('\n')
				}
			default:
				if err := b.finding("UNSUPPORTED_DOCX_CONSTRUCT", anchor); err != nil {
					return err
				}
			}
		case xml.CharData:
			if textDepth > 0 {
				if int64(len(t)) > b.manifest.OutputBytes-int64(paragraph.Len()) {
					return resourceFailure("output")
				}
				paragraph.Write(t)
			} else if strings.TrimSpace(string(t)) != "" {
				if err := b.finding("UNSUPPORTED_DOCX_TEXT", anchor); err != nil {
					return err
				}
			}
		case xml.EndElement:
			if textDepth == depth {
				textDepth = 0
			}
			if pDepth == depth {
				ordinal := pToken
				original := model.Span{ArtifactHash: b.manifest.Request.Artifact.Asset.SHA256, Space: model.OriginalBytes, Member: docxBodyPath, Object: fmt.Sprintf("paragraph/%d", paragraphs), TokenOrdinal: &ordinal, ReasonCode: "BYTE_MAPPING_UNAVAILABLE"}
				decoded := model.Span{ArtifactHash: memberRef.SHA256, Space: model.DecodedMember, Member: docxBodyPath, Object: original.Object, TokenOrdinal: &ordinal, Range: &model.ByteRange{Start: start, End: dec.InputOffset()}}
				if err := b.add(model.BlockParagraph, paragraph.String(), []model.Span{original, decoded}); err != nil {
					return err
				}
				paragraphs++
				pDepth = 0
			}
			depth--
		case xml.Directive:
			return fmt.Errorf("docx: XML_DIRECTIVE_FORBIDDEN")
		}
	}
	if !documentSeen || paragraphs == 0 || depth != 0 {
		return fmt.Errorf("docx: CONTENT_MISSING")
	}
	return nil
}

// checkDOCXXML applies one aggregate token budget to all XML package members,
// including unsupported parts. They must satisfy safety bounds before findings.
func checkDOCXXML(data []byte, depthLimit int, remaining *int) error {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	depth := 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("docx: XML_INVALID")
		}
		if *remaining <= 0 {
			return resourceFailure("items")
		}
		*remaining = *remaining - 1
		switch token.(type) {
		case xml.StartElement:
			depth++
			if depth > depthLimit {
				return resourceFailure("depth")
			}
		case xml.EndElement:
			depth--
		case xml.Directive:
			return fmt.Errorf("docx: XML_DIRECTIVE_FORBIDDEN")
		}
	}
}
