package cli

import (
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/syncer"
)

// syncCLIFixture isolates HOME/XDG so syncer.Bootstrap resolves state,
// lock dir, and trees inside a temp sandbox, then returns the two trees.
type syncCLIFixture struct {
	home   string
	local  string
	mirror string
}

func newSyncCLIFixture(t *testing.T) *syncCLIFixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))

	local := filepath.Join(home, "workspace", "work")
	mirror := filepath.Join(home, "gdrive-workspace", "work")
	for _, dir := range []string{local, mirror} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	stateFile := filepath.Join(home, ".config", "dotfiles", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(stateFile), 0o755); err != nil {
		t.Fatal(err)
	}
	state := "modules:\n  gsync:\n    local_path: " + local + "\n    mirror_path: " + mirror + "\n"
	if err := os.WriteFile(stateFile, []byte(state), 0o644); err != nil {
		t.Fatal(err)
	}
	return &syncCLIFixture{home: home, local: local, mirror: mirror}
}

// seedOldConflict creates <tree>/.sync-conflicts/<stamp>/ with one file,
// aged 40 days so the default 30-day prune cutoff selects it.
func (f *syncCLIFixture) seedOldConflict(t *testing.T, tree, stamp string) string {
	t.Helper()
	dir := filepath.Join(tree, ".sync-conflicts", stamp)
	if err := os.MkdirAll(filepath.Join(dir, "from-gdrive"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "from-gdrive", "old.bin"), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-40 * 24 * time.Hour)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSyncSetupRequiresOwnerBeforeInstallingScheduler(t *testing.T) {
	f := newSyncCLIFixture(t)
	paths := syncer.ResolveLocalPaths(f.local)
	if err := syncer.SaveLocalConfig(paths, &syncer.LocalConfig{
		Target:      "local:" + f.mirror,
		Propagation: syncer.DefaultPropagationPolicy(),
	}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(paths.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &syncer.Config{LocalPaths: paths}
	err = setupSchedulerOwner(cfg, "", false, false)
	if err == nil {
		t.Fatal("ownerless sync setup unexpectedly succeeded")
	}
	preferred := syncer.PreferredMachineName()
	for _, want := range []string{preferred, "--owner"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ownerless setup error %q missing %q", err, want)
		}
	}
	after, readErr := os.ReadFile(paths.ConfigFile)
	if readErr != nil {
		t.Fatalf("read store config after rejected setup: %v", readErr)
	}
	if string(after) != string(before) {
		t.Errorf("ownerless setup wrote scheduler config before rejection:\nwant %q\n got %q", before, after)
	}
}

func TestSyncSetupOwnerFlagRecordsOwnerAndRejectsUnresolvableSelf(t *testing.T) {
	f := newSyncCLIFixture(t)
	paths := syncer.ResolveLocalPaths(f.local)
	if err := syncer.SaveLocalConfig(paths, &syncer.LocalConfig{
		Target:      "local:" + f.mirror,
		Propagation: syncer.DefaultPropagationPolicy(),
	}); err != nil {
		t.Fatal(err)
	}

	cfg := &syncer.Config{LocalPaths: paths}
	if err := setupSchedulerOwner(cfg, "scheduler-host", true, false); err != nil {
		t.Fatalf("--owner scheduler-host: %v", err)
	}
	stored, ok, err := syncer.LoadLocalConfig(paths)
	if err != nil || !ok || stored.Owner != "scheduler-host" {
		t.Fatalf("--owner did not persist scheduler owner: cfg=%#v ok=%t err=%v", stored, ok, err)
	}
	if err := setupSchedulerOwner(&syncer.Config{Owner: stored.Owner, LocalPaths: paths}, "", false, false); err != nil {
		t.Fatalf("setup with an existing owner should succeed: %v", err)
	}

	oldPreferred := preferredMachineName
	preferredMachineName = func() string { return "" }
	defer func() { preferredMachineName = oldPreferred }()
	if err := setupSchedulerOwner(&syncer.Config{LocalPaths: paths}, "self", true, false); err == nil || !strings.Contains(err.Error(), "cannot determine") {
		t.Fatalf("unresolvable --owner self error = %v", err)
	}
	if err := setupSchedulerOwner(&syncer.Config{LocalPaths: paths}, "", false, true); err == nil || !strings.Contains(err.Error(), "cannot determine") {
		t.Fatalf("ownerless dry-run should still fail name resolution: %v", err)
	}
}

func TestSyncConflictsPruneCLI_DryRunThenYes(t *testing.T) {
	f := newSyncCLIFixture(t)
	wsConflict := f.seedOldConflict(t, f.local, "2026-01-01T00-00-00Z")
	mirrorConflict := f.seedOldConflict(t, f.mirror, "2026-01-02T00-00-00Z")

	out, errOut, err := runDotForTest("gsync", "conflicts", "prune", "--dry-run")
	if err != nil {
		t.Fatalf("prune --dry-run: %v\nstderr=%s", err, errOut)
	}
	if !strings.Contains(out, "Would reclaim") {
		t.Errorf("dry-run output missing plan summary:\n%s", out)
	}
	for _, label := range []string{"workspace", "mirror"} {
		if !strings.Contains(out, label) {
			t.Errorf("dry-run plan missing %s tree section:\n%s", label, out)
		}
	}
	for _, dir := range []string{wsConflict, mirrorConflict} {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("dry-run must not delete %s: %v", dir, err)
		}
	}

	out, errOut, err = runDotForTest("gsync", "conflicts", "prune", "--yes")
	if err != nil {
		t.Fatalf("prune --yes: %v\nstderr=%s", err, errOut)
	}
	if !strings.Contains(out, "pruned") {
		t.Errorf("prune output missing result:\n%s", out)
	}
	for _, dir := range []string{wsConflict, mirrorConflict} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("%s should be removed after prune --yes", dir)
		}
	}
}

