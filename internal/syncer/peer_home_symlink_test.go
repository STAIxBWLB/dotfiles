package syncer

import (
	"context"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePeerHomeLink(t *testing.T, home, rel, target string) {
	t.Helper()
	path := filepath.Join(home, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}

func TestPeerHomeSymlinkInventory(t *testing.T) {
	home := t.TempDir()
	writePeerHomeLink(t, home, "skills/dangling", "missing")
	snap, err := inventoryPeerHomeTracked(home, []string{"skills"})
	if err != nil {
		t.Fatal(err)
	}
	if !snap["skills/dangling"].Present {
		t.Fatal("dangling symlink missing from home inventory")
	}
}

func TestPeerHomeSymlinkDelete(t *testing.T) {
	cfg, _, local, peer := peerHomeTrackedFixture(t, "skills\n")
	writePeerHomeFile(t, local, "skills/keep", "keep", peerHomeFixedTime)
	writePeerHomeFile(t, peer, "skills/keep", "keep", peerHomeFixedTime)
	writePeerHomeLink(t, local, "skills/tool", "missing")
	writePeerHomeLink(t, peer, "skills/tool", "missing")
	if complete, _, err := peerHomeTrackedSync(context.Background(), peerScheduleRunner(false), peerScheduleRunner(false), cfg, nil, false, false, false); err != nil || !complete {
		t.Fatalf("bootstrap: %v, complete=%v", err, complete)
	}
	if err := os.Remove(filepath.Join(local, "skills/tool")); err != nil {
		t.Fatal(err)
	}
	if complete, _, err := peerHomeTrackedSync(context.Background(), peerScheduleRunner(false), peerScheduleRunner(false), cfg, nil, false, false, false); err != nil || !complete {
		t.Fatalf("delete: %v, complete=%v", err, complete)
	}
	if _, err := os.Lstat(filepath.Join(peer, "skills/tool")); !os.IsNotExist(err) {
		t.Fatalf("deleted link still present on peer: %v", err)
	}
	tombs, err := LoadTombstones(cfg.LocalPaths.TombstonesFile)
	if err != nil || len(tombs) != 1 || tombs[0].RelPath != "skills/tool" {
		t.Fatalf("tombstones: %+v, %v", tombs, err)
	}
	if target, err := os.Readlink(singleQuarantinedHomeFile(t, peer, "skills/tool")); err != nil || target != "missing" {
		t.Fatalf("quarantined link=%q, %v", target, err)
	}
}

func TestPeerHomeSymlinkCreateRetargetConflict(t *testing.T) {
	cfg, _, local, peer := peerHomeTrackedFixture(t, "skills\n")
	rel := "skills/tool -> named"
	writePeerHomeLink(t, local, rel, "first target -> path")
	run := func() *homeTrackedPlan {
		t.Helper()
		complete, plan, err := peerHomeTrackedSync(context.Background(), peerScheduleRunner(false), peerScheduleRunner(false), cfg, nil, false, false, false)
		if err != nil || !complete {
			t.Fatalf("sync: %v, complete=%v", err, complete)
		}
		return plan
	}
	check := func(home, want string) {
		t.Helper()
		target, err := os.Readlink(filepath.Join(home, rel))
		if err != nil || target != want {
			t.Fatalf("link=%q, %v, want %q", target, err, want)
		}
	}
	run()
	check(peer, "first target -> path")
	writePeerHomeLink(t, local, rel, "local target -> path")
	run()
	check(peer, "local target -> path")
	writePeerHomeLink(t, peer, rel, "other target -> path")
	run()
	check(local, "other target -> path")
	writePeerHomeLink(t, local, rel, "local wins")
	writePeerHomeLink(t, peer, rel, "peer loses")
	tp := run()
	if len(tp.plan.Conflicts) != 1 {
		t.Fatalf("conflicts=%+v", tp.plan.Conflicts)
	}
	check(peer, "local wins")
	if target, err := os.Readlink(singleQuarantinedHomeFile(t, peer, rel)); err != nil || target != "peer loses" {
		t.Fatalf("quarantined link=%q, %v", target, err)
	}
}

func TestPeerHomeSymlinkPeerDelete(t *testing.T) {
	cfg, _, local, peer := peerHomeTrackedFixture(t, "skills\n")
	for _, home := range []string{local, peer} {
		writePeerHomeFile(t, home, "skills/keep", "keep", peerHomeFixedTime)
		writePeerHomeLink(t, home, "skills/tool", "missing")
	}
	for i := 0; i < 2; i++ {
		complete, _, err := peerHomeTrackedSync(context.Background(), peerScheduleRunner(false), peerScheduleRunner(false), cfg, nil, false, false, false)
		if err != nil || !complete {
			t.Fatalf("cycle %d: %v, complete=%v", i, err, complete)
		}
		if i == 0 {
			if err := os.Remove(filepath.Join(peer, "skills/tool")); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := os.Lstat(filepath.Join(local, "skills/tool")); !os.IsNotExist(err) {
		t.Fatalf("peer-deleted link still local: %v", err)
	}
	if target, err := os.Readlink(singleQuarantinedHomeFile(t, local, "skills/tool")); err != nil || target != "missing" {
		t.Fatalf("quarantined link=%q, %v", target, err)
	}
}

func TestPeerHomeSymlinkNoTraversal(t *testing.T) {
	home, outside := t.TempDir(), t.TempDir()
	writePeerHomeFile(t, outside, "keep", "outside", peerHomeFixedTime)
	writePeerHomeLink(t, home, "skills/directory", outside)
	writePeerHomeLink(t, home, "direct", outside)
	snap, err := inventoryPeerHomeTracked(home, []string{"skills", "direct"})
	if err != nil || len(snap) != 2 || !snap["direct"].Present || !snap["skills/directory"].Present {
		t.Fatalf("link inventory=%+v, %v", snap, err)
	}
	if _, err := inventoryPeerHomeTracked(home, []string{"skills/directory/keep"}); err == nil {
		t.Fatal("tracked entry traversed symlink parent")
	}
	cfg := &Config{Home: home}
	if err := deletePeerHomeLocal(cfg, "2026-01-02T03-04-05.000000Z", []string{"skills/directory/keep"}, false); err == nil {
		t.Fatal("local delete traversed symlink parent")
	}
	if body, err := os.ReadFile(filepath.Join(outside, "keep")); err != nil || string(body) != "outside" {
		t.Fatalf("outside target changed=%q, %v", body, err)
	}
}

func TestPeerHomeSymlinkRemoteAncestorRefusal(t *testing.T) {
	cfg, _, _, peer := peerHomeTrackedFixture(t, "skills/sub\n")
	outside := t.TempDir()
	writePeerHomeFile(t, outside, "sub/file", "outside", peerHomeFixedTime)
	writePeerHomeLink(t, peer, "skills", outside)
	if _, err := peerHomeRemoteInventory(context.Background(), peerScheduleRunner(false), cfg, []string{"skills/sub"}, nil, false); err == nil {
		t.Fatal("remote inventory traversed symlink ancestor")
	}
	if err := pushPeerHomePass(context.Background(), peerScheduleRunner(false), cfg, []string{"skills/sub/file"}, "2026-01-02T03-04-05.000000Z", false, false); err == nil {
		t.Fatal("remote push traversed symlink ancestor")
	}
}

func TestPeerHomeSymlinkFingerprintsAndMigration(t *testing.T) {
	a := fingerprintLinkTarget("target-a", peerHomeFixedTime)
	b := fingerprintLinkTarget("target-b", peerHomeFixedTime)
	if peerFingerprintEqual(a, b) {
		t.Fatal("same-size same-mtime retarget undetected")
	}
	if peerFingerprintEqual(a, Fingerprint{Size: a.Size, Mtime: a.Mtime}) {
		t.Fatal("link aliases regular file")
	}
	for _, fp := range []Fingerprint{a, b} {
		got, err := DecodeCompactFingerprint(fp.CompactEncode())
		if err != nil || got != fp {
			t.Fatalf("fingerprint roundtrip=%+v, %v", got, err)
		}
	}
	cfg, _, local, peer := peerHomeTrackedFixture(t, "skills\n")
	writePeerHomeFile(t, local, "skills/keep", "keep", peerHomeFixedTime)
	writePeerHomeFile(t, peer, "skills/keep", "keep", peerHomeFixedTime)
	// Simulate an existing regular-only home baseline and a link missing locally
	// before the upgrade. With no prior link entry, this is a peer-only create.
	seedPeerHomeBaseline(t, cfg, map[string]Fingerprint{"skills/keep": peerHomeFP(t, local, "skills/keep")})
	writePeerHomeLink(t, peer, "skills/old", "missing")
	complete, tp, err := peerHomeTrackedSync(context.Background(), peerScheduleRunner(false), peerScheduleRunner(false), cfg, nil, false, false, false)
	if err != nil || !complete || len(tp.plan.DeleteLocal)+len(tp.plan.DeleteRemote) != 0 {
		t.Fatalf("migration: %+v, %v, complete=%v", tp, err, complete)
	}
	baseline, err := LoadBaselineManifest(peerHomeBaselineFile(cfg.LocalPaths))
	if err != nil || baseline["skills/old"].Sha == "" {
		t.Fatalf("migration baseline=%+v, %v", baseline, err)
	}
	if target, err := os.Readlink(filepath.Join(local, "skills/old")); err != nil || target != "missing" {
		t.Fatalf("historical absence should import peer link=%q, %v", target, err)
	}
}

func TestPeerHomeSymlinkDeleteCap(t *testing.T) {
	cfg, _, local, peer := peerHomeTrackedFixture(t, "skills\n")
	cfg.MaxDelete = 1
	for _, home := range []string{local, peer} {
		for _, rel := range []string{"skills/a", "skills/b", "skills/c", "skills/d"} {
			writePeerHomeLink(t, home, rel, "missing")
		}
	}
	complete, _, err := peerHomeTrackedSync(context.Background(), peerScheduleRunner(false), peerScheduleRunner(false), cfg, nil, false, false, false)
	if err != nil || !complete {
		t.Fatalf("bootstrap: %v", err)
	}
	for _, rel := range []string{"skills/a", "skills/b"} {
		if err := os.Remove(filepath.Join(local, rel)); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := peerHomeTrackedSync(context.Background(), peerScheduleRunner(false), peerScheduleRunner(false), cfg, nil, false, false, false); err == nil {
		t.Fatal("symlink deletes bypassed max_delete")
	}
	for _, rel := range []string{"skills/a", "skills/b"} {
		if _, err := os.Lstat(filepath.Join(peer, rel)); err != nil {
			t.Fatalf("cap refusal removed peer link: %v", err)
		}
	}
}

func TestPeerHomeSymlinkRemoteParserRejectsAmbiguity(t *testing.T) {
	for _, raw := range []string{
		"@@cL+++++++++\t7\t2026/02/03-04:05:06\tskills/link\t -> bad\\#012target",
		"@@cL+++++++++\t7\t2026/02/03-04:05:06\tskills/link\t -> bad\ttarget",
		"@@cL+++++++++\t7\t2026/02/03-04:05:06\tskills/link\tinvalid",
	} {
		if _, err := parsePeerHomeRemoteInventory(raw, nil); err == nil {
			t.Fatalf("ambiguous record accepted: %q", raw)
		}
	}
}

func TestPeerHomeSymlinkRemoteParserNonRegularWarnings(t *testing.T) {
	warnings := "skipping non-regular file \"skills/fifo\"\nskipping non-regular file \"skills/socket\"\n"
	file := "@@>f+++++++++\t4\t2026/02/03-04:05:06\tskills/keep\t\n"
	snap, err := parsePeerHomeRemoteInventory(warnings+file, nil)
	if err != nil || len(snap) != 1 || !snap["skills/keep"].Present {
		t.Fatalf("unknown non-regular inventory=%+v, %v", snap, err)
	}
	for _, rel := range []string{"skills/fifo", "skills/socket"} {
		if _, err := parsePeerHomeRemoteInventory(warnings+file, map[string]Fingerprint{rel: {Size: 4}}); err == nil || !strings.Contains(err.Error(), "non-regular on the peer") {
			t.Fatalf("baseline transition %q accepted: %v", rel, err)
		}
	}
	if _, err := parsePeerHomeRemoteInventory("unexpected stdout\n"+file, nil); err == nil {
		t.Fatal("unexpected stdout accepted")
	}
}

func TestPeerHomeSymlinkRemoteInventoryIgnoresUnknownFIFO(t *testing.T) {
	cfg, _, _, peer := peerHomeTrackedFixture(t, "skills\n")
	writePeerHomeFile(t, peer, "skills/keep", "keep", peerHomeFixedTime)
	path := filepath.Join(peer, "skills/fifo")
	if err := unix.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	snap, err := peerHomeRemoteInventory(context.Background(), peerScheduleRunner(false), cfg, []string{"skills"}, nil, false)
	if err != nil || len(snap) != 1 || !snap["skills/keep"].Present {
		t.Fatalf("actual-rsync FIFO inventory=%+v, %v", snap, err)
	}
	if _, err := peerHomeRemoteInventory(context.Background(), peerScheduleRunner(false), cfg, []string{"skills"}, map[string]Fingerprint{"skills/fifo": {Size: 4}}, false); err == nil || !strings.Contains(err.Error(), "non-regular on the peer") {
		t.Fatalf("actual-rsync baseline FIFO transition accepted: %v", err)
	}
}
