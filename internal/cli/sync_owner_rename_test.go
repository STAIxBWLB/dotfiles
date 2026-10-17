package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/syncer"
)

func TestSyncOwnerRenameGuards(t *testing.T) {
	f := newSyncCLIFixture(t)
	paths := syncer.ResolveLocalPaths(f.local)
	if err := syncer.SaveLocalConfig(paths, &syncer.LocalConfig{Target: "local:" + f.mirror, Owner: "other-mac", Propagation: syncer.DefaultPropagationPolicy()}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"sync", "owner", "--local-only"}, "--local-only only applies to --rename"},
		{[]string{"sync", "owner", "--rename", "--set", "x", "a", "b"}, "none of the others can be"},
		{[]string{"sync", "owner", "--rename", "other-mac", "another-mac"}, "run --rename on the machine being renamed"},
	} {
		if _, _, err := runDotForTest(tc.args...); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, want %q", tc.args, err, tc.want)
		}
	}
	if got, _, _ := syncer.LoadLocalConfig(paths); got.Owner != "other-mac" {
		t.Fatalf("a refused rename wrote %+v", got)
	}

	// --local-only on the peer side (the plumbing) renames, --dry-run first.
	if _, _, err := runDotForTest("--dry-run", "sync", "owner", "--rename", "--local-only", "other-mac", "renamed-mac"); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := syncer.LoadLocalConfig(paths); got.Owner != "other-mac" {
		t.Fatalf("--dry-run wrote %+v", got)
	}
	if _, _, err := runDotForTest("sync", "owner", "--rename", "--local-only", "other-mac", "renamed-mac"); err != nil {
		t.Fatal(err)
	}
	// This Mac answers to neither name: it is the other Mac's step, which
	// records no alias.
	if got, _, _ := syncer.LoadLocalConfig(paths); got.Owner != "renamed-mac" || len(got.OwnerAliases) != 0 {
		t.Fatalf("after --local-only rename: %+v", got)
	}
}

