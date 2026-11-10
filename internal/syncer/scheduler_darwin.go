//go:build darwin

package syncer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// InstallKind renders and loads the launchd plist for the given kind.
// Idempotent — unloads any pre-existing copy before reload so config
// changes (e.g. new Interval) take effect on rerun.
func (s *Scheduler) InstallKind(ctx context.Context, kind SchedulerKind) error {
	if err := validateSchedulerMutationHome(s.Config.Home); err != nil {
		return err
	}
	data := s.templateDataFor(kind)
	if data.DotfilesPath == "" {
		return fmt.Errorf("cannot find dot binary in PATH; run `make install` first")
	}
	plist := s.Paths.PlistFor(kind)
	if err := preparePlistTemplateData(&data); err != nil {
		return fmt.Errorf("cannot serialize shared scheduler plist %s: %w; existing artifact was left untouched; run dot sync setup after fixing the path", plist, err)
	}

	content, err := s.Engine.Render("sync/com.dotfiles.sync.plist.tmpl", data)
	if err != nil {
		return fmt.Errorf("rendering plist: %w", err)
	}

	dir := filepath.Dir(plist)
	if err := s.Runner.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating LaunchAgents dir: %w", err)
	}
	if err := s.Runner.WriteFileAtomic(plist, content, 0644); err != nil {
		return fmt.Errorf("writing plist: %w", err)
	}
	if s.requiresTargetUserServiceDomain() {
		return &SchedulerTargetUserActionRequiredError{}
	}

	// Unload first (ignore the error if it wasn't loaded already).
	_, _ = s.Runner.Run(ctx, "launchctl", "unload", plist)

	if _, err := s.Runner.Run(ctx, "launchctl", "load", plist); err != nil {
		return fmt.Errorf("loading plist: %w", err)
	}
	return nil
}

// UninstallKind unloads the launchd job and removes the plist file
// for the given kind. Missing-file is not an error.
func (s *Scheduler) UninstallKind(ctx context.Context, kind SchedulerKind) error {
	if err := validateSchedulerMutationHome(s.Config.Home); err != nil {
		return err
	}
	plist := s.Paths.PlistFor(kind)
	if s.requiresTargetUserServiceDomain() {
		if s.Runner.FileExists(plist) {
			if err := s.Runner.Remove(plist); err != nil {
				return fmt.Errorf("removing plist: %w", err)
			}
		}
		return &SchedulerTargetUserActionRequiredError{}
	}
	_, _ = s.Runner.Run(ctx, "launchctl", "unload", plist)
	if !s.Runner.FileExists(plist) {
		return nil
	}
	return s.Runner.Remove(plist)
}

// PauseKind unloads the launchd job (file stays on disk so Resume
// re-attaches).
func (s *Scheduler) PauseKind(ctx context.Context, kind SchedulerKind) error {
	if err := validateSchedulerMutationHome(s.Config.Home); err != nil {
		return err
	}
	plist := s.Paths.PlistFor(kind)
	if !s.Runner.FileExists(plist) {
		return nil
	}
	if s.requiresTargetUserServiceDomain() {
		return &SchedulerTargetUserActionRequiredError{}
	}
	_, err := s.Runner.Run(ctx, "launchctl", "unload", plist)
	return err
}

// ResumeKind re-loads the launchd job from its persisted plist.
//
// The unload comes first because `load` leaves a job launchd already holds
// running the command line it cached: a plist rewritten on disk (the store
// flag migration) or a job stuck in spawn failed would stay broken otherwise.
// A job that is not loaded makes the unload fail, which is ignored the same
// way InstallKind ignores it.
func (s *Scheduler) ResumeKind(ctx context.Context, kind SchedulerKind) error {
	if err := validateSchedulerMutationHome(s.Config.Home); err != nil {
		return err
	}
	plist := s.Paths.PlistFor(kind)
	if !s.Runner.FileExists(plist) {
		return nil
	}
	if s.requiresTargetUserServiceDomain() {
		return &SchedulerTargetUserActionRequiredError{}
	}
	_, _ = s.Runner.Run(ctx, "launchctl", "unload", plist)
	_, err := s.Runner.Run(ctx, "launchctl", "load", plist)
	return err
}

// StateKind queries launchctl to report the unit's runtime status for
// the given kind. A loaded job whose print output shows state = spawn
// failed reports SchedulerSpawnFailed, not SchedulerRunning: launchd holds
// it but never spawns it again until a reload (#233).
func (s *Scheduler) StateKind(ctx context.Context, kind SchedulerKind) SchedulerState {
	return s.InspectKind(ctx, kind).State
}

// InspectKind reads launchd without loading, unloading or rewriting its job.
func (s *Scheduler) InspectKind(ctx context.Context, kind SchedulerKind) SchedulerSnapshot {
	plist := s.Paths.PlistFor(kind)
	if !s.Runner.FileExists(plist) {
		return SchedulerSnapshot{State: SchedulerNotInstalled}
	}
	if s.requiresTargetUserServiceDomain() {
		return SchedulerSnapshot{State: SchedulerTargetUserActionRequired}
	}
	target := launchdPrintTarget(os.Getuid(), s.Paths.LaunchdLabelFor(kind))
	result, err := s.Runner.RunQuery(ctx, "launchctl", "print", target)
	printOK := err == nil && result != nil && result.ExitCode == 0
	snapshot := SchedulerSnapshot{State: launchdStateFromPrintStatus(true, printOK)}
	if printOK {
		snapshot.LastExitCode = LaunchdLastExitCode(result.Stdout)
	}
	if printOK && LaunchdSpawnFailed(result.Stdout) {
		snapshot.State = SchedulerSpawnFailed
	}
	return snapshot
}

// legacyLaunchdLabels are unit names superseded by com.dotfiles.sync*.
var legacyLaunchdLabels = []string{
	"com.dotfiles.gdrive-sync",        // pre-rename push unit
	"com.dotfiles.gdrive-sync-intake", // pre-rename pull unit
	"com.dotfiles.workspace-sync",     // retired SSH-only `dot sync`
}

// CleanupLegacyUnits unloads and removes launchd plists left behind by the
// pre-rename schedulers (gdrive-sync push/intake, SSH-only workspace-sync).
// Best-effort: missing files and unload failures are ignored.
func (s *Scheduler) CleanupLegacyUnits(ctx context.Context) error {
	if err := validateSchedulerMutationHome(s.Config.Home); err != nil {
		return err
	}
	dir := filepath.Dir(s.Paths.LaunchdPlist)
	if s.requiresTargetUserServiceDomain() {
		for _, label := range legacyLaunchdLabels {
			plist := filepath.Join(dir, label+".plist")
			if s.Runner.FileExists(plist) {
				if err := s.Runner.Remove(plist); err != nil {
					return fmt.Errorf("removing legacy plist: %w", err)
				}
			}
		}
		return &SchedulerTargetUserActionRequiredError{}
	}
	for _, label := range legacyLaunchdLabels {
		plist := filepath.Join(dir, label+".plist")
		_, _ = s.Runner.Run(ctx, "launchctl", "unload", plist)
		if s.Runner.FileExists(plist) {
			_ = s.Runner.Remove(plist)
		}
	}
	return nil
}

func launchdPrintTarget(uid int, label string) string {
	return fmt.Sprintf("gui/%d/%s", uid, label)
}

func launchdStateFromPrintStatus(plistExists bool, printOK bool) SchedulerState {
	if !plistExists {
		return SchedulerNotInstalled
	}
	if printOK {
		return SchedulerRunning
	}
	return SchedulerStopped
}
