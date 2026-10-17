package aipolicy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/config"
)

func preferenceFixture(t *testing.T) (Preferences, *config.AIPolicyConfig, []Runtime) {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return Preferences{Home: home, Explicit: true}, &config.AIPolicyConfig{Enabled: true, Revision: "1", Targets: []config.AIPolicyTarget{{ID: "primary", Agent: "claude", Model: "sonnet", Effort: "balanced", Billing: "subscription", Validated: true, Version: "2.1.283"}}}, []Runtime{{Agent: "claude", Available: true, AutoReview: true, Version: "2.1.283"}}
}
func TestPreferencesLifecycle(t *testing.T) {
	prefs, policy, inventory := preferenceFixture(t)
	changes, err := prefs.Apply(policy, inventory, true)
	if err != nil || len(changes) != 1 || changes[0].Status != "create" {
		t.Fatalf("%+v %v", changes, err)
	}
	if _, err = os.Stat(filepath.Join(prefs.Home, ".local")); !os.IsNotExist(err) {
		t.Fatal("dry run created state")
	}
	changes, err = prefs.Apply(policy, inventory, false)
	if err != nil {
		t.Fatal(err)
	}
	path := changes[0].Path
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "\"defaultMode\": \"auto\"") || !strings.Contains(string(data), "\"modelSettings\"") {
		t.Fatal(string(data))
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("overlay must be private")
	}
	changes, err = prefs.Apply(policy, inventory, false)
	if err != nil || changes[0].Status != "current" {
		t.Fatalf("%+v %v", changes, err)
	}
	policy.Targets[0].Model = "opus"
	changes, err = prefs.Apply(policy, inventory, false)
	if err != nil || changes[0].Status != "applied" {
		t.Fatalf("%+v %v", changes, err)
	}
	unrelated := filepath.Join(filepath.Dir(path), "foreign.json")
	if err = os.WriteFile(unrelated, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = prefs.Rollback(false); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("overlay survived rollback")
	}
	if data, err = os.ReadFile(unrelated); err != nil || string(data) != "keep" {
		t.Fatal("foreign file changed")
	}
}
func TestPreferencesRefuseForeignEdits(t *testing.T) {
	prefs, policy, inventory := preferenceFixture(t)
	changes, err := prefs.Apply(policy, inventory, false)
	if err != nil {
		t.Fatal(err)
	}
	path := changes[0].Path
	if err = os.WriteFile(path, []byte("foreign"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = prefs.Apply(policy, inventory, false); err == nil {
		t.Fatal("apply accepted foreign edit")
	}
	if _, err = prefs.Rollback(false); err == nil {
		t.Fatal("rollback accepted foreign edit")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "foreign" {
		t.Fatal("foreign edit changed")
	}
}
func TestPreferencesSymlinkAndMalformedReceipt(t *testing.T) {
	prefs, policy, inventory := preferenceFixture(t)
	other := t.TempDir()
	if err := os.Symlink(other, filepath.Join(prefs.Home, ".local")); err != nil {
		t.Fatal(err)
	}
	if _, err := prefs.Plan(policy, inventory); err == nil {
		t.Fatal("symlink accepted")
	}
	if err := os.Remove(filepath.Join(prefs.Home, ".local")); err != nil {
		t.Fatal(err)
	}
	root, _ := prefs.root()
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "receipt.json"), []byte("{bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := prefs.Apply(policy, inventory, false); err == nil {
		t.Fatal("malformed receipt accepted")
	}
}
func TestPreferencesCodexIsolated(t *testing.T) {
	prefs, policy, _ := preferenceFixture(t)
	target := &policy.Targets[0]
	target.Agent = "codex"
	target.Version = "0.157.1"
	target.Model = "gpt-5"
	inventory := []Runtime{{Agent: "codex", Available: true, AutoReview: true, Version: "0.157.1", Home: "/ignored/ambient-home"}}
	native := filepath.Join(prefs.Home, ".codex")
	if err := os.MkdirAll(native, 0700); err != nil {
		t.Fatal(err)
	}
	base := []byte("model = \"old\"\n[mcp_servers.private]\nurl = \"secret\"\n[profiles.legacy]\nmodel = \"old\"\n")
	if err := os.WriteFile(filepath.Join(native, "config.toml"), base, 0600); err != nil {
		t.Fatal(err)
	}
	changes, err := prefs.Apply(policy, inventory, false)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(changes[0].Path) != native || changes[0].LaunchArgs[0] != "--profile" || changes[0].BaseManaged || !strings.Contains(changes[0].Constraint, "legacy inline Codex profiles") {
		t.Fatalf("%+v", changes)
	}
	data, err := os.ReadFile(changes[0].Path)
	if err != nil || !strings.Contains(string(data), "approvals_reviewer = \"auto_review\"") {
		t.Fatalf("%s %v", data, err)
	}
	if _, err = prefs.Rollback(false); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(native, "config.toml"))
	if err != nil || string(data) != string(base) {
		t.Fatal("native base modified")
	}
}
func TestPreferencesUnvalidatedAndUnknownVersion(t *testing.T) {
	prefs, policy, inventory := preferenceFixture(t)
	policy.Targets[0].Validated = false
	changes, err := prefs.Apply(policy, inventory, false)
	if err != nil || changes[0].Status != "unsupported" {
		t.Fatalf("%+v %v", changes, err)
	}
	policy.Targets[0].Validated = true
	policy.Targets[0].Version = "99"
	inventory[0].Version = "99"
	changes, err = prefs.Apply(policy, inventory, false)
	if err != nil || changes[0].Status != "unsupported" {
		t.Fatalf("%+v %v", changes, err)
	}
	if _, err = os.Stat(filepath.Join(prefs.Home, ".local")); !os.IsNotExist(err) {
		t.Fatal("unsupported apply created state")
	}
}

func TestPreferencesRejectReceiptTraversalAndWrongDirectory(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		for _, malicious := range []string{"nested/../../outside/", "wrong-directory/"} {
			t.Run(agent+"/"+malicious, func(t *testing.T) {
				prefs, _, _ := preferenceFixture(t)
				prefs.Home = filepath.Join(prefs.Home, "home")
				if err := os.MkdirAll(prefs.Home, 0700); err != nil {
					t.Fatal(err)
				}
				root, _ := prefs.root()
				if err := os.MkdirAll(root, 0700); err != nil {
					t.Fatal(err)
				}
				relative := malicious + preferenceName("primary", agent)
				path := filepath.Join(prefs.Home, relative)
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				content := []byte("foreign content")
				if err := os.WriteFile(path, content, 0600); err != nil {
					t.Fatal(err)
				}
				receipt := preferenceReceipt{Entries: []preferenceEntry{{TargetID: "primary", Agent: agent, Hash: preferenceHash(content), RelativePath: relative}}}
				data, err := json.Marshal(receipt)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(filepath.Join(root, "receipt.json"), data, 0600); err != nil {
					t.Fatal(err)
				}
				if _, err = prefs.Rollback(false); err == nil {
					t.Fatal("unsafe receipt accepted")
				}
				preserved, err := os.ReadFile(path)
				if err != nil || string(preserved) != string(content) {
					t.Fatal("foreign file deleted")
				}
			})
		}
	}
}
func TestPreferencesSubscriptionOverrideBlocksWrite(t *testing.T) {
	prefs, policy, inventory := preferenceFixture(t)
	inventory[0].SubscriptionConflict = true
	changes, err := prefs.Apply(policy, inventory, false)
	if err != nil || len(changes) != 1 || changes[0].Status != "unsupported" {
		t.Fatalf("%+v %v", changes, err)
	}
	if _, err = os.Stat(filepath.Join(prefs.Home, ".local")); !os.IsNotExist(err) {
		t.Fatal("override wrote preference state")
	}
}

