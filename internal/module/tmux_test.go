package module

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/config"
)

// The ahub menu is opt-in (#331): off, tmux.conf has no prefix+A; on, a real
// tmux loads the file without error and binds prefix+A to the menu, while
// every default binding stays as it was.
func TestTmuxConfAhubKeysOptIn(t *testing.T) {
	render := func(t *testing.T, on bool) string {
		cfg := &config.Config{}
		cfg.Modules.Workspace.Enabled = true
		cfg.Modules.Workspace.AhubKeys = on
		rc, _, _ := twoHomeRunContext(t, cfg)
		out, err := rc.Template.Render("tmux/tmux.conf.tmpl", rc.Config.TemplateData(rc.HomeDir))
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	off, on := render(t, false), render(t, true)
	if strings.Contains(off, "bind A") || strings.Contains(off, "ahub") {
		t.Fatalf("the menu must be absent unless ahub_keys is set:\n%s", off)
	}
	for _, line := range strings.Split(off, "\n") {
		if !strings.Contains(on, line) {
			t.Fatalf("turning ahub_keys on changed an existing line: %q", line)
		}
	}
	if !strings.Contains(on, `dot hub #{q:session_name} git`) || !strings.Contains(on, "TMUX_PANE=#{pane_id}") {
		t.Fatalf("menu entries missing:\n%s", on)
	}

	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	dir, err := os.MkdirTemp("", "dk") // short: a socket path is limited to ~100 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	conf := filepath.Join(dir, "tmux.conf")
	if err := os.WriteFile(conf, []byte(on), 0o644); err != nil {
		t.Fatal(err)
	}
	tmux := func(args ...string) (string, error) {
		c := exec.Command("tmux", append([]string{"-S", filepath.Join(dir, "s"), "-f", conf}, args...)...)
		c.Env = append(os.Environ(), "TMUX=")
		out, err := c.CombinedOutput()
		return string(out), err
	}
	if out, err := tmux("new-session", "-d", "-s", "probe"); err != nil {
		t.Fatalf("tmux rejected the rendered config: %v\n%s", err, out)
	}
	t.Cleanup(func() { _, _ = tmux("kill-server") })
	keys, err := tmux("list-keys", "-T", "prefix")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`-T prefix A\s+display-menu`).MatchString(keys) || !regexp.MustCompile(`-T prefix H\s+resize-pane -L 5`).MatchString(keys) {
		t.Fatalf("prefix table after loading:\n%s", keys)
	}
}
