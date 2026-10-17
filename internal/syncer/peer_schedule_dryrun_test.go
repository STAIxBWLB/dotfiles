package syncer

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// peerScheduleSandbox builds the one situation PeerSchedule's validation chain
// accepts, which is what makes the no-plist row below non-vacuous: without it
// the chain refuses before reaching the write and the row would pass against a
// completely unguarded on-arm.
//
// The chain needs a reachable peer whose `dot peer status --json` points back at
// this workspace, so PATH is pointed at a directory holding a stub `ssh` that
// prints that document and a stub `launchctl` that does nothing. Both are
// absolute-shebang shell scripts, so emptying PATH does not break them.
func peerScheduleSandbox(t *testing.T) (*Config, string) {
	t.Helper()
	names := MachineNames()
	if len(names) == 0 {
		t.Skip("this host reports no machine name, so CheckOwner cannot be satisfied")
	}
	owner := names[0]

	home := t.TempDir()
	t.Setenv("HOME", home)
	localPath := filepath.Join(home, "workspace", "work")
	cfg := &Config{
		Profile:    PeerProfile,
		Owner:      owner,
		LocalPath:  localPath,
		Target:     Target{Kind: TargetSSH, Host: "coordinator.example", Path: "/remote/workspace/work"},
		LogFile:    filepath.Join(localPath, ".dotfiles", "peer", "log", "peer.log"),
		LocalPaths: ResolveLocalPathsForProfile(localPath, PeerProfile),
	}

	binDir := t.TempDir()
	status := fmt.Sprintf(
		`{"schemaVersion":%d,"kind":"peer","profile":{"configured":true,"owner":%q,"workspacePath":%q,"target":{"path":%q}}}`,
		PeerStatusSchemaVersion, owner, cfg.Target.Path, localPath)
	// `echo` is a shell builtin, so the stub needs nothing else on PATH. The
	// document carries no single quote, so single-quoting it is safe.
	writeStub(t, filepath.Join(binDir, "ssh"), "#!/bin/sh\ncase \"$*\" in\n  *\"list dot candidates\"*) printf '/fake/dot\\tdot version 9.9.9 (fake)\\n' ;;\n  *) echo '"+status+"' ;;\nesac\n")
	writeStub(t, filepath.Join(binDir, "launchctl"), "#!/bin/sh\nif [ -n \"$DOTFILES_TEST_LAUNCHCTL_ARGS\" ]; then printf '%s\\n' \"$*\" >> \"$DOTFILES_TEST_LAUNCHCTL_ARGS\"; fi\nexit 0\n")
	t.Setenv("PATH", binDir)

	return cfg, filepath.Join(home, "Library", "LaunchAgents", "com.dotfiles.peer.plist")
}

