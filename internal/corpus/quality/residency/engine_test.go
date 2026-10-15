package residency

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/quality/api"
)

func testPolicy() ResidencyPolicy {
	return ResidencyPolicy{"residency-policy-v1", "vn", "residency-signals-v1", "closed", ceiling, frozenRules()}
}

const baseHTTP = "GET / HTTP/1.1\r\nHost: example.invalid\r\n\r\n"

func testRequest(t *testing.T, data []byte, media string, outbound bool) (ScreenRequest, []ResolvedObject) {
	t.Helper()
	p, _, err := PolicyCodec(ceiling).Encode(testPolicy())
	if err != nil {
		t.Fatal("fixture policy setup")
	}
	r := ScreenRequest{SchemaVersion: "screen-request-v1", Country: "vn", SourceID: "fixture",
		OwnerKey: "execution-" + strings.Repeat("1", 32), Operation: "artifact_export", SubjectKind: "captured_artifact",
		Subject: hashRef("objects", data), MediaType: media, Policy: hashRef("policies", p),
		Budget: model.ResourceBudget{MaxItems: 4096, MaxInputBytes: 8388608, MaxPages: 1, MaxElapsed: 5 * time.Second, MaxConcurrent: 1}, Evidence: []ScreenEvidence{}}
	objects := []ResolvedObject{{r.Subject, data}}
	if outbound {
		r.Operation, r.SubjectKind, r.MediaType = "capture_export", "outbound_http", "application/http"
		r.Subject = hashRef("subjects", data)
		objects[0].Ref = r.Subject
	} else {
		receipt := []byte("{}")
		r.Capture = &api.ResidencyRequest{Artifact: model.CaptureRef{Country: "vn", SourceID: "fixture",
			CaptureKey: "capture-" + strings.Repeat("2", 32), Asset: r.Subject, MediaType: media,
			RequestHash: hash([]byte(baseHTTP)), ResponseHash: hash(data), TransportReceipt: hashRef("objects", receipt)}, Policy: r.Policy, Budget: r.Budget}
		if r.Subject.SHA256 != hash(receipt) {
			objects = append(objects, ResolvedObject{r.Capture.Artifact.TransportReceipt, receipt})
		}
	}
	sort.Slice(objects, func(i, j int) bool { return objects[i].Ref.SHA256 < objects[j].Ref.SHA256 })
	return r, objects
}

func testEngine(t *testing.T) *Engine {
	t.Helper()
	e, err := NewEngine(testPolicy(), ceiling)
	if err != nil {
		t.Fatal("engine setup")
	}
	return e
}

// installProof is compiled only into this package's test binary. It pins the
// entire authored request before Evaluate, including subject, evidence, policy,
// operation and owner. Production has no assignment to Engine.proof.
func installProof(t *testing.T, e *Engine, expected ScreenRequest) {
	t.Helper()
	data, digest, err := RequestCodec(ceiling).Encode(expected)
	if err != nil {
		t.Fatal("proof setup")
	}
	e.proof = func(actual []byte, actualDigest model.Digest) bool {
		return actualDigest == digest && bytes.Equal(actual, data) && hash(actual) == digest
	}
}

func requireCounts(t *testing.T, id string, report ResidencyReport, expected map[string]int64) {
	t.Helper()
	rules := frozenRules()
	if len(report.Counts) != len(rules) {
		t.Fatalf("%s local-count-shape", id)
	}
	for n, count := range report.Counts {
		if count.ID != rules[n].ID || count.Matches != expected[count.ID] {
			t.Fatalf("%s local-count", id)
		}
	}
}

