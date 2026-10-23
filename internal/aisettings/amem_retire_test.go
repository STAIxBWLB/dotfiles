package aisettings

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---- the memory block: only where amem is used, backed up, never failing an apply ----

func newAgentsHome(t *testing.T) (*AgentsManager, string) {
	t.Helper()
	home := t.TempDir()
	return &AgentsManager{HomeDir: home, SSOTDir: filepath.Join(home, ".config", "dotfiles", "agents")}, home
}

// With no amem on PATH, MemoryWanted turns on the MCP configs and the SSOT alone.
func noAmemOnPath(t *testing.T) { t.Setenv("PATH", t.TempDir()) }

func TestEnsureMemoryBlockLeavesAMachineWithoutAmemAlone(t *testing.T) {
	noAmemOnPath(t)
	m, home := newAgentsHome(t)
	mustWriteFile(t, m.SSOTPath(), "# rules\n\n"+legacyBlock+"\n")
	before := snapshotTree(t, home)
	res, err := m.EnsureMemoryBlock(false)
	if err != nil || res.Changed || res.Skipped == "" {
		t.Fatalf("a machine without amem was touched: %+v %v", res, err)
	}
	if !sameTree(before, snapshotTree(t, home)) {
		t.Fatal("the SSOT or its backup tree changed although amem is not in use")
	}
	if needed, _ := m.MemoryBlockNeeded(); needed {
		t.Fatal("Check would report a change on a machine without amem")
	}
}

func TestEnsureMemoryBlockBacksUpTheSSOTBeforeRewritingIt(t *testing.T) {
	noAmemOnPath(t)
	m, home := newAgentsHome(t)
	original := "# rules\n\n" + legacyBlock + "\n"
	mustWriteFile(t, m.SSOTPath(), original)
	mustWriteJSON(t, filepath.Join(home, ".qwen", "settings.json"), map[string]any{"mcpServers": map[string]any{AmemServerName: map[string]any{"command": "/x/amem", "args": []string{"mcp"}}}})
	res, err := m.EnsureMemoryBlock(false)
	if err != nil || !res.Changed || res.Backup == "" {
		t.Fatalf("not written or not backed up: %+v %v", res, err)
	}
	if raw, _ := os.ReadFile(res.Backup); string(raw) != original {
		t.Fatalf("the backup is not the pre-edit SSOT:\n%s", raw)
	}
	if !strings.Contains(res.Backup, filepath.Join(".local", "share", "dotfiles", "backup", "agents-ssot")) {
		t.Fatalf("backup outside the documented tree: %s", res.Backup)
	}
	if got, _ := os.ReadFile(m.SSOTPath()); strings.Contains(string(got), "claude-mem") || countOf(string(got), memoryBlockStart) != 1 {
		t.Fatalf("not rewritten:\n%s", got)
	}
	again, err := m.EnsureMemoryBlock(false)
	if err != nil || again.Changed || again.Backup != "" {
		t.Fatalf("a second pass changed or backed up again: %+v %v", again, err)
	}
}

func TestEnsureMemoryBlockDryRunWritesNothingAndBacksUpNothing(t *testing.T) {
	m, home := newAgentsHome(t)
	mustWriteFile(t, m.SSOTPath(), "# rules\n\n"+legacyBlock+"\n"+memoryBlockStart+"\nold\n"+memoryBlockEnd+"\n")
	before := snapshotTree(t, home)
	res, err := m.EnsureMemoryBlock(true)
	if err != nil || !res.Changed {
		t.Fatalf("a wanted machine reports no change: %+v %v", res, err)
	}
	if !sameTree(before, snapshotTree(t, home)) {
		t.Fatal("a dry run wrote the SSOT or a backup")
	}
}

// A bad marker is a finding the person reads, not a reason to fail the apply it rides on.
func TestEnsureMemoryBlockReportsAMalformedMarkerWithoutFailingOrWriting(t *testing.T) {
	for name, body := range map[string]string{
		"duplicate legacy": legacyBlock + "\n" + legacyBlock + "\n" + memoryBlockStart + "\nx\n" + memoryBlockEnd + "\n",
		"unclosed amem":    "# r\n" + memoryBlockStart + "\nno end\n",
	} {
		t.Run(name, func(t *testing.T) {
			m, home := newAgentsHome(t)
			mustWriteFile(t, m.SSOTPath(), body)
			before := snapshotTree(t, home)
			res, err := m.EnsureMemoryBlock(false)
			if err != nil {
				t.Fatalf("a malformed marker failed the apply: %v", err)
			}
			if res.Warning == "" || res.Changed {
				t.Fatalf("not reported as a finding: %+v", res)
			}
			if !strings.Contains(res.Warning, m.SSOTPath()) {
				t.Fatalf("the warning does not name the file: %s", res.Warning)
			}
			if !sameTree(before, snapshotTree(t, home)) {
				t.Fatal("a malformed marker still led to a write")
			}
			if needed, warning := m.MemoryBlockNeeded(); needed || warning == "" {
				t.Fatalf("Check: needed=%v warning=%q", needed, warning)
			}
		})
	}
}

