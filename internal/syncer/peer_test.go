package syncer

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/config"
)

func TestProfileStoresAreIsolated(t *testing.T) {
	ws := t.TempDir()
	mirror := ResolveLocalPathsForProfile(ws, DefaultProfile)
	peer := ResolveLocalPathsForProfile(ws, "peer")

	if mirror.StoreDir == peer.StoreDir {
		t.Fatalf("profiles share a store dir: %s", mirror.StoreDir)
	}
	if filepath.Base(mirror.StoreDir) != "sync" {
		t.Errorf("default profile store = %s, want .../sync", mirror.StoreDir)
	}
	if filepath.Base(peer.StoreDir) != "peer" {
		t.Errorf("peer store = %s, want .../peer", peer.StoreDir)
	}
	// allow.txt separation is the whole point: the peer profile opts secrets IN,
	// and that must not reach the mirror profile, which must keep them OUT.
	if mirror.AllowFile == peer.AllowFile {
		t.Error("profiles share allow.txt; a peer secrets opt-in would leak to the cloud mirror")
	}
	if mirror.BaselineFile == peer.BaselineFile {
		t.Error("profiles share baseline.manifest")
	}
}

func TestPeerStoreIsNotGitTracked(t *testing.T) {
	// The managed .gitignore block ignores /.dotfiles/* and whitelists only
	// sync/. A shared, git-tracked peer baseline would produce merge conflicts
	// in the very file that coordinates the two machines.
	var whitelisted []string
	for _, e := range gitignoreEntries {
		if strings.HasPrefix(e, "!/") {
			whitelisted = append(whitelisted, e)
		}
	}
	for _, e := range whitelisted {
		if strings.Contains(e, "/peer/") {
			t.Fatalf("peer store is whitelisted in .gitignore (%s); it must stay machine-local", e)
		}
	}
	if !containsString(gitignoreEntries, "/.dotfiles/*") {
		t.Fatal("missing /.dotfiles/* catch-all; a new profile store would not be ignored")
	}
}

func TestValidateProfile(t *testing.T) {
	for _, ok := range []string{"", " ", "s", "sync", "peer", "peer2", "research-2", "research.v2", "research_2", "  research-2.v1  "} {
		if err := ValidateProfile(ok); err != nil {
			t.Errorf("ValidateProfile(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{
		".", "..", ".research", "_research", "-research",
		"research notes", "research\tnotes", "research\nnotes", "research\x00notes",
		"a/b", "a\\b", "../etc", "research&ops", "research<ops", "research>ops",
		"%i", "research%ops", "$HOME", "research\"ops", "research'ops", "café", string([]byte{'r', 0xff}),
	} {
		err := ValidateProfile(bad)
		if err == nil {
			t.Errorf("ValidateProfile(%q) = nil, want error", bad)
			continue
		}
		for _, want := range []string{fmt.Sprintf("%q", bad), "ASCII token"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("ValidateProfile(%q) error = %q, want %q", bad, err, want)
			}
		}
	}
	if NormalizeProfile("") != DefaultProfile {
		t.Error("empty profile should normalize to the default")
	}
}

func TestProfilePathsKeepDefaultUnchanged(t *testing.T) {
	home := t.TempDir()
	base, err := resolvePathsForHome(home)
	if err != nil {
		t.Fatal(err)
	}
	def, err := resolvePathsForHomeProfile(home, DefaultProfile)
	if err != nil {
		t.Fatal(err)
	}
	if def.LockDir != base.LockDir || def.LaunchdPlist != base.LaunchdPlist {
		t.Errorf("default profile changed existing paths:\n lock %s vs %s\n plist %s vs %s",
			def.LockDir, base.LockDir, def.LaunchdPlist, base.LaunchdPlist)
	}
	peer, err := resolvePathsForHomeProfile(home, "peer")
	if err != nil {
		t.Fatal(err)
	}
	if peer.LockDir != base.LockDir {
		t.Errorf("peer must share the default lock to serialize workspace mutations: %s vs %s", peer.LockDir, base.LockDir)
	}
	if peer.LaunchdPlist == base.LaunchdPlist {
		t.Error("peer shares the launchd unit; installing one scheduler would uninstall the other")
	}
	if !strings.HasSuffix(peer.LaunchdPlist, "com.dotfiles.peer.plist") {
		t.Errorf("peer plist = %s", peer.LaunchdPlist)
	}
}

// TestResolveConfigRejectsUnsafeProfile pins profile validation at
// resolveConfig (internal/syncer/sync.go, its first statement), which after
// RES-01 is the only place a Config carrying a profile can be built. The
// assertion used to be pointed at ResolveScheduler, which inherited
// ValidateProfile through the resolver chain; it no longer validates, so an
// assertion left there would pass on any profile string.
//
// The unsafe strings drive a space, a format specifier, a shell
// metacharacter and a newline, each of which would otherwise become a unit
// filename or a launchd label.
func TestResolveConfigRejectsUnsafeProfile(t *testing.T) {
	home := t.TempDir()
	for _, profile := range []string{"research notes", "%i", "research&ops", "research\nops"} {
		t.Run(fmt.Sprintf("%q", profile), func(t *testing.T) {
			state := &config.UserState{}
			state.Modules.Gsync.LocalPath = filepath.Join(home, "workspace", "work")
			if _, err := ResolveConfigForHomeProfile(state, home, profile); err == nil {
				t.Fatalf("ResolveConfigForHomeProfile(%q) = nil, want error", profile)
			}
			entries, err := os.ReadDir(home)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("unsafe profile created target-home artifacts: %v", entries)
			}
		})
	}
}

