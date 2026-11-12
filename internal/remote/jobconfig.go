package remote

import (
	"fmt"
	"math"
	"path"
	"regexp"
	"strings"

	"github.com/STAIxBWLB/dotfiles/internal/admission"
	"github.com/STAIxBWLB/dotfiles/internal/config"
)

// Defaults for a job profile's optional keys.
const (
	DefaultJobsRoot = "dot-remote-jobs"
	DefaultMaxJobs  = 1
	// DefaultLocalJobsRoot is where a fallback job runs on this machine, under
	// the local home, when the profile names no local.root.
	DefaultLocalJobsRoot = ".local/state/dot/remote-jobs"
	FallbackLocal        = "local"
	FallbackNone         = "none"
)

// jobNamePattern is what a job name and a profile name may be: they name a
// directory on the host and a container, so they stay short and plain.
var jobNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// envPlaceholder is one {name} in a profile env value.
var envPlaceholder = regexp.MustCompile(`\{([^{}]*)\}`)

// JobProfile is one resolved `jobs.<profile>` entry of a host.
type JobProfile struct {
	Name string
	// Root holds the job directories on the host: absolute, or relative to the
	// remote $HOME.
	Root         string
	Dockerfile   string
	Ref          string
	CPUs         int
	Memory       string
	MaxJobs      int
	MaxLoadRatio float64
	MinFreeGB    int
	// Env is the container env, values still carrying {cpus} and {half_cpus}.
	Env       []EnvPair
	Fallback  string
	LocalRoot string
	LocalPath []string
	LocalEnv  []EnvPair
}

// ImageHost is the host as `dot remote setup --profile` and a job see it: the
// same ssh and runtime, but the profile's image. A built profile image is
// named <prefix>-<profile>:latest, so it sits next to the dev image.
func (p JobProfile) ImageHost(h Host) Host {
	out := h
	out.Prefix = h.Prefix + "-" + p.Name
	out.Dockerfile, out.Ref = p.Dockerfile, p.Ref
	out.CPUs, out.Memory = p.CPUs, p.Memory
	out.Caches, out.Env = nil, nil
	return out
}

func resolveJobs(hostKey string, host Host, in map[string]config.UserRemoteJob, home string) (map[string]JobProfile, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make(map[string]JobProfile, len(in))
	for _, name := range sortedKeys(in) {
		profile, err := resolveJob(hostKey+".jobs."+name, name, host, in[name], home)
		if err != nil {
			return nil, err
		}
		out[name] = profile
	}
	return out, nil
}

