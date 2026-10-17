package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/config"
)

func TestMain(m *testing.M) { _ = os.Unsetenv("CODEX_HOME"); os.Exit(m.Run()) }
func TestResolveUpdateToolsKeepsPhaseOrder(t *testing.T) {
	cmd := newAIUpdateCmd()
	if err := cmd.Flags().Set("tool", "gsd,claude"); err != nil {
		t.Fatal(err)
	}
	got, err := resolveUpdateTools(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "claude,gsd" {
		t.Fatalf("got %v", got)
	}
}
func TestResolveUpdateToolsRejectsExcluded(t *testing.T) {
	for _, id := range []string{"skills", "gemini", "cursor", "copilot", "kiro", "firecrawl", "agent-hub", ","} {
		t.Run(id, func(t *testing.T) {
			cmd := newAIUpdateCmd()
			_ = cmd.Flags().Set("tool", id)
			if _, err := resolveUpdateTools(cmd); err == nil {
				t.Fatalf("accepted %s", id)
			}
		})
	}
}
func TestAIUpdateRequiresSavedSelection(t *testing.T) {
	_, _, err := runDotForTest("--home", t.TempDir(), "--dry-run", "ai", "update", "--json")
	if err == nil || !strings.Contains(err.Error(), "dot ai setup") {
		t.Fatalf("got %v", err)
	}
}
func TestAIUpdateRejectsUnselectedTarget(t *testing.T) {
	home := t.TempDir()
	state := &config.UserState{}
	state.Modules.AI.Tooling = &config.AIToolingConfig{Agents: []string{"codex"}}
	if err := config.SaveStateForHome(home, state); err != nil {
		t.Fatal(err)
	}
	_, _, err := runDotForTest("--home", home, "--dry-run", "ai", "update", "--tool", "claude", "--json")
	if err == nil || !strings.Contains(err.Error(), "not selected") {
		t.Fatalf("got %v", err)
	}
}
func TestAIUpdateExplicitEmptySelectionDoesNothing(t *testing.T) {
	home := t.TempDir()
	state := &config.UserState{}
	state.Modules.AI.Tooling = &config.AIToolingConfig{}
	if err := config.SaveStateForHome(home, state); err != nil {
		t.Fatal(err)
	}
	out, stderr, err := runDotForTest("--home", home, "--dry-run", "ai", "update", "--json")
	if err != nil {
		t.Fatalf("%v %s", err, stderr)
	}
	if !strings.Contains(out, `"items": []`) || strings.Contains(out, "updated") {
		t.Fatalf("unexpected %s", out)
	}
}
func TestAIUpdateAndAuthRegistered(t *testing.T) {
	out, stderr, err := runDotForTest("ai", "--help")
	if err != nil {
		t.Fatalf("%v %s", err, stderr)
	}
	for _, want := range []string{"update", "auth"} {
		if !strings.Contains(out, want) {
			t.Fatal(want)
		}
	}
}
