package quality

import (
	"reflect"
	"strconv"
	"unicode/utf8"

	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/quality/api"
	"danny.vn/banhmi/internal/corpus/quality/formats"
)

// ConversionRefs names the completed conversion's three retained manifests.
// Composition verifies their contents and catalog linkage separately.
type ConversionRefs struct {
	// RequestManifest references the exact retained conversion request.
	RequestManifest model.AssetRef
	// Document references the retained converted document.
	Document model.AssetRef
	// OutputManifest references the retained sandbox output manifest.
	OutputManifest model.AssetRef
}

// Report evaluates the request itself and verifies the result with the pinned
// codec. Output overflow replaces the entire detailed report with REPORT_LIMIT.
func (g *Gate) Report(request api.GateRequest, refs ConversionRefs) (formats.GateReport, error) {
	if !validRefs(&refs) {
		return formats.GateReport{}, errInput
	}
	evaluation, err := g.Evaluate(request)
	if err != nil && err != errLimit {
		return formats.GateReport{}, err
	}
	if !consistentReportAssets(request.Parts.Findings, &refs) {
		return formats.GateReport{}, errInput
	}
	if err == errLimit {
		return g.LimitReport(request.Document.OriginalHash, request.Document.CandidateDigest, &refs)
	}
	report := g.report(request.Document.OriginalHash, request.Document.CandidateDigest, evaluation, &refs)
	if !g.reportFits(report) {
		return g.LimitReport(request.Document.OriginalHash, request.Document.CandidateDigest, &refs)
	}
	return g.checkedReport(report)
}

// MissingReport constructs an unconverted quarantine for CONVERSION_MISSING
// or RESOURCE_LIMIT. It fails if even the minimal report cannot fit.
func (g *Gate) MissingReport(original, candidate model.Digest, reason string) (formats.GateReport, error) {
	if reason != "CONVERSION_MISSING" && reason != "RESOURCE_LIMIT" {
		return formats.GateReport{}, errInput
	}
	return g.minimalReport(original, candidate, reason, nil)
}

// LimitReport constructs an all-unknown REPORT_LIMIT quarantine. Non-nil refs
// preserve a completed conversion; nil refs identify an unconverted artifact.
func (g *Gate) LimitReport(original, candidate model.Digest, refs *ConversionRefs) (formats.GateReport, error) {
	return g.minimalReport(original, candidate, "REPORT_LIMIT", refs)
}

func validRefs(refs *ConversionRefs) bool {
	return refs == nil || (refs.RequestManifest.Validate() == nil && refs.Document.Validate() == nil && refs.OutputManifest.Validate() == nil)
}

// Check contradictions separately from capacity so overflow cannot hide invalid
// evidence by replacing it with a smaller report.
func consistentReportAssets(findings []model.Finding, refs *ConversionRefs) bool {
	paths := make(map[string]model.Digest)
	sizes := make(map[model.Digest]int64)
	add := func(ref model.AssetRef) bool {
		if old, ok := paths[ref.Path]; ok && old != ref.SHA256 {
			return false
		}
		if old, ok := sizes[ref.SHA256]; ok && old != ref.ByteSize {
			return false
		}
		paths[ref.Path], sizes[ref.SHA256] = ref.SHA256, ref.ByteSize
		return true
	}
	if !add(formats.ReportSentinel()) {
		return false
	}
	if refs != nil && (!add(refs.RequestManifest) || !add(refs.Document) || !add(refs.OutputManifest)) {
		return false
	}
	for _, finding := range findings {
		for _, ref := range finding.Evidence {
			if !add(ref) {
				return false
			}
		}
	}
	return true
}

func (g *Gate) minimalReport(original, candidate model.Digest, reason string, refs *ConversionRefs) (formats.GateReport, error) {
	if g == nil || len(g.profile.Metrics) == 0 {
		return formats.GateReport{}, errPolicy
	}
	if original.Validate() != nil || candidate.Validate() != nil || !validRefs(refs) || !consistentReportAssets(nil, refs) {
		return formats.GateReport{}, errInput
	}
	evaluation := api.QualityEvaluation{Metrics: []model.QualityMetric{}, Findings: []model.Finding{}, Outcome: model.QualityQuarantined}
	budget, ok := newEvaluationBudget(g.limits)
	if !ok {
		return formats.GateReport{}, errLimit
	}
	for _, requirement := range g.profile.Metrics {
		metric := unknownMetric(requirement, reason)
		if !budget.reserve(metric) {
			return formats.GateReport{}, errLimit
		}
		evaluation.Metrics = append(evaluation.Metrics, metric)
	}
	finding := artifactFinding(original, reason, model.SeverityBlocking)
	if !budget.reserve(finding) {
		return formats.GateReport{}, errLimit
	}
	evaluation.Findings = append(evaluation.Findings, finding)
	report := g.report(original, candidate, evaluation, refs)
	if !g.reportFits(report) {
		return formats.GateReport{}, errLimit
	}
	return g.checkedReport(report)
}

func (g *Gate) report(original, candidate model.Digest, evaluation api.QualityEvaluation, refs *ConversionRefs) formats.GateReport {
	report := formats.GateReport{
		SchemaVersion: "gate-report-v1",
		Artifact: model.QualityReport{
			ArtifactHash:     original,
			CandidateDigest:  candidate,
			Report:           formats.ReportSentinel(),
			PolicyHash:       g.profile.PolicyHash,
			ThresholdVersion: g.policy.ThresholdVersion,
			Outcome:          evaluation.Outcome,
			Metrics:          evaluation.Metrics,
			Findings:         evaluation.Findings,
		},
	}
	if refs != nil {
		copy := *refs
		report.RequestManifest = &copy.RequestManifest
		report.Document = &copy.Document
		report.OutputManifest = &copy.OutputManifest
	}
	return report
}

