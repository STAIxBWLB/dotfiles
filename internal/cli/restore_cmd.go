package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/aisettings"
	"github.com/STAIxBWLB/dotfiles/internal/appsettings"
	"github.com/STAIxBWLB/dotfiles/internal/profilesnap"
	"github.com/STAIxBWLB/dotfiles/internal/secrets"
	"github.com/STAIxBWLB/dotfiles/internal/sliceutil"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

// newRestoreCmd returns the top-level `dot restore` one-stop wizard — the
// counterpart of `dot backup` for setting up a (possibly fresh) machine
// from the shared backup root.
func newRestoreCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "restore",
		Short: "One-stop interactive restore (profile, apps, AI, secrets)",
		Long: `Restore everything dot manages from a shared backup root in one run.

The wizard confirms the backup root, picks the source host (any machine
that backed up into the same root — cross-host restore), selects domains
and snapshot versions, optionally runs 'dot apply' after the profile
restore (installs packages including age), then restores secrets, app
settings, and AI settings in a safe order. Existing local files are
preserved in pre-restore backup locations that each step reports.

Order: profile → state reload → apply (optional) → secrets → apps → ai.
A profile restore failure aborts the wizard (later steps depend on the
restored state); any other failure is recorded and the run continues.
--version pins both the profile and AI snapshot; use the individual
commands (dot profile restore / dot ai restore) to mix versions.`,
		Args: cobra.NoArgs,
		RunE: runOnestopRestore,
	}
	c.Flags().String("from", "", "Backup root (overrides configured BackupRoot)")
	c.Flags().String("host", "", "Source hostname to restore from (default: this host)")
	c.Flags().String("version", "", "Snapshot version for selected profile/AI domains (default: latest; must exist in each selected domain)")
	c.Flags().String("scope", "", "Comma-separated domains to restore (profile,apps,ai,secrets)")
	c.Flags().Bool("include-secrets", false, "Restore ~/.ssh/age_key* from the profile snapshot")
	c.Flags().Bool("include-auth", false, "Restore AI auth tokens from the AI snapshot")
	c.Flags().Bool("apply", false, "Run 'dot apply' after the profile restore")
	return c
}

