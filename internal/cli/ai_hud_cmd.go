package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/aisettings"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

// dot ai hud and dot ai coauthor-guard: the two command families that
// write into another tool's config and can persist their choice into
// dot user state.

func newAIHudCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "hud",
		Short: "Manage dot-native Claude Code and Codex HUD status lines",
	}
	c.AddCommand(newAIHudStatusCmd())
	c.AddCommand(newAIHudApplyCmd())
	return c
}

func newAIHudStatusCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "status",
		Short: "Show Claude Code and Codex HUD status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			tools, _ := cmd.Flags().GetString("tool")
			mgr := newHUDManagerFromCmd(cmd)
			items, err := mgr.Status(parseAgentToolIDs(tools))
			if err != nil {
				return err
			}
			p := printerFrom(cmd)
			p.Header("AI HUD")
			printHUDItems(p, items)
			return nil
		},
	}
	c.Flags().String("tool", "", "Comma-separated tool IDs (claude,codex)")
	return c
}

func newAIHudApplyCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "apply",
		Short: "Apply dot-native HUD settings to Claude Code and Codex",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			tools, _ := cmd.Flags().GetString("tool")
			persist, _ := cmd.Flags().GetBool("persist")
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			mgr := newHUDManagerFromCmd(cmd)
			force, _ := cmd.Flags().GetBool("force")
			result, err := mgr.Apply(aisettings.HUDOptions{Tools: parseAgentToolIDs(tools), DryRun: dryRun, Force: force})
			if err != nil {
				return err
			}
			if persist && !dryRun {
				if err := persistAIHUD(cmd); err != nil {
					return err
				}
			}
			if err := auditAIEvent(cmd, "ai.hud.apply", hudApplyPayload(result, persist)); err != nil {
				return err
			}
			p := printerFrom(cmd)
			p.Header("AI HUD Apply")
			printHUDApplyResult(p, result)
			if persist {
				p.KV("Persist", fmt.Sprintf("%v", !dryRun))
			}
			return nil
		},
	}
	c.Flags().String("tool", "", "Comma-separated tool IDs (claude,codex)")
	c.Flags().Bool("persist", false, "Persist modules.ai.hud=true for future dot apply runs")
	c.Flags().Bool("force", false, "Take over a statusLine currently owned by another tool")
	return c
}

func newAICoauthoredGuardCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "coauthor-guard",
		Short: "Warn or block AI-added Co-authored commit trailers",
	}
	c.AddCommand(newAICoauthoredGuardStatusCmd())
	c.AddCommand(newAICoauthoredGuardApplyCmd())
	return c
}

func newAICoauthoredGuardStatusCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "status",
		Short: "Show coauthor guard status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			mode, _ := cmd.Flags().GetString("mode")
			mgr := newCoauthorGuardManagerFromCmd(cmd)
			st, err := mgr.Status(mode)
			if err != nil {
				return err
			}
			p := printerFrom(cmd)
			p.Header("Coauthor Guard")
			printCoauthorGuardStatus(p, st)
			return nil
		},
	}
	c.Flags().String("mode", aisettings.CoauthorGuardBlock, "Guard mode: off, warn, or block")
	return c
}

func newAICoauthoredGuardApplyCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "apply",
		Short: "Apply coauthor guard instruction and Git commit-msg hook",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			mode, _ := cmd.Flags().GetString("mode")
			persist, _ := cmd.Flags().GetBool("persist")
			applyAgents, _ := cmd.Flags().GetBool("apply-agents")
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			mgr := newCoauthorGuardManagerFromCmd(cmd)
			result, err := mgr.Apply(aisettings.CoauthorGuardOptions{
				Mode:        mode,
				DryRun:      dryRun,
				ApplyAgents: applyAgents,
			})
			if err != nil {
				return err
			}
			if persist && !dryRun {
				if err := persistCoauthorGuard(cmd, result.Status.Mode); err != nil {
					return err
				}
			}
			if err := auditAIEvent(cmd, "ai.coauthor_guard.apply", coauthorGuardPayload(result, persist)); err != nil {
				return err
			}
			p := printerFrom(cmd)
			p.Header("Coauthor Guard Apply")
			printCoauthorGuardStatus(p, result.Status)
			if result.Warning != "" {
				p.Warn("%s", result.Warning)
			}
			if result.HookChanged {
				p.Bullet(ui.StyleHint.Render(ui.MarkPending), "hook updated")
			}
			if result.ConfigChanged {
				p.Bullet(ui.StyleHint.Render(ui.MarkPending), "git hook config updated")
			}
			if result.AgentsChanged {
				p.Bullet(ui.StyleHint.Render(ui.MarkPending), "AGENTS instruction updated")
			}
			if result.AgentsApplied {
				p.Bullet(ui.StyleHint.Render(ui.MarkPending), "agent targets reapplied")
			}
			if persist {
				p.KV("Persist", fmt.Sprintf("%v", !dryRun))
			}
			return nil
		},
	}
	c.Flags().String("mode", aisettings.CoauthorGuardBlock, "Guard mode: off, warn, or block")
	c.Flags().Bool("persist", false, "Persist modules.git.coauthor_guard for future dot apply runs")
	c.Flags().Bool("apply-agents", false, "Reapply agents SSOT to live tool targets after updating the instruction")
	return c
}

