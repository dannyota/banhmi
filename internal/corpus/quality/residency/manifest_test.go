package residency

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"danny.vn/banhmi/internal/corpus/conversion"
	conversionapi "danny.vn/banhmi/internal/corpus/conversion/api"
	"danny.vn/banhmi/internal/corpus/model"
)

func TestResidencyRequestCanonical(t *testing.T) {
	for _, outbound := range []bool{true, false} {
		data := []byte("abc")
		if outbound {
			data = []byte(baseHTTP)
		}
		r, _ := testRequest(t, data, "text/plain", outbound)
		encoded, digest, err := RequestCodec(ceiling).Encode(r)
		if err != nil || digest != hash(encoded) || bytes.HasSuffix(encoded, []byte("\n")) {
			t.Fatal("request encode")
		}
		if !bytes.HasPrefix(encoded, []byte(`{"SchemaVersion":"screen-request-v1","Country":"vn","SourceID":"fixture","OwnerKey":`)) || !bytes.HasSuffix(encoded, []byte(`"Evidence":[]}`)) {
			t.Fatal("request field-order")
		}
		if outbound && !bytes.Contains(encoded, []byte(`"Capture":null`)) {
			t.Fatal("request capture-shape")
		}
		decoded, decodedDigest, err := RequestCodec(ceiling).Decode(encoded)
		if err != nil || decodedDigest != digest || !reflect.DeepEqual(decoded, r) {
			t.Fatal("request round-trip")
		}
	}
}

func TestResidencyCanonicalJSONNegatives(t *testing.T) {
	r, _ := testRequest(t, []byte("abc"), "text/plain", false)
	data, _, err := RequestCodec(ceiling).Encode(r)
	if err != nil {
		t.Fatal("json-negative setup")
	}
	for n, invalid := range [][]byte{
		append(append([]byte(nil), data...), '\n'),
		append(append([]byte(nil), data...), []byte("{}")...),
		bytes.Replace(data, []byte(`"Evidence":[]`), []byte(`"Evidence":null`), 1),
		bytes.Replace(data, []byte(`"Country":"vn",`), nil, 1),
		bytes.Replace(data, []byte(`"Country":"vn"`), []byte(`"Country":"vn","Extra":1`), 1),
		bytes.Replace(data, []byte(`"Country":"vn"`), []byte(`"Country":"vn","Country":"vn"`), 1),
		bytes.Replace(data, []byte(`"MaxPages":1`), []byte(`"MaxPages":1,"MaxPages":1`), 1),
		bytes.Replace(data, []byte(`"ByteSize":3`), []byte(`"ByteSize":3.0`), 1),
		bytes.Replace(data, []byte(`"ByteSize":3`), []byte(`"ByteSize":3e0`), 1),
		bytes.Replace(data, []byte(`"ByteSize":3`), []byte(`"ByteSize":-0`), 1),
		bytes.Replace(data, []byte(`"MediaType":"text/plain"`), []byte(`"MediaType":"\ud800"`), 1),
		bytes.Replace(data, []byte(`"MediaType":"text/plain"`), []byte(`"MediaType":"\udc00"`), 1),
		bytes.Replace(data, []byte(`"MediaType":"text/plain"`), []byte(`"MediaType":"\u0000"`), 1),
		bytes.Replace(data, []byte(`"Country":"vn"`), []byte("\"Country\":\"\xff\""), 1),
		bytes.Replace(data, []byte(`"Country":"vn","SourceID":"fixture"`), []byte(`"SourceID":"fixture","Country":"vn"`), 1),
	} {
		got, digest, err := RequestCodec(ceiling).Decode(invalid)
		if err == nil || !reflect.DeepEqual(got, ScreenRequest{}) || digest != "" {
			t.Fatalf("json-negative-%d decode", n)
		}
	}
	for n, text := range []string{`"\ud800"`, `"\udc00"`, `"\ud800\u0041"`, `"\u0000"`, `{"a":1,"a":2}`} {
		if inspectJSON([]byte(text), 4096, 65536, false, nil) != inputError {
			t.Fatalf("json-structural-%d decode", n)
		}
	}
}

func TestResidencyEscapingAndUnicode(t *testing.T) {
	var previous model.Digest
	for n, media := range []string{"<>&\u2028\u2029", "á", "a\u0301"} {
		r, _ := testRequest(t, []byte("abc"), media, false)
		data, digest, err := RequestCodec(ceiling).Encode(r)
		if err != nil || n > 0 && previous == digest {
			t.Fatalf("unicode-%d encode", n)
		}
		if n == 0 && !bytes.Contains(data, []byte(`\u003c\u003e\u0026\u2028\u2029`)) {
			t.Fatal("unicode escaping")
		}
		decoded, _, err := RequestCodec(ceiling).Decode(data)
		if err != nil || decoded.MediaType != media {
			t.Fatalf("unicode-%d round-trip", n)
		}
		previous = digest
	}
}

