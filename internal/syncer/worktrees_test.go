package syncer

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// makeFakeWorktree plants a linked-worktree shape without running git: a
// .git FILE pointing at a gitdir, with or without a commondir file. A real
// linked worktree's gitdir carries commondir; a submodule's does not.
func makeFakeGitdir(t *testing.T, dir, gitdir string, commondir bool) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(gitdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if commondir {
		if err := os.WriteFile(filepath.Join(gitdir, "commondir"), []byte("..\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: "+gitdir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestIsLinkedWorktreeDir(t *testing.T) {
	root := t.TempDir()

	worktree := filepath.Join(root, "wt")
	makeFakeGitdir(t, worktree, filepath.Join(root, "main", ".git", "worktrees", "wt"), true)
	if !isLinkedWorktreeDir(worktree) {
		t.Error("gitdir with commondir not detected as a linked worktree")
	}

	submodule := filepath.Join(root, "sub")
	makeFakeGitdir(t, submodule, filepath.Join(root, "main", ".git", "modules", "sub"), false)
	if isLinkedWorktreeDir(submodule) {
		t.Error("submodule gitdir (no commondir) misdetected as a linked worktree")
	}

	plain := filepath.Join(root, "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	if isLinkedWorktreeDir(plain) {
		t.Error("plain directory misdetected as a linked worktree")
	}

	realRepo := filepath.Join(root, "repo")
	if err := os.MkdirAll(filepath.Join(realRepo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if isLinkedWorktreeDir(realRepo) {
		t.Error("directory .git misdetected as a linked worktree")
	}
}

// Detection is by content at any depth, including a worktree of a submodule;
// submodules themselves keep syncing.
func TestDetectLinkedWorktrees(t *testing.T) {
	root := t.TempDir()
	makeFakeGitdir(t, filepath.Join(root, "dev", "repo-wt-1"), filepath.Join(root, "main", ".git", "worktrees", "repo-wt-1"), true)
	makeFakeGitdir(t, filepath.Join(root, "sub", ".worktrees", "y"), filepath.Join(root, "main", ".git", "modules", "sub", "worktrees", "y"), true)
	makeFakeGitdir(t, filepath.Join(root, "sub"), filepath.Join(root, "main", ".git", "modules", "sub"), false)
	// Content inside a detected worktree must not produce entries of its own.
	if err := os.WriteFile(filepath.Join(root, "dev", "repo-wt-1", "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := DetectLinkedWorktrees(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"dev/repo-wt-1", "sub/.worktrees/y"}
	if !slices.Equal(got, want) {
		t.Fatalf("DetectLinkedWorktrees = %v, want %v", got, want)
	}
}

// A real git worktree is detected, and a real submodule is not — the
// synthetic fixtures above only model the .git-file shape.
func TestDetectLinkedWorktrees_RealGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	env := append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	sub := filepath.Join(root, "subsrc")
	run(root, "init", "-q", "-b", "main", sub)
	if err := os.WriteFile(filepath.Join(sub, "f.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(sub, "add", ".")
	run(sub, "commit", "-qm", "c1")

	ws := filepath.Join(root, "ws")
	run(root, "init", "-q", "-b", "main", ws)
	if err := os.WriteFile(filepath.Join(ws, "keep.txt"), []byte("k"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(ws, "add", ".")
	run(ws, "commit", "-qm", "root")
	run(ws, "-c", "protocol.file.allow=always", "submodule", "add", "-q", sub, "sub")
	run(ws, "worktree", "add", "--detach", filepath.Join(ws, "dev", "ws-wt"), "HEAD")
	run(ws, "worktree", "add", "--detach", filepath.Join(ws, "sub-wt-check"), "HEAD")
	run(filepath.Join(ws, "sub"), "worktree", "add", "--detach", filepath.Join(ws, "sub", ".worktrees", "y"), "HEAD")

	got, err := DetectLinkedWorktrees(ws)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"dev/ws-wt", "sub-wt-check", "sub/.worktrees/y"}
	if !slices.Equal(got, want) {
		t.Fatalf("DetectLinkedWorktrees = %v, want %v", got, want)
	}
}

func newPeerWorktreeTestConfig(t *testing.T, workspace string) *Config {
	t.Helper()
	paths := ResolveLocalPathsForProfile(workspace, PeerProfile)
	if err := EnsureLocalLayout(paths); err != nil {
		t.Fatalf("EnsureLocalLayout: %v", err)
	}
	mirror := t.TempDir()
	return &Config{
		Profile:           PeerProfile,
		IncludeSubmodules: true,
		LocalPath:         workspace + "/",
		MirrorPath:        mirror + "/",
		Target:            Target{Kind: TargetSSH, Host: "peer", Path: "/remote/work"},
		ConfigDir:         paths.StoreDir,
		FilterMode:        FilterModeExclude,
		ExcludesFile:      paths.ExcludeFile,
		IgnoreFile:        paths.IgnoreFile,
		MaxDelete:         100,
		Propagation:       DefaultPropagationPolicy(),
		LocalPaths:        paths,
	}
}

func TestMergePeerWorktrees_StickyUnion(t *testing.T) {
	workspace := t.TempDir()
	cfg := newPeerWorktreeTestConfig(t, workspace)

	// Stored list: a worktree that no longer exists anywhere.
	if err := SavePeerWorktrees(cfg.LocalPaths.WorktreesFile, []string{"gone/wt"}); err != nil {
		t.Fatal(err)
	}
	// Local detection: a live worktree.
	makeFakeGitdir(t, filepath.Join(workspace, "dev", "local-wt"), filepath.Join(workspace, "main", ".git", "worktrees", "local-wt"), true)

	got, err := MergePeerWorktrees(cfg, []string{"remote/wt", "../evil", ""}, true)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"dev/local-wt", "gone/wt", "remote/wt"}
	if !slices.Equal(got, want) {
		t.Fatalf("union = %v, want %v (invalid remote entries dropped)", got, want)
	}

	// The persisted file carries the union.
	stored, err := LoadPeerWorktrees(cfg.LocalPaths.WorktreesFile)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(stored, want) {
		t.Fatalf("stored = %v, want %v", stored, want)
	}

	// Stickiness: the live worktree disappears, the entry stays.
	if err := os.RemoveAll(filepath.Join(workspace, "dev")); err != nil {
		t.Fatal(err)
	}
	got, err = MergePeerWorktrees(cfg, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("after the worktree vanished, union = %v, want sticky %v", got, want)
	}
}

func TestMergePeerWorktrees_PreviewDoesNotPersist(t *testing.T) {
	workspace := t.TempDir()
	cfg := newPeerWorktreeTestConfig(t, workspace)
	makeFakeGitdir(t, filepath.Join(workspace, "wt"), filepath.Join(workspace, "main", ".git", "worktrees", "wt"), true)

	got, err := MergePeerWorktrees(cfg, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"wt"}) {
		t.Fatalf("union = %v, want [wt]", got)
	}
	if _, err := os.Stat(cfg.LocalPaths.WorktreesFile); !os.IsNotExist(err) {
		t.Fatal("preview wrote the sticky list into the peer store")
	}
}

// The remote document's worktrees field is optional: a peer on a previous
// release sends nothing and the run must tolerate it (schemaVersion stays 1).
func TestParseRemotePeerStatusWorktreesOptional(t *testing.T) {
	cfg := &Config{
		Owner:     "coordinator.local",
		LocalPath: "/Users/test/work/",
		Target:    Target{Kind: TargetSSH, Host: "peer", Path: "/Users/test/work"},
	}
	withField := `{"schemaVersion":1,"kind":"peer","worktrees":["dev/a-wt"],"profile":{"configured":true,"workspacePath":"/Users/test/work","owner":"coordinator","target":{"path":"/Users/test/work"}}}`
	status, err := parseRemotePeerStatus(cfg, withField)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(status.Worktrees, []string{"dev/a-wt"}) {
		t.Fatalf("worktrees = %v", status.Worktrees)
	}
	withoutField := `{"schemaVersion":1,"kind":"peer","profile":{"configured":true,"workspacePath":"/Users/test/work","owner":"coordinator","target":{"path":"/Users/test/work"}}}`
	status, err = parseRemotePeerStatus(cfg, withoutField)
	if err != nil {
		t.Fatal(err)
	}
	if status.Worktrees != nil {
		t.Fatalf("absent worktrees field decoded as %v, want nil", status.Worktrees)
	}
}

// AC3: a linked worktree at any path on either machine, including a worktree
// of a submodule, is excluded from inventory, transfer and baseline on both
// sides; a husk (plain files where a worktree used to be) is covered by the
// sticky union. The Go-side classification and the real-rsync transfer set
// must agree, and creating/removing a worktree plans zero creates/deletes.
func TestPeerWorktrees_RsyncParity(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not installed")
	}
	workspace := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(workspace, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Payload that must travel: ordinary files and submodule content.
	write("keep.txt", "k")
	write("docs/a.md", "a")
	makeFakeGitdir(t, filepath.Join(workspace, "sub"), filepath.Join(workspace, "main", ".git", "modules", "sub"), false)
	write("sub/code.py", "print(1)")
	// A live linked worktree: detected locally.
	makeFakeGitdir(t, filepath.Join(workspace, "dev", "repo-wt-1"), filepath.Join(workspace, "main", ".git", "worktrees", "repo-wt-1"), true)
	write("dev/repo-wt-1/src/main.go", "package main")
	// A live worktree whose name carries rsync wildcard syntax: detected
	// locally, excluded through the escaped filter pattern.
	makeFakeGitdir(t, filepath.Join(workspace, "dev", "repo-wt-[x]"), filepath.Join(workspace, "main", ".git", "worktrees", "repo-wt-[x]"), true)
	write("dev/repo-wt-[x]/src/lib.go", "package lib")
	// A worktree of a submodule: detected locally.
	makeFakeGitdir(t, filepath.Join(workspace, "sub", ".worktrees", "y"), filepath.Join(workspace, "main", ".git", "modules", "sub", "worktrees", "y"), true)
	write("sub/.worktrees/y/notes.md", "n")
	// A husk: plain files where the remote's worktree stood. No .git file, so
	// only the remote's report (or the sticky list) covers it.
	write("dev/repo-wt-husk/out.bin", "husk")

	cfg := newPeerWorktreeTestConfig(t, workspace)
	detected, err := DetectLinkedWorktrees(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"dev/repo-wt-1", "dev/repo-wt-[x]", "sub/.worktrees/y"}; !slices.Equal(detected, want) {
		t.Fatalf("detected = %v, want %v", detected, want)
	}
	// The husk arrives through the remote side's report.
	effective, err := MergePeerWorktrees(cfg, []string{"dev/repo-wt-husk"}, false)
	if err != nil {
		t.Fatal(err)
	}
	cfg.WorktreeExcludes = effective
	excludedPrefixes := []string{"dev/repo-wt-1/", "dev/repo-wt-[x]/", "dev/repo-wt-husk/", "sub/.worktrees/y/"}

	// rsync side: the transfer set the peer filters produce.
	rf, err := PreparePeerPlanFilters(cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	defer rf.Cleanup()
	if rf.WorktreesDyn == "" {
		t.Fatal("peer runtime filters did not materialize the worktree layer")
	}
	dest := t.TempDir()
	args := append([]string{"-r", "--dry-run", "--out-format=%n"}, PeerFilterArgs(cfg, rf)...)
	args = append(args, workspace+"/", dest+"/")
	out, err := exec.Command("rsync", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("rsync: %v\n%s", err, out)
	}
	var rsyncKept []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" || strings.HasSuffix(line, "/") {
			continue
		}
		if line == "main/.git/modules/sub/" || strings.HasPrefix(line, "main/") {
			t.Errorf("detection scaffold leaked into the transfer set: %q", line)
			continue
		}
		rsyncKept = append(rsyncKept, line)
	}

	// Go side: the same classification through newSyncFilter.
	filter, err := newSyncFilter(cfg, strings.TrimRight(cfg.MirrorPath, "/"))
	if err != nil {
		t.Fatal(err)
	}
	var goKept []string
	err = filepath.WalkDir(workspace, func(path string, d os.DirEntry, err error) error {
		if err != nil || path == workspace {
			return err
		}
		rel, _ := filepath.Rel(workspace, path)
		rel = filepath.ToSlash(rel)
		if rel == ".dotfiles" || strings.HasPrefix(rel, ".dotfiles/") || rel == "main" || strings.HasPrefix(rel, "main/") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if filter.shouldSkip(path, rel, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			goKept = append(goKept, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, rel := range append(append([]string{}, rsyncKept...), goKept...) {
		for _, prefix := range excludedPrefixes {
			if strings.HasPrefix(rel+"/", prefix) || rel == strings.TrimSuffix(prefix, "/") {
				t.Errorf("worktree path %q was classified as transferable", rel)
			}
		}
	}
	wantKept := []string{".gitignore", "docs/a.md", "keep.txt", "sub/code.py"}
	slices.Sort(rsyncKept)
	slices.Sort(goKept)
	if !slices.Equal(rsyncKept, wantKept) {
		t.Errorf("rsync kept %v, want %v", rsyncKept, wantKept)
	}
	if !slices.Equal(goKept, wantKept) {
		t.Errorf("Go filter kept %v, want %v", goKept, wantKept)
	}

	// Inventory and plan: worktree paths produce no creates and no deletes,
	// whether the worktree exists (local detection) or is gone (sticky list).
	snapshot, err := InventoryPeer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	assertPlanQuiet := func(local PeerSnapshot, label string) {
		t.Helper()
		plan, err := PlanPeerReconcile(nil, local, PeerSnapshot{})
		if err != nil {
			t.Fatal(err)
		}
		for _, group := range [][]string{plan.Push, plan.Pull, plan.DeleteLocal, plan.DeleteRemote} {
			for _, rel := range group {
				for _, prefix := range excludedPrefixes {
					if strings.HasPrefix(rel+"/", prefix) {
						t.Errorf("%s: plan carries worktree path %q", label, rel)
					}
				}
			}
		}
	}
	assertPlanQuiet(snapshot, "worktree present")
	for rel := range snapshot {
		for _, prefix := range excludedPrefixes {
			if strings.HasPrefix(rel+"/", prefix) {
				t.Errorf("inventory carries worktree path %q", rel)
			}
		}
	}
	if _, ok := snapshot["sub/code.py"]; !ok {
		t.Error("submodule content missing from the inventory; submodules must keep syncing")
	}

	// Remove the live worktree; the sticky union keeps its path excluded.
	if err := os.RemoveAll(filepath.Join(workspace, "dev", "repo-wt-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := MergePeerWorktrees(cfg, nil, true); err != nil {
		t.Fatal(err)
	}
	snapshot, err = InventoryPeer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	assertPlanQuiet(snapshot, "worktree removed")
}

// rsync wildcard syntax in a worktree name must reach the filter file as an
// escaped literal pattern; a line separator in the name is rejected outright
// rather than injecting a second filter rule.
func TestMaterializeWorktreesDynFileEscapesMetacharacters(t *testing.T) {
	dir := t.TempDir()
	out, err := MaterializeWorktreesDynFile(dir, []string{"dev/repo-wt-[x]", "dev/wt-*"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	for _, want := range []string{"/dev/repo-wt-\\[x]\n", "/dev/wt-\\*\n"} {
		if !strings.Contains(body, want) {
			t.Errorf("filter file missing escaped pattern %q:\n%s", want, body)
		}
	}
	if _, err := MaterializeWorktreesDynFile(dir, []string{"dev/bad\nname"}); err == nil {
		t.Fatal("a line separator in a worktree name was written as a filter rule")
	}
}

// A local worktree whose path cannot be represented exactly in the
// line-oriented store stops the run loudly; silently dropping it would sync
// the worktree as regular files.
func TestMergePeerWorktreesRejectsUnrepresentableLocalPath(t *testing.T) {
	workspace := t.TempDir()
	gitdir := filepath.Join(workspace, "main", ".git", "worktrees", "bad")
	if err := os.MkdirAll(gitdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitdir, "commondir"), []byte("..\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wtDir := filepath.Join(workspace, "dev", "bad\rname")
	if err := os.MkdirAll(wtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtDir, ".git"), []byte("gitdir: "+gitdir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := newPeerWorktreeTestConfig(t, workspace)
	if _, err := MergePeerWorktrees(cfg, nil, false); err == nil || !strings.Contains(err.Error(), "cannot be represented") {
		t.Fatalf("err = %v, want the unrepresentable local path to stop the run", err)
	}
}

// A directory removed after the walk listed it - a temp tree torn down while
// the scan runs - counts as gone and does not fail the run (#268 AC1).
func TestDetectLinkedWorktrees_VanishedDirectory(t *testing.T) {
	root := t.TempDir()
	gone := filepath.Join(root, "tmp", "session-1")
	if err := os.MkdirAll(filepath.Join(gone, "deeper"), 0o755); err != nil {
		t.Fatal(err)
	}
	makeFakeGitdir(t, filepath.Join(root, "dev", "wt"), filepath.Join(root, "main", ".git", "worktrees", "wt"), true)
	// The skip hook runs after the walk listed the directory and before it is
	// read: removing it there is the race the scheduled run lost.
	removeOnVisit := func(rel string) bool {
		if rel == "tmp/session-1" {
			if err := os.RemoveAll(gone); err != nil {
				t.Fatal(err)
			}
		}
		return false
	}
	got, err := detectLinkedWorktrees(root, removeOnVisit, "")
	if err != nil {
		t.Fatalf("vanished directory failed the scan: %v", err)
	}
	if !slices.Equal(got, []string{"dev/wt"}) {
		t.Fatalf("got %v, want [dev/wt]", got)
	}
}

func unreadableDir(t *testing.T, dir string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 directory")
	}
	if err := os.MkdirAll(filepath.Join(dir, "inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
}

// An unreadable directory inside a tree the peer filters exclude is never
// read: neither the editable ignore.txt layer nor the code-owned volatile
// layer can fail the run (#268 AC2).
func TestMergePeerWorktrees_SkipsExcludedUnreadable(t *testing.T) {
	workspace := t.TempDir()
	cfg := newPeerWorktreeTestConfig(t, workspace)
	if err := os.WriteFile(cfg.IgnoreFile, []byte(".agenthub/state/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	makeFakeGitdir(t, filepath.Join(workspace, "dev", "wt"), filepath.Join(workspace, "main", ".git", "worktrees", "wt"), true)
	unreadableDir(t, filepath.Join(workspace, "dev", "hub", ".agenthub", "state", "benchmarks"))
	unreadableDir(t, filepath.Join(workspace, "scratchpad", "temp", "runtime"))

	got, err := MergePeerWorktrees(cfg, nil, false)
	if err != nil {
		t.Fatalf("excluded unreadable directory failed the scan: %v", err)
	}
	if !slices.Equal(got, []string{"dev/wt"}) {
		t.Fatalf("got %v, want [dev/wt]", got)
	}
}

// An unreadable directory the transfer would include still fails closed, and
// the error names the path and both fixes (#268 AC3).
func TestMergePeerWorktrees_IncludedUnreadableFailsWithFix(t *testing.T) {
	workspace := t.TempDir()
	cfg := newPeerWorktreeTestConfig(t, workspace)
	locked := filepath.Join(workspace, "docs", "locked")
	unreadableDir(t, locked)

	_, err := MergePeerWorktrees(cfg, nil, false)
	if err == nil {
		t.Fatal("an unreadable included directory must fail the scan")
	}
	for _, want := range []string{locked, "chmod u+rx", cfg.IgnoreFile} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

// Stored and peer-reported worktree roots are excluded from every transfer,
// so the scan does not read them either (#268).
func TestMergePeerWorktrees_SkipsKnownRootsUnreadable(t *testing.T) {
	workspace := t.TempDir()
	cfg := newPeerWorktreeTestConfig(t, workspace)
	if err := SavePeerWorktrees(cfg.LocalPaths.WorktreesFile, []string{"dev/old-wt"}); err != nil {
		t.Fatal(err)
	}
	unreadableDir(t, filepath.Join(workspace, "dev", "old-wt", "locked"))
	unreadableDir(t, filepath.Join(workspace, "dev", "remote-wt", "locked"))

	got, err := MergePeerWorktrees(cfg, []string{"dev/remote-wt"}, false)
	if err != nil {
		t.Fatalf("a known worktree root was read: %v", err)
	}
	if !slices.Equal(got, []string{"dev/old-wt", "dev/remote-wt"}) {
		t.Fatalf("got %v", got)
	}
}

// The status list the other Mac reports ignores its own ignore.txt: the
// coordinator's filters decide the transfer, so a worktree hidden only by a
// local rule must still be reported (#268). The code-owned volatile layer,
// identical on both sides, is still pruned.
func TestDetectPeerWorktrees_ReportsWorktreesUnderLocalOnlyRules(t *testing.T) {
	workspace := t.TempDir()
	cfg := newPeerWorktreeTestConfig(t, workspace)
	if err := os.WriteFile(cfg.IgnoreFile, []byte("/dev/feature-wt/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	makeFakeGitdir(t, filepath.Join(workspace, "dev", "feature-wt"), filepath.Join(workspace, "main", ".git", "worktrees", "feature-wt"), true)
	unreadableDir(t, filepath.Join(workspace, "scratchpad", "temp", "runtime"))

	got, err := DetectPeerWorktrees(cfg, workspace)
	if err != nil {
		t.Fatalf("the code-owned volatile layer was not pruned: %v", err)
	}
	if !slices.Equal(got, []string{"dev/feature-wt"}) {
		t.Fatalf("got %v, want the worktree a local-only rule hides", got)
	}
}
