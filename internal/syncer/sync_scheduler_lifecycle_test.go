package syncer

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

func TestSyncSchedulerLifecycle_InstalledKinds(t *testing.T) {
	tests := []struct {
		name      string
		operation string
		push      bool
		intake    bool
		failFirst bool
	}{
		{name: "pause pull only", operation: "pause", intake: true},
		{name: "pause push only", operation: "pause", push: true},
		{name: "pause dual", operation: "pause", push: true, intake: true},
		{name: "pause neither", operation: "pause"},
		{name: "pause stops after push failure", operation: "pause", push: true, intake: true, failFirst: true},
		{name: "resume pull only", operation: "resume", intake: true},
		{name: "resume push only", operation: "resume", push: true},
		{name: "resume dual", operation: "resume", push: true, intake: true},
		{name: "resume neither", operation: "resume"},
		{name: "resume stops after push failure", operation: "resume", push: true, intake: true, failFirst: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, record, paths := schedulerLifecycleConfig(t, tc.push, tc.intake, tc.operation, tc.failFirst)
			ctx := context.Background()
			var schedulerErr error
			var lifecycleComplete bool
			switch tc.operation {
			case "pause":
				result, err := SyncPause(ctx, cfg, exec.NewRunner(false, slog.New(slog.NewTextHandler(io.Discard, nil))))
				if err != nil {
					t.Fatalf("SyncPause: %v", err)
				}
				schedulerErr = result.SchedulerErr
				lifecycleComplete = result.SchedulerStopped
			case "resume":
				result, err := SyncResume(ctx, cfg, exec.NewRunner(false, slog.New(slog.NewTextHandler(io.Discard, nil))))
				if err != nil {
					t.Fatalf("SyncResume: %v", err)
				}
				schedulerErr = result.SchedulerErr
				lifecycleComplete = result.SchedulerResumed
			default:
				t.Fatalf("unknown operation %q", tc.operation)
			}

			got := readSchedulerLifecycleCommands(t, record)
			want := schedulerLifecycleCommands(paths, tc.push, tc.intake, tc.operation, tc.failFirst)
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Fatalf("service-manager argv = %q, want %q", got, want)
			}

			installed := tc.push || tc.intake
			if installed && schedulerStateQueryCount(got) == 0 {
				t.Fatal("installed scheduler row made no state query")
			}
			if installed && schedulerLifecycleActionCount(got) == 0 {
				t.Fatal("installed scheduler row made no lifecycle action")
			}
			if !installed && len(got) != 0 {
				t.Fatalf("neither-installed row made service-manager calls: %q", got)
			}
			if tc.failFirst {
				if schedulerErr == nil {
					t.Fatal("first lifecycle failure was not retained")
				}
				if lifecycleComplete {
					t.Fatal("aggregate lifecycle success stayed true after first failure")
				}
			} else if installed && !lifecycleComplete {
				t.Fatal("successful installed lifecycle did not set aggregate success")
			}
		})
	}
}

