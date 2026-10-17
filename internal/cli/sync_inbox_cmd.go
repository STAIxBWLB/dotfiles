package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/syncer"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
	"github.com/spf13/cobra"
)

// dot sync inbox: inspect and manage the mirror intake staging area.

func newSyncInboxCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "inbox",
		Short: "Inspect and manage the mirror intake staging area",
		Long: `View what's staged + tracked under .dotfiles/sync/, force a
re-intake of one path, or clear the imports + tombstones manifests
entirely.

  dot sync inbox                  # alias for list
  dot sync inbox list
  dot sync inbox forget <relpath> # next intake re-stages this path
  dot sync inbox clear            # empty imports + tombstones`,
		RunE: runSyncInboxList,
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "list",
			Short: "Show staged run-dirs, imports manifest entries, and tombstones",
			RunE:  runSyncInboxList,
		},
		&cobra.Command{
			Use:          "forget <relpath>",
			Short:        "Drop a path from imports.manifest so the next intake re-stages it",
			Args:         cobra.ExactArgs(1),
			RunE:         runSyncInboxForget,
			SilenceUsage: true,
		},
		&cobra.Command{
			Use:          "clear",
			Short:        "Empty imports.manifest and tombstones.log",
			RunE:         runSyncInboxClear,
			SilenceUsage: true,
		},
	)
	return cmd
}

func runSyncInboxList(cmd *cobra.Command, _ []string) error {
	bs, err := syncer.Bootstrap(syncBootstrapOptions(cmd, true))
	if err != nil {
		return err
	}
	report, err := syncer.InboxSummary(bs.Config)
	if err != nil {
		return err
	}
	p := printerFrom(cmd)

	p.Header("gsync inbox")
	p.KV("Staging root", report.StagingRoot)
	p.KV("Pending run-dirs", fmt.Sprintf("%d (%d files)", report.RunDirs, report.Files))
	p.KV("Imports manifest", fmt.Sprintf("%d entries", report.Imports))
	p.KV("Tombstones", fmt.Sprintf("%d entries", len(report.Tombstones)))
	if len(report.Tombstones) > 0 {
		p.Section("Recent tombstones (newest 5):")
		shown := report.Tombstones
		if len(shown) > 5 {
			shown = shown[len(shown)-5:]
		}
		for _, t := range shown {
			p.Bullet("•", fmt.Sprintf("%s (detected %s)", t.RelPath, t.DetectedAt.Format(time.RFC3339)))
		}
	}
	p.Blank()
	return nil
}

func runSyncInboxForget(cmd *cobra.Command, args []string) error {
	bs, err := syncer.Bootstrap(syncBootstrapOptions(cmd, false))
	if err != nil {
		return err
	}
	rel := strings.TrimSpace(args[0])
	dropped, err := syncer.InboxForget(bs.Config, args[0])
	if err != nil {
		return err
	}
	p := printerFrom(cmd)
	if dropped {
		p.Line("✓ forgot %q — next intake will re-stage it if mirror still has it", rel)
	} else {
		p.Line("no entry for %q in imports.manifest — nothing to forget", rel)
	}
	return nil
}

func runSyncInboxClear(cmd *cobra.Command, _ []string) error {
	bs, err := syncer.Bootstrap(syncBootstrapOptions(cmd, false))
	if err != nil {
		return err
	}
	cfg := bs.Config
	yes, _ := cmd.Flags().GetBool("yes")
	imports, tomb, err := syncer.InboxManifestCounts(cfg)
	if err != nil {
		return err
	}
	p := printerFrom(cmd)
	if imports == 0 && tomb == 0 {
		p.Line("imports.manifest and tombstones.log are already empty.")
		return nil
	}
	confirmed, err := ui.Confirm(fmt.Sprintf("Clear %d imports + %d tombstones? Next intake will re-stage anything still on mirror.", imports, tomb), yes)
	if err != nil {
		return err
	}
	if !confirmed {
		p.Line("Aborted.")
		return nil
	}
	if err := syncer.ClearImportsAndTombstones(cfg.LocalPaths); err != nil {
		return err
	}
	p.Line("✓ cleared %d imports + %d tombstones.", imports, tomb)
	return nil
}
