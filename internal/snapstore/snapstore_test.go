package snapstore

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// fixedTime is the timestamp every expected id in this file derives from, so
// the expectations can be literal strings instead of restatements of the code
// under test. TestNewVersion pins the two to each other.
var fixedTime = time.Date(2026, 8, 24, 12, 30, 0, 0, time.UTC)

const fixedVersion = "20260824T123000Z"

func quietRunner(dryRun bool) *exec.Runner {
	return exec.NewRunner(dryRun, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// mustMkdirAll creates a snapshot directory, since "this version exists" is an
// os.Stat result and every collision decision in this package is that result.
func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", path, err)
	}
}

// TestNewVersion keeps fixedVersion honest: every other test in this file
// asserts against literal ids derived from it, so a format change must break
// here rather than silently rewrite what those literals mean.
func TestNewVersion(t *testing.T) {
	if got := NewVersion(fixedTime); got != fixedVersion {
		t.Errorf("NewVersion = %q, want %q", got, fixedVersion)
	}
	// Non-UTC input must still produce the UTC instant, so ids sort by real time.
	if got := NewVersion(fixedTime.In(time.FixedZone("KST", 9*60*60))); got != fixedVersion {
		t.Errorf("NewVersion of a non-UTC time = %q, want %q", got, fixedVersion)
	}
}

// TestUniqueVersion pins the collision behavior a snapshot write depends on: a
// returned id must name a directory that does not already exist, because the
// caller creates a snapshot under it and a collision writes into somebody
// else's backup. Each row asserts the exact id AND that the id is free, so a
// UniqueVersion that returned the base unconditionally fails every row but the
// first.
func TestUniqueVersion(t *testing.T) {
	tests := []struct {
		name     string
		existing []string
		want     string
	}{
		{
			name: "free base returns the base id unchanged",
			want: fixedVersion,
		},
		{
			name:     "taken base returns the first free suffixed id",
			existing: []string{fixedVersion},
			want:     fixedVersion + "-2",
		},
		{
			name:     "a run of taken ids returns the first gap after them",
			existing: []string{fixedVersion, fixedVersion + "-2", fixedVersion + "-3"},
			want:     fixedVersion + "-4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for _, v := range tt.existing {
				mustMkdirAll(t, filepath.Join(root, v))
			}
			versionPath := func(v string) string { return filepath.Join(root, v) }

			got, err := UniqueVersion(fixedTime, versionPath)

			if err != nil {
				t.Fatalf("UniqueVersion error = %v, want nil", err)
			}
			if got != tt.want {
				t.Errorf("UniqueVersion = %q, want %q", got, tt.want)
			}
			if _, serr := os.Stat(versionPath(got)); serr == nil {
				t.Errorf("UniqueVersion returned %q, which already exists on disk", got)
			}
		})
	}
}

// TestUniqueVersion_Exhausted pins the loop bound by COUNTING the probes rather
// than by creating ninety-eight directories: versionPath records each candidate
// and points every one of them at a single directory that does exist, so every
// probe reports "taken" and the loop runs to its end. Counted from the source:
// the loop is `for i := 2; i < 100` and the suffix is applied at the END of the
// body, so the candidates actually stat'd are base, base-2 ... base-98 — 98 of
// them — and base-99 is assigned on the last iteration but never probed.
func TestUniqueVersion_Exhausted(t *testing.T) {
	root := t.TempDir()
	taken := filepath.Join(root, "taken")
	mustMkdirAll(t, taken)

	var probed []string
	got, err := UniqueVersion(fixedTime, func(v string) string {
		probed = append(probed, v)
		return taken
	})

	if err == nil {
		t.Fatalf("UniqueVersion error = nil, want an error; id = %q", got)
	}
	// An empty id matters on its own: a caller that ignores the error must not
	// receive something that looks like a usable version.
	if got != "" {
		t.Errorf("UniqueVersion id = %q, want %q so an ignored error cannot yield a usable path", got, "")
	}
	if !strings.Contains(err.Error(), fixedVersion) {
		t.Errorf("UniqueVersion error = %q, want it to name the base id %q", err, fixedVersion)
	}
	if len(probed) != 98 {
		t.Errorf("UniqueVersion probed %d candidates, want 98", len(probed))
	}
	if len(probed) > 0 && probed[0] != fixedVersion {
		t.Errorf("first probe = %q, want the unsuffixed base %q", probed[0], fixedVersion)
	}
	if last := probed[len(probed)-1]; last != fixedVersion+"-98" {
		t.Errorf("last probe = %q, want %q", last, fixedVersion+"-98")
	}
}