func TestEnsureMemoryBlockWithNoSSOTIsSkippedNotAnError(t *testing.T) {
	m, _ := newAgentsHome(t)
	res, err := m.EnsureMemoryBlock(false)
	if err != nil || res.Skipped == "" {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestMemoryWantedFollowsAmemOnPathOrARegisteredServerOrTheBlockItself(t *testing.T) {
	noAmemOnPath(t)
	m, home := newAgentsHome(t)
	mustWriteFile(t, m.SSOTPath(), "# rules\n")
	if m.MemoryWanted() {
		t.Fatal("wanted on an empty machine")
	}
	mustWriteJSON(t, filepath.Join(home, ".copilot", "mcp-config.json"), map[string]any{"mcpServers": map[string]any{"other": map[string]any{}}})
	if m.MemoryWanted() {
		t.Fatal("wanted because some other server is registered")
	}
	mustWriteJSON(t, filepath.Join(home, ".copilot", "mcp-config.json"), map[string]any{"mcpServers": map[string]any{AmemServerName: map[string]any{}}})
	if !m.MemoryWanted() {
		t.Fatal("not wanted although an amem server is registered")
	}
	m2, _ := newAgentsHome(t)
	mustWriteFile(t, m2.SSOTPath(), "# rules\n"+memoryBlockStart+"\nx\n"+memoryBlockEnd+"\n")
	if !m2.MemoryWanted() {
		t.Fatal("an SSOT that already has the block must keep it current")
	}
	bin := t.TempDir()
	mustWriteFile(t, filepath.Join(bin, "amem"), "#!/bin/sh\n")
	_ = os.Chmod(filepath.Join(bin, "amem"), 0o755)
	t.Setenv("PATH", bin)
	m3, _ := newAgentsHome(t)
	if !m3.MemoryWanted() {
		t.Fatal("not wanted although amem is on PATH")
	}
}

// ---- the MCP merge: a null servers map, key order, HTML escaping ----

func TestAmemInstallSurvivesAnNullMcpServersAndAnNullFile(t *testing.T) {
	for name, body := range map[string]string{"null servers": `{"mcpServers": null, "keep": 1}`, "null file": `null`, "empty object": `{}`} {
		t.Run(name, func(t *testing.T) {
			m, _ := newAmem(t)
			mustWriteFile(t, m.QwenMCPPath(), body)
			if _, err := m.Install(); err != nil {
				t.Fatalf("install panicked or failed: %v", err)
			}
			if readServers(t, m.QwenMCPPath())[AmemServerName] == nil {
				t.Fatal("no amem entry")
			}
		})
	}
}

func TestAmemInstallDoesNotHTMLEscapeAnArgumentOrAnotherServer(t *testing.T) {
	m, _ := newAmem(t)
	mustWriteFile(t, m.QwenMCPPath(), `{"mcpServers":{"other":{"command":"sh","args":["-c","a && b < c > d"]}}}`)
	if _, err := m.Install(); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(m.QwenMCPPath())
	for _, escaped := range []string{`\u0026`, `\u003c`, `\u003e`} {
		if strings.Contains(string(raw), escaped) {
			t.Fatalf("the merge rewrote another server's arguments with HTML escapes:\n%s", raw)
		}
	}
	if !strings.Contains(string(raw), "a && b < c > d") {
		t.Fatalf("another server's argument was altered:\n%s", raw)
	}
}

// ---- the retirement ----

type recorder struct {
	calls [][]string
	envs  [][]string
	fail  map[string]bool
}

func (r *recorder) run(_ context.Context, env []string, name string, args ...string) (string, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	r.envs = append(r.envs, env)
	if r.fail[name+" "+strings.Join(args, " ")] {
		return "boom", os.ErrInvalid
	}
	return "", nil
}

// seedClaudeMemHome builds a home that has every claude-mem footprint dot knows.
func seedClaudeMemHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	mustWriteJSON(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"), map[string]any{"plugins": map[string]any{claudeMemPluginID: []any{map[string]any{"scope": "user"}}}})
	mustWriteJSON(t, filepath.Join(home, ".claude", "plugins", "known_marketplaces.json"), map[string]any{claudeMemMarketplace: map[string]any{}})
	mustWriteFile(t, filepath.Join(home, ".claude", "settings.json"), "{\n  \"permissions\": {\n    \"allow\": [\n      \"mcp__plugin_claude-mem_mcp-search\"\n    ]\n  },\n  \"enabledPlugins\": {\n    \"claude-mem@thedotmack\": true\n  }\n}\n")
	mustWriteFile(t, filepath.Join(home, ".codex", "config.toml"), "[marketplaces.claude-mem-local]\nsource = \"x\"\n[plugins.\"claude-mem@claude-mem-local\"]\nenabled = true\n")
	for _, p := range []string{filepath.Join(home, ".kimi-code", "mcp.json"), filepath.Join(home, ".qwen", "settings.json"), filepath.Join(home, ".gemini", "config", "mcp_config.json"), filepath.Join(home, ".kiro", "settings", "mcp.json"), filepath.Join(home, ".copilot", "mcp-config.json")} {
		mustWriteJSON(t, p, map[string]any{"mcpServers": map[string]any{"claude-mem": map[string]any{"command": "/x/dot"}, "keepme": map[string]any{"command": "/x/keep"}}})
	}
	for _, label := range []string{claudeMemBridgeLabel, claudeMemSyncLabel} {
		mustWriteFile(t, filepath.Join(home, "Library", "LaunchAgents", label+".plist"), "<plist/>")
	}
	mustWriteFile(t, filepath.Join(home, ".claude-mem", "claude-mem.db"), "SQLITE-DATA")
	mustWriteFile(t, filepath.Join(home, ".claude-mem", "settings.json"), "{}")
	return home
}

// retireOpts describes the person's own home: ambient variables count, as they do for the real run.
func retireOpts(home string, rec *recorder) RetireOptions {
	return RetireOptions{
		HomeDir: home, Run: rec.run, GOOS: "linux", Ambient: []string{"PATH=/usr/bin", "HOME=/somewhere/else"},
		Now:      func() time.Time { return time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC) },
		Ps:       func(context.Context) (string, error) { return "/sbin/launchd\n/usr/bin/zsh\n", nil },
		PortOpen: func(int) bool { return false },
	}
}

func stepIDs(r RetireResult) map[string]RetireStatus {
	out := map[string]RetireStatus{}
	for _, s := range r.Steps {
		out[s.ID] = s.Status
	}
	return out
}

func seedImport(t *testing.T, home string) {
	t.Helper()
	mustWriteFile(t, filepath.Join(home, ".agenthub", "memory", "projects", "abc", "import-state.json"), `{"deadbeef": 42}`)
}

func applyOpts(home string, rec *recorder) RetireOptions {
	o := retireOpts(home, rec)
	o.Apply, o.ImportConfirmed = true, true
	return o
}

// refused asserts an apply was turned away before it started: a reason, no native call, no write.
func refused(t *testing.T, home string, rec *recorder, o RetireOptions, mention string) RetireResult {
	t.Helper()
	before := snapshotTree(t, home)
	result, err := RetireClaudeMem(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if result.Blocked == "" {
		t.Fatalf("not refused: steps=%v", stepIDs(result))
	}
	if !strings.Contains(result.Blocked, mention) {
		t.Fatalf("the refusal does not say why (%q): %s", mention, result.Blocked)
	}
	if len(rec.calls) != 0 {
		t.Fatalf("a refused apply ran native commands: %v", rec.calls)
	}
	if !sameTree(before, snapshotTree(t, home)) {
		t.Fatal("a refused apply changed the home")
	}
	return result
}

func TestRetireDryRunListsEveryItemAndTouchesNothing(t *testing.T) {
	home := seedClaudeMemHome(t)
	rec := &recorder{}
	before := snapshotTree(t, home)
	result, err := RetireClaudeMem(context.Background(), retireOpts(home, rec))
	if err != nil {
		t.Fatal(err)
	}
	if result.Apply || result.Blocked != "" {
		t.Fatalf("dry run flags: %+v", result)
	}
	if len(rec.calls) != 0 {
		t.Fatalf("a dry run ran native commands: %v", rec.calls)
	}
	if !sameTree(before, snapshotTree(t, home)) {
		t.Fatal("a dry run changed the home")
	}
	want := []string{
		"claude-plugin-disable", "claude-plugin-uninstall", "claude-marketplace", "codex-plugin", "codex-marketplace",
		"mcp-kimi", "mcp-qwen", "mcp-antigravity", "mcp-kiro", "mcp-copilot",
		"launchd-" + claudeMemBridgeLabel, "launchd-" + claudeMemSyncLabel, "archive",
	}
	got := stepIDs(result)
	for _, id := range want {
		if got[id] != RetireWould {
			t.Errorf("step %s is %q, want %q", id, got[id], RetireWould)
		}
	}
	if len(result.Steps) != len(want) {
		t.Errorf("%d steps, want %d", len(result.Steps), len(want))
	}
	if result.Clean() {
		t.Fatal("the read-back must show residue before the real run")
	}
}

func TestRetireDryRunPrintsExactSettingsEditsAndNeverWritesThem(t *testing.T) {
	home := seedClaudeMemHome(t)
	settings := filepath.Join(home, ".claude", "settings.json")
	before, _ := os.ReadFile(settings)
	result, err := RetireClaudeMem(context.Background(), retireOpts(home, &recorder{}))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(result.Manual, "\n")
	for _, want := range []string{settings + " line 4: remove \"mcp__plugin_claude-mem_mcp-search\"", settings + " line 8: remove \"claude-mem@thedotmack\": true"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing edit %q in:\n%s", want, joined)
		}
	}
	if after, _ := os.ReadFile(settings); string(after) != string(before) {
		t.Fatal("dot wrote a file it does not own")
	}
}

func TestRetireDryRunOnAHomeWithoutClaudeMemHasNothingToDo(t *testing.T) {
	home := t.TempDir()
	result, err := RetireClaudeMem(context.Background(), retireOpts(home, &recorder{}))
	if err != nil {
		t.Fatal(err)
	}
	for id, status := range stepIDs(result) {
		if status != RetireAbsent {
			t.Errorf("%s is %q on a clean home", id, status)
		}
	}
	if len(result.Manual) != 0 || !result.Clean() {
		t.Fatalf("clean home: manual=%v clean=%v readback=%+v", result.Manual, result.Clean(), result.ReadBack)
	}
}

func TestRetireApplyRefusesWithoutImportConfirmationOrEvidence(t *testing.T) {
	home := seedClaudeMemHome(t)
	for name, mod := range map[string]func(*RetireOptions){
		"no confirmation":        func(o *RetireOptions) { o.ImportConfirmed = false },
		"confirmed, no evidence": func(o *RetireOptions) {},
	} {
		t.Run(name, func(t *testing.T) {
			rec := &recorder{}
			o := applyOpts(home, rec)
			mod(&o)
			refused(t, home, rec, o, "import")
		})
	}
}

// A cursor of 0 means nothing was handled; the layouts amem uses count when they hold a positive row id.
func TestRetireImportEvidenceNeedsAPositiveRowInAnyAmemLayout(t *testing.T) {
	home := t.TempDir()
	o := retireOpts(home, &recorder{})
	o.defaults()
	if o.ImportEvidence() != 0 {
		t.Fatal("evidence on an empty home")
	}
	cursor := filepath.Join(home, ".agenthub", "memory", "projects", "a", "import-state.json")
	mustWriteFile(t, cursor, `{"k": 0}`)
	if o.ImportEvidence() != 0 {
		t.Fatal("a cursor at 0 counted as evidence")
	}
	mustWriteFile(t, cursor, `{broken`)
	if o.ImportEvidence() != 0 {
		t.Fatal("a corrupt cursor counted as evidence")
	}
	for _, rel := range []string{"import-state.json", "user/import-state.json", "projects/b/import-state.json", "generations/g/import-state.json", "generations/g/projects/c/import-state.json"} {
		fresh := t.TempDir()
		o2 := retireOpts(fresh, &recorder{})
		o2.defaults()
		mustWriteFile(t, filepath.Join(fresh, ".agenthub", "memory", rel), `{"k": 7}`)
		if o2.ImportEvidence() != 1 {
			t.Errorf("layout %s not recognized", rel)
		}
	}
}

// Ambient AGENTHUB_HOME, AMEM_HOME, CODEX_HOME and CLAUDE_MEM_DATA_DIR belong to the invoking shell, never to a
// scratch --home: nothing is read from them, and --apply is refused.
func TestRetireUnderAnExplicitHomeReadsNothingFromTheAmbientEnvironment(t *testing.T) {
	scratch := t.TempDir()
	realHome := t.TempDir()
	mustWriteFile(t, filepath.Join(realHome, "memory", "projects", "abc", "import-state.json"), `{"deadbeef": 42}`)
	mustWriteFile(t, filepath.Join(realHome, "codex", "config.toml"), "[plugins.\"claude-mem@claude-mem-local\"]\nenabled = true\n")
	mustWriteFile(t, filepath.Join(realHome, "mem-data", "claude-mem.db"), "REAL")
	ambient := []string{"AGENTHUB_HOME=" + realHome, "AMEM_HOME=" + filepath.Join(realHome, "memory"), "CODEX_HOME=" + filepath.Join(realHome, "codex"), "CLAUDE_MEM_DATA_DIR=" + filepath.Join(realHome, "mem-data"), "PATH=/usr/bin"}
	rec := &recorder{}
	o := RetireOptions{HomeDir: scratch, ExplicitHome: true, Ambient: ambient, Run: rec.run, GOOS: "linux",
		Ps: func(context.Context) (string, error) { return "", nil }, PortOpen: func(int) bool { return false }}
	o.defaults()
	if got := o.amemHome(); got != filepath.Join(scratch, ".agenthub", "memory") {
		t.Fatalf("amemHome %q leaked the ambient environment", got)
	}
	if got := o.codexHome(); got != filepath.Join(scratch, ".codex") {
		t.Fatalf("codexHome %q leaked CODEX_HOME", got)
	}
	if got := o.dataDir(); got != filepath.Join(scratch, ".claude-mem") {
		t.Fatalf("dataDir %q leaked CLAUDE_MEM_DATA_DIR", got)
	}
	if o.ImportEvidence() != 0 {
		t.Fatal("the person's real import cursors satisfied the gate for a scratch home")
	}
	result, err := RetireClaudeMem(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	for id, status := range stepIDs(result) {
		if status != RetireAbsent {
			t.Errorf("%s is %q for an empty scratch home: the real home leaked in", id, status)
		}
	}
	o.Apply, o.ImportConfirmed = true, true
	beforeReal, beforeScratch := snapshotTree(t, realHome), snapshotTree(t, scratch)
	res, err := RetireClaudeMem(context.Background(), o)
	if err != nil || !strings.Contains(res.Blocked, "--home") {
		t.Fatalf("apply under --home was not refused: %+v %v", res, err)
	}
	if len(rec.calls) != 0 {
		t.Fatalf("native commands ran under --home: %v", rec.calls)
	}
	if !sameTree(beforeReal, snapshotTree(t, realHome)) || !sameTree(beforeScratch, snapshotTree(t, scratch)) {
		t.Fatal("a refused apply under --home wrote something")
	}
}

// The person's own home resolves the ambient variables, and the native commands keep the ambient environment
// except HOME, which is pinned to the home being retired.
func TestRetireOwnHomeUsesAmbientVariablesAndPinsHomeForNativeCommands(t *testing.T) {
	home := seedClaudeMemHome(t)
	other := t.TempDir()
	mustWriteFile(t, filepath.Join(other, "memory", "projects", "abc", "import-state.json"), `{"deadbeef": 42}`)
	rec := &recorder{}
	o := applyOpts(home, rec)
	o.Ambient = []string{"AGENTHUB_HOME=" + other, "PATH=/usr/bin", "HOME=/stale", "KEEP=1"}
	result, err := RetireClaudeMem(context.Background(), o)
	if err != nil || result.Blocked != "" {
		t.Fatalf("own home with AGENTHUB_HOME cursors was blocked: %+v %v", result, err)
	}
	if len(rec.envs) == 0 {
		t.Fatal("no native command ran")
	}
	for i, env := range rec.envs {
		has := map[string]string{}
		for _, kv := range env {
			k, v, _ := strings.Cut(kv, "=")
			has[k] = v
		}
		if has["HOME"] != home {
			t.Errorf("call %d ran with HOME=%q, want %q", i, has["HOME"], home)
		}
		if has["KEEP"] != "1" || has["AGENTHUB_HOME"] != other {
			t.Errorf("call %d lost the person's own environment: %v", i, has)
		}
	}
}

func TestRetireNativeEnvUnderAnExplicitHomeCarriesNoAmbientHome(t *testing.T) {
	scratch := t.TempDir()
	o := RetireOptions{HomeDir: scratch, ExplicitHome: true, Ambient: []string{"HOME=/real", "CODEX_HOME=/real/codex", "AMEM_HOME=/real/amem", "AGENTHUB_HOME=/real/hub", "CLAUDE_CONFIG_DIR=/real/claude", "CLAUDE_MEM_DATA_DIR=/real/mem", "PATH=/usr/bin"}}
	o.defaults()
	has := map[string]string{}
	for _, kv := range o.nativeEnv() {
		k, v, _ := strings.Cut(kv, "=")
		has[k] = v
	}
	if has["HOME"] != scratch || has["CODEX_HOME"] != filepath.Join(scratch, ".codex") || has["AMEM_HOME"] != filepath.Join(scratch, ".agenthub", "memory") {
		t.Fatalf("home variables not derived from --home: %v", has)
	}
	for _, leaked := range []string{"AGENTHUB_HOME", "CLAUDE_CONFIG_DIR", "CLAUDE_MEM_DATA_DIR"} {
		if v, ok := has[leaked]; ok {
			t.Errorf("%s=%q leaked into a native command under --home", leaked, v)
		}
	}
	if has["PATH"] != "/usr/bin" {
		t.Errorf("PATH was dropped: %v", has)
	}
}

func TestRetireApplyArchivesNeverDeletesAndLeavesNoResidue(t *testing.T) {
	home := seedClaudeMemHome(t)
	seedImport(t, home)
	rec := &recorder{}
	result, err := RetireClaudeMem(context.Background(), applyOpts(home, rec))
	if err != nil || result.Blocked != "" {
		t.Fatalf("blocked: %+v err=%v", result, err)
	}
	for _, s := range result.Steps {
		if s.Status != RetireDone {
			t.Errorf("step %s ended %q: %s", s.ID, s.Status, s.Detail)
		}
	}
	archive := filepath.Join(home, ".claude-mem.archive-20261024")
	if result.Archive != archive {
		t.Fatalf("archive %q, want %q", result.Archive, archive)
	}
	if raw, err := os.ReadFile(filepath.Join(archive, "claude-mem.db")); err != nil || string(raw) != "SQLITE-DATA" {
		t.Fatalf("the data is not intact in the archive: %v %q", err, raw)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude-mem")); err == nil {
		t.Fatal("the original directory is still there")
	}
	for _, label := range []string{claudeMemBridgeLabel, claudeMemSyncLabel} {
		if _, err := os.Stat(filepath.Join(home, "Library", "LaunchAgents", label+".plist")); err == nil {
			t.Errorf("%s survived", label)
		}
	}
	for _, p := range []string{filepath.Join(home, ".kimi-code", "mcp.json"), filepath.Join(home, ".copilot", "mcp-config.json")} {
		servers := readServers(t, p)
		if servers["claude-mem"] != nil || servers["keepme"] == nil {
			t.Errorf("%s: claude-mem not removed or another server lost: %v", p, servers)
		}
	}
	want := [][]string{
		{"claude", "plugin", "disable", claudeMemPluginID}, {"claude", "plugin", "uninstall", claudeMemPluginID},
		{"claude", "plugin", "marketplace", "remove", claudeMemMarketplace},
		{"codex", "plugin", "remove", "claude-mem", "--marketplace", codexClaudeMemMarket},
		{"codex", "plugin", "marketplace", "remove", codexClaudeMemMarket},
	}
	if len(rec.calls) != len(want) {
		t.Fatalf("native commands: %v", rec.calls)
	}
	for i, call := range want {
		if strings.Join(rec.calls[i], " ") != strings.Join(call, " ") {
			t.Errorf("call %d = %v, want %v", i, rec.calls[i], call)
		}
	}
}

// A live claude-mem (an open Claude session) is refused BEFORE any step, so the usual run never ends half-done,
// and the refusal names a count and process names, never a command line.
func TestRetireRefusesALiveClaudeMemProcessBeforeAnyStep(t *testing.T) {
	home := seedClaudeMemHome(t)
	seedImport(t, home)
	rec := &recorder{}
	o := applyOpts(home, rec)
	o.Ps = func(context.Context) (string, error) {
		return "bun /Users/someone/.claude/plugins/cache/thedotmack/claude-mem/15.0.2/scripts/worker-service.cjs --daemon --token=SECRET-ARGV\nbun /x/claude-mem/scripts/transcript-watcher.cjs watch\n", nil
	}
	result := refused(t, home, rec, o, "2 claude-mem process(es) running (transcript-watcher.cjs, worker-service.cjs)")
	if strings.Contains(result.Blocked, "SECRET-ARGV") || strings.Contains(result.Blocked, "/Users/someone") {
		t.Fatalf("the refusal printed argv: %s", result.Blocked)
	}
	for _, rb := range result.ReadBack {
		if strings.Contains(rb.Found, "SECRET-ARGV") || strings.Contains(rb.Found, "/Users/someone") {
			t.Fatalf("the read-back printed argv: %+v", rb)
		}
	}
	for id, status := range stepIDs(result) {
		if status == RetireDone || status == RetireFailed {
			t.Errorf("step %s ran (%s) although the apply was refused", id, status)
		}
	}
}

func TestRetireRefusesWhenTheProcessListCannotBeRead(t *testing.T) {
	home := seedClaudeMemHome(t)
	seedImport(t, home)
	rec := &recorder{}
	o := applyOpts(home, rec)
	o.Ps = func(context.Context) (string, error) { return "", os.ErrPermission }
	refused(t, home, rec, o, "process list could not be read")
}

func TestRetireRefusesASymlinkedDataDirectory(t *testing.T) {
	home := seedClaudeMemHome(t)
	seedImport(t, home)
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.Rename(filepath.Join(home, ".claude-mem"), target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(home, ".claude-mem")); err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	refused(t, home, rec, applyOpts(home, rec), "symlink")
	if raw, err := os.ReadFile(filepath.Join(target, "claude-mem.db")); err != nil || string(raw) != "SQLITE-DATA" {
		t.Fatal("the symlink target changed")
	}
}

func TestRetireHonoursClaudeMemDataDirForTheOwnHome(t *testing.T) {
	home := seedClaudeMemHome(t)
	seedImport(t, home)
	custom := filepath.Join(t.TempDir(), "mem-data")
	mustWriteFile(t, filepath.Join(custom, "claude-mem.db"), "CUSTOM")
	rec := &recorder{}
	o := applyOpts(home, rec)
	o.Ambient = []string{"CLAUDE_MEM_DATA_DIR=" + custom, "PATH=/usr/bin"}
	result, err := RetireClaudeMem(context.Background(), o)
	if err != nil || result.Blocked != "" {
		t.Fatalf("%+v %v", result, err)
	}
	if want := custom + ".archive-20261024"; result.Archive != want {
		t.Fatalf("archive %q, want %q", result.Archive, want)
	}
	if raw, _ := os.ReadFile(filepath.Join(result.Archive, "claude-mem.db")); string(raw) != "CUSTOM" {
		t.Fatal("the custom data directory was not the one archived")
	}
	if raw, _ := os.ReadFile(filepath.Join(home, ".claude-mem", "claude-mem.db")); string(raw) != "SQLITE-DATA" {
		t.Fatal("the default directory was touched although CLAUDE_MEM_DATA_DIR names another")
	}
}

// A file that cannot be read or parsed is unknown: never absent, never clean, and the apply names it.
func TestRetireUnreadableEvidenceBlocksTheApplyAndFailsTheReadBack(t *testing.T) {
	cases := map[string]func(t *testing.T, home string) string{
		"JSONC qwen settings": func(t *testing.T, home string) string {
			p := filepath.Join(home, ".qwen", "settings.json")
			mustWriteFile(t, p, "{\n  // a comment\n  \"mcpServers\": {\"claude-mem\": {\"command\": \"/x\"}}\n}\n")
			return p
		},
		"unparseable installed_plugins": func(t *testing.T, home string) string {
			p := filepath.Join(home, ".claude", "plugins", "installed_plugins.json")
			mustWriteFile(t, p, "{not json")
			return p
		},
		"mcpServers is a string": func(t *testing.T, home string) string {
			p := filepath.Join(home, ".kiro", "settings", "mcp.json")
			mustWriteFile(t, p, `{"mcpServers": "oops"}`)
			return p
		},
	}
	for name, seed := range cases {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			seedImport(t, home)
			target := seed(t, home)
			rec := &recorder{}
			dry, err := RetireClaudeMem(context.Background(), retireOpts(home, rec))
			if err != nil {
				t.Fatal(err)
			}
			if dry.Clean() {
				t.Fatalf("an unreadable file read back as clean: %+v", dry.ReadBack)
			}
			sawUnknown := false
			for _, step := range dry.Steps {
				if step.Target == target && step.Status == RetireAbsent {
					t.Errorf("%s was marked absent although it could not be read", target)
				}
				if step.Status == RetireUnknown && (step.Target == target || strings.Contains(step.Detail, "not valid JSON") || strings.Contains(step.Detail, "not an object")) {
					sawUnknown = true
				}
			}
			if !sawUnknown {
				t.Fatalf("no step reports %s as unknown: %v", target, stepIDs(dry))
			}
			refused(t, home, rec, applyOpts(home, rec), "could not read")
		})
	}
}

