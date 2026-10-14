//go:build linux

package conversion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"danny.vn/banhmi/internal/corpus/conversion/api"
	"danny.vn/banhmi/internal/corpus/conversion/sandbox"
	"danny.vn/banhmi/internal/corpus/model"
)

type stagedRun interface {
	ManifestBytes() ([]byte, error)
	Open(int) (*os.File, error)
	Close() error
	TerminationMechanisms() []string
}

type sandboxRunner interface {
	Run(context.Context, sandbox.Attempt) (stagedRun, error)
}

type supervisorRunner struct{ supervisor *sandbox.Supervisor }

func (r supervisorRunner) Run(ctx context.Context, attempt sandbox.Attempt) (stagedRun, error) {
	receipt, err := r.supervisor.Run(ctx, attempt)
	return &receipt, err
}

var (
	_ stagedRun       = (*sandbox.Receipt)(nil)
	_ sandboxRunner   = supervisorRunner{}
	_ api.Converter   = (*Converter)(nil)
	_ sandbox.Handler = ChildHandler
)

// RetainOutput copies verified staged bytes into activity-owned durable staging.
// It must consume the reader before returning and must not accept evidence or
// register a writer receipt. All outputs are verified before the first call.
// Failed retention may leave inert staged objects, never success envelopes.
type RetainOutput func(context.Context, model.AssetRef, io.Reader) error

// Converter delegates all original-byte parsing to a fixed sandbox supervisor.
// It only hashes and copies inert staged bytes, and parses the supervisor's
// bounded reference manifest. It never opens a captured artifact or parses a
// converted block document on the trusted side.
type Converter struct {
	runner   sandboxRunner
	inputDir string
	manifest ConversionManifest
	retain   RetainOutput
}

// NewConverter binds one ledger-resolved manifest and a local input directory.
// The caller prepares input.json using the bundle-v1 schema. Original and recipe
// bytes use canonical standard base64. BundleInputBytes sizes input allowance.
// Only a real supervisor is accepted. Child composition belongs to M1.14.
func NewConverter(supervisor *sandbox.Supervisor, inputDir string, m ConversionManifest, retain RetainOutput) (*Converter, error) {
	if supervisor == nil || retain == nil || !filepath.IsAbs(inputDir) {
		return nil, fmt.Errorf("converter: SUPERVISOR_INPUT_AND_RETENTION_REQUIRED")
	}
	if err := m.validate(); err != nil {
		return nil, err
	}
	return &Converter{runner: supervisorRunner{supervisor: supervisor}, inputDir: inputDir, manifest: m, retain: retain}, nil
}

