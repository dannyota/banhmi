package residency

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"danny.vn/banhmi/internal/corpus/conversion"
	"danny.vn/banhmi/internal/corpus/model"
)

// ResolvedObject supplies exact bytes under a logical immutable reference.
// Evaluate never treats Ref.Path as a filesystem path.
type ResolvedObject struct {
	Ref   model.AssetRef
	Bytes []byte
}

// Engine inspects authenticated bounded byte closures without external I/O.
type Engine struct {
	policy ResidencyPolicy
	limits ResidencyLimits
	rules  []*regexp.Regexp
	// Only same-package test compilation can install the synthetic oracle.
	// Its input is the entire canonical request, including all retained bindings.
	proof func([]byte, model.Digest) bool
}

// NewEngine copies the frozen policy and trusted limits. Production proof is nil.
func NewEngine(policy ResidencyPolicy, limits ResidencyLimits) (*Engine, error) {
	if _, _, err := PolicyCodec(limits).Encode(policy); err != nil {
		return nil, err
	}
	if _, err := ReportCodec(policy, limits); err != nil {
		return nil, err
	}
	policy.Rules = append([]DetectorRule(nil), policy.Rules...)
	e := &Engine{policy: policy, limits: stricter(limits, policy.Limits)}
	for _, rule := range policy.Rules {
		r, err := regexp.Compile(rule.Pattern)
		if err != nil {
			return nil, policyError
		}
		e.rules = append(e.rules, r)
	}
	return e, nil
}

// Evaluate authenticates the complete closure before inspecting any views.
// It returns a zero report on every error and never grants production admission.
func (e *Engine) Evaluate(ctx context.Context, request ScreenRequest, objects []ResolvedObject) (ResidencyReport, error) {
	var zero ResidencyReport
	if e == nil || ctx == nil {
		return zero, inputError
	}
	if err := contextError(ctx); err != nil {
		return zero, err
	}
	if request.Budget.Validate() != nil {
		return zero, inputError
	}
	deadline := time.Now().Add(min(request.Budget.MaxElapsed, time.Duration(e.limits.MaxElapsedMS)*time.Millisecond))
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	if err := contextError(ctx); err != nil {
		return zero, err
	}
	requestBytes, digest, err := RequestCodec(e.limits).Encode(request)
	if err != nil {
		return zero, err
	}
	policyBytes, _, err := PolicyCodec(e.limits).Encode(e.policy)
	if err != nil {
		return zero, err
	}
	if request.Policy != hashRef("policies", policyBytes) {
		return zero, bindingError
	}
	closure, err := resolveClosure(ctx, request, requestBytes, policyBytes, objects, e.limits)
	if err != nil {
		return zero, err
	}
	i := inspection{ctx: ctx, limits: e.limits, items: min(e.limits.MaxItems, int64(request.Budget.MaxItems)), bytes: e.limits.MaxDecodedBytes,
		counts: make([]DetectorCount, len(e.policy.Rules)), rules: e.rules}
	for n, rule := range e.policy.Rules {
		i.counts[n].ID = rule.ID
	}
	subject := closure[request.Subject.SHA256]
	complete, unsupported := false, false
	if request.SubjectKind == "outbound_http" {
		unsupported, err = i.httpViews(subject)
		complete = !unsupported
	} else {
		media := textMedia(request.MediaType)
		if media == "text/plain" || media == "text/html" {
			err = i.view(string(subject))
			complete = media == "text/plain"
		}
	}
	if err != nil {
		return zero, err
	}
	for _, evidence := range request.Evidence {
		if err := i.view(string(closure[evidence.Asset.SHA256])); err != nil {
			return zero, err
		}
	}
	if err := contextError(ctx); err != nil {
		return zero, err
	}
	evidenceBytes, _ := json.Marshal(request.Evidence)
	report := ResidencyReport{SchemaVersion: "residency-report-v1", Request: hashRef("requests", requestBytes),
		Subject: ByteIdentity{request.Subject.SHA256, request.Subject.ByteSize}, Policy: request.Policy,
		Country: request.Country, SourceID: request.SourceID, OwnerKey: request.OwnerKey, Operation: request.Operation,
		RuleVersion: e.policy.RuleVersion, EvidenceHash: hash(evidenceBytes), Complete: complete,
		Class: model.ResidencyUnclassified, ReasonCode: "NO_POSITIVE_PROOF", Counts: i.counts}
	positive := false
	switch {
	case unsupported:
		report.Complete, report.ReasonCode = false, "UNSUPPORTED_ENCODING"
		for n := range report.Counts {
			report.Counts[n].Matches = 0
		}
	case i.matches > 0:
		report.Class, report.ReasonCode = model.ResidencyPersonal, "SIGNAL_PRESENT"
	case !complete:
		report.ReasonCode = "INCOMPLETE_SURFACE"
	case e.proof != nil && e.proof(append([]byte(nil), requestBytes...), digest):
		positive = true
		proof := proofDigest(digest)
		report.Class, report.ReasonCode, report.AdmissionProof = model.ResidencyPublic, "SYNTHETIC_PROOF", &proof
	}
	if err := validateReportBinding(request, report, e.policy, positive); err != nil {
		return zero, err
	}
	codec, err := reportCodec(e.policy, e.limits, positive)
	if err != nil {
		return zero, err
	}
	if _, _, err := codec.Encode(report); err != nil {
		return zero, err
	}
	if err := contextError(ctx); err != nil {
		return zero, err
	}
	return report, nil
}

