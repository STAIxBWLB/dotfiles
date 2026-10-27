package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/aisettings"
)

// The tests here run the real root command with --home, the way a person does, against a temp home.
// Nothing touches the invoking user's real files: --home isolates every path dot resolves.

func ssotFor(t *testing.T, home string) string {
	t.Helper()
	return filepath.Join(home, aisettings.AgentsSSOTRelPath, aisettings.AgentsSSOTName)
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

const cliLegacyBlock = "<!-- dotfiles:claude-mem:start -->\n## Persistent Memory\n\n- query the `claude-mem` MCP server\n<!-- dotfiles:claude-mem:end -->"

// AC1: `dot ai agents apply` writes the amem + vault block, removes the claude-mem block, and a
// re-apply leaves exactly one.
func TestAgentsApplyWritesAmemBlockAndRemovesClaudeMemBlockOnce(t *testing.T) {
	fakeAmem(t) // the block is written only where amem is in use; never depend on the host having it
	home := t.TempDir()
	ssot := ssotFor(t, home)
	writeCLITestFile(t, ssot, "# My rules\n\nkeep me\n\n"+cliLegacyBlock+"\n\n## Tail\n\nkeep me too\n")
	for i := 0; i < 3; i++ {
		if _, errOut, err := runDotForTest("--home", home, "ai", "agents", "apply", "--tool", "codex"); err != nil {
			t.Fatalf("apply %d: %v\nstderr=%s", i, err, errOut)
		}
		text := readFile(t, ssot)
		if got := strings.Count(text, "<!-- dotfiles:amem:start -->"); got != 1 {
			t.Fatalf("apply %d: %d amem blocks, want exactly 1:\n%s", i, got, text)
		}
		if strings.Contains(text, "claude-mem") {
			t.Fatalf("apply %d: the claude-mem block or a mention survived:\n%s", i, text)
		}
		for _, keep := range []string{"# My rules", "keep me", "## Tail", "keep me too", "amem_search", "Vault-First"} {
			if !strings.Contains(text, keep) {
				t.Fatalf("apply %d lost %q:\n%s", i, keep, text)
			}
		}
	}
	// The rendered target carries the same block.
	target := readFile(t, filepath.Join(home, ".codex", "AGENTS.md"))
	if strings.Count(target, "<!-- dotfiles:amem:start -->") != 1 || strings.Contains(target, "claude-mem") {
		t.Fatalf("rendered target not updated exactly once:\n%s", target)
	}
}

func TestAgentsApplyDryRunWritesNeitherTheSSOTNorTargets(t *testing.T) {
	fakeAmem(t) // without amem the apply is a no-op and this test would pass vacuously
	home := t.TempDir()
	ssot := ssotFor(t, home)
	body := "# rules\n\n" + cliLegacyBlock + "\n"
	writeCLITestFile(t, ssot, body)
	if _, errOut, err := runDotForTest("--home", home, "--dry-run", "ai", "agents", "apply", "--tool", "codex"); err != nil {
		t.Fatalf("%v\n%s", err, errOut)
	}
	if got := readFile(t, ssot); got != body {
		t.Fatalf("dry run rewrote the SSOT:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "AGENTS.md")); err == nil {
		t.Fatal("dry run wrote a target")
	}
}

// AC2: `dot ai memory install` on a clean HOME registers amem for Kiro and Copilot, writes no launchd
// agent, leaves the `amem setup` clients alone, and prints the `amem setup` lines.
func fakeAmem(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "amem")
	writeCLITestFile(t, path, "#!/bin/sh\necho 0.11.0\n")
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return path
}

func TestMemoryInstallOnCleanHomeRegistersKiroAndCopilotWithNoLaunchAgent(t *testing.T) {
	home := t.TempDir()
	amem := fakeAmem(t)
	cfg := writeAgentsConfig(t, home, "kimi", "qwen", "antigravity", "kiro", "copilot")
	out, errOut, err := runDotForTest("--home", home, "--config", cfg, "ai", "memory", "install")
	if err != nil {
		t.Fatalf("install: %v\nstderr=%s", err, errOut)
	}
	for _, path := range []string{filepath.Join(home, ".kiro", "settings", "mcp.json"), filepath.Join(home, ".copilot", "mcp-config.json")} {
		var doc struct {
			Servers map[string]struct {
				Command string   `json:"command"`
				Args    []string `json:"args"`
			} `json:"mcpServers"`
		}
		if err := json.Unmarshal([]byte(readFile(t, path)), &doc); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		entry, ok := doc.Servers["amem"]
		if !ok || entry.Command != amem || len(entry.Args) != 1 || entry.Args[0] != "mcp" {
			t.Errorf("%s: amem entry %+v, want command %s args [mcp]", path, entry, amem)
		}
	}
	for _, want := range []string{"amem setup all --apply", "amem setup agy --apply", "amem ingest codex", "No LaunchAgent is written"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "Library")); err == nil {
		t.Fatal("install created ~/Library: no launchd agent may be written for amem")
	}
	for _, forbidden := range []string{".claude-mem", ".claude", ".codex", ".kimi-code", ".qwen", ".gemini"} {
		if _, err := os.Stat(filepath.Join(home, forbidden)); err == nil {
			t.Errorf("install wrote %s, a path it does not own", forbidden)
		}
	}
}

