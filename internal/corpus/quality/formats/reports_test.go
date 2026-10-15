package formats

import (
	"reflect"
	"strings"
	"testing"

	"danny.vn/banhmi/internal/corpus/model"
)

func TestCandidateAndRollupBindings(t *testing.T) {
	limits := DefaultLimits()
	policy := fixturePolicy(t)
	input := decodedFixture(t, "candidate-input-v1", CandidateInputCodec(limits))
	output := decodedFixture(t, "candidate-output-v1", CandidateOutputCodec(limits))
	reportCodec, err := GateReportCodec(policy, limits)
	if err != nil {
		t.Fatal(err)
	}
	rollupCodec, err := GateRollupCodec(policy, limits)
	if err != nil {
		t.Fatal(err)
	}
	complete := decodedFixture(t, "gate-report-complete", reportCodec)
	rollup := decodedFixture(t, "gate-rollup-complete", rollupCodec)
	if err := ValidateCandidateBinding(input, output, limits); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRollupBinding(input, &output, []GateReport{complete}, rollup, policy, limits); err != nil {
		t.Fatal(err)
	}
	pre := decodedFixture(t, "gate-report-pre-conversion", reportCodec)
	preRollup := decodedFixture(t, "gate-rollup-pre-conversion", rollupCodec)
	if err := ValidateRollupBinding(input, nil, []GateReport{pre}, preRollup, policy, limits); err != nil {
		t.Fatal(err)
	}
	ocr := decodedFixture(t, "gate-report-needs-ocr", reportCodec)
	ocrRollup := decodedFixture(t, "gate-rollup-needs-ocr", rollupCodec)
	if err := ValidateRollupBinding(input, nil, []GateReport{ocr}, ocrRollup, policy, limits); err != nil {
		t.Fatal(err)
	}
	scope := decodedFixture(t, "gap-scope-v1", GapScopeCodec(limits))
	if err := ValidateScopeBinding(scope, preRollup, input.Artifacts[0].Capture.Asset.SHA256, limits); err != nil {
		t.Fatal(err)
	}
	output.CandidateDigest = model.Digest(strings.Repeat("a", 64))
	if ValidateCandidateBinding(input, output, limits) == nil {
		t.Fatal("accepted substituted candidate output")
	}
	output = decodedFixture(t, "candidate-output-v1", CandidateOutputCodec(limits))
	if err := ValidateRollupBinding(input, &output, []GateReport{complete}, rollup, policy, limits); err != nil {
		t.Fatal("unchanged report binding rejected", err)
	}
	rollup.ArtifactReports[0].Report.ByteSize++
	if ValidateRollupBinding(input, &output, []GateReport{complete}, rollup, policy, limits) == nil {
		t.Fatal("accepted substituted artifact report")
	}
	scope.OutputDigest = model.Digest(strings.Repeat("a", 64))
	if ValidateScopeBinding(scope, preRollup, input.Artifacts[0].Capture.Asset.SHA256, limits) == nil {
		t.Fatal("accepted substituted decision scope")
	}
}

