package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/STAIxBWLB/dotfiles/internal/cli"
)

var (
	version = "dev"
	commit  = "none"
)

func main() {
	if err := cli.Execute(version, commit); err != nil {
		code := 1
		// A command that carries its own exit code (admission defers with
		// 75/EX_TEMPFAIL, a wrapped command keeps its own status) wins over
		// the generic failure code.
		var exitCoder interface{ ExitCode() int }
		if errors.As(err, &exitCoder) {
			code = exitCoder.ExitCode()
		}
		// The unknown-command gate has already written its own guidance;
		// printing "Error: unknown command" behind it would add a seventh
		// line. Every other error keeps the existing format.
		if !errors.Is(err, cli.ErrUnknownCommand) {
			fmt.Fprintln(os.Stderr, "Error:", err)
		}
		os.Exit(code)
	}
}
