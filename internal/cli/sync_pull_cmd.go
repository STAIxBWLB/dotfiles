package cli

import (
	"fmt"
	"path/filepath"

	"github.com/STAIxBWLB/dotfiles/internal/syncer"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
	"github.com/spf13/cobra"
)

// dot sync pull, intake and fetch: the three commands that bring mirror
// content back into the workspace.

func newSyncPullCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pull",
		Short: "Restore/update baseline-tracked mirror payloads into the workspace",
		Long: `Pull applies mirror-side changes only for paths listed in
.dotfiles/sync/baseline.manifest. Baseline is expected to be tracked in
Git, so a second machine can git pull the index and then restore binary
payloads from the cloud mirror.

Files absent from baseline are not copied into the workspace by pull; run
intake to stage new mirror-origin files under inbox/gdrive/<ts>/ for manual
review. If local and mirror both changed a baseline-tracked file, manual mode
asks before applying, clean mode aborts, and force mode overwrites local after
backing up the local version into .sync-conflicts/<ts>/from-workspace/.`,
		RunE:         runSyncPull,
		SilenceUsage: true,
	}
	cmd.Flags().Bool("strict", false, "force sha256 fingerprints for every baseline entry (slower; catches content changes that preserve size+mtime)")
	return cmd
}

func runSyncPull(cmd *cobra.Command, _ []string) error {
	bs, err := syncer.Bootstrap(syncBootstrapOptions(cmd, false))
	if err != nil {
		return err
	}
	cfg := bs.Config
	if err := syncer.RejectGenericPeerProfile(cfg); err != nil {
		return err
	}
	p := printerFrom(cmd)
	if syncPreflight(p, cfg, bs.Runner) != nil {
		return nil
	}
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	strict, _ := cmd.Flags().GetBool("strict")
	mode, err := gdriveSyncModeFrom(cmd)
	if err != nil {
		return err
	}

	res, err := syncer.PullCommand(cmd.Context(), syncer.PullCommandOptions{
		State:    bs.State,
		Config:   cfg,
		Runner:   bs.Runner,
		Mode:     mode,
		Strict:   strict,
		DryRun:   dryRun,
		Progress: renderSyncEvent(p, syncRender{cfg: cfg, mode: mode}),
		Confirm:  confirmSync(cmd),
	})
	if err != nil {
		return err
	}
	switch res.Outcome {
	case syncer.PullLockBusy:
		p.Line("  %s", res.LockErr)
	case syncer.PullAborted:
		p.Line("Aborted.")
	case syncer.PullCompleteDirect:
		p.Line("✓ Pull complete.")
	case syncer.PullCompleteTracked:
		printPullResult(p, cfg, res.Result)
	case syncer.PullPlanned:
	}
	return nil
}

func newSyncIntakeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "intake",
		Short: "Stage new mirror-origin files for manual routing",
		Long: `Compares the mirror against baseline.manifest and imports.manifest to
find new mirror-origin files. New candidates are copied into a timestamped
subdirectory of <local>/inbox/gdrive/<intake-ts>/ for the operator to review
and route.

Changed baseline-tracked files are skipped and left for ` + "`dot sync pull`" + `.
Mirror-side deletions against baseline are detected by pull, not intake.

  --strict   Use sha256 fingerprints (catches content changes that
             preserve mtime). Default is fast size+mtime mode.`,
		RunE:         runSyncIntake,
		SilenceUsage: true,
	}
	cmd.Flags().Bool("strict", false, "use sha256 fingerprints instead of size+mtime")
	return cmd
}

func runSyncIntake(cmd *cobra.Command, _ []string) error {
	bs, err := syncer.Bootstrap(syncBootstrapOptions(cmd, false))
	if err != nil {
		return err
	}
	cfg := bs.Config
	p := printerFrom(cmd)
	if syncPreflight(p, cfg, bs.Runner) != nil {
		return nil
	}
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	strict, _ := cmd.Flags().GetBool("strict")
	if _, err := gdriveSyncModeFrom(cmd); err != nil {
		return err
	}

	out, err := syncer.IntakeCommand(cmd.Context(), syncer.IntakeCommandOptions{
		Config:   cfg,
		Runner:   bs.Runner,
		Strict:   strict,
		DryRun:   dryRun,
		Progress: renderSyncEvent(p, syncRender{cfg: cfg, strict: strict}),
	})
	if err != nil {
		return err
	}
	if out.Outcome == syncer.IntakeLockBusy {
		p.Line("  %s", out.LockErr)
		return nil
	}
	res := out.Result

	printPullResult(p, cfg, res.Pull)
	if res.StagingDir != "" {
		p.Line("  ✓ %d intaked into %s", len(res.Intaked), res.StagingDir)
	} else {
		p.Line("  %d intaked", len(res.Intaked))
	}
	p.Line("  %d skipped (baseline match)", len(res.SkippedBase))
	if len(res.SkippedTracked) > 0 {
		p.Line("  %d skipped (tracked conflict/unresolved)", len(res.SkippedTracked))
	}
	p.Line("  %d skipped (imports match)", len(res.SkippedImports))
	return nil
}

