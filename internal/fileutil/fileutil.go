package fileutil

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

const backupDir = ".local/share/dotfiles/backup"

// EnsureFile writes content to path if it differs from current content.
// Returns true if the file was written.
func EnsureFile(runner *exec.Runner, home, path string, content []byte, perm os.FileMode) (bool, error) {
	existing, err := runner.ReadFile(path)
	if err == nil && hashBytes(existing) == hashBytes(content) {
		return false, nil
	}

	// Backup existing file
	if err == nil {
		if backupErr := backup(runner, home, path); backupErr != nil {
			return false, fmt.Errorf("backing up %q: %w", path, backupErr)
		}
	}

	// Ensure parent directory
	dir := filepath.Dir(path)
	if err := runner.MkdirAll(dir, 0755); err != nil {
		return false, fmt.Errorf("creating directory %q: %w", dir, err)
	}

	if err := runner.WriteFile(path, content, perm); err != nil {
		return false, fmt.Errorf("writing %q: %w", path, err)
	}
	return true, nil
}

// EnsureFileAtomic is EnsureFile with a temp-and-rename write, for files that
// another process rewrites concurrently: a reader must never observe a torn
// file. Rename replaces a symlink at path instead of writing through it, so
// use it only on paths owned as regular files.
func EnsureFileAtomic(runner *exec.Runner, home, path string, content []byte, perm os.FileMode) (bool, error) {
	existing, err := runner.ReadFile(path)
	if err == nil && hashBytes(existing) == hashBytes(content) {
		return false, nil
	}

	if err == nil {
		if backupErr := backup(runner, home, path); backupErr != nil {
			return false, fmt.Errorf("backing up %q: %w", path, backupErr)
		}
	}

	dir := filepath.Dir(path)
	if err := runner.MkdirAll(dir, 0755); err != nil {
		return false, fmt.Errorf("creating directory %q: %w", dir, err)
	}

	if err := runner.WriteFileAtomic(path, content, perm); err != nil {
		return false, fmt.Errorf("writing %q: %w", path, err)
	}
	return true, nil
}

// NeedsUpdate checks if a file needs to be written.
func NeedsUpdate(runner *exec.Runner, path string, content []byte) bool {
	existing, err := runner.ReadFile(path)
	if err != nil {
		return true
	}
	return hashBytes(existing) != hashBytes(content)
}

func hashBytes(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
