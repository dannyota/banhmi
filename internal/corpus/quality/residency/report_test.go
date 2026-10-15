package residency

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/quality/api"
)

func baseReport(t *testing.T) (ScreenRequest, ResidencyReport) {
	t.Helper()
	r, objects := testRequest(t, []byte(baseHTTP), "", true)
	report, err := testEngine(t).Evaluate(context.Background(), r, objects)
	if err != nil {
		t.Fatal("report setup")
	}
	return r, report
}

func TestResidencyReportRoundTripAndBinding(t *testing.T) {
	r, report := baseReport(t)
	codec, err := ReportCodec(testPolicy(), ceiling)
	if err != nil {
		t.Fatal("report-codec setup")
	}
	data, digest, err := codec.Encode(report)
	if err != nil || digest != hash(data) || bytes.HasSuffix(data, []byte("\n")) || !bytes.HasPrefix(data, []byte(`{"SchemaVersion":"residency-report-v1","Request":`)) || !bytes.HasSuffix(data, []byte(`"AdmissionProof":null}`)) {
		t.Fatal("report encode")
	}
	decoded, decodedDigest, err := codec.Decode(data)
	if err != nil || decodedDigest != digest || !reflect.DeepEqual(report, decoded) || ValidateReportBinding(r, decoded, testPolicy()) != nil {
		t.Fatal("report round-trip")
	}
	for n, mutate := range []func(*ResidencyReport){
		func(r *ResidencyReport) { r.Request.ByteSize++ },
		func(r *ResidencyReport) { r.Request = hashRef("requests", []byte("other")) },
		func(r *ResidencyReport) { r.Subject.ByteSize++ },
		func(r *ResidencyReport) { r.Subject.SHA256 = hash([]byte("other")) },
		func(r *ResidencyReport) { r.Policy.ByteSize++ },
		func(r *ResidencyReport) { r.Policy = hashRef("policies", []byte("other")) },
		func(r *ResidencyReport) { r.Country = "my" },
		func(r *ResidencyReport) { r.SourceID = "other" },
		func(r *ResidencyReport) { r.OwnerKey = "execution-" + strings.Repeat("3", 32) },
		func(r *ResidencyReport) { r.Operation = "artifact_export" },
		func(r *ResidencyReport) { r.RuleVersion = "other" },
		func(r *ResidencyReport) { r.EvidenceHash = hash([]byte("other")) },
	} {
		changed := report
		mutate(&changed)
		if ValidateReportBinding(r, changed, testPolicy()) == nil {
			t.Fatalf("binding-%d rejection", n)
		}
	}
}

func TestResidencyReportClosedCombinations(t *testing.T) {
	_, base := baseReport(t)
	codec, err := ReportCodec(testPolicy(), ceiling)
	if err != nil {
		t.Fatal("report-combination setup")
	}
	for n, mutate := range []func(*ResidencyReport){
		func(r *ResidencyReport) { r.Class = model.ResidencyPublic },
		func(r *ResidencyReport) { r.Class = model.ResidencyPersonal },
		func(r *ResidencyReport) { r.Complete = false },
		func(r *ResidencyReport) { r.ReasonCode = "SIGNAL_PRESENT" },
		func(r *ResidencyReport) { r.ReasonCode = "INCOMPLETE_SURFACE" },
		func(r *ResidencyReport) { r.ReasonCode = "UNSUPPORTED_ENCODING" },
		func(r *ResidencyReport) { r.Counts = nil },
		func(r *ResidencyReport) { r.Counts[0].Matches = -1 },
		func(r *ResidencyReport) { r.Counts[0].ID = "other" },
		func(r *ResidencyReport) { r.Counts[0].Matches = 1 },
		func(r *ResidencyReport) {
			proof := proofDigest(r.Request.SHA256)
			r.AdmissionProof = &proof
		},
	} {
		r := base
		r.Counts = append([]DetectorCount(nil), base.Counts...)
		mutate(&r)
		if data, digest, err := codec.Encode(r); err == nil || data != nil || digest != "" {
			t.Fatalf("combination-%d rejection", n)
		}
	}
	for n, complete := range []bool{true, false} {
		r := base
		r.Counts = append([]DetectorCount(nil), base.Counts...)
		r.Counts[0].Matches, r.Complete, r.Class, r.ReasonCode = 1, complete, model.ResidencyPersonal, "SIGNAL_PRESENT"
		if _, _, err := codec.Encode(r); err != nil {
			t.Fatalf("signal-combination-%d encode", n)
		}
	}
}

