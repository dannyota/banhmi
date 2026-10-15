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

func TestIdentityInputsShareFrozenBounds(t *testing.T) {
	hash, _, _ := sourceFixture()
	span := model.Span{ArtifactHash: hash, Space: model.RawText, Range: &model.ByteRange{End: 1}}
	context := IdentityContext{Title: "x", Fields: []model.Span{span}, Limits: model.RuleLimits{MaxItems: 2, MaxBytes: 1000}}
	input := NumberInput{Text: "x", Sources: []model.Span{span}, Context: context}
	if err := input.Validate(); err != nil {
		t.Fatal(err)
	}
	input.Context.Limits.MaxItems = 1
	if err := input.Validate(); err == nil || !strings.Contains(err.Error(), "item bound") {
		t.Fatalf("number and context provenance did not share allowance: %v", err)
	}
	input.Context.Limits.MaxItems = 2
	input.Context.Limits.MaxBytes = int64(2 + 2*(len(hash)+len(model.RawText)))
	if err := input.Validate(); err != nil {
		t.Fatal(err)
	}
	input.Context.Limits.MaxBytes--
	if err := input.Validate(); err == nil || !strings.Contains(err.Error(), "byte bound") {
		t.Fatalf("number and context bytes did not share allowance: %v", err)
	}
	span.Member = strings.Repeat("x", 1<<20)
	if err := span.Validate(); err != nil {
		t.Fatal(err)
	}
	context.Fields = []model.Span{span}
	for _, limit := range []int64{1, 1000} {
		context.Limits.MaxBytes = limit
		if err := context.Validate(); err == nil || !strings.Contains(err.Error(), "byte bound") {
			t.Fatalf("oversized context provenance accepted: %v", err)
		}
		input.Context = IdentityContext{Fields: []model.Span{{ArtifactHash: hash, Space: model.RawText, Range: &model.ByteRange{End: 1}}}, Limits: context.Limits}
		input.Sources = []model.Span{span}
		if err := input.Validate(); err == nil || !strings.Contains(err.Error(), "byte bound") {
			t.Fatalf("oversized number provenance accepted: %v", err)
		}
	}
}

func TestIdentityInputsCheckLengthsBeforeUTF8(t *testing.T) {
	hash, _, _ := sourceFixture()
	span := model.Span{ArtifactHash: hash, Space: model.RawText, Range: &model.ByteRange{End: 1}}
	context := IdentityContext{Fields: []model.Span{span}, Limits: model.RuleLimits{MaxItems: 2, MaxBytes: 1000}}
	oversized := strings.Repeat("x", 1<<20) + "\xff"
	context.Title = oversized
	if err := context.Validate(); err == nil || !strings.Contains(err.Error(), "byte bound") || strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("context scanned encoding before length: %v", err)
	}
	context.Title = ""
	input := NumberInput{Text: oversized, Sources: []model.Span{span}, Context: context}
	if err := input.Validate(); err == nil || !strings.Contains(err.Error(), "byte bound") || strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("number scanned encoding before length: %v", err)
	}
}

