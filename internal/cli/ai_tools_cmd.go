package cli

import (
	"encoding/json"
	"fmt"
	"github.com/STAIxBWLB/dotfiles/internal/aisettings"
	"github.com/STAIxBWLB/dotfiles/internal/aitooling"
	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/spf13/cobra"
)

func newAIToolsCmd() *cobra.Command {
	c := &cobra.Command{Use: "tools", Short: "Inspect or reconcile selected development tooling"}
	list := &cobra.Command{Use: "list", Short: "List selectable agents and four managed add-ons", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		asJSON, _ := cmd.Flags().GetBool("json")
		if asJSON {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
				Items []aitooling.Entry `json:"items"`
			}{Items: aitooling.Catalog()})
		}
		for _, e := range aitooling.Catalog() {
			fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", e.Kind, e.ID, e.Name)
		}
		return nil
	}}
	list.Flags().Bool("json", false, "Print JSON")
	c.AddCommand(list)
	for _, spec := range []struct {
		name string
		op   aitooling.Operation
	}{{"status", aitooling.Inspect}, {"apply", aitooling.Ensure}} {
		op := spec.op
		child := &cobra.Command{Use: spec.name, Short: "Inspect or apply saved tooling selections", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			selection, err := effectiveToolingForCmd(cmd)
			if err != nil {
				return err
			}
			if selection == nil {
				return fmt.Errorf("no tooling selection configured; run dot ai setup")
			}
			return runToolingSelection(cmd, *selection, op)
		}}
		child.Flags().Bool("json", false, "Print JSON")
		c.AddCommand(child)
	}
	return c
}

func runToolingSelection(cmd *cobra.Command, selection config.AIToolingConfig, op aitooling.Operation) error {
	dry, _ := cmd.Flags().GetBool("dry-run")
	over, _ := cmd.Flags().GetString("home")
	engine := aitooling.New(aitooling.Options{HomeDir: homeFromCmd(cmd), ExplicitHome: over != "", DryRun: dry, Out: cmd.ErrOrStderr()})
	if err := aitooling.ValidateSelection(selection); err != nil {
		return err
	}
	report, runErr := engine.Run(cmd.Context(), selection, op)
	if runErr != nil && len(report.Items) == 0 {
		return runErr
	}
	if op == aitooling.Ensure && cmd.Context().Err() == nil && !toolingAdmissionBlocked(report) && len(selection.Agents) > 0 {
		mgr := newAgentsManagerFromCmd(cmd)
		mgr.SelectedTools = append([]string{}, selection.Agents...)
		if _, err := mgr.Init(aisettings.InitOptions{}); err != nil {
			return err
		}
		if _, err := mgr.EnsureContinuityPolicy(); err != nil {
			return err
		}
		if err := ensureMemoryBlock(mgr, dry); err != nil {
			return err
		}
		if !dry {
			if tools := registeredInstructionTools(mgr, selection.Agents); len(tools) > 0 {
				if _, err := mgr.Apply(aisettings.ApplyOptions{Tools: tools}); err != nil {
					return err
				}
			}
		}
	}
	if len(selection.Agents) > 0 {
		mgr := newAgentsManagerFromCmd(cmd)
		needed, err := mgr.ContinuityPolicyNeeded()
		if err != nil {
			return err
		}
		if needed {
			report.Items = append(report.Items, aitooling.ItemResult{ID: "shared-context", Kind: "instructions", Status: "policy-missing", Detail: "run dot ai tools apply to install shared-context guidance"})
		}
		statuses, err := mgr.Status()
		if err != nil {
			return err
		}
		selected := map[string]bool{}
		for _, id := range selection.Agents {
			selected[id] = true
		}
		for _, st := range statuses {
			if selected[st.Tool.ID] {
				report.Items = append(report.Items, aitooling.ItemResult{ID: st.Tool.ID, Kind: "instructions", Status: st.Drift, Detail: st.TargetPath + "; runtime discovery requires agent verification"})
			}
		}
	}
	asJSON, _ := cmd.Flags().GetBool("json")
	if asJSON {
		if err := json.NewEncoder(cmd.OutOrStdout()).Encode(report); err != nil {
			return err
		}
	} else {
		for _, item := range report.Items {
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s: %s %s\n", item.Kind, item.ID, item.Status, item.Detail)
		}
	}
	if runErr != nil {
		return runErr
	}
	if report.Failed > 0 || report.Deferred > 0 {
		return fmt.Errorf("tooling incomplete: %d failed, %d deferred", report.Failed, report.Deferred)
	}
	return nil
}

func toolingAdmissionBlocked(report aitooling.Report) bool {
	for _, item := range report.Items {
		if item.Kind == "resource" || item.ID == "admission" {
			return true
		}
	}
	return false
}

// registeredInstructionTools intersects selected agents with the instruction
// registry; the install catalog can lead the registry (e.g. gencode has no
// verified instruction target yet).
func registeredInstructionTools(mgr *aisettings.AgentsManager, agents []string) []string {
	var out []string
	for _, id := range agents {
		if _, ok := mgr.Tool(id); ok {
			out = append(out, id)
		}
	}
	return out
}
