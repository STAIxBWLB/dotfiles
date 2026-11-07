package cli

import (
	"fmt"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/aisettings"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

func newAIResearchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "research",
		Short: "Register ares, the research engine, for the agents dot owns",
		Long: `Register ares (@staix/agent-research) as the agents' research MCP server for
Kiro and GitHub Copilot, and print the 'ares setup' lines for Claude Code,
Codex, Kimi, Qwen, pi, jcode and Antigravity, which ares registers itself.

dot runs no research process and installs ares only on request: install it
with 'dot ai family install ares' (or 'bun add -g @staix/agent-research'),
then run 'dot ai research install'.

The AI policy auto-approves ares's read tools only. Tools that write a study,
decide a gate, reach the network or start or read a container keep their
approval prompt.`,
	}
	cmd.AddCommand(newAIResearchInstallCmd())
	cmd.AddCommand(newAIResearchStatusCmd())
	return cmd
}

func newAresManagerFromCmd(cmd *cobra.Command) (*aisettings.AresManager, error) {
	aresPath, err := exec.LookPath("ares")
	if err == nil {
		aresPath, err = filepath.Abs(aresPath)
	}
	if err != nil {
		aresPath = ""
	}
	mgr := aisettings.NewAresManager(homeFromCmd(cmd), aresPath)
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
	return mgr, nil
}

func newAIResearchInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Register ares's MCP server for Kiro and Copilot",
		Long: `Add the ares MCP server ('ares mcp') to the Kiro and Copilot configs when
those agents are selected, write the research block into the agents SSOT
(after a backup), and print the 'ares setup' lines for the other agents. Every
other server and key is preserved. Re-running changes nothing.

ares must already be installed (an @staix/agent-research release inside the
range 'dot ai research status' names) and on PATH. With --dry-run nothing is
written and every file it would change is listed.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			mgr, err := newAresManagerFromCmd(cmd)
			if err != nil {
				return err
			}
			if mgr.AresPath == "" {
				return fmt.Errorf("ares is not on PATH: install %s %s (for example `dot ai family install ares`), then re-run", aisettings.AresPackage, aisettings.AresSupportedRange)
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
			p.Header("ares Integration")
			p.KV("ares", mgr.AresPath)
			verb := "wrote"
			if dryRun {
				verb = "would write"
			}
			if len(result.ConfigPaths) == 0 && !result.Instructions {
				p.Line("Nothing to change: Kiro and Copilot already register ares or are not selected, and the research block is current")
			}
			for _, path := range result.ConfigPaths {
				p.Line("%s %s", verb, path)
			}
			if result.Instructions {
				p.Line("%s the research block into %s", verb, agents.SSOTPath())
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
				auditAIEventBestEffort(cmd, "ai.research.install", map[string]any{"config_paths": result.ConfigPaths, "instructions": result.Instructions})
			}
			return nil
		},
	}
}

func newAIResearchStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show ares integration health",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			mgr, err := newAresManagerFromCmd(cmd)
			if err != nil {
				return err
			}
			status := mgr.Status(cmd.Context(), newAgentsManagerFromCmd(cmd).SSOTPath())
			p := printerFrom(cmd)
			p.Header("ares Status")
			printFamilyVersion(p, "ares", status.AresPath, status.Version, status.Installed(), status.Range(), status.Manifest.Supported != "")
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
			p.Line("Claude Code, Codex, Kimi, Qwen, pi, jcode and Antigravity are registered by `ares setup`; see `dot ai research install` for the lines.")
			p.Section("Shared")
			printMemoryState(p, "instructions", status.InstructionsEnabled, "agents SSOT research block")
			return nil
		},
	}
}
