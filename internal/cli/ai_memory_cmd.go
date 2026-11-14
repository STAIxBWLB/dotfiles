package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/aisettings"
	"github.com/STAIxBWLB/dotfiles/internal/toolversion"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

func newAIMemoryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "memory",
		RunE:  runAIGroupHelp,
		Short: "Audit and archive native agent memory",
		Long: `Register amem (@staix/agent-mem) as the project memory for Kiro and
GitHub Copilot, and print the 'amem setup' lines for Claude Code, Codex, Kimi,
Qwen, pi and Antigravity, which amem registers itself.

dot runs no memory process and installs amem only on request: install the exact
version yourself or with 'dot ai family install amem', then run 'dot ai mcp install amem'. amem has no resident worker, so dot
writes no LaunchAgent for it. Sessions of agents without hooks reach amem
through 'amem ingest <agent>', run at session end or on demand.

'dot ai memory audit' reports the size and drift of each agent's native memory
store, and 'dot ai memory archive' moves stale files out of the loaded layer
(a dry run unless --apply is given).`,
	}
	cmd.AddCommand(newAIMemoryAuditCmd())
	cmd.AddCommand(newAIMemoryArchiveCmd())
	return cmd
}

func newAmemManagerFromCmd(cmd *cobra.Command) (*aisettings.AmemManager, error) {
	home := homeFromCmd(cmd)
	amemPath, err := exec.LookPath("amem")
	if err == nil {
		amemPath, err = filepath.Abs(amemPath)
	}
	if err != nil {
		amemPath = ""
	}
	mgr := aisettings.NewAmemManager(home, amemPath)
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
	if override == "" {
		if adopted := os.Getenv("KIMI_CODE_HOME"); filepath.IsAbs(adopted) {
			mgr.KimiHome = adopted
		}
	}
	return mgr, nil
}

func newAIMemoryInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Register amem's MCP server for Kiro and Copilot",
		Long: `Add the amem MCP server ('amem mcp') to the Kiro and Copilot configs when
those agents are selected, and print the 'amem setup' lines for the other
agents. dot leaves the Kimi, Qwen and Antigravity configs untouched: 'amem setup'
owns them. Every other server and key is preserved. Re-running changes nothing.

amem must already be installed (an @staix/agent-mem release inside the range
'dot ai mcp status amem' names) and on PATH. With --dry-run nothing is written.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			mgr, err := newAmemManagerFromCmd(cmd)
			if err != nil {
				return err
			}
			if mgr.AmemPath == "" {
				return fmt.Errorf("amem is not on PATH: install %s %s (for example `bun add -g %s`), then re-run", aisettings.AmemPackage, aisettings.AmemSupportedRange, aisettings.AmemPackage)
			}
			p := printerFrom(cmd)
			install := mgr.Install
			if dryRun {
				install = mgr.Plan
			}
			result, err := install()
			if err != nil {
				return err
			}
			p.Header("amem Integration")
			p.KV("amem", mgr.AmemPath)
			verb := "wrote"
			if dryRun {
				verb = "would write"
			}
			if len(result.ConfigPaths) == 0 {
				p.Line("Nothing to %s: Kiro and Copilot already register amem or are not selected", map[bool]string{true: "write", false: "change"}[dryRun])
			}
			for _, path := range result.ConfigPaths {
				p.Line("%s %s", verb, path)
			}
			p.Line("No LaunchAgent is written and no process is started.")
			p.Section("Apply by hand (files dot does not own)")
			for _, line := range result.Manual {
				p.Line("  %s", line)
			}
			if !dryRun {
				auditAIEventBestEffort(cmd, "ai.memory.install", map[string]any{"config_paths": result.ConfigPaths})
			}
			return nil
		},
	}
}

func newAIMemoryStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show amem integration health",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			mgr, err := newAmemManagerFromCmd(cmd)
			if err != nil {
				return err
			}
			status := mgr.Status(cmd.Context(), newAgentsManagerFromCmd(cmd).SSOTPath())
			p := printerFrom(cmd)
			p.Header("amem Status")
			printFamilyVersion(p, "amem", status.AmemPath, status.Version, status.Installed(), status.Range(), status.Manifest.Supported != "")
			p.Section("Agents")
			for _, row := range []struct {
				label, id, detail string
				ok                bool
			}{
				{"kiro", "kiro", "MCP", status.KiroMCP},
				{"copilot", "copilot", "MCP", status.CopilotMCP},
				{"pi", "pi", "instructions; MCP and capture through `amem setup pi`", status.PiAgents},
			} {
				if !status.Selected[row.id] {
					p.Bullet(ui.StyleHint.Render(ui.MarkAbsent), fmt.Sprintf("%-13s %-12s %s", ui.StyleValue.Render(row.label), "not selected", "dot manages no config for it"))
					continue
				}
				printMemoryState(p, row.label, row.ok, row.detail)
			}
			for _, row := range [][2]string{{"kimi", "amem setup kimi"}, {"qwen", "amem setup qwen"}, {"agy", "amem setup agy, per repository"}} {
				p.Bullet(ui.StyleHint.Render(ui.MarkPartial), fmt.Sprintf("%-13s %-12s %s", ui.StyleValue.Render(row[0]), "amem setup", "managed by `"+row[1]+"`; dot writes no config"))
			}
			p.Section("Shared")
			printMemoryState(p, "instructions", status.InstructionsEnabled, "agents SSOT memory block")
			p.Line("Claude Code and Codex are registered by `amem setup`; see `dot ai mcp install amem` for the lines.")
			return nil
		},
	}
}

// printFamilyVersion prints the installed amem or vmem against its supported range, and warns only
// outside it. The range is the installed release's manifest, else dot's compiled fallback.
// raw is the `--version` output, shown when it names no stable release (a prerelease warns too).
func printFamilyVersion(p *Printer, name, path, raw, installed, supported string, fromManifest bool) {
	source := "compiled fallback"
	if fromManifest {
		source = "from its manifest"
	}
	shown := installed
	switch {
	case path == "":
		shown = "not found on PATH"
	case installed == "" && raw == "":
		shown = "version unreadable"
	case installed == "":
		shown = raw
	}
	p.KV(name, fmt.Sprintf("%s (supported %s, %s)", shown, supported, source))
	if path != "" && (installed != "" || raw != "") && !toolversion.Satisfies(supported, installed) {
		p.Warn("  installed %s %s is outside the supported range %s; install a stable release inside it", name, shown, supported)
	}
}

func printMemoryState(p *Printer, label string, ok bool, detail string) {
	marker := ui.StyleHint.Render(ui.MarkAbsent)
	state := "missing"
	if ok {
		marker = ui.StyleSuccess.Render(ui.MarkPresent)
		state = "ready"
	}
	p.Bullet(marker, fmt.Sprintf("%-13s %-7s %s", ui.StyleValue.Render(label), state, detail))
}
