package remote

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// What a run sent to a mirror is the only record that can say which files left
// the checkout. git's listing says what the tree holds now, and the mirror's own
// contents cannot tell a deleted file from a build output the host made - which
// is why the delete pass used to guess from the ignore files, and why a
// negation in one of them broke it.
//
// So each run keeps its own listing here, beside the admission state, and the
// next run deletes exactly what is in it and gone from disk.
//
// It is kept on this machine rather than on the host deliberately: two machines
// may share one mirror, and a listing written by the other one must never make
// this machine delete files it never sent.

// sentRoot is the directory holding one file per mirror this machine has sent
// to. It sits beside the admission state root, whose parent is the dot state
// directory; Deps.SentRoot overrides it.
func (d Deps) sentRoot() string {
	if d.SentRoot != "" {
		return d.SentRoot
	}
	if d.Store != nil && d.Store.Root != "" {
		return filepath.Join(filepath.Dir(d.Store.Root), "remote-sent")
	}
	return ""
}

// sentPath is where one mirror's last listing lives: the host and the mirror key
// together, because one checkout can be mirrored to two hosts and the two
// listings describe different mirrors.
func (d Deps) sentPath(h Host, rel string) string {
	root := d.sentRoot()
	if root == "" {
		return ""
	}
	return filepath.Join(root, fileNameSafe(h.Name)+"--"+MirrorKey(rel)+".sent")
}

// readSent answers what this machine last sent to one mirror, and whether it has
// that record at all: a mirror this machine has never sent to has none, and a
// checkout with no files at all is a record with nothing in it.
func (d Deps) readSent(h Host, rel string) ([]byte, bool, error) {
	sentPath := d.sentPath(h, rel)
	if sentPath == "" {
		return nil, false, fmt.Errorf("no state directory to keep the sent listing in")
	}
	data, err := os.ReadFile(sentPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return data, true, nil
}

// writeSent records what this run sent, for the next run of the same mirror to
// subtract.
func (d Deps) writeSent(h Host, rel string, listing []byte) error {
	sentPath := d.sentPath(h, rel)
	if sentPath == "" {
		return fmt.Errorf("no state directory to keep the sent listing in")
	}
	if err := os.MkdirAll(filepath.Dir(sentPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(sentPath, listing, 0o644)
}

// goneFromDisk is what the delete pass removes: every path this machine sent
// before, or lists now, that its checkout no longer has on disk. NUL-separated,
// like the listing itself, and sorted so the argv a run prints is stable.
//
// A path that is merely unlisted is not deleted: `git rm --cached` on a build
// output leaves the file on this machine and takes it out of git's view, and
// deleting the mirror's copy would destroy what the host built. Only a path that
// is gone here is gone there.
func goneFromDisk(src string, listings ...[]byte) []byte {
	seen := map[string]bool{}
	for _, listing := range listings {
		for _, rel := range strings.Split(string(listing), "\x00") {
			if rel == "" || seen[rel] || !safeRelPath(rel) {
				continue
			}
			seen[rel] = true
		}
	}
	var gone []string
	for rel := range seen {
		if _, err := os.Lstat(filepath.Join(src, filepath.FromSlash(rel))); err != nil {
			gone = append(gone, rel)
		}
	}
	sort.Strings(gone)
	if len(gone) == 0 {
		return nil
	}
	return []byte(strings.Join(gone, "\x00") + "\x00")
}

// safeRelPath reports whether a listed path is one rsync can take as a
// --files-from argument under the checkout: relative, clean, and not escaping
// it. git does not emit anything else, and a path that does is dropped rather
// than handed to a delete.
func safeRelPath(rel string) bool {
	clean := path.Clean(rel)
	if clean != rel || clean == "." || clean == ".." || path.IsAbs(clean) {
		return false
	}
	// path.Clean keeps a leading "..", so "../escape.go" is already clean and
	// has to be refused here.
	return !strings.HasPrefix(clean, "../")
}

// fileNameSafe turns a host name into one path element. Host names are config
// keys and may hold anything; the mirror key beside it is already opaque.
func fileNameSafe(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "host"
	}
	return b.String()
}
