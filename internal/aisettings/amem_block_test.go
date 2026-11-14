package aisettings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	// A server `amem setup qwen` registered counts too, although dot no longer writes that file.
	m4, home4 := newAgentsHome(t)
	mustWriteJSON(t, filepath.Join(home4, ".qwen", "settings.json"), map[string]any{"mcpServers": map[string]any{AmemServerName: map[string]any{}}})
	if !m4.MemoryWanted() {
		t.Fatal("not wanted although `amem setup` registered a server")
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
			mustWriteFile(t, m.KiroMCPPath(), body)
			if _, err := m.Install(); err != nil {
				t.Fatalf("install panicked or failed: %v", err)
			}
			if readServers(t, m.KiroMCPPath())[AmemServerName] == nil {
				t.Fatal("no amem entry")
			}
		})
	}
}

func TestAmemInstallDoesNotHTMLEscapeAnArgumentOrAnotherServer(t *testing.T) {
	m, _ := newAmem(t)
	mustWriteFile(t, m.KiroMCPPath(), `{"mcpServers":{"other":{"command":"sh","args":["-c","a && b < c > d"]}}}`)
	if _, err := m.Install(); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(m.KiroMCPPath())
	for _, escaped := range []string{`\u0026`, `\u003c`, `\u003e`} {
		if strings.Contains(string(raw), escaped) {
			t.Fatalf("the merge rewrote another server's arguments with HTML escapes:\n%s", raw)
		}
	}
	if !strings.Contains(string(raw), "a && b < c > d") {
		t.Fatalf("another server's argument was altered:\n%s", raw)
	}
}
