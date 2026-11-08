package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/config"
)

// #370: dot apply no longer sweeps files retired before v2.65. A home that
// still holds them keeps them byte-for-byte, and apply does not plan to
// touch them.
func TestApplyLeavesRetiredFilesUntouched(t *testing.T) {
	home := t.TempDir()
	if err := config.SaveStateForHome(home, &config.UserState{Name: "N", Email: "n@example.invalid", Profile: "minimal"}); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		".config/shell/30-ai-tools.sh":     "# old ai tools\n",
		".npmrc":                           "store-dir=/x\nvirtual-store-dir=/y\n",
		".config/github-copilot/AGENTS.md": "<!-- Managed by dotfiles -->\n# agents\n",
		"Library/LaunchAgents/com.rclone.workspace-bisync.plist":   "<plist/>\n",
		".config/systemd/user/rclone-bisync.timer":                 "[Timer]\n",
		".config/systemd/user/rclone-bisync.service":               "[Service]\n",
		"Library/LaunchAgents/com.entelecheia.spotlight-nmi.plist": "<plist/>\n",
		".local/bin/nmi-stamp":                                     "#!/bin/sh\n",
		".config/shrc/40-spotlight-nmi":                            "# nmi\n",
	}
	for rel, body := range files {
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	out, errOut, err := runDotForTest("--home", home, "apply", "--yes", "--dry-run", "--module", "ai,node")
	if err != nil {
		t.Fatalf("apply: %v\nstdout:\n%s\nstderr:\n%s", err, out, errOut)
	}
	for _, needle := range []string{"30-ai-tools", ".npmrc", "github-copilot", "rclone", "dot clone", "spotlight-nmi", "nmi-stamp"} {
		if strings.Contains(out, needle) || strings.Contains(errOut, needle) {
			t.Errorf("apply still plans to touch %q\nstdout:\n%s\nstderr:\n%s", needle, out, errOut)
		}
	}
	for rel, body := range files {
		got, err := os.ReadFile(filepath.Join(home, rel))
		if err != nil || string(got) != body {
			t.Errorf("%s changed: %q, %v", rel, got, err)
		}
	}
}
