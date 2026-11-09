package aisettings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSkillsManagerStatusClassifiesTargets(t *testing.T) {
	home := t.TempDir()
	source := filepath.Join(home, ".maru", "skills")
	writeSkillTestFile(t, filepath.Join(source, "in-sync-skill", "SKILL.md"), "# In Sync\n")
	writeSkillTestFile(t, filepath.Join(source, "missing-skill", "SKILL.md"), "# Missing\n")
	writeSkillTestFile(t, filepath.Join(source, "conflict-skill", "SKILL.md"), "# Conflict\n")

	claudeRoot := filepath.Join(home, ".claude", "skills")
	if err := os.MkdirAll(claudeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	// in-sync: symlink to the source
	if err := os.Symlink(filepath.Join(source, "in-sync-skill"), filepath.Join(claudeRoot, "in-sync-skill")); err != nil {
		t.Fatal(err)
	}
	// conflict: real directory instead of a symlink
	writeSkillTestFile(t, filepath.Join(claudeRoot, "conflict-skill", "SKILL.md"), "# Hand Edit\n")

	mgr := NewSkillsManager(home)
	report, err := mgr.Status(SkillsOptions{Provider: SkillsProviderMaru, Tools: []string{"claude"}})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	got := map[string]string{}
	for _, item := range report.Items {
		got[item.SkillName] = item.Status
	}
	want := map[string]string{
		"in-sync-skill":  SkillLinkStatusInSync,
		"missing-skill":  SkillLinkStatusMissing,
		"conflict-skill": SkillLinkStatusConflict,
	}
	for name, status := range want {
		if got[name] != status {
			t.Errorf("skill %s status = %q, want %q (all: %v)", name, got[name], status, got)
		}
	}
	// Status must never create or repair anything.
	if _, err := os.Lstat(filepath.Join(claudeRoot, "missing-skill")); !os.IsNotExist(err) {
		t.Fatalf("status created missing target, stat err=%v", err)
	}
}

func TestSkillsManagerStatusSourceMissing(t *testing.T) {
	home := t.TempDir() // no ~/.maru/skills at all
	mgr := NewSkillsManager(home)

	report, err := mgr.Status(SkillsOptions{Provider: SkillsProviderMaru, Tools: []string{"claude"}})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(report.Items) != 1 || report.Items[0].Status != SkillLinkStatusSourceMissing {
		t.Fatalf("items = %#v, want single source-missing item", report.Items)
	}
	if len(report.Warnings) == 0 {
		t.Fatal("missing SSOT root should produce a warning")
	}
}

func TestSkillsManagerRejectsRetiredAnchorProvider(t *testing.T) {
	mgr := NewSkillsManager(t.TempDir())
	if _, err := mgr.Status(SkillsOptions{Provider: "anchor", Tools: []string{"claude"}}); err == nil {
		t.Fatal("retired anchor provider must be rejected")
	}
}

func TestSkillsManagerMaruProviderDefaults(t *testing.T) {
	home := t.TempDir()
	writeSkillTestFile(t, filepath.Join(home, ".maru", "skills", "vault-extract", "SKILL.md"), "# Vault Extract\n")
	mgr := NewSkillsManager(home)

	report, err := mgr.Status(SkillsOptions{Provider: SkillsProviderMaru, Tools: []string{"claude"}})
	if err != nil {
		t.Fatalf("Status with maru provider: %v", err)
	}
	if report.Provider != SkillsProviderMaru {
		t.Fatalf("provider = %q, want %q", report.Provider, SkillsProviderMaru)
	}
	if report.SSOTPath != filepath.Join(home, ".maru", "skills") {
		t.Fatalf("ssot = %q, want maru default", report.SSOTPath)
	}
	if len(report.Sources) != 1 || report.Sources[0].Name != "vault-extract" {
		t.Fatalf("sources = %#v", report.Sources)
	}
}

func TestSkillsManagerRejectsRetiredInventoryOnlyTargets(t *testing.T) {
	mgr := NewSkillsManager(t.TempDir())
	for _, tool := range []string{"agents", "agy"} {
		if _, err := mgr.Status(SkillsOptions{Provider: SkillsProviderMaru, Tools: []string{tool}}); err == nil {
			t.Errorf("retired skills tool %q must be rejected", tool)
		}
	}
}

func TestSkillsManagerDefaultToolsDetectsPresentSkillRoots(t *testing.T) {
	home := t.TempDir()
	// Create the skills roots themselves (not just the tool homes).
	if err := os.MkdirAll(filepath.Join(home, ".claude", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".gemini", "antigravity", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	mgr := NewSkillsManager(home)

	got := mgr.DefaultTools()
	if !defaultToolsHas(got, "claude") {
		t.Fatalf("DefaultTools = %v, want claude", got)
	}
	if defaultToolsHas(got, "antigravity") {
		t.Fatalf("DefaultTools treated unsupported Antigravity as managed: %v", got)
	}
	if defaultToolsHas(got, "codex") {
		t.Fatalf("DefaultTools detected codex without ~/.codex/skills: %v", got)
	}
}

func TestSkillsManagerDefaultToolsFallsBackToAll(t *testing.T) {
	home := t.TempDir() // no skills roots present
	mgr := NewSkillsManager(home)

	want := map[string]bool{}
	for _, tool := range RegisteredSkillTools() {
		want[tool.ID] = true
	}
	got := mgr.DefaultTools()
	if len(got) != len(want) {
		t.Fatalf("fallback DefaultTools = %v, want all %d registered tools", got, len(want))
	}
	for _, id := range got {
		if !want[id] {
			t.Fatalf("fallback returned unexpected tool %q (got %v)", id, got)
		}
		delete(want, id)
	}
	if len(want) != 0 {
		t.Fatalf("fallback omitted registered tools: %v", want)
	}
}

func defaultToolsHas(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func writeSkillTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
