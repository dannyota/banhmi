package conversion

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"

	"danny.vn/banhmi/internal/corpus/conversion/api"
	"danny.vn/banhmi/internal/corpus/model"
)

// ConversionManifest is the trusted request in the JSON input bundle. The trusted
// caller supplies the same value to NewConverter after resolving the ledger.
// Asset paths remain reference bindings, never host paths. The recipe bytes
// are retained verbatim and hash checked; this baseline uses only these explicit
// settings and never executes recipe-supplied instructions.
type ConversionManifest struct {
	// RequestKey is the screened convert ledger key.
	RequestKey string
	// Request binds the captured original, converter recipe and frozen budget.
	Request api.ConvertRequest
	// ObservationKey names the immutable source observation.
	ObservationKey string
	// ArtifactRole preserves the artifact's role within its document version.
	ArtifactRole string
	// Language is the recipe's native evidence language.
	Language string
	// HTMLSelector is body or one exact #id in this baseline selector version.
	HTMLSelector string
	// DetectorHash pins the detector recipe. ConvertedDocument.DetectorHash
	// instead names the emitted detection result, including the actual format.
	DetectorHash model.Digest
	// CandidateDigest binds the caller's immutable candidate manifest.
	CandidateDigest model.Digest
	// OutputBytes bounds aggregate staged content before sandbox manifest overhead.
	OutputBytes int64
}

func (m ConversionManifest) validate() error {
	if model.ValidateEnvelopeKey(m.RequestKey, "convert") != nil || m.Request.Artifact.Validate() != nil || m.Request.Recipe.Validate() != nil || m.Request.Budget.Validate() != nil || m.DetectorHash.Validate() != nil || m.CandidateDigest.Validate() != nil || m.ObservationKey == "" || m.ArtifactRole == "" || m.Language == "" || m.OutputBytes <= 0 || m.OutputBytes > math.MaxInt64/2 || m.Request.Budget.MaxInputBytes > math.MaxInt64/2 {
		return fmt.Errorf("manifest: INVALID_REQUEST")
	}
	if m.Request.Artifact.Asset.Path == "request.json" || m.Request.Recipe.Path == "request.json" || m.Request.Artifact.Asset.Path == m.Request.Recipe.Path {
		return fmt.Errorf("manifest: PATH_COLLISION")
	}
	return nil
}

func digest(data []byte) model.Digest {
	sum := sha256.Sum256(data)
	return model.Digest(hex.EncodeToString(sum[:]))
}

func artifactFinding(reason string, hash model.Digest) model.Finding {
	return model.Finding{ReasonCode: reason, Severity: model.SeverityBlocking, Scope: model.GapArtifact, ScopeKey: string(hash)}
}

type artifactSet struct {
	files map[string][]byte
	types map[string]string
	bytes int64
	limit int64
	items int
}

func (s *artifactSet) put(path string, data []byte) (model.AssetRef, error) {
	ref := model.AssetRef{Path: path, SHA256: digest(data), ByteSize: int64(len(data))}
	if err := ref.Validate(); err != nil {
		return model.AssetRef{}, fmt.Errorf("stage: INVALID_PATH")
	}
	if old, ok := s.files[path]; ok {
		if digest(old) != ref.SHA256 {
			return model.AssetRef{}, fmt.Errorf("stage: PATH_COLLISION")
		}
		return ref, nil
	}
	if len(s.files) >= s.items {
		return model.AssetRef{}, resourceFailure("items")
	}
	if ref.ByteSize > s.limit-s.bytes {
		return model.AssetRef{}, resourceFailure("output")
	}
	s.files[path] = append([]byte(nil), data...)
	s.bytes += ref.ByteSize
	return ref, nil
}

func (s *artifactSet) object(data []byte) (model.AssetRef, error) {
	return s.put("objects/"+string(digest(data)), data)
}

func (s *artifactSet) value(value any) (model.AssetRef, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return model.AssetRef{}, fmt.Errorf("stage: ENCODE_FAILED")
	}
	ref, err := s.object(data)
	if err == nil {
		if s.types == nil {
			s.types = map[string]string{}
		}
		s.types[ref.Path] = "json"
	}
	return ref, err
}

func (s *artifactSet) refs() []model.AssetRef {
	paths := make([]string, 0, len(s.files))
	for path := range s.files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	refs := make([]model.AssetRef, 0, len(paths))
	for _, path := range paths {
		data := s.files[path]
		refs = append(refs, model.AssetRef{Path: path, SHA256: digest(data), ByteSize: int64(len(data))})
	}
	return refs
}

