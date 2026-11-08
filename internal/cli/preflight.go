package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

// newPreflightCmd keeps the old name working for one release; its two modes
// are now `dot init --check-only` and `dot init --detect` (#378).
func newPreflightCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "preflight",
		Short:  "Deprecated: use dot init --check-only or dot init --detect",
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			checkOnly, _ := cmd.Flags().GetBool("check-only")
			if checkOnly {
				warnRenamed(cmd, "dot preflight --check-only", "dot init --check-only")
			} else {
				warnRenamed(cmd, "dot preflight", "dot init --detect")
			}
			return runInitDetect(cmd, checkOnly)
		},
	}
	cmd.Flags().Bool("check-only", false, "Only run checks, don't generate config")
	cmd.Flags().Bool("force", false, "Overwrite existing config file")
	return cmd
}

// runInitDetect scans the environment and, unless checkOnly, writes a config
// generated from the detected system. An existing config is kept unless
// --force is set.
func runInitDetect(cmd *cobra.Command, checkOnly bool) error {
	force, _ := cmd.Flags().GetBool("force")
	// Same resolution as the wizard: --home, then $DOTFILES_HOME.
	homeOverride := homeOverrideFrom(cmd)

	home := homeOverride
	if home == "" {
		home, _ = os.UserHomeDir()
	}

	sysInfo, err := config.DetectSystem()
	if err != nil {
		return fmt.Errorf("detecting system: %w", err)
	}

	p := printerFrom(cmd)
	report := config.RunPreflightChecks(sysInfo, home)
	printPreflightReport(p, report)

	if checkOnly {
		return nil
	}

	statePath := statePathFor(homeOverride)
	if _, err := os.Stat(statePath); err == nil && !force {
		p.Line("\nConfig already exists: %s", statePath)
		p.Line("Use --force to overwrite.")
		return nil
	}

	state := config.GeneratePreflightConfig(sysInfo)
	return saveStateAndSummarize(cmd, homeOverride, state, "Generated config.")
}

func printPreflightReport(p *Printer, report *config.PreflightReport) {
	p.Header("Environment Preflight")

	for _, c := range report.Checks {
		var marker string
		switch c.Status {
		case config.CheckPass:
			marker = ui.StyleSuccess.Render(ui.MarkPresent)
		case config.CheckWarn:
			marker = ui.StyleWarning.Render(ui.MarkWarn)
		case config.CheckFail:
			marker = ui.StyleError.Render(ui.MarkFail)
		default:
			marker = ui.StyleHint.Render(ui.MarkPartial)
		}
		p.Bullet(marker, fmt.Sprintf("%-25s %s", ui.StyleValue.Render(c.Name), ui.StyleHint.Render(c.Value)))
		if c.Message != "" {
			p.Line("     %s", ui.StyleHint.Render(c.Message))
		}
	}

	pass, warn, fail := report.Counts()
	p.Blank()
	p.Line("  Result: %d passed, %d warnings, %d failures", pass, warn, fail)
}
