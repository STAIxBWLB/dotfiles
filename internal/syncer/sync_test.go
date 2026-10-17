package syncer

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/config"
)

func newTestConfig(t *testing.T) *Config {
	t.Helper()
	dir := t.TempDir()
	paths := ResolveLocalPaths(dir)
	if err := EnsureLocalLayout(paths); err != nil {
		t.Fatalf("EnsureLocalLayout: %v", err)
	}
	includePatterns, err := LoadDefaultIncludePatterns()
	if err != nil {
		t.Fatalf("LoadDefaultIncludePatterns: %v", err)
	}
	return &Config{
		LocalPath:       "/tmp/test-local/",
		MirrorPath:      "/tmp/test-mirror/",
		Target:          Target{Kind: TargetLocal, Path: "/tmp/test-mirror/"},
		ConfigDir:       paths.StoreDir,
		FilterMode:      DefaultFilterMode(),
		IncludeFile:     paths.IncludeFile,
		IncludePatterns: includePatterns,
		ExcludesFile:    paths.ExcludeFile,
		LogFile:         "/tmp/test.log",
		LockDir:         "/tmp/test.lock",
		MaxDelete:       1000,
		Propagation:     DefaultPropagationPolicy(),
		LocalPaths:      paths,
	}
}

// newIsolatedState returns a UserState whose gsync LocalPath
// points at a fresh per-test directory. Use this for any test that
// calls ResolveConfig — without it the test would migrate against the
// real ~/workspace/work tree and pollute it (or pick up state from
// previous test runs).
func newIsolatedState(t *testing.T) *config.UserState {
	t.Helper()
	// Isolate HOME: ResolveConfig reads os.UserHomeDir() for the mirror
	// default, which now consults cloud detection. A fresh temp home has no
	// Dropbox/Drive, so the default stays the deterministic gdrive fallback
	// regardless of the contributor's real ~/Library/CloudStorage.
	t.Setenv("HOME", t.TempDir())
	state := &config.UserState{}
	state.Modules.Gsync.LocalPath = t.TempDir()
	return state
}

func TestPullArgs_WorkspaceAuthoritative(t *testing.T) {
	cfg := newTestConfig(t)
	conflict := &ConflictDir{Timestamp: "2026-05-01T12-00-00Z"}

	args := pullArgs(cfg, conflict, runtimeFilters{}, false)

	// Must include --update (workspace-authoritative).
	if !slices.Contains(args, "--update") {
		t.Errorf("pullArgs missing --update; got %v", args)
	}

	// Must NEVER include --delete or --delete-after (would destroy workspace-only adds).
	for _, forbidden := range []string{"--delete", "--delete-after", "--delete-excluded"} {
		if slices.Contains(args, forbidden) {
			t.Errorf("pullArgs leaked %q — workspace adds would be destroyed", forbidden)
		}
	}

	// Must include --backup --backup-dir pointing at from-gdrive subtree.
	if !slices.Contains(args, "--backup") {
		t.Errorf("pullArgs missing --backup; got %v", args)
	}
	wantBackup := "--backup-dir=.sync-conflicts/2026-05-01T12-00-00Z/from-gdrive"
	if !slices.Contains(args, wantBackup) {
		t.Errorf("pullArgs missing %q; got %v", wantBackup, args)
	}

	// Source = mirror, destination = local; both with trailing slashes.
	if args[len(args)-2] != cfg.MirrorPath || args[len(args)-1] != cfg.LocalPath {
		t.Errorf("pullArgs source/dest order wrong: ...%v %v (want mirror→local)",
			args[len(args)-2], args[len(args)-1])
	}
}

func TestPullArgs_DryRunPlumbing(t *testing.T) {
	cfg := newTestConfig(t)
	conflict := &ConflictDir{Timestamp: "ts"}

	noDry := pullArgs(cfg, conflict, runtimeFilters{}, false)
	if slices.Contains(noDry, "--dry-run") {
		t.Error("pullArgs(dryRun=false) leaked --dry-run")
	}

	dry := pullArgs(cfg, conflict, runtimeFilters{}, true)
	if !slices.Contains(dry, "--dry-run") {
		t.Errorf("pullArgs(dryRun=true) missing --dry-run; got %v", dry)
	}
}

