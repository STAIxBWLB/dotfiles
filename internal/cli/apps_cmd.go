package cli

import (
	"context"
	"fmt"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/appsettings"
	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/sliceutil"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

// newAppsCmd returns the `dot apps` command with subcommands for
// macOS application install + settings backup/restore.
func newAppsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apps",
		Short: "macOS app install and settings backup/restore",
		Long: `Manage macOS cask applications and their user settings.

Subcommands:
  list     Show the embedded cask catalog (groups, defaults).
  install  Install the selected casks (uses saved state, brew install --cask).
  status   Report install + backup presence for each tracked app.
  backup   Copy app settings to the host-scoped backup archive.
  restore  Copy app settings back from the archive.`,
	}
	cmd.AddCommand(newAppsListCmd())
	cmd.AddCommand(newAppsInstallCmd())
	cmd.AddCommand(newAppsStatusCmd())
	cmd.AddCommand(newAppsBackupCmd())
	cmd.AddCommand(newAppsRestoreCmd())
	return cmd
}

// renderAppsEvent turns engine progress into the lines the apps commands
// have always printed. One renderer for every call site, so the wording
// cannot drift apart between install and backup.
func renderAppsEvent(p *Printer) func(appsettings.Event) {
	return func(e appsettings.Event) {
		switch e.Kind {
		case appsettings.EventAllInstalled:
			p.Line("%s", ui.StyleSuccess.Render("✓ all selected casks already installed"))
		case appsettings.EventSkippedExisting:
			p.Line("%s", ui.StyleHint.Render(fmt.Sprintf(
				"↷ skipping %d already-present app(s): %s  (use --force to reinstall)",
				len(e.Tokens), strings.Join(e.Tokens, ", "))))
		case appsettings.EventNothingToInstall:
			p.Line("%s", ui.StyleSuccess.Render("✓ nothing to install"))
		case appsettings.EventDryRunTap:
			p.Line("dry-run: would tap %d Homebrew repo(s): %s", len(e.Tokens), strings.Join(e.Tokens, ", "))
		case appsettings.EventDryRunInstall:
			p.Line("dry-run: would install %d cask(s): %s", len(e.Tokens), strings.Join(e.Tokens, ", "))
		case appsettings.EventTapping:
			p.Line("Tapping %d Homebrew repo(s): %s", len(e.Tokens), strings.Join(e.Tokens, ", "))
		case appsettings.EventInstalling:
			p.Line("Installing %d cask(s): %s", len(e.Tokens), strings.Join(e.Tokens, ", "))
		case appsettings.EventInstallComplete:
			p.Line("%s", ui.StyleSuccess.Render("✓ install complete"))
		case appsettings.EventAppDiscovered:
			p.Line("%s", ui.StyleSuccess.Render(fmt.Sprintf(
				"  ✓ %q — discovered %d backup path(s)", e.Token, e.Paths)))
		case appsettings.EventAppNotFound:
			p.Line("%s", ui.StyleWarning.Render(fmt.Sprintf(
				"  ⚠ %q — not in manifest and .app bundle not found; will be ignored", e.Token)))
		}
	}
}

// --- list ---

func newAppsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "Show the cask catalog (groups + defaults)",
		Args:  cobra.NoArgs,
		RunE:  runAppsList,
	}
}

func runAppsList(cmd *cobra.Command, _ []string) error {
	res, err := appsettings.List(appsettings.ListOptions{Brew: appsBrewForQuery(cmd)})
	if err != nil {
		return err
	}

	p := printerFrom(cmd)
	p.Header("macOS Cask Catalog")
	for _, g := range res.Groups {
		p.Section(g.Name)
		for _, a := range g.Apps {
			marks := []string{}
			if a.Default {
				marks = append(marks, ui.StyleSuccess.Render(ui.MarkStarred))
			}
			if a.Recommended && !a.Default {
				marks = append(marks, ui.StyleSuccess.Render(ui.MarkPreferred))
			}
			if a.Installed {
				marks = append(marks, ui.StyleSuccess.Render(ui.MarkPresent))
			}
			marker := strings.Join(marks, " ")
			if marker == "" {
				marker = " "
			}
			p.Bullet(marker, fmt.Sprintf("%s  %s",
				ui.StyleValue.Render(a.Token),
				ui.StyleHint.Render(a.Name)))
		}
	}
	p.Blank()
	p.Line("  %s", ui.StyleHint.Render(ui.MarkStarred+" default   "+ui.MarkPreferred+" recommended   "+ui.MarkPresent+" installed"))
	return nil
}

