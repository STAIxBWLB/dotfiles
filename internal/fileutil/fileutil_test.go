package fileutil

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

func newTestRunner() *exec.Runner {
	return exec.NewRunner(false, slog.Default())
}

func TestNeedsUpdate_MissingFile(t *testing.T) {
	runner := newTestRunner()
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.txt")

	if !NeedsUpdate(runner, path, []byte("content")) {
		t.Error("NeedsUpdate: expected true for missing file, got false")
	}
}

func TestNeedsUpdate_IdenticalContent(t *testing.T) {
	runner := newTestRunner()
	dir := t.TempDir()
	path := filepath.Join(dir, "same.txt")
	content := []byte("hello world\n")

	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}

	if NeedsUpdate(runner, path, content) {
		t.Error("NeedsUpdate: expected false for identical content, got true")
	}
}

func TestNeedsUpdate_DifferentContent(t *testing.T) {
	runner := newTestRunner()
	dir := t.TempDir()
	path := filepath.Join(dir, "changed.txt")

	if err := os.WriteFile(path, []byte("old content"), 0644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}

	if !NeedsUpdate(runner, path, []byte("new content")) {
		t.Error("NeedsUpdate: expected true for different content, got false")
	}
}

func TestNeedsUpdate_EmptyVsNonEmpty(t *testing.T) {
	runner := newTestRunner()
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")

	if err := os.WriteFile(path, []byte{}, 0644); err != nil {
		t.Fatalf("writing empty test file: %v", err)
	}

	if !NeedsUpdate(runner, path, []byte("something")) {
		t.Error("NeedsUpdate: expected true when file is empty but content is not, got false")
	}
}

func TestNeedsUpdate_BothEmpty(t *testing.T) {
	runner := newTestRunner()
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")

	if err := os.WriteFile(path, []byte{}, 0644); err != nil {
		t.Fatalf("writing empty test file: %v", err)
	}

	if NeedsUpdate(runner, path, []byte{}) {
		t.Error("NeedsUpdate: expected false when both file and content are empty, got true")
	}
}

func TestEnsureFile_WritesNewFile(t *testing.T) {
	runner := newTestRunner()
	dir := t.TempDir()
	path := filepath.Join(dir, "subdir", "new.txt")
	content := []byte("new file content\n")

	written, err := EnsureFile(runner, dir, path, content, 0644)
	if err != nil {
		t.Fatalf("EnsureFile: %v", err)
	}
	if !written {
		t.Error("EnsureFile: expected written=true for new file")
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading written file: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("EnsureFile: written content = %q, want %q", got, content)
	}
}

func TestEnsureFile_SkipsIdentical(t *testing.T) {
	runner := newTestRunner()
	dir := t.TempDir()
	path := filepath.Join(dir, "existing.txt")
	content := []byte("same content\n")

	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	written, err := EnsureFile(runner, dir, path, content, 0644)
	if err != nil {
		t.Fatalf("EnsureFile: %v", err)
	}
	if written {
		t.Error("EnsureFile: expected written=false for identical content")
	}
}

func TestEnsureFile_UpdatesDifferent(t *testing.T) {
	runner := newTestRunner()
	dir := t.TempDir()
	path := filepath.Join(dir, "update.txt")

	if err := os.WriteFile(path, []byte("old"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	newContent := []byte("new content")
	written, err := EnsureFile(runner, dir, path, newContent, 0644)
	if err != nil {
		t.Fatalf("EnsureFile: %v", err)
	}
	if !written {
		t.Error("EnsureFile: expected written=true for different content")
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading updated file: %v", err)
	}
	if string(got) != string(newContent) {
		t.Errorf("EnsureFile: content after update = %q, want %q", got, newContent)
	}
}

func TestEnsureFileAtomic_WritesAndReplaces(t *testing.T) {
	runner := newTestRunner()
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "config.toml")
	first := []byte("v1\n")

	written, err := EnsureFileAtomic(runner, dir, path, first, 0o600)
	if err != nil || !written {
		t.Fatalf("EnsureFileAtomic new file: written=%v err=%v", written, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("perm = %v, want 0600", info.Mode().Perm())
	}

	written, err = EnsureFileAtomic(runner, dir, path, first, 0o600)
	if err != nil || written {
		t.Fatalf("identical content: written=%v err=%v, want skip", written, err)
	}

	second := []byte("v2\n")
	written, err = EnsureFileAtomic(runner, dir, path, second, 0o600)
	if err != nil || !written {
		t.Fatalf("replace: written=%v err=%v", written, err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "v2\n" {
		t.Errorf("content = %q, want v2", got)
	}
	// No temp file may be left behind next to the target.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "config.toml" {
			t.Errorf("leftover file %q in target dir", e.Name())
		}
	}
}

func TestEnsureFileAtomic_RejectsSymlinkTarget(t *testing.T) {
	runner := newTestRunner()
	dir := t.TempDir()
	real := filepath.Join(dir, "real.toml")
	if err := os.WriteFile(real, []byte("real\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.toml")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	if _, err := EnsureFileAtomic(runner, dir, link, []byte("new\n"), 0o600); err == nil {
		t.Fatal("atomic write over a symlink must error, not replace the link")
	}
	got, _ := os.ReadFile(real)
	if string(got) != "real\n" {
		t.Errorf("symlink target mutated: %q", got)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Error("symlink was replaced")
	}
}
