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

func newAIMemoryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "memory",
		Short: "Register amem for the agents and retire claude-mem",
		Long: `Register amem (@staix/agent-mem) as the project memory for Kimi, Qwen,
Antigravity, Kiro and GitHub Copilot, and print the exact lines for Claude Code
and Codex, which keep their own plugin and MCP state.

dot never installs amem and runs no memory process: install the exact version
yourself, then run 'dot ai memory install'. amem has no resident worker, so dot
writes no LaunchAgent for it. Sessions of agents without hooks reach amem
through 'amem ingest <agent>', run at session end or on demand.

'dot ai memory retire-claude-mem' removes the old claude-mem integration. It is
a dry run unless --apply is given.`,
	}
	cmd.AddCommand(newAIMemoryInstallCmd())
	cmd.AddCommand(newAIMemoryStatusCmd())
	cmd.AddCommand(newAIMemoryRetireCmd())
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
		Short: "Register amem's MCP server for the selected agents",
		Long: `Add the amem MCP server ('amem mcp') to the config of each selected agent whose
MCP file dot owns, and print the lines for Claude Code and Codex. Every other
server and key in those files is preserved. Re-running changes nothing.

amem must already be installed (the exact @staix/agent-mem version, which
'dot ai memory status' names) and on PATH. With --dry-run nothing is written.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			mgr, err := newAmemManagerFromCmd(cmd)
			if err != nil {
				return err
			}
			if mgr.AmemPath == "" {
				return fmt.Errorf("amem is not on PATH: install @staix/agent-mem %s exactly (for example `bun add -g @staix/agent-mem@%s`), then re-run", aisettings.AmemPinnedVersion, aisettings.AmemPinnedVersion)
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
				p.Line("MCP configs already register amem; nothing to %s", map[bool]string{true: "write", false: "change"}[dryRun])
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
			version := status.Version
			switch {
			case status.AmemPath == "":
				version = "not found on PATH"
			case version == "":
				version = "version unreadable"
			}
			p.KV("amem", version+" (expected "+aisettings.AmemPinnedVersion+")")
			if status.AmemPath != "" && status.Version != "" && !containsVersion(status.Version, aisettings.AmemPinnedVersion) {
				p.Warn("  installed amem is not the pinned version %s; install it exactly", aisettings.AmemPinnedVersion)
			}
			p.Section("Agents")
			printMemoryState(p, "kimi", status.KimiMCP, "MCP")
			printMemoryState(p, "qwen", status.QwenMCP, "MCP")
			printMemoryState(p, "agy", status.AntigravityMCP, "MCP")
			printMemoryState(p, "kiro", status.KiroMCP, "MCP")
			printMemoryState(p, "copilot", status.CopilotMCP, "MCP")
			printMemoryState(p, "pi", status.PiAgents, "instructions; capture through `amem ingest pi` (pi has no MCP)")
			p.Section("Shared")
			printMemoryState(p, "instructions", status.InstructionsEnabled, "agents SSOT memory block")
			p.Line("Claude Code and Codex keep their own plugin and MCP state; see `dot ai memory install` for the lines.")
			return nil
		},
	}
}

func containsVersion(output, want string) bool {
	for i := 0; i+len(want) <= len(output); i++ {
		if output[i:i+len(want)] == want {
			before := i == 0 || output[i-1] < '0' || output[i-1] > '9'
			after := i+len(want) == len(output) || output[i+len(want)] < '0' || output[i+len(want)] > '9'
			if before && after {
				return true
			}
		}
	}
	return false
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

func newAIMemoryRetireCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "retire-claude-mem",
		Short: "Remove the claude-mem integration (dry run unless --apply)",
		Long: `Retire claude-mem after its notes were imported into amem.

Without --apply this only lists what would be touched and reads back what is
left on the machine; it changes nothing. With --apply, in order, it:

  1. disables and uninstalls the claude-mem plugin for Claude Code and Codex,
     and removes their claude-mem marketplaces (through the native CLIs);
  2. removes the claude-mem entry from the MCP configs of Kimi, Qwen,
     Antigravity, Kiro and Copilot;
  3. boots out and removes the two dotfiles LaunchAgents;
  4. moves ~/.claude-mem to ~/.claude-mem.archive-<date> (never deletes it,
     and refuses while a claude-mem process runs);
  5. reads back that no claude-mem process, port, hook or LaunchAgent remains.

It never edits ~/.claude/settings.json, which Claude Code owns: the exact lines
to remove are printed for you. --apply refuses unless --import-complete says
the 'amem import claude-mem' run is done and an amem import cursor exists.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			apply, _ := cmd.Flags().GetBool("apply")
			imported, _ := cmd.Flags().GetBool("import-complete")
			if dry, _ := cmd.Flags().GetBool("dry-run"); dry {
				apply = false
			}
			opts := aisettings.RetireOptions{HomeDir: homeFromCmd(cmd), Apply: apply, ImportConfirmed: imported}
			if adopted := os.Getenv("KIMI_CODE_HOME"); filepath.IsAbs(adopted) {
				if over, _ := cmd.Flags().GetString("home"); over == "" {
					opts.KimiHome = adopted
				}
			}
			result, err := aisettings.RetireClaudeMem(cmd.Context(), opts)
			if err != nil {
				return err
			}
			printRetireResult(printerFrom(cmd), result)
			if apply && result.Blocked == "" {
				auditAIEventBestEffort(cmd, "ai.memory.retire", map[string]any{"archive": result.Archive, "clean": result.Clean()})
			}
			if result.Blocked != "" {
				return fmt.Errorf("%s", result.Blocked)
			}
			if apply && !result.Clean() {
				return fmt.Errorf("claude-mem residue remains; see the read-back above")
			}
			return nil
		},
	}
	c.Flags().Bool("apply", false, "Run the retirement (default: dry run)")
	c.Flags().Bool("import-complete", false, "Confirm that `amem import claude-mem` has finished")
	return c
}

func printRetireResult(p *Printer, r aisettings.RetireResult) {
	if r.Apply && r.Blocked == "" {
		p.Header("Retire claude-mem")
	} else {
		p.Header("Retire claude-mem (dry run: nothing is changed)")
	}
	p.Section("Steps")
	for _, step := range r.Steps {
		p.Line("  [%-6s] %-24s %s", step.Status, step.ID, step.Action)
		if step.Detail != "" && step.Status != aisettings.RetireWould && step.Status != aisettings.RetireAbsent {
			p.Line("           %s", step.Detail)
		}
	}
	if len(r.Manual) > 0 {
		p.Section("Edit by hand (Claude Code owns this file; dot never writes it)")
		for _, line := range r.Manual {
			p.Line("  %s", line)
		}
	}
	p.Section("Read-back")
	for _, item := range r.ReadBack {
		if item.Clean {
			p.Line("  [clean ] %s", item.Check)
		} else {
			p.Line("  [FOUND ] %s: %s", item.Check, item.Found)
		}
	}
	if r.Archive != "" {
		p.Line("Archive: %s (kept; delete it yourself when you are sure)", r.Archive)
	}
}
