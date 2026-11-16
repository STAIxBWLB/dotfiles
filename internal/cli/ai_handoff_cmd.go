package cli

import (
	"encoding/json"
	"fmt"

	"github.com/STAIxBWLB/dotfiles/internal/aihandoff"
	"github.com/spf13/cobra"
)

func newAIHandoffCmd() *cobra.Command {
	c := &cobra.Command{Use: "handoff", Short: "Share local development notes across selected agents"}
	show := &cobra.Command{Use: "show", Short: "Read repository handoffs with freshness checks", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		project, _ := cmd.Flags().GetString("project")
		report, err := aihandoff.Show(cmd.Context(), homeFromCmd(cmd), project)
		if err != nil {
			return err
		}
		asJSON, _ := cmd.Flags().GetBool("json")
		if asJSON {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
		}
		p := printerFrom(cmd)
		p.Line("%s", report.Verification)
		for _, entry := range report.Entries {
			p.Line("%s %s [%s] %s (producer claim: %s)\n%s", entry.Record.Time.Format("2006-01-02T15:04:05Z"), entry.Record.Agent, entry.Record.Kind, entry.State, entry.Record.ProducerResult, entry.Record.Summary)
			for _, reason := range entry.Reasons {
				p.Line("  %s", reason)
			}
		}
		if len(report.Entries) == 0 {
			p.Line("No handoffs recorded.")
		}
		return nil
	}}
	show.Flags().String("project", ".", "git repository or worktree directory")
	show.Flags().Bool("json", false, "print JSON")
	record := &cobra.Command{Use: "record", Short: "Record an agent's summary and artifact fingerprints", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
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
			return usageError(cmd, fmt.Errorf("--summary-file is required; provide a curated note, not raw transcripts or credentials"))
		}
		summary, err := aihandoff.ReadSummaryFile(cmd.Context(), summaryPath)
		if err != nil {
			return err
		}
		note, err := aihandoff.RecordNote(cmd.Context(), aihandoff.Options{Home: homeFromCmd(cmd), Project: project, Agent: agent, Kind: kind, Summary: string(summary), Artifacts: artifacts, Result: result, SelectedAgents: selection.Agents, DryRun: dry})
		if err != nil {
			return err
		}
		p := printerFrom(cmd)
		if dry {
			p.DryRun("record %s handoff for %s at %s", note.Kind, note.Agent, note.HEAD)
		} else {
			p.Line("Recorded %s handoff for %s at %s (result is a producer claim).", note.Kind, note.Agent, note.HEAD)
		}
		return nil
	}}
	record.Flags().String("project", ".", "git repository or worktree directory")
	record.Flags().String("agent", "", "selected agent ID producing the note")
	record.Flags().String("kind", "progress", "note kind: plan, progress, review, validation, learning")
	record.Flags().String("summary-file", "", "curated UTF-8 summary file, at most 32 KiB")
	record.Flags().StringArray("artifact", nil, "repository-relative artifact to fingerprint (repeatable, at most 16, total hash budget 32 MiB)")
	record.Flags().String("result", "unverified", "producer claim: unverified, passed, failed")
	c.AddCommand(show, record)
	return c
}
