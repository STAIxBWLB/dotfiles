package aipolicy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/config"
)

func fixture(t *testing.T) (*config.AIPolicyConfig, []Runtime) {
	return &config.AIPolicyConfig{Enabled: true, Revision: "r1", Targets: []config.AIPolicyTarget{{ID: "primary", Agent: "claude", Model: "sonnet", Version: "2.1.283", Billing: "subscription", Validated: true, Workloads: []string{"routine", "implementation", "deep-analysis", "independent-review", "documents-teaching", "visual-production"}, Capabilities: []string{"code", "text"}}}}, []Runtime{{Agent: "claude", Version: "2.1.283", Executable: "/usr/local/bin/claude", Home: t.TempDir(), HomeMode: "pinned", Available: true, AutoReview: true, SubscriptionVerified: true, Capabilities: []string{"code", "text"}}}
}
func TestWorkloadsAndEffort(t *testing.T) {
	for _, tc := range []struct{ task, w, e string }{{"check status", "routine", "medium"}, {"implement parser", "implementation", "medium"}, {"debug regression", "deep-analysis", "high"}, {"review PR", "independent-review", "high"}, {"write document", "documents-teaching", "medium"}, {"design image", "visual-production", "medium"}, {"unspecified", "routine", "medium"}} {
		p, inv := fixture(t)
		r, err := Resolve(p, Request{Task: tc.task}, inv)
		if err != nil {
			t.Fatal(err)
		}
		if r.Workload != tc.w || r.Effort != tc.e || !r.Eligible {
			t.Fatalf("%s: %#v", tc.task, r)
		}
	}
}
func TestRejectsUnsafeCandidates(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*config.AIPolicyConfig, []Runtime)
		req    Request
	}{
		{"unvalidated", func(p *config.AIPolicyConfig, _ []Runtime) { p.Targets[0].Validated = false }, Request{}},
		{"unknown billing", func(p *config.AIPolicyConfig, _ []Runtime) { p.Targets[0].Billing = "" }, Request{}},
		{"changed version", func(_ *config.AIPolicyConfig, i []Runtime) { i[0].Version = "2.1.284" }, Request{}},
		{"no reviewer", func(_ *config.AIPolicyConfig, i []Runtime) { i[0].AutoReview = false }, Request{}},
		{"missing capability", func(_ *config.AIPolicyConfig, _ []Runtime) {}, Request{RequiredCapabilities: []string{"image"}}},
		{"explicit agent", func(_ *config.AIPolicyConfig, _ []Runtime) {}, Request{Agent: "codex"}},
		{"explicit workload", func(_ *config.AIPolicyConfig, _ []Runtime) {}, Request{Workload: "unknown"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, i := fixture(t)
			tc.change(p, i)
			r, err := Resolve(p, tc.req, i)
			if err == nil || r.Eligible {
				t.Fatal("accepted unsafe candidate")
			}
		})
	}
}
func TestPriorityAndExplicitWorkload(t *testing.T) {
	p, i := fixture(t)
	other := p.Targets[0]
	other.ID = "higher"
	other.Priority = 3
	p.Targets = append(p.Targets, other)
	r, err := Resolve(p, Request{Task: "review", Workload: "routine"}, i)
	if err != nil || r.TargetID != "higher" || r.Workload != "routine" {
		t.Fatalf("%#v %v", r, err)
	}
}
func TestDefaultNeverAutoValidates(t *testing.T) {
	_, i := fixture(t)
	p := DefaultPolicy(i)
	if p.Enabled || p.Targets[0].Validated || p.Targets[0].Billing != "" {
		t.Fatal("inspection silently validated account")
	}
}
func TestInspectExplicitHome(t *testing.T) {
	bin := t.TempDir()
	home := t.TempDir()
	ambient := t.TempDir()
	t.Setenv("PATH", bin)
	t.Setenv("CODEX_HOME", ambient)
	script := "#!/bin/sh\ncase \"$1\" in\n--version) echo 'codex-cli 0.157.1';;\n--help) echo '--approve-for-me --model';;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	for _, explicit := range []bool{true, false} {
		inspectHome := home
		if !explicit {
			inspectHome, _ = os.UserHomeDir()
		}
		inv, err := Inspect(context.Background(), inspectHome, explicit)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range inv {
			if r.Agent != "codex" {
				continue
			}
			want := ambient
			if explicit {
				want = filepath.Join(home, ".codex")
			}
			if r.Home != want || !r.AutoReview || !r.Available {
				t.Fatalf("%#v", r)
			}
		}
	}
}
func TestNoApprovalBypassFallback(t *testing.T) {
	for _, agent := range []string{"grok", "kimi", "opencode", "qwen", "kiro", "pi"} {
		args, _, err := permissionArgs(Runtime{Agent: agent, Version: "1.0.0", AutoReview: true}, true)
		if err == nil || strings.Contains(strings.Join(args, " "), "yolo") {
			t.Fatalf("unsafe fallback %s", agent)
		}
	}
}

