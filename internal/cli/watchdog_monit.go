package cli

import (
	"context"
	"fmt"
	"os"
	osexec "os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
	"github.com/STAIxBWLB/dotfiles/internal/watchdog"
)

// dot watchdog monit — the P3 setup/status/uninstall steps for monit
// service-health supervision and the Screen Sharing heal helper.

// setupMonitStep ensures the monit binary, renders the monitrc, and loads the
// user LaunchAgent, then installs the root Screen Sharing heal helper when
// the operator consented to it. dotPath is the resolved `dot` binary the
// monitrc's notify execs invoke.
func setupMonitStep(p *Printer, mgr *watchdog.Manager, wcfg config.WatchdogConfig, dotPath string, yes bool) error {
	settings, err := watchdog.ResolveMonit(wcfg.Monit, runtime.NumCPU())
	if err != nil {
		return err
	}
	ctx := context.Background()
	monitPath, err := resolveMonitPath(ctx, mgr.Runner)
	if err != nil {
		install, cerr := ui.Confirm("monit is not installed; install it with `brew install monit`?", yes)
		if cerr != nil {
			return cerr
		}
		if !install {
			return fmt.Errorf("monit is required by watchdog.monit; install it (`brew install monit`) and rerun `dot watchdog setup`")
		}
		if err := mgr.Runner.RunInteractive(ctx, "brew", "install", "monit"); err != nil {
			return fmt.Errorf("installing monit: %w", err)
		}
		monitPath, err = resolveMonitPath(ctx, mgr.Runner)
		if err != nil {
			return fmt.Errorf("monit still not found after the brew install: %w", err)
		}
	}
	ok, err := ui.Confirm("Install the monit LaunchAgent "+watchdog.MonitLabel+" (load/CPU alerts to dot watchdog notify)?", yes)
	if err != nil {
		return err
	}
	if !ok {
		p.Line("Skipped monit agent.")
		return nil
	}
	// The heal consent is decided BEFORE the render: the monitrc's
	// screensharing check execs the sudo grant this step installs, so a
	// declined helper must leave a monitrc without the check.
	heal := false
	if settings.ScreenSharing {
		heal, err = ui.Confirm(fmt.Sprintf("Install the Screen Sharing heal helper %s and its passwordless sudo grant (root)?", watchdog.ScreenSharingHealPath), yes)
		if err != nil {
			return err
		}
		if !heal {
			settings.ScreenSharing = false
			p.Line("Screen Sharing heal helper skipped; the monitrc omits the screensharing check.")
		}
	}
	monitrc, err := watchdog.RenderMonitrc(dotPath, mgr.MonitLogPath(), settings)
	if err != nil {
		return err
	}
	if err := mgr.InstallMonit(ctx, monitPath, monitrc); err != nil {
		return err
	}
	p.Line("  ✓ monit agent installed (load > %g, cpu user > %g%%, system > %g%%, %d cycles)",
		settings.Load1Threshold, settings.CPUUserThreshold, settings.CPUSystemThreshold, settings.Cycles)
	if heal {
		if err := installScreenSharingHeal(p, mgr); err != nil {
			return err
		}
	}
	return nil
}

