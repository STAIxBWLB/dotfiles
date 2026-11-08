package syncer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// PeerSideFacts are one machine's answers to the pre-reunion checks (#182).
// `dot peer doctor --self` prints this machine's; the doctor on the
// other Mac reads them over ssh and compares both sides.
type PeerSideFacts struct {
	MachineNames []string `json:"machineNames"`
	// PreferredName is what `dot peer adopt --self` records here.
	PreferredName string   `json:"preferredName,omitempty"`
	DotPath       string   `json:"dotPath"`
	DotVersion    string   `json:"dotVersion"`
	RsyncPath     string   `json:"rsyncPath,omitempty"`
	RsyncVersion  string   `json:"rsyncVersion,omitempty"`
	RsyncError    string   `json:"rsyncError,omitempty"`
	NFDMarked     bool     `json:"nfdMarked"`
	NonNFD        int      `json:"nonNfd"`
	NonNFDSample  []string `json:"nonNfdSample,omitempty"`
	NonNFDError   string   `json:"nonNfdError,omitempty"`
	// NonNFD* count the names as the other Mac's normalize over ssh walks
	// them (linked worktrees included); CoordNonNFD as this Mac's own
	// preflight walks them when it coordinates (without them).
	CoordNonNFD  *NFDCount         `json:"coordNonNfd,omitempty"`
	Owner        string            `json:"owner"`
	OwnerAliases []string          `json:"ownerAliases,omitempty"`
	OwnerEpoch   int               `json:"ownerEpoch"`
	FencePending bool              `json:"fencePending,omitempty"`
	Coordinator  bool              `json:"coordinator"`
	Scheduler    bool              `json:"scheduler"`
	Replica      *PeerReplicaFacts `json:"replica,omitempty"`
	// ReplicaError is why `dot peer takeover` would refuse the replica.
	ReplicaError string            `json:"replicaError,omitempty"`
	MaxDelete    int               `json:"maxDelete"`
	Propagation  PropagationPolicy `json:"propagation"`
	// WorkspacePath and TargetPath are the profile's two ends, for the
	// fence's topology check; TargetHost is the target's ssh host.
	WorkspacePath string `json:"workspacePath"`
	TargetPath    string `json:"targetPath"`
	TargetHost    string `json:"targetHost,omitempty"`
	// Filters maps each peer filter file to its sha256, or "absent".
	Filters map[string]string `json:"filters"`
	// CoordConfig is the rest of the peer config only the coordinator's
	// copy applies (#203); nil from a peer whose dot predates it.
	CoordConfig *CoordinatorConfig `json:"coordConfig,omitempty"`
}

// CoordinatorConfig is peer config that only the coordinator's copy
// applies, beyond max_delete, propagation and the filter files.
// shared_excludes is not here: a peer profile has no mirror path, so it
// has no effect on either Mac.
type CoordinatorConfig struct {
	HostMerge         map[string][]string `json:"hostMerge,omitempty"`
	IncludeSubmodules bool                `json:"includeSubmodules"`
	FilterMode        string              `json:"filterMode"`
	// HostMergeFiles is each file this Mac's host_merge applies to, with
	// its copy here, for the other Mac to judge both copies as this Mac's
	// sync would.
	HostMergeFiles []HostMergeFile `json:"hostMergeFiles,omitempty"`
	// HostMergeError is why this Mac's peer sync would stop over
	// host_merge once it coordinates. A --self report carries only its
	// config's own error; the doctor fills in the rest (hostMergeVerdicts).
	HostMergeError string `json:"hostMergeError,omitempty"`
}

// HostMergeFile is one host_merge file and the state of its copy on the
// Mac that reports it: absent, symlink, nonregular, notjson or ok.
type HostMergeFile struct {
	Rel   string `json:"rel"`
	State string `json:"state"`
}

// localCoordConfig reads this Mac's coordinator-only config and the state
// of each host_merge file here. It only reads.
func localCoordConfig(cfg *Config) *CoordinatorConfig {
	c := &CoordinatorConfig{
		HostMerge:         cfg.HostMerge,
		IncludeSubmodules: cfg.IncludeSubmodules,
		FilterMode:        string(normalizeFilterMode(cfg.FilterMode)),
	}
	if len(cfg.HostMerge) == 0 {
		return c
	}
	if err := validateHostMerge(cfg.HostMerge); err != nil {
		c.HostMergeError = err.Error()
		return c
	}
	files, err := hostMergeFiles(cfg)
	if err != nil {
		c.HostMergeError = err.Error()
		return c
	}
	for _, rel := range files {
		c.HostMergeFiles = append(c.HostMergeFiles, HostMergeFile{Rel: rel, State: hostFileState(filepath.Join(cfg.HomeDir(), filepath.FromSlash(rel)))})
	}
	return c
}

