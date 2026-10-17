package syncer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/config"
)

func TestGetStatus_IgnoresStaleLock(t *testing.T) {
	f := newIntakeFixture(t)
	if err := os.MkdirAll(f.cfg.LockDir, 0o755); err != nil {
		t.Fatalf("seed lock dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(f.cfg.LockDir, "lock.pid"), []byte("99999999"), 0o644); err != nil {
		t.Fatalf("seed lock pid: %v", err)
	}

	st, err := GetStatus(context.Background(), f.runner, f.cfg, &config.UserState{}, nil)
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.LockHeld {
		t.Fatal("stale lock reported as held")
	}
}

func TestGetStatus_DoesNotReportUnavailableRsync(t *testing.T) {
	for _, mode := range []string{"missing", "probe fails"} {
		t.Run(mode, func(t *testing.T) {
			f := newIntakeFixture(t)
			t.Setenv("PATH", t.TempDir())
			if mode == "probe fails" {
				f.cfg.RsyncPath = filepath.Join(t.TempDir(), "rsync")
				if err := os.WriteFile(f.cfg.RsyncPath, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			st, err := GetStatus(context.Background(), f.runner, f.cfg, &config.UserState{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if st.RsyncPath != "" || st.RsyncVersion != "" {
				t.Fatalf("unavailable client reported: path=%q version=%q", st.RsyncPath, st.RsyncVersion)
			}
		})
	}
}

func TestGetStatus_SSHPeerCountsLocalConflictsOnce(t *testing.T) {
	f := newIntakeFixture(t)
	f.cfg.Profile = PeerProfile
	f.cfg.Target = Target{Kind: TargetSSH, Host: "peer", Path: "/work"}
	f.cfg.MirrorPath = ""
	conflict := filepath.Join(f.local, conflictsDirName, "2026-08-04T00-00-00Z")
	if err := os.MkdirAll(conflict, 0o755); err != nil {
		t.Fatal(err)
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(f.local); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })

	st, err := GetStatus(context.Background(), f.runner, f.cfg, &config.UserState{}, nil)
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if got := len(st.Conflicts); got != 1 {
		t.Fatalf("SSH peer conflicts = %d, want 1", got)
	}
}

// Status reports the conflicts the last push refused on apart from the
// retained backup directories and their size (#264).
func TestGetStatus_LiveConflictsAndBackupBytes(t *testing.T) {
	f := newIntakeFixture(t)
	dir := filepath.Join(f.mirror, conflictsDirName, "20261018T043113.000", backupSubFromWorkspace)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := UpdateLocalState(f.cfg.LocalPaths, func(s *LocalState) { s.LastPushConflicts = 3 }); err != nil {
		t.Fatal(err)
	}
	st, err := GetStatus(context.Background(), f.runner, f.cfg, &config.UserState{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.LiveConflicts != 3 || len(st.Conflicts) != 1 || st.BackupBytes != 5 {
		t.Fatalf("live = %d, backups = %d, bytes = %d; want 3, 1, 5", st.LiveConflicts, len(st.Conflicts), st.BackupBytes)
	}
}