func TestPushArgs_DefaultPropagation_NoDelete(t *testing.T) {
	cfg := newTestConfig(t)
	conflict := &ConflictDir{Timestamp: "2026-05-01T12-00-00Z"}

	args := pushArgs(cfg, conflict, runtimeFilters{}, false)

	// Default policy {create:true, update:true, delete:false} relies on
	// rsync's natural copy-new-and-modified behavior. NO delete flags.
	for _, forbidden := range []string{"--delete", "--delete-after", "--delete-before", "--delete-during"} {
		if slices.Contains(args, forbidden) {
			t.Errorf("default-policy pushArgs leaked %q — delete must be opt-in", forbidden)
		}
	}
	for _, a := range args {
		if strings.HasPrefix(a, "--max-delete") {
			t.Errorf("default-policy pushArgs leaked %q — max-delete only meaningful with delete on", a)
		}
	}

	// Default also doesn't toggle the create/update scope flags.
	for _, forbidden := range []string{"--existing", "--ignore-existing"} {
		if slices.Contains(args, forbidden) {
			t.Errorf("default-policy pushArgs leaked %q — both create and update should be on", forbidden)
		}
	}

	// from-workspace backup subdir on the mirror side is always-on.
	wantBackup := "--backup-dir=.sync-conflicts/2026-05-01T12-00-00Z/from-workspace"
	if !slices.Contains(args, wantBackup) {
		t.Errorf("pushArgs missing %q; got %v", wantBackup, args)
	}

	// --update belongs on pull, never push.
	if slices.Contains(args, "--update") {
		t.Error("pushArgs leaked --update — push must overwrite based on mtime+size, not skip-newer")
	}

	// Source = local, destination = mirror.
	if args[len(args)-2] != cfg.LocalPath || args[len(args)-1] != cfg.MirrorPath {
		t.Errorf("pushArgs source/dest order wrong: ...%v %v (want local→mirror)",
			args[len(args)-2], args[len(args)-1])
	}
}

func TestPushArgs_AllTogglesOn_HasDeleteAfterAndMaxDelete(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.MaxDelete = 250
	cfg.Propagation = PropagationPolicy{Create: true, Update: true, Delete: true}
	conflict := &ConflictDir{Timestamp: "ts"}

	args := pushArgs(cfg, conflict, runtimeFilters{}, false)

	if !slices.Contains(args, "--delete-after") {
		t.Errorf("all-on pushArgs missing --delete-after; got %v", args)
	}
	wantMax := "--max-delete=" + strconv.Itoa(cfg.MaxDelete)
	if !slices.Contains(args, wantMax) {
		t.Errorf("all-on pushArgs missing %q; got %v", wantMax, args)
	}
	for _, forbidden := range []string{"--existing", "--ignore-existing"} {
		if slices.Contains(args, forbidden) {
			t.Errorf("all-on pushArgs leaked %q — both create+update are on", forbidden)
		}
	}
}

func TestPushArgs_CreatesOnly_HasIgnoreExisting(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Propagation = PropagationPolicy{Create: true, Update: false, Delete: false}
	conflict := &ConflictDir{Timestamp: "ts"}

	args := pushArgs(cfg, conflict, runtimeFilters{}, false)

	if !slices.Contains(args, "--ignore-existing") {
		t.Errorf("creates-only pushArgs missing --ignore-existing; got %v", args)
	}
	if slices.Contains(args, "--existing") {
		t.Error("creates-only pushArgs leaked --existing — would skip new files entirely")
	}
	if slices.Contains(args, "--delete-after") {
		t.Error("creates-only pushArgs leaked --delete-after — delete is off")
	}
}

