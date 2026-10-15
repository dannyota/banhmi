// Package quality implements the pure, conservative baseline artifact gate.
package quality

import (
	"errors"
	"strings"

	"golang.org/x/text/unicode/norm"

	conversionapi "danny.vn/banhmi/internal/corpus/conversion/api"
	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/quality/api"
	"danny.vn/banhmi/internal/corpus/quality/formats"
)

var (
	errInput  = errors.New("GATE_INPUT_INVALID")
	errPolicy = errors.New("GATE_POLICY_INVALID")
	errLimit  = errors.New("GATE_REPORT_LIMIT")
)

// Options pins the country policy, resource limits and optional language seam.
// The baseline rejects every non-nil language provider pending qualification.
type Options struct {
	// Policy pins the country, profiles and report threshold version.
	Policy formats.PolicyBundle
	// Limits caps work independently of the policy's acceptance limits.
	Limits formats.Limits
	// Language must be nil until a qualified provider contract is implemented.
	Language conversionapi.LanguageRules
}

// Gate evaluates bounded local values without I/O or qualification shortcuts.
// Construct it with NewGate; its configuration is private and immutable.
type Gate struct {
	policy  formats.PolicyBundle
	limits  formats.Limits
	profile model.QualityMetricProfile
	codec   formats.Codec[formats.GateReport]
}

var _ api.Gate = (*Gate)(nil)

// NewGate validates and copies the pinned policy and parses both profiles.
func NewGate(options Options) (*Gate, error) {
	if options.Language != nil || options.Limits.Validate() != nil {
		return nil, errPolicy
	}
	data, _, err := formats.PolicyBundleCodec(options.Limits).Encode(options.Policy)
	if err != nil {
		return nil, errPolicy
	}
	policy, _, err := formats.PolicyBundleCodec(options.Limits).Decode(data)
	if err != nil {
		return nil, errPolicy
	}
	limits := formats.Limits{
		MaxBlobs:       min(options.Limits.MaxBlobs, policy.AcceptanceLimits.MaxBlobs),
		MaxRows:        min(options.Limits.MaxRows, policy.AcceptanceLimits.MaxRows),
		MaxJSONBytes:   min(options.Limits.MaxJSONBytes, policy.AcceptanceLimits.MaxJSONBytes, 4194304),
		MaxObjectBytes: min(options.Limits.MaxObjectBytes, policy.AcceptanceLimits.MaxObjectBytes),
		MaxElapsedMS:   min(options.Limits.MaxElapsedMS, policy.AcceptanceLimits.MaxElapsedMS),
	}
	profile, rollup, err := formats.PolicyProfiles(policy, limits)
	if err != nil {
		return nil, errPolicy
	}
	for _, requirements := range [][]model.MetricRequirement{profile.Metrics, rollup.Metrics} {
		for _, requirement := range requirements {
			if _, ok := parseThreshold(requirement.Threshold); !ok {
				return nil, errPolicy
			}
		}
	}
	codec, err := formats.GateReportCodec(policy, limits)
	if err != nil {
		return nil, errPolicy
	}
	return &Gate{policy: policy, limits: limits, profile: profile, codec: codec}, nil
}

func sameString(a, b string) bool {
	return len(a) == len(b) && a == b
}

func (g *Gate) pinned(profile model.QualityMetricProfile) bool {
	if !sameString(string(profile.PolicyHash), string(g.profile.PolicyHash)) || len(profile.Metrics) != len(g.profile.Metrics) {
		return false
	}
	for i, want := range g.profile.Metrics {
		got := profile.Metrics[i]
		if !sameString(string(got.Kind), string(want.Kind)) || !sameString(got.Threshold, want.Threshold) || got.Mandatory != want.Mandatory || got.AllowNotApplicable != want.AllowNotApplicable {
			return false
		}
	}
	return true
}