func TestResidencyRequestUnion(t *testing.T) {
	for n, mutate := range []func(*ScreenRequest){
		func(r *ScreenRequest) { r.Country = "sg" },
		func(r *ScreenRequest) { r.SourceID = "Bad" },
		func(r *ScreenRequest) { r.OwnerKey = "screen-" + strings.Repeat("1", 32) },
		func(r *ScreenRequest) { r.Operation = "capture_export" },
		func(r *ScreenRequest) { r.SubjectKind = "other" },
		func(r *ScreenRequest) { r.Capture = nil },
		func(r *ScreenRequest) { r.Capture.Artifact.CaptureKey = "x" },
		func(r *ScreenRequest) { r.Capture.Artifact.Country = "my" },
		func(r *ScreenRequest) { r.Capture.Artifact.SourceID = "other" },
		func(r *ScreenRequest) { r.MediaType = "text/html" },
		func(r *ScreenRequest) { r.Subject.Path = "other" },
		func(r *ScreenRequest) { r.Policy.Path = "other" },
		func(r *ScreenRequest) { r.Budget.MaxPages++ },
		func(r *ScreenRequest) { r.Budget.MaxConcurrent = 0 },
		func(r *ScreenRequest) { r.Evidence = nil },
		func(r *ScreenRequest) { r.Subject.ByteSize = -1 },
	} {
		r, _ := testRequest(t, []byte("abc"), "text/plain", false)
		mutate(&r)
		if data, digest, err := RequestCodec(ceiling).Encode(r); err == nil || data != nil || digest != "" {
			t.Fatalf("union-%d encode", n)
		}
	}
	for n, mutate := range []func(*ScreenRequest){
		func(r *ScreenRequest) { r.Subject.Path = "objects/" + string(r.Subject.SHA256) },
		func(r *ScreenRequest) { r.MediaType = "text/plain" },
		func(r *ScreenRequest) { r.Operation = "artifact_export" },
		func(r *ScreenRequest) { r.Evidence = []ScreenEvidence{{}} },
	} {
		r, _ := testRequest(t, []byte(baseHTTP), "", true)
		mutate(&r)
		if _, _, err := RequestCodec(ceiling).Encode(r); err == nil {
			t.Fatalf("outbound-union-%d encode", n)
		}
	}
}

func TestResidencyRequestAndClosureBounds(t *testing.T) {
	r, _ := testRequest(t, []byte("abc"), "text/plain", false)
	data, _, err := RequestCodec(ceiling).Encode(r)
	if err != nil {
		t.Fatal("request-bound setup")
	}
	var total int64 = int64(len(data))
	seen := make(map[model.Digest]bool)
	if err := visitRefs(r, func(ref model.AssetRef) error {
		if !seen[ref.SHA256] {
			total += ref.ByteSize
			seen[ref.SHA256] = true
		}
		return nil
	}); err != nil {
		t.Fatal("request-bound accounting")
	}
	for n, configure := range []func(*ResidencyLimits){
		func(l *ResidencyLimits) { l.MaxRequestBytes = int64(len(data)) },
		func(l *ResidencyLimits) { l.MaxSubjectBytes = 3 },
		func(l *ResidencyLimits) { l.MaxDecodedBytes = total },
		func(l *ResidencyLimits) { l.MaxObjects = int64(len(seen)) + 1 },
	} {
		limits := ceiling
		configure(&limits)
		if _, _, err := RequestCodec(limits).Encode(r); err != nil {
			t.Fatalf("request-bound-%d limit", n)
		}
		switch n {
		case 0:
			limits.MaxRequestBytes--
		case 1:
			limits.MaxSubjectBytes--
		case 2:
			limits.MaxDecodedBytes--
		case 3:
			limits.MaxObjects--
		}
		if _, _, err := RequestCodec(limits).Encode(r); err != limitError {
			t.Fatalf("request-bound-%d limit-plus-one", n)
		}
	}
	changed := r
	capture := *r.Capture
	changed.Capture = &capture
	changed.Budget.MaxInputBytes = 3
	changed.Capture.Budget = changed.Budget
	if _, _, err := RequestCodec(ceiling).Encode(changed); err != nil {
		t.Fatal("budget-input limit")
	}
	changed.Budget.MaxInputBytes--
	changed.Capture.Budget = changed.Budget
	if _, _, err := RequestCodec(ceiling).Encode(changed); err != limitError {
		t.Fatal("budget-input limit-plus-one")
	}
}

