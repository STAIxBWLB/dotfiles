package remote

import (
	"context"
	"errors"
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
	host   Host
	rel    string
	deps   Deps
	// errs is what the run reported on stderr, warnings about the sent listing
	// included.
	errs strings.Builder
	// gitEnv pins git to the fixture, so the person's global ignores cannot
	// decide what this checkout lists.
	gitEnv []string
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
	f := &syncFixture{t: t, root: t.TempDir(), rsync: rsync, host: testHost(), rel: "one"}
	f.source = filepath.Join(f.root, "workspace", "one")
	f.dest = filepath.Join(f.root, "home", "dot-remote", "one")
	for _, dir := range []string{f.source, f.dest} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The sent listing lives in the state directory a run uses beside the
	// admission root, here a directory of the fixture's own. The mirror's own
	// file list is what a run over ssh would read, so the fixture reads it from
	// the local destination; git is the real one, its global configuration
	// pinned so the person's ignores cannot decide what the fixture calls
	// ignored.
	f.deps = Deps{
		SentRoot: filepath.Join(f.root, "state", "dot", "remote-sent"),
		Err:      &f.errs,
		MirrorFiles: func(_ context.Context, _ Host, _ string) ([]byte, error) {
			return f.mirrorFiles(), nil
		},
	}
	f.pinGitEnv()
	return f
}

// pinGitEnv keeps the real git calls inside the fixture's own configuration.
func (f *syncFixture) pinGitEnv() {
	f.t.Helper()
	f.t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	f.t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	f.t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	f.t.Setenv("XDG_CONFIG_HOME", filepath.Join(f.root, "xdg"))
}

// mirrorFiles is what the host would print, using the production listing
// command with HOME pointed at the fixture's home rather than a walk of its own:
// what the reconcile sees is then exactly what `find` on the host reports,
// pruning and symlinks included.
func (f *syncFixture) mirrorFiles() []byte {
	f.t.Helper()
	command := mirrorFindCommand(filepath.ToSlash(filepath.Join(f.host.Root, f.rel)))
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Env = append(os.Environ(), "HOME="+filepath.Join(f.root, "home"))
	out, err := cmd.Output()
	if err != nil {
		f.t.Fatalf("listing the mirror with %q: %v", command, err)
	}
	return out
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

// gitInit turns the source into a checkout, once per fixture.
func (f *syncFixture) gitInit() {
	f.t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		f.t.Skip("git not installed")
	}
	f.gitEnv = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	f.git(git, "-c", "init.defaultBranch=main", "init", "--quiet", ".")
}

// gitRun runs one git command in the fixture, looking the client up again.
func (f *syncFixture) gitRun(args ...string) []byte {
	f.t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		f.t.Skip("git not installed")
	}
	return f.git(git, args...)
}

// gitList is git's own listing of the checkout, which is exactly what a run
// feeds the ship pass.
func (f *syncFixture) gitList() []byte {
	f.t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		f.t.Skip("git not installed")
	}
	return f.git(git, gitListArgs()...)
}

// gitListing turns the source into a checkout and answers its listing, for the
// one-run tests.
func (f *syncFixture) gitListing() []byte {
	f.t.Helper()
	f.gitInit()
	return f.gitList()
}

