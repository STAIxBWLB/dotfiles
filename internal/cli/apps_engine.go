package cli

import (
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/appsettings"
	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// cli-side assembly for the apps engine: everything here reads flags or
// state and hands the engine plain values. No orchestration lives in this
// file.

// appsBrewCtx loads user state and constructs a Brew wrapper + Runner.
func appsBrewCtx(cmd *cobra.Command) (*config.UserState, *exec.Brew, *exec.Runner, error) {
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	homeOverride, _ := cmd.Flags().GetString("home")

	var state *config.UserState
	var err error
	if homeOverride != "" {
		state, err = config.LoadStateForHome(homeOverride)
	} else {
		state, err = config.LoadState()
	}
	if err != nil {
		return nil, nil, nil, fmt.Errorf("load state: %w", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	runner := exec.NewRunner(dryRun, logger)
	brew := exec.NewBrew(runner)
	return state, brew, runner, nil
}

// appsBrewForQuery returns a Brew handle for read-only cask queries, or nil
// off macOS where no cask can be installed. A state-load failure also yields
// nil: the catalog listings degrade to "install state unknown" rather than
// failing the command.
func appsBrewForQuery(cmd *cobra.Command) *exec.Brew {
	if runtime.GOOS != "darwin" {
		return nil
	}
	_, brew, _, _ := appsBrewCtx(cmd)
	return brew
}

// newAppsEngine constructs an appsettings.Engine from flags + state.
// Resolution precedence for the backup root: --to / --from > state.BackupRoot
// > auto-detected Drive > default local dir.
func newAppsEngine(cmd *cobra.Command) (*appsettings.Engine, error) {
	state, _, runner, err := appsBrewCtx(cmd)
	if err != nil {
		return nil, err
	}

	home, _ := os.UserHomeDir()
	if over, _ := cmd.Flags().GetString("home"); over != "" {
		home = over
	}

	root := resolveBackupRoot(cmd, state, home)

	hostname, _ := os.Hostname()
	if idx := strings.Index(hostname, "."); idx > 0 {
		hostname = hostname[:idx]
	}

	mf, err := appsettings.LoadManifest()
	if err != nil {
		return nil, err
	}

	return &appsettings.Engine{
		Runner:   runner,
		HomeDir:  home,
		Root:     root,
		Hostname: hostname,
		Manifest: mf,
	}, nil
}

// resolveBackupRoot centralizes the flag → state → detect → default chain.
func resolveBackupRoot(cmd *cobra.Command, state *config.UserState, home string) string {
	if v, err := cmd.Flags().GetString("to"); err == nil && v != "" {
		return appsettings.ExpandHome(v, home)
	}
	if v, err := cmd.Flags().GetString("from"); err == nil && v != "" {
		return appsettings.ExpandHome(v, home)
	}
	if state.Modules.MacApps.BackupRoot != "" {
		return appsettings.ExpandHome(state.Modules.MacApps.BackupRoot, home)
	}
	if cloud := appsettings.DetectCloudCandidate(home); cloud != "" {
		return cloud
	}
	return appsettings.DefaultBackupRoot(home)
}

// resolveBackupTokens picks which manifest entries should be backed up / restored.
// Precedence:
//  1. explicit tokens on the command line (caller passes them)
//  2. --all flag → every manifest entry
//  3. state.Modules.MacApps.BackupApps → user's curated backup list
//  4. manifest ∩ installed casks (default when brew is available)
//  5. every manifest entry (fallback)
func resolveBackupTokens(cmd *cobra.Command, eng *appsettings.Engine) []string {
	all, _ := cmd.Flags().GetBool("all")
	tokens := eng.Manifest.Tokens()
	if all {
		return tokens
	}

	state, brew, _, _ := appsBrewCtx(cmd)
	if state != nil && len(state.Modules.MacApps.BackupApps) > 0 {
		return intersectManifest(state.Modules.MacApps.BackupApps, eng.Manifest)
	}

	if brew == nil || !brew.IsAvailable() {
		return tokens
	}
	installed := brew.InstalledCasks()
	if len(installed) == 0 {
		return tokens
	}
	var out []string
	for _, t := range tokens {
		if installed[t] {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return tokens
	}
	return out
}

func intersectManifest(tokens []string, mf *appsettings.Manifest) []string {
	valid := make(map[string]bool)
	for _, t := range mf.Tokens() {
		valid[t] = true
	}
	var out []string
	for _, t := range tokens {
		if valid[t] {
			out = append(out, t)
		}
	}
	return out
}
