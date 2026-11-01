package aitooling

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type binarySpec struct {
	npm, metadata, installer, relpath, brew string
	// allowSuffix accepts prerelease-suffixed versions (e.g. 1.18.32-gencode.1)
	// for metadata and pins; the strict stable X.Y.Z rule otherwise applies.
	allowSuffix bool
	// platformManifest formats the metadata URL with a runtime platform token.
	platformManifest bool
	// selfUpdating binaries update themselves; dot installs but never updates.
	selfUpdating bool
}

func binarySpecs() map[string]binarySpec {
	return map[string]binarySpec{
		"claude":   {npm: "@anthropic-ai/claude-code", metadata: "https://registry.npmjs.org/@anthropic-ai/claude-code/latest", installer: "https://claude.ai/install.sh", relpath: ".local/bin/claude", brew: "claude-code"},
		"codex":    {npm: "@openai/codex", metadata: "https://registry.npmjs.org/@openai/codex/latest", brew: "codex"},
		"qwen":     {npm: "@qwen-code/qwen-code", metadata: "https://registry.npmjs.org/@qwen-code/qwen-code/latest"},
		"kimi":     {metadata: "https://code.kimi.com/kimi-code/latest", installer: "https://code.kimi.com/kimi-code/install.sh", relpath: ".kimi-code/bin/kimi"},
		"grok":     {metadata: "https://x.ai/cli/stable", installer: "https://x.ai/cli/install.sh", relpath: ".grok/bin/grok"},
		"opencode": {npm: "opencode-ai", metadata: "https://api.github.com/repos/anomalyco/opencode/releases/latest", installer: "https://opencode.ai/install", relpath: ".opencode/bin/opencode", brew: "opencode"},
		"gencode":  {npm: "@genspark/gencode", metadata: "https://registry.npmjs.org/@genspark/gencode/latest", allowSuffix: true},
		"pi":       {npm: "@earendil-works/pi-coding-agent", metadata: "https://registry.npmjs.org/@earendil-works/pi-coding-agent/latest"},
		// pi.dev/install.sh is interactive; the npm provider is the managed route.
		"antigravity": {metadata: "https://antigravity-cli-auto-updater-974169037036.us-central1.run.app/manifests/%s.json", installer: "https://antigravity.google/cli/install.sh", relpath: ".local/bin/agy", platformManifest: true, selfUpdating: true},
		"ripwire":     {metadata: "https://api.github.com/repos/redhat-et/ripwire/releases/latest", installer: "https://raw.githubusercontent.com/redhat-et/ripwire/v%s/scripts/install.sh", relpath: ".local/bin/ripwire"},
		"ocr":         {npm: "@alibaba-group/open-code-review", metadata: "https://registry.npmjs.org/@alibaba-group/open-code-review/latest"},
	}
}
func (e *Engine) binary(ctx context.Context, en Entry, pin string, op Operation) ItemResult {
	r := ItemResult{ID: en.ID, Kind: en.Kind}
	spec := binarySpecs()[en.ID]
	path := e.find(en.Binary)
	if path != "" {
		v, err := e.exec(ctx, command{Path: path, Args: []string{"--version"}, Env: e.environment(), Dir: e.opts.HomeDir, Timeout: 15 * time.Second})
		if err != nil {
			r.Status = "unknown"
			r.Detail = "installed executable version probe failed"
			return r
		}
		r.Installed = versionInOutput.FindString(v)
		if r.Installed == "" {
			r.Status = "unknown"
			r.Detail = "installed version could not be parsed"
			return r
		}
	}
	provider, prefix := e.provenance(en.ID, path, spec)
	if provider == "brew" {
		return e.brewBinary(ctx, en, pin, op, path, spec, r)
	}
	latest, err := e.latestVersion(ctx, metadataURL(spec), spec.allowSuffix)
	if err != nil {
		r.Status = "unknown"
		r.Detail = err.Error()
		if op != Inspect && !e.opts.DryRun {
			r.Status = "deferred-metadata"
		}
		return r
	}
	r.Latest = latest
	target := latest
	if pin != "" {
		target = strings.TrimPrefix(pin, "v")
	}
	if spec.selfUpdating && pin != "" && target != latest {
		r.Status = "deferred-pin"
		r.Detail = fmt.Sprintf("%s installer only ships the current release %s; requested pin %s unavailable", en.Name, latest, target)
		return r
	}
	if path != "" && provider == "unknown" {
		r.Status = "deferred-provenance"
		r.Detail = "existing installation provider is unknown; keep it intact and adopt explicitly"
		return r
	}
	if r.Installed == target {
		r.Status = "up-to-date"
		if pin != "" {
			r.Status = "pinned"
		}
		r.Detail = "provider: " + provider
		if op != Inspect && !e.opts.DryRun {
			e.record(en.ID, provider, path, r.Installed, r.Status)
		}
		return r
	}
	if op == Inspect {
		r.Status = "update-available"
		if path == "" {
			r.Status = "missing"
		}
		r.Detail = "provider: " + provider
		return r
	}
	if op == Ensure && path != "" && pin == "" {
		r.Status = "installed"
		r.Detail = "update available; run dot ai update"
		if !e.opts.DryRun {
			e.record(en.ID, provider, path, r.Installed, r.Status)
		}
		return r
	}
	if e.opts.DryRun {
		r.Status = "planned"
		r.Detail = fmt.Sprintf("%s: install %s %s", provider, en.ID, target)
		return r
	}
	// A recorded version mismatch may be a deliberate user upgrade or pin. Do
	// not overwrite it until the selection is reconciled explicitly.
	if prior, ok := e.receipts[en.ID]; ok && pin == "" && prior.Version != "" && path != "" && prior.Version != r.Installed {
		r.Status = "deferred-local-change"
		r.Detail = "installed version changed outside dot; inspect and re-adopt before updating"
		return r
	}
	if provider == "npm" && e.opts.ExplicitHome && !strings.HasPrefix(filepath.Clean(prefix), filepath.Clean(e.opts.HomeDir)+string(os.PathSeparator)) {
		r.Status = "deferred-home"
		r.Detail = "existing npm prefix belongs outside requested home; preserve it"
		return r
	}
	if spec.selfUpdating && path != "" {
		r.Status = "deferred-self-update"
		r.Detail = en.Name + " self-updates in the background; remove " + path + " and run dot ai ensure to reinstall"
		return r
	}
	// Drain-aware activation (#319): the install changes what new sessions start; running processes of
	// the old version are reported and never stopped.
	live := ""
	if path != "" {
		live = drainNote(e.liveProcesses(ctx, en.Binary, path, prefix, spec))
	}
	if err = e.installBinary(ctx, en.ID, target, path, provider, prefix, spec); err != nil {
		r.Status = "failed"
		r.Detail = err.Error() + live
		return r
	}
	afterPath := e.find(en.Binary)
	if afterPath == "" && spec.relpath != "" {
		afterPath = filepath.Join(e.opts.HomeDir, spec.relpath)
	}
	out, err := e.exec(ctx, command{Path: afterPath, Args: []string{"--version"}, Env: e.environment(), Dir: e.opts.HomeDir, Timeout: 15 * time.Second})
	after := versionInOutput.FindString(out)
	if err != nil || after != target {
		r.Status = "failed"
		r.Detail = "installer returned, but requested version was not verified"
		return r
	}
	before := r.Installed
	r.Installed = after
	r.Status = "updated"
	if before == "" {
		r.Status = "installed"
	}
	r.Detail = fmt.Sprintf("%s: %s -> %s; authentication not changed", provider, before, after) + live
	e.record(en.ID, provider, afterPath, after, r.Status)
	return r
}

