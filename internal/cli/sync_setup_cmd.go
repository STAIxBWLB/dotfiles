package cli

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/syncer"
	"github.com/spf13/cobra"
)

// dot sync setup, pause and resume: the opt-in scheduler and its gate.

var preferredMachineName = syncer.PreferredMachineName

func newSyncSetupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Install rsync (if missing) and manage opt-in gsync schedulers",
		Long: `One-time setup. Verifies rsync is available (offers to install via brew/apt
if not), then configures the platform's user-scheduler (launchd LaunchAgent on
macOS, systemd user-timer on Linux). Automatic sync is off by default; pass an
interval flag to opt in.

  --push-interval=DUR    Deploy automatic ` + "`dot sync push --mode=MODE`" + `.
  --pull-interval=DUR    Deploy automatic ` + "`dot sync pull --mode=MODE`" + `.
  --push-mode=MODE       Automatic push mode: clean or force (default clean).
  --pull-mode=MODE       Automatic intake mode: clean or force (default clean).
  --owner=NAME           Record the machine allowed to run this scheduler; use self for this machine.

Idempotent — re-run safely after an interval change to reload the unit.`,
		RunE:         runSyncSetup,
		SilenceUsage: true,
	}
	cmd.Flags().String("push-interval", "", "deploy push scheduler at this cadence (e.g. 15m, 1h, 0 to remove)")
	cmd.Flags().String("pull-interval", "", "deploy pull scheduler at this cadence (e.g. 15m, 1h, 0 to remove)")
	cmd.Flags().String("push-mode", syncer.ModeClean.String(), "automatic push mode: clean or force")
	cmd.Flags().String("pull-mode", syncer.ModeClean.String(), "automatic intake mode: clean or force")
	cmd.Flags().String("owner", "", "machine allowed to run this scheduler (self or a machine name)")
	return cmd
}

