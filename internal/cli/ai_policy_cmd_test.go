package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/aipolicy"
	"github.com/STAIxBWLB/dotfiles/internal/config"
)

func goldenAIPolicyFixture(t *testing.T) (home, root string) {
	t.Helper()
	home, root = goldenAIFixture(t)
	// Use a canonical fixture boundary so native profile paths and ownership
	// hashes do not depend on macOS's /var alias spelling.
	var err error
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	for _, key := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "OPENAI_API_KEY", "OPENAI_BASE_URL", "CODEX_API_KEY"} {
		t.Setenv(key, "")
	}
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\ncase \"$1\" in\n--version) echo 'codex-cli 0.157.1';;\n--help) echo '--approve-for-me --model --profile';;\n*) exit 99;;\nesac\n"), 0700); err != nil {
		t.Fatal(err)
	}
	writeCLITestFile(t, filepath.Join(home, ".codex", "auth.json"), `{"auth_mode":"chatgpt"}`)
	t.Setenv("PATH", bin)
	state, err := config.LoadStateForHome(home)
	if err != nil {
		t.Fatal(err)
	}
	state.Modules.AI.Policy = &config.AIPolicyConfig{Enabled: true, Revision: "fixture-1", MaxSwitches: 2, Targets: []config.AIPolicyTarget{{ID: "codex-balanced", Agent: "codex", Model: "gpt-policy-test", Billing: "subscription", Version: "0.157.1", Validated: true, Workloads: []string{"routine", "implementation", "deep-analysis", "independent-review", "documents-teaching", "visual-production"}, Capabilities: []string{"text", "code", "shell", "auto-review"}}}}
	if err = config.SaveStateForHome(home, state); err != nil {
		t.Fatal(err)
	}
	return home, root
}

func TestPolicyResolveUsesValidatedFixtureAndExplicitConfigAuthority(t *testing.T) {
	home, root := goldenAIPolicyFixture(t)
	out, _, err := runDotForTest("--home", home, "ai", "policy", "resolve", "--project", root, "--task", "코드를 검토하라", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var resolution aipolicy.Resolution
	if err = json.Unmarshal([]byte(out), &resolution); err != nil {
		t.Fatal(err)
	}
	if !resolution.Eligible || resolution.Agent != "codex" || resolution.Workload != "independent-review" || resolution.Effort != "high" {
		t.Fatalf("unexpected resolution: %+v", resolution)
	}
	path := filepath.Join(home, "no-policy.yaml")
	if err = os.WriteFile(path, []byte("modules:\n  ai:\n    enabled: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, _, err = runDotForTest("--home", home, "--config", path, "ai", "policy", "resolve", "--project", root, "--task", "status", "--json")
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("saved policy bypassed explicit config: %v", err)
	}
}

func TestPolicyDryRunDoesNotProbeOrWrite(t *testing.T) {
	home, _ := goldenAIPolicyFixture(t)
	marker := filepath.Join(home, "probed")
	if err := os.WriteFile(filepath.Join(home, "bin", "codex"), []byte("#!/bin/sh\n/bin/touch '"+marker+"'\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(home, ".config", "dotfiles", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = runDotForTest("--home", home, "ai", "policy", "apply", "--persist", "--dry-run", "--json")
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(home, ".config", "dotfiles", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("dry run changed desired state")
	}
	for _, path := range []string{marker, filepath.Join(home, ".local", "share", "dotfiles", "ai", "policy")} {
		if _, err = os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("dry run created %s", path)
		}
	}
}

func TestPolicyApplyAndRollbackPreserveNativeConfig(t *testing.T) {
	home, _ := goldenAIPolicyFixture(t)
	path := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	const original = "approval_policy = \"never\"\n[mcp_servers.example]\ncommand = \"native-owned\"\n"
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"apply", "apply", "rollback"} {
		if _, _, err := runDotForTest("--home", home, "ai", "policy", action, "--json"); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != original {
		t.Fatalf("native base changed: %q / %v", got, err)
	}
}

func TestPolicyStateApplicationKeepsExplicitAbsence(t *testing.T) {
	cfg := &config.Config{}
	state := &config.UserState{}
	state.Modules.AI.Policy = &config.AIPolicyConfig{Enabled: true, Revision: "saved"}
	applyStateWithToolingAuthority(cfg, state, true)
	if cfg.Modules.AI.Policy != nil {
		t.Fatal("explicit absence inherited saved policy")
	}
}

func TestExplicitPolicyOnlyConfigEnablesAIModule(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		cfg := &config.Config{}
		cfg.Modules.AI.Policy = &config.AIPolicyConfig{Enabled: enabled, Revision: "explicit"}
		state := &config.UserState{}
		state.Modules.AI.Policy = &config.AIPolicyConfig{Enabled: true, Revision: "saved"}
		applyStateWithToolingAuthority(cfg, state, true)
		if cfg.Modules.AI.Enabled != enabled || cfg.Modules.AI.Policy.Revision != "explicit" {
			t.Fatalf("policy-only authority lost: %+v", cfg.Modules.AI)
		}
	}
}
