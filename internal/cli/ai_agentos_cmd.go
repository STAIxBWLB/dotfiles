package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/aisettings"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

func newAISkillsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skills",
		Short: "Diagnose AI Markdown skills (read-only; the Maru app manages runtime symlinks)",
	}
	cmd.AddCommand(newAISkillsListCmd())
	cmd.AddCommand(newAISkillsValidateCmd())
	cmd.AddCommand(newAISkillsStatusCmd())
	cmd.AddCommand(newAISkillsPathCmd())
	return cmd
}

func newAISkillsListCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "list",
		Short: "List SKILL.md files and metadata status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			report, asJSON, _, err := scanSkillsFromCmd(cmd)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(cmd, report)
			}
			verbose, _ := cmd.Flags().GetBool("verbose")
			printSkillReport(printerFrom(cmd), report, false, verbose)
			return nil
		},
	}
	addSkillScanFlags(c)
	return c
}

func newAISkillsValidateCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "validate",
		Short: "Validate SKILL.md frontmatter and duplicate names",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			report, asJSON, strict, err := scanSkillsFromCmd(cmd)
			if err != nil {
				return err
			}
			errs := report.ValidationErrors(strict)
			if asJSON {
				if err := printJSON(cmd, report); err != nil {
					return err
				}
			} else {
				p := printerFrom(cmd)
				verbose, _ := cmd.Flags().GetBool("verbose")
				printSkillReport(p, report, strict, verbose)
				p.Summary(skillValidationSummary(report, strict))
			}
			if len(errs) > 0 {
				return fmt.Errorf("skill validation failed: %s", strings.Join(errs, "; "))
			}
			return nil
		},
	}
	addSkillScanFlags(c)
	return c
}

func addSkillScanFlags(c *cobra.Command) {
	c.Flags().String("tool", "", "Comma-separated default skill roots to scan (claude,codex,agents,gemini,antigravity)")
	c.Flags().StringArray("root", nil, "Explicit skill root to scan; may be repeated and replaces default roots")
	c.Flags().Bool("json", false, "Print JSON")
	c.Flags().Bool("strict", false, "Treat legacy skills without schema_version/frontmatter as validation failures")
}

func scanSkillsFromCmd(cmd *cobra.Command) (*aisettings.SkillScanReport, bool, bool, error) {
	toolFlag, _ := cmd.Flags().GetString("tool")
	roots, _ := cmd.Flags().GetStringArray("root")
	asJSON, _ := cmd.Flags().GetBool("json")
	strict, _ := cmd.Flags().GetBool("strict")
	home := homeFromCmd(cmd)
	report, err := aisettings.ScanSkills(aisettings.SkillScanOptions{
		HomeDir: home,
		Tools:   parseAgentToolIDs(toolFlag),
		Roots:   roots,
	})
	return report, asJSON, strict, err
}

func newAISkillsStatusCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "status",
		Short: "Show configured skills SSOT symlink status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts, err := skillsOptionsFromCmd(cmd)
			if err != nil {
				return err
			}
			report, err := newSkillsManagerFromCmd(cmd).Status(opts)
			if err != nil {
				return err
			}
			asJSON, _ := cmd.Flags().GetBool("json")
			if asJSON {
				return printJSON(cmd, report)
			}
			verbose, _ := cmd.Flags().GetBool("verbose")
			printSkillsStatus(printerFrom(cmd), report, verbose)
			return nil
		},
	}
	addSkillManageFlags(c)
	c.Flags().Bool("json", false, "Print JSON")
	return c
}

func newAISkillsPathCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "path",
		Short: "Show skills SSOT and target skill roots",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts, err := skillsOptionsFromCmd(cmd)
			if err != nil {
				return err
			}
			mgr := newSkillsManagerFromCmd(cmd)
			report, err := mgr.Status(opts)
			if err != nil {
				return err
			}
			p := printerFrom(cmd)
			p.Header("AI Skills Paths")
			printSkillsStatusSummary(p, report)
			p.Section("Target Roots")
			for _, tool := range report.Tools {
				root, err := mgr.TargetRoot(tool)
				if err != nil {
					return err
				}
				p.Bullet(ui.StyleHint.Render(ui.MarkPartial), fmt.Sprintf("%-12s %s", ui.StyleValue.Render(tool), root))
			}
			return nil
		},
	}
	addSkillManageFlags(c)
	return c
}

func addSkillManageFlags(c *cobra.Command) {
	c.Flags().String("provider", "", "Skills SSOT provider: maru or path (defaults to maru)")
	c.Flags().String("ssot", "", "Skills SSOT root path (defaults to ~/.maru/skills for provider=maru)")
	c.Flags().String("tool", "", "Comma-separated managed targets (claude,codex); auto-detected when omitted")
}

