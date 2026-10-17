package syncer

import (
	"context"
	"fmt"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/template"
)

// SyncEventKind names one observable step of a `dot sync` run. The engine
// emits kinds; cli owns the wording (D-06/D-10).
//
// An `Out io.Writer` was rejected here for the fifth consecutive slice: a
// single writer cannot reproduce the stdout/stderr split of p.Line versus
// p.Warn, and the styled lines would drag the terminal-styling package into
// this one.
type SyncEventKind int

const (
	SyncEventDryRunNotice    SyncEventKind = iota // the run will not write
	SyncEventPushSSHStart                         // direct rsync push to an ssh target begins
	SyncEventPushPlanStart                        // the local push plan header
	SyncEventPushPlanReady                        // PushPlan carries the computed plan
	SyncEventPullSSHStart                         // direct rsync pull from an ssh target begins
	SyncEventPullPlanStart                        // the baseline-tracked pull plan header
	SyncEventPullPlanReady                        // PullResult carries the planned pull
	SyncEventIntakeStart                          // the intake staging pass begins
	SyncEventFetchMissing                         // Path: a path the target does not have
	SyncEventPartialTransfer                      // rsync moved some but not all; Err carries it
	SyncEventPruneSummary                         // Candidates/Reclaimed carry the prune totals
	SyncEventLeftoversMoved                       // Candidates mirror leftovers moved into Path
	SyncEventFetchExcluded                        // Path: a requested path a filter layer keeps out
)

// SyncEvent is one step outcome. Only the fields its kind documents are set.
// Everything derivable from the resolved Config is deliberately absent: the
// caller already holds the Config and reads those values off it directly.
type SyncEvent struct {
	Kind       SyncEventKind
	Path       string
	Err        error
	PushPlan   *PushPlan
	PullResult *PullResult
	Candidates int
	Reclaimed  int64
}

func emitSync(progress func(SyncEvent), e SyncEvent) {
	if progress != nil {
		progress(e)
	}
}

// ConfirmKind names a decision the engine needs from the operator. cli owns
// the wording and the `--yes` policy (D-09); the engine only says which
// decision is pending.
type ConfirmKind int

const (
	ConfirmPushSSH        ConfirmKind = iota // send to an ssh target without a plan preview
	ConfirmPushPlan                          // apply the printed push plan
	ConfirmPullPlan                          // apply the printed pull plan
	ConfirmPruneConflicts                    // remove the listed backup directories
	ConfirmInstallRsync                      // install the missing rsync binary
)

// ConfirmRequest is one pending decision. Candidates and Reclaimed are set
// only for ConfirmPruneConflicts, whose prompt quotes both numbers.
type ConfirmRequest struct {
	Kind       ConfirmKind
	Candidates int
	Reclaimed  int64
}

// ConfirmFunc answers a ConfirmRequest. A nil ConfirmFunc declines, which is
// the safe reading for an engine caller that never wired a prompt.
type ConfirmFunc func(ConfirmRequest) (bool, error)

func askSync(confirm ConfirmFunc, req ConfirmRequest) (bool, error) {
	if confirm == nil {
		return false, nil
	}
	return confirm(req)
}

// PreflightBlockKind names why a sync run may not proceed.
type PreflightBlockKind int

const (
	PreflightRsyncMissing   PreflightBlockKind = iota // rsync is not on PATH
	PreflightLocalMissing                             // Path: the missing workspace tree
	PreflightSSHUnreachable                           // Err: why the ssh target did not answer
	PreflightMirrorMissing                            // Path: the missing mirror tree
	PreflightPaused                                   // the paused gate is set
)

// PreflightBlock is the reason a run stopped before doing anything. Only the
// fields its kind documents are set.
type PreflightBlock struct {
	Kind PreflightBlockKind
	Path string
	Err  error
}