func TestContinuationRequestValidation(t *testing.T) {
	hash := model.Digest(strings.Repeat("a", 64))
	budgetHash := model.Digest(strings.Repeat("b", 64))
	budget := model.ResourceBudget{MaxItems: 2, MaxInputBytes: 1000, MaxPages: 3, MaxElapsed: time.Minute, MaxConcurrent: 1}
	origin := RequestPlan{Country: "vn", SourceID: "source", RequestKey: "request", WindowKey: "window", Purpose: model.PurposeListing, URL: "https://example.org/list", Method: "GET", Cursor: "1", PolicyHash: hash, BudgetHash: budgetHash, Budget: budget}
	capture := model.CaptureRef{Country: "vn", SourceID: "source", CaptureKey: "capture", Asset: model.AssetRef{Path: "capture/list", SHA256: hash}, MediaType: "text/html", RequestHash: hash, ResponseHash: hash, TransportReceipt: model.AssetRef{Path: "capture/receipt", SHA256: hash}}
	request := ContinuationRequest{Origin: origin, WindowKey: "window", PolicyHash: hash, BudgetHash: budgetHash, Budget: budget, Mode: DiscoveryIncremental, ParentCapture: capture, Target: "ctrl$grid", Fields: []FormField{{Name: "__STATE", Value: "state+&="}}}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*ContinuationRequest){
		"window": func(r *ContinuationRequest) {
			r.WindowKey = "other"
		},
		"policy": func(r *ContinuationRequest) {
			r.PolicyHash = budgetHash
		},
		"budget hash": func(r *ContinuationRequest) {
			r.BudgetHash = hash
		},
		"items": func(r *ContinuationRequest) {
			r.Budget.MaxItems++
		},
		"bytes": func(r *ContinuationRequest) {
			r.Budget.MaxInputBytes++
		},
		"pages": func(r *ContinuationRequest) {
			r.Budget.MaxPages++
		},
		"elapsed": func(r *ContinuationRequest) {
			r.Budget.MaxElapsed++
		},
		"concurrent": func(r *ContinuationRequest) {
			r.Budget.MaxConcurrent++
		},
		"mode": func(r *ContinuationRequest) {
			r.Mode = "unknown"
		},
		"purpose": func(r *ContinuationRequest) {
			r.Origin.Purpose = model.PurposeDetail
		},
		"capture": func(r *ContinuationRequest) {
			r.ParentCapture.CaptureKey = ""
		},
		"country": func(r *ContinuationRequest) {
			r.ParentCapture.Country = "my"
		},
		"source": func(r *ContinuationRequest) {
			r.ParentCapture.SourceID = "other"
		},
		"count": func(r *ContinuationRequest) {
			r.Fields = make([]FormField, 3)
		},
		"field bytes": func(r *ContinuationRequest) {
			r.Fields = []FormField{{Name: "state", Value: strings.Repeat("x", 1000)}}
		},
		"target bytes": func(r *ContinuationRequest) {
			r.Target = strings.Repeat("x", 1001)
		},
		"invalid UTF-8": func(r *ContinuationRequest) {
			r.Fields = []FormField{{Name: "state", Value: string([]byte{0xff})}}
		},
		"duplicate": func(r *ContinuationRequest) {
			r.Fields = []FormField{{Name: "state"}, {Name: "state"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := request
			mutate(&bad)
			if err := bad.Validate(); err == nil {
				t.Fatal("invalid continuation accepted")
			}
		})
	}
	for _, key := range []string{"", "../state", "https://example.org", "x&y", "x=y", "x%24y", "x y", "x\ny", "đ"} {
		bad := request
		bad.Fields = []FormField{{Name: key}}
		if err := bad.Validate(); err == nil {
			t.Fatalf("invalid key accepted: %q", key)
		}
		bad = request
		bad.Target = key
		if err := bad.Validate(); err == nil {
			t.Fatalf("invalid target accepted: %q", key)
		}
	}
	// The exact combined decoded byte allowance is inclusive.
	request.Fields = []FormField{{Name: "state", Value: strings.Repeat("x", 1000-len(request.Target)-len("state"))}}
	if err := request.Validate(); err != nil {
		t.Fatalf("exact decoded byte bound rejected: %v", err)
	}
	request.Fields[0].Value += "x"
	if err := request.Validate(); err == nil {
		t.Fatal("combined decoded byte bound exceeded")
	}
}

func TestContinuationCapabilityDocumentation(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "ports.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, declaration := range file.Decls {
		declaration, ok := declaration.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range declaration.Specs {
			typ, ok := spec.(*ast.TypeSpec)
			if !ok || typ.Name.Name != "Continuer" {
				continue
			}
			seen = true
			if declaration.Doc == nil || !strings.Contains(declaration.Doc.Text(), "without this capability has no continuation") {
				t.Fatal("optional continuation absence is undocumented")
			}
		}
	}
	if !seen {
		t.Fatal("continuation documentation guard saw no capability")
	}
}