func printPullResult(p *Printer, cfg *syncer.Config, res *syncer.PullResult) {
	if res == nil {
		return
	}
	p.Line("  %d pulled (%d restored)", len(res.Pulled), len(res.Restored))
	if len(res.LocalModified) > 0 {
		p.Line("  %d local-modified tracked files left for push", len(res.LocalModified))
	}
	if len(res.Conflicts) > 0 {
		p.Line("  %d tracked conflicts — Drive copies saved under %s", len(res.Conflicts),
			filepath.Join(stripTrailingSlash(cfg.LocalPath), ".sync-conflicts"))
	}
	if len(res.Tombstones) > 0 {
		p.Line("  %d tombstones recorded — see %s", len(res.Tombstones), cfg.LocalPaths.TombstonesFile)
	}
}

func printPullPlan(p *Printer, cfg *syncer.Config, res *syncer.PullResult) {
	if res == nil {
		return
	}
	updates := differenceStrings(res.Pulled, res.Restored)
	affected := affectedDirsFromLists(res.Pulled, res.Restored, res.LocalModified, pullConflictPaths(res.Conflicts), tombstonePaths(res.Tombstones))
	if len(affected) > 0 {
		p.Section("Affected folders")
		printPathList(p, affected)
	}
	if len(updates) > 0 {
		p.Section(fmt.Sprintf("Updates from Drive: %d", len(updates)))
		printPathList(p, updates)
	}
	if len(res.Restored) > 0 {
		p.Section(fmt.Sprintf("Restores from Drive: %d", len(res.Restored)))
		printPathList(p, res.Restored)
	}
	if len(res.LocalModified) > 0 {
		p.Section(fmt.Sprintf("Local-only changes: %d", len(res.LocalModified)))
		printPathList(p, res.LocalModified)
	}
	if len(res.Conflicts) > 0 {
		p.Section(fmt.Sprintf("Conflicts: %d", len(res.Conflicts)))
		for _, c := range res.Conflicts {
			reason := c.Reason
			if reason == "" {
				reason = "local and Drive both changed"
			}
			p.Line("  !  %s — %s", c.RelPath, reason)
			if c.BackupPath != "" {
				p.Line("     backup: %s", c.BackupPath)
			}
		}
	}
	if len(res.Tombstones) > 0 {
		p.Section(fmt.Sprintf("Mirror deletions: %d", len(res.Tombstones)))
		printPathList(p, tombstonePaths(res.Tombstones))
		p.Line("  tombstones: %s", cfg.LocalPaths.TombstonesFile)
	}
	if len(affected) == 0 && len(res.LocalModified) == 0 {
		p.Line("  No pull changes.")
	}
}

func newSyncFetchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "fetch <path>...",
		Short: "Restore specific files or folders from the target on demand",
		Long: `Fetch pulls the named files or directories (workspace-relative paths)
from the sync target into the workspace — the on-demand entry point when a
specific file, folder, program, or event needs the binaries backing a path
without running a full pull. Other tools can shell out to it:

  dot sync fetch projects/oda/koica-tiu/06-proposal
  dot sync fetch admin/scan.pdf research/data --dry-run

Safety: newer local files are never overwritten (--update), overwrites are
backed up under .sync-conflicts/, nothing is deleted, and the exclude layers
still apply — .git and non-allowed secrets can never be imported. Paths
missing on the target, and paths a sync filter keeps out (submodules, .git,
secrets, excludes, shared folders), are reported and skipped. An absolute path is accepted
when it lies under the workspace.`,
		Args:         cobra.MinimumNArgs(1),
		RunE:         runSyncFetch,
		SilenceUsage: true,
	}
}

func runSyncFetch(cmd *cobra.Command, args []string) error {
	bs, err := syncer.Bootstrap(syncBootstrapOptions(cmd, false))
	if err != nil {
		return err
	}
	cfg := bs.Config
	p := printerFrom(cmd)
	if syncPreflight(p, cfg, bs.Runner) != nil {
		return nil
	}
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	res, err := syncer.FetchCommand(cmd.Context(), syncer.FetchOptions{
		State:    bs.State,
		Config:   cfg,
		Runner:   bs.Runner,
		Paths:    args,
		DryRun:   dryRun,
		Progress: renderSyncEvent(p, syncRender{cfg: cfg}),
	})
	if err != nil {
		return err
	}
	if res.Outcome == syncer.FetchLockBusy {
		p.Line("  %s", res.LockErr)
		return nil
	}
	if res.Result == nil || len(res.Result.Fetched) == 0 {
		p.Line("Nothing to fetch.")
		return nil
	}
	p.Line("%s", ui.StyleSuccess.Render(fmt.Sprintf("✓ fetched %d path(s)", len(res.Result.Fetched))))
	return nil
}
