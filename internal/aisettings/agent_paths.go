package aisettings

import (
	"fmt"
	"github.com/STAIxBWLB/dotfiles/internal/config"
	"os"
	"path/filepath"
)

func containsAgentID(ids []string, id string) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}

// InstructionPath resolves only the selected runtime's documented override.
// Explicit homes must never inherit the invoking agent's profile paths.
func InstructionPath(home, id string, explicit bool, fallback string) string {
	actual, _ := os.UserHomeDir()
	if explicit || filepath.Clean(home) != filepath.Clean(actual) {
		return fallback
	}
	var root string
	switch id {
	case "codex":
		root = os.Getenv("CODEX_HOME")
	case "kimi":
		root = os.Getenv("KIMI_CODE_HOME")
	case "opencode":
		root = os.Getenv("OPENCODE_CONFIG_DIR")
		if root == "" {
			root = filepath.Join(config.ConfigHome(home, explicit), "opencode")
		}
	}
	if root != "" && filepath.IsAbs(root) {
		return filepath.Join(root, "AGENTS.md")
	}
	return fallback
}

// codexStandardProfile adopts the standard and active profile without duplicating
// writes when either directory is a symlink to the same instruction file.
func (m *AgentsManager) codexStandardProfile() *AgentsManager {
	if m.ExplicitHome || m.profilePass {
		return nil
	}
	active, err := m.TargetPath("codex")
	if err != nil {
		return nil
	}
	standard := filepath.Join(m.homeDir(), ".codex", "AGENTS.md")
	if filepath.Clean(active) == filepath.Clean(standard) {
		return nil
	}
	resolve := func(path string) string {
		if p, err := filepath.EvalSymlinks(path); err == nil {
			return p
		}
		if p, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
			return filepath.Join(p, filepath.Base(path))
		}
		return path
	}
	if resolve(active) == resolve(standard) {
		return nil
	}
	clone := *m
	clone.ExplicitHome = true
	clone.profilePass = true
	clone.SelectedTools = []string{"codex"}
	return &clone
}

// Separate active Codex profiles share overlays, never conflict baselines.
func (m *AgentsManager) stateKey(id string) string {
	if id == "codex" {
		target, err := m.TargetPath(id)
		standard := filepath.Join(m.homeDir(), ".codex", "AGENTS.md")
		if err == nil && filepath.Clean(target) != filepath.Clean(standard) {
			return id + ":" + normalizedHash([]byte(target))
		}
	}
	return id
}

func validateInstructionOverride(home, id string, explicit bool) error {
	actual, _ := os.UserHomeDir()
	if explicit || filepath.Clean(home) != filepath.Clean(actual) {
		return nil
	}
	var keys []string
	switch id {
	case "codex":
		keys = []string{"CODEX_HOME"}
	case "kimi":
		keys = []string{"KIMI_CODE_HOME"}
	case "opencode":
		keys = []string{"OPENCODE_CONFIG_DIR"}
		if os.Getenv("OPENCODE_CONFIG_DIR") == "" {
			if !filepath.IsAbs(config.ConfigHome(home, explicit)) {
				return fmt.Errorf("XDG_CONFIG_HOME must be an absolute path; refusing instruction fallback")
			}
		}
	}
	for _, key := range keys {
		if value := os.Getenv(key); value != "" && !filepath.IsAbs(value) {
			return fmt.Errorf("%s must be an absolute path; refusing instruction fallback", key)
		}
	}
	return nil
}