// hostFileState is a host_merge copy's state, in the terms planHostMerges
// decides by.
// ponytail: an Lstat error other than not-exist (EACCES, a parent that is a
// file) reads as nonregular, where the sync errors on the coordinator and
// its ssh probe reads absent; split the state if that ever shows up.
func hostFileState(path string) string {
	info, err := os.Lstat(path)
	switch {
	case os.IsNotExist(err):
		return "absent"
	case err != nil:
		return "nonregular"
	case info.Mode()&os.ModeSymlink != 0:
		return "symlink"
	case !info.Mode().IsRegular():
		return "nonregular"
	}
	data, err := os.ReadFile(path)
	if err == nil {
		_, err = decodeJSONObject(data)
	}
	if err != nil {
		return "notjson"
	}
	return "ok"
}

// hostMergeRefused is planHostMerges' refusal for one file from the
// coordinator's copy state and the other Mac's (#203), with its wording:
// a file on one Mac only is not refused.
func hostMergeRefused(coord, other, coordHost, otherHost string) string {
	switch {
	case coord == "absent":
		return ""
	case other == "symlink":
		return "a symlink on " + otherHost
	case other == "nonregular":
		return "not a regular file on " + otherHost + " (a directory?)"
	case other == "absent":
		return ""
	case coord == "symlink" || coord == "nonregular":
		return "not a regular file on " + coordHost + " (a symlink?)"
	case coord == "notjson" || other == "notjson":
		return "both copies must be JSON objects"
	}
	return ""
}

// hostMergeVerdicts sets each side's HostMergeError to what its peer sync
// would say over host_merge, both copies of each file judged: this Mac's
// by planHostMerges itself (reading the other Mac's copies over ssh), the
// other Mac's from the states it reported and this Mac's copies (#203).
func hostMergeVerdicts(ctx context.Context, probe *exec.Runner, cfg *Config, local, peer *PeerSideFacts, here, there string) {
	if lc := local.CoordConfig; lc != nil && lc.HostMergeError == "" {
		if merges, err := planHostMerges(ctx, probe, cfg); err != nil {
			lc.HostMergeError = err.Error()
		} else if err := hostMergeRefusal(merges, "its peer sync stops on it before anything moves"); err != nil {
			lc.HostMergeError = err.Error()
		}
	}
	if pc := peer.CoordConfig; pc != nil && pc.HostMergeError == "" {
		for _, f := range pc.HostMergeFiles {
			mine := hostFileState(filepath.Join(cfg.HomeDir(), filepath.FromSlash(f.Rel)))
			if why := hostMergeRefused(f.State, mine, there, here); why != "" {
				pc.HostMergeError = fmt.Sprintf("host_merge %s: %s; fix it or drop it from host_merge (its peer sync stops on it before anything moves)", f.Rel, why)
				break
			}
		}
	}
}

// NFDCount is one walk's count of names not in NFD.
type NFDCount struct {
	Count  int      `json:"count"`
	Sample []string `json:"sample,omitempty"`
	Error  string   `json:"error,omitempty"`
}

// PeerReplicaFacts describe the takeover replica in this machine's store.
type PeerReplicaFacts struct {
	Generation  int       `json:"generation"`
	Coordinator string    `json:"coordinator"`
	Epoch       int       `json:"epoch"`
	WrittenAt   time.Time `json:"writtenAt"`
}