func TestResidencyEvidenceBoundsAndOrder(t *testing.T) {
	r, _ := testRequest(t, []byte("abc"), "text/plain", false)
	for n := 0; n < 17; n++ {
		ref := hashRef("metadata", []byte(strings.Repeat("x", n)))
		r.Evidence = append(r.Evidence, ScreenEvidence{"metadata_utf8", r.Subject.SHA256, ref, nil})
	}
	sort.Slice(r.Evidence, func(i, j int) bool { return evidenceLess(r.Evidence[i], r.Evidence[j]) })
	r.Evidence = r.Evidence[:16]
	if _, _, err := RequestCodec(ceiling).Encode(r); err != nil {
		t.Fatal("evidence limit")
	}
	r.Evidence = append(r.Evidence, ScreenEvidence{"metadata_utf8", r.Subject.SHA256, hashRef("metadata", []byte("last")), nil})
	sort.Slice(r.Evidence, func(i, j int) bool { return evidenceLess(r.Evidence[i], r.Evidence[j]) })
	if _, _, err := RequestCodec(ceiling).Encode(r); err != limitError {
		t.Fatal("evidence limit-plus-one")
	}
	r.Evidence = r.Evidence[:2]
	r.Evidence[0], r.Evidence[1] = r.Evidence[1], r.Evidence[0]
	if _, _, err := RequestCodec(ceiling).Encode(r); err != bindingError {
		t.Fatal("evidence order")
	}
}

func TestResidencyJSONDepthAndStrings(t *testing.T) {
	for n, depth := range []int{64, 65} {
		data := []byte(strings.Repeat("[", depth) + "0" + strings.Repeat("]", depth))
		err := inspectJSON(data, 4096, 65536, true, nil)
		if n == 0 && err != nil || n == 1 && err != limitError {
			t.Fatalf("depth-%d bound", n)
		}
	}
	for n, data := range []string{`["abc"]`, `["abcd"]`, `["ab","cd"]`} {
		err := inspectJSON([]byte(data), 4096, 3, true, nil)
		if n == 0 && err != nil || n > 0 && err != limitError {
			t.Fatalf("strings-%d bound", n)
		}
	}
}

func TestResidencyPathAndHashConsistency(t *testing.T) {
	r, _ := testRequest(t, []byte("abc"), "text/plain", false)
	for n, path := range []string{strings.Repeat("x", 512), strings.Repeat("x", 513)} {
		r.Subject.Path, r.Capture.Artifact.Asset.Path = path, path
		_, _, err := RequestCodec(ceiling).Encode(r)
		if n == 0 && err != nil || n == 1 && err != limitError {
			t.Fatalf("path-%d bound", n)
		}
	}
	r, _ = testRequest(t, []byte("abc"), "text/plain", false)
	r.Capture.Artifact.TransportReceipt.Path = r.Subject.Path
	if _, _, err := RequestCodec(ceiling).Encode(r); err != bindingError {
		t.Fatal("path-conflict binding")
	}
	r, _ = testRequest(t, []byte("abc"), "text/plain", false)
	r.Capture.Artifact.TransportReceipt.SHA256 = r.Subject.SHA256
	if _, _, err := RequestCodec(ceiling).Encode(r); err != bindingError {
		t.Fatal("size-conflict binding")
	}
}

