package remote

import "strings"

// builtinExcludes are never sent to a host, even when the local repository
// tracks them. They are the directories that only ever hold this machine's
// build products, the nested worktrees that hold whole other checkouts, and
// the secrets and agent runtime state that must not leave the laptop.
//
// A pattern without a slash matches at any depth, which is what every entry
// here wants: node_modules under a workspace package is the same directory as
// the one at the top.
var builtinExcludes = []string{
	// VCS metadata: the mirror is fed by git's own listing, never by a copy.
	".git",
	// Dependency and build directories.
	"node_modules",
	"target",
	"dist",
	".venv",
	"venv",
	".next",
	".turbo",
	"coverage",
	"test-results",
	// Nested worktrees hold another checkout of the same repository.
	".worktrees",
	".claude/worktrees",
	".qwen/worktrees",
	".codex/worktrees",
	// macOS noise.
	".DS_Store",
	// Secrets and agent runtime state.
	".env",
	".env.*",
	".dev.vars",
	"*.pem",
	"*.key",
	".vercel",
	".maru",
	".agenthub",
}

// envTemplateInclude is the one exception to the .env.* rule: a template with
// no values in it is what a container needs to be told about the variables a
// project expects.
const envTemplateInclude = ".env.example"

// BuiltinExcludes returns the never-sent patterns.
func BuiltinExcludes() []string { return append([]string(nil), builtinExcludes...) }

// Excludes is the built-in list plus the operator's extra entries. The extras
// only add: no configured entry can remove a built-in one.
func (c Config) Excludes() []string {
	out := BuiltinExcludes()
	for _, extra := range c.ExcludeExtra {
		extra = strings.TrimSpace(extra)
		if extra != "" {
			out = append(out, extra)
		}
	}
	return out
}

// ExcludeArgs renders the rsync filter arguments for a transfer: the env
// template include first, then one exclude per pattern.
func ExcludeArgs(excludes []string) []string {
	args := make([]string, 0, len(excludes)+1)
	args = append(args, "--include="+envTemplateInclude)
	for _, pattern := range excludes {
		args = append(args, "--exclude="+pattern)
	}
	return args
}
