package remote

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// syncFixture is one local checkout and a local stand-in for its mirror. The
// two-pass sync takes any rsync destination, and a local path needs no ssh
// host, so the passes run for real against the installed rsync.
type syncFixture struct {
	t      *testing.T
	root   string
	source string
	dest   string
	rsync  string
}

func newSyncFixture(t *testing.T) *syncFixture {
	t.Helper()
	rsync, err := exec.LookPath("rsync")
	if err != nil {
		t.Skip("rsync not installed")
	}
	banner, err := exec.Command(rsync, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("rsync --version: %v", err)
	}
	if _, err := CheckRsyncVersion(string(banner)); err != nil {
		t.Skipf("the installed rsync cannot run the two-pass sync: %v", err)
	}
	f := &syncFixture{t: t, root: t.TempDir(), rsync: rsync}
	f.source = filepath.Join(f.root, "workspace", "one")
	f.dest = filepath.Join(f.root, "home", "dot-remote", "one")
	for _, dir := range []string{f.source, f.dest} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f *syncFixture) write(root, rel, content string) {
	f.t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *syncFixture) sourceFile(rel, content string) { f.write(f.source, rel, content) }
func (f *syncFixture) destFile(rel, content string)   { f.write(f.dest, rel, content) }

func (f *syncFixture) read(rel string) string {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.dest, filepath.FromSlash(rel)))
	if err != nil {
		f.t.Fatalf("read %s from the mirror: %v", rel, err)
	}
	return string(data)
}

func (f *syncFixture) missing(rel string) {
	f.t.Helper()
	_, err := os.Stat(filepath.Join(f.dest, filepath.FromSlash(rel)))
	if err == nil {
		f.t.Errorf("%s reached the mirror", rel)
	}
}

// present lists every file under the mirror, as slash paths.
func (f *syncFixture) present() []string {
	f.t.Helper()
	var files []string
	err := filepath.WalkDir(f.dest, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, relErr := filepath.Rel(f.dest, path)
		if relErr != nil {
			return relErr
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		f.t.Fatal(err)
	}
	slices.Sort(files)
	return files
}

// gitInit turns the source into a checkout and returns git's own listing of
// it, which is exactly what a run feeds the ship pass.
func (f *syncFixture) gitListing() []byte {
	f.t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		f.t.Skip("git not installed")
	}
	// The person's global ignores must not decide what this fixture tracks.
	env := append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	run := func(args ...string) []byte {
		cmd := exec.Command(git, args...)
		cmd.Dir = f.source
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			f.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return out
	}
	run("-c", "init.defaultBranch=main", "init", "--quiet", ".")
	return run(gitListArgs()...)
}

// sync runs the plan the way Deps.sync does: each step in order, with its
// stdin attached, and a failure reported with the rsync output.
func (f *syncFixture) sync(excludes []string, listing []byte) {
	f.t.Helper()
	steps := SyncPlan(f.rsync, f.source, f.dest, excludes, listing)
	if len(steps) == 0 {
		f.t.Fatal("the plan is empty")
	}
	for _, step := range steps {
		runSyncStep(f.t, step)
	}
}

func runSyncStep(t *testing.T, step Step) {
	t.Helper()
	cmd := exec.Command(step.Argv[0], step.Argv[1:]...)
	if step.Stdin != nil {
		cmd.Stdin = strings.NewReader(string(step.Stdin))
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("rsync %v: %v\n%s", step.Argv[1:], err, out)
	}
}

