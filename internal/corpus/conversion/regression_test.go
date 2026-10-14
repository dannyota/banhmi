//go:build linux

package conversion

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"danny.vn/banhmi/internal/corpus/conversion/api"
	"danny.vn/banhmi/internal/corpus/conversion/sandbox"
	"danny.vn/banhmi/internal/corpus/model"
)

func TestDeletedLedgerRejectsChangedText(t *testing.T) {
	testDamagedChain(t, "deleted-ledger")
}

func TestReorderedLedgerRejectsChangedText(t *testing.T) {
	testDamagedChain(t, "reordered-ledger")
}

func TestDeletedMapRejectsUncoveredText(t *testing.T) {
	testDamagedChain(t, "deleted-map")
}

func TestDisconnectedMapRejectsUncoveredText(t *testing.T) {
	testDamagedChain(t, "disconnected-map")
}

func testDamagedChain(t *testing.T, damage string) {
	t.Helper()
	data := []byte("<body><p>á</p><p>é</p></body>")
	b := newBlockBuilder(testManifest(data, "text/html"))
	if err := htmlBlocks(data, b); err != nil {
		t.Fatal(err)
	}
	switch damage {
	case "deleted-ledger":
		b.ledger = nil
	case "reordered-ledger":
		b.ledger[0], b.ledger[1] = b.ledger[1], b.ledger[0]
		for i := range b.ledger {
			b.ledger[i].Ordinal = i
		}
	case "deleted-map":
		b.maps = b.maps[:len(b.maps)-1]
	case "disconnected-map":
		b.maps[1].Sources = b.maps[3].Sources
	}
	if _, err := b.finish(); err == nil {
		t.Fatal("incomplete transform chain accepted")
	}
	last := b.findings[len(b.findings)-1]
	if last.ReasonCode != "IRREVERSIBLE_EDIT" || last.Severity != model.SeverityBlocking {
		t.Fatal("damaged transform chain lacks blocking finding")
	}
}

func TestHTMLStructuralBlockBoundaries(t *testing.T) {
	for _, fixture := range []struct {
		name string
		html string
		text []string
		kind []model.BlockKind
	}{
		{"inline-heading", "<body><h1>Điều <b>1</b></h1></body>", []string{"Điều 1"}, []model.BlockKind{model.BlockHeading}},
		{"formatted-paragraph", "<body><p>A <b>B</b> <em>C</em></p><p>D</p></body>", []string{"A B C", "D"}, []model.BlockKind{model.BlockParagraph, model.BlockParagraph}},
		{"empty-paragraph", "<body><p></p><p>x</p><p></p></body>", []string{"", "x", ""}, []model.BlockKind{model.BlockParagraph, model.BlockParagraph, model.BlockParagraph}},
		{"nested-lists", "<body><ul><li>outer<ul><li>inner</li></ul></li><li>next</li></ul></body>", []string{"outer", "inner", "next"}, []model.BlockKind{model.BlockListItem, model.BlockListItem, model.BlockListItem}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			data := []byte(fixture.html)
			b := newBlockBuilder(testManifest(data, "text/html"))
			if err := htmlBlocks(data, b); err != nil {
				t.Fatal(err)
			}
			if _, err := b.finish(); err != nil {
				t.Fatal(err)
			}
			if len(b.blocks) != len(fixture.text) {
				t.Fatalf("got %d structural blocks", len(b.blocks))
			}
			for i, block := range b.blocks {
				if block.RawText != fixture.text[i] || block.Kind != fixture.kind[i] || len(block.SourceSpans) == 0 {
					t.Fatal("structural boundary or source anchors lost")
				}
			}
			if fixture.name == "inline-heading" && len(b.blocks[0].SourceSpans) != 2 {
				t.Fatal("inline heading lost a contributing text token")
			}
			if fixture.name == "nested-lists" {
				found := false
				for _, finding := range b.findings {
					found = found || finding.ReasonCode == "NESTED_HTML_STRUCTURE_UNQUALIFIED"
				}
				if !found {
					t.Fatal("unqualified nested hierarchy lacks a finding")
				}
			}
		})
	}
}

