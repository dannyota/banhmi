package formats

import (
	"encoding/json"
	"strings"
	"testing"

	"danny.vn/banhmi/internal/corpus/model"
)

func TestQualityRowsRejectInvalidWriterText(t *testing.T) {
	for _, field := range []string{
		"metric-kind", "metric-state", "metric-reason", "metric-threshold",
		"finding-code-reason", "finding-severity", "finding-scope", "finding-scope-key",
		"gap-code-reason", "gap-scope", "gap-scope-key", "evidence-path", "evidence-hash",
	} {
		t.Run(field, func(t *testing.T) {
			project := func(value string) ([]QualityRow, error) {
				metric := model.QualityMetric{Kind: model.MetricGeometry, State: model.MetricUnknown, Threshold: "ngưỡng", ReasonCode: "CHƯA_ĐO", Evidence: []model.AssetRef{}, Exclusions: []model.Span{}}
				finding := model.Finding{ReasonCode: "CẢNH_BÁO", Severity: model.SeverityWarning, Scope: model.GapArtifact, ScopeKey: "phạm vi", Evidence: []model.AssetRef{}, Spans: []model.Span{}}
				gap := Gap{Scope: model.GapArtifact, ScopeKey: "phạm vi", ReasonCode: "THIẾU", Spans: []model.Span{}}
				asset := model.AssetRef{Path: "catalog/văn-bản", SHA256: model.Digest(strings.Repeat("a", 64)), ByteSize: 1}
				if value != "" {
					switch field {
					case "metric-kind":
						metric.Kind = model.MetricKind(value)
					case "metric-state":
						metric.State = model.MetricState(value)
					case "metric-reason":
						metric.ReasonCode = value
					case "metric-threshold":
						metric.Threshold = value
					case "finding-code-reason":
						finding.ReasonCode = value
					case "finding-severity":
						finding.Severity = model.Severity(value)
					case "finding-scope":
						finding.Scope = model.GapKind(value)
					case "finding-scope-key":
						finding.ScopeKey = value
					case "gap-code-reason":
						gap.ReasonCode = value
					case "gap-scope":
						gap.Scope = model.GapKind(value)
					case "gap-scope-key":
						gap.ScopeKey = value
					case "evidence-path":
						asset.Path = value
					case "evidence-hash":
						asset.SHA256 = model.Digest(value)
					}
				}
				finding.Evidence = []model.AssetRef{asset}
				return ProjectQualityRows([]model.QualityMetric{metric}, []model.Finding{finding}, []Gap{gap}, []model.AssetRef{asset}, DefaultLimits())
			}
			rows, err := project("")
			if err != nil || len(rows) != 3 {
				t.Fatal("valid Unicode text rejected", err)
			}
			if rows[0].Reason == nil || *rows[0].Reason != "CHƯA_ĐO" || rows[0].Threshold == nil || *rows[0].Threshold != "ngưỡng" || rows[1].Code != "CẢNH_BÁO" || rows[1].ScopeKey == nil || *rows[1].ScopeKey != "phạm vi" || rows[2].Code != "THIẾU" {
				t.Fatal("projection changed valid Unicode text")
			}
			for _, invalid := range []string{"X\x00Y", "X\xffY"} {
				if _, err := project(invalid); err == nil {
					t.Fatalf("invalid writer text accepted: %q", invalid)
				}
			}
		})
	}
}

func TestQualityParentsRejectInvalidWriterText(t *testing.T) {
	limits := DefaultLimits()
	for _, kind := range []string{"artifact", "rollup"} {
		for _, field := range []string{"threshold", "report-path", "report-hash", "policy-hash", "outcome", "artifact-hash"} {
			if kind == "rollup" && field == "artifact-hash" {
				continue // Roll-up parents have no artifact hash.
			}
			t.Run(kind+"/"+field, func(t *testing.T) {
				for _, value := range []string{"", "X\x00Y", "X\xffY"} {
					policy := fixturePolicy(t)
					artifactCodec, err := GateReportCodec(policy, limits)
					if err != nil {
						t.Fatal(err)
					}
					rollupCodec, err := GateRollupCodec(policy, limits)
					if err != nil {
						t.Fatal(err)
					}
					artifact := decodedFixture(t, "gate-report-pre-conversion", artifactCodec)
					rollup := decodedFixture(t, "gate-rollup-pre-conversion", rollupCodec)
					if value != "" {
						switch field {
						case "threshold":
							policy.ThresholdVersion = value
							artifact.Artifact.ThresholdVersion = value
							rollup.ThresholdVersion = value
							// Bind the changed policy so a NUL case cannot fail
							// merely because its report uses a stale policy hash.
							if value == "X\x00Y" {
								_, digest, err := PolicyBundleCodec(limits).Encode(policy)
								if err != nil {
									t.Fatal(err)
								}
								artifact.Artifact.PolicyHash = digest
								rollup.PolicyHash = digest
							}
						case "policy-hash":
							artifact.Artifact.PolicyHash = model.Digest(value)
							rollup.PolicyHash = model.Digest(value)
						case "outcome":
							artifact.Artifact.Outcome = model.QualityOutcome(value)
							rollup.Outcome = model.QualityOutcome(value)
						case "artifact-hash":
							artifact.Artifact.ArtifactHash = model.Digest(value)
						}
					}
					var report any = artifact
					if kind == "rollup" {
						report = rollup
					}
					data, err := json.Marshal(report)
					if err != nil {
						t.Fatal(err)
					}
					ref := model.AssetRef{Path: "catalog/report", SHA256: Hash(data), ByteSize: int64(len(data))}
					if value != "" && field == "report-path" {
						ref.Path = value
					}
					if value != "" && field == "report-hash" {
						ref.SHA256 = model.Digest(value)
					}
					var parent QualityParent
					if kind == "artifact" {
						parent, err = ProjectArtifactParent(artifact, ref, policy, limits)
					} else {
						parent, err = ProjectRollupParent(rollup, ref, policy, limits)
					}
					if value != "" {
						if err == nil {
							t.Fatalf("invalid parent text accepted: %q", value)
						}
					} else if err != nil || parent.Report != ref || parent.ThresholdVersion != policy.ThresholdVersion || parent.PolicyHash != artifact.Artifact.PolicyHash || parent.Outcome != artifact.Artifact.Outcome {
						t.Fatal("valid parent projection mismatch", err)
					}
				}
			})
		}
	}
}

func TestWriterSpaceRejectsUnknownVocabulary(t *testing.T) {
	for _, test := range []struct {
		space model.CoordinateSpace
		want  model.ProvenanceSpace
	}{
		{model.OriginalBytes, model.ProvenanceOriginalBytes},
		{model.DecodedMember, model.ProvenanceMemberBytes},
		{model.RawText, model.ProvenanceRawText},
		{model.NormalizedText, model.ProvenanceNormalizedText},
	} {
		if got, err := WriterSpace(test.space); err != nil || got != test.want {
			t.Fatal("valid writer space mapping changed", got, err)
		}
	}
	for _, invalid := range []model.CoordinateSpace{"", "X\x00Y", "X\xffY", "unknown"} {
		if _, err := WriterSpace(invalid); err == nil {
			t.Fatal("invalid writer space accepted")
		}
	}
}
