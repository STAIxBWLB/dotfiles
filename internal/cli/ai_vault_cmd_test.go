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
	writeCLITestFile(t, path, "#!/bin/sh\necho 0.2.1\n")
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return path
}

// legacyEntry is a server syntax dot does not manage, seeded so the install tests
// prove it survives.
const legacyEntry = `{"command":"npx","args":["old-vault-mcp","/vault"]}`

func vaultFixture(t *testing.T) (home string, files map[string]string) {
	t.Helper()
	home = t.TempDir()
	files = map[string]string{
		"kiro":    filepath.Join(home, ".kiro", "settings", "mcp.json"),
		"copilot": filepath.Join(home, ".copilot", "mcp-config.json"),
		"claude":  filepath.Join(home, ".claude.json"),
		"codex":   filepath.Join(home, ".codex", "config.toml"),
	}
	writeCLITestFile(t, files["kiro"], `{"mcpServers":{"legacy":`+legacyEntry+`,"other":{"command":"x"}},"keep":true}`)
	writeCLITestFile(t, files["copilot"], `{"mcpServers":{"legacy":`+legacyEntry+`}}`)
	writeCLITestFile(t, files["claude"], `{"theme":"dark","mcpServers":{"legacy":`+legacyEntry+`}}`)
	writeCLITestFile(t, files["codex"], "model = \"gpt\"\n\n[mcp_servers.legacy]\ncommand = \"npx\"\n\n[mcp_servers.legacy.env]\nX = \"1\"\n")
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
	out, errOut, err := runDotForTest("--home", home, "--dry-run", "ai", "mcp", "install", "vmem")
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
		if _, errOut, err := runDotForTest("--home", home, "--config", cfg, "ai", "mcp", "install", "vmem"); err != nil {
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
	if kiro.Servers["vmem"]["command"] != vmem || kiro.Servers["other"] == nil || !kiro.Keep || kiro.Servers["legacy"] == nil {
		t.Fatalf("kiro config: %+v", kiro)
	}
	if !strings.Contains(readFile(t, files["copilot"]), `"tools": [`) {
		t.Fatal("copilot entry shape")
	}
	ssot := readFile(t, ssotFor(t, home))
	if strings.Count(ssot, "<!-- dotfiles:vmem:start -->") != 1 || !strings.Contains(ssot, "keep me") {
		t.Fatalf("vault block: %s", ssot)
	}
	// Notes are written through vmem and found with vmem_search first; notes, root docs, ops/ and reports/ may be read from files.
	for _, want := range []string{"Vault notes are written through the `vmem` MCP tools", "Find notes with vmem_search first", "may then be read read-only from files", "are written with `vmem report`", "Never edit vault files directly or through another MCP server"} {
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
	out, _, err := runDotForTest("--home", home, "--config", cfg, "--dry-run", "ai", "mcp", "install", "vmem")
	if err != nil || !strings.Contains(out, "Nothing to change") {
		t.Fatalf("not idempotent: %v\n%s", err, out)
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
