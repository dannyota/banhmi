package extract

import (
	"fmt"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// blockElems are block-level elements whose end becomes a line break, so a vbpl
// document body's Điều/Khoản/paragraph structure survives extraction to text.
var blockElems = map[atom.Atom]bool{
	atom.P: true, atom.Div: true, atom.Li: true, atom.Ul: true, atom.Ol: true,
	atom.Tr: true, atom.Table: true, atom.Section: true, atom.Article: true,
	atom.Blockquote: true, atom.H1: true, atom.H2: true, atom.H3: true,
	atom.H4: true, atom.H5: true, atom.H6: true,
}

// HTML extracts plain text from an inline HTML document body (vbpl's
// documentContent.content), preserving block boundaries as line breaks, decoding
// entities, and NFC-normalizing. <script>/<style> are dropped. The result may be
// empty when the markup carries no text.
func HTML(s string) (string, error) {
	doc, err := html.Parse(strings.NewReader(s))
	if err != nil {
		return "", fmt.Errorf("parse html: %w", err)
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.DataAtom {
			case atom.Script, atom.Style, atom.Head, atom.Title, atom.Noscript:
				return
			}
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == html.ElementNode {
			switch {
			case n.DataAtom == atom.Br:
				b.WriteByte('\n')
			case n.DataAtom == atom.Td || n.DataAtom == atom.Th:
				b.WriteByte(' ')
			case blockElems[n.DataAtom]:
				b.WriteByte('\n')
			}
		}
	}
	walk(doc)
	return Normalize(tidyLines(b.String())), nil
}

// tidyLines collapses intra-line whitespace and runs of blank lines, and trims.
func tidyLines(s string) string {
	var out []string
	blank := 0
	for ln := range strings.SplitSeq(s, "\n") {
		ln = strings.Join(strings.Fields(ln), " ")
		if ln == "" {
			if blank++; blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, ln)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