func runSyncSetup(cmd *cobra.Command, _ []string) error {
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	bs, err := syncer.Bootstrap(syncBootstrapOptions(cmd, dryRun))
	if err != nil {
		return err
	}
	cfg, runner := bs.Config, bs.Runner
	if err := syncer.RejectGenericPeerProfile(cfg); err != nil {
		return err
	}
	ctx := cmd.Context()
	p := printerFrom(cmd)

	if dryRun {
		p.Line("(dry-run — no changes)")
	}

	pushInterval, pullInterval := 0, 0
	if cmd.Flags().Changed("push-interval") {
		raw, _ := cmd.Flags().GetString("push-interval")
		seconds, err := parseIntervalFlag(raw)
		if err != nil {
			return fmt.Errorf("--push-interval: %w", err)
		}
		pushInterval = seconds
	}
	if cmd.Flags().Changed("pull-interval") {
		raw, _ := cmd.Flags().GetString("pull-interval")
		seconds, err := parseIntervalFlag(raw)
		if err != nil {
			return fmt.Errorf("--pull-interval: %w", err)
		}
		pullInterval = seconds
	}
	pushModeRaw, _ := cmd.Flags().GetString("push-mode")
	pushMode, err := parseAutomaticModeFlag(pushModeRaw)
	if err != nil {
		return fmt.Errorf("--push-mode: %w", err)
	}
	pullModeRaw, _ := cmd.Flags().GetString("pull-mode")
	pullMode, err := parseAutomaticModeFlag(pullModeRaw)
	if err != nil {
		return fmt.Errorf("--pull-mode: %w", err)
	}
	owner, _ := cmd.Flags().GetString("owner")
	if err := setupSchedulerOwner(cfg, owner, cmd.Flags().Changed("owner"), dryRun); err != nil {
		return err
	}
	if err := syncer.SetLocalSchedule(cfg, pushInterval, pullInterval, pushMode, pullMode, dryRun); err != nil {
		return fmt.Errorf("saving scheduler config: %w", err)
	}

	// 1. Check / install rsync
	p.Line("Checking rsync...")
	rsync, err := syncer.EnsureRsync(ctx, runner, p.Out, dryRun, confirmSync(cmd))
	if err != nil {
		return err
	}
	switch rsync.Outcome {
	case syncer.RsyncPresent, syncer.RsyncInstalled:
		p.Line("  ✓ rsync installed (%s)", rsync.Version)
	case syncer.RsyncWouldInstall:
		p.Line("  ~ rsync not found; would install after confirmation")
	case syncer.RsyncInstallDeclined:
		p.Line("Aborted.")
		return nil
	}

	// 2. Deploy scheduler(s) only when explicitly enabled.
	p.Line("Configuring opt-in scheduler...")
	sched, paths, err := syncer.ResolveScheduler(cfg, runner)
	if err != nil {
		return err
	}
	if dryRun {
		if cfg.Interval > 0 {
			p.Line("  ~ would install push unit (interval: %s, mode: %s)", formatInterval(cfg.Interval), cfg.PushMode)
			p.Line("  unit: %s", scheduleUnitLabel(paths))
		} else {
			p.Line("  ~ would ensure push scheduler is off")
		}
		if cfg.PullInterval > 0 {
			p.Line("  ~ would install pull unit (interval: %s, mode: %s)", formatInterval(cfg.PullInterval), cfg.PullMode)
		} else {
			p.Line("  ~ would ensure pull scheduler is off")
		}
		if cfg.Home != "" {
			p.Line("  ~ would stage or retire target-home scheduler files only; no caller-domain service manager would run")
			p.Line("  ~ then log in as the target user or rerun this command without --home via sudo -iu <target-user>")
		}
		p.Line("  log:  %s", cfg.LogFile)
		p.Blank()
		p.Line("✓ gsync setup dry-run complete.")
		return nil
	}
	if err := sched.Install(ctx); err != nil {
		return fmt.Errorf("installing scheduler: %w", err)
	}
	if cfg.Interval > 0 {
		p.Line("  ✓ push unit installed (interval: %s, mode: %s)", formatInterval(cfg.Interval), cfg.PushMode)
		p.Line("  unit: %s", scheduleUnitLabel(paths))
	} else {
		p.Line("  (push scheduler off — pass --push-interval=DUR to enable)")
	}
	if cfg.PullInterval > 0 {
		p.Line("  ✓ pull unit installed (interval: %s, mode: %s)", formatInterval(cfg.PullInterval), cfg.PullMode)
	} else {
		p.Line("  (pull scheduler off — pass --pull-interval=DUR to enable)")
	}
	p.Line("  log:  %s", cfg.LogFile)

	p.Blank()
	p.Line("✓ gsync setup complete.")
	if cfg.Paused {
		p.Line("  Paused gate is set — run `dot sync resume` to start syncing.")
	} else {
		p.Line("  Run `dot sync push` or `dot sync pull` when you want to sync manually.")
	}
	return nil
}

// setupSchedulerOwner enforces the one-writer precondition before setup writes
// scheduler settings or installs a unit. Existing workspaces remain opt-in for
// push and pull; only this explicit unattended-writer setup path requires it.
func setupSchedulerOwner(cfg *syncer.Config, rawOwner string, changed, dryRun bool) error {
	if changed {
		owner := strings.TrimSpace(rawOwner)
		if owner == "self" {
			owner = preferredMachineName()
			if owner == "" {
				return fmt.Errorf("cannot determine this machine's name for --owner self")
			}
		}
		if owner == "" {
			return fmt.Errorf("--owner requires self or a machine name")
		}
		if err := syncer.SetLocalOwner(cfg, owner, dryRun); err != nil {
			return fmt.Errorf("saving scheduler owner: %w", err)
		}
		return nil
	}
	if strings.TrimSpace(cfg.Owner) != "" {
		return nil
	}
	owner := preferredMachineName()
	if owner == "" {
		return fmt.Errorf("cannot determine this machine's name; set one before installing a sync scheduler")
	}
	return fmt.Errorf("cannot install a sync scheduler without an owner: two writers on one target can corrupt it\n\nRemedy: rerun `dot sync setup --owner %s`", owner)
}

