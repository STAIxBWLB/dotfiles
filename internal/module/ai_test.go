package module

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	dotexec "github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/template"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

// Legacy configs may still carry modules.ai.skills.enabled: true. The AI
// module must ignore it: skill symlinks are owned by the Maru app, so neither
// Check nor Apply may report or touch skill targets.
func TestAIModuleIgnoresLegacySkillsConfig(t *testing.T) {
	home := t.TempDir()
	// A configured source skill with no target symlink — the old code would
	// have reported a "apply skills SSOT" change and created the symlink.
	ssot := filepath.Join(home, ".maru", "skills")
	if err := os.MkdirAll(filepath.Join(ssot, "vault-extract"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ssot, "vault-extract", "SKILL.md"), []byte("# Vault Extract\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rc := &RunContext{
		Config: &config.Config{Modules: config.ModulesConfig{AI: config.AIConfig{
			Enabled: true,
			Skills: config.AISkillsConfig{
				Enabled:  true,
				Provider: "maru",
				Tools:    []string{"claude"},
			},
		}}},
		Runner:   dotexec.NewRunner(false, slog.Default()),
		Template: template.NewEngine(),
		HomeDir:  home,
	}

	res, err := (&AIModule{}).Check(context.Background(), rc)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	for _, c := range res.Changes {
		if strings.Contains(c.Description, "skills") || strings.Contains(c.Command, "skills") {
			t.Errorf("Check reported a skills change despite diagnose-only policy: %+v", c)
		}
	}

	if _, err := (&AIModule{}).Apply(context.Background(), rc); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(home, ".claude", "skills", "vault-extract")); !os.IsNotExist(err) {
		t.Fatalf("Apply created a skill symlink despite diagnose-only policy, stat err=%v", err)
	}
}

// Fresh machines enable modules.ai.agents_ssot by default, so the first
// `dot apply` must scaffold the SSOT instead of failing on a missing file.
func TestAIModuleScaffoldsAgentsSSOTOnFreshApply(t *testing.T) {
	home := t.TempDir()
	rc := &RunContext{
		Config: &config.Config{Modules: config.ModulesConfig{AI: config.AIConfig{
			Enabled:    true,
			AgentsSSOT: true,
		}}},
		Runner:   dotexec.NewRunner(false, slog.Default()),
		Template: template.NewEngine(),
		HomeDir:  home,
	}

	if _, err := (&AIModule{}).Apply(context.Background(), rc); err != nil {
		t.Fatalf("Apply on fresh home: %v", err)
	}
	ssot := filepath.Join(home, ".config", "dotfiles", "agents", "AGENTS.md")
	if _, err := os.Stat(ssot); err != nil {
		t.Fatalf("Apply did not scaffold agents SSOT: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "CLAUDE.md")); err != nil {
		t.Fatalf("Apply did not render scaffolded SSOT to claude target: %v", err)
	}
}

// Dry-run writes nothing, so a fresh home must complete without error and
// without creating the SSOT or any tool target.
func TestAIModuleFreshDryRunSkipsAgentsSSOT(t *testing.T) {
	home := t.TempDir()
	rc := &RunContext{
		Config: &config.Config{Modules: config.ModulesConfig{AI: config.AIConfig{
			Enabled:    true,
			AgentsSSOT: true,
		}}},
		Runner:   dotexec.NewRunner(true, slog.Default()),
		Template: template.NewEngine(),
		HomeDir:  home,
		DryRun:   true,
	}

	if _, err := (&AIModule{}).Apply(context.Background(), rc); err != nil {
		t.Fatalf("dry-run Apply on fresh home: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "dotfiles", "agents", "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("dry-run created agents SSOT, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "CLAUDE.md")); !os.IsNotExist(err) {
		t.Fatalf("dry-run created claude target, stat err=%v", err)
	}
}

// A malformed amem memory block is a finding, not a change: it must reach the
// person on a warn row, never on the present row that Apply messages get.
func TestAIModuleMemoryBlockWarningUsesWarnMarker(t *testing.T) {
	home := t.TempDir()
	ssotDir := filepath.Join(home, ".config", "dotfiles", "agents")
	if err := os.MkdirAll(ssotDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// An opening marker with no end makes the memory block malformed, and its
	// presence makes the memory block wanted on a machine without amem.
	if err := os.WriteFile(filepath.Join(ssotDir, "AGENTS.md"), []byte("# rules\n<!-- dotfiles:amem:start -->\nno end\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	rc := &RunContext{
		Config: &config.Config{Modules: config.ModulesConfig{AI: config.AIConfig{
			Enabled:    true,
			AgentsSSOT: true,
		}}},
		Runner:   dotexec.NewRunner(false, slog.Default()),
		Template: template.NewEngine(),
		HomeDir:  home,
		Out:      &out,
	}

	res, err := (&AIModule{}).Apply(context.Background(), rc)
	if err != nil {
		t.Fatalf("a malformed memory block failed the apply: %v", err)
	}
	for _, msg := range res.Messages {
		if strings.Contains(msg, "memory block was not written") {
			t.Fatalf("the warning rode in Messages, which render as a present row: %q", msg)
		}
	}
	// The buffer is not a terminal, so the color policy strips the role color and
	// leaves the plain marker.
	want := "  " + ui.MarkerColumn(ui.MarkWarn) + " ai: "
	if !strings.Contains(out.String(), want) || !strings.Contains(out.String(), "the memory block was not written") {
		t.Fatalf("no warn-marker row for the warning, got %q", out.String())
	}
	if strings.Contains(out.String(), ui.MarkerColumn(ui.MarkPresent)+" ai:") {
		t.Fatalf("the warning rendered with the present marker: %q", out.String())
	}
}
