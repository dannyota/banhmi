package document

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"danny.vn/banhmi/internal/corpus/blob"
	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/run"
	"danny.vn/banhmi/internal/corpus/store/document/documentdb"
)

// MaxConversionItems bounds a bulk map or quality report transport.
const MaxConversionItems = 1000

// MaxConversionPayloadBytes bounds encoded transport, including byte hex expansion.
const MaxConversionPayloadBytes = 4 * 1024 * 1024

// MaxConversionPageItems bounds every public page of retained evidence.
const MaxConversionPageItems int32 = 100

// Conversion shares the revision adapter's transaction and grant implementation.
// Construction performs no I/O. The supervisor supplies verified blob metadata.
type Conversion struct{ revisions *Revisions }

// NewConversion constructs a conversion adapter without opening a transaction.
func NewConversion(db TransactionStarter, writer string) (*Conversion, error) {
	if model.ValidateKey(writer) != nil {
		return nil, reject("conversion_writer_key")
	}
	r, err := NewRevisions(db, writer, ProvisionLimits{MaxItems: MaxConversionItems, MaxPayloadBytes: MaxConversionPayloadBytes,
		MaxCitationBytes: MaxConversionPayloadBytes, MaxAnchorBytes: MaxConversionPayloadBytes, MaxAnchors: MaxConversionItems})
	if err != nil {
		return nil, err
	}
	return &Conversion{revisions: r}, nil
}

// ConversionReceipt retains the original database ID and immutable SQL projection.
// Payload uses storage field names and includes original execution provenance.
// Ledger byte fields use hexadecimal, not JSON's base64 encoding.
type ConversionReceipt struct {
	// ID is the committed row's database-local identity.
	ID int64
	// Payload retains the stored fields, including original execution provenance.
	Payload json.RawMessage
}

// ConversionTarget pins one reserved revision key and immutable candidate.
// Reports and comparisons may precede creation of the revision row.
type ConversionTarget struct {
	// RevisionKey is the canonical reserved revision key.
	RevisionKey string
	// CandidateDigest pins the candidate manifest independently of the revision.
	CandidateDigest model.Digest
}

func (v ConversionTarget) validate(g Grant) error {
	if validateDocumentKey(v.RevisionKey, g.Execution.Country, true) != nil {
		return reject("conversion_revision_key")
	}
	if err := v.CandidateDigest.Validate(); err != nil {
		return reject("conversion_candidate_digest")
	}
	return nil
}

// ConversionArtifactInput binds an output to verified manifest membership.
// Members are the complete, supervisor-verified manifest projection, not bytes
// supplied by an untrusted converter. This adapter does not open blobs.
type ConversionArtifactInput struct {
	// Role selects one output role from the model vocabulary.
	Role model.ConversionArtifactRole
	// Ordinal is the nonnegative role-local manifest ordinal.
	Ordinal int32
	// Blob is catalog-issued output metadata, including its object reference.
	Blob blob.Metadata
	// SchemaVersion identifies the output's interchange schema.
	SchemaVersion string
	// OriginalHash pins the retained original bytes.
	OriginalHash model.Digest
	// ConversionRecipeDigest pins the converter recipe.
	ConversionRecipeDigest model.Digest
	// NormalizationRecipeDigest pins the normalizer recipe.
	NormalizationRecipeDigest model.Digest
	// DetectorDigest pins the format detector recipe and result identity.
	DetectorDigest model.Digest
	// Manifest identifies the retained complete output manifest.
	Manifest blob.Metadata
	// Members is the complete verified projection of that manifest.
	Members []ConversionArtifactMember
}

// ConversionArtifactMember is one verified output manifest entry.
type ConversionArtifactMember struct {
	// Role identifies the member's conversion role.
	Role model.ConversionArtifactRole
	// Ordinal distinguishes members within that role.
	Ordinal int32
	// Hash pins the member bytes.
	Hash model.Digest
	// ByteSize is the member's exact catalog byte size.
	ByteSize int64
}

type conversionPayload map[string]any

func conversionDigests(ds ...model.Digest) error {
	for _, d := range ds {
		if d.Validate() != nil {
			return reject("conversion_digest")
		}
	}
	return nil
}

func conversionMetadata(m blob.Metadata) error {
	if m.Validate() != nil {
		return reject("conversion_blob_metadata")
	}
	return validateText(m.MediaType, m.ObjectRef, m.CaptureSource, m.CaptureRef)
}

func conversionEncode(v any) (json.RawMessage, error) {
	remaining := MaxConversionPayloadBytes
	if !conversionSize(reflect.ValueOf(v), &remaining) {
		return nil, reject("conversion_payload_bound")
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, reject("conversion_transport")
	}
	if len(raw) > MaxConversionPayloadBytes {
		return nil, reject("conversion_payload_bound")
	}
	return raw, nil
}

func conversionSize(v reflect.Value, remaining *int) bool {
	if !v.IsValid() {
		return true
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if !v.IsNil() {
			return conversionSize(v.Elem(), remaining)
		}
	case reflect.String:
		if v.Len() > *remaining {
			return false
		}
		*remaining -= v.Len()
	case reflect.Slice, reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			if v.Len() > *remaining {
				return false
			}
			*remaining -= v.Len()
			return true
		}
		for i := 0; i < v.Len(); i++ {
			if !conversionSize(v.Index(i), remaining) {
				return false
			}
		}
	case reflect.Map:
		iterator := v.MapRange()
		for iterator.Next() {
			if !conversionSize(iterator.Key(), remaining) || !conversionSize(iterator.Value(), remaining) {
				return false
			}
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if !conversionSize(v.Field(i), remaining) {
				return false
			}
		}
	}
	return true
}

