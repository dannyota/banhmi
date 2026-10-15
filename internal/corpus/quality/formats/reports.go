package formats

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"danny.vn/banhmi/internal/corpus/model"
)

// ReportSentinel returns the exact self-reference retained in gate-report-v1.
func ReportSentinel() model.AssetRef {
	return model.AssetRef{Path: "reports/self", SHA256: model.Digest(strings.Repeat("0", 64)), ByteSize: 0}
}

// GateReportCodec constructs a gate-report-v1 codec from a trusted policy.
// Policy and caller bounds both apply; the stricter value wins.
func GateReportCodec(policy PolicyBundle, limits Limits) (Codec[GateReport], error) {
	profile, err := trustedProfile(policy, limits, false)
	if err != nil {
		return Codec[GateReport]{}, err
	}
	threshold := policy.ThresholdVersion
	return Codec[GateReport]{limits: stricter(limits, policy.AcceptanceLimits), validate: func(v GateReport) error {
		if v.SchemaVersion != "gate-report-v1" || v.Artifact.Report != ReportSentinel() || v.Artifact.ThresholdVersion != threshold {
			return fmt.Errorf("invalid gate report version, sentinel or threshold")
		}
		converted := v.RequestManifest != nil && v.Document != nil && v.OutputManifest != nil
		missing := v.RequestManifest == nil && v.Document == nil && v.OutputManifest == nil
		if !converted && !missing {
			return fmt.Errorf("conversion references must be all present or all null")
		}
		if missing && (v.Artifact.Outcome != model.QualityQuarantined && v.Artifact.Outcome != model.QualityNeedsOCR || len(v.Artifact.Findings) == 0) {
			return fmt.Errorf("missing conversion requires ineligible outcome and explicit finding")
		}
		if err := decisionOutcome(v.Artifact.Outcome, v.Artifact.DecisionRef); err != nil {
			return err
		}
		if err := metricOrder(v.Artifact.Metrics, profile); err != nil {
			return err
		}
		// Validate a local copy with a content-addressed reference after verifying
		// the sentinel. Retained bytes are never rewritten. Catalog binding is
		// verified separately by ValidateReportReference.
		data, err := json.Marshal(v)
		if err != nil {
			return err
		}
		local := v.Artifact
		local.Report = model.AssetRef{Path: "reports/" + string(Hash(data)), SHA256: Hash(data), ByteSize: int64(len(data))}
		return local.Validate(profile)
	}}, nil
}

// ValidateReportReference verifies a catalog-issued report reference and returns
// the local model report with that reference substituted for its sentinel.
func ValidateReportReference(v GateReport, ref model.AssetRef, policy PolicyBundle, limits Limits) (model.QualityReport, error) {
	codec, err := GateReportCodec(policy, limits)
	if err != nil {
		return model.QualityReport{}, err
	}
	data, digest, err := codec.Encode(v)
	if err != nil {
		return model.QualityReport{}, err
	}
	if err := ref.Validate(); err != nil {
		return model.QualityReport{}, err
	}
	if ref.SHA256 != digest || ref.ByteSize != int64(len(data)) {
		return model.QualityReport{}, fmt.Errorf("catalog report hash or size mismatch")
	}
	local := v.Artifact
	local.Report = ref
	profile, err := trustedProfile(policy, limits, false)
	if err != nil {
		return model.QualityReport{}, err
	}
	return local, local.Validate(profile)
}