func TestSubscriptionOverridesAreRejected(t *testing.T) {
	p, i := fixture(t)
	i[0].SubscriptionConflict = true
	r, err := Resolve(p, Request{}, i)
	if err == nil || r.Eligible || r.Reason == "" {
		t.Fatal("billing override accepted")
	}
	p.Targets[0].Billing = "dgx"
	if _, err := Resolve(p, Request{}, i); err == nil {
		t.Fatal("unbound DGX target accepted")
	}
}
func TestPreferenceInspectionDoesNotExposeSecrets(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ANTHROPIC_API_KEY", "test-secret-must-not-leak")
	if err := os.WriteFile(filepath.Join(home, "settings.json"), []byte(`{"permissions":{"defaultMode":"bypassPermissions"},"env":{"ANTHROPIC_AUTH_TOKEN":{"unexpected":"shape"}},"effortLevel":"high"}`), 0600); err != nil {
		t.Fatal(err)
	}
	r := Runtime{Agent: "claude", Home: home}
	inspectPreferences(&r)
	if !r.SubscriptionConflict || r.Preferences["permission_mode"] != "bypassPermissions" {
		t.Fatalf("%#v", r)
	}
	for _, v := range r.Preferences {
		if strings.Contains(v, "secret") {
			t.Fatal("secret exposed")
		}
	}
}
func TestRelativeRuntimeHomeRejected(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("CODEX_HOME", "relative")
	actual, _ := os.UserHomeDir()
	_, err := Inspect(context.Background(), actual, false)
	if err == nil {
		t.Fatal("relative home accepted")
	}
}

func TestProjectProviderOverrideRejected(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "nested")
	if err := os.MkdirAll(filepath.Join(root, ".claude"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".claude", "settings.local.json"), []byte(`{"env":{"ANTHROPIC_BASE_URL":"https://example.invalid"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	p, i := fixture(t)
	r, err := Resolve(p, Request{CWD: cwd}, i)
	if err == nil || r.Eligible {
		t.Fatal("ancestor provider override accepted")
	}
}

func TestUnverifiedSubscriptionRejected(t *testing.T) {
	p, i := fixture(t)
	i[0].SubscriptionVerified = false
	r, err := Resolve(p, Request{}, i)
	if err == nil || r.BillingVerified {
		t.Fatal("unverified subscription selected")
	}
}
func TestCodexAuthModeOnly(t *testing.T) {
	home := t.TempDir()
	for _, tc := range []struct {
		mode string
		want bool
	}{{"chatgpt", true}, {"apikey", false}, {"", false}} {
		if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"auth_mode":"`+tc.mode+`","tokens":{"secret":"never-output"}}`), 0600); err != nil {
			t.Fatal(err)
		}
		if got := codexSubscription(home); got != tc.want {
			t.Fatalf("mode %s got %v", tc.mode, got)
		}
	}
}
func TestExplicitProbeEnvironmentIsolation(t *testing.T) {
	home := t.TempDir()
	for _, key := range []string{"CODEX_HOME", "CLAUDE_CONFIG_DIR", "KIMI_CODE_HOME", "OPENCODE_CONFIG_DIR", "OPENCODE_CONFIG", "XDG_CONFIG_HOME"} {
		t.Setenv(key, "/ambient-secret-profile")
	}
	env := probeEnvironment(home, true, Runtime{Agent: "kimi", Home: filepath.Join(home, ".kimi-code")})
	for _, item := range env {
		if strings.Contains(item, "/ambient-secret-profile") {
			t.Fatalf("ambient root retained: %s", strings.SplitN(item, "=", 2)[0])
		}
	}
}

