package module

import (
	"path/filepath"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/aitooling"
	"github.com/STAIxBWLB/dotfiles/internal/config"
)

func TestAISelectedManagedFilesExcludeUnselectedClaude(t *testing.T) {
	for _, agents := range [][]string{{}, {"codex"}, {"grok", "qwen"}} {
		cfg := &config.Config{}
		cfg.Modules.AI.Tooling = &config.AIToolingConfig{Agents: agents}
		rc := &RunContext{Config: cfg, HomeDir: t.TempDir()}
		for _, file := range (&AIModule{}).managedFiles(rc) {
			if file.destPath == filepath.Join(rc.HomeDir, ".config", "claude", "settings.json") {
				t.Fatalf("unselected Claude settings write for %v", agents)
			}
		}
	}
}

func TestToolingStatusConvergence(t *testing.T) {
	for _, status := range []string{"installed", "up-to-date", "pinned", "update-available"} {
		if !toolingStatusSatisfied(aitooling.ItemResult{Status: status, Installed: "1.2.3"}) {
			t.Fatal(status)
		}
	}
	for _, status := range []string{"missing", "unknown", "deferred-resource-pressure"} {
		if toolingStatusSatisfied(aitooling.ItemResult{Status: status}) {
			t.Fatal(status)
		}
	}
}