// Evaluate preserves input and returns only unknown baseline metrics and local
// diagnostics. Mandatory unqualified geometry and fidelity force quarantine.
func (g *Gate) Evaluate(request api.GateRequest) (api.QualityEvaluation, error) {
	zero := api.QualityEvaluation{}
	if g == nil || len(g.profile.Metrics) == 0 {
		return zero, errPolicy
	}
	if !g.pinned(request.Profile) {
		return zero, errPolicy
	}
	for _, limits := range []model.RuleLimits{request.Document.Limits, request.Parts.Limits} {
		if limits.Validate() != nil || int64(limits.MaxItems) > g.limits.MaxRows || limits.MaxBytes > g.limits.MaxJSONBytes {
			return zero, errInput
		}
	}
	if request.Validate() != nil || !validRelations(request) {
		return zero, errInput
	}
	result := api.QualityEvaluation{Metrics: []model.QualityMetric{}, Findings: []model.Finding{}, Outcome: model.QualityQuarantined}
	budget, ok := newEvaluationBudget(g.limits)
	if !ok {
		return zero, errLimit
	}
	add := func(f model.Finding) bool {
		// Reserve the serialized view before copying any producer entries.
		if f.Spans == nil {
			f.Spans = []model.Span{}
		}
		if f.Evidence == nil {
			f.Evidence = []model.AssetRef{}
		}
		if !budget.reserve(f) {
			return false
		}
		result.Findings = append(result.Findings, copyFinding(f))
		return true
	}
	for _, requirement := range g.profile.Metrics {
		metric := unknownMetric(requirement, baselineReason(requirement.Kind))
		if !budget.reserve(metric) {
			return zero, errLimit
		}
		result.Metrics = append(result.Metrics, metric)
		if !add(unknownFinding(request.Document.OriginalHash, requirement)) {
			return zero, errLimit
		}
	}
	var text strings.Builder
	for _, block := range request.Document.Blocks {
		if !textBearing(block.Kind) {
			continue
		}
		for _, signal := range []struct {
			code     string
			severity model.Severity
			on       bool
		}{
			{"TEXT_NOT_NFC", model.SeverityBlocking, !norm.NFC.IsNormalString(block.NormalizedText)},
			{"EMPTY_TEXT_BLOCK", model.SeverityWarning, block.NormalizedText == ""},
		} {
			if signal.on {
				finding := artifactFinding(request.Document.OriginalHash, signal.code, signal.severity)
				finding.Spans = block.SourceSpans
				if !add(finding) {
					return zero, errLimit
				}
			}
		}
		text.WriteString(block.NormalizedText)
	}
	for _, code := range textSignals(text.String()) {
		if !add(artifactFinding(request.Document.OriginalHash, code, model.SeverityWarning)) {
			return zero, errLimit
		}
	}
	if len(request.Parts.Exclusions) > 0 {
		finding := artifactFinding(request.Document.OriginalHash, "EXCLUSIONS_UNSUPPORTED", model.SeverityBlocking)
		// Reserve the nested spans before constructing the output collection.
		if !budget.reserveExclusions(finding, request.Parts.Exclusions) {
			return zero, errLimit
		}
		for _, exclusion := range request.Parts.Exclusions {
			finding.Spans = append(finding.Spans, exclusion.Span)
		}
		result.Findings = append(result.Findings, copyFinding(finding))
	}
	for _, finding := range request.Parts.Findings {
		if !add(finding) {
			return zero, errLimit
		}
	}
	return result, nil
}

func validParents(parents map[string]string) bool {
	states := make(map[string]uint8, len(parents))
	for id := range parents {
		path := []string{}
		for current := id; current != "" && states[current] != 2; current = parents[current] {
			if _, ok := parents[current]; !ok || states[current] == 1 {
				return false
			}
			states[current] = 1
			path = append(path, current)
		}
		for _, current := range path {
			states[current] = 2
		}
	}
	return true
}

// spanAnchor makes optional scalar coordinates comparable without pointer identity.
type spanAnchor struct {
	hash       model.Digest
	block      string
	space      string
	object     string
	member     string
	reason     string
	page       int
	ordinal    int
	hasPage    bool
	hasOrdinal bool
	hasRange   bool
}

func anchor(s model.Span) spanAnchor {
	a := spanAnchor{hash: s.ArtifactHash, block: s.BlockID, space: string(s.Space), object: s.Object, member: s.Member, reason: s.ReasonCode, hasPage: s.Page != nil, hasOrdinal: s.TokenOrdinal != nil, hasRange: s.Range != nil}
	if s.Page != nil {
		a.page = *s.Page
	}
	if s.TokenOrdinal != nil {
		a.ordinal = *s.TokenOrdinal
	}
	return a
}

