package syncer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// installFakePeerSSH installs a stub ssh that serves the two canned probes a
// peer run makes - the rsync version and `dot peer status --json` - and
// executes every other remote command locally, so a peer run against a local
// "peer" tree exercises the real rsync wire without sshd. The version answer
// must carry "version 3." or the openrsync guard (rsyncbin.go) refuses the
// peer. PATH keeps its real entries because the run shells out to rsync and
// git, not just ssh.
func installFakePeerSSH(t *testing.T, statusJSON string) {
	t.Helper()
	requirePeerRsync(t)
	if strings.Contains(statusJSON, "'") {
		t.Fatal("canned status JSON must not contain a single quote")
	}
	// Everything after the host token is the remote command; real sshd
	// receives those args joined with spaces, so the stub rejoins them the
	// same way before handing them to a local shell.
	script := "#!/bin/sh\n" +
		"while [ $# -gt 0 ]; do\n" +
		"  case \"$1\" in\n" +
		"    -o) shift 2 ;;\n" +
		"    fake-peer) shift; break ;;\n" +
		"    *) shift ;;\n" +
		"  esac\n" +
		"done\n" +
		"case \"$*\" in\n" +
		"  *\"list dot candidates\"*) exec /bin/sh -c \"$*\" ;;\n" +
		"  *--version*) echo 'rsync  version 3.4.1  protocol version 32' ;;\n" +
		"  *\"peer status --json\"*) printf '%s\\n' '" + statusJSON + "' ;;\n" +
		"  *) exec /bin/sh -c \"$*\" ;;\n" +
		"esac\n"
	bin := t.TempDir()
	writeStub(t, filepath.Join(bin, "ssh"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	dot := filepath.Join(bin, "dot")
	writeStub(t, dot, "#!/bin/sh\n[ \"$1\" = --version ] && echo 'dot version 9.9.9 (fake)'\nexit 0\n")
	useRemoteDotCandidates(t, dot)
}

// useRemoteDotCandidates points the peer's dot probe at fixture binaries only,
// so a test never reaches a dot installed on the host running it.
func useRemoteDotCandidates(t *testing.T, paths ...string) {
	t.Helper()
	quoted := make([]string, len(paths))
	for i, path := range paths {
		quoted[i] = shellQuote(path)
	}
	old := remoteDotCandidates
	remoteDotCandidates = strings.Join(quoted, " ")
	t.Cleanup(func() { remoteDotCandidates = old })
}

// peerDryRunSandbox builds a coordinator config aimed at a local directory
// standing in for the SSH peer, with the peer store deliberately absent: the
// guard asserts a preview never creates it.
func peerDryRunSandbox(t *testing.T) (*Config, *LocalPaths) {
	t.Helper()
	names := MachineNames()
	if len(names) == 0 {
		t.Skip("this host reports no machine name, so CheckOwner cannot be satisfied")
	}
	owner := names[0]

	base := t.TempDir()
	local := filepath.Join(base, "workspace")
	peer := filepath.Join(base, "peer")
	mirror := filepath.Join(base, "mirror")
	for _, dir := range []string{local, peer, mirror} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// One payload only the peer has. The plan must report it, which is what
	// makes the no-store assertion non-vacuous: the remote inventory really
	// ran through the materialized filter files.
	if err := os.WriteFile(filepath.Join(peer, "peer-only.txt"), []byte("peer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "local-only.txt"), []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	paths := ResolveLocalPathsForProfile(local, PeerProfile)
	cfg := &Config{
		Profile:     PeerProfile,
		Owner:       owner,
		LocalPath:   local + "/",
		MirrorPath:  mirror + "/",
		Target:      Target{Kind: TargetSSH, Host: "fake-peer", Path: peer},
		ConfigDir:   paths.StoreDir,
		LockDir:     filepath.Join(base, "peer.lock"),
		LocalPaths:  paths,
		FilterMode:  FilterModeExclude,
		MaxDelete:   1000,
		Propagation: DefaultPropagationPolicy(),
	}
	status := fmt.Sprintf(
		`{"schemaVersion":%d,"kind":"peer","dotVersion":"9.9.9 (fake)","profile":{"configured":true,"owner":%q,"workspacePath":%q,"target":{"path":%q}}}`,
		PeerStatusSchemaVersion, owner, peer, local)
	installFakePeerSSH(t, status)
	return cfg, paths
}

func TestPeerDiff_DryRunLeavesStoreUnmaterialized(t *testing.T) {
	cfg, paths := peerDryRunSandbox(t)
	res, err := PeerDiff(context.Background(), PeerDiffOptions{
		Config: cfg,
		Probe:  peerScheduleRunner(false),
	})
	if err != nil {
		t.Fatalf("PeerDiff: %v", err)
	}
	if res.Unreachable {
		t.Fatal("fake peer reported unreachable, so the plan never ran")
	}
	if !slices.Contains(res.Plan.Pull, "peer-only.txt") {
		t.Fatalf("plan missed the peer-only payload, so the inventory did not really run: %+v", res.Plan)
	}
	if _, statErr := os.Stat(paths.StoreDir); !os.IsNotExist(statErr) {
		t.Errorf("peer diff (always a preview) materialized the peer store at %s (stat err: %v)", paths.StoreDir, statErr)
	}
}

func TestPeerSync_DryRunLeavesStoreUnmaterialized(t *testing.T) {
	cfg, paths := peerDryRunSandbox(t)
	res, err := PeerSync(context.Background(), PeerSyncOptions{
		Config:   cfg,
		Runner:   peerScheduleRunner(true),
		Probe:    peerScheduleRunner(false),
		SkipHome: true,
		DryRun:   true,
	})
	if err != nil {
		t.Fatalf("PeerSync --dry-run: %v", err)
	}
	if res.Unreachable {
		t.Fatal("fake peer reported unreachable, so the run never planned")
	}
	if !res.Complete {
		t.Error("an additive preview was reported incomplete")
	}
	if _, statErr := os.Stat(paths.StoreDir); !os.IsNotExist(statErr) {
		t.Errorf("peer sync --dry-run materialized the peer store at %s (stat err: %v)", paths.StoreDir, statErr)
	}
	localRoot := strings.TrimRight(cfg.LocalPath, "/")
	if _, statErr := os.Stat(filepath.Join(localRoot, "peer-only.txt")); !os.IsNotExist(statErr) {
		t.Errorf("peer sync --dry-run pulled peer-only.txt into the workspace (stat err: %v)", statErr)
	}
}