func runOnestopRestore(cmd *cobra.Command, _ []string) error {
	o, err := newOnestopCtx(cmd)
	if err != nil {
		return err
	}
	p := o.p
	p.Header("One-stop Restore")

	// 1. Backup root (no state writes before preflight).
	if err := o.confirmRoot(false); err != nil {
		return err
	}

	// 2. Preflight: anything restorable under this root?
	hosts, err := restorableHosts(o.root)
	if err != nil {
		return err
	}
	if len(hosts) == 0 {
		return fmt.Errorf("no backups found under %s — point --from at a backup root (e.g. your Drive dotfiles-backup folder)", o.root)
	}

	// 3. Source host (single host for the whole session).
	host, err := o.pickSourceHost(hosts)
	if err != nil {
		return err
	}
	o.host = host
	p.KV("Source host", host)

	// 4. Availability scan → scope selection.
	available, labels := o.scanRestoreAvailability()
	if len(available) == 0 {
		return fmt.Errorf("host %s has no restorable snapshots under %s", host, o.root)
	}
	scopes := available
	if raw, _ := cmd.Flags().GetString("scope"); raw != "" {
		scopes, err = parseScope(raw, available)
		if err != nil {
			return err
		}
	} else if !o.yes {
		opts := make([]ui.SelectOption, 0, len(available))
		for _, s := range available {
			opts = append(opts, ui.SelectOption{Label: labels[s], Value: s})
		}
		scopes, err = ui.MultiSelectLabeled("Select what to restore", opts, available, false)
		if err != nil {
			return err
		}
		if len(scopes) == 0 {
			p.Line("aborted (nothing selected)")
			return nil
		}
	}
	selected := make(map[string]bool, len(scopes))
	for _, s := range scopes {
		selected[s] = true
	}

	// 5. Versions (profile/ai) + tag-mismatch warning.
	profileVersion, aiVersion, err := o.pickVersions(selected)
	if err != nil {
		return err
	}

	// 6. Per-domain follow-ups.
	includeSecrets, _ := cmd.Flags().GetBool("include-secrets")
	// Decrypting the .age archives needs the age identity, and resolving
	// which archive is the SSH key needs ssh.key_name — both arrive with the
	// profile snapshot. Unlike the backup side (see planAgeIdentity) this
	// does not pull the profile domain in on its own: a profile restore
	// overwrites the live config.yaml, which is too big a side effect to add
	// silently. Force the age keys when profile is already in scope, and say
	// so plainly when it is not.
	switch {
	case selected["secrets"] && selected["profile"]:
		if !includeSecrets {
			includeSecrets = true
			p.Line("  Restoring age keys — the .age archives cannot be decrypted without them.")
		}
	case selected["secrets"]:
		p.Warn("  secrets without profile: the age identity and ssh.key_name come from the profile snapshot.")
		p.Warn("  Add profile to the scope, or the decrypt will fail (or silently skip the SSH key).")
	case selected["profile"] && !o.yes:
		includeSecrets, err = ui.ConfirmBool("Restore age keys (~/.ssh/age_key*) from the profile snapshot?", includeSecrets, false)
		if err != nil {
			return err
		}
	}
	includeAuth, _ := cmd.Flags().GetBool("include-auth")
	if selected["ai"] && !o.yes {
		includeAuth, err = ui.ConfirmBool("Restore AI auth tokens (OAuth/API credentials)?", includeAuth, false)
		if err != nil {
			return err
		}
	}
	runApplyAfter, _ := cmd.Flags().GetBool("apply")
	if selected["profile"] && !o.yes {
		runApplyAfter, err = ui.ConfirmBool("Run 'dot apply' after the profile restore? (installs packages — may take a while)", runApplyAfter, false)
		if err != nil {
			return err
		}
	}

	// 7. Plan summary + warning + confirmation.
	p.Section("Plan")
	p.KV("Root", o.root)
	p.KV("Source host", host)
	p.KV("Scope", strings.Join(scopes, ", "))
	if selected["profile"] {
		p.KV("Profile ver", orLatest(profileVersion))
		p.KV("Age keys", fmt.Sprintf("%v", includeSecrets))
		p.KV("Apply", fmt.Sprintf("%v", runApplyAfter))
	}
	if selected["ai"] {
		p.KV("AI ver", orLatest(aiVersion))
		p.KV("AI auth", fmt.Sprintf("%v", includeAuth))
	}
	if o.dryRun {
		p.KV("Mode", "dry-run")
	}
	p.Warn("  This overwrites local settings. Quit target apps, AI CLIs, and Maru first.")
	p.Line("  %s", ui.StyleHint.Render("(existing files are preserved in pre-restore backups reported per step)"))
	ok, err := ui.Confirm("Proceed with restore?", o.yes)
	if err != nil {
		return err
	}
	if !ok {
		p.Line("aborted")
		return nil
	}

	// 8. Execute. The profile step is load-bearing: later steps read the
	// restored state, so its failure aborts the wizard. Everything else is
	// independent — failures are recorded and the run continues.
	var steps []onestopStep
	if selected["profile"] {
		step, srcConfig := o.restoreProfileStep(profileVersion, includeSecrets)
		steps = append(steps, step)
		if step.Err != nil {
			printOnestopSummary(p, steps)
			return fmt.Errorf("profile restore failed — aborting (later steps depend on the restored state): %w", step.Err)
		}
		// Sync in-memory state to the restored configuration so later steps
		// (secrets identity/key_name, app backup list) see it; the session
		// root stays pinned — a config.yaml from another machine must not
		// redirect the remaining steps mid-run. In a real run we reload the
		// just-written config; in dry-run config.yaml was never written, so
		// we load the source snapshot directly to keep the preview faithful.
		// Either failure aborts like a profile failure: continuing with the
		// wrong state would restore secrets/apps against it.
		var stateErr error
		if o.dryRun {
			stateErr = o.loadStateFromSnapshot(srcConfig)
		} else {
			stateErr = o.reloadState()
		}
		if stateErr != nil {
			steps = append(steps, onestopStep{Name: "state", Err: stateErr})
			printOnestopSummary(p, steps)
			return fmt.Errorf("state reload failed — aborting (later steps depend on the restored state): %w", stateErr)
		}
		if runApplyAfter {
			steps = append(steps, o.applyStep())
		}
	}
	if selected["secrets"] {
		steps = append(steps, o.restoreSecretsStep())
	}
	if selected["apps"] {
		steps = append(steps, o.restoreAppsStep())
	}
	if selected["ai"] {
		steps = append(steps, o.restoreAIStep(aiVersion, includeAuth))
	}

	// 9. Outcome.
	failed := printOnestopSummary(p, steps)
	if o.state.Modules.MacApps.BackupRoot == "" {
		p.Line("  %s", ui.StyleHint.Render(fmt.Sprintf("Tip: save this backup root with 'dot profile root %s'", o.root)))
	}
	if selected["ai"] {
		p.Line("  %s", ui.StyleHint.Render("Tip: run 'dot ai agents apply' to reapply the agents SSOT to tool targets"))
	}
	if failed > 0 {
		return fmt.Errorf("%d restore step(s) failed", failed)
	}
	p.Success("one-stop restore complete")
	return nil
}

