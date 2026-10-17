package syncer

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dotexec "github.com/STAIxBWLB/dotfiles/internal/exec"
)

func TestRemoteTargetConflictRootUsesTargetPathAndRejectsBroadRoots(t *testing.T) {
	target := Target{Kind: TargetSSH, Host: "peer", Path: "~/workspace/work/"}
	got, err := RemoteTargetConflictRoot(target)
	if err != nil {
		t.Fatalf("RemoteTargetConflictRoot: %v", err)
	}
	if got != "~/workspace/work/.sync-conflicts" {
		t.Fatalf("root = %q, want target path/.sync-conflicts", got)
	}

	for _, bad := range []Target{
		{Kind: TargetSSH, Host: "peer", Path: ""},
		{Kind: TargetSSH, Host: "peer", Path: "/"},
		{Kind: TargetSSH, Host: "peer", Path: "~/../"},
		{Kind: TargetLocal, Path: "/tmp/work"},
	} {
		if _, err := RemoteTargetConflictRoot(bad); err == nil {
			t.Errorf("RemoteTargetConflictRoot(%+v) accepted unsafe target", bad)
		}
	}
}

func TestListAndPruneRemoteConflictsUsesValidatedInventory(t *testing.T) {
	installFakeSSH(t)
	tree := t.TempDir()
	root := filepath.Join(tree, conflictsDirName)
	old := filepath.Join(root, "2026-01-01T00-00-00Z")
	keep := filepath.Join(root, "2026-08-01T00-00-00Z")
	for _, dir := range []string{old, keep} {
		if err := os.MkdirAll(filepath.Join(dir, "from-workspace"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(old, "from-workspace", "old.bin"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(keep, "from-workspace", "keep.bin"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-40 * 24 * time.Hour).Truncate(time.Second)
	keepTime := time.Now().Add(-2 * 24 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(old, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(keep, keepTime, keepTime); err != nil {
		t.Fatal(err)
	}

	target := Target{Kind: TargetSSH, Host: "fake-peer", Path: tree}
	remoteRoot, err := RemoteTargetConflictRoot(target)
	if err != nil {
		t.Fatal(err)
	}
	runner := dotexec.NewRunner(true, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	entries, err := ListRemoteConflicts(context.Background(), runner, target, remoteRoot)
	if err != nil {
		t.Fatalf("ListRemoteConflicts: %v", err)
	}
	if len(entries) != 2 || entries[0].Timestamp != filepath.Base(old) || entries[1].Timestamp != filepath.Base(keep) {
		t.Fatalf("entries = %+v, want oldest-first old/keep", entries)
	}
	if entries[0].Size <= 0 {
		t.Errorf("remote size = %d, want positive inventory size", entries[0].Size)
	}

	cutoff := time.Now().Add(-30 * 24 * time.Hour)
	plan, err := PruneRemoteConflicts(context.Background(), runner, target, remoteRoot, cutoff, true)
	if err != nil {
		t.Fatalf("PruneRemoteConflicts dry-run: %v", err)
	}
	if len(plan.Pruned) != 1 || plan.Pruned[0].Timestamp != filepath.Base(old) {
		t.Fatalf("dry-run plan = %+v, want only old", plan.Pruned)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatalf("dry-run removed old conflict: %v", err)
	}
	late := filepath.Join(root, "2025-12-01T00-00-00Z")
	if err := os.MkdirAll(late, 0o755); err != nil {
		t.Fatal(err)
	}
	lateTime := time.Now().Add(-50 * 24 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(late, lateTime, lateTime); err != nil {
		t.Fatal(err)
	}

	runner = dotexec.NewRunner(false, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	res, err := ApplyRemoteConflictPrune(context.Background(), runner, target, remoteRoot, plan.Pruned)
	if err != nil {
		t.Fatalf("PruneRemoteConflicts: %v", err)
	}
	if len(res.Pruned) != 1 || res.Reclaimed <= 0 {
		t.Fatalf("prune result = %+v, want one reclaimed entry", res)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("old conflict still exists after prune, stat err=%v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("new conflict should survive prune: %v", err)
	}
	if _, err := os.Stat(late); err != nil {
		t.Errorf("unconfirmed late conflict should survive exact prune: %v", err)
	}
}

func TestRemoteConflictValidationRefusesSymlinksAndNonDirectories(t *testing.T) {
	installFakeSSH(t)
	targetRoot := t.TempDir()
	target := Target{Kind: TargetSSH, Host: "fake-peer", Path: targetRoot}
	root, err := RemoteTargetConflictRoot(target)
	if err != nil {
		t.Fatal(err)
	}

	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(targetRoot, conflictsDirName)); err != nil {
		t.Fatal(err)
	}
	runner := dotexec.NewRunner(false, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if _, err := ListRemoteConflicts(context.Background(), runner, target, root); err == nil {
		t.Fatal("symlinked conflict root was accepted")
	}
	if err := os.Remove(filepath.Join(targetRoot, conflictsDirName)); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "stray"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ListRemoteConflicts(context.Background(), runner, target, root); err == nil {
		t.Fatal("non-directory conflict child was accepted")
	}
	if err := os.Remove(filepath.Join(root, "stray")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "safe"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "safe-link")); err != nil {
		t.Fatal(err)
	}
	if _, err := ListRemoteConflicts(context.Background(), runner, target, root); err == nil {
		t.Fatal("symlinked timestamp was accepted")
	}
	if err := os.Remove(filepath.Join(root, "safe-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "not-a-timestamp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ListRemoteConflicts(context.Background(), runner, target, root); err == nil {
		t.Fatal("non-timestamp conflict directory was accepted")
	}
}

func TestRemoteConflictPruneRefusesEmptySelectionAndUnsafeNames(t *testing.T) {
	root := filepath.Join(t.TempDir(), conflictsDirName)
	for _, stamps := range [][]string{nil, {}, {"../outside"}, {"-rf"}, {""}} {
		if _, err := remoteConflictPruneCommand(root, stamps); err == nil {
			t.Errorf("remoteConflictPruneCommand(%v) accepted unsafe selection", stamps)
		}
	}
	if _, err := remoteConflictListCommand(filepath.Join(t.TempDir(), "other")); err == nil {
		t.Fatal("list command accepted an unowned root")
	}
}

func installFakeSSH(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	ssh := filepath.Join(bin, "ssh")
	// The production command is a single quoted `sh -c ...` string after the
	// host. Execute that exact command locally so the test exercises the same
	// shell validation and quoting without requiring a network peer.
	script := "#!/bin/sh\nlast=\nfor arg do last=$arg; done\nexec /bin/sh -c \"$last\"\n"
	if err := os.WriteFile(ssh, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return ssh
}

func TestParseRemoteConflictListRejectsMalformedRecords(t *testing.T) {
	root := "/tmp/work/.sync-conflicts"
	for _, body := range []string{
		"bad\n",
		"DOTFILES_CONFLICT_V1\t../escape\t1\t1\n",
		"DOTFILES_CONFLICT_V1\tgood\t-1\t1\n",
		"DOTFILES_CONFLICT_V1\tgood\t1\t-1\n",
	} {
		if _, err := parseRemoteConflictList(body, root); err == nil {
			t.Errorf("parseRemoteConflictList accepted malformed body %q", body)
		}
	}
	if _, err := parseRemoteConflictList("DOTFILES_CONFLICT_V1\tgood\t1\t2\nDOTFILES_CONFLICT_V1\tgood\t1\t2\n", root); err == nil {
		t.Fatal("duplicate remote conflict timestamp was accepted")
	}
	if !strings.HasSuffix(root, "/.sync-conflicts") {
		t.Fatal("test root malformed")
	}
}
