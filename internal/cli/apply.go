package cli

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/module"
	"github.com/STAIxBWLB/dotfiles/internal/template"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

var errAborted = errors.New("aborted by user")

func newApplyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "apply",
		Short: "Apply dot configuration",
		Long:  "Apply the selected profile's configuration to the user environment.",
		RunE:  runApply,
	}
}

func runApply(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	yes, _ := cmd.Flags().GetBool("yes")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	profileName, _ := cmd.Flags().GetString("profile")
	moduleFilter, _ := cmd.Flags().GetStringSlice("module")
	configPath, _ := cmd.Flags().GetString("config")
	homeOverride, _ := cmd.Flags().GetString("home")

	// Check env vars
	if os.Getenv("DOTFILES_YES") == "true" {
		yes = true
	}
	if profileName == "" {
		profileName = os.Getenv("DOTFILES_PROFILE")
	}
	if homeOverride == "" {
		homeOverride = os.Getenv("DOTFILES_HOME")
	}

	// Resolve home directory
	home := homeOverride
	if home == "" {
		home, _ = os.UserHomeDir()
	}

	// Load user state (from target home if overridden)
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

	// Use profile from state if not specified
	if profileName == "" && state.Profile != "" {
		profileName = state.Profile
	}

	// Without a terminal the prompts cannot run: huh opens /dev/tty and dies
	// with a bubbletea error over a plain ssh command (#183). Stored values
	// stand in for the prompts; a missing one, or the confirmation a real
	// apply needs, is an error that names the way out.
	interactive := ui.TerminalAttached()
	if !yes && !interactive {
		if missing := missingApplyValues(state, profileName, configPath); len(missing) > 0 {
			return fmt.Errorf("no terminal for prompts, and nothing supplies %s", strings.Join(missing, "; "))
		}
		if !dryRun {
			return fmt.Errorf("no terminal to confirm the apply; pass --yes, or run dot apply from a terminal")
		}
	}

	// Detect system
	sysInfo, err := config.DetectSystem()
	if err != nil {
		return fmt.Errorf("detecting system: %w", err)
	}

	// Select profile
	if profileName == "" && configPath == "" {
		suggested := sysInfo.SuggestProfile()
		if yes {
			profileName = suggested
		} else {
			profileName, err = ui.Select(
				"Select profile",
				config.AvailableProfiles(),
				suggested,
				false,
			)
			if err != nil {
				return err
			}
		}
	}

	// Interactive configuration
	p := printerFrom(cmd)
	state.Profile = profileName
	if err := configureInteractive(p, state, home, profileName, sysInfo, yes || !interactive); err != nil {
		if errors.Is(err, errAborted) {
			p.Line("Aborted.")
			return nil
		}
		return err
	}

	// Save updated state. The guard sits ABOVE the fork on purpose: the fork
	// only picks a destination, so one condition covers both arms, and a guard
	// duplicated inside them is how one arm gets fixed and the other does not
	// (BUG-05). It cannot live in internal/config either — saveStateAt has no
	// runner to consult and the runner does not exist until :137.
	if dryRun {
		statePath := config.StatePath()
		if homeOverride != "" {
			statePath = config.StatePathForHome(homeOverride)
		}
		p.Line("dry-run: would write %s", statePath)
	} else if homeOverride != "" {
		if err := config.SaveStateForHome(homeOverride, state); err != nil {
			return fmt.Errorf("saving state: %w", err)
		}
	} else {
		if err := config.SaveState(state); err != nil {
			return fmt.Errorf("saving state: %w", err)
		}
	}

	// Load config from profile
	cfg, err := config.Load(profileName, configPath, sysInfo)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	// Apply user state to config
	applyStateWithToolingAuthority(cfg, state, configPath != "")

	// Apply env overrides
	config.ApplyEnvOverrides(cfg)

	// Setup runner
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	runner := exec.NewRunner(dryRun, logger)
	brew := exec.NewBrew(runner)
	tmplEngine := template.NewEngine()

	// The rclone-based `dot clone` command is gone, but machines that ever
	// ran `dot clone setup` still have its scheduler installed — with no
	// remaining subcommand to remove it. Sweep those legacy units here.
	cleanupLegacyCloneScheduler(ctx, p, runner, home, homeOverride == "")

	// Build module list
	registry := module.NewRegistry()
	moduleFilter, err = module.NormalizeFilter(moduleFilter)
	if err != nil {
		return err
	}
	modules := registry.Resolve(cfg, moduleFilter)

	if len(modules) == 0 {
		p.Line("No modules to apply.")
		return nil
	}

	// Show plan
	p.Line("\nProfile: %s", profileName)
	if dryRun {
		p.Line("Mode: dry-run (no changes will be made)")
	}
	if homeOverride != "" {
		p.Line("Home: %s", homeOverride)
	}
	names := make([]string, 0, len(modules))
	for _, m := range modules {
		names = append(names, m.Name())
	}
	p.Line("Modules: %s", strings.Join(names, ", "))

	// Final confirmation
	if !yes && !dryRun {
		confirmed, err := ui.Confirm("Apply this configuration?", false)
		if err != nil {
			return err
		}
		if !confirmed {
			return errAborted
		}
	}

	// Execute modules
	rc := &module.RunContext{
		ExplicitHome: homeOverride != "",
		Config:       cfg,
		Runner:       runner,
		Brew:         brew,
		Template:     tmplEngine,
		DryRun:       dryRun,
		Yes:          yes,
		HomeDir:      home,
		Out:          p.Out,
	}

	p.Line("")
	if err := module.RunAll(ctx, modules, rc); err != nil {
		return err
	}

	// Refresh shell completion scripts from the current cobra tree so any
	// new subcommand (e.g. gsync) tab-completes after the next shell
	// session. Failures here are logged but don't fail apply — completion
	// is a UX nicety, not a correctness requirement.
	if changed, err := installCompletions(cmd.Root(), runner, home); err != nil {
		runner.Logger.Warn("installing shell completions failed", "err", err)
	} else if changed && !dryRun {
		p.Line("✓ shell completions refreshed in %s", completionDir(home))
	}
	return nil
}

