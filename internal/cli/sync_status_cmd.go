package cli

import (
	"fmt"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/syncer"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
	"github.com/STAIxBWLB/dotfiles/internal/ws"
	"github.com/spf13/cobra"
)

// dot sync status.

func newSyncStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show local↔mirror sync status",
		Long: `Show local↔mirror sync status.

Status identifies each allow.txt rule that re-includes a built-in secret
exclusion. --json exposes the equivalent versioned structured records.`,
		RunE: runSyncStatus,
	}
	cmd.Flags().Bool("json", false, "print a stable machine-readable status document")
	return cmd
}

func runSyncStatus(cmd *cobra.Command, _ []string) error {
	bs, err := syncer.Bootstrap(syncBootstrapOptions(cmd, true))
	if err != nil {
		return err
	}
	cfg := bs.Config
	sched, _, err := syncer.ResolveScheduler(cfg, bs.Runner)
	if err != nil {
		return err
	}
	st, err := syncer.GetStatus(cmd.Context(), bs.Runner, cfg, bs.State, sched)
	if err != nil {
		return err
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		return writeSyncStatusJSON(cmd, cfg, st, sched)
	}
	p := printerFrom(cmd)
	p.Header("Sync Status")

	if st.RsyncVersion != "" {
		p.KV("rsync", st.RsyncPath+" ("+st.RsyncVersion+")")
	} else {
		p.KV("rsync", "not installed")
	}
	p.KV("Local", st.LocalPath)
	p.KV("Target", st.Target.String())
	if st.StoreDir != "" {
		p.KV("Config", st.StoreDir)
	}
	p.KV("Local exists", boolStr(st.LocalExists))
	if !st.Target.IsSSH() {
		p.KV("Mirror exists", boolStr(st.MirrorExists))
	}
	if st.Paused {
		p.KV("Paused", "yes — run `dot sync resume` to activate")
	} else {
		p.KV("Paused", "no")
	}
	p.KV("Propagation", st.Propagation.String())
	p.KV("Filter mode", st.FilterMode.String())
	if st.SubmoduleCount > 0 {
		p.KV("Submodules", fmt.Sprintf("%d excluded — they sync via Git, not `dot sync`", st.SubmoduleCount))
	}
	if st.UnsupportedNames > 0 {
		p.KV("Unsupported names", ui.StyleWarning.Render(fmt.Sprintf("%d in the workspace — Dropbox/Windows cannot store these; `dot sync names trim` fixes trailing whitespace, names ending in a period need a manual rename", st.UnsupportedNames)))
	}
	if st.AllowCount > 0 {
		p.KV("Secrets", ui.StyleWarning.Render(fmt.Sprintf("allowed: %d pattern(s) in allow.txt — these sync to the target", st.AllowCount)))
	} else {
		p.KV("Secrets", "deny-by-default (allow.txt empty)")
	}
	printSensitiveOverrides(p, st.SensitiveOverrides, true)
	if st.IncludeFile != "" {
		p.KV("Include file", st.IncludeFile)
	}
	if st.ExcludeFile != "" {
		p.KV("Exclude file", st.ExcludeFile)
	}
	if st.IgnoreFile != "" {
		p.KV("Ignore file", st.IgnoreFile)
	}
	if st.Interval > 0 {
		p.KV("Push interval", formatInterval(st.Interval))
		p.KV("Push mode", st.PushMode.String())
		p.KV("Push scheduler", st.SchedulerState.StringForProfile(cfg.Profile))
		if st.SchedulerLastExitCode != nil {
			p.KV("Push last exit", fmt.Sprintf("%d", *st.SchedulerLastExitCode))
		}
	} else {
		p.KV("Push scheduler", "(off — `dot sync setup --push-interval=DUR` to enable)")
	}
	if st.PullInterval > 0 {
		p.KV("Pull interval", formatInterval(st.PullInterval))
		p.KV("Pull mode", st.PullMode.String())
		p.KV("Pull scheduler", st.IntakeSchedulerState.StringForProfile(cfg.Profile))
		if st.IntakeSchedulerLastExitCode != nil {
			p.KV("Pull last exit", fmt.Sprintf("%d", *st.IntakeSchedulerLastExitCode))
		}
	} else {
		p.KV("Pull scheduler", "(off — `dot sync setup --pull-interval=DUR` to enable)")
	}
	if st.Propagation.Delete {
		p.KV("Max delete", fmt.Sprintf("%d", st.MaxDelete))
	}
	p.KV("Lock held", boolStr(st.LockHeld))
	p.KV("Last pull", formatLastSync(st.LastPull))
	p.KV("Last push", formatLastSync(st.LastPush))
	if why := st.PushStalled(time.Now()); why != "" {
		p.KV("Push stalled", ui.StyleWarning.Render(why+" — run `dot sync push` to see the plan, `dot sync log` for each run"))
	}
	p.KV("Last intake", formatLastSync(st.LastIntake))
	if st.LastIntakeTSDir != "" {
		p.KV("Last intake dir", st.LastIntakeTSDir)
	}

	if st.ConflictsError != "" {
		p.Warn("conflict backups not listed: %s", st.ConflictsError)
	}
	printConflictState(p, st, time.Now())
	if n := len(st.Shared); n > 0 {
		p.KV("Shared", fmt.Sprintf("%d manual entries — see `dot sync shared`", n))
	}
	p.Blank()
	return nil
}

// printConflictState shows live conflicts apart from retained backups (#264):
// live conflicts block a clean push, backups are copies that need nothing
// until they are pruned.
func printConflictState(p *Printer, st *syncer.Status, now time.Time) {
	if st.LiveConflicts > 0 {
		p.KV("Conflicts", ui.StyleWarning.Render(fmt.Sprintf("%d live: the last push refused on them; `dot sync push --dry-run` lists them", st.LiveConflicts)))
	} else {
		p.KV("Conflicts", "0 live")
	}
	if len(st.Conflicts) > 0 {
		p.Section(fmt.Sprintf("Backups: %d directories (%s), kept until `dot sync conflicts prune`", len(st.Conflicts), ws.FormatSize(st.BackupBytes)))
		for _, c := range st.Conflicts {
			age := now.Sub(c.ModTime).Truncate(time.Hour)
			p.Bullet("•", fmt.Sprintf("%s (%s ago)", c.Timestamp, age))
		}
	}
}
