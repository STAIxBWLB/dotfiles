package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/module"
	"github.com/STAIxBWLB/dotfiles/internal/secrets"
	"github.com/STAIxBWLB/dotfiles/internal/syncer"
	"github.com/STAIxBWLB/dotfiles/internal/template"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
	"github.com/STAIxBWLB/dotfiles/internal/workspace"
)

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show full environment status at a glance",
		Long:  "Unified dashboard: system, user, modules, secrets, sync, and workspace projects.",
		RunE:  runStatus,
	}
}

func runStatus(cmd *cobra.Command, _ []string) error {
	p := printerFrom(cmd)
	p.Header("dot Status")

	// ── resolve the home every section reports on ──────────────────────
	// The same four steps check.go:30-51, apply.go and diff.go already take.
	// Precedence: --home wins, $DOTFILES_HOME is the fallback, the process
	// home is last — and the override outranks XDG_CONFIG_HOME, because
	// config.StatePathForHome joins the home directly without consulting the
	// variable, which is what those three commands already do when the flag
	// is set. This follows that precedent rather than inventing an ordering.
	homeOverride := homeOverrideFrom(cmd)
	home := homeOverride
	if home == "" {
		home, _ = os.UserHomeDir()
	}

	// ── load shared state ──────────────────────────────────────────────
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

	sysInfo, _ := config.DetectSystem()

	// ── System ─────────────────────────────────────────────────────────
	p.Section("System")
	if sysInfo != nil {
		p.KV("OS", sysInfo.OS+"/"+sysInfo.Arch)
		p.KV("Hostname", sysInfo.Hostname)
		p.KV("Shell", sysInfo.Shell)
		if sysInfo.HasGit {
			p.KV("Git", sysInfo.GitVersion)
		}
		if sysInfo.HasBrew {
			p.KV("Brew", sysInfo.BrewPath)
		}
		if sysInfo.HasNVIDIAGPU {
			p.KV("GPU", sysInfo.GPUModel)
		}
		if sysInfo.HasCUDA {
			p.KV("CUDA", sysInfo.CUDAHome)
		}
	}

	// ── User ───────────────────────────────────────────────────────────
	p.Section("User")
	p.KV("Name", state.Name)
	p.KV("Email", state.Email)
	p.KV("GitHub", state.GithubUser)
	p.KV("Profile", state.Profile)
	statePath := config.StatePath()
	if homeOverride != "" {
		statePath = config.StatePathForHome(homeOverride)
	}
	p.KV("Config", statePath)

	// ── Modules ────────────────────────────────────────────────────────
	statusPrintModules(p, cmd, state, sysInfo, home)

	// ── Secrets ────────────────────────────────────────────────────────
	statusPrintSecrets(p, state, home)

	// ── Sync ───────────────────────────────────────────────────────────
	statusPrintSync(p, state, home)

	// ── Workspace ──────────────────────────────────────────────────────
	statusPrintWorkspace(p, home)

	p.Blank()
	return nil
}

// statusPrintModules checks all enabled modules and prints a compact summary.
// home is the caller's resolved --home target: the RunContext every module
// check reads must point at it, not at the invoking user.
func statusPrintModules(p *Printer, cmd *cobra.Command, state *config.UserState, sysInfo *config.SystemInfo, home string) {
	profileName, _ := cmd.Flags().GetString("profile")
	configPath, _ := cmd.Flags().GetString("config")
	if profileName == "" {
		profileName = os.Getenv("DOTFILES_PROFILE")
	}
	if profileName == "" {
		profileName = state.Profile
	}
	if profileName == "" && sysInfo != nil && configPath == "" {
		profileName = sysInfo.SuggestProfile()
	}

	cfg, err := config.Load(profileName, configPath, sysInfo)
	if err != nil {
		p.Section("Modules")
		p.Line("  %s", ui.StyleHint.Render("(could not load config: "+err.Error()+")"))
		return
	}
	config.ApplyStateToConfig(cfg, state)
	config.ApplyEnvOverrides(cfg)

	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	runner := exec.NewRunner(true, logger)
	brew := exec.NewBrew(runner)
	tmplEngine := template.NewEngine()

	registry := module.NewRegistry()
	modules := registry.Resolve(cfg, nil)

	rc := &module.RunContext{
		Config:   cfg,
		Runner:   runner,
		Brew:     brew,
		Template: tmplEngine,
		DryRun:   true,
		Yes:      true,
		HomeDir:  home,
	}

	results, err := module.CheckAll(ctx, modules, rc)
	if err != nil {
		p.Section("Modules")
		p.Line("  %s", ui.StyleHint.Render("(check failed: "+err.Error()+")"))
		return
	}

	satisfied := 0
	total := len(modules)
	for _, m := range modules {
		if r := results[m.Name()]; r != nil && r.Satisfied {
			satisfied++
		}
	}

	p.Section(fmt.Sprintf("Modules (%d/%d satisfied)", satisfied, total))
	for _, m := range modules {
		r := results[m.Name()]
		marker := ui.StyleHint.Render(ui.MarkAbsent)
		if r != nil && r.Satisfied {
			marker = ui.StyleSuccess.Render(ui.MarkPresent)
		}
		p.Bullet(marker, ui.StyleValue.Render(m.Name()))
	}

	if pending := total - satisfied; pending > 0 {
		p.Blank()
		p.Line("  %s", ui.StyleHint.Render(
			fmt.Sprintf("%d module(s) need attention — run 'dot check' for details.", pending)))
	}
}

