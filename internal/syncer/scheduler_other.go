//go:build !darwin

package syncer

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// InspectKind reports the timer state; launchd exit codes do not apply here.
func (s *Scheduler) InspectKind(ctx context.Context, kind SchedulerKind) SchedulerSnapshot {
	return SchedulerSnapshot{State: s.StateKind(ctx, kind)}
}

// InstallKind renders the systemd user service + timer for the kind
// and enables them. Idempotent — daemon-reload is safe on every
// invocation, and `enable --now` accepts an already-enabled unit.
func (s *Scheduler) InstallKind(ctx context.Context, kind SchedulerKind) error {
	if err := validateSchedulerMutationHome(s.Config.Home); err != nil {
		return err
	}
	data := s.templateDataFor(kind)
	if data.DotfilesPath == "" {
		return fmt.Errorf("cannot find dot binary in PATH; run `make install` first")
	}

	svcContent, err := s.Engine.Render("sync/dotfiles-sync.service.tmpl", data)
	if err != nil {
		return fmt.Errorf("rendering service: %w", err)
	}
	timerContent, err := s.Engine.Render("sync/dotfiles-sync.timer.tmpl", data)
	if err != nil {
		return fmt.Errorf("rendering timer: %w", err)
	}

	servicePath := s.Paths.SystemdServiceFor(kind)
	timerPath := s.Paths.SystemdTimerFor(kind)
	dir := filepath.Dir(servicePath)
	if err := s.Runner.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating systemd dir: %w", err)
	}
	if err := s.Runner.WriteFileAtomic(servicePath, svcContent, 0644); err != nil {
		return fmt.Errorf("writing service: %w", err)
	}
	if err := s.Runner.WriteFileAtomic(timerPath, timerContent, 0644); err != nil {
		return fmt.Errorf("writing timer: %w", err)
	}
	if s.requiresTargetUserServiceDomain() {
		return &SchedulerTargetUserActionRequiredError{}
	}

	if _, err := s.Runner.Run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
		return fmt.Errorf("daemon-reload: %w", err)
	}
	if _, err := s.Runner.Run(ctx, "systemctl", "--user", "enable", "--now", filepath.Base(timerPath)); err != nil {
		return fmt.Errorf("enabling timer: %w", err)
	}
	return nil
}

// UninstallKind disables the timer and removes the systemd unit files
// for the given kind. Missing-file is not an error.
func (s *Scheduler) UninstallKind(ctx context.Context, kind SchedulerKind) error {
	if err := validateSchedulerMutationHome(s.Config.Home); err != nil {
		return err
	}
	timer := s.Paths.SystemdTimerFor(kind)
	service := s.Paths.SystemdServiceFor(kind)
	if s.requiresTargetUserServiceDomain() {
		if s.Runner.FileExists(timer) {
			if err := s.Runner.Remove(timer); err != nil {
				return fmt.Errorf("removing timer: %w", err)
			}
		}
		if s.Runner.FileExists(service) {
			if err := s.Runner.Remove(service); err != nil {
				return fmt.Errorf("removing service: %w", err)
			}
		}
		return &SchedulerTargetUserActionRequiredError{}
	}
	_, _ = s.Runner.Run(ctx, "systemctl", "--user", "disable", "--now", filepath.Base(timer))
	_ = s.Runner.Remove(timer)
	_ = s.Runner.Remove(service)
	_, _ = s.Runner.Run(ctx, "systemctl", "--user", "daemon-reload")
	return nil
}

// PauseKind stops the timer for the kind (units stay on disk; Resume
// restarts).
func (s *Scheduler) PauseKind(ctx context.Context, kind SchedulerKind) error {
	if err := validateSchedulerMutationHome(s.Config.Home); err != nil {
		return err
	}
	timer := s.Paths.SystemdTimerFor(kind)
	if !s.Runner.FileExists(timer) {
		return nil
	}
	if s.requiresTargetUserServiceDomain() {
		return &SchedulerTargetUserActionRequiredError{}
	}
	_, err := s.Runner.Run(ctx, "systemctl", "--user", "stop", filepath.Base(timer))
	return err
}

// ResumeKind reloads the units and starts the timer for the kind.
//
// daemon-reload runs first because systemd keeps the ExecStart it read until
// it is told to reload: a service unit rewritten on disk would otherwise start
// with the old command line.
func (s *Scheduler) ResumeKind(ctx context.Context, kind SchedulerKind) error {
	if err := validateSchedulerMutationHome(s.Config.Home); err != nil {
		return err
	}
	timer := s.Paths.SystemdTimerFor(kind)
	if !s.Runner.FileExists(timer) {
		return nil
	}
	if s.requiresTargetUserServiceDomain() {
		return &SchedulerTargetUserActionRequiredError{}
	}
	if _, err := s.Runner.Run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
		return fmt.Errorf("daemon-reload: %w", err)
	}
	_, err := s.Runner.Run(ctx, "systemctl", "--user", "start", filepath.Base(timer))
	return err
}

// legacySystemdUnits are unit names superseded by dotfiles-sync.*.
var legacySystemdUnits = []string{
	"dotfiles-gdrive-sync.service",
	"dotfiles-gdrive-sync.timer",
	"dotfiles-gdrive-sync-intake.service",
	"dotfiles-gdrive-sync-intake.timer",
}

// CleanupLegacyUnits disables and removes systemd units left behind by the
// pre-rename gdrive-sync schedulers. Best-effort.
func (s *Scheduler) CleanupLegacyUnits(ctx context.Context) error {
	if err := validateSchedulerMutationHome(s.Config.Home); err != nil {
		return err
	}
	dir := filepath.Dir(s.Paths.SystemdService)
	removed := false
	if s.requiresTargetUserServiceDomain() {
		for _, unit := range legacySystemdUnits {
			path := filepath.Join(dir, unit)
			if s.Runner.FileExists(path) {
				if err := s.Runner.Remove(path); err != nil {
					return fmt.Errorf("removing legacy systemd unit: %w", err)
				}
			}
		}
		return &SchedulerTargetUserActionRequiredError{}
	}
	for _, unit := range legacySystemdUnits {
		if strings.HasSuffix(unit, ".timer") {
			_, _ = s.Runner.Run(ctx, "systemctl", "--user", "disable", "--now", unit)
		}
		path := filepath.Join(dir, unit)
		if s.Runner.FileExists(path) {
			_ = s.Runner.Remove(path)
			removed = true
		}
	}
	if removed {
		_, _ = s.Runner.Run(ctx, "systemctl", "--user", "daemon-reload")
	}
	return nil
}

// StateKind asks systemctl for the timer's runtime status for the kind.
func (s *Scheduler) StateKind(ctx context.Context, kind SchedulerKind) SchedulerState {
	timer := s.Paths.SystemdTimerFor(kind)
	if !s.Runner.FileExists(timer) {
		return SchedulerNotInstalled
	}
	if s.requiresTargetUserServiceDomain() {
		return SchedulerTargetUserActionRequired
	}
	result, err := s.Runner.Run(ctx, "systemctl", "--user", "is-active", filepath.Base(timer))
	if err != nil || result.ExitCode != 0 {
		return SchedulerStopped
	}
	if strings.TrimSpace(result.Stdout) == "active" {
		return SchedulerRunning
	}
	return SchedulerStopped
}
