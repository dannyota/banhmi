package model

import (
	"fmt"
	"strings"
	"testing"
)

func contractReport() QualityReport {
	a := contractAsset()
	r := QualityReport{ArtifactHash: a.SHA256, CandidateDigest: a.SHA256, Report: a, PolicyHash: a.SHA256, ThresholdVersion: "1", Outcome: QualityAccepted}
	for _, requirement := range contractProfile().Metrics {
		r.Metrics = append(r.Metrics, QualityMetric{Kind: requirement.Kind, State: MetricMeasured, Numerator: 1, Denominator: 1, Threshold: requirement.Threshold, Mandatory: requirement.Mandatory, Passed: true})
	}
	return r
}

func TestMandatoryQualityUnknownCannotPass(t *testing.T) {
	r := contractReport()
	if err := r.Validate(contractProfile()); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []MetricKind{MetricGeometry, MetricFidelity, MetricProvenanceCompleteness} {
		r := contractReport()
		for i := range r.Metrics {
			if r.Metrics[i].Kind == kind {
				r.Metrics[i] = QualityMetric{Kind: kind, State: MetricUnknown, Mandatory: true, Threshold: "1", ReasonCode: "UNKNOWN"}
			}
		}
		if r.Validate(contractProfile()) == nil {
			t.Fatal("unknown mandatory quality passed")
		}
		r.Outcome = QualityQuarantined
		if err := r.Validate(contractProfile()); err != nil {
			t.Fatal(err)
		}
	}
	r = contractReport()
	r.Outcome = QualityAcceptedWithGaps
	if r.Validate(contractProfile()) == nil {
		t.Fatal("gap acceptance omitted decision")
	}
	if PartKind("unknown").Valid() || MetricState("unknown-value").Valid() || MetricKind("unknown").Valid() || QualityOutcome("unknown").Valid() || Severity("unknown").Valid() {
		t.Fatal("accepted unknown conversion vocabulary")
	}
	if err := (PartResult{}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestConvertedDocumentAndPartValidation(t *testing.T) {
	a := contractAsset()
	s := Span{ArtifactHash: a.SHA256, Space: RawText, Range: &ByteRange{Start: 0, End: 1}}
	b := Block{ID: "block-1", Kind: BlockParagraph, RawText: "x", NormalizedText: a, Language: "vi", Authority: AuthorityNonBinding, SourceSpans: []Span{s}}
	d := ConvertedDocument{SchemaVersion: "1", Original: contractCapture(), ObservationKey: "observation", ArtifactRole: "body", RecipeHash: a.SHA256, DetectorHash: a.SHA256, CandidateDigest: a.SHA256,
		RawBlocks: a, NormalizedBlocks: a, RawText: a, NormalizedText: a, OffsetMap: a, EditLedger: a, Blocks: []Block{b}}
	if err := d.Validate(100); err != nil {
		t.Fatal(err)
	}
	d.Blocks = []Block{b, b}
	if d.Validate(100) == nil {
		t.Fatal("duplicate block accepted")
	}
	b.SourceSpans = []Span{{ArtifactHash: a.SHA256, Space: RawText, Range: &ByteRange{Start: 0, End: 2}}}
	d.Blocks = []Block{b}
	if d.Validate(100) == nil {
		t.Fatal("out-of-bounds artifact span accepted")
	}
	p := PartResult{ClassifiedRanges: []ClassifiedRange{{Kind: PartBody, Namespace: "body", ArtifactHash: a.SHA256, BlockIDs: []string{"block-1"}, Spans: []Span{s}}},
		Namespaces: []NamespaceBoundary{{Key: "body", Kind: PartBody, Label: a, Boundary: s}},
		RequiredArtifacts: []ArtifactExpectation{{ArtifactKey: "attachment", Role: "appendix", Required: true, Evidence: s}},
		Exclusions: []ExcludedRange{{Span: s, ReasonCode: "SIGNATURE"}},
		ReferenceSpans: []PartReference{{Span: s, Selector: a, Resolution: a}},
		Findings: []Finding{{ReasonCode: "UNRESOLVED", Severity: SeverityWarning, Scope: GapPart, ScopeKey: "part"}}}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	p.Exclusions[0].ReasonCode = ""
	if p.Validate() == nil {
		t.Fatal("exclusion without reason accepted")
	}
}

func contractProfile() QualityMetricProfile {
	p := QualityMetricProfile{PolicyHash: contractAsset().SHA256}
	for _, kind := range []MetricKind{MetricPageAccounting, MetricTextCoverage, MetricDiacriticValidity, MetricCorruption, MetricStructureCompleteness, MetricCitationParse, MetricTableIntegrity, MetricMetadataAgreement, MetricProvenanceCompleteness, MetricGeometry, MetricFidelity} {
		p.Metrics = append(p.Metrics, MetricRequirement{Kind: kind, Mandatory: true, Threshold: "1"})
	}
	return p
}

func TestQualityProfileCompletenessAndMandatoryStatus(t *testing.T) {
	profile := contractProfile()
	for i, requirement := range profile.Metrics {
		t.Run(string(requirement.Kind), func(t *testing.T) {
			r := contractReport()
			r.Metrics = append(r.Metrics[:i:i], r.Metrics[i+1:]...)
			if r.Validate(profile) == nil {
				t.Fatal("omitted required check passed")
			}
			r = contractReport()
			r.Metrics[i].Mandatory = false
			if r.Validate(profile) == nil {
				t.Fatal("disabled mandatory flag passed")
			}
			r = contractReport()
			r.Metrics[i].Passed = false
			if r.Validate(profile) == nil {
				t.Fatal("failed required check passed")
			}
		})
	}
	r := contractReport()
	r.Metrics[0] = r.Metrics[1]
	if r.Validate(profile) == nil {
		t.Fatal("duplicate substituted for missing metric")
	}
	r = contractReport()
	r.Metrics[0].Kind = "unknown-check"
	if r.Validate(profile) == nil {
		t.Fatal("unexpected metric passed")
	}
	r = contractReport()
	profile.PolicyHash = Digest(strings.Repeat("b", 64))
	if r.Validate(profile) == nil {
		t.Fatal("unbound trusted profile passed")
	}
}

func TestQualityProfileQualifiedNotApplicable(t *testing.T) {
	r, profile := contractReport(), contractProfile()
	r.Metrics[0] = QualityMetric{Kind: MetricPageAccounting, State: MetricNotApplicable, Mandatory: true, Threshold: "1", ReasonCode: "NATIVE_ELEMENT_INVENTORY", Evidence: []AssetRef{contractAsset()}}
	if r.Validate(profile) == nil {
		t.Fatal("unpermitted not-applicable passed")
	}
	profile.Metrics[0].AllowNotApplicable = true
	if err := r.Validate(profile); err != nil {
		t.Fatal(err)
	}
	r.Metrics[0].Evidence = nil
	if r.Validate(profile) == nil {
		t.Fatal("unqualified not-applicable passed")
	}
}

func chainDocument(count int) ConvertedDocument {
	a := contractAsset()
	d := ConvertedDocument{SchemaVersion: "1", Original: contractCapture(), ObservationKey: "observation", ArtifactRole: "body", RecipeHash: a.SHA256, DetectorHash: a.SHA256, CandidateDigest: a.SHA256,
		RawBlocks: a, NormalizedBlocks: a, RawText: a, NormalizedText: a, OffsetMap: a, EditLedger: a}
	for i := 0; i < count; i++ {
		parent := ""
		if i+1 < count {
			parent = fmt.Sprintf("block-%d", i+1)
		}
		d.Blocks = append(d.Blocks, Block{ID: fmt.Sprintf("block-%d", i), ParentID: parent, Ordinal: i, Kind: BlockParagraph, RawText: "x", NormalizedText: a, Language: "vi", Authority: AuthorityNonBinding,
			SourceSpans: []Span{{ArtifactHash: a.SHA256, Space: RawText, Range: &ByteRange{Start: 0, End: 1}}}})
	}
	return d
}

func TestConvertedDocumentLongChainAndLateCycle(t *testing.T) {
	const count = 20000
	d := chainDocument(count)
	if err := d.Validate(count); err != nil {
		t.Fatal(err)
	}
	d.Blocks[count-1].ParentID = d.Blocks[count-2].ID
	if d.Validate(count) == nil {
		t.Fatal("late cycle accepted")
	}
	d.Blocks[count-1].ParentID = "missing"
	if d.Validate(count) == nil {
		t.Fatal("unknown parent accepted")
	}
	if err := d.Validate(count-1); err == nil || !strings.Contains(err.Error(), "block count") {
		t.Fatal("frozen bound not enforced before traversal")
	}
	if d.Validate(0) == nil {
		t.Fatal("unbounded block validation accepted")
	}
}

func decodedPartFixture() DecodedPartResult {
	a := contractAsset()
	s := Span{ArtifactHash: a.SHA256, Space: RawText, Range: &ByteRange{End: 1}}
	return DecodedPartResult{ClassifiedRanges: []ClassifiedRange{{Kind: PartBody, Namespace: "body", ArtifactHash: a.SHA256, BlockIDs: []string{"block"}, Spans: []Span{s}}}, Limits: RuleLimits{MaxItems: 3, MaxBytes: 1000}}
}

func TestDecodedPartNestedItemBounds(t *testing.T) {
	if err := decodedPartFixture().Validate(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*DecodedPartResult)
	}{
		{"block IDs", func(p *DecodedPartResult) { p.ClassifiedRanges[0].BlockIDs = []string{"a", "b", "c", "d"} }},
		{"spans", func(p *DecodedPartResult) {
			s := p.ClassifiedRanges[0].Spans[0]
			p.ClassifiedRanges[0].Spans = []Span{s, s, s, s}
		}},
		{"aggregate", func(p *DecodedPartResult) { p.Limits.MaxItems = 2 }},
		{"finding spans", func(p *DecodedPartResult) {
			p.ClassifiedRanges = nil
			p.Findings = []Finding{{Spans: make([]Span, 4)}}
		}},
		{"finding evidence", func(p *DecodedPartResult) {
			p.ClassifiedRanges = nil
			p.Findings = []Finding{{Evidence: make([]AssetRef, 4)}}
		}},
		{"reference targets", func(p *DecodedPartResult) {
			p.ClassifiedRanges = nil
			p.ReferenceSpans = []DecodedPartReference{{TargetKeys: []string{"a", "b", "c", "d"}}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := decodedPartFixture()
			test.mutate(&p)
			if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "item bound") {
				t.Fatalf("nested items were not bounded before validation: %v", err)
			}
		})
	}
	p := decodedPartFixture()
	p.Limits = RuleLimits{MaxItems: 1, MaxBytes: 1}
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "item bound") {
		t.Fatalf("tiny limits accepted nested collections: %v", err)
	}
}