// --- install ---

func newAppsInstallCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "install [token...]",
		Short: "Install macOS cask apps (interactive by default; args skip the picker)",
		Long: `Install macOS cask applications.

Modes:
  - positional args       : install exactly those tokens.
  - --defaults            : install the catalog's default set.
  - --recommended         : install the catalog's recommended set.
  - --all                 : install every cask in the catalog.
  - --select              : open the checkbox picker even when state is set.
  - no args + interactive : open the checkbox picker, preselected from saved state.
  - no args + --yes       : use saved state (falls back to catalog recommended).

Casks whose .app already exists under /Applications (e.g. installed via the
App Store or downloaded directly) are skipped by default. Pass --force to
reinstall them over the existing bundle.

After an interactive run, the updated selection can be saved back to the user
state file so subsequent 'dot apply' runs honor it.`,
		Args: cobra.ArbitraryArgs,
		RunE: runAppsInstall,
	}
	c.Flags().Bool("defaults", false, "Install the catalog's default set regardless of saved state")
	c.Flags().Bool("recommended", false, "Install the catalog's recommended set regardless of saved state")
	c.Flags().Bool("all", false, "Install every app in the catalog")
	c.Flags().Bool("select", false, "Force the interactive picker even when state has a list")
	c.Flags().Bool("no-save", false, "Do not persist the interactive selection back to state")
	c.Flags().Bool("force", false, "Reinstall even when the .app already exists under /Applications")
	return c
}

func runAppsInstall(cmd *cobra.Command, args []string) error {
	p := printerFrom(cmd)
	if runtime.GOOS != "darwin" {
		p.Line("%s", ui.StyleWarning.Render("not macOS — apps install is a no-op"))
		return nil
	}
	yes, _ := cmd.Flags().GetBool("yes")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	useDefaults, _ := cmd.Flags().GetBool("defaults")
	useRecommended, _ := cmd.Flags().GetBool("recommended")
	useAll, _ := cmd.Flags().GetBool("all")
	forceSelect, _ := cmd.Flags().GetBool("select")
	noSave, _ := cmd.Flags().GetBool("no-save")
	force, _ := cmd.Flags().GetBool("force")

	if useDefaults && useRecommended {
		return fmt.Errorf("--defaults and --recommended are mutually exclusive")
	}

	state, brew, _, err := appsBrewCtx(cmd)
	if err != nil {
		return err
	}
	if !brew.IsAvailable() {
		return fmt.Errorf("homebrew not available")
	}

	sel, err := appsettings.SelectInstall(appsettings.InstallSelectOptions{
		Args:              args,
		All:               useAll,
		Defaults:          useDefaults,
		Recommended:       useRecommended,
		ForceSelect:       forceSelect,
		Yes:               yes,
		SavedCasks:        state.Modules.MacApps.Casks,
		SavedCasksExtra:   state.Modules.MacApps.CasksExtra,
		SavedTerminalApps: state.Modules.TerminalApps.Apps,
	})
	if err != nil {
		return err
	}

	want := sel.Tokens
	saveAfter := false
	if sel.Interactive {
		p.Line("%s", ui.StyleHint.Render(fmt.Sprintf(
			"  Catalog: %d apps across %d groups  (★ defaults, ✓ installed)", len(sel.PickTokens), sel.CatalogGroups)))
		selected, err := ui.MultiSelect("Pick apps to install", sel.PickTokens, sel.Preselect, false)
		if err != nil {
			return err
		}
		extraDefault := strings.Join(state.Modules.MacApps.CasksExtra, " ")
		extraRaw, err := ui.Input("Additional casks (space-separated, optional)", extraDefault, false)
		if err != nil {
			return err
		}
		extra := splitTokenList(extraRaw)
		want = sliceutil.Dedupe(append(append([]string(nil), selected...), extra...))

		if !noSave {
			save, err := ui.ConfirmBool("Save this selection to state?", true, false)
			if err != nil {
				return err
			}
			if save {
				saveAfter = true
				state.Modules.MacApps.Enabled = true
				state.Modules.MacApps.Casks = selected
				state.Modules.MacApps.CasksExtra = extra
			}
		}
	}

	if err := appsettings.Install(context.Background(), appsettings.InstallOptions{
		Brew:     brew,
		Tokens:   want,
		Force:    force,
		DryRun:   dryRun,
		Progress: renderAppsEvent(p),
	}); err != nil {
		return err
	}
	if saveAfter {
		return persistUserState(cmd, state)
	}
	return nil
}

