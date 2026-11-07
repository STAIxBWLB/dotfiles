package cli

import (
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/STAIxBWLB/dotfiles/internal/workspace"
	"github.com/spf13/cobra"
)

func newHubCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "hub <project> [console|status|ui]",
		Short: "Open a registered project's agent-hub console, status or dashboard",
		Long: `Open agent-hub (ahub) for a registered project, at its registered root.

  console (default)  Outside tmux, runs 'ahub --project <root> console' in this
                     terminal. Inside that project's workspace session, focuses
                     its AHUB pane, or adds one 'hub' window to a session
                     without one. Elsewhere in tmux it prints how to switch.
  status             Runs 'ahub --project <root> status' once.
  ui                 Runs 'ahub --project <root> ui' (the local dashboard).

It never starts the hub or a peer: an AHUB slot starts the hub only when you
press Enter, and 'ahub up' is yours to run. It never relayouts a workspace.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: runHub,
	}
}

// hubTmux runs one tmux command for dot hub; tests replace it.
var hubTmux = func(args ...string) (string, error) {
	out, err := osexec.Command("tmux", args...).Output()
	return strings.TrimSpace(string(out)), err
}

// hubExec replaces dot with an ahub command; tests replace it.
var hubExec = func(argv []string) error {
	path, err := osexec.LookPath(argv[0])
	if err != nil {
		return err
	}
	return syscall.Exec(path, argv, os.Environ())
}

func runHub(cmd *cobra.Command, args []string) error {
	name, action := args[0], "console"
	if len(args) == 2 {
		action = args[1]
	}
	if action != "console" && action != "status" && action != "ui" {
		return fmt.Errorf("unknown action %q; use console, status or ui", action)
	}
	if err := workspace.ValidateSessionName(name); err != nil {
		return err
	}
	cfg, err := workspace.LoadConfig()
	if err != nil {
		return fmt.Errorf("loading workspace config: %w", err)
	}
	proj := cfg.FindProject(name)
	if proj == nil {
		return fmt.Errorf("project %q is not registered; register it with 'dot register %s <path>'", name, name)
	}
	if _, err := osexec.LookPath("ahub"); err != nil {
		return fmt.Errorf("ahub is not on PATH; install it with 'dot ai family install ahub'")
	}
	if action != "console" || os.Getenv("TMUX") == "" {
		return hubExec([]string{"ahub", "--project", proj.Path, action})
	}
	if _, err := workspace.Deploy(); err != nil {
		return fmt.Errorf("deploying workspace scripts: %w", err)
	}
	dataDir, err := workspace.DataDir()
	if err != nil {
		return err
	}
	msg, err := focusHub(name, proj.Path, filepath.Join(dataDir, "ahub-pane.sh"))
	if msg != "" {
		printerFrom(cmd).Line("%s", msg)
	}
	return err
}

// focusHub focuses the project workspace's AHUB slot, or adds one 'hub'
// window when the session has none. The session must be the project's by name
// and by start directory; any other session gets guidance only. A tmux lock
// serializes concurrent calls so two never add two windows.
func focusHub(name, root, paneScript string) (string, error) {
	current, err := hubTmux("display-message", "-p", "#{session_name}\t#{session_path}")
	if err != nil {
		return "", fmt.Errorf("reading the current tmux session: %w", err)
	}
	session, path, _ := strings.Cut(current, "\t")
	if session != name || !samePath(path, root) {
		return fmt.Sprintf("This tmux session (%s) is not the %s workspace at %s. Switch with 'tmux switch-client -t %s' and run 'dot hub %s' again, or run 'ahub --project %s console' here.", session, name, root, name, name, shellQuote(root)), nil
	}
	lock := "dot-hub-" + name
	if _, err := hubTmux("wait-for", "-L", lock); err != nil {
		return "", fmt.Errorf("locking %s: %w", lock, err)
	}
	defer func() { _, _ = hubTmux("wait-for", "-U", lock) }()

	panes, err := hubTmux("list-panes", "-s", "-t", "="+name, "-F", "#{pane_id}\t#{window_id}\t#{@dot_slot}")
	if err != nil {
		return "", fmt.Errorf("listing panes of %s: %w", name, err)
	}
	for _, line := range strings.Split(panes, "\n") {
		f := strings.Split(line, "\t")
		if len(f) == 3 && f[2] == "hub" {
			if _, err := hubTmux("select-window", "-t", f[1]); err != nil {
				return "", err
			}
			_, err := hubTmux("select-pane", "-t", f[0])
			return "", err
		}
	}
	pane, err := hubTmux("new-window", "-t", "="+name+":", "-n", "hub", "-c", root, "-P", "-F", "#{pane_id}")
	if err != nil {
		return "", fmt.Errorf("adding the hub window: %w", err)
	}
	for _, step := range [][]string{
		{"set-option", "-p", "-t", pane, "@dot_slot", "hub"},
		{"select-pane", "-t", pane, "-T", "AHUB"},
		{"send-keys", "-t", pane, "bash " + shellQuote(paneScript) + " hub " + shellQuote(root), "Enter"},
	} {
		if _, err := hubTmux(step...); err != nil {
			return "", fmt.Errorf("preparing the hub window: %w", err)
		}
	}
	return "Added a 'hub' window to " + name + ".", nil
}

func samePath(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && filepath.Clean(ra) == filepath.Clean(rb)
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
