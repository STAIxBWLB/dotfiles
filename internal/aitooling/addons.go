package aitooling

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/config"
)

func (e *Engine) addon(ctx context.Context, en Entry, s config.AIToolingConfig, op Operation) []ItemResult {
	if en.ID == "gsd" {
		return e.gsd(ctx, s, op)
	}
	var results []ItemResult
	for _, agent := range s.Agents {
		r := ItemResult{ID: en.ID + "/" + agent, Kind: "integration"}
		switch {
		case agent == "claude" || agent == "codex":
			r = e.plugin(ctx, en.ID, agent, s.Pins[en.ID], op)
		case en.ID == "ponytail" && agent == "grok":
			r = e.grokPonytail(ctx, s.Pins[en.ID], op)
		case en.ID == "ponytail" && agent == "opencode":
			r = e.openCodePonytail(ctx, s.Pins[en.ID], op)
		default:
			r.Status = "partial"
			r.Detail = "no verified native adapter; select compatible portable skills through Maru; session capture is not established"
		}
		results = append(results, r)
	}
	if en.ID == "claude-mem" && needsMemoryBridge(s) {
		results = append(results, e.memoryBridge(ctx, s, op))
	}
	if len(results) == 0 {
		results = append(results, ItemResult{ID: en.ID, Kind: "tool", Status: "deferred-no-agent", Detail: "select at least one agent"})
	}
	return results
}

type pluginSpec struct{ selector, market, source, metadata string }

