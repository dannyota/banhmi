package quality

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/quality/api"
	"danny.vn/banhmi/internal/corpus/quality/formats"
)

func fixtureGate(t *testing.T) (*Gate, api.GateRequest, ConversionRefs) {
	t.Helper()
	data, err := os.ReadFile("formats/testdata/policy-bundle-v1.json")
	if err != nil {
		t.Fatal("policy fixture unavailable")
	}
	policy, _, err := formats.PolicyBundleCodec(formats.DefaultLimits()).Decode(data)
	if err != nil {
		t.Fatal("policy fixture invalid")
	}
	g, err := NewGate(Options{Policy: policy, Limits: formats.DefaultLimits()})
	if err != nil {
		t.Fatal("constructor rejected fixture")
	}
	original := model.Digest(strings.Repeat("a", 64))
	candidate := model.Digest(strings.Repeat("b", 64))
	span := model.Span{ArtifactHash: original, BlockID: "block-one", Space: model.OriginalBytes, Range: &model.ByteRange{Start: 0, End: 300}}
	limits := model.RuleLimits{MaxItems: 10000, MaxBytes: 4194304}
	request := api.GateRequest{
		Document: model.DecodedDocument{
			OriginalHash:    original,
			CandidateDigest: candidate,
			Limits:          limits,
			Blocks:          []model.DecodedBlock{{ID: "block-one", Kind: model.BlockParagraph, RawText: strings.Repeat("á", 60), NormalizedText: strings.Repeat("á", 60), Language: "vi", Authority: model.AuthorityUnknown, SourceSpans: []model.Span{span}}},
		},
		Parts:    model.DecodedPartResult{Limits: limits},
		Profile:  model.QualityMetricProfile{
			PolicyHash: g.profile.PolicyHash,
			Metrics:    append([]model.MetricRequirement{}, g.profile.Metrics...),
		},
	}
	refs := ConversionRefs{
		RequestManifest: model.AssetRef{Path: "requests/one", SHA256: model.Digest(strings.Repeat("c", 64)), ByteSize: 1},
		Document:        model.AssetRef{Path: "results/one", SHA256: model.Digest(strings.Repeat("d", 64)), ByteSize: 1},
		OutputManifest:  model.AssetRef{Path: "manifests/one", SHA256: model.Digest(strings.Repeat("e", 64)), ByteSize: 1},
	}
	return g, request, refs
}

func TestMetricLimitsProvenanceRangeContainment(t *testing.T) {
	for _, c := range []struct {
		id      string
		sources []model.ByteRange
		queries []model.ByteRange
		want    bool
	}{
		{"exact", []model.ByteRange{{Start: 0, End: 10}}, []model.ByteRange{{Start: 0, End: 10}}, true},
		{"partial", []model.ByteRange{{Start: 0, End: 10}}, []model.ByteRange{{Start: 1, End: 9}}, true},
		{"overflow", []model.ByteRange{{Start: 0, End: 10}}, []model.ByteRange{{Start: 1, End: 11}}, false},
		{"adjacent", []model.ByteRange{{Start: 0, End: 10}, {Start: 10, End: 20}}, []model.ByteRange{{Start: 5, End: 15}}, false},
		{"gap", []model.ByteRange{{Start: 0, End: 10}, {Start: 20, End: 30}}, []model.ByteRange{{Start: 11, End: 19}}, false},
		{"unordered", []model.ByteRange{{Start: 256, End: 300}, {Start: 0, End: 10}}, []model.ByteRange{{Start: 256, End: 299}, {Start: 1, End: 10}}, true},
		{"equal-start", []model.ByteRange{{Start: 5, End: 6}, {Start: 5, End: 100}}, []model.ByteRange{{Start: 5, End: 100}}, true},
		{"high-bits", []model.ByteRange{{Start: 1 << 60, End: 1<<60 + 10}}, []model.ByteRange{{Start: 1<<60 + 1, End: 1<<60 + 9}}, true},
	} {
		t.Run(c.id, func(t *testing.T) {
			if rangesCovered(c.sources, c.queries) != c.want {
				t.Fatal("provenance containment mismatch")
			}
		})
	}
}

