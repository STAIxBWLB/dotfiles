package secrets

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// This file guards BUG-09: a temp file in store.go must be unremovable-by-
// forgetting, not merely removed on the paths somebody remembered.
//
// The guard is structural rather than behavioral on purpose. The defect only
// fires when os.File.Close returns an error on a regular file, which no
// portable fixture can induce — so a behavioral test for it would be green
// before and after the fix, which is no evidence at all. The source-position
// rule below is red on exactly the three defective sites and green on the one
// that already cleaned up explicitly.
//
// The behavioral rows that ARE reachable live partly in secrets_test.go
// already: TestRestoreFile_FailedDecryptLeavesDestIntact and
// TestRestoreFile_NewDest assert the restore path leaves no litter on failure
// or success, and TestEncryptFile_FailureLeavesDestUntouched asserts the same
// for the encrypt path. The rows this file adds are the copy path's, which had
// none, because copyArchive is the site whose explicit cleanup was replaced by
// a defer and therefore the one that could regress.

// expectedTempFileSites is the fail-closed inventory of functions in store.go
// that create a temp file. Every name here must still be found: a rename that
// silently dropped a site from the guard's coverage is the failure this list
// exists to catch. A site NOT on the list is still checked — a fifth temp-file
// site added later is covered without touching this list.
var expectedTempFileSites = []string{
	"copyArchive",
	"encryptFile",
	"restoreFile",
	"verifier func literal",
}

// tempFileSite is one function body in store.go that calls os.CreateTemp.
type tempFileSite struct {
	name string
	body *ast.BlockStmt
}

// TestStoreTempFileCleanupRegisteredBeforeAnyErrorReturn asserts, for every
// temp-file site in store.go, that no statement in the window between the
// create and the rename can return without either removing the temp file
// itself or having a cleanup already registered.
//
// The rename closes the window: once the temp file has been renamed onto its
// destination there is nothing left to strand, which is why the terminal
// `return nil` after it is not a finding.
//
// That is the honest form of the rule. `defer os.Remove(tmpPath)` on the
// statement after the create satisfies it; so does copyArchive's pre-fix shape
// of removing explicitly on each error path. What fails it is the shape
// BUG-09 names: a `tmp.Close()` check that early-returns while the only
// cleanup sits one line below it.
func TestStoreTempFileCleanupRegisteredBeforeAnyErrorReturn(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "store.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing store.go: %v", err)
	}

	sites := collectTempFileSites(file)
	if len(sites) == 0 {
		t.Fatal("no os.CreateTemp site found in store.go: the guard would report success without checking anything")
	}

	found := make(map[string]bool, len(sites))
	for _, s := range sites {
		found[s.name] = true
	}
	for _, want := range expectedTempFileSites {
		if !found[want] {
			t.Errorf("expected temp-file site %q is no longer found in store.go; if it was renamed, rename it in expectedTempFileSites too rather than leaving the guard blind to it", want)
		}
	}

	for _, s := range sites {
		t.Run(s.name, func(t *testing.T) {
			if err := checkCleanupOrder(fset, s.body); err != nil {
				t.Error(err)
			}
		})
	}
}

// collectTempFileSites returns every function body in the file that calls
// os.CreateTemp among its own top-level statements. Function literals are
// collected separately from their enclosing declaration, because a closure's
// returns and defers belong to the closure, not to the function that built it.
func collectTempFileSites(file *ast.File) []tempFileSite {
	var sites []tempFileSite
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		if blockCreatesTempFile(fn.Body) {
			sites = append(sites, tempFileSite{name: fn.Name.Name, body: fn.Body})
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			lit, ok := n.(*ast.FuncLit)
			if !ok {
				return true
			}
			if blockCreatesTempFile(lit.Body) {
				sites = append(sites, tempFileSite{name: fn.Name.Name + " func literal", body: lit.Body})
			}
			return true
		})
	}
	return sites
}

func blockCreatesTempFile(body *ast.BlockStmt) bool {
	for _, stmt := range body.List {
		if stmtContains(stmt, isCallTo("os", "CreateTemp")) {
			return true
		}
	}
	return false
}