func pluginSpecs() map[string]pluginSpec {
	return map[string]pluginSpec{
		"ponytail":   {selector: "ponytail@ponytail", market: "ponytail", source: "DietrichGebert/ponytail", metadata: "https://raw.githubusercontent.com/DietrichGebert/ponytail/main/.claude-plugin/plugin.json"},
		"claude-mem": {selector: "claude-mem@thedotmack", market: "thedotmack", source: "thedotmack/claude-mem", metadata: "https://registry.npmjs.org/claude-mem/latest"},
		"ocr":        {selector: "open-code-review@open-code-review", market: "open-code-review", source: "alibaba/open-code-review", metadata: "https://raw.githubusercontent.com/alibaba/open-code-review/main/plugins/open-code-review/claude-code/.claude-plugin/plugin.json"},
	}
}
func (e *Engine) plugin(ctx context.Context, id, agent, pin string, op Operation) ItemResult {
	spec := pluginSpecs()[id]
	r := ItemResult{ID: id + "/" + agent, Kind: "integration"}
	if e.pluginOtherSource(ctx, agent, strings.Split(spec.selector, "@")[0], spec.selector) {
		r.Status = "deferred-provenance"
		r.Detail = "same plugin is installed from another source; preserve source and migrate explicitly"
		return r
	}
	installed, installPath := e.pluginVersion(ctx, agent, spec.selector)
	r.Installed = installed
	latest, err := e.latest(ctx, spec.metadata)
	if err != nil {
		r.Status = "unknown"
		r.Detail = "plugin release metadata unavailable"
		return r
	}
	r.Latest = latest
	if pin != "" && strings.TrimPrefix(pin, "v") != r.Installed {
		r.Status = "deferred-pin"
		r.Detail = "native marketplace cannot guarantee exact pin; existing plugin preserved"
		return r
	}
	if pin != "" && strings.TrimPrefix(pin, "v") == r.Installed {
		r.Status = "pinned"
		r.Detail = "explicit plugin version pin preserved"
		return r
	}
	if r.Installed == latest {
		r.Status = "installed"
		r.Detail = "native plugin files installed; hook activation/trust and restart may be pending"
		return r
	}
	if op == Inspect {
		r.Status = "update-available"
		if r.Installed == "" {
			r.Status = "missing"
		}
		return r
	}
	if op == Ensure && r.Installed != "" {
		r.Status = "installed"
		r.Detail = "update available; run dot ai update"
		return r
	}
	if e.opts.DryRun {
		r.Status = "planned"
		r.Detail = "native targeted plugin installation: " + spec.selector
		return r
	}
	if id == "claude-mem" {
		// Native setup/update may start or restart the singleton observer. An agent
		// selection is not permission to interrupt active captures or cooldowns.
		if e.memoryWorkerActive(ctx) {
			r.Status = "deferred-active-worker"
			r.Detail = "memory worker/service state present; complete native update in an explicit maintenance window"
			return r
		}
		for _, dep := range []string{"bun", "uv"} {
			if e.find(dep) == "" {
				r.Status = "deferred-prerequisite"
				r.Detail = dep + " is required; dot will not auto-install unselected runtimes"
				return r
			}
		}
	}
	if installPath != "" && !e.pluginIntegrity(ctx, r.ID, agent, spec.selector, installPath) {
		r.Status = "deferred-local-change"
		r.Detail = "plugin installation has local changes or unreadable provenance"
		return r
	}
	bin := e.find(agent)
	if bin == "" {
		r.Status = "deferred-prerequisite"
		r.Detail = agent + " CLI missing"
		return r
	}
	marketPath := filepath.Join(e.profile(agent), "plugins", "marketplaces", spec.market)
	if pathExists(marketPath) {
		if e.localPluginChanges(ctx, marketPath) {
			r.Status = "deferred-local-change"
			r.Detail = "marketplace source has local changes"
			return r
		}
		// Do not reinterpret a locally registered source as the official source.
		out, err := e.exec(ctx, command{Path: e.find("git"), Args: []string{"-C", marketPath, "remote", "get-url", "origin"}, Env: e.environment()})
		if err != nil || !officialGitRemote(strings.TrimSpace(out), spec.source) {
			r.Status = "deferred-provenance"
			r.Detail = "existing marketplace source is not the verified official Git source"
			return r
		}
	} else {
		if _, err = e.run(ctx, bin, "plugin", "marketplace", "add", spec.source); err != nil {
			r.Status = "failed"
			r.Detail = err.Error()
			return r
		}
	}
	verb := "update"
	if agent == "codex" {
		verb = "upgrade"
	}
	if _, err = e.run(ctx, bin, "plugin", "marketplace", verb, spec.market); err != nil {
		r.Status = "failed"
		r.Detail = err.Error()
		return r
	}
	if candidate := e.pluginCandidateVersion(ctx, agent, spec); candidate != latest || !stableVersion.MatchString(candidate) {
		r.Status = "deferred-metadata"
		r.Detail = "refreshed native marketplace cannot verify the resolved stable plugin version; installation deferred"
		return r
	}
	args := []string{"plugin", "install", spec.selector, "--scope", "user"}
	if r.Installed != "" {
		args = []string{"plugin", "update", spec.selector, "--scope", "user"}
	}
	if agent == "codex" {
		args = []string{"plugin", "add", spec.selector}
	}
	if _, err = e.run(ctx, bin, args...); err != nil {
		r.Status = "failed"
		r.Detail = err.Error()
		return r
	}
	after, afterPath := e.pluginVersion(ctx, agent, spec.selector)
	if after == "" || after != latest {
		r.Status = "partial"
		r.Detail = "native installer completed; installed target version could not be verified"
		return r
	}
	r.Installed = after
	r.Status = "pending-trust"
	r.Detail = "plugin files verified; native hook trust/restart and runtime readiness must be verified separately"
	e.record(r.ID, "native-plugin", afterPath, after, r.Status)
	if digest, err := pluginDigest(afterPath); err == nil {
		rec := e.receipts[r.ID]
		rec.Integrity = digest
		e.receipts[r.ID] = rec
	}
	return r
}
func officialGitRemote(raw, repo string) bool {
	return raw == "https://github.com/"+repo || raw == "https://github.com/"+repo+".git" || raw == "git@github.com:"+repo+".git"
}
func (e *Engine) localPluginChanges(ctx context.Context, path string) bool {
	git := e.find("git")
	if git == "" {
		return true
	}
	if !pathExists(filepath.Join(path, ".git")) {
		return false
	}
	out, err := e.exec(ctx, command{Path: git, Args: []string{"-C", path, "status", "--porcelain", "--untracked-files=no"}, Env: e.environment()})
	return err != nil || strings.TrimSpace(out) != ""
}
func (e *Engine) memoryWorkerActive(ctx context.Context) bool {
	data := filepath.Join(e.opts.HomeDir, ".claude-mem")
	if !e.opts.ExplicitHome && filepath.IsAbs(os.Getenv("CLAUDE_MEM_DATA_DIR")) {
		data = os.Getenv("CLAUDE_MEM_DATA_DIR")
	}
	for _, p := range []string{"worker.pid", "worker.lock", "worker.port", "worker-service.pid", "supervisor.json", "observer-health.json", "quota-cooldown.json"} {
		if pathExists(filepath.Join(data, p)) {
			return true
		}
	}
	if pathExists(filepath.Join(e.opts.HomeDir, "Library", "LaunchAgents", "com.claude-mem.worker.plist")) {
		return true
	}
	// The current native supervisor does not necessarily leave a PID file.
	// Inspect command names read-only, and fail closed if process discovery is
	// unavailable. Never print the process list: unrelated argv may be private.
	out, err := e.exec(ctx, command{Path: "/bin/ps", Args: []string{"-axo", "command="}, Env: e.environment(), Timeout: 5 * time.Second})
	if err != nil {
		return true
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "worker-service.cjs") || strings.Contains(line, "claude-mem") && strings.Contains(line, "supervisor") {
			return true
		}
	}
	return false
}