// liveProcesses lists the running processes of an installed tool before it is replaced: argv[0] is
// its PATH entry or the file that entry resolves to, or (for the person's own home, not an explicit
// --home) its bare PATH name, or the command line runs a file inside its npm package. An unreadable
// process table lists nothing; it never blocks the install.
func (e *Engine) liveProcesses(ctx context.Context, name, path, prefix string, spec binarySpec) []int {
	out, err := e.processes(ctx)
	if err != nil {
		return nil
	}
	resolved, _ := filepath.EvalSymlinks(path)
	contains := []string{}
	if spec.npm != "" && prefix != "" {
		contains = append(contains, filepath.Join(prefix, "lib", "node_modules", spec.npm)+"/")
	}
	var pids []int
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid == os.Getpid() {
			continue
		}
		argv0 := fields[1]
		match := (argv0 == name && !e.opts.ExplicitHome) || argv0 == path || (resolved != "" && argv0 == resolved)
		for _, c := range contains {
			match = match || strings.Contains(line, c)
		}
		if match {
			pids = append(pids, pid)
		}
	}
	return pids
}

// drainNote reports live processes: they keep the files they started with, and only new sessions get
// the new version. Nothing is stopped and nothing waits for them.
func drainNote(pids []int) string {
	if len(pids) == 0 {
		return ""
	}
	shown := pids
	more := ""
	if len(shown) > 10 {
		shown, more = shown[:10], fmt.Sprintf(" and %d more", len(pids)-10)
	}
	list := make([]string, len(shown))
	for i, pid := range shown {
		list[i] = strconv.Itoa(pid)
	}
	return fmt.Sprintf("; %d running process(es) left as they are (pid %s%s): new sessions get the new version, nothing was stopped", len(pids), strings.Join(list, ", "), more)
}