func TestMemoryInstallDryRunWritesNothingAndSaysWhatItWould(t *testing.T) {
	home := t.TempDir()
	fakeAmem(t)
	cfg := writeAgentsConfig(t, home, "kimi", "qwen", "antigravity", "kiro", "copilot")
	out, errOut, err := runDotForTest("--home", home, "--config", cfg, "--dry-run", "ai", "memory", "install")
	if err != nil {
		t.Fatalf("%v\n%s", err, errOut)
	}
	if !strings.Contains(out, "would write") {
		t.Fatalf("dry run did not say what it would write:\n%s", out)
	}
	for _, p := range []string{filepath.Join(home, ".kimi-code"), filepath.Join(home, ".qwen"), filepath.Join(home, ".kiro"), filepath.Join(home, ".copilot")} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("dry run created %s", p)
		}
	}
	// AC: the dry run lists only the Kiro and Copilot config paths.
	var listed []string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "would write ") {
			listed = append(listed, strings.TrimSpace(line[strings.Index(line, "would write ")+len("would write "):]))
		}
	}
	want := []string{filepath.Join(home, ".kiro", "settings", "mcp.json"), filepath.Join(home, ".copilot", "mcp-config.json")}
	if strings.Join(listed, ",") != strings.Join(want, ",") {
		t.Fatalf("dry run listed %v, want %v:\n%s", listed, want, out)
	}
}

// A selection of only `amem setup` clients writes nothing and must not claim amem is already registered.
func TestMemoryInstallWithOnlyAmemSetupClientsSaysWhyNothingIsWritten(t *testing.T) {
	home := t.TempDir()
	fakeAmem(t)
	cfg := writeAgentsConfig(t, home, "kimi", "qwen")
	out, errOut, err := runDotForTest("--home", home, "--config", cfg, "ai", "memory", "install")
	if err != nil {
		t.Fatalf("%v\n%s", err, errOut)
	}
	if !strings.Contains(out, "or are not selected") || strings.Contains(out, "wrote ") {
		t.Fatalf("unclear or wrong output:\n%s", out)
	}
	for _, p := range []string{".kimi-code", ".qwen", ".kiro", ".copilot"} {
		if _, err := os.Stat(filepath.Join(home, p)); err == nil {
			t.Errorf("install created %s", p)
		}
	}
}

func TestMemoryInstallWithoutAmemOnPathExplainsTheExactInstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	cfg := writeAgentsConfig(t, home, "kiro")
	_, errOut, err := runDotForTest("--home", home, "--config", cfg, "ai", "memory", "install")
	if err == nil || !strings.Contains(err.Error()+errOut, "@staix/agent-mem "+aisettings.AmemPinnedVersion) {
		t.Fatalf("no actionable message: %v\n%s", err, errOut)
	}
	if _, statErr := os.Stat(filepath.Join(home, ".kiro")); statErr == nil {
		t.Fatal("wrote a config although amem is missing")
	}
}

func TestMemoryStatusSaysWhichAgentsAreReady(t *testing.T) {
	home := t.TempDir()
	fakeAmem(t)
	cfg := writeAgentsConfig(t, home, "kimi", "qwen", "antigravity", "kiro", "copilot")
	out, _, err := runDotForTest("--home", home, "--config", cfg, "ai", "memory", "status")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "ready") {
		t.Fatalf("a clean home reported ready:\n%s", out)
	}
	if _, errOut, err := runDotForTest("--home", home, "--config", cfg, "ai", "memory", "install"); err != nil {
		t.Fatalf("%v\n%s", err, errOut)
	}
	out, _, err = runDotForTest("--home", home, "--config", cfg, "ai", "memory", "status")
	if err != nil {
		t.Fatal(err)
	}
	for _, agent := range []string{"kiro", "copilot"} {
		line := lineWith(out, agent)
		if !strings.Contains(line, "ready") {
			t.Errorf("%s not ready after install:\n%s", agent, out)
		}
	}
	// AC: the `amem setup` clients read as managed by amem, never as missing.
	for _, agent := range []string{"kimi", "qwen", "agy"} {
		if line := lineWith(out, agent); !strings.Contains(line, "managed by `amem setup") || strings.Contains(line, "missing") {
			t.Errorf("%s: %q, want managed by amem setup", agent, line)
		}
	}
	if !strings.Contains(out, "expected "+aisettings.AmemPinnedVersion) {
		t.Errorf("status does not name the pinned version:\n%s", out)
	}
	// AC: amem 0.11.0 on PATH is the pin, so no mismatch is reported.
	if strings.Contains(out, "not the pinned version") {
		t.Errorf("amem %s reported as a mismatch:\n%s", aisettings.AmemPinnedVersion, out)
	}
}

