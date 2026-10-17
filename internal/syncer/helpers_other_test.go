//go:build !darwin

package syncer

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/config"
)

func TestExplicitHomeLockIgnoresInvokerXDGCache(t *testing.T) {
	invoker := t.TempDir()
	target := t.TempDir()
	invokerCache := filepath.Join(invoker, ".cache")
	if err := os.MkdirAll(invokerCache, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(invokerCache, "sentinel"), []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", invoker)
	t.Setenv("XDG_CACHE_HOME", invokerCache)

	state := &config.UserState{}
	state.Modules.Gsync.LocalPath = filepath.Join(target, "workspace", "work")
	cfg, err := ResolveConfigForHomeProfile(state, target, DefaultProfile)
	if err != nil {
		t.Fatalf("ResolveConfigForHomeProfile: %v", err)
	}
	want := filepath.Join(target, ".cache", "dotfiles", "sync.lock")
	if cfg.LockDir != want {
		t.Fatalf("Config.LockDir = %q, want target cache lock %q", cfg.LockDir, want)
	}
	paths, err := resolvePathsForHomeProfile(target, DefaultProfile)
	if err != nil {
		t.Fatalf("resolvePathsForHomeProfile: %v", err)
	}
	if paths.LockDir != want {
		t.Fatalf("Paths.LockDir = %q, want target cache lock %q", paths.LockDir, want)
	}

	release, err := AcquireLock(cfg.LockDir)
	if err != nil {
		t.Fatalf("AcquireLock(%q): %v", cfg.LockDir, err)
	}
	release()

	data, err := os.ReadFile(filepath.Join(invokerCache, "sentinel"))
	if err != nil {
		t.Fatalf("read invoker sentinel: %v", err)
	}
	if string(data) != "unchanged" {
		t.Fatalf("invoker cache changed: %q", data)
	}
	if _, err := os.Stat(filepath.Join(invokerCache, "dotfiles")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("target lock created an invoker-cache entry: %v", err)
	}
}

func TestNoOverridePreservesXDGCache(t *testing.T) {
	home := t.TempDir()
	cache := filepath.Join(t.TempDir(), "cache")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", cache)

	state := &config.UserState{}
	state.Modules.Gsync.LocalPath = filepath.Join(home, "workspace", "work")
	cfg, err := ResolveConfigForHomeProfile(state, "", DefaultProfile)
	if err != nil {
		t.Fatalf("ResolveConfigForHomeProfile: %v", err)
	}
	want := filepath.Join(cache, "dotfiles", "sync.lock")
	if cfg.LockDir != want {
		t.Fatalf("Config.LockDir = %q, want XDG cache lock %q", cfg.LockDir, want)
	}
	paths, err := resolvePathsForHomeProfile("", DefaultProfile)
	if err != nil {
		t.Fatalf("resolvePathsForHomeProfile: %v", err)
	}
	if paths.LockDir != cfg.LockDir {
		t.Fatalf("resolver LockDir = %q, want Config.LockDir %q", paths.LockDir, cfg.LockDir)
	}
}
