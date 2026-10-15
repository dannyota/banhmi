package formats

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"danny.vn/banhmi/internal/corpus/model"
)

// QualityRow is a pure projection of one quality_finding child. Pointer fields
// outside the record kind remain null. Evidence uses catalog paths; SourceSpans
// uses model coordinate names, as required for report JSON storage.
type QualityRow struct {
	Ordinal     int32
	RecordKind  model.QualityRecordKind
	Code        string
	Severity    *model.Severity
	Scope       *model.GapKind
	ScopeKey    *string
	Reason      *string
	MetricKind  *model.MetricKind
	MetricState *model.MetricState
	Numerator   *int64
	Denominator *int64
	Threshold   *string
	Mandatory   *bool
	Passed      *bool
	Evidence    []model.AssetRef
	SourceSpans []model.Span
}

// QualityParent is the content projection of a quality_report parent. Target,
// revision and original execution bindings are supplied by the writer consumer.
type QualityParent struct {
	Report           model.AssetRef
	ArtifactHash     *model.Digest
	PolicyHash       model.Digest
	ThresholdVersion string
	Outcome          model.QualityOutcome
	RecordCount      int32
}

// ProjectQualityRows numbers metrics, findings, then gaps and resolves every
// evidence reference through the supplied verified catalog. It enforces M1.5's
// 1000-record and 4 MiB transport ceilings in addition to policy bounds.
func ProjectQualityRows(metrics []model.QualityMetric, findings []model.Finding, gaps []Gap, catalog []model.AssetRef, limits Limits) ([]QualityRow, error) {
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	if err := validateTrees(limits, false, struct {
		Metrics  []model.QualityMetric
		Findings []model.Finding
		Gaps     []Gap
	}{metrics, findings, gaps}, catalog); err != nil {
		return nil, err
	}
	if len(metrics) > 1000 || len(findings) > 1000-len(metrics) || len(gaps) > 1000-len(metrics)-len(findings) {
		return nil, fmt.Errorf("quality projection exceeds 1000 records")
	}
	objects := make(map[model.Digest]model.AssetRef, len(catalog))
	for _, ref := range catalog {
		if old, ok := objects[ref.SHA256]; ok && old != ref {
			return nil, fmt.Errorf("ambiguous catalog-issued reference")
		}
		objects[ref.SHA256] = ref
	}
	rows := make([]QualityRow, 0, len(metrics)+len(findings)+len(gaps))
	appendRow := func(row QualityRow, evidence []model.AssetRef, spans []model.Span) error {
		refs := make(map[model.Digest]model.AssetRef)
		for _, ref := range evidence {
			resolved, ok := objects[ref.SHA256]
			if !ok || resolved.ByteSize != ref.ByteSize {
				return fmt.Errorf("qualification evidence lacks exact catalog reference")
			}
			refs[ref.SHA256] = resolved
		}
		row.SourceSpans = make([]model.Span, len(spans))
		for i, span := range spans {
			ref, ok := objects[span.ArtifactHash]
			if !ok {
				return fmt.Errorf("span hash cannot introduce uncataloged evidence")
			}
			projected, err := ProjectReportSpan(span, ref)
			if err != nil {
				return err
			}
			refs[span.ArtifactHash] = ref
			row.SourceSpans[i] = projected
		}
		row.Evidence = make([]model.AssetRef, 0, len(refs))
		for _, ref := range refs {
			row.Evidence = append(row.Evidence, ref)
		}
		sort.Slice(row.Evidence, func(i, j int) bool { return row.Evidence[i].SHA256 < row.Evidence[j].SHA256 })
		row.Ordinal = int32(len(rows))
		rows = append(rows, row)
		return nil
	}
	for _, metric := range metrics {
		if err := metric.Validate(); err != nil {
			return nil, err
		}
		row := QualityRow{
			RecordKind:  model.QualityRecordMetric,
			Code:        string(metric.Kind),
			Reason:      optionalString(metric.ReasonCode),
			MetricKind:  &metric.Kind,
			MetricState: &metric.State,
			Numerator:   &metric.Numerator,
			Denominator: &metric.Denominator,
			Threshold:   &metric.Threshold,
			Mandatory:   &metric.Mandatory,
			Passed:      &metric.Passed,
		}
		if err := appendRow(row, metric.Evidence, metric.Exclusions); err != nil {
			return nil, err
		}
	}
	for _, finding := range findings {
		if err := finding.Validate(); err != nil {
			return nil, err
		}
		row := QualityRow{
			RecordKind: model.QualityRecordFinding,
			Code:       finding.ReasonCode,
			Severity:   &finding.Severity,
			Scope:      &finding.Scope,
			ScopeKey:   &finding.ScopeKey,
			Reason:     &finding.ReasonCode,
		}
		if err := appendRow(row, finding.Evidence, finding.Spans); err != nil {
			return nil, err
		}
	}
	for _, gap := range gaps {
		if !gap.Scope.Valid() || gap.ScopeKey == "" || gap.ReasonCode == "" || gap.Ordinal < 0 {
			return nil, fmt.Errorf("invalid projected gap")
		}
		severity := model.SeverityWarning
		row := QualityRow{
			RecordKind: model.QualityRecordGap,
			Code:       gap.ReasonCode,
			Severity:   &severity,
			Scope:      &gap.Scope,
			ScopeKey:   &gap.ScopeKey,
			Reason:     &gap.ReasonCode,
		}
		if err := appendRow(row, nil, gap.Spans); err != nil {
			return nil, err
		}
	}
	data, err := qualityRowsJSON(rows)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > min(limits.MaxJSONBytes, 4194304) {
		return nil, fmt.Errorf("quality projection exceeds transport byte bound")
	}
	return rows, nil
}