// TestUniqueVersion_UnprobableCandidateIsTreatedAsTaken pins the non-missing
// stat arm: a candidate whose parent denies traversal cannot be proven unused,
// so it must count as taken rather than be handed back as free.
func TestUniqueVersion_UnprobableCandidateIsTreatedAsTaken(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root traverses a 0000 directory, so the permission arm cannot be provoked here")
	}
	root := t.TempDir()
	blocked := filepath.Join(root, "blocked")
	mustMkdirAll(t, blocked)
	if err := os.Chmod(blocked, 0o000); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o755) })

	got, err := UniqueVersion(fixedTime, func(v string) string { return filepath.Join(blocked, v) })

	if err == nil {
		t.Fatalf("UniqueVersion error = nil, want an error; id = %q", got)
	}
	if got != "" {
		t.Errorf("UniqueVersion id = %q, want %q: an unreadable candidate is not a free one", got, "")
	}
}

// TestResolveLatest pins which snapshot a restore reads. The pointer wins only
// when it names a version that is still on disk; all three fallback triggers
// (no pointer, empty pointer, stale pointer) must reach the newest listed
// snapshot instead. The honored row asserts the lister was never called, so a
// ResolveLatest that ignored the pointer and always listed cannot pass it.
func TestResolveLatest(t *testing.T) {
	listBoom := errors.New("list exploded")

	tests := []struct {
		name       string
		pointer    string
		noPointer  bool
		onDisk     []string
		listed     []string
		listErr    error
		want       string
		wantErrIs  error
		wantErrHas []string
		wantCalls  int
	}{
		{
			name:      "pointer naming a live version is honored without listing",
			pointer:   "v3\n",
			onDisk:    []string{"v3", "v2", "v1"},
			listed:    []string{"v9"},
			want:      "v3",
			wantCalls: 0,
		},
		{
			name:      "missing pointer falls back to the newest listed",
			noPointer: true,
			onDisk:    []string{"v2", "v1"},
			listed:    []string{"v2", "v1"},
			want:      "v2",
			wantCalls: 1,
		},
		{
			name:      "whitespace-only pointer falls back to the newest listed",
			pointer:   "  \n\t ",
			onDisk:    []string{"v2", "v1"},
			listed:    []string{"v2", "v1"},
			want:      "v2",
			wantCalls: 1,
		},
		{
			name:      "stale pointer falls back rather than returning the dangling name",
			pointer:   "v9",
			onDisk:    []string{"v2", "v1"},
			listed:    []string{"v2", "v1"},
			want:      "v2",
			wantCalls: 1,
		},
		{
			name:       "no snapshots is an error naming the host root",
			noPointer:  true,
			listed:     nil,
			wantErrHas: []string{"no snapshots under"},
			wantCalls:  1,
		},
		{
			name:      "lister error is propagated unchanged",
			noPointer: true,
			listErr:   listBoom,
			wantErrIs: listBoom,
			wantCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			pointerPath := filepath.Join(root, "latest")
			if !tt.noPointer {
				if err := os.WriteFile(pointerPath, []byte(tt.pointer), 0o644); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
			}
			for _, v := range tt.onDisk {
				mustMkdirAll(t, filepath.Join(root, v))
			}
			calls := 0
			versions := func() ([]string, error) {
				calls++
				return tt.listed, tt.listErr
			}

			got, err := ResolveLatest(pointerPath, root, func(v string) string { return filepath.Join(root, v) }, versions)

			wantErr := tt.wantErrIs != nil || len(tt.wantErrHas) > 0
			if wantErr && err == nil {
				t.Fatalf("ResolveLatest error = nil, want an error; version = %q", got)
			}
			if !wantErr && err != nil {
				t.Fatalf("ResolveLatest error = %v, want nil", err)
			}
			if tt.wantErrIs != nil && !errors.Is(err, tt.wantErrIs) {
				t.Errorf("ResolveLatest error = %v, want it to wrap %v", err, tt.wantErrIs)
			}
			for _, want := range tt.wantErrHas {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("ResolveLatest error = %q, want it to contain %q", err, want)
				}
			}
			if wantErr && got != "" {
				t.Errorf("ResolveLatest version = %q alongside an error, want %q", got, "")
			}
			if got != tt.want {
				t.Errorf("ResolveLatest = %q, want %q", got, tt.want)
			}
			if calls != tt.wantCalls {
				t.Errorf("lister called %d times, want %d", calls, tt.wantCalls)
			}
		})
	}
}

