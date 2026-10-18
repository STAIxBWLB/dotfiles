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

// The coordinator's filter decides as a whole: its allow line re-including a
// path under its own deny gets that path renamed, and an allow line only the
// other Mac has cannot re-include a tree the coordinator excludes (#279 AC5).
func TestPeerNormalizeExcludes_UsesTheCoordinatorsAllowOrder(t *testing.T) {
	coordinator := newPeerWorktreeTestConfig(t, t.TempDir())
	if err := os.WriteFile(coordinator.IgnoreFile, []byte("keep/\n.agenthub/state/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nfc := "café.txt"
	coordinator.AllowPatterns = []string{"/keep/" + nfc}
	payload, err := encodePeerNormalizeExcludes(coordinator)
	if err != nil {
		t.Fatal(err)
	}

	workspace := t.TempDir()
	writeNFDTestFile(t, workspace, "keep/"+nfc, "synced")
	unreadableDir(t, filepath.Join(workspace, ".agenthub", "state", "db"))
	remote := newPeerWorktreeTestConfig(t, workspace)
	remote.AllowPatterns = []string{"/.agenthub/state/db/inner"}
	if err := ApplyPeerNormalizeExcludes(remote, payload); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanWorkspaceNameNormalization(remote)
	if err != nil {
		t.Fatalf("a remote-only allow re-included the coordinator's exclude: %v", err)
	}
	if len(plan.Renames) != 1 || plan.Renames[0].OldRel != "keep/"+nfc {
		t.Fatalf("renames = %#v, want the coordinator-allowed keep/%s", plan.Renames, nfc)
	}
}
