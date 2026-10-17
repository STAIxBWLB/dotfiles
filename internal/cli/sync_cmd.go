package cli

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"charm.land/huh/v2"
	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/syncer"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
	"github.com/STAIxBWLB/dotfiles/internal/ws"
)

// printSensitiveOverrides renders the engine's complete, sorted policy
// findings without changing the transfer decision they describe. statusMode
// preserves status's key/value adjacency; push previews use a final section.
func printSensitiveOverrides(p *Printer, overrides []syncer.SensitiveOverride, statusMode bool) {
	if len(overrides) == 0 {
		return
	}
	if statusMode {
		p.KV("Sensitive overrides", fmt.Sprintf("%d allow rule(s) override built-in secret exclusions", len(overrides)))
	} else {
		p.Section(fmt.Sprintf("Sensitive overrides: %d", len(overrides)))
	}
	marker := ui.StyleWarning.Render(ui.MarkWarn)
	for _, override := range overrides {
		p.Bullet(marker, fmt.Sprintf("%s re-includes %s", escapeTerminalControls(override.AllowPattern), escapeTerminalControls(override.DenyPattern)))
	}
}

// escapeTerminalControls preserves complete pattern text while replacing every
// terminal control rune with its Go escape spelling. Invalid UTF-8 bytes are
// represented as byte escapes instead of being dropped or replaced.
func escapeTerminalControls(value string) string {
	var escaped strings.Builder
	for len(value) > 0 {
		r, size := utf8.DecodeRuneInString(value)
		if r == utf8.RuneError && size == 1 {
			fmt.Fprintf(&escaped, "\\x%02x", value[0])
			value = value[1:]
			continue
		}
		if unicode.IsControl(r) {
			quoted := strconv.QuoteRuneToASCII(r)
			escaped.WriteString(quoted[1 : len(quoted)-1])
		} else {
			escaped.WriteRune(r)
		}
		value = value[size:]
	}
	return escaped.String()
}

func newSyncCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "sync",
		Aliases: []string{"gsync", "gdrive-sync"},
		Short:   "Sync workspace to a local mirror or SSH remote via rsync",
		Args:    cobra.NoArgs,
		Long: `Unified workspace sync: rsync the workspace to a target — either a
local cloud-client folder (e.g. ~/Dropbox/work) or an SSH remote (host:path).

The sync set is a Git-aware union: every file tracked by the workspace's
root Git repo, plus untracked files matching the binary-extension allowlist.
Git submodules are never synced here — they sync through Git itself.
Secrets (.env, .secrets, ...) are excluded by default and only sync when
explicitly allowed in allow.txt.

Workspace is authoritative. Push sends local creates and updates to the
target; pull restores baseline-tracked payloads from it. New target-origin
files still stage into inbox/gdrive for manual routing.

	Getting started:
	  dot sync setup       Check rsync and manage the opt-in schedulers
	  dot sync target      Show or set the sync target
	  dot sync resume      Clear the paused gate
	  dot sync push        Push workspace → target (use --mode for clean/force)
	  dot sync pull        Restore baseline-tracked payloads from target

	Maintenance:
	  dot sync status      Show filters, last pull/push/intake, conflicts, paused state, scheduler
	  dot sync filters     Show effective filter layers or reset them from templates
	  dot sync conflicts   List or prune timestamped backup directories
	  dot sync names       Plan or apply staged NFD filename normalization
	  dot sync pause       Stop managed schedulers + set paused gate
	  dot sync resume      Clear paused gate and re-arm installed schedulers

Run without a subcommand to print this help.
Deprecated aliases: 'dot gsync', 'dot gdrive-sync'.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
		SilenceUsage: true,
		PersistentPreRun: func(cmd *cobra.Command, _ []string) {
			warnDeprecatedSyncAlias()
		},
	}
	cmd.PersistentFlags().String("profile", syncer.DefaultProfile,
		"sync profile (store under <workspace>/.dotfiles/<profile>/); \"sync\" is the cloud mirror")
	cmd.PersistentFlags().BoolP("verbose", "V", false, "Show rsync progress output")
	cmd.PersistentFlags().String("mode", syncer.ModeManual.String(), "execution mode for push/pull: manual, clean, or force")
	cmd.PersistentFlags().String("filter-mode", "", "override config filter mode for this run: include or exclude")
	cmd.AddCommand(
		newSyncSyncCmd(),
		newSyncPullCmd(),
		newSyncPushCmd(),
		newSyncIntakeCmd(),
		newSyncInboxCmd(),
		newSyncStatusCmd(),
		newSyncLogCmd(),
		newSyncConfigureCmd(),
		newSyncOwnerCmd(),
		newSyncConflictsCmd(),
		newSyncSetupCmd(),
		newSyncResumeCmd(),
		newSyncPauseCmd(),
		newSyncSharedCmd(),
		newSyncInitCmd(),
		newSyncTargetCmd(),
		newSyncMirrorCmd(),
		newSyncFiltersCmd(),
		newSyncNamesCmd(),
		newSyncFetchCmd(),
	)
	return cmd
}

// warnDeprecatedSyncAlias prints a one-line notice when the sync command
// group was invoked through a legacy alias. Checked against os.Args because
// cobra's CalledAs() reflects the leaf command, not the group.
func warnDeprecatedSyncAlias() {
	if len(os.Args) < 2 {
		return
	}
	switch os.Args[1] {
	case "gsync", "gdrive-sync":
		fmt.Fprintf(os.Stderr, "note: 'dot %s' is deprecated; use 'dot sync'\n", os.Args[1])
	}
}

// syncBootstrapOptions translates the sync command tree's flags into the
// engine's bootstrap options. Flag translation is cli's retained role; state
// loading, profile resolution and runner construction are the engine's
// (syncer.Bootstrap, D-07).
func syncBootstrapOptions(cmd *cobra.Command, readOnly bool) syncer.BootstrapOptions {
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	profile, _ := cmd.Flags().GetString("profile")
	verbose, _ := cmd.Flags().GetBool("verbose")
	filterMode, _ := cmd.Flags().GetString("filter-mode")
	return syncer.BootstrapOptions{
		Profile:       profile,
		ReadOnly:      readOnly,
		DryRun:        dryRun,
		Verbose:       verbose,
		FilterMode:    filterMode,
		FilterModeSet: cmd.Flags().Changed("filter-mode"),
		Home:          homeOverrideFrom(cmd),
	}
}

// syncPreflight reports whether the run may proceed, printing the engine's
// refusal when it may not. Where this sits among each handler's other guards
// is what fixes that command's error precedence, so the call stays in cli even
// though syncer.Preflight does the classifying.
func syncPreflight(p *Printer, cfg *syncer.Config, runner *exec.Runner) *syncer.PreflightBlock {
	block := syncer.Preflight(runner, cfg)
	if block != nil {
		p.Line("%s", block.Reason())
	}
	return block
}

// syncRender carries what the shared event renderer needs from the invoking
// command: the resolved config plus the two flag-derived tokens that appear
// inside the progress lines.
type syncRender struct {
	cfg                *syncer.Config
	mode               syncer.RunMode
	strict             bool
	dryRun             bool
	sensitiveOverrides []syncer.SensitiveOverride
}

// renderSyncEvent turns engine progress into the lines the sync commands have
// always printed. One renderer for every call site, so the wording cannot
// drift apart between push, pull, intake and fetch.
func renderSyncEvent(p *Printer, r syncRender) func(syncer.SyncEvent) {
	cfg := r.cfg
	return func(e syncer.SyncEvent) {
		switch e.Kind {
		case syncer.SyncEventDryRunNotice:
			p.Line("  (dry-run — no changes)")
		case syncer.SyncEventPushSSHStart:
			p.Line("Push %s → %s (%s, direct rsync — no plan preview for ssh targets)",
				cfg.LocalPath, cfg.Target.RsyncDest(), cfg.Propagation)
			if r.dryRun {
				p.Line("  (dry-run — no changes)")
			}
			printSensitiveOverrides(p, r.sensitiveOverrides, false)
		case syncer.SyncEventPushPlanStart:
			p.Line("Push plan for %s → %s (%s, mode=%s)", cfg.LocalPath, cfg.MirrorPath, cfg.Propagation, r.mode)
		case syncer.SyncEventPushPlanReady:
			printPushPlan(p, e.PushPlan)
			printSensitiveOverrides(p, r.sensitiveOverrides, false)
		case syncer.SyncEventPullSSHStart:
			p.Line("Pull %s → %s (direct rsync --update; workspace files win ties)",
				cfg.Target.RsyncDest(), cfg.LocalPath)
		case syncer.SyncEventPullPlanStart:
			p.Line("Pull plan for baseline-tracked payloads %s → %s (%s)", cfg.MirrorPath, cfg.LocalPath, r.mode)
		case syncer.SyncEventPullPlanReady:
			printPullPlan(p, cfg, e.PullResult)
		case syncer.SyncEventIntakeStart:
			p.Line("Intaking %s → %s/inbox/gdrive/<ts>/ (%s mode)", cfg.MirrorPath, stripTrailingSlash(cfg.LocalPath), strictLabel(r.strict))
		case syncer.SyncEventFetchMissing:
			p.Warn("not on target, skipped: %s", e.Path)
		case syncer.SyncEventFetchExcluded:
			p.Warn("kept out by a sync filter (submodules, .git, secrets, excludes or shared folders), skipped: %s", e.Path)
		case syncer.SyncEventPartialTransfer:
			_ = reportPushPartial(p, e.Err)
		case syncer.SyncEventLeftoversMoved:
			p.Line("  Moved %d mirror leftover(s) to %s", e.Candidates, e.Path)
		case syncer.SyncEventPruneSummary:
			p.Line("Would reclaim %s across %d backup dir(s).", ws.FormatSize(e.Reclaimed), e.Candidates)
		}
	}
}

// strictLabel names the fingerprinting mode intake reports it is running in.
func strictLabel(strict bool) string {
	if strict {
		return "strict"
	}
	return "fast"
}

// syncPrompt asks a yes/no question; tests replace it to answer without a
// terminal.
var syncPrompt = ui.Confirm

// confirmSync answers the engine's confirmation requests. The engine names the
// decision; cli owns both the wording and the `--yes` policy (D-09).
func confirmSync(cmd *cobra.Command) syncer.ConfirmFunc {
	return func(req syncer.ConfirmRequest) (bool, error) {
		yes, _ := cmd.Flags().GetBool("yes")
		switch req.Kind {
		case syncer.ConfirmPushSSH:
			return declinedOnAbort(syncPrompt("Push to SSH target?", yes))
		case syncer.ConfirmPushPlan:
			return declinedOnAbort(syncPrompt("Apply this push plan?", yes))
		case syncer.ConfirmPullPlan:
			return syncPrompt("Apply this pull plan?", yes)
		case syncer.ConfirmPruneConflicts:
			return syncPrompt(fmt.Sprintf("Remove %d backup dir(s), reclaiming %s?", req.Candidates, ws.FormatSize(req.Reclaimed)), yes)
		case syncer.ConfirmInstallRsync:
			return syncPrompt("rsync not found. Install it?", yes)
		}
		return false, fmt.Errorf("unknown confirmation request %d", req.Kind)
	}
}

// declinedOnAbort reads Ctrl-C at a push prompt as a decline: the operator
// stopped the run, which is not a failed push (#224).
func declinedOnAbort(ok bool, err error) (bool, error) {
	if errors.Is(err, huh.ErrUserAborted) {
		return false, nil
	}
	return ok, err
}
