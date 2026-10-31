package aisettings

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustWriteJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, path, string(raw))
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// CODEX_HOME from the launching shell (Orca sets one) must not leak into the
// temp homes these tests build.
func TestMain(m *testing.M) {
	os.Unsetenv("CODEX_HOME")
	os.Exit(m.Run())
}

// snapshotTree maps every file under root to its content, so a test can prove
// a call wrote nothing.
func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			raw, _ := os.ReadFile(path)
			out[path] = string(raw)
		}
		return nil
	})
	return out
}

func sameTree(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// ---- AC1: the instruction block ----

const legacyBlock = legacyMemoryBlockStart + "\n## Persistent Memory\n\n- query the claude-mem MCP server\n" + legacyMemoryBlockEnd

func countOf(text, needle string) int { return strings.Count(text, needle) }

func TestEnsureMemoryInstructionsReplacesLegacyBlockExactlyOnce(t *testing.T) {
	ssot := filepath.Join(t.TempDir(), "AGENTS.md")
	mustWriteFile(t, ssot, "# Rules\n\nkeep this\n\n"+legacyBlock+"\n\n## After\n\nkeep that too\n")
	changed, err := EnsureMemoryInstructions(ssot)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	got, _ := os.ReadFile(ssot)
	text := string(got)
	if countOf(text, memoryBlockStart) != 1 || countOf(text, memoryBlockEnd) != 1 {
		t.Fatalf("want exactly one amem block:\n%s", text)
	}
	if strings.Contains(text, "claude-mem") {
		t.Fatalf("legacy block or mention survived:\n%s", text)
	}
	for _, keep := range []string{"# Rules", "keep this", "## After", "keep that too"} {
		if !strings.Contains(text, keep) {
			t.Fatalf("lost user content %q:\n%s", keep, text)
		}
	}
	if !HasMemoryInstructions(ssot) {
		t.Fatal("HasMemoryInstructions false after apply")
	}
}

func TestEnsureMemoryInstructionsIsIdempotent(t *testing.T) {
	ssot := filepath.Join(t.TempDir(), "AGENTS.md")
	mustWriteFile(t, ssot, "# Rules\n\n"+legacyBlock+"\n")
	if _, err := EnsureMemoryInstructions(ssot); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(ssot)
	for i := 0; i < 3; i++ {
		changed, err := EnsureMemoryInstructions(ssot)
		if err != nil || changed {
			t.Fatalf("re-apply %d changed=%v err=%v", i, changed, err)
		}
	}
	again, _ := os.ReadFile(ssot)
	if string(first) != string(again) || countOf(string(again), memoryBlockStart) != 1 {
		t.Fatalf("re-apply duplicated or drifted:\n%s", again)
	}
}

func TestEnsureMemoryInstructionsOnFileWithNoBlockAppendsOne(t *testing.T) {
	ssot := filepath.Join(t.TempDir(), "AGENTS.md")
	mustWriteFile(t, ssot, "# Rules\n")
	if changed, err := EnsureMemoryInstructions(ssot); err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	got, _ := os.ReadFile(ssot)
	if countOf(string(got), memoryBlockStart) != 1 || !strings.HasPrefix(string(got), "# Rules\n") {
		t.Fatalf("%s", got)
	}
}

func TestEnsureMemoryInstructionsRefusesMalformedMarkers(t *testing.T) {
	for name, body := range map[string]string{
		"duplicate legacy": legacyBlock + "\n" + legacyBlock + "\n",
		"duplicate amem":   memoryBlockStart + "\nx\n" + memoryBlockEnd + "\n" + memoryBlockStart + "\nx\n" + memoryBlockEnd + "\n",
		"unclosed amem":    "# r\n" + memoryBlockStart + "\nno end\n",
		"end before start": memoryBlockEnd + "\n" + memoryBlockStart + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			ssot := filepath.Join(t.TempDir(), "AGENTS.md")
			mustWriteFile(t, ssot, body)
			if _, err := EnsureMemoryInstructions(ssot); err == nil {
				t.Fatal("accepted malformed markers")
			}
			if got, _ := os.ReadFile(ssot); string(got) != body {
				t.Fatalf("a refused apply still rewrote the file:\n%s", got)
			}
		})
	}
}

func TestMemoryInstructionsNeededWritesNothing(t *testing.T) {
	ssot := filepath.Join(t.TempDir(), "AGENTS.md")
	mustWriteFile(t, ssot, legacyBlock+"\n")
	before := snapshotTree(t, filepath.Dir(ssot))
	needed, err := MemoryInstructionsNeeded(ssot)
	if err != nil || !needed {
		t.Fatalf("needed=%v err=%v", needed, err)
	}
	if !sameTree(before, snapshotTree(t, filepath.Dir(ssot))) {
		t.Fatal("inspection wrote a file")
	}
	if _, err := EnsureMemoryInstructions(ssot); err != nil {
		t.Fatal(err)
	}
	if needed, _ = MemoryInstructionsNeeded(ssot); needed {
		t.Fatal("still needed after apply")
	}
}

