package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/STAIxBWLB/dotfiles/internal/resourceguard"
	"github.com/STAIxBWLB/dotfiles/internal/syncer"
	"github.com/spf13/cobra"
)

// dot sync push and its back-compat `sync` alias.

func newSyncSyncCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "sync",
		Short:        "Alias for `push` (kept for back-compat; prefer `dot sync push`)",
		RunE:         runSync,
		SilenceUsage: true,
	}
}

// runSync handles the explicit `sync` subcommand. The historical
// Pull+Push semantics were retired; this is now a thin alias for push that
// prints a one-line deprecation hint so callers gradually migrate to
// `push`. The bare `dot sync` (no subcommand) prints help instead.
func runSync(cmd *cobra.Command, args []string) error {
	printerFrom(cmd).Line("(note: `sync` is now an alias for `push`; use `dot sync pull` for baseline-tracked mirror payloads)")
	return runSyncPush(cmd, args)
}

func newSyncPushCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "push",
		Short: "Preview and send workspace changes to mirror under a propagation policy",
		Long: `Push the workspace tree to the gdrive mirror under a propagation
policy. The default policy '{create:true, update:true, delete:false}'
copies new and modified files but never deletes mirror-side content. By default
push prints the upload plan and asks before applying.

Flag --propagate= takes a comma-separated allowlist; absent items are
disabled. Examples:

  dot sync push                              # preview, then confirm
  dot sync push --mode=clean                 # apply only if no conflicts
  dot sync push --mode=force                 # overwrite with backups
  dot sync push --propagate=create,update,delete   # full sync
  dot sync push --propagate=create           # additive only
  dot sync push --propagate=update           # in-place updates only

The per-workspace store (.dotfiles/) and intake staging area
(inbox/gdrive/) are always excluded so they never round-trip to mirror.

Previews, including --dry-run, display operator-approved sensitive overrides.
Dry-run does not transfer files.`,
		RunE:         runSyncPush,
		SilenceUsage: true,
	}
	cmd.Flags().String("propagate", "", "comma-separated allowlist of propagation kinds (create,update,delete)")
	return cmd
}

func runSyncPush(cmd *cobra.Command, _ []string) error {
	bs, err := syncer.Bootstrap(syncBootstrapOptions(cmd, false))
	if err != nil {
		return err
	}
	cfg := bs.Config
	if err := syncer.RejectGenericPeerProfile(cfg); err != nil {
		return err
	}
	p := printerFrom(cmd)

	dryRun, _ := cmd.Flags().GetBool("dry-run")

	// Refuse before touching anything: a second writer on one target is the
	// failure this guard exists for, and it is cheap to detect up front.
	if err := syncer.CheckOwner(cfg); err != nil {
		recordPushRefusal(cfg, err, dryRun)
		return err
	}

	if cmd.Flags().Changed("propagate") {
		raw, _ := cmd.Flags().GetString("propagate")
		policy, err := parsePropagateFlag(raw)
		if err != nil {
			return fmt.Errorf("--propagate: %w", err)
		}
		cfg.Propagation = policy
	}

	if err := syncer.ResolvePushRsync(cmd.Context(), bs.Runner, cfg); err != nil && !cfg.Paused {
		recordPushRefusal(cfg, err, dryRun)
		return err
	}

	if block := syncPreflight(p, cfg, bs.Runner); block != nil {
		recordPushRefusal(cfg, errors.New(block.Reason()), dryRun)
		return nil
	}
	mode, err := gdriveSyncModeFrom(cmd)
	if err != nil {
		return err
	}
	overrides := syncer.SensitiveOverrides(cfg.AllowPatterns)

	// The scheduled push and a manual one hold the same named lease (#267).
	defer resourceguard.HoldBackgroundSync(cmd.Context(), "mirror push ("+cfg.Profile+")")()
	res, err := syncer.PushCommand(cmd.Context(), syncer.PushOptions{
		State:  bs.State,
		Config: cfg,
		Runner: bs.Runner,
		Mode:   mode,
		DryRun: dryRun,
		Progress: renderSyncEvent(p, syncRender{
			cfg:                cfg,
			mode:               mode,
			dryRun:             dryRun,
			sensitiveOverrides: overrides,
		}),
		Confirm: confirmSync(cmd),
	})
	if err != nil {
		return err
	}
	switch res.Outcome {
	case syncer.PushLockBusy:
		p.Line("  %s", res.LockErr)
	case syncer.PushAborted:
		p.Line("Aborted.")
	case syncer.PushComplete:
		p.Line("✓ Push complete.")
	case syncer.PushPlanned:
	}
	return nil
}

// recordPushRefusal stores a refusal that stopped a real push before the
// engine ran, so `dot sync status` shows it like a refused plan (#224).
func recordPushRefusal(cfg *syncer.Config, why error, dryRun bool) {
	if !dryRun {
		_ = syncer.RecordPushRefusal(cfg, fmt.Errorf("push refused: %w", why))
	}
}

