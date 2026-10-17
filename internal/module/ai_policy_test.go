package module

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/config"
)

func policyContext(t *testing.T) *RunContext {
	return &RunContext{HomeDir: t.TempDir(), ExplicitHome: true, Config: &config.Config{Modules: config.ModulesConfig{AI: config.AIConfig{Policy: &config.AIPolicyConfig{Enabled: true, Revision: "1", Targets: []config.AIPolicyTarget{{ID: "primary", Agent: "claude", Version: "2.1.283", Model: "sonnet", Billing: "subscription", Validated: true, Workloads: []string{"routine"}}}}}}}}
}
func TestModulePolicyDryRunNeverProbesOrWrites(t *testing.T) {
	rc := policyContext(t)
	rc.DryRun = true
	bin := t.TempDir()
	marker := filepath.Join(rc.HomeDir, "probe")
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	items, err := modulePolicyChanges(context.Background(), rc, true)
	if err != nil || len(items) != 1 || items[0].Status != "unverified" {
		t.Fatalf("%#v %v", items, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("dry-run probed runtime")
	}
	entries, err := os.ReadDir(rc.HomeDir)
	if err != nil || len(entries) != 0 {
		t.Fatal("dry-run wrote files")
	}
}
func TestModulePolicyDisabledIsNoop(t *testing.T) {
	rc := policyContext(t)
	rc.Config.Modules.AI.Policy.Enabled = false
	items, err := modulePolicyChanges(context.Background(), rc, true)
	if err != nil || len(items) != 0 {
		t.Fatalf("%v %v", items, err)
	}
}
func TestModulePolicyUnvalidatedIsDeferred(t *testing.T) {
	rc := policyContext(t)
	t.Setenv("PATH", t.TempDir())
	rc.Config.Modules.AI.Policy.Targets[0].Validated = false
	changes, err := checkModulePolicy(context.Background(), rc)
	if err != nil || len(changes) == 0 {
		t.Fatalf("%v %v", changes, err)
	}
	messages, err := applyModulePolicy(context.Background(), rc)
	if err == nil || !strings.Contains(strings.Join(messages, " "), "unsupported") {
		t.Fatalf("%v %v", messages, err)
	}
}
func TestModulePolicyValidatesBeforeDryRun(t *testing.T) {
	rc := policyContext(t)
	rc.DryRun = true
	rc.Config.Modules.AI.Policy.Targets[0].ID = "../bad"
	if _, err := modulePolicyChanges(context.Background(), rc, true); err == nil {
		t.Fatal("invalid dry run accepted")
	}
}

func TestModulePolicyOverlayConverges(t *testing.T) {
	rc := policyContext(t)
	resolved, err := filepath.EvalSymlinks(rc.HomeDir)
	if err != nil {
		t.Fatal(err)
	}
	rc.HomeDir = resolved
	bin := t.TempDir()
	script := "#!/bin/sh\ncase \"$1\" in\n--version) echo '2.1.283';;\n--help) echo '--permission-mode auto --effort --model';;\nauth) echo '{\"loggedIn\":true,\"authMethod\":\"claude.ai\",\"apiProvider\":\"firstParty\",\"subscriptionType\":\"max\"}';;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	for _, key := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY"} {
		t.Setenv(key, "")
	}
	before, err := checkModulePolicy(context.Background(), rc)
	if err != nil || len(before) == 0 {
		t.Fatalf("before: %v %v", before, err)
	}
	if _, err := applyModulePolicy(context.Background(), rc); err != nil {
		t.Fatal(err)
	}
	after, err := checkModulePolicy(context.Background(), rc)
	if err != nil || len(after) != 0 {
		t.Fatalf("after: %v %v", after, err)
	}
	if _, err := os.Stat(filepath.Join(rc.HomeDir, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Fatal("native base preferences modified")
	}
}
