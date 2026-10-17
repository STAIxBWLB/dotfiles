// Package claudecfg_test holds the assertions that need a package which
// imports claudecfg. guard is the hook entry point BUG-02 names, and an
// in-package test file cannot import it without an import cycle.
package claudecfg_test

import (
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/claudecfg"
	dotexec "github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/guard"
)

// guardHookEntries is how many marker-tagged hook commands EnsureHookEntries
// registers: one per PreToolUse matcher (Bash, and the file-mutation set).
const guardHookEntries = 2

// TestInspectHookEntries_ReadsUnderAHeldLock is BUG-02's "the hook read path
// takes no lock" clause, asserted through the entry point a Claude Code hook
// actually calls rather than only through claudecfg.Read.
//
// The lock is planted by hand, with this process's own live pid inside, so
// fileutil treats it as held rather than stale: a reader that serialized
// behind a writer would block here, and one that serialized behind a stale
// or root-owned lock would block for a whole stale window. Neither is
// acceptable on a path Claude Code invokes on every tool call.
func TestInspectHookEntries_ReadsUnderAHeldLock(t *testing.T) {
	home := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	command := guard.HookCommand("/opt/dot/bin/dot")
	if _, err := guard.EnsureHookEntries(dotexec.NewRunner(false, logger), home, command); err != nil {
		t.Fatal(err)
	}

	lockDir := claudecfg.LockDir(home)
	if err := os.MkdirAll(lockDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lockDir, "lock.pid"), []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}

	commands, err := guard.InspectHookEntries(home)
	if err != nil {
		t.Fatalf("the hook read path must not block on a held write lock: %v", err)
	}
	if len(commands) != guardHookEntries {
		t.Fatalf("InspectHookEntries = %v, want %d marker-tagged commands", commands, guardHookEntries)
	}
	for _, got := range commands {
		if got != command {
			t.Fatalf("InspectHookEntries returned %q, want %q", got, command)
		}
	}
	// The read left the writer's lock exactly where it found it.
	if _, err := os.Stat(lockDir); err != nil {
		t.Fatalf("the read disturbed the held lock: %v", err)
	}
}