func TestSyncConflictsPruneCLI_LockHeldDeletesNothing(t *testing.T) {
	f := newSyncCLIFixture(t)
	conflict := f.seedOldConflict(t, f.local, "2026-01-01T00-00-00Z")

	// Resolve the lock directory the way the command under test does, rather
	// than joining one by hand: cfg.LockDir is systemPaths.LockDir, and
	// os.UserCacheDir returns Library/Caches on darwin, so a literal ".cache"
	// join would pass the linux job and fail the macOS one.
	state, err := config.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := syncer.ResolveConfigReadOnly(state)
	if err != nil {
		t.Fatal(err)
	}
	release, err := syncer.AcquireLock(cfg.LockDir)
	if err != nil {
		t.Fatalf("acquiring lock: %v", err)
	}
	defer release()

	out, errOut, err := runDotForTest("gsync", "conflicts", "prune", "--yes")
	if err != nil {
		t.Fatalf("prune under held lock should not error: %v\nstderr=%s", err, errOut)
	}
	if !strings.Contains(out, "another sync is running") {
		t.Errorf("expected lock-held notice, got:\n%s", out)
	}
	if _, err := os.Stat(conflict); err != nil {
		t.Errorf("held lock must prevent deletion: %v", err)
	}
}

func TestSyncConflictsSSHProfileListsAndPrunesRemoteRoots(t *testing.T) {
	f := newSyncCLIFixture(t)
	localConflict := f.seedOldConflict(t, f.local, "2026-01-02T00-00-00Z")
	remote := t.TempDir()
	remoteConflict := f.seedOldConflict(t, remote, "2026-01-03T00-00-00Z")
	peerHomeConflict := filepath.Join(f.home, ".dot-peer-conflicts", "2026-01-04T00-00-00Z")
	if err := os.MkdirAll(peerHomeConflict, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(peerHomeConflict, "old.bin"), []byte("peer-home"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-40 * 24 * time.Hour)
	if err := os.Chtimes(peerHomeConflict, old, old); err != nil {
		t.Fatal(err)
	}

	peerConfig := filepath.Join(f.local, ".dotfiles", "peer", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(peerConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(peerConfig, []byte("target: ssh:fake-peer:"+remote+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stubSSHForSyncCLI(t)

	out, errOut, err := runDotForTest("sync", "conflicts", "--profile=peer")
	if err != nil {
		t.Fatalf("SSH conflict list: %v\nstderr=%s", err, errOut)
	}
	if !strings.Contains(out, "remote target") || !strings.Contains(out, "remote home") {
		t.Fatalf("remote conflict sections missing:\n%s", out)
	}
	if !strings.Contains(out, "2026-01-03T00-00-00Z") || !strings.Contains(out, "2026-01-04T00-00-00Z") {
		t.Fatalf("remote conflict timestamps missing:\n%s", out)
	}
	if strings.Contains(out, "under /.sync-conflicts") {
		t.Fatalf("SSH conflict list regressed to local root scan:\n%s", out)
	}

	out, errOut, err = runDotForTest("sync", "conflicts", "prune", "--profile=peer", "--dry-run")
	if err != nil {
		t.Fatalf("SSH conflict prune --dry-run: %v\nstderr=%s", err, errOut)
	}
	if !strings.Contains(out, "remote target") || !strings.Contains(out, "remote home") || !strings.Contains(out, "dry-run") {
		t.Fatalf("remote dry-run plan incomplete:\n%s", out)
	}
	for _, dir := range []string{remoteConflict, peerHomeConflict} {
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("remote dry-run removed %s: %v", dir, err)
		}
	}

	out, errOut, err = runDotForTest("sync", "conflicts", "prune", "--profile=peer", "--remote-only", "--yes")
	if err != nil {
		t.Fatalf("SSH conflict prune --yes: %v\nstderr=%s", err, errOut)
	}
	if !strings.Contains(out, "pruned") {
		t.Fatalf("remote prune output missing result:\n%s", out)
	}
	for _, dir := range []string{remoteConflict, peerHomeConflict} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("remote conflict %s remains after prune, stat err=%v", dir, err)
		}
	}
	if _, err := os.Stat(localConflict); err != nil {
		t.Fatalf("--remote-only removed local conflict backup: %v", err)
	}
}

func TestSyncConflictsRemoteOnlyRejectsLocalTarget(t *testing.T) {
	newSyncCLIFixture(t)
	_, _, err := runDotForTest("sync", "conflicts", "prune", "--remote-only", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "requires an SSH target") {
		t.Fatalf("--remote-only local target error = %v", err)
	}
}

func stubSSHForSyncCLI(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	ssh := filepath.Join(bin, "ssh")
	// Execute the exact remote shell command locally. The command generated by
	// the syncer is the last ssh argument, so this exercises quoting and all
	// remote root/timestamp guards without requiring a network fixture.
	script := "#!/bin/sh\nlast=\nfor arg do last=$arg; done\nexec /bin/sh -c \"$last\"\n"
	if err := os.WriteFile(ssh, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestSyncPullCLI_StrictFlagWiring(t *testing.T) {
	if _, err := osexec.LookPath("rsync"); err != nil {
		t.Skip("rsync not installed; gsync preflight would refuse to run")
	}
	f := newSyncCLIFixture(t)

	// Seed a baseline-tracked file whose mirror copy changed content while
	// preserving size+mtime — invisible to the default fast tier, visible
	// only under --strict. Must use an extension the default include-mode
	// filter syncs (binary payloads only).
	rel := "assets/data.png"
	mirrorAbs := filepath.Join(f.mirror, rel)
	localAbs := filepath.Join(f.local, rel)
	for _, abs := range []string{mirrorAbs, localAbs} {
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte("v1-bytes"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	localPaths := syncer.ResolveLocalPaths(f.local + "/")
	if err := syncer.EnsureLocalLayout(localPaths); err != nil {
		t.Fatal(err)
	}
	base, err := syncer.FingerprintFile(mirrorAbs, syncer.FingerprintStrict)
	if err != nil {
		t.Fatal(err)
	}
	if err := syncer.SaveBaselineManifest(localPaths.BaselineFile, map[string]syncer.Fingerprint{rel: base}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(localAbs, base.Mtime, base.Mtime); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mirrorAbs, []byte("v2-BYTES"), 0o644); err != nil { // same length
		t.Fatal(err)
	}
	if err := os.Chtimes(mirrorAbs, base.Mtime, base.Mtime); err != nil {
		t.Fatal(err)
	}

	out, errOut, err := runDotForTest("gsync", "pull", "--dry-run")
	if err != nil {
		t.Fatalf("pull --dry-run: %v\nstderr=%s", err, errOut)
	}
	if strings.Contains(out, "Updates from Drive") {
		t.Errorf("default fast tier should not plan a pull here:\n%s", out)
	}

	out, errOut, err = runDotForTest("gsync", "pull", "--strict", "--dry-run")
	if err != nil {
		t.Fatalf("pull --strict --dry-run: %v\nstderr=%s", err, errOut)
	}
	if !strings.Contains(out, "Updates from Drive: 1") {
		t.Errorf("--strict must reach PullTracked and plan the pull:\n%s", out)
	}
}

func TestSyncMirrorCLI_SetsLocalConfigAndPrints(t *testing.T) {
	f := newSyncCLIFixture(t)
	newMirror := filepath.Join(f.home, "Dropbox", "work")

	// Set the mirror.
	out, errOut, err := runDotForTest("gsync", "mirror", newMirror)
	if err != nil {
		t.Fatalf("gsync mirror set: %v\nstdout=%s\nstderr=%s", err, out, errOut)
	}
	if !strings.Contains(out, "sync target set") || !strings.Contains(out, newMirror) {
		t.Errorf("set output unexpected:\n%s", out)
	}

	// Local config (authoritative for the current workspace) must carry it.
	localCfg := filepath.Join(f.local, ".dotfiles", "sync", "config.yaml")
	data, err := os.ReadFile(localCfg)
	if err != nil {
		t.Fatalf("read local config: %v", err)
	}
	if !strings.Contains(string(data), "mirror_path: "+newMirror) {
		t.Errorf("local config missing mirror_path:\n%s", data)
	}

	// No-arg prints the resolved mirror.
	out, _, err = runDotForTest("gsync", "mirror")
	if err != nil {
		t.Fatalf("gsync mirror print: %v", err)
	}
	if !strings.Contains(out, newMirror) {
		t.Errorf("print output should show %q:\n%s", newMirror, out)
	}
}

func TestSyncMirrorCLI_PrintAndDryRunAreReadOnly(t *testing.T) {
	f := newSyncCLIFixture(t)
	store := filepath.Join(f.local, ".dotfiles", "sync")

	// No-arg print on a fresh workspace must not create the local layout.
	if _, _, err := runDotForTest("gsync", "mirror"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store); !os.IsNotExist(err) {
		t.Errorf("no-arg print created the local gsync layout: %v", err)
	}

	// --dry-run set: shows the would-be path, still no layout, no .gitignore.
	newMirror := filepath.Join(f.home, "Dropbox", "work")
	out, _, err := runDotForTest("gsync", "mirror", newMirror, "--dry-run")
	if err != nil {
		t.Fatalf("gsync mirror --dry-run: %v", err)
	}
	if !strings.Contains(out, "[dry-run]") || !strings.Contains(out, newMirror) {
		t.Errorf("dry-run output unexpected:\n%s", out)
	}
	if _, err := os.Stat(store); !os.IsNotExist(err) {
		t.Errorf("dry-run created the local gsync layout: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.local, ".gitignore")); !os.IsNotExist(err) {
		t.Errorf("dry-run created .gitignore: %v", err)
	}
}

func TestSyncMirrorCLI_HonorsHomeOverride(t *testing.T) {
	f := newSyncCLIFixture(t)
	other := t.TempDir() // a different user's home
	if err := os.MkdirAll(filepath.Join(other, ".config", "dotfiles"), 0o755); err != nil {
		t.Fatal(err)
	}

	// ~ in the path must expand against --home, and global state must be
	// written for that home — not the current HOME.
	out, errOut, err := runDotForTest("gsync", "mirror", "~/Dropbox/work", "--home", other)
	if err != nil {
		t.Fatalf("gsync mirror --home: %v\nstdout=%s\nstderr=%s", err, out, errOut)
	}
	wantMirror := filepath.Join(other, "Dropbox", "work")
	if !strings.Contains(out, wantMirror) {
		t.Errorf("~ should expand against --home %q:\n%s", wantMirror, out)
	}
	otherCfg, err := os.ReadFile(filepath.Join(other, ".config", "dotfiles", "config.yaml"))
	if err != nil {
		t.Fatalf("read --home state: %v", err)
	}
	if !strings.Contains(string(otherCfg), "mirror_path: "+wantMirror) {
		t.Errorf("--home global state missing mirror_path:\n%s", otherCfg)
	}
	// The current user's state must NOT have been written with this mirror.
	if cur, _ := os.ReadFile(filepath.Join(f.home, ".config", "dotfiles", "config.yaml")); strings.Contains(string(cur), wantMirror) {
		t.Errorf("current-user state wrongly written under --home:\n%s", cur)
	}
}

// The doctor's topology fix: a peer target set this way keeps the owner,
// epoch and every other key of the peer store (dot peer init would record
// this Mac as the owner).
func TestSyncTargetOnThePeerProfileKeepsOwnerAndEpoch(t *testing.T) {
	f := newSyncCLIFixture(t)
	store := filepath.Join(f.local, ".dotfiles", "peer", "config.yaml")
	writeCLITestFile(t, store, "target: ssh:peer-alias:/Users/a/elsewhere\nowner: the-coordinator\nowner_epoch: 2\npropagation:\n  create: true\n  update: true\n  delete: true\n")
	if _, errOut, err := runDotForTest("sync", "target", "--profile=peer", "ssh:peer-alias:/Users/a/work"); err != nil {
		t.Fatalf("%v\n%s", err, errOut)
	}
	got, _, err := syncer.LoadLocalConfig(syncer.ResolveLocalPathsForProfile(f.local, "peer"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Target != "ssh:peer-alias:/Users/a/work" || got.Owner != "the-coordinator" || got.OwnerEpoch != 2 {
		t.Fatalf("store = %+v", got)
	}
}

// A refusal before the engine runs (here a missing mirror) is recorded like a
// refused plan, so `dot sync status` shows it; a dry-run records nothing.
func TestSyncPushCLI_RecordsAPreflightRefusal(t *testing.T) {
	if _, err := osexec.LookPath("rsync"); err != nil {
		t.Skip("rsync not installed; gsync preflight would refuse to run")
	}
	f := newSyncCLIFixture(t)
	if err := os.RemoveAll(f.mirror); err != nil {
		t.Fatal(err)
	}
	paths := syncer.ResolveLocalPaths(f.local + "/")
	if _, errOut, err := runDotForTest("gsync", "push", "--mode=clean", "--dry-run"); err != nil {
		t.Fatalf("push --dry-run: %v\nstderr=%s", err, errOut)
	}
	if st, _ := syncer.LoadLocalState(paths); st != nil && st.LastPushError != "" {
		t.Fatalf("a dry-run recorded %q", st.LastPushError)
	}
	if _, errOut, err := runDotForTest("gsync", "push", "--mode=clean"); err != nil {
		t.Fatalf("push: %v\nstderr=%s", err, errOut)
	}
	st, err := syncer.LoadLocalState(paths)
	if err != nil {
		t.Fatal(err)
	}
	if want := "push refused: Mirror path missing: "; !strings.HasPrefix(st.LastPushError, want) {
		t.Errorf("LastPushError = %q, want prefix %q", st.LastPushError, want)
	}
	if out, _, _ := runDotForTest("gsync", "status"); !strings.Contains(out, "Push stalled") || !strings.Contains(out, "Mirror path missing") {
		t.Errorf("status does not name the refusal:\n%s", out)
	}
	// A paused profile records nothing, whatever else blocks the push.
	if err := syncer.UpdateLocalState(paths, func(s *syncer.LocalState) { s.LastPushError = "" }); err != nil {
		t.Fatal(err)
	}
	if _, errOut, err := runDotForTest("gsync", "pause"); err != nil {
		t.Fatalf("pause: %v\nstderr=%s", err, errOut)
	}
	if _, errOut, err := runDotForTest("gsync", "push", "--mode=clean"); err != nil {
		t.Fatalf("paused push: %v\nstderr=%s", err, errOut)
	}
	if st, _ = syncer.LoadLocalState(paths); st.LastPushError != "" {
		t.Errorf("a paused profile recorded %q", st.LastPushError)
	}
}

// An owner mismatch is recorded like any refusal, but not on a paused profile.
func TestSyncPushCLI_RecordsAnOwnerRefusalUnlessPaused(t *testing.T) {
	f := newSyncCLIFixture(t)
	paths := syncer.ResolveLocalPaths(f.local + "/")
	if err := syncer.SaveLocalConfig(paths, &syncer.LocalConfig{
		Target:      "local:" + f.mirror,
		Propagation: syncer.DefaultPropagationPolicy(),
		Owner:       "some-other-mac",
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runDotForTest("gsync", "push", "--mode=clean"); err == nil {
		t.Fatal("a push on a non-owner succeeded")
	}
	st, err := syncer.LoadLocalState(paths)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(st.LastPushError, "push refused: profile ") {
		t.Fatalf("LastPushError = %q, want the owner refusal", st.LastPushError)
	}

	if err := syncer.UpdateLocalState(paths, func(s *syncer.LocalState) { s.LastPushError = "" }); err != nil {
		t.Fatal(err)
	}
	if _, errOut, err := runDotForTest("gsync", "pause"); err != nil {
		t.Fatalf("pause: %v\nstderr=%s", err, errOut)
	}
	_, _, _ = runDotForTest("gsync", "push", "--mode=clean")
	if st, _ = syncer.LoadLocalState(paths); st.LastPushError != "" {
		t.Errorf("a paused profile recorded %q", st.LastPushError)
	}
}

// A stored shared entry the cleaning drops is listed as ignored, and `shared
// clear` clears it instead of reporting an empty list (#231).
func TestSyncSharedCLI_ListsAndClearsAnEntryTheCleaningDrops(t *testing.T) {
	f := newSyncCLIFixture(t)
	if _, errOut, err := runDotForTest("gsync", "shared", "add", "team/x"); err != nil {
		t.Fatalf("shared add: %v\nstderr=%s", err, errOut)
	}
	paths := syncer.ResolveLocalPaths(f.local + "/")
	cfg, _, err := syncer.LoadLocalConfig(paths)
	if err != nil {
		t.Fatal(err)
	}
	cfg.SharedExcludes = append(cfg.SharedExcludes, "/team/ops")
	if err := syncer.SaveLocalConfig(paths, cfg); err != nil {
		t.Fatal(err)
	}
	out, errOut, err := runDotForTest("gsync", "shared", "list")
	if err != nil || !strings.Contains(out, "team/x") || !strings.Contains(out, "ignored") || !strings.Contains(out, `"/team/ops"`) {
		t.Fatalf("shared list = %v\n%s%s", err, out, errOut)
	}
	if out, errOut, err := runDotForTest("gsync", "shared", "clear", "--yes"); err != nil || !strings.Contains(out, "Cleared 2") {
		t.Fatalf("shared clear = %v\n%s%s", err, out, errOut)
	}
	if cfg, _, _ := syncer.LoadLocalConfig(paths); len(cfg.SharedExcludes) != 0 {
		t.Errorf("stored entries after clear = %q", cfg.SharedExcludes)
	}
}