// The block is a pointer, not the rule text, and names the real amem surface.
func TestMemoryInstructionsNameRealAmemSurface(t *testing.T) {
	for _, want := range []string{"amem_search", "amem_work_state_read", "amem ingest", "Vault-First"} {
		if !strings.Contains(memoryInstructions, want) {
			t.Errorf("block does not mention %q", want)
		}
	}
	if strings.Contains(memoryInstructions, "claude-mem") {
		t.Error("amem block mentions claude-mem")
	}
	if n := strings.Count(memoryInstructions, "\n"); n > 8 {
		t.Errorf("block is %d lines; the rule text lives in the workspace, this stays a short pointer", n)
	}
}

// ---- AC2: install registers amem, writes no launchd agent ----

func newAmem(t *testing.T) (*AmemManager, string) {
	t.Helper()
	home := t.TempDir()
	amem := filepath.Join(home, "bin", "amem")
	mustWriteFile(t, amem, "#!/bin/sh\necho 0.14.0\n")
	if err := os.Chmod(amem, 0o755); err != nil {
		t.Fatal(err)
	}
	return NewAmemManager(home, amem), home
}

func readServers(t *testing.T, path string) map[string]map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Servers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Servers
}

func TestAmemInstallRegistersKiroAndCopilotOnCleanHome(t *testing.T) {
	m, home := newAmem(t)
	result, err := m.Install()
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ConfigPaths) != 2 {
		t.Fatalf("want the Kiro and Copilot configs written, got %v", result.ConfigPaths)
	}
	for agent, path := range map[string]string{"kiro": m.KiroMCPPath(), "copilot": m.CopilotMCPPath()} {
		entry := readServers(t, path)[AmemServerName]
		if entry == nil {
			t.Fatalf("%s: no amem entry in %s", agent, path)
		}
		if entry["command"] != m.AmemPath {
			t.Errorf("%s: command %v, want %s", agent, entry["command"], m.AmemPath)
		}
		if args, _ := entry["args"].([]any); len(args) != 1 || args[0] != "mcp" {
			t.Errorf("%s: args %v, want [mcp]", agent, entry["args"])
		}
		if !strings.HasPrefix(path, home) {
			t.Errorf("%s wrote outside the home: %s", agent, path)
		}
	}
	if e := readServers(t, m.KiroMCPPath())[AmemServerName]; e["disabled"] != false {
		t.Errorf("kiro entry must carry disabled:false: %v", e)
	}
	if e := readServers(t, m.CopilotMCPPath())[AmemServerName]; e["type"] != "local" {
		t.Errorf("copilot entry must be type local: %v", e)
	}
	for _, dir := range []string{".pi", ".kimi-code", ".qwen", ".gemini"} {
		if _, err := os.Stat(filepath.Join(home, dir)); err == nil {
			t.Errorf("install created %s, which `amem setup` owns", dir)
		}
	}
}

// AC: Kimi, Qwen and Antigravity configs belong to `amem setup` and stay byte-identical, whatever they hold
// and whatever the selection says.
func TestAmemInstallLeavesTheAmemSetupConfigsByteIdentical(t *testing.T) {
	m, _ := newAmem(t)
	files := map[string]string{
		m.KimiMCPPath():        `{"mcpServers":{"amem":{"command":"/opt/bun","args":["--smol","/x/amem.js","mcp"]}}}`,
		m.QwenMCPPath():        `{"context":{},"mcpServers":{}}`,
		m.AntigravityMCPPath(): "",
	}
	for path, content := range files {
		mustWriteFile(t, path, content)
	}
	m.SelectedAgents = []string{"kimi", "qwen", "antigravity", "kiro", "copilot"}
	for _, run := range []func() (AmemInstallResult, error){m.Plan, m.Install} {
		result, err := run()
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range result.ConfigPaths {
			if _, owned := files[path]; owned {
				t.Errorf("listed %s, which `amem setup` owns", path)
			}
		}
	}
	for path, content := range files {
		if got, _ := os.ReadFile(path); string(got) != content {
			t.Errorf("%s changed: %q", path, got)
		}
	}
}

