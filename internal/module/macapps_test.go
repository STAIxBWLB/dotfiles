package module

import (
	"io"
	"log/slog"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

func TestMacAppsResolveCasks_AppendsExtrasToDefaults(t *testing.T) {
	m := &MacAppsModule{}
	rc := &RunContext{
		Config: &config.Config{
			CasksExtra: []string{"iterm2"},
		},
	}

	got := m.resolveCasks(rc)
	if !contains(got, "arc") {
		t.Fatalf("default casks were not preserved: %v", got)
	}
	if !contains(got, "iterm2") {
		t.Fatalf("extra cask missing: %v", got)
	}
}

func TestMacAppsResolveCasks_ConfiguredCasksWin(t *testing.T) {
	m := &MacAppsModule{}
	rc := &RunContext{
		Config: &config.Config{
			Casks:      []string{"raycast"},
			CasksExtra: []string{"raycast", "iterm2"},
		},
	}

	got := m.resolveCasks(rc)
	want := []string{"raycast", "iterm2"}
	if len(got) != len(want) {
		t.Fatalf("resolveCasks length = %d, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("resolveCasks[%d] = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
}

// State files written before the Anchor -> Maru rename may still list the
// anchor cask; it must resolve to maru-workspace (deduped) instead of
// failing brew with an unknown cask.
func TestMacAppsResolveCasks_RewritesLegacyAnchor(t *testing.T) {
	m := &MacAppsModule{}
	rc := &RunContext{
		Config: &config.Config{
			Casks: []string{"anchor", "raycast", "maru-workspace"},
		},
	}

	got := m.resolveCasks(rc)
	want := []string{"maru-workspace", "raycast"}
	if len(got) != len(want) {
		t.Fatalf("resolveCasks = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("resolveCasks[%d] = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
}

func TestMacAppsResolveCasks_IncludesTerminalApps(t *testing.T) {
	m := &MacAppsModule{}
	rc := &RunContext{
		Config: &config.Config{
			Modules: config.ModulesConfig{
				Terminal: config.TermConfig{
					Enabled: true,
					Apps:    []string{"wave", "cmux"},
				},
			},
		},
	}

	got := m.resolveCasks(rc)
	if !contains(got, "arc") {
		t.Fatalf("default casks were not preserved: %v", got)
	}
	if !contains(got, "wave") || !contains(got, "cmux") {
		t.Fatalf("terminal app casks missing: %v", got)
	}
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// TestMacAppsRequiredTaps_IndependentOfInstalledState pins the tap-trust fix:
// trust must cover every tap a cask declares, not only the taps that are
// missing. An already-tapped repo can still be untrusted, and Homebrew then
// refuses to load the cask, which aborts the whole apply.
func TestMacAppsRequiredTaps_IndependentOfInstalledState(t *testing.T) {
	m := &MacAppsModule{}
	runner := exec.NewRunner(true, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rc := &RunContext{Runner: runner, Brew: exec.NewBrew(runner)}

	got := m.requiredTaps(rc, []string{"cmux", "orca", "maru-workspace", "raycast"})

	for _, want := range []string{"manaflow-ai/cmux", "stablyai/orca", "staixbwlb/cask"} {
		if !contains(got, want) {
			t.Errorf("requiredTaps = %v, missing %q", got, want)
		}
	}
	// raycast declares no tap; nothing extra should be trusted on its behalf.
	if len(got) != 3 {
		t.Errorf("requiredTaps = %v, want exactly the 3 declared taps", got)
	}
}

// A nil Brew means there is nothing to trust against, not a panic.
func TestMacAppsRequiredTaps_NilBrew(t *testing.T) {
	m := &MacAppsModule{}
	if got := m.requiredTaps(&RunContext{}, []string{"cmux"}); got != nil {
		t.Errorf("requiredTaps with nil Brew = %v, want nil", got)
	}
}