type blockBuilder struct {
	manifest ConversionManifest
	assets   *artifactSet
	blocks   []model.Block
	maps     []model.OffsetMapSegment
	ledger   []model.EditLedgerEntry
	findings []model.Finding
	raw      []byte
	normal   []byte
}

func newBlockBuilder(m ConversionManifest) *blockBuilder {
	return &blockBuilder{manifest: m, assets: &artifactSet{files: map[string][]byte{}, limit: m.OutputBytes, items: m.Request.Budget.MaxItems}}
}

func (b *blockBuilder) finding(reason string, spans ...model.Span) error {
	if len(b.findings) >= b.manifest.Request.Budget.MaxItems {
		return resourceFailure("items")
	}
	f := artifactFinding(reason, b.manifest.Request.Artifact.Asset.SHA256)
	f.Spans = spans
	b.findings = append(b.findings, f)
	return nil
}

// add hashes canonical content as JSON [schema,kind,raw,normalized]. The local ID
// includes ordinal so equal paragraphs remain distinct. Neither incidental ZIP
// metadata nor source file names enter the content hash.
func (b *blockBuilder) add(kind model.BlockKind, raw string, spans []model.Span) error {
	if len(b.blocks) >= b.manifest.Request.Budget.MaxItems || len(spans) > b.manifest.Request.Budget.MaxItems {
		return resourceFailure("items")
	}
	if len(spans) == 0 {
		return fmt.Errorf("blocks: LOST_ANCHOR")
	}
	for _, span := range spans {
		if span.Validate() != nil || (span.Space != model.OriginalBytes && span.Space != model.DecodedMember) {
			return fmt.Errorf("blocks: LOST_ANCHOR")
		}
		if span.Space == model.OriginalBytes {
			if span.ArtifactHash != b.manifest.Request.Artifact.Asset.SHA256 || (span.Range != nil && span.Range.End > b.manifest.Request.Artifact.Asset.ByteSize) {
				return fmt.Errorf("blocks: LOST_ANCHOR")
			}
		} else {
			member, ok := b.assets.files["objects/"+string(span.ArtifactHash)]
			if !ok || span.Member == "" || (span.Range != nil && span.Range.End > int64(len(member))) {
				return fmt.Errorf("blocks: LOST_ANCHOR")
			}
		}
	}
	rawRef, err := b.assets.object([]byte(raw))
	if err != nil {
		return err
	}
	rawSpan := model.Span{ArtifactHash: rawRef.SHA256, Space: model.RawText, Object: "text", ReasonCode: "BYTE_MAPPING_UNAVAILABLE"}
	if len(raw) > 0 {
		rawSpan.Range = &model.ByteRange{Start: 0, End: int64(len(raw))}
		rawSpan.ReasonCode = ""
	}
	value, findings, err := (TextNormalizer{}).Normalize(model.TextInput{Text: raw, Sources: []model.Span{rawSpan}, Limits: model.RuleLimits{MaxItems: b.manifest.Request.Budget.MaxItems, MaxBytes: b.manifest.OutputBytes}})
	if err != nil {
		return err
	}
	for _, finding := range findings {
		if err := b.finding(finding.ReasonCode, spans...); err != nil {
			return err
		}
	}
	normalRef, err := b.assets.object([]byte(value.Text))
	if err != nil {
		return err
	}
	canonical, err := json.Marshal([]string{"block-v1", string(kind), raw, value.Text})
	if err != nil {
		return fmt.Errorf("blocks: ENCODE_FAILED")
	}
	id := fmt.Sprintf("block-%06d-%s", len(b.blocks), digest(canonical))
	for i := range spans {
		spans[i].BlockID = id
	}
	b.blocks = append(b.blocks, model.Block{ID: id, Ordinal: len(b.blocks), Kind: kind, RawText: raw, NormalizedText: normalRef, Language: b.manifest.Language, Authority: model.AuthorityUnknown, SourceSpans: spans})
	b.maps = append(b.maps, model.OffsetMapSegment{Sources: spans, Destinations: []model.Span{rawSpan}, Transform: TransformSourceDecode, Ordinal: len(b.maps)})
	for _, segment := range value.OffsetMap {
		segment.Ordinal = len(b.maps)
		b.maps = append(b.maps, segment)
	}
	for _, edit := range value.EditLedger {
		removed, err := b.assets.object([]byte(edit.Removed))
		if err != nil {
			return err
		}
		replacement, err := b.assets.object([]byte(edit.Replacement))
		if err != nil {
			return err
		}
		evidence, err := b.assets.value(edit)
		if err != nil {
			return err
		}
		b.ledger = append(b.ledger, model.EditLedgerEntry{Kind: edit.Kind, Ordinal: len(b.ledger), Rule: edit.Rule, Version: edit.Version, Inputs: edit.Inputs, Outputs: edit.Outputs, Removed: &removed, Replacement: &replacement, Evidence: evidence})
	}
	if int64(len(raw)) > b.manifest.OutputBytes-int64(len(b.raw)) || int64(len(value.Text)) > b.manifest.OutputBytes-int64(len(b.normal)) {
		return resourceFailure("output")
	}
	b.raw = append(b.raw, raw...)
	b.normal = append(b.normal, value.Text...)
	if len(b.maps) > b.manifest.Request.Budget.MaxItems || len(b.ledger) > b.manifest.Request.Budget.MaxItems {
		return resourceFailure("items")
	}
	return nil
}

