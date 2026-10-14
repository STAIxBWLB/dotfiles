package aisettings

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClaudeMemBuildTranscriptConfigUsesSessionWorkspaces(t *testing.T) {
	home := t.TempDir()
	kimiWorkspace := filepath.Join(home, "work", "kimi-project")
	kiroWorkspace := filepath.Join(home, "work", "kiro-project")
	mustMkdirAll(t, kimiWorkspace)
	mustMkdirAll(t, kiroWorkspace)

	kimiSession := filepath.Join(home, ".kimi-code", "sessions", "wd_test", "session_11111111-1111-1111-1111-111111111111")
	mustWriteJSON(t, filepath.Join(kimiSession, "state.json"), map[string]any{"workDir": kimiWorkspace})
	mustWriteFile(t, filepath.Join(kimiSession, "agents", "main", "wire.jsonl"), "{}\n")

	kiroSession := filepath.Join(home, ".kiro", "sessions", "workspace", "sess_22222222-2222-2222-2222-222222222222")
	mustWriteJSON(t, filepath.Join(kiroSession, "session.json"), map[string]any{"workspacePaths": []string{kiroWorkspace}})
	mustWriteFile(t, filepath.Join(kiroSession, "messages.jsonl"), "{}\n")

	mgr := NewClaudeMemManager(home, filepath.Join(home, "bin", "dot"), filepath.Join(home, "bin", "node"))
	config, err := mgr.BuildTranscriptConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Watches) != 2 {
		t.Fatalf("watches = %d, want 2: %+v", len(config.Watches), config.Watches)
	}
	counts := countWatches(config.Watches)
	if counts["kimi"] != 1 || counts["kiro"] != 1 {
		t.Fatalf("watch counts = %#v", counts)
	}
	byName := map[string]transcriptWatch{}
	for _, watch := range config.Watches {
		byName[watch.Name] = watch
		if watch.StartAtEnd {
			t.Fatalf("%s watch must replay a newly discovered session from offset zero", watch.Name)
		}
	}
	if byName["kimi"].Workspace != kimiWorkspace || byName["kiro"].Workspace != kiroWorkspace {
		t.Fatalf("workspace mapping wrong: %#v", byName)
	}

	raw, err := json.Marshal(config.Schemas)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"turn.prompt", "turn.ended", "turn.cancel", "event.toolCallId", "payload.toolCallId", "turn_end"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("schemas missing %q: %s", want, raw)
		}
	}
}

func TestClaudeMemBuildTranscriptConfigUsesKimiCWD(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "work", "kimi-current")
	mustMkdirAll(t, workspace)

	session := filepath.Join(home, ".kimi-code", "sessions", "wd_test", "session_33333333-3333-3333-3333-333333333333")
	mustWriteJSON(t, filepath.Join(session, "state.json"), map[string]any{"cwd": workspace, "workDir": filepath.Join(home, "work", "stale")})
	mustWriteFile(t, filepath.Join(session, "agents", "main", "wire.jsonl"), "{}\n")

	mgr := NewClaudeMemManager(home, filepath.Join(home, "bin", "dot"), filepath.Join(home, "bin", "node"))
	config, err := mgr.BuildTranscriptConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Watches) != 1 {
		t.Fatalf("watches = %d, want 1: %+v", len(config.Watches), config.Watches)
	}
	if config.Watches[0].Name != "kimi" || config.Watches[0].Workspace != workspace {
		t.Fatalf("Kimi cwd workspace mapping wrong: %+v", config.Watches[0])
	}
}

func TestClaudeMemMCPMergePreservesOtherServers(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".kimi-code", "mcp.json")
	mustWriteJSON(t, path, map[string]any{
		"mcpServers": map[string]any{
			"obsidian": map[string]any{"command": "mcpvault", "args": []string{"vault"}},
		},
		"custom": true,
	})
	dotPath := filepath.Join(home, ".local", "bin", "dot")
	changed, err := ensureMCPEntry(path, dotPath, mcpVariantStandard)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("first merge reported no change")
	}
	var got struct {
		Custom     bool `json:"custom"`
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if !readJSONFile(path, &got) {
		t.Fatal("merged MCP config did not parse")
	}
	if !got.Custom || got.MCPServers["obsidian"].Command != "mcpvault" {
		t.Fatalf("unrelated config was not preserved: %#v", got)
	}
	entry := got.MCPServers["claude-mem"]
	if entry.Command != dotPath || strings.Join(entry.Args, " ") != "ai memory mcp-server" {
		t.Fatalf("claude-mem entry = %#v", entry)
	}
	changed, err = ensureMCPEntry(path, dotPath, mcpVariantStandard)
	if err != nil || changed {
		t.Fatalf("second merge changed=%v err=%v, want idempotent", changed, err)
	}
}

func TestClaudeMemInstructionsAreIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	mustWriteFile(t, path, "# AI Agents\n\n## Existing\n\n- keep\n")
	changed, err := EnsureMemoryInstructions(path)
	if err != nil || !changed {
		t.Fatalf("first ensure changed=%v err=%v", changed, err)
	}
	changed, err = EnsureMemoryInstructions(path)
	if err != nil || changed {
		t.Fatalf("second ensure changed=%v err=%v", changed, err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Count(string(raw), memoryBlockStart) != 1 || !strings.Contains(string(raw), "## Existing") {
		t.Fatalf("managed block or existing content wrong:\n%s", raw)
	}
}

// writeClaudeMemTree writes a claude-mem plugin dir: always the three scripts,
// plus a populated node_modules when runnable.
func writeClaudeMemTree(t *testing.T, root string, runnable bool) {
	t.Helper()
	for _, name := range []string{"mcp-server.cjs", "transcript-watcher.cjs", "bun-runner.js"} {
		mustWriteFile(t, filepath.Join(root, "scripts", name), "")
	}
	if runnable {
		mustWriteFile(t, filepath.Join(root, "node_modules", "zod", "package.json"), "{}")
	}
}

func marketplaceCheckoutDir(home string) string {
	return filepath.Join(home, ".claude", "plugins", "marketplaces", "thedotmack", "plugin")
}

func codexCacheDir(home, version string) string {
	return filepath.Join(home, ".codex", "plugins", "cache", "claude-mem-local", "claude-mem", version)
}

func TestClaudeMemLocatePluginRequiresRunnableInstall(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(t *testing.T, home string)
		want    func(home string) string
		wantErr string
	}{
		{
			name: "broken checkout falls through to runnable codex cache",
			setup: func(t *testing.T, home string) {
				writeClaudeMemTree(t, marketplaceCheckoutDir(home), false)
				writeClaudeMemTree(t, codexCacheDir(home, "13.14.0"), true)
			},
			want: func(home string) string { return codexCacheDir(home, "13.14.0") },
		},
		{
			name: "runnable checkout still preferred",
			setup: func(t *testing.T, home string) {
				writeClaudeMemTree(t, marketplaceCheckoutDir(home), true)
				writeClaudeMemTree(t, codexCacheDir(home, "13.14.0"), true)
			},
			want: marketplaceCheckoutDir,
		},
		{
			name: "only broken candidates error with repair command",
			setup: func(t *testing.T, home string) {
				writeClaudeMemTree(t, marketplaceCheckoutDir(home), false)
			},
			wantErr: "codex plugin remove claude-mem",
		},
		{
			name: "empty node_modules is not runnable",
			setup: func(t *testing.T, home string) {
				writeClaudeMemTree(t, marketplaceCheckoutDir(home), false)
				mustMkdirAll(t, filepath.Join(marketplaceCheckoutDir(home), "node_modules"))
			},
			wantErr: "codex plugin remove claude-mem",
		},
		{
			name:    "no candidates",
			setup:   func(t *testing.T, home string) {},
			wantErr: "not found",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CLAUDE_PLUGIN_ROOT", "")
			t.Setenv("PLUGIN_ROOT", "")
			home := t.TempDir()
			tc.setup(t, home)
			mgr := NewClaudeMemManager(home, "/bin/dot", "/bin/node")
			got, err := mgr.LocatePlugin()
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if want := tc.want(home); got != want {
				t.Fatalf("plugin = %q, want %q", got, want)
			}
		})
	}
}

func claudeCacheDir(home, version string) string {
	return filepath.Join(home, ".claude", "plugins", "cache", "thedotmack", "claude-mem", version)
}

// writeInstalledRuntime writes a plugin dir the way current claude-mem releases
// install it: scripts and a populated node_modules, no .install-version marker.
func writeInstalledRuntime(t *testing.T, root string) {
	t.Helper()
	writeClaudeMemTree(t, root, true)
}

func TestClaudeMemLocatePluginFollowsClaudeInstallRecord(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(t *testing.T, home string)
		want    func(home string) string
		wantErr string
	}{
		{
			name: "recorded install path outranks a runnable cache",
			setup: func(t *testing.T, home string) {
				writeClaudeMemTree(t, marketplaceCheckoutDir(home), false)
				writeClaudeMemTree(t, claudeCacheDir(home, "13.24.30"), true)
				recorded := filepath.Join(home, "installs", "claude-mem")
				writeInstalledRuntime(t, recorded)
				mustWriteJSON(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"), map[string]any{
					"version": 2,
					"plugins": map[string]any{
						"claude-mem@thedotmack": []map[string]any{
							{"scope": "user", "installPath": recorded},
						},
					},
				})
			},
			want: func(home string) string { return filepath.Join(home, "installs", "claude-mem") },
		},
		{
			name: "orphaned cache is never returned",
			setup: func(t *testing.T, home string) {
				dir := claudeCacheDir(home, "13.24.0")
				writeClaudeMemTree(t, dir, true)
				mustWriteFile(t, filepath.Join(dir, ".orphaned_at"), "1788595203387")
			},
			wantErr: "not found",
		},
		{
			name: "recorded install path that is orphaned is never returned",
			setup: func(t *testing.T, home string) {
				recorded := claudeCacheDir(home, "13.24.23")
				writeInstalledRuntime(t, recorded)
				mustWriteFile(t, filepath.Join(recorded, ".orphaned_at"), "1788595203387")
				mustWriteJSON(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"), map[string]any{
					"version": 2,
					"plugins": map[string]any{
						"claude-mem@thedotmack": []map[string]any{
							{"scope": "user", "installPath": recorded},
						},
					},
				})
			},
			wantErr: "not found",
		},
		{
			name: "cache without install marker is runnable",
			setup: func(t *testing.T, home string) {
				writeInstalledRuntime(t, claudeCacheDir(home, "13.24.23"))
			},
			want: func(home string) string { return claudeCacheDir(home, "13.24.23") },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CLAUDE_PLUGIN_ROOT", "")
			t.Setenv("PLUGIN_ROOT", "")
			home := t.TempDir()
			tc.setup(t, home)
			got, err := NewClaudeMemManager(home, "/bin/dot", "/bin/node").LocatePlugin()
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if want := tc.want(home); got != want {
				t.Fatalf("plugin = %q, want %q", got, want)
			}
		})
	}
}

