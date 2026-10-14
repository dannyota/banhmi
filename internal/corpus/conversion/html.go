package conversion

import (
	"bytes"
	"fmt"
	stdhtml "html"
	"io"
	"mime"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	"golang.org/x/text/unicode/norm"

	"danny.vn/banhmi/internal/corpus/model"
)

// blockElems are block-level elements whose end becomes a line break.
var blockElems = map[atom.Atom]bool{
	atom.P: true, atom.Div: true, atom.Li: true, atom.Ul: true, atom.Ol: true,
	atom.Tr: true, atom.Table: true, atom.Section: true, atom.Article: true,
	atom.Blockquote: true, atom.H1: true, atom.H2: true, atom.H3: true,
	atom.H4: true, atom.H5: true, atom.H6: true,
}

// HTML extracts plain text, preserves block boundaries as line breaks, decodes
// entities, and normalizes to NFC. Non-body elements such as script and style are
// dropped. The result may be empty when the markup carries no text.
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
	return norm.NFC.String(tidyLines(b.String())), nil
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

// htmlBlocks preserves source-token byte ranges instead of reconstructing offsets
// from the repaired HTML DOM. The baseline selector is body or one exact #id.
func htmlBlocks(data []byte, b *blockBuilder) error {
	return htmlBlocksBounded(data, b, 128)
}

