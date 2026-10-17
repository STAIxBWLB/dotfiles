package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/config"
)

func TestSetupRequiresExplicitFirstUnattendedSelection(t *testing.T) {
	c := newAISetupCmd()
	c.Flags().String("home", t.TempDir(), "")
	c.Flags().Bool("yes", true, "")
	c.Flags().Bool("dry-run", true, "")
	c.SetOut(&bytes.Buffer{})
	c.SetErr(&bytes.Buffer{})
	err := c.Execute()
	if err == nil || !strings.Contains(err.Error(), "--agents") {
		t.Fatalf("got %v", err)
	}
}
func TestSetupEmptyDryRunDoesNotPersistSelection(t *testing.T) {
	home := t.TempDir()
	c := newAISetupCmd()
	c.Flags().String("home", home, "")
	c.Flags().Bool("yes", true, "")
	c.Flags().Bool("dry-run", true, "")
	c.SetOut(&bytes.Buffer{})
	c.SetErr(&bytes.Buffer{})
	c.SetArgs([]string{"--agents="})
	if err := c.Execute(); err != nil {
		t.Fatal(err)
	}
	state, err := config.LoadStateForHome(home)
	if err != nil {
		t.Fatal(err)
	}
	if state.Modules.AI.Tooling != nil {
		t.Fatal("dry-run persisted selection")
	}
}

func TestExplicitConfigSelectionOverridesSavedState(t *testing.T) {
	home := t.TempDir()
	state := &config.UserState{}
	state.Modules.AI.Tooling = &config.AIToolingConfig{Agents: []string{"claude", "codex"}}
	if err := config.SaveStateForHome(home, state); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "selection.yaml")
	if err := os.WriteFile(path, []byte("modules:\n  ai:\n    tooling:\n      agents: [qwen]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c := newAISetupCmd()
	c.Flags().String("home", home, "")
	c.Flags().String("config", path, "")
	selected, err := effectiveToolingForCmd(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected.Agents) != 1 || selected.Agents[0] != "qwen" {
		t.Fatalf("wrong selection: %#v", selected)
	}
	if got := newAgentsManagerFromCmd(c).DefaultApplyTools(); len(got) != 1 || got[0] != "qwen" {
		t.Fatal(got)
	}
}

func TestExplicitConfigWithoutAIDoesNotEnableAI(t *testing.T) {
	for _, saved := range []*config.AIToolingConfig{nil, {Agents: []string{"claude", "codex"}}} {
		cfg := &config.Config{}
		state := &config.UserState{}
		state.Modules.AI.Enabled = true
		state.Modules.AI.Tooling = saved
		applyStateWithToolingAuthority(cfg, state, true)
		if cfg.Modules.AI.Enabled {
			t.Fatal("explicit config without AI enabled it")
		}
		if cfg.Modules.AI.Tooling == nil || len(cfg.Modules.AI.Tooling.Agents) != 0 {
			t.Fatal("saved selection reentered explicit config")
		}
	}
}