// LocalPeerSideFacts gathers this machine's facts. It only reads; the NFD
// count walks the workspace like `dot sync names normalize --dry-run`.
func LocalPeerSideFacts(ctx context.Context, probe *exec.Runner, cfg *Config, dotVersion string) *PeerSideFacts {
	f := &PeerSideFacts{
		MachineNames: MachineNames(),

		PreferredName: PreferredMachineName(),
		DotVersion:    dotVersion,
		Owner:         cfg.Owner,
		OwnerAliases:  cfg.OwnerAliases,
		OwnerEpoch:    cfg.OwnerEpoch,
		FencePending:  cfg.FencePending,
		Coordinator:   IsPeerCoordinator(cfg),
		MaxDelete:     cfg.MaxDelete,
		Propagation:   cfg.Propagation,
		Filters:       map[string]string{},
		CoordConfig:   localCoordConfig(cfg),

		WorkspacePath: strings.TrimRight(cfg.LocalPath, "/"),
		TargetPath:    cfg.Target.Path,
		TargetHost:    cfg.Target.Host,
	}
	if exe, err := peerExecutable(); err == nil {
		f.DotPath = exe
	}
	if path, ver, err := LocalRsyncPath(ctx, probe); err != nil {
		f.RsyncError = err.Error()
	} else {
		f.RsyncPath, f.RsyncVersion = path, ver
	}
	if cfg.LocalPaths != nil {
		f.NFDMarked = NFDMigrationMarked(cfg.LocalPaths.WorkspaceRoot)
	}
	// Count as each role's sync walks this Mac: the coordinator's preflight
	// skips the linked worktrees a peer run excludes; the other Mac is
	// normalized by `dot sync names normalize` over ssh, which walks them.
	// The doctor judges the count of the role the roles fix leaves.
	// One walk; the coordinator's count leaves out names in linked worktrees.
	count := func(renames []NameRename, skip []string) NFDCount {
		var n NFDCount
		for _, r := range renames {
			if slices.ContainsFunc(skip, func(wt string) bool { return r.OldRel == wt || strings.HasPrefix(r.OldRel, wt+"/") }) {
				continue
			}
			if n.Count < 3 {
				n.Sample = append(n.Sample, r.OldRel)
			}
			n.Count++
		}
		return n
	}
	coord := NFDCount{}
	if plan, err := PlanWorkspaceNameNormalization(cfg); err != nil {
		f.NonNFDError, coord.Error = err.Error(), err.Error()
	} else {
		all := count(plan.Renames, nil)
		f.NonNFD, f.NonNFDSample = all.Count, all.Sample
		// PeerSync stops on this error before it walks: so does the count.
		if worktrees, err := MergePeerWorktrees(cfg, nil, false); err != nil {
			coord.Error = "linked worktrees: " + err.Error()
		} else {
			coord = count(plan.Renames, worktrees)
		}
	}
	f.CoordNonNFD = &coord
	if _, err := os.Stat(filepath.Join(cfg.HomeDir(), "Library", "LaunchAgents", peerSchedulerLabel+".plist")); err == nil {
		f.Scheduler = true
	}
	if cfg.LocalPaths != nil {
		f.Replica, f.ReplicaError = localReplicaFacts(cfg)
		for name, path := range peerReplicaSources(cfg) {
			if strings.HasPrefix(name, "baseline") {
				continue
			}
			data, err := os.ReadFile(path)
			if err != nil {
				f.Filters[name] = "absent"
				continue
			}
			sum := sha256.Sum256(data)
			f.Filters[name] = hex.EncodeToString(sum[:])
		}
	}
	return f
}

// localReplicaFacts applies the takeover's own checks, so the doctor never
// passes a replica that `dot peer takeover` would refuse.
func localReplicaFacts(cfg *Config) (*PeerReplicaFacts, string) {
	info, err := os.Stat(filepath.Join(peerReplicaDir(cfg.LocalPaths), "meta.yaml"))
	if os.IsNotExist(err) {
		return nil, ""
	}
	meta, _, err := loadPeerReplica(cfg)
	if err != nil {
		return nil, err.Error()
	}
	return &PeerReplicaFacts{Generation: meta.Generation, Coordinator: meta.Coordinator, Epoch: meta.Epoch, WrittenAt: info.ModTime().UTC()}, ""
}

func answersTo(names []string, name string) bool {
	want := NormalizeHostname(name)
	for _, n := range names {
		if want != "" && NormalizeHostname(n) == want {
			return true
		}
	}
	return false
}

// nfdVerdict is whether a coordinator's peer syncs, or its diff and dry
// run, stop over one Mac's names not in NFD, by the sync's rules: the push
// preflight (nfdPushRefusal) and its plan on the coordinator; under a
// marked coordinator, the ssh normalize of the other Mac (whose plan must
// succeed) and the inventory gate its diff and dry run apply; under an
// unmarked one, the pull that brings the other Mac's names as they are, for
// its next preflight to refuse. TestDoctorNFDVerdictMatchesTheSync runs the
// real syncs for each case.
func nfdVerdict(s, o *PeerSideFacts, host, other string) (level, detail, fix string) {
	fix = "on " + host + ": dot sync names normalize --profile=peer --yes"
	var quoted []string
	for _, name := range s.NonNFDSample {
		quoted = append(quoted, fmt.Sprintf("%q", name))
	}
	names := fmt.Sprintf("%d name(s) not in NFD", s.NonNFD)
	if len(quoted) > 0 {
		names += " (" + strings.Join(quoted, ", ") + ")"
	}
	switch {
	case s.Coordinator && s.NonNFDError != "":
		return DoctorFail, host + " (coordinator): cannot plan its names, and its sync stops there too: " + s.NonNFDError, ""
	case s.Coordinator && nfdPushRefusal(s.NFDMarked, s.NonNFD, PeerProfile) != nil:
		return DoctorFail, host + " (coordinator, unmarked): " + names + "; its sync refuses before anything moves", fix
	case s.Coordinator && s.NonNFD > 0:
		return DoctorWarn, host + " (coordinator, NFD-marked): " + names + "; its next sync renames them", ""
	case o.Coordinator && o.NFDMarked && s.NonNFDError != "":
		return DoctorFail, host + ": cannot plan its names, and " + other + "'s sync normalizes them here: " + s.NonNFDError, ""
	case o.Coordinator && o.NFDMarked && s.NonNFD > 0:
		return DoctorFail, host + ": " + names + "; " + other + ", the NFD-marked coordinator, stops its diff and dry run on them (its sync renames them first)", fix
	case o.Coordinator && s.NonNFD > 0:
		return DoctorFail, host + ": " + names + "; the next sync pulls them to " + other + ", unmarked, whose following sync refuses them", fix
	case s.NonNFDError != "":
		return DoctorWarn, host + ": cannot count non-NFD names: " + s.NonNFDError, ""
	case s.NonNFD > 0:
		return DoctorWarn, host + ": " + names, fix
	}
	marker := "unmarked"
	if s.NFDMarked {
		marker = "NFD-marked"
	}
	return DoctorPass, host + ": every name in NFD (" + marker + ")", ""
}

