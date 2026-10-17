package syncer

import (
	"context"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// PushOptions carries what `dot sync push` needs once its cli-side guards have
// run. The propagation override is not a field: the pre-move body applied
// `--propagate` to Config before preflight, so the caller keeps doing that and
// the ordering of the two refusals is unchanged.
type PushOptions struct {
	State    *config.UserState
	Config   *Config
	Runner   *exec.Runner
	Mode     RunMode
	DryRun   bool
	Progress func(SyncEvent)
	Confirm  ConfirmFunc
	// WhileLocked runs once this run holds the sync lock, and its release
	// when the run ends: the caller's background lease then always belongs
	// to the run that owns the lock (#267). Nil holds nothing.
	WhileLocked func() (release func())
}

// PushOutcome names how a push run ended; cli renders the closing line.
type PushOutcome int

const (
	PushLockBusy PushOutcome = iota // another run holds the sync lock
	PushPlanned                     // dry-run, or nothing to send: no transfer ran
	PushAborted                     // the operator declined
	PushComplete                    // the transfer ran
)

// PushCommandResult is the terminal state of a push run.
type PushCommandResult struct {
	Outcome PushOutcome
	LockErr error
}

// PushCommand runs the `dot sync push` transaction. It holds the sync lock
// across name normalization, planning, the confirmation and the transfer, so
// no second writer can interleave with any of them.
func PushCommand(ctx context.Context, opts PushOptions) (res *PushCommandResult, err error) {
	cfg, runner, state := opts.Config, opts.Runner, opts.State

	release, lockErr := AcquireLockForRun(cfg.LockDir, opts.DryRun)
	if lockErr != nil {
		return &PushCommandResult{Outcome: PushLockBusy, LockErr: lockErr}, nil
	}
	defer release()
	if opts.WhileLocked != nil {
		defer opts.WhileLocked()()
	}
	// Whether this run completed is decided here, once, from how it returned.
	partial := false
	liveConflicts := 0 // conflicts a refused run leaves for status (#264)
	if !opts.DryRun && !cfg.Paused {
		defer func() {
			switch {
			case res != nil && res.Outcome == PushAborted:
				return // an operator decline is not an attempt
			case res == nil && err == nil:
				return // a panic: the run neither completed nor failed
			}
			if recErr := recordPushAttempt(cfg, err, partial, liveConflicts); recErr != nil && err == nil {
				err = fmt.Errorf("state update: %w", recErr)
			}
		}()
	}

	if err := ResolvePushRsync(ctx, runner, cfg); err != nil {
		return nil, fmt.Errorf("push refused: %w", err)
	}

	// Once the explicit workspace migration has written its marker, every real
	// push canonicalizes newly downloaded or created NFC names before either a
	// preview plan or rsync filter is materialized. Dry-runs remain read-only.
	if !opts.DryRun {
		if err := NormalizeWorkspaceNamesBeforePush(cfg); err != nil {
			return nil, fmt.Errorf("normalizing workspace names: %w", err)
		}
		cfg.NamesNormalized = true
	}

	// SSH targets cannot be plan-previewed (the remote tree is not
	// walkable); push runs rsync directly, like the retired SSH-only sync.
	if cfg.Target.IsSSH() {
		emitSync(opts.Progress, SyncEvent{Kind: SyncEventPushSSHStart})
		if opts.Mode == ModeManual && !opts.DryRun {
			confirmed, err := askSync(opts.Confirm, ConfirmRequest{Kind: ConfirmPushSSH})
			if err != nil {
				return nil, err
			}
			if !confirmed {
				return &PushCommandResult{Outcome: PushAborted}, nil
			}
		}
		raw := Push(ctx, runner, cfg, opts.DryRun)
		if ctxErr := ctx.Err(); ctxErr != nil && (raw == nil || IsPartialTransfer(raw)) {
			// The same interrupt rule as the local branch below: dot was
			// interrupted, so Push may have skipped the refresh (rsyncExited)
			// and the run is not complete; a canceled SSH push whose rsync
			// exited 24 must not stamp last_push (#233).
			raw = fmt.Errorf("push interrupted: %w", ctxErr)
		}
		partial = skippedFiles(raw)
		pushErr := downgradePartial(opts.Progress, raw)
		RecordResult(state, cfg, "push", pushErr, opts.DryRun)
		if pushErr != nil {
			return nil, fmt.Errorf("push failed: %w", pushErr)
		}
		return &PushCommandResult{Outcome: PushComplete}, nil
	}

	emitSync(opts.Progress, SyncEvent{Kind: SyncEventPushPlanStart})
	if opts.DryRun {
		emitSync(opts.Progress, SyncEvent{Kind: SyncEventDryRunNotice})
	}
	plan, err := PlanPush(cfg)
	if err != nil {
		return nil, fmt.Errorf("planning push: %w", err)
	}
	emitSync(opts.Progress, SyncEvent{Kind: SyncEventPushPlanReady, PushPlan: plan})
	if !opts.DryRun && len(plan.BaselineRefresh) > 0 {
		if err := refreshBaselineRows(cfg, plan.BaselineRefresh); err != nil {
			return nil, err
		}
	}
	if opts.DryRun || (!plan.HasChanges() && !plan.HasConflicts()) {
		RecordResult(state, cfg, "push", nil, opts.DryRun)
		return &PushCommandResult{Outcome: PushPlanned}, nil
	}
	if opts.Mode == ModeClean && plan.HasConflicts() {
		refused := fmt.Errorf("push refused: %d conflict(s); rerun with --mode=force to overwrite with backups", len(plan.Conflicts))
		liveConflicts = len(plan.Conflicts)
		RecordResult(state, cfg, "push", refused, false)
		return nil, refused
	}
	if opts.Mode == ModeManual {
		confirmed, err := askSync(opts.Confirm, ConfirmRequest{Kind: ConfirmPushPlan})
		if err != nil {
			return nil, err
		}
		if !confirmed {
			return &PushCommandResult{Outcome: PushAborted}, nil
		}
	}
	raw := Push(ctx, runner, cfg, false)
	if ctxErr := ctx.Err(); ctxErr != nil && (raw == nil || IsPartialTransfer(raw)) {
		// dot was interrupted, so Push may have skipped the refresh
		// (rsyncExited): neither the post-push checks nor the leftover move
		// may act on that baseline, and the run is not complete (#231).
		raw = fmt.Errorf("push interrupted: %w", ctxErr)
	}
	partial = skippedFiles(raw)
	pushErr := downgradePartial(opts.Progress, raw)
	if pushErr == nil {
		pushErr = unappliedDeletes(ctx, runner, cfg, plan.Deletes)
	}
	if pushErr == nil && plan.MoveLeftovers && len(plan.Leftovers) > 0 {
		dir, moved, moveErr := MoveMirrorLeftovers(cfg, plan.Leftovers)
		if moveErr != nil {
			pushErr = moveErr
		} else if moved > 0 {
			emitSync(opts.Progress, SyncEvent{Kind: SyncEventLeftoversMoved, Path: dir, Candidates: moved})
		}
	}
	RecordResult(state, cfg, "push", pushErr, false)
	if pushErr != nil {
		return nil, fmt.Errorf("push failed: %w", pushErr)
	}
	return &PushCommandResult{Outcome: PushComplete}, nil
}

// refreshBaselineRows writes the given baseline rows, keeping every other
// row: the content-equal pairs a plan found (#265).
func refreshBaselineRows(cfg *Config, rows map[string]Fingerprint) error {
	baseline, err := LoadBaselineManifest(cfg.LocalPaths.BaselineFile)
	if err != nil {
		return fmt.Errorf("baseline refresh: %w", err)
	}
	for rel, fp := range rows {
		baseline[rel] = fp
	}
	if err := SaveBaselineManifest(cfg.LocalPaths.BaselineFile, baseline); err != nil {
		return fmt.Errorf("baseline refresh: %w", err)
	}
	return nil
}

// unappliedDeletes fails a push that left a planned deletion in the mirror
// while the workspace still lacks the file. The baseline keeps such a file
// proven, so the run must not pass as complete (#224).
//
// It names a cause only when the evidence is certain: the rsync this run used
// prints an openrsync banner, and openrsync deletes nothing with --backup.
// Otherwise it lists what makes rsync 3.x keep a file without picking one: an
// exit code does not say which (exit 23 also follows receiver errors that leave
// the deletions applied), and a filter can protect a name (an exclude.txt
// pattern; the generated layers escape literal paths, #228).
func unappliedDeletes(ctx context.Context, runner *exec.Runner, cfg *Config, deletes []string) error {
	local := strings.TrimRight(cfg.LocalPath, "/")
	mirror := strings.TrimRight(cfg.MirrorPath, "/")
	var left []string
	for _, rel := range deletes {
		if _, err := os.Lstat(filepath.Join(local, rel)); err == nil {
			continue // restored in the workspace since the plan
		}
		if _, err := os.Lstat(filepath.Join(mirror, rel)); err == nil {
			left = append(left, rel)
		}
	}
	if len(left) == 0 {
		return nil
	}
	why := "rsync kept them: it skips deletions after an I/O error (see its output), and a filter can protect a name"
	if res, err := runner.RunQuery(ctx, cfg.rsyncBin(), "--version"); err == nil && strings.Contains(res.Stdout, "openrsync") {
		why = "openrsync deletes nothing with --backup; install rsync 3.x (#227)"
	}
	return fmt.Errorf("%d planned deletion(s) left in the mirror, first %s: %s", len(left), left[0], why)
}

// skippedFiles reports an rsync run that left files out because of an error
// (exit 23). Exit 24 only means files vanished before transfer: every file
// still in the workspace was sent, so that run is complete.
func skippedFiles(err error) bool {
	var p *PartialTransferError
	return errors.As(err, &p) && p.Code == 23
}

// RecordPushRefusal records a push the CLI refused before PushCommand ran (an
// owner mismatch, a preflight block), so status shows it like any refusal. A
// paused profile records nothing, whatever refused the push: a pause is meant
// to stop pushes. The write takes the sync lock; while a run holds it, that
// run records its own outcome instead.
func RecordPushRefusal(cfg *Config, why error) error {
	if cfg.Paused {
		return nil
	}
	release, err := AcquireLockForRun(cfg.LockDir, false)
	if err != nil {
		return nil
	}
	defer release()
	return recordPushAttempt(cfg, why, false, -1) // refused before planning: conflicts unknown
}

// recordPushAttempt notes how a real push run ended in the profile state. A
// refused or failed run keeps its first line and the time the failing streak
// began. Any other run clears the error, and only a run that sent every file
// still in the workspace stamps last_push: one that skipped files (exit 23)
// does not, so a push that keeps skipping them goes stale in status (#224). It
// runs after the post-push checks, so a run they fail never counts as a push.
func recordPushAttempt(cfg *Config, runErr error, partial bool, liveConflicts int) error {
	if cfg.LocalPaths == nil {
		return nil
	}
	now := time.Now().UTC()
	return UpdateLocalState(cfg.LocalPaths, func(s *LocalState) {
		s.LastPushAttempt = now
		if liveConflicts >= 0 {
			s.LastPushConflicts = liveConflicts
		}
		if runErr == nil {
			if !partial {
				s.LastPush = now
			}
			s.LastPushError, s.LastPushErrorSince = "", time.Time{}
			return
		}
		if s.LastPushError == "" {
			s.LastPushErrorSince = now
		}
		msg := []rune(pushErrorSummary(runErr))
		if len(msg) > 300 {
			msg = append(msg[:300], []rune("...")...)
		}
		s.LastPushError = string(msg)
	})
}

// pushErrorSummary names a push error in one line. A failed command's error
// text leads with its whole argv, which says nothing, so it becomes the
// program's name, its exit status and the first line of what it printed.
func pushErrorSummary(err error) string {
	var ce *exec.CmdError
	if !errors.As(err, &ce) {
		return firstLine(err.Error())
	}
	var ee *osexec.ExitError
	if !errors.As(ce.Err, &ee) {
		return "push failed: " + firstLine(ce.Err.Error()) // it never started
	}
	name := "command"
	if f := strings.Fields(ce.Cmd); len(f) > 0 {
		name = filepath.Base(f[0])
	}
	msg := fmt.Sprintf("push failed: %s exit %d", name, ce.ExitCode)
	if line := firstLine(strings.TrimSpace(ce.Stderr)); line != "" {
		msg += ": " + line
	}
	return msg
}

// downgradePartial turns an rsync partial transfer (exit 23/24) into a
// progress event and a successful run. Only the classification lives here: the
// two lines the operator reads are cli's, which is why this hands the error
// out to the event rather than describing it.
func downgradePartial(progress func(SyncEvent), err error) error {
	if err == nil || !IsPartialTransfer(err) {
		return err
	}
	emitSync(progress, SyncEvent{Kind: SyncEventPartialTransfer, Err: err})
	return nil
}

// PullCommandOptions carries what `dot sync pull` needs after its cli-side
// guards have run. The name is not `PullOptions`: that belongs to PullTracked,
// which this entry calls twice, and renaming an existing engine type was ruled
// out for this phase.
type PullCommandOptions struct {
	State    *config.UserState
	Config   *Config
	Runner   *exec.Runner
	Mode     RunMode
	Strict   bool
	DryRun   bool
	Progress func(SyncEvent)
	Confirm  ConfirmFunc
}

// PullOutcome names how a pull run ended.
type PullOutcome int

const (
	PullLockBusy        PullOutcome = iota // another run holds the sync lock
	PullPlanned                            // dry-run, or nothing to apply
	PullAborted                            // the operator declined
	PullCompleteDirect                     // ssh target: rsync ran without a plan preview
	PullCompleteTracked                    // local target: Result carries what was applied
)

// PullCommandResult is the terminal state of a pull run.
type PullCommandResult struct {
	Outcome PullOutcome
	LockErr error
	Result  *PullResult // set for PullCompleteTracked
}

// PullCommand runs the `dot sync pull` transaction under the sync lock.
func PullCommand(ctx context.Context, opts PullCommandOptions) (*PullCommandResult, error) {
	cfg, runner, state := opts.Config, opts.Runner, opts.State

	release, lockErr := AcquireLockForRun(cfg.LockDir, opts.DryRun)
	if lockErr != nil {
		return &PullCommandResult{Outcome: PullLockBusy, LockErr: lockErr}, nil
	}
	defer release()

	// SSH targets: direct rsync pull (--update, backups). The baseline-
	// driven pull below needs to walk the target tree, which ssh can't.
	if cfg.Target.IsSSH() {
		emitSync(opts.Progress, SyncEvent{Kind: SyncEventPullSSHStart})
		if opts.DryRun {
			emitSync(opts.Progress, SyncEvent{Kind: SyncEventDryRunNotice})
		}
		pullErr := PullDirect(ctx, runner, cfg, opts.DryRun)
		RecordResult(state, cfg, "pull", pullErr, opts.DryRun)
		if pullErr != nil {
			return nil, fmt.Errorf("pull failed: %w", pullErr)
		}
		return &PullCommandResult{Outcome: PullCompleteDirect}, nil
	}

	emitSync(opts.Progress, SyncEvent{Kind: SyncEventPullPlanStart})
	if opts.DryRun {
		emitSync(opts.Progress, SyncEvent{Kind: SyncEventDryRunNotice})
	}
	plan, err := PullTracked(cfg, PullOptions{DryRun: true, Strict: opts.Strict})
	if err != nil {
		return nil, fmt.Errorf("planning pull: %w", err)
	}
	emitSync(opts.Progress, SyncEvent{Kind: SyncEventPullPlanReady, PullResult: plan})
	if opts.DryRun || !plan.HasChanges() {
		return &PullCommandResult{Outcome: PullPlanned}, nil
	}
	if opts.Mode == ModeClean && len(plan.Conflicts) > 0 {
		return nil, fmt.Errorf("pull refused: %d conflict(s); rerun with --mode=force to overwrite with backups", len(plan.Conflicts))
	}
	force := opts.Mode == ModeForce
	if opts.Mode == ModeManual {
		confirmed, err := askSync(opts.Confirm, ConfirmRequest{Kind: ConfirmPullPlan})
		if err != nil {
			return nil, err
		}
		if !confirmed {
			return &PullCommandResult{Outcome: PullAborted}, nil
		}
		force = len(plan.Conflicts) > 0
	}
	res, err := PullTracked(cfg, PullOptions{Force: force, Strict: opts.Strict})
	RecordResult(state, cfg, "pull", err, false)
	if err != nil {
		return nil, fmt.Errorf("pull failed: %w", err)
	}
	return &PullCommandResult{Outcome: PullCompleteTracked, Result: res}, nil
}

// IntakeCommandOptions carries what `dot sync intake` needs. As with pull, the
// unqualified name belongs to the existing IntakeOptions this entry fills in.
type IntakeCommandOptions struct {
	Config   *Config
	Runner   *exec.Runner
	Strict   bool
	DryRun   bool
	Progress func(SyncEvent)
}

// IntakeOutcome names how an intake run ended.
type IntakeOutcome int

const (
	IntakeLockBusy IntakeOutcome = iota // another run holds the sync lock
	IntakeDone                          // the staging pass ran; Result carries it
)

// IntakeCommandResult is the terminal state of an intake run.
type IntakeCommandResult struct {
	Outcome IntakeOutcome
	LockErr error
	Result  *IntakeResult
}

// IntakeCommand stages new mirror-origin files under the sync lock.
func IntakeCommand(ctx context.Context, opts IntakeCommandOptions) (*IntakeCommandResult, error) {
	cfg := opts.Config

	release, lockErr := AcquireLockForRun(cfg.LockDir, opts.DryRun)
	if lockErr != nil {
		return &IntakeCommandResult{Outcome: IntakeLockBusy, LockErr: lockErr}, nil
	}
	defer release()

	emitSync(opts.Progress, SyncEvent{Kind: SyncEventIntakeStart})
	if opts.DryRun {
		emitSync(opts.Progress, SyncEvent{Kind: SyncEventDryRunNotice})
	}
	res, err := Intake(ctx, opts.Runner, cfg, IntakeOptions{
		Strict: opts.Strict,
		DryRun: opts.DryRun,
	})
	if err != nil {
		return nil, fmt.Errorf("intake failed: %w", err)
	}
	return &IntakeCommandResult{Outcome: IntakeDone, Result: res}, nil
}

// FetchOptions carries what `dot sync fetch` needs. Paths are the
// workspace-relative arguments, already validated as non-empty by cobra.
type FetchOptions struct {
	State    *config.UserState
	Config   *Config
	Runner   *exec.Runner
	Paths    []string
	DryRun   bool
	Progress func(SyncEvent)
}

// FetchOutcome names how a fetch run ended.
type FetchOutcome int

const (
	FetchLockBusy FetchOutcome = iota // another run holds the sync lock
	FetchDone                         // the fetch ran; Result carries it
)

// FetchCommandResult is the terminal state of a fetch run.
type FetchCommandResult struct {
	Outcome FetchOutcome
	LockErr error
	Result  *FetchResult
}

// FetchCommand restores the named paths from the target under the sync lock.
func FetchCommand(ctx context.Context, opts FetchOptions) (*FetchCommandResult, error) {
	cfg := opts.Config

	release, lockErr := AcquireLockForRun(cfg.LockDir, opts.DryRun)
	if lockErr != nil {
		return &FetchCommandResult{Outcome: FetchLockBusy, LockErr: lockErr}, nil
	}
	defer release()

	if opts.DryRun {
		emitSync(opts.Progress, SyncEvent{Kind: SyncEventDryRunNotice})
	}
	res, fetchErr := Fetch(ctx, opts.Runner, cfg, opts.Paths, opts.DryRun)
	RecordResult(opts.State, cfg, "fetch", fetchErr, opts.DryRun)
	if res != nil {
		for _, rel := range res.Missing {
			emitSync(opts.Progress, SyncEvent{Kind: SyncEventFetchMissing, Path: rel})
		}
		for _, rel := range res.Excluded {
			emitSync(opts.Progress, SyncEvent{Kind: SyncEventFetchExcluded, Path: rel})
		}
	}
	if fetchErr != nil {
		return nil, fmt.Errorf("fetch failed: %w", fetchErr)
	}
	return &FetchCommandResult{Outcome: FetchDone, Result: res}, nil
}
