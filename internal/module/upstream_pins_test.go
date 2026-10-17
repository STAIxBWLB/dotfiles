package module

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	internalexec "github.com/STAIxBWLB/dotfiles/internal/exec"
)

func TestStageGitComponent_RejectsRevisionMismatch(t *testing.T) {
	original := runPinnedGit
	t.Cleanup(func() { runPinnedGit = original })
	var calls [][]string
	runPinnedGit = func(_ context.Context, _ *internalexec.Runner, args ...string) (*internalexec.Result, error) {
		calls = append(calls, append([]string(nil), args...))
		if len(args) >= 2 && args[len(args)-2] == "rev-parse" {
			return &internalexec.Result{Stdout: "different-sha\n"}, nil
		}
		return &internalexec.Result{}, nil
	}
	rc := &RunContext{Runner: internalexec.NewProbeRunner()}
	pin := gitComponentPin{Name: "fixture", Repository: "https://example.invalid/repo.git", Commit: "146461f7c6d95f4ba1220559d66eb113418b40a8"}
	err := stageGitComponent(context.Background(), rc, pin, t.TempDir()+"/stage")
	if err == nil || !strings.Contains(err.Error(), "expected commit") {
		t.Fatalf("stageGitComponent error = %v, want exact-revision refusal", err)
	}
	if len(calls) != 4 || !reflect.DeepEqual(calls[0][:4], []string{"clone", "--no-checkout", "--depth", "1"}) {
		t.Fatalf("git command sequence = %#v", calls)
	}
}

func TestComponentPinLifecycle(t *testing.T) {
	marker := componentPinMarker{
		Schema:    componentPinMarkerSchema,
		Component: "oh-my-zsh-base",
		Source:    "https://github.com/ohmyzsh/ohmyzsh.git",
		Commit:    "146461f7c6d95f4ba1220559d66eb113418b40a8",
		Owned:     ohMyZshOwnership().currentEntries(),
		Files:     map[string]string{"oh-my-zsh.sh": "digest"},
	}
	if err := marker.validate(); err != nil {
		t.Fatalf("valid marker rejected: %v", err)
	}
	if !reflect.DeepEqual(marker.Owned, expectedOhMyZshOwnedEntries) {
		t.Fatalf("owned entries = %#v, want %#v", marker.Owned, expectedOhMyZshOwnedEntries)
	}
}