// TestResolveLatest_PointerReadErrorIsPropagated pins the one non-fallback
// error arm. A pointer that is unreadable for a reason other than absence means
// the restore source is unknown, not that there is no pointer, so falling back
// would pick a snapshot the operator never asked for. The pointer path is a
// directory here, which makes os.ReadFile fail with something that is not
// os.IsNotExist on both Linux and darwin.
func TestResolveLatest_PointerReadErrorIsPropagated(t *testing.T) {
	root := t.TempDir()
	pointerPath := filepath.Join(root, "latest")
	mustMkdirAll(t, pointerPath)
	mustMkdirAll(t, filepath.Join(root, "v1"))

	called := false
	got, err := ResolveLatest(pointerPath, root,
		func(v string) string { return filepath.Join(root, v) },
		func() ([]string, error) { called = true; return []string{"v1"}, nil })

	if err == nil {
		t.Fatalf("ResolveLatest error = nil, want the pointer read error; version = %q", got)
	}
	if got != "" {
		t.Errorf("ResolveLatest = %q, want %q", got, "")
	}
	if called {
		t.Error("lister was consulted; a pointer read error that is not a missing file must propagate rather than fall back")
	}
}

// TestPrune pins which snapshots survive a prune. The latest row is the
// load-bearing one: the snapshot a restore would read must never be pruned,
// even when it falls outside the keep window. The runner is a dry-run runner,
// so a regression here is measured against the removal list rather than against
// deleted fixtures.
func TestPrune(t *testing.T) {
	snaps := []SnapshotInfo{
		{Version: "v3", Path: "/snap/v3"},
		{Version: "v2", Path: "/snap/v2"},
		{Version: "v1", Path: "/snap/v1"},
	}

	tests := []struct {
		name   string
		keep   int
		latest string
		want   []string
	}{
		{name: "keep covers every snapshot", keep: 3, want: nil},
		{name: "keep above the count removes nothing", keep: 10, want: nil},
		{name: "keep below one is clamped to one", keep: 0, want: []string{"v2", "v1"}},
		{name: "the latest snapshot survives past the keep window", keep: 1, latest: "v1", want: []string{"v2"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Prune(quietRunner(true), tt.keep, snaps, tt.latest)
			if err != nil {
				t.Fatalf("Prune error = %v, want nil", err)
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("Prune removed %v, want %v", got, tt.want)
			}
		})
	}
}

// TestPreRestoreDir pins the pre-restore backup path. A collision here would
// overwrite the copy of the live home taken just before a restore, which is the
// only copy of what the restore is about to replace.
func TestPreRestoreDir(t *testing.T) {
	home := t.TempDir()
	base := filepath.Join(home, ".local", "share", "dotfiles", "backup", "pre", fixedVersion)

	if got := PreRestoreDir(home, []string{"pre"}, fixedTime); got != base {
		t.Fatalf("PreRestoreDir = %q, want %q", got, base)
	}

	mustMkdirAll(t, base)
	if got := PreRestoreDir(home, []string{"pre"}, fixedTime); got != base+"-2" {
		t.Errorf("PreRestoreDir with the base taken = %q, want %q", got, base+"-2")
	}
}
