//go:build linux

package conversion

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"danny.vn/banhmi/internal/corpus/conversion/sandbox"
	"danny.vn/banhmi/internal/corpus/model"
)

const docxTestPrefix = `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`
const docxTestSuffix = `</w:body></w:document>`

// rehashDerivationFixture rebuilds all block objects, maps, ledgers, aggregate
// bytes, reports and outer references. Superseded objects are deliberately absent.
func rehashDerivationFixture(t *testing.T, m ConversionManifest, assets *artifactSet, document model.ConvertedDocument, quality qualityReport) *artifactSet {
	t.Helper()
	builderManifest := m
	// Permit constructing an internally hashed out-of-bounds attack. Validation
	// still receives the original trusted manifest and original byte length.
	for _, block := range document.Blocks {
		for _, span := range block.SourceSpans {
			if span.Space == model.OriginalBytes && span.Range != nil {
				builderManifest.Request.Artifact.Asset.ByteSize = max(builderManifest.Request.Artifact.Asset.ByteSize, span.Range.End)
			}
		}
	}
	b := newBlockBuilder(builderManifest)
	for _, block := range document.Blocks {
		for _, span := range block.SourceSpans {
			if span.Space == model.DecodedMember {
				path := "objects/" + string(span.ArtifactHash)
				if _, err := b.assets.put(path, assets.files[path]); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := b.add(block.Kind, block.RawText, block.SourceSpans); err != nil {
			t.Fatal(err)
		}
	}
	forged, err := b.finish()
	if err != nil {
		t.Fatal(err)
	}
	forged.Original = m.Request.Artifact
	forged.DetectorHash = document.DetectorHash
	detectorPath := "objects/" + string(document.DetectorHash)
	if _, err := b.assets.put(detectorPath, assets.files[detectorPath]); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(forged)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.assets.put(resultPath(manifestDigest(t, m), "document.json"), encoded); err != nil {
		t.Fatal(err)
	}
	quality.DocumentHash = digest(encoded)
	replaceTestQuality(t, m, b.assets, quality)
	return b.assets
}

// TestDOCXDerivationRejectsRehashedSubstitution covers one paragraph and a
// consistent forged member referenced by every block of a multi-block document.
func TestDOCXDerivationRejectsRehashedSubstitution(t *testing.T) {
	for _, damage := range []string{"single-paragraph-member", "all-block-member", "raw-text", "range", "token"} {
		t.Run(damage, func(t *testing.T) {
			body := []byte(docxTestPrefix + `<w:p><w:r><w:t>abc</w:t></w:r></w:p>` + docxTestSuffix)
			if damage == "all-block-member" {
				body = []byte(docxTestPrefix + `<w:p><w:r><w:t>abc</w:t></w:r></w:p><w:p><w:r><w:t>def</w:t></w:r></w:p>` + docxTestSuffix)
			}
			original := fixtureDOCX(t, body)
			m, assets, document, quality := semanticFixture(t, original, "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
			switch damage {
			case "single-paragraph-member", "all-block-member":
				forgedBody := bytes.ReplaceAll(body, []byte("abc"), []byte("xyz"))
				ref, err := assets.object(forgedBody)
				if err != nil {
					t.Fatal(err)
				}
				for i := range document.Blocks {
					document.Blocks[i].SourceSpans[1].ArtifactHash = ref.SHA256
				}
				document.Blocks[0].RawText = "xyz"
			case "raw-text":
				document.Blocks[0].RawText = "xyz"
			case "range":
				document.Blocks[0].SourceSpans[1].Range.Start++
			case "token":
				ordinal := *document.Blocks[0].SourceSpans[0].TokenOrdinal + 1
				for i := range document.Blocks[0].SourceSpans {
					document.Blocks[0].SourceSpans[i].TokenOrdinal = &ordinal
				}
			}
			forged := rehashDerivationFixture(t, m, assets, document, quality)
			if err := validateSemantics(m, manifestDigest(t, m), forged); err != nil {
				t.Fatalf("attack must pass staged-only semantics: %v", err)
			}
			if err := validateTestAssets(t, m, original, forged); err == nil || err.Error() != "validate: SOURCE_DERIVATION_MISMATCH" {
				t.Fatalf("source derivation did not reject forged DOCX: %v", err)
			}
		})
	}
}

// TestHTMLDerivationRejectsRehashedSubstitution retains trusted original-byte
// anchors while substituting raw text or appending text that no anchor covers.
func TestHTMLDerivationRejectsRehashedSubstitution(t *testing.T) {
	for _, damage := range []string{"raw-text", "uncovered-text", "outside-original", "token", "partial-token"} {
		t.Run(damage, func(t *testing.T) {
			original := []byte("<body>abc</body>")
			m, assets, document, quality := semanticFixture(t, original, "text/html")
			span := &document.Blocks[0].SourceSpans[0]
			switch damage {
			case "raw-text":
				document.Blocks[0].RawText = "xyz"
			case "uncovered-text":
				document.Blocks[0].RawText = "abcxyz"
			case "outside-original":
				span.Range = &model.ByteRange{Start: int64(len(original)), End: int64(len(original)) + 3}
			case "token":
				ordinal := *span.TokenOrdinal + 1
				span.TokenOrdinal = &ordinal
			case "partial-token":
				span.Range.Start++
				document.Blocks[0].RawText = "bc"
			}
			forged := rehashDerivationFixture(t, m, assets, document, quality)
			if err := validateDerivation(original, document, m, sandbox.OfficeLimits()); err == nil {
				t.Fatal("independent source check accepted forged HTML")
			}
			if damage != "outside-original" {
				if err := validateSemantics(m, manifestDigest(t, m), forged); err != nil {
					t.Fatalf("attack must pass staged-only semantics: %v", err)
				}
			}
			if err := validateTestAssets(t, m, original, forged); err == nil {
				t.Fatal("fully rehashed HTML source substitution accepted")
			} else if damage != "outside-original" && err.Error() != "validate: SOURCE_DERIVATION_MISMATCH" {
				t.Fatalf("HTML attack rejected for unrelated reason: %v", err)
			}
		})
	}
}

// TestValidationBundleOriginalAllowance includes the original and staged bytes
// together, rather than taking the larger of two disjoint transport ceilings.
func TestValidationBundleOriginalAllowance(t *testing.T) {
	m := testManifest([]byte("<body>abc</body>"), "text/html")
	limit, err := BundleInputBytes(m)
	if err != nil {
		t.Fatal(err)
	}
	encode := func(n int64) int64 { return ((n + 2) / 3) * 4 }
	want := encode(m.Request.Budget.MaxInputBytes) + encode(m.OutputBytes) + (1 << 20) + int64(m.Request.Budget.MaxItems)*4 + (128 << 10)
	if limit < want {
		t.Fatal("validation input ceiling omits the opaque original")
	}
}

// TestValidationOriginalBinding checks original bytes before any staged output
// is interpreted. Same-length substitution isolates the SHA-256 check.
func TestValidationOriginalBinding(t *testing.T) {
	original := []byte("<body>abc</body>")
	m := testManifest(original, "text/html")
	request, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	for _, damage := range []string{"hash", "length", "missing", "limit", "noncanonical"} {
		t.Run(damage, func(t *testing.T) {
			data := append([]byte(nil), original...)
			switch damage {
			case "hash":
				data = []byte("<body>xyz</body>")
			case "length":
				data = data[:len(data)-1]
			case "missing":
				data = nil
			case "limit":
				data = append(data, 'x')
			}
			encoded := base64.StdEncoding.EncodeToString(data)
			if damage == "noncanonical" {
				encoded += "\n"
			}
			bundle := inputBundle{Version: "bundle-v1", Mode: "validate", Request: request, Original: encoded}
			err := validateBundle(sandbox.ChildRequest{}, bundle, m)
			if err == nil || err.Error() == "validate: INPUT_INVALID" {
				t.Fatalf("original binding was not checked first: %v", err)
			}
			if damage == "hash" || damage == "length" || damage == "missing" {
				if err.Error() != "validate: ORIGINAL_HASH_OR_LENGTH_MISMATCH" {
					t.Fatalf("original binding failure lost: %v", err)
				}
			}
		})
	}
}

// TestDerivationPositiveControls covers the retained fixtures and baseline
// decoding rules, including empty blocks, multiple anchors and unsupported parts.
func TestDerivationPositiveControls(t *testing.T) {
	for _, path := range []string{"testdata/minimal.html", "testdata/document.xml"} {
		t.Run(path, func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			media := "text/html"
			if strings.HasSuffix(path, ".xml") {
				data, media = fixtureDOCX(t, data), "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
			}
			semanticFixture(t, data, media)
		})
	}
	for _, html := range []string{
		"<body><h1>Điều <b>1</b></h1><p>A <b>B</b> <em>C</em></p></body>",
		"<body><p></p><p>x</p><p></p><img src='https://invalid.example/x'></body>",
		"<body><ul><li>outer<ul><li>inner</li></ul></li><li>next</li></ul></body>",
		"<body><p>A&amp;B\r\n  ___</p><p>C<br>D</p><br></body>",
		"<body><table><tr><td>x</td></tr></table><script>secret()</script><p>x</p></body>",
	} {
		semanticFixture(t, []byte(html), "text/html")
	}
	for _, paragraphs := range []string{
		`<w:p></w:p><w:p><w:r><w:t>x</w:t></w:r></w:p>`,
		"<w:p><w:r><w:t>A&amp;B\r\n  ___</w:t><w:tab/><w:t>á</w:t><w:br/><w:cr/></w:r></w:p>",
		`<w:tbl><w:tr><w:tc><w:p><w:r><w:t>cell</w:t></w:r></w:p></w:tc></w:tr></w:tbl>`,
		`<w:ins><w:p><w:r><w:t>revision</w:t></w:r></w:p></w:ins>`,
		`<w:p><w:rPr><w:rFonts w:ascii="VNI"/></w:rPr><w:r><w:t>ambiguous</w:t></w:r></w:p>`,
	} {
		semanticFixture(t, fixtureDOCX(t, []byte(docxTestPrefix+paragraphs+docxTestSuffix)), "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
	}
}

// TestDerivationResourceBounds keeps independent source checks inside the same
// package, expansion, ratio, XML depth and token ceilings as the sandbox.
func TestDerivationResourceBounds(t *testing.T) {
	original := fixtureDOCX(t, []byte(docxTestPrefix+`<w:p><w:r><w:t>x</w:t></w:r></w:p>`+docxTestSuffix))
	m, _, document, _ := semanticFixture(t, original, "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
	for _, dimension := range []string{"members", "expanded", "ratio", "depth", "tokens"} {
		t.Run(dimension, func(t *testing.T) {
			limits, manifest := sandbox.OfficeLimits(), m
			bound := sandbox.BoundMembers
			switch dimension {
			case "members":
				limits.Members = 0
			case "expanded":
				limits.ExpandedBytes, bound = 1, sandbox.BoundExpanded
			case "ratio":
				limits.ExpansionRatio, bound = 1, sandbox.BoundRatio
			case "depth":
				limits.XMLDepth, bound = 1, sandbox.BoundXML
			case "tokens":
				manifest.Request.Budget.MaxItems = 1
			}
			err := validateDerivation(original, document, manifest, limits)
			var failure *sandbox.Failure
			if !errors.As(err, &failure) || failure.Cause != sandbox.ResourceLimit || failure.Bound != bound {
				t.Fatalf("derivation resource classification lost for %s: %v", dimension, err)
			}
		})
	}
	html := []byte("<body><p>abc</p></body>")
	htmlManifest, _, htmlDocument, _ := semanticFixture(t, html, "text/html")
	for _, dimension := range []string{"expanded", "depth", "tokens"} {
		t.Run("html/"+dimension, func(t *testing.T) {
			limits, manifest := sandbox.OfficeLimits(), htmlManifest
			bound := sandbox.BoundMembers
			switch dimension {
			case "expanded":
				limits.ExpandedBytes, bound = 1, sandbox.BoundExpanded
			case "depth":
				limits.XMLDepth, bound = 1, sandbox.BoundXML
			case "tokens":
				manifest.Request.Budget.MaxItems = 1
			}
			err := validateDerivation(html, htmlDocument, manifest, limits)
			var failure *sandbox.Failure
			if !errors.As(err, &failure) || failure.Cause != sandbox.ResourceLimit || failure.Bound != bound {
				t.Fatalf("HTML derivation resource classification lost for %s: %v", dimension, err)
			}
		})
	}
}