func TestDecodedPartNestedByteBounds(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*DecodedPartResult)
	}{
		{"namespace", func(p *DecodedPartResult) { p.ClassifiedRanges[0].Namespace = strings.Repeat("x", 1001) }},
		{"block ID", func(p *DecodedPartResult) { p.ClassifiedRanges[0].BlockIDs[0] = strings.Repeat("x", 1001) }},
		{"span member", func(p *DecodedPartResult) { p.ClassifiedRanges[0].Spans[0].Member = strings.Repeat("x", 1001) }},
		{"span object", func(p *DecodedPartResult) { p.ClassifiedRanges[0].Spans[0].Object = strings.Repeat("x", 1001) }},
		{"namespace key", func(p *DecodedPartResult) { p.Namespaces = []DecodedNamespace{{Key: strings.Repeat("x", 1001)}} }},
		{"artifact key", func(p *DecodedPartResult) { p.RequiredArtifacts = []ArtifactExpectation{{ArtifactKey: strings.Repeat("x", 1001)}} }},
		{"exclusion reason", func(p *DecodedPartResult) { p.Exclusions = []ExcludedRange{{ReasonCode: strings.Repeat("x", 1001)}} }},
		{"finding evidence path", func(p *DecodedPartResult) { p.Findings = []Finding{{Evidence: []AssetRef{{Path: strings.Repeat("x", 1001)}}}} }},
		{"reference selector", func(p *DecodedPartResult) { p.ReferenceSpans = []DecodedPartReference{{Selector: strings.Repeat("x", 1001)}} }},
		{"tiny limits", func(p *DecodedPartResult) {
			p.ClassifiedRanges[0].BlockIDs = nil
			p.ClassifiedRanges[0].Spans = nil
			p.Limits = RuleLimits{MaxItems: 1, MaxBytes: 1}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := decodedPartFixture()
			p.Limits.MaxItems = 100
			test.mutate(&p)
			if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "byte bound") {
				t.Fatalf("nested bytes were not bounded before validation: %v", err)
			}
		})
	}
	// The exact aggregate byte allowance passes; one fewer byte fails.
	p := decodedPartFixture()
	s := p.ClassifiedRanges[0].Spans[0]
	p.Limits.MaxBytes = int64(len(PartBody) + len("body") + len(contractAsset().SHA256) + len("block") + len(s.ArtifactHash) + len(s.Space))
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	p.Limits.MaxBytes--
	if p.Validate() == nil {
		t.Fatal("aggregate byte bound ignored")
	}
}
