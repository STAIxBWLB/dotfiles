package guard

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/claudecfg"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// foreignSettings mirrors the shape of a real user settings.json with hooks
// owned by other tools that guard must never touch.
const foreignSettings = `{
  "statusLine": {
    "type": "command",
    "command": "~/.claude/statusline-dot.py"
  },
  "hooks": {
    "SessionStart": [
      {
        "hooks": [
          { "type": "command", "command": "~/.maru/bin/init-env" }
        ]
      }
    ],
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": [
          { "type": "command", "command": "/usr/local/bin/other-tool check" }
        ]
      }
    ]
  }
}
`

func testRunner() *exec.Runner {
	return exec.NewRunner(false, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("settings.json invalid after write: %v\n%s", err, data)
	}
	return parsed
}

func TestEnsureHookEntriesCreatesFile(t *testing.T) {
	home := t.TempDir()
	hookCmd := HookCommand("/usr/local/bin/dot")

	changed, err := EnsureHookEntries(testRunner(), home, hookCmd)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("first ensure should report changed")
	}
	commands, err := InspectHookEntries(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 2 {
		t.Fatalf("expected 2 guard hook entries, got %d", len(commands))
	}
	for _, c := range commands {
		if c != hookCmd {
			t.Fatalf("hook command = %q, want %q", c, hookCmd)
		}
	}

	// Idempotent: second ensure is a no-op.
	changed, err = EnsureHookEntries(testRunner(), home, hookCmd)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("second ensure should be a no-op")
	}
}

func TestEnsureHookEntriesReplacesStaleCommand(t *testing.T) {
	home := t.TempDir()
	if _, err := EnsureHookEntries(testRunner(), home, HookCommand("/old/path/dot")); err != nil {
		t.Fatal(err)
	}
	newCmd := HookCommand("/new/path/dot")
	changed, err := EnsureHookEntries(testRunner(), home, newCmd)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("path change should report changed")
	}
	commands, _ := InspectHookEntries(home)
	if len(commands) != 2 {
		t.Fatalf("stale entries not replaced: %v", commands)
	}
	for _, c := range commands {
		if c != newCmd {
			t.Fatalf("hook command = %q, want %q", c, newCmd)
		}
	}
}

func TestEnsureAndRemovePreserveForeignHooks(t *testing.T) {
	home := t.TempDir()
	path := claudecfg.SettingsPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(foreignSettings), 0o644); err != nil {
		t.Fatal(err)
	}
	var before map[string]any
	if err := json.Unmarshal([]byte(foreignSettings), &before); err != nil {
		t.Fatal(err)
	}

	if _, err := EnsureHookEntries(testRunner(), home, HookCommand("/usr/local/bin/dot")); err != nil {
		t.Fatal(err)
	}
	after := readJSON(t, path)
	pre := after["hooks"].(map[string]any)["PreToolUse"].([]any)
	if len(pre) != 3 { // 1 foreign + 2 guard
		t.Fatalf("expected 3 PreToolUse entries, got %d", len(pre))
	}

	removed, err := RemoveHookEntries(testRunner(), home)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Fatalf("removed = %d, want 2", removed)
	}
	restored := readJSON(t, path)
	if !reflect.DeepEqual(before, restored) {
		t.Fatalf("foreign settings not restored.\nbefore: %#v\nafter:  %#v", before, restored)
	}
}

func TestRemoveHookEntriesCleansEmptyContainers(t *testing.T) {
	home := t.TempDir()
	if _, err := EnsureHookEntries(testRunner(), home, HookCommand("/usr/local/bin/dot")); err != nil {
		t.Fatal(err)
	}
	if _, err := RemoveHookEntries(testRunner(), home); err != nil {
		t.Fatal(err)
	}
	settings := readJSON(t, claudecfg.SettingsPath(home))
	if _, ok := settings["hooks"]; ok {
		t.Fatalf("empty hooks container should be removed: %#v", settings)
	}
}

func TestHookCommandRoundTrip(t *testing.T) {
	cases := []struct{ path string }{
		{"/usr/local/bin/dot"},
		{"/Users/My Name/.local/bin/dot"}, // home with a space must survive sh -c
	}
	for _, tc := range cases {
		cmd := HookCommand(tc.path)
		if got := HookBinary(cmd); got != tc.path {
			t.Fatalf("HookBinary(HookCommand(%q)) = %q", tc.path, got)
		}
	}
	// Legacy unquoted command still parses.
	if got := HookBinary("/usr/local/bin/dot guard hook # dot-guard"); got != "/usr/local/bin/dot" {
		t.Fatalf("legacy parse = %q", got)
	}
	if got := HookBinary(""); got != "" {
		t.Fatalf("empty parse = %q", got)
	}
}

func TestRemoveHookEntriesMissingFile(t *testing.T) {
	removed, err := RemoveHookEntries(testRunner(), t.TempDir())
	if err != nil || removed != 0 {
		t.Fatalf("missing file: removed=%d err=%v, want 0/nil", removed, err)
	}
}

func TestInvalidJSONRefused(t *testing.T) {
	home := t.TempDir()
	path := claudecfg.SettingsPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	broken := []byte("{ not json")
	if err := os.WriteFile(path, broken, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureHookEntries(testRunner(), home, HookCommand("/usr/local/bin/dot")); err == nil {
		t.Fatal("invalid JSON must be a hard error")
	}
	data, _ := os.ReadFile(path)
	if string(data) != string(broken) {
		t.Fatal("invalid file must be left untouched")
	}
}