func TestResidencyReportBounds(t *testing.T) {
	_, report := baseReport(t)
	data, _ := json.Marshal(report)
	limits := ceiling
	limits.MaxReportBytes = int64(len(data))
	codec, err := ReportCodec(testPolicy(), limits)
	if err != nil {
		t.Fatal("report-bound setup")
	}
	if _, _, err := codec.Encode(report); err != nil {
		t.Fatal("report-byte limit")
	}
	limits.MaxReportBytes--
	codec, err = ReportCodec(testPolicy(), limits)
	if err == nil {
		_, _, err = codec.Encode(report)
	}
	if err != limitError {
		t.Fatal("report-byte limit-plus-one")
	}
	limits.MaxReportBytes = 1
	if _, err := ReportCodec(testPolicy(), limits); err != limitError {
		t.Fatal("minimal-report bound")
	}
	codec, err = ReportCodec(testPolicy(), ceiling)
	if err != nil {
		t.Fatal("report-count setup")
	}
	report.Counts[0].Matches = ceiling.MaxMatches
	report.Class, report.ReasonCode = model.ResidencyPersonal, "SIGNAL_PRESENT"
	if _, _, err := codec.Encode(report); err != nil {
		t.Fatal("report-count limit")
	}
	report.Counts[1].Matches = 1
	if _, _, err := codec.Encode(report); err != limitError {
		t.Fatal("report-count limit-plus-one")
	}
}

func TestResidencyInspectionOperationalBounds(t *testing.T) {
	for n, configure := range []func(*inspection){
		func(i *inspection) { i.bytes = 3 },
		func(i *inspection) { i.items = 1 },
		func(i *inspection) { i.limits.MaxMatches = 3 },
	} {
		i := inspection{ctx: context.Background(), limits: ceiling, bytes: 10, items: 10,
			counts: []DetectorCount{{ID: "test"}}, rules: []*regexp.Regexp{regexp.MustCompile("a")}}
		configure(&i)
		if i.view("a a") != nil {
			t.Fatalf("inspection-%d limit", n)
		}
		var err error
		switch n {
		case 0:
			err = i.view("x")
		case 1:
			err = i.view("")
		case 2:
			if i.view("a") != nil {
				t.Fatal("inspection-match limit")
			}
			err = i.view("a")
		}
		if err != limitError {
			t.Fatalf("inspection-%d limit-plus-one", n)
		}
	}
}

func TestResidencyCeilingWorkBounds(t *testing.T) {
	for n, bound := range []string{"views", "matches", "view-bytes", "objects", "closure-bytes", "subject"} {
		switch bound {
		case "views":
			i := inspection{ctx: context.Background(), limits: ceiling, items: ceiling.MaxItems, bytes: ceiling.MaxDecodedBytes}
			for count := int64(0); count < ceiling.MaxItems; count++ {
				if i.view("") != nil {
					t.Fatal("views limit")
				}
			}
			if i.view("") != limitError {
				t.Fatal("views limit-plus-one")
			}
		case "matches":
			i := inspection{ctx: context.Background(), limits: ceiling, items: 2, bytes: ceiling.MaxDecodedBytes,
				counts: []DetectorCount{{ID: "email"}}, rules: []*regexp.Regexp{regexp.MustCompile(frozenRules()[0].Pattern)}}
			if i.view(strings.Repeat("a@example.invalid ", int(ceiling.MaxMatches))) != nil || i.matches != ceiling.MaxMatches {
				t.Fatal("matches limit")
			}
			if i.view("a@example.invalid") != limitError {
				t.Fatal("matches limit-plus-one")
			}
		case "view-bytes":
			i := inspection{ctx: context.Background(), limits: ceiling, items: 2, bytes: ceiling.MaxDecodedBytes}
			if i.view(strings.Repeat("x", int(ceiling.MaxDecodedBytes))) != nil || i.view("x") != limitError {
				t.Fatal("view-bytes limit-plus-one")
			}
		case "objects":
			refs := make([]model.AssetRef, 0)
			for count := int64(0); count < ceiling.MaxObjects-1; count++ {
				refs = append(refs, hashRef("objects", []byte(strings.Repeat("x", int(count)))))
			}
			if dependencyBounds(refs, []byte("root"), ceiling) != nil {
				t.Fatal("objects limit")
			}
			refs = append(refs, hashRef("objects", []byte("last")))
			if dependencyBounds(refs, []byte("root"), ceiling) != limitError {
				t.Fatal("objects limit-plus-one")
			}
		case "closure-bytes":
			ref := hashRef("objects", []byte("x"))
			ref.ByteSize = ceiling.MaxDecodedBytes - 4
			if dependencyBounds(ref, []byte("root"), ceiling) != nil {
				t.Fatal("closure-bytes limit")
			}
			ref.ByteSize++
			if dependencyBounds(ref, []byte("root"), ceiling) != limitError {
				t.Fatal("closure-bytes limit-plus-one")
			}
		case "subject":
			r, objects := testRequest(t, bytes.Repeat([]byte("x"), int(ceiling.MaxSubjectBytes)), "image/png", false)
			if report, err := testEngine(t).Evaluate(context.Background(), r, objects); err != nil || report.Complete || report.AdmissionProof != nil {
				t.Fatal("subject limit")
			}
			r.Subject.ByteSize++
			r.Capture.Artifact.Asset = r.Subject
			if _, _, err := RequestCodec(ceiling).Encode(r); err != limitError {
				t.Fatal("subject limit-plus-one")
			}
		default:
			t.Fatalf("ceiling-%d setup", n)
		}
	}
}

