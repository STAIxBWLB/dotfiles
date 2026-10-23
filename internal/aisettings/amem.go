package aisettings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// AmemPinnedVersion names the first @staix/agent-mem release that carries the
// P3 hooks and transcript ingest (agent-mem 0.6.0, the release that follows
// 0df2cf91). It is the version `dot ai memory status` expects to find. dot
// never installs the package: the person installs the exact version, and this
// constant is what status compares against.
const AmemPinnedVersion = "0.6.0"

// AmemServerName is the MCP server name amem is registered under. Tool names
// reach agents as mcp__amem__amem_search and so on.
const AmemServerName = "amem"

const (
	memoryBlockStart = "<!-- dotfiles:amem:start -->"
	memoryBlockEnd   = "<!-- dotfiles:amem:end -->"
	// legacyMemoryBlockStart and End delimit the block `dot ai memory install`
	// wrote for claude-mem. Every apply removes it, once and exactly.
	legacyMemoryBlockStart = "<!-- dotfiles:claude-mem:start -->"
	legacyMemoryBlockEnd   = "<!-- dotfiles:claude-mem:end -->"
)

// memoryInstructions is a short pointer, not the rule text: the rules live in
// the workspace (ai-workspace#53), and project memory and work lists come from
// amem while permanent and external knowledge comes from the vault.
const memoryInstructions = memoryBlockStart + `
## Memory

- Project memory and work lists come from the ` + "`amem`" + ` MCP server (amem_search, amem_work_state_read). Query it before non-trivial work when prior project context may affect the result, and treat what it returns as a lead to verify against the current workspace.
- Permanent and external knowledge comes from the vault under the workspace Vault-First policy. Promote a durable note to the vault with the workspace vault skills, never by writing a memory file.
- Session capture is automatic where the agent has hooks, and ` + "`amem ingest <agent>`" + ` covers the rest. Do not duplicate it into separate memory files unless explicitly requested.
` + memoryBlockEnd

// AmemInstallResult reports what Install wrote or would write.
type AmemInstallResult struct {
	// ConfigPaths are the MCP config files that changed (or would change).
	ConfigPaths []string
	// Manual holds the exact lines for files dot does not own: the person
	// applies them to Claude Code and Codex, which keep their own plugin and
	// MCP state.
	Manual []string
}

// AmemStatus is the read-only health of the amem integration.
type AmemStatus struct {
	KimiMCP        bool
	QwenMCP        bool
	AntigravityMCP bool
	KiroMCP        bool
	CopilotMCP     bool
	// PiAgents reports pi's instructions target. pi has no MCP support, so it
	// captures through `amem ingest pi` and reads through the instructions.
	PiAgents            bool
	InstructionsEnabled bool
	// AmemPath is the amem executable found on PATH, empty when missing.
	AmemPath string
	// Version is `amem --version` output, empty when it could not be read.
	Version string
}

// AmemManager registers amem's MCP stdio server for the agents that read MCP
// config files dot owns. It writes no launchd agent and starts no process.
type AmemManager struct {
	HomeDir string
	// AmemPath is the absolute path of the amem executable. Required to install.
	AmemPath string
	// SelectedAgents nil registers every supported agent. A non-nil selection
	// limits writes to those agents.
	SelectedAgents []string
	// KimiHome is an explicitly adopted native Kimi Code home, never ~/.kimi.
	KimiHome string
	// ExplicitHome forbids integrating a different live user/profile domain.
	ExplicitHome bool
	// Version reads the installed amem version; nil runs `amem --version`.
	Version func(ctx context.Context, amemPath string) (string, error)
}

func NewAmemManager(homeDir, amemPath string) *AmemManager {
	return &AmemManager{HomeDir: homeDir, AmemPath: amemPath}
}

func (m *AmemManager) KimiMCPPath() string {
	if filepath.IsAbs(m.KimiHome) {
		return filepath.Join(m.KimiHome, "mcp.json")
	}
	return filepath.Join(m.HomeDir, ".kimi-code", "mcp.json")
}

