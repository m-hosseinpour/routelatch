package trayui

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestTrayUIPackageKeepsItsDataBoundary proves the contract's "tray reads no
// state/config file" rule structurally: the pure controller/model package has
// no filesystem or store import, so it cannot open state or configuration.
func TestTrayUIPackageKeepsItsDataBoundary(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	forbidden := map[string]bool{
		"os": true, "io/ioutil": true, "syscall": true,
		"routelatch/internal/store": true, "routelatch/internal/config": true,
	}
	fset := token.NewFileSet()
	files := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(".", name)
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		files++
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("%s import: %v", path, err)
			}
			if forbidden[importPath] {
				t.Fatalf("%s imports %q; the tray package must not read state or configuration", name, importPath)
			}
		}
	}
	if files == 0 {
		t.Fatal("no non-test Go files were inspected")
	}
}
