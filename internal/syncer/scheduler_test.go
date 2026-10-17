package syncer

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/template"
)

type plistProgramArguments struct {
	ProgramArguments []string `xml:"dict>array>string"`
}

func renderedPlistProgramArguments(t *testing.T, data SchedulerTemplateData) []string {
	t.Helper()
	if err := preparePlistTemplateData(&data); err != nil {
		t.Fatalf("prepare plist data: %v", err)
	}
	body, err := template.NewEngine().Render("sync/com.dotfiles.sync.plist.tmpl", data)
	if err != nil {
		t.Fatalf("render plist: %v", err)
	}
	var plist plistProgramArguments
	if err := xml.Unmarshal(body, &plist); err != nil {
		t.Fatalf("parse plist: %v\n--- rendered ---\n%s", err, body)
	}
	return plist.ProgramArguments
}

func TestPlistHomeArgument_SupportedHome(t *testing.T) {
	home := "/tmp/a b\tline\nnext\rreturn & <tag> 'quote' \"double\" \\ % $ 유니코드/" + strings.Repeat("long-", 64)
	want := "--home=" + home

	got, err := plistHomeArgument(home)
	if err != nil {
		t.Fatalf("plistHomeArgument(%q): %v", home, err)
	}
	again, err := plistHomeArgument(home)
	if err != nil {
		t.Fatalf("second plistHomeArgument(%q): %v", home, err)
	}
	if got != again {
		t.Fatalf("serializer is not deterministic:\nfirst:  %q\nsecond: %q", got, again)
	}

	args := renderedPlistProgramArguments(t, SchedulerTemplateData{
		DotfilesPath: "/usr/local/bin/dot", Home: home, PlistHomeArg: got,
		LogFile: "/tmp/dot.log", Interval: 60, Label: launchdLabel,
		Action: "push", Mode: ModeClean.String(), Description: "test", ServiceName: systemdServiceName,
	})
	if gotArgs := matchingHomeArguments(args); !reflect.DeepEqual(gotArgs, []string{want}) {
		t.Fatalf("ProgramArguments home items = %q, want %q", gotArgs, []string{want})
	}
}

func TestPlistHomeArgument_RejectsXMLIllegalHome(t *testing.T) {
	cases := []struct {
		name string
		home string
	}{
		{name: "invalid UTF-8", home: string([]byte{'/', 't', 'm', 'p', '/', 0xff})},
		{name: "U+0001", home: "/tmp/bad\x01home"},
		{name: "U+000B", home: "/tmp/bad\x0bhome"},
		{name: "U+000C", home: "/tmp/bad\x0chome"},
		{name: "U+001F", home: "/tmp/bad\x1fhome"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := plistHomeArgument(tc.home)
			if err == nil {
				t.Fatalf("plistHomeArgument(%q) unexpectedly accepted XML-illegal home", tc.home)
			}
			for _, want := range []string{fmt.Sprintf("%q", tc.home), "XML 1.0", "rename or move", "valid UTF-8"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q missing %q", err, want)
				}
			}
		})
	}
}

func matchingHomeArguments(args []string) []string {
	var homes []string
	for _, arg := range args {
		if strings.HasPrefix(arg, "--home=") {
			homes = append(homes, arg)
		}
	}
	return homes
}

func TestSchedulerState_String(t *testing.T) {
	cases := map[SchedulerState]string{
		SchedulerNotInstalled: "not installed",
		SchedulerRunning:      "running",
		SchedulerStopped:      "stopped",
	}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Errorf("SchedulerState(%d).String() = %q, want %q", s, got, want)
		}
	}
}

func TestSchedulerLabels_DistinctFromRsync(t *testing.T) {
	// Stable identifiers must not collide with internal/rsync's so that
	// both schedulers can run on the same machine. Hard-code the strings
	// so a casual rename catches in review.
	if launchdLabel != "com.dotfiles.sync" {
		t.Errorf("launchdLabel = %q, want com.dotfiles.sync (must differ from rsync)", launchdLabel)
	}
	if systemdTimerName != "dotfiles-sync.timer" {
		t.Errorf("systemdTimerName = %q, want dotfiles-sync.timer (must differ from rsync)", systemdTimerName)
	}
	if systemdServiceName != "dotfiles-sync.service" {
		t.Errorf("systemdServiceName = %q, want dotfiles-sync.service", systemdServiceName)
	}
	for _, label := range []string{launchdLabel, systemdTimerName, systemdServiceName} {
		if strings.Contains(label, "workspace-sync") {
			t.Errorf("label %q collides with rsync's `com.dotfiles.workspace-sync` namespace", label)
		}
	}
}