// TestResidencyCanonicalFixtures checks hand-authored spec bytes against the
// real codecs and engine. Missing files and missing digest pins fail.
func TestResidencyCanonicalFixtures(t *testing.T) {
	fixtures := make(map[string][]byte)
	pins := residencyPins(t)
	objectFiles := make(map[model.Digest]string)
	for name, digest := range pins {
		if strings.HasPrefix(name, "conversion-") || strings.HasPrefix(name, "subject-") || name == "metadata.bytes" || name == "transport-receipt.json" {
			objectFiles[digest] = name
		}
	}
	retainObjects := func(objects []ResolvedObject) {
		for _, object := range objects {
			name, ok := objectFiles[object.Ref.SHA256]
			if !ok {
				t.Fatal("fixtures missing-object-pin")
			}
			fixtures[name] = object.Bytes
		}
	}
	fixtures["conversion-candidate.bytes"] = []byte("candidate")
	fixtures["conversion-block-canonical.json"] = []byte(`["block-v1","paragraph","abc","abc"]`)
	fixtures["conversion-edit-raw.bytes"] = []byte("a\u0301")
	fixtures["conversion-edit-normalized.bytes"] = []byte("á")
	editValue, _, err := (conversion.TextNormalizer{}).Normalize(model.TextInput{
		Text: "a\u0301", Sources: []model.Span{{ArtifactHash: fixtureDigest(fixtures["conversion-edit-raw.bytes"]), Space: model.RawText, Object: "text", Range: &model.ByteRange{Start: 0, End: 3}}},
		Limits: model.RuleLimits{MaxItems: 4096, MaxBytes: 1 << 20},
	})
	if err != nil || len(editValue.EditLedger) != 1 || editValue.Text != string(fixtures["conversion-edit-normalized.bytes"]) {
		t.Fatal("fixtures normalization-edit")
	}
	fixtures["conversion-edit-evidence.json"], err = json.Marshal(editValue.EditLedger[0])
	if err != nil {
		t.Fatal("fixtures normalization-edit-bytes")
	}
	fixtures["evidence-empty.json"] = []byte("[]")
	p := testPolicy()
	policyBytes, _, err := PolicyCodec(ceiling).Encode(p)
	if err != nil {
		t.Fatal("fixtures policy")
	}
	fixtures["residency-policy-v1.json"] = policyBytes
	metadataRequest, metadataObjects := testRequest(t, []byte("abc"), "text/plain", false)
	metadata := []byte("metadata")
	metadataRef := hashRef("metadata", metadata)
	metadataRequest.Evidence = []ScreenEvidence{{"metadata_utf8", metadataRequest.Subject.SHA256, metadataRef, nil}}
	metadataObjects = append(metadataObjects, ResolvedObject{metadataRef, metadata})
	sort.Slice(metadataObjects, func(i, j int) bool { return metadataObjects[i].Ref.SHA256 < metadataObjects[j].Ref.SHA256 })
	fixtures["screen-request-metadata.json"], _, err = RequestCodec(ceiling).Encode(metadataRequest)
	if err != nil {
		t.Fatal("fixtures metadata-request")
	}
	retainObjects(metadataObjects)
	if _, err := testEngine(t).Evaluate(context.Background(), metadataRequest, metadataObjects); err != nil {
		t.Fatal("fixtures metadata-inspection")
	}
	for n, media := range []string{"<>&\u2028\u2029", "á", "a\u0301"} {
		unicodeRequest, unicodeObjects := testRequest(t, []byte("abc"), media, false)
		data, _, err := RequestCodec(ceiling).Encode(unicodeRequest)
		if err != nil {
			t.Fatal("fixtures unicode")
		}
		fixtures[[]string{"screen-request-escaping.json", "screen-request-composed.json", "screen-request-decomposed.json"}[n]] = data
		if _, err := testEngine(t).Evaluate(context.Background(), unicodeRequest, unicodeObjects); err != nil {
			t.Fatal("fixtures unicode-inspection")
		}
	}
	converted, convertedObjects := convertedRequest(t)
	convertedBytes, _, err := RequestCodec(ceiling).Encode(converted)
	if err != nil {
		t.Fatal("fixtures converted-request")
	}
	fixtures["screen-request-converted.json"] = convertedBytes
	convertedReport, err := testEngine(t).Evaluate(context.Background(), converted, convertedObjects)
	if err != nil {
		t.Fatal("fixtures converted-report")
	}
	closedReportCodec, err := ReportCodec(p, ceiling)
	if err != nil {
		t.Fatal("fixtures report-codec")
	}
	fixtures["residency-report-converted.json"], _, err = closedReportCodec.Encode(convertedReport)
	if err != nil {
		t.Fatal("fixtures converted-report-bytes")
	}
	retainObjects(convertedObjects)
	fixtures["evidence-converted.json"], err = json.Marshal(converted.Evidence)
	if err != nil {
		t.Fatal("fixtures converted-evidence")
	}
	for n, subject := range [][]byte{[]byte(baseHTTP), []byte("abc"), []byte("<p>abc</p>")} {
		r, objects := testRequest(t, subject, "text/plain", n == 0)
		name := "captured"
		if n == 0 {
			name = "outbound"
		}
		if n == 2 {
			name, r.MediaType = "html", "text/html"
			r.Capture.Artifact.MediaType = r.MediaType
		}
		requestBytes, _, err := RequestCodec(ceiling).Encode(r)
		if err != nil {
			t.Fatal("fixtures request")
		}
		fixtures["screen-request-"+name+".json"] = requestBytes
		retainObjects(objects)
		e := testEngine(t)
		report, err := e.Evaluate(context.Background(), r, objects)
		if err != nil {
			t.Fatal("fixtures report")
		}
		codec, err := ReportCodec(p, ceiling)
		if err != nil {
			t.Fatal("fixtures codec")
		}
		reportBytes, _, err := codec.Encode(report)
		if err != nil {
			t.Fatal("fixtures report-bytes")
		}
		fixtures["residency-report-"+name+".json"] = reportBytes
		if n == 0 {
			oracle := residencyFixture(t, "screen-request-outbound.json")
			pinned := pins["screen-request-outbound.json"]
			if fixtureDigest(oracle) != pinned || !bytes.Equal(oracle, requestBytes) {
				t.Fatal("fixtures positive-oracle-pin")
			}
			e.proof = func(actual []byte, digest model.Digest) bool {
				return digest == pinned && fixtureDigest(actual) == pinned && bytes.Equal(actual, oracle)
			}
			fixtures["synthetic-proof.json"], err = json.Marshal(struct {
				Version string
				Request model.Digest
			}{"synthetic-residency-proof-v1", pinned})
			if err != nil {
				t.Fatal("fixtures proof-bytes")
			}
			positive, err := e.Evaluate(context.Background(), r, objects)
			if err != nil || positive.AdmissionProof == nil {
				t.Fatal("fixtures positive")
			}
			positiveCodec, err := reportCodec(p, ceiling, true)
			if err != nil {
				t.Fatal("fixtures positive-codec")
			}
			positiveBytes, _, err := positiveCodec.Encode(positive)
			if err != nil {
				t.Fatal("fixtures positive-bytes")
			}
			fixtures["residency-report-public-test.json"] = positiveBytes
		}
	}
	negativeIndex := []struct {
		Case string
		File string
		Kind ErrorKind
	}{
		{"null-array", "negative-null-array.input", inputError},
		{"duplicate-key", "negative-duplicate-key.input", inputError},
		{"surrogate", "negative-surrogate.input", inputError},
		{"unknown", "negative-unknown.input", inputError},
		{"missing", "negative-missing.input", inputError},
		{"reordered", "negative-reordered.input", inputError},
		{"whitespace", "negative-whitespace.input", inputError},
		{"newline", "negative-newline.input", inputError},
		{"trailing", "negative-trailing.input", inputError},
		{"float", "negative-float.input", inputError},
		{"exponent", "negative-exponent.input", inputError},
		{"negative-zero", "negative-zero.input", inputError},
		{"invalid-utf8", "negative-utf8.input", inputError},
		{"overflow", "negative-overflow.input", limitError},
		{"wrong-optional", "negative-optional.input", inputError},
		{"policy-pattern", "negative-policy-pattern.input", policyError},
		{"policy-version", "negative-policy-version.input", policyError},
	}
	fixtures["negatives.json"], err = json.Marshal(negativeIndex)
	if err != nil {
		t.Fatal("fixtures negative-index")
	}
	for _, negative := range negativeIndex {
		data := residencyFixture(t, negative.File)
		var err error
		if strings.HasPrefix(negative.Case, "policy-") {
			value, digest, failure := PolicyCodec(ceiling).Decode(data)
			err = failure
			if !reflect.DeepEqual(value, ResidencyPolicy{}) || digest != "" {
				t.Fatal("fixtures negative-policy-zero")
			}
		} else {
			value, digest, failure := RequestCodec(ceiling).Decode(data)
			err = failure
			if !reflect.DeepEqual(value, ScreenRequest{}) || digest != "" {
				t.Fatal("fixtures negative-request-zero")
			}
		}
		if err != negative.Kind {
			t.Fatal("fixtures negative-class")
		}
	}
	if len(pins) != len(fixtures)+len(negativeIndex) {
		t.Fatal("fixtures pin-coverage")
	}
	for name, expected := range fixtures {
		actual := residencyFixture(t, name)
		if !bytes.Equal(actual, expected) || fixtureDigest(actual) != pins[name] {
			t.Fatal("fixtures retained-bytes")
		}
		if strings.HasSuffix(name, ".json") && bytes.HasSuffix(actual, []byte("\n")) {
			t.Fatal("fixtures final-newline")
		}
		switch {
		case name == "residency-policy-v1.json":
			requireFixtureValue(t, actual, p, PolicyCodec(ceiling), pins[name])
		case strings.HasPrefix(name, "screen-request-"):
			var value ScreenRequest
			if json.Unmarshal(expected, &value) != nil {
				t.Fatal("fixtures request-value")
			}
			requireFixtureValue(t, actual, value, RequestCodec(ceiling), pins[name])
		case strings.HasPrefix(name, "residency-report-"):
			var value ResidencyReport
			if json.Unmarshal(expected, &value) != nil {
				t.Fatal("fixtures report-value")
			}
			codec := closedReportCodec
			if name == "residency-report-public-test.json" {
				closed, digest, failure := closedReportCodec.Decode(actual)
				if failure != bindingError || digest != "" || !reflect.DeepEqual(closed, ResidencyReport{}) {
					t.Fatal("fixtures production-public-refusal")
				}
				codec, err = reportCodec(p, ceiling, true)
				if err != nil {
					t.Fatal("fixtures public-codec")
				}
			}
			requireFixtureValue(t, actual, value, codec, pins[name])
		default:
			if strings.HasSuffix(name, ".bytes") {
				continue
			}
			var value, retained any
			if json.Unmarshal(expected, &value) != nil || json.Unmarshal(actual, &retained) != nil || !reflect.DeepEqual(value, retained) {
				t.Fatal("fixtures auxiliary-value")
			}
		}
	}
	for _, negative := range negativeIndex {
		if fixtureDigest(residencyFixture(t, negative.File)) != pins[negative.File] {
			t.Fatal("fixtures negative-pin")
		}
	}
}