func (v ConversionArtifactInput) payload() (conversionPayload, error) {
	if !v.Role.Valid() || v.Ordinal < 0 || v.SchemaVersion == "" {
		return nil, reject("conversion_artifact_fields")
	}
	if err := validateText(v.SchemaVersion); err != nil {
		return nil, err
	}
	if len(v.SchemaVersion) > MaxConversionPayloadBytes {
		return nil, reject("conversion_payload_bound")
	}
	for _, m := range []blob.Metadata{v.Blob, v.Manifest} {
		if err := conversionMetadata(m); err != nil {
			return nil, err
		}
	}
	if err := conversionDigests(v.OriginalHash, v.ConversionRecipeDigest, v.NormalizationRecipeDigest, v.DetectorDigest); err != nil {
		return nil, err
	}
	if len(v.Members) == 0 || len(v.Members) > MaxConversionItems {
		return nil, reject("conversion_manifest_bound")
	}
	seen := make(map[struct {
		role    model.ConversionArtifactRole
		ordinal int32
	}]bool)
	found := false
	for _, m := range v.Members {
		key := struct {
			role    model.ConversionArtifactRole
			ordinal int32
		}{m.Role, m.Ordinal}
		if !m.Role.Valid() || m.Ordinal < 0 || m.ByteSize < 0 || m.Hash.Validate() != nil || seen[key] {
			return nil, reject("conversion_manifest_member")
		}
		seen[key] = true
		if m.Role == v.Role && m.Ordinal == v.Ordinal {
			found = string(m.Hash) == v.Blob.SHA256 && m.ByteSize == v.Blob.ByteSize
		}
	}
	if !found {
		return nil, reject("conversion_manifest_membership")
	}
	return conversionPayload{"role": v.Role, "ordinal": v.Ordinal, "blob_hash": v.Blob.SHA256, "byte_size": v.Blob.ByteSize,
		"schema_version": v.SchemaVersion, "original_hash": v.OriginalHash, "conversion_recipe_digest": v.ConversionRecipeDigest,
		"normalization_recipe_digest": v.NormalizationRecipeDigest, "detector_digest": v.DetectorDigest,
		"manifest_hash": v.Manifest.SHA256, "manifest_byte_size": v.Manifest.ByteSize}, nil
}

// ConversionSegment retains one source/destination edge of a many-to-many map.
// Empty half-open ranges are allowed for insertion and deletion edges.
type ConversionSegment struct {
	// MapHash pins the complete retained map artifact.
	MapHash model.Digest
	// Source names the source artifact, block and coordinates.
	Source model.Span
	// Destination names the destination artifact, block and coordinates.
	Destination model.Span
	// TransformOrder is the nonnegative transformation sequence position.
	TransformOrder int32
	// Ordinal distinguishes edges within the transformation.
	Ordinal int32
	// Transform identifies the retained transformation rule.
	Transform string
}

func conversionSpan(s model.Span, a model.AssetRef) (conversionPayload, error) {
	if s.ArtifactHash != a.SHA256 || !s.Space.Valid() || model.ValidateKey(s.BlockID) != nil || s.TokenOrdinal != nil {
		return nil, reject("conversion_span_identity")
	}
	if s.Page != nil && (*s.Page <= 0 || int64(*s.Page) > 2147483647) {
		return nil, reject("conversion_span_page")
	}
	if err := validateText(s.Object, s.Member, s.ReasonCode); err != nil {
		return nil, err
	}
	space := map[model.CoordinateSpace]model.ProvenanceSpace{model.OriginalBytes: model.ProvenanceOriginalBytes,
		model.DecodedMember: model.ProvenanceMemberBytes, model.RawText: model.ProvenanceRawText, model.NormalizedText: model.ProvenanceNormalizedText}[s.Space]
	var start, end *int64
	var reason, object, member *string
	if s.Object != "" {
		object = &s.Object
	}
	if s.Member != "" {
		member = &s.Member
	}
	if s.Range != nil {
		if s.Range.Start < 0 || s.Range.End < s.Range.Start || s.Range.End > a.ByteSize || s.ReasonCode != "" {
			return nil, reject("conversion_span_range")
		}
		start, end = &s.Range.Start, &s.Range.End
	} else {
		if s.ReasonCode != "BYTE_MAPPING_UNAVAILABLE" || (s.Page == nil && object == nil && member == nil) {
			return nil, reject("conversion_span_anchor")
		}
		reason = &s.ReasonCode
	}
	return conversionPayload{"block_id": s.BlockID, "space": space, "start": start, "end": end, "page": s.Page, "object": object, "member": member, "reason": reason}, nil
}

func conversionSegments(source, destination model.AssetRef, items []ConversionSegment) ([]conversionPayload, error) {
	if source.Validate() != nil || destination.Validate() != nil {
		return nil, reject("conversion_pair")
	}
	if len(items) == 0 || len(items) > MaxConversionItems {
		return nil, reject("conversion_segment_bound")
	}
	result := make([]conversionPayload, 0, len(items))
	seen := make(map[[2]int32]bool)
	for _, v := range items {
		key := [2]int32{v.TransformOrder, v.Ordinal}
		if v.MapHash.Validate() != nil || v.TransformOrder < 0 || v.Ordinal < 0 || v.Transform == "" || seen[key] {
			return nil, reject("conversion_segment_order")
		}
		seen[key] = true
		if err := validateText(v.Transform); err != nil {
			return nil, err
		}
		p := conversionPayload{"map_hash": v.MapHash, "transform_order": v.TransformOrder, "segment_ordinal": v.Ordinal, "transform": v.Transform}
		for _, side := range []struct {
			prefix string
			span   model.Span
			asset  model.AssetRef
		}{{"source_", v.Source, source}, {"destination_", v.Destination, destination}} {
			fields, err := conversionSpan(side.span, side.asset)
			if err != nil {
				return nil, err
			}
			for key, value := range fields {
				p[side.prefix+key] = value
			}
		}
		result = append(result, p)
	}
	return result, nil
}

// ConversionEditInput retains the exact bytes required to reverse one operation.
// The caller verifies these bytes against the hashed input/output artifacts.
type ConversionEditInput struct {
	// LedgerHash pins the complete retained edit ledger.
	LedgerHash model.Digest
	// Ordinal is the nonnegative operation ordinal within that ledger.
	Ordinal int32
	// Operation selects the model's reversible edit kind.
	Operation model.EditKind
	// Rule identifies the transformation rule.
	Rule string
	// RuleVersion pins the rule version.
	RuleVersion string
	// Input identifies the exact input bytes and declared byte bound.
	Input model.AssetRef
	// Output identifies the exact output bytes and declared byte bound.
	Output model.AssetRef
	// InputRange is the half-open range replaced or removed by the operation.
	InputRange model.ByteRange
	// OutputRange is the half-open range inserted or retained by the operation.
	OutputRange model.ByteRange
	// RemovedBytes preserves the exact input range, including arbitrary bytes.
	RemovedBytes []byte
	// ReplacementBytes preserves the exact output range, including arbitrary bytes.
	ReplacementBytes []byte
	// EncodingEvidence is an immutable bounded JSON object.
	EncodingEvidence json.RawMessage
	// FontEvidence is an immutable bounded JSON object.
	FontEvidence json.RawMessage
	// SourceAnchors is an immutable bounded JSON array.
	SourceAnchors json.RawMessage
}

