package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/aisettings"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

// The four dot ai archive commands: snapshot backup/restore against a
// backup root, and tarball export/import. Each translates its flags into
// an aisettings Options struct and renders the Summary that comes back.

func newAIBackupCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "backup",
		Short: "Create a versioned AI settings snapshot",
		Args:  cobra.NoArgs,
		RunE:  runAIBackup,
	}
	c.Flags().String("to", "", "Backup root (overrides configured BackupRoot)")
	c.Flags().String("tag", "", "Human-friendly label stored in meta.yaml")
	c.Flags().Bool("include-auth", false, "Include auth/local-secret files")
	return c
}

func runAIBackup(cmd *cobra.Command, _ []string) error {
	includeAuth, _ := cmd.Flags().GetBool("include-auth")
	tag, _ := cmd.Flags().GetString("tag")
	eng, err := newAIEngine(cmd)
	if err != nil {
		return err
	}
	sum, err := eng.Backup(aisettings.BackupOptions{Tag: tag, IncludeAuth: includeAuth})
	if err != nil {
		return err
	}
	auditAIEventBestEffort(cmd, "ai.backup", aiSummaryPayload(sum))
	printAISummary(printerFrom(cmd), "AI Backup", sum)
	return nil
}

func newAIRestoreCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "restore",
		Short: "Restore AI settings from a versioned snapshot",
		Args:  cobra.NoArgs,
		RunE:  runAIRestore,
	}
	c.Flags().String("from", "", "Backup root (overrides configured BackupRoot)")
	c.Flags().String("host", "", "Source hostname to restore from (default: this host)")
	c.Flags().String("version", "", `Specific version to restore, or "latest" (default: latest)`)
	c.Flags().Bool("include-auth", false, "Restore auth/local-secret files from the snapshot")
	c.Flags().Bool("reapply-agents", false, "After restore, reapply the agents SSOT to tool targets")
	return c
}

func runAIRestore(cmd *cobra.Command, _ []string) error {
	yes, _ := cmd.Flags().GetBool("yes")
	includeAuth, _ := cmd.Flags().GetBool("include-auth")
	version, _ := cmd.Flags().GetString("version")
	reapplyAgents, _ := cmd.Flags().GetBool("reapply-agents")
	eng, err := newAIEngine(cmd)
	if err != nil {
		return err
	}
	host, err := hostOverride(cmd, eng.Hostname)
	if err != nil {
		return err
	}
	eng.Hostname = host
	p := printerFrom(cmd)
	if version == "" || version == "latest" {
		v, err := eng.ResolveLatest()
		if err != nil {
			return err
		}
		version = v
	}
	if !yes {
		p.Line("About to restore AI settings from snapshot %s.", version)
		ok, err := ui.ConfirmBool("Continue?", false, false)
		if err != nil {
			return err
		}
		if !ok {
			p.Line("aborted")
			return nil
		}
	}
	sum, err := eng.Restore(aisettings.RestoreOptions{Version: version, IncludeAuth: includeAuth})
	if err != nil {
		return err
	}
	auditAIEventBestEffort(cmd, "ai.restore", aiSummaryPayload(sum))
	printAISummary(p, "AI Restore", sum)
	if sum.PreBackupPath != "" {
		p.Line("  %s  %s", ui.StyleKey.Render("Previous:"), ui.StyleHint.Render(sum.PreBackupPath))
	}
	if reapplyAgents {
		mgr := newAgentsManagerFromCmd(cmd)
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		result, err := mgr.Apply(aisettings.ApplyOptions{Tools: mgr.DefaultApplyTools(), DryRun: dryRun})
		if err != nil {
			return err
		}
		if err := auditAIEvent(cmd, "ai.agents.apply", agentsApplyPayload(result)); err != nil {
			return err
		}
		p.Section("Agents SSOT Reapply")
		printAgentsApplyResult(p, result)
	}
	return nil
}

func newAIExportCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "export <file.tar.gz>",
		Short: "Export AI settings to a portable tar.gz archive",
		Args:  cobra.ExactArgs(1),
		RunE:  runAIExport,
	}
	c.Flags().String("tag", "", "Human-friendly label stored in meta.yaml")
	c.Flags().Bool("include-auth", false, "Include auth/local-secret files")
	return c
}

func runAIExport(cmd *cobra.Command, args []string) error {
	includeAuth, _ := cmd.Flags().GetBool("include-auth")
	tag, _ := cmd.Flags().GetString("tag")
	eng, err := newAIEngine(cmd)
	if err != nil {
		return err
	}
	sum, err := eng.Export(args[0], aisettings.BackupOptions{Tag: tag, IncludeAuth: includeAuth})
	if err != nil {
		return err
	}
	auditAIEventBestEffort(cmd, "ai.export", aiSummaryPayload(sum))
	printAISummary(printerFrom(cmd), "AI Export", sum)
	return nil
}

func newAIImportCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "import <file.tar.gz>",
		Short: "Import AI settings from a portable tar.gz archive",
		Args:  cobra.ExactArgs(1),
		RunE:  runAIImport,
	}
	c.Flags().Bool("include-auth", false, "Import auth/local-secret files from the archive")
	return c
}

func runAIImport(cmd *cobra.Command, args []string) error {
	yes, _ := cmd.Flags().GetBool("yes")
	includeAuth, _ := cmd.Flags().GetBool("include-auth")
	eng, err := newAIEngine(cmd)
	if err != nil {
		return err
	}
	p := printerFrom(cmd)
	if !yes {
		p.Line("About to import AI settings from %s.", args[0])
		ok, err := ui.ConfirmBool("Continue?", false, false)
		if err != nil {
			return err
		}
		if !ok {
			p.Line("aborted")
			return nil
		}
	}
	sum, err := eng.Import(args[0], aisettings.RestoreOptions{IncludeAuth: includeAuth})
	if err != nil {
		return err
	}
	auditAIEventBestEffort(cmd, "ai.import", aiSummaryPayload(sum))
	printAISummary(p, "AI Import", sum)
	if sum.PreBackupPath != "" {
		p.Line("  %s  %s", ui.StyleKey.Render("Previous:"), ui.StyleHint.Render(sum.PreBackupPath))
	}
	return nil
}

func printAISummary(p *Printer, title string, sum *aisettings.Summary) {
	p.Header(title + " Summary")
	if sum.Version != "" {
		p.KV("Version", sum.Version)
	}
	if sum.Path != "" {
		p.KV("Path", sum.Path)
	}
	p.Section("Entries")
	for _, entry := range sum.Entries {
		if entry.Skipped > 0 {
			p.Bullet(ui.StyleHint.Render(ui.MarkPartial), fmt.Sprintf("%-8s kept live copy (move ~/%s aside first to restore from snapshot)",
				ui.StyleValue.Render(entry.Tool), entry.Path))
			continue
		}
		marker := ui.StyleHint.Render(ui.MarkPartial)
		if entry.Copied > 0 {
			marker = ui.StyleSuccess.Render(ui.MarkPresent)
		}
		if entry.Auth {
			marker = ui.StyleWarning.Render(ui.MarkWarn)
		}
		p.Bullet(marker, fmt.Sprintf("%-8s paths:%d copied / %d missing  files:%d  bytes:%d  %s",
			ui.StyleValue.Render(entry.Tool), entry.Copied, entry.Missing, entry.Files, entry.Bytes, entry.Path))
	}
	p.Blank()
	p.Line("  Total: %d file(s), %d byte(s)", sum.Files, sum.Bytes)
}

// auditAIEventBestEffort appends to the audit log but never fails the
// command: the operation already succeeded, and an unwritable audit log
// must not turn that success into an error.
func auditAIEventBestEffort(cmd *cobra.Command, typ string, payload map[string]any) {
	if err := auditAIEvent(cmd, typ, payload); err != nil {
		printerFrom(cmd).Warn("  audit log write failed: %v", err)
	}
}
