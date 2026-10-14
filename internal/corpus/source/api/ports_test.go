package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"danny.vn/banhmi/internal/corpus/model"
)

func TestSourceAPIBoundary(t *testing.T) {
	files, err := parser.ParseDir(token.NewFileSet(), ".", func(f os.FileInfo) bool { return !strings.HasSuffix(f.Name(), "_test.go") }, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range files {
		for name, f := range p.Files {
			for _, imp := range f.Imports {
				path, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(path, ".") && path != "danny.vn/banhmi/internal/corpus/model" && path != "danny.vn/banhmi/internal/corpus/run" {
					t.Errorf("%s imports implementation or external dependency %s", name, path)
				}
			}
		}
	}
}

func TestFactoryValidationDoesNotInvokeConstructors(t *testing.T) {
	f := Factory{
		SourceID: "source",
		NewPlanner: func() Planner {
			t.Fatal("planner invoked")
			return nil
		},
		NewDecoder: func() Decoder {
			t.Fatal("decoder invoked")
			return nil
		},
	}
	if err := f.Validate(); err != nil {
		t.Fatal(err)
	}
	f.NewDecoder = nil
	if f.Validate() == nil {
		t.Fatal("incomplete factory accepted")
	}
	if DiscoveryMode("unknown").Valid() || NumberMatchKind("unknown").Valid() {
		t.Fatal("unknown discovery mode accepted")
	}
}

func TestEnvelopeSentinels(t *testing.T) {
	hash := model.Digest(strings.Repeat("a", 64))
	for _, envelope := range []interface{ Validate() error }{
		RequestEnvelope{Key: "request-" + strings.Repeat("a", 32), Manifest: hash},
		DecoderEnvelope{Key: "decoder-" + strings.Repeat("a", 32), Manifest: hash},
	} {
		if err := envelope.Validate(); err != nil {
			t.Fatal(err)
		}
		if _, err := json.Marshal(envelope); err != nil {
			t.Fatal(err)
		}
		typ := reflect.TypeOf(envelope)
		if typ.NumField() != 2 || typ.Field(0).Name != "Key" || typ.Field(1).Name != "Manifest" {
			t.Fatalf("unexpected transport payload shape: %s", typ)
		}
		for _, sentinel := range []string{"Jane.Doe", "synthetic.person@example.invalid", "Jane Doe", "people/Jane.Doe", "https://example.invalid/person/Jane.Doe?email=synthetic.person@example.invalid", "/tmp/Jane.Doe"} {
			for field := 0; field < typ.NumField(); field++ {
				changed := reflect.New(typ).Elem()
				changed.Set(reflect.ValueOf(envelope))
				changed.Field(field).SetString(sentinel)
				invalid := changed.Interface().(interface{ Validate() error })
				err := invalid.Validate()
				if err == nil || strings.Contains(err.Error(), sentinel) {
					t.Fatalf("%s field %d accepted or echoed sentinel", typ, field)
				}
				encoded, err := json.Marshal(invalid)
				if err == nil || len(encoded) != 0 || strings.Contains(err.Error(), sentinel) {
					t.Fatalf("%s field %d serialized or echoed sentinel", typ, field)
				}
			}
		}
	}
}

func sourceFixture() (model.Digest, model.ResourceBudget, model.CaptureRef) {
	hash := model.Digest(strings.Repeat("a", 64))
	budget := model.ResourceBudget{MaxItems: 2, MaxInputBytes: 100, MaxPages: 1, MaxElapsed: time.Second, MaxConcurrent: 1}
	asset := model.AssetRef{Path: "objects/capture", SHA256: hash, ByteSize: 1}
	capture := model.CaptureRef{Country: "vn", SourceID: "source", CaptureKey: "capture", Asset: asset, MediaType: "text/html", RequestHash: hash, ResponseHash: hash, TransportReceipt: asset}
	return hash, budget, capture
}

func TestRequestOriginAndFrozenBudget(t *testing.T) {
	hash, budget, capture := sourceFixture()
	detail := DetailRequest{Country: "vn", SourceID: "source", ExternalID: "native/identifier", ListingCaptures: []model.CaptureRef{capture}, WindowKey: "window", PolicyHash: hash, BudgetHash: hash, Budget: budget}
	artifact := ArtifactRequest{Country: "vn", SourceID: "source", Artifact: model.ArtifactInventoryEntry{ArtifactKey: "artifact", Role: "body", URL: "https://example.invalid/file"}, ParentObservation: "observation", WindowKey: "window", PolicyHash: hash, BudgetHash: hash, Budget: budget}
	for _, request := range []interface{ Validate() error }{detail, artifact} {
		if err := request.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for _, mutate := range []func(*DetailRequest){func(r *DetailRequest) { r.WindowKey = "" }, func(r *DetailRequest) { r.BudgetHash = "" }} {
		changed := detail
		mutate(&changed)
		if changed.Validate() == nil {
			t.Fatal("detail accepted missing origin")
		}
	}
	for _, mutate := range []func(*ArtifactRequest){func(r *ArtifactRequest) { r.WindowKey = "" }, func(r *ArtifactRequest) { r.BudgetHash = "" }} {
		changed := artifact
		mutate(&changed)
		if changed.Validate() == nil {
			t.Fatal("artifact accepted missing origin")
		}
	}
	plan := RequestPlan{Country: "vn", SourceID: "source", RequestKey: "request", WindowKey: detail.WindowKey, Purpose: model.PurposeDetail, URL: "https://example.invalid/item", Method: "GET", PolicyHash: hash, BudgetHash: detail.BudgetHash, Budget: detail.Budget}
	if err := plan.ValidateOrigin(detail.WindowKey, detail.BudgetHash, detail.Budget); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*RequestPlan){
		func(r *RequestPlan) { r.WindowKey = "other-window" },
		func(r *RequestPlan) { r.BudgetHash = model.Digest(strings.Repeat("b", 64)) },
		func(r *RequestPlan) { r.Budget.MaxInputBytes++ },
	} {
		changed := plan
		mutate(&changed)
		if changed.ValidateOrigin(detail.WindowKey, detail.BudgetHash, detail.Budget) == nil {
			t.Fatal("plan changed original window or frozen allowance")
		}
	}
}

type decodedIdentityRules struct{}

func (decodedIdentityRules) Classify(c IdentityContext) (DocumentType, []model.Finding) {
	return DocumentType{Key: c.Type, Evidence: c.Fields}, nil
}

func (decodedIdentityRules) ParseNumber(i NumberInput) (NumberMatch, []model.Finding) {
	return NumberMatch{Kind: NumberExact, Candidates: []NumberCandidate{{Exact: i.Text, NormalizedKey: i.Text, Namespace: i.Context.Type, Source: i.Sources[0]}}}, nil
}

func TestIdentityRulesReceiveDecodedValues(t *testing.T) {
	hash, _, _ := sourceFixture()
	span := model.Span{ArtifactHash: hash, Space: model.RawText, Range: &model.ByteRange{Start: 0, End: 1}}
	input := NumberInput{Text: "50/2024/TT-NHNN", Sources: []model.Span{span}, Context: IdentityContext{Title: "Synthetic title", Issuer: "Synthetic issuer", Type: "circular", Fields: []model.Span{span}, Limits: model.RuleLimits{MaxItems: 10, MaxBytes: 1000}}}
	if err := input.Validate(); err != nil {
		t.Fatal(err)
	}
	var rules IdentityRules = decodedIdentityRules{}
	result, _ := rules.ParseNumber(input)
	if result.Candidates[0].Exact != input.Text || result.Candidates[0].Namespace != input.Context.Type || result.Candidates[0].Source != span {
		t.Fatal("pure rule lost computed values or provenance")
	}
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
	input.Context.Limits.MaxBytes = 1
	if input.Validate() == nil {
		t.Fatal("unbounded number input accepted")
	}
}

func TestCapturedInputVerifiesMountedBytes(t *testing.T) {
	_, budget, capture := sourceFixture()
	bytes := []byte("x")
	digest := sha256.Sum256(bytes)
	capture.Asset.SHA256 = model.Digest(hex.EncodeToString(digest[:]))
	input := CapturedInput{Capture: capture, Content: bytes, Budget: budget}
	if err := input.Validate(); err != nil {
		t.Fatal(err)
	}
	input.Content = []byte("y")
	if input.Validate() == nil {
		t.Fatal("changed mounted bytes accepted")
	}
	input.Content = []byte("xx")
	if input.Validate() == nil {
		t.Fatal("wrong byte count accepted")
	}
}

func TestExportedContractDocumentation(t *testing.T) {
	for _, name := range []string{"ports.go"} {
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		for _, declaration := range file.Decls {
			switch declaration := declaration.(type) {
			case *ast.FuncDecl:
				if declaration.Name.IsExported() && declaration.Doc == nil {
					t.Errorf("%s: exported method %s lacks documentation", name, declaration.Name.Name)
				}
			case *ast.GenDecl:
				for _, spec := range declaration.Specs {
					if value, ok := spec.(*ast.ValueSpec); ok {
						for _, valueName := range value.Names {
							if valueName.IsExported() && value.Doc == nil && (declaration.Lparen.IsValid() || declaration.Doc == nil) {
								t.Errorf("%s: exported value %s lacks documentation", name, valueName.Name)
							}
						}
						continue
					}
					typ, ok := spec.(*ast.TypeSpec)
					if !ok || !typ.Name.IsExported() {
						continue
					}
					if typ.Doc == nil && declaration.Doc == nil {
						t.Errorf("%s: exported type %s lacks documentation", name, typ.Name.Name)
					}
					if contract, ok := typ.Type.(*ast.InterfaceType); ok {
						for _, method := range contract.Methods.List {
							if len(method.Names) > 0 && method.Names[0].IsExported() && method.Doc == nil {
								t.Errorf("%s: interface method %s lacks documentation", name, method.Names[0].Name)
							}
						}
					}
				}
			}
		}
	}
}