func TestRetirePermissionDeniedSettingsIsNamedNotSkipped(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads every file")
	}
	home := t.TempDir()
	settings := filepath.Join(home, ".claude", "settings.json")
	mustWriteFile(t, settings, "{\"enabledPlugins\": {\"claude-mem@thedotmack\": true}}\n")
	if err := os.Chmod(settings, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(settings, 0o600) })
	result, err := RetireClaudeMem(context.Background(), retireOpts(home, &recorder{}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(result.Manual, "\n"), settings+": could not be read") {
		t.Fatalf("an unreadable settings.json was skipped silently: %v", result.Manual)
	}
	if result.Clean() {
		t.Fatalf("read back as clean: %+v", result.ReadBack)
	}
}

// Residue the read-back must catch, one kind at a time.
func TestRetireReadBackFailsClosedOnEachKindOfResidue(t *testing.T) {
	cases := map[string]func(t *testing.T, home string, o *RetireOptions){
		"process": func(t *testing.T, home string, o *RetireOptions) {
			o.Ps = func(context.Context) (string, error) {
				return "bun /x/plugins/claude-mem/scripts/worker-service.cjs --daemon\n", nil
			}
		},
		"unreadable process list": func(t *testing.T, home string, o *RetireOptions) {
			o.Ps = func(context.Context) (string, error) { return "", os.ErrPermission }
		},
		"port": func(t *testing.T, home string, o *RetireOptions) { o.PortOpen = func(int) bool { return true } },
		"worker launchd agent": func(t *testing.T, home string, o *RetireOptions) {
			mustWriteFile(t, filepath.Join(home, "Library", "LaunchAgents", claudeMemWorkerLabel+".plist"), "<plist/>")
		},
		"installed plugin": func(t *testing.T, home string, o *RetireOptions) {
			mustWriteJSON(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"), map[string]any{"plugins": map[string]any{claudeMemPluginID: []any{}}})
		},
		"codex config": func(t *testing.T, home string, o *RetireOptions) {
			mustWriteFile(t, filepath.Join(home, ".codex", "config.toml"), "[plugins.\"claude-mem@claude-mem-local\"]\nenabled = true\n")
		},
		"settings line": func(t *testing.T, home string, o *RetireOptions) {
			mustWriteFile(t, filepath.Join(home, ".claude", "settings.json"), "{\n  \"enabledPlugins\": {\"claude-mem@thedotmack\": true}\n}\n")
		},
		"mcp entry": func(t *testing.T, home string, o *RetireOptions) {
			mustWriteJSON(t, filepath.Join(home, ".qwen", "settings.json"), map[string]any{"mcpServers": map[string]any{"claude-mem": map[string]any{}}})
		},
	}
	for name, residue := range cases {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			o := retireOpts(home, &recorder{})
			residue(t, home, &o)
			result, err := RetireClaudeMem(context.Background(), o)
			if err != nil {
				t.Fatal(err)
			}
			if result.Clean() {
				t.Fatalf("residue %q passed the read-back: %+v", name, result.ReadBack)
			}
		})
	}
}

