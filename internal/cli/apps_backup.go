package cli

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/appsettings"
	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/sliceutil"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

// --- backup ---

func newAppsBackupCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "backup [token...]",
		Short: "Snapshot macOS app settings to the backup archive",
		Long: `Back up macOS application settings listed in the embedded manifest.

Modes:
  - positional args       : back up exactly those tokens.
  - --all                 : back up every manifest entry.
  - --select              : open the checkbox picker even when state has a list.
  - no args + interactive : open the checkbox picker. The list shows the
                            installed casks that also have a manifest entry,
                            plus any custom tokens you added previously.
                            Apps with an existing backup snapshot (or in your
                            saved selection) come pre-ticked. You can also
                            type extra tokens; each is validated against the
                            manifest before being accepted.
  - no args + --yes       : use saved state (falls back to manifest ∩ installed).`,
		Args: cobra.ArbitraryArgs,
		RunE: runAppsBackup,
	}
	c.Flags().String("to", "", "Backup root (overrides configured BackupDir)")
	c.Flags().Bool("all", false, "Back up every manifest entry (default: manifest ∩ installed casks)")
	c.Flags().Bool("select", false, "Force the interactive picker even when state has a list")
	c.Flags().Bool("no-save", false, "Do not persist the interactive selection back to state")
	return c
}

func runAppsBackup(cmd *cobra.Command, args []string) error {
	p := printerFrom(cmd)
	if runtime.GOOS != "darwin" {
		p.Line("%s", ui.StyleWarning.Render("not macOS — apps backup is a no-op"))
		return nil
	}
	yes, _ := cmd.Flags().GetBool("yes")
	useAll, _ := cmd.Flags().GetBool("all")
	forceSelect, _ := cmd.Flags().GetBool("select")
	noSave, _ := cmd.Flags().GetBool("no-save")

	eng, err := newAppsEngine(cmd)
	if err != nil {
		return err
	}

	state, brew, _, err := appsBrewCtx(cmd)
	if err != nil {
		return err
	}

	var tokens []string
	saveAfter := false

	switch {
	case len(args) > 0:
		tokens = eng.AdoptRequestedApps(args, renderAppsEvent(p))
	case useAll:
		tokens = eng.Manifest.Tokens()
	case forceSelect || !yes:
		tokens, saveAfter, err = pickBackupTokens(p, eng, state, brew, noSave)
		if err != nil {
			return err
		}
	default:
		// Saved selections may contain display-name tokens that aren't in
		// the embedded manifest (apps discovered in earlier interactive
		// runs). Re-resolve them before the manifest intersection inside
		// resolveBackupTokens silently drops them; fall back to the archive
		// layout when the app is no longer on disk.
		eng.AdoptSavedApps(state.Modules.MacApps.BackupApps)
		eng.AdoptArchivedApps()
		tokens = resolveBackupTokens(cmd, eng)
	}

	if len(tokens) == 0 {
		return fmt.Errorf("nothing to back up")
	}

	sum, err := eng.Backup(context.Background(), appsettings.BackupOptions{Tokens: tokens})
	if err != nil {
		return err
	}
	printAppSummary(p, "Backup", sum)

	// Record last backup on the in-memory state so a later persist (e.g.
	// the interactive save-selection path) can't overwrite the stamp with
	// a stale pre-backup snapshot of the state file.
	stamped, err := eng.StampLastBackup(sum, tokens)
	if err != nil {
		eng.Runner.Logger.Warn("write last-backup stamp", "err", err)
	}
	if stamped {
		state.Modules.MacApps.LastBackup = &config.BackupRecord{
			Path:  eng.HostRoot(),
			Time:  time.Now(),
			Files: sum.Files,
		}
	}
	if saveAfter {
		if err := persistUserState(cmd, state); err != nil {
			return err
		}
	} else if stamped {
		if err := persistUserState(cmd, state); err != nil {
			eng.Runner.Logger.Warn("record last backup", "err", err)
		}
	}
	if sum.Failed > 0 {
		return fmt.Errorf("%d path(s) failed to back up — their previous archive copies were kept (other paths refreshed)", sum.Failed)
	}
	return nil
}

