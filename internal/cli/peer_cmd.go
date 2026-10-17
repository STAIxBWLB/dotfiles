package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/fileutil"
	"github.com/STAIxBWLB/dotfiles/internal/syncer"
)

// PeerProfile is the sync profile name used for machine-to-machine sync.
const PeerProfile = syncer.PeerProfile

func newPeerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "peer",
		Short: "Sync the workspace directly to another machine over SSH",
		Args:  cobra.NoArgs,
		Long: `Machine-to-machine workspace sync, so either Mac can continue the same work.

This is a sibling of ` + "`dot sync`" + `, not a replacement. They answer different
questions:

  dot sync   workspace -> cloud mirror. One writer. Secrets excluded.
             For reading the workspace from a phone or another device.
  dot peer   workspace <-> another machine over ssh. Both directions.
             Secrets included. Submodule working trees included, because
             uncommitted work inside a submodule is exactly what Git has
             not seen and what a second machine still needs.
             Linked Git worktrees (.claude/worktrees, .qwen/worktrees,
             .worktrees) are never carried: they are branch checkouts that
             travel through Git.

Routine one-sided changes transfer directly. A path changed differently on
both machines is resolved by the configured coordinator, and the losing peer
payload is quarantined once under .sync-conflicts/ when it exists.

With propagation.delete on, a file removed here is removed on the peer too,
into that same quarantine, and "dot sync conflicts prune" expires it later.
Only paths recorded in the baseline are eligible: a path the peer created and
this machine has never seen is not a deletion and is left alone. The set is
capped by max_delete, so a failed mount cannot present the whole tree as
deleted. With propagation.delete off, a file removed here simply stops being
sent and the peer keeps its copy.

Host paths listed in home-paths-tracked.txt get the same baseline-aware
treatment: their deletes propagate and simultaneous edits quarantine the
losing payload under ~/.dot-peer-conflicts. Host paths left in home-paths.txt
alone stay additive, newest-mtime wins.

A peer that is offline is not an error: the scheduled run probes reachability
first and exits cleanly when the other machine is away.

One Mac is the coordinator at a time, but the role can move. A planned switch
(dot peer handover) syncs once, moves ownership with a higher epoch, and
bootstraps the new coordinator with no baseline. An unplanned switch (dot
peer takeover) installs the replica the last coordinator pushed after every
complete run, then fences the returning machine by epoch: the loser adopts
the new owner and removes its scheduler, so two coordinators never run.`,
		RunE: func(c *cobra.Command, _ []string) error { return c.Help() },
	}

	cmd.AddCommand(newPeerInitCmd())
	cmd.AddCommand(newPeerStatusCmd())
	cmd.AddCommand(newPeerDoctorCmd())
	cmd.AddCommand(newPeerSyncCmd())
	cmd.AddCommand(newPeerSetupCmd())
	cmd.AddCommand(newPeerDiffCmd())
	cmd.AddCommand(newPeerHomePathsCmd())
	cmd.AddCommand(newPeerGitCmd())
	cmd.AddCommand(newPeerHandoverCmd())
	cmd.AddCommand(newPeerTakeoverCmd())
	cmd.AddCommand(newPeerAdoptCmd())
	return cmd
}

// peerBootstrapOptions translates the peer command tree's flags into the
// engine's bootstrap options. The profile is fixed here, which is why
// `--profile` is deliberately not read: `dot peer` only ever addresses the
// peer store.
func peerBootstrapOptions(cmd *cobra.Command) syncer.BootstrapOptions {
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	verbose, _ := cmd.Flags().GetBool("verbose")
	return syncer.BootstrapOptions{Profile: PeerProfile, DryRun: dryRun, Verbose: verbose, Home: homeOverrideFrom(cmd)}
}