// Residue dot does not own must say what to edit, not just fail: a claude-mem line in the Codex config, and the worker
// LaunchAgent that claude-mem itself installed.
func TestRetireReadBackResidueThatDotDoesNotOwnComesWithEditLines(t *testing.T) {
	home := t.TempDir()
	codex := filepath.Join(home, ".codex", "config.toml")
	mustWriteFile(t, codex, "model = \"x\"\n[plugins.\"claude-mem@claude-mem-local\"]\nenabled = true\n")
	worker := filepath.Join(home, "Library", "LaunchAgents", claudeMemWorkerLabel+".plist")
	mustWriteFile(t, worker, "<string>claude-mem</string>\n")
	result, err := RetireClaudeMem(context.Background(), retireOpts(home, &recorder{}))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(result.Manual, "\n")
	for _, want := range []string{codex + " line 2: remove [plugins.\"claude-mem@claude-mem-local\"]", worker + " line 1:"} {
		if !strings.Contains(joined, want) {
			t.Errorf("no edit line %q in:\n%s", want, joined)
		}
	}
}

// The native commands rewrite enabledPlugins, so the lines to edit are read again after them.
func TestRetireManualEditsAreRecomputedAfterTheNativeCommands(t *testing.T) {
	home := seedClaudeMemHome(t)
	seedImport(t, home)
	settings := filepath.Join(home, ".claude", "settings.json")
	rec := &recorder{}
	o := applyOpts(home, rec)
	o.Run = func(ctx context.Context, env []string, name string, args ...string) (string, error) {
		if name == "claude" && len(args) > 1 && args[1] == "uninstall" {
			// what the real CLI does: drop its own enabledPlugins entry
			mustWriteFile(t, settings, "{\n  \"permissions\": {\n    \"allow\": [\n      \"mcp__plugin_claude-mem_mcp-search\"\n    ]\n  }\n}\n")
		}
		return rec.run(ctx, env, name, args...)
	}
	result, err := RetireClaudeMem(context.Background(), o)
	if err != nil || result.Blocked != "" {
		t.Fatalf("%+v %v", result, err)
	}
	joined := strings.Join(result.Manual, "\n")
	if strings.Contains(joined, "claude-mem@thedotmack") {
		t.Fatalf("a stale line (already removed by the CLI) is still listed:\n%s", joined)
	}
	if !strings.Contains(joined, settings+" line 4: remove \"mcp__plugin_claude-mem_mcp-search\"") {
		t.Fatalf("the line that remains is not listed with its new number:\n%s", joined)
	}
}

