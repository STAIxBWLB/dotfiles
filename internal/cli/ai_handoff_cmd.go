package cli

import (
	"encoding/json"
	"fmt"

	"github.com/STAIxBWLB/dotfiles/internal/aihandoff"
	"github.com/spf13/cobra"
)

func newAIHandoffCmd() *cobra.Command {
	c := &cobra.Command{Use: "handoff", Short: "Share explicit local development notes across selected agents"}
	show := &cobra.Command{Use: "show", Short: "Read repository handoffs with freshness checks (results remain claims)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		project, _ := cmd.Flags().GetString("project")
		report, err := aihandoff.Show(cmd.Context(), homeFromCmd(cmd), project)
		if err != nil {
			return err
		}
		asJSON, _ := cmd.Flags().GetBool("json")
		if asJSON {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
		}
		fmt.Fprintln(cmd.OutOrStdout(), report.Verification)
		for _, entry := range report.Entries {
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s [%s] %s (producer claim: %s)\n%s\n", entry.Record.Time.Format("2006-01-02T15:04:05Z"), entry.Record.Agent, entry.Record.Kind, entry.State, entry.Record.ProducerResult, entry.Record.Summary)
			for _, reason := range entry.Reasons {
				fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", reason)
			}
		}
		if len(report.Entries) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "No handoffs recorded.")
		}
		return nil
	}}
	show.Flags().String("project", ".", "Git repository or worktree directory")
	show.Flags().Bool("json", false, "Print JSON")
	record := &cobra.Command{Use: "record", Short: "Record a selected agent's explicit summary and artifact fingerprints", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		selection, err := effectiveToolingForCmd(cmd)
		if err != nil {
			return err
		}
		if selection == nil {
			return fmt.Errorf("configure selected agents with dot ai setup before recording handoffs")
		}
		project, _ := cmd.Flags().GetString("project")
		agent, _ := cmd.Flags().GetString("agent")
		kind, _ := cmd.Flags().GetString("kind")
		summaryPath, _ := cmd.Flags().GetString("summary-file")
		artifacts, _ := cmd.Flags().GetStringArray("artifact")
		result, _ := cmd.Flags().GetString("result")
		dry, _ := cmd.Flags().GetBool("dry-run")
		if summaryPath == "" {
			return fmt.Errorf("--summary-file is required; provide a curated note, not raw transcripts or credentials")
		}
		summary, err := aihandoff.ReadSummaryFile(cmd.Context(), summaryPath)
		if err != nil {
			return err
		}
		note, err := aihandoff.RecordNote(cmd.Context(), aihandoff.Options{Home: homeFromCmd(cmd), Project: project, Agent: agent, Kind: kind, Summary: string(summary), Artifacts: artifacts, Result: result, SelectedAgents: selection.Agents, DryRun: dry})
		if err != nil {
			return err
		}
		if dry {
			fmt.Fprintf(cmd.OutOrStdout(), "dry-run: would record %s handoff for %s at %s\n", note.Kind, note.Agent, note.HEAD)
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "Recorded %s handoff for %s at %s (result is a producer claim).\n", note.Kind, note.Agent, note.HEAD)
		}
		return nil
	}}
	record.Flags().String("project", ".", "Git repository or worktree directory")
	record.Flags().String("agent", "", "Selected agent ID producing the note")
	record.Flags().String("kind", "progress", "Note kind: plan, progress, review, validation, learning")
	record.Flags().String("summary-file", "", "UTF-8 curated summary file, at most 32 KiB")
	record.Flags().StringArray("artifact", nil, "Repository-relative artifact to fingerprint (repeatable, at most 16, total hash budget 32 MiB)")
	record.Flags().String("result", "unverified", "Producer claim: unverified, passed, failed")
	c.AddCommand(show, record)
	return c
}
