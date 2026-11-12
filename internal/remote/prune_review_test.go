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
	home := gitRootHome(t, h, "r.git")
	out, code := runAt(t, home, "echo daemon down >&2; return 1;", pruneRefScript(h, "r.git", "refs/verify/0123456789ab/a-n"))
	if code != 3 || !strings.Contains(out, "cannot tell") {
		t.Errorf("ps failure: %d %s", code, out)
	}
}

// gitRootHome makes a home whose git_root holds a bare repo with one verify ref.
func gitRootHome(t *testing.T, h Host, repo string) string {
	t.Helper()
	home := t.TempDir()
	bare := filepath.Join(home, h.GitRoot, repo)
	if out, err := osexec.Command("git", "init", "-q", "--bare", bare).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	src := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"}, {"-c", "user.name=t", "-c", "user.email=t@x", "commit", "-q", "--allow-empty", "-m", "x"},
		{"push", "-q", bare, "HEAD:refs/verify/0123456789ab/a-n"},
	} {
		if out, err := osexec.Command("git", append([]string{"-C", src}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return home
}

func refExists(t *testing.T, repo string) bool {
	t.Helper()
	return osexec.Command("git", "-C", repo, "rev-parse", "-q", "--verify", "refs/verify/0123456789ab/a-n").Run() == nil
}

// #430.1: a repo name under git_root that is a symlink, or resolves outside
// git_root, is refused before any ref is deleted.
func TestPruneRefRefusesASymlinkedRepo(t *testing.T) {
	h := testHost()
	home := gitRootHome(t, h, "real.git")
	outside := filepath.Join(home, "elsewhere.git")
	if err := os.Rename(filepath.Join(home, h.GitRoot, "real.git"), outside); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, h.GitRoot, "r.git")); err != nil {
		t.Fatal(err)
	}
	out, code := runAt(t, home, ":;", pruneRefScript(h, "r.git", "refs/verify/0123456789ab/a-n"))
	if code != 4 || !strings.Contains(out, "is a symlink") {
		t.Errorf("symlinked repo: %d %s", code, out)
	}
	if !refExists(t, outside) {
		t.Error("the ref outside git_root was deleted")
	}
	// The happy path still deletes.
	home = gitRootHome(t, h, "r.git")
	if out, code := runAt(t, home, ":;", pruneRefScript(h, "r.git", "refs/verify/0123456789ab/a-n")); code != 0 {
		t.Fatalf("plain repo: %d %s", code, out)
	}
	if refExists(t, filepath.Join(home, h.GitRoot, "r.git")) {
		t.Error("the ref of a plain repo was not deleted")
	}
}

// #430.2: a symlinked records directory is refused by the listing and by the
// removal, which leaves the claim and the record where they are.
func TestPruneRefusesASymlinkedRecordsDirectory(t *testing.T) {
	h := testHost()
	home := t.TempDir()
	elsewhere := filepath.Join(home, "elsewhere")
	if err := os.MkdirAll(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(elsewhere, MirrorKey("work/gone")), []byte("work/gone"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, h.Root, "work", "gone"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(home, h.Root, ".dot-remote")); err != nil {
		t.Fatal(err)
	}
	out, _ := runAt(t, home, ":;", pruneListScriptFor(h))
	if !strings.Contains(out, "error\t") || !strings.Contains(out, "is a symlink") || strings.Contains(out, "mirror\t") {
		t.Errorf("listing: %s", out)
	}
	out, code := runAt(t, home, ":;", pruneMirrorScript(h, "work/gone"))
	if code != 4 || !strings.Contains(out, "is a symlink") {
		t.Errorf("removal: %d %s", code, out)
	}
	if _, err := os.Stat(filepath.Join(elsewhere, MirrorKey("work/gone"))); err != nil {
		t.Error("the record outside the root was removed")
	}
	if _, err := os.Stat(filepath.Join(elsewhere, MirrorKey("work/gone")+".claim")); err == nil {
		t.Error("a claim was made outside the root")
	}
	if _, err := os.Stat(filepath.Join(home, h.Root, "work", "gone")); err != nil {
		t.Error("the mirror was removed")
	}
}

// #430.1: a plain directory under git_root whose .git points at another
// repository (a symlink or a gitdir file) is refused, and the ref there
// survives.
func TestPruneRefRefusesARepoThatPointsElsewhere(t *testing.T) {
	h := testHost()
	for _, how := range []string{"symlink", "gitdir"} {
		home := gitRootHome(t, h, "real.git")
		outside := filepath.Join(home, "elsewhere.git")
		if err := os.Rename(filepath.Join(home, h.GitRoot, "real.git"), outside); err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(home, h.GitRoot, "r.git")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		var err error
		if how == "symlink" {
			err = os.Symlink(outside, filepath.Join(dir, ".git"))
		} else {
			err = os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: "+outside+"\n"), 0o644)
		}
		if err != nil {
			t.Fatal(err)
		}
		out, code := runAt(t, home, ":;", pruneRefScript(h, "r.git", "refs/verify/0123456789ab/a-n"))
		if code != 4 || !strings.Contains(out, "refused") {
			t.Errorf("%s: %d %s", how, code, out)
		}
		if !refExists(t, outside) {
			t.Errorf("%s: the ref in the outside repository was deleted", how)
		}
	}
}

// #430.3: a running verify of the same key under another prefix keeps the ref.
func TestPruneRefWaitsForAnotherPrefixesVerify(t *testing.T) {
	h := testHost()
	home := gitRootHome(t, h, "r.git")
	// The stub runtime answers `ps --filter name=...` like docker: it prints
	// the running container whose name matches the filter's regex.
	stub := `for a in "$@"; do case "$a" in name=*) re=${a#name=};; esac; done; ` +
		`printf '%s\n' other-rdev-verify-0123456789ab | grep -E -e "$re" || true;`
	out, code := runAt(t, home, stub, pruneRefScript(h, "r.git", "refs/verify/0123456789ab/a-n"))
	if code != 3 || !strings.Contains(out, "other-rdev-verify-0123456789ab") {
		t.Errorf("another prefix's verify: %d %s", code, out)
	}
	if !refExists(t, filepath.Join(home, h.GitRoot, "r.git")) {
		t.Error("the ref of a running verify was deleted")
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
