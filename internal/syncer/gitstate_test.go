package syncer

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gitStateFixtures are real-git sandboxes under t.TempDir(). They never touch
// the user's actual repositories.
func gitStateRun_(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=dot-test", "GIT_AUTHOR_EMAIL=dot-test@example.invalid",
		"GIT_COMMITTER_NAME=dot-test", "GIT_COMMITTER_EMAIL=dot-test@example.invalid",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s (in %s): %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func gitStateInitRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitStateRun_(t, dir, "init", "-q", "-b", "main")
}

func gitStateCommitFile(t *testing.T, dir, name, content, message string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitStateRun_(t, dir, "add", name)
	gitStateRun_(t, dir, "commit", "-q", "-m", message)
	return gitStateRun_(t, dir, "rev-parse", "HEAD")
}

func gitStateHead(t *testing.T, dir string) string {
	t.Helper()
	return gitStateRun_(t, dir, "rev-parse", "HEAD")
}

// gitStateObjectCount counts files under .git/objects (loose and packed), the
// observable for "classification writes no objects".
func gitStateObjectCount(t *testing.T, repo string) int {
	t.Helper()
	gitdir := gitStateRun_(t, repo, "rev-parse", "--absolute-git-dir")
	count := 0
	err := filepath.Walk(filepath.Join(gitdir, "objects"), func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			count++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return count
}

func gitStateFileBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// gitStateRewriteTracked overwrites a tracked file the way peer sync delivers
// it and backdates the mtime, so git's stat comparison can never mistake the
// rewrite for the indexed version no matter how fast the test runs.
func gitStateRewriteTracked(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Unix(1700000000, 0)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

func gitStateIndexBytes(t *testing.T, repo string) []byte {
	t.Helper()
	gitdir := gitStateRun_(t, repo, "rev-parse", "--absolute-git-dir")
	return gitStateFileBytes(t, filepath.Join(gitdir, "index"))
}

func gitStateReport(t *testing.T, res *GitStateResult, path string) *GitRepoReport {
	t.Helper()
	for _, rep := range res.Repos {
		if rep.Path == path {
			return rep
		}
	}
	t.Fatalf("no report for %q in %+v", path, res.Repos)
	return nil
}

// gitStateBehindFixture builds the AC1 scenario: a workspace whose submodule
// HEAD sits one commit behind the parent gitlink while the submodule worktree
// already matches the gitlink's tree. It returns the workspace root, the
// submodule path, the old HEAD (C1) and the gitlink target (C2).
func gitStateBehindFixture(t *testing.T) (ws, sub, c1, c2 string) {
	t.Helper()
	tmp := t.TempDir()

	subSrc := filepath.Join(tmp, "sub-src")
	gitStateInitRepo(t, subSrc)
	c1 = gitStateCommitFile(t, subSrc, "file.txt", "v1\n", "c1")
	c2 = gitStateCommitFile(t, subSrc, "file.txt", "v2\n", "c2")

	ws = filepath.Join(tmp, "ws")
	gitStateInitRepo(t, ws)
	gitStateRun_(t, ws, "-c", "protocol.file.allow=always", "submodule", "add", "-q", subSrc, "sub")
	gitStateRun_(t, ws, "commit", "-q", "-m", "add sub")

	sub = filepath.Join(ws, "sub")
	// HEAD moves back to C1; the file keeps the C2 content, the way peer sync
	// delivers it: files travel, HEAD stays behind.
	gitStateRun_(t, sub, "checkout", "-q", c1)
	gitStateRewriteTracked(t, filepath.Join(sub, "file.txt"), "v2\n")
	return ws, sub, c1, c2
}

// AC1: HEAD behind the gitlink, worktree identical to the gitlink tree.
// Realign moves HEAD to the descendant commit; the worktree stays
// byte-identical and no object is written. The undo record restores HEAD.
func TestPeerGitRealign_DescendantMatch(t *testing.T) {
	ws, sub, c1, c2 := gitStateBehindFixture(t)
	ctx := context.Background()

	res, err := PeerGitStatus(ctx, ws, nil)
	if err != nil {
		t.Fatal(err)
	}
	rep := gitStateReport(t, res, "sub")
	if rep.Status != GitRepoRealignable {
		t.Fatalf("status = %q (%s), want realignable", rep.Status, rep.Reason)
	}
	if rep.Target != c2 {
		t.Fatalf("target = %q, want gitlink %q", rep.Target, c2)
	}
	if rep.TargetDiffs != 0 {
		t.Fatalf("target diffs = %d, want 0", rep.TargetDiffs)
	}

	objectsBefore := gitStateObjectCount(t, sub)
	worktreeBefore := gitStateFileBytes(t, filepath.Join(sub, "file.txt"))

	res, err = PeerGitRealign(ctx, ws, nil, RealignOptions{Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	rep = gitStateReport(t, res, "sub")
	if rep.Status != GitRepoRealigned {
		t.Fatalf("status = %q (%s), want realigned", rep.Status, rep.Reason)
	}
	if rep.PreviousHead != c1 {
		t.Fatalf("undo record = %q, want old HEAD %q", rep.PreviousHead, c1)
	}
	if got := gitStateHead(t, sub); got != c2 {
		t.Fatalf("HEAD = %q, want %q", got, c2)
	}
	if got := gitStateFileBytes(t, filepath.Join(sub, "file.txt")); string(got) != string(worktreeBefore) {
		t.Fatalf("worktree changed: got %q, want %q", got, worktreeBefore)
	}
	if got := gitStateObjectCount(t, sub); got != objectsBefore {
		t.Fatalf("object count changed: %d -> %d", objectsBefore, got)
	}
	if reflog := gitStateRun_(t, sub, "log", "-g", "-1", "--format=%gs"); !strings.Contains(reflog, "dot peer realign") {
		t.Fatalf("reflog subject %q does not record the realign", reflog)
	}

	// The undo record restores HEAD and index exactly.
	gitStateRun_(t, sub, "reset", "--mixed", "-q", rep.PreviousHead)
	if got := gitStateHead(t, sub); got != c1 {
		t.Fatalf("after undo HEAD = %q, want %q", got, c1)
	}
	if got := gitStateFileBytes(t, filepath.Join(sub, "file.txt")); string(got) != string(worktreeBefore) {
		t.Fatalf("undo changed the worktree: got %q, want %q", got, worktreeBefore)
	}
}

// The first-parent chain from HEAD to the branch upstream supplies the
// candidate when there is no parent gitlink. The command never fetches.
func TestPeerGitRealign_UpstreamChainCandidate(t *testing.T) {
	tmp := t.TempDir()
	origin := filepath.Join(tmp, "origin")
	gitStateInitRepo(t, origin)
	c1 := gitStateCommitFile(t, origin, "file.txt", "v1\n", "c1")
	c2 := gitStateCommitFile(t, origin, "file.txt", "v2\n", "c2")

	ws := filepath.Join(tmp, "ws")
	gitStateRun_(t, tmp, "clone", "-q", origin, ws)
	gitStateRun_(t, ws, "reset", "--hard", "-q", c1)
	gitStateRewriteTracked(t, filepath.Join(ws, "file.txt"), "v2\n")

	res, err := PeerGitRealign(context.Background(), ws, nil, RealignOptions{Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	rep := gitStateReport(t, res, ".")
	if rep.Status != GitRepoRealigned || rep.Target != c2 {
		t.Fatalf("status = %q target = %q, want realigned -> %q", rep.Status, rep.Target, c2)
	}
}

// AC2: classification writes no objects and no worktree files. A modified
// tracked file and an untracked file are present so the content test actually
// runs; untracked files never block.
func TestPeerGitStatus_WritesNothing(t *testing.T) {
	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	gitStateInitRepo(t, repo)
	gitStateCommitFile(t, repo, "tracked.txt", "base\n", "base")
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("modified\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	objectsBefore := gitStateObjectCount(t, repo)
	indexBefore := gitStateIndexBytes(t, repo)
	worktreeBefore := gitStateFileBytes(t, filepath.Join(repo, "tracked.txt"))

	res, err := PeerGitStatus(context.Background(), repo, nil)
	if err != nil {
		t.Fatal(err)
	}
	rep := gitStateReport(t, res, ".")
	if rep.Status != GitRepoNoMatch {
		t.Fatalf("status = %q (%s), want no-match (no descendant candidate)", rep.Status, rep.Reason)
	}
	if rep.HeadDiffs != 1 {
		t.Fatalf("head diffs = %d, want 1 (untracked files never count)", rep.HeadDiffs)
	}

	if got := gitStateObjectCount(t, repo); got != objectsBefore {
		t.Fatalf("object count changed: %d -> %d", objectsBefore, got)
	}
	if got := gitStateIndexBytes(t, repo); string(got) != string(indexBefore) {
		t.Fatal("index changed during classification")
	}
	if got := gitStateFileBytes(t, filepath.Join(repo, "tracked.txt")); string(got) != string(worktreeBefore) {
		t.Fatal("worktree changed during classification")
	}
	if _, err := os.Stat(filepath.Join(repo, "untracked.txt")); err != nil {
		t.Fatal("untracked file removed during classification")
	}
}

// The default run is a preview: nothing under .git changes.
func TestPeerGitRealign_PreviewChangesNothing(t *testing.T) {
	ws, sub, c1, _ := gitStateBehindFixture(t)

	headBefore := gitStateHead(t, sub)
	indexBefore := gitStateIndexBytes(t, sub)
	objectsBefore := gitStateObjectCount(t, sub)

	res, err := PeerGitRealign(context.Background(), ws, nil, RealignOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep := gitStateReport(t, res, "sub"); rep.Status != GitRepoRealignable {
		t.Fatalf("preview status = %q, want realignable", rep.Status)
	}
	if got := gitStateHead(t, sub); got != headBefore || got != c1 {
		t.Fatalf("HEAD moved in a preview: %q", got)
	}
	if got := gitStateIndexBytes(t, sub); string(got) != string(indexBefore) {
		t.Fatal("index changed in a preview")
	}
	if got := gitStateObjectCount(t, sub); got != objectsBefore {
		t.Fatalf("object count changed in a preview: %d -> %d", objectsBefore, got)
	}
}

// AC7: locked, in-progress, unmerged and staged repos are skipped and
// reported with a reason; an applied realign leaves them untouched.
func TestPeerGit_SkipConditions(t *testing.T) {
	newRepo := func(t *testing.T) string {
		repo := filepath.Join(t.TempDir(), "repo")
		gitStateInitRepo(t, repo)
		gitStateCommitFile(t, repo, "file.txt", "v1\n", "c1")
		return repo
	}
	gitdir := func(t *testing.T, repo string) string {
		return gitStateRun_(t, repo, "rev-parse", "--absolute-git-dir")
	}

	cases := []struct {
		name       string
		prepare    func(t *testing.T, repo string)
		wantReason string
	}{
		{
			name: "index lock",
			prepare: func(t *testing.T, repo string) {
				if err := os.WriteFile(filepath.Join(gitdir(t, repo), "index.lock"), []byte{}, 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantReason: "index.lock present",
		},
		{
			name: "operation in progress",
			prepare: func(t *testing.T, repo string) {
				if err := os.WriteFile(filepath.Join(gitdir(t, repo), "MERGE_HEAD"), []byte("deadbeef\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantReason: "operation in progress (MERGE_HEAD)",
		},
		{
			name: "unmerged entries",
			prepare: func(t *testing.T, repo string) {
				gitStateRun_(t, repo, "checkout", "-q", "-b", "side")
				gitStateCommitFile(t, repo, "file.txt", "side\n", "side")
				gitStateRun_(t, repo, "checkout", "-q", "main")
				gitStateCommitFile(t, repo, "file.txt", "main\n", "main")
				cmd := exec.Command("git", "merge", "--no-commit", "side")
				cmd.Dir = repo
				// Identity, like gitStateRun_: hosts without a global git
				// identity (CI) refuse even a --no-commit merge, which would
				// silently leave a clean repo instead of the conflict fixture.
				cmd.Env = append(os.Environ(),
					"GIT_AUTHOR_NAME=dot-test", "GIT_AUTHOR_EMAIL=dot-test@example.invalid",
					"GIT_COMMITTER_NAME=dot-test", "GIT_COMMITTER_EMAIL=dot-test@example.invalid",
				)
				_ = cmd.Run() // the conflict is the fixture
				if out := gitStateRun_(t, repo, "ls-files", "-u"); strings.TrimSpace(out) == "" {
					t.Fatal("merge did not produce the conflict fixture")
				}
			},
			wantReason: "unmerged index entries",
		},
		{
			name: "staged changes",
			prepare: func(t *testing.T, repo string) {
				if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("staged\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				gitStateRun_(t, repo, "add", "file.txt")
			},
			wantReason: "staged changes",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newRepo(t)
			tc.prepare(t, repo)
			headBefore := gitStateHead(t, repo)

			res, err := PeerGitStatus(context.Background(), repo, nil)
			if err != nil {
				t.Fatal(err)
			}
			rep := gitStateReport(t, res, ".")
			if rep.Status != GitRepoSkipped {
				t.Fatalf("status = %q, want skipped", rep.Status)
			}
			if rep.Reason != tc.wantReason {
				t.Fatalf("reason = %q, want %q", rep.Reason, tc.wantReason)
			}

			res, err = PeerGitRealign(context.Background(), repo, nil, RealignOptions{Apply: true})
			if err != nil {
				t.Fatal(err)
			}
			if rep := gitStateReport(t, res, "."); rep.Status != GitRepoSkipped {
				t.Fatalf("realign status = %q, want skipped", rep.Status)
			}
			if got := gitStateHead(t, repo); got != headBefore {
				t.Fatalf("HEAD moved on a skipped repo: %q -> %q", headBefore, got)
			}
		})
	}
}

// AC7: a parent gitlink naming a commit the repo does not have marks that
// repo unresolvable, and the run continues with the other repos.
func TestPeerGit_MissingGitlinkObject(t *testing.T) {
	tmp := t.TempDir()
	ws := filepath.Join(tmp, "ws")
	gitStateInitRepo(t, ws)
	gitStateCommitFile(t, ws, "root.txt", "root\n", "root")

	sub := filepath.Join(ws, "sub")
	gitStateInitRepo(t, sub)
	gitStateCommitFile(t, sub, "file.txt", "v1\n", "c1")
	// The repo must need a realign for the vanished gitlink to matter; a
	// clean repo never looks at its candidates.
	gitStateRewriteTracked(t, filepath.Join(sub, "file.txt"), "v2\n")

	dead := "0123456789012345678901234567890123456789"
	gitStateRun_(t, ws, "update-index", "--add", "--cacheinfo", "160000,"+dead+",sub")
	gitStateRun_(t, ws, "commit", "-q", "-m", "record dead gitlink")

	res, err := PeerGitStatus(context.Background(), ws, nil)
	if err != nil {
		t.Fatal(err)
	}
	rep := gitStateReport(t, res, "sub")
	if rep.Status != GitRepoUnresolvable {
		t.Fatalf("status = %q, want unresolvable", rep.Status)
	}
	if !strings.Contains(rep.Reason, "gitlink") {
		t.Fatalf("reason = %q, want a gitlink explanation", rep.Reason)
	}
	// The run continued: the root repo was classified too.
	if root := gitStateReport(t, res, "."); root.Status == "" {
		t.Fatal("root repo was not classified")
	}
}

// A linked worktree at a gitlink path is listed but never realigned.
func TestPeerGit_LinkedWorktreeListedNotRealigned(t *testing.T) {
	tmp := t.TempDir()
	subSrc := filepath.Join(tmp, "sub-src")
	gitStateInitRepo(t, subSrc)
	c1 := gitStateCommitFile(t, subSrc, "file.txt", "v1\n", "c1")

	ws := filepath.Join(tmp, "ws")
	gitStateInitRepo(t, ws)
	gitStateRun_(t, ws, "update-index", "--add", "--cacheinfo", "160000,"+c1+",sub")
	gitStateRun_(t, ws, "commit", "-q", "-m", "record gitlink")

	sub := filepath.Join(ws, "sub")
	gitStateRun_(t, subSrc, "worktree", "add", "--detach", sub, c1)

	res, err := PeerGitRealign(context.Background(), ws, nil, RealignOptions{Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	rep := gitStateReport(t, res, "sub")
	if rep.Status != GitRepoLinkedWorktree {
		t.Fatalf("status = %q (%s), want linked-worktree", rep.Status, rep.Reason)
	}
	if got := gitStateHead(t, sub); got != c1 {
		t.Fatalf("linked worktree HEAD moved: %q", got)
	}
}

// Optional repo arguments restrict the run; an unknown path is an error
// before anything mutates.
func TestPeerGit_RepoRestriction(t *testing.T) {
	ws, _, _, _ := gitStateBehindFixture(t)
	ctx := context.Background()

	res, err := PeerGitStatus(ctx, ws, []string{"sub"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Repos) != 1 || res.Repos[0].Path != "sub" {
		t.Fatalf("restricted run reported %+v, want only sub", res.Repos)
	}

	if _, err := PeerGitStatus(ctx, ws, []string{"nosuch"}); err == nil {
		t.Fatal("unknown repo argument did not error")
	}
	if _, err := PeerGitRealign(ctx, ws, []string{"nosuch"}, RealignOptions{Apply: true}); err == nil {
		t.Fatal("unknown repo argument did not error before an applied realign")
	}
}

// A strict descendant whose tree equals HEAD's (an empty commit, or a
// sequence whose net tree is unchanged) must still be offered: HEAD
// otherwise stays behind forever even though the files already match.
func TestPeerGitRealign_SameTreeDescendant(t *testing.T) {
	tmp := t.TempDir()
	origin := filepath.Join(tmp, "origin")
	gitStateInitRepo(t, origin)
	c1 := gitStateCommitFile(t, origin, "file.txt", "v1\n", "c1")
	gitStateRun_(t, origin, "commit", "-q", "--allow-empty", "-m", "empty")
	c2 := gitStateHead(t, origin)

	ws := filepath.Join(tmp, "ws")
	gitStateRun_(t, tmp, "clone", "-q", origin, ws)
	gitStateRun_(t, ws, "reset", "--hard", "-q", c1)

	res, err := PeerGitStatus(context.Background(), ws, nil)
	if err != nil {
		t.Fatal(err)
	}
	rep := gitStateReport(t, res, ".")
	if rep.Status != GitRepoRealignable || rep.Target != c2 {
		t.Fatalf("status = %q target = %q, want realignable -> same-tree descendant %q", rep.Status, rep.Target, c2)
	}

	res, err = PeerGitRealign(context.Background(), ws, nil, RealignOptions{Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	rep = gitStateReport(t, res, ".")
	if rep.Status != GitRepoRealigned {
		t.Fatalf("status = %q (%s), want realigned", rep.Status, rep.Reason)
	}
	if got := gitStateHead(t, ws); got != c2 {
		t.Fatalf("HEAD = %q, want %q", got, c2)
	}
}

// #177: a parent whose commits after HEAD only bump a child's gitlink ties on
// its own content across every candidate. The child's files already match
// the tip, so one realign run must move the parent to the tip and the child
// to the tip's gitlink, and the preview must say the children decided.
func TestPeerGitRealign_GitlinkOnlyDescendantsFollowChildren(t *testing.T) {
	tmp := t.TempDir()
	subSrc := filepath.Join(tmp, "sub-src")
	gitStateInitRepo(t, subSrc)
	s1 := gitStateCommitFile(t, subSrc, "file.txt", "v1\n", "s1")
	gitStateCommitFile(t, subSrc, "file.txt", "v2\n", "s2")
	s3 := gitStateCommitFile(t, subSrc, "file.txt", "v3\n", "s3")

	origin := filepath.Join(tmp, "origin")
	gitStateInitRepo(t, origin)
	gitStateCommitFile(t, origin, "readme.md", "parent\n", "p0 base")
	gitStateRun_(t, origin, "-c", "protocol.file.allow=always", "submodule", "add", "-q", subSrc, "sub")
	gitStateRun_(t, origin, "-C", "sub", "checkout", "-q", s1)
	gitStateRun_(t, origin, "add", "sub")
	gitStateRun_(t, origin, "commit", "-q", "-m", "p0 add sub at s1")
	p0 := gitStateHead(t, origin)
	for _, rev := range []string{"HEAD~1", "HEAD"} { // gitlink-only commits: s2, then s3
		gitStateRun_(t, origin, "-C", "sub", "checkout", "-q", gitStateRun_(t, subSrc, "rev-parse", rev))
		gitStateRun_(t, origin, "add", "sub")
		gitStateRun_(t, origin, "commit", "-q", "-m", "bump sub")
	}
	tip := gitStateHead(t, origin)

	ws := filepath.Join(tmp, "ws")
	gitStateRun_(t, tmp, "-c", "protocol.file.allow=always", "clone", "-q", "--recurse-submodules", origin, ws)
	// The switched-to Mac: HEADs stayed at p0/s1 while peer sync delivered
	// the tip's files, so the child's worktree holds s3's content.
	gitStateRun_(t, ws, "reset", "-q", "--mixed", p0)
	sub := filepath.Join(ws, "sub")
	gitStateRun_(t, sub, "checkout", "-q", s1)
	gitStateRewriteTracked(t, filepath.Join(sub, "file.txt"), "v3\n")

	ctx := context.Background()
	preview, err := PeerGitRealign(ctx, ws, nil, RealignOptions{})
	if err != nil {
		t.Fatal(err)
	}
	root := gitStateReport(t, preview, ".")
	if root.Status != GitRepoRealignable || root.Target != tip || !strings.Contains(root.TieBreak, "children") {
		t.Fatalf("preview root = %q -> %q tie %q, want realignable -> tip %q decided by children", root.Status, root.Target, root.TieBreak, tip)
	}
	if child := gitStateReport(t, preview, "sub"); child.Status != GitRepoRealignable || child.Target != s3 {
		t.Fatalf("preview child = %q -> %q, want realignable -> %q (the tip's gitlink)", child.Status, child.Target, s3)
	}

	res, err := PeerGitRealign(ctx, ws, nil, RealignOptions{Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := gitStateHead(t, ws); got != tip {
		t.Fatalf("parent HEAD = %s, want the tip %s (status %+v)", got, tip, gitStateReport(t, res, "."))
	}
	if got := gitStateHead(t, sub); got != s3 {
		t.Fatalf("child HEAD = %s, want the tip gitlink %s (status %+v)", got, s3, gitStateReport(t, res, "sub"))
	}
	if out := gitStateRun_(t, ws, "status", "--porcelain"); out != "" {
		t.Fatalf("workspace not clean after one realign run:\n%s", out)
	}
}

// Ties without children fall back to the newest candidate.
func TestPeerGitRealign_TieWithoutChildrenTakesNewest(t *testing.T) {
	tmp := t.TempDir()
	origin := filepath.Join(tmp, "origin")
	gitStateInitRepo(t, origin)
	c1 := gitStateCommitFile(t, origin, "file.txt", "v1\n", "c1")
	gitStateRun_(t, origin, "commit", "-q", "--allow-empty", "-m", "e1")
	gitStateRun_(t, origin, "commit", "-q", "--allow-empty", "-m", "e2")
	tip := gitStateHead(t, origin)

	ws := filepath.Join(tmp, "ws")
	gitStateRun_(t, tmp, "clone", "-q", origin, ws)
	gitStateRun_(t, ws, "reset", "--hard", "-q", c1)

	res, err := PeerGitStatus(context.Background(), ws, nil)
	if err != nil {
		t.Fatal(err)
	}
	rep := gitStateReport(t, res, ".")
	if rep.Target != tip || !strings.Contains(rep.TieBreak, "newest candidate") {
		t.Fatalf("target = %q tie %q, want the newest %q", rep.Target, rep.TieBreak, tip)
	}
}

// A clean parent must not move past an upstream gitlink bump whose content
// its child does not have yet (the bump came from CI or another machine and
// peer sync has not delivered the child's files): HEAD's gitlink matches
// the child, so HEAD wins the children tie-break and the parent stays clean.
func TestPeerGitRealign_CleanParentStaysBehindUndeliveredGitlinkBump(t *testing.T) {
	tmp := t.TempDir()
	subSrc := filepath.Join(tmp, "sub-src")
	gitStateInitRepo(t, subSrc)
	s1 := gitStateCommitFile(t, subSrc, "file.txt", "v1\n", "s1")
	s2 := gitStateCommitFile(t, subSrc, "file.txt", "v2\n", "s2")

	origin := filepath.Join(tmp, "origin")
	gitStateInitRepo(t, origin)
	gitStateCommitFile(t, origin, "readme.md", "parent\n", "base")
	gitStateRun_(t, origin, "-c", "protocol.file.allow=always", "submodule", "add", "-q", subSrc, "sub")
	gitStateRun_(t, origin, "-C", "sub", "checkout", "-q", s1)
	gitStateRun_(t, origin, "add", "sub")
	gitStateRun_(t, origin, "commit", "-q", "-m", "p0")
	p0 := gitStateHead(t, origin)
	gitStateRun_(t, origin, "-C", "sub", "checkout", "-q", s2)
	gitStateRun_(t, origin, "add", "sub")
	gitStateRun_(t, origin, "commit", "-q", "-m", "p1 bump only")

	ws := filepath.Join(tmp, "ws")
	gitStateRun_(t, tmp, "-c", "protocol.file.allow=always", "clone", "-q", "--recurse-submodules", origin, ws)
	gitStateRun_(t, ws, "reset", "-q", "--hard", p0)
	gitStateRun_(t, ws, "-c", "protocol.file.allow=always", "submodule", "update", "-q")

	res, err := PeerGitRealign(context.Background(), ws, nil, RealignOptions{Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep := gitStateReport(t, res, "."); rep.Status != GitRepoAligned || !strings.Contains(rep.TieBreak, "HEAD stays") {
		t.Fatalf("root = %+v, want aligned with HEAD kept", rep)
	}
	if got := gitStateHead(t, ws); got != p0 {
		t.Fatalf("parent moved to %s", got)
	}
	if out := gitStateRun_(t, ws, "status", "--porcelain"); out != "" {
		t.Fatalf("parent not clean:\n%s", out)
	}
}

// Run from a git hook, GIT_DIR and friends would aim every per-repo command
// at the hook's repository; realign clears them like git does for a
// submodule.
func TestPeerGitStatus_IgnoresRepoPinningEnv(t *testing.T) {
	ws, _, _, c2 := gitStateBehindFixture(t)
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "not-a-repo"))
	t.Setenv("GIT_WORK_TREE", t.TempDir())
	res, err := PeerGitStatus(context.Background(), ws, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep := gitStateReport(t, res, "sub"); rep.Status != GitRepoRealignable || rep.Target != c2 {
		t.Fatalf("sub = %+v", rep)
	}
}

// `git -c` settings are the caller's config, kept as git keeps them when it
// enters a submodule; the repo-pinning variables go.
func TestGitCleanEnvKeepsConfigOverrides(t *testing.T) {
	t.Setenv("GIT_DIR", "/elsewhere")
	t.Setenv("GIT_CONFIG_PARAMETERS", "'foo.bar'='baz'")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "safe.directory")
	t.Setenv("GIT_CONFIG_VALUE_0", "*")
	env := strings.Join(gitCleanEnv(context.Background(), "git"), "\n")
	for _, want := range []string{"GIT_CONFIG_PARAMETERS=", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=safe.directory"} {
		if !strings.Contains(env, want) {
			t.Errorf("dropped %s", want)
		}
	}
	if strings.Contains(env, "GIT_DIR=") {
		t.Error("kept GIT_DIR")
	}
}

// A child that lacks a gitlink's commit cannot vote for it, but its known
// answer still counts: the parent stays with HEAD's gitlink, which the child
// matches, and the tie line names the missing commit to fetch.
func TestPeerGitRealign_MissingGitlinkCommitKeepsTheParentAndSaysSo(t *testing.T) {
	tmp := t.TempDir()
	subSrc := filepath.Join(tmp, "sub-src")
	gitStateInitRepo(t, subSrc)
	gitStateCommitFile(t, subSrc, "file.txt", "v1\n", "s1")

	origin := filepath.Join(tmp, "origin")
	gitStateInitRepo(t, origin)
	gitStateCommitFile(t, origin, "readme.md", "parent\n", "base")
	gitStateRun_(t, origin, "-c", "protocol.file.allow=always", "submodule", "add", "-q", subSrc, "sub")
	gitStateRun_(t, origin, "commit", "-q", "-m", "p0")
	p0 := gitStateHead(t, origin)
	// s2 has s1's content and lives only in origin's checkout of sub: the
	// peer's commit that never reached the child's remote.
	gitStateRun_(t, origin, "-C", "sub", "commit", "-q", "--allow-empty", "-m", "s2")
	s2 := gitStateHead(t, filepath.Join(origin, "sub"))
	gitStateRun_(t, origin, "add", "sub")
	gitStateRun_(t, origin, "commit", "-q", "-m", "p1 bump only")

	ws := filepath.Join(tmp, "ws")
	gitStateRun_(t, tmp, "clone", "-q", origin, ws)
	gitStateRun_(t, ws, "reset", "-q", "--hard", p0)
	gitStateRun_(t, ws, "-c", "protocol.file.allow=always", "submodule", "update", "-q", "--init")

	res, err := PeerGitRealign(context.Background(), ws, nil, RealignOptions{})
	if err != nil {
		t.Fatal(err)
	}
	root := gitStateReport(t, res, ".")
	if root.Status != GitRepoAligned || !strings.Contains(root.TieBreak, "HEAD stays") || !strings.Contains(root.TieBreak, "sub lacks "+shortRev(s2)) {
		t.Fatalf("root = %+v, want aligned, HEAD kept, and the missing %s named", root, shortRev(s2))
	}
}

// The unfetched bump changes content (#189 round 10): moving the parent
// would leave it ahead of the child, and a fetch of the child afterwards
// would not bring the files, so the parent stays and stays clean.
func TestPeerGitRealign_CleanParentStaysBehindAnUnfetchedGitlinkBump(t *testing.T) {
	tmp := t.TempDir()
	subSrc := filepath.Join(tmp, "sub-src")
	gitStateInitRepo(t, subSrc)
	gitStateCommitFile(t, subSrc, "file.txt", "v1\n", "s1")

	origin := filepath.Join(tmp, "origin")
	gitStateInitRepo(t, origin)
	gitStateCommitFile(t, origin, "readme.md", "parent\n", "base")
	gitStateRun_(t, origin, "-c", "protocol.file.allow=always", "submodule", "add", "-q", subSrc, "sub")
	gitStateRun_(t, origin, "commit", "-q", "-m", "p0")
	p0 := gitStateHead(t, origin)

	ws := filepath.Join(tmp, "ws")
	gitStateRun_(t, tmp, "-c", "protocol.file.allow=always", "clone", "-q", "--recurse-submodules", origin, ws)

	s2 := gitStateCommitFile(t, subSrc, "file.txt", "v2\n", "s2")
	gitStateRun_(t, origin, "-C", "sub", "fetch", "-q")
	gitStateRun_(t, origin, "-C", "sub", "checkout", "-q", s2)
	gitStateRun_(t, origin, "add", "sub")
	gitStateRun_(t, origin, "commit", "-q", "-m", "p1 bump sub to v2")
	gitStateRun_(t, ws, "fetch", "-q", "--no-recurse-submodules")

	res, err := PeerGitRealign(context.Background(), ws, nil, RealignOptions{Apply: true, Fetch: true})
	if err != nil {
		t.Fatal(err)
	}
	root := gitStateReport(t, res, ".")
	if root.Status != GitRepoAligned || !strings.Contains(root.TieBreak, "HEAD stays") || !strings.Contains(root.TieBreak, "sub lacks "+shortRev(s2)) {
		t.Fatalf("root = %+v, want aligned, HEAD kept, and the missing %s named", root, shortRev(s2))
	}
	if got := gitStateHead(t, ws); got != p0 {
		t.Fatalf("parent moved to %s past a bump the child lacks", got)
	}
	if out := gitStateRun_(t, ws, "status", "--porcelain"); out != "" {
		t.Fatalf("parent not clean:\n%s", out)
	}
}

// A child with uncommitted edits still votes for the commit it was edited
// from (#197): the parent stays behind the bump the child never received,
// also when the edit is in the file the bump changed.
func TestPeerGitRealign_ChildWithEditsStillKeepsTheParentBehind(t *testing.T) {
	for _, edited := range []string{"other.txt", "file.txt"} {
		t.Run(edited, func(t *testing.T) { childWithEditsKeepsTheParentBehind(t, edited) })
	}
}

func childWithEditsKeepsTheParentBehind(t *testing.T, edited string) {
	tmp := t.TempDir()
	subSrc := filepath.Join(tmp, "sub-src")
	gitStateInitRepo(t, subSrc)
	gitStateCommitFile(t, subSrc, "other.txt", "o1\n", "other")
	s1 := gitStateCommitFile(t, subSrc, "file.txt", "v1\n", "s1")
	s2 := gitStateCommitFile(t, subSrc, "file.txt", "v2\n", "s2")

	origin := filepath.Join(tmp, "origin")
	gitStateInitRepo(t, origin)
	gitStateCommitFile(t, origin, "readme.md", "parent\n", "base")
	gitStateRun_(t, origin, "-c", "protocol.file.allow=always", "submodule", "add", "-q", subSrc, "sub")
	gitStateRun_(t, origin, "-C", "sub", "checkout", "-q", s1)
	gitStateRun_(t, origin, "add", "sub")
	gitStateRun_(t, origin, "commit", "-q", "-m", "p0")
	p0 := gitStateHead(t, origin)
	gitStateRun_(t, origin, "-C", "sub", "checkout", "-q", s2)
	gitStateRun_(t, origin, "add", "sub")
	gitStateRun_(t, origin, "commit", "-q", "-m", "p1 bump only")

	ws := filepath.Join(tmp, "ws")
	gitStateRun_(t, tmp, "-c", "protocol.file.allow=always", "clone", "-q", "--recurse-submodules", origin, ws)
	gitStateRun_(t, ws, "reset", "-q", "--hard", p0)
	gitStateRun_(t, ws, "-c", "protocol.file.allow=always", "submodule", "update", "-q")
	gitStateRewriteTracked(t, filepath.Join(ws, "sub", edited), "work in progress\n")

	res, err := PeerGitRealign(context.Background(), ws, nil, RealignOptions{Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep := gitStateReport(t, res, "."); rep.Status != GitRepoAligned || !strings.Contains(rep.TieBreak, "HEAD stays") {
		t.Fatalf("root = %+v, want aligned with HEAD kept", rep)
	}
	if got := gitStateHead(t, ws); got != p0 {
		t.Fatalf("parent moved to %s past a bump the child lacks", got)
	}
}

// A delivered submodule removal: peer sync deletes the child's files but
// never carries its .git, so the checkout stays. The removal has no commit
// to compare with, and the files match HEAD's gitlink no longer: the child
// does not vote, and the parent moves (#189 round 11).
func TestPeerGitRealign_DeliveredSubmoduleRemovalMovesTheParent(t *testing.T) {
	tmp := t.TempDir()
	subSrc := filepath.Join(tmp, "x-src")
	gitStateInitRepo(t, subSrc)
	gitStateCommitFile(t, subSrc, "file.txt", "v1\n", "x1")

	origin := filepath.Join(tmp, "origin")
	gitStateInitRepo(t, origin)
	gitStateCommitFile(t, origin, "readme.md", "parent\n", "base")
	gitStateRun_(t, origin, "-c", "protocol.file.allow=always", "submodule", "add", "-q", subSrc, "x")
	gitStateRun_(t, origin, "commit", "-q", "-m", "p0 add x")
	p0 := gitStateHead(t, origin)
	gitStateRun_(t, origin, "rm", "-q", "x")
	gitStateRun_(t, origin, "commit", "-q", "-m", "p1 remove x")
	p1 := gitStateHead(t, origin)

	ws := filepath.Join(tmp, "ws")
	gitStateRun_(t, tmp, "clone", "-q", origin, ws)
	gitStateRun_(t, ws, "reset", "-q", "--hard", p0)
	gitStateRun_(t, ws, "-c", "protocol.file.allow=always", "submodule", "update", "-q", "--init")
	if err := os.Remove(filepath.Join(ws, "x", "file.txt")); err != nil {
		t.Fatal(err)
	}

	res, err := PeerGitRealign(context.Background(), ws, nil, RealignOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// The preview says why it moves, though no rule had to choose (#177).
	if root := gitStateReport(t, res, "."); root.Status != GitRepoRealignable || root.Target != p1 || !strings.Contains(root.TieBreak, "no child tells them apart") {
		t.Fatalf("root = %+v, want realignable to the removal %s with its tie line", root, p1)
	}
}

// work -> dev -> dev/maru: a dev commit that only bumps maru ties with its
// parent on dev's own content, so maru's content decides for dev too. The
// root stays behind an undelivered nested bump and moves for a delivered
// one.
func TestPeerGitRealign_NestedChildrenDecideTheMiddleRepo(t *testing.T) {
	for _, tc := range []struct{ delivered, devEdited bool }{{false, false}, {true, false}, {true, true}} {
		delivered := tc.delivered
		t.Run(fmt.Sprintf("delivered=%v devEdited=%v", tc.delivered, tc.devEdited), func(t *testing.T) {
			tmp := t.TempDir()
			maruSrc := filepath.Join(tmp, "maru-src")
			gitStateInitRepo(t, maruSrc)
			m0 := gitStateCommitFile(t, maruSrc, "file.txt", "v1\n", "m0")
			m1 := gitStateCommitFile(t, maruSrc, "file.txt", "v2\n", "m1")

			devSrc := filepath.Join(tmp, "dev-src")
			gitStateInitRepo(t, devSrc)
			gitStateCommitFile(t, devSrc, "readme.md", "dev\n", "base")
			gitStateRun_(t, devSrc, "-c", "protocol.file.allow=always", "submodule", "add", "-q", maruSrc, "maru")
			gitStateRun_(t, devSrc, "-C", "maru", "checkout", "-q", m0)
			gitStateRun_(t, devSrc, "add", "maru")
			gitStateRun_(t, devSrc, "commit", "-q", "-m", "d0")
			d0 := gitStateHead(t, devSrc)
			gitStateRun_(t, devSrc, "-C", "maru", "checkout", "-q", m1)
			gitStateRun_(t, devSrc, "add", "maru")
			gitStateRun_(t, devSrc, "commit", "-q", "-m", "d1 bump maru only")
			d1 := gitStateHead(t, devSrc)

			origin := filepath.Join(tmp, "origin")
			gitStateInitRepo(t, origin)
			gitStateCommitFile(t, origin, "readme.md", "work\n", "base")
			gitStateRun_(t, origin, "-c", "protocol.file.allow=always", "submodule", "add", "-q", devSrc, "dev")
			gitStateRun_(t, origin, "-C", "dev", "checkout", "-q", d0)
			gitStateRun_(t, origin, "add", "dev")
			gitStateRun_(t, origin, "commit", "-q", "-m", "p0")
			p0 := gitStateHead(t, origin)
			gitStateRun_(t, origin, "-C", "dev", "checkout", "-q", d1)
			gitStateRun_(t, origin, "add", "dev")
			gitStateRun_(t, origin, "commit", "-q", "-m", "p1 bump dev only")
			p1 := gitStateHead(t, origin)

			ws := filepath.Join(tmp, "ws")
			gitStateRun_(t, tmp, "-c", "protocol.file.allow=always", "clone", "-q", "--recurse-submodules", origin, ws)
			gitStateRun_(t, ws, "reset", "-q", "--hard", p0)
			gitStateRun_(t, ws, "-c", "protocol.file.allow=always", "submodule", "update", "-q", "--recursive")
			if delivered {
				gitStateRewriteTracked(t, filepath.Join(ws, "dev", "maru", "file.txt"), "v2\n")
			}
			if tc.devEdited {
				// An edit no commit touches: dev's commits still differ in
				// their children only, so maru decides.
				gitStateRewriteTracked(t, filepath.Join(ws, "dev", "readme.md"), "dev, edited\n")
			}

			res, err := PeerGitRealign(context.Background(), ws, nil, RealignOptions{Apply: true})
			if err != nil {
				t.Fatal(err)
			}
			root := gitStateReport(t, res, ".")
			if !delivered {
				if root.Status != GitRepoAligned || !strings.Contains(root.TieBreak, "HEAD stays") || gitStateHead(t, ws) != p0 {
					t.Fatalf("root = %+v, want HEAD kept behind the undelivered nested bump", root)
				}
				if out := gitStateRun_(t, ws, "status", "--porcelain"); out != "" {
					t.Fatalf("workspace not clean:\n%s", out)
				}
				return
			}
			if got := gitStateHead(t, ws); got != p1 {
				t.Fatalf("root HEAD = %s, want %s (%+v)", got, p1, root)
			}
		})
	}
}

// The child holds a commit between HEAD's gitlink and the candidate's
// (the sending Mac's own state): the candidate records a commit the child's
// content has not reached, so the parent stays, and the child moves to its
// own match (#189 round 12).
func TestPeerGitRealign_ChildBetweenTheGitlinksKeepsTheParent(t *testing.T) {
	// unfetched: the child lacks the candidate's s3, so only the child's
	// exact match at s2 says the candidate is not where it is.
	for _, unfetched := range []bool{false, true} {
		t.Run(fmt.Sprintf("unfetched=%v", unfetched), func(t *testing.T) { childBetweenTheGitlinks(t, unfetched) })
	}
}

func childBetweenTheGitlinks(t *testing.T, unfetched bool) {
	tmp := t.TempDir()
	subSrc := filepath.Join(tmp, "sub-src")
	gitStateInitRepo(t, subSrc)
	for _, f := range []string{"a", "b", "c"} {
		gitStateCommitFile(t, subSrc, f, "1\n", f+"1")
	}
	s1 := gitStateCommitFile(t, subSrc, "d", "1\n", "s1")
	for _, f := range []string{"a", "b"} {
		gitStateCommitFile(t, subSrc, f, "2\n", f+"2")
	}
	s2 := gitStateCommitFile(t, subSrc, "c", "2\n", "s2")
	s3 := ""
	if !unfetched {
		s3 = gitStateCommitFile(t, subSrc, "d", "3\n", "s3")
	}

	origin := filepath.Join(tmp, "origin")
	gitStateInitRepo(t, origin)
	gitStateCommitFile(t, origin, "readme.md", "parent\n", "base")
	gitStateRun_(t, origin, "-c", "protocol.file.allow=always", "submodule", "add", "-q", subSrc, "sub")
	gitStateRun_(t, origin, "-C", "sub", "checkout", "-q", s1)
	gitStateRun_(t, origin, "add", "sub")
	gitStateRun_(t, origin, "commit", "-q", "-m", "p0")
	p0 := gitStateHead(t, origin)
	ws := filepath.Join(tmp, "ws")
	if unfetched {
		gitStateRun_(t, tmp, "-c", "protocol.file.allow=always", "clone", "-q", "--recurse-submodules", origin, ws)
		s3 = gitStateCommitFile(t, subSrc, "d", "3\n", "s3")
		gitStateRun_(t, origin, "-C", "sub", "fetch", "-q")
	}
	gitStateRun_(t, origin, "-C", "sub", "checkout", "-q", s3)
	gitStateRun_(t, origin, "add", "sub")
	gitStateRun_(t, origin, "commit", "-q", "-m", "p1 bump to s3")

	if unfetched {
		gitStateRun_(t, ws, "fetch", "-q", "--no-recurse-submodules")
	} else {
		gitStateRun_(t, tmp, "-c", "protocol.file.allow=always", "clone", "-q", "--recurse-submodules", origin, ws)
	}
	gitStateRun_(t, ws, "reset", "-q", "--hard", p0)
	gitStateRun_(t, ws, "-c", "protocol.file.allow=always", "submodule", "update", "-q")
	sub := filepath.Join(ws, "sub")
	gitStateRun_(t, sub, "checkout", "-q", "-B", "main", s1)
	gitStateRun_(t, sub, "branch", "-q", "--set-upstream-to=origin/main")
	for _, f := range []string{"a", "b", "c"} {
		gitStateRewriteTracked(t, filepath.Join(sub, f), "2\n")
	}

	res, err := PeerGitRealign(context.Background(), ws, nil, RealignOptions{Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if root := gitStateReport(t, res, "."); root.Status != GitRepoAligned || !strings.Contains(root.TieBreak, "HEAD stays") || gitStateHead(t, ws) != p0 {
		t.Fatalf("root = %+v, want HEAD kept below the child's s2", root)
	}
	if got := gitStateHead(t, sub); got != s2 {
		t.Fatalf("child HEAD = %s, want its own match %s", got, s2)
	}
	if log := gitStateRun_(t, ws, "diff", "--submodule=log"); strings.Contains(log, "rewind") {
		t.Fatalf("the parent records a rewind:\n%s", log)
	}
}

// A submodule added upstream whose checkout never arrived: moving would
// leave it deleted in the worktree (a commit -a records the removal), so
// the parent stays. A delivered one (files, no .git) moves.
func TestPeerGitRealign_UndeliveredSubmoduleAdditionKeepsTheParent(t *testing.T) {
	for _, delivered := range []bool{false, true} {
		t.Run(fmt.Sprintf("delivered=%v", delivered), func(t *testing.T) {
			tmp := t.TempDir()
			xSrc := filepath.Join(tmp, "x-src")
			gitStateInitRepo(t, xSrc)
			gitStateCommitFile(t, xSrc, "file.txt", "v1\n", "x1")

			origin := filepath.Join(tmp, "origin")
			gitStateInitRepo(t, origin)
			p0 := gitStateCommitFile(t, origin, "readme.md", "parent\n", "p0")
			gitStateRun_(t, origin, "-c", "protocol.file.allow=always", "submodule", "add", "-q", xSrc, "x")
			gitStateRun_(t, origin, "commit", "-q", "-m", "p1 add x")
			p1 := gitStateHead(t, origin)

			ws := filepath.Join(tmp, "ws")
			gitStateRun_(t, tmp, "clone", "-q", origin, ws)
			gitStateRun_(t, ws, "reset", "-q", "--hard", p0)
			if err := os.RemoveAll(filepath.Join(ws, "x")); err != nil {
				t.Fatal(err)
			}
			if delivered {
				if err := os.MkdirAll(filepath.Join(ws, "x"), 0o755); err != nil {
					t.Fatal(err)
				}
				gitStateRewriteTracked(t, filepath.Join(ws, "x", "file.txt"), "v1\n")
			}

			res, err := PeerGitRealign(context.Background(), ws, nil, RealignOptions{Apply: true})
			if err != nil {
				t.Fatal(err)
			}
			root := gitStateReport(t, res, ".")
			if !delivered {
				if root.Status != GitRepoAligned || gitStateHead(t, ws) != p0 {
					t.Fatalf("root = %+v, want HEAD kept", root)
				}
				if out := gitStateRun_(t, ws, "status", "--porcelain"); out != "" {
					t.Fatalf("workspace not clean:\n%s", out)
				}
				return
			}
			if got := gitStateHead(t, ws); got != p1 {
				t.Fatalf("root HEAD = %s, want %s (%+v)", got, p1, root)
			}
		})
	}
}

// A child this run leaves alone (a stale index.lock, or outside the
// restriction) stays at its HEAD whatever its files show: the parent must
// not move past it, or it records a rewind (#189 round 13). Also when the
// child lacks the candidate's commit: HEAD cannot reach it (round 14).
func TestPeerGitRealign_ParentStaysWithAChildLeftAlone(t *testing.T) {
	for _, tc := range []struct {
		name      string
		lock      bool
		restrict  []string
		unfetched bool
	}{
		{"lock", true, nil, false},
		{"restricted to the parent", false, []string{"."}, false},
		{"lock, unfetched", true, nil, true},
		{"restricted to the parent, unfetched", false, []string{"."}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			subSrc := filepath.Join(tmp, "sub-src")
			gitStateInitRepo(t, subSrc)
			s1 := gitStateCommitFile(t, subSrc, "file.txt", "v1\n", "s1")
			s2 := ""
			if !tc.unfetched {
				s2 = gitStateCommitFile(t, subSrc, "file.txt", "v2\n", "s2")
			}
			origin := filepath.Join(tmp, "origin")
			gitStateInitRepo(t, origin)
			gitStateCommitFile(t, origin, "readme.md", "parent\n", "base")
			gitStateRun_(t, origin, "-c", "protocol.file.allow=always", "submodule", "add", "-q", subSrc, "sub")
			gitStateRun_(t, origin, "-C", "sub", "checkout", "-q", s1)
			gitStateRun_(t, origin, "add", "sub")
			gitStateRun_(t, origin, "commit", "-q", "-m", "p0")
			p0 := gitStateHead(t, origin)
			ws := filepath.Join(tmp, "ws")
			if tc.unfetched {
				gitStateRun_(t, tmp, "-c", "protocol.file.allow=always", "clone", "-q", "--recurse-submodules", origin, ws)
				s2 = gitStateCommitFile(t, subSrc, "file.txt", "v2\n", "s2")
				gitStateRun_(t, origin, "-C", "sub", "fetch", "-q")
			}
			gitStateRun_(t, origin, "-C", "sub", "checkout", "-q", s2)
			gitStateRun_(t, origin, "add", "sub")
			gitStateRun_(t, origin, "commit", "-q", "-m", "p1")

			if tc.unfetched {
				gitStateRun_(t, ws, "fetch", "-q", "--no-recurse-submodules")
			} else {
				gitStateRun_(t, tmp, "-c", "protocol.file.allow=always", "clone", "-q", "--recurse-submodules", origin, ws)
			}
			gitStateRun_(t, ws, "reset", "-q", "--hard", p0)
			gitStateRun_(t, ws, "-c", "protocol.file.allow=always", "submodule", "update", "-q")
			sub := filepath.Join(ws, "sub")
			gitStateRewriteTracked(t, filepath.Join(sub, "file.txt"), "v2\n") // delivered
			if tc.lock {
				gitdir := gitStateRun_(t, sub, "rev-parse", "--absolute-git-dir")
				if err := os.WriteFile(filepath.Join(gitdir, "index.lock"), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			res, err := PeerGitRealign(context.Background(), ws, tc.restrict, RealignOptions{Apply: true})
			if err != nil {
				t.Fatal(err)
			}
			if root := gitStateReport(t, res, "."); gitStateHead(t, ws) != p0 || !strings.Contains(root.TieBreak, "left alone") {
				t.Fatalf("root = %+v, want HEAD kept with the child named", root)
			}
			if log := gitStateRun_(t, ws, "diff", "--submodule=log"); strings.Contains(log, "rewind") {
				t.Fatalf("the parent records a rewind:\n%s", log)
			}
		})
	}
}

// Same-content commits in a grandchild: "newest" is the one that descends
// from the others, whichever order a run lists them in, so the parent's
// question and the middle repo's own run agree (#189 round 13).
func TestPeerGitRealign_NewestDoesNotDependOnOrder(t *testing.T) {
	tmp := t.TempDir()
	xSrc := filepath.Join(tmp, "x-src")
	gitStateInitRepo(t, xSrc)
	x0 := gitStateCommitFile(t, xSrc, "file.txt", "v0\n", "x0")
	x1 := gitStateCommitFile(t, xSrc, "file.txt", "v1\n", "x1")
	gitStateRun_(t, xSrc, "commit", "-q", "--allow-empty", "-m", "x2 empty")
	x2 := gitStateHead(t, xSrc)

	mSrc := filepath.Join(tmp, "m-src")
	gitStateInitRepo(t, mSrc)
	gitStateCommitFile(t, mSrc, "readme.md", "m\n", "base")
	gitStateRun_(t, mSrc, "-c", "protocol.file.allow=always", "submodule", "add", "-q", xSrc, "x")
	var ms []string
	for _, x := range []string{x0, x1, x2} {
		gitStateRun_(t, mSrc, "-C", "x", "checkout", "-q", x)
		gitStateRun_(t, mSrc, "add", "x")
		gitStateRun_(t, mSrc, "commit", "-q", "-m", "m records "+shortRev(x))
		ms = append(ms, gitStateHead(t, mSrc))
	}

	origin := filepath.Join(tmp, "origin")
	gitStateInitRepo(t, origin)
	gitStateCommitFile(t, origin, "readme.md", "root\n", "base")
	gitStateRun_(t, origin, "-c", "protocol.file.allow=always", "submodule", "add", "-q", mSrc, "m")
	gitStateRun_(t, origin, "-C", "m", "checkout", "-q", ms[0])
	gitStateRun_(t, origin, "add", "m")
	gitStateRun_(t, origin, "commit", "-q", "-m", "r0")
	r0 := gitStateHead(t, origin)
	gitStateRun_(t, origin, "-C", "m", "checkout", "-q", ms[2])
	gitStateRun_(t, origin, "add", "m")
	gitStateRun_(t, origin, "commit", "-q", "-m", "r1")

	ws := filepath.Join(tmp, "ws")
	gitStateRun_(t, tmp, "-c", "protocol.file.allow=always", "clone", "-q", "--recurse-submodules", origin, ws)
	gitStateRun_(t, ws, "reset", "-q", "--hard", r0)
	gitStateRun_(t, ws, "-c", "protocol.file.allow=always", "submodule", "update", "-q", "--recursive")
	m := filepath.Join(ws, "m")
	gitStateRun_(t, m, "checkout", "-q", "-B", "main", ms[0])
	gitStateRun_(t, m, "branch", "-q", "--set-upstream-to=origin/main")
	gitStateRewriteTracked(t, filepath.Join(m, "x", "file.txt"), "v1\n")

	if _, err := PeerGitRealign(context.Background(), ws, nil, RealignOptions{Apply: true}); err != nil {
		t.Fatal(err)
	}
	if log := gitStateRun_(t, ws, "diff", "--submodule=log"); strings.Contains(log, "rewind") {
		t.Fatalf("the root records a rewind:\n%s", log)
	}
}

// Children that disagree keep HEAD: moving would take one child's bump in
// and leave the other's content behind its new gitlink. A count of votes
// ties here, and the tie went to the candidate.
func TestPeerGitRealign_ChildrenThatDisagreeKeepHEAD(t *testing.T) {
	tmp := t.TempDir()
	origin := filepath.Join(tmp, "origin")
	gitStateInitRepo(t, origin)
	gitStateCommitFile(t, origin, "readme.md", "parent\n", "base")
	bumps := map[string]string{}
	for _, name := range []string{"a", "b"} {
		src := filepath.Join(tmp, name+"-src")
		gitStateInitRepo(t, src)
		first := gitStateCommitFile(t, src, "file.txt", name+"1\n", "1")
		bumps[name] = gitStateCommitFile(t, src, "file.txt", name+"2\n", "2")
		gitStateRun_(t, origin, "-c", "protocol.file.allow=always", "submodule", "add", "-q", src, name)
		gitStateRun_(t, origin, "-C", name, "checkout", "-q", first)
		gitStateRun_(t, origin, "add", name)
	}
	gitStateRun_(t, origin, "commit", "-q", "-m", "p0")
	p0 := gitStateHead(t, origin)
	for name, bump := range bumps {
		gitStateRun_(t, origin, "-C", name, "checkout", "-q", bump)
		gitStateRun_(t, origin, "add", name)
	}
	gitStateRun_(t, origin, "commit", "-q", "-m", "p1 bump a and b")

	ws := filepath.Join(tmp, "ws")
	gitStateRun_(t, tmp, "-c", "protocol.file.allow=always", "clone", "-q", "--recurse-submodules", origin, ws)
	gitStateRun_(t, ws, "reset", "-q", "--hard", p0)
	gitStateRun_(t, ws, "-c", "protocol.file.allow=always", "submodule", "update", "-q")
	// Peer sync delivered a's bump; b's never came.
	gitStateRewriteTracked(t, filepath.Join(ws, "a", "file.txt"), "a2\n")

	res, err := PeerGitRealign(context.Background(), ws, nil, RealignOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep := gitStateReport(t, res, "."); rep.Status != GitRepoAligned || !strings.Contains(rep.TieBreak, "HEAD stays") {
		t.Fatalf("root = %+v, want aligned with HEAD kept", rep)
	}
}

// The parent's own content forces a move and every candidate records a
// gitlink the child has not reached: the newest is taken, and the tie line
// says the child is behind all of them before a commit -a records a rewind
// (#189 round 16).
func TestPeerGitRealign_ForcedMovePastEveryChildSaysSo(t *testing.T) {
	tmp := t.TempDir()
	subSrc := filepath.Join(tmp, "sub-src")
	gitStateInitRepo(t, subSrc)
	s0 := gitStateCommitFile(t, subSrc, "file.txt", "v0\n", "s0")
	s1 := gitStateCommitFile(t, subSrc, "file.txt", "v1\n", "s1")
	s2 := gitStateCommitFile(t, subSrc, "file.txt", "v2\n", "s2")

	origin := filepath.Join(tmp, "origin")
	gitStateInitRepo(t, origin)
	gitStateCommitFile(t, origin, "readme.md", "parent\n", "base")
	gitStateRun_(t, origin, "-c", "protocol.file.allow=always", "submodule", "add", "-q", subSrc, "sub")
	gitStateRun_(t, origin, "-C", "sub", "checkout", "-q", s0)
	gitStateRun_(t, origin, "add", "sub")
	gitStateRun_(t, origin, "commit", "-q", "-m", "p0")
	p0 := gitStateHead(t, origin)
	gitStateRun_(t, origin, "-C", "sub", "checkout", "-q", s1)
	gitStateRun_(t, origin, "add", "sub")
	gitStateCommitFile(t, origin, "readme.md", "parent v2\n", "p1 readme and bump s1")
	gitStateRun_(t, origin, "-C", "sub", "checkout", "-q", s2)
	gitStateRun_(t, origin, "add", "sub")
	gitStateRun_(t, origin, "commit", "-q", "-m", "p2 bump s2")

	ws := filepath.Join(tmp, "ws")
	gitStateRun_(t, tmp, "-c", "protocol.file.allow=always", "clone", "-q", "--recurse-submodules", origin, ws)
	gitStateRun_(t, ws, "reset", "-q", "--hard", p0)
	gitStateRun_(t, ws, "-c", "protocol.file.allow=always", "submodule", "update", "-q")
	gitStateRewriteTracked(t, filepath.Join(ws, "readme.md"), "parent v2\n")

	res, err := PeerGitRealign(context.Background(), ws, nil, RealignOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if root := gitStateReport(t, res, "."); root.Status != GitRepoRealignable || !strings.Contains(root.TieBreak, "is past what sub holds") {
		t.Fatalf("root = %+v, want a move whose tie line names sub", root)
	}
}

// The run's caches take only full commit ids: a ref name scored twice
// around a move of that ref gets two answers (#189 round 16).
func TestGitStateCachesSkipRefNames(t *testing.T) {
	repo := t.TempDir()
	gitStateInitRepo(t, repo)
	a := gitStateCommitFile(t, repo, "f.txt", "a\n", "a")
	b := gitStateCommitFile(t, repo, "f.txt", "b\n", "b")
	gitStateRun_(t, repo, "update-ref", "refs/heads/x", a)
	r := &gitStateRun{git: "git"}
	ctx := context.Background()
	gitdir, err := r.gitDir(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	before, err := r.contentDiffs(ctx, repo, gitdir, "refs/heads/x")
	if err != nil || before != 1 {
		t.Fatalf("diffs against a = %d, %v; want 1", before, err)
	}
	gitStateRun_(t, repo, "update-ref", "refs/heads/x", b)
	if after, err := r.contentDiffs(ctx, repo, gitdir, "refs/heads/x"); err != nil || after != 0 {
		t.Fatalf("diffs after the ref moved to b = %d, %v; want 0 (a stale cached answer?)", after, err)
	}
}

// A parent's question to a child is the child's own turn with the gitlink
// the contender records, so the parent never records a commit the child's
// turn will not take (#189 round 17). Single level: the newest contender
// reverts the child's gitlink to a commit whose files the child is past.
func TestPeerGitRealign_ParentFollowsTheChildsOwnTurn(t *testing.T) {
	tmp := t.TempDir()
	subSrc := filepath.Join(tmp, "sub-src")
	gitStateInitRepo(t, subSrc)
	gitStateCommitFile(t, subSrc, "a.txt", "0\n", "a0")
	gitStateCommitFile(t, subSrc, "c.txt", "0\n", "c0 file")
	c0 := gitStateCommitFile(t, subSrc, "b.txt", "0\n", "c0")
	cG := gitStateCommitFile(t, subSrc, "a.txt", "G\n", "cG")
	gitStateCommitFile(t, subSrc, "a.txt", "0\n", "revert a")
	cT := gitStateCommitFile(t, subSrc, "b.txt", "T\n", "cT")

	origin := filepath.Join(tmp, "origin")
	gitStateInitRepo(t, origin)
	gitStateCommitFile(t, origin, "readme.md", "parent\n", "base")
	gitStateRun_(t, origin, "-c", "protocol.file.allow=always", "submodule", "add", "-q", subSrc, "sub")
	for _, c := range []struct{ sha, msg string }{{c0, "r0"}, {cT, "r1 sub@cT"}, {cG, "r2 sub@cG"}} {
		gitStateRun_(t, origin, "-C", "sub", "checkout", "-q", c.sha)
		gitStateRun_(t, origin, "add", "sub")
		gitStateRun_(t, origin, "commit", "-q", "-m", c.msg)
	}
	r0 := gitStateRun_(t, origin, "rev-parse", "HEAD~2")
	r1 := gitStateRun_(t, origin, "rev-parse", "HEAD~1")

	ws := filepath.Join(tmp, "ws")
	gitStateRun_(t, tmp, "-c", "protocol.file.allow=always", "clone", "-q", "--recurse-submodules", origin, ws)
	gitStateRun_(t, ws, "reset", "-q", "--hard", r0)
	gitStateRun_(t, ws, "-c", "protocol.file.allow=always", "submodule", "update", "-q")
	sub := filepath.Join(ws, "sub")
	gitStateRewriteTracked(t, filepath.Join(sub, "b.txt"), "T\n") // cT's files delivered
	gitStateRewriteTracked(t, filepath.Join(sub, "c.txt"), "wip\n")

	if _, err := PeerGitRealign(context.Background(), ws, nil, RealignOptions{Apply: true}); err != nil {
		t.Fatal(err)
	}
	if got := gitStateHead(t, ws); got != r1 {
		t.Fatalf("parent HEAD = %s, want r1 %s (the contender the child's turn follows)", got, r1)
	}
	if got := gitStateHead(t, sub); got != cT {
		t.Fatalf("child HEAD = %s, want %s", got, cT)
	}
	if log := gitStateRun_(t, ws, "diff", "--submodule=log"); strings.Contains(log, "rewind") {
		t.Fatalf("the parent records a rewind:\n%s", log)
	}
}

// Nested, the workspace shape: root -> dev -> maru, dev's upstream past
// the root's record, maru holding its tip's files with edits. Every level
// ends where its own turn takes it, and nothing records a rewind.
func TestPeerGitRealign_NestedTurnsAgree(t *testing.T) {
	tmp := t.TempDir()
	maruSrc := filepath.Join(tmp, "maru-src")
	gitStateInitRepo(t, maruSrc)
	gitStateCommitFile(t, maruSrc, "w.txt", "0\n", "w")
	m0 := gitStateCommitFile(t, maruSrc, "a.txt", "0\n", "m0")
	mG := gitStateCommitFile(t, maruSrc, "a.txt", "G\n", "mG")
	mT := gitStateCommitFile(t, maruSrc, "a.txt", "T\n", "mT")

	devSrc := filepath.Join(tmp, "dev-src")
	gitStateInitRepo(t, devSrc)
	gitStateCommitFile(t, devSrc, "readme.md", "dev\n", "base")
	gitStateRun_(t, devSrc, "-c", "protocol.file.allow=always", "submodule", "add", "-q", maruSrc, "maru")
	var ds []string
	for _, m := range []string{m0, mG, mT} {
		gitStateRun_(t, devSrc, "-C", "maru", "checkout", "-q", m)
		gitStateRun_(t, devSrc, "add", "maru")
		gitStateRun_(t, devSrc, "commit", "-q", "-m", "d@"+shortRev(m))
		ds = append(ds, gitStateHead(t, devSrc))
	}
	d0, d1, d2 := ds[0], ds[1], ds[2]

	origin := filepath.Join(tmp, "origin")
	gitStateInitRepo(t, origin)
	gitStateCommitFile(t, origin, "readme.md", "work\n", "base")
	gitStateRun_(t, origin, "-c", "protocol.file.allow=always", "submodule", "add", "-q", devSrc, "dev")
	var ws0 []string
	for _, d := range []string{d0, d1} {
		gitStateRun_(t, origin, "-C", "dev", "checkout", "-q", d)
		gitStateRun_(t, origin, "add", "dev")
		gitStateRun_(t, origin, "commit", "-q", "-m", "w@"+shortRev(d))
		ws0 = append(ws0, gitStateHead(t, origin))
	}
	w0, w1 := ws0[0], ws0[1]

	ws := filepath.Join(tmp, "ws")
	gitStateRun_(t, tmp, "-c", "protocol.file.allow=always", "clone", "-q", "--recurse-submodules", origin, ws)
	gitStateRun_(t, ws, "reset", "-q", "--hard", w0)
	gitStateRun_(t, ws, "-c", "protocol.file.allow=always", "submodule", "update", "-q", "--recursive")
	dev, maru := filepath.Join(ws, "dev"), filepath.Join(ws, "dev", "maru")
	gitStateRun_(t, dev, "checkout", "-q", "-B", "main", d0)
	gitStateRun_(t, dev, "branch", "-q", "--set-upstream-to=origin/main")
	gitStateRewriteTracked(t, filepath.Join(maru, "a.txt"), "T\n") // mT's files delivered
	gitStateRewriteTracked(t, filepath.Join(maru, "w.txt"), "wip\n")

	if _, err := PeerGitRealign(context.Background(), ws, nil, RealignOptions{Apply: true}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ repo, want, name string }{{ws, w1, "root"}, {dev, d2, "dev"}, {maru, mT, "maru"}} {
		if got := gitStateHead(t, c.repo); got != c.want {
			t.Errorf("%s HEAD = %s, want %s", c.name, shortRev(got), shortRev(c.want))
		}
	}
	for _, repo := range []string{ws, dev} {
		if log := gitStateRun_(t, repo, "diff", "--submodule=log"); strings.Contains(log, "rewind") {
			t.Errorf("%s records a rewind:\n%s", repo, log)
		}
	}
}

// A forced move with a single candidate warns too (#189 round 20): the
// parent's own content needs p1, which records a gitlink the child is not at.
func TestPeerGitRealign_ForcedSingleMoveSaysSo(t *testing.T) {
	tmp := t.TempDir()
	subSrc := filepath.Join(tmp, "sub-src")
	gitStateInitRepo(t, subSrc)
	s0 := gitStateCommitFile(t, subSrc, "file.txt", "v0\n", "s0")
	s1 := gitStateCommitFile(t, subSrc, "file.txt", "v1\n", "s1")
	origin := filepath.Join(tmp, "origin")
	gitStateInitRepo(t, origin)
	gitStateCommitFile(t, origin, "readme.md", "parent\n", "base")
	gitStateRun_(t, origin, "-c", "protocol.file.allow=always", "submodule", "add", "-q", subSrc, "sub")
	gitStateRun_(t, origin, "-C", "sub", "checkout", "-q", s0)
	gitStateRun_(t, origin, "add", "sub")
	gitStateRun_(t, origin, "commit", "-q", "-m", "p0")
	p0 := gitStateHead(t, origin)
	gitStateRun_(t, origin, "-C", "sub", "checkout", "-q", s1)
	gitStateRun_(t, origin, "add", "sub")
	gitStateCommitFile(t, origin, "readme.md", "parent v2\n", "p1 readme and bump s1")
	ws := filepath.Join(tmp, "ws")
	gitStateRun_(t, tmp, "-c", "protocol.file.allow=always", "clone", "-q", "--recurse-submodules", origin, ws)
	gitStateRun_(t, ws, "reset", "-q", "--hard", p0)
	gitStateRun_(t, ws, "-c", "protocol.file.allow=always", "submodule", "update", "-q")
	gitStateRewriteTracked(t, filepath.Join(ws, "readme.md"), "parent v2\n")

	res, err := PeerGitRealign(context.Background(), ws, nil, RealignOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if root := gitStateReport(t, res, "."); root.Status != GitRepoRealignable || !strings.Contains(root.TieBreak, "is past what sub holds") {
		t.Fatalf("root = %+v, want a move whose tie line names sub", root)
	}
}

func TestLackingGroupsPerPath(t *testing.T) {
	got := lacking([]string{"leaf lacks a1", "leaf lacks a2", "other lacks b1", "leaf lacks a3"})
	if got != "leaf lacks 3 commits (a1, ...), other lacks b1" {
		t.Fatalf("lacking = %q", got)
	}
}

// A submodule the move adds whose files arrived without a .git is nothing
// a commit -a can rewind: the forced move's tie line leaves it out
// (#189 round 21).
func TestPeerGitRealign_ForcedMoveIgnoresADeliveredAddition(t *testing.T) {
	tmp := t.TempDir()
	xSrc := filepath.Join(tmp, "x-src")
	gitStateInitRepo(t, xSrc)
	gitStateCommitFile(t, xSrc, "file.txt", "v1\n", "x1")
	origin := filepath.Join(tmp, "origin")
	gitStateInitRepo(t, origin)
	p0 := gitStateCommitFile(t, origin, "readme.md", "parent\n", "p0")
	gitStateRun_(t, origin, "-c", "protocol.file.allow=always", "submodule", "add", "-q", xSrc, "x")
	gitStateCommitFile(t, origin, "readme.md", "parent v2\n", "p1 readme and add x")
	ws := filepath.Join(tmp, "ws")
	gitStateRun_(t, tmp, "clone", "-q", origin, ws)
	gitStateRun_(t, ws, "reset", "-q", "--hard", p0)
	if err := os.RemoveAll(filepath.Join(ws, "x")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(ws, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitStateRewriteTracked(t, filepath.Join(ws, "x", "file.txt"), "v1\n")
	gitStateRewriteTracked(t, filepath.Join(ws, "readme.md"), "parent v2\n")

	res, err := PeerGitRealign(context.Background(), ws, nil, RealignOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if root := gitStateReport(t, res, "."); root.Status != GitRepoRealignable || strings.Contains(root.TieBreak, "past what x") {
		t.Fatalf("root = %+v, want a move that does not warn about x", root)
	}
}

// fetchFixture is a clean parent at p0 whose upstream p1 bumps sub to s2,
// a commit made after the workspace was cloned, so its child lacks it and
// can fetch it from its origin; the child has an edit in other.txt, and
// with delivered it holds s2's file.txt too.
func fetchFixture(t *testing.T, delivered bool) (ws, sub, p0, p1, s2 string) {
	t.Helper()
	tmp := t.TempDir()
	subSrc := filepath.Join(tmp, "sub-src")
	gitStateInitRepo(t, subSrc)
	gitStateCommitFile(t, subSrc, "other.txt", "o\n", "other")
	gitStateCommitFile(t, subSrc, "file.txt", "v1\n", "s1")
	origin := filepath.Join(tmp, "origin")
	gitStateInitRepo(t, origin)
	gitStateCommitFile(t, origin, "readme.md", "parent\n", "base")
	gitStateRun_(t, origin, "-c", "protocol.file.allow=always", "submodule", "add", "-q", subSrc, "sub")
	gitStateRun_(t, origin, "commit", "-q", "-m", "p0")
	p0 = gitStateHead(t, origin)
	ws = filepath.Join(tmp, "ws")
	gitStateRun_(t, tmp, "-c", "protocol.file.allow=always", "clone", "-q", "--recurse-submodules", origin, ws)
	s2 = gitStateCommitFile(t, subSrc, "file.txt", "v2\n", "s2")
	gitStateRun_(t, origin, "-C", "sub", "fetch", "-q")
	gitStateRun_(t, origin, "-C", "sub", "checkout", "-q", s2)
	gitStateRun_(t, origin, "add", "sub")
	gitStateRun_(t, origin, "commit", "-q", "-m", "p1 bump sub to s2")
	p1 = gitStateHead(t, origin)
	gitStateRun_(t, ws, "fetch", "-q", "--no-recurse-submodules")
	sub = filepath.Join(ws, "sub")
	gitStateRun_(t, sub, "config", "protocol.file.allow", "always")
	gitStateRewriteTracked(t, filepath.Join(sub, "other.txt"), "work in progress\n")
	if delivered {
		gitStateRewriteTracked(t, filepath.Join(sub, "file.txt"), "v2\n")
	}
	return ws, sub, p0, p1, s2
}

// #201: under --apply --fetch a child that lacks the commit a candidate
// records is fetched before it is judged, so a child with edits keeps a
// clean parent behind a bump whose content it does not hold, and lets it
// move when it does.
func TestPeerGitRealign_FetchesAChildBeforeJudgingIt(t *testing.T) {
	for _, delivered := range []bool{false, true} {
		t.Run(fmt.Sprintf("delivered=%v", delivered), func(t *testing.T) {
			ws, sub, p0, p1, s2 := fetchFixture(t, delivered)
			res, err := PeerGitRealign(context.Background(), ws, nil, RealignOptions{Apply: true, Fetch: true})
			if err != nil {
				t.Fatal(err)
			}
			root := gitStateReport(t, res, ".")
			if !strings.Contains(root.TieBreak, "sub: fetched before judging") {
				t.Errorf("tie line does not say the child was fetched: %q", root.TieBreak)
			}
			if !delivered {
				if gitStateHead(t, ws) != p0 {
					t.Fatalf("root = %+v, want HEAD kept behind a bump the child does not hold", root)
				}
			} else if gitStateHead(t, ws) != p1 || gitStateHead(t, sub) != s2 {
				t.Fatalf("root %s child %s, want both moved (%+v)", shortRev(gitStateHead(t, ws)), shortRev(gitStateHead(t, sub)), root)
			}
			if log := gitStateRun_(t, ws, "diff", "--submodule=log"); strings.Contains(log, "  <") {
				t.Fatalf("the parent records a commit the child lacks:\n%s", log)
			}
		})
	}
}

// #201: the pre-judgment fetch happens only under --apply --fetch, and
// never in a child the run leaves alone (outside the restriction, or
// blocked).
func TestPeerGitRealign_FetchesOnlyWhenAllowed(t *testing.T) {
	for _, tc := range []struct {
		name     string
		opts     RealignOptions
		restrict []string
		lock     bool
	}{
		{"preview", RealignOptions{Fetch: true}, nil, false},
		{"apply without fetch", RealignOptions{Apply: true}, nil, false},
		{"child outside the restriction", RealignOptions{Apply: true, Fetch: true}, []string{"."}, false},
		{"locked child", RealignOptions{Apply: true, Fetch: true}, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws, sub, _, _, s2 := fetchFixture(t, false)
			if tc.lock {
				if err := os.WriteFile(filepath.Join(gitStateRun_(t, sub, "rev-parse", "--absolute-git-dir"), "index.lock"), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			res, err := PeerGitRealign(context.Background(), ws, tc.restrict, tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			if err := exec.Command("git", "-C", sub, "cat-file", "-e", s2+"^{commit}").Run(); err == nil {
				t.Fatalf("the child was fetched (it has %s)", shortRev(s2))
			}
			if root := gitStateReport(t, res, "."); strings.Contains(root.TieBreak, "fetch") && strings.Contains(root.TieBreak, "before judging") {
				t.Fatalf("tie line reports a fetch: %q", root.TieBreak)
			}
		})
	}
}

// A childTarget answer during which a child was fetched is not kept: the
// fetch can change what it rests on, and the repo is judged again (#208
// review). Asked again with nothing left to fetch, it is kept.
func TestChildTargetKeepsNoAnswerAFetchCrossed(t *testing.T) {
	tmp := t.TempDir()
	maruSrc := filepath.Join(tmp, "maru-src")
	gitStateInitRepo(t, maruSrc)
	gitStateCommitFile(t, maruSrc, "file.txt", "m0\n", "m0")
	devSrc := filepath.Join(tmp, "dev-src")
	gitStateInitRepo(t, devSrc)
	gitStateCommitFile(t, devSrc, "readme.md", "dev\n", "base")
	gitStateRun_(t, devSrc, "-c", "protocol.file.allow=always", "submodule", "add", "-q", maruSrc, "maru")
	gitStateRun_(t, devSrc, "commit", "-q", "-m", "d0")
	ws := filepath.Join(tmp, "ws")
	gitStateRun_(t, tmp, "-c", "protocol.file.allow=always", "clone", "-q", "--recurse-submodules", devSrc, ws)
	m1 := gitStateCommitFile(t, maruSrc, "file.txt", "m1\n", "m1")
	gitStateRun_(t, devSrc, "-C", "maru", "fetch", "-q")
	gitStateRun_(t, devSrc, "-C", "maru", "checkout", "-q", m1)
	gitStateRun_(t, devSrc, "add", "maru")
	gitStateRun_(t, devSrc, "commit", "-q", "-m", "d1 bump maru only")
	d1 := gitStateHead(t, devSrc)
	gitStateRun_(t, ws, "fetch", "-q", "--no-recurse-submodules")
	gitStateRun_(t, filepath.Join(ws, "maru"), "config", "protocol.file.allow", "always")

	// ws plays the middle repo: asked where it ends if its parent records
	// d1, it judges maru, which lacks m1 and is fetched on the way.
	r := &gitStateRun{root: ws, git: "git", opts: RealignOptions{Apply: true, Fetch: true}}
	// An answer given before the fetch (maru where it is) is forgotten by it.
	maru := filepath.Join(ws, "maru")
	r.childTarget(context.Background(), maru, "")
	if _, kept := r.targets[maru+"\x00"]; !kept {
		t.Fatal("an answer before any fetch was not kept")
	}
	key := ws + "\x00" + d1
	r.childTarget(context.Background(), ws, d1)
	if _, kept := r.targets[key]; kept || len(r.fetched) != 1 {
		t.Fatalf("kept an answer a fetch crossed (fetched %v)", r.fetched)
	}
	if _, kept := r.targets[maru+"\x00"]; kept {
		t.Fatal("an answer from before the fetch survived it")
	}
	r.childTarget(context.Background(), ws, d1)
	if _, kept := r.targets[key]; !kept {
		t.Fatal("an answer with no fetch during it was not kept")
	}
}

// #201, nested: a middle repo lacking a gitlink-only bump of its own child
// is fetched too, and each level is judged with the commits present.
func TestPeerGitRealign_FetchesANestedChildBeforeJudgingIt(t *testing.T) {
	for _, delivered := range []bool{false, true} {
		t.Run(fmt.Sprintf("delivered=%v", delivered), func(t *testing.T) {
			tmp := t.TempDir()
			maruSrc := filepath.Join(tmp, "maru-src")
			gitStateInitRepo(t, maruSrc)
			gitStateCommitFile(t, maruSrc, "file.txt", "m0\n", "m0")
			devSrc := filepath.Join(tmp, "dev-src")
			gitStateInitRepo(t, devSrc)
			gitStateCommitFile(t, devSrc, "readme.md", "dev\n", "base")
			gitStateRun_(t, devSrc, "-c", "protocol.file.allow=always", "submodule", "add", "-q", maruSrc, "maru")
			gitStateRun_(t, devSrc, "commit", "-q", "-m", "d0")
			origin := filepath.Join(tmp, "origin")
			gitStateInitRepo(t, origin)
			gitStateCommitFile(t, origin, "readme.md", "work\n", "base")
			gitStateRun_(t, origin, "-c", "protocol.file.allow=always", "submodule", "add", "-q", devSrc, "dev")
			gitStateRun_(t, origin, "commit", "-q", "-m", "r0")
			r0 := gitStateHead(t, origin)
			ws := filepath.Join(tmp, "ws")
			gitStateRun_(t, tmp, "-c", "protocol.file.allow=always", "clone", "-q", "--recurse-submodules", origin, ws)

			// After the clone: m1, the dev bump d1 and the root bump r1.
			m1 := gitStateCommitFile(t, maruSrc, "file.txt", "m1\n", "m1")
			gitStateRun_(t, devSrc, "-C", "maru", "fetch", "-q")
			gitStateRun_(t, devSrc, "-C", "maru", "checkout", "-q", m1)
			gitStateRun_(t, devSrc, "add", "maru")
			gitStateRun_(t, devSrc, "commit", "-q", "-m", "d1 bump maru only")
			d1 := gitStateHead(t, devSrc)
			gitStateRun_(t, origin, "-C", "dev", "fetch", "-q")
			gitStateRun_(t, origin, "-C", "dev", "checkout", "-q", d1)
			gitStateRun_(t, origin, "add", "dev")
			gitStateRun_(t, origin, "commit", "-q", "-m", "r1 bump dev only")
			r1 := gitStateHead(t, origin)
			gitStateRun_(t, ws, "fetch", "-q", "--no-recurse-submodules")
			dev, maru := filepath.Join(ws, "dev"), filepath.Join(ws, "dev", "maru")
			gitStateRun_(t, dev, "config", "protocol.file.allow", "always")
			gitStateRun_(t, maru, "config", "protocol.file.allow", "always")
			if delivered {
				gitStateRewriteTracked(t, filepath.Join(maru, "file.txt"), "m1\n")
			}

			if _, err := PeerGitRealign(context.Background(), ws, nil, RealignOptions{Apply: true, Fetch: true}); err != nil {
				t.Fatal(err)
			}
			if !delivered {
				if gitStateHead(t, ws) != r0 {
					t.Fatalf("root moved to %s past a nested bump nobody holds", shortRev(gitStateHead(t, ws)))
				}
			} else {
				for _, c := range []struct{ repo, want string }{{ws, r1}, {dev, d1}, {maru, m1}} {
					if got := gitStateHead(t, c.repo); got != c.want {
						t.Errorf("%s HEAD = %s, want %s", c.repo, shortRev(got), shortRev(c.want))
					}
				}
			}
			for _, repo := range []string{ws, dev} {
				if log := gitStateRun_(t, repo, "diff", "--submodule=log"); strings.Contains(log, "  <") {
					t.Errorf("%s records a commit its child lacks:\n%s", repo, log)
				}
			}
		})
	}
}

// A child fetched before its parent was judged, whose commit is still
// missing (never pushed), is not fetched again from the same origin in its
// own turn: the report says what the first fetch did (#208 review).
func TestPeerGitRealign_FetchesAChildOnce(t *testing.T) {
	ws, sub, _, _, s2 := fetchFixture(t, false)
	// The bumped commit never reached the child's origin.
	src := gitStateRun_(t, sub, "remote", "get-url", "origin")
	gitStateRun_(t, src, "reset", "-q", "--hard", "HEAD~1")
	gitStateRun_(t, src, "reflog", "expire", "--expire=now", "--all")
	gitStateRun_(t, src, "gc", "-q", "--prune=now")
	res, err := PeerGitRealign(context.Background(), ws, nil, RealignOptions{Apply: true, Fetch: true})
	if err != nil {
		t.Fatal(err)
	}
	child := gitStateReport(t, res, "sub")
	if !strings.Contains(child.Suggestion, "origin was fetched before judging but "+shortRev(s2)+" is still missing") {
		t.Fatalf("child = %+v, want the first fetch's outcome", child)
	}
}

// A fetch that failed before the parent was judged is tried again in the
// child's own turn, and the report says the fetch failed (#201).
func TestPeerGitRealign_RetriesAFailedFetchToJudge(t *testing.T) {
	ws, sub, _, _, _ := fetchFixture(t, false)
	gitStateRun_(t, sub, "config", "--unset", "protocol.file.allow")
	res, err := PeerGitRealign(context.Background(), ws, nil, RealignOptions{Apply: true, Fetch: true})
	if err != nil {
		t.Fatal(err)
	}
	child := gitStateReport(t, res, "sub")
	if !strings.Contains(child.Reason, "fetch failed") || strings.Contains(child.Suggestion, "pushed with its submodule") {
		t.Fatalf("child = %+v, want the fetch failure, not push advice", child)
	}
}

// #360: the Mac that did not push has a stale origin/main, so a repo whose
// files match the pushed commit looks at-tip until its upstream is fetched.
// FetchUpstream fetches first and moves nothing by itself; a repo with no
// remote is reported and left alone.
func TestPeerGit_FetchUpstreamFindsThePushedCommit(t *testing.T) {
	tmp := t.TempDir()
	origin := filepath.Join(tmp, "origin")
	gitStateInitRepo(t, origin)
	c1 := gitStateCommitFile(t, origin, "file.txt", "v1\n", "c1")
	ws := filepath.Join(tmp, "ws")
	gitStateRun_(t, tmp, "clone", "-q", origin, ws)
	c2 := gitStateCommitFile(t, origin, "file.txt", "v2\n", "c2") // pushed after this clone last fetched
	gitStateRewriteTracked(t, filepath.Join(ws, "file.txt"), "v2\n")
	ctx := context.Background()

	res, err := PeerGitStatus(ctx, ws, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep := gitStateReport(t, res, "."); rep.Status != GitRepoNoMatch || rep.Class != GitClassAtTip || rep.Fetch != "" {
		t.Fatalf("before the fetch: %+v", rep)
	}
	res, err = PeerGitRealign(ctx, ws, nil, RealignOptions{FetchUpstream: true})
	if err != nil {
		t.Fatal(err)
	}
	rep := gitStateReport(t, res, ".")
	if rep.Status != GitRepoRealignable || rep.Target != c2 || rep.Fetch != "fetched origin" {
		t.Fatalf("after the fetch: %+v", rep)
	}
	if head := gitStateHead(t, ws); head != c1 {
		t.Fatalf("a fetch moved HEAD to %s", head)
	}
	res, err = PeerGitRealign(ctx, ws, nil, RealignOptions{Apply: true, Fetch: true, FetchUpstream: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep := gitStateReport(t, res, "."); rep.Status != GitRepoRealigned || gitStateHead(t, ws) != c2 {
		t.Fatalf("realign --apply --fetch: %+v", rep)
	}

	// A repo the run leaves alone (here: staged changes) is not fetched.
	busy := filepath.Join(tmp, "busy")
	gitStateRun_(t, tmp, "clone", "-q", origin, busy)
	gitStateCommitFile(t, origin, "file.txt", "v3\n", "c3")
	if err := os.WriteFile(filepath.Join(busy, "new.txt"), []byte("n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitStateRun_(t, busy, "add", "new.txt")
	tracking := gitStateRun_(t, busy, "rev-parse", "origin/main")
	res, err = PeerGitRealign(ctx, busy, nil, RealignOptions{FetchUpstream: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep := gitStateReport(t, res, "."); rep.Fetch != "not fetched: staged changes" || gitStateRun_(t, busy, "rev-parse", "origin/main") != tracking {
		t.Fatalf("staged repo was fetched: %+v", rep)
	}

	lone := filepath.Join(tmp, "lone")
	gitStateInitRepo(t, lone)
	gitStateCommitFile(t, lone, "a.txt", "a\n", "a")
	res, err = PeerGitRealign(ctx, lone, nil, RealignOptions{FetchUpstream: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep := gitStateReport(t, res, "."); !strings.HasPrefix(rep.Fetch, "not fetched") {
		t.Fatalf("repo without a remote: %+v", rep)
	}
}
