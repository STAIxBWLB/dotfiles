package module

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	dotexec "github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/template"
)

const lifecycleFixtureCommit = "146461f7c6d95f4ba1220559d66eb113418b40a8"

func lifecycleFixturePin(commit string) gitComponentPin {
	return gitComponentPin{
		Name: "lifecycle-fixture", Repository: "https://example.invalid/lifecycle.git", Commit: commit,
		RequiredPaths: []string{"oh-my-zsh.sh", "lib"},
		Ownership:     componentOwnership{Current: []string{"lib", "oh-my-zsh.sh"}},
	}
}

func withShellSourcePins(t *testing.T, pins ...gitComponentPin) {
	t.Helper()
	original := shellSourcePins
	shellSourcePins = append([]gitComponentPin(nil), pins...)
	t.Cleanup(func() { shellSourcePins = original })
}

func withoutZsh(t *testing.T) {
	t.Helper()
	original := zshCandidatePaths
	zshCandidatePaths = []string{filepath.Join(t.TempDir(), "not-zsh")}
	t.Cleanup(func() { zshCandidatePaths = original })
}

func shellLifecycleContext(t *testing.T) (*RunContext, string) {
	t.Helper()
	home := t.TempDir()
	return &RunContext{
		Config:   &config.Config{},
		Runner:   dotexec.NewRunner(false, slog.New(slog.NewTextHandler(io.Discard, nil))),
		Template: template.NewEngine(),
		Out:      io.Discard,
		HomeDir:  home,
	}, home
}

func installFixtureComponent(t *testing.T, rc *RunContext, pin gitComponentPin, contents map[string]string) {
	t.Helper()
	stageShellFixture(t, pin.Commit, contents)
	if err := activateGitComponent(context.Background(), rc, (&ShellModule{}).sourceDestination(rc.HomeDir, pin), pin); err != nil {
		t.Fatalf("installing fixture component: %v", err)
	}
}

func sourceChangeScheduled(result *CheckResult, name string) bool {
	for _, change := range result.Changes {
		if change.Description == "install pinned source "+name {
			return true
		}
	}
	return false
}

func sourceMessageInstalled(result *ApplyResult, name string) bool {
	for _, message := range result.Messages {
		if message == "installed pinned source "+name {
			return true
		}
	}
	return false
}

func readFixtureMarker(t *testing.T, destination, name string) componentPinMarker {
	t.Helper()
	marker, err := readComponentPinMarker(filepath.Join(destination, markerFileName(name)))
	if err != nil {
		t.Fatalf("reading fixture marker: %v", err)
	}
	return marker
}

func TestShellModule_OfflineVerifiedMarkerNeedsNoGit(t *testing.T) {
	withoutZsh(t)
	pin := lifecycleFixturePin(lifecycleFixtureCommit)
	withShellSourcePins(t, pin)
	rc, _ := shellLifecycleContext(t)
	installFixtureComponent(t, rc, pin, map[string]string{"oh-my-zsh.sh": "compiled", "lib": ""})

	original := runPinnedGit
	t.Cleanup(func() { runPinnedGit = original })
	runPinnedGit = func(_ context.Context, _ *dotexec.Runner, args ...string) (*dotexec.Result, error) {
		t.Fatalf("offline-valid component ran git: %q", args)
		return nil, nil
	}

	module := &ShellModule{}
	check, err := module.Check(context.Background(), rc)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if sourceChangeScheduled(check, pin.Name) {
		t.Fatalf("Check scheduled source install: %#v", check.Changes)
	}
	apply, err := module.Apply(context.Background(), rc)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if sourceMessageInstalled(apply, pin.Name) {
		t.Fatalf("Apply installed offline-valid source: %#v", apply.Messages)
	}
}

func TestShellModule_UnverifiedInstalledStatesReinstall(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(t *testing.T, destination string, pin gitComponentPin)
	}{
		{
			name: "legacy marker only",
			prepare: func(t *testing.T, destination string, pin gitComponentPin) {
				t.Helper()
				if err := os.Remove(filepath.Join(destination, markerFileName(pin.Name))); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(destination, ".dotfiles-refresh"), []byte("legacy"), 0600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "missing marker",
			prepare: func(t *testing.T, destination string, pin gitComponentPin) {
				t.Helper()
				if err := os.Remove(filepath.Join(destination, markerFileName(pin.Name))); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "malformed marker",
			prepare: func(t *testing.T, destination string, pin gitComponentPin) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(destination, markerFileName(pin.Name)), []byte(`{"schema":`), 0600); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withoutZsh(t)
			pin := lifecycleFixturePin(lifecycleFixtureCommit)
			withShellSourcePins(t, pin)
			rc, _ := shellLifecycleContext(t)
			installFixtureComponent(t, rc, pin, map[string]string{"oh-my-zsh.sh": "installed", "lib": ""})
			destination := (&ShellModule{}).sourceDestination(rc.HomeDir, pin)
			if err := os.WriteFile(filepath.Join(destination, "oh-my-zsh.sh"), []byte("unverified"), 0600); err != nil {
				t.Fatal(err)
			}
			tc.prepare(t, destination, pin)
			stageShellFixture(t, pin.Commit, map[string]string{"oh-my-zsh.sh": "compiled", "lib": ""})

			module := &ShellModule{}
			check, err := module.Check(context.Background(), rc)
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			if !sourceChangeScheduled(check, pin.Name) {
				t.Fatalf("Check did not schedule source install: %#v", check.Changes)
			}
			if _, err := module.Apply(context.Background(), rc); err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if marker := readFixtureMarker(t, destination, pin.Name); marker.Commit != pin.Commit {
				t.Fatalf("marker commit = %q, want %q", marker.Commit, pin.Commit)
			}
			if data, err := os.ReadFile(filepath.Join(destination, "oh-my-zsh.sh")); err != nil || string(data) != "compiled" {
				t.Fatalf("managed file = %q, %v", data, err)
			}
		})
	}
}