// resolvedSchedulerConfig resolves a Config for the given home and profile
// through resolveConfig, the single writer of Config.SystemPaths, and then
// restores the scheduler cadence fields the hand-built literals used to carry.
// Every scheduler test that needs a real SystemPaths goes through here rather
// than writing the layout it is about to assert on.
func resolvedSchedulerConfig(t *testing.T, home, profile string) *Config {
	t.Helper()
	state := &config.UserState{}
	state.Modules.Gsync.LocalPath = filepath.Join(home, "workspace", "work")
	cfg, err := ResolveConfigForHomeProfile(state, home, profile)
	if err != nil {
		t.Fatalf("ResolveConfigForHomeProfile(%q): %v", profile, err)
	}
	cfg.Interval = 600
	cfg.PullInterval = 600
	cfg.PushMode = ModeClean
	return cfg
}

// TestResolveConfig_ProfilePathsDerivedFromProfile is the derivation half of
// the old TestResolveScheduler_ProfilePathAndRenderedUnitAgree. It asserts the
// per-profile scheduler identities on a layout resolveConfig produced, not on
// one the test wrote into the Config first; after RES-01 ResolveScheduler
// returns cfg.SystemPaths verbatim, so asserting there would assert the test
// against itself.
func TestResolveConfig_ProfilePathsDerivedFromProfile(t *testing.T) {
	home := t.TempDir()
	cfg := resolvedSchedulerConfig(t, home, "research-2.v1")
	paths := cfg.SystemPaths
	if paths == nil {
		t.Fatal("the resolved config carries no SystemPaths")
	}
	if paths.Profile != "research-2.v1" {
		t.Fatalf("Paths profile = %q, want normalized research-2.v1", paths.Profile)
	}

	for path, want := range map[string]string{
		"launchd plist":   "com.dotfiles.research-2.v1.plist",
		"systemd service": "dotfiles-research-2.v1.service",
		"systemd timer":   "dotfiles-research-2.v1.timer",
	} {
		var got string
		switch path {
		case "launchd plist":
			got = filepath.Base(paths.LaunchdPlist)
		case "systemd service":
			got = filepath.Base(paths.SystemdService)
		case "systemd timer":
			got = filepath.Base(paths.SystemdTimer)
		}
		if got != want {
			t.Errorf("%s path = %q, want %q", path, got, want)
		}
	}
}

// TestResolveScheduler_NilSystemPathsErrors pins the RES-01 nil contract at
// internal/syncer/sync_cmd_ops.go: ResolveScheduler reads cfg.SystemPaths and
// refuses an unresolved config rather than re-resolving one from the home and
// profile alone, which is the partial-resolution entry point this milestone
// removes.
func TestResolveScheduler_NilSystemPathsErrors(t *testing.T) {
	cfg := &Config{
		Home:     t.TempDir(),
		Profile:  "research-2.v1",
		Interval: 600,
		PushMode: ModeClean,
	}
	scheduler, paths, err := ResolveScheduler(cfg, peerScheduleRunner(true))
	if err == nil {
		t.Fatal("ResolveScheduler on a nil SystemPaths = nil error, want an error")
	}
	if scheduler != nil {
		t.Errorf("ResolveScheduler returned a scheduler alongside its error: %+v", scheduler)
	}
	if paths != nil {
		t.Errorf("ResolveScheduler returned paths alongside its error: %+v", paths)
	}
}

