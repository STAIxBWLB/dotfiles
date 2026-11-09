package fileutil

import (
	"path/filepath"
	"strings"
)

// SameDirectory reports whether two paths name the same directory, following
// symlinks on both sides: a cloud folder is routinely reachable under a
// symlinked spelling (~/Dropbox -> ~/Library/CloudStorage/Dropbox), and a
// textual compare would read one tree as two. A path that does not resolve (it
// may not exist yet) makes the comparison textual, which leans towards
// reporting a difference rather than guessing that two paths are the same.
func SameDirectory(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	ca, cb := filepath.Clean(a), filepath.Clean(b)
	ra, errA := filepath.EvalSymlinks(ca)
	rb, errB := filepath.EvalSymlinks(cb)
	if errA == nil && errB == nil {
		return ra == rb
	}
	return ca == cb
}
