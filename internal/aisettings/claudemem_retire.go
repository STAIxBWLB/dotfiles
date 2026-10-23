package aisettings

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// RetireStatus is the state of one retirement step.
type RetireStatus string

const (
	RetireWould  RetireStatus = "would"  // dry run: this step would run
	RetireDone   RetireStatus = "done"   // apply: ran and succeeded
	RetireAbsent RetireStatus = "absent" // nothing to do on this machine
	RetireFailed RetireStatus = "failed" // apply: ran and failed
)

// RetireStep is one thing the retirement touches.
type RetireStep struct {
	ID      string
	Kind    string // plugin, marketplace, mcp, launchd, archive
	Target  string
	Action  string
	Status  RetireStatus
	Detail  string
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
	// Blocked is why --apply refused to start, empty otherwise.
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

// Runner runs a native command and returns its combined output. It is the one
// seam through which retirement mutates anything outside dot's own files, so a
// test proves the dry run by counting calls.
type Runner func(ctx context.Context, name string, args ...string) (string, error)

// RetireOptions configures the retirement.
type RetireOptions struct {
	HomeDir string
	// Apply runs the steps. The default is a dry run that writes nothing.
	Apply bool
	// ImportConfirmed is the person's statement that the claude-mem import into
	// amem has completed. Apply refuses without it and without import evidence.
	ImportConfirmed bool
	// AmemHome is the amem memory directory; empty derives it like amem does.
	AmemHome string
	Now      func() time.Time
	Run      Runner
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
	claudeMemWorkerPlist    = "com.claude-mem.worker.plist"
	claudeMemDefaultWorkerP = 37777
)

func defaultRunner(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
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
}

func (o RetireOptions) dataDir() string { return filepath.Join(o.HomeDir, ".claude-mem") }
func (o RetireOptions) codexConfig() string {
	return filepath.Join(CodexHomeDir(o.HomeDir), "config.toml")
}
func (o RetireOptions) claudeDir() string { return filepath.Join(o.HomeDir, ".claude") }
func (o RetireOptions) agentsDir() string { return filepath.Join(o.HomeDir, "Library", "LaunchAgents") }

// archivePath is the dated destination ~/.claude-mem is moved to, never a
// deletion. A second archive on the same day gets a numeric suffix.
func (o RetireOptions) archivePath() string {
	base := o.dataDir() + ".archive-" + o.Now().Format("20060102")
	path := base
	for i := 2; ; i++ {
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			return path
		}
		path = base + "-" + strconv.Itoa(i)
	}
}

// amemHome mirrors amem's own resolution: AMEM_HOME, else AGENTHUB_HOME/memory,
// else ~/.agenthub/memory.
func (o RetireOptions) amemHome() string {
	if o.AmemHome != "" {
		return o.AmemHome
	}
	if v := strings.TrimSpace(os.Getenv("AMEM_HOME")); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("AGENTHUB_HOME")); v != "" {
		return filepath.Join(v, "memory")
	}
	return filepath.Join(o.HomeDir, ".agenthub", "memory")
}

// ImportEvidence counts the cursor files `amem import claude-mem` leaves in the
// amem home. Cursors are keyed by a hash of the source path, alias and project,
// which dot cannot recompute, so this proves an import ran, not that it covered
// every project: the person's confirmation covers the rest.
func (o RetireOptions) ImportEvidence() int {
	n := 0
	entries, _ := filepath.Glob(filepath.Join(o.amemHome(), "projects", "*", "import-state.json"))
	for _, path := range entries {
		var cursors map[string]json.Number
		raw, err := os.ReadFile(path)
		if err == nil && json.Unmarshal(raw, &cursors) == nil && len(cursors) > 0 {
			n++
		}
	}
	return n
}

