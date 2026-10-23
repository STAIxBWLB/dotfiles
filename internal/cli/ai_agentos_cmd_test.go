package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAISkillsListJSONAndValidateStrict(t *testing.T) {
	home := t.TempDir()
	writeCLITestFile(t, filepath.Join(home, ".codex", "skills", "valid", "SKILL.md"), `---
name: valid-skill
description: Valid
schema_version: v1
---
# Valid
`)
	writeCLITestFile(t, filepath.Join(home, ".codex", "skills", "legacy", "SKILL.md"), `---
name: legacy-skill
description: Missing schema
---
# Legacy
`)

	out, errOut, err := runDotForTest("--home", home, "ai", "skills", "list", "--tool", "codex", "--json")
	if err != nil {
		t.Fatalf("skills list: %v\nstderr=%s", err, errOut)
	}
	if !strings.Contains(out, `"status": "valid"`) || !strings.Contains(out, `"status": "legacy"`) {
		t.Fatalf("skills list json missing statuses:\n%s", out)
	}

	_, _, err = runDotForTest("--home", home, "ai", "skills", "validate", "--tool", "codex")
	if err != nil {
		t.Fatalf("non-strict validate should pass legacy skills: %v", err)
	}
	_, _, err = runDotForTest("--home", home, "ai", "skills", "validate", "--tool", "codex", "--strict")
	if err == nil {
		t.Fatal("strict validate should fail legacy skills")
	}
}

func TestAISkillsListGeminiScansGeminiRoot(t *testing.T) {
	home := t.TempDir()
	writeCLITestFile(t, filepath.Join(home, ".gemini", "skills", "valid", "SKILL.md"), `---
name: gemini-skill
description: Valid
schema_version: v1
---
# Valid
`)

	out, errOut, err := runDotForTest("--home", home, "ai", "skills", "list", "--tool", "gemini", "--json")
	if err != nil {
		t.Fatalf("skills list: %v\nstderr=%s", err, errOut)
	}
	if !strings.Contains(out, `"tool": "gemini"`) || !strings.Contains(out, `"name": "gemini-skill"`) {
		t.Fatalf("gemini scan did not use ~/.gemini/skills:\n%s", out)
	}
}

func TestAISkillsApplySubcommandRemoved(t *testing.T) {
	home := t.TempDir()

	out, errOut, err := runDotForTest("--home", home, "ai", "skills", "--help")
	if err != nil {
		t.Fatalf("skills help: %v\nstderr=%s", err, errOut)
	}
	if strings.Contains(out, "apply") {
		t.Fatalf("skills help still advertises apply (Maru owns symlinks):\n%s", out)
	}
	if !strings.Contains(out, "read-only") {
		t.Fatalf("skills help missing read-only wording:\n%s", out)
	}
}

func TestAISkillsStatusUsesLegacyEnabledConfigWithoutActingOnIt(t *testing.T) {
	home := t.TempDir()
	ssot := filepath.Join(home, "maru-skills")
	writeCLITestFile(t, filepath.Join(ssot, "vault-extract", "SKILL.md"), "# Vault Extract\n")
	// Legacy config: modules.ai.skills.enabled is deprecated but must still load.
	writeCLITestFile(t, filepath.Join(home, ".config", "dotfiles", "config.yaml"),
		"name: Test\nprofile: full\nmodules:\n  ai:\n    enabled: true\n    skills:\n      enabled: true\n      provider: maru\n      ssot_path: "+ssot+"\n      tools: [claude]\n")

	status, errOut, err := runDotForTest("--home", home, "ai", "skills", "status", "--json")
	if err != nil {
		t.Fatalf("skills status from legacy config: %v\nstderr=%s", err, errOut)
	}
	if !strings.Contains(status, `"status": "missing"`) {
		t.Fatalf("status did not diagnose configured skills:\n%s", status)
	}
	// Diagnose-only: the missing target must not have been created.
	if _, err := os.Lstat(filepath.Join(home, ".claude", "skills", "vault-extract")); !os.IsNotExist(err) {
		t.Fatalf("status created a symlink, stat err=%v", err)
	}
}

