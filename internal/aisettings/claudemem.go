package aisettings

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	ClaudeMemLaunchdLabel = "com.dotfiles.claude-mem-bridge"
	memoryBlockStart      = "<!-- dotfiles:claude-mem:start -->"
	memoryBlockEnd        = "<!-- dotfiles:claude-mem:end -->"
)

const memoryInstructions = `<!-- dotfiles:claude-mem:start -->
## Persistent Memory

- Before non-trivial work, query the ` + "`claude-mem`" + ` MCP server when prior workspace context may affect the result. A CLI without that server (pi has no MCP support) works from the workspace itself; its sessions still reach the store through the transcript bridge.
- Treat retrieved memory as a lead, and verify drift-prone facts against the current workspace or live system.
- Codex hooks and the Kimi/Kiro/Copilot/Qwen/pi transcript bridge capture session activity automatically. Do not duplicate it into separate memory files unless explicitly requested.
<!-- dotfiles:claude-mem:end -->`

// ClaudeMemManager manages the cross-CLI claude-mem integration. Codex uses
// the plugin's native hooks; Kimi, Kiro, GitHub Copilot CLI, Qwen Code, and pi
// use MCP for recall and a transcript bridge for capture — except pi, which
// has no MCP support by design and joins the transcript bridge only.
type ClaudeMemManager struct {
	HomeDir    string
	DotPath    string
	NodePath   string
	BunPath    string
	PluginRoot string
	// SelectedAgents nil retains the legacy integration. A non-nil empty
	// selection disables new MCP writes and transcript capture.
	SelectedAgents []string
	// ExplicitHome forbids integrating a different live user/profile domain.
	ExplicitHome bool
	// AgentSelection refreshes configured selection before every bridge scan.
	AgentSelection func() ([]string, error)
	// KimiHome is an explicitly adopted native Kimi Code home, never ~/.kimi.
	KimiHome string
	// RunBunInstall runs `bun install` in dir; nil uses the real bun. Test seam.
	RunBunInstall func(ctx context.Context, bunPath, dir string) ([]byte, error)
}

type ClaudeMemInstallResult struct {
	PluginRoot  string
	ConfigPaths []string
	BridgePath  string
	WatchCount  map[string]int
	// CodexCachePath is the codex plugin cache whose runtime was installed
	// during this run; empty when nothing needed repair.
	CodexCachePath string
	// CodexCacheError is a non-fatal repair failure: the rest of the install
	// (MCP wiring, transcript bridge) still completed.
	CodexCacheError string
}

type ClaudeMemStatus struct {
	PluginRoot         string
	PluginVersion      string
	PluginError        string
	CodexNativeHooks   bool
	CodexCachePath     string
	CodexCacheRunnable bool
	// CodexHome is CODEX_HOME when it differs from ~/.codex (see CodexHomeOverride).
	CodexHome string
	KimiMCP   bool
	QwenMCP   bool
	// PiAgents reports pi's fan-out instructions target, which is all dot
	// installs on pi's side: pi has no MCP entry to check.
	PiAgents            bool
	KiroMCP             bool
	CopilotMCP          bool
	InstructionsEnabled bool
	BridgeInstalled     bool
	BridgeRunning       bool
	WatchCount          map[string]int
}

type transcriptWatchConfig struct {
	Version   int                         `json:"version"`
	Schemas   map[string]transcriptSchema `json:"schemas"`
	Watches   []transcriptWatch           `json:"watches"`
	StateFile string                      `json:"stateFile"`
}

type transcriptSchema struct {
	Name        string            `json:"name"`
	Version     string            `json:"version,omitempty"`
	Description string            `json:"description,omitempty"`
	Events      []transcriptEvent `json:"events"`
}

type transcriptEvent struct {
	Name   string         `json:"name"`
	Match  map[string]any `json:"match,omitempty"`
	Action string         `json:"action"`
	Fields map[string]any `json:"fields,omitempty"`
}

type transcriptWatch struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Schema     string `json:"schema"`
	Workspace  string `json:"workspace"`
	StartAtEnd bool   `json:"startAtEnd"`
}

func NewClaudeMemManager(homeDir, dotPath, nodePath string) *ClaudeMemManager {
	return &ClaudeMemManager{HomeDir: homeDir, DotPath: dotPath, NodePath: nodePath}
}

func (m *ClaudeMemManager) DataDir() string {
	return filepath.Join(m.HomeDir, ".claude-mem")
}

func (m *ClaudeMemManager) TranscriptConfigPath() string {
	return filepath.Join(m.DataDir(), "cross-cli-transcript-watch.json")
}

func (m *ClaudeMemManager) TranscriptStatePath() string {
	return filepath.Join(m.DataDir(), "cross-cli-transcript-watch-state.json")
}

func (m *ClaudeMemManager) LaunchdPlistPath() string {
	return filepath.Join(m.HomeDir, "Library", "LaunchAgents", ClaudeMemLaunchdLabel+".plist")
}

func (m *ClaudeMemManager) BridgeLogPath() string {
	return filepath.Join(m.DataDir(), "logs", "cross-cli-bridge.log")
}

func (m *ClaudeMemManager) KimiMCPPath() string {
	return filepath.Join(m.kimiHomeDir(), "mcp.json")
}

func (m *ClaudeMemManager) QwenMCPPath() string {
	return filepath.Join(m.HomeDir, ".qwen", "settings.json")
}

// PiAgentsPath is pi's global instructions target. pi takes no MCP entry, so
// this file is the only thing dot writes into pi's agent home.
func (m *ClaudeMemManager) PiAgentsPath() string {
	return filepath.Join(m.HomeDir, ".pi", "agent", "AGENTS.md")
}

func (m *ClaudeMemManager) KiroMCPPath() string {
	return filepath.Join(m.HomeDir, ".kiro", "settings", "mcp.json")
}

func (m *ClaudeMemManager) CopilotMCPPath() string {
	return filepath.Join(m.HomeDir, ".copilot", "mcp-config.json")
}

// ClaudeMemRepairCommand repairs the codex claude-mem plugin cache; shared
// with the CLI so every surface prints the same repair procedure. Install
// materializes the cache runtime itself (EnsureCodexCacheRuntime), because
// `codex plugin add` only snapshots the marketplace checkout, which has no
// node_modules.
const ClaudeMemRepairCommand = "dot ai memory install"

// claudeMemReinstallCommand recreates the codex plugin cache from scratch. codex
// 0.154 rejects the bare `plugin remove <name>` form, so the marketplace is
// always spelled out.
const claudeMemReinstallCommand = "codex plugin remove claude-mem --marketplace claude-mem-local && codex plugin add claude-mem@claude-mem-local"

const claudeMemRepairHint = "repair: " + ClaudeMemRepairCommand + " (or: " + claudeMemReinstallCommand + ", then " + ClaudeMemRepairCommand + ")"

// codexCacheInstallTimeout bounds the dependency install so a stalled registry
// fetch surfaces as an error instead of a silent hang.
const codexCacheInstallTimeout = 5 * time.Minute