func TestReportPolicyAndSentinel(t *testing.T) {
	limits := DefaultLimits()
	policy := fixturePolicy(t)
	codec, err := GateReportCodec(policy, limits)
	if err != nil {
		t.Fatal(err)
	}
	value := decodedFixture(t, "gate-report-pre-conversion", codec)
	data, digest, err := codec.Encode(value)
	if err != nil {
		t.Fatal(err)
	}
	ref := model.AssetRef{Path: "catalog/report", SHA256: digest, ByteSize: int64(len(data))}
	local, err := ValidateReportReference(value, ref, policy, limits)
	if err != nil || local.Report != ref || value.Artifact.Report != ReportSentinel() {
		t.Fatal("sentinel substitution rewrote bytes or failed", err)
	}
	ref.ByteSize++
	if _, err := ValidateReportReference(value, ref, policy, limits); err == nil {
		t.Fatal("catalog reference size mismatch accepted")
	}
	mutations := map[string]func(*GateReport){
		"sentinel": func(v *GateReport) {
			v.Artifact.Report.Path = "reports/arbitrary"
		},
		"version": func(v *GateReport) {
			v.SchemaVersion = "gate-report-v2"
		},
		"policy": func(v *GateReport) {
			v.Artifact.PolicyHash = model.Digest(strings.Repeat("a", 64))
		},
		"threshold": func(v *GateReport) {
			v.Artifact.ThresholdVersion = "other"
		},
		"accepting": func(v *GateReport) {
			v.Artifact.Outcome = model.QualityAccepted
		},
		"finding": func(v *GateReport) {
			v.Artifact.Findings = []model.Finding{}
		},
		"partial": func(v *GateReport) {
			v.Document = &ref
		},
		"null-array": func(v *GateReport) {
			v.Artifact.Findings = nil
		},
		"unknown-field-count": func(v *GateReport) {
			v.Artifact.Metrics = v.Artifact.Metrics[:1]
		},
		"metric-order": func(v *GateReport) {
			v.Artifact.Metrics[0], v.Artifact.Metrics[1] = v.Artifact.Metrics[1], v.Artifact.Metrics[0]
		},
		"not-applicable": func(v *GateReport) {
			v.Artifact.Metrics[0].State = model.MetricNotApplicable
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			v := decodedFixture(t, "gate-report-pre-conversion", codec)
			mutate(&v)
			if _, _, err := codec.Encode(v); err == nil {
				t.Fatal("invalid report accepted")
			}
		})
	}
	// Mandatory unknown geometry/fidelity stay ineligible after conversion too.
	converted := decodedFixture(t, "gate-report-complete", codec)
	converted.Artifact.Outcome = model.QualityAccepted
	if _, _, err := codec.Encode(converted); err == nil {
		t.Fatal("conversion bypassed mandatory unknown metrics")
	}
}

