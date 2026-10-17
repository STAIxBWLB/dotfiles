package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

func newPreflightCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "preflight",
		Short: "Check environment and generate config",
		Long:  "Run environment checks and generate a dot configuration based on the detected system.",
		RunE:  runPreflight,
	}
	cmd.Flags().Bool("check-only", false, "Only run checks, don't generate config")
	cmd.Flags().Bool("force", false, "Overwrite existing config file")
	return cmd
}

func runPreflight(cmd *cobra.Command, _ []string) error {
	checkOnly, _ := cmd.Flags().GetBool("check-only")
	force, _ := cmd.Flags().GetBool("force")
	homeOverride, _ := cmd.Flags().GetString("home")

	// Resolve home directory
	home := homeOverride
	if home == "" {
		home, _ = os.UserHomeDir()
	}

	// Detect system
	sysInfo, err := config.DetectSystem()
	if err != nil {
		return fmt.Errorf("detecting system: %w", err)
	}

	// Run preflight checks
	p := printerFrom(cmd)
	report := config.RunPreflightChecks(sysInfo, home)
	printPreflightReport(p, report)

	if checkOnly {
		return nil
	}

	// Check existing config
	statePath := config.StatePath()
	if homeOverride != "" {
		statePath = config.StatePathForHome(homeOverride)
	}
	if _, err := os.Stat(statePath); err == nil && !force {
		p.Line("\nConfig already exists: %s", statePath)
		p.Line("Use --force to overwrite.")
		return nil
	}

	// Generate config
	state := config.GeneratePreflightConfig(sysInfo)

	// Save
	if homeOverride != "" {
		if err := config.SaveStateForHome(homeOverride, state); err != nil {
			return fmt.Errorf("saving state: %w", err)
		}
	} else {
		if err := config.SaveState(state); err != nil {
			return fmt.Errorf("saving state: %w", err)
		}
	}

	p.Header("Generated Config")
	ui.PrintStateSummary(state)
	p.Blank()
	p.KV("Config saved", statePath)
	p.Line("  Run 'dot apply' to apply the configuration.")

	return nil
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