func schedulerLifecycleConfig(t *testing.T, push, intake bool, operation string, failFirst bool) (*Config, string, *Paths) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(root, "scheduler-args")
	if runtime.GOOS == "darwin" {
		writeStub(t, filepath.Join(binDir, "launchctl"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$DOTFILES_TEST_SCHEDULER_ARGS\"\nif [ \"$1\" = \"print\" ]; then\n  if [ \"$DOTFILES_TEST_SCHEDULER_STATE\" = \"stopped\" ]; then exit 1; fi\n  exit 0\nfi\nif [ \"$DOTFILES_TEST_FAIL_FIRST\" = \"1\" ]; then exit 42; fi\nexit 0\n")
	} else {
		writeStub(t, filepath.Join(binDir, "systemctl"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$DOTFILES_TEST_SCHEDULER_ARGS\"\nif [ \"$2\" = \"is-active\" ]; then\n  if [ \"$DOTFILES_TEST_SCHEDULER_STATE\" = \"stopped\" ]; then exit 1; fi\n  echo active\n  exit 0\nfi\nif [ \"$DOTFILES_TEST_FAIL_FIRST\" = \"1\" ]; then exit 42; fi\nexit 0\n")
	}
	t.Setenv("PATH", binDir)
	t.Setenv("DOTFILES_TEST_SCHEDULER_ARGS", record)
	t.Setenv("DOTFILES_TEST_SCHEDULER_STATE", map[bool]string{true: "running", false: "stopped"}[operation == "pause"])
	if failFirst {
		t.Setenv("DOTFILES_TEST_FAIL_FIRST", "1")
	}

	paths, err := resolvePathsForHomeProfile(root, DefaultProfile)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		installed bool
		kind      SchedulerKind
	}{{push, SchedulerKindPush}, {intake, SchedulerKindIntake}} {
		if !item.installed {
			continue
		}
		artifact := paths.PlistFor(item.kind)
		if runtime.GOOS != "darwin" {
			artifact = paths.SystemdTimerFor(item.kind)
		}
		if err := os.MkdirAll(filepath.Dir(artifact), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(artifact, []byte("persisted scheduler artifact"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	localPaths := ResolveLocalPaths(filepath.Join(root, "workspace"))
	if err := SaveLocalConfig(localPaths, &LocalConfig{Propagation: DefaultPropagationPolicy()}); err != nil {
		t.Fatal(err)
	}
	return &Config{
		Profile:      DefaultProfile,
		LocalPath:    filepath.Join(root, "workspace"),
		LocalPaths:   localPaths,
		SystemPaths:  paths,
		Interval:     schedulerInterval(push),
		PullInterval: schedulerInterval(intake),
		Paused:       operation == "resume",
	}, record, paths
}

func schedulerInterval(installed bool) int {
	if installed {
		return 60
	}
	return 0
}

func schedulerLifecycleCommands(paths *Paths, push, intake bool, operation string, failFirst bool) []string {
	var commands []string
	for _, item := range []struct {
		installed bool
		kind      SchedulerKind
	}{{push, SchedulerKindPush}, {intake, SchedulerKindIntake}} {
		if !item.installed {
			continue
		}
		commands = append(commands, schedulerStateCommand(paths, item.kind))
	}
	for _, item := range []struct {
		installed bool
		kind      SchedulerKind
	}{{push, SchedulerKindPush}, {intake, SchedulerKindIntake}} {
		if !item.installed {
			continue
		}
		commands = append(commands, schedulerLifecycleCommand(paths, item.kind, operation))
		if failFirst {
			break
		}
	}
	return commands
}

func schedulerStateCommand(paths *Paths, kind SchedulerKind) string {
	if runtime.GOOS == "darwin" {
		return fmt.Sprintf("print gui/%d/%s", os.Getuid(), paths.LaunchdLabelFor(kind))
	}
	return "--user is-active " + filepath.Base(paths.SystemdTimerFor(kind))
}

func schedulerLifecycleCommand(paths *Paths, kind SchedulerKind, operation string) string {
	if runtime.GOOS == "darwin" {
		command := map[string]string{"pause": "unload", "resume": "load"}[operation]
		return command + " " + paths.PlistFor(kind)
	}
	command := map[string]string{"pause": "stop", "resume": "start"}[operation]
	return "--user " + command + " " + filepath.Base(paths.SystemdTimerFor(kind))
}

func readSchedulerLifecycleCommands(t *testing.T, record string) []string {
	t.Helper()
	body, err := os.ReadFile(record)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read service-manager argv: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(body)), "\n")
}

func schedulerStateQueryCount(commands []string) int {
	count := 0
	for _, command := range commands {
		if strings.HasPrefix(command, "print ") || strings.HasPrefix(command, "--user is-active ") {
			count++
		}
	}
	return count
}

func schedulerLifecycleActionCount(commands []string) int {
	count := 0
	for _, command := range commands {
		if strings.HasPrefix(command, "unload ") || strings.HasPrefix(command, "load ") || strings.HasPrefix(command, "--user stop ") || strings.HasPrefix(command, "--user start ") {
			count++
		}
	}
	return count
}
