package cli

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/entelecheia/dotfiles-v2/internal/aisettings"
	"github.com/entelecheia/dotfiles-v2/internal/exec"
	"github.com/entelecheia/dotfiles-v2/internal/syncer"
)

// dot ai memory sync — cross-machine claude-mem replication over ssh.
// Status, dry-run preview, and count rendering live in
// ai_memory_sync_status.go.

func newAIMemorySyncCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "sync [push|pull|sync|sessions|status]",
		Short: "Replicate the claude-mem store with a peer over ssh",
		Long: `Replicate observations, session summaries, and their sdk_sessions rows
with a peer Mac. Incremental by default (receiver's MAX(created_at) minus a
7-day overlap); --full re-offers everything and dedupe absorbs it. The peer
needs only the dot binary: the transport is
'ssh <target> dot ai memory sync --serve <op>' with JSON over stdio.

sessions replicates all sessions both ways. status prints both sides'
counts plus the last scheduled run. With no action, sync runs both
directions. --peer defaults to the configured dot peer target.`,
		Args:         cobra.MaximumNArgs(1),
		RunE:         runAIMemorySync,
		SilenceUsage: true,
	}
	c.Flags().String("peer", "", "ssh target to sync with (default: the dot peer target)")
	c.Flags().Bool("full", false, "disable the incremental cutoff (re-offer all rows)")
	c.Flags().String("remote-db", "", "DB path on the peer (default: the peer's ~/.claude-mem/claude-mem.db)")
	c.Flags().String("serve", "", "run one serve op against the local DB (used by the ssh transport)")
	_ = c.Flags().MarkHidden("serve")
	return c
}

func runAIMemorySync(cmd *cobra.Command, args []string) error {
	if op, _ := cmd.Flags().GetString("serve"); op != "" {
		dbPath := memorySyncDBPath(cmd)
		return aisettings.ServeOp(cmd.Context(), dbPath, homeFor(cmd), op, cmd.InOrStdin(), cmd.OutOrStdout())
	}

	action := "sync"
	if len(args) > 0 {
		action = args[0]
	}
	switch action {
	case "push", "pull", "sync", "sessions", "status":
	default:
		return fmt.Errorf("unknown sync action %q (want push, pull, sync, sessions, or status)", action)
	}

	peer, err := memorySyncPeer(cmd)
	if err != nil {
		return err
	}
	home := homeFor(cmd)
	if action == "status" {
		return printMemorySyncStatus(printerFrom(cmd), cmd, home, peer)
	}

	scheduled := os.Getenv(scheduledRunEnv) == "1"
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	p := printerFrom(cmd)

	// An unreachable peer on a scheduled run is a skipped run, exit 0 — the
	// LaunchAgent pre-checks too; this covers direct scheduled invocations.
	probe := aisettingsProbeRunner()
	if err := syncer.CheckSSH(cmd.Context(), probe, peer.Target); err != nil {
		if scheduled {
			return nil
		}
		return fmt.Errorf("peer %s is unreachable: %w", peer.Target, err)
	}

	// The scheduled pass keeps both Macs on the latest claude-mem first, so
	// the pair converges on one plugin version (best-effort; a failed update
	// never blocks the sync itself).
	if scheduled {
		mgr, merr := newClaudeMemManagerFromCmd(cmd)
		if merr == nil {
			runner := memorySyncRunner(dryRun)
			if _, uerr := mgr.UpdateClaudeMemPlugin(cmd.Context(), runner, scheduled); uerr != nil {
				p.Warn("claude-mem update check failed (continuing with sync): %v", uerr)
			}
		}
	}

	db, err := aisettings.OpenSyncDB(aisettings.DefaultSyncDBPath(home))
	if err != nil {
		return err
	}
	defer db.Close()

	full, _ := cmd.Flags().GetBool("full")
	opts := aisettings.SyncOptions{Full: full, Direction: action, OnlySessions: action == "sessions"}
	if dryRun {
		return printMemorySyncDryRun(cmd, db, peer, opts)
	}
	report, err := aisettings.RunMemorySync(cmd.Context(), db, &aisettings.SSHTransport{}, peer, opts, time.Now())
	if err != nil {
		return err
	}

	// Backfill kick on this side when a pull added content rows (the serve
	// side kicks its own worker after a push).
	if report.Pulled.NeedsBackfill() {
		_ = aisettings.KickWorkerRestart(cmd.Context(), home, nil)
	}
	if err := aisettings.SaveSyncState(aisettings.SyncStatePath(home), peer.Target, aisettings.SyncStateEntry{
		LastRun:    time.Now(),
		LastResult: report.Summarize(),
	}); err != nil {
		p.Warn("could not persist sync state: %v", err)
	}
	auditAIEventBestEffort(cmd, "ai.memory.sync", map[string]any{
		"peer": peer.Target, "direction": opts.Direction, "report": report,
	})

	p.Header("claude-mem sync")
	p.KV("Peer", peer.Target)
	p.KV("Action", action)
	p.Line("  %s", report.Summarize())
	printMemorySyncCounts(p, "Local", report.LocalCounts)
	printMemorySyncCounts(p, "Peer", report.PeerCounts)
	logSkippedColumns(p, report)
	p.Success("✓ sync complete")
	return nil
}

