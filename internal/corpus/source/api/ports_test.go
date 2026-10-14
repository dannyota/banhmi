package api

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
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