func TestPreferencesCanonicalDeclaredHome(t *testing.T) {
	prefs, policy, inventory := preferenceFixture(t)
	// A declared home may itself be reached through an OS/user alias. Descendants
	// remain protected by the existing symlink and receipt containment tests.
	container := t.TempDir()
	alias := filepath.Join(container, "home-alias")
	if err := os.Symlink(prefs.Home, alias); err != nil {
		t.Fatal(err)
	}
	prefs.Home = alias
	changes, err := prefs.Apply(policy, inventory, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Status != "applied" {
		t.Fatalf("%+v", changes)
	}
	changes, err = prefs.Status(policy, inventory)
	if err != nil || changes[0].Status != "current" {
		t.Fatalf("%+v %v", changes, err)
	}
	if _, err = prefs.Rollback(false); err != nil {
		t.Fatal(err)
	}
}

func TestPreferencesNativeProfileNamespacesCoexist(t *testing.T) {
	prefs, policy, _ := preferenceFixture(t)
	prefs.Explicit = false
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	policy.Targets[0].Agent = "codex"
	policy.Targets[0].Version = "0.157.1"
	paths := map[string]string{}
	for _, profile := range []string{"a", "b", "default"} {
		native := filepath.Join(prefs.Home, "profiles", profile)
		env := native
		if profile == "default" {
			native = filepath.Join(prefs.Home, ".codex")
			env = ""
		}
		t.Setenv("CODEX_HOME", env)
		inventory := []Runtime{{Agent: "codex", Home: native, Version: "0.157.1", Available: true, AutoReview: true, SubscriptionVerified: true}}
		changes, err := prefs.Apply(policy, inventory, false)
		if err != nil {
			t.Fatal(err)
		}
		paths[profile] = changes[0].Path
		if _, err = prefs.Status(policy, inventory); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CODEX_HOME", filepath.Join(prefs.Home, "profiles", "a"))
	if _, err := prefs.Rollback(false); err != nil {
		t.Fatal(err)
	}
	for profile, path := range paths {
		_, err := os.Stat(path)
		if profile == "a" && !os.IsNotExist(err) {
			t.Fatal("A not removed")
		}
		if profile != "a" && err != nil {
			t.Fatalf("other profile removed: %s", profile)
		}
	}
	t.Setenv("CODEX_HOME", "")
	// Explicit/default namespaces may share a native home without file collisions.
	prefs.Explicit = true
	inventory := []Runtime{{Agent: "codex", Home: filepath.Join(prefs.Home, ".codex"), Version: "0.157.1", Available: true, AutoReview: true, SubscriptionVerified: true}}
	changes, err := prefs.Apply(policy, inventory, false)
	if err != nil {
		t.Fatal(err)
	}
	if changes[0].Path == paths["default"] {
		t.Fatal("home mode namespaces collided")
	}
	if _, err = prefs.Rollback(false); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(paths["default"]); err != nil {
		t.Fatal("default namespace removed")
	}
}

func TestPreferencesNamespaceRelativeLayoutDeterministic(t *testing.T) {
	first, policy, inventory := preferenceFixture(t)
	second, _, _ := preferenceFixture(t)
	first.Explicit = false
	second.Explicit = false
	inventory[0].Home = filepath.Join(first.Home, ".claude")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", filepath.Join(first.Home, "profiles", "work"))
	firstChanges, err := first.Plan(policy, inventory)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", filepath.Join(second.Home, "profiles", "work"))
	inventory[0].Home = filepath.Join(second.Home, ".claude")
	secondChanges, err := second.Plan(policy, inventory)
	if err != nil {
		t.Fatal(err)
	}
	firstRelative, err := filepath.Rel(first.Home, firstChanges[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	secondRelative, err := filepath.Rel(second.Home, secondChanges[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	if firstRelative != secondRelative {
		t.Fatalf("equivalent layouts produced different names: %s, %s", firstRelative, secondRelative)
	}
	if firstChanges[0].Path == secondChanges[0].Path {
		t.Fatal("independent home stores collided")
	}
}
