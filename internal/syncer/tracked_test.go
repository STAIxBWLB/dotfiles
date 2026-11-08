package syncer

import (
	"context"
	"os"
	osexec "os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestGitSubmodulePaths_ParsesGitmodules(t *testing.T) {
	root := t.TempDir()
	gitmodules := `[submodule "dev"]
	path = dev
	url = https://example.com/dev.git
[submodule "sites/a"]
	path = sites/a
	url = https://example.com/a.git
[submodule "vault"]
	path = vault
	url = https://example.com/vault.git
[submodule "my sub"]
	path = vendor/my sub
	url = https://example.com/my-sub.git
[submodule "a b"]
	path = a  b
	url = https://example.com/a-b.git
`
	if err := os.WriteFile(filepath.Join(root, ".gitmodules"), []byte(gitmodules), 0o644); err != nil {
		t.Fatal(err)
	}
	got := gitSubmodulePaths(root)
	want := []string{"a  b", "dev", "sites/a", "vault", "vendor/my sub"}
	if !slices.Equal(got, want) {
		t.Errorf("gitSubmodulePaths = %v, want %v", got, want)
	}
}

func TestGitSubmodulePaths_MissingFileReturnsNil(t *testing.T) {
	if got := gitSubmodulePaths(t.TempDir()); got != nil {
		t.Errorf("expected nil for missing .gitmodules, got %v", got)
	}
}

func TestPush_SubmodulePathsWithSpacesAreExcludedByBothFilters(t *testing.T) {
	requireRsync(t)
	tests := []struct {
		name string
		path string
		near string
	}{
		{name: "my sub", path: "vendor/my sub", near: "vendor/my sub-nearby"},
		{name: "sub", path: "a  b", near: "a b"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			f := newIntakeFixture(t)
			gitmodules := "[submodule \"" + tt.name + "\"]\n\tpath = " + tt.path + "\n\turl = https://example.com/lib.git\n"
			if err := os.WriteFile(filepath.Join(f.local, ".gitmodules"), []byte(gitmodules), 0o644); err != nil {
				t.Fatal(err)
			}
			excluded := tt.path + "/inside.txt"
			f.writeLocal(excluded, "local submodule content")
			f.writeMirror(excluded, "mirror submodule content")
			kept := tt.near + "/nearby.txt"
			f.writeLocal(kept, "workspace content")

			filter, err := newSyncFilter(f.cfg, f.mirror)
			if err != nil {
				t.Fatal(err)
			}
			if !filter.shouldSkip(filepath.Join(f.local, excluded), excluded, false) {
				t.Fatalf("Go filter did not exclude submodule path %q; paths=%q", excluded, filter.submodules)
			}
			plan, err := PlanPush(f.cfg)
			if err != nil {
				t.Fatalf("PlanPush: %v", err)
			}
			if slices.Contains(plan.Creates, excluded) || slices.Contains(plan.Updates, excluded) || slices.Contains(plan.Deletes, excluded) {
				t.Errorf("PlanPush includes excluded submodule path %q: %+v", excluded, plan)
			}
			if err := os.Remove(filepath.Join(f.local, excluded)); err != nil {
				t.Fatal(err)
			}

			// PullTracked must not restore a mirror-side path under a submodule.
			mtime, err := os.Stat(filepath.Join(f.mirror, excluded))
			if err != nil {
				t.Fatal(err)
			}
			f.seedBaseline(excluded, "mirror submodule content", mtime.ModTime())
			if _, err := PullTracked(f.cfg, PullOptions{}); err != nil {
				t.Fatalf("PullTracked: %v", err)
			}
			if _, err := os.Stat(filepath.Join(f.local, excluded)); !os.IsNotExist(err) {
				t.Errorf("PullTracked restored excluded submodule path (err=%v)", err)
			}

			fetch, err := Fetch(context.Background(), f.runner, f.cfg, []string{excluded}, true)
			if err != nil {
				t.Fatalf("Fetch dry run: %v", err)
			}
			if !slices.Equal(fetch.Excluded, []string{excluded}) {
				t.Errorf("Fetch Excluded = %q, want [%q]", fetch.Excluded, excluded)
			}

			f.writeLocal(excluded, "local submodule content")
			if err := Push(context.Background(), f.runner, f.cfg, false); err != nil {
				t.Fatalf("Push: %v", err)
			}
			got, err := os.ReadFile(filepath.Join(f.mirror, excluded))
			if err != nil || string(got) != "mirror submodule content" {
				t.Errorf("rsync changed excluded submodule file: body=%q err=%v", got, err)
			}
			if _, err := os.Stat(filepath.Join(f.mirror, kept)); err != nil {
				t.Errorf("rsync excluded neighboring path %q: %v", kept, err)
			}
		})
	}
}

