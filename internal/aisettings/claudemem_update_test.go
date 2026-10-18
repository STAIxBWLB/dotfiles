package aisettings

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// seedUpdateHome builds a home with a marketplace checkout at version
// market and an installed_plugins.json at version installed ("" = absent).
func seedUpdateHome(t *testing.T, market, installed string) string {
	t.Helper()
	home := t.TempDir()
	pluginDir := filepath.Join(home, ".claude", "plugins", "marketplaces", "thedotmack", "plugin", ".claude-plugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.json"),
		[]byte(`{"name":"claude-mem","version":"`+market+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if installed != "" {
		cacheDir := filepath.Join(home, ".claude", "plugins")
		doc := `{"version":2,"plugins":{"claude-mem@thedotmack":[{"scope":"user","installPath":"` +
			filepath.Join(cacheDir, "cache", "thedotmack", "claude-mem", installed) + `","version":"` + installed + `"}]}}`
		if err := os.WriteFile(filepath.Join(cacheDir, "installed_plugins.json"), []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

// dryRunner never executes a mutation, so the update flow is testable on
// any machine regardless of whether claude/codex are installed.
func dryRunner() *exec.Runner {
	return exec.NewRunner(true, slog.Default())
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"13.28.0", "13.28.0", 0},
		{"13.25.2", "13.28.0", -1},
		{"13.28.0", "13.9.0", 1}, // string compare gets this wrong
		{"13.9.0", "13.28.0", -1},
		{"14.0.0", "13.99.9", 1},
	}
	for _, tc := range cases {
		if got := compareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestMarketplaceAndInstalledVersions(t *testing.T) {
	home := seedUpdateHome(t, "13.28.0", "13.25.3")
	mgr := NewClaudeMemManager(home, "", "")
	market, err := mgr.MarketplacePluginVersion()
	if err != nil {
		t.Fatal(err)
	}
	if market != "13.28.0" {
		t.Errorf("marketplace = %q", market)
	}
	if got := mgr.InstalledClaudeMemVersion(); got != "13.25.3" {
		t.Errorf("installed = %q", got)
	}
	if _, err := NewClaudeMemManager(t.TempDir(), "", "").MarketplacePluginVersion(); err == nil {
		t.Error("missing checkout must error")
	}
}

// The installed-version lookup must not drift to a foreign marketplace or a
// project-scoped record: the updater owns claude-mem@thedotmack, user scope.
func TestInstalledClaudeMemVersion_ExactKeyAndUserScope(t *testing.T) {
	home := t.TempDir()
	doc := `{"version":2,"plugins":{` +
		`"claude-mem@other-market":[{"scope":"user","version":"99.0.0"}],` +
		`"claude-mem@thedotmack":[{"scope":"project","version":"1.0.0"},{"scope":"user","version":"13.25.3"}]` +
		`}}`
	if err := os.MkdirAll(filepath.Join(home, ".claude", "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", "plugins", "installed_plugins.json"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	mgr := NewClaudeMemManager(home, "", "")
	if got := mgr.InstalledClaudeMemVersion(); got != "13.25.3" {
		t.Errorf("installed = %q, want the thedotmack user-scoped 13.25.3", got)
	}
}

func TestUpdateClaudeMemPlugin_AlreadyCurrent(t *testing.T) {
	home := seedUpdateHome(t, "13.28.0", "13.28.0")
	mgr := NewClaudeMemManager(home, "", "")
	result, err := mgr.UpdateClaudeMemPlugin(context.Background(), dryRunner(), false)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if result.Updated {
		t.Fatalf("current install must not update: %#v", result)
	}
	if result.After != "13.28.0" || result.MarketplaceVersion != "13.28.0" {
		t.Fatalf("versions = %#v", result)
	}
}

func TestUpdateClaudeMemPlugin_BehindUpdates(t *testing.T) {
	home := seedUpdateHome(t, "13.28.0", "13.25.3")
	mgr := NewClaudeMemManager(home, "", "")
	result, err := mgr.UpdateClaudeMemPlugin(context.Background(), dryRunner(), false)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !result.Updated {
		t.Fatalf("behind install must update: %#v", result)
	}
	// Dry-run reports the bump without claiming it happened.
	if result.After != result.Before {
		t.Fatalf("dry-run After = %q, want unchanged %q", result.After, result.Before)
	}
}

func TestUpdateClaudeMemPlugin_InstalledNewerIsNotDowngrade(t *testing.T) {
	home := seedUpdateHome(t, "13.25.3", "13.28.0")
	mgr := NewClaudeMemManager(home, "", "")
	result, err := mgr.UpdateClaudeMemPlugin(context.Background(), dryRunner(), false)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if result.Updated {
		t.Fatalf("a newer install than the marketplace must not update: %#v", result)
	}
}

func TestUpdateClaudeMemPlugin_NotInstalled(t *testing.T) {
	home := seedUpdateHome(t, "13.28.0", "")
	mgr := NewClaudeMemManager(home, "", "")
	if _, err := mgr.UpdateClaudeMemPlugin(context.Background(), dryRunner(), false); err == nil {
		t.Fatal("an uninstalled plugin must error with an install hint")
	}
}

func TestRenderSyncAgentPlist_Golden(t *testing.T) {
	got := RenderSyncAgentPlist("/Users/test/.local/bin/dot", "user@mac2", "/Users/test", "/Users/test/.claude-mem/logs/claude-mem-sync.log")
	golden := filepath.Join("testdata", "claude-mem-sync.plist.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden (run with UPDATE_GOLDEN=1 to create): %v", err)
	}
	if got != string(want) {
		t.Errorf("sync plist drifted from golden:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestRenderSyncAgentPlist_Contract(t *testing.T) {
	plist := RenderSyncAgentPlist("/usr/local/bin/dot", "user@mac2", "/home/u", "/home/u/.claude-mem/logs/claude-mem-sync.log")
	for _, want := range []string{
		ClaudeMemSyncLaunchdLabel,
		"ssh -o BatchMode=yes -o ConnectTimeout=10 &#39;user@mac2&#39; true || exit 0",
		"&#39;/usr/local/bin/dot&#39; ai memory sync --peer &#39;user@mac2&#39; &amp; pid=$!",
		"(sleep 900; kill -TERM $pid; sleep 30; kill -KILL $pid)",
		"exit $rc",
		"<key>DOT_SCHEDULED_RUN</key>",
		"<integer>3600</integer>",
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("sync plist missing %q", want)
		}
	}
}

// The peer and dot path land inside a /bin/sh -c script: they must be
// shell-quoted so a hostile or space-bearing value cannot break out.
func TestRenderSyncAgentPlist_ShellQuotesTheScript(t *testing.T) {
	plist := RenderSyncAgentPlist("/usr/local/bin/dot", "user@mac2;touch /tmp/pwned", "/home/u", "/tmp/log")
	if !strings.Contains(plist, "&#39;user@mac2;touch /tmp/pwned&#39;") {
		t.Errorf("peer is not single-quoted in the agent script:\n%s", plist)
	}
}

func TestInstallSyncAgent_RefusesOffDarwinAndEmptyPeer(t *testing.T) {
	mgr := NewClaudeMemManager(t.TempDir(), "/usr/local/bin/dot", "")
	if err := mgr.InstallSyncAgent(context.Background(), ""); err == nil {
		t.Fatal("empty peer must be refused")
	}
	if runtime.GOOS == "darwin" {
		t.Skip("on darwin the install path would touch launchd; covered by the golden + contract tests")
	}
	if err := mgr.InstallSyncAgent(context.Background(), "user@mac2"); err == nil {
		t.Fatal("off-darwin install must be refused")
	}
}
