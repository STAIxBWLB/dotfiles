// Package ghauth provides helpers for GitHub CLI (gh) authentication
// used during workspace repo cloning and any operation that may touch
// private GitHub repositories.
package ghauth

import (
	"context"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// Scopes lists OAuth scopes requested during gh auth login.
// Covers: private repo access, SSH/GPG key management, Actions workflows, org read.
var Scopes = []string{
	"repo",
	"read:org",
	"gist",
	"workflow",
	"admin:public_key",
	"admin:ssh_signing_key",
	"write:gpg_key",
}

// Authenticated reports whether gh CLI is currently authenticated to github.com.
// Uses RunQuery so it works even in dry-run mode (read-only detection).
func Authenticated(runner *exec.Runner) bool {
	result, err := runner.RunQuery(context.Background(), "gh", "auth", "status")
	return err == nil && result.ExitCode == 0
}

// Login runs interactive `gh auth login` with broad scopes and HTTPS git protocol.
// Uses RunAttached so the user can interact with the browser/device-code flow.
func Login(ctx context.Context, runner *exec.Runner) error {
	args := []string{"auth", "login",
		"--hostname", "github.com",
		"--git-protocol", "https",
		"--web",
	}
	for _, s := range Scopes {
		args = append(args, "--scopes", s)
	}
	return runner.RunAttached(ctx, "gh", args...)
}
