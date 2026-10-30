package aisettings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// VmemPinnedVersion is the exact @staix/vault-mem release dot expects. dot never installs it: the person
// installs this exact version (`bun add -g @staix/vault-mem@0.1.0`) and `dot ai vault status` compares
// against it. The `vmem mcp` and `vmem setup <host>` forms printed here and the tool names in
// internal/aipolicy match the published 0.1.0 package.
const VmemPinnedVersion = "0.1.0"

// VmemServerName is the MCP server name vmem is registered under. Tool names reach agents as
// mcp__vmem__vmem_search and so on.
const VmemServerName = "vmem"

// ObsidianServerName is the retired generic Obsidian MCP server (@bitbonsai/mcpvault) vmem replaces.
const ObsidianServerName = "obsidian"

const (
	vaultBlockStart = "<!-- dotfiles:vmem:start -->"
	vaultBlockEnd   = "<!-- dotfiles:vmem:end -->"
)

// vaultInstructions is a short pointer, not the rule text: the vault rules live in the workspace.
const vaultInstructions = vaultBlockStart + `
## Vault

- The vault is read and written only through the ` + "`vmem`" + ` MCP server (vmem_search, vmem_read, vmem_create, ...). Do not edit vault notes as plain files or through another MCP server.
- vmem validates every write; a refusal names the rule to fix. vmem_move, vmem_delete, vmem_promote and vmem_promote_from_amem preview first and need confirm=true with the returned fingerprint.
` + vaultBlockEnd

// VmemInstallResult reports what Install wrote or would write.
type VmemInstallResult struct {
	// ConfigPaths are the MCP config files that changed (or would change).
	ConfigPaths []string
	// Instructions is true when the AGENTS SSOT vault block changed (or would change).
	Instructions bool
	// Backup is the SSOT copy taken before the vault block was written.
	Backup string
	// Manual holds the exact `vmem setup` lines for the clients vmem registers itself.
	Manual []string
}

// VmemStatus is the read-only health of the vmem integration.
type VmemStatus struct {
	KiroMCP             bool
	CopilotMCP          bool
	InstructionsEnabled bool
	Selected            map[string]bool
	VmemPath            string
	Version             string
	// ObsidianRegistered lists the MCP configs that still register the retired obsidian server.
	ObsidianRegistered []string
}

// VmemManager registers vmem's MCP stdio server for Kiro and Copilot, the agents `vmem setup` does not
// cover, and retires the obsidian server from the files dot owns. It never starts a process.
type VmemManager struct {
	HomeDir        string
	VmemPath       string
	SelectedAgents []string
	ExplicitHome   bool
	// ClaudeConfigDir is $CLAUDE_CONFIG_DIR when set (the active Claude profile).
	ClaudeConfigDir string
	// CodexHome is $CODEX_HOME when set.
	CodexHome string
	// Version reads the installed vmem version; nil runs `vmem --version`.
	Version func(ctx context.Context, vmemPath string) (string, error)
}

func NewVmemManager(homeDir, vmemPath string) *VmemManager {
	return &VmemManager{HomeDir: homeDir, VmemPath: vmemPath}
}

func (m *VmemManager) KiroMCPPath() string {
	return filepath.Join(m.HomeDir, ".kiro", "settings", "mcp.json")
}
func (m *VmemManager) CopilotMCPPath() string {
	return filepath.Join(m.HomeDir, ".copilot", "mcp-config.json")
}

// ClaudeConfigPath is the Claude Code user config holding mcpServers (owned by Claude Code, never written by dot).
func (m *VmemManager) ClaudeConfigPath() string {
	if filepath.IsAbs(m.ClaudeConfigDir) {
		return filepath.Join(m.ClaudeConfigDir, ".claude.json")
	}
	return filepath.Join(m.HomeDir, ".claude.json")
}

