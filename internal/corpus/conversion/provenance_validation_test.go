//go:build linux

package conversion

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"danny.vn/banhmi/internal/corpus/conversion/sandbox"
	"danny.vn/banhmi/internal/corpus/model"
)

func semanticFixture(t *testing.T, data []byte, media string) (ConversionManifest, *artifactSet, model.ConvertedDocument, qualityReport) {
	t.Helper()
	m := testManifest(data, media)
	return semanticManifestFixture(t, data, m)
}

func semanticManifestFixture(t *testing.T, data []byte, m ConversionManifest) (ConversionManifest, *artifactSet, model.ConvertedDocument, qualityReport) {
	t.Helper()
	req := childRequest(t, data, m)
	if err := ChildHandler(req); err != nil {
		t.Fatal(err)
	}
	staged, refs := readChildOutputs(t, req)
	assets := newBlockBuilder(m).assets
	for i, ref := range refs {
		content, err := os.ReadFile(staged.paths[i])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := assets.put(ref.Path, content); err != nil {
			t.Fatal(err)
		}
	}
	var document model.ConvertedDocument
	var quality qualityReport
	key := manifestDigest(t, m)
	if decodeJSON(assets.files[resultPath(key, "document.json")], &document) != nil || decodeJSON(assets.files[resultPath(key, "quality.json")], &quality) != nil {
		t.Fatal("invalid semantic fixture")
	}
	if err := validateTestAssets(t, m, data, assets); err != nil {
		t.Fatalf("valid semantic fixture rejected: %v", err)
	}
	return m, assets, document, quality
}

func validateTestAssets(t *testing.T, m ConversionManifest, original []byte, assets *artifactSet) error {
	t.Helper()
	request, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	bundle := inputBundle{Version: "bundle-v1", Mode: "validate", Request: request, Original: base64.StdEncoding.EncodeToString(original)}
	// Rehash every outer reference so rejection must come from semantic checks.
	for _, ref := range assets.refs() {
		bundle.Outputs = append(bundle.Outputs, bundledOutput{Ref: ref, Data: base64.StdEncoding.EncodeToString(assets.files[ref.Path])})
	}
	req := sandbox.ChildRequest{OutputDir: t.TempDir(), ScratchDir: t.TempDir(), Limits: sandbox.OfficeLimits()}
	return validateBundle(req, bundle, m)
}

func replaceTestQuality(t *testing.T, m ConversionManifest, assets *artifactSet, quality qualityReport) {
	t.Helper()
	data, err := json.Marshal(quality)
	if err != nil {
		t.Fatal(err)
	}
	assets.files[resultPath(manifestDigest(t, m), "quality.json")] = data
}

