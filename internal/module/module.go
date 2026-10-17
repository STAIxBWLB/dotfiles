package module

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/template"
)

// Module is the interface that all dotfiles modules must implement.
type Module interface {
	Name() string
	Check(ctx context.Context, rc *RunContext) (*CheckResult, error)
	Apply(ctx context.Context, rc *RunContext) (*ApplyResult, error)
}

// RunContext carries config and runtime options to all modules.
type RunContext struct {
	Config       *config.Config
	Runner       *exec.Runner
	Brew         *exec.Brew
	Template     *template.Engine
	DryRun       bool
	Yes          bool
	HomeDir      string
	ExplicitHome bool
	// Out receives module progress output; nil means os.Stdout.
	Out io.Writer
}

// out returns the writer progress output goes to: rc.Out when set,
// os.Stdout when rc or rc.Out is nil.
func (rc *RunContext) out() io.Writer {
	if rc == nil || rc.Out == nil {
		return os.Stdout
	}
	return rc.Out
}

// CheckResult holds the result of a module's Check operation.
type CheckResult struct {
	Satisfied bool
	Changes   []Change
}

// Change describes a single pending change.
type Change struct {
	Description string
	Command     string
}

// ApplyResult holds the result of a module's Apply operation.
type ApplyResult struct {
	Changed  bool
	Messages []string
}

// defaultOrder defines the static module execution order.
var defaultOrder = []string{
	"packages",
	"shell",
	"node",
	"git",
	"ssh",
	"terminal",
	"container",
	"tmux",
	"workspace",
	"ai",
	"fonts",
	"macapps",
	"conda",
	"gpg",
	"secrets",
}

// Registry manages module registration and resolution.
type Registry struct {
	modules map[string]Module
}

// NewRegistry creates a registry with all modules registered.
func NewRegistry() *Registry {
	r := &Registry{modules: make(map[string]Module)}
	r.Register(&PackagesModule{})
	r.Register(&ShellModule{})
	r.Register(&NodeModule{})
	r.Register(&GitModule{})
	r.Register(&SSHModule{})
	r.Register(&TerminalModule{})
	r.Register(&ContainerModule{})
	r.Register(&TmuxModule{})
	r.Register(&WorkspaceModule{})
	r.Register(&AIModule{})
	r.Register(&FontsModule{})
	r.Register(&MacAppsModule{})
	r.Register(&CondaModule{})
	r.Register(&GPGModule{})
	r.Register(&SecretsModule{})
	return r
}

// NormalizeFilter validates module filters and catches removed public names.
func NormalizeFilter(filter []string) ([]string, error) {
	for _, name := range filter {
		if name == "ai-tools" {
			return nil, fmt.Errorf("module %q was renamed to %q; use --module ai", "ai-tools", "ai")
		}
	}
	return filter, nil
}

// Register adds or replaces a module.
func (r *Registry) Register(m Module) {
	r.modules[m.Name()] = m
}

// Resolve returns modules in execution order, filtered by config and --module flag.
func (r *Registry) Resolve(cfg *config.Config, filter []string) []Module {
	filterSet := make(map[string]bool, len(filter))
	for _, f := range filter {
		filterSet[f] = true
	}

	var result []Module
	for _, name := range defaultOrder {
		if !cfg.IsModuleEnabled(name) {
			continue
		}
		if len(filterSet) > 0 && !filterSet[name] {
			continue
		}
		if m, ok := r.modules[name]; ok {
			result = append(result, m)
		}
	}
	return result
}

// RunAll executes Check then Apply on each module in order. A module whose
// Check fails counts as a failed module just as an Apply failure does, so a
// run that could not even evaluate a module never reports success.
func RunAll(ctx context.Context, modules []Module, rc *RunContext) error {
	var errors []string
	for _, m := range modules {
		check, err := m.Check(ctx, rc)
		if err != nil {
			fmt.Fprintf(rc.out(), "  ⚠ %s: check error: %v\n", m.Name(), err)
			errors = append(errors, fmt.Sprintf("%s: %v", m.Name(), err))
			continue
		}
		if check.Satisfied {
			fmt.Fprintf(rc.out(), "  ✓ %s: already satisfied\n", m.Name())
			continue
		}

		for _, c := range check.Changes {
			fmt.Fprintf(rc.out(), "  → %s: %s\n", m.Name(), c.Description)
		}

		if rc.DryRun {
			continue
		}

		result, err := m.Apply(ctx, rc)
		if err != nil {
			fmt.Fprintf(rc.out(), "  ✗ %s: %v\n", m.Name(), err)
			errors = append(errors, fmt.Sprintf("%s: %v", m.Name(), err))
			continue
		}
		if result.Changed {
			for _, msg := range result.Messages {
				fmt.Fprintf(rc.out(), "  ✓ %s: %s\n", m.Name(), msg)
			}
		}
	}
	if len(errors) > 0 {
		return fmt.Errorf("%d module(s) failed: %v", len(errors), errors)
	}
	return nil
}

// CheckAll runs Check on each module and returns results.
func CheckAll(ctx context.Context, modules []Module, rc *RunContext) (map[string]*CheckResult, error) {
	results := make(map[string]*CheckResult, len(modules))
	for _, m := range modules {
		check, err := m.Check(ctx, rc)
		if err != nil {
			return nil, fmt.Errorf("module %s check: %w", m.Name(), err)
		}
		results[m.Name()] = check
	}
	return results, nil
}
