package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/clean"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

func newCleanCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "clean [path]",
		Short: "Remove junk directories (node_modules, caches, venvs) from workspace",
		Long: `Scan for and remove directories that waste disk space and cause
cloud sync problems: node_modules, __pycache__, .venv,
build caches, and .DS_Store files.

Default: scan and show what would be removed (preview mode).
Use --yes to actually delete. Use --all to include risky patterns
(dist/, build/, out/, target/).

The _sys/ subtree is ALWAYS protected and will never be touched.`,
		Aliases: []string{"gc"},
		Args:    cobra.MaximumNArgs(1),
		RunE:    runClean,
	}
	cmd.Flags().Bool("all", false, "Include risky patterns (dist/, build/, out/, target/)")
	return cmd
}

func runClean(cmd *cobra.Command, args []string) error {
	root := defaultWorkspaceWorkRoot()
	if len(args) > 0 {
		root = absPath(args[0])
	}

	if _, err := os.Stat(root); err != nil {
		return fmt.Errorf("path does not exist: %s", root)
	}

	includeRisky, _ := cmd.Flags().GetBool("all")
	yes, _ := cmd.Flags().GetBool("yes")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	p := printerFrom(cmd)

	p.Header("Workspace Cleanup")
	p.KV("Scanning", root)

	scanner := clean.NewScanner(root, includeRisky)
	result, err := scanner.Scan()
	if err != nil {
		return fmt.Errorf("scan failed: %w", err)
	}

	if len(result.Matches) == 0 && len(result.Protected) == 0 {
		p.Blank()
		p.Success("No junk found. Workspace is clean.")
		return nil
	}

	// Group matches by category
	categories := []string{"node", "python", "cache", "build", "misc"}
	for _, cat := range categories {
		var catMatches []clean.Match
		for _, m := range result.Matches {
			if m.Pattern.Category == cat {
				catMatches = append(catMatches, m)
			}
		}
		if len(catMatches) == 0 {
			continue
		}

		var catSize int64
		for _, m := range catMatches {
			catSize += m.Size
		}

		p.Section(fmt.Sprintf("%s (%d items, %s)", cat, len(catMatches), clean.FormatSize(catSize)))

		if cat == "misc" {
			// .DS_Store and similar: collapse to a single summary line
			p.Bullet(ui.StyleHint.Render(ui.MarkPartial),
				fmt.Sprintf("%s %s",
					ui.StyleHint.Render(".DS_Store"),
					ui.StyleHint.Render(fmt.Sprintf("(%d files)", len(catMatches)))))
			continue
		}

		for _, m := range catMatches {
			marker := ui.StyleHint.Render(ui.MarkPending)
			p.Bullet(marker, fmt.Sprintf("%-15s %s  %s",
				ui.StyleHint.Render(m.Pattern.Name),
				ui.StyleValue.Render(m.RelPath),
				ui.StyleHint.Render(clean.FormatSize(m.Size))))
		}
	}

	// Protected items
	if len(result.Protected) > 0 {
		p.Section("Protected (skipped)")
		for _, m := range result.Protected {
			marker := ui.StyleHint.Render(ui.MarkPartial)
			p.Bullet(marker, fmt.Sprintf("%-15s %s  %s",
				ui.StyleHint.Render(m.Pattern.Name),
				ui.StyleValue.Render(m.RelPath),
				ui.StyleHint.Render("(inside _sys/)")))
		}
	}

	// Summary
	p.Blank()
	summary := fmt.Sprintf("Total: %d items, ~%s to free",
		len(result.Matches), clean.FormatSize(result.TotalSize()))
	if len(result.Protected) > 0 {
		summary += fmt.Sprintf(" (%d protected, not touched)", len(result.Protected))
	}
	p.Line("  %s", summary)

	// Action
	if dryRun || !yes {
		hint := "Run with --yes to delete"
		if !includeRisky {
			hint += ", or --all --yes to include dist/build/out/target"
		}
		p.Line("  %s", ui.StyleHint.Render(hint))
		return nil
	}

	// Actually delete
	p.Blank()
	deleted, freed, errs := clean.Delete(result.Matches)
	for _, e := range errs {
		p.Bullet(ui.StyleError.Render(ui.MarkFail), fmt.Sprintf("%s", e))
	}
	p.Bullet(ui.StyleSuccess.Render(ui.MarkPresent),
		fmt.Sprintf("Deleted %d items, freed %s", deleted, clean.FormatSize(freed)))
	return nil
}
