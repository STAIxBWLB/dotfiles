package remote

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/config"
)

func intPtr(v int) *int           { return &v }
func floatPtr(v float64) *float64 { return &v }

func validHost() config.UserRemoteHost {
	return config.UserRemoteHost{Image: config.UserRemoteImage{Ref: "ghcr.io/org/dev:1"}}
}

func stateWith(host config.UserRemoteHost) config.UserRemoteState {
	return config.UserRemoteState{
		Default: "buildbox",
		Hosts:   map[string]config.UserRemoteHost{"buildbox": host},
	}
}

func TestLoadAppliesDefaults(t *testing.T) {
	cfg, err := Load(stateWith(validHost()), "/home/u")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Enabled() {
		t.Fatal("a block with one host is not enabled")
	}
	if cfg.LocalRoot != "/home/u" {
		t.Errorf("local_root = %q, want the home directory", cfg.LocalRoot)
	}
	host, err := cfg.Host("")
	if err != nil {
		t.Fatalf("Host: %v", err)
	}
	for _, want := range []struct {
		name string
		got  string
		want string
	}{
		{"ssh", host.SSH, "buildbox"},
		{"runtime", host.Runtime, "docker"},
		{"root", host.Root, DefaultRoot},
		{"git_root", host.GitRoot, DefaultGitRoot},
		{"src_root", host.SrcRoot, DefaultSrcRoot},
		{"memory", host.Memory, DefaultMemory},
	} {
		if want.got != want.want {
			t.Errorf("%s = %q, want %q", want.name, want.got, want.want)
		}
	}
	if host.MaxRuns != DefaultMaxRuns {
		t.Errorf("max_runs = %d, want %d", host.MaxRuns, DefaultMaxRuns)
	}
	if host.MaxLoadRatio != 0 {
		t.Errorf("max_load_ratio = %g, want no load check", host.MaxLoadRatio)
	}
	if !strings.HasSuffix(host.Prefix, prefixSuffix) {
		t.Errorf("prefix = %q, want the <user>%s default", host.Prefix, prefixSuffix)
	}
	if host.Image() != "ghcr.io/org/dev:1" {
		t.Errorf("image = %q, want the prebuilt ref", host.Image())
	}
}

func TestLoadEmptyBlockEnablesNothing(t *testing.T) {
	cfg, err := Load(config.UserRemoteState{}, "/home/u")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Enabled() {
		t.Fatal("an empty block enabled remote work")
	}
	if _, err := cfg.Host(""); err == nil || !strings.Contains(err.Error(), "no remote hosts are configured") {
		t.Errorf("Host on an empty block = %v, want the enabling instruction", err)
	}
}

