package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/aisettings"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

func newAIMemoryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "memory",
		Short: "Register amem for the agents and retire claude-mem",
		Long: `Register amem (@staix/agent-mem) as the project memory for Kiro and
GitHub Copilot, and print the 'amem setup' lines for Claude Code, Codex, Kimi,
Qwen, pi and Antigravity, which amem registers itself.

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
		Short: "Register amem's MCP server for Kiro and Copilot",
		Long: `Add the amem MCP server ('amem mcp') to the Kiro and Copilot configs when
those agents are selected, and print the 'amem setup' lines for the other
agents. dot leaves the Kimi, Qwen and Antigravity configs untouched: 'amem setup'
owns them. Every other server and key is preserved. Re-running changes nothing.

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
			p.Line("Claude Code and Codex are registered by `amem setup`; see `dot ai memory install` for the lines.")
			return nil
		},
	}
}

// containsVersion reports whether output names exactly the version want. A digit or a dot next to the
// number means it is part of a longer version (10.11.0, 0.11.01, 0.11.0.1, 1.0.11.0), which is not the pin.
// A trailing dot that ends a sentence ("amem 0.11.0.") is allowed only when no digit follows it, and a
// prerelease or build suffix (-rc1, +build) is not the release.
func containsVersion(output, want string) bool {
	isDigit := func(b byte) bool { return b >= '0' && b <= '9' }
	for i := 0; i+len(want) <= len(output); i++ {
		if output[i:i+len(want)] != want {
			continue
		}
		end := i + len(want)
		if i > 0 && (isDigit(output[i-1]) || output[i-1] == '.') {
			continue
		}
		if end < len(output) && isDigit(output[end]) {
			continue
		}
		if end+1 < len(output) && output[end] == '.' && isDigit(output[end+1]) {
			continue
		}
		// 0.11.0-rc1 and 0.11.0+build are prereleases or builds of the pin, not the release itself.
		if end+1 < len(output) && (output[end] == '-' || output[end] == '+') && (isDigit(output[end+1]) || output[end+1] >= 'a' && output[end+1] <= 'z' || output[end+1] >= 'A' && output[end+1] <= 'Z') {
			continue
		}
		return true
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

Claude Code is handled in its active profile: $CLAUDE_CONFIG_DIR when set,
else ~/.claude. The dry run names that profile, the files to back up before
--apply, and any claude-mem left in the default ~/.claude while another
profile is active (that profile is never changed; run again with
CLAUDE_CONFIG_DIR unset to retire it). It never edits the profile's
settings.json, which Claude Code owns: the exact lines to remove are printed
for you. --apply refuses unless --import-complete says
the 'amem import claude-mem' run is done and an amem import cursor exists.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			apply, _ := cmd.Flags().GetBool("apply")
			imported, _ := cmd.Flags().GetBool("import-complete")
			if dry, _ := cmd.Flags().GetBool("dry-run"); dry {
				apply = false
			}
			over, _ := cmd.Flags().GetString("home")
			opts := aisettings.RetireOptions{HomeDir: homeFromCmd(cmd), ExplicitHome: over != "", Apply: apply, ImportConfirmed: imported}
			if adopted := os.Getenv("KIMI_CODE_HOME"); filepath.IsAbs(adopted) && over == "" {
				opts.KimiHome = adopted
			}
			result, err := aisettings.RetireClaudeMem(cmd.Context(), opts)
			if err != nil {
				return err
			}
			printRetireResult(printerFrom(cmd), result)
			if apply && result.Blocked == "" {
				auditAIEventBestEffort(cmd, "ai.memory.retire", map[string]any{"archive": result.Archive, "clean": result.Clean(), "failed_steps": len(result.Failed())})
			}
			if result.Blocked != "" {
				return fmt.Errorf("%s", result.Blocked)
			}
			if apply {
				if failed := result.Failed(); len(failed) > 0 {
					names := make([]string, len(failed))
					for i, step := range failed {
						names[i] = step.ID
					}
					return fmt.Errorf("%d retirement step(s) failed (%s); see the steps above", len(failed), strings.Join(names, ", "))
				}
				if !result.Clean() {
					return fmt.Errorf("claude-mem residue remains; see the read-back above")
				}
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
	source := "default"
	if r.ClaudeProfileFromEnv {
		source = "CLAUDE_CONFIG_DIR"
	}
	p.Line("Claude profile: %s (%s)", r.ClaudeProfile, source)
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
	if len(r.Backup) > 0 && (!r.Apply || r.Blocked != "") {
		p.Section("Back up before --apply (the run or your edits rewrite these)")
		for _, path := range r.Backup {
			p.Line("  %s", path)
		}
	}
	if len(r.OtherProfile) > 0 {
		p.Section("Default Claude profile (not changed by this run; retire it with `env -u CLAUDE_CONFIG_DIR dot ai memory retire-claude-mem`)")
		for _, line := range r.OtherProfile {
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
