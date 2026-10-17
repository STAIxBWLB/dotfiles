package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/syncer"
)

const peerStatusSchemaVersion = syncer.PeerStatusSchemaVersion
const peerSchedulerUnsupportedState = "unsupported: peer scheduler requires macOS launchd"

type peerSchedulerSnapshot struct {
	Label           string
	State           string
	IntervalSeconds int
	LastExitCode    *int
	RunCount        *int
}

type peerStatusJSON struct {
	SchemaVersion int            `json:"schemaVersion"`
	Kind          string         `json:"kind"`
	Profile       syncStatusJSON `json:"profile"`
	Job           syncJobJSON    `json:"job"`
	LastExitCode  *int           `json:"lastExitCode"`
	RunCount      *int           `json:"runCount"`
	LastHeldAt    *string        `json:"lastHeldAt"`
	HomePathsPath string         `json:"homePathsPath"`
	// Worktrees lists the linked-worktree roots detected in the workspace.
	// Optional and omitted when empty: the schema stays at version 1 and a
	// peer on a previous release simply sends nothing, which decodes as the
	// empty list the sticky union already tolerates.
	Worktrees []string `json:"worktrees,omitempty"`
	// OwnerEpoch and FencePending carry the coordinator-transition state the
	// fence compares on first contact. Optional: a peer on a previous release
	// sends neither, which the fence reads as "no epoch support" and answers
	// with the pre-epoch owner-mismatch refusal (AC8).
	OwnerEpoch   int  `json:"ownerEpoch,omitempty"`
	FencePending bool `json:"fencePending,omitempty"`
	// DotVersion names this binary so a newer peer can word its
	// feature-skipped messages after the version that lacks the feature.
	DotVersion string `json:"dotVersion,omitempty"`
	// Role is "coordinator" when this machine passes the peer profile's
	// owner guard, "peer" otherwise (#182).
	Role string `json:"role,omitempty"`
}

// peerRole names this machine's part in the peer pair.
func peerRole(cfg *syncer.Config) string {
	if syncer.IsPeerCoordinator(cfg) {
		return "coordinator"
	}
	return "peer"
}

func newPeerStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "status",
		Short:        "Show local peer profile and scheduler status",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE:         runPeerStatus,
	}
	cmd.Flags().Bool("json", false, "print a stable machine-readable status document")
	return cmd
}

// peerBootstrapReadOnly resolves the peer profile without creating its store.
// The runner is deliberately live: reading status must work under --dry-run.
//
// It takes the command because it must read --home: this is the path
// `dot peer status --json` and `dot peer home-paths get` take, and neither
// goes through peerBootstrapOptions (BUG-07).
func peerBootstrapReadOnly(cmd *cobra.Command) (*syncer.BootstrapResult, error) {
	return syncer.Bootstrap(syncer.BootstrapOptions{
		Profile:  PeerProfile,
		ReadOnly: true,
		Home:     homeOverrideFrom(cmd),
	})
}

func runPeerStatus(cmd *cobra.Command, _ []string) error {
	bs, err := peerBootstrapReadOnly(cmd)
	if err != nil {
		return err
	}
	state, cfg, runner := bs.State, bs.Config, bs.Runner
	st, err := syncer.GetStatus(cmd.Context(), runner, cfg, state, nil)
	if err != nil {
		return err
	}
	snapshot := inspectPeerScheduler(cmd.Context(), runner, homeFor(cmd), homeOverrideFrom(cmd) != "", runtime.GOOS)
	base := buildSyncStatusJSON(cfg, st, &syncer.Scheduler{Paths: cfg.SystemPaths})
	base.Kind = "peer-profile"
	base.Jobs = []syncJobJSON{}
	job := syncJobJSON{
		ID:              "peer-sync",
		Action:          "peer-sync",
		Label:           snapshot.Label,
		IntervalSeconds: snapshot.IntervalSeconds,
		Mode:            "safe-bidirectional",
		State:           snapshot.State,
		LastRunAt:       newestTimeJSON(st.LastPull, st.LastPush),
		LastExitCode:    snapshot.LastExitCode,
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		// The status document feeds the remote side's sticky worktree union.
		// A detection failure must fail closed: an under-reported list lets
		// the peer sync a real worktree as regular files (the #135 husk
		// class), so refuse rather than emit an incomplete document.
		worktrees, err := syncer.DetectPeerWorktrees(cfg, st.LocalPath)
		if err != nil {
			return fmt.Errorf("linked-worktree detection failed; refusing to report an incomplete worktree list: %w", err)
		}
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(peerStatusJSON{
			SchemaVersion: peerStatusSchemaVersion,
			Kind:          "peer",
			Profile:       base,
			Job:           job,
			LastExitCode:  snapshot.LastExitCode,
			RunCount:      snapshot.RunCount,
			LastHeldAt:    timeJSON(st.LastHeld),
			HomePathsPath: syncer.PeerHomePathsFile(cfg.LocalPaths),
			Worktrees:     worktrees,
			OwnerEpoch:    cfg.OwnerEpoch,
			FencePending:  cfg.FencePending,
			DotVersion:    cmd.Root().Version,
			Role:          peerRole(cfg),
		})
	}
	p := printerFrom(cmd)
	p.Header("Peer Status")
	p.KV("Workspace", st.LocalPath)
	if worktrees, _ := syncer.DetectLinkedWorktrees(st.LocalPath); len(worktrees) > 0 {
		p.KV("Linked worktrees", strconv.Itoa(len(worktrees))+" (excluded from sync)")
	}
	p.KV("Target", st.Target.String())
	// The peer probes every dot install and uses the newest release; this
	// names the binary answering here, which over ssh may not be the one an
	// interactive shell finds first (#176).
	if exe, err := os.Executable(); err == nil {
		p.KV("Dot", exe+" ("+cmd.Root().Version+")")
	}
	// The owner is a recorded name and this machine answers to live host
	// names; after a Mac rename the two drift apart (#185).
	owner := cfg.Owner
	if owner == "" {
		owner = "(unset)"
	}
	if len(cfg.OwnerAliases) > 0 {
		owner += " (aliases: " + strings.Join(cfg.OwnerAliases, ", ") + ")"
	}
	p.KV("Owner", owner)
	p.KV("This machine", strings.Join(syncer.MachineNames(), ", "))
	// The role decides how the rest reads: runs are recorded on the machine
	// that made them, so the peer's timestamps are its time as coordinator,
	// not stale coordinator activity (#182).
	role := peerRole(cfg)
	switch {
	case role == "coordinator":
		p.KV("Role", "coordinator")
	case strings.TrimSpace(cfg.Owner) == "":
		p.KV("Role", "peer; no coordinator set (dot sync owner --profile=peer --set <machine>)")
	default:
		p.KV("Role", "peer; syncs run on the coordinator "+cfg.Owner)
	}
	scheduler := snapshot.State
	if role == "peer" && snapshot.State == syncer.SchedulerNotInstalled.String() {
		scheduler += " (expected on the peer)"
	}
	p.KV("Scheduler", scheduler)
	if snapshot.IntervalSeconds > 0 {
		p.KV("Interval", formatInterval(snapshot.IntervalSeconds))
	}
	if snapshot.LastExitCode != nil {
		p.KV("Last exit", strconv.Itoa(*snapshot.LastExitCode))
	}
	if role == "coordinator" {
		p.KV("Last pull", formatLastSync(st.LastPull))
		p.KV("Last push", formatLastSync(st.LastPush))
	} else {
		last := st.LastPull
		if st.LastPush.After(last) {
			last = st.LastPush
		}
		if last.IsZero() {
			p.KV("Last run here", formatLastSync(last))
		} else {
			p.KV("Last run here", formatLastSync(last)+" (while this Mac was the coordinator)")
		}
	}
	if !st.LastHeld.IsZero() {
		// A held run transferred files but left deletions pending, so the
		// timestamps above must not be read as a clean exchange.
		held := formatLastSync(st.LastHeld)
		if role != "coordinator" {
			held += " (while this Mac was the coordinator)"
		}
		p.KV("Held transitions", held)
	}
	p.KV("Conflicts", strconv.Itoa(len(st.Conflicts)))
	if st.ConflictsError != "" {
		p.Warn("conflict backups not listed: %s", st.ConflictsError)
	}
	return nil
}

