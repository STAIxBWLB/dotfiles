package config

import (
	"testing"
)

func TestToolingSelectionRoundTripAndClone(t *testing.T) {
	for _, selection := range []*AIToolingConfig{nil, {}, {Agents: []string{"claude", "codex"}, Tools: []string{"ripwire"}, Skills: []string{"example"}, Pins: map[string]string{"ripwire": "1.2.3"}, Updates: AIUpdateScheduleConfig{Enabled: true}}} {
		home := t.TempDir()
		state := &UserState{Name: "Test", Profile: "full"}
		state.Modules.AI.Tooling = selection
		if err := SaveStateForHome(home, state); err != nil {
			t.Fatal(err)
		}
		loaded, err := LoadStateFrom(StatePathForHome(home))
		if err != nil {
			t.Fatal(err)
		}
		if (loaded.Modules.AI.Tooling == nil) != (selection == nil) {
			t.Fatalf("selection presence lost: %#v", loaded.Modules.AI.Tooling)
		}
		cfg := &Config{}
		ApplyStateToConfig(cfg, loaded)
		if selection != nil && !cfg.Modules.AI.Enabled {
			t.Fatal("selection must enable ai")
		}
		if selection != nil && len(selection.Agents) > 0 {
			cfg.Modules.AI.Tooling.Agents[0] = "qwen"
			if loaded.Modules.AI.Tooling.Agents[0] != "claude" {
				t.Fatal("merge aliases state")
			}
		}
	}
}

func TestToolingIdentifiers(t *testing.T) {
	for _, id := range []string{"ripwire", "amem", "read-notes"} {
		if err := ValidateAITooling(&AIToolingConfig{Skills: []string{id}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"", "../escape", "a/b", "a\\b", "a\nb"} {
		if err := ValidateAITooling(&AIToolingConfig{Skills: []string{id}}); err == nil {
			t.Fatalf("accepted %q", id)
		}
	}
}
