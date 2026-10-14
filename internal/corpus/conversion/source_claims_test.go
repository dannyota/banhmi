//go:build linux

package conversion

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"danny.vn/banhmi/internal/corpus/model"
)

// TestSourceKindsRejectRehashedSubstitution isolates kind after rebuilding all
// dependent objects and IDs. The DOCX baseline emits paragraphs, including
// drawing-bearing paragraphs; only HTML emits image blocks.
func TestSourceKindsRejectRehashedSubstitution(t *testing.T) {
	for _, fixture := range []struct {
		name     string
		original string
		kind     model.BlockKind
		docx     bool
	}{
		{"html-paragraph-heading", "<body><p>abc</p></body>", model.BlockHeading, false},
		{"html-image-paragraph", "<body><img></body>", model.BlockParagraph, false},
		{"docx-paragraph-heading", docxTestPrefix + `<w:p><w:r><w:t>abc</w:t></w:r></w:p>` + docxTestSuffix, model.BlockHeading, true},
		{"docx-paragraph-image", docxTestPrefix + `<w:p><w:r><w:drawing/></w:r></w:p>` + docxTestSuffix, model.BlockImage, true},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			original, media := []byte(fixture.original), "text/html"
			if fixture.docx {
				original, media = fixtureDOCX(t, original), "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
			}
			m, assets, document, quality := semanticFixture(t, original, media)
			document.Blocks[0].Kind = fixture.kind
			forged := rehashDerivationFixture(t, m, assets, document, quality)
			if err := validateSemantics(m, manifestDigest(t, m), forged); err != nil {
				t.Fatalf("kind attack must pass staged-only semantics: %v", err)
			}
			if err := validateTestAssets(t, m, original, forged); err == nil || err.Error() != "validate: SOURCE_DERIVATION_MISMATCH" {
				t.Fatalf("rehashed kind substitution was not rejected by source check: %v", err)
			}
		})
	}
}

