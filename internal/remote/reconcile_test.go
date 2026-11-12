package remote

import (
	"slices"
	"strings"
	"testing"
)

// A host's find prints paths with a "./" prefix, and nothing else here may take
// something that is not a path under the mirror.
func TestMirrorPathsReadsFindsOutput(t *testing.T) {
	got := mirrorPaths([]byte("./b.go\x00./docs/a.md\x00.\x00\x00./../escape.go\x00./b.go\x00"))
	if want := []string{"b.go", "docs/a.md"}; !slices.Equal(got, want) {
		t.Errorf("mirrorPaths = %v, want %v", got, want)
	}
}

// The candidates of an adopted mirror are the files this checkout neither lists
// nor has. A file that is still here is left alone however git judges it: naming
// it on the delete pass would send it.
func TestMirrorCandidatesAreWhatTheCheckoutNeitherListsNorHas(t *testing.T) {
	src := t.TempDir()
	writeTree(t, src, "here/kept.go", "package main\n")
	writeTree(t, src, "here/unlisted.go", "ignored but still here\n")

	mirror := []byte("./here/kept.go\x00./here/unlisted.go\x00./here/gone.go\x00./docs/stale.md\x00./docs/a.md\x00")
	listing := []byte("here/kept.go\x00docs/a.md\x00")

	got := mirrorCandidates(src, mirror, listing)
	if want := []string{"docs/stale.md", "here/gone.go"}; !slices.Equal(got, want) {
		t.Errorf("mirrorCandidates = %v, want %v", got, want)
	}
}

// The deletion set drops what git's ignore rules cover, which is how a build
// output the host made survives an adopted mirror being cleaned.
func TestDeletionSetKeepsWhatGitIgnores(t *testing.T) {
	candidates := []string{"Cargo.lock", "build/app.o", "docs/stale.md", "node_modules/pkg/index.js"}
	ignored := map[string]bool{"build/app.o": true, "node_modules/pkg/index.js": true}

	if got := nulList(deletionSet(candidates, ignored)); !slices.Equal(got, []string{"Cargo.lock", "docs/stale.md"}) {
		t.Errorf("deletionSet = %v, want the two git does not ignore", got)
	}
	if got := deletionSet(candidates, map[string]bool{"Cargo.lock": true, "build/app.o": true, "docs/stale.md": true, "node_modules/pkg/index.js": true}); got != nil {
		t.Errorf("deletionSet = %q, want nothing to delete when git ignores it all", got)
	}
}

// The listing command prunes the directories that only hold build products, so a
// node_modules or target tree is never walked file by file, and it survives a
// mirror path the shell would otherwise parse.
func TestMirrorFindCommandPrunesAndQuotes(t *testing.T) {
	command := mirrorFindCommand("dot-remote/work/dev/one; $(echo hi)")
	// $HOME stays for the host and the mirror path is quoted for it, with its own
	// `$(` escaped, so nothing expands here.
	if want := `cd "$HOME"/"dot-remote/work/dev/one; \$(echo hi)" 2>/dev/null || exit 0; find . `; !strings.HasPrefix(command, want) {
		t.Errorf("find command starts with %q, want %q", command, want)
	}
	for _, want := range []string{
		"-name 'node_modules' -o",
		"-name 'target' -o",
		"-name '.git' -o",
		`\( `,
		` \) -prune -o -type f -print0`,
	} {
		if !strings.Contains(command, want) {
			t.Errorf("find command is missing %q:\n%s", want, command)
		}
	}
	// A pattern with a glob or a slash cannot be pruned by name, so it is left to
	// git's own answer instead of narrowing the walk.
	for _, name := range []string{".env.*", "*.pem", ".claude/worktrees"} {
		if prunableName.MatchString(name) {
			t.Errorf("prunableName(%q) = true, but find cannot prune it by name", name)
		}
	}
	if !prunableName.MatchString("node_modules") {
		t.Error("prunableName refused a plain directory name")
	}
}

func TestGitIgnoreArgsAskForTheRulesOnly(t *testing.T) {
	want := []string{"check-ignore", "--no-index", "-z", "--stdin"}
	if got := gitIgnoreArgs(); !slices.Equal(got, want) {
		t.Errorf("gitIgnoreArgs = %v, want %v", got, want)
	}
}
