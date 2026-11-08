package cli

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReadOnlyCommandDoesNotRewriteLegacyState is D-05's proof, and it is not
// the same claim as "the command succeeded". The legacy-key conversion happens
// in memory at the load choke point; if it ever grew a write, `dot check`,
// `dot diff` and internal/profilesnap would start rewriting files they are
// only reading -- profilesnap reads OTHER homes' state files merely to inspect
// them. So the assertion is byte identity of the file itself.
func TestReadOnlyCommandDoesNotRewriteLegacyState(t *testing.T) {
	home := t.TempDir()
	statePath := filepath.Join(home, ".config", "dotfiles", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := "name: Test\nprofile: full\nmodules:\n  warp: true\n  ai_tools: true\n"
	if err := os.WriteFile(statePath, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	digest := func() string {
		data, err := os.ReadFile(statePath)
		if err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("%x", sha256.Sum256(data))
	}
	before := digest()

	out, errOut, err := runDotForTest("--home", home, "diff", "--summary")
	if err != nil {
		t.Fatalf("diff --summary: %v\nstdout=%s\nstderr=%s", err, out, errOut)
	}
	// Non-vacuity: the profile shown comes from the state file, so the command
	// demonstrably reached the load path this test is about.
	if !strings.Contains(out, "full") {
		t.Fatalf("diff --summary did not read the seeded state file:\n%s", out)
	}

	if after := digest(); after != before {
		t.Fatalf("read-only command rewrote the state file:\nbefore=%s\nafter=%s\n%s",
			before, after, mustReadFile(t, statePath))
	}
	if got := mustReadFile(t, statePath); got != legacy {
		t.Fatalf("state file contents changed:\n%s", got)
	}
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