// parseIntervalFlag accepts a Go duration string ("15m", "1h"),
// a bare integer (seconds), or "0" to disable. Returns seconds.
func parseIntervalFlag(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "0" {
		return 0, nil
	}
	var seconds int
	// Try Go duration first (handles "15m", "1h", "30s").
	if d, err := time.ParseDuration(raw); err == nil {
		seconds = int(d.Seconds())
	} else {
		// Bare integer fallback.
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil {
			return 0, fmt.Errorf("not a duration or seconds: %q", raw)
		}
		seconds = parsed
	}
	if err := syncer.ValidateScheduleInterval(seconds); err != nil {
		return 0, err
	}
	return seconds, nil
}

func parseAutomaticModeFlag(raw string) (syncer.RunMode, error) {
	mode, err := syncer.ParseRunMode(raw)
	if err != nil {
		return "", err
	}
	return syncer.NormalizeAutomaticMode(mode)
}

func newSyncResumeCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "resume",
		Short:        "Clear the Paused gate so pull/push/sync can run",
		RunE:         runSyncResume,
		SilenceUsage: true,
	}
}

func runSyncResume(cmd *cobra.Command, _ []string) error {
	bs, err := syncer.Bootstrap(syncBootstrapOptions(cmd, false))
	if err != nil {
		return err
	}
	if err := syncer.RejectGenericPeerProfile(bs.Config); err != nil {
		return err
	}
	p := printerFrom(cmd)

	res, err := syncer.SyncResume(cmd.Context(), bs.Config, bs.Runner)
	if err != nil {
		return err
	}
	if res.WasPaused {
		p.Line("✓ gsync resumed.")
	} else {
		p.Line("gsync was not paused.")
	}
	if res.SchedulerOff {
		p.Line("scheduler remains off — run `dot sync setup --push-interval=DUR` or `--pull-interval=DUR` to enable.")
		return nil
	}
	if res.SchedulerErr != nil {
		var targetUserErr *syncer.SchedulerTargetUserActionRequiredError
		if errors.As(res.SchedulerErr, &targetUserErr) {
			p.Warn("scheduler resume requires target-user action: %v", res.SchedulerErr)
		} else {
			p.Warn("scheduler resume failed: %v", res.SchedulerErr)
		}
	} else if res.SchedulerResumed {
		p.Line("✓ scheduler resumed.")
	}
	return nil
}

func newSyncPauseCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "pause",
		Short:        "Set the Paused gate so pull/push/sync refuse to run",
		RunE:         runSyncPause,
		SilenceUsage: true,
	}
}

func runSyncPause(cmd *cobra.Command, _ []string) error {
	bs, err := syncer.Bootstrap(syncBootstrapOptions(cmd, false))
	if err != nil {
		return err
	}
	if err := syncer.RejectGenericPeerProfile(bs.Config); err != nil {
		return err
	}
	p := printerFrom(cmd)

	res, err := syncer.SyncPause(cmd.Context(), bs.Config, bs.Runner)
	if err != nil {
		return err
	}
	if !res.WasPaused {
		p.Line("✓ gsync paused.")
	} else {
		p.Line("gsync was already paused.")
	}
	if res.SchedulerErr != nil {
		var targetUserErr *syncer.SchedulerTargetUserActionRequiredError
		if errors.As(res.SchedulerErr, &targetUserErr) {
			p.Warn("scheduler pause requires target-user action: %v", res.SchedulerErr)
		} else {
			p.Warn("scheduler pause failed: %v", res.SchedulerErr)
		}
	} else if res.SchedulerStopped {
		p.Line("✓ scheduler stopped.")
	}
	return nil
}
