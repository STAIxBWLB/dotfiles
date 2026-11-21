package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/gittransport"
)

const gitTransportLabel = "com.dotfiles.git-transport"

// newGitTransportProber is replaced in tests to keep probes off the network.
var newGitTransportProber = gittransport.NewProber

// launchd starts jobs with a bare PATH; git needs the credential helper and
// ssh on it, so the plist carries the usual install locations.
const gitTransportPlistTmpl = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>` + gitTransportLabel + `</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>git</string>
    <string>transport</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>%s/.local/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
  </dict>
  <key>StartInterval</key>
  <integer>%d</integer>
  <key>RunAtLoad</key>
  <true/>
  <key>ProcessType</key>
  <string>Background</string>
  <key>StandardOutPath</key>
  <string>%s</string>
  <key>StandardErrorPath</key>
  <string>%s</string>
</dict>
</plist>
`

func newGitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "git",
		Short: "Git settings dot manages beyond the config file",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(newGitTransportCmd())
	return cmd
}

func newGitTransportCmd() *cobra.Command {
	var check, asJSON bool
	cmd := &cobra.Command{
		Use:   "transport",
		Short: "Use HTTPS for GitHub while SSH to GitHub is blocked",
		Long: `Probe SSH and HTTPS to GitHub and point GitHub remotes at the transport
that works. The choice is written to ~/.config/git/github-transport.inc,
which the managed git config includes when a GitHub user is configured.

ssh mode rewrites https://github.com/ to git@github.com: (the default).
https mode rewrites git@github.com: and ssh://git@github.com/ to
https://github.com/, so fetch and push go through the configured
credential helper (for example gh auth git-credential).

SSH counts as usable only when GitHub authenticates the key. HTTPS counts
as usable when github.com is reachable and git can fill a credential for
it without a prompt. With SSH usable the mode is ssh; with only HTTPS
usable it is https; with neither the current mode is kept.

Git cannot switch transport in the middle of a fetch, so the mode changes
only when this command runs: by hand, or every interval through
dot git transport setup on macOS.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(c *cobra.Command, _ []string) error {
			home := homeFromCmd(c)
			dryRun, _ := c.Flags().GetBool("dry-run")
			report, err := newGitTransportProber().Run(c.Context(), home, check || dryRun)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(c, report)
			}
			p := printerFrom(c)
			p.KV("ssh", transportLine(report.SSH))
			p.KV("https", transportLine(report.HTTPS))
			before := string(report.Before)
			if before == "" {
				before = "none"
			}
			switch {
			case report.Changed:
				p.Success("%s: %s -> %s mode", time.Now().Format(time.RFC3339), before, report.After)
			case report.After != report.Before && (check || dryRun):
				p.DryRun("switch %s -> %s mode", before, report.After)
			case !report.SSH.Usable && !report.HTTPS.Usable:
				p.Warn("neither ssh nor https to GitHub works; keeping %s mode", before)
			default:
				p.Success("%s mode (unchanged)", report.After)
			}
			p.KV("file", report.File)
			if !report.Included {
				p.Warn("%s does not include %s yet, so the mode has no effect; run dot apply", "~/.config/git/config", gittransport.FileName)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "probe and report without writing the mode file")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the probe report as JSON")
	cmd.AddCommand(newGitTransportSetupCmd(), newGitTransportUninstallCmd())
	return cmd
}

func transportLine(r gittransport.Result) string {
	state := "blocked"
	if r.Usable {
		state = "usable"
	}
	return state + " (" + r.Detail + ")"
}

func gitTransportPlistPath(home string) string {
	return filepath.Join(home, "Library", "LaunchAgents", gitTransportLabel+".plist")
}

func newGitTransportSetupCmd() *cobra.Command {
	var interval time.Duration
	cmd := &cobra.Command{
		Use:          "setup",
		Short:        "Run dot git transport periodically as a LaunchAgent",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(c *cobra.Command, _ []string) error {
			p := printerFrom(c)
			home := homeFromCmd(c)
			dryRun, _ := c.Flags().GetBool("dry-run")
			if runtime.GOOS != "darwin" {
				return fmt.Errorf("git transport setup needs launchd; on Linux run dot git transport by hand")
			}
			if interval < time.Minute {
				return fmt.Errorf("--interval must be at least 1m (got %s)", interval)
			}
			exe := resolveGuardDotPath(home)
			plist := gitTransportPlistPath(home)
			logFile := filepath.Join(home, ".local", "log", "dotfiles-git-transport.log")
			if dryRun {
				p.DryRun("write %s", plist)
				p.DryRun("load %s every %s", gitTransportLabel, interval)
				return nil
			}
			if err := os.MkdirAll(filepath.Dir(logFile), 0o755); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
				return err
			}
			body := fmt.Sprintf(gitTransportPlistTmpl, exe, home, int(interval.Seconds()), logFile, logFile)
			if err := os.WriteFile(plist, []byte(body), 0o644); err != nil {
				return err
			}
			if !sameUserHome(c) {
				p.Success("wrote %s", plist)
				p.Line("  --home set: log in as that user (or use sudo -u) to load it")
				return nil
			}
			runner := guardRunner(false)
			ctx := context.Background()
			_, _ = runner.Run(ctx, "launchctl", "bootout", guiTarget()+"/"+gitTransportLabel)
			if _, err := runner.Run(ctx, "launchctl", "bootstrap", guiTarget(), plist); err != nil {
				return fmt.Errorf("loading %s: %w", plist, err)
			}
			p.Success("git transport check scheduled every %s", interval)
			p.KV("plist", plist)
			p.KV("log", logFile)
			return nil
		},
	}
	cmd.Flags().DurationVar(&interval, "interval", 10*time.Minute, "how often to probe")
	return cmd
}

func newGitTransportUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "uninstall",
		Short:        "Remove the periodic git transport check",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(c *cobra.Command, _ []string) error {
			p := printerFrom(c)
			home := homeFromCmd(c)
			plist := gitTransportPlistPath(home)
			dryRun, _ := c.Flags().GetBool("dry-run")
			if dryRun {
				p.DryRun("remove %s", plist)
				return nil
			}
			if sameUserHome(c) {
				_, _ = guardRunner(false).Run(context.Background(), "launchctl", "bootout", guiTarget()+"/"+gitTransportLabel)
			}
			if err := os.Remove(plist); err != nil && !os.IsNotExist(err) {
				return err
			}
			p.Success("git transport check removed (the current mode file is left as it is)")
			return nil
		},
	}
}