// safeErrorSink checks the entire returned error type and text, not substrings.
func safeErrorSink(err error) bool {
	kind, ok := err.(ErrorKind)
	if !ok || errors.Unwrap(err) != nil {
		return false
	}
	return kind == inputError || kind == policyError || kind == bindingError || kind == limitError || kind == cancelledError || kind == deadlineError
}

// safeEnvelopeSink checks both field allowlists and exact final serialization.
func safeEnvelopeSink(data, expected []byte) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || len(fields) != 2 || fields["Key"] == nil || fields["Manifest"] == nil {
		return false
	}
	return bytes.Equal(data, expected)
}

func TestResidencySinkSensitivity(t *testing.T) {
	r, report := baseReport(t)
	requestBytes, requestDigest, err := RequestCodec(ceiling).Encode(r)
	if err != nil || requestDigest != hash(requestBytes) {
		t.Fatal("sink setup")
	}
	codec, err := ReportCodec(testPolicy(), ceiling)
	if err != nil {
		t.Fatal("sink-codec setup")
	}
	_, reportDigest, err := codec.Encode(report)
	if err != nil {
		t.Fatal("sink-report setup")
	}
	requestEnvelope := api.ResidencyRequestEnvelope{Key: "screen-" + strings.Repeat("4", 32), Manifest: requestDigest}
	resultEnvelope := model.ResidencyEnvelope{Key: "residency-" + strings.Repeat("4", 32), Manifest: reportDigest}
	expected := [][]byte{
		[]byte(`{"Key":"screen-44444444444444444444444444444444","Manifest":"536786ce6f69564e69bea4a4e6b91bdfcf71cd1bf0018c7d4c47ba43bee4f6e6"}`),
		[]byte(`{"Key":"residency-44444444444444444444444444444444","Manifest":"011751da5c78515f13cfef1afe012c265cec163dc10cdd695696c941a869e45f"}`),
	}
	for n, envelope := range []any{requestEnvelope, resultEnvelope} {
		data, err := json.Marshal(envelope)
		if err != nil || !safeEnvelopeSink(data, expected[n]) {
			t.Fatalf("envelope-%d baseline", n)
		}
		var want, got map[string]string
		if json.Unmarshal(expected[n], &want) != nil || json.Unmarshal(data, &got) != nil || !reflect.DeepEqual(want, got) {
			t.Fatalf("envelope-%d values", n)
		}
		for m, injection := range []map[string]any{
			{"Text": "Nguyễn Văn Mẫu <a@example.invalid>"},
			{"Start": 123, "End": 456},
			{"Class": model.ResidencyPersonal},
			{"ReasonCode": "SIGNAL_PRESENT"},
			{"Counts": []DetectorCount{{ID: "email", Matches: 1}}},
			{"Key": "Nguyễn Văn Mẫu <a@example.invalid>"},
			{"Manifest": "Nguyễn Văn Mẫu <a@example.invalid>"},
		} {
			var fields map[string]any
			if json.Unmarshal(data, &fields) != nil {
				t.Fatal("envelope-control setup")
			}
			for name, value := range injection {
				fields[name] = value
			}
			injected, err := json.Marshal(fields)
			if err != nil || safeEnvelopeSink(injected, expected[n]) {
				t.Fatalf("envelope-%d-control-%d sink", n, m)
			}
		}
	}
	for n, err := range []error{inputError, policyError, bindingError, limitError, cancelledError, deadlineError} {
		if !safeErrorSink(err) {
			t.Fatalf("error-%d baseline", n)
		}
	}
	for n, injected := range []string{"Nguyễn Văn Mẫu <a@example.invalid>", `{"Start":123,"End":456}`, "personal_data", "SIGNAL_PRESENT", `{"email":1}`} {
		if safeErrorSink(errors.New(injected)) || safeErrorSink(ErrorKind(injected)) {
			t.Fatalf("error-control-%d sink", n)
		}
	}
	for n, mutate := range []func(*ScreenRequest){
		func(r *ScreenRequest) { r.SourceID = "Nguyễn Văn Mẫu" },
		func(r *ScreenRequest) { r.Subject.ByteSize = -123 },
		func(r *ScreenRequest) { r.Operation = "personal_data" },
		func(r *ScreenRequest) { r.Operation = "SIGNAL_PRESENT" },
		func(r *ScreenRequest) { r.MediaType = strings.Repeat("x", 65537) },
	} {
		changed := r
		mutate(&changed)
		_, _, err := RequestCodec(ceiling).Encode(changed)
		if !safeErrorSink(err) {
			t.Fatalf("error-real-%d sink", n)
		}
	}
}
