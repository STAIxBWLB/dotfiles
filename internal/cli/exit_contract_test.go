package cli

import (
	"bytes"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// usageMarkers are the headings cobra prints when it emits a usage block. Root
// sets SilenceUsage (root.go:29), so a failing command must show none of them:
// a usage dump behind an error buries the error in a screen of flags.
var usageMarkers = []string{"Usage:", "Available Commands:", "Global Flags:"}

// failingArgs is a command that reaches a RunE error deterministically and
// writes nothing outside the temp HOME it is given: the profile is validated
// while saving state, before any module runs.
func failingArgs(home string) []string {
	return []string{"apply", "--home", home, "--profile", "nosuchprofile", "--yes"}
}

func assertNoUsageBlock(t *testing.T, label, stream string) {
	t.Helper()
	for _, marker := range usageMarkers {
		if strings.Contains(stream, marker) {
			t.Errorf("%s carries a cobra usage block (%q):\n%s", label, marker, stream)
		}
	}
}

// buildDotForTest builds the dot binary the subprocess subtests drive. A build
// failure is fatal rather than a skip: a silent skip would make the exit
// contract vacuous exactly when the tree is broken.
func buildDotForTest(t *testing.T) string {
	t.Helper()
	if _, err := osexec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH; cannot build dot for the subprocess assertions")
	}
	bin := filepath.Join(t.TempDir(), "dot")
	build := osexec.Command("go", "build", "-p", "2", "-o", bin, "../../cmd/dot")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building dot failed: %v\n%s", err, out)
	}
	return bin
}

// runIsolated runs the binary under an environment carrying only HOME and
// PATH, the way tests/scenarios/differential.sh:238 isolates a launchd-style
// run, and returns stdout, stderr and the exit code.
func runIsolated(t *testing.T, bin, home string, args ...string) (string, string, int) {
	t.Helper()
	cmd := osexec.Command(bin, args...)
	cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	code := 0
	if err != nil {
		exitErr, ok := err.(*osexec.ExitError)
		if !ok {
			t.Fatalf("running %v: %v", args, err)
		}
		code = exitErr.ExitCode()
	}
	return out.String(), errb.String(), code
}

// TestExitContract pins BUG-01's stderr contract: a failing dot command reports
// its error once, with no usage block, and the Check-error aggregation does not
// turn any ordinary run into a non-zero exit.
func TestExitContract(t *testing.T) {
	bin := buildDotForTest(t)
	t.Run("in-process failure prints no usage block", func(t *testing.T) {
		home := t.TempDir()
		out, errOut, err := runDotForTest(failingArgs(home)...)
		if err == nil {
			t.Fatalf("expected an error from %v, got nil\nstdout=%s", failingArgs(home), out)
		}
		assertNoUsageBlock(t, "stdout", out)
		assertNoUsageBlock(t, "stderr", errOut)
	})

	t.Run("minimal environment prints exactly one error line", func(t *testing.T) {
		home := t.TempDir()
		out, errOut, code := runIsolated(t, bin, home, failingArgs(home)...)

		if code != 1 {
			t.Errorf("exit code = %d, want 1\nstdout=%s\nstderr=%s", code, out, errOut)
		}
		var lines []string
		for _, line := range strings.Split(strings.TrimRight(errOut, "\n"), "\n") {
			if strings.TrimSpace(line) != "" {
				lines = append(lines, line)
			}
		}
		if len(lines) != 1 {
			t.Fatalf("stderr = %d line(s), want exactly 1:\n%s", len(lines), errOut)
		}
		if !strings.HasPrefix(lines[0], "dot:") {
			t.Errorf("stderr line = %q, want it to start with %q", lines[0], "dot:")
		}
		assertNoUsageBlock(t, "stdout", out)
		assertNoUsageBlock(t, "stderr", errOut)
	})

	t.Run("usage failures exit two with tool prefix", func(t *testing.T) {
		home := t.TempDir()
		for _, args := range [][]string{{"not-a-command"}, {"--color=never", "not-a-command"}, {"version", "--not-a-flag"}, {"open"}} {
			out, stderr, code := runIsolated(t, bin, home, args...)
			if code != 2 || out != "" || !strings.HasPrefix(stderr, "dot: ") {
				t.Errorf("%v: exit=%d out=%q stderr=%q", args, code, out, stderr)
			}
		}
	})

	// Aggregating Check errors turns a previously swallowed failure into a
	// non-zero exit, and differential.sh:363 plus dryrun_property_test.go:186
	// assert exit 0 absolutely. Measure it here so a Check path that fires in a
	// clean sandbox surfaces in this package rather than in CI's differential.
	t.Run("read-only surfaces still exit zero", func(t *testing.T) {
		home := t.TempDir()

		runs := [][]string{{"diff", "--summary"}}
		for _, profile := range []string{"minimal", "full", "server"} {
			runs = append(runs, []string{"apply", "--profile", profile, "--yes", "--dry-run"})
		}
		for _, args := range runs {
			out, errOut, code := runIsolated(t, bin, home, args...)
			if code != 0 {
				t.Errorf("dot %s exit code = %d, want 0\nstdout=%s\nstderr=%s",
					strings.Join(args, " "), code, out, errOut)
			}
		}
	})
}