func (e *Engine) pluginVersion(ctx context.Context, agent, selector string) (string, string) {
	if agent == "claude" {
		var doc struct {
			Plugins map[string][]struct{ Scope, Version, InstallPath string }
		}
		b, err := os.ReadFile(filepath.Join(e.profile(agent), "plugins", "installed_plugins.json"))
		if err != nil || json.Unmarshal(b, &doc) != nil {
			return "", ""
		}
		for _, p := range doc.Plugins[selector] {
			if p.Scope == "user" {
				return p.Version, p.InstallPath
			}
		}
		return "", ""
	}

	bin := e.find("codex")
	if bin == "" {
		return "", ""
	}
	out, err := e.exec(ctx, command{Path: bin, Args: []string{"plugin", "list", "--json"}, Env: e.environment(), Dir: e.opts.HomeDir})
	var doc struct {
		Installed []struct {
			PluginID      string `json:"pluginId"`
			Name, Version string
			Installed     bool
			Source        struct{ Path string }
		}
	}
	if err != nil || json.Unmarshal([]byte(out), &doc) != nil {
		return "", ""
	}
	for _, p := range doc.Installed {
		if p.Installed && p.PluginID == selector {
			return p.Version, p.Source.Path
		}
	}
	return "", ""

}
func (e *Engine) openCodePonytail(ctx context.Context, pin string, op Operation) ItemResult {
	r := ItemResult{ID: "ponytail/opencode", Kind: "integration"}
	latest, err := e.latest(ctx, "https://registry.npmjs.org/@dietrichgebert/ponytail/latest")
	if err != nil {
		r.Status = "unknown"
		r.Detail = "npm metadata unavailable"
		return r
	}
	r.Latest = latest
	target := latest
	if pin != "" {
		target = strings.TrimPrefix(pin, "v")
	}
	if !e.opts.ExplicitHome && (os.Getenv("OPENCODE_CONFIG") != "" || os.Getenv("OPENCODE_CONFIG_CONTENT") != "") {
		r.Status = "deferred-local-config"
		r.Detail = "OpenCode custom configuration override is active; preserve it and configure ponytail natively"
		return r
	}
	p := filepath.Join(e.profile("opencode"), "opencode.json")
	if pathExists(filepath.Join(e.profile("opencode"), "opencode.jsonc")) {
		r.Status = "deferred-local-config"
		r.Detail = "JSONC configuration requires an explicit native configuration edit; preserved"
		return r
	}
	if st, err := os.Lstat(p); err == nil && st.Mode()&os.ModeSymlink != 0 {
		r.Status = "deferred-local-config"
		r.Detail = "OpenCode configuration is a symlink; preserve its external ownership"
		return r
	}
	doc := map[string]json.RawMessage{}
	before, err := os.ReadFile(p)
	if err == nil {
		if json.Unmarshal(before, &doc) != nil {
			r.Status = "deferred-local-config"
			r.Detail = "OpenCode config is not JSON; preserved"
			return r
		}
	} else if !os.IsNotExist(err) {
		r.Status = "failed"
		r.Detail = "cannot read OpenCode configuration"
		return r
	}
	var plugins []string
	if b, ok := doc["plugin"]; ok {
		if json.Unmarshal(b, &plugins) != nil {
			r.Status = "deferred-local-config"
			r.Detail = "unknown plugin configuration shape; preserved"
			return r
		}
	}
	value := "@dietrichgebert/ponytail@" + target
	index := -1
	for i, v := range plugins {
		if v == "@dietrichgebert/ponytail" || strings.HasPrefix(v, "@dietrichgebert/ponytail@") {
			index = i
			r.Installed = strings.TrimPrefix(v, "@dietrichgebert/ponytail@")
			if v == value {
				if installed := e.openCodePluginVersion(); installed == target {
					r.Status = "installed"
					r.Installed = installed
					r.Detail = "exact native package installation verified"
					return r
				}
				if op == Inspect || e.opts.DryRun {
					r.Status = "pending-runtime"
					r.Detail = "exact package configured; native installation pending"
					return r
				}
			}
		}
	}
	if index >= 0 && plugins[index] != "@dietrichgebert/ponytail" && plugins[index] != value && pin == "" {
		prior, owned := e.receipts[r.ID]
		if !owned || prior.Version != r.Installed {
			r.Status = "pinned"
			r.Detail = "existing exact OpenCode plugin reference preserved"
			return r
		}
	}
	if op == Inspect {
		r.Status = "missing"
		if index >= 0 {
			r.Status = "update-available"
		}
		return r
	}
	if e.opts.DryRun {
		r.Status = "planned"
		r.Detail = "add exact native OpenCode npm plugin reference"
		return r
	}
	bin := e.find("opencode")
	if bin == "" {
		r.Status = "deferred-prerequisite"
		r.Detail = "OpenCode CLI missing"
		return r
	}
	if cache, version := e.openCodePluginCache(); cache != "" && !e.ponyCacheClean(ctx, cache, version) {
		r.Status = "deferred-local-change"
		r.Detail = "native OpenCode plugin cache differs from its managed digest or verified npm distribution"
		return r
	}
	if before != nil {
		backup, backupErr := os.CreateTemp(filepath.Dir(p), "opencode.json.dot-backup-*")
		if backupErr != nil {
			r.Status = "failed"
			r.Detail = "cannot back up native OpenCode configuration"
			return r
		}
		_, backupErr = backup.Write(before)
		closeErr := backup.Close()
		if backupErr != nil || closeErr != nil {
			r.Status = "failed"
			r.Detail = "cannot finish native OpenCode configuration backup"
			return r
		}
	}
	args := []string{"plugin", value, "--global", "--pure"}
	if index >= 0 {
		args = append(args, "--force")
	}
	if _, err = e.run(ctx, bin, args...); err != nil {
		r.Status = "failed"
		r.Detail = err.Error()
		return r
	}
	if version := e.openCodePluginVersion(); version == target && e.openCodeReference(target) {
		r.Installed = version
		r.Status = "installed"
		r.Detail = "native OpenCode plugin package verified; activation occurs in the next native session"
	} else {
		r.Status = "pending-runtime"
		r.Detail = "native plugin installer returned; installed package version not yet verifiable"
	}
	if r.Status == "installed" {
		e.record(r.ID, "opencode-npm", p, target, r.Status)
		if cache, _ := e.openCodePluginCache(); cache != "" {
			if digest, err := pluginDigest(cache); err == nil {
				rec := e.receipts[r.ID]
				rec.Integrity = digest
				e.receipts[r.ID] = rec
			}
		}
	}
	return r
}
func (e *Engine) openCodePluginVersion() string {
	_, version := e.openCodePluginCache()
	return version
}
func (e *Engine) openCodePluginCache() (string, string) {
	cache := filepath.Join(e.opts.HomeDir, ".cache")
	if !e.opts.ExplicitHome && filepath.IsAbs(os.Getenv("XDG_CACHE_HOME")) {
		cache = os.Getenv("XDG_CACHE_HOME")
	}
	for _, root := range []string{e.profile("opencode"), filepath.Join(cache, "opencode")} {
		p := filepath.Join(root, "node_modules", "@dietrichgebert", "ponytail", "package.json")
		var doc struct{ Name, Version string }
		b, err := os.ReadFile(p)
		if err == nil && json.Unmarshal(b, &doc) == nil && doc.Name == "@dietrichgebert/ponytail" && stableVersion.MatchString(doc.Version) {
			return filepath.Dir(p), doc.Version
		}
	}
	return "", ""
}
func (e *Engine) openCodeReference(version string) bool {
	var doc struct{ Plugin []string }
	b, err := os.ReadFile(filepath.Join(e.profile("opencode"), "opencode.json"))
	return err == nil && json.Unmarshal(b, &doc) == nil && slices.Contains(doc.Plugin, "@dietrichgebert/ponytail@"+version)
}