// Without the peer's answer, only a Mac that still answers to the old name
// may rename: after the host rename it could be the other Mac claiming the
// owner (round-3 review).
func TestSyncOwnerRenameFailsClosedWithoutThePeer(t *testing.T) {
	f := newSyncCLIFixture(t)
	bin := t.TempDir()
	writeCLITestFile(t, filepath.Join(bin, "ssh"), "#!/bin/sh\nexit 255\n")
	if err := os.Chmod(filepath.Join(bin, "ssh"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	self := syncer.PreferredMachineName()
	if self == "" {
		t.Skip("no machine name")
	}
	for _, profile := range []string{"sync", "peer"} {
		writeCLITestFile(t, filepath.Join(f.local, ".dotfiles", profile, "config.yaml"),
			"target: ssh:asleep-peer:/remote/work\nowner: coordinator-mac\npropagation:\n  create: true\n  update: true\n  delete: true\n")
	}
	_, _, err := runDotForTest("sync", "owner", "--rename", "coordinator-mac", self)
	if err == nil || !strings.Contains(err.Error(), "could not confirm") {
		t.Fatalf("err = %v, want a fail-closed refusal", err)
	}
	local, _, _ := syncer.LoadLocalConfig(syncer.ResolveLocalPathsForProfile(f.local, "peer"))
	if local.Owner != "coordinator-mac" {
		t.Fatalf("a refused rename wrote %+v", local)
	}
	// This Mac owns the profiles, but the new name is the peer's target.
	writeCLITestFile(t, filepath.Join(f.local, ".dotfiles", "peer", "config.yaml"),
		"target: ssh:user.name@asleep-peer.example.net:/remote/work\nowner: "+self+"\npropagation:\n  create: true\n  update: true\n  delete: true\n")
	if _, _, err := runDotForTest("sync", "owner", "--rename", self, "asleep-peer"); err == nil || !strings.Contains(err.Error(), "peer target") {
		t.Fatalf("renaming to the peer target's name: %v", err)
	}
	// A Mac that answers to <old> renames without the peer, exits 0, and
	// says <new> was not checked against the other Mac.
	out, errOut, err := runDotForTest("sync", "owner", "--rename", self, "renamed-mac")
	if err != nil || !strings.Contains(out+errOut, "the peer was not checked") || !strings.Contains(out+errOut, "On the other Mac, when reachable") {
		t.Fatalf("offline rename: %v\n%s%s", err, out, errOut)
	}
}

// Round 4: after both host renames, neither Mac answers to <old>. The Mac
// without dot's scheduler (the inactive one) must not rename itself into the
// owner; the Mac with it may.
func TestSyncOwnerRenameNeedsTheOwnersScheduler(t *testing.T) {
	f := newSyncCLIFixture(t)
	self := syncer.PreferredMachineName()
	if self == "" {
		t.Skip("no machine name")
	}
	// The stub serves the status document from a file, so each case can set
	// the peer's scheduler state.
	statusFile := filepath.Join(t.TempDir(), "status.json")
	peerSays := func(state string) {
		writeCLITestFile(t, statusFile, `{"schemaVersion":1,"kind":"peer","profile":{"configured":true,"owner":"gone-mac","machineNames":["other-mac"],"workspacePath":"/remote/work","target":{"path":"`+f.local+`"}},"job":{"state":"`+state+`"}}`)
	}
	bin := t.TempDir()
	writeCLITestFile(t, filepath.Join(bin, "ssh"), "#!/bin/sh\ncase \"$*\" in\n"+
		"  *\"list dot candidates\"*) printf '/fake/dot\\tdot version 9.9.9 (fake)\\n' ;;\n"+
		"  *\"peer status --json\"*) cat '"+statusFile+"' ;;\n"+
		"  *) exit 0 ;;\nesac\n")
	if err := os.Chmod(filepath.Join(bin, "ssh"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, profile := range []string{"sync", "peer"} {
		writeCLITestFile(t, filepath.Join(f.local, ".dotfiles", profile, "config.yaml"),
			"target: ssh:user@peer-alias:/remote/work\nowner: gone-mac\npropagation:\n  create: true\n  update: true\n  delete: true\n")
	}
	agents := filepath.Join(f.home, "Library", "LaunchAgents")
	refused := func(why, want string) {
		t.Helper()
		if _, _, err := runDotForTest("sync", "owner", "--rename", "gone-mac", self); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: err = %v, want %q", why, err, want)
		}
	}

	peerSays("not installed")
	if runtime.GOOS != "darwin" {
		// The scheduler proof is launchd's: elsewhere only --local-only renames.
		refused("host without launchd", "needs launchd")
		return
	}
	refused("no scheduler here", "does not run the peer scheduler")
	// `dot sync setup` installs the mirror unit on either Mac: it proves
	// nothing.
	writeCLITestFile(t, filepath.Join(agents, "com.dotfiles.sync.plist"), "<plist/>")
	refused("mirror unit only", "does not run the peer scheduler")
	// A stale plist (by hand, an older dot, or the old coordinator's until
	// it next runs): both Macs hold one.
	writeCLITestFile(t, filepath.Join(agents, "com.dotfiles.peer.plist"), "<plist/>")
	peerSays("running")
	refused("both claim the scheduler", "both Macs claim")
	peerSays("")
	refused("peer scheduler unknown", "could not report its peer scheduler")

	peerSays("not installed")
	if _, _, err := runDotForTest("sync", "owner", "--rename", "gone-mac", self); err != nil {
		t.Fatalf("the coordinator could not rename: %v", err)
	}
	local, _, _ := syncer.LoadLocalConfig(syncer.ResolveLocalPathsForProfile(f.local, "peer"))
	if local.Owner != self {
		t.Fatalf("owner = %q, want %q", local.Owner, self)
	}
}

// Without a peer to confirm, a Mac that no longer answers to <old> cannot
// show it is the owner: a mirror-only workspace, or a peer store that does
// not load, waits for --local-only.
func TestSyncOwnerRenameWithoutAPeerAnswerFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name, peerConfig, want string
	}{
		{"mirror only", "", "no peer to confirm"},
		{"peer store does not load", "target: ssh:peerhost\nowner: gone-mac\n", "could not confirm"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSyncCLIFixture(t)
			self := syncer.PreferredMachineName()
			if self == "" {
				t.Skip("no machine name")
			}
			writeCLITestFile(t, filepath.Join(f.local, ".dotfiles", "sync", "config.yaml"),
				"owner: gone-mac\npropagation:\n  create: true\n  update: true\n  delete: true\n")
			if tc.peerConfig != "" {
				writeCLITestFile(t, filepath.Join(f.local, ".dotfiles", "peer", "config.yaml"), tc.peerConfig)
			}
			writeCLITestFile(t, filepath.Join(f.home, "Library", "LaunchAgents", "com.dotfiles.sync.plist"), "<plist/>")
			writeCLITestFile(t, filepath.Join(f.home, "Library", "LaunchAgents", "com.dotfiles.peer.plist"), "<plist/>")
			if _, _, err := runDotForTest("sync", "owner", "--rename", "gone-mac", self); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			local, _, _ := syncer.LoadLocalConfig(syncer.ResolveLocalPathsForProfile(f.local, "sync"))
			if local.Owner != "gone-mac" {
				t.Fatalf("a refused rename wrote the store: owner %q", local.Owner)
			}
		})
	}
}