func (m *AmemManager) QwenMCPPath() string {
	return filepath.Join(m.HomeDir, ".qwen", "settings.json")
}

// AntigravityMCPPath is the Antigravity CLI's shared MCP config.
func (m *AmemManager) AntigravityMCPPath() string {
	return filepath.Join(m.HomeDir, ".gemini", "config", "mcp_config.json")
}

// PiAgentsPath is pi's global instructions target. pi takes no MCP entry.
func (m *AmemManager) PiAgentsPath() string {
	return filepath.Join(m.HomeDir, ".pi", "agent", "AGENTS.md")
}

func (m *AmemManager) KiroMCPPath() string {
	return filepath.Join(m.HomeDir, ".kiro", "settings", "mcp.json")
}

func (m *AmemManager) CopilotMCPPath() string {
	return filepath.Join(m.HomeDir, ".copilot", "mcp-config.json")
}

type mcpEntryVariant int

const (
	mcpVariantStandard mcpEntryVariant = iota // kimi, qwen, agy: command + args only
	mcpVariantKiro                            // kiro: adds "disabled": false
	mcpVariantCopilot                         // copilot: adds "type": "local", "tools": ["*"]
)

type mcpTarget struct {
	agent   string
	path    string
	variant mcpEntryVariant
}

func (m *AmemManager) targets() []mcpTarget {
	return []mcpTarget{
		{"kimi", m.KimiMCPPath(), mcpVariantStandard},
		{"qwen", m.QwenMCPPath(), mcpVariantStandard},
		{"antigravity", m.AntigravityMCPPath(), mcpVariantStandard},
		{"kiro", m.KiroMCPPath(), mcpVariantKiro},
		{"copilot", m.CopilotMCPPath(), mcpVariantCopilot},
	}
}

func (m *AmemManager) selected(agent string) bool {
	if m.SelectedAgents == nil {
		return true
	}
	for _, id := range m.SelectedAgents {
		if id == agent {
			return true
		}
	}
	return false
}

// Manual lists the exact edits for files dot does not own. Claude Code loads
// amem as a plugin whose hooks it runs itself, and Codex keeps MCP servers in
// its own config; neither is written here (docs/BOUNDARIES.md).
func (m *AmemManager) manualLines() []string {
	return []string{
		"Claude Code (amem plugin with its hooks): claude --plugin-dir <the @staix/agent-mem package root>   # the directory holding .claude-plugin/, hooks/ and bin/",
		"Codex (MCP): codex mcp add " + AmemServerName + " -- " + m.amemCommand() + " mcp",
		"pi (no MCP): run `amem ingest pi` at session end or on demand; `amem ingest codex` and `amem ingest kimi` do the same for those agents",
	}
}

func (m *AmemManager) amemCommand() string {
	if m.AmemPath != "" {
		return m.AmemPath
	}
	return "amem"
}

// Plan returns what Install would change, writing nothing.
func (m *AmemManager) Plan() (AmemInstallResult, error) { return m.install(true) }

// Install registers amem as an MCP server for every selected agent whose MCP
// config dot owns, and returns the manual lines for the rest. It never starts
// a process and writes no launchd agent.
func (m *AmemManager) Install() (AmemInstallResult, error) { return m.install(false) }

func (m *AmemManager) install(dryRun bool) (AmemInstallResult, error) {
	if m.AmemPath == "" || !filepath.IsAbs(m.AmemPath) {
		return AmemInstallResult{}, errors.New("amem executable path must be absolute: install the exact @staix/agent-mem version first, then re-run")
	}
	if m.ExplicitHome && m.SelectedAgents == nil {
		return AmemInstallResult{}, errors.New("explicit --home requires an explicit agent selection")
	}
	result := AmemInstallResult{Manual: m.manualLines()}
	for _, target := range m.targets() {
		if !m.selected(target.agent) {
			continue
		}
		changed, err := ensureAmemEntry(target.path, m.AmemPath, target.variant, dryRun)
		if err != nil {
			return result, err
		}
		if changed {
			result.ConfigPaths = append(result.ConfigPaths, target.path)
		}
	}
	return result, nil
}

