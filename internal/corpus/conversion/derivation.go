//go:build linux

package conversion

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	stdhtml "html"
	"io"
	"io/fs"
	"mime"
	"os"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"

	"danny.vn/banhmi/internal/corpus/conversion/sandbox"
	"danny.vn/banhmi/internal/corpus/model"
)

func derivationFailure() error {
	return fmt.Errorf("validate: SOURCE_DERIVATION_MISMATCH")
}

// validateDerivation checks the source-to-raw edge without invoking either block
// adapter, normalization, or canonical reconstruction. The caller has already
// verified original's exact hash and length against the trusted request.
func validateDerivation(original []byte, document model.ConvertedDocument, m ConversionManifest, limits sandbox.Limits) error {
	media, parameters, err := mime.ParseMediaType(m.Request.Artifact.MediaType)
	if err != nil {
		return derivationFailure()
	}
	if charset := parameters["charset"]; charset != "" && !strings.EqualFold(charset, "utf-8") && !strings.EqualFold(charset, "utf8") {
		return derivationFailure()
	}
	switch media {
	case "text/html":
		return validateHTMLDerivation(original, document.Blocks, m, limits)
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return validateDOCXDerivation(original, document.Blocks, m, limits)
	default:
		return derivationFailure()
	}
}

type sourceFragment struct {
	ordinal int
	text    string
	empty   bool
}

// validateHTMLDerivation decodes exact original tokens. Raw HTML text uses
// html.UnescapeString and preserves whitespace, including CRLF and form blanks.
// A br contributes one newline; structural and image anchors can carry no text.
// Matching whole tokens also prevents ranges inside tags or character references
// from claiming a different decoding context.
func validateHTMLDerivation(original []byte, blocks []model.Block, m ConversionManifest, limits sandbox.Limits) error {
	if !utf8.Valid(original) {
		return derivationFailure()
	}
	if int64(len(original)) > limits.ExpandedBytes {
		return resourceFailure("expanded")
	}
	fragments := map[model.ByteRange]sourceFragment{}
	z := html.NewTokenizer(bytes.NewReader(original))
	z.SetMaxBuf(len(original) + 1)
	var offset int64
	depth := 0
	for ordinal := 0; ; ordinal++ {
		if ordinal >= m.Request.Budget.MaxItems {
			return resourceFailure("items")
		}
		kind := z.Next()
		raw := append([]byte(nil), z.Raw()...)
		rangeKey := model.ByteRange{Start: offset, End: offset + int64(len(raw))}
		offset = rangeKey.End
		if kind == html.ErrorToken {
			if z.Err() != io.EOF {
				return derivationFailure()
			}
			break
		}
		fragment := sourceFragment{ordinal: ordinal}
		switch kind {
		case html.TextToken:
			fragment.text = stdhtml.UnescapeString(string(raw))
		case html.StartTagToken, html.SelfClosingTagToken:
			token := z.Token()
			if kind == html.StartTagToken {
				switch token.Data {
				case "br", "img", "meta", "link", "hr", "input", "wbr", "area", "base", "embed", "param", "source", "track", "col":
				default:
					depth++
					if depth > limits.XMLDepth {
						return resourceFailure("depth")
					}
				}
			}
			if token.Data == "br" {
				fragment.text = "\n"
			} else {
				fragment.empty = true
			}
		case html.EndTagToken:
			depth--
			if depth < 0 {
				return derivationFailure()
			}
			continue
		default:
			continue
		}
		fragments[rangeKey] = fragment
	}
	if depth != 0 {
		return derivationFailure()
	}
	for _, block := range blocks {
		var decoded strings.Builder
		var previousEnd int64
		for i, span := range block.SourceSpans {
			if span.Range == nil || span.Range.Start < 0 || span.Range.End > int64(len(original)) || span.TokenOrdinal == nil || (i > 0 && span.Range.Start < previousEnd) {
				return derivationFailure()
			}
			fragment, ok := fragments[*span.Range]
			if !ok || fragment.ordinal != *span.TokenOrdinal || (fragment.empty && (block.RawText != "" || len(block.SourceSpans) != 1)) {
				return derivationFailure()
			}
			if int64(len(fragment.text)) > m.OutputBytes-int64(decoded.Len()) {
				return resourceFailure("output")
			}
			decoded.WriteString(fragment.text)
			previousEnd = span.Range.End
		}
		if len(block.SourceSpans) == 0 || decoded.String() != block.RawText {
			return derivationFailure()
		}
	}
	return nil
}

