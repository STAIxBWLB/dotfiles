package syncer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/STAIxBWLB/dotfiles/internal/config"
)

func TestPropagationPolicy_Validate(t *testing.T) {
	cases := []struct {
		name    string
		policy  PropagationPolicy
		wantErr bool
	}{
		{"default safe", DefaultPropagationPolicy(), false},
		{"create only", PropagationPolicy{Create: true}, false},
		{"update only", PropagationPolicy{Update: true}, false},
		{"delete only", PropagationPolicy{Delete: true}, false},
		{"all on", PropagationPolicy{Create: true, Update: true, Delete: true}, false},
		{"all off", PropagationPolicy{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.policy.Validate()
			if (err != nil) != tc.wantErr {
				t.Errorf("Validate() err=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestPropagationPolicy_String(t *testing.T) {
	cases := []struct {
		policy PropagationPolicy
		want   string
	}{
		{DefaultPropagationPolicy(), "create+update (delete off)"},
		{PropagationPolicy{Create: true, Update: true, Delete: true}, "create+update+delete"},
		{PropagationPolicy{Delete: true}, "delete (create,update off)"},
		{PropagationPolicy{}, "(none — invalid)"},
	}
	for _, tc := range cases {
		if got := tc.policy.String(); got != tc.want {
			t.Errorf("String(%+v) = %q, want %q", tc.policy, got, tc.want)
		}
	}
}

func TestResolveLocalPaths_LayoutShape(t *testing.T) {
	root := "/tmp/wk"
	paths := ResolveLocalPaths(root + "/")

	wantStore := filepath.Join(root, ".dotfiles", "sync")
	if paths.StoreDir != wantStore {
		t.Errorf("StoreDir = %q, want %q", paths.StoreDir, wantStore)
	}
	if paths.WorkspaceRoot != root {
		t.Errorf("WorkspaceRoot = %q, want %q", paths.WorkspaceRoot, root)
	}
	for _, want := range []string{
		filepath.Join(wantStore, "config.yaml"),
		filepath.Join(wantStore, "include.txt"),
		filepath.Join(wantStore, "exclude.txt"),
		filepath.Join(wantStore, "ignore.txt"),
		filepath.Join(wantStore, "shared-excludes.dyn.conf"),
		filepath.Join(wantStore, "baseline.manifest"),
		filepath.Join(wantStore, "imports.manifest"),
		filepath.Join(wantStore, "tombstones.log"),
		filepath.Join(wantStore, "log", "sync.log"),
	} {
		seen := false
		for _, got := range []string{
			paths.ConfigFile, paths.IncludeFile, paths.ExcludeFile, paths.IgnoreFile,
			paths.SharedDynFile, paths.BaselineFile, paths.ImportsFile,
			paths.TombstonesFile, paths.LogFile,
		} {
			if got == want {
				seen = true
				break
			}
		}
		if !seen {
			t.Errorf("missing path %q in resolved layout: %+v", want, paths)
		}
	}
}

func TestEnsureLocalLayout_CreatesAllDefaults(t *testing.T) {
	tmp := t.TempDir()
	paths := ResolveLocalPaths(tmp)
	if err := EnsureLocalLayout(paths); err != nil {
		t.Fatalf("EnsureLocalLayout: %v", err)
	}
	for _, p := range []string{paths.StoreDir, paths.LogDir} {
		if info, err := os.Stat(p); err != nil || !info.IsDir() {
			t.Errorf("dir not created: %s (err %v)", p, err)
		}
	}
	for _, p := range []string{paths.IncludeFile, paths.ExcludeFile, paths.IgnoreFile, paths.BaselineFile, paths.ImportsFile, paths.TombstonesFile} {
		body, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("missing file: %s (err %v)", p, err)
			continue
		}
		if len(body) == 0 {
			t.Errorf("file %s should have a header, got empty", p)
		}
	}
	// Workspace .gitignore must allow the Git-shared baseline while ignoring
	// machine-local gsync state.
	body, err := os.ReadFile(paths.WorkspaceIgnore)
	if err != nil {
		t.Fatalf("reading workspace .gitignore: %v", err)
	}
	for _, want := range gitignoreEntries {
		if !strings.Contains(string(body), want) {
			t.Errorf(".gitignore missing %q\n--- got ---\n%s", want, body)
		}
	}
}

func TestEnsureLocalLayout_Idempotent(t *testing.T) {
	tmp := t.TempDir()
	paths := ResolveLocalPaths(tmp)
	if err := EnsureLocalLayout(paths); err != nil {
		t.Fatalf("first EnsureLocalLayout: %v", err)
	}
	// Mutate the exclude file; a second call must not overwrite.
	custom := []byte("# operator-edited\n")
	if err := os.WriteFile(paths.ExcludeFile, custom, 0644); err != nil {
		t.Fatalf("seed custom: %v", err)
	}
	if err := EnsureLocalLayout(paths); err != nil {
		t.Fatalf("second EnsureLocalLayout: %v", err)
	}
	got, err := os.ReadFile(paths.ExcludeFile)
	if err != nil {
		t.Fatalf("read after second call: %v", err)
	}
	if string(got) != string(custom) {
		t.Errorf("exclude file rewritten on second EnsureLocalLayout; want untouched %q got %q", custom, got)
	}
}

func TestAppendGitignoreBlock_Idempotent(t *testing.T) {
	tmp := t.TempDir()
	gi := filepath.Join(tmp, ".gitignore")

	if err := appendGitignoreBlock(gi, gitignoreEntries); err != nil {
		t.Fatalf("first append: %v", err)
	}
	if err := appendGitignoreBlock(gi, gitignoreEntries); err != nil {
		t.Fatalf("second append: %v", err)
	}

	body, err := os.ReadFile(gi)
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	lines := map[string]int{}
	for _, line := range strings.Split(string(body), "\n") {
		lines[strings.TrimSpace(line)]++
	}
	for _, want := range gitignoreEntries {
		if n := lines[want]; n != 1 {
			t.Errorf("%q count = %d, want 1\n%s", want, n, body)
		}
	}
}

func TestAppendGitignoreBlock_PreservesExistingContent(t *testing.T) {
	tmp := t.TempDir()
	gi := filepath.Join(tmp, ".gitignore")
	existing := "# project ignores\n.env\n*.log\n"
	if err := os.WriteFile(gi, []byte(existing), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := appendGitignoreBlock(gi, gitignoreEntries); err != nil {
		t.Fatalf("append: %v", err)
	}

	got, err := os.ReadFile(gi)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(got), ".env") || !strings.Contains(string(got), "*.log") {
		t.Errorf("existing entries lost\n%s", got)
	}
	for _, want := range gitignoreEntries {
		if !strings.Contains(string(got), want) {
			t.Errorf("new entry %q missing\n%s", want, got)
		}
	}
}

func TestLocalConfig_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	paths := ResolveLocalPaths(tmp)

	original := &LocalConfig{
		MirrorPath:     "/x/mirror",
		FilterMode:     FilterModeExclude,
		Propagation:    PropagationPolicy{Create: true, Update: true, Delete: true},
		MaxDelete:      500,
		Interval:       600,
		PullInterval:   900,
		PushMode:       ModeClean,
		PullMode:       ModeForce,
		Paused:         true,
		SharedExcludes: []string{"projects/foo", "projects/bar"},
	}
	if err := SaveLocalConfig(paths, original); err != nil {
		t.Fatalf("SaveLocalConfig: %v", err)
	}

	got, ok, err := LoadLocalConfig(paths)
	if err != nil {
		t.Fatalf("LoadLocalConfig: %v", err)
	}
	if !ok {
		t.Fatal("LoadLocalConfig reported missing after save")
	}
	if got.MirrorPath != original.MirrorPath ||
		got.FilterMode != original.FilterMode ||
		got.Propagation != original.Propagation ||
		got.MaxDelete != original.MaxDelete ||
		got.Interval != original.Interval ||
		got.PullInterval != original.PullInterval ||
		got.PushMode != original.PushMode ||
		got.PullMode != original.PullMode ||
		got.Paused != original.Paused ||
		len(got.SharedExcludes) != len(original.SharedExcludes) {
		t.Errorf("round-trip mismatch:\n  got %+v\n  want %+v", got, original)
	}
}

func TestLocalConfig_RefusesEmptyPropagationOnSave(t *testing.T) {
	tmp := t.TempDir()
	paths := ResolveLocalPaths(tmp)

	cfg := &LocalConfig{Propagation: PropagationPolicy{}} // all-false
	if err := SaveLocalConfig(paths, cfg); err == nil {
		t.Error("SaveLocalConfig should reject all-false propagation")
	}
}

func TestLoadLocalConfig_InvalidScheduleModesFallBackToClean(t *testing.T) {
	tmp := t.TempDir()
	paths := ResolveLocalPaths(tmp)
	if err := os.MkdirAll(paths.StoreDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte(`propagation:
    create: true
    update: true
    delete: false
push_mode: manual
pull_mode: bogus
`)
	if err := os.WriteFile(paths.ConfigFile, body, 0o644); err != nil {
		t.Fatal(err)
	}

	got, ok, err := LoadLocalConfig(paths)
	if err != nil {
		t.Fatalf("LoadLocalConfig: %v", err)
	}
	if !ok {
		t.Fatal("LoadLocalConfig reported missing")
	}
	if got.PushMode != ModeClean || got.PullMode != ModeClean {
		t.Fatalf("invalid modes should fall back to clean; got push=%q pull=%q", got.PushMode, got.PullMode)
	}
}

func TestLoadLocalConfig_InvalidFilterModeFallsBackToInclude(t *testing.T) {
	tmp := t.TempDir()
	paths := ResolveLocalPaths(tmp)
	if err := os.MkdirAll(paths.StoreDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte(`filter_mode: legacy
propagation:
    create: true
    update: true
    delete: false
`)
	if err := os.WriteFile(paths.ConfigFile, body, 0o644); err != nil {
		t.Fatal(err)
	}

	got, ok, err := LoadLocalConfig(paths)
	if err != nil {
		t.Fatalf("LoadLocalConfig: %v", err)
	}
	if !ok {
		t.Fatal("LoadLocalConfig reported missing")
	}
	if got.FilterMode != FilterModeInclude {
		t.Fatalf("invalid filter_mode should fall back to include; got %q", got.FilterMode)
	}
}

func TestSaveLocalConfig_RejectsInvalidFilterMode(t *testing.T) {
	tmp := t.TempDir()
	paths := ResolveLocalPaths(tmp)

	cfg := &LocalConfig{FilterMode: FilterMode("bogus"), Propagation: DefaultPropagationPolicy()}
	if err := SaveLocalConfig(paths, cfg); err == nil {
		t.Fatal("SaveLocalConfig should reject invalid filter_mode")
	}
}

func TestLoadLocalConfig_InvalidScheduleIntervalsAreClamped(t *testing.T) {
	tmp := t.TempDir()
	paths := ResolveLocalPaths(tmp)
	if err := os.MkdirAll(paths.StoreDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte(`propagation:
    create: true
    update: true
    delete: false
interval: 30
pull_interval: 999999
`)
	if err := os.WriteFile(paths.ConfigFile, body, 0o644); err != nil {
		t.Fatal(err)
	}

	got, ok, err := LoadLocalConfig(paths)
	if err != nil {
		t.Fatalf("LoadLocalConfig: %v", err)
	}
	if !ok {
		t.Fatal("LoadLocalConfig reported missing")
	}
	if got.Interval != ScheduleIntervalMin || got.PullInterval != ScheduleIntervalMax {
		t.Fatalf("invalid intervals should clamp to supported range; got push=%d pull=%d", got.Interval, got.PullInterval)
	}
}

func TestSaveLocalConfig_RejectsInvalidScheduleConfigWithoutWriting(t *testing.T) {
	tmp := t.TempDir()

	cases := []struct {
		name string
		cfg  *LocalConfig
	}{
		{
			name: "manual push mode",
			cfg: &LocalConfig{
				Propagation: DefaultPropagationPolicy(),
				PushMode:    ModeManual,
				PullMode:    ModeClean,
			},
		},
		{
			name: "unknown pull mode",
			cfg: &LocalConfig{
				Propagation: DefaultPropagationPolicy(),
				PushMode:    ModeClean,
				PullMode:    RunMode("bogus"),
			},
		},
		{
			name: "invalid push interval",
			cfg: &LocalConfig{
				Propagation: DefaultPropagationPolicy(),
				Interval:    30,
				PushMode:    ModeClean,
				PullMode:    ModeClean,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			casePaths := ResolveLocalPaths(filepath.Join(tmp, strings.ReplaceAll(tc.name, " ", "-")))
			if err := SaveLocalConfig(casePaths, tc.cfg); err == nil {
				t.Fatal("SaveLocalConfig should reject invalid schedule config")
			}
			if _, err := os.Stat(casePaths.ConfigFile); !os.IsNotExist(err) {
				t.Fatalf("invalid config should not be written; stat err=%v", err)
			}
		})
	}
}

func TestLoadLocalConfig_MissingReturnsFalse(t *testing.T) {
	tmp := t.TempDir()
	paths := ResolveLocalPaths(tmp)

	got, ok, err := LoadLocalConfig(paths)
	if err != nil {
		t.Fatalf("LoadLocalConfig: %v", err)
	}
	if ok {
		t.Errorf("LoadLocalConfig returned ok=true on missing file: %+v", got)
	}
}

func TestMigrateFromGlobal_PopulatesLocalLayout(t *testing.T) {
	tmp := t.TempDir()
	paths := ResolveLocalPaths(tmp)

	state := &config.UserState{}
	state.Modules.Gsync.MirrorPath = "/legacy/mirror"
	state.Modules.Gsync.MaxDelete = 250
	state.Modules.Gsync.Interval = 600
	state.Modules.Gsync.Paused = true
	state.Modules.Gsync.SharedExcludes = []string{"projects/legacy"}

	cfg, err := MigrateFromGlobal(state, paths)
	if err != nil {
		t.Fatalf("MigrateFromGlobal: %v", err)
	}
	// Migrated fields:
	if cfg.MirrorPath != "/legacy/mirror" || cfg.MaxDelete != 250 || cfg.Interval != 0 || !cfg.Paused {
		t.Errorf("migrated fields wrong: %+v", cfg)
	}
	if cfg.FilterMode != FilterModeInclude {
		t.Errorf("migrated FilterMode = %q, want include", cfg.FilterMode)
	}
	if cfg.PushMode != ModeClean || cfg.PullMode != ModeClean {
		t.Errorf("migrated modes wrong: %+v", cfg)
	}
	if len(cfg.SharedExcludes) != 1 || cfg.SharedExcludes[0] != "projects/legacy" {
		t.Errorf("SharedExcludes not migrated: %+v", cfg.SharedExcludes)
	}
	// Defaults filled in:
	want := DefaultPropagationPolicy()
	if cfg.Propagation != want {
		t.Errorf("Propagation default = %+v, want %+v", cfg.Propagation, want)
	}
	// On-disk artifacts present:
	for _, p := range []string{paths.ConfigFile, paths.IncludeFile, paths.ExcludeFile, paths.IgnoreFile, paths.BaselineFile, paths.ImportsFile, paths.TombstonesFile} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("missing post-migration file %s: %v", p, err)
		}
	}
}

func TestLoadOrMigrateLocalConfig_MigrationRunsOnce(t *testing.T) {
	tmp := t.TempDir()
	paths := ResolveLocalPaths(tmp)

	state := &config.UserState{}
	state.Modules.Gsync.MirrorPath = "/legacy/mirror"

	first, err := LoadOrMigrateLocalConfig(state, paths)
	if err != nil {
		t.Fatalf("first LoadOrMigrateLocalConfig: %v", err)
	}
	// Mutate the disk-resident config so we can detect re-migration.
	first.MirrorPath = "/edited/by/operator"
	if err := SaveLocalConfig(paths, first); err != nil {
		t.Fatalf("SaveLocalConfig: %v", err)
	}

	// Second call must see the operator's edit, NOT the global state.
	second, err := LoadOrMigrateLocalConfig(state, paths)
	if err != nil {
		t.Fatalf("second LoadOrMigrateLocalConfig: %v", err)
	}
	if second.MirrorPath != "/edited/by/operator" {
		t.Errorf("re-migration overwrote local edits: MirrorPath = %q", second.MirrorPath)
	}
}

func TestLocalState_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	paths := ResolveLocalPaths(tmp)

	st := &LocalState{LastIntakeTSDir: "2026-05-02T10-00-00Z"}
	if err := SaveLocalState(paths, st); err != nil {
		t.Fatalf("SaveLocalState: %v", err)
	}
	got, err := LoadLocalState(paths)
	if err != nil {
		t.Fatalf("LoadLocalState: %v", err)
	}
	if got.LastIntakeTSDir != "2026-05-02T10-00-00Z" {
		t.Errorf("LastIntakeTSDir round-trip: got %q want %q", got.LastIntakeTSDir, "2026-05-02T10-00-00Z")
	}
}

func TestLoadLocalState_MissingReturnsZero(t *testing.T) {
	tmp := t.TempDir()
	paths := ResolveLocalPaths(tmp)

	got, err := LoadLocalState(paths)
	if err != nil {
		t.Fatalf("LoadLocalState: %v", err)
	}
	if got == nil || !got.LastPull.IsZero() || !got.LastPush.IsZero() || !got.LastIntake.IsZero() {
		t.Errorf("expected zero state, got %+v", got)
	}
}

// #196: a key this dot does not know survives its save, value intact, while
// a key it owns follows the struct (cleared stays cleared).
func TestSaveLocalConfigKeepsUnknownKeys(t *testing.T) {
	paths := ResolveLocalPathsForProfile(t.TempDir(), PeerProfile)
	if err := os.MkdirAll(paths.StoreDir, 0o755); err != nil {
		t.Fatal(err)
	}
	on := "target: ssh:peer:/w\nowner: a\nowner_aliases:\n    - old-a\nfuture_key:\n    nested: [1, 2]\n    text: 'keep me'\npropagation:\n    create: true\n    update: true\n    delete: true\n"
	if err := os.WriteFile(paths.ConfigFile, []byte(on), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := LoadLocalConfig(paths)
	if err != nil {
		t.Fatal(err)
	}
	cfg.OwnerAliases = nil
	if err := SaveLocalConfig(paths, cfg); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(paths.ConfigFile)
	if !strings.Contains(string(body), "future_key:\n    nested: [1, 2]\n    text: 'keep me'\n") {
		t.Errorf("the unknown key changed or went:\n%s", body)
	}
	if strings.Contains(string(body), "owner_aliases") {
		t.Errorf("a cleared key came back:\n%s", body)
	}
	if again, _, err := LoadLocalConfig(paths); err != nil || again.Owner != "a" {
		t.Fatalf("reload: %+v %v", again, err)
	}
}

// A carried value that aliases an anchor on a key LocalConfig rewrites is
// copied with the alias resolved, so the file still loads (#200 review).
func TestSaveLocalConfigResolvesAliasesInKeptKeys(t *testing.T) {
	paths := ResolveLocalPathsForProfile(t.TempDir(), PeerProfile)
	if err := os.MkdirAll(paths.StoreDir, 0o755); err != nil {
		t.Fatal(err)
	}
	on := "propagation: &p {create: true, update: true, delete: true}\nfuture: *p\n"
	if err := os.WriteFile(paths.ConfigFile, []byte(on), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := LoadLocalConfig(paths)
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveLocalConfig(paths, cfg); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadLocalConfig(paths); err != nil {
		body, _ := os.ReadFile(paths.ConfigFile)
		t.Fatalf("the saved file does not load: %v\n%s", err, body)
	}
	body, _ := os.ReadFile(paths.ConfigFile)
	if !strings.Contains(string(body), "future:") || !strings.Contains(string(body), "create: true") {
		t.Fatalf("the kept key lost its value:\n%s", body)
	}
}

// A key the decoder reads as a known one is not carried: a top-level merge
// key (what it set was loaded into the struct) or an alias used as a key.
// So a key the operator cleared stays cleared, and a known key is never
// written twice (#200 review).
func TestSaveLocalConfigDropsKeysTheDecoderReadsAsKnown(t *testing.T) {
	for name, on := range map[string]string{
		"merge key":      "x-base: &b {owner_aliases: [old]}\nowner: a\n<<: *b\n",
		"alias key":      "x: &k owner_aliases\nowner: a\n*k : [old]\n",
		"alias of owner": "x: &k owner\n*k : a\n",
	} {
		t.Run(name, func(t *testing.T) {
			paths := ResolveLocalPathsForProfile(t.TempDir(), PeerProfile)
			if err := os.MkdirAll(paths.StoreDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(paths.ConfigFile, []byte(on), 0o644); err != nil {
				t.Fatal(err)
			}
			cfg, _, err := LoadLocalConfig(paths)
			if err != nil {
				t.Fatal(err)
			}
			cfg.OwnerAliases = nil
			if err := SaveLocalConfig(paths, cfg); err != nil {
				t.Fatal(err)
			}
			again, _, err := LoadLocalConfig(paths)
			if body, _ := os.ReadFile(paths.ConfigFile); err != nil || again.Owner != "a" || len(again.OwnerAliases) != 0 || !strings.Contains(string(body), "\nx") {
				t.Fatalf("reload = %+v, %v, want owner a, no aliases, x kept:\n%s", again, err, body)
			}
		})
	}
}

// Kept values were never decoded, so nothing guarded their aliases: one
// inside what it names, or a billion-laughs expansion, refuses the save
// instead of crashing it, and the file stays as it was (#200 review).
func TestSaveLocalConfigRefusesRunawayAliases(t *testing.T) {
	laughs := "a: &a [x, x, x, x, x, x, x, x, x, x]\n"
	for _, n := range []string{"b", "c", "d"} {
		prev := string(rune(n[0] - 1))
		laughs += n + ": &" + n + " [" + strings.Repeat("*"+prev+", ", 9) + "*" + prev + "]\n"
	}
	for name, tc := range map[string]struct{ on, want string }{
		"cycle":  {"owner: a\nfuture: &a [*a]\n", "contains an alias to itself"},
		"laughs": {"owner: a\n" + laughs, "expand past"},
	} {
		t.Run(name, func(t *testing.T) {
			paths := ResolveLocalPathsForProfile(t.TempDir(), PeerProfile)
			if err := os.MkdirAll(paths.StoreDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(paths.ConfigFile, []byte(tc.on), 0o644); err != nil {
				t.Fatal(err)
			}
			cfg, _, err := LoadLocalConfig(paths)
			if err != nil {
				t.Fatal(err)
			}
			if err := SaveLocalConfig(paths, cfg); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("save = %v, want a refusal saying %q", err, tc.want)
			}
			if body, _ := os.ReadFile(paths.ConfigFile); string(body) != tc.on {
				t.Fatalf("the file changed:\n%s", body)
			}
		})
	}
}

// Every LocalConfig field has an explicit yaml name, so the keys it owns
// are exactly what it marshals (an untagged field would be carried twice).
func TestLocalConfigKeysMatchWhatItMarshals(t *testing.T) {
	full := LocalConfig{Target: "t", Owner: "o", OwnerAliases: []string{"a"}, OwnerEpoch: 1, FencePending: true, IncludeSubmodules: true,
		MirrorPath: "m", FilterMode: DefaultFilterMode(), MaxDelete: 1, Interval: 1, PullInterval: 1, PushMode: "clean", PullMode: "clean",
		Paused: true, SharedExcludes: []string{"x"}, Hooks: PeerHooks{OnActivate: []string{"a"}}, RemoteDot: "d", HostMerge: map[string][]string{"f": {"k"}}}
	data, err := yaml.Marshal(&full)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := yaml.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	known := localConfigKeys()
	for key := range m {
		if !known[key] {
			t.Errorf("marshaled key %q is not owned", key)
		}
	}
	if len(m) != len(known) {
		t.Errorf("owned %d keys, marshaled %d: fill every field in this test", len(known), len(m))
	}
}
