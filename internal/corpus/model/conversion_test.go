package model

import "testing"

func contractReport() QualityReport {
	a := contractAsset()
	return QualityReport{ArtifactHash: a.SHA256, CandidateDigest: a.SHA256, Report: a, PolicyHash: a.SHA256, ThresholdVersion: "1", Outcome: QualityAccepted,
		Metrics: []QualityMetric{{Kind: MetricGeometry, State: MetricMeasured, Numerator: 1, Denominator: 1, Threshold: "1", Mandatory: true, Passed: true}}}
}

func TestMandatoryQualityUnknownCannotPass(t *testing.T) {
	r := contractReport()
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []MetricKind{MetricGeometry, MetricFidelity, MetricProvenanceCompleteness} {
		r := contractReport()
		r.Metrics[0] = QualityMetric{Kind: kind, State: MetricUnknown, Mandatory: true, Threshold: "1", ReasonCode: "UNKNOWN"}
		if r.Validate() == nil {
			t.Fatal("unknown mandatory quality passed")
		}
		r.Outcome = QualityQuarantined
		if err := r.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	r = contractReport()
	r.Outcome = QualityAcceptedWithGaps
	if r.Validate() == nil {
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
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	d.Blocks = []Block{b, b}
	if d.Validate() == nil {
		t.Fatal("duplicate block accepted")
	}
	b.SourceSpans = []Span{{ArtifactHash: a.SHA256, Space: RawText, Range: &ByteRange{Start: 0, End: 2}}}
	d.Blocks = []Block{b}
	if d.Validate() == nil {
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