// validateDOCXDerivation inventories the original ZIP under the sandbox bounds,
// but decompresses only the member named by block anchors. Baseline source-root
// validation limits these anchors to word/document.xml and pairs their names.
func validateDOCXDerivation(original []byte, blocks []model.Block, m ConversionManifest, limits sandbox.Limits) error {
	zr, err := zip.NewReader(bytes.NewReader(original), int64(len(original)))
	if err != nil {
		return derivationFailure()
	}
	if len(zr.File) > limits.Members {
		return resourceFailure("items")
	}
	seen := map[string]bool{}
	remaining := limits.ExpandedBytes
	var body []byte
	for _, file := range zr.File {
		name := strings.TrimSuffix(file.Name, "/")
		if !fs.ValidPath(name) || strings.ContainsAny(file.Name, "\\:") || seen[name] || file.Mode()&os.ModeSymlink != 0 {
			return derivationFailure()
		}
		seen[name] = true
		if file.FileInfo().IsDir() {
			continue
		}
		if !file.Mode().IsRegular() {
			return derivationFailure()
		}
		if file.UncompressedSize64 > uint64(remaining) {
			return resourceFailure("expanded")
		}
		remaining -= int64(file.UncompressedSize64)
		if file.UncompressedSize64 > 0 && (file.CompressedSize64 == 0 || (file.UncompressedSize64-1)/file.CompressedSize64 >= uint64(limits.ExpansionRatio)) {
			return resourceFailure("ratio")
		}
		if file.Name != docxBodyPath {
			continue
		}
		reader, err := file.Open()
		if err != nil {
			return derivationFailure()
		}
		body, err = io.ReadAll(io.LimitReader(reader, limits.ExpandedBytes+1))
		closeErr := reader.Close()
		if int64(len(body)) > limits.ExpandedBytes {
			return resourceFailure("expanded")
		}
		if err != nil || closeErr != nil || uint64(len(body)) != file.UncompressedSize64 {
			return derivationFailure()
		}
	}
	if len(body) == 0 {
		return derivationFailure()
	}
	memberHash := digest(body)
	for _, block := range blocks {
		if len(block.SourceSpans) != 2 || block.SourceSpans[1].ArtifactHash != memberHash || block.SourceSpans[0].Member != docxBodyPath || block.SourceSpans[1].Member != docxBodyPath {
			return derivationFailure()
		}
	}
	return validateDOCXParagraphs(body, blocks, m, limits)
}

// validateDOCXParagraphs streams the trusted member once and compares each
// anchored paragraph in place. Only Word t character data, tabs and line breaks
// contribute text. No blocks, normalized text, reports or findings are rebuilt.
func validateDOCXParagraphs(body []byte, blocks []model.Block, m ConversionManifest, limits sandbox.Limits) error {
	const wordNS = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	decoder := xml.NewDecoder(bytes.NewReader(body))
	depth, paragraphDepth, textDepth, index, paragraphToken := 0, 0, 0, 0, 0
	var start int64
	var text strings.Builder
	for ordinal := 0; ; ordinal++ {
		if ordinal >= m.Request.Budget.MaxItems {
			return resourceFailure("items")
		}
		before := decoder.InputOffset()
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return derivationFailure()
		}
		switch token := token.(type) {
		case xml.StartElement:
			depth++
			if depth > limits.XMLDepth {
				return resourceFailure("depth")
			}
			if token.Name.Space != wordNS {
				continue
			}
			switch token.Name.Local {
			case "p":
				if paragraphDepth != 0 {
					return derivationFailure()
				}
				paragraphDepth, paragraphToken, start = depth, ordinal, before
				text.Reset()
			case "t":
				if paragraphDepth == 0 || textDepth != 0 {
					return derivationFailure()
				}
				textDepth = depth
			case "tab", "br", "cr":
				if paragraphDepth == 0 {
					return derivationFailure()
				}
				if token.Name.Local == "tab" {
					text.WriteByte('\t')
				} else {
					text.WriteByte('\n')
				}
			}
		case xml.CharData:
			if textDepth > 0 {
				if int64(len(token)) > m.OutputBytes-int64(text.Len()) {
					return resourceFailure("output")
				}
				text.Write(token)
			}
		case xml.EndElement:
			if textDepth == depth {
				textDepth = 0
			}
			if paragraphDepth == depth {
				if index >= len(blocks) {
					return derivationFailure()
				}
				block := blocks[index]
				span := block.SourceSpans[1]
				if span.Range == nil || span.Range.Start != start || span.Range.End != decoder.InputOffset() || span.TokenOrdinal == nil || *span.TokenOrdinal != paragraphToken || span.Object != fmt.Sprintf("paragraph/%d", index) || block.RawText != text.String() {
					return derivationFailure()
				}
				index++
				paragraphDepth = 0
			}
			depth--
		case xml.Directive:
			return derivationFailure()
		}
		if int64(text.Len()) > m.OutputBytes {
			return resourceFailure("output")
		}
	}
	if depth != 0 || index != len(blocks) {
		return derivationFailure()
	}
	return nil
}
