package cli

// GUARD-03's sibling for the sync and peer trees, which the apply-shaped guard in
// dryrun_property_test.go never reached. `--dry-run must remain truthful` is a
// promise about every command, and BUG-13 measured twelve files left behind by a
// single `dot peer sync --dry-run` on an empty HOME.
//
// These commands do not honor --home yet (BUG-07/BUG-08, plan 05-04), so the
// sandbox is built from HOME plus the XDG variables the way the GUARD-03 test
// builds it, not by passing the flag.

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/syncer"
)

// syncDryRunSandbox points every home-derived resolution at a fresh temp dir.
// XDG_CONFIG_HOME outranks HOME in config.StateDir and syncer's own path
// resolution, so a HOME-only sandbox would still read the developer's real
// config.
func syncDryRunSandbox(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	return home
}

// TestSyncAndPeer_DryRunLeavesEmptyHomeUntouched pins BUG-13 and D-03.
//
// The exit code is deliberately not asserted. Several of these rows fail on an
// unconfigured target, and that is fine: the store creation BUG-13 records
// happens during profile resolution, before any command body runs, so a run that
// errors out has already written by the time it errors. The claim is about the
// tree.
func TestSyncAndPeer_DryRunLeavesEmptyHomeUntouched(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"sync push", []string{"sync", "push", "--dry-run"}},
		{"peer sync", []string{"peer", "sync", "--dry-run"}},
		// D-03: creating the store is what init is for, but a preview of that
		// creation is still a preview. The ROADMAP note that would have exempted
		// these two is overridden.
		{"sync init", []string{"sync", "init", "--dry-run"}},
		{"peer init", []string{"peer", "init", "--host", "someone@peer.example", "--dry-run"}},
		// BUG-14's cli-level row. It stops at the unconfigured-target check on an
		// empty HOME, which is the point: even a run that refuses must not have
		// written the store on its way to refusing. The plist guard itself is
		// asserted past the whole validation chain in
		// internal/syncer/peer_schedule_dryrun_test.go, where a stub peer can be
		// made to satisfy it.
		{"peer schedule", []string{"peer", "setup", "--interval", "15m", "--dry-run"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := syncDryRunSandbox(t)
			before := snapshotTree(t, home)
			_, _, _ = runDotForTest(tc.args...)
			added := addedSnapshotLines(before, snapshotTree(t, home))
			if len(added) > 0 {
				t.Errorf("dot %s wrote %d entr(ies) into an empty HOME:\n  %s",
					strings.Join(tc.args, " "), len(added), strings.Join(added, "\n  "))
			}
		})
	}
}

func TestSyncSensitiveOverridesDryRunNoMutation(t *testing.T) {
	home := syncDryRunSandbox(t)
	before := snapshotTree(t, home)
	config := &syncer.Config{LocalPath: "/workspace/", MirrorPath: "/mirror/", Propagation: syncer.DefaultPropagationPolicy()}
	var out bytes.Buffer
	render := renderSyncEvent(&Printer{Out: &out, Err: io.Discard}, syncRender{
		cfg:                config,
		mode:               syncer.ModeManual,
		dryRun:             true,
		sensitiveOverrides: []syncer.SensitiveOverride{{AllowPattern: "/.aws/credentials", DenyPattern: "/.aws/credentials"}},
	})
	for _, event := range []syncer.SyncEvent{
		{Kind: syncer.SyncEventPushPlanStart},
		{Kind: syncer.SyncEventDryRunNotice},
		{Kind: syncer.SyncEventPushPlanReady, PushPlan: &syncer.PushPlan{}},
		{Kind: syncer.SyncEventPushSSHStart},
	} {
		render(event)
	}
	if after := snapshotTree(t, home); after != before {
		t.Fatalf("sensitive dry-run diagnostics mutated the sandbox:\nbefore=%s\nafter=%s", before, after)
	}
	if strings.Contains(out.String(), "Apply this push plan?") || strings.Contains(out.String(), "Push to SSH target?") {
		t.Fatalf("sensitive dry-run diagnostics prompted unexpectedly:\n%s", out.String())
	}
}