func TestDecodedSourceSharedAllowance(t *testing.T) {
	_, _, capture := sourceFixture()
	limits := model.RuleLimits{MaxItems: 20, MaxBytes: 10000}
	span := model.Span{ArtifactHash: capture.Asset.SHA256, Space: model.OriginalBytes, Object: "field", ReasonCode: "BYTE_MAPPING_UNAVAILABLE"}
	observation := DecodedObservation{SourceID: capture.SourceID, ExternalID: "1", VersionKey: "version", Capture: capture, Fields: []DecodedField{{Name: "number", Value: "116/ 2025/QH15", Sources: []model.Span{span}}}, Limits: limits}
	page := DecodedDiscoveryPage{PageKey: "page", Capture: capture, Observations: []DecodedObservation{observation}, Cursor: model.CursorPosition{WindowKey: "window", SliceKey: "slice"}, Limits: limits}
	if err := page.Validate(); err != nil {
		t.Fatal(err)
	}
	page.Limits.MaxItems = 2
	if err := page.Validate(); err == nil || !strings.Contains(err.Error(), "item bound") {
		t.Fatalf("nested fields and provenance did not share allowance: %v", err)
	}
	page.Limits = limits
	page.Observations[0].Fields[0].Value = strings.Repeat("x", 10001) + "\xff"
	if err := page.Validate(); err == nil || !strings.Contains(err.Error(), "byte bound") {
		t.Fatalf("length must precede UTF-8 scan: %v", err)
	}
}

func TestDecodedContinuationAndTransportIsolation(t *testing.T) {
	limits := model.RuleLimits{MaxItems: 2, MaxBytes: 100}
	value := DecodedContinuation{Target: "ctrl$grvDocument", Fields: []FormField{{Name: "__VIEWSTATE", Value: "synthetic+&="}}}
	if err := value.Validate(limits); err != nil {
		t.Fatal(err)
	}
	value.Fields = append(value.Fields, value.Fields[0])
	if value.Validate(limits) == nil {
		t.Fatal("duplicate form state accepted")
	}
	for _, envelope := range []any{RequestEnvelope{}, DecoderEnvelope{}, model.ListingPageEnvelope{}, model.DetailEnvelope{}} {
		typ := reflect.TypeOf(envelope)
		if typ.NumField() != 2 {
			t.Fatalf("decoded continuation or filename entered transport: %s", typ)
		}
	}
}

