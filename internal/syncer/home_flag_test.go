package syncer

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/template"
)

// Threading --home into Bootstrap moved the PROFILE onto the target home and
// left these three engine sites resolving the process home, so one command
// operated on two homes at once. Each row asserts the argv, the path or the
// rendered unit a wrong home would land in, not merely that a home was passed.

// homeFlagSandbox returns an invoking home (installed as the process HOME) and
// a separate target home, with a Config whose Home names the target.
func homeFlagSandbox(t *testing.T) (invoker, target string, cfg *Config) {
	t.Helper()
	invoker = t.TempDir()
	target = t.TempDir()
	t.Setenv("HOME", invoker)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(invoker, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(invoker, ".cache"))

	localPath := filepath.Join(target, "workspace", "work")
	systemPaths, err := resolvePathsForHomeProfile(target, PeerProfile)
	if err != nil {
		t.Fatalf("resolvePathsForHomeProfile: %v", err)
	}
	cfg = &Config{
		Profile:     PeerProfile,
		Home:        target,
		LocalPath:   localPath,
		Target:      Target{Kind: TargetSSH, Host: "coordinator.example", Path: "/remote/workspace/work"},
		LogFile:     filepath.Join(localPath, ".dotfiles", "peer", "log", "peer.log"),
		LocalPaths:  ResolveLocalPathsForProfile(localPath, PeerProfile),
		SystemPaths: systemPaths,
	}
	return invoker, target, cfg
}