func conversionJSONShape(raw json.RawMessage, array bool) error {
	if len(raw) > MaxConversionPayloadBytes || !utf8.Valid(raw) || !json.Valid(raw) {
		return reject("conversion_json")
	}
	if err := validateAnchorEscapes(raw); err != nil {
		return err
	}
	if err := validateAnchorTokens(raw); err != nil {
		return err
	}
	v, err := jsonValue(raw)
	if err != nil {
		return err
	}
	if array {
		if a, ok := v.([]any); !ok || len(a) > MaxConversionItems {
			return reject("conversion_json_array")
		}
	} else if _, ok := v.(map[string]any); !ok {
		return reject("conversion_json_object")
	}
	return nil
}

func (v ConversionEditInput) payload() (conversionPayload, error) {
	if !v.Operation.Valid() || v.Ordinal < 0 || v.Rule == "" || v.RuleVersion == "" || v.LedgerHash.Validate() != nil {
		return nil, reject("conversion_edit_fields")
	}
	if err := validateText(v.Rule, v.RuleVersion); err != nil {
		return nil, err
	}
	if v.Input.Validate() != nil || v.Output.Validate() != nil {
		return nil, reject("conversion_edit_artifacts")
	}
	for _, side := range []struct {
		r model.ByteRange
		a model.AssetRef
		b []byte
	}{{v.InputRange, v.Input, v.RemovedBytes}, {v.OutputRange, v.Output, v.ReplacementBytes}} {
		if side.r.Start < 0 || side.r.End < side.r.Start || side.r.End > side.a.ByteSize || side.r.End-side.r.Start != int64(len(side.b)) {
			return nil, reject("conversion_edit_range_bytes")
		}
	}
	if (v.Operation == model.EditInsert && len(v.RemovedBytes) != 0) || (v.Operation == model.EditDelete && len(v.ReplacementBytes) != 0) {
		return nil, reject("conversion_edit_sides")
	}
	if len(v.RemovedBytes) > MaxConversionPayloadBytes/4 || len(v.ReplacementBytes) > MaxConversionPayloadBytes/4 {
		return nil, reject("conversion_edit_byte_bound")
	}
	for _, raw := range []json.RawMessage{v.EncodingEvidence, v.FontEvidence} {
		if err := conversionJSONShape(raw, false); err != nil {
			return nil, err
		}
	}
	if err := conversionJSONShape(v.SourceAnchors, true); err != nil {
		return nil, err
	}
	return conversionPayload{"ledger_hash": v.LedgerHash, "operation_ordinal": v.Ordinal, "operation": v.Operation, "rule": v.Rule, "rule_version": v.RuleVersion,
		"input_hash": v.Input.SHA256, "output_hash": v.Output.SHA256, "input_start": v.InputRange.Start, "input_end": v.InputRange.End,
		"output_start": v.OutputRange.Start, "output_end": v.OutputRange.End, "removed_bytes": hex.EncodeToString(v.RemovedBytes),
		"replacement_bytes": hex.EncodeToString(v.ReplacementBytes), "encoding_evidence": v.EncodingEvidence, "font_evidence": v.FontEvidence, "source_anchors": v.SourceAnchors}, nil
}

// ConversionQualityRecord represents exactly one metric, finding or scoped gap.
// Ordinals must cover [0, RecordCount). Nullable fields select the record kind.
type ConversionQualityRecord struct {
	// Ordinal locates the child in the complete report record set.
	Ordinal int32 `json:"ordinal"`
	// Kind selects metric, finding or gap fields.
	Kind model.QualityRecordKind `json:"record_kind"`
	// Code is the retained machine reason code.
	Code string `json:"code"`
	// Severity is present for findings and gaps only.
	Severity *model.Severity `json:"severity"`
	// Scope is present for findings and gaps only.
	Scope *model.GapKind `json:"scope_kind"`
	// ScopeKey identifies the exact finding or gap scope.
	ScopeKey *string `json:"scope_key"`
	// Reason explains a finding, gap, unknown or inapplicable metric.
	Reason *string `json:"reason"`
	// MetricKind is present for metrics only.
	MetricKind *model.MetricKind `json:"metric_kind"`
	// MetricState distinguishes measured, unknown and inapplicable metrics.
	MetricState *model.MetricState `json:"metric_state"`
	// Numerator retains the metric's nonnegative measured count.
	Numerator *int64 `json:"numerator"`
	// Denominator retains the count of eligible metric inputs.
	Denominator *int64 `json:"denominator"`
	// Threshold retains the resolved policy threshold without interpreting it.
	Threshold *string `json:"threshold"`
	// Mandatory retains the policy's mandatory status.
	Mandatory *bool `json:"mandatory"`
	// Passed retains the measured result without deciding acceptance.
	Passed *bool `json:"passed"`
	// Evidence is a bounded JSON array of model.AssetRef values.
	// Every reference requires an explicit ByteSize and a cataloged hash.
	Evidence json.RawMessage `json:"evidence"`
	// SourceSpans is a bounded JSON array of model.Span values.
	// Every span names an artifact in this record's Evidence array.
	SourceSpans json.RawMessage `json:"source_spans"`
}

// conversionQualityAsset requires an explicit byte size, including zero.
// Its JSON fields match model.AssetRef's portable artifact reference.
type conversionQualityAsset struct {
	Path     string
	SHA256   model.Digest
	ByteSize *int64
}

