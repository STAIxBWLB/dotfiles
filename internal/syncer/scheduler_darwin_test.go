//go:build darwin

package syncer

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/template"
)

func TestLaunchdPrintTarget(t *testing.T) {
	got := launchdPrintTarget(501, SchedulerKindPush.LaunchdLabel())
	want := "gui/501/com.dotfiles.sync"
	if got != want {
		t.Fatalf("launchdPrintTarget = %q, want %q", got, want)
	}
}

// StateKind must read the recorded spawn-failed dump as SchedulerSpawnFailed,
// not SchedulerRunning: launchd still answers print with exit 0 for the loaded
// job (#233).
func TestSchedulerStateKind_SpawnFailedIsNotRunning(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dump := filepath.Join(root, "print.out")
	if err := os.WriteFile(dump, []byte(recordedLaunchdSpawnFailedPrint), 0o644); err != nil {
		t.Fatal(err)
	}
	writeStub(t, filepath.Join(binDir, "launchctl"), "#!/bin/sh\n/bin/cat \"$DOTFILES_TEST_LAUNCHCTL_DUMP\"\nexit 0\n")
	t.Setenv("PATH", binDir)
	t.Setenv("DOTFILES_TEST_LAUNCHCTL_DUMP", dump)

	paths := pathsFor(root, filepath.Join(root, "cache"))
	plist := paths.PlistFor(SchedulerKindPush)
	if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plist, []byte("persisted plist"), 0o644); err != nil {
		t.Fatal(err)
	}

	scheduler := NewScheduler(
		exec.NewRunner(false, slog.New(slog.NewTextHandler(io.Discard, nil))),
		paths,
		&Config{},
		template.NewEngine(),
	)
	if got := scheduler.StateKind(context.Background(), SchedulerKindPush); got != SchedulerSpawnFailed {
		t.Fatalf("StateKind = %s, want spawn failed", got)
	}
	snapshot := scheduler.InspectKind(context.Background(), SchedulerKindPush)
	if snapshot.LastExitCode == nil || *snapshot.LastExitCode != 78 {
		t.Fatalf("last exit code = %v, want 78", snapshot.LastExitCode)
	}
}

func TestSyncPauseResumeReloadsSpawnFailedJobs(t *testing.T) {
	cfg, record, paths := schedulerLifecycleConfig(t, true, true, "pause", false)
	stub := `#!/bin/sh
printf '%s\n' "$*" >> "$DOTFILES_TEST_SCHEDULER_ARGS"
case "$1" in
  print)
    label="${2##*/}"
    [ ! -f "$HOME/Library/LaunchAgents/$label.plist.unloaded" ] || exit 1
    printf 'state = spawn failed\nlast exit code = 78: EX_CONFIG\n'
    ;;
  unload) : > "$2.unloaded" ;;
  load) [ -f "$2.unloaded" ] || exit 42 ;;
  *) exit 99 ;;
esac
`
	writeStub(t, filepath.Join(os.Getenv("PATH"), "launchctl"), stub)
	runner := exec.NewRunner(false, slog.New(slog.NewTextHandler(io.Discard, nil)))
	paused, err := SyncPause(context.Background(), cfg, runner)
	if err != nil || paused.SchedulerErr != nil || !paused.SchedulerStopped {
		t.Fatalf("pause did not unload failed jobs: %+v, %v", paused, err)
	}
	resumed, err := SyncResume(context.Background(), cfg, runner)
	if err != nil || resumed.SchedulerErr != nil || !resumed.SchedulerResumed {
		t.Fatalf("resume did not reload failed jobs: %+v, %v", resumed, err)
	}
	want := append(schedulerLifecycleCommands(paths, true, true, "pause", false), schedulerLifecycleCommands(paths, true, true, "resume", false)...)
	got := readSchedulerLifecycleCommands(t, record)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("service-manager commands = %q, want unload before load: %q", got, want)
	}
}