// CodexConfigPath is Codex's config.toml (owned by the person, never written by dot).
func (m *VmemManager) CodexConfigPath() string {
	if filepath.IsAbs(m.CodexHome) {
		return filepath.Join(m.CodexHome, "config.toml")
	}
	return filepath.Join(m.HomeDir, ".codex", "config.toml")
}

func (m *VmemManager) selected(agent string) bool {
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

// ownedTargets are the MCP configs dot writes for vmem and may retire obsidian from.
func (m *VmemManager) ownedTargets() []mcpTarget {
	return []mcpTarget{{"kiro", m.KiroMCPPath(), mcpVariantKiro}, {"copilot", m.CopilotMCPPath(), mcpVariantCopilot}}
}

// manualLines are the person's (or an agent's) steps for the hosts vmem registers itself.
func (m *VmemManager) manualLines() []string {
	return []string{
		"Claude Code, Codex, Kimi, Qwen, pi, jcode (MCP): vmem setup all --apply   # one host: vmem setup claude --apply; dry run without --apply",
		"Antigravity (per workspace): run `vmem setup agy --apply` inside the workspace",
		"Undo any of them: vmem setup <host> --undo",
		"Restart each host session so the new MCP registration loads",
	}
}

// Plan returns what Install would change, writing nothing.
func (m *VmemManager) Plan(agents *AgentsManager) (VmemInstallResult, error) {
	return m.install(agents, true)
}

// Install registers vmem for Kiro and Copilot when selected and writes the vault block into the agents SSOT
// (after the SSOT backup every SSOT edit takes). It returns the `vmem setup` lines for the rest.
func (m *VmemManager) Install(agents *AgentsManager) (VmemInstallResult, error) {
	return m.install(agents, false)
}

func (m *VmemManager) install(agents *AgentsManager, dryRun bool) (VmemInstallResult, error) {
	if m.VmemPath == "" || !filepath.IsAbs(m.VmemPath) {
		return VmemInstallResult{}, errors.New("vmem executable path must be absolute: install the exact @staix/vault-mem version first, then re-run")
	}
	if m.ExplicitHome && m.SelectedAgents == nil && !dryRun {
		return VmemInstallResult{}, errors.New("explicit --home requires an explicit agent selection")
	}
	result := VmemInstallResult{Manual: m.manualLines()}
	for _, target := range m.ownedTargets() {
		if !m.selected(target.agent) {
			continue
		}
		changed, err := ensureMCPServer(target.path, VmemServerName, mcpServerEntry(m.VmemPath, target.variant), dryRun)
		if err != nil {
			return result, err
		}
		if changed {
			result.ConfigPaths = append(result.ConfigPaths, target.path)
		}
	}
	if agents != nil {
		ssotPath := agents.SSOTPath()
		raw, err := os.ReadFile(ssotPath)
		switch {
		case os.IsNotExist(err):
		case err != nil:
			return result, fmt.Errorf("read agents SSOT: %w", err)
		default:
			next, perr := patchBlock(string(raw), vaultBlockStart, vaultBlockEnd, vaultInstructions)
			if perr != nil {
				return result, perr
			}
			if next != string(raw) {
				result.Instructions = true
				if !dryRun {
					backup, err := agents.backupSSOT()
					if err != nil {
						return result, fmt.Errorf("back up the agents SSOT before the vault block: %w", err)
					}
					result.Backup = backup
					if err := atomicWriteFile(ssotPath, []byte(next), fileModeOrDefault(ssotPath, 0o644)); err != nil {
						return result, err
					}
				}
			}
		}
	}
	return result, nil
}

// mcpServerEntry is `<command> mcp` in the agent's shape (kiro enabled, copilot local with all tools).
func mcpServerEntry(command string, variant mcpEntryVariant) map[string]any {
	entry := map[string]any{"command": command, "args": []string{"mcp"}}
	switch variant {
	case mcpVariantKiro:
		entry["disabled"] = false
	case mcpVariantCopilot:
		entry["type"] = "local"
		entry["tools"] = []string{"*"}
	}
	return entry
}

// readMCPServers parses a JSON MCP config into its top-level document and mcpServers map.
func readMCPServers(path string) (map[string]json.RawMessage, map[string]json.RawMessage, bool, error) {
	doc, servers := map[string]json.RawMessage{}, map[string]json.RawMessage{}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return doc, servers, false, nil
	}
	if err != nil {
		return nil, nil, false, err
	}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, nil, true, fmt.Errorf("parse MCP config %s: %w", path, err)
		}
		if doc == nil {
			doc = map[string]json.RawMessage{}
		}
	}
	if current := doc["mcpServers"]; len(current) > 0 && string(bytes.TrimSpace(current)) != "null" {
		if err := json.Unmarshal(current, &servers); err != nil {
			return nil, nil, true, fmt.Errorf("parse mcpServers in %s: %w", path, err)
		}
		if servers == nil {
			servers = map[string]json.RawMessage{}
		}
	}
	return doc, servers, true, nil
}