// RetireClaudeMem plans, and with Apply runs, the retirement of claude-mem:
// native plugin removal for Claude and Codex, MCP entries of the other agents,
// both dotfiles launchd agents, and a dated archive of ~/.claude-mem. It ends
// with a read-back that no claude-mem process, port, hook or launchd agent
// remains. It never deletes the data directory and never edits a file dot does
// not own; those edits are returned in Manual.
func RetireClaudeMem(ctx context.Context, opts RetireOptions) (RetireResult, error) {
	opts.defaults()
	if opts.HomeDir == "" || !filepath.IsAbs(opts.HomeDir) {
		return RetireResult{}, fmt.Errorf("retire needs an absolute home directory")
	}
	result := RetireResult{Apply: opts.Apply}
	result.Steps = opts.plan()
	result.Manual = opts.manualEdits()
	if opts.Apply {
		if blocked := opts.gate(); blocked != "" {
			result.Blocked = blocked
			markWould(result.Steps)
			result.ReadBack = opts.readBack(ctx)
			return result, nil
		}
		opts.execute(ctx, &result)
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

// gate is why apply must not start: the import into amem must have completed.
func (o RetireOptions) gate() string {
	if o.ImportConfirmed && o.ImportEvidence() > 0 {
		return ""
	}
	if !o.ImportConfirmed {
		return "refusing to retire: the import into amem is not confirmed; run `amem import claude-mem`, check it, then pass --import-complete"
	}
	return "refusing to retire: no amem import cursor under " + o.amemHome() + "; run `amem import claude-mem` first"
}

func exists(path string) bool { _, err := os.Lstat(path); return err == nil }

func (o RetireOptions) plan() []RetireStep {
	var steps []RetireStep
	add := func(s RetireStep) {
		if s.Status == "" && !s.present() {
			s.Status = RetireAbsent
		}
		steps = append(steps, s)
	}
	claudeInstalled := jsonHasKey(filepath.Join(o.claudeDir(), "plugins", "installed_plugins.json"), "plugins", claudeMemPluginID)
	claudeMarket := jsonHasKey(filepath.Join(o.claudeDir(), "plugins", "known_marketplaces.json"), "", claudeMemMarketplace)
	codexPlugin := fileContains(o.codexConfig(), "claude-mem@") || fileContains(o.codexConfig(), "plugins.\"claude-mem")
	codexMarket := fileContains(o.codexConfig(), codexClaudeMemMarket)
	add(RetireStep{ID: "claude-plugin-disable", Kind: "plugin", Target: claudeMemPluginID, Action: "claude plugin disable " + claudeMemPluginID,
		command: []string{"claude", "plugin", "disable", claudeMemPluginID}, Detail: presence(claudeInstalled)})
	add(RetireStep{ID: "claude-plugin-uninstall", Kind: "plugin", Target: claudeMemPluginID, Action: "claude plugin uninstall " + claudeMemPluginID,
		command: []string{"claude", "plugin", "uninstall", claudeMemPluginID}, Detail: presence(claudeInstalled)})
	add(RetireStep{ID: "claude-marketplace", Kind: "marketplace", Target: claudeMemMarketplace, Action: "claude plugin marketplace remove " + claudeMemMarketplace,
		command: []string{"claude", "plugin", "marketplace", "remove", claudeMemMarketplace}, Detail: presence(claudeMarket)})
	add(RetireStep{ID: "codex-plugin", Kind: "plugin", Target: "claude-mem@" + codexClaudeMemMarket, Action: "codex plugin remove claude-mem --marketplace " + codexClaudeMemMarket,
		command: []string{"codex", "plugin", "remove", "claude-mem", "--marketplace", codexClaudeMemMarket}, Detail: presence(codexPlugin)})
	add(RetireStep{ID: "codex-marketplace", Kind: "marketplace", Target: codexClaudeMemMarket, Action: "codex plugin marketplace remove " + codexClaudeMemMarket,
		command: []string{"codex", "plugin", "marketplace", "remove", codexClaudeMemMarket}, Detail: presence(codexMarket)})
	for _, t := range o.mcpTargets() {
		add(RetireStep{ID: "mcp-" + t.agent, Kind: "mcp", Target: t.path, Action: "remove the claude-mem entry under mcpServers in " + t.path, Detail: presence(mcpHasServer(t.path, "claude-mem"))})
	}
	for _, label := range []string{claudeMemBridgeLabel, claudeMemSyncLabel} {
		plist := filepath.Join(o.agentsDir(), label+".plist")
		add(RetireStep{ID: "launchd-" + label, Kind: "launchd", Target: plist, Action: "launchctl bootout gui/<uid>/" + label + " and remove " + plist, Detail: presence(exists(plist))})
	}
	add(RetireStep{ID: "archive", Kind: "archive", Target: o.dataDir(), Action: "move " + o.dataDir() + " to " + o.archivePath() + " (never deleted)", Detail: presence(exists(o.dataDir()))})
	return steps
}

func presence(ok bool) string {
	if ok {
		return "present"
	}
	return "not present"
}

func (s RetireStep) present() bool { return s.Detail == "present" }

type retireMCPTarget struct{ agent, path string }

func (o RetireOptions) mcpTargets() []retireMCPTarget {
	m := &AmemManager{HomeDir: o.HomeDir, KimiHome: o.KimiHome}
	return []retireMCPTarget{
		{"kimi", m.KimiMCPPath()}, {"qwen", m.QwenMCPPath()}, {"antigravity", m.AntigravityMCPPath()},
		{"kiro", m.KiroMCPPath()}, {"copilot", m.CopilotMCPPath()},
	}
}

func (o RetireOptions) execute(ctx context.Context, result *RetireResult) {
	for i := range result.Steps {
		step := &result.Steps[i]
		if step.Status == RetireAbsent {
			continue
		}
		var err error
		switch step.Kind {
		case "plugin", "marketplace":
			_, err = o.Run(ctx, step.command[0], step.command[1:]...)
		case "mcp":
			err = removeMCPServer(step.Target, "claude-mem")
		case "launchd":
			err = o.removeLaunchAgent(ctx, step.Target)
		case "archive":
			// A live worker holds the SQLite files open: moving them under it corrupts both.
			if line := o.runningWorker(ctx); line != "" {
				err = fmt.Errorf("a claude-mem process is still running (%s); stop it, then re-run", line)
				break
			}
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

func (o RetireOptions) removeLaunchAgent(ctx context.Context, plist string) error {
	label := strings.TrimSuffix(filepath.Base(plist), ".plist")
	if o.GOOS == "darwin" {
		// bootout fails when the agent is not loaded, which is the state wanted.
		_, _ = o.Run(ctx, "launchctl", "bootout", "gui/"+strconv.Itoa(os.Getuid())+"/"+label)
	}
	return os.Remove(plist)
}

// manualEdits are the exact lines of files dot does not own. Claude Code's
// settings.json belongs to Claude Code, so dot prints each claude-mem line with
// its number and never writes the file (docs/BOUNDARIES.md).
func (o RetireOptions) manualEdits() []string {
	var out []string
	for _, rel := range []string{"settings.json", "settings.local.json"} {
		path := filepath.Join(o.claudeDir(), rel)
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for n, line := range strings.Split(string(raw), "\n") {
			if strings.Contains(line, "claude-mem") {
				out = append(out, fmt.Sprintf("%s line %d: remove %s", path, n+1, strings.TrimSpace(line)))
			}
		}
	}
	return out
}

// runningWorker returns the first claude-mem process command line (trimmed), a
// note when the process list cannot be read, or "" when none runs. An unreadable
// list is never "clean": the read-back fails closed.
func (o RetireOptions) runningWorker(ctx context.Context) string {
	ps, err := o.Ps(ctx)
	if err != nil {
		return "process list unavailable: " + err.Error()
	}
	for _, line := range strings.Split(ps, "\n") {
		if strings.Contains(line, "worker-service.cjs") || strings.Contains(line, "claude-mem") && !strings.Contains(line, "dot ai memory retire") {
			line = strings.TrimSpace(line)
			if len(line) > 120 {
				line = line[:120]
			}
			return line
		}
	}
	return ""
}

// readBack checks that nothing of claude-mem is left. Read-only in every mode.
func (o RetireOptions) readBack(ctx context.Context) []RetireReadBack {
	var out []RetireReadBack
	add := func(check string, found string) {
		out = append(out, RetireReadBack{Check: check, Clean: found == "", Found: found})
	}
	add("no claude-mem process", o.runningWorker(ctx))
	port := claudeMemDefaultWorkerP
	for _, dir := range []string{o.dataDir(), o.dataDir() + ".archive"} {
		if raw, err := os.ReadFile(filepath.Join(dir, "worker.port")); err == nil {
			if p, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && p > 0 && p < 65536 {
				port = p
			}
		}
	}
	listening := ""
	if o.PortOpen(port) {
		listening = "port " + strconv.Itoa(port) + " is listening"
	}
	add("no claude-mem worker port", listening)
	hooks := ""
	switch {
	case jsonHasKey(filepath.Join(o.claudeDir(), "plugins", "installed_plugins.json"), "plugins", claudeMemPluginID):
		hooks = "Claude plugin " + claudeMemPluginID + " still installed"
	case fileContains(o.codexConfig(), "claude-mem"):
		hooks = o.codexConfig() + " still names claude-mem"
	case len(o.manualEdits()) > 0:
		hooks = fmt.Sprintf("%d claude-mem line(s) remain in Claude settings (edit them by hand, see above)", len(o.manualEdits()))
	}
	add("no claude-mem plugin or hook", hooks)
	launchd := ""
	for _, name := range []string{claudeMemBridgeLabel + ".plist", claudeMemSyncLabel + ".plist", claudeMemWorkerPlist} {
		if exists(filepath.Join(o.agentsDir(), name)) {
			launchd = name + " still present"
			break
		}
	}
	add("no claude-mem launchd agent", launchd)
	for _, t := range o.mcpTargets() {
		if mcpHasServer(t.path, "claude-mem") {
			add("no claude-mem MCP entry", t.path+" still has one")
			return out
		}
	}
	add("no claude-mem MCP entry", "")
	return out
}

func jsonHasKey(path, outer, key string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil {
		return false
	}
	if outer != "" {
		var inner map[string]json.RawMessage
		if json.Unmarshal(doc[outer], &inner) != nil {
			return false
		}
		doc = inner
	}
	_, ok := doc[key]
	return ok
}

func fileContains(path, needle string) bool {
	raw, err := os.ReadFile(path)
	return err == nil && strings.Contains(string(raw), needle)
}

func mcpHasServer(path, name string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var doc struct {
		Servers map[string]json.RawMessage `json:"mcpServers"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return false
	}
	_, ok := doc.Servers[name]
	return ok
}

// removeMCPServer deletes one server from mcpServers, keeping every other server
// and top-level key. A file without the server is left untouched.
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
	if err := json.Unmarshal(doc["mcpServers"], &servers); err != nil {
		return fmt.Errorf("parse mcpServers in %s: %w", path, err)
	}
	if _, ok := servers[name]; !ok {
		return nil
	}
	delete(servers, name)
	doc["mcpServers"], _ = json.Marshal(servers)
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(path, append(out, '\n'), fileModeOrDefault(path, 0o600))
}

// CodexHomeDir is the codex home dot inspects: CODEX_HOME when the shell sets
// it, with a leading ~ expanded and symlinks resolved; otherwise ~/.codex.
func CodexHomeDir(homeDir string) string {
	raw := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if raw == "" {
		return filepath.Join(homeDir, ".codex")
	}
	if raw == "~" || strings.HasPrefix(raw, "~/") {
		raw = filepath.Join(homeDir, strings.TrimPrefix(raw, "~"))
	}
	raw = filepath.Clean(raw)
	if resolved, err := filepath.EvalSymlinks(raw); err == nil {
		return resolved
	}
	return raw
}