// Convert verifies the request binding, delegates parsing, verifies every staged
// digest and exact length, retains outputs, and closes the receipt on every path.
// Both returned envelopes are zero on any failure, including receipt cleanup.
func (c *Converter) Convert(ctx context.Context, input api.ConvertEnvelope) (conversion model.ConversionEnvelope, quality model.QualityEnvelope, resultErr error) {
	if c == nil || input.Validate() != nil || c.manifest.validate() != nil {
		return conversion, quality, fmt.Errorf("convert: INVALID_REQUEST")
	}
	ctx, cancel := context.WithTimeout(ctx, c.manifest.Request.Budget.MaxElapsed)
	defer cancel()
	canonical, err := json.Marshal(c.manifest)
	if err != nil || input.Key != c.manifest.RequestKey || digest(canonical) != input.Manifest {
		return conversion, quality, fmt.Errorf("convert: MANIFEST_MISMATCH")
	}
	if c.runner == nil || c.retain == nil {
		return conversion, quality, fmt.Errorf("convert: SANDBOX_REQUIRED")
	}
	receipt, err := c.runner.Run(ctx, sandbox.Attempt{Key: "conversion-" + strings.TrimPrefix(input.Key, "convert-"), InputDir: c.inputDir, InputType: sandbox.TypeJSON})
	if receipt != nil {
		defer func() {
			if closeErr := receipt.Close(); closeErr != nil && resultErr == nil {
				resultErr = fmt.Errorf("convert: RECEIPT_CLOSE_FAILED")
			}
			if resultErr != nil {
				conversion = model.ConversionEnvelope{}
				quality = model.QualityEnvelope{}
			}
		}()
	}
	if err != nil {
		return conversion, quality, screenedFailure(err)
	}
	if receipt == nil {
		return conversion, quality, fmt.Errorf("convert: SANDBOX_FAILED")
	}
	for _, mechanism := range receipt.TerminationMechanisms() {
		if mechanism == string(sandbox.ContainerRetained) || mechanism == string(sandbox.ParentRetained) || mechanism == string(sandbox.AccountingUnconfirmed) {
			return conversion, quality, fmt.Errorf("convert: TERMINATION_UNCONFIRMED")
		}
	}
	manifest, err := receipt.ManifestBytes()
	if err != nil {
		return conversion, quality, fmt.Errorf("convert: OUTPUT_MANIFEST_INVALID")
	}
	if int64(len(manifest)) > min(c.manifest.OutputBytes, 1<<20) {
		return conversion, quality, resourceFailure("output")
	}
	var refs []model.AssetRef
	if decodeJSON(manifest, &refs) != nil || len(refs) == 0 {
		return conversion, quality, fmt.Errorf("convert: OUTPUT_MANIFEST_INVALID")
	}
	if len(refs) > c.manifest.Request.Budget.MaxItems {
		return conversion, quality, resourceFailure("items")
	}
	reencoded, err := json.Marshal(refs)
	if err != nil || !bytes.Equal(reencoded, manifest) {
		return conversion, quality, fmt.Errorf("convert: OUTPUT_MANIFEST_NONCANONICAL")
	}
	remaining := c.manifest.OutputBytes
	documentSeen, qualitySeen := false, false
	var qualityHash model.Digest
	for i, ref := range refs {
		if ref.Validate() != nil || (i > 0 && refs[i-1].Path >= ref.Path) {
			return conversion, quality, fmt.Errorf("convert: OUTPUT_MANIFEST_INVALID")
		}
		if ref.ByteSize > remaining {
			return conversion, quality, resourceFailure("output")
		}
		remaining -= ref.ByteSize
		if ref.Path == resultPath(input.Manifest, "document.json") {
			documentSeen = true
		}
		if ref.Path == resultPath(input.Manifest, "quality.json") {
			qualitySeen = true
			qualityHash = ref.SHA256
		}
	}
	if !documentSeen || !qualitySeen {
		return conversion, quality, fmt.Errorf("convert: REQUEST_OUTPUT_BINDING_MISSING")
	}
	// Hash all exact bytes before retention. These buffers stay under the frozen
	// aggregate output ceiling; no second pathname read can replace checked data.
	contents := make([][]byte, len(refs))
	for i, ref := range refs {
		if ctx.Err() != nil {
			return conversion, quality, fmt.Errorf("convert: CANCELLED")
		}
		file, err := receipt.Open(i)
		if err != nil {
			return conversion, quality, fmt.Errorf("convert: OUTPUT_UNAVAILABLE")
		}
		data, readErr := io.ReadAll(io.LimitReader(file, ref.ByteSize+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil || int64(len(data)) != ref.ByteSize || digest(data) != ref.SHA256 {
			return conversion, quality, fmt.Errorf("convert: OUTPUT_HASH_OR_LENGTH_MISMATCH")
		}
		contents[i] = data
	}
	if err := c.validateOutputs(ctx, input.Manifest, refs, contents); err != nil {
		return conversion, quality, err
	}
	for i, ref := range refs {
		if ctx.Err() != nil || c.retain(ctx, ref, bytes.NewReader(contents[i])) != nil {
			return conversion, quality, fmt.Errorf("convert: RETENTION_FAILED")
		}
	}
	manifestRef := model.AssetRef{Path: "manifests/" + string(digest(manifest)), SHA256: digest(manifest), ByteSize: int64(len(manifest))}
	if ctx.Err() != nil || c.retain(ctx, manifestRef, bytes.NewReader(manifest)) != nil {
		return conversion, quality, fmt.Errorf("convert: RETENTION_FAILED")
	}
	suffix := strings.TrimPrefix(input.Key, "convert-")
	return model.ConversionEnvelope{Key: "conversion-" + suffix, Manifest: manifestRef.SHA256}, model.QualityEnvelope{Key: "quality-" + suffix, Manifest: qualityHash}, nil
}

func resultPath(request model.Digest, name string) string {
	return "results/" + string(request) + "/" + name
}

func decodeJSON(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("json: INVALID")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return fmt.Errorf("json: TRAILING_VALUE")
	}
	return nil
}

// ChildHandler handles conversion or semantic validation inside sandbox.ChildMain.
// Both modes admit a single JSON bundle through unchanged sandbox preflight.
// Direct calls are synthetic tests and provide no isolation.
func ChildHandler(req sandbox.ChildRequest) error {
	bundle, m, err := readBundle(req)
	if err != nil {
		return err
	}
	if bundle.Mode == "validate" {
		return validateBundle(req, bundle, m)
	}
	if bundle.Mode != "convert" || len(bundle.Outputs) != 0 {
		return fmt.Errorf("child: INVALID_MODE")
	}
	original, err := decodeBase64(bundle.Original, min(m.Request.Budget.MaxInputBytes, m.Request.Artifact.Asset.ByteSize))
	if err != nil {
		return err
	}
	recipe, err := decodeBase64(bundle.Recipe, 64<<10)
	if err != nil {
		return err
	}
	if !matchesAsset(original, m.Request.Artifact.Asset) || !matchesAsset(recipe, m.Request.Recipe) {
		return fmt.Errorf("child: INPUT_HASH_OR_LENGTH_MISMATCH")
	}
	canonical := bundle.Request
	b := newBlockBuilder(m)
	media, parameters, err := mime.ParseMediaType(m.Request.Artifact.MediaType)
	if err != nil {
		return fmt.Errorf("child: MEDIA_TYPE_INVALID")
	}
	if charset := parameters["charset"]; charset != "" && !strings.EqualFold(charset, "utf-8") && !strings.EqualFold(charset, "utf8") {
		return fmt.Errorf("child: ENCODING_AMBIGUOUS")
	}
	format := ""
	switch {
	case bytes.HasPrefix(original, []byte{'P', 'K', 3, 4}):
		format = "docx"
		if media != "application/vnd.openxmlformats-officedocument.wordprocessingml.document" {
			return fmt.Errorf("child: FORMAT_MISMATCH")
		}
		err = docxBlocksBounded(original, b, req.Limits.ExpandedBytes, req.Limits.Members, req.Limits.XMLDepth, req.Limits.ExpansionRatio)
	case bytes.HasPrefix(bytes.TrimSpace(original), []byte("<")):
		format = "html"
		if media != "text/html" {
			return fmt.Errorf("child: FORMAT_MISMATCH")
		}
		if !utf8.Valid(original) {
			return fmt.Errorf("child: ENCODING_AMBIGUOUS")
		}
		if int64(len(original)) > req.Limits.ExpandedBytes {
			return resourceFailure("expanded")
		}
		err = htmlBlocksBounded(original, b, req.Limits.XMLDepth)
	default:
		return fmt.Errorf("child: FORMAT_UNSUPPORTED")
	}
	if err != nil {
		return err
	}
	if err := b.finding("GEOMETRY_UNKNOWN"); err != nil {
		return err
	}
	if err := b.finding("FIDELITY_UNQUALIFIED"); err != nil {
		return err
	}
	document, err := b.finish()
	if err != nil {
		return err
	}
	detection, err := b.assets.value(detectionResult{"detect-v1", format, m.Request.Artifact.Asset.SHA256, m.DetectorHash})
	if err != nil {
		return err
	}
	document.DetectorHash = detection.SHA256
	// These observations are not the later QualityGate's policy report. They
	// always quarantine, and cannot grant acceptance or claim format qualification.
	documentBytes, err := json.Marshal(document)
	if err != nil {
		return fmt.Errorf("child: ENCODE_FAILED")
	}
	quality := qualityReport{SchemaVersion: "conversion-quality-v1", ArtifactHash: m.Request.Artifact.Asset.SHA256, CandidateDigest: m.CandidateDigest, RequestDigest: digest(canonical), RecipeHash: m.Request.Recipe.SHA256, DetectorHash: document.DetectorHash, DocumentHash: digest(documentBytes), Outcome: model.QualityQuarantined, Findings: b.findings}
	for _, kind := range []model.MetricKind{model.MetricGeometry, model.MetricFidelity} {
		quality.Metrics = append(quality.Metrics, model.QualityMetric{Kind: kind, State: model.MetricUnknown, Mandatory: true, Threshold: "qualification-required", ReasonCode: "BASELINE_UNQUALIFIED"})
	}
	key := digest(canonical)
	for _, output := range []struct {
		name  string
		value any
	}{{"document.json", document}, {"quality.json", quality}} {
		data, err := json.Marshal(output.value)
		if err != nil {
			return fmt.Errorf("child: ENCODE_FAILED")
		}
		if _, err := b.assets.put(resultPath(key, output.name), data); err != nil {
			return err
		}
	}
	return writeArtifacts(req, b.assets)
}

func matchesAsset(data []byte, ref model.AssetRef) bool {
	return int64(len(data)) == ref.ByteSize && digest(data) == ref.SHA256
}

func writeArtifacts(req sandbox.ChildRequest, assets *artifactSet) error {
	root, err := os.OpenRoot(req.OutputDir)
	if err != nil {
		return fmt.Errorf("child: OUTPUT_UNAVAILABLE")
	}
	defer root.Close()
	var manifest sandbox.Manifest
	for _, ref := range assets.refs() {
		if err := root.MkdirAll(filepath.ToSlash(filepath.Dir(ref.Path)), 0700); err != nil {
			return fmt.Errorf("child: OUTPUT_DIRECTORY_FAILED")
		}
		file, err := root.OpenFile(ref.Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return fmt.Errorf("child: OUTPUT_CREATE_FAILED")
		}
		_, writeErr := file.Write(assets.files[ref.Path])
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			return fmt.Errorf("child: OUTPUT_WRITE_FAILED")
		}
		kind := sandbox.TypeText
		if declared := assets.types[ref.Path]; declared != "" {
			kind = sandbox.OutputType(declared)
		}
		if strings.HasPrefix(ref.Path, "results/") {
			kind = sandbox.TypeJSON
		}
		// Structured objects retain their declared parser type for the fresh
		// sandbox validation child. Consumers resolve them only in isolation.
		manifest.Outputs = append(manifest.Outputs, sandbox.Output{Path: ref.Path, Type: kind, Size: ref.ByteSize})
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("child: OUTPUT_MANIFEST_ENCODE_FAILED")
	}
	if len(manifest.Outputs)+1 > req.Limits.TempFiles {
		return resourceFailure("files")
	}
	if int64(len(data)) > min(req.Limits.OutputBytes-assets.bytes, 1<<20) {
		return resourceFailure("output")
	}
	file, err := root.OpenFile("manifest.json", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("child: OUTPUT_MANIFEST_CREATE_FAILED")
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return fmt.Errorf("child: OUTPUT_MANIFEST_WRITE_FAILED")
	}
	return nil
}

func screenedFailure(err error) error {
	var failure *sandbox.Failure
	if errors.As(err, &failure) && failure != nil {
		// Child diagnostics are never forwarded. Preserve only the closed cause
		// and bound vocabulary accepted by the frozen sandbox protocol.
		copy := *failure
		copy.Reason = ""
		valid := false
		switch copy.Cause {
		case sandbox.Timeout, sandbox.StartupTimeout, sandbox.Cancelled, sandbox.ExitFailure, sandbox.UnsafeOutput:
			valid = copy.Bound == ""
		case sandbox.ResourceLimit:
			switch copy.Bound {
			case sandbox.BoundCPU, sandbox.BoundMemory, sandbox.BoundScratch, sandbox.BoundOutput, sandbox.BoundFiles, sandbox.BoundInput, sandbox.BoundExpanded, sandbox.BoundMembers, sandbox.BoundXML, sandbox.BoundPages, sandbox.BoundPixels, sandbox.BoundRatio:
				valid = true
			}
		}
		if valid {
			return fmt.Errorf("convert: %w", &copy)
		}
	}
	return fmt.Errorf("convert: SANDBOX_FAILED")
}