func (e *Engine) gsd(ctx context.Context, s config.AIToolingConfig, op Operation) []ItemResult {
	latest, err := e.latest(ctx, "https://registry.npmjs.org/@opengsd/gsd-core/latest")
	if err != nil {
		return []ItemResult{{ID: "gsd/core", Kind: "tool", Status: "unknown", Detail: "GSD core metadata unavailable"}}
	}
	target := latest
	if s.Pins["gsd"] != "" {
		target = strings.TrimPrefix(s.Pins["gsd"], "v")
	}
	var results []ItemResult
	seen := map[string]bool{}
	for _, agent := range s.Agents {
		r := ItemResult{ID: "gsd/core/" + agent, Kind: "integration", Latest: latest}
		if agent == "grok" {
			r.Status = "partial"
			r.Detail = "GSD does not provide a verified Grok adapter"
			results = append(results, r)
			continue
		}
		root := e.profile(agent)
		if seen[root] {
			continue
		}
		seen[root] = true
		version, clean := e.gsdManifest(ctx, root, agent)
		r.Installed = version
		switch {
		case version == target && clean:
			r.Status = "installed"
		case op == Inspect:
			r.Status = "missing"
			if version != "" {
				r.Status = "update-available"
			}
		case e.opts.DryRun:
			r.Status = "planned"
			r.Detail = "native GSD installer with explicit runtime/config directory"
		case !clean:
			r.Status = "deferred-local-change"
			r.Detail = "GSD manifest files have local edits; retain patches through native reapply workflow"
		case e.find("npx") == "":
			r.Status = "deferred-prerequisite"
			r.Detail = "Node.js/npm is required"
		case op == Ensure && version != "" && s.Pins["gsd"] == "":
			r.Status = "installed"
			r.Detail = "update available; run dot ai update"
		default:
			flag := agent
			if agent == "kimi" {
				flag = "kimi-code"
			}
			_, installErr := e.run(ctx, e.find("npx"), "--yes", "--package", "@opengsd/gsd-core@"+target, "gsd-core", "--"+flag, "--global", "--config-dir", root, "--no-legacy-cleanup")
			after, _ := e.gsdManifest(ctx, root, agent)
			if installErr != nil {
				r.Status = "failed"
				r.Detail = installErr.Error()
			} else if after != target {
				r.Status = "partial"
				r.Detail = "native install returned; requested manifest version not verified"
			} else {
				r.Installed = after
				r.Status = "installed"
				e.record(r.ID, "gsd-core", root, after, r.Status)
			}
		}
		results = append(results, r)
	}
	// GSD-pi is an independently versioned component, not the unrelated pi CLI.
	pi := ItemResult{ID: "gsd/pi", Kind: "tool"}
	pi.Latest, _ = e.latest(ctx, "https://registry.npmjs.org/@opengsd/gsd-pi/latest")
	if pi.Latest == "" {
		pi.Status = "unknown"
		pi.Detail = "GSD-pi metadata unavailable"
	} else {
		pi = e.gsdPi(ctx, s.Pins["gsd-pi"], pi, op)
	}
	return append(results, pi)
}
func gsdManifestAt(root, skillsRoot string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(root, "gsd-file-manifest.json"))
	if os.IsNotExist(err) {
		if pathExists(filepath.Join(root, "gsd-core")) {
			return "", false
		}
		return "", true
	}
	if err != nil {
		return "", false
	}
	var doc struct {
		Version string            `json:"version"`
		Files   map[string]string `json:"files"`
	}
	if json.Unmarshal(b, &doc) != nil {
		return "", false
	}
	for rel, want := range doc.Files {
		if filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(filepath.Clean(rel), ".."+string(os.PathSeparator)) {
			return doc.Version, false
		}
		path := filepath.Join(root, rel)
		if skillsRoot != "" && strings.HasPrefix(rel, "skills/") {
			path = filepath.Join(skillsRoot, strings.TrimPrefix(rel, "skills/"))
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return doc.Version, false
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != want {
			return doc.Version, false
		}
	}
	return doc.Version, true
}
func (e *Engine) gsdPi(ctx context.Context, pin string, r ItemResult, op Operation) ItemResult {
	path := e.find("gsd")
	if path != "" {
		out, err := e.run(ctx, path, "--version")
		if err != nil {
			r.Status = "unknown"
			return r
		}
		r.Installed = versionInOutput.FindString(out)
	}
	target := r.Latest
	if pin != "" {
		target = strings.TrimPrefix(pin, "v")
	}
	if r.Installed == target {
		r.Status = "installed"
		return r
	}
	if op == Inspect {
		r.Status = "missing"
		if path != "" {
			r.Status = "update-available"
		}
		return r
	}
	if e.opts.DryRun {
		r.Status = "planned"
		return r
	}
	spec := binarySpec{npm: "@opengsd/gsd-pi"}
	provider, prefix := e.provenance("gsd-pi", path, spec)
	if provider != "npm" {
		r.Status = "deferred-provenance"
		r.Detail = "existing gsd binary is not an adopted GSD-pi npm installation"
		return r
	}
	if op == Ensure && path != "" && pin == "" {
		r.Status = "installed"
		return r
	}
	requestedHome := e.opts.HomeDir
	if canonical, err := filepath.EvalSymlinks(requestedHome); err == nil {
		requestedHome = canonical
	}
	if e.opts.ExplicitHome && !strings.HasPrefix(filepath.Clean(prefix), filepath.Clean(requestedHome)+string(os.PathSeparator)) {
		r.Status = "deferred-home"
		r.Detail = "existing npm prefix belongs outside requested home; preserve it"
		return r
	}
	if err := e.installBinary(ctx, "gsd-pi", target, path, provider, prefix, spec); err != nil {
		r.Status = "failed"
		r.Detail = err.Error()
		return r
	}
	out, err := e.run(ctx, e.find("gsd"), "--version")
	after := versionInOutput.FindString(out)
	if err != nil || after != target {
		r.Status = "failed"
		r.Detail = "GSD-pi version verification failed"
		return r
	}
	r.Installed = after
	r.Status = "installed"
	e.record("gsd-pi", "npm", e.find("gsd"), after, r.Status)
	return r
}

