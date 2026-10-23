package module

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/STAIxBWLB/dotfiles/internal/aisettings"
	"github.com/STAIxBWLB/dotfiles/internal/aitooling"
	"github.com/STAIxBWLB/dotfiles/internal/config"
)

// AIModule manages AI CLI/config helper shell configs and Claude settings.
type AIModule struct{}

func (m *AIModule) Name() string { return "ai" }

func (m *AIModule) managedFiles(rc *RunContext) []templatedFile {
	files := []templatedFile{
		{
			templatePath: "shell/30-ai.sh.tmpl",
			destPath:     filepath.Join(rc.HomeDir, ".config", "shell", "30-ai.sh"),
			isTemplate:   true,
			perm:         0644,
		},
		{
			templatePath: "claude/settings.json.tmpl",
			destPath:     filepath.Join(rc.HomeDir, ".config", "claude", "settings.json"),
			isTemplate:   true,
			perm:         0644,
		},
	}
	if selection := rc.Config.Modules.AI.Tooling; selection != nil {
		selected := false
		for _, id := range selection.Agents {
			if id == "claude" {
				selected = true
			}
		}
		if !selected {
			return files[:1]
		}
	}
	return files
}

func (m *AIModule) Check(ctx context.Context, rc *RunContext) (*CheckResult, error) {
	if err := config.ValidateAIPolicy(rc.Config.Modules.AI.Policy); err != nil {
		return nil, err
	}
	changes, err := checkTemplatedFiles(rc, m.managedFiles(rc))
	if err != nil {
		return nil, err
	}

	legacy := filepath.Join(rc.HomeDir, ".config", "shell", "30-ai-tools.sh")
	if rc.Runner.FileExists(legacy) {
		changes = append(changes, Change{
			Description: fmt.Sprintf("remove legacy %s", legacy),
			Command:     fmt.Sprintf("rm %s", legacy),
		})
	}
	// Copilot's fan-out target moved from ~/.config/github-copilot/AGENTS.md
	// to ~/.copilot/copilot-instructions.md. Only a dot-rendered file (managed
	// header) is removed: the old dir also holds IDE auth files, and the
	// AGENTS.md there could be user-authored.
	legacyCopilot := filepath.Join(rc.HomeDir, ".config", "github-copilot", "AGENTS.md")
	if rc.Config.Modules.AI.Tooling == nil && aisettings.IsManagedAgentsFile(legacyCopilot) {
		changes = append(changes, Change{
			Description: fmt.Sprintf("remove legacy %s", legacyCopilot),
			Command:     fmt.Sprintf("rm %q", legacyCopilot),
		})
	}
	// Cursor and Grok were dropped from the agents registry (Grok reads
	// ~/.claude/CLAUDE.md through its Claude compat); the same rule applies to
	// their retired fan-out targets as to Copilot's above.
	for _, retired := range retiredAgentsTargets(rc.HomeDir) {
		if aisettings.IsManagedAgentsFile(retired) {
			changes = append(changes, Change{
				Description: fmt.Sprintf("remove legacy %s", retired),
				Command:     fmt.Sprintf("rm %q", retired),
			})
		}
	}
	if (rc.Config.Modules.AI.Tooling == nil && rc.Config.Modules.AI.AgentsSSOT) || (rc.Config.Modules.AI.Tooling != nil && len(rc.Config.Modules.AI.Tooling.Agents) > 0) {
		manager := aisettings.NewAgentsManager(rc.Runner, rc.HomeDir, rc.ExplicitHome)
		manager.Out = rc.out()
		manager.ExplicitHome = rc.ExplicitHome
		if selection := rc.Config.Modules.AI.Tooling; selection != nil {
			manager.SelectedTools = append([]string{}, selection.Agents...)
		}
		if rc.Config.Modules.AI.Tooling != nil {
			needed, err := manager.ContinuityPolicyNeeded()
			if err != nil {
				return nil, err
			}
			if needed {
				changes = append(changes, Change{Description: "install shared development context guidance", Command: "dot ai tools apply"})
			}
		}
		// Only where amem is in use; a malformed marker is a finding at apply time, never a failed Check.
		if needed, _ := manager.MemoryBlockNeeded(); needed {
			changes = append(changes, Change{Description: "write the amem memory block and drop the retired claude-mem block", Command: "dot ai agents apply"})
		}
		statuses, err := manager.Status()
		if err != nil {
			return nil, fmt.Errorf("agents SSOT status: %w", err)
		}
		applySet := make(map[string]bool)
		for _, id := range manager.DefaultApplyTools() {
			applySet[id] = true
		}
		for _, st := range statuses {
			if !applySet[st.Tool.ID] || st.Drift == "in-sync" {
				continue
			}
			changes = append(changes, Change{
				Description: fmt.Sprintf("reapply agents SSOT to %s (%s)", st.Tool.ID, st.Drift),
				Command:     fmt.Sprintf("dot ai agents apply --tool %s", st.Tool.ID),
			})
		}
	}
	if rc.Config.Modules.AI.HUD && rc.Config.Modules.AI.Tooling == nil {
		manager := aisettings.NewHUDManager(rc.Runner, rc.HomeDir)
		statuses, err := manager.Status(nil)
		if err != nil {
			return nil, fmt.Errorf("AI HUD status: %w", err)
		}
		for _, st := range statuses {
			if st.Drift == "in-sync" {
				continue
			}
			changes = append(changes, Change{
				Description: fmt.Sprintf("apply AI HUD to %s (%s)", st.ToolID, st.Drift),
				Command:     fmt.Sprintf("dot ai hud apply --tool %s", st.ToolID),
			})
		}
	}
	// modules.ai.skills is intentionally not checked: runtime skill symlinks
	// are owned by the Maru app; dot only offers read-only diagnostics via
	// `dot ai skills status`.
	if mode := rc.Config.Modules.Git.CoauthorGuard; mode != "" && mode != aisettings.CoauthorGuardOff {
		manager := aisettings.NewCoauthorGuardManager(rc.Runner, rc.HomeDir, rc.ExplicitHome)
		status, err := manager.Status(mode)
		if err != nil {
			return nil, fmt.Errorf("coauthor guard status: %w", err)
		}
		if status.AgentsDrift != "in-sync" {
			changes = append(changes, Change{
				Description: fmt.Sprintf("apply coauthor guard AGENTS instruction (%s)", status.AgentsDrift),
				Command:     "dot ai coauthor-guard apply",
			})
		}
	}

	if selection := rc.Config.Modules.AI.Tooling; selection != nil {
		report, err := newModuleToolingEngine(rc).Run(ctx, *selection, aitooling.Inspect)
		if err != nil {
			return nil, err
		}
		for _, item := range report.Items {
			if !toolingStatusSatisfied(item) {
				changes = append(changes, Change{Description: fmt.Sprintf("%s: %s (%s)", item.ID, item.Status, item.Detail), Command: "dot ai tools apply"})
			}
		}
	}
	policyChanges, err := checkModulePolicy(ctx, rc)
	if err != nil {
		return nil, err
	}
	changes = append(changes, policyChanges...)
	return &CheckResult{Satisfied: len(changes) == 0, Changes: changes}, nil
}

