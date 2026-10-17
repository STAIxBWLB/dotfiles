package cli

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/tunnel"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
	"github.com/STAIxBWLB/dotfiles/internal/watchdog"
)

// dot watchdog warp — one WARP heal pass — plus the setup/uninstall/status
// steps for the warp daemon and headless power hardening.

func newWatchdogWarpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "warp",
		Short: "Run one WARP self-heal pass (the root daemon invokes this)",
		Long: `Probe WARP health (warp-cli Connected AND an interface address in
100.96.0.0/12), fold it into the consecutive-failure state, and act: after
fail_threshold consecutive failures, warp-cli disconnect/connect, up to 3
attempts; then launchctl kickstart -k the WARP daemon, rate-limited to one
restart per 30 minutes. Every action is logged and notified.`,
		Args:         cobra.NoArgs,
		RunE:         func(cmd *cobra.Command, args []string) error { return runWatchdogWarpForGOOS(cmd, args, runtime.GOOS) },
		SilenceUsage: true,
	}
}

func runWatchdogWarpForGOOS(cmd *cobra.Command, _ []string, goos string) error {
	if goos != "darwin" {
		return fmt.Errorf("dot watchdog warp is macOS-only")
	}
	ctx := context.Background()
	scheduled := os.Getenv(watchdog.ScheduledRunEnv) == "1"
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	runner := watchdogRunner(false)
	mgr := watchdog.NewManager(runner, homeFor(cmd))
	wcfg, err := loadWatchdogSnapshot(mgr)
	if err != nil {
		return err
	}
	if !wcfg.Warp.Enabled {
		return fmt.Errorf("WARP heal is disabled in the watchdog snapshot; set watchdog.warp.enabled: true and rerun `dot watchdog setup`")
	}
	settings := watchdog.ResolveWarp(wcfg.Warp)

	statusOut, err := runner.RunQuery(ctx, "warp-cli", "status")
	if err != nil {
		return fmt.Errorf("probing warp-cli status: %w", err)
	}
	ifconfigOut, err := runner.RunQuery(ctx, "ifconfig")
	if err != nil {
		return fmt.Errorf("probing interfaces: %w", err)
	}
	healthy := watchdog.WarpHealthy(statusOut.Stdout, ifconfigOut.Stdout)
	if !dryRun {
		release, busy, err := watchdog.AcquireWarpLock(mgr.StateDir())
		if err != nil {
			return err
		}
		if busy {
			// Another pass holds the lock; the next interval retries.
			_ = watchdog.AppendEvent(mgr.LogPath(), watchdog.Event{Level: "info", Event: "skip", Msg: "another warp pass holds the lock"})
			return nil
		}
		defer release()
	}
	state, err := watchdog.LoadWarpState(mgr.WarpStatePath())
	if err != nil {
		return err
	}
	now := time.Now()
	action, next := watchdog.DecideWarpAction(healthy, state, settings.FailThreshold, now)

	p := printerFrom(cmd)
	if dryRun {
		p.Line("[dry-run] healthy=%v consecutive=%d attempts=%d → would %s",
			healthy, next.ConsecutiveFailures, next.Attempts, action)
		return nil
	}
	if err := executeWarpAction(ctx, mgr, runner, wcfg, state, action, next, now); err != nil {
		return err
	}
	if err := watchdog.SaveWarpState(mgr.WarpStatePath(), next); err != nil {
		return err
	}
	if !scheduled {
		p.Line("warp healthy=%v consecutive=%d action=%s", healthy, next.ConsecutiveFailures, action)
	}
	return nil
}

