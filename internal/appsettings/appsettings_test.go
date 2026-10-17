package appsettings

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

func TestLoadManifestHasExpectedTokens(t *testing.T) {
	mf, err := LoadManifest()
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	expected := []string{
		"1password", "raycast", "obsidian", "fantastical", "arc",
		"cmux", "cursor", "chatgpt", "claude", "slack",
		"bartender", "one-switch", "popclip", "shottr", "wispr-flow",
		"forklift", "yoink", "hazel", "termius", "keyboard-maestro", "moom",
	}
	set := make(map[string]bool)
	for _, a := range mf.Apps {
		set[a.Token] = true
		if len(a.Paths) == 0 {
			t.Errorf("token %q: no paths", a.Token)
		}
	}
	for _, want := range expected {
		if !set[want] {
			t.Errorf("manifest missing token %q", want)
		}
	}
	if len(mf.Tokens()) != len(mf.Apps) {
		t.Errorf("Tokens() length mismatch: got %d want %d", len(mf.Tokens()), len(mf.Apps))
	}
}

func TestIsExcluded(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"Preferences/com.foo.plist", false},
		{"Application Support/Foo/Cache/blob", true},
		{"Application Support/Foo/GPUCache/state", true},
		{"Application Support/Foo/Singleton.lock", true},
		{"Application Support/Foo/log.log", true},
		{"Application Support/Foo/.DS_Store", true},
		{"Application Support/Foo/Code Cache/file", true},
		{"Containers/a.b.c/.com.apple.containermanagerd.metadata.plist", true},
		{".com.apple.containermanagerd.metadata.plist", true},
	}
	for _, c := range cases {
		got := isExcluded(c.path)
		if got != c.want {
			t.Errorf("isExcluded(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestEngineBackupAndRestoreRoundtrip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix-only paths")
	}
	home := t.TempDir()
	// Build fake Library layout with one plist and one support dir w/ caches.
	lib := filepath.Join(home, "Library")
	prefDir := filepath.Join(lib, "Preferences")
	if err := os.MkdirAll(prefDir, 0o755); err != nil {
		t.Fatal(err)
	}
	plistPath := filepath.Join(prefDir, "com.test.moom.plist")
	if err := os.WriteFile(plistPath, []byte("moom-plist"), 0o644); err != nil {
		t.Fatal(err)
	}

	backup := filepath.Join(home, "backup")

	// Manifest with a single known entry matching the test file.
	mf := &Manifest{Apps: []AppEntry{{
		Token: "testmoom",
		Paths: []PathEntry{{Type: "pref", Path: "Preferences/com.test.moom.plist"}},
	}}}

	runner := exec.NewRunner(false, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	eng := &Engine{
		Runner:   runner,
		HomeDir:  home,
		Root:     backup,
		Hostname: "testhost",
		Manifest: mf,
	}

	sum, err := eng.Backup(context.Background(), BackupOptions{Tokens: []string{"testmoom"}})
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	if sum.Files != 1 {
		t.Errorf("backup files: got %d want 1", sum.Files)
	}
	archivePath := filepath.Join(backup, "app-settings", "testhost", "testmoom", "Preferences", "com.test.moom.plist")
	if _, err := os.Stat(archivePath); err != nil {
		t.Errorf("archive path missing: %v", err)
	}

	// Mutate live file, then restore and confirm original contents returned.
	if err := os.WriteFile(plistPath, []byte("mutated"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Restore(context.Background(), RestoreOptions{Tokens: []string{"testmoom"}}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	got, err := os.ReadFile(plistPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "moom-plist" {
		t.Errorf("restore content: got %q want %q", got, "moom-plist")
	}
}

func TestBackupSkipsExcludedSubtrees(t *testing.T) {
	home := t.TempDir()
	lib := filepath.Join(home, "Library", "Application Support", "Foo")
	if err := os.MkdirAll(filepath.Join(lib, "Caches"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lib, "settings.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lib, "Caches", "big.blob"), []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}

	mf := &Manifest{Apps: []AppEntry{{
		Token: "foo",
		Paths: []PathEntry{{Type: "support", Path: "Application Support/Foo"}},
	}}}
	runner := exec.NewRunner(false, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	eng := &Engine{Runner: runner, HomeDir: home, Root: filepath.Join(home, "bk"), Hostname: "h", Manifest: mf}

	if _, err := eng.Backup(context.Background(), BackupOptions{}); err != nil {
		t.Fatal(err)
	}
	archiveRoot := filepath.Join(home, "bk", "app-settings", "h", "foo", "Application Support", "Foo")
	if _, err := os.Stat(filepath.Join(archiveRoot, "settings.json")); err != nil {
		t.Errorf("settings.json missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(archiveRoot, "Caches")); err == nil {
		t.Errorf("Caches/ should have been excluded")
	}
}

func TestDryRunProducesNoFiles(t *testing.T) {
	home := t.TempDir()
	prefDir := filepath.Join(home, "Library", "Preferences")
	if err := os.MkdirAll(prefDir, 0o755); err != nil {
		t.Fatal(err)
	}
	plistPath := filepath.Join(prefDir, "com.test.dry.plist")
	if err := os.WriteFile(plistPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	mf := &Manifest{Apps: []AppEntry{{
		Token: "dry",
		Paths: []PathEntry{{Type: "pref", Path: "Preferences/com.test.dry.plist"}},
	}}}
	backup := filepath.Join(home, "bk")
	runner := exec.NewRunner(true, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	eng := &Engine{Runner: runner, HomeDir: home, Root: backup, Hostname: "h", Manifest: mf}

	if _, err := eng.Backup(context.Background(), BackupOptions{}); err != nil {
		t.Fatalf("dry-run backup: %v", err)
	}
	// The host root dir itself is skipped in dry-run (MkdirAll), so no files.
	var foundFiles bool
	if _, err := os.Stat(backup); err == nil {
		_ = filepath.WalkDir(backup, func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.Type().IsRegular() {
				foundFiles = true
			}
			return nil
		})
	}
	if foundFiles {
		t.Error("dry-run produced regular files")
	}
}

func newRoundtripEngine(t *testing.T, home string, mf *Manifest) *Engine {
	t.Helper()
	runner := exec.NewRunner(false, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	return &Engine{
		Runner:   runner,
		HomeDir:  home,
		Root:     filepath.Join(home, "bk"),
		Hostname: "h",
		Manifest: mf,
	}
}

func TestRestoreSnapshotsExistingFilesFirst(t *testing.T) {
	home := t.TempDir()
	plistPath := filepath.Join(home, "Library", "Preferences", "com.test.pre.plist")
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plistPath, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	mf := &Manifest{Apps: []AppEntry{{
		Token: "pre",
		Paths: []PathEntry{{Type: "pref", Path: "Preferences/com.test.pre.plist"}},
	}}}
	eng := newRoundtripEngine(t, home, mf)

	if _, err := eng.Backup(context.Background(), BackupOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plistPath, []byte("live-edit"), 0o644); err != nil {
		t.Fatal(err)
	}

	sum, err := eng.Restore(context.Background(), RestoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if sum.PreBackupPath == "" {
		t.Fatal("PreBackupPath not set despite overwriting a live file")
	}
	pre, err := os.ReadFile(filepath.Join(sum.PreBackupPath, "pre", "Preferences", "com.test.pre.plist"))
	if err != nil || string(pre) != "live-edit" {
		t.Errorf("pre-restore copy wrong: %q err=%v", pre, err)
	}
	got, _ := os.ReadFile(plistPath)
	if string(got) != "original" {
		t.Errorf("restore content: %q", got)
	}
}

func TestBackupFailureKeepsPreviousArchive(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("chmod-based failure injection is ineffective as root")
	}
	home := t.TempDir()
	prefDir := filepath.Join(home, "Library", "Preferences")
	support := filepath.Join(home, "Library", "Application Support", "Foo")
	for _, d := range []string{prefDir, support} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Two paths on one token: a TCC-style unreadable one and a readable one.
	guarded := filepath.Join(support, "settings.json")
	readable := filepath.Join(prefDir, "com.foo.plist")
	if err := os.WriteFile(guarded, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(readable, []byte("r1"), 0o644); err != nil {
		t.Fatal(err)
	}
	mf := &Manifest{Apps: []AppEntry{{
		Token: "foo",
		Paths: []PathEntry{
			{Type: "support", Path: "Application Support/Foo"},
			{Type: "pref", Path: "Preferences/com.foo.plist"},
		},
	}}}
	eng := newRoundtripEngine(t, home, mf)

	if _, err := eng.Backup(context.Background(), BackupOptions{}); err != nil {
		t.Fatal(err)
	}
	guardedArchive := filepath.Join(eng.HostRoot(), "foo", "Application Support", "Foo", "settings.json")
	readableArchive := filepath.Join(eng.HostRoot(), "foo", "Preferences", "com.foo.plist")
	if _, err := os.Stat(guardedArchive); err != nil {
		t.Fatal(err)
	}

	// Mutate both, then make the guarded path unreadable so the next backup
	// fails on it but still stages the readable one.
	if err := os.WriteFile(guarded, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(readable, []byte("r2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(guarded, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(guarded, 0o644) })

	sum, err := eng.Backup(context.Background(), BackupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Failed == 0 {
		t.Fatal("expected Failed > 0 for unreadable source")
	}
	// Guarded path keeps its previous (v1) copy — not wiped, not partial.
	if got, err := os.ReadFile(guardedArchive); err != nil || string(got) != "v1" {
		t.Errorf("guarded archive should keep v1, got %q err=%v", got, err)
	}
	// Readable path on the same token IS refreshed (partial salvage).
	if got, err := os.ReadFile(readableArchive); err != nil || string(got) != "r2" {
		t.Errorf("readable archive should refresh to r2, got %q err=%v", got, err)
	}
	if entries, _ := os.ReadDir(filepath.Join(eng.HostRoot(), ".staging")); len(entries) > 0 {
		t.Errorf("staging leftovers remain: %v", entries)
	}
}

// TestBackupCommitsReadableWhenFailedPathNeverArchived verifies that on a
// first-ever backup where one path fails and has no prior archive copy,
// the readable path is still committed — nothing is lost because the
// failed path was never archived, and refusing to commit would mean a
// persistently-unreadable path blocks the readable one from ever backing
// up (the regression this fix targets).
func TestBackupCommitsReadableWhenFailedPathNeverArchived(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("chmod-based failure injection is ineffective as root")
	}
	home := t.TempDir()
	prefDir := filepath.Join(home, "Library", "Preferences")
	if err := os.MkdirAll(prefDir, 0o755); err != nil {
		t.Fatal(err)
	}
	readable := filepath.Join(prefDir, "com.read.plist")
	guarded := filepath.Join(prefDir, "com.guard.plist")
	if err := os.WriteFile(readable, []byte("r1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(guarded, []byte("g1"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(guarded, 0o644) })
	mf := &Manifest{Apps: []AppEntry{{
		Token: "foo",
		Paths: []PathEntry{
			{Type: "pref", Path: "Preferences/com.read.plist"},
			{Type: "pref", Path: "Preferences/com.guard.plist"},
		},
	}}}
	eng := newRoundtripEngine(t, home, mf)

	sum, err := eng.Backup(context.Background(), BackupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Failed == 0 {
		t.Fatal("expected Failed > 0")
	}
	// Readable path committed; guarded path simply absent (never archived).
	if got, err := os.ReadFile(filepath.Join(eng.HostRoot(), "foo", "Preferences", "com.read.plist")); err != nil || string(got) != "r1" {
		t.Errorf("readable path not committed: %q err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(eng.HostRoot(), "foo", "Preferences", "com.guard.plist")); !os.IsNotExist(err) {
		t.Errorf("guarded path should be absent (never archived): %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(eng.HostRoot(), ".staging")); len(entries) > 0 {
		t.Errorf("staging leftovers remain: %v", entries)
	}
}

func TestBackupSeedsArchivedCopyWhenLiveMissing(t *testing.T) {
	home := t.TempDir()
	plistPath := filepath.Join(home, "Library", "Preferences", "com.test.gone.plist")
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plistPath, []byte("only-copy"), 0o644); err != nil {
		t.Fatal(err)
	}
	mf := &Manifest{Apps: []AppEntry{{
		Token: "gone",
		Paths: []PathEntry{{Type: "pref", Path: "Preferences/com.test.gone.plist"}},
	}}}
	eng := newRoundtripEngine(t, home, mf)

	if _, err := eng.Backup(context.Background(), BackupOptions{}); err != nil {
		t.Fatal(err)
	}
	// App uninstalled: live file gone. Re-backup must not wipe the archive.
	if err := os.Remove(plistPath); err != nil {
		t.Fatal(err)
	}
	sum, err := eng.Backup(context.Background(), BackupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Failed != 0 {
		t.Fatalf("unexpected failures: %+v", sum)
	}
	archived := filepath.Join(eng.HostRoot(), "gone", "Preferences", "com.test.gone.plist")
	got, err := os.ReadFile(archived)
	if err != nil || string(got) != "only-copy" {
		t.Errorf("archived only-copy lost: %q err=%v", got, err)
	}
}

// seedOrphanPrev simulates a crash between "final -> prev" and
// "staging -> final": only <token>.prev exists, holding "old".
func seedOrphanPrev(t *testing.T, eng *Engine, token string) {
	t.Helper()
	prev := filepath.Join(eng.HostRoot(), token+".prev")
	if err := os.MkdirAll(filepath.Join(prev, "Preferences"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prev, "Preferences", "com.x.plist"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestBackupRecoversOrphanPrev drives recovery through the real Backup
// entry point (not recoverStaging directly), pinning the call-site wiring.
// The token has no live source, so Backup does no copying — only the
// recovery pass runs.
func TestBackupRecoversOrphanPrev(t *testing.T) {
	home := t.TempDir()
	mf := &Manifest{Apps: []AppEntry{{
		Token: "x",
		Paths: []PathEntry{{Type: "pref", Path: "Preferences/com.x.plist"}},
	}}}
	eng := newRoundtripEngine(t, home, mf)
	if err := os.MkdirAll(eng.HostRoot(), 0o755); err != nil {
		t.Fatal(err)
	}
	seedOrphanPrev(t, eng, "x")

	if _, err := eng.Backup(context.Background(), BackupOptions{}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(eng.HostRoot(), "x", "Preferences", "com.x.plist"))
	if err != nil || string(got) != "old" {
		t.Errorf("Backup did not recover orphan .prev: %q err=%v", got, err)
	}
}

// TestRestoreRecoversOrphanPrev pins the Restore call site, which has its
// own recoverStaging invocation independent of Backup's.
func TestRestoreRecoversOrphanPrev(t *testing.T) {
	home := t.TempDir()
	mf := &Manifest{Apps: []AppEntry{{
		Token: "x",
		Paths: []PathEntry{{Type: "pref", Path: "Preferences/com.x.plist"}},
	}}}
	eng := newRoundtripEngine(t, home, mf)
	if err := os.MkdirAll(eng.HostRoot(), 0o755); err != nil {
		t.Fatal(err)
	}
	seedOrphanPrev(t, eng, "x")

	if _, err := eng.Restore(context.Background(), RestoreOptions{Tokens: []string{"x"}}); err != nil {
		t.Fatal(err)
	}
	// Recovery renamed x.prev → x, then Restore copied it back to Library.
	got, err := os.ReadFile(filepath.Join(home, "Library", "Preferences", "com.x.plist"))
	if err != nil || string(got) != "old" {
		t.Errorf("Restore did not recover+apply orphan .prev: %q err=%v", got, err)
	}
}

func TestAdoptArchivedAppsSynthesizesEntries(t *testing.T) {
	home := t.TempDir()
	mf := &Manifest{Apps: []AppEntry{{
		Token: "known",
		Paths: []PathEntry{{Type: "pref", Path: "Preferences/com.known.plist"}},
	}}}
	eng := newRoundtripEngine(t, home, mf)

	// Archive contains a discovered app that isn't in the manifest.
	tokenDir := filepath.Join(eng.HostRoot(), "Moom Classic")
	for _, p := range []string{
		filepath.Join(tokenDir, "Preferences", "com.manytricks.Moom.plist"),
		filepath.Join(tokenDir, "Application Support", "Moom", "windows.dat"),
	} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Noise that must be ignored.
	if err := os.MkdirAll(filepath.Join(eng.HostRoot(), ".staging", "x.123"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(eng.HostRoot(), "dead.prev"), 0o755); err != nil {
		t.Fatal(err)
	}

	adopted := eng.AdoptArchivedApps()
	if len(adopted) != 1 || adopted[0] != "Moom Classic" {
		t.Fatalf("adopted = %v", adopted)
	}
	entry := eng.Manifest.App("Moom Classic")
	if entry == nil {
		t.Fatal("synthesized entry not appended to manifest")
		return
	}
	wantPaths := map[string]bool{
		filepath.Join("Preferences", "com.manytricks.Moom.plist"): true,
		filepath.Join("Application Support", "Moom"):              true,
	}
	for _, p := range entry.Paths {
		if !wantPaths[p.Path] {
			t.Errorf("unexpected synthesized path %q", p.Path)
		}
		delete(wantPaths, p.Path)
	}
	for p := range wantPaths {
		t.Errorf("missing synthesized path %q", p)
	}

	// Restore must now bring the archived settings back.
	sum, err := eng.Restore(context.Background(), RestoreOptions{Tokens: []string{"Moom Classic"}})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Failed != 0 {
		t.Fatalf("restore failed: %+v", sum)
	}
	live := filepath.Join(home, "Library", "Preferences", "com.manytricks.Moom.plist")
	if _, err := os.Stat(live); err != nil {
		t.Errorf("adopted app not restored: %v", err)
	}
}

func TestLastBackupStampRoundtrip(t *testing.T) {
	home := t.TempDir()
	eng := newRoundtripEngine(t, home, &Manifest{})
	if err := os.MkdirAll(eng.HostRoot(), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := eng.ReadLastBackupStamp(); err != nil || got != nil {
		t.Fatalf("absent stamp should be (nil, nil): %v %v", got, err)
	}
	stamp := BackupStamp{Tag: "onestop-x", Tokens: []string{"a", "b"}, Files: 3}
	if err := eng.WriteLastBackupStamp(stamp); err != nil {
		t.Fatal(err)
	}
	got, err := eng.ReadLastBackupStamp()
	if err != nil || got == nil {
		t.Fatal(err)
	}
	if got.Tag != "onestop-x" || got.Files != 3 || len(got.Tokens) != 2 {
		t.Errorf("stamp roundtrip wrong: %+v", got)
	}
}

func TestListHosts(t *testing.T) {
	root := t.TempDir()
	if hosts, err := ListHosts(root); err != nil || hosts != nil {
		t.Fatalf("missing tree should be (nil, nil): %v %v", hosts, err)
	}
	for _, h := range []string{"mac2", "mac1"} {
		if err := os.MkdirAll(filepath.Join(root, "app-settings", h), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	hosts, err := ListHosts(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 2 || hosts[0] != "mac1" || hosts[1] != "mac2" {
		t.Errorf("hosts = %v", hosts)
	}
}

func TestBackupRefusesUnsafeToken(t *testing.T) {
	home := t.TempDir()
	plistPath := filepath.Join(home, "Library", "Preferences", "com.x.plist")
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plistPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A traversal token that would escape HostRoot via filepath.Join and
	// feed os.RemoveAll/os.Rename if not guarded.
	mf := &Manifest{Apps: []AppEntry{{
		Token: "../escape",
		Paths: []PathEntry{{Type: "pref", Path: "Preferences/com.x.plist"}},
	}}}
	eng := newRoundtripEngine(t, home, mf)

	sum, err := eng.Backup(context.Background(), BackupOptions{Tokens: []string{"../escape"}})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Failed == 0 {
		t.Fatal("unsafe token should be counted as Failed, not silently processed")
	}
	// Nothing escaped the host tree: the sibling dir must not exist.
	if _, err := os.Stat(filepath.Join(eng.Root, "app-settings", "escape")); !os.IsNotExist(err) {
		t.Errorf("unsafe token escaped host root: %v", err)
	}
}

func TestSafeToken(t *testing.T) {
	for _, bad := range []string{"", ".", "..", "a/b", "../x", "x/../y"} {
		if safeToken(bad) {
			t.Errorf("safeToken(%q) = true, want false", bad)
		}
	}
	for _, ok := range []string{"raycast", "Moom Classic", "com.foo.bar", "1password"} {
		if !safeToken(ok) {
			t.Errorf("safeToken(%q) = false, want true", ok)
		}
	}
}

func TestDetectCloudCandidatePrefersDropbox(t *testing.T) {
	home := t.TempDir()
	cs := filepath.Join(home, "Library", "CloudStorage")
	// Both a Google Drive and a Dropbox cloud root, each with a secrets marker.
	if err := os.MkdirAll(filepath.Join(cs, "GoogleDrive-x@gmail.com", "My Drive", "secrets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cs, "Dropbox", "secrets"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := DetectCloudCandidate(home)
	want := filepath.Join(cs, "Dropbox", "secrets", "dotfiles-backup")
	if got != want {
		t.Errorf("DetectCloudCandidate = %q, want Dropbox %q", got, want)
	}
}

func TestDetectCloudCandidateFallsBackToDrive(t *testing.T) {
	home := t.TempDir()
	cs := filepath.Join(home, "Library", "CloudStorage")
	if err := os.MkdirAll(filepath.Join(cs, "GoogleDrive-x@gmail.com", "secrets"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := DetectCloudCandidate(home)
	want := filepath.Join(cs, "GoogleDrive-x@gmail.com", "secrets", "dotfiles-backup")
	if got != want {
		t.Errorf("DetectCloudCandidate = %q, want Drive %q", got, want)
	}
}

func TestDetectCloudCandidateHonorsDropboxSymlinkInHome(t *testing.T) {
	home := t.TempDir()
	real := filepath.Join(home, "Library", "CloudStorage", "Dropbox")
	if err := os.MkdirAll(filepath.Join(real, "secrets"), 0o755); err != nil {
		t.Fatal(err)
	}
	// ~/Dropbox symlink → the CloudStorage dir (as on a real machine).
	if err := os.Symlink(real, filepath.Join(home, "Dropbox")); err != nil {
		t.Fatal(err)
	}
	got := DetectCloudCandidate(home)
	// CloudStorage is scanned first → canonical path wins over the symlink.
	want := filepath.Join(real, "secrets", "dotfiles-backup")
	if got != want {
		t.Errorf("DetectCloudCandidate = %q, want canonical %q", got, want)
	}
}

func TestDetectCloudCandidateRequiresSecretsMarker(t *testing.T) {
	home := t.TempDir()
	// Dropbox dir present but no secrets/ subdir, and no Drive → "".
	if err := os.MkdirAll(filepath.Join(home, "Library", "CloudStorage", "Dropbox", "other"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := DetectCloudCandidate(home); got != "" {
		t.Errorf("DetectCloudCandidate = %q, want empty (no secrets marker)", got)
	}
}

func TestDetectCloudCandidateNoCloud(t *testing.T) {
	if got := DetectCloudCandidate(t.TempDir()); got != "" {
		t.Errorf("DetectCloudCandidate = %q, want empty", got)
	}
}