func writeStub(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func peerScheduleRunner(dryRun bool) *exec.Runner {
	return exec.NewRunner(dryRun, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// TestPeerSchedule_DryRunWritesNoPlist pins BUG-14. The plist write bypasses the
// runner (os.MkdirAll + os.WriteFile), so a preview left
// ~/Library/LaunchAgents/com.dotfiles.peer.plist on disk with no loaded job
// behind it — precisely the inconsistent state the off-arm thirty lines above
// already refuses to create.
func TestPeerSchedule_DryRunWritesNoPlist(t *testing.T) {
	tests := []struct {
		name          string
		optDryRun     bool
		runnerDryRun  bool
		wantPlistOnFS bool
	}{
		{name: "option set", optDryRun: true, runnerDryRun: true},
		// A caller that set the flag on the runner and not on the option still
		// gets a preview: the same reconciliation the three aisettings managers
		// make.
		{name: "runner flag only", optDryRun: false, runnerDryRun: true},
		// Non-vacuity. Without this row the guard could be a permanent disable
		// and every row above would still be green.
		{name: "no dry-run at all", optDryRun: false, runnerDryRun: false, wantPlistOnFS: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, plist := peerScheduleSandbox(t)
			res, err := PeerSchedule(context.Background(), PeerScheduleOptions{
				Config:   cfg,
				Runner:   peerScheduleRunner(tc.runnerDryRun),
				Probe:    peerScheduleRunner(false),
				Interval: 15 * time.Minute,
				DryRun:   tc.optDryRun,
			})
			if err != nil {
				t.Fatalf("PeerSchedule: %v", err)
			}

			_, statErr := os.Stat(plist)
			if tc.wantPlistOnFS {
				if statErr != nil {
					t.Fatalf("a real run wrote no plist at %s (stat err: %v), so the dry-run guard disabled the install outright", plist, statErr)
				}
				if res.DryRun {
					t.Error("a real run reported DryRun on its result")
				}
				return
			}
			if !os.IsNotExist(statErr) {
				t.Errorf("dry-run wrote the plist it only previews: %s (stat err: %v)", plist, statErr)
			}
			if !res.DryRun {
				t.Error("the result carries no dry-run flag, so cli has nothing to branch on")
			}
			if res.Plist != plist {
				t.Errorf("result names plist %q, want %q — cli renders the path from the result", res.Plist, plist)
			}
		})
	}
}

func TestPeerSchedule_DryRunExplicitHomeReportsTargetUserAction(t *testing.T) {
	for _, mode := range []struct {
		name      string
		optDryRun bool
		runnerDry bool
	}{
		{name: "option-only", optDryRun: true},
		{name: "runner-only", runnerDry: true},
	} {
		for _, off := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/off=%t", mode.name, off), func(t *testing.T) {
				cfg, _ := peerScheduleSandbox(t)
				cfg.Home = t.TempDir()
				plist := filepath.Join(cfg.Home, "Library", "LaunchAgents", "com.dotfiles.peer.plist")
				parent := filepath.Dir(plist)
				const seeded = "seeded explicit-home peer plist"
				if off {
					if err := os.MkdirAll(parent, 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(plist, []byte(seeded), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				record := filepath.Join(t.TempDir(), "launchctl-args")
				t.Setenv("DOTFILES_TEST_LAUNCHCTL_ARGS", record)
				var runnerLog bytes.Buffer
				runner := exec.NewRunner(mode.runnerDry, slog.New(slog.NewTextHandler(&runnerLog, nil)))

				res, err := PeerSchedule(context.Background(), PeerScheduleOptions{
					Config: cfg, Runner: runner, Probe: peerScheduleRunner(false),
					Interval: 15 * time.Minute, Off: off, DryRun: mode.optDryRun,
				})
				if err != nil {
					t.Fatalf("PeerSchedule dry-run: %v", err)
				}
				if res == nil || !res.DryRun || !res.TargetUserActionRequired {
					t.Fatalf("PeerSchedule dry-run result = %+v, want target-user action", res)
				}
				if off {
					body, readErr := os.ReadFile(plist)
					if readErr != nil || string(body) != seeded {
						t.Fatalf("explicit-home off dry-run changed plist: body=%q err=%v", body, readErr)
					}
					entries, readErr := os.ReadDir(parent)
					if readErr != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(plist) {
						t.Fatalf("explicit-home off dry-run changed parent entries: entries=%v err=%v", entries, readErr)
					}
				} else if _, statErr := os.Stat(parent); !os.IsNotExist(statErr) {
					t.Fatalf("explicit-home install dry-run created parent %s: %v", parent, statErr)
				}
				if got, readErr := os.ReadFile(record); readErr == nil || !os.IsNotExist(readErr) {
					t.Fatalf("explicit-home dry-run invoked caller launchctl: %q (read error %v)", got, readErr)
				}
				if strings.Contains(runnerLog.String(), "launchctl") {
					t.Fatalf("explicit-home runner dry-run attempted launchctl: %s", runnerLog.String())
				}
			})
		}
	}
}

func TestPeerSchedule_OffRunnerDryRunKeepsPlist(t *testing.T) {
	cfg, plist := peerScheduleSandbox(t)
	if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
		t.Fatal(err)
	}
	const seeded = "seeded peer plist"
	if err := os.WriteFile(plist, []byte(seeded), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := PeerSchedule(context.Background(), PeerScheduleOptions{
		Config: cfg, Runner: peerScheduleRunner(true), Probe: peerScheduleRunner(false),
		Off: true, DryRun: false,
	})
	if err != nil {
		t.Fatalf("PeerSchedule runner-only dry-run off: %v", err)
	}
	if res == nil || !res.Off || !res.DryRun || res.Plist != plist {
		t.Fatalf("PeerSchedule runner-only dry-run off result = %+v", res)
	}
	if body, readErr := os.ReadFile(plist); readErr != nil || string(body) != seeded {
		t.Fatalf("runner-only dry-run off changed plist: body=%q err=%v", body, readErr)
	}
}

// TestPeerSchedule_ValidationPrecedenceUnderDryRun holds the guard's POSITION,
// not just its existence. A preview that skipped the validation chain would be a
// different lie from the one BUG-14 fixes, and the off-arm's own precedence
// (validation sits AFTER it, so `--off --interval 1s` succeeds today) must not
// move either.
func TestPeerSchedule_ValidationPrecedenceUnderDryRun(t *testing.T) {
	t.Run("dry-run still fails a bad interval", func(t *testing.T) {
		cfg, plist := peerScheduleSandbox(t)
		_, err := PeerSchedule(context.Background(), PeerScheduleOptions{
			Config:   cfg,
			Runner:   peerScheduleRunner(true),
			Probe:    peerScheduleRunner(false),
			Interval: time.Second,
			DryRun:   true,
		})
		if err == nil {
			t.Fatal("a dry run accepted --interval 1s, so the guard was placed above the validation chain")
		}
		if _, statErr := os.Stat(plist); !os.IsNotExist(statErr) {
			t.Errorf("a rejected dry run still wrote %s", plist)
		}
	})

	t.Run("off arm still precedes validation", func(t *testing.T) {
		cfg, plist := peerScheduleSandbox(t)
		res, err := PeerSchedule(context.Background(), PeerScheduleOptions{
			Config:   cfg,
			Runner:   peerScheduleRunner(true),
			Probe:    peerScheduleRunner(false),
			Interval: time.Second, // below the minimum, and deliberately ignored
			Off:      true,
			DryRun:   true,
		})
		if err != nil {
			t.Fatalf("--off --interval 1s must still succeed: %v", err)
		}
		if !res.Off || !res.DryRun || res.Plist != plist {
			t.Errorf("off-arm result changed shape: %+v", res)
		}
	})
}

func TestPeerSchedule_RejectsInvalidHomeBeforeMutation(t *testing.T) {
	for _, invalidHome := range []string{
		string([]byte{'/', 't', 'm', 'p', '/', 0xff}),
		"/tmp/control\x01home",
	} {
		for _, off := range []bool{false, true} {
			t.Run(fmt.Sprintf("off=%t home=%q", off, invalidHome), func(t *testing.T) {
				cfg, _ := peerScheduleSandbox(t)
				cfg.Home = invalidHome
				plist := filepath.Join(cfg.HomeDir(), "Library", "LaunchAgents", "com.dotfiles.peer.plist")
				const seeded = "seeded peer plist"
				seededOnFS := os.MkdirAll(filepath.Dir(plist), 0o755) == nil
				if seededOnFS {
					seededOnFS = os.WriteFile(plist, []byte(seeded), 0o644) == nil
				}

				res, err := PeerSchedule(context.Background(), PeerScheduleOptions{
					Config: cfg, Runner: peerScheduleRunner(false), Probe: peerScheduleRunner(false),
					Interval: time.Hour, Off: off,
				})
				if err == nil {
					t.Fatal("invalid home unexpectedly reached peer scheduler mutation")
				}
				if res != nil {
					t.Fatalf("rejected peer scheduler returned result: %+v", res)
				}
				if seededOnFS {
					if got, readErr := os.ReadFile(plist); readErr != nil || string(got) != seeded {
						t.Fatalf("rejection changed seeded plist: got %q, read error %v", got, readErr)
					}
				} else if _, statErr := os.Stat(plist); !os.IsNotExist(statErr) {
					t.Fatalf("rejection created unseedable invalid-byte plist: %v", statErr)
				}
				for _, want := range []string{fmt.Sprintf("%q", invalidHome), plist, "XML 1.0", "left untouched", "dot peer setup"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q missing %q", err, want)
					}
				}
			})
		}
	}
}