// GateRollupCodec constructs the gate-rollup-v1 rule A codec from pinned policy.
func GateRollupCodec(policy PolicyBundle, limits Limits) (Codec[GateRollup], error) {
	profile, err := trustedProfile(policy, limits, true)
	if err != nil {
		return Codec[GateRollup]{}, err
	}
	threshold := policy.ThresholdVersion
	reasons := append([]string(nil), policy.GapReasonCodes...)
	return Codec[GateRollup]{limits: stricter(limits, policy.AcceptanceLimits), validate: func(v GateRollup) error {
		if v.SchemaVersion != "gate-rollup-v1" || v.PolicyHash != profile.PolicyHash || v.ThresholdVersion != threshold || len(v.ArtifactReports) == 0 {
			return fmt.Errorf("invalid roll-up version, policy or artifact set")
		}
		if err := decisionOutcome(v.Outcome, v.DecisionRef); err != nil {
			return err
		}
		accepting := v.Outcome == model.QualityAccepted || v.Outcome == model.QualityAcceptedWithGaps
		needsOCR := false
		for i, report := range v.ArtifactReports {
			if report.Ordinal != i || !report.Outcome.Valid() {
				return fmt.Errorf("roll-up report ordinals or outcome invalid")
			}
			needsOCR = needsOCR || report.Outcome == model.QualityNeedsOCR
			if accepting && (report.Outcome == model.QualityQuarantined || report.Outcome == model.QualityNeedsOCR) {
				return fmt.Errorf("ineligible artifact prevents accepting roll-up")
			}
		}
		if needsOCR && v.Outcome != model.QualityNeedsOCR {
			return fmt.Errorf("needs_ocr artifact takes precedence")
		}
		for i, ordinal := range v.MissingOrdinals {
			if ordinal < 0 || ordinal >= len(v.ArtifactReports) || (i > 0 && v.MissingOrdinals[i-1] >= ordinal) {
				return fmt.Errorf("missing ordinals must be sorted unique members")
			}
			if v.ArtifactReports[ordinal].Outcome != model.QualityQuarantined && v.ArtifactReports[ordinal].Outcome != model.QualityNeedsOCR {
				return fmt.Errorf("missing conversion cannot have accepting artifact report")
			}
		}
		if (len(v.MissingOrdinals) == 0) != (v.OutputManifest != nil) || (accepting && len(v.MissingOrdinals) > 0) {
			return fmt.Errorf("output presence disagrees with complete conversion")
		}
		if (v.Outcome == model.QualityAccepted && len(v.Gaps) > 0) || (v.Outcome == model.QualityAcceptedWithGaps && len(v.Gaps) == 0) {
			return fmt.Errorf("accepting outcome disagrees with gaps")
		}
		if err := coverageShape(v.Gaps, v.Retained); err != nil {
			return err
		}
		if len(v.Retained) != len(v.ArtifactReports) {
			return fmt.Errorf("one retained entry required per artifact ordinal")
		}
		for _, ordinal := range v.MissingOrdinals {
			explicit := false
			for _, gap := range v.Gaps {
				if gap.Ordinal == ordinal {
					if len(gap.Spans) != 0 {
						return fmt.Errorf("missing text cannot have excluded byte ranges")
					}
					explicit = explicit || gap.Scope == model.GapArtifact || gap.Scope == model.GapDocumentRevision
				}
			}
			if len(v.Retained[ordinal].Spans) != 0 || !explicit {
				return fmt.Errorf("missing conversion requires empty retained spans and explicit gap")
			}
		}
		for _, gap := range v.Gaps {
			if gap.Ordinal >= len(v.ArtifactReports) || !contains(reasons, gap.ReasonCode) {
				return fmt.Errorf("gap ordinal or reason not selected by policy")
			}
		}
		if err := metricOrder(v.Metrics, profile); err != nil {
			return err
		}
		local := model.QualityReport{
			ArtifactHash:     v.CandidateDigest,
			CandidateDigest:  v.CandidateDigest,
			Report:           ReportSentinel(),
			PolicyHash:       v.PolicyHash,
			ThresholdVersion: v.ThresholdVersion,
			Outcome:          v.Outcome,
			Metrics:          v.Metrics,
			Findings:         v.Findings,
			DecisionRef:      v.DecisionRef,
		}
		return local.Validate(profile)
	}}, nil
}

// GapScopeCodec constructs the synthetic gap-scope-v1 rule A codec. Consumers
// must compare its selection with the report and separately authorize decisions.
func GapScopeCodec(limits Limits) Codec[GapScope] {
	return Codec[GapScope]{limits: limits, validate: func(v GapScope) error {
		if v.SchemaVersion != "gap-scope-v1" {
			return fmt.Errorf("unknown gap scope version")
		}
		return coverageShape(v.Gaps, v.Retained)
	}}
}

// ValidateScopeBinding compares decision evidence with the selected roll-up.
// OutputDigest is the candidate output hash, or the roll-up hash when absent.
func ValidateScopeBinding(scope GapScope, rollup GateRollup, original model.Digest, limits Limits) error {
	if _, _, err := GapScopeCodec(limits).Encode(scope); err != nil {
		return err
	}
	data, err := json.Marshal(rollup)
	if err != nil {
		return err
	}
	output := Hash(data)
	if rollup.OutputManifest != nil {
		output = rollup.OutputManifest.SHA256
	}
	selected := false
	for _, report := range rollup.ArtifactReports {
		selected = selected || report.OriginalHash == original
	}
	if !selected || scope.CandidateDigest != rollup.CandidateDigest || scope.OriginalHash != original || scope.OutputDigest != output || !reflect.DeepEqual(scope.Gaps, rollup.Gaps) || !reflect.DeepEqual(scope.Retained, rollup.Retained) {
		return fmt.Errorf("decision scope selection mismatch")
	}
	return nil
}

func metricOrder(metrics []model.QualityMetric, profile model.QualityMetricProfile) error {
	if len(metrics) != len(profile.Metrics) {
		return fmt.Errorf("metric count differs from profile")
	}
	for i, metric := range metrics {
		if metric.Kind != profile.Metrics[i].Kind {
			return fmt.Errorf("metrics must follow trusted profile order")
		}
	}
	return nil
}

func decisionOutcome(outcome model.QualityOutcome, decision string) error {
	if !outcome.Valid() || (outcome == model.QualityAcceptedWithGaps) != (decision != "") {
		return fmt.Errorf("decision reference only permitted for named-gap acceptance")
	}
	return nil
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func stricter(a, b Limits) Limits {
	return Limits{
		MaxBlobs:       min(a.MaxBlobs, b.MaxBlobs),
		MaxRows:        min(a.MaxRows, b.MaxRows),
		MaxJSONBytes:   min(a.MaxJSONBytes, b.MaxJSONBytes),
		MaxObjectBytes: min(a.MaxObjectBytes, b.MaxObjectBytes),
		MaxElapsedMS:   min(a.MaxElapsedMS, b.MaxElapsedMS),
	}
}
