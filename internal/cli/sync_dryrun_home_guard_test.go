package cli

// GUARD-03, sync and peer half: every `--dry-run`-accepting sync subcommand must
// leave the target tree byte-identical, exactly as `dot apply --dry-run` already
// must in dryrun_property_test.go.
//
// The apply half has never covered this tree. Both of its arms
// (TestApplyDryRunLeavesEmptyHomeByteIdentical here and
// tests/scenarios/dry-run-empty-home.sh in the container) drive `apply` and
// nothing else, so every write the sync and peer engines make during a preview
// has been outside the guard since the guard was written. BUG-13's own
// requirement text predicted this ("this class is currently unguarded in the
// sync and peer trees"); Phase 5 closed the store creation it named and left the
// guard gap open, which is how `sync fetch --dry-run` kept re-creating the store.
//
// There is deliberately no known-deviation table here. The apply half keeps one
// because it inherited five real deviations that had to be retired one at a
// time. This half starts empty and stays empty: a preview that writes is a
// defect, and the fix is the engine, not an entry.

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/syncer"
)

// TestSyncDryRunLeavesTreeByteIdentical drives every sync subcommand that
// accepts --dry-run against a seeded workspace and asserts the whole HOME comes
// back unchanged.
//
// The fixture seeds a workspace and mirror because the preconditions run before
// the engine: against a bare HOME every one of these commands returns at "Local
// path missing" without reaching the lock, so a bare-HOME version of this test
// would pass while the defect it guards was fully present.
//
// The peer rows run against a stub `ssh` that serves canned version/status
// probes and executes the remote command locally (setupPeerPreview below), so
// they reach the same engine path a real preview takes without sshd or a
// network peer.
func TestSyncDryRunLeavesTreeByteIdentical(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		peer bool
	}{
		{"push", []string{"sync", "push", "--dry-run"}, false},
		{"pull", []string{"sync", "pull", "--dry-run"}, false},
		{"intake", []string{"sync", "intake", "--dry-run"}, false},
		{"fetch", []string{"sync", "fetch", "seed.txt", "--dry-run"}, false},
		{"conflicts-prune", []string{"sync", "conflicts", "prune", "--dry-run"}, false},
		// The control: sync names already takes its lock below the dry-run early
		// return (sync_names_cmd.go:76), so it must stay clean through the fix.
		{"names", []string{"sync", "names", "--dry-run"}, false},
		// peer diff is always a preview; peer sync previews via --dry-run.
		{"peer-diff", []string{"peer", "diff", "--dry-run"}, true},
		{"peer-sync", []string{"peer", "sync", "--dry-run"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSyncCLIFixture(t)
			writeCLITestFile(t, filepath.Join(f.local, "seed.txt"), "payload\n")
			writeCLITestFile(t, filepath.Join(f.mirror, "seed.txt"), "payload\n")
			if tc.peer {
				f.setupPeerPreview(t)
			}

			args := append(append([]string{}, tc.args...), "--home", f.home)

			before := snapshotTree(t, f.home)
			if _, _, err := runDotForTest(args...); err != nil {
				t.Fatalf("%v: %v", args, err)
			}
			after := snapshotTree(t, f.home)

			for _, line := range addedSnapshotLines(before, after) {
				t.Errorf("`dot %s --dry-run` wrote %s into the target home\n  full snapshot line: %q\n  A preview must leave the tree byte-identical. Fix the engine; do not record a deviation for it.",
					tc.name, snapshotLinePath(line), line)
			}

			// A second preview must add nothing the first one did not: a preview
			// that keeps growing its footprint on repeat is still writing.
			if _, _, err := runDotForTest(args...); err != nil {
				t.Fatalf("second run %v: %v", args, err)
			}
			if twice := snapshotTree(t, f.home); twice != after {
				t.Errorf("a second `dot %s --dry-run` changed the tree the first one left behind\n  after first:\n%s\n  after second:\n%s", tc.name, after, twice)
			}
		})
	}
}

// setupPeerPreview turns the fixture into a configured coordinator whose SSH
// "peer" is a local tree outside HOME, so the peer dry-run rows reach the same
// engine path a real preview takes. Everything it writes is fixture setup: the
// snapshot starts after it returns. The store layout is materialized up front
// because that is the state a configured workspace is already in; the guard is
// that the preview adds nothing to it.
func (f *syncCLIFixture) setupPeerPreview(t *testing.T) {
	t.Helper()
	owner := syncer.PreferredMachineName()
	if owner == "" {
		t.Skip("this host reports no machine name, so the peer owner guard cannot be satisfied")
	}
	peer := t.TempDir()
	writeCLITestFile(t, filepath.Join(peer, "peer-only.txt"), "peer\n")

	peerPaths := syncer.ResolveLocalPathsForProfile(f.local, syncer.PeerProfile)
	if err := syncer.EnsureLocalLayout(peerPaths); err != nil {
		t.Fatal(err)
	}
	if err := syncer.SaveLocalConfig(peerPaths, &syncer.LocalConfig{
		Target:      "ssh:fake-peer:" + peer,
		Owner:       owner,
		FilterMode:  syncer.FilterModeExclude,
		Propagation: syncer.DefaultPropagationPolicy(),
	}); err != nil {
		t.Fatal(err)
	}

	// The stub answers the two canned probes a peer run makes - the rsync
	// version, which must carry "version 3." or the openrsync guard
	// (internal/syncer/rsyncbin.go) refuses the peer, and `dot peer status
	// --json` pointing back at this workspace - and executes every other
	// remote command locally. Everything after the host token is rejoined
	// with spaces the way sshd delivers it.
	status := fmt.Sprintf(
		`{"schemaVersion":%d,"kind":"peer","profile":{"configured":true,"owner":%q,"workspacePath":%q,"target":{"path":%q}}}`,
		syncer.PeerStatusSchemaVersion, owner, peer, f.local)
	script := "#!/bin/sh\n" +
		"while [ $# -gt 0 ]; do\n" +
		"  case \"$1\" in\n" +
		"    -o) shift 2 ;;\n" +
		"    fake-peer) shift; break ;;\n" +
		"    *) shift ;;\n" +
		"  esac\n" +
		"done\n" +
		"case \"$*\" in\n" +
		"  *\"list dot candidates\"*) printf '/fake/dot\\tdot version 9.9.9 (fake)\\n' ;;\n" +
		"  *--version*) echo 'rsync  version 3.4.1  protocol version 32' ;;\n" +
		"  *\"peer status --json\"*) printf '%s\\n' '" + status + "' ;;\n" +
		"  *) exec /bin/sh -c \"$*\" ;;\n" +
		"esac\n"
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}