// resolveMonitPath finds the monit binary: PATH first, then the Homebrew
// prefix locations. The fallback matters in GUI/launchd contexts where
// /opt/homebrew/bin (or /usr/local/bin on Intel) is not in the process PATH
// and a fresh `brew install monit` still leaves LookPath empty-handed.
func resolveMonitPath(ctx context.Context, runner *exec.Runner) (string, error) {
	if path, err := osexec.LookPath("monit"); err == nil {
		return path, nil
	}
	candidates := []string{"/opt/homebrew/bin/monit", "/usr/local/bin/monit"}
	if out, err := runner.RunQuery(ctx, "brew", "--prefix"); err == nil {
		if prefix := strings.TrimSpace(out.Stdout); prefix != "" {
			candidates = append([]string{filepath.Join(prefix, "bin", "monit")}, candidates...)
		}
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("monit not found in PATH or the Homebrew prefix locations")
}

// installScreenSharingHeal installs the root-owned heal helper and its
// passwordless sudoers grant after the operator consented, with the same
// prime-then-install discipline as the WARP daemon step.
func installScreenSharingHeal(p *Printer, mgr *watchdog.Manager) error {
	ctx := context.Background()
	if err := mgr.Runner.RunInteractive(ctx, "sudo", "-v"); err != nil {
		return fmt.Errorf("sudo is required for the root helper and sudoers grant: %w", err)
	}
	current, err := user.Current()
	if err != nil {
		return fmt.Errorf("resolving the installing user for the sudoers grant: %w", err)
	}
	if err := mgr.InstallScreenSharingHeal(ctx, current.Username); err != nil {
		return err
	}
	p.Line("  ✓ Screen Sharing heal helper installed (sudoers grant %s)", watchdog.ScreenSharingSudoersPath)
	return nil
}

// printMonitStatusRows adds the P3 rows to `dot watchdog status`: the monit
// binary, the agent's plist/loaded state, and monitrc drift against the
// resolved snapshot config. wcfg is nil when no snapshot exists.
func printMonitStatusRows(p *Printer, mgr *watchdog.Manager, wcfg *config.WatchdogConfig) {
	if runtime.GOOS != "darwin" {
		p.KV("Monit", "(macOS only)")
		return
	}
	if monitPath, err := osexec.LookPath("monit"); err == nil {
		p.KV("Monit", monitPath)
	} else {
		p.KV("Monit", "not installed")
	}
	st := mgr.ProbeMonit(context.Background())
	p.KV("Monit agent", filePresence(mgr.MonitPlistPath())+" loaded="+boolStr(st.Loaded))
	p.KV("Monitrc", monitrcStatus(mgr, wcfg))
}

// monitrcStatus compares the on-disk monitrc with what the snapshot would
// render today. A drifted control file means monit runs stale thresholds.
func monitrcStatus(mgr *watchdog.Manager, wcfg *config.WatchdogConfig) string {
	data, err := os.ReadFile(mgr.MonitrcPath())
	if os.IsNotExist(err) {
		return "missing"
	}
	if err != nil {
		return "unreadable: " + err.Error()
	}
	if wcfg == nil || !wcfg.Monit.Enabled {
		return "present"
	}
	settings, err := watchdog.ResolveMonit(wcfg.Monit, runtime.NumCPU())
	if err != nil {
		return "present (monit config invalid: " + err.Error() + ")"
	}
	dotPath, err := osexec.LookPath("dot")
	if err != nil {
		return "present (dot not in PATH; drift unknown)"
	}
	monitrc, err := watchdog.RenderMonitrc(dotPath, mgr.MonitLogPath(), settings)
	if err != nil {
		return "present (render error: " + err.Error() + ")"
	}
	if string(data) == monitrc {
		return "in sync"
	}
	return "DRIFTED — rerun `dot watchdog setup`"
}

// removeMonitStep unloads the agent and removes the plist and monitrc when
// either exists, mirroring the reaper removal (no extra prompt).
func removeMonitStep(p *Printer, mgr *watchdog.Manager) error {
	if !mgr.Runner.FileExists(mgr.MonitPlistPath()) && !mgr.Runner.FileExists(mgr.MonitrcPath()) {
		return nil
	}
	if err := mgr.UninstallMonit(context.Background()); err != nil {
		return err
	}
	p.Line("Removed monit agent and monitrc.")
	return nil
}

// removeScreenSharingHealStep offers the root helper/sudoers removal under
// the same interactive-only, default-No discipline as the power restore:
// --yes never auto-confirms a root-owned change.
func removeScreenSharingHealStep(p *Printer, mgr *watchdog.Manager, yes bool) error {
	if !mgr.Runner.FileExists(watchdog.ScreenSharingHealPath) && !mgr.Runner.FileExists(watchdog.ScreenSharingSudoersPath) {
		return nil
	}
	ok, err := ui.ConfirmBool("Remove the root Screen Sharing heal helper and its sudoers grant?", false, yes)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	ctx := context.Background()
	if err := mgr.Runner.RunInteractive(ctx, "sudo", "-v"); err != nil {
		return fmt.Errorf("sudo is required to remove the root helper and sudoers grant: %w", err)
	}
	if err := mgr.UninstallScreenSharingHeal(ctx); err != nil {
		return err
	}
	p.Line("Removed Screen Sharing heal helper and sudoers grant.")
	return nil
}