func TestLoadRejectsInvalidKeys(t *testing.T) {
	cases := []struct {
		name  string
		state func() config.UserRemoteState
		want  string
	}{
		{"unknown default host", func() config.UserRemoteState {
			s := stateWith(validHost())
			s.Default = "elsewhere"
			return s
		}, "remote.default"},
		{"neither image", func() config.UserRemoteState {
			return stateWith(config.UserRemoteHost{})
		}, "remote.hosts.buildbox.image"},
		{"both images", func() config.UserRemoteState {
			host := validHost()
			host.Image.Dockerfile = "/home/u/Dockerfile"
			return stateWith(host)
		}, "remote.hosts.buildbox.image"},
		{"unknown runtime", func() config.UserRemoteState {
			host := validHost()
			host.Runtime = "cri-o"
			return stateWith(host)
		}, "remote.hosts.buildbox.runtime"},
		{"max_runs below one", func() config.UserRemoteState {
			host := validHost()
			host.MaxRuns = intPtr(0)
			return stateWith(host)
		}, "remote.hosts.buildbox.max_runs"},
		{"max_runs above the ceiling", func() config.UserRemoteState {
			host := validHost()
			host.MaxRuns = intPtr(9)
			return stateWith(host)
		}, "remote.hosts.buildbox.max_runs"},
		{"ratio at zero", func() config.UserRemoteState {
			host := validHost()
			host.MaxLoadRatio = floatPtr(0)
			return stateWith(host)
		}, "remote.hosts.buildbox.max_load_ratio"},
		{"ratio below zero", func() config.UserRemoteState {
			host := validHost()
			host.MaxLoadRatio = floatPtr(-1)
			return stateWith(host)
		}, "remote.hosts.buildbox.max_load_ratio"},
		{"absolute root", func() config.UserRemoteState {
			host := validHost()
			host.Root = "/srv/dot-remote"
			return stateWith(host)
		}, "remote.hosts.buildbox.root"},
		{"root escapes home", func() config.UserRemoteState {
			host := validHost()
			host.Root = "../../etc"
			return stateWith(host)
		}, "remote.hosts.buildbox.root"},
		{"memory is not a size", func() config.UserRemoteState {
			host := validHost()
			host.Memory = "32 gigabytes"
			return stateWith(host)
		}, "remote.hosts.buildbox.memory"},
		{"cache target is relative", func() config.UserRemoteState {
			host := validHost()
			host.Caches = map[string]string{"cargo": "cargo/registry"}
			return stateWith(host)
		}, "remote.hosts.buildbox.caches.cargo"},
		{"env name", func() config.UserRemoteState {
			host := validHost()
			host.Env = map[string]string{"A B": "x"}
			return stateWith(host)
		}, "remote.hosts.buildbox.env"},
		{"relative dockerfile", func() config.UserRemoteState {
			host := config.UserRemoteHost{Image: config.UserRemoteImage{Dockerfile: "Dockerfile.dev"}}
			return stateWith(host)
		}, "remote.hosts.buildbox.image.dockerfile"},
		{"unknown project host", func() config.UserRemoteState {
			s := stateWith(validHost())
			s.Projects = map[string]config.UserRemoteProject{"/home/u/work/one": {Host: "other"}}
			return s
		}, "remote.projects./home/u/work/one.host"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(tc.state(), "/home/u")
			if err == nil {
				t.Fatalf("Load accepted %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not name %q", err, tc.want)
			}
		})
	}
}

func TestLoadExpandsTildeInLocalPaths(t *testing.T) {
	host := config.UserRemoteHost{Image: config.UserRemoteImage{Dockerfile: "~/images/dev.Dockerfile"}}
	state := stateWith(host)
	state.LocalRoot = "~/workspace"
	state.Projects = map[string]config.UserRemoteProject{"~/workspace/work/one": {Verify: "make check"}}
	cfg, err := Load(state, "/home/u")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LocalRoot != "/home/u/workspace" {
		t.Errorf("local_root = %q, want the expanded path", cfg.LocalRoot)
	}
	resolved, err := cfg.Host("buildbox")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Dockerfile != "/home/u/images/dev.Dockerfile" {
		t.Errorf("dockerfile = %q, want the expanded path", resolved.Dockerfile)
	}
	if len(cfg.Projects) != 1 || cfg.Projects[0].Path != "/home/u/workspace/work/one" {
		t.Fatalf("projects = %+v, want the expanded key", cfg.Projects)
	}
}

func TestMatchTakesTheLongestPrefixOnAPathBoundary(t *testing.T) {
	cfg, err := Load(config.UserRemoteState{
		Default: "a",
		Hosts: map[string]config.UserRemoteHost{
			"a": validHost(),
			"b": validHost(),
		},
		Projects: map[string]config.UserRemoteProject{
			"/home/u/work":           {Host: "a", Verify: "make check"},
			"/home/u/work/dev/one":   {Host: "b", Verify: "scripts/check.sh"},
			"/home/u/work/dev/one-x": {Host: "b", Verify: "other.sh"},
		},
	}, "/home/u")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cases := map[string]string{
		"/home/u/work":                    "make check",
		"/home/u/work/dev/one":            "scripts/check.sh",
		"/home/u/work/dev/one/sub":        "scripts/check.sh",
		"/home/u/work/dev/one-x":          "other.sh",
		"/home/u/work/dev/two":            "make check",
		"/home/u/work/dev/one/../one/sub": "scripts/check.sh",
	}
	for path, want := range cases {
		project, ok := cfg.Match(path)
		if !ok {
			t.Errorf("Match(%q) found no entry", path)
			continue
		}
		if project.Verify != want {
			t.Errorf("Match(%q).Verify = %q, want %q", path, project.Verify, want)
		}
	}
	// A sibling with the same prefix as a configured path is not under it.
	if _, ok := cfg.Match("/home/u/workbench"); ok {
		t.Error("Match treated /home/u/workbench as under /home/u/work")
	}
}

