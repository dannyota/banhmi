package serve

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestServingPackageExcludesWorkerImports(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", "..", ".."))
	forbidden := []string{"/pkg/app", "/pkg/pipeline", "/pkg/ingest", "/internal/corpus"}
	seen := map[string]bool{}
	var visit func(string)
	visit = func(dir string) {
		if seen[dir] {
			return
		}
		seen[dir] = true
		packages, err := parser.ParseDir(token.NewFileSet(), dir, func(info os.FileInfo) bool {
			return !strings.HasSuffix(info.Name(), "_test.go")
		}, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, pkg := range packages {
			for _, file := range pkg.Files {
				for _, spec := range file.Imports {
					path, err := strconv.Unquote(spec.Path.Value)
					if err != nil {
						t.Fatal(err)
					}
					for _, fragment := range forbidden {
						if strings.Contains(path, fragment) {
							t.Errorf("serving import %q crosses worker boundary", path)
						}
					}
					const module = "danny.vn/banhmi/"
					if strings.HasPrefix(path, module) {
						visit(filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(path, module))))
					}
				}
			}
		}
	}
	visit(".")
}