func TestResidencyClassification(t *testing.T) {
	cases := []struct {
		id       string
		text     string
		media    string
		complete bool
		reason   string
		counts   map[string]int64
	}{
		{"id12", "000000000001", "text/plain", true, "SIGNAL_PRESENT", map[string]int64{"id12": 1}},
		{"id9", "000000001", "text/plain", true, "SIGNAL_PRESENT", map[string]int64{"id9": 1}},
		{"email", "a@example.invalid", "text/plain", true, "SIGNAL_PRESENT", map[string]int64{"email": 1}},
		{"phone", "0900000000", "text/plain", true, "SIGNAL_PRESENT", map[string]int64{"phone": 1}},
		{"tax", "MST: 0000000000", "text/plain", true, "SIGNAL_PRESENT", map[string]int64{"phone": 1, "tax": 1}},
		{"tax-native", "mã số thuế: 0000000000", "text/plain", true, "SIGNAL_PRESENT", map[string]int64{"phone": 1, "tax": 1}},
		{"names", "Nguyễn Văn Mẫu", "text/plain", true, "NO_POSITIVE_PROOF", nil},
		{"empty", "", "text/plain", true, "NO_POSITIVE_PROOF", nil},
		{"case-media", "abc", "TEXT/PLAIN; CHARSET=UTF-8", true, "NO_POSITIVE_PROOF", nil},
		{"html", "<p>abc</p>", "text/html", false, "INCOMPLETE_SURFACE", nil},
		{"html-attribute", `<p title="a@example.invalid">`, "text/html", false, "SIGNAL_PRESENT", map[string]int64{"email": 1}},
		{"html-comment", "<!-- a@example.invalid -->", "text/html", false, "SIGNAL_PRESENT", map[string]int64{"email": 1}},
		{"entity", "a&#64;example.invalid", "text/html", false, "INCOMPLETE_SURFACE", nil},
		{"opaque", "a@example.invalid", "image/png", false, "INCOMPLETE_SURFACE", nil},
		{"scan", "abc", "application/pdf", false, "INCOMPLETE_SURFACE", nil},
		{"charset", "abc", "text/plain; charset=windows-1258", false, "INCOMPLETE_SURFACE", nil},
		{"ascii-digits", "０９００００００００", "text/plain", true, "NO_POSITIVE_PROOF", nil},
		{"newlines", "000000001\n000000000001", "text/plain", true, "SIGNAL_PRESENT", map[string]int64{"id9": 1, "id12": 1}},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			r, objects := testRequest(t, []byte(tc.text), tc.media, false)
			e := testEngine(t)
			report, err := e.Evaluate(context.Background(), r, objects)
			if err != nil || report.Complete != tc.complete || report.ReasonCode != tc.reason || report.AdmissionProof != nil {
				t.Fatalf("%s result", tc.id)
			}
			requireCounts(t, tc.id, report, tc.counts)
			if report.Class == model.ResidencyPublic {
				// Only complete no-signal cases can use the exact test oracle.
				if !tc.complete || tc.reason != "NO_POSITIVE_PROOF" {
					t.Fatalf("%s proof", tc.id)
				}
			}
		})
	}
}

func TestResidencyBoundaries(t *testing.T) {
	signals := []string{"000000000001", "000000001", "a@example.invalid", "0900000000", "MST: 0000000000"}
	for n, signal := range signals {
		for b, boundary := range []string{"a", "đ", "\u0301", "1", "_"} {
			for side := 0; side < 2; side++ {
				id := strconv.Itoa(n) + "-" + strconv.Itoa(b) + "-" + strconv.Itoa(side)
				text := boundary + signal
				if side == 1 {
					text = signal + boundary
				}
				if n == 2 {
					start := len(boundary)
					if side == 1 {
						start = 0
					}
					// RE2 can absorb adjoining email local/domain characters.
					// Check the prescribed rune boundary on the fixed candidate.
					if matchBoundary(text, start, start+len(signal), true) {
						t.Fatalf("%s email-boundary", id)
					}
					continue
				}
				r, objects := testRequest(t, []byte(text), "text/plain", false)
				report, err := testEngine(t).Evaluate(context.Background(), r, objects)
				if err != nil || report.Class == model.ResidencyPublic {
					t.Fatalf("%s boundary-result", id)
				}
				// A tax prefix boundary does not suppress the independent phone.
				expected := map[string]int64{}
				if n == 4 && side == 0 {
					expected["phone"] = 1
				}
				if n == 1 && b == 3 && side == 1 {
					// Appending a digit creates an independent ten-digit phone.
					expected["phone"] = 1
				}
				requireCounts(t, id, report, expected)
			}
		}
	}
	for n, text := range []string{"a@example.invalid.", "a@example.invalid-", "a@example.invalid_", ".a@example.invalid"} {
		r, objects := testRequest(t, []byte(text), "text/plain", false)
		report, err := testEngine(t).Evaluate(context.Background(), r, objects)
		want := int64(0)
		if n == 3 {
			want = 1 // The leading dot belongs to the RE2 candidate.
		}
		if err != nil {
			t.Fatal("email-boundary result")
		}
		requireCounts(t, "email-boundary", report, map[string]int64{"email": want})
	}
}

