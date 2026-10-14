//go:build linux

package conversion

import (
	"bytes"
	stdhtml "html"
	"io"
	"strings"

	"golang.org/x/net/html"

	"danny.vn/banhmi/internal/corpus/conversion/sandbox"
	"danny.vn/banhmi/internal/corpus/model"
)

type htmlSourceOwner struct {
	anchor  model.ByteRange
	ordinal int
	count   int
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
func scanHTMLOwners(original []byte, m ConversionManifest, limits sandbox.Limits) (map[model.ByteRange]sourceFragment, []int, error) {
	selector := m.HTMLSelector
	if selector != "body" && (!strings.HasPrefix(selector, "#") || len(selector) < 2) {
		return nil, nil, derivationFailure()
	}
	fragments := map[model.ByteRange]sourceFragment{}
	var owners []htmlSourceOwner
	var stack []htmlSourceFrame
	selectedDepth, matches, implicit := 0, 0, -1
	newOwner := func(anchor model.ByteRange, ordinal int) int {
		owners = append(owners, htmlSourceOwner{anchor: anchor, ordinal: ordinal})
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
		fragment := sourceFragment{ordinal: ordinal}
		contributes := false
		switch kind {
		case html.StartTagToken, html.SelfClosingTagToken:
			name := token.Data
			match := selector == "body" && name == "body"
			for _, attr := range token.Attr {
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
				switch name {
				case "p", "blockquote", "div", "section", "article", "li", "h1", "h2", "h3", "h4", "h5", "h6":
					owner = newOwner(anchor, ordinal)
					implicit = -1
				case "img":
					newOwner(anchor, ordinal)
				case "br":
					if owner < 0 {
						owner = implicit
					}
					if owner < 0 {
						owner = newOwner(anchor, ordinal)
						implicit = owner
					}
					fragment.text, contributes = "\n", true
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
			if selectedDepth == 0 || excluded || len(raw) == 0 {
				continue
			}
			if owner < 0 {
				owner = implicit
			}
			if owner < 0 {
				owner = newOwner(anchor, ordinal)
				implicit = owner
			}
			fragment.text, contributes = stdhtml.UnescapeString(string(raw)), true
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
	counts := make([]int, len(owners))
	for owner, source := range owners {
		counts[owner] = source.count
		if source.count == 0 {
			// Opening tags are eligible only for their own empty structural
			// block or image. Nonempty owners must use contributing tokens.
			fragments[source.anchor] = sourceFragment{ordinal: source.ordinal, owner: owner, empty: true}
			counts[owner] = 1
		}
	}
	return fragments, counts, nil
}
