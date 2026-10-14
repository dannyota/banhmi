//go:build linux

package conversion

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"danny.vn/banhmi/internal/corpus/conversion/api"
	"danny.vn/banhmi/internal/corpus/conversion/sandbox"
	"danny.vn/banhmi/internal/corpus/model"
)

type fakeStaged struct {
	paths    []string
	manifest []byte
	closed   bool
	failOpen bool
	failMeta bool
	failEnd  bool
}

func (r *fakeStaged) ManifestBytes() ([]byte, error) {
	if r.failMeta {
		return nil, errors.New("synthetic")
	}
	return r.manifest, nil
}
func (r *fakeStaged) Open(index int) (*os.File, error) {
	if r.failOpen {
		return nil, errors.New("synthetic")
	}
	return os.Open(r.paths[index])
}
func (r *fakeStaged) Close() error {
	r.closed = true
	if r.failEnd {
		return errors.New("synthetic")
	}
	return nil
}
func (*fakeStaged) TerminationMechanisms() []string { return []string{"synthetic"} }

type fakeSandbox struct {
	t              *testing.T
	calls          int
	first          sandbox.Attempt
	run            *fakeStaged
	err            error
	attempt        sandbox.Attempt
	validation     *fakeStaged
	validationErr  error
	validationEdit func(*fakeStaged)
}

func (r *fakeSandbox) Run(_ context.Context, attempt sandbox.Attempt) (stagedRun, error) {
	r.calls++
	if r.calls == 1 {
		r.first = attempt
		if r.run == nil {
			return nil, r.err
		}
		return r.run, r.err
	}
	r.attempt = attempt
	if r.validationErr != nil {
		return nil, r.validationErr
	}
	req := sandbox.ChildRequest{InputDir: attempt.InputDir, OutputDir: r.t.TempDir(), ScratchDir: r.t.TempDir(), InputType: attempt.InputType, Limits: sandbox.OfficeLimits()}
	if err := ChildHandler(req); err != nil {
		return nil, err
	}
	receipt, _ := readChildOutputs(r.t, req)
	r.validation = receipt
	if r.validationEdit != nil {
		r.validationEdit(receipt)
	}
	return receipt, nil
}

func TestConverterStagedIntegrityAndCleanup(t *testing.T) {
	for _, failure := range []string{"", "hash", "size", "manifest", "open", "run", "retain", "close", "path", "duplicate", "missing", "order", "budget"} {
		t.Run(failure, func(t *testing.T) {
			original := []byte("<body>abc</body>")
			m := testManifest(original, "text/html")
			keyHash := manifestDigest(t, m)
			req := childRequest(t, original, m)
			if err := ChildHandler(req); err != nil {
				t.Fatal(err)
			}
			r, refs := readChildOutputs(t, req)
			f := &fakeSandbox{t: t, run: r}
			switch failure {
			case "hash":
				refs[1].SHA256 = digest([]byte("different"))
			case "size":
				refs[1].ByteSize--
			case "open":
				r.failOpen = true
			case "run":
				f.err = errors.New("synthetic")
			case "close":
				r.failEnd = true
			case "path":
				refs[0].Path = "../escape"
			case "duplicate":
				refs[1].Path = refs[0].Path
			case "missing":
				refs = refs[:1]
			case "order":
				refs[0], refs[1] = refs[1], refs[0]
			case "budget":
				refs[1].ByteSize = m.OutputBytes + 1
			}
			var err error
			r.manifest, err = json.Marshal(refs)
			if err != nil {
				t.Fatal(err)
			}
			if failure == "manifest" {
				r.manifest = []byte("invalid")
			}
			retained := 0
			c := &Converter{runner: f, inputDir: "/absent-artifact-input", manifest: m, retain: func(_ context.Context, _ model.AssetRef, data io.Reader) error {
				if failure == "retain" {
					return errors.New("synthetic")
				}
				_, err := io.Copy(io.Discard, data)
				retained++
				return err
			}}
			conversion, quality, err := c.Convert(context.Background(), api.ConvertEnvelope{Key: m.RequestKey, Manifest: keyHash})
			if !r.closed {
				t.Fatal("receipt was not closed")
			}
			if failure == "" {
				if err != nil || conversion.Validate() != nil || quality.Validate() != nil || retained != len(refs)+1 {
					t.Fatalf("valid staged result rejected: %v, retained %d", err, retained)
				}
				if f.first.InputDir != "/absent-artifact-input" || f.first.InputType != sandbox.TypeJSON || f.calls != 2 {
					t.Fatal("artifact was not delegated to sandbox")
				}
			} else if err == nil || conversion.Key != "" || quality.Key != "" {
				t.Fatal("failed output returned success envelopes")
			}
			if (failure == "hash" || failure == "size" || failure == "budget") && retained != 0 {
				t.Fatal("retained output before validating all hashes and lengths")
			}
		})
	}
}