func TestResidencyPositiveProofIsolation(t *testing.T) {
	r, objects := testRequest(t, []byte(baseHTTP), "", true)
	e := testEngine(t)
	closed, err := e.Evaluate(context.Background(), r, objects)
	if err != nil || closed.Class != model.ResidencyUnclassified || closed.ReasonCode != "NO_POSITIVE_PROOF" {
		t.Fatal("closed result")
	}
	installProof(t, e, r)
	positive, err := e.Evaluate(context.Background(), r, objects)
	if err != nil || positive.Class != model.ResidencyPublic || positive.AdmissionProof == nil || *positive.AdmissionProof != proofDigest(positive.Request.SHA256) {
		t.Fatal("positive result")
	}
	codec, err := ReportCodec(testPolicy(), ceiling)
	if err != nil {
		t.Fatal("production-codec setup")
	}
	if data, digest, err := codec.Encode(positive); err != bindingError || data != nil || digest != "" {
		t.Fatal("production-codec result")
	}
	if ValidateReportBinding(r, positive, testPolicy()) != bindingError {
		t.Fatal("production-binding result")
	}
	for n, change := range []func(*ScreenRequest){
		func(r *ScreenRequest) { r.OwnerKey = "execution-" + strings.Repeat("3", 32) },
		func(r *ScreenRequest) { r.SourceID = "different" },
		func(r *ScreenRequest) { r.Budget.MaxItems-- },
	} {
		changed := r
		change(&changed)
		report, err := e.Evaluate(context.Background(), changed, objects)
		if err != nil || report.Class == model.ResidencyPublic || report.ReasonCode != "NO_POSITIVE_PROOF" {
			t.Fatalf("proof-%d result", n)
		}
	}
	e.proof = nil
	report, err := e.Evaluate(context.Background(), r, objects)
	if err != nil || !reflect.DeepEqual(report, closed) {
		t.Fatal("removed-proof result")
	}
}

func TestResidencyDisabledDetectorControls(t *testing.T) {
	for n, text := range []string{"a@example.invalid", "000000000001", "000000001", "0900000000", "MST: 0000000000"} {
		r, objects := testRequest(t, []byte(text), "text/plain", false)
		e := testEngine(t)
		before, err := e.Evaluate(context.Background(), r, objects)
		if err != nil || before.Counts[n].Matches == 0 {
			t.Fatalf("disabled-%d baseline", n)
		}
		e.rules[n] = regexp.MustCompile(`a^`)
		after, err := e.Evaluate(context.Background(), r, objects)
		if err != nil || after.Counts[n].Matches != 0 || after.Class == model.ResidencyPublic || after.AdmissionProof != nil {
			t.Fatalf("disabled-%d control", n)
		}
	}
}

