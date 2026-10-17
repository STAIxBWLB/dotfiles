package module

import (
	"context"
	"fmt"

	"github.com/STAIxBWLB/dotfiles/internal/aipolicy"
	"github.com/STAIxBWLB/dotfiles/internal/config"
)

// modulePolicyChanges never probes CLIs on a strict dry run. Native base
// preferences are not owned here; only generated launch overlays are reconciled.
func modulePolicyChanges(ctx context.Context, rc *RunContext, apply bool) ([]aipolicy.PreferenceChange, error) {
	policy := rc.Config.Modules.AI.Policy
	if err := config.ValidateAIPolicy(policy); err != nil {
		return nil, err
	}
	if policy == nil || !policy.Enabled {
		return nil, nil
	}
	if len(policy.Targets) == 0 {
		return []aipolicy.PreferenceChange{{Status: "deferred", Constraint: "enabled AI policy has no validated targets"}}, nil
	}
	if rc.DryRun {
		return []aipolicy.PreferenceChange{{Status: "unverified", Constraint: "AI policy preview: runtime probes and overlay writes skipped in strict dry run"}}, nil
	}
	inventory, err := aipolicy.Inspect(ctx, rc.HomeDir, rc.ExplicitHome)
	if err != nil {
		return nil, err
	}
	preferences := aipolicy.Preferences{Home: rc.HomeDir, Explicit: rc.ExplicitHome}
	if apply {
		return preferences.Apply(policy, inventory, false)
	}
	return preferences.Status(policy, inventory)
}
func checkModulePolicy(ctx context.Context, rc *RunContext) ([]Change, error) {
	items, err := modulePolicyChanges(ctx, rc, false)
	if err != nil {
		return nil, err
	}
	changes := []Change{}
	for _, item := range items {
		if item.Status != "current" {
			changes = append(changes, Change{Description: fmt.Sprintf("AI policy %s: %s (%s)", item.TargetID, item.Status, item.Constraint), Command: "dot ai policy apply"})
		}
	}
	return changes, nil
}
func applyModulePolicy(ctx context.Context, rc *RunContext) ([]string, error) {
	items, err := modulePolicyChanges(ctx, rc, true)
	messages := []string{}
	incomplete := false
	for _, item := range items {
		if item.Status == "current" {
			continue
		}
		messages = append(messages, fmt.Sprintf("AI policy %s: %s (%s)", item.TargetID, item.Status, item.Constraint))
		if item.Status != "applied" && item.Status != "unverified" {
			incomplete = true
		}
	}
	if err != nil {
		return messages, err
	}
	if incomplete {
		return messages, fmt.Errorf("AI policy incomplete: unsupported or unvalidated targets remain deferred")
	}
	return messages, nil
}