// ensureMCPServer merges one server entry, preserving every other server and key. It reports a change.
func ensureMCPServer(path, name string, entry map[string]any, dryRun bool) (bool, error) {
	doc, servers, _, err := readMCPServers(path)
	if err != nil {
		return false, err
	}
	entryRaw, _ := marshalNoEscape(entry)
	var existing map[string]any
	if json.Unmarshal(servers[name], &existing) == nil && existing != nil {
		if existingRaw, _ := marshalNoEscape(existing); bytes.Equal(existingRaw, entryRaw) {
			return false, nil
		}
	}
	if dryRun {
		return true, nil
	}
	servers[name] = entryRaw
	serversRaw, err := marshalNoEscape(servers)
	if err != nil {
		return false, err
	}
	doc["mcpServers"] = serversRaw
	out, err := marshalIndentNoEscape(doc)
	if err != nil {
		return false, err
	}
	return true, atomicWriteFile(path, out, fileModeOrDefault(path, 0o600))
}

// hasMCPServer reports whether a JSON config registers name as `<command> mcp` in the agent's shape.
func hasMCPServer(path, name, command string, variant mcpEntryVariant) bool {
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
	server, ok := doc.Servers[name]
	if !ok || len(server.Args) != 1 || server.Args[0] != "mcp" || !sameExecutable(server.Command, command) {
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

// codexRegistersObsidian reports whether a Codex config.toml has an [mcp_servers.obsidian] table (or a subtable).
func codexRegistersObsidian(path string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "[") {
			continue
		}
		end := strings.Index(line, "]")
		if end < 0 {
			continue
		}
		table := knowledgeTableName(strings.Trim(line[:end+1], "[]"))
		if table == "mcp_servers."+ObsidianServerName || strings.HasPrefix(table, "mcp_servers."+ObsidianServerName+".") {
			return true
		}
	}
	return false
}

// knowledgeTableName normalizes a TOML table header: trims spaces around dots and quotes on simple keys.
func knowledgeTableName(header string) string {
	parts := strings.Split(header, ".")
	for i, part := range parts {
		parts[i] = strings.Trim(strings.TrimSpace(part), `"'`)
	}
	return strings.Join(parts, ".")
}

// Status reads the integration without writing or starting anything.
func (m *VmemManager) Status(ctx context.Context, ssotPath string) VmemStatus {
	status := VmemStatus{
		KiroMCP:    hasMCPServer(m.KiroMCPPath(), VmemServerName, m.VmemPath, mcpVariantKiro),
		CopilotMCP: hasMCPServer(m.CopilotMCPPath(), VmemServerName, m.VmemPath, mcpVariantCopilot),
		VmemPath:   m.VmemPath,
		Selected:   map[string]bool{"kiro": m.selected("kiro"), "copilot": m.selected("copilot")},
	}
	raw, err := os.ReadFile(ssotPath)
	status.InstructionsEnabled = err == nil && bytes.Contains(raw, []byte(vaultBlockStart)) && bytes.Contains(raw, []byte(vaultBlockEnd))
	status.ObsidianRegistered = m.obsidianRegistrations()
	if m.VmemPath != "" {
		read := m.Version
		if read == nil {
			read = vmemVersion
		}
		if version, err := read(ctx, m.VmemPath); err == nil {
			status.Version = version
		}
	}
	return status
}

