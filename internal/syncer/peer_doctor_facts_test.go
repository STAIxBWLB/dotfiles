package syncer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/text/unicode/norm"
)

func doctorFacts() (*PeerSideFacts, *PeerSideFacts) {
	base := func() *PeerSideFacts {
		return &PeerSideFacts{RsyncPath: "/opt/homebrew/bin/rsync", RsyncVersion: "rsync  version 3.4.4", Owner: "m5x26", OwnerEpoch: 2,
			MaxDelete: 100, Propagation: PropagationPolicy{Create: true, Update: true, Delete: true}, Filters: map[string]string{"exclude.txt": "a"},
			CoordConfig: &CoordinatorConfig{HostMerge: map[string][]string{".claude.json": {"mcpServers", "projects"}}, FilterMode: "exclude"}}
	}
	local, peer := base(), base()
	local.Coordinator, local.Scheduler = true, true
	peer.Replica = &PeerReplicaFacts{Generation: 7, Coordinator: "m5x26"}
	return local, peer
}

func checkFor(checks []DoctorCheck, name, level string) *DoctorCheck {
	for i := range checks {
		if checks[i].Name == name && checks[i].Level == level {
			return &checks[i]
		}
	}
	return nil
}

func TestEvaluatePeerSides(t *testing.T) {
	local, peer := doctorFacts()
	checks := evaluatePeerSides(local, peer, "m5x26", "m3x23")
	for _, c := range checks {
		if c.Level != DoctorPass {
			t.Fatalf("a healthy pair reported %+v", c)
		}
	}

	local, peer = doctorFacts()
	peer.Coordinator, peer.Scheduler = true, true
	// The reunion: the marked coordinator's inventory stops on the peer's names.
	local.NFDMarked, local.NonNFD, local.NonNFDSample = true, 1, []string{"a.md"}
	peer.NonNFD, peer.NonNFDSample = 16, []string{"sites/x/한글.md"}
	peer.MaxDelete, peer.Filters = 2000, map[string]string{"exclude.txt": "b", "allow.txt": "c"}
	peer.RsyncError = "local rsync is openrsync or 2.x"
	peer.Replica = nil
	checks = evaluatePeerSides(local, peer, "m5x26", "m3x23")
	for _, want := range []struct{ name, level, fix string }{
		{"roles", DoctorFail, "dot peer adopt --owner <a name only the chosen Mac answers to> --epoch 3"},
		{"nfd", DoctorFail, "on m3x23: dot sync names normalize --profile=peer"},
		{"nfd", DoctorWarn, "on m3x23: dot sync names normalize --profile=peer"},
		{"rsync", DoctorFail, "on m3x23: brew install rsync"},
		{"config", DoctorWarn, ""},
	} {
		found := false
		for _, c := range checks {
			found = found || c.Name == want.name && c.Level == want.level && strings.Contains(c.Fix, want.fix)
		}
		if !found {
			t.Errorf("no %s/%s check with fix %q in %+v", want.name, want.level, want.fix, checks)
		}
	}

	// Under an unmarked coordinator the other Mac's names are pulled as they
	// are and refused by its next sync, whatever the other Mac's marker.
	local, peer = doctorFacts()
	peer.NFDMarked, peer.NonNFD = true, 3
	checks = evaluatePeerSides(local, peer, "m5x26", "m3x23")
	if c := checkFor(checks, "nfd", DoctorFail); c == nil || c.Fix != "on m3x23: dot sync names normalize --profile=peer --yes" {
		t.Errorf("the other Mac's names under an unmarked coordinator: %+v", c)
	}

	// A takeover's pending fence is the fence's to settle, not two
	// coordinators: the lower epoch demotes at its next run.
	local, peer = doctorFacts()
	local.MachineNames = []string{"m5x26"}
	peer.Coordinator, peer.Scheduler, peer.OwnerEpoch, peer.FencePending = true, true, 3, true
	peer.Owner, peer.MachineNames = "m3x23", []string{"m3x23"}
	checks = evaluatePeerSides(local, peer, "m5x26", "m3x23")
	if c := checkFor(checks, "roles", DoctorFail); c != nil {
		t.Errorf("a pending fence failed: %+v", c)
	}
	if c := checkFor(checks, "roles", DoctorWarn); c == nil || c.Fix != "on m5x26: dot peer sync (its fence demotes it)" {
		t.Errorf("pending fence not explained: %+v", checks)
	}

	// Owners the fence compares and refuses: a stale name at the same epoch,
	// or an empty one on a peer without an epoch.
	for _, tc := range []struct {
		owner string
		epoch int
	}{{"m5x26-old", 2}, {"", 0}} {
		local, peer = doctorFacts()
		peer.Owner, peer.OwnerEpoch = tc.owner, tc.epoch
		c := checkFor(evaluatePeerSides(local, peer, "m5x26", "m3x23"), "roles", DoctorFail)
		if c == nil || c.Fix != "on m3x23: dot peer adopt --owner 'm5x26' --epoch 2" {
			t.Errorf("owner %q epoch %d: fence refusal not flagged: %+v", tc.owner, tc.epoch, c)
		}
	}

	// The coordinator's own push preflight (nfdPushRefusal) and plan errors
	// stop its sync: the doctor fails where the sync stops.
	for _, tc := range []struct {
		name   string
		mutate func(local, peer *PeerSideFacts)
		fix    string
	}{
		{"unmarked coordinator with names", func(l, _ *PeerSideFacts) { l.NonNFD = 16 }, "on m5x26: dot sync names normalize --profile=peer --yes"},
		{"coordinator cannot plan", func(l, _ *PeerSideFacts) { l.NonNFDError = "permission denied" }, ""},
		{"marked coordinator, peer cannot plan", func(l, p *PeerSideFacts) { l.NFDMarked, p.NFDMarked, p.NonNFDError = true, true, "collision" }, ""},
	} {
		local, peer = doctorFacts()
		tc.mutate(local, peer)
		c := checkFor(evaluatePeerSides(local, peer, "m5x26", "m3x23"), "nfd", DoctorFail)
		if c == nil || c.Fix != tc.fix {
			t.Errorf("%s: %+v", tc.name, c)
		}
	}

	// The fence's topology check comes first: a peer profile that does not
	// point back here refuses every sync.
	local, peer = doctorFacts()
	local.WorkspacePath, local.TargetPath, local.TargetHost = "/Users/a/work", "/Users/b/work", "m3x23.ts.net"
	peer.WorkspacePath, peer.TargetPath, peer.TargetHost = "/Users/b/work", "/Users/a/elsewhere", "m5x26.ts.net"
	// The fix names the Mac that is wrong and the target, and leaves the
	// owner and epoch alone (dot peer init would claim the owner).
	if c := checkFor(evaluatePeerSides(local, peer, "m5x26", "m3x23"), "topology", DoctorFail); c == nil || !strings.Contains(c.Detail, "does not point back") || c.Fix != "on m3x23: dot sync target --profile=peer ssh:m5x26.ts.net:/Users/a/work" {
		t.Errorf("topology mismatch: %+v", c)
	}
	// Also with both Macs coordinating after a takeover: the fence refuses
	// on both sides before any demotion.
	peer.Coordinator, peer.OwnerEpoch = true, 3
	if c := checkFor(evaluatePeerSides(local, peer, "m5x26", "m3x23"), "topology", DoctorFail); c == nil || !strings.Contains(c.Detail, "does not point back") {
		t.Errorf("topology mismatch with two coordinators passed: %+v", c)
	}
	// The topology row stands on its own: with no coordinator, and next to
	// the roles row it does not hide.
	for _, tc := range []struct {
		name   string
		mutate func(l, p *PeerSideFacts)
	}{
		{"neither", func(l, p *PeerSideFacts) { l.Coordinator, p.Coordinator, p.OwnerEpoch = false, false, 2 }},
		{"two at one epoch", func(l, p *PeerSideFacts) { p.Coordinator, p.OwnerEpoch = true, 2 }},
	} {
		local, peer := doctorFacts()
		local.WorkspacePath, local.TargetPath = "/Users/a/work", "/Users/b/work"
		peer.WorkspacePath, peer.TargetPath = "/Users/b/work", "/Users/a/elsewhere"
		tc.mutate(local, peer)
		checks := evaluatePeerSides(local, peer, "m5x26", "m3x23")
		if checkFor(checks, "topology", DoctorFail) == nil || checkFor(checks, "roles", DoctorFail) == nil {
			t.Errorf("%s: topology or roles row missing: %+v", tc.name, checks)
		}
	}

	// The other Mac holds a higher epoch: the coordinator's next sync
	// demotes it. It stays the owner when it answers to the recorded name,
	// else no coordinator is left.
	for _, tc := range []struct {
		owner, level string
	}{{"m5x26", DoctorWarn}, {"old-name", DoctorFail}} {
		local, peer = doctorFacts()
		local.MachineNames = []string{"m5x26"}
		peer.Owner, peer.OwnerEpoch = tc.owner, 3
		checks := evaluatePeerSides(local, peer, "m5x26", "m3x23")
		c := checkFor(checks, "roles", tc.level)
		if c == nil || c.Fix != "on m3x23: dot peer adopt --owner 'm5x26' --epoch 2" || !strings.Contains(c.Detail, "demotes") {
			t.Errorf("peer owner %q at a higher epoch: %+v", tc.owner, c)
		}
		// The other rows judge the state that fix leaves: m5x26 stays the
		// coordinator, so its scheduler stays and it needs no replica.
		for _, c := range checks {
			if (c.Name == "scheduler" || c.Name == "replica") && c.Level != DoctorPass {
				t.Errorf("peer owner %q at a higher epoch contradicts the roles fix: %+v", tc.owner, c)
			}
		}
	}

	// A coordinator the fence demotes still runs that sync, which checks
	// rsync before the fence: without rsync 3.x it never gets demoted.
	local, peer = doctorFacts()
	local.OwnerEpoch = 3
	peer.Coordinator, peer.Scheduler, peer.RsyncError = true, true, "openrsync"
	if c := checkFor(evaluatePeerSides(local, peer, "m5x26", "m3x23"), "rsync", DoctorFail); c == nil {
		t.Errorf("the demoting coordinator's missing rsync passed")
	}

	// A takeover from a coordinator that never had an epoch: the higher
	// side's fence refuses until the lower one's sync demotes it.
	local, peer = doctorFacts()
	local.OwnerEpoch, local.FencePending = 1, true
	peer.Coordinator, peer.Scheduler, peer.Owner, peer.OwnerEpoch = true, true, "m3x23", 0
	if c := checkFor(evaluatePeerSides(local, peer, "m5x26", "m3x23"), "roles", DoctorWarn); c == nil || !strings.Contains(c.Detail, "refused until m3x23's next sync") || c.Fix != "on m3x23: dot peer sync (its fence demotes it)" {
		t.Errorf("the higher side's refusal went unsaid: %+v", c)
	}
	// The coordinator holds the higher epoch: it proceeds; the other Mac is
	// told to catch up, since it never syncs.
	local, peer = doctorFacts()
	local.OwnerEpoch = 3
	checks = evaluatePeerSides(local, peer, "m5x26", "m3x23")
	if checkFor(checks, "roles", DoctorPass) == nil || checkFor(checks, "roles", DoctorWarn) == nil || checkFor(checks, "roles", DoctorFail) != nil {
		t.Errorf("coordinator ahead: %+v", checks)
	}

	// No coordinator: no sync runs. The other Mac's rsync client only
	// matters once it coordinates.
	local, peer = doctorFacts()
	local.Coordinator, peer.RsyncError = false, "openrsync"
	checks = evaluatePeerSides(local, peer, "m5x26", "m3x23")
	if checkFor(checks, "roles", DoctorFail) == nil || checkFor(checks, "rsync", DoctorWarn) == nil || checkFor(checks, "rsync", DoctorFail) != nil {
		t.Errorf("no coordinator / peer rsync: %+v", checks)
	}

	// A replica the takeover would refuse, or one the peer staged itself.
	local, peer = doctorFacts()
	peer.Replica, peer.ReplicaError = nil, "peer replica: exclude.txt sha256 mismatch"
	if c := checkFor(evaluatePeerSides(local, peer, "m5x26", "m3x23"), "replica", DoctorWarn); c == nil || !strings.Contains(c.Detail, "sha256 mismatch") {
		t.Errorf("refused replica passed")
	}
	local, peer = doctorFacts()
	peer.MachineNames, peer.Replica.Coordinator = []string{"m3x23"}, "m3x23"
	if c := checkFor(evaluatePeerSides(local, peer, "m5x26", "m3x23"), "replica", DoctorWarn); c == nil {
		t.Errorf("self-staged replica passed")
	}

	local, peer = doctorFacts()
	local.Scheduler, peer.Scheduler, peer.Replica = false, true, nil
	checks = evaluatePeerSides(local, peer, "m5x26", "m3x23")
	if c := checkFor(checks, "scheduler", DoctorWarn); c == nil || !strings.Contains(c.Fix, "on m5x26: dot peer setup") {
		t.Errorf("missing coordinator scheduler not flagged: %+v", checks)
	}
	if c := checkFor(checks, "replica", DoctorWarn); c == nil || !strings.Contains(c.Detail, "m3x23") {
		t.Errorf("missing replica not flagged: %+v", checks)
	}
}