// --set that takes the coordinator role from this Mac removes its peer
// scheduler, as a demotion does: a stale plist would otherwise read as the
// owner's in a later --rename.
func TestSyncOwnerSetAwayRemovesThePeerScheduler(t *testing.T) {
	f := newSyncCLIFixture(t)
	self := syncer.PreferredMachineName()
	if self == "" {
		t.Skip("no machine name")
	}
	bin := t.TempDir()
	writeCLITestFile(t, filepath.Join(bin, "launchctl"), "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(filepath.Join(bin, "launchctl"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	writeCLITestFile(t, filepath.Join(f.local, ".dotfiles", "peer", "config.yaml"),
		"target: ssh:peer-alias:/remote/work\nowner: "+self+"\nowner_epoch: 2\npropagation:\n  create: true\n  update: true\n  delete: true\nhooks:\n  on_deactivate:\n    - launchd-bootout com.none.job.*\n")
	plist := filepath.Join(f.home, "Library", "LaunchAgents", "com.dotfiles.peer.plist")
	writeCLITestFile(t, plist, "<plist/>")

	// Setting itself again keeps the scheduler.
	if _, errOut, err := runDotForTest("sync", "owner", "--profile=peer", "--set", self); err != nil {
		t.Fatalf("--set self: %v\n%s", err, errOut)
	}
	if _, err := os.Stat(plist); err != nil {
		t.Fatalf("the coordinator's own --set removed its scheduler: %v", err)
	}
	out, errOut, err := runDotForTest("sync", "owner", "--profile=peer", "--set", "other-mac")
	if err != nil {
		t.Fatalf("--set other: %v\n%s", err, errOut)
	}
	if _, err := os.Stat(plist); !os.IsNotExist(err) {
		t.Fatalf("the plist stayed after the role moved away: %v", err)
	}
	// The demotion's on_deactivate outcome is reported, as setup --off does.
	if !strings.Contains(out+errOut, "hook on_deactivate launchd-bootout com.none.job.*") {
		t.Fatalf("no hook line:\n%s%s", out, errOut)
	}
}

// The peer step on a Mac that owns nothing by <old> is a no-op, not a failure.
func TestSyncOwnerRenameLocalOnlyWithNothingOwned(t *testing.T) {
	f := newSyncCLIFixture(t)
	writeCLITestFile(t, filepath.Join(f.local, ".dotfiles", "sync", "config.yaml"),
		"owner: someone-else\npropagation:\n  create: true\n  update: true\n  delete: true\n")
	out, errOut, err := runDotForTest("sync", "owner", "--rename", "--local-only", "gone-mac", "new-mac")
	if err != nil || !strings.Contains(out+errOut, "nothing to rename") {
		t.Fatalf("err = %v\n%s%s", err, out, errOut)
	}
}

// A dry-run --set writes nothing: not the owner, not the epoch, and the
// scheduler stays.
func TestSyncOwnerSetDryRunWritesNothing(t *testing.T) {
	f := newSyncCLIFixture(t)
	self := syncer.PreferredMachineName()
	if self == "" {
		t.Skip("no machine name")
	}
	store := filepath.Join(f.local, ".dotfiles", "peer", "config.yaml")
	writeCLITestFile(t, store, "target: ssh:peer-alias:/remote/work\nowner: "+self+"\nowner_epoch: 2\npropagation:\n  create: true\n  update: true\n  delete: true\n")
	plist := filepath.Join(f.home, "Library", "LaunchAgents", "com.dotfiles.peer.plist")
	writeCLITestFile(t, plist, "<plist/>")
	before, _ := os.ReadFile(store)
	if _, errOut, err := runDotForTest("--dry-run", "sync", "owner", "--profile=peer", "--set", "other-mac"); err != nil {
		t.Fatalf("%v\n%s", err, errOut)
	}
	if after, _ := os.ReadFile(store); string(after) != string(before) {
		t.Fatalf("a dry run rewrote the store:\n%s", after)
	}
	if _, err := os.Stat(plist); err != nil {
		t.Fatalf("a dry run removed the scheduler: %v", err)
	}
}

