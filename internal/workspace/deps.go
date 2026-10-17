package workspace

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// outOrStdout normalizes a nil writer to os.Stdout, so the package states
// its nil rule once.
func outOrStdout(w io.Writer) io.Writer {
	if w == nil {
		return os.Stdout
	}
	return w
}

// ToolInfo describes a workspace dependency.
type ToolInfo struct {
	Name     string
	Formula  string // brew formula name
	Required bool   // if true, must be installed; if false, fallback is used
}

// WorkspaceTools returns the list of tools used by workspace layouts.
func WorkspaceTools() []ToolInfo {
	return []ToolInfo{
		{Name: "tmux", Formula: "tmux", Required: true},
		{Name: "claude", Formula: "", Required: false}, // installed via npm, not brew
		{Name: "lazygit", Formula: "lazygit", Required: false},
		{Name: "btop", Formula: "btop", Required: false},
		{Name: "yazi", Formula: "yazi", Required: false},
		{Name: "eza", Formula: "eza", Required: false},
	}
}

// DepStatus holds the installation status of workspace tools.
type DepStatus struct {
	Installed []string
	Missing   []string // optional tools not found (will use fallback)
	Required  []string // required tools not found (must install)
}

// CheckDeps checks which workspace tools are available.
func CheckDeps(runner *exec.Runner) *DepStatus {
	status := &DepStatus{}
	for _, tool := range WorkspaceTools() {
		if runner.CommandExists(tool.Name) {
			status.Installed = append(status.Installed, tool.Name)
		} else if tool.Required {
			status.Required = append(status.Required, tool.Name)
		} else {
			status.Missing = append(status.Missing, tool.Name)
		}
	}
	return status
}

// InstallRequired installs required missing tools via brew.
// Optional tools are skipped (they have fallbacks in the shell scripts).
// Progress lines go to out; a nil out means process stdout.
func InstallRequired(ctx context.Context, runner *exec.Runner, brew *exec.Brew, out io.Writer) error {
	out = outOrStdout(out)
	status := CheckDeps(runner)
	if len(status.Required) == 0 {
		return nil
	}

	if !brew.IsAvailable() {
		return fmt.Errorf("required tools missing (%v) and brew is not available; run 'dot apply' first", status.Required)
	}

	// Collect brew formulas for required tools
	toolMap := make(map[string]ToolInfo)
	for _, t := range WorkspaceTools() {
		toolMap[t.Name] = t
	}

	var formulas []string
	for _, name := range status.Required {
		if t, ok := toolMap[name]; ok && t.Formula != "" {
			formulas = append(formulas, t.Formula)
		}
	}

	if len(formulas) == 0 {
		return fmt.Errorf("required tools missing but no brew formula available: %v", status.Required)
	}

	fmt.Fprintf(out, "  Installing required tools: %v\n", formulas)
	return brew.Install(ctx, formulas)
}

// InstallOptional attempts to install optional missing tools via brew.
// Failures are logged but not fatal.
// Progress lines go to out; a nil out means process stdout.
func InstallOptional(ctx context.Context, runner *exec.Runner, brew *exec.Brew, out io.Writer) {
	out = outOrStdout(out)
	if !brew.IsAvailable() {
		return
	}

	status := CheckDeps(runner)
	if len(status.Missing) == 0 {
		return
	}

	toolMap := make(map[string]ToolInfo)
	for _, t := range WorkspaceTools() {
		toolMap[t.Name] = t
	}

	var formulas []string
	for _, name := range status.Missing {
		if t, ok := toolMap[name]; ok && t.Formula != "" {
			formulas = append(formulas, t.Formula)
		}
	}

	if len(formulas) == 0 {
		return
	}

	fmt.Fprintf(out, "  Installing optional tools: %v\n", formulas)
	if err := brew.Install(ctx, formulas); err != nil {
		fmt.Fprintf(out, "  Warning: some optional tools failed to install: %v\n", err)
	}
}