// The two-pass sync against the installed rsync: git's listing ships, the
// second pass prunes what the checkout dropped, and the host's own build
// outputs survive it.
func TestTwoPassSyncShipsGitFilesAndPrunesTheMirror(t *testing.T) {
	f := newSyncFixture(t)
	f.sourceFile(".gitignore", ".env.local\nnode_modules/\nbuild/\n")
	f.sourceFile("main.go", "package main\n")
	f.sourceFile("docs/a.md", "# one\n")
	f.sourceFile(".env.example", "API_URL=\n")
	// Ignored locally, so git never lists them.
	f.sourceFile(".env.local", "API_URL=secret\n")
	f.sourceFile("node_modules/pkg/index.js", "module.exports = 1\n")
	f.sourceFile("build/local.bin", "local build output\n")

	// The mirror carries a build output of its own and one file the checkout
	// no longer has.
	f.destFile(".gitignore", "build/\n")
	f.destFile("build/out.bin", "compiled on the host\n")
	f.destFile("stale.txt", "deleted locally\n")
	f.destFile("main.go", "package main // stale\n")

	listing := f.gitListing()
	for _, want := range []string{".env.example", ".gitignore", "docs/a.md", "main.go"} {
		if !strings.Contains(string(listing), want+"\x00") {
			t.Fatalf("git's listing does not name %s: %q", want, listing)
		}
	}
	for _, ignored := range []string{".env.local", "node_modules", "build/"} {
		if strings.Contains(string(listing), ignored) {
			t.Errorf("git's listing names the ignored path %s: %q", ignored, listing)
		}
	}

	f.sync((Config{}).Excludes(), listing)

	want := []string{".env.example", ".gitignore", "build/out.bin", "docs/a.md", "main.go"}
	if got := f.present(); !slices.Equal(got, want) {
		t.Errorf("mirror holds %v, want %v", got, want)
	}
	if got := f.read("main.go"); got != "package main\n" {
		t.Errorf("main.go = %q, want the local content", got)
	}
	if got := f.read("build/out.bin"); got != "compiled on the host\n" {
		t.Errorf("the host's build output changed to %q", got)
	}
	if got := f.read("docs/a.md"); got != "# one\n" {
		t.Errorf("docs/a.md = %q", got)
	}
	f.missing("stale.txt")
}

// Only the second pass deletes. The ship pass has to leave what the checkout
// dropped in place, because that is the pass that cannot tell a stale file
// from a build output the host made.
func TestTheShipPassAloneDeletesNothing(t *testing.T) {
	f := newSyncFixture(t)
	f.sourceFile("main.go", "package main\n")
	f.destFile("stale.txt", "deleted locally\n")
	steps := SyncPlan(f.rsync, f.source, f.dest, (Config{}).Excludes(), []byte("main.go\x00"))
	runSyncStep(t, steps[0])
	want := []string{"main.go", "stale.txt"}
	if got := f.present(); !slices.Equal(got, want) {
		t.Errorf("after the ship pass the mirror holds %v, want %v", got, want)
	}
}

// The listing can name a file removed between the git call and rsync, so the
// ship pass has to tolerate a path that is already gone.
func TestTheShipPassToleratesAListedFileThatIsGone(t *testing.T) {
	f := newSyncFixture(t)
	f.sourceFile("keep.txt", "keep\n")
	f.sync((Config{}).Excludes(), []byte("keep.txt\x00removed.txt\x00"))
	if got := f.present(); !slices.Equal(got, []string{"keep.txt"}) {
		t.Errorf("mirror holds %v, want only keep.txt", got)
	}
}

// A path that is not a checkout gets one mirroring pass: the exclude list
// stands in for git's listing and --delete keeps the two sides equal.
func TestSyncOfAPathThatIsNotACheckoutMirrorsAndDeletes(t *testing.T) {
	f := newSyncFixture(t)
	f.sourceFile("notes.md", "# notes\n")
	f.sourceFile(".env", "TOKEN=secret\n")
	f.sourceFile("node_modules/pkg/index.js", "module.exports = 1\n")
	f.sourceFile("secrets/server.pem", "-----BEGIN KEY-----\n")
	f.destFile("gone.txt", "removed locally\n")
	f.destFile("notes.md", "old\n")

	f.sync((Config{}).Excludes(), nil)

	if got := f.present(); !slices.Equal(got, []string{"notes.md"}) {
		t.Errorf("mirror holds %v, want only notes.md", got)
	}
	if got := f.read("notes.md"); got != "# notes\n" {
		t.Errorf("notes.md = %q, want the local content", got)
	}
}

// The operator's extra excludes reach rsync as filters, so a large local
// directory nobody configured is still skipped.
func TestExtraExcludesReachRsync(t *testing.T) {
	f := newSyncFixture(t)
	f.sourceFile("notes.md", "# notes\n")
	f.sourceFile("datasets/imagenet.bin", "huge\n")
	f.sync((Config{ExcludeExtra: []string{"datasets"}}).Excludes(), nil)
	if got := f.present(); !slices.Equal(got, []string{"notes.md"}) {
		t.Errorf("mirror holds %v, want the extra exclude to keep datasets out", got)
	}
}