func TestShellModule_InstalledDriftReinstallsAndPreservesOperatorPaths(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(t *testing.T, destination string, pin gitComponentPin)
	}{
		{
			name: "marker commit drift",
			prepare: func(t *testing.T, destination string, pin gitComponentPin) {
				t.Helper()
				marker := readFixtureMarker(t, destination, pin.Name)
				marker.Commit = "0f1e2d3c4b5a69788796a5b4c3d2e1f009182736"
				if err := writeComponentPinMarker(filepath.Join(destination, markerFileName(pin.Name)), marker); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "marker owned list drift",
			prepare: func(t *testing.T, destination string, pin gitComponentPin) {
				t.Helper()
				marker := readFixtureMarker(t, destination, pin.Name)
				marker.Owned = []string{"oh-my-zsh.sh"}
				if err := writeComponentPinMarker(filepath.Join(destination, markerFileName(pin.Name)), marker); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "managed file digest drift",
			prepare: func(t *testing.T, destination string, _ gitComponentPin) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(destination, "oh-my-zsh.sh"), []byte("tampered"), 0600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "missing required path",
			prepare: func(t *testing.T, destination string, _ gitComponentPin) {
				t.Helper()
				if err := os.RemoveAll(filepath.Join(destination, "lib")); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withoutZsh(t)
			pin := lifecycleFixturePin(lifecycleFixtureCommit)
			withShellSourcePins(t, pin)
			rc, _ := shellLifecycleContext(t)
			installFixtureComponent(t, rc, pin, map[string]string{"oh-my-zsh.sh": "installed", "lib": ""})
			destination := (&ShellModule{}).sourceDestination(rc.HomeDir, pin)
			custom := filepath.Join(destination, "custom", "user.zsh")
			if err := os.MkdirAll(filepath.Dir(custom), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(custom, []byte("operator"), 0600); err != nil {
				t.Fatal(err)
			}
			tc.prepare(t, destination, pin)
			stageShellFixture(t, pin.Commit, map[string]string{"oh-my-zsh.sh": "compiled", "lib": ""})

			module := &ShellModule{}
			check, err := module.Check(context.Background(), rc)
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			if !sourceChangeScheduled(check, pin.Name) {
				t.Fatalf("Check did not schedule source install: %#v", check.Changes)
			}
			if _, err := module.Apply(context.Background(), rc); err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if marker := readFixtureMarker(t, destination, pin.Name); marker.Commit != pin.Commit {
				t.Fatalf("marker commit = %q, want %q", marker.Commit, pin.Commit)
			}
			if data, err := os.ReadFile(custom); err != nil || string(data) != "operator" {
				t.Fatalf("operator path = %q, %v", data, err)
			}
		})
	}
}

func TestShellModule_DowngradesToOlderCompiledPin(t *testing.T) {
	withoutZsh(t)
	newer := lifecycleFixturePin("0f1e2d3c4b5a69788796a5b4c3d2e1f009182736")
	older := lifecycleFixturePin(lifecycleFixtureCommit)
	rc, _ := shellLifecycleContext(t)
	module := &ShellModule{}

	withShellSourcePins(t, newer)
	stageShellFixture(t, newer.Commit, map[string]string{"oh-my-zsh.sh": "newer upstream", "lib": ""})
	if _, err := module.Apply(context.Background(), rc); err != nil {
		t.Fatalf("installing newer pin: %v", err)
	}
	destination := module.sourceDestination(rc.HomeDir, newer)
	if marker := readFixtureMarker(t, destination, newer.Name); marker.Commit != newer.Commit {
		t.Fatalf("newer marker commit = %q, want %q", marker.Commit, newer.Commit)
	}
	if data, err := os.ReadFile(filepath.Join(destination, "oh-my-zsh.sh")); err != nil || string(data) != "newer upstream" {
		t.Fatalf("newer managed file = %q, %v", data, err)
	}
	custom := filepath.Join(destination, "custom", "user.zsh")
	if err := os.MkdirAll(filepath.Dir(custom), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(custom, []byte("operator"), 0600); err != nil {
		t.Fatal(err)
	}

	withShellSourcePins(t, older)
	stageShellFixture(t, older.Commit, map[string]string{"oh-my-zsh.sh": "older upstream", "lib": ""})
	// The labels identify fixture order only; the binary's compiled pin wins.
	if module.sourcePinMatches(rc, destination, older) {
		t.Fatal("older compiled pin accepted newer installed marker")
	}
	check, err := module.Check(context.Background(), rc)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !sourceChangeScheduled(check, older.Name) {
		t.Fatalf("Check changes = %#v, want source install", check.Changes)
	}
	apply, err := module.Apply(context.Background(), rc)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !sourceMessageInstalled(apply, older.Name) {
		t.Fatalf("Apply messages = %#v, want source install", apply.Messages)
	}
	if marker := readFixtureMarker(t, destination, older.Name); marker.Commit != older.Commit {
		t.Fatalf("older marker commit = %q, want %q", marker.Commit, older.Commit)
	}
	if data, err := os.ReadFile(filepath.Join(destination, "oh-my-zsh.sh")); err != nil || string(data) != "older upstream" {
		t.Fatalf("older managed file = %q, %v", data, err)
	}
	if data, err := os.ReadFile(custom); err != nil || string(data) != "operator" {
		t.Fatalf("operator path = %q, %v", data, err)
	}
}
