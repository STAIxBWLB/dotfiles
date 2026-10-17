package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/aitooling"
	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
	"github.com/spf13/cobra"
)

func newAISetupCmd() *cobra.Command {
	c := &cobra.Command{Use: "setup", Short: "Select agent CLIs, development add-ons, instructions and shared skills", Args: cobra.NoArgs, RunE: runAISetup}
	c.Flags().String("agents", "", "Selected agent IDs, comma separated (empty selects none)")
	c.Flags().String("tools", "", "Selected add-on IDs: ripwire,ocr,gsd,claude-mem,ponytail")
	c.Flags().String("skills", "", "Selected Maru skill names or IDs, comma separated")
	c.Flags().Bool("non-interactive", false, "Require explicit or saved selections without prompting")
	return c
}

func runAISetup(cmd *cobra.Command, _ []string) error {
	state, err := loadStateForCmd(cmd)
	if err != nil {
		return err
	}
	selection, err := effectiveToolingForCmd(cmd)
	if err != nil {
		return err
	}
	yes, _ := cmd.Flags().GetBool("yes")
	noninteractive, _ := cmd.Flags().GetBool("non-interactive")
	unattended := yes || noninteractive
	if selection == nil {
		if unattended && !cmd.Flags().Changed("agents") {
			return fmt.Errorf("first noninteractive setup requires --agents; use --agents= for an empty selection")
		}
		selection = &config.AIToolingConfig{Agents: []string{}}
		if !unattended {
			for _, e := range aitooling.Catalog() {
				if e.Kind == "agent" {
					if _, err := exec.LookPath(e.Binary); err == nil {
						selection.Agents = append(selection.Agents, e.ID)
					}
				}
			}
		}
	}
	for _, field := range []string{"agents", "tools", "skills"} {
		var values *[]string
		switch field {
		case "agents":
			values = &selection.Agents
		case "tools":
			values = &selection.Tools
		case "skills":
			values = &selection.Skills
		}
		if cmd.Flags().Changed(field) {
			raw, _ := cmd.Flags().GetString(field)
			*values = parseAgentToolIDs(raw)
			if field == "skills" {
				*values = []string{}
				for _, part := range strings.Split(raw, ",") {
					if name := strings.TrimSpace(part); name != "" {
						*values = append(*values, name)
					}
				}
			}
			continue
		}
		if unattended {
			continue
		}
		var options []string
		if field == "skills" {
			options, err = maruSkillChoices(cmd)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "Shared skill selection unavailable: %v; preserving current choices.\n", err)
				continue
			}
		} else {
			kind := "tool"
			if field == "agents" {
				kind = "agent"
			}
			for _, e := range aitooling.Catalog() {
				if e.Kind == kind {
					options = append(options, e.ID)
				}
			}
		}
		if len(options) == 0 {
			continue
		}
		*values, err = ui.MultiSelect("Select "+field, options, *values, false)
		if err != nil {
			return err
		}
	}
	selectedIDs := map[string]bool{}
	for _, id := range append(append([]string{}, selection.Agents...), selection.Tools...) {
		selectedIDs[id] = true
	}
	if selectedIDs["gsd"] {
		selectedIDs["gsd-pi"] = true
	}
	for id := range selection.Pins {
		if !selectedIDs[id] {
			delete(selection.Pins, id)
		}
	}
	if err := config.ValidateAITooling(selection); err != nil {
		return err
	}
	if err := aitooling.ValidateSelection(*selection); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Agents: %s\nAdd-ons: %s\nShared skills: %s\n", strings.Join(selection.Agents, ", "), strings.Join(selection.Tools, ", "), strings.Join(selection.Skills, ", "))
	dry, _ := cmd.Flags().GetBool("dry-run")
	if !unattended && !dry {
		ok, err := ui.ConfirmBool("Apply this selection?", false, false)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
	}
	if !dry {
		state.Modules.AI.Enabled = true
		state.Modules.AI.AgentsSSOT = true
		state.Modules.AI.Tooling = selection
		if err := saveStateForCmd(cmd, state); err != nil {
			return err
		}
	}
	return runToolingSelection(cmd, *selection, aitooling.Ensure)
}

func maruSkillChoices(cmd *cobra.Command) ([]string, error) {
	ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
	defer cancel()
	proc := exec.CommandContext(ctx, "maru", "skills", "list", "--json")
	// Explicit homes must not enumerate another profile's registry.
	home, _ := cmd.Flags().GetString("home")
	if home != "" {
		return nil, fmt.Errorf("interactive registry discovery is unavailable with --home; provide --skills explicitly")
	}
	data, err := proc.Output()
	if err != nil {
		return nil, err
	}
	var records []struct {
		Name        string `json:"name"`
		ID          string `json:"id"`
		Installable bool   `json:"installable"`
	}
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, err
	}
	names := map[string]int{}
	for _, r := range records {
		if r.Installable {
			names[r.Name]++
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, r := range records {
		if !r.Installable {
			continue
		}
		value := r.Name
		if value == "" || names[value] > 1 {
			value = r.ID
		}
		if value != "" && !seen[value] {
			out = append(out, value)
			seen[value] = true
		}
	}
	return out, nil
}
