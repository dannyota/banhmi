package conversion

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"danny.vn/banhmi/internal/corpus/conversion/api"
	"danny.vn/banhmi/internal/corpus/model"
)

func testManifest(original []byte, media string) ConversionManifest {
	h := digest([]byte("synthetic"))
	recipe := []byte("synthetic-baseline-v1")
	return ConversionManifest{
		RequestKey: "convert-" + strings.Repeat("1", 32),
		Request: api.ConvertRequest{
			Artifact: model.CaptureRef{Country: "vn", SourceID: "synthetic", CaptureKey: "synthetic", Asset: model.AssetRef{Path: "original", SHA256: digest(original), ByteSize: int64(len(original))}, MediaType: media, RequestHash: h, ResponseHash: h, TransportReceipt: model.AssetRef{Path: "transport", SHA256: h, ByteSize: 1}},
			Recipe:   model.AssetRef{Path: "recipe", SHA256: digest(recipe), ByteSize: int64(len(recipe))},
			Budget:   model.ResourceBudget{MaxItems: 1000, MaxInputBytes: 1 << 20, MaxPages: 10, MaxElapsed: time.Second, MaxConcurrent: 1},
		},
		ObservationKey: "synthetic", ArtifactRole: "body", Language: "vi", HTMLSelector: "body", DetectorHash: h, CandidateDigest: h, OutputBytes: 1 << 20,
	}
}