func TestConverterRejectsUnboundRequest(t *testing.T) {
	m := testManifest([]byte("abc"), "text/html")
	c := &Converter{manifest: m}
	if _, _, err := c.Convert(context.Background(), api.ConvertEnvelope{Key: m.RequestKey, Manifest: digest([]byte("wrong"))}); err == nil {
		t.Fatal("accepted unbound request")
	}
	if _, err := NewConverter(nil, "/synthetic", m, func(context.Context, model.AssetRef, io.Reader) error { return nil }); err == nil {
		t.Fatal("accepted missing supervisor")
	}
}

func manifestDigest(t *testing.T, m ConversionManifest) model.Digest {
	t.Helper()
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return digest(data)
}

func TestCanonicalBlockHashFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/expected.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Format    string
		Ordinal   int
		Canonical []string
		Start     int64
		End       int64
	}
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures {
		path, media := "testdata/minimal.html", "text/html"
		if fixture.Format == "docx" {
			path, media = "testdata/document.xml", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
		}
		original, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if fixture.Format == "docx" {
			original = fixtureDOCX(t, original)
		}
		b := newBlockBuilder(testManifest(original, media))
		if fixture.Format == "docx" {
			err = docxBlocks(original, b, 1<<20, 1000, 128)
		} else {
			err = htmlBlocks(original, b)
		}
		if err != nil {
			t.Fatal(err)
		}
		matchedAnchor := false
		for _, span := range b.blocks[fixture.Ordinal].SourceSpans {
			if span.Range != nil && span.Range.Start == fixture.Start && span.Range.End == fixture.End {
				matchedAnchor = true
			}
		}
		if !matchedAnchor {
			t.Fatal("adapter anchor differs from authored fixture")
		}
		canonical, err := json.Marshal(fixture.Canonical)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(canonical)
		if !strings.HasSuffix(b.blocks[fixture.Ordinal].ID, hex.EncodeToString(sum[:])) {
			t.Fatal("block does not match authored canonical hash fixture")
		}
		if !bytes.Equal(b.assets.files[b.blocks[fixture.Ordinal].NormalizedText.Path], []byte(fixture.Canonical[3])) {
			t.Fatal("normalized fixture text mismatch")
		}
	}
}

func childRequest(t *testing.T, original []byte, m ConversionManifest) sandbox.ChildRequest {
	t.Helper()
	input, output := t.TempDir(), t.TempDir()
	request, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := json.Marshal(inputBundle{Version: "bundle-v1", Mode: "convert", Request: request, Original: base64.StdEncoding.EncodeToString(original), Recipe: base64.StdEncoding.EncodeToString([]byte("synthetic-baseline-v1"))})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(input, "input.json"), bundle, 0600); err != nil {
		t.Fatal(err)
	}
	limits := sandbox.OfficeLimits()
	limits.InputBytes, err = BundleInputBytes(m)
	if err != nil {
		t.Fatal(err)
	}
	return sandbox.ChildRequest{InputDir: input, OutputDir: output, ScratchDir: t.TempDir(), InputType: sandbox.TypeJSON, Limits: limits}
}

func readChildOutputs(t *testing.T, req sandbox.ChildRequest) (*fakeStaged, []model.AssetRef) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(req.OutputDir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest sandbox.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	var refs []model.AssetRef
	staged := &fakeStaged{}
	for _, output := range manifest.Outputs {
		local := filepath.Join(req.OutputDir, filepath.FromSlash(output.Path))
		data, err := os.ReadFile(local)
		if err != nil || int64(len(data)) != output.Size {
			t.Fatalf("invalid staged output: %v", err)
		}
		staged.paths = append(staged.paths, local)
		refs = append(refs, model.AssetRef{Path: output.Path, SHA256: digest(data), ByteSize: output.Size})
	}
	staged.manifest, err = json.Marshal(refs)
	if err != nil {
		t.Fatal(err)
	}
	return staged, refs
}