func TestAIAgentsApplyAntigravityAndGeminiAlias(t *testing.T) {
	home := t.TempDir()
	ssot := filepath.Join(home, ".config", "dotfiles", "agents", "AGENTS.md")
	target := filepath.Join(home, ".gemini", "GEMINI.md")
	writeCLITestFile(t, ssot, "shared\n")

	if _, errOut, err := runDotForTest("--home", home, "ai", "agents", "apply", "--tool", "antigravity"); err != nil {
		t.Fatalf("agents apply antigravity: %v\nstderr=%s", err, errOut)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "shared\n") {
		t.Fatalf("antigravity target missing shared instructions: %q", got)
	}

	if _, errOut, err := runDotForTest("--home", home, "ai", "agents", "apply", "--tool", "gemini"); err != nil {
		t.Fatalf("agents apply gemini alias: %v\nstderr=%s", err, errOut)
	}
}

func TestAIAuditTailAndSummary(t *testing.T) {
	home := t.TempDir()
	if _, _, err := runDotForTest("--home", home, "ai", "audit", "summary"); err != nil {
		t.Fatalf("empty audit summary: %v", err)
	}
	if _, _, err := runDotForTest("--home", home, "ai", "audit", "tail", "1"); err != nil {
		t.Fatalf("empty audit tail: %v", err)
	}
}