func TestSemanticSubstitutionUnderCurrentFilenamesIsRejected(t *testing.T) {
	original := []byte("<body>current</body>")
	m := testManifest(original, "text/html")
	other := []byte("<body>stale</body>")
	stale := testManifest(other, "text/html")
	req := childRequest(t, other, stale)
	if err := ChildHandler(req); err != nil {
		t.Fatal(err)
	}
	r, refs := readChildOutputs(t, req)
	for i := range refs {
		for _, name := range []string{"document.json", "quality.json"} {
			if refs[i].Path == resultPath(manifestDigest(t, stale), name) {
				refs[i].Path = resultPath(manifestDigest(t, m), name)
			}
		}
	}
	var err error
	r.manifest, err = json.Marshal(refs)
	if err != nil {
		t.Fatal(err)
	}
	retained := 0
	c := &Converter{runner: &fakeSandbox{t: t, run: r}, inputDir: childRequest(t, original, m).InputDir, manifest: m, retain: func(context.Context, model.AssetRef, io.Reader) error {
		retained++
		return nil
	}}
	conversion, quality, err := c.Convert(context.Background(), api.ConvertEnvelope{Key: m.RequestKey, Manifest: manifestDigest(t, m)})
	if err == nil || retained != 0 || conversion.Key != "" || quality.Key != "" || !r.closed {
		t.Fatal("stale semantic content retained under current request names")
	}
}

func TestTypedResourceFailureSurvivesConvert(t *testing.T) {
	for _, bound := range []sandbox.Bound{sandbox.BoundOutput, sandbox.BoundInput, sandbox.BoundExpanded, sandbox.BoundXML, sandbox.BoundRatio} {
		m := testManifest([]byte("<body>abc</body>"), "text/html")
		c := &Converter{runner: &fakeSandbox{t: t, err: &sandbox.Failure{Cause: sandbox.ResourceLimit, Bound: bound}}, manifest: m, retain: func(context.Context, model.AssetRef, io.Reader) error {
			t.Fatal("retained after resource failure")
			return nil
		}}
		_, _, err := c.Convert(context.Background(), api.ConvertEnvelope{Key: m.RequestKey, Manifest: manifestDigest(t, m)})
		var failure *sandbox.Failure
		if !errors.As(err, &failure) || failure.Cause != sandbox.ResourceLimit || failure.Bound != bound {
			t.Fatalf("resource classification lost for %s", bound)
		}
	}
}