func TestCodexClaudeMemCache(t *testing.T) {
	t.Run("no cache", func(t *testing.T) {
		path, runnable := CodexClaudeMemCache(t.TempDir())
		if path != "" || runnable {
			t.Fatalf("got (%q, %v), want empty and false", path, runnable)
		}
	})
	t.Run("newest broken wins over older runnable", func(t *testing.T) {
		home := t.TempDir()
		older := codexCacheDir(home, "13.13.1")
		newer := codexCacheDir(home, "13.14.0")
		writeClaudeMemTree(t, older, true)
		writeClaudeMemTree(t, newer, false)
		past := time.Now().Add(-time.Hour)
		if err := os.Chtimes(older, past, past); err != nil {
			t.Fatal(err)
		}
		path, runnable := CodexClaudeMemCache(home)
		if path != newer || runnable {
			t.Fatalf("got (%q, %v), want (%q, false)", path, runnable, newer)
		}
	})
	t.Run("newest runnable", func(t *testing.T) {
		home := t.TempDir()
		writeClaudeMemTree(t, codexCacheDir(home, "13.14.0"), true)
		path, runnable := CodexClaudeMemCache(home)
		if path != codexCacheDir(home, "13.14.0") || !runnable {
			t.Fatalf("got (%q, %v), want runnable cache", path, runnable)
		}
	})
	t.Run("orphaned newest is skipped", func(t *testing.T) {
		home := t.TempDir()
		older := codexCacheDir(home, "13.13.1")
		newer := codexCacheDir(home, "13.14.0")
		writeClaudeMemTree(t, older, true)
		writeClaudeMemTree(t, newer, true)
		mustWriteFile(t, filepath.Join(newer, ".orphaned_at"), "1788595203387")
		past := time.Now().Add(-time.Hour)
		if err := os.Chtimes(older, past, past); err != nil {
			t.Fatal(err)
		}
		path, runnable := CodexClaudeMemCache(home)
		if path != older || !runnable {
			t.Fatalf("got (%q, %v), want (%q, true)", path, runnable, older)
		}
	})
}

func TestClaudeMemStatusFlagsBrokenCodexCache(t *testing.T) {
	t.Setenv("CLAUDE_PLUGIN_ROOT", "")
	t.Setenv("PLUGIN_ROOT", "")
	home := t.TempDir()
	checkout := marketplaceCheckoutDir(home)
	writeClaudeMemTree(t, checkout, true)
	mustWriteFile(t, filepath.Join(checkout, "hooks", "codex-hooks.json"), "{}")
	mustWriteFile(t, filepath.Join(home, ".codex", "config.toml"), "[plugins.\"claude-mem@claude-mem-local\"]\nenabled = true\n")
	writeClaudeMemTree(t, codexCacheDir(home, "13.14.0"), false)

	mgr := NewClaudeMemManager(home, "/bin/dot", "/bin/node")
	status := mgr.Status(context.Background(), filepath.Join(home, "AGENTS.md"))
	if status.CodexCacheRunnable {
		t.Fatal("broken codex cache reported runnable")
	}
	if status.CodexCachePath != codexCacheDir(home, "13.14.0") {
		t.Fatalf("cache path = %q", status.CodexCachePath)
	}
	if status.CodexNativeHooks {
		t.Fatal("codex row must go red when the codex plugin cache runtime is missing")
	}
}

// The plugin can be enabled in config.toml with no codex cache at all (deleted
// or never snapshotted); native hooks execute from that cache, so the codex
// row must not report ready on the strength of a healthy claude-side copy.
func TestClaudeMemStatusRequiresCodexCache(t *testing.T) {
	t.Setenv("CLAUDE_PLUGIN_ROOT", "")
	t.Setenv("PLUGIN_ROOT", "")
	home := t.TempDir()
	checkout := marketplaceCheckoutDir(home)
	writeClaudeMemTree(t, checkout, true)
	mustWriteFile(t, filepath.Join(checkout, "hooks", "codex-hooks.json"), "{}")
	mustWriteFile(t, filepath.Join(home, ".codex", "config.toml"), "[plugins.\"claude-mem@claude-mem-local\"]\nenabled = true\n")

	mgr := NewClaudeMemManager(home, "/bin/dot", "/bin/node")
	status := mgr.Status(context.Background(), filepath.Join(home, "AGENTS.md"))
	if status.CodexCachePath != "" {
		t.Fatalf("cache path = %q, want none", status.CodexCachePath)
	}
	if status.CodexNativeHooks {
		t.Fatal("codex row must not report ready without a codex plugin cache")
	}
}

func TestCodexClaudeMemEnabledAcceptsAnyMarketplaceAlias(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	mustWriteFile(t, path, `[plugins."claude-mem@thedotmack"]
enabled = false

[plugins."claude-mem@local"]
enabled = true

[plugins."other@local"]
enabled = false
`)
	if !codexClaudeMemEnabled(path) {
		t.Fatal("enabled local claude-mem plugin was not detected")
	}
	mustWriteFile(t, path, `[plugins."claude-mem@local"]
enabled = false
`)
	if codexClaudeMemEnabled(path) {
		t.Fatal("disabled claude-mem plugin reported enabled")
	}
}

