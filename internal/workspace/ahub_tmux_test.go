package workspace

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestAhubLayoutGeometry builds the ahub layouts on an isolated tmux server at
// the reference terminal sizes and measures the panes (#331 AC2): AHUB keeps at
// least 80x24 wherever the full console is claimed, a small terminal gets the
// compact topology, and an 80x24 terminal opens the plain console.
func TestAhubLayoutGeometry(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	sock, err := os.MkdirTemp("", "dt") // short: a socket path is limited to ~100 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sock) })
	bin, project := t.TempDir(), t.TempDir()
	// A fake ahub: the hub runs, and the console reports how it was opened.
	fake := "#!/bin/sh\ncase \"$*\" in *status*) exit 0 ;; *console*) echo \"FAKE-CONSOLE $*\"; sleep 30 ;; esac\n"
	if err := os.WriteFile(filepath.Join(bin, "ahub"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "TMUX_TMPDIR="+sock, "PATH="+bin+":"+os.Getenv("PATH"), "TMUX=",
		"DOT_AHUB_PEER_A_ID=codex", "DOT_AHUB_PEER_A_ARGS=codex", "DOT_AHUB_PEER_B_ID=claude", "DOT_AHUB_PEER_B_ARGS=claude")
	tmux := func(t *testing.T, args ...string) string {
		t.Helper()
		c := exec.Command("tmux", append([]string{"-f", "/dev/null"}, args...)...)
		c.Env = env
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("tmux %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	// A bare server with a plain shell, so the person's tmux.conf and shell rc stay out.
	tmux(t, "new-session", "-d", "-s", "boot")
	t.Cleanup(func() { c := exec.Command("tmux", "kill-server"); c.Env = env; _ = c.Run() })
	tmux(t, "set-option", "-g", "default-shell", bash)
	tmux(t, "set-option", "-g", "default-command", bash+" --norc --noprofile")

	scripts, _ := filepath.Abs("scripts")
	for _, tc := range []struct {
		layout     string
		cols, rows int
		wantWindow string // the window holding AHUB
		minW, minH int    // AHUB pane at least
		plain      bool   // the plain console instead of --panels
	}{
		{"ahub", 220, 60, "work", 88, 59, false},
		{"ahub", 200, 56, "work", 80, 55, false},
		{"ahub", 160, 45, "hub", 160, 44, false}, // below 200x56: compact
		{"ahub-compact", 100, 30, "hub", 100, 29, false},
		{"ahub-compact", 80, 25, "hub", 80, 24, false},
		{"ahub-compact", 80, 24, "hub", 80, 23, true},
	} {
		session := fmt.Sprintf("t%dx%d%s", tc.cols, tc.rows, strings.TrimPrefix(tc.layout, "ahub"))
		t.Run(session, func(t *testing.T) {
			c := exec.Command(bash, "-c", `source "$1/layouts.sh" && load_layout "$2" "$3" "$4" default`, "_", scripts, tc.layout, session, project)
			c.Env = append(env, fmt.Sprintf("DOT_COLS=%d", tc.cols), fmt.Sprintf("DOT_ROWS=%d", tc.rows))
			if out, err := c.CombinedOutput(); err != nil {
				t.Fatalf("load_layout: %v\n%s", err, out)
			}
			rows := tmux(t, "list-panes", "-s", "-t", "="+session, "-F", "#{window_name}\t#{@dot_slot}\t#{pane_width}\t#{pane_height}\t#{pane_id}")
			slots := map[string]bool{}
			var hubPane string
			for _, line := range strings.Split(rows, "\n") {
				f := strings.Split(line, "\t")
				slots[f[1]] = true
				if f[1] != "hub" {
					continue
				}
				hubPane = f[4]
				var w, h int
				fmt.Sscan(f[2], &w)
				fmt.Sscan(f[3], &h)
				if f[0] != tc.wantWindow || w < tc.minW || h < tc.minH {
					t.Errorf("AHUB in window %s is %dx%d, want window %s and at least %dx%d", f[0], w, h, tc.wantWindow, tc.minW, tc.minH)
				}
			}
			for _, slot := range []string{"hub", "peer-a", "peer-b", "verify"} {
				if !slots[slot] {
					t.Errorf("slot %s missing; panes:\n%s", slot, rows)
				}
			}
			if root := tmux(t, "show-options", "-v", "-t", session, "@dot_root"); root != project {
				t.Errorf("@dot_root %q, want %q", root, project)
			}
			want := "console --panels"
			if tc.plain {
				want = "below 80x24"
			}
			var screen string
			for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
				if screen = tmux(t, "capture-pane", "-p", "-t", hubPane); strings.Contains(screen, want) {
					return
				}
			}
			t.Errorf("AHUB pane never showed %q:\n%s", want, screen)
		})
	}
}
