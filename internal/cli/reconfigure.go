package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

func newReconfigureCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reconfigure",
		Short: "Re-run init prompts with current values as defaults",
		Long:  "Edit existing dot configuration interactively, then optionally apply.",
		RunE:  runReconfigure,
	}
}

func runReconfigure(cmd *cobra.Command, _ []string) error {
	homeOverride, _ := cmd.Flags().GetString("home")
	var state *config.UserState
	var err error
	if homeOverride != "" {
		state, err = config.LoadStateForHome(homeOverride)
	} else {
		state, err = config.LoadState()
	}
	if err != nil {
		return fmt.Errorf("loading state: %w", err)
	}

	p := printerFrom(cmd)
	if state.Name == "" {
		p.Line("No existing configuration found. Running init...")
	} else {
		p.Line("Current configuration:")
		ui.PrintStateSummary(state)
		p.Line("")
	}

	// Re-run the init flow — it uses existing state values as defaults.
	if err := runInitFlow(cmd, true); err != nil {
		return err
	}

	yes, _ := cmd.Flags().GetBool("yes")

	apply, err := ui.ConfirmBool("Apply updated configuration now?", false, yes)
	if err != nil {
		return err
	}
	if apply {
		p.Line("")
		return runApply(cmd, nil)
	}

	p.Line("Run 'dot apply' when ready to apply.")
	return nil
}
