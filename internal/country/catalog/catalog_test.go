package catalog_test

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"danny.vn/banhmi/internal/core/country"
	"danny.vn/banhmi/internal/country/catalog"
	"danny.vn/banhmi/internal/country/vn"
)

func TestCatalogContainsOneInventoryDeclaration(t *testing.T) {
	declarations := catalog.Declarations()
	if len(declarations) != 1 || declarations[0].Code != "vn" || declarations[0].Entry != nil {
		t.Fatal("catalog must contain one inventory-only Vietnam declaration")
	}
	if !reflect.DeepEqual(declarations[0].Descriptor.Manifest, vn.AssetManifest()) {
		t.Fatal("catalog diverged from frozen Vietnam assets")
	}
	r, err := country.NewRegistry(declarations)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Freeze(); err != nil {
		t.Fatal(err)
	}
	if len(r.Entries()) != 0 {
		t.Fatal("catalog inventory became a runtime entry")
	}
	for _, code := range []country.Code{"", "vn", "my", "id", "sg", "th", "kh", "unknown"} {
		_, err := r.RequireEnabled(code)
		wantCode := code
		if wantCode == "" {
			wantCode = "vn"
		}
		var countryError *country.CountryError
		if !errors.Is(err, country.ErrCountryAbsent) || !errors.As(err, &countryError) || countryError.Code != wantCode {
			t.Fatalf("admission %q did not retain typed absence: %v", code, err)
		}
	}
}

func TestCatalogReturnsIndependentDeclarations(t *testing.T) {
	want := vn.AssetManifest()
	declarations := catalog.Declarations()
	d := &declarations[0]
	d.Code = "my"
	d.Entry = &country.Entry{Code: "vn", State: country.Enabled}
	m := &d.Descriptor.Manifest
	m.Code = "my"
	m.ModuleVersion = "changed"
	m.OCRHints[0] = "en"
	m.Sources[0].ID = "changed"
	m.Capabilities[0].Version = "changed"
	m.Assets[0].ByteSize++
	declarations = append(declarations, country.Declaration{Code: "my"})
	got := catalog.Declarations()
	if len(got) != 1 || got[0].Code != "vn" || got[0].Entry != nil || !reflect.DeepEqual(got[0].Descriptor.Manifest, want) {
		t.Fatal("caller mutation changed a later catalog result")
	}
}

func TestCatalogRejectsForgedRuntimeEntries(t *testing.T) {
	for _, state := range []country.State{country.Enabled, country.MigratedDisabled, country.Absent, "forged"} {
		declarations := catalog.Declarations()
		declarations[0].Entry = &country.Entry{Code: "vn", State: state, Manifest: declarations[0].Descriptor.Manifest.Clone()}
		if r, err := country.NewRegistry(declarations); r != nil || !errors.Is(err, country.ErrEntryIncomplete) {
			t.Fatalf("forged state %q did not fail construction: %v", state, err)
		}
	}
}

func TestPureAssemblyImportBoundary(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate package source")
	}
	base := filepath.Dir(filename)
	// Scan the closed foundation graph, including its transitive packages.
	allowed := []string{
		"bytes", "crypto/sha256", "embed", "encoding/json", "errors", "fmt",
		"io/fs", "slices", "strings", "unicode/utf8",
		"danny.vn/banhmi/internal/core/country",
		"danny.vn/banhmi/internal/corpus/model",
		"danny.vn/banhmi/internal/country/vn",
	}
	for _, relative := range []string{".", "../vn", "../../core/country", "../../corpus/model"} {
		directory := filepath.Join(base, relative)
		files, err := os.ReadDir(directory)
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			if file.IsDir() || !strings.HasSuffix(file.Name(), ".go") || strings.HasSuffix(file.Name(), "_test.go") {
				continue
			}
			path := filepath.Join(directory, file.Name())
			source, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, spec := range source.Imports {
				importPath, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					t.Fatal(err)
				}
				if !slices.Contains(allowed, importPath) {
					t.Errorf("%s imports outside the pure foundation graph: %s", file.Name(), importPath)
				}
			}
			for _, declaration := range source.Decls {
				if fn, ok := declaration.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == "init" {
					t.Errorf("%s registers an init effect", file.Name())
				}
			}
		}
	}
}
