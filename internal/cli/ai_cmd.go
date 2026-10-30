package cli

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/aisettings"
	"github.com/STAIxBWLB/dotfiles/internal/config/catalog"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

func newAICmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ai",
		Short: "AI CLI/config helpers and settings backup/restore",
		Long: `Manage portable AI assistant configuration.

The ai module writes shell/config helper files. It does not install Claude,
Codex, Antigravity, or ChatGPT apps; use 'dot apps install' for Homebrew casks.

With --include-auth, the snapshot carries each tool's credential file
(.codex/auth.json, .kimi-code/credentials, .pi/agent/auth.json,
.gemini/oauth_creds.json). pi sessions, extensions, and skills stay out:
they are machine state pi rebuilds itself.`,
	}
	cmd.AddCommand(newAIHandoffCmd())
	cmd.AddCommand(newAISetupCmd())
	cmd.AddCommand(newAIToolsCmd())
	cmd.AddCommand(newAIRunCmd())
	cmd.AddCommand(newAIPolicyCmd())
	cmd.AddCommand(newAISessionCmd())
	cmd.AddCommand(newAIListCmd())
	cmd.AddCommand(newAIStatusCmd())
	cmd.AddCommand(newAIBackupCmd())
	cmd.AddCommand(newAIRestoreCmd())
	cmd.AddCommand(newAIExportCmd())
	cmd.AddCommand(newAIImportCmd())
	cmd.AddCommand(newAIPruneCmd())
	cmd.AddCommand(newAIHudCmd())
	cmd.AddCommand(newAICoauthoredGuardCmd())
	cmd.AddCommand(newAIAgentsCmd())
	cmd.AddCommand(newAISkillsCmd())
	cmd.AddCommand(newAIMemoryCmd())
	cmd.AddCommand(newAIVaultCmd())
	cmd.AddCommand(newAIUpdateCmd())
	cmd.AddCommand(newAIAuthCmd())
	cmd.AddCommand(newAIAuditCmd())
	return cmd
}

func newAIListCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "list",
		Short: "List AI helpers, detected CLIs, and managed paths",
		Args:  cobra.NoArgs,
		RunE:  runAIList,
	}
	c.Flags().Bool("include-auth", false, "Show auth/local-secret paths that are excluded by default")
	return c
}

// extraDetectedCLIs are probed by `dot ai list` but are not `dot ai update`
// phases: agy has no self-update path dot drives, and gh/fabric are helpers
// the 30-ai.sh aliases depend on.
var extraDetectedCLIs = []string{"gh", "fabric"}

// detectedCLINames derives the probe list from the update phases so a binary
// rename in updateToolBinary cannot leave this list silently reporting
// "(not found)".
func detectedCLINames() []string {
	return append([]string{"claude", "codex", "kimi", "qwen", "grok", "opencode", "gencode", "pi", "agy", "copilot", "gemini", "kiro-cli", "cursor-agent"}, extraDetectedCLIs...)
}

func runAIList(cmd *cobra.Command, _ []string) error {
	includeAuth, _ := cmd.Flags().GetBool("include-auth")
	p := printerFrom(cmd)
	p.Header("AI Helpers")
	p.Section("Module")
	p.KV("Name", "ai")
	p.KV("Writes", "~/.config/shell/30-ai.sh, ~/.config/claude/settings.json")
	p.KV("Installs apps", "no — use `dot apps install`")

	p.Section("Detected CLI tools")
	for _, name := range detectedCLINames() {
		path, err := exec.LookPath(name)
		marker := ui.StyleHint.Render(ui.MarkAbsent)
		value := "(not found)"
		if err == nil {
			marker = ui.StyleSuccess.Render(ui.MarkPresent)
			value = path
		}
		p.Bullet(marker, fmt.Sprintf("%-13s %s", ui.StyleValue.Render(name), ui.StyleHint.Render(value)))
	}

	p.Section("Portable settings")
	for _, entry := range aisettings.Entries(includeAuth) {
		marker := ui.StyleHint.Render(ui.MarkPartial)
		if entry.Auth {
			marker = ui.StyleWarning.Render(ui.MarkWarn)
		}
		label := entry.Path
		if entry.Auth {
			label += "  (auth)"
		}
		if aisettings.ManagedByAgents(entry.Path) {
			label += "  (agents SSOT)"
		}
		p.Bullet(marker, fmt.Sprintf("%-8s %s", ui.StyleValue.Render(entry.Tool), label))
	}

	if apps, err := aiCaskTokens(); err == nil && len(apps) > 0 {
		p.Section("AI app casks")
		p.Line("  %s", ui.StyleHint.Render(strings.Join(apps, ", ")))
	}
	return nil
}