func (m *AIModule) Apply(ctx context.Context, rc *RunContext) (*ApplyResult, error) {
	if err := config.ValidateAIPolicy(rc.Config.Modules.AI.Policy); err != nil {
		return nil, err
	}
	var messages []string
	var toolingErr error
	if selection := rc.Config.Modules.AI.Tooling; selection != nil {
		if err := aitooling.ValidateSelection(*selection); err != nil {
			return nil, err
		}
		report, err := newModuleToolingEngine(rc).Run(ctx, *selection, aitooling.Ensure)
		if err != nil && len(report.Items) == 0 {
			return nil, err
		}
		for _, item := range report.Items {
			messages = append(messages, fmt.Sprintf("%s: %s (%s)", item.ID, item.Status, item.Detail))
		}

		if ctx.Err() != nil {
			return &ApplyResult{Messages: messages}, ctx.Err()
		}
		for _, item := range report.Items {
			if item.Kind == "resource" || item.ID == "admission" {
				return &ApplyResult{Messages: messages}, fmt.Errorf("tooling admission blocked: %s", item.Detail)
			}
		}
		toolingErr = err
		if toolingErr == nil && (report.Failed > 0 || report.Deferred > 0) {
			toolingErr = fmt.Errorf("tooling incomplete: %d failed, %d deferred", report.Failed, report.Deferred)
		}
	}

	fileMessages, err := applyTemplatedFiles(rc, m.managedFiles(rc))
	if err != nil {
		return nil, err
	}
	messages = append(messages, fileMessages...)

	legacy := filepath.Join(rc.HomeDir, ".config", "shell", "30-ai-tools.sh")
	if rc.Runner.FileExists(legacy) {
		if err := rc.Runner.Remove(legacy); err != nil {
			return nil, fmt.Errorf("removing legacy %s: %w", legacy, err)
		}
		messages = append(messages, fmt.Sprintf("removed legacy %s", legacy))
	}
	// See Check: only remove the old copilot target if dot rendered it.
	legacyCopilot := filepath.Join(rc.HomeDir, ".config", "github-copilot", "AGENTS.md")
	if rc.Config.Modules.AI.Tooling == nil && aisettings.IsManagedAgentsFile(legacyCopilot) {
		if err := rc.Runner.Remove(legacyCopilot); err != nil {
			return nil, fmt.Errorf("removing legacy %s: %w", legacyCopilot, err)
		}
		messages = append(messages, fmt.Sprintf("removed legacy %s", legacyCopilot))
	}
	for _, retired := range retiredAgentsTargets(rc.HomeDir) {
		if aisettings.IsManagedAgentsFile(retired) {
			if err := rc.Runner.Remove(retired); err != nil {
				return nil, fmt.Errorf("removing legacy %s: %w", retired, err)
			}
			messages = append(messages, fmt.Sprintf("removed legacy %s", retired))
		}
	}
	if mode := rc.Config.Modules.Git.CoauthorGuard; mode != "" && mode != aisettings.CoauthorGuardOff {
		manager := aisettings.NewCoauthorGuardManager(rc.Runner, rc.HomeDir, rc.ExplicitHome)
		result, err := manager.Apply(aisettings.CoauthorGuardOptions{Mode: mode, DryRun: rc.DryRun, ApplyAgents: rc.Config.Modules.AI.AgentsSSOT && rc.Config.Modules.AI.Tooling == nil})
		if err != nil {
			return nil, fmt.Errorf("applying coauthor guard AGENTS instruction: %w", err)
		}
		if result.AgentsChanged {
			messages = append(messages, "applied coauthor guard AGENTS instruction")
		}
		if result.AgentsApplied {
			messages = append(messages, "reapplied agents SSOT after coauthor guard update")
		}
	}

	if (rc.Config.Modules.AI.Tooling == nil && rc.Config.Modules.AI.AgentsSSOT) || (rc.Config.Modules.AI.Tooling != nil && len(rc.Config.Modules.AI.Tooling.Agents) > 0) {
		manager := aisettings.NewAgentsManager(rc.Runner, rc.HomeDir, rc.ExplicitHome)
		manager.Out = rc.out()
		manager.ExplicitHome = rc.ExplicitHome
		if selection := rc.Config.Modules.AI.Tooling; selection != nil {
			manager.SelectedTools = append([]string{}, selection.Agents...)
		}
		ssotMissing := !rc.Runner.FileExists(manager.SSOTPath())
		if ssotMissing {
			// Fresh machines enable agents_ssot by default, so the first apply
			// must scaffold the SSOT before rendering it to tool targets.
			if _, err := manager.Init(aisettings.InitOptions{}); err != nil {
				return nil, fmt.Errorf("scaffolding agents SSOT: %w", err)
			}
			messages = append(messages, fmt.Sprintf("scaffolded agents SSOT %s", manager.SSOTPath()))
		}
		if rc.Config.Modules.AI.Tooling != nil {
			changed, err := manager.EnsureContinuityPolicy()
			if err != nil {
				return nil, err
			}
			if changed {
				messages = append(messages, "updated shared development context guidance")
			}
		}
		if !rc.DryRun {
			// Also right after the scaffold above, and only on a machine that uses amem. A bad marker is a
			// message the person reads, never a reason to fail the rest of the apply.
			res, err := manager.EnsureMemoryBlock(false)
			if err != nil {
				return nil, err
			}
			switch {
			case res.Warning != "":
				messages = append(messages, "warning: "+res.Warning)
			case res.Changed:
				messages = append(messages, fmt.Sprintf("updated the amem memory block in the agents SSOT (backup: %s)", res.Backup))
			}
		}
		if ssotMissing && rc.DryRun {
			// Dry-run writes nothing, so there is no SSOT to render yet.
			messages = append(messages, "dry-run: agents SSOT apply deferred until the SSOT is scaffolded")
		} else {
			result, err := manager.Apply(aisettings.ApplyOptions{Tools: manager.DefaultApplyTools(), DryRun: rc.DryRun})
			if err != nil {
				return nil, fmt.Errorf("applying agents SSOT: %w", err)
			}
			for _, item := range result.Items {
				if item.Changed {
					messages = append(messages, fmt.Sprintf("applied agents SSOT to %s", item.TargetPath))
				}
			}
			messages = append(messages, result.Warnings...)
		}
	}
	if rc.Config.Modules.AI.HUD && rc.Config.Modules.AI.Tooling == nil {
		manager := aisettings.NewHUDManager(rc.Runner, rc.HomeDir)
		result, err := manager.Apply(aisettings.HUDOptions{DryRun: rc.DryRun})
		if err != nil {
			return nil, fmt.Errorf("applying AI HUD: %w", err)
		}
		for _, item := range result.Items {
			if item.Changed {
				messages = append(messages, fmt.Sprintf("applied AI HUD to %s", item.ToolID))
			}
		}
	}

	policyMessages, policyErr := applyModulePolicy(ctx, rc)
	messages = append(messages, policyMessages...)
	return &ApplyResult{Changed: len(messages) > 0, Messages: messages}, errors.Join(toolingErr, policyErr)
}

func newModuleToolingEngine(rc *RunContext) *aitooling.Engine {
	home, _ := os.UserHomeDir()
	return aitooling.New(aitooling.Options{HomeDir: rc.HomeDir, ExplicitHome: rc.ExplicitHome || filepath.Clean(home) != filepath.Clean(rc.HomeDir), DryRun: rc.DryRun, Out: rc.out()})
}

func toolingStatusSatisfied(item aitooling.ItemResult) bool {
	switch item.Status {
	case "installed", "ready", "in-sync", "up-to-date", "pinned", "updated":
		return true
	case "update-available":
		return item.Installed != ""
	}
	return false
}

// retiredAgentsTargets lists fan-out targets of tools dropped from the agents
// registry. Check and Apply remove each one only if dot rendered it.
func retiredAgentsTargets(home string) []string {
	return []string{filepath.Join(home, ".cursor", "AGENTS.md"), filepath.Join(home, ".grok", "AGENTS.md")}
}
