// Package remote mirrors a local git checkout onto an ssh-reachable host and
// runs a command there in a container (#421).
//
// The local machine stays the only place source is edited: a run sends git's
// view of the tree, nothing is ever copied back, and the work happens inside
// the image the host built. A run holds the admission controller's `remote`
// leases, so one mirror path runs one job at a time and one host carries at
// most max_runs of them from this machine.
package remote

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"math"
	"os/user"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/STAIxBWLB/dotfiles/internal/admission"
	"github.com/STAIxBWLB/dotfiles/internal/appsettings"
	"github.com/STAIxBWLB/dotfiles/internal/config"
)

// Defaults for the optional keys of one host.
const (
	DefaultRoot     = "dot-remote"
	DefaultGitRoot  = "dot-remote-git"
	DefaultSrcRoot  = "dot-remote-src"
	DefaultMemory   = "32g"
	DefaultMaxRuns  = 3
	DefaultRuntime  = "docker"
	prefixSuffix    = "-rdev"
	mirrorMountRoot = "/work"
)

// runtimes are the container runtimes a host may name.
var runtimes = map[string]bool{"docker": true, "podman": true}

// memoryPattern accepts the size syntax both runtimes take: a number with an
// optional b/k/m/g suffix.
var memoryPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?[bkmgBKMG]?$`)

// namePattern is the character set a volume name, image prefix or container
// name may use.
var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// envPattern is a portable environment variable name.
var envPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Config is the resolved `remote:` block: defaults applied, values validated,
// local paths absolute.
type Config struct {
	Default      string
	Hosts        map[string]Host
	LocalRoot    string
	ExcludeExtra []string
	Projects     []Project
}

// Host is one resolved host entry.
type Host struct {
	Name string
	// SSH is the ssh destination or alias; it defaults to the host's name.
	SSH string
	// Runtime is the container runtime binary on the host.
	Runtime string
	// Prefix is prepended to every image, volume and container name this host
	// carries, so two people share one host without colliding.
	Prefix string
	// Root, GitRoot and SrcRoot are paths relative to the remote $HOME.
	Root    string
	GitRoot string
	SrcRoot string
	CPUs    int
	Memory  string
	MaxRuns int
	// MaxLoadRatio defers a run at load1 >= ratio x the host's CPU count.
	// Zero means the host sets no load check.
	MaxLoadRatio float64
	// Dockerfile is a local file built on the host from stdin; Ref is a
	// prebuilt image. Exactly one of them is set.
	Dockerfile string
	Ref        string
	Caches     []Cache
	Env        []EnvPair
}

// Cache is one named volume mounted into the container.
type Cache struct {
	Name   string
	Target string
}

// EnvPair is one extra container environment variable.
type EnvPair struct {
	Key   string
	Value string
}

// Project is one configured checkout and the defaults that travel with it.
type Project struct {
	Path   string
	Verify string
	Base   string
	Host   string
}

// Load resolves and validates the `remote:` block. home is the local home a
// ~-form path expands against, and an empty block resolves to an empty Config
// with nothing enabled.
func Load(state config.UserRemoteState, home string) (Config, error) {
	localRoot, err := localPath(state.LocalRoot, home, "remote.local_root")
	if err != nil {
		return Config{}, err
	}
	if localRoot == "" {
		localRoot = home
	}
	// Resolved, because a run resolves the path it is given: a local_root that
	// crosses a symlink (/tmp on macOS, /home on some Linux hosts) would
	// otherwise refuse every checkout below it as outside.
	localRoot = resolveExisting(localRoot)
	cfg := Config{
		LocalRoot:    localRoot,
		Hosts:        make(map[string]Host, len(state.Hosts)),
		ExcludeExtra: append([]string(nil), state.ExcludeExtra...),
	}
	for _, name := range sortedKeys(state.Hosts) {
		host, herr := resolveHost(name, state.Hosts[name], home)
		if herr != nil {
			return Config{}, herr
		}
		cfg.Hosts[name] = host
	}
	cfg.Default = strings.TrimSpace(state.Default)
	if cfg.Default == "" && len(cfg.Hosts) == 1 {
		for name := range cfg.Hosts {
			cfg.Default = name
		}
	}
	if cfg.Default != "" {
		if _, ok := cfg.Hosts[cfg.Default]; !ok {
			return Config{}, fmt.Errorf("remote.default: unknown host %q (configured: %s)", cfg.Default, strings.Join(sortedKeys(cfg.Hosts), ", "))
		}
	}
	for _, key := range sortedKeys(state.Projects) {
		project, perr := resolveProject(key, state.Projects[key], home, cfg.Hosts)
		if perr != nil {
			return Config{}, perr
		}
		cfg.Projects = append(cfg.Projects, project)
	}
	// Longest prefix first, so a per-worktree entry wins over its repo.
	sort.SliceStable(cfg.Projects, func(i, j int) bool {
		if len(cfg.Projects[i].Path) != len(cfg.Projects[j].Path) {
			return len(cfg.Projects[i].Path) > len(cfg.Projects[j].Path)
		}
		return cfg.Projects[i].Path < cfg.Projects[j].Path
	})
	return cfg, nil
}

// Enabled reports whether the block configures anything at all. Nothing about
// remote work happens without it.
func (c Config) Enabled() bool { return len(c.Hosts) > 0 }

// Host resolves a host by name, or the default host when name is empty.
func (c Config) Host(name string) (Host, error) {
	if !c.Enabled() {
		return Host{}, fmt.Errorf("no remote hosts are configured: add a `remote:` block to ~/.config/dotfiles/config.yaml (see docs/guides/servers.md)")
	}
	if name == "" {
		name = c.Default
	}
	if name == "" {
		return Host{}, fmt.Errorf("no default remote host: set `remote.default` or pass --host")
	}
	host, ok := c.Hosts[name]
	if !ok {
		return Host{}, fmt.Errorf("unknown host %q (configured: %s)", name, strings.Join(sortedKeys(c.Hosts), ", "))
	}
	return host, nil
}

// Match returns the longest configured project entry covering path.
func (c Config) Match(path string) (Project, bool) {
	clean := filepath.Clean(path)
	for _, project := range c.Projects {
		if clean == project.Path || strings.HasPrefix(clean, project.Path+string(filepath.Separator)) {
			return project, true
		}
	}
	return Project{}, false
}

// HostFor resolves the host one job uses: the --host value, then the host of
// the project entry covering path, then remote.default. The project entry is the
// whole reason it exists: a checkout whose builds belong on a particular host
// says so once in the config.
func (c Config) HostFor(flagName, path string) (Host, error) {
	if flagName != "" {
		return c.Host(flagName)
	}
	if path != "" {
		if project, ok := c.Match(path); ok && project.Host != "" {
			return c.Host(project.Host)
		}
	}
	return c.Host("")
}

// Rel returns path's location under LocalRoot as a slash path, which is what
// names the mirror directory and the container's working directory.
func (c Config) Rel(path string) (string, error) {
	clean := filepath.Clean(path)
	rel, err := filepath.Rel(c.LocalRoot, clean)
	if err != nil {
		return "", fmt.Errorf("%s is not under remote.local_root %s: %w", clean, c.LocalRoot, err)
	}
	rel = filepath.ToSlash(rel)
	if rel == "." {
		return "", fmt.Errorf("refusing to mirror the whole of remote.local_root (%s): pass a checkout below it", c.LocalRoot)
	}
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("%s is outside remote.local_root (%s)", clean, c.LocalRoot)
	}
	return rel, nil
}

// RefuseRoots reports why path cannot be mirrored at all: the local root and
// the home directory hold every checkout below them, so mirroring one of them
// would send the whole tree.
func (c Config) RefuseRoots(path, home string) error {
	clean := filepath.Clean(path)
	for _, root := range []string{c.LocalRoot, home} {
		if root == "" {
			continue
		}
		if clean == filepath.Clean(root) {
			return fmt.Errorf("refusing to mirror %s: it is remote.local_root or $HOME, pass a checkout below it", clean)
		}
	}
	return nil
}

// MirrorKey is the short hash naming one mirror's container. It is derived from
// the mirror's own path - the checkout's location relative to local_root - and
// not from the local absolute path, so two machines that mirror the same
// relative checkout to one host ask the runtime for the same container name and
// the second run is refused by the host rather than the lease store.
func MirrorKey(rel string) string {
	// Leading slash first, so "a/b" and "/a/b" are one key rather than two.
	sum := sha1.Sum([]byte(path.Clean("/" + rel)))
	return hex.EncodeToString(sum[:])[:12]
}

// MirrorDir is the mirror's location relative to the remote $HOME.
func (h Host) MirrorDir(rel string) string { return path.Join(h.Root, rel) }

// WorkDir is where the mirror is mounted inside the container.
func (h Host) WorkDir(rel string) string { return path.Join(mirrorMountRoot, rel) }

// ContainerName is the container one mirror runs in.
func (h Host) ContainerName(rel string) string { return h.Prefix + "-" + MirrorKey(rel) }

// Image is the image a run uses: the prebuilt ref, or the one the host built
// from the Dockerfile.
func (h Host) Image() string {
	if h.Ref != "" {
		return h.Ref
	}
	return h.Prefix + ":latest"
}

// Volume is the host-side name of one configured cache volume.
func (h Host) Volume(name string) string { return h.Prefix + "-" + name }

// EnvPairs is every environment variable a run passes: CARGO_BUILD_JOBS and
// GOMAXPROCS default to cpus, and the host's own entries override them. The
// order is stable, so one config renders one argv.
func (h Host) EnvPairs() []EnvPair {
	values := map[string]string{}
	if h.CPUs > 0 {
		cpus := fmt.Sprintf("%d", h.CPUs)
		values["CARGO_BUILD_JOBS"] = cpus
		values["GOMAXPROCS"] = cpus
	}
	for _, pair := range h.Env {
		values[pair.Key] = pair.Value
	}
	out := make([]EnvPair, 0, len(values))
	for _, key := range sortedKeys(values) {
		out = append(out, EnvPair{Key: key, Value: values[key]})
	}
	return out
}

func resolveHost(name string, in config.UserRemoteHost, home string) (Host, error) {
	host := Host{
		Name:       name,
		SSH:        strings.TrimSpace(in.SSH),
		Runtime:    strings.TrimSpace(in.Runtime),
		Prefix:     strings.TrimSpace(in.Prefix),
		Root:       strings.TrimSpace(in.Root),
		GitRoot:    strings.TrimSpace(in.GitRoot),
		SrcRoot:    strings.TrimSpace(in.SrcRoot),
		CPUs:       in.CPUs,
		Memory:     strings.TrimSpace(in.Memory),
		MaxRuns:    DefaultMaxRuns,
		Dockerfile: strings.TrimSpace(in.Image.Dockerfile),
		Ref:        strings.TrimSpace(in.Image.Ref),
	}
	key := "remote.hosts." + name
	if host.SSH == "" {
		host.SSH = name
	}
	// A destination beginning with '-' would be read as an ssh option: the one
	// word this package passes to ssh unquoted is the destination, and
	// `ssh -oBatchMode=yes -v ...` is not a host. Whitespace is refused too,
	// because ssh would split it into separate arguments.
	if strings.HasPrefix(host.SSH, "-") {
		return Host{}, fmt.Errorf("%s.ssh: %q must not start with '-', which ssh reads as an option", key, host.SSH)
	}
	if strings.ContainsAny(host.SSH, " \t\n") {
		return Host{}, fmt.Errorf("%s.ssh: %q must be one ssh destination or alias with no spaces", key, host.SSH)
	}
	if host.Runtime == "" {
		host.Runtime = DefaultRuntime
	}
	if !runtimes[host.Runtime] {
		return Host{}, fmt.Errorf("%s.runtime: %q is not docker or podman", key, host.Runtime)
	}
	if host.Prefix == "" {
		host.Prefix = localUserName() + prefixSuffix
	}
	if !namePattern.MatchString(host.Prefix) {
		return Host{}, fmt.Errorf("%s.prefix: %q may only use letters, digits, '.', '_' and '-', and must start with a letter or digit", key, host.Prefix)
	}
	for _, field := range []struct {
		key   string
		value *string
		fall  string
	}{
		{"root", &host.Root, DefaultRoot},
		{"git_root", &host.GitRoot, DefaultGitRoot},
		{"src_root", &host.SrcRoot, DefaultSrcRoot},
	} {
		if *field.value == "" {
			*field.value = field.fall
		}
		if msg := checkRelativeName(*field.value); msg != "" {
			return Host{}, fmt.Errorf("%s.%s: %s", key, field.key, msg)
		}
	}
	if host.CPUs < 0 {
		return Host{}, fmt.Errorf("%s.cpus: %d must not be negative", key, host.CPUs)
	}
	if host.Memory == "" {
		host.Memory = DefaultMemory
	}
	if !memoryPattern.MatchString(host.Memory) {
		return Host{}, fmt.Errorf("%s.memory: %q is not a size like 32g", key, host.Memory)
	}
	if in.MaxRuns != nil {
		host.MaxRuns = *in.MaxRuns
		if host.MaxRuns < 1 || host.MaxRuns > admission.MaxSlotsPerScope {
			return Host{}, fmt.Errorf("%s.max_runs: %d is outside 1-%d", key, host.MaxRuns, admission.MaxSlotsPerScope)
		}
	}
	if in.MaxLoadRatio != nil {
		ratio := *in.MaxLoadRatio
		if math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio <= 0 {
			return Host{}, fmt.Errorf("%s.max_load_ratio: %g must be above zero", key, ratio)
		}
		host.MaxLoadRatio = ratio
	}
	switch {
	case host.Dockerfile != "" && host.Ref != "":
		return Host{}, fmt.Errorf("%s.image: set either dockerfile or ref, not both", key)
	case host.Dockerfile == "" && host.Ref == "":
		return Host{}, fmt.Errorf("%s.image: set one of dockerfile (built on the host) or ref (pulled)", key)
	}
	if host.Dockerfile != "" {
		expanded, err := localPath(host.Dockerfile, home, key+".image.dockerfile")
		if err != nil {
			return Host{}, err
		}
		host.Dockerfile = expanded
	}
	if strings.Contains(host.Ref, " ") {
		return Host{}, fmt.Errorf("%s.image.ref: %q contains a space", key, host.Ref)
	}
	for _, cacheName := range sortedKeys(in.Caches) {
		target := strings.TrimSpace(in.Caches[cacheName])
		if !namePattern.MatchString(cacheName) {
			return Host{}, fmt.Errorf("%s.caches: volume name %q may only use letters, digits, '.', '_' and '-'", key, cacheName)
		}
		if !path.IsAbs(target) {
			return Host{}, fmt.Errorf("%s.caches.%s: %q must be an absolute path inside the container", key, cacheName, target)
		}
		host.Caches = append(host.Caches, Cache{Name: cacheName, Target: path.Clean(target)})
	}
	for _, envKey := range sortedKeys(in.Env) {
		if !envPattern.MatchString(envKey) {
			return Host{}, fmt.Errorf("%s.env: %q is not a variable name", key, envKey)
		}
		host.Env = append(host.Env, EnvPair{Key: envKey, Value: in.Env[envKey]})
	}
	return host, nil
}

func resolveProject(key string, in config.UserRemoteProject, home string, hosts map[string]Host) (Project, error) {
	absolute, err := localPath(key, home, "remote.projects")
	if err != nil {
		return Project{}, err
	}
	if absolute == "" {
		return Project{}, fmt.Errorf("remote.projects: the entry %q is not a path", key)
	}
	project := Project{
		Path:   resolveExisting(absolute),
		Verify: strings.TrimSpace(in.Verify),
		Base:   strings.TrimSpace(in.Base),
		Host:   strings.TrimSpace(in.Host),
	}
	if project.Host != "" {
		if _, ok := hosts[project.Host]; !ok {
			return Project{}, fmt.Errorf("remote.projects.%s.host: unknown host %q", key, project.Host)
		}
	}
	return project, nil
}

// checkRelativeName refuses a remote path that would escape the remote $HOME
// or override it outright: these names are joined under $HOME on the host.
func checkRelativeName(value string) string {
	if path.IsAbs(value) {
		return fmt.Sprintf("%q must be relative to the remote $HOME, not absolute", value)
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." {
			return fmt.Sprintf("%q must not contain '..'", value)
		}
	}
	if strings.TrimSpace(value) == "" || value == "." {
		return "must name a directory"
	}
	return ""
}

// localPath expands a local ~-form path and makes it absolute.
func localPath(value, home, key string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	expanded := appsettings.ExpandHome(value, home)
	if !filepath.IsAbs(expanded) {
		return "", fmt.Errorf("%s: %q must be an absolute path or start with ~/", key, value)
	}
	return filepath.Clean(expanded), nil
}

// resolveExisting resolves a path's symlinks when it exists, and otherwise
// keeps it cleaned. Running the resolution over a path that does not exist would
// resolve whatever ancestor happens to be a symlink (/home on macOS is one) and
// spell a made-up path differently, so absence simply leaves it alone: the
// config is read again on the next command, by which time the checkout exists.
func resolveExisting(value string) string {
	if value == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(value); err == nil {
		return resolved
	}
	return filepath.Clean(value)
}

// localUserName is the invoking user, whose name defaults the host prefix. Any
// character a container name rejects becomes '-', so the default prefix always
// passes namePattern.
func localUserName() string {
	name := ""
	if current, err := user.Current(); err == nil {
		name = current.Username
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	cleaned := b.String()
	if cleaned == "" || !namePattern.MatchString(cleaned) {
		return "dot"
	}
	return cleaned
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
