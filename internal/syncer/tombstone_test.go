package syncer

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	goexec "github.com/STAIxBWLB/dotfiles/internal/exec"
)

// tombstoneFixture builds a peer-shaped config whose local tree is a real
// temp dir, seeds the baseline with the given relpaths, and creates the
// subset that still exists on disk.
func tombstoneFixture(t *testing.T, baselineRels, onDiskRels []string) *Config {
	t.Helper()
	local := t.TempDir()
	cfg := newTestConfig(t)
	cfg.LocalPath = local + "/"
	cfg.Profile = PeerProfile
	cfg.Target = Target{Kind: TargetSSH, Host: "user@peer", Path: "/remote/work"}
	cfg.Propagation = PropagationPolicy{Create: true, Update: true, Delete: true}
	cfg.FilterMode = FilterModeExclude

	for _, rel := range onDiskRels {
		abs := filepath.Join(local, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(abs, []byte("payload"), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	baseline := map[string]Fingerprint{}
	for _, rel := range baselineRels {
		baseline[rel] = Fingerprint{Size: 7, Mtime: time.Now().UTC()}
	}
	if err := SaveBaselineManifest(cfg.LocalPaths.BaselineFile, baseline); err != nil {
		t.Fatalf("SaveBaselineManifest: %v", err)
	}
	if err := markPeerBaselineTarget(cfg); err != nil {
		t.Fatalf("markPeerBaselineTarget: %v", err)
	}
	return cfg
}

func TestComputeTombstones_InBaselineAndAbsentLocally(t *testing.T) {
	cfg := tombstoneFixture(t,
		[]string{"notes/keep.md", "inbox/drop/gone.csv"},
		[]string{"notes/keep.md"},
	)

	got, err := ComputeTombstones(cfg)
	if err != nil {
		t.Fatalf("ComputeTombstones: %v", err)
	}
	want := []string{"inbox/drop/gone.csv"}
	if !slices.Equal(got, want) {
		t.Errorf("tombstones = %v, want %v", got, want)
	}
}

// A path the peer created is absent locally but was never in the baseline.
// Deleting it would destroy the peer's new work.
func TestComputeTombstones_NotInBaselineIsNotADeletion(t *testing.T) {
	cfg := tombstoneFixture(t, []string{"notes/keep.md"}, []string{"notes/keep.md"})

	got, err := ComputeTombstones(cfg)
	if err != nil {
		t.Fatalf("ComputeTombstones: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("tombstones = %v, want none — peer-only paths are not deletions", got)
	}
}

func TestComputeTombstones_RequiresBaselineForCurrentPeer(t *testing.T) {
	cfg := tombstoneFixture(t, []string{"gone.md"}, nil)
	if err := os.Remove(peerBaselineTargetFile(cfg)); err != nil {
		t.Fatal(err)
	}
	got, err := ComputeTombstones(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("pre-feature baseline produced destructive tombstones: %v", got)
	}

	if err := markPeerBaselineTarget(cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Target.Path = "/different/work"
	got, err = ComputeTombstones(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("baseline for a different peer target produced tombstones: %v", got)
	}
}

func TestComputeTombstones_RecreatedLocallyIsNotADeletion(t *testing.T) {
	cfg := tombstoneFixture(t,
		[]string{"notes/back.md"},
		[]string{"notes/back.md"},
	)

	got, err := ComputeTombstones(cfg)
	if err != nil {
		t.Fatalf("ComputeTombstones: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("tombstones = %v, want none — the path exists locally again", got)
	}
}

func TestComputeTombstones_SymlinkReplacementIsADeletion(t *testing.T) {
	cfg := tombstoneFixture(t, []string{"notes/replaced.md"}, nil)
	local := strings.TrimRight(cfg.LocalPath, "/")
	if err := os.MkdirAll(filepath.Join(local, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target.md")
	if err := os.WriteFile(target, []byte("local target"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(local, "notes", "replaced.md")); err != nil {
		t.Fatal(err)
	}

	got, err := ComputeTombstones(cfg)
	if err != nil {
		t.Fatalf("ComputeTombstones: %v", err)
	}
	want := []string{"notes/replaced.md"}
	if !slices.Equal(got, want) {
		t.Errorf("tombstones = %v, want %v; --no-links does not sync the replacement", got, want)
	}
}

func TestComputeTombstones_IncludesPeerSubmodulePayloads(t *testing.T) {
	cfg := tombstoneFixture(t, []string{"dev/tool/gone.md"}, nil)
	local := strings.TrimRight(cfg.LocalPath, "/")
	if err := os.MkdirAll(filepath.Join(local, "dev", "tool"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitmodules := "[submodule \"tool\"]\n\tpath = dev/tool\n\turl = git@example.com:tool.git\n"
	if err := os.WriteFile(filepath.Join(local, ".gitmodules"), []byte(gitmodules), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg.IncludeSubmodules = true

	got, err := ComputeTombstones(cfg)
	if err != nil {
		t.Fatalf("ComputeTombstones: %v", err)
	}
	want := []string{"dev/tool/gone.md"}
	if !slices.Equal(got, want) {
		t.Errorf("tombstones = %v, want %v; peer profiles carry submodule working trees", got, want)
	}
}

func TestComputeTombstones_RejectsNonDirectoryRoot(t *testing.T) {
	cfg := tombstoneFixture(t, []string{"gone.md"}, nil)
	rootFile := filepath.Join(t.TempDir(), "workspace")
	if err := os.WriteFile(rootFile, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg.LocalPath = rootFile

	if _, err := ComputeTombstones(cfg); err == nil {
		t.Fatal("non-directory workspace root must fail closed")
	}
}

func TestComputeTombstones_RejectsUnsafeBaselinePath(t *testing.T) {
	cfg := tombstoneFixture(t, []string{"/outside.md"}, nil)

	if _, err := ComputeTombstones(cfg); err == nil {
		t.Fatal("absolute baseline path must fail before reaching rsync")
	}
}

func TestComputeTombstones_RejectsUnreadableSubtree(t *testing.T) {
	cfg := tombstoneFixture(t, []string{"locked/gone.md"}, nil)
	locked := filepath.Join(strings.TrimRight(cfg.LocalPath, "/"), "locked")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	if _, err := os.ReadDir(locked); err == nil {
		t.Skip("test process can read mode-000 directories")
	}

	if _, err := ComputeTombstones(cfg); err == nil {
		t.Fatal("incomplete local inventory must fail closed")
	}
}

func TestComputeTombstones_SkipsFilteredPaths(t *testing.T) {
	cfg := tombstoneFixture(t,
		[]string{".maru/secrets/token.env", "notes/keep.md"},
		[]string{"notes/keep.md"},
	)

	got, err := ComputeTombstones(cfg)
	if err != nil {
		t.Fatalf("ComputeTombstones: %v", err)
	}
	for _, rel := range got {
		if strings.HasPrefix(rel, ".maru/secrets/") {
			t.Errorf("tombstones leaked a filtered secret path: %v", got)
		}
	}
}

func TestComputeTombstones_SkipsPathsUnderDirectoryOnlyFilter(t *testing.T) {
	cfg := tombstoneFixture(t, []string{"archive/gone.md", "notes/gone.md"}, nil)
	cfg.IgnoreFile = cfg.LocalPaths.IgnoreFile
	if err := os.WriteFile(cfg.IgnoreFile, []byte("/archive/\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ComputeTombstones(cfg)
	if err != nil {
		t.Fatalf("ComputeTombstones: %v", err)
	}
	want := []string{"notes/gone.md"}
	if !slices.Equal(got, want) {
		t.Errorf("tombstones = %v, want %v; excluded directory descendants are not deletions", got, want)
	}
}

// Delete propagation is peer-only. For a mirror target the baseline records
// the mirror tree, so baseline-minus-local means something else entirely.
func TestComputeTombstones_RefusesNonSSHTarget(t *testing.T) {
	cfg := tombstoneFixture(t, []string{"gone.md"}, nil)
	cfg.Target = Target{Kind: TargetLocal, Path: "/tmp/mirror"}

	got, err := ComputeTombstones(cfg)
	if err != nil {
		t.Fatalf("ComputeTombstones: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("tombstones = %v, want none for a non-SSH target", got)
	}
}

func TestComputeTombstones_RefusesWhenDeleteOff(t *testing.T) {
	cfg := tombstoneFixture(t, []string{"gone.md"}, nil)
	cfg.Propagation = PropagationPolicy{Create: true, Update: true, Delete: false}

	got, err := ComputeTombstones(cfg)
	if err != nil {
		t.Fatalf("ComputeTombstones: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("tombstones = %v, want none when propagation.delete is off", got)
	}
}

func TestComputeTombstones_RequiresFullPeerPropagation(t *testing.T) {
	cfg := tombstoneFixture(t, []string{"gone.md"}, nil)
	cfg.Propagation = PropagationPolicy{Create: false, Update: true, Delete: true}

	if _, err := ComputeTombstones(cfg); err == nil {
		t.Fatal("delete propagation cannot trust a baseline that intentionally omitted creates")
	}
}

func TestDeletePassArgs_TargetsOnlyTheListedPaths(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Target = Target{Kind: TargetSSH, Host: "user@peer", Path: "/remote/work"}
	conflict := &ConflictDir{Timestamp: "ts"}

	args := deletePassArgs(cfg, conflict, "/tmp/list", "/tmp/tombstone-source", false)

	for _, want := range []string{
		"--files-from=/tmp/list",
		"--from0",
		"--ignore-missing-args",
		"--delete-missing-args",
		"--backup",
		"--backup-dir=" + conflict.PushBackupRel(),
	} {
		if !slices.Contains(args, want) {
			t.Errorf("deletePassArgs missing %q; got %v", want, args)
		}
	}
	// --delete would remove every peer path absent locally, not just the
	// listed ones. That is the failure this pass exists to avoid.
	for _, forbidden := range []string{"--delete", "--delete-after", "--delete-during", "--delete-excluded"} {
		if slices.Contains(args, forbidden) {
			t.Errorf("deletePassArgs leaked %q — only listed paths may be removed; got %v", forbidden, args)
		}
	}
}

func TestDeletePassArgs_DryRunPropagates(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Target = Target{Kind: TargetSSH, Host: "user@peer", Path: "/remote/work"}

	args := deletePassArgs(cfg, &ConflictDir{Timestamp: "ts"}, "/tmp/list", "/tmp/tombstone-source", true)

	if !slices.Contains(args, "--dry-run") {
		t.Errorf("dry-run deletePassArgs missing --dry-run; got %v", args)
	}
}

func TestWriteTombstoneList_IsNulDelimited(t *testing.T) {
	dir := t.TempDir()
	rels := []string{"inbox/drop/왕솬 chat.csv", "notes/a b.md"}

	path, err := writeTombstoneList(dir, rels)
	if err != nil {
		t.Fatalf("writeTombstoneList: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read list: %v", err)
	}
	if strings.Contains(string(raw), "\n") {
		t.Error("tombstone list contains a newline — names with spaces need NUL delimiting")
	}
	got := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
	if !slices.Equal(got, rels) {
		t.Errorf("list = %q, want %q", got, rels)
	}
}

func TestMaterializeTombstoneExcludes_AnchorsEachPath(t *testing.T) {
	dir := t.TempDir()

	path, err := MaterializeTombstoneExcludesFile(dir, []string{"inbox/drop/gone.csv"})
	if err != nil {
		t.Fatalf("MaterializeTombstoneExcludesFile: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read excludes: %v", err)
	}
	// Anchored at the transfer root, otherwise the pattern would also match a
	// same-named file elsewhere in the tree.
	if !strings.Contains(string(raw), "/inbox/drop/gone.csv\n") {
		t.Errorf("excludes file missing anchored path; got %q", raw)
	}
}

func TestMaterializeTombstoneExcludes_EscapesWildcardSyntax(t *testing.T) {
	dir := t.TempDir()

	path, err := MaterializeTombstoneExcludesFile(dir, []string{`dir/a\b[0]*?.txt`})
	if err != nil {
		t.Fatalf("MaterializeTombstoneExcludesFile: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := `/dir/a\\b\[0]\*\?.txt` + "\n"
	if !strings.Contains(string(raw), want) {
		t.Errorf("literal filter = %q, want line %q", raw, want)
	}
}

func TestMaterializeTombstoneExcludes_RejectsLineSeparators(t *testing.T) {
	for _, rel := range []string{"dir/a\nb.md", "dir/a\rb.md"} {
		if _, err := MaterializeTombstoneExcludesFile(t.TempDir(), []string{rel}); err == nil {
			t.Errorf("path %q must be rejected by the line-oriented filter format", rel)
		}
	}
}

func TestMaterializeTombstoneExcludes_MatchesOnlyTheLiteralPath(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not installed")
	}
	root := t.TempDir()
	source := filepath.Join(root, "source")
	dest := filepath.Join(root, "dest")
	if err := os.MkdirAll(filepath.Join(source, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	literal := filepath.Join(source, "dir", "[ab]*?.txt")
	sibling := filepath.Join(source, "dir", "abX.txt")
	for _, path := range []string{literal, sibling} {
		if err := os.WriteFile(path, []byte("payload"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	excludes, err := MaterializeTombstoneExcludesFile(root, []string{"dir/[ab]*?.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("rsync", "-a", "--exclude-from="+excludes, source+"/", dest+"/").CombinedOutput(); err != nil {
		t.Fatalf("rsync: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dest, "dir", "[ab]*?.txt")); !os.IsNotExist(err) {
		t.Errorf("literal tombstone was not excluded: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "dir", "abX.txt")); err != nil {
		t.Errorf("wildcard-like tombstone excluded a sibling: %v", err)
	}
}

func TestPullArgs_ExcludesTombstonedPaths(t *testing.T) {
	cfg := newTestConfig(t)
	conflict := &ConflictDir{Timestamp: "ts"}
	rf := runtimeFilters{TombstonesDyn: "/tmp/tombstones.excl"}

	args := pullArgs(cfg, conflict, rf, false)

	if !slices.Contains(args, "--exclude-from=/tmp/tombstones.excl") {
		t.Errorf("pullArgs missing the tombstone exclude layer; got %v", args)
	}
	// Without this the pull restores the file before the delete pass can run,
	// which is the whole bug.
	idx := slices.Index(args, "--exclude-from=/tmp/tombstones.excl")
	trackedIdx := slices.Index(args, "--include-from="+rf.TrackedDyn)
	if trackedIdx != -1 && idx > trackedIdx {
		t.Error("tombstone excludes must precede the include layer — rsync is first-match-wins")
	}
}

func TestPullArgs_StillNeverDeletes(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Propagation = PropagationPolicy{Create: true, Update: true, Delete: true}

	args := pullArgs(cfg, &ConflictDir{Timestamp: "ts"}, runtimeFilters{}, false)

	for _, forbidden := range []string{"--delete", "--delete-after", "--delete-during", "--delete-missing-args"} {
		if slices.Contains(args, forbidden) {
			t.Errorf("pullArgs leaked %q — the pull must never delete locally; got %v", forbidden, args)
		}
	}
}

// End-to-end against real rsync, with a local directory standing in for the
// peer. Proves the property the whole feature rests on: the listed path is
// removed and quarantined, and a path the peer created is left alone.
func TestPropagateDeletes_RemovesListedPathAndSparesPeerOnlyFile(t *testing.T) {
	requireDeleteMissingArgsRsync(t)
	root := t.TempDir()
	local := filepath.Join(root, "local")
	peer := filepath.Join(root, "peer")
	for _, d := range []string{local, filepath.Join(peer, "inbox", "drop")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Deleted locally, still on the peer.
	if err := os.WriteFile(filepath.Join(peer, "inbox", "drop", "gone.csv"), []byte("removed"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Created on the peer, never seen here. Must survive.
	if err := os.WriteFile(filepath.Join(peer, "inbox", "drop", "peer-new.md"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := newTestConfig(t)
	cfg.LocalPath = local + "/"
	cfg.MirrorPath = peer + "/"
	cfg.Target = Target{Kind: TargetLocal, Path: peer + "/"}
	cfg.LogFile = filepath.Join(root, "sync.log")
	cfg.RsyncPath = "rsync"
	if err := SaveBaselineManifest(cfg.LocalPaths.BaselineFile, map[string]Fingerprint{
		"inbox/drop/gone.csv": {Size: 7, Mtime: time.Now().UTC()},
	}); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	runner := goexec.NewRunner(false, logger)
	conflict := &ConflictDir{Timestamp: "TS"}

	if err := propagateDeletes(context.Background(), runner, cfg, conflict,
		[]string{"inbox/drop/gone.csv"}, false); err != nil {
		t.Fatalf("PropagateDeletes: %v", err)
	}

	if _, err := os.Stat(filepath.Join(peer, "inbox", "drop", "gone.csv")); !os.IsNotExist(err) {
		t.Error("tombstoned path still on the peer")
	}
	if _, err := os.Stat(filepath.Join(peer, "inbox", "drop", "peer-new.md")); err != nil {
		t.Errorf("peer-only file was destroyed: %v", err)
	}
	quarantined := filepath.Join(peer, conflictsDirName, "TS", "from-workspace", "inbox", "drop", "gone.csv")
	body, err := os.ReadFile(quarantined)
	if err != nil {
		t.Fatalf("expected quarantine copy at %s: %v", quarantined, err)
	}
	if string(body) != "removed" {
		t.Errorf("quarantined content = %q, want %q", body, "removed")
	}
}

func requireDeleteMissingArgsRsync(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not installed")
	}
	help, err := exec.Command("rsync", "--help").CombinedOutput()
	if err != nil {
		t.Skipf("cannot inspect rsync capabilities: %v", err)
	}
	for _, option := range []string{"--ignore-missing-args", "--delete-missing-args"} {
		if !strings.Contains(string(help), option) {
			t.Skipf("rsync does not support %s (peer deletion requires rsync 3.x)", option)
		}
	}
}

func TestPropagateDeletes_RefusesOverCap(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.MaxDelete = 1
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	err := propagateDeletes(context.Background(), goexec.NewRunner(false, logger), cfg,
		&ConflictDir{Timestamp: "TS"}, []string{"a", "b"}, false)

	if err == nil {
		t.Fatal("over-cap delete pass must refuse before touching the peer")
	}
}

func TestPropagateDeletes_RejectsNonPeerTarget(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Propagation = PropagationPolicy{Create: true, Update: true, Delete: true}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	err := PropagateDeletes(context.Background(), goexec.NewRunner(false, logger), cfg,
		&ConflictDir{Timestamp: "TS"}, []string{"gone.md"}, false)
	if err == nil {
		t.Fatal("exported delete propagation must reject non-peer targets")
	}
}

func TestRemoteQuarantineCommand_RejectsSymlinkAndQuotesPaths(t *testing.T) {
	root := filepath.Join(t.TempDir(), "peer's work")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	command, err := remoteQuarantineCommand(root, "TS", true)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("sh", "-c", command).CombinedOutput(); err != nil {
		t.Fatalf("safe quarantine preflight: %v\n%s", err, out)
	}
	leaf := filepath.Join(root, conflictsDirName, "TS", "from-workspace")
	if info, err := os.Lstat(leaf); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("safe quarantine leaf not created as a real directory: info=%v err=%v", info, err)
	}

	outside := t.TempDir()
	if err := os.RemoveAll(filepath.Join(root, conflictsDirName)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, conflictsDirName)); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("sh", "-c", command).CombinedOutput(); err == nil {
		t.Fatalf("symlinked quarantine passed preflight: %s", out)
	}
}

func TestRemoteQuarantineCommandRejectsBroadWorkspaceRoots(t *testing.T) {
	for _, root := range []string{"", "/", "~", "~/../work", "/tmp/../work", "bad\nroot"} {
		if _, err := remoteQuarantineCommand(root, "TS", true); err == nil {
			t.Errorf("accepted unsafe remote workspace root %q", root)
		}
	}
}

func TestPropagateDeletes_DoesNotSwallowPartialTransfer(t *testing.T) {
	fakeBin := t.TempDir()
	fakeRsync := filepath.Join(fakeBin, "rsync")
	if err := os.WriteFile(fakeRsync, []byte("#!/bin/sh\nexit 23\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg := newTestConfig(t)
	cfg.LocalPath = t.TempDir() + "/"
	cfg.Target = Target{Kind: TargetLocal, Path: t.TempDir() + "/"}
	if err := SaveBaselineManifest(cfg.LocalPaths.BaselineFile, map[string]Fingerprint{
		"gone.md": {Size: 1, Mtime: time.Now().UTC()},
	}); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	err := propagateDeletes(context.Background(), goexec.NewRunner(false, logger), cfg,
		&ConflictDir{Timestamp: "TS"}, []string{"gone.md"}, false)
	if err == nil {
		t.Fatal("exit 23 can mean a failed target delete and must abort before baseline refresh")
	}
	if !IsPartialTransfer(err) {
		t.Fatalf("error = %T %v, want classified partial transfer", err, err)
	}
}

func TestPropagateDeletes_RejectsRsyncCannotDeleteWarning(t *testing.T) {
	fakeBin := t.TempDir()
	fakeRsync := filepath.Join(fakeBin, "rsync")
	if err := os.WriteFile(fakeRsync, []byte("#!/bin/sh\necho 'cannot delete non-empty directory: gone' >&2\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg := newTestConfig(t)
	cfg.LocalPath = t.TempDir() + "/"
	cfg.Target = Target{Kind: TargetLocal, Path: t.TempDir() + "/"}
	if err := SaveBaselineManifest(cfg.LocalPaths.BaselineFile, map[string]Fingerprint{
		"gone": {Size: 1, Mtime: time.Now().UTC()},
	}); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	err := propagateDeletes(context.Background(), goexec.NewRunner(false, logger), cfg,
		&ConflictDir{Timestamp: "TS"}, []string{"gone"}, false)
	if err == nil {
		t.Fatal("rsync's exit-0 cannot-delete warning must not retire the tombstone")
	}
}

func TestPush_SSHPartialTransferKeepsBaselineUnchanged(t *testing.T) {
	fakeBin := t.TempDir()
	fakeRsync := filepath.Join(fakeBin, "rsync")
	if err := os.WriteFile(fakeRsync, []byte("#!/bin/sh\nexit 23\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg := tombstoneFixture(t, []string{"old.md"}, []string{"new.md"})
	cfg.LogFile = filepath.Join(t.TempDir(), "sync.log")
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	err := Push(context.Background(), goexec.NewRunner(false, logger), cfg, false)
	if !IsPartialTransfer(err) {
		t.Fatalf("Push error = %T %v, want classified partial transfer", err, err)
	}
	baseline, loadErr := LoadBaselineManifest(cfg.LocalPaths.BaselineFile)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if _, ok := baseline["old.md"]; !ok {
		t.Errorf("partial SSH push retired the prior baseline: %v", baseline)
	}
	if _, ok := baseline["new.md"]; ok {
		t.Errorf("partial SSH push recorded an unconfirmed remote path: %v", baseline)
	}
}

func TestPush_PeerFullPolicyMarksBaselineForCurrentTarget(t *testing.T) {
	fakeBin := t.TempDir()
	fakeRsync := filepath.Join(fakeBin, "rsync")
	if err := os.WriteFile(fakeRsync, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg := tombstoneFixture(t, []string{"keep.md"}, []string{"keep.md"})
	cfg.LogFile = filepath.Join(t.TempDir(), "sync.log")
	if err := os.Remove(peerBaselineTargetFile(cfg)); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	if err := Push(context.Background(), goexec.NewRunner(false, logger), cfg, false); err != nil {
		t.Fatalf("Push: %v", err)
	}
	ready, err := peerBaselineMatchesTarget(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !ready {
		t.Fatal("successful full peer push did not establish target-bound baseline provenance")
	}
}

func TestPush_PeerRestrictedPolicyDoesNotContaminateBaseline(t *testing.T) {
	fakeBin := t.TempDir()
	fakeRsync := filepath.Join(fakeBin, "rsync")
	if err := os.WriteFile(fakeRsync, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg := tombstoneFixture(t, []string{"confirmed.md"}, []string{"confirmed.md", "not-sent.md"})
	cfg.LogFile = filepath.Join(t.TempDir(), "sync.log")
	cfg.Propagation = PropagationPolicy{Create: false, Update: true, Delete: false}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	if err := Push(context.Background(), goexec.NewRunner(false, logger), cfg, false); err != nil {
		t.Fatalf("Push: %v", err)
	}
	baseline, err := LoadBaselineManifest(cfg.LocalPaths.BaselineFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := baseline["confirmed.md"]; !ok {
		t.Fatalf("restricted push retired the prior confirmed baseline: %v", baseline)
	}
	if _, ok := baseline["not-sent.md"]; ok {
		t.Fatalf("restricted push recorded a path that create=false could not send: %v", baseline)
	}
}

// Blanket --delete removes every target path absent locally. On a peer that
// is most of the other machine's tree, so the dedicated pass owns deletion
// there and the push must stay additive even with propagation.delete on.
func TestPushArgs_PeerSSHTargetNeverGetsBlanketDelete(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Profile = PeerProfile
	cfg.Target = Target{Kind: TargetSSH, Host: "user@peer", Path: "/remote/work"}
	cfg.Propagation = PropagationPolicy{Create: true, Update: true, Delete: true}

	args := pushArgs(cfg, &ConflictDir{Timestamp: "ts"}, runtimeFilters{}, false)

	for _, forbidden := range []string{"--delete", "--delete-after", "--delete-during", "--delete-excluded"} {
		if slices.Contains(args, forbidden) {
			t.Errorf("ssh pushArgs leaked %q — it would delete the peer's own files; got %v", forbidden, args)
		}
	}
}

func TestPushArgs_GenericSSHTargetKeepsConfiguredDelete(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Profile = DefaultProfile
	cfg.Target = Target{Kind: TargetSSH, Host: "user@backup", Path: "/remote/work"}
	cfg.Propagation = PropagationPolicy{Create: true, Update: true, Delete: true}

	args := pushArgs(cfg, &ConflictDir{Timestamp: "ts"}, runtimeFilters{}, false)

	if !slices.Contains(args, "--delete-after") {
		t.Errorf("generic SSH sync lost configured delete propagation; got %v", args)
	}
}

// The mirror has a single writer and no separate delete pass, so it keeps the
// blanket behavior.
func TestPushArgs_LocalTargetKeepsBlanketDelete(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Target = Target{Kind: TargetLocal, Path: "/tmp/mirror/"}
	cfg.Propagation = PropagationPolicy{Create: true, Update: true, Delete: true}

	args := pushArgs(cfg, &ConflictDir{Timestamp: "ts"}, runtimeFilters{}, false)

	if !slices.Contains(args, "--delete-after") {
		t.Errorf("mirror pushArgs lost --delete-after; got %v", args)
	}
}

func TestCheckTombstoneCap(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.MaxDelete = 2

	if err := checkTombstoneCap(cfg, []string{"a", "b"}); err != nil {
		t.Errorf("at the cap should pass, got %v", err)
	}
	err := checkTombstoneCap(cfg, []string{"a", "b", "c"})
	if err == nil {
		t.Fatal("over the cap must refuse — a broken filter can present the whole tree as deleted")
	}
	if !strings.Contains(err.Error(), "max_delete") {
		t.Errorf("error should name the knob to raise; got %v", err)
	}
}
