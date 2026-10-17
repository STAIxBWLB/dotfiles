package watchdog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// Manager owns the on-disk locations and launchd lifecycle of the watchdog.
// Everything macOS-specific is gated on GOOS at runtime (never build tags),
// so the package compiles and its pure logic stays testable on Linux.
type Manager struct {
	Runner *exec.Runner
	Home   string
	GOOS   string // empty means runtime.GOOS
}

// NewManager builds a Manager for the given home on the current platform.
func NewManager(runner *exec.Runner, home string) *Manager {
	return &Manager{Runner: runner, Home: home}
}

func (m *Manager) goos() string {
	if m.GOOS != "" {
		return m.GOOS
	}
	return runtime.GOOS
}

// PlistPath is where the reaper LaunchAgent lives.
func (m *Manager) PlistPath() string {
	return filepath.Join(m.Home, "Library", "LaunchAgents", ReapLabel+".plist")
}

// StateDir holds the resolved config snapshot and the sample history.
func (m *Manager) StateDir() string {
	return filepath.Join(m.Home, ".local", "state", "dot", "watchdog")
}

// SnapshotPath is the resolved watchdog config the scheduled reaper reads.
func (m *Manager) SnapshotPath() string {
	return filepath.Join(m.StateDir(), "watchdog.yaml")
}

// SamplesPath is the cross-run CPU history.
func (m *Manager) SamplesPath() string {
	return filepath.Join(m.StateDir(), "samples.json")
}

// LogDir holds the JSON-lines watchdog log and the launchd stdout/stderr logs.
func (m *Manager) LogDir() string {
	return filepath.Join(m.Home, "Library", "Logs", "dot")
}

// LogPath is the JSON-lines event log `dot watchdog log` tails.
func (m *Manager) LogPath() string {
	return filepath.Join(m.LogDir(), "watchdog.log")
}

// ErrNeedsDarwin gates launchd operations on non-macOS builds at runtime.
var ErrNeedsDarwin = errors.New("the watchdog LaunchAgent is macOS-only")

// Install writes the config snapshot and the reaper plist, then (re)loads
// the LaunchAgent. Idempotent: any pre-loaded copy is unloaded first so an
// interval change takes effect on rerun.
func (m *Manager) Install(ctx context.Context, dotPath string, interval time.Duration, snapshot []byte) error {
	if m.goos() != "darwin" {
		return ErrNeedsDarwin
	}
	for _, dir := range []string{filepath.Dir(m.PlistPath()), m.StateDir(), m.LogDir()} {
		if err := m.Runner.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating %s: %w", dir, err)
		}
	}
	if err := m.Runner.WriteFileAtomic(m.SnapshotPath(), snapshot, 0o644); err != nil {
		return fmt.Errorf("writing config snapshot: %w", err)
	}
	plist := RenderReapPlist(dotPath, interval, m.LogDir())
	if err := m.Runner.WriteFileAtomic(m.PlistPath(), []byte(plist), 0o644); err != nil {
		return fmt.Errorf("writing plist: %w", err)
	}
	_, _ = m.Runner.Run(ctx, "launchctl", "unload", m.PlistPath())
	if _, err := m.Runner.Run(ctx, "launchctl", "load", m.PlistPath()); err != nil {
		return fmt.Errorf("loading plist: %w", err)
	}
	return nil
}

// Uninstall unloads the LaunchAgent and removes the plist. Missing files
// are not an error. The state dir and log are deliberately kept; the CLI
// asks about those separately.
func (m *Manager) Uninstall(ctx context.Context) error {
	if m.goos() != "darwin" {
		return ErrNeedsDarwin
	}
	_, _ = m.Runner.Run(ctx, "launchctl", "unload", m.PlistPath())
	if !m.Runner.FileExists(m.PlistPath()) {
		return nil
	}
	if err := m.Runner.Remove(m.PlistPath()); err != nil {
		return fmt.Errorf("removing plist: %w", err)
	}
	return nil
}

// Status is the launchd-side view of the reaper unit.
type Status struct {
	PlistExists bool
	Loaded      bool
}

// Probe reports whether the plist exists and the unit is loaded. On
// non-darwin systems the probe is plist-only.
func (m *Manager) Probe(ctx context.Context) Status {
	st := Status{PlistExists: m.Runner.FileExists(m.PlistPath())}
	if !st.PlistExists || m.goos() != "darwin" {
		return st
	}
	target := fmt.Sprintf("gui/%d/%s", os.Getuid(), ReapLabel)
	result, err := m.Runner.RunQuery(ctx, "launchctl", "print", target)
	st.Loaded = err == nil && result != nil && result.ExitCode == 0
	return st
}

