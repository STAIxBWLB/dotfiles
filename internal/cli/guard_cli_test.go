package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRootRegistersGuard(t *testing.T) {
	root := NewRootCmd("dev", "test")
	known := knownSubcommands(root)
	if !known["guard"] {
		t.Fatal("knownSubcommands missing guard")
	}
	cmd, _, err := root.Find([]string{"guard"})
	if err != nil {
		t.Fatalf("Find(guard): %v", err)
	}
	if cmd.Name() != "guard" {
		t.Fatalf("Find(guard) = %q, want guard", cmd.Name())
	}
}

func TestGuardLifecycle(t *testing.T) {
	home := t.TempDir()
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	statePath := filepath.Join(home, ".config", "dotfiles", "config.yaml")

	// enable registers hooks and sets careful.
	out, errOut, err := runDotForTest("--home", home, "guard", "enable")
	if err != nil {
		t.Fatalf("enable: %v\nstdout=%s\nstderr=%s", err, out, errOut)
	}
	if !strings.Contains(out, "registered PreToolUse hooks") {
		t.Fatalf("enable output unexpected:\n%s", out)
	}
	settings, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("settings.json not written: %v", err)
	}
	if !strings.Contains(string(settings), "# dot-guard") {
		t.Fatalf("settings.json missing marker:\n%s", settings)
	}
	state, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("state not written: %v", err)
	}
	if !strings.Contains(string(state), "careful: true") {
		t.Fatalf("state missing careful:\n%s", state)
	}

	// status reflects registration.
	out, _, err = runDotForTest("--home", home, "guard", "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out, "registered (2 entries") {
		t.Fatalf("status output unexpected:\n%s", out)
	}

	// freeze sets the boundary.
	boundary := filepath.Join(home, "project")
	if err := os.MkdirAll(boundary, 0o755); err != nil {
		t.Fatal(err)
	}
	out, _, err = runDotForTest("--home", home, "guard", "freeze", boundary)
	if err != nil {
		t.Fatalf("freeze: %v\n%s", err, out)
	}
	if !strings.Contains(out, "freeze boundary set:") {
		t.Fatalf("freeze output unexpected:\n%s", out)
	}

	// unfreeze clears it.
	out, _, err = runDotForTest("--home", home, "guard", "unfreeze")
	if err != nil {
		t.Fatalf("unfreeze: %v", err)
	}
	if !strings.Contains(out, "freeze boundary cleared") {
		t.Fatalf("unfreeze output unexpected:\n%s", out)
	}

	// disable removes marker entries and clears state.
	out, _, err = runDotForTest("--home", home, "guard", "disable")
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if !strings.Contains(out, "removed 2 dot-guard hook entries") {
		t.Fatalf("disable output unexpected:\n%s", out)
	}
	settings, _ = os.ReadFile(settingsPath)
	if strings.Contains(string(settings), "# dot-guard") {
		t.Fatalf("marker entries not removed:\n%s", settings)
	}
	state, _ = os.ReadFile(statePath)
	if strings.Contains(string(state), "guard:") {
		t.Fatalf("guard state not cleared:\n%s", state)
	}
}

func TestGuardFreezeRejectsMissingDir(t *testing.T) {
	home := t.TempDir()
	_, _, err := runDotForTest("--home", home, "guard", "freeze", filepath.Join(home, "nope"))
	if err == nil {
		t.Fatal("freeze on a missing directory should error")
	}
}

func TestGuardDryRunWritesNothing(t *testing.T) {
	home := t.TempDir()
	out, _, err := runDotForTest("--home", home, "--dry-run", "guard", "enable")
	if err != nil {
		t.Fatalf("dry-run enable: %v", err)
	}
	if !strings.Contains(out, "would ") {
		t.Fatalf("dry-run output missing marker:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Fatal("dry-run enable must not write settings.json")
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "dotfiles", "config.yaml")); !os.IsNotExist(err) {
		t.Fatal("dry-run enable must not write state")
	}
}

func TestGuardHookSubcommand(t *testing.T) {
	home := t.TempDir()
	if _, _, err := runDotForTest("--home", home, "guard", "enable"); err != nil {
		t.Fatalf("enable: %v", err)
	}

	run := func(stdin string) string {
		t.Helper()
		root := NewRootCmd("dev", "test")
		var out, errb bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&errb)
		root.SetIn(strings.NewReader(stdin))
		root.SetArgs([]string{"--home", home, "guard", "hook"})
		if err := root.Execute(); err != nil {
			t.Fatalf("hook: %v\nstderr=%s", err, errb.String())
		}
		return out.String()
	}

	// Destructive Bash command warns.
	got := run(`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"git push --force"}}`)
	if !strings.Contains(got, `"permissionDecision":"ask"`) {
		t.Fatalf("expected ask decision, got %s", got)
	}

	// Harmless Bash command allows.
	got = run(`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"}}`)
	if strings.TrimSpace(got) != "{}" {
		t.Fatalf("expected {}, got %s", got)
	}

	// Freeze denies out-of-boundary edits.
	boundary := filepath.Join(home, "project")
	if err := os.MkdirAll(boundary, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runDotForTest("--home", home, "guard", "freeze", boundary); err != nil {
		t.Fatalf("freeze: %v", err)
	}
	got = run(`{"hook_event_name":"PreToolUse","tool_name":"Edit","tool_input":{"file_path":"/etc/hosts"}}`)
	if !strings.Contains(got, `"permissionDecision":"deny"`) {
		t.Fatalf("expected deny decision, got %s", got)
	}

	// Malformed stdin fails open.
	got = run("not json")
	if strings.TrimSpace(got) != "{}" {
		t.Fatalf("malformed stdin must fail open, got %s", got)
	}
}
