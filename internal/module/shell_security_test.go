package module

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	internalexec "github.com/STAIxBWLB/dotfiles/internal/exec"
)

var shellFixtureDirectories = map[string]bool{
	".devcontainer": true,
	".github":       true,
	"cache":         true,
	"lib":           true,
	"log":           true,
	"plugins":       true,
	"templates":     true,
	"themes":        true,
	"tools":         true,
}

func stageShellFixture(t *testing.T, commit string, files map[string]string) {
	t.Helper()
	original := runPinnedGit
	t.Cleanup(func() { runPinnedGit = original })
	runPinnedGit = func(_ context.Context, _ *internalexec.Runner, args ...string) (*internalexec.Result, error) {
		if args[0] == "clone" {
			stage := args[len(args)-1]
			for name, contents := range files {
				path := filepath.Join(stage, name)
				if contents == "" {
					if err := os.MkdirAll(path, 0755); err != nil {
						return nil, err
					}
					continue
				}
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					return nil, err
				}
				if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
					return nil, err
				}
			}
		}
		if len(args) >= 2 && args[len(args)-2] == "rev-parse" {
			return &internalexec.Result{Stdout: commit + "\n"}, nil
		}
		return &internalexec.Result{}, nil
	}
}

func shellBaseStageFiles(pin gitComponentPin) map[string]string {
	owned := pin.Ownership.currentEntries()
	files := make(map[string]string, len(owned))
	for _, entry := range owned {
		if shellFixtureDirectories[entry] {
			files[entry] = ""
		} else {
			files[entry] = "fixture " + entry
		}
	}
	return files
}

func TestShellBase_PreservesUnownedEntries(t *testing.T) {
	pin := shellSourcePins[0]
	stageShellFixture(t, pin.Commit, shellBaseStageFiles(pin))
	destination := filepath.Join(t.TempDir(), ".oh-my-zsh")
	if err := os.MkdirAll(filepath.Join(destination, "custom"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "custom", "user.zsh"), []byte("operator customization"), 0600); err != nil {
		t.Fatal(err)
	}

	rc := &RunContext{Runner: internalexec.NewRunner(false, slog.Default())}
	if err := activateGitComponent(context.Background(), rc, destination, pin); err != nil {
		t.Fatalf("activateGitComponent: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(destination, "custom", "user.zsh")); err != nil || string(data) != "operator customization" {
		t.Fatalf("custom entry changed: %q, %v", data, err)
	}
}

func TestShellBase_TamperedMarkerCannotDeleteUnownedEntry(t *testing.T) {
	pin := shellSourcePins[0]
	stageShellFixture(t, pin.Commit, shellBaseStageFiles(pin))
	destination := filepath.Join(t.TempDir(), ".oh-my-zsh")
	if err := os.MkdirAll(filepath.Join(destination, "custom"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "custom", "user.zsh"), []byte("operator customization"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeComponentPinMarker(filepath.Join(destination, markerFileName(pin.Name)), componentPinMarker{
		Schema: componentPinMarkerSchema, Component: pin.Name, Source: pin.Repository, Commit: pin.Commit,
		Owned: []string{"custom"}, Files: map[string]string{"custom": "untrusted"},
	}); err != nil {
		t.Fatal(err)
	}

	rc := &RunContext{Runner: internalexec.NewRunner(false, slog.Default())}
	if err := activateGitComponent(context.Background(), rc, destination, pin); err != nil {
		t.Fatalf("activateGitComponent: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(destination, "custom", "user.zsh")); err != nil || string(data) != "operator customization" {
		t.Fatalf("custom entry changed: %q, %v", data, err)
	}
}

func TestShellBase_RestoresRollbackAfterPromotionValidationFailure(t *testing.T) {
	commit := "146461f7c6d95f4ba1220559d66eb113418b40a8"
	pin := gitComponentPin{
		Name: "shell-fixture", Repository: "https://example.invalid/shell.git", Commit: commit,
		RequiredPaths: []string{"managed", "required-but-unowned"}, Ownership: componentOwnership{Current: []string{"managed"}},
	}
	stageShellFixture(t, commit, map[string]string{
		"managed":              "new",
		"required-but-unowned": "staged only",
	})
	destination := filepath.Join(t.TempDir(), ".oh-my-zsh")
	if err := os.MkdirAll(destination, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "managed"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}

	rc := &RunContext{Runner: internalexec.NewRunner(false, slog.Default())}
	err := activateGitComponent(context.Background(), rc, destination, pin)
	if err == nil {
		t.Fatal("expected promoted-layout validation failure")
	}
	if data, readErr := os.ReadFile(filepath.Join(destination, "managed")); readErr != nil || string(data) != "old" {
		t.Fatalf("active entry was not restored: %q, %v", data, readErr)
	}
}

func TestShellBase_LegacyRefreshMarkerReinstalls(t *testing.T) {
	if !legacyRefreshRequiresInstall(true, false) {
		t.Fatal("refresh-only state must be reinstalled")
	}
}