// Every row judges the state the roles fix leaves: across coordinator
// shapes no host is told to remove its scheduler and to install it or adopt
// the role, and a Mac the fence demotes is not told to set up.
func TestEvaluatePeerSidesFixesDoNotContradict(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(l, p *PeerSideFacts)
		never  []string
		want   string
	}{
		{"one coordinator", func(l, p *PeerSideFacts) {}, nil, ""},
		{"neither, this Mac keeps its plist", func(l, p *PeerSideFacts) { l.Coordinator = false }, []string{"on m5x26: dot peer setup --off"}, ""},
		{"two, the lower without a plist", func(l, p *PeerSideFacts) {
			l.Scheduler, l.MachineNames = false, []string{"m5x26"}
			p.Coordinator, p.Scheduler, p.OwnerEpoch, p.Owner, p.MachineNames = true, true, 3, "m3x23", []string{"m3x23"}
		}, []string{"on m5x26: dot peer setup"}, ""},
		{"two, the higher without a plist", func(l, p *PeerSideFacts) {
			l.OwnerEpoch, l.Scheduler, l.MachineNames = 3, false, []string{"m5x26"}
			p.Coordinator, p.Scheduler, p.Owner, p.MachineNames = true, true, "m3x23", []string{"m3x23"}
		}, []string{"on m3x23: dot peer setup --off"}, "after the fixes above, on m5x26: dot peer setup"},
		{"two at one epoch", func(l, p *PeerSideFacts) { p.Coordinator, p.Scheduler = true, true }, nil, ""},
		// Undecided: one line, no host told to set up.
		{"two at one epoch, no plists", func(l, p *PeerSideFacts) {
			l.Scheduler = false
			p.Coordinator = true
		}, []string{"on m5x26: dot peer setup", "on m3x23: dot peer setup"}, "dot peer setup on the Mac you choose"},
		// A name both answer to: aligning or adopting would make two.
		{"sole coordinator, the peer answers to its owner", func(l, p *PeerSideFacts) {
			p.MachineNames = []string{"m3x23", "m5x26"}
			p.Owner, p.OwnerEpoch = "", 0
		}, []string{"adopt --owner m5x26"}, "a name only the chosen Mac answers to"},
		{"neither, the peer answers to this Mac's name", func(l, p *PeerSideFacts) {
			l.Coordinator, l.PreferredName = false, "m5x26"
			p.MachineNames = []string{"m5x26"}
		}, []string{"adopt --self"}, "a name only the chosen Mac answers to"},
		// A renamed coordinator (#193 round 12): only the Mac with the peer
		// scheduler can run the rename, so it is the one to coordinate.
		{"neither, renamed, the peer keeps the plist", func(l, p *PeerSideFacts) {
			l.Coordinator, l.Scheduler, l.Owner = false, false, "old-name"
			p.Scheduler, p.Owner, p.PreferredName = true, "old-name", "m3x23"
		}, []string{"on m5x26: dot sync owner --rename", "on m5x26: dot peer adopt --self"}, "on m3x23: dot sync owner --rename 'old-name' <its name now>"},
		{"neither, renamed, no plists", func(l, p *PeerSideFacts) {
			l.Coordinator, l.Scheduler, l.Owner = false, false, "old-name"
			p.Owner = "old-name"
		}, []string{"on m5x26: dot peer setup", "on m3x23: dot peer setup"}, "--local-only"},
		{"sole coordinator demoted to no one", func(l, p *PeerSideFacts) {
			l.MachineNames = []string{"m5x26"}
			p.Owner, p.OwnerEpoch = "old-name", 3
		}, []string{"on m5x26: dot peer setup --off"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			local, peer := doctorFacts()
			tc.mutate(local, peer)
			var fixes []string
			for _, c := range evaluatePeerSides(local, peer, "m5x26", "m3x23") {
				if c.Fix != "" {
					fixes = append(fixes, c.Fix)
				}
			}
			all := strings.Join(fixes, "\n")
			for _, host := range []string{"m5x26", "m3x23"} {
				off := strings.Contains(all, "on "+host+": dot peer setup --off")
				on := strings.Contains(strings.ReplaceAll(all, "setup --off", ""), "on "+host+": dot peer setup")
				takes := strings.Contains(all, "on "+host+": dot peer adopt --self") || strings.Contains(all, "on "+host+": dot sync owner --rename")
				if off && (on || takes) {
					t.Errorf("%s is told both ways:\n%s", host, all)
				}
			}
			if !strings.Contains(all, tc.want) {
				t.Errorf("no fix %q:\n%s", tc.want, all)
			}
			for _, never := range tc.never {
				if strings.Contains(strings.ReplaceAll(all, never+" --off", ""), never) {
					t.Errorf("fix %q given:\n%s", never, all)
				}
			}
		})
	}

	// The "choose" fix, applied for either choice, leaves a pair whose
	// fence settles on the chosen Mac: one owner at one new epoch.
	local, peer := doctorFacts()
	local.MachineNames = []string{"m5x26", "mac"}
	peer.Coordinator, peer.OwnerEpoch, peer.Owner, peer.MachineNames = true, 3, "mac", []string{"m3x23", "mac"}
	c := checkFor(evaluatePeerSides(local, peer, "m5x26", "m3x23"), "roles", DoctorFail)
	var epoch int
	if c == nil || !strings.Contains(c.Fix, "dot peer adopt --owner") {
		t.Fatalf("shared-name takeover: %+v", c)
	}
	if _, err := fmt.Sscanf(c.Fix[strings.Index(c.Fix, "--epoch ")+len("--epoch "):], "%d", &epoch); err != nil || epoch != 4 {
		t.Fatalf("fix %q: epoch %d, %v", c.Fix, epoch, err)
	}
	for _, chosen := range []string{"m5x26", "m3x23"} {
		coord := fenceSide{Owner: chosen, Epoch: epoch}
		other := fenceSide{Owner: chosen, Epoch: epoch} // it does not answer to the chosen name
		if demote, err := fenceDecision(coord, other); demote || err != nil {
			t.Errorf("chose %s: demote %v, err %v", chosen, demote, err)
		}
	}

	// Mid-rename (#191): the coordinator records the new name with the old
	// one as an alias, the other Mac still the old name, at one epoch. The
	// fence proceeds through the alias, so the doctor passes too.
	local, peer = doctorFacts()
	local.OwnerAliases, peer.Owner = []string{"old-m5"}, "old-m5"
	if checks := evaluatePeerSides(local, peer, "m5x26", "m3x23"); checkFor(checks, "roles", DoctorFail) != nil || checkFor(checks, "roles", DoctorPass) == nil {
		t.Errorf("a half-migrated rename fails the roles row: %+v", checks)
	} else if c := checkFor(checks, "roles", DoctorWarn); c == nil || c.Fix != "on m3x23: dot sync owner --rename 'old-m5' 'm5x26' --local-only" {
		// The sync names the pending step on every run; so does the doctor.
		t.Errorf("the pending rename step is not named: %+v", checks)
	}

	// A demotion to the owner a Mac already records keeps its aliases, as
	// PeerAdopt does, so it stays the coordinator through one of them.
	local, peer = doctorFacts()
	local.Owner, local.OwnerAliases, local.MachineNames = "new-a", []string{"m5x26"}, []string{"m5x26"}
	peer.Owner, peer.OwnerEpoch, peer.MachineNames = "new-a", 3, []string{"m3x23"}
	if checks := evaluatePeerSides(local, peer, "m5x26", "m3x23"); checkFor(checks, "roles", DoctorFail) != nil ||
		checkFor(checks, "roles", DoctorWarn) == nil || !strings.Contains(checkFor(checks, "roles", DoctorWarn).Detail, "stays the owner") {
		t.Errorf("an alias kept through the demotion: %+v", checks)
	}

	// A higher epoch with no owner: the coordinator's fence refuses, as the
	// sync does (#202), and the align fix restores its owner there.
	local, peer = doctorFacts()
	local.MachineNames = []string{"m5x26"}
	peer.Owner, peer.OwnerEpoch = "", 3
	if c := checkFor(evaluatePeerSides(local, peer, "m5x26", "m3x23"), "roles", DoctorFail); c == nil || !strings.Contains(c.Detail, "next sync is refused") || !strings.Contains(c.Detail, "no owner") ||
		!strings.HasSuffix(c.Fix, "dot peer adopt --owner 'm5x26' --epoch 2") || !strings.Contains(c.Detail, "dot peer adopt --owner 'm5x26' --epoch 2") {
		t.Errorf("demotion to no owner: %+v", c)
	}

	// Neither answers to the one owner both record: a renamed coordinator,
	// so the fix names the rename (it keeps epoch and baselines) first.
	local, peer = doctorFacts()
	local.Coordinator, local.Owner, peer.Owner = false, "old-name", "old-name"
	if c := checkFor(evaluatePeerSides(local, peer, "m5x26", "m3x23"), "roles", DoctorFail); c == nil || !strings.Contains(c.Fix, "dot sync owner --rename 'old-name' <its name now>") {
		t.Errorf("renamed coordinator: %+v", c)
	}

	// After an offline rename and a second host rename the two Macs record
	// different owners, one the other's alias: each renames the owner it
	// records, which RenameOwner accepts (#193 round 13). The Mac the fix
	// makes the coordinator needs rsync 3.x like one that coordinates now.
	local, peer = doctorFacts()
	local.Coordinator, local.Scheduler, local.Owner, local.MachineNames = false, false, "a", []string{"m5x26"}
	peer.Scheduler, peer.Owner, peer.OwnerAliases, peer.MachineNames = true, "b", []string{"a"}, []string{"m3x23"}
	peer.RsyncError = "openrsync is not rsync 3.x"
	checks := evaluatePeerSides(local, peer, "m5x26", "m3x23")
	if c := checkFor(checks, "roles", DoctorFail); c == nil || !strings.Contains(c.Fix, `if m3x23 was "b": on m3x23: dot sync owner --rename 'b' <its name now>`) {
		t.Errorf("rename of the owner the coordinator records: %+v", c)
	}
	if checkFor(checks, "rsync", DoctorFail) == nil {
		t.Errorf("the settled coordinator's missing rsync is not a failure: %+v", checks)
	}
	peer.Scheduler = false
	if c := checkFor(evaluatePeerSides(local, peer, "m5x26", "m3x23"), "roles", DoctorFail); c == nil || !strings.Contains(c.Fix, "<the owner it records: 'a' here, 'b' on m3x23>") {
		t.Errorf("undecided rename with two recorded owners: %+v", c)
	}

	// A Mac judged as the coordinator after the roles fix is counted as its
	// own sync walks it (without linked worktrees).
	local, peer = doctorFacts()
	local.Coordinator, local.NonNFD, local.NonNFDSample = false, 1, []string{"wt/한글.md"}
	local.CoordNonNFD = &NFDCount{}
	if c := checkFor(evaluatePeerSides(local, peer, "m5x26", "m3x23"), "nfd", DoctorFail); c != nil {
		t.Errorf("a linked worktree's name failed the settled coordinator: %+v", c)
	}

	// The lower Mac answers to the higher one's owner: its demotion leaves
	// two writers at one epoch, which the fence lets through.
	local, peer = doctorFacts()
	local.MachineNames = []string{"m5x26"}
	peer.Coordinator, peer.Scheduler, peer.OwnerEpoch = true, true, 3
	if c := checkFor(evaluatePeerSides(local, peer, "m5x26", "m3x23"), "roles", DoctorFail); c == nil || !strings.Contains(c.Detail, "leaves two coordinators") {
		t.Errorf("a shared owner name passed: %+v", c)
	}
}