func TestPushArgs_UpdateOnly_HasExisting(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Propagation = PropagationPolicy{Create: false, Update: true, Delete: false}
	conflict := &ConflictDir{Timestamp: "ts"}

	args := pushArgs(cfg, conflict, runtimeFilters{}, false)

	if !slices.Contains(args, "--existing") {
		t.Errorf("update-only pushArgs missing --existing; got %v", args)
	}
	if slices.Contains(args, "--ignore-existing") {
		t.Error("update-only pushArgs leaked --ignore-existing — would skip everything")
	}
	if slices.Contains(args, "--delete-after") {
		t.Error("update-only pushArgs leaked --delete-after — delete is off")
	}
}

func TestPushArgs_DeleteOnly_HasExistingIgnoreExistingDelete(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Propagation = PropagationPolicy{Create: false, Update: false, Delete: true}
	conflict := &ConflictDir{Timestamp: "ts"}

	args := pushArgs(cfg, conflict, runtimeFilters{}, false)

	for _, want := range []string{"--existing", "--ignore-existing", "--delete-after"} {
		if !slices.Contains(args, want) {
			t.Errorf("delete-only pushArgs missing %q; got %v", want, args)
		}
	}
}

func TestPushArgs_AlwaysExcludesInboxGdriveAndDotfiles(t *testing.T) {
	conflict := &ConflictDir{Timestamp: "ts"}
	for name, policy := range map[string]PropagationPolicy{
		"default":     DefaultPropagationPolicy(),
		"all-on":      {Create: true, Update: true, Delete: true},
		"creates":     {Create: true, Update: false, Delete: false},
		"updates":     {Create: false, Update: true, Delete: false},
		"delete-only": {Create: false, Update: false, Delete: true},
	} {
		cfg := newTestConfig(t)
		cfg.Propagation = policy

		args := pushArgs(cfg, conflict, runtimeFilters{}, false)

		for _, want := range []string{"--exclude=/.dotfiles/", "--exclude=/inbox/gdrive/"} {
			if !slices.Contains(args, want) {
				t.Errorf("[%s] pushArgs missing always-on exclude %q; got %v", name, want, args)
			}
		}
	}
}

func TestPushArgs_PrunesEmptyDirs(t *testing.T) {
	conflict := &ConflictDir{Timestamp: "ts"}
	for name, policy := range map[string]PropagationPolicy{
		"default":     DefaultPropagationPolicy(),
		"all-on":      {Create: true, Update: true, Delete: true},
		"creates":     {Create: true, Update: false, Delete: false},
		"updates":     {Create: false, Update: true, Delete: false},
		"delete-only": {Create: false, Update: false, Delete: true},
	} {
		cfg := newTestConfig(t)
		cfg.Propagation = policy

		args := pushArgs(cfg, conflict, runtimeFilters{}, false)

		if !slices.Contains(args, "--prune-empty-dirs") {
			t.Errorf("[%s] pushArgs missing --prune-empty-dirs; got %v", name, args)
		}
	}
}

func TestPullArgs_DoesNotPruneEmptyDirs(t *testing.T) {
	cfg := newTestConfig(t)
	conflict := &ConflictDir{Timestamp: "ts"}

	args := pullArgs(cfg, conflict, runtimeFilters{}, false)

	if slices.Contains(args, "--prune-empty-dirs") {
		t.Errorf("pullArgs leaked --prune-empty-dirs; user only opted into push-side pruning. got %v", args)
	}
}

func TestPushArgs_NoMaxDeleteWhenDeleteOff(t *testing.T) {
	conflict := &ConflictDir{Timestamp: "ts"}
	for name, policy := range map[string]PropagationPolicy{
		"default": DefaultPropagationPolicy(),
		"creates": {Create: true, Update: false, Delete: false},
		"updates": {Create: false, Update: true, Delete: false},
	} {
		cfg := newTestConfig(t)
		cfg.MaxDelete = 250
		cfg.Propagation = policy

		args := pushArgs(cfg, conflict, runtimeFilters{}, false)

		for _, a := range args {
			if strings.HasPrefix(a, "--max-delete") {
				t.Errorf("[%s] pushArgs leaked %q with delete off", name, a)
			}
		}
	}
}

