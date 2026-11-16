package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/aisettings"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

func newAIVaultCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vault",
		RunE:  runAIGroupHelp,
		Short: "Register vmem as the agents' vault server",
		Long: `Register vmem (@staix/vault-mem) as the agents' vault server for Kiro and
GitHub Copilot, and print the 'vmem setup' lines for Claude Code, Codex, Kimi,
Qwen, pi, jcode and Antigravity, which vmem registers itself.

dot runs no vault process and installs vmem only on request: install the exact
version yourself or with 'dot ai family install vmem', then run
'dot ai mcp install vmem'.`,
	}
	return cmd
}

func newVmemManagerFromCmd(cmd *cobra.Command) (*aisettings.VmemManager, error) {
	home := homeFromCmd(cmd)
	vmemPath, err := exec.LookPath("vmem")
	if err == nil {
		vmemPath, err = filepath.Abs(vmemPath)
	}
	if err != nil {
		vmemPath = ""
	}
	mgr := aisettings.NewVmemManager(home, vmemPath)
	selected, err := effectiveToolingForCmd(cmd)
	if err != nil {
		return nil, err
	}
	if selected != nil {
		mgr.SelectedAgents = append([]string{}, selected.Agents...)
	} else if path, _ := cmd.Flags().GetString("config"); path != "" {
		mgr.SelectedAgents = []string{}
	}
	override, _ := cmd.Flags().GetString("home")
	mgr.ExplicitHome = override != ""
	// The person's active Claude and Codex profiles, never adopted for an explicit --home.
	if override == "" {
		if dir := os.Getenv("CLAUDE_CONFIG_DIR"); filepath.IsAbs(dir) {
			mgr.ClaudeConfigDir = dir
		}
		if dir := os.Getenv("CODEX_HOME"); filepath.IsAbs(dir) {
			mgr.CodexHome = dir
		}
	}
	return mgr, nil
}

func newAIVaultInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Register vmem's MCP server for Kiro and Copilot",
		Long: `Add the vmem MCP server ('vmem mcp') to the Kiro and Copilot configs when
those agents are selected, write the vault block into the agents SSOT (after a
backup), and print the 'vmem setup' lines for the other agents. Every other
server and key is preserved. Re-running changes nothing.

vmem must already be installed (an @staix/vault-mem release inside the range
'dot ai mcp status vmem' names) and on PATH. With --dry-run nothing is written.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			mgr, err := newVmemManagerFromCmd(cmd)
			if err != nil {
				return err
			}
			if mgr.VmemPath == "" {
				return fmt.Errorf("vmem is not on PATH: install %s %s (for example `bun add -g %s`), then re-run", aisettings.VmemPackage, aisettings.VmemSupportedRange, aisettings.VmemPackage)
			}
			agents := newAgentsManagerFromCmd(cmd)
			install := mgr.Install
			if dryRun {
				install = mgr.Plan
			}
			result, err := install(agents)
			if err != nil {
				return err
			}
			p := printerFrom(cmd)
			p.Header("vmem Integration")
			p.KV("vmem", mgr.VmemPath)
			verb := "wrote"
			if dryRun {
				verb = "would write"
			}
			if len(result.ConfigPaths) == 0 && !result.Instructions {
				p.Line("Nothing to change: Kiro and Copilot already register vmem or are not selected, and the vault block is current")
			}
			for _, path := range result.ConfigPaths {
				p.Line("%s %s", verb, path)
			}
			if result.Instructions {
				p.Line("%s the vault block into %s", verb, agents.SSOTPath())
				if result.Backup != "" {
					p.Line("  backup: %s", result.Backup)
				}
			}
			p.Line("No process is started.")
			p.Section("Apply by hand (files dot does not own)")
			for _, line := range result.Manual {
				p.Line("  %s", line)
			}
			if !dryRun {
				auditAIEventBestEffort(cmd, "ai.vault.install", map[string]any{"config_paths": result.ConfigPaths, "instructions": result.Instructions})
			}
			return nil
		},
	}
}

func newAIVaultStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show vmem integration health",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			mgr, err := newVmemManagerFromCmd(cmd)
			if err != nil {
				return err
			}
			status := mgr.Status(cmd.Context(), newAgentsManagerFromCmd(cmd).SSOTPath())
			p := printerFrom(cmd)
			p.Header("vmem Status")
			printFamilyVersion(p, "vmem", status.VmemPath, status.Version, status.Installed(), status.Range(), status.Manifest.Supported != "")
			p.Section("Agents")
			for _, row := range []struct {
				label, id string
				ok        bool
			}{{"kiro", "kiro", status.KiroMCP}, {"copilot", "copilot", status.CopilotMCP}} {
				if !status.Selected[row.id] {
					p.Bullet(ui.StyleHint.Render(ui.MarkAbsent), fmt.Sprintf("%-13s %-12s %s", ui.StyleValue.Render(row.label), "not selected", "dot manages no config for it"))
					continue
				}
				printMemoryState(p, row.label, row.ok, "MCP")
			}
			p.Line("Claude Code, Codex, Kimi, Qwen, pi, jcode and Antigravity are registered by `vmem setup`; see `dot ai mcp install vmem` for the lines.")
			p.Section("Shared")
			printMemoryState(p, "instructions", status.InstructionsEnabled, "agents SSOT vault block")
			return nil
		},
	}
}
