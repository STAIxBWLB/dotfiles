package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// docs/guides/architecture.md lists every Go package with its role; a package
// added without a row fails here (#369 AC4).
func TestArchitectureGuideListsEveryPackage(t *testing.T) {
	root := filepath.Join("..", "..")
	guide, err := os.ReadFile(filepath.Join(root, "docs", "guides", "architecture.md"))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, top := range []string{"cmd", "internal", "tools"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.Contains(path, "testdata") {
				return err
			}
			rel, _ := filepath.Rel(root, filepath.Dir(path))
			seen[filepath.ToSlash(rel)] = true
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) < 30 {
		t.Fatalf("found only %d packages; the walk is broken", len(seen))
	}
	for pkg := range seen {
		if !strings.Contains(string(guide), "| `"+pkg+"` |") {
			t.Errorf("docs/guides/architecture.md has no row for %s", pkg)
		}
	}
}
