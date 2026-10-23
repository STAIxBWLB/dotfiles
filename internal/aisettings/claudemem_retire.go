package aisettings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// RetireStatus is the state of one retirement step.
type RetireStatus string

const (
	RetireWould   RetireStatus = "would"   // dry run: this step would run
	RetireDone    RetireStatus = "done"    // apply: ran and succeeded
	RetireAbsent  RetireStatus = "absent"  // nothing to do on this machine
	RetireFailed  RetireStatus = "failed"  // apply: ran and failed
	RetireUnknown RetireStatus = "unknown" // the evidence could not be read: never treated as absent
)

// RetireStep is one thing the retirement touches.
type RetireStep struct {
	ID     string
	Kind   string // plugin, marketplace, mcp, launchd, archive
	Target string
	Action string
	Status RetireStatus
	Detail string
	// Source is the file whose contents could not be read, set only when Status is unknown.
	Source  string
	command []string
}

// RetireReadBack is one residue check run at the end.
type RetireReadBack struct {
	Check string
	Clean bool
	Found string
}

// RetireResult is the whole report: the steps, the edits for files dot does
// not own, and the final read-back.
type RetireResult struct {
	Apply    bool
	Steps    []RetireStep
	Manual   []string
	ReadBack []RetireReadBack
	Archive  string
	// Blocked is why --apply refused to start, empty otherwise. Nothing was changed.
	Blocked string
}

// Clean reports whether the read-back found no claude-mem residue.
func (r RetireResult) Clean() bool {
	for _, item := range r.ReadBack {
		if !item.Clean {
			return false
		}
	}
	return true
}

// Runner runs a native command in the given environment and returns its combined
// output. It is the one seam through which retirement mutates anything outside
// dot's own files, so a test proves the dry run by counting calls and proves the
// apply by reading the environment each call received.
type Runner func(ctx context.Context, env []string, name string, args ...string) (string, error)

// RetireOptions configures the retirement.
type RetireOptions struct {
	// HomeDir is the home every path is read from and written under. Nothing here
	// consults AMEM_HOME, AGENTHUB_HOME, CODEX_HOME or CLAUDE_MEM_DATA_DIR from the
	// ambient environment: an explicit home ignores ambient overrides
	// (docs/BOUNDARIES.md), and the person's own home resolves them in Ambient.
	HomeDir string
	// ExplicitHome says HomeDir came from --home. Apply is refused then: the native
	// CLIs and launchd act on the invoking user's real state, so a scratch home can
	// be planned and read back but never applied.
	ExplicitHome bool
	// Ambient is the environment of the invoking user. Consulted only when
	// ExplicitHome is false. Nil means os.Environ.
	Ambient []string
	// Apply runs the steps. The default is a dry run that writes nothing.
	Apply bool
	// ImportConfirmed is the person's statement that the claude-mem import into
	// amem has completed. Apply refuses without it and without import evidence.
	ImportConfirmed bool
	Now             func() time.Time
	Run             Runner
	// Ps lists process command lines; PortOpen reports a listening local port.
	Ps       func(ctx context.Context) (string, error)
	PortOpen func(port int) bool
	GOOS     string
	// KimiHome is an explicitly adopted native Kimi Code home.
	KimiHome string
}

const (
	claudeMemPluginID       = "claude-mem@thedotmack"
	claudeMemMarketplace    = "thedotmack"
	codexClaudeMemMarket    = "claude-mem-local"
	claudeMemBridgeLabel    = "com.dotfiles.claude-mem-bridge"
	claudeMemSyncLabel      = "com.dotfiles.claude-mem-sync"
	claudeMemWorkerLabel    = "com.claude-mem.worker"
	claudeMemDefaultWorkerP = 37777
)

func defaultRunner(ctx context.Context, env []string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func defaultPs(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "/bin/ps", "-axo", "command=").Output()
	return string(out), err
}

