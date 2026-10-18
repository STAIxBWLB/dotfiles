package syncer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The remote normalize walk skips what the coordinator excludes, by worktree
// root and by ignore pattern, once it has the coordinator's context; without
// it the same unreadable directory still fails (#279 AC1, AC2, AC4).
func TestPeerNormalizeExcludes_CoordinatorContextSkipsExcludedUnreadable(t *testing.T) {
	coordinator := newPeerWorktreeTestConfig(t, t.TempDir())
	if err := os.WriteFile(coordinator.IgnoreFile, []byte(".agenthub/state/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	coordinator.WorktreeExcludes = []string{"dev/wt"}
	payload, err := encodePeerNormalizeExcludes(coordinator)
	if err != nil {
		t.Fatal(err)
	}

	workspace := t.TempDir()
	unreadableDir(t, filepath.Join(workspace, "dev", "hub", ".agenthub", "state", "db"))
	unreadableDir(t, filepath.Join(workspace, "dev", "wt", "locked"))

	if _, err := PlanWorkspaceNameNormalization(newPeerWorktreeTestConfig(t, workspace)); err == nil {
		t.Fatal("without the coordinator's context the unreadable directories must still fail")
	}
	remote := newPeerWorktreeTestConfig(t, workspace)
	if err := ApplyPeerNormalizeExcludes(remote, payload); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanWorkspaceNameNormalization(remote); err != nil {
		t.Fatalf("excluded unreadable directories failed the plan: %v", err)
	}

	cmd := remotePeerNormalizeCommand("/bin/dot", payload)
	if !strings.HasPrefix(cmd, PeerNormalizeExcludesEnv+"="+shellQuote(payload)+" exec ") {
		t.Fatalf("command %q does not carry the quoted payload", cmd)
	}
	for _, want := range []string{"dev/wt", ".agenthub/state/"} {
		if !strings.Contains(payload, want) {
			t.Errorf("payload %s lacks %q", payload, want)
		}
	}
}

func TestPeerNormalizeExcludes_MalformedFailsNamingTheVariable(t *testing.T) {
	err := ApplyPeerNormalizeExcludes(&Config{}, "{not json")
	if err == nil || !strings.Contains(err.Error(), PeerNormalizeExcludesEnv) {
		t.Fatalf("error = %v, want one naming %s", err, PeerNormalizeExcludesEnv)
	}
}