func (g *Gate) checkedReport(report formats.GateReport) (formats.GateReport, error) {
	if _, _, err := g.codec.Encode(report); err != nil {
		return formats.GateReport{}, errInput
	}
	return report, nil
}

// resultBudget reserves records, nested entries and exact JSON bytes before
// output allocations. Report overhead is checked separately before encoding.
type resultBudget struct {
	rows, bytes, records int64
	metricSeen           bool
	findingSeen          bool
}

func newResultBudget(limits formats.Limits) resultBudget {
	return resultBudget{rows: limits.MaxRows, bytes: limits.MaxJSONBytes, records: min(limits.MaxRows, 1000)}
}

func newEvaluationBudget(limits formats.Limits) (resultBudget, bool) {
	budget := newResultBudget(limits)
	ok := budget.walk(reflect.ValueOf(api.QualityEvaluation{Metrics: []model.QualityMetric{}, Findings: []model.Finding{}, Outcome: model.QualityQuarantined}))
	return budget, ok
}

func (b *resultBudget) reserve(value any) bool {
	if b.records == 0 || b.rows == 0 {
		return false
	}
	b.records--
	b.rows--
	var seen *bool
	switch value.(type) {
	case model.QualityMetric:
		seen = &b.metricSeen
	case model.Finding:
		seen = &b.findingSeen
	default:
		return false
	}
	if *seen && !b.take(1) {
		return false
	}
	*seen = true
	return b.walk(reflect.ValueOf(value))
}

func (b *resultBudget) reserveExclusions(f model.Finding, exclusions []model.ExcludedRange) bool {
	if !b.reserve(f) || int64(len(exclusions)) > b.rows {
		return false
	}
	b.rows -= int64(len(exclusions))
	for i, exclusion := range exclusions {
		if (i > 0 && !b.take(1)) || !b.walk(reflect.ValueOf(exclusion.Span)) {
			return false
		}
	}
	return true
}

func (b *resultBudget) take(n int64) bool {
	if n > b.bytes {
		return false
	}
	b.bytes -= n
	return true
}

// walk counts the exact rule A encoding without encoding or allocating a copy.
// The closed report types contain only structs, strings, scalars and arrays.
func (b *resultBudget) walk(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return b.take(4)
		}
		return b.walk(v.Elem())
	case reflect.Struct:
		if !b.take(2) {
			return false
		}
		for i := 0; i < v.NumField(); i++ {
			if (i > 0 && !b.take(1)) || !b.quoted(v.Type().Field(i).Name) || !b.take(1) || !b.walk(v.Field(i)) {
				return false
			}
		}
		return true
	case reflect.Slice:
		if v.IsNil() || int64(v.Len()) > b.rows || !b.take(2) {
			return false
		}
		b.rows -= int64(v.Len())
		for i := 0; i < v.Len(); i++ {
			if (i > 0 && !b.take(1)) || !b.walk(v.Index(i)) {
				return false
			}
		}
		return true
	case reflect.String:
		return b.quoted(v.String())
	case reflect.Bool:
		if v.Bool() {
			return b.take(4)
		}
		return b.take(5)
	case reflect.Int, reflect.Int32, reflect.Int64:
		return b.take(int64(len(strconv.FormatInt(v.Int(), 10))))
	}
	return false
}

func (b *resultBudget) quoted(s string) bool {
	if int64(len(s)) > b.bytes || !b.take(2) || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		n := int64(utf8.RuneLen(r))
		switch {
		case r == '"' || r == '\\' || r == '\b' || r == '\f' || r == '\n' || r == '\r' || r == '\t':
			n = 2
		case r < 0x20 || r == '<' || r == '>' || r == '&' || r == '\u2028' || r == '\u2029':
			n = 6
		}
		if !b.take(n) {
			return false
		}
	}
	return true
}

func (g *Gate) reportFits(report formats.GateReport) bool {
	if int64(len(report.Artifact.Metrics)) > min(g.limits.MaxRows, 1000)-int64(len(report.Artifact.Findings)) {
		return false
	}
	b := newResultBudget(g.limits)
	if !b.walk(reflect.ValueOf(report)) {
		return false
	}
	// Root and referenced objects share the codec's aggregate object budget.
	remaining := g.limits.MaxObjectBytes - (g.limits.MaxJSONBytes - b.bytes)
	if remaining < 0 {
		return false
	}
	seen := make(map[model.Digest]bool)
	var walk func(reflect.Value) bool
	walk = func(v reflect.Value) bool {
		if v.Kind() == reflect.Pointer {
			return v.IsNil() || walk(v.Elem())
		}
		if v.CanInterface() {
			if ref, ok := v.Interface().(model.AssetRef); ok {
				if ref == formats.ReportSentinel() || seen[ref.SHA256] {
					return true
				}
				if int64(len(seen)) >= g.limits.MaxBlobs-1 || ref.ByteSize > remaining {
					return false
				}
				seen[ref.SHA256] = true
				remaining -= ref.ByteSize
				return true
			}
		}
		switch v.Kind() {
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				if !walk(v.Field(i)) {
					return false
				}
			}
		case reflect.Slice:
			for i := 0; i < v.Len(); i++ {
				if !walk(v.Index(i)) {
					return false
				}
			}
		}
		return true
	}
	return walk(reflect.ValueOf(report))
}
