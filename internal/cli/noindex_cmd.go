package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/noindex"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

const noindexLabel = "com.dotfiles.noindex"

// legacyNoindexLabel is the hand-rolled agent this command replaces. Booting it
// out on setup keeps two sweepers from running against the same trees.
const legacyNoindexLabel = "com.entelecheia.spotlight-nmi"

// noindexPlistTmpl mirrors the inline template used by `dot peer setup`. The
// job only touches the local filesystem, so unlike the sync agent it needs no
// PATH gymnastics.
const noindexPlistTmpl = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>` + noindexLabel + `</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>noindex</string>
  </array>
  <key>Nice</key>
  <integer>10</integer>
  <key>StartInterval</key>
  <integer>%d</integer>
  <key>RunAtLoad</key>
  <true/>
  <key>StandardOutPath</key>
  <string>%s</string>
  <key>StandardErrorPath</key>
  <string>%s</string>
</dict>
</plist>
`

func newNoindexCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "noindex [path...]",
		Short: "Keep Spotlight out of build and cache directories",
		Long: `Drop a .metadata_never_index marker into regenerable directories so
macOS Spotlight skips them and everything under them.

With no arguments this sweeps the default roots: project trees
(~/workspace, ~/Sites, ...) are walked and every node_modules, .venv,
.next, target, ... inside them gets its own marker, while tool and cache
trees (~/.local, ~/.npm, ~/.cursor, ...) get a single marker at the top.
Pass paths to walk those instead.

build/ and out/ are left alone even though dot clean calls them junk:
that is where finished deliverables land, and they should stay findable.

The marker only stops future indexing. Anything already in the Spotlight
store stays there until a full reindex (sudo mdutil -E /).

