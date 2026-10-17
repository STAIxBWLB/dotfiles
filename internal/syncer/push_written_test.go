package syncer

import (
	"context"
	"errors"
	"os"
	osexec "os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/text/unicode/norm"
)

func TestWrittenFiles(t *testing.T) {
	out := strings.Join([]string{
		"@@written .d..t...... ./",
		"@@written >f+++++++++ notes/new.md",
		"@@written >f.st...... notes/changed file.md",
		"         1.20K 100%    0.00kB/s    0:00:00 (xfr#1, to-chk=0/3)\r",
		"@@written cd+++++++++ notes/",
		"@@written *deleting   notes/old.md",
		"*deleting old.txt",
		"@@written >f+++++++ \\#355\\#225\\#234\\#352\\#270\\#200.txt", // openrsync escapes non-ASCII
		"@@written >f+++++++++ lit\\#134#123.md",                       // a literal \#123 in the name
		"@@written >f+++++++++ raw/\xed\\#225\\#234\xea\xb8\\#200.txt", // openrsync under UTF-8 escapes some bytes
		"Number of files: 3 (reg: 2, dir: 1)",
		"",
	}, "\n")
	got := writtenFiles(out)
	want := []string{"notes/new.md", "notes/changed file.md", "한글.txt", `lit\#123.md`, "raw/한글.txt"}
	if len(got) != len(want) {
		t.Errorf("writtenFiles = %v, want %v", got, want)
	}
	for _, rel := range want {
		if !got[rel] {
			t.Errorf("writtenFiles missing %q (got %v)", rel, got)
		}
	}
}