func skillsOptionsFromCmd(cmd *cobra.Command) (aisettings.SkillsOptions, error) {
	provider, _ := cmd.Flags().GetString("provider")
	ssot, _ := cmd.Flags().GetString("ssot")
	toolFlag, _ := cmd.Flags().GetString("tool")

	tools := parseAgentToolIDs(toolFlag)
	if state, err := loadStateForCmd(cmd); err == nil {
		cfg := state.Modules.AI.Skills
		if provider == "" {
			provider = cfg.Provider
		}
		if ssot == "" {
			ssot = cfg.SSOTPath
		}
		if !cmd.Flags().Changed("tool") && state.Modules.AI.Tooling != nil {
			tools = append([]string{}, state.Modules.AI.Tooling.Agents...)
		} else if len(tools) == 0 && len(cfg.Tools) > 0 {
			tools = append([]string(nil), cfg.Tools...)
		}
	}

	if !cmd.Flags().Changed("tool") {
		selection, err := effectiveToolingForCmd(cmd)
		if err != nil {
			return aisettings.SkillsOptions{}, err
		}
		if selection != nil {
			tools = append([]string{}, selection.Agents...)
		} else if path, _ := cmd.Flags().GetString("config"); path != "" {
			tools = []string{}
		}
	}

	// Provider defaults to maru when neither a provider nor an ssot is given;
	// maru auto-defaults ssot to ~/.maru/skills. Gated on both empty so a
	// lone --ssot still infers provider=path in resolveOptions.
	if provider == "" && ssot == "" {
		provider = aisettings.SkillsProviderMaru
	}

	// CLI-friendly remediation. The library (internal/aisettings) keeps its
	// validation errors caller-neutral; flag/config guidance is added here,
	// where --provider/--ssot/--tool actually exist.
	if provider == aisettings.SkillsProviderPath && ssot == "" {
		return aisettings.SkillsOptions{}, fmt.Errorf("skills provider %q requires --ssot <dir>; or use --provider maru (defaults to %s)", aisettings.SkillsProviderPath, aisettings.DefaultMaruSkillsRoot)
	}

	// The read-only diagnostics default the tool set to detected tools
	// (fallback to all registered) so they never hard-fail.
	if len(tools) == 0 {
		tools = newSkillsManagerFromCmd(cmd).DefaultTools()
	}

	return aisettings.SkillsOptions{Provider: provider, SSOTPath: ssot, Tools: tools}, nil
}

func newSkillsManagerFromCmd(cmd *cobra.Command) *aisettings.SkillsManager {
	mgr := aisettings.NewSkillsManager(homeFromCmd(cmd))
	over, _ := cmd.Flags().GetString("home")
	mgr.ExplicitHome = over != ""
	if selection, err := effectiveToolingForCmd(cmd); err == nil && selection != nil {
		mgr.SelectedTools = append([]string{}, selection.Agents...)
	} else if path, _ := cmd.Flags().GetString("config"); path != "" {
		mgr.SelectedTools = []string{}
	}
	return mgr
}

func printSkillsStatus(p *Printer, report *aisettings.SkillsStatusReport, verbose bool) {
	p.Header("AI Skills Status")
	p.Line("%s", ui.StyleHint.Render("read-only diagnosis; runtime symlinks are managed by the Maru app (sync via Maru)"))
	printSkillsStatusSummary(p, report)
	if len(report.Items) > 0 {
		p.Section("Targets")
		var rows []ui.CheckRow
		for _, item := range report.Items {
			marker, _ := skillLinkStatusMarker(item.Status)
			if item.ToolID == "" {
				p.Bullet(marker, item.Message)
				continue
			}
			detail := item.ToolID + " " + item.TargetPath
			if item.Message != "" {
				detail += "\n" + item.Message
			}
			rows = append(rows, ui.CheckRow{Marker: marker, ID: item.SkillName, State: item.Status, Detail: detail})
		}
		p.CheckRows(rows, verbose)
	}

	for _, warning := range report.Warnings {
		p.Bullet(ui.MarkWarn, warning)
	}
}

func printSkillsStatusSummary(p *Printer, report *aisettings.SkillsStatusReport) {
	if report == nil {
		return
	}
	p.KV("Provider", report.Provider)
	p.KV("SSOT", report.SSOTPath)
	p.KV("Tools", strings.Join(report.Tools, ","))
	p.KV("Sources", fmt.Sprintf("%d", len(report.Sources)))
}

