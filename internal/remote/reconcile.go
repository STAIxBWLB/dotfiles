package remote

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// A mirror this machine has no record of cannot be subtracted from a record, and
// it may already hold a tree another checkout built. The honest answer there is
// git's own: a file the mirror holds that this checkout neither lists nor has,
// and that git's ignore rules do not cover, is gone; an ignored path is a build
// output and stays.
//
// rsync's filter files cannot answer that question (see the note in
// sentlist.go), and git can: check-ignore reads the same rules git wrote, its
// negation included. So the first run of a mirror lists what the mirror holds
// over ssh, keeps the paths this checkout has on disk, asks git about the rest,
// and deletes what git does not claim.

// mirrorPaths reads what a host's `find` printed as relative paths, dropping
// the "./" prefix find writes and anything that is not a path under the mirror.
func mirrorPaths(found []byte) []string {
	seen := map[string]bool{}
	for _, rel := range strings.Split(string(found), "\x00") {
		rel = strings.TrimPrefix(rel, "./")
		if safeRelPath(rel) {
			seen[rel] = true
		}
	}
	paths := make([]string, 0, len(seen))
	for rel := range seen {
		paths = append(paths, rel)
	}
	slices.Sort(paths)
	return paths
}

// mirrorCandidates is what an adopted mirror holds that this checkout neither
// lists nor has on disk, in path order. A path this checkout still has is left
// alone whatever git says about it: deleting it would destroy a file that is
// here, and naming it on the delete pass would send it. A path git still lists
// but that is gone from disk is not a candidate either - the ship pass skips it
// with --ignore-missing-args, so it comes from goneFromDisk instead.
func mirrorCandidates(src string, mirror, listing []byte) []string {
	listed := listedPaths(listing)
	var candidates []string
	for _, rel := range mirrorPaths(mirror) {
		if listed[rel] {
			continue
		}
		if _, err := os.Lstat(filepath.Join(src, filepath.FromSlash(rel))); err == nil {
			continue
		}
		candidates = append(candidates, rel)
	}
	return candidates
}

// deletionSet is the delete pass's input: the candidates git's ignore rules do
// not cover, NUL-separated, or nil when nothing is left.
func deletionSet(candidates []string, ignored map[string]bool) []byte {
	var gone []string
	for _, rel := range candidates {
		if !ignored[rel] {
			gone = append(gone, rel)
		}
	}
	if len(gone) == 0 {
		return nil
	}
	slices.Sort(gone)
	return []byte(strings.Join(gone, "\x00") + "\x00")
}

// prunableName matches an exclude entry that names one directory outright: no
// slash, no glob. Those are the ones `find` can prune, so a node_modules or
// target tree on the mirror is never walked file by file.
var prunableName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// mirrorFindCommand lists every file one mirror holds, relative to the mirror
// and NUL-separated. A mirror that does not exist yet has nothing to list, and a
// directory that cannot be entered is left to the caller as an empty answer
// rather than an error: this list only ever decides what may be deleted.
//
// A symlink is listed as well as a file, and never followed: the ship pass sends
// a git-tracked symlink as a symlink, so one this checkout dropped is only
// visible here if `find` prints it, and `-type f` alone would leave it on an
// adopted mirror for good.
func mirrorFindCommand(mirror string) string {
	var prune []string
	for _, name := range builtinExcludes {
		if prunableName.MatchString(name) {
			prune = append(prune, "-name", ShellQuote(name), "-o")
		}
	}
	list := "find . \\( -type f -o -type l \\) -print0"
	if len(prune) > 0 {
		list = "find . \\( " + strings.TrimSuffix(strings.Join(prune, " "), " -o") + " \\) -prune -o \\( -type f -o -type l \\) -print0"
	}
	return `cd "$HOME"/` + quotedWord(mirror) + ` 2>/dev/null || exit 0; ` + list
}

// gitIgnoreArgs asks git which of the paths on stdin its ignore rules cover.
// --no-index judges a path by the rules rather than by the index, and -z keeps a
// path with a space intact.
func gitIgnoreArgs() []string {
	return []string{"check-ignore", "--no-index", "-z", "--stdin"}
}
