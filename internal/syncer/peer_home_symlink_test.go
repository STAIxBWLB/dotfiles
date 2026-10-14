package syncer

import (
	"context"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
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
	if err := deletePeerHomeLocal(cfg, "2026-01-02T03-04-05.000000Z", []string{"skills/directory/keep"}, false, nil); err == nil {
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

func TestPeerHomeAncestorCommandsLargeFlatTree(t *testing.T) {
	var rels []string
	for i := 0; i < 10000; i++ {
		rels = append(rels, fmt.Sprintf("skills/%s-%05d", strings.Repeat("f", 40), i))
	}
	if len(strings.Join(rels, " ")) < 128*1024 {
		t.Fatal("fixture must exceed a single-argument limit")
	}
	commands, err := peerHomeAncestorCommands(rels)
	if err != nil || len(commands) != 1 {
		t.Fatalf("flat tree commands=%d, %v", len(commands), err)
	}
	if len(commands[0]) > peerHomePreflightCommandLimit || !strings.HasSuffix(commands[0], " "+shellQuote(rels[0])) {
		t.Fatalf("flat parent did not use a bounded representative: %s", commands[0])
	}
	// Even a duplicate-parent input must still have its own path validated.
	if _, err := peerHomeAncestorCommands(append(rels, "skills/../escape")); err == nil {
		t.Fatal("deduplication bypassed path validation")
	}
}

func TestPeerHomeAncestorCommandsBoundDistinctParents(t *testing.T) {
	var rels []string
	for i := 0; i < 1500; i++ {
		parent := fmt.Sprintf("skills/parent-%04d-%s'quoted", i, strings.Repeat("p", 90))
		rels = append(rels, parent+"/first", parent+"/second")
	}
	commands, err := peerHomeAncestorCommands(rels)
	if err != nil || len(commands) < 2 {
		t.Fatalf("distinct-parent batches=%d, %v", len(commands), err)
	}
	joined := strings.Join(commands, "\n")
	for _, command := range commands {
		if len(command) > peerHomePreflightCommandLimit {
			t.Fatalf("command exceeds bound: %d", len(command))
		}
	}
	for i, rel := range rels {
		count := strings.Count(joined, " "+shellQuote(rel))
		want := 1
		if i%2 == 1 {
			want = 0
		}
		if count != want {
			t.Fatalf("parent representative coverage %q=%d, want%d", rel, count, want)
		}
	}
	if _, err := peerHomeAncestorCommands([]string{"skills/" + strings.Repeat("p", peerHomePreflightCommandLimit) + "/file"}); err == nil {
		t.Fatal("oversized single argument accepted")
	}
}

func TestPeerHomeAncestorPreflightLargeTrees(t *testing.T) {
	cfg, _, _, peer := peerHomeTrackedFixture(t, "skills\n")
	writePeerHomeFile(t, peer, "skills/keep", "keep", peerHomeFixedTime)
	baseline := map[string]Fingerprint{}
	for i := 0; i < 10000; i++ {
		baseline[fmt.Sprintf("skills/%s-%05d", strings.Repeat("f", 40), i)] = Fingerprint{Size: 4}
	}
	snap, err := peerHomeRemoteInventory(context.Background(), peerScheduleRunner(false), cfg, []string{"skills"}, baseline, false)
	if err != nil || !snap["skills/keep"].Present {
		t.Fatalf("actual-rsync large flat baseline inventory=%+v, %v", snap, err)
	}
	var rels []string
	for i := 0; i < 1500; i++ {
		rels = append(rels, fmt.Sprintf("skills/parent-%04d-%s/file", i, strings.Repeat("p", 90)))
	}
	if err := preflightPeerHomeAncestors(context.Background(), peerScheduleRunner(false), cfg, rels); err != nil {
		t.Fatalf("safe distinct-parent batches: %v", err)
	}
	// The unsafe parent is deliberately in the final batch. The later check
	// must still run, even when all prior batches successfully inspected home.
	unsafe := filepath.Dir(rels[len(rels)-1])
	writePeerHomeLink(t, peer, unsafe, t.TempDir())
	if err := preflightPeerHomeAncestors(context.Background(), peerScheduleRunner(false), cfg, rels); err == nil {
		t.Fatal("late-batch symlink ancestor bypassed safety coverage")
	}
}

func TestPeerHomeTombstoneWriteFailurePrecedesDelete(t *testing.T) {
	cfg, _, local, peer := peerHomeTrackedFixture(t, "skills\n")
	for _, home := range []string{local, peer} {
		writePeerHomeFile(t, home, "skills/keep", "keep", peerHomeFixedTime)
		writePeerHomeLink(t, home, "skills/tool", "missing")
	}
	run := func() error {
		_, _, err := peerHomeTrackedSync(context.Background(), peerScheduleRunner(false), peerScheduleRunner(false), cfg, nil, false, false, false)
		return err
	}
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(local, "skills/tool")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(cfg.LocalPaths.TombstonesFile, 0700); err != nil {
		t.Fatal(err)
	}
	if err := run(); err == nil || !strings.Contains(err.Error(), "recording home tombstones") {
		t.Fatalf("audit write failure=%v", err)
	}
	if target, err := os.Readlink(filepath.Join(peer, "skills/tool")); err != nil || target != "missing" {
		t.Fatalf("unrecorded peer deletion occurred: %q, %v", target, err)
	}
	if _, err := os.Lstat(filepath.Join(peer, homeConflictDirName)); !os.IsNotExist(err) {
		t.Fatalf("audit failure still created peer quarantine: %v", err)
	}
	baseline, err := LoadBaselineManifest(peerHomeBaselineFile(cfg.LocalPaths))
	if err != nil || baseline["skills/tool"].Sha == "" {
		t.Fatalf("audit failure retired baseline evidence=%+v, %v", baseline, err)
	}
	if err := os.Remove(cfg.LocalPaths.TombstonesFile); err != nil {
		t.Fatal(err)
	}
	if err := run(); err != nil {
		t.Fatalf("retry: %v", err)
	}
	tombs, err := LoadTombstones(cfg.LocalPaths.TombstonesFile)
	if err != nil || len(tombs) != 1 || tombs[0].RelPath != "skills/tool" {
		t.Fatalf("retry tombstones=%+v, %v", tombs, err)
	}
}

func TestPeerHomeTombstoneRetainedWhenDeleteFails(t *testing.T) {
	cfg, _, local, peer := peerHomeTrackedFixture(t, "skills\n")
	for _, home := range []string{local, peer} {
		writePeerHomeFile(t, home, "skills/keep", "keep", peerHomeFixedTime)
		writePeerHomeLink(t, home, "skills/tool", "missing")
	}
	run := func() error {
		_, _, err := peerHomeTrackedSync(context.Background(), peerScheduleRunner(false), peerScheduleRunner(false), cfg, nil, false, false, false)
		return err
	}
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(local, "skills/tool")); err != nil {
		t.Fatal(err)
	}
	originalPath := os.Getenv("PATH")
	rsync := cfg.rsyncBin()
	// Config resolves the binary through PATH, so pin the real path before
	// placing a failure stub ahead of it.
	realRsync, err := osexec.LookPath(rsync)
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	writeStub(t, filepath.Join(bin, "rsync"), "#!/bin/sh\ncase \"$*\" in *--delete-missing-args*) exit 23;; esac\nexec "+shellQuote(realRsync)+" \"$@\"\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+originalPath)
	if err := run(); err == nil {
		t.Fatal("delete failure not propagated")
	}
	tombs, err := LoadTombstones(cfg.LocalPaths.TombstonesFile)
	if err != nil || len(tombs) != 1 || tombs[0].RelPath != "skills/tool" {
		t.Fatalf("failed delete lacks durable detected evidence=%+v, %v", tombs, err)
	}
	baseline, err := LoadBaselineManifest(peerHomeBaselineFile(cfg.LocalPaths))
	if err != nil || baseline["skills/tool"].Sha == "" {
		t.Fatalf("failed delete retired baseline=%+v, %v", baseline, err)
	}
	t.Setenv("PATH", originalPath)
	if err := run(); err != nil {
		t.Fatalf("delete retry: %v", err)
	}
	tombs, err = LoadTombstones(cfg.LocalPaths.TombstonesFile)
	if err != nil || len(tombs) < 1 || tombs[0].RelPath != "skills/tool" {
		t.Fatalf("retry lost detected evidence=%+v, %v", tombs, err)
	}
}

func TestPeerHomeLocalRetargetAfterPlanRefused(t *testing.T) {
	cfg, _, local, peer := peerHomeTrackedFixture(t, "skills\n")
	writePeerHomeLink(t, local, "skills/tool", "original")
	writePeerHomeLink(t, peer, "skills/tool", "original")
	seedPeerHomeBaseline(t, cfg, map[string]Fingerprint{"skills/tool": peerHomeFP(t, local, "skills/tool")})
	writePeerHomeLink(t, peer, "skills/tool", "peer edit")
	tp, err := planPeerHomeTracked(context.Background(), peerScheduleRunner(false), cfg, nil, false)
	if err != nil || len(tp.plan.Pull) != 1 {
		t.Fatalf("pull plan=%+v, %v", tp, err)
	}
	// Retarget during the remote ancestor probe, after planning and before
	// pull's final destination validation.
	originalSSH, err := osexec.LookPath("ssh")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	path := filepath.Join(local, "skills/tool")
	writeStub(t, filepath.Join(bin, "ssh"), "#!/bin/sh\ncase \"$*\" in *\"unsafe peer home ancestor\"*) rm -- "+shellQuote(path)+"; ln -s 'late edit' "+shellQuote(path)+";; esac\nexec "+shellQuote(originalSSH)+" \"$@\"\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := pullPeerHomePaths(context.Background(), peerScheduleRunner(false), cfg, tp.plan, false); err == nil || !strings.Contains(err.Error(), "changed after planning") {
		t.Fatalf("late retarget overwrite not refused: %v", err)
	}
	if target, err := os.Readlink(filepath.Join(local, "skills/tool")); err != nil || target != "late edit" {
		t.Fatalf("late edit overwritten=%q, %v", target, err)
	}
	if err := deletePeerHomeLocal(cfg, "2026-01-02T03-04-05.000000Z", []string{"skills/tool"}, false, tp.plan.LocalBefore); err == nil || !strings.Contains(err.Error(), "changed after planning") {
		t.Fatalf("late retarget deletion not refused: %v", err)
	}
}

func TestPeerHomeTopologyConflictsRefusedBeforeEffects(t *testing.T) {
	for _, localLink := range []bool{true, false} {
		t.Run(fmt.Sprintf("local-link=%v", localLink), func(t *testing.T) {
			cfg, _, local, peer := peerHomeTrackedFixture(t, "skills\n")
			linkHome, dirHome := local, peer
			if !localLink {
				linkHome, dirHome = peer, local
			}
			writePeerHomeLink(t, linkHome, "skills/tool", "missing")
			writePeerHomeFile(t, dirHome, "skills/tool/keep", "must survive", peerHomeFixedTime)
			// A nearby sortable sibling must not hide the descendant search.
			writePeerHomeFile(t, local, "skills/tool.", "nearby", peerHomeFixedTime)
			if _, _, err := peerHomeTrackedSync(context.Background(), peerScheduleRunner(false), peerScheduleRunner(false), cfg, nil, false, false, false); err == nil || !strings.Contains(err.Error(), "topology") {
				t.Fatalf("topology conflict not refused: %v", err)
			}
			if body, err := os.ReadFile(filepath.Join(dirHome, "skills/tool/keep")); err != nil || string(body) != "must survive" {
				t.Fatalf("populated directory mutated=%q, %v", body, err)
			}
			if target, err := os.Readlink(filepath.Join(linkHome, "skills/tool")); err != nil || target != "missing" {
				t.Fatalf("link mutated=%q, %v", target, err)
			}
		})
	}
}

func TestPeerHomePreflightBoundsConnectionAndDeadline(t *testing.T) {
	bin := t.TempDir()
	log := filepath.Join(bin, "argv")
	writeStub(t, filepath.Join(bin, "ssh"), "#!/bin/sh\nprintf '%s\\n' \"$@\" > "+shellQuote(log)+"\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfg := &Config{Target: Target{Kind: TargetSSH, Host: "fake-peer"}}
	if err := runPeerHomePreflight(context.Background(), peerScheduleRunner(false), cfg, "true"); err != nil {
		t.Fatal(err)
	}
	argv, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, option := range []string{"ConnectTimeout=5", "ServerAliveInterval=5", "ServerAliveCountMax=2"} {
		if !strings.Contains(string(argv), option+"\n") {
			t.Fatalf("bounded probe missing %q: %s", option, argv)
		}
	}
	writeStub(t, filepath.Join(bin, "ssh"), "#!/bin/sh\nexec /bin/sleep 2\n")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := runPeerHomePreflight(ctx, peerScheduleRunner(false), cfg, "true"); err == nil || time.Since(start) > time.Second {
		t.Fatalf("probe did not honor caller deadline: %v, elapsed=%v", err, time.Since(start))
	}
}

func TestPeerHomeEscapedSymlinkTargetsActualRsync(t *testing.T) {
	cfg, _, local, peer := peerHomeTrackedFixture(t, "skills\n")
	targets := []string{"has\ttab", "has\nnewline", "has\rcarriage", `literal\#012target`, `literal\#12`, "대상/도구"}
	for i, target := range targets {
		writePeerHomeLink(t, peer, fmt.Sprintf("skills/link%d", i), target)
	}
	snap, err := peerHomeRemoteInventory(context.Background(), peerScheduleRunner(false), cfg, []string{"skills"}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	for i, target := range targets {
		rel := fmt.Sprintf("skills/link%d", i)
		want := fingerprintLinkTarget(target, peerHomeFixedTime)
		if snap[rel].FP.Sha != want.Sha || snap[rel].FP.Size != want.Size {
			t.Fatalf("target %q fingerprint=%+v, want=%+v", target, snap[rel].FP, want)
		}
	}
	if complete, _, err := peerHomeTrackedSync(context.Background(), peerScheduleRunner(false), peerScheduleRunner(false), cfg, nil, false, false, false); err != nil || !complete {
		t.Fatalf("escaped-target pull: %v, complete=%v", err, complete)
	}
	for i, target := range targets {
		got, err := os.Readlink(filepath.Join(local, fmt.Sprintf("skills/link%d", i)))
		if err != nil || got != target {
			t.Fatalf("target byte preservation=%q, want%q, %v", got, target, err)
		}
	}
}

func TestPeerHomeLinkTargetDecoderRejectsMalformed(t *testing.T) {
	for _, test := range []struct {
		raw  string
		size int64
	}{
		{`bad\#888`, 4}, {`bad\#777`, 4}, {`bad\#000`, 4}, {"has\ttab", 99}, {`literal\#134#012`, 1},
	} {
		if _, err := decodePeerHomeLinkTarget(test.raw, test.size); err == nil {
			t.Fatalf("malformed target accepted: %+v", test)
		}
	}
}

func TestPeerHomeRemoteParserStillRejectsFilenameTabs(t *testing.T) {
	raw := "@@>f+++++++++\t4\t2026/02/03-04:05:06\tskills/has\ttab\t"
	if _, err := parsePeerHomeRemoteInventory(raw, nil); err == nil {
		t.Fatal("TAB in regular filename accepted as a shortened path")
	}
}

func TestPeerHomeTopologyBaselineLinkVersusDescendants(t *testing.T) {
	baseline := map[string]Fingerprint{"skills/tool": fingerprintLinkTarget("old", peerHomeFixedTime)}
	snapshot := PeerSnapshot{"skills/tool/keep": {Present: true, FP: Fingerprint{Size: 4, Mtime: peerHomeFixedTime}}}
	if err := validatePeerHomeTopology(baseline, snapshot); err == nil {
		t.Fatal("baseline link versus populated directory transition accepted")
	}
}
