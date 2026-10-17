package module

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/STAIxBWLB/dotfiles/internal/fileutil"
)

const condarcContent = `auto_activate_base: false
channels:
  - conda-forge
  - defaults
`

// CondaModule detects conda/mamba and ensures .condarc.
type CondaModule struct{}

func (m *CondaModule) Name() string { return "conda" }

func (m *CondaModule) condaCmd(rc *RunContext) string {
	if rc.Runner.CommandExists("mamba") {
		return "mamba"
	}
	if rc.Runner.CommandExists("conda") {
		return "conda"
	}
	return ""
}

func (m *CondaModule) Check(ctx context.Context, rc *RunContext) (*CheckResult, error) {
	var changes []Change

	cmd := m.condaCmd(rc)
	if cmd == "" {
		changes = append(changes, Change{
			Description: "conda/mamba not found in PATH",
			Command:     "install conda or mamba",
		})
		return &CheckResult{Satisfied: false, Changes: changes}, nil
	}

	condarcPath := filepath.Join(rc.HomeDir, ".condarc")
	if fileutil.NeedsUpdate(rc.Runner, condarcPath, []byte(condarcContent)) {
		changes = append(changes, Change{
			Description: fmt.Sprintf("write %s", condarcPath),
			Command:     "write default .condarc",
		})
	}

	return &CheckResult{Satisfied: len(changes) == 0, Changes: changes}, nil
}

func (m *CondaModule) Apply(ctx context.Context, rc *RunContext) (*ApplyResult, error) {
	var messages []string

	cmd := m.condaCmd(rc)
	if cmd == "" {
		return &ApplyResult{
			Changed:  false,
			Messages: []string{"conda/mamba not found, skipping"},
		}, nil
	}

	// Ensure .condarc
	condarcPath := filepath.Join(rc.HomeDir, ".condarc")
	written, err := fileutil.EnsureFile(rc.Runner, rc.HomeDir, condarcPath, []byte(condarcContent), 0644)
	if err != nil {
		return nil, fmt.Errorf("writing %s: %w", condarcPath, err)
	}
	if written {
		messages = append(messages, fmt.Sprintf("wrote %s", condarcPath))
	}

	return &ApplyResult{Changed: len(messages) > 0, Messages: messages}, nil
}
