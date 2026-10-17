package ui

import (
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/config"
)

func TestConfigureTerminalFreshFullDefaultsToOrca(t *testing.T) {
	for name, system := range map[string]*config.SystemInfo{
		"macos": {OS: "darwin"},
		"arch":  {OS: "linux", DistroID: "arch"},
	} {
		t.Run(name, func(t *testing.T) {
			state := &config.UserState{}
			if err := ConfigureTerminal(state, "full", system, true); err != nil {
				t.Fatalf("ConfigureTerminal: %v", err)
			}
			if got := state.Modules.TerminalApps.Apps; len(got) != 1 || got[0] != "orca" {
				t.Fatalf("terminal apps = %v, want [orca]", got)
			}
		})
	}
}

func TestConfigureTerminalPreservesExplicitMacSelection(t *testing.T) {
	state := &config.UserState{}
	state.Modules.TerminalApps.Enabled = true
	state.Modules.TerminalApps.Apps = []string{"warp", "wave"}

	if err := ConfigureTerminal(state, "full", &config.SystemInfo{OS: "darwin"}, true); err != nil {
		t.Fatalf("ConfigureTerminal: %v", err)
	}
	if got := state.Modules.TerminalApps.Apps; len(got) != 2 || got[0] != "warp" || got[1] != "wave" {
		t.Fatalf("terminal apps = %v, want [warp wave]", got)
	}
}

func TestConfigureTerminalReplacesUnsupportedImportedSelection(t *testing.T) {
	state := &config.UserState{}
	state.Modules.TerminalApps.Enabled = true
	state.Modules.TerminalApps.Apps = []string{"warp"}

	system := &config.SystemInfo{OS: "linux", DistroID: "manjaro", DistroLike: []string{"arch"}}
	if err := ConfigureTerminal(state, "full", system, true); err != nil {
		t.Fatalf("ConfigureTerminal: %v", err)
	}
	if got := state.Modules.TerminalApps.Apps; len(got) != 1 || got[0] != "orca" {
		t.Fatalf("terminal apps = %v, want [orca]", got)
	}
}
