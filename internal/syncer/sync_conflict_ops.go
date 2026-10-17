package syncer

import (
	"context"
	"fmt"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// ConflictTrees returns the (label, root) pairs that accumulate
// .sync-conflicts/ backups: pull backups land in the workspace tree,
// push backups land in the mirror tree.
func ConflictTrees(cfg *Config) [][2]string {
	trees := [][2]string{{"workspace", trimTrailingSlash(cfg.LocalPath)}}
	// SSH targets have no local mirror tree: ResolveConfig intentionally leaves
	// MirrorPath empty for them. Never turn that empty value into "/" and scan
	// the invoking machine's root; remote conflicts are handled below through
	// cfg.Target.Path.
	if !cfg.Target.IsSSH() {
		trees = append(trees, [2]string{"mirror", trimTrailingSlash(cfg.MirrorPath)})
	}
	return trees
}

// RemoteConflictTrees returns the (label, root) pairs that live on an ssh
// target. A local target has none.
func RemoteConflictTrees(cfg *Config) ([][2]string, error) {
	if !cfg.Target.IsSSH() {
		return nil, nil
	}
	root, err := RemoteTargetConflictRoot(cfg.Target)
	if err != nil {
		return nil, err
	}
	trees := [][2]string{{"remote target", root}}
	if cfg.Profile == PeerProfile {
		trees = append(trees, [2]string{"remote home", PeerHomeConflictRoot})
	}
	return trees, nil
}

// ResolvePruneCutoff turns the prune flags into a cutoff time. olderChanged
// reports whether --older-than was set explicitly, so it can be rejected in
// combination with --all.
func ResolvePruneCutoff(olderDays int, all, olderChanged bool) (time.Time, error) {
	if all && olderChanged {
		return time.Time{}, fmt.Errorf("--all and --older-than are mutually exclusive")
	}
	if olderDays < 0 {
		return time.Time{}, fmt.Errorf("--older-than must be >= 0 (got %d)", olderDays)
	}
	if all {
		return time.Now(), nil
	}
	return time.Now().Add(-time.Duration(olderDays) * 24 * time.Hour), nil
}

// ConflictListing is one tree's backup directories. It is delivered through a
// callback rather than collected into a slice so the caller renders each tree
// as it is walked: the pre-move code printed tree N before listing tree N+1,
// and a failure on the later tree left the earlier output standing.
type ConflictListing struct {
	Label    string
	Root     string
	IsRemote bool
	Entries  []ConflictEntry       // set when IsRemote is false
	Remotes  []RemoteConflictEntry // set when IsRemote is true
}

// ConflictsList walks the local and remote conflict trees, handing each one to
// emit as soon as it is read.
func ConflictsList(ctx context.Context, runner *exec.Runner, cfg *Config, emit func(ConflictListing)) error {
	for _, tree := range ConflictTrees(cfg) {
		confs, err := ListConflicts(tree[1])
		if err != nil {
			return err
		}
		if emit != nil {
			emit(ConflictListing{Label: tree[0], Root: tree[1], Entries: confs})
		}
	}
	remoteTrees, err := RemoteConflictTrees(cfg)
	if err != nil {
		return err
	}
	for _, tree := range remoteTrees {
		confs, err := ListRemoteConflicts(ctx, runner, cfg.Target, tree[1])
		if err != nil {
			return err
		}
		if emit != nil {
			emit(ConflictListing{Label: tree[0], Root: tree[1], IsRemote: true, Remotes: confs})
		}
	}
	return nil
}

// PruneTreeReport is one tree's prune outcome, planned or applied.
//
// Now is the single reference instant the whole plan's ages are measured
// against. It is the engine's rather than the caller's because the pre-move
// code read the clock after planning finished, not before it started.
type PruneTreeReport struct {
	Label    string
	Root     string
	IsRemote bool
	Now      time.Time
	Result   *PruneResult
}

func emitPrune(report func(PruneTreeReport), r PruneTreeReport) {
	if report != nil {
		report(r)
	}
}

// ConflictsPruneOptions controls ConflictsPrune. OnPlanned receives every tree
// with candidates before the confirmation; OnPruned receives every tree that
// actually lost directories.
type ConflictsPruneOptions struct {
	Config     *Config
	Runner     *exec.Runner
	Cutoff     time.Time
	RemoteOnly bool
	DryRun     bool
	Progress   func(SyncEvent)
	OnPlanned  func(PruneTreeReport)
	OnPruned   func(PruneTreeReport)
	Confirm    ConfirmFunc
}

// PruneOutcome names how a prune run ended.
type PruneOutcome int

const (
	PruneLockBusy    PruneOutcome = iota // another run holds the sync lock
	PruneNothingToDo                     // no backup directory matched the cutoff
	PrunePlanned                         // dry-run: the plan was reported, nothing removed
	PruneAborted                         // the operator declined
	PruneDone                            // the selected directories were removed
)

// ConflictsPruneResult is the terminal state of a prune run.
type ConflictsPruneResult struct {
	Outcome PruneOutcome
	LockErr error
}

// ConflictsPrune removes timestamped backup directories older than the cutoff
// from the selected local and remote trees.
//
// The sync lock is held across planning, the confirmation and the removal so
// RemoveAll never interleaves with an rsync pass that is actively writing new
// backups.
func ConflictsPrune(ctx context.Context, opts ConflictsPruneOptions) (*ConflictsPruneResult, error) {
	cfg, runner := opts.Config, opts.Runner

	release, lockErr := AcquireLockForRun(cfg.LockDir, opts.DryRun)
	if lockErr != nil {
		return &ConflictsPruneResult{Outcome: PruneLockBusy, LockErr: lockErr}, nil
	}
	defer release()

	trees := ConflictTrees(cfg)
	if opts.RemoteOnly {
		trees = nil
	}
	plans := make([]*PruneResult, len(trees))
	var candidates int
	var reclaim int64
	for i, tree := range trees {
		plan, err := PruneConflicts(tree[1], opts.Cutoff, true)
		if err != nil {
			return nil, err
		}
		plans[i] = plan
		candidates += len(plan.Pruned)
		reclaim += plan.Reclaimed
	}
	remoteTrees, err := RemoteConflictTrees(cfg)
	if err != nil {
		return nil, err
	}
	remotePlans := make([]*PruneResult, len(remoteTrees))
	for i, tree := range remoteTrees {
		plan, err := PruneRemoteConflicts(ctx, runner, cfg.Target, tree[1], opts.Cutoff, true)
		if err != nil {
			return nil, err
		}
		remotePlans[i] = plan
		candidates += len(plan.Pruned)
		reclaim += plan.Reclaimed
	}

	now := time.Now()
	for i, tree := range trees {
		if len(plans[i].Pruned) == 0 {
			continue
		}
		emitPrune(opts.OnPlanned, PruneTreeReport{Label: tree[0], Root: tree[1], Now: now, Result: plans[i]})
	}
	for i, tree := range remoteTrees {
		if len(remotePlans[i].Pruned) == 0 {
			continue
		}
		emitPrune(opts.OnPlanned, PruneTreeReport{Label: tree[0], Root: tree[1], IsRemote: true, Now: now, Result: remotePlans[i]})
	}
	if candidates == 0 {
		return &ConflictsPruneResult{Outcome: PruneNothingToDo}, nil
	}
	emitSync(opts.Progress, SyncEvent{Kind: SyncEventPruneSummary, Candidates: candidates, Reclaimed: reclaim})
	if opts.DryRun {
		return &ConflictsPruneResult{Outcome: PrunePlanned}, nil
	}

	confirmed, err := askSync(opts.Confirm, ConfirmRequest{
		Kind:       ConfirmPruneConflicts,
		Candidates: candidates,
		Reclaimed:  reclaim,
	})
	if err != nil {
		return nil, err
	}
	if !confirmed {
		return &ConflictsPruneResult{Outcome: PruneAborted}, nil
	}

	for _, tree := range trees {
		res, err := PruneConflicts(tree[1], opts.Cutoff, false)
		if err != nil {
			return nil, err
		}
		if len(res.Pruned) == 0 {
			continue
		}
		emitPrune(opts.OnPruned, PruneTreeReport{Label: tree[0], Root: tree[1], Now: now, Result: res})
	}
	for i, tree := range remoteTrees {
		res, err := ApplyRemoteConflictPrune(ctx, runner, cfg.Target, tree[1], remotePlans[i].Pruned)
		if err != nil {
			return nil, err
		}
		if len(res.Pruned) == 0 {
			continue
		}
		emitPrune(opts.OnPruned, PruneTreeReport{Label: tree[0], Root: tree[1], IsRemote: true, Now: now, Result: res})
	}
	return &ConflictsPruneResult{Outcome: PruneDone}, nil
}
