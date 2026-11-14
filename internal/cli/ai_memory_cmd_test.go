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

// AC2: `dot ai mcp install amem` on a clean HOME registers amem for Kiro and Copilot, writes no launchd
// agent, leaves the `amem setup` clients alone, and prints the `amem setup` lines.
func fakeAmem(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "amem")
	writeCLITestFile(t, path, "#!/bin/sh\necho 0.14.0\n")
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
	out, errOut, err := runDotForTest("--home", home, "--config", cfg, "ai", "mcp", "install", "amem")
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
	out, errOut, err := runDotForTest("--home", home, "--config", cfg, "--dry-run", "ai", "mcp", "install", "amem")
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
	out, errOut, err := runDotForTest("--home", home, "--config", cfg, "ai", "mcp", "install", "amem")
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
	_, errOut, err := runDotForTest("--home", home, "--config", cfg, "ai", "mcp", "install", "amem")
	if err == nil || !strings.Contains(err.Error()+errOut, "@staix/agent-mem "+aisettings.AmemSupportedRange) {
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
	out, _, err := runDotForTest("--home", home, "--config", cfg, "ai", "mcp", "status", "amem")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "ready") {
		t.Fatalf("a clean home reported ready:\n%s", out)
	}
	if _, errOut, err := runDotForTest("--home", home, "--config", cfg, "ai", "mcp", "install", "amem"); err != nil {
		t.Fatalf("%v\n%s", err, errOut)
	}
	out, _, err = runDotForTest("--home", home, "--config", cfg, "ai", "mcp", "status", "amem")
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
	if !strings.Contains(out, "supported "+aisettings.AmemSupportedRange) {
		t.Errorf("status does not name the supported range:\n%s", out)
	}
	// AC: amem 0.14.0 on PATH is inside the range, so no mismatch is reported.
	if strings.Contains(out, "outside the supported range") {
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
	out, _, err := runDotForTest("--home", home, "--config", cfg, "ai", "mcp", "status", "amem")
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

func fakeAmemScript(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "amem")
	writeCLITestFile(t, path, "#!/bin/sh\n"+body)
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// #319 AC1: a release newer than the compiled floor is inside the range and needs no dot release; the
// manifest's range replaces the compiled one; a release outside it, or a prerelease, still warns.
func TestMemoryStatusWarnsOnlyOutsideTheSupportedRange(t *testing.T) {
	for _, c := range []struct {
		name, script, want string
		warn               bool
	}{
		{"newer release, no manifest", "echo 0.17.0\n", "0.17.0 (supported " + aisettings.AmemSupportedRange + ", compiled fallback)", false},
		{"older than the floor", "echo 0.13.0\n", "0.13.0 (supported", true},
		{"prerelease", "echo 0.18.0-rc1\n", "0.18.0-rc1 (supported", true},
		{"manifest range", `[ "$1" = manifest ] && { echo '{"name":"@staix/agent-mem","version":"0.20.0","supported":">=0.16.0 <2.0.0"}'; exit 0; }; echo 0.20.0` + "\n", "0.20.0 (supported >=0.16.0 <2.0.0, from its manifest)", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			fakeAmemScript(t, c.script)
			cfg := writeAgentsConfig(t, home, "kiro")
			out, errOut, err := runDotForTest("--home", home, "--config", cfg, "ai", "mcp", "status", "amem")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, c.want) {
				t.Errorf("want %q in:\n%s", c.want, out)
			}
			if got := strings.Contains(out+errOut, "outside the supported range"); got != c.warn {
				t.Errorf("warned=%v, want %v:\n%s%s", got, c.warn, out, errOut)
			}
		})
	}
}
