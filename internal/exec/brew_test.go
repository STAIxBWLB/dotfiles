package exec

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// TestExtractAppName covers every shape that brew cask JSON emits for an
// `app` artifact entry: a plain string, a tuple with a target override, and
// a tuple with only a source. Keeping this close to the production function
// makes regressions cheap to catch when brew's schema shifts.
func TestExtractAppName(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"plain string", `"Raycast.app"`, "Raycast.app"},
		{"path-qualified string", `"Some/Nested/Path/Foo.app"`, "Foo.app"},
		{"tuple with target", `["Source.app", {"target": "/Applications/Target.app"}]`, "Target.app"},
		{"tuple source only", `["Source.app"]`, "Source.app"},
		{"empty tuple", `[]`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractAppName(json.RawMessage(tc.raw))
			if got != tc.want {
				t.Errorf("extractAppName(%s) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestIsFormulaInstalledAcceptsTapQualifiedFormula(t *testing.T) {
	installed := map[string]bool{"bun": true}
	if !isFormulaInstalled(installed, "oven-sh/bun/bun") {
		t.Fatal("expected tap-qualified formula to match installed short formula name")
	}
	if isFormulaInstalled(installed, "oven-sh/other/other") {
		t.Fatal("unexpected match for unrelated tap-qualified formula")
	}
}

func TestTapsForFormulasIncludesMaruCLI(t *testing.T) {
	got := TapsForFormulas([]string{
		"git",
		"maru-cli",
		"staixbwlb/cask/maru-cli",
		"maru-cli",
	})
	want := []string{"staixbwlb/cask"}

	if len(got) != len(want) {
		t.Fatalf("expected %d taps, got %d: %#v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tap %d: expected %q, got %q", i, want[i], got[i])
		}
	}
}

func TestInstallableFormulasForGOOSSkipsDarwinOnlyFormulasOnLinux(t *testing.T) {
	formulas := []string{"git", "maru-cli", "staixbwlb/cask/maru-cli", "tmux"}

	linux := installableFormulasForGOOS(formulas, "linux")
	wantLinux := []string{"git", "tmux"}
	if len(linux) != len(wantLinux) {
		t.Fatalf("linux formulas = %#v, want %#v", linux, wantLinux)
	}
	for i := range wantLinux {
		if linux[i] != wantLinux[i] {
			t.Fatalf("linux formula %d: expected %q, got %q", i, wantLinux[i], linux[i])
		}
	}

	darwin := installableFormulasForGOOS(formulas, "darwin")
	wantDarwin := []string{"git", "maru-cli", "staixbwlb/cask/maru-cli", "tmux"}
	if len(darwin) != len(wantDarwin) {
		t.Fatalf("darwin formulas = %#v, want %#v", darwin, wantDarwin)
	}
	for i := range wantDarwin {
		if darwin[i] != wantDarwin[i] {
			t.Fatalf("darwin formula %d: expected %q, got %q", i, wantDarwin[i], darwin[i])
		}
	}
}

func TestFormulaInstallGroupsSplitTapQualifiedFormula(t *testing.T) {
	got := formulaInstallGroups([]string{
		"git",
		"unzip",
		"gcc",
		"oven-sh/bun/bun",
		"tmux",
		"uv",
	})
	want := [][]string{
		{"git", "unzip", "gcc"},
		{"oven-sh/bun/bun"},
		{"tmux", "uv"},
	}

	if len(got) != len(want) {
		t.Fatalf("expected %d groups, got %d: %#v", len(want), len(got), got)
	}
	for i := range want {
		if len(got[i]) != len(want[i]) {
			t.Fatalf("group %d: expected %#v, got %#v", i, want[i], got[i])
		}
		for j := range want[i] {
			if got[i][j] != want[i][j] {
				t.Fatalf("group %d item %d: expected %q, got %q", i, j, want[i][j], got[i][j])
			}
		}
	}
}

func TestMissingFromInstalledDedupesInOrder(t *testing.T) {
	installed := map[string]bool{"homebrew/core": true}
	got := missingFromInstalled(installed, []string{
		"homebrew/core",
		"manaflow-ai/cmux",
		"manaflow-ai/cmux",
		"other/tap",
	})
	want := []string{"manaflow-ai/cmux", "other/tap"}
	if len(got) != len(want) {
		t.Fatalf("expected %d missing taps, got %d: %#v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("missing tap %d: expected %q, got %q", i, want[i], got[i])
		}
	}
}

func TestGCCCompilerFromBrewVersions(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want string
	}{
		{"current gcc", "gcc 15.2.0_1\n", "gcc-15"},
		{"future major", "gcc 16.1.0\n", "gcc-16"},
		{"missing version", "gcc\n", ""},
		{"wrong formula", "llvm 22.0.0\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := gccCompilerFromBrewVersions(tc.out); got != tc.want {
				t.Fatalf("expected %q, got %q", tc.want, got)
			}
		})
	}
}

