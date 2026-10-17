package cli

import (
	"fmt"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/syncer"
	"github.com/STAIxBWLB/dotfiles/internal/ws"
	"github.com/spf13/cobra"
)

// dot sync conflicts: list and prune the .sync-conflicts/ backup trees.

func newSyncConflictsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "conflicts",
		Short: "List or prune .sync-conflicts/ backup directories",
		Long: `Conflict backups accumulate in both trees: pull backups under the
workspace, push backups under the mirror. For SSH targets, push backups are
listed and pruned on the remote target under <target>/.sync-conflicts; the
peer profile also includes ~/.dot-peer-conflicts from its host-path pass.

  dot sync conflicts                       # alias for list
  dot sync conflicts list
  dot sync conflicts prune                 # remove backups older than 30 days
  dot sync conflicts prune --older-than 7
  dot sync conflicts prune --all           # remove every backup
  dot sync conflicts prune --all --remote-only --profile=peer
                                             # preserve this machine's backups`,
		RunE: runSyncConflictsList,
	}
	prune := &cobra.Command{
		Use:          "prune",
		Short:        "Remove old conflict backups from selected local/remote trees",
		RunE:         runSyncConflictsPrune,
		SilenceUsage: true,
	}
	prune.Flags().Int("older-than", 30, "prune backups older than this many days")
	prune.Flags().Bool("all", false, "prune every backup regardless of age")
	prune.Flags().Bool("remote-only", false, "prune only SSH target backups; preserve local workspace backups")
	cmd.AddCommand(
		&cobra.Command{
			Use:   "list",
			Short: "List .sync-conflicts/ backup directories in both trees",
			RunE:  runSyncConflictsList,
		},
		prune,
	)
	return cmd
}

func runSyncConflictsList(cmd *cobra.Command, _ []string) error {
	bs, err := syncer.Bootstrap(syncBootstrapOptions(cmd, true))
	if err != nil {
		return err
	}
	p := printerFrom(cmd)
	now := time.Now()
	if err := syncer.ConflictsList(cmd.Context(), bs.Runner, bs.Config, func(l syncer.ConflictListing) {
		printConflictListing(p, now, l)
	}); err != nil {
		return err
	}
	p.Line("Prune candidates (▲) with: dot sync conflicts prune")
	return nil
}

// printConflictListing renders one tree's backup directories. The engine walks
// the trees and hands each one over as it is read, so a later tree's failure
// still leaves the earlier trees printed, exactly as before the move.
func printConflictListing(p *Printer, now time.Time, l syncer.ConflictListing) {
	suffix := "/.sync-conflicts/"
	if l.IsRemote {
		suffix = "/"
	}
	if len(l.Entries) == 0 && len(l.Remotes) == 0 {
		p.Line("No conflict backups under %s%s (%s)", l.Root, suffix, l.Label)
		return
	}
	p.Header(fmt.Sprintf("Conflict backups under %s%s (%s)", l.Root, suffix, l.Label))
	for _, c := range l.Entries {
		p.Bullet(conflictAgeMarker(now, c.ModTime), fmt.Sprintf("%s (%s ago) — %s", c.Timestamp, conflictAge(now, c.ModTime), c.Path))
	}
	for _, c := range l.Remotes {
		p.Bullet(conflictAgeMarker(now, c.ModTime), fmt.Sprintf("%s (%s ago, %s) — %s", c.Timestamp, conflictAge(now, c.ModTime), ws.FormatSize(c.Size), c.Path))
	}
	p.Blank()
}

func conflictAge(now, modTime time.Time) time.Duration {
	return now.Sub(modTime).Truncate(time.Hour)
}

// conflictAgeMarker flags backups older than 30 days as prune candidates.
func conflictAgeMarker(now, modTime time.Time) string {
	if conflictAge(now, modTime) > 30*24*time.Hour {
		return "▲"
	}
	return "•"
}

func runSyncConflictsPrune(cmd *cobra.Command, _ []string) error {
	bs, err := syncer.Bootstrap(syncBootstrapOptions(cmd, false))
	if err != nil {
		return err
	}
	cfg := bs.Config
	p := printerFrom(cmd)
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	olderDays, _ := cmd.Flags().GetInt("older-than")
	all, _ := cmd.Flags().GetBool("all")
	remoteOnly, _ := cmd.Flags().GetBool("remote-only")
	if remoteOnly && !cfg.Target.IsSSH() {
		return fmt.Errorf("--remote-only requires an SSH target")
	}

	cutoff, err := syncer.ResolvePruneCutoff(olderDays, all, cmd.Flags().Changed("older-than"))
	if err != nil {
		return err
	}

	res, err := syncer.ConflictsPrune(cmd.Context(), syncer.ConflictsPruneOptions{
		Config:     cfg,
		Runner:     bs.Runner,
		Cutoff:     cutoff,
		RemoteOnly: remoteOnly,
		DryRun:     dryRun,
		Progress:   renderSyncEvent(p, syncRender{cfg: cfg}),
		OnPlanned:  func(r syncer.PruneTreeReport) { printPrunePlan(p, r) },
		OnPruned:   func(r syncer.PruneTreeReport) { printPruneApplied(p, r) },
		Confirm:    confirmSync(cmd),
	})
	if err != nil {
		return err
	}
	switch res.Outcome {
	case syncer.PruneLockBusy:
		p.Line("  %s", res.LockErr)
	case syncer.PruneNothingToDo:
		p.Line("Nothing to prune.")
	case syncer.PrunePlanned:
		p.Line("  (dry-run — no changes)")
	case syncer.PruneAborted:
		p.Line("Aborted.")
	case syncer.PruneDone:
	}
	return nil
}

// printPrunePlan lists one tree's prune candidates.
func printPrunePlan(p *Printer, r syncer.PruneTreeReport) {
	p.Section(fmt.Sprintf("%s — %s", r.Label, r.Result.Root))
	for _, c := range r.Result.Pruned {
		p.Bullet("▲", fmt.Sprintf("%s (%s ago, %s)", c.Timestamp, conflictAge(r.Now, c.ModTime), ws.FormatSize(c.Size)))
	}
}

// printPruneApplied reports one tree's removal, plus the follow-on consequence
// that only the mirror and remote-target trees have.
func printPruneApplied(p *Printer, r syncer.PruneTreeReport) {
	suffix := "/.sync-conflicts/"
	if r.IsRemote {
		suffix = "/"
	}
	p.Success("pruned %d backup dir(s) (freed %s) under %s%s", len(r.Result.Pruned), ws.FormatSize(r.Result.Reclaimed), r.Root, suffix)
	switch r.Label {
	case "mirror":
		p.Line("  The Drive sync client will propagate these deletions and reclaim cloud quota.")
	case "remote target":
		p.Line("  The peer target now has the selected conflict backups removed.")
	}
}