// probeRunner is always live, even under --dry-run.
//
// Reachability, the remote rsync version and the clock are read-only questions
// about the peer, and a dry-run runner does not execute anything - it returns
// empty output. That made every probe answer "nothing found", so `peer sync
// --dry-run` failed with "no rsync found" against a peer doctor had just
// confirmed. A dry run must still be able to inspect the remote; only the
// transfer itself is what --dry-run holds back.
func probeRunner() *exec.Runner {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return exec.NewRunner(false, logger)
}

// scheduledRunEnv marks a run started by the launchd job `dot peer schedule`
// installs. The plist plants it (internal/syncer/peer_commands.go); this is
// the only place that reads it.
//
// The mechanism was chosen rather than inherited: launchd sets no
// distinguishing variable of its own, a TTY check misfires under CI and under
// a pipe, and a new persistent flag would appear in the command's help output
// for a mode no human invokes. The plist already had an EnvironmentVariables
// dict, so the marker cost one entry there.
const scheduledRunEnv = "DOT_SCHEDULED_RUN"

// isScheduledRun reports whether this process was started by the peer
// scheduler rather than by a person.
func isScheduledRun() bool { return os.Getenv(scheduledRunEnv) != "" }

// quietScheduledContention turns a lost lock race into a clean exit on a
// scheduled run and leaves every other failure exactly as loud as it was.
//
// launchd consumes the exit code. A sibling `dot peer sync` still holding the
// lock when the interval fires is normal operation on a laptop, not a job
// failure, and reporting it as one trains the operator to ignore the report.
// Contention is matched with errors.Is against the sentinel, never against
// the message, so an unwritable lock parent or an unreachable peer stays loud.
// The line goes through the runner's logger, which the plist routes to
// StandardOutPath, so a swallowed run is still recorded.
//
// ponytail: known ceiling. A plist installed by an older dot carries no
// marker, so its runs stay loud until the operator re-runs `dot peer
// schedule`. That is a one-time cost, stated here rather than discovered.
func quietScheduledContention(runner *exec.Runner, err error) error {
	if err == nil || !isScheduledRun() || !errors.Is(err, fileutil.ErrLockHeld) {
		return err
	}
	runner.Logger.Info("scheduled peer sync skipped: another run holds the lock", "err", err)
	return nil
}

// renderPeerEvent turns engine progress into the lines `dot peer sync` has
// always printed. One renderer for every step, so the wording cannot drift
// apart between the pull, push and host-path passes.
func renderPeerEvent(p *Printer) func(syncer.PeerEvent) {
	return func(e syncer.PeerEvent) {
		switch e.Kind {
		case syncer.PeerEventPullStart:
			p.Section("pull from peer")
		case syncer.PeerEventRemoteDeletesHeld:
			p.Warn("remote-only deletes held: peer baseline provenance is not established")
		case syncer.PeerEventPropagateDeletesStart:
			p.Section("propagate deletions")
		case syncer.PeerEventLocalDeletesHeld:
			p.Warn("local deletes held: peer baseline provenance is not established")
		case syncer.PeerEventPushStart:
			p.Section("push to peer")
		case syncer.PeerEventHostPathsStart:
			p.Section("host paths")
		case syncer.PeerEventHomeTrackedStart:
			p.Section("tracked host paths")
		case syncer.PeerEventHostPathsMissing:
			p.Warn("no host-path list at %s; skipping", e.Path)
		case syncer.PeerEventPartialTransfer:
			reportPartial(p, e.Err)
		case syncer.PeerEventPeerLacksHandover:
			p.Warn("peer dot %s predates owner epochs; fence, handover and takeover are skipped on that side", e.Path)
			p.Line("  Upgrade dot on the peer, or pin a newer binary with remote_dot in the peer config.")
		case syncer.PeerEventPeerDotUnreleased:
			p.Warn("peer dot %s is not a release build; install a release there, or pin it with remote_dot in the peer config", e.Path)
		case syncer.PeerEventDotVersionMismatch:
			p.Warn("peer dot %s is a different release from this machine's; upgrade the older side", e.Path)
		case syncer.PeerEventOwnerRenamePending:
			p.Warn("the peer still records this Mac's earlier name, so it stays an alias here; run there: %s", e.Path)
		case syncer.PeerEventHostMerged:
			p.Success("merged ~/%s from both machines (host_merge)", e.Path)
		case syncer.PeerEventHostMergeHeld:
			p.Warn("held ~/%s: host_merge merges it only in a two-way dot peer sync", e.Path)
		case syncer.PeerEventOwnerAliasesRetired:
			if e.Err != nil {
				p.Warn("retiring the earlier owner names failed (retried on the next run): %v", e.Err)
			} else {
				p.Success("both machines record the renamed owner; earlier names retired in: %s", e.Path)
			}
		case syncer.PeerEventReplicaPushFailed:
			p.Warn("replica push failed: %v", e.Err)
			p.Line("  The run itself is complete; the next unplanned switch may lack a fresh replica.")
		}
	}
}