func TestCopilotMCPEntryHasTypeAndTools(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".copilot", "mcp-config.json")
	mustWriteJSON(t, path, map[string]any{
		"mcpServers": map[string]any{
			"other": map[string]any{"command": "other-cmd", "args": []string{"arg1"}},
		},
	})
	dotPath := filepath.Join(home, ".local", "bin", "dot")
	changed, err := ensureMCPEntry(path, dotPath, mcpVariantCopilot)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("first merge reported no change")
	}
	var got struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
			Type    string   `json:"type"`
			Tools   []string `json:"tools"`
		} `json:"mcpServers"`
	}
	if !readJSONFile(path, &got) {
		t.Fatal("merged Copilot MCP config did not parse")
	}
	entry := got.MCPServers["claude-mem"]
	if entry.Command != dotPath {
		t.Fatalf("command = %q, want %q", entry.Command, dotPath)
	}
	if strings.Join(entry.Args, " ") != "ai memory mcp-server" {
		t.Fatalf("args = %v, want [ai memory mcp-server]", entry.Args)
	}
	if entry.Type != "local" {
		t.Fatalf("type = %q, want \"local\"", entry.Type)
	}
	if len(entry.Tools) != 1 || entry.Tools[0] != "*" {
		t.Fatalf("tools = %v, want [\"*\"]", entry.Tools)
	}
	// other entries must be preserved
	if got.MCPServers["other"].Command != "other-cmd" {
		t.Fatalf("unrelated entry was not preserved: %#v", got.MCPServers)
	}
	// idempotency
	changed, err = ensureMCPEntry(path, dotPath, mcpVariantCopilot)
	if err != nil || changed {
		t.Fatalf("second merge changed=%v err=%v, want idempotent", changed, err)
	}
}

func TestCopilotWatchesDiscoversSessionWorkspace(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "projects", "myrepo")
	mustMkdirAll(t, workspace)

	sessionDir := filepath.Join(home, ".copilot", "session-state", "sess_abc123")
	eventsPath := filepath.Join(sessionDir, "events.jsonl")
	startEvent := `{"type":"session.start","data":{"context":{"cwd":"` + workspace + `","gitRoot":"` + workspace + `"}}}`
	mustWriteFile(t, eventsPath, startEvent+"\n")

	mgr := NewClaudeMemManager(home, filepath.Join(home, "bin", "dot"), filepath.Join(home, "bin", "node"))
	config, err := mgr.BuildTranscriptConfig()
	if err != nil {
		t.Fatal(err)
	}
	counts := countWatches(config.Watches)
	if counts["copilot"] != 1 {
		t.Fatalf("copilot watch count = %d, want 1: %+v", counts["copilot"], config.Watches)
	}
	var found transcriptWatch
	for _, w := range config.Watches {
		if w.Name == "copilot" {
			found = w
		}
	}
	if found.Workspace != workspace {
		t.Fatalf("copilot watch workspace = %q, want %q", found.Workspace, workspace)
	}
	if found.Path != eventsPath {
		t.Fatalf("copilot watch path = %q, want %q", found.Path, eventsPath)
	}
	if found.StartAtEnd {
		t.Fatal("copilot watch must replay a newly discovered session from offset zero")
	}
}

func TestCopilotWatchesSkipsEventsWithoutSessionStart(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "projects", "myrepo")
	mustMkdirAll(t, workspace)

	// events.jsonl whose first line is NOT a session.start event
	sessionDir := filepath.Join(home, ".copilot", "session-state", "sess_nostart")
	eventsPath := filepath.Join(sessionDir, "events.jsonl")
	mustWriteFile(t, eventsPath, `{"type":"user","data":{"message":"hello"}}`+"\n")

	mgr := NewClaudeMemManager(home, filepath.Join(home, "bin", "dot"), filepath.Join(home, "bin", "node"))
	config, err := mgr.BuildTranscriptConfig()
	if err != nil {
		t.Fatal(err)
	}
	counts := countWatches(config.Watches)
	if counts["copilot"] != 0 {
		t.Fatalf("copilot watch count = %d, want 0 for missing session.start", counts["copilot"])
	}
}

func TestCopilotTranscriptSchemaFields(t *testing.T) {
	schema := copilotTranscriptSchema()
	raw, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"user.message", "assistant.message", "tool.execution_start", "tool.execution_complete", "session.shutdown"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("copilot schema missing %q: %s", want, raw)
		}
	}
	if schema.Name != "copilot" {
		t.Fatalf("schema.Name = %q, want \"copilot\"", schema.Name)
	}
	// user-message should map to session_init (matching kimi/kiro pattern)
	found := false
	for _, ev := range schema.Events {
		if ev.Name == "user-message" && ev.Action == "session_init" {
			found = true
		}
	}
	if !found {
		t.Fatal("copilot schema: user-message event must use session_init action")
	}
}

func TestBuildTranscriptConfigIncludesCopilotInCounts(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "projects", "repo")
	mustMkdirAll(t, workspace)

	sessionDir := filepath.Join(home, ".copilot", "session-state", "sess_xyz")
	eventsPath := filepath.Join(sessionDir, "events.jsonl")
	startEvent := `{"type":"session.start","data":{"context":{"cwd":"` + workspace + `"}}}`
	mustWriteFile(t, eventsPath, startEvent+"\n")

	mgr := NewClaudeMemManager(home, filepath.Join(home, "bin", "dot"), filepath.Join(home, "bin", "node"))
	cfg, err := mgr.BuildTranscriptConfig()
	if err != nil {
		t.Fatal(err)
	}
	counts := countWatches(cfg.Watches)
	if _, ok := counts["copilot"]; !ok {
		t.Fatal("countWatches result is missing the \"copilot\" key")
	}
	if counts["copilot"] != 1 {
		t.Fatalf("copilot count = %d, want 1", counts["copilot"])
	}
	// Schemas map must contain "copilot"
	if _, ok := cfg.Schemas["copilot"]; !ok {
		t.Fatal("BuildTranscriptConfig schemas missing \"copilot\"")
	}
}

