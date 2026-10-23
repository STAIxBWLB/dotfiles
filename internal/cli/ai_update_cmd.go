package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/aischedule"
	"github.com/STAIxBWLB/dotfiles/internal/aitooling"
)

// updateTools is the fixed phase order of `dot ai update`.
var updateTools = []string{"claude", "codex", "kimi", "qwen", "grok", "opencode", "gencode", "pi", "antigravity", "ripwire", "ocr", "gsd", "ponytail"}

func newAIUpdateCmd() *cobra.Command {
	c := &cobra.Command{Use: "update", Short: "Update explicitly selected agent CLIs and development tools", Long: `Update only the saved nine-agent/four-addon allowlist.

Run 'dot ai setup' first. Native providers retain installation ownership.
Heavy work is serialized by the host resource guard. Unavailable metadata,
unsupported adapters, resource pressure, authentication and trust remain
explicitly pending; no blanket plugin, marketplace, or skill update runs.`, Args: cobra.NoArgs, RunE: runAIUpdate}
	c.Flags().Bool("check", false, "Inspect installed and available versions without mutation")
	c.Flags().StringSlice("tool", nil, "Limit to saved selections ("+strings.Join(updateTools, ",")+")")
	c.Flags().Bool("json", false, "Emit machine-readable JSON")
	c.Flags().Bool("scheduled", false, "Run due weekly maintenance with resource recovery admission")
	_ = c.Flags().MarkHidden("scheduled")
	c.AddCommand(newAIUpdateScheduleCmd())
	return c
}
func runAIUpdate(cmd *cobra.Command, _ []string) error {
	selectedConfig, err := effectiveToolingForCmd(cmd)
	if err != nil {
		return err
	}
	if selectedConfig == nil {
		return fmt.Errorf("AI tooling is not configured; run 'dot ai setup' to choose agents and tools")
	}
	selection := *selectedConfig
	ids, err := resolveUpdateTools(cmd)
	if err != nil {
		return err
	}
	if cmd.Flags().Changed("tool") {
		allowed := append(append([]string{}, selection.Agents...), selection.Tools...)
		for _, id := range ids {
			if !containsString(allowed, id) {
				return fmt.Errorf("%s is not selected; select it with dot ai setup first", id)
			}
		}

	}
	check, _ := cmd.Flags().GetBool("check")
	dry, _ := cmd.Flags().GetBool("dry-run")
	scheduled, _ := cmd.Flags().GetBool("scheduled")
	if scheduled && (check || dry) {
		scheduled = false
	}
	home := homeFromCmd(cmd)
	if scheduled {
		if !selection.Updates.Enabled {
			return fmt.Errorf("scheduled AI updates are disabled")
		}
		due, err := aischedule.BeginScheduled(home, time.Now())
		if err != nil {
			return err
		}
		if !due {
			return printJSON(cmd, map[string]string{"status": "not-due"})
		}
	}
	explicit, _ := cmd.Flags().GetString("home")
	engine := aitooling.New(aitooling.Options{HomeDir: home, ExplicitHome: explicit != "", DryRun: dry, Scheduled: scheduled, Out: cmd.ErrOrStderr(), Only: func() []string {
		if cmd.Flags().Changed("tool") {
			return ids
		}
		return nil
	}()})
	operation := aitooling.Update
	if check {
		operation = aitooling.Inspect
	}
	report, runErr := engine.Run(cmd.Context(), selection, operation)
	if scheduled {
		if err := aischedule.FinishScheduled(home, time.Now(), runErr == nil && report.Deferred == 0); err != nil && runErr == nil {
			runErr = err
		}
	}
	if !check && !dry {
		auditAIEventBestEffort(cmd, "ai.update", map[string]any{"operation": operation, "failed": report.Failed, "deferred": report.Deferred, "items": report.Items})
	}
	asJSON, _ := cmd.Flags().GetBool("json")
	if asJSON {
		if err := printJSON(cmd, report); err != nil {
			return err
		}
	} else {
		p := printerFrom(cmd)
		p.Header("Selected AI Tooling")
		for _, item := range report.Items {
			p.KV(item.ID, item.Status+" "+item.Installed+" "+item.Detail)
		}
		if report.Deferred > 0 {
			p.Warn("%d operation(s) deferred or awaiting activation", report.Deferred)
		}
	}
	return runErr
}

func resolveUpdateTools(cmd *cobra.Command) ([]string, error) {
	requested, _ := cmd.Flags().GetStringSlice("tool")
	if len(requested) == 0 {
		return updateTools, nil
	}
	var out []string
	for _, raw := range requested {
		for _, part := range strings.Split(raw, ",") {
			id := strings.TrimSpace(part)
			if id == "" {
				continue
			}
			if !containsString(updateTools, id) {
				return nil, fmt.Errorf("unknown tool %q (valid: %s)", id, strings.Join(updateTools, ", "))
			}
			if !containsString(out, id) {
				out = append(out, id)
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("--tool resolved to no tools (valid: %s)", strings.Join(updateTools, ", "))
	}
	// Keep the fixed phase order regardless of flag order.
	ordered := make([]string, 0, len(out))
	for _, id := range updateTools {
		if containsString(out, id) {
			ordered = append(ordered, id)
		}
	}
	return ordered, nil
}
