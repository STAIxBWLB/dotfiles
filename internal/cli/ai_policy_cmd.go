package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/STAIxBWLB/dotfiles/internal/aipolicy"
	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/spf13/cobra"
)

type aiPolicyReport struct {
	SchemaVersion int                         `json:"schema_version"`
	Operation     string                      `json:"operation"`
	DryRun        bool                        `json:"dry_run"`
	Policy        *config.AIPolicyConfig      `json:"policy"`
	Inventory     []aipolicy.Runtime          `json:"inventory"`
	Changes       []aipolicy.PreferenceChange `json:"changes"`
	Constraints   []string                    `json:"constraints"`
}

func effectivePolicyForCmd(cmd *cobra.Command) (*config.AIPolicyConfig, error) {
	path, _ := cmd.Flags().GetString("config")
	if path != "" {
		cfg, err := config.Load("", path, nil)
		if err != nil {
			return nil, err
		}
		return cfg.Modules.AI.Policy.Clone(), config.ValidateAIPolicy(cfg.Modules.AI.Policy)
	}
	state, err := loadStateForCmd(cmd)
	if err != nil {
		return nil, err
	}
	return state.Modules.AI.Policy.Clone(), config.ValidateAIPolicy(state.Modules.AI.Policy)
}

func policyPreferences(cmd *cobra.Command) aipolicy.Preferences {
	over, _ := cmd.Flags().GetString("home")
	return aipolicy.Preferences{Home: homeFromCmd(cmd), Explicit: over != ""}
}

func inspectPolicyForCmd(cmd *cobra.Command) ([]aipolicy.Runtime, error) {
	over, _ := cmd.Flags().GetString("home")
	return aipolicy.Inspect(cmd.Context(), homeFromCmd(cmd), over != "")
}

func newAIPolicyCmd() *cobra.Command {
	c := &cobra.Command{Use: "policy", Short: "Inspect, resolve and reconcile adaptive AI operating policies"}
	for _, operation := range []string{"inspect", "diff", "apply", "status", "rollback"} {
		op := operation
		child := &cobra.Command{Use: op, Short: map[string]string{
			"inspect":  "Inspect local agent capabilities and portable policy without changing settings",
			"diff":     "Preview selected policy overlays without changing native settings",
			"apply":    "Apply owned native policy overlays and optionally persist the desired policy",
			"status":   "Compare owned overlays with selected policy and report drift",
			"rollback": "Remove unchanged owned policy overlays; preserve native defaults",
		}[op], Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return runAIPolicy(cmd, op) }}
		child.Flags().Bool("json", false, "Emit a versioned JSON report")
		if op == "apply" {
			child.Flags().Bool("persist", false, "Save the supplied policy into dotfiles user state after applying overlays")
		}
		c.AddCommand(child)
	}
	resolve := &cobra.Command{Use: "resolve", Short: "Select a validated configuration for one task without launching it", Args: cobra.NoArgs, RunE: runAIPolicyResolve}
	addPolicyRequestFlags(resolve)
	resolve.Flags().Bool("json", false, "Emit the versioned app integration contract")
	c.AddCommand(resolve)
	return c
}

func addPolicyRequestFlags(c *cobra.Command) {
	c.Flags().String("task", "", "Task intent used for automatic workload classification")
	c.Flags().String("workload", "auto", "auto, routine, implementation, deep-analysis, independent-review, documents-teaching, visual-production")
	c.Flags().String("agent", "", "Explicit agent selection (never silently substituted)")
	c.Flags().String("project", "", "Task working directory (defaults to current directory)")
	c.Flags().String("origin", "cli", "Calling surface, such as cli, maru or orca")
	c.Flags().Bool("unattended", false, "Require a supported unattended approval path")
	c.Flags().StringSlice("require", nil, "Required validated capabilities")
}

func policyRequest(cmd *cobra.Command) (aipolicy.Request, error) {
	r := aipolicy.Request{}
	r.Task, _ = cmd.Flags().GetString("task")
	r.Workload, _ = cmd.Flags().GetString("workload")
	r.Agent, _ = cmd.Flags().GetString("agent")
	if r.Agent == "auto" {
		r.Agent = ""
	}
	r.CWD, _ = cmd.Flags().GetString("project")
	if r.CWD == "" {
		var err error
		r.CWD, err = os.Getwd()
		if err != nil {
			return r, err
		}
	}
	abs, err := filepath.Abs(r.CWD)
	if err != nil {
		return r, err
	}
	r.CWD = abs
	info, err := os.Stat(r.CWD)
	if err != nil {
		return r, fmt.Errorf("task directory: %w", err)
	}
	if !info.IsDir() {
		return r, fmt.Errorf("task directory is not a directory")
	}
	r.Origin, _ = cmd.Flags().GetString("origin")
	r.Unattended, _ = cmd.Flags().GetBool("unattended")
	r.RequiredCapabilities, _ = cmd.Flags().GetStringSlice("require")
	return r, nil
}

