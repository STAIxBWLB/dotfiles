package cli

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/aisettings"
	"github.com/STAIxBWLB/dotfiles/internal/syncer"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

// dot ai memory sync — status, dry-run preview, and count rendering.
// The command and transport live in ai_memory_sync_cmd.go.

func printMemorySyncDryRun(cmd *cobra.Command, db *aisettings.SyncDB, peer aisettings.SyncPeer, opts aisettings.SyncOptions) error {
	p := printerFrom(cmd)
	t := &aisettings.SSHTransport{}
	p.Header("claude-mem sync (dry run)")
	p.KV("Peer", peer.Target)
	preview := func(label string, maxes map[string]string, export func(context.Context, map[string]string, bool) (*aisettings.Bundle, error)) error {
		cutoffs, err := memorySyncCutoffsForPreview(maxes, opts)
		if err != nil {
			return err
		}
		bundle, err := export(cmd.Context(), cutoffs, opts.OnlySessions)
		if err != nil {
			return err
		}
		p.Line("  %s: %d observations, %d summaries, %d sessions", label, len(bundle.Observations), len(bundle.Summaries), len(bundle.Sessions))
		return nil
	}
	// Preview the requested direction, not always the push: a pull dry-run
	// exports the PEER against the local maxima. sessions-mode runs both.
	if opts.OnlySessions || opts.Direction == "push" || opts.Direction == "sync" {
		maxes, err := t.Max(cmd.Context(), peer)
		if err != nil {
			return err
		}
		if err := preview("would push (local export)", maxes, db.Export); err != nil {
			return err
		}
	}
	if opts.OnlySessions || opts.Direction == "pull" || opts.Direction == "sync" {
		maxes, err := db.MaxCreatedAt(cmd.Context())
		if err != nil {
			return err
		}
		if err := preview("would pull (peer export)", maxes, func(ctx context.Context, cutoffs map[string]string, onlySessions bool) (*aisettings.Bundle, error) {
			return t.Export(ctx, peer, cutoffs, onlySessions)
		}); err != nil {
			return err
		}
	}
	return nil
}

func memorySyncCutoffsForPreview(maxes map[string]string, opts aisettings.SyncOptions) (map[string]string, error) {
	if opts.Full || opts.OnlySessions {
		return nil, nil
	}
	out := map[string]string{}
	for _, table := range []string{"observations", "session_summaries"} {
		cutoff, err := aisettings.CutoffFor(maxes[table], time.Now())
		if err != nil {
			return nil, err
		}
		out[table] = cutoff
	}
	return out, nil
}

// printMemorySyncStatus renders both the `sync status` action and the
// "peer sync" section of `dot ai memory status`.
func printMemorySyncStatus(p *Printer, cmd *cobra.Command, home string, peer aisettings.SyncPeer) error {
	p.Header("claude-mem peer sync")
	mgr, err := newClaudeMemManagerFromCmd(cmd)
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(mgr.SyncLaunchdPlistPath()); statErr == nil {
		p.KV("Agent", mgr.SyncLaunchdPlistPath())
	} else {
		p.KV("Agent", "not installed — run: dot ai memory install --peer "+peer.Target)
	}
	p.KV("Peer", peer.Target)
	if peers, err := aisettings.LoadSyncState(aisettings.SyncStatePath(home)); err == nil {
		if entry, ok := peers[peer.Target]; ok {
			p.KV("Last run", entry.LastRun.Local().Format("2006-01-02 15:04:05"))
			p.KV("Last result", entry.LastResult)
		} else {
			p.KV("Last run", "(never)")
		}
	}
	db, err := aisettings.OpenSyncDBReadOnly(aisettings.DefaultSyncDBPath(home))
	if err != nil {
		return err
	}
	defer db.Close()
	local, err := db.Counts(cmd.Context())
	if err != nil {
		return err
	}
	printMemorySyncCounts(p, "Local", local)
	counts, err := (&aisettings.SSHTransport{}).Counts(cmd.Context(), peer)
	if err != nil {
		p.KV("Peer counts", "unreachable: "+err.Error())
		return nil
	}
	printMemorySyncCounts(p, "Peer", counts)
	return nil
}

