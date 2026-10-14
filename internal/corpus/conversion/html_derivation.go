//go:build linux

package conversion

import (
	"bytes"
	stdhtml "html"
	"io"
	"mime"
	"strings"

	"golang.org/x/net/html"

	"danny.vn/banhmi/internal/corpus/conversion/sandbox"
	"danny.vn/banhmi/internal/corpus/model"
)

type htmlSourceOwner struct {
	anchor  model.ByteRange
	ordinal int
	count   int
	kind    model.BlockKind
}

type htmlSourceFrame struct {
	name     string
	owner    int
	excluded bool
}

// scanHTMLOwners inventories eligible anchors and their owners in creation
// order. It builds no converted blocks, canonical IDs, normalized text, maps or
// reports. Explicit owners are ordered by their opening tags, not their first
// text token: a parent's text may legitimately follow a child's text.
func scanHTMLOwners(original []byte, m ConversionManifest, limits sandbox.Limits, gaps *sourceGaps) (map[model.ByteRange]sourceFragment, []htmlSourceOwner, error) {
	selector := m.HTMLSelector
	if selector != "body" && (!strings.HasPrefix(selector, "#") || len(selector) < 2) {
		return nil, nil, derivationFailure()
	}
	fragments := map[model.ByteRange]sourceFragment{}
	var owners []htmlSourceOwner
	var stack []htmlSourceFrame
	selectedDepth, matches, implicit := 0, 0, -1
	newOwner := func(anchor model.ByteRange, ordinal int, kind model.BlockKind) int {
		owners = append(owners, htmlSourceOwner{anchor: anchor, ordinal: ordinal, kind: kind})
		return len(owners) - 1
	}
	z := html.NewTokenizer(bytes.NewReader(original))
	z.SetMaxBuf(len(original) + 1)
	var offset int64
	for ordinal := 0; ; ordinal++ {
		if ordinal >= m.Request.Budget.MaxItems {
			return nil, nil, resourceFailure("items")
		}
		kind := z.Next()
		raw := append([]byte(nil), z.Raw()...)
		anchor := model.ByteRange{Start: offset, End: offset + int64(len(raw))}
		offset = anchor.End
		if kind == html.ErrorToken {
			if z.Err() != io.EOF {
				return nil, nil, derivationFailure()
			}
			break
		}
		owner, excluded := -1, false
		if len(stack) > 0 {
			parent := stack[len(stack)-1]
			owner, excluded = parent.owner, parent.excluded
		}
		token := z.Token()
		tokenOrdinal := ordinal
		span := model.Span{ArtifactHash: m.Request.Artifact.Asset.SHA256, Space: model.OriginalBytes, Range: &anchor, TokenOrdinal: &tokenOrdinal}
		fragment := sourceFragment{ordinal: ordinal}
		contributes := false
		switch kind {
		case html.StartTagToken, html.SelfClosingTagToken:
			name := token.Data
			match := selector == "body" && name == "body"
			for _, attr := range token.Attr {
				if attr.Key == "charset" && !strings.EqualFold(attr.Val, "utf-8") && !strings.EqualFold(attr.Val, "utf8") {
					return nil, nil, derivationFailure()
				}
				if name == "meta" && attr.Key == "content" && strings.Contains(strings.ToLower(attr.Val), "charset") {
					_, parameters, err := mime.ParseMediaType(attr.Val)
					if err != nil || (!strings.EqualFold(parameters["charset"], "utf-8") && !strings.EqualFold(parameters["charset"], "utf8")) {
						return nil, nil, derivationFailure()
					}
				}
				if strings.HasPrefix(selector, "#") && attr.Key == "id" && attr.Val == selector[1:] {
					match = true
				}
			}
			void := false
			switch name {
			case "br", "img", "meta", "link", "hr", "input", "wbr", "area", "base", "embed", "param", "source", "track", "col":
				void = true
			}
			if match {
				matches++
				if matches != 1 || void || kind == html.SelfClosingTagToken {
					return nil, nil, derivationFailure()
				}
				selectedDepth = len(stack) + 1
			}
			if selectedDepth > 0 {
				blockKind := model.BlockParagraph
				if name == "li" {
					blockKind = model.BlockListItem
				} else if len(name) == 2 && name[0] == 'h' && name[1] >= '1' && name[1] <= '6' {
					blockKind = model.BlockHeading
				}
				switch name {
				case "p", "blockquote", "div", "section", "article", "li", "h1", "h2", "h3", "h4", "h5", "h6":
					if owner >= 0 {
						gaps.add("NESTED_HTML_STRUCTURE_UNQUALIFIED", span)
					}
					owner = newOwner(anchor, ordinal, blockKind)
					implicit = -1
				case "img":
					newOwner(anchor, ordinal, model.BlockImage)
					gaps.add("EXTERNAL_OR_UNRESOLVED_IMAGE", span)
				case "br":
					if owner < 0 {
						owner = implicit
					}
					if owner < 0 {
						owner = newOwner(anchor, ordinal, model.BlockParagraph)
						implicit = owner
					}
					fragment.text, contributes = "\n", true
				}
				for _, attr := range token.Attr {
					if attr.Key != "id" {
						gaps.add("HTML_ATTRIBUTES_UNQUALIFIED", span)
						break
					}
				}
				switch name {
				case "body", "div", "section", "article", "p", "span", "strong", "b", "em", "i", "u", "h1", "h2", "h3", "h4", "h5", "h6", "ul", "ol", "li", "blockquote", "br", "img":
				default:
					gaps.add("UNSUPPORTED_HTML_CONSTRUCT", span)
				}
			}
			if !void && kind != html.SelfClosingTagToken {
				excluded = excluded || name == "script" || name == "style" || name == "noscript"
				stack = append(stack, htmlSourceFrame{name: name, owner: owner, excluded: excluded})
				if len(stack) > limits.XMLDepth {
					return nil, nil, resourceFailure("depth")
				}
			}
		case html.EndTagToken:
			if len(stack) == 0 || stack[len(stack)-1].name != token.Data {
				return nil, nil, derivationFailure()
			}
			if selectedDepth == len(stack) {
				selectedDepth = 0
			}
			stack = stack[:len(stack)-1]
			switch token.Data {
			case "p", "li", "div", "section", "article", "ul", "ol":
				implicit = -1
			default:
				if len(token.Data) == 2 && token.Data[0] == 'h' {
					implicit = -1
				}
			}
		case html.TextToken:
			if selectedDepth == 0 || len(raw) == 0 {
				continue
			}
			if excluded {
				gaps.add("UNSUPPORTED_HTML_CONTENT", span)
				continue
			}
			if owner < 0 {
				owner = implicit
			}
			if owner < 0 {
				owner = newOwner(anchor, ordinal, model.BlockParagraph)
				implicit = owner
			}
			fragment.text, contributes = stdhtml.UnescapeString(string(raw)), true
		case html.CommentToken, html.DoctypeToken:
			if selectedDepth > 0 {
				gaps.add("UNSUPPORTED_HTML_CONTENT", span)
			}
		}
		if contributes {
			fragment.owner = owner
			fragments[anchor] = fragment
			owners[owner].count++
		}
	}
	if matches != 1 || len(stack) != 0 || len(owners) == 0 {
		return nil, nil, derivationFailure()
	}
	for owner, source := range owners {
		if source.count == 0 {
			// Opening tags are eligible only for their own empty structural
			// block or image. Nonempty owners must use contributing tokens.
			fragments[source.anchor] = sourceFragment{ordinal: source.ordinal, owner: owner, empty: true}
			owners[owner].count = 1
		}
	}
	return fragments, owners, gaps.err()
}
