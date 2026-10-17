package cli

import (
	"fmt"

	"github.com/STAIxBWLB/dotfiles/internal/syncer"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
	"github.com/spf13/cobra"
)

// dot sync shared: the manual shared-folder exclusion list.

func newSyncSharedCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "shared",
		Short: "Manage manual shared-folder exclusions",
		Long: `View and manage which folders gsync skips because they are shared.

This list contains relative paths the operator added to the workspace-local
sync config. Use it for owned-but-shared-out folders that must never be
propagated through the workspace-authoritative mirror flow.

The list feeds a per-run dynamic excludes file passed to rsync.

  dot sync shared             # alias for list
  dot sync shared list
  dot sync shared add <path>...
  dot sync shared remove <path>...
  dot sync shared clear`,
		RunE: runSyncSharedList,
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "list",
			Short: "Show manual shared entries",
			RunE:  runSyncSharedList,
		},
		&cobra.Command{
			Use:          "add <path>...",
			Short:        "Add one or more paths to the manual shared-excludes list",
			Args:         cobra.MinimumNArgs(1),
			RunE:         runSyncSharedAdd,
			SilenceUsage: true,
		},
		&cobra.Command{
			Use:          "remove <path>...",
			Aliases:      []string{"rm"},
			Short:        "Remove one or more paths from the manual shared-excludes list",
			Args:         cobra.MinimumNArgs(1),
			RunE:         runSyncSharedRemove,
			SilenceUsage: true,
		},
		&cobra.Command{
			Use:          "clear",
			Short:        "Empty the manual shared-excludes list",
			RunE:         runSyncSharedClear,
			SilenceUsage: true,
		},
	)
	return cmd
}

func runSyncSharedList(cmd *cobra.Command, _ []string) error {
	bs, err := syncer.Bootstrap(syncBootstrapOptions(cmd, true))
	if err != nil {
		return err
	}
	cfg := bs.Config
	entries, err := syncer.ScanShared(stripTrailingSlash(cfg.MirrorPath), cfg.SharedExcludes)
	if err != nil {
		return fmt.Errorf("scanning shared entries: %w", err)
	}
	p := printerFrom(cmd)
	if cfg.Target.IsSSH() {
		// Shared excludes name folders shared out of a local cloud mirror;
		// an SSH target has none, so the stored entries do nothing here.
		return printInactiveShared(p, cfg.SharedExcludes, cfg.Target.RsyncDest())
	}
	dropped := syncer.DroppedSharedEntries(cfg.SharedExcludes)
	if len(entries) == 0 && len(dropped) == 0 {
		p.Line("No manual shared excludes configured.")
		p.Line("Add owned-but-shared-out folders with: dot sync shared add <path>")
		return nil
	}
	p.Header(fmt.Sprintf("Shared exclusions under %s", stripTrailingSlash(cfg.MirrorPath)))
	for _, e := range entries {
		detail := e.Detail
		if detail == "" {
			detail = "—"
		}
		p.Line("  %-8s  %-40s  %s", e.Reason.String(), e.RelPath, detail)
	}
	for _, raw := range dropped {
		p.Line("  %-8s  %-40q  %s", "ignored", raw, "(not a path under the workspace; dot sync shared remove it)")
	}
	p.Blank()
	p.Line("auto entries are detected from filesystem properties; manual entries are operator-curated.")
	return nil
}

func runSyncSharedAdd(cmd *cobra.Command, args []string) error {
	bs, err := syncer.Bootstrap(syncBootstrapOptions(cmd, false))
	if err != nil {
		return err
	}
	added, err := syncer.SharedAdd(bs.Config, args)
	if err != nil {
		return err
	}

	p := printerFrom(cmd)
	if len(added) == 0 {
		p.Line("No new entries — all already present.")
	} else {
		for _, rel := range added {
			p.Line("✓ added %q", rel)
		}
	}
	return nil
}

func runSyncSharedRemove(cmd *cobra.Command, args []string) error {
	bs, err := syncer.Bootstrap(syncBootstrapOptions(cmd, false))
	if err != nil {
		return err
	}
	removed, err := syncer.SharedRemove(bs.Config, args)
	if err != nil {
		return err
	}

	p := printerFrom(cmd)
	if len(removed) == 0 {
		p.Line("No matching entries — nothing removed.")
	} else {
		for _, rel := range removed {
			p.Line("✓ removed %q", rel)
		}
	}
	return nil
}

func runSyncSharedClear(cmd *cobra.Command, _ []string) error {
	bs, err := syncer.Bootstrap(syncBootstrapOptions(cmd, false))
	if err != nil {
		return err
	}
	yes, _ := cmd.Flags().GetBool("yes")
	n, err := syncer.SharedCount(bs.Config)
	if err != nil {
		return err
	}
	p := printerFrom(cmd)
	if n == 0 {
		p.Line("Manual shared-excludes list is already empty.")
		return nil
	}
	confirmed, err := ui.Confirm(fmt.Sprintf("Clear %d manual shared-excludes entries?", n), yes)
	if err != nil {
		return err
	}
	if !confirmed {
		p.Line("Aborted.")
		return nil
	}
	if err := syncer.SharedClear(bs.Config); err != nil {
		return err
	}
	p.Line("✓ Cleared %d manual entries.", n)
	return nil
}

// printInactiveShared lists the stored shared entries of an SSH-target
// profile, which apply to a local mirror only, so the list agrees with the
// count `shared clear` uses (#231).
func printInactiveShared(p *Printer, stored []string, target string) error {
	entries := syncer.StoredSharedEntries(stored)
	if len(entries) == 0 {
		p.Line("No manual shared excludes configured.")
		return nil
	}
	p.Line("Shared excludes apply to a local mirror target only; these stored entries are inactive for %s:", target)
	for _, e := range entries {
		p.Line("  %q", e)
	}
	return nil
}
