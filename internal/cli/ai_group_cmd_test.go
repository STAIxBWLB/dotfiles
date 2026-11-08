package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAIGroupBackupStatusCompatibility(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	args := []string{"--home", home, "ai", "backup", "status", "--from", root, "--host", "fixture-host"}
	before := treeDigest(t, home)
	out, stderr, err := runDotForTest(args...)
	if err != nil || strings.Contains(stderr, "deprecated") {
		t.Fatalf("canonical status: %v\n%s", err, stderr)
	}
	legacy, warning, legacyErr := runDotForTest("--home", home, "ai", "status", "--from", root, "--host", "fixture-host")
	if legacyErr != nil || legacy != out || !strings.Contains(warning, "dot ai backup status") || strings.Contains(legacy, "deprecated") {
		t.Fatalf("legacy status: %v\nstdout=%s\nstderr=%s", legacyErr, legacy, warning)
	}
	if treeDigest(t, home) != before {
		t.Fatal("backup status wrote to the home")
	}
}

func TestAIGroupListCompatibility(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	for _, auth := range []bool{false, true} {
		canonical := []string{"--home", home, "ai", "tools", "list", "--details"}
		legacy := []string{"--home", home, "ai", "list"}
		if auth {
			canonical = append(canonical, "--include-auth")
			legacy = append(legacy, "--include-auth")
		}
		out, _, err := runDotForTest(canonical...)
		old, warning, oldErr := runDotForTest(legacy...)
		if err != nil || oldErr != nil || out != old || !strings.Contains(warning, "dot ai tools list --details") || strings.Contains(old, "deprecated") {
			t.Fatalf("list auth=%t: %v / %v\n%s", auth, err, oldErr, warning)
		}
	}
	_, _, err := runDotForTest("ai", "tools", "list", "--details", "--json")
	if err == nil || !strings.Contains(err.Error(), "omit") {
		t.Fatalf("incompatible modes need an actionable error: %v", err)
	}
}

func TestAIGroupFamilyRollbackCompatibility(t *testing.T) {
	home := t.TempDir()
	_, _, canonical := runDotForTest("--home", home, "--dry-run", "ai", "family", "rollback", "amem")
	_, warning, legacy := runDotForTest("--home", home, "--dry-run", "ai", "rollback", "amem")
	if canonical == nil || legacy == nil || canonical.Error() != legacy.Error() || !strings.Contains(warning, "dot ai family rollback") {
		t.Fatalf("rollback canonical=%v legacy=%v warning=%s", canonical, legacy, warning)
	}
}

func TestAIGroupMCPCompatibilityAndOwnership(t *testing.T) {
	for _, spec := range []struct {
		id, legacy string
		fake       func(*testing.T) string
	}{{"amem", "memory", fakeAmem}, {"vmem", "vault", fakeVmem}, {"ares", "research", func(t *testing.T) string { return fakeAres(t, "0.1.0") }}} {
		t.Run(spec.id, func(t *testing.T) {
			binary := spec.fake(t)
			home, files := researchFixture(t)
			cfg := writeAgentsConfig(t, home, "kiro", "copilot")
			before := treeDigest(t, home)
			prefix := []string{"--home", home, "--config", cfg}
			out, _, err := runDotForTest(append(append([]string{}, prefix...), "ai", "mcp", "status", spec.id)...)
			legacy, warning, oldErr := runDotForTest(append(append([]string{}, prefix...), "ai", spec.legacy, "status")...)
			if err != nil || oldErr != nil || out != legacy || !strings.Contains(warning, "dot ai mcp status "+spec.id) || strings.Contains(legacy, "deprecated") {
				t.Fatalf("status parity: %v / %v\n%s", err, oldErr, warning)
			}
			for _, command := range [][]string{{"ai", "mcp", "install", spec.id}, {"ai", spec.legacy, "install"}} {
				args := append(append([]string{}, prefix...), "--dry-run")
				plan, _, planErr := runDotForTest(append(args, command...)...)
				if planErr != nil || !strings.Contains(plan, "would write") {
					t.Fatalf("dry run %v: %v\n%s", command, planErr, plan)
				}
			}
			if treeDigest(t, home) != before {
				t.Fatal("status or dry run wrote to the fixture")
			}
			protected := map[string]string{"claude": readFile(t, files["claude"]), "codex": readFile(t, files["codex"])}
			if _, stderr, err := runDotForTest(append(append([]string{}, prefix...), "ai", "mcp", "install", spec.id)...); err != nil {
				t.Fatalf("install: %v\n%s", err, stderr)
			}
			var kiro struct {
				Servers map[string]struct {
					Command string `json:"command"`
				} `json:"mcpServers"`
			}
			if err := json.Unmarshal([]byte(readFile(t, files["kiro"])), &kiro); err != nil {
				t.Fatal(err)
			}
			if kiro.Servers[spec.id].Command != binary || kiro.Servers["other"].Command != "x" {
				t.Fatalf("registration changed the provider or a foreign entry: %+v", kiro.Servers)
			}
			for id, content := range protected {
				if readFile(t, files[id]) != content {
					t.Fatalf("install changed unowned %s settings", id)
				}
			}
		})
	}
}

func TestAIGroupMCPRejectsInvalidRequestsWithoutWrites(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	before := treeDigest(t, home)
	for _, args := range [][]string{{"install"}, {"status", "unknown"}, {"install", "amem", "vmem"}} {
		command := append([]string{"--home", home, "ai", "mcp"}, args...)
		if _, _, err := runDotForTest(command...); err == nil || !strings.Contains(err.Error(), "amem") || !strings.Contains(err.Error(), "vmem") || !strings.Contains(err.Error(), "ares") {
			t.Fatalf("invalid request %v: %v", args, err)
		}
	}
	if treeDigest(t, home) != before {
		t.Fatal("invalid MCP request wrote files")
	}
	if _, err := os.Stat(filepath.Join(home, ".kiro")); !os.IsNotExist(err) {
		t.Fatalf("invalid request initialized an integration: %v", err)
	}
}

func TestAIGroupMCPRejectsUnknownAction(t *testing.T) {
	home := t.TempDir()
	if _, _, err := runDotForTest("--home", home, "ai", "mcp", "unknown", "amem"); err == nil || !strings.Contains(err.Error(), "dot ai mcp install") {
		t.Fatalf("unknown action needs a replacement command: %v", err)
	}
}

func TestAIGroupMCPRequiresSelectionForExplicitHome(t *testing.T) {
	fakeAmem(t)
	fakeVmem(t)
	fakeAres(t, "0.1.0")
	for _, id := range []string{"amem", "vmem", "ares"} {
		home := t.TempDir()
		before := treeDigest(t, home)
		if _, _, err := runDotForTest("--home", home, "ai", "mcp", "install", id); err == nil || !strings.Contains(err.Error(), "selection") {
			t.Fatalf("%s must refuse an explicit home without a selection: %v", id, err)
		}
		if treeDigest(t, home) != before {
			t.Fatalf("%s wrote before validating selection", id)
		}
	}
}
