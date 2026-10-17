package cli

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/module"
	"github.com/STAIxBWLB/dotfiles/internal/template"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

func newCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check",
		Short: "Check current state against profile",
		Long:  "Check which modules are satisfied and which need changes.",
		RunE:  runCheck,
	}
}

func runCheck(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	profileName, _ := cmd.Flags().GetString("profile")
	moduleFilter, _ := cmd.Flags().GetStringSlice("module")
	configPath, _ := cmd.Flags().GetString("config")
	homeOverride, _ := cmd.Flags().GetString("home")

	if profileName == "" {
		profileName = os.Getenv("DOTFILES_PROFILE")
	}
	if homeOverride == "" {
		homeOverride = os.Getenv("DOTFILES_HOME")
	}

	home := homeOverride
	if home == "" {
		home, _ = os.UserHomeDir()
	}

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

	if profileName == "" && state.Profile != "" {
		profileName = state.Profile
	}

	sysInfo, err := config.DetectSystem()
	if err != nil {
		return fmt.Errorf("detecting system: %w", err)
	}

	if profileName == "" && configPath == "" {
		profileName = sysInfo.SuggestProfile()
	}

	cfg, err := config.Load(profileName, configPath, sysInfo)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	applyStateWithToolingAuthority(cfg, state, configPath != "")
	config.ApplyEnvOverrides(cfg)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	}))
	runner := exec.NewRunner(true, logger) // always dry-run for check
	brew := exec.NewBrew(runner)
	tmplEngine := template.NewEngine()

	registry := module.NewRegistry()
	moduleFilter, err = module.NormalizeFilter(moduleFilter)
	if err != nil {
		return err
	}
	modules := registry.Resolve(cfg, moduleFilter)

	rc := &module.RunContext{
		ExplicitHome: homeOverride != "",
		Config:       cfg,
		Runner:       runner,
		Brew:         brew,
		Template:     tmplEngine,
		DryRun:       true,
		Yes:          true,
		HomeDir:      home,
	}

	results, err := module.CheckAll(ctx, modules, rc)
	if err != nil {
		return err
	}

	// Print report
	p := printerFrom(cmd)
	p.Header("Module Check")
	p.KV("Profile", profileName)

	p.Section("Modules")
	okCount, pendingCount, totalChanges := 0, 0, 0
	for _, m := range modules {
		r := results[m.Name()]
		changeCount := len(r.Changes)
		marker := ui.StyleSuccess.Render(ui.MarkPresent)
		detail := ui.StyleHint.Render("satisfied")
		if !r.Satisfied {
			marker = ui.StyleHint.Render(ui.MarkPending)
			detail = ui.StyleWarning.Render(fmt.Sprintf("%d pending change(s)", changeCount))
			pendingCount++
			totalChanges += changeCount
		} else {
			okCount++
		}
		p.Bullet(marker, fmt.Sprintf("%-15s %s", ui.StyleValue.Render(m.Name()), detail))
		for _, c := range r.Changes {
			p.Line("      → %s", c.Description)
		}
	}

	p.Blank()
	if pendingCount == 0 {
		p.Success("All %d modules satisfied.", okCount)
	} else {
		p.Line("%d/%d modules satisfied, %d pending (%d change(s)).",
			okCount, okCount+pendingCount, pendingCount, totalChanges)
		p.Line("Run 'dot apply' to apply pending changes.")
	}

	return nil
}
