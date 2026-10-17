//go:build linux

package syncer

import (
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/template"
)

func TestSystemdAnalyze_SpecialHome(t *testing.T) {
	tool, err := osexec.LookPath("systemd-analyze")
	if err != nil {
		t.Fatalf("systemd-analyze is required for Linux scheduler syntax evidence: %v", err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	home := "/tmp/dotfiles scheduler % $ ' \" \\ 유니코드/" + strings.Repeat("long-", 32)
	body, err := template.NewEngine().Render("sync/dotfiles-sync.service.tmpl", SchedulerTemplateData{
		DotfilesPath: exe, Home: home, SystemdHomeArg: systemdHomeArgument(home),
		LogFile: "/tmp/dotfiles.log", Interval: 60, Label: launchdLabel,
		Action: "push", Mode: ModeClean.String(), Description: "scheduler parser test", ServiceName: systemdServiceName,
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), systemdServiceName)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if output, err := osexec.Command(tool, "verify", path).CombinedOutput(); err != nil {
		t.Fatalf("systemd-analyze rejected rendered scheduler service: %v\n%s", err, output)
	}
}

func TestSystemdAnalyze_ProfiledUnits(t *testing.T) {
	tool, err := osexec.LookPath("systemd-analyze")
	if err != nil {
		t.Fatalf("systemd-analyze is required for Linux scheduler syntax evidence: %v", err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	paths := withProfile(pathsFor(root, filepath.Join(root, "cache")), "research")
	for _, kind := range []SchedulerKind{SchedulerKindPush, SchedulerKindIntake} {
		t.Run(kind.Action(), func(t *testing.T) {
			data := SchedulerTemplateData{
				DotfilesPath: exe, Home: root, SystemdHomeArg: systemdHomeArgument(root),
				LogFile: filepath.Join(root, "sync.log"), Interval: 60,
				Label: paths.LaunchdLabelFor(kind), Profile: "research", Action: kind.Action(),
				Mode: ModeClean.String(), Description: "profiled scheduler parser test",
				ServiceName: paths.SystemdServiceNameFor(kind),
			}
			service, err := template.NewEngine().Render("sync/dotfiles-sync.service.tmpl", data)
			if err != nil {
				t.Fatalf("render service: %v", err)
			}
			timer, err := template.NewEngine().Render("sync/dotfiles-sync.timer.tmpl", data)
			if err != nil {
				t.Fatalf("render timer: %v", err)
			}
			servicePath := filepath.Join(root, paths.SystemdServiceNameFor(kind))
			timerPath := filepath.Join(root, paths.SystemdTimerNameFor(kind))
			if err := os.WriteFile(servicePath, service, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(timerPath, timer, 0o644); err != nil {
				t.Fatal(err)
			}
			if output, err := osexec.Command(tool, "verify", servicePath, timerPath).CombinedOutput(); err != nil {
				t.Fatalf("systemd-analyze rejected profiled %s service/timer pair: %v\n%s", kind.Action(), err, output)
			}
		})
	}
}
