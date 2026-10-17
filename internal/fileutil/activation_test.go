package fileutil

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

func activationRunner(dryRun bool) *exec.Runner {
	return exec.NewRunner(dryRun, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
}

func writeActivationFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestActivateOwnedComponent(t *testing.T) {
	t.Run("replaces only owned entries", func(t *testing.T) {
		root := t.TempDir()
		dest := filepath.Join(root, "component")
		stage := filepath.Join(root, ".component-stage")
		writeActivationFile(t, filepath.Join(dest, "managed"), "old")
		writeActivationFile(t, filepath.Join(dest, "custom", "user.txt"), "user")
		writeActivationFile(t, filepath.Join(stage, "managed"), "new")

		if err := ActivateOwnedComponent(activationRunner(false), ActivationOptions{
			DestinationRoot: dest,
			StagedRoot:      stage,
			OwnedEntries:    []string{"managed"},
			Validate: func(path string) error {
				_, err := os.Stat(filepath.Join(path, "managed"))
				return err
			},
		}); err != nil {
			t.Fatalf("ActivateOwnedComponent: %v", err)
		}
		if data, err := os.ReadFile(filepath.Join(dest, "managed")); err != nil || string(data) != "new" {
			t.Fatalf("managed entry = %q, %v", data, err)
		}
		if data, err := os.ReadFile(filepath.Join(dest, "custom", "user.txt")); err != nil || string(data) != "user" {
			t.Fatalf("unmanaged entry = %q, %v", data, err)
		}
	})

	t.Run("failed promotion restores old entry", func(t *testing.T) {
		root := t.TempDir()
		dest := filepath.Join(root, "component")
		stage := filepath.Join(root, ".component-stage")
		writeActivationFile(t, filepath.Join(dest, "managed"), "old")
		writeActivationFile(t, filepath.Join(stage, "managed"), "new")

		oldRename := activationRename
		t.Cleanup(func() { activationRename = oldRename })
		activationRename = func(root *os.Root, oldpath, newpath string) error {
			if oldpath == filepath.Join(filepath.Base(stage), "managed") && newpath == filepath.Join(filepath.Base(dest), "managed") {
				return errors.New("injected promotion failure")
			}
			return root.Rename(oldpath, newpath)
		}

		err := ActivateOwnedComponent(activationRunner(false), ActivationOptions{
			DestinationRoot: dest,
			StagedRoot:      stage,
			OwnedEntries:    []string{"managed"},
			Validate:        func(string) error { return nil },
		})
		if err == nil {
			t.Fatal("expected promotion failure")
		}
		if data, readErr := os.ReadFile(filepath.Join(dest, "managed")); readErr != nil || string(data) != "old" {
			t.Fatalf("old entry was not restored: %q, %v", data, readErr)
		}
		entries, readErr := os.ReadDir(root)
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, entry := range entries {
			if strings.Contains(entry.Name(), "rollback") {
				t.Fatalf("rollback directory leaked: %s", entry.Name())
			}
		}
	})

	t.Run("failed restoration preserves rollback entry", func(t *testing.T) {
		root := t.TempDir()
		dest := filepath.Join(root, "component")
		stage := filepath.Join(root, ".component-stage")
		writeActivationFile(t, filepath.Join(dest, "managed"), "old")
		writeActivationFile(t, filepath.Join(stage, "managed"), "new")

		oldRename := activationRename
		t.Cleanup(func() { activationRename = oldRename })
		activationRename = func(root *os.Root, oldpath, newpath string) error {
			if oldpath == filepath.Join(filepath.Base(stage), "managed") && newpath == filepath.Join(filepath.Base(dest), "managed") {
				return errors.New("injected promotion failure")
			}
			if strings.Contains(oldpath, ".component.rollback-") && newpath == filepath.Join(filepath.Base(dest), "managed") {
				return errors.New("injected restoration failure")
			}
			return root.Rename(oldpath, newpath)
		}

		err := ActivateOwnedComponent(activationRunner(false), ActivationOptions{
			DestinationRoot: dest,
			StagedRoot:      stage,
			OwnedEntries:    []string{"managed"},
			Validate:        func(string) error { return nil },
		})
		if err == nil || !strings.Contains(err.Error(), "rollback artifacts preserved") {
			t.Fatalf("activation error = %v, want preserved rollback artifact", err)
		}
		entries, readErr := os.ReadDir(root)
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, entry := range entries {
			if !strings.Contains(entry.Name(), "rollback") {
				continue
			}
			data, readErr := os.ReadFile(filepath.Join(root, entry.Name(), "managed"))
			if readErr != nil || string(data) != "old" {
				t.Fatalf("preserved rollback entry = %q, %v", data, readErr)
			}
			return
		}
		t.Fatal("rollback directory was removed after restoration failure")
	})

	t.Run("staged validation preserves active component", func(t *testing.T) {
		root := t.TempDir()
		dest := filepath.Join(root, "component")
		stage := filepath.Join(root, ".component-stage")
		writeActivationFile(t, filepath.Join(dest, "managed"), "old")
		writeActivationFile(t, filepath.Join(stage, "managed"), "new")
		err := ActivateOwnedComponent(activationRunner(false), ActivationOptions{
			DestinationRoot: dest,
			StagedRoot:      stage,
			OwnedEntries:    []string{"managed"},
			Validate:        func(string) error { return errors.New("invalid stage") },
		})
		if err == nil {
			t.Fatal("expected validation failure")
		}
		if data, readErr := os.ReadFile(filepath.Join(dest, "managed")); readErr != nil || string(data) != "old" {
			t.Fatalf("active entry changed after validation failure: %q, %v", data, readErr)
		}
	})

	t.Run("promoted validation rolls back in order", func(t *testing.T) {
		root := t.TempDir()
		dest := filepath.Join(root, "component")
		stage := filepath.Join(root, ".component-stage")
		writeActivationFile(t, filepath.Join(dest, "managed"), "old")
		writeActivationFile(t, filepath.Join(stage, "managed"), "new")
		var validations []string
		err := ActivateOwnedComponent(activationRunner(false), ActivationOptions{
			DestinationRoot: dest,
			StagedRoot:      stage,
			OwnedEntries:    []string{"managed"},
			Validate: func(path string) error {
				validations = append(validations, path)
				if path == dest {
					return errors.New("invalid promoted state")
				}
				return nil
			},
		})
		if err == nil {
			t.Fatal("expected promoted validation failure")
		}
		if got, want := strings.Join(validations, ","), stage+","+dest; got != want {
			t.Fatalf("validation order = %q, want %q", got, want)
		}
		if data, readErr := os.ReadFile(filepath.Join(dest, "managed")); readErr != nil || string(data) != "old" {
			t.Fatalf("old entry was not restored after promoted validation: %q, %v", data, readErr)
		}
	})

	t.Run("stale entries are removed only after promoted validation", func(t *testing.T) {
		root := t.TempDir()
		dest := filepath.Join(root, "component")
		stage := filepath.Join(root, ".component-stage")
		writeActivationFile(t, filepath.Join(dest, "managed"), "old")
		writeActivationFile(t, filepath.Join(dest, "stale"), "old stale")
		writeActivationFile(t, filepath.Join(stage, "managed"), "new")

		err := ActivateOwnedComponent(activationRunner(false), ActivationOptions{
			DestinationRoot: dest,
			StagedRoot:      stage,
			OwnedEntries:    []string{"managed"},
			StaleEntries:    []string{"stale"},
			Validate: func(path string) error {
				if path == dest {
					return errors.New("invalid promoted state")
				}
				return nil
			},
		})
		if err == nil {
			t.Fatal("expected promoted validation failure")
		}
		if data, readErr := os.ReadFile(filepath.Join(dest, "managed")); readErr != nil || string(data) != "old" {
			t.Fatalf("managed entry was not restored: %q, %v", data, readErr)
		}
		if data, readErr := os.ReadFile(filepath.Join(dest, "stale")); readErr != nil || string(data) != "old stale" {
			t.Fatalf("stale entry was not restored: %q, %v", data, readErr)
		}

		stage = filepath.Join(root, ".component-stage-success")
		writeActivationFile(t, filepath.Join(stage, "managed"), "new")
		if err := ActivateOwnedComponent(activationRunner(false), ActivationOptions{
			DestinationRoot: dest,
			StagedRoot:      stage,
			OwnedEntries:    []string{"managed"},
			StaleEntries:    []string{"stale"},
			Validate:        func(string) error { return nil },
		}); err != nil {
			t.Fatalf("successful stale activation: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(dest, "stale")); !os.IsNotExist(err) {
			t.Fatalf("stale entry remains after success: %v", err)
		}
	})

	t.Run("dry run does not mutate", func(t *testing.T) {
		root := t.TempDir()
		dest := filepath.Join(root, "component")
		stage := filepath.Join(root, ".component-stage")
		writeActivationFile(t, filepath.Join(dest, "managed"), "old")
		writeActivationFile(t, filepath.Join(stage, "managed"), "new")
		if err := ActivateOwnedComponent(activationRunner(true), ActivationOptions{
			DestinationRoot: dest,
			StagedRoot:      stage,
			OwnedEntries:    []string{"managed"},
			Validate:        func(string) error { return nil },
		}); err != nil {
			t.Fatalf("dry-run ActivateOwnedComponent: %v", err)
		}
		if data, err := os.ReadFile(filepath.Join(dest, "managed")); err != nil || string(data) != "old" {
			t.Fatalf("dry run changed active entry: %q, %v", data, err)
		}
	})

	t.Run("rejects symlinked component root without touching its target", func(t *testing.T) {
		root := t.TempDir()
		external := filepath.Join(root, "external")
		dest := filepath.Join(root, "component")
		stage := filepath.Join(root, ".component-stage")
		writeActivationFile(t, filepath.Join(external, "managed"), "outside old")
		writeActivationFile(t, filepath.Join(stage, "managed"), "new")
		if err := os.Symlink(external, dest); err != nil {
			t.Fatal(err)
		}

		err := ActivateOwnedComponent(activationRunner(false), ActivationOptions{
			DestinationRoot: dest,
			StagedRoot:      stage,
			OwnedEntries:    []string{"managed"},
			Validate:        func(string) error { return nil },
		})
		if err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Fatalf("activation error = %v, want symlink refusal", err)
		}
		if data, readErr := os.ReadFile(filepath.Join(external, "managed")); readErr != nil || string(data) != "outside old" {
			t.Fatalf("external file changed: %q, %v", data, readErr)
		}
	})

	t.Run("rejects symlinked intermediate path without touching its target", func(t *testing.T) {
		root := t.TempDir()
		dest := filepath.Join(root, "component")
		stage := filepath.Join(root, ".component-stage")
		external := filepath.Join(root, "external")
		writeActivationFile(t, filepath.Join(external, "font.ttf"), "outside old")
		writeActivationFile(t, filepath.Join(stage, "nested", "font.ttf"), "new")
		if err := os.MkdirAll(dest, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(external, filepath.Join(dest, "nested")); err != nil {
			t.Fatal(err)
		}

		err := ActivateOwnedComponent(activationRunner(false), ActivationOptions{
			DestinationRoot: dest,
			StagedRoot:      stage,
			OwnedEntries:    []string{filepath.Join("nested", "font.ttf")},
			Validate:        func(string) error { return nil },
		})
		if err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Fatalf("activation error = %v, want symlink refusal", err)
		}
		if data, readErr := os.ReadFile(filepath.Join(external, "font.ttf")); readErr != nil || string(data) != "outside old" {
			t.Fatalf("external file changed: %q, %v", data, readErr)
		}
	})
}

func TestActivateOwnedComponent_Concurrent(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "component")
	stage := filepath.Join(root, ".component-stage")
	writeActivationFile(t, filepath.Join(dest, "managed"), "old")
	writeActivationFile(t, filepath.Join(stage, "managed"), "new")
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	validatedStage := false
	go func() {
		done <- ActivateOwnedComponent(activationRunner(false), ActivationOptions{
			DestinationRoot: dest,
			StagedRoot:      stage,
			OwnedEntries:    []string{"managed"},
			Validate: func(path string) error {
				if path == stage && !validatedStage {
					validatedStage = true
					close(entered)
					<-release
				}
				return nil
			},
		})
	}()
	<-entered
	err := ActivateOwnedComponent(activationRunner(false), ActivationOptions{
		DestinationRoot: dest,
		StagedRoot:      stage,
		OwnedEntries:    []string{"managed"},
		Validate:        func(string) error { return nil },
	})
	if !errors.Is(err, ErrLockHeld) {
		t.Fatalf("concurrent activation error = %v, want ErrLockHeld", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("first activation: %v", err)
	}
}
