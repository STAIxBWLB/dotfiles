package syncer

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// Status is the snapshot returned by GetStatus for the `status` command.
type Status struct {
	Profile              string
	Owner                string
	LocalPath            string
	MirrorPath           string
	Target               Target // parsed destination (local dir or ssh remote)
	StoreDir             string // <local>/.dotfiles/sync/ — empty if unresolved
	LocalExists          bool
	MirrorExists         bool
	Paused               bool
	FilterMode           FilterMode
	IncludeFile          string
	ExcludeFile          string
	IgnoreFile           string
	AllowCount           int // active allow.txt patterns (secrets opt-in) — warn when > 0
	SensitiveOverrides   []SensitiveOverride
	SubmoduleCount       int // submodules excluded from sync (they sync via Git)
	UnsupportedNames     int // workspace names Dropbox/Windows cannot store — see `dot sync names trim`
	Propagation          PropagationPolicy
	LastPull             time.Time
	LastPush             time.Time
	LastIntake           time.Time
	LastIntakeTSDir      string
	LastHeld             time.Time // last peer run that held destructive transitions
	LastPushAttempt      time.Time // latest real push run
	LastPushError        string    // why the latest push did not complete; empty once one does
	LastPushErrorSince   time.Time // when the failing streak began
	RsyncPath            string
	RsyncVersion         string // empty if not installed
	LockHeld             bool   // someone has gsync.lock right now
	MaxDelete            int
	Interval             int
	PullInterval         int            // 0 → no pull scheduler
	PushMode             RunMode        // automatic push mode
	PullMode             RunMode        // automatic intake mode
	SchedulerState       SchedulerState // push unit
	IntakeSchedulerState SchedulerState // pull unit (if installed)
	Conflicts            []ConflictEntry
	BackupBytes          int64  // bytes held by the Conflicts directories
	LiveConflicts        int    // conflicts the latest push refused on (#264)
	ConflictsError       string // a tree whose backups could not be listed (a symlinked .sync-conflicts)
	Shared               []SharedEntry

	SchedulerLastExitCode       *int
	IntakeSchedulerLastExitCode *int
}