func TestLaunchdStateFromPrintStatus(t *testing.T) {
	cases := []struct {
		name        string
		plistExists bool
		printOK     bool
		want        SchedulerState
	}{
		{"missing plist", false, true, SchedulerNotInstalled},
		{"loaded", true, true, SchedulerRunning},
		{"not loaded", true, false, SchedulerStopped},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := launchdStateFromPrintStatus(tc.plistExists, tc.printOK)
			if got != tc.want {
				t.Fatalf("state = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestSchedulerStateKind_ProfiledLaunchdTarget(t *testing.T) {
	for _, tc := range []struct {
		name    string
		profile string
		kind    SchedulerKind
	}{
		{name: "default push", profile: DefaultProfile, kind: SchedulerKindPush},
		{name: "default intake", profile: DefaultProfile, kind: SchedulerKindIntake},
		{name: "custom push", profile: "research", kind: SchedulerKindPush},
		{name: "custom intake", profile: "research", kind: SchedulerKindIntake},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			binDir := filepath.Join(root, "bin")
			if err := os.MkdirAll(binDir, 0o755); err != nil {
				t.Fatal(err)
			}
			record := filepath.Join(root, "launchctl-args")
			writeStub(t, filepath.Join(binDir, "launchctl"), "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$DOTFILES_TEST_LAUNCHCTL_ARGS\"\nexit 0\n")
			t.Setenv("PATH", binDir)
			t.Setenv("DOTFILES_TEST_LAUNCHCTL_ARGS", record)

			paths := withProfile(pathsFor(root, filepath.Join(root, "cache")), tc.profile)
			plist := paths.PlistFor(tc.kind)
			if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(plist, []byte("persisted plist"), 0o644); err != nil {
				t.Fatal(err)
			}

			scheduler := NewScheduler(
				exec.NewRunner(false, slog.New(slog.NewTextHandler(io.Discard, nil))),
				paths,
				&Config{},
				template.NewEngine(),
			)
			if got := scheduler.StateKind(context.Background(), tc.kind); got != SchedulerRunning {
				t.Fatalf("StateKind = %s, want running", got)
			}
			got, err := os.ReadFile(record)
			if err != nil {
				t.Fatalf("read launchctl arguments: %v", err)
			}
			want := "print\n" + launchdPrintTarget(os.Getuid(), paths.LaunchdLabelFor(tc.kind))
			if strings.TrimSpace(string(got)) != want {
				t.Fatalf("launchctl arguments = %q, want %q", strings.TrimSpace(string(got)), want)
			}
		})
	}
}

func TestSchedulerInstallKind_ProfiledPlist(t *testing.T) {
	for _, kind := range []SchedulerKind{SchedulerKindPush, SchedulerKindIntake} {
		t.Run(kind.Action(), func(t *testing.T) {
			root := t.TempDir()
			binDir := filepath.Join(root, "bin")
			if err := os.MkdirAll(binDir, 0o755); err != nil {
				t.Fatal(err)
			}
			writeStub(t, filepath.Join(binDir, "dot"), "#!/bin/sh\nexit 0\n")
			writeStub(t, filepath.Join(binDir, "launchctl"), "#!/bin/sh\nexit 0\n")
			t.Setenv("PATH", binDir)

			paths := withProfile(pathsFor(root, filepath.Join(root, "cache")), "research")
			scheduler := NewScheduler(
				exec.NewRunner(false, slog.New(slog.NewTextHandler(io.Discard, nil))),
				paths,
				&Config{LocalPath: filepath.Join(root, "workspace"), LogFile: filepath.Join(root, "sync.log"), Interval: 60},
				template.NewEngine(),
			)
			if err := scheduler.InstallKind(context.Background(), kind); err != nil {
				t.Fatalf("InstallKind: %v", err)
			}
			body, err := os.ReadFile(paths.PlistFor(kind))
			if err != nil {
				t.Fatalf("read persisted plist: %v", err)
			}
			label, args := plistLabelAndProgramArguments(t, body)
			if label != paths.LaunchdLabelFor(kind) {
				t.Fatalf("persisted Label = %q, want %q", label, paths.LaunchdLabelFor(kind))
			}
			if got := matchingProfileArguments(args); len(got) != 1 || got[0] != "--profile=research" {
				t.Fatalf("profile arguments = %q, want exactly --profile=research", got)
			}
		})
	}
}

func TestSchedulerInstallKind_PlistPathFieldsRoundTrip(t *testing.T) {
	root := filepath.Join(t.TempDir(), "paths & <and>")
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeStub(t, filepath.Join(binDir, "dot"), "#!/bin/sh\nexit 0\n")
	writeStub(t, filepath.Join(binDir, "launchctl"), "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", binDir)

	localPath := filepath.Join(root, "workspace")
	logFile := filepath.Join(localPath, "logs", "sync.log")
	plistPath := filepath.Join(root, "Library", "LaunchAgents", "com.dotfiles.sync.plist")
	scheduler := NewScheduler(
		exec.NewRunner(false, slog.New(slog.NewTextHandler(io.Discard, nil))),
		&Paths{LaunchdPlist: plistPath},
		&Config{Home: root, LocalPath: localPath, LogFile: logFile, Interval: 60},
		template.NewEngine(),
	)

	if err := scheduler.InstallKind(context.Background(), SchedulerKindPush); err == nil || !strings.Contains(err.Error(), "no service-manager action ran in the caller domain") {
		t.Fatalf("InstallKind explicit-home error = %v, want target-user instruction", err)
	}
	body, err := os.ReadFile(plistPath)
	if err != nil {
		t.Fatalf("read persisted plist: %v", err)
	}
	var plist plistProgramArguments
	if err := xml.Unmarshal(body, &plist); err != nil {
		t.Fatalf("persisted plist must parse: %v\n%s", err, body)
	}
	if len(plist.ProgramArguments) == 0 || plist.ProgramArguments[0] != filepath.Join(binDir, "dot") {
		t.Fatalf("ProgramArguments[0] = %q, want %q", plist.ProgramArguments, filepath.Join(binDir, "dot"))
	}
	if got := matchingHomeArguments(plist.ProgramArguments); len(got) != 1 || got[0] != "--home="+root {
		t.Fatalf("home arguments = %q, want %q", got, "--home="+root)
	}
	paths := plistStandardPaths(t, body)
	for _, key := range []string{"StandardOutPath", "StandardErrorPath"} {
		if paths[key] != logFile {
			t.Errorf("%s = %q, want %q", key, paths[key], logFile)
		}
	}
}

func TestSchedulerInstallKind_RejectsUnrepresentablePlistPathBeforeMutation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		field      string
		value      string
		executable bool
	}{
		{name: "invalid executable UTF-8", field: "executable", value: string([]byte("/tmp/dot-\xff")), executable: true},
		{name: "invalid executable control", field: "executable", value: "/tmp/dot-\x01", executable: true},
		{name: "invalid log UTF-8", field: "log file", value: string([]byte("/tmp/log-\xff"))},
		{name: "invalid log control", field: "log file", value: "/tmp/log-\x01"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			plistPath := filepath.Join(root, "Library", "LaunchAgents", "com.dotfiles.sync.plist")
			if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
				t.Fatal(err)
			}
			const seeded = "seeded shared plist"
			if err := os.WriteFile(plistPath, []byte(seeded), 0o644); err != nil {
				t.Fatal(err)
			}
			var actions bytes.Buffer
			runner := exec.NewRunner(true, slog.New(slog.NewTextHandler(&actions, &slog.HandlerOptions{Level: slog.LevelDebug})))
			scheduler := NewScheduler(runner, &Paths{LaunchdPlist: plistPath}, &Config{
				Home: root, LocalPath: filepath.Join(root, "workspace"), LogFile: filepath.Join(root, "sync.log"), Interval: 60,
			}, template.NewEngine())
			if tc.executable {
				previous := schedulerLookPath
				schedulerLookPath = func(string) (string, error) { return tc.value, nil }
				t.Cleanup(func() { schedulerLookPath = previous })
			} else {
				scheduler.Config.LogFile = tc.value
				previous := schedulerLookPath
				schedulerLookPath = func(string) (string, error) { return "/tmp/dot", nil }
				t.Cleanup(func() { schedulerLookPath = previous })
			}

			err := scheduler.InstallKind(context.Background(), SchedulerKindPush)
			if err == nil {
				t.Fatal("unrepresentable plist path unexpectedly reached mutation")
			}
			for _, want := range []string{tc.field, fmt.Sprintf("%q", tc.value), plistPath, "XML 1.0", "left untouched", "dot sync setup"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q missing %q", err, want)
				}
			}
			if got, readErr := os.ReadFile(plistPath); readErr != nil || string(got) != seeded {
				t.Fatalf("rejection changed seeded plist: %q, %v", got, readErr)
			}
			if temps, globErr := filepath.Glob(filepath.Join(filepath.Dir(plistPath), ".dot-write-*")); globErr != nil || len(temps) != 0 {
				t.Fatalf("rejection left temporary plist files: %v, %v", temps, globErr)
			}
			if actions.Len() != 0 {
				t.Fatalf("rejection performed runner or launchctl action:\n%s", actions.String())
			}
		})
	}
}

