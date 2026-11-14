package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/STAIxBWLB/dotfiles/internal/aisettings"
	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/module"
	"github.com/STAIxBWLB/dotfiles/internal/syncer"
	"github.com/charmbracelet/x/ansi"
)

func fixtureStatus(t *testing.T) (home, cfg string) {
	t.Helper()
	pinColorEnv(t)
	t.Setenv("COLUMNS", "")
	t.Setenv("LC_ALL", "en_US.UTF-8")
	t.Setenv("DOT_COLOR_THEME", "ansi")
	_, home = newStatusHomeFixture(t)
	cfg = filepath.Join(home, "report.yaml")
	writeCLITestFile(t, cfg, "modules:\n  git:\n    enabled: true\n")
	oldDetect, oldCheck := statusDetectSystem, statusCheckModules
	statusDetectSystem = func() (*config.SystemInfo, error) {
		return &config.SystemInfo{OS: "linux", Arch: "test", Hostname: "fixture", Shell: "fixture"}, nil
	}
	statusCheckModules = func(_ context.Context, mods []module.Module, _ *module.RunContext) (map[string]*module.CheckResult, error) {
		if len(mods) != 1 || mods[0].Name() != "git" {
			t.Fatalf("fixture did not select only git: %v", mods)
		}
		return map[string]*module.CheckResult{"git": {Satisfied: false, Changes: []module.Change{{Description: "configure git"}}}}, nil
	}
	t.Cleanup(func() { statusDetectSystem, statusCheckModules = oldDetect, oldCheck })
	return home, cfg
}