func validRelations(request api.GateRequest) bool {
	parents := make(map[string]string, len(request.Document.Blocks))
	provenance := make(map[spanAnchor][]model.ByteRange)
	for _, block := range request.Document.Blocks {
		parents[block.ID] = block.ParentID
		for _, span := range block.SourceSpans {
			if (span.BlockID != "" && span.BlockID != block.ID) || (span.Space == model.OriginalBytes && span.ArtifactHash != request.Document.OriginalHash) {
				return false
			}
			a := anchor(span)
			if span.Range != nil {
				provenance[a] = append(provenance[a], *span.Range)
			} else {
				provenance[a] = nil
			}
		}
	}
	if !validParents(parents) {
		return false
	}
	validPartSpan := func(span model.Span) bool {
		if span.Space == model.OriginalBytes && span.ArtifactHash != request.Document.OriginalHash {
			return false
		}
		if span.BlockID != "" {
			_, ok := parents[span.BlockID]
			return ok
		}
		return true
	}
	queries := make(map[spanAnchor][]model.ByteRange)
	known := func(span model.Span) bool {
		a := anchor(span)
		_, ok := provenance[a]
		if !ok {
			return false
		}
		if span.Range == nil {
			return true
		}
		queries[a] = append(queries[a], *span.Range)
		return true
	}
	namespaces := make(map[string]string, len(request.Parts.Namespaces))
	for _, namespace := range request.Parts.Namespaces {
		if _, exists := namespaces[namespace.Key]; exists || !validPartSpan(namespace.Boundary) {
			return false
		}
		namespaces[namespace.Key] = namespace.ParentKey
	}
	if !validParents(namespaces) {
		return false
	}
	owned := make(map[string]bool)
	for _, classified := range request.Parts.ClassifiedRanges {
		if classified.ArtifactHash != request.Document.OriginalHash {
			return false
		}
		if _, ok := namespaces[classified.Namespace]; !ok {
			return false
		}
		members := make(map[string]bool, len(classified.BlockIDs))
		for _, id := range classified.BlockIDs {
			if _, ok := parents[id]; !ok || owned[id] {
				return false
			}
			owned[id] = true
			members[id] = true
		}
		for _, span := range classified.Spans {
			if !validPartSpan(span) || (span.BlockID != "" && !members[span.BlockID]) {
				return false
			}
		}
	}
	for _, exclusion := range request.Parts.Exclusions {
		if !known(exclusion.Span) {
			return false
		}
	}
	for _, reference := range request.Parts.ReferenceSpans {
		if !validPartSpan(reference.Span) {
			return false
		}
		for _, target := range reference.TargetKeys {
			if _, ok := namespaces[target]; !ok {
				return false
			}
		}
	}
	for _, artifact := range request.Parts.RequiredArtifacts {
		if !validPartSpan(artifact.Evidence) {
			return false
		}
	}
	for _, finding := range request.Parts.Findings {
		for _, span := range finding.Spans {
			if !validPartSpan(span) {
				return false
			}
		}
	}
	for a, ranges := range queries {
		if !rangesCovered(provenance[a], ranges) {
			return false
		}
	}
	return true
}

type rangeEvent struct {
	rangeValue model.ByteRange
	query      bool
}

// rangesCovered uses eight fixed-width radix passes and one sweep. Sources
// precede queries with the same start. A query must fit inside one source span,
// so adjacent source intervals never manufacture a larger provenance range.
func rangesCovered(sources, queries []model.ByteRange) bool {
	if len(sources) > int(^uint(0)>>1)-len(queries) {
		return false
	}
	events := make([]rangeEvent, 0, len(sources)+len(queries))
	for _, source := range sources {
		events = append(events, rangeEvent{rangeValue: source})
	}
	for _, query := range queries {
		events = append(events, rangeEvent{rangeValue: query, query: true})
	}
	scratch := make([]rangeEvent, len(events))
	for shift := uint(0); shift < 64; shift += 8 {
		var positions [256]int
		for _, event := range events {
			positions[(uint64(event.rangeValue.Start)>>shift)&255]++
		}
		offset := 0
		for i, count := range positions {
			positions[i] = offset
			offset += count
		}
		for _, event := range events {
			bucket := (uint64(event.rangeValue.Start) >> shift) & 255
			scratch[positions[bucket]] = event
			positions[bucket]++
		}
		events, scratch = scratch, events
	}
	var end int64
	for _, event := range events {
		if event.query {
			if event.rangeValue.End > end {
				return false
			}
		} else {
			end = max(end, event.rangeValue.End)
		}
	}
	return true
}