func TestGatePinnedProfile(t *testing.T) {
	for _, id := range []string{"optional", "order", "hash", "threshold", "inapplicable", "missing", "extra"} {
		t.Run(id, func(t *testing.T) {
			g, request, _ := fixtureGate(t)
			switch id {
			case "optional":
				for i := range request.Profile.Metrics {
					request.Profile.Metrics[i].Mandatory = false
				}
			case "order":
				request.Profile.Metrics[0], request.Profile.Metrics[1] = request.Profile.Metrics[1], request.Profile.Metrics[0]
			case "hash":
				request.Profile.PolicyHash = request.Document.OriginalHash
			case "threshold":
				request.Profile.Metrics[0].Threshold = "min:0"
			case "inapplicable":
				request.Profile.Metrics[0].AllowNotApplicable = true
			case "missing":
				request.Profile.Metrics = request.Profile.Metrics[:1]
			case "extra":
				request.Profile.Metrics = append(request.Profile.Metrics, request.Profile.Metrics[0])
			}
			request.Document.Blocks = nil
			request.Document.Limits.MaxItems = 0
			got, err := g.Evaluate(request)
			if err != errPolicy || !reflect.DeepEqual(got, api.QualityEvaluation{}) {
				t.Fatal("forged profile did not fail first with zero result")
			}
		})
	}
}

func TestGateDeterminismAndIsolation(t *testing.T) {
	g, request, refs := fixtureGate(t)
	first, err := g.Evaluate(request)
	if err != nil || first.Outcome != model.QualityQuarantined {
		t.Fatal("baseline did not quarantine")
	}
	for _, metric := range first.Metrics {
		if metric.State != model.MetricUnknown || metric.Passed || metric.Numerator != 0 || metric.Denominator != 0 || metric.Evidence == nil || metric.Exclusions == nil {
			t.Fatal("unknown metric contract failed")
		}
	}
	second, err := g.Evaluate(request)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatal("evaluation is not deterministic")
	}
	first.Outcome = model.QualityAccepted
	first.Metrics[0].Passed = true
	first.Findings[0].ReasonCode = "ALTERED"
	report, err := g.Report(request, refs)
	if err != nil || report.Artifact.Outcome != model.QualityQuarantined || report.Artifact.Metrics[0].Passed || report.Artifact.Findings[0].ReasonCode != "FIDELITY_UNQUALIFIED" {
		t.Fatal("report consumed altered evaluation")
	}
	encoded, digest, err := g.codec.Encode(report)
	if err != nil {
		t.Fatal("report codec rejected baseline")
	}
	decoded, secondDigest, err := g.codec.Decode(encoded)
	if err != nil || digest != secondDigest || !reflect.DeepEqual(report, decoded) {
		t.Fatal("non-null report round trip failed")
	}
	again, err := g.Report(request, refs)
	if err != nil {
		t.Fatal("repeat report failed")
	}
	other, _, err := g.codec.Encode(again)
	if err != nil || string(other) != string(encoded) {
		t.Fatal("report bytes are not deterministic")
	}
}

func classifiedFixture(request *api.GateRequest) {
	span := request.Document.Blocks[0].SourceSpans[0]
	request.Parts.Namespaces = []model.DecodedNamespace{{Key: "body", Kind: model.PartBody, Label: "Nội dung", Boundary: span}}
	request.Parts.ClassifiedRanges = []model.ClassifiedRange{{Kind: model.PartBody, Namespace: "body", ArtifactHash: request.Document.OriginalHash, BlockIDs: []string{"block-one"}, Spans: []model.Span{span}}}
}