func TestRelNamesTheMirrorPath(t *testing.T) {
	cfg, err := Load(config.UserRemoteState{
		Default:   "buildbox",
		LocalRoot: "/home/u/workspace",
		Hosts:     map[string]config.UserRemoteHost{"buildbox": validHost()},
	}, "/home/u")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	rel, err := cfg.Rel("/home/u/workspace/work/dev/one")
	if err != nil || rel != "work/dev/one" {
		t.Fatalf("Rel = %q, %v, want work/dev/one", rel, err)
	}
	if _, err := cfg.Rel("/home/u/workspace"); err == nil || !strings.Contains(err.Error(), "refusing to mirror the whole") {
		t.Errorf("Rel(root) = %v, want the whole-root refusal", err)
	}
	if _, err := cfg.Rel("/home/u/elsewhere"); err == nil || !strings.Contains(err.Error(), "outside remote.local_root") {
		t.Errorf("Rel(outside) = %v, want the outside refusal", err)
	}
}

func TestRefuseRoots(t *testing.T) {
	cfg, err := Load(config.UserRemoteState{
		Default:   "buildbox",
		LocalRoot: "/home/u/workspace",
		Hosts:     map[string]config.UserRemoteHost{"buildbox": validHost()},
	}, "/home/u")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, path := range []string{"/home/u/workspace", "/home/u", "/home/u/"} {
		if err := cfg.RefuseRoots(path, "/home/u"); err == nil {
			t.Errorf("RefuseRoots(%q) allowed it", path)
		}
	}
	if err := cfg.RefuseRoots("/home/u/workspace/work/one", "/home/u"); err != nil {
		t.Errorf("RefuseRoots(repo) = %v", err)
	}
}

func TestMirrorAndContainerNames(t *testing.T) {
	cfg, err := Load(stateWith(validHost()), "/home/u")
	if err != nil {
		t.Fatal(err)
	}
	host, err := cfg.Host("")
	if err != nil {
		t.Fatal(err)
	}
	if got := host.MirrorDir("work/dev/one"); got != DefaultRoot+"/work/dev/one" {
		t.Errorf("mirror = %q", got)
	}
	if got := host.WorkDir("work/dev/one"); got != "/work/work/dev/one" {
		t.Errorf("workdir = %q", got)
	}
	key := MirrorKey("work/dev/one")
	if len(key) != 12 {
		t.Errorf("mirror key = %q, want 12 hex characters", key)
	}
	if key != MirrorKey("work/dev/one/") || key != MirrorKey("/work/dev/one") {
		t.Error("the mirror key changed with a trailing separator or a leading slash")
	}
	if key == MirrorKey("work/dev/two") {
		t.Error("two mirrors shared a container name")
	}
	if got := host.ContainerName("work/dev/one"); got != host.Prefix+"-"+key {
		t.Errorf("container = %q", got)
	}
	if got := host.Volume("cargo"); got != host.Prefix+"-cargo" {
		t.Errorf("volume = %q", got)
	}
}

func TestEnvPairsDefaultToCPUsAndKeepOverrides(t *testing.T) {
	host := Host{CPUs: 8, Env: []EnvPair{{Key: "LANG", Value: "C.UTF-8"}}}
	pairs := host.EnvPairs()
	want := map[string]string{"CARGO_BUILD_JOBS": "8", "GOMAXPROCS": "8", "LANG": "C.UTF-8"}
	if len(pairs) != len(want) {
		t.Fatalf("pairs = %+v, want %v", pairs, want)
	}
	for _, pair := range pairs {
		if want[pair.Key] != pair.Value {
			t.Errorf("%s = %q, want %q", pair.Key, pair.Value, want[pair.Key])
		}
	}
	overridden := Host{CPUs: 8, Env: []EnvPair{{Key: "GOMAXPROCS", Value: "2"}}}
	for _, pair := range overridden.EnvPairs() {
		if pair.Key == "GOMAXPROCS" && pair.Value != "2" {
			t.Errorf("GOMAXPROCS = %q, want the configured 2", pair.Value)
		}
	}
	if none := (Host{}).EnvPairs(); len(none) != 0 {
		t.Errorf("a host with no cpus passed %+v, want nothing", none)
	}
}

