package cli

import (
	"context"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/syncer"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

func newPeerHandoverCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "handover [<peer>]",
		Short: "Hand the coordinator role to the peer (planned switch)",
		Args:  cobra.MaximumNArgs(1),
		Long: `Planned coordinator switch, run on the CURRENT coordinator with both
machines reachable:

  1. Run one complete peer sync; refuse if it holds anything back.
  2. Set the owner and the next epoch on the peer, then locally.
  3. Remove the local scheduler.
  4. On the peer, set the old baselines aside and run the first sync as an
     additive bootstrap (no baseline means no deletes can be planned, and
     right after step 1 almost nothing transfers).
  5. Install the peer's scheduler; its setup runs the peer's on_activate.
  6. Run this Mac's on_deactivate hooks, last: an app-quit may end the
     process running the handover.

A lost terminal (SIGHUP) does not stop a handover once it has started. If
one stops after step 3 anyway, finish by hand: dot peer setup --off here,
then dot peer sync and dot peer setup on the peer.

The takeover replica is not used here: right after a complete run the safest
baseline is no baseline. After the switch, realign the repos on the new
coordinator with ` + "`dot peer git realign --apply`" + ` (fetch first).`,
		SilenceUsage: true,
		RunE: func(c *cobra.Command, args []string) error {
			p := printerFrom(c)
			if dry, _ := c.Flags().GetBool("dry-run"); !dry {
				// nohup: from step 2 on, a dropped ssh session must not end
				// the switch halfway, with ownership moved and this Mac's
				// on_deactivate not run.
				signal.Ignore(syscall.SIGHUP)
			}
			bs, err := syncer.Bootstrap(peerBootstrapOptions(c))
			if err != nil {
				return err
			}
			peer := ""
			if len(args) == 1 {
				peer = args[0]
			}
			dryRun, _ := c.Flags().GetBool("dry-run")
			res, err := syncer.PeerHandover(context.Background(), syncer.PeerHandoverOptions{
				Config: bs.Config,
				Runner: bs.Runner,
				Probe:  probeRunner(),
				Peer:   peer,
				DryRun: dryRun,

				LocalDotVersion: c.Root().Version,
				Warn:            func(msg string) { p.Warn("%s", msg) },
			})
			if err != nil {
				if res != nil {
					printPeerHooks(p, res.Hooks) // a demotion's hooks ran
				}
				return err
			}
			if res.DryRun {
				p.Header("Peer Handover (preview)")
			} else {
				p.Header("Peer Handover")
			}
			for _, step := range res.Steps {
				p.Bullet(ui.MarkPresent, step)
			}
			printPeerHooks(p, res.Hooks)
			for _, line := range res.RemoteHookFailures {
				p.Warn("peer %s", line)
			}
			if res.DryRun {
				p.Blank()
				p.Line("Run without --dry-run to hand over.")
				return nil
			}
			p.Blank()
			p.Success("coordinator is now %q (epoch %d)", res.NewOwner, res.Epoch)
			p.Line("On the new coordinator, fetch and realign repos with:")
			p.Line("  dot peer git realign --apply")
			return nil
		},
	}
	return cmd
}

// newPeerAdoptCmd is the plumbing handover and takeover use over ssh. It
// prints exactly one line — the adopted owner — so the invoking machine can
// record the same value without knowing the peer's name beforehand.
func newPeerAdoptCmd() *cobra.Command {
	var self bool
	var owner, epoch, replicaGeneration string
	var fencePending bool
	cmd := &cobra.Command{
		Use:          "adopt",
		Short:        "Record coordinator ownership and epoch (handover plumbing)",
		Hidden:       true,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(c *cobra.Command, _ []string) error {
			bs, err := syncer.Bootstrap(peerBootstrapOptions(c))
			if err != nil {
				return err
			}
			epochN, err := strconv.Atoi(epoch)
			if err != nil {
				return err
			}
			genN := -1
			if replicaGeneration != "" {
				if genN, err = strconv.Atoi(replicaGeneration); err != nil {
					return err
				}
			}
			dryRun, _ := c.Flags().GetBool("dry-run")
			if dryRun {
				p := printerFrom(c)
				p.Line("dry-run: would adopt owner (self=%v, %q), epoch %d, fence_pending=%v", self, owner, epochN, fencePending)
				return nil
			}
			adopted, err := syncer.PeerAdopt(bs.Config, syncer.PeerAdoptOptions{
				Self:              self,
				Owner:             owner,
				Epoch:             epochN,
				ReplicaGeneration: genN,
				FencePending:      fencePending,
			})
			if err != nil {
				return err
			}
			// The single output line is the contract peerRemoteAdopt parses.
			p := printerFrom(c)
			p.Line("%s", adopted)
			return nil
		},
	}
	cmd.Flags().BoolVar(&self, "self", false, "adopt this machine's preferred name as owner")
	cmd.Flags().StringVar(&owner, "owner", "", "explicit owner name")
	cmd.Flags().StringVar(&epoch, "epoch", "", "coordinator epoch to record (required)")
	cmd.Flags().StringVar(&replicaGeneration, "replica-generation", "", "replica generation counter to record")
	cmd.Flags().BoolVar(&fencePending, "fence-pending", false, "record a pending fence (takeover)")
	_ = cmd.MarkFlagRequired("epoch")
	return cmd
}