func TestRetireNativeFailureIsReportedPerStepAndTheRestStillRuns(t *testing.T) {
	home := seedClaudeMemHome(t)
	seedImport(t, home)
	rec := &recorder{fail: map[string]bool{"claude plugin uninstall " + claudeMemPluginID: true}}
	result, _ := RetireClaudeMem(context.Background(), applyOpts(home, rec))
	got := stepIDs(result)
	if got["claude-plugin-uninstall"] != RetireFailed || got["codex-plugin"] != RetireDone || got["archive"] != RetireDone {
		t.Fatalf("per-step status wrong: %v", got)
	}
}

func TestRetireArchiveNameNeverCollides(t *testing.T) {
	home := seedClaudeMemHome(t)
	mustWriteFile(t, filepath.Join(home, ".claude-mem.archive-20261024", "older"), "keep")
	seedImport(t, home)
	result, _ := RetireClaudeMem(context.Background(), applyOpts(home, &recorder{}))
	if result.Archive != filepath.Join(home, ".claude-mem.archive-20261024-2") {
		t.Fatalf("archive %q", result.Archive)
	}
	if raw, _ := os.ReadFile(filepath.Join(home, ".claude-mem.archive-20261024", "older")); string(raw) != "keep" {
		t.Fatal("an earlier archive was overwritten")
	}
}

