package aisettings

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	mustWriteFile(t, amem, "#!/bin/sh\necho 0.6.0\n")
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

func TestAmemInstallRegistersEveryAgentOnCleanHome(t *testing.T) {
	m, home := newAmem(t)
	result, err := m.Install()
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ConfigPaths) != 5 {
		t.Fatalf("want 5 MCP configs written, got %v", result.ConfigPaths)
	}
	for agent, path := range map[string]string{"kimi": m.KimiMCPPath(), "qwen": m.QwenMCPPath(), "antigravity": m.AntigravityMCPPath(), "kiro": m.KiroMCPPath(), "copilot": m.CopilotMCPPath()} {
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
	if _, err := os.Stat(filepath.Join(home, ".pi")); err == nil {
		t.Error("pi has no MCP support: install must not create its directory")
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
	if err != nil || len(result.ConfigPaths) != 5 {
		t.Fatalf("plan: %v %v", result, err)
	}
	if !sameTree(before, snapshotTree(t, home)) {
		t.Fatal("plan wrote a file")
	}
}

func TestAmemInstallPreservesOtherServersAndKeysAndIsIdempotent(t *testing.T) {
	m, _ := newAmem(t)
	mustWriteJSON(t, m.QwenMCPPath(), map[string]any{
		"context":    map[string]any{"fileName": []string{"AGENTS.md"}},
		"mcpServers": map[string]any{"other": map[string]any{"command": "/bin/other", "args": []string{"x"}}},
	})
	if _, err := m.Install(); err != nil {
		t.Fatal(err)
	}
	servers := readServers(t, m.QwenMCPPath())
	if servers["other"]["command"] != "/bin/other" || servers[AmemServerName] == nil {
		t.Fatalf("lost or missed a server: %v", servers)
	}
	var doc map[string]json.RawMessage
	raw, _ := os.ReadFile(m.QwenMCPPath())
	_ = json.Unmarshal(raw, &doc)
	if _, ok := doc["context"]; !ok {
		t.Fatal("lost the unrelated top-level key")
	}
	second, err := m.Install()
	if err != nil || len(second.ConfigPaths) != 0 {
		t.Fatalf("second install not idempotent: %v %v", second, err)
	}
}

func TestAmemInstallTreatsEmptyAgyConfigAsNoServers(t *testing.T) {
	m, _ := newAmem(t)
	mustWriteFile(t, m.AntigravityMCPPath(), "")
	if _, err := m.Install(); err != nil {
		t.Fatal(err)
	}
	if readServers(t, m.AntigravityMCPPath())[AmemServerName] == nil {
		t.Fatal("no entry in the formerly empty agy config")
	}
}

func TestAmemInstallRespectsSelectionAndKimiHome(t *testing.T) {
	m, home := newAmem(t)
	m.SelectedAgents = []string{"qwen"}
	kimi := filepath.Join(home, "adopted-kimi")
	m.KimiHome = kimi
	result, err := m.Install()
	if err != nil || len(result.ConfigPaths) != 1 || result.ConfigPaths[0] != m.QwenMCPPath() {
		t.Fatalf("selection ignored: %v %v", result, err)
	}
	m.SelectedAgents = []string{"kimi"}
	if _, err := m.Install(); err != nil {
		t.Fatal(err)
	}
	if readServers(t, filepath.Join(kimi, "mcp.json"))[AmemServerName] == nil {
		t.Fatal("adopted KIMI_CODE_HOME ignored")
	}
	if _, err := os.Stat(filepath.Join(home, ".kimi-code")); err == nil {
		t.Fatal("wrote ~/.kimi-code although a Kimi home was adopted")
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
	if r, err := m.Plan(); err != nil || len(r.ConfigPaths) != 5 {
		t.Errorf("plan under an explicit --home without a selection: %v %v", r, err)
	}
	if entries, _ := os.ReadDir(m.HomeDir); len(entries) != 0 {
		t.Errorf("the refused install or the plan wrote into the home: %v", entries)
	}
}

func TestAmemInstallRefusesUnparsableConfigWithoutOverwriting(t *testing.T) {
	m, _ := newAmem(t)
	mustWriteFile(t, m.KimiMCPPath(), "{not json")
	if _, err := m.Install(); err == nil {
		t.Fatal("overwrote an unparsable config")
	}
	if got, _ := os.ReadFile(m.KimiMCPPath()); string(got) != "{not json" {
		t.Fatalf("config changed: %s", got)
	}
}

func TestAmemInstallPrintsExactManualLinesForClaudeAndCodex(t *testing.T) {
	m, _ := newAmem(t)
	result, err := m.Install()
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(result.Manual, "\n")
	for _, want := range []string{"claude --plugin-dir", "codex mcp add amem -- " + m.AmemPath + " mcp", "amem ingest pi"} {
		if !strings.Contains(joined, want) {
			t.Errorf("manual lines missing %q:\n%s", want, joined)
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
	m.Version = func(context.Context, string) (string, error) { return "0.6.0", nil }
	ssot := filepath.Join(m.HomeDir, "AGENTS.md")
	mustWriteFile(t, ssot, "# r\n")
	st := m.Status(context.Background(), ssot)
	if st.KimiMCP || st.QwenMCP || st.KiroMCP || st.CopilotMCP || st.AntigravityMCP || st.InstructionsEnabled {
		t.Fatalf("clean home reported ready: %+v", st)
	}
	if st.Version != "0.6.0" {
		t.Fatalf("version %q", st.Version)
	}
	if _, err := m.Install(); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureMemoryInstructions(ssot); err != nil {
		t.Fatal(err)
	}
	st = m.Status(context.Background(), ssot)
	if !(st.KimiMCP && st.QwenMCP && st.KiroMCP && st.CopilotMCP && st.AntigravityMCP && st.InstructionsEnabled) {
		t.Fatalf("installed home not ready: %+v", st)
	}
}

// A different executable, a disabled kiro entry and a wrong-shaped copilot entry
// must not read as ready.
func TestAmemStatusRejectsForeignOrMisshapenEntries(t *testing.T) {
	m, _ := newAmem(t)
	mustWriteJSON(t, m.KimiMCPPath(), map[string]any{"mcpServers": map[string]any{AmemServerName: map[string]any{"command": "/usr/bin/other", "args": []string{"mcp"}}}})
	mustWriteJSON(t, m.KiroMCPPath(), map[string]any{"mcpServers": map[string]any{AmemServerName: map[string]any{"command": m.AmemPath, "args": []string{"mcp"}, "disabled": true}}})
	mustWriteJSON(t, m.CopilotMCPPath(), map[string]any{"mcpServers": map[string]any{AmemServerName: map[string]any{"command": m.AmemPath, "args": []string{"mcp"}}}})
	mustWriteJSON(t, m.QwenMCPPath(), map[string]any{"mcpServers": map[string]any{AmemServerName: map[string]any{"command": m.AmemPath, "args": []string{"mcp", "--extra"}}}})
	m.Version = func(context.Context, string) (string, error) { return "", os.ErrNotExist }
	st := m.Status(context.Background(), "")
	if st.KimiMCP || st.KiroMCP || st.CopilotMCP || st.QwenMCP {
		t.Fatalf("misshapen entries counted as ready: %+v", st)
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

// ---- AC4: the retirement ----

type recorder struct {
	calls [][]string
	fail  map[string]bool
}

func (r *recorder) run(_ context.Context, name string, args ...string) (string, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	if r.fail[name+" "+strings.Join(args, " ")] {
		return "boom", os.ErrInvalid
	}
	return "", nil
}

// seedClaudeMemHome builds a home that has every claude-mem footprint dot knows.
func seedClaudeMemHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	mustWriteJSON(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"), map[string]any{"plugins": map[string]any{claudeMemPluginID: []any{map[string]any{"scope": "user"}}}})
	mustWriteJSON(t, filepath.Join(home, ".claude", "plugins", "known_marketplaces.json"), map[string]any{claudeMemMarketplace: map[string]any{}})
	mustWriteFile(t, filepath.Join(home, ".claude", "settings.json"), "{\n  \"permissions\": {\n    \"allow\": [\n      \"mcp__plugin_claude-mem_mcp-search\"\n    ]\n  },\n  \"enabledPlugins\": {\n    \"claude-mem@thedotmack\": true\n  }\n}\n")
	mustWriteFile(t, filepath.Join(home, ".codex", "config.toml"), "[marketplaces.claude-mem-local]\nsource = \"x\"\n[plugins.\"claude-mem@claude-mem-local\"]\nenabled = true\n")
	for _, p := range []string{filepath.Join(home, ".kimi-code", "mcp.json"), filepath.Join(home, ".qwen", "settings.json"), filepath.Join(home, ".gemini", "config", "mcp_config.json"), filepath.Join(home, ".kiro", "settings", "mcp.json"), filepath.Join(home, ".copilot", "mcp-config.json")} {
		mustWriteJSON(t, p, map[string]any{"mcpServers": map[string]any{"claude-mem": map[string]any{"command": "/x/dot"}, "keepme": map[string]any{"command": "/x/keep"}}})
	}
	for _, label := range []string{claudeMemBridgeLabel, claudeMemSyncLabel} {
		mustWriteFile(t, filepath.Join(home, "Library", "LaunchAgents", label+".plist"), "<plist/>")
	}
	mustWriteFile(t, filepath.Join(home, ".claude-mem", "claude-mem.db"), "SQLITE-DATA")
	mustWriteFile(t, filepath.Join(home, ".claude-mem", "settings.json"), "{}")
	return home
}

func retireOpts(home string, rec *recorder) RetireOptions {
	return RetireOptions{
		HomeDir: home, Run: rec.run, GOOS: "linux", AmemHome: filepath.Join(home, ".agenthub", "memory"),
		Now:      func() time.Time { return time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC) },
		Ps:       func(context.Context) (string, error) { return "/sbin/launchd\n/usr/bin/zsh\n", nil },
		PortOpen: func(int) bool { return false },
	}
}

func stepIDs(r RetireResult) map[string]RetireStatus {
	out := map[string]RetireStatus{}
	for _, s := range r.Steps {
		out[s.ID] = s.Status
	}
	return out
}

func TestRetireDryRunListsEveryItemAndTouchesNothing(t *testing.T) {
	home := seedClaudeMemHome(t)
	rec := &recorder{}
	before := snapshotTree(t, home)
	result, err := RetireClaudeMem(context.Background(), retireOpts(home, rec))
	if err != nil {
		t.Fatal(err)
	}
	if result.Apply || result.Blocked != "" {
		t.Fatalf("dry run flags: %+v", result)
	}
	if len(rec.calls) != 0 {
		t.Fatalf("a dry run ran native commands: %v", rec.calls)
	}
	if !sameTree(before, snapshotTree(t, home)) {
		t.Fatal("a dry run changed the home")
	}
	want := []string{
		"claude-plugin-disable", "claude-plugin-uninstall", "claude-marketplace", "codex-plugin", "codex-marketplace",
		"mcp-kimi", "mcp-qwen", "mcp-antigravity", "mcp-kiro", "mcp-copilot",
		"launchd-" + claudeMemBridgeLabel, "launchd-" + claudeMemSyncLabel, "archive",
	}
	got := stepIDs(result)
	for _, id := range want {
		if got[id] != RetireWould {
			t.Errorf("step %s is %q, want %q", id, got[id], RetireWould)
		}
	}
	if len(result.Steps) != len(want) {
		t.Errorf("%d steps, want %d", len(result.Steps), len(want))
	}
	if result.Clean() {
		t.Fatal("the read-back must show residue before the real run")
	}
}

func TestRetireDryRunPrintsExactSettingsEditsAndNeverWritesThem(t *testing.T) {
	home := seedClaudeMemHome(t)
	settings := filepath.Join(home, ".claude", "settings.json")
	before, _ := os.ReadFile(settings)
	result, err := RetireClaudeMem(context.Background(), retireOpts(home, &recorder{}))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(result.Manual, "\n")
	for _, want := range []string{settings + " line 4: remove \"mcp__plugin_claude-mem_mcp-search\"", settings + " line 8: remove \"claude-mem@thedotmack\": true"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing edit %q in:\n%s", want, joined)
		}
	}
	if after, _ := os.ReadFile(settings); string(after) != string(before) {
		t.Fatal("dot wrote a file it does not own")
	}
}

func TestRetireDryRunOnAHomeWithoutClaudeMemHasNothingToDo(t *testing.T) {
	home := t.TempDir()
	result, err := RetireClaudeMem(context.Background(), retireOpts(home, &recorder{}))
	if err != nil {
		t.Fatal(err)
	}
	for id, status := range stepIDs(result) {
		if status != RetireAbsent {
			t.Errorf("%s is %q on a clean home", id, status)
		}
	}
	if len(result.Manual) != 0 || !result.Clean() {
		t.Fatalf("clean home: manual=%v clean=%v", result.Manual, result.Clean())
	}
}

func TestRetireApplyRefusesWithoutImportConfirmationOrEvidence(t *testing.T) {
	home := seedClaudeMemHome(t)
	for name, opts := range map[string]func(*RetireOptions){
		"no confirmation":        func(o *RetireOptions) {},
		"confirmed, no evidence": func(o *RetireOptions) { o.ImportConfirmed = true },
	} {
		t.Run(name, func(t *testing.T) {
			rec := &recorder{}
			o := retireOpts(home, rec)
			o.Apply = true
			opts(&o)
			before := snapshotTree(t, home)
			result, err := RetireClaudeMem(context.Background(), o)
			if err != nil || result.Blocked == "" {
				t.Fatalf("not blocked: %+v err=%v", result, err)
			}
			if len(rec.calls) != 0 || !sameTree(before, snapshotTree(t, home)) {
				t.Fatal("a blocked apply changed something")
			}
			if !strings.Contains(result.Blocked, "import") {
				t.Fatalf("block reason does not name the import: %s", result.Blocked)
			}
		})
	}
}

func seedImport(t *testing.T, home string) {
	t.Helper()
	mustWriteFile(t, filepath.Join(home, ".agenthub", "memory", "projects", "abc", "import-state.json"), `{"deadbeef": 42}`)
}

func TestRetireApplyArchivesNeverDeletesAndLeavesNoResidue(t *testing.T) {
	home := seedClaudeMemHome(t)
	seedImport(t, home)
	rec := &recorder{}
	o := retireOpts(home, rec)
	o.Apply, o.ImportConfirmed = true, true
	result, err := RetireClaudeMem(context.Background(), o)
	if err != nil || result.Blocked != "" {
		t.Fatalf("blocked: %+v err=%v", result, err)
	}
	for _, s := range result.Steps {
		if s.Status != RetireDone {
			t.Errorf("step %s ended %q: %s", s.ID, s.Status, s.Detail)
		}
	}
	archive := filepath.Join(home, ".claude-mem.archive-20261024")
	if result.Archive != archive {
		t.Fatalf("archive %q, want %q", result.Archive, archive)
	}
	if raw, err := os.ReadFile(filepath.Join(archive, "claude-mem.db")); err != nil || string(raw) != "SQLITE-DATA" {
		t.Fatalf("the data is not intact in the archive: %v %q", err, raw)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude-mem")); err == nil {
		t.Fatal("the original directory is still there")
	}
	for _, label := range []string{claudeMemBridgeLabel, claudeMemSyncLabel} {
		if _, err := os.Stat(filepath.Join(home, "Library", "LaunchAgents", label+".plist")); err == nil {
			t.Errorf("%s survived", label)
		}
	}
	for _, p := range []string{filepath.Join(home, ".kimi-code", "mcp.json"), filepath.Join(home, ".copilot", "mcp-config.json")} {
		servers := readServers(t, p)
		if servers["claude-mem"] != nil || servers["keepme"] == nil {
			t.Errorf("%s: claude-mem not removed or another server lost: %v", p, servers)
		}
	}
	want := [][]string{
		{"claude", "plugin", "disable", claudeMemPluginID}, {"claude", "plugin", "uninstall", claudeMemPluginID},
		{"claude", "plugin", "marketplace", "remove", claudeMemMarketplace},
		{"codex", "plugin", "remove", "claude-mem", "--marketplace", codexClaudeMemMarket},
		{"codex", "plugin", "marketplace", "remove", codexClaudeMemMarket},
	}
	if len(rec.calls) != len(want) {
		t.Fatalf("native commands: %v", rec.calls)
	}
	for i, call := range want {
		if strings.Join(rec.calls[i], " ") != strings.Join(call, " ") {
			t.Errorf("call %d = %v, want %v", i, rec.calls[i], call)
		}
	}
}

// The native commands here only record: the plugin files stay on disk, as they
// would if the CLI refused. The read-back must say so instead of passing.
func TestRetireReadBackFailsClosedOnEachKindOfResidue(t *testing.T) {
	cases := map[string]func(t *testing.T, home string, o *RetireOptions){
		"process": func(t *testing.T, home string, o *RetireOptions) {
			o.Ps = func(context.Context) (string, error) {
				return "bun /x/plugins/claude-mem/scripts/worker-service.cjs --daemon\n", nil
			}
		},
		"unreadable process list": func(t *testing.T, home string, o *RetireOptions) {
			o.Ps = func(context.Context) (string, error) { return "", os.ErrPermission }
		},
		"port": func(t *testing.T, home string, o *RetireOptions) { o.PortOpen = func(int) bool { return true } },
		"worker launchd agent": func(t *testing.T, home string, o *RetireOptions) {
			mustWriteFile(t, filepath.Join(home, "Library", "LaunchAgents", claudeMemWorkerPlist), "<plist/>")
		},
		"installed plugin": func(t *testing.T, home string, o *RetireOptions) {
			mustWriteJSON(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"), map[string]any{"plugins": map[string]any{claudeMemPluginID: []any{}}})
		},
		"codex config": func(t *testing.T, home string, o *RetireOptions) {
			mustWriteFile(t, filepath.Join(home, ".codex", "config.toml"), "[plugins.\"claude-mem@claude-mem-local\"]\nenabled = true\n")
		},
		"settings line": func(t *testing.T, home string, o *RetireOptions) {
			mustWriteFile(t, filepath.Join(home, ".claude", "settings.json"), "{\n  \"enabledPlugins\": {\"claude-mem@thedotmack\": true}\n}\n")
		},
		"mcp entry": func(t *testing.T, home string, o *RetireOptions) {
			mustWriteJSON(t, filepath.Join(home, ".qwen", "settings.json"), map[string]any{"mcpServers": map[string]any{"claude-mem": map[string]any{}}})
		},
	}
	for name, residue := range cases {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			o := retireOpts(home, &recorder{})
			residue(t, home, &o)
			result, err := RetireClaudeMem(context.Background(), o)
			if err != nil {
				t.Fatal(err)
			}
			if result.Clean() {
				t.Fatalf("residue %q passed the read-back: %+v", name, result.ReadBack)
			}
		})
	}
}

func TestRetireApplyDoesNotMoveTheDataUnderALiveWorker(t *testing.T) {
	home := seedClaudeMemHome(t)
	seedImport(t, home)
	o := retireOpts(home, &recorder{})
	o.Apply, o.ImportConfirmed = true, true
	o.Ps = func(context.Context) (string, error) { return "bun /x/claude-mem/scripts/worker-service.cjs\n", nil }
	result, err := RetireClaudeMem(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if status := stepIDs(result)["archive"]; status != RetireFailed {
		t.Fatalf("archive step %q with a live worker, want failed", status)
	}
	if raw, err := os.ReadFile(filepath.Join(home, ".claude-mem", "claude-mem.db")); err != nil || string(raw) != "SQLITE-DATA" {
		t.Fatal("the data was moved under a live worker")
	}
	if result.Clean() {
		t.Fatal("a live worker must fail the read-back")
	}
}

func TestRetireNativeFailureIsReportedPerStepAndTheRestStillRuns(t *testing.T) {
	home := seedClaudeMemHome(t)
	seedImport(t, home)
	rec := &recorder{fail: map[string]bool{"claude plugin uninstall " + claudeMemPluginID: true}}
	o := retireOpts(home, rec)
	o.Apply, o.ImportConfirmed = true, true
	result, _ := RetireClaudeMem(context.Background(), o)
	got := stepIDs(result)
	if got["claude-plugin-uninstall"] != RetireFailed || got["codex-plugin"] != RetireDone || got["archive"] != RetireDone {
		t.Fatalf("per-step status wrong: %v", got)
	}
}

func TestRetireArchiveNameNeverCollides(t *testing.T) {
	home := seedClaudeMemHome(t)
	mustWriteFile(t, filepath.Join(home, ".claude-mem.archive-20261024", "older"), "keep")
	seedImport(t, home)
	o := retireOpts(home, &recorder{})
	o.Apply, o.ImportConfirmed = true, true
	result, _ := RetireClaudeMem(context.Background(), o)
	if result.Archive != filepath.Join(home, ".claude-mem.archive-20261024-2") {
		t.Fatalf("archive %q", result.Archive)
	}
	if raw, _ := os.ReadFile(filepath.Join(home, ".claude-mem.archive-20261024", "older")); string(raw) != "keep" {
		t.Fatal("an earlier archive was overwritten")
	}
}

func TestRetireNeedsAnAbsoluteHome(t *testing.T) {
	if _, err := RetireClaudeMem(context.Background(), RetireOptions{HomeDir: "relative"}); err == nil {
		t.Fatal("accepted a relative home")
	}
}

func TestRetireWorkerPortReadFromTheDataDir(t *testing.T) {
	home := t.TempDir()
	mustWriteFile(t, filepath.Join(home, ".claude-mem", "worker.port"), "41999\n")
	var asked []int
	o := retireOpts(home, &recorder{})
	o.PortOpen = func(p int) bool { asked = append(asked, p); return false }
	if _, err := RetireClaudeMem(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 || asked[0] != 41999 {
		t.Fatalf("checked ports %v, want [41999]", asked)
	}
}