// TestResolveScheduler_ProfilePathAndRenderedUnitAgree keeps the persisted unit
// path, launchd label, and systemd command line coupled to the same profile.
// A scheduler that only fixes one of those identities can still run the wrong
// store unattended.
func TestResolveScheduler_ProfilePathAndRenderedUnitAgree(t *testing.T) {
	home := t.TempDir()
	cfg := resolvedSchedulerConfig(t, home, "research-2.v1")
	scheduler, paths, err := ResolveScheduler(cfg, peerScheduleRunner(true))
	if err != nil {
		t.Fatalf("ResolveScheduler: %v", err)
	}

	// Template data must read the identity Paths already normalized and used for
	// persisted files, rather than independently reading mutable Config.Profile.
	cfg.Profile = "mismatch"
	for _, kind := range []SchedulerKind{SchedulerKindPush, SchedulerKindIntake} {
		data := scheduler.templateDataFor(kind)
		if got, want := filepath.Base(paths.PlistFor(kind)), data.Label+".plist"; got != want {
			t.Errorf("%s plist basename = %q, want rendered label %q", kind.Action(), got, want)
		}
		if got, want := filepath.Base(paths.SystemdServiceFor(kind)), data.ServiceName; got != want {
			t.Errorf("%s service basename = %q, want rendered service %q", kind.Action(), got, want)
		}
		if got, want := filepath.Base(paths.SystemdTimerFor(kind)), strings.TrimSuffix(data.ServiceName, ".service")+".timer"; got != want {
			t.Errorf("%s timer basename = %q, want rendered timer %q", kind.Action(), got, want)
		}
		if data.Profile != "research-2.v1" {
			t.Errorf("%s rendered profile = %q, want research-2.v1", kind.Action(), data.Profile)
		}
		service, err := scheduler.Engine.Render("sync/dotfiles-sync.service.tmpl", data)
		if err != nil {
			t.Fatalf("render %s systemd service: %v", kind.Action(), err)
		}
		if !strings.Contains(string(service), "--profile=research-2.v1 \"--home="+home) {
			t.Errorf("%s systemd ExecStart does not carry the same profile before --home:\n%s", kind.Action(), service)
		}
		args := renderedPlistProgramArguments(t, data)
		if !containsString(args, "--profile=research-2.v1") {
			t.Errorf("%s plist profile argument = %q, want research-2.v1", kind.Action(), args)
		}
	}

	defaultCfg := resolvedSchedulerConfig(t, home, DefaultProfile)
	_, defaultPaths, err := ResolveScheduler(defaultCfg, peerScheduleRunner(true))
	if err != nil {
		t.Fatalf("ResolveScheduler(default): %v", err)
	}
	for path, want := range map[string]string{
		"launchd plist":   "com.dotfiles.sync.plist",
		"systemd service": "dotfiles-sync.service",
		"systemd timer":   "dotfiles-sync.timer",
	} {
		var got string
		switch path {
		case "launchd plist":
			got = filepath.Base(defaultPaths.LaunchdPlist)
		case "systemd service":
			got = filepath.Base(defaultPaths.SystemdService)
		case "systemd timer":
			got = filepath.Base(defaultPaths.SystemdTimer)
		}
		if got != want {
			t.Errorf("default %s path = %q, want %q", path, got, want)
		}
	}
	for _, kind := range []SchedulerKind{SchedulerKindPush, SchedulerKindIntake} {
		data := defaultPathsSchedulerData(t, defaultCfg, defaultPaths, kind)
		if data.Profile != "" {
			t.Errorf("default %s profile argument = %q, want omitted", kind.Action(), data.Profile)
		}
		if got, want := filepath.Base(defaultPaths.PlistFor(kind)), data.Label+".plist"; got != want {
			t.Errorf("default %s plist basename = %q, want %q", kind.Action(), got, want)
		}
		if got, want := filepath.Base(defaultPaths.SystemdServiceFor(kind)), data.ServiceName; got != want {
			t.Errorf("default %s service basename = %q, want %q", kind.Action(), got, want)
		}
		service, err := scheduler.Engine.Render("sync/dotfiles-sync.service.tmpl", data)
		if err != nil {
			t.Fatalf("render default %s systemd service: %v", kind.Action(), err)
		}
		if strings.Contains(string(service), "--profile=") {
			t.Errorf("default %s service unexpectedly renders --profile:\n%s", kind.Action(), service)
		}
	}
}

func defaultPathsSchedulerData(t *testing.T, cfg *Config, paths *Paths, kind SchedulerKind) SchedulerTemplateData {
	t.Helper()
	scheduler := NewScheduler(peerScheduleRunner(true), paths, cfg, template.NewEngine())
	return scheduler.templateDataFor(kind)
}

