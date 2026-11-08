package syncer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// peerHandoverSandbox builds a coordinator config aimed at a local directory
// standing in for the SSH peer, like peerDryRunSandbox but with a real local
// store (real runs write it) and a tunable canned status document. The fake
// ssh serves the document and executes every other remote command locally.
type peerHandoverSandbox struct {
	cfg    *Config
	paths  *LocalPaths
	home   string
	local  string
	peer   string
	owner  string
	record string
}

type peerStatusFields struct {
	owner      string // empty = sandbox owner
	noOwner    bool   // the peer records no owner (a --clear there)
	epoch      int
	dotVersion string // empty = omitted (previous release)
}

func (f peerStatusFields) json(owner, peer, local string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `{"schemaVersion":%d,"kind":"peer"`, PeerStatusSchemaVersion)
	if f.epoch > 0 {
		fmt.Fprintf(&b, `,"ownerEpoch":%d`, f.epoch)
	}
	if f.dotVersion != "" {
		fmt.Fprintf(&b, `,"dotVersion":%q`, f.dotVersion)
	}
	statusOwner := f.owner
	if statusOwner == "" && !f.noOwner {
		statusOwner = owner
	}
	fmt.Fprintf(&b, `,"profile":{"configured":true,"owner":%q,"workspacePath":%q,"target":{"path":%q}}}`,
		statusOwner, peer, local)
	return b.String()
}