// git runs one git command in the fixture's checkout and returns stdout.
func (f *syncFixture) git(git string, args ...string) []byte {
	f.t.Helper()
	env := f.gitEnv
	if env == nil {
		// A git call before gitInit (an added file, say) still has to ignore the
		// person's global configuration.
		env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	}
	cmd := exec.Command(git, args...)
	cmd.Dir = f.source
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// runAsRun does what a run does for a checkout: decide the sync input for this
// checkout, run the plan, then record what was sent - the same three steps Run
// performs, through the same functions.
func (f *syncFixture) runAsRun(listing []byte) {
	f.t.Helper()
	f.runAsRunExcludes((Config{}).Excludes(), listing)
}

// runAsRunExcludes is the same, with an operator exclude list.
func (f *syncFixture) runAsRunExcludes(excludes []string, listing []byte) {
	f.t.Helper()
	decision := f.deps.syncInput(context.Background(), f.host, f.dest, f.rel, f.source, SyncInput{
		Excludes: excludes,
		Listing:  listing,
	})
	f.runPlan(decision.Input)
	if !decision.Record {
		// A path that is not a checkout has no listing to record, and a run that
		// could not reconcile writes none either, which is what Run does too.
		return
	}
	if err := f.deps.writeSent(f.host, f.rel, f.source, listing); err != nil {
		f.t.Fatalf("recording what the run sent: %v", err)
	}
}

func (f *syncFixture) runPlan(in SyncInput) {
	f.t.Helper()
	steps := SyncPlan(f.rsync, f.source, f.dest, in)
	if len(steps) == 0 {
		f.t.Fatal("the plan is empty")
	}
	for _, step := range steps {
		runSyncStep(f.t, step)
	}
}

// runSyncStep runs one step against the local mirror and fails the test through
// the same rule a run applies, so the tolerated status is whatever
// stepError says it is rather than something this fixture decides.
func runSyncStep(t *testing.T, step Step) {
	t.Helper()
	cmd := exec.Command(step.Argv[0], step.Argv[1:]...)
	if step.Stdin != nil {
		cmd.Stdin = strings.NewReader(string(step.Stdin))
	}
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	exit := 0
	if errors.As(err, &exitErr) {
		exit = exitErr.ExitCode()
	}
	if ferr := stepError(step, exit, err); ferr != nil {
		t.Fatalf("rsync %v: %v\n%s", step.Argv[1:], ferr, out)
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

	f.runAsRun(listing)

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

// Only the ship pass sends. The delete pass has to leave what the checkout
// dropped in place, because it is the pass that cannot tell a stale file from a
// build output the host made.
func TestTheShipPassAloneDeletesNothing(t *testing.T) {
	f := newSyncFixture(t)
	f.sourceFile("main.go", "package main\n")
	f.destFile("stale.txt", "deleted locally\n")
	steps := SyncPlan(f.rsync, f.source, f.dest, SyncInput{
		Excludes: (Config{}).Excludes(),
		Listing:  []byte("main.go\x00"),
		Deletes:  []byte("stale.txt\x00"),
	})
	if len(steps) != 2 {
		t.Fatalf("steps = %d, want the delete pass and the ship pass", len(steps))
	}
	runSyncStep(t, steps[1])
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
	f.runAsRun([]byte("keep.txt\x00removed.txt\x00"))
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

	f.runAsRun(nil)

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
	f.runAsRunExcludes((Config{ExcludeExtra: []string{"datasets"}}).Excludes(), nil)
	if got := f.present(); !slices.Equal(got, []string{"notes.md"}) {
		t.Errorf("mirror holds %v, want the extra exclude to keep datasets out", got)
	}
}

// A mirror path can hold a space, a semicolon or a '$(' - a checkout named
// "notes; v2" is enough. rsync has to move the tree into such a path intact.
//
// The plan passes -s (--secluded-args), which sends the paths over rsync's own
// channel instead of letting the far side's shell parse them. rsync 3.2.3 is the
// accepted floor here and it does not protect by default (3.2.4 and later do),
// which is why the flag is explicit; this test is what proves the transfer still
// works with the flag on a path that would otherwise need quoting. The flag's
// presence in the argv is pinned separately in the plan test.
func TestAPathWithShellCharactersMirrorsIntact(t *testing.T) {
	f := newSyncFixture(t)
	f.sourceFile("main.go", "package main\n")
	f.dest = filepath.Join(f.root, "home", "dot-remote", "weird name; $(echo hi)")
	if err := os.MkdirAll(f.dest, 0o755); err != nil {
		t.Fatal(err)
	}
	f.runAsRun(nil)
	if got := f.present(); !slices.Equal(got, []string{"main.go"}) {
		t.Errorf("mirror holds %v, want the tree intact under the odd path", got)
	}
	if got := f.read("main.go"); got != "package main\n" {
		t.Errorf("main.go = %q", got)
	}
	// Nothing the shell would have run made it to the filesystem.
	if entries, err := os.ReadDir(f.root); err != nil {
		t.Fatal(err)
	} else {
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "hi") || strings.Contains(entry.Name(), "echo") {
				t.Errorf("the path was evaluated by a shell: %s", entry.Name())
			}
		}
	}
}

// gitignore is not git's only ignore source. The reconcile asks git, so a rule
// in .git/info/exclude keeps what the host built when an adopted mirror is
// cleaned, while a file git does not claim leaves.
func TestAFirstRunKeepsWhatTheOtherIgnoreFilesCover(t *testing.T) {
	f := newSyncFixture(t)
	f.sourceFile(".gitignore", "build/\n")
	f.sourceFile("main.go", "package main\n")
	f.gitInit()

	exclude := filepath.Join(f.source, ".git", "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(exclude), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exclude, []byte("artifacts/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.destFile("main.go", "package main\n")
	f.destFile("artifacts/out.tar", "built on the host\n")
	f.destFile("stale.txt", "an older checkout's file\n")

	f.runAsRun(f.gitList())

	f.missing("stale.txt")
	if got := f.read("artifacts/out.tar"); got != "built on the host\n" {
		t.Errorf("the reconcile removed a file .git/info/exclude covers: %q", got)
	}
}

// The global excludes file is the third source, and git answers for it too.
func TestAFirstRunKeepsWhatTheGlobalExcludesFileCovers(t *testing.T) {
	f := newSyncFixture(t)
	f.sourceFile("main.go", "package main\n")
	f.gitInit()

	globalIgnore := filepath.Join(f.root, "gitignore-global")
	if err := os.WriteFile(globalIgnore, []byte("vendor/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	globalConfig := filepath.Join(f.root, "gitconfig")
	if err := os.WriteFile(globalConfig, []byte("[core]\n\texcludesFile = "+globalIgnore+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)

	f.destFile("main.go", "package main\n")
	f.destFile("vendor/lib.a", "vendored on the host\n")
	f.destFile("stale.txt", "an older checkout's file\n")

	f.runAsRun(f.gitList())

	f.missing("stale.txt")
	if got := f.read("vendor/lib.a"); got != "vendored on the host\n" {
		t.Errorf("the reconcile removed a globally ignored file: %q", got)
	}
}

// The first run of a mirror this machine has no record of is where the old
// ignore-file prune could keep a file for good: it protected what `*.lock`
// matched, the run then recorded a listing without the deleted lock file, and
// the file was never a candidate again. The reconcile asks git instead, and git
// says the negation re-includes Cargo.lock, so it goes - while the ignored build
// output beside it stays.
func TestAFirstRunDeletesAStaleFileGitDoesNotIgnore(t *testing.T) {
	f := newSyncFixture(t)
	f.sourceFile(".gitignore", "*.lock\n!Cargo.lock\nbuild/\n")
	f.sourceFile("main.go", "package main\n")
	f.gitInit()

	// An adopted mirror: a lock file that was sent once and is deleted here, and
	// a build output the host made under a rule git does cover.
	f.destFile("Cargo.lock", "name = \"one\"\n")
	f.destFile("build/app.o", "compiled there\n")

	f.runAsRun(f.gitList())

	f.missing("Cargo.lock")
	if got := f.read("build/app.o"); got != "compiled there\n" {
		t.Errorf("the reconcile removed the host's build output: %q", got)
	}
}

// A path git still lists but this machine no longer has on disk is gone: `rm
// old.go` without staging the deletion leaves it in `git ls-files --cached`, so
// the ship pass skips it with --ignore-missing-args and the mirror keeps the
// copy an earlier checkout sent. On a first run nothing else names it either,
// because a candidate is a path this checkout does not list.
func TestAFirstRunDeletesATrackedFileThatIsGoneFromDisk(t *testing.T) {
	f := newSyncFixture(t)
	f.sourceFile("main.go", "package main\n")
	f.sourceFile("old.go", "package main\n")
	f.gitInit()
	f.gitRun("add", "old.go", "main.go")
	if listing := f.gitList(); !slices.Contains(strings.Split(string(listing), "\x00"), "old.go") {
		t.Fatalf("git does not list old.go, so the case under test is not here: %q", listing)
	}

	// The file leaves the disk and stays in git's listing.
	if err := os.Remove(filepath.Join(f.source, "old.go")); err != nil {
		t.Fatal(err)
	}
	f.destFile("old.go", "package main\n")
	f.destFile("main.go", "package main\n")

	f.runAsRun(f.gitList())

	f.missing("old.go")
	if got := f.read("main.go"); got != "package main\n" {
		t.Errorf("the run removed a file this machine still has: %q", got)
	}
}

// A symlink this checkout sent and then deleted leaves an adopted mirror too:
// the listing has to ask for links as well as files, and has to leave them
// un-followed so a mirror link pointing at a directory cannot walk the reconcile
// somewhere else.
func TestAFirstRunDeletesAStaleSymlink(t *testing.T) {
	f := newSyncFixture(t)
	f.sourceFile("main.go", "package main\n")
	f.gitInit()

	link := filepath.Join(f.dest, "current")
	if err := os.Symlink("releases/one", link); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}
	f.destFile("main.go", "package main\n")

	f.runAsRun(f.gitList())

	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Errorf("the reconcile left the stale symlink on the mirror: %v", err)
	}
	if got := f.read("main.go"); got != "package main\n" {
		t.Errorf("the reconcile removed a file this machine still has: %q", got)
	}
}

// The tests below run a checkout twice, through the same decision a run makes
// (syncInput), and delete something between the runs. That is the only
// shape that can tell what the mirror loses from what it keeps: the first run
// leaves a record of what was sent, the second subtracts.

// An ignore file is not a filter file. rsync reads its patterns as plain
// excludes and cannot express git's `!` negation, so a delete pass built from
// .gitignore protects what `*.lock` matches: a Cargo.lock this machine sent once
// and then deleted stayed on the mirror, where the next build used a file the
// checkout no longer has. The sent listing never reads the ignore file, so the
// file this machine no longer has is the file the mirror loses.
func TestANegatedIgnoreRuleNoLongerKeepsADeletedFileOnTheMirror(t *testing.T) {
	f := newSyncFixture(t)
	f.sourceFile(".gitignore", "*.lock\n!Cargo.lock\nbuild/\n")
	f.sourceFile("main.go", "package main\n")
	f.sourceFile("Cargo.lock", "name = \"one\"\n")
	f.gitInit()
	if listing := f.gitList(); !slices.Contains(strings.Split(string(listing), "\x00"), "Cargo.lock") {
		t.Fatalf("git does not list Cargo.lock, so the negation is not the case under test: %q", listing)
	}

	f.runAsRun(f.gitList())
	if got := f.read("Cargo.lock"); got != "name = \"one\"\n" {
		t.Fatalf("the first run sent %q, want the lock file", got)
	}

	if err := os.Remove(filepath.Join(f.source, "Cargo.lock")); err != nil {
		t.Fatal(err)
	}
	f.runAsRun(f.gitList())
	f.missing("Cargo.lock")
}

// A file force-added under an ignored directory is in git's listing, so the
// ship pass sent it, and deleting it here has to delete it there. The directory
// pattern that keeps the rest of `out/` home must not protect a file this same
// machine sent, and it must still keep the rest of the directory home.
func TestAForceAddedFileUnderAnIgnoredDirectoryIsRemovedWhenDeleted(t *testing.T) {
	f := newSyncFixture(t)
	f.sourceFile(".gitignore", "out/\n")
	f.sourceFile("main.go", "package main\n")
	f.sourceFile("out/keep.txt", "generated here, never sent\n")
	f.sourceFile("out/sent.txt", "force-added\n")
	f.gitInit()
	f.gitRun("add", "-f", "out/sent.txt")
	if listing := f.gitList(); !strings.Contains(string(listing), "out/sent.txt\x00") {
		t.Fatalf("git does not list the force-added file: %q", listing)
	}

	f.runAsRun(f.gitList())
	if got := f.read("out/sent.txt"); got != "force-added\n" {
		t.Fatalf("the first run sent %q, want the force-added file", got)
	}

	if err := os.Remove(filepath.Join(f.source, "out", "sent.txt")); err != nil {
		t.Fatal(err)
	}
	f.runAsRun(f.gitList())
	f.missing("out/sent.txt")
	if got := f.present(); slices.Contains(got, "out/keep.txt") {
		t.Errorf("the ship pass sent a file .gitignore covers: %v", got)
	}
}

// A build output the host made is not a candidate for deletion, because the
// delete pass names the paths this machine sent instead of asking rsync to prune
// everything the checkout no longer holds.
func TestAnIgnoredBuildOutputOnTheMirrorSurvivesTheDeletePass(t *testing.T) {
	f := newSyncFixture(t)
	f.sourceFile(".gitignore", "build/\n")
	f.sourceFile("main.go", "package main\n")
	f.sourceFile("notes.md", "sent once\n")
	f.gitInit()
	f.runAsRun(f.gitList())

	f.destFile("build/app.o", "compiled there\n")
	if err := os.Remove(filepath.Join(f.source, "notes.md")); err != nil {
		t.Fatal(err)
	}
	f.runAsRun(f.gitList())

	f.missing("notes.md")
	if got := f.read("build/app.o"); got != "compiled there\n" {
		t.Errorf("the delete pass removed the host's build output: %q", got)
	}
}

// The same two directions at depth: git honors an ignore file in any directory,
// and the reconcile asks git about every path, so a nested file this machine sent
// and deleted leaves the mirror while a build output under the same nested rule
// stays.
func TestANestedIgnoreFileLetsAStaleFileGoAndKeepsABuildOutput(t *testing.T) {
	f := newSyncFixture(t)
	f.sourceFile(".gitignore", "build/\n")
	f.sourceFile("sub/.gitignore", "build/\n*.tmp\n")
	f.sourceFile("sub/src/main.go", "package main\n")
	f.sourceFile("sub/notes.md", "sent once\n")
	f.gitInit()
	f.runAsRun(f.gitList())

	f.destFile("sub/build/app.o", "compiled there\n")
	if err := os.Remove(filepath.Join(f.source, "sub", "notes.md")); err != nil {
		t.Fatal(err)
	}
	f.runAsRun(f.gitList())

	f.missing("sub/notes.md")
	if got := f.read("sub/build/app.o"); got != "compiled there\n" {
		t.Errorf("the delete pass removed a build output under a nested ignore file: %q", got)
	}
}

// The record names the mirror it describes, not the name a config file gives the
// host. Repointing the host's root keeps the host name and the relative path, so
// the two mirrors share one record file - and its paths belong to the old
// mirror. Acting on it would delete a file from a mirror this machine never fed.
func TestARecordWrittenForAnotherRootDoesNotDeleteFromThisMirror(t *testing.T) {
	f := newSyncFixture(t)
	f.sourceFile(".gitignore", "out/\n")
	f.sourceFile("main.go", "package main\n")
	f.sourceFile("out/sent.txt", "force-added\n")
	f.gitInit()
	f.gitRun("add", "-f", "out/sent.txt")

	// The record now names out/sent.txt, and the checkout drops it: out of git's
	// listing, gone from disk.
	f.runAsRun(f.gitList())
	f.gitRun("rm", "-q", "--cached", "out/sent.txt")
	if err := os.Remove(filepath.Join(f.source, "out", "sent.txt")); err != nil {
		t.Fatal(err)
	}

	// The host's root moves: same host name, same relative path, another mirror.
	f.host.Root = "dot-remote2"
	f.dest = filepath.Join(f.root, "home", "dot-remote2", "one")
	if err := os.MkdirAll(f.dest, 0o755); err != nil {
		t.Fatal(err)
	}
	// Something the host built there, at the path the old record names.
	f.destFile("out/sent.txt", "built there\n")

	f.runAsRun(f.gitList())

	if got := f.read("out/sent.txt"); got != "built there\n" {
		t.Errorf("the record of another mirror deleted %q from this one", got)
	}
	if !strings.Contains(f.errs.String(), "was written for another mirror") {
		t.Errorf("stderr does not say the record describes another mirror:\n%s", f.errs.String())
	}
}

// A directory the checkout turns into a file is an ordinary git change, and the
// ship pass cannot make way for the file while the mirror still holds the old
// directory: rsync fails with "could not make way for new regular file", and it
// used to fail before the delete pass that would have run. --force replaces the
// directory with the file the listing names.
func TestADirectoryReplacedByAFileIsReplacedOnTheMirror(t *testing.T) {
	f := newSyncFixture(t)
	f.sourceFile("main.go", "package main\n")
	f.sourceFile("item/old.txt", "old\n")
	f.gitInit()
	f.runAsRun(f.gitList())
	if got := f.read("item/old.txt"); got != "old\n" {
		t.Fatalf("the first run sent %q, want the file under the directory", got)
	}

	if err := os.RemoveAll(filepath.Join(f.source, "item")); err != nil {
		t.Fatal(err)
	}
	f.sourceFile("item", "now a file\n")

	f.runAsRun(f.gitList())

	if got := f.read("item"); got != "now a file\n" {
		t.Errorf("the mirror has %q at item, want the file", got)
	}
}

// The same shape with a symlink, which the ship pass sends as a link.
func TestADirectoryReplacedByASymlinkIsReplacedOnTheMirror(t *testing.T) {
	f := newSyncFixture(t)
	f.sourceFile("main.go", "package main\n")
	f.sourceFile("item/old.txt", "old\n")
	f.gitInit()
	f.runAsRun(f.gitList())

	if err := os.RemoveAll(filepath.Join(f.source, "item")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("main.go", filepath.Join(f.source, "item")); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}

	f.runAsRun(f.gitList())

	info, err := os.Lstat(filepath.Join(f.dest, "item"))
	if err != nil {
		t.Fatalf("stat the mirror's item: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the mirror's item is %v, want a symlink", info.Mode())
	}
	target, err := os.Readlink(filepath.Join(f.dest, "item"))
	if err != nil || target != "main.go" {
		t.Errorf("the mirror's item points at %q (%v), want main.go", target, err)
	}
}

// --force is what replaces a directory that still holds something the delete pass
// did not name - a build output the host made inside it. That is the only case it
// acts on: everything the checkout left alone keeps its files.
func TestADirectoryReplacedByAFileGoesEvenWithABuildOutputInside(t *testing.T) {
	f := newSyncFixture(t)
	f.sourceFile("main.go", "package main\n")
	f.sourceFile("item/old.txt", "old\n")
	f.sourceFile("keep/kept.txt", "kept\n")
	f.gitInit()
	f.runAsRun(f.gitList())
	f.destFile("item/build.o", "built on the host\n")
	f.destFile("keep/built.o", "built on the host\n")

	if err := os.RemoveAll(filepath.Join(f.source, "item")); err != nil {
		t.Fatal(err)
	}
	f.sourceFile("item", "now a file\n")

	f.runAsRun(f.gitList())

	if got := f.read("item"); got != "now a file\n" {
		t.Errorf("the mirror has %q at item, want the file", got)
	}
	if got := f.read("keep/built.o"); got != "built on the host\n" {
		t.Errorf("--force reached outside the path it replaced: %q", got)
	}
}

// The reverse shape, which has to keep working: a file the checkout turns into a
// directory.
func TestAFileReplacedByADirectoryIsReplacedOnTheMirror(t *testing.T) {
	f := newSyncFixture(t)
	f.sourceFile("main.go", "package main\n")
	f.sourceFile("item", "was a file\n")
	f.gitInit()
	f.runAsRun(f.gitList())
	if got := f.read("item"); got != "was a file\n" {
		t.Fatalf("the first run sent %q, want the file", got)
	}

	if err := os.Remove(filepath.Join(f.source, "item")); err != nil {
		t.Fatal(err)
	}
	f.sourceFile("item/a.txt", "now a directory\n")

	f.runAsRun(f.gitList())

	if got := f.read("item/a.txt"); got != "now a directory\n" {
		t.Errorf("the mirror has %q under item, want the directory", got)
	}
}

// A directory the checkout deletes outright names files whose parent went with
// them, and rsync says 24 for that ("some files vanished"). The deletion happens,
// so the run has to go on rather than fail and repeat itself forever - and the
// host's own build output under the same directory is not named, so it stays.
func TestADeletedDirectoryLosesItsFilesAndKeepsWhatTheHostBuilt(t *testing.T) {
	f := newSyncFixture(t)
	f.sourceFile("main.go", "package main\n")
	f.sourceFile("item/one.txt", "one\n")
	f.sourceFile("item/two.txt", "two\n")
	f.gitInit()
	f.runAsRun(f.gitList())
	if got := f.read("item/one.txt"); got != "one\n" {
		t.Fatalf("the first run sent %q", got)
	}
	f.destFile("item/build.o", "built on the host\n")

	if err := os.RemoveAll(filepath.Join(f.source, "item")); err != nil {
		t.Fatal(err)
	}

	// The run has to succeed: rsync's 24 is the answer this step asked for.
	f.runAsRun(f.gitList())

	f.missing("item/one.txt")
	f.missing("item/two.txt")
	if got := f.read("item/build.o"); got != "built on the host\n" {
		t.Errorf("the delete pass removed the host's build output: %q", got)
	}
}
