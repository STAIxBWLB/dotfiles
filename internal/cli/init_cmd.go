package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

func newInitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Interactive setup for dot",
		Long: `Collect user preferences and save them to the dot state file.

Use --from to import settings from another machine's exported config:
  dot init --from ~/workspace/secrets/dotfiles-config.yaml

Use --check-only to scan the environment without writing anything, or
--detect to write a config generated from the detected system without
prompts (--force overwrites an existing one).`,
		RunE: runInit,
	}
	cmd.Flags().String("from", "", "Import settings from an exported config file (identity & preferences as defaults)")
	addInitDetectFlags(cmd)
	return cmd
}

func addInitDetectFlags(cmd *cobra.Command) {
	cmd.Flags().Bool("check-only", false, "Only run environment checks, don't write config")
	cmd.Flags().Bool("detect", false, "Write a config generated from the detected system, without prompts")
	cmd.Flags().Bool("force", false, "With --detect, overwrite an existing config file")
}

func runInit(cmd *cobra.Command, _ []string) error {
	checkOnly, _ := cmd.Flags().GetBool("check-only")
	detect, _ := cmd.Flags().GetBool("detect")
	if checkOnly || detect {
		return runInitDetect(cmd, checkOnly)
	}
	return runInitFlow(cmd, false)
}

func runInitFlow(cmd *cobra.Command, skipExistingGate bool) error {
	yes, _ := cmd.Flags().GetBool("yes")
	homeOverride := homeOverrideFrom(cmd)
	wizardHome := homeOverride
	if wizardHome == "" {
		wizardHome, _ = os.UserHomeDir()
	}
	fromPath, _ := cmd.Flags().GetString("from")
	p := printerFrom(cmd)

	var state *config.UserState
	var err error

	if fromPath != "" {
		// Import from external config file — use as defaults for interactive prompts
		state, err = config.LoadStateFrom(fromPath)
		if err != nil {
			return fmt.Errorf("importing config from %s: %w", fromPath, err)
		}
		p.Line("Imported settings from %s", fromPath)
		ui.PrintStateSummary(state)
		p.Line("")
		p.Line("Machine-specific settings (workspace, terminal) will be confirmed interactively.")
		p.Line("")
	} else if homeOverride != "" {
		state, err = config.LoadStateForHome(homeOverride)
		if err != nil {
			return fmt.Errorf("loading state: %w", err)
		}
	} else {
		state, err = config.LoadState()
		if err != nil {
			return fmt.Errorf("loading state: %w", err)
		}
	}

	// If state already has data (not from --from), ask whether to reconfigure.
	freshState := state.Name == "" && fromPath == ""
	if state.Name != "" && !yes && fromPath == "" && !skipExistingGate {
		p.Line("Current configuration:")
		ui.PrintStateSummary(state)
		p.Line("")

		reconfigure, err := ui.ConfirmBool("Reconfigure existing settings?", false, false)
		if err != nil {
			return err
		}
		if !reconfigure {
			p.Line("Keeping existing configuration.")
			return nil
		}
	}

	// --- Identity ---
	if err := ui.ConfigureIdentity(state, yes); err != nil {
		return err
	}

	// --- Profile ---
	sysInfo, err := config.DetectSystem()
	if err != nil {
		return fmt.Errorf("detecting system: %w", err)
	}
	if err := ui.ConfigureProfile(state, sysInfo.SuggestProfile(), yes); err != nil {
		return err
	}

	// --- SSH ---
	if err := ui.ConfigureSSH(state, wizardHome, yes); err != nil {
		return err
	}

	// --- Workspace ---
	if err := ui.ConfigureWorkspace(state, state.Profile, yes); err != nil {
		return err
	}

	// --- AI ---
	if err := ui.ConfigureAI(state, yes, freshState); err != nil {
		return err
	}

	// --- Terminal ---
	if err := ui.ConfigureTerminal(state, state.Profile, sysInfo, yes); err != nil {
		return err
	}

	// --- Fonts ---
	if err := ui.ConfigureFonts(state, state.Profile, yes); err != nil {
		return err
	}

	// --- Secrets ---
	if err := ui.ConfigureSecrets(state, wizardHome, state.Profile, yes); err != nil {
		return err
	}

	// --- macOS Apps ---
	if err := ui.ConfigureMacApps(state, wizardHome, state.Profile, yes); err != nil {
		return err
	}

	// --- Persist ---
	if err := saveStateAndSummarize(cmd, homeOverride, state, "Configuration saved."); err != nil {
		return err
	}
	return nil
}

// saveStateAndSummarize is the one save-and-print-summary tail shared by the
// interactive wizard and --detect (#378).
func saveStateAndSummarize(cmd *cobra.Command, homeOverride string, state *config.UserState, heading string) error {
	p := printerFrom(cmd)
	if homeOverride != "" {
		if err := config.SaveStateForHome(homeOverride, state); err != nil {
			return fmt.Errorf("saving state: %w", err)
		}
	} else {
		if err := config.SaveState(state); err != nil {
			return fmt.Errorf("saving state: %w", err)
		}
	}

	p.Line("")
	p.Line("%s", heading)
	p.Line("  State file: %s", statePathFor(homeOverride))
	ui.PrintStateSummary(state)
	p.Line("")
	p.Line("Run 'dot apply' to apply the configuration.")
	return nil
}

func statePathFor(homeOverride string) string {
	if homeOverride != "" {
		return config.StatePathForHome(homeOverride)
	}
	return config.StatePath()
}