// TestSyncInit_WithoutDryRunStillCreatesTheStore is the non-vacuity row for the
// preview arms above: without it, a preview arm that never returned to the real
// work would leave every row green while `dot sync init` had stopped working.
func TestSyncInit_WithoutDryRunStillCreatesTheStore(t *testing.T) {
	home := syncDryRunSandbox(t)
	before := snapshotTree(t, home)
	if out, errOut, err := runDotForTest("sync", "init"); err != nil {
		t.Fatalf("dot sync init: %v\nstdout=%s\nstderr=%s", err, out, errOut)
	}
	if added := addedSnapshotLines(before, snapshotTree(t, home)); len(added) == 0 {
		t.Error("dot sync init created nothing, so the dry-run preview arm disabled the store creation outright rather than skipping it for the preview")
	}
}

// TestSyncInit_DryRunPreviewNamesEveryWrite pins what the preview must name.
// A preview that is silent about a write is the same defect as a preview that
// performs one: the operator cannot tell what the real run will touch.
//
// The workspace .gitignore is the one WORKSPACE-level file `dot sync init`
// touches — EnsureLocalLayout ends with appendGitignoreBlock — and everything
// else it creates lives under the store.
func TestSyncInit_DryRunPreviewNamesEveryWrite(t *testing.T) {
	home := syncDryRunSandbox(t)
	out, errOut, err := runDotForTest("sync", "init", "--dry-run")
	if err != nil {
		t.Fatalf("dot sync init --dry-run: %v\nstdout=%s\nstderr=%s", err, out, errOut)
	}

	gitignore := filepath.Join(home, "workspace", "work", ".gitignore")
	if !strings.Contains(out, gitignore) {
		t.Errorf("the preview never names the workspace .gitignore the real run appends to: %s\nstdout=%s", gitignore, out)
	}
}

// TestSyncInit_DryRunPreviewsThePostMigrationWorld is the sharper half.
//
// ResolveLocalPathsForProfile keeps a pre-rename fallback so READ-ONLY commands
// stay truthful before MigrateLegacyStore has run, and plan 05-03 selected that
// read-only resolver for every dry run — including dry runs of mutating
// commands. For a mutating command, truthful means describing the world AFTER
// the migration it performs first: a real `dot sync init` on a legacy workspace
// renames .dotfiles/gdrive-sync to .dotfiles/sync and then operates there.
//
// Machines in that state exist: this repo migrated its own store in 2026-08.
func TestSyncInit_DryRunPreviewsThePostMigrationWorld(t *testing.T) {
	legacyWorkspace := func(t *testing.T) (home, legacy, current string) {
		t.Helper()
		home = syncDryRunSandbox(t)
		root := filepath.Join(home, "workspace", "work")
		legacy = filepath.Join(root, ".dotfiles", "gdrive-sync")
		current = filepath.Join(root, ".dotfiles", "sync")
		return home, legacy, current
	}

	t.Run("legacy store present", func(t *testing.T) {
		_, legacy, current := legacyWorkspace(t)
		if err := os.MkdirAll(legacy, 0o755); err != nil {
			t.Fatal(err)
		}
		before := snapshotTree(t, filepath.Dir(filepath.Dir(legacy)))

		out, errOut, err := runDotForTest("sync", "init", "--dry-run")
		if err != nil {
			t.Fatalf("dot sync init --dry-run: %v\nstdout=%s\nstderr=%s", err, out, errOut)
		}

		if !strings.Contains(out, current) {
			t.Errorf("the preview describes the pre-migration world: it never names %s, which is where the real run operates after MigrateLegacyStore\nstdout=%s", current, out)
		}
		if !strings.Contains(out, legacy) {
			t.Errorf("the preview is silent about the pending rename of %s, which is work the real run would do before anything else\nstdout=%s", legacy, out)
		}
		if after := snapshotTree(t, filepath.Dir(filepath.Dir(legacy))); after != before {
			t.Errorf("the preview renamed or created something\nbefore:\n%s\nafter:\n%s", before, after)
		}
	})

	// Non-vacuity: with nothing to migrate the preview must not invent a
	// rename. Without this row the fix could name the legacy path
	// unconditionally and the row above would still be green.
	t.Run("no legacy store", func(t *testing.T) {
		_, legacy, current := legacyWorkspace(t)
		out, errOut, err := runDotForTest("sync", "init", "--dry-run")
		if err != nil {
			t.Fatalf("dot sync init --dry-run: %v\nstdout=%s\nstderr=%s", err, out, errOut)
		}
		if !strings.Contains(out, current) {
			t.Errorf("the preview does not name the store it would create: %s\nstdout=%s", current, out)
		}
		if strings.Contains(out, legacy) {
			t.Errorf("the preview claims a rename on a workspace with no legacy store\nstdout=%s", out)
		}
	})
}
