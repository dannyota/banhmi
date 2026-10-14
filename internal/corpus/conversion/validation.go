//go:build linux

package conversion

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"danny.vn/banhmi/internal/corpus/conversion/sandbox"
	"danny.vn/banhmi/internal/corpus/model"
)

const verdictLimit = 1 << 20

type validationVerdict struct {
	Version string
	Request model.Digest
	Outputs []model.AssetRef
}

type qualityReport struct {
	SchemaVersion   string
	ArtifactHash    model.Digest
	CandidateDigest model.Digest
	RequestDigest   model.Digest
	RecipeHash      model.Digest
	DetectorHash    model.Digest
	DocumentHash    model.Digest
	Outcome         model.QualityOutcome
	Metrics         []model.QualityMetric
	Findings        []model.Finding
}

type detectionResult struct {
	Version  string
	Format   string
	Original model.Digest
	Recipe   model.Digest
}

func (c *Converter) validateOutputs(ctx context.Context, key model.Digest, refs []model.AssetRef, contents [][]byte) (resultErr error) {
	request, err := json.Marshal(c.manifest)
	if err != nil {
		return fmt.Errorf("validate: REQUEST_INVALID")
	}
	original, err := opaqueOriginal(c.inputDir, c.manifest)
	if err != nil {
		return err
	}
	bundle := inputBundle{Version: "bundle-v1", Mode: "validate", Request: request, Original: original}
	for i, ref := range refs {
		bundle.Outputs = append(bundle.Outputs, bundledOutput{Ref: ref, Data: base64.StdEncoding.EncodeToString(contents[i])})
	}
	data, err := json.Marshal(bundle)
	if err != nil {
		return fmt.Errorf("validate: BUNDLE_INVALID")
	}
	inputDir, err := os.MkdirTemp("", "conversion-validation-")
	if err != nil {
		return fmt.Errorf("validate: INPUT_UNAVAILABLE")
	}
	defer func() {
		if err := os.RemoveAll(inputDir); err != nil && resultErr == nil {
			resultErr = fmt.Errorf("validate: INPUT_CLEANUP_FAILED")
		}
	}()
	if err := os.WriteFile(filepath.Join(inputDir, "input.json"), data, 0600); err != nil {
		return fmt.Errorf("validate: INPUT_UNAVAILABLE")
	}
	receipt, err := c.runner.Run(ctx, sandbox.Attempt{Key: "conversion-" + strings.TrimPrefix(c.manifest.RequestKey, "convert-"), InputDir: inputDir, InputType: sandbox.TypeJSON})
	if receipt != nil {
		defer func() {
			if err := receipt.Close(); err != nil && resultErr == nil {
				resultErr = fmt.Errorf("validate: RECEIPT_CLOSE_FAILED")
			}
		}()
	}
	if err != nil {
		return screenedFailure(err)
	}
	if receipt == nil {
		return fmt.Errorf("validate: RECEIPT_MISSING")
	}
	for _, mechanism := range receipt.TerminationMechanisms() {
		if mechanism == string(sandbox.ContainerRetained) || mechanism == string(sandbox.ParentRetained) || mechanism == string(sandbox.AccountingUnconfirmed) {
			return fmt.Errorf("validate: TERMINATION_UNCONFIRMED")
		}
	}
	manifest, err := receipt.ManifestBytes()
	var verdictRefs []model.AssetRef
	if err != nil || len(manifest) > 4096 || decodeJSON(manifest, &verdictRefs) != nil || len(verdictRefs) != 1 {
		return fmt.Errorf("validate: VERDICT_MANIFEST_INVALID")
	}
	canonicalRefs, err := json.Marshal(verdictRefs)
	if err != nil || !bytes.Equal(canonicalRefs, manifest) {
		return fmt.Errorf("validate: VERDICT_MANIFEST_INVALID")
	}
	ref := verdictRefs[0]
	if ref.Validate() != nil || ref.Path != "verdict.json" || ref.ByteSize > verdictLimit {
		return fmt.Errorf("validate: VERDICT_INVALID")
	}
	file, err := receipt.Open(0)
	if err != nil {
		return fmt.Errorf("validate: VERDICT_UNAVAILABLE")
	}
	data, readErr := io.ReadAll(io.LimitReader(file, ref.ByteSize+1))
	closeErr := file.Close()
	var verdict validationVerdict
	if readErr != nil || closeErr != nil || !matchesAsset(data, ref) || decodeJSON(data, &verdict) != nil || verdict.Version != "validation-v1" || verdict.Request != key || !reflect.DeepEqual(verdict.Outputs, refs) {
		return fmt.Errorf("validate: VERDICT_BINDING_MISMATCH")
	}
	canonicalVerdict, err := json.Marshal(verdict)
	if err != nil || !bytes.Equal(canonicalVerdict, data) {
		return fmt.Errorf("validate: VERDICT_NONCANONICAL")
	}
	return nil
}