// WarpPlistPath is where the root WARP-heal LaunchDaemon lives (system
// domain, not under Home).
func (m *Manager) WarpPlistPath() string {
	return "/Library/LaunchDaemons/" + WarpLabel + ".plist"
}

// WarpStatePath is the consecutive-failure/heal bookkeeping shared by the
// root daemon and manual passes.
func (m *Manager) WarpStatePath() string {
	return filepath.Join(m.StateDir(), "warp.json")
}

// PowerStatePath holds the pre-hardening pmset/systemsetup values.
func (m *Manager) PowerStatePath() string {
	return filepath.Join(m.StateDir(), "power.json")
}

// InstallWarp resolves the Cloudflare WARP daemon label, installs the
// root heal daemon, persists the label BEFORE the RunAtLoad job can fire
// its first pass, and bootstraps. The caller primes sudo first. Idempotent:
// any loaded copy is booted out first so an interval change takes effect on
// rerun. Refuses to install when the WARP daemon itself cannot be found —
// kickstarting the wrong service later is worse.
func (m *Manager) InstallWarp(ctx context.Context, dotPath string, interval time.Duration) (string, error) {
	if m.goos() != "darwin" {
		return "", ErrNeedsDarwin
	}
	// `sudo launchctl list` inspects the system domain, where Cloudflare's
	// LaunchDaemon lives; the bare form would list only the user's domain
	// and report WARP absent on a healthy install.
	out, err := m.Runner.RunQuery(ctx, "sudo", "launchctl", "list")
	if err != nil {
		return "", fmt.Errorf("listing launchd services: %w", err)
	}
	daemonLabel, err := ResolveWarpDaemonLabel(out.Stdout)
	if err != nil {
		return "", err
	}
	plist := RenderWarpPlist(dotPath, m.Home, interval, m.LogDir())
	if err := sudoInstallContent(ctx, m.Runner, []byte(plist), m.WarpPlistPath(), 0o644); err != nil {
		return "", err
	}
	// The daemon starts at bootstrap (RunAtLoad), so the label must be on
	// disk first: a first pass that reads a label-less state would save it
	// back over the value this install resolved.
	state, err := LoadWarpState(m.WarpStatePath())
	if err != nil {
		return "", err
	}
	state.DaemonLabel = daemonLabel
	if err := SaveWarpState(m.WarpStatePath(), state); err != nil {
		return "", err
	}
	_, _ = m.Runner.Run(ctx, "sudo", "launchctl", "bootout", "system/"+WarpLabel)
	if _, err := m.Runner.Run(ctx, "sudo", "launchctl", "bootstrap", "system", m.WarpPlistPath()); err != nil {
		return "", fmt.Errorf("bootstrapping warp daemon: %w", err)
	}
	return daemonLabel, nil
}

// UninstallWarp boots out the heal daemon and removes its plist. Missing
// files are not an error. The caller primes sudo first.
func (m *Manager) UninstallWarp(ctx context.Context) error {
	if m.goos() != "darwin" {
		return ErrNeedsDarwin
	}
	_, _ = m.Runner.Run(ctx, "sudo", "launchctl", "bootout", "system/"+WarpLabel)
	if !m.Runner.FileExists(m.WarpPlistPath()) {
		return nil
	}
	if _, err := m.Runner.Run(ctx, "sudo", "rm", "-f", m.WarpPlistPath()); err != nil {
		return fmt.Errorf("removing warp plist: %w", err)
	}
	return nil
}

// ProbeWarp reports whether the heal daemon's plist exists and, when the
// system-domain print succeeds without root, whether it is loaded. A print
// that needs privileges leaves Loaded false rather than failing the status
// command.
func (m *Manager) ProbeWarp(ctx context.Context) Status {
	st := Status{PlistExists: m.Runner.FileExists(m.WarpPlistPath())}
	if !st.PlistExists || m.goos() != "darwin" {
		return st
	}
	result, err := m.Runner.RunQuery(ctx, "launchctl", "print", "system/"+WarpLabel)
	st.Loaded = err == nil && result != nil && result.ExitCode == 0
	return st
}

// sudoInstallContent stages content to a temp file and installs it
// root:wheel (mirrors tunnel.SudoInstallContent, kept local so the watchdog
// engine does not depend on the tunnel engine).
func sudoInstallContent(ctx context.Context, runner *exec.Runner, content []byte, dest string, mode os.FileMode) error {
	tmp, err := os.CreateTemp("", "dot-watchdog-*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp file: %w", err)
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if _, err := runner.Run(ctx, "sudo", "install", "-m", fmt.Sprintf("%04o", mode.Perm()), "-o", "root", "-g", "wheel", tmpPath, dest); err != nil {
		return fmt.Errorf("installing %s: %w", dest, err)
	}
	return nil
}