func TestDecodedResultNestedBoundsAndAssociation(t *testing.T) {
	_, _, capture := sourceFixture()
	limits := model.RuleLimits{MaxItems: 100, MaxBytes: 10000}
	span := model.Span{ArtifactHash: capture.Asset.SHA256, Space: model.OriginalBytes, Range: &model.ByteRange{End: 1}}
	field := DecodedField{Name: "number", Value: "116/ 2025/QH15", Sources: []model.Span{span}, Limits: limits}
	relation := DecodedRelation{Kind: "vbpl_type_10", Target: "123", Sources: []model.Span{span}, Limits: limits}
	for _, value := range []interface{ Validate() error }{field, relation} {
		if err := value.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	observation := DecodedObservation{SourceID: capture.SourceID, ExternalID: "1", VersionKey: "version", Capture: capture, Fields: []DecodedField{field}, Relations: []DecodedRelation{relation}, SourceTree: model.SourceTree{Nodes: []model.SourceNode{{ID: "node", Label: "Điều 1", Sources: []model.Span{span}}}}, Limits: limits}
	detail := DecodedDetailResult{Observation: observation, Artifacts: []model.ArtifactInventoryEntry{{ArtifactKey: "artifact", Role: "main", URL: "https://example.org/file", DeclaredFilename: "synthetic.doc", Format: "doc"}}, Limits: limits}
	if err := detail.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*DecodedDetailResult){
		"total items":       func(r *DecodedDetailResult) { r.Limits.MaxItems = 3 },
		"nested tree bytes": func(r *DecodedDetailResult) { r.Observation.SourceTree.Nodes[0].Label = strings.Repeat("x", 10001) },
		"artifact filename": func(r *DecodedDetailResult) { r.Artifacts[0].DeclaredFilename = strings.Repeat("x", 10001) },
		"artifact format":   func(r *DecodedDetailResult) { r.Artifacts[0].Format = "exe" },
		"relation target":   func(r *DecodedDetailResult) { r.Observation.Relations[0].Target = strings.Repeat("x", 10001) },
		"provenance": func(r *DecodedDetailResult) {
			r.Observation.Fields[0].Sources[0].ArtifactHash = model.Digest(strings.Repeat("b", 64))
		},
		"capture strings": func(r *DecodedDetailResult) { r.Observation.Capture.TransportReceipt.Path = strings.Repeat("x", 10001) },
	} {
		t.Run(name, func(t *testing.T) {
			// JSON provides independent nested copies for these mutation cases.
			encoded, err := json.Marshal(detail)
			if err != nil {
				t.Fatal(err)
			}
			var bad DecodedDetailResult
			if err := json.Unmarshal(encoded, &bad); err != nil {
				t.Fatal(err)
			}
			mutate(&bad)
			if bad.Validate() == nil {
				t.Fatal("invalid decoded result accepted")
			}
		})
	}
}

func TestNewLocalValuesStayOutOfEnvelopes(t *testing.T) {
	sentinel := "synthetic.person@example.invalid"
	local := DecodedDiscoveryPage{Continuation: &DecodedContinuation{Target: "grid", Fields: []FormField{{Name: "state", Value: sentinel}}}, Observations: []DecodedObservation{{Fields: []DecodedField{{Value: sentinel}}, Relations: []DecodedRelation{{Target: sentinel}}}}}
	inventory := model.ArtifactInventoryEntry{DeclaredFilename: sentinel, Format: "unknown"}
	// Native values can exist only in local manifests, with no added transport field.
	if local.Continuation.Fields[0].Value != inventory.DeclaredFilename {
		t.Fatal("sentinel fixture changed")
	}
	hash := model.Digest(strings.Repeat("a", 64))
	for _, envelope := range []any{RequestEnvelope{Key: "request-" + strings.Repeat("a", 32), Manifest: hash}, DecoderEnvelope{Key: "decoder-" + strings.Repeat("a", 32), Manifest: hash}, model.ListingPageEnvelope{Key: "page-" + strings.Repeat("a", 32), Manifest: hash}, model.DetailEnvelope{Key: "detail-" + strings.Repeat("a", 32), Manifest: hash}} {
		encoded, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), sentinel) || strings.Contains(string(encoded), "DeclaredFilename") || strings.Contains(string(encoded), "Continuation") {
			t.Fatal("native decoded value entered transport")
		}
	}
}

func TestDecodedContinuationSharesPageAllowance(t *testing.T) {
	_, _, capture := sourceFixture()
	page := DecodedDiscoveryPage{PageKey: "page", Capture: capture, Cursor: model.CursorPosition{WindowKey: "window", SliceKey: "slice"}, Continuation: &DecodedContinuation{Target: "grid", Fields: []FormField{{Name: "state", Value: "synthetic+&="}}}, Limits: model.RuleLimits{MaxItems: 1, MaxBytes: 10000}}
	if err := page.Validate(); err != nil {
		t.Fatal(err)
	}
	page.Findings = []model.Finding{{ReasonCode: "INCOMPLETE", Severity: model.SeverityBlocking, Scope: model.GapSourceWindow, ScopeKey: "window"}}
	if err := page.Validate(); err == nil || !strings.Contains(err.Error(), "item bound") {
		t.Fatalf("continuation got a fresh allowance: %v", err)
	}
	page.Findings = nil
	page.Continuation.Fields[0].Value = strings.Repeat("x", 10001) + "\xff"
	if err := page.Validate(); err == nil || !strings.Contains(err.Error(), "byte bound") {
		t.Fatalf("form scanned before length check: %v", err)
	}
}

