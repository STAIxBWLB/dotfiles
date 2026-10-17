// Package admission implements the repository-scoped heavy-job admission
// controller: one heavy slot per project repo (shared across its worktrees,
// branches, sessions, and subagents), one host-wide maintenance slot, and a
// host-pressure gate that defers new heavy work while the machine is under
// memory, thermal, CPU, or WindowServer stress. The mechanism is deferral,
// never termination: the controller blocks NEW work and reports the owner of
// the running one. All platform differences are runtime GOOS switches (never
// build tags) and every decision is a pure function with an injected clock,
// so the whole decision surface is testable on Linux.
package admission

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// MaintenanceScope is the single shared scope for global tool installs and
// updates (dotfiles #150): maintenance work serializes against itself across
// every repo on the host instead of taking a per-repo slot.
const MaintenanceScope = "maintenance"

// ResolveScope returns the canonical admission scope for dir. A git checkout
// resolves to its absolute common git dir, so every worktree, branch, and
// session of the same repository shares one heavy slot. A directory outside
// any repository (or a machine without git) falls back to a hash of its
// absolute path, which is stable across sessions.
func ResolveScope(ctx context.Context, runner *exec.Runner, dir string) (string, error) {
	res, err := runner.RunQuery(ctx, "git", "-C", dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err == nil && res != nil {
		if scope, ok := ScopeFromGitCommonDir(res.Stdout); ok {
			return scope, nil
		}
	}
	return FallbackScope(dir)
}

// ScopeFromGitCommonDir extracts the scope from `git rev-parse
// --path-format=absolute --git-common-dir` output. Only an absolute path
// qualifies: empty output or a relative path from an old git is a miss, and
// the caller falls back rather than admitting against a scope that could
// collide with another repo.
func ScopeFromGitCommonDir(out string) (string, bool) {
	path := strings.TrimSpace(out)
	if path == "" || !filepath.IsAbs(path) {
		return "", false
	}
	return filepath.Clean(path), true
}

// FallbackScope hashes an absolute directory path into a stable scope key
// for directories outside any repository. The hash (not the path) becomes
// the identity so the key carries no separators or length problems into the
// slot directory name.
func FallbackScope(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolving absolute path of %s: %w", dir, err)
	}
	sum := sha256.Sum256([]byte(abs))
	return "cwd-" + hex.EncodeToString(sum[:])[:16], nil
}
