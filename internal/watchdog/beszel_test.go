package watchdog

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

func renderBeszelPlistOrFail(t *testing.T, agentPath, envPath, listen, logDir string) string {
	t.Helper()
	plist, err := RenderBeszelPlist(agentPath, envPath, listen, logDir)
	if err != nil {
		t.Fatalf("RenderBeszelPlist: %v", err)
	}
	return plist
}

func TestRenderBeszelPlist_Golden(t *testing.T) {
	got := renderBeszelPlistOrFail(t, "/opt/homebrew/bin/beszel-agent", "$HOME/.config/beszel/agent.env", ":45876", "/Users/test/Library/Logs/dot")
	golden := filepath.Join("testdata", "beszel.plist.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden (run with UPDATE_GOLDEN=1 to create): %v", err)
	}
	if got != string(want) {
		t.Errorf("beszel plist drifted from golden:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// The env-sourcing line is the whole reason the unit is dot-owned: the agent
// gets HUB_URL/KEY/TOKEN/LISTEN from the secrets-managed env file, and
// launchd restarts the long-running agent (KeepAlive) from the resolved brew
// binary.
func TestRenderBeszelPlist_SourcesTheEnvFileAndKeepsAlive(t *testing.T) {
	plist := renderBeszelPlistOrFail(t, "/opt/homebrew/bin/beszel-agent", "$HOME/.config/beszel/agent.env", ":45876", "/tmp/logs")
	for _, want := range []string{
		BeszelLabel,
		`<string>/bin/sh</string>`,
		`set -a; . "$HOME/.config/beszel/agent.env"; set +a; export LISTEN="${LISTEN:-:45876}"; exec "/opt/homebrew/bin/beszel-agent"`,
		"<key>KeepAlive</key>",
		"<key>RunAtLoad</key>",
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("beszel plist missing %q", want)
		}
	}
}

// The resolved listen is only the fallback: the ${LISTEN:-...} form lets an
// env-file LISTEN win over the config value (precedence: env file > config).
func TestRenderBeszelPlist_CustomListenIsTheFallback(t *testing.T) {
	plist := renderBeszelPlistOrFail(t, "/usr/local/bin/beszel-agent", "$HOME/.config/beszel/agent.env", ":9999", "/tmp/logs")
	if !strings.Contains(plist, `export LISTEN="${LISTEN:-:9999}"`) {
		t.Errorf("custom listen not rendered as the env-overridable fallback:\n%s", plist)
	}
}

func TestRenderBeszelPlist_RejectsPathsThePlistCannotCarry(t *testing.T) {
	good := []string{"/opt/homebrew/bin/beszel-agent", "$HOME/.config/beszel/agent.env", ":45876", "/tmp/logs"}
	for i, name := range []string{"agent path", "env path", "listen", "log dir"} {
		for _, bad := range []string{`quote"inside`, "line\nbreak", "carriage\rreturn"} {
			args := append([]string(nil), good...)
			args[i] = bad
			if _, err := RenderBeszelPlist(args[0], args[1], args[2], args[3]); err == nil {
				t.Errorf("%s %q must be rejected", name, bad)
			} else if !strings.Contains(err.Error(), name) {
				t.Errorf("rejection must name the %s: %v", name, err)
			}
		}
	}
}

// An ampersand or angle bracket is legal in a path but corrupts plist XML
// raw; the renderer must escape it (and launchd decodes it back).
func TestRenderBeszelPlist_XmlEscapesInterpolatedValues(t *testing.T) {
	plist := renderBeszelPlistOrFail(t, "/opt/homebrew/bin/beszel-agent", "$HOME/.config/beszel/agent.env", ":45876", "/tmp/r&d/<logs>")
	for _, want := range []string{"/tmp/r&amp;d/&lt;logs&gt;/beszel.out.log", "/tmp/r&amp;d/&lt;logs&gt;/beszel.err.log"} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist missing the escaped %q:\n%s", want, plist)
		}
	}
	if strings.Contains(plist, "/tmp/r&d/") {
		t.Errorf("raw ampersand leaked into the plist:\n%s", plist)
	}
}

