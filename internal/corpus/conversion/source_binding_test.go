//go:build linux

package conversion

import (
	"testing"

	"danny.vn/banhmi/internal/corpus/model"
)

// TestInlineSelectorOwnerKind retains ancestor context outside the selected
// subtree. Kind substitution preserves text and anchors and rebuilds the graph.
func TestInlineSelectorOwnerKind(t *testing.T) {
	for _, fixture := range []struct {
		name     string
		original string
		kind     model.BlockKind
	}{
		{"heading", `<body><h1><span id="target">abc</span></h1></body>`, model.BlockHeading},
		{"list-item", `<body><li><span id="target">abc</span></li></body>`, model.BlockListItem},
		{"inner-heading", `<body><li><h2><span id="target">abc</span></h2></li></body>`, model.BlockHeading},
		{"inner-list-item", `<body><h2><li><span id="target">abc</span></li></h2></body>`, model.BlockListItem},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			original := []byte(fixture.original)
			m := testManifest(original, "text/html")
			m.HTMLSelector = "#target"
			m, assets, document, quality := semanticManifestFixture(t, original, m)
			if len(document.Blocks) != 1 || document.Blocks[0].Kind != fixture.kind || document.Blocks[0].RawText != "abc" {
				t.Fatal("inline selector lost ancestor kind or text")
			}
			start, ordinal := int64(28), 3
			if fixture.name == "inner-heading" || fixture.name == "inner-list-item" {
				start, ordinal = 32, 4
			}
			spans := document.Blocks[0].SourceSpans
			if len(spans) != 1 || spans[0].Range == nil || *spans[0].Range != (model.ByteRange{Start: start, End: start + 3}) || spans[0].TokenOrdinal == nil || *spans[0].TokenOrdinal != ordinal {
				t.Fatal("inline selector baseline anchor changed")
			}
			if len(quality.Findings) != 2 || quality.Findings[0].ReasonCode != "GEOMETRY_UNKNOWN" || quality.Findings[1].ReasonCode != "FIDELITY_UNQUALIFIED" {
				t.Fatal("inline selector generic findings changed")
			}
			document.Blocks[0].Kind = model.BlockParagraph
			forged := rehashDerivationFixture(t, m, assets, document, quality)
			assertSourceBindingRejection(t, m, original, forged)
		})
	}
}

// TestInlineSelectorBreakOwner keeps the distinct baseline rule: an implicit
// owner first created by br is a paragraph, even inside a heading or list item.
func TestInlineSelectorBreakOwner(t *testing.T) {
	for _, tag := range []string{"h1", "li"} {
		t.Run(tag, func(t *testing.T) {
			original := []byte(`<body><` + tag + `><span id="target"><br>abc</span></` + tag + `></body>`)
			m := testManifest(original, "text/html")
			m.HTMLSelector = "#target"
			_, _, document, _ := semanticManifestFixture(t, original, m)
			if len(document.Blocks) != 1 || document.Blocks[0].Kind != model.BlockParagraph || document.Blocks[0].RawText != "\nabc" {
				t.Fatal("implicit break owner changed kind or text")
			}
		})
	}
}

// TestHTMLSpanCountAndOrder isolates the count comparison and ascending-range
// guard respectively. Each attack also changes raw text to the claimed spans.
func TestHTMLSpanCountAndOrder(t *testing.T) {
	for _, damage := range []string{"count", "order"} {
		t.Run(damage, func(t *testing.T) {
			original := []byte("<body><p>A<b>B</b></p></body>")
			m, assets, document, quality := semanticFixture(t, original, "text/html")
			block := &document.Blocks[0]
			if len(block.SourceSpans) != 2 || block.RawText != "AB" {
				t.Fatal("span control baseline changed")
			}
			if damage == "count" {
				block.SourceSpans = block.SourceSpans[:1]
				block.RawText = "A"
			} else {
				block.SourceSpans[0], block.SourceSpans[1] = block.SourceSpans[1], block.SourceSpans[0]
				block.RawText = "BA"
			}
			forged := rehashDerivationFixture(t, m, assets, document, quality)
			assertSourceBindingRejection(t, m, original, forged)
		})
	}
}

// TestDOCXParagraphRangeEnd isolates the decoded paragraph end comparison.
// The original ZIP member, start, text and token ordinal remain unchanged.
func TestDOCXParagraphRangeEnd(t *testing.T) {
	original := fixtureDOCX(t, []byte(docxTestPrefix+`<w:p><w:r><w:t>abc</w:t></w:r></w:p>`+docxTestSuffix))
	m, assets, document, quality := semanticFixture(t, original, "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
	document.Blocks[0].SourceSpans[1].Range.End--
	forged := rehashDerivationFixture(t, m, assets, document, quality)
	assertSourceBindingRejection(t, m, original, forged)
}

func assertSourceBindingRejection(t *testing.T, m ConversionManifest, original []byte, forged *artifactSet) {
	t.Helper()
	if err := validateSemantics(m, manifestDigest(t, m), forged); err != nil {
		t.Fatalf("attack must pass staged-only semantics: %v", err)
	}
	if err := validateTestAssets(t, m, original, forged); err == nil || err.Error() != "validate: SOURCE_DERIVATION_MISMATCH" {
		t.Fatalf("source substitution rejected for wrong reason or accepted: %v", err)
	}
}