func htmlBlocksBounded(data []byte, b *blockBuilder, depthLimit int) error {
	selector := b.manifest.HTMLSelector
	if selector != "body" && (!strings.HasPrefix(selector, "#") || len(selector) < 2) {
		return fmt.Errorf("html: SELECTOR_REQUIRED")
	}
	z := html.NewTokenizer(bytes.NewReader(data))
	z.SetMaxBuf(int(min(int64(len(data))+1, b.manifest.Request.Budget.MaxInputBytes+1)))
	type pendingBlock struct {
		kind  model.BlockKind
		text  strings.Builder
		spans []model.Span
		empty model.Span
	}
	var pending []*pendingBlock
	var owners []*pendingBlock
	var implicit *pendingBlock
	var stack []string
	selectedDepth, matches := 0, 0
	var offset int64
	for tokens := 0; ; tokens++ {
		if tokens >= b.manifest.Request.Budget.MaxItems {
			return resourceFailure("items")
		}
		typeID := z.Next()
		raw := append([]byte(nil), z.Raw()...)
		start := offset
		offset += int64(len(raw))
		if typeID == html.ErrorToken {
			if z.Err() != io.EOF {
				return fmt.Errorf("html: TOKEN_INVALID")
			}
			break
		}
		ordinal := tokens
		span := model.Span{ArtifactHash: b.manifest.Request.Artifact.Asset.SHA256, Space: model.OriginalBytes, Range: &model.ByteRange{Start: start, End: offset}, TokenOrdinal: &ordinal}
		token := z.Token()
		switch typeID {
		case html.StartTagToken, html.SelfClosingTagToken:
			name := token.Data
			match := selector == "body" && name == "body"
			for _, attr := range token.Attr {
				if strings.HasPrefix(selector, "#") && attr.Key == "id" && attr.Val == selector[1:] {
					match = true
				}
				if attr.Key == "charset" && !strings.EqualFold(attr.Val, "utf-8") && !strings.EqualFold(attr.Val, "utf8") {
					return fmt.Errorf("html: ENCODING_AMBIGUOUS")
				}
				if name == "meta" && attr.Key == "content" && strings.Contains(strings.ToLower(attr.Val), "charset") {
					_, parameters, err := mime.ParseMediaType(attr.Val)
					if err != nil || (!strings.EqualFold(parameters["charset"], "utf-8") && !strings.EqualFold(parameters["charset"], "utf8")) {
						return fmt.Errorf("html: ENCODING_AMBIGUOUS")
					}
				}
			}
			void := name == "br" || name == "img" || name == "meta" || name == "link" || name == "hr" || name == "input" || name == "wbr" || name == "area" || name == "base" || name == "embed" || name == "param" || name == "source" || name == "track" || name == "col"
			if match {
				matches++
				if matches != 1 || void || typeID == html.SelfClosingTagToken {
					return fmt.Errorf("html: SELECTOR_AMBIGUOUS")
				}
				selectedDepth = len(stack) + 1
			}
			var owner *pendingBlock
			if len(owners) > 0 {
				owner = owners[len(owners)-1]
			}
			if selectedDepth > 0 {
				kind := model.BlockKind("")
				if name == "p" || name == "blockquote" || name == "div" || name == "section" || name == "article" {
					kind = model.BlockParagraph
				} else if name == "li" {
					kind = model.BlockListItem
				} else if len(name) == 2 && name[0] == 'h' && name[1] >= '1' && name[1] <= '6' {
					kind = model.BlockHeading
				}
				if kind != "" {
					if owner != nil {
						if err := b.finding("NESTED_HTML_STRUCTURE_UNQUALIFIED", span); err != nil {
							return err
						}
					}
					owner = &pendingBlock{kind: kind, empty: span}
					pending = append(pending, owner)
					implicit = nil
				}
				for _, attr := range token.Attr {
					if attr.Key != "id" {
						if err := b.finding("HTML_ATTRIBUTES_UNQUALIFIED", span); err != nil {
							return err
						}
						break
					}
				}
				switch name {
				case "body", "div", "section", "article", "p", "span", "strong", "b", "em", "i", "u", "h1", "h2", "h3", "h4", "h5", "h6", "ul", "ol", "li", "blockquote":
				case "br":
					if owner == nil {
						owner = implicit
					}
					if owner == nil {
						owner = &pendingBlock{kind: model.BlockParagraph, empty: span}
						pending = append(pending, owner)
						implicit = owner
					}
					owner.text.WriteByte('\n')
					owner.spans = append(owner.spans, span)
				case "img":
					pending = append(pending, &pendingBlock{kind: model.BlockImage, empty: span})
					if err := b.finding("EXTERNAL_OR_UNRESOLVED_IMAGE", span); err != nil {
						return err
					}
				default:
					if err := b.finding("UNSUPPORTED_HTML_CONSTRUCT", span); err != nil {
						return err
					}
				}
			}
			if !void && typeID != html.SelfClosingTagToken {
				stack = append(stack, name)
				owners = append(owners, owner)
				if len(stack) > depthLimit {
					return resourceFailure("depth")
				}
			}
		case html.EndTagToken:
			if len(stack) == 0 || stack[len(stack)-1] != token.Data {
				return fmt.Errorf("html: FIDELITY_UNRESOLVED_MARKUP")
			}
			if selectedDepth == len(stack) {
				selectedDepth = 0
			}
			stack = stack[:len(stack)-1]
			owners = owners[:len(owners)-1]
			if token.Data == "p" || token.Data == "li" || token.Data == "div" || token.Data == "section" || token.Data == "article" || token.Data == "ul" || token.Data == "ol" || (len(token.Data) == 2 && token.Data[0] == 'h') {
				implicit = nil
			}
		case html.TextToken:
			if selectedDepth == 0 || len(raw) == 0 {
				continue
			}
			kind := model.BlockParagraph
			for _, name := range stack {
				if name == "script" || name == "style" || name == "noscript" {
					if err := b.finding("UNSUPPORTED_HTML_CONTENT", span); err != nil {
						return err
					}
					kind = ""
					break
				}
				if name == "li" {
					kind = model.BlockListItem
				} else if len(name) == 2 && name[0] == 'h' && name[1] >= '1' && name[1] <= '6' {
					kind = model.BlockHeading
				}
			}
			if kind != "" {
				var owner *pendingBlock
				if len(owners) > 0 {
					owner = owners[len(owners)-1]
				}
				if owner == nil {
					owner = implicit
					if owner == nil {
						owner = &pendingBlock{kind: kind, empty: span}
						pending = append(pending, owner)
						implicit = owner
					}
				}
				if int64(len(raw)) > b.manifest.OutputBytes-int64(owner.text.Len()) {
					return resourceFailure("output")
				}
				owner.text.WriteString(stdhtml.UnescapeString(string(raw)))
				owner.spans = append(owner.spans, span)
			}
		case html.CommentToken, html.DoctypeToken:
			if selectedDepth > 0 {
				if err := b.finding("UNSUPPORTED_HTML_CONTENT", span); err != nil {
					return err
				}
			}
		}
	}
	for _, block := range pending {
		if len(block.spans) == 0 {
			block.spans = []model.Span{block.empty}
		}
		if err := b.add(block.kind, block.text.String(), block.spans); err != nil {
			return err
		}
	}
	if matches != 1 || len(stack) != 0 || len(b.blocks) == 0 {
		return fmt.Errorf("html: SELECTOR_OR_CONTENT_MISSING")
	}
	return nil
}
