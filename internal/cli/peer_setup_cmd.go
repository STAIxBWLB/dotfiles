package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/syncer"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

func newPeerInitCmd() *cobra.Command {
	var host string
	var remotePath string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create the peer profile pointing at another machine",
		Args:  cobra.NoArgs,
		Long: `Write <workspace>/.dotfiles/peer/ with a target, secrets opt-in, and the
host-path list.

Use a host name that survives a network change: an SSH config alias that
maps to the other machine's private-network address (for example its
Cloudflare WARP IPv6, with HostKeyAlias), so the profile target does not
change when the address does. A laptop needs one that works without an inbound port.`,
		RunE: func(c *cobra.Command, _ []string) error {
			p := printerFrom(c)
			if strings.TrimSpace(host) == "" {
				return fmt.Errorf("--host is required (e.g. --host yj.lee@other-mac, an SSH config alias)")
			}
			bs, err := syncer.Bootstrap(peerBootstrapOptions(c))
			if err != nil {
				return err
			}
			dryRun, _ := c.Flags().GetBool("dry-run")
			res, err := syncer.PeerInit(syncer.PeerInitOptions{
				Config:     bs.Config,
				Host:       host,
				RemotePath: remotePath,
				Runner:     bs.Runner,
				DryRun:     dryRun,
			})
			if err != nil {
				return err
			}
			if res.DryRun {
				p.Line("dry-run: would write %s", res.ConfigFile)
				p.Line("dry-run: would create %s", res.AllowFile)
				p.Line("dry-run: would create %s", res.HomePathsFile)
				p.Line("dry-run: would create %s", res.HomeTrackedFile)
				p.KV("target", res.Target)
				return nil
			}

			p.Success("peer profile ready")
			p.KV("target", res.Target)
			p.KV("store", res.StoreDir)
			p.KV("secrets", "opted in via "+res.AllowFile)
			p.KV("host paths", res.HomePathsFile)
			p.KV("tracked host paths", res.HomeTrackedFile)
			deletes := "disabled; peer copy retained"
			if res.Propagation.Delete {
				deletes = "baseline-recorded paths quarantine on peer"
				if res.MaxDelete > 0 {
					deletes = fmt.Sprintf("%s (max %d per run)", deletes, res.MaxDelete)
				}
			}
			p.KV("deletes", deletes)
			p.Blank()
			p.Line("Next: dot peer doctor")
			return nil
		},
	}
	cmd.Flags().StringVar(&host, "host", "", "ssh destination for the other machine (user@host)")
	cmd.Flags().StringVar(&remotePath, "remote-path", "", "workspace path on the peer (default: same as local)")
	return cmd
}

func newPeerSetupCmd() *cobra.Command {
	return newPeerSetupCmdForOS(runtime.GOOS)
}

func newPeerSetupCmdForOS(goos string) *cobra.Command {
	var interval time.Duration
	var off bool
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Install or remove the periodic peer sync job",
		Args:  cobra.NoArgs,
		Long: `Schedule dot peer sync.

An unreachable peer exits 0, so a laptop that is away simply produces quiet
no-op runs rather than failures. That is why this can be scheduled at all.

Pick an interval in minutes, not seconds: the payload is large and each run
walks the whole tree.

Role hooks: the machine without the coordinator role must run no jobs that
write the workspace. List them in .dotfiles/peer/config.yaml:

  hooks:
    on_deactivate:
      - launchd-bootout com.maru.job.*
      - app-quit Maru
    on_activate:
      - launchd-bootstrap com.maru.job.*
      - app-open Maru

on_activate runs after this command installs the scheduler (also the step a
handover runs on the new coordinator, and the one a takeover names next);
on_deactivate runs after --off, on the old coordinator in a handover, on a
machine that demotes itself at the fence, and after a peer dot sync owner
--set/--clear that takes the role from this Mac. The launchd actions act on the
jobs of ~/Library/LaunchAgents/<glob>.plist: launchd-bootout disables them,
so a reboot does not load them again, and records which ones it disabled;
launchd-bootstrap re-enables only those, so a job stopped outside dot stays
stopped. app-quit sends an Apple Event, which macOS allows per sending
program: in a scheduled demotion that is dot itself, which must be allowed
under Privacy & Security > Automation when it first asks; until then the
action fails and the app keeps running. --dry-run lists what each would do.
Each action has a one-minute limit. Results are printed and appended to the
peer log; a failed hook never stops the command or a sync. Upgrade dot on
both Macs before adding hooks: a dot without them runs none (and one from
before #196 drops the key when it saves; later ones keep top-level keys).`,
		RunE: func(c *cobra.Command, _ []string) error {
			if goos != "darwin" {
				return fmt.Errorf("peer scheduler requires macOS launchd (host OS %s); no scheduler artifact was changed", goos)
			}
			p := printerFrom(c)
			bs, err := syncer.Bootstrap(peerBootstrapOptions(c))
			if err != nil {
				return err
			}
			dryRun, _ := c.Flags().GetBool("dry-run")
			res, err := syncer.PeerSchedule(context.Background(), syncer.PeerScheduleOptions{
				Config:   bs.Config,
				Runner:   bs.Runner,
				Probe:    probeRunner(),
				Interval: interval,
				Off:      off,
				DryRun:   dryRun,
			})
			if err != nil {
				return err
			}
			if res.Off {
				if res.DryRun {
					printPeerScheduleDryRun(p, res)
					printPeerHooks(p, res.Hooks)
					return nil
				}
				p.Success("peer sync job removed")
				printPeerHooks(p, res.Hooks)
				return nil
			}
			if res.DryRun {
				printPeerScheduleDryRun(p, res)
				printPeerHooks(p, res.Hooks)
				return nil
			}
			p.Success("peer sync scheduled every %s", res.Interval)
			p.KV("plist", res.Plist)
			p.KV("log", res.LogFile)
			if res.SeededHomeTrackedFile != "" {
				p.KV("tracked host paths", res.SeededHomeTrackedFile+" (seeded)")
			}
			printPeerHooks(p, res.Hooks)
			return nil
		},
	}
	cmd.Flags().DurationVar(&interval, "interval", 15*time.Minute, "how often to sync with the peer")
	cmd.Flags().BoolVar(&off, "off", false, "remove the scheduled job")
	return cmd
}

