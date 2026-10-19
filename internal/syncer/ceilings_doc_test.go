package syncer

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCeilingsDoc(t *testing.T) {
	docPath := filepath.Join("..", "..", "docs", "CEILINGS.md")
	data, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("read %s: %v", docPath, err)
	}
	doc := string(data)

	// Pin paths and marker comments, not line numbers: unrelated edits above a
	// named site must not make this useful drift check noisy.
	requiredCeilings := []string{
		"## Process-wide manifest serialization",
		"## Guard hooks are not a sandbox",
		"## Peer first-contact trust on first use",
		"## Whole-file manifest writes",
		"## Preview cannot prove an openrsync transfer",
	}
	if len(requiredCeilings) != 5 {
		t.Fatalf("required ceiling count = %d, want five", len(requiredCeilings))
	}
	for _, heading := range requiredCeilings {
		if !strings.Contains(doc, heading) {
			t.Errorf("required ceiling %q missing from %s", heading, docPath)
		}
	}
	if !strings.Contains(doc, "## Newer state schema field shapes") {
		t.Error("phase-minted schema ceiling missing from docs/CEILINGS.md")
	}
	// BUG-26 routes its cleanup to a documented ceiling. The marker at
	// internal/syncer/sync_cmd_ops.go promised this section and it was never
	// written, so the requirement pointed at nothing.
	if !strings.Contains(doc, "## Stale per-profile scheduler units") {
		t.Error("stale per-profile scheduler ceiling missing from docs/CEILINGS.md")
	}
	if !strings.Contains(doc, "## host_merge read-merge-write race") {
		t.Error("host_merge race ceiling (#181) missing from docs/CEILINGS.md")
	}
	if !strings.Contains(doc, "## Rescue reads the newest 50 upstream commits") || !strings.Contains(doc, "internal/syncer/gitrescue.go") {
		t.Error("rescue chain ceiling (#189) missing from docs/CEILINGS.md")
	}
	if !strings.Contains(doc, "## Rewrite twins by author, date and subject") {
		t.Error("rewrite twin ceiling (#216, #221) missing from docs/CEILINGS.md")
	}
	if !strings.Contains(doc, "## Owner aliases outside the coordinator's peer run") {
		t.Error("owner alias ceiling (#185) missing from docs/CEILINGS.md")
	}
	if !strings.Contains(doc, "## Unknown config keys, top level only") {
		t.Error("unknown config keys ceiling (#196) missing from docs/CEILINGS.md")
	}
	if !strings.Contains(doc, "## Container translation table") || !strings.Contains(doc, "## Container DNS injection") {
		t.Error("container ceilings (#223) missing from docs/CEILINGS.md")
	}
	if !strings.Contains(doc, "## Tracked-home directory mutation window") {
		t.Error("tracked-home directory mutation ceiling (#249) missing from docs/CEILINGS.md")
	}

	for _, path := range []string{
		"internal/syncer/manifest.go",
		"internal/cli/guard_cmd.go",
		"internal/guard/careful.go",
		"internal/syncer/rsyncbin.go",
		"internal/syncer/peer_commands.go",
		"internal/config/state.go",
		"internal/syncer/sync_cmd_ops.go",
		"internal/admission/uncovered.go",
	} {
		if !strings.Contains(doc, path) {
			t.Errorf("document does not name %q", path)
		}
		if _, err := os.Stat(filepath.Join("..", "..", path)); err != nil {
			t.Errorf("documented path %q is unavailable: %v", path, err)
		}
	}

	for _, want := range []struct {
		path   string
		marker string
	}{
		{"internal/syncer/manifest.go", "ponytail: known ceiling. See docs/CEILINGS.md (manifest serialization)."},
		{"internal/syncer/rsyncbin.go", "ponytail: known ceiling. See docs/CEILINGS.md (preview protocol limit)."},
		{"internal/syncer/rsyncbin.go", "ponytail: known ceiling. See docs/CEILINGS.md (first-contact trust)."},
		{"internal/syncer/sync_cmd_ops.go", "See docs/CEILINGS.md (stale per-profile scheduler units)"},
		{"internal/syncer/peer_host_merge.go", "ponytail: known ceiling. See docs/CEILINGS.md (host_merge read-merge-write race)."},
		{"internal/syncer/sync_store_ops.go", "ponytail: known ceiling. See docs/CEILINGS.md (owner aliases outside the coordinator's peer run)."},
		{"internal/syncer/local_store.go", "ponytail: known ceiling. See docs/CEILINGS.md (unknown config keys, top level only)."},
		{"internal/syncer/gitrescue.go", "ponytail: known ceiling. See docs/CEILINGS.md (rewrite twins by author, date and subject)."},
		{"internal/container/translate.go", "ponytail: known ceiling. See docs/CEILINGS.md (container translation table)."},
		{"internal/container/host.go", "ponytail: known ceiling. See docs/CEILINGS.md (container DNS injection)."},
		{"internal/admission/uncovered.go", "ponytail: known ceiling. See docs/CEILINGS.md (cwd as the owner of busy unshaped work)."},
	} {
		data, err := os.ReadFile(filepath.Join("..", "..", want.path))
		if err != nil {
			t.Errorf("read marker file %q: %v", want.path, err)
			continue
		}
		if !strings.Contains(string(data), want.marker) {
			t.Errorf("documented marker %q missing from %s", want.marker, want.path)
		}
	}
}

// TestCeilingMarkersNameTheDocumentCorrectly sweeps every `ponytail: known
// ceiling` marker in the tree rather than the three the table above pins.
//
// The pinned list is an allowlist that has to be hand-extended, which is
// exactly how the sync_cmd_ops.go marker came to say `docs/ceilings.md`: it
// was never added, so neither its casing nor its missing section was reachable
// by a test. Lowercase resolves only on a case-insensitive filesystem, so that
// pointer was dead on Linux while passing CI on macOS.
//
// Markers that reference no document are left alone. A `ponytail: known
// ceiling` comment is a local annotation first; only some are promoted to
// docs/CEILINGS.md, and forcing every one of them into the document would
// grow it into a list of notes rather than accepted ceilings.
func TestCeilingMarkersNameTheDocumentCorrectly(t *testing.T) {
	root := filepath.Join("..", "..")
	const canonical = "docs/CEILINGS.md"

	swept := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == "vendor" || name == "graphify-out" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// This file necessarily carries the lowercase spelling: it is the string
		// the sweep matches on. Skipping it keeps the check from reporting its
		// own machinery, which would train the reader to ignore the output.
		if filepath.Base(path) == "ceilings_doc_test.go" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(data), "\n") {
			lower := strings.ToLower(line)
			if !strings.Contains(lower, "docs/ceilings.md") {
				continue
			}
			swept++
			if !strings.Contains(line, canonical) {
				rel, _ := filepath.Rel(root, path)
				t.Errorf("%s:%d names the ceilings document with the wrong casing\n  line: %s\n  want the exact string %q: a lowercase path resolves only on a case-insensitive filesystem, so this reference is dead on Linux",
					rel, i+1, strings.TrimSpace(line), canonical)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("sweeping for ceiling markers: %v", err)
	}
	// Non-vacuity: the sweep must actually have found references. A walk that
	// silently matched nothing would pass forever.
	if swept < 4 {
		t.Errorf("swept only %d reference(s) to the ceilings document, want at least the four pinned markers - the walk is not reaching the tree", swept)
	}
}