// pickBackupTokens runs the interactive backup picker.
//
// List construction:
//   - Base options = manifest tokens ∩ installed casks (the apps that are
//     present on this machine AND have backup paths defined).
//   - Options are union-ed with the user's previously saved selection
//     (state.BackupApps), so any custom tokens added in earlier runs remain
//     visible.
//   - All options must exist in the manifest — a token without backup paths
//     would yield an empty snapshot.
//
// Pre-selection: apps whose archive directory already contains files (a prior
// successful backup) OR apps present in state.BackupApps.
//
// Extras: a single comma-separated input so tokens containing spaces
// (e.g. "Moom Classic") work without escaping. Each entry is trimmed and
// rejected unless it appears in the manifest. A warning is shown when the
// entered token isn't currently installed, but the entry is kept (so a
// machine-less backup is still possible).
func pickBackupTokens(p *Printer, eng *appsettings.Engine, state *config.UserState, brew *exec.Brew, noSave bool) ([]string, bool, error) {
	manifestTokens := eng.Manifest.Tokens()
	inManifest := make(map[string]bool, len(manifestTokens))
	for _, t := range manifestTokens {
		inManifest[t] = true
	}

	var installed map[string]bool
	if brew != nil && brew.IsAvailable() {
		installed = brew.InstalledCasks()
	}

	// Apps with an existing archive directory (prior successful backup).
	successSet := make(map[string]bool)
	for _, s := range eng.Status(manifestTokens) {
		if s.PresentBak > 0 {
			successSet[s.Token] = true
		}
	}

	// User's prior selection (includes any custom tokens).
	priorSet := make(map[string]bool)
	for _, t := range state.Modules.MacApps.BackupApps {
		priorSet[t] = true
	}

	// Build option list: installed∩manifest first (manifest order), then
	// additional prior/success entries that weren't installed at query time.
	optionsSet := make(map[string]bool)
	var options []string
	for _, t := range manifestTokens {
		if installed == nil || installed[t] {
			optionsSet[t] = true
			options = append(options, t)
		}
	}
	for _, t := range manifestTokens {
		if optionsSet[t] {
			continue
		}
		if priorSet[t] || successSet[t] {
			optionsSet[t] = true
			options = append(options, t)
		}
	}
	if len(options) == 0 {
		// No brew or no installed matches → fall back to the full manifest so
		// the user still has something to tick.
		options = manifestTokens
	}

	// Preselect: prior selection ∪ prior successful backups (intersected
	// with options so huh doesn't complain about unknown values).
	var preselect []string
	for _, t := range options {
		if priorSet[t] || successSet[t] {
			preselect = append(preselect, t)
		}
	}

	p.Line("%s", ui.StyleHint.Render(fmt.Sprintf(
		"  %d candidate app(s) — pre-ticked: saved selection + previously backed-up",
		len(options))))

	selected, err := ui.MultiSelect("Select apps to back up", options, preselect, false)
	if err != nil {
		return nil, false, err
	}

	// Prior custom entries — tokens the user added by hand in a previous run
	// that aren't surfaced by the checkbox list (either not installed as a
	// cask, or only discoverable by display name like "Moom Classic"). Carry
	// them forward as the default for the free-form input so they don't have
	// to be retyped; the validation loop below will re-resolve each one.
	selectedSet := make(map[string]bool, len(selected))
	for _, t := range selected {
		selectedSet[t] = true
	}
	var priorCustoms []string
	for _, t := range state.Modules.MacApps.BackupApps {
		if selectedSet[t] {
			continue
		}
		if inManifest[t] && installed != nil && installed[t] {
			continue // already in the checkbox list
		}
		priorCustoms = append(priorCustoms, t)
	}

	// Prefill the input with the user's prior custom entries (comma-separated)
	// so they can be edited rather than retyped; the comma separator keeps
	// multi-word tokens like "Moom Classic" intact.
	extraDefault := strings.Join(priorCustoms, ", ")
	p.Line("%s", ui.StyleHint.Render(
		"  Separate multiple entries with commas; spaces inside an entry are kept (e.g. Moom Classic, Hazel)."))
	extraRaw, err := ui.Input("Additional apps", extraDefault, false)
	if err != nil {
		return nil, false, err
	}

	var validExtras []string
	for _, entry := range splitCommaList(extraRaw) {
		if selectedSet[entry] || sliceutil.Contains(validExtras, entry) {
			continue
		}
		if inManifest[entry] {
			if installed != nil && !installed[entry] {
				p.Line("%s", ui.StyleWarning.Render(fmt.Sprintf(
					"  ⚠ %q — not currently installed; backup will skip missing paths", entry)))
			}
			validExtras = append(validExtras, entry)
			continue
		}
		// Not in the embedded manifest — try to discover the app on disk by
		// name (e.g. "Moom Classic") and synthesize a runtime entry. Accept
		// it only if we can read its bundle identifier and find at least one
		// standard Library location.
		discovered := appsettings.DiscoverApp(eng.HomeDir, entry)
		if discovered == nil {
			p.Line("%s", ui.StyleError.Render(fmt.Sprintf(
				"  ✗ %q — .app bundle not found and not in manifest; skipped", entry)))
			continue
		}
		eng.Manifest.Apps = append(eng.Manifest.Apps, *discovered)
		inManifest[entry] = true
		p.Line("%s", ui.StyleSuccess.Render(fmt.Sprintf(
			"  ✓ %q — discovered %d backup path(s)", entry, len(discovered.Paths))))
		validExtras = append(validExtras, entry)
	}

	tokens := sliceutil.Dedupe(append(append([]string(nil), selected...), validExtras...))
	if len(tokens) == 0 {
		return nil, false, fmt.Errorf("no apps selected for backup")
	}

	if noSave {
		return tokens, false, nil
	}
	save, err := ui.ConfirmBool("Save this selection to state?", true, false)
	if err != nil {
		return nil, false, err
	}
	if !save {
		return tokens, false, nil
	}
	state.Modules.MacApps.Enabled = true
	state.Modules.MacApps.BackupApps = tokens
	return tokens, true, nil
}