// A local_root that crosses a symlink is the ordinary macOS case (/tmp and
// /var are symlinks) and the ordinary Linux one (/home). A run resolves the
// path it is given, so the root has to be resolved too or every checkout below
// it is refused as outside.
func TestLocalRootAndProjectsAreSymlinkResolved(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	// The checkout exists before the config is read, which is the order a real
	// command has: every `dot remote` re-reads the config.
	checkout := filepath.Join(real, "work", "one")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatal(err)
	}
	state := stateWith(validHost())
	state.LocalRoot = link
	state.Projects = map[string]config.UserRemoteProject{
		link + "/work/one": {Host: "buildbox"},
	}
	cfg, err := Load(state, "/home/u")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	resolved, rerr := filepath.EvalSymlinks(real)
	if rerr != nil {
		t.Fatal(rerr)
	}
	checkout = filepath.Join(resolved, "work", "one")
	if cfg.LocalRoot != resolved {
		t.Errorf("local_root = %q, want the resolved %q", cfg.LocalRoot, resolved)
	}
	// The resolved root accepts a resolved checkout, which is what a run passes.
	if _, rerr := cfg.Rel(checkout); rerr != nil {
		t.Errorf("Rel(%s) = %v, want it inside the resolved root", checkout, rerr)
	}
	if project, ok := cfg.Match(checkout); !ok || project.Host != "buildbox" {
		t.Errorf("Match(%s) = %+v, %v, want the project entry", checkout, project, ok)
	}
}

// An ssh destination beginning with '-' is an option, not a host, and ssh would
// read it as one.
func TestAnSSHDestinationCannotBeAnOption(t *testing.T) {
	for _, dest := range []string{"-v", "--proxy-command=sh", "-oProxyCommand=sh"} {
		host := validHost()
		host.SSH = dest
		_, err := Load(stateWith(host), "/home/u")
		if err == nil {
			t.Fatalf("Load accepted the ssh destination %q", dest)
		}
		if !strings.Contains(err.Error(), "must not start with '-'") {
			t.Errorf("error %q does not explain the refusal", err)
		}
	}
	host := validHost()
	host.SSH = "build box"
	if _, err := Load(stateWith(host), "/home/u"); err == nil {
		t.Error("Load accepted an ssh destination with a space")
	}
}

// The host a job uses is the --host value, then the matching project's host,
// then remote.default: the project entry is what makes a checkout's own host
// travel with the config.
func TestHostForPrefersTheFlagThenTheProjectThenTheDefault(t *testing.T) {
	state := config.UserRemoteState{
		Default: "buildbox",
		Hosts: map[string]config.UserRemoteHost{
			"buildbox": validHost(),
			"bigbox":   validHost(),
		},
		Projects: map[string]config.UserRemoteProject{
			"/home/u/workspace/work/one": {Host: "bigbox"},
		},
	}
	cfg, err := Load(state, "/home/u")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if host, err := cfg.HostFor("", "/home/u/workspace/work/one/sub"); err != nil || host.Name != "bigbox" {
		t.Errorf("HostFor(project path) = %q, %v, want bigbox", host.Name, err)
	}
	if host, err := cfg.HostFor("buildbox", "/home/u/workspace/work/one"); err != nil || host.Name != "buildbox" {
		t.Errorf("HostFor(--host) = %q, %v, want the flag to win", host.Name, err)
	}
	if host, err := cfg.HostFor("", "/home/u/workspace/other"); err != nil || host.Name != "buildbox" {
		t.Errorf("HostFor(unmatched path) = %q, %v, want the default", host.Name, err)
	}
	if host, err := cfg.HostFor("", ""); err != nil || host.Name != "buildbox" {
		t.Errorf("HostFor(host command) = %q, %v, want the default", host.Name, err)
	}
}