func amemEntry(amemPath string, variant mcpEntryVariant) map[string]any {
	entry := map[string]any{"command": amemPath, "args": []string{"mcp"}}
	switch variant {
	case mcpVariantKiro:
		entry["disabled"] = false
	case mcpVariantCopilot:
		entry["type"] = "local"
		entry["tools"] = []string{"*"}
	}
	return entry
}

// ensureAmemEntry merges the amem server into a config's mcpServers, preserving
// every other server and top-level key. It reports whether the file changed.
func ensureAmemEntry(path, amemPath string, variant mcpEntryVariant, dryRun bool) (bool, error) {
	doc := map[string]json.RawMessage{}
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	// agy ships its MCP config as an empty file; treat that as no servers.
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &doc); err != nil {
			return false, fmt.Errorf("parse MCP config %s: %w", path, err)
		}
	}
	servers := map[string]json.RawMessage{}
	if current := doc["mcpServers"]; len(current) > 0 {
		if err := json.Unmarshal(current, &servers); err != nil {
			return false, fmt.Errorf("parse mcpServers in %s: %w", path, err)
		}
	}
	entryRaw, _ := json.Marshal(amemEntry(amemPath, variant))
	var existing map[string]any
	if json.Unmarshal(servers[AmemServerName], &existing) == nil {
		existingRaw, _ := json.Marshal(existing)
		if bytes.Equal(existingRaw, entryRaw) {
			return false, nil
		}
	}
	if dryRun {
		return true, nil
	}
	servers[AmemServerName] = entryRaw
	serversRaw, _ := json.Marshal(servers)
	doc["mcpServers"] = serversRaw
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return false, err
	}
	return true, atomicWriteFile(path, append(out, '\n'), fileModeOrDefault(path, 0o600))
}