func TestPush_RefusesEmptyPropagation(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.Propagation = PropagationPolicy{} // all false — invalid

	err := Push(context.Background(), nil, cfg, true)
	if err == nil {
		t.Fatal("Push with all-false policy must error before any rsync work")
	}
	if !strings.Contains(err.Error(), "propagation") {
		t.Errorf("Push refusal error must mention propagation; got %v", err)
	}
}

func TestResolveConfig_Defaults(t *testing.T) {
	state := newIsolatedState(t)
	cfg, err := ResolveConfig(state)
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	// LocalPath comes from the isolated tempdir, but ResolveConfig
	// must have suffix-normalized it to a trailing slash.
	if !strings.HasSuffix(cfg.LocalPath, "/") {
		t.Errorf("LocalPath missing trailing slash: %q", cfg.LocalPath)
	}
	if !strings.HasSuffix(cfg.MirrorPath, "gdrive-workspace/work/") {
		t.Errorf("MirrorPath default wrong: %s", cfg.MirrorPath)
	}
	if cfg.MaxDelete != defaultMaxDelete {
		t.Errorf("MaxDelete default = %d, want %d", cfg.MaxDelete, defaultMaxDelete)
	}
	if cfg.ExcludesFile == "" {
		t.Error("ExcludesFile not resolved")
	}
	// Default propagation policy is the safe baseline.
	want := DefaultPropagationPolicy()
	if cfg.Propagation != want {
		t.Errorf("Propagation = %+v, want %+v", cfg.Propagation, want)
	}
	if cfg.FilterMode != FilterModeInclude {
		t.Errorf("FilterMode = %q, want include", cfg.FilterMode)
	}
	if cfg.IncludeFile == "" || len(cfg.IncludePatterns) == 0 {
		t.Errorf("include defaults not resolved: file=%q patterns=%v", cfg.IncludeFile, cfg.IncludePatterns)
	}
	// LocalPaths is always populated so callers can drill into the .dotfiles store.
	if cfg.LocalPaths == nil || cfg.LocalPaths.StoreDir == "" {
		t.Errorf("LocalPaths not resolved: %+v", cfg.LocalPaths)
	}
	if cfg.LogFile != cfg.LocalPaths.LogFile {
		t.Errorf("LogFile = %q, want local store log %q", cfg.LogFile, cfg.LocalPaths.LogFile)
	}
}

func TestResolveConfig_StateOverrides(t *testing.T) {
	tmp := t.TempDir()
	state := &config.UserState{}
	state.Modules.Gsync.LocalPath = tmp
	state.Modules.Gsync.MirrorPath = "/custom/mirror"
	state.Modules.Gsync.MaxDelete = 50

	cfg, err := ResolveConfig(state)
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	if cfg.LocalPath != tmp+"/" {
		t.Errorf("LocalPath = %q, want %q", cfg.LocalPath, tmp+"/")
	}
	if cfg.MirrorPath != "/custom/mirror/" {
		t.Errorf("MirrorPath = %q, want trailing-slashed /custom/mirror/", cfg.MirrorPath)
	}
	if cfg.MaxDelete != 50 {
		t.Errorf("MaxDelete = %d, want 50", cfg.MaxDelete)
	}
}

