package formats

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"danny.vn/banhmi/internal/corpus/conversion"
	"danny.vn/banhmi/internal/corpus/model"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	if filepath.Ext(name) == "" {
		if strings.HasPrefix(name, "negative-") {
			name += ".input"
		} else {
			name += ".json"
		}
	}
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func pins(t *testing.T) map[string]model.Digest {
	t.Helper()
	var entries []struct {
		File   string
		Digest model.Digest
	}
	if err := json.Unmarshal(fixture(t, "digests"), &entries); err != nil {
		t.Fatal(err)
	}
	result := make(map[string]model.Digest)
	for _, entry := range entries {
		if _, exists := result[entry.File]; exists {
			t.Fatal("duplicate digest pin")
		}
		result[entry.File] = entry.Digest
	}
	return result
}

func decodedFixture[T any](t *testing.T, name string, codec Codec[T]) T {
	t.Helper()
	v, _, err := codec.Decode(fixture(t, name))
	if err != nil {
		t.Fatal(name, err)
	}
	return v
}

func fixturePolicy(t *testing.T) PolicyBundle {
	t.Helper()
	return decodedFixture(t, "policy-bundle-v1", PolicyBundleCodec(DefaultLimits()))
}

func roundTrip[T any](t *testing.T, name string, codec Codec[T]) {
	t.Helper()
	data := fixture(t, name)
	value, digest, err := codec.Decode(data)
	if err != nil {
		t.Fatal(name, err)
	}
	expected := pins(t)[name+".json"]
	if digest != expected {
		t.Fatalf("%s digest = %s, want %s", name, digest, expected)
	}
	encoded, encodedDigest, err := codec.Encode(value)
	if err != nil || encodedDigest != expected || !bytes.Equal(encoded, data) {
		t.Fatalf("%s canonical round trip changed bytes: %v", name, err)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != string(expected) {
		t.Fatal("independent SHA-256 does not match pin")
	}
	// Every format rejects wire ambiguity independently of semantic validation.
	version := "SchemaVersion"
	if name == "policy-bundle-v1" {
		version = "schema_version"
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatal(err)
	}
	v := object[version]
	mutations := map[string][]byte{
		"unknown":   append(append([]byte(nil), data[:len(data)-1]...), []byte(`,"Unexpected":true}`)...),
		"duplicate": append(append([]byte(nil), data[:len(data)-1]...), []byte(`,"`+version+`":`+string(v)+`}`)...),
		"trailing":  append(append([]byte(nil), data...), []byte(`{}`)...),
		"newline":   append(append([]byte(nil), data...), '\n'),
		"version":   bytes.Replace(data, v, []byte(`"unknown-v99"`), 1),
	}
	for reason, mutated := range mutations {
		t.Run(reason, func(t *testing.T) {
			if _, _, err := codec.Decode(mutated); err == nil {
				t.Fatal("accepted ambiguous or unknown format bytes")
			}
		})
	}
}

func TestCanonicalFixtures(t *testing.T) {
	limits := DefaultLimits()
	policy := fixturePolicy(t)
	report, err := GateReportCodec(policy, limits)
	if err != nil {
		t.Fatal(err)
	}
	rollup, err := GateRollupCodec(policy, limits)
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*testing.T){
		"normalizer-v1":              func(t *testing.T) { roundTrip(t, "normalizer-v1", NormalizerCodec(limits)) },
		"parser-v1":                  func(t *testing.T) { roundTrip(t, "parser-v1", ParserCodec(limits)) },
		"candidate-input-v1":         func(t *testing.T) { roundTrip(t, "candidate-input-v1", CandidateInputCodec(limits)) },
		"candidate-input-reprocess":  func(t *testing.T) { roundTrip(t, "candidate-input-reprocess", CandidateInputCodec(limits)) },
		"candidate-output-v1":        func(t *testing.T) { roundTrip(t, "candidate-output-v1", CandidateOutputCodec(limits)) },
		"policy-bundle-v1":           func(t *testing.T) { roundTrip(t, "policy-bundle-v1", PolicyBundleCodec(limits)) },
		"gate-report-complete":       func(t *testing.T) { roundTrip(t, "gate-report-complete", report) },
		"gate-report-pre-conversion": func(t *testing.T) { roundTrip(t, "gate-report-pre-conversion", report) },
		"gate-report-needs-ocr":      func(t *testing.T) { roundTrip(t, "gate-report-needs-ocr", report) },
		"gate-rollup-complete":       func(t *testing.T) { roundTrip(t, "gate-rollup-complete", rollup) },
		"gate-rollup-pre-conversion": func(t *testing.T) { roundTrip(t, "gate-rollup-pre-conversion", rollup) },
		"gate-rollup-needs-ocr":      func(t *testing.T) { roundTrip(t, "gate-rollup-needs-ocr", rollup) },
		"gap-scope-v1":               func(t *testing.T) { roundTrip(t, "gap-scope-v1", GapScopeCodec(limits)) },
	}
	if len(tests) != len(pins(t)) {
		t.Fatal("fixture pins and codec cases differ")
	}
	for name, run := range tests {
		t.Run(name, run)
	}
}