func TestPlistTemplate_RendersPushUnit(t *testing.T) {
	engine := template.NewEngine()
	data := SchedulerTemplateData{
		DotfilesPath: "/usr/local/bin/dotfiles",
		LogFile:      "/tmp/gd.log",
		Interval:     420,
		Label:        SchedulerKindPush.LaunchdLabel(),
		Action:       SchedulerKindPush.Action(),
		Mode:         ModeClean.String(),
		Description:  SchedulerKindPush.Description(),
		ServiceName:  SchedulerKindPush.SystemdServiceName(),
	}
	if err := preparePlistTemplateData(&data); err != nil {
		t.Fatalf("prepare plist data: %v", err)
	}
	out, err := engine.Render("sync/com.dotfiles.sync.plist.tmpl", data)
	if err != nil {
		t.Fatalf("render plist: %v", err)
	}
	body := string(out)
	for _, want := range []string{
		"<string>com.dotfiles.sync</string>",
		"<string>/usr/local/bin/dotfiles</string>",
		"<string>sync</string>",
		"<string>push</string>",
		"<string>--mode=clean</string>",
		"<integer>420</integer>",
		"<string>/tmp/gd.log</string>",
		// PATH must list Homebrew prefixes so launchd can prefer modern
		// rsync when installed, while CLI args remain Apple-rsync compatible.
		"<key>EnvironmentVariables</key>",
		"<key>PATH</key>",
		"/opt/homebrew/bin",
		"/usr/local/bin",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered plist missing %q\n--- got ---\n%s", want, body)
		}
	}
	// Push unit must NOT invoke the deprecated alias or the intake action.
	if strings.Contains(body, "<string>gsync</string>") {
		t.Error("push plist still references deprecated `gsync` command")
	}
	if strings.Contains(body, "<string>intake</string>") {
		t.Error("push plist leaked intake action")
	}
}

func TestPlistTemplate_RendersIntakeUnit(t *testing.T) {
	engine := template.NewEngine()
	data := SchedulerTemplateData{
		DotfilesPath: "/usr/local/bin/dotfiles",
		LogFile:      "/tmp/gd.log",
		Interval:     900,
		Label:        SchedulerKindIntake.LaunchdLabel(),
		Action:       SchedulerKindIntake.Action(),
		Mode:         ModeForce.String(),
		Description:  SchedulerKindIntake.Description(),
		ServiceName:  SchedulerKindIntake.SystemdServiceName(),
	}
	if err := preparePlistTemplateData(&data); err != nil {
		t.Fatalf("prepare intake plist data: %v", err)
	}
	out, err := engine.Render("sync/com.dotfiles.sync.plist.tmpl", data)
	if err != nil {
		t.Fatalf("render intake plist: %v", err)
	}
	body := string(out)
	for _, want := range []string{
		"<string>com.dotfiles.sync-intake</string>",
		"<string>pull</string>",
		"<string>--mode=force</string>",
		"<integer>900</integer>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("intake plist missing %q", want)
		}
	}
	if strings.Contains(body, "<string>push</string>") {
		t.Error("pull plist leaked push action")
	}
}

func TestSystemdTemplates_RenderPushUnit(t *testing.T) {
	engine := template.NewEngine()
	data := SchedulerTemplateData{
		DotfilesPath: "/home/u/.local/bin/dot",
		LogFile:      "/home/u/.local/log/g.log",
		Interval:     900,
		Label:        SchedulerKindPush.LaunchdLabel(),
		Action:       SchedulerKindPush.Action(),
		Mode:         ModeClean.String(),
		Description:  SchedulerKindPush.Description(),
		ServiceName:  SchedulerKindPush.SystemdServiceName(),
	}

	svc, err := engine.Render("sync/dotfiles-sync.service.tmpl", data)
	if err != nil {
		t.Fatalf("render service: %v", err)
	}
	if !strings.Contains(string(svc), "ExecStart=/home/u/.local/bin/dot sync push --mode=clean") {
		t.Errorf("service ExecStart wrong:\n%s", svc)
	}

	timer, err := engine.Render("sync/dotfiles-sync.timer.tmpl", data)
	if err != nil {
		t.Fatalf("render timer: %v", err)
	}
	for _, want := range []string{
		"OnUnitActiveSec=900s",
		"Unit=dotfiles-sync.service",
		"WantedBy=timers.target",
	} {
		if !strings.Contains(string(timer), want) {
			t.Errorf("timer missing %q\n%s", want, timer)
		}
	}
}