func (b *blockBuilder) finish() (model.ConvertedDocument, error) {
	if err := b.verifyChains(); err != nil {
		return model.ConvertedDocument{}, err
	}
	d := model.ConvertedDocument{SchemaVersion: "blocks-v1", Original: b.manifest.Request.Artifact, ObservationKey: b.manifest.ObservationKey, ArtifactRole: b.manifest.ArtifactRole, RecipeHash: b.manifest.Request.Recipe.SHA256, DetectorHash: b.manifest.DetectorHash, CandidateDigest: b.manifest.CandidateDigest, Blocks: b.blocks}
	var err error
	if d.RawText, err = b.assets.object(b.raw); err != nil {
		return d, err
	}
	if d.NormalizedText, err = b.assets.object(b.normal); err != nil {
		return d, err
	}
	rawBlocks := make([]struct {
		ID      string
		Kind    model.BlockKind
		Text    string
		Sources []model.Span
	}, len(b.blocks))
	for i, block := range b.blocks {
		rawBlocks[i].ID, rawBlocks[i].Kind, rawBlocks[i].Text, rawBlocks[i].Sources = block.ID, block.Kind, block.RawText, block.SourceSpans
	}
	if d.RawBlocks, err = b.assets.value(rawBlocks); err != nil {
		return d, err
	}
	if d.NormalizedBlocks, err = b.assets.value(b.blocks); err != nil {
		return d, err
	}
	for _, edit := range b.ledger {
		if err := b.verifyEdit(edit); err != nil {
			return d, err
		}
	}
	for ordinal, segment := range b.maps {
		if segment.Validate() != nil || segment.Ordinal != ordinal || (segment.Transform != TransformSourceDecode && segment.Transform != TransformIdentity && segment.Transform != TransformNFCCRLF) {
			return d, fmt.Errorf("blocks: INVALID_MAP")
		}
		for _, side := range [][]model.Span{segment.Sources, segment.Destinations} {
			for _, span := range side {
				if !b.resolves(span) {
					return d, fmt.Errorf("blocks: LOST_ANCHOR")
				}
			}
		}
	}
	if d.OffsetMap, err = b.assets.value(b.maps); err != nil {
		return d, err
	}
	if d.EditLedger, err = b.assets.value(b.ledger); err != nil {
		return d, err
	}
	if d.Validate(b.manifest.Request.Budget.MaxItems) != nil {
		return d, fmt.Errorf("blocks: INVALID_DOCUMENT")
	}
	return d, nil
}

