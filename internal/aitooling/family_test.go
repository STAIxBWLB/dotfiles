package aitooling

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/resourceguard"
)

// fakeFamilyNPM makes npm write a package.json and a relative bin link into its --prefix, as npm does.
func fakeFamilyNPM(t *testing.T, e *Engine, calls *int) {
	t.Helper()
	fakeBinary(t, e, "npm")
	e.exec = func(_ context.Context, c command) (string, error) {
		if filepath.Base(c.Path) != "npm" {
			t.Fatalf("unexpected command %s", c.Path)
		}
		*calls++
		prefix := c.Args[slices.Index(c.Args, "--prefix")+1]
		spec := c.Args[len(c.Args)-1]
		at := strings.LastIndex(spec, "@")
		pkg, version := spec[:at], spec[at+1:]
		var bin string
		for _, ft := range FamilyTools() {
			if ft.Package == pkg {
				bin = ft.Binary
			}
		}
		dir := filepath.Join(prefix, "lib", "node_modules", pkg)
		if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"`+pkg+`","version":"`+version+`"}`), 0o644); err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dir, "bin", bin+".js"), []byte("#!/usr/bin/env bun\n"), 0o755); err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Join(prefix, "bin"), 0o755); err != nil {
			return "", err
		}
		return "", os.Symlink(filepath.Join("..", "lib", "node_modules", pkg, "bin", bin+".js"), filepath.Join(prefix, "bin", bin))
	}
}

func hasNote(r FamilyResult, sub string) bool {
	return slices.ContainsFunc(r.Notes, func(n string) bool { return strings.Contains(n, sub) })
}

// #319 AC2/AC3: each version gets its own directory, `current` moves by rename, running processes
// keep their files and are only reported, N-1 stays, older versions go once nothing runs them, and
// rollback restores N-1.
func TestFamilyInstallActivateAndRollback(t *testing.T) {
	e := testEngine(t)
	calls := 0
	fakeFamilyNPM(t, e, &calls)
	ctx := context.Background()
	root := filepath.Join(e.FamilyRoot(), "amem")

	r, err := e.FamilyInstall(ctx, "amem", "1.0.0", false)
	if err != nil || r.Status != "activated" || r.Current != "1.0.0" || r.Previous != "" {
		t.Fatalf("first install = %+v, %v", r, err)
	}
	link := filepath.Join(e.opts.HomeDir, ".local", "bin", "amem")
	if got, _ := os.Readlink(link); got != filepath.Join(root, "current", "bin", "amem") {
		t.Fatalf("PATH entry = %q", got)
	}
	if _, err := os.Stat(link); err != nil {
		t.Fatalf("PATH entry does not resolve: %v", err)
	}

	old := filepath.Join(root, "1.0.0", "lib", "node_modules", "@staix", "agent-mem", "bin", "amem.js")
	e.processes = func(context.Context) (string, error) {
		return "  4242 /opt/homebrew/bin/bun " + old + " mcp\n  4343 /usr/bin/vim notes\n", nil
	}
	r, err = e.FamilyInstall(ctx, "amem", "2.0.0", false)
	if err != nil || r.Current != "2.0.0" || r.Previous != "1.0.0" {
		t.Fatalf("second install = %+v, %v", r, err)
	}
	if !hasNote(r, "pid 4242") || hasNote(r, "4343") || !hasNote(r, "nothing was stopped") {
		t.Fatalf("drain note missing or wrong: %v", r.Notes)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatalf("running version's files were touched: %v", err)
	}
	wantRoot := filepath.Join(root, "2.0.0", "lib", "node_modules", "@staix", "agent-mem")
	if resolved, err := filepath.EvalSymlinks(wantRoot); err == nil {
		wantRoot = resolved
	}
	if !hasNote(r, "amem setup <target> --root '"+wantRoot+"'") || !hasNote(r, "dot did not run it") {
		t.Fatalf("setup command not printed: %v", r.Notes)
	}

	// 1.0.0 is N-2 now and still running: kept.
	r, err = e.FamilyInstall(ctx, "amem", "3.0.0", false)
	if err != nil || !hasNote(r, "kept amem 1.0.0") || !slices.Equal(r.Versions, []string{"1.0.0", "2.0.0", "3.0.0"}) {
		t.Fatalf("third install = %+v, %v", r, err)
	}
	// A session launched through the PATH link names no version: every version stays.
	link = filepath.Join(e.opts.HomeDir, ".local", "bin", "amem")
	e.processes = func(context.Context) (string, error) { return "  5151 node " + link + " mcp\n", nil }
	r, err = e.FamilyInstall(ctx, "amem", "3.0.0", false)
	if err != nil || !hasNote(r, "cannot be tied to one version") || !slices.Equal(r.Versions, []string{"1.0.0", "2.0.0", "3.0.0"}) {
		t.Fatalf("link-launched session = %+v, %v", r, err)
	}
	// Nothing runs it any more: the next activation removes it, never N-1.
	e.processes = func(context.Context) (string, error) { return "", nil }
	r, err = e.FamilyInstall(ctx, "amem", "3.0.0", false)
	if err != nil || r.Status != "active" || !hasNote(r, "removed amem 1.0.0") || !slices.Equal(r.Versions, []string{"2.0.0", "3.0.0"}) || calls != 3 {
		t.Fatalf("re-activation = %+v, %v (npm calls %d)", r, err, calls)
	}

	r, err = e.FamilyRollback(ctx, "amem")
	if err != nil || r.Current != "2.0.0" || r.Previous != "3.0.0" {
		t.Fatalf("rollback = %+v, %v", r, err)
	}
	if got, _ := os.Readlink(filepath.Join(root, "current")); got != "2.0.0" {
		t.Fatalf("current = %q", got)
	}
	st := e.FamilyStatus()
	if st[1].ID != "amem" || st[1].Current != "2.0.0" || st[1].Status != "active" || len(st[1].Notes) != 0 || st[3].Status != "not-installed" || st[0].Status != "not-installed" || st[2].Status != "not-installed" {
		t.Fatalf("status = %+v", st)
	}
}

func TestFamilyInstallLatestAndDryRun(t *testing.T) {
	e := testEngine(t)
	calls := 0
	fakeFamilyNPM(t, e, &calls)
	ctx := context.Background()

	if _, err := e.FamilyInstall(ctx, "nope", "", false); err == nil {
		t.Fatal("unknown tool accepted")
	}
	if _, err := e.FamilyInstall(ctx, "vmem", "latest", false); err == nil {
		t.Fatal("non-semver version accepted")
	}
	e.opts.DryRun = true
	r, err := e.FamilyInstall(ctx, "vmem", "", false)
	if err != nil || r.Status != "planned" || !hasNote(r, "@staix/vault-mem@2.0.0") || calls != 0 {
		t.Fatalf("dry run = %+v, %v", r, err)
	}
	if _, err := os.Stat(e.FamilyRoot()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry run wrote %s", e.FamilyRoot())
	}
	e.opts.DryRun = false
	r, err = e.FamilyInstall(ctx, "vmem", "", false)
	if err != nil || r.Current != "2.0.0" || !hasNote(r, "vmem setup <target>") {
		t.Fatalf("latest install = %+v, %v", r, err)
	}
	// A dry run against the version already current says so instead of planning a reinstall.
	e.opts.DryRun = true
	if r, err = e.FamilyInstall(ctx, "vmem", "", false); err != nil || r.Status != "active" || !hasNote(r, "already current") {
		t.Fatalf("dry run at latest = %+v, %v", r, err)
	}
	e.opts.DryRun = false
	if _, err := e.FamilyRollback(ctx, "vmem"); err == nil {
		t.Fatal("rollback without a previous version succeeded")
	}
	// update never moves current below the registry's latest (2.0.0 here) and skips what is not installed.
	if _, err := e.FamilyInstall(ctx, "vmem", "3.0.0", false); err != nil {
		t.Fatal(err)
	}
	if r, err := e.FamilyUpdate(ctx, "vmem", false); err != nil || r.Current != "3.0.0" || !hasNote(r, "newer than the registry's latest") {
		t.Fatalf("update past latest = %+v, %v", r, err)
	}
	if r, err := e.FamilyUpdate(ctx, "ares", false); err != nil || r.Status != "not-installed" {
		t.Fatalf("update of a tool not installed = %+v, %v", r, err)
	}
	if _, err := e.FamilyRollback(ctx, "vmem"); err != nil {
		t.Fatal(err)
	}
	// Rollback takes the same slot as install, so the two never interleave.
	if _, err := e.FamilyInstall(ctx, "vmem", "1.0.0", false); err != nil {
		t.Fatal(err)
	}
	e.acquire = func(context.Context, resourceguard.Options) (func(), error) {
		return nil, &resourceguard.DeferredError{Reason: "busy"}
	}
	if r, err := e.FamilyRollback(ctx, "vmem"); err != nil || r.Status != "deferred-resource-pressure" {
		t.Fatalf("rollback under a busy slot = %+v, %v", r, err)
	}
}

// A PATH entry dot did not make is replaced only with --adopt, and a regular file never.
func TestFamilyPathEntryOwnership(t *testing.T) {
	e := testEngine(t)
	calls := 0
	fakeFamilyNPM(t, e, &calls)
	ctx := context.Background()
	bin := filepath.Join(e.opts.HomeDir, ".local", "bin")
	foreign := filepath.Join(e.opts.HomeDir, ".bun", "install", "global", "node_modules", "@staix", "vault-mem", "bin", "vmem.js")
	if err := os.MkdirAll(filepath.Dir(foreign), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(foreign, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(foreign, filepath.Join(bin, "vmem")); err != nil {
		t.Fatal(err)
	}
	r, err := e.FamilyInstall(ctx, "vmem", "1.0.0", false)
	if err != nil || !hasNote(r, "left alone; rerun with --adopt") {
		t.Fatalf("foreign link = %+v, %v", r, err)
	}
	if got, _ := os.Readlink(filepath.Join(bin, "vmem")); got != foreign {
		t.Fatalf("foreign link replaced without --adopt: %q", got)
	}
	if st := e.FamilyStatus(); len(st[2].Notes) != 1 {
		t.Fatalf("status does not flag the foreign PATH entry: %+v", st[2])
	}
	r, err = e.FamilyInstall(ctx, "vmem", "1.0.0", true)
	if err != nil || !hasNote(r, "that install is untouched") {
		t.Fatalf("adopt = %+v, %v", r, err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("adopt touched the other install: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bin, "amem"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	r, err = e.FamilyInstall(ctx, "amem", "1.0.0", true)
	if err != nil || !hasNote(r, "is a file from another install") {
		t.Fatalf("regular file = %+v, %v", r, err)
	}
}

// A relative ~/.local/bin link that resolves to current is dot's own: no status note, no --adopt (#324).
func TestFamilyRelativePathEntryIsOwn(t *testing.T) {
	e := testEngine(t)
	calls := 0
	fakeFamilyNPM(t, e, &calls)
	ctx := context.Background()
	link := filepath.Join(e.opts.HomeDir, ".local", "bin", "amem")
	if _, err := e.FamilyInstall(ctx, "amem", "1.0.0", false); err != nil {
		t.Fatal(err)
	}
	rel := filepath.Join("..", "share", "dotfiles", "tools", "amem", "current", "bin", "amem")
	if err := swapLink(link, rel); err != nil {
		t.Fatal(err)
	}
	if st := e.FamilyStatus(); len(st[1].Notes) != 0 {
		t.Fatalf("relative link to current flagged: %+v", st[1])
	}
	r, err := e.FamilyInstall(ctx, "amem", "2.0.0", false)
	if err != nil || hasNote(r, "--adopt") || r.Current != "2.0.0" {
		t.Fatalf("relative link to current treated as foreign: %+v, %v", r, err)
	}
	if got, _ := os.Readlink(link); got != rel {
		t.Fatalf("own relative link rewritten: %q", got)
	}
}

// A failed or unverifiable npm run leaves no version directory behind, and an unverified directory
// is refused rather than activated.
func TestFamilyInstallFailureLeavesNothing(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	if _, err := e.FamilyInstall(ctx, "amem", "1.0.0", false); err == nil || !strings.Contains(err.Error(), "npm prerequisite missing") {
		t.Fatalf("missing npm = %v", err)
	}
	fakeBinary(t, e, "npm")
	e.exec = func(context.Context, command) (string, error) { return "", nil }
	if _, err := e.FamilyInstall(ctx, "amem", "1.0.0", false); err == nil || !strings.Contains(err.Error(), "not verified") {
		t.Fatalf("unverified npm result = %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(e.FamilyRoot(), "amem"))
	if len(entries) != 0 {
		t.Fatalf("failed install left %v", entries)
	}
	if err := os.MkdirAll(filepath.Join(e.FamilyRoot(), "amem", "1.0.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := e.FamilyInstall(ctx, "amem", "1.0.0", false); err == nil || !strings.Contains(err.Error(), "remove it and retry") {
		t.Fatalf("unverified dir = %v", err)
	}
}

// #335: ahub's global CLI installs like any family tool; dot prints `ahub setup` for the shared
// Claude plugin, never runs it, and says when bun, ahub's runtime, is missing.
func TestFamilyInstallAhub(t *testing.T) {
	e := testEngine(t)
	calls := 0
	fakeFamilyNPM(t, e, &calls)
	ctx := context.Background()
	link := filepath.Join(e.opts.HomeDir, ".local", "bin", "ahub")

	r, err := e.FamilyInstall(ctx, "ahub", "0.31.0", false)
	if err != nil || r.Status != "activated" || calls != 1 || !hasNote(r, "run: "+filepath.Join(e.FamilyRoot(), "ahub", "0.31.0", "bin", "ahub")+" setup") || !hasNote(r, "dot did not run it") || !hasNote(r, "bun, which is not on PATH") {
		t.Fatalf("install = %+v, %v", r, err)
	}
	if got := readLinkAbs(link); got != filepath.Join(e.FamilyRoot(), "ahub", "current", "bin", "ahub") {
		t.Fatalf("PATH entry = %q", got)
	}
	fakeBinary(t, e, "bun")
	r, err = e.FamilyInstall(ctx, "ahub", "0.32.0", false)
	if err != nil || r.Previous != "0.31.0" || hasNote(r, "bun, which is not on PATH") || !hasNote(r, "plugin to 0.32.0") {
		t.Fatalf("upgrade = %+v, %v", r, err)
	}
	r, err = e.FamilyRollback(ctx, "ahub")
	if err != nil || r.Current != "0.31.0" || !hasNote(r, "plugin to 0.31.0") {
		t.Fatalf("rollback = %+v, %v", r, err)
	}
	if st := e.FamilyStatus(); st[0].ID != "ahub" || st[0].Status != "active" || st[0].Current != "0.31.0" || len(st[0].Notes) != 0 {
		t.Fatalf("status = %+v", st[0])
	}
}

// #318: ares installs from @staix/agent-research; dot prints the `ares setup --root` line and the
// bun note (ares runs on bun) and runs neither.
func TestFamilyInstallAres(t *testing.T) {
	e := testEngine(t)
	calls := 0
	fakeFamilyNPM(t, e, &calls)
	r, err := e.FamilyInstall(context.Background(), "ares", "0.1.0", false)
	if err != nil || r.Status != "activated" || calls != 1 || !hasNote(r, "ares setup <target> --root '") || !hasNote(r, "dot did not run it") || !hasNote(r, "ares runs on bun, which is not on PATH") {
		t.Fatalf("install = %+v, %v", r, err)
	}
	if _, err := os.Stat(filepath.Join(e.FamilyRoot(), "ares", "0.1.0", "lib", "node_modules", "@staix", "agent-research", "package.json")); err != nil {
		t.Fatal(err)
	}
}