func TestAmemInstallWritesNoLaunchAgentAndNoClaudeMemData(t *testing.T) {
	m, home := newAmem(t)
	if _, err := m.Install(); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"Library", ".claude-mem", ".claude", ".codex"} {
		if _, err := os.Stat(filepath.Join(home, forbidden)); err == nil {
			t.Errorf("install created %s", forbidden)
		}
	}
	for path := range snapshotTree(t, home) {
		if strings.Contains(path, "LaunchAgents") || strings.HasSuffix(path, ".plist") {
			t.Errorf("install wrote a launchd file: %s", path)
		}
	}
}

func TestAmemInstallPlanWritesNothing(t *testing.T) {
	m, home := newAmem(t)
	before := snapshotTree(t, home)
	result, err := m.Plan()
	if err != nil || len(result.ConfigPaths) != 2 {
		t.Fatalf("plan: %v %v", result, err)
	}
	if !sameTree(before, snapshotTree(t, home)) {
		t.Fatal("plan wrote a file")
	}
}

func TestAmemInstallPreservesOtherServersAndKeysAndIsIdempotent(t *testing.T) {
	m, _ := newAmem(t)
	mustWriteJSON(t, m.KiroMCPPath(), map[string]any{
		"context":    map[string]any{"fileName": []string{"AGENTS.md"}},
		"mcpServers": map[string]any{"other": map[string]any{"command": "/bin/other", "args": []string{"x"}}},
	})
	if _, err := m.Install(); err != nil {
		t.Fatal(err)
	}
	servers := readServers(t, m.KiroMCPPath())
	if servers["other"]["command"] != "/bin/other" || servers[AmemServerName] == nil {
		t.Fatalf("lost or missed a server: %v", servers)
	}
	var doc map[string]json.RawMessage
	raw, _ := os.ReadFile(m.KiroMCPPath())
	_ = json.Unmarshal(raw, &doc)
	if _, ok := doc["context"]; !ok {
		t.Fatal("lost the unrelated top-level key")
	}
	second, err := m.Install()
	if err != nil || len(second.ConfigPaths) != 0 {
		t.Fatalf("second install not idempotent: %v %v", second, err)
	}
}

func TestAmemInstallTreatsAnEmptyConfigAsNoServers(t *testing.T) {
	m, _ := newAmem(t)
	mustWriteFile(t, m.KiroMCPPath(), "")
	if _, err := m.Install(); err != nil {
		t.Fatal(err)
	}
	if readServers(t, m.KiroMCPPath())[AmemServerName] == nil {
		t.Fatal("no entry in the formerly empty config")
	}
}

func TestAmemInstallRespectsSelection(t *testing.T) {
	m, home := newAmem(t)
	m.SelectedAgents = []string{"copilot"}
	result, err := m.Install()
	if err != nil || len(result.ConfigPaths) != 1 || result.ConfigPaths[0] != m.CopilotMCPPath() {
		t.Fatalf("selection ignored: %v %v", result, err)
	}
	m.SelectedAgents = []string{"kimi", "qwen", "antigravity"}
	m.KimiHome = filepath.Join(home, "adopted-kimi")
	if r, err := m.Install(); err != nil || len(r.ConfigPaths) != 0 {
		t.Fatalf("an `amem setup` client was written: %v %v", r, err)
	}
	if _, err := os.Stat(m.KimiHome); err == nil {
		t.Fatal("wrote the adopted Kimi home")
	}
	m.SelectedAgents = []string{}
	if r, err := m.Install(); err != nil || len(r.ConfigPaths) != 0 {
		t.Fatalf("an empty selection must write nothing: %v %v", r, err)
	}
}

func TestAmemInstallNeedsAbsoluteExecutable(t *testing.T) {
	for _, path := range []string{"", "amem", "./amem"} {
		m := NewAmemManager(t.TempDir(), path)
		if _, err := m.Install(); err == nil {
			t.Errorf("accepted amem path %q", path)
		}
	}
	m := NewAmemManager(t.TempDir(), "/opt/bin/amem")
	m.ExplicitHome = true
	if _, err := m.Install(); err == nil {
		t.Error("explicit --home without a selection must be refused")
	}
	// ... but a plan writes nothing, so it may show what every agent would get.
	if r, err := m.Plan(); err != nil || len(r.ConfigPaths) != 2 {
		t.Errorf("plan under an explicit --home without a selection: %v %v", r, err)
	}
	if entries, _ := os.ReadDir(m.HomeDir); len(entries) != 0 {
		t.Errorf("the refused install or the plan wrote into the home: %v", entries)
	}
}