// #182 AC1: the doctor reads the peer's facts over ssh and compares them.
func TestPeerDoctor_ComparesBothMachines(t *testing.T) {
	cfg, _ := peerDryRunSandbox(t)
	_, peer := doctorFacts()
	peer.Coordinator, peer.Scheduler, peer.OwnerEpoch = true, true, cfg.OwnerEpoch
	doc, err := json.Marshal(peer)
	if err != nil {
		t.Fatal(err)
	}
	dot := filepath.Join(t.TempDir(), "dot")
	writeStub(t, dot, "#!/bin/sh\n"+
		"[ \"$1\" = --version ] && { echo 'dot version 9.9.9 (fake)'; exit 0; }\n"+
		"[ \"$*\" = 'peer doctor --self' ] && { printf '%s\\n' '"+string(doc)+"'; exit 0; }\n"+
		"exit 1\n")
	useRemoteDotCandidates(t, dot)
	report, err := PeerDoctor(context.Background(), PeerDoctorOptions{Config: cfg, Probe: peerScheduleRunner(false), LocalDotVersion: "9.9.9 (fake)"})
	if err != nil {
		t.Fatal(err)
	}
	if report.Peer == nil || report.Local == nil {
		t.Fatalf("facts missing: peer %v (%v), local %v", report.Peer, report.PeerFactsErr, report.Local)
	}
	if report.Local.Coordinator && checkFor(report.Checks, "roles", DoctorFail) == nil {
		t.Fatalf("two coordinators not flagged: %+v", report.Checks)
	}
	if report.Problems == 0 {
		t.Fatal("a failing check did not count as a problem")
	}
}

