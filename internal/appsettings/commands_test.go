package appsettings

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// TestStampLastBackup_ReportsWhetherTheWriteLanded pins BUG-11. The caller
// (internal/cli/apps_backup.go) records state.Modules.MacApps.LastBackup on
// the strength of the returned flag alone, so a flag that says "stamped"
// after a failed write makes user state claim a backup with nothing on disk
// behind it.
//
// The unwritable-root row is the non-vacuous one: before the fix it returned
// true alongside the error.
func TestStampLastBackup_ReportsWhetherTheWriteLanded(t *testing.T) {
	for _, row := range []struct {
		name        string
		dryRun      bool
		failed      int
		createRoot  bool
		wantStamped bool
		wantErr     bool
	}{
		{name: "successful write stamps", createRoot: true, wantStamped: true},
		{name: "unwritable root does not stamp", wantErr: true},
		{name: "dry run does not stamp", dryRun: true, createRoot: true},
		{name: "failed paths do not stamp", failed: 1, createRoot: true},
	} {
		t.Run(row.name, func(t *testing.T) {
			home := t.TempDir()
			logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
			eng := &Engine{
				Runner:   exec.NewRunner(row.dryRun, logger),
				HomeDir:  home,
				Root:     filepath.Join(home, "bk"),
				Hostname: "h",
				Manifest: &Manifest{},
			}
			if row.createRoot {
				if err := os.MkdirAll(eng.HostRoot(), 0o755); err != nil {
					t.Fatal(err)
				}
			}

			stamped, err := eng.StampLastBackup(&Summary{Failed: row.failed, Files: 2}, []string{"a"})
			if row.wantErr && err == nil {
				t.Fatal("writing into a host root that does not exist should fail")
			}
			if !row.wantErr && err != nil {
				t.Fatalf("StampLastBackup: %v", err)
			}
			if stamped != row.wantStamped {
				t.Errorf("stamped = %v, want %v — the caller records a last-backup in user state on this flag alone (BUG-11)", stamped, row.wantStamped)
			}

			// The flag and the disk must agree in both directions.
			got, readErr := eng.ReadLastBackupStamp()
			if readErr != nil {
				t.Fatalf("ReadLastBackupStamp: %v", readErr)
			}
			if row.wantStamped && got == nil {
				t.Error("reported stamped, but no stamp is on disk")
			}
			if !row.wantStamped && got != nil {
				t.Errorf("reported not stamped, but a stamp is on disk: %+v", got)
			}
		})
	}
}

// stubBrew installs a fake `brew` on an otherwise EMPTY PATH and returns a
// Brew wired to it. The PATH must hold nothing else: brewProgram asks PATH
// first and falls back to stat-ing /opt/homebrew/bin, so a developer machine
// with a real Homebrew would otherwise answer these rows with live state.
func stubBrew(t *testing.T, script string) *exec.Brew {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "brew"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	b := exec.NewBrew(exec.NewRunner(false, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))))
	if !b.IsAvailable() {
		t.Fatal("the stub brew is not reachable, so this row would exercise the brew-absent path instead of the one under test")
	}
	return b
}

// TestStatusReport_InstallStateHasThreeCases pins the signal BUG-10's third
// glyph rests on. StatusApp.InstallKnown is what tells the operator whether
// dot could ask Homebrew at all, and a failed `brew list --cask -1` must not
// be reported as a confident "not installed" -- which is what it became once
// the not-installed arm started rendering MarkAbsent instead of MarkPartial.
//
// The failing-query row is red before the fix: InstalledCasks returned an
// empty non-nil map on error, so `installed != nil` was always true.
func TestStatusReport_InstallStateHasThreeCases(t *testing.T) {
	for _, row := range []struct {
		name          string
		script        string
		wantInstalled bool
		wantKnown     bool
	}{
		{"brew healthy and the cask is installed", "#!/bin/sh\necho moom\n", true, true},
		{"brew healthy and the cask is not installed", "#!/bin/sh\nexit 0\n", false, true},
		{"brew query fails", "#!/bin/sh\nexit 1\n", false, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			home := t.TempDir()
			mf := &Manifest{Apps: []AppEntry{{
				Token: "moom",
				Paths: []PathEntry{{Type: "pref", Path: "Preferences/com.test.moom.plist"}},
			}}}
			eng := newRoundtripEngine(t, home, mf)

			res := eng.StatusReport(StatusOptions{Brew: stubBrew(t, row.script)})
			if len(res.Apps) != 1 {
				t.Fatalf("expected one app in the report, got %d", len(res.Apps))
			}
			got := res.Apps[0]
			if got.Installed != row.wantInstalled {
				t.Errorf("Installed = %v, want %v", got.Installed, row.wantInstalled)
			}
			if got.InstallKnown != row.wantKnown {
				t.Errorf("InstallKnown = %v, want %v -- a failed brew query must not be reported as a confident install state (BUG-10)", got.InstallKnown, row.wantKnown)
			}
		})
	}
}