func validateBundle(req sandbox.ChildRequest, bundle inputBundle, m ConversionManifest) error {
	original, err := decodeBase64(bundle.Original, min(m.Request.Budget.MaxInputBytes, m.Request.Artifact.Asset.ByteSize))
	if err != nil {
		return err
	}
	if !matchesAsset(original, m.Request.Artifact.Asset) {
		return fmt.Errorf("validate: ORIGINAL_HASH_OR_LENGTH_MISMATCH")
	}
	if bundle.Recipe != "" || len(bundle.Outputs) == 0 || len(bundle.Outputs) > m.Request.Budget.MaxItems {
		return fmt.Errorf("validate: INPUT_INVALID")
	}
	assets := &artifactSet{files: map[string][]byte{}, limit: m.OutputBytes, items: m.Request.Budget.MaxItems}
	var refs []model.AssetRef
	remaining := m.OutputBytes
	for i, output := range bundle.Outputs {
		ref := output.Ref
		if ref.Validate() != nil || (i > 0 && refs[i-1].Path >= ref.Path) || ref.ByteSize > remaining {
			return fmt.Errorf("validate: OUTPUT_SET_INVALID")
		}
		data, err := decodeBase64(output.Data, ref.ByteSize)
		if err != nil || !matchesAsset(data, ref) {
			return fmt.Errorf("validate: OUTPUT_HASH_OR_LENGTH_MISMATCH")
		}
		if _, err := assets.put(ref.Path, data); err != nil {
			return err
		}
		remaining -= ref.ByteSize
		refs = append(refs, ref)
	}
	key := digest(bundle.Request)
	if err := validateSemantics(m, key, assets); err != nil {
		return err
	}
	var document model.ConvertedDocument
	if decodeJSON(assets.files[resultPath(key, "document.json")], &document) != nil {
		return fmt.Errorf("validate: DOCUMENT_INVALID")
	}
	if err := validateDerivation(original, document, m, req.Limits); err != nil {
		return err
	}
	data, err := json.Marshal(validationVerdict{Version: "validation-v1", Request: key, Outputs: refs})
	if err != nil || len(data) > verdictLimit {
		return resourceFailure("output")
	}
	verdict := &artifactSet{files: map[string][]byte{}, limit: req.Limits.OutputBytes, items: req.Limits.TempFiles}
	if _, err := verdict.put("verdict.json", data); err != nil {
		return err
	}
	verdict.types = map[string]string{"verdict.json": "json"}
	return writeArtifacts(req, verdict)
}