// #182 AC2: the NFD inventory error names the host and the exact command.
func TestPeerInventoryNFDErrorNamesHostAndCommand(t *testing.T) {
	requirePeerRsync(t)
	cfg, _ := peerDryRunSandbox(t)
	if err := MarkNFDMigration(cfg.LocalPaths.WorkspaceRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Target.Path, norm.NFC.String("한글.md")), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := PeerDiff(context.Background(), PeerDiffOptions{Config: cfg, Probe: peerScheduleRunner(false)})
	if err == nil || !strings.Contains(err.Error(), "on fake-peer; run there: dot sync names normalize --profile=peer") {
		t.Fatalf("err = %v", err)
	}
}

// The sync summary counts the deletions it quarantined (issue comment).
func TestPeerSyncCountsQuarantinedDeletions(t *testing.T) {
	sb := newPeerHandoverSandbox(t, peerStatusFields{epoch: 1, dotVersion: "9.9.9 (fake)"}, 1)
	gone := filepath.Join(sb.local, "gone-on-peer.txt")
	if err := os.WriteFile(gone, []byte("was on both\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fp, err := FingerprintFile(gone, FingerprintFast)
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveBaselineManifest(sb.paths.BaselineFile, map[string]Fingerprint{"gone-on-peer.txt": fp}); err != nil {
		t.Fatal(err)
	}
	if err := markPeerBaselineTarget(sb.cfg); err != nil {
		t.Fatal(err)
	}
	res, err := PeerSync(context.Background(), PeerSyncOptions{Config: sb.cfg, Runner: peerScheduleRunner(false), Probe: peerScheduleRunner(false), SkipHome: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.QuarantinedHere != 1 || res.QuarantinedOnPeer != 0 || res.ConflictStamp == "" {
		t.Fatalf("result = %+v, want one deletion quarantined here", res)
	}
}

// Parity (#182): the doctor's NFD verdict is FAIL exactly when a real peer
// diff, or one of two real syncs in a row, stops, for each coordinator
// marker and each Mac holding a name not in NFD. Real rsync, fake ssh.
func TestDoctorNFDVerdictMatchesTheSync(t *testing.T) {
	requirePeerRsync(t)
	nfc := norm.NFC.String("한글.md")
	// The other Mac is a plain peer, or a coordinator the fence will demote
	// (a takeover's reunion: it still passes its own owner guard at a lower
	// epoch, NFD-marked). Its names are judged against the Mac that syncs.
	for _, demoted := range []bool{false, true} {
		for _, marked := range []bool{false, true} {
			for _, where := range []string{"none", "coordinator", "other"} {
				t.Run(fmt.Sprintf("otherDemoted=%v marked=%v names=%s", demoted, marked, where), func(t *testing.T) {
					fields, localEpoch := peerStatusFields{epoch: 1, dotVersion: "9.9.9 (fake)"}, 1
					if demoted {
						fields.owner, localEpoch = "old-coordinator", 2
					}
					sb := newPeerHandoverSandbox(t, fields, localEpoch)
					if marked {
						if err := MarkNFDMigration(sb.local); err != nil {
							t.Fatal(err)
						}
					}
					other := &PeerSideFacts{Owner: sb.owner, OwnerEpoch: 1}
					if demoted {
						other = &PeerSideFacts{Owner: "old-coordinator", OwnerEpoch: 1, Coordinator: true, NFDMarked: true}
					}
					switch where {
					case "coordinator":
						if err := os.WriteFile(filepath.Join(sb.local, nfc), []byte("x"), 0o644); err != nil {
							t.Fatal(err)
						}
					case "other":
						if err := os.WriteFile(filepath.Join(sb.peer, nfc), []byte("x"), 0o644); err != nil {
							t.Fatal(err)
						}
						other.NonNFD, other.NonNFDSample = 1, []string{nfc}
					}
					facts := LocalPeerSideFacts(context.Background(), peerScheduleRunner(false), sb.cfg, "9.9.9")
					if !facts.Coordinator {
						t.Fatal("the sandbox Mac is not the coordinator")
					}
					doctorFails := false
					for _, c := range evaluatePeerSides(facts, other, "here", "there") {
						doctorFails = doctorFails || c.Name == "nfd" && c.Level == DoctorFail
					}

					_, diffErr := PeerDiff(context.Background(), PeerDiffOptions{Config: sb.cfg, Probe: peerScheduleRunner(false)})
					stops := diffErr != nil
					for run := 1; run <= 2 && !stops; run++ {
						_, err := PeerSync(context.Background(), PeerSyncOptions{Config: sb.cfg, Runner: peerScheduleRunner(false), Probe: peerScheduleRunner(false), SkipHome: true})
						stops = err != nil
					}
					if doctorFails != stops {
						t.Fatalf("doctor fails %v, sync stops %v (diff: %v)", doctorFails, stops, diffErr)
					}
				})
			}
		}
	}
}

// #203: the rest of the coordinator-only config is compared, each side's
// host_merge verdict is judged as the roles fix leaves it, and a peer whose
// dot predates the report is told to upgrade rather than compared.
func TestEvaluatePeerSidesComparesCoordinatorConfig(t *testing.T) {
	local, peer := doctorFacts()
	peer.CoordConfig.HostMerge = map[string][]string{".claude.json": {"projects", "mcpServers"}} // same set, other order
	if c := checkFor(evaluatePeerSides(local, peer, "m5x26", "m3x23"), "config", DoctorPass); c == nil {
		t.Errorf("host_merge keys in another order counted as a difference")
	}

	local, peer = doctorFacts()
	peer.CoordConfig.IncludeSubmodules, peer.CoordConfig.FilterMode, peer.CoordConfig.HostMerge = true, "include", nil
	c := checkFor(evaluatePeerSides(local, peer, "m5x26", "m3x23"), "config", DoctorWarn)
	if c == nil || c.Detail != "host_merge, include_submodules, filter_mode differ between the Macs; the coordinator's copy decides its peer sync" {
		t.Errorf("differing keys: %+v", c)
	}

	// The coordinator's verdict fails; the other Mac's warns.
	local, peer = doctorFacts()
	local.CoordConfig.HostMergeError, peer.CoordConfig.HostMergeError = "host_merge .claude.json: a symlink on m3x23", "host_merge: bad"
	checks := evaluatePeerSides(local, peer, "m5x26", "m3x23")
	if c := checkFor(checks, "config", DoctorFail); c == nil || !strings.Contains(c.Detail, "m5x26's peer sync stops over host_merge: host_merge .claude.json: a symlink on m3x23") {
		t.Errorf("coordinator's host_merge verdict: %+v", checks)
	}
	if c := checkFor(checks, "config", DoctorWarn); c == nil || !strings.Contains(c.Detail, "m3x23's peer sync would stop over host_merge once it coordinates") {
		t.Errorf("other Mac's host_merge verdict: %+v", checks)
	}

	// An older peer dot: told to upgrade, and this Mac's own verdict still
	// fails the coordinator.
	local, peer = doctorFacts()
	peer.CoordConfig = nil
	local.CoordConfig.HostMergeError = `host_merge: "~/.claude.json" must be a path relative to $HOME, like .claude.json`
	checks = evaluatePeerSides(local, peer, "m5x26", "m3x23")
	if c := checkFor(checks, "config", DoctorWarn); c == nil || c.Fix != "upgrade dot on m3x23 to compare them" {
		t.Errorf("older peer dot: %+v", checks)
	}
	if c := checkFor(checks, "config", DoctorFail); c == nil || !strings.Contains(c.Detail, "must be a path relative to $HOME") {
		t.Errorf("this Mac's verdict dropped with an older peer: %+v", checks)
	}
}

// localCoordConfig reports its config's own error and each host_merge
// file's state here (#203).
func TestLocalCoordConfigReportsHostMergeFiles(t *testing.T) {
	cfg, localHome, _ := claudeJSONFixture(t, `{"mcpServers":{}}`, `{"mcpServers":{}}`)
	cfg.HostMerge = map[string][]string{"../x.json": {"a"}}
	if c := localCoordConfig(cfg); !strings.Contains(c.HostMergeError, "relative to $HOME") {
		t.Fatalf("invalid config: %+v", c)
	}
	cfg.HostMerge = map[string][]string{".claude.json": {"mcpServers"}}
	path := filepath.Join(localHome, ".claude.json")
	if err := os.Rename(path, path+".real"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path+".real", path); err != nil {
		t.Fatal(err)
	}
	if c := localCoordConfig(cfg); c.HostMergeError != "" || len(c.HostMergeFiles) != 1 || c.HostMergeFiles[0] != (HostMergeFile{Rel: ".claude.json", State: "symlink"}) {
		t.Fatalf("symlinked copy: %+v", c)
	}
}

// The doctor's verdict for the other Mac (hostMergeRefused, from the
// states of both copies) matches what planHostMerges, the sync's own
// check, decides for every pair of copy states (#203).
func TestHostMergeVerdictMatchesTheSync(t *testing.T) {
	states := []string{"absent", "ok", "notjson", "symlink", "nonregular"}
	place := func(t *testing.T, home, state string) {
		t.Helper()
		path := filepath.Join(home, ".claude.json")
		if err := os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}
		switch state {
		case "ok":
			writePeerHomeFile(t, home, ".claude.json", `{"mcpServers":{"a":{}}}`, peerHomeFixedTime)
		case "notjson":
			writePeerHomeFile(t, home, ".claude.json", "null", peerHomeFixedTime)
		case "symlink":
			writePeerHomeFile(t, home, "real.json", `{"mcpServers":{}}`, peerHomeFixedTime)
			if err := os.Symlink(filepath.Join(home, "real.json"), path); err != nil {
				t.Fatal(err)
			}
		case "nonregular":
			if err := os.MkdirAll(path, 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, coord := range states {
		for _, other := range states {
			t.Run(coord+"/"+other, func(t *testing.T) {
				cfg, localHome, peerHome := claudeJSONFixture(t, `{"mcpServers":{}}`, `{"mcpServers":{}}`)
				cfg.HostMerge = map[string][]string{".claude.json": {"mcpServers"}}
				place(t, localHome, coord)
				place(t, peerHome, other)
				refused := false
				merges, err := planHostMerges(context.Background(), peerScheduleRunner(false), cfg)
				if err != nil {
					t.Fatalf("planHostMerges: %v", err)
				}
				if hostMergeRefusal(merges, "") != nil {
					refused = true
				}
				if got := hostFileState(filepath.Join(localHome, ".claude.json")); got != coord {
					t.Fatalf("local state = %s, want %s", got, coord)
				}
				if got := hostFileState(filepath.Join(peerHome, ".claude.json")); got != other {
					t.Fatalf("peer state = %s, want %s", got, other)
				}
				if doctor := hostMergeRefused(coord, other, "here", cfg.Target.Host); (doctor != "") != refused {
					t.Fatalf("doctor %q, sync refused %v", doctor, refused)
				}
			})
		}
	}
}

// hostMergeVerdicts asks planHostMerges itself for this Mac, reading the
// other Mac's copy: a symlink there stops this coordinator's sync (#203).
func TestHostMergeVerdictsReadTheOtherMacsCopy(t *testing.T) {
	cfg, _, peerHome := claudeJSONFixture(t, `{"mcpServers":{"a":{}}}`, `{"mcpServers":{"b":{}}}`)
	cfg.HostMerge = map[string][]string{".claude.json": {"mcpServers"}}
	path := filepath.Join(peerHome, ".claude.json")
	if err := os.Rename(path, path+".real"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path+".real", path); err != nil {
		t.Fatal(err)
	}
	local, peer := &PeerSideFacts{CoordConfig: localCoordConfig(cfg)}, &PeerSideFacts{}
	hostMergeVerdicts(context.Background(), peerScheduleRunner(false), cfg, local, peer, "here", cfg.Target.Host)
	if !strings.Contains(local.CoordConfig.HostMergeError, "a symlink on "+cfg.Target.Host) {
		t.Fatalf("verdict = %q, want the sync's refusal of the other Mac's symlink", local.CoordConfig.HostMergeError)
	}
}

// The other Mac's verdict reads this Mac's copy as the other side: a
// symlink here stops the other Mac's sync once it coordinates (#203).
func TestHostMergeVerdictsJudgeThisMacsCopyForTheOther(t *testing.T) {
	cfg, localHome, _ := claudeJSONFixture(t, `{"mcpServers":{"a":{}}}`, `{"mcpServers":{"b":{}}}`)
	path := filepath.Join(localHome, ".claude.json")
	if err := os.Rename(path, path+".real"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path+".real", path); err != nil {
		t.Fatal(err)
	}
	peer := &PeerSideFacts{CoordConfig: &CoordinatorConfig{HostMergeFiles: []HostMergeFile{{Rel: ".claude.json", State: "ok"}}}}
	hostMergeVerdicts(context.Background(), peerScheduleRunner(false), cfg, &PeerSideFacts{}, peer, "here", "there")
	if !strings.Contains(peer.CoordConfig.HostMergeError, "a symlink on here") {
		t.Fatalf("verdict = %q, want the other Mac's sync refusing this Mac's symlink", peer.CoordConfig.HostMergeError)
	}
}