func TestMainStatusReportConformanceGoldens(t *testing.T) {
	home, cfg := fixtureStatus(t)
	for _, mode := range []string{"never", "always"} {
		t.Run(mode, func(t *testing.T) {
			out, stderr, err := runDotForTest("--home", home, "--config", cfg, "status", "--color="+mode)
			if err != nil {
				t.Fatalf("status view acquired failure exit: %v", err)
			}
			if stderr != "" {
				t.Fatalf("report findings leaked to stderr: %s", stderr)
			}
			plain := ansi.Strip(out)
			if !strings.HasSuffix(plain, "✗ 1 failed\n") || strings.HasSuffix(plain, "\n\n") || strings.Contains(plain, "module(s) need attention") {
				t.Fatalf("missing final summary: %s", plain)
			}
			if strings.ContainsRune(out, 27) != (mode == "always") {
				t.Fatal("color policy not applied to main report")
			}
			normalized := strings.ReplaceAll(out, home, "@HOME@")
			var snapshot bytes.Buffer
			e := json.NewEncoder(&snapshot)
			e.SetEscapeHTML(false)
			e.SetIndent("", "  ")
			if err := e.Encode(strings.SplitAfter(normalized, "\n")); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join("testdata", "status-standard-"+mode+".golden")
			if os.Getenv("UPDATE_CONFORMANCE_GOLDENS") == "1" {
				if err := os.WriteFile(target, snapshot.Bytes(), 0644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(want, snapshot.Bytes()) {
				t.Fatalf("main status golden changed:\n%s", snapshot.String())
			}
		})
	}
}

func TestMainReportAt80ColumnsAndEnvironmentColors(t *testing.T) {
	home, cfg := fixtureStatus(t)
	t.Setenv("COLUMNS", "80")
	t.Setenv("NO_COLOR", "1")
	t.Setenv("FORCE_COLOR", "1")
	out, _, err := runDotForTest("--home", home, "--config", cfg, "status", "--color=auto")
	if err != nil || strings.ContainsRune(out, 27) {
		t.Fatalf("NO_COLOR: %v", err)
	}
	for _, line := range strings.Split(out, "\n") {
		if n := lipgloss.Width(line); n > 80 {
			t.Errorf("%d cells: %q", n, line)
		}
	}
	t.Setenv("NO_COLOR", "")
	out, _, err = runDotForTest("--home", home, "--config", cfg, "status", "--color=auto")
	if err != nil || !strings.ContainsRune(out, 27) {
		t.Fatalf("FORCE_COLOR on pipe: %v", err)
	}
	t.Setenv("LC_ALL", "C")
	out, _, err = runDotForTest("--home", home, "--config", cfg, "status", "--color=never")
	if err != nil || !strings.HasSuffix(out, "x 1 failed\n") || strings.ContainsAny(out, "✓✗⚠▸") {
		t.Fatalf("ASCII report: %v\n%s", err, out)
	}
}

func TestLayoutDoctorFailsOnlyForRequiredTools(t *testing.T) {
	pinColorEnv(t)
	t.Setenv("COLUMNS", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	out, stderr, err := runDotForTest("layouts", "doctor", "--color=never")
	if ErrorExitCode(err) != 1 || !strings.Contains(out, "failed") || stderr != "" {
		t.Fatalf("doctor: exit=%d err=%v stdout=%s stderr=%s", ErrorExitCode(err), err, out, stderr)
	}
	var buf bytes.Buffer
	PrintError(&buf, err)
	if !strings.HasPrefix(buf.String(), "dot: ") {
		t.Fatal("failure lacks tool prefix")
	}
	// Providing only the required tool leaves optional misses as warnings/0.
	tools := os.Getenv("PATH")
	tmux := filepath.Join(tools, "tmux")
	if err := os.WriteFile(tmux, []byte("#!/bin/sh\nprintf 'tmux 3.3\\n'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	out, stderr, err = runDotForTest("layouts", "doctor", "--color=never")
	if err != nil || stderr != "" || !strings.Contains(out, "warnings") || !strings.HasSuffix(out, "1 ok\n") {
		t.Fatalf("warnings changed doctor exit: err=%v stdout=%s stderr=%s", err, out, stderr)
	}
}

func TestSkillValidationSummaryIncludesStrictDuplicatesAndReadErrors(t *testing.T) {
	report := &aisettings.SkillScanReport{Items: []aisettings.SkillInventoryItem{{Status: aisettings.SkillStatusValid}, {Status: aisettings.SkillStatusLegacy}, {Status: aisettings.SkillStatusInvalid}}, Duplicates: []aisettings.SkillDuplicate{{Name: "duplicate"}}, Errors: []string{"read failed"}}
	normal, strict := skillValidationSummary(report, false), skillValidationSummary(report, true)
	if normal.OK != 1 || normal.Warnings != 1 || normal.Failed != 3 || strict.Failed != 4 || strict.Warnings != 0 {
		t.Fatalf("normal=%+v strict=%+v", normal, strict)
	}
	if got := skillValidationSummary(&aisettings.SkillScanReport{}, false); got.Unknown != 1 || got.OK != 0 {
		t.Fatalf("empty scan became clean: %+v", got)
	}
}

func TestPeerDoctorWarningUnknownAndFailureMatchContract(t *testing.T) {
	pinColorEnv(t)
	t.Setenv("COLUMNS", "")
	for _, tc := range []struct {
		name            string
		skewErr         error
		skewOK          bool
		failed, unknown int
	}{
		{"skew failed", nil, false, 1, 0}, {"skew unknown", errors.New("unavailable"), false, 0, 1}, {"skew ok", nil, true, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, stderr bytes.Buffer
			r := &syncer.PeerDoctorReport{LocalRsyncPath: "rsync", RemoteDot: "dot 1", ClockSkew: 10 * time.Second, ClockSkewErr: tc.skewErr, ClockSkewOK: tc.skewOK, Problems: tc.failed, Peer: &syncer.PeerSideFacts{}}
			summary := printPeerDoctorReport(&Printer{Out: &out, Err: &stderr}, r, false)
			if summary.Failed != tc.failed || summary.Unknown != tc.unknown || stderr.Len() != 0 {
				t.Fatalf("summary=%+v stderr=%s", summary, stderr.String())
			}
			if !strings.Contains(out.String(), "clock skew") {
				t.Fatal("lost clock skew finding")
			}
		})
	}
}

func TestMainStatusLongProjectIDsDoNotWrap(t *testing.T) {
	home, cfg := fixtureStatus(t)
	t.Setenv("COLUMNS", "80")
	id := "project-" + strings.Repeat("a", 90)
	writeCLITestFile(t, filepath.Join(home, ".config", "dot", "workspace.yaml"), "projects:\n  - name: "+id+"\n    path: "+filepath.Join(home, "proj")+"\n")
	out, _, err := runDotForTest("--home", home, "--config", cfg, "status", "--color=never")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, id) || !strings.Contains(out, "...") || !strings.Contains(out, "registered") {
		t.Fatalf("ID column unbounded: %s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if lipgloss.Width(line) > 80 {
			t.Fatalf("line exceeds 80: %q", line)
		}
	}
	t.Setenv("COLUMNS", "")
	out, _, err = runDotForTest("--home", home, "--config", cfg, "status", "--color=never", "--verbose")
	if err != nil || !strings.Contains(out, id) {
		t.Fatalf("verbose lost full ID: %v\n%s", err, out)
	}
}