func TestResolveConfigReadOnly_DoesNotCreateLocalStore(t *testing.T) {
	state := newIsolatedState(t)
	local := strings.TrimRight(state.Modules.Gsync.LocalPath, "/")

	cfg, err := ResolveConfigReadOnly(state)
	if err != nil {
		t.Fatalf("ResolveConfigReadOnly: %v", err)
	}
	if cfg.LocalPaths == nil {
		t.Fatal("LocalPaths not populated")
	}
	if cfg.LocalPaths.WorkspaceRoot != local {
		t.Errorf("WorkspaceRoot = %q, want %q", cfg.LocalPaths.WorkspaceRoot, local)
	}
	if _, err := os.Stat(cfg.LocalPaths.StoreDir); !os.IsNotExist(err) {
		t.Fatalf("read-only resolve created local store or got unexpected error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(local, ".gitignore")); !os.IsNotExist(err) {
		t.Fatalf("read-only resolve created .gitignore or got unexpected error: %v", err)
	}
}

func TestResolveConfig_PrefersCloudMirror(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// A Dropbox cloud root with a secrets marker → mirror default follows it.
	if err := os.MkdirAll(filepath.Join(home, "Library", "CloudStorage", "Dropbox", "secrets"), 0o755); err != nil {
		t.Fatal(err)
	}
	state := &config.UserState{}
	state.Modules.Gsync.LocalPath = t.TempDir()

	cfg, err := ResolveConfig(state)
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	want := filepath.Join(home, "Library", "CloudStorage", "Dropbox", "work") + "/"
	if cfg.MirrorPath != want {
		t.Errorf("MirrorPath = %q, want cloud mirror %q", cfg.MirrorPath, want)
	}
}

func TestResolveConfig_LocalMirrorWinsOverCloud(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "Library", "CloudStorage", "Dropbox", "secrets"), 0o755); err != nil {
		t.Fatal(err)
	}
	state := &config.UserState{}
	state.Modules.Gsync.LocalPath = t.TempDir()
	state.Modules.Gsync.MirrorPath = "/explicit/mirror"

	cfg, err := ResolveConfig(state)
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	if cfg.MirrorPath != "/explicit/mirror/" {
		t.Errorf("MirrorPath = %q, want explicit /explicit/mirror/ (cloud must not override)", cfg.MirrorPath)
	}
}

func TestResolveConfigReadOnlyForHome_UsesGivenHome(t *testing.T) {
	// os.UserHomeDir() would resolve here — the --home override must not use it.
	currentHome := t.TempDir()
	t.Setenv("HOME", currentHome)

	overrideHome := t.TempDir()
	state := &config.UserState{}
	state.Modules.Gsync.LocalPath = t.TempDir()
	// MirrorPath empty → default is <home>/gdrive-workspace/work.

	cfg, err := ResolveConfigReadOnlyForHomeProfile(state, overrideHome, DefaultProfile)
	if err != nil {
		t.Fatalf("ResolveConfigReadOnlyForHomeProfile: %v", err)
	}
	want := filepath.Join(overrideHome, "gdrive-workspace", "work") + "/"
	if cfg.MirrorPath != want {
		t.Errorf("MirrorPath = %q, want override-home default %q", cfg.MirrorPath, want)
	}
	if strings.HasPrefix(cfg.MirrorPath, currentHome) {
		t.Errorf("MirrorPath must not resolve under the invoking user's home %q: got %q", currentHome, cfg.MirrorPath)
	}
}

func TestResolveConfigReadOnlyForHome_ExpandsTildeAgainstHome(t *testing.T) {
	currentHome := t.TempDir()
	t.Setenv("HOME", currentHome)
	overrideHome := t.TempDir()

	state := &config.UserState{}
	state.Modules.Gsync.LocalPath = t.TempDir()
	state.Modules.Gsync.MirrorPath = "~/cloud/work"

	cfg, err := ResolveConfigReadOnlyForHomeProfile(state, overrideHome, DefaultProfile)
	if err != nil {
		t.Fatalf("ResolveConfigReadOnlyForHomeProfile: %v", err)
	}
	want := filepath.Join(overrideHome, "cloud", "work") + "/"
	if cfg.MirrorPath != want {
		t.Errorf("MirrorPath = %q, want %q (~ expanded against override home)", cfg.MirrorPath, want)
	}
}
