package cli

import (
	"context"
	"fmt"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
	"github.com/STAIxBWLB/dotfiles/internal/watchdog"
)

// dot watchdog beszel wiring (phase P4): the setup step, the status rows,
// the uninstall step, and the Homebrew resolution the agent install needs.
//
// Lifecycle: `dot watchdog setup` installs the agent LaunchAgent when
// watchdog.beszel.enabled (skipping gracefully when hub_url or the
// secrets-managed env file is missing), `dot watchdog status` reports
// presence-only rows, and `dot watchdog uninstall` removes the plist while
// always keeping the env file.

// beszelFormulas are tried in order and the first one brew can resolve wins.
// Verified 2026-09: homebrew-core carries no beszel-agent (`brew info
// beszel-agent` fails), so in practice the upstream tap
// henrygd/beszel (github.com/henrygd/homebrew-beszel) is what resolves;
// `brew install` of the tap-qualified name taps it on demand.
var beszelFormulas = []string{"beszel-agent", "henrygd/beszel/beszel-agent"}

// setupBeszelStep installs the Beszel agent LaunchAgent when
// watchdog.beszel.enabled. Validation runs BEFORE any Homebrew interaction:
// a missing hub_url or env file performs the documented non-fatal skip with
// no brew prompt and no install. The default env file carries the hub
// KEY/TOKEN, is secrets-managed, and arrives via `dot secrets restore`; a
// custom watchdog.beszel.env_path is NOT covered by `dot secrets`
// backup/restore, which both the missing-file skip and the install note say.
func setupBeszelStep(p *Printer, mgr *watchdog.Manager, bcfg config.WatchdogBeszelConfig, yes bool) error {
	settings, err := watchdog.ResolveBeszel(bcfg, mgr.Home)
	if err != nil {
		p.Warn("skipping the Beszel agent plist: %v", err)
		return nil
	}
	secretsCovered := settings.EnvPath == mgr.BeszelEnvPath()
	if !mgr.Runner.FileExists(settings.EnvPath) {
		if secretsCovered {
			p.Warn("skipping the Beszel agent plist: %s not found — restore it with `dot secrets restore` (it carries the hub KEY/TOKEN)", settings.EnvPath)
		} else {
			p.Warn("skipping the Beszel agent plist: %s not found — place it there manually; a custom watchdog.beszel.env_path is not covered by `dot secrets` backup/restore", settings.EnvPath)
		}
		return nil
	}
	if !secretsCovered {
		p.Line("note: %s is a custom env_path — it is not covered by `dot secrets` backup/restore; back it up yourself", settings.EnvPath)
	}
	ctx := context.Background()
	agentPath, err := ensureBeszelAgent(ctx, p, mgr.Runner, yes)
	if err != nil {
		return err
	}
	ok, err := ui.Confirm("Install the Beszel agent LaunchAgent "+watchdog.BeszelLabel+"?", yes)
	if err != nil {
		return err
	}
	if !ok {
		p.Line("Skipped Beszel agent.")
		return nil
	}
	if err := mgr.InstallBeszel(ctx, agentPath, settings.EnvPath, settings.Listen); err != nil {
		return err
	}
	p.Line("  ✓ Beszel agent installed (%s, hub %s, listen fallback %s)", agentPath, settings.HubURL, settings.Listen)
	return nil
}

// ensureBeszelAgent returns the beszel-agent binary path, installing the
// formula with the usual confirm when the binary is missing.
func ensureBeszelAgent(ctx context.Context, p *Printer, runner *exec.Runner, yes bool) (string, error) {
	if path := beszelAgentPath(ctx, runner); path != "" {
		return path, nil
	}
	formula := beszelFormula(ctx, runner)
	ok, err := ui.Confirm("Install the Beszel agent via Homebrew ("+formula+")?", yes)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("beszel-agent is not installed; install it with `brew install %s` and rerun `dot watchdog setup`", formula)
	}
	if _, err := runner.Run(ctx, "brew", "install", formula); err != nil {
		return "", fmt.Errorf("brew install %s: %w", formula, err)
	}
	if path := beszelAgentPath(ctx, runner); path != "" {
		return path, nil
	}
	return "", fmt.Errorf("beszel-agent binary not found after `brew install %s`", formula)
}