// hostBrewPrefixExecutable reports the brew the platform prefix list points at
// on THIS host, or "" when no Homebrew prefix exists here. The prefix list is
// spelled out again rather than read from brewPrefixDirs: a test that asks
// production code where to look agrees with it by construction and stops being
// able to catch a wrong list.
func hostBrewPrefixExecutable() string {
	for _, dir := range []string{"/opt/homebrew/bin", "/home/linuxbrew/.linuxbrew/bin", "/home/linuxbrew/.linuxbrew/sbin"} {
		candidate := filepath.Join(dir, "brew")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

func testBrew(t *testing.T) *Brew {
	t.Helper()
	return NewBrew(NewRunner(false, slog.New(slog.NewTextHandler(io.Discard, nil))))
}

// TestBrew_DetectionSurvivesAnEmptyPATH is the property BUG-06's fix was chosen
// to preserve (D-01): on a fresh process whose PATH has not been set up yet,
// Homebrew is still detected. The rejected small diff — deleting the PATH
// refresh from PackagesModule.Check and stopping there — turns this row red and
// makes `dot check` report "install Homebrew" for an installed Homebrew.
//
// The two detection rows split on the host because the property itself does:
// this one needs a prefix to find, and the row below needs none to exist. A
// Homebrew developer machine runs this one, CI's clean linux container runs the
// other, and between them both directions are covered.
func TestBrew_DetectionSurvivesAnEmptyPATH(t *testing.T) {
	brewExe := hostBrewPrefixExecutable()
	if brewExe == "" {
		t.Skip("no Homebrew prefix on this host, so there is nothing for the resolver to find; TestBrew_DetectionFalseWithoutPATHOrPrefix is the row that runs here")
	}
	t.Setenv("PATH", t.TempDir()) // empty: LookPath can no longer reach brew

	if !testBrew(t).IsAvailable() {
		t.Fatalf("IsAvailable() reported false with brew present at %s and PATH emptied; detection is reading the process PATH only, which is the regression D-01 rejected the small diff to avoid", brewExe)
	}
}

// TestBrew_DetectionFalseWithoutPATHOrPrefix is the anti-hardcode row. Without
// it the resolver could return a fixed prefix path unconditionally and the row
// above would still be green.
func TestBrew_DetectionFalseWithoutPATHOrPrefix(t *testing.T) {
	if brewExe := hostBrewPrefixExecutable(); brewExe != "" {
		t.Skipf("host Homebrew at %s is exactly what the resolver is supposed to find, so this row cannot run here; CI's linux container has no prefix and runs it unconditionally", brewExe)
	}
	t.Setenv("PATH", t.TempDir())

	if testBrew(t).IsAvailable() {
		t.Fatal("IsAvailable() reported true with an empty PATH and no Homebrew prefix on the host, so it is answering from a hardcoded path rather than from something it found")
	}
}

// TestBrew_ReadOnlyProbesLeaveProcessPATHUnchanged is BUG-06's own assertion:
// production code must not widen the process PATH from a read-only path. The
// os.Setenv that did (RefreshPath) survives, but only on the two write paths
// D-02 permits, and nothing a probe touches may reach it.
//
// This row does not skip. It is meaningful on a host with Homebrew (where the
// mutation used to fire) and on one without (where it asserts the resolver did
// not introduce a new one).
//
// Ceiling this does NOT assert: the probes still EXECUTE. RunQuery has no
// dry-run branch and a subprocess invoked by absolute path is not confined by a
// PATH sandbox, so real brew still runs and still writes under its own cache.
// That residual is accepted and named (T-05-11); the unconditional empty-HOME
// property is carried by tests/scenarios/dry-run-empty-home.sh in CI's linux job.
func TestBrew_ReadOnlyProbesLeaveProcessPATHUnchanged(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	want := os.Getenv("PATH")

	b := testBrew(t)
	b.IsAvailable()
	b.IsInstalled("git")
	b.IsCaskInstalled("iterm2")
	b.InstalledTaps()

	if got := os.Getenv("PATH"); got != want {
		t.Errorf("a read-only Brew probe widened the process PATH, so production code re-opened a sandbox the caller built\n  before: %q\n   after: %q", want, got)
	}
}
