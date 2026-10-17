package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/syncer"
)

// newPeerDiffCmd reports paths where the two machines disagree.
//
// This exists because "no data loss" and "no surprises" are different
// properties. Peer sync runs with --update, so when the same path was edited on
// both machines and the timestamps do not order cleanly, rsync skips it in both
// directions: nothing is destroyed, but the machines quietly stop agreeing and
// no one is told. Measured on a real round trip.
//
// The detector is a metadata-only dry run in each direction. A path that both
// sides want to send is a path where they differ. That misses two files with
// identical size and mtime but different content - rsync cannot see that without
// reading every byte, and a checksum pass over 60 GB is not something to do on a
// schedule.
func newPeerDiffCmd() *cobra.Command {
	var list, jsonOut bool
	cmd := &cobra.Command{
		Use:   "diff",
		Short: "List paths where this machine and the peer disagree",
		Long: `Count where the two machines disagree. --list prints every planned action,
workspace and host paths alike, with each side's size and mtime; --json
prints the same plan as a document. The deletion counts are shown next to
max_delete, which caps each direction of the workspace and of the tracked
host paths.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			p := printerFrom(c)
			bs, err := syncer.Bootstrap(peerBootstrapOptions(c))
			if err != nil {
				return err
			}
			res, err := syncer.PeerDiff(context.Background(), syncer.PeerDiffOptions{
				Config:  bs.Config,
				Probe:   probeRunner(),
				Itemize: list || jsonOut,
			})
			if res != nil && res.Items != nil {
				if perr := printPeerPlanOutput(c, p, res.Items, jsonOut); perr != nil {
					return perr
				}
				return err
			}
			if err != nil {
				return err
			}
			if res.Unreachable {
				if jsonOut {
					return writePeerPlanJSON(c, peerPlanJSON{Unreachable: true})
				}
				p.Warn("peer %s unreachable", bs.Config.Target.Host)
				return nil
			}
			plan := res.Plan

			p.Section("peer divergence")
			p.KV("would send", strconv.Itoa(len(plan.Push)+len(plan.DeleteRemote)))
			p.KV("would receive", strconv.Itoa(len(plan.Pull)+len(plan.DeleteLocal)))
			if !plan.HasConflicts() {
				p.Success("no path is contested")
				return nil
			}
			p.Warn("%d path(s) changed on BOTH machines:", len(plan.Conflicts))
			for i, conflict := range plan.Conflicts {
				if i >= 40 {
					p.Line("  ... and %d more", len(plan.Conflicts)-i)
					break
				}
				p.Line("  %s", conflict.RelPath)
			}
			p.Blank()
			p.Line("The profile owner is the coordinator; its version wins on the next peer sync.")
			p.Line("The losing peer payload is quarantined once under .sync-conflicts/.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&list, "list", false, "print every planned action, host paths included")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the itemized plan as JSON")
	return cmd
}

// peerPlanSchemaVersion is the `peer diff --json` / `peer sync --json`
// plan document schema; new fields are additive.
const peerPlanSchemaVersion = 1

// Complete is set by a sync that ran; false means it held destructive
// transitions. Demoted means this machine lost the fence and moved nothing.
type peerPlanJSON struct {
	SchemaVersion int    `json:"schemaVersion"`
	Kind          string `json:"kind"`
	Unreachable   bool   `json:"unreachable,omitempty"`
	Demoted       bool   `json:"demoted,omitempty"`
	Complete      *bool  `json:"complete,omitempty"`
	*syncer.PeerRunPlan
}

func writePeerPlanJSON(c *cobra.Command, doc peerPlanJSON) error {
	doc.SchemaVersion, doc.Kind = peerPlanSchemaVersion, "peer-plan"
	enc := json.NewEncoder(c.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}

// printPeerPlanOutput writes the plan as the JSON document or as the list,
// never both: stdout carries one of them.
func printPeerPlanOutput(c *cobra.Command, p *Printer, plan *syncer.PeerRunPlan, jsonOut bool) error {
	if jsonOut {
		return writePeerPlanJSON(c, peerPlanJSON{PeerRunPlan: plan})
	}
	return printPeerRunPlan(p, plan)
}

// printPeerRunPlan renders the itemized plan: one line per action, grouped by
// scope, then the deletion counts next to max_delete.
func printPeerRunPlan(p *Printer, plan *syncer.PeerRunPlan) error {
	p.Section(fmt.Sprintf("peer plan (%d action(s))", len(plan.Items)))
	scope := ""
	for _, it := range plan.Items {
		if it.Scope != scope {
			scope = it.Scope
			p.Line("  %s", scope)
		}
		line := fmt.Sprintf("    %-4s %-8s %s", it.Direction, it.Action, it.Path)
		if side := planSide("here", it.Local) + planSide("peer", it.Peer); side != "" {
			line += "  [" + strings.TrimSuffix(side, "; ") + "]"
		}
		if it.Hot {
			line += "  [hot]"
		}
		if it.Reason != "" {
			line += "  (" + it.Reason + ")"
		}
		p.Line("%s", line)
		for _, k := range it.Keys {
			p.Line("        %s", k)
		}
		if it.Warning != "" {
			p.Warn("        %s", it.Warning)
		}
	}
	var parts []string
	for _, sc := range []string{syncer.PlanScopeWorkspace, syncer.PlanScopeHostTracked} {
		if d, ok := plan.Deletes[sc]; ok {
			parts = append(parts, fmt.Sprintf("%s in %d / out %d", sc, d.In, d.Out))
		}
	}
	if len(parts) > 0 {
		limit := "no max_delete"
		if plan.MaxDelete > 0 {
			limit = fmt.Sprintf("max_delete %d per direction", plan.MaxDelete)
		}
		p.KV("deletions", strings.Join(parts, ", ")+" ("+limit+")")
	}
	return nil
}

func planSide(label string, side *syncer.PlanSide) string {
	if side == nil {
		return ""
	}
	return fmt.Sprintf("%s %d B %s; ", label, side.Size, side.Mtime.Local().Format("2006-01-02 15:04"))
}