// GetStatus collects current sync state from cfg + state + filesystem.
// Always non-mutating; safe to run while a sync is in progress.
//
// sched may be nil — callers that don't have a Scheduler instance get a
// SchedulerNotInstalled value back rather than a panic.
func GetStatus(ctx context.Context, runner *exec.Runner, cfg *Config, state *config.UserState, sched *Scheduler) (*Status, error) {
	_ = state // legacy global state is no longer authoritative for gsync status.
	storeDir := ""
	var localState LocalState
	if cfg.LocalPaths != nil {
		storeDir = cfg.LocalPaths.StoreDir
		if st, err := LoadLocalState(cfg.LocalPaths); err == nil && st != nil {
			localState = *st
		}
	}
	allowCount := 0
	for _, p := range cfg.AllowPatterns {
		p = strings.TrimSpace(p)
		if p != "" && !strings.HasPrefix(p, "#") {
			allowCount++
		}
	}
	// Collect the complete, already-sorted policy finding before constructing the
	// status snapshot. Callers therefore receive either every typed override or
	// no snapshot at all; terminal and JSON renderers never have to assemble a
	// partial report while emitting output.
	sensitiveOverrides := SensitiveOverrides(cfg.AllowPatterns)
	s := &Status{
		Profile:            cfg.Profile,
		Owner:              cfg.Owner,
		LocalPath:          strings.TrimRight(cfg.LocalPath, "/"),
		MirrorPath:         strings.TrimRight(cfg.MirrorPath, "/"),
		Target:             cfg.Target,
		StoreDir:           storeDir,
		LocalExists:        runner.IsDir(cfg.LocalPath),
		MirrorExists:       cfg.Target.IsSSH() || runner.IsDir(cfg.MirrorPath),
		Paused:             cfg.Paused,
		FilterMode:         cfg.FilterMode,
		IncludeFile:        cfg.IncludeFile,
		ExcludeFile:        cfg.ExcludesFile,
		IgnoreFile:         cfg.IgnoreFile,
		AllowCount:         allowCount,
		SensitiveOverrides: sensitiveOverrides,
		SubmoduleCount:     len(gitSubmodulePaths(strings.TrimRight(cfg.LocalPath, "/"))),
		Propagation:        cfg.Propagation,
		LastPull:           localState.LastPull,
		LastPush:           localState.LastPush,
		LastIntake:         localState.LastIntake,
		LastIntakeTSDir:    localState.LastIntakeTSDir,
		LastHeld:           localState.LastHeld,
		LastPushAttempt:    localState.LastPushAttempt,
		LastPushError:      localState.LastPushError,
		LastPushErrorSince: localState.LastPushErrorSince,
		LockHeld:           pathExists(cfg.LockDir) && !lockIsStale(cfg.LockDir),
		MaxDelete:          cfg.MaxDelete,
		Interval:           cfg.Interval,
		PullInterval:       cfg.PullInterval,
		PushMode:           cfg.PushMode,
		PullMode:           cfg.PullMode,
	}
	if sched != nil {
		push, intake := sched.InspectKind(ctx, SchedulerKindPush), sched.InspectKind(ctx, SchedulerKindIntake)
		s.SchedulerState, s.SchedulerLastExitCode = push.State, push.LastExitCode
		s.IntakeSchedulerState, s.IntakeSchedulerLastExitCode = intake.State, intake.LastExitCode
	}

	statusConfig := *cfg
	_ = ResolvePushRsync(ctx, runner, &statusConfig)
	rsyncPath := statusConfig.rsyncBin()
	if runner.CommandExists(rsyncPath) {
		if result, err := runner.RunQuery(ctx, rsyncPath, "--version"); err == nil {
			s.RsyncPath = rsyncPath
			if i := strings.IndexByte(result.Stdout, '\n'); i > 0 {
				s.RsyncVersion = strings.TrimSpace(result.Stdout[:i])
			} else {
				s.RsyncVersion = strings.TrimSpace(result.Stdout)
			}
		}
	}

	// Conflicts accumulate in both trees (pull backups in the workspace,
	// push backups in the mirror) — keep the snapshot consistent with
	// `conflicts list`/`prune`.
	if confs, err := ListConflicts(s.LocalPath); err == nil {
		s.Conflicts = confs
	} else {
		s.ConflictsError = err.Error()
	}
	if s.MirrorPath != "" && !cfg.Target.IsSSH() && filepath.Clean(s.MirrorPath) != filepath.Clean(s.LocalPath) {
		if confs, err := ListConflicts(s.MirrorPath); err == nil {
			s.Conflicts = append(s.Conflicts, confs...)
		} else if s.ConflictsError == "" {
			s.ConflictsError = err.Error()
		}
	}

	for _, c := range s.Conflicts {
		s.BackupBytes += conflictDirSize(c.Path)
	}
	s.LiveConflicts = localState.LastPushConflicts

	// Populate manual shared entries. Errors are non-fatal; status is best-effort.
	if shared, err := ScanShared(s.MirrorPath, cfg.SharedExcludes); err == nil {
		s.Shared = shared
	}

	// Same best-effort rule: an unwalkable workspace must not fail status.
	if count, err := CountUnsupportedNames(cfg); err == nil {
		s.UnsupportedNames = count
	}

	return s, nil
}

// PushStalled reports why pushes look stuck, or "" when they do not: the last
// push run failed, or the push scheduler is on and no push completed within
// three intervals, or none ever did although runs were attempted (#224). The
// stale check is skipped while paused (pushes are meant to stop) or while a
// run holds the lock (a long push is still going).
func (s *Status) PushStalled(now time.Time) string {
	if s.LastPushError != "" {
		return fmt.Sprintf("pushes failing since %s: %s",
			s.LastPushErrorSince.Local().Format("2006-01-02 15:04"), s.LastPushError)
	}
	if s.Interval <= 0 || s.SchedulerState != SchedulerRunning || s.Paused || s.LockHeld {
		return ""
	}
	if s.LastPush.IsZero() {
		if !s.LastPushAttempt.IsZero() {
			return "no push has completed yet; the runs so far skipped files"
		}
		return ""
	}
	if now.Sub(s.LastPush) > 3*time.Duration(s.Interval)*time.Second {
		return fmt.Sprintf("no push completed for %s (interval %s)",
			now.Sub(s.LastPush).Truncate(time.Minute), time.Duration(s.Interval)*time.Second)
	}
	return ""
}