// factsFenceSide is a Mac's owner record as the fence compares it; its
// owner guard is its CanPush.
func factsFenceSide(f *PeerSideFacts) fenceSide {
	return fenceSide{Owner: f.Owner, Aliases: f.OwnerAliases, Epoch: f.OwnerEpoch, CanPush: f.Coordinator}
}

// topologyFix names the Mac whose peer target does not point at the other
// Mac's workspace and the target that does, set without touching the
// owner or epoch (dot peer init would record this Mac as the owner).
func topologyFix(local, peer *PeerSideFacts, here, there string) string {
	var fixes []string
	for _, s := range []struct {
		host string
		f, o *PeerSideFacts
	}{{here, local, peer}, {there, peer, local}} {
		if filepath.Clean(s.f.TargetPath) == filepath.Clean(s.o.WorkspacePath) {
			continue
		}
		host := s.f.TargetHost
		if host == "" {
			host = "<ssh host>"
		}
		target := "ssh:" + host + ":" + s.o.WorkspacePath
		if strings.ContainsAny(target, " \t'\"$\\`;&|()<>*?[]{}!#~") {
			target = shellQuote(target)
		}
		fixes = append(fixes, fmt.Sprintf("on %s: dot sync target --profile=peer %s", s.host, target))
	}
	return strings.Join(fixes, "; ")
}

// IsPeerCoordinator reports whether this machine is the peer pair's
// coordinator: a set owner that passes the owner guard here.
func IsPeerCoordinator(cfg *Config) bool {
	return strings.TrimSpace(cfg.Owner) != "" && CheckOwner(cfg) == nil
}

// remotePeerSideFacts runs `dot peer doctor --self` on the peer.
func remotePeerSideFacts(ctx context.Context, runner *exec.Runner, cfg *Config) (*PeerSideFacts, error) {
	out, err := peerRemoteDot(ctx, runner, cfg, "peer", "doctor", "--self")
	if err != nil {
		if strings.Contains(err.Error(), "unknown flag") {
			return nil, fmt.Errorf("%w (the peer's dot predates `peer doctor --self`; upgrade it)", err)
		}
		return nil, err
	}
	var f PeerSideFacts
	if err := json.Unmarshal([]byte(out), &f); err != nil {
		return nil, fmt.Errorf("peer doctor facts: %w", err)
	}
	return &f, nil
}

// Doctor check levels.
const (
	DoctorPass = "pass"
	DoctorWarn = "warn"
	DoctorFail = "fail"
)

