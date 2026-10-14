package corpus

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"danny.vn/banhmi/internal/corpus/source/api"
)

func TestFactoriesAreLazyAndPartialUntilDecodersArrive(t *testing.T) {
	calls := 0
	bindings := map[string]Constructors{}
	for _, id := range []string{"vbpl", "congbao", "vanban", "sbv_hanoi"} {
		bindings[id] = Constructors{NewPlanner: func() api.Planner {
			calls++
			return nil
		}}
	}
	factories, err := Factories(bindings)
	if err != nil || calls != 0 {
		t.Fatalf("binding invoked constructor: %v, calls=%d", err, calls)
	}
	var ids []string
	for _, factory := range factories {
		ids = append(ids, factory.SourceID)
		if factory.NewPlanner == nil || factory.Validate() == nil {
			t.Fatal("partial factory claims complete decoder binding")
		}
	}
	if !reflect.DeepEqual(ids, []string{"congbao", "sbv_hanoi", "vanban", "vbpl"}) {
		t.Fatalf("source bindings: %v", ids)
	}
	delete(bindings, "vbpl")
	bindings["sbv"] = Constructors{NewPlanner: func() api.Planner { return nil }}
	if _, err := Factories(bindings); err == nil {
		t.Fatal("direct SBV replaced a declared source")
	}
	delete(bindings, "sbv")
	if _, err := Factories(bindings); err == nil {
		t.Fatal("missing source accepted")
	}
	if calls != 0 {
		t.Fatal("failure path invoked constructor")
	}
}

// These contracts cover every owned package, including test imports. Production
// imports have a narrower standard-library allowlist to prohibit I/O mechanisms.
func TestOwnedPackageImportBoundariesAndConstructorPurity(t *testing.T) {
	allowed := map[string]bool{
		"danny.vn/banhmi/internal/corpus/source/api": true,
		"danny.vn/banhmi/internal/corpus/model":      true,
		"danny.vn/banhmi/internal/country/vn":        true,
	}
	pureStandard := map[string]bool{"crypto/sha256": true, "encoding/json": true, "fmt": true, "net/url": true, "strconv": true, "strings": true}
	for _, dir := range []string{".", "../sources/vbpl", "../sources/congbao", "../sources/vanban", "../sources/sbvhanoi"} {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			packages, err := parser.ParseDir(token.NewFileSet(), dir, nil, 0)
			if err != nil || len(packages) == 0 {
				t.Fatalf("parse %s: %v", dir, err)
			}
			productionCount := 0
			for _, pkg := range packages {
				for name, file := range pkg.Files {
					production := !strings.HasSuffix(name, "_test.go")
					for _, imp := range file.Imports {
						path, err := strconv.Unquote(imp.Path.Value)
						if err != nil {
							t.Fatal(err)
						}
						if strings.Contains(strings.Split(path, "/")[0], ".") {
							if !allowed[path] {
								t.Errorf("%s imports forbidden package %s", name, path)
							}
						} else if production && !pureStandard[path] {
							t.Errorf("%s imports non-pure standard package %s", name, path)
						}
						if imp.Name != nil && (production || imp.Name.Name == "." || imp.Name.Name == "_") {
							t.Errorf("%s has hidden import side effects", name)
						}
					}
					if !production {
						continue
					}
					productionCount++
					for _, decl := range file.Decls {
						if function, ok := decl.(*ast.FuncDecl); ok && function.Name.Name == "init" {
							t.Errorf("%s declares init", name)
						}
						if declaration, ok := decl.(*ast.GenDecl); ok && declaration.Tok == token.VAR {
							for _, spec := range declaration.Specs {
								value := spec.(*ast.ValueSpec)
								for _, ident := range value.Names {
									if ident.Name != "_" {
										t.Errorf("%s declares mutable package state %s", name, ident.Name)
									}
								}
								for _, expression := range value.Values {
									// Interface assertions may convert nil to *Planner only.
									call, ok := expression.(*ast.CallExpr)
									if !ok || len(call.Args) != 1 {
										t.Errorf("%s has package-level execution", name)
										continue
									}
									paren, ok := call.Fun.(*ast.ParenExpr)
									if !ok {
										t.Errorf("%s has package-level function call", name)
									} else if _, ok := paren.X.(*ast.StarExpr); !ok {
										t.Errorf("%s has package-level function call", name)
									}
								}
							}
						}
					}
					ast.Inspect(file, func(node ast.Node) bool {
						call, ok := node.(*ast.CallExpr)
						if !ok {
							return true
						}
						if ident, ok := call.Fun.(*ast.Ident); ok && (ident.Name == "print" || ident.Name == "println" || ident.Name == "panic") {
							t.Errorf("%s uses forbidden side effect %s", name, ident.Name)
						}
						if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
							if receiver, ok := selector.X.(*ast.Ident); ok {
								if receiver.Name == "fmt" && selector.Sel.Name != "Sprintf" && selector.Sel.Name != "Errorf" {
									t.Errorf("%s performs I/O through fmt.%s", name, selector.Sel.Name)
								}
								if receiver.Name == "vn" && selector.Sel.Name != "Declare" {
									t.Errorf("%s resolves assets through vn.%s", name, selector.Sel.Name)
								}
								if selector.Sel.Name == "NewPlanner" || selector.Sel.Name == "NewDecoder" {
									t.Errorf("%s eagerly invokes a factory constructor", name)
								}
							}
						}
						return true
					})
				}
			}
			if productionCount == 0 {
				t.Fatal("purity check saw no production code")
			}
		})
	}
}

