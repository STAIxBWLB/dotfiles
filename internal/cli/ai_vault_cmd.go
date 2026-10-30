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
		Short: "Register vmem as the vault writer and retire the Obsidian MCP server",
		Long: `Register vmem (@staix/vault-mem) as the agents' vault server for Kiro and
GitHub Copilot, and print the 'vmem setup' lines for Claude Code, Codex, Kimi,
Qwen, pi, jcode and Antigravity, which vmem registers itself.

dot never installs vmem and runs no vault process: install the exact version
yourself, then run 'dot ai vault install'.

'dot ai vault retire-mcpvault' removes the generic Obsidian MCP server
("obsidian", @bitbonsai/mcpvault) from the configs dot owns and prints the
lines for the rest. It is a dry run unless --apply is given.`,
	}
	cmd.AddCommand(newAIVaultInstallCmd())
	cmd.AddCommand(newAIVaultStatusCmd())
	cmd.AddCommand(newAIVaultRetireCmd())
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

vmem must already be installed (the exact @staix/vault-mem version, which
'dot ai vault status' names) and on PATH. With --dry-run nothing is written.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			mgr, err := newVmemManagerFromCmd(cmd)
			if err != nil {
				return err
			}
			if mgr.VmemPath == "" {
				return fmt.Errorf("vmem is not on PATH: install @staix/vault-mem %s exactly (for example `bun add -g @staix/vault-mem@%s`), then re-run", aisettings.VmemPinnedVersion, aisettings.VmemPinnedVersion)
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
			version := status.Version
			switch {
			case status.VmemPath == "":
				version = "not found on PATH"
			case version == "":
				version = "version unreadable"
			}
			p.KV("vmem", version+" (expected "+aisettings.VmemPinnedVersion+")")
			if status.VmemPath != "" && status.Version != "" && !containsVersion(status.Version, aisettings.VmemPinnedVersion) {
				p.Warn("  installed vmem is not the pinned version %s; install it exactly", aisettings.VmemPinnedVersion)
			}
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
			p.Line("Claude Code, Codex, Kimi, Qwen, pi, jcode and Antigravity are registered by `vmem setup`; see `dot ai vault install` for the lines.")
			p.Section("Shared")
			printMemoryState(p, "instructions", status.InstructionsEnabled, "agents SSOT vault block")
			if len(status.ObsidianRegistered) > 0 {
				p.Section("Retired server still registered")
				for _, path := range status.ObsidianRegistered {
					p.Warn("  obsidian (@bitbonsai/mcpvault) is still registered in %s; run `dot ai vault retire-mcpvault`", path)
				}
			}
			return nil
		},
	}
}

func newAIVaultRetireCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "retire-mcpvault",
		Short: "Remove the Obsidian MCP server (dry run unless --apply)",
		Long: `Retire the generic Obsidian MCP server ("obsidian", @bitbonsai/mcpvault)
after vmem is registered.

Without --apply this lists every file it would change and changes nothing.
With --apply it removes mcpServers.obsidian from the Kiro and Copilot configs,
the files dot owns, for the selected agents only, and only from a config that
already registers a working vmem entry; any other is skipped and reported. It never edits Claude Code's or Codex's config: the exact
lines to run or delete are printed instead. It refuses while vmem is not on
PATH, so the vault is never left without an agent-facing server.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			apply, _ := cmd.Flags().GetBool("apply")
			if dry, _ := cmd.Flags().GetBool("dry-run"); dry {
				apply = false
			}
			mgr, err := newVmemManagerFromCmd(cmd)
			if err != nil {
				return err
			}
			result, err := mgr.RetireMcpvault(apply)
			if err != nil {
				return err
			}
			p := printerFrom(cmd)
			if apply {
				p.Header("Retire the Obsidian MCP server")
			} else {
				p.Header("Retire the Obsidian MCP server (dry run: nothing is changed)")
			}
			if len(result.Files) == 0 {
				p.Line("No file dot owns registers obsidian.")
			}
			for _, path := range result.Files {
				if apply {
					p.Line("removed mcpServers.obsidian from %s", path)
				} else {
					p.Line("would remove mcpServers.obsidian from %s", path)
				}
			}
			if len(result.Skipped) > 0 {
				p.Section("Skipped (no working vmem entry yet)")
				for _, line := range result.Skipped {
					p.Warn("  %s", line)
				}
			}
			if len(result.Manual) > 0 {
				p.Section("Run or edit by hand (dot never writes these files)")
				for _, line := range result.Manual {
					p.Line("  %s", line)
				}
			}
			if apply {
				auditAIEventBestEffort(cmd, "ai.vault.retire", map[string]any{"removed": result.Removed, "remaining": len(result.Remaining)})
			}
			return nil
		},
	}
	c.Flags().Bool("apply", false, "Remove the obsidian server from the files dot owns (default: dry run)")
	return c
}
