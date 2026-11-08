package syncer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// peerHomeFixedTime pins every fixture mtime to one exact second, so a local
// FingerprintFast and the remote inventory's second-precision %M compare equal
// only when the test made them equal.
var peerHomeFixedTime = time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)

func writePeerHomeFile(t *testing.T, root, rel, body string, mtime time.Time) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(abs, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func peerHomeFP(t *testing.T, root, rel string) Fingerprint {
	t.Helper()
	fp, err := FingerprintFile(filepath.Join(root, filepath.FromSlash(rel)), FingerprintFast)
	if err != nil {
		t.Fatalf("fingerprint %s: %v", rel, err)
	}
	return fp
}

// installFakePeerHomeSSH is installFakePeerSSH plus a chdir into the fake
// peer's home. A bare "host:" rsync endpoint resolves to the remote shell's
// cwd, which over real sshd is the login home; the stub must reproduce that
// or tracked-home transfers would land in the test process's cwd.
func installFakePeerHomeSSH(t *testing.T, statusJSON, peerHome string) {
	t.Helper()
	requirePeerRsync(t)
	if strings.Contains(statusJSON, "'") || strings.Contains(peerHome, "'") {
		t.Fatal("fixture values must not contain a single quote")
	}
	script := "#!/bin/sh\n" +
		"while [ $# -gt 0 ]; do\n" +
		"  case \"$1\" in\n" +
		"    -o) shift 2 ;;\n" +
		"    fake-peer) shift; break ;;\n" +
		"    *) shift ;;\n" +
		"  esac\n" +
		"done\n" +
		"case \"$*\" in\n" +
		"  *\"list dot candidates\"*) exec /bin/sh -c \"$*\" ;;\n" +
		"  *--version*) echo 'rsync  version 3.4.1  protocol version 32' ;;\n" +
		"  *\"peer status --json\"*) printf '%s\\n' '" + statusJSON + "' ;;\n" +
		"  *) HOME='" + peerHome + "'; export HOME; cd \"$HOME\" || exit 1; exec /bin/sh -c \"$*\" ;;\n" +
		"esac\n"
	bin := t.TempDir()
	writeStub(t, filepath.Join(bin, "ssh"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	dot := filepath.Join(bin, "dot")
	writeStub(t, dot, "#!/bin/sh\n[ \"$1\" = --version ] && echo 'dot version 9.9.9 (fake)'\nexit 0\n")
	useRemoteDotCandidates(t, dot)
}

// peerHomeTrackedFixture builds a full peer sandbox: a coordinator config with
// its own home and workspace, a local directory standing in for the peer's
// home (reached through the ssh stub), and the tracked list file written into
// an existing peer store.
func peerHomeTrackedFixture(t *testing.T, trackedList string) (cfg *Config, paths *LocalPaths, localHome, peerHome string) {
	t.Helper()
	requireDeleteMissingArgsRsync(t)
	names := MachineNames()
	if len(names) == 0 {
		t.Skip("this host reports no machine name, so CheckOwner cannot be satisfied")
	}
	owner := names[0]

	base := t.TempDir()
	localHome = filepath.Join(base, "local-home")
	peerHome = filepath.Join(base, "peer-home")
	local := filepath.Join(base, "workspace")
	peerWS := filepath.Join(base, "peer-workspace")
	for _, dir := range []string{localHome, peerHome, local, peerWS} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	paths = ResolveLocalPathsForProfile(local, PeerProfile)
	if err := os.MkdirAll(paths.StoreDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(PeerHomeTrackedFile(paths), []byte(trackedList), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg = &Config{
		Profile:     PeerProfile,
		Owner:       owner,
		Home:        localHome,
		LocalPath:   local + "/",
		MirrorPath:  filepath.Join(base, "mirror") + "/",
		Target:      Target{Kind: TargetSSH, Host: "fake-peer", Path: peerWS},
		ConfigDir:   paths.StoreDir,
		LockDir:     filepath.Join(base, "peer.lock"),
		LocalPaths:  paths,
		FilterMode:  FilterModeExclude,
		MaxDelete:   1000,
		Propagation: PropagationPolicy{Create: true, Update: true, Delete: true},
	}
	status := fmt.Sprintf(
		`{"schemaVersion":%d,"kind":"peer","dotVersion":"9.9.9 (fake)","profile":{"configured":true,"owner":%q,"workspacePath":%q,"target":{"path":%q}}}`,
		PeerStatusSchemaVersion, owner, peerWS, local)
	installFakePeerHomeSSH(t, status, peerHome)
	return cfg, paths, localHome, peerHome
}

func seedPeerHomeBaseline(t *testing.T, cfg *Config, entries map[string]Fingerprint) {
	t.Helper()
	baselineFile := peerHomeBaselineFile(cfg.LocalPaths)
	if err := SaveBaselineManifest(baselineFile, entries); err != nil {
		t.Fatal(err)
	}
	if err := markBaselineTarget(baselineFile, homeBaselineTargetName, cfg.Target.RsyncDest()); err != nil {
		t.Fatal(err)
	}
}

// singleQuarantinedHomeFile finds the one quarantined copy of rel under a
// home's .dot-peer-conflicts, failing on zero or several matches.
func singleQuarantinedHomeFile(t *testing.T, home, rel string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(home, homeConflictDirName, "*", homeConflictSubFromPeer, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("want exactly one quarantined copy of %s under %s, got %v", rel, home, matches)
	}
	return matches[0]
}

// TestPeerHomeTrackedSync_PropagatesDeleteToPeerHome is the property the whole
// pass rests on: a local delete under a tracked home path removes the peer's
// copy into quarantine, while a file the peer created is spared and pulled.
func TestPeerHomeTrackedSync_PropagatesDeleteToPeerHome(t *testing.T) {
	cfg, _, localHome, peerHome := peerHomeTrackedFixture(t, "# tracked host paths\nmem\n")
	writePeerHomeFile(t, localHome, "mem/keep.txt", "keep", peerHomeFixedTime)
	writePeerHomeFile(t, peerHome, "mem/keep.txt", "keep", peerHomeFixedTime)
	writePeerHomeFile(t, peerHome, "mem/gone.txt", "removed", peerHomeFixedTime)
	writePeerHomeFile(t, peerHome, "mem/peer-new.txt", "new", peerHomeFixedTime)
	seedPeerHomeBaseline(t, cfg, map[string]Fingerprint{
		"mem/keep.txt": peerHomeFP(t, localHome, "mem/keep.txt"),
		"mem/gone.txt": {Size: int64(len("removed")), Mtime: peerHomeFixedTime},
	})

	complete, _, err := peerHomeTrackedSync(context.Background(),
		peerScheduleRunner(false), peerScheduleRunner(false), cfg, nil, false, false, false)
	if err != nil {
		t.Fatalf("peerHomeTrackedSync: %v", err)
	}
	if !complete {
		t.Error("an authorized delete pass was reported incomplete")
	}

	if _, err := os.Lstat(filepath.Join(peerHome, "mem", "gone.txt")); !os.IsNotExist(err) {
		t.Errorf("tombstoned tracked path still on the peer: %v", err)
	}
	quarantined := singleQuarantinedHomeFile(t, peerHome, "mem/gone.txt")
	if body, err := os.ReadFile(quarantined); err != nil || string(body) != "removed" {
		t.Errorf("quarantined peer copy = %q, %v; want %q", body, err, "removed")
	}
	if _, err := os.Lstat(filepath.Join(peerHome, "mem", "peer-new.txt")); err != nil {
		t.Errorf("peer-only tracked file was destroyed: %v", err)
	}
	if body, err := os.ReadFile(filepath.Join(localHome, "mem", "peer-new.txt")); err != nil || string(body) != "new" {
		t.Errorf("peer-only tracked file was not pulled: %q, %v", body, err)
	}

	baseline, err := LoadBaselineManifest(peerHomeBaselineFile(cfg.LocalPaths))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := baseline["mem/gone.txt"]; ok {
		t.Error("home baseline still carries the propagated delete")
	}
	for _, rel := range []string{"mem/keep.txt", "mem/peer-new.txt"} {
		if _, ok := baseline[rel]; !ok {
			t.Errorf("home baseline lost %s after a complete transaction", rel)
		}
	}
	tombs, err := LoadTombstones(cfg.LocalPaths.TombstonesFile)
	if err != nil {
		t.Fatal(err)
	}
	found := slices.IndexFunc(tombs, func(tb Tombstone) bool { return tb.RelPath == "mem/gone.txt" })
	if found == -1 {
		t.Errorf("tombstones audit lacks the propagated home delete: %+v", tombs)
	}
}

// TestPeerHomeTrackedSync_DualEditQuarantinesPeerPayload: the same tracked
// file edited on both sides keeps the coordinator copy in place on the peer
// and the peer's copy in the home conflict root, with an audit entry.
func TestPeerHomeTrackedSync_DualEditQuarantinesPeerPayload(t *testing.T) {
	cfg, _, localHome, peerHome := peerHomeTrackedFixture(t, "mem\n")
	original := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	writePeerHomeFile(t, localHome, "mem/conflict.txt", "local-wins", peerHomeFixedTime)
	writePeerHomeFile(t, peerHome, "mem/conflict.txt", "peer-loses", peerHomeFixedTime.Add(time.Hour))
	seedPeerHomeBaseline(t, cfg, map[string]Fingerprint{
		"mem/conflict.txt": {Size: int64(len("original")), Mtime: original},
	})

	complete, _, err := peerHomeTrackedSync(context.Background(),
		peerScheduleRunner(false), peerScheduleRunner(false), cfg, nil, false, false, false)
	if err != nil {
		t.Fatalf("peerHomeTrackedSync: %v", err)
	}
	if !complete {
		t.Error("a dual-edit conflict is not a held transition; the run must complete")
	}

	if body, err := os.ReadFile(filepath.Join(peerHome, "mem", "conflict.txt")); err != nil || string(body) != "local-wins" {
		t.Errorf("peer copy after conflict = %q, %v; want the coordinator's %q", body, err, "local-wins")
	}
	if body, err := os.ReadFile(filepath.Join(localHome, "mem", "conflict.txt")); err != nil || string(body) != "local-wins" {
		t.Errorf("local copy after conflict = %q, %v; want %q untouched", body, err, "local-wins")
	}
	backup := singleQuarantinedHomeFile(t, peerHome, "mem/conflict.txt")
	if body, err := os.ReadFile(backup); err != nil || string(body) != "peer-loses" {
		t.Errorf("quarantined peer payload = %q, %v; want %q", body, err, "peer-loses")
	}

	audit, err := os.ReadFile(filepath.Join(cfg.ConfigDir, "peer-conflicts.log"))
	if err != nil {
		t.Fatalf("dual-edit conflict was not audited: %v", err)
	}
	if !strings.Contains(string(audit), "mem/conflict.txt") || !strings.Contains(string(audit), "simultaneous edit/edit") {
		t.Errorf("audit entry missing the conflict path or reason: %s", audit)
	}

	baseline, err := LoadBaselineManifest(peerHomeBaselineFile(cfg.LocalPaths))
	if err != nil {
		t.Fatal(err)
	}
	want := peerHomeFP(t, localHome, "mem/conflict.txt")
	if got, ok := baseline["mem/conflict.txt"]; !ok || got.Size != want.Size || !sameMtime(got.Mtime, want.Mtime) {
		t.Errorf("home baseline after conflict = %+v (present %v), want the local fingerprint %+v", got, ok, want)
	}
}

// TestPeerHomeTrackedSync_QuarantinesLocalCopyOnPeerDelete: when the peer
// deleted a tracked file, the local copy moves into the local home conflict
// root instead of being unlinked.
func TestPeerHomeTrackedSync_QuarantinesLocalCopyOnPeerDelete(t *testing.T) {
	cfg, _, localHome, peerHome := peerHomeTrackedFixture(t, "mem\n")
	writePeerHomeFile(t, localHome, "mem/shared.txt", "shared", peerHomeFixedTime)
	writePeerHomeFile(t, localHome, "mem/peer-deleted.txt", "mine", peerHomeFixedTime)
	writePeerHomeFile(t, peerHome, "mem/shared.txt", "shared", peerHomeFixedTime)
	seedPeerHomeBaseline(t, cfg, map[string]Fingerprint{
		"mem/shared.txt":       peerHomeFP(t, localHome, "mem/shared.txt"),
		"mem/peer-deleted.txt": peerHomeFP(t, localHome, "mem/peer-deleted.txt"),
	})

	complete, _, err := peerHomeTrackedSync(context.Background(),
		peerScheduleRunner(false), peerScheduleRunner(false), cfg, nil, false, false, false)
	if err != nil {
		t.Fatalf("peerHomeTrackedSync: %v", err)
	}
	if !complete {
		t.Error("an authorized inbound delete was reported incomplete")
	}

	if _, err := os.Lstat(filepath.Join(localHome, "mem", "peer-deleted.txt")); !os.IsNotExist(err) {
		t.Errorf("peer-deleted file still in place locally: %v", err)
	}
	quarantined := singleQuarantinedHomeFile(t, localHome, "mem/peer-deleted.txt")
	if body, err := os.ReadFile(quarantined); err != nil || string(body) != "mine" {
		t.Errorf("local quarantine copy = %q, %v; want %q", body, err, "mine")
	}
	if _, err := os.Lstat(filepath.Join(peerHome, "mem", "shared.txt")); err != nil {
		t.Errorf("unrelated peer file touched: %v", err)
	}

	baseline, err := LoadBaselineManifest(peerHomeBaselineFile(cfg.LocalPaths))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := baseline["mem/peer-deleted.txt"]; ok {
		t.Error("home baseline still carries the accepted peer delete")
	}
	if _, ok := baseline["mem/shared.txt"]; !ok {
		t.Error("home baseline lost the untouched shared file")
	}
}

// TestPeerHomeTrackedSync_HoldsDeletesWithoutProvenance: without a home
// baseline target marker, a local delete stays pending, the peer copy is
// retained, and the home baseline does not advance.
func TestPeerHomeTrackedSync_HoldsDeletesWithoutProvenance(t *testing.T) {
	cfg, _, localHome, peerHome := peerHomeTrackedFixture(t, "mem\n")
	writePeerHomeFile(t, localHome, "mem/keep.txt", "keep", peerHomeFixedTime)
	writePeerHomeFile(t, peerHome, "mem/keep.txt", "keep", peerHomeFixedTime)
	writePeerHomeFile(t, peerHome, "mem/gone.txt", "removed", peerHomeFixedTime)
	baselineFile := peerHomeBaselineFile(cfg.LocalPaths)
	if err := SaveBaselineManifest(baselineFile, map[string]Fingerprint{
		"mem/keep.txt": peerHomeFP(t, localHome, "mem/keep.txt"),
		"mem/gone.txt": {Size: int64(len("removed")), Mtime: peerHomeFixedTime},
	}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(baselineFile)
	if err != nil {
		t.Fatal(err)
	}

	var events []PeerEvent
	complete, _, err := peerHomeTrackedSync(context.Background(),
		peerScheduleRunner(false), peerScheduleRunner(false), cfg,
		func(e PeerEvent) { events = append(events, e) }, false, false, false)
	if err != nil {
		t.Fatalf("peerHomeTrackedSync: %v", err)
	}
	if complete {
		t.Error("a run with held deletes must report complete=false")
	}
	if !slices.ContainsFunc(events, func(e PeerEvent) bool { return e.Kind == PeerEventLocalDeletesHeld }) {
		t.Errorf("no local-deletes-held event emitted: %+v", events)
	}
	if _, err := os.Lstat(filepath.Join(peerHome, "mem", "gone.txt")); err != nil {
		t.Errorf("unproven delete removed the peer copy: %v", err)
	}
	after, err := os.ReadFile(baselineFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("home baseline advanced while a delete was held")
	}
}

// TestPeerHomeTrackedSync_DryRunWritesNothing mirrors the #103 store guard for
// the tracked pass: a preview with real pending work writes nothing to the
// store, the local home, or the peer. The second arm runs the same fixture
// live, which is what makes the first arm non-vacuous.
func TestPeerHomeTrackedSync_DryRunWritesNothing(t *testing.T) {
	cfg, paths, localHome, peerHome := peerHomeTrackedFixture(t, "mem\n")
	writePeerHomeFile(t, localHome, "mem/keep.txt", "keep", peerHomeFixedTime)
	writePeerHomeFile(t, peerHome, "mem/keep.txt", "keep", peerHomeFixedTime)
	writePeerHomeFile(t, peerHome, "mem/gone.txt", "removed", peerHomeFixedTime)
	writePeerHomeFile(t, peerHome, "mem/peer-new.txt", "new", peerHomeFixedTime)
	seedPeerHomeBaseline(t, cfg, map[string]Fingerprint{
		"mem/keep.txt": peerHomeFP(t, localHome, "mem/keep.txt"),
		"mem/gone.txt": {Size: int64(len("removed")), Mtime: peerHomeFixedTime},
	})

	var out bytes.Buffer
	cfg.Out = &out
	complete, _, err := peerHomeTrackedSync(context.Background(),
		peerScheduleRunner(true), peerScheduleRunner(false), cfg, nil, true, false, false)
	if err != nil {
		t.Fatalf("peerHomeTrackedSync --dry-run: %v", err)
	}
	if !complete {
		t.Error("an authorized preview was reported incomplete")
	}
	// Non-vacuity: the preview really saw the tombstone and the peer create.
	if !strings.Contains(out.String(), "Delete: 1 tracked home path(s)") {
		t.Errorf("preview never reached the delete pass; the no-write assertions would be vacuous:\n%s", out.String())
	}

	if _, err := os.Lstat(filepath.Join(peerHome, "mem", "gone.txt")); err != nil {
		t.Errorf("dry-run deleted the peer copy: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(localHome, "mem", "peer-new.txt")); !os.IsNotExist(err) {
		t.Errorf("dry-run pulled into the local home: %v", err)
	}
	for _, home := range []string{localHome, peerHome} {
		if _, err := os.Lstat(filepath.Join(home, homeConflictDirName)); !os.IsNotExist(err) {
			t.Errorf("dry-run created a quarantine tree under %s: %v", home, err)
		}
	}
	baselineFile := peerHomeBaselineFile(paths)
	baseline, err := LoadBaselineManifest(baselineFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := baseline["mem/gone.txt"]; !ok {
		t.Error("dry-run retired the tombstone from the home baseline")
	}
	for _, name := range []string{"peer-conflicts.log", filepath.Base(cfg.LocalPaths.TombstonesFile)} {
		if _, err := os.Lstat(filepath.Join(paths.StoreDir, name)); !os.IsNotExist(err) {
			t.Errorf("dry-run left %s in the peer store: %v", name, err)
		}
	}
	dynFiles, err := filepath.Glob(filepath.Join(paths.StoreDir, "*.dyn"))
	if err != nil || len(dynFiles) != 0 {
		t.Errorf("dry-run left staged list files in the store: %v, %v", dynFiles, err)
	}
}

func TestPeerHomeDeletePassArgs_ScopedBackupIntoHomeConflictRoot(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Target = Target{Kind: TargetSSH, Host: "user@peer", Path: "/remote/work"}

	args := peerHomeDeletePassArgs(cfg, "/tmp/list", "/tmp/src", "2026-01-02T03-04-05.000000Z", false)

	for _, want := range []string{
		"--files-from=/tmp/list", "--from0", "--ignore-missing-args",
		"--delete-missing-args", "--backup",
		"--backup-dir=.dot-peer-conflicts/2026-01-02T03-04-05.000000Z/from-peer",
	} {
		if !slices.Contains(args, want) {
			t.Errorf("delete pass argv missing %q: %v", want, args)
		}
	}
	if got := args[len(args)-2:]; got[0] != "/tmp/src/" || got[1] != "user@peer:" {
		t.Errorf("delete pass endpoints = %v, want the staging root and the bare peer home", got)
	}
	if slices.Contains(args, "--update") || slices.Contains(args, "--delete") {
		t.Errorf("delete pass must stay scoped to its list, no --update or bare --delete: %v", args)
	}
}

func TestPeerHomeTransferArgs_NoUpdateScopedNulList(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Target = Target{Kind: TargetSSH, Host: "user@peer", Path: "/remote/work"}

	args := peerHomeTransferArgs(cfg, "/tmp/tracked.dyn", true)

	for _, want := range []string{"--files-from=/tmp/tracked.dyn", "--from0", "--ignore-missing-args", "--dry-run"} {
		if !slices.Contains(args, want) {
			t.Errorf("transfer argv missing %q: %v", want, args)
		}
	}
	// The home baseline, not mtime, arbitrates tracked paths; --update would
	// silently discard the older side of a dual edit.
	if slices.Contains(args, "--update") {
		t.Errorf("tracked transfers must not carry --update: %v", args)
	}
}

// TestPeerHomeSync_StripsTrackedEntries pins the split: an entry the tracked
// pass owns — by exact match or by nesting in either direction — is removed
// from the additive pass's --files-from, and with no overlap the original
// list is used byte-identically.
func TestPeerHomeSync_StripsTrackedEntries(t *testing.T) {
	_, target, cfg := homeFlagSandbox(t)
	if err := os.MkdirAll(cfg.LocalPaths.StoreDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg.ConfigDir = cfg.LocalPaths.StoreDir
	original := PeerHomePathsFile(cfg.LocalPaths)
	// .claude is an untracked ANCESTOR of the tracked memory entry: left in
	// place, the recursive additive pass would re-sync the tracked subtree.
	if err := os.WriteFile(original, []byte(".ssh\nmem\n.claude\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(PeerHomeTrackedFile(cfg.LocalPaths), []byte("mem\n.claude/projects/-x-y/memory\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	binDir := t.TempDir()
	argvLog := filepath.Join(binDir, "argv.log")
	writeStub(t, filepath.Join(binDir, "rsync"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> "+argvLog+"\n")
	t.Setenv("PATH", binDir)

	if err := peerHomeSync(context.Background(), peerScheduleRunner(false), cfg, nil, false, false, false); err != nil {
		t.Fatalf("peerHomeSync: %v", err)
	}
	logged, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatalf("the stub rsync was never invoked: %v", err)
	}
	argv := string(logged)
	if strings.Contains(argv, "--files-from="+original) {
		t.Errorf("the additive pass still read the unfiltered list:\n%s", argv)
	}
	marker := "--files-from="
	idx := strings.Index(argv, marker)
	if idx == -1 {
		t.Fatalf("no --files-from in argv:\n%s", argv)
	}
	dynPath := strings.Fields(argv[idx+len(marker):])[0]
	dynBody, err := os.ReadFile(dynPath)
	if err != nil {
		t.Fatalf("reading the filtered list: %v", err)
	}
	if !strings.Contains(string(dynBody), ".ssh") || strings.Contains(string(dynBody), "mem") ||
		strings.Contains(string(dynBody), ".claude") {
		t.Errorf("filtered list = %q, want only .ssh kept; tracked mem and ancestor .claude removed", dynBody)
	}

	// Non-vacuity: with no tracked list, the pass reads the original file.
	if err := os.Remove(PeerHomeTrackedFile(cfg.LocalPaths)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(argvLog); err != nil {
		t.Fatal(err)
	}
	if err := peerHomeSync(context.Background(), peerScheduleRunner(false), cfg, nil, false, false, false); err != nil {
		t.Fatalf("peerHomeSync without a tracked list: %v", err)
	}
	logged, err = os.ReadFile(argvLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logged), "--files-from="+original) {
		t.Errorf("without a tracked list the additive pass must read the original file:\n%s", logged)
	}
	if strings.Contains(string(logged), target+"/.dotfiles") && strings.Contains(string(logged), "untracked.dyn") {
		t.Errorf("without overlap a filtered list was still materialized:\n%s", logged)
	}
}

func TestReadPeerHomeTrackedEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "home-paths-tracked.txt")
	body := "# comment\n\n.claude/projects/-x-y/memory\n.claude/projects/-x-y/memory\nmem/\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := readPeerHomeTrackedEntries(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".claude/projects/-x-y/memory", "mem"}
	if !slices.Equal(entries, want) {
		t.Errorf("entries = %v, want %v (comments, blanks, duplicates, trailing slashes handled)", entries, want)
	}

	for _, bad := range []string{"../escape", "/abs/path", "has\x00nul", "x/../../y"} {
		if err := os.WriteFile(path, []byte(bad+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := readPeerHomeTrackedEntries(path); err == nil {
			t.Errorf("accepted unsafe tracked entry %q", bad)
		}
	}
}

func TestComputeHomeTombstones_RetiresEntriesRemovedFromList(t *testing.T) {
	baseline := map[string]Fingerprint{
		"mem/gone.txt":        {Size: 1, Mtime: peerHomeFixedTime},
		"old-list/retired.md": {Size: 1, Mtime: peerHomeFixedTime},
	}
	snapshot := PeerSnapshot{"mem/keep.txt": {Present: true}}

	tombstones, err := computeHomeTombstones(snapshot, baseline, []string{"mem"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(tombstones, []string{"mem/gone.txt"}) {
		t.Errorf("tombstones = %v, want only the deleted file under a current tracked entry", tombstones)
	}
}

func TestFilterUntrackedHomeEntries_DropsOverlapsBothDirections(t *testing.T) {
	tests := []struct {
		name     string
		additive []string
		tracked  []string
		want     []string
		changed  bool
	}{
		{"exact match dropped", []string{".ssh", "mem"}, []string{"mem"}, []string{".ssh"}, true},
		{"untracked ancestor of a tracked entry dropped", []string{".claude", ".ssh"}, []string{".claude/projects/-x-y/memory"}, []string{".ssh"}, true},
		{"untracked descendant of a tracked directory dropped", []string{".claude/settings.json", ".ssh"}, []string{".claude"}, []string{".ssh"}, true},
		{"shared prefix without a slash boundary kept", []string{".claude.json", ".ssh"}, []string{".claude"}, []string{".claude.json", ".ssh"}, false},
		{"no overlap keeps the list byte-identical", []string{".ssh", ".gitconfig"}, []string{"mem"}, []string{".ssh", ".gitconfig"}, false},
		{"trailing slash on the additive entry still matches", []string{"mem/"}, []string{"mem"}, nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			kept, changed := filterUntrackedHomeEntries(tc.additive, tc.tracked)
			if changed != tc.changed {
				t.Errorf("changed = %v, want %v", changed, tc.changed)
			}
			if !slices.Equal(kept, tc.want) {
				t.Errorf("kept = %v, want %v", kept, tc.want)
			}
		})
	}
}

func TestPeerSchedule_SeedsTrackedHomePathsOnce(t *testing.T) {
	cfg, _ := peerScheduleSandbox(t)
	cfg.LocalPaths = ResolveLocalPathsForProfile(cfg.LocalPath, PeerProfile)
	tracked := PeerHomeTrackedFile(cfg.LocalPaths)

	// A preview seeds nothing.
	res, err := PeerSchedule(context.Background(), PeerScheduleOptions{
		Config: cfg, Runner: peerScheduleRunner(false), Probe: peerScheduleRunner(false),
		Interval: time.Hour, DryRun: true,
	})
	if err != nil {
		t.Fatalf("PeerSchedule dry-run: %v", err)
	}
	if !res.DryRun {
		t.Fatal("expected a preview result")
	}
	if _, statErr := os.Lstat(tracked); !os.IsNotExist(statErr) {
		t.Fatalf("dry-run seeded the tracked list: %v", statErr)
	}

	// The sandbox target path differs from the local workspace, so the seed
	// must be the header without the derived Claude memory entry.
	res, err = PeerSchedule(context.Background(), PeerScheduleOptions{
		Config: cfg, Runner: peerScheduleRunner(false), Probe: peerScheduleRunner(false), Interval: time.Hour,
	})
	var targetUserErr *SchedulerTargetUserActionRequiredError
	if err != nil && !errors.As(err, &targetUserErr) {
		t.Fatalf("PeerSchedule: %v", err)
	}
	if res == nil {
		t.Fatal("PeerSchedule returned no staged result")
	}
	if res.SeededHomeTrackedFile != tracked {
		t.Errorf("SeededHomeTrackedFile = %q, want %q", res.SeededHomeTrackedFile, tracked)
	}
	body, err := os.ReadFile(tracked)
	if err != nil {
		t.Fatalf("setup did not seed the tracked list: %v", err)
	}
	if !strings.Contains(string(body), "# dot peer home-paths-tracked.txt") {
		t.Errorf("seeded tracked list lost its header:\n%s", body)
	}
	if strings.Contains(string(body), "/memory") {
		t.Errorf("asymmetric workspace paths seeded a memory entry that would be wrong on one machine:\n%s", body)
	}

	// Seed-once: an operator's edit survives a refresh.
	if err := os.WriteFile(tracked, []byte("custom\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err = PeerSchedule(context.Background(), PeerScheduleOptions{
		Config: cfg, Runner: peerScheduleRunner(false), Probe: peerScheduleRunner(false), Interval: time.Hour,
	})
	if err != nil && !errors.As(err, &targetUserErr) {
		t.Fatalf("PeerSchedule re-run: %v", err)
	}
	if res.SeededHomeTrackedFile != "" {
		t.Errorf("re-run reported a seed over an existing file: %q", res.SeededHomeTrackedFile)
	}
	body, err = os.ReadFile(tracked)
	if err != nil || string(body) != "custom\n" {
		t.Errorf("re-run rewrote the tracked list: %q, %v", body, err)
	}
}

func TestPeerHomeTrackedSeed_DerivesClaudeMemoryDir(t *testing.T) {
	cfg := &Config{LocalPath: "/Users/yj.lee/workspace/work/"}
	seed := peerHomeTrackedSeed(cfg, "/Users/yj.lee/workspace/work")
	want := ".claude/projects/-Users-yj-lee-workspace-work/memory\n"
	if !strings.HasSuffix(seed, want) {
		t.Errorf("seed does not end with the derived memory entry %q:\n%s", want, seed)
	}
	if !strings.Contains(seed, "#") {
		t.Error("seed lost its documentation header")
	}

	// A peer with a different workspace path flattens to a different Claude
	// project directory, so the entry would name the wrong directory on one
	// machine: seed the header only.
	asymmetric := peerHomeTrackedSeed(cfg, "/Users/other/workspace/work")
	if strings.Contains(asymmetric, "/memory") {
		t.Errorf("asymmetric workspace paths still seeded the derived entry:\n%s", asymmetric)
	}
	if !strings.Contains(asymmetric, "add entries by hand") {
		t.Errorf("asymmetric seed does not explain how to add entries:\n%s", asymmetric)
	}
}

func TestPeerInit_AsymmetricRemotePathSeedsHeaderOnly(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.LocalPath = "/tmp/test-local/"

	res, err := PeerInit(PeerInitOptions{Config: cfg, Host: "user@peer", RemotePath: "/different/workspace"})
	if err != nil {
		t.Fatalf("PeerInit: %v", err)
	}
	body, err := os.ReadFile(res.HomeTrackedFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), ".claude/projects/") {
		t.Errorf("asymmetric remote path seeded a memory entry that would be wrong on one machine:\n%s", body)
	}
}

func TestPeerInit_SeedsTrackedHomePathsOnce(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.LocalPath = "/tmp/test-local/"

	res, err := PeerInit(PeerInitOptions{Config: cfg, Host: "user@peer"})
	if err != nil {
		t.Fatalf("PeerInit: %v", err)
	}
	if res.HomeTrackedFile != PeerHomeTrackedFile(cfg.LocalPaths) {
		t.Errorf("HomeTrackedFile = %q, want %q", res.HomeTrackedFile, PeerHomeTrackedFile(cfg.LocalPaths))
	}
	body, err := os.ReadFile(res.HomeTrackedFile)
	if err != nil {
		t.Fatalf("tracked list was not seeded: %v", err)
	}
	if !strings.Contains(string(body), ".claude/projects/-tmp-test-local/memory\n") {
		t.Errorf("seeded tracked list lacks the derived memory entry:\n%s", body)
	}

	// Seed-once: an operator's edit survives a re-init.
	if err := os.WriteFile(res.HomeTrackedFile, []byte("custom\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := PeerInit(PeerInitOptions{Config: cfg, Host: "user@peer"}); err != nil {
		t.Fatal(err)
	}
	body, err = os.ReadFile(res.HomeTrackedFile)
	if err != nil || string(body) != "custom\n" {
		t.Errorf("re-init rewrote the tracked list: %q, %v", body, err)
	}
}

// TestPeerSync_DryRunHoldsTrackedHomeWrites wires the tracked pass into the
// full run: a `peer sync --dry-run` with pending tracked-home work writes
// nothing to the store, the local home, the peer home, or the workspace.
func TestPeerSync_DryRunHoldsTrackedHomeWrites(t *testing.T) {
	cfg, paths, localHome, peerHome := peerHomeTrackedFixture(t, "mem\n")
	local := strings.TrimRight(cfg.LocalPath, "/")
	peerWS := cfg.Target.Path
	if err := os.WriteFile(filepath.Join(peerWS, "peer-only.txt"), []byte("peer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "local-only.txt"), []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writePeerHomeFile(t, localHome, "mem/keep.txt", "keep", peerHomeFixedTime)
	writePeerHomeFile(t, peerHome, "mem/keep.txt", "keep", peerHomeFixedTime)
	writePeerHomeFile(t, peerHome, "mem/gone.txt", "removed", peerHomeFixedTime)
	writePeerHomeFile(t, peerHome, "mem/peer-new.txt", "new", peerHomeFixedTime)
	seedPeerHomeBaseline(t, cfg, map[string]Fingerprint{
		"mem/keep.txt": peerHomeFP(t, localHome, "mem/keep.txt"),
		"mem/gone.txt": {Size: int64(len("removed")), Mtime: peerHomeFixedTime},
	})
	baselineFile := peerHomeBaselineFile(paths)
	baselineBefore, err := os.ReadFile(baselineFile)
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cfg.Out = &out
	res, err := PeerSync(context.Background(), PeerSyncOptions{
		Config:   cfg,
		Runner:   peerScheduleRunner(true),
		Probe:    peerScheduleRunner(false),
		DryRun:   true,
		Progress: nil,
	})
	if err != nil {
		t.Fatalf("PeerSync --dry-run: %v", err)
	}
	if res.Unreachable {
		t.Fatal("fake peer reported unreachable, so the run never planned")
	}
	if !res.Complete {
		t.Error("an authorized preview was reported incomplete")
	}
	// Non-vacuity: the tracked delete and the peer's create were really seen.
	if !strings.Contains(out.String(), "Delete: 1 tracked home path(s)") {
		t.Errorf("the tracked delete preview never ran; the no-write assertions would be vacuous:\n%s", out.String())
	}

	if _, err := os.Lstat(filepath.Join(peerHome, "mem", "gone.txt")); err != nil {
		t.Errorf("dry-run deleted the tracked peer copy: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(localHome, "mem", "peer-new.txt")); !os.IsNotExist(err) {
		t.Errorf("dry-run pulled into the local home: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(local, "peer-only.txt")); !os.IsNotExist(err) {
		t.Errorf("dry-run pulled into the workspace: %v", err)
	}
	for _, home := range []string{localHome, peerHome} {
		if _, err := os.Lstat(filepath.Join(home, homeConflictDirName)); !os.IsNotExist(err) {
			t.Errorf("dry-run created a quarantine tree under %s: %v", home, err)
		}
	}
	baselineAfter, err := os.ReadFile(baselineFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(baselineBefore, baselineAfter) {
		t.Error("dry-run rewrote the home baseline")
	}
	for _, name := range []string{
		"peer-conflicts.log",
		filepath.Base(cfg.LocalPaths.TombstonesFile),
		filepath.Base(cfg.LocalPaths.BaselineFile),
		peerBaselineTargetName,
		homeBaselineTargetName + ".tmp",
	} {
		if _, err := os.Lstat(filepath.Join(paths.StoreDir, name)); !os.IsNotExist(err) {
			t.Errorf("dry-run left %s in the peer store: %v", name, err)
		}
	}
	dynFiles, err := filepath.Glob(filepath.Join(paths.StoreDir, "*.dyn"))
	if err != nil || len(dynFiles) != 0 {
		t.Errorf("dry-run left staged list files in the store: %v, %v", dynFiles, err)
	}
}