func printPushPlan(p *Printer, plan *syncer.PushPlan) {
	if plan == nil {
		return
	}
	affected := affectedDirsFromLists(plan.Creates, plan.Updates, plan.Deletes, pushConflictPaths(plan.Conflicts))
	if len(affected) > 0 {
		p.Section("Affected folders")
		printPathList(p, affected)
	}
	if len(plan.Creates) > 0 {
		p.Section(fmt.Sprintf("Uploads: %d", len(plan.Creates)))
		printPathList(p, plan.Creates)
	}
	if len(plan.Updates) > 0 {
		p.Section(fmt.Sprintf("Updates: %d", len(plan.Updates)))
		printPathList(p, plan.Updates)
	}
	if len(plan.Deletes) > 0 {
		p.Section(fmt.Sprintf("Deletes: %d", len(plan.Deletes)))
		printPathList(p, plan.Deletes)
	}
	if len(plan.SkippedPolicy) > 0 {
		p.Section(fmt.Sprintf("Skipped by propagation policy: %d", len(plan.SkippedPolicy)))
		printPathList(p, plan.SkippedPolicy)
	}
	if len(plan.Unsupported) > 0 {
		p.Section(fmt.Sprintf("Unsupported names: %d", len(plan.Unsupported)))
		printPathList(p, plan.Unsupported)
		p.Line("  Dropbox/Windows cannot store these names; they are excluded from this push.")
		p.Line("  Run `dot sync names trim` to rename trailing-whitespace names; rename a name ending in a period by hand.")
	}
	if len(plan.Leftovers) > 0 {
		p.Section(fmt.Sprintf("Mirror leftovers: %d", len(plan.Leftovers)))
		printPathList(p, plan.Leftovers)
		p.Line("  Unsupported names the baseline proves the workspace put in the mirror and no longer has (left by a rename).")
		if plan.MoveLeftovers {
			p.Line("  The push moves them into the workspace's .sync-conflicts/<ts>/from-mirror/; more than max_delete fails the run instead.")
		} else {
			p.Line("  Delete propagation is off, so they are listed only; a push without it drops their baseline proof, so move them by hand.")
		}
	}
	if len(plan.MirrorUnsupported) > 0 {
		p.Section(fmt.Sprintf("Mirror-only unsupported names: %d", len(plan.MirrorUnsupported)))
		printPathList(p, plan.MirrorUnsupported)
		p.Line("  No baseline proof that the workspace put these here; they may be cloud files, so they are listed only.")
	}
	if plan.Placeholders > 0 {
		// Without this an operator reading a conflict list has no way to see
		// that most of the mirror is evicted rather than genuinely different.
		p.Line("  %d mirror file(s) in the sync set are cloud placeholders — their content lives only in the provider's cloud.", plan.Placeholders)
	}
	if len(plan.Conflicts) > 0 {
		p.Section(fmt.Sprintf("Drive conflicts: %d", len(plan.Conflicts)))
		for _, c := range plan.Conflicts {
			reason := c.Reason
			if reason == "" {
				reason = "local and mirror differ"
			}
			p.Line("  !  %s — %s", c.RelPath, reason)
		}
	}
	if len(affected) == 0 && len(plan.SkippedPolicy) == 0 && len(plan.Unsupported) == 0 && len(plan.Leftovers) == 0 && len(plan.MirrorUnsupported) == 0 {
		p.Line("  No push changes.")
	}
}

// parsePropagateFlag parses the --propagate= comma-separated allowlist.
// Empty (after split + trim) is rejected — there's no meaningful rsync
// invocation that does nothing.
func parsePropagateFlag(value string) (syncer.PropagationPolicy, error) {
	var p syncer.PropagationPolicy
	seen := map[string]bool{}
	nonEmpty := 0
	for _, raw := range strings.Split(value, ",") {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		nonEmpty++
		if seen[v] {
			return p, fmt.Errorf("duplicate token %q", v)
		}
		seen[v] = true
		switch v {
		case "create":
			p.Create = true
		case "update":
			p.Update = true
		case "delete":
			p.Delete = true
		default:
			return p, fmt.Errorf("unknown token %q (want create|update|delete)", v)
		}
	}
	if nonEmpty == 0 {
		return p, fmt.Errorf("must list at least one of create,update,delete")
	}
	return p, nil
}

// reportPushPartial downgrades an rsync partial transfer (exit 23/24) from a
// failed run to a warning, matching what the peer path already does.
//
// Why the mirror needs this: the target is a cloud-provider folder, and a file
// Dropbox keeps online-only is dataless on disk. Touching one asks the provider
// to hydrate it, and macOS answers EDEADLK rather than block, so rsync reports
// those paths as skipped and exits 23. That is routine for this target, not a
// broken run - the destination copy still exists in the cloud, and hydrating
// the whole mirror to satisfy a stat would be far worse than skipping it.
//
// Treating 23 as fatal made the scheduled push exit 1 forever on a workspace
// with any archived, never-opened files.
func reportPushPartial(p *Printer, err error) error {
	if err == nil || !syncer.IsPartialTransfer(err) {
		return err
	}
	p.Warn("partial transfer: %v", err)
	p.Line("  Some files were skipped; the rest arrived. Files the cloud client")
	p.Line("  keeps online-only cannot be stat'd locally and are expected here.")
	return nil
}
