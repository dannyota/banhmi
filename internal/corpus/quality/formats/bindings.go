package formats

import (
	"fmt"
	"reflect"

	"danny.vn/banhmi/internal/corpus/model"
)

// ValidateRollupBinding verifies candidate, policy, report and output selections
// over decoded values. Reports use input ordinal order. Physical object bytes
// and original execution provenance remain acceptance's responsibility.
func ValidateRollupBinding(input CandidateInput, output *CandidateOutput, reports []GateReport, rollup GateRollup, policy PolicyBundle, limits Limits) error {
	_, candidate, err := CandidateInputCodec(limits).Encode(input)
	if err != nil {
		return err
	}
	_, policyHash, err := PolicyBundleCodec(limits).Encode(policy)
	if err != nil {
		return err
	}
	if input.GatePolicyDigest != policyHash || input.Country != policy.Country || input.ThresholdVersion != policy.ThresholdVersion {
		return fmt.Errorf("candidate selected policy mismatch")
	}
	codec, err := GateRollupCodec(policy, limits)
	if err != nil {
		return err
	}
	if _, _, err := codec.Encode(rollup); err != nil {
		return err
	}
	if rollup.CandidateDigest != candidate || len(reports) != len(input.Artifacts) || len(rollup.ArtifactReports) != len(input.Artifacts) {
		return fmt.Errorf("roll-up does not cover selected candidate")
	}
	artifactCodec, err := GateReportCodec(policy, limits)
	if err != nil {
		return err
	}
	missing := make([]int, 0)
	coverage := make([]CoverageArtifact, len(reports))
	for i, report := range reports {
		data, hash, err := artifactCodec.Encode(report)
		if err != nil {
			return err
		}
		selected := rollup.ArtifactReports[i]
		if report.Artifact.CandidateDigest != candidate || report.Artifact.ArtifactHash != input.Artifacts[i].Capture.Asset.SHA256 || selected.OriginalHash != report.Artifact.ArtifactHash || selected.Report.SHA256 != hash || selected.Report.ByteSize != int64(len(data)) || selected.Outcome != report.Artifact.Outcome {
			return fmt.Errorf("artifact report selection mismatch")
		}
		coverage[i].Ordinal = i
		if report.Document == nil {
			missing = append(missing, i)
		}
	}
	if !reflect.DeepEqual(missing, rollup.MissingOrdinals) || (output == nil) != (rollup.OutputManifest == nil) {
		return fmt.Errorf("missing conversion or output selection mismatch")
	}
	if output != nil {
		if err := ValidateCandidateBinding(input, *output, limits); err != nil {
			return err
		}
		data, hash, err := CandidateOutputCodec(limits).Encode(*output)
		if err != nil {
			return err
		}
		if rollup.OutputManifest.SHA256 != hash || rollup.OutputManifest.ByteSize != int64(len(data)) {
			return fmt.Errorf("candidate output reference mismatch")
		}
		for i := range coverage {
			ref := output.Artifacts[i].NormalizedText
			coverage[i].NormalizedText = &ref
		}
		return ValidatePartition(rollup.Gaps, rollup.Retained, coverage, stricter(limits, policy.AcceptanceLimits))
	}
	// Partial conversion has no complete output manifest. Its available text
	// lengths must be supplied separately to ValidatePartition by the consumer
	// after decoding each retained M1.12 document.
	return nil
}

// ValidateNormalizationBinding verifies that every decoded map transform and
// ledger rule/version occurs in the normalizer manifest derived by the producer.
// Frozen M1.12 collections may be null. Validation preserves their retained bytes.
func ValidateNormalizationBinding(normalizer Normalizer, maps []model.OffsetMapSegment, ledger []model.EditLedgerEntry, limits Limits) error {
	if _, _, err := NormalizerCodec(limits).Encode(normalizer); err != nil {
		return err
	}
	if err := validateTrees(limits, true, struct {
		Maps   []model.OffsetMapSegment
		Ledger []model.EditLedgerEntry
	}{maps, ledger}); err != nil {
		return err
	}
	for _, segment := range maps {
		if err := segment.Validate(); err != nil {
			return err
		}
		if !contains(normalizer.Transforms, segment.Transform) {
			return fmt.Errorf("unlisted map transform")
		}
	}
	for _, entry := range ledger {
		if err := entry.Validate(); err != nil {
			return err
		}
		found := false
		for _, rule := range normalizer.Rules {
			found = found || (rule.Rule == entry.Rule && rule.Version == entry.Version)
		}
		if !found {
			return fmt.Errorf("unlisted ledger rule or version")
		}
	}
	return nil
}