func newestTimeJSON(values ...time.Time) *string {
	var newest time.Time
	for _, value := range values {
		if value.After(newest) {
			newest = value
		}
	}
	return timeJSON(newest)
}

// inspectPeerScheduler reads the peer agent's plist under the home the run is
// pointed at. It takes the home rather than resolving one: a --home run that
// reported the invoking user's agent state would be the same disclosure the
// workspace fields above carry (BUG-07).
func inspectPeerScheduler(ctx context.Context, runner *exec.Runner, home string, targetUserDomain bool, goos string) peerSchedulerSnapshot {
	const label = "com.dotfiles.peer"
	snapshot := peerSchedulerSnapshot{Label: label, State: syncer.SchedulerNotInstalled.String()}
	if goos != "darwin" {
		snapshot.State = peerSchedulerUnsupportedState
		return snapshot
	}
	if home == "" {
		snapshot.State = "unknown: no home directory"
		return snapshot
	}
	plist := filepath.Join(home, "Library", "LaunchAgents", label+".plist")
	body, err := os.ReadFile(plist)
	if err != nil {
		// Only a missing plist is "not installed": an owner rename trusts
		// that answer from the other Mac.
		if !os.IsNotExist(err) {
			snapshot.State = "unknown: " + err.Error()
		}
		return snapshot
	}
	snapshot.State = syncer.SchedulerStopped.String()
	snapshot.IntervalSeconds = plistInteger(string(body), "StartInterval")
	if targetUserDomain {
		snapshot.State = syncer.SchedulerTargetUserActionRequired.String()
		return snapshot
	}
	result, err := runner.RunQuery(ctx, "launchctl", "print", fmt.Sprintf("gui/%d/%s", os.Getuid(), label))
	if err != nil || result == nil || result.ExitCode != 0 {
		return snapshot
	}
	snapshot.State = syncer.SchedulerRunning.String()
	if syncer.LaunchdSpawnFailed(result.Stdout) {
		// launchd holds the job but cannot spawn it (seen after a Homebrew
		// upgrade replaced the binary, #233); `dot peer setup` bootouts and
		// re-bootstraps com.dotfiles.peer, which clears the state.
		snapshot.State = "spawn failed; reload the job with `dot peer setup`"
	}
	snapshot.LastExitCode = syncer.LaunchdLastExitCode(result.Stdout)
	for _, line := range strings.Split(result.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if raw, ok := strings.CutPrefix(line, "runs ="); ok {
			if value, parseErr := strconv.Atoi(strings.TrimSpace(raw)); parseErr == nil {
				snapshot.RunCount = &value
			}
		}
	}
	return snapshot
}

func plistInteger(body, key string) int {
	marker := "<key>" + key + "</key>"
	index := strings.Index(body, marker)
	if index < 0 {
		return 0
	}
	rest := body[index+len(marker):]
	start := strings.Index(rest, "<integer>")
	end := strings.Index(rest, "</integer>")
	if start < 0 || end < 0 || end <= start {
		return 0
	}
	value, _ := strconv.Atoi(strings.TrimSpace(rest[start+len("<integer>") : end]))
	return value
}
