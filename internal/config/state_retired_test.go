package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #373 AC1: each retired value loads, is rewritten on the next save and
// validates afterwards.
func TestLoadState_RetiredValuesRewrittenOnSave(t *testing.T) {
	for _, tc := range []struct {
		name, yaml, gone, want string
	}{
		{"anchor provider", "  ai:\n    skills:\n      provider: anchor\n", "provider: anchor", "provider: maru"},
		{"skills tool agents", "  ai:\n    skills:\n      provider: maru\n      tools: [claude, agents]\n", "- agents", "- claude"},
		{"skills tool gemini", "  ai:\n    skills:\n      provider: maru\n      tools: [gemini, codex]\n", "- gemini", "- codex"},
		{"skills tool antigravity", "  ai:\n    skills:\n      provider: maru\n      tools: [antigravity, claude]\n", "- antigravity", "- claude"},
		{"anchor cask", "  macapps:\n    casks: [raycast, anchor]\n", "- anchor\n", "- maru-workspace"},
		{"anchor cask dedup", "  macapps:\n    casks: [anchor, maru-workspace]\n", "- anchor\n", "- maru-workspace"},
		{"legacy ai_tools key", "  ai_tools: true\n", "ai_tools", "enabled: true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte("name: Test\nprofile: full\nmodules:\n"+tc.yaml), 0o644); err != nil {
				t.Fatal(err)
			}
			loaded, err := loadStateAt(path)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if err := saveStateAt(path, loaded); err != nil {
				t.Fatalf("save: %v", err)
			}
			data, _ := os.ReadFile(path)
			if strings.Contains(string(data), tc.gone) || !strings.Contains(string(data), tc.want) {
				t.Fatalf("saved file still retired or missing %q:\n%s", tc.want, data)
			}
			again, err := loadStateAt(path)
			if err != nil {
				t.Fatal(err)
			}
			if len(again.carriedLegacy) != 0 {
				t.Errorf("rewritten file still carries legacy values: %v", again.carriedLegacy)
			}
			if err := again.Validate(); err != nil {
				t.Errorf("rewritten state does not validate: %v", err)
			}
		})
	}
}

// #373 AC2: a stamped file is canonical; a stray legacy key is not overlaid.
func TestLoadState_StampedFileIgnoresStrayLegacyKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("schema_version: 4\nname: Test\nprofile: full\nmodules:\n  ai_tools: true\n  warp: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadStateAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Modules.AI.Enabled || loaded.Modules.Warp || len(loaded.carriedLegacy) != 0 {
		t.Fatalf("stray legacy keys were overlaid: ai=%v warp=%v carried=%v", loaded.Modules.AI.Enabled, loaded.Modules.Warp, loaded.carriedLegacy)
	}
}