// obsidianRegistrations lists every known MCP config that still registers the retired obsidian server.
func (m *VmemManager) obsidianRegistrations() []string {
	var found []string
	for _, path := range []string{m.ClaudeConfigPath(), m.KiroMCPPath(), m.CopilotMCPPath()} {
		if state, _ := mcpServerState(path, ObsidianServerName); state == present {
			found = append(found, path)
		}
	}
	if codexRegistersObsidian(m.CodexConfigPath()) {
		found = append(found, m.CodexConfigPath())
	}
	return found
}

// RetireMcpvaultResult is what retire-mcpvault did or would do.
type RetireMcpvaultResult struct {
	Apply bool
	// Files are the dot-owned configs that register obsidian: removed with Apply, listed otherwise.
	Files []string
	// Removed are the files actually rewritten.
	Removed []string
	// Manual are the person-run lines for configs dot does not own.
	Manual []string
	// Remaining lists configs that still register obsidian after the run.
	Remaining []string
}

// RetireMcpvault removes mcpServers.obsidian from the configs dot owns (Kiro, Copilot) and prints the
// lines for the rest (Claude Code, Codex). It is a dry run unless apply, and refuses without vmem on PATH,
// so a vault is never left without an agent-facing server.
func (m *VmemManager) RetireMcpvault(apply bool) (RetireMcpvaultResult, error) {
	result := RetireMcpvaultResult{Apply: apply}
	if m.VmemPath == "" {
		return result, errors.New("vmem is not on PATH: install @staix/vault-mem " + VmemPinnedVersion + " and register it before retiring the obsidian server")
	}
	for _, target := range m.ownedTargets() {
		state, err := mcpServerState(target.path, ObsidianServerName)
		if err != nil {
			return result, fmt.Errorf("%s: %w", target.path, err)
		}
		if state != present {
			continue
		}
		result.Files = append(result.Files, target.path)
		if apply {
			if err := removeMCPServer(target.path, ObsidianServerName); err != nil {
				return result, err
			}
			result.Removed = append(result.Removed, target.path)
		}
	}
	if state, _ := mcpServerState(m.ClaudeConfigPath(), ObsidianServerName); state == present {
		result.Manual = append(result.Manual, "Claude Code ("+m.ClaudeConfigPath()+"): claude mcp remove obsidian --scope user")
	}
	if codexRegistersObsidian(m.CodexConfigPath()) {
		result.Manual = append(result.Manual, "Codex ("+m.CodexConfigPath()+"): delete the [mcp_servers.obsidian] table and every [mcp_servers.obsidian.*] table")
	}
	result.Remaining = m.obsidianRegistrations()
	return result, nil
}

func vmemVersion(ctx context.Context, vmemPath string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, vmemPath, "--version").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// patchBlock sets one marker-delimited block exactly once; malformed or duplicate markers are an error.
func patchBlock(content, startMarker, endMarker, block string) (string, error) {
	starts, ends := strings.Count(content, startMarker), strings.Count(content, endMarker)
	if starts == 0 && ends == 0 {
		return strings.TrimRight(content, "\n") + "\n\n" + block + "\n", nil
	}
	start, end := strings.Index(content, startMarker), strings.Index(content, endMarker)
	if starts != 1 || ends != 1 || end < start {
		return "", fmt.Errorf("malformed or duplicate %s markers in agents SSOT; reconcile them before applying", startMarker)
	}
	return content[:start] + block + content[end+len(endMarker):], nil
}