// ChildMain changes process limits and closes runtime descriptors. A fresh test
// subprocess is required; it must exit before returning to the testing runtime.
func TestConversionChildEntry(t *testing.T) {
	if os.Getenv("CONVERSION_CHILD_ENTRY") != "1" {
		return
	}
	if err := sandbox.ChildMain(os.Stdin, os.Stdout, ChildHandler); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func childMainResponse(t *testing.T, req sandbox.ChildRequest) *sandbox.Failure {
	t.Helper()
	req.Phase = "execute"
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestConversionChildEntry$")
	command.Env = []string{"CONVERSION_CHILD_ENTRY=1"}
	command.Stdin = bytes.NewReader(data)
	out, err := command.Output()
	if err != nil {
		t.Fatal("ChildMain subprocess failed")
	}
	var response struct {
		Outputs []model.AssetRef
		Failure *sandbox.Failure
	}
	if decodeJSON(out, &response) != nil {
		t.Fatal("ChildMain response invalid")
	}
	return response.Failure
}

func TestJSONBundleAdmissionThroughChildMain(t *testing.T) {
	for _, format := range []string{"html-void", "docx"} {
		t.Run(format, func(t *testing.T) {
			data, media := []byte("<body>A<br>B</body>"), "text/html"
			if format == "docx" {
				body, err := os.ReadFile("testdata/document.xml")
				if err != nil {
					t.Fatal(err)
				}
				data = fixtureDOCX(t, body)
				media = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
			}
			m := testManifest(data, media)
			req := childRequest(t, data, m)
			if failure := childMainResponse(t, req); failure != nil {
				t.Fatalf("JSON admission failed: %s", failure)
			}
			if _, err := os.Stat(filepath.Join(req.OutputDir, "manifest.json")); err != nil {
				t.Fatal("admitted conversion produced no manifest")
			}
			staged, refs := readChildOutputs(t, req)
			request, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			bundle := inputBundle{Version: "bundle-v1", Mode: "validate", Request: request, Original: base64.StdEncoding.EncodeToString(data)}
			for i, ref := range refs {
				content, err := os.ReadFile(staged.paths[i])
				if err != nil {
					t.Fatal(err)
				}
				bundle.Outputs = append(bundle.Outputs, bundledOutput{Ref: ref, Data: base64.StdEncoding.EncodeToString(content)})
			}
			encoded, err := json.Marshal(bundle)
			if err != nil {
				t.Fatal(err)
			}
			req.InputDir, req.OutputDir = t.TempDir(), t.TempDir()
			if err := os.WriteFile(filepath.Join(req.InputDir, "input.json"), encoded, 0600); err != nil {
				t.Fatal(err)
			}
			if failure := childMainResponse(t, req); failure != nil {
				t.Fatalf("isolated semantic validation failed: %s", failure)
			}
			if _, err := os.Stat(filepath.Join(req.OutputDir, "verdict.json")); err != nil {
				t.Fatal("semantic validation produced no verdict")
			}
		})
	}
}

func TestHandlerOutputLimitSurvivesChildMain(t *testing.T) {
	data := []byte("<body>abc</body>")
	m := testManifest(data, "text/html")
	m.OutputBytes = 1
	req := childRequest(t, data, m)
	failure := childMainResponse(t, req)
	if failure == nil || failure.Cause != sandbox.ResourceLimit || failure.Bound != sandbox.BoundOutput {
		t.Fatal("handler output resource classification lost at ChildMain")
	}
}

func TestValidationVerdictRejectsRequestOrOutputSubstitution(t *testing.T) {
	for _, damage := range []string{"request", "output-hash", "extra-field", "oversized"} {
		t.Run(damage, func(t *testing.T) {
			data := []byte("<body>abc</body>")
			m := testManifest(data, "text/html")
			req := childRequest(t, data, m)
			if err := ChildHandler(req); err != nil {
				t.Fatal(err)
			}
			r, _ := readChildOutputs(t, req)
			f := &fakeSandbox{t: t, run: r, validationEdit: func(verdictRun *fakeStaged) {
				var refs []model.AssetRef
				if decodeJSON(verdictRun.manifest, &refs) != nil {
					t.Fatal("invalid test verdict manifest")
				}
				data, err := os.ReadFile(verdictRun.paths[0])
				if err != nil {
					t.Fatal(err)
				}
				var verdict validationVerdict
				if decodeJSON(data, &verdict) != nil {
					t.Fatal("invalid test verdict")
				}
				switch damage {
				case "request":
					verdict.Request = digest([]byte("other-request"))
				case "output-hash":
					verdict.Outputs[0].SHA256 = digest([]byte("other-output"))
				}
				data, err = json.Marshal(verdict)
				if err != nil {
					t.Fatal(err)
				}
				if damage == "extra-field" {
					data = append(data[:len(data)-1], []byte(`,"Unknown":true}`)...)
				}
				if damage == "oversized" {
					data = bytes.Repeat([]byte(" "), verdictLimit+1)
				}
				if err := os.WriteFile(verdictRun.paths[0], data, 0600); err != nil {
					t.Fatal(err)
				}
				refs[0].SHA256, refs[0].ByteSize = digest(data), int64(len(data))
				verdictRun.manifest, err = json.Marshal(refs)
				if err != nil {
					t.Fatal(err)
				}
			}}
			retained := 0
			c := &Converter{runner: f, inputDir: req.InputDir, manifest: m, retain: func(context.Context, model.AssetRef, io.Reader) error {
				retained++
				return nil
			}}
			conversion, quality, err := c.Convert(context.Background(), api.ConvertEnvelope{Key: m.RequestKey, Manifest: manifestDigest(t, m)})
			if err == nil || conversion.Key != "" || quality.Key != "" || retained != 0 || !r.closed || f.validation == nil || !f.validation.closed {
				t.Fatal("invalid validation verdict returned success or leaked a receipt")
			}
			if damage == "oversized" && f.validation.opens != 0 {
				t.Fatal("oversized verdict opened before size rejection")
			}
		})
	}
}

func TestValidationRunPreservesTypedResourceFailure(t *testing.T) {
	data := []byte("<body>abc</body>")
	m := testManifest(data, "text/html")
	req := childRequest(t, data, m)
	if err := ChildHandler(req); err != nil {
		t.Fatal(err)
	}
	r, _ := readChildOutputs(t, req)
	f := &fakeSandbox{t: t, run: r, validationErr: &sandbox.Failure{Cause: sandbox.ResourceLimit, Bound: sandbox.BoundMemory}}
	c := &Converter{runner: f, inputDir: req.InputDir, manifest: m, retain: func(context.Context, model.AssetRef, io.Reader) error {
		t.Fatal("retained before semantic validation")
		return nil
	}}
	_, _, err := c.Convert(context.Background(), api.ConvertEnvelope{Key: m.RequestKey, Manifest: manifestDigest(t, m)})
	var failure *sandbox.Failure
	if !errors.As(err, &failure) || failure.Cause != sandbox.ResourceLimit || failure.Bound != sandbox.BoundMemory || !r.closed || f.calls != 2 {
		t.Fatal("validation resource failure lost classification or leaked receipt")
	}
}

func TestDOCXFormatResourceBounds(t *testing.T) {
	body := []byte(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>x</w:t></w:r></w:p></w:body></w:document>`)
	for _, dimension := range []string{"members", "expanded", "ratio", "depth", "tokens"} {
		t.Run(dimension, func(t *testing.T) {
			data := fixtureDOCX(t, body)
			m := testManifest(data, "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
			expanded, members, depth, ratio := int64(1<<20), 1000, 128, int64(100)
			bound := sandbox.BoundMembers
			switch dimension {
			case "members":
				members = 0
			case "expanded":
				expanded, bound = 1, sandbox.BoundExpanded
			case "ratio":
				ratio, bound = 1, sandbox.BoundRatio
			case "depth":
				depth, bound = 1, sandbox.BoundXML
			case "tokens":
				m.Request.Budget.MaxItems = 1
			}
			err := docxBlocksBounded(data, newBlockBuilder(m), expanded, members, depth, ratio)
			var failure *sandbox.Failure
			if !errors.As(err, &failure) || failure.Cause != sandbox.ResourceLimit || failure.Bound != bound {
				t.Fatalf("DOCX %s resource classification lost", dimension)
			}
		})
	}
}

func TestSemanticBindingsAndObjectClosure(t *testing.T) {
	for _, damage := range []string{"recipe", "candidate", "detector", "observation", "role", "schema", "report", "missing-object", "extra-object"} {
		t.Run(damage, func(t *testing.T) {
			data := []byte("<body>á</body>")
			m := testManifest(data, "text/html")
			req := childRequest(t, data, m)
			if err := ChildHandler(req); err != nil {
				t.Fatal(err)
			}
			staged, refs := readChildOutputs(t, req)
			assets := &artifactSet{files: map[string][]byte{}, limit: m.OutputBytes, items: m.Request.Budget.MaxItems}
			for i, ref := range refs {
				content, err := os.ReadFile(staged.paths[i])
				if err != nil {
					t.Fatal(err)
				}
				if _, err := assets.put(ref.Path, content); err != nil {
					t.Fatal(err)
				}
			}
			key := manifestDigest(t, m)
			documentPath, qualityPath := resultPath(key, "document.json"), resultPath(key, "quality.json")
			var document model.ConvertedDocument
			var quality qualityReport
			if decodeJSON(assets.files[documentPath], &document) != nil || decodeJSON(assets.files[qualityPath], &quality) != nil {
				t.Fatal("test outputs invalid")
			}
			switch damage {
			case "recipe":
				document.RecipeHash = digest([]byte("other-recipe"))
			case "candidate":
				document.CandidateDigest = digest([]byte("other-candidate"))
			case "detector":
				document.DetectorHash = digest([]byte("other-detector"))
			case "observation":
				document.ObservationKey = "other-observation"
			case "role":
				document.ArtifactRole = "other-role"
			case "schema":
				document.SchemaVersion = "blocks-unknown"
			case "report":
				quality.DocumentHash = digest([]byte("other-document"))
			case "missing-object":
				delete(assets.files, document.RawText.Path)
			case "extra-object":
				if _, err := assets.object([]byte("unreferenced")); err != nil {
					t.Fatal(err)
				}
			}
			var err error
			assets.files[documentPath], err = json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			// Keep the report's document hash current when attacking other bindings.
			if damage != "report" {
				quality.DocumentHash = digest(assets.files[documentPath])
			}
			assets.files[qualityPath], err = json.Marshal(quality)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateSemantics(m, key, assets); err == nil {
				t.Fatal("semantic substitution or broken object closure accepted")
			}
		})
	}
}

func TestEmptyStructuralBlocksPassIsolatedSemanticValidation(t *testing.T) {
	data := []byte("<body><h1>A <b>B</b></h1><p></p><p>C<br>D</p></body>")
	m := testManifest(data, "text/html")
	req := childRequest(t, data, m)
	if err := ChildHandler(req); err != nil {
		t.Fatal(err)
	}
	r, _ := readChildOutputs(t, req)
	f := &fakeSandbox{t: t, run: r}
	c := &Converter{runner: f, inputDir: req.InputDir, manifest: m, retain: func(_ context.Context, ref model.AssetRef, reader io.Reader) error {
		content, err := io.ReadAll(reader)
		if err != nil || !matchesAsset(content, ref) {
			t.Fatal("retained bytes differ")
		}
		return nil
	}}
	conversion, quality, err := c.Convert(context.Background(), api.ConvertEnvelope{Key: m.RequestKey, Manifest: manifestDigest(t, m)})
	if err != nil || conversion.Validate() != nil || quality.Validate() != nil || f.calls != 2 || !r.closed || f.validation == nil || !f.validation.closed {
		t.Fatalf("empty structural block failed semantic handoff: %v", err)
	}
}

func TestBundleRejectsNoncanonicalOrOversizedBase64(t *testing.T) {
	for _, damage := range []string{"newline", "invalid", "recipe-limit", "original-limit"} {
		t.Run(damage, func(t *testing.T) {
			data := []byte("<body>abc</body>")
			m := testManifest(data, "text/html")
			req := childRequest(t, data, m)
			path := filepath.Join(req.InputDir, "input.json")
			encoded, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var bundle inputBundle
			if decodeJSON(encoded, &bundle) != nil {
				t.Fatal("invalid test bundle")
			}
			switch damage {
			case "newline":
				bundle.Recipe += "\n"
				decoded, err := base64.StdEncoding.Strict().DecodeString(bundle.Recipe)
				if err != nil || !matchesAsset(decoded, m.Request.Recipe) || len(bundle.Recipe) > base64.StdEncoding.EncodedLen(64<<10) {
					t.Fatal("noncanonical recipe does not isolate canonical enforcement")
				}
			case "invalid":
				bundle.Original = "!"
			case "recipe-limit":
				bundle.Recipe = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("x"), (64<<10)+1))
			case "original-limit":
				bundle.Original = base64.StdEncoding.EncodeToString(append(data, 'x'))
			}
			encoded, err = json.Marshal(bundle)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, encoded, 0600); err != nil {
				t.Fatal(err)
			}
			err = ChildHandler(req)
			if err == nil {
				t.Fatal("invalid base64 bundle admitted")
			}
			if damage == "recipe-limit" || damage == "original-limit" {
				var failure *sandbox.Failure
				if !errors.As(err, &failure) || failure.Cause != sandbox.ResourceLimit || failure.Bound != sandbox.BoundInput {
					t.Fatal("decoded input limit lost classification")
				}
			}
		})
	}
}
