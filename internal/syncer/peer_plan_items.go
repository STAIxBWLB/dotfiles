package syncer

import (
	"context"
	"fmt"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// Scopes of a peer plan item.
const (
	PlanScopeWorkspace   = "workspace"
	PlanScopeHostTracked = "host-tracked"
	PlanScopeHost        = "host"
)

// PlanSide is one machine's copy of a planned path.
type PlanSide struct {
	Size  int64     `json:"size"`
	Mtime time.Time `json:"mtime"`
}

// PeerPlanItem is one action a peer run plans (#180). Direction is "pull"
// (peer to this machine), "push", or "both" for a host_merge file (merged on
// both machines, or a conflict that stops the run); Action is create,
// update, delete, conflict or merge. Local and Peer describe each side's
// copy where it is known: the additive host pass only sees the sending side.
type PeerPlanItem struct {
	Path      string    `json:"path"`
	Scope     string    `json:"scope"`
	Action    string    `json:"action"`
	Direction string    `json:"direction"`
	Local     *PlanSide `json:"local,omitempty"`
	Peer      *PlanSide `json:"peer,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	// Hot marks a host file both Macs rewrite on their own; Keys lists its
	// key-level differences and Warning what newest-wins would drop (#181).
	Hot     bool     `json:"hot,omitempty"`
	Keys    []string `json:"keys,omitempty"`
	Warning string   `json:"warning,omitempty"`
}

// PlanDeletes counts the deletions of one scope by direction; max_delete
// caps each direction of each scope separately.
type PlanDeletes struct {
	In  int `json:"in"`
	Out int `json:"out"`
}

// PeerRunPlan is the itemized plan of one peer run: every path the
// workspace, tracked host and additive host passes would touch.
type PeerRunPlan struct {
	Items     []PeerPlanItem         `json:"items"`
	Deletes   map[string]PlanDeletes `json:"deletes"`
	MaxDelete int                    `json:"maxDelete"`
	deletes   bool                   // propagation.delete
}

func newPeerRunPlan(cfg *Config) *PeerRunPlan {
	return &PeerRunPlan{Items: []PeerPlanItem{}, Deletes: map[string]PlanDeletes{}, MaxDelete: cfg.MaxDelete, deletes: cfg.Propagation.Delete}
}

// planRun is what one run does with a scope's plan: the directions it moves
// and what authorizes its deletions. A planned deletion the run does not
// execute is listed with the reason it is held.
type planRun struct {
	pushOnly, pullOnly bool
	authorized         bool     // the baseline is verified for this peer
	evidence           []string // this machine's deletion evidence (tombstones)
}

// addPlan flattens a three-way plan of one scope into the items run moves.
func (p *PeerRunPlan) addPlan(plan *PeerPlan, scope string, run planRun) {
	if plan == nil {
		return
	}
	side := func(snap PeerSnapshot, rel string) *PlanSide {
		if f, ok := snap[rel]; ok && f.Present {
			return &PlanSide{Size: f.FP.Size, Mtime: f.FP.Mtime}
		}
		return nil
	}
	conflicts := map[string]string{}
	for _, c := range plan.Conflicts {
		conflicts[c.RelPath] = c.Reason
	}
	item := func(rel, action, direction string) PeerPlanItem {
		it := PeerPlanItem{Path: rel, Scope: scope, Action: action, Direction: direction,
			Local: side(plan.LocalBefore, rel), Peer: side(plan.RemoteBefore, rel)}
		if reason, ok := conflicts[rel]; ok && direction == "push" {
			it.Action, it.Reason = "conflict", reason
		}
		return it
	}
	held := ""
	switch {
	case !p.deletes:
		held = "held: propagation.delete is off"
	case !run.authorized:
		held = "held: the baseline is not verified for this peer yet"
	}
	if !run.pushOnly {
		for _, rel := range plan.Pull {
			action := "update"
			if side(plan.LocalBefore, rel) == nil {
				action = "create"
			}
			p.Items = append(p.Items, item(rel, action, "pull"))
		}
		for _, rel := range plan.DeleteLocal {
			it := item(rel, "delete", "pull")
			it.Reason = held
			p.Items = append(p.Items, it)
		}
	}
	if !run.pullOnly {
		for _, rel := range plan.Push {
			action := "update"
			if side(plan.RemoteBefore, rel) == nil {
				action = "create"
			}
			p.Items = append(p.Items, item(rel, action, "push"))
		}
		for _, rel := range plan.DeleteRemote {
			it := item(rel, "delete", "push")
			reason := held
			if held == "" && !slices.Contains(run.evidence, rel) {
				reason = "held: no deletion evidence on this machine"
			}
			// A delete/edit conflict keeps its reason: the run deletes the
			// peer's edited copy (into quarantine) unless held.
			switch {
			case it.Reason == "":
				it.Reason = reason
			case reason != "":
				it.Reason += "; " + reason
			}
			p.Items = append(p.Items, it)
		}
	}
	d := p.Deletes[scope]
	d.In += len(plan.DeleteLocal)
	d.Out += len(plan.DeleteRemote)
	p.Deletes[scope] = d
}

// sortItems orders the plan by scope (workspace, tracked, host) then path.
func (p *PeerRunPlan) sortItems() {
	rank := map[string]int{PlanScopeWorkspace: 0, PlanScopeHostTracked: 1, PlanScopeHost: 2}
	sort.SliceStable(p.Items, func(i, j int) bool {
		a, b := p.Items[i], p.Items[j]
		if a.Scope != b.Scope {
			return rank[a.Scope] < rank[b.Scope]
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Direction < b.Direction
	})
}

// peerHomeAdditiveItems lists what the additive host-path pass would move,
// from the same arguments peerHomeSync runs with plus --dry-run and an
// itemized out-format. rsync decides that pass at transfer time (--update,
// newest wins), so this is what a run starting now would do. Only the
// sending side's size and mtime are known here.
func peerHomeAdditiveItems(ctx context.Context, probe *exec.Runner, cfg *Config, pushOnly, pullOnly bool) ([]PeerPlanItem, error) {
	list := PeerHomePathsFile(cfg.LocalPaths)
	if _, err := os.Stat(list); err != nil {
		return nil, nil
	}
	list, cleanup, err := peerHomeUntrackedList(cfg, list, true)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	home, remote := cfg.HomeDir()+"/", cfg.Target.Host+":"
	var items []PeerPlanItem
	var base []string
	run := func(direction, src, dst string) error {
		args := append(append([]string{}, base...), src, dst)
		res, err := probe.Run(ctx, "env", append([]string{"TZ=UTC", cfg.rsyncBin()}, args...)...)
		if err != nil {
			return fmt.Errorf("peer host plan (%s): %w", direction, err)
		}
		for _, line := range strings.Split(res.Stdout, "\n") {
			it, ok, err := parseAdditiveItem(line, direction)
			if err != nil {
				return err
			}
			if ok {
				items = append(items, it)
			}
		}
		return nil
	}
	pass := func(args []string) error {
		base = append(args, "-8", "--dry-run", "--out-format=@@%i\t%l\t%M\t%n")
		if !pushOnly {
			if err := run("pull", remote, home); err != nil {
				return err
			}
		}
		if !pullOnly {
			return run("push", home, remote)
		}
		return nil
	}
	if err := pass(peerHomeAdditiveArgs(cfg, list, false, false)); err != nil {
		return nil, err
	}
	hmList, hmCleanup, err := hostMergeList(cfg, true)
	if err != nil {
		return nil, err
	}
	if hmList != "" {
		defer hmCleanup()
		if err := pass(peerHomeAdditiveArgs(cfg, hmList, false, true)); err != nil {
			return nil, err
		}
	}
	return items, nil
}

// parseAdditiveItem reads one "@@%i\t%l\t%M\t%n" record. Only files count;
// directories and attribute-only changes are not transfers of a payload.
func parseAdditiveItem(line, direction string) (PeerPlanItem, bool, error) {
	rest, ok := strings.CutPrefix(line, "@@")
	if !ok {
		return PeerPlanItem{}, false, nil
	}
	parts := strings.SplitN(rest, "\t", 4)
	if len(parts) != 4 || len(parts[0]) < 3 {
		return PeerPlanItem{}, false, fmt.Errorf("peer host plan: malformed rsync record %q", line)
	}
	change := parts[0]
	if change[1] != 'f' {
		return PeerPlanItem{}, false, nil
	}
	size, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return PeerPlanItem{}, false, fmt.Errorf("peer host plan: malformed size in %q", line)
	}
	mtime, err := parsePeerRsyncTime(parts[2], time.UTC)
	if err != nil {
		return PeerPlanItem{}, false, fmt.Errorf("peer host plan: malformed mtime in %q", line)
	}
	action := "update"
	if strings.Trim(change[2:], "+") == "" {
		action = "create"
	}
	it := PeerPlanItem{Path: strings.TrimPrefix(parts[3], "./"), Scope: PlanScopeHost, Action: action, Direction: direction}
	sender := &PlanSide{Size: size, Mtime: mtime}
	if direction == "pull" {
		it.Peer = sender
	} else {
		it.Local = sender
	}
	return it, true, nil
}