// missingApplyValues lists what a prompt would have to supply, each with its
// non-interactive source: the profile (unless --config names the
// configuration) and the identity, which the stored state or the
// DOTFILES_NAME/DOTFILES_EMAIL overrides provide. --yes is no source for the
// identity: it skips the prompts without detecting one.
func missingApplyValues(state *config.UserState, profile, configPath string) []string {
	var missing []string
	if profile == "" && configPath == "" {
		missing = append(missing, "the profile (pass --profile, set DOTFILES_PROFILE, or pass --yes for the suggested one)")
	}
	var identity []string
	if state.Name == "" && os.Getenv("DOTFILES_NAME") == "" {
		identity = append(identity, "name")
	}
	if state.Email == "" && os.Getenv("DOTFILES_EMAIL") == "" {
		identity = append(identity, "email")
	}
	if len(identity) > 0 {
		missing = append(missing, "the "+strings.Join(identity, " and ")+" (set DOTFILES_NAME and DOTFILES_EMAIL, or run dot init from a terminal)")
	}
	return missing
}

// configureInteractive walks through each configuration section interactively.
// Skipped entirely when --yes is set or no terminal is attached.
func configureInteractive(
	p *Printer,
	state *config.UserState,
	home string,
	profile string,
	system *config.SystemInfo,
	yes bool,
) error {
	if yes {
		return nil
	}

	p.Line("\n=== Configuration ===")
	freshState := state.Name == ""

	if err := ui.ConfigureIdentity(state, false); err != nil {
		return err
	}

	if err := ui.ConfigureSSH(state, home, false); err != nil {
		return err
	}

	if err := ui.ConfigureWorkspace(state, profile, false); err != nil {
		return err
	}

	if err := ui.ConfigureAI(state, false, freshState); err != nil {
		return err
	}

	if err := ui.ConfigureTerminal(state, profile, system, false); err != nil {
		return err
	}

	if err := ui.ConfigureFonts(state, profile, false); err != nil {
		return err
	}

	ui.PrintStateSummary(state)
	return nil
}