func TestNegativeFixtures(t *testing.T) {
	var entries []struct {
		File  string
		Codec string
	}
	if err := json.Unmarshal(fixture(t, "negatives"), &entries); err != nil {
		t.Fatal(err)
	}
	limits := DefaultLimits()
	report, err := GateReportCodec(fixturePolicy(t), limits)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		t.Run(entry.File, func(t *testing.T) {
			data := fixture(t, entry.File)
			var err error
			switch entry.Codec {
			case "parser":
				_, _, err = ParserCodec(limits).Decode(data)
			case "normalizer":
				_, _, err = NormalizerCodec(limits).Decode(data)
			case "policy":
				_, _, err = PolicyBundleCodec(limits).Decode(data)
			case "report":
				_, _, err = report.Decode(data)
			default:
				t.Fatal("uncovered negative codec")
			}
			if err == nil {
				t.Fatal("accepted negative fixture")
			}
		})
	}
}

func TestNormalizerDerivation(t *testing.T) {
	value := BaselineNormalizer()
	expected := decodedFixture(t, "normalizer-v1", NormalizerCodec(DefaultLimits()))
	if !reflect.DeepEqual(value, expected) {
		t.Fatal("normalizer derivation changed pinned bytes")
	}
	if len(value.Rules) != 1 || value.Rules[0].Rule != conversion.NormalizationRuleNFCCRLF || value.Rules[0].Version != conversion.NormalizationVersionNFCCRLF {
		t.Fatal("normalizer does not use exported M1.12 rule/version")
	}
	for _, transform := range []string{conversion.TransformIdentity, conversion.TransformNFCCRLF, conversion.TransformSourceDecode} {
		if !contains(value.Transforms, transform) {
			t.Fatal("missing exported transform", transform)
		}
	}
}

func TestLimits(t *testing.T) {
	defaults := DefaultLimits()
	if defaults != (Limits{1024, 10000, 4194304, 67108864, 60000}) {
		t.Fatal("unmeasured defaults changed")
	}
	for _, limits := range []Limits{
		{0, 1, 1, 1, 1}, {1025, 1, 1, 1, 1}, {1, 0, 1, 1, 1},
		{1, 1, 0, 1, 1}, {1, 1, 1, 0, 1}, {1, 1, 1, 1, 0},
		{1, -1, 1, 1, 1},
	} {
		if limits.Validate() == nil {
			t.Fatal("accepted unbounded limits", limits)
		}
	}
	data := fixture(t, "normalizer-v1")
	limits := defaults
	limits.MaxJSONBytes = int64(len(data) - 1)
	if _, _, err := NormalizerCodec(limits).Decode(data); err == nil {
		t.Fatal("JSON byte bound did not reject before decoding")
	}
	limits = defaults
	limits.MaxRows = 3 // One rule plus three transforms exceeds three rows.
	if _, _, err := NormalizerCodec(limits).Decode(data); err == nil {
		t.Fatal("row bound did not reject before typed array allocation")
	}
	input := decodedFixture(t, "candidate-input-v1", CandidateInputCodec(defaults))
	limits = defaults
	limits.MaxBlobs = 1
	if _, _, err := CandidateInputCodec(limits).Encode(input); err == nil {
		t.Fatal("blob bound ignored")
	}
	limits = defaults
	limits.MaxObjectBytes = 1
	if _, _, err := CandidateInputCodec(limits).Encode(input); err == nil {
		t.Fatal("aggregate object byte bound ignored")
	}
	if err := (Limits{1, 2, 3, 4, 5}).Validate(); err != nil {
		t.Fatal("configurable smaller bounds rejected", err)
	}
}