func (e *Engine) skills(ctx context.Context, s config.AIToolingConfig, op Operation) ItemResult {
	r := ItemResult{ID: "shared-skills", Kind: "skills"}
	bin := e.find("maru")
	if bin == "" {
		r.Status = "deferred-maru-capability"
		r.Detail = "Maru with selected skill federation support is required"
		return r
	}
	out, err := e.run(ctx, bin, "skills", "capabilities", "--json")
	var cap struct {
		SchemaVersion int      `json:"schemaVersion"`
		Targets       []string `json:"targets"`
		SelectedSync  bool     `json:"selectedSync"`
		List          bool     `json:"list"`
	}
	if err != nil || json.Unmarshal([]byte(out), &cap) != nil || cap.SchemaVersion != 1 || !cap.SelectedSync || !cap.List {
		r.Status = "deferred-maru-capability"
		r.Detail = "installed Maru lacks selected federation contract"
		return r
	}
	for _, a := range s.Agents {
		if !slices.Contains(cap.Targets, a) {
			r.Status = "deferred-maru-capability"
			r.Detail = "Maru does not support selected target " + a
			return r
		}
	}
	if len(s.Agents) == 0 {
		r.Status = "deferred-no-agent"
		r.Detail = "no selected agent destinations"
		return r
	}
	mode := "--check"
	if op != Inspect && !e.opts.DryRun {
		mode = "--apply"
	}
	args := []string{"skills", "sync", mode, "--tools", strings.Join(s.Agents, ","), "--skills", strings.Join(s.Skills, ","), "--json"}
	out, err = e.run(ctx, bin, args...)
	if err != nil {
		r.Status = "failed"
		r.Detail = "Maru selected skill federation failed; inspect maru skills sync --check"
		return r
	}
	var report skillSyncReport
	if err = json.Unmarshal([]byte(out), &report); err != nil || !report.valid(s, mode == "--apply") {
		r.Status = "unknown"
		r.Detail = "Maru returned an incomplete or incompatible selected sync report"
		return r
	}
	for _, action := range report.Actions {
		if action.Action != "link-canonical" && action.Action != "link-tool" && action.Action != "record-install" {
			r.Status = "deferred-skill-conflict"
			r.Detail = "Maru reported a conflicting or unsupported selected skill action"
			return r
		}
	}
	if mode == "--check" {
		r.Status = "up-to-date"
		if len(report.Actions) > 0 {
			r.Status = "missing"
		}
		if e.opts.DryRun {
			r.Status = "planned"
		}
		r.Detail = "selected Maru skill plan verified"
		return r
	}
	args[2] = "--check"
	out, err = e.run(ctx, bin, args...)
	var after skillSyncReport
	if err != nil || json.Unmarshal([]byte(out), &after) != nil || !after.valid(s, false) || len(after.Actions) > 0 {
		r.Status = "partial"
		r.Detail = "Maru applied changes but selected skill readiness did not verify"
		return r
	}
	r.Status = "applied"
	r.Detail = "Maru selected skill sync verified with a follow-up check"
	return r
}