// DoctorCheck is one comparison of the two machines, with the command that
// fixes it and the host to run it on.
type DoctorCheck struct {
	Name   string `json:"name"`
	Level  string `json:"level"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

// rolesVerdict is the roles rows: what peerFence decides at the next sync,
// asked through the same fenceDecision from the side that runs it, and who
// coordinates once their fix has run (lc, pc), the state every other row
// judges so that no two fix lines contradict each other. decided is false
// when the fix leaves the choice to the operator: two coordinators at one
// epoch, or a name both Macs answer to, which would make two.
func rolesVerdict(local, peer *PeerSideFacts, here, there string) (checks []DoctorCheck, lc, pc, decided bool) {
	add := func(level, detail, fix string) {
		checks = append(checks, DoctorCheck{Name: "roles", Level: level, Detail: detail, Fix: fix})
	}
	// Both record the chosen owner at one new epoch, so neither side's fence
	// demotes the other; `dot sync owner --set` bumps each Mac's own epoch
	// and keeps them apart.
	choose := fmt.Sprintf("on both Macs: dot peer adopt --owner <a name only the chosen Mac answers to> --epoch %d", max(local.OwnerEpoch, peer.OwnerEpoch)+1)
	lower, higher := here, there
	if local.OwnerEpoch > peer.OwnerEpoch {
		lower, higher = there, here
	}
	switch {
	case local.Coordinator && peer.Coordinator && local.OwnerEpoch == peer.OwnerEpoch:
		// The fence refuses on both sides, or both write: an operator settles it.
		add(DoctorFail, fmt.Sprintf("both machines pass their owner guard at epoch %d: two coordinators", local.OwnerEpoch), choose)
		return checks, true, true, false
	case local.Coordinator && peer.Coordinator:
		// A takeover's pending fence: the lower epoch demotes at its next
		// run. Each side's fence is asked, as each side's sync would.
		lo, hi := local, peer
		if local.OwnerEpoch > peer.OwnerEpoch {
			lo, hi = peer, local
		}
		// The lower side always demotes (the higher epoch is at least 1).
		settle := "on " + lower + ": dot peer sync (its fence demotes it)"
		switch _, herr := fenceDecision(factsFenceSide(hi), factsFenceSide(lo)); {
		case passesAfterAdopting(lo, hi.Owner):
			// It adopts that owner and epoch and still passes its guard: two
			// writers at one epoch, which the fence lets through.
			add(DoctorFail, fmt.Sprintf("both machines pass their owner guard, and %s answers to %s's owner %q too: its demotion leaves two coordinators", lower, higher, hi.Owner), choose)
			return checks, true, true, false
		case herr != nil:
			add(DoctorWarn, fmt.Sprintf("both machines pass their owner guard; %s (epoch %d) wins, but its syncs are refused until %s's next sync demotes it (%v)",
				higher, hi.OwnerEpoch, lower, herr), settle)
		default:
			add(DoctorWarn, fmt.Sprintf("both machines pass their owner guard; the fence settles it: %s (epoch %d) wins over %s (epoch %d), whose next sync demotes it, removing its scheduler and running its on_deactivate hooks",
				higher, hi.OwnerEpoch, lower, lo.OwnerEpoch), settle)
		}
		return checks, local.OwnerEpoch > peer.OwnerEpoch, peer.OwnerEpoch > local.OwnerEpoch, true
	case !local.Coordinator && !peer.Coordinator:
		// Each owner guard refuses, so no peer sync runs. The chosen Mac
		// adopts itself above both epochs; the other records the same owner
		// and epoch, which the fence also needs when it has no epoch yet.
		detail := fmt.Sprintf("neither machine is the coordinator (owner %q here, %q on %s)", local.Owner, peer.Owner, there)
		e := max(local.OwnerEpoch, peer.OwnerEpoch) + 1
		// Both record one owner neither answers to: most likely the
		// coordinator was renamed. A rename keeps its epoch and baselines,
		// and it runs only on the Mac with the peer scheduler while the
		// other has none, so that Mac is the one to coordinate either way.
		renamed := sameOwner(local.Owner, local.OwnerAliases, peer.Owner, peer.OwnerAliases)
		coord, c, other, o := here, local, there, peer
		if renamed && peer.Scheduler && !local.Scheduler {
			coord, c, other, o = there, peer, here, local
		}
		name, arg := c.PreferredName, shellQuote(c.PreferredName)
		if name == "" {
			arg = "<" + coord + "'s name>"
		}
		if answersTo(o.MachineNames, name) {
			add(DoctorFail, fmt.Sprintf("neither machine is the coordinator, and %s answers to %q too: adopting it on both makes two coordinators", other, name), choose)
			return checks, false, false, false
		}
		adopt := fmt.Sprintf("to coordinate from %s: on %s: dot peer adopt --self --epoch %d, then on %s: dot peer adopt --owner %s --epoch %d", coord, coord, e, other, arg, e)
		if renamed {
			// Each Mac renames the owner it records itself (after an offline
			// rename they differ, one known to the other as an alias):
			// RenameOwner refuses any other, and --local-only would pass over
			// it as nothing to rename.
			if local.Scheduler == peer.Scheduler {
				// Neither Mac can prove it runs the owner's scheduler, so the
				// rename needs its explicit override on both, and the operator
				// chooses.
				was, old := fmt.Sprintf("%q", local.Owner), shellQuote(local.Owner)
				if NormalizeHostname(local.Owner) != NormalizeHostname(peer.Owner) {
					was = "the owner"
					old = fmt.Sprintf("<the owner it records: %s here, %s on %s>", shellQuote(local.Owner), shellQuote(peer.Owner), there)
				}
				add(DoctorFail, detail, fmt.Sprintf("if one of these Macs was %s: on it, dot sync owner --rename %s <its name now> --local-only, then the same on the other Mac with that Mac's new name; otherwise %s", was, old, adopt))
				return checks, false, false, false
			}
			adopt = fmt.Sprintf("if %s was %q: on %s: dot sync owner --rename %s <its name now>; otherwise %s", coord, c.Owner, coord, shellQuote(c.Owner), adopt)
		}
		add(DoctorFail, detail, adopt)
		return checks, coord == here, coord == there, true
	}
	c, n, coord, other := local, peer, here, there
	if peer.Coordinator {
		c, n, coord, other = peer, local, there, here
	}
	if answersTo(n.MachineNames, c.Owner) {
		// Aligned, it would pass its guard too, at the same epoch and owner.
		add(DoctorFail, fmt.Sprintf("%s answers to the coordinator's owner %q too: once it records that owner, both machines pass their owner guard at one epoch", other, c.Owner), choose)
		return checks, false, false, false
	}
	// The settled state: the other Mac records the coordinator's owner and
	// epoch, so the fence proceeds.
	align := fmt.Sprintf("on %s: dot peer adopt --owner %s --epoch %d", other, shellQuote(c.Owner), c.OwnerEpoch)
	demote, err := fenceDecision(factsFenceSide(c), factsFenceSide(n))
	switch {
	case err != nil:
		add(DoctorFail, fmt.Sprintf("%s's next sync is refused: %v", coord, err), align)
	case demote && passesAfterAdopting(c, n.Owner):
		add(DoctorWarn, fmt.Sprintf("%s records epoch %d over %s's %d: %s's next sync demotes it, removing its scheduler and running its on_deactivate hooks, though it stays the owner", other, n.OwnerEpoch, coord, c.OwnerEpoch, coord), align)
	case demote:
		add(DoctorFail, fmt.Sprintf("%s records owner %q at epoch %d over %s's %d: %s's next sync demotes it to that owner, which it does not answer to, leaving no coordinator", other, n.Owner, n.OwnerEpoch, coord, c.OwnerEpoch, coord), align)
	case n.OwnerEpoch != c.OwnerEpoch:
		// The coordinator proceeds; the other Mac never syncs to catch up.
		add(DoctorPass, fmt.Sprintf("coordinator: %s, owner %q (epoch %d here, %d on %s)", coord, c.Owner, local.OwnerEpoch, peer.OwnerEpoch, there), "")
		why := ""
		if !sameOwner(n.Owner, n.OwnerAliases, c.Owner, c.OwnerAliases) {
			why = "; dot peer setup on " + coord + " needs both to name the same owner"
		}
		add(DoctorWarn, fmt.Sprintf("%s still records owner %q at epoch %d (the coordinator's: %q at %d)%s", other, n.Owner, n.OwnerEpoch, c.Owner, c.OwnerEpoch, why), align)
	default:
		add(DoctorPass, fmt.Sprintf("coordinator: %s, owner %q (epoch %d here, %d on %s)", coord, c.Owner, local.OwnerEpoch, peer.OwnerEpoch, there), "")
		// An offline rename whose --local-only step never ran there: the
		// coordinator's sync says so on every run (PeerEventOwnerRenamePending).
		if old := n.Owner; NormalizeHostname(old) != NormalizeHostname(c.Owner) && ownersMatch(old, nil, c.OwnerAliases...) {
			add(DoctorWarn, fmt.Sprintf("%s still records the coordinator's earlier name %q; the alias keeps them working until it records %q", other, old, c.Owner),
				"on "+other+": dot sync owner --rename "+shellQuote(old)+" "+shellQuote(c.Owner)+" --local-only")
		}
	}
	return checks, local.Coordinator, peer.Coordinator, true
}

// passesAfterAdopting says whether s still passes its owner guard once it
// adopts owner, as a demotion's PeerAdopt leaves it: AssignOwner keeps its
// own aliases only when owner is the one it already records.
func passesAfterAdopting(s *PeerSideFacts, owner string) bool {
	var kept []string
	if NormalizeHostname(owner) == NormalizeHostname(s.Owner) {
		kept = s.OwnerAliases
	}
	return ownersMatch(owner, kept, s.MachineNames...)
}

// evaluatePeerSides compares both machines' facts (#182). here and there
// name the machines in details and fixes.
func evaluatePeerSides(local, peer *PeerSideFacts, here, there string) []DoctorCheck {
	var checks []DoctorCheck
	add := func(name, level, detail, fix string) {
		checks = append(checks, DoctorCheck{Name: name, Level: level, Detail: detail, Fix: fix})
	}
	type side struct {
		host  string
		f     *PeerSideFacts
		guard bool // passes its owner guard now
	}
	roles, lc, pc, decided := rolesVerdict(local, peer, here, there)
	if !decided {
		lc, pc = local.Coordinator, peer.Coordinator
	}
	el, ep := *local, *peer
	el.Coordinator, ep.Coordinator = lc, pc
	// A coordinator's names are counted as its sync walks them.
	for _, f := range []*PeerSideFacts{&el, &ep} {
		if f.Coordinator && f.CoordNonNFD != nil {
			f.NonNFD, f.NonNFDSample, f.NonNFDError = f.CoordNonNFD.Count, f.CoordNonNFD.Sample, f.CoordNonNFD.Error
		}
	}
	sides := []side{{here, &el, local.Coordinator}, {there, &ep, peer.Coordinator}}

	// This machine's client is the doctor's first line. Every Mac that passes
	// its owner guard runs one (a sync checks rsync before its fence, so a
	// coordinator about to be demoted needs it to get there), and so does the
	// Mac the roles fix makes the coordinator.
	switch {
	case peer.RsyncError != "" && (peer.Coordinator || ep.Coordinator):
		add("rsync", DoctorFail, there+": "+peer.RsyncError, "on "+there+": brew install rsync")
	case peer.RsyncError != "":
		add("rsync", DoctorWarn, there+": "+peer.RsyncError+" (its client once it coordinates; see remote rsync above for the server this Mac uses)", "on "+there+": brew install rsync")
	default:
		add("rsync", DoctorPass, there+": "+peer.RsyncPath+" ("+peer.RsyncVersion+")", "")
	}

	for i, s := range sides {
		o := sides[1-i]
		level, detail, fix := nfdVerdict(s.f, o.f, s.host, o.host)
		add("nfd", level, detail, fix)
	}
	if local.NFDMarked != peer.NFDMarked {
		marked, other := here, there
		if peer.NFDMarked {
			marked, other = there, here
		}
		add("nfd", DoctorWarn, "the NFD marker is set on "+marked+" only", "on "+other+": dot sync names normalize --profile=peer --yes")
	}

	// The fence checks topology first, on whichever Mac syncs, and dot peer
	// setup checks it too; it is symmetric, so one call answers for both.
	// Facts from an older dot lack the paths.
	if topo := topologyError(local.WorkspacePath, local.TargetPath, peer.WorkspacePath, peer.TargetPath); topo != nil && local.WorkspacePath != "" && peer.WorkspacePath != "" {
		add("topology", DoctorFail, "every sync is refused, and dot peer setup outside a takeover's pending fence: "+topo.Error()+"; a new target resets the baseline, so deletions are held until the next complete sync", topologyFix(local, peer, here, there))
	}
	checks = append(checks, roles...)

	// Their fixes come after the roles and topology fixes, when those have
	// one: dot peer setup checks both, and the replica comes from the
	// settled coordinator.
	after := ""
	if slices.ContainsFunc(checks, func(c DoctorCheck) bool { return (c.Name == "roles" || c.Name == "topology") && c.Level != DoctorPass }) {
		after = "after the fixes above, "
	}
	if !decided {
		add("scheduler", DoctorWarn, "the roles fix chooses the coordinator; only it keeps a peer scheduler", after+"dot peer setup on the Mac you choose, dot peer setup --off on the other")
		return appendConfigChecks(checks, &el, &ep, here, there)
	}
	for _, s := range sides {
		switch {
		case s.f.Coordinator && !s.f.Scheduler:
			add("scheduler", DoctorWarn, s.host+" is the coordinator but has no peer scheduler", after+"on "+s.host+": dot peer setup")
		case !s.f.Coordinator && s.f.Scheduler && s.guard:
			// The fence demotes it, and the demotion removes the scheduler.
			add("scheduler", DoctorPass, s.host+": its demoting sync removes its peer scheduler", "")
		case !s.f.Coordinator && s.f.Scheduler:
			add("scheduler", DoctorWarn, s.host+" is not the coordinator but has a peer scheduler", after+"on "+s.host+": dot peer setup --off")
		case s.f.Scheduler:
			add("scheduler", DoctorPass, s.host+": peer scheduler installed (coordinator)", "")
		default:
			add("scheduler", DoctorPass, s.host+": no peer scheduler (not the coordinator)", "")
		}
	}

	for _, s := range sides {
		if s.f.Coordinator {
			continue
		}
		if s.f.ReplicaError != "" {
			add("replica", DoctorWarn, s.host+": a takeover there would refuse the replica: "+s.f.ReplicaError, after+"run a complete dot peer sync on the coordinator")
			continue
		}
		if s.f.Replica == nil {
			add("replica", DoctorWarn, "no takeover replica on "+s.host+": a takeover there is not possible", after+"run a complete dot peer sync on the coordinator")
			continue
		}
		if slices.ContainsFunc(s.f.MachineNames, func(n string) bool {
			return NormalizeHostname(n) == NormalizeHostname(s.f.Replica.Coordinator)
		}) {
			add("replica", DoctorWarn, s.host+": the replica there was staged by "+s.host+" itself, not pushed by the coordinator", after+"run a complete dot peer sync on the coordinator")
			continue
		}
		age := time.Since(s.f.Replica.WrittenAt).Round(time.Minute)
		add("replica", DoctorPass, fmt.Sprintf("%s: replica generation %d from %s, written %s ago", s.host, s.f.Replica.Generation, s.f.Replica.Coordinator, age), "")
	}
	return appendConfigChecks(checks, &el, &ep, here, there)
}

// appendConfigChecks compares the config only the coordinator's copy
// applies, judging each side as the roles fix leaves it (#203).
func appendConfigChecks(checks []DoctorCheck, local, peer *PeerSideFacts, here, there string) []DoctorCheck {
	add := func(name, level, detail, fix string) {
		checks = append(checks, DoctorCheck{Name: name, Level: level, Detail: detail, Fix: fix})
	}
	if local.MaxDelete != peer.MaxDelete {
		add("config", DoctorWarn, fmt.Sprintf("max_delete is %d here and %d on %s; only the coordinator's applies", local.MaxDelete, peer.MaxDelete, there), "align .dotfiles/peer/config.yaml on both")
	}
	if local.Propagation != peer.Propagation {
		add("config", DoctorWarn, fmt.Sprintf("propagation differs (%+v here, %+v on %s)", local.Propagation, peer.Propagation, there), "align .dotfiles/peer/config.yaml on both")
	}
	var drift []string
	for name, sum := range local.Filters {
		if peer.Filters[name] != sum {
			drift = append(drift, name)
		}
	}
	for name := range peer.Filters {
		if _, ok := local.Filters[name]; !ok {
			drift = append(drift, name)
		}
	}
	sort.Strings(drift)
	if len(drift) > 0 {
		add("config", DoctorWarn, "filter files differ: "+strings.Join(drift, ", "), "copy the coordinator's .dotfiles/peer filter files to the other Mac")
	}
	lc, pc := local.CoordConfig, peer.CoordConfig
	switch {
	case pc == nil:
		add("config", DoctorWarn, there+"'s dot does not report host_merge, include_submodules or filter_mode", "upgrade dot on "+there+" to compare them")
	case lc != nil:
		var differ []string
		if !sameHostMerge(lc.HostMerge, pc.HostMerge) {
			differ = append(differ, "host_merge")
		}
		if lc.IncludeSubmodules != pc.IncludeSubmodules {
			differ = append(differ, "include_submodules")
		}
		if lc.FilterMode != pc.FilterMode {
			differ = append(differ, "filter_mode")
		}
		if len(differ) > 0 {
			add("config", DoctorWarn, strings.Join(differ, ", ")+" differ between the Macs; the coordinator's copy decides its peer sync", "align .dotfiles/peer/config.yaml on both")
		}
	}
	// Each side's own verdict, this Mac's even when the other Mac's dot is
	// too old to report its own.
	for _, s := range []struct {
		host string
		f    *PeerSideFacts
	}{{here, local}, {there, peer}} {
		if s.f.CoordConfig == nil || s.f.CoordConfig.HostMergeError == "" {
			continue
		}
		msg := s.f.CoordConfig.HostMergeError
		if s.f.Coordinator {
			add("config", DoctorFail, s.host+"'s peer sync stops over host_merge: "+msg, "fix host_merge in .dotfiles/peer/config.yaml on "+s.host+", or the file the detail names")
		} else {
			add("config", DoctorWarn, s.host+"'s peer sync would stop over host_merge once it coordinates: "+msg, "fix host_merge in .dotfiles/peer/config.yaml on "+s.host+", or the file the detail names")
		}
	}
	if checks[len(checks)-1].Name != "config" {
		add("config", DoctorPass, "the coordinator-only config matches (max_delete, propagation, filter files, host_merge, include_submodules, filter_mode)", "")
	}
	return checks
}

// sameHostMerge compares two host_merge maps, each file's keys as a set.
func sameHostMerge(a, b map[string][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for rel, keys := range a {
		other, ok := b[rel]
		if !ok || !slices.Equal(sortedCopy(keys), sortedCopy(other)) {
			return false
		}
	}
	return true
}

func sortedCopy(s []string) []string {
	c := slices.Clone(s)
	slices.Sort(c)
	return c
}
