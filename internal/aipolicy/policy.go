// Package aipolicy resolves validated portable preferences against local capabilities.
package aipolicy

import "github.com/STAIxBWLB/dotfiles/internal/config"

type Request struct {
	Task                 string   `json:"task"`
	Workload             string   `json:"workload"`
	Agent                string   `json:"agent"`
	CWD                  string   `json:"cwd"`
	Origin               string   `json:"origin"`
	Unattended           bool     `json:"unattended"`
	RequiredCapabilities []string `json:"required_capabilities"`
}
type Runtime struct {
	HomeMode             string            `json:"home_mode"`
	SubscriptionVerified bool              `json:"subscription_verified"`
	SubscriptionConflict bool              `json:"subscription_conflict"`
	Agent                string            `json:"agent"`
	Version              string            `json:"version"`
	Executable           string            `json:"executable"`
	Home                 string            `json:"home"`
	Available            bool              `json:"available"`
	AutoReview           bool              `json:"auto_review"`
	Capabilities         []string          `json:"capabilities"`
	Constraints          []string          `json:"constraints"`
	Preferences          map[string]string `json:"preferences,omitempty"`
}
type Resolution struct {
	HomeMode            string              `json:"home_mode"`
	KnowledgeApprovals  []KnowledgeApproval `json:"knowledge_approvals"`
	BillingVerified     bool                `json:"billing_verified"`
	Billing             string              `json:"billing"`
	SchemaVersion       int                 `json:"schema_version"`
	PolicyRevision      string              `json:"policy_revision"`
	TargetID            string              `json:"target_id"`
	Version             string              `json:"version"`
	Workload            string              `json:"workload"`
	Agent               string              `json:"agent"`
	Model               string              `json:"model"`
	Effort              string              `json:"effort"`
	Executable          string              `json:"executable"`
	Home                string              `json:"home"`
	PermissionIntent    string              `json:"permission_intent"`
	PermissionMechanism string              `json:"permission_mechanism"`
	LaunchArgs          []string            `json:"launch_args"`
	Eligible            bool                `json:"eligible"`
	Reason              string              `json:"reason"`
	Constraints         []string            `json:"constraints"`
	Capabilities        []string            `json:"capabilities"`
	Continuity          string              `json:"continuity"`
}

// DefaultPolicy is a reviewable inventory seed. Inspection cannot prove billing,
// account entitlement or model quality, so it never silently validates targets.
func DefaultPolicy(inventory []Runtime) *config.AIPolicyConfig {
	p := &config.AIPolicyConfig{Revision: "1", MaxSwitches: 2, Targets: []config.AIPolicyTarget{}}
	for _, r := range inventory {
		if r.Available {
			p.Targets = append(p.Targets, config.AIPolicyTarget{ID: r.Agent, Agent: r.Agent, Version: r.Version, Workloads: []string{"routine", "implementation", "deep-analysis", "independent-review", "documents-teaching", "visual-production"}, Capabilities: append([]string{}, r.Capabilities...)})
		}
	}
	return p
}