func writePolicyJSON(cmd *cobra.Command, value any) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(value)
}

func runAIPolicy(cmd *cobra.Command, op string) error {
	dry, _ := cmd.Flags().GetBool("dry-run")
	prefs := policyPreferences(cmd)
	report := aiPolicyReport{SchemaVersion: 1, Operation: op, DryRun: dry, Inventory: []aipolicy.Runtime{}, Changes: []aipolicy.PreferenceChange{}, Constraints: []string{"Native base settings are not owned; policy overlays govern controlled launches only."}}
	if op == "rollback" {
		changes, err := prefs.Rollback(dry)
		if err != nil {
			return err
		}
		report.Changes = changes
	} else {
		policy, err := effectivePolicyForCmd(cmd)
		if err != nil {
			return err
		}
		report.Policy = policy
		if dry {
			report.Constraints = append(report.Constraints, "Strict dry run skips native probes and writes; use policy diff for an inspected overlay preview.")
		} else {
			inventory, err := inspectPolicyForCmd(cmd)
			if err != nil {
				return err
			}
			report.Inventory = inventory
			if op == "inspect" && policy == nil {
				report.Policy = aipolicy.DefaultPolicy(inventory)
			}
			switch op {
			case "diff":
				report.Changes, err = prefs.Plan(policy, inventory)
			case "status":
				report.Changes, err = prefs.Status(policy, inventory)
			case "apply":
				if policy == nil || !policy.Enabled {
					return fmt.Errorf("configure an enabled modules.ai.policy before applying")
				}
				report.Changes, err = prefs.Apply(policy, inventory, false)
				if err == nil {
					usable := false
					for _, change := range report.Changes {
						usable = usable || change.Status == "applied" || change.Status == "current"
					}
					if !usable {
						return fmt.Errorf("no usable policy overlays: inspect and validate an available automatic-review target")
					}
					persist, _ := cmd.Flags().GetBool("persist")
					if persist {
						state, loadErr := loadStateForCmd(cmd)
						if loadErr != nil {
							return loadErr
						}
						state.Modules.AI.Policy = policy.Clone()
						if saveErr := saveStateForCmd(cmd, state); saveErr != nil {
							return fmt.Errorf("overlays applied, saving desired policy failed: %w", saveErr)
						}
					}
					auditAIEventBestEffort(cmd, "ai.policy.apply", map[string]any{"revision": policy.Revision, "targets": len(report.Changes)})
				}
			}
			if err != nil {
				return err
			}
		}
	}
	if op == "rollback" && !dry {
		auditAIEventBestEffort(cmd, "ai.policy.rollback", map[string]any{"targets": len(report.Changes)})
	}
	jsonMode, _ := cmd.Flags().GetBool("json")
	if jsonMode {
		return writePolicyJSON(cmd, report)
	}
	if report.Policy != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Policy %s: enabled=%t, targets=%d\n", report.Policy.Revision, report.Policy.Enabled, len(report.Policy.Targets))
	}
	for _, runtime := range report.Inventory {
		fmt.Fprintf(cmd.OutOrStdout(), "%s: version=%s available=%t native-review=%t\n", runtime.Agent, runtime.Version, runtime.Available, runtime.AutoReview)
	}
	for _, change := range report.Changes {
		fmt.Fprintf(cmd.OutOrStdout(), "%s: %s (%s)\n", change.TargetID, change.Status, change.Path)
	}
	for _, constraint := range report.Constraints {
		fmt.Fprintln(cmd.OutOrStdout(), constraint)
	}
	return nil
}

func runAIPolicyResolve(cmd *cobra.Command, _ []string) error {
	policy, err := effectivePolicyForCmd(cmd)
	if err != nil {
		return err
	}
	r, err := policyRequest(cmd)
	if err != nil {
		return err
	}
	dry, _ := cmd.Flags().GetBool("dry-run")
	if dry {
		return fmt.Errorf("resolve requires read-only runtime probes; remove --dry-run or use policy inspect --dry-run")
	}
	inventory, err := inspectPolicyForCmd(cmd)
	if err != nil {
		return err
	}
	resolved, err := aipolicy.Resolve(policy, r, inventory)
	if err != nil {
		return err
	}
	jsonMode, _ := cmd.Flags().GetBool("json")
	if jsonMode {
		return writePolicyJSON(cmd, resolved)
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s: %s / %s / %s (%s)\n%s\n%s\n", resolved.Workload, resolved.Agent, resolved.Model, resolved.Effort, resolved.PermissionMechanism, resolved.Reason, strings.Join(resolved.Constraints, "\n"))
	return err
}
