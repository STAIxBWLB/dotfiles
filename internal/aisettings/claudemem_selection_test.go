package aisettings

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMemorySelectedSubsetLeavesOtherIntegrationsUntouched(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "work")
	mustMkdirAll(t, workspace)
	plugin := marketplaceCheckoutDir(home)
	writeClaudeMemTree(t, plugin, true)
	qwen := filepath.Join(home, ".qwen", "projects", "project", "chats", "chat.jsonl")
	mustWriteFile(t, qwen, "{}\n")
	mustWriteJSON(t, filepath.Join(filepath.Dir(qwen), "chat.runtime.json"), map[string]any{"work_dir": workspace})
	kiro := filepath.Join(home, ".kiro", "sessions", "project", "sess_old")
	mustWriteJSON(t, filepath.Join(kiro, "session.json"), map[string]any{"workspacePaths": []string{workspace}})
	mustWriteFile(t, filepath.Join(kiro, "messages.jsonl"), "{}\n")
	oldKiro := filepath.Join(home, ".kiro", "settings", "mcp.json")
	mustWriteFile(t, oldKiro, `{"existing":"preserve"}`)
	m := NewClaudeMemManager(home, "/opt/bin/dot", "")
	m.PluginRoot = plugin
	m.SelectedAgents = []string{"qwen"}
	m.RunBunInstall = func(context.Context, string, string) ([]byte, error) {
		t.Fatal("preparation installed runtime")
		return nil, nil
	}
	result, err := m.Install(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.BridgePath != "" || result.WatchCount["qwen"] != 1 || result.WatchCount["kiro"] != 0 {
		t.Fatalf("%+v", result)
	}
	for _, path := range []string{m.KimiMCPPath(), m.CopilotMCPPath(), m.LaunchdPlistPath(), m.PiAgentsPath()} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("unexpected integration %s", path)
		}
	}
	raw, _ := os.ReadFile(oldKiro)
	if string(raw) != `{"existing":"preserve"}` {
		t.Fatal("foreign integration changed")
	}
	config, err := m.BuildTranscriptConfig()
	if err != nil || len(config.Schemas) != 1 || len(config.Watches) != 1 {
		t.Fatalf("%+v %v", config, err)
	}
}
func TestMemoryExplicitEmptyDoesNotCreateConfiguration(t *testing.T) {
	home := t.TempDir()
	m := NewClaudeMemManager(home, "", "")
	m.SelectedAgents = []string{}
	if _, err := m.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(home)
	if len(entries) != 0 {
		t.Fatalf("empty selection wrote files: %v", entries)
	}
	config, err := m.BuildTranscriptConfig()
	if err != nil || len(config.Schemas) != 0 || len(config.Watches) != 0 {
		t.Fatalf("%+v %v", config, err)
	}
}
func TestMemorySelectionRefreshClearsOnlyManagedWatches(t *testing.T) {
	home := t.TempDir()
	m := NewClaudeMemManager(home, "", "")
	m.SelectedAgents = []string{}
	mustWriteJSON(t, m.TranscriptConfigPath(), transcriptWatchConfig{Version: 1, Watches: []transcriptWatch{{Name: "kiro", Path: "/old"}}})
	if _, err := m.PrepareSelectedIntegration(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(m.TranscriptConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	var cfg transcriptWatchConfig
	if err := json.Unmarshal(data, &cfg); err != nil || len(cfg.Watches) != 0 {
		t.Fatalf("%s %v", data, err)
	}
	m.AgentSelection = func() ([]string, error) { return []string{"qwen"}, nil }
	cfg, err = m.BuildTranscriptConfig()
	if err != nil || len(cfg.Schemas) != 1 {
		t.Fatalf("%+v %v", cfg, err)
	}
	m.AgentSelection = func() ([]string, error) { return []string{}, nil }
	cfg, err = m.BuildTranscriptConfig()
	if err != nil || len(cfg.Schemas) != 0 {
		t.Fatalf("stale selection: %+v %v", cfg, err)
	}
}
func TestMemoryDisabledBridgeDoesNotStartWorker(t *testing.T) {
	home := t.TempDir()
	m := NewClaudeMemManager(home, "", "")
	m.SelectedAgents = []string{}
	m.BunPath = "/definitely/missing/bun"
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := m.RunBridge(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.DataDir()); !os.IsNotExist(err) {
		t.Fatal("disabled bridge created runtime tree")
	}
}
func TestMemoryKimiUsesOnlyAdoptedNativeCodeHome(t *testing.T) {
	home := t.TempDir()
	adopted := t.TempDir()
	workspace := filepath.Join(home, "work")
	mustMkdirAll(t, workspace)
	for _, root := range []string{adopted, filepath.Join(home, ".kimi"), filepath.Join(home, ".kimi-code")} {
		session := filepath.Join(root, "sessions", "work", "session_1")
		mustWriteJSON(t, filepath.Join(session, "state.json"), map[string]any{"cwd": workspace})
		mustWriteFile(t, filepath.Join(session, "agents", "main", "wire.jsonl"), "{}\n")
	}
	m := NewClaudeMemManager(home, "/opt/bin/dot", "")
	m.SelectedAgents = []string{"kimi"}
	m.KimiHome = adopted
	cfg, err := m.BuildTranscriptConfig()
	if err != nil || len(cfg.Watches) != 1 {
		t.Fatalf("%+v %v", cfg, err)
	}
	if cfg.Watches[0].Path != filepath.Join(adopted, "sessions", "work", "session_1", "agents", "main", "wire.jsonl") {
		t.Fatal("foreign or legacy Kimi profile watched")
	}
	if m.KimiMCPPath() != filepath.Join(adopted, "mcp.json") {
		t.Fatal("wrong Kimi MCP destination")
	}
}

func TestMemoryExplicitHomeDefersBeforeForeignBackendDiscovery(t *testing.T) {
	home := t.TempDir()
	foreign := t.TempDir()
	writeClaudeMemTree(t, foreign, true)
	t.Setenv("CLAUDE_PLUGIN_ROOT", foreign)
	t.Setenv("PLUGIN_ROOT", foreign)
	t.Setenv("CODEX_HOME", foreign)
	m := NewClaudeMemManager(home, "/opt/bin/dot", "")
	m.SelectedAgents = []string{"qwen"}
	m.ExplicitHome = true
	if _, err := m.PrepareSelectedIntegration(); err == nil {
		t.Fatal("alternate-home integration unexpectedly allowed")
	}
	entries, _ := os.ReadDir(home)
	if len(entries) != 0 {
		t.Fatalf("alternate home mutated: %v", entries)
	}
}
func TestSelectedRecallPinsHomeAndBackend(t *testing.T) {
	home := t.TempDir()
	plugin := marketplaceCheckoutDir(home)
	writeClaudeMemTree(t, plugin, true)
	m := NewClaudeMemManager(home, "/opt/bin/dot", "")
	m.SelectedAgents = []string{"qwen"}
	m.PluginRoot = plugin
	if _, err := m.PrepareSelectedIntegration(); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(m.QwenMCPPath())
	var doc struct {
		Servers map[string]struct {
			Args []string          `json:"args"`
			Env  map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	entry := doc.Servers["claude-mem"]
	if len(entry.Args) < 2 || entry.Args[0] != "--home" || entry.Args[1] != home || entry.Env["HOME"] != home || entry.Env["CLAUDE_PLUGIN_ROOT"] != plugin {
		t.Fatalf("unscoped recall: %+v", entry)
	}
}

// agy has no transcript the bridge reads, so selecting it wires MCP recall
// only, and keeps the other servers in its shared config.
func TestSelectedAntigravityGetsMCPRecall(t *testing.T) {
	home := t.TempDir()
	plugin := marketplaceCheckoutDir(home)
	writeClaudeMemTree(t, plugin, true)
	m := NewClaudeMemManager(home, "/opt/bin/dot", "")
	m.SelectedAgents = []string{"antigravity"}
	m.PluginRoot = plugin
	mustWriteFile(t, m.AntigravityMCPPath(), `{"mcpServers":{"other":{"command":"x"}}}`)
	if _, err := m.PrepareSelectedIntegration(); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(m.AntigravityMCPPath())
	var doc struct {
		Servers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || doc.Servers["claude-mem"] == nil || doc.Servers["other"] == nil {
		t.Fatalf("agy MCP config = %s (%v)", raw, err)
	}
	if !hasManagedMCPEntry(m.AntigravityMCPPath(), m.DotPath) {
		t.Fatal("status does not see the agy entry")
	}
	if _, err := os.Stat(m.QwenMCPPath()); !os.IsNotExist(err) {
		t.Fatal("unselected qwen was wired")
	}
}