// TestEmptyDOCXParagraphPassesSemanticValidation preserves empty paragraph maps.
func TestEmptyDOCXParagraphPassesSemanticValidation(t *testing.T) {
	body := []byte(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p></w:p><w:p><w:r><w:t>x</w:t></w:r></w:p></w:body></w:document>`)
	_, _, document, _ := semanticFixture(t, fixtureDOCX(t, body), "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
	if len(document.Blocks) != 2 || document.Blocks[0].RawText != "" || document.Blocks[1].RawText != "x" {
		t.Fatal("empty DOCX paragraph lost")
	}
}

// TestQualityEvidenceClosure checks exact bytes and closure for every evidence field.
func TestQualityEvidenceClosure(t *testing.T) {
	for _, location := range []string{"finding", "geometry", "fidelity"} {
		for _, damage := range []string{"baseline", "arbitrary", "absent", "hash", "length"} {
			t.Run(location+"/"+damage, func(t *testing.T) {
				m, assets, document, quality := semanticFixture(t, []byte("<body>abc</body>"), "text/html")
				ref := model.AssetRef{Path: "objects/absent", SHA256: digest([]byte("evidence")), ByteSize: 8}
				if damage == "arbitrary" {
					var err error
					ref, err = assets.object([]byte("evidence"))
					if err != nil {
						t.Fatal(err)
					}
				}
				if damage == "hash" || damage == "length" {
					ref = document.RawText
				}
				switch damage {
				case "hash":
					ref.SHA256 = digest([]byte("substitution"))
				case "length":
					ref.ByteSize++
				}
				if damage != "baseline" {
					switch location {
					case "finding":
						quality.Findings[0].Evidence = []model.AssetRef{ref}
					case "geometry":
						quality.Metrics[0].Evidence = []model.AssetRef{ref}
					case "fidelity":
						quality.Metrics[1].Evidence = []model.AssetRef{ref}
					}
				}
				replaceTestQuality(t, m, assets, quality)
				err := validateTestAssets(t, m, []byte("<body>abc</body>"), assets)
				if (err == nil) != (damage == "baseline") {
					t.Fatalf("quality evidence closure: %v", err)
				}
			})
		}
	}
}

// TestMetricExclusionClosure checks retained objects, trusted roots and byte bounds.
func TestMetricExclusionClosure(t *testing.T) {
	for _, index := range []int{0, 1} {
		for _, damage := range []string{"baseline", "arbitrary", "absent", "original", "bounds"} {
			t.Run(fmt.Sprintf("%d/%s", index, damage), func(t *testing.T) {
				m, assets, document, quality := semanticFixture(t, []byte("<body>abc</body>"), "text/html")
				ref := model.AssetRef{SHA256: digest([]byte("exclusion")), ByteSize: 9}
				if damage == "arbitrary" {
					var err error
					ref, err = assets.object([]byte("exclusion"))
					if err != nil {
						t.Fatal(err)
					}
				}
				if damage == "bounds" {
					ref = document.RawText
				}
				span := model.Span{ArtifactHash: ref.SHA256, Space: model.RawText, Range: &model.ByteRange{Start: 0, End: ref.ByteSize}}
				if damage == "original" {
					span.Space = model.OriginalBytes
				}
				if damage == "bounds" {
					span.Range.End++
				}
				if damage != "baseline" {
					quality.Metrics[index].Exclusions = []model.Span{span}
				}
				replaceTestQuality(t, m, assets, quality)
				err := validateTestAssets(t, m, []byte("<body>abc</body>"), assets)
				if (err == nil) != (damage == "baseline") {
					t.Fatalf("metric exclusion closure: %v", err)
				}
			})
		}
	}
}

// TestFormatSourceRootSubstitution rejects attacks with fully rehashed artifacts.
func TestFormatSourceRootSubstitution(t *testing.T) {
	for _, damage := range []string{"html-member", "docx-decoded-only", "docx-member", "docx-object", "docx-token", "docx-disconnected-hash"} {
		t.Run(damage, func(t *testing.T) {
			data, media := []byte("<body>abc</body>"), "text/html"
			if damage != "html-member" {
				data = fixtureDOCX(t, []byte(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>abc</w:t></w:r></w:p><w:p><w:r><w:t>def</w:t></w:r></w:p></w:body></w:document>`))
				media = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
			}
			m, assets, document, quality := semanticFixture(t, data, media)
			spans := document.Blocks[0].SourceSpans
			switch damage {
			case "html-member":
				spans[0].ArtifactHash = digest([]byte("abc"))
				spans[0].Space = model.DecodedMember
				spans[0].Member = docxBodyPath
				spans[0].Range = &model.ByteRange{Start: 0, End: 3}
			case "docx-decoded-only":
				for i := range document.Blocks {
					document.Blocks[i].SourceSpans = document.Blocks[i].SourceSpans[1:]
				}
			case "docx-member":
				spans[1].Member = "word/other.xml"
			case "docx-object":
				spans[1].Object = "paragraph/2"
			case "docx-token":
				ordinal := *spans[1].TokenOrdinal + 1
				spans[1].TokenOrdinal = &ordinal
			case "docx-disconnected-hash":
				spans[1].ArtifactHash = digest([]byte("abc"))
				spans[1].Range = &model.ByteRange{Start: 0, End: 3}
			}
			// Reproduce every canonical object from the substituted spans. This
			// deliberately uses the same reconstruction that accepted the attack.
			rebuilt := newBlockBuilder(m)
			for _, block := range document.Blocks {
				for _, span := range block.SourceSpans {
					path := "objects/" + string(span.ArtifactHash)
					if span.Space != model.OriginalBytes {
						if _, err := rebuilt.assets.put(path, assets.files[path]); err != nil {
							t.Fatal(err)
						}
					}
				}
				if err := rebuilt.add(block.Kind, block.RawText, block.SourceSpans); err != nil {
					t.Fatal(err)
				}
			}
			// finish must succeed for the attacks accepted by the old validator.
			forged, err := rebuilt.finish()
			if err != nil {
				t.Fatal(err)
			}
			forged.DetectorHash = document.DetectorHash
			detectorPath := "objects/" + string(document.DetectorHash)
			if _, err := rebuilt.assets.put(detectorPath, assets.files[detectorPath]); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(forged)
			if err != nil {
				t.Fatal(err)
			}
			key := manifestDigest(t, m)
			if _, err := rebuilt.assets.put(resultPath(key, "document.json"), encoded); err != nil {
				t.Fatal(err)
			}
			quality.DocumentHash = digest(encoded)
			replaceTestQuality(t, m, rebuilt.assets, quality)
			if err := validateTestAssets(t, m, data, rebuilt.assets); err == nil {
				t.Fatal("fully rehashed source root substitution accepted")
			}
		})
	}
}