// Reason is the line a command prints for the block.
func (b *PreflightBlock) Reason() string {
	switch b.Kind {
	case PreflightRsyncMissing:
		return "rsync not installed. Install via: brew install rsync"
	case PreflightLocalMissing:
		return "Local path missing: " + b.Path
	case PreflightSSHUnreachable:
		return fmt.Sprintf("SSH target unreachable: %v", b.Err)
	case PreflightMirrorMissing:
		return "Mirror path missing: " + b.Path
	case PreflightPaused:
		return "sync is paused. Run `dot sync resume` to activate."
	}
	return "preflight refused"
}

// Preflight validates that sync can proceed. A nil return means it can.
//
// The caller decides where this sits relative to its other guards: the order
// of the refusals is what fixes each command's error precedence, so the call
// site stays in cli even though the classification is the engine's.
func Preflight(runner *exec.Runner, cfg *Config) *PreflightBlock {
	if !runner.CommandExists(cfg.rsyncBin()) {
		return &PreflightBlock{Kind: PreflightRsyncMissing}
	}
	if !runner.IsDir(cfg.LocalPath) {
		return &PreflightBlock{Kind: PreflightLocalMissing, Path: cfg.LocalPath}
	}
	if cfg.Target.IsSSH() {
		if err := CheckSSH(context.Background(), runner, cfg.Target.Host); err != nil {
			return &PreflightBlock{Kind: PreflightSSHUnreachable, Err: err}
		}
	} else if !runner.IsDir(cfg.MirrorPath) {
		return &PreflightBlock{Kind: PreflightMirrorMissing, Path: cfg.MirrorPath}
	}
	if cfg.Paused {
		return &PreflightBlock{Kind: PreflightPaused}
	}
	return nil
}

// RecordResult updates the on-disk log after a sync operation. Runtime
// timestamps now live in the workspace-local gsync state file.
func RecordResult(state *config.UserState, cfg *Config, op string, syncErr error, dryRun bool) {
	_ = state
	if dryRun {
		return
	}
	exitCode := 0
	if syncErr != nil {
		exitCode = 1
	}
	AppendLog(cfg.LogFile, op, exitCode)
	// The two rotation bounds were already declared for this call in sync.go;
	// the pre-move cli body repeated them as literals of the same value.
	RotateLog(cfg.LogFile, logRotateMaxLines, logRotateKeepLines)
}

// ResolveScheduler builds a Scheduler bound to the same runner+cfg used
// elsewhere in the gsync subcommands. Returns the Paths used so callers can
// introspect plist/timer locations. The unit lands under the home the config
// was resolved for: a unit written into the invoking user's LaunchAgents for
// another user's workspace fires forever against the wrong tree. It reads
// cfg.SystemPaths rather than resolving its own, so the home and the profile
// are both the ones resolveConfig saw; an unresolved config is an error here,
// not a silent re-resolution from half the inputs.
func ResolveScheduler(cfg *Config, runner *exec.Runner) (*Scheduler, *Paths, error) {
	if cfg.SystemPaths == nil {
		return nil, nil, fmt.Errorf("system paths unresolved")
	}
	paths := cfg.SystemPaths
	// ponytail: known ceiling. A scheduler installed by an older binary for a
	// non-default profile remains at the old default path and can keep firing
	// beside the corrected unit. Cleanup must enumerate unknown historical
	// profiles without violating dry-run semantics.
	// See docs/CEILINGS.md (stale per-profile scheduler units)
	return NewScheduler(runner, paths, cfg, template.NewEngine()), paths, nil
}

// RejectGenericPeerProfile refuses a generic sync entrypoint on the peer
// profile.
//
// The peer profile has a different transaction: compute tombstones, protect
// the pull, quarantine remote deletions, then push. Generic sync entrypoints
// skip that transaction and can retire or restore the only deletion evidence.
func RejectGenericPeerProfile(cfg *Config) error {
	if cfg != nil && cfg.Profile == PeerProfile {
		return fmt.Errorf("the peer profile must use `dot peer sync` and `dot peer setup`; generic sync push, pull, setup, pause, and resume bypass peer tombstones")
	}
	return nil
}