func newPeerHandoverSandbox(t *testing.T, fields peerStatusFields, localEpoch int) *peerHandoverSandbox {
	t.Helper()
	names := MachineNames()
	if len(names) == 0 {
		t.Skip("this host reports no machine name, so CheckOwner cannot be satisfied")
	}
	owner := names[0]

	home := t.TempDir()
	t.Setenv("HOME", home)
	base := t.TempDir()
	local := filepath.Join(base, "workspace")
	peer := filepath.Join(base, "peer")
	mirror := filepath.Join(base, "mirror")
	for _, dir := range []string{local, peer, mirror} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(peer, "peer-only.txt"), []byte("peer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "local-only.txt"), []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	paths := ResolveLocalPathsForProfile(local, PeerProfile)
	if err := EnsureLocalLayout(paths); err != nil {
		t.Fatal(err)
	}
	if err := SaveLocalConfig(paths, &LocalConfig{
		Target:      "ssh:fake-peer:" + peer,
		Owner:       owner,
		OwnerEpoch:  localEpoch,
		Propagation: PropagationPolicy{Create: true, Update: true, Delete: true},
		MaxDelete:   100,
	}); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{
		Profile:     PeerProfile,
		Owner:       owner,
		OwnerEpoch:  localEpoch,
		LocalPath:   local + "/",
		MirrorPath:  mirror + "/",
		Target:      Target{Kind: TargetSSH, Host: "fake-peer", Path: peer},
		ConfigDir:   paths.StoreDir,
		LockDir:     filepath.Join(base, "peer.lock"),
		LocalPaths:  paths,
		FilterMode:  FilterModeExclude,
		MaxDelete:   100,
		Propagation: PropagationPolicy{Create: true, Update: true, Delete: true},
	}
	record := filepath.Join(base, "record.log")
	installFakePeerSSH(t, fields.json(owner, peer, local))
	return &peerHandoverSandbox{cfg: cfg, paths: paths, home: home, local: local, peer: peer, owner: owner, record: record}
}

// installRecordingLaunchctl plants a launchctl stub that appends its argv to
// the sandbox record, so demotion order (bootout last) is observable.
func (s *peerHandoverSandbox) installRecordingLaunchctl(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	writeStub(t, filepath.Join(bin, "launchctl"),
		"#!/bin/sh\necho \"launchctl $*\" >> '"+s.record+"'\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// installFakeRemoteDot plants a dot stub where the remote resolver looks
// first ($HOME/.local/bin/dot). It emulates `peer adopt` against the peer
// store, records every invocation in order, and succeeds at sync/setup. The
// peer store is seeded with the baselines a real peer carries, so the
// handover's set-aside step has something to move.
func (s *peerHandoverSandbox) installFakeRemoteDot(t *testing.T) {
	t.Helper()
	store := filepath.Join(s.peer, ".dotfiles", "peer")
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"baseline.manifest", "baseline.peer-target"} {
		if err := os.WriteFile(filepath.Join(store, name), []byte("stale\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	binDir := filepath.Join(s.home, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"[ \"$1\" = --version ] && { echo 'dot version 9.9.9 (fake)'; exit 0; }\n" +
		"echo \"dot $*\" >> '" + s.record + "'\n" +
		"store='" + store + "'\n" +
		"case \"$1 $2\" in\n" +
		"  'peer adopt')\n" +
		"    epoch=; gen=\n" +
		"    for arg do case \"$arg\" in [0-9]*) [ -z \"$epoch\" ] && epoch=$arg || gen=$arg ;; esac; done\n" +
		"    printf 'target: ssh:fake-peer:%s\\nowner: peer-mac\\nowner_epoch: %s\\n' \"" + s.local + "\" \"$epoch\" > \"$store/config.yaml\"\n" +
		"    [ -n \"$gen\" ] && printf '%s\\n' \"$gen\" > \"$store/replica-generation\"\n" +
		"    echo peer-mac ;;\n" +
		"  *) exit 0 ;;\n" +
		"esac\n"
	writeStub(t, filepath.Join(binDir, "dot"), script)
	useRemoteDotCandidates(t, filepath.Join(binDir, "dot"))
}

func (s *peerHandoverSandbox) recordLines(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(s.record)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func (s *peerHandoverSandbox) plantPeerPlist(t *testing.T) string {
	t.Helper()
	plist := filepath.Join(s.home, "Library", "LaunchAgents", "com.dotfiles.peer.plist")
	if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plist, []byte("<plist/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	return plist
}

func loadPeerStoreConfig(t *testing.T, paths *LocalPaths) *LocalConfig {
	t.Helper()
	local, ok, err := LoadLocalConfig(paths)
	if err != nil || !ok {
		t.Fatalf("LoadLocalConfig: ok=%v err=%v", ok, err)
	}
	return local
}

// ── fence (AC6, AC8) ────────────────────────────────────────────────────

func TestPeerFence(t *testing.T) {
	cfg := &Config{
		Owner:      "mac-a",
		OwnerEpoch: 2,
		LocalPath:  "/Users/a/work/",
		Target:     Target{Kind: TargetSSH, Host: "peer", Path: "/Users/b/work"},
	}
	remote := func(owner string, epoch int, version string) *remotePeerStatus {
		s := &remotePeerStatus{OwnerEpoch: epoch, DotVersion: version}
		s.Profile.Owner = owner
		s.Profile.WorkspacePath = "/Users/b/work"
		s.Profile.Target.Path = "/Users/a/work"
		return s
	}

	// AC8: a previous-release peer (no version, no epoch) with the same owner
	// proceeds exactly as today, flagged legacy for the skip message.
	demote, legacy, err := peerFence(cfg, remote("mac-a", 0, ""))
	if err != nil || demote || !legacy {
		t.Fatalf("same-owner old remote: demote=%v legacy=%v err=%v", demote, legacy, err)
	}
	// AC8 + AC6: a remote without epoch support refuses through the existing
	// owner-mismatch check.
	if _, _, err := peerFence(cfg, remote("mac-b", 0, "")); err == nil {
		t.Fatal("old remote with a different owner did not refuse")
	}
	// A current-release peer that simply predates any owner change is not
	// flagged legacy.
	if _, legacy, err := peerFence(cfg, remote("mac-a", 0, "1.2.3")); err != nil || legacy {
		t.Fatalf("epoch-0 current remote: legacy=%v err=%v", legacy, err)
	}
	// Higher remote epoch wins: demote.
	demote, _, err = peerFence(cfg, remote("mac-b", 3, "1.2.3"))
	if err != nil || !demote {
		t.Fatalf("higher remote epoch: demote=%v err=%v", demote, err)
	}
	// A higher remote epoch with no owner refuses instead of demoting to no
	// owner (#202).
	demote, _, err = peerFence(cfg, remote("", 3, "1.2.3"))
	if demote || err == nil || !strings.Contains(err.Error(), "no owner") || !strings.Contains(err.Error(), "dot peer adopt --owner 'mac-a' --epoch 2") {
		t.Fatalf("higher remote epoch, no owner: demote=%v err=%v", demote, err)
	}
	// So does one at this Mac's epoch (a --clear from one epoch behind),
	// where the cleared peer also passes its own guard.
	cleared := remote("", 2, "1.2.3")
	cleared.Profile.CanPush = true
	if demote, _, err := peerFence(cfg, cleared); demote || err == nil || !strings.Contains(err.Error(), "dot peer adopt --owner 'mac-a' --epoch 2") {
		t.Fatalf("equal epoch, no owner: demote=%v err=%v", demote, err)
	}
	// A peer whose dot predates epochs has no adopt: its fix is --set there.
	legacyCleared := remote("", 0, "")
	legacyCleared.Profile.CanPush = true
	if _, _, err := peerFence(cfg, legacyCleared); err == nil || !strings.Contains(err.Error(), "dot sync owner --profile=peer --set 'mac-a'") || strings.Contains(err.Error(), "dot peer adopt --owner") {
		t.Fatalf("legacy peer, no owner: %v", err)
	}
	// A peer with no owner below this Mac's epoch is not a coordinator.
	lower := remote("", 1, "1.2.3")
	lower.Profile.CanPush = true
	if demote, _, err := peerFence(cfg, lower); demote || err != nil {
		t.Fatalf("lower epoch, no owner: demote=%v err=%v", demote, err)
	}
	// This Mac recording no owner is told to record the peer's, not --set
	// itself past the coordinator.
	clearedHere := *cfg
	clearedHere.Owner = ""
	if _, _, err := peerFence(&clearedHere, remote("mac-b", 3, "1.2.3")); err == nil || !strings.Contains(err.Error(), "dot peer adopt --owner 'mac-b' --epoch 3") {
		t.Fatalf("no owner here: %v", err)
	}
	if _, _, err := peerFence(&clearedHere, remote("mac-b", 0, "")); err == nil || !strings.Contains(err.Error(), "dot sync owner --profile=peer --set 'mac-b'") {
		t.Fatalf("no owner here, legacy peer: %v", err)
	}
	// And dot peer setup's owner check, which advised --set on both Macs.
	if err := ownerMatchError(fenceSide{Owner: "mac-a", Epoch: 2}, fenceSide{CanPush: true}); err == nil || !strings.Contains(err.Error(), "dot peer adopt --owner 'mac-a' --epoch 2") || strings.Contains(err.Error(), "--set") {
		t.Fatalf("setup check, no owner: %v", err)
	}
	// Higher local epoch wins: proceed.
	demote, _, err = peerFence(cfg, remote("mac-b", 1, "1.2.3"))
	if err != nil || demote {
		t.Fatalf("lower remote epoch: demote=%v err=%v", demote, err)
	}
	// Equal epochs with different owners refuse on both sides.
	if _, _, err := peerFence(cfg, remote("mac-b", 2, "1.2.3")); err == nil || !strings.Contains(err.Error(), "equal owner epochs") {
		t.Fatalf("equal epochs different owners: err=%v", err)
	}
	// Equal epochs, same owner: normal run.
	if _, _, err := peerFence(cfg, remote("mac-a", 2, "1.2.3")); err != nil {
		t.Fatalf("equal epochs same owner: err=%v", err)
	}
}

func TestSetOwnerBumpsEpochOnPeerProfileOnly(t *testing.T) {
	for _, profile := range []string{PeerProfile, DefaultProfile} {
		paths := ResolveLocalPathsForProfile(t.TempDir(), profile)
		if err := EnsureLocalLayout(paths); err != nil {
			t.Fatal(err)
		}
		if err := SaveLocalConfig(paths, &LocalConfig{Owner: "old", OwnerEpoch: 5, FencePending: true, Propagation: DefaultPropagationPolicy()}); err != nil {
			t.Fatal(err)
		}
		cfg := &Config{Profile: profile, LocalPaths: paths}
		if _, err := SetOwner(OwnerOptions{Config: cfg, SetTo: "new"}); err != nil {
			t.Fatal(err)
		}
		got := loadPeerStoreConfig(t, paths)
		if profile == PeerProfile {
			if got.OwnerEpoch != 6 || got.FencePending {
				t.Errorf("peer profile: epoch=%d fence=%v, want 6/false", got.OwnerEpoch, got.FencePending)
			}
		} else if got.OwnerEpoch != 5 || !got.FencePending {
			t.Errorf("default profile: epoch=%d fence=%v, want unchanged 5/true", got.OwnerEpoch, got.FencePending)
		}
	}
}

func TestSetOwnerBumpsEpochOnUnchangedPeerOwner(t *testing.T) {
	// Equal-fence recovery sets the same coordinator on both machines; the
	// machine that already owned the profile must still advance its epoch or
	// it reads the other's bumped record as a lost fence and demotes itself.
	paths := ResolveLocalPathsForProfile(t.TempDir(), PeerProfile)
	if err := EnsureLocalLayout(paths); err != nil {
		t.Fatal(err)
	}
	if err := SaveLocalConfig(paths, &LocalConfig{Owner: "mac-a", OwnerEpoch: 5, Propagation: DefaultPropagationPolicy()}); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{Profile: PeerProfile, LocalPaths: paths}
	if _, err := SetOwner(OwnerOptions{Config: cfg, SetTo: "mac-a"}); err != nil {
		t.Fatal(err)
	}
	if got := loadPeerStoreConfig(t, paths); got.OwnerEpoch != 6 {
		t.Errorf("epoch = %d, want 6 after re-setting the same owner", got.OwnerEpoch)
	}
}

func TestPeerAdopt(t *testing.T) {
	paths := ResolveLocalPathsForProfile(t.TempDir(), PeerProfile)
	if err := EnsureLocalLayout(paths); err != nil {
		t.Fatal(err)
	}
	if err := SaveLocalConfig(paths, &LocalConfig{Owner: "old", Propagation: DefaultPropagationPolicy()}); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{Profile: PeerProfile, LocalPaths: paths}
	owner, err := PeerAdopt(cfg, PeerAdoptOptions{Owner: "mac-b", Epoch: 7, ReplicaGeneration: 3, FencePending: true})
	if err != nil {
		t.Fatal(err)
	}
	if owner != "mac-b" {
		t.Fatalf("owner = %q", owner)
	}
	got := loadPeerStoreConfig(t, paths)
	if got.Owner != "mac-b" || got.OwnerEpoch != 7 || !got.FencePending {
		t.Fatalf("config = %+v", got)
	}
	gen, err := readPeerReplicaGeneration(paths)
	if err != nil || gen != 3 {
		t.Fatalf("generation = %d, %v", gen, err)
	}
	if _, err := PeerAdopt(cfg, PeerAdoptOptions{Self: true, Epoch: 1}); err != nil {
		t.Fatalf("self adopt: %v", err)
	}
	if got := loadPeerStoreConfig(t, paths); got.FencePending {
		t.Fatal("fence_pending not cleared by a plain adopt")
	}
}

// ── replica push after a complete run (foundation of AC5) ────────────────

func TestPeerSyncCompletePushesReplica(t *testing.T) {
	sb := newPeerHandoverSandbox(t, peerStatusFields{epoch: 1, dotVersion: "dev"}, 1)

	res, err := PeerSync(context.Background(), PeerSyncOptions{
		Config:   sb.cfg,
		Runner:   peerScheduleRunner(false),
		Probe:    peerScheduleRunner(false),
		SkipHome: true,
	})
	if err != nil {
		t.Fatalf("PeerSync: %v", err)
	}
	if !res.Complete || res.Demoted {
		t.Fatalf("result = %+v, want complete", res)
	}

	// The replica landed on the peer store with meta.yaml.
	replicaDir := filepath.Join(sb.peer, ".dotfiles", "peer", "replica")
	metaBody, err := os.ReadFile(filepath.Join(replicaDir, "meta.yaml"))
	if err != nil {
		t.Fatalf("replica meta: %v", err)
	}
	meta := string(metaBody)
	for _, want := range []string{"generation: 1", "coordinator: " + sb.owner, "epoch: 1", "target: ssh:fake-peer:" + sb.peer} {
		if !strings.Contains(meta, want) {
			t.Errorf("meta.yaml missing %q:\n%s", want, meta)
		}
	}
	for _, name := range []string{"baseline.manifest", "baseline.peer-target", "exclude.txt", "ignore.txt", "allow.txt"} {
		if _, err := os.Stat(filepath.Join(replicaDir, name)); err != nil {
			t.Errorf("replica missing %s", name)
		}
	}
	// The baseline marker matches this target (AC4's bootstrap provenance).
	marker, err := os.ReadFile(baselineTargetFileFor(sb.paths.BaselineFile, peerBaselineTargetName))
	if err != nil {
		t.Fatal(err)
	}
	if string(marker) != sb.cfg.Target.RsyncDest()+"\n" {
		t.Errorf("baseline marker = %q, want %q", marker, sb.cfg.Target.RsyncDest())
	}
	// The local generation counter advanced; a second run increments it.
	gen, err := readPeerReplicaGeneration(sb.paths)
	if err != nil || gen != 1 {
		t.Fatalf("generation = %d, %v", gen, err)
	}
	if _, err := PeerSync(context.Background(), PeerSyncOptions{
		Config: sb.cfg, Runner: peerScheduleRunner(false), Probe: peerScheduleRunner(false), SkipHome: true,
	}); err != nil {
		t.Fatal(err)
	}
	metaBody, _ = os.ReadFile(filepath.Join(replicaDir, "meta.yaml"))
	if !strings.Contains(string(metaBody), "generation: 2") {
		t.Errorf("second run did not advance the replica generation:\n%s", metaBody)
	}
}

// ── takeover (AC5) ────────────────────────────────────────────────────────

// peerTakeoverFixture builds the standby's store with a replica the old
// coordinator pushed: baselines, filter files and a valid meta.yaml.
func peerTakeoverFixture(t *testing.T) (*Config, *LocalPaths, string) {
	t.Helper()
	base := t.TempDir()
	local := filepath.Join(base, "workspace")
	remoteA := filepath.Join(base, "a-workspace")
	for _, dir := range []string{local, remoteA} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	paths := ResolveLocalPathsForProfile(local, PeerProfile)
	if err := EnsureLocalLayout(paths); err != nil {
		t.Fatal(err)
	}
	if err := SaveLocalConfig(paths, &LocalConfig{
		Target:      "ssh:fake-peer:" + remoteA,
		Owner:       "mac-a",
		OwnerEpoch:  3,
		MaxDelete:   100,
		Propagation: DefaultPropagationPolicy(),
	}); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{
		Profile:    PeerProfile,
		Owner:      "mac-a",
		OwnerEpoch: 3,
		LocalPath:  local + "/",
		Target:     Target{Kind: TargetSSH, Host: "fake-peer", Path: remoteA},
		LocalPaths: paths,
		MaxDelete:  100,
	}

	replicaDir := peerReplicaDir(paths)
	if err := os.MkdirAll(replicaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	baseline := map[string]Fingerprint{
		"a.txt": {Size: 1, Mtime: time.Unix(100, 0)},
		"b.txt": {Size: 1, Mtime: time.Unix(100, 0)},
		"x.txt": {Size: 1, Mtime: time.Unix(100, 0)},
	}
	if err := SaveBaselineManifest(filepath.Join(replicaDir, "baseline.manifest"), baseline); err != nil {
		t.Fatal(err)
	}
	staleMarker := "mac-a-host:" + local + "/\n"
	if err := os.WriteFile(filepath.Join(replicaDir, "baseline.peer-target"), []byte(staleMarker), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(replicaDir, "exclude.txt"), []byte("*.replica-tmp\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(replicaDir, "home-paths.txt"), []byte(".zshrc\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	meta := peerReplicaMeta{
		Generation:  7,
		Coordinator: "mac-a",
		Epoch:       3,
		Target:      "ssh:fake-peer:" + local,
		Files:       map[string]string{},
	}
	for _, name := range []string{"baseline.manifest", "baseline.peer-target", "exclude.txt", "home-paths.txt"} {
		data, err := os.ReadFile(filepath.Join(replicaDir, name))
		if err != nil {
			t.Fatal(err)
		}
		meta.Files[name] = sha256Hex(data)
	}
	writeReplicaMeta(t, replicaDir, meta)
	return cfg, paths, remoteA
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func writeReplicaMeta(t *testing.T, replicaDir string, meta peerReplicaMeta) {
	t.Helper()
	body, err := yaml.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(replicaDir, "meta.yaml"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPeerTakeover(t *testing.T) {
	cfg, paths, _ := peerTakeoverFixture(t)
	self := PreferredMachineName()
	if self == "" {
		t.Skip("no machine name")
	}

	res, err := PeerTakeover(cfg, PeerTakeoverOptions{Yes: true})
	if err != nil {
		t.Fatalf("PeerTakeover: %v", err)
	}
	if res.Epoch != 4 || res.Generation != 7 || res.Owner != self {
		t.Fatalf("result = %+v, want owner %q epoch 4 generation 7", res, self)
	}

	// Baselines installed; the marker is rewritten for THIS profile's
	// direction, so delete provenance carries over (and max_delete is never
	// raised to make the switch work).
	installed, err := LoadBaselineManifest(paths.BaselineFile)
	if err != nil || len(installed) != 3 {
		t.Fatalf("installed baseline = %v, %v", installed, err)
	}
	marker, err := os.ReadFile(baselineTargetFileFor(paths.BaselineFile, peerBaselineTargetName))
	if err != nil {
		t.Fatal(err)
	}
	if string(marker) != cfg.Target.RsyncDest()+"\n" {
		t.Errorf("rewritten marker = %q, want %q", marker, cfg.Target.RsyncDest())
	}
	got := loadPeerStoreConfig(t, paths)
	if got.Owner != self || got.OwnerEpoch != 4 || !got.FencePending {
		t.Errorf("config = %+v, want self/4/fence-pending", got)
	}
	if got.MaxDelete != 100 {
		t.Errorf("max_delete changed to %d", got.MaxDelete)
	}
	gen, _ := readPeerReplicaGeneration(paths)
	if gen != 7 {
		t.Errorf("generation = %d, want 7", gen)
	}

	// AC5: the first run once the other Mac returns plans no delete older
	// than the replica generation, and the active Mac's simultaneous edits
	// win. b.txt was deleted here after the replica; x.txt was edited on both
	// sides; d.txt appeared on the peer before the replica without reaching
	// the baseline, so its absence here is a pull, never a deletion.
	baseline := map[string]Fingerprint{
		"a.txt": {Size: 1, Mtime: time.Unix(100, 0)},
		"b.txt": {Size: 1, Mtime: time.Unix(100, 0)},
		"x.txt": {Size: 1, Mtime: time.Unix(100, 0)},
	}
	localSnap := PeerSnapshot{
		"a.txt": {Present: true, FP: Fingerprint{Size: 1, Mtime: time.Unix(100, 0)}},
		"x.txt": {Present: true, FP: Fingerprint{Size: 2, Mtime: time.Unix(200, 0)}},
	}
	remoteSnap := PeerSnapshot{
		"a.txt": {Present: true, FP: Fingerprint{Size: 1, Mtime: time.Unix(100, 0)}},
		"b.txt": {Present: true, FP: Fingerprint{Size: 1, Mtime: time.Unix(100, 0)}},
		"x.txt": {Present: true, FP: Fingerprint{Size: 3, Mtime: time.Unix(300, 0)}},
		"d.txt": {Present: true, FP: Fingerprint{Size: 1, Mtime: time.Unix(50, 0)}},
	}
	plan, err := PlanPeerReconcile(baseline, localSnap, remoteSnap)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.DeleteRemote, []string{"b.txt"}) {
		t.Errorf("DeleteRemote = %v, want only the post-replica delete b.txt", plan.DeleteRemote)
	}
	if len(plan.DeleteLocal) != 0 {
		t.Errorf("DeleteLocal = %v, want none", plan.DeleteLocal)
	}
	if !slices.Contains(plan.Push, "x.txt") || slices.Contains(plan.Pull, "x.txt") {
		t.Errorf("coordinator's edit must win the x.txt conflict: push=%v pull=%v", plan.Push, plan.Pull)
	}
	if !slices.Contains(plan.Pull, "d.txt") {
		t.Errorf("peer-created d.txt must be pulled, not deleted: pull=%v", plan.Pull)
	}
}

func TestPeerTakeoverValidation(t *testing.T) {
	t.Run("corrupt file", func(t *testing.T) {
		cfg, paths, _ := peerTakeoverFixture(t)
		if err := os.WriteFile(filepath.Join(peerReplicaDir(paths), "exclude.txt"), []byte("tampered\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := PeerTakeover(cfg, PeerTakeoverOptions{Yes: true}); err == nil || !strings.Contains(err.Error(), "sha256") {
			t.Fatalf("err = %v, want sha256 mismatch", err)
		}
	})
	t.Run("stale generation", func(t *testing.T) {
		cfg, paths, _ := peerTakeoverFixture(t)
		if err := writePeerReplicaGeneration(paths, 9); err != nil {
			t.Fatal(err)
		}
		if _, err := PeerTakeover(cfg, PeerTakeoverOptions{Yes: true}); err == nil || !strings.Contains(err.Error(), "older") {
			t.Fatalf("err = %v, want stale generation refusal", err)
		}
	})
	t.Run("wrong direction", func(t *testing.T) {
		cfg, paths, _ := peerTakeoverFixture(t)
		replicaDir := peerReplicaDir(paths)
		meta, _, err := loadPeerReplica(cfg)
		if err != nil {
			t.Fatal(err)
		}
		meta.Target = "ssh:fake-peer:/somewhere/else"
		writeReplicaMeta(t, replicaDir, *meta)
		if _, err := PeerTakeover(cfg, PeerTakeoverOptions{Yes: true}); err == nil || !strings.Contains(err.Error(), "reverse") {
			t.Fatalf("err = %v, want reverse-target refusal", err)
		}
	})
	t.Run("dry-run installs nothing", func(t *testing.T) {
		cfg, paths, _ := peerTakeoverFixture(t)
		if _, err := PeerTakeover(cfg, PeerTakeoverOptions{Yes: true, DryRun: true}); err != nil {
			t.Fatal(err)
		}
		if installed, err := LoadBaselineManifest(paths.BaselineFile); err != nil || len(installed) != 0 {
			t.Errorf("dry-run installed %d baseline entries", len(installed))
		}
		if got := loadPeerStoreConfig(t, paths); got.Owner != "mac-a" || got.FencePending {
			t.Errorf("dry-run changed the owner config: %+v", got)
		}
	})
	t.Run("declined", func(t *testing.T) {
		cfg, paths, _ := peerTakeoverFixture(t)
		_, err := PeerTakeover(cfg, PeerTakeoverOptions{Confirm: func(string, *PeerTakeoverResult) (bool, error) { return false, nil }})
		if err == nil || !strings.Contains(err.Error(), "declined") {
			t.Fatalf("err = %v, want declined", err)
		}
		if installed, err := LoadBaselineManifest(paths.BaselineFile); err != nil || len(installed) != 0 {
			t.Errorf("declined takeover installed %d baseline entries", len(installed))
		}
	})
}

// ── demotion (AC6) ────────────────────────────────────────────────────────

func TestPeerSyncDemotesOnLostFence(t *testing.T) {
	sb := newPeerHandoverSandbox(t, peerStatusFields{owner: "mac-b", epoch: 5, dotVersion: "dev"}, 1)
	sb.installRecordingLaunchctl(t)
	plist := sb.plantPeerPlist(t)

	res, err := PeerSync(context.Background(), PeerSyncOptions{
		Config:   sb.cfg,
		Runner:   peerScheduleRunner(false),
		Probe:    peerScheduleRunner(false),
		SkipHome: true,
	})
	if err != nil {
		t.Fatalf("PeerSync: %v", err)
	}
	if !res.Demoted {
		t.Fatalf("result = %+v, want demoted", res)
	}

	// It adopted the winner's owner and epoch and cleared any fence.
	got := loadPeerStoreConfig(t, sb.paths)
	if got.Owner != "mac-b" || got.OwnerEpoch != 5 || got.FencePending {
		t.Errorf("config = %+v, want mac-b/5/no-fence", got)
	}
	// No plist and no loaded job behind: the plist is gone and bootout ran.
	if _, err := os.Stat(plist); !os.IsNotExist(err) {
		t.Error("plist left behind after demotion")
	}
	lines := sb.recordLines(t)
	if len(lines) == 0 || lines[len(lines)-1] != "launchctl bootout gui/"+strconv.Itoa(os.Getuid())+"/com.dotfiles.peer" {
		t.Errorf("bootout was not the last service-manager action: %v", lines)
	}
	// Nothing transferred in either direction.
	if _, err := os.Stat(filepath.Join(sb.local, "peer-only.txt")); !os.IsNotExist(err) {
		t.Error("demoted machine pulled from the peer")
	}
	if _, err := os.Stat(filepath.Join(sb.peer, "local-only.txt")); !os.IsNotExist(err) {
		t.Error("demoted machine pushed to the peer")
	}
	if _, err := os.Stat(filepath.Join(sb.peer, ".dotfiles", "peer", "replica")); !os.IsNotExist(err) {
		t.Error("demoted machine pushed a replica")
	}
}

func TestPeerSyncEqualEpochDifferentOwnersRefuses(t *testing.T) {
	sb := newPeerHandoverSandbox(t, peerStatusFields{owner: "mac-b", epoch: 1, dotVersion: "dev"}, 1)
	_, err := PeerSync(context.Background(), PeerSyncOptions{
		Config: sb.cfg, Runner: peerScheduleRunner(false), Probe: peerScheduleRunner(false), SkipHome: true,
	})
	if err == nil || !strings.Contains(err.Error(), "equal owner epochs") {
		t.Fatalf("err = %v, want equal-epoch refusal", err)
	}
	if got := loadPeerStoreConfig(t, sb.paths); got.Owner != sb.owner {
		t.Errorf("owner changed to %q during a refusal", got.Owner)
	}
}

// AC8: a pair with one side on the previous release keeps syncing files
// exactly as today; the new features are skipped with a message.
func TestPeerSyncWithLegacyPeerStillSyncs(t *testing.T) {
	sb := newPeerHandoverSandbox(t, peerStatusFields{epoch: 0, dotVersion: ""}, 0)
	var events []PeerEvent
	res, err := PeerSync(context.Background(), PeerSyncOptions{
		Config:   sb.cfg,
		Runner:   peerScheduleRunner(false),
		Probe:    peerScheduleRunner(false),
		SkipHome: true,
		Progress: func(e PeerEvent) { events = append(events, e) },
	})
	if err != nil {
		t.Fatalf("PeerSync: %v", err)
	}
	if !res.Complete {
		t.Fatalf("result = %+v, want complete", res)
	}
	if _, err := os.Stat(filepath.Join(sb.local, "peer-only.txt")); err != nil {
		t.Error("files did not sync with a legacy peer")
	}
	var legacyNotice bool
	for _, e := range events {
		if e.Kind == PeerEventPeerLacksHandover {
			legacyNotice = true
			// #176: the notice names the binary the run talked to.
			if !strings.Contains(e.Path, "/dot (dot version 9.9.9 (fake))") {
				t.Errorf("legacy notice %q lacks the peer binary path and version", e.Path)
			}
		}
	}
	if !legacyNotice {
		t.Error("no legacy-peer notice emitted")
	}
}

// ── owner re-check before the first remote mutation ───────────────────────

func TestRecheckPeerOwnerBeforeMutation(t *testing.T) {
	okSandbox := newPeerHandoverSandbox(t, peerStatusFields{epoch: 1, dotVersion: "dev"}, 1)
	if err := recheckPeerOwnerBeforeMutation(context.Background(), peerScheduleRunner(false), okSandbox.cfg); err != nil {
		t.Fatalf("matching owner rejected: %v", err)
	}

	lost := newPeerHandoverSandbox(t, peerStatusFields{owner: "mac-b", epoch: 9, dotVersion: "dev"}, 1)
	if err := recheckPeerOwnerBeforeMutation(context.Background(), peerScheduleRunner(false), lost.cfg); err == nil || !strings.Contains(err.Error(), "higher epoch") {
		t.Fatalf("err = %v, want mid-run fence loss", err)
	}
}

// ── handover (AC4) ────────────────────────────────────────────────────────

func TestPeerHandover(t *testing.T) {
	sb := newPeerHandoverSandbox(t, peerStatusFields{epoch: 1, dotVersion: "dev"}, 1)
	sb.installRecordingLaunchctl(t)
	sb.installFakeRemoteDot(t)
	plist := sb.plantPeerPlist(t)

	// The peer's release is older than this one: said before it adopts
	// (#196). A dry run advises the upgrade; a real run, which goes on to
	// adopt, names the repair on the peer.
	// It holds for any older release, patch skew included, and names the
	// hooks consequence when hooks are set.
	var dry []string
	hooked := *sb.cfg
	hooked.Hooks = PeerHooks{OnActivate: []string{"app-open Maru"}}
	if _, err := PeerHandover(context.Background(), PeerHandoverOptions{
		Config:          &hooked,
		Runner:          peerScheduleRunner(false),
		Probe:           peerScheduleRunner(false),
		LocalDotVersion: "99.0.0 (local)",
		DryRun:          true,
		Warn:            func(msg string) { dry = append(dry, msg) },
	}); err != nil {
		t.Fatalf("PeerHandover --dry-run: %v", err)
	}
	if len(dry) != 1 || !strings.Contains(dry[0], "config keys added after its release do not take effect there") ||
		!strings.Contains(dry[0], "no on_activate there") || !strings.HasSuffix(dry[0], "upgrade dot on "+sb.cfg.Target.Host+" before handing over") {
		t.Errorf("dry-run warnings = %q", dry)
	}
	var warned []string
	res, err := PeerHandover(context.Background(), PeerHandoverOptions{
		Config:          sb.cfg,
		Runner:          peerScheduleRunner(false),
		Probe:           peerScheduleRunner(false),
		LocalDotVersion: "99.0.0 (local)",
		Warn: func(msg string) {
			for _, line := range sb.recordLines(t) {
				if strings.HasPrefix(line, "dot peer adopt") {
					t.Errorf("warned after the adopt: %q", msg)
				}
			}
			warned = append(warned, msg)
		},
	})
	if err != nil {
		t.Fatalf("PeerHandover: %v", err)
	}
	if res.NewOwner != "peer-mac" || res.Epoch != 2 {
		t.Fatalf("result = %+v, want peer-mac/2", res)
	}
	if len(warned) != 1 || !strings.Contains(warned[0], "9.9.9") || !strings.Contains(warned[0], "older than this machine's 99.0.0") ||
		!strings.Contains(warned[0], "run `dot peer setup` there") || strings.Contains(warned[0], "on_activate") {
		t.Errorf("warnings = %q", warned)
	}

	// Owner and epoch moved on both sides (local first assertion, remote via
	// the fake dot's config write).
	got := loadPeerStoreConfig(t, sb.paths)
	if got.Owner != "peer-mac" || got.OwnerEpoch != 2 {
		t.Errorf("local config = %+v, want peer-mac/2", got)
	}
	remoteCfg, err := os.ReadFile(filepath.Join(sb.peer, ".dotfiles", "peer", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(remoteCfg), "owner: peer-mac") || !strings.Contains(string(remoteCfg), "owner_epoch: 2") {
		t.Errorf("remote config = %s", remoteCfg)
	}

	// The old Mac has no scheduler, the new one was set up, and the bootstrap
	// ran between those two events.
	if _, err := os.Stat(plist); !os.IsNotExist(err) {
		t.Error("local plist left behind after handover")
	}
	lines := sb.recordLines(t)
	var adoptIdx, bootoutIdx, syncIdx, setupIdx = -1, -1, -1, -1
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "dot peer adopt"):
			adoptIdx = i
		case strings.HasPrefix(line, "launchctl bootout"):
			bootoutIdx = i
		case line == "dot peer sync":
			syncIdx = i
		case line == "dot peer setup":
			setupIdx = i
		}
	}
	if adoptIdx < 0 || bootoutIdx < 0 || syncIdx < 0 || setupIdx < 0 {
		t.Fatalf("missing handover steps in %v", lines)
	}
	if adoptIdx > bootoutIdx || bootoutIdx > syncIdx || syncIdx > setupIdx {
		t.Errorf("handover step order = %v, want adopt < bootout < sync < setup", lines)
	}

	// The peer's old baselines were set aside, not deleted.
	store := filepath.Join(sb.peer, ".dotfiles", "peer")
	entries, err := os.ReadDir(store)
	if err != nil {
		t.Fatal(err)
	}
	var setAside int
	for _, e := range entries {
		if strings.Contains(e.Name(), ".set-aside-") {
			setAside++
		}
		if e.Name() == "baseline.manifest" || e.Name() == "baseline.peer-target" {
			t.Errorf("baseline %s was not set aside", e.Name())
		}
	}
	if setAside == 0 {
		t.Error("no baseline was set aside on the peer")
	}
}

// The bootstrap after a handover plans zero deletes and commits a baseline
// with a matching target marker: covered by
// TestPeerSyncCompletePushesReplica's fresh-store run (no baseline -> nothing
// held, marker asserted there).

func TestPeerHandoverRefusesAHeldSync(t *testing.T) {
	sb := newPeerHandoverSandbox(t, peerStatusFields{epoch: 1, dotVersion: "dev"}, 1)
	sb.installFakeRemoteDot(t)

	// A baseline with mismatched provenance and a path the peer still holds
	// unchanged: the plan wants to propagate its local deletion, the run must
	// hold that transition back, and the handover must refuse before touching
	// anything.
	stamp := time.Unix(100, 0)
	if err := os.Chtimes(filepath.Join(sb.peer, "peer-only.txt"), stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if err := SaveBaselineManifest(sb.paths.BaselineFile, map[string]Fingerprint{
		"peer-only.txt": {Size: 5, Mtime: stamp},
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(baselineTargetFileFor(sb.paths.BaselineFile, peerBaselineTargetName),
		[]byte("some-other-dest/\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := PeerHandover(context.Background(), PeerHandoverOptions{
		Config: sb.cfg,
		Runner: peerScheduleRunner(false),
		Probe:  peerScheduleRunner(false),
	})
	if err == nil || !strings.Contains(err.Error(), "held") {
		t.Fatalf("err = %v, want held-sync refusal", err)
	}
	if lines := sb.recordLines(t); len(lines) != 0 {
		t.Errorf("a refused handover still ran remote steps: %v", lines)
	}
	if got := loadPeerStoreConfig(t, sb.paths); got.Owner != sb.owner {
		t.Errorf("owner changed to %q during a refused handover", got.Owner)
	}
}

func TestPeerHandoverRefusesLegacyPeer(t *testing.T) {
	sb := newPeerHandoverSandbox(t, peerStatusFields{epoch: 0, dotVersion: ""}, 0)
	sb.installFakeRemoteDot(t)
	_, err := PeerHandover(context.Background(), PeerHandoverOptions{
		Config: sb.cfg,
		Runner: peerScheduleRunner(false),
		Probe:  peerScheduleRunner(false),
	})
	if err == nil || !strings.Contains(err.Error(), "upgrade dot") {
		t.Fatalf("err = %v, want upgrade-required refusal", err)
	}
}

// A pending fence lets the scheduler install while the old coordinator is
// unreachable; without it the reachability check still refuses.
func TestPeerScheduleSkipsReachabilityOnlyWhileFencePending(t *testing.T) {
	base := newPeerHandoverSandbox(t, peerStatusFields{epoch: 1, dotVersion: "dev"}, 1)
	// No fake ssh this time: the peer is unreachable.
	t.Setenv("PATH", t.TempDir())

	cfg := *base.cfg
	cfg.FencePending = true
	if _, err := PeerSchedule(context.Background(), PeerScheduleOptions{
		Config:   &cfg,
		Runner:   peerScheduleRunner(false),
		Probe:    peerScheduleRunner(false),
		Interval: time.Minute,
		DryRun:   true,
	}); err != nil {
		t.Fatalf("fence-pending schedule: %v", err)
	}

	cfg.FencePending = false
	if _, err := PeerSchedule(context.Background(), PeerScheduleOptions{
		Config:   &cfg,
		Runner:   peerScheduleRunner(false),
		Probe:    peerScheduleRunner(false),
		Interval: time.Minute,
		DryRun:   true,
	}); err == nil {
		t.Fatal("schedule without a pending fence skipped the reachability check")
	}
}