func TestSchedulerMutators_RejectInvalidHomeBeforeMutation(t *testing.T) {
	for _, invalidHome := range []string{
		string([]byte("/tmp/dot-\xff")),
		"/tmp/dot-\x01",
	} {
		for _, action := range darwinMutatorActions() {
			t.Run(action.name+"/"+fmt.Sprintf("%q", invalidHome), func(t *testing.T) {
				scheduler, paths, seeded, actions := darwinMutatorSandbox(t, invalidHome)
				if err := action.run(scheduler); err == nil {
					t.Fatal("invalid home unexpectedly reached Darwin mutation")
				}
				assertDarwinSeedsUntouched(t, paths, seeded)
				if actions.Len() != 0 {
					t.Fatalf("invalid home performed filesystem or launchctl action:\n%s", actions.String())
				}
			})
		}
	}

	for _, action := range darwinMutatorActions() {
		t.Run("valid non-vacuity/"+action.name, func(t *testing.T) {
			scheduler, _, _, actions := darwinMutatorSandbox(t, "")
			binDir := t.TempDir()
			writeStub(t, filepath.Join(binDir, "dot"), "#!/bin/sh\nexit 0\n")
			t.Setenv("PATH", binDir)
			if err := action.run(scheduler); err != nil {
				t.Fatalf("valid %s: %v", action.name, err)
			}
			if actions.Len() == 0 {
				t.Fatalf("valid %s produced no runner action, so the invalid fixture is vacuous", action.name)
			}
		})
	}
}

