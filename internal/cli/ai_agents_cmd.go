package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/aisettings"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

// dot ai agents: the shared AGENTS.md SSOT and its copy-render to each
// tool target. The tool-id parsing and drift glyphs at the bottom are
// shared with the hud and coauthor-guard commands.

func newAIAgentsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "agents",
		Short: "Manage the shared AI agents instruction SSOT",
		Long:  "Manage ~/.config/dotfiles/agents/AGENTS.md and copy-render it to Claude, Codex, and optional AI coding tool targets.",
	}
	c.AddCommand(newAIAgentsListCmd(false))
	c.AddCommand(newAIAgentsListCmd(true))
	c.AddCommand(newAIAgentsInitCmd())
	c.AddCommand(newAIAgentsAuthorCmd())
	c.AddCommand(newAIAgentsShowCmd())
	c.AddCommand(newAIAgentsEditCmd())
	c.AddCommand(newAIAgentsApplyCmd())
	c.AddCommand(newAIAgentsPullCmd())
	c.AddCommand(newAIAgentsDiffCmd())
	c.AddCommand(newAIAgentsPathCmd())
	return c
}

func newAIAgentsListCmd(verbose bool) *cobra.Command {
	use := "list"
	short := "List registered agents targets and drift"
	if verbose {
		use = "status"
		short = "Show detailed agents SSOT drift status"
	}
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			mgr := newAgentsManagerFromCmd(cmd)
			statuses, err := mgr.Status()
			if err != nil {
				return err
			}
			p := printerFrom(cmd)
			p.Header("AI Agents SSOT")
			p.KV("SSOT", mgr.SSOTPath())
			p.Section("Targets")
			for _, st := range statuses {
				marker, style := agentDriftMarker(st.Drift)
				opt := ""
				if st.Tool.Optional {
					opt = " optional"
				}
				overlay := ""
				if st.OverlayExists {
					overlay = " overlay"
				}
				p.Bullet(style.Render(marker), fmt.Sprintf("%-8s %-14s %s%s%s",
					ui.StyleValue.Render(st.Tool.ID), st.Drift, st.TargetPath, opt, overlay))
				if verbose {
					p.Line("      rendered:%s target:%s", shortHash(st.RenderedHash), shortHash(st.TargetHash))
				}
			}
			return nil
		},
	}
}

func newAIAgentsInitCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "init",
		Short: "Create the shared agents SSOT",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			from, _ := cmd.Flags().GetString("from-current")
			yes, _ := cmd.Flags().GetBool("yes")
			force, _ := cmd.Flags().GetBool("force")
			mgr := newAgentsManagerFromCmd(cmd)
			res, err := mgr.Init(aisettings.InitOptions{FromCurrent: from, Yes: yes, Force: force})
			if err != nil {
				return err
			}
			if err := auditAIEvent(cmd, "ai.agents.init", map[string]any{
				"path":        res.Path,
				"created":     res.Created,
				"from_tool":   res.FromTool,
				"backup_path": res.BackupPath,
			}); err != nil {
				return err
			}
			p := printerFrom(cmd)
			p.Header("AI Agents Init")
			p.KV("SSOT", res.Path)
			if res.FromTool != "" {
				p.KV("From", res.FromTool)
			}
			if res.BackupPath != "" {
				p.KV("Backup", res.BackupPath)
			}
			if res.Created {
				p.Success("created")
			} else {
				p.Line("already exists")
			}
			return nil
		},
	}
	c.Flags().String("from-current", "", "Seed AGENTS.md from an existing tool target")
	c.Flags().Bool("force", false, "Overwrite an existing SSOT after backing it up")
	return c
}

func newAIAgentsAuthorCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "author",
		Short: "Interactively or programmatically edit SSOT sections",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			from, _ := cmd.Flags().GetString("from-current")
			nonInteractive, _ := cmd.Flags().GetBool("non-interactive")
			section, _ := cmd.Flags().GetString("section")
			value, _ := cmd.Flags().GetString("value")
			yes, _ := cmd.Flags().GetBool("yes")
			mgr := newAgentsManagerFromCmd(cmd)
			res, err := mgr.Author(aisettings.AuthorOptions{
				FromCurrent:    from,
				NonInteractive: nonInteractive,
				Section:        section,
				Value:          value,
				Yes:            yes,
			})
			if err != nil {
				return err
			}
			if err := auditAIEvent(cmd, "ai.agents.author", map[string]any{
				"path":     res.Path,
				"changed":  res.Changed,
				"sections": res.Sections,
			}); err != nil {
				return err
			}
			p := printerFrom(cmd)
			p.Header("AI Agents Author")
			p.KV("SSOT", res.Path)
			if len(res.Sections) > 0 {
				p.KV("Sections", strings.Join(res.Sections, ", "))
			}
			if res.Changed {
				p.Success("updated")
			} else {
				p.Line("no changes")
			}
			return nil
		},
	}
	c.Flags().String("from-current", "", "Pull from a live tool target before authoring")
	c.Flags().Bool("non-interactive", false, "Update one section without the wizard")
	c.Flags().String("section", "", "Section name for --non-interactive")
	c.Flags().String("value", "", "Section value for --non-interactive")
	return c
}

func newAIAgentsShowCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "show",
		Short: "Print the raw or rendered agents SSOT",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rendered, _ := cmd.Flags().GetString("rendered")
			withLineNumbers, _ := cmd.Flags().GetBool("with-line-numbers")
			mgr := newAgentsManagerFromCmd(cmd)
			out, err := mgr.Show(aisettings.ShowOptions{RenderedTool: rendered, WithLineNumbers: withLineNumbers})
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), out)
			if !strings.HasSuffix(out, "\n") {
				fmt.Fprintln(cmd.OutOrStdout())
			}
			return nil
		},
	}
	c.Flags().String("rendered", "", "Print SSOT rendered for one tool")
	c.Flags().Bool("with-line-numbers", false, "Prefix output with line numbers")
	return c
}

func newAIAgentsEditCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "edit",
		Short: "Open $EDITOR on the shared AGENTS.md",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			mgr := newAgentsManagerFromCmd(cmd)
			editor := os.Getenv("EDITOR")
			if err := mgr.Edit(context.Background(), editor); err != nil {
				return err
			}
			return auditAIEvent(cmd, "ai.agents.edit", map[string]any{"path": mgr.SSOTPath()})
		},
	}
}

func newAIAgentsApplyCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "apply",
		Short: "Copy-render the SSOT to agent tool targets",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			toolFlag, _ := cmd.Flags().GetString("tool")
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			force, _ := cmd.Flags().GetBool("force")
			mgr := newAgentsManagerFromCmd(cmd)
			ids := parseAgentToolIDs(toolFlag)
			if len(ids) == 0 {
				ids = mgr.DefaultApplyTools()
			}
			result, err := mgr.Apply(aisettings.ApplyOptions{Tools: ids, DryRun: dryRun, Force: force})
			if err != nil {
				return err
			}
			if err := auditAIEvent(cmd, "ai.agents.apply", agentsApplyPayload(result)); err != nil {
				return err
			}
			p := printerFrom(cmd)
			p.Header("AI Agents Apply")
			printAgentsApplyResult(p, result)
			return nil
		},
	}
	c.Flags().String("tool", "", "Comma-separated tool IDs to apply")
	c.Flags().Bool("force", false, "Overwrite a target changed outside the last dot-managed apply after backing it up")
	return c
}

func newAIAgentsPullCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "pull",
		Short: "Copy one live tool target back into the SSOT",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			from, _ := cmd.Flags().GetString("from")
			yes, _ := cmd.Flags().GetBool("yes")
			mgr := newAgentsManagerFromCmd(cmd)
			res, err := mgr.Pull(aisettings.PullOptions{FromTool: from, Yes: yes})
			if err != nil {
				return err
			}
			if err := auditAIEvent(cmd, "ai.agents.pull", map[string]any{
				"from_tool":   res.FromTool,
				"source_path": res.SourcePath,
				"ssot_path":   res.SSOTPath,
				"backup_path": res.BackupPath,
				"changed":     res.Changed,
			}); err != nil {
				return err
			}
			p := printerFrom(cmd)
			p.Header("AI Agents Pull")
			p.KV("From", res.SourcePath)
			p.KV("SSOT", res.SSOTPath)
			if res.BackupPath != "" {
				p.KV("Backup", res.BackupPath)
			}
			if res.Changed {
				p.Success("updated")
			} else {
				p.Line("already matches")
			}
			return nil
		},
	}
	c.Flags().String("from", "", "Tool ID to pull from")
	return c
}

func newAIAgentsDiffCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "diff",
		Short: "Show rendered-vs-live diff for agents targets",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			toolFlag, _ := cmd.Flags().GetString("tool")
			mgr := newAgentsManagerFromCmd(cmd)
			ids := parseAgentToolIDs(toolFlag)
			if len(ids) == 0 {
				ids = mgr.DefaultApplyTools()
			}
			for _, id := range ids {
				diff, err := mgr.Diff(id)
				if err != nil {
					return err
				}
				if diff == "" {
					fmt.Fprintf(cmd.OutOrStdout(), "%s: in-sync\n", id)
					continue
				}
				fmt.Fprint(cmd.OutOrStdout(), diff)
			}
			return nil
		},
	}
	c.Flags().String("tool", "", "Comma-separated tool IDs to diff")
	return c
}

func newAIAgentsPathCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the absolute agents SSOT directory",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			mgr := newAgentsManagerFromCmd(cmd)
			fmt.Fprintln(cmd.OutOrStdout(), mgr.SSOTDirPath())
			return nil
		},
	}
}

func printAgentsApplyResult(p *Printer, result *aisettings.ApplyResult) {
	for _, warning := range result.Warnings {
		p.Warn("%s", warning)
	}
	changed := 0
	for _, item := range result.Items {
		marker := ui.StyleSuccess.Render(ui.MarkPresent)
		state := "in-sync"
		if item.Changed {
			changed++
			marker = ui.StyleHint.Render(ui.MarkPending)
			state = "would write"
			if !result.DryRun {
				state = "wrote"
			}
		}
		p.Bullet(marker, fmt.Sprintf("%-8s %-10s %s", ui.StyleValue.Render(item.ToolID), state, item.TargetPath))
		if result.DryRun && item.Diff != "" {
			p.Line("%s", item.Diff)
		}
	}
	if changed == 0 {
		p.Success("all selected targets already match")
	}
}

func parseAgentToolIDs(value string) []string {
	var ids []string
	for _, part := range strings.Split(value, ",") {
		part = strings.ToLower(strings.TrimSpace(part))
		if part != "" {
			ids = append(ids, part)
		}
	}
	return ids
}

func agentDriftMarker(drift string) (string, interface{ Render(...string) string }) {
	switch drift {
	case "in-sync":
		return ui.MarkPresent, ui.StyleSuccess
	case "out-of-sync":
		return ui.MarkWarn, ui.StyleWarning
	case "target-missing", "ssot-missing":
		return ui.MarkAbsent, ui.StyleHint
	default:
		return ui.MarkPartial, ui.StyleHint
	}
}

func shortHash(hash string) string {
	if hash == "" {
		return "-"
	}
	if len(hash) <= 12 {
		return hash
	}
	return hash[:12]
}