func residencyPins(t *testing.T) map[string]model.Digest {
	t.Helper()
	var index []struct {
		File    string       `json:"file"`
		Digest  model.Digest `json:"digest"`
		Size    int64        `json:"size"`
		Section string       `json:"section"`
	}
	if json.Unmarshal(residencyFixture(t, "digests.json"), &index) != nil {
		t.Fatal("fixtures digest-index")
	}
	pins := make(map[string]model.Digest, len(index))
	for _, entry := range index {
		if _, exists := pins[entry.File]; exists || entry.Digest.Validate() != nil || filepath.Base(entry.File) != entry.File {
			t.Fatal("fixtures duplicate-or-invalid-pin")
		}
		data := residencyFixture(t, entry.File)
		if int64(len(data)) != entry.Size || fixtureDigest(data) != entry.Digest {
			t.Fatal("fixtures independent-size-or-digest")
		}
		if entry.Section != "types" && entry.Section != "results" && entry.Section != "handoff" {
			t.Fatal("fixtures spec-section")
		}
		pins[entry.File] = entry.Digest
	}
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatal("fixtures directory")
	}
	for _, entry := range entries {
		if entry.Name() == "README.md" || entry.Name() == "digests.json" {
			continue
		}
		if _, ok := pins[entry.Name()]; !ok || !entry.Type().IsRegular() {
			t.Fatal("fixtures unpinned-file")
		}
	}
	return pins
}

