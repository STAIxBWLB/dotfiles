package syncer

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// replaceWithSymlink turns the workspace file rel into a symlink to target,
// relative to rel's folder, as a project that dedupes a clip does.
func (f *intakeFixture) replaceWithSymlink(rel, target string) {
	f.t.Helper()
	abs := filepath.Join(f.local, rel)
	if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
		f.t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Symlink(target, abs); err != nil {
		f.t.Fatal(err)
	}
}

const shadowRel = "video/cut/clips/day.mp4"

// shadowFixture has the clip pushed once as a file, then replaced by a
// symlink to the shared copy; proven seeds the baseline row that push wrote.
func shadowFixture(t *testing.T, proven bool) *intakeFixture {
	t.Helper()
	f := newIntakeFixture(t)
	f.cfg.Propagation.Delete = true
	f.writeLocal("video/clips/day.mp4", "clip")
	mtime := f.writeMirror(shadowRel, "clip")
	if proven {
		f.seedBaseline(shadowRel, "clip", mtime)
	}
	f.replaceWithSymlink(shadowRel, "../../clips/day.mp4")
	return f
}

func TestPlanPush_AFileReplacedByASymlink(t *testing.T) {
	t.Run("delete propagation off", func(t *testing.T) {
		f := shadowFixture(t, true)
		f.cfg.Propagation.Delete = false
		plan, err := PlanPush(f.cfg)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(plan.SkippedPolicy, shadowRel) || len(plan.Shadowed) != 0 || len(plan.Conflicts) != 0 {
			t.Errorf("SkippedPolicy = %v, Shadowed = %v, Conflicts = %+v", plan.SkippedPolicy, plan.Shadowed, plan.Conflicts)
		}
	})
	t.Run("baseline proves the copy", func(t *testing.T) {
		plan, err := PlanPush(shadowFixture(t, true).cfg)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(plan.Shadowed, []string{shadowRel}) || len(plan.Conflicts) != 0 || !plan.HasChanges() {
			t.Errorf("Shadowed = %v, Conflicts = %+v", plan.Shadowed, plan.Conflicts)
		}
	})
	t.Run("no baseline", func(t *testing.T) {
		plan, err := PlanPush(shadowFixture(t, false).cfg)
		if err != nil {
			t.Fatal(err)
		}
		if len(plan.Shadowed) != 0 || len(plan.Conflicts) != 1 || plan.Conflicts[0].RelPath != shadowRel || !plan.Conflicts[0].LocalSymlink {
			t.Errorf("Shadowed = %v, Conflicts = %+v", plan.Shadowed, plan.Conflicts)
		}
	})
}

func TestPushCommand_MovesAMirrorFileTheWorkspaceReplacedWithASymlink(t *testing.T) {
	requireRsync(t)
	f := shadowFixture(t, true)
	var moved SyncEvent
	_, err := PushCommand(context.Background(), PushOptions{
		Config: f.cfg, Runner: f.runner, Mode: ModeClean,
		Progress: func(e SyncEvent) {
			if e.Kind == SyncEventShadowedMoved {
				moved = e
			}
		},
	})
	if err != nil {
		t.Fatalf("PushCommand: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(f.mirror, shadowRel)); !os.IsNotExist(err) {
		t.Errorf("the mirror still holds the replaced file (err=%v)", err)
	}
	if moved.Candidates != 1 || !strings.HasPrefix(moved.Path, filepath.Join(f.local, ".sync-conflicts")) {
		t.Fatalf("moved event = %+v", moved)
	}
	if body, err := os.ReadFile(filepath.Join(moved.Path, shadowRel)); err != nil || string(body) != "clip" {
		t.Errorf("backup copy = %q, %v", body, err)
	}
	if info, err := os.Lstat(filepath.Join(f.local, shadowRel)); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the workspace symlink was touched: %v", err)
	}
	baseline, _ := LoadBaselineManifest(f.cfg.LocalPaths.BaselineFile)
	if _, ok := baseline[shadowRel]; ok {
		t.Error("the baseline still lists the moved file")
	}
	plan, err := PlanPush(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if plan.HasChanges() || plan.HasConflicts() {
		t.Errorf("next plan = %+v, want nothing to do", plan)
	}
}

func TestPushCommand_LeavesAShadowedFileEditedDuringThePush(t *testing.T) {
	requireRsync(t)
	f := shadowFixture(t, true)
	mirrorAbs := filepath.Join(f.mirror, shadowRel)
	f.cfg.RsyncPath = writeRsyncThen(t, `echo "edited in the cloud" >> "`+mirrorAbs+`"`)
	if _, err := PushCommand(context.Background(), PushOptions{Config: f.cfg, Runner: f.runner, Mode: ModeClean}); err != nil {
		t.Fatalf("PushCommand: %v", err)
	}
	if _, err := os.Stat(mirrorAbs); err != nil {
		t.Errorf("the edited copy left the mirror: %v", err)
	}
}

func TestPushCommand_OnlyForceMovesAnUnprovenShadowedFile(t *testing.T) {
	requireRsync(t)
	f := shadowFixture(t, false)
	run := func(mode RunMode) error {
		_, err := PushCommand(context.Background(), PushOptions{Config: f.cfg, Runner: f.runner, Mode: mode})
		return err
	}
	if err := run(ModeClean); err == nil || !strings.Contains(err.Error(), "push refused: 1 conflict") {
		t.Fatalf("clean push = %v, want a refusal", err)
	}
	if err := run(ModeForce); err != nil {
		t.Fatalf("force push: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(f.mirror, shadowRel)); !os.IsNotExist(err) {
		t.Errorf("force left the replaced file in the mirror (err=%v)", err)
	}
	if err := run(ModeClean); err != nil {
		t.Errorf("clean push after force: %v", err)
	}
}

func TestPushCommand_ForceLeavesAShadowedPlaceholder(t *testing.T) {
	requireRsync(t)
	f := newIntakeFixture(t)
	f.cfg.Propagation.Delete = true
	f.writeLocal("video/clips/day.mp4", "clip")
	f.writeMirror(shadowRel, "")
	makePlaceholder(t, filepath.Join(f.mirror, shadowRel))
	f.replaceWithSymlink(shadowRel, "../../clips/day.mp4")
	if _, err := PushCommand(context.Background(), PushOptions{Config: f.cfg, Runner: f.runner, Mode: ModeForce}); err != nil {
		t.Fatalf("force push: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(f.mirror, shadowRel)); err != nil {
		t.Errorf("force moved a cloud placeholder out of the mirror: %v", err)
	}
}