func TestManager_BeszelPaths(t *testing.T) {
	m := NewManager(nil, "/home/u")
	if got := m.BeszelPlistPath(); got != "/home/u/Library/LaunchAgents/com.dotfiles.beszel-agent.plist" {
		t.Errorf("BeszelPlistPath = %q", got)
	}
	if got := m.BeszelEnvPath(); got != "/home/u/.config/beszel/agent.env" {
		t.Errorf("BeszelEnvPath = %q", got)
	}
}

func TestManager_BeszelEnvRef(t *testing.T) {
	m := NewManager(nil, "/home/u")
	if got := m.BeszelEnvRef("/home/u/.config/beszel/agent.env"); got != "$HOME/.config/beszel/agent.env" {
		t.Errorf("in-home env ref = %q, want $HOME-relative", got)
	}
	if got := m.BeszelEnvRef("/var/lib/beszel/agent.env"); got != "/var/lib/beszel/agent.env" {
		t.Errorf("outside-home env ref = %q, want absolute", got)
	}
	// A sibling home must not collapse into $HOME: /home/u2 is not under /home/u.
	if got := m.BeszelEnvRef("/home/u2/agent.env"); got != "/home/u2/agent.env" {
		t.Errorf("sibling-home env ref = %q, want absolute", got)
	}
}

func TestManager_BeszelRefusalsOffDarwin(t *testing.T) {
	m := linuxManager(t.TempDir())
	if err := m.InstallBeszel(context.Background(), "/usr/local/bin/beszel-agent", "/home/u/.config/beszel/agent.env", ":45876"); !errors.Is(err, ErrNeedsDarwin) {
		t.Fatalf("InstallBeszel off-darwin = %v, want ErrNeedsDarwin", err)
	}
	if _, statErr := os.Stat(m.BeszelPlistPath()); !os.IsNotExist(statErr) {
		t.Fatalf("plist exists after a refused install: %v", statErr)
	}
	if err := m.UninstallBeszel(context.Background()); !errors.Is(err, ErrNeedsDarwin) {
		t.Fatalf("UninstallBeszel off-darwin = %v, want ErrNeedsDarwin", err)
	}
	if st := m.ProbeBeszel(context.Background()); st.PlistExists || st.Loaded {
		t.Fatalf("beszel probe off-darwin = %#v, want all false", st)
	}
}

// A path the renderer cannot carry must fail the install BEFORE the plist is
// written or launchctl is touched.
func TestManager_InstallBeszel_RejectsBadPathsBeforeWriting(t *testing.T) {
	m := NewManager(exec.NewRunner(false, slog.Default()), t.TempDir())
	m.GOOS = "darwin"
	err := m.InstallBeszel(context.Background(), `/opt/homebrew/bin/"beszel"`, "/home/u/.config/beszel/agent.env", ":45876")
	if err == nil || !strings.Contains(err.Error(), "agent path") {
		t.Fatalf("InstallBeszel with a quoted agent path = %v, want a validation error", err)
	}
	if _, statErr := os.Stat(m.BeszelPlistPath()); !os.IsNotExist(statErr) {
		t.Fatalf("plist written despite the validation failure: %v", statErr)
	}
}

// Uninstall removes the plist and nothing else: the secrets-managed env file
// next to it must survive. launchctl unload of a never-loaded unit fails and
// is ignored by design, so this runs the same on every platform.
func TestManager_UninstallBeszel_RemovesPlistOnly(t *testing.T) {
	home := t.TempDir()
	m := NewManager(exec.NewRunner(false, slog.Default()), home)
	m.GOOS = "darwin"
	envPath := m.BeszelEnvPath()
	for _, path := range []string{m.BeszelPlistPath(), envPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.UninstallBeszel(context.Background()); err != nil {
		t.Fatalf("UninstallBeszel: %v", err)
	}
	if _, err := os.Stat(m.BeszelPlistPath()); !os.IsNotExist(err) {
		t.Fatalf("plist still present after uninstall: %v", err)
	}
	if _, err := os.Stat(envPath); err != nil {
		t.Fatalf("the secrets-managed env file must survive uninstall: %v", err)
	}
}
