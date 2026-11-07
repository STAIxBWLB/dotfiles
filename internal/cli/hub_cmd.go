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
		Use:   "hub <project> [console|status|ui|git|files]",
		Short: "Open a registered project's agent-hub console, status or dashboard, or its Git or Files window",
		Long: `Open agent-hub (ahub) for a registered project, at its registered root.

  console (default)  Outside tmux, runs 'ahub --project <root> console' in this
                     terminal. Inside that project's workspace session, focuses
                     its AHUB pane, or adds one 'hub' window to a session
                     without one. Elsewhere in tmux it prints how to switch.
  status             Runs 'ahub --project <root> status' once.
  ui                 Runs 'ahub --project <root> ui' (the local dashboard).
  git, files         Inside that project's workspace session only: focuses its
                     'git' or 'files' window, or adds one that runs lazygit
                     (else git status) or yazi (else eza, tree, ls).

With modules.workspace.ahub_keys: true, prefix+A opens a menu with these.

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
	slot, isSlot := hubSlots[action]
	if !isSlot && action != "status" && action != "ui" {
		return fmt.Errorf("unknown action %q; use console, status, ui, git or files", action)
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
	if os.Getenv("TMUX") == "" && slot.tool != "" {
		return fmt.Errorf("'dot hub %s %s' opens a window in the %s workspace; run it there, or 'dot open %s' first", name, action, name, name)
	}
	if slot.tool == "" {
		if _, err := osexec.LookPath("ahub"); err != nil {
			return fmt.Errorf("ahub is not on PATH; install it with 'dot ai family install ahub'")
		}
	}
	if !isSlot || os.Getenv("TMUX") == "" {
		return hubExec([]string{"ahub", "--project", proj.Path, action})
	}
	if _, err := workspace.Deploy(); err != nil {
		return fmt.Errorf("deploying workspace scripts: %w", err)
	}
	dataDir, err := workspace.DataDir()
	if err != nil {
		return err
	}
	msg, err := focusSlot(name, proj.Path, dataDir, slot)
	// Under a key binding's run-shell, any output leaves the calling pane in
	// view mode; the new window is feedback enough, so only guidance is shown.
	if msg != "" && (!strings.HasPrefix(msg, "Added a ") || stdoutIsTerminal()) {
		printerFrom(cmd).Line("%s", msg)
	}
	return err
}

// hubSlot is a window dot hub focuses or adds: the pane tag (@dot_slot), the
// window name, and the tools.sh launcher it runs (empty for the AHUB slot,
// which runs ahub-pane.sh).
type hubSlot struct{ tag, window, tool string }

var hubSlots = map[string]hubSlot{
	"console": {"hub", "hub", ""},
	"git":     {"git", "git", "run_lazygit"},
	"files":   {"files", "files", "run_filetree"},
}

// hubRunTool runs a tools.sh launcher against a pane; tests replace it.
var hubRunTool = func(toolsScript, fn, pane, root string) error {
	return osexec.Command("bash", "-c", `source "$1" && PANE="$2" && "$3" "$4"`, "dot-hub", toolsScript, pane, fn, root).Run()
}

// focusSlot focuses the project workspace's pane tagged with slot, or adds
// one window for it when the session has none. The session must be the
// project's by name and by start directory; any other session gets guidance
// only. A tmux lock serializes concurrent calls so two never add two windows.
// The caller's session is read through $TMUX_PANE when set: under run-shell
// (a key binding) there is no client, and an untargeted display-message
// names the most recent session instead.
func focusSlot(name, root, dataDir string, slot hubSlot) (string, error) {
	read := []string{"display-message", "-p"}
	if pane := os.Getenv("TMUX_PANE"); pane != "" {
		read = append(read, "-t", pane)
	}
	current, err := hubTmux(append(read, "#{session_name}\t#{session_path}")...)
	if err != nil {
		return "", fmt.Errorf("reading the current tmux session: %w", err)
	}
	session, path, _ := strings.Cut(current, "\t")
	if session != name || !samePath(path, root) {
		msg := fmt.Sprintf("This tmux session (%s) is not the %s workspace at %s. Switch with 'tmux switch-client -t %s' and run 'dot hub %s' again", session, name, root, name, name)
		if slot.tool == "" {
			return msg + fmt.Sprintf(", or run 'ahub --project %s console' here.", shellQuote(root)), nil
		}
		return msg + ".", nil
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
		if len(f) == 3 && f[2] == slot.tag {
			if _, err := hubTmux("select-window", "-t", f[1]); err != nil {
				return "", err
			}
			_, err := hubTmux("select-pane", "-t", f[0])
			return "", err
		}
	}
	pane, err := hubTmux("new-window", "-t", "="+name+":", "-n", slot.window, "-c", root, "-P", "-F", "#{pane_id}")
	if err != nil {
		return "", fmt.Errorf("adding the %s window: %w", slot.window, err)
	}
	if _, err := hubTmux("set-option", "-p", "-t", pane, "@dot_slot", slot.tag); err != nil {
		return "", fmt.Errorf("preparing the %s window: %w", slot.window, err)
	}
	if slot.tool != "" {
		if err := hubRunTool(filepath.Join(dataDir, "tools.sh"), slot.tool, pane, root); err != nil {
			return "", fmt.Errorf("starting %s in the %s window: %w", slot.tool, slot.window, err)
		}
	} else {
		for _, step := range [][]string{
			{"select-pane", "-t", pane, "-T", "AHUB"},
			{"send-keys", "-t", pane, "bash " + shellQuote(filepath.Join(dataDir, "ahub-pane.sh")) + " hub " + shellQuote(root), "Enter"},
		} {
			if _, err := hubTmux(step...); err != nil {
				return "", fmt.Errorf("preparing the hub window: %w", err)
			}
		}
	}
	return "Added a '" + slot.window + "' window to " + name + ".", nil
}

func stdoutIsTerminal() bool {
	info, err := os.Stdout.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func samePath(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && filepath.Clean(ra) == filepath.Clean(rb)
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