func TestQwenTranscriptSchemaFields(t *testing.T) {
	schema := qwenTranscriptSchema()
	raw, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"provenance", "real_user", "message.parts[0].text", "tool_result", "functionResponse", "assistant_message"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("qwen schema missing %q: %s", want, raw)
		}
	}
	if schema.Name != "qwen" {
		t.Fatalf("schema.Name = %q, want \"qwen\"", schema.Name)
	}
	// user-prompt should map to session_init (matching kimi/kiro/copilot pattern)
	found := false
	for _, ev := range schema.Events {
		if ev.Name == "user-prompt" && ev.Action == "session_init" {
			found = true
		}
	}
	if !found {
		t.Fatal("qwen schema: user-prompt event must use session_init action")
	}
}

func TestQwenWatchesDiscoversSessionWorkspace(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "work", "qwen-project")
	mustMkdirAll(t, workspace)

	chatPath := filepath.Join(home, ".qwen", "projects", "-Users-yj-lee-work-qwen-project", "chats", "44444444-4444-4444-4444-444444444444.jsonl")
	mustWriteJSON(t, strings.TrimSuffix(chatPath, ".jsonl")+".runtime.json", map[string]any{"work_dir": workspace})
	mustWriteFile(t, chatPath, `{"type":"user","provenance":"real_user","message":{"parts":[{"text":"hi"}]}}`+"\n")

	mgr := NewClaudeMemManager(home, filepath.Join(home, "bin", "dot"), filepath.Join(home, "bin", "node"))
	config, err := mgr.BuildTranscriptConfig()
	if err != nil {
		t.Fatal(err)
	}
	counts := countWatches(config.Watches)
	if counts["qwen"] != 1 {
		t.Fatalf("qwen watch count = %d, want 1: %+v", counts["qwen"], config.Watches)
	}
	var found transcriptWatch
	for _, w := range config.Watches {
		if w.Name == "qwen" {
			found = w
		}
	}
	if found.Workspace != workspace {
		t.Fatalf("qwen watch workspace = %q, want %q from the sibling runtime.json", found.Workspace, workspace)
	}
	if found.Path != chatPath {
		t.Fatalf("qwen watch path = %q, want %q", found.Path, chatPath)
	}
	if found.Schema != "qwen" {
		t.Fatalf("qwen watch schema = %q, want \"qwen\"", found.Schema)
	}
	if found.StartAtEnd {
		t.Fatal("qwen watch must replay a newly discovered session from offset zero")
	}
	if _, ok := config.Schemas["qwen"]; !ok {
		t.Fatal("BuildTranscriptConfig schemas missing \"qwen\"")
	}
}

func TestQwenWatchesFallsBackToFirstLineCWD(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "work", "qwen-current")
	mustMkdirAll(t, workspace)

	// No sibling runtime.json: the first chat line's top-level cwd wins.
	chatPath := filepath.Join(home, ".qwen", "projects", "-Users-yj-lee-work-qwen-current", "chats", "55555555-5555-5555-5555-555555555555.jsonl")
	mustWriteFile(t, chatPath, `{"cwd":"`+workspace+`","type":"user","provenance":"real_user","message":{"parts":[{"text":"hi"}]}}`+"\n")

	mgr := NewClaudeMemManager(home, filepath.Join(home, "bin", "dot"), filepath.Join(home, "bin", "node"))
	config, err := mgr.BuildTranscriptConfig()
	if err != nil {
		t.Fatal(err)
	}
	if counts := countWatches(config.Watches); counts["qwen"] != 1 {
		t.Fatalf("qwen watch count = %d, want 1: %+v", counts["qwen"], config.Watches)
	}
	if config.Watches[0].Name != "qwen" || config.Watches[0].Workspace != workspace {
		t.Fatalf("qwen first-line cwd workspace mapping wrong: %+v", config.Watches[0])
	}
}

func TestQwenWatchesSkipsChatsWithoutWorkspace(t *testing.T) {
	home := t.TempDir()

	// Empty transcript with no runtime.json: no cwd to resolve anywhere.
	chatPath := filepath.Join(home, ".qwen", "projects", "-tmp-empty", "chats", "66666666-6666-6666-6666-666666666666.jsonl")
	mustWriteFile(t, chatPath, "")

	mgr := NewClaudeMemManager(home, filepath.Join(home, "bin", "dot"), filepath.Join(home, "bin", "node"))
	config, err := mgr.BuildTranscriptConfig()
	if err != nil {
		t.Fatal(err)
	}
	if counts := countWatches(config.Watches); counts["qwen"] != 0 {
		t.Fatalf("qwen watch count = %d, want 0 for a chat with no resolvable workspace", counts["qwen"])
	}
}