func aiCaskTokens() ([]string, error) {
	cat, err := catalog.LoadMacApps()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, group := range cat.Groups {
		if group.Name != "AI" {
			continue
		}
		for _, app := range group.Apps {
			out = append(out, app.Token)
		}
	}
	return out, nil
}

func newAIStatusCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "status",
		Short: "Show AI settings live/backup status",
		Args:  cobra.NoArgs,
		RunE:  runAIStatus,
	}
	c.Flags().String("from", "", "Backup root to inspect")
	c.Flags().String("host", "", "Hostname to inspect (default: this host)")
	c.Flags().Bool("include-auth", false, "Include auth/local-secret paths in status")
	return c
}

func runAIStatus(cmd *cobra.Command, _ []string) error {
	includeAuth, _ := cmd.Flags().GetBool("include-auth")
	eng, err := newAIEngine(cmd)
	if err != nil {
		return err
	}
	host, err := hostOverride(cmd, eng.Hostname)
	if err != nil {
		return err
	}
	eng.Hostname = host
	report := eng.StatusReport(aisettings.StatusOptions{IncludeAuth: includeAuth})
	p := printerFrom(cmd)
	p.Header("AI Config Status")
	p.KV("Host", report.Hostname)
	p.KV("Backup", report.HostRoot)
	if report.LatestKnown {
		p.KV("Latest", report.Latest)
	} else {
		p.KV("Latest", "(none)")
	}
	p.Section("Paths")
	for _, st := range report.Entries {
		live := ui.MarkPartial
		backup := ui.MarkPartial
		if st.PresentLive {
			live = ui.MarkPresent
		}
		if st.PresentBackup {
			backup = ui.MarkPresent
		}
		marker := ui.StyleHint.Render(ui.MarkPartial)
		if st.PresentLive && st.PresentBackup {
			marker = ui.StyleSuccess.Render(ui.MarkPresent)
		}
		if st.Entry.Auth {
			marker = ui.StyleWarning.Render(ui.MarkWarn)
		}
		label := st.Entry.Path
		if st.ManagedByAgents {
			label += "  (agents SSOT)"
		}
		p.Bullet(marker, fmt.Sprintf("%-8s live:%s backup:%s  %s",
			ui.StyleValue.Render(st.Entry.Tool), live, backup, label))
	}
	return nil
}

func newAIPruneCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "prune",
		Short: "Delete older AI config snapshots, keeping the newest N",
		Args:  cobra.NoArgs,
		RunE:  runAIPrune,
	}
	c.Flags().String("from", "", "Backup root (overrides configured BackupRoot)")
	c.Flags().String("host", "", "Hostname to prune (default: this host)")
	c.Flags().Int("keep", 5, "Number of most recent snapshots to keep")
	return c
}

func runAIPrune(cmd *cobra.Command, _ []string) error {
	yes, _ := cmd.Flags().GetBool("yes")
	keep, _ := cmd.Flags().GetInt("keep")
	eng, err := newAIEngine(cmd)
	if err != nil {
		return err
	}
	host, err := hostOverride(cmd, eng.Hostname)
	if err != nil {
		return err
	}
	eng.Hostname = host
	p := printerFrom(cmd)

	// Below 1 is rejected rather than floored (D-06): the engine clamps to 1
	// while the confirmation line prints the raw count, so a keep of 0 asked
	// the operator to approve a deletion the run never intended (BUG-12).
	// Placed here, after host validation, so the first error a doubly-wrong
	// run reports does not move.
	if keep < 1 {
		return fmt.Errorf("--keep must be at least 1 (got %d)", keep)
	}

	opts := aisettings.PruneOptions{Keep: keep}
	plan, err := eng.PlanPrune(opts)
	if err != nil {
		return err
	}
	if plan.Delete <= 0 {
		p.Line("Nothing to prune (%d snapshots <= keep=%d).", plan.Total, plan.Keep)
		return nil
	}
	if !yes {
		p.Line("About to delete %d snapshot(s) under %s.", plan.Delete, plan.HostRoot)
		ok, err := ui.ConfirmBool("Continue?", false, false)
		if err != nil {
			return err
		}
		if !ok {
			p.Line("aborted")
			return nil
		}
	}
	removed, err := eng.Prune(opts)
	if err != nil {
		return err
	}
	p.Line("Pruned %d snapshot(s):", len(removed))
	for _, v := range removed {
		p.Line("  - %s", v)
	}
	return nil
}