func contextError(ctx context.Context) error {
	switch ctx.Err() {
	case context.DeadlineExceeded:
		return deadlineError
	case context.Canceled:
		return cancelledError
	default:
		return nil
	}
}

func validConversionQuality(q conversionQuality) bool {
	if q.Outcome != model.QualityQuarantined || len(q.Metrics) != 2 {
		return false
	}
	for n, m := range q.Metrics {
		kind := []model.MetricKind{model.MetricGeometry, model.MetricFidelity}[n]
		if m.Validate() != nil || m.Kind != kind || m.State != model.MetricUnknown || m.Passed || !m.Mandatory || m.Threshold != "qualification-required" || m.ReasonCode != "BASELINE_UNQUALIFIED" || m.Evidence != nil || m.Exclusions != nil {
			return false
		}
	}
	for _, f := range q.Findings {
		if f.Validate() != nil {
			return false
		}
	}
	return true
}

func validConversionManifest(m conversion.ConversionManifest) bool {
	return model.ValidateEnvelopeKey(m.RequestKey, "convert") == nil && m.Request.Artifact.Validate() == nil && m.Request.Recipe.Validate() == nil && m.Request.Budget.Validate() == nil && m.DetectorHash.Validate() == nil && m.CandidateDigest.Validate() == nil && m.ObservationKey != "" && m.ArtifactRole != "" && m.Language != "" && m.OutputBytes > 0 && m.OutputBytes <= math.MaxInt64/2 && m.Request.Budget.MaxInputBytes <= math.MaxInt64/2 && m.Request.Artifact.Asset.Path != "request.json" && m.Request.Recipe.Path != "request.json" && m.Request.Artifact.Asset.Path != m.Request.Recipe.Path
}

type inspection struct {
	ctx     context.Context
	limits  ResidencyLimits
	items   int64
	bytes   int64
	matches int64
	counts  []DetectorCount
	rules   []*regexp.Regexp
}

func (i *inspection) view(text string) error {
	if err := contextError(i.ctx); err != nil {
		return err
	}
	if i.items == 0 || int64(len(text)) > i.bytes {
		return limitError
	}
	i.items--
	i.bytes -= int64(len(text))
	if !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return inputError
	}
	for n, rule := range i.rules {
		if err := contextError(i.ctx); err != nil {
			return err
		}
		// Enumerate non-overlapping RE2 candidates once. Rejected boundaries do
		// not restart the search inside a candidate. The next candidate begins
		// at the previous candidate's end, exactly as FindAllStringIndex does.
		for at := 0; at < len(text); {
			if err := contextError(i.ctx); err != nil {
				return err
			}
			match := rule.FindStringIndex(text[at:])
			if match == nil {
				break
			}
			start, end := at+match[0], at+match[1]
			if end <= at {
				return policyError
			}
			at = end
			if !matchBoundary(text, start, end, i.counts[n].ID == "email") {
				continue
			}
			if i.matches == i.limits.MaxMatches {
				return limitError
			}
			i.matches++
			i.counts[n].Matches++
		}
	}
	return nil
}

func matchBoundary(text string, start, end int, email bool) bool {
	word := func(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r) }
	if start > 0 {
		r, _ := utf8.DecodeLastRuneInString(text[:start])
		if word(r) || (email && r < 128 && strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789.!#$%&'*+/=?^_`{|}~-", r)) {
			return false
		}
	}
	if end < len(text) {
		r, _ := utf8.DecodeRuneInString(text[end:])
		if word(r) || (email && r < 128 && strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789.-", r)) {
			return false
		}
	}
	return true
}

func asciiLower(s string) string {
	b := []byte(s)
	for n, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[n] += 'a' - 'A'
		}
	}
	return string(b)
}

func textMedia(value string) string {
	parts := strings.Split(value, ";")
	if len(parts) > 2 {
		return ""
	}
	if len(parts) == 2 {
		name, _, ok := strings.Cut(parts[1], "=")
		if !ok || asciiLower(strings.Trim(name, " \t")) != "charset" {
			return ""
		}
	}
	media, params, err := mime.ParseMediaType(value)
	if err != nil || len(params) > 1 {
		return ""
	}
	for name, value := range params {
		if asciiLower(name) != "charset" || asciiLower(value) != "utf-8" {
			return ""
		}
	}
	return asciiLower(media)
}