func TestResidencyHTTPViews(t *testing.T) {
	cases := []struct {
		id, data, reason string
		emails           int64
		err              ErrorKind
	}{
		{"base", baseHTTP, "NO_POSITIVE_PROOF", 0, ""},
		{"repeat-target", "GET /a@example.invalid HTTP/1.1\r\nHost: example.invalid\r\n\r\n", "SIGNAL_PRESENT", 2, ""},
		{"percent", "GET /a%40example.invalid HTTP/1.1\r\nHost: example.invalid\r\n\r\n", "SIGNAL_PRESENT", 1, ""},
		{"residual", "GET /a%2540example.invalid HTTP/1.1\r\nHost: example.invalid\r\n\r\n", "UNSUPPORTED_ENCODING", 0, ""},
		{"bad-percent", "GET /% HTTP/1.1\r\nHost: example.invalid\r\n\r\n", "", 0, inputError},
		{"fragment", "GET /#abc HTTP/1.1\r\nHost: example.invalid\r\n\r\n", "", 0, inputError},
		{"header", "GET / HTTP/1.1\r\nHost: example.invalid\r\nx-test: a@example.invalid\r\nX-Test: b@example.invalid\r\n\r\n", "SIGNAL_PRESENT", 2, ""},
		{"folded", "GET / HTTP/1.1\r\nHost: example.invalid\r\nx-test: x\r\n y\r\n\r\n", "", 0, inputError},
		{"duplicate-length", "POST / HTTP/1.1\r\nHost: example.invalid\r\nContent-Length: 0\r\nContent-Length: 0\r\n\r\n", "", 0, inputError},
		{"mixed-framing", "POST / HTTP/1.1\r\nHost: example.invalid\r\nContent-Length: 0\r\nTransfer-Encoding: chunked\r\n\r\n0\r\n\r\n", "", 0, inputError},
		{"unsupported-transfer", "POST / HTTP/1.1\r\nHost: example.invalid\r\nTransfer-Encoding: gzip\r\n\r\nabc", "UNSUPPORTED_ENCODING", 0, ""},
		{"chunked", "POST / HTTP/1.1\r\nHost: example.invalid\r\nTransfer-Encoding: chunked\r\nContent-Type: text/plain\r\n\r\n3\r\nabc\r\n0\r\n\r\n", "NO_POSITIVE_PROOF", 0, ""},
		{"trailer", "POST / HTTP/1.1\r\nHost: example.invalid\r\nTransfer-Encoding: chunked\r\nTrailer: X-Test\r\n\r\n0\r\nX-Test: x\r\n\r\n", "", 0, inputError},
		{"trailing", baseHTTP + "x", "", 0, inputError},
		{"star", "OPTIONS * HTTP/1.1\r\nHost: example.invalid\r\n\r\n", "UNSUPPORTED_ENCODING", 0, ""},
		{"gzip", httpBody("text/plain", "a@example.invalid", "Content-Encoding: gzip\r\n"), "UNSUPPORTED_ENCODING", 0, ""},
		{"body-charset", httpBody("text/plain; charset=windows-1258", "abc", ""), "UNSUPPORTED_ENCODING", 0, ""},
		{"duplicate-charset", httpBody("text/plain; charset=utf-8; charset=utf-8", "abc", ""), "UNSUPPORTED_ENCODING", 0, ""},
		{"sensitive-header", httpBody("text/plain", "abc", "Cookie: a@example.invalid\r\nAuthorization: b@example.invalid\r\n"), "SIGNAL_PRESENT", 2, ""},
		{"form", httpBody("application/x-www-form-urlencoded", "a%40example.invalid", ""), "SIGNAL_PRESENT", 1, ""},
		{"form-plus", httpBody("application/x-www-form-urlencoded", "a%40example.invalid+abc", ""), "SIGNAL_PRESENT", 1, ""},
		{"form-residual", httpBody("application/x-www-form-urlencoded", "a%2540example.invalid", ""), "UNSUPPORTED_ENCODING", 0, ""},
		{"json-escaped", httpBody("application/json", `{"x":"a\u0040example.invalid"}`, ""), "SIGNAL_PRESENT", 1, ""},
		{"json-repeated-value", httpBody("application/json", `{"x":["a\u0040example.invalid","a\u0040example.invalid"]}`, ""), "SIGNAL_PRESENT", 2, ""},
		{"json-duplicate", httpBody("application/json", `{"x":1,"x":2}`, ""), "", 0, inputError},
		{"json-null", httpBody("application/json", `null`, ""), "", 0, inputError},
		{"json-surrogate", httpBody("application/json", `{"x":"\ud800"}`, ""), "", 0, inputError},
		{"json-numbers", httpBody("application/json", `{"x":1e9999,"y":true,"z":null}`, ""), "NO_POSITIVE_PROOF", 0, ""},
		{"truncated", "POST / HTTP/1.1\r\nHost: example.invalid\r\nContent-Length: 3\r\n\r\nx", "", 0, inputError},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			r, objects := testRequest(t, []byte(tc.data), "", true)
			report, err := testEngine(t).Evaluate(context.Background(), r, objects)
			if tc.err != "" {
				if err != tc.err || !reflect.DeepEqual(report, ResidencyReport{}) {
					t.Fatalf("%s error", tc.id)
				}
				return
			}
			if err != nil || report.ReasonCode != tc.reason {
				t.Fatalf("%s result", tc.id)
			}
			requireCounts(t, tc.id, report, map[string]int64{"email": tc.emails})
		})
	}
}

