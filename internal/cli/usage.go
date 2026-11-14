package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

// UsageError identifies invocation errors separately from operation failures.
// The wrapped cause is retained for errors.Is/errors.As callers.
type UsageError struct {
	Err     error
	Command string
	Usage   string
}

func (e *UsageError) Error() string { return e.Err.Error() }
func (e *UsageError) Unwrap() error { return e.Err }
func (*UsageError) ExitCode() int   { return 2 }

// ErrorExitCode preserves explicit statuses such as admission's 75 and a
// wrapped remote command's own status. Untyped operation failures remain 1.
func ErrorExitCode(err error) int {
	if err == nil {
		return 0
	}
	var coded interface{ ExitCode() int }
	if errors.As(err, &coded) {
		return coded.ExitCode()
	}
	return 1
}

func usageError(cmd *cobra.Command, err error) error {
	if err == nil {
		return nil
	}
	var coded interface{ ExitCode() int }
	if errors.As(err, &coded) {
		return err
	}
	return &UsageError{Err: err, Command: cmd.CommandPath(), Usage: cmd.UseLine()}
}

// PrintError writes process-level failures. The unknown-command gate already
// writes its own guidance, so its sentinel must not acquire a duplicate line.
func PrintError(w io.Writer, err error) {
	if err == nil || errors.Is(err, ErrUnknownCommand) {
		return
	}
	ui.WriteReportLine(w, "dot: "+err.Error())
	var usage *UsageError
	if errors.As(err, &usage) {
		ui.WriteReportLine(w, "Usage: "+usage.Usage)
		topic := strings.TrimPrefix(usage.Command, "dot")
		ui.WriteReportLine(w, fmt.Sprintf("Run 'dot help%s' for details.", topic))
	}
}

// completionOutput defers Cobra's captured completion writer until execution,
// so eagerly registering its validators does not bypass a caller's SetOut.
type completionOutput struct{ root *cobra.Command }

func (w completionOutput) Write(p []byte) (int, error) { return w.root.OutOrStdout().Write(p) }

// configureUsage binds typed validation to the command tree, including Cobra's
// generated commands. It does not classify handler errors by their text.
func configureUsage(root *cobra.Command) {
	// NewRootCmd has not installed an output override yet. Restore its nil
	// default after Cobra captures the proxy, retaining runtime stdout fallback.
	root.SetOut(completionOutput{root: root})
	root.InitDefaultCompletionCmd()
	root.SetOut(nil)
	root.InitDefaultHelpCmd()
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error { return usageError(cmd, err) })
	for _, child := range root.Commands() {
		if child.Name() == "help" {
			child.Args = func(cmd *cobra.Command, args []string) error {
				_, remaining, err := root.Find(args)
				if err != nil {
					return err
				}
				if len(remaining) > 0 {
					return fmt.Errorf("unknown help topic %q", strings.Join(args, " "))
				}
				return nil
			}
		}
	}
	bindUsage(root)
}

func bindUsage(cmd *cobra.Command) {
	validate := cmd.Args
	if validate == nil {
		if !cmd.HasParent() || cmd.HasSubCommands() {
			validate = func(c *cobra.Command, args []string) error {
				if len(args) == 0 {
					return nil
				}
				return fmt.Errorf("unknown command %q for %q; run '%s --help' for available commands", args[0], c.CommandPath(), c.CommandPath())
			}
		} else {
			validate = cobra.ArbitraryArgs
		}
	}
	cmd.Args = func(c *cobra.Command, args []string) error {
		if err := validate(c, args); err != nil {
			return usageError(c, err)
		}
		// Flags are already parsed here. Binding these checks also classifies
		// Cobra's later required/group validation without intercepting operations.
		if err := c.ValidateRequiredFlags(); err != nil {
			return usageError(c, err)
		}
		return usageError(c, c.ValidateFlagGroups())
	}
	for _, child := range cmd.Commands() {
		bindUsage(child)
	}
}