// verifyChains reconstructs the baseline's complete transform chain per block.
// Checking before staging also ensures damaged shared objects produce a finding
// instead of an earlier aggregate-object path collision.
func (b *blockBuilder) verifyChains() error {
	fail := func() error {
		if err := b.finding("IRREVERSIBLE_EDIT"); err != nil {
			return err
		}
		return fmt.Errorf("blocks: IRREVERSIBLE_EDIT")
	}
	for i, entry := range b.ledger {
		if entry.Ordinal != i {
			return fail()
		}
		if err := b.verifyEdit(entry); err != nil {
			return err
		}
	}
	expected := newBlockBuilder(b.manifest)
	// Decoded-member anchors resolve to the retained member bytes.
	for path, data := range b.assets.files {
		expected.assets.files[path] = data
	}
	for _, block := range b.blocks {
		if err := expected.add(block.Kind, block.RawText, append([]model.Span(nil), block.SourceSpans...)); err != nil {
			return fail()
		}
		if !matchesBlockText(b.assets, block.NormalizedText, expected.blocks[len(expected.blocks)-1].NormalizedText) {
			return fail()
		}
	}
	if !reflect.DeepEqual(expected.blocks, b.blocks) || !reflect.DeepEqual(expected.maps, b.maps) || !reflect.DeepEqual(expected.ledger, b.ledger) || !bytes.Equal(expected.raw, b.raw) || !bytes.Equal(expected.normal, b.normal) {
		return fail()
	}
	// Reverse entries in their complete block order against actual staged bytes.
	index := 0
	for _, block := range b.blocks {
		text := string(b.assets.files[block.NormalizedText.Path])
		var edits []model.DecodedEdit
		if text != block.RawText {
			if index >= len(b.ledger) {
				return fail()
			}
			var edit model.DecodedEdit
			if json.Unmarshal(b.assets.files[b.ledger[index].Evidence.Path], &edit) != nil {
				return fail()
			}
			edits = append(edits, edit)
			index++
		}
		restored, err := reverseText(model.MappedValue{Text: text, EditLedger: edits})
		if err != nil || restored != block.RawText {
			return fail()
		}
	}
	if index != len(b.ledger) {
		return fail()
	}
	return nil
}

func matchesBlockText(assets *artifactSet, actual, expected model.AssetRef) bool {
	data, ok := assets.files[actual.Path]
	return ok && actual == expected && digest(data) == actual.SHA256 && int64(len(data)) == actual.ByteSize
}

// verifyEdit checks retained bytes and evidence as well as the ledger's shape.
// A valid-looking reference cannot substitute a different removed string.
func (b *blockBuilder) verifyEdit(entry model.EditLedgerEntry) error {
	fail := func() error {
		if err := b.finding("IRREVERSIBLE_EDIT"); err != nil {
			return err
		}
		return fmt.Errorf("blocks: IRREVERSIBLE_EDIT")
	}
	if entry.Validate() != nil || entry.Removed == nil || entry.Replacement == nil || len(entry.Inputs) != 1 || len(entry.Outputs) != 1 {
		return fail()
	}
	for _, ref := range []model.AssetRef{*entry.Removed, *entry.Replacement, entry.Evidence} {
		data, ok := b.assets.files[ref.Path]
		if !ok || digest(data) != ref.SHA256 || int64(len(data)) != ref.ByteSize {
			return fail()
		}
	}
	var edit model.DecodedEdit
	if json.Unmarshal(b.assets.files[entry.Evidence.Path], &edit) != nil || edit.Kind != entry.Kind || edit.Rule != entry.Rule || edit.Version != entry.Version {
		return fail()
	}
	input, output := entry.Inputs[0], entry.Outputs[0]
	if input.ArtifactHash != entry.Removed.SHA256 || output.ArtifactHash != entry.Replacement.SHA256 || input.Range == nil || output.Range == nil || input.Range.Start != 0 || output.Range.Start != 0 || input.Range.End != entry.Removed.ByteSize || output.Range.End != entry.Replacement.ByteSize {
		return fail()
	}
	inputs, err := json.Marshal(entry.Inputs)
	if err != nil {
		return fail()
	}
	evidenceInputs, err := json.Marshal(edit.Inputs)
	if err != nil || !bytes.Equal(inputs, evidenceInputs) {
		return fail()
	}
	outputs, err := json.Marshal(entry.Outputs)
	if err != nil {
		return fail()
	}
	evidenceOutputs, err := json.Marshal(edit.Outputs)
	if err != nil || !bytes.Equal(outputs, evidenceOutputs) {
		return fail()
	}
	if edit.Removed != string(b.assets.files[entry.Removed.Path]) || edit.Replacement != string(b.assets.files[entry.Replacement.Path]) {
		return fail()
	}
	restored, err := reverseText(model.MappedValue{Text: edit.Replacement, EditLedger: []model.DecodedEdit{edit}})
	if err != nil || restored != edit.Removed {
		return fail()
	}
	return nil
}

func (b *blockBuilder) resolves(span model.Span) bool {
	if span.Validate() != nil {
		return false
	}
	if span.Space == model.OriginalBytes {
		return span.ArtifactHash == b.manifest.Request.Artifact.Asset.SHA256 && (span.Range == nil || span.Range.End <= b.manifest.Request.Artifact.Asset.ByteSize)
	}
	data, ok := b.assets.files["objects/"+string(span.ArtifactHash)]
	return ok && digest(data) == span.ArtifactHash && (span.Range == nil || span.Range.End <= int64(len(data)))
}