Interactive shells stamp ./node_modules right after npm/pnpm/yarn/bun
finish, so this command is the backstop for everything else.`,
		Args:         cobra.ArbitraryArgs,
		SilenceUsage: true,
		RunE:         runNoindex,
	}
	cmd.Flags().BoolP("verbose", "v", false, "List every directory marked")
	cmd.AddCommand(newNoindexSetupCmd(), newNoindexUninstallCmd())
	return cmd
}

func runNoindex(cmd *cobra.Command, args []string) error {
	home := homeFromCmd(cmd)
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	verbose, _ := cmd.Flags().GetBool("verbose")
	p := printerFrom(cmd)

	opts := noindex.Options{DryRun: dryRun}
	if len(args) > 0 {
		for _, a := range args {
			// absPath expands ~ against the invoking user; under --home the
			// effective home is the one that matters.
			if strings.HasPrefix(a, "~/") {
				a = filepath.Join(home, a[2:])
			}
			path := absPath(a)
			if _, err := os.Stat(path); err != nil {
				return fmt.Errorf("path does not exist: %s", path)
			}
			opts.WalkRoots = append(opts.WalkRoots, path)
		}
	} else {
		opts.WalkRoots = noindex.DefaultWalkRoots(home)
		opts.CacheRoots = noindex.DefaultCacheRoots(home)
	}

	res := noindex.Sweep(opts)

	if verbose {
		for _, d := range res.Marked {
			p.Bullet(ui.StyleHint.Render(ui.MarkPending), d)
		}
		for _, d := range res.Failed {
			p.Bullet(ui.StyleWarning.Render(ui.MarkWarn), d)
		}
	}

	// A read-only tree does not fail the sweep, but it must not read as
	// success either: "nothing to do" and "could not write" look identical
	// from the outside otherwise.
	if len(res.Failed) > 0 {
		p.Warn("could not mark %d directories (permissions?); re-run with -v to list them", len(res.Failed))
	}

	verb := "marked"
	if dryRun {
		verb = "would mark"
	}
	if len(res.Marked) == 0 && len(res.Failed) == 0 {
		p.Success("nothing to do (%d already marked)", res.Present)
		return nil
	}
	p.Success("%s %d directories (%d already marked)", verb, len(res.Marked), res.Present)
	return nil
}

func newNoindexSetupCmd() *cobra.Command {
	var interval time.Duration
	cmd := &cobra.Command{
		Use:          "setup",
		Short:        "Install the periodic noindex sweep as a LaunchAgent",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(c *cobra.Command, _ []string) error {
			p := printerFrom(c)
			home := homeFromCmd(c)
			dryRun, _ := c.Flags().GetBool("dry-run")
			if runtime.GOOS != "darwin" {
				return fmt.Errorf("noindex setup needs launchd; Spotlight markers only mean something on macOS")
			}
			if interval < time.Minute {
				return fmt.Errorf("--interval must be at least 1m (got %s)", interval)
			}
			// Same reasoning as guard: point launchd at ~/.local/bin/dot so the
			// job survives a rebuild or a moved source tree, and only fall back
			// to this binary's own path when that is not installed.
			exe := resolveGuardDotPath(home)
			plist := noindexPlistPath(home)
			logFile := filepath.Join(home, ".local", "log", "dotfiles-noindex.log")

			if dryRun {
				p.Line("dry-run: would write %s", plist)
				p.Line("dry-run: would load %s every %s", noindexLabel, interval)
				return nil
			}

			if err := os.MkdirAll(filepath.Dir(logFile), 0o755); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
				return err
			}
			body := fmt.Sprintf(noindexPlistTmpl, exe, int(interval.Seconds()), logFile, logFile)
			if err := os.WriteFile(plist, []byte(body), 0o644); err != nil {
				return err
			}

			ctx := context.Background()
			// launchctl can only address the *invoking* user's domain, so under
			// --home the plist belongs to someone else: write it, retire the
			// legacy files in that home, and let them load it. Same rule as
			// cleanupLegacyCloneScheduler.
			sameUser := sameUserHome(c)
			if sameUser {
				runner := guardRunner(false)
				_, _ = runner.Run(ctx, "launchctl", "bootout", guiTarget()+"/"+noindexLabel)
				if _, err := runner.Run(ctx, "launchctl", "bootstrap", guiTarget(), plist); err != nil {
					return fmt.Errorf("loading %s: %w", plist, err)
				}
			}

			cleanupLegacyNoindex(ctx, p, home, sameUser)

			if !sameUser {
				p.Success("wrote %s", plist)
				p.Line("  --home set: log in as that user (or use sudo -u) to load it")
				return nil
			}
			p.Success("noindex sweep scheduled every %s", interval)
			p.KV("plist", plist)
			p.KV("log", logFile)
			return nil
		},
	}
	cmd.Flags().DurationVar(&interval, "interval", 6*time.Hour, "how often to sweep")
	return cmd
}

func newNoindexUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "uninstall",
		Short:        "Remove the periodic noindex sweep",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(c *cobra.Command, _ []string) error {
			p := printerFrom(c)
			home := homeFromCmd(c)
			plist := noindexPlistPath(home)
			dryRun, _ := c.Flags().GetBool("dry-run")
			if dryRun {
				p.Line("dry-run: would remove %s", plist)
				return nil
			}
			if sameUserHome(c) {
				_, _ = guardRunner(false).Run(context.Background(), "launchctl", "bootout", guiTarget()+"/"+noindexLabel)
			}
			if err := os.Remove(plist); err != nil && !os.IsNotExist(err) {
				return err
			}
			p.Success("noindex sweep removed (existing markers are left in place)")
			return nil
		},
	}
}

func noindexPlistPath(home string) string {
	return filepath.Join(home, "Library", "LaunchAgents", noindexLabel+".plist")
}

func guiTarget() string {
	return "gui/" + strconv.Itoa(os.Getuid())
}

// sameUserHome reports whether the effective home belongs to the invoking user.
// launchctl can only address gui/<our uid>, so under --home every launchctl
// call would hit the wrong domain with paths from another home.
func sameUserHome(cmd *cobra.Command) bool {
	over, _ := cmd.Flags().GetString("home")
	return over == ""
}

// cleanupLegacyNoindex retires the hand-rolled spotlight-nmi setup this command
// replaces: its LaunchAgent, its sweeper script, and the shell snippet that
// defined the npm/pnpm/yarn/bun wrappers. The wrappers now live in
// ~/.config/shell/00-exports.sh, which `dot apply` owns; leaving the old file
// behind would re-shadow the managed pnpm wrapper. Best-effort and quiet when
// nothing is installed. Unloading is only attempted for the invoking user
// (sameUser); under --home only the files in the target home are removed.
func cleanupLegacyNoindex(ctx context.Context, p *Printer, home string, sameUser bool) {
	legacyPlist := filepath.Join(home, "Library", "LaunchAgents", legacyNoindexLabel+".plist")
	if _, err := os.Stat(legacyPlist); err == nil && sameUser {
		_, _ = guardRunner(false).Run(ctx, "launchctl", "bootout", guiTarget()+"/"+legacyNoindexLabel)
	}
	for _, path := range []string{
		legacyPlist,
		filepath.Join(home, ".local", "bin", "nmi-stamp"),
		filepath.Join(home, ".config", "shrc", "40-spotlight-nmi"),
	} {
		if err := os.Remove(path); err == nil {
			p.Line("  removed legacy %s", path)
		}
	}
}