func TestAIMemoryStatusAndDryRunAreReadOnly(t *testing.T) {
	home := t.TempDir()
	ssot := filepath.Join(home, ".config", "dotfiles", "agents", "AGENTS.md")
	writeCLITestFile(t, ssot, "# AI Agents\n")
	// Hermetic: a stand-in amem on a PATH of its own, never whatever amem the machine running the test has.
	amemDir := t.TempDir()
	writeCLITestFile(t, filepath.Join(amemDir, "amem"), "#!/bin/sh\necho 0.6.0\n")
	if err := os.Chmod(filepath.Join(amemDir, "amem"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", amemDir)
	before := treeOf(t, home)

	status, errOut, err := runDotForTest("--home", home, "ai", "memory", "status")
	if err != nil {
		t.Fatalf("memory status: %v\nstderr=%s", err, errOut)
	}
	for _, want := range []string{"amem Status", "kimi", "qwen", "agy", "kiro", "copilot", "pi", "instructions", "0.6.0"} {
		if !strings.Contains(status, want) {
			t.Fatalf("memory status missing %q:\n%s", want, status)
		}
	}

	dryRun, errOut, err := runDotForTest("--home", home, "--dry-run", "ai", "memory", "install")
	if err != nil {
		t.Fatalf("memory install --dry-run: %v\nstderr=%s", err, errOut)
	}
	if !strings.Contains(dryRun, "would write") && !strings.Contains(dryRun, "nothing to write") {
		t.Fatalf("memory dry-run missing plan:\n%s", dryRun)
	}
	if _, err := os.Stat(filepath.Join(home, ".kimi-code", "mcp.json")); !os.IsNotExist(err) {
		t.Fatalf("memory dry-run wrote Kimi config, stat err=%v", err)
	}
	after := treeOf(t, home)
	if len(before) != len(after) {
		t.Fatalf("status and dry run changed the home: %d -> %d files", len(before), len(after))
	}
}

func TestAIAgentsApplyForceWritesAndAudits(t *testing.T) {
	home := t.TempDir()
	ssot := filepath.Join(home, ".config", "dotfiles", "agents", "AGENTS.md")
	target := filepath.Join(home, ".codex", "AGENTS.md")
	writeCLITestFile(t, ssot, "shared\n")
	writeCLITestFile(t, target, "hand edit\n")

	_, _, err := runDotForTest("--home", home, "ai", "agents", "apply", "--tool", "codex")
	if err == nil {
		t.Fatal("agents apply should fail on protected write conflict")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hand edit\n" {
		t.Fatalf("unforced apply overwrote target: %q", got)
	}

	out, errOut, err := runDotForTest("--home", home, "ai", "agents", "apply", "--tool", "codex", "--force")
	if err != nil {
		t.Fatalf("agents apply --force: %v\nstdout=%s\nstderr=%s", err, out, errOut)
	}
	got, err = os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "shared\n") {
		t.Fatalf("forced apply did not render SSOT: %q", got)
	}
	events, _, err := runDotForTest("--home", home, "ai", "audit", "tail", "1")
	if err != nil {
		t.Fatalf("audit tail: %v", err)
	}
	if !strings.Contains(events, `"type":"ai.agents.apply"`) {
		t.Fatalf("audit tail missing agents apply event:\n%s", events)
	}
}

func TestAISkillsPathDefaultsWithNoFlags(t *testing.T) {
	home := t.TempDir()
	writeCLITestFile(t, filepath.Join(home, ".claude", "skills", ".keep"), "")
	writeCLITestFile(t, filepath.Join(home, ".maru", "skills", "vault-extract", "SKILL.md"), "# Vault Extract\n")

	out, errOut, err := runDotForTest("--home", home, "ai", "skills", "path")
	if err != nil {
		t.Fatalf("skills path with no flags should succeed: %v\nstderr=%s", err, errOut)
	}
	if !strings.Contains(out, "maru") {
		t.Fatalf("path output missing provider maru:\n%s", out)
	}
	if !strings.Contains(out, filepath.Join(home, ".maru", "skills")) {
		t.Fatalf("path output missing maru SSOT root:\n%s", out)
	}
	if !strings.Contains(out, "Target Roots") || !strings.Contains(out, filepath.Join(home, ".claude", "skills")) {
		t.Fatalf("path output missing detected target root:\n%s", out)
	}
}

func TestAISkillsStatusDefaultsWithNoFlags(t *testing.T) {
	home := t.TempDir()
	writeCLITestFile(t, filepath.Join(home, ".claude", "skills", ".keep"), "")
	writeCLITestFile(t, filepath.Join(home, ".maru", "skills", "vault-extract", "SKILL.md"), "# Vault Extract\n")

	out, errOut, err := runDotForTest("--home", home, "ai", "skills", "status", "--json")
	if err != nil {
		t.Fatalf("skills status with no flags should succeed: %v\nstderr=%s", err, errOut)
	}
	if !strings.Contains(out, `"provider": "maru"`) {
		t.Fatalf("status json missing provider maru:\n%s", out)
	}
	if !strings.Contains(out, `"claude"`) {
		t.Fatalf("status json missing detected tool:\n%s", out)
	}
}

func TestAISkillsStatusAcceptsLegacyAnchorProvider(t *testing.T) {
	home := t.TempDir()
	writeCLITestFile(t, filepath.Join(home, ".claude", "skills", ".keep"), "")
	writeCLITestFile(t, filepath.Join(home, ".maru", "skills", "vault-extract", "SKILL.md"), "# Vault Extract\n")

	out, errOut, err := runDotForTest("--home", home, "ai", "skills", "status", "--provider", "anchor", "--json")
	if err != nil {
		t.Fatalf("skills status with legacy anchor provider should succeed: %v\nstderr=%s", err, errOut)
	}
	if !strings.Contains(out, `"provider": "maru"`) {
		t.Fatalf("legacy anchor provider should resolve to maru:\n%s", out)
	}
}

func TestAISkillsPathFallsBackToAllToolsWhenNoneDetected(t *testing.T) {
	home := t.TempDir() // no ~/.claude, ~/.codex, ~/.gemini etc.
	writeCLITestFile(t, filepath.Join(home, ".maru", "skills", "vault-extract", "SKILL.md"), "# Vault Extract\n")

	out, errOut, err := runDotForTest("--home", home, "ai", "skills", "path")
	if err != nil {
		t.Fatalf("skills path fallback should succeed: %v\nstderr=%s", err, errOut)
	}
	for _, id := range []string{"claude", "codex"} {
		if !strings.Contains(out, id) {
			t.Fatalf("fallback path output missing tool %q:\n%s", id, out)
		}
	}
}

func TestAISkillsStatusProviderPathRequiresSSOT(t *testing.T) {
	home := t.TempDir()

	_, errOut, err := runDotForTest("--home", home, "ai", "skills", "status", "--provider", "path", "--tool", "claude")
	if err == nil {
		t.Fatal("status with provider=path and no --ssot should error")
	}
	msg := err.Error() + errOut
	if !strings.Contains(msg, "--ssot") || !strings.Contains(msg, "maru") {
		t.Fatalf("provider=path error not actionable: %v\nstderr=%s", err, errOut)
	}
}

func TestAISkillsStatusUnknownToolListsValid(t *testing.T) {
	home := t.TempDir()
	writeCLITestFile(t, filepath.Join(home, ".maru", "skills", "demo", "SKILL.md"), "# Demo\n")

	_, errOut, err := runDotForTest("--home", home, "ai", "skills", "status", "--tool", "bogus")
	if err == nil {
		t.Fatal("status with an unknown tool should error")
	}
	msg := err.Error() + errOut
	if !strings.Contains(msg, "bogus") || !strings.Contains(msg, "valid:") {
		t.Fatalf("unknown-tool error missing valid list: %v\nstderr=%s", err, errOut)
	}
}

func runDotForTest(args ...string) (string, string, error) {
	root := NewRootCmd("dev", "test")
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), errb.String(), err
}

func writeCLITestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
