package main

import (
	"os"

	"github.com/STAIxBWLB/dotfiles/internal/cli"
)

var (
	version = "dev"
	commit  = "none"
)

func main() {
	if err := cli.Execute(version, commit); err != nil {
		cli.PrintError(os.Stderr, err)
		os.Exit(cli.ErrorExitCode(err))
	}
}