// printMemorySyncStatusSection renders the compact "Peer sync" section of
// `dot ai memory status`: agent, peer, last run/result, both sides' counts.
func printMemorySyncStatusSection(p *Printer, cmd *cobra.Command, mgr *aisettings.ClaudeMemManager) {
	p.Section("Peer sync")
	hint := ui.StyleHint.Render(ui.MarkPartial)
	installed := false
	if _, err := os.Stat(mgr.SyncLaunchdPlistPath()); err == nil {
		installed = true
	}
	switch {
	case !installed:
		p.Bullet(hint, "sync agent not installed — run: dot ai memory install --peer <target>")
	case memorySyncAgentLoaded(cmd):
		p.Bullet(ui.StyleSuccess.Render(ui.MarkPresent), "sync agent installed and loaded (hourly)")
	default:
		// A plist on disk says nothing about launchd state: an unloaded or
		// crashed agent must not render as a running one.
		p.Bullet(hint, "sync agent installed but not loaded — rerun: dot ai memory install --peer <target>")
	}
	home := mgr.HomeDir
	target, remoteDot := "", ""
	if bs, err := syncer.Bootstrap(syncer.BootstrapOptions{Profile: PeerProfile, Home: homeOverrideFrom(cmd), ReadOnly: true}); err == nil && bs.Config.Target.IsSSH() {
		target, remoteDot = bs.Config.Target.Host, bs.Config.RemoteDot
	}
	if target == "" {
		if recorded := memorySyncPeersFromState(home); len(recorded) > 0 {
			target = recorded[0]
		}
	}
	if target == "" {
		p.Bullet(hint, "no peer configured (dot peer init or --peer)")
		return
	}
	peers, _ := aisettings.LoadSyncState(aisettings.SyncStatePath(home))
	line := "peer " + target
	if entry, ok := peers[target]; ok {
		if entry.LastRun.IsZero() {
			line += " · " + entry.LastResult
		} else {
			line += " · last run " + entry.LastRun.Local().Format("2006-01-02 15:04") + " · " + entry.LastResult
		}
	} else {
		line += " · never synced"
	}
	p.Bullet(hint, line)
	db, err := aisettings.OpenSyncDBReadOnly(aisettings.DefaultSyncDBPath(home))
	if err != nil {
		return
	}
	defer db.Close()
	if local, err := db.Counts(cmd.Context()); err == nil {
		p.Bullet(hint, syncCountsLine("local", local))
	}
	counts, err := (&aisettings.SSHTransport{}).Counts(cmd.Context(), aisettings.SyncPeer{Target: target, RemoteDot: remoteDot})
	if err != nil {
		p.Bullet(hint, "peer counts unreachable")
		return
	}
	p.Bullet(hint, syncCountsLine("peer", counts))
}

// memorySyncAgentLoaded probes launchd for the sync agent's live state.
// Non-darwin or a failed print reports not-loaded; it never errors status.
func memorySyncAgentLoaded(cmd *cobra.Command) bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	target := fmt.Sprintf("gui/%d/%s", os.Getuid(), aisettings.ClaudeMemSyncLaunchdLabel)
	result, err := aisettingsProbeRunner().RunQuery(cmd.Context(), "launchctl", "print", target)
	return err == nil && result != nil && result.ExitCode == 0
}

func syncCountsLine(label string, c *aisettings.TableCounts) string {
	return fmt.Sprintf("%s: obs %d · sums %d · sessions %d · obs-without-session %d",
		label, c.Obs, c.Sums, c.Sessions, c.ObsWithoutSession)
}

func printMemorySyncCounts(p *Printer, label string, c *aisettings.TableCounts) {
	if c == nil {
		return
	}
	p.KV(label, fmt.Sprintf("obs %d · sums %d · sessions %d · obs-without-session %d",
		c.Obs, c.Sums, c.Sessions, c.ObsWithoutSession))
}

// memorySyncPeersFromState lists recorded peers for the status section when
// no dot peer target is configured.
func memorySyncPeersFromState(home string) []string {
	peers, err := aisettings.LoadSyncState(aisettings.SyncStatePath(home))
	if err != nil || len(peers) == 0 {
		return nil
	}
	out := make([]string, 0, len(peers))
	for peer := range peers {
		out = append(out, peer)
	}
	sort.Strings(out)
	return out
}
