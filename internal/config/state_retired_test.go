package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/appsettings"
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

// #394 row 10: modules.gdrive_sync.mirror_path lost its last reader, so a file
// that still sets it has nothing left to resolve. It does not lose the value
// yet: while it names a folder other than the detected default, a workspace
// store without `target` would silently redirect, and internal/syncer refuses
// exactly that on the strength of this value. Any unrelated save (`dot apply`,
// `dot reconfigure`) must carry it through, silently.
func TestLoadState_CarriesTheRetiredMirrorPathWhileItStillMoves(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "schema_version: 4\nname: Test\nprofile: full\nmodules:\n  gdrive_sync:\n    mirror_path: /legacy/mirror\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadStateAt(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded.retiredKeys) != 1 || loaded.retiredKeys[0] != retiredMirrorPathStateKey {
		t.Fatalf("retiredKeys = %v, want [%s]", loaded.retiredKeys, retiredMirrorPathStateKey)
	}
	if got := loaded.RetiredMirrorPath(); got != "/legacy/mirror" {
		t.Errorf("RetiredMirrorPath = %q, want /legacy/mirror", got)
	}

	out := captureStderr(t, func() {
		if err := saveStateAt(path, loaded); err != nil {
			t.Fatalf("save: %v", err)
		}
	})
	if strings.Contains(out, "retired") {
		t.Errorf("a carried key was announced as dropped: %q", out)
	}

	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "mirror_path: /legacy/mirror") {
		t.Fatalf("the save dropped a value that still moves a destination:\n%s", data)
	}

	// Carrying is idempotent: the flag is spent, the value stays.
	if again := captureStderr(t, func() {
		if err := saveStateAt(path, loaded); err != nil {
			t.Fatalf("second save: %v", err)
		}
	}); strings.Contains(again, "retired") {
		t.Errorf("second save repeated the note: %q", again)
	}
	reloaded, err := loadStateAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.RetiredMirrorPath(); got != "/legacy/mirror" {
		t.Errorf("reloaded value = %q, want /legacy/mirror", got)
	}
}

// The other half of the same rule: once the retired value names the folder
// resolution would use, it decides nothing, so the save drops it and says so
// once. The stamped version is the point: #373's gate governs keys whose values
// are migrated, and this one has no canonical form, so the note has to fire for
// a canonical file too.
func TestLoadState_DropsTheRetiredMirrorPathKeyOnceItIsMoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	moot := filepath.Join(home, "gdrive-workspace", "work")
	if want := appsettings.DefaultMirrorPath(home); moot != want {
		t.Fatalf("test default drifted: %q vs %q", moot, want)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "schema_version: 4\nname: Test\nprofile: full\nmodules:\n  gdrive_sync:\n    mirror_path: " + moot + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadStateAt(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	out := captureStderr(t, func() {
		if err := saveStateAt(path, loaded); err != nil {
			t.Fatalf("save: %v", err)
		}
	})
	if got := strings.Count(out, "note: dropped the retired `"+retiredMirrorPathStateKey+"` key"); got != 1 {
		t.Fatalf("want exactly one retired-key note, got %d in %q", got, out)
	}
	if !strings.Contains(out, "`dot sync target`") {
		t.Errorf("the note does not name the command that sets the replacement: %q", out)
	}

	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "mirror_path") {
		t.Fatalf("saved file still carries the retired key:\n%s", data)
	}

	// One note per run: the save that announced the key clears the flag.
	if again := captureStderr(t, func() {
		if err := saveStateAt(path, loaded); err != nil {
			t.Fatalf("second save: %v", err)
		}
	}); strings.Contains(again, "retired") {
		t.Errorf("second save repeated the note: %q", again)
	}

	reloaded, err := loadStateAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.retiredKeys) != 0 {
		t.Errorf("rewritten file still reports retired keys: %v", reloaded.retiredKeys)
	}
	if got := reloaded.RetiredMirrorPath(); got != "" {
		t.Errorf("rewritten file still reports a retired value: %q", got)
	}
}