func TestSchedulerExplicitHome_DarwinServiceDomain(t *testing.T) {
	for _, tc := range []struct {
		name        string
		prepare     func(t *testing.T, paths *Paths)
		run         func(*Scheduler) error
		wantPresent []SchedulerKind
		wantAbsent  []SchedulerKind
		legacy      bool
	}{
		{
			name: "install stages artifacts only",
			run: func(s *Scheduler) error {
				return s.Install(context.Background())
			},
			wantPresent: []SchedulerKind{SchedulerKindPush, SchedulerKindIntake},
			legacy:      true,
		},
		{
			name: "uninstall retires artifacts only",
			prepare: func(t *testing.T, paths *Paths) {
				seedDarwinSchedulerArtifacts(t, paths)
			},
			run: func(s *Scheduler) error {
				return s.Uninstall(context.Background())
			},
			wantAbsent: []SchedulerKind{SchedulerKindPush, SchedulerKindIntake},
			legacy:     true,
		},
		{
			name: "pause leaves persisted artifacts alone",
			prepare: func(t *testing.T, paths *Paths) {
				seedDarwinSchedulerArtifacts(t, paths)
			},
			run: func(s *Scheduler) error {
				return s.PauseKind(context.Background(), SchedulerKindPush)
			},
			wantPresent: []SchedulerKind{SchedulerKindPush},
		},
		{
			name: "resume leaves persisted artifacts alone",
			prepare: func(t *testing.T, paths *Paths) {
				seedDarwinSchedulerArtifacts(t, paths)
			},
			run: func(s *Scheduler) error {
				return s.ResumeKind(context.Background(), SchedulerKindPush)
			},
			wantPresent: []SchedulerKind{SchedulerKindPush},
		},
		{
			name: "legacy cleanup retires files only",
			prepare: func(t *testing.T, paths *Paths) {
				seedDarwinLegacyArtifacts(t, paths)
			},
			run: func(s *Scheduler) error {
				return s.CleanupLegacyUnits(context.Background())
			},
			legacy: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := t.TempDir()
			paths := pathsFor(target, filepath.Join(target, "cache"))
			binDir := filepath.Join(t.TempDir(), "bin")
			if err := os.MkdirAll(binDir, 0o755); err != nil {
				t.Fatal(err)
			}
			record := filepath.Join(t.TempDir(), "launchctl-args")
			writeStub(t, filepath.Join(binDir, "dot"), "#!/bin/sh\nexit 0\n")
			writeStub(t, filepath.Join(binDir, "launchctl"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$DOTFILES_TEST_LAUNCHCTL_ARGS\"\nexit 0\n")
			t.Setenv("PATH", binDir)
			t.Setenv("DOTFILES_TEST_LAUNCHCTL_ARGS", record)

			if tc.prepare != nil {
				tc.prepare(t, paths)
			}
			scheduler := NewScheduler(
				exec.NewRunner(false, slog.New(slog.NewTextHandler(io.Discard, nil))),
				paths,
				&Config{Home: target, LocalPath: filepath.Join(target, "workspace"), LogFile: filepath.Join(target, "sync.log"), Interval: 60, PullInterval: 120},
				template.NewEngine(),
			)
			err := tc.run(scheduler)
			if err == nil || !strings.Contains(err.Error(), "no service-manager action ran in the caller domain") {
				t.Fatalf("explicit-home action error = %v, want target-user instruction", err)
			}
			if got := readDarwinSchedulerActions(t, record); len(got) != 0 {
				t.Fatalf("explicit-home action invoked caller launchctl: %q", got)
			}
			for _, kind := range tc.wantPresent {
				if !scheduler.Runner.FileExists(paths.PlistFor(kind)) {
					t.Errorf("%s plist was not staged", kind.Action())
				}
			}
			for _, kind := range tc.wantAbsent {
				if scheduler.Runner.FileExists(paths.PlistFor(kind)) {
					t.Errorf("%s plist was not retired", kind.Action())
				}
			}
			for _, label := range legacyLaunchdLabels {
				path := filepath.Join(filepath.Dir(paths.LaunchdPlist), label+".plist")
				if tc.legacy && scheduler.Runner.FileExists(path) {
					t.Errorf("legacy plist %s was not retired", label)
				}
			}
		})
	}

	t.Run("status is actionable without a caller query", func(t *testing.T) {
		target := t.TempDir()
		paths := pathsFor(target, filepath.Join(target, "cache"))
		seedDarwinSchedulerArtifacts(t, paths)
		record := filepath.Join(t.TempDir(), "launchctl-args")
		binDir := filepath.Join(t.TempDir(), "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeStub(t, filepath.Join(binDir, "launchctl"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$DOTFILES_TEST_LAUNCHCTL_ARGS\"\nexit 0\n")
		t.Setenv("PATH", binDir)
		t.Setenv("DOTFILES_TEST_LAUNCHCTL_ARGS", record)
		scheduler := NewScheduler(exec.NewRunner(false, slog.New(slog.NewTextHandler(io.Discard, nil))), paths, &Config{Home: target}, template.NewEngine())
		if got := scheduler.StateKind(context.Background(), SchedulerKindPush).String(); !strings.Contains(got, "target user") {
			t.Fatalf("explicit-home state = %q, want actionable target-user state", got)
		}
		if got := readDarwinSchedulerActions(t, record); len(got) != 0 {
			t.Fatalf("explicit-home status invoked caller launchctl: %q", got)
		}
	})
}