// The other Mac's migration step records no alias: only the Mac being
// renamed answers to one of the names.
func TestSyncOwnerRenameLocalOnlyOnTheOtherMacKeepsNoAlias(t *testing.T) {
	f := newSyncCLIFixture(t)
	writeCLITestFile(t, filepath.Join(f.local, ".dotfiles", "sync", "config.yaml"),
		"owner: gone-mac\npropagation:\n  create: true\n  update: true\n  delete: true\n")
	if _, errOut, err := runDotForTest("sync", "owner", "--rename", "--local-only", "gone-mac", "new-mac"); err != nil {
		t.Fatalf("%v\n%s", err, errOut)
	}
	local, _, _ := syncer.LoadLocalConfig(syncer.ResolveLocalPathsForProfile(f.local, "sync"))
	if local.Owner != "new-mac" || len(local.OwnerAliases) != 0 {
		t.Fatalf("owner %q aliases %v, want new-mac and none", local.Owner, local.OwnerAliases)
	}
}

// A peer without launchd reports its scheduler as unsupported, which is
// none: it cannot hold the owner's scheduler.
func TestSyncOwnerRenameTreatsAnUnsupportedPeerSchedulerAsNone(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the scheduler proof needs launchd here")
	}
	self := syncer.PreferredMachineName()
	if self == "" {
		t.Skip("no machine name")
	}
	for _, tc := range []struct {
		name, state, old, new, wantErr, wantOut string
	}{
		{"unsupported is none", "unsupported: peer scheduler requires macOS launchd", "gone-mac", self, "", ""},
		// Unknown is no answer: it fails closed, and says so.
		{"unknown refuses", "unknown: permission denied", "gone-mac", self, "could not report its peer scheduler", ""},
		// Before this Mac's host rename, <new> is checked only against the
		// other Mac's current names.
		{"new not yet this Mac's", "not installed", self, "renamed-mac", "", "checked only against the other Mac's current names"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSyncCLIFixture(t)
			status := `{"schemaVersion":1,"kind":"peer","profile":{"configured":true,"owner":"` + tc.old + `","machineNames":["other-box"],"workspacePath":"/remote/work","target":{"path":"` + f.local + `"}},"job":{"state":"` + tc.state + `"}}`
			bin := t.TempDir()
			writeCLITestFile(t, filepath.Join(bin, "ssh"), "#!/bin/sh\ncase \"$*\" in\n"+
				"  *\"list dot candidates\"*) printf '/fake/dot\\tdot version 9.9.9 (fake)\\n' ;;\n"+
				"  *\"peer status --json\"*) printf '%s\\n' '"+status+"' ;;\n"+
				"  *) exit 0 ;;\nesac\n")
			if err := os.Chmod(filepath.Join(bin, "ssh"), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			for _, profile := range []string{"sync", "peer"} {
				writeCLITestFile(t, filepath.Join(f.local, ".dotfiles", profile, "config.yaml"),
					"target: ssh:peer-alias:/remote/work\nowner: "+tc.old+"\npropagation:\n  create: true\n  update: true\n  delete: true\n")
			}
			writeCLITestFile(t, filepath.Join(f.home, "Library", "LaunchAgents", "com.dotfiles.peer.plist"), "<plist/>")
			out, errOut, err := runDotForTest("sync", "owner", "--rename", tc.old, tc.new)
			switch {
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			case tc.wantErr == "" && err != nil:
				t.Fatalf("%v\n%s", err, errOut)
			case !strings.Contains(out+errOut, tc.wantOut):
				t.Fatalf("output lacks %q:\n%s%s", tc.wantOut, out, errOut)
			}
		})
	}
}
