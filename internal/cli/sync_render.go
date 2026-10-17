package cli

import (
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/syncer"
	"github.com/spf13/cobra"
)

// Formatting helpers shared by the dot sync renderers, plus the one flag
// parser every transfer command reads.

func gdriveSyncModeFrom(cmd *cobra.Command) (syncer.RunMode, error) {
	raw, _ := cmd.Flags().GetString("mode")
	mode, err := syncer.ParseRunMode(raw)
	if err != nil {
		return "", fmt.Errorf("--mode: %w", err)
	}
	return mode, nil
}

func printPathList(p *Printer, paths []string) {
	for _, path := range paths {
		p.Line("  -  %s", path)
	}
}

func affectedDirsFromLists(groups ...[]string) []string {
	seen := map[string]struct{}{}
	for _, group := range groups {
		for _, rel := range group {
			dir := filepath.ToSlash(filepath.Dir(rel))
			if dir == "." || dir == "/" {
				dir = "."
			}
			seen[dir] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for dir := range seen {
		out = append(out, dir)
	}
	sort.Strings(out)
	return out
}

func differenceStrings(all, subtract []string) []string {
	if len(all) == 0 {
		return nil
	}
	remove := map[string]struct{}{}
	for _, s := range subtract {
		remove[s] = struct{}{}
	}
	out := make([]string, 0, len(all))
	for _, s := range all {
		if _, ok := remove[s]; ok {
			continue
		}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func pullConflictPaths(conflicts []syncer.PullConflict) []string {
	out := make([]string, 0, len(conflicts))
	for _, c := range conflicts {
		out = append(out, c.RelPath)
	}
	sort.Strings(out)
	return out
}

func pushConflictPaths(conflicts []syncer.PushConflict) []string {
	out := make([]string, 0, len(conflicts))
	for _, c := range conflicts {
		out = append(out, c.RelPath)
	}
	sort.Strings(out)
	return out
}

func tombstonePaths(tombstones []syncer.Tombstone) []string {
	out := make([]string, 0, len(tombstones))
	for _, t := range tombstones {
		out = append(out, t.RelPath)
	}
	sort.Strings(out)
	return out
}

func formatLastSync(t time.Time) string {
	if t.IsZero() {
		return "(never)"
	}
	ago := time.Since(t).Truncate(time.Second)
	return fmt.Sprintf("%s ago", ago)
}

func boolStr(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// scheduleUnitLabel returns a human-friendly identifier for the scheduler
// artifact on the current platform — the launchd plist path on macOS,
// or the systemd timer unit path on Linux.
func scheduleUnitLabel(paths *syncer.Paths) string {
	if runtime.GOOS == "darwin" {
		return paths.LaunchdPlist
	}
	return paths.SystemdTimer
}

func stripTrailingSlash(p string) string {
	if len(p) > 1 && p[len(p)-1] == '/' {
		return p[:len(p)-1]
	}
	return p
}
