package syncer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPickRemoteDot_PrefersNewestReleaseOverStaleDevBuild(t *testing.T) {
	tests := []struct {
		name, probe, want string
		passed            int
	}{
		{"stale dev build first", "/h/.local/bin/dot\tdot version dev (f467e65)\n/opt/homebrew/bin/dot\tdot version 2.70.22 (388a398)\n", "/opt/homebrew/bin/dot", 1},
		{"older release first", "/h/.local/bin/dot\tdot version 2.70.10 (aaa)\n/opt/homebrew/bin/dot\tdot version 2.70.22 (bbb)\n", "/opt/homebrew/bin/dot", 1},
		{"newest first stays", "/h/.local/bin/dot\tdot version 2.71.0 (aaa)\n/opt/homebrew/bin/dot\tdot version 2.70.22 (bbb)\n", "/h/.local/bin/dot", 1},
		{"equal releases keep order, the copy is not listed", "/h/.local/bin/dot\tdot version 2.70.22 (x)\n/opt/homebrew/bin/dot\tdot version 2.70.22 (x)\n", "/h/.local/bin/dot", 0},
		{"only a dev build", "/h/.local/bin/dot\tdot version dev (f467e65)\n", "/h/.local/bin/dot", 0},
		{"command -v duplicate", "/opt/homebrew/bin/dot\tdot version 2.70.22 (x)\n/opt/homebrew/bin/dot\tdot version 2.70.22 (x)\n", "/opt/homebrew/bin/dot", 0},
		{"graphviz dot ignored", "/usr/bin/dot\t\n/opt/homebrew/bin/dot\tdot version 2.70.22 (x)\n", "/opt/homebrew/bin/dot", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := pickRemoteDot(tt.probe, false)
			if err != nil {
				t.Fatal(err)
			}
			if got.Path != tt.want || len(got.Passed) != tt.passed {
				t.Fatalf("picked %s passed %v, want %s with %d passed", got, got.Passed, tt.want, tt.passed)
			}
		})
	}
	if _, err := pickRemoteDot("\n", false); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("empty probe: err = %v", err)
	}
	if got, _ := pickRemoteDot("/h/.local/bin/dot\tdot version dev (x)\n", false); !got.Unreleased() {
		t.Fatal("an unpinned dev build is not flagged")
	}
	if got, _ := pickRemoteDot("/h/dev/dot\tdot version dev (x)\n", true); got.Unreleased() {
		t.Fatal("a pinned dev build is flagged")
	}
	if got := remoteShellPath("~/bin/dot"); got != `"$HOME"/'bin/dot'` {
		t.Fatalf("remoteShellPath = %s", got)
	}
}

func TestDotVersionsDiffer(t *testing.T) {
	rel := &remoteDot{release: []int{2, 70, 22}}
	for _, tt := range []struct {
		local string
		peer  *remoteDot
		want  bool
	}{
		{"2.70.22 (388a398)", rel, false},
		{"2.70.21 (f467e65)", rel, true},
		{"dev (f467e65)", rel, false},
		{"2.70.22 (x)", &remoteDot{}, false},
		{"", rel, false},
	} {
		if got := dotVersionsDiffer(tt.local, tt.peer); got != tt.want {
			t.Errorf("dotVersionsDiffer(%q, %v) = %v, want %v", tt.local, tt.peer.release, got, tt.want)
		}
	}
}