// TestPeerHomeSync_TransfersAgainstTheTargetHome is F1. peerHomeSync rsyncs the
// host-path list in both directions with the home as source and destination;
// resolving that home from the process meant `dot peer sync --home <other>`
// pulled the peer's .ssh and shell secrets over the INVOKING user's, which is
// how the first real run deleted this machine's known_hosts entry.
func TestPeerHomeSync_TransfersAgainstTheTargetHome(t *testing.T) {
	invoker, target, cfg := homeFlagSandbox(t)

	if err := os.MkdirAll(cfg.LocalPaths.StoreDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(PeerHomePathsFile(cfg.LocalPaths), []byte(".ssh\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A stub rsync that records its argv: the transfer IS the argv, so this is
	// the only place a wrong home is observable.
	binDir := t.TempDir()
	argvLog := filepath.Join(binDir, "argv.log")
	writeStub(t, filepath.Join(binDir, "rsync"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> "+argvLog+"\n")
	t.Setenv("PATH", binDir)

	if err := peerHomeSync(context.Background(), peerScheduleRunner(false), cfg, nil, false, false, false); err != nil {
		t.Fatalf("peerHomeSync: %v", err)
	}

	logged, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatalf("the stub rsync was never invoked: %v", err)
	}
	argv := string(logged)
	if strings.Count(strings.TrimSpace(argv), "\n") != 1 {
		t.Fatalf("want one pull and one push invocation, got:\n%s", argv)
	}
	// The endpoints, not the whole argv: --files-from names a path under the
	// target home too, so a substring search would pass vacuously.
	remote := cfg.Target.Host + ":"
	if !strings.Contains(argv, remote+" "+target+"/") {
		t.Errorf("the pull did not land in the target home:\n%s", argv)
	}
	if !strings.Contains(argv, target+"/ "+remote) {
		t.Errorf("the push did not come from the target home:\n%s", argv)
	}
	if strings.Contains(argv, remote+" "+invoker+"/") || strings.Contains(argv, invoker+"/ "+remote) {
		t.Errorf("peer home sync read or wrote the invoking user's home:\n%s", argv)
	}
}

// TestPeerHomeSync_WithoutOverrideUsesProcessHome is the non-vacuity row: an
// empty Config.Home must still resolve the process home, or the fix would be a
// hardcoded sandbox.
func TestPeerHomeSync_WithoutOverrideUsesProcessHome(t *testing.T) {
	invoker, _, cfg := homeFlagSandbox(t)
	cfg.Home = ""

	if err := os.MkdirAll(cfg.LocalPaths.StoreDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(PeerHomePathsFile(cfg.LocalPaths), []byte(".ssh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	argvLog := filepath.Join(binDir, "argv.log")
	writeStub(t, filepath.Join(binDir, "rsync"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> "+argvLog+"\n")
	t.Setenv("PATH", binDir)

	if err := peerHomeSync(context.Background(), peerScheduleRunner(false), cfg, nil, false, false, false); err != nil {
		t.Fatalf("peerHomeSync: %v", err)
	}
	logged, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatalf("the stub rsync was never invoked: %v", err)
	}
	remote := cfg.Target.Host + ":"
	if !strings.Contains(string(logged), remote+" "+invoker+"/") {
		t.Errorf("without an override the transfer left the process home:\n%s", string(logged))
	}
}

// TestResolveConfig_SystemPathsFollowTheTargetHome is F2's first half: the unit
// file itself was written under the invoking user's LaunchAgents/systemd
// directory even when the profile it schedules lives in another home.
//
// The subject is resolveConfig, the one point where a home and a profile are
// both in hand. Config.SystemPaths is now what every scheduler artifact is
// derived from, so this is where a resolver that stopped following the home
// would be observable; asserting it through ResolveScheduler would only
// re-read the pointer the caller handed in (RES-01, internal/syncer/sync.go
// resolveConfig is the single writer of Config.SystemPaths).
func TestResolveConfig_SystemPathsFollowTheTargetHome(t *testing.T) {
	invoker, target, _ := homeFlagSandbox(t)

	state := &config.UserState{}
	state.Modules.Gsync.LocalPath = filepath.Join(target, "workspace", "work")
	resolved, err := ResolveConfigForHomeProfile(state, target, PeerProfile)
	if err != nil {
		t.Fatalf("ResolveConfigForHomeProfile: %v", err)
	}
	if resolved.SystemPaths == nil {
		t.Fatal("the resolved config carries no SystemPaths")
	}
	paths := resolved.SystemPaths
	for _, unit := range []string{paths.LaunchdPlist, paths.SystemdService, paths.SystemdTimer, paths.LogFile} {
		if !strings.HasPrefix(unit, target+string(os.PathSeparator)) {
			t.Errorf("scheduler artifact outside the target home: %s", unit)
		}
		if strings.HasPrefix(unit, invoker+string(os.PathSeparator)) {
			t.Errorf("scheduler artifact under the invoking user's home: %s", unit)
		}
	}
	// The scheduler and Config paths share the target's lock domain. Its layout
	// follows cacheDirForHome: Library/Caches on darwin, .cache everywhere else.
	// The !darwin companion test pins real lock acquisition under that layout.
	wantLockDir := filepath.Join(target, ".cache", "dotfiles", "sync.lock")
	if runtime.GOOS == "darwin" {
		wantLockDir = filepath.Join(target, "Library", "Caches", "dotfiles", "sync.lock")
	}
	if paths.LockDir != wantLockDir {
		t.Errorf("scheduler lock = %q, want target lock %q", paths.LockDir, wantLockDir)
	}
	if resolved.LockDir != wantLockDir {
		t.Errorf("Config.LockDir = %q, want target lock %q", resolved.LockDir, wantLockDir)
	}
	if strings.HasPrefix(resolved.LockDir, invoker+string(os.PathSeparator)) {
		t.Errorf("Config.LockDir escaped into the invoking home: %s", resolved.LockDir)
	}

	// Non-vacuity: no override, and the same resolver call resolves the
	// process home. This arm fails if the fix is a hardcoded sandbox.
	bare, err := ResolveConfigForHomeProfile(state, "", PeerProfile)
	if err != nil {
		t.Fatalf("ResolveConfigForHomeProfile(no override): %v", err)
	}
	if !strings.HasPrefix(bare.SystemPaths.LaunchdPlist, invoker+string(os.PathSeparator)) {
		t.Errorf("without an override the scheduler artifact left the process home: %s", bare.SystemPaths.LaunchdPlist)
	}
}

// TestRenderedSchedulerUnitCarriesHome is F2's second half, and the half that
// outlives the command: the unit runs `dot sync ...` on a timer. Without the
// override in the rendered ProgramArguments, every future scheduled execution
// operates on the invoking user's workspace.
func TestRenderedSchedulerUnitCarriesHome(t *testing.T) {
	_, target, cfg := homeFlagSandbox(t)
	cfg.Profile = DefaultProfile
	cfg.Interval = 600

	sched := NewScheduler(peerScheduleRunner(true), &Paths{}, cfg, template.NewEngine())
	for _, tmpl := range []string{"sync/com.dotfiles.sync.plist.tmpl", "sync/dotfiles-sync.service.tmpl"} {
		data := sched.templateDataFor(SchedulerKindPush)
		if tmpl == "sync/com.dotfiles.sync.plist.tmpl" {
			if err := preparePlistTemplateData(&data); err != nil {
				t.Fatalf("prepare plist data: %v", err)
			}
		}
		body, err := sched.Engine.Render(tmpl, data)
		if err != nil {
			t.Fatalf("rendering %s: %v", tmpl, err)
		}
		if !strings.Contains(string(body), "--home="+target) {
			t.Errorf("%s renders a unit that runs without the override:\n%s", tmpl, string(body))
		}

		// Non-vacuity: no override, no flag. A unit that always carried one
		// would pin the running user's home into every machine's scheduler.
		cfg.Home = ""
		data = sched.templateDataFor(SchedulerKindPush)
		if tmpl == "sync/com.dotfiles.sync.plist.tmpl" {
			if err := preparePlistTemplateData(&data); err != nil {
				t.Fatalf("prepare empty-home plist data: %v", err)
			}
		}
		body, err = sched.Engine.Render(tmpl, data)
		if err != nil {
			t.Fatalf("rendering %s: %v", tmpl, err)
		}
		if strings.Contains(string(body), "--home") {
			t.Errorf("%s renders --home with no override set:\n%s", tmpl, string(body))
		}
		cfg.Home = target
	}

	// The plist must recover one exact argv item rather than merely contain a
	// recognizable substring. Markup and whitespace are literal path data.
	specialHome := target + "/space & <tag> 'quote' \"double\" \\ % $ 유니코드"
	cfg.Home = specialHome
	data := sched.templateDataFor(SchedulerKindPush)
	if err := preparePlistTemplateData(&data); err != nil {
		t.Fatalf("prepare special plist data: %v", err)
	}
	body, err := sched.Engine.Render("sync/com.dotfiles.sync.plist.tmpl", data)
	if err != nil {
		t.Fatalf("rendering special plist: %v", err)
	}
	var plist plistProgramArguments
	if err := xml.Unmarshal(body, &plist); err != nil {
		t.Fatalf("special plist must parse: %v\n%s", err, body)
	}
	if got := matchingHomeArguments(plist.ProgramArguments); !reflect.DeepEqual(got, []string{"--home=" + specialHome}) {
		t.Fatalf("special plist home arguments = %#v, want %#v", got, []string{"--home=" + specialHome})
	}
}

// TestPeerSchedule_WritesUnderTheTargetHome covers the sibling site Codex did
// not name: PeerSchedule resolves its own plist path and renders its own unit
// body, so it carries both halves of F2 for the peer job.
func TestPeerSchedule_WritesUnderTheTargetHome(t *testing.T) {
	cfg, _ := peerScheduleSandbox(t)
	invoker, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	cfg.Home = target
	launchctlRecord := filepath.Join(t.TempDir(), "launchctl-args")
	t.Setenv("DOTFILES_TEST_LAUNCHCTL_ARGS", launchctlRecord)

	res, err := PeerSchedule(context.Background(), PeerScheduleOptions{
		Config:   cfg,
		Runner:   peerScheduleRunner(false),
		Probe:    peerScheduleRunner(false),
		Interval: time.Hour,
	})
	var targetUserErr *SchedulerTargetUserActionRequiredError
	if !errors.As(err, &targetUserErr) {
		t.Fatalf("PeerSchedule error = %v, want target-user action", err)
	}
	if res == nil {
		t.Fatal("PeerSchedule returned no staged result")
	}
	if got, readErr := os.ReadFile(launchctlRecord); readErr == nil || !os.IsNotExist(readErr) {
		t.Fatalf("explicit-home schedule invoked caller launchctl: %q (read error %v)", got, readErr)
	}
	if !strings.HasPrefix(res.Plist, target+string(os.PathSeparator)) {
		t.Errorf("peer plist written outside the target home: %s", res.Plist)
	}
	if strings.HasPrefix(res.Plist, invoker+string(os.PathSeparator)) {
		t.Errorf("peer plist written under the invoking user's home: %s", res.Plist)
	}
	body, err := os.ReadFile(res.Plist)
	if err != nil {
		t.Fatalf("reading the written plist: %v", err)
	}
	var plist plistProgramArguments
	if err := xml.Unmarshal(body, &plist); err != nil {
		t.Fatalf("the written peer plist must parse: %v\n%s", err, body)
	}
	if got := matchingHomeArguments(plist.ProgramArguments); !reflect.DeepEqual(got, []string{"--home=" + target}) {
		t.Errorf("peer plist home arguments = %#v, want %#v", got, []string{"--home=" + target})
	}
}

func TestPeerSchedule_ExplicitHomeOffRemovesFileWithoutCallerLaunchctl(t *testing.T) {
	cfg, _ := peerScheduleSandbox(t)
	cfg.Home = t.TempDir()
	plist := filepath.Join(cfg.Home, "Library", "LaunchAgents", "com.dotfiles.peer.plist")
	if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plist, []byte("seeded"), 0o644); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(t.TempDir(), "launchctl-args")
	t.Setenv("DOTFILES_TEST_LAUNCHCTL_ARGS", record)

	res, err := PeerSchedule(context.Background(), PeerScheduleOptions{
		Config: cfg, Runner: peerScheduleRunner(false), Probe: peerScheduleRunner(false), Off: true,
	})
	var targetUserErr *SchedulerTargetUserActionRequiredError
	if !errors.As(err, &targetUserErr) {
		t.Fatalf("PeerSchedule off error = %v, want target-user action", err)
	}
	if res == nil || !res.Off || res.Plist != plist {
		t.Fatalf("PeerSchedule off result = %+v, want removed %s", res, plist)
	}
	if _, statErr := os.Stat(plist); !os.IsNotExist(statErr) {
		t.Fatalf("explicit-home off left plist behind: %v", statErr)
	}
	if got, readErr := os.ReadFile(record); readErr == nil || !os.IsNotExist(readErr) {
		t.Fatalf("explicit-home off invoked caller launchctl: %q (read error %v)", got, readErr)
	}
}

func TestPeerSchedule_PlistPathFieldsRoundTrip(t *testing.T) {
	root := filepath.Join(t.TempDir(), "peer & <paths>")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	names := MachineNames()
	if len(names) == 0 {
		t.Skip("this host reports no machine name, so CheckOwner cannot be satisfied")
	}
	localPath := filepath.Join(root, "workspace")
	cfg := &Config{
		Profile:   PeerProfile,
		Home:      root,
		Owner:     names[0],
		LocalPath: localPath,
		Target:    Target{Kind: TargetSSH, Host: "coordinator.example", Path: "/remote/workspace/work"},
		LogFile:   filepath.Join(localPath, "logs", "peer.log"),
	}
	binDir := t.TempDir()
	status := fmt.Sprintf(`{"schemaVersion":%d,"kind":"peer","profile":{"configured":true,"owner":%q,"workspacePath":%q,"target":{"path":%q}}}`,
		PeerStatusSchemaVersion, cfg.Owner, cfg.Target.Path, localPath)
	writeStub(t, filepath.Join(binDir, "ssh"), "#!/bin/sh\ncase \"$*\" in\n  *\"list dot candidates\"*) printf '/fake/dot\\tdot version 9.9.9 (fake)\\n' ;;\n  *) echo '"+status+"' ;;\nesac\n")
	writeStub(t, filepath.Join(binDir, "launchctl"), "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", binDir)
	peerBin := filepath.Join(root, "bin")
	if err := os.MkdirAll(peerBin, 0o755); err != nil {
		t.Fatal(err)
	}
	peerPath := filepath.Join(peerBin, "dot")
	writeStub(t, peerPath, "#!/bin/sh\nexit 0\n")
	previousExecutable := peerExecutable
	peerExecutable = func() (string, error) { return peerPath, nil }
	t.Cleanup(func() { peerExecutable = previousExecutable })

	res, err := PeerSchedule(context.Background(), PeerScheduleOptions{
		Config: cfg, Runner: peerScheduleRunner(false), Probe: peerScheduleRunner(false), Interval: time.Hour,
	})
	var targetUserErr *SchedulerTargetUserActionRequiredError
	if !errors.As(err, &targetUserErr) {
		t.Fatalf("PeerSchedule error = %v, want target-user action", err)
	}
	if res == nil {
		t.Fatal("PeerSchedule returned no staged result")
	}
	body, err := os.ReadFile(res.Plist)
	if err != nil {
		t.Fatalf("read persisted peer plist: %v", err)
	}
	var plist plistProgramArguments
	if err := xml.Unmarshal(body, &plist); err != nil {
		t.Fatalf("persisted peer plist must parse: %v\n%s", err, body)
	}
	if len(plist.ProgramArguments) == 0 || plist.ProgramArguments[0] != peerPath {
		t.Fatalf("ProgramArguments[0] = %q, want %q", plist.ProgramArguments, peerPath)
	}
	if got := matchingHomeArguments(plist.ProgramArguments); !reflect.DeepEqual(got, []string{"--home=" + root}) {
		t.Fatalf("home arguments = %q, want %q", got, []string{"--home=" + root})
	}
	paths := peerPlistStandardPaths(t, body)
	for _, key := range []string{"StandardOutPath", "StandardErrorPath"} {
		if paths[key] != cfg.LogFile {
			t.Errorf("%s = %q, want %q", key, paths[key], cfg.LogFile)
		}
	}
}

func peerPlistStandardPaths(t *testing.T, body []byte) map[string]string {
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
			t.Fatalf("decode peer plist: %v", err)
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

func TestPeerSchedule_RejectsUnrepresentablePlistPathBeforeMutation(t *testing.T) {
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
			cfg, plist := peerScheduleSandbox(t)
			// This regression must remain independent of coordinator reachability:
			// malformed local plist text is rejected before peer preflight. If the
			// order regresses, the missing ssh binary returns that failure instead.
			t.Setenv("PATH", t.TempDir())
			const seeded = "seeded peer plist"
			if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(plist, []byte(seeded), 0o644); err != nil {
				t.Fatal(err)
			}
			previousExecutable := peerExecutable
			if tc.executable {
				peerExecutable = func() (string, error) { return tc.value, nil }
			} else {
				cfg.LogFile = tc.value
				peerExecutable = func() (string, error) { return "/tmp/dot", nil }
			}
			t.Cleanup(func() { peerExecutable = previousExecutable })
			var actions bytes.Buffer
			runner := exec.NewRunner(true, slog.New(slog.NewTextHandler(&actions, &slog.HandlerOptions{Level: slog.LevelDebug})))

			res, err := PeerSchedule(context.Background(), PeerScheduleOptions{
				Config: cfg, Runner: runner, Probe: peerScheduleRunner(false), Interval: time.Hour, DryRun: true,
			})
			if err == nil {
				t.Fatal("unrepresentable plist path unexpectedly reached peer scheduler mutation")
			}
			if res != nil {
				t.Fatalf("rejected peer scheduler returned result: %+v", res)
			}
			for _, want := range []string{tc.field, fmt.Sprintf("%q", tc.value), plist, "XML 1.0", "left untouched", "dot peer setup"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q missing %q", err, want)
				}
			}
			if got, readErr := os.ReadFile(plist); readErr != nil || string(got) != seeded {
				t.Fatalf("rejection changed seeded plist: %q, %v", got, readErr)
			}
			if temps, globErr := filepath.Glob(filepath.Join(filepath.Dir(plist), ".dot-write-*")); globErr != nil || len(temps) != 0 {
				t.Fatalf("rejection left temporary plist files: %v, %v", temps, globErr)
			}
			if actions.Len() != 0 {
				t.Fatalf("rejection performed runner or launchctl action:\n%s", actions.String())
			}
		})
	}
}
