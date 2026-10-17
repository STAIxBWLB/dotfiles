package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/fileutil"
	"github.com/STAIxBWLB/dotfiles/internal/syncer"
	"github.com/STAIxBWLB/dotfiles/internal/watchdog"
)

// peerAlertKind classifies one peer sync outcome for the alert reporter.
type peerAlertKind int

const (
	peerAlertNone peerAlertKind = iota
	peerAlertFailure
	peerAlertRecovery
)

// summarizePeerAlertError compacts a run error for an outbound alert: the
// first line only, the operator's home folded to ~, capped in length. Full
// diagnostics stay in the local log; the alert carries a failure summary
// that is safe to send and to persist as the episode's LastMessage (#244).
func summarizePeerAlertError(err error) string {
	msg := strings.SplitN(err.Error(), "\n", 2)[0]
	if home, herr := os.UserHomeDir(); herr == nil && home != "" {
		msg = strings.ReplaceAll(msg, home, "~")
	}
	const maxRunes = 200
	if r := []rune(msg); len(r) > maxRunes {
		msg = string(r[:maxRunes-3]) + "..."
	}
	return msg
}

// classifyPeerOutcome maps a finished PeerSync call onto an alert. The
// designed-clean shapes never alert — an offline peer, a fence demotion and
// a lost lock race are normal operation on a laptop, and reporting them
// trains the operator to ignore the channel (#244). A held two-way run and a
// quarantine burst warn; a real transfer error is critical; a complete run
// reports recovery, which the alerter only turns into a message when an
// episode was open.
func classifyPeerOutcome(res *syncer.PeerSyncResult, runErr error, twoWay bool, quarantineThreshold int, machine string) (peerAlertKind, string, string) {
	if res != nil && (res.Unreachable || res.Demoted) {
		return peerAlertNone, "", ""
	}
	if runErr != nil {
		if errors.Is(runErr, fileutil.ErrLockHeld) {
			return peerAlertNone, "", ""
		}
		return peerAlertFailure, "critical", fmt.Sprintf("peer sync failed on %s: %s", machine, summarizePeerAlertError(runErr))
	}
	if res == nil {
		return peerAlertNone, "", ""
	}
	if !res.Complete {
		if !twoWay {
			return peerAlertNone, "", ""
		}
		return peerAlertFailure, "warn", fmt.Sprintf("peer sync on %s held destructive transitions; baseline unchanged", machine)
	}
	if quarantined := res.QuarantinedHere + res.QuarantinedOnPeer; quarantineThreshold > 0 && quarantined > quarantineThreshold {
		return peerAlertFailure, "warn", fmt.Sprintf("peer sync on %s quarantined %d deletion(s) (threshold %d, stamp %s)",
			machine, quarantined, quarantineThreshold, res.ConflictStamp)
	}
	return peerAlertRecovery, "info", fmt.Sprintf("peer sync on %s recovered: baseline committed", machine)
}

// reportPeerSyncAlert routes a finished run's outcome through the watchdog
// notifier's transition/rate-limit alerter (#244). It is strictly additive:
// it fires only when watchdog.notify.telegram is enabled, never under
// --dry-run, and an alert failure is a stderr warning — the sync's exit code
// and semantics never change. Interactive runs report too; the alerter's
// ok→fail dedup is what keeps that from spamming. Config comes from the live
// profile (not the watchdog snapshot), so telegram knob changes take effect
// on the next run without re-running `dot watchdog setup`. When credentials
// are not configured the report is skipped before any state write, so an
// episode is never recorded as delivered-or-cleared on a host that cannot
// send. The episode state lives beside the peer baseline in the profile's
// store dir, serialized by a PID lock because the run lock is already
// released by the time the outcome is reported.
func reportPeerSyncAlert(ctx context.Context, cmd *cobra.Command, bs *syncer.BootstrapResult, res *syncer.PeerSyncResult, runErr error, dryRun, twoWay bool) {
	p := printerFrom(cmd)
	if dryRun {
		return
	}
	wcfg, err := loadWatchdogConfig(cmd)
	if err != nil {
		p.Warn("peer alert skipped: %v", err)
		return
	}
	if !wcfg.Notify.Telegram.Enabled {
		return
	}
	settings := watchdog.ResolveNotify(wcfg.Notify, homeFor(cmd))
	if _, _, ok, err := watchdog.LoadTelegramEnv(settings.Telegram.EnvPath); err != nil {
		p.Warn("peer alert skipped: %v", err)
		return
	} else if !ok {
		return
	}
	release, err := fileutil.AcquirePIDLock(filepath.Join(bs.Config.ConfigDir, "alert.lock"), fileutil.LockOptions{Label: "another peer alert is reporting"})
	if errors.Is(err, fileutil.ErrLockHeld) {
		// A concurrent run is reporting right now; the next run reconciles
		// the episode from the persisted state.
		return
	}
	if err != nil {
		p.Warn("peer alert skipped: %v", err)
		return
	}
	defer release()
	alerter := &watchdog.Alerter{
		Notifier:         watchdog.NewNotifier(settings, watchdogRunner(false), runtime.GOOS),
		StatePath:        filepath.Join(bs.Config.ConfigDir, "alert-state.json"),
		ReminderInterval: settings.Telegram.ReminderInterval,
	}
	kind, level, msg := classifyPeerOutcome(res, runErr, twoWay, settings.Telegram.QuarantineThreshold, syncer.PreferredMachineName())
	var aerr error
	switch kind {
	case peerAlertFailure:
		aerr = alerter.ReportFailure(ctx, level, msg)
	case peerAlertRecovery:
		aerr = alerter.ReportSuccess(ctx, msg)
	}
	if aerr != nil {
		p.Warn("peer alert failed: %v", aerr)
	}
}
