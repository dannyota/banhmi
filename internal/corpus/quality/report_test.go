package quality

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/quality/api"
	"danny.vn/banhmi/internal/corpus/quality/formats"
)

func TestGateMinimalReportGoldenAndCatalogHandoff(t *testing.T) {
	g, request, _ := fixtureGate(t)
	report, err := g.MissingReport(request.Document.OriginalHash, request.Document.CandidateDigest, "CONVERSION_MISSING")
	if err != nil {
		t.Fatal("missing report failed")
	}
	data, digest, err := g.codec.Encode(report)
	if err != nil {
		t.Fatal("missing report codec failed")
	}
	want, err := os.ReadFile("testdata/missing-report.json")
	if err != nil || string(data) != strings.TrimSuffix(string(want), "\n") {
		t.Fatal("minimal report golden bytes mismatch")
	}
	decoded, secondDigest, err := g.codec.Decode(data)
	if err != nil || secondDigest != digest || !reflect.DeepEqual(report, decoded) {
		t.Fatal("minimal report round trip failed")
	}
	ref := model.AssetRef{Path: "reports/" + string(digest), SHA256: digest, ByteSize: int64(len(data))}
	local, err := formats.ValidateReportReference(report, ref, g.policy, g.limits)
	if err != nil || (api.GateResult{Report: local, Outcome: local.Outcome}).Validate(g.profile) != nil {
		t.Fatal("catalog handoff failed")
	}
	if report.Artifact.Report != formats.ReportSentinel() || local.Report != ref {
		t.Fatal("retained sentinel was rewritten")
	}
	ref.ByteSize++
	if _, err := formats.ValidateReportReference(report, ref, g.policy, g.limits); err == nil {
		t.Fatal("incorrect catalog size accepted")
	}
}

func TestGateMinimalReportsAndInvalidReferences(t *testing.T) {
	g, request, refs := fixtureGate(t)
	for _, reason := range []string{"CONVERSION_MISSING", "RESOURCE_LIMIT"} {
		report, err := g.MissingReport(request.Document.OriginalHash, request.Document.CandidateDigest, reason)
		if err != nil || report.RequestManifest != nil || report.Document != nil || report.OutputManifest != nil || len(report.Artifact.Findings) != 1 || report.Artifact.Findings[0].ReasonCode != reason {
			t.Fatal("missing conversion contract failed")
		}
		for _, metric := range report.Artifact.Metrics {
			if metric.ReasonCode != reason || metric.State != model.MetricUnknown || metric.Passed {
				t.Fatal("minimal metric contract failed")
			}
		}
	}
	for _, completed := range []*ConversionRefs{nil, &refs} {
		report, err := g.LimitReport(request.Document.OriginalHash, request.Document.CandidateDigest, completed)
		if err != nil || report.Artifact.Findings[0].ReasonCode != "REPORT_LIMIT" || (report.Document == nil) != (completed == nil) {
			t.Fatal("limit report conversion binding failed")
		}
	}
	if _, err := g.MissingReport(request.Document.OriginalHash, request.Document.CandidateDigest, "OTHER"); err != errInput {
		t.Fatal("unsupported missing reason accepted")
	}
	if _, err := g.LimitReport("invalid", request.Document.CandidateDigest, nil); err != errInput {
		t.Fatal("invalid digest repaired")
	}
	refs.Document.Path = "../invalid"
	if _, err := g.Report(request, refs); err != errInput {
		t.Fatal("invalid conversion reference repaired")
	}
	refs.Document.Path = refs.RequestManifest.Path
	if _, err := g.Report(request, refs); err != errInput {
		t.Fatal("codec conflict fell back to a limit report")
	}
}

func TestMetricLimitsExactReportAndMinimalFailure(t *testing.T) {
	g, request, refs := fixtureGate(t)
	report, err := g.Report(request, refs)
	if err != nil {
		t.Fatal("baseline report failed")
	}
	data, _, err := g.codec.Encode(report)
	if err != nil {
		t.Fatal("baseline report encoding failed")
	}
	g.limits.MaxJSONBytes = int64(len(data))
	if !g.reportFits(report) {
		t.Fatal("exact report byte limit rejected")
	}
	g.limits.MaxJSONBytes--
	if g.reportFits(report) {
		t.Fatal("report byte limit plus one accepted")
	}
	// The constructor and codec remain pinned. Only this private test budget is
	// lowered to isolate the pre-encoding failure path from policy encoding.
	g.limits.MaxJSONBytes = 1
	minimal, err := g.LimitReport(request.Document.OriginalHash, request.Document.CandidateDigest, &refs)
	if err != errLimit || !reflect.DeepEqual(minimal, formats.GateReport{}) {
		t.Fatal("minimal-report failure did not return zero report")
	}
	g.limits.MaxJSONBytes = 4194304
	g.limits.MaxRows = 3
	if _, err := g.LimitReport(request.Document.OriginalHash, request.Document.CandidateDigest, nil); err != nil {
		t.Fatal("exact minimal row limit rejected")
	}
	g.limits.MaxRows = 2
	if _, err := g.LimitReport(request.Document.OriginalHash, request.Document.CandidateDigest, nil); err != errLimit {
		t.Fatal("minimal row limit plus one accepted")
	}
}

