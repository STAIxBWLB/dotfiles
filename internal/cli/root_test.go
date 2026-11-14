package cli

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
)

// TestUnknownCommandGate_WritesGuidanceAndReturnsSentinel verifies that an
// unrecognized first argument is rejected with the ErrUnknownCommand sentinel
// and the exact six-line guidance. Without this guarantee a typo could be
// silently routed somewhere unexpected, or main could print a redundant
// "dot: unknown command" line behind guidance the user already saw.
func TestUnknownCommandGate_WritesGuidanceAndReturnsSentinel(t *testing.T) {
	cmd := NewRootCmd("dev", "test")
	var buf bytes.Buffer

	err := unknownCommandGate(cmd, []string{"dot", "definitely-not-a-command"}, &buf)
	if !errors.Is(err, ErrUnknownCommand) {
		t.Fatalf("err = %v, want errors.Is(err, ErrUnknownCommand)", err)
	}
	if got := ErrorExitCode(err); got != 2 {
		t.Fatalf("unknown command exit=%d, want 2", got)
	}

	out := buf.String()
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 6 {
		t.Fatalf("guidance = %d lines, want 6:\n%s", len(lines), out)
	}
	if want := `dot: unknown command "definitely-not-a-command"`; lines[0] != want {
		t.Errorf("first line = %q, want %q", lines[0], want)
	}
	if !strings.Contains(out, "  dot open definitely-not-a-command") {
		t.Errorf("guidance missing open-subcommand suggestion:\n%s", out)
	}
}

// TestUnknownCommandGate_AcceptsKnownCommandsAndFlags verifies that the gate
// passes through every input the old inline gate passed through: registered
// subcommands, registered aliases, flags, cobra's hidden completion hooks, an
// empty first argument, and a bare argv. Without this guarantee legitimate
// invocations would start dying with exit 1 after the extraction.
func TestUnknownCommandGate_AcceptsKnownCommandsAndFlags(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"registered subcommand", []string{"dot", "version"}},
		{"registered alias", []string{"dot", "ls"}},
		{"flag", []string{"dot", "--help"}},
		{"completion hook prefix", []string{"dot", "__complete"}},
		{"empty first argument", []string{"dot", ""}},
		{"argv of length one", []string{"dot"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := NewRootCmd("dev", "test")
			var buf bytes.Buffer
			if err := unknownCommandGate(cmd, tc.args, &buf); err != nil {
				t.Errorf("gate(%v) = %v, want nil", tc.args, err)
			}
			if buf.Len() != 0 {
				t.Errorf("gate(%v) wrote %q, want no output", tc.args, buf.String())
			}
		})
	}
}

// TestExecute_UnknownCommandRunsCallerDefers verifies that a deferred function
// registered by Execute's caller runs on the unknown-command path. Without
// this guarantee the old os.Exit(1) inside Execute would silently skip every
// caller's cleanup (lock releases, temp-file removals, flushes); under that
// code this test binary itself would terminate before reporting anything.
//
// The guidance goes to the real stderr here, intentionally: Execute wires the
// gate to the command's error stream, and redirecting it would test a
// different code path than the one users hit.
func TestExecute_UnknownCommandRunsCallerDefers(t *testing.T) {
	oldArgs := os.Args
	os.Args = []string{"dot", "definitely-not-a-command"}
	defer func() { os.Args = oldArgs }()

	deferredRan := false
	err := executeWithCallerDefer(&deferredRan)

	if err == nil {
		t.Fatal("Execute with an unknown command returned nil, want non-nil")
	}
	if !errors.Is(err, ErrUnknownCommand) {
		t.Errorf("err = %v, want errors.Is(err, ErrUnknownCommand)", err)
	}
	if !deferredRan {
		t.Error("caller-registered deferred function did not run on the unknown-command path")
	}
}

// executeWithCallerDefer registers the defer in its own frame — the frame
// that calls Execute — because the guarantee under test is precisely that a
// caller's defers survive the unknown-command path.
func executeWithCallerDefer(ran *bool) error {
	defer func() { *ran = true }()
	return Execute("dev", "test")
}