func TestRollupEligibility(t *testing.T) {
	policy := fixturePolicy(t)
	codec, err := GateRollupCodec(policy, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	for _, outcome := range []model.QualityOutcome{model.QualityAccepted, model.QualityAcceptedWithGaps} {
		value := decodedFixture(t, "gate-rollup-needs-ocr", codec)
		value.Outcome = outcome
		if outcome == model.QualityAcceptedWithGaps {
			value.DecisionRef = "decision-1"
		}
		if _, _, err := codec.Encode(value); err == nil {
			t.Fatal("needs_ocr roll-up became eligible", outcome)
		}
	}
	value := decodedFixture(t, "gate-rollup-needs-ocr", codec)
	value.Outcome = model.QualityQuarantined
	if _, _, err := codec.Encode(value); err == nil {
		t.Fatal("needs_ocr precedence lost")
	}
	value = decodedFixture(t, "gate-rollup-pre-conversion", codec)
	value.MissingOrdinals = []int{0, 0}
	if _, _, err := codec.Encode(value); err == nil {
		t.Fatal("duplicate missing ordinal accepted")
	}
	value = decodedFixture(t, "gate-rollup-pre-conversion", codec)
	value.Gaps[0].ReasonCode = "UNLISTED"
	if _, _, err := codec.Encode(value); err == nil {
		t.Fatal("unlisted gap reason accepted")
	}
	value = decodedFixture(t, "gate-rollup-pre-conversion", codec)
	value.DecisionRef = "decision-1"
	if _, _, err := codec.Encode(value); err == nil {
		t.Fatal("quarantine carried acceptance decision")
	}
}

func TestPolicyProfiles(t *testing.T) {
	policy := fixturePolicy(t)
	codec := PolicyBundleCodec(DefaultLimits())
	for _, mutate := range []func(*PolicyBundle){
		func(v *PolicyBundle) { v.ArtifactProfile[0].Mandatory = false },
		func(v *PolicyBundle) { v.RollupProfile = v.RollupProfile[:1] },
		func(v *PolicyBundle) {
			v.ArtifactProfile[0], v.ArtifactProfile[1] = v.ArtifactProfile[1], v.ArtifactProfile[0]
		},
		func(v *PolicyBundle) { v.GapReasonCodes = []string{"Z", "A"} },
		func(v *PolicyBundle) { v.GapReasonCodes = []string{"A", "A"} },
	} {
		value := fixturePolicy(t)
		mutate(&value)
		if _, _, err := codec.Encode(value); err == nil {
			t.Fatal("invalid policy profile or allowlist accepted")
		}
	}
	// Constructors snapshot requirements rather than retaining mutable policy arrays.
	reportCodec, err := GateReportCodec(policy, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	policy.ArtifactProfile[0].Threshold = "mutated"
	if _, _, err := reportCodec.Decode(fixture(t, "gate-report-complete")); err != nil {
		t.Fatal("caller mutation changed a constructed codec", err)
	}
}

func TestCandidateStructuralFailures(t *testing.T) {
	codec := CandidateInputCodec(DefaultLimits())
	for _, mutate := range []func(*CandidateInput){
		func(v *CandidateInput) { v.Artifacts[0].ObservationOrdinal = -1 },
		func(v *CandidateInput) { v.Artifacts[0].Capture.Country = "sg" },
		func(v *CandidateInput) { v.Artifacts = append(v.Artifacts, v.Artifacts[0]) },
		func(v *CandidateInput) { v.HTMLSelector = "" },
		func(v *CandidateInput) { v.InputDigest = "ABC" },
		func(v *CandidateInput) { v.Artifacts[0].Capture.Asset.Path = "../escape" },
		func(v *CandidateInput) { v.Mode = "reprocess" },
		func(v *CandidateInput) { v.Predecessor = &Predecessor{} },
		func(v *CandidateInput) {
			v.ParserManifest.SHA256 = v.NormalizerManifest.SHA256
		},
		func(v *CandidateInput) {
			v.ParserManifest.Path = v.NormalizerManifest.Path
		},
	} {
		value := decodedFixture(t, "candidate-input-v1", codec)
		mutate(&value)
		if _, _, err := codec.Encode(value); err == nil {
			t.Fatal("invalid candidate accepted")
		}
	}
	input := decodedFixture(t, "candidate-input-v1", codec)
	reprocess := decodedFixture(t, "candidate-input-reprocess", codec)
	reprocess.Predecessor.DocumentVersionKey = "document-previous"
	if _, _, err := codec.Encode(reprocess); err != nil {
		t.Fatal("cross-version predecessor rejected; identity is a consumer binding", err)
	}
	if input.Artifacts[0].ObservationOrdinal == 0 {
		t.Fatal("fixture fails to separate source and candidate ordinals")
	}
	outputCodec := CandidateOutputCodec(DefaultLimits())
	output := decodedFixture(t, "candidate-output-v1", outputCodec)
	output.Artifacts[0].Ordinal = 1
	if _, _, err := outputCodec.Encode(output); err == nil {
		t.Fatal("missing output ordinal accepted")
	}
	output = decodedFixture(t, "candidate-output-v1", outputCodec)
	output.TextHash = model.Digest(strings.Repeat("0", 64))
	if _, _, err := outputCodec.Encode(output); err == nil {
		t.Fatal("incorrect text preimage accepted")
	}
}

func TestProjectionAndPartition(t *testing.T) {
	limits := DefaultLimits()
	output := decodedFixture(t, "candidate-output-v1", CandidateOutputCodec(limits))
	text := output.Artifacts[0].NormalizedText
	span := model.Span{
		ArtifactHash: text.SHA256,
		BlockID:      "document",
		Space:        model.NormalizedText,
		Range:        &model.ByteRange{Start: 0, End: 3},
	}
	retained := []Retained{{Ordinal: 0, Spans: []model.Span{span}}}
	artifacts := []CoverageArtifact{{Ordinal: 0, NormalizedText: &text}}
	if err := ValidatePartition([]Gap{}, retained, artifacts, limits); err != nil {
		t.Fatal(err)
	}
	gap := Gap{Ordinal: 0, Scope: model.GapArtifact, ScopeKey: "artifact-1", ReasonCode: "TEXT_UNAVAILABLE", Spans: []model.Span{span}}
	if ValidatePartition([]Gap{gap}, retained, artifacts, limits) == nil {
		t.Fatal("overlap between retained and excluded accepted")
	}
	if ValidatePartition([]Gap{}, []Retained{{Ordinal: 0, Spans: []model.Span{}}}, artifacts, limits) == nil {
		t.Fatal("uncovered bytes accepted")
	}
	gap.Spans = []model.Span{}
	artifacts[0].NormalizedText = nil
	if err := ValidatePartition([]Gap{gap}, []Retained{{Ordinal: 0, Spans: []model.Span{}}}, artifacts, limits); err != nil {
		t.Fatal("missing text with explicit gap rejected", err)
	}
	if ValidatePartition([]Gap{}, []Retained{{Ordinal: 0, Spans: []model.Span{}}}, artifacts, limits) == nil {
		t.Fatal("missing text without explicit gap accepted")
	}
	if ValidateSpanReferences([]model.Span{span}, []model.AssetRef{}, limits) == nil {
		t.Fatal("span introduced uncataloged object")
	}
	if err := ValidateSpanReferences([]model.Span{span}, []model.AssetRef{text}, limits); err != nil {
		t.Fatal(err)
	}
	// Preserve standalone qualification evidence as well as span references.
	qualification := model.AssetRef{Path: "logical/qualification", SHA256: model.Digest(strings.Repeat("a", 64)), ByteSize: 1}
	catalogText := text
	catalogText.Path = "catalog/text"
	catalogQualification := qualification
	catalogQualification.Path = "catalog/qualification"
	ordinal := 3
	span.BlockID = ""
	span.TokenOrdinal = &ordinal
	finding := model.Finding{ReasonCode: "SYNTHETIC", Severity: model.SeverityWarning, Scope: model.GapArtifact, ScopeKey: "artifact-1", Spans: []model.Span{span}, Evidence: []model.AssetRef{qualification, text, qualification}}
	rows, err := ProjectQualityRows([]model.QualityMetric{}, []model.Finding{finding}, []Gap{}, []model.AssetRef{catalogText, catalogQualification}, limits)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Ordinal != 0 || len(rows[0].Evidence) != 2 || rows[0].Evidence[0] != catalogText || rows[0].Evidence[1] != catalogQualification || rows[0].SourceSpans[0].TokenOrdinal != nil || rows[0].SourceSpans[0].BlockID != "document" || rows[0].MetricKind != nil {
		t.Fatal("quality projection differs from spec")
	}
	if finding.Spans[0].TokenOrdinal == nil || finding.Spans[0].BlockID != "" {
		t.Fatal("projection mutated retained value")
	}
	rows[0].SourceSpans[0].Range.End = 2
	if !reflect.DeepEqual(finding.Spans[0].Range, &model.ByteRange{Start: 0, End: 3}) {
		t.Fatal("projected range aliases retained range")
	}
	if _, err := ProjectQualityRows([]model.QualityMetric{}, []model.Finding{finding}, []Gap{}, []model.AssetRef{catalogText}, limits); err == nil {
		t.Fatal("missing qualification evidence ignored")
	}
	for space, expected := range map[model.CoordinateSpace]model.ProvenanceSpace{
		model.OriginalBytes:  model.ProvenanceOriginalBytes,
		model.DecodedMember:  model.ProvenanceMemberBytes,
		model.RawText:        model.ProvenanceRawText,
		model.NormalizedText: model.ProvenanceNormalizedText,
	} {
		actual, err := WriterSpace(space)
		if err != nil || actual != expected {
			t.Fatal("writer coordinate mapping mismatch", err)
		}
	}
}
