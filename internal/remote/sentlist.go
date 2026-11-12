package remote

import (
	"crypto/sha256"
	"encoding/hex"
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
//
// A record also names the mirror it describes. The name a config file gives a
// host is not that name: the same host name and relative path can point at a
// different root, a different ssh destination or a different local checkout
// after one edit, and a record from before that edit is a deletion list against
// a mirror this machine never fed.

// sentHeader opens every record. The identity that follows it is checked before
// any path in the record is used, so a record describing another mirror is
// recognized rather than acted on.
const sentHeader = "dot-remote-sent 1 "

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
// listings describe different mirrors. What the record is a record *of* is the
// identity inside it, not this name.
func (d Deps) sentPath(h Host, rel string) string {
	root := d.sentRoot()
	if root == "" {
		return ""
	}
	return filepath.Join(root, fileNameSafe(h.Name)+"--"+MirrorKey(rel)+".sent")
}

// sentIdentity is the mirror a record describes: where the host is reached, the
// remote root, the mirror's relative path and the local checkout. Hashing the
// four keeps the header one word a person can also compare by eye when two
// records are in question.
func sentIdentity(h Host, rel, checkout string) string {
	sum := sha256.New()
	for _, field := range []string{h.SSH, h.Root, rel, filepath.ToSlash(checkout)} {
		sum.Write([]byte(field))
		sum.Write([]byte{0})
	}
	return hex.EncodeToString(sum.Sum(nil))
}

// sentRecord is what one look at the state directory found for a mirror.
type sentRecord struct {
	// Listing is what this machine sent to this mirror, NUL-separated.
	Listing []byte
	// Found says the record describes this mirror, and may be subtracted from.
	Found bool
	// Stale says a record exists but does not describe this mirror - another
	// root or checkout, or a file this code cannot read - so it must not drive a
	// delete.
	Stale bool
}

// readSent answers what this machine last sent to one mirror. A mirror this
// machine has never sent to has no record, and a checkout with no files at all
// is a record with nothing in it, so Found stays true there.
func (d Deps) readSent(h Host, rel, checkout string) (sentRecord, error) {
	sentPath := d.sentPath(h, rel)
	if sentPath == "" {
		return sentRecord{}, fmt.Errorf("no state directory to keep the sent listing in")
	}
	data, err := os.ReadFile(sentPath)
	if err != nil {
		if os.IsNotExist(err) {
			return sentRecord{}, nil
		}
		return sentRecord{}, err
	}
	header, listing, ok := strings.Cut(string(data), "\n")
	if !ok || !strings.HasPrefix(header, sentHeader) {
		return sentRecord{Stale: true}, nil
	}
	if strings.TrimPrefix(header, sentHeader) != sentIdentity(h, rel, checkout) {
		return sentRecord{Stale: true}, nil
	}
	return sentRecord{Listing: []byte(listing), Found: true}, nil
}

// writeSent records what this run sent, for the next run of the same mirror to
// subtract.
func (d Deps) writeSent(h Host, rel, checkout string, listing []byte) error {
	sentPath := d.sentPath(h, rel)
	if sentPath == "" {
		return fmt.Errorf("no state directory to keep the sent listing in")
	}
	dir := filepath.Dir(sentPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	content := append([]byte(sentHeader+sentIdentity(h, rel, checkout)+"\n"), listing...)
	// Written aside and renamed: a run that dies mid-write leaves the previous
	// record whole instead of a truncated deletion list.
	tmp, err := os.CreateTemp(dir, filepath.Base(sentPath)+".*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	remove := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		remove()
		return err
	}
	if err := tmp.Close(); err != nil {
		remove()
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		remove()
		return err
	}
	if err := os.Rename(tmpName, sentPath); err != nil {
		remove()
		return err
	}
	return nil
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
		for rel := range listedPaths(listing) {
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

// listedPaths reads a listing as a set of paths under the checkout, dropping
// anything that is not one.
func listedPaths(listing []byte) map[string]bool {
	set := map[string]bool{}
	for _, rel := range strings.Split(string(listing), "\x00") {
		if safeRelPath(rel) {
			set[rel] = true
		}
	}
	return set
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