func TestCheckOwner(t *testing.T) {
	host, err := ShortHostname()
	if err != nil {
		t.Skip("hostname unavailable")
	}
	if err := CheckOwner(&Config{Profile: "sync"}); err != nil {
		t.Errorf("empty owner must stay unrestricted, got %v", err)
	}
	if err := CheckOwner(&Config{Profile: "sync", Owner: host}); err != nil {
		t.Errorf("owner == this host should pass, got %v", err)
	}
	// Domain suffixes must not defeat the comparison.
	if err := CheckOwner(&Config{Profile: "sync", Owner: host + ".local"}); err != nil {
		t.Errorf("owner with domain suffix should pass, got %v", err)
	}
	err = CheckOwner(&Config{Profile: "sync", Owner: "some-other-machine"})
	if err == nil {
		t.Fatal("foreign owner must be refused")
	}
	if !strings.Contains(err.Error(), "refusing to write") {
		t.Errorf("error should say what it refuses: %v", err)
	}
}

func TestRemoteRsyncUsable(t *testing.T) {
	// The exact banners seen in the field.
	unusable := []string{
		"openrsync: protocol version 29\nrsync version 2.6.9 compatible",
		"rsync  version 2.6.9  protocol version 29",
	}
	for _, v := range unusable {
		if remoteRsyncUsable(v) {
			t.Errorf("remoteRsyncUsable(%q) = true, want false", firstLine(v))
		}
	}
	usable := []string{
		"rsync  version 3.4.4  protocol version 32",
		"rsync  version 3.2.7  protocol version 31",
	}
	for _, v := range usable {
		if !remoteRsyncUsable(v) {
			t.Errorf("remoteRsyncUsable(%q) = false, want true", firstLine(v))
		}
	}
}

func TestClassifyRsyncError(t *testing.T) {
	if got := classifyRsyncError(nil); got != nil {
		t.Errorf("nil in, %v out", got)
	}
	// 23 = partial transfer, 24 = vanished files. Both are routine on a live
	// tree and must not be treated as "stop everything".
	for _, code := range []int{23, 24} {
		err := exitErr(t, code)
		got := classifyRsyncError(err)
		if !IsPartialTransfer(got) {
			t.Errorf("exit %d should classify as partial, got %v", code, got)
		}
	}
	for _, code := range []int{1, 12, 30} {
		got := classifyRsyncError(exitErr(t, code))
		if IsPartialTransfer(got) {
			t.Errorf("exit %d must not be treated as partial", code)
		}
	}
}

func TestCommonArgsAddsDirWriteBit(t *testing.T) {
	cfg := &Config{FilterMode: FilterModeExclude}
	args := commonArgs(cfg, runtimeFilters{})
	if !containsString(args, "--chmod=Du+w") {
		t.Error("missing --chmod=Du+w; a read-only source dir would fail with exit 23")
	}
}

func TestTransportPassesRemoteRsyncPath(t *testing.T) {
	local := &Config{Target: Target{Kind: TargetLocal, Path: "/tmp/x/"}}
	if got := rsyncTransportArgs(local); got != nil {
		t.Errorf("local target should have no transport args, got %v", got)
	}
	ssh := &Config{Target: Target{Kind: TargetSSH, Host: "h", Path: "/p"}}
	got := rsyncTransportArgs(ssh)
	if !containsString(got, "-e") || !containsString(got, "ssh") {
		t.Errorf("ssh target needs -e ssh, got %v", got)
	}
	if containsString(got, "--rsync-path=") {
		t.Error("no --rsync-path expected when the remote default is usable")
	}
	ssh.RemoteRsyncPath = "/opt/homebrew/bin/rsync"
	got = rsyncTransportArgs(ssh)
	if !containsString(got, "--rsync-path=/opt/homebrew/bin/rsync") {
		t.Errorf("remote rsync override not passed: %v", got)
	}
}