// checkCleanupOrder walks the statements after the os.CreateTemp call and
// reports the first one that can return without the temp file being removable.
func checkCleanupOrder(fset *token.FileSet, body *ast.BlockStmt) error {
	stmts := body.List
	create := -1
	for i, stmt := range stmts {
		if stmtContains(stmt, isCallTo("os", "CreateTemp")) {
			create = i
			break
		}
	}
	if create < 0 {
		return fmt.Errorf("no top-level os.CreateTemp statement, so this site cannot be checked")
	}
	createPos := fset.Position(stmts[create].Pos())

	i := create + 1
	// The create's own error check is the one returning statement the rule
	// permits: nothing can be registered before the create has succeeded.
	if i < len(stmts) {
		if _, isIf := stmts[i].(*ast.IfStmt); isIf && stmtContains(stmts[i], isReturn) {
			i++
		}
	}
	for ; i < len(stmts); i++ {
		stmt := stmts[i]
		if deferRemoves(stmt) {
			return nil
		}
		if stmtContains(stmt, isReturn) && !stmtContains(stmt, isCallTo("os", "Remove")) {
			return fmt.Errorf("%s: this statement returns after the os.CreateTemp at %s without removing the temp file, and no cleanup is registered yet — register the cleanup on the statement after the create (BUG-09)",
				fset.Position(stmt.Pos()), createPos)
		}
		if stmtContains(stmt, isCallTo("os", "Rename")) {
			// The rename consumed the temp file; nothing after it can
			// strand one.
			return nil
		}
	}
	return fmt.Errorf("no temp-file cleanup is registered after the os.CreateTemp at %s, and the temp file is never renamed away either", createPos)
}

// deferRemoves reports whether stmt is a defer that removes something. Unlike
// stmtContains it descends into the deferred literal, since that literal is
// exactly where a wrapped cleanup would live.
func deferRemoves(stmt ast.Stmt) bool {
	d, ok := stmt.(*ast.DeferStmt)
	if !ok {
		return false
	}
	removes := false
	ast.Inspect(d, func(n ast.Node) bool {
		if n != nil && isCallTo("os", "Remove")(n) {
			removes = true
		}
		return !removes
	})
	return removes
}

// stmtContains reports whether n contains a node matching want, without
// descending into nested function literals: their statements run in a
// different scope and say nothing about this one's control flow.
func stmtContains(n ast.Node, want func(ast.Node) bool) bool {
	found := false
	ast.Inspect(n, func(c ast.Node) bool {
		if found || c == nil {
			return false
		}
		if _, isLit := c.(*ast.FuncLit); isLit && c != n {
			return false
		}
		if want(c) {
			found = true
			return false
		}
		return true
	})
	return found
}

func isReturn(n ast.Node) bool {
	_, ok := n.(*ast.ReturnStmt)
	return ok
}

func isCallTo(pkg, name string) func(ast.Node) bool {
	return func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return false
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != name {
			return false
		}
		ident, ok := sel.X.(*ast.Ident)
		return ok && ident.Name == pkg
	}
}

func newCopyRunner(t *testing.T) *exec.Runner {
	t.Helper()
	return exec.NewRunner(false, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
}

func globCopyLitter(t *testing.T, dst string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(dst), "."+filepath.Base(dst)+".copy-*"))
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

// TestCopyArchive_FailedRenameLeavesNoTempFile is the row that proves
// copyArchive's four explicit os.Remove calls could be replaced by one defer
// without regressing. A directory at dst makes the rename fail after the temp
// file has been written, which is the last error path in the function.
func TestCopyArchive_FailedRenameLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.age")
	if err := os.WriteFile(src, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "occupied")
	if err := os.MkdirAll(filepath.Join(dst, "child"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := copyArchive(newCopyRunner(t), src, dst); err == nil {
		t.Fatal("copying onto a directory should fail the rename")
	}
	if got := globCopyLitter(t, dst); len(got) != 0 {
		t.Errorf("temp litter left behind by a failed rename: %v", got)
	}
}

func TestCopyArchive_SuccessLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.age")
	if err := os.WriteFile(src, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "out", "a.age")
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := copyArchive(newCopyRunner(t), src, dst); err != nil {
		t.Fatal(err)
	}
	if got := globCopyLitter(t, dst); len(got) != 0 {
		t.Errorf("temp litter left behind by a successful copy: %v", got)
	}
}

// TestEncryptFile_SuccessLeavesNoTempFile pins that the deferred removal is a
// no-op after the rename: the archive must survive its own cleanup.
func TestEncryptFile_SuccessLeavesNoTempFile(t *testing.T) {
	stubAge(t, false)
	dir := t.TempDir()
	src := filepath.Join(dir, "plain")
	if err := os.WriteFile(src, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "store", "plain.age")
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := encryptFile(context.Background(), newCopyRunner(t), []string{"-r", "age1x"}, src, dest, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("archive missing after a successful encrypt: %v", err)
	}
	litter, _ := filepath.Glob(filepath.Join(filepath.Dir(dest), ".*enc-*"))
	if len(litter) != 0 {
		t.Errorf("temp litter left behind by a successful encrypt: %v", litter)
	}
}