func TestSystemdTemplates_RenderIntakeUnit(t *testing.T) {
	engine := template.NewEngine()
	data := SchedulerTemplateData{
		DotfilesPath: "/home/u/.local/bin/dot",
		LogFile:      "/home/u/.local/log/g.log",
		Interval:     900,
		Label:        SchedulerKindIntake.LaunchdLabel(),
		Action:       SchedulerKindIntake.Action(),
		Mode:         ModeForce.String(),
		Description:  SchedulerKindIntake.Description(),
		ServiceName:  SchedulerKindIntake.SystemdServiceName(),
	}

	svc, err := engine.Render("sync/dotfiles-sync.service.tmpl", data)
	if err != nil {
		t.Fatalf("render intake service: %v", err)
	}
	if !strings.Contains(string(svc), "ExecStart=/home/u/.local/bin/dot sync pull --mode=force") {
		t.Errorf("pull service ExecStart wrong:\n%s", svc)
	}

	timer, err := engine.Render("sync/dotfiles-sync.timer.tmpl", data)
	if err != nil {
		t.Fatalf("render intake timer: %v", err)
	}
	if !strings.Contains(string(timer), "Unit=dotfiles-sync-intake.service") {
		t.Errorf("intake timer must reference -intake service:\n%s", timer)
	}
}

func TestSystemdTemplate_SpecialHome(t *testing.T) {
	longHome := "/tmp/a b\tline\nnext\rreturn % $ ' \" \\ ; | ( ) [ ] { } * ? ! 유니코드/" + strings.Repeat("long-", 64)
	longExpectedArg := "\"--home=/tmp/a b\\tline\\nnext\\rreturn %% $$ ' \\\" \\\\ ; | ( ) [ ] { } * ? ! 유니코드/" + strings.Repeat("long-", 64) + "\""
	cases := []struct {
		name string
		home string
		arg  string
	}{
		{name: "empty", home: "", arg: ""},
		{name: "special long literal", home: longHome, arg: longExpectedArg},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := systemdHomeArgument(tc.home); got != tc.arg {
				t.Fatalf("systemdHomeArgument(%q) = %q, want %q", tc.home, got, tc.arg)
			}
			data := SchedulerTemplateData{
				DotfilesPath: "/home/u/.local/bin/dot", Home: tc.home, SystemdHomeArg: tc.arg,
				LogFile: "/tmp/dot.log", Interval: 60, Label: launchdLabel,
				Action: "push", Mode: ModeClean.String(), Description: "test", ServiceName: systemdServiceName,
			}
			first, err := template.NewEngine().Render("sync/dotfiles-sync.service.tmpl", data)
			if err != nil {
				t.Fatalf("render service: %v", err)
			}
			second, err := template.NewEngine().Render("sync/dotfiles-sync.service.tmpl", data)
			if err != nil {
				t.Fatalf("render service a second time: %v", err)
			}
			if string(first) != string(second) {
				t.Fatalf("render is not deterministic:\nfirst:  %q\nsecond: %q", first, second)
			}
			want := "ExecStart=/home/u/.local/bin/dot sync push --mode=clean"
			if tc.arg != "" {
				want += " " + tc.arg
			}
			if !strings.Contains(string(first), want) {
				t.Fatalf("ExecStart does not preserve one target-ready home item:\n got: %q\nwant: %q", string(first), want)
			}
		})
	}
}

func TestSchedulerKind_LabelsAreDistinct(t *testing.T) {
	if SchedulerKindPush.LaunchdLabel() == SchedulerKindIntake.LaunchdLabel() {
		t.Error("push and intake share a launchd label")
	}
	if SchedulerKindPush.SystemdTimerName() == SchedulerKindIntake.SystemdTimerName() {
		t.Error("push and intake share a systemd timer name")
	}
	if SchedulerKindPush.Action() != "push" || SchedulerKindIntake.Action() != "pull" {
		t.Errorf("Action() mismatch: push=%s intake=%s",
			SchedulerKindPush.Action(), SchedulerKindIntake.Action())
	}
}

func TestPathsFor_Kind(t *testing.T) {
	paths, err := resolvePathsForHomeProfile(t.TempDir(), DefaultProfile)
	if err != nil {
		t.Fatal(err)
	}
	if paths.PlistFor(SchedulerKindPush) != paths.LaunchdPlist {
		t.Errorf("PlistFor(push) should equal LaunchdPlist: %q vs %q",
			paths.PlistFor(SchedulerKindPush), paths.LaunchdPlist)
	}
	intakePlist := paths.PlistFor(SchedulerKindIntake)
	if intakePlist == paths.LaunchdPlist {
		t.Error("intake plist must differ from push plist")
	}
	if !strings.HasSuffix(intakePlist, "com.dotfiles.sync-intake.plist") {
		t.Errorf("intake plist tail wrong: %s", intakePlist)
	}
	if !strings.HasSuffix(paths.SystemdServiceFor(SchedulerKindIntake), "dotfiles-sync-intake.service") {
		t.Errorf("intake service path wrong: %s", paths.SystemdServiceFor(SchedulerKindIntake))
	}
	if !strings.HasSuffix(paths.SystemdTimerFor(SchedulerKindIntake), "dotfiles-sync-intake.timer") {
		t.Errorf("intake timer path wrong: %s", paths.SystemdTimerFor(SchedulerKindIntake))
	}
}

