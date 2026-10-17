package syncer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"golang.org/x/text/unicode/norm"
)

const openrsyncBanner = "openrsync: protocol version 29\nrsync version 2.6.9 compatible"

func TestMirrorPush_CanceledProbeKeepsCancellation(t *testing.T) {
	f := newIntakeFixture(t)
	f.cfg.Propagation.Delete = true
	withLocalRsyncCandidates(t, filepath.Join(t.TempDir(), "missing"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := PushCommand(ctx, PushOptions{Config: f.cfg, Runner: f.runner, Mode: ModeClean})
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "push interrupted") || strings.Contains(err.Error(), "brew install") {
		t.Fatalf("want cancellation without installation advice, got %v", err)
	}
	state, err := LoadLocalState(f.cfg.LocalPaths)
	if err != nil || !strings.Contains(state.LastPushError, "context canceled") || strings.Contains(state.LastPushError, "brew install") {
		t.Fatalf("recorded refusal = %+v, %v", state, err)
	}
}

func TestMirrorPush_OldRsyncStillWorksWithoutDeletes(t *testing.T) {
	real := requirePeerRsync(t)
	f := newIntakeFixture(t)
	stub := filepath.Join(t.TempDir(), "rsync")
	writeStub(t, stub, "#!/bin/sh\nif [ \"$1\" = --version ]; then\n  printf '%s\\n' '"+openrsyncBanner+"'\nelse\n  exec '"+real+"' \"$@\"\nfi\n")
	withLocalRsyncCandidates(t, stub)
	f.cfg.RsyncPath = stub
	f.writeLocal("notes/new.md", "new")
	if _, err := PushCommand(context.Background(), PushOptions{Config: f.cfg, Runner: f.runner, Mode: ModeClean}); err != nil {
		t.Fatalf("push without deletes refused: %v", err)
	}
	if body, err := os.ReadFile(filepath.Join(f.mirror, "notes/new.md")); err != nil || string(body) != "new" {
		t.Fatalf("mirror = %q, %v", body, err)
	}
}

func TestMirrorPush_RefusesOldRsyncBeforeTransfer(t *testing.T) {
	for _, mode := range []string{"real", "dry-run", "paused"} {
		t.Run(mode, func(t *testing.T) {
			f := newIntakeFixture(t)
			stub := openrsyncOnPath(t, "")
			withLocalRsyncCandidates(t, "rsync")
			f.cfg.RsyncPath = stub
			f.cfg.Propagation.Delete = true
			f.cfg.Paused = mode == "paused"
			f.writeLocal("notes/new.md", "new")
			_, err := PushCommand(context.Background(), PushOptions{Config: f.cfg, Runner: f.runner, Mode: ModeClean, DryRun: mode == "dry-run"})
			if err == nil || !strings.Contains(err.Error(), "rsync 3.x is needed to delete with a backup") || strings.Contains(err.Error(), "exit status 97") {
				t.Fatalf("want pre-transfer refusal, got %v", err)
			}
			if _, err := os.Stat(filepath.Join(f.mirror, "notes/new.md")); !os.IsNotExist(err) {
				t.Fatalf("refused push transferred a file: %v", err)
			}
			state, _ := LoadLocalState(f.cfg.LocalPaths)
			if mode == "real" {
				if state.LastPushAttempt.IsZero() || !strings.Contains(state.LastPushError, "brew install rsync") {
					t.Fatalf("refusal not recorded: %+v", state)
				}
			} else if !state.LastPushAttempt.IsZero() || state.LastPushError != "" {
				t.Fatalf("%s recorded a refusal: %+v", mode, state)
			}
		})
	}
}

func TestMirrorPush_UsesFallbackRsyncForBackedUpDeletes(t *testing.T) {
	real := requirePeerRsync(t)
	f := newIntakeFixture(t)
	stub := openrsyncOnPath(t, "")
	withLocalRsyncCandidates(t, "rsync", real)
	f.cfg.RsyncPath = stub
	f.cfg.Propagation.Delete = true
	f.writeLocal("notes/gone.md", "original")
	clean := PushOptions{Config: f.cfg, Runner: f.runner, Mode: ModeClean}
	if _, err := PushCommand(context.Background(), clean); err != nil {
		t.Fatal(err)
	}
	if f.cfg.RsyncPath != real {
		t.Fatalf("client = %q, want fallback %q", f.cfg.RsyncPath, real)
	}
	if err := os.Remove(filepath.Join(f.local, "notes/gone.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := PushCommand(context.Background(), clean); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.mirror, "notes/gone.md")); !os.IsNotExist(err) {
		t.Fatalf("delete did not apply: %v", err)
	}
	backups, err := filepath.Glob(filepath.Join(f.mirror, ".sync-conflicts", "*", "from-workspace", "notes", "gone.md"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("want one deletion backup: %v, %v", backups, err)
	}
	if body, err := os.ReadFile(backups[0]); err != nil || string(body) != "original" {
		t.Fatalf("backup = %q, %v", body, err)
	}
	// Status resolves from the same PATH and fallbacks without mutating cfg.
	f.cfg.RsyncPath = stub
	status, err := GetStatus(context.Background(), f.runner, f.cfg, &config.UserState{}, nil)
	if err != nil || status.RsyncPath != real || !remoteRsyncUsable(status.RsyncVersion) || f.cfg.RsyncPath != stub {
		t.Fatalf("status = %+v, %v; cfg client = %q", status, err, f.cfg.RsyncPath)
	}
}

// requirePeerRsync skips a test that drives a real peer run when this host
// has no rsync 3.x client: peer runs refuse openrsync (#175).
func requirePeerRsync(t *testing.T) string {
	t.Helper()
	path, _, err := LocalRsyncPath(context.Background(), peerScheduleRunner(false))
	if err != nil {
		t.Skipf("peer runs need rsync 3.x: %v", err)
	}
	return path
}

func withLocalRsyncCandidates(t *testing.T, cands ...string) {
	t.Helper()
	old := localRsyncCandidates
	localRsyncCandidates = cands
	t.Cleanup(func() { localRsyncCandidates = old })
}

// openrsyncOnPath puts a stub named rsync first on PATH. It answers the
// version probe as openrsync; as a server (the fake peer's side) it runs
// real, when given; used as the local client it fails loudly.
func openrsyncOnPath(t *testing.T, real string) string {
	t.Helper()
	bin := t.TempDir()
	server := "exit 97"
	if real != "" {
		server = "exec '" + real + "' \"$@\""
	}
	writeStub(t, filepath.Join(bin, "rsync"), "#!/bin/sh\n"+
		"case \"$*\" in\n"+
		"  *--version*) printf '%s\\n' '"+openrsyncBanner+"' ;;\n"+
		"  *--server*) "+server+" ;;\n"+
		"  *) echo 'openrsync stub used as the local client' >&2; exit 97 ;;\n"+
		"esac\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return filepath.Join(bin, "rsync")
}

func TestLocalRsyncPath_SkipsOpenrsyncFirstOnPath(t *testing.T) {
	openrsyncOnPath(t, "")
	good := filepath.Join(t.TempDir(), "rsync")
	writeStub(t, good, "#!/bin/sh\necho 'rsync  version 3.4.4  protocol version 32'\n")
	withLocalRsyncCandidates(t, "rsync", good)

	path, ver, err := LocalRsyncPath(context.Background(), peerScheduleRunner(false))
	if err != nil {
		t.Fatalf("LocalRsyncPath: %v", err)
	}
	if path != good || !strings.Contains(ver, "3.4.4") {
		t.Fatalf("got %q (%q), want the 3.x candidate %q", path, ver, good)
	}
}

func TestLocalRsyncPath_OnlyOpenrsyncNamesPathAndVersion(t *testing.T) {
	stub := openrsyncOnPath(t, "")
	withLocalRsyncCandidates(t, "rsync", filepath.Join(t.TempDir(), "missing"))

	_, _, err := LocalRsyncPath(context.Background(), peerScheduleRunner(false))
	if err == nil {
		t.Fatal("openrsync alone was accepted")
	}
	for _, want := range []string{stub, "openrsync: protocol version 29", "brew install rsync"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

// With only openrsync locally, diff and sync stop before any transfer: the
// stub fails loudly if it is ever used as a client, so reaching the error
// through the resolver (not the stub's exit 97) is the proof.
func TestPeerDiffAndSync_RefuseOpenrsyncOnlyBeforeTransfer(t *testing.T) {
	requirePeerRsync(t)
	cfg, _ := peerDryRunSandbox(t)
	stub := openrsyncOnPath(t, "")
	withLocalRsyncCandidates(t, "rsync")

	_, diffErr := PeerDiff(context.Background(), PeerDiffOptions{Config: cfg, Probe: peerScheduleRunner(false)})
	_, syncErr := PeerSync(context.Background(), PeerSyncOptions{
		Config: cfg, Runner: peerScheduleRunner(false), Probe: peerScheduleRunner(false),
	})
	for name, err := range map[string]error{"diff": diffErr, "sync": syncErr} {
		if err == nil || !strings.Contains(err.Error(), stub) || !strings.Contains(err.Error(), "openrsync") {
			t.Errorf("%s: want a local-rsync refusal naming %s, got %v", name, stub, err)
		}
		if err != nil && strings.Contains(err.Error(), "exit status 97") {
			t.Errorf("%s: the openrsync stub ran as a client: %v", name, err)
		}
	}
}

// The #175 scenario end to end with real rsync: openrsync first on PATH, the
// C locale a bare ssh command runs under, the NFD marker set, and a Korean
// NFD name only the peer has. The run must resolve the 3.x client and list
// the name byte-exact.
func TestPeerDiff_NonASCIINameThroughResolvedLocalRsync(t *testing.T) {
	real := requirePeerRsync(t)
	cfg, _ := peerDryRunSandbox(t)
	openrsyncOnPath(t, real)
	withLocalRsyncCandidates(t, "rsync", real)
	t.Setenv("LC_ALL", "C")
	t.Setenv("LANG", "C")

	if err := MarkNFDMigration(cfg.LocalPaths.WorkspaceRoot); err != nil {
		t.Fatal(err)
	}
	name := norm.NFD.String("docs/한글-보고서.md")
	abs := filepath.Join(cfg.Target.Path, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("nfd\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := PeerDiff(context.Background(), PeerDiffOptions{Config: cfg, Probe: peerScheduleRunner(false)})
	if err != nil {
		t.Fatalf("PeerDiff: %v", err)
	}
	if cfg.RsyncPath != real {
		t.Errorf("local client = %q, want the resolved 3.x %q", cfg.RsyncPath, real)
	}
	if !slices.Contains(res.Plan.Pull, name) {
		t.Fatalf("plan missed the NFD name %q: %+v", name, res.Plan.Pull)
	}
}

func TestParsePeerRemoteInventory_EscapedNameIsNotAnNFDError(t *testing.T) {
	out := "@@4\t2026/09/28-01:02:03\tdocs/\\#341\\#204\\#222-x.md\n"
	_, err := parsePeerRemoteInventory(out, nil, nil, true)
	if err == nil {
		t.Fatal("escaped name accepted")
	}
	if !strings.Contains(err.Error(), "octal escapes") || strings.Contains(err.Error(), "NFD-normalized") {
		t.Fatalf("want an rsync escaping error, got %v", err)
	}
}
