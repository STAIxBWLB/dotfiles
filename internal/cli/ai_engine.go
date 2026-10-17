package cli

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/aisettings"
	"github.com/STAIxBWLB/dotfiles/internal/config"
	execrun "github.com/STAIxBWLB/dotfiles/internal/exec"
)

// Command-side assembly for the ai family: the cobra flags a command was
// given become the aisettings engine and managers that run it, plus the
// user-state load/save the persist flags need.

func newAIEngine(cmd *cobra.Command) (*aisettings.Engine, error) {
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
		return nil, fmt.Errorf("load state: %w", err)
	}
	home, _ := os.UserHomeDir()
	if homeOverride != "" {
		home = homeOverride
	}
	root := resolveBackupRoot(cmd, state, home)
	hostname, _ := os.Hostname()
	if idx := strings.Index(hostname, "."); idx > 0 {
		hostname = hostname[:idx]
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return &aisettings.Engine{
		Runner:   execrun.NewRunner(dryRun, logger),
		HomeDir:  home,
		Root:     root,
		Hostname: hostname,
		User:     os.Getenv("USER"),
	}, nil
}

func newAgentsManagerFromCmd(cmd *cobra.Command) *aisettings.AgentsManager {
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	home, _ := os.UserHomeDir()
	if over, _ := cmd.Flags().GetString("home"); over != "" {
		home = over
	}
	logger := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), &slog.HandlerOptions{Level: slog.LevelWarn}))
	over, _ := cmd.Flags().GetString("home")
	mgr := aisettings.NewAgentsManager(execrun.NewRunner(dryRun, logger), home, over != "")
	mgr.Out = cmd.OutOrStdout()
	if selection, err := effectiveToolingForCmd(cmd); err != nil {
		mgr.SelectionError = err
	} else if selection != nil {
		mgr.SelectionError = nil
		mgr.SelectedTools = append([]string{}, selection.Agents...)
	} else if path, _ := cmd.Flags().GetString("config"); path != "" {
		mgr.SelectionError = nil
		mgr.SelectedTools = []string{}
	}
	return mgr
}

func newHUDManagerFromCmd(cmd *cobra.Command) *aisettings.HUDManager {
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	home, _ := os.UserHomeDir()
	if over, _ := cmd.Flags().GetString("home"); over != "" {
		home = over
	}
	logger := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), &slog.HandlerOptions{Level: slog.LevelWarn}))
	return aisettings.NewHUDManager(execrun.NewRunner(dryRun, logger), home)
}

func newCoauthorGuardManagerFromCmd(cmd *cobra.Command) *aisettings.CoauthorGuardManager {
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	home, _ := os.UserHomeDir()
	if over, _ := cmd.Flags().GetString("home"); over != "" {
		home = over
	}
	logger := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), &slog.HandlerOptions{Level: slog.LevelWarn}))
	over, _ := cmd.Flags().GetString("home")
	return aisettings.NewCoauthorGuardManager(execrun.NewRunner(dryRun, logger), home, over != "")
}

func loadStateForCmd(cmd *cobra.Command) (*config.UserState, error) {
	if homeOverride, _ := cmd.Flags().GetString("home"); homeOverride != "" {
		return config.LoadStateForHome(homeOverride)
	}
	return config.LoadState()
}

func saveStateForCmd(cmd *cobra.Command, state *config.UserState) error {
	if homeOverride, _ := cmd.Flags().GetString("home"); homeOverride != "" {
		return config.SaveStateForHome(homeOverride, state)
	}
	return config.SaveState(state)
}

// effectiveToolingForCmd gives an explicitly supplied config authority over saved
// selections, including an absent/empty tooling block (no broad fallback).
func effectiveToolingForCmd(cmd *cobra.Command) (*config.AIToolingConfig, error) {
	path, _ := cmd.Flags().GetString("config")
	if path != "" {
		cfg, err := config.Load("", path, nil)
		if err != nil {
			return nil, err
		}
		return cfg.Modules.AI.Tooling.Clone(), nil
	}
	state, err := loadStateForCmd(cmd)
	if err != nil {
		return nil, err
	}
	return state.Modules.AI.Tooling.Clone(), nil
}

// Explicit config owns both selection presence and AI enablement. Empty tooling
// blocks stop legacy fanout without enabling an omitted/disabled AI module.
func applyStateWithToolingAuthority(cfg *config.Config, state *config.UserState, explicit bool) {
	selected := cfg.Modules.AI.Tooling.Clone()
	policy := cfg.Modules.AI.Policy.Clone()
	hasPolicy := policy != nil && policy.Enabled
	enabled := cfg.Modules.AI.Enabled
	hasSelection := selected != nil
	config.ApplyStateToConfig(cfg, state)
	if explicit {
		cfg.Modules.AI.Policy = policy
		if selected == nil {
			selected = &config.AIToolingConfig{Agents: []string{}}
		}
		cfg.Modules.AI.Tooling = selected
		cfg.Modules.AI.Enabled = enabled || hasSelection || hasPolicy
	}
}