func TestPiTranscriptSchemaFields(t *testing.T) {
	schema := piTranscriptSchema()
	raw, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"message.role", "message.content[0].text", "message.toolName", "tool_result", "assistant_message"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("pi schema missing %q: %s", want, raw)
		}
	}
	if schema.Name != "pi" {
		t.Fatalf("schema.Name = %q, want \"pi\"", schema.Name)
	}
	// user-prompt should map to session_init (matching kimi/kiro/copilot/qwen pattern)
	found := false
	for _, ev := range schema.Events {
		if ev.Name == "user-prompt" && ev.Action == "session_init" {
			found = true
		}
	}
	if !found {
		t.Fatal("pi schema: user-prompt event must use session_init action")
	}
	// The coalesce must walk content slots in ascending order: that is what
	// makes it "the first text block" rather than "whatever sits at a fixed
	// index". A thinking block keys its prose under "thinking" and a toolCall
	// block has no text, so an earlier slot resolves only when it really is
	// text. Reversing this order would store a thinking block as the reply.
	var assistant transcriptEvent
	for _, ev := range schema.Events {
		if ev.Action == "assistant_message" {
			assistant = ev
		}
	}
	selector, ok := assistant.Fields["message"].(map[string]any)
	if !ok {
		t.Fatalf("pi assistant message field is %T, want a coalesce selector", assistant.Fields["message"])
	}
	paths, ok := selector["coalesce"].([]any)
	if !ok || len(paths) < 3 {
		t.Fatalf("pi assistant coalesce = %v, want at least three content slots", selector["coalesce"])
	}
	for i, path := range paths {
		want := fmt.Sprintf("message.content[%d].text", i)
		if path != want {
			t.Fatalf("pi assistant coalesce[%d] = %v, want %q in ascending slot order", i, path, want)
		}
	}
}

func TestPiWatchesDiscoversSessionWorkspace(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "work", "pi-project")
	mustMkdirAll(t, workspace)

	// pi has no sidecar file: the first line's session header carries cwd.
	sessionPath := filepath.Join(home, ".pi", "agent", "sessions", "--tmp-pi-project--", "1770000000000-77777777-7777-7777-7777-777777777777.jsonl")
	mustWriteFile(t, sessionPath, `{"type":"session","version":3,"id":"77777777-7777-7777-7777-777777777777","timestamp":"2026-09-20T00:00:00.000Z","cwd":"`+workspace+`"}`+"\n"+`{"type":"message","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`+"\n")

	mgr := NewClaudeMemManager(home, filepath.Join(home, "bin", "dot"), filepath.Join(home, "bin", "node"))
	config, err := mgr.BuildTranscriptConfig()
	if err != nil {
		t.Fatal(err)
	}
	counts := countWatches(config.Watches)
	if counts["pi"] != 1 {
		t.Fatalf("pi watch count = %d, want 1: %+v", counts["pi"], config.Watches)
	}
	var found transcriptWatch
	for _, w := range config.Watches {
		if w.Name == "pi" {
			found = w
		}
	}
	if found.Workspace != workspace {
		t.Fatalf("pi watch workspace = %q, want %q from the session header", found.Workspace, workspace)
	}
	if found.Path != sessionPath {
		t.Fatalf("pi watch path = %q, want %q", found.Path, sessionPath)
	}
	if found.Schema != "pi" {
		t.Fatalf("pi watch schema = %q, want \"pi\"", found.Schema)
	}
	if found.StartAtEnd {
		t.Fatal("pi watch must replay a newly discovered session from offset zero")
	}
	if _, ok := config.Schemas["pi"]; !ok {
		t.Fatal("BuildTranscriptConfig schemas missing \"pi\"")
	}
}

func TestPiWatchesSkipsSessionsWithoutWorkspace(t *testing.T) {
	home := t.TempDir()

	// Session header missing or without cwd: no workspace to resolve.
	sessionPath := filepath.Join(home, ".pi", "agent", "sessions", "--tmp-empty--", "1770000001000-88888888-8888-8888-8888-888888888888.jsonl")
	mustWriteFile(t, sessionPath, `{"type":"message","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`+"\n")

	mgr := NewClaudeMemManager(home, filepath.Join(home, "bin", "dot"), filepath.Join(home, "bin", "node"))
	config, err := mgr.BuildTranscriptConfig()
	if err != nil {
		t.Fatal(err)
	}
	if counts := countWatches(config.Watches); counts["pi"] != 0 {
		t.Fatalf("pi watch count = %d, want 0 for a session with no resolvable workspace", counts["pi"])
	}
}

func TestPiSessionWorkspaceHandlesLineEndings(t *testing.T) {
	workspace := "/tmp/work/pi-crlf"
	for name, firstLine := range map[string]string{
		"crlf":       `{"type":"session","version":3,"cwd":"` + workspace + `"}` + "\r\n",
		"no-newline": `{"type":"session","version":3,"cwd":"` + workspace + `"}`,
		"empty":      "",
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			sessionPath := filepath.Join(home, ".pi", "agent", "sessions", "--tmp--", "1770000002000-99999999-9999-9999-9999-999999999999.jsonl")
			mustWriteFile(t, sessionPath, firstLine)
			got := piSessionWorkspace(sessionPath)
			if name != "empty" && got != workspace {
				t.Fatalf("piSessionWorkspace = %q, want %q (first line ending: %s)", got, workspace, name)
			}
			if name == "empty" && got != "" {
				t.Fatalf("piSessionWorkspace on an empty file = %q, want empty", got)
			}
		})
	}
}