func resolveJob(key, name string, host Host, in config.UserRemoteJob, home string) (JobProfile, error) {
	if !jobNamePattern.MatchString(name) {
		return JobProfile{}, fmt.Errorf("%s: the profile name must match %s", key, jobNamePattern)
	}
	p := JobProfile{
		Name:       name,
		Root:       strings.TrimSpace(in.Root),
		Dockerfile: strings.TrimSpace(in.Image.Dockerfile),
		Ref:        strings.TrimSpace(in.Image.Ref),
		CPUs:       in.CPUs,
		Memory:     strings.TrimSpace(in.Memory),
		MaxJobs:    DefaultMaxJobs,
		Fallback:   strings.TrimSpace(in.Fallback),
	}
	if p.Root == "" {
		p.Root = path.Join(DefaultJobsRoot, name)
	}
	if !path.IsAbs(p.Root) {
		if msg := checkRelativeName(p.Root); msg != "" {
			return JobProfile{}, fmt.Errorf("%s.root: %s", key, msg)
		}
	}
	for _, part := range strings.Split(p.Root, "/") {
		if part == ".." {
			return JobProfile{}, fmt.Errorf("%s.root: %q must not contain '..'", key, p.Root)
		}
	}
	if path.Clean(p.Root) == "/" {
		return JobProfile{}, fmt.Errorf("%s.root: %q must name a directory below /", key, p.Root)
	}
	p.Root = path.Clean(p.Root)
	switch {
	case p.Dockerfile != "" && p.Ref != "":
		return JobProfile{}, fmt.Errorf("%s.image: set either dockerfile or ref, not both", key)
	case p.Dockerfile == "" && p.Ref == "":
		return JobProfile{}, fmt.Errorf("%s.image: set one of dockerfile (built on the host) or ref (pulled)", key)
	}
	if p.Dockerfile != "" {
		expanded, err := localPath(p.Dockerfile, home, key+".image.dockerfile")
		if err != nil {
			return JobProfile{}, err
		}
		p.Dockerfile = expanded
	}
	if strings.ContainsAny(p.Ref, " \t\n") {
		return JobProfile{}, fmt.Errorf("%s.image.ref: %q contains a space", key, p.Ref)
	}
	if p.CPUs < 0 {
		return JobProfile{}, fmt.Errorf("%s.cpus: %d must not be negative", key, p.CPUs)
	}
	if p.CPUs == 0 {
		p.CPUs = host.CPUs
	}
	if p.Memory == "" {
		p.Memory = host.Memory
	}
	if !memoryPattern.MatchString(p.Memory) {
		return JobProfile{}, fmt.Errorf("%s.memory: %q is not a size like 32g", key, p.Memory)
	}
	if in.MaxJobs != nil {
		p.MaxJobs = *in.MaxJobs
		if p.MaxJobs < 1 || p.MaxJobs > admission.MaxSlotsPerScope {
			return JobProfile{}, fmt.Errorf("%s.max_jobs: %d is outside 1-%d", key, p.MaxJobs, admission.MaxSlotsPerScope)
		}
	}
	if in.MaxLoadRatio != nil {
		ratio := *in.MaxLoadRatio
		if math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio <= 0 {
			return JobProfile{}, fmt.Errorf("%s.max_load_ratio: %g must be above zero", key, ratio)
		}
		p.MaxLoadRatio = ratio
	}
	if in.MinFreeGB != nil {
		if *in.MinFreeGB < 0 {
			return JobProfile{}, fmt.Errorf("%s.min_free_gb: %d must not be negative", key, *in.MinFreeGB)
		}
		p.MinFreeGB = *in.MinFreeGB
	}
	for _, envKey := range sortedKeys(in.Env) {
		if !envPattern.MatchString(envKey) {
			return JobProfile{}, fmt.Errorf("%s.env: %q is not a variable name", key, envKey)
		}
		value := in.Env[envKey]
		for _, m := range envPlaceholder.FindAllStringSubmatch(value, -1) {
			if m[1] != "cpus" && m[1] != "half_cpus" {
				return JobProfile{}, fmt.Errorf("%s.env.%s: unknown placeholder {%s}; use {cpus} or {half_cpus}", key, envKey, m[1])
			}
		}
		p.Env = append(p.Env, EnvPair{Key: envKey, Value: value})
	}
	switch p.Fallback {
	case "":
		p.Fallback = FallbackLocal
	case FallbackLocal, FallbackNone:
	default:
		return JobProfile{}, fmt.Errorf("%s.fallback: %q is not local or none", key, p.Fallback)
	}
	localRoot, err := localPath(in.Local.Root, home, key+".local.root")
	if err != nil {
		return JobProfile{}, err
	}
	if localRoot == "" {
		localRoot = path.Join(home, DefaultLocalJobsRoot, name)
	}
	p.LocalRoot = localRoot
	for i, entry := range in.Local.Path {
		expanded, err := localPath(entry, home, fmt.Sprintf("%s.local.path[%d]", key, i))
		if err != nil {
			return JobProfile{}, err
		}
		if expanded == "" {
			return JobProfile{}, fmt.Errorf("%s.local.path[%d]: empty entry", key, i)
		}
		p.LocalPath = append(p.LocalPath, expanded)
	}
	for _, envKey := range sortedKeys(in.Local.Env) {
		if !envPattern.MatchString(envKey) {
			return JobProfile{}, fmt.Errorf("%s.local.env: %q is not a variable name", key, envKey)
		}
		p.LocalEnv = append(p.LocalEnv, EnvPair{Key: envKey, Value: in.Local.Env[envKey]})
	}
	return p, nil
}

// JobEnv renders the profile's container env for a job on cpus CPUs: {cpus}
// is the count, {half_cpus} half of it, at least 1.
func (p JobProfile) JobEnv(cpus int) []EnvPair {
	half := cpus / 2
	if half < 1 {
		half = 1
	}
	replacer := strings.NewReplacer("{cpus}", fmt.Sprint(cpus), "{half_cpus}", fmt.Sprint(half))
	out := make([]EnvPair, 0, len(p.Env))
	for _, pair := range p.Env {
		out = append(out, EnvPair{Key: pair.Key, Value: replacer.Replace(pair.Value)})
	}
	return out
}

// Profile picks the job profile a command uses: the named one, or the only one
// the host has.
func (h Host) Profile(name string) (JobProfile, error) {
	if len(h.Jobs) == 0 {
		return JobProfile{}, fmt.Errorf("%s has no job profiles: add remote.hosts.%s.jobs (see docs/guides/servers.md)", h.Name, h.Name)
	}
	if name == "" {
		if len(h.Jobs) > 1 {
			return JobProfile{}, fmt.Errorf("%s has several job profiles (%s): pass --profile", h.Name, strings.Join(sortedKeys(h.Jobs), ", "))
		}
		for _, p := range h.Jobs {
			return p, nil
		}
	}
	p, ok := h.Jobs[name]
	if !ok {
		return JobProfile{}, fmt.Errorf("%s has no job profile %q (configured: %s)", h.Name, name, strings.Join(sortedKeys(h.Jobs), ", "))
	}
	return p, nil
}