func skillLinkStatusMarker(status string) (string, interface{ Render(...string) string }) {
	switch status {
	case aisettings.SkillLinkStatusInSync:
		return ui.MarkPresent, ui.StyleSuccess
	case aisettings.SkillLinkStatusConflict:
		return ui.MarkWarn, ui.StyleWarning
	case aisettings.SkillLinkStatusMissing, aisettings.SkillLinkStatusSourceMissing:
		return ui.MarkAbsent, ui.StyleHint
	default:
		return ui.MarkUnknown, ui.StyleHint
	}
}

func printSkillReport(p *Printer, report *aisettings.SkillScanReport, strict, verbose bool) {
	p.Header("AI Skills")
	p.KV("Roots", fmt.Sprintf("%d", len(report.Roots)))
	p.KV("Skills", fmt.Sprintf("%d", len(report.Items)))
	p.KV("Valid", fmt.Sprintf("%d", report.Counts[aisettings.SkillStatusValid]))
	p.KV("Legacy", fmt.Sprintf("%d", report.Counts[aisettings.SkillStatusLegacy]))
	p.KV("Invalid", fmt.Sprintf("%d", report.Counts[aisettings.SkillStatusInvalid]))
	if strict {
		p.KV("Strict", "true")
	}
	p.Section("Roots")
	for _, root := range report.Roots {
		p.Bullet(ui.StyleHint.Render(ui.MarkPartial), fmt.Sprintf("%-8s %s", ui.StyleValue.Render(root.Tool), root.Path))
	}
	if len(report.Items) > 0 {
		p.Section("Skills")
		rows := make([]ui.CheckRow, 0, len(report.Items))
		for _, item := range report.Items {
			marker, _ := skillStatusMarker(item.Status)
			if strict && item.Status == aisettings.SkillStatusLegacy {
				marker = ui.MarkFail
			}
			name := item.Frontmatter.Name
			if name == "" {
				name = "(unnamed)"
			}
			detail := item.Tool + " " + item.Path
			if len(item.Errors) > 0 {
				detail += "\n" + strings.Join(item.Errors, "; ")
			}
			rows = append(rows, ui.CheckRow{Marker: marker, ID: name, State: item.Status, Detail: detail})
		}
		p.CheckRows(rows, verbose)
	}

	if len(report.Duplicates) > 0 {
		p.Section("Duplicates")
		for _, dup := range report.Duplicates {
			p.Bullet(ui.MarkFail, fmt.Sprintf("%s: %s", dup.Name, strings.Join(dup.Paths, ", ")))
		}
	}
	for _, err := range report.Errors {
		p.Bullet(ui.MarkFail, err)
	}
}

func skillStatusMarker(status string) (string, interface{ Render(...string) string }) {
	switch status {
	case aisettings.SkillStatusValid:
		return ui.MarkPresent, ui.StyleSuccess
	case aisettings.SkillStatusInvalid:
		return ui.MarkFail, ui.StyleError
	case aisettings.SkillStatusLegacy:
		return ui.MarkPartial, ui.StyleHint
	default:
		return ui.MarkUnknown, ui.StyleHint
	}
}

func newAIAuditCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Inspect append-only dot ai audit events",
	}
	cmd.AddCommand(newAIAuditTailCmd())
	cmd.AddCommand(newAIAuditSummaryCmd())
	return cmd
}

func newAIAuditTailCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tail [N]",
		Short: "Print the last N dot ai audit events as JSONL",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			n := 20
			if len(args) == 1 {
				parsed, err := strconv.Atoi(args[0])
				if err != nil || parsed < 1 {
					return usageError(cmd, fmt.Errorf("argument must be a positive integer"))
				}
				n = parsed
			}
			events, malformed, err := aisettings.TailAIEvents(homeFromCmd(cmd), n)
			if err != nil {
				return err
			}
			for _, event := range events {
				line, err := json.Marshal(event)
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), string(line))
			}
			if malformed > 0 {
				printerFrom(cmd).Warn("%d malformed audit line(s) skipped", malformed)
			}
			return nil
		},
	}
}

func newAIAuditSummaryCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "summary",
		Short: "Summarize dot ai audit events by type",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			sum, err := aisettings.SummarizeAIEvents(homeFromCmd(cmd))
			if err != nil {
				return err
			}
			p := printerFrom(cmd)
			p.Header("AI Audit")
			p.KV("Path", sum.Path)
			p.KV("Events", fmt.Sprintf("%d", sum.Total))
			if sum.LastEvent != nil {
				p.KV("Last", fmt.Sprintf("%s %s", sum.LastEvent.Timestamp, sum.LastEvent.Type))
			}
			if sum.Malformed > 0 {
				p.KV("Malformed", fmt.Sprintf("%d", sum.Malformed))
			}
			if len(sum.ByType) > 0 {
				p.Section("By Type")
				for _, typ := range aisettings.SortedAIEventTypes(sum) {
					p.Bullet(ui.StyleHint.Render(ui.MarkPartial), fmt.Sprintf("%-24s %d", typ, sum.ByType[typ]))
				}
			}
			return nil
		},
	}
}

