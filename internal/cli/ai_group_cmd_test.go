package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAIGroupBackupStatusReadOnly(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	args := []string{"--home", home, "ai", "backup", "status", "--from", root, "--host", "fixture-host"}
	before := treeDigest(t, home)
	out, stderr, err := runDotForTest(args...)
	if err != nil || out == "" || strings.Contains(stderr, "deprecated") {
		t.Fatalf("canonical status: %v\n%s", err, stderr)
	}
	if treeDigest(t, home) != before {
		t.Fatal("backup status wrote to the home")
	}
}

func TestAIGroupDetailedList(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	for _, auth := range []bool{false, true} {
		canonical := []string{"--home", home, "ai", "tools", "list", "--details"}
		if auth {
			canonical = append(canonical, "--include-auth")
		}
		out, warning, err := runDotForTest(canonical...)
		if err != nil || !strings.Contains(out, "Detected CLI tools") || strings.Contains(warning, "deprecated") {
			t.Fatalf("list auth=%t: %v\nstdout=%s\nstderr=%s", auth, err, out, warning)
		}
	}
	_, _, err := runDotForTest("ai", "tools", "list", "--details", "--json")
	if err == nil || !strings.Contains(err.Error(), "omit") {
		t.Fatalf("incompatible modes need an actionable error: %v", err)
	}
}

func TestAIGroupMCPOwnership(t *testing.T) {
	for _, spec := range []struct {
		id   string
		fake func(*testing.T) string
	}{{"amem", fakeAmem}, {"vmem", fakeVmem}, {"ares", func(t *testing.T) string { return fakeAres(t, "0.1.0") }}} {
		t.Run(spec.id, func(t *testing.T) {
			binary := spec.fake(t)
			home, files := researchFixture(t)
			cfg := writeAgentsConfig(t, home, "kiro", "copilot")
			before := treeDigest(t, home)
			prefix := []string{"--home", home, "--config", cfg}
			_, warning, err := runDotForTest(append(append([]string{}, prefix...), "ai", "mcp", "status", spec.id)...)
			if err != nil || strings.Contains(warning, "deprecated") {
				t.Fatalf("status: %v\n%s", err, warning)
			}
			args := append(append([]string{}, prefix...), "--dry-run", "ai", "mcp", "install", spec.id)
			plan, _, planErr := runDotForTest(args...)
			if planErr != nil || !strings.Contains(plan, "would write") {
				t.Fatalf("dry run: %v\n%s", planErr, plan)
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

func TestAIGroupRemovedAliasesRejectWithoutWrites(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	before := treeDigest(t, home)
	for _, route := range [][]string{
		{"list"}, {"status"}, {"rollback", "amem"},
		{"memory", "install"}, {"memory", "status"},
		{"vault", "install"}, {"vault", "status"},
		{"research"}, {"research", "install"}, {"research", "status"},
	} {
		args := append([]string{"--home", home, "ai"}, route...)
		if _, _, err := runDotForTest(args...); err == nil || !strings.Contains(err.Error(), "unknown command") || !strings.Contains(err.Error(), "--help") {
			t.Errorf("removed route %v must fail with guidance: %v", route, err)
		}
	}
	for _, route := range [][]string{{"ai"}, {"ai", "memory"}, {"ai", "vault"}} {
		args := append([]string{"--home", home}, route...)
		if out, _, err := runDotForTest(args...); err != nil || !strings.Contains(out, "Usage:") {
			t.Errorf("bare group %v must still show help: %v\n%s", route, err, out)
		}
	}
	if treeDigest(t, home) != before {
		t.Fatal("removed command wrote files")
	}
}
