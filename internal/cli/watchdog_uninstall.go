package cli

import (
	"context"
	"fmt"
	"os"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/ui"
	"github.com/STAIxBWLB/dotfiles/internal/watchdog"
)

// dot watchdog uninstall — removes the reaper agent, the monit supervision
// agent, the Beszel agent plist, and the root daemons, with interactive-only
// gates for every root-owned or destructive step. The secrets-managed Beszel
// env file is never removed.

func newWatchdogUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the watchdog reaper agent, WARP heal daemon, monit supervision, and Beszel agent plist (macOS)",
		Long: `Unload and remove the reaper LaunchAgent, the monit agent and monitrc,
the Beszel agent LaunchAgent, and, when installed, the root WARP heal
LaunchDaemon. The secrets-managed Beszel env file
(~/.config/beszel/agent.env) is never removed. Restoring the power settings
saved by setup --headless, removing the root Screen Sharing heal helper and
its sudoers grant, and removing the state directory and logs are
interactive-only prompts that default to No; --yes never auto-confirms them.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWatchdogUninstallForGOOS(cmd, args, runtime.GOOS)
		},
		SilenceUsage: true,
	}
}

func runWatchdogUninstallForGOOS(cmd *cobra.Command, _ []string, goos string) error {
	if goos != "darwin" {
		return fmt.Errorf("dot watchdog uninstall is macOS-only")
	}
	if homeOverride, _ := cmd.Flags().GetString("home"); homeOverride != "" {
		return fmt.Errorf("--home is not supported; the watchdog LaunchAgent manages this Mac's user domain")
	}
	p := printerFrom(cmd)
	yes, _ := cmd.Flags().GetBool("yes")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cannot determine home directory: %w", err)
	}
	mgr := watchdog.NewManager(watchdogRunner(dryRun), home)
	if dryRun {
		p.Line("[dry-run] would unload and remove %s", mgr.PlistPath())
		if mgr.Runner.FileExists(mgr.WarpPlistPath()) {
			p.Line("[dry-run] would boot out and remove %s", mgr.WarpPlistPath())
		}
		if mgr.Runner.FileExists(mgr.MonitPlistPath()) || mgr.Runner.FileExists(mgr.MonitrcPath()) {
			p.Line("[dry-run] would unload and remove %s and %s", mgr.MonitPlistPath(), mgr.MonitrcPath())
		}
		beszelUninstallDryRunNote(p, mgr)
		if mgr.Runner.FileExists(watchdog.ScreenSharingHealPath) || mgr.Runner.FileExists(watchdog.ScreenSharingSudoersPath) {
			p.Line("[dry-run] would offer to remove %s and %s", watchdog.ScreenSharingHealPath, watchdog.ScreenSharingSudoersPath)
		}
		if _, exists, _ := watchdog.LoadPowerState(mgr.PowerStatePath()); exists {
			p.Line("[dry-run] would offer to restore the power settings in %s", mgr.PowerStatePath())
		}
		return nil
	}
	if err := mgr.Uninstall(context.Background()); err != nil {
		return err
	}
	p.Line("Removed LaunchAgent plist (if present).")
	if mgr.Runner.FileExists(mgr.WarpPlistPath()) {
		if err := mgr.Runner.RunInteractive(context.Background(), "sudo", "-v"); err != nil {
			return fmt.Errorf("sudo is required to remove the system-domain daemon: %w", err)
		}
		if err := mgr.UninstallWarp(context.Background()); err != nil {
			return err
		}
		p.Line("Removed WARP heal daemon.")
	}
	if err := removeMonitStep(p, mgr); err != nil {
		return err
	}
	if err := removeBeszelStep(p, mgr); err != nil {
		return err
	}
	if err := removeScreenSharingHealStep(p, mgr, yes); err != nil {
		return err
	}
	if err := restorePowerStep(p, mgr, yes); err != nil {
		return err
	}
	removeState, err := ui.ConfirmBool(
		fmt.Sprintf("Remove watchdog state and logs (%s, %s)?", mgr.StateDir(), mgr.LogPath()), false, yes)
	if err != nil {
		return err
	}
	if removeState {
		if err := mgr.Runner.RemoveAll(mgr.StateDir()); err != nil {
			return fmt.Errorf("removing state dir: %w", err)
		}
		if mgr.Runner.FileExists(mgr.LogPath()) {
			if err := mgr.Runner.Remove(mgr.LogPath()); err != nil {
				return fmt.Errorf("removing log: %w", err)
			}
		}
		p.Line("Removed watchdog state and logs.")
	}
	p.Success("✓ dot watchdog uninstalled")
	return nil
}