// executeWarpAction applies the decided action, logging and notifying on
// every one. The state save happens in the caller only after this succeeds,
// so a failed action does not consume an attempt — but a failed NOTIFICATION
// must not block it either: an ntfy outage after a kickstart would otherwise
// leave LastRestart unsaved and defeat the restart rate limit. Kickstart
// runs without sudo: the invoking daemon is root; a manual unprivileged run
// gets a clear error instead of a hidden password prompt.
func executeWarpAction(ctx context.Context, mgr *watchdog.Manager, runner *exec.Runner, wcfg config.WatchdogConfig, state watchdog.WarpState, action watchdog.WarpAction, next watchdog.WarpState, now time.Time) error {
	notifier := watchdog.NewNotifier(watchdog.ResolveNotify(wcfg.Notify, mgr.Home), runner, runtime.GOOS)
	event := watchdog.Event{Time: now, Event: "warp", Action: action.String()}
	// notifyBestEffort reports the alert, then records — not returns — a
	// delivery failure, so the pass still persists its state.
	notifyBestEffort := func(level, msg string) {
		if err := notifier.Notify(ctx, level, msg); err != nil {
			_ = watchdog.AppendEvent(mgr.LogPath(), watchdog.Event{Time: now, Level: "warn", Event: "error", Msg: "notification failed: " + err.Error()})
		}
	}
	switch action {
	case watchdog.WarpActionNone:
		return nil
	case watchdog.WarpActionRateLimited:
		event.Level = "warn"
		event.Msg = fmt.Sprintf("WARP still unhealthy after %d failures; daemon restart rate-limited (one per %s)", next.ConsecutiveFailures, watchdog.WarpRestartMinInterval)
		return watchdog.AppendEvent(mgr.LogPath(), event)
	case watchdog.WarpActionReconnect:
		if _, err := runner.Run(ctx, "warp-cli", "disconnect"); err != nil {
			return fmt.Errorf("warp-cli disconnect: %w", err)
		}
		if _, err := runner.Run(ctx, "warp-cli", "connect"); err != nil {
			return fmt.Errorf("warp-cli connect: %w", err)
		}
		event.Level = "warn"
		event.Msg = fmt.Sprintf("WARP reconnect attempt %d after %d consecutive failures", next.Attempts, next.ConsecutiveFailures)
		if err := watchdog.AppendEvent(mgr.LogPath(), event); err != nil {
			return err
		}
		notifyBestEffort("warn", event.Msg)
		return nil
	case watchdog.WarpActionKickstart:
		label := state.DaemonLabel
		if label == "" {
			return fmt.Errorf("WARP daemon label unknown; rerun `dot watchdog setup` to resolve and persist it")
		}
		// warp.json is user-writable while this pass may run as root: never
		// kickstart a label that cannot be Cloudflare's WARP daemon.
		if !watchdog.ValidWarpDaemonLabel(label) {
			return fmt.Errorf("persisted WARP daemon label %q failed validation; rerun `dot watchdog setup`", label)
		}
		if _, err := runner.Run(ctx, "launchctl", "kickstart", "-k", "system/"+label); err != nil {
			return fmt.Errorf("kickstarting WARP daemon (needs root; try `sudo dot watchdog warp`): %w", err)
		}
		event.Level = "critical"
		event.Msg = fmt.Sprintf("WARP daemon %s kickstarted after %d failed reconnects; further restarts rate-limited to one per %s", label, watchdog.MaxReconnectAttempts, watchdog.WarpRestartMinInterval)
		if err := watchdog.AppendEvent(mgr.LogPath(), event); err != nil {
			return err
		}
		notifyBestEffort("critical", event.Msg)
		return nil
	}
	return nil
}

// setupWarpDaemonStep installs the root heal daemon when warp is enabled.
// dotPath is the resolved `dot` binary the daemon invokes.
func setupWarpDaemonStep(p *Printer, mgr *watchdog.Manager, wcfg config.WatchdogConfig, dotPath string, yes bool) error {
	settings := watchdog.ResolveWarp(wcfg.Warp)
	ok, err := ui.Confirm("Install the WARP heal LaunchDaemon "+watchdog.WarpLabel+" (root, system domain)?", yes)
	if err != nil {
		return err
	}
	if !ok {
		p.Line("Skipped WARP heal daemon.")
		return nil
	}
	ctx := context.Background()
	if err := mgr.Runner.RunInteractive(ctx, "sudo", "-v"); err != nil {
		return fmt.Errorf("sudo is required for the system-domain daemon: %w", err)
	}
	daemonLabel, err := mgr.InstallWarp(ctx, dotPath, settings.Interval)
	if err != nil {
		return err
	}
	p.Line("  ✓ WARP heal daemon installed (every %s, kickstart target %s)", settings.Interval, daemonLabel)
	return nil
}