func TestHashManagedFiles_RecordsSafeSymlinkIdentity(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"target-a", "target-b"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("same bytes"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(root, "managed-link")
	if err := os.Symlink("target-a", link); err != nil {
		t.Fatal(err)
	}

	before, err := hashManagedFiles(root, []string{"managed-link", "target-a", "target-b"})
	if err != nil {
		t.Fatalf("hashManagedFiles with safe symlink: %v", err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target-b", link); err != nil {
		t.Fatal(err)
	}
	after, err := hashManagedFiles(root, []string{"managed-link", "target-a", "target-b"})
	if err != nil {
		t.Fatalf("hashManagedFiles after symlink retarget: %v", err)
	}
	if before["managed-link"] == after["managed-link"] {
		t.Fatal("symlink retarget did not change managed-file identity")
	}

	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", link); err != nil {
		t.Fatal(err)
	}
	if _, err := hashManagedFiles(root, []string{"managed-link"}); err == nil {
		t.Fatal("absolute managed symlink accepted")
	}
}

func TestActivateGitComponent_TamperedMarkerCannotDeleteUnownedEntry(t *testing.T) {
	original := runPinnedGit
	t.Cleanup(func() { runPinnedGit = original })
	commit := "146461f7c6d95f4ba1220559d66eb113418b40a8"
	pin := gitComponentPin{
		Name: "fixture", Repository: "https://example.invalid/fixture.git", Commit: commit,
		RequiredPaths: []string{"managed"}, Ownership: componentOwnership{Current: []string{"managed"}},
	}
	runPinnedGit = func(_ context.Context, _ *internalexec.Runner, args ...string) (*internalexec.Result, error) {
		if args[0] == "clone" {
			stage := args[len(args)-1]
			if err := os.MkdirAll(stage, 0755); err != nil {
				return nil, err
			}
			if err := os.WriteFile(filepath.Join(stage, "managed"), []byte("new"), 0600); err != nil {
				return nil, err
			}
		}
		if args[len(args)-2] == "rev-parse" {
			return &internalexec.Result{Stdout: commit + "\n"}, nil
		}
		return &internalexec.Result{}, nil
	}

	destination := filepath.Join(t.TempDir(), "component")
	if err := os.MkdirAll(destination, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "custom"), []byte("operator data"), 0600); err != nil {
		t.Fatal(err)
	}
	tampered, err := json.Marshal(componentPinMarker{
		Schema: componentPinMarkerSchema, Component: pin.Name, Source: pin.Repository, Commit: commit,
		Owned: []string{"custom"}, Files: map[string]string{"custom": "not trusted"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, markerFileName(pin.Name)), tampered, 0600); err != nil {
		t.Fatal(err)
	}

	rc := &RunContext{Runner: internalexec.NewRunner(false, slog.Default())}
	if err := activateGitComponent(context.Background(), rc, destination, pin); err != nil {
		t.Fatalf("activateGitComponent: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(destination, "custom")); err != nil || string(data) != "operator data" {
		t.Fatalf("unowned entry changed: %q, %v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(destination, "managed")); err != nil || string(data) != "new" {
		t.Fatalf("managed entry = %q, %v", data, err)
	}
}

func TestComponentOwnership_TrustedIncludesRetiredButNotArbitraryPaths(t *testing.T) {
	ownership := componentOwnership{Current: []string{"managed"}, Retired: []string{"legacy-tools"}}
	trusted := ownership.trustedEntries()
	if _, ok := trusted["managed"]; !ok {
		t.Fatal("current entry is not trusted")
	}
	if _, ok := trusted["legacy-tools"]; !ok {
		t.Fatal("retired entry is not trusted")
	}
	if _, ok := trusted["custom"]; ok {
		t.Fatal("arbitrary entry is trusted")
	}
	current := ownership.currentEntries()
	current[0] = "changed"
	if ownership.Current[0] != "managed" {
		t.Fatal("currentEntries returned the ownership backing slice")
	}
}

func TestTrustedStaleOwnedEntries(t *testing.T) {
	commit := "146461f7c6d95f4ba1220559d66eb113418b40a8"
	pin := gitComponentPin{
		Name: "fixture", Repository: "https://example.invalid/fixture.git", Commit: commit,
		Ownership: componentOwnership{Current: []string{"managed"}, Retired: []string{"legacy-tools"}},
	}
	for _, tc := range []struct {
		name     string
		previous componentPinMarker
		pin      gitComponentPin
		want     []string
	}{
		{
			name: "trusted retirement", previous: componentPinMarker{Component: pin.Name, Source: pin.Repository, Owned: []string{"legacy-tools", "managed"}}, pin: pin,
			want: []string{"legacy-tools"},
		},
		{
			name: "tampered marker is all or nothing", previous: componentPinMarker{Component: pin.Name, Source: pin.Repository, Owned: []string{"custom", "legacy-tools", "managed"}}, pin: pin,
		},
		{
			name: "different component", previous: componentPinMarker{Component: "other", Source: pin.Repository, Owned: []string{"legacy-tools", "managed"}}, pin: pin,
		},
		{
			name: "different source", previous: componentPinMarker{Component: pin.Name, Source: "https://example.invalid/other.git", Owned: []string{"legacy-tools", "managed"}}, pin: pin,
		},
		{
			name: "staged ownership pins derive from stage", previous: componentPinMarker{Component: pin.Name, Source: pin.Repository, Owned: []string{"legacy-tools"}}, pin: gitComponentPin{Name: pin.Name, Repository: pin.Repository},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := trustedStaleOwnedEntries(tc.previous, tc.pin, []string{"managed"}); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("trustedStaleOwnedEntries() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestActivateGitComponent_RetiredOwnershipCleanup(t *testing.T) {
	original := runPinnedGit
	t.Cleanup(func() { runPinnedGit = original })
	commit := "146461f7c6d95f4ba1220559d66eb113418b40a8"
	pin := gitComponentPin{
		Name: "fixture", Repository: "https://example.invalid/fixture.git", Commit: commit,
		RequiredPaths: []string{"managed"}, Ownership: componentOwnership{Current: []string{"managed"}, Retired: []string{"legacy-tools"}},
	}
	runPinnedGit = func(_ context.Context, _ *internalexec.Runner, args ...string) (*internalexec.Result, error) {
		if args[0] == "clone" {
			stage := args[len(args)-1]
			if err := os.MkdirAll(stage, 0755); err != nil {
				return nil, err
			}
			if err := os.WriteFile(filepath.Join(stage, "managed"), []byte("new"), 0600); err != nil {
				return nil, err
			}
		}
		if len(args) >= 2 && args[len(args)-2] == "rev-parse" {
			return &internalexec.Result{Stdout: commit + "\n"}, nil
		}
		return &internalexec.Result{}, nil
	}

	for _, tc := range []struct {
		name           string
		owned          []string
		wantLegacyGone bool
	}{
		{name: "trusted retired entry", owned: []string{"legacy-tools", "managed"}, wantLegacyGone: true},
		{name: "tampered marker is all or nothing", owned: []string{"custom", "legacy-tools", "managed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "component")
			if err := os.MkdirAll(destination, 0755); err != nil {
				t.Fatal(err)
			}
			for name, contents := range map[string]string{"managed": "old", "legacy-tools": "retired", "custom": "operator"} {
				if err := os.WriteFile(filepath.Join(destination, name), []byte(contents), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := writeComponentPinMarker(filepath.Join(destination, markerFileName(pin.Name)), componentPinMarker{
				Schema: componentPinMarkerSchema, Component: pin.Name, Source: pin.Repository, Commit: commit,
				Owned: tc.owned, Files: map[string]string{"managed": "old"},
			}); err != nil {
				t.Fatal(err)
			}
			rc := &RunContext{Runner: internalexec.NewRunner(false, slog.Default())}
			if err := activateGitComponent(context.Background(), rc, destination, pin); err != nil {
				t.Fatalf("activateGitComponent: %v", err)
			}
			_, legacyErr := os.Lstat(filepath.Join(destination, "legacy-tools"))
			if tc.wantLegacyGone && !os.IsNotExist(legacyErr) {
				t.Fatalf("retired entry still exists: %v", legacyErr)
			}
			if !tc.wantLegacyGone && legacyErr != nil {
				t.Fatalf("tampered marker removed retired entry: %v", legacyErr)
			}
			if data, err := os.ReadFile(filepath.Join(destination, "custom")); err != nil || string(data) != "operator" {
				t.Fatalf("custom entry = %q, %v", data, err)
			}
		})
	}
}