func TestNativeProbePreservesAbsentConfigHome(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if err := os.Unsetenv("CLAUDE_CONFIG_DIR"); err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	env := probeEnvironment(home, false, Runtime{Agent: "claude", Home: filepath.Join(home, ".claude"), HomeMode: "native-default"})
	for _, item := range env {
		if strings.HasPrefix(item, "CLAUDE_CONFIG_DIR=") {
			t.Fatal("native default keychain namespace changed")
		}
	}
}

func TestCodexEndpointOverrideConflicts(t *testing.T) {
	for _, key := range []string{"openai_base_url", `"openai_base_url"`} {
		home := t.TempDir()
		if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(key+` = "https://endpoint.invalid"`), 0600); err != nil {
			t.Fatal(err)
		}
		r := Runtime{Agent: "codex", Home: home}
		inspectPreferences(&r)
		if !r.SubscriptionConflict {
			t.Fatal("global endpoint override accepted")
		}
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".codex"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".codex", "config.toml"), []byte(`openai_base_url = "https://endpoint.invalid"`), 0600); err != nil {
		t.Fatal(err)
	}
	conflict, err := projectSubscriptionConflict("codex", filepath.Join(root, "nested"))
	if err != nil || !conflict {
		t.Fatalf("project endpoint: %v %v", conflict, err)
	}
}
func TestKnowledgeAskDenyPrecedence(t *testing.T) {
	for _, rule := range []string{"mcp__vmem__vmem_create", "mcp__vmem__*", "mcp__*", "mcp__vmem"} {
		home := t.TempDir()
		if err := os.WriteFile(filepath.Join(home, "settings.json"), []byte(`{"permissions":{"ask":["`+rule+`"]}}`), 0600); err != nil {
			t.Fatal(err)
		}
		conflicts, err := KnowledgeConflicts(Runtime{Agent: "claude", Home: home}, "", []KnowledgeApproval{{Server: "vmem", Tool: "vmem_create", ApprovalMode: "approve"}})
		if err != nil || len(conflicts) == 0 {
			t.Fatalf("rule %s: %v %v", rule, conflicts, err)
		}
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".claude"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".claude", "settings.local.json"), []byte(`{"permissions":{"deny":["mcp__vmem__vmem_create"]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	conflicts, err := KnowledgeConflicts(Runtime{Agent: "claude", Home: t.TempDir()}, filepath.Join(root, "nested"), []KnowledgeApproval{{Server: "vmem", Tool: "vmem_create", ApprovalMode: "approve"}})
	if err != nil || len(conflicts) == 0 {
		t.Fatalf("ancestor deny: %v %v", conflicts, err)
	}
}
func TestKnowledgeDeleteOnlyRulePreserved(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "settings.json"), []byte(`{"permissions":{"ask":["mcp__vmem__vmem_delete"]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	conflicts, err := KnowledgeConflicts(Runtime{Agent: "claude", Home: home}, "", []KnowledgeApproval{{Server: "vmem", Tool: "vmem_create", ApprovalMode: "approve"}})
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("unrelated delete ask: %v %v", conflicts, err)
	}
}

func TestKnowledgeConflictDoesNotFallbackToOtherTarget(t *testing.T) {
	p, i := fixture(t)
	if err := os.WriteFile(filepath.Join(i[0].Home, "settings.json"), []byte(`{"permissions":{"deny":["mcp__vmem__vmem_create"]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	other := p.Targets[0]
	other.ID = "secondary"
	other.Agent = "codex"
	other.Version = "0.157.1"
	p.Targets = append(p.Targets, other)
	i = append(i, Runtime{Agent: "codex", Version: "0.157.1", Home: t.TempDir(), HomeMode: "pinned", Available: true, AutoReview: true, SubscriptionVerified: true, Capabilities: []string{"text", "code"}})
	r, err := Resolve(p, Request{}, i)
	if err == nil || r.Eligible || r.Agent == "codex" {
		t.Fatal("native deny bypassed by fallback target")
	}
}
