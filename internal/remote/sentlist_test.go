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

// The delete pass takes one list, and a first run has two reasons to name a path:
// the mirror holds it and this checkout neither lists nor has it, or git still
// lists it and it is gone from disk.
func TestNulUnionMergesBothKindsOfDeletion(t *testing.T) {
	if got := nulUnion(nil, nil, []byte{}); got != nil {
		t.Errorf("nulUnion of nothing = %q, want nothing", got)
	}
	got := nulUnion([]byte("stale.txt\x00docs/old.md\x00"), []byte("main.go\x00docs/old.md\x00"))
	if want := []string{"docs/old.md", "main.go", "stale.txt"}; !slices.Equal(nulList(got), want) {
		t.Errorf("nulUnion = %v, want %v", nulList(got), want)
	}
	// One list is returned as it stands, which is what the no-candidate case gets.
	if got := nulUnion(nil, []byte("gone.go\x00")); string(got) != "gone.go\x00" {
		t.Errorf("nulUnion = %q, want the one list", got)
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

	if record, err := deps.readSent(host, "one", "/local/one"); err != nil || record.Found || record.Stale {
		t.Fatalf("readSent on a fresh state = (%+v, %v), want no record and no error", record, err)
	}
	if err := deps.writeSent(host, "one", "/local/one", []byte("a.go\x00b.go\x00")); err != nil {
		t.Fatal(err)
	}
	record, err := deps.readSent(host, "one", "/local/one")
	if err != nil || !record.Found {
		t.Fatalf("readSent = (%+v, %v), want the record back", record, err)
	}
	if string(record.Listing) != "a.go\x00b.go\x00" {
		t.Errorf("record = %q, want the listing as sent", record.Listing)
	}
	if record, err := deps.readSent(host, "two", "/local/two"); err != nil || record.Found || record.Stale {
		t.Errorf("another mirror read this record: %+v, %v", record, err)
	}
	// The record is written aside and renamed, so nothing is left beside it.
	entries, err := os.ReadDir(deps.SentRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		var names []string
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Errorf("state directory holds %v, want only the record", names)
	}
}

// The name a config file gives a host is not the identity of the mirror behind
// it: the same host name and relative path can point at another root, another
// ssh destination or another local checkout after one edit. A record from before
// that edit must not be used, because its paths would be deleted from a mirror
// this machine never fed.
func TestASentRecordThatDescribesAnotherMirrorIsNotUsed(t *testing.T) {
	deps := Deps{SentRoot: t.TempDir()}
	host := testHost()
	checkout := "/local/one"
	if err := deps.writeSent(host, "one", checkout, []byte("build/app.o\x00")); err != nil {
		t.Fatal(err)
	}

	moved := host
	moved.Root = "dot-remote2"
	reached := host
	reached.SSH = "buildbox.example"
	for name, tc := range map[string]struct {
		host     Host
		rel      string
		checkout string
	}{
		"another root":        {host: moved, rel: "one", checkout: checkout},
		"another destination": {host: reached, rel: "one", checkout: checkout},
		"another checkout":    {host: host, rel: "one", checkout: "/local/two"},
	} {
		record, err := deps.readSent(tc.host, tc.rel, tc.checkout)
		if err != nil {
			t.Errorf("%s: readSent: %v", name, err)
			continue
		}
		if record.Found {
			t.Errorf("%s: the record was used for a mirror it does not describe", name)
		}
		if !record.Stale {
			t.Errorf("%s: the record did not read as describing another mirror", name)
		}
	}
	// Another mirror path is another record file, so it is no record at all
	// rather than one about this mirror.
	if record, err := deps.readSent(host, "two", checkout); err != nil || record.Found || record.Stale {
		t.Errorf("the record of another mirror path = (%+v, %v), want no record", record, err)
	}
	// The record it does describe is still there.
	if record, err := deps.readSent(host, "one", checkout); err != nil || !record.Found {
		t.Errorf("the original mirror lost its record: %+v, %v", record, err)
	}
}

// A record this code cannot read is not one it may act on either.
func TestAnUnreadableSentRecordReadsAsAnotherMirror(t *testing.T) {
	deps := Deps{SentRoot: t.TempDir()}
	host := testHost()
	path := deps.sentPath(host, "one")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{"", "a.go\x00b.go\x00", "dot-remote-sent 9 deadbeef\nmain.go\x00"} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		record, err := deps.readSent(host, "one", "/local/one")
		if err != nil {
			t.Fatalf("readSent(%q): %v", content, err)
		}
		if record.Found || !record.Stale {
			t.Errorf("readSent(%q) = %+v, want a record that describes another mirror", content, record)
		}
	}
}

// Without a state directory there is nowhere to keep the record, and the run
// says so instead of syncing without one.
func TestSentRecordWithoutAStateDirectoryIsAnError(t *testing.T) {
	deps := Deps{}
	if err := deps.writeSent(testHost(), "one", "/local/one", []byte("a.go\x00")); err == nil {
		t.Error("writeSent with no state directory succeeded")
	}
	if record, err := deps.readSent(testHost(), "one", "/local/one"); err == nil || record.Found {
		t.Errorf("readSent with no state directory = (%+v, %v), want an error", record, err)
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
