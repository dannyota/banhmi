package model

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func contractAsset() AssetRef {
	return AssetRef{Path: "artifacts/value", SHA256: Digest(strings.Repeat("a", 64)), ByteSize: 1}
}

func contractCapture() CaptureRef {
	a := contractAsset()
	return CaptureRef{Country: "vn", SourceID: "source", CaptureKey: "capture", Asset: a, MediaType: "text/html", RequestHash: a.SHA256, ResponseHash: a.SHA256, TransportReceipt: a}
}

func TestAcceptanceAndClosedIntakeKinds(t *testing.T) {
	for _, s := range []AcceptanceState{Candidate, Quarantined, Accepted, AcceptedWithGaps, Excluded} {
		if !s.Valid() || s.BuildEligible() != (s == Accepted || s == AcceptedWithGaps) {
			t.Fatalf("invalid eligibility for %s", s)
		}
	}
	if AcceptanceState("unknown").Valid() || AcceptanceState("unknown").BuildEligible() || DecisionKind("unknown").Valid() || GapKind("unknown").Valid() || ResidencyClass("unknown").Valid() {
		t.Fatal("unknown intake kind accepted")
	}
	for _, k := range []DecisionKind{DecisionRetryConverter, DecisionAcceptOCR, DecisionAcceptGaps, DecisionExclude, DecisionAlternateSource} {
		if !k.Valid() {
			t.Fatal(k)
		}
	}
	a := contractAsset()
	for _, c := range []ResidencyClass{ResidencyPublic, ResidencyPersonal, ResidencyUnclassified} {
		r := ResidencyAdmission{ArtifactHash: a.SHA256, PolicyHash: a.SHA256, Class: c, ReasonCode: "CLASSIFIED", Report: a}
		if r.Validate() != nil || r.Admitted() != (c == ResidencyPublic) {
			t.Fatal("incorrect residency admission")
		}
	}
	if err := contractCapture().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestContractValidatorsHaveNoIOImports(t *testing.T) {
	allowed := map[string]bool{"encoding/json": true, "fmt": true, "strings": true, "net/url": true, "time": true, "unicode/utf8": true}
	for _, name := range []string{"intake.go", "observations.go", "blocks.go", "conversion.go"} {
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil || !allowed[p] {
				t.Fatalf("I/O-capable dependency %s in %s", imp.Path.Value, name)
			}
		}
	}
}

func TestIntakeEnvelopesContainNoRawByteFields(t *testing.T) {
	seen := make(map[reflect.Type]bool)
	var inspect func(reflect.Type)
	inspect = func(typ reflect.Type) {
		if seen[typ] {
			return
		}
		seen[typ] = true
		switch typ.Kind() {
		case reflect.Interface:
			t.Fatalf("untyped payload interface in %s", typ)
		case reflect.Slice, reflect.Array:
			if typ.Elem().Kind() == reflect.Uint8 {
				t.Fatalf("raw byte payload in %s", typ)
			}
			inspect(typ.Elem())
		case reflect.Pointer:
			inspect(typ.Elem())
		case reflect.Struct:
			for i := 0; i < typ.NumField(); i++ {
				inspect(typ.Field(i).Type)
			}
		}
	}
	for _, typ := range []reflect.Type{reflect.TypeFor[CaptureRef](), reflect.TypeFor[Observation](), reflect.TypeFor[FollowUpRequest](), reflect.TypeFor[ListingPageResult](), reflect.TypeFor[DetailResult](), reflect.TypeFor[ResidencyAdmission]()} {
		inspect(typ)
	}
}

func TestEnvelopeSentinels(t *testing.T) {
	hash := Digest(strings.Repeat("a", 64))
	for _, envelope := range []interface{ Validate() error }{
		CaptureEnvelope{Key: "capture-" + strings.Repeat("a", 32), Manifest: hash},
		ObservationEnvelope{Key: "observation-" + strings.Repeat("a", 32), Manifest: hash},
		FollowUpEnvelope{Key: "request-" + strings.Repeat("a", 32), Manifest: hash},
		ListingPageEnvelope{Key: "page-" + strings.Repeat("a", 32), Manifest: hash},
		DetailEnvelope{Key: "detail-" + strings.Repeat("a", 32), Manifest: hash},
		ConversionEnvelope{Key: "conversion-" + strings.Repeat("a", 32), Manifest: hash},
		QualityEnvelope{Key: "quality-" + strings.Repeat("a", 32), Manifest: hash},
		ResidencyEnvelope{Key: "residency-" + strings.Repeat("a", 32), Manifest: hash},
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

func TestLedgerKeyGrammar(t *testing.T) {
	for _, key := range []string{"a", "0", "version-1", "a.b_c", strings.Repeat("a", 128)} {
		if err := ValidateKey(key); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []string{"", "Jane.Doe", "https://example.invalid/person/Jane.Doe", "person/name", "person:name", "_name", strings.Repeat("a", 129)} {
		if ValidateKey(key) == nil {
			t.Fatal("accepted non-ledger key")
		}
	}
	if ValidateEnvelopeKey("observation-"+strings.Repeat("a", 32), "capture") == nil {
		t.Fatal("accepted another envelope kind's key")
	}
}

func TestExportedContractDocumentation(t *testing.T) {
	for _, name := range []string{"intake.go", "observations.go", "blocks.go", "conversion.go"} {
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
