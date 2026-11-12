package remote

import (
	"context"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runAt runs a script in bash with HOME set and a docker function body.
func runAt(t *testing.T, home, dockerBody, script string) (string, int) {
	t.Helper()
	c := osexec.Command("bash", "-c", "docker(){ "+dockerBody+" }; "+script)
	c.Env = append(os.Environ(), "HOME="+home)
	out, err := c.CombinedOutput()
	code := 0
	if ee, ok := err.(*osexec.ExitError); ok {
		code = ee.ExitCode()
	}
	return string(out), code
}

// F1: an empty or missing job root still lets the listing finish with 0.
func TestPruneListingSucceedsWithEmptyOrMissingJobRoots(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "empty-jobs"), 0o755); err != nil {
		t.Fatal(err)
	}
	h := testHost()
	h.Jobs = map[string]JobProfile{
		"a": {Name: "a", Root: "empty-jobs"},
		"b": {Name: "b", Root: "missing-jobs"},
	}
	out, code := runAt(t, home, ":;", pruneListScriptFor(h))
	if code != 0 {
		t.Fatalf("listing exited %d: %s", code, out)
	}
}

// F3: a symlinked mirror root is allowed; only components below it are
// checked, and globbing is off while the path is split.
func TestPruneAllowsASymlinkedRootAndDoesNotGlob(t *testing.T) {
	home := t.TempDir()
	h := testHost()
	data := filepath.Join(home, "data", "dot-remote")
	if err := os.MkdirAll(filepath.Join(data, ".dot-remote"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(data, "work", "gone"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(data, filepath.Join(home, h.Root)); err != nil {
		t.Fatal(err)
	}
	out, code := runAt(t, home, ":;", pruneMirrorScript(h, "work/gone"))
	if code != 0 {
		t.Fatalf("a symlinked root was refused (%d): %s", code, out)
	}
	if _, err := os.Stat(filepath.Join(data, "work", "gone")); err == nil {
		t.Error("the mirror under a symlinked root was not removed")
	}
	script := pruneMirrorScript(h, "a/*")
	if !strings.Contains(script, "set -f") || strings.Index(script, "set -f") > strings.Index(script, "for part in") {
		t.Error("the walk does not disable globbing before splitting the path")
	}
}

// F2: a refusal reaches stderr, so the item's error names the cause.
func TestPruneReasonsGoToStderr(t *testing.T) {
	for _, script := range []string{pruneMirrorTemplate, pruneRefTemplate, pruneContainerTemplate} {
		for _, line := range strings.Split(script, "\n") {
			if strings.Contains(line, "echo") && (strings.Contains(line, "busy") || strings.Contains(line, "refused") || strings.Contains(line, "cannot tell")) && !strings.Contains(line, ">&2") {
				t.Errorf("reason not on stderr: %s", line)
			}
		}
	}
}

// F4: only dot's own container names are candidates.
func TestDotContainerNameForms(t *testing.T) {
	h := testHost()
	h.Prefix = "a"
	h.Jobs = map[string]JobProfile{"media": {Name: "media"}}
	for name, want := range map[string]bool{
		"a-0123456789ab":            true,
		"a-verify-0123456789ab":     true,
		"a-src-root":                true,
		"a-src-lib.x":               true,
		"a-job-media_j1":            true,
		"a-job-other_j1":            false,
		"a-gpu-0123456789ab":        false,
		"aX0123456789ab":            false,
		"a-verify-xyz":              false,
		"b-0123456789ab":            false,
		"a-0123456789abc":           false,
		"a-gpu-verify-0123456789ab": false,
	} {
		if got := dotContainer(h, name); got != want {
			t.Errorf("dotContainer(%q) = %v, want %v", name, got, want)
		}
	}
	root := t.TempDir()
	f := &pruneFixture{listing: "container\ta-gpu-0123456789ab\tExited\ncontainer\ta-0123456789ab\tExited\n"}
	report, err := f.deps(root).Prune(context.Background(), Config{LocalRoot: root}, h, PruneOptions{})
	if err != nil || len(report.Items) != 1 || report.Items[0].Name != "a-0123456789ab" {
		t.Errorf("%v %+v", err, report.Items)
	}
}

// F5: an unreadable local path is not "gone".
func TestPruneSkipsAMirrorWhoseCheckoutCannotBeStatted(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads anything")
	}
	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	if err := os.MkdirAll(filepath.Join(locked, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(locked, 0o755) }()
	f := &pruneFixture{listing: "mirror\t" + MirrorKey("locked/x") + "\tfree\tlocked/x\n"}
	report, err := f.deps(root).Prune(context.Background(), Config{LocalRoot: root}, testHost(), PruneOptions{})
	if err != nil || len(report.Items) != 0 || len(report.Skipped) != 1 || !strings.Contains(report.Skipped[0], "cannot tell") {
		t.Errorf("%v items %+v skipped %v", err, report.Items, report.Skipped)
	}
}

// F6: a parent mirror is not removed while a nested one is claimed or held.
func TestPruneKeepsAParentOfAHeldMirror(t *testing.T) {
	root := t.TempDir()
	f := &pruneFixture{listing: strings.Join([]string{
		"mirror\t" + MirrorKey("repo") + "\tfree\trepo",
		"mirror\t" + MirrorKey("repo/sub") + "\tclaimed\trepo/sub",
		"mirror\t" + MirrorKey("other") + "\tfree\tother",
	}, "\n")}
	h := testHost()
	d := f.deps(root)
	held, err := AcquirePath(context.Background(), d.Store, selfLease(t, root), h, filepath.Join(root, "other", "deep"), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	report, err := d.Prune(context.Background(), Config{LocalRoot: root}, h, PruneOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Items) != 0 || len(report.Skipped) != 3 {
		t.Errorf("items %+v skipped %v", report.Items, report.Skipped)
	}
}

// F7: a ref of a checkout this machine holds is left, and a failing ps makes
// the ref removal fail closed.
func TestPruneVerifyRefFailsClosed(t *testing.T) {
	root := t.TempDir()
	h := testHost()
	f := &pruneFixture{listing: "ref\tr.git\trefs/verify/" + MirrorKey("busy") + "/abc-n\n"}
	d := f.deps(root)
	held, err := AcquirePath(context.Background(), d.Store, selfLease(t, root), h, filepath.Join(root, "busy"), 0)
	if err != nil {
		t.Fatal(err)
	}
	report, err := d.Prune(context.Background(), Config{LocalRoot: root}, h, PruneOptions{})
	_ = held.Release()
	if err != nil || len(report.Items) != 0 || len(report.Skipped) != 1 {
		t.Errorf("held ref: %v %+v %v", err, report.Items, report.Skipped)
	}
	script := strings.Replace(pruneRefTemplate, "%s", "docker", 1)
	out, code := runAt(t, t.TempDir(), "echo daemon down >&2; return 1;", script)
	if code != 3 || !strings.Contains(out, "cannot tell") {
		t.Errorf("ps failure: %d %s", code, out)
	}
}

// F6: a mirror nested in a held parent is kept too.
func TestPruneKeepsAMirrorInsideAHeldParent(t *testing.T) {
	root := t.TempDir()
	f := &pruneFixture{listing: strings.Join([]string{
		"mirror\t" + MirrorKey("repo") + "\tclaimed\trepo",
		"mirror\t" + MirrorKey("repo/sub") + "\tfree\trepo/sub",
	}, "\n")}
	report, err := f.deps(root).Prune(context.Background(), Config{LocalRoot: root}, testHost(), PruneOptions{})
	if err != nil || len(report.Items) != 0 || len(report.Skipped) != 2 {
		t.Errorf("%v items %+v skipped %v", err, report.Items, report.Skipped)
	}
}

// F7: no store means no prune.
func TestPruneRefusesWithoutTheLeaseStore(t *testing.T) {
	root := t.TempDir()
	f := &pruneFixture{listing: ""}
	d := f.deps(root)
	d.Store = nil
	if _, err := d.Prune(context.Background(), Config{LocalRoot: root}, testHost(), PruneOptions{}); err == nil || !strings.Contains(err.Error(), "dot admit status") {
		t.Errorf("err = %v", err)
	}
}

// F9: a running mirror container, or a runtime that cannot answer, stops the
// mirror removal and gives the claim back.
func TestPruneMirrorChecksForARunningContainer(t *testing.T) {
	h := testHost()
	for body, wantMsg := range map[string]string{
		"echo " + h.ContainerName("work/gone") + ";": "busy: a container is running",
		"echo down >&2; return 1;":                   "cannot tell whether a run",
	} {
		home := t.TempDir()
		records := filepath.Join(home, h.Root, ".dot-remote")
		if err := os.MkdirAll(filepath.Join(home, h.Root, "work", "gone"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(records, 0o755); err != nil {
			t.Fatal(err)
		}
		out, code := runAt(t, home, body, pruneMirrorScript(h, "work/gone"))
		if code != 3 || !strings.Contains(out, wantMsg) {
			t.Errorf("%q: %d %s", body, code, out)
		}
		if _, err := os.Stat(filepath.Join(home, h.Root, "work", "gone")); err != nil {
			t.Error("the mirror was removed")
		}
		if _, err := os.Stat(filepath.Join(records, MirrorKey("work/gone")+".claim")); err == nil {
			t.Error("the claim was left behind")
		}
	}
}

// A mirror record whose path carries a newline cannot forge a ref line that
// reaches outside git_root.
func TestPruneRejectsForgedRefLines(t *testing.T) {
	root := t.TempDir()
	f := &pruneFixture{listing: strings.Join([]string{
		"mirror\t" + MirrorKey("x") + "\tfree\tx",
		"ref\t../x.git\trefs/verify/0123456789ab/a-n",
		"ref\t.hidden.git\trefs/verify/0123456789ab/a-n",
		"ref\tr.git\trefs/heads/main",
		"ref\tr.git\trefs/verify/0123456789ab/../../x",
		"ref\tr.git\trefs/verify/0123456789ab/a-n",
	}, "\n")}
	report, err := f.deps(root).Prune(context.Background(), Config{LocalRoot: root}, testHost(), PruneOptions{Yes: true})
	if err != nil {
		t.Fatal(err)
	}
	refs := 0
	for _, item := range report.Items {
		if item.Kind == "verify-ref" {
			refs++
			if item.Name != "r.git refs/verify/0123456789ab/a-n" {
				t.Errorf("forged ref accepted: %s", item.Name)
			}
		}
	}
	if refs != 1 || len(report.Skipped) != 4 {
		t.Errorf("refs %d skipped %v", refs, report.Skipped)
	}
	if strings.Contains(strings.Join(f.calls, "\n"), "../") {
		t.Errorf("a removal reached outside git_root: %v", f.calls)
	}
}

// The listing script stays valid shell for an ssh destination holding a
// quote, and the mirror claim is released however the script ends.
func TestPruneScriptsQuoteAndReleaseTheClaim(t *testing.T) {
	h := testHost()
	h.SSH = "o'brien"
	if out, err := osexec.Command("bash", "-n", "-c", pruneListScriptFor(h)).CombinedOutput(); err != nil {
		t.Errorf("listing does not parse with a quoted ssh destination: %v %s", err, out)
	}
	script := pruneMirrorScript(testHost(), "a")
	trap := strings.Index(script, "trap 'rmdir")
	if trap < 0 || trap < strings.Index(script, `mkdir "$lock"`) || strings.Count(script, `rmdir "$lock"`) != 1 {
		t.Errorf("the claim release is not one EXIT trap right after the mkdir:\n%s", script)
	}
}
