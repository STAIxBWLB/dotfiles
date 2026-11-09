package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/aisettings"
)

// fakeAres puts an `ares` on PATH that reports version (and publishes no manifest).
func fakeAres(t *testing.T, version string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "ares")
	writeCLITestFile(t, path, "#!/bin/sh\n[ \"$1\" = \"--version\" ] || exit 2\necho "+version+"\n")
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return path
}

func researchFixture(t *testing.T) (home string, files map[string]string) {
	t.Helper()
	home = t.TempDir()
	files = map[string]string{
		"kiro":    filepath.Join(home, ".kiro", "settings", "mcp.json"),
		"copilot": filepath.Join(home, ".copilot", "mcp-config.json"),
		"claude":  filepath.Join(home, ".claude.json"),
		"codex":   filepath.Join(home, ".codex", "config.toml"),
	}
	writeCLITestFile(t, files["kiro"], `{"mcpServers":{"other":{"command":"x"}},"keep":true}`)
	writeCLITestFile(t, files["copilot"], `{"mcpServers":{}}`)
	writeCLITestFile(t, files["claude"], `{"theme":"dark"}`)
	writeCLITestFile(t, files["codex"], "model = \"gpt\"\n")
	writeCLITestFile(t, ssotFor(t, home), "# Rules\n\nkeep me\n")
	return home, files
}

// AC2: the dry run lists every file it would touch and writes none of them.
func TestResearchInstallDryRunListsEveryFileAndWritesNothing(t *testing.T) {
	fakeAres(t, "0.1.0")
	home, files := researchFixture(t)
	before := snapshotFiles(t, files)
	ssotBefore := readFile(t, ssotFor(t, home))
	out, errOut, err := runDotForTest("--home", home, "--dry-run", "ai", "mcp", "install", "ares")
	if err != nil {
		t.Fatalf("%v %s", err, errOut)
	}
	for _, want := range []string{"would write " + files["kiro"], "would write " + files["copilot"], "would write the research block into " + ssotFor(t, home), "ares setup all --apply", "ares setup agy --apply"} {
		if !strings.Contains(out, want) {
			t.Fatalf("dry run output misses %q:\n%s", want, out)
		}
	}
	if !equalMaps(snapshotFiles(t, files), before) || readFile(t, ssotFor(t, home)) != ssotBefore {
		t.Fatal("dry run changed files")
	}
}

// AC1/AC2: install registers `ares mcp` for Kiro and Copilot and the research block, and a second run changes nothing.
func TestResearchInstallRegistersKiroAndCopilotIdempotently(t *testing.T) {
	ares := fakeAres(t, "0.1.0")
	home, files := researchFixture(t)
	claudeBefore, codexBefore := readFile(t, files["claude"]), readFile(t, files["codex"])
	cfg := writeAgentsConfig(t, home, "kiro", "copilot")
	for i := 0; i < 2; i++ {
		if _, errOut, err := runDotForTest("--home", home, "--config", cfg, "ai", "mcp", "install", "ares"); err != nil {
			t.Fatalf("%v %s", err, errOut)
		}
	}
	var kiro struct {
		Servers map[string]map[string]any `json:"mcpServers"`
		Keep    bool                      `json:"keep"`
	}
	if err := json.Unmarshal([]byte(readFile(t, files["kiro"])), &kiro); err != nil {
		t.Fatal(err)
	}
	if kiro.Servers["ares"]["command"] != ares || kiro.Servers["other"] == nil || !kiro.Keep {
		t.Fatalf("kiro config: %+v", kiro)
	}
	if copilot := readFile(t, files["copilot"]); !strings.Contains(copilot, `"ares"`) || !strings.Contains(copilot, `"tools": [`) {
		t.Fatalf("copilot entry shape:\n%s", copilot)
	}
	ssot := readFile(t, ssotFor(t, home))
	if strings.Count(ssot, "<!-- dotfiles:ares:start -->") != 1 || strings.Count(ssot, "<!-- dotfiles:ares:end -->") != 1 || !strings.Contains(ssot, "keep me") || !strings.Contains(ssot, "never auto-approved") {
		t.Fatalf("research block:\n%s", ssot)
	}
	if readFile(t, files["claude"]) != claudeBefore || readFile(t, files["codex"]) != codexBefore {
		t.Fatal("install touched a file dot does not own")
	}
	out, _, err := runDotForTest("--home", home, "--config", cfg, "--dry-run", "ai", "mcp", "install", "ares")
	if err != nil || !strings.Contains(out, "Nothing to change") {
		t.Fatalf("not idempotent: %v\n%s", err, out)
	}
	out, errOut, err := runDotForTest("--home", home, "--config", cfg, "ai", "mcp", "status", "ares")
	if err != nil || strings.Count(out, "ready") != 3 || !strings.Contains(out, "0.1.0 (supported "+aisettings.AresSupportedRange+", compiled fallback)") || strings.Contains(out+errOut, "outside the supported range") {
		t.Fatalf("status after install: %v\n%s%s", err, out, errOut)
	}
}

// AC1: without ares on PATH, install refuses and writes nothing; status says so.
func TestResearchWithoutAres(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	home, files := researchFixture(t)
	before := snapshotFiles(t, files)
	if _, _, err := runDotForTest("--home", home, "ai", "mcp", "install", "ares"); err == nil || !strings.Contains(err.Error(), "ares is not on PATH") {
		t.Fatalf("expected refusal, got %v", err)
	}
	if !equalMaps(snapshotFiles(t, files), before) {
		t.Fatal("refused install changed files")
	}
	out, errOut, err := runDotForTest("--home", home, "ai", "mcp", "status", "ares")
	if err != nil || !strings.Contains(out, "not found on PATH") || !strings.Contains(out, "missing") {
		t.Fatalf("status without ares: %v\n%s%s", err, out, errOut)
	}
}

// AC1: status warns when the installed ares is outside the supported range.
func TestResearchStatusWarnsOnVersionMismatch(t *testing.T) {
	fakeAres(t, "1.2.0")
	home, _ := researchFixture(t)
	out, errOut, err := runDotForTest("--home", home, "ai", "mcp", "status", "ares")
	if err != nil {
		t.Fatalf("%v %s", err, errOut)
	}
	if !strings.Contains(out+errOut, "installed ares 1.2.0 is outside the supported range "+aisettings.AresSupportedRange) {
		t.Fatalf("no version warning:\n%s%s", out, errOut)
	}
}
