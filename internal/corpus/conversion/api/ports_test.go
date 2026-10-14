package api

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"danny.vn/banhmi/internal/corpus/model"
)

func TestConversionAPIBoundary(t *testing.T) {
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

func TestEnvelopeSentinels(t *testing.T) {
	hash := model.Digest(strings.Repeat("a", 64))
	for _, envelope := range []interface{ Validate() error }{
		ConvertEnvelope{Key: "convert-" + strings.Repeat("a", 32), Manifest: hash},
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

type decodedRules struct{}

func (decodedRules) Structure(d model.DecodedDocument, p model.DecodedPartResult, tree model.SourceTree) (model.BodyValue, []model.Finding) {
	return model.BodyValue{Nodes: tree.Nodes, CandidateDigest: d.CandidateDigest}, p.Findings
}

func (decodedRules) Normalize(i model.TextInput) (model.MappedValue, []model.Finding) {
	return model.MappedValue{Text: i.Text, OffsetMap: []model.OffsetMapSegment{{Sources: i.Sources, Destinations: i.Sources, Transform: "identity"}}}, nil
}

func (decodedRules) Inspect(d model.DecodedDocument) ([]model.QualityMetric, []model.Finding) {
	return nil, nil
}

func TestPureRulesReceiveValuesAndReturnComputedContent(t *testing.T) {
	hash := model.Digest(strings.Repeat("a", 64))
	span := model.Span{ArtifactHash: hash, Space: model.RawText, Range: &model.ByteRange{Start: 0, End: 1}}
	limits := model.RuleLimits{MaxItems: 10, MaxBytes: 1000}
	doc := model.DecodedDocument{OriginalHash: hash, CandidateDigest: hash, Limits: limits, Blocks: []model.DecodedBlock{{ID: "block", Kind: model.BlockParagraph, RawText: "x", NormalizedText: "x", Language: "vi", Authority: model.AuthorityBinding, SourceSpans: []model.Span{span}}}}
	tree := model.SourceTree{Limits: limits, Nodes: []model.SourceNode{{ID: "article", Label: "Điều 1", BlockIDs: []string{"block"}, Sources: []model.Span{span}}}}
	parts := model.DecodedPartResult{Limits: limits}
	for _, value := range []interface{ Validate() error }{doc, tree, parts} {
		if err := value.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	var body BodyRules = decodedRules{}
	result, _ := body.Structure(doc, parts, tree)
	if result.Nodes[0].Label != tree.Nodes[0].Label || result.CandidateDigest != doc.CandidateDigest {
		t.Fatal("pure structure lost decoded labels or provenance")
	}
	input := model.TextInput{Text: "x", Sources: []model.Span{span}, Limits: limits}
	if err := input.Validate(); err != nil {
		t.Fatal(err)
	}
	var language LanguageRules = decodedRules{}
	mapped, _ := language.Normalize(input)
	if mapped.Text != input.Text || mapped.OffsetMap[0].Sources[0] != span {
		t.Fatal("pure normalization returned no computed content or provenance")
	}
	input.Limits.MaxBytes = 0
	if input.Validate() == nil {
		t.Fatal("unbounded rule input accepted")
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