// Report evidence uses model.AssetRef JSON values. Source spans use model.Span
// JSON values and must name an evidence artifact with its exact declared size.
// The original JSON is retained so replay compares every immutable field.
func conversionQualityReferences(r ConversionQualityRecord) ([]conversionBlobRef, error) {
	if err := conversionJSONShape(r.Evidence, true); err != nil {
		return nil, err
	}
	if err := conversionJSONShape(r.SourceSpans, true); err != nil {
		return nil, err
	}
	var assets []conversionQualityAsset
	var spans []model.Span
	decode := func(raw json.RawMessage, dst any) error {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if err := d.Decode(dst); err != nil {
			return reject("conversion_quality_reference_shape")
		}
		return nil
	}
	if err := decode(r.Evidence, &assets); err != nil {
		return nil, err
	}
	if err := decode(r.SourceSpans, &spans); err != nil {
		return nil, err
	}
	byHash := make(map[model.Digest]model.AssetRef, len(assets))
	refs := make([]conversionBlobRef, 0, len(assets))
	for _, reference := range assets {
		if reference.ByteSize == nil {
			return nil, reject("conversion_quality_asset_size")
		}
		a := model.AssetRef{Path: reference.Path, SHA256: reference.SHA256, ByteSize: *reference.ByteSize}
		if a.Validate() != nil {
			return nil, reject("conversion_quality_asset")
		}
		if previous, ok := byHash[a.SHA256]; ok && previous.ByteSize != a.ByteSize {
			return nil, reject("conversion_quality_asset_size")
		}
		byHash[a.SHA256] = a
		refs = append(refs, assetRef(a))
	}
	for _, span := range spans {
		a, ok := byHash[span.ArtifactHash]
		if !ok {
			return nil, reject("conversion_quality_span_artifact")
		}
		if _, err := conversionSpan(span, a); err != nil {
			return nil, err
		}
	}
	return refs, nil
}

// ConversionQualityInput persists a report without deciding acceptance or policy.
// RecordCount explicitly binds the complete child set to the hashed report.
type ConversionQualityInput struct {
	// Report identifies the immutable report blob and its catalog object.
	Report blob.Metadata
	// ArtifactHash is absent for a document roll-up report.
	ArtifactHash *model.Digest
	// PolicyDigest pins the policy that produced the report.
	PolicyDigest model.Digest
	// ThresholdVersion pins the threshold profile version.
	ThresholdVersion string
	// Outcome retains the report outcome without accepting the candidate.
	Outcome model.QualityOutcome
	// RecordCount declares the complete number of metric, finding and gap rows.
	RecordCount int32
	// Records must contain every declared ordinal exactly once.
	Records []ConversionQualityRecord
}

func (v ConversionQualityInput) payload() (conversionPayload, error) {
	if err := conversionMetadata(v.Report); err != nil {
		return nil, err
	}
	if !v.Outcome.Valid() || v.PolicyDigest.Validate() != nil || v.ThresholdVersion == "" || v.RecordCount < 0 || int(v.RecordCount) != len(v.Records) || len(v.Records) > MaxConversionItems {
		return nil, reject("conversion_report_fields_or_count")
	}
	if v.ArtifactHash != nil && v.ArtifactHash.Validate() != nil {
		return nil, reject("conversion_report_artifact")
	}
	if err := validateText(v.ThresholdVersion); err != nil {
		return nil, err
	}
	records := append([]ConversionQualityRecord{}, v.Records...)
	seen := make(map[int32]bool)
	for _, r := range records {
		if !r.Kind.Valid() || r.Code == "" || r.Ordinal < 0 || r.Ordinal >= v.RecordCount || seen[r.Ordinal] {
			return nil, reject("conversion_quality_record")
		}
		seen[r.Ordinal] = true
		texts := []string{r.Code}
		for _, s := range []*string{r.ScopeKey, r.Reason, r.Threshold} {
			if s != nil {
				if *s == "" {
					return nil, reject("conversion_quality_empty_text")
				}
				texts = append(texts, *s)
			}
		}
		if err := validateText(texts...); err != nil {
			return nil, err
		}
		if r.Kind == model.QualityRecordMetric {
			if r.Severity != nil || r.Scope != nil || r.ScopeKey != nil || r.MetricKind == nil || !r.MetricKind.Valid() || r.MetricState == nil || !r.MetricState.Valid() || r.Numerator == nil || r.Denominator == nil || r.Threshold == nil || r.Mandatory == nil || r.Passed == nil {
				return nil, reject("conversion_metric_fields")
			}
			if *r.Numerator < 0 || *r.Denominator < 0 || (*r.MetricState == model.MetricMeasured && *r.Denominator == 0) || (*r.MetricState != model.MetricMeasured && (r.Reason == nil || *r.Passed || *r.Numerator != 0 || *r.Denominator != 0)) {
				return nil, reject("conversion_metric_counts")
			}
		} else if r.Severity == nil || !r.Severity.Valid() || r.Scope == nil || !r.Scope.Valid() || r.ScopeKey == nil || r.Reason == nil || r.MetricKind != nil || r.MetricState != nil || r.Numerator != nil || r.Denominator != nil || r.Threshold != nil || r.Mandatory != nil || r.Passed != nil {
			return nil, reject("conversion_finding_fields")
		}
		if _, err := conversionQualityReferences(r); err != nil {
			return nil, err
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Ordinal < records[j].Ordinal })
	return conversionPayload{"report_hash": v.Report.SHA256, "report_byte_size": v.Report.ByteSize, "artifact_hash": v.ArtifactHash,
		"policy_digest": v.PolicyDigest, "threshold_version": v.ThresholdVersion, "outcome": v.Outcome, "record_count": v.RecordCount, "records": records}, nil
}

// ConversionComparisonInput pins two reports and a captured acceptance baseline.
// Staleness is derived by SQL; no method changes an existing comparison.
type ConversionComparisonInput struct {
	// PriorReportID selects the retained baseline report.
	PriorReportID int64
	// CandidateReportID selects the retained candidate report.
	CandidateReportID int64
	// PriorRevisionKey binds the baseline report to the same document identity.
	PriorRevisionKey string
	// PriorCandidateDigest pins the baseline candidate.
	PriorCandidateDigest model.Digest
	// RecipeDigest pins the comparison recipe.
	RecipeDigest model.Digest
	// Result identifies the immutable comparison result blob.
	Result blob.Metadata
	// AcceptanceWatermark pins the verified acceptance baseline; zero means none.
	AcceptanceWatermark int64
}

func (v ConversionComparisonInput) payload(g Grant, target ConversionTarget) (conversionPayload, error) {
	if v.PriorReportID <= 0 || v.CandidateReportID <= 0 || v.AcceptanceWatermark < 0 || v.PriorCandidateDigest == target.CandidateDigest {
		return nil, reject("conversion_comparison_fields")
	}
	if validateDocumentKey(v.PriorRevisionKey, g.Execution.Country, true) != nil {
		return nil, reject("conversion_prior_revision_key")
	}
	if strings.Split(v.PriorRevisionKey, ":rev:")[0] != strings.Split(target.RevisionKey, ":rev:")[0] {
		return nil, reject("conversion_comparison_identity")
	}
	if err := conversionDigests(v.PriorCandidateDigest, v.RecipeDigest); err != nil {
		return nil, err
	}
	if err := conversionMetadata(v.Result); err != nil {
		return nil, err
	}
	return conversionPayload{"prior_report_id": v.PriorReportID, "candidate_report_id": v.CandidateReportID, "prior_revision_key": v.PriorRevisionKey,
		"prior_candidate_digest": v.PriorCandidateDigest, "comparison_recipe_digest": v.RecipeDigest, "result_hash": v.Result.SHA256,
		"result_byte_size": v.Result.ByteSize, "acceptance_watermark": v.AcceptanceWatermark}, nil
}