// splitTokenList parses whitespace/comma-separated tokens into a clean list.
func splitTokenList(s string) []string {
	replacer := strings.NewReplacer(",", " ", "\t", " ")
	parts := strings.Fields(replacer.Replace(s))
	return sliceutil.Dedupe(parts)
}

// persistUserState writes user state honoring the --home override.
func persistUserState(cmd *cobra.Command, state *config.UserState) error {
	homeOverride, _ := cmd.Flags().GetString("home")
	if homeOverride != "" {
		return config.SaveStateForHome(homeOverride, state)
	}
	return config.SaveState(state)
}

// --- status ---

func newAppsStatusCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "status",
		Short: "Show install + backup presence for tracked apps",
		Args:  cobra.NoArgs,
		RunE:  runAppsStatus,
	}
	c.Flags().String("from", "", "Backup root to inspect (overrides configured BackupDir)")
	c.Flags().String("host", "", "Hostname to inspect (default: this host)")
	return c
}

// appsInstallMarker maps an app's install state to a (marker, style) tuple,
// in the shape agentDriftMarker already establishes. Three states, because
// StatusApp carries three: installed, known-and-not-installed, and install
// state unknown — no brew, a non-macOS host, or a failed state load.
func appsInstallMarker(installKnown, installed bool) (string, interface{ Render(...string) string }) {
	switch {
	case !installKnown:
		return ui.MarkPartial, ui.StyleHint
	case installed:
		return ui.MarkPresent, ui.StyleSuccess
	default:
		// Absent in the neutral style: Homebrew was asked and said no,
		// which is a fact rather than a failure (BUG-10, D-07).
		return ui.MarkAbsent, ui.StyleHint
	}
}

func runAppsStatus(cmd *cobra.Command, _ []string) error {
	eng, err := newAppsEngine(cmd)
	if err != nil {
		return err
	}
	host, err := hostOverride(cmd, eng.Hostname)
	if err != nil {
		return err
	}
	eng.Hostname = host

	res := eng.StatusReport(appsettings.StatusOptions{Brew: appsBrewForQuery(cmd)})

	p := printerFrom(cmd)
	p.Header("macOS App Settings Status")
	p.KV("Host", res.Hostname)
	p.KV("Backup", res.HostRoot)
	p.Section("Apps")

	for _, s := range res.Apps {
		mark, style := appsInstallMarker(s.InstallKnown, s.Installed)
		marker := style.Render(mark)
		live := fmt.Sprintf("%d/%d", s.PresentLive, s.TotalLive)
		bak := fmt.Sprintf("%d/%d", s.PresentBak, s.TotalBak)
		switch s.PresentBak {
		case 0:
			bak = ui.StyleHint.Render(bak)
		case s.TotalBak:
			bak = ui.StyleSuccess.Render(bak)
		default:
			bak = ui.StyleWarning.Render(bak)
		}
		p.Bullet(marker, fmt.Sprintf("%-22s  live:%-6s  backup:%-8s",
			ui.StyleValue.Render(s.Token), live, bak))
	}
	return nil
}