func lineWith(text, needle string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	return ""
}

func writeAgentsConfig(t *testing.T, home string, agents ...string) string {
	t.Helper()
	quoted := make([]string, len(agents))
	for i, a := range agents {
		quoted[i] = `"` + a + `"`
	}
	path := filepath.Join(t.TempDir(), "dot.yaml")
	writeCLITestFile(t, path, "modules:\n  ai:\n    enabled: true\n    tooling:\n      agents: ["+strings.Join(quoted, ", ")+"]\n")
	return path
}

// AC4: the retirement dry run from the real command lists every item, reads back what is left,
// and changes nothing.
func seedClaudeMemFootprint(t *testing.T, home string) {
	t.Helper()
	writeCLITestFile(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"), `{"plugins":{"claude-mem@thedotmack":[{"scope":"user"}]}}`)
	writeCLITestFile(t, filepath.Join(home, ".claude", "plugins", "known_marketplaces.json"), `{"thedotmack":{}}`)
	writeCLITestFile(t, filepath.Join(home, ".claude", "settings.json"), "{\n  \"enabledPlugins\": {\n    \"claude-mem@thedotmack\": true\n  }\n}\n")
	writeCLITestFile(t, filepath.Join(home, ".codex", "config.toml"), "[plugins.\"claude-mem@claude-mem-local\"]\nenabled = true\n")
	writeCLITestFile(t, filepath.Join(home, ".qwen", "settings.json"), `{"mcpServers":{"claude-mem":{"command":"/x"},"other":{"command":"/y"}}}`)
	writeCLITestFile(t, filepath.Join(home, "Library", "LaunchAgents", "com.dotfiles.claude-mem-bridge.plist"), "<plist/>")
	writeCLITestFile(t, filepath.Join(home, "Library", "LaunchAgents", "com.dotfiles.claude-mem-sync.plist"), "<plist/>")
	writeCLITestFile(t, filepath.Join(home, ".claude-mem", "claude-mem.db"), "DATA")
}

func treeOf(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			raw, _ := os.ReadFile(path)
			out[path] = string(raw)
		}
		return nil
	})
	return out
}

func TestRetireClaudeMemDryRunIsTheDefaultAndChangesNothing(t *testing.T) {
	home := t.TempDir()
	seedClaudeMemFootprint(t, home)
	before := treeOf(t, home)
	out, errOut, err := runDotForTest("--home", home, "ai", "memory", "retire-claude-mem")
	if err != nil {
		t.Fatalf("dry run failed: %v\n%s\n%s", err, out, errOut)
	}
	after := treeOf(t, home)
	if len(before) != len(after) {
		t.Fatalf("a dry run changed the file set: %d -> %d", len(before), len(after))
	}
	for path, body := range before {
		if after[path] != body {
			t.Errorf("a dry run changed %s", path)
		}
	}
	for _, want := range []string{
		"dry run: nothing is changed",
		"claude plugin disable claude-mem@thedotmack", "claude plugin uninstall claude-mem@thedotmack", "claude plugin marketplace remove thedotmack",
		"codex plugin remove claude-mem --marketplace claude-mem-local", "codex plugin marketplace remove claude-mem-local",
		"claude-mem entry under mcpServers in " + filepath.Join(home, ".qwen", "settings.json"),
		"com.dotfiles.claude-mem-bridge", "com.dotfiles.claude-mem-sync",
		"move " + filepath.Join(home, ".claude-mem") + " to ", "(never deleted)",
		"Edit by hand", filepath.Join(home, ".claude", "settings.json") + " line 3: remove \"claude-mem@thedotmack\": true",
		"Read-back", "[FOUND ] no claude-mem plugin or hook",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run output missing %q:\n%s", want, out)
		}
	}
}