func seedDarwinSchedulerArtifacts(t *testing.T, paths *Paths) {
	t.Helper()
	for _, kind := range []SchedulerKind{SchedulerKindPush, SchedulerKindIntake} {
		path := paths.PlistFor(kind)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("persisted plist"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func seedDarwinLegacyArtifacts(t *testing.T, paths *Paths) {
	t.Helper()
	for _, label := range legacyLaunchdLabels {
		path := filepath.Join(filepath.Dir(paths.LaunchdPlist), label+".plist")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("legacy plist"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func readDarwinSchedulerActions(t *testing.T, record string) []string {
	t.Helper()
	body, err := os.ReadFile(record)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(body)), "\n")
}

type darwinMutatorAction struct {
	name string
	run  func(*Scheduler) error
}

func darwinMutatorActions() []darwinMutatorAction {
	return []darwinMutatorAction{
		{name: "InstallKind", run: func(s *Scheduler) error { return s.InstallKind(context.Background(), SchedulerKindPush) }},
		{name: "UninstallKind", run: func(s *Scheduler) error { return s.UninstallKind(context.Background(), SchedulerKindPush) }},
		{name: "PauseKind", run: func(s *Scheduler) error { return s.PauseKind(context.Background(), SchedulerKindPush) }},
		{name: "ResumeKind", run: func(s *Scheduler) error { return s.ResumeKind(context.Background(), SchedulerKindPush) }},
		{name: "CleanupLegacyUnits", run: func(s *Scheduler) error { return s.CleanupLegacyUnits(context.Background()) }},
	}
}

func darwinMutatorSandbox(t *testing.T, home string) (*Scheduler, *Paths, map[string]string, *bytes.Buffer) {
	t.Helper()
	root := t.TempDir()
	paths := &Paths{LaunchdPlist: filepath.Join(root, "Library", "LaunchAgents", "com.dotfiles.sync.plist")}
	seeded := map[string]string{}
	for _, plist := range []string{paths.PlistFor(SchedulerKindPush), paths.PlistFor(SchedulerKindIntake)} {
		seeded[plist] = "seeded current " + filepath.Base(plist)
	}
	for _, label := range legacyLaunchdLabels {
		plist := filepath.Join(filepath.Dir(paths.LaunchdPlist), label+".plist")
		seeded[plist] = "seeded legacy " + label
	}
	for path, content := range seeded {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var actions bytes.Buffer
	runner := exec.NewRunner(true, slog.New(slog.NewTextHandler(&actions, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return NewScheduler(runner, paths, &Config{
		Home: home, LocalPath: filepath.Join(root, "workspace"), LogFile: filepath.Join(root, "sync.log"), Interval: 60,
	}, template.NewEngine()), paths, seeded, &actions
}

func assertDarwinSeedsUntouched(t *testing.T, paths *Paths, seeded map[string]string) {
	t.Helper()
	for path, want := range seeded {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("mutation changed seeded artifact %s: %q, %v", path, got, err)
		}
	}
	if temps, err := filepath.Glob(filepath.Join(filepath.Dir(paths.LaunchdPlist), ".dot-write-*")); err != nil || len(temps) != 0 {
		t.Fatalf("mutation left temporary plist files: %v, %v", temps, err)
	}
}

func plistStandardPaths(t *testing.T, body []byte) map[string]string {
	t.Helper()
	decoder := xml.NewDecoder(strings.NewReader(string(body)))
	values := map[string]string{}
	var key, text string
	var inKey, inString bool
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return values
		}
		if err != nil {
			t.Fatalf("decode plist: %v", err)
		}
		switch token := token.(type) {
		case xml.StartElement:
			text = ""
			inKey = token.Name.Local == "key"
			inString = token.Name.Local == "string"
		case xml.CharData:
			if inKey || inString {
				text += string(token)
			}
		case xml.EndElement:
			switch token.Name.Local {
			case "key":
				key, inKey = text, false
			case "string":
				if key == "StandardOutPath" || key == "StandardErrorPath" {
					values[key] = text
				}
				inString = false
			}
		}
	}
}

func plistLabelAndProgramArguments(t *testing.T, body []byte) (string, []string) {
	t.Helper()
	decoder := xml.NewDecoder(bytes.NewReader(body))
	var key, text, label string
	var args []string
	var inKey, inString bool
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return label, args
		}
		if err != nil {
			t.Fatalf("decode plist: %v", err)
		}
		switch token := token.(type) {
		case xml.StartElement:
			text = ""
			inKey = token.Name.Local == "key"
			inString = token.Name.Local == "string"
		case xml.CharData:
			if inKey || inString {
				text += string(token)
			}
		case xml.EndElement:
			switch token.Name.Local {
			case "key":
				key, inKey = text, false
			case "string":
				if key == "Label" {
					label = text
				}
				if key == "ProgramArguments" {
					args = append(args, text)
				}
				inString = false
			}
		}
	}
}

func matchingProfileArguments(args []string) []string {
	var found []string
	for _, arg := range args {
		if strings.HasPrefix(arg, "--profile=") {
			found = append(found, arg)
		}
	}
	return found
}