// restorableHosts unions the hosts that left any per-host tree under root.
func restorableHosts(root string) ([]string, error) {
	union := map[string]bool{}
	for _, list := range []func(string) ([]string, error){
		profilesnap.ListHosts,
		appsettings.ListHosts,
		aisettings.ListHosts,
	} {
		hosts, err := list(root)
		if err != nil {
			return nil, err
		}
		for _, h := range hosts {
			union[h] = true
		}
	}
	if entries, err := os.ReadDir(filepath.Join(root, "secrets-age")); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				union[e.Name()] = true
			}
		}
	}
	out := make([]string, 0, len(union))
	for h := range union {
		out = append(out, h)
	}
	sort.Strings(out)
	return out, nil
}

// pickSourceHost resolves the source host: --host flag (validated), the
// only host, the current host when present, or an interactive pick.
func (o *onestopCtx) pickSourceHost(hosts []string) (string, error) {
	if h, _ := o.cmd.Flags().GetString("host"); h != "" {
		if !sliceutil.Contains(hosts, h) {
			return "", fmt.Errorf("host %q has no backups under %s (available: %s)", h, o.root, strings.Join(hosts, ", "))
		}
		return h, nil
	}
	if len(hosts) == 1 {
		return hosts[0], nil
	}
	def := ""
	if sliceutil.Contains(hosts, o.host) {
		def = o.host
	}
	if o.yes {
		if def != "" {
			return def, nil
		}
		return "", fmt.Errorf("multiple source hosts under %s (%s) — pass --host", o.root, strings.Join(hosts, ", "))
	}
	if def == "" {
		def = hosts[0]
	}
	return ui.Select("Restore from which host?", hosts, def, false)
}

// scanRestoreAvailability reports which domains have restorable data for
// the selected source host.
func (o *onestopCtx) scanRestoreAvailability() ([]string, map[string]string) {
	var available []string
	labels := map[string]string{}
	if snaps, err := o.profileEngine().List(); err == nil && len(snaps) > 0 {
		available = append(available, "profile")
		labels["profile"] = fmt.Sprintf("profile — config.yaml + app lists (%d snapshot(s))", len(snaps))
	}
	if runtime.GOOS == "darwin" {
		if _, err := os.Stat(filepath.Join(o.root, "app-settings", o.host)); err == nil {
			available = append(available, "apps")
			labels["apps"] = "apps — macOS app settings"
		}
	}
	if snaps, err := o.aiEngine().List(); err == nil && len(snaps) > 0 {
		available = append(available, "ai")
		labels["ai"] = fmt.Sprintf("ai — AI CLI/agent + Maru settings (%d snapshot(s))", len(snaps))
	}
	if hasAgeArchives(o.secretsArchiveDir()) {
		available = append(available, "secrets")
		labels["secrets"] = "secrets — encrypted .age archives"
	}
	return available, labels
}