// Under --home the native claude and codex CLIs and launchd would act on the invoking user's real state, so --apply
// is refused outright, whatever else is true, with nothing changed. The dry run still plans against that home.
func TestRetireClaudeMemApplyUnderHomeIsRefusedAndChangesNothing(t *testing.T) {
	home := t.TempDir()
	seedClaudeMemFootprint(t, home)
	// even with every other precondition satisfied
	writeCLITestFile(t, filepath.Join(home, ".agenthub", "memory", "projects", "abc", "import-state.json"), `{"k": 42}`)
	before := treeOf(t, home)
	for _, args := range [][]string{{"--apply"}, {"--apply", "--import-complete"}} {
		_, errOut, err := runDotForTest(append([]string{"--home", home, "ai", "memory", "retire-claude-mem"}, args...)...)
		if err == nil || !strings.Contains(err.Error()+errOut, "refusing to apply under --home") {
			t.Fatalf("%v under --home was not refused: %v\n%s", args, err, errOut)
		}
		after := treeOf(t, home)
		if len(after) != len(before) {
			t.Fatalf("a refused apply changed the file set: %d -> %d", len(before), len(after))
		}
		for path, body := range before {
			if after[path] != body {
				t.Errorf("a refused apply changed %s", path)
			}
		}
	}
	if _, statErr := os.Stat(filepath.Join(home, ".claude-mem", "claude-mem.db")); statErr != nil {
		t.Fatal("a refused apply moved the data")
	}
}

func TestRetireClaudeMemDryRunFlagBeatsApply(t *testing.T) {
	home := t.TempDir()
	seedClaudeMemFootprint(t, home)
	before := treeOf(t, home)
	if _, _, err := runDotForTest("--home", home, "--dry-run", "ai", "memory", "retire-claude-mem", "--apply", "--import-complete"); err != nil {
		t.Fatal(err)
	}
	after := treeOf(t, home)
	for path, body := range before {
		if after[path] != body {
			t.Errorf("--dry-run did not win over --apply: %s changed", path)
		}
	}
}

// A saved selection that still lists the retired add-on keeps working, without it. `tools status`
// loads the selection and validates it against the catalog, which is what `dot apply` does.
func TestSavedSelectionNamingClaudeMemKeepsWorking(t *testing.T) {
	home := t.TempDir()
	cfg := filepath.Join(t.TempDir(), "dot.yaml")
	writeCLITestFile(t, cfg, "modules:\n  ai:\n    enabled: true\n    tooling:\n      agents: [\"qwen\"]\n      tools: [\"claude-mem\"]\n      pins:\n        claude-mem: \"13.28.0\"\n")
	out, errOut, err := runDotForTest("--home", home, "--config", cfg, "ai", "tools", "status", "--json")
	if err != nil {
		t.Fatalf("a selection that names claude-mem now fails: %v\n%s", err, errOut)
	}
	if strings.Contains(out, "claude-mem") {
		t.Fatalf("claude-mem is still reported:\n%s", out)
	}
}

func TestToolsListNamesFourAddonsAndNoMemoryAddon(t *testing.T) {
	out, errOut, err := runDotForTest("--home", t.TempDir(), "ai", "tools", "list")
	if err != nil {
		t.Fatalf("%v\n%s", err, errOut)
	}
	if strings.Contains(out, "claude-mem") || strings.Contains(out, "amem") {
		t.Fatalf("a memory add-on is still in the catalog:\n%s", out)
	}
	for _, want := range []string{"ripwire", "ocr", "gsd", "ponytail"} {
		if !strings.Contains(out, want) {
			t.Errorf("catalog lost %s:\n%s", want, out)
		}
	}
}

// An agent outside the saved selection is "not selected", never "missing": dot was told not to manage it.
func TestMemoryStatusDoesNotCallAnUnselectedAgentMissing(t *testing.T) {
	home := t.TempDir()
	fakeAmem(t)
	cfg := writeAgentsConfig(t, home, "kiro")
	out, _, err := runDotForTest("--home", home, "--config", cfg, "ai", "memory", "status")
	if err != nil {
		t.Fatal(err)
	}
	if line := lineWith(out, "kiro"); !strings.Contains(line, "missing") {
		t.Errorf("a selected agent without amem must read missing:\n%s", out)
	}
	for _, agent := range []string{"copilot"} {
		if line := lineWith(out, agent); !strings.Contains(line, "not selected") || strings.Contains(line, "missing") {
			t.Errorf("%s: %q, want not selected", agent, line)
		}
	}
}

// status warns unless amem reports exactly the pinned version: a longer number that merely contains it is not it.
func TestContainsVersionMatchesTheWholeNumberOnly(t *testing.T) {
	for output, want := range map[string]bool{
		"0.11.0": true, "amem 0.11.0": true, "0.11.0\n": true, "agent-mem v0.11.0 (bun)": true,
		"10.11.0": false, "0.11.01": false, "0.11.0.1": false, "0.11": false, "1.0.11.0": false,
		"0.6.0": false, "": false, "amem 0.11.0.": true, "0.11.0-rc1": false, "0.11.0+build5": false, "0.11.0 - ok": true,
	} {
		if got := containsVersion(output, "0.11.0"); got != want {
			t.Errorf("containsVersion(%q) = %v, want %v", output, got, want)
		}
	}
}
