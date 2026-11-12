package remote

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestGoneFromDiskNamesOnlyPathsMissingOnDisk(t *testing.T) {
	src := t.TempDir()
	writeTree(t, src, "kept.go", "package main\n")
	writeTree(t, src, "docs/still.md", "# still\n")

	// previous is what this machine sent, current what git lists now, and
	// cached-only.go is a file `git rm --cached` took out of the listing while
	// leaving it on disk - a build output the host may also have.
	previous := []byte("kept.go\x00docs/still.md\x00cached-only.go\x00")
	current := []byte("kept.go\x00fresh.go\x00")
	writeTree(t, src, "cached-only.go", "build output\n")

	got := nulList(goneFromDisk(src, previous, current))
	if want := []string{"fresh.go"}; !slices.Equal(got, want) {
		t.Errorf("goneFromDisk = %v, want %v: a path still on disk is not deleted, listed or not", got, want)
	}
}

func TestGoneFromDiskIsSortedAndEmptyWhenNothingLeft(t *testing.T) {
	src := t.TempDir()
	writeTree(t, src, "b.go", "package main\n")
	writeTree(t, src, "a.go", "package main\n")
	if got := goneFromDisk(src, []byte("b.go\x00a.go\x00"), []byte("b.go\x00a.go\x00")); got != nil {
		t.Errorf("goneFromDisk = %q, want nothing to delete", got)
	}
	sorted := nulList(goneFromDisk(src, []byte("b.go\x00a.go\x00z.go\x00y.go\x00")))
	if want := []string{"y.go", "z.go"}; !slices.Equal(sorted, want) {
		t.Errorf("goneFromDisk = %v, want %v in path order", sorted, want)
	}
}

// A listing is git's own output, but a delete pass still only names a path that
// stays under the checkout: rsync would resolve anything else against the
// source it was given.
func TestGoneFromDiskDropsAPathThatLeavesTheCheckout(t *testing.T) {
	src := t.TempDir()
	writeTree(t, src, "ok.go", "package main\n")
	got := nulList(goneFromDisk(src, []byte("../escape.go\x00/etc/passwd\x00./ok.go\x00dir/gone.go\x00")))
	if want := []string{"dir/gone.go"}; !slices.Equal(got, want) {
		t.Errorf("goneFromDisk = %v, want %v: only a path under the checkout", got, want)
	}
	for _, rel := range []string{"../escape.go", "/etc/passwd", "./ok.go", ".", ""} {
		if safeRelPath(rel) {
			t.Errorf("safeRelPath(%q) = true", rel)
		}
	}
	if !safeRelPath("docs/still.md") {
		t.Error("safeRelPath refused a path git emits")
	}
}

// One record per mirror and per host: one checkout can be mirrored to two hosts,
// and two checkouts can share a host.
func TestSentRecordIsPerMirrorAndPerHost(t *testing.T) {
	deps := Deps{SentRoot: t.TempDir()}
	host := testHost()
	other := host
	other.Name = "gpu box"

	one := deps.sentPath(host, "one")
	for _, different := range []string{deps.sentPath(host, "two"), deps.sentPath(other, "one")} {
		if one == different {
			t.Errorf("two records share the path %s", one)
		}
	}
	// A host name is a config key and may hold a space, so the record's name has
	// to stay one path element.
	if name := filepath.Base(deps.sentPath(other, "one")); strings.ContainsAny(name, " /") {
		t.Errorf("record name %q is not one path element", name)
	}
	if got := fileNameSafe(""); got == "" || strings.ContainsAny(got, " /") {
		t.Errorf("fileNameSafe(\"\") = %q, want a usable name", got)
	}

	if _, ok, err := deps.readSent(host, "one"); ok || err != nil {
		t.Fatalf("readSent on a fresh state = (ok=%v, err=%v), want no record and no error", ok, err)
	}
	if err := deps.writeSent(host, "one", []byte("a.go\x00b.go\x00")); err != nil {
		t.Fatal(err)
	}
	data, ok, err := deps.readSent(host, "one")
	if err != nil || !ok {
		t.Fatalf("readSent = (ok=%v, err=%v), want the record back", ok, err)
	}
	if string(data) != "a.go\x00b.go\x00" {
		t.Errorf("record = %q, want the listing as sent", data)
	}
	if _, ok, err := deps.readSent(host, "two"); ok || err != nil {
		t.Errorf("another mirror read this record: ok=%v err=%v", ok, err)
	}
}

// Without a state directory there is nowhere to keep the record, and the run
// says so instead of syncing without one.
func TestSentRecordWithoutAStateDirectoryIsAnError(t *testing.T) {
	deps := Deps{}
	if err := deps.writeSent(testHost(), "one", []byte("a.go\x00")); err == nil {
		t.Error("writeSent with no state directory succeeded")
	}
	if _, ok, err := deps.readSent(testHost(), "one"); err == nil || ok {
		t.Errorf("readSent with no state directory = (ok=%v, err=%v), want an error", ok, err)
	}
}

func writeTree(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func nulList(data []byte) []string {
	var out []string
	for _, item := range strings.Split(string(data), "\x00") {
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}