func TestAmemInstallRefusesUnparsableConfigWithoutOverwriting(t *testing.T) {
	m, _ := newAmem(t)
	mustWriteFile(t, m.CopilotMCPPath(), "{not json")
	if _, err := m.Install(); err == nil {
		t.Fatal("overwrote an unparsable config")
	}
	if got, _ := os.ReadFile(m.CopilotMCPPath()); string(got) != "{not json" {
		t.Fatalf("config changed: %s", got)
	}
}

// The forms below were checked in the published 0.14.0 tarball source: `setup` takes claude, codex, kimi, pi,
// qwen, jcode, agy and all (all leaves agy out), and `ingest` takes codex, kimi, pi and qwen.
func TestAmemInstallPrintsTheAmemSetupLines(t *testing.T) {
	m, _ := newAmem(t)
	result, err := m.Install()
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(result.Manual, "\n")
	for _, want := range []string{"amem setup all --apply", "amem setup claude --apply", "amem setup agy --apply", "either `amem setup claude` or the agent-mem plugin", "amem ingest codex"} {
		if !strings.Contains(joined, want) {
			t.Errorf("manual lines missing %q:\n%s", want, joined)
		}
	}
	// The 0.11.0 plugin declares its own MCP server, so the old two-step Claude registration would duplicate it.
	for _, gone := range []string{"claude mcp add", "--plugin-dir", "codex mcp add"} {
		if strings.Contains(joined, gone) {
			t.Errorf("manual lines still print %q:\n%s", gone, joined)
		}
	}
	for _, c := range []string{".claude/settings.json", ".codex/config.toml"} {
		if _, err := os.Stat(filepath.Join(m.HomeDir, c)); err == nil {
			t.Errorf("install wrote %s, a file dot does not own", c)
		}
	}
}

func TestAmemStatusReportsEachAgentAndVersion(t *testing.T) {
	m, _ := newAmem(t)
	m.Version = func(context.Context, string) (string, error) { return "0.14.0", nil }
	ssot := filepath.Join(m.HomeDir, "AGENTS.md")
	mustWriteFile(t, ssot, "# r\n")
	st := m.Status(context.Background(), ssot)
	if st.KiroMCP || st.CopilotMCP || st.InstructionsEnabled {
		t.Fatalf("clean home reported ready: %+v", st)
	}
	if st.Version != "0.14.0" {
		t.Fatalf("version %q", st.Version)
	}
	if _, err := m.Install(); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureMemoryInstructions(ssot); err != nil {
		t.Fatal(err)
	}
	st = m.Status(context.Background(), ssot)
	if !st.KiroMCP || !st.CopilotMCP || !st.InstructionsEnabled {
		t.Fatalf("installed home not ready: %+v", st)
	}
}

// A different executable, a disabled kiro entry and a wrong-shaped copilot entry
// must not read as ready.
func TestAmemStatusRejectsForeignOrMisshapenEntries(t *testing.T) {
	m, _ := newAmem(t)
	mustWriteJSON(t, m.KiroMCPPath(), map[string]any{"mcpServers": map[string]any{AmemServerName: map[string]any{"command": m.AmemPath, "args": []string{"mcp"}, "disabled": true}}})
	mustWriteJSON(t, m.CopilotMCPPath(), map[string]any{"mcpServers": map[string]any{AmemServerName: map[string]any{"command": m.AmemPath, "args": []string{"mcp"}}}})
	m.Version = func(context.Context, string) (string, error) { return "", os.ErrNotExist }
	st := m.Status(context.Background(), "")
	if st.KiroMCP || st.CopilotMCP {
		t.Fatalf("misshapen entries counted as ready: %+v", st)
	}
	mustWriteJSON(t, m.KiroMCPPath(), map[string]any{"mcpServers": map[string]any{AmemServerName: map[string]any{"command": "/usr/bin/other", "args": []string{"mcp"}}}})
	mustWriteJSON(t, m.CopilotMCPPath(), map[string]any{"mcpServers": map[string]any{AmemServerName: map[string]any{"command": m.AmemPath, "args": []string{"mcp", "--extra"}, "type": "local", "tools": []string{"*"}}}})
	if st := m.Status(context.Background(), ""); st.KiroMCP || st.CopilotMCP {
		t.Fatalf("a foreign command or extra args counted as ready: %+v", st)
	}
	if st.Version != "" {
		t.Fatalf("an unreadable version must stay empty, got %q", st.Version)
	}
}

func TestAmemPinnedVersionIsAStableSemver(t *testing.T) {
	parts := strings.Split(AmemPinnedVersion, ".")
	if len(parts) != 3 {
		t.Fatalf("pin %q is not X.Y.Z", AmemPinnedVersion)
	}
	if AmemPinnedVersion == "0.5.0" {
		t.Fatal("0.5.0 lacks the P3 hooks and ingest; the pin must be the first release that has them")
	}
}