// applyPowerStep captures the current power values into power.json BEFORE
// applying the headless set, so uninstall can restore them. A rerun keeps
// the first capture: re-capturing the already-hardened values would make a
// later "restore" replay the hardened set and lose the original ones.
func applyPowerStep(p *Printer, mgr *watchdog.Manager, yes bool) error {
	ok, err := ui.Confirm("Apply headless power settings (pmset: no sleep on charger, autorestart, womp; restartfreeze on)?", yes)
	if err != nil {
		return err
	}
	if !ok {
		p.Line("Skipped power hardening.")
		return nil
	}
	ctx := context.Background()
	if err := mgr.Runner.RunInteractive(ctx, "sudo", "-v"); err != nil {
		return fmt.Errorf("sudo is required for pmset/systemsetup: %w", err)
	}
	if _, exists, err := watchdog.LoadPowerState(mgr.PowerStatePath()); err != nil {
		return err
	} else if !exists {
		pmsetG, err := mgr.Runner.RunQuery(ctx, "pmset", "-g", "custom")
		if err != nil {
			return fmt.Errorf("reading current pmset values: %w", err)
		}
		// systemsetup requires administrator privileges even for -get.
		freeze, err := mgr.Runner.RunQuery(ctx, "sudo", "systemsetup", "-getrestartfreeze")
		if err != nil {
			return fmt.Errorf("reading current restartfreeze value: %w", err)
		}
		state, err := watchdog.CapturePower(pmsetG.Stdout, freeze.Stdout)
		if err != nil {
			return err
		}
		if err := watchdog.SavePowerState(mgr.PowerStatePath(), state); err != nil {
			return err
		}
	} else {
		p.Line("  keeping the original power snapshot in %s (already captured)", mgr.PowerStatePath())
	}
	if err := watchdog.RunPowerCommands(ctx, mgr.Runner, true, watchdog.PowerApplyCommands()); err != nil {
		return err
	}
	_ = watchdog.AppendEvent(mgr.LogPath(), watchdog.Event{Level: "info", Event: "power", Msg: "headless power hardening applied"})
	p.Line("  ✓ power hardening applied (prior values in %s)", mgr.PowerStatePath())
	return nil
}

// restorePowerStep offers the uninstall-time restore. Interactive-only and
// default-No, matching the state/log deletion: --yes never auto-confirms.
func restorePowerStep(p *Printer, mgr *watchdog.Manager, yes bool) error {
	state, exists, err := watchdog.LoadPowerState(mgr.PowerStatePath())
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	ok, err := ui.ConfirmBool("Restore the power settings saved in power.json?", false, yes)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	ctx := context.Background()
	if err := mgr.Runner.RunInteractive(ctx, "sudo", "-v"); err != nil {
		return fmt.Errorf("sudo is required for pmset/systemsetup: %w", err)
	}
	if err := watchdog.RunPowerCommands(ctx, mgr.Runner, true, watchdog.PowerRestoreCommands(state)); err != nil {
		return err
	}
	if err := mgr.Runner.Remove(mgr.PowerStatePath()); err != nil {
		return fmt.Errorf("removing power state: %w", err)
	}
	_ = watchdog.AppendEvent(mgr.LogPath(), watchdog.Event{Level: "info", Event: "power", Msg: "power settings restored"})
	p.Line("Restored previous power settings.")
	return nil
}

// printWarpStatusRows adds the P2 rows to `dot watchdog status`: heal
// daemon, live WARP health, and the tunnel recommendation. wcfg is nil when
// no snapshot exists.
func printWarpStatusRows(p *Printer, mgr *watchdog.Manager, wcfg *config.WatchdogConfig) {
	if runtime.GOOS != "darwin" {
		p.KV("WARP daemon", "(macOS only)")
	} else {
		st := mgr.ProbeWarp(context.Background())
		p.KV("WARP daemon", filePresence(mgr.WarpPlistPath())+" loaded="+boolStr(st.Loaded))
	}
	if wcfg != nil && wcfg.Warp.Enabled && runtime.GOOS == "darwin" {
		p.KV("WARP health", probeWarpHealthSummary(mgr))
	}
	// The tunnel is the independent SSH path when WARP (and everything
	// user-domain) is down; remote hosts should carry both.
	if _, err := os.Stat(tunnel.PlistPath); err == nil {
		p.KV("Tunnel", tunnel.Label+" installed (independent SSH path)")
	} else {
		p.KV("Tunnel", "not installed — on remote hosts run `dot tunnel setup` for an independent SSH path")
	}
}

func probeWarpHealthSummary(mgr *watchdog.Manager) string {
	ctx := context.Background()
	statusOut, err := mgr.Runner.RunQuery(ctx, "warp-cli", "status")
	if err != nil {
		return "unknown (warp-cli unavailable)"
	}
	ifconfigOut, err := mgr.Runner.RunQuery(ctx, "ifconfig")
	if err != nil {
		return "unknown (ifconfig unavailable)"
	}
	if watchdog.WarpHealthy(statusOut.Stdout, ifconfigOut.Stdout) {
		return "healthy"
	}
	return fmt.Sprintf("unhealthy (warp-cli: %s)", watchdog.ParseWarpStatus(statusOut.Stdout))
}