// beszelFormula picks the first candidate formula brew can resolve.
func beszelFormula(ctx context.Context, runner *exec.Runner) string {
	for _, formula := range beszelFormulas {
		if res, err := runner.RunQuery(ctx, "brew", "info", formula); err == nil && res.ExitCode == 0 {
			return formula
		}
	}
	// Neither resolved (brew missing or offline): fall back to the
	// tap-qualified name so the install attempt reports a useful error.
	return beszelFormulas[len(beszelFormulas)-1]
}

// beszelAgentPath locates the agent binary: PATH first, then the brew
// prefix's bin dir. Returns "" when it cannot be found.
func beszelAgentPath(ctx context.Context, runner *exec.Runner) string {
	if path, err := osexec.LookPath("beszel-agent"); err == nil {
		return path
	}
	res, err := runner.RunQuery(ctx, "brew", "--prefix")
	if err != nil || res.ExitCode != 0 {
		return ""
	}
	candidate := filepath.Join(strings.TrimSpace(res.Stdout), "bin", "beszel-agent")
	if runner.FileExists(candidate) {
		return candidate
	}
	return ""
}

// printBeszelStatusRows adds the P4 rows to `dot watchdog status`: hub from
// the snapshot config, env file PRESENCE only (its KEY/TOKEN contents never
// reach status output), plist state, and the agent binary. wcfg is nil when
// no snapshot exists.
func printBeszelStatusRows(p *Printer, mgr *watchdog.Manager, wcfg *config.WatchdogConfig) {
	if wcfg == nil || !wcfg.Beszel.Enabled {
		p.KV("Beszel agent", "disabled")
		return
	}
	// hub_url validation is setup-time; status needs the defaults even when
	// the hub is not configured yet.
	settings, _ := watchdog.ResolveBeszel(wcfg.Beszel, mgr.Home)
	p.KV("Beszel hub", wcfg.Beszel.HubURL)
	p.KV("Beszel env file", filePresence(settings.EnvPath))
	if runtime.GOOS != "darwin" {
		p.KV("Beszel plist", "(macOS only)")
		return
	}
	st := mgr.ProbeBeszel(context.Background())
	p.KV("Beszel plist", filePresence(mgr.BeszelPlistPath())+" loaded="+boolStr(st.Loaded))
	agentPath := beszelAgentPath(context.Background(), mgr.Runner)
	if agentPath == "" {
		agentPath = "not installed"
	}
	p.KV("Beszel binary", agentPath)
}

// beszelUninstallDryRunNote prints the dry-run line for the beszel removal.
// The note names the env file on purpose: it is the one watchdog-adjacent
// file uninstall never touches, and the dry-run is where that promise shows.
func beszelUninstallDryRunNote(p *Printer, mgr *watchdog.Manager) {
	if mgr.Runner.FileExists(mgr.BeszelPlistPath()) {
		p.Line("[dry-run] would unload and remove %s (keeping the secrets-managed env file)", mgr.BeszelPlistPath())
	}
}

// removeBeszelStep unloads the agent and removes the plist when it exists,
// mirroring removeMonitStep (no extra prompt). The secrets-managed env file
// (~/.config/beszel/agent.env) is deliberately left in place: its lifecycle
// belongs to `dot secrets`, and removing it here would strand the age
// archive's plaintext counterpart without a prompt.
func removeBeszelStep(p *Printer, mgr *watchdog.Manager) error {
	if !mgr.Runner.FileExists(mgr.BeszelPlistPath()) {
		return nil
	}
	if err := mgr.UninstallBeszel(context.Background()); err != nil {
		return err
	}
	p.Line("Removed Beszel agent LaunchAgent plist (the secrets-managed env file is kept).")
	return nil
}