// pickVersions resolves the profile/AI snapshot versions ("" = latest) and
// warns when the two latest snapshots carry different tags — a sign that
// one of them came from a different backup run.
func (o *onestopCtx) pickVersions(selected map[string]bool) (profileVersion, aiVersion string, err error) {
	pinned, _ := o.cmd.Flags().GetString("version")
	profileVersion, aiVersion = pinned, pinned

	var profSnaps []profilesnap.Snapshot
	var aiSnaps []aisettings.Snapshot
	if selected["profile"] {
		if profSnaps, err = o.profileEngine().List(); err != nil {
			return "", "", err
		}
	}
	if selected["ai"] {
		if aiSnaps, err = o.aiEngine().List(); err != nil {
			return "", "", err
		}
	}
	if selected["profile"] && selected["ai"] && len(profSnaps) > 0 && len(aiSnaps) > 0 {
		pt, at := profSnaps[0].Tag, aiSnaps[0].Tag
		if pt != "" && at != "" && pt != at {
			o.p.Warn("  Latest profile (tag %q) and AI (tag %q) snapshots come from different backup runs.", pt, at)
		}
	}

	if pinned != "" {
		if selected["profile"] {
			found := false
			for _, s := range profSnaps {
				if s.Version == pinned {
					found = true
					break
				}
			}
			if !found {
				return "", "", fmt.Errorf("profile snapshot version %q not found for host %s", pinned, o.host)
			}
		}
		if selected["ai"] {
			found := false
			for _, s := range aiSnaps {
				if s.Version == pinned {
					found = true
					break
				}
			}
			if !found {
				return "", "", fmt.Errorf("AI snapshot version %q not found for host %s", pinned, o.host)
			}
		}
		return profileVersion, aiVersion, nil
	}
	if o.yes {
		return profileVersion, aiVersion, nil
	}
	useLatest, err := ui.ConfirmBool("Restore the latest snapshots?", true, false)
	if err != nil || useLatest {
		return "", "", err
	}
	if selected["profile"] && len(profSnaps) > 0 {
		options := make([]string, 0, len(profSnaps))
		for _, s := range profSnaps {
			options = append(options, s.Version)
		}
		if profileVersion, err = ui.Select("Profile snapshot version", options, options[0], false); err != nil {
			return "", "", err
		}
	}
	if selected["ai"] && len(aiSnaps) > 0 {
		options := make([]string, 0, len(aiSnaps))
		for _, s := range aiSnaps {
			options = append(options, s.Version)
		}
		if aiVersion, err = ui.Select("AI snapshot version", options, options[0], false); err != nil {
			return "", "", err
		}
	}
	return profileVersion, aiVersion, nil
}

func orLatest(version string) string {
	if version == "" {
		return "latest"
	}
	return version
}

// --- step implementations ---

// restoreProfileStep restores the profile snapshot and returns the step
// plus the path to the snapshot's source config.yaml (empty on error), so
// the caller can sync in-memory state from it during a dry-run where
// config.yaml is never written.
func (o *onestopCtx) restoreProfileStep(version string, includeSecrets bool) (onestopStep, string) {
	eng := o.profileEngine()
	snap, err := eng.Restore(profilesnap.RestoreOptions{
		Version:        version,
		IncludeSecrets: includeSecrets,
		IncludeState:   true,
	})
	if err != nil {
		return onestopStep{Name: "profile", Err: err}, ""
	}
	detail := fmt.Sprintf("version %s → %s", snap.Version, eng.StatePath)
	if snap.PreRestoreBackup != "" {
		o.p.Line("  %s  %s", ui.StyleKey.Render("Previous:"), ui.StyleHint.Render(snap.PreRestoreBackup))
	}
	if includeSecrets && snap.RestoredSecrets == 0 {
		o.p.Warn("  profile: age keys requested but snapshot %s contains none", snap.Version)
	}
	return onestopStep{Name: "profile", Detail: detail}, filepath.Join(snap.Path, "config.yaml")
}