func httpBody(media, body, extra string) string {
	return "POST / HTTP/1.1\r\nHost: example.invalid\r\nContent-Type: " + media + "\r\nContent-Length: " + strconv.Itoa(len(body)) + "\r\n" + extra + "\r\n" + body
}

func TestResidencyEvidenceAndClosure(t *testing.T) {
	r, objects := testRequest(t, []byte("abc"), "image/png", false)
	metadata := []byte("a@example.invalid")
	ref := hashRef("metadata", metadata)
	r.Evidence = []ScreenEvidence{{Kind: "metadata_utf8", SubjectHash: r.Subject.SHA256, Asset: ref}}
	objects = append(objects, ResolvedObject{ref, metadata})
	sort.Slice(objects, func(i, j int) bool { return objects[i].Ref.SHA256 < objects[j].Ref.SHA256 })
	report, err := testEngine(t).Evaluate(context.Background(), r, objects)
	if err != nil || report.Complete || report.Class != model.ResidencyPersonal {
		t.Fatal("metadata result")
	}
	requireCounts(t, "metadata", report, map[string]int64{"email": 1})
	for n, change := range []func([]ResolvedObject) []ResolvedObject{
		func(o []ResolvedObject) []ResolvedObject { return o[:len(o)-1] },
		func(o []ResolvedObject) []ResolvedObject {
			o[0].Bytes = []byte("changed")
			return o
		},
		func(o []ResolvedObject) []ResolvedObject {
			o[0].Ref.ByteSize++
			return o
		},
		func(o []ResolvedObject) []ResolvedObject {
			o[0].Ref.Path = "alias"
			return o
		},
		func(o []ResolvedObject) []ResolvedObject {
			o[0], o[1] = o[1], o[0]
			return o
		},
		func(o []ResolvedObject) []ResolvedObject { return append(o, o[0]) },
	} {
		changed := change(append([]ResolvedObject(nil), objects...))
		got, err := testEngine(t).Evaluate(context.Background(), r, changed)
		if err != bindingError || !reflect.DeepEqual(got, ResidencyReport{}) {
			t.Fatalf("closure-%d error", n)
		}
	}
	for n, text := range [][]byte{{0xff}, {'a', 0}} {
		changed := r
		changed.Evidence = []ScreenEvidence{{Kind: "metadata_utf8", SubjectHash: r.Subject.SHA256, Asset: hashRef("metadata", text)}}
		base := make([]ResolvedObject, 0)
		for _, o := range objects {
			if o.Ref.SHA256 != ref.SHA256 {
				base = append(base, o)
			}
		}
		base = append(base, ResolvedObject{changed.Evidence[0].Asset, text})
		sort.Slice(base, func(i, j int) bool { return base[i].Ref.SHA256 < base[j].Ref.SHA256 })
		if _, err := testEngine(t).Evaluate(context.Background(), changed, base); err != inputError {
			t.Fatalf("metadata-%d error", n)
		}
	}
}