func TestResolveConfig_IntervalDefaultsAndClamps(t *testing.T) {
	t.Run("zero -> off", func(t *testing.T) {
		state := newIsolatedState(t)
		cfg, err := ResolveConfig(state)
		if err != nil {
			t.Fatalf("ResolveConfig: %v", err)
		}
		if cfg.Interval != 0 {
			t.Errorf("Interval = %d, want 0", cfg.Interval)
		}
	})

	t.Run("below min clamps up", func(t *testing.T) {
		state := newIsolatedState(t)
		seedRawLocalConfig(t, state, "interval: 5\n")
		cfg, err := ResolveConfig(state)
		if err != nil {
			t.Fatalf("ResolveConfig: %v", err)
		}
		if cfg.Interval != ScheduleIntervalMin {
			t.Errorf("Interval = %d, want %d (clamped to min)", cfg.Interval, ScheduleIntervalMin)
		}
	})

	t.Run("above max clamps down", func(t *testing.T) {
		state := newIsolatedState(t)
		seedRawLocalConfig(t, state, "interval: 200000\n")
		cfg, err := ResolveConfig(state)
		if err != nil {
			t.Fatalf("ResolveConfig: %v", err)
		}
		if cfg.Interval != ScheduleIntervalMax {
			t.Errorf("Interval = %d, want %d (clamped to max)", cfg.Interval, ScheduleIntervalMax)
		}
	})

	t.Run("valid passes through", func(t *testing.T) {
		state := newIsolatedState(t)
		seedLocalConfig(t, state, LocalConfig{Propagation: DefaultPropagationPolicy(), Interval: 600})
		cfg, err := ResolveConfig(state)
		if err != nil {
			t.Fatalf("ResolveConfig: %v", err)
		}
		if cfg.Interval != 600 {
			t.Errorf("Interval = %d, want 600", cfg.Interval)
		}
	})
}

func seedLocalConfig(t *testing.T, state *config.UserState, local LocalConfig) {
	t.Helper()
	paths := ResolveLocalPaths(state.Modules.Gsync.LocalPath)
	if err := EnsureLocalLayout(paths); err != nil {
		t.Fatalf("EnsureLocalLayout: %v", err)
	}
	if local.Propagation == (PropagationPolicy{}) {
		local.Propagation = DefaultPropagationPolicy()
	}
	if err := SaveLocalConfig(paths, &local); err != nil {
		t.Fatalf("SaveLocalConfig: %v", err)
	}
}

func seedRawLocalConfig(t *testing.T, state *config.UserState, extraYAML string) {
	t.Helper()
	paths := ResolveLocalPaths(state.Modules.Gsync.LocalPath)
	if err := EnsureLocalLayout(paths); err != nil {
		t.Fatalf("EnsureLocalLayout: %v", err)
	}
	body := "propagation:\n  create: true\n  update: true\n  delete: false\n" + extraYAML
	if err := os.WriteFile(paths.ConfigFile, []byte(body), 0o644); err != nil {
		t.Fatalf("write raw local config: %v", err)
	}
}

func TestResolvedPathsIncludeSchedulerArtifacts(t *testing.T) {
	paths, err := resolvePaths()
	if err != nil {
		t.Fatalf("resolvePaths: %v", err)
	}
	if !strings.HasSuffix(paths.LaunchdPlist, "com.dotfiles.sync.plist") {
		t.Errorf("LaunchdPlist tail wrong: %s", paths.LaunchdPlist)
	}
	if !strings.HasSuffix(paths.SystemdService, "dotfiles-sync.service") {
		t.Errorf("SystemdService tail wrong: %s", paths.SystemdService)
	}
	if !strings.HasSuffix(paths.SystemdTimer, "dotfiles-sync.timer") {
		t.Errorf("SystemdTimer tail wrong: %s", paths.SystemdTimer)
	}
}