func (o *onestopCtx) applyStep() onestopStep {
	// In dry-run the state reload was skipped, so runApply would preview
	// against the un-restored state and (via apply.go) write config.yaml
	// before its own dry-run gate — short-circuit instead.
	if o.dryRun {
		return onestopStep{Name: "apply", Detail: "dry-run: would run dot apply"}
	}
	// In-process: persistent flags (--yes/--home/...) are inherited via the
	// shared command; runApply reloads state itself.
	if err := runApply(o.cmd, nil); err != nil {
		return onestopStep{Name: "apply", Err: err}
	}
	return onestopStep{Name: "apply", Detail: "dot apply completed"}
}

func (o *onestopCtx) restoreSecretsStep() onestopStep {
	src := o.secretsArchiveDir()
	result, err := secrets.Restore(context.Background(), secrets.RestoreOptions{
		Runner:   o.runner,
		State:    o.state,
		Home:     o.home,
		Src:      src,
		Confirm:  confirmSecrets(o.yes),
		Progress: renderSecretsEvent(o.p),
	})
	if err != nil {
		return onestopStep{Name: "secrets", Err: err}
	}
	detail := fmt.Sprintf("%d restored, %d unchanged, %d skipped",
		result.Restored, result.Unchanged, result.Skipped)
	// Unmatched .age archives (e.g. a key from a host with a different
	// ssh.key_name, or an obsolete leftover) are non-fatal: every entry
	// that maps to the restored config was restored. secrets.Restore
	// already printed a prominent warning with a remediation hint, so we
	// surface the count in the summary detail without failing the run.
	if len(result.Unmatched) > 0 {
		detail += fmt.Sprintf("; %d unmatched archive(s) not restored: %s",
			len(result.Unmatched), strings.Join(result.Unmatched, ", "))
	}
	return onestopStep{Name: "secrets", Detail: detail}
}

func (o *onestopCtx) restoreAppsStep() onestopStep {
	eng, err := o.appsEngine()
	if err != nil {
		return onestopStep{Name: "apps", Err: err}
	}
	adopted := eng.AdoptArchivedApps()
	tokens := sliceutil.Dedupe(append(resolveBackupTokens(o.cmd, eng), adopted...))
	sum, err := eng.Restore(context.Background(), appsettings.RestoreOptions{Tokens: tokens})
	if err != nil {
		return onestopStep{Name: "apps", Err: err}
	}
	if sum.PreBackupPath != "" {
		o.p.Line("  %s  %s", ui.StyleKey.Render("Previous:"), ui.StyleHint.Render(sum.PreBackupPath))
	}
	if !o.dryRun {
		eng.FlushCFPrefsd(context.Background())
	}
	if sum.Failed > 0 {
		return onestopStep{Name: "apps", Err: fmt.Errorf("%d path(s) failed to restore", sum.Failed)}
	}
	return onestopStep{Name: "apps", Detail: fmt.Sprintf("%d app(s), %d file(s)", len(sum.Apps), sum.Files)}
}

func (o *onestopCtx) restoreAIStep(version string, includeAuth bool) onestopStep {
	eng := o.aiEngine()
	sum, err := eng.Restore(aisettings.RestoreOptions{Version: version, IncludeAuth: includeAuth})
	if err != nil {
		return onestopStep{Name: "ai", Err: err}
	}
	auditAIEventBestEffort(o.cmd, "ai.restore", aiSummaryPayload(sum))
	if sum.PreBackupPath != "" {
		o.p.Line("  %s  %s", ui.StyleKey.Render("Previous:"), ui.StyleHint.Render(sum.PreBackupPath))
	}
	return onestopStep{Name: "ai", Detail: fmt.Sprintf("version %s (%d files)", sum.Version, sum.Files)}
}