func logSkippedColumns(p *Printer, report *aisettings.SyncReport) {
	seen := map[string]bool{}
	for _, res := range []*aisettings.ImportResult{report.Pushed, report.Pulled} {
		if res == nil {
			continue
		}
		for _, col := range res.Skipped {
			if !seen[col] {
				seen[col] = true
				p.Warn("skipped column %q (not present on receiver)", col)
			}
		}
	}
}

// memorySyncPeer resolves --peer, falling back to the configured dot peer
// target (read-only profile resolution). A target that is the peer
// profile's host runs the dot its remote_dot pins, as dot peer does.
func memorySyncPeer(cmd *cobra.Command) (aisettings.SyncPeer, error) {
	target, _ := cmd.Flags().GetString("peer")
	var profile *syncer.Config
	bs, err := syncer.Bootstrap(syncer.BootstrapOptions{Profile: PeerProfile, Home: homeOverrideFrom(cmd), ReadOnly: true})
	switch {
	case err == nil && bs.Config.Target.IsSSH():
		profile = bs.Config
	case target == "" && err != nil:
		return aisettings.SyncPeer{}, err
	}
	if target == "" {
		if profile == nil {
			return aisettings.SyncPeer{}, fmt.Errorf("no --peer given and dot peer is not configured; pass --peer <ssh-target> or run: dot peer init --host <user@host>")
		}
		target = profile.Target.Host
	}
	if err := validateSyncPeerTarget(target); err != nil {
		return aisettings.SyncPeer{}, err
	}
	remoteDB, _ := cmd.Flags().GetString("remote-db")
	peer := aisettings.SyncPeer{Target: target, RemoteDB: remoteDB}
	if profile != nil && target == profile.Target.Host {
		peer.RemoteDot = profile.RemoteDot
	}
	return peer, nil
}

// validateSyncPeerTarget rejects targets that ssh would read as its own
// options or that break the remote command line: the value goes straight to
// `ssh <target>` and a leading '-' can become a ProxyCommand injection.
func validateSyncPeerTarget(target string) error {
	if target == "" {
		return fmt.Errorf("sync peer target is empty")
	}
	if strings.HasPrefix(target, "-") {
		return fmt.Errorf("sync peer target %q must not start with '-' (ssh would parse it as an option)", target)
	}
	for _, r := range target {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return fmt.Errorf("sync peer target %q must not contain whitespace or control characters", target)
		}
	}
	return nil
}

func memorySyncDBPath(cmd *cobra.Command) string {
	if remote, _ := cmd.Flags().GetString("remote-db"); remote != "" {
		return remote
	}
	return aisettings.DefaultSyncDBPath(homeFor(cmd))
}

// aisettingsProbeRunner is the always-live probe runner for reachability
// checks (ssh probes must run even under --dry-run).
func aisettingsProbeRunner() *exec.Runner {
	return exec.NewProbeRunner()
}

func memorySyncRunner(dryRun bool) *exec.Runner {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return exec.NewRunner(dryRun, logger)
}
