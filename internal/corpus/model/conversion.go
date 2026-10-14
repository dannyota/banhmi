package model

import "fmt"

// CandidateDigest binds a candidate manifest, not a mutable revision locator.
type CandidateDigest = Digest

// ConvertedDocument is the versioned block interchange, not a text rendering.
// Recipes pin detector, converter image, settings, fonts/models and language.
type ConvertedDocument struct {
	SchemaVersion   string
	Original        CaptureRef
	ObservationKey  string
	ArtifactRole    string
	RecipeHash      Digest
	DetectorHash    Digest
	CandidateDigest CandidateDigest
	RawBlocks       AssetRef
	NormalizedBlocks AssetRef
	RawText         AssetRef
	NormalizedText  AssetRef
	OffsetMap       AssetRef
	EditLedger      AssetRef
	Blocks          []Block
}

func (d ConvertedDocument) Validate() error {
	if d.SchemaVersion == "" || d.ObservationKey == "" || d.ArtifactRole == "" {
		return fmt.Errorf("conversion schema and provenance are required")
	}
	if err := d.Original.Validate(); err != nil {
		return err
	}
	for _, h := range []Digest{d.RecipeHash, d.DetectorHash, d.CandidateDigest} {
		if err := h.Validate(); err != nil {
			return err
		}
	}
	for _, a := range []AssetRef{d.RawBlocks, d.NormalizedBlocks, d.RawText, d.NormalizedText, d.OffsetMap, d.EditLedger} {
		if err := a.Validate(); err != nil {
			return err
		}
	}
	seen := make(map[string]bool)
	for i, b := range d.Blocks {
		if err := b.Validate(); err != nil {
			return err
		}
		if seen[b.ID] || (i > 0 && b.Ordinal <= d.Blocks[i-1].Ordinal) {
			return fmt.Errorf("duplicate block or nonascending order")
		}
		seen[b.ID] = true
	}
	for _, b := range d.Blocks {
		if b.ParentID != "" && !seen[b.ParentID] {
			return fmt.Errorf("unknown block parent")
		}
		parents := make(map[string]bool)
		parent := b.ParentID
		for parent != "" {
			if !seen[parent] {
				return fmt.Errorf("unknown block ancestor")
			}
			if parents[parent] {
				return fmt.Errorf("cyclic block parents")
			}
			parents[parent] = true
			for _, candidate := range d.Blocks {
				if candidate.ID == parent {
					parent = candidate.ParentID
					break
				}
			}
		}
		for _, s := range b.SourceSpans {
			for _, a := range []AssetRef{d.Original.Asset, d.RawText, d.NormalizedText} {
				if s.ArtifactHash == a.SHA256 && s.Range != nil && s.Range.End > a.ByteSize {
					return fmt.Errorf("span exceeds referenced artifact bytes")
				}
			}
		}
	}
	return nil
}

type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityBlocking Severity = "blocking"
)

func (s Severity) Valid() bool { return s == SeverityInfo || s == SeverityWarning || s == SeverityBlocking }

// Finding carries machine reasons and exact scopes, never free-form private notes.
type Finding struct {
	ReasonCode string
	Severity   Severity
	Scope      GapKind
	ScopeKey   string
	Spans      []Span
	Evidence   []AssetRef
}