func TestQwenMCPEntryPreservesQwenSettings(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".qwen", "settings.json")
	mustWriteJSON(t, path, map[string]any{
		"$version": 4,
		"model":    "qwen3-coder-plus",
		"modelProviders": []map[string]any{
			{"name": "modelstudio", "baseURL": "https://dashscope.example/compatible-mode/v1", "apiKey": "sk-test"},
		},
		"security": map[string]any{"auth": map[string]any{"selectedType": "qwen-oauth"}},
		"ui":       map[string]any{"hideWindowTitle": true},
		"mcpServers": map[string]any{
			"obsidian": map[string]any{"command": "mcpvault", "args": []string{"obsidian"}},
		},
	})
	dotPath := filepath.Join(home, ".local", "bin", "dot")
	changed, err := ensureMCPEntry(path, dotPath, mcpVariantStandard)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("first merge reported no change")
	}
	var got struct {
		Version        int    `json:"$version"`
		Model          string `json:"model"`
		ModelProviders []any  `json:"modelProviders"`
		Security       struct {
			Auth struct {
				SelectedType string `json:"selectedType"`
			} `json:"auth"`
		} `json:"security"`
		UI         map[string]any `json:"ui"`
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if !readJSONFile(path, &got) {
		t.Fatal("merged Qwen settings did not parse")
	}
	entry := got.MCPServers["claude-mem"]
	if entry.Command != dotPath {
		t.Fatalf("command = %q, want %q", entry.Command, dotPath)
	}
	if strings.Join(entry.Args, " ") != "ai memory mcp-server" {
		t.Fatalf("args = %v, want [ai memory mcp-server]", entry.Args)
	}
	// Qwen's own model/auth settings must survive the merge untouched.
	if got.Version != 4 || got.Model != "qwen3-coder-plus" || got.Security.Auth.SelectedType != "qwen-oauth" {
		t.Fatalf("qwen settings were not preserved: %#v", got)
	}
	if len(got.ModelProviders) != 1 || got.UI["hideWindowTitle"] != true {
		t.Fatalf("qwen settings were not preserved: %#v", got)
	}
	if got.MCPServers["obsidian"].Command != "mcpvault" {
		t.Fatalf("unrelated entry was not preserved: %#v", got.MCPServers)
	}
	// idempotency
	changed, err = ensureMCPEntry(path, dotPath, mcpVariantStandard)
	if err != nil || changed {
		t.Fatalf("second merge changed=%v err=%v, want idempotent", changed, err)
	}
}

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
	mustMkdirAll(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestTranscriptConfigWatchesIsArrayWhenEmpty pins the JSON shape, not the Go
// value: a nil slice marshals to `null`, and the plugin's watcher rejects that
// as an invalid config. A machine with no Kimi/Kiro sessions is exactly the
// fresh-install case, so this failed only where it mattered most.
func TestTranscriptConfigWatchesIsArrayWhenEmpty(t *testing.T) {
	m := &ClaudeMemManager{HomeDir: t.TempDir()} // no kimi/kiro sessions
	cfg, err := m.BuildTranscriptConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Watches) != 0 {
		t.Fatalf("expected no watches, got %d", len(cfg.Watches))
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(`"watches":null`)) {
		t.Errorf("watches serialized as null; the plugin rejects that config:\n%s", raw)
	}
	if !bytes.Contains(raw, []byte(`"watches":[]`)) {
		t.Errorf("watches is not an empty array:\n%s", raw)
	}
}

// CODEX_HOME from the launching shell (Orca sets one) must not leak into the
// temp homes these tests build.
func TestMain(m *testing.M) {
	os.Unsetenv("CODEX_HOME")
	os.Exit(m.Run())
}

func TestEnsureCodexCacheRuntimeInstallsMissingNodeModules(t *testing.T) {
	home := t.TempDir()
	cache := codexCacheDir(home, "13.25.1")
	writeClaudeMemTree(t, cache, false)
	mustMkdirAll(t, filepath.Join(cache, "node_modules")) // interrupted install: empty dir

	mgr := NewClaudeMemManager(home, "/bin/dot", "/bin/node")
	mgr.BunPath = "/bin/bun"
	var gotBun, gotDir string
	mgr.RunBunInstall = func(ctx context.Context, bunPath, dir string) ([]byte, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("bun install ran without a deadline")
		}
		gotBun, gotDir = bunPath, dir
		mustWriteFile(t, filepath.Join(dir, "node_modules", "zod", "package.json"), "{}")
		return []byte("37 packages installed"), nil
	}

	repaired, err := mgr.EnsureCodexCacheRuntime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if repaired != cache {
		t.Fatalf("repaired = %q, want %q", repaired, cache)
	}
	if gotBun != "/bin/bun" || gotDir != cache {
		t.Fatalf("bun install ran as (%q in %q)", gotBun, gotDir)
	}
	if _, runnable := CodexClaudeMemCache(home); !runnable {
		t.Fatal("cache still reported broken after repair")
	}

	// Idempotent: a runnable cache never triggers another install.
	mgr.RunBunInstall = func(context.Context, string, string) ([]byte, error) {
		t.Fatal("bun install ran on a runnable cache")
		return nil, nil
	}
	if repaired, err := mgr.EnsureCodexCacheRuntime(context.Background()); err != nil || repaired != "" {
		t.Fatalf("second run: repaired=%q err=%v", repaired, err)
	}
}

func TestEnsureCodexCacheRuntimeReportsInstallThatLeavesCacheBroken(t *testing.T) {
	home := t.TempDir()
	cache := codexCacheDir(home, "13.25.1")
	writeClaudeMemTree(t, cache, false)
	mgr := NewClaudeMemManager(home, "/bin/dot", "/bin/node")
	mgr.BunPath = "/bin/bun"
	mgr.RunBunInstall = func(context.Context, string, string) ([]byte, error) { return []byte("ok"), nil }

	repaired, err := mgr.EnsureCodexCacheRuntime(context.Background())
	if repaired != "" || err == nil || !strings.Contains(err.Error(), "left node_modules empty") {
		t.Fatalf("repaired=%q err=%v", repaired, err)
	}

	// A repair needs an absolute bun; without one the caller gets an error, not a silent skip.
	mgr.BunPath = ""
	if _, err := mgr.EnsureCodexCacheRuntime(context.Background()); err == nil || !strings.Contains(err.Error(), "bun executable path") {
		t.Fatalf("missing bun path: err=%v", err)
	}

	// No cache at all is not an error: codex creates it on `plugin add`.
	if repaired, err := NewClaudeMemManager(t.TempDir(), "/bin/dot", "/bin/node").EnsureCodexCacheRuntime(context.Background()); err != nil || repaired != "" {
		t.Fatalf("no cache: repaired=%q err=%v", repaired, err)
	}
}

