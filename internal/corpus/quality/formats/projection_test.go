package formats

import (
	"strings"
	"testing"

	"danny.vn/banhmi/internal/corpus/model"
)

func TestQualityRecordOrderAndParents(t *testing.T) {
	limits := DefaultLimits()
	policy := fixturePolicy(t)
	codec, err := GateReportCodec(policy, limits)
	if err != nil {
		t.Fatal(err)
	}
	report := decodedFixture(t, "gate-report-pre-conversion", codec)
	gap := Gap{Ordinal: 0, Scope: model.GapArtifact, ScopeKey: "artifact-1", ReasonCode: "CONVERSION_MISSING", Spans: []model.Span{}}
	rows, err := ProjectQualityRows(report.Artifact.Metrics, report.Artifact.Findings, []Gap{gap}, []model.AssetRef{}, limits)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 6 {
		t.Fatal("lost child records")
	}
	for i, row := range rows {
		if row.Ordinal != int32(i) || row.SourceSpans == nil || row.Evidence == nil {
			t.Fatal("ordinal or non-null array mismatch")
		}
		if i < 2 {
			if row.RecordKind != model.QualityRecordMetric || row.Code != string(report.Artifact.Metrics[i].Kind) || row.MetricKind == nil || row.Severity != nil || row.Scope != nil || row.ScopeKey != nil || row.Reason == nil {
				t.Fatal("metric row fields differ from spec")
			}
		} else {
			if row.Severity == nil || row.Scope == nil || row.ScopeKey == nil || row.Reason == nil || row.Code != *row.Reason || row.MetricKind != nil || row.MetricState != nil || row.Numerator != nil || row.Denominator != nil || row.Threshold != nil || row.Mandatory != nil || row.Passed != nil {
				t.Fatal("finding/gap null fields differ from spec")
			}
		}
	}
	if rows[5].RecordKind != model.QualityRecordGap || *rows[5].Severity != model.SeverityWarning {
		t.Fatal("gap must follow findings with warning severity")
	}
	data, hash, err := codec.Encode(report)
	if err != nil {
		t.Fatal(err)
	}
	ref := model.AssetRef{Path: "catalog/report", SHA256: hash, ByteSize: int64(len(data))}
	parent, err := ProjectArtifactParent(report, ref, policy, limits)
	if err != nil || parent.ArtifactHash == nil || *parent.ArtifactHash != report.Artifact.ArtifactHash || parent.RecordCount != 5 {
		t.Fatal("artifact parent projection mismatch", err)
	}
	rollupCodec, err := GateRollupCodec(policy, limits)
	if err != nil {
		t.Fatal(err)
	}
	rollup := decodedFixture(t, "gate-rollup-pre-conversion", rollupCodec)
	data, hash, err = rollupCodec.Encode(rollup)
	if err != nil {
		t.Fatal(err)
	}
	ref = model.AssetRef{Path: "catalog/rollup", SHA256: hash, ByteSize: int64(len(data))}
	parent, err = ProjectRollupParent(rollup, ref, policy, limits)
	if err != nil || parent.ArtifactHash != nil || parent.RecordCount != 5 {
		t.Fatal("roll-up parent projection mismatch", err)
	}
	tooMany := make([]model.Finding, 1001)
	for i := range tooMany {
		tooMany[i] = report.Artifact.Findings[0]
	}
	if _, err := ProjectQualityRows([]model.QualityMetric{}, tooMany, []Gap{}, []model.AssetRef{}, limits); err == nil {
		t.Fatal("1000-record writer ceiling ignored")
	}
}

func TestNormalizationMembership(t *testing.T) {
	limits := DefaultLimits()
	asset := model.AssetRef{Path: "objects/text", SHA256: model.Digest(strings.Repeat("a", 64)), ByteSize: 3}
	span := model.Span{ArtifactHash: asset.SHA256, Space: model.RawText, Range: &model.ByteRange{Start: 0, End: 3}}
	maps := []model.OffsetMapSegment{{Sources: []model.Span{span}, Destinations: []model.Span{span}, Transform: "identity-v1", Ordinal: 0}}
	ledger := []model.EditLedgerEntry{{
		Kind:        model.EditNormalize,
		Ordinal:     0,
		Rule:        "nfc-crlf",
		Version:     "1",
		Inputs:      []model.Span{span},
		Outputs:     []model.Span{span},
		Removed:     &asset,
		Replacement: &asset,
		Evidence:    asset,
	}}
	if err := ValidateNormalizationBinding(BaselineNormalizer(), maps, ledger, limits); err != nil {
		t.Fatal(err)
	}
	maps[0].Transform = "unlisted"
	if ValidateNormalizationBinding(BaselineNormalizer(), maps, ledger, limits) == nil {
		t.Fatal("unlisted transform accepted")
	}
	maps[0].Transform = "identity-v1"
	ledger[0].Version = "2"
	if ValidateNormalizationBinding(BaselineNormalizer(), maps, ledger, limits) == nil {
		t.Fatal("unlisted rule version accepted")
	}
	for _, normalizer := range []Normalizer{
		{SchemaVersion: "normalizer-v1", Rules: []NormalizationRule{{Rule: "nfc-crlf", Version: "1"}, {Rule: "nfc-crlf", Version: "1"}}, Transforms: []string{"identity-v1"}},
		{SchemaVersion: "normalizer-v1", Rules: []NormalizationRule{{Rule: "nfc-crlf", Version: "1"}}, Transforms: []string{"source-decode-v1", "identity-v1"}},
	} {
		if _, _, err := NormalizerCodec(limits).Encode(normalizer); err == nil {
			t.Fatal("duplicate rules or unsorted transforms accepted")
		}
	}
}

func TestRootAndPointerBounds(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxObjectBytes = 1
	if _, _, err := ParserCodec(limits).Encode(Parser{SchemaVersion: "parser-v1", Parser: "none"}); err == nil {
		t.Fatal("root bytes excluded from object bound")
	}
	limits = DefaultLimits()
	input := decodedFixture(t, "candidate-input-v1", CandidateInputCodec(limits))
	// The input names five distinct objects. Its root is a sixth object.
	limits.MaxBlobs = 5
	if _, _, err := CandidateInputCodec(limits).Encode(input); err == nil {
		t.Fatal("root excluded from blob bound")
	}
	limits.MaxBlobs = 6
	if _, _, err := CandidateInputCodec(limits).Encode(input); err != nil {
		t.Fatal("exact root plus dependency bound rejected", err)
	}
	page := 2147483648
	asset := model.AssetRef{Path: "objects/source", SHA256: model.Digest(strings.Repeat("a", 64)), ByteSize: 3}
	span := model.Span{ArtifactHash: asset.SHA256, Space: model.OriginalBytes, Page: &page, ReasonCode: "BYTE_MAPPING_UNAVAILABLE"}
	if _, err := ProjectReportSpan(span, asset); err == nil {
		t.Fatal("page overflow accepted by writer projection")
	}
	span.Page = nil
	span.Range = &model.ByteRange{Start: 0, End: 3}
	span.ReasonCode = "invalid"
	if _, err := ProjectReportSpan(span, asset); err == nil {
		t.Fatal("ranged span with reason accepted")
	}
	span.ReasonCode = ""
	span.BlockID = "invalid:local:key"
	if _, err := ProjectReportSpan(span, asset); err == nil {
		t.Fatal("invalid writer block key accepted")
	}
}