func (f Finding) Validate() error {
	if f.ReasonCode == "" || !f.Severity.Valid() || !f.Scope.Valid() || f.ScopeKey == "" {
		return fmt.Errorf("finding requires reason, severity and typed scope")
	}
	for _, s := range f.Spans {
		if err := s.Validate(); err != nil {
			return err
		}
	}
	for _, a := range f.Evidence {
		if err := a.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func validateFindings(findings []Finding) error {
	for _, f := range findings {
		if err := f.Validate(); err != nil {
			return err
		}
	}
	return nil
}

type MetricState string

const (
	MetricMeasured      MetricState = "measured"
	MetricUnknown       MetricState = "unknown"
	MetricNotApplicable MetricState = "not_applicable"
)

func (s MetricState) Valid() bool { return s == MetricMeasured || s == MetricUnknown || s == MetricNotApplicable }

type MetricKind string

const (
	MetricPageAccounting        MetricKind = "page_accounting"
	MetricTextCoverage          MetricKind = "text_coverage"
	MetricDiacriticValidity     MetricKind = "diacritic_validity_rate"
	MetricCorruption            MetricKind = "corruption_rate"
	MetricStructureCompleteness MetricKind = "structure_completeness"
	MetricCitationParse         MetricKind = "citation_parse_rate"
	MetricTableIntegrity        MetricKind = "table_integrity"
	MetricMetadataAgreement     MetricKind = "metadata_agreement"
	MetricProvenanceCompleteness MetricKind = "provenance_completeness"
	MetricGeometry              MetricKind = "geometry"
	MetricFidelity              MetricKind = "fidelity"
)

func (k MetricKind) Valid() bool {
	switch k {
	case MetricPageAccounting, MetricTextCoverage, MetricDiacriticValidity, MetricCorruption,
		MetricStructureCompleteness, MetricCitationParse, MetricTableIntegrity, MetricMetadataAgreement,
		MetricProvenanceCompleteness, MetricGeometry, MetricFidelity:
		return true
	default:
		return false
	}
}

type QualityMetric struct {
	Kind        MetricKind
	State       MetricState
	Numerator   int64
	Denominator int64
	Threshold   string
	Mandatory   bool
	Passed      bool
	ReasonCode  string
	Evidence    []AssetRef
	Exclusions  []Span
}

func (m QualityMetric) Validate() error {
	if !m.Kind.Valid() || !m.State.Valid() || m.Numerator < 0 || m.Denominator < 0 || m.Threshold == "" {
		return fmt.Errorf("invalid quality metric")
	}
	if m.State == MetricMeasured && m.Denominator == 0 {
		return fmt.Errorf("measured metric requires a denominator")
	}
	if m.State != MetricMeasured && (m.ReasonCode == "" || m.Passed || m.Numerator != 0 || m.Denominator != 0) {
		return fmt.Errorf("unknown or inapplicable metric requires reason and no score/pass")
	}
	for _, a := range m.Evidence {
		if err := a.Validate(); err != nil {
			return err
		}
	}
	for _, s := range m.Exclusions {
		if err := s.Validate(); err != nil {
			return err
		}
	}
	return nil
}

type QualityOutcome string

const (
	QualityAccepted         QualityOutcome = "accepted"
	QualityAcceptedWithGaps QualityOutcome = "accepted_with_gaps"
	QualityQuarantined      QualityOutcome = "quarantined"
	QualityNeedsOCR         QualityOutcome = "needs_ocr"
)

func (o QualityOutcome) Valid() bool {
	return o == QualityAccepted || o == QualityAcceptedWithGaps || o == QualityQuarantined || o == QualityNeedsOCR
}

// QualityReport is per artifact; needs_ocr requests work and never acceptance.
type QualityReport struct {
	ArtifactHash     Digest
	CandidateDigest  CandidateDigest
	Report           AssetRef
	PolicyHash       Digest
	ThresholdVersion string
	Outcome          QualityOutcome
	Metrics          []QualityMetric
	Findings         []Finding
	DecisionRef      string
}

func (r QualityReport) Validate() error {
	if !r.Outcome.Valid() || r.ThresholdVersion == "" || len(r.Metrics) == 0 {
		return fmt.Errorf("quality report requires outcome, threshold version and metrics")
	}
	for _, d := range []Digest{r.ArtifactHash, r.CandidateDigest, r.PolicyHash} {
		if err := d.Validate(); err != nil {
			return err
		}
	}
	if err := r.Report.Validate(); err != nil {
		return err
	}
	accepting := r.Outcome == QualityAccepted || r.Outcome == QualityAcceptedWithGaps
	if r.Outcome == QualityAcceptedWithGaps && r.DecisionRef == "" {
		return fmt.Errorf("named-gap acceptance requires a decision reference")
	}
	seen := make(map[MetricKind]bool)
	for _, m := range r.Metrics {
		if err := m.Validate(); err != nil {
			return err
		}
		if seen[m.Kind] {
			return fmt.Errorf("duplicate quality metric")
		}
		seen[m.Kind] = true
		mandatory := m.Mandatory || m.Kind == MetricGeometry || m.Kind == MetricFidelity
		if accepting && mandatory && (m.State == MetricUnknown || (m.State == MetricMeasured && !m.Passed)) {
			return fmt.Errorf("failed or unknown mandatory metric cannot pass")
		}
	}
	if err := validateFindings(r.Findings); err != nil {
		return err
	}
	for _, f := range r.Findings {
		if accepting && f.Severity == SeverityBlocking {
			return fmt.Errorf("blocking finding cannot pass")
		}
	}
	return nil
}

type PartKind string

const (
	PartBody          PartKind = "body"
	PartIssuedText    PartKind = "issued_text"
	PartAppendix      PartKind = "appendix"
	PartForm          PartKind = "form"
	PartTable         PartKind = "table"
	PartAttachment    PartKind = "attachment"
	PartExcludedBlock PartKind = "excluded_block"
)

func (k PartKind) Valid() bool {
	return k == PartBody || k == PartIssuedText || k == PartAppendix || k == PartForm || k == PartTable || k == PartAttachment || k == PartExcludedBlock
}

type ClassifiedRange struct {
	Kind        PartKind
	Namespace   string
	ArtifactHash Digest
	BlockIDs    []string
	Spans       []Span
}

type NamespaceBoundary struct {
	Key       string
	ParentKey string
	Kind      PartKind
	Label     AssetRef
	Boundary  Span
}

type ArtifactExpectation struct {
	ArtifactKey string
	Role        string
	Required    bool
	Evidence    Span
}

type ExcludedRange struct {
	Span       Span
	ReasonCode string
}

type PartReference struct {
	Span       Span
	Selector   AssetRef
	TargetKeys []string
	Resolution AssetRef
}

// PartResult retains all six parser outputs before final block ownership.
// Native labels and parsed selectors remain hash-bound, not rewritten labels.
type PartResult struct {
	ClassifiedRanges  []ClassifiedRange
	Namespaces        []NamespaceBoundary
	RequiredArtifacts []ArtifactExpectation
	Exclusions        []ExcludedRange
	ReferenceSpans    []PartReference
	Findings          []Finding
}

func (p PartResult) Validate() error {
	for _, r := range p.ClassifiedRanges {
		if !r.Kind.Valid() || r.Namespace == "" || len(r.BlockIDs) == 0 || len(r.Spans) == 0 {
			return fmt.Errorf("invalid classified part range")
		}
		if err := r.ArtifactHash.Validate(); err != nil {
			return err
		}
		for _, s := range r.Spans {
			if err := s.Validate(); err != nil {
				return err
			}
		}
	}
	for _, n := range p.Namespaces {
		if n.Key == "" || n.Key == n.ParentKey || !n.Kind.Valid() {
			return fmt.Errorf("invalid namespace boundary")
		}
		if err := n.Label.Validate(); err != nil {
			return err
		}
		if err := n.Boundary.Validate(); err != nil {
			return err
		}
	}
	for _, a := range p.RequiredArtifacts {
		if a.ArtifactKey == "" || a.Role == "" {
			return fmt.Errorf("invalid artifact expectation")
		}
		if err := a.Evidence.Validate(); err != nil {
			return err
		}
	}
	for _, e := range p.Exclusions {
		if e.ReasonCode == "" {
			return fmt.Errorf("exclusion reason is required")
		}
		if err := e.Span.Validate(); err != nil {
			return err
		}
	}
	for _, r := range p.ReferenceSpans {
		if err := r.Span.Validate(); err != nil {
			return err
		}
		for _, a := range []AssetRef{r.Selector, r.Resolution} {
			if err := a.Validate(); err != nil {
				return err
			}
		}
	}
	return validateFindings(p.Findings)
}

type MappedText struct {
	Text       AssetRef
	OffsetMap  AssetRef
	EditLedger AssetRef
}

func (m MappedText) Validate() error {
	for _, a := range []AssetRef{m.Text, m.OffsetMap, m.EditLedger} {
		if err := a.Validate(); err != nil {
			return err
		}
	}
	return nil
}

type BodyTree struct {
	Tree          AssetRef
	CandidateHash Digest
}

func (b BodyTree) Validate() error {
	if err := b.Tree.Validate(); err != nil {
		return err
	}
	return b.CandidateHash.Validate()
}
