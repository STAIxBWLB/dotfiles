package aisettings

import (
	"encoding/json"
	"github.com/STAIxBWLB/dotfiles/internal/config"
	"os"
	"path/filepath"
	"testing"
)

func TestSelectedAgentsEmptyDoesNotFanOut(t *testing.T) {
	home := t.TempDir()
	m := NewAgentsManager(nil, home)
	m.SelectedTools = []string{}
	result, err := m.Apply(ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 0 {
		t.Fatal("empty selection fanout")
	}
	if _, err := os.Stat(m.StatePath()); !os.IsNotExist(err) {
		t.Fatal("empty selection wrote state")
	}
	if _, err := m.Apply(ApplyOptions{Tools: []string{"claude"}}); err == nil {
		t.Fatal("unselected explicit agent accepted")
	}
}
func TestInstructionOverridesStayInsideExplicitHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", "/other/profile")
	m := NewAgentsManager(nil, home)
	m.ExplicitHome = true
	actual, err := m.TargetPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	if actual != filepath.Join(home, ".codex", "AGENTS.md") {
		t.Fatal(actual)
	}
}
func TestQwenContextPreservesSettings(t *testing.T) {
	home := t.TempDir()
	m := NewAgentsManager(nil, home)
	path := filepath.Join(home, ".qwen", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"context":{"fileName":["CUSTOM.md"]},"model":{"name":"keep"}}`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.ensureQwenContext(true); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if string(before) != string(original) {
		t.Fatal("dry run wrote settings")
	}
	if err := m.ensureQwenContext(false); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	if settings["model"].(map[string]any)["name"] != "keep" {
		t.Fatal("lost model")
	}
	names := settings["context"].(map[string]any)["fileName"].([]any)
	if len(names) != 2 || names[0] != "CUSTOM.md" || names[1] != "AGENTS.md" {
		t.Fatal(names)
	}
	if err := m.ensureQwenContext(false); err != nil {
		t.Fatal(err)
	}
}

func TestCodexProfilesPreflightAndSeparateBackups(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	active := filepath.Join(home, "active-codex")
	t.Setenv("CODEX_HOME", active)
	m := NewAgentsManager(nil, home)
	m.SelectedTools = []string{"codex"}
	if _, err := m.Init(InitOptions{}); err != nil {
		t.Fatal(err)
	}
	standard := filepath.Join(home, ".codex", "AGENTS.md")
	if err := os.MkdirAll(filepath.Dir(standard), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(standard, []byte("standard edit\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Apply(ApplyOptions{}); err == nil {
		t.Fatal("standard conflict ignored")
	}
	activePath := filepath.Join(active, "AGENTS.md")
	if _, err := os.Stat(activePath); !os.IsNotExist(err) {
		t.Fatal("active file changed before standard conflict")
	}
	if _, err := os.Stat(m.StatePath()); !os.IsNotExist(err) {
		t.Fatal("state changed before conflict")
	}
	if err := os.MkdirAll(active, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(activePath, []byte("active edit\n"), 0644); err != nil {
		t.Fatal(err)
	}
	report, err := m.Apply(ApplyOptions{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Items) != 2 {
		t.Fatalf("got %d targets", len(report.Items))
	}
	if report.Items[0].BackupPath == report.Items[1].BackupPath {
		t.Fatal("profile backups collide")
	}
	first, _ := os.ReadFile(report.Items[0].BackupPath)
	second, _ := os.ReadFile(report.Items[1].BackupPath)
	if string(first) != "active edit\n" || string(second) != "standard edit\n" {
		t.Fatalf("backups lost: %q %q", first, second)
	}
	if err := os.Remove(standard); err != nil {
		t.Fatal(err)
	}
	statuses, err := m.Status()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, st := range statuses {
		if st.TargetPath == standard && st.Drift == "target-missing" {
			found = true
		}
	}
	if !found {
		t.Fatal("standard profile drift invisible")
	}
}

func TestRelativeRuntimeOverrideRefusesFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "relative-profile")
	m := NewAgentsManager(nil, home)
	if _, err := m.TargetPath("codex"); err == nil {
		t.Fatal("relative CODEX_HOME accepted")
	}
	m.ExplicitHome = true
	if got, err := m.TargetPath("codex"); err != nil || got != filepath.Join(home, ".codex", "AGENTS.md") {
		t.Fatalf("explicit home: %s %v", got, err)
	}
}

func TestAgentSelectionRespectsXDGAndExplicitHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	state := &config.UserState{}
	state.Modules.AI.Tooling = &config.AIToolingConfig{Agents: []string{"qwen"}}
	if err := config.SaveState(state); err != nil {
		t.Fatal(err)
	}
	m := NewAgentsManager(nil, home)
	if got := m.DefaultApplyTools(); len(got) != 1 || got[0] != "qwen" {
		t.Fatalf("ignored XDG selection: %v", got)
	}
	local := &config.UserState{}
	local.Modules.AI.Tooling = &config.AIToolingConfig{Agents: []string{"codex"}}
	if err := config.SaveStateForHome(home, local); err != nil {
		t.Fatal(err)
	}
	explicit := NewAgentsManager(nil, home, true)
	if got := explicit.DefaultApplyTools(); len(got) != 1 || got[0] != "codex" {
		t.Fatalf("explicit home leaked XDG selection: %v", got)
	}
}

func TestOpenCodeUsesCanonicalConfigHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OPENCODE_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "custom-config"))
	m := NewAgentsManager(nil, home)
	if got, err := m.TargetPath("opencode"); err != nil || got != filepath.Join(home, "custom-config", "opencode", "AGENTS.md") {
		t.Fatalf("ambient: %s %v", got, err)
	}
	t.Setenv("XDG_CONFIG_HOME", "relative")
	if _, err := m.TargetPath("opencode"); err == nil {
		t.Fatal("relative XDG fallback accepted")
	}
	explicit := NewAgentsManager(nil, home, true)
	if got, err := explicit.TargetPath("opencode"); err != nil || got != filepath.Join(home, ".config", "opencode", "AGENTS.md") {
		t.Fatalf("explicit: %s %v", got, err)
	}
}