func TestCodexHomeFollowsCodexHomeEnv(t *testing.T) {
	home := t.TempDir()
	mgr := NewClaudeMemManager(home, "/bin/dot", "/bin/node")

	// Unset, ~/.codex spelled out, and ~ shorthand all mean the default home.
	for _, value := range []string{"", filepath.Join(home, ".codex"), "~/.codex"} {
		t.Setenv("CODEX_HOME", value)
		if got := mgr.CodexHomeOverride(); got != "" {
			t.Fatalf("CODEX_HOME=%q reported as override: %q", value, got)
		}
	}
	// A symlink to ~/.codex is still the default home.
	mustMkdirAll(t, filepath.Join(home, ".codex"))
	link := filepath.Join(home, "codex-link")
	if err := os.Symlink(filepath.Join(home, ".codex"), link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", link)
	if got := mgr.CodexHomeOverride(); got != "" {
		t.Fatalf("symlinked default home reported as override: %q", got)
	}

	// A foreign home is inspected AND reported: the cache lookup, the config
	// check and the status row all follow it.
	foreign := filepath.Join(home, "orca", "codex-home")
	cache := filepath.Join(foreign, "plugins", "cache", "claude-mem-local", "claude-mem", "13.25.1")
	writeClaudeMemTree(t, cache, false)
	t.Setenv("CODEX_HOME", foreign)
	want := canonicalPath(foreign)
	if got := mgr.CodexHomeOverride(); got != want {
		t.Fatalf("override = %q, want %q", got, want)
	}
	status := mgr.Status(context.Background(), filepath.Join(home, "AGENTS.md"))
	if status.CodexHome != want || status.CodexCachePath != canonicalPath(cache) || status.CodexCacheRunnable {
		t.Fatalf("status = home %q cache %q runnable %v", status.CodexHome, status.CodexCachePath, status.CodexCacheRunnable)
	}
	if _, runnable := CodexClaudeMemCache(home); runnable {
		t.Fatal("foreign broken cache reported runnable")
	}
}

func TestMemoryMCPStatusExecutableAliases(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "dot")
	alias := filepath.Join(dir, "dot-alias")
	other := filepath.Join(dir, "other-dot")
	broken := filepath.Join(dir, "broken-dot")
	mustWriteFile(t, real, "dot")
	mustWriteFile(t, other, "dot")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "missing"), broken); err != nil {
		t.Fatal(err)
	}
	for _, variant := range []mcpEntryVariant{mcpVariantStandard, mcpVariantKiro, mcpVariantCopilot} {
		for _, tc := range []struct {
			name, command, executable string
			want                      bool
		}{
			{"same", real, real, true},
			{"configured alias", alias, real, true},
			{"invoked alias", real, alias, true},
			{"different file", other, real, false},
			{"broken configured alias", broken, real, false},
			{"broken invoked alias", real, broken, false},
		} {
			t.Run(fmt.Sprintf("%d/%s", variant, tc.name), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "mcp.json")
				if _, err := ensureMCPEntry(path, tc.command, variant); err != nil {
					t.Fatal(err)
				}
				before, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				check := hasManagedMCPEntry
				if variant == mcpVariantCopilot {
					check = hasManagedCopilotMCPEntry
				}
				if got := check(path, tc.executable); got != tc.want {
					t.Fatalf("ready = %v, want %v", got, tc.want)
				}
				after, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(before, after) {
					t.Fatal("status changed MCP configuration")
				}
			})
		}
	}
}

func TestMemoryMCPStatusAliasesPreserveContract(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "dot")
	alias := filepath.Join(dir, "dot-alias")
	mustWriteFile(t, real, "dot")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		args    []string
		kind    string
		tools   []string
		copilot bool
	}{
		{"wrong args", []string{"ai", "memory", "other"}, "local", []string{"*"}, false},
		{"wrong copilot args", []string{"ai", "memory", "other"}, "local", []string{"*"}, true},
		{"wrong copilot type", []string{"ai", "memory", "mcp-server"}, "stdio", []string{"*"}, true},
		{"wrong copilot tools", []string{"ai", "memory", "mcp-server"}, "local", []string{"other"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "mcp.json")
			mustWriteJSON(t, path, map[string]any{"mcpServers": map[string]any{"claude-mem": map[string]any{"command": alias, "args": tc.args, "type": tc.kind, "tools": tc.tools}}})
			check := hasManagedMCPEntry
			if tc.copilot {
				check = hasManagedCopilotMCPEntry
			}
			if check(path, real) {
				t.Fatal("invalid MCP contract reported ready")
			}
		})
	}
}

func TestMemoryMCPStatusDisabledAdapter(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "dot")
	alias := filepath.Join(dir, "dot-alias")
	mustWriteFile(t, real, "dot")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{real, alias} {
		for _, disabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/disabled=%v", filepath.Base(command), disabled), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "mcp.json")
				mustWriteJSON(t, path, map[string]any{"mcpServers": map[string]any{"claude-mem": map[string]any{"command": command, "args": []string{"ai", "memory", "mcp-server"}, "disabled": disabled}}})
				before, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if got := hasManagedMCPEntry(path, real); got == disabled {
					t.Fatalf("ready=%v, disabled=%v", got, disabled)
				}
				after, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(before, after) {
					t.Fatal("status changed disabled setting")
				}
			})
		}
	}
}