func printHUDItems(p *Printer, items []aisettings.HUDItem) {
	for _, item := range items {
		marker, style := agentDriftMarker(item.Drift)
		label := fmt.Sprintf("%-8s %-12s %s", ui.StyleValue.Render(item.ToolID), item.Drift, item.TargetPath)
		if item.Detail != "" {
			label += "  " + ui.StyleHint.Render(item.Detail)
		}
		p.Bullet(style.Render(marker), label)
	}
}

func printHUDApplyResult(p *Printer, result *aisettings.HUDResult) {
	changed, conflicts := 0, 0
	for _, item := range result.Items {
		marker := ui.StyleSuccess.Render(ui.MarkPresent)
		state := "in-sync"
		switch {
		case item.Changed:
			changed++
			marker = ui.StyleHint.Render(ui.MarkPending)
			state = "wrote"
			if result.DryRun {
				state = "would write"
			}
		case item.Drift == "out-of-sync":
			// Not written and still drifted: the target is owned by another
			// tool. Rendering this as in-sync would report success for a
			// change that did not happen.
			conflicts++
			marker = ui.StyleWarning.Render(ui.MarkWarn)
			state = "conflict"
		}
		label := fmt.Sprintf("%-8s %-12s %s", ui.StyleValue.Render(item.ToolID), state, item.TargetPath)
		if item.Detail != "" {
			label += "  " + ui.StyleHint.Render(item.Detail)
		}
		p.Bullet(marker, label)
	}
	switch {
	case conflicts > 0:
		p.Warn("%d HUD target(s) left untouched: owned by another tool. Rerun with --force to take over.", conflicts)
	case changed == 0:
		p.Success("all selected HUD targets already match")
	}
}

func printCoauthorGuardStatus(p *Printer, st aisettings.CoauthorGuardStatus) {
	p.KV("Mode", st.Mode)
	p.KV("Hook", st.HookPath)
	p.KV("Git config", st.GitConfigPath)
	p.KV("AGENTS", st.AgentsPath)
	p.Section("Drift")
	for _, row := range []struct {
		name  string
		drift string
	}{
		{"hook", st.HookDrift},
		{"hookConfig", st.HookConfigDrift},
		{"agents", st.AgentsDrift},
	} {
		marker, style := agentDriftMarker(row.drift)
		p.Bullet(style.Render(marker), fmt.Sprintf("%-10s %s", row.name, row.drift))
	}
	if !st.GitHooksSupported && st.Mode != aisettings.CoauthorGuardOff {
		p.Warn("git %s predates config-based hooks (2.54); upgrade git to enforce the guard", st.GitVersion)
	}
	if st.HooksPathLeftover != "" {
		p.Warn("leftover dot-managed core.hooksPath %q; apply migrates it away", st.HooksPathLeftover)
	}
}

func persistAIHUD(cmd *cobra.Command) error {
	state, err := loadStateForCmd(cmd)
	if err != nil {
		return err
	}
	state.Modules.AI.Enabled = true
	state.Modules.AI.HUD = true
	return saveStateForCmd(cmd, state)
}

func persistCoauthorGuard(cmd *cobra.Command, mode string) error {
	state, err := loadStateForCmd(cmd)
	if err != nil {
		return err
	}
	state.Modules.AI.Enabled = true
	state.Modules.Git.CoauthorGuard = mode
	return saveStateForCmd(cmd, state)
}