func auditAIEvent(cmd *cobra.Command, typ string, payload map[string]any) error {
	if dryRun, _ := cmd.Flags().GetBool("dry-run"); dryRun {
		return nil
	}
	_, err := aisettings.AppendAIEvent(homeFromCmd(cmd), typ, payload)
	return err
}

func homeFromCmd(cmd *cobra.Command) string {
	home, _ := os.UserHomeDir()
	if cmd != nil {
		if over, _ := cmd.Flags().GetString("home"); over != "" {
			home = over
		}
	}
	return home
}

func aiSummaryPayload(sum *aisettings.Summary) map[string]any {
	if sum == nil {
		return map[string]any{}
	}
	payload := map[string]any{
		"version":       sum.Version,
		"path":          sum.Path,
		"entries_count": len(sum.Entries),
		"files":         sum.Files,
		"bytes":         sum.Bytes,
	}
	if sum.PreBackupPath != "" {
		payload["pre_backup_path"] = sum.PreBackupPath
	}
	return payload
}

func agentsApplyPayload(result *aisettings.ApplyResult) map[string]any {
	payload := map[string]any{
		"items_count": 0,
		"changed":     0,
		"backed_up":   0,
	}
	if result == nil {
		return payload
	}
	payload["items_count"] = len(result.Items)
	items := make([]map[string]any, 0, len(result.Items))
	changed := 0
	backedUp := 0
	for _, item := range result.Items {
		if item.Changed {
			changed++
		}
		if item.BackedUp {
			backedUp++
		}
		items = append(items, map[string]any{
			"tool":          item.ToolID,
			"path":          item.TargetPath,
			"changed":       item.Changed,
			"backed_up":     item.BackedUp,
			"backup_path":   item.BackupPath,
			"conflict":      item.Conflict,
			"expected_hash": item.ExpectedHash,
			"actual_hash":   item.ActualHash,
		})
	}
	payload["changed"] = changed
	payload["backed_up"] = backedUp
	payload["items"] = items
	return payload
}

func hudApplyPayload(result *aisettings.HUDResult, persist bool) map[string]any {
	payload := map[string]any{
		"persist":     persist,
		"items_count": 0,
		"changed":     0,
	}
	if result == nil {
		return payload
	}
	payload["items_count"] = len(result.Items)
	items := make([]map[string]any, 0, len(result.Items))
	changed := 0
	for _, item := range result.Items {
		if item.Changed {
			changed++
		}
		items = append(items, map[string]any{
			"tool":    item.ToolID,
			"path":    item.TargetPath,
			"changed": item.Changed,
			"drift":   item.Drift,
		})
	}
	payload["changed"] = changed
	payload["items"] = items
	return payload
}

func coauthorGuardPayload(result *aisettings.CoauthorGuardResult, persist bool) map[string]any {
	payload := map[string]any{"persist": persist}
	if result == nil {
		return payload
	}
	payload["mode"] = result.Status.Mode
	payload["hook_path"] = result.Status.HookPath
	payload["git_config_path"] = result.Status.GitConfigPath
	payload["agents_path"] = result.Status.AgentsPath
	payload["hook_changed"] = result.HookChanged
	payload["config_changed"] = result.ConfigChanged
	payload["agents_changed"] = result.AgentsChanged
	payload["agents_applied"] = result.AgentsApplied
	return payload
}

func printJSON(cmd *cobra.Command, value any) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}

func skillValidationSummary(report *aisettings.SkillScanReport, strict bool) ui.CheckSummary {
	summary := ui.CheckSummary{Failed: len(report.Errors) + len(report.Duplicates)}
	for _, item := range report.Items {
		switch item.Status {
		case aisettings.SkillStatusValid:
			summary.OK++
		case aisettings.SkillStatusInvalid:
			summary.Failed++
		case aisettings.SkillStatusLegacy:
			if strict {
				summary.Failed++
			} else {
				summary.Warnings++
			}
		default:
			summary.Unknown++
		}
	}
	if summary.Failed+summary.Warnings+summary.OK+summary.Unknown == 0 {
		summary.Unknown = 1
	}
	return summary
}
