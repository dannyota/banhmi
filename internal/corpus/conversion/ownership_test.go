//go:build linux

package conversion

import (
	"bytes"
	"fmt"
	"testing"

	"danny.vn/banhmi/internal/corpus/conversion/sandbox"
	"danny.vn/banhmi/internal/corpus/model"
)

// TestDerivationRejectsRehashedBlockOwnership checks the source edge after all
// staged objects and references have been rebuilt, with no stale objects left.
// DOCX already enforces these rules through its sequential paragraph comparison.
func TestDerivationRejectsRehashedBlockOwnership(t *testing.T) {
	for _, format := range []string{"html", "docx"} {
		for _, damage := range []string{"duplicate-block", "repeated-text-wrong-owner", "reversed-blocks"} {
			t.Run(format+"/"+damage, func(t *testing.T) {
				text := "def"
				if damage == "repeated-text-wrong-owner" {
					text = "abc"
				}
				original := []byte("<body><p>abc</p><p>" + text + "</p></body>")
				media := "text/html"
				if damage == "duplicate-block" {
					original = []byte("<body>abc</body>")
				}
				if format == "docx" {
					paragraphs := `<w:p><w:r><w:t>abc</w:t></w:r></w:p>`
					if damage != "duplicate-block" {
						paragraphs += `<w:p><w:r><w:t>` + text + `</w:t></w:r></w:p>`
					}
					original = fixtureDOCX(t, []byte(docxTestPrefix+paragraphs+docxTestSuffix))
					media = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
				}
				m, assets, document, quality := semanticFixture(t, original, media)
				switch damage {
				case "duplicate-block":
					duplicate := document.Blocks[0]
					duplicate.SourceSpans = copyOwnershipSpans(duplicate.SourceSpans)
					document.Blocks = append(document.Blocks, duplicate)
				case "repeated-text-wrong-owner":
					document.Blocks[1].SourceSpans = copyOwnershipSpans(document.Blocks[0].SourceSpans)
				case "reversed-blocks":
					document.Blocks[0], document.Blocks[1] = document.Blocks[1], document.Blocks[0]
				}
				if format == "docx" {
					// Rebind the staged paragraph labels to the forged order. Only
					// the original scan can establish their actual source owners.
					for i := range document.Blocks {
						for j := range document.Blocks[i].SourceSpans {
							document.Blocks[i].SourceSpans[j].Object = fmt.Sprintf("paragraph/%d", i)
						}
					}
				}
				forged := rehashDerivationFixture(t, m, assets, document, quality)
				if err := validateSemantics(m, manifestDigest(t, m), forged); err != nil {
					t.Fatalf("attack must pass staged-only semantics: %v", err)
				}
				if err := validateTestAssets(t, m, original, forged); err == nil || err.Error() != "validate: SOURCE_DERIVATION_MISMATCH" {
					t.Fatalf("source ownership did not reject rehashed attack: %v", err)
				}
			})
		}
	}
}

func copyOwnershipSpans(spans []model.Span) []model.Span {
	copy := append([]model.Span(nil), spans...)
	for i := range copy {
		if spans[i].Range != nil {
			rangeCopy := *spans[i].Range
			copy[i].Range = &rangeCopy
		}
		if spans[i].TokenOrdinal != nil {
			ordinal := *spans[i].TokenOrdinal
			copy[i].TokenOrdinal = &ordinal
		}
	}
	return copy
}

// TestHTMLDerivationInterleavedOwners preserves parent creation order even when
// the parent's final text token follows a child's token or an image anchor.
func TestHTMLDerivationInterleavedOwners(t *testing.T) {
	for _, original := range []string{
		"<body><div>before<p>child</p>after</div></body>",
		"<body><div><p>child</p>after</div></body>",
		"<body><div><p>child</p></div></body>",
		"<body><ul><li>before<ul><li>child</li></ul>after</li></ul></body>",
		"<body><p>before<img>after</p></body>",
		"<body>before<b>inline</b><br>after<p>next</p>last</body>",
	} {
		semanticFixture(t, []byte(original), "text/html")
	}
}

// TestHTMLDerivationRejectsRehashedTokenReuse retains within-block reuse
// rejection alongside the new cross-block ownership rules.
func TestHTMLDerivationRejectsRehashedTokenReuse(t *testing.T) {
	original := []byte("<body><p>abc<b>abc</b></p></body>")
	m, assets, document, quality := semanticFixture(t, original, "text/html")
	if len(document.Blocks) != 1 || len(document.Blocks[0].SourceSpans) != 2 {
		t.Fatal("token reuse fixture lost its two source tokens")
	}
	document.Blocks[0].SourceSpans[1] = copyOwnershipSpans(document.Blocks[0].SourceSpans[:1])[0]
	forged := rehashDerivationFixture(t, m, assets, document, quality)
	if err := validateSemantics(m, manifestDigest(t, m), forged); err != nil {
		t.Fatalf("attack must pass staged-only semantics: %v", err)
	}
	if err := validateTestAssets(t, m, original, forged); err == nil || err.Error() != "validate: SOURCE_DERIVATION_MISMATCH" {
		t.Fatalf("source token reuse accepted: %v", err)
	}
}

// TestHTMLDerivationRejectsRehashedIneligibleTokens checks that matching text
// outside the selector or inside discarded content cannot anchor a body block.
func TestHTMLDerivationRejectsRehashedIneligibleTokens(t *testing.T) {
	for _, fixture := range []struct {
		name    string
		html    string
		ordinal int
	}{
		{"outside-selector", "<html>abc<body>abc</body></html>", 1},
		{"discarded-content", "<body><script>abc</script><p>abc</p></body>", 2},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			original := []byte(fixture.html)
			m, assets, document, quality := semanticFixture(t, original, "text/html")
			start := int64(bytes.Index(original, []byte("abc")))
			span := &document.Blocks[0].SourceSpans[0]
			span.Range = &model.ByteRange{Start: start, End: start + 3}
			span.TokenOrdinal = &fixture.ordinal
			forged := rehashDerivationFixture(t, m, assets, document, quality)
			if err := validateSemantics(m, manifestDigest(t, m), forged); err != nil {
				t.Fatalf("attack must pass staged-only semantics: %v", err)
			}
			if err := validateTestAssets(t, m, original, forged); err == nil || err.Error() != "validate: SOURCE_DERIVATION_MISMATCH" {
				t.Fatalf("ineligible source token accepted: %v", err)
			}
		})
	}
}

// TestHTMLDerivationIDSelector checks exact-ID eligibility independently of
// body selection and rejects an absent selected subtree.
func TestHTMLDerivationIDSelector(t *testing.T) {
	original := []byte("<body><div id='target'>abc</div></body>")
	m, _, document, _ := semanticFixture(t, original, "text/html")
	m.HTMLSelector = "#target"
	if err := validateDerivation(original, document, m, sandbox.OfficeLimits()); err != nil {
		t.Fatalf("valid exact-ID owner rejected: %v", err)
	}
	m.HTMLSelector = "#missing"
	if err := validateDerivation(original, document, m, sandbox.OfficeLimits()); err == nil {
		t.Fatal("absent selector accepted")
	}
}