// statusPrintSecrets prints a compact secrets summary for the given home.
func statusPrintSecrets(p *Printer, state *config.UserState, home string) {
	p.Section("Secrets")

	if home == "" {
		p.Line("  %s", ui.StyleHint.Render("(cannot resolve home dir)"))
		return
	}

	keyName := state.SSH.KeyName
	if keyName == "" {
		keyName = "id_ed25519"
	}

	fileStatus := func(path string) string {
		if _, err := os.Stat(path); err == nil {
			return "present"
		}
		return "missing"
	}

	p.KV("SSH key", fileStatus(filepath.Join(home, ".ssh", keyName)))
	p.KV("Shell secrets", fileStatus(filepath.Join(home, ".config", "shell", "90-secrets.sh")))

	// Count .age files. The store-relative constant is the single spelling
	// of that path: onestop.go joins it against its session home too.
	storeDir := filepath.Join(home, secrets.StoreDirRel)
	ageCount := 0
	if entries, err := os.ReadDir(storeDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() && filepath.Ext(e.Name()) == ".age" {
				ageCount++
			}
		}
	}
	p.KV("Encrypted", fmt.Sprintf("%d file(s)", ageCount))

	if lb := state.Secrets.LastBackup; lb != nil && lb.Path != "" {
		p.KV("Secrets last backup", fmt.Sprintf("%s (%s ago, %d files)",
			lb.Path, humanDuration(time.Since(lb.Time)), lb.Files))
	} else {
		p.KV("Secrets last backup", "(none)")
	}
	if lb := state.Modules.MacApps.LastBackup; lb != nil && lb.Path != "" {
		p.KV("Apps last backup", fmt.Sprintf("%s (%s ago, %d files)",
			lb.Path, humanDuration(time.Since(lb.Time)), lb.Files))
	} else {
		p.KV("Apps last backup", "(none)")
	}
}

// statusPrintSync prints a compact workspace sync (mirror) summary for the
// given home.
func statusPrintSync(p *Printer, state *config.UserState, home string) {
	p.Section("Sync")

	// The default profile is passed explicitly: which profile a status read
	// reports on is a decision this call site makes, not one a resolver makes
	// for it.
	cfg, err := syncer.ResolveConfigReadOnlyForHomeProfile(state, home, syncer.DefaultProfile)
	if err != nil {
		p.Line("  %s", ui.StyleHint.Render("(config error: "+err.Error()+")"))
		return
	}
	// BUG-28 (pre-change site: internal/cli/status_cmd.go:249): the scheduler
	// was built from an artifact layout re-resolved from the home alone, which
	// dropped the profile the config had just been resolved for; it now reads
	// the layout that same resolve produced. The substituted value is
	// byte-identical at HEAD -- the read-only home entry point hard-coded the
	// default profile and withProfile is the identity for it -- so nothing in
	// this section's output moves, and the guard against a partial resolution
	// returning is the structural inventory, not an output assertion.
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	runner := exec.NewRunner(true, logger)
	engine := template.NewEngine()
	sched := syncer.NewScheduler(runner, cfg.SystemPaths, cfg, engine)
	st, err := syncer.GetStatus(context.Background(), runner, cfg, state, sched)
	if err != nil {
		p.Line("  %s", ui.StyleHint.Render("(status unavailable: "+err.Error()+")"))
		return
	}

	p.KV("Local", st.LocalPath)
	p.KV("Mirror", st.MirrorPath)
	if st.Paused {
		p.KV("Paused", "yes")
	} else {
		p.KV("Paused", "no")
	}
	p.KV("Last push", formatLastSync(st.LastPush))
	p.KV("Last pull", formatLastSync(st.LastPull))
}

// statusPrintWorkspace lists the given home's registered projects and the
// active tmux sessions.
func statusPrintWorkspace(p *Printer, home string) {
	cfg, err := workspace.LoadConfigForHome(home)
	if err != nil {
		p.Section("Workspace")
		p.Line("  %s", ui.StyleHint.Render("(could not load config: "+err.Error()+")"))
		return
	}

	p.Section(fmt.Sprintf("Workspace (%d projects)", len(cfg.Projects)))

	if len(cfg.Projects) == 0 {
		p.Line("  %s", ui.StyleHint.Render("(none — use 'dot register <name>' to add one)"))
		return
	}

	// Collect active tmux sessions
	active := make(map[string]bool)
	runner := exec.NewRunner(false, slog.Default())
	res, err := runner.RunQuery(context.Background(), "tmux", "list-sessions", "-F", "#{session_name}")
	if err == nil {
		for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
			if line != "" {
				active[line] = true
			}
		}
	}

	for _, proj := range cfg.Projects {
		marker := ui.StyleHint.Render(ui.MarkPartial)
		if active[proj.Name] {
			marker = ui.StyleSuccess.Render(ui.MarkStarred)
		}
		p.Bullet(marker, fmt.Sprintf("%s  %s",
			ui.StyleValue.Render(fmt.Sprintf("%-18s", proj.Name)),
			ui.StyleHint.Render(proj.Path)))
	}
}
