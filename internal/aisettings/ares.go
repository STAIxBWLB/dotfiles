package aisettings

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/STAIxBWLB/dotfiles/internal/toolversion"
)

// AresPinnedVersion is the oldest @staix/agent-research release dot supports. dot never installs it
// outside `dot ai family install ares`. The `ares mcp` and `ares setup <host>` forms printed here match
// the 0.1.0 release (tag v0.1.0). It is only the fallback floor: the version and the tool list come
// from the installed release's manifest (`ares manifest --json`), so a new ares release needs no dot
// release.
const AresPinnedVersion = "0.1.0"

// AresSupportedRange is the compiled fallback range, used when the installed ares publishes none.
const AresSupportedRange = ">=" + AresPinnedVersion + " <1.0.0"

// AresPackage is the npm package ares is published as.
const AresPackage = "@staix/agent-research"

// AresServerName is the MCP server name ares is registered under. Tool names reach agents as
// mcp__ares__ares_status and so on.
const AresServerName = "ares"

const (
	researchBlockStart = "<!-- dotfiles:ares:start -->"
	researchBlockEnd   = "<!-- dotfiles:ares:end -->"
)

// researchInstructions is a short pointer, not the research rules: ares serves its own playbook.
const researchInstructions = researchBlockStart + `
## Research

- Literature studies, experiments and their gates run through the ` + "`ares`" + ` MCP tools (ares_status, ares_lit_search, ...); read ares_playbook first.
- Tools that reach the network, an executor or a container (ares_lit_search, ares_lit_verify, ares_attempt_run, ares_executor_check, ...) are never auto-approved; each call needs approval.
- Gate approvals (ares_gate approve) are the person's decision, not an agent's.
` + researchBlockEnd

// AresInstallResult reports what Install wrote or would write.
type AresInstallResult struct {
	// ConfigPaths are the MCP config files that changed (or would change).
	ConfigPaths []string
	// Instructions is true when the AGENTS SSOT research block changed (or would change).
	Instructions bool
	// Backup is the SSOT copy taken before the research block was written.
	Backup string
	// Manual holds the exact `ares setup` lines for the clients ares registers itself.
	Manual []string
}

// AresStatus is the read-only health of the ares integration.
type AresStatus struct {
	KiroMCP             bool
	CopilotMCP          bool
	InstructionsEnabled bool
	Selected            map[string]bool
	AresPath            string
	Version             string
	// Manifest is what the installed ares publishes about itself; zero when it publishes nothing.
	Manifest toolversion.Manifest
}

// Installed is the installed ares version: the manifest's, else the one in `ares --version`.
func (s AresStatus) Installed() string { return installedVersion(s.Manifest, s.Version) }

// Range is the supported range: the manifest's when it publishes one, else AresSupportedRange.
func (s AresStatus) Range() string { return supportedRange(s.Manifest, AresSupportedRange) }

// AresManager registers ares's MCP stdio server for Kiro and Copilot, the agents `ares setup` does not
// cover. It never starts a process.
type AresManager struct {
	HomeDir        string
	AresPath       string
	SelectedAgents []string
	ExplicitHome   bool
	// Version reads the installed ares version; nil runs `ares --version`.
	Version func(ctx context.Context, aresPath string) (string, error)
	// Manifest reads what the installed ares publishes; nil runs toolversion.Read.
	Manifest func(ctx context.Context, aresPath string) toolversion.Manifest
}

func NewAresManager(homeDir, aresPath string) *AresManager {
	return &AresManager{HomeDir: homeDir, AresPath: aresPath}
}

func (m *AresManager) KiroMCPPath() string {
	return filepath.Join(m.HomeDir, ".kiro", "settings", "mcp.json")
}
func (m *AresManager) CopilotMCPPath() string {
	return filepath.Join(m.HomeDir, ".copilot", "mcp-config.json")
}

func (m *AresManager) selected(agent string) bool {
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

func (m *AresManager) ownedTargets() []mcpTarget {
	return []mcpTarget{{"kiro", m.KiroMCPPath(), mcpVariantKiro}, {"copilot", m.CopilotMCPPath(), mcpVariantCopilot}}
}

// manualLines are the person's (or an agent's) steps for the hosts ares registers itself.
func (m *AresManager) manualLines() []string {
	return []string{
		"Claude Code, Codex, Kimi, Qwen, pi, jcode (MCP): ares setup all --apply   # one host: ares setup claude --apply; dry run without --apply",
		"Antigravity (per workspace): run `ares setup agy --apply` inside the workspace",
		"Undo any of them: ares setup <host> --undo",
		"Restart each host session so the new MCP registration loads",
	}
}

// Plan returns what Install would change, writing nothing.
func (m *AresManager) Plan(agents *AgentsManager) (AresInstallResult, error) {
	return m.install(agents, true)
}

// Install registers ares for Kiro and Copilot when selected and writes the research block into the
// agents SSOT. It returns the `ares setup` lines for the rest.
func (m *AresManager) Install(agents *AgentsManager) (AresInstallResult, error) {
	return m.install(agents, false)
}

func (m *AresManager) install(agents *AgentsManager, dryRun bool) (AresInstallResult, error) {
	if m.AresPath == "" || !filepath.IsAbs(m.AresPath) {
		return AresInstallResult{}, errors.New("ares executable path must be absolute: install " + AresPackage + " first, then re-run")
	}
	if m.ExplicitHome && m.SelectedAgents == nil && !dryRun {
		return AresInstallResult{}, errors.New("explicit --home requires an explicit agent selection")
	}
	result := AresInstallResult{Manual: m.manualLines()}
	var err error
	result.ConfigPaths, result.Instructions, result.Backup, err = registerMCPAndBlock(m.ownedTargets(), m.selected, AresServerName, m.AresPath, agents, researchBlockStart, researchBlockEnd, researchInstructions, dryRun)
	return result, err
}

// Status reads the integration without writing or starting anything but `ares --version` and
// `ares manifest --json`.
func (m *AresManager) Status(ctx context.Context, ssotPath string) AresStatus {
	status := AresStatus{
		KiroMCP:    hasMCPServer(m.KiroMCPPath(), AresServerName, m.AresPath, mcpVariantKiro),
		CopilotMCP: hasMCPServer(m.CopilotMCPPath(), AresServerName, m.AresPath, mcpVariantCopilot),
		AresPath:   m.AresPath,
		Selected:   map[string]bool{"kiro": m.selected("kiro"), "copilot": m.selected("copilot")},
	}
	raw, err := os.ReadFile(ssotPath)
	status.InstructionsEnabled = err == nil && bytes.Contains(raw, []byte(researchBlockStart)) && bytes.Contains(raw, []byte(researchBlockEnd))
	if m.AresPath == "" {
		return status
	}
	read := m.Version
	if read == nil {
		read = commandVersion
	}
	if version, err := read(ctx, m.AresPath); err == nil {
		status.Version = version
	}
	manifest := m.Manifest
	if manifest == nil {
		manifest = func(ctx context.Context, path string) toolversion.Manifest {
			return toolversion.Read(ctx, path, AresPackage)
		}
	}
	status.Manifest = manifest(ctx, m.AresPath)
	return status
}