// splitCommaList parses a strictly comma-separated list into trimmed entries.
// Unlike splitTokenList it preserves internal whitespace, so values like
// "Moom Classic" survive intact.
func splitCommaList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return sliceutil.Dedupe(out)
}

// --- restore ---

func newAppsRestoreCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "restore [token...]",
		Short: "Restore macOS app settings from the backup archive",
		Args:  cobra.ArbitraryArgs,
		RunE:  runAppsRestore,
	}
	c.Flags().String("from", "", "Backup root (overrides configured BackupDir)")
	c.Flags().String("host", "", "Source hostname to restore from (default: this host)")
	c.Flags().Bool("all", false, "Restore every manifest entry")
	return c
}

func runAppsRestore(cmd *cobra.Command, args []string) error {
	p := printerFrom(cmd)
	if runtime.GOOS != "darwin" {
		p.Line("%s", ui.StyleWarning.Render("not macOS — apps restore is a no-op"))
		return nil
	}
	yes, _ := cmd.Flags().GetBool("yes")
	eng, err := newAppsEngine(cmd)
	if err != nil {
		return err
	}
	host, err := hostOverride(cmd, eng.Hostname)
	if err != nil {
		return err
	}
	eng.Hostname = host

	// Apps captured by name discovery on the source machine live only in
	// the archive — synthesize entries for them so they restore too.
	adopted := eng.AdoptArchivedApps()

	tokens := args
	if len(tokens) == 0 {
		tokens = resolveBackupTokens(cmd, eng)
		tokens = sliceutil.Dedupe(append(tokens, adopted...))
	}

	if !yes {
		p.Line("%s", ui.StyleWarning.Render("This overwrites local app settings. Quit target apps first."))
		p.Line("%s", ui.StyleHint.Render("  (existing files are snapshotted under ~/.local/share/dotfiles/backup/app-settings/ first)"))
		ok, err := ui.ConfirmBool("Continue with restore?", false, false)
		if err != nil {
			return err
		}
		if !ok {
			p.Line("aborted")
			return nil
		}
	}

	sum, err := eng.Restore(context.Background(), appsettings.RestoreOptions{Tokens: tokens})
	if err != nil {
		return err
	}
	printAppSummary(p, "Restore", sum)
	if sum.PreBackupPath != "" {
		p.Line("  %s  %s", ui.StyleKey.Render("Previous:"), ui.StyleHint.Render(sum.PreBackupPath))
	}
	// The flush stays on this side of the seam: killall cfprefsd warns on a
	// host with no cfprefsd running, and moving it into the engine entry
	// would put that warning ahead of the summary above.
	if !eng.Runner.DryRun {
		eng.FlushCFPrefsd(context.Background())
	}
	if sum.Failed > 0 {
		return fmt.Errorf("%d path(s) failed to restore", sum.Failed)
	}
	return nil
}

func printAppSummary(p *Printer, label string, sum *appsettings.Summary) {
	p.Header(label + " Summary")
	for _, a := range sum.Apps {
		line := fmt.Sprintf("%s  paths: %d copied / %d missing  files: %d  bytes: %d",
			ui.StyleValue.Render(a.Token), a.Copied, a.Missing, a.Files, a.Bytes)
		marker := ui.StyleHint.Render(ui.MarkPartial)
		if a.Failed > 0 {
			line += "  " + ui.StyleError.Render(fmt.Sprintf("failed: %d", a.Failed))
			marker = ui.StyleError.Render(ui.MarkFail)
		}
		p.Bullet(marker, line)
	}
	p.Blank()
	p.Line("  Total: %d file(s), %d byte(s)", sum.Files, sum.Bytes)
	if sum.Failed > 0 {
		p.Warn("  %d path(s) failed", sum.Failed)
	}
}