func TestPolicyJSONB(t *testing.T) {
	data := fixture(t, "policy-bundle-v1")
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	indented, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	_, canonical, digest, err := PolicyFromJSONB(indented, DefaultLimits())
	if err != nil || !bytes.Equal(canonical, data) || digest != pins(t)["policy-bundle-v1.json"] {
		t.Fatal("JSONB changed canonical policy digest", err)
	}
	for _, name := range []string{"negative-float", "negative-exponent", "negative-null", "negative-bound"} {
		if _, _, _, err := PolicyFromJSONB(fixture(t, name), DefaultLimits()); err == nil {
			t.Fatal("JSONB accepted forbidden number, null or bound", name)
		}
	}
	// Values beyond float64's exact integer range must survive without rounding.
	policy := fixturePolicy(t)
	policy.AcceptanceLimits.MaxElapsedMS = 9007199254740993
	encoded, hash, err := PolicyBundleCodec(DefaultLimits()).Encode(policy)
	if err != nil {
		t.Fatal(err)
	}
	decoded, canonical, decodedHash, err := PolicyFromJSONB(encoded, DefaultLimits())
	if err != nil || decoded.AcceptanceLimits.MaxElapsedMS != policy.AcceptanceLimits.MaxElapsedMS || hash != decodedHash || !bytes.Equal(encoded, canonical) {
		t.Fatal("JSONB integer precision lost", err)
	}
}

func TestGoEscapingAndNestedDuplicates(t *testing.T) {
	codec := ParserCodec(DefaultLimits())
	data, _, err := codec.Encode(Parser{SchemaVersion: "parser-v1", Parser: "<>&\u2028\u2029"})
	expected := []byte(`{"SchemaVersion":"parser-v1","Parser":"\u003c\u003e\u0026\u2028\u2029"}`)
	if err != nil || !bytes.Equal(data, expected) {
		t.Fatal("codec did not use standard Go JSON escaping", err)
	}
	noncanonical := bytes.Replace(data, []byte(`\u003c`), []byte("<"), 1)
	if _, _, err := codec.Decode(noncanonical); err == nil {
		t.Fatal("accepted alternative escaping")
	}
	policy := fixturePolicy(t)
	reportCodec, err := GateReportCodec(policy, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	data = fixture(t, "gate-report-complete")
	data = bytes.Replace(data, []byte(`"Artifact":{`), []byte(`"Artifact":{"ArtifactHash":"`+strings.Repeat("c", 64)+`",`), 1)
	if _, _, err := reportCodec.Decode(data); err == nil {
		t.Fatal("nested duplicate key accepted")
	}
	policyBytes := fixture(t, "policy-bundle-v1")
	policyBytes = bytes.Replace(policyBytes, []byte(`"max_rows":10000`), []byte(`"max_rows":10000,"max_rows":10000`), 1)
	if _, _, _, err := PolicyFromJSONB(policyBytes, DefaultLimits()); err == nil {
		t.Fatal("JSONB duplicate key accepted before insertion")
	}
	artifact, rollup, err := PolicyProfiles(policy, DefaultLimits())
	if err != nil || artifact.PolicyHash != pins(t)["policy-bundle-v1.json"] || rollup.PolicyHash != artifact.PolicyHash || !reflect.DeepEqual(artifact.Metrics, policy.ArtifactProfile) || !reflect.DeepEqual(rollup.Metrics, policy.RollupProfile) {
		t.Fatal("policy model mapping changed", err)
	}
	artifact.Metrics[0].Threshold = "changed"
	if policy.ArtifactProfile[0].Threshold == "changed" || rollup.Metrics[0].Threshold == "changed" {
		t.Fatal("policy profiles share mutable slices")
	}
}
