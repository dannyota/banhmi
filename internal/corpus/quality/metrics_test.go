package quality

import (
	"sort"
	"testing"

	"danny.vn/banhmi/internal/corpus/model"
)

func TestGateAllBaselineMetrics(t *testing.T) {
	want := map[model.MetricKind]string{
		model.MetricGeometry:               "BASELINE_UNQUALIFIED",
		model.MetricFidelity:               "BASELINE_UNQUALIFIED",
		model.MetricPageAccounting:         "PAGE_INVENTORY_UNAVAILABLE",
		model.MetricTextCoverage:           "REGION_INVENTORY_UNAVAILABLE",
		model.MetricTableIntegrity:         "TABLE_INVENTORY_UNAVAILABLE",
		model.MetricMetadataAgreement:      "METADATA_UNAVAILABLE",
		model.MetricStructureCompleteness:  "SOURCE_STRUCTURE_UNAVAILABLE",
		model.MetricProvenanceCompleteness: "PROVENANCE_VERIFICATION_UNAVAILABLE",
		model.MetricDiacriticValidity:      "LANGUAGE_RULES_UNQUALIFIED",
		model.MetricCitationParse:          "LANGUAGE_RULES_UNQUALIFIED",
		model.MetricCorruption:             "CORRUPTION_RULES_UNQUALIFIED",
	}
	g, request, refs := fixtureGate(t)
	policy := g.policy
	policy.ArtifactProfile = []model.MetricRequirement{}
	for kind := range want {
		policy.ArtifactProfile = append(policy.ArtifactProfile, model.MetricRequirement{Kind: kind, Mandatory: kind == model.MetricGeometry || kind == model.MetricFidelity, AllowNotApplicable: true, Threshold: "min:0"})
	}
	sort.Slice(policy.ArtifactProfile, func(i, j int) bool { return policy.ArtifactProfile[i].Kind < policy.ArtifactProfile[j].Kind })
	g, err := NewGate(Options{Policy: policy, Limits: g.limits})
	if err != nil {
		t.Fatal("numerical profile rejected")
	}
	request.Profile = g.profile
	report, err := g.Report(request, refs)
	if err != nil || report.Artifact.Outcome != model.QualityQuarantined || len(report.Artifact.Metrics) != len(want) {
		t.Fatal("all-metric report failed")
	}
	for i, metric := range report.Artifact.Metrics {
		if metric.Kind != policy.ArtifactProfile[i].Kind || metric.ReasonCode != want[metric.Kind] || metric.State != model.MetricUnknown || metric.Passed || metric.Numerator != 0 || metric.Denominator != 0 || metric.Threshold != "min:0" || len(metric.Evidence) != 0 || len(metric.Exclusions) != 0 {
			t.Fatal("baseline metric definition mismatch")
		}
		severity := model.SeverityWarning
		if metric.Mandatory {
			severity = model.SeverityBlocking
		}
		if report.Artifact.Findings[i].Severity != severity {
			t.Fatal("metric finding severity mismatch")
		}
	}
}