func TestDecodedContractBoundaryTypes(t *testing.T) {
	for _, value := range []any{DecodedField{}, DecodedRelation{}, DecodedObservation{}, DecodedDiscoveryPage{}, DecodedDetailResult{}, DecodedContinuation{}} {
		typ := reflect.TypeOf(value)
		for index := 0; index < typ.NumField(); index++ {
			field := typ.Field(index)
			if field.Type.Kind() == reflect.Interface || field.Type.Kind() == reflect.Func || field.Type.Kind() == reflect.Chan || field.Type.Kind() == reflect.UnsafePointer {
				t.Fatalf("decoded contract %s carries a capability in %s", typ, field.Name)
			}
		}
	}
}

func TestExpectedResponseKindBounds(t *testing.T) {
	hash, budget, capture := sourceFixture()
	detail := DetailRequest{Country: "vn", SourceID: "source", ExternalID: "123", ListingCaptures: []model.CaptureRef{capture}, WindowKey: "window", PolicyHash: hash, BudgetHash: hash, Budget: budget}
	plan := RequestPlan{Country: "vn", SourceID: "source", RequestKey: "request", WindowKey: "window", Purpose: model.PurposeDetail, URL: "https://example.invalid/item", Method: "GET", PolicyHash: hash, BudgetHash: hash, Budget: budget}
	for _, kind := range []string{"", "metadata", "files", "tree", "diagram"} {
		detail.ExpectedResponseKind, plan.ExpectedResponseKind = kind, kind
		if err := detail.Validate(); err != nil {
			t.Fatalf("detail kind %q: %v", kind, err)
		}
		if err := plan.Validate(); err != nil {
			t.Fatalf("plan kind %q: %v", kind, err)
		}
	}
	for _, kind := range []string{"FILES", "unknown", strings.Repeat("a", 65), "files\x00"} {
		detail.ExpectedResponseKind, plan.ExpectedResponseKind = kind, kind
		if detail.Validate() == nil || plan.Validate() == nil {
			t.Fatalf("accepted response kind %q", kind)
		}
	}
	plan.ExpectedResponseKind, plan.Purpose = "files", model.PurposeArtifact
	if plan.Validate() == nil {
		t.Fatal("artifact plan carried detail response kind")
	}
	plan.Purpose = model.PurposeDetail
	plan.Budget.MaxInputBytes = int64(len(plan.URL))
	if err := plan.Validate(); err == nil || !strings.Contains(err.Error(), "byte bound") {
		t.Fatalf("kind got a fresh byte allowance: %v", err)
	}
	plan.ExpectedResponseKind = ""
	if err := plan.Validate(); err != nil {
		t.Fatalf("URL boundary failed: %v", err)
	}
	detail.ExpectedResponseKind = "metadata"
	detail.Budget.MaxInputBytes = int64(len(detail.ExpectedResponseKind) - 1)
	if err := detail.Validate(); err == nil || !strings.Contains(err.Error(), "byte bound") {
		t.Fatalf("detail kind bypassed byte bound: %v", err)
	}
	detail.Budget.MaxInputBytes++
	if err := detail.Validate(); err != nil {
		t.Fatalf("detail kind boundary failed: %v", err)
	}
	for _, envelope := range []any{RequestEnvelope{Key: "request-" + strings.Repeat("a", 32), Manifest: hash}, DecoderEnvelope{Key: "decoder-" + strings.Repeat("a", 32), Manifest: hash}} {
		encoded, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(encoded, &fields); err != nil {
			t.Fatal(err)
		}
		if len(fields) != 2 || strings.Contains(string(encoded), "ExpectedResponseKind") {
			t.Fatal("parser metadata entered transport envelope")
		}
	}
}