// listProcesses reads `ps -Ao pid=,args=`; the process table is read-only and stays in memory.
func listProcesses(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := osexec.CommandContext(ctx, "ps", "-Ao", "pid=,args=").Output()
	return string(out), err
}

// metadataURL resolves the latest-version metadata endpoint, expanding the
// platform token for platform-specific manifests.
func metadataURL(s binarySpec) string {
	if !s.platformManifest {
		return s.metadata
	}
	platform := runtime.GOOS + "_" + runtime.GOARCH
	switch platform {
	case "darwin_arm64", "darwin_amd64", "linux_amd64", "linux_arm64":
	default:
		// Unsupported platforms keep the literal template; the metadata fetch
		// fails and the entry defers instead of installing a wrong build.
		return s.metadata
	}
	return fmt.Sprintf(s.metadata, platform)
}

func (e *Engine) provenance(id, path string, s binarySpec) (string, string) {
	if path == "" {
		if s.installer != "" {
			return "native", ""
		}
		return "npm", filepath.Join(e.opts.HomeDir, ".local")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		resolved = path
	}
	if strings.Contains(resolved, "/Cellar/") || strings.Contains(resolved, "/Caskroom/") {
		return "brew", ""
	}
	if s.npm != "" {
		marker := "/lib/node_modules/" + s.npm + "/"
		if i := strings.Index(resolved, marker); i >= 0 {
			return "npm", resolved[:i]
		}
	}
	if s.relpath != "" && filepath.Clean(path) == filepath.Join(e.opts.HomeDir, s.relpath) {
		return "native", ""
	}
	if prior, ok := e.receipts[id]; ok && prior.Path == path {
		return prior.Provider, ""
	}
	return "unknown", ""
}
func (e *Engine) installBinary(ctx context.Context, id, version, path, provider, prefix string, s binarySpec) error {
	switch provider {
	case "brew":
		pkg, err := e.brewPackage(ctx, path, s)
		if err != nil {
			return err
		}
		return e.applyBrew(ctx, pkg, version, path != "")
	case "npm":
		npm := e.find("npm")
		if npm == "" {
			return fmt.Errorf("npm prerequisite missing; install Node.js before %s", id)
		}
		if prefix == "" {
			return fmt.Errorf("npm prefix unknown; refusing cross-prefix install")
		}
		_, err := e.run(ctx, npm, "install", "--global", "--prefix", prefix, "--registry=https://registry.npmjs.org", s.npm+"@"+version)
		return err
	case "native":
		if s.installer == "" {
			return fmt.Errorf("native provider unavailable for %s", id)
		}
		url := s.installer
		if id == "ripwire" {
			url = fmt.Sprintf(url, version)
		}
		body, err := e.get(ctx, url)
		if err != nil {
			return fmt.Errorf("official installer unavailable: %w", err)
		}
		env := e.environment()
		args := []string{"-s", "--", version}
		switch id {
		case "kimi":
			env = setEnv(env, "KIMI_NO_MODIFY_PATH", "1")
			env = setEnv(env, "KIMI_INSTALL_DIR", filepath.Join(e.opts.HomeDir, ".kimi-code"))
			args = []string{"-s", "--", "--version", version}
		case "opencode":
			args = []string{"-s", "--", "--version", version, "--no-modify-path"}
		case "ripwire":
			env = setEnv(env, "RIPWIRE_REPO", "redhat-et/ripwire")
			env = setEnv(env, "RIPWIRE_INSTALL_YES", "1")
			env = setEnv(env, "RIPWIRE_NO_ACTIVATE", "1")
			env = setEnv(env, "RIPWIRE_VERSION", "v"+version)
			env = setEnv(env, "RIPWIRE_INSTALL_PREFIX", filepath.Join(e.opts.HomeDir, ".local"))
			args = []string{"-s"}
		case "grok":
			env = setEnv(env, "GROK_CHANNEL", "stable")
			env = setEnv(env, "GROK_BIN_DIR", filepath.Join(e.opts.HomeDir, ".grok", "bin"))
		case "antigravity":
			args = []string{"-s", "--", "--dir", filepath.Join(e.opts.HomeDir, ".local", "bin")}
		}
		_, err = e.exec(ctx, command{Path: "/bin/bash", Args: args, Env: env, Dir: e.opts.HomeDir, Input: body})
		return err
	default:
		return fmt.Errorf("unknown installation provider for %s", id)
	}
}