func TestMetricLimitsReportObjectsAndBlobs(t *testing.T) {
	g, request, refs := fixtureGate(t)
	report, err := g.Report(request, refs)
	if err != nil {
		t.Fatal("object-bound fixture rejected")
	}
	data, _, err := g.codec.Encode(report)
	if err != nil {
		t.Fatal("object-bound fixture encoding failed")
	}
	g.limits.MaxObjectBytes = int64(len(data)) + 3
	if !g.reportFits(report) {
		t.Fatal("exact object byte limit rejected")
	}
	g.limits.MaxObjectBytes--
	if g.reportFits(report) {
		t.Fatal("object byte limit plus one accepted")
	}
	g.limits.MaxObjectBytes = 67108864
	g.limits.MaxBlobs = 4
	if !g.reportFits(report) {
		t.Fatal("exact root and dependency count rejected")
	}
	g.limits.MaxBlobs = 3
	if g.reportFits(report) {
		t.Fatal("blob limit plus one accepted")
	}
	if got, err := g.LimitReport(request.Document.OriginalHash, request.Document.CandidateDigest, &refs); err != errLimit || !reflect.DeepEqual(got, formats.GateReport{}) {
		t.Fatal("minimal report exceeded dependency count")
	}
}

func TestMetricLimitsDetailedOverflowReplaced(t *testing.T) {
	g, request, refs := fixtureGate(t)
	request.Parts.Findings = make([]model.Finding, 997)
	for i := range request.Parts.Findings {
		request.Parts.Findings[i] = artifactFinding(request.Document.OriginalHash, "PRODUCER", model.SeverityWarning)
	}
	report, err := g.Report(request, refs)
	if err != nil || len(report.Artifact.Findings) != 1 || report.Artifact.Findings[0].ReasonCode != "REPORT_LIMIT" || report.Document == nil {
		t.Fatal("detailed overflow was not fully replaced")
	}
	if request.Parts.Findings[0].ReasonCode != "PRODUCER" {
		t.Fatal("overflow changed producer input")
	}
	request.Parts.Findings[0].Evidence = []model.AssetRef{refs.Document}
	request.Parts.Findings[0].Evidence[0].Path = refs.RequestManifest.Path
	if got, err := g.Report(request, refs); err != errInput || !reflect.DeepEqual(got, formats.GateReport{}) {
		t.Fatal("overflow concealed contradictory evidence")
	}
}

func TestMetricLimitsRecordCeiling(t *testing.T) {
	g, request, refs := fixtureGate(t)
	for _, count := range []int{996, 997} {
		request.Parts.Findings = make([]model.Finding, count)
		for i := range request.Parts.Findings {
			request.Parts.Findings[i] = artifactFinding(request.Document.OriginalHash, "PRODUCER", model.SeverityWarning)
		}
		report, err := g.Report(request, refs)
		if err != nil {
			t.Fatal("record ceiling report failed")
		}
		if count == 996 && len(report.Artifact.Metrics)+len(report.Artifact.Findings) != 1000 {
			t.Fatal("exact record ceiling rejected")
		}
		if count == 997 && report.Artifact.Findings[0].ReasonCode != "REPORT_LIMIT" {
			t.Fatal("record ceiling plus one accepted")
		}
	}
}

