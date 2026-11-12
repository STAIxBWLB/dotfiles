package remote

import (
	"context"
	"errors"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// gitRun runs git in dir for a test fixture and returns trimmed stdout.
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := osexec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newRepo makes a repository with one commit under root/name.
func newRepo(t *testing.T, root, name string) string {
	t.Helper()
	repo := filepath.Join(root, name)
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "-c", "init.defaultBranch=main", "init", "-q")
	write(t, filepath.Join(repo, "a.txt"), "one\n")
	write(t, filepath.Join(repo, ".gitignore"), "build/\n")
	gitRun(t, repo, "add", "-A")
	gitRun(t, repo, "commit", "-q", "-m", "first")
	return repo
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotCommitWithoutDirtyIsHEAD(t *testing.T) {
	repo := newRepo(t, t.TempDir(), "r")
	head := gitRun(t, repo, "rev-parse", "HEAD")
	got, err := SnapshotCommit(context.Background(), repo, false)
	if err != nil || got != head {
		t.Fatalf("SnapshotCommit = %q, %v; want HEAD %s", got, err, head)
	}
}

// --dirty sends the working tree as it is - a modified tracked file, a new
// untracked one, a deletion - but leaves the index, HEAD and every ref alone,
// and never includes an ignored file.
func TestSnapshotCommitDirtyLeavesIndexAndRefsUntouched(t *testing.T) {
	repo := newRepo(t, t.TempDir(), "r")
	write(t, filepath.Join(repo, "b.txt"), "staged\n")
	gitRun(t, repo, "add", "b.txt")
	write(t, filepath.Join(repo, "a.txt"), "changed\n")
	write(t, filepath.Join(repo, "new.txt"), "untracked\n")
	write(t, filepath.Join(repo, "build", "out.bin"), "ignored\n")

	head := gitRun(t, repo, "rev-parse", "HEAD")
	refs := gitRun(t, repo, "for-each-ref")
	indexBefore, err := os.ReadFile(filepath.Join(repo, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	statusBefore := gitRun(t, repo, "status", "--porcelain")

	sha, err := SnapshotCommit(context.Background(), repo, true)
	if err != nil {
		t.Fatal(err)
	}
	if sha == head {
		t.Fatal("a dirty snapshot returned HEAD")
	}
	if parent := gitRun(t, repo, "rev-parse", sha+"^"); parent != head {
		t.Errorf("snapshot parent = %s, want HEAD %s", parent, head)
	}
	files := gitRun(t, repo, "ls-tree", "-r", "--name-only", sha)
	for _, want := range []string{"a.txt", "b.txt", "new.txt", ".gitignore"} {
		if !strings.Contains("\n"+files+"\n", "\n"+want+"\n") {
			t.Errorf("snapshot lacks %s: %q", want, files)
		}
	}
	if strings.Contains(files, "build/out.bin") {
		t.Errorf("snapshot carries an ignored file: %q", files)
	}
	if got := gitRun(t, repo, "show", sha+":a.txt"); got != "changed" {
		t.Errorf("a.txt in snapshot = %q, want the working-tree content", got)
	}

	if after := gitRun(t, repo, "rev-parse", "HEAD"); after != head {
		t.Errorf("HEAD moved: %s -> %s", head, after)
	}
	if after := gitRun(t, repo, "for-each-ref"); after != refs {
		t.Errorf("refs changed:\n%s\n->\n%s", refs, after)
	}
	indexAfter, err := os.ReadFile(filepath.Join(repo, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	if string(indexAfter) != string(indexBefore) {
		t.Error("the checkout's index changed")
	}
	if after := gitRun(t, repo, "status", "--porcelain"); after != statusBefore {
		t.Errorf("status changed:\n%s\n->\n%s", statusBefore, after)
	}
}

func TestBareNameIsSharedByWorktreesAndSafe(t *testing.T) {
	if got := BareName("/home/me/work/my repo/.git"); !strings.HasPrefix(got, "my-repo-") || len(got) != len("my-repo-")+8 {
		t.Errorf("BareName = %q", got)
	}
	if got := BareName("/srv/x.git"); !strings.HasPrefix(got, "x.git-") {
		t.Errorf("BareName of a bare common dir = %q", got)
	}
	if BareName("/a/r/.git") == BareName("/b/r/.git") {
		t.Error("two repositories of one name share a bare repo")
	}
}

func TestVerifyCommandMountsTheBareRepoReadOnlyAndDropsRoot(t *testing.T) {
	h := testHost()
	h.Caches = []Cache{{Name: "home", Target: "/home/dev"}}
	cmd := VerifyCommand(h, "me-rdev-verify-k", "r-1234abcd.git", "refs/verify/k",
		baseRef{Local: "refs/remotes/origin/main", Branch: "main"}, []string{"make", "check"})
	for _, want := range []string{
		`-v "$HOME"/'dot-remote-git/r-1234abcd.git':/git/repo.git:ro`,
		"--user 0", "--name me-rdev-verify-k", "--cpus 16", "-v me-rdev-home:/home/dev",
		"setpriv", "--inh-caps=-all",
		"+refs/heads/main:refs/remotes/origin/main", "+refs/verify/k:refs/verify/target",
		"checkout -q --detach refs/verify/target", "make check",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("verify command lacks %q:\n%s", want, cmd)
		}
	}
	if !strings.HasPrefix(cmd, `U=$(id -u) G=$(id -g); docker run --rm`) {
		t.Errorf("verify command does not start with the host ids: %s", cmd)
	}
	if noBase := VerifyCommand(h, "c", "b.git", "refs/verify/k", baseRef{}, []string{"true"}); strings.Contains(noBase, "refs/remotes/origin") {
		t.Errorf("no base still fetches one: %s", noBase)
	}
}

func TestResolveBase(t *testing.T) {
	root := t.TempDir()
	origin := newRepo(t, root, "origin")
	clone := filepath.Join(root, "clone")
	gitRun(t, root, "clone", "-q", origin, clone)
	ctx := context.Background()

	got, err := resolveBase(ctx, clone, "")
	if err != nil || got.Local != "refs/remotes/origin/main" || got.Branch != "main" {
		t.Errorf("default base = %+v, %v", got, err)
	}
	got, err = resolveBase(ctx, clone, "main")
	if err != nil || got.Local != "refs/heads/main" || got.Branch != "main" {
		t.Errorf("configured local base = %+v, %v", got, err)
	}
	if _, err := resolveBase(ctx, clone, "origin/nope"); err == nil {
		t.Error("a base that does not resolve was accepted")
	}
	lone := newRepo(t, root, "lone")
	if got, err := resolveBase(ctx, lone, ""); err != nil || got.Branch != "" {
		t.Errorf("a repo with no remote = %+v, %v; want no base", got, err)
	}
}

// verifyFixture is a real repository under a configured local_root, with the
// fake execer standing in for ssh, git push and the runtime.
func verifyFixture(t *testing.T) (*runFixture, string) {
	t.Helper()
	f := newRunFixture(t)
	repo := newRepo(t, f.root, "vrepo")
	f.cfg.Projects = []Project{{Path: repo, Verify: "scripts/check.sh"}}
	return f, repo
}

func (f *runFixture) verifyDeps() Deps {
	d := f.deps()
	d.Git = nil
	d.Nonce = func() string { return "n1" }
	// The push, the bare repo's creation and the ref's deletion succeed; the
	// container run answers with the fixture's exit.
	d.Exec = func(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) (int, syscall.Signal, error) {
		last := argv[len(argv)-1]
		if hasArg(argv, "push") || strings.Contains(last, "git init -q --bare") || strings.Contains(last, "update-ref -d") {
			f.exec.calls = append(f.exec.calls, argv)
			return 0, 0, nil
		}
		return f.exec.execute(ctx, argv, stdin, stdout, stderr)
	}
	return d
}

func TestVerifyPushesRunsAndDeletesTheRef(t *testing.T) {
	f, repo := verifyFixture(t)
	err := f.verifyDeps().Verify(context.Background(), f.cfg, VerifyOptions{Path: repo, HostName: f.host.Name})
	if err != nil {
		t.Fatalf("Verify = %v\nstderr: %s", err, f.errOut.String())
	}
	var push, run, del []string
	for _, argv := range f.exec.calls {
		last := argv[len(argv)-1]
		switch {
		case hasArg(argv, "push"):
			push = argv
		case strings.Contains(last, "/git/repo.git:ro"):
			run = argv
		case strings.Contains(last, "update-ref -d"):
			del = argv
		}
	}
	head := gitRun(t, repo, "rev-parse", "HEAD")
	ref := VerifyRef("vrepo", head, "n1")
	if push == nil || !hasArg(push, "+"+head+":"+ref) {
		t.Errorf("push = %v, want HEAD to %s", push, ref)
	}
	if run == nil || !strings.Contains(run[len(run)-1], "scripts/check.sh") || !strings.Contains(run[len(run)-1], ref+":refs/verify/target") {
		t.Errorf("container run = %v, want the project's verify command fetching %s", run, ref)
	}
	if del == nil || !strings.Contains(del[len(del)-1], "update-ref -d "+ShellQuote(ref)) {
		t.Errorf("the ref was not deleted: %v", del)
	}
	if held := f.leases(); len(held) != 0 {
		t.Errorf("leases left: %+v", held)
	}
}

// Two runs of one path, from one machine or two, push to different refs, so
// neither can replace the commit the other's container is about to fetch.
func TestVerifyRefIsUniquePerRun(t *testing.T) {
	first, second := VerifyRef("p", "abc", verifyNonce()), VerifyRef("p", "abc", verifyNonce())
	if first == second {
		t.Error("two runs of one commit share a ref")
	}
	if !strings.HasPrefix(VerifyRef("p", "abc", "x"), "refs/verify/"+MirrorKey("p")+"/") {
		t.Errorf("ref = %q", VerifyRef("p", "abc", "x"))
	}
}

// A branch name git accepts may carry shell metacharacters; each refspec stays
// one word of the inner script, so it can neither run a command nor skip the
// checkout.
func TestVerifyCheckoutQuotesRefspecs(t *testing.T) {
	dir := t.TempDir()
	for _, branch := range []string{"main;id", "x$(touch pwned)", "a`touch pwned`"} {
		ref := "refs/verify/k/s-n"
		inner := verifyInner(ref, baseRef{Local: "refs/remotes/origin/" + branch, Branch: branch}, []string{"echo ran"})
		// git and cd are stubbed: each git call prints its argv on one line.
		stub := `git(){ printf 'git'; printf ' [%s]' "$@"; printf '\n'; }; cd(){ :; }; `
		cmd := osexec.Command("bash", "-c", stub+inner)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("branch %q: %v: %s", branch, err, out)
		}
		want := "git [fetch] [-q] [origin] [+refs/heads/" + branch + ":refs/remotes/origin/" + branch + "] [+" + ref + ":refs/verify/target]\n"
		if !strings.Contains(string(out), want) {
			t.Errorf("branch %q: fetch argv is not two whole refspecs:\n%s", branch, out)
		}
		if !strings.Contains(string(out), "git [checkout] [-q] [--detach] [refs/verify/target]") || !strings.HasSuffix(string(out), "ran\n") {
			t.Errorf("branch %q skipped the checkout or the command:\n%s", branch, out)
		}
		if _, err := os.Stat(filepath.Join(dir, "pwned")); err == nil {
			t.Fatalf("branch %q ran a command", branch)
		}
	}
}

func hasArg(argv []string, want string) bool {
	for _, a := range argv {
		if a == want {
			return true
		}
	}
	return false
}

func TestVerifyDeletesTheRefWhenTheCommandFails(t *testing.T) {
	f, repo := verifyFixture(t)
	f.exec.exit = 3
	err := f.verifyDeps().Verify(context.Background(), f.cfg, VerifyOptions{Path: repo, HostName: f.host.Name, Command: []string{"false"}})
	var code *ExitCode
	if !errors.As(err, &code) || code.Code != 3 {
		t.Fatalf("Verify = %v, want the command's status 3", err)
	}
	deleted := false
	for _, argv := range f.exec.calls {
		if strings.Contains(argv[len(argv)-1], "update-ref -d") {
			deleted = true
		}
	}
	if !deleted {
		t.Error("a failed verify left its ref on the host")
	}
}

func TestVerifyWithoutACommandOrProjectFails(t *testing.T) {
	f, repo := verifyFixture(t)
	f.cfg.Projects = nil
	err := f.verifyDeps().Verify(context.Background(), f.cfg, VerifyOptions{Path: repo, HostName: f.host.Name})
	if err == nil || !strings.Contains(err.Error(), "remote.projects.<path>.verify") {
		t.Fatalf("Verify = %v, want the missing verify command named", err)
	}
	if len(f.exec.calls) != 0 {
		t.Errorf("sent %d commands", len(f.exec.calls))
	}
}

// verify and run of one checkout share the path lease.
func TestVerifyDefersWhileARunHoldsThePath(t *testing.T) {
	f, repo := verifyFixture(t)
	held, err := AcquirePath(context.Background(), f.store, selfLease(t, repo), f.host, repo, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	err = f.verifyDeps().Verify(context.Background(), f.cfg, VerifyOptions{Path: repo, HostName: f.host.Name})
	var deferred *Deferred
	if !errors.As(err, &deferred) || deferred.Outcome.Scope != PathScope(repo) {
		t.Fatalf("Verify = %v, want a defer on the path scope", err)
	}
	if len(f.exec.calls) != 0 {
		t.Errorf("a deferred verify sent %d commands", len(f.exec.calls))
	}
}

func TestVerifyDefersOnHostLoad(t *testing.T) {
	f, repo := verifyFixture(t)
	f.host.MaxLoadRatio = 0.5
	f.cfg.Hosts[f.host.Name] = f.host
	f.exec.loads = "9.0 1 1 1/1 1\n4\n"
	err := f.verifyDeps().Verify(context.Background(), f.cfg, VerifyOptions{Path: repo, HostName: f.host.Name})
	var deferred *Deferred
	if !errors.As(err, &deferred) || deferred.Outcome.Scope != HostScope(f.host.Name) {
		t.Fatalf("Verify = %v, want a load defer", err)
	}
	if held := f.leases(); len(held) != 0 {
		t.Errorf("a deferred verify kept leases: %+v", held)
	}
}

func TestVerifyDryRunSendsNothing(t *testing.T) {
	f, repo := verifyFixture(t)
	err := f.verifyDeps().Verify(context.Background(), f.cfg, VerifyOptions{Path: repo, HostName: f.host.Name, DryRun: true, Dirty: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.exec.calls) != 0 || !strings.Contains(f.out.String(), "working tree") {
		t.Errorf("dry run: %d calls, out %q", len(f.exec.calls), f.out.String())
	}
}

func TestSrcCommandMountsNoCaches(t *testing.T) {
	h := testHost()
	h.Caches = []Cache{{Name: "cargo", Target: "/usr/local/cargo/registry"}}
	cmd := SrcCommand(h, "lib", []string{"rg", "-n", "x"})
	if strings.Contains(cmd, "me-rdev-cargo") || strings.Contains(cmd, "/usr/local/cargo") {
		t.Errorf("src mounts a cache: %s", cmd)
	}
	for _, want := range []string{`test -d "$HOME"/'dot-remote-src'/'lib'`, "exit 66", `-v "$HOME"/'dot-remote-src':/src`, "-w /src/lib", "--name me-rdev-src-lib", "rg -n x"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("src command lacks %q:\n%s", want, cmd)
		}
	}
	root := SrcCommand(h, "", []string{"git clone u x"})
	if !strings.HasPrefix(root, `mkdir -p "$HOME"/'dot-remote-src' && `) || !strings.Contains(root, "-w /src ") {
		t.Errorf("src without NAME: %s", root)
	}
}

func TestSrcRejectsANameThatLeavesSrcRoot(t *testing.T) {
	f := newRunFixture(t)
	for _, name := range []string{"../x", "a/b", ".hidden", "-x"} {
		err := f.deps().Src(context.Background(), f.cfg, SrcOptions{Name: name, HostName: f.host.Name, Command: []string{"true"}, Cwd: f.root})
		if err == nil || !strings.Contains(err.Error(), "bad clone name") {
			t.Errorf("Src(%q) = %v", name, err)
		}
	}
	if len(f.exec.calls) != 0 {
		t.Errorf("sent %d commands", len(f.exec.calls))
	}
}

// The host's exit 66 for a missing clone is the job's exit.
func TestSrcMissingCloneExits66(t *testing.T) {
	f := newRunFixture(t)
	f.exec.exit = ExitMissingClone
	err := f.deps().Src(context.Background(), f.cfg, SrcOptions{Name: "nope", HostName: f.host.Name, Command: []string{"ls"}, Cwd: f.root})
	var code *ExitCode
	if !errors.As(err, &code) || code.Code != ExitMissingClone {
		t.Fatalf("Src = %v, want exit 66", err)
	}
	if held := f.leases(); len(held) != 0 {
		t.Errorf("leases left: %+v", held)
	}
}

func TestSrcHoldsASrcLeaseAndTheHostLease(t *testing.T) {
	f := newRunFixture(t)
	f.host.MaxRuns = 1
	f.cfg.Hosts[f.host.Name] = f.host
	held, err := AcquireHost(context.Background(), f.store, selfLease(t, f.root), f.host, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	err = f.deps().Src(context.Background(), f.cfg, SrcOptions{HostName: f.host.Name, Command: []string{"true"}, Cwd: f.root})
	var deferred *Deferred
	if !errors.As(err, &deferred) || deferred.Outcome.Scope != HostScope(f.host.Name) {
		t.Fatalf("Src = %v, want a max_runs defer", err)
	}
}
