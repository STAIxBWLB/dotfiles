package aipolicy

import (
	"fmt"
	"slices"
	"strings"

	"github.com/STAIxBWLB/dotfiles/internal/config"
)

func classify(task string) string {
	s := strings.ToLower(task)
	for _, group := range []struct {
		workload string
		words    []string
	}{
		{"independent-review", []string{"review", "audit", "검토", "리뷰"}},
		{"deep-analysis", []string{"diagnos", "debug", "research", "analysis", "조사", "분석", "원인"}},
		{"visual-production", []string{"image", "visual", "design", "디자인", "이미지"}},
		{"documents-teaching", []string{"document", "teach", "report", "문서", "강의", "보고서"}},
		{"implementation", []string{"implement", "code", "fix", "build", "구현", "수정", "개발"}},
		{"routine", []string{"status", "list", "check", "상태", "목록"}},
	} {
		for _, word := range group.words {
			if strings.Contains(s, word) {
				return group.workload
			}
		}
	}
	return "routine"
}

func Resolve(policy *config.AIPolicyConfig, request Request, inventory []Runtime) (result Resolution, err error) {
	defer func() {
		if err != nil {
			result.Reason = err.Error()
			result.Eligible = false
		}
	}()
	result = Resolution{KnowledgeApprovals: []KnowledgeApproval{}, SchemaVersion: 1, PermissionIntent: "auto-review", LaunchArgs: []string{}, Constraints: []string{}, Capabilities: []string{}, Continuity: "checkpoint-handoff"}
	if err := config.ValidateAIPolicy(policy); err != nil {
		return result, err
	}
	if policy == nil || !policy.Enabled {
		return result, fmt.Errorf("AI policy is disabled; configure and validate targets before enabling it")
	}
	result.PolicyRevision = policy.Revision
	workload := request.Workload
	if workload == "" || workload == "auto" {
		workload = classify(request.Task)
	}
	if !slices.Contains([]string{"routine", "implementation", "deep-analysis", "independent-review", "documents-teaching", "visual-production"}, workload) {
		return result, fmt.Errorf("unsupported workload %q", workload)
	}
	result.Workload = workload
	targets := append([]config.AIPolicyTarget{}, policy.Targets...)
	slices.SortStableFunc(targets, func(a, b config.AIPolicyTarget) int {
		if a.Priority > b.Priority {
			return -1
		}
		if a.Priority < b.Priority {
			return 1
		}
		return strings.Compare(a.ID, b.ID)
	})
	projectPaths := map[string][]string{}
	projectErrors := map[string]error{}
	inspectedPaths := map[string]bool{}
	for _, t := range targets {
		if request.Agent != "" && t.Agent != request.Agent {
			continue
		}
		reject := func(reason string) { result.Constraints = append(result.Constraints, t.ID+": "+reason) }
		if !t.Validated {
			reject("target has not been validated")
			continue
		}
		if t.Billing == "dgx" {
			reject("DGX endpoint/auth binding unverified; cannot auto-route")
			continue
		}
		if t.Billing != "subscription" {
			reject("billing is unknown")
			continue
		}
		if !slices.Contains(t.Workloads, workload) {
			reject("workload not validated")
			continue
		}
		idx := slices.IndexFunc(inventory, func(r Runtime) bool { return r.Agent == t.Agent && r.Available && r.Version == t.Version })
		if idx < 0 {
			reject("validated runtime version unavailable; inspect and revalidate target")
			continue
		}
		runtime := inventory[idx]
		if !runtime.SubscriptionVerified {
			reject("native subscription authentication unverified; sign in with a supported subscription account")
			continue
		}
		if t.Billing == "subscription" {
			if !inspectedPaths[t.Agent] {
				projectPaths[t.Agent], projectErrors[t.Agent] = projectSettingsFiles(t.Agent, request.CWD)
				inspectedPaths[t.Agent] = true
			}
			e := projectErrors[t.Agent]
			conflict := false
			if e == nil {
				conflict = projectFilesSubscriptionConflict(t.Agent, projectPaths[t.Agent])
			}
			if e != nil {
				reject(e.Error())
				continue
			}
			if conflict {
				reject("project native provider override conflicts with subscription billing")
				continue
			}
		}
		if t.Billing == "subscription" && runtime.SubscriptionConflict {
			reject("provider environment or config conflicts with subscription billing; remove provider overrides and reverify subscription authentication")
			continue
		}
		missing := ""
		for _, cap := range request.RequiredCapabilities {
			if !slices.Contains(t.Capabilities, cap) || !slices.Contains(runtime.Capabilities, cap) {
				missing = cap
				break
			}
		}
		if missing != "" {
			reject("required capability unavailable: " + missing)
			continue
		}
		args, mechanism, err := permissionArgs(runtime, request.Unattended)
		if err != nil {
			reject(err.Error())
			continue
		}
		effort := t.Effort
		if effort == "" {
			effort = "balanced"
			if workload == "deep-analysis" || workload == "independent-review" {
				effort = "high"
			}
		}
		switch t.Agent {
		case "claude":
			if effort == "balanced" {
				effort = "medium"
			}
			args = append(args, "--model", t.Model, "--effort", effort)
		case "codex":
			if effort == "balanced" {
				effort = "medium"
			}
			args = append(args, "--model", t.Model, "-c", "model_reasoning_effort=\""+effort+"\"")
		default:
			reject("model and effort launch adapter not validated")
			continue
		}
		approvals, knowledgeErr := KnowledgeApprovals(runtime)
		if knowledgeErr != nil {
			reject("knowledge approval inspection failed: " + knowledgeErr.Error())
			continue
		}
		conflicts, knowledgeErr := knowledgeConflictsForProjectFiles(runtime, projectPaths[t.Agent], approvals)
		if knowledgeErr != nil {
			reject(knowledgeErr.Error())
			continue
		}
		if len(conflicts) > 0 {
			reject(strings.Join(conflicts, "; "))
			return result, fmt.Errorf("knowledge approval conflicts with native rules; resolve the scoped rules before routing: %s", strings.Join(conflicts, "; "))
		}
		knowledgeArgs, knowledgeErr := KnowledgeArgs(runtime, approvals)
		if knowledgeErr != nil {
			reject("knowledge approval launch adapter failed: " + knowledgeErr.Error())
			continue
		}
		args = append(args, knowledgeArgs...)
		result.KnowledgeApprovals = append([]KnowledgeApproval{}, approvals...)
		result.BillingVerified = true
		result.Billing = t.Billing
		result.TargetID = t.ID
		result.Agent = t.Agent
		result.Version = runtime.Version
		result.Model = t.Model
		result.Effort = effort
		result.Executable = runtime.Executable
		result.Home = runtime.Home
		result.HomeMode = runtime.HomeMode
		result.PermissionMechanism = mechanism
		result.LaunchArgs = args
		result.Eligible = true
		result.Reason = "highest-priority validated target for " + workload
		result.Capabilities = append([]string{}, t.Capabilities...)
		result.Constraints = append(result.Constraints, runtime.Constraints...)
		return result, nil
	}
	return result, fmt.Errorf("no eligible AI policy target for %s: %s", workload, strings.Join(result.Constraints, "; "))
}

func permissionArgs(r Runtime, unattended bool) ([]string, string, error) {
	switch r.Agent {
	case "claude":
		if r.Version == "2.1.283" && r.AutoReview {
			return []string{"--permission-mode", "auto"}, "native-auto-review", nil
		}
	case "codex":
		if r.Version == "0.157.1" && r.AutoReview {
			return []string{"--approve-for-me"}, "native-auto-review", nil
		}
	case "kimi":
		if unattended {
			return nil, "", fmt.Errorf("kimi risk-aware asking does not authorize unattended writes")
		}
		return nil, "", fmt.Errorf("kimi requires a verified host-mediated approval path")
	case "opencode":
		return nil, "", fmt.Errorf("OpenCode rules are not an automatic reviewer; host-mediated path required")
	}
	return nil, "", fmt.Errorf("automatic review unavailable or unsupported runtime version; never fall back to bypass")
}
