package cli

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestUsageErrorsAreTypedAcrossCommandTree(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"unknown top level", []string{"definitely-unknown"}, "unknown command"},
		{"unknown after global flag", []string{"--color=never", "definitely-unknown"}, "unknown command"},
		{"unknown nested command", []string{"peer", "definitely-unknown"}, "unknown command"},
		{"unknown flag", []string{"version", "--not-a-flag"}, "unknown flag"},
		{"invalid flag value", []string{"--color=sometimes", "version"}, "invalid color mode"},
		{"missing flag value", []string{"--home"}, "flag needs an argument"},
		{"missing positional arg", []string{"open"}, "arg"},
		{"extra positional arg", []string{"ai", "skills", "validate", "extra"}, "unknown command"},
		{"completion args", []string{"completion", "bash", "extra"}, "unknown command"},
		{"unknown help topic", []string{"help", "definitely-unknown"}, "unknown help topic"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := NewRootCmd("dev", "test")
			var out, stderr bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&stderr)
			root.SetArgs(tc.args)
			err := root.Execute()
			var usage *UsageError
			if !errors.As(err, &usage) || ErrorExitCode(err) != 2 || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%v: %T %v (exit %d)", tc.args, err, err, ErrorExitCode(err))
			}
			if out.Len() != 0 {
				t.Fatalf("invalid invocation ran command: %q", out.String())
			}
		})
	}
}

func TestUsageValidationPreservesOperationAndExplicitStatuses(t *testing.T) {
	operation := errors.New("operation failed")
	for _, tc := range []struct {
		name string
		err  error
		code int
	}{
		{"operation", operation, 1},
		{"deferred", &ExitCodeError{Code: 75, Err: operation}, 75},
		{"remote unavailable", &ExitCodeError{Code: 69, Err: operation}, 69},
		{"command status", &ExitCodeError{Code: 7, Err: operation}, 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := &cobra.Command{Use: "dot", SilenceErrors: true, SilenceUsage: true}
			root.AddCommand(&cobra.Command{Use: "probe", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error { return tc.err }})
			configureUsage(root)
			root.SetArgs([]string{"probe"})
			err := root.Execute()
			if !errors.Is(err, tc.err) || ErrorExitCode(err) != tc.code {
				t.Fatalf("operation reclassified: %T %v (exit %d)", err, err, ErrorExitCode(err))
			}
		})
	}
	if ErrorExitCode(nil) != 0 {
		t.Fatal("success acquired an error exit")
	}
	// Even a custom Args validator that already supplies a status keeps it.
	c := &cobra.Command{Use: "probe"}
	explicit := &ExitCodeError{Code: 75, Err: operation}
	if got := usageError(c, explicit); got != explicit {
		t.Fatal("explicit Args status was overwritten")
	}
}

func TestRequiredAndGroupedFlagsAreUsageErrors(t *testing.T) {
	root := &cobra.Command{Use: "dot", SilenceErrors: true, SilenceUsage: true}
	probe := &cobra.Command{Use: "probe", RunE: func(*cobra.Command, []string) error { t.Fatal("invalid flags reached operation"); return nil }}
	probe.Flags().String("required", "", "")
	if err := probe.MarkFlagRequired("required"); err != nil {
		t.Fatal(err)
	}
	probe.Flags().Bool("first", false, "")
	probe.Flags().Bool("second", false, "")
	probe.MarkFlagsMutuallyExclusive("first", "second")
	root.AddCommand(probe)
	configureUsage(root)
	for _, args := range [][]string{{"probe"}, {"probe", "--required=x", "--first", "--second"}} {
		root.SetArgs(args)
		if err := root.Execute(); ErrorExitCode(err) != 2 {
			t.Fatalf("%v: %v (exit %d)", args, err, ErrorExitCode(err))
		}
	}
}

func TestCompletionStillWritesToRuntimeOutput(t *testing.T) {
	root := NewRootCmd("dev", "test")
	var out, stderr bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&stderr)
	root.SetArgs([]string{"completion", "bash"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "bash completion V2 for dot") || stderr.Len() != 0 {
		t.Fatalf("completion bypassed output sink (bytes %d, stderr %q)", out.Len(), stderr.String())
	}
}

func TestPrintErrorPrefixesFailuresAndGivesUsage(t *testing.T) {
	root := NewRootCmd("dev", "test")
	root.SetArgs([]string{"open"})
	err := root.Execute()
	var stderr bytes.Buffer
	PrintError(&stderr, err)
	got := stderr.String()
	if !strings.HasPrefix(got, "dot: ") || !strings.Contains(got, "Usage: dot open") || !strings.Contains(got, "Run 'dot help open' for details.") {
		t.Fatalf("usage diagnostic = %q", got)
	}
	stderr.Reset()
	PrintError(&stderr, errors.New("could not read fixture"))
	if got := stderr.String(); got != "dot: could not read fixture\n" {
		t.Fatalf("operation diagnostic = %q", got)
	}
	stderr.Reset()
	PrintError(&stderr, fmt.Errorf("wrapped: %w", ErrUnknownCommand))
	if stderr.Len() != 0 {
		t.Fatal("unknown-command gate acquired duplicate diagnostics")
	}
	PrintError(&stderr, nil)
	if stderr.Len() != 0 {
		t.Fatal("nil error printed diagnostics")
	}
}

func TestManualInputValidationUsesUsageStatus(t *testing.T) {
	for _, args := range [][]string{
		{"admit", "--class=invalid", "--dry-run", "--", "true"},
		{"admit", "--wait=-1s", "--dry-run", "--", "true"},
		{"admit", "--ignore=invalid", "--dry-run", "--", "true"},
		{"update", "--version=development", "--dry-run"},
		{"register", "example", "--layout=invalid"},
		{"ai", "family", "install"},
		{"ai", "memory", "audit", "--agent=invalid"},
	} {
		_, _, err := runDotForTest(args...)
		if ErrorExitCode(err) != 2 {
			t.Errorf("%v: %v exit=%d", args, err, ErrorExitCode(err))
		}
	}
}

// Before root Args is bound, Cobra's legacy Find validation runs ahead of help
// flag initialization. It can consume --home as --help's apparent value and
// then misclassify the directory as a command. Bound Args defers validation to
// the parser, where both flags are known.
func TestHelpHomeFixDefersLegacyFindValidationUntilFlagParsing(t *testing.T) {
	root := &cobra.Command{Use: "dot", SilenceErrors: true, SilenceUsage: true}
	root.PersistentFlags().String("home", "", "")
	root.AddCommand(&cobra.Command{Use: "known", RunE: func(*cobra.Command, []string) error { return nil }})
	args := []string{"--help", "--home", t.TempDir()}
	if _, _, err := root.Find(args); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("legacy precondition not reproduced: %v", err)
	}
	configureUsage(root)
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(args)
	if err := root.Execute(); err != nil || !strings.Contains(stdout.String(), "Usage:") || stderr.Len() != 0 {
		t.Fatalf("bound Args did not allow help parsing: %v out=%q stderr=%q", err, stdout.String(), stderr.String())
	}
}