func TestModuleRootDoesNotImportWorkerFactories(t *testing.T) {
	packages, err := parser.ParseDir(token.NewFileSet(), "..", nil, 0)
	if err != nil || len(packages) == 0 {
		t.Fatalf("parse Vietnam root: %v", err)
	}
	for _, pkg := range packages {
		for name, file := range pkg.Files {
			for _, imp := range file.Imports {
				path, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					t.Fatal(err)
				}
				if strings.HasPrefix(path, "danny.vn/banhmi/internal/country/vn/corpus") {
					t.Errorf("module root %s imports worker factories", name)
				}
			}
		}
	}
}

// The only country helper called by constructors is Declare. Verify its
// dependency AssetManifest stays a literal declaration rather than loading assets.
func TestDescriptorConstructorDependenciesArePure(t *testing.T) {
	packages, err := parser.ParseDir(token.NewFileSet(), "..", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, pkg := range packages {
		for name, file := range pkg.Files {
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			for _, declaration := range file.Decls {
				function, ok := declaration.(*ast.FuncDecl)
				if !ok || function.Name.Name != "Declare" && function.Name.Name != "AssetManifest" {
					continue
				}
				seen[function.Name.Name] = true
				ast.Inspect(function.Body, func(node ast.Node) bool {
					if call, ok := node.(*ast.CallExpr); ok {
						ident, ok := call.Fun.(*ast.Ident)
						if !ok || function.Name.Name != "Declare" || ident.Name != "AssetManifest" {
							t.Errorf("descriptor constructor %s has non-declaration execution", function.Name.Name)
						}
					}
					return true
				})
			}
		}
	}
	if len(seen) != 2 {
		t.Fatal("descriptor purity check missed constructor dependencies")
	}
}

func TestFactoryPreservesOptionalContinuation(t *testing.T) {
	// Embedded ports model capability presence without importing implementations.
	withContinuation := &struct {
		api.Planner
		api.Continuer
	}{}
	withoutContinuation := &struct{ api.Planner }{}
	bindings := map[string]Constructors{}
	for _, id := range []string{"vbpl", "congbao", "vanban", "sbv_hanoi"} {
		bindings[id] = Constructors{NewPlanner: func() api.Planner { return withoutContinuation }}
	}
	bindings["vanban"] = Constructors{NewPlanner: func() api.Planner { return withContinuation }}
	factories, err := Factories(bindings)
	if err != nil {
		t.Fatal(err)
	}
	for _, factory := range factories {
		planner := factory.NewPlanner()
		_, continues := planner.(api.Continuer)
		if continues != (factory.SourceID == "vanban") {
			t.Fatalf("%s: worker factory changed continuation capability", factory.SourceID)
		}
		if factory.SourceID == "vanban" && planner != withContinuation {
			t.Fatal("worker factory replaced planner")
		}
	}
}

func TestPlannerSourceIDDocumentation(t *testing.T) {
	for _, source := range []string{"vbpl", "congbao", "vanban", "sbvhanoi"} {
		path := filepath.Join("..", "sources", source, "planner.go")
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		seen := false
		for _, declaration := range file.Decls {
			declaration, ok := declaration.(*ast.GenDecl)
			if !ok || declaration.Tok != token.CONST {
				continue
			}
			for _, spec := range declaration.Specs {
				value := spec.(*ast.ValueSpec)
				for _, name := range value.Names {
					if name.Name != "SourceID" {
						continue
					}
					seen = true
					if value.Doc == nil && declaration.Doc == nil {
						t.Errorf("%s: SourceID lacks documentation", path)
					}
				}
			}
		}
		if !seen {
			t.Errorf("%s: documentation guard found no SourceID", path)
		}
	}
}
