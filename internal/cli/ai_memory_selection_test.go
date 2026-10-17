package cli

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/spf13/cobra"
)

func memorySelectionCommand(home, path string) *cobra.Command {
	cmd := &cobra.Command{Use: "memory-test"}
	cmd.Flags().String("home", home, "")
	cmd.Flags().String("config", path, "")
	return cmd
}
func saveMemorySelection(t *testing.T, home string, agents []string) {
	t.Helper()
	state := &config.UserState{}
	if agents != nil {
		state.Modules.AI.Tooling = &config.AIToolingConfig{Agents: agents}
	}
	if err := config.SaveStateForHome(home, state); err != nil {
		t.Fatal(err)
	}
}
func TestMemoryExplicitConfigOverridesSavedSelection(t *testing.T) {
	home := t.TempDir()
	saveMemorySelection(t, home, []string{"kimi"})
	path := filepath.Join(t.TempDir(), "selection.yaml")
	if err := os.WriteFile(path, []byte("modules:\n  ai:\n    tooling:\n      agents: [qwen]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	mgr, err := newClaudeMemManagerFromCmd(memorySelectionCommand(home, path))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(mgr.SelectedAgents, []string{"qwen"}) {
		t.Fatalf("initial selection: %v", mgr.SelectedAgents)
	}
	saveMemorySelection(t, home, []string{"claude", "codex"})
	selected, err := mgr.AgentSelection()
	if err != nil || !slices.Equal(selected, []string{"qwen"}) {
		t.Fatalf("refresh lost config authority: %v %v", selected, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.AgentSelection(); err == nil {
		t.Fatal("missing authoritative config fell back to state")
	}
}
func TestMemoryExplicitConfigNeverFallsBackToLegacy(t *testing.T) {
	for _, body := range []string{"modules: {}\n", "modules:\n  ai:\n    tooling:\n      agents: []\n"} {
		for _, saved := range [][]string{nil, {"kimi"}} {
			home := t.TempDir()
			saveMemorySelection(t, home, saved)
			path := filepath.Join(t.TempDir(), "selection.yaml")
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			mgr, err := newClaudeMemManagerFromCmd(memorySelectionCommand(home, path))
			if err != nil {
				t.Fatal(err)
			}
			if mgr.SelectedAgents == nil || len(mgr.SelectedAgents) != 0 {
				t.Fatalf("initial broadened selection: %#v", mgr.SelectedAgents)
			}
			selected, err := mgr.AgentSelection()
			if err != nil || selected == nil || len(selected) != 0 {
				t.Fatalf("refresh broadened selection: %#v %v", selected, err)
			}
		}
	}
}
func TestMemorySelectionWithoutConfigReloadsSavedState(t *testing.T) {
	home := t.TempDir()
	saveMemorySelection(t, home, nil)
	mgr, err := newClaudeMemManagerFromCmd(memorySelectionCommand(home, ""))
	if err != nil {
		t.Fatal(err)
	}
	if mgr.SelectedAgents != nil {
		t.Fatal("legacy absent selection changed")
	}
	saveMemorySelection(t, home, []string{"kimi"})
	selected, err := mgr.AgentSelection()
	if err != nil || !slices.Equal(selected, []string{"kimi"}) {
		t.Fatalf("saved selection not refreshed: %v %v", selected, err)
	}
	saveMemorySelection(t, home, []string{})
	selected, err = mgr.AgentSelection()
	if err != nil || selected == nil || len(selected) != 0 {
		t.Fatalf("explicit-empty state lost: %#v %v", selected, err)
	}
}
func TestMemoryMalformedExplicitConfigFails(t *testing.T) {
	home := t.TempDir()
	saveMemorySelection(t, home, []string{"kimi"})
	path := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(path, []byte("modules: ["), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := newClaudeMemManagerFromCmd(memorySelectionCommand(home, path)); err == nil {
		t.Fatal("malformed authoritative config ignored")
	}
}
