package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// `dot peer status --json` carries the optional worktrees field the remote
// side's sticky union is built from (schemaVersion stays 1; the GUARD-04
// golden pins the field-absent shape for an empty workspace).
func TestPeerStatusJSONIncludesWorktrees(t *testing.T) {
	_, root := goldenSyncFixture(t)

	// A linked-worktree shape: .git FILE -> gitdir with commondir. Detection
	// is filesystem-only, so the sandboxed empty PATH does not matter.
	gitdir := filepath.Join(root, "main", ".git", "worktrees", "wt-x")
	if err := os.MkdirAll(gitdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitdir, "commondir"), []byte("..\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wtDir := filepath.Join(root, "dev", "wt-x")
	if err := os.MkdirAll(wtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtDir, ".git"), []byte("gitdir: "+gitdir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, errOut, err := runDotForTest("peer", "status", "--json")
	if err != nil {
		t.Fatalf("peer status --json: %v\nstderr=%s", err, errOut)
	}
	var doc struct {
		SchemaVersion int      `json:"schemaVersion"`
		Worktrees     []string `json:"worktrees"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("status document does not parse: %v\n%s", err, out)
	}
	if doc.SchemaVersion != 1 {
		t.Fatalf("schemaVersion = %d, want 1", doc.SchemaVersion)
	}
	if !slices.Equal(doc.Worktrees, []string{"dev/wt-x"}) {
		t.Fatalf("worktrees = %v, want [dev/wt-x]", doc.Worktrees)
	}
}

// The status document feeds the coordinator's sticky union: a directory the
// scan cannot read is reported, never silently left out, so the coordinator
// can refuse it unless its own filters exclude it (#268; the #135 husk class).
func TestPeerStatusJSONReportsUnreadableDirectories(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 directory")
	}
	_, root := goldenSyncFixture(t)
	locked := filepath.Join(root, "dev", "locked")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	out, errOut, err := runDotForTest("peer", "status", "--json")
	if err != nil {
		t.Fatalf("peer status --json: %v\nstderr=%s", err, errOut)
	}
	var doc struct {
		Unscanned []string `json:"unscannedWorktreeDirs"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("status document does not parse: %v\n%s", err, out)
	}
	if !slices.Equal(doc.Unscanned, []string{"dev/locked"}) {
		t.Fatalf("unscannedWorktreeDirs = %v, want [dev/locked]", doc.Unscanned)
	}
}