func fixtureDigest(data []byte) model.Digest {
	sum := sha256.Sum256(data)
	return model.Digest(hex.EncodeToString(sum[:]))
}

func residencyFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal("fixtures missing-file")
	}
	return data
}

func requireFixtureValue[T any](t *testing.T, data []byte, expected T, codec Codec[T], pin model.Digest) {
	t.Helper()
	value, digest, err := codec.Decode(data)
	if err != nil || digest != pin || !reflect.DeepEqual(value, expected) {
		t.Fatal("fixtures decoded-value")
	}
	encoded, digest, err := codec.Encode(value)
	if err != nil || digest != pin || !bytes.Equal(encoded, data) {
		t.Fatal("fixtures re-encoded-value")
	}
}

// convertedRequest authors a complete single-paragraph HTML binding fixture.
// It makes no extraction-quality or source-completeness claim.
func convertedRequest(t *testing.T) (ScreenRequest, []ResolvedObject) {
	t.Helper()
	return convertedRequestMedia(t, "text/html")
}

func convertedRequestMedia(t *testing.T, media string) (ScreenRequest, []ResolvedObject) {
	t.Helper()
	return convertedFixture(t, media, "abc", conversionDamage{})
}

func convertedFixture(t *testing.T, media, text string, damage conversionDamage) (ScreenRequest, []ResolvedObject) {
	t.Helper()
	original := []byte("<body><p>" + text + "</p></body>")
	format := "html"
	var xml []byte
	if media != "text/html" {
		format = "docx"
		xml = []byte(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>` + strings.ReplaceAll(text, "\r", "&#13;") + `</w:t></w:r></w:p></w:body></w:document>`)
		var archive bytes.Buffer
		writer := zip.NewWriter(&archive)
		entry, err := writer.Create("word/document.xml")
		if err != nil {
			t.Fatal("converted docx-entry")
		}
		if _, err := entry.Write(xml); err != nil {
			t.Fatal("converted docx-write")
		}
		if writer.Close() != nil {
			t.Fatal("converted docx-close")
		}
		original = archive.Bytes()
	}
	r, inputs := testRequest(t, original, media, false)
	objects := make(map[model.Digest]ResolvedObject)
	for _, input := range inputs {
		objects[input.Ref.SHA256] = input
	}
	put := func(path string, data []byte) model.AssetRef {
		ref := model.AssetRef{Path: path, SHA256: hash(data), ByteSize: int64(len(data))}
		if old, ok := objects[ref.SHA256]; !ok || path < old.Ref.Path {
			objects[ref.SHA256] = ResolvedObject{ref, data}
		}
		return ref
	}
	value := func(v any) model.AssetRef {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal("converted encode")
		}
		return put("objects/"+string(hash(data)), data)
	}
	recipe := value(struct{}{})
	m := conversion.ConversionManifest{RequestKey: "convert-" + strings.Repeat("5", 32),
		Request:        conversionapi.ConvertRequest{Artifact: r.Capture.Artifact, Recipe: recipe, Budget: r.Budget},
		ObservationKey: "observation-" + strings.Repeat("6", 32), ArtifactRole: "original", Language: "vi",
		HTMLSelector: "body", DetectorHash: recipe.SHA256, CandidateDigest: hash([]byte("candidate")), OutputBytes: 1 << 20}
	if damage.request != nil {
		damage.request(&m)
	}
	mBytes, err := json.Marshal(m)
	if err != nil {
		t.Fatal("converted manifest")
	}
	mRef := put("requests/"+string(hash(mBytes)), mBytes)
	object := func(data []byte) model.AssetRef { return put("objects/"+string(hash(data)), data) }
	textRef := object([]byte(text))
	rawSpan := model.Span{ArtifactHash: textRef.SHA256, Space: model.RawText, Object: "text", Range: &model.ByteRange{Start: 0, End: int64(len(text))}}
	normal, _, err := (conversion.TextNormalizer{}).Normalize(model.TextInput{Text: text, Sources: []model.Span{rawSpan}, Limits: model.RuleLimits{MaxItems: r.Budget.MaxItems, MaxBytes: 1 << 20}})
	if err != nil {
		t.Fatal("converted normalization")
	}
	normalRef := object([]byte(normal.Text))
	canonical, _ := json.Marshal([]string{"block-v1", "paragraph", text, normal.Text})
	id := "block-000000-" + string(hash(canonical))
	ordinal := 2
	sources := []model.Span{{ArtifactHash: r.Subject.SHA256, BlockID: id, Space: model.OriginalBytes, Range: &model.ByteRange{Start: 9, End: 9 + int64(len(text))}, TokenOrdinal: &ordinal}}
	if format == "docx" {
		memberRef := object(xml)
		sources = []model.Span{
			{ArtifactHash: r.Subject.SHA256, BlockID: id, Space: model.OriginalBytes, Object: "paragraph/0", Member: "word/document.xml", TokenOrdinal: &ordinal, ReasonCode: "BYTE_MAPPING_UNAVAILABLE"},
			{ArtifactHash: memberRef.SHA256, BlockID: id, Space: model.DecodedMember, Range: &model.ByteRange{Start: 91, End: int64(bytes.Index(xml, []byte("</w:p>")) + len("</w:p>"))}, Object: "paragraph/0", Member: "word/document.xml", TokenOrdinal: &ordinal},
		}
	}
	block := model.Block{ID: id, Ordinal: 0, Kind: model.BlockParagraph, RawText: text, NormalizedText: normalRef, Language: "vi", Authority: model.AuthorityUnknown, SourceSpans: sources}
	if damage.block != nil {
		damage.block(&block)
	}
	doc := model.ConvertedDocument{SchemaVersion: "blocks-v1", Original: r.Capture.Artifact,
		ObservationKey: m.ObservationKey, ArtifactRole: m.ArtifactRole, RecipeHash: recipe.SHA256,
		CandidateDigest: m.CandidateDigest, RawText: textRef, NormalizedText: normalRef, Blocks: []model.Block{block}}
	if damage.aggregate {
		doc.RawText = object([]byte(strings.Repeat("x", len(text))))
	}
	if damage.normalAggregate {
		doc.NormalizedText = object([]byte(strings.Repeat("x", len(normal.Text))))
	}
	doc.RawBlocks = value([]struct {
		ID      string
		Kind    model.BlockKind
		Text    string
		Sources []model.Span
	}{{block.ID, block.Kind, block.RawText, block.SourceSpans}})
	doc.NormalizedBlocks = value(doc.Blocks)
	maps := []model.OffsetMapSegment{{Sources: block.SourceSpans, Destinations: []model.Span{rawSpan}, Transform: conversion.TransformSourceDecode, Ordinal: 0}}
	for _, segment := range normal.OffsetMap {
		segment.Ordinal = len(maps)
		maps = append(maps, segment)
	}
	var edits []model.EditLedgerEntry
	for _, edit := range normal.EditLedger {
		removed, replacement := object([]byte(edit.Removed)), object([]byte(edit.Replacement))
		edits = append(edits, model.EditLedgerEntry{Kind: edit.Kind, Ordinal: len(edits), Rule: edit.Rule, Version: edit.Version, Inputs: edit.Inputs, Outputs: edit.Outputs, Removed: &removed, Replacement: &replacement, Evidence: value(edit)})
	}
	if damage.chain != nil {
		damage.chain(&maps, &edits)
	}
	doc.OffsetMap = value(maps)
	doc.EditLedger = value(edits)
	detector := value(struct {
		Version  string
		Format   string
		Original model.Digest
		Recipe   model.Digest
	}{"detect-v1", format, r.Subject.SHA256, m.DetectorHash})
	doc.DetectorHash = detector.SHA256
	docBytes, err := json.Marshal(doc)
	if err != nil {
		t.Fatal("converted document")
	}
	prefix := "results/" + string(mRef.SHA256) + "/"
	docRef := put(prefix+"document.json", docBytes)
	q := conversionQuality{SchemaVersion: "conversion-quality-v1", ArtifactHash: r.Subject.SHA256,
		CandidateDigest: m.CandidateDigest, RequestDigest: mRef.SHA256, RecipeHash: recipe.SHA256,
		DetectorHash: detector.SHA256, DocumentHash: docRef.SHA256, Outcome: model.QualityQuarantined,
		Metrics: []model.QualityMetric{
			{Kind: model.MetricGeometry, State: model.MetricUnknown, Mandatory: true, Threshold: "qualification-required", ReasonCode: "BASELINE_UNQUALIFIED"},
			{Kind: model.MetricFidelity, State: model.MetricUnknown, Mandatory: true, Threshold: "qualification-required", ReasonCode: "BASELINE_UNQUALIFIED"},
		}, Findings: []model.Finding{
			{ReasonCode: "GEOMETRY_UNKNOWN", Severity: model.SeverityBlocking, Scope: model.GapArtifact, ScopeKey: string(r.Subject.SHA256)},
			{ReasonCode: "FIDELITY_UNQUALIFIED", Severity: model.SeverityBlocking, Scope: model.GapArtifact, ScopeKey: string(r.Subject.SHA256)},
		}}
	if damage.quality != nil {
		damage.quality(&q)
	}
	qBytes, err := json.Marshal(q)
	if err != nil {
		t.Fatal("converted quality")
	}
	put(prefix+"quality.json", qBytes)
	if damage.unused {
		object([]byte("unused"))
	}
	var members []model.AssetRef
	for _, item := range objects {
		if item.Ref != r.Subject && item.Ref != r.Capture.Artifact.TransportReceipt && item.Ref != recipe && item.Ref != mRef {
			members = append(members, item.Ref)
		}
	}
	sort.Slice(members, func(i, j int) bool { return members[i].Path < members[j].Path })
	outputBytes, err := json.Marshal(members)
	if err != nil {
		t.Fatal("converted outputs")
	}
	if damage.outputs != nil {
		outputBytes = damage.outputs(outputBytes)
	}
	outputs := object(outputBytes)
	r.Evidence = []ScreenEvidence{{Kind: "converted_raw_utf8", SubjectHash: r.Subject.SHA256, Asset: doc.RawText, Conversion: &ConversionEvidence{mRef, docRef, outputs}}}
	resolved := make([]ResolvedObject, 0, len(objects))
	for _, object := range objects {
		resolved = append(resolved, object)
	}
	sort.Slice(resolved, func(i, j int) bool { return resolved[i].Ref.SHA256 < resolved[j].Ref.SHA256 })
	return r, resolved
}
