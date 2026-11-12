package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/remote"
)

func newRemotePruneCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "prune [--yes]",
		Short: "List, and with --yes remove, what runs left behind on the host",
		Long: `List what runs left behind on the host: mirrors whose local checkout
(remote.local_root/<path>) no longer exists, verify refs a killed verify did
not delete, and containers carrying the host's prefix that are not running.
Nothing is deleted without --yes. A mirror a run holds (its claim on the host,
or a lease on this machine) is skipped, and a removal takes the mirror's claim
itself so a run starting meanwhile defers instead of syncing into it.

Job directories 'dot remote job' kept after a failure are listed too, but
never removed: their out/ may be the only copy of what the job made.`,
		Args:         cobra.NoArgs,
		RunE:         runRemotePrune,
		SilenceUsage: true,
	}
	cmd.Flags().Bool("json", false, "print the report as JSON")
	return cmd
}

func runRemotePrune(cmd *cobra.Command, _ []string) error {
	p := printerFrom(cmd)
	cfg, host, deps, err := remoteSession(cmd, "")
	if err != nil {
		return err
	}
	yes, _ := cmd.Flags().GetBool("yes")
	asJSON, _ := cmd.Flags().GetBool("json")
	if dry, _ := cmd.Flags().GetBool("dry-run"); dry {
		// Nothing is asked of the host: the listing it would run is the answer.
		plan := struct {
			Host    string `json:"host"`
			SSH     string `json:"ssh"`
			Listing string `json:"listing"`
		}{host.Name, host.SSH, remote.PruneListScript(host)}
		if asJSON {
			data, merr := json.MarshalIndent(plan, "", "  ")
			if merr != nil {
				return merr
			}
			p.Line("%s", data)
			return nil
		}
		p.Line("would list on %s over ssh:\n%s", host.SSH, plan.Listing)
		return nil
	}
	report, err := deps.Prune(cmd.Context(), cfg, host, remote.PruneOptions{Yes: yes})
	if err != nil {
		return err
	}
	if asJSON {
		data, merr := json.MarshalIndent(report, "", "  ")
		if merr != nil {
			return merr
		}
		p.Line("%s", data)
	} else {
		p.Header("dot remote prune " + report.Host)
		if len(report.Items) == 0 {
			p.Line("nothing to remove")
		}
		for _, item := range report.Items {
			state := "would remove"
			switch {
			case item.Error != "":
				state = "could not remove (" + item.Error + ")"
			case item.Removed:
				state = "removed"
			}
			p.Line("  %s %s %s: %s", state, item.Kind, item.Name, item.Why)
		}
		for _, skip := range report.Skipped {
			p.Line("  skipped: %s", skip)
		}
		for _, job := range report.Kept {
			p.Line("  kept job (%s): %s", job.Profile, job.Dir)
		}
		if !yes && len(report.Items) > 0 {
			p.Line("run again with --yes to remove these")
		}
	}
	for _, item := range report.Items {
		if item.Error != "" {
			return fmt.Errorf("prune could not remove every item on %s", report.Host)
		}
	}
	return nil
}