func TestMetricLimitsInvalidInputs(t *testing.T) {
	cases := []string{"utf8", "parent", "cycle", "foreign", "foreign-original-span", "source-block", "unknown-block", "duplicate-owner", "namespace", "namespace-parent", "namespace-cycle", "exclusion-hash", "exclusion-range", "exclusion-member", "declaration-items", "declaration-bytes", "nested-items", "nested-bytes"}
	for _, id := range cases {
		t.Run(id, func(t *testing.T) {
			g, request, _ := fixtureGate(t)
			classifiedFixture(&request)
			switch id {
			case "utf8":
				request.Document.Blocks[0].NormalizedText = string([]byte{0xff})
			case "parent":
				request.Document.Blocks[0].ParentID = "missing"
			case "cycle":
				request.Document.Blocks[0].ParentID = "block-one"
			case "foreign":
				request.Parts.ClassifiedRanges[0].ArtifactHash = request.Document.CandidateDigest
			case "foreign-original-span":
				request.Document.Blocks[0].SourceSpans[0].ArtifactHash = request.Document.CandidateDigest
			case "source-block":
				request.Document.Blocks[0].SourceSpans[0].BlockID = "missing"
			case "unknown-block":
				request.Parts.ClassifiedRanges[0].BlockIDs[0] = "missing"
			case "duplicate-owner":
				request.Parts.ClassifiedRanges = append(request.Parts.ClassifiedRanges, request.Parts.ClassifiedRanges[0])
			case "namespace":
				request.Parts.ClassifiedRanges[0].Namespace = "missing"
			case "namespace-parent":
				request.Parts.Namespaces[0].ParentKey = "missing"
			case "namespace-cycle":
				request.Parts.Namespaces[0].ParentKey = "other"
				n := request.Parts.Namespaces[0]
				n.Key, n.ParentKey = "other", "body"
				request.Parts.Namespaces = append(request.Parts.Namespaces, n)
			case "exclusion-hash", "exclusion-range", "exclusion-member":
				span := request.Document.Blocks[0].SourceSpans[0]
				span.Range = &model.ByteRange{Start: 1, End: 2}
				if id == "exclusion-hash" {
					span.ArtifactHash = request.Document.CandidateDigest
				} else if id == "exclusion-range" {
					span.Range.End = 301
				} else {
					span.Member = "other.xml"
				}
				request.Parts.Exclusions = []model.ExcludedRange{{Span: span, ReasonCode: "EXCLUDED"}}
			case "declaration-items":
				request.Document.Limits.MaxItems++
			case "declaration-bytes":
				request.Parts.Limits.MaxBytes++
			case "nested-items":
				request.Parts.Limits.MaxItems = 2
			case "nested-bytes":
				request.Parts.Limits.MaxBytes = 1
			}
			got, err := g.Evaluate(request)
			if err != errInput || !reflect.DeepEqual(got, api.QualityEvaluation{}) {
				t.Fatal("invalid input did not return fixed error and zero evaluation")
			}
		})
	}
}

func TestMetricLimitsNonNFCAndExclusions(t *testing.T) {
	g, request, _ := fixtureGate(t)
	request.Document.Blocks[0].NormalizedText = "a\u0301"
	span := request.Document.Blocks[0].SourceSpans[0]
	span.Range = &model.ByteRange{Start: 10, End: 20}
	request.Parts.Exclusions = []model.ExcludedRange{{Span: span, ReasonCode: "PARTIAL"}}
	got, err := g.Evaluate(request)
	if err != nil || got.Outcome != model.QualityQuarantined {
		t.Fatal("partial exclusion evaluation failed")
	}
	codes := []string{}
	for _, f := range got.Findings {
		codes = append(codes, f.ReasonCode)
	}
	want := []string{"FIDELITY_UNQUALIFIED", "GEOMETRY_UNKNOWN", "TEXT_NOT_NFC", "TEXT_TOO_FEW_LETTERS", "EXCLUSIONS_UNSUPPORTED"}
	if !reflect.DeepEqual(codes, want) || request.Document.Blocks[0].NormalizedText != "a\u0301" {
		t.Fatal("finding order or input preservation failed")
	}
	last := got.Findings[len(got.Findings)-1]
	if !reflect.DeepEqual(last.Spans, []model.Span{span}) || len(got.Metrics[0].Exclusions) != 0 {
		t.Fatal("unsupported exclusion was applied or rewritten")
	}
	last.Spans[0].Range.End = 19
	if request.Parts.Exclusions[0].Span.Range.End != 20 {
		t.Fatal("output span aliases input")
	}
}

func TestMetricLimitsExactInputBounds(t *testing.T) {
	g, request, _ := fixtureGate(t)
	// One block plus one span. Every document string contributes to MaxBytes.
	b := request.Document.Blocks[0]
	s := b.SourceSpans[0]
	var size int64
	for _, value := range []string{string(request.Document.OriginalHash), string(request.Document.CandidateDigest), b.ID, b.ParentID, string(b.Kind), b.RawText, b.NormalizedText, b.Language, string(b.Authority), string(s.ArtifactHash), s.BlockID, string(s.Space), s.Object, s.Member, s.ReasonCode} {
		size += int64(len(value))
	}
	request.Document.Limits = model.RuleLimits{MaxItems: 2, MaxBytes: size}
	if _, err := g.Evaluate(request); err != nil {
		t.Fatal("exact input limit rejected")
	}
	request.Document.Blocks[0].NormalizedText += "a"
	if _, err := g.Evaluate(request); err != errInput {
		t.Fatal("input limit plus one accepted")
	}
	request.Document.Blocks[0] = b
	request.Document.Blocks[0].SourceSpans = append([]model.Span{s}, s)
	if _, err := g.Evaluate(request); err != errInput {
		t.Fatal("nested item limit plus one accepted")
	}
}

type unqualifiedLanguage struct{}