// pathExists is intentionally not an executable invocation or initialization.
func pathExists(p string) bool { _, err := os.Stat(p); return err == nil }

// Homebrew metadata is authoritative for its own available stable version;
// upstream npm/GitHub versions may not yet have an equivalent formula/cask.
type brewPackage struct {
	executable, name, kind, version string
	pinned                          bool
}

func (e *Engine) brewEnvironment() []string {
	env := e.environment()
	for _, key := range []string{"HOMEBREW_NO_AUTO_UPDATE", "HOMEBREW_NO_INSTALL_CLEANUP", "HOMEBREW_NO_INSTALLED_DEPENDENTS_CHECK", "HOMEBREW_NO_ANALYTICS"} {
		env = setEnv(env, key, "1")
	}
	return env
}
func (e *Engine) brewPackage(ctx context.Context, path string, spec binarySpec) (brewPackage, error) {
	p := brewPackage{name: spec.brew}
	if p.name == "" {
		return p, fmt.Errorf("homebrew package identity unavailable; adopt the selected package explicitly")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		resolved = path
	}
	var prefix string
	for marker, kind := range map[string]string{"/Cellar/": "formula", "/Caskroom/": "cask"} {
		if i := strings.Index(resolved, marker); i >= 0 {
			prefix, p.kind = resolved[:i], kind
			if strings.Split(resolved[i+len(marker):], "/")[0] != p.name {
				return p, fmt.Errorf("homebrew package path does not match selected %s", p.name)
			}
		}
	}
	if prefix == "" {
		return p, fmt.Errorf("homebrew Cellar/Caskroom provenance unavailable; explicit adoption required")
	}
	p.executable = filepath.Join(prefix, "bin", "brew")
	if !pathExists(p.executable) {
		return p, fmt.Errorf("owning Homebrew executable unavailable at %s", p.executable)
	}
	out, err := e.exec(ctx, command{Path: p.executable, Args: []string{"info", "--json=v2", "--" + p.kind, p.name}, Env: e.brewEnvironment(), Dir: e.opts.HomeDir, Timeout: 30 * time.Second})
	if err != nil {
		return p, fmt.Errorf("homebrew metadata unavailable: %w", err)
	}
	var info struct {
		Formulae []struct {
			Name     string `json:"name"`
			FullName string `json:"full_name"`
			Tap      string `json:"tap"`
			Pinned   bool   `json:"pinned"`
			Versions struct {
				Stable string `json:"stable"`
			} `json:"versions"`
		} `json:"formulae"`
		Casks []struct {
			Token     string `json:"token"`
			FullToken string `json:"full_token"`
			Tap       string `json:"tap"`
			Version   string `json:"version"`
		} `json:"casks"`
	}
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		return p, fmt.Errorf("invalid Homebrew metadata")
	}
	if p.kind == "formula" {
		if len(info.Formulae) != 1 || len(info.Casks) != 0 {
			return p, fmt.Errorf("ambiguous Homebrew formula metadata")
		}
		f := info.Formulae[0]
		if f.Name != p.name || f.Tap != "homebrew/core" || (f.FullName != "" && f.FullName != p.name && f.FullName != "homebrew/core/"+p.name) {
			return p, fmt.Errorf("homebrew formula source requires explicit adoption; no tap trust changes performed")
		}
		p.version, p.pinned = f.Versions.Stable, f.Pinned
	} else {
		if len(info.Casks) != 1 || len(info.Formulae) != 0 {
			return p, fmt.Errorf("ambiguous Homebrew cask metadata")
		}
		c := info.Casks[0]
		if c.Token != p.name || c.Tap != "homebrew/cask" || (c.FullToken != "" && c.FullToken != p.name && c.FullToken != "homebrew/cask/"+p.name) {
			return p, fmt.Errorf("homebrew cask source requires explicit adoption; no tap trust changes performed")
		}
		p.version = c.Version
	}
	if versionInOutput.FindString(p.version) != p.version || p.version == "" {
		return p, fmt.Errorf("homebrew stable version unavailable or not exactly representable")
	}
	return p, nil
}
func (e *Engine) applyBrew(ctx context.Context, p brewPackage, target string, installed bool) error {
	actual, _ := os.UserHomeDir()
	if e.opts.ExplicitHome && filepath.Clean(actual) != filepath.Clean(e.opts.HomeDir) {
		return fmt.Errorf("homebrew is host-owned; use the owning user's home for installation")
	}
	if p.pinned {
		return fmt.Errorf("homebrew package is pinned; reconcile its pin explicitly")
	}
	if target != p.version {
		return fmt.Errorf("requested version %s unavailable in Homebrew (available %s); preserve pin or select an available version", target, p.version)
	}
	action := "install"
	if installed {
		action = "upgrade"
	}
	_, err := e.exec(ctx, command{Path: p.executable, Args: []string{action, "--" + p.kind, p.name}, Env: e.brewEnvironment(), Dir: e.opts.HomeDir})
	return err
}
func (e *Engine) brewBinary(ctx context.Context, en Entry, pin string, op Operation, path string, s binarySpec, r ItemResult) ItemResult {
	p, err := e.brewPackage(ctx, path, s)
	if err != nil {
		r.Status = "deferred-provenance"
		r.Detail = err.Error()
		return r
	}
	r.Latest = p.version
	target := p.version
	if pin != "" {
		target = strings.TrimPrefix(pin, "v")
	}
	if r.Installed == target {
		r.Status = "up-to-date"
		if pin != "" || p.pinned {
			r.Status = "pinned"
		}
		r.Detail = "provider: brew"
		if op != Inspect && !e.opts.DryRun {
			e.record(en.ID, "brew", path, r.Installed, r.Status)
		}
		return r
	}
	if p.pinned || target != p.version {
		r.Status = "deferred-pin"
		r.Detail = fmt.Sprintf("Homebrew pin preserved; requested %s, available %s; reconcile explicitly", target, p.version)
		return r
	}
	if op == Inspect {
		r.Status = "update-available"
		r.Detail = "provider: brew; exact selected package " + p.name
		return r
	}
	if op == Ensure && path != "" {
		r.Status = "installed"
		r.Detail = "Homebrew-owned installation preserved; run dot ai update"
		if !e.opts.DryRun {
			e.record(en.ID, "brew", path, r.Installed, r.Status)
		}
		return r
	}
	actual, _ := os.UserHomeDir()
	if e.opts.ExplicitHome && filepath.Clean(actual) != filepath.Clean(e.opts.HomeDir) {
		r.Status = "deferred-home"
		r.Detail = "Homebrew belongs to the host; rerun for the owning user's home"
		return r
	}
	if e.opts.DryRun {
		r.Status = "planned"
		r.Detail = "brew upgrade --" + p.kind + " " + p.name + " to " + target
		return r
	}
	if prior, ok := e.receipts[en.ID]; ok && prior.Version != "" && prior.Version != r.Installed {
		r.Status = "deferred-local-change"
		r.Detail = "installed version changed outside dot; inspect and re-adopt before updating"
		return r
	}
	// Recheck immediately before mutation; do not silently install a moved target.
	fresh, err := e.brewPackage(ctx, path, s)
	if err != nil || fresh.pinned || fresh.version != target {
		r.Status = "deferred-provider"
		r.Detail = "Homebrew metadata or pin changed before installation; inspect and retry"
		if err != nil {
			r.Detail = err.Error()
		}
		return r
	}
	live := ""
	if path != "" {
		live = drainNote(e.liveProcesses(ctx, en.Binary, path, "", s))
	}
	if err = e.applyBrew(ctx, fresh, target, path != ""); err != nil {
		r.Status = "failed"
		r.Detail = err.Error() + live
		return r
	}
	out, err := e.exec(ctx, command{Path: path, Args: []string{"--version"}, Env: e.environment(), Dir: e.opts.HomeDir, Timeout: 15 * time.Second})
	after := versionInOutput.FindString(out)
	if err != nil || after != target {
		r.Status = "failed"
		r.Detail = "Homebrew returned but selected version was not verified"
		return r
	}
	before := r.Installed
	r.Installed = after
	r.Status = "updated"
	r.Detail = fmt.Sprintf("brew: %s -> %s", before, after) + live
	e.record(en.ID, "brew", path, after, r.Status)
	return r
}