func TestChildHandlerProducesPerArtifactQuarantineAndVerifiedOutputs(t *testing.T) {
	for _, format := range []string{"html", "docx"} {
		t.Run(format, func(t *testing.T) {
			path, media := "testdata/minimal.html", "text/html"
			if format == "docx" {
				path, media = "testdata/document.xml", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
			}
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if format == "docx" {
				original = fixtureDOCX(t, original)
			}
			m := testManifest(original, media)
			req := childRequest(t, original, m)
			if err := ChildHandler(req); err != nil {
				t.Fatal(err)
			}
			staged, refs := readChildOutputs(t, req)
			key := manifestDigest(t, m)
			for _, ref := range refs {
				data, err := os.ReadFile(filepath.Join(req.OutputDir, filepath.FromSlash(ref.Path)))
				if err != nil {
					t.Fatal(err)
				}
				if ref.Path == resultPath(key, "document.json") {
					var document model.ConvertedDocument
					if json.Unmarshal(data, &document) != nil || document.Validate(m.Request.Budget.MaxItems) != nil || document.Original.Asset != m.Request.Artifact.Asset {
						t.Fatal("invalid document or original binding")
					}
				}
				if ref.Path == resultPath(key, "quality.json") {
					var report struct {
						Outcome      model.QualityOutcome
						ArtifactHash model.Digest
						Metrics      []model.QualityMetric
						Findings     []model.Finding
					}
					if err := json.Unmarshal(data, &report); err != nil {
						t.Fatal(err)
					}
					if report.Outcome != model.QualityQuarantined || report.ArtifactHash != m.Request.Artifact.Asset.SHA256 || len(report.Metrics) != 2 {
						t.Fatal("baseline report claimed acceptance or lost artifact binding")
					}
					for _, metric := range report.Metrics {
						if metric.Validate() != nil || metric.State != model.MetricUnknown || metric.Passed || !metric.Mandatory {
							t.Fatal("unknown geometry or fidelity passed")
						}
					}
					if len(report.Findings) < 2 {
						t.Fatal("unknown geometry and fidelity findings missing")
					}
				}
			}
			c := &Converter{runner: &fakeSandbox{t: t, run: staged}, inputDir: "/unreadable-original", manifest: m, retain: func(_ context.Context, ref model.AssetRef, reader io.Reader) error {
				data, err := io.ReadAll(reader)
				if err != nil || digest(data) != ref.SHA256 || int64(len(data)) != ref.ByteSize {
					return errors.New("retained bytes differ")
				}
				return nil
			}}
			if _, _, err := c.Convert(context.Background(), api.ConvertEnvelope{Key: m.RequestKey, Manifest: key}); err != nil || !staged.closed {
				t.Fatalf("trusted handoff failed: %v", err)
			}
		})
	}
}

func TestChildHandlerRejectsMismatchAndBudgets(t *testing.T) {
	original := []byte("<body><p>abc</p></body>")
	for _, failure := range []string{"hash", "length", "format", "selector", "input", "expanded", "output", "items", "malformed", "encoding"} {
		t.Run(failure, func(t *testing.T) {
			data := original
			if failure == "malformed" {
				data = []byte("<body><p>x</body>")
			}
			if failure == "encoding" {
				data = append([]byte("<body>"), 0xff)
			}
			m := testManifest(data, "text/html")
			switch failure {
			case "hash":
				m.Request.Artifact.Asset.SHA256 = digest([]byte("other"))
			case "length":
				m.Request.Artifact.Asset.ByteSize++
			case "format":
				m.Request.Artifact.MediaType = "application/msword"
			case "selector":
				m.HTMLSelector = "#missing"
			case "output":
				m.OutputBytes = 1
			case "items":
				m.Request.Budget.MaxItems = 1
			}
			req := childRequest(t, data, m)
			if failure == "input" {
				req.Limits.InputBytes = 1
			}
			if failure == "expanded" {
				req.Limits.ExpandedBytes = 1
			}
			if err := ChildHandler(req); err == nil {
				t.Fatal("negative produced success")
			}
			if _, err := os.Stat(filepath.Join(req.OutputDir, "manifest.json")); !os.IsNotExist(err) {
				t.Fatal("negative published a complete output manifest")
			}
		})
	}
}

func TestConverterClosesReceiptWhenCancelledOrManifestUnavailable(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		m := testManifest([]byte("abc"), "text/html")
		r := &fakeStaged{failMeta: !cancelled, manifest: []byte("[]")}
		c := &Converter{runner: &fakeSandbox{t: t, run: r}, manifest: m, retain: func(context.Context, model.AssetRef, io.Reader) error { return nil }}
		ctx, cancel := context.WithCancel(context.Background())
		if cancelled {
			cancel()
		}
		_, _, err := c.Convert(ctx, api.ConvertEnvelope{Key: m.RequestKey, Manifest: manifestDigest(t, m)})
		cancel()
		if err == nil || !r.closed {
			t.Fatal("failed request leaked receipt")
		}
	}
}