// CodexHomeDir is the codex home dot inspects and repairs: CODEX_HOME when the
// shell sets it (Orca points it at a per-account home), with a leading ~
// expanded and symlinks resolved; otherwise ~/.codex.
func CodexHomeDir(homeDir string) string {
	raw := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if raw == "" {
		return filepath.Join(homeDir, ".codex")
	}
	if raw == "~" || strings.HasPrefix(raw, "~/") {
		raw = filepath.Join(homeDir, strings.TrimPrefix(raw, "~"))
	}
	return canonicalPath(raw)
}

func canonicalPath(path string) string {
	path = filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

// CodexHomeOverride returns the codex home dot inspected when CODEX_HOME moved
// it away from ~/.codex, so status can say which home the codex row describes.
// Empty when CODEX_HOME is unset or names ~/.codex (also via ~ or a symlink).
func (m *ClaudeMemManager) CodexHomeOverride() string {
	home := CodexHomeDir(m.HomeDir)
	if home == canonicalPath(filepath.Join(m.HomeDir, ".codex")) {
		return ""
	}
	return home
}

func (m *ClaudeMemManager) codexConfigPath() string {
	return filepath.Join(CodexHomeDir(m.HomeDir), "config.toml")
}

// EnsureCodexCacheRuntime installs the codex plugin cache's dependencies when
// node_modules is missing or empty, so codex's native hooks can run. Returns
// the cache path when an install ran and left it runnable, "" when nothing
// needed doing (no cache, or already runnable), and an error when a repair was
// needed but could not be completed. It does not depend on LocatePlugin, so it
// works when the codex cache is the only claude-mem copy on the machine.
func (m *ClaudeMemManager) EnsureCodexCacheRuntime(ctx context.Context) (string, error) {
	path, runnable := CodexClaudeMemCache(m.HomeDir)
	if path == "" || runnable {
		return "", nil
	}
	if !hasClaudeMemScripts(path) {
		return "", fmt.Errorf("codex plugin cache at %s is not a claude-mem plugin; %s", path, claudeMemReinstallCommand)
	}
	if m.BunPath == "" || !filepath.IsAbs(m.BunPath) {
		return "", errors.New("bun executable path must be absolute to install the codex plugin cache runtime")
	}
	run := m.RunBunInstall
	if run == nil {
		run = runBunInstall
	}
	ctx, cancel := context.WithTimeout(ctx, codexCacheInstallTimeout)
	defer cancel()
	out, err := run(ctx, m.BunPath, path)
	if err != nil {
		return "", fmt.Errorf("bun install in %s: %w: %s", path, err, strings.TrimSpace(string(out)))
	}
	if !hasClaudeMemRuntime(path) {
		return "", fmt.Errorf("bun install in %s left node_modules empty; %s", path, claudeMemReinstallCommand)
	}
	return path, nil
}

// runBunInstall runs the pinned dependency install. bunPath comes from
// exec.LookPath + filepath.Abs in the CLI and is re-checked here; dir is a
// glob match under the codex plugin cache, never user input.
func runBunInstall(ctx context.Context, bunPath, dir string) ([]byte, error) {
	if !filepath.IsAbs(bunPath) {
		return nil, fmt.Errorf("bun path %q is not absolute", bunPath)
	}
	// nosemgrep: go.lang.security.audit.dangerous-exec-command -- bunPath is
	// exec.LookPath+filepath.Abs output re-checked as absolute above; the
	// arguments are static and dir is a codex plugin cache glob match.
	cmd := exec.CommandContext(ctx, bunPath, "install", "--frozen-lockfile")
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

// LocatePlugin resolves an installed claude-mem plugin without pinning a
// cache version. The Claude marketplace checkout is preferred because Codex,
// Kimi, and Kiro can all share it — but only when its runtime is actually
// installed; a bare git checkout (scripts committed, no node_modules) falls
// through to the installed plugin caches.
func (m *ClaudeMemManager) LocatePlugin() (string, error) {
	if m.PluginRoot != "" && isClaudeMemPlugin(m.PluginRoot) {
		return m.PluginRoot, nil
	}

	var candidates []string
	for _, root := range []string{os.Getenv("CLAUDE_PLUGIN_ROOT"), os.Getenv("PLUGIN_ROOT")} {
		if root != "" {
			candidates = append(candidates, root)
		}
	}
	candidates = append(candidates,
		filepath.Join(m.HomeDir, ".claude", "plugins", "marketplaces", "thedotmack", "plugin"),
	)
	// Claude Code records the active install in installed_plugins.json and
	// deletes superseded cache versions 14 days after marking them orphaned, so
	// the recorded path outranks any mtime guess over the cache dirs.
	candidates = append(candidates, m.installedClaudeMemPaths()...)
	for _, pattern := range []string{
		filepath.Join(CodexHomeDir(m.HomeDir), "plugins", "cache", "claude-mem-local", "claude-mem", "*"),
		filepath.Join(CodexHomeDir(m.HomeDir), "plugins", "cache", "thedotmack", "claude-mem", "*"),
		filepath.Join(m.HomeDir, ".claude", "plugins", "cache", "thedotmack", "claude-mem", "*"),
	} {
		matches, _ := filepath.Glob(pattern)
		matches = withoutOrphaned(matches)
		sort.Slice(matches, func(i, j int) bool {
			left, _ := os.Stat(matches[i])
			right, _ := os.Stat(matches[j])
			if left == nil || right == nil {
				return matches[i] > matches[j]
			}
			return left.ModTime().After(right.ModTime())
		})
		candidates = append(candidates, matches...)
	}

	broken := ""
	for _, candidate := range candidates {
		for _, root := range []string{candidate, filepath.Join(candidate, "plugin")} {
			if isClaudeMemPlugin(root) {
				return root, nil
			}
			if broken == "" && hasClaudeMemScripts(root) {
				broken = root
			}
		}
	}
	if broken != "" {
		return "", fmt.Errorf("claude-mem plugin at %s is missing its runtime (node_modules); %s", broken, claudeMemRepairHint)
	}
	return "", errors.New("claude-mem plugin not found; install it in Claude Code or Codex first")
}

func isClaudeMemPlugin(root string) bool {
	return hasClaudeMemScripts(root) && hasClaudeMemRuntime(root)
}

func hasClaudeMemScripts(root string) bool {
	if root == "" {
		return false
	}
	for _, name := range []string{"mcp-server.cjs", "transcript-watcher.cjs", "bun-runner.js"} {
		if info, err := os.Stat(filepath.Join(root, "scripts", name)); err != nil || info.IsDir() {
			return false
		}
	}
	return true
}

// hasClaudeMemRuntime reports whether installed dependencies are present.
// Current claude-mem releases no longer write the .install-version marker, so
// node_modules is the signal. An interrupted install can leave an empty
// node_modules, which cannot run the MCP server or transcript watcher, so the
// directory must hold at least one entry. A marketplace git checkout without
// it is rejected the same way.
func hasClaudeMemRuntime(root string) bool {
	entries, err := os.ReadDir(filepath.Join(root, "node_modules"))
	return err == nil && len(entries) > 0
}

// installedClaudeMemPaths returns claude-mem install paths recorded in Claude
// Code's installed_plugins.json, user scope first, in a stable order.
func (m *ClaudeMemManager) installedClaudeMemPaths() []string {
	raw, err := os.ReadFile(filepath.Join(m.HomeDir, ".claude", "plugins", "installed_plugins.json"))
	if err != nil {
		return nil
	}
	var doc struct {
		Plugins map[string][]struct {
			Scope       string `json:"scope"`
			InstallPath string `json:"installPath"`
		} `json:"plugins"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return nil
	}
	keys := make([]string, 0, len(doc.Plugins))
	for key := range doc.Plugins {
		if strings.HasPrefix(key, "claude-mem@") {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	var user, other []string
	for _, key := range keys {
		for _, install := range doc.Plugins[key] {
			switch {
			case install.InstallPath == "":
			case install.Scope == "user":
				user = append(user, install.InstallPath)
			default:
				other = append(other, install.InstallPath)
			}
		}
	}
	// A recorded installPath can point at a cache version Claude Code has
	// since orphaned, so it goes through the same filter as the glob fallback.
	return withoutOrphaned(append(user, other...))
}

// withoutOrphaned drops plugin cache dirs that Claude Code has marked for
// deletion with an .orphaned_at file.
func withoutOrphaned(dirs []string) []string {
	kept := dirs[:0]
	for _, dir := range dirs {
		if !fileExists(filepath.Join(dir, ".orphaned_at")) {
			kept = append(kept, dir)
		}
	}
	return kept
}

// CodexClaudeMemCache returns the newest non-orphaned codex claude-mem plugin
// cache dir and whether its runtime is installed. path == "" means no cache
// exists. Orphaned dirs are marked for deletion, so they never win. The cache
// is looked up under CodexHomeDir, so a shell with CODEX_HOME set inspects the
// same home its codex uses.
// ponytail: newest-modtime dir approximates codex's active cache version;
// parse config.toml plugin pins if this ever misfires.
func CodexClaudeMemCache(homeDir string) (string, bool) {
	var newest string
	var newestMod time.Time
	codexHome := CodexHomeDir(homeDir)
	for _, pattern := range []string{
		filepath.Join(codexHome, "plugins", "cache", "claude-mem-local", "claude-mem", "*"),
		filepath.Join(codexHome, "plugins", "cache", "thedotmack", "claude-mem", "*"),
	} {
		matches, _ := filepath.Glob(pattern)
		matches = withoutOrphaned(matches)
		for _, match := range matches {
			info, err := os.Stat(match)
			if err != nil || !info.IsDir() {
				continue
			}
			if newest == "" || info.ModTime().After(newestMod) {
				newest, newestMod = match, info.ModTime()
			}
		}
	}
	if newest == "" {
		return "", false
	}
	return newest, isClaudeMemPlugin(newest)
}

// EnsureMemoryInstructions adds the idempotent recall/capture policy to the
// shared AGENTS SSOT. Rendering remains owned by AgentsManager.
func EnsureMemoryInstructions(ssotPath string) (bool, error) {
	raw, err := os.ReadFile(ssotPath)
	if err != nil {
		return false, fmt.Errorf("read agents SSOT: %w", err)
	}
	text := string(raw)
	start := strings.Index(text, memoryBlockStart)
	end := strings.Index(text, memoryBlockEnd)
	var next string
	if start >= 0 && end >= start {
		end += len(memoryBlockEnd)
		next = text[:start] + memoryInstructions + text[end:]
	} else {
		next = strings.TrimRight(text, "\n") + "\n\n" + memoryInstructions + "\n"
	}
	if next == text {
		return false, nil
	}
	return true, atomicWriteFile(ssotPath, []byte(next), fileModeOrDefault(ssotPath, 0o644))
}

func HasMemoryInstructions(path string) bool {
	raw, err := os.ReadFile(path)
	return err == nil && bytes.Contains(raw, []byte(memoryBlockStart)) && bytes.Contains(raw, []byte(memoryBlockEnd))
}

// BuildTranscriptConfig discovers concrete session files so each transcript is
// associated with its workspace: Kimi/Kiro record it in sidecar metadata,
// Copilot sessions carry it in the session.start event's context, Qwen
// sessions carry it in a sibling .runtime.json (first-line cwd as fallback),
// and pi sessions carry it in the first line's session header.
func (m *ClaudeMemManager) BuildTranscriptConfig() (transcriptWatchConfig, error) {
	selection, err := m.memorySelection()
	if err != nil {
		return transcriptWatchConfig{}, err
	}
	watches := []transcriptWatch{}
	schemas := map[string]transcriptSchema{}
	for _, target := range []struct {
		name     string
		schema   transcriptSchema
		discover func() []transcriptWatch
	}{
		{"kimi", kimiTranscriptSchema(), m.kimiWatches}, {"kiro", kiroTranscriptSchema(), m.kiroWatches},
		{"copilot", copilotTranscriptSchema(), m.copilotWatches}, {"qwen", qwenTranscriptSchema(), m.qwenWatches},
		{"pi", piTranscriptSchema(), m.piWatches},
	} {
		if !memoryAgentSelected(selection, target.name) {
			continue
		}
		watches = append(watches, target.discover()...)
		schemas[target.name] = target.schema
	}
	sort.Slice(watches, func(i, j int) bool { return watches[i].Path < watches[j].Path })
	return transcriptWatchConfig{Version: 1, Schemas: schemas, Watches: watches, StateFile: m.TranscriptStatePath()}, nil
}

// BuildTranscriptConfigForDisplay returns discovery counts without exposing
// the bridge's internal schema representation to the CLI package.
func (m *ClaudeMemManager) BuildTranscriptConfigForDisplay() (map[string]int, error) {
	config, err := m.BuildTranscriptConfig()
	if err != nil {
		return nil, err
	}
	return countWatches(config.Watches), nil
}

func (m *ClaudeMemManager) kimiWatches() []transcriptWatch {
	pattern := filepath.Join(m.kimiHomeDir(), "sessions", "*", "session_*", "state.json")
	stateFiles, _ := filepath.Glob(pattern)
	var watches []transcriptWatch
	for _, statePath := range stateFiles {
		var state struct {
			WorkDir string `json:"workDir"`
			CWD     string `json:"cwd"`
		}
		if !readJSONFile(statePath, &state) {
			continue
		}
		// 0.40 records the live directory in `cwd`; `workDir` is the legacy
		// field and can be stale, so it is only a fallback.
		workspace := state.CWD
		if workspace == "" {
			workspace = state.WorkDir
		}
		if !isAbsoluteDirectory(workspace) {
			continue
		}
		wirePath := filepath.Join(filepath.Dir(statePath), "agents", "main", "wire.jsonl")
		if info, err := os.Stat(wirePath); err != nil || info.IsDir() {
			continue
		}
		watches = append(watches, transcriptWatch{
			Name: "kimi", Path: wirePath, Schema: "kimi", Workspace: workspace, StartAtEnd: false,
		})
	}
	return watches
}

func (m *ClaudeMemManager) kiroWatches() []transcriptWatch {
	pattern := filepath.Join(m.HomeDir, ".kiro", "sessions", "*", "sess_*", "session.json")
	stateFiles, _ := filepath.Glob(pattern)
	var watches []transcriptWatch
	for _, statePath := range stateFiles {
		var state struct {
			WorkspacePaths []string `json:"workspacePaths"`
		}
		if !readJSONFile(statePath, &state) || len(state.WorkspacePaths) == 0 || !isAbsoluteDirectory(state.WorkspacePaths[0]) {
			continue
		}
		messagesPath := filepath.Join(filepath.Dir(statePath), "messages.jsonl")
		if info, err := os.Stat(messagesPath); err != nil || info.IsDir() {
			continue
		}
		watches = append(watches, transcriptWatch{
			Name: "kiro", Path: messagesPath, Schema: "kiro", Workspace: state.WorkspacePaths[0], StartAtEnd: false,
		})
	}
	return watches
}

func (m *ClaudeMemManager) copilotWatches() []transcriptWatch {
	pattern := filepath.Join(m.HomeDir, ".copilot", "session-state", "*", "events.jsonl")
	eventsFiles, _ := filepath.Glob(pattern)
	var watches []transcriptWatch
	for _, eventsPath := range eventsFiles {
		if info, err := os.Stat(eventsPath); err != nil || info.IsDir() {
			continue
		}
		workspace := copilotSessionWorkspace(eventsPath)
		if !isAbsoluteDirectory(workspace) {
			continue
		}
		watches = append(watches, transcriptWatch{
			Name: "copilot", Path: eventsPath, Schema: "copilot", Workspace: workspace, StartAtEnd: false,
		})
	}
	return watches
}

// copilotSessionWorkspace reads the first line of a Copilot events.jsonl and
// extracts the workspace path from the session.start event's data.context.cwd
// (or data.context.gitRoot as a fallback). This avoids opening the live SQLite
// session-store.db.
func copilotSessionWorkspace(eventsPath string) string {
	f, err := os.Open(eventsPath)
	if err != nil {
		return ""
	}
	defer f.Close()
	reader := bufio.NewReader(f)
	firstLine, err := reader.ReadString('\n')
	if err != nil && firstLine == "" {
		return ""
	}
	var event struct {
		Type string `json:"type"`
		Data struct {
			Context struct {
				CWD     string `json:"cwd"`
				GitRoot string `json:"gitRoot"`
			} `json:"context"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(strings.TrimRight(firstLine, "\r\n")), &event) != nil || event.Type != "session.start" {
		return ""
	}
	if event.Data.Context.CWD != "" {
		return event.Data.Context.CWD
	}
	return event.Data.Context.GitRoot
}

// qwenWatches discovers Qwen Code chat transcripts. Each chat has a sibling
// <session>.runtime.json recording the live work_dir; the first chat line's
// top-level cwd is the fallback.
func (m *ClaudeMemManager) qwenWatches() []transcriptWatch {
	pattern := filepath.Join(m.HomeDir, ".qwen", "projects", "*", "chats", "*.jsonl")
	chatFiles, _ := filepath.Glob(pattern)
	var watches []transcriptWatch
	for _, chatPath := range chatFiles {
		if info, err := os.Stat(chatPath); err != nil || info.IsDir() {
			continue
		}
		workspace := qwenSessionWorkspace(chatPath)
		if !isAbsoluteDirectory(workspace) {
			continue
		}
		watches = append(watches, transcriptWatch{
			Name: "qwen", Path: chatPath, Schema: "qwen", Workspace: workspace, StartAtEnd: false,
		})
	}
	return watches
}

// qwenSessionWorkspace reads the sibling <session>.runtime.json's work_dir,
// falling back to the first chat line's top-level cwd.
func qwenSessionWorkspace(chatPath string) string {
	var runtimeState struct {
		WorkDir string `json:"work_dir"`
	}
	if readJSONFile(strings.TrimSuffix(chatPath, ".jsonl")+".runtime.json", &runtimeState) && runtimeState.WorkDir != "" {
		return runtimeState.WorkDir
	}
	var line struct {
		CWD string `json:"cwd"`
	}
	if !readFirstJSONLine(chatPath, &line) {
		return ""
	}
	return line.CWD
}

// piWatches discovers pi session transcripts. Each session carries its cwd in
// the first line's session header — pi has no sidecar file, unlike Qwen's
// .runtime.json.
func (m *ClaudeMemManager) piWatches() []transcriptWatch {
	pattern := filepath.Join(m.HomeDir, ".pi", "agent", "sessions", "*", "*.jsonl")
	sessionFiles, _ := filepath.Glob(pattern)
	var watches []transcriptWatch
	for _, sessionPath := range sessionFiles {
		if info, err := os.Stat(sessionPath); err != nil || info.IsDir() {
			continue
		}
		workspace := piSessionWorkspace(sessionPath)
		if !isAbsoluteDirectory(workspace) {
			continue
		}
		watches = append(watches, transcriptWatch{
			Name: "pi", Path: sessionPath, Schema: "pi", Workspace: workspace, StartAtEnd: false,
		})
	}
	return watches
}

// piSessionWorkspace reads the first line's session header cwd.
func piSessionWorkspace(sessionPath string) string {
	var line struct {
		Type string `json:"type"`
		CWD  string `json:"cwd"`
	}
	if !readFirstJSONLine(sessionPath, &line) || line.Type != "session" {
		return ""
	}
	return line.CWD
}

// readFirstJSONLine decodes the first line of a JSONL transcript into target.
// Both pi and Qwen carry the session's cwd there, and a transcript can be huge,
// so only that line is read. A CRLF line ending, a file with no trailing
// newline, and an empty file all resolve without an error path of their own.
func readFirstJSONLine(path string, target any) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	firstLine, err := bufio.NewReader(f).ReadString('\n')
	if err != nil && firstLine == "" {
		return false
	}
	return json.Unmarshal([]byte(strings.TrimRight(firstLine, "\r\n")), target) == nil
}

func readJSONFile(path string, target any) bool {
	raw, err := os.ReadFile(path)
	return err == nil && json.Unmarshal(raw, target) == nil
}

func isAbsoluteDirectory(path string) bool {
	if path == "" || !filepath.IsAbs(path) {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func kimiTranscriptSchema() transcriptSchema {
	return transcriptSchema{
		Name: "kimi", Version: "0.40", Description: "Kimi Code wire.jsonl session events.",
		Events: []transcriptEvent{
			{Name: "user-prompt", Match: equals("type", "turn.prompt"), Action: "session_init", Fields: map[string]any{"prompt": "input[0].text"}},
			{Name: "assistant-text", Match: equals("event.part.type", "text"), Action: "assistant_message", Fields: map[string]any{"message": "event.part.text"}},
			{Name: "tool-call", Match: equals("event.type", "tool.call"), Action: "tool_use", Fields: map[string]any{"toolId": "event.toolCallId", "toolName": "event.name", "toolInput": "event.args"}},
			{Name: "tool-result", Match: equals("event.type", "tool.result"), Action: "tool_result", Fields: map[string]any{"toolId": "event.toolCallId", "toolResponse": map[string]any{"coalesce": []any{"event.result.output", "event.result"}}}},
			{Name: "turn-ended", Match: equals("type", "turn.ended"), Action: "session_end"},
			{Name: "canceled-turn", Match: equals("type", "turn.cancel"), Action: "session_end"},
		},
	}
}

func kiroTranscriptSchema() transcriptSchema {
	return transcriptSchema{
		Name: "kiro", Version: "2.13", Description: "Kiro CLI messages.jsonl session events.",
		Events: []transcriptEvent{
			{Name: "user-prompt", Match: equals("payload.type", "user"), Action: "session_init", Fields: map[string]any{"prompt": "payload.content"}},
			{Name: "assistant-message", Match: equals("payload.type", "assistant"), Action: "assistant_message", Fields: map[string]any{"message": "payload.content"}},
			{Name: "tool-call", Match: equals("payload.type", "tool_call"), Action: "tool_use", Fields: map[string]any{"toolId": "payload.toolCallId", "toolName": "payload.toolName", "toolInput": "payload.args"}},
			{Name: "tool-result", Match: equals("payload.type", "tool_result"), Action: "tool_result", Fields: map[string]any{"toolId": "payload.toolCallId", "toolResponse": "payload.content"}},
			{Name: "turn-end", Match: equals("payload.type", "turn_end"), Action: "session_end"},
		},
	}
}

func copilotTranscriptSchema() transcriptSchema {
	return transcriptSchema{
		Name: "copilot", Version: "1.0", Description: "GitHub Copilot CLI session-state events.jsonl per-session log.",
		Events: []transcriptEvent{
			{Name: "user-message", Match: equals("type", "user.message"), Action: "session_init", Fields: map[string]any{"prompt": "data.content"}},
			{Name: "assistant-message", Match: equals("type", "assistant.message"), Action: "assistant_message", Fields: map[string]any{"message": "data.content"}},
			{Name: "tool-call", Match: equals("type", "tool.execution_start"), Action: "tool_use", Fields: map[string]any{"toolId": "data.toolCallId", "toolName": "data.toolName", "toolInput": "data.arguments"}},
			{Name: "tool-result", Match: equals("type", "tool.execution_complete"), Action: "tool_result", Fields: map[string]any{"toolId": "data.toolCallId", "toolResponse": map[string]any{"coalesce": []any{"data.result.content", "data.result"}}}},
			{Name: "session-end", Match: equals("type", "session.shutdown"), Action: "session_end"},
		},
	}
}

// qwenTranscriptSchema maps Qwen Code chat JSONL lines to bridge events. The
// watcher matcher supports a single top-level path per event, so every event
// matches on type or provenance: tool_result lines carry message.role "user"
// but type "tool_result", so top-level type stays unambiguous. Assistant lines
// pack thought/text/functionCall parts into one line and there is no
// session-end marker, so tool_use is not mapped and lines that match no event
// are ignored.
//
// ponytail: a Qwen thought part is {"text": ..., "thought": true}, so the
// coalesce cannot tell it from a reply part, and an assistant line whose only
// text is a thought stores that thought as the message. Ruling it out needs a
// second condition on the same event, which the single-path matcher has no
// room for. The cost is bounded: assistant_message only feeds
// last_assistant_message, which the watcher reads on session_end — an event
// Qwen's format never emits — so nothing leaves the bridge.
func qwenTranscriptSchema() transcriptSchema {
	return transcriptSchema{
		Name: "qwen", Version: "0.24", Description: "Qwen Code chat JSONL per-session log.",
		Events: []transcriptEvent{
			{Name: "user-prompt", Match: equals("provenance", "real_user"), Action: "session_init", Fields: map[string]any{"prompt": "message.parts[0].text"}},
			{Name: "assistant-message", Match: equals("type", "assistant"), Action: "assistant_message", Fields: map[string]any{"message": map[string]any{"coalesce": []any{"message.parts[1].text", "message.parts[0].text"}}}},
			{Name: "tool-result", Match: equals("type", "tool_result"), Action: "tool_result", Fields: map[string]any{"toolName": "message.parts[0].functionResponse.name", "toolResponse": "message.parts[0].functionResponse.response"}},
		},
	}
}

// piTranscriptSchema maps pi session JSONL lines to bridge events. Every
// entry is a message envelope, so all events match on message.role: user
// messages carry no provenance marker, toolResult is a message-level role
// (not a top-level type), and assistant content interleaves thinking/text/
// toolCall blocks so positions vary. The coalesce walks the first content
// slots in order, which selects the first text block: a pi thinking block
// keys its prose under "thinking" and a toolCall block has no text at all,
// so only a real text block resolves. pi has no session-end marker and
// toolCall blocks are not mapped; lines that match no event are ignored.
//
// ponytail: four slots, because the field selector takes literal indices and
// the watcher's matcher has no way to say "the first block of type text".
// Text past the fourth block is dropped; widen the list if a real transcript
// ever shows one.
func piTranscriptSchema() transcriptSchema {
	return transcriptSchema{
		Name: "pi", Version: "3", Description: "pi session JSONL per-session log.",
		Events: []transcriptEvent{
			{Name: "user-prompt", Match: equals("message.role", "user"), Action: "session_init", Fields: map[string]any{"prompt": "message.content[0].text"}},
			{Name: "assistant-message", Match: equals("message.role", "assistant"), Action: "assistant_message", Fields: map[string]any{"message": map[string]any{"coalesce": []any{"message.content[0].text", "message.content[1].text", "message.content[2].text", "message.content[3].text"}}}},
			{Name: "tool-result", Match: equals("message.role", "toolResult"), Action: "tool_result", Fields: map[string]any{"toolName": "message.toolName", "toolResponse": "message.content[0].text"}},
		},
	}
}

func equals(path string, value any) map[string]any {
	return map[string]any{"path": path, "equals": value}
}

func (m *ClaudeMemManager) writeTranscriptConfig(config transcriptWatchConfig) (bool, error) {
	raw, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return false, err
	}
	raw = append(raw, '\n')
	return writeIfChanged(m.TranscriptConfigPath(), raw, 0o600)
}

func (m *ClaudeMemManager) seedTranscriptState(config transcriptWatchConfig) error {
	if _, err := os.Stat(m.TranscriptStatePath()); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	offsets := make(map[string]int64, len(config.Watches))
	for _, watch := range config.Watches {
		if info, err := os.Stat(watch.Path); err == nil {
			offsets[watch.Path] = info.Size()
		}
	}
	raw, err := json.MarshalIndent(map[string]any{"offsets": offsets}, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(m.TranscriptStatePath(), append(raw, '\n'), 0o600)
}

// mcpEntryVariant controls which extra fields are emitted when writing a
// claude-mem entry into an MCP configuration file.
type mcpEntryVariant int

const (
	mcpVariantStandard mcpEntryVariant = iota // kimi: command + args only
	mcpVariantKiro                            // kiro: adds "disabled": false
	mcpVariantCopilot                         // copilot: adds "type": "local", "tools": ["*"]
)

// Install wires recall into Kimi/Kiro/Copilot, prepares transcript capture,
// and loads the macOS user LaunchAgent that keeps new sessions discovered.
func (m *ClaudeMemManager) Install(ctx context.Context) (ClaudeMemInstallResult, error) {
	selection, selectionErr := m.memorySelection()
	if selectionErr != nil {
		return ClaudeMemInstallResult{}, selectionErr
	}
	if selection != nil {
		return m.PrepareSelectedIntegration()
	}

	if m.DotPath == "" || !filepath.IsAbs(m.DotPath) {
		return ClaudeMemInstallResult{}, errors.New("dot executable path must be absolute")
	}
	if m.NodePath == "" || !filepath.IsAbs(m.NodePath) {
		return ClaudeMemInstallResult{}, errors.New("node executable path must be absolute")
	}
	if m.BunPath == "" || !filepath.IsAbs(m.BunPath) {
		return ClaudeMemInstallResult{}, errors.New("bun executable path must be absolute")
	}
	// Repair the codex cache before locating the plugin: on a machine where the
	// codex cache is the only claude-mem copy, LocatePlugin can only succeed
	// after node_modules exists. A failed repair is reported, not fatal, so the
	// Kimi/Kiro/Copilot/Qwen/pi wiring and the bridge still install.
	codexCache, codexErr := m.EnsureCodexCacheRuntime(ctx)
	codexCacheError := ""
	if codexErr != nil {
		codexCacheError = codexErr.Error()
	}
	pluginRoot, err := m.LocatePlugin()
	if err != nil {
		return ClaudeMemInstallResult{}, err
	}
	if !codexClaudeMemEnabled(m.codexConfigPath()) {
		return ClaudeMemInstallResult{}, errors.New("codex claude-mem plugin is not enabled; run `codex plugin add claude-mem@claude-mem-local` first")
	}

	var changedPaths []string
	for _, target := range []struct {
		path    string
		variant mcpEntryVariant
	}{
		{m.KimiMCPPath(), mcpVariantStandard},
		{m.QwenMCPPath(), mcpVariantStandard},
		{m.KiroMCPPath(), mcpVariantKiro},
		{m.CopilotMCPPath(), mcpVariantCopilot},
	} {
		changed, err := ensureMCPEntry(target.path, m.DotPath, target.variant)
		if err != nil {
			return ClaudeMemInstallResult{}, err
		}
		if changed {
			changedPaths = append(changedPaths, target.path)
		}
	}

	config, err := m.BuildTranscriptConfig()
	if err != nil {
		return ClaudeMemInstallResult{}, err
	}
	if _, err := m.writeTranscriptConfig(config); err != nil {
		return ClaudeMemInstallResult{}, err
	}
	if err := m.seedTranscriptState(config); err != nil {
		return ClaudeMemInstallResult{}, fmt.Errorf("seed transcript state: %w", err)
	}
	if err := m.installLaunchAgent(ctx); err != nil {
		return ClaudeMemInstallResult{}, err
	}

	return ClaudeMemInstallResult{
		PluginRoot: pluginRoot, ConfigPaths: changedPaths, BridgePath: m.LaunchdPlistPath(), WatchCount: countWatches(config.Watches),
		CodexCachePath: codexCache, CodexCacheError: codexCacheError,
	}, nil
}

func ensureMCPEntry(path, dotPath string, variant mcpEntryVariant) (bool, error) {
	return ensureScopedMCPEntry(path, dotPath, variant, "", "")
}

func ensureScopedMCPEntry(path, dotPath string, variant mcpEntryVariant, home, pluginRoot string) (bool, error) {
	doc := map[string]json.RawMessage{}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &doc); err != nil {
			return false, fmt.Errorf("parse MCP config %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return false, err
	}
	servers := map[string]json.RawMessage{}
	if raw := doc["mcpServers"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &servers); err != nil {
			return false, fmt.Errorf("parse mcpServers in %s: %w", path, err)
		}
	}
	entry := map[string]any{"command": dotPath, "args": []string{"ai", "memory", "mcp-server"}}
	if home != "" {
		entry["args"] = []string{"--home", home, "ai", "memory", "mcp-server"}
		entry["env"] = map[string]string{"HOME": home, "CLAUDE_PLUGIN_ROOT": pluginRoot, "PLUGIN_ROOT": pluginRoot, "CODEX_HOME": filepath.Join(home, ".codex")}
	}
	switch variant {
	case mcpVariantKiro:
		entry["disabled"] = false
	case mcpVariantCopilot:
		entry["type"] = "local"
		entry["tools"] = []string{"*"}
	}
	entryRaw, _ := json.Marshal(entry)
	var currentEntry map[string]any
	if json.Unmarshal(servers["claude-mem"], &currentEntry) == nil {
		currentRaw, _ := json.Marshal(currentEntry)
		if bytes.Equal(currentRaw, entryRaw) {
			return false, nil
		}
	}
	servers["claude-mem"] = entryRaw
	serversRaw, _ := json.Marshal(servers)
	doc["mcpServers"] = serversRaw
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return false, err
	}
	return true, atomicWriteFile(path, append(out, '\n'), fileModeOrDefault(path, 0o600))
}

func (m *ClaudeMemManager) installLaunchAgent(ctx context.Context) error {
	if runtime.GOOS != "darwin" {
		return errors.New("claude-mem bridge service installation currently requires macOS launchd")
	}
	for _, dir := range []string{filepath.Dir(m.LaunchdPlistPath()), filepath.Dir(m.BridgeLogPath())} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>ai</string>
    <string>memory</string>
    <string>bridge</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>HOME</key>
    <string>%s</string>
    <key>PATH</key>
    <string>%s:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string>
  </dict>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>ThrottleInterval</key>
  <integer>5</integer>
  <key>StandardOutPath</key>
  <string>%s</string>
  <key>StandardErrorPath</key>
  <string>%s</string>
</dict>
</plist>
`, ClaudeMemLaunchdLabel, xmlEscape(m.DotPath), xmlEscape(m.HomeDir), xmlEscape(filepath.Dir(m.NodePath)), xmlEscape(m.BridgeLogPath()), xmlEscape(m.BridgeLogPath()))
	if err := atomicWriteFile(m.LaunchdPlistPath(), []byte(plist), 0o644); err != nil {
		return fmt.Errorf("write launch agent: %w", err)
	}
	domain := "gui/" + strconv.Itoa(os.Getuid())
	target := domain + "/" + ClaudeMemLaunchdLabel
	printTarget := func() error {
		return exec.CommandContext(ctx, "launchctl", "print", target).Run()
	}
	_ = exec.CommandContext(ctx, "launchctl", "bootout", target).Run()
	// bootout is asynchronous: wait until the old service has actually left
	// the domain, otherwise bootstrap/print below can observe the dying
	// instance and kickstart then fails with "Could not find service".
	for attempt := 0; attempt < 20 && printTarget() == nil; attempt++ {
		time.Sleep(100 * time.Millisecond)
	}
	// The old bridge has exited, so nothing holds its log open. Reset the log
	// once it has grown large; KeepAlive restarts otherwise append forever.
	if info, err := os.Stat(m.BridgeLogPath()); err == nil && info.Size() > 10<<20 {
		_ = os.Truncate(m.BridgeLogPath(), 0)
	}
	var bootstrapErr error
	var bootstrapOut []byte
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 250 * time.Millisecond)
		}
		bootstrapOut, bootstrapErr = exec.CommandContext(ctx, "launchctl", "bootstrap", domain, m.LaunchdPlistPath()).CombinedOutput()
		if bootstrapErr == nil || printTarget() == nil {
			bootstrapErr = nil
			break
		}
	}
	if bootstrapErr != nil {
		return fmt.Errorf("bootstrap claude-mem bridge: %w: %s", bootstrapErr, strings.TrimSpace(string(bootstrapOut)))
	}
	var kickstartErr error
	var kickstartOut []byte
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 250 * time.Millisecond)
		}
		kickstartOut, kickstartErr = exec.CommandContext(ctx, "launchctl", "kickstart", "-k", target).CombinedOutput()
		if kickstartErr == nil {
			return nil
		}
	}
	return fmt.Errorf("start claude-mem bridge: %w: %s", kickstartErr, strings.TrimSpace(string(kickstartOut)))
}

func xmlEscape(value string) string {
	return html.EscapeString(value)
}

// RunMCPServer attaches the current terminal to claude-mem's stdio MCP server.
func (m *ClaudeMemManager) RunMCPServer(ctx context.Context) error {
	if m.NodePath == "" || !filepath.IsAbs(m.NodePath) {
		return errors.New("node executable path must be absolute")
	}
	pluginRoot, err := m.LocatePlugin()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, m.NodePath, filepath.Join(pluginRoot, "scripts", "mcp-server.cjs"))
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// RunBridge supervises the claude-mem transcript watcher and reloads it when a
// newly created Kimi, Kiro, Copilot, Qwen, or pi session adds a concrete
// workspace mapping.
func (m *ClaudeMemManager) RunBridge(ctx context.Context) error {
	if m.BunPath == "" || !filepath.IsAbs(m.BunPath) {
		return errors.New("bun executable path must be absolute")
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	var child *exec.Cmd
	var done chan error
	var signature string
	var lastRescanErr string
	start := func() error {
		config, err := m.BuildTranscriptConfig()
		if err != nil {
			return err
		}
		selection, err := m.memorySelection()
		if err != nil {
			return err
		}
		if selection != nil && len(config.Watches) == 0 {
			if child != nil {
				stopChild(child, done)
				child, done = nil, nil
			}
			signature = ""
			if _, err := os.Stat(m.TranscriptConfigPath()); err == nil {
				_, err = m.writeTranscriptConfig(config)
				return err
			} else if !os.IsNotExist(err) {
				return err
			}
			return nil
		}
		pluginRoot, err := m.LocatePlugin()
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(config.Watches)
		nextSignature := pluginRoot + "\n" + string(raw)
		if child != nil && nextSignature == signature {
			return nil
		}
		if child != nil {
			stopChild(child, done)
			child, done = nil, nil
		}
		if _, err := m.writeTranscriptConfig(config); err != nil {
			return err
		}
		child = exec.CommandContext(ctx, m.BunPath,
			filepath.Join(pluginRoot, "scripts", "transcript-watcher.cjs"),
			"watch", "--config", m.TranscriptConfigPath(),
		)
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			child = nil
			return err
		}
		done = make(chan error, 1)
		go func(cmd *exec.Cmd, ch chan<- error) { ch <- cmd.Wait() }(child, done)
		signature = nextSignature
		return nil
	}

	if err := start(); err != nil {
		return err
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			if child != nil {
				stopChild(child, done)
			}
			return nil
		case err := <-done:
			child, done, signature = nil, nil, ""
			if ctx.Err() == nil && err != nil {
				fmt.Fprintf(os.Stderr, "claude-mem transcript watcher exited: %v\n", err)
			}
		case <-ticker.C:
			// Suppress a failure that repeats the immediately preceding
			// message: consecutive suppression bounds the 2 s ticker spam
			// (278k identical lines in one launchd log) without hiding a
			// cause that alternates between messages.
			if err := start(); err != nil {
				if msg := err.Error(); msg != lastRescanErr {
					fmt.Fprintf(os.Stderr, "claude-mem bridge rescan failed: %v\n", err)
					lastRescanErr = msg
				}
			} else if lastRescanErr != "" {
				fmt.Fprintln(os.Stderr, "claude-mem bridge rescan recovered")
				lastRescanErr = ""
			}
		}
	}
}

func stopChild(child *exec.Cmd, done <-chan error) {
	if child == nil || child.Process == nil {
		return
	}
	_ = child.Process.Signal(os.Interrupt)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = child.Process.Kill()
		<-done
	}
}

func (m *ClaudeMemManager) Status(ctx context.Context, ssotPath string) ClaudeMemStatus {
	status := ClaudeMemStatus{WatchCount: map[string]int{}}
	status.CodexCachePath, status.CodexCacheRunnable = CodexClaudeMemCache(m.HomeDir)
	status.CodexHome = m.CodexHomeOverride()
	if pluginRoot, err := m.LocatePlugin(); err == nil {
		status.PluginRoot = pluginRoot
		// Codex's native hooks execute from codex's own plugin cache, not from
		// dot's resolved root, so an absent or broken cache turns the codex
		// row red even when a healthy claude-side copy resolves.
		status.CodexNativeHooks = fileExists(filepath.Join(pluginRoot, "hooks", "codex-hooks.json")) &&
			codexClaudeMemEnabled(m.codexConfigPath()) &&
			status.CodexCacheRunnable
		var manifest struct {
			Version string `json:"version"`
		}
		if readJSONFile(filepath.Join(pluginRoot, ".claude-plugin", "plugin.json"), &manifest) || readJSONFile(filepath.Join(pluginRoot, ".codex-plugin", "plugin.json"), &manifest) {
			status.PluginVersion = manifest.Version
		}
	} else {
		status.PluginError = err.Error()
	}
	status.KimiMCP = hasManagedMCPEntry(m.KimiMCPPath(), m.DotPath)
	status.QwenMCP = hasManagedMCPEntry(m.QwenMCPPath(), m.DotPath)
	if info, err := os.Stat(m.PiAgentsPath()); err == nil && !info.IsDir() {
		status.PiAgents = true
	}
	status.KiroMCP = hasManagedMCPEntry(m.KiroMCPPath(), m.DotPath)
	status.CopilotMCP = hasManagedCopilotMCPEntry(m.CopilotMCPPath(), m.DotPath)
	status.InstructionsEnabled = HasMemoryInstructions(ssotPath)
	status.BridgeInstalled = fileExists(m.LaunchdPlistPath())
	if config, err := m.BuildTranscriptConfig(); err == nil {
		status.WatchCount = countWatches(config.Watches)
	}
	if runtime.GOOS == "darwin" {
		target := fmt.Sprintf("gui/%d/%s", os.Getuid(), ClaudeMemLaunchdLabel)
		status.BridgeRunning = exec.CommandContext(ctx, "launchctl", "print", target).Run() == nil
	}
	return status
}

func codexClaudeMemEnabled(path string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	inClaudeMemPlugin := false
	for _, rawLine := range strings.Split(string(raw), "\n") {
		line := strings.TrimSpace(rawLine)
		if strings.HasPrefix(line, "[") {
			inClaudeMemPlugin = strings.HasPrefix(line, `[plugins."claude-mem@`) && strings.HasSuffix(line, `"]`)
			continue
		}
		if inClaudeMemPlugin && line == "enabled = true" {
			return true
		}
	}
	return false
}

func hasManagedMCPEntry(path, dotPath string) bool {
	var doc struct {
		MCPServers map[string]struct {
			Command  string   `json:"command"`
			Args     []string `json:"args"`
			Disabled bool     `json:"disabled"`
		} `json:"mcpServers"`
	}
	if !readJSONFile(path, &doc) {
		return false
	}
	entry, ok := doc.MCPServers["claude-mem"]
	args := entry.Args
	if len(args) == 5 && args[0] == "--home" && filepath.IsAbs(args[1]) {
		args = args[2:]
	}
	return ok && !entry.Disabled && len(args) == 3 && args[0] == "ai" && args[1] == "memory" && args[2] == "mcp-server" && sameMemoryExecutable(entry.Command, dotPath)
}

func hasManagedCopilotMCPEntry(path, dotPath string) bool {
	var doc struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
			Type    string   `json:"type"`
			Tools   []string `json:"tools"`
		} `json:"mcpServers"`
	}
	if !readJSONFile(path, &doc) {
		return false
	}
	entry, ok := doc.MCPServers["claude-mem"]
	return ok &&
		len(entry.Args) == 3 && entry.Args[0] == "ai" && entry.Args[1] == "memory" && entry.Args[2] == "mcp-server" &&
		entry.Type == "local" &&
		len(entry.Tools) == 1 && entry.Tools[0] == "*" && sameMemoryExecutable(entry.Command, dotPath)
}

// Preserve exact configured paths, and recognize filesystem aliases only when
// both absolute paths exist and name the same file. Never resolve bare commands
// through PATH: a different executable must not look like a managed adapter.
func sameMemoryExecutable(configured, invoked string) bool {
	if configured == invoked {
		return configured != ""
	}
	if !filepath.IsAbs(configured) || !filepath.IsAbs(invoked) {
		return false
	}
	configuredInfo, configuredErr := os.Stat(configured)
	invokedInfo, invokedErr := os.Stat(invoked)
	return configuredErr == nil && invokedErr == nil &&
		!configuredInfo.IsDir() && !invokedInfo.IsDir() && os.SameFile(configuredInfo, invokedInfo)
}

func countWatches(watches []transcriptWatch) map[string]int {
	counts := map[string]int{"kimi": 0, "kiro": 0, "copilot": 0, "qwen": 0, "pi": 0}
	for _, watch := range watches {
		counts[watch.Name]++
	}
	return counts
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func fileModeOrDefault(path string, fallback fs.FileMode) fs.FileMode {
	if info, err := os.Stat(path); err == nil {
		return info.Mode().Perm()
	}
	return fallback
}

func writeIfChanged(path string, content []byte, mode fs.FileMode) (bool, error) {
	if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, content) {
		return false, nil
	}
	return true, atomicWriteFile(path, content, mode)
}

func atomicWriteFile(path string, content []byte, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(content); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func (m *ClaudeMemManager) memorySelection() ([]string, error) {
	if m.AgentSelection != nil {
		return m.AgentSelection()
	}
	return m.SelectedAgents, nil
}
func memoryAgentSelected(selection []string, agent string) bool {
	if selection == nil {
		return true
	}
	for _, selected := range selection {
		if selected == agent {
			return true
		}
	}
	return false
}
func (m *ClaudeMemManager) kimiHomeDir() string {
	if filepath.IsAbs(m.KimiHome) {
		return m.KimiHome
	}
	return filepath.Join(m.HomeDir, ".kimi-code")
}

// PrepareSelectedIntegration updates selected recall adapters and the existing
// bridge's desired watch configuration only. It never installs dependencies,
// repairs caches, registers services, starts a backend, or restarts workers.
// Native Claude/Codex hooks remain plugin-owned. Grok/OpenCode capture has no
// proven adapter here and is deliberately absent from the watch configuration.
func (m *ClaudeMemManager) PrepareSelectedIntegration() (ClaudeMemInstallResult, error) {
	if m.ExplicitHome {
		return ClaudeMemInstallResult{}, errors.New("selected memory bridge preparation for explicit --home is deferred; adopt the live runtime profile first")
	}
	selection, err := m.memorySelection()
	if err != nil {
		return ClaudeMemInstallResult{}, err
	}
	if selection == nil {
		return ClaudeMemInstallResult{}, errors.New("explicit agent selection required for safe bridge preparation")
	}
	config, err := m.BuildTranscriptConfig()
	if err != nil {
		return ClaudeMemInstallResult{}, err
	}
	result := ClaudeMemInstallResult{WatchCount: countWatches(config.Watches)}
	bridgeSelected := memoryAgentSelected(selection, "kimi") || memoryAgentSelected(selection, "qwen")
	if !bridgeSelected {
		// Stop exposing previously managed watches without creating a worker
		// or new configuration tree on a never-configured host.
		if _, err := os.Stat(m.TranscriptConfigPath()); err == nil {
			_, err = m.writeTranscriptConfig(config)
			return result, err
		} else if !os.IsNotExist(err) {
			return result, err
		}
		return result, nil
	}
	if !filepath.IsAbs(m.DotPath) {
		return result, errors.New("absolute dot executable required for selected memory recall")
	}
	result.PluginRoot, err = m.LocatePlugin()
	if err != nil {
		return result, err
	}
	for _, target := range []struct{ agent, path string }{{"kimi", m.KimiMCPPath()}, {"qwen", m.QwenMCPPath()}} {
		if !memoryAgentSelected(selection, target.agent) {
			continue
		}
		changed, err := ensureScopedMCPEntry(target.path, m.DotPath, mcpVariantStandard, m.HomeDir, result.PluginRoot)
		if err != nil {
			return result, err
		}
		if changed {
			result.ConfigPaths = append(result.ConfigPaths, target.path)
		}
	}
	if _, err = m.writeTranscriptConfig(config); err != nil {
		return result, err
	}
	if err = m.seedTranscriptState(config); err != nil {
		return result, err
	}
	return result, nil
}