type skillSyncReport struct {
	Applied         *bool    `json:"applied"`
	Tools           []string `json:"tools"`
	DesiredSkills   *int     `json:"desiredSkills"`
	DesiredInstalls *int     `json:"desiredInstalls"`
	Actions         []struct {
		Action string `json:"action"`
	} `json:"actions"`
}

func (r skillSyncReport) valid(s config.AIToolingConfig, applied bool) bool {
	if r.Applied == nil || *r.Applied != applied || r.DesiredSkills == nil || *r.DesiredSkills != len(s.Skills) || r.DesiredInstalls == nil || *r.DesiredInstalls != len(s.Skills)*len(s.Agents) || len(r.Tools) != len(s.Agents) {
		return false
	}
	for _, a := range s.Agents {
		if !slices.Contains(r.Tools, a) {
			return false
		}
	}
	return true
}
func (e *Engine) gsdManifest(ctx context.Context, root, agent string) (string, bool) {
	skillsRoot := filepath.Join(root, "skills")
	// Query the installed native layout resolver; Codex's shared skills root is
	// deliberately different from CODEX_HOME, including Orca profiles.
	if agent == "codex" {
		node := e.find("node")
		script := filepath.Join(root, "gsd-core", "bin", "gsd-tools.cjs")
		if node != "" && pathExists(script) {
			out, err := e.run(ctx, node, script, "query", "skills-root", "codex", "--raw")
			if err != nil {
				return "", false
			}
			skillsRoot = strings.TrimSpace(out)
			if !filepath.IsAbs(skillsRoot) {
				return "", false
			}
		} else if pathExists(filepath.Join(root, "gsd-file-manifest.json")) {
			return "", false
		}
	}
	return gsdManifestAt(root, skillsRoot)
}