// AC1: a stale dev build at the first candidate and a newer release at the
// second. The remote status document, and therefore the fence, must come
// from the release. Every remote command runs for real through the ssh stub,
// so the document is whatever the chosen binary prints.
func TestRemoteStatusAndFenceUseTheNewestRelease(t *testing.T) {
	bin := t.TempDir()
	writeStub(t, filepath.Join(bin, "ssh"), "#!/bin/sh\n"+
		"while [ $# -gt 0 ]; do case \"$1\" in -o) shift 2 ;; fake-peer) shift; break ;; *) shift ;; esac; done\n"+
		"exec /bin/sh -c \"$*\"\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg := &Config{Profile: PeerProfile, Owner: "mac-a", OwnerEpoch: 1, LocalPath: "/w/", Target: Target{Kind: TargetSSH, Host: "fake-peer", Path: "/p"}}
	doc := func(extra string) string {
		return fmt.Sprintf(`{"schemaVersion":%d,"kind":"peer"%s,"profile":{"configured":true,"owner":"mac-a","workspacePath":"/p","target":{"path":"/w"}}}`, PeerStatusSchemaVersion, extra)
	}
	stale := filepath.Join(t.TempDir(), "dot")
	writeStub(t, stale, "#!/bin/sh\n[ \"$1\" = --version ] && { echo 'dot version dev (f922760)'; exit 0; }\necho '"+doc("")+"'\n")
	release := filepath.Join(t.TempDir(), "dot")
	writeStub(t, release, "#!/bin/sh\n[ \"$1\" = --version ] && { echo 'dot version 2.70.22 (388a398)'; exit 0; }\necho '"+doc(`,"ownerEpoch":1,"dotVersion":"2.70.22 (388a398)"`)+"'\n")
	useRemoteDotCandidates(t, stale, release)

	status, err := fetchRemotePeerStatus(context.Background(), peerScheduleRunner(false), cfg)
	if err != nil {
		t.Fatalf("fetchRemotePeerStatus: %v", err)
	}
	if status.DotVersion != "2.70.22 (388a398)" || status.OwnerEpoch != 1 {
		t.Fatalf("status came from the stale build: %+v", status)
	}
	if _, err := peerFence(cfg, status); err != nil {
		t.Fatalf("fence refused the release build: %v", err)
	}
	if cfg.remoteDot.Path != release || len(cfg.remoteDot.Passed) != 1 || !strings.Contains(cfg.remoteDot.Passed[0], stale) {
		t.Fatalf("resolved %s passed %v", cfg.remoteDot, cfg.remoteDot.Passed)
	}

	// remote_dot pins a dev build explicitly; a bad pin names the pin.
	cfg.remoteDot, cfg.RemoteDot = nil, stale
	if dot, err := resolveRemoteDot(context.Background(), peerScheduleRunner(false), cfg); err != nil || dot.Path != stale || dot.Unreleased() {
		t.Fatalf("pinned dot = %v, %v; want %s", dot, err, stale)
	}
	cfg.remoteDot, cfg.RemoteDot = nil, stale+"-missing"
	if _, err := resolveRemoteDot(context.Background(), peerScheduleRunner(false), cfg); err == nil || !strings.Contains(err.Error(), "remote_dot") {
		t.Fatalf("bad pin: err = %v", err)
	}
}

// Only a dev build and no pin: the sync says so instead of falling back
// silently.
func TestPeerSync_ReportsUnreleasedPeerDot(t *testing.T) {
	sb := newPeerHandoverSandbox(t, peerStatusFields{epoch: 1, dotVersion: "dev (abc)"}, 1)
	dev := filepath.Join(t.TempDir(), "dot")
	writeStub(t, dev, "#!/bin/sh\n[ \"$1\" = --version ] && echo 'dot version dev (abc)'\nexit 0\n")
	useRemoteDotCandidates(t, dev)
	var got string
	if _, err := PeerSync(context.Background(), PeerSyncOptions{
		Config: sb.cfg, Runner: peerScheduleRunner(true), Probe: peerScheduleRunner(false), DryRun: true, SkipHome: true,
		LocalDotVersion: "2.70.22 (x)",
		Progress: func(e PeerEvent) {
			if e.Kind == PeerEventPeerDotUnreleased {
				got = e.Path
			}
		},
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, dev) || !strings.Contains(got, "dev (abc)") {
		t.Fatalf("no unreleased notice naming %s; got %q", dev, got)
	}
}

func TestPeerSync_ReportsPeerOnADifferentRelease(t *testing.T) {
	sb := newPeerHandoverSandbox(t, peerStatusFields{epoch: 1, dotVersion: "9.9.9 (fake)"}, 1)
	var mismatch string
	_, err := PeerSync(context.Background(), PeerSyncOptions{
		Config: sb.cfg, Runner: peerScheduleRunner(true), Probe: peerScheduleRunner(false),
		DryRun: true, SkipHome: true, LocalDotVersion: "2.70.22 (388a398)",
		Progress: func(e PeerEvent) {
			if e.Kind == PeerEventDotVersionMismatch {
				mismatch = e.Path
			}
		},
	})
	if err != nil {
		t.Fatalf("PeerSync: %v", err)
	}
	if !strings.Contains(mismatch, "dot version 9.9.9 (fake)") {
		t.Fatalf("no version-mismatch notice naming the peer binary; got %q", mismatch)
	}
}

// AC2: doctor prints both binaries and flags the mismatch.
func TestPeerDoctor_ReportsLocalAndPeerDot(t *testing.T) {
	cfg, _ := peerDryRunSandbox(t)
	report, err := PeerDoctor(context.Background(), PeerDoctorOptions{
		Config: cfg, Probe: peerScheduleRunner(false), LocalDotVersion: "2.70.22 (388a398)",
	})
	if err != nil {
		t.Fatalf("PeerDoctor: %v", err)
	}
	if report.LocalDotPath == "" || report.LocalDotVersion != "2.70.22 (388a398)" {
		t.Errorf("local dot missing: %q %q", report.LocalDotPath, report.LocalDotVersion)
	}
	if !strings.Contains(report.RemoteDot, "dot version 9.9.9 (fake)") || !report.DotMismatch {
		t.Errorf("peer dot = %q mismatch=%v; want the fake release flagged", report.RemoteDot, report.DotMismatch)
	}
}

// The handover warns only for a peer release older than this one; equal,
// newer and dev builds on either side stay silent (#196).
func TestPeerDotOlder(t *testing.T) {
	for _, tc := range []struct {
		local, banner string
		warns         bool
	}{
		{"2.70.22 (x)", "dot version 2.70.21 (y)", true},
		{"2.70.22 (x)", "dot version 2.70.22 (y)", false},
		{"2.70.22 (x)", "dot version 2.71.0 (y)", false},
		{"2.70.22 (x)", "dot version dev (y)", false},
		{"dev (x)", "dot version 2.70.21 (y)", false},
		{"", "dot version 2.70.21 (y)", false},
	} {
		remote, err := pickRemoteDot("/opt/homebrew/bin/dot\t"+tc.banner+"\n", false)
		if err != nil {
			t.Fatal(err)
		}
		if got := peerDotOlder(tc.local, remote) != ""; got != tc.warns {
			t.Errorf("local %q peer %q: warns %v, want %v", tc.local, tc.banner, got, tc.warns)
		}
	}
}

// A failed probe says what ssh said, not the probe script, and keeps ssh's
// exit status for dot's own exit code.
func TestProbePeerDotFailureKeepsTheSSHStatus(t *testing.T) {
	for _, stderr := range []string{"ssh: connect to host fake-peer: Connection refused", ""} {
		bin := t.TempDir()
		script := "#!/bin/sh\nexit 255\n"
		if stderr != "" {
			script = "#!/bin/sh\necho '" + stderr + "' >&2\nexit 255\n"
		}
		writeStub(t, filepath.Join(bin, "ssh"), script)
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

		_, err := ResolvePeerDotPath(context.Background(), peerScheduleRunner(false), "fake-peer", "")
		var exitCoder interface{ ExitCode() int }
		if err == nil || !errors.As(err, &exitCoder) || exitCoder.ExitCode() != 255 {
			t.Fatalf("stderr %q: err = %v; want ssh's exit status 255 in the chain", stderr, err)
		}
		msg := err.Error()
		if strings.Contains(msg, "dot-peer: list dot candidates") || strings.HasSuffix(msg, ": ") || (stderr != "" && !strings.Contains(msg, stderr)) {
			t.Fatalf("stderr %q: message %q", stderr, msg)
		}
	}
}
