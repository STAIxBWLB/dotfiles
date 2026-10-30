package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeVmem(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "vmem")
	writeCLITestFile(t, path, "#!/bin/sh\necho 0.1.4\n")
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return path
}

func withoutVmem(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

const obsidianEntry = `{"command":"npx","args":["@bitbonsai/mcpvault@0.12.5","/vault"]}`

func vaultFixture(t *testing.T) (home string, files map[string]string) {
	t.Helper()
	home = t.TempDir()
	files = map[string]string{
		"kiro":    filepath.Join(home, ".kiro", "settings", "mcp.json"),
		"copilot": filepath.Join(home, ".copilot", "mcp-config.json"),
		"claude":  filepath.Join(home, ".claude.json"),
		"codex":   filepath.Join(home, ".codex", "config.toml"),
	}
	writeCLITestFile(t, files["kiro"], `{"mcpServers":{"obsidian":`+obsidianEntry+`,"other":{"command":"x"}},"keep":true}`)
	writeCLITestFile(t, files["copilot"], `{"mcpServers":{"obsidian":`+obsidianEntry+`}}`)
	writeCLITestFile(t, files["claude"], `{"theme":"dark","mcpServers":{"obsidian":`+obsidianEntry+`}}`)
	writeCLITestFile(t, files["codex"], "model = \"gpt\"\n\n[mcp_servers.obsidian]\ncommand = \"npx\"\n\n[mcp_servers.obsidian.env]\nX = \"1\"\n")
	writeCLITestFile(t, ssotFor(t, home), "# Rules\n\nkeep me\n")
	return home, files
}

func snapshotFiles(t *testing.T, files map[string]string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for key, path := range files {
		out[key] = readFile(t, path)
	}
	return out
}

func TestVaultInstallDryRunListsEveryFileAndWritesNothing(t *testing.T) {
	fakeVmem(t)
	home, files := vaultFixture(t)
	before := snapshotFiles(t, files)
	ssotBefore := readFile(t, ssotFor(t, home))
	out, errOut, err := runDotForTest("--home", home, "--dry-run", "ai", "vault", "install")
	if err != nil {
		t.Fatalf("%v %s", err, errOut)
	}
	for _, want := range []string{"would write " + files["kiro"], "would write " + files["copilot"], "would write the vault block into " + ssotFor(t, home), "vmem setup all --apply", "vmem setup agy --apply"} {
		if !strings.Contains(out, want) {
			t.Fatalf("dry run output misses %q:\n%s", want, out)
		}
	}
	if got := snapshotFiles(t, files); !equalMaps(got, before) || readFile(t, ssotFor(t, home)) != ssotBefore {
		t.Fatal("dry run changed files")
	}
}

func TestVaultInstallRegistersKiroAndCopilotIdempotently(t *testing.T) {
	vmem := fakeVmem(t)
	home, files := vaultFixture(t)
	claudeBefore, codexBefore := readFile(t, files["claude"]), readFile(t, files["codex"])
	cfg := writeAgentsConfig(t, home, "kiro", "copilot")
	for i := 0; i < 2; i++ {
		if _, errOut, err := runDotForTest("--home", home, "--config", cfg, "ai", "vault", "install"); err != nil {
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
	if kiro.Servers["vmem"]["command"] != vmem || kiro.Servers["other"] == nil || !kiro.Keep || kiro.Servers["obsidian"] == nil {
		t.Fatalf("kiro config: %+v", kiro)
	}
	if !strings.Contains(readFile(t, files["copilot"]), `"tools": [`) {
		t.Fatal("copilot entry shape")
	}
	ssot := readFile(t, ssotFor(t, home))
	if strings.Count(ssot, "<!-- dotfiles:vmem:start -->") != 1 || !strings.Contains(ssot, "keep me") {
		t.Fatalf("vault block: %s", ssot)
	}
	// Notes go through vmem; ops/ and reports/ are written with `vmem report` and may be read from files.
	for _, want := range []string{"Vault notes are read and written through the `vmem` MCP tools", "written with `vmem report` and may be read read-only from files", "Never edit vault files directly or through another MCP server"} {
		if !strings.Contains(ssot, want) {
			t.Fatalf("vault block misses %q:\n%s", want, ssot)
		}
	}
	if strings.Contains(ssot, "read and written only through") {
		t.Fatal("vault block still forbids read-only file reads of ops/ and reports/")
	}
	if readFile(t, files["claude"]) != claudeBefore || readFile(t, files["codex"]) != codexBefore {
		t.Fatal("install touched a file dot does not own")
	}
	out, _, err := runDotForTest("--home", home, "--config", cfg, "--dry-run", "ai", "vault", "install")
	if err != nil || !strings.Contains(out, "Nothing to change") {
		t.Fatalf("not idempotent: %v\n%s", err, out)
	}
}

func TestVaultStatusWarnsWhileObsidianIsRegistered(t *testing.T) {
	fakeVmem(t)
	home, files := vaultFixture(t)
	out, errOut, err := runDotForTest("--home", home, "ai", "vault", "status")
	if err != nil {
		t.Fatalf("%v %s", err, errOut)
	}
	combined := out + errOut
	for _, path := range []string{files["kiro"], files["copilot"], files["claude"], files["codex"]} {
		if !strings.Contains(combined, "obsidian (@bitbonsai/mcpvault) is still registered in "+path) {
			t.Fatalf("status misses the obsidian warning for %s:\n%s", path, combined)
		}
	}
	if !strings.Contains(combined, "0.1.4 (expected 0.1.4)") {
		t.Fatalf("version line:\n%s", combined)
	}
	clean := t.TempDir()
	writeCLITestFile(t, ssotFor(t, clean), "# Rules\n")
	out, errOut, _ = runDotForTest("--home", clean, "ai", "vault", "status")
	if strings.Contains(out+errOut, "still registered") {
		t.Fatalf("warned without obsidian:\n%s", out+errOut)
	}
}

func TestVaultRetireIsDryRunByDefaultAndNeverTouchesUnownedFiles(t *testing.T) {
	fakeVmem(t)
	home, files := vaultFixture(t)
	cfg := writeAgentsConfig(t, home, "kiro", "copilot")
	if _, errOut, err := runDotForTest("--home", home, "--config", cfg, "ai", "vault", "install"); err != nil {
		t.Fatalf("%v %s", err, errOut)
	}
	before := snapshotFiles(t, files)
	out, errOut, err := runDotForTest("--home", home, "--config", cfg, "ai", "vault", "retire-mcpvault")
	if err != nil {
		t.Fatalf("%v %s", err, errOut)
	}
	for _, want := range []string{"dry run", "would remove mcpServers.obsidian from " + files["kiro"], "would remove mcpServers.obsidian from " + files["copilot"], "claude mcp remove obsidian --scope user", "[mcp_servers.obsidian.*]"} {
		if !strings.Contains(out, want) {
			t.Fatalf("dry run output misses %q:\n%s", want, out)
		}
	}
	if !equalMaps(snapshotFiles(t, files), before) {
		t.Fatal("dry run changed files")
	}
	if _, errOut, err := runDotForTest("--home", home, "--config", cfg, "ai", "vault", "retire-mcpvault", "--apply"); err != nil {
		t.Fatalf("%v %s", err, errOut)
	}
	for _, key := range []string{"kiro", "copilot"} {
		if strings.Contains(readFile(t, files[key]), "obsidian") {
			t.Fatalf("%s still registers obsidian", key)
		}
	}
	if !strings.Contains(readFile(t, files["kiro"]), `"other"`) || !strings.Contains(readFile(t, files["kiro"]), `"keep": true`) {
		t.Fatal("retire dropped unrelated kiro entries")
	}
	if readFile(t, files["claude"]) != before["claude"] || readFile(t, files["codex"]) != before["codex"] {
		t.Fatal("retire touched a file dot does not own")
	}
}

func TestVaultRetireRefusesWithoutVmem(t *testing.T) {
	withoutVmem(t)
	home, files := vaultFixture(t)
	before := snapshotFiles(t, files)
	_, _, err := runDotForTest("--home", home, "ai", "vault", "retire-mcpvault", "--apply")
	if err == nil || !strings.Contains(err.Error(), "vmem is not on PATH") {
		t.Fatalf("expected refusal, got %v", err)
	}
	if !equalMaps(snapshotFiles(t, files), before) {
		t.Fatal("refused run changed files")
	}
	if _, _, err := runDotForTest("--home", home, "ai", "vault", "install"); err == nil {
		t.Fatal("install without vmem succeeded")
	}
}

// P1: retire honors the saved selection. An unselected agent's config is never rewritten.
func TestVaultRetireSkipsUnselectedAgents(t *testing.T) {
	fakeVmem(t)
	home, files := vaultFixture(t)
	both := writeAgentsConfig(t, home, "kiro", "copilot")
	if _, errOut, err := runDotForTest("--home", home, "--config", both, "ai", "vault", "install"); err != nil {
		t.Fatalf("%v %s", err, errOut)
	}
	copilotBefore := readFile(t, files["copilot"])
	onlyKiro := writeAgentsConfig(t, home, "kiro")
	out, errOut, err := runDotForTest("--home", home, "--config", onlyKiro, "ai", "vault", "retire-mcpvault", "--apply")
	if err != nil {
		t.Fatalf("%v %s", err, errOut)
	}
	if strings.Contains(readFile(t, files["kiro"]), `"obsidian"`) {
		t.Fatal("selected kiro still registers obsidian")
	}
	if readFile(t, files["copilot"]) != copilotBefore || strings.Contains(out, files["copilot"]) {
		t.Fatalf("unselected copilot was touched or listed:\n%s", out)
	}
}

// P2: retire removes obsidian only where a working vmem entry already exists; otherwise it skips and says so.
func TestVaultRetireRequiresAWorkingVmemEntry(t *testing.T) {
	fakeVmem(t)
	home, files := vaultFixture(t)
	cfg := writeAgentsConfig(t, home, "kiro", "copilot")
	before := snapshotFiles(t, files)
	for _, args := range [][]string{{"ai", "vault", "retire-mcpvault"}, {"ai", "vault", "retire-mcpvault", "--apply"}} {
		out, errOut, err := runDotForTest(append([]string{"--home", home, "--config", cfg}, args...)...)
		if err != nil {
			t.Fatalf("%v %s", err, errOut)
		}
		combined := out + errOut
		for _, key := range []string{"kiro", "copilot"} {
			if !strings.Contains(combined, files[key]+" has no working vmem entry; run `dot ai vault install` first, obsidian is kept") {
				t.Fatalf("%v: missing skip message for %s:\n%s", args, key, combined)
			}
			if strings.Contains(combined, "remove mcpServers.obsidian from "+files[key]) {
				t.Fatalf("%v: planned removal without vmem for %s", args, key)
			}
		}
	}
	if !equalMaps(snapshotFiles(t, files), before) {
		t.Fatal("retire removed obsidian from a config without vmem")
	}
	// A vmem entry pointing at another executable is not working either.
	writeCLITestFile(t, files["kiro"], `{"mcpServers":{"obsidian":`+obsidianEntry+`,"vmem":{"command":"/nonexistent/vmem","args":["mcp"]}}}`)
	if _, _, err := runDotForTest("--home", home, "--config", cfg, "ai", "vault", "retire-mcpvault", "--apply"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFile(t, files["kiro"]), `"obsidian"`) {
		t.Fatal("obsidian removed although the vmem entry is not this vmem")
	}
}

func equalMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}