func (e *Engine) pluginOtherSource(ctx context.Context, agent, name, selector string) bool {
	if agent == "claude" {
		var doc struct{ Plugins map[string]json.RawMessage }
		b, err := os.ReadFile(filepath.Join(e.profile(agent), "plugins", "installed_plugins.json"))
		if err != nil {
			return false
		}
		if json.Unmarshal(b, &doc) != nil {
			return true
		}
		for id := range doc.Plugins {
			if strings.HasPrefix(id, name+"@") && id != selector {
				return true
			}
		}
		return false
	}
	bin := e.find(agent)
	if bin == "" {
		return false
	}
	out, err := e.run(ctx, bin, "plugin", "list", "--json")
	if err != nil {
		return true
	}
	var doc struct {
		Installed []struct {
			PluginID string `json:"pluginId"`
			Name     string
		}
	}
	if json.Unmarshal([]byte(out), &doc) != nil {
		return true
	}
	for _, p := range doc.Installed {
		if p.Name == name && p.PluginID != selector {
			return true
		}
	}
	return false
}

func (e *Engine) pluginCandidateVersion(ctx context.Context, agent string, spec pluginSpec) string {
	if agent == "codex" {
		bin := e.find(agent)
		if bin == "" {
			return ""
		}
		out, err := e.run(ctx, bin, "plugin", "list", "--available", "--json")
		if err != nil {
			return ""
		}
		type plugin struct {
			PluginID string `json:"pluginId"`
			Version  string
		}
		var doc struct{ Installed, Available []plugin }
		if json.Unmarshal([]byte(out), &doc) != nil {
			return ""
		}
		for _, p := range append(doc.Available, doc.Installed...) {
			if p.PluginID == spec.selector {
				return p.Version
			}
		}
		return ""
	}
	root := filepath.Join(e.profile(agent), "plugins", "marketplaces", spec.market)
	var doc struct {
		Plugins []struct {
			Name, Version string
			Source        json.RawMessage
		}
	}
	b, err := os.ReadFile(filepath.Join(root, ".claude-plugin", "marketplace.json"))
	if err != nil || json.Unmarshal(b, &doc) != nil {
		return ""
	}
	for _, p := range doc.Plugins {
		if p.Name != strings.Split(spec.selector, "@")[0] {
			continue
		}
		var source string
		if json.Unmarshal(p.Source, &source) != nil || filepath.IsAbs(source) {
			return ""
		}
		source = filepath.Clean(source)
		if source == ".." || strings.HasPrefix(source, "../") {
			return ""
		}
		var manifest struct{ Version string }
		b, err = os.ReadFile(filepath.Join(root, source, ".claude-plugin", "plugin.json"))
		if err == nil && json.Unmarshal(b, &manifest) == nil && manifest.Version != "" {
			return manifest.Version
		}
		return p.Version
	}
	return ""
}