func TestProvenanceRetainedAndProjected(t *testing.T) {
	g, request, refs := fixtureGate(t)
	page, ordinal := 2, 17
	span := request.Document.Blocks[0].SourceSpans[0]
	span.Space = model.DecodedMember
	span.ArtifactHash = model.Digest(strings.Repeat("f", 64))
	span.Member, span.Object = "people/Jane.Doe/document.xml", "paragraph/one"
	span.Page, span.TokenOrdinal = &page, &ordinal
	request.Document.Blocks[0].SourceSpans = []model.Span{span}
	request.Document.Blocks[0].NormalizedText = "a\u0301"
	producer := artifactFinding(request.Document.OriginalHash, "PRODUCER", model.SeverityWarning)
	producer.ScopeKey = "Jane.Doe"
	producer.Evidence = []model.AssetRef{{Path: "people/Jane.Doe", SHA256: model.Digest(strings.Repeat("1", 64)), ByteSize: 4}}
	request.Parts.Findings = []model.Finding{producer}
	report, err := g.Report(request, refs)
	if err != nil || !reflect.DeepEqual(report.Artifact.Findings[2].Spans, []model.Span{span}) || !reflect.DeepEqual(report.Artifact.Findings[len(report.Artifact.Findings)-1], producer) {
		t.Fatal("local provenance preservation failed")
	}
	data, _, err := g.codec.Encode(report)
	if err != nil {
		t.Fatal("local provenance encoding failed")
	}
	decoded, _, err := g.codec.Decode(data)
	if err != nil || !reflect.DeepEqual(decoded.Artifact.Findings[2].Spans[0], span) {
		t.Fatal("local provenance round trip failed")
	}
	asset := model.AssetRef{Path: "objects/member", SHA256: span.ArtifactHash, ByteSize: 300}
	projected, err := formats.ProjectReportSpan(span, asset)
	if err != nil || projected.TokenOrdinal != nil {
		t.Fatal("projection did not remove token ordinal")
	}
	want := span
	want.TokenOrdinal = nil
	if !reflect.DeepEqual(projected, want) || report.Artifact.Findings[2].Spans[0].TokenOrdinal == nil {
		t.Fatal("projection changed retained provenance")
	}
	rows, err := formats.ProjectQualityRows(report.Artifact.Metrics, report.Artifact.Findings, []formats.Gap{}, []model.AssetRef{asset, producer.Evidence[0]}, g.limits)
	if err != nil || !reflect.DeepEqual(rows[4].SourceSpans, []model.Span{want}) {
		t.Fatal("codec-backed writer projection failed")
	}
	// No outward publisher is owned by 2A. The only transport supplied by the
	// frozen seam accepts opaque references and rejects these local labels.
	for _, label := range []string{span.Member, producer.ScopeKey, producer.Evidence[0].Path} {
		envelope := api.ResidencyRequestEnvelope{Key: label, Manifest: request.Document.OriginalHash}
		encoded, err := envelope.MarshalJSON()
		if err == nil || len(encoded) != 0 || strings.Contains(err.Error(), label) {
			t.Fatal("local evidence escaped through reference envelope")
		}
	}
}

func TestMetricLimitsMinimalFailureWithPinnedConstructor(t *testing.T) {
	base, request, _ := fixtureGate(t)
	data, _, err := formats.PolicyBundleCodec(base.limits).Encode(base.policy)
	if err != nil {
		t.Fatal("policy size unavailable")
	}
	limits := base.limits
	limits.MaxJSONBytes = int64(len(data))
	g, err := NewGate(Options{Policy: base.policy, Limits: limits})
	if err != nil {
		t.Fatal("exact policy byte bound rejected")
	}
	got, err := g.MissingReport(request.Document.OriginalHash, request.Document.CandidateDigest, "CONVERSION_MISSING")
	if err != errLimit || !reflect.DeepEqual(got, formats.GateReport{}) {
		t.Fatal("minimal report exceeded pinned constructor bound")
	}
}

func TestMetricLimitsJSONPreflightMatchesCodec(t *testing.T) {
	g, request, refs := fixtureGate(t)
	for _, label := range []string{"plain", "<>&\"\\\n\t\r", "á\u2028\u2029", "\x00\b\f"} {
		finding := artifactFinding(request.Document.OriginalHash, "PRODUCER", model.SeverityInfo)
		finding.ScopeKey = label
		request.Parts.Findings = []model.Finding{finding}
		report, err := g.Report(request, refs)
		if err != nil {
			t.Fatal("JSON counting fixture rejected")
		}
		data, _, err := g.codec.Encode(report)
		if err != nil {
			t.Fatal("JSON counting codec rejected fixture")
		}
		budget := newResultBudget(g.limits)
		if !budget.walk(reflect.ValueOf(report)) || g.limits.MaxJSONBytes-budget.bytes != int64(len(data)) {
			t.Fatal("JSON preflight differs from canonical codec bytes")
		}
	}
}

func TestDiagnosticsGoldenReportBytes(t *testing.T) {
	g, request, refs := fixtureGate(t)
	request.Document.Blocks[0].NormalizedText = "Đang cập nhật file đính kèm"
	report, err := g.Report(request, refs)
	if err != nil {
		t.Fatal("diagnostic golden report failed")
	}
	data, _, err := g.codec.Encode(report)
	if err != nil {
		t.Fatal("diagnostic golden encoding failed")
	}
	want, err := os.ReadFile("testdata/diagnostic-report.json")
	if err != nil || string(data) != strings.TrimSuffix(string(want), "\n") {
		t.Fatal("diagnostic golden bytes mismatch")
	}
	if _, _, err := g.codec.Decode(data); err != nil {
		t.Fatal("diagnostic golden round trip failed")
	}
}