func TestGitTrackedForSync_SkipsGitlinks(t *testing.T) {
	root := t.TempDir()
	mustGit := func(args ...string) {
		cmd := osexec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v unavailable: %v\n%s", args, err, out)
		}
	}
	mustGit("init")
	if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit("add", "notes.md")
	// Fake a gitlink entry (mode 160000) without needing a real submodule.
	mustGit("update-index", "--add", "--cacheinfo", "160000",
		"0000000000000000000000000000000000000001", "fakesub")

	got := gitTrackedForSync(root)
	if !got["notes.md"] {
		t.Errorf("tracked file missing: %v", got)
	}
	if got["fakesub"] {
		t.Errorf("gitlink leaked into tracked set: %v", got)
	}
}

func TestUnionTrackedWithBaseline_DeletePropagation(t *testing.T) {
	// A tracked file deleted locally leaves git ls-files but must stay in
	// the include layer via its baseline key so rsync --delete can remove
	// the mirror copy (excluded dest files are protected from --delete).
	tracked := map[string]bool{"kept.md": true}
	baseline := map[string]Fingerprint{
		"kept.md":    {},
		"deleted.md": {},
	}
	got := unionTrackedWithBaseline(tracked, baseline)
	want := []string{"deleted.md", "kept.md"}
	if !slices.Equal(got, want) {
		t.Errorf("union = %v, want %v", got, want)
	}
}

func TestMaterializeSubmodulesAndTrackedFiles(t *testing.T) {
	paths := ResolveLocalPaths(t.TempDir())
	subPath, err := MaterializeSubmodulesDynFile(paths.StoreDir, []string{"dev", "sites/a"})
	if err != nil {
		t.Fatal(err)
	}
	subBody, _ := os.ReadFile(subPath)
	for _, want := range []string{"/dev\n", "/dev/\n", "/sites/a\n", "/sites/a/\n"} {
		if !strings.Contains(string(subBody), want) {
			t.Errorf("submodules dyn missing %q:\n%s", want, subBody)
		}
	}

	trackedPath, err := MaterializeTrackedIncludesFile(paths.StoreDir, []string{"a/b.md", "c.pdf"})
	if err != nil {
		t.Fatal(err)
	}
	trackedBody, _ := os.ReadFile(trackedPath)
	for _, want := range []string{"/a/b.md\n", "/c.pdf\n"} {
		if !strings.Contains(string(trackedBody), want) {
			t.Errorf("tracked dyn missing %q:\n%s", want, trackedBody)
		}
	}
}

// A workspace with only the pre-2.59.0 store is refused with the move that
// keeps its baseline, never synced from an empty new store (#371).
func TestLegacyStoreError(t *testing.T) {
	root := t.TempDir()
	if err := LegacyStoreError(root); err != nil {
		t.Fatalf("no store at all: %v", err)
	}
	oldStore := filepath.Join(root, ".dotfiles", "gdrive-sync")
	if err := os.MkdirAll(oldStore, 0o755); err != nil {
		t.Fatal(err)
	}
	err := LegacyStoreError(root)
	if err == nil || !strings.Contains(err.Error(), "mv ") || !strings.Contains(err.Error(), filepath.Join(root, ".dotfiles", "sync")) {
		t.Fatalf("legacy only: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".dotfiles", "sync"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := LegacyStoreError(root); err != nil {
		t.Fatalf("new store present: %v", err)
	}
}

// The printed remedy is shell-safe for any workspace path and keeps the old
// baseline: run it through /bin/sh on a path holding $ and a quote (#371).
func TestLegacyStoreErrorRemedyRunsInShell(t *testing.T) {
	root := filepath.Join(t.TempDir(), "work $HOME it's")
	old := filepath.Join(root, ".dotfiles", "gdrive-sync")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "baseline.manifest"), []byte("docs/a.md\t1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("!/.dotfiles/gdrive-sync/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := LegacyStoreError(root)
	if err == nil {
		t.Fatal("no refusal")
	}
	var command string
	for _, line := range strings.Split(err.Error(), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "mv ") {
			command = strings.TrimSpace(line)
		}
	}
	cmd := osexec.Command("/bin/sh", "-c", command)
	cmd.Env = []string{"PATH=/bin:/usr/bin", "HOME=/nonexistent"}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v %s", command, err, out)
	}
	if data, err := os.ReadFile(filepath.Join(root, ".dotfiles", "sync", "baseline.manifest")); err != nil || string(data) != "docs/a.md\t1\n" {
		t.Fatalf("baseline not carried: %q %v", data, err)
	}
	if body, _ := os.ReadFile(filepath.Join(root, ".gitignore")); string(body) != "!/.dotfiles/sync/\n" {
		t.Fatalf(".gitignore = %q", body)
	}
	if err := LegacyStoreError(root); err != nil {
		t.Fatalf("still refused after the remedy: %v", err)
	}
}
