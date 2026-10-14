package model

import (
	"fmt"
	"unicode/utf8"
)

// CandidateDigest binds a candidate manifest, not a mutable revision locator.
type CandidateDigest = Digest

// ConvertedDocument is the versioned block interchange, not a text rendering.
// Recipes pin detector, converter image, settings, fonts/models and language.
type ConvertedDocument struct {
	SchemaVersion    string
	Original         CaptureRef
	ObservationKey   string
	ArtifactRole     string
	RecipeHash       Digest
	DetectorHash     Digest
	CandidateDigest  CandidateDigest
	RawBlocks        AssetRef
	NormalizedBlocks AssetRef
	RawText          AssetRef
	NormalizedText   AssetRef
	OffsetMap        AssetRef
	EditLedger       AssetRef
	Blocks           []Block
}

// Validate enforces the trusted frozen block bound before validating structure and provenance.
func (d ConvertedDocument) Validate(maxBlocks int) error {
	if maxBlocks <= 0 || len(d.Blocks) > maxBlocks {
		return fmt.Errorf("block count exceeds frozen bound")
	}
	if d.SchemaVersion == "" || d.ObservationKey == "" || d.ArtifactRole == "" {
		return fmt.Errorf("conversion schema and provenance are required")
	}
	if err := d.Original.Validate(); err != nil {
		return fmt.Errorf("original: %w", err)
	}
	for name, h := range map[string]Digest{"recipe hash": d.RecipeHash, "detector hash": d.DetectorHash, "candidate digest": d.CandidateDigest} {
		if err := h.Validate(); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	for name, a := range map[string]AssetRef{"raw blocks": d.RawBlocks, "normalized blocks": d.NormalizedBlocks, "raw text": d.RawText, "normalized text": d.NormalizedText, "offset map": d.OffsetMap, "edit ledger": d.EditLedger} {
		if err := a.Validate(); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	parents := make(map[string]string, len(d.Blocks))
	for i, b := range d.Blocks {
		if err := b.Validate(); err != nil {
			return fmt.Errorf("block: %w", err)
		}
		if _, exists := parents[b.ID]; exists || (i > 0 && b.Ordinal <= d.Blocks[i-1].Ordinal) {
			return fmt.Errorf("duplicate block or nonascending order")
		}
		parents[b.ID] = b.ParentID
	}
	// Each node becomes visiting once and visited once. Parent lookup is O(1).
	states := make(map[string]uint8, len(parents))
	for id := range parents {
		if states[id] == 2 {
			continue
		}
		path := make([]string, 0)
		for current := id; current != ""; current = parents[current] {
			if _, exists := parents[current]; !exists {
				return fmt.Errorf("unknown block parent")
			}
			if states[current] == 1 {
				return fmt.Errorf("cyclic block parents")
			}
			if states[current] == 2 {
				break
			}
			states[current] = 1
			path = append(path, current)
		}
		for _, current := range path {
			states[current] = 2
		}
	}
	for _, b := range d.Blocks {
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

// Severity names the impact of a machine finding.
type Severity string

const (
	// SeverityInfo marks an informational finding.
	SeverityInfo     Severity = "info"
	// SeverityWarning marks a warning finding.
	SeverityWarning  Severity = "warning"
	// SeverityBlocking prevents acceptance until resolved.
	SeverityBlocking Severity = "blocking"
)

// Valid reports membership in the closed contract vocabulary.
func (s Severity) Valid() bool {
	return s == SeverityInfo || s == SeverityWarning || s == SeverityBlocking
}

// Finding carries machine reasons and exact scopes, never free-form private notes.
type Finding struct {
	ReasonCode string
	Severity   Severity
	Scope      GapKind
	ScopeKey   string
	Spans      []Span
	Evidence   []AssetRef
}

// Validate checks the machine reason, typed scope, severity and evidence.
func (f Finding) Validate() error {
	if f.ReasonCode == "" || !f.Severity.Valid() || !f.Scope.Valid() || f.ScopeKey == "" {
		return fmt.Errorf("finding requires reason, severity and typed scope")
	}
	for _, s := range f.Spans {
		if err := s.Validate(); err != nil {
			return fmt.Errorf("source span: %w", err)
		}
	}
	for _, a := range f.Evidence {
		if err := a.Validate(); err != nil {
			return fmt.Errorf("asset reference: %w", err)
		}
	}
	return nil
}

func validateFindings(findings []Finding) error {
	for _, f := range findings {
		if err := f.Validate(); err != nil {
			return fmt.Errorf("finding: %w", err)
		}
	}
	return nil
}

// MetricState distinguishes measured, unknown and qualified inapplicable checks.
type MetricState string

const (
	// MetricMeasured records a measured quality check.
	MetricMeasured      MetricState = "measured"
	// MetricUnknown records an unresolved quality check.
	MetricUnknown       MetricState = "unknown"
	// MetricNotApplicable records a qualified inapplicable check.
	MetricNotApplicable MetricState = "not_applicable"
)

// Valid reports membership in the closed contract vocabulary.
func (s MetricState) Valid() bool {
	return s == MetricMeasured || s == MetricUnknown || s == MetricNotApplicable
}

// MetricKind names a supported artifact-quality check.
type MetricKind string

const (
	// MetricPageAccounting checks accounted source pages.
	MetricPageAccounting         MetricKind = "page_accounting"
	// MetricTextCoverage checks text coverage.
	MetricTextCoverage           MetricKind = "text_coverage"
	// MetricDiacriticValidity checks diacritic validity.
	MetricDiacriticValidity      MetricKind = "diacritic_validity_rate"
	// MetricCorruption checks text corruption.
	MetricCorruption             MetricKind = "corruption_rate"
	// MetricStructureCompleteness checks structure completeness.
	MetricStructureCompleteness  MetricKind = "structure_completeness"
	// MetricCitationParse checks citation parsing.
	MetricCitationParse          MetricKind = "citation_parse_rate"
	// MetricTableIntegrity checks table integrity.
	MetricTableIntegrity         MetricKind = "table_integrity"
	// MetricMetadataAgreement checks agreement with source metadata.
	MetricMetadataAgreement      MetricKind = "metadata_agreement"
	// MetricProvenanceCompleteness checks provenance completeness.
	MetricProvenanceCompleteness MetricKind = "provenance_completeness"
	// MetricGeometry checks source geometry.
	MetricGeometry               MetricKind = "geometry"
	// MetricFidelity checks source fidelity.
	MetricFidelity               MetricKind = "fidelity"
)

// Valid reports membership in the closed contract vocabulary.
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

// QualityMetric retains a measured result and its exact supporting provenance.
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

// Validate checks metric states, measured counts and qualification provenance.
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
			return fmt.Errorf("asset reference: %w", err)
		}
	}
	for _, s := range m.Exclusions {
		if err := s.Validate(); err != nil {
			return fmt.Errorf("source span: %w", err)
		}
	}
	return nil
}

// QualityOutcome names the artifact admission or further-work outcome.
type QualityOutcome string

const (
	// QualityAccepted records admission without accepted gaps.
	QualityAccepted         QualityOutcome = "accepted"
	// QualityAcceptedWithGaps records admission with authorized gaps.
	QualityAcceptedWithGaps QualityOutcome = "accepted_with_gaps"
	// QualityQuarantined records content held for resolution.
	QualityQuarantined      QualityOutcome = "quarantined"
	// QualityNeedsOCR requests separate non-binding OCR processing.
	QualityNeedsOCR         QualityOutcome = "needs_ocr"
)

// Valid reports membership in the closed contract vocabulary.
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

// Validate requires exactly the checks and mandatory status from the trusted policy profile.
func (r QualityReport) Validate(profile QualityMetricProfile) error {
	if err := profile.Validate(); err != nil {
		return fmt.Errorf("trusted metric profile: %w", err)
	}
	if profile.PolicyHash != r.PolicyHash {
		return fmt.Errorf("metric profile policy does not match report")
	}
	if !r.Outcome.Valid() || r.ThresholdVersion == "" || len(r.Metrics) == 0 {
		return fmt.Errorf("quality report requires outcome, threshold version and metrics")
	}
	for name, d := range map[string]Digest{"artifact hash": r.ArtifactHash, "candidate digest": r.CandidateDigest, "policy hash": r.PolicyHash} {
		if err := d.Validate(); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	if err := r.Report.Validate(); err != nil {
		return fmt.Errorf("report: %w", err)
	}
	accepting := r.Outcome == QualityAccepted || r.Outcome == QualityAcceptedWithGaps
	if r.Outcome == QualityAcceptedWithGaps && r.DecisionRef == "" {
		return fmt.Errorf("named-gap acceptance requires a decision reference")
	}
	expected := make(map[MetricKind]MetricRequirement, len(profile.Metrics))
	for _, requirement := range profile.Metrics {
		expected[requirement.Kind] = requirement
	}
	if len(r.Metrics) != len(expected) {
		return fmt.Errorf("report must contain every expected metric exactly once")
	}
	seen := make(map[MetricKind]bool)
	for _, m := range r.Metrics {
		if err := m.Validate(); err != nil {
			return fmt.Errorf("quality metric: %w", err)
		}
		if seen[m.Kind] {
			return fmt.Errorf("duplicate quality metric")
		}
		seen[m.Kind] = true
		requirement, exists := expected[m.Kind]
		if !exists {
			return fmt.Errorf("unexpected quality metric")
		}
		if m.Mandatory != requirement.Mandatory || m.Threshold != requirement.Threshold {
			return fmt.Errorf("metric mandatory status or threshold differs from trusted profile")
		}
		if m.State == MetricNotApplicable && (!requirement.AllowNotApplicable || len(m.Evidence) == 0) {
			return fmt.Errorf("not-applicable metric requires profile permission and qualification evidence")
		}
		if accepting && requirement.Mandatory && (m.State == MetricUnknown || (m.State == MetricMeasured && !m.Passed)) {
			return fmt.Errorf("failed or unknown mandatory metric cannot pass")
		}
	}
	if err := validateFindings(r.Findings); err != nil {
		return fmt.Errorf("findings: %w", err)
	}
	for _, f := range r.Findings {
		if accepting && f.Severity == SeverityBlocking {
			return fmt.Errorf("blocking finding cannot pass")
		}
	}
	return nil
}

// PartKind names a supported document part or excluded block.
type PartKind string

const (
	// PartBody identifies the document body.
	PartBody          PartKind = "body"
	// PartIssuedText identifies separately issued text.
	PartIssuedText    PartKind = "issued_text"
	// PartAppendix identifies an appendix.
	PartAppendix      PartKind = "appendix"
	// PartForm identifies a form.
	PartForm          PartKind = "form"
	// PartTable identifies a table part.
	PartTable         PartKind = "table"
	// PartAttachment identifies an attachment.
	PartAttachment    PartKind = "attachment"
	// PartExcludedBlock identifies excluded source blocks.
	PartExcludedBlock PartKind = "excluded_block"
)

// Valid reports membership in the closed contract vocabulary.
func (k PartKind) Valid() bool {
	return k == PartBody || k == PartIssuedText || k == PartAppendix || k == PartForm || k == PartTable || k == PartAttachment || k == PartExcludedBlock
}

// ClassifiedRange assigns source blocks to a part namespace with exact spans.
type ClassifiedRange struct {
	Kind         PartKind
	Namespace    string
	ArtifactHash Digest
	BlockIDs     []string
	Spans        []Span
}

// NamespaceBoundary pins a local namespace label and source boundary.
type NamespaceBoundary struct {
	Key       string
	ParentKey string
	Kind      PartKind
	Label     AssetRef
	Boundary  Span
}

// ArtifactExpectation records a required or optional artifact and its source evidence.
type ArtifactExpectation struct {
	ArtifactKey string
	Role        string
	Required    bool
	Evidence    Span
}

// ExcludedRange binds an exclusion reason to an exact source span.
type ExcludedRange struct {
	Span       Span
	ReasonCode string
}

// PartReference retains selectors, target keys and resolution provenance.
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

// Validate checks all six parser outputs and their exact source provenance.
func (p PartResult) Validate() error {
	for _, r := range p.ClassifiedRanges {
		if !r.Kind.Valid() || r.Namespace == "" || len(r.BlockIDs) == 0 || len(r.Spans) == 0 {
			return fmt.Errorf("invalid classified part range")
		}
		if err := r.ArtifactHash.Validate(); err != nil {
			return fmt.Errorf("ArtifactHash: %w", err)
		}
		for _, s := range r.Spans {
			if err := s.Validate(); err != nil {
				return fmt.Errorf("source span: %w", err)
			}
		}
	}
	for _, n := range p.Namespaces {
		if n.Key == "" || n.Key == n.ParentKey || !n.Kind.Valid() {
			return fmt.Errorf("invalid namespace boundary")
		}
		if err := n.Label.Validate(); err != nil {
			return fmt.Errorf("n.Label: %w", err)
		}
		if err := n.Boundary.Validate(); err != nil {
			return fmt.Errorf("n.Boundary: %w", err)
		}
	}
	for _, a := range p.RequiredArtifacts {
		if a.ArtifactKey == "" || a.Role == "" {
			return fmt.Errorf("invalid artifact expectation")
		}
		if err := a.Evidence.Validate(); err != nil {
			return fmt.Errorf("Evidence: %w", err)
		}
	}
	for _, e := range p.Exclusions {
		if e.ReasonCode == "" {
			return fmt.Errorf("exclusion reason is required")
		}
		if err := e.Span.Validate(); err != nil {
			return fmt.Errorf("Span: %w", err)
		}
	}
	for _, r := range p.ReferenceSpans {
		if err := r.Span.Validate(); err != nil {
			return fmt.Errorf("Span: %w", err)
		}
		for _, a := range []AssetRef{r.Selector, r.Resolution} {
			if err := a.Validate(); err != nil {
				return fmt.Errorf("asset reference: %w", err)
			}
		}
	}
	if err := validateFindings(p.Findings); err != nil {
		return fmt.Errorf("findings: %w", err)
	}
	return nil
}

// MappedText references staged computed text, offset maps and edit ledger.
type MappedText struct {
	Text       AssetRef
	OffsetMap  AssetRef
	EditLedger AssetRef
}

// Validate checks the three staged text, offset-map and edit-ledger references.
func (m MappedText) Validate() error {
	for name, a := range map[string]AssetRef{"text": m.Text, "offset map": m.OffsetMap, "edit ledger": m.EditLedger} {
		if err := a.Validate(); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

// BodyTree references a staged tree bound to its exact candidate.
type BodyTree struct {
	Tree          AssetRef
	CandidateHash Digest
}

// Validate checks the staged tree reference and candidate binding.
func (b BodyTree) Validate() error {
	if err := b.Tree.Validate(); err != nil {
		return fmt.Errorf("Tree: %w", err)
	}
	if err := b.CandidateHash.Validate(); err != nil {
		return fmt.Errorf("b.CandidateHash: %w", err)
	}
	return nil
}

// MetricRequirement defines one expected check in a trusted policy profile.
// Not-applicable states require both permission here and qualification evidence.
type MetricRequirement struct {
	Kind               MetricKind
	Mandatory          bool
	AllowNotApplicable bool
	Threshold          string
}

// QualityMetricProfile is resolved and verified by the consumer from PolicyHash.
// It is a trusted local argument, never a report-supplied or transport value.
type QualityMetricProfile struct {
	PolicyHash Digest
	Metrics    []MetricRequirement
}

// Validate checks a complete, unique policy profile without resolving it.
func (p QualityMetricProfile) Validate() error {
	if err := p.PolicyHash.Validate(); err != nil {
		return fmt.Errorf("policy hash: %w", err)
	}
	if len(p.Metrics) == 0 {
		return fmt.Errorf("metric profile requires expected metrics")
	}
	seen := make(map[MetricKind]bool, len(p.Metrics))
	for _, m := range p.Metrics {
		if !m.Kind.Valid() || m.Threshold == "" || seen[m.Kind] {
			return fmt.Errorf("metric profile requires known, unique checks and thresholds")
		}
		seen[m.Kind] = true
	}
	return nil
}

// DecodedNamespace retains native labels as local values for pure structure rules.
type DecodedNamespace struct {
	Key       string
	ParentKey string
	Kind      PartKind
	Label     string
	Boundary  Span
}

// DecodedPartReference supplies parsed selector and resolution values with provenance.
type DecodedPartReference struct {
	Span       Span
	Selector   string
	TargetKeys []string
	Resolution string
}

// DecodedPartResult contains all six decoded parser outputs for pure rules.
// The consumer resolves and verifies namespace labels, selectors and resolutions.
type DecodedPartResult struct {
	ClassifiedRanges  []ClassifiedRange
	Namespaces        []DecodedNamespace
	RequiredArtifacts []ArtifactExpectation
	Exclusions        []ExcludedRange
	ReferenceSpans    []DecodedPartReference
	Findings          []Finding
	Limits            RuleLimits
}

// Validate bounds every collection and decoded value before a pure rule call.
// MaxItems counts top-level and nested collection entries together. MaxBytes
// counts every string, including enum values, hashes and provenance locators.
// Bounds are checked by subtraction before collection traversal or UTF-8 scans.
func (p DecodedPartResult) Validate() error {
	if err := p.Limits.Validate(); err != nil {
		return fmt.Errorf("part limits: %w", err)
	}
	bound := decodedRuleBound{items: p.Limits.MaxItems, bytes: p.Limits.MaxBytes}
	for _, count := range []int{len(p.ClassifiedRanges), len(p.Namespaces), len(p.RequiredArtifacts), len(p.Exclusions), len(p.ReferenceSpans), len(p.Findings)} {
		if err := bound.collection(count); err != nil {
			return err
		}
	}
	for _, r := range p.ClassifiedRanges {
		if err := bound.collection(len(r.BlockIDs)); err != nil {
			return err
		}
		if err := bound.collection(len(r.Spans)); err != nil {
			return err
		}
		if err := bound.strings(string(r.Kind), r.Namespace, string(r.ArtifactHash)); err != nil {
			return err
		}
		for _, id := range r.BlockIDs {
			if err := bound.strings(id); err != nil {
				return err
			}
		}
		for _, s := range r.Spans {
			if err := bound.span(s); err != nil {
				return err
			}
		}
	}
	for _, n := range p.Namespaces {
		if err := bound.strings(n.Key, n.ParentKey, string(n.Kind), n.Label); err != nil {
			return err
		}
		if err := bound.span(n.Boundary); err != nil {
			return err
		}
	}
	for _, a := range p.RequiredArtifacts {
		if err := bound.strings(a.ArtifactKey, a.Role); err != nil {
			return err
		}
		if err := bound.span(a.Evidence); err != nil {
			return err
		}
	}
	for _, e := range p.Exclusions {
		if err := bound.strings(e.ReasonCode); err != nil {
			return err
		}
		if err := bound.span(e.Span); err != nil {
			return err
		}
	}
	for _, r := range p.ReferenceSpans {
		if err := bound.collection(len(r.TargetKeys)); err != nil {
			return err
		}
		if err := bound.strings(r.Selector, r.Resolution); err != nil {
			return err
		}
		for _, key := range r.TargetKeys {
			if err := bound.strings(key); err != nil {
				return err
			}
		}
		if err := bound.span(r.Span); err != nil {
			return err
		}
	}
	for _, f := range p.Findings {
		if err := bound.collection(len(f.Spans)); err != nil {
			return err
		}
		if err := bound.collection(len(f.Evidence)); err != nil {
			return err
		}
		if err := bound.strings(f.ReasonCode, string(f.Severity), string(f.Scope), f.ScopeKey); err != nil {
			return err
		}
		for _, s := range f.Spans {
			if err := bound.span(s); err != nil {
				return err
			}
		}
		for _, a := range f.Evidence {
			if err := bound.strings(a.Path, string(a.SHA256)); err != nil {
				return err
			}
		}
	}
	if err := (PartResult{ClassifiedRanges: p.ClassifiedRanges, RequiredArtifacts: p.RequiredArtifacts, Exclusions: p.Exclusions, Findings: p.Findings}).Validate(); err != nil {
		return fmt.Errorf("decoded part provenance: %w", err)
	}
	for _, n := range p.Namespaces {
		if n.Key == "" || n.Key == n.ParentKey || !n.Kind.Valid() {
			return fmt.Errorf("invalid decoded namespace")
		}
		if err := n.Boundary.Validate(); err != nil {
			return fmt.Errorf("namespace boundary: %w", err)
		}
	}
	for _, r := range p.ReferenceSpans {
		if err := r.Span.Validate(); err != nil {
			return fmt.Errorf("reference span: %w", err)
		}
	}
	return nil
}

type decodedRuleBound struct {
	items int
	bytes int64
}

func (b *decodedRuleBound) collection(count int) error {
	if count > b.items {
		return fmt.Errorf("decoded input exceeds frozen item bound")
	}
	b.items -= count
	return nil
}

func (b *decodedRuleBound) strings(values ...string) error {
	for _, value := range values {
		if int64(len(value)) > b.bytes {
			return fmt.Errorf("decoded input exceeds frozen byte bound")
		}
		b.bytes -= int64(len(value))
		if !utf8.ValidString(value) {
			return fmt.Errorf("decoded input requires valid UTF-8")
		}
	}
	return nil
}

func (b *decodedRuleBound) span(s Span) error {
	return b.strings(string(s.ArtifactHash), s.BlockID, string(s.Space), s.Object, s.Member, s.ReasonCode)
}
