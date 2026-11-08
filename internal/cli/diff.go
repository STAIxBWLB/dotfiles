package cli

import (
	"context"
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

func newDiffCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Show pending changes without applying",
		Long: `Show what would change if 'dot apply' were run (dry-run with verbose change details).

--summary lists each module's pending change descriptions without the
commands, the report 'dot check' printed before it was folded into diff.`,
		RunE: runDiff,
	}
	cmd.Flags().Bool("summary", false, "List pending changes per module without the commands")
	return cmd
}

// newCheckCmd keeps the old name working for one release.
func newCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "check",
		Short:  "Deprecated: use dot diff --summary",
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			warnRenamed(cmd, "dot check", "dot diff --summary")
			return runModulePlan(cmd, true)
		},
	}
}

// warnRenamed prints the one-release compatibility notice on stderr, so
// scripts that parse stdout see the same report under the old name.
func warnRenamed(cmd *cobra.Command, oldName, newName string) {
	fmt.Fprintf(cmd.ErrOrStderr(), "Warning: %s is deprecated; use '%s' instead. The old name will be removed after one release.\n", oldName, newName)
}

func runDiff(cmd *cobra.Command, _ []string) error {
	summary, _ := cmd.Flags().GetBool("summary")
	return runModulePlan(cmd, summary)
}

// modulePlan is a dry-run Check of every selected module: the bootstrap
// check and diff used to duplicate.
type modulePlan struct {
	profile string
	modules []module.Module
	results map[string]*module.CheckResult
}

func loadModulePlan(ctx context.Context, cmd *cobra.Command) (*modulePlan, error) {
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
		return nil, fmt.Errorf("loading state: %w", err)
	}

	if profileName == "" && state.Profile != "" {
		profileName = state.Profile
	}

	sysInfo, err := config.DetectSystem()
	if err != nil {
		return nil, fmt.Errorf("detecting system: %w", err)
	}

	if profileName == "" && configPath == "" {
		profileName = sysInfo.SuggestProfile()
	}

	cfg, err := config.Load(profileName, configPath, sysInfo)
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}

	applyStateWithToolingAuthority(cfg, state, configPath != "")
	config.ApplyEnvOverrides(cfg)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	}))
	runner := exec.NewRunner(true, logger) // always dry-run
	moduleFilter, err = module.NormalizeFilter(moduleFilter)
	if err != nil {
		return nil, err
	}
	modules := module.NewRegistry().Resolve(cfg, moduleFilter)

	rc := &module.RunContext{
		ExplicitHome: homeOverride != "",
		Config:       cfg,
		Runner:       runner,
		Brew:         exec.NewBrew(runner),
		Template:     template.NewEngine(),
		DryRun:       true,
		Yes:          true,
		HomeDir:      home,
	}

	results, err := module.CheckAll(ctx, modules, rc)
	if err != nil {
		return nil, err
	}
	return &modulePlan{profile: profileName, modules: modules, results: results}, nil
}

func runModulePlan(cmd *cobra.Command, summary bool) error {
	plan, err := loadModulePlan(cmd.Context(), cmd)
	if err != nil {
		return err
	}
	p := printerFrom(cmd)
	if summary {
		printPlanSummary(p, plan)
	} else {
		printPlanDiff(p, plan)
	}
	return nil
}

func printPlanSummary(p *Printer, plan *modulePlan) {
	p.Header("Module Check")
	p.KV("Profile", plan.profile)

	p.Section("Modules")
	okCount, pendingCount, totalChanges := 0, 0, 0
	for _, m := range plan.modules {
		r := plan.results[m.Name()]
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
}

func printPlanDiff(p *Printer, plan *modulePlan) {
	p.Header("Dry-run Diff")
	p.KV("Profile", plan.profile)
	p.Section("Modules")

	pendingCount := 0
	for _, m := range plan.modules {
		r := plan.results[m.Name()]
		if r.Satisfied {
			marker := ui.StyleSuccess.Render(ui.MarkPresent)
			p.Bullet(marker, fmt.Sprintf("%-15s %s", ui.StyleValue.Render(m.Name()), ui.StyleHint.Render("already satisfied")))
			continue
		}
		pendingCount++
		marker := ui.StyleHint.Render(ui.MarkPending)
		p.Bullet(marker, fmt.Sprintf("%-15s %s", ui.StyleValue.Render(m.Name()),
			ui.StyleWarning.Render(fmt.Sprintf("%d pending change(s)", len(r.Changes)))))
		for _, c := range r.Changes {
			p.Line("      → %s", c.Description)
			if c.Command != "" {
				p.Line("        $ %s", c.Command)
			}
		}
	}

	p.Blank()
	if pendingCount == 0 {
		p.Success("Nothing to do — all modules satisfied.")
	} else {
		p.Line("%d module(s) have pending changes. Run 'dot apply' to apply them.", pendingCount)
	}
}