func validateSemantics(m ConversionManifest, key model.Digest, assets *artifactSet) error {
	fail := func() error { return fmt.Errorf("validate: SEMANTIC_BINDING_INVALID") }
	var document model.ConvertedDocument
	var quality qualityReport
	documentPath, qualityPath := resultPath(key, "document.json"), resultPath(key, "quality.json")
	if decodeJSON(assets.files[documentPath], &document) != nil || decodeJSON(assets.files[qualityPath], &quality) != nil || document.Validate(m.Request.Budget.MaxItems) != nil {
		return fail()
	}
	if document.SchemaVersion != "blocks-v1" || !reflect.DeepEqual(document.Original, m.Request.Artifact) || document.RecipeHash != m.Request.Recipe.SHA256 || document.CandidateDigest != m.CandidateDigest || document.ObservationKey != m.ObservationKey || document.ArtifactRole != m.ArtifactRole || len(document.Blocks) == 0 {
		return fail()
	}
	used := map[string]bool{documentPath: true, qualityPath: true}
	resolve := func(ref model.AssetRef) bool {
		data, ok := assets.files[ref.Path]
		if !ok || ref.Validate() != nil || !matchesAsset(data, ref) {
			return false
		}
		used[ref.Path] = true
		return true
	}
	for _, ref := range []model.AssetRef{document.RawBlocks, document.NormalizedBlocks, document.RawText, document.NormalizedText, document.OffsetMap, document.EditLedger} {
		if !resolve(ref) {
			return fail()
		}
	}
	detectorPath := "objects/" + string(document.DetectorHash)
	var detector detectionResult
	if decodeJSON(assets.files[detectorPath], &detector) != nil || digest(assets.files[detectorPath]) != document.DetectorHash || detector.Version != "detect-v1" || detector.Original != m.Request.Artifact.Asset.SHA256 || detector.Recipe != m.DetectorHash {
		return fail()
	}
	media, _, err := mime.ParseMediaType(m.Request.Artifact.MediaType)
	if err != nil || !((media == "text/html" && detector.Format == "html") || (media == "application/vnd.openxmlformats-officedocument.wordprocessingml.document" && detector.Format == "docx")) {
		return fail()
	}
	used[detectorPath] = true
	if quality.SchemaVersion != "conversion-quality-v1" || quality.ArtifactHash != m.Request.Artifact.Asset.SHA256 || quality.CandidateDigest != m.CandidateDigest || quality.RequestDigest != key || quality.RecipeHash != m.Request.Recipe.SHA256 || quality.DetectorHash != document.DetectorHash || quality.DocumentHash != digest(assets.files[documentPath]) || quality.Outcome != model.QualityQuarantined || len(quality.Metrics) != 2 || len(quality.Findings) > m.Request.Budget.MaxItems {
		return fail()
	}
	for i, metric := range quality.Metrics {
		kind := []model.MetricKind{model.MetricGeometry, model.MetricFidelity}[i]
		if metric.Validate() != nil || metric.Kind != kind || metric.State != model.MetricUnknown || metric.Passed || !metric.Mandatory || metric.Threshold != "qualification-required" || metric.ReasonCode != "BASELINE_UNQUALIFIED" {
			return fail()
		}
		for _, ref := range metric.Evidence {
			if !resolve(ref) {
				return fail()
			}
		}
	}
	b := newBlockBuilder(m)
	b.assets = assets
	b.blocks = document.Blocks
	b.raw, b.normal = assets.files[document.RawText.Path], assets.files[document.NormalizedText.Path]
	if decodeJSON(assets.files[document.OffsetMap.Path], &b.maps) != nil || decodeJSON(assets.files[document.EditLedger.Path], &b.ledger) != nil || len(b.maps) > m.Request.Budget.MaxItems || len(b.ledger) > m.Request.Budget.MaxItems {
		return fail()
	}
	checkSpan := func(span model.Span) bool {
		if !b.resolves(span) {
			return false
		}
		if span.Space != model.OriginalBytes {
			used["objects/"+string(span.ArtifactHash)] = true
		}
		return true
	}
	for _, metric := range quality.Metrics {
		for _, span := range metric.Exclusions {
			if !checkSpan(span) {
				return fail()
			}
		}
	}
	if !validSourceRoots(detector.Format, m.Request.Artifact.Asset, b.blocks) {
		return fail()
	}
	for _, block := range b.blocks {
		if block.Language != m.Language || !resolve(block.NormalizedText) || block.Content != nil || len(block.Findings) != 0 {
			return fail()
		}
		for _, span := range block.SourceSpans {
			if !checkSpan(span) {
				return fail()
			}
		}
	}
	for _, entry := range b.ledger {
		if entry.Removed == nil || entry.Replacement == nil || !resolve(*entry.Removed) || !resolve(*entry.Replacement) || !resolve(entry.Evidence) {
			return fail()
		}
	}
	for _, segment := range b.maps {
		for _, spans := range [][]model.Span{segment.Sources, segment.Destinations} {
			for _, span := range spans {
				if !checkSpan(span) {
					return fail()
				}
			}
		}
	}
	seenFindings := map[string]bool{}
	for _, finding := range quality.Findings {
		if finding.Validate() != nil || finding.Severity != model.SeverityBlocking || finding.Scope != model.GapArtifact || finding.ScopeKey != string(m.Request.Artifact.Asset.SHA256) {
			return fail()
		}
		seenFindings[finding.ReasonCode] = true
		for _, ref := range finding.Evidence {
			if !resolve(ref) {
				return fail()
			}
		}
		for _, span := range finding.Spans {
			if !checkSpan(span) {
				return fail()
			}
		}
	}
	if !seenFindings["GEOMETRY_UNKNOWN"] || !seenFindings["FIDELITY_UNQUALIFIED"] {
		return fail()
	}
	// finish rechecks complete chains, canonical blocks, aggregate bytes and every
	// ledger object before reproducing the referenced canonical structured objects.
	rebuilt, err := b.finish()
	if err != nil {
		return fail()
	}
	rebuilt.DetectorHash = document.DetectorHash
	want, err := json.Marshal(rebuilt)
	if err != nil || !bytes.Equal(want, assets.files[documentPath]) || len(used) != len(assets.files) {
		return fail()
	}
	return nil
}

// validSourceRoots checks the baseline's format-specific provenance before any
// reconstruction can copy untrusted source spans into canonical objects.
func validSourceRoots(format string, original model.AssetRef, blocks []model.Block) bool {
	var memberHash model.Digest
	for _, block := range blocks {
		if format == "html" {
			for _, span := range block.SourceSpans {
				if span.Space != model.OriginalBytes || span.ArtifactHash != original.SHA256 || span.Member != "" || span.Range == nil || span.Range.End > original.ByteSize || span.BlockID != block.ID {
					return false
				}
			}
			continue
		}
		if format != "docx" || len(block.SourceSpans) != 2 {
			return false
		}
		root, decoded := block.SourceSpans[0], block.SourceSpans[1]
		if root.Space != model.OriginalBytes || root.ArtifactHash != original.SHA256 || root.Member != docxBodyPath || root.Range != nil || root.ReasonCode != "BYTE_MAPPING_UNAVAILABLE" || root.Object != fmt.Sprintf("paragraph/%d", block.Ordinal) || root.TokenOrdinal == nil || root.BlockID != block.ID {
			return false
		}
		if decoded.Space != model.DecodedMember || decoded.Member != root.Member || decoded.Object != root.Object || decoded.TokenOrdinal == nil || *decoded.TokenOrdinal != *root.TokenOrdinal || decoded.BlockID != block.ID || decoded.Range == nil {
			return false
		}
		if memberHash != "" && decoded.ArtifactHash != memberHash {
			return false
		}
		memberHash = decoded.ArtifactHash
	}
	return true
}
