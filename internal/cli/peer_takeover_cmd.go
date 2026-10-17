package cli

import (
	"strconv"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/syncer"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

func newPeerTakeoverCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "takeover",
		Short: "Take the coordinator role while the other Mac is away (unplanned switch)",
		Args:  cobra.NoArgs,
		Long: `Unplanned coordinator switch, run on the Mac that becomes active while
the current coordinator is unreachable:

  1. Validate the replica the last coordinator pushed after its last
     complete run: per-file sha256, generation not older than the last one
     this store saw, and a meta target that is the reverse of this profile.
  2. Show how the replica's filter files differ from the local ones and
     require confirmation.
  3. Install the replica baselines with the target markers rewritten for
     this profile, so delete provenance carries over from the last
     complete run. max_delete is never raised.
  4. Adopt this machine as owner with the next epoch and a pending fence.

While the fence is pending, peer setup and peer sync skip the reachability
and remote-owner requirements; an unreachable run still exits 0. When the
other Mac returns, the first run settles the role by epoch: this machine's
edits win simultaneous-edit conflicts, and the returning Mac adopts the new
owner, removes its scheduler and transfers nothing.

What the replica cannot cover is lost by design: unpushed commits and
uncommitted changes the old coordinator made after its last complete run.
After the switch, fetch and realign repos with ` + "`dot peer git realign --apply`" + `.`,
		SilenceUsage: true,
		RunE: func(c *cobra.Command, _ []string) error {
			p := printerFrom(c)
			bs, err := syncer.Bootstrap(peerBootstrapOptions(c))
			if err != nil {
				return err
			}
			yes, _ := c.Flags().GetBool("yes")
			dryRun, _ := c.Flags().GetBool("dry-run")
			diffsShown := false
			showDiffs := func(res *syncer.PeerTakeoverResult) {
				p.KV("Replica generation", strconv.Itoa(res.Generation))
				if len(res.FilterDiffs) == 0 {
					p.Line("Filter files: no differences from the replica.")
				} else {
					p.Section("filter files vs replica")
					for _, d := range res.FilterDiffs {
						p.KV(d.Name, d.Status)
					}
				}
			}
			res, err := syncer.PeerTakeover(bs.Config, syncer.PeerTakeoverOptions{
				Yes:    yes,
				DryRun: dryRun,
				Confirm: func(prompt string, preview *syncer.PeerTakeoverResult) (bool, error) {
					// The differences must inform the decision, so they are
					// shown before the prompt, not after the install.
					p.Header("Peer Takeover")
					showDiffs(preview)
					p.Blank()
					diffsShown = true
					return ui.Confirm(prompt, false)
				},
			})
			if err != nil {
				return err
			}
			if res.DryRun {
				p.Header("Peer Takeover (preview)")
			} else if !diffsShown {
				p.Header("Peer Takeover")
			}
			if !diffsShown {
				showDiffs(res)
			}
			if res.DryRun {
				p.Blank()
				p.Line("Run without --dry-run to take over.")
				return nil
			}
			p.Blank()
			p.Success("this machine is now the coordinator %q (epoch %d, fence pending)", res.Owner, res.Epoch)
			p.Line("Next steps:")
			p.Line("  dot peer setup                 install the scheduler (works offline while the fence is pending)")
			p.Line("  dot peer git realign --apply   realign repos after a fetch")
			return nil
		},
	}
	return cmd
}