// hasAmemEntry reports whether a config registers amem as `<amem> mcp`, in the
// shape the agent expects (kiro not disabled, copilot local with all tools). A
// configured command counts only when it is the same file as amemPath, never by
// resolving a bare name through PATH.
func hasAmemEntry(path, amemPath string, variant mcpEntryVariant) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var doc struct {
		Servers map[string]struct {
			Command  string   `json:"command"`
			Args     []string `json:"args"`
			Disabled bool     `json:"disabled"`
			Type     string   `json:"type"`
			Tools    []string `json:"tools"`
		} `json:"mcpServers"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return false
	}
	server, ok := doc.Servers[AmemServerName]
	if !ok || len(server.Args) != 1 || server.Args[0] != "mcp" || !sameExecutable(server.Command, amemPath) {
		return false
	}
	switch variant {
	case mcpVariantKiro:
		return !server.Disabled
	case mcpVariantCopilot:
		return server.Type == "local" && len(server.Tools) == 1 && server.Tools[0] == "*"
	}
	return true
}

// sameExecutable accepts an exact match, or two absolute paths that exist and
// name the same file.
func sameExecutable(configured, want string) bool {
	if configured == "" || want == "" {
		return false
	}
	if configured == want {
		return true
	}
	if !filepath.IsAbs(configured) || !filepath.IsAbs(want) {
		return false
	}
	a, errA := os.Stat(configured)
	b, errB := os.Stat(want)
	return errA == nil && errB == nil && !a.IsDir() && !b.IsDir() && os.SameFile(a, b)
}

// Status reads the integration without writing or starting anything.
func (m *AmemManager) Status(ctx context.Context, ssotPath string) AmemStatus {
	status := AmemStatus{
		KimiMCP:        hasAmemEntry(m.KimiMCPPath(), m.AmemPath, mcpVariantStandard),
		QwenMCP:        hasAmemEntry(m.QwenMCPPath(), m.AmemPath, mcpVariantStandard),
		AntigravityMCP: hasAmemEntry(m.AntigravityMCPPath(), m.AmemPath, mcpVariantStandard),
		KiroMCP:        hasAmemEntry(m.KiroMCPPath(), m.AmemPath, mcpVariantKiro),
		CopilotMCP:     hasAmemEntry(m.CopilotMCPPath(), m.AmemPath, mcpVariantCopilot),
		AmemPath:       m.AmemPath,
	}
	if info, err := os.Stat(m.PiAgentsPath()); err == nil && !info.IsDir() {
		status.PiAgents = true
	}
	status.InstructionsEnabled = HasMemoryInstructions(ssotPath)
	if m.AmemPath != "" {
		read := m.Version
		if read == nil {
			read = amemVersion
		}
		if version, err := read(ctx, m.AmemPath); err == nil {
			status.Version = version
		}
	}
	return status
}

// EnsureMemoryInstructions writes the amem block into the shared AGENTS SSOT
// and removes the claude-mem block, so a re-apply never leaves two. It reports
// whether the file changed. Rendering stays owned by AgentsManager.
func EnsureMemoryInstructions(ssotPath string) (bool, error) {
	raw, err := os.ReadFile(ssotPath)
	if err != nil {
		return false, fmt.Errorf("read agents SSOT: %w", err)
	}
	text := string(raw)
	next, err := patchMemoryInstructions(text)
	if err != nil {
		return false, err
	}
	if next == text {
		return false, nil
	}
	return true, atomicWriteFile(ssotPath, []byte(next), fileModeOrDefault(ssotPath, 0o644))
}

// MemoryInstructionsNeeded reports SSOT drift without writing.
func MemoryInstructionsNeeded(ssotPath string) (bool, error) {
	raw, err := os.ReadFile(ssotPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read agents SSOT: %w", err)
	}
	next, err := patchMemoryInstructions(string(raw))
	return next != string(raw), err
}

// patchMemoryInstructions removes every legacy claude-mem block and sets the
// amem block exactly once. Malformed or duplicate markers are an error rather
// than a silent merge.
func patchMemoryInstructions(content string) (string, error) {
	var err error
	content, err = removeBlock(content, legacyMemoryBlockStart, legacyMemoryBlockEnd)
	if err != nil {
		return "", err
	}
	starts, ends := strings.Count(content, memoryBlockStart), strings.Count(content, memoryBlockEnd)
	if starts == 0 && ends == 0 {
		return strings.TrimRight(content, "\n") + "\n\n" + memoryInstructions + "\n", nil
	}
	start, end := strings.Index(content, memoryBlockStart), strings.Index(content, memoryBlockEnd)
	if starts != 1 || ends != 1 || end < start {
		return "", errors.New("malformed or duplicate amem markers in agents SSOT; reconcile them before applying")
	}
	return content[:start] + memoryInstructions + content[end+len(memoryBlockEnd):], nil
}

// removeBlock deletes one marker-delimited block and the blank line before it.
func removeBlock(content, startMarker, endMarker string) (string, error) {
	starts, ends := strings.Count(content, startMarker), strings.Count(content, endMarker)
	if starts == 0 && ends == 0 {
		return content, nil
	}
	start, end := strings.Index(content, startMarker), strings.Index(content, endMarker)
	if starts != 1 || ends != 1 || end < start {
		return "", fmt.Errorf("malformed or duplicate %s markers in agents SSOT; reconcile them before applying", startMarker)
	}
	end += len(endMarker)
	head := strings.TrimRight(content[:start], "\n")
	tail := strings.TrimLeft(content[end:], "\n")
	switch {
	case head == "":
		return tail, nil
	case tail == "":
		return head + "\n", nil
	}
	return head + "\n\n" + tail, nil
}

func HasMemoryInstructions(path string) bool {
	raw, err := os.ReadFile(path)
	return err == nil && bytes.Contains(raw, []byte(memoryBlockStart)) && bytes.Contains(raw, []byte(memoryBlockEnd)) &&
		!bytes.Contains(raw, []byte(legacyMemoryBlockStart))
}

func fileModeOrDefault(path string, fallback fs.FileMode) fs.FileMode {
	if info, err := os.Stat(path); err == nil {
		return info.Mode().Perm()
	}
	return fallback
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

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// amemVersion runs `amem --version` with a short deadline. A failure leaves the
// version unknown; status reports that rather than guessing.
func amemVersion(ctx context.Context, amemPath string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, amemPath, "--version").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