func defaultPortOpen(port int) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func (o *RetireOptions) defaults() {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Run == nil {
		o.Run = defaultRunner
	}
	if o.Ps == nil {
		o.Ps = defaultPs
	}
	if o.PortOpen == nil {
		o.PortOpen = defaultPortOpen
	}
	if o.GOOS == "" {
		o.GOOS = runtime.GOOS
	}
	if o.Ambient == nil {
		o.Ambient = os.Environ()
	}
}

func ambientValue(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if k, v, ok := strings.Cut(env[i], "="); ok && k == key {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// ambient returns an environment variable only for the person's own home. Under an explicit
// home it is always empty, so no path below can come from the invoking shell.
func (o RetireOptions) ambient(key string) string {
	if o.ExplicitHome {
		return ""
	}
	return ambientValue(o.Ambient, key)
}

func (o RetireOptions) claudeDir() string { return filepath.Join(o.HomeDir, ".claude") }
func (o RetireOptions) agentsDir() string { return filepath.Join(o.HomeDir, "Library", "LaunchAgents") }

// codexHome is $CODEX_HOME (own home only, ~ expanded), else ~/.codex.
func (o RetireOptions) codexHome() string {
	raw := o.ambient("CODEX_HOME")
	if raw == "" {
		return filepath.Join(o.HomeDir, ".codex")
	}
	if raw == "~" || strings.HasPrefix(raw, "~/") {
		raw = filepath.Join(o.HomeDir, strings.TrimPrefix(raw, "~"))
	}
	raw = filepath.Clean(raw)
	if resolved, err := filepath.EvalSymlinks(raw); err == nil {
		return resolved
	}
	return raw
}

func (o RetireOptions) codexConfig() string { return filepath.Join(o.codexHome(), "config.toml") }

// dataDir is ~/.claude-mem, or $CLAUDE_MEM_DATA_DIR when the person's own shell sets it
// (an absolute path, as the add-on engine on main honoured).
func (o RetireOptions) dataDir() string {
	if v := o.ambient("CLAUDE_MEM_DATA_DIR"); filepath.IsAbs(v) {
		return filepath.Clean(v)
	}
	return filepath.Join(o.HomeDir, ".claude-mem")
}

// archivePath is the dated destination the data directory is moved to, never a
// deletion. A name that already exists (checked with Lstat, so a dangling
// symlink counts) is skipped with a numeric suffix.
func (o RetireOptions) archivePath() string {
	base := o.dataDir() + ".archive-" + o.Now().Format("20060102")
	path := base
	for i := 2; ; i++ {
		if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
			return path
		}
		path = base + "-" + strconv.Itoa(i)
	}
}

// amemHome mirrors amem's own resolution: AMEM_HOME, else AGENTHUB_HOME/memory, else
// ~/.agenthub/memory. The first two come from the ambient environment of the person's own
// home only; an explicit home never reads them.
func (o RetireOptions) amemHome() string {
	if v := o.ambient("AMEM_HOME"); v != "" {
		return v
	}
	if v := o.ambient("AGENTHUB_HOME"); v != "" {
		return filepath.Join(v, "memory")
	}
	return filepath.Join(o.HomeDir, ".agenthub", "memory")
}

// nativeEnv is the environment every native command runs with. It starts from the
// invoking environment minus every variable that names a memory or agent home, then pins
// HOME. For the person's own home the ambient CODEX_HOME, AMEM_HOME and AGENTHUB_HOME are kept,
// because those are what select the state the CLIs act on; for an explicit home they are
// replaced by values derived from it (apply is refused there, so this serves tests and
// any future opt-in, and proves no ambient home can leak into a native call).
func (o RetireOptions) nativeEnv() []string {
	drop := map[string]bool{"HOME": true, "CODEX_HOME": true, "CLAUDE_CONFIG_DIR": true, "AMEM_HOME": true, "AGENTHUB_HOME": true, "CLAUDE_MEM_DATA_DIR": true}
	var env []string
	for _, kv := range o.Ambient {
		k, _, _ := strings.Cut(kv, "=")
		if o.ExplicitHome && drop[k] {
			continue
		}
		if k == "HOME" {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "HOME="+o.HomeDir)
	if o.ExplicitHome {
		env = append(env, "CODEX_HOME="+o.codexHome(), "AMEM_HOME="+o.amemHome())
	}
	return env
}

// ImportEvidence counts the amem import cursors that hold a positive row id. `amem import
// claude-mem` keeps one cursor file per curated store (<store>/import-state.json), keyed
// by a hash of the source, alias and project, which dot cannot recompute: this proves an
// import ran, not that it covered every project, and the person's confirmation covers the
// rest. A shared-v1 home keeps it at the home itself, a generation home under
// generations/<id>, a split home under projects/<hash> and user. A cursor of 0 means
// nothing was handled and is not evidence.
func (o RetireOptions) ImportEvidence() int {
	home := o.amemHome()
	var candidates []string
	candidates = append(candidates, filepath.Join(home, "import-state.json"), filepath.Join(home, "user", "import-state.json"))
	for _, pattern := range []string{"projects/*/import-state.json", "generations/*/import-state.json", "generations/*/projects/*/import-state.json", "generations/*/user/import-state.json"} {
		matches, _ := filepath.Glob(filepath.Join(home, pattern))
		candidates = append(candidates, matches...)
	}
	n := 0
	for _, path := range candidates {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var cursors map[string]json.Number
		if json.Unmarshal(raw, &cursors) != nil {
			continue
		}
		for _, value := range cursors {
			if id, err := value.Int64(); err == nil && id > 0 {
				n++
				break
			}
		}
	}
	return n
}

// RetireClaudeMem plans, and with Apply runs, the retirement of claude-mem:
// native plugin removal for Claude and Codex, MCP entries of the other agents,
// both dotfiles launchd agents, and a dated archive of the data directory. It ends
// with a read-back that no claude-mem process, port, hook or launchd agent
// remains. It never deletes the data directory and never edits a file dot does
// not own; those edits are returned in Manual.
//
// Everything that decides whether it may start is checked before the first step, so a
// refusal leaves the machine exactly as it was: an explicit --home, a missing import, a
// running claude-mem process, an unreadable or symlinked data directory, and any evidence
// that could not be read.
func RetireClaudeMem(ctx context.Context, opts RetireOptions) (RetireResult, error) {
	opts.defaults()
	if opts.HomeDir == "" || !filepath.IsAbs(opts.HomeDir) {
		return RetireResult{}, fmt.Errorf("retire needs an absolute home directory")
	}
	result := RetireResult{Apply: opts.Apply}
	result.Steps = opts.plan()
	result.Manual = opts.manualEdits()
	if opts.Apply {
		if blocked := opts.gate(ctx, result.Steps); blocked != "" {
			result.Blocked = blocked
			markWould(result.Steps)
			result.ReadBack = opts.readBack(ctx)
			return result, nil
		}
		opts.execute(ctx, &result)
		// The native commands rewrite the files whose lines the person must edit; read them again.
		result.Manual = opts.manualEdits()
		result.ReadBack = opts.readBack(ctx)
		return result, nil
	}
	markWould(result.Steps)
	result.ReadBack = opts.readBack(ctx)
	return result, nil
}

func markWould(steps []RetireStep) {
	for i := range steps {
		if steps[i].Status == "" {
			steps[i].Status = RetireWould
		}
	}
}

// gate is why apply must not start. It returns "" only when every precondition holds, and it
// runs before any step, so a refusal changes nothing.
func (o RetireOptions) gate(ctx context.Context, steps []RetireStep) string {
	if o.ExplicitHome {
		return "refusing to apply under --home: the native claude and codex CLIs and launchd act on the invoking user's real state, not on " + o.HomeDir + "; run without --home to retire the real machine, or use the dry run to plan against this home"
	}
	if !o.ImportConfirmed {
		return "refusing to retire: the import into amem is not confirmed; run `amem import claude-mem`, check it, then pass --import-complete"
	}
	if o.ImportEvidence() == 0 {
		return "refusing to retire: no amem import cursor with a handled row under " + o.amemHome() + " (shared, generation and split layouts are checked); run `amem import claude-mem` first"
	}
	if live := o.runningWorker(ctx); live != "" {
		return "refusing to retire: " + live + "; stop claude-mem (quit the Claude and Codex sessions that load it), then re-run. Nothing was changed"
	}
	if info, err := os.Lstat(o.dataDir()); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		return "refusing to retire: " + o.dataDir() + " is a symlink; moving it would archive the link, not the data. Point it at a real directory or archive it by hand"
	}
	seen := map[string]bool{}
	var unreadable []string
	for _, step := range steps {
		if step.Status == RetireUnknown && !seen[step.Detail] {
			seen[step.Detail] = true
			unreadable = append(unreadable, step.Detail)
		}
	}
	if len(unreadable) > 0 {
		sort.Strings(unreadable)
		return "refusing to retire: could not read " + strings.Join(unreadable, "; ") + ". Fix or move the file, then re-run. Nothing was changed"
	}
	return ""
}

// presence is the tri-state result of looking for claude-mem somewhere.
type presence int

const (
	absent presence = iota
	present
	unknown
)

func (p presence) detail(err error) string {
	switch p {
	case present:
		return "present"
	case unknown:
		return "unreadable: " + err.Error()
	}
	return "not present"
}

func exists(path string) bool { _, err := os.Lstat(path); return err == nil }

// readFileState reads a file once: a missing file is absent evidence, any other error is unknown.
func readFileState(path string) ([]byte, presence, error) {
	raw, err := os.ReadFile(path)
	if err == nil {
		return raw, present, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil, absent, nil
	}
	return nil, unknown, err
}

// jsonKeyState reports whether key exists (under outer when given) in a JSON file. A file that
// is missing is absent; one that cannot be read or parsed (JSONC, a permission error) is unknown.
func jsonKeyState(path, outer, key string) (presence, error) {
	raw, state, err := readFileState(path)
	if state != present {
		return state, err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return unknown, fmt.Errorf("not valid JSON (%v)", err)
	}
	if outer != "" {
		var inner map[string]json.RawMessage
		if len(doc[outer]) == 0 || string(bytes.TrimSpace(doc[outer])) == "null" {
			return absent, nil
		}
		if err := json.Unmarshal(doc[outer], &inner); err != nil {
			return unknown, fmt.Errorf("%q is not an object (%v)", outer, err)
		}
		doc = inner
	}
	if _, ok := doc[key]; ok {
		return present, nil
	}
	return absent, nil
}

// fileContainsState reports whether a text file names needle, with the same tri-state.
func fileContainsState(path, needle string) (presence, error) {
	raw, state, err := readFileState(path)
	if state != present {
		return state, err
	}
	if strings.Contains(string(raw), needle) {
		return present, nil
	}
	return absent, nil
}

func mcpServerState(path, name string) (presence, error) {
	raw, state, err := readFileState(path)
	if state != present {
		return state, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return absent, nil
	}
	var doc struct {
		Servers json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return unknown, fmt.Errorf("not valid JSON (%v)", err)
	}
	if len(doc.Servers) == 0 || string(bytes.TrimSpace(doc.Servers)) == "null" {
		return absent, nil
	}
	var servers map[string]json.RawMessage
	if err := json.Unmarshal(doc.Servers, &servers); err != nil {
		return unknown, fmt.Errorf("mcpServers is not an object (%v)", err)
	}
	if _, ok := servers[name]; ok {
		return present, nil
	}
	return absent, nil
}

func (o RetireOptions) plan() []RetireStep {
	var steps []RetireStep
	add := func(s RetireStep, state presence, err error, source ...string) {
		s.Detail = state.detail(err)
		if state == unknown && len(source) > 0 {
			// The refusal must name the file that could not be read, not only the thing sought in it.
			s.Detail = source[0] + " " + s.Detail
			s.Source = source[0]
		}
		switch state {
		case absent:
			s.Status = RetireAbsent
		case unknown:
			s.Status = RetireUnknown
		}
		steps = append(steps, s)
	}
	installed, ierr := jsonKeyState(filepath.Join(o.claudeDir(), "plugins", "installed_plugins.json"), "plugins", claudeMemPluginID)
	market, merr := jsonKeyState(filepath.Join(o.claudeDir(), "plugins", "known_marketplaces.json"), "", claudeMemMarketplace)
	codexName, cerr := fileContainsState(o.codexConfig(), "claude-mem@")
	codexMarket, kerr := fileContainsState(o.codexConfig(), codexClaudeMemMarket)
	add(RetireStep{ID: "claude-plugin-disable", Kind: "plugin", Target: claudeMemPluginID, Action: "claude plugin disable " + claudeMemPluginID,
		command: []string{"claude", "plugin", "disable", claudeMemPluginID}}, installed, ierr, filepath.Join(o.claudeDir(), "plugins", "installed_plugins.json"))
	add(RetireStep{ID: "claude-plugin-uninstall", Kind: "plugin", Target: claudeMemPluginID, Action: "claude plugin uninstall " + claudeMemPluginID,
		command: []string{"claude", "plugin", "uninstall", claudeMemPluginID}}, installed, ierr, filepath.Join(o.claudeDir(), "plugins", "installed_plugins.json"))
	add(RetireStep{ID: "claude-marketplace", Kind: "marketplace", Target: claudeMemMarketplace, Action: "claude plugin marketplace remove " + claudeMemMarketplace,
		command: []string{"claude", "plugin", "marketplace", "remove", claudeMemMarketplace}}, market, merr, filepath.Join(o.claudeDir(), "plugins", "known_marketplaces.json"))
	add(RetireStep{ID: "codex-plugin", Kind: "plugin", Target: "claude-mem@" + codexClaudeMemMarket, Action: "codex plugin remove claude-mem --marketplace " + codexClaudeMemMarket,
		command: []string{"codex", "plugin", "remove", "claude-mem", "--marketplace", codexClaudeMemMarket}}, codexName, cerr, o.codexConfig())
	add(RetireStep{ID: "codex-marketplace", Kind: "marketplace", Target: codexClaudeMemMarket, Action: "codex plugin marketplace remove " + codexClaudeMemMarket,
		command: []string{"codex", "plugin", "marketplace", "remove", codexClaudeMemMarket}}, codexMarket, kerr, o.codexConfig())
	for _, t := range o.mcpTargets() {
		state, err := mcpServerState(t.path, "claude-mem")
		add(RetireStep{ID: "mcp-" + t.agent, Kind: "mcp", Target: t.path, Action: "remove the claude-mem entry under mcpServers in " + t.path}, state, err, t.path)
	}
	for _, label := range []string{claudeMemBridgeLabel, claudeMemSyncLabel} {
		plist := filepath.Join(o.agentsDir(), label+".plist")
		state := absent
		if exists(plist) {
			state = present
		}
		add(RetireStep{ID: "launchd-" + label, Kind: "launchd", Target: plist, Action: "launchctl bootout gui/<uid>/" + label + " and remove " + plist}, state, nil)
	}
	dataState, derr := absent, error(nil)
	if _, err := os.Lstat(o.dataDir()); err == nil {
		dataState = present
	} else if !errors.Is(err, fs.ErrNotExist) {
		dataState, derr = unknown, err
	}
	add(RetireStep{ID: "archive", Kind: "archive", Target: o.dataDir(), Action: "move " + o.dataDir() + " to " + o.archivePath() + " (never deleted)"}, dataState, derr, o.dataDir())
	return steps
}

type retireMCPTarget struct{ agent, path string }

func (o RetireOptions) mcpTargets() []retireMCPTarget {
	m := &AmemManager{HomeDir: o.HomeDir, KimiHome: o.KimiHome}
	return []retireMCPTarget{
		{"kimi", m.KimiMCPPath()}, {"qwen", m.QwenMCPPath()}, {"antigravity", m.AntigravityMCPPath()},
		{"kiro", m.KiroMCPPath()}, {"copilot", m.CopilotMCPPath()},
	}
}

func (o RetireOptions) execute(ctx context.Context, result *RetireResult) {
	env := o.nativeEnv()
	for i := range result.Steps {
		step := &result.Steps[i]
		if step.Status == RetireAbsent {
			continue
		}
		var err error
		switch step.Kind {
		case "plugin", "marketplace":
			_, err = o.Run(ctx, env, step.command[0], step.command[1:]...)
		case "mcp":
			err = removeMCPServer(step.Target, "claude-mem")
		case "launchd":
			err = o.removeLaunchAgent(ctx, env, step.Target)
		case "archive":
			dest := o.archivePath()
			err = os.Rename(o.dataDir(), dest)
			if err == nil {
				result.Archive = dest
			}
		}
		if err != nil {
			step.Status, step.Detail = RetireFailed, err.Error()
			continue
		}
		step.Status = RetireDone
	}
}

func (o RetireOptions) removeLaunchAgent(ctx context.Context, env []string, plist string) error {
	label := strings.TrimSuffix(filepath.Base(plist), ".plist")
	if o.GOOS == "darwin" {
		// bootout fails when the agent is not loaded, which is the state wanted.
		_, _ = o.Run(ctx, env, "launchctl", "bootout", "gui/"+strconv.Itoa(os.Getuid())+"/"+label)
	}
	return os.Remove(plist)
}

// manualEdits are the exact lines of files dot does not own: Claude Code's settings
// (docs/BOUNDARIES.md) and any claude-mem line left in the Codex config or a LaunchAgent that
// dot did not write. A file that cannot be read is named, never skipped.
func (o RetireOptions) manualEdits() []string {
	var out []string
	scan := func(path string) {
		raw, state, err := readFileState(path)
		switch state {
		case unknown:
			out = append(out, fmt.Sprintf("%s: could not be read (%v); check it for claude-mem lines by hand", path, err))
		case present:
			for n, line := range strings.Split(string(raw), "\n") {
				if strings.Contains(line, "claude-mem") {
					out = append(out, fmt.Sprintf("%s line %d: remove %s", path, n+1, strings.TrimSpace(line)))
				}
			}
		}
	}
	scan(filepath.Join(o.claudeDir(), "settings.json"))
	scan(filepath.Join(o.claudeDir(), "settings.local.json"))
	scan(o.codexConfig())
	scan(filepath.Join(o.agentsDir(), claudeMemWorkerLabel+".plist"))
	return out
}

// runningWorker names a running claude-mem process as "N claude-mem process(es) running (worker-service.cjs)",
// says so when the process list cannot be read, or returns "" when none runs. It never prints argv:
// unrelated arguments on a command line may be private.
func (o RetireOptions) runningWorker(ctx context.Context) string {
	ps, err := o.Ps(ctx)
	if err != nil {
		return "the process list could not be read (" + err.Error() + "), so a running claude-mem cannot be ruled out"
	}
	count, names := 0, map[string]bool{}
	for _, line := range strings.Split(ps, "\n") {
		hit := strings.Contains(line, "worker-service.cjs") || strings.Contains(line, "claude-mem") && !strings.Contains(line, "dot ai memory retire")
		if !hit {
			continue
		}
		count++
		switch {
		case strings.Contains(line, "worker-service.cjs"):
			names["worker-service.cjs"] = true
		case strings.Contains(line, "transcript-watcher.cjs"):
			names["transcript-watcher.cjs"] = true
		case strings.Contains(line, "mcp-server.cjs"):
			names["mcp-server.cjs"] = true
		default:
			names["claude-mem"] = true
		}
	}
	if count == 0 {
		return ""
	}
	var list []string
	for name := range names {
		list = append(list, name)
	}
	sort.Strings(list)
	return fmt.Sprintf("%d claude-mem process(es) running (%s)", count, strings.Join(list, ", "))
}

// readBack checks that nothing of claude-mem is left. Read-only in every mode. Evidence that cannot
// be read is reported as found, never as clean.
func (o RetireOptions) readBack(ctx context.Context) []RetireReadBack {
	var out []RetireReadBack
	add := func(check string, found string) {
		out = append(out, RetireReadBack{Check: check, Clean: found == "", Found: found})
	}
	add("no claude-mem process", o.runningWorker(ctx))
	port := claudeMemDefaultWorkerP
	for _, dir := range []string{o.dataDir(), o.archivePathNewest()} {
		if dir == "" {
			continue
		}
		if raw, err := os.ReadFile(filepath.Join(dir, "worker.port")); err == nil {
			if p, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && p > 0 && p < 65536 {
				port = p
				break
			}
		}
	}
	listening := ""
	if o.PortOpen(port) {
		listening = "port " + strconv.Itoa(port) + " is listening"
	}
	add("no claude-mem worker port", listening)
	add("no claude-mem plugin or hook", o.pluginResidue())
	launchd := ""
	for _, name := range []string{claudeMemBridgeLabel, claudeMemSyncLabel, claudeMemWorkerLabel} {
		if exists(filepath.Join(o.agentsDir(), name+".plist")) {
			launchd = name + ".plist still present"
			break
		}
	}
	add("no claude-mem launchd agent", launchd)
	add("no claude-mem MCP entry", o.mcpResidue())
	return out
}

// pluginResidue returns what still names claude-mem in the plugin and hook state, or "".
func (o RetireOptions) pluginResidue() string {
	state, err := jsonKeyState(filepath.Join(o.claudeDir(), "plugins", "installed_plugins.json"), "plugins", claudeMemPluginID)
	switch state {
	case present:
		return "Claude plugin " + claudeMemPluginID + " still installed"
	case unknown:
		return filepath.Join(o.claudeDir(), "plugins", "installed_plugins.json") + " could not be read (" + err.Error() + ")"
	}
	state, err = fileContainsState(o.codexConfig(), "claude-mem")
	switch state {
	case present:
		return o.codexConfig() + " still names claude-mem (see the lines to edit by hand)"
	case unknown:
		return o.codexConfig() + " could not be read (" + err.Error() + ")"
	}
	for _, line := range o.manualEdits() {
		return line
	}
	return ""
}

func (o RetireOptions) mcpResidue() string {
	for _, t := range o.mcpTargets() {
		state, err := mcpServerState(t.path, "claude-mem")
		switch state {
		case present:
			return t.path + " still has one"
		case unknown:
			return t.path + " could not be read (" + err.Error() + ")"
		}
	}
	return ""
}

// archivePathNewest is the most recent dated archive of the data directory, or "".
func (o RetireOptions) archivePathNewest() string {
	matches, _ := filepath.Glob(o.dataDir() + ".archive-*")
	if len(matches) == 0 {
		return ""
	}
	sort.Strings(matches)
	return matches[len(matches)-1]
}

// removeMCPServer deletes one server from mcpServers, keeping every other server and top-level key and
// writing the file back without HTML escaping. A file without the server is left untouched, and so is
// one that cannot be parsed (the error says so).
func removeMCPServer(path, name string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	doc := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("parse MCP config %s: %w", path, err)
	}
	servers := map[string]json.RawMessage{}
	if len(doc["mcpServers"]) > 0 && string(bytes.TrimSpace(doc["mcpServers"])) != "null" {
		if err := json.Unmarshal(doc["mcpServers"], &servers); err != nil {
			return fmt.Errorf("parse mcpServers in %s: %w", path, err)
		}
	}
	if _, ok := servers[name]; !ok {
		return nil
	}
	delete(servers, name)
	inner, err := marshalNoEscape(servers)
	if err != nil {
		return err
	}
	doc["mcpServers"] = inner
	out, err := marshalIndentNoEscape(doc)
	if err != nil {
		return err
	}
	return atomicWriteFile(path, out, fileModeOrDefault(path, 0o600))
}

func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func marshalIndentNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
