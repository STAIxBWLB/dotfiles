package cli

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/aischedule"
	"github.com/STAIxBWLB/dotfiles/internal/resourceguard"
	"github.com/spf13/cobra"
)

var newAIScheduleManager = aischedule.New

func newAIUpdateScheduleCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "schedule", Short: "Manage opt-in Sunday 04:00 stable maintenance (macOS)"}
	for _, action := range []string{"enable", "disable", "status"} {
		action := action
		c := &cobra.Command{Use: action, Short: action + " the weekly AI update LaunchAgent", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return runAIUpdateSchedule(cmd, action) }}
		c.Flags().Bool("json", false, "Emit scheduler status as JSON")
		c.Flags().Duration("wait", 6*time.Minute, "Maximum time to wait for the maintenance slot and host recovery when enabling (at most 10m)")
		cmd.AddCommand(c)
	}
	return cmd
}
func runAIUpdateSchedule(cmd *cobra.Command, action string) error {
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	home := homeFromCmd(cmd)
	m := newAIScheduleManager(home, dryRun)
	if action != "status" {
		if over, _ := cmd.Flags().GetString("home"); over != "" {
			return fmt.Errorf("--home is not supported for schedule changes; launchd controls the current user's domain")
		}
	}
	var status aischedule.Status
	var err error
	switch action {
	case "enable":
		if m.GOOS != "darwin" {
			return fmt.Errorf("AI update scheduling is macOS-only; manual updates remain available")
		}
		state, e := loadStateForCmd(cmd)
		if e != nil {
			return e
		}
		if state.Modules.AI.Tooling == nil || len(state.Modules.AI.Tooling.Agents) == 0 {
			return fmt.Errorf("configure selected agents with dot ai setup before enabling maintenance")
		}
		// Registration does not bypass admission: take the maintenance slot on a
		// healthy host first, then release it before launchd can perform its
		// startup check.
		if !dryRun {
			wait, _ := cmd.Flags().GetDuration("wait")
			if wait <= 0 || wait > 10*time.Minute {
				return fmt.Errorf("--wait must be greater than zero and no more than 10m")
			}
			release, e := resourceguard.WaitAcquire(cmd.Context(), resourceguard.Options{HomeDir: home, Purpose: "enable weekly AI maintenance", ScopeKey: "tooling"}, wait)
			if e != nil {
				return e
			}
			release()
		}
		wasEnabled := state.Modules.AI.Tooling.Updates.Enabled
		if !dryRun {
			// Save intent before launchd's immediate startup invocation so it
			// can respect disabled selections without a registration race.
			state.Modules.AI.Tooling.Updates.Enabled = true
			if err = saveStateForCmd(cmd, state); err != nil {
				return err
			}
		}
		status, err = m.Enable(cmd.Context())
		if err != nil && !dryRun {
			state.Modules.AI.Tooling.Updates.Enabled = wasEnabled
			if rollbackErr := saveStateForCmd(cmd, state); rollbackErr != nil {
				return fmt.Errorf("%w; restoring schedule intent: %v", err, rollbackErr)
			}
		}
	case "disable":
		status, err = m.Disable(cmd.Context())
		if err == nil && !dryRun {
			state, e := loadStateForCmd(cmd)
			if e != nil {
				return e
			}
			if state.Modules.AI.Tooling != nil {
				state.Modules.AI.Tooling.Updates.Enabled = false
				err = saveStateForCmd(cmd, state)
			}
		}
	default:
		status, err = m.Status(cmd.Context())
	}
	if err != nil {
		return err
	}
	asJSON, _ := cmd.Flags().GetBool("json")
	if asJSON {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(status)
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "AI maintenance: enabled=%t installed=%t loaded=%t supported=%t\n%s\n%s\nScheduled attempts remaining: %d/%d; budget exhausted=%t\n", status.Enabled, status.Installed, status.Loaded, status.Supported, status.Schedule, status.Plist, status.AttemptsRemaining, aischedule.WeeklyAttemptLimit, status.BudgetExhausted)
	if err == nil && status.NextEligible != nil {
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Next eligible: %s\n", status.NextEligible.Format(time.RFC3339))
	}
	return err
}