// Compare only immutable payload fields. Stored projections also contain IDs
// and original execution provenance, which are returned rather than replaced.
func conversionSubset(stored, input any) bool {
	if m, ok := input.(map[string]any); ok {
		s, ok := stored.(map[string]any)
		if !ok {
			return false
		}
		for key, value := range m {
			if key == "records" {
				if !conversionSubset(s[key], value) {
					return false
				}
			} else if !reflect.DeepEqual(s[key], value) {
				return false
			}
		}
		return true
	}
	if a, ok := input.([]any); ok {
		s, ok := stored.([]any)
		if !ok || len(s) != len(a) {
			return false
		}
		for i := range a {
			if !conversionSubset(s[i], a[i]) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(stored, input)
}

func conversionMatch(row ConversionReceipt, payload json.RawMessage, version string) bool {
	s, err := jsonValue(row.Payload)
	if err != nil {
		return false
	}
	i, err := jsonValue(payload)
	if err != nil {
		return false
	}
	m, ok := s.(map[string]any)
	return ok && m["document_version_key"] == version && conversionSubset(s, i)
}

func conversionParent(ctx context.Context, q documentdb.Querier, g Grant, target ConversionTarget, optional bool) (int64, error) {
	row, err := q.ReadDocumentRevision(ctx, target.RevisionKey)
	if errors.Is(err, pgx.ErrNoRows) && optional {
		return 0, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, reject("conversion_parent_missing")
	}
	if err != nil {
		return 0, databaseError(ctx, "read conversion parent", err)
	}
	if row.DocumentVersionKey != g.Execution.Scope.Subject.LeaseKey() {
		return 0, fmt.Errorf("conversion parent version: %w", run.ErrStaleGrant)
	}
	return row.ID, nil
}

func conversionStored(ctx context.Context, q documentdb.Querier, kind string, target ConversionTarget, key json.RawMessage) (ConversionReceipt, error) {
	row, err := q.ReadConversionReceipt(ctx, documentdb.ReadConversionReceiptParams{RecordKind: kind, RevisionKey: target.RevisionKey, CandidateDigest: string(target.CandidateDigest), ReceiptKey: key, RecordLimit: MaxConversionItems + 1})
	return ConversionReceipt{ID: row.ID, Payload: row.Payload}, err
}

// All mutation kinds use the same transaction wrapper. An empty mutation result
// is rolled back immediately. Only a new transaction may read or retry it.
func (c *Conversion) mutate(ctx context.Context, g Grant, target ConversionTarget, kind string, p conversionPayload, refs []conversionBlobRef, insert func(context.Context, documentdb.Querier, int64, json.RawMessage) error) (ConversionReceipt, error) {
	var zero ConversionReceipt
	if err := target.validate(g); err != nil {
		return zero, err
	}
	p["revision_key"], p["candidate_digest"], p["document_version_key"] = target.RevisionKey, target.CandidateDigest, g.Execution.Scope.Subject.LeaseKey()
	payload, err := conversionEncode(p)
	if err != nil {
		return zero, err
	}
	refPayload, err := conversionEncode(refs)
	if err != nil {
		return zero, err
	}
	for attempt := 0; attempt < 3; attempt++ {
		result, err := revisionTransaction(ctx, c.revisions, g, func(_ pgx.Tx, q documentdb.Querier) (ConversionReceipt, error) {
			parent, err := conversionParent(ctx, q, g, target, kind == "report" || kind == "comparison")
			if err != nil {
				return zero, err
			}
			stored, err := conversionStored(ctx, q, kind, target, payload)
			if err == nil {
				if !conversionMatch(stored, payload, g.Execution.Scope.Subject.LeaseKey()) {
					return zero, conflict("conversion immutable payload")
				}
				if err := conversionBlobs(ctx, q, refs, refPayload); err != nil {
					return zero, err
				}
				// Exact receipts intentionally include stale comparisons. Read
				// eligibility separately so replay never inserts or rewrites a
				// captured baseline, including after an invisible-winner retry.
				if kind == "comparison" {
					eligible, err := q.ReadConversionComparisonEligibility(ctx, stored.ID)
					if err != nil {
						return zero, databaseError(ctx, "read comparison eligibility", err)
					}
					if !eligible {
						return zero, reject("conversion_comparison_stale")
					}
				}
				return stored, nil
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return zero, databaseError(ctx, "read conversion receipt", err)
			}
			if err := conversionBlobs(ctx, q, refs, refPayload); err != nil {
				return zero, err
			}
			if attempt == 2 {
				return zero, reject("conversion_provenance_or_baseline_ineligible")
			}
			if err := insert(ctx, q, parent, payload); errors.Is(err, pgx.ErrNoRows) {
				return zero, errBulkEmpty
			} else if err != nil {
				return zero, databaseError(ctx, "write conversion provenance", err)
			}
			stored, err = conversionStored(ctx, q, kind, target, payload)
			if errors.Is(err, pgx.ErrNoRows) {
				return zero, errBulkEmpty
			}
			if err != nil {
				return zero, databaseError(ctx, "resolve conversion receipt", err)
			}
			if !conversionMatch(stored, payload, g.Execution.Scope.Subject.LeaseKey()) {
				return zero, conflict("conversion immutable payload")
			}
			return stored, nil
		})
		if !errors.Is(err, errBulkEmpty) {
			return result, err
		}
		var cleanup revisionRollbackFailure
		if errors.As(err, &cleanup) {
			return zero, errors.Join(ctx.Err(), cleanup)
		}
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
	}
	return zero, reject("conversion_provenance_ineligible")
}

// RegisterConversionArtifact validates metadata and manifest membership before
// locking. Identical replay returns the original row; changed content conflicts.
func (c *Conversion) RegisterConversionArtifact(ctx context.Context, g Grant, target ConversionTarget, v ConversionArtifactInput) (ConversionReceipt, error) {
	p, err := v.payload()
	if err != nil {
		return ConversionReceipt{}, err
	}
	refs := []conversionBlobRef{metadataRef(v.Blob), metadataRef(v.Manifest), {Hash: v.OriginalHash}}
	for _, member := range v.Members {
		size := member.ByteSize
		refs = append(refs, conversionBlobRef{Hash: member.Hash, ByteSize: &size})
	}
	return c.mutate(ctx, g, target, "artifact", p, refs, func(ctx context.Context, q documentdb.Querier, parent int64, payload json.RawMessage) error {
		e := g.Execution
		_, err := q.RegisterConversionArtifact(ctx, documentdb.RegisterConversionArtifactParams{AdmissionEpoch: e.AdmissionEpoch, AdmittedWriter: c.revisions.writer,
			ResourceKind: "document_version", ResourceKey: e.Scope.Subject.LeaseKey(), Column5: e.Owner, Column6: e.AttemptFence, ID: g.ExecutionID,
			InputDigest: string(g.InputDigest), RecipeDigest: string(g.RecipeDigest), BudgetDigest: string(g.BudgetDigest), ID_2: parent,
			RevisionKey: target.RevisionKey, CandidateDigest: string(target.CandidateDigest), Column14: payload})
		return err
	})
}

// AppendEditLedgerEntry appends one immutable operation under its ledger ordinal.
// It validates exact retained byte lengths; complete reversal remains a converter
// evidence check because the adapter does not read artifact bytes.
func (c *Conversion) AppendEditLedgerEntry(ctx context.Context, g Grant, target ConversionTarget, v ConversionEditInput) (ConversionReceipt, error) {
	p, err := v.payload()
	if err != nil {
		return ConversionReceipt{}, err
	}
	return c.mutate(ctx, g, target, "ledger", p, []conversionBlobRef{{Hash: v.LedgerHash}, assetRef(v.Input), assetRef(v.Output)}, func(ctx context.Context, q documentdb.Querier, parent int64, payload json.RawMessage) error {
		e := g.Execution
		_, err := q.AppendEditLedgerEntry(ctx, documentdb.AppendEditLedgerEntryParams{Column1: e.AdmissionEpoch, Column2: c.revisions.writer, Column3: "document_version",
			Column4: e.Scope.Subject.LeaseKey(), Column5: e.Owner, Column6: e.AttemptFence, Column7: g.ExecutionID, Column8: string(g.InputDigest),
			Column9: string(g.RecipeDigest), Column10: string(g.BudgetDigest), Column11: parent, Column12: target.RevisionKey, Column13: string(target.CandidateDigest), Column14: payload})
		return err
	})
}

// RecordConversionQualityReport writes a report and its complete child set in
// one transaction, including quarantined candidates with no revision row.
func (c *Conversion) RecordConversionQualityReport(ctx context.Context, g Grant, target ConversionTarget, v ConversionQualityInput) (ConversionReceipt, error) {
	p, err := v.payload()
	if err != nil {
		return ConversionReceipt{}, err
	}
	refs := []conversionBlobRef{metadataRef(v.Report)}
	if v.ArtifactHash != nil {
		refs = append(refs, conversionBlobRef{Hash: *v.ArtifactHash})
	}
	for _, record := range v.Records {
		evidence, err := conversionQualityReferences(record)
		if err != nil {
			return ConversionReceipt{}, err
		}
		refs = append(refs, evidence...)
	}
	return c.mutate(ctx, g, target, "report", p, refs, func(ctx context.Context, q documentdb.Querier, parent int64, payload json.RawMessage) error {
		e := g.Execution
		row, err := q.RecordConversionQualityReport(ctx, documentdb.RecordConversionQualityReportParams{AdmissionEpoch: e.AdmissionEpoch, AdmittedWriter: c.revisions.writer,
			ResourceKind: "document_version", ResourceKey: e.Scope.Subject.LeaseKey(), Column5: e.Owner, Column6: e.AttemptFence, ID: g.ExecutionID,
			InputDigest: string(g.InputDigest), RecipeDigest: string(g.RecipeDigest), BudgetDigest: string(g.BudgetDigest), Column11: parent,
			Column12: target.RevisionKey, CandidateDigest: string(target.CandidateDigest), Column14: payload})
		if err == nil && row.FindingCount != int64(v.RecordCount) {
			return errBulkEmpty
		}
		return err
	})
}

// RecordRevisionComparison records immutable comparison provenance only after
// SQL verifies both report bindings and the live acceptance baseline.
func (c *Conversion) RecordRevisionComparison(ctx context.Context, g Grant, target ConversionTarget, v ConversionComparisonInput) (ConversionReceipt, error) {
	p, err := v.payload(g, target)
	if err != nil {
		return ConversionReceipt{}, err
	}
	return c.mutate(ctx, g, target, "comparison", p, []conversionBlobRef{metadataRef(v.Result)}, func(ctx context.Context, q documentdb.Querier, parent int64, payload json.RawMessage) error {
		e := g.Execution
		_, err := q.RecordRevisionComparison(ctx, documentdb.RecordRevisionComparisonParams{Column1: e.AdmissionEpoch, Column2: c.revisions.writer, Column3: "document_version",
			Column4: e.Scope.Subject.LeaseKey(), Column5: e.Owner, Column6: e.AttemptFence, Column7: g.ExecutionID, Column8: string(g.InputDigest),
			Column9: string(g.RecipeDigest), Column10: string(g.BudgetDigest), Column11: parent, Column12: target.RevisionKey, Column13: string(target.CandidateDigest), Column14: payload})
		return err
	})
}

type conversionBlobRef struct {
	Hash      model.Digest `json:"hash"`
	ByteSize  *int64       `json:"byte_size"`
	ObjectRef *string      `json:"object_ref"`
}

func metadataRef(v blob.Metadata) conversionBlobRef {
	return conversionBlobRef{model.Digest(v.SHA256), &v.ByteSize, &v.ObjectRef}
}
func assetRef(v model.AssetRef) conversionBlobRef {
	return conversionBlobRef{Hash: v.SHA256, ByteSize: &v.ByteSize}
}

func conversionBlobs(ctx context.Context, q documentdb.Querier, refs []conversionBlobRef, payload json.RawMessage) error {
	rows, err := q.ReadConversionBlobReferences(ctx, payload)
	if err != nil {
		return databaseError(ctx, "resolve conversion blobs", err)
	}
	byHash := make(map[string]documentdb.ReadConversionBlobReferencesRow, len(rows))
	for _, row := range rows {
		byHash[row.Sha256] = row
	}
	for _, ref := range refs {
		row, ok := byHash[string(ref.Hash)]
		if !ok {
			return reject("conversion_blob_missing")
		}
		if (ref.ByteSize != nil && *ref.ByteSize != row.ByteSize) || (ref.ObjectRef != nil && *ref.ObjectRef != row.ObjectRef) {
			return reject("conversion_blob_reference_mismatch")
		}
	}
	return nil
}

func segmentReceipts(ctx context.Context, q documentdb.Querier, target ConversionTarget, source, destination model.AssetRef, payload json.RawMessage) ([]ConversionReceipt, error) {
	rows, err := q.ReadConversionSegmentReceipts(ctx, documentdb.ReadConversionSegmentReceiptsParams{RevisionKey: target.RevisionKey,
		CandidateDigest: string(target.CandidateDigest), SourceHash: string(source.SHA256), DestinationHash: string(destination.SHA256), SegmentKeys: payload})
	if err != nil {
		return nil, databaseError(ctx, "read map receipts", err)
	}
	result := make([]ConversionReceipt, len(rows))
	for i, row := range rows {
		result[i] = ConversionReceipt{ID: row.ID, Payload: row.Payload}
	}
	return result, nil
}

func classifySegments(rows []ConversionReceipt, items []conversionPayload, version string) ([]ConversionReceipt, bool, error) {
	byKey := make(map[[2]any]ConversionReceipt, len(rows))
	for _, row := range rows {
		value, err := jsonValue(row.Payload)
		if err != nil {
			return nil, false, reject("conversion_stored_projection")
		}
		m, ok := value.(map[string]any)
		if !ok {
			return nil, false, reject("conversion_stored_projection")
		}
		byKey[[2]any{m["transform_order"], m["segment_ordinal"]}] = row
	}
	result := make([]ConversionReceipt, 0, len(items))
	for _, item := range items {
		payload, err := conversionEncode(item)
		if err != nil {
			return nil, false, err
		}
		value, err := jsonValue(payload)
		if err != nil {
			return nil, false, err
		}
		m, ok := value.(map[string]any)
		if !ok {
			return nil, false, reject("conversion_segment_projection")
		}
		if row, ok := byKey[[2]any{m["transform_order"], m["segment_ordinal"]}]; ok {
			if !conversionMatch(row, payload, version) {
				return nil, false, conflict("offset map immutable payload")
			}
			result = append(result, row)
		}
	}
	return result, len(result) == len(items), nil
}

// InsertOffsetMapSegments inserts a bounded batch for one exact artifact pair.
// Unknown byte mappings remain null with usable anchors. Empty results force
// rollback before any fresh receipt read or retry. Receipts follow input order.
func (c *Conversion) InsertOffsetMapSegments(ctx context.Context, g Grant, target ConversionTarget, source, destination model.AssetRef, items []ConversionSegment) ([]ConversionReceipt, error) {
	if err := target.validate(g); err != nil {
		return nil, err
	}
	projection, err := conversionSegments(source, destination, items)
	if err != nil {
		return nil, err
	}
	payload, err := conversionEncode(projection)
	if err != nil {
		return nil, err
	}
	refs := []conversionBlobRef{assetRef(source), assetRef(destination)}
	for _, item := range items {
		refs = append(refs, conversionBlobRef{Hash: item.MapHash})
	}
	refPayload, err := conversionEncode(refs)
	if err != nil {
		return nil, err
	}
	for attempt := 0; attempt < 3; attempt++ {
		result, err := revisionTransaction(ctx, c.revisions, g, func(_ pgx.Tx, q documentdb.Querier) ([]ConversionReceipt, error) {
			parent, err := conversionParent(ctx, q, g, target, false)
			if err != nil {
				return nil, err
			}
			rows, err := segmentReceipts(ctx, q, target, source, destination, payload)
			if err != nil {
				return nil, err
			}
			result, complete, err := classifySegments(rows, projection, g.Execution.Scope.Subject.LeaseKey())
			if err != nil {
				return nil, err
			}
			if err := conversionBlobs(ctx, q, refs, refPayload); err != nil {
				return nil, err
			}
			if complete {
				return result, nil
			}
			if attempt == 2 {
				return nil, conflict("offset map winner unresolved after rollback")
			}
			e := g.Execution
			_, err = q.InsertOffsetMapSegments(ctx, documentdb.InsertOffsetMapSegmentsParams{AdmissionEpoch: e.AdmissionEpoch, AdmittedWriter: c.revisions.writer,
				ResourceKind: "document_version", ResourceKey: e.Scope.Subject.LeaseKey(), Column5: e.Owner, Column6: e.AttemptFence, ID: g.ExecutionID,
				InputDigest: string(g.InputDigest), RecipeDigest: string(g.RecipeDigest), BudgetDigest: string(g.BudgetDigest), ID_2: parent,
				RevisionKey: target.RevisionKey, CandidateDigest: string(target.CandidateDigest), SourceHash: string(source.SHA256), DestinationHash: string(destination.SHA256), Column16: payload})
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, errBulkEmpty
			}
			if err != nil {
				return nil, databaseError(ctx, "insert offset map", err)
			}
			rows, err = segmentReceipts(ctx, q, target, source, destination, payload)
			if err != nil {
				return nil, err
			}
			result, complete, err = classifySegments(rows, projection, g.Execution.Scope.Subject.LeaseKey())
			if err != nil {
				return nil, err
			}
			if !complete {
				return nil, errBulkEmpty
			}
			return result, nil
		})
		if !errors.Is(err, errBulkEmpty) {
			return result, err
		}
		var cleanup revisionRollbackFailure
		if errors.As(err, &cleanup) {
			return nil, errors.Join(ctx.Err(), cleanup)
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, conflict("offset map winner unresolved after rollback")
}

func conversionPage(g Grant, key string, after int64, limit int32) error {
	if after < 0 || limit <= 0 || limit > MaxConversionPageItems {
		return reject("conversion_page_bound")
	}
	if validateDocumentKey(key, g.Execution.Country, true) != nil {
		return reject("conversion_revision_key")
	}
	return nil
}

// ReadConversionArtifacts reads a bounded revision/role page. Start afterID at
// zero; use the last returned ID for the next page. Every page rechecks the grant.
func (c *Conversion) ReadConversionArtifacts(ctx context.Context, g Grant, key string, role model.ConversionArtifactRole, afterID int64, limit int32) ([]documentdb.SilverConversionArtifact, error) {
	if err := conversionPage(g, key, afterID, limit); err != nil {
		return nil, err
	}
	if !role.Valid() {
		return nil, reject("conversion_role")
	}
	return revisionTransaction(ctx, c.revisions, g, func(_ pgx.Tx, q documentdb.Querier) ([]documentdb.SilverConversionArtifact, error) {
		rows, err := q.ReadConversionArtifactsPage(ctx, documentdb.ReadConversionArtifactsPageParams{DocumentVersionKey: g.Execution.Scope.Subject.LeaseKey(), RevisionKey: key, Role: string(role), AfterID: afterID, PageLimit: limit})
		if err != nil {
			return nil, databaseError(ctx, "read conversion artifacts", err)
		}
		return rows, nil
	})
}

// ReadOffsetMapSegments reads a bounded revision/block page by database ID.
func (c *Conversion) ReadOffsetMapSegments(ctx context.Context, g Grant, key, blockID string, afterID int64, limit int32) ([]documentdb.SilverOffsetMapSegment, error) {
	if err := conversionPage(g, key, afterID, limit); err != nil {
		return nil, err
	}
	if model.ValidateKey(blockID) != nil {
		return nil, reject("conversion_block_id")
	}
	return revisionTransaction(ctx, c.revisions, g, func(_ pgx.Tx, q documentdb.Querier) ([]documentdb.SilverOffsetMapSegment, error) {
		rows, err := q.ReadOffsetMapSegmentsPage(ctx, documentdb.ReadOffsetMapSegmentsPageParams{DocumentVersionKey: g.Execution.Scope.Subject.LeaseKey(), RevisionKey: key, BlockID: blockID, AfterID: afterID, PageLimit: limit})
		if err != nil {
			return nil, databaseError(ctx, "read offset map", err)
		}
		return rows, nil
	})
}

// ReadEditLedgerByHashes reads a bounded candidate and input/output hash page.
func (c *Conversion) ReadEditLedgerByHashes(ctx context.Context, g Grant, target ConversionTarget, input, output model.Digest, afterID int64, limit int32) ([]documentdb.SilverEditLedger, error) {
	if err := conversionPage(g, target.RevisionKey, afterID, limit); err != nil {
		return nil, err
	}
	if err := conversionDigests(target.CandidateDigest, input, output); err != nil {
		return nil, err
	}
	return revisionTransaction(ctx, c.revisions, g, func(_ pgx.Tx, q documentdb.Querier) ([]documentdb.SilverEditLedger, error) {
		rows, err := q.ReadEditLedgerByHashesPage(ctx, documentdb.ReadEditLedgerByHashesPageParams{DocumentVersionKey: g.Execution.Scope.Subject.LeaseKey(), RevisionKey: target.RevisionKey, CandidateDigest: string(target.CandidateDigest), InputHash: string(input), OutputHash: string(output), AfterID: afterID, PageLimit: limit})
		if err != nil {
			return nil, databaseError(ctx, "read edit ledger", err)
		}
		return rows, nil
	})
}

// ReadConversionQualityReports reads a bounded revision/candidate report page.
func (c *Conversion) ReadConversionQualityReports(ctx context.Context, g Grant, target ConversionTarget, afterID int64, limit int32) ([]documentdb.SilverQualityReport, error) {
	if err := conversionPage(g, target.RevisionKey, afterID, limit); err != nil {
		return nil, err
	}
	if err := target.validate(g); err != nil {
		return nil, err
	}
	return revisionTransaction(ctx, c.revisions, g, func(_ pgx.Tx, q documentdb.Querier) ([]documentdb.SilverQualityReport, error) {
		rows, err := q.ReadConversionQualityReportsPage(ctx, documentdb.ReadConversionQualityReportsPageParams{DocumentVersionKey: g.Execution.Scope.Subject.LeaseKey(), RevisionKey: target.RevisionKey, CandidateDigest: string(target.CandidateDigest), AfterID: afterID, PageLimit: limit})
		if err != nil {
			return nil, databaseError(ctx, "read quality reports", err)
		}
		return rows, nil
	})
}

// ReadConversionQualityFindings reads a bounded report page and verifies its
// revision/candidate binding in SQL instead of trusting a database-local ID.
func (c *Conversion) ReadConversionQualityFindings(ctx context.Context, g Grant, target ConversionTarget, reportID, afterID int64, limit int32) ([]documentdb.SilverQualityFinding, error) {
	if err := conversionPage(g, target.RevisionKey, afterID, limit); err != nil {
		return nil, err
	}
	if err := target.validate(g); err != nil {
		return nil, err
	}
	if reportID <= 0 {
		return nil, reject("conversion_report_id")
	}
	return revisionTransaction(ctx, c.revisions, g, func(_ pgx.Tx, q documentdb.Querier) ([]documentdb.SilverQualityFinding, error) {
		rows, err := q.ReadConversionQualityFindingsPage(ctx, documentdb.ReadConversionQualityFindingsPageParams{DocumentVersionKey: g.Execution.Scope.Subject.LeaseKey(), QualityReportID: reportID, RevisionKey: target.RevisionKey, CandidateDigest: string(target.CandidateDigest), AfterID: afterID, PageLimit: limit})
		if err != nil {
			return nil, databaseError(ctx, "read quality findings", err)
		}
		return rows, nil
	})
}

// ReadRevisionComparisons reads a bounded digest-pair page of currently eligible
// comparisons. SQL derives staleness from retained acceptance events and state.
func (c *Conversion) ReadRevisionComparisons(ctx context.Context, g Grant, prior, candidate model.Digest, afterID int64, limit int32) ([]documentdb.SilverRevisionComparison, error) {
	if afterID < 0 || limit <= 0 || limit > MaxConversionPageItems {
		return nil, reject("conversion_page_bound")
	}
	if err := conversionDigests(prior, candidate); err != nil {
		return nil, err
	}
	return revisionTransaction(ctx, c.revisions, g, func(_ pgx.Tx, q documentdb.Querier) ([]documentdb.SilverRevisionComparison, error) {
		rows, err := q.ReadRevisionComparisonsPage(ctx, documentdb.ReadRevisionComparisonsPageParams{DocumentVersionKey: g.Execution.Scope.Subject.LeaseKey(), PriorCandidateDigest: string(prior), CandidateDigest: string(candidate), AfterID: afterID, PageLimit: limit})
		if err != nil {
			return nil, databaseError(ctx, "read revision comparisons", err)
		}
		return rows, nil
	})
}