// printPeerHooks reports each role hook: what it did, what it would do in a
// preview, or why it failed (a failure never stops the command).
func printPeerHooks(p *Printer, hooks []syncer.HookResult) {
	for _, h := range hooks {
		switch {
		case h.Err != nil:
			p.Warn("hook %s %s: %v", h.Phase, h.Action, h.Err)
		case h.DryRun:
			p.Line("dry-run: hook %s %s: %s", h.Phase, h.Action, h.Detail)
		default:
			p.Success("hook %s %s: %s", h.Phase, h.Action, h.Detail)
		}
	}
}

func printPeerScheduleDryRun(p *Printer, res *syncer.PeerScheduleResult) {
	if res.Off {
		p.Line("dry-run: would remove %s", res.Plist)
	} else {
		p.Line("dry-run: would write %s", res.Plist)
	}
	if res.TargetUserActionRequired {
		p.Warn("target-user action required: %s", syncer.SchedulerTargetUserInstruction())
	}
}

func newPeerDoctorCmd() *cobra.Command {
	var self bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check that a peer sync would work before running one",
		Args:  cobra.NoArgs,
		Long: `Probe everything that silently breaks a peer transfer.

Checks, and why each exists:
  local rsync    a non-login shell finds macOS openrsync before Homebrew's
                 3.x; openrsync escapes non-ASCII names in the inventory
  reachability   an offline peer must be a clean no-op, not a failure
  peer dot       the newest release among the peer's dot installs; a stale
                 build at ~/.local/bin/dot must not shadow it
  remote rsync   macOS 26 ships openrsync, which cannot receive -aHAX from a
                 3.x client — and --dry-run never surfaces it, because a dry
                 run ships no file data
  clock skew     "newer wins" is only meaningful if the clocks agree
  disk headroom  the receiving side has to hold the payload
  keychain       tokens there cannot be transferred, and cannot even be
                 verified over ssh — a reminder, not a failure

Then both machines are compared, each check with the command that fixes it
and the Mac to run it on: rsync on each side; names not in NFD, judged by
the sync's own rules (an unmarked coordinator's sync refuses its own, and
the other Mac's once a pull brings them; an NFD-marked one's diff and dry
run stop on the other Mac's); which machine is the coordinator, their owner
epochs and whether their profiles point at each other, as the sync's fence
decides it (a takeover's pending fence settles at the lower epoch's next
run); a scheduler only on the coordinator; a takeover replica on the other
Mac that a takeover would accept; the config only the coordinator's copy
applies (max_delete, propagation, filter files, host_merge,
include_submodules, filter_mode) where it differs, and a host_merge that
would stop the coordinator's sync. The doctor writes nothing on either Mac.`,
		RunE: func(c *cobra.Command, _ []string) error {
			p := printerFrom(c)
			if self {
				// Plumbing for the other Mac's doctor: this machine's facts.
				bs, err := peerBootstrapReadOnly(c)
				if err != nil {
					return err
				}
				facts := syncer.LocalPeerSideFacts(c.Context(), probeRunner(), bs.Config, c.Root().Version)
				enc := json.NewEncoder(c.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(facts)
			}
			// The doctor only reads, on both Macs (#203).
			opts := peerBootstrapOptions(c)
			opts.ReadOnly = true
			bs, err := syncer.Bootstrap(opts)
			if err != nil {
				return err
			}
			report, err := syncer.PeerDoctor(context.Background(), syncer.PeerDoctorOptions{
				Config:          bs.Config,
				Probe:           probeRunner(),
				LocalDotVersion: c.Root().Version,
			})
			if err != nil {
				return err
			}

			verbose, _ := c.Flags().GetBool("verbose")
			printPeerDoctorReport(p, report, verbose)
			if report.Problems > 0 {
				return fmt.Errorf("%d peer precondition(s) need attention", report.Problems)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&self, "self", false, "print this machine's facts as JSON (read by the other Mac's doctor)")
	_ = cmd.Flags().MarkHidden("self")
	return cmd
}

func printPeerDoctorReport(p *Printer, r *syncer.PeerDoctorReport, verbose bool) ui.CheckSummary {
	p.Header("Peer Doctor")
	p.KV("Target", r.Target)
	p.KV("Local dot", r.LocalDotPath+" ("+r.LocalDotVersion+")")
	var rows []ui.CheckRow
	summary := ui.CheckSummary{}
	add := func(marker, id, state, detail string) {
		rows = append(rows, ui.CheckRow{Marker: marker, ID: id, State: state, Detail: detail})
		switch marker {
		case ui.MarkFail:
			summary.Failed++
		case ui.MarkWarn:
			summary.Warnings++
		case ui.MarkUnknown:
			summary.Unknown++
		default:
			summary.OK++
		}
	}
	if r.LocalRsyncErr != nil {
		add(ui.MarkFail, "local rsync", "failed", r.LocalRsyncErr.Error())
	} else {
		add(ui.MarkPresent, "local rsync", "ok", r.LocalRsyncPath+" ("+r.LocalRsyncVersion+")")
	}
	if r.Unreachable {
		add(ui.MarkWarn, "reachability", "warning", fmt.Sprint(r.UnreachableErr))
		p.CheckRows(rows, verbose)
		p.Line("A scheduled run would exit cleanly here; a manual one has nothing to do.")
		p.Summary(summary)
		return summary
	}
	add(ui.MarkPresent, "reachability", "ok", "reachable")
	switch {
	case r.RemoteDotErr != nil:
		add(ui.MarkFail, "peer dot", "failed", r.RemoteDotErr.Error())
	case r.DotMismatch:
		add(ui.MarkWarn, "peer dot", "warning", r.RemoteDot+" differs from this release; upgrade the older side")
	case r.DotUnreleased:
		add(ui.MarkWarn, "peer dot", "warning", r.RemoteDot+" is not a release; install a release or pin remote_dot")
	default:
		add(ui.MarkPresent, "peer dot", "ok", r.RemoteDot)
	}
	if len(r.RemoteDotPassed) > 0 {
		p.KV("Passed over", strings.Join(r.RemoteDotPassed, ", "))
	}
	if r.RemoteRsyncErr != nil {
		add(ui.MarkFail, "remote rsync", "failed", r.RemoteRsyncErr.Error())
	} else {
		detail := r.RemoteRsyncPath
		if detail == "" {
			detail = "default is usable"
		}
		add(ui.MarkPresent, "remote rsync", "ok", detail)
	}
	switch {
	case r.ClockSkewErr != nil:
		add(ui.MarkUnknown, "clock skew", "unknown", r.ClockSkewErr.Error())
	case !r.ClockSkewOK:
		add(ui.MarkFail, "clock skew", "failed", fmt.Sprintf("%s: newer-wins comparisons exceed the allowed skew", r.ClockSkew))
	default:
		add(ui.MarkPresent, "clock skew", "ok", r.ClockSkew.String())
	}
	if r.DiskKnown {
		p.KV("Peer disk", r.Disk)
	}
	switch {
	case r.PeerFactsErr != nil:
		add(ui.MarkFail, "peer facts", "failed", r.PeerFactsErr.Error())
	case r.Peer == nil:
		add(ui.MarkUnknown, "peer facts", "unknown", "not compared: peer dot did not resolve")
	}
	for _, check := range r.Checks {
		marker, state := ui.MarkPresent, "ok"
		switch check.Level {
		case syncer.DoctorFail:
			marker, state = ui.MarkFail, "failed"
		case syncer.DoctorWarn:
			marker, state = ui.MarkWarn, "warning"
		}
		detail := check.Detail
		if check.Fix != "" {
			detail += "\n" + ui.MarkNext + " " + check.Fix
		}
		add(marker, check.Name, state, detail)
	}
	p.CheckRows(rows, verbose)
	p.Line("Reminder: keychain-backed tokens cannot be synced by file copy or verified over ssh; check them locally.")
	p.Summary(summary)
	return summary
}