func (unqualifiedLanguage) Inspect(model.DecodedDocument) ([]model.QualityMetric, []model.Finding) {
	return nil, nil
}

func (unqualifiedLanguage) Normalize(model.TextInput) (model.MappedValue, []model.Finding) {
	return model.MappedValue{}, nil
}

func TestGateConstructorPinsAndRejects(t *testing.T) {
	g, request, refs := fixtureGate(t)
	policy := g.policy
	options := Options{Policy: policy, Limits: g.limits}
	copy, err := NewGate(options)
	if err != nil {
		t.Fatal("copy constructor failed")
	}
	options.Policy.ArtifactProfile[0].Mandatory = false
	options.Policy.RollupProfile[0].Threshold = "max:1"
	options.Policy.GapReasonCodes[0] = "ALTERED"
	report, err := copy.Report(request, refs)
	if err != nil || !report.Artifact.Metrics[0].Mandatory || copy.policy.RollupProfile[0].Threshold != "qualification-required" || copy.policy.GapReasonCodes[0] != "CONVERSION_MISSING" {
		t.Fatal("constructor retained caller configuration aliases")
	}
	for _, id := range []string{"language", "typed-nil-language", "limits", "artifact-threshold", "rollup-threshold", "optional-geometry", "optional-fidelity"} {
		t.Run(id, func(t *testing.T) {
			base, _, _ := fixtureGate(t)
			options := Options{Policy: base.policy, Limits: base.limits}
			switch id {
			case "language":
				options.Language = unqualifiedLanguage{}
			case "typed-nil-language":
				var provider *unqualifiedLanguage
				options.Language = provider
			case "limits":
				options.Limits.MaxRows = 0
			case "artifact-threshold":
				options.Policy.ArtifactProfile[0].Threshold = "min:0.50"
			case "rollup-threshold":
				options.Policy.RollupProfile[0].Threshold = "min:2"
			case "optional-geometry":
				options.Policy.ArtifactProfile[1].Mandatory = false
			case "optional-fidelity":
				options.Policy.RollupProfile[0].Mandatory = false
			}
			if got, err := NewGate(options); err != errPolicy || got != nil {
				t.Fatal("invalid constructor options accepted")
			}
		})
	}
}

func TestGateEffectiveLimits(t *testing.T) {
	base, request, _ := fixtureGate(t)
	policy := base.policy
	policy.AcceptanceLimits.MaxRows = 100
	policy.AcceptanceLimits.MaxObjectBytes = 100000
	caller := base.limits
	caller.MaxBlobs = 10
	caller.MaxJSONBytes = 100000
	caller.MaxElapsedMS = 500
	g, err := NewGate(Options{Policy: policy, Limits: caller})
	if err != nil || g.limits.MaxRows != 100 || g.limits.MaxObjectBytes != 100000 || g.limits.MaxBlobs != 10 || g.limits.MaxJSONBytes != 100000 || g.limits.MaxElapsedMS != 500 {
		t.Fatal("effective limits are not the fieldwise minimum")
	}
	request.Profile = g.profile
	request.Document.Limits = model.RuleLimits{MaxItems: 100, MaxBytes: 100000}
	request.Parts.Limits = request.Document.Limits
	if _, err := g.Evaluate(request); err != nil {
		t.Fatal("effective declaration limit rejected")
	}
	request.Parts.Limits.MaxItems++
	if _, err := g.Evaluate(request); err != errInput {
		t.Fatal("policy declaration cap plus one accepted")
	}
}

func TestGatePureBoundary(t *testing.T) {
	allowed := func(path string) bool {
		switch path {
		case "errors", "strings", "reflect", "strconv", "unicode", "unicode/utf8", "math/bits",
			"golang.org/x/text/unicode/norm",
			"danny.vn/banhmi/internal/corpus/model",
			"danny.vn/banhmi/internal/corpus/quality/api",
			"danny.vn/banhmi/internal/corpus/quality/formats",
			"danny.vn/banhmi/internal/corpus/conversion/api":
			return true
		}
		return false
	}
	for _, name := range []string{"gate.go", "metrics.go", "thresholds.go", "findings.go", "textsignals.go", "report.go"} {
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatal("pure boundary source invalid")
		}
		for _, imp := range file.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil || !allowed(path) {
				t.Fatal("pure boundary import violation")
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			if function, ok := node.(*ast.FuncDecl); ok && function.Name.Name == "init" {
				t.Fatal("pure boundary contains init")
			}
			if _, ok := node.(*ast.GoStmt); ok {
				t.Fatal("pure boundary contains goroutine")
			}
			return true
		})
	}
}
