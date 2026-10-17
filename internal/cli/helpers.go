package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

func absPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	if cwd, err := os.Getwd(); err == nil {
		return filepath.Clean(filepath.Join(cwd, path))
	}
	return filepath.Clean(path)
}

func defaultWorkspaceWorkRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "workspace", "work")
}

// cleanupLegacyCloneScheduler removes scheduler units installed by the
// removed `dot clone` (rclone) command, which otherwise keep firing with no
// remaining subcommand able to uninstall them. Best-effort and silent when
// nothing is installed. Unload/disable is only attempted for the invoking
// user (sameUser); under --home only the unit files in the target home are
// removed.
func cleanupLegacyCloneScheduler(ctx context.Context, p *Printer, runner *exec.Runner, home string, sameUser bool) {
	if runtime.GOOS == "darwin" {
		plist := filepath.Join(home, "Library", "LaunchAgents", "com.rclone.workspace-bisync.plist")
		if !runner.FileExists(plist) {
			return
		}
		if sameUser {
			_, _ = runner.Run(ctx, "launchctl", "unload", plist)
		}
		if err := runner.Remove(plist); err == nil {
			p.Line("Removed legacy 'dot clone' scheduler (%s)", plist)
		}
		return
	}
	unitDir := filepath.Join(home, ".config", "systemd", "user")
	timer := filepath.Join(unitDir, "rclone-bisync.timer")
	service := filepath.Join(unitDir, "rclone-bisync.service")
	if !runner.FileExists(timer) && !runner.FileExists(service) {
		return
	}
	if sameUser {
		_, _ = runner.Run(ctx, "systemctl", "--user", "disable", "--now", "rclone-bisync.timer")
	}
	removed := false
	for _, unit := range []string{timer, service} {
		if runner.FileExists(unit) && runner.Remove(unit) == nil {
			removed = true
		}
	}
	if removed {
		if sameUser {
			_, _ = runner.Run(ctx, "systemctl", "--user", "daemon-reload")
		}
		p.Line("Removed legacy 'dot clone' scheduler units (rclone-bisync)")
	}
}

func formatInterval(seconds int) string {
	if seconds <= 0 {
		return "(off)"
	}
	if seconds%3600 == 0 {
		return fmt.Sprintf("%dh", seconds/3600)
	}
	if seconds%60 == 0 {
		return fmt.Sprintf("%dm", seconds/60)
	}
	return fmt.Sprintf("%ds", seconds)
}

// containsString reports whether needle appears in haystack.
func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