func TestResidencyCancellation(t *testing.T) {
	r, objects := testRequest(t, []byte(baseHTTP), "", true)
	for n, expired := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		want := cancelledError
		if expired {
			cancel()
			ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			want = deadlineError
		} else {
			cancel()
		}
		got, err := testEngine(t).Evaluate(ctx, r, objects)
		cancel()
		if err != want || !reflect.DeepEqual(got, ResidencyReport{}) {
			t.Fatalf("context-%d error", n)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	inspection := inspection{ctx: ctx, limits: ceiling, items: 1, bytes: 10, counts: []DetectorCount{{ID: "email"}}, rules: []*regexp.Regexp{regexp.MustCompile("a")}}
	cancel()
	if inspection.view("a") != cancelledError {
		t.Fatal("context-view error")
	}
}

type steppedContext struct {
	context.Context
	remaining int
}

func (c *steppedContext) Err() error {
	if c.remaining == 0 {
		return context.Canceled
	}
	c.remaining--
	return nil
}

func TestResidencyCancellationBetweenPhases(t *testing.T) {
	r, objects := testRequest(t, []byte("abc"), "text/plain", false)
	requestBytes, _, err := RequestCodec(ceiling).Encode(r)
	if err != nil {
		t.Fatal("phase-context request")
	}
	policyBytes, _, err := PolicyCodec(ceiling).Encode(testPolicy())
	if err != nil {
		t.Fatal("phase-context policy")
	}
	ctx := &steppedContext{Context: context.Background(), remaining: 1}
	if value, err := resolveClosure(ctx, r, requestBytes, policyBytes, objects, ceiling); err != cancelledError || value != nil {
		t.Fatal("phase-context closure")
	}
	for n, remaining := range []int{1, 4} {
		i := inspection{ctx: &steppedContext{Context: context.Background(), remaining: remaining}, limits: ceiling,
			items: 1, bytes: 3, counts: []DetectorCount{{ID: "first"}, {ID: "second"}},
			rules: []*regexp.Regexp{regexp.MustCompile("a"), regexp.MustCompile("b")}}
		if i.view("a a") != cancelledError {
			t.Fatalf("phase-context-%d regex", n)
		}
	}
	e := testEngine(t)
	ctxFinal, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.proof = func([]byte, model.Digest) bool {
		cancel()
		return true
	}
	if report, err := e.Evaluate(ctxFinal, r, objects); err != cancelledError || !reflect.DeepEqual(report, ResidencyReport{}) {
		t.Fatal("phase-context report")
	}
}

func TestResidencyNoCrossFieldMatch(t *testing.T) {
	data := "GET / HTTP/1.1\r\nHost: example.invalid\r\nX-A: a@\r\nX-B: example.invalid\r\n\r\n"
	r, objects := testRequest(t, []byte(data), "", true)
	report, err := testEngine(t).Evaluate(context.Background(), r, objects)
	if err != nil || report.ReasonCode != "NO_POSITIVE_PROOF" {
		t.Fatal("cross-field result")
	}
	requireCounts(t, "cross-field", report, nil)
}

func TestResidencyReportHasNoTextOrCoordinates(t *testing.T) {
	r, objects := testRequest(t, []byte("a@example.invalid"), "text/plain", false)
	report, err := testEngine(t).Evaluate(context.Background(), r, objects)
	if err != nil {
		t.Fatal("private-report setup")
	}
	data, err := json.Marshal(report)
	if err != nil || bytes.Contains(data, []byte("a@example.invalid")) || bytes.Contains(data, []byte("Start")) || bytes.Contains(data, []byte("End")) {
		t.Fatal("private-report sink")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || len(fields) != 15 {
		t.Fatal("private-report field-allowlist")
	}
	for _, name := range []string{"SchemaVersion", "Request", "Subject", "Policy", "Country", "SourceID", "OwnerKey", "Operation", "RuleVersion", "EvidenceHash", "Complete", "Class", "ReasonCode", "Counts", "AdmissionProof"} {
		if _, ok := fields[name]; !ok {
			t.Fatal("private-report field-allowlist")
		}
	}
}