// TestTrackedIncludesRecurseSubmodules is the regression test for the measured
// failure: tracked files inside a submodule were absent from the include layer,
// so an exclude pattern dropped them and the receiver saw deletions.
func TestTrackedIncludesRecurseSubmodules(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	sub := filepath.Join(root, "sites", "inner")
	mustMkdir(t, filepath.Join(sub, "__pycache__"))
	gitInit(t, sub)
	writeFile(t, filepath.Join(sub, "__pycache__", "a.pyc"), "x")
	gitAddCommit(t, sub, "seed")

	gitInit(t, root)
	writeFile(t, filepath.Join(root, "top.txt"), "y")
	gitAddCommit(t, root, "root")

	// The outer repo sees the submodule dir but not its contents.
	outer := gitTrackedForSync(root)
	if outer["sites/inner/__pycache__/a.pyc"] {
		t.Fatal("precondition failed: outer ls-files already lists submodule contents")
	}

	got := gitTrackedInSubmodules(root, []string{"sites/inner"})
	if !got["sites/inner/__pycache__/a.pyc"] {
		t.Errorf("submodule-tracked file missing from include layer; got %v", keys(got))
	}
}

// helpers

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want || (strings.HasSuffix(want, "=") && strings.HasPrefix(s, want)) {
			return true
		}
	}
	return false
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func exitErr(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("sh", "-c", "exit "+itoa(code)).Run()
	if err == nil {
		t.Fatalf("expected a non-zero exit for code %d", code)
	}
	return err
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, p, body string) {
	t.Helper()
	mustMkdir(t, filepath.Dir(p))
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func gitAddCommit(t *testing.T, dir, msg string) {
	t.Helper()
	// -c core.excludesFile=/dev/null: the invoking user's global gitignore
	// (core.excludesFile, e.g. ~/.config/git/gitignore.global) must not
	// swallow seeded fixtures like __pycache__/a.pyc.
	for _, args := range [][]string{{"-c", "core.excludesFile=/dev/null", "add", "-A"}, {"commit", "-q", "-m", msg}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func TestPreferredMachineNameIsDNSSafeAndSpecific(t *testing.T) {
	got := PreferredMachineName()
	if got == "" {
		t.Skip("no machine name available")
	}
	if !dnsSafeName(got) {
		t.Errorf("PreferredMachineName() = %q, which is not DNS-safe; owner values get typed and diffed", got)
	}
	if got == "mac" && len(MachineNames()) > 1 {
		t.Errorf("picked the generic default %q despite better candidates %v", got, MachineNames())
	}
	// Whatever it picks must actually satisfy the guard on this machine.
	if err := CheckOwner(&Config{Profile: "sync", Owner: got}); err != nil {
		t.Errorf("PreferredMachineName() = %q but CheckOwner rejects it: %v", got, err)
	}
}

// TestFreshProfileHasEveryFilterFileRsyncReferences is the regression test for a
// real failure: commonArgs passes --exclude-from for exclude.txt and ignore.txt
// unconditionally, and rsync exits 11 when it cannot open one. A freshly created
// profile whose store lacked them failed every transfer.
func TestFreshProfileHasEveryFilterFileRsyncReferences(t *testing.T) {
	ws := t.TempDir()
	paths := ResolveLocalPathsForProfile(ws, "peer")
	if err := EnsureLocalLayout(paths); err != nil {
		t.Fatalf("EnsureLocalLayout: %v", err)
	}
	for _, f := range []struct{ name, path string }{
		{"exclude.txt", paths.ExcludeFile},
		{"ignore.txt", paths.IgnoreFile},
		{"include.txt", paths.IncludeFile},
		{"allow.txt", paths.AllowFile},
	} {
		if _, err := os.Stat(f.path); err != nil {
			t.Errorf("%s missing after layout creation: %v (rsync would exit 11)", f.name, err)
		}
	}
}

// TestTrackedIncludesRecurseDeeply guards the depth the doc comment promises.
// A fixed two-level walk silently drops the third level and reintroduces the
// tracked-but-excluded deletion bug in trees like dev/ that nest submodules.
func TestTrackedIncludesRecurseDeeply(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	deep := filepath.Join(root, "dev", "outer", "inner")
	mustMkdir(t, deep)
	gitInit(t, deep)
	writeFile(t, filepath.Join(deep, "__pycache__", "deep.pyc"), "x")
	gitAddCommit(t, deep, "deep")

	got := gitTrackedInSubmodules(root, []string{"dev"})
	// dev is not itself a repo here, so walk from the level that is.
	got2 := gitTrackedInSubmodules(filepath.Join(root, "dev"), []string{"outer/inner"})
	if !got2["outer/inner/__pycache__/deep.pyc"] {
		t.Errorf("third-level tracked file missing; got %v (and %v)", keys(got2), keys(got))
	}
}

func TestSchedulerUnitsAreProfileScoped(t *testing.T) {
	// Profile-aware file paths are not enough: the unit identifier lives INSIDE
	// the rendered file, so two profiles would write different files carrying
	// the same launchd Label and the second load would collide with the first.
	defaultPaths := &Paths{Profile: DefaultProfile}
	if got := defaultPaths.LaunchdLabelFor(SchedulerKindPush); got != launchdLabel {
		t.Errorf("default profile label changed: %s", got)
	}
	peerPaths := &Paths{Profile: "peer"}
	peer := peerPaths.LaunchdLabelFor(SchedulerKindPush)
	if peer == launchdLabel {
		t.Error("peer profile shares the default launchd label")
	}
	if !strings.Contains(peer, "peer") {
		t.Errorf("peer label = %s, want it to name the profile", peer)
	}
	if got := profileArg(DefaultProfile); got != "" {
		t.Errorf("default profile must render no --profile arg, got %q", got)
	}
	if got := profileArg("peer"); got != "peer" {
		t.Errorf("profileArg(peer) = %q", got)
	}
	if svc := peerPaths.SystemdServiceNameFor(SchedulerKindPush); svc == defaultPaths.SystemdServiceNameFor(SchedulerKindPush) {
		t.Error("systemd unit name is not profile-scoped")
	}
}

// TestMachineNamesSurvivesMinimalPATH reproduces the launchd failure: the
// scheduler hands agents a PATH without /usr/sbin, so a PATH-resolved scutil
// silently returned nothing and the owner guard locked out the owning machine.
// Only the scheduled run failed, so the interactive check kept reporting OK.
func TestMachineNamesSurvivesMinimalPATH(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("scutil is darwin-only")
	}
	full := MachineNames()
	t.Setenv("PATH", "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin") // launchd's PATH
	minimal := MachineNames()
	// Compare as sets: MachineNames promises which names identify this machine,
	// not what order they come back in. CheckOwner only ever tests membership.
	if !sameNameSet(full, minimal) {
		t.Fatalf("PATH without /usr/sbin changed identity: %v vs %v", minimal, full)
	}
	// And the guard must accept the name we would have recorded as owner.
	if err := CheckOwner(&Config{Owner: PreferredMachineName()}); err != nil {
		t.Fatalf("owner guard rejects this machine under launchd PATH: %v", err)
	}
}

// sameNameSet reports whether two name slices carry the same members,
// ignoring order and duplicates.
func sameNameSet(a, b []string) bool {
	set := func(in []string) map[string]bool {
		out := make(map[string]bool, len(in))
		for _, v := range in {
			out[v] = true
		}
		return out
	}
	x, y := set(a), set(b)
	if len(x) != len(y) {
		return false
	}
	for k := range x {
		if !y[k] {
			return false
		}
	}
	return true
}

// TestPushFinalizesLocalTargetAfterPartialTransfer pins the ordering the
// reviewer caught for a local mirror: converting a partial transfer to success
// at the CLI layer is not enough, because Push once returned before
// RefreshBaseline. SSH targets deliberately return before refresh because
// their baseline cannot confirm which paths reached the remote.
//
// Asserting on source order rather than behavior is deliberate: faking rsync
// here would need an exec seam that does not exist, and the bug was purely one
// of control flow - an early return placed above the finalization.
func TestPushFinalizesLocalTargetAfterPartialTransfer(t *testing.T) {
	src, err := os.ReadFile("sync.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	start := strings.Index(body, "func Push(")
	if start < 0 {
		t.Fatal("Push not found")
	}
	end := strings.Index(body[start:], "\nfunc ")
	if end < 0 {
		t.Fatal("could not bound Push")
	}
	push := body[start : start+end]

	rsyncAt := strings.Index(push, "runRsyncOutput(")
	guardAt := strings.Index(push, "rsyncExited(")
	baselineAt := strings.Index(push, "refreshBaseline(")
	if rsyncAt < 0 || baselineAt < 0 {
		t.Fatal("Push no longer calls runRsyncOutput and refreshBaseline")
	}
	if guardAt < 0 {
		t.Fatal("Push does not classify rsync exits; exit 23 or 25 would skip finalization")
	}
	if rsyncAt >= guardAt || guardAt >= baselineAt {
		t.Errorf("expected runRsyncOutput -> rsyncExited -> refreshBaseline, got %d/%d/%d",
			rsyncAt, guardAt, baselineAt)
	}
	if !strings.Contains(push, "return rsyncErr\n}") {
		t.Error("Push must surface the rsync error so callers can report it")
	}
}

// TestPeerPlistCarriesTheScheduledRunMarker is the other half of the
// scheduled-run detection: launchd sets no distinguishing variable of its
// own, so the job dot installs plants one. The cli side asserts the same
// literal, since nothing else binds the two packages.
func TestPeerPlistCarriesTheScheduledRunMarker(t *testing.T) {
	if !strings.Contains(peerPlistTmpl, "<key>DOT_SCHEDULED_RUN</key>") {
		t.Fatal("the peer launchd job must mark itself as a scheduled run")
	}
	if !strings.Contains(peerPlistTmpl, "<key>EnvironmentVariables</key>") {
		t.Fatal("the marker belongs in the plist's existing EnvironmentVariables dict")
	}
}

func TestRecordPeerRunStampsOnlyTheLegsThatRan(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		pushOnly, pullOnly           bool
		complete                     bool
		wantPull, wantPush, wantHeld bool
	}{
		{name: "full clean run", complete: true, wantPull: true, wantPush: true},
		{name: "push only", pushOnly: true, complete: true, wantPush: true},
		{name: "pull only", pullOnly: true, complete: true, wantPull: true},
		{name: "held transitions", wantPull: true, wantPush: true, wantHeld: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			paths := ResolveLocalPathsForProfile(t.TempDir(), PeerProfile)
			if err := recordPeerRun(paths, tc.pushOnly, tc.pullOnly, tc.complete); err != nil {
				t.Fatal(err)
			}
			st, err := LoadLocalState(paths)
			if err != nil {
				t.Fatal(err)
			}
			if got := !st.LastPull.IsZero(); got != tc.wantPull {
				t.Errorf("last_pull written = %v, want %v", got, tc.wantPull)
			}
			if got := !st.LastPush.IsZero(); got != tc.wantPush {
				t.Errorf("last_push written = %v, want %v", got, tc.wantPush)
			}
			if got := !st.LastHeld.IsZero(); got != tc.wantHeld {
				t.Errorf("last_held written = %v, want %v", got, tc.wantHeld)
			}
		})
	}
}

// A clean run must retire the previous run's hold marker, or `peer status`
// keeps reporting a held exchange that has since been completed.
func TestRecordPeerRunClearsAnEarlierHold(t *testing.T) {
	paths := ResolveLocalPathsForProfile(t.TempDir(), PeerProfile)
	if err := recordPeerRun(paths, false, false, false); err != nil {
		t.Fatal(err)
	}
	if err := recordPeerRun(paths, false, false, true); err != nil {
		t.Fatal(err)
	}
	st, err := LoadLocalState(paths)
	if err != nil {
		t.Fatal(err)
	}
	if !st.LastHeld.IsZero() {
		t.Fatalf("last_held = %s, want cleared by the clean run", st.LastHeld)
	}
	if st.LastPush.IsZero() || st.LastPull.IsZero() {
		t.Fatalf("clean run did not stamp both legs: %+v", st)
	}
}

// A one-way run never evaluates the deletions of the leg it skipped, so it
// cannot retire a hold: clearing it there would report a resolved exchange
// while the held transition is still pending.
func TestRecordPeerRunKeepsHoldAcrossOneWayRuns(t *testing.T) {
	for _, tc := range []struct {
		name               string
		pushOnly, pullOnly bool
	}{
		{name: "push only", pushOnly: true},
		{name: "pull only", pullOnly: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			paths := ResolveLocalPathsForProfile(t.TempDir(), PeerProfile)
			if err := recordPeerRun(paths, false, false, false); err != nil {
				t.Fatal(err)
			}
			if err := recordPeerRun(paths, tc.pushOnly, tc.pullOnly, true); err != nil {
				t.Fatal(err)
			}
			st, err := LoadLocalState(paths)
			if err != nil {
				t.Fatal(err)
			}
			if st.LastHeld.IsZero() {
				t.Fatal("one-way run cleared a hold it never re-evaluated")
			}
		})
	}
}