func fixtureDOCX(t *testing.T, body []byte) []byte {
	t.Helper()
	var data bytes.Buffer
	writer := zip.NewWriter(&data)
	file, err := writer.Create(docxBodyPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestBlockAdaptersDeterminismAndAnchors(t *testing.T) {
	for _, format := range []string{"html", "docx"} {
		t.Run(format, func(t *testing.T) {
			path, media := "testdata/minimal.html", "text/html"
			if format == "docx" {
				path, media = "testdata/document.xml", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
			}
			source, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			original := source
			if format == "docx" {
				original = fixtureDOCX(t, source)
			}
			m := testManifest(original, media)
			var first *blockBuilder
			for attempt := 0; attempt < 2; attempt++ {
				b := newBlockBuilder(m)
				if format == "docx" {
					err = docxBlocks(original, b, 1<<20, 1000, 128)
				} else {
					err = htmlBlocks(original, b)
				}
				if err != nil {
					t.Fatal(err)
				}
				doc, err := b.finish()
				if err != nil || len(doc.Blocks) != 2 {
					t.Fatalf("finish: %v, blocks %d", err, len(doc.Blocks))
				}
				for _, block := range doc.Blocks {
					if block.Authority != model.AuthorityUnknown {
						t.Fatal("baseline asserted authority")
					}
					found := false
					for _, span := range block.SourceSpans {
						if span.Range == nil {
							if span.ReasonCode != "BYTE_MAPPING_UNAVAILABLE" || span.ArtifactHash != digest(original) {
								t.Fatal("missing original member provenance")
							}
							continue
						}
						var raw []byte
						if span.Space == model.OriginalBytes {
							raw = original
						} else {
							raw = b.assets.files["objects/"+string(span.ArtifactHash)]
						}
						if digest(raw) != span.ArtifactHash || span.Range.End > int64(len(raw)) {
							t.Fatal("source range does not resolve to exact hashed bytes")
						}
						fragment := raw[span.Range.Start:span.Range.End]
						if !bytes.Contains(fragment, []byte(block.RawText)) {
							t.Fatal("anchor does not round-trip to source text")
						}
						found = true
					}
					if !found {
						t.Fatal("block has no exact source range")
					}
				}
				for _, edit := range b.ledger {
					removed := b.assets.files[edit.Removed.Path]
					replacement := b.assets.files[edit.Replacement.Path]
					if !bytes.Equal(removed, []byte("á  ___")) || !bytes.Equal(replacement, []byte("á  ___")) {
						t.Fatal("ledger lost bytes or collapsed form blanks")
					}
				}
				if len(b.ledger) != 1 {
					t.Fatal("NFC edit not recorded")
				}
				if first != nil && (!reflect.DeepEqual(first.blocks, b.blocks) || !reflect.DeepEqual(first.maps, b.maps) || !reflect.DeepEqual(first.assets.files, b.assets.files)) {
					t.Fatal("adapter output changed for identical bytes")
				}
				first = b
			}
		})
	}
}

func TestBlockBoundsAndLostAnchors(t *testing.T) {
	m := testManifest([]byte("abc"), "text/html")
	b := newBlockBuilder(m)
	if err := b.add(model.BlockParagraph, "abc", nil); err == nil {
		t.Fatal("lost anchor accepted")
	}
	span := model.Span{ArtifactHash: m.Request.Artifact.Asset.SHA256, Space: model.OriginalBytes, Range: &model.ByteRange{Start: 0, End: 3}}
	m.OutputBytes = 2
	if err := newBlockBuilder(m).add(model.BlockParagraph, "abc", []model.Span{span}); err == nil {
		t.Fatal("output byte budget ignored")
	}
	m.OutputBytes = 1 << 20
	m.Request.Budget.MaxItems = 1
	if err := htmlBlocks([]byte("<body>abc</body>"), newBlockBuilder(m)); err == nil {
		t.Fatal("token budget ignored")
	}
}

func TestUnsupportedConstructsStayVisible(t *testing.T) {
	for _, text := range []string{"<body><table><tr><td>x</td></tr></table></body>", "<body><script>secret()</script><p>x</p></body>", "<body><img src='https://invalid.example/x'><p>x</p></body>"} {
		b := newBlockBuilder(testManifest([]byte(text), "text/html"))
		if err := htmlBlocks([]byte(text), b); err != nil {
			t.Fatal(err)
		}
		if len(b.findings) == 0 {
			t.Fatal("unsupported construct silently flattened")
		}
		for _, finding := range b.findings {
			if finding.Severity != model.SeverityBlocking || finding.Validate() != nil {
				t.Fatal("unsupported content is not a blocking scoped finding")
			}
		}
	}
}

func TestCanonicalSerializationIsStable(t *testing.T) {
	m := testManifest([]byte("abc"), "text/html")
	data, err := json.Marshal(m)
	if err != nil || m.validate() != nil || !bytes.Contains(data, []byte("CandidateDigest")) {
		t.Fatal("invalid synthetic manifest")
	}
}

func TestSourceAnchorsRejectUnknownMembersAndExcessiveRanges(t *testing.T) {
	m := testManifest([]byte("abc"), "text/html")
	for _, span := range []model.Span{
		{ArtifactHash: m.Request.Artifact.Asset.SHA256, Space: model.OriginalBytes, Range: &model.ByteRange{Start: 0, End: 4}},
		{ArtifactHash: digest([]byte("absent")), Space: model.OriginalBytes, Range: &model.ByteRange{Start: 0, End: 1}},
		{ArtifactHash: digest([]byte("absent")), Space: model.DecodedMember, Member: "word/document.xml", Range: &model.ByteRange{Start: 0, End: 1}},
	} {
		if err := newBlockBuilder(m).add(model.BlockParagraph, "abc", []model.Span{span}); err == nil {
			t.Fatal("unresolvable anchor accepted")
		}
	}
}

func TestDOCXUnsupportedRegionsAndLimits(t *testing.T) {
	const prefix = `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`
	const suffix = `</w:body></w:document>`
	for _, construct := range []string{
		`<w:tbl><w:tr><w:tc><w:p><w:r><w:t>cell</w:t></w:r></w:p></w:tc></w:tr></w:tbl>`,
		`<w:ins><w:p><w:r><w:t>revision</w:t></w:r></w:p></w:ins>`,
		`<w:p><w:rPr><w:rFonts w:ascii="VNI"/></w:rPr><w:r><w:t>ambiguous</w:t></w:r></w:p>`,
	} {
		data := fixtureDOCX(t, []byte(prefix+construct+suffix))
		b := newBlockBuilder(testManifest(data, "application/vnd.openxmlformats-officedocument.wordprocessingml.document"))
		if err := docxBlocks(data, b, 1<<20, 1000, 128); err != nil {
			t.Fatal(err)
		}
		if len(b.findings) == 0 || len(b.blocks) == 0 {
			t.Fatal("unsupported DOCX region disappeared")
		}
		if err := docxBlocks(data, newBlockBuilder(b.manifest), 1, 1000, 128); err == nil {
			t.Fatal("expanded byte limit ignored")
		}
		if err := docxBlocks(data, newBlockBuilder(b.manifest), 1<<20, 1000, 1); err == nil {
			t.Fatal("XML depth limit ignored")
		}
	}
}

func TestHTMLSourceTokenDecodingPreservesCapturedBytes(t *testing.T) {
	data := []byte("<body><p>A&amp;B\r\n  ___</p></body>")
	b := newBlockBuilder(testManifest(data, "text/html"))
	if err := htmlBlocks(data, b); err != nil {
		t.Fatal(err)
	}
	block := b.blocks[0]
	span := block.SourceSpans[0]
	if string(data[span.Range.Start:span.Range.End]) != "A&amp;B\r\n  ___" || block.RawText != "A&B\r\n  ___" {
		t.Fatal("source token offsets were replaced by decoded text offsets")
	}
	if string(b.assets.files[block.NormalizedText.Path]) != "A&B\n  ___" || len(b.ledger) != 1 || string(b.assets.files[b.ledger[0].Removed.Path]) != block.RawText {
		t.Fatal("CRLF normalization is not exactly reversible")
	}
}

func TestStagedLedgerReversalReproducesAllRawConverterText(t *testing.T) {
	data := []byte("<body><p>á\r\n  ___</p><p>unchanged</p><p>ắ</p></body>")
	b := newBlockBuilder(testManifest(data, "text/html"))
	if err := htmlBlocks(data, b); err != nil {
		t.Fatal(err)
	}
	document, err := b.finish()
	if err != nil {
		t.Fatal(err)
	}
	var ledger []model.EditLedgerEntry
	if err := json.Unmarshal(b.assets.files[document.EditLedger.Path], &ledger); err != nil {
		t.Fatal(err)
	}
	var restored []byte
	for _, block := range document.Blocks {
		normal := string(b.assets.files[block.NormalizedText.Path])
		raw := normal
		for _, entry := range ledger {
			if entry.Inputs[0].ArtifactHash != digest([]byte(block.RawText)) {
				continue
			}
			var edit model.DecodedEdit
			if err := json.Unmarshal(b.assets.files[entry.Evidence.Path], &edit); err != nil {
				t.Fatal(err)
			}
			if entry.Validate() != nil || edit.Removed != string(b.assets.files[entry.Removed.Path]) || edit.Replacement != string(b.assets.files[entry.Replacement.Path]) {
				t.Fatal("staged ledger evidence disagrees with retained bytes")
			}
			value := model.MappedValue{Text: normal, EditLedger: []model.DecodedEdit{edit}}
			raw, err = reverseText(value)
			if err != nil {
				t.Fatal(err)
			}
		}
		if raw != block.RawText {
			t.Fatal("staged ledger cannot recover a block")
		}
		restored = append(restored, raw...)
	}
	if !bytes.Equal(restored, b.assets.files[document.RawText.Path]) {
		t.Fatal("staged reversal differs from raw converter bytes")
	}
}

func TestHTMLRejectsAmbiguousEncodingAndMissingSelector(t *testing.T) {
	for _, text := range []string{
		`<html><head><meta charset="windows-1258"></head><body>x</body></html>`,
		`<html><head><meta http-equiv="Content-Type" content="text/html; charset=windows-1258"></head><body>x</body></html>`,
		`<body id="a">one</body><body id="a">two</body>`,
	} {
		if err := htmlBlocks([]byte(text), newBlockBuilder(testManifest([]byte(text), "text/html"))); err == nil {
			t.Fatal("ambiguous HTML input accepted")
		}
	}
}

func TestIrreversibleStagedEditFailsWithBlockingFinding(t *testing.T) {
	data := []byte("<body>á</body>")
	for _, failure := range []string{"removed", "replacement", "evidence", "range"} {
		b := newBlockBuilder(testManifest(data, "text/html"))
		if err := htmlBlocks(data, b); err != nil {
			t.Fatal(err)
		}
		entry := &b.ledger[0]
		switch failure {
		case "removed":
			ref, err := b.assets.object([]byte("different"))
			if err != nil {
				t.Fatal(err)
			}
			entry.Removed = &ref
		case "replacement":
			b.assets.files[entry.Replacement.Path] = []byte("changed")
		case "evidence":
			entry.Rule = "unreviewed"
		case "range":
			entry.Inputs[0].Range.End++
		}
		if _, err := b.finish(); err == nil {
			t.Fatal("irreversible staged ledger accepted")
		}
		if len(b.findings) == 0 || b.findings[len(b.findings)-1].ReasonCode != "IRREVERSIBLE_EDIT" || b.findings[len(b.findings)-1].Severity != model.SeverityBlocking {
			t.Fatal("irreversible edit has no blocking finding")
		}
	}
}

func TestStagedMapRejectsLostAnchorAndUnorderedTransform(t *testing.T) {
	data := []byte("<body>abc</body>")
	for _, failure := range []string{"hash", "range", "order", "transform"} {
		b := newBlockBuilder(testManifest(data, "text/html"))
		if err := htmlBlocks(data, b); err != nil {
			t.Fatal(err)
		}
		switch failure {
		case "hash":
			b.maps[0].Sources[0].ArtifactHash = digest([]byte("absent"))
		case "range":
			b.maps[1].Destinations[0].Range.End = 1000
		case "order":
			b.maps[0].Ordinal++
		case "transform":
			b.maps[0].Transform = "unreviewed"
		}
		if _, err := b.finish(); err == nil {
			t.Fatal("invalid staged map accepted")
		}
	}
}