func qualityRowsJSON(rows []QualityRow) ([]byte, error) {
	values := make([]map[string]any, len(rows))
	for i, row := range rows {
		values[i] = map[string]any{
			"ordinal":      row.Ordinal,
			"record_kind":  row.RecordKind,
			"code":         row.Code,
			"severity":     row.Severity,
			"scope_kind":   row.Scope,
			"scope_key":    row.ScopeKey,
			"reason":       row.Reason,
			"metric_kind":  row.MetricKind,
			"metric_state": row.MetricState,
			"numerator":    row.Numerator,
			"denominator":  row.Denominator,
			"threshold":    row.Threshold,
			"mandatory":    row.Mandatory,
			"passed":       row.Passed,
			"evidence":     row.Evidence,
			"source_spans": row.SourceSpans,
		}
	}
	return json.Marshal(values)
}

// ProjectReportSpan clears token ordinals and supplies the reserved document
// block ID for an empty ID. Every other field and the model space name survives.
func ProjectReportSpan(span model.Span, asset model.AssetRef) (model.Span, error) {
	for _, text := range []string{span.Object, span.Member, span.ReasonCode} {
		if !utf8.ValidString(text) || strings.ContainsRune(text, '\x00') {
			return model.Span{}, fmt.Errorf("projected span text violates writer constraints")
		}
	}
	if err := span.Validate(); err != nil {
		return model.Span{}, err
	}
	if err := asset.Validate(); err != nil {
		return model.Span{}, err
	}
	if span.ArtifactHash != asset.SHA256 || (span.Range != nil && (span.Range.End > asset.ByteSize || span.ReasonCode != "")) {
		return model.Span{}, fmt.Errorf("projected span hash, reason or byte bound mismatch")
	}
	span.TokenOrdinal = nil
	if span.BlockID == "" {
		span.BlockID = "document"
	}
	if model.ValidateKey(span.BlockID) != nil || (span.Page != nil && int64(*span.Page) > 2147483647) {
		return model.Span{}, fmt.Errorf("projected block ID or page exceeds writer constraints")
	}
	// Copy pointers so the projection cannot mutate decoded retained values.
	if span.Range != nil {
		rangeCopy := *span.Range
		span.Range = &rangeCopy
	}
	if span.Page != nil {
		page := *span.Page
		span.Page = &page
	}
	return span, nil
}

// WriterSpace maps native model coordinates to the frozen SQL vocabulary.
func WriterSpace(space model.CoordinateSpace) (model.ProvenanceSpace, error) {
	switch space {
	case model.OriginalBytes:
		return model.ProvenanceOriginalBytes, nil
	case model.DecodedMember:
		return model.ProvenanceMemberBytes, nil
	case model.RawText:
		return model.ProvenanceRawText, nil
	case model.NormalizedText:
		return model.ProvenanceNormalizedText, nil
	default:
		return "", fmt.Errorf("unknown coordinate space")
	}
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// ProjectArtifactParent verifies the report reference and projects artifact
// parent fields. Child rows must be produced from the same decoded report.
func ProjectArtifactParent(report GateReport, ref model.AssetRef, policy PolicyBundle, limits Limits) (QualityParent, error) {
	local, err := ValidateReportReference(report, ref, policy, limits)
	if err != nil {
		return QualityParent{}, err
	}
	count := len(local.Metrics) + len(local.Findings)
	if count > 1000 {
		return QualityParent{}, fmt.Errorf("quality parent exceeds record ceiling")
	}
	return QualityParent{
		Report:           ref,
		ArtifactHash:     &local.ArtifactHash,
		PolicyHash:       local.PolicyHash,
		ThresholdVersion: local.ThresholdVersion,
		Outcome:          local.Outcome,
		RecordCount:      int32(count),
	}, nil
}

// ProjectRollupParent verifies exact roll-up bytes against catalog metadata.
// Its artifact hash remains null.
func ProjectRollupParent(report GateRollup, ref model.AssetRef, policy PolicyBundle, limits Limits) (QualityParent, error) {
	codec, err := GateRollupCodec(policy, limits)
	if err != nil {
		return QualityParent{}, err
	}
	data, digest, err := codec.Encode(report)
	if err != nil {
		return QualityParent{}, err
	}
	if err := ref.Validate(); err != nil {
		return QualityParent{}, err
	}
	count := len(report.Metrics) + len(report.Findings) + len(report.Gaps)
	if ref.SHA256 != digest || ref.ByteSize != int64(len(data)) || count > 1000 {
		return QualityParent{}, fmt.Errorf("roll-up catalog reference or record ceiling mismatch")
	}
	return QualityParent{
		Report:           ref,
		PolicyHash:       report.PolicyHash,
		ThresholdVersion: report.ThresholdVersion,
		Outcome:          report.Outcome,
		RecordCount:      int32(count),
	}, nil
}