func packageGapFixture(t *testing.T) []byte {
	t.Helper()
	var data bytes.Buffer
	w := zip.NewWriter(&data)
	for _, member := range []struct{ name, data string }{
		{docxBodyPath, docxTestPrefix + `<w:p><w:r><w:t>abc</w:t></w:r></w:p>` + docxTestSuffix},
		{"word/header1.xml", `<header/>`},
		{"word/header2.xml", `<header/>`},
	} {
		f, err := w.Create(member.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(member.data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

// TestMandatorySourceGapsRejectRehashedRemoval preserves correct blocks and
// both generic quarantine findings. Each removal erases one source occurrence,
// including two identical reasons with distinct anchors or package members.
func TestMandatorySourceGapsRejectRehashedRemoval(t *testing.T) {
	for _, fixture := range []struct {
		name, original, reason string
		docx                   bool
	}{
		{"image", "<body><img></body>", "EXTERNAL_OR_UNRESOLVED_IMAGE", false},
		{"discarded-text", "<body><script>hidden</script><p>abc</p></body>", "UNSUPPORTED_HTML_CONTENT", false},
		{"comment", "<body><!--hidden--><p>abc</p></body>", "UNSUPPORTED_HTML_CONTENT", false},
		{"doctype", "<body><!DOCTYPE html><p>abc</p></body>", "UNSUPPORTED_HTML_CONTENT", false},
		{"attributes", "<body><p class='x'>abc</p></body>", "HTML_ATTRIBUTES_UNQUALIFIED", false},
		{"nested", "<body><div><p>abc</p></div></body>", "NESTED_HTML_STRUCTURE_UNQUALIFIED", false},
		{"unsupported-html", "<body><table><tr><td>abc</td></tr></table></body>", "UNSUPPORTED_HTML_CONSTRUCT", false},
		{"repeated-images", "<body><img><img></body>", "EXTERNAL_OR_UNRESOLVED_IMAGE", false},
		{"docx-construct", `<w:tbl><w:p><w:r><w:t>abc</w:t></w:r></w:p></w:tbl>`, "UNSUPPORTED_DOCX_CONSTRUCT", true},
		{"docx-namespace", `<x:tag xmlns:x="urn:unsupported"/><w:p/>`, "UNSUPPORTED_XML_NAMESPACE", true},
		{"docx-text", `lost<w:p/>`, "UNSUPPORTED_DOCX_TEXT", true},
		{"docx-break", `<w:p><w:r><w:br w:type="page"/></w:r></w:p>`, "BREAK_GEOMETRY_UNKNOWN", true},
		{"docx-members", "", "UNQUALIFIED_PACKAGE_MEMBER", true},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			original, media := []byte(fixture.original), "text/html"
			if fixture.docx {
				original = fixtureDOCX(t, []byte(docxTestPrefix+fixture.original+docxTestSuffix))
				media = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
			}
			if fixture.name == "docx-members" {
				original = packageGapFixture(t)
			}
			m, assets, _, quality := semanticFixture(t, original, media)
			removed := false
			for i, finding := range quality.Findings {
				if finding.ReasonCode == fixture.reason {
					quality.Findings = append(quality.Findings[:i], quality.Findings[i+1:]...)
					removed = true
					break
				}
			}
			if !removed {
				t.Fatal("source gap fixture omitted required reason")
			}
			replaceTestQuality(t, m, assets, quality)
			if err := validateSemantics(m, manifestDigest(t, m), assets); err != nil {
				t.Fatalf("finding removal must preserve staged-only semantics: %v", err)
			}
			if err := validateTestAssets(t, m, original, assets); err == nil || err.Error() != "validate: SOURCE_FINDING_MISMATCH" {
				t.Fatalf("rehashed source finding removal accepted: %v", err)
			}
		})
	}
}

// replaceClaimDocument rehashes the report edge and all outer references, while
// retaining the existing structured objects to isolate a document scalar field.
func replaceClaimDocument(t *testing.T, m ConversionManifest, assets *artifactSet, document model.ConvertedDocument, quality qualityReport) {
	t.Helper()
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	assets.files[resultPath(manifestDigest(t, m), "document.json")] = data
	quality.DocumentHash = digest(data)
	replaceTestQuality(t, m, assets, quality)
}

// TestBaselineBlockClaimsRejectSubstitution isolates fields that must be absent
// or fixed by the trusted request, with current normalized-block object hashes.
func TestBaselineBlockClaimsRejectSubstitution(t *testing.T) {
	for _, field := range []string{"ParentID", "Authority", "Language", "Content", "Findings"} {
		t.Run(field, func(t *testing.T) {
			original := []byte("<body><p>abc</p><p>def</p></body>")
			m, assets, document, quality := semanticFixture(t, original, "text/html")
			block := &document.Blocks[0]
			switch field {
			case "ParentID":
				block.ParentID = document.Blocks[1].ID
			case "Authority":
				block.Authority = model.AuthorityBinding
			case "Language":
				block.Language = "en"
			case "Content":
				block.Content = &document.RawText
			case "Findings":
				block.Findings = []model.Finding{quality.Findings[0]}
			}
			old := document.NormalizedBlocks.Path
			ref, err := assets.value(document.Blocks)
			if err != nil {
				t.Fatal(err)
			}
			document.NormalizedBlocks = ref
			delete(assets.files, old)
			replaceClaimDocument(t, m, assets, document, quality)
			if err := validateTestAssets(t, m, original, assets); err == nil {
				t.Fatal("rehashed unsupported block claim accepted")
			}
		})
	}
}

// TestSourceSpanClaimsRejectRehashedSubstitution isolates optional source-anchor
// fields previously copied by canonical reconstruction without original checks.
func TestSourceSpanClaimsRejectRehashedSubstitution(t *testing.T) {
	for _, format := range []string{"html", "docx"} {
		for _, field := range []string{"Page", "Object", "ReasonCode", "root-page", "root-reason"} {
			t.Run(format+"/"+field, func(t *testing.T) {
				original, media := []byte("<body><p>abc</p></body>"), "text/html"
				if format == "docx" {
					original = fixtureDOCX(t, []byte(docxTestPrefix+`<w:p><w:r><w:t>abc</w:t></w:r></w:p>`+docxTestSuffix))
					media = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
				}
				m, assets, document, quality := semanticFixture(t, original, media)
				span := &document.Blocks[0].SourceSpans[len(document.Blocks[0].SourceSpans)-1]
				if field == "root-page" || field == "root-reason" {
					span = &document.Blocks[0].SourceSpans[0]
				}
				switch field {
				case "Page", "root-page":
					page := 1
					span.Page = &page
				case "Object":
					span.Object = "invented-object"
				case "ReasonCode", "root-reason":
					span.ReasonCode = "INVENTED_REASON"
				}
				if format == "docx" && field == "root-reason" {
					// An unavailable mapping with a fabricated reason cannot be
					// built by add. Rehash the actual staged graph directly.
					rehashClaimAnchors(t, m, assets, &document, &quality)
					if err := validateTestAssets(t, m, original, assets); err == nil {
						t.Fatal("rehashed unavailable-mapping reason accepted")
					}
					return
				}
				forged := rehashDerivationFixture(t, m, assets, document, quality)
				if err := validateTestAssets(t, m, original, forged); err == nil {
					t.Fatal("rehashed unsupported source anchor claim accepted")
				}
			})
		}
	}
}

func rehashClaimAnchors(t *testing.T, m ConversionManifest, assets *artifactSet, document *model.ConvertedDocument, quality *qualityReport) {
	t.Helper()
	var maps []model.OffsetMapSegment
	if decodeJSON(assets.files[document.OffsetMap.Path], &maps) != nil {
		t.Fatal("invalid map fixture")
	}
	maps[0].Sources = document.Blocks[0].SourceSpans
	rawBlocks := make([]struct {
		ID      string
		Kind    model.BlockKind
		Text    string
		Sources []model.Span
	}, len(document.Blocks))
	for i, block := range document.Blocks {
		rawBlocks[i].ID, rawBlocks[i].Kind, rawBlocks[i].Text, rawBlocks[i].Sources = block.ID, block.Kind, block.RawText, block.SourceSpans
	}
	for _, object := range []struct {
		ref   *model.AssetRef
		value any
	}{{&document.RawBlocks, rawBlocks}, {&document.NormalizedBlocks, document.Blocks}, {&document.OffsetMap, maps}} {
		old := object.ref.Path
		ref, err := assets.value(object.value)
		if err != nil {
			t.Fatal(err)
		}
		*object.ref = ref
		if old != ref.Path {
			delete(assets.files, old)
		}
	}
	replaceClaimDocument(t, m, assets, *document, *quality)
}

// TestReportSerializationRejectsHiddenClaims rehashes duplicate and unknown
// fields that could otherwise be retained despite a last-value JSON decoding.
func TestReportSerializationRejectsHiddenClaims(t *testing.T) {
	for _, field := range []string{"quality-duplicate", "detection-duplicate", "gaps", "decision", "qualification"} {
		t.Run(field, func(t *testing.T) {
			original := []byte("<body>abc</body>")
			m, assets, document, quality := semanticFixture(t, original, "text/html")
			if field == "detection-duplicate" {
				old := "objects/" + string(document.DetectorHash)
				data := append([]byte(`{"Format":"docx",`), assets.files[old][1:]...)
				ref, err := assets.object(data)
				if err != nil {
					t.Fatal(err)
				}
				delete(assets.files, old)
				document.DetectorHash, quality.DetectorHash = ref.SHA256, ref.SHA256
				replaceClaimDocument(t, m, assets, document, quality)
			} else {
				path := resultPath(manifestDigest(t, m), "quality.json")
				prefix := `{"Outcome":"accepted",`
				switch field {
				case "gaps":
					prefix = `{"Gaps":[],`
				case "decision":
					prefix = `{"DecisionRef":"invented",`
				case "qualification":
					prefix = `{"Qualified":true,`
				}
				assets.files[path] = append([]byte(prefix), assets.files[path][1:]...)
			}
			if err := validateTestAssets(t, m, original, assets); err == nil {
				t.Fatal("rehashed hidden report claim accepted")
			}
		})
	}
}

// TestReportClaimsRejectRehashedSubstitution changes one baseline report scalar
// at a time. Empty evidence/exclusion positives and all closure negatives live
// in TestQualityEvidenceClosure and TestMetricExclusionClosure.
func TestReportClaimsRejectRehashedSubstitution(t *testing.T) {
	for _, field := range []string{"SchemaVersion", "ArtifactHash", "CandidateDigest", "RequestDigest", "RecipeHash", "DetectorHash", "Outcome"} {
		t.Run(field, func(t *testing.T) {
			original := []byte("<body>abc</body>")
			m, assets, _, quality := semanticFixture(t, original, "text/html")
			value := reflect.ValueOf(&quality).Elem().FieldByName(field)
			if field == "Outcome" {
				value.SetString(string(model.QualityAccepted))
			} else if field == "SchemaVersion" {
				value.SetString("conversion-quality-other")
			} else {
				value.SetString(string(digest([]byte("other"))))
			}
			replaceTestQuality(t, m, assets, quality)
			if err := validateTestAssets(t, m, original, assets); err == nil {
				t.Fatal("rehashed report scalar substitution accepted")
			}
		})
	}
	for _, field := range []string{"count", "order", "Kind", "State", "Numerator", "Denominator", "Threshold", "Mandatory", "Passed", "ReasonCode"} {
		t.Run("metric/"+field, func(t *testing.T) {
			original := []byte("<body>abc</body>")
			m, assets, _, quality := semanticFixture(t, original, "text/html")
			metric := &quality.Metrics[0]
			switch field {
			case "count":
				quality.Metrics = quality.Metrics[:1]
			case "order":
				quality.Metrics[0], quality.Metrics[1] = quality.Metrics[1], quality.Metrics[0]
			case "Kind":
				metric.Kind = model.MetricFidelity
			case "State":
				metric.State = model.MetricNotApplicable
			case "Numerator":
				metric.Numerator = 1
			case "Denominator":
				metric.Denominator = 1
			case "Threshold":
				metric.Threshold = "other-threshold"
			case "Mandatory":
				metric.Mandatory = false
			case "Passed":
				metric.Passed = true
			case "ReasonCode":
				metric.ReasonCode = "OTHER_REASON"
			}
			replaceTestQuality(t, m, assets, quality)
			if err := validateTestAssets(t, m, original, assets); err == nil {
				t.Fatal("rehashed metric scalar substitution accepted")
			}
		})
	}
}

// TestFindingClaimsRejectRehashedSubstitution isolates each finding field and
// every source-span field. Valid but wrong anchors must fail original comparison.
func TestFindingClaimsRejectRehashedSubstitution(t *testing.T) {
	for _, field := range []string{"ReasonCode", "Severity", "Scope", "ScopeKey", "missing-spans", "duplicate", "ArtifactHash", "BlockID", "Space", "Start", "End", "Page", "Object", "Member", "TokenOrdinal", "SpanReasonCode"} {
		t.Run(field, func(t *testing.T) {
			original := []byte("<body><img><img></body>")
			m, assets, _, quality := semanticFixture(t, original, "text/html")
			finding := &quality.Findings[0]
			span := &finding.Spans[0]
			switch field {
			case "ReasonCode":
				finding.ReasonCode = "OTHER_REASON"
			case "Severity":
				finding.Severity = model.SeverityWarning
			case "Scope":
				finding.Scope = model.GapDocumentRevision
			case "ScopeKey":
				finding.ScopeKey = string(digest([]byte("other")))
			case "missing-spans":
				finding.Spans = nil
			case "duplicate":
				quality.Findings[1] = quality.Findings[0]
			case "ArtifactHash":
				span.ArtifactHash = digest([]byte("other"))
			case "BlockID":
				span.BlockID = "invented-block"
			case "Space":
				span.Space = model.RawText
			case "Start":
				span.Range.Start++
			case "End":
				span.Range.End--
			case "Page":
				page := 1
				span.Page = &page
			case "Object":
				span.Object = "invented-object"
			case "Member":
				span.Member = "invented-member"
			case "TokenOrdinal":
				ordinal := *span.TokenOrdinal + 1
				span.TokenOrdinal = &ordinal
			case "SpanReasonCode":
				span.ReasonCode = "OTHER_REASON"
			}
			replaceTestQuality(t, m, assets, quality)
			if err := validateTestAssets(t, m, original, assets); err == nil {
				t.Fatal("rehashed finding claim substitution accepted")
			}
		})
	}
}

// TestTrustedCaptureClaimsRejectRehashedSubstitution isolates every nested
// capture leaf. No edited request is supplied to validation.
func TestTrustedCaptureClaimsRejectRehashedSubstitution(t *testing.T) {
	for _, field := range []string{"Country", "SourceID", "CaptureKey", "MediaType", "RequestHash", "ResponseHash", "Asset.Path", "Asset.SHA256", "Asset.ByteSize", "TransportReceipt.Path", "TransportReceipt.SHA256", "TransportReceipt.ByteSize"} {
		t.Run(field, func(t *testing.T) {
			original := []byte("<body>abc</body>")
			m, assets, document, quality := semanticFixture(t, original, "text/html")
			switch field {
			case "Country":
				document.Original.Country = "other-country"
			case "SourceID":
				document.Original.SourceID = "other-source"
			case "CaptureKey":
				document.Original.CaptureKey = "other-capture"
			case "MediaType":
				document.Original.MediaType = "application/octet-stream"
			case "RequestHash":
				document.Original.RequestHash = digest([]byte("other"))
			case "ResponseHash":
				document.Original.ResponseHash = digest([]byte("other"))
			case "Asset.Path":
				document.Original.Asset.Path = "other-original"
			case "Asset.SHA256":
				document.Original.Asset.SHA256 = digest([]byte("other"))
			case "Asset.ByteSize":
				document.Original.Asset.ByteSize++
			case "TransportReceipt.Path":
				document.Original.TransportReceipt.Path = "other-transport"
			case "TransportReceipt.SHA256":
				document.Original.TransportReceipt.SHA256 = digest([]byte("other"))
			case "TransportReceipt.ByteSize":
				document.Original.TransportReceipt.ByteSize++
			}
			replaceClaimDocument(t, m, assets, document, quality)
			if err := validateTestAssets(t, m, original, assets); err == nil {
				t.Fatal("rehashed trusted capture substitution accepted")
			}
		})
	}
}

// TestDetectorClaimsRejectRehashedSubstitution rehashes detection, document,
// report and outer references and removes the superseded detection object.
func TestDetectorClaimsRejectRehashedSubstitution(t *testing.T) {
	for _, field := range []string{"Version", "Format", "Original", "Recipe"} {
		t.Run(field, func(t *testing.T) {
			original := []byte("<body>abc</body>")
			m, assets, document, quality := semanticFixture(t, original, "text/html")
			old := "objects/" + string(document.DetectorHash)
			var detection detectionResult
			if decodeJSON(assets.files[old], &detection) != nil {
				t.Fatal("invalid detection fixture")
			}
			switch field {
			case "Version":
				detection.Version = "detect-other"
			case "Format":
				detection.Format = "docx"
			case "Original":
				detection.Original = digest([]byte("other"))
			case "Recipe":
				detection.Recipe = digest([]byte("other"))
			}
			ref, err := assets.value(detection)
			if err != nil {
				t.Fatal(err)
			}
			delete(assets.files, old)
			document.DetectorHash, quality.DetectorHash = ref.SHA256, ref.SHA256
			replaceClaimDocument(t, m, assets, document, quality)
			if err := validateTestAssets(t, m, original, assets); err == nil {
				t.Fatal("rehashed detection claim accepted")
			}
		})
	}
}