// writeRsyncThen returns an rsync stand-in that runs the real rsync, then the
// shell command after (a live process acting on the tree before the baseline
// refresh runs, #224), and exits with rsync's status.
func writeRsyncThen(t *testing.T, after string) string {
	t.Helper()
	real := requirePeerRsync(t)
	script := filepath.Join(t.TempDir(), "rsync")
	body := "#!/bin/sh\n[ \"$1\" = --version ] && exec \"" + real + "\" --version\n\"" + real + "\" \"$@\"\nrc=$?\n" + after + "\nexit $rc\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func TestPush_RecordsAWrittenFileWhoseLocalTwinVanished(t *testing.T) {
	requireRsync(t)
	f := newIntakeFixture(t)
	f.cfg.Propagation.Delete = true
	f.writeLocal("notes/keep.md", "keep")
	f.writeLocal("renders/work-1/frame_0001.jpg", "frame")
	f.cfg.RsyncPath = writeRsyncThen(t, `rm -f "`+filepath.Join(f.local, "renders/work-1/frame_0001.jpg")+`"`)

	if err := Push(context.Background(), f.runner, f.cfg, false); err != nil {
		t.Fatalf("Push: %v", err)
	}
	baseline, err := LoadBaselineManifest(f.cfg.LocalPaths.BaselineFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := baseline["renders/work-1/frame_0001.jpg"]; !ok {
		t.Fatalf("baseline lacks the frame the push wrote: %v", baseline)
	}

	// A file that arrives in the mirror on its own is still mirror-origin.
	f.writeMirror("from-dropbox.md", "added in the cloud")
	f.cfg.RsyncPath = ""
	plan, err := PlanPush(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.Deletes, []string{"renders/work-1/frame_0001.jpg"}) {
		t.Errorf("Deletes = %v, want the vanished frame", plan.Deletes)
	}
	if len(plan.Conflicts) != 1 || plan.Conflicts[0].RelPath != "from-dropbox.md" ||
		plan.Conflicts[0].Reason != "mirror-only file is not in baseline" {
		t.Errorf("Conflicts = %+v, want only from-dropbox.md as mirror-origin", plan.Conflicts)
	}

	_, err = PushCommand(context.Background(), PushOptions{Config: f.cfg, Runner: f.runner, Mode: ModeForce})
	if err != nil {
		t.Fatalf("PushCommand: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.mirror, "renders/work-1/frame_0001.jpg")); !os.IsNotExist(err) {
		t.Errorf("frame still in the mirror after the next push (err=%v)", err)
	}
}

func TestPushCommand_RecordsAFailedRsyncByExitAndStderr(t *testing.T) {
	f := newIntakeFixture(t)
	f.writeLocal("notes/new.md", "new")
	f.cfg.RsyncPath = filepath.Join(t.TempDir(), "rsync")
	script := "#!/bin/sh\necho 'rsync: connection unexpectedly closed' >&2\necho 'second line' >&2\nexit 12\n"
	if err := os.WriteFile(f.cfg.RsyncPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := PushCommand(context.Background(), PushOptions{Config: f.cfg, Runner: f.runner, Mode: ModeForce}); err == nil {
		t.Fatal("PushCommand succeeded with a failing rsync")
	}
	st, err := LoadLocalState(f.cfg.LocalPaths)
	if err != nil {
		t.Fatal(err)
	}
	if want := "push failed: rsync exit 12: rsync: connection unexpectedly closed"; st.LastPushError != want {
		t.Errorf("LastPushError = %q, want %q", st.LastPushError, want)
	}
}

func TestPushCommand_RecordsARefusalUntilAPushCompletes(t *testing.T) {
	requireRsync(t)
	f := newIntakeFixture(t)
	f.cfg.Propagation.Delete = true
	f.writeLocal("notes/keep.md", "keep")
	f.writeMirror("from-dropbox.md", "added in the cloud")
	run := func(mode RunMode) error {
		_, err := PushCommand(context.Background(), PushOptions{Config: f.cfg, Runner: f.runner, Mode: mode})
		return err
	}

	if err := run(ModeClean); err == nil || !strings.Contains(err.Error(), "push refused: 1 conflict") {
		t.Fatalf("clean push = %v, want a refusal", err)
	}
	st, err := LoadLocalState(f.cfg.LocalPaths)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(st.LastPushError, "push refused: 1 conflict") || st.LastPushErrorSince.IsZero() || st.LastPushAttempt.IsZero() {
		t.Fatalf("state after a refusal = %+v", st)
	}
	since := st.LastPushErrorSince
	time.Sleep(10 * time.Millisecond)
	_ = run(ModeClean)
	if st, _ = LoadLocalState(f.cfg.LocalPaths); !st.LastPushErrorSince.Equal(since) || !st.LastPushAttempt.After(since) {
		t.Errorf("a second refusal moved the streak start or kept the attempt: %+v", st)
	}

	if err := run(ModeForce); err != nil {
		t.Fatalf("force push: %v", err)
	}
	if st, _ = LoadLocalState(f.cfg.LocalPaths); st.LastPushError != "" || !st.LastPushErrorSince.IsZero() || st.LastPush.IsZero() {
		t.Errorf("state after a completed push = %+v", st)
	}
}

func TestStatus_PushStalled(t *testing.T) {
	now := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		st   Status
		want string
	}{
		{"quiet", Status{Interval: 300, SchedulerState: SchedulerRunning, LastPush: now.Add(-10 * time.Minute)}, ""},
		{"failing", Status{LastPushError: "push refused: 719 conflict(s)", LastPushErrorSince: now.Add(-49 * time.Hour)}, "pushes failing since"},
		{"stale", Status{Interval: 300, SchedulerState: SchedulerRunning, LastPush: now.Add(-16 * time.Minute)}, "no push completed for 16m0s"},
		{"stale but scheduler off", Status{Interval: 300, SchedulerState: SchedulerNotInstalled, LastPush: now.Add(-49 * time.Hour)}, ""},
		{"stale but paused", Status{Interval: 300, SchedulerState: SchedulerRunning, Paused: true, LastPush: now.Add(-49 * time.Hour)}, ""},
		{"stale while a run holds the lock", Status{Interval: 300, SchedulerState: SchedulerRunning, LockHeld: true, LastPush: now.Add(-49 * time.Hour)}, ""},
		{"never completed", Status{Interval: 300, SchedulerState: SchedulerRunning, LastPushAttempt: now.Add(-time.Minute)}, "no push has completed yet"},
		{"never attempted", Status{Interval: 300, SchedulerState: SchedulerRunning}, ""},
	} {
		got := tc.st.PushStalled(now)
		if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
			t.Errorf("%s: PushStalled = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestPushCommand_MovesMirrorLeftoversOutOfTheMirror(t *testing.T) {
	requireRsync(t)
	f := newIntakeFixture(t)
	f.cfg.Propagation.Delete = true
	// The workspace renamed "notice./" to "notice/"; the mirror still has the
	// old copy, which the baseline recorded while both sides had it.
	f.writeLocal("spoc/notice/a.pdf", "a")
	f.seedBaseline("spoc/notice./a.pdf", "a", f.writeMirror("spoc/notice./a.pdf", "a"))
	f.writeMirror("spoc/notice./.DS_Store", "finder")
	f.seedBaseline("old./sub/c.md", "c", f.writeMirror("old./sub/c.md", "c"))
	// A workspace name Dropbox cannot store stays excluded and its mirror
	// copy is not touched.
	f.writeLocal("keep./b.md", "b")
	f.writeMirror("keep./b.md", "b")

	plan, err := PlanPush(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.Leftovers, []string{"old./sub/c.md", "spoc/notice./a.pdf"}) || !slices.Equal(plan.Unsupported, []string{"keep./b.md"}) || !plan.MoveLeftovers {
		t.Fatalf("Leftovers = %v, Unsupported = %v, MoveLeftovers = %v", plan.Leftovers, plan.Unsupported, plan.MoveLeftovers)
	}

	var moved SyncEvent
	_, err = PushCommand(context.Background(), PushOptions{
		Config: f.cfg, Runner: f.runner, Mode: ModeClean,
		Progress: func(e SyncEvent) {
			if e.Kind == SyncEventLeftoversMoved {
				moved = e
			}
		},
	})
	if err != nil {
		t.Fatalf("PushCommand: %v", err)
	}
	for _, gone := range []string{"spoc/notice.", "old."} {
		if _, err := os.Stat(filepath.Join(f.mirror, gone)); !os.IsNotExist(err) {
			t.Errorf("old folder %s still in the mirror (err=%v)", gone, err)
		}
	}
	if _, err := os.Stat(filepath.Join(f.mirror, "spoc/notice/a.pdf")); err != nil {
		t.Errorf("renamed copy missing from the mirror: %v", err)
	}
	if moved.Candidates != 2 || !strings.HasPrefix(moved.Path, filepath.Join(f.local, ".sync-conflicts")) {
		t.Fatalf("moved event = %+v", moved)
	}
	if body, err := os.ReadFile(filepath.Join(moved.Path, "spoc/notice./a.pdf")); err != nil || string(body) != "a" {
		t.Errorf("backup copy = %q, %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(f.mirror, "keep./b.md")); err != nil {
		t.Errorf("a workspace unsupported name's mirror copy was touched: %v", err)
	}
	// A pull would read a moved leftover still in the baseline as a mirror
	// deletion.
	if baseline, _ := LoadBaselineManifest(f.cfg.LocalPaths.BaselineFile); len(baseline) > 0 {
		for _, rel := range []string{"spoc/notice./a.pdf", "old./sub/c.md"} {
			if _, ok := baseline[rel]; ok {
				t.Errorf("the baseline still lists moved leftover %s", rel)
			}
		}
	}

	f.cfg.MaxDelete = 0
	f.seedBaseline("old./c.md", "c", f.writeMirror("old./c.md", "c"))
	if _, err := PushCommand(context.Background(), PushOptions{Config: f.cfg, Runner: f.runner, Mode: ModeClean}); err == nil ||
		!strings.Contains(err.Error(), "exceed max_delete") {
		t.Errorf("leftovers over max_delete: err = %v", err)
	}
}

// A mirror-only unsupported name without baseline proof may be a cloud file
// (Google Drive stores "Acme Inc.", Dropbox likely does too): listed, never
// moved. So is a proven name whose copy changed, or an online-only stub.
func TestPushCommand_ListsUnprovenMirrorOnlyUnsupportedNames(t *testing.T) {
	requireRsync(t)
	f := newIntakeFixture(t)
	f.cfg.Propagation.Delete = true
	f.writeLocal("notes/keep.md", "keep")
	f.writeMirror("clients/Acme Inc./contract.pdf", "signed")
	f.seedBaseline("edited./x.pdf", "old", f.writeMirror("edited./x.pdf", "edited in the cloud"))

	plan, err := PlanPush(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"clients/Acme Inc./contract.pdf", "edited./x.pdf"}
	if len(plan.Leftovers) != 0 || !slices.Equal(plan.MirrorUnsupported, want) {
		t.Fatalf("Leftovers = %v, MirrorUnsupported = %v", plan.Leftovers, plan.MirrorUnsupported)
	}
	if _, err := PushCommand(context.Background(), PushOptions{Config: f.cfg, Runner: f.runner, Mode: ModeClean}); err != nil {
		t.Fatalf("PushCommand: %v", err)
	}
	for _, rel := range want {
		if _, err := os.Stat(filepath.Join(f.mirror, rel)); err != nil {
			t.Errorf("%s left the mirror: %v", rel, err)
		}
	}

	stub := filepath.Join(f.mirror, "stub./y.pdf")
	makePlaceholder(t, stub)
	f.seedBaseline("stub./y.pdf", "", time.Time{})
	if plan, err = PlanPush(f.cfg); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(plan.Leftovers, "stub./y.pdf") || !slices.Contains(plan.MirrorUnsupported, "stub./y.pdf") {
		t.Errorf("an online-only stub was classed as a leftover: %v / %v", plan.Leftovers, plan.MirrorUnsupported)
	}
}

// A file an earlier push wrote is unchanged in this run, so rsync does not
// list it; when its local copy vanishes during this run, the baseline keeps
// the proof and the next push deletes it instead of refusing it (#224).
func TestPush_CarriesAProvenEntryWhoseLocalCopyVanishedDuringThePush(t *testing.T) {
	requireRsync(t)
	f := newIntakeFixture(t)
	f.cfg.Propagation.Delete = true
	f.writeLocal("notes/keep.md", "keep")
	f.writeLocal("renders/work-1/frame_0001.jpg", "one")
	if err := Push(context.Background(), f.runner, f.cfg, false); err != nil {
		t.Fatalf("push 1: %v", err)
	}
	f.writeLocal("renders/work-1/frame_0002.jpg", "two")
	f.cfg.RsyncPath = writeRsyncThen(t, `rm -rf "`+filepath.Join(f.local, "renders/work-1")+`"`)
	if err := Push(context.Background(), f.runner, f.cfg, false); err != nil {
		t.Fatalf("push 2: %v", err)
	}
	f.cfg.RsyncPath = ""
	plan, err := PlanPush(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.Deletes, []string{"renders/work-1/frame_0001.jpg", "renders/work-1/frame_0002.jpg"}) || len(plan.Conflicts) != 0 {
		t.Errorf("Deletes = %v, Conflicts = %+v; want both frames deleted, no conflict", plan.Deletes, plan.Conflicts)
	}

	// With delete propagation off the entry is not carried: a pull must not
	// restore a file the workspace deleted long ago.
	f.cfg.Propagation.Delete = false
	if err := Push(context.Background(), f.runner, f.cfg, false); err != nil {
		t.Fatalf("push 3: %v", err)
	}
	baseline, _ := LoadBaselineManifest(f.cfg.LocalPaths.BaselineFile)
	if _, ok := baseline["renders/work-1/frame_0001.jpg"]; ok {
		t.Error("a vanished file's entry was carried with delete propagation off")
	}
}

// The same sequence end to end: the next clean push deletes both frames and
// completes (#224).
func TestPushCommand_DeletesWhatVanishedDuringTheLastPush(t *testing.T) {
	requireRsync(t)
	requirePeerRsync(t)
	f := newIntakeFixture(t)
	f.cfg.Propagation.Delete = true
	clean := PushOptions{Config: f.cfg, Runner: f.runner, Mode: ModeClean}
	f.writeLocal("notes/keep.md", "keep")
	f.writeLocal("renders/work-1/frame_0001.jpg", "one")
	if _, err := PushCommand(context.Background(), clean); err != nil {
		t.Fatalf("push 1: %v", err)
	}
	f.writeLocal("renders/work-1/frame_0002.jpg", "two")
	f.cfg.RsyncPath = writeRsyncThen(t, `rm -rf "`+filepath.Join(f.local, "renders/work-1")+`"`)
	if _, err := PushCommand(context.Background(), clean); err != nil {
		t.Fatalf("push 2: %v", err)
	}
	f.cfg.RsyncPath = ""
	if _, err := PushCommand(context.Background(), clean); err != nil {
		t.Fatalf("push 3: %v", err)
	}
	for _, rel := range []string{"renders/work-1/frame_0001.jpg", "renders/work-1/frame_0002.jpg"} {
		if _, err := os.Stat(filepath.Join(f.mirror, rel)); !os.IsNotExist(err) {
			t.Errorf("%s is still in the mirror (err=%v)", rel, err)
		}
	}
	if st, _ := LoadLocalState(f.cfg.LocalPaths); st.LastPush.IsZero() || st.LastPushError != "" {
		t.Errorf("state after the deleting push: LastPush = %v, LastPushError = %q", st.LastPush, st.LastPushError)
	}
}

// A dry run with changes sends nothing and records nothing.
func TestPushCommand_ADryRunRecordsNothing(t *testing.T) {
	requireRsync(t)
	f := newIntakeFixture(t)
	f.writeLocal("notes/new.md", "new")
	if _, err := PushCommand(context.Background(), PushOptions{Config: f.cfg, Runner: f.runner, Mode: ModeClean, DryRun: true}); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if st, _ := LoadLocalState(f.cfg.LocalPaths); !st.LastPushAttempt.IsZero() || !st.LastPush.IsZero() || st.LastPushError != "" {
		t.Errorf("a dry run recorded state: %+v", st)
	}
	if _, err := os.Stat(filepath.Join(f.mirror, "notes/new.md")); !os.IsNotExist(err) {
		t.Errorf("a dry run sent a file (err=%v)", err)
	}
}

func TestPushCommand_RecordsARsyncThatDidNotStart(t *testing.T) {
	f := newIntakeFixture(t)
	f.writeLocal("notes/new.md", "new")
	f.cfg.RsyncPath = filepath.Join(t.TempDir(), "no-such-rsync")
	if _, err := PushCommand(context.Background(), PushOptions{Config: f.cfg, Runner: f.runner, Mode: ModeForce}); err == nil {
		t.Fatal("PushCommand succeeded without an rsync")
	}
	st, _ := LoadLocalState(f.cfg.LocalPaths)
	if !strings.HasPrefix(st.LastPushError, "push failed: ") || !strings.Contains(st.LastPushError, "no such file or directory") {
		t.Errorf("LastPushError = %q, want the start failure", st.LastPushError)
	}
}

// rsync stops at --max-delete with exit 25 only after every transfer
// (--delete-after), so the files that run wrote are still recorded (#224).
func TestPushCommand_RecordsWrittenFilesWhenRsyncStopsAtMaxDelete(t *testing.T) {
	requireRsync(t)
	requirePeerRsync(t)
	f := newIntakeFixture(t)
	f.cfg.Propagation.Delete = true
	f.writeLocal("notes/a.md", "a")
	f.writeLocal("notes/b.md", "b")
	if _, err := PushCommand(context.Background(), PushOptions{Config: f.cfg, Runner: f.runner, Mode: ModeClean}); err != nil {
		t.Fatalf("push 1: %v", err)
	}
	before, _ := LoadLocalState(f.cfg.LocalPaths)
	if before.LastPush.IsZero() {
		t.Fatal("a completed push did not stamp last_push")
	}
	for _, rel := range []string{"notes/a.md", "notes/b.md"} {
		if err := os.Remove(filepath.Join(f.local, rel)); err != nil {
			t.Fatal(err)
		}
	}
	frame := "renders/work-1/frame_0001.jpg"
	f.writeLocal(frame, "frame")
	f.cfg.MaxDelete = 1
	f.cfg.RsyncPath = writeRsyncThen(t, `rm -f "`+filepath.Join(f.local, frame)+`"`)
	if _, err := PushCommand(context.Background(), PushOptions{Config: f.cfg, Runner: f.runner, Mode: ModeClean}); err == nil {
		t.Fatal("a push past max_delete succeeded")
	}
	st, _ := LoadLocalState(f.cfg.LocalPaths)
	if !strings.HasPrefix(st.LastPushError, "push failed: rsync exit 25") || !st.LastPush.Equal(before.LastPush) {
		t.Errorf("state after exit 25 = %+v; want the error recorded and last_push unchanged", st)
	}

	f.cfg.RsyncPath = ""
	plan, err := PlanPush(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Conflicts) != 0 || len(plan.Deletes) != 2 || !slices.Contains(plan.Deletes, frame) {
		t.Errorf("Deletes = %v, Conflicts = %+v; want the frame and the undeleted note as deletes", plan.Deletes, plan.Conflicts)
	}
}

// rsync skips every deletion after an I/O error (exit 23) and openrsync
// deletes nothing with --backup. A push that leaves a planned deletion in the
// mirror is recorded as failed, and the next working push deletes it (#224).
func TestPushCommand_FailsWhenAPlannedDeletionStaysInTheMirror(t *testing.T) {
	requireRsync(t)
	f := newIntakeFixture(t)
	f.cfg.Propagation.Delete = true
	f.writeLocal("notes/keep.md", "keep")
	f.writeLocal("notes/gone.md", "gone")
	if _, err := PushCommand(context.Background(), PushOptions{Config: f.cfg, Runner: f.runner, Mode: ModeClean}); err != nil {
		t.Fatalf("push 1: %v", err)
	}
	before, _ := LoadLocalState(f.cfg.LocalPaths)
	if err := os.Remove(filepath.Join(f.local, "notes/gone.md")); err != nil {
		t.Fatal(err)
	}
	real := requirePeerRsync(t)
	// dropDeletes runs the real rsync without --delete-after, then exits with
	// status rc: deletions skipped silently (0) or after an I/O error (23). A
	// version query reaches the real rsync, so its banner stays the evidence.
	dropDeletes := func(rc string) string {
		script := filepath.Join(t.TempDir(), "rsync")
		body := "#!/bin/sh\n[ \"$1\" = --version ] && exec \"" + real + "\" --version\nfor a do\n  shift\n  [ \"$a\" = --delete-after ] || set -- \"$@\" \"$a\"\ndone\n\"" + real + "\" \"$@\" || exit\nexit " + rc + "\n"
		if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		return script
	}
	// The same post-transfer check catches silent skips and I/O failures.
	cause := "rsync kept them: it skips deletions after an I/O error"
	for _, rc := range []string{"23", "0"} {
		f.cfg.RsyncPath = dropDeletes(rc)
		_, err := PushCommand(context.Background(), PushOptions{Config: f.cfg, Runner: f.runner, Mode: ModeClean})
		if err == nil || !strings.Contains(err.Error(), "1 planned deletion(s) left in the mirror, first notes/gone.md: "+cause) {
			t.Fatalf("PushCommand with rsync exit %s = %v, want the unapplied deletion with %q", rc, err, cause)
		}
	}
	if st, _ := LoadLocalState(f.cfg.LocalPaths); !strings.Contains(st.LastPushError, "planned deletion(s) left") || !st.LastPush.Equal(before.LastPush) {
		t.Errorf("state after the failed run = %+v; want the error recorded and last_push unchanged", st)
	}

	f.cfg.RsyncPath = ""
	if _, err := PushCommand(context.Background(), PushOptions{Config: f.cfg, Runner: f.runner, Mode: ModeClean}); err != nil {
		t.Fatalf("next push: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.mirror, "notes/gone.md")); !os.IsNotExist(err) {
		t.Errorf("the deletion is still pending after a working push (err=%v)", err)
	}
}

// A leftover edited in the cloud while the push ran is no longer proven by the
// refreshed baseline, so the move leaves it in the mirror (#225).
func TestPushCommand_LeavesALeftoverEditedDuringThePush(t *testing.T) {
	requireRsync(t)
	f := newIntakeFixture(t)
	f.cfg.Propagation.Delete = true
	f.writeLocal("old/c.md", "c")
	f.seedBaseline("old./c.md", "c", f.writeMirror("old./c.md", "c"))
	leftover := filepath.Join(f.mirror, "old./c.md")
	f.cfg.RsyncPath = writeRsyncThen(t, `echo "edited in the cloud" >> "`+leftover+`"`)
	moved := false
	_, err := PushCommand(context.Background(), PushOptions{
		Config: f.cfg, Runner: f.runner, Mode: ModeClean,
		Progress: func(e SyncEvent) { moved = moved || e.Kind == SyncEventLeftoversMoved },
	})
	if err != nil {
		t.Fatalf("PushCommand: %v", err)
	}
	if _, err := os.Stat(leftover); err != nil || moved {
		t.Errorf("the edited leftover left the mirror (moved=%v, err=%v)", moved, err)
	}
}

// A run that skipped files (exit 23) clears the error but is not a completed
// push, so a push that keeps skipping files goes stale (#224).
func TestPushCommand_APartialTransferIsNotACompletedPush(t *testing.T) {
	requireRsync(t)
	f := newIntakeFixture(t)
	f.writeLocal("notes/keep.md", "keep")
	if _, err := PushCommand(context.Background(), PushOptions{Config: f.cfg, Runner: f.runner, Mode: ModeClean}); err != nil {
		t.Fatalf("push 1: %v", err)
	}
	first, _ := LoadLocalState(f.cfg.LocalPaths)
	// A run with nothing to send is a completed push too.
	time.Sleep(10 * time.Millisecond)
	if _, err := PushCommand(context.Background(), PushOptions{Config: f.cfg, Runner: f.runner, Mode: ModeClean}); err != nil {
		t.Fatalf("push with no changes: %v", err)
	}
	before, _ := LoadLocalState(f.cfg.LocalPaths)
	if !before.LastPush.After(first.LastPush) {
		t.Fatalf("a run with no changes did not stamp last_push: %+v", before)
	}
	if err := UpdateLocalState(f.cfg.LocalPaths, func(s *LocalState) { s.LastPushError = "push refused: earlier" }); err != nil {
		t.Fatal(err)
	}
	f.writeLocal("notes/new.md", "new")
	f.cfg.RsyncPath = writeRsyncThen(t, "exit 23")
	if _, err := PushCommand(context.Background(), PushOptions{Config: f.cfg, Runner: f.runner, Mode: ModeClean}); err != nil {
		t.Fatalf("partial push: %v", err)
	}
	st, _ := LoadLocalState(f.cfg.LocalPaths)
	if !st.LastPush.Equal(before.LastPush) || st.LastPushError != "" || !st.LastPushAttempt.After(before.LastPush) {
		t.Errorf("state after a partial transfer = %+v; want last_push kept, the error cleared, the attempt stamped", st)
	}

	// Exit 24 only means files vanished before transfer: the run is complete.
	f.writeLocal("notes/newer.md", "newer")
	f.cfg.RsyncPath = writeRsyncThen(t, "exit 24")
	if _, err := PushCommand(context.Background(), PushOptions{Config: f.cfg, Runner: f.runner, Mode: ModeClean}); err != nil {
		t.Fatalf("push with vanished files: %v", err)
	}
	if st, _ = LoadLocalState(f.cfg.LocalPaths); !st.LastPush.After(before.LastPush) {
		t.Errorf("an exit-24 run did not stamp last_push: %+v", st)
	}
}

// A panic inside the run (a renderer, the plan code) neither completes nor
// fails the push, so the recorder leaves the state alone.
func TestPushCommand_APanicRecordsNothing(t *testing.T) {
	f := newIntakeFixture(t)
	f.writeLocal("notes/new.md", "new")
	if err := UpdateLocalState(f.cfg.LocalPaths, func(s *LocalState) { s.LastPushError = "push refused: earlier" }); err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() { _ = recover() }()
		_, _ = PushCommand(context.Background(), PushOptions{
			Config: f.cfg, Runner: f.runner, Mode: ModeClean,
			Progress: func(e SyncEvent) {
				if e.Kind == SyncEventPushPlanReady {
					panic("renderer")
				}
			},
		})
	}()
	st, _ := LoadLocalState(f.cfg.LocalPaths)
	if st.LastPushError != "push refused: earlier" || !st.LastPush.IsZero() || !st.LastPushAttempt.IsZero() {
		t.Errorf("state after a panic = %+v; want it untouched", st)
	}
}

// rsync killed by a signal before it could exit has no status, so the
// refresh is skipped; rsync that catches a signal exits 20 and is finalized.
func TestPush_ARunKilledBeforeRsyncExitedSkipsTheRefresh(t *testing.T) {
	requireRsync(t)
	f := newIntakeFixture(t)
	f.writeLocal("notes/new.md", "new")
	f.cfg.RsyncPath = writeRsyncThen(t, "kill -9 $$")
	if err := Push(context.Background(), f.runner, f.cfg, false); err == nil {
		t.Fatal("Push succeeded although rsync was killed")
	}
	baseline, _ := LoadBaselineManifest(f.cfg.LocalPaths.BaselineFile)
	if _, ok := baseline["notes/new.md"]; ok {
		t.Error("a killed run refreshed the baseline")
	}

	f.cfg.RsyncPath = writeRsyncThen(t, "exit 20")
	if err := Push(context.Background(), f.runner, f.cfg, false); err == nil {
		t.Fatal("Push succeeded although rsync exited 20")
	}
	if baseline, _ = LoadBaselineManifest(f.cfg.LocalPaths.BaselineFile); baseline["notes/new.md"].Size == 0 {
		t.Error("an rsync that caught a signal (exit 20) did not refresh the baseline")
	}
}

// A leftover whose move is held back (here by max_delete) keeps its proof, so
// a pull in between must not restore the old name: that would undo the rename
// for good. The next push that may move it does (#225).
func TestPullTracked_LeavesAPendingLeftoverInTheMirror(t *testing.T) {
	requireRsync(t)
	f := newIntakeFixture(t)
	f.cfg.Propagation.Delete = true
	f.writeLocal("old/c.md", "c")
	f.seedBaseline("old./c.md", "c", f.writeMirror("old./c.md", "c"))
	f.cfg.MaxDelete = 0
	if _, err := PushCommand(context.Background(), PushOptions{Config: f.cfg, Runner: f.runner, Mode: ModeClean}); err == nil || !strings.Contains(err.Error(), "exceed max_delete") {
		t.Fatalf("push = %v, want the leftover held back by max_delete", err)
	}
	res, err := PullTracked(f.cfg, PullOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(res.Restored, "old./c.md") {
		t.Errorf("the pull restored a pending leftover: Restored = %v", res.Restored)
	}
	if _, err := os.Lstat(filepath.Join(f.local, "old.")); !os.IsNotExist(err) {
		t.Fatalf("the workspace has the renamed-away name again (err=%v)", err)
	}
	f.cfg.MaxDelete = 100
	if _, err := PushCommand(context.Background(), PushOptions{Config: f.cfg, Runner: f.runner, Mode: ModeClean}); err != nil {
		t.Fatalf("push: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.mirror, "old./c.md")); !os.IsNotExist(err) {
		t.Errorf("the leftover was not moved after the pull (err=%v)", err)
	}
}

// errOnlyCtx reports cancellation without closing Done, so rsync is not
// killed: the window between rsync's exit and dot reading its status.
type errOnlyCtx struct{ context.Context }

func (errOnlyCtx) Err() error { return context.Canceled }

// A push during which dot was interrupted, whose rsync then exited 24, records
// a failure, leaves last_push unchanged and moves no leftover: the refresh did
// not run, so the post-push steps have no current baseline (#231).
func TestPushCommand_AnInterruptedRunDoesNotFinalize(t *testing.T) {
	requireRsync(t)
	f := newIntakeFixture(t)
	f.cfg.Propagation.Delete = true
	f.writeLocal("notes/keep.md", "keep")
	f.seedBaseline("old./c.md", "c", f.writeMirror("old./c.md", "c"))
	f.cfg.RsyncPath = writeRsyncThen(t, "exit 24")
	_, err := PushCommand(errOnlyCtx{context.Background()}, PushOptions{Config: f.cfg, Runner: f.runner, Mode: ModeClean})
	if err == nil || !strings.Contains(err.Error(), "push interrupted") {
		t.Fatalf("PushCommand = %v, want an interrupted failure", err)
	}
	if st, _ := LoadLocalState(f.cfg.LocalPaths); !st.LastPush.IsZero() || !strings.Contains(st.LastPushError, "interrupted") {
		t.Errorf("state: LastPush = %v, LastPushError = %q", st.LastPush, st.LastPushError)
	}
	if _, err := os.Stat(filepath.Join(f.mirror, "old./c.md")); err != nil {
		t.Errorf("an interrupted run moved a leftover: %v", err)
	}
}

// An interrupted SSH push whose rsync exited 24 records a failure and leaves
// last_push unchanged: the SSH branch of PushCommand applies the same
// ctx.Err() stop as the local branch (#233).
func TestPushCommand_AnInterruptedSSHPushDoesNotStampLastPush(t *testing.T) {
	f := newIntakeFixture(t)
	lastPush := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	if err := SaveLocalState(f.cfg.LocalPaths, &LocalState{LastPush: lastPush}); err != nil {
		t.Fatal(err)
	}
	f.cfg.Target = Target{Kind: TargetSSH, Host: "user@peer", Path: "/remote/work"}
	// A stub, not writeRsyncThen: the real rsync would dial the fake peer.
	stub := filepath.Join(t.TempDir(), "rsync")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nexit 24\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.cfg.RsyncPath = stub
	_, err := PushCommand(errOnlyCtx{context.Background()}, PushOptions{Config: f.cfg, Runner: f.runner, Mode: ModeClean})
	if err == nil || !strings.Contains(err.Error(), "push interrupted") {
		t.Fatalf("PushCommand = %v, want an interrupted failure", err)
	}
	if st, _ := LoadLocalState(f.cfg.LocalPaths); !st.LastPush.Equal(lastPush) || !strings.Contains(st.LastPushError, "interrupted") {
		t.Errorf("state: LastPush = %v, LastPushError = %q", st.LastPush, st.LastPushError)
	}
}

// A run during which dot was interrupted skips the refresh even when rsync,
// which a terminal Ctrl-C reaches too, exited 20 before it was killed.
func TestRsyncExited_AnInterruptedDotDoesNotFinalize(t *testing.T) {
	exit20 := osexec.Command("sh", "-c", "exit 20").Run()
	if !rsyncExited(context.Background(), exit20) {
		t.Fatalf("exit 20 (%v) is not finalized", exit20)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if rsyncExited(ctx, exit20) {
		t.Error("an interrupted dot finalizes rsync's exit 20")
	}
}

// The backup path is checked component by component: a symlinked
// .sync-conflicts would carry the backup out of the workspace (here back
// into the mirror), so the move refuses and the leftover stays.
func TestMoveMirrorLeftovers_RefusesASymlinkedBackupDir(t *testing.T) {
	f := newIntakeFixture(t)
	f.seedBaseline("old./c.md", "c", f.writeMirror("old./c.md", "c"))
	conflicts := filepath.Join(f.local, conflictsDirName)
	if err := os.RemoveAll(conflicts); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.mirror, conflicts); err != nil {
		t.Fatal(err)
	}
	_, moved, err := MoveMirrorLeftovers(f.cfg, []string{"old./c.md"})
	if err == nil || moved != 0 {
		t.Fatalf("MoveMirrorLeftovers = %d, %v; want a refusal", moved, err)
	}
	if _, err := os.Stat(filepath.Join(f.mirror, "old./c.md")); err != nil {
		t.Errorf("the leftover left the mirror: %v", err)
	}
	if entries, _ := os.ReadDir(f.mirror); len(entries) != 1 {
		t.Errorf("the move wrote through the symlink into the mirror: %v", entries)
	}
}

// Only a workspace path proven absent counts as gone: an unreadable twin keeps
// its leftover in the mirror, and the refresh drops its entry rather than
// carrying it as the proof of a rename (#225).
func TestMoveMirrorLeftovers_LeavesAnUnreadableWorkspaceTwin(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 directory")
	}
	f := newIntakeFixture(t)
	f.cfg.Propagation.Delete = true
	f.writeLocal("x./f.md", "f")
	f.seedBaseline("x./f.md", "f", f.writeMirror("x./f.md", "f"))
	dir := filepath.Join(f.local, "x.")
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if _, moved, err := MoveMirrorLeftovers(f.cfg, []string{"x./f.md"}); err != nil || moved != 0 {
		t.Fatalf("MoveMirrorLeftovers = %d, %v; want nothing moved", moved, err)
	}
	if _, err := os.Stat(filepath.Join(f.mirror, "x./f.md")); err != nil {
		t.Errorf("the leftover of an unreadable workspace twin left the mirror: %v", err)
	}
	if err := RefreshBaseline(f.cfg, FingerprintFast); err != nil {
		t.Fatalf("RefreshBaseline: %v", err)
	}
	if baseline, _ := LoadBaselineManifest(f.cfg.LocalPaths.BaselineFile); baseline["x./f.md"] != (Fingerprint{}) {
		t.Errorf("the refresh carried an unreadable twin's entry: %+v", baseline["x./f.md"])
	}
}

// Exactly max_delete leftovers move; one more stops the move before it starts.
func TestMoveMirrorLeftovers_MovesUpToMaxDelete(t *testing.T) {
	f := newIntakeFixture(t)
	for _, rel := range []string{"a./1.md", "a./2.md"} {
		f.seedBaseline(rel, "x", f.writeMirror(rel, "x"))
	}
	f.cfg.MaxDelete = 1
	if _, moved, err := MoveMirrorLeftovers(f.cfg, []string{"a./1.md", "a./2.md"}); err == nil || moved != 0 {
		t.Fatalf("two leftovers over max_delete 1: moved %d, err %v", moved, err)
	}
	if _, moved, err := MoveMirrorLeftovers(f.cfg, []string{"a./1.md"}); err != nil || moved != 1 {
		t.Fatalf("one leftover at max_delete 1: moved %d, err %v", moved, err)
	}
}

// A baseline save that fails after the move still names where the moved
// leftovers went.
func TestMoveMirrorLeftovers_ASaveFailureNamesTheBackup(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes a read-only directory")
	}
	f := newIntakeFixture(t)
	f.seedBaseline("old./c.md", "c", f.writeMirror("old./c.md", "c"))
	store := filepath.Dir(f.cfg.LocalPaths.BaselineFile)
	if err := os.Chmod(f.cfg.LocalPaths.BaselineFile, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(store, 0o755)
		_ = os.Chmod(f.cfg.LocalPaths.BaselineFile, 0o644)
	})
	dir, moved, err := MoveMirrorLeftovers(f.cfg, []string{"old./c.md"})
	if err == nil || moved != 1 || !strings.Contains(err.Error(), dir) {
		t.Fatalf("MoveMirrorLeftovers = %q, %d, %v; want one moved and an error naming the backup", dir, moved, err)
	}
}

// A planned deletion the workspace restored before rsync built its list is
// sent, not deleted, and the push still completes (#224).
func TestPushCommand_ADeletionRestoredBeforeTheRunIsNotUnapplied(t *testing.T) {
	requireRsync(t)
	f := newIntakeFixture(t)
	f.cfg.Propagation.Delete = true
	f.writeLocal("notes/keep.md", "keep")
	f.writeLocal("notes/back.md", "back")
	clean := PushOptions{Config: f.cfg, Runner: f.runner, Mode: ModeClean}
	if _, err := PushCommand(context.Background(), clean); err != nil {
		t.Fatalf("push 1: %v", err)
	}
	restored := filepath.Join(f.local, "notes/back.md")
	if err := os.Remove(restored); err != nil {
		t.Fatal(err)
	}
	real, _ := osexec.LookPath("rsync")
	script := filepath.Join(t.TempDir(), "rsync")
	body := "#!/bin/sh\n[ \"$1\" = --version ] || printf back > \"" + restored + "\"\nexec \"" + real + "\" \"$@\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	f.cfg.RsyncPath = script
	if _, err := PushCommand(context.Background(), clean); err != nil {
		t.Fatalf("push 2: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.mirror, "notes/back.md")); err != nil {
		t.Errorf("the restored file is not in the mirror: %v", err)
	}
}

// On a filesystem that finds a name in either Unicode form (APFS), a mirror
// leftover whose name the workspace holds in NFD is not planned: the move would
// skip it, so a plan that listed it would force a push every interval (#225).
func TestPlanPush_ALeftoverTheWorkspaceHasInAnotherUnicodeFormIsNotPlanned(t *testing.T) {
	f := newIntakeFixture(t)
	f.cfg.Propagation.Delete = true
	nfc := norm.NFC.String("café./f.md")
	nfd := norm.NFD.String(nfc)
	f.writeLocal(nfd, "f")
	if _, err := os.Lstat(filepath.Join(f.local, nfc)); err != nil {
		t.Skip("this filesystem tells NFC and NFD names apart")
	}
	f.seedBaseline(nfc, "f", f.writeMirror(nfc, "f"))
	plan, err := PlanPush(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Leftovers) != 0 || !slices.Equal(plan.Unsupported, []string{nfd}) {
		t.Errorf("Leftovers = %q, Unsupported = %q; want no leftover and the workspace name", plan.Leftovers, plan.Unsupported)
	}
}

// Two leftovers one filesystem folds together would map to one backup path;
// the move stops instead of replacing the first backup.
func TestMoveMirrorLeftovers_NeverReplacesABackup(t *testing.T) {
	f := newIntakeFixture(t)
	f.seedBaseline("old./c.md", "c", f.writeMirror("old./c.md", "c"))
	dir, moved, err := MoveMirrorLeftovers(f.cfg, []string{"old./c.md", "old./c.md"})
	if err == nil || !strings.Contains(err.Error(), "already holds a backup") || moved != 1 {
		t.Fatalf("MoveMirrorLeftovers = %q, %d, %v; want one moved and a refusal", dir, moved, err)
	}
	if body, err := os.ReadFile(filepath.Join(dir, "old./c.md")); err != nil || string(body) != "c" {
		t.Errorf("first backup = %q, %v", body, err)
	}
}

// A failed rsync stays the recorded reason when the refresh after it fails too
// (here the mirror vanished mid-run).
func TestPushCommand_AFailedRunKeepsItsReasonWhenTheRefreshFails(t *testing.T) {
	f := newIntakeFixture(t)
	f.writeLocal("notes/new.md", "new")
	f.cfg.RsyncPath = filepath.Join(t.TempDir(), "rsync")
	script := "#!/bin/sh\nrm -rf \"" + f.mirror + "\"\necho 'rsync: connection unexpectedly closed' >&2\nexit 12\n"
	if err := os.WriteFile(f.cfg.RsyncPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := PushCommand(context.Background(), PushOptions{Config: f.cfg, Runner: f.runner, Mode: ModeForce}); err == nil {
		t.Fatal("PushCommand succeeded with a failing rsync")
	}
	if st, _ := LoadLocalState(f.cfg.LocalPaths); st.LastPushError != "push failed: rsync exit 12: rsync: connection unexpectedly closed" {
		t.Errorf("LastPushError = %q, want the rsync failure", st.LastPushError)
	}
}

// A refusal recorded outside PushCommand respects a pause and the sync lock:
// while a run holds the lock, that run records its own outcome.
func TestRecordPushRefusal_SkipsWhilePausedOrLocked(t *testing.T) {
	f := newIntakeFixture(t)
	release, err := AcquireLockForRun(f.cfg.LockDir, false)
	if err != nil {
		t.Fatal(err)
	}
	_ = RecordPushRefusal(f.cfg, errors.New("push refused: while locked"))
	release()
	f.cfg.Paused = true
	_ = RecordPushRefusal(f.cfg, errors.New("push refused: while paused"))
	if st, _ := LoadLocalState(f.cfg.LocalPaths); st.LastPushError != "" {
		t.Fatalf("recorded %q while locked or paused", st.LastPushError)
	}
	f.cfg.Paused = false
	_ = RecordPushRefusal(f.cfg, errors.New("push refused: now"))
	if st, _ := LoadLocalState(f.cfg.LocalPaths); st.LastPushError != "push refused: now" {
		t.Errorf("LastPushError = %q, want the refusal", st.LastPushError)
	}
}

// A --verbose push tees rsync's output to the terminal and still records the
// files it wrote (#224).
func TestPush_VerboseRecordsAWrittenFile(t *testing.T) {
	requireRsync(t)
	f := newIntakeFixture(t)
	f.cfg.Verbose = true
	frame := "renders/work-1/frame_0001.jpg"
	f.writeLocal(frame, "frame")
	f.cfg.RsyncPath = writeRsyncThen(t, `rm -f "`+filepath.Join(f.local, frame)+`"`)
	if err := Push(context.Background(), f.runner, f.cfg, false); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if baseline, _ := LoadBaselineManifest(f.cfg.LocalPaths.BaselineFile); baseline[frame].Size == 0 {
		t.Errorf("a verbose push did not record the frame it wrote: %v", baseline)
	}
}

// A mirror push never sends an unsupported name, so the refresh must not take
// a cloud edit of its mirror copy as proof: after the workspace renames the
// folder, the edited copy is listed, not moved; an unchanged sibling the
// baseline proves is still moved (#225).
func TestPushCommand_ACloudEditOfAnUnsupportedNameIsNeverALeftover(t *testing.T) {
	requireRsync(t)
	f := newIntakeFixture(t)
	f.cfg.Propagation.Delete = true
	for _, rel := range []string{"a./f.md", "a./g.md"} {
		f.writeLocal(rel, "v1")
		f.seedBaseline(rel, "v1", f.writeMirror(rel, "v1"))
	}
	f.writeMirror("a./f.md", "edited in the cloud, longer")
	// A refresh before #225 fingerprinted the mirror copy, which no push sent.
	f.writeLocal("a./h.md", "workspace v1")
	f.seedBaseline("a./h.md", "cloud copy, recorded", f.writeMirror("a./h.md", "cloud copy, recorded"))
	if _, err := PushCommand(context.Background(), PushOptions{Config: f.cfg, Runner: f.runner, Mode: ModeClean}); err != nil {
		t.Fatalf("push with the cloud edit: %v", err)
	}
	baseline, _ := LoadBaselineManifest(f.cfg.LocalPaths.BaselineFile)
	for _, rel := range []string{"a./f.md", "a./h.md"} {
		if fp, ok := baseline[rel]; ok {
			t.Fatalf("the refresh kept %s's cloud copy as proof: %+v", rel, fp)
		}
	}

	// The workspace renames a./ to a/.
	if err := os.Rename(filepath.Join(f.local, "a."), filepath.Join(f.local, "a")); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanPush(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.Leftovers, []string{"a./g.md"}) || !slices.Equal(plan.MirrorUnsupported, []string{"a./f.md", "a./h.md"}) {
		t.Fatalf("Leftovers = %v, MirrorUnsupported = %v", plan.Leftovers, plan.MirrorUnsupported)
	}
	if _, err := PushCommand(context.Background(), PushOptions{Config: f.cfg, Runner: f.runner, Mode: ModeClean}); err != nil {
		t.Fatalf("push after the rename: %v", err)
	}
	if body, err := os.ReadFile(filepath.Join(f.mirror, "a./f.md")); err != nil || string(body) != "edited in the cloud, longer" {
		t.Errorf("the cloud-edited copy left the mirror: %q, %v", body, err)
	}
	if body, err := os.ReadFile(filepath.Join(f.mirror, "a./h.md")); err != nil || string(body) != "cloud copy, recorded" {
		t.Errorf("the cloud copy an old refresh recorded left the mirror: %q, %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(f.mirror, "a./g.md")); !os.IsNotExist(err) {
		t.Errorf("the proven leftover was not moved (err=%v)", err)
	}
}

// Without delete propagation a leftover is listed, never moved, and an
// operator decline records nothing (#225, #224).
func TestPushCommand_LeftoversNeedDeletePropagationAndADeclineRecordsNothing(t *testing.T) {
	requireRsync(t)
	f := newIntakeFixture(t)
	f.cfg.Propagation.Delete = false
	f.writeLocal("notes/keep.md", "keep")
	f.seedBaseline("old./c.md", "c", f.writeMirror("old./c.md", "c"))
	plan, err := PlanPush(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.Leftovers, []string{"old./c.md"}) || plan.MoveLeftovers {
		t.Fatalf("Leftovers = %v, MoveLeftovers = %v; want listed only", plan.Leftovers, plan.MoveLeftovers)
	}

	declined, err := PushCommand(context.Background(), PushOptions{
		Config: f.cfg, Runner: f.runner, Mode: ModeManual,
		Confirm: func(ConfirmRequest) (bool, error) { return false, nil },
	})
	if err != nil || declined.Outcome != PushAborted {
		t.Fatalf("declined push = %+v, %v", declined, err)
	}
	if st, _ := LoadLocalState(f.cfg.LocalPaths); !st.LastPushAttempt.IsZero() || !st.LastPush.IsZero() {
		t.Errorf("an operator decline recorded state: %+v", st)
	}

	if _, err := PushCommand(context.Background(), PushOptions{Config: f.cfg, Runner: f.runner, Mode: ModeClean}); err != nil {
		t.Fatalf("PushCommand: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.mirror, "old./c.md")); err != nil {
		t.Errorf("a leftover moved without delete propagation: %v", err)
	}
	// Its local twin is gone and deletes do not propagate, so its proof drops,
	// as for any other file: a later pull must not restore a long-deleted name.
	if baseline, _ := LoadBaselineManifest(f.cfg.LocalPaths.BaselineFile); baseline["old./c.md"] != (Fingerprint{}) {
		t.Errorf("the refresh kept the entry of an unsupported name the workspace deleted: %+v", baseline["old./c.md"])
	}
}

// A leftover the workspace has again by the time of the move (a rename undone
// during the push, or the same name in another Unicode form) stays (#225).
func TestPushCommand_ALeftoverTheWorkspaceHasAgainStays(t *testing.T) {
	requireRsync(t)
	f := newIntakeFixture(t)
	f.cfg.Propagation.Delete = true
	f.writeLocal("notes/keep.md", "keep")
	f.seedBaseline("old./c.md", "c", f.writeMirror("old./c.md", "c"))
	restored := filepath.Join(f.local, "old.", "c.md")
	f.cfg.RsyncPath = writeRsyncThen(t, `mkdir -p "`+filepath.Dir(restored)+`" && printf c > "`+restored+`"`)
	if _, err := PushCommand(context.Background(), PushOptions{Config: f.cfg, Runner: f.runner, Mode: ModeClean}); err != nil {
		t.Fatalf("PushCommand: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.mirror, "old./c.md")); err != nil {
		t.Errorf("a leftover the workspace has again left the mirror: %v", err)
	}
}

// The background lease follows the sync lock (#267): a run that loses the
// lock never takes it, and the run that holds the lock releases it at the end.
func TestPushCommand_WhileLockedRunsOnlyUnderTheLock(t *testing.T) {
	requireRsync(t)
	f := newIntakeFixture(t)
	f.writeLocal("notes/a.md", "a")
	held, released := 0, 0
	opts := PushOptions{Config: f.cfg, Runner: f.runner, Mode: ModeClean, WhileLocked: func() func() {
		held++
		return func() { released++ }
	}}

	release, err := AcquireLockForRun(f.cfg.LockDir, false)
	if err != nil {
		t.Fatal(err)
	}
	res, err := PushCommand(context.Background(), opts)
	release()
	if err != nil || res.Outcome != PushLockBusy || held != 0 {
		t.Fatalf("a run without the lock: outcome %v, err %v, lease taken %d time(s)", res, err, held)
	}
	if _, err := PushCommand(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if held != 1 || released != 1 {
		t.Fatalf("lease taken %d, released %d; want 1 and 1", held, released)
	}
}

// pushClean runs a clean push and fails the test on any error.
func (f *intakeFixture) pushClean(mode RunMode) {
	f.t.Helper()
	if _, err := PushCommand(context.Background(), PushOptions{Config: f.cfg, Runner: f.runner, Mode: mode}); err != nil {
		f.t.Fatalf("push (%s): %v", mode, err)
	}
}

// later sets a file's mtime past every write so far: rsync and the plan only
// see a change through size and mtime.
func later(t *testing.T, abs string, by time.Duration) {
	t.Helper()
	when := time.Now().Add(by)
	if err := os.Chtimes(abs, when, when); err != nil {
		t.Fatal(err)
	}
}

// Pushing an ordinary edit leaves no backup behind (#264 AC1).
func TestPushCommand_RoutineUpdateLeavesNoBackup(t *testing.T) {
	requireRsync(t)
	f := newIntakeFixture(t)
	f.writeLocal("notes/a.md", "v1")
	f.pushClean(ModeClean)
	f.writeLocal("notes/a.md", "v2 edited")
	later(t, filepath.Join(f.local, "notes/a.md"), time.Minute)
	f.pushClean(ModeClean)

	if got, _ := os.ReadFile(filepath.Join(f.mirror, "notes/a.md")); string(got) != "v2 edited" {
		t.Fatalf("mirror copy = %q, want the edit", got)
	}
	if entries, err := ListConflicts(f.mirror); err != nil || len(entries) != 0 {
		t.Fatalf("a routine update left backups: %v, %v", entries, err)
	}
}

// A propagated deletion and a forced overwrite of a copy changed in the
// mirror are still backed up (#264 AC2).
func TestPushCommand_DeletionAndForcedOverwriteKeepBackups(t *testing.T) {
	requireRsync(t)
	requirePeerRsync(t)
	f := newIntakeFixture(t)
	f.cfg.Propagation.Delete = true
	f.writeLocal("notes/gone.md", "delete me")
	f.writeLocal("notes/both.md", "base")
	f.pushClean(ModeClean)

	if err := os.Remove(filepath.Join(f.local, "notes/gone.md")); err != nil {
		t.Fatal(err)
	}
	f.writeMirror("notes/both.md", "edited in the cloud")
	later(t, filepath.Join(f.mirror, "notes/both.md"), time.Minute)
	f.writeLocal("notes/both.md", "edited here too")
	later(t, filepath.Join(f.local, "notes/both.md"), 2*time.Minute)
	f.pushClean(ModeForce)

	entries, err := ListConflicts(f.mirror)
	if err != nil || len(entries) != 1 {
		t.Fatalf("backups = %v, %v; want one directory", entries, err)
	}
	backup := filepath.Join(entries[0].Path, backupSubFromWorkspace)
	for rel, want := range map[string]string{"notes/gone.md": "delete me", "notes/both.md": "edited in the cloud"} {
		if got, err := os.ReadFile(filepath.Join(backup, rel)); err != nil || string(got) != want {
			t.Errorf("backup of %s = %q, %v; want %q", rel, got, err, want)
		}
	}
}

// Identical bytes under a new mirror mtime do not block a clean push, the
// baseline row records the sha, and a real content change still refuses
// (#265).
func TestPushCommand_MtimeOnlyMirrorChangeIsNotAConflict(t *testing.T) {
	requireRsync(t)
	f := newIntakeFixture(t)
	hook := "claude-hooks/a.sh"
	f.writeLocal(hook, "#!/bin/sh\necho hi\n")
	f.pushClean(ModeClean)
	// The provider rewrites only the timestamp.
	later(t, filepath.Join(f.mirror, hook), time.Hour)

	// Nothing else to send: the run records the sha and moves nothing.
	f.pushClean(ModeClean)
	baseline, err := LoadBaselineManifest(f.cfg.LocalPaths.BaselineFile)
	if err != nil {
		t.Fatal(err)
	}
	if row := baseline[hook]; row.Sha == "" {
		t.Fatalf("baseline row = %+v, want the sha", row)
	}
	f.pushClean(ModeClean) // stable: compared by content again

	// With something else to send the clean push goes through, and rsync's
	// replaced copy of the hook is a routine backup that does not stay.
	f.writeLocal("notes/new.md", "something to send")
	f.pushClean(ModeClean)
	if got, _ := os.ReadFile(filepath.Join(f.mirror, "notes/new.md")); string(got) != "something to send" {
		t.Fatalf("the clean push did not run: %q", got)
	}
	if entries, _ := ListConflicts(f.mirror); len(entries) != 0 {
		t.Fatalf("backups left: %v", entries)
	}

	f.writeMirror(hook, "#!/bin/sh\necho HI\n") // same size, new bytes
	later(t, filepath.Join(f.mirror, hook), 2*time.Hour)
	f.writeLocal(hook, "#!/bin/sh\necho yo\n")
	later(t, filepath.Join(f.local, hook), 3*time.Hour)
	if _, err := PushCommand(context.Background(), PushOptions{Config: f.cfg, Runner: f.runner, Mode: ModeClean}); err == nil || !strings.Contains(err.Error(), "push refused: 1 conflict") {
		t.Fatalf("a real mirror content change = %v, want a refusal", err)
	}
	if st, _ := LoadLocalState(f.cfg.LocalPaths); st.LastPushConflicts != 1 {
		t.Fatalf("live conflicts after the refusal = %d, want 1", st.LastPushConflicts)
	}
}
