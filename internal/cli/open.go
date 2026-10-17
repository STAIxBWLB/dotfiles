package cli

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	osexec "os/exec"
	"strings"
	"syscall"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/workspace"
	"github.com/spf13/cobra"
)

func newOpenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "open <project>",
		Short: "Launch or resume a tmux workspace",
		Long: `Launch a new tmux workspace for a project, or resume an existing session.
If the project is not registered, you'll be prompted to register it.`,
		Args: cobra.ExactArgs(1),
		RunE: runOpen,
	}
	cmd.Flags().String("layout", "", "Layout to use (dev, claude, monitor)")
	cmd.Flags().String("theme", "", "Theme to use (default, dracula, nord, catppuccin, tokyo-night)")
	cmd.Flags().Bool("install-optional", false, "Also install optional tools (lazygit, btop, yazi, eza)")
	return cmd
}

func runOpen(cmd *cobra.Command, args []string) error {
	name := args[0]
	ctx := context.Background()
	p := printerFrom(cmd)

	// Validate session name
	if err := workspace.ValidateSessionName(name); err != nil {
		return err
	}

	// Detect nested tmux
	if os.Getenv("TMUX") != "" {
		p.Line("Already inside a tmux session.")
		p.Line("Use 'tmux switch-client -t %s' to switch, or detach first (C-a d).", name)
		return nil
	}

	// Load workspace config
	cfg, err := workspace.LoadConfig()
	if err != nil {
		return fmt.Errorf("loading workspace config: %w", err)
	}

	proj := cfg.FindProject(name)
	if proj == nil {
		// Auto-register with current directory
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting current directory: %w", err)
		}

		yes, _ := cmd.Flags().GetBool("yes")
		if !yes {
			p.Raw("Project %q is not registered. Register %s as %q? [Y/n] ", name, cwd, name)
			reader := bufio.NewReader(os.Stdin)
			answer, _ := reader.ReadString('\n')
			answer = strings.TrimSpace(strings.ToLower(answer))
			if answer != "" && answer != "y" && answer != "yes" {
				return fmt.Errorf("aborted")
			}
		}

		if err := cfg.AddProject(name, cwd, "", ""); err != nil {
			return err
		}
		if err := cfg.Save(); err != nil {
			return fmt.Errorf("saving config: %w", err)
		}
		p.Line("  Registered project %q → %s", name, cwd)
		proj = cfg.FindProject(name)
	}

	// Verify project path still exists
	if _, err := os.Stat(proj.Path); err != nil {
		return fmt.Errorf("project path %q does not exist (re-register with 'dot register %s <path>')", proj.Path, name)
	}

	// Determine layout and theme
	layout := cfg.EffectiveLayout(proj)
	theme := cfg.EffectiveTheme(proj)

	if flagLayout, _ := cmd.Flags().GetString("layout"); flagLayout != "" {
		if !workspace.IsValidLayout(flagLayout) {
			return fmt.Errorf("unknown layout %q; valid: %v", flagLayout, workspace.ValidLayouts())
		}
		layout = flagLayout
	}
	if flagTheme, _ := cmd.Flags().GetString("theme"); flagTheme != "" {
		if !workspace.IsValidTheme(flagTheme) {
			return fmt.Errorf("unknown theme %q; valid: %v", flagTheme, workspace.ValidThemes())
		}
		theme = flagTheme
	}

	// Check and install dependencies
	runner := exec.NewRunner(false, slog.Default())
	brew := exec.NewBrew(runner)

	if err := workspace.InstallRequired(ctx, runner, brew, cmd.OutOrStdout()); err != nil {
		return fmt.Errorf("installing required tools: %w", err)
	}

	installOptional, _ := cmd.Flags().GetBool("install-optional")
	if installOptional {
		workspace.InstallOptional(ctx, runner, brew, cmd.OutOrStdout())
	}

	// Deploy shell scripts
	changed, err := workspace.Deploy()
	if err != nil {
		return fmt.Errorf("deploying workspace scripts: %w", err)
	}
	if changed {
		p.Line("  Workspace scripts updated")
	}

	// Get launcher path
	launcher, err := workspace.LauncherPath()
	if err != nil {
		return err
	}

	// Exec into the launcher (replaces this process)
	bashPath, err := findBash()
	if err != nil {
		return err
	}

	p.Line("  Opening workspace: %s (layout=%s, theme=%s)", name, layout, theme)
	if err := syscall.Exec(bashPath, []string{
		"bash", launcher, name, proj.Path, layout, theme,
	}, os.Environ()); err != nil {
		return fmt.Errorf("exec %s %s: %w", bashPath, launcher, err)
	}
	return nil
}

func findBash() (string, error) {
	// Try SHELL env first if it's bash
	if shell := os.Getenv("SHELL"); strings.HasSuffix(shell, "/bash") {
		if _, err := os.Stat(shell); err == nil {
			return shell, nil
		}
	}
	// Try PATH lookup
	if p, err := osexec.LookPath("bash"); err == nil {
		return p, nil
	}
	// Fallback to well-known paths
	for _, p := range []string{"/opt/homebrew/bin/bash", "/usr/local/bin/bash", "/bin/bash"} {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("bash not found")
}