// reportPartial annotates rsync's partial-transfer outcome. The engine has
// already classified it and returns it as a hard transaction failure; this is
// only the announcement.
func reportPartial(p *Printer, err error) {
	p.Warn("partial transfer: %v", err)
	p.Line("  Peer transaction stopped; baseline unchanged. Re-run to retry it.")
}

func newPeerSyncCmd() *cobra.Command {
	var pushOnly, pullOnly, skipHome, list, jsonOut bool
	var approve string
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Exchange workspace and host paths with the peer (both directions)",
		Args:  cobra.NoArgs,
		Long: `Build one baseline-aware plan, accept one-sided peer changes, then
publish coordinator changes. Routine updates are backup-free; only simultaneous
conflicts and propagated deletes create quarantine copies.

When propagation.delete is on, baseline-proven deletes can flow in either
direction. Unknown peer-created paths are pulled and never classified as local
deletions. The common baseline advances only after the complete workspace and
host-path transaction succeeds.

Tracked host paths (home-paths-tracked.txt) run their own baseline-aware
transaction before the additive host-path pass: deletes propagate into the
receiving side's ~/.dot-peer-conflicts quarantine and dual edits keep both
versions. Untracked host paths keep additive newest-mtime-wins behavior.

A plan with more deletions than max_delete is refused and names a digest of
its exact deletion set. After reviewing it (dot peer diff --list), approve that
set for one run with --approve-deletes <digest>; if the set changed since,
the run refuses again with the new digest. The config is never edited.

Exits 0 when the peer is unreachable. That is what makes this safe to schedule
on a laptop.`,
		RunE: func(c *cobra.Command, _ []string) (runErr error) {
			p := printerFrom(c)
			bs, err := syncer.Bootstrap(peerBootstrapOptions(c))
			if err != nil {
				return err
			}
			dryRun, _ := c.Flags().GetBool("dry-run")
			if isScheduledRun() && !dryRun {
				finish, err := startScheduledPeerLog(bs.Config.LogFile)
				if err != nil {
					bs.Runner.Logger.Warn("peer run log unavailable; output stays on launchd's redirection", "err", err)
				} else {
					defer func() { finish(runErr) }()
				}
			}
			if approve != "" {
				if !syncer.ValidDeletePlanDigest(approve) {
					return fmt.Errorf("--approve-deletes takes the digest a dry run prints, got %q", approve)
				}
				bs.Config.ApproveDeletes = approve
			}
			// A JSON document owns stdout; progress lines would corrupt it.
			progress := renderPeerEvent(p)
			if jsonOut {
				if bs.Config.Verbose {
					return fmt.Errorf("--json and --verbose both write to stdout; drop one")
				}
				// Warnings still reach the operator, on stderr.
				progress = renderPeerEvent(&Printer{Out: c.ErrOrStderr(), Err: c.ErrOrStderr()})
				bs.Config.Out = c.ErrOrStderr()
			}
			res, err := syncer.PeerSync(context.Background(), syncer.PeerSyncOptions{
				Config:   bs.Config,
				Runner:   bs.Runner,
				Probe:    probeRunner(),
				PushOnly: pushOnly,
				PullOnly: pullOnly,
				SkipHome: skipHome,
				DryRun:   dryRun,
				Progress: progress,
				Itemize:  list || jsonOut,

				LocalDotVersion: c.Root().Version,
				WhileLocked:     backgroundLease(dryRun, "peer sync"),
			})
			// Alerting observes the outcome; it never masks or replaces the
			// run's own error and exit code below.
			reportPeerSyncAlert(context.Background(), c, bs, res, err, dryRun, !pushOnly && !pullOnly)
			// Under --json stdout carries the document only: hook outcomes go
			// to stderr, like the progress lines.
			hookOut := p
			if jsonOut {
				hookOut = &Printer{Out: c.ErrOrStderr(), Err: c.ErrOrStderr()}
			}
			if err != nil {
				if res != nil && res.Demoted {
					printPeerHooks(hookOut, res.Hooks) // they ran before the failure
				}
				return quietScheduledContention(bs.Runner, err)
			}
			if jsonOut {
				if res.Demoted {
					printPeerHooks(hookOut, res.Hooks)
				}
				doc := peerPlanJSON{Unreachable: res.Unreachable, Demoted: res.Demoted, PeerRunPlan: res.Plan}
				if !res.Unreachable && !res.Demoted {
					doc.Complete = &res.Complete
				}
				return writePeerPlanJSON(c, doc)
			}
			if res.Plan != nil {
				if err := printPeerRunPlan(p, res.Plan); err != nil {
					return err
				}
			}
			if res.Unreachable {
				p.Warn("peer %s unreachable; nothing to do", bs.Config.Target.Host)
				return nil
			}
			if res.Demoted && dryRun {
				p.Warn("this machine would lose the coordinator fence: a real run adopts the peer's owner and epoch and removes its scheduler")
				printPeerHooks(p, res.Hooks)
				return nil
			}
			if res.Demoted {
				p.Warn("this machine lost the coordinator fence: it adopted the peer's owner and epoch")
				p.Line("  Its scheduler was removed and nothing was transferred.")
				printPeerHooks(p, res.Hooks)
				return nil
			}
			p.Blank()
			if res.Complete {
				p.Success("peer sync complete")
			} else {
				p.Warn("peer sync held destructive transitions; baseline unchanged")
			}
			// Deletions are quarantined, not removed; say how many, so the
			// workspace entries are not lost among the host-path ones (#182).
			verb, dir := "quarantined", ".sync-conflicts/"+res.ConflictStamp+"/"
			if dryRun {
				// A dry run creates no stamp directory.
				verb, dir = "would quarantine", ".sync-conflicts/"
			}
			if res.QuarantinedHere > 0 {
				p.Line("%s %d deletion(s) from the peer under %s here", verb, res.QuarantinedHere, dir)
			}
			if res.QuarantinedOnPeer > 0 {
				p.Line("%s %d deletion(s) from here under %s on the peer", verb, res.QuarantinedOnPeer, dir)
			}
			p.Line("Conflicting edits keep both versions; list them with:")
			p.Line("  dot sync conflicts --profile=%s", PeerProfile)
			return nil
		},
	}
	cmd.Flags().BoolVar(&pushOnly, "push-only", false, "send local changes without pulling first")
	cmd.Flags().BoolVar(&pullOnly, "pull-only", false, "receive peer changes without pushing")
	cmd.Flags().BoolVar(&skipHome, "skip-home", false, "workspace only; skip the host-path pass")
	cmd.Flags().BoolVar(&list, "list", false, "print every planned action (with --dry-run: exactly what a run would do)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the itemized plan as JSON")
	cmd.Flags().StringVar(&approve, "approve-deletes", "", "run one reviewed deletion set over max_delete, named by the digest a refused run prints")
	return cmd
}