// A dangling symlink holding the dated name still counts as taken (Lstat, not Stat).
func TestRetireArchiveNameSkipsADanglingSymlink(t *testing.T) {
	home := seedClaudeMemHome(t)
	if err := os.Symlink(filepath.Join(home, "nowhere"), filepath.Join(home, ".claude-mem.archive-20261024")); err != nil {
		t.Fatal(err)
	}
	seedImport(t, home)
	result, _ := RetireClaudeMem(context.Background(), applyOpts(home, &recorder{}))
	if result.Archive != filepath.Join(home, ".claude-mem.archive-20261024-2") {
		t.Fatalf("archive %q: the dangling name was reused", result.Archive)
	}
}

func TestRetireNeedsAnAbsoluteHome(t *testing.T) {
	if _, err := RetireClaudeMem(context.Background(), RetireOptions{HomeDir: "relative"}); err == nil {
		t.Fatal("accepted a relative home")
	}
}

// The worker port is read from the data directory, or from the newest dated archive once it moved.
func TestRetireWorkerPortReadFromTheDataDirOrTheDatedArchive(t *testing.T) {
	for name, where := range map[string]string{"data dir": ".claude-mem", "dated archive": ".claude-mem.archive-20261024"} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			mustWriteFile(t, filepath.Join(home, where, "worker.port"), "41999\n")
			var asked []int
			o := retireOpts(home, &recorder{})
			o.PortOpen = func(p int) bool { asked = append(asked, p); return false }
			if _, err := RetireClaudeMem(context.Background(), o); err != nil {
				t.Fatal(err)
			}
			if len(asked) != 1 || asked[0] != 41999 {
				t.Fatalf("checked ports %v, want [41999]", asked)
			}
		})
	}
}

// "plugins" present but not an object (a list, a string) cannot be searched for claude-mem: that is unknown, not absent.
func TestRetireAPluginsKeyThatIsNotAnObjectIsUnknownNotAbsent(t *testing.T) {
	home := t.TempDir()
	installed := filepath.Join(home, ".claude", "plugins", "installed_plugins.json")
	mustWriteFile(t, installed, `{"plugins": ["claude-mem@thedotmack"]}`)
	dry, err := RetireClaudeMem(context.Background(), retireOpts(home, &recorder{}))
	if err != nil {
		t.Fatal(err)
	}
	if got := stepIDs(dry)["claude-plugin-uninstall"]; got != RetireUnknown {
		t.Fatalf("a plugins list was read as %q, want %q", got, RetireUnknown)
	}
	if dry.Clean() {
		t.Fatalf("read back as clean: %+v", dry.ReadBack)
	}
	rec := &recorder{}
	seedImport(t, home)
	refused(t, home, rec, applyOpts(home, rec), installed)
}