func residualEscape(s string) bool {
	hex := func(c byte) bool { return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }
	for n := 0; n+2 < len(s); n++ {
		if s[n] == '%' && hex(s[n+1]) && hex(s[n+2]) {
			return true
		}
	}
	return false
}

func httpToken(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", c)) {
			return false
		}
	}
	return true
}

// httpViews checks wire details before net/http can discard or normalize them.
func (i *inspection) httpViews(data []byte) (bool, error) {
	headerEnd := bytes.Index(data, []byte("\r\n\r\n"))
	if headerEnd < 0 {
		return false, inputError
	}
	if int64(bytes.Count(data[:headerEnd], []byte("\r\n"))) > i.items {
		return false, limitError
	}
	lines := strings.Split(string(data[:headerEnd]), "\r\n")
	first := strings.Split(lines[0], " ")
	if len(first) != 3 || !httpToken(first[0]) || first[2] != "HTTP/1.1" {
		return false, inputError
	}
	headers := make(map[string][]string)
	for _, line := range lines[1:] {
		name, value, ok := strings.Cut(line, ":")
		if !ok || !httpToken(name) || strings.ContainsAny(line, "\r\n") {
			return false, inputError
		}
		for _, c := range value {
			if c < 0x20 && c != '\t' || c == 0x7f {
				return false, inputError
			}
		}
		name = asciiLower(name)
		if int64(len(headers[name])) >= i.items {
			return false, limitError
		}
		headers[name] = append(headers[name], strings.Trim(value, " \t"))
	}
	if len(headers["host"]) != 1 || len(headers["content-length"]) > 1 || len(headers["transfer-encoding"]) > 1 || len(headers["trailer"]) != 0 || (len(headers["content-length"]) != 0 && len(headers["transfer-encoding"]) != 0) {
		return false, inputError
	}
	unsupportedTransfer := len(headers["transfer-encoding"]) != 0 && asciiLower(headers["transfer-encoding"][0]) != "chunked"
	if unsupportedTransfer {
		target, err := url.ParseRequestURI(first[1])
		if err != nil || target.User != nil || strings.Contains(first[1], "#") {
			return false, inputError
		}
	}
	reader := bufio.NewReader(bytes.NewReader(data))
	request, err := http.ReadRequest(reader)
	if err != nil {
		if unsupportedTransfer {
			return true, nil
		}
		return false, inputError
	}
	defer request.Body.Close()
	body, err := io.ReadAll(io.LimitReader(request.Body, i.limits.MaxSubjectBytes+1))
	if err != nil || len(request.Trailer) != 0 {
		return false, inputError
	}
	if int64(len(body)) > i.limits.MaxSubjectBytes {
		return false, limitError
	}
	if _, err := reader.ReadByte(); err != io.EOF {
		return false, inputError
	}
	if request.URL.User != nil || request.URL.Fragment != "" || strings.Contains(first[1], "#") {
		return false, inputError
	}
	if first[0] == "CONNECT" || !strings.HasPrefix(first[1], "/") || strings.HasPrefix(first[1], "//") {
		return true, nil
	}
	if ce := headers["content-encoding"]; len(ce) > 1 || len(ce) == 1 && asciiLower(ce[0]) != "identity" {
		return true, nil
	}
	path, query, hasQuery := strings.Cut(first[1], "?")
	decoded, err := url.PathUnescape(path)
	if err != nil {
		return false, inputError
	}
	if hasQuery {
		q, err := url.QueryUnescape(query)
		if err != nil {
			return false, inputError
		}
		decoded += "?" + q
	}
	if !utf8.ValidString(decoded) {
		return false, inputError
	}
	if residualEscape(decoded) {
		return true, nil
	}
	media := textMedia(request.Header.Get("Content-Type"))
	if len(body) > 0 && (len(headers["content-type"]) != 1 || media != "text/plain" && media != "application/json" && media != "application/x-www-form-urlencoded") {
		return true, nil
	}
	var form string
	if len(body) > 0 && media == "application/x-www-form-urlencoded" {
		form, err = url.QueryUnescape(string(body))
		if err != nil || !utf8.ValidString(form) {
			return false, inputError
		}
		if residualEscape(form) {
			return true, nil
		}
	}
	for _, view := range []string{first[0], first[1], decoded, request.Host} {
		if err := i.view(view); err != nil {
			return false, err
		}
	}
	names := make([]string, 0, len(headers))
	for name := range headers {
		if name != "host" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if err := i.view(name); err != nil {
			return false, err
		}
		for _, value := range headers[name] {
			if err := i.view(value); err != nil {
				return false, err
			}
		}
	}
	if len(body) == 0 {
		return false, nil
	}
	if err := i.view(string(body)); err != nil {
		return false, err
	}
	switch media {
	case "application/x-www-form-urlencoded":
		if err := i.view(form); err != nil {
			return false, err
		}
	case "application/json":
		if err := inspectJSON(body, i.limits.MaxItems, i.limits.MaxDecodedBytes, true, i.view); err != nil {
			return false, err
		}
	}
	return false, nil
}
