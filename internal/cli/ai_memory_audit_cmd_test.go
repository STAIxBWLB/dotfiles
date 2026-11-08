package cli

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// privateWord sits on a line that matches a marker; only --show may print it.
const privateWord = "zebra-42"

// goldenMemoryFixture is a synthetic home with all five memory stores (#367):
// a Claude index over its line limit with a broken link and an orphan, Codex
// ad-hoc notes, a Qwen store, a Grok index, and a fake jcode on PATH.
func goldenMemoryFixture(t *testing.T) (home, root string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	bin := filepath.Join(home, "bin")
	t.Setenv("PATH", bin)

	w := func(rel, body string) { writeCLITestFile(t, filepath.Join(home, rel), body) }
	w("bin/jcode", "#!/bin/sh\nprintf 'Memory Statistics:\\n  Project memories: 2\\n  Global memories:  1\\n  Total:            3\\n'\n")
	if err := os.Chmod(filepath.Join(bin, "jcode"), 0o755); err != nil {
		t.Fatal(err)
	}
	w(".config/dotfiles/agents/memory-markers.yaml", `markers:
  - pattern: old-plugin
    retired: 2026-10-01
    replacement: new-tool
    reason: the plugin was removed
  - pattern: 'old-repo(-v1)?'
    regex: true
    retired: 2026-09-01
    replacement: new-repo
    reason: the repository was renamed
`)

	claude := ".claude/projects/-proj-a/memory/"
	index := "- [A](a.md) - kept\n- [Gone](gone.md) - deleted file\n- [Spaced](my%20note.md)\n" + strings.Repeat("- filler\n", 198)
	w(claude+"MEMORY.md", index)
	w(claude+"a.md", "Use old-plugin for capture. "+privateWord+"\n")
	w(claude+"orphan.md", "Nothing links this.\n")
	w(claude+"my note.md", "Linked with an escaped space.\n")
	w(claude+"_archive/old.md", "old-plugin, archived already\n")

	codex := ".codex/memories/"
	w(codex+"memory_summary.md", "# Summary\nnote-one applies.\n")
	w(codex+"MEMORY.md", "# Task Group\nsee note-one\n")
	w(codex+"raw_memories.md", "raw\n")
	w(codex+"rollout_summaries/r1.md", "rollout\n")
	w(codex+"extensions/ad_hoc/notes/note-one.md", "old-plugin is current. "+privateWord+"\n")

	w(".qwen/memories/MEMORY.md", "- [F](feedback/f.md)\n")
	w(".qwen/memories/feedback/f.md", "Push to old-repo-v1.\n")

	w(".grok/memory-v2/global/MEMORY.md", "# Memory\nold-plugin\n")
	w(".grok/memory-v2/global/topics/t.md", "not read: grok topics are outside the index\n")

	stamp := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	_ = filepath.WalkDir(home, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			_ = os.Chtimes(path, stamp, stamp)
		}
		return nil
	})
	return home, t.TempDir()
}

// treeDigest records every directory and, per file, its mode, size, mtime and content.
func treeDigest(t *testing.T, root string) string {
	t.Helper()
	var lines []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.IsDir() { // a rename changes its parent's mtime
			lines = append(lines, path+"/")
			return nil
		}
		sum := ""
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum = fmt.Sprintf("%x", sha256.Sum256(data))
		}
		lines = append(lines, fmt.Sprintf("%s %v %d %d %s", path, info.Mode(), info.Size(), info.ModTime().UnixNano(), sum))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func wantExitOne(t *testing.T, err error) {
	t.Helper()
	var exitErr *ExitCodeError
	if !errors.As(err, &exitErr) || exitErr.Code != 1 {
		t.Fatalf("err = %v, want exit code 1", err)
	}
}

// AC2 and AC3: the audit writes nothing, and only --show prints memory text.
func TestMemoryAuditReadOnlyAndQuiet(t *testing.T) {
	home, _ := goldenMemoryFixture(t)
	before := treeDigest(t, home)
	for _, args := range [][]string{
		{"ai", "memory", "audit"},
		{"ai", "memory", "audit", "--json"},
		{"ai", "memory", "audit", "--show"},
		{"ai", "memory", "audit", "--json", "--show"},
	} {
		out, _, err := runDotForTest(args...)
		wantExitOne(t, err)
		show := strings.Contains(strings.Join(args, " "), "--show")
		if got := strings.Contains(out, privateWord); got != show {
			t.Errorf("%v: memory text printed = %v, want %v\n%s", args, got, show, out)
		}
	}
	if after := treeDigest(t, home); after != before {
		t.Fatalf("audit changed the home tree\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// AC1 in text form: limits, links, orphans and per-agent marker hits.
func TestMemoryAuditFindings(t *testing.T) {
	goldenMemoryFixture(t)
	out, _, err := runDotForTest("ai", "memory", "audit")
	wantExitOne(t, err)
	for _, want := range []string{
		"201 lines", "OVER LIMIT", "broken link: gone.md", "not in the index (1): orphan.md",
		`"old-plugin": 1 hit(s) in a.md`, `"old-plugin": 1 hit(s) in extensions/ad_hoc/notes/note-one.md`,
		`"old-repo(-v1)?": 1 hit(s) in feedback/f.md`, "jcode memory stats  3 files",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "_archive") || strings.Contains(out, "topics/t.md") {
		t.Errorf("archived files and Grok topic files must not be read:\n%s", out)
	}
}

// AC6: missing stores and a missing marker file are unavailable, not clean.
func TestMemoryAuditUnavailable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	t.Setenv("PATH", t.TempDir())
	out, _, err := runDotForTest("ai", "memory", "audit", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"markersStatus": "unavailable"`) || strings.Contains(out, `"status": "ok"`) {
		t.Fatalf("want every store and the markers unavailable:\n%s", out)
	}
	for _, agent := range []string{"claude", "codex", "qwen", "grok", "jcode"} {
		if !strings.Contains(out, `"agent": "`+agent+`"`) {
			t.Errorf("%s has no row", agent)
		}
	}
}

// AC4: a preview changes nothing; --apply moves, logs one event, and prints a
// reverse command that puts every file back.
func TestMemoryArchiveRoundTrip(t *testing.T) {
	home, _ := goldenMemoryFixture(t)
	orphan := filepath.Join(home, ".claude/projects/-proj-a/memory/orphan.md")
	note := filepath.Join(home, ".codex/memories/extensions/ad_hoc/notes/note-one.md")
	before := treeDigest(t, home)

	out, _, err := runDotForTest("ai", "memory", "archive", "claude", orphan)
	if err != nil || !strings.Contains(out, "_archive/orphan.md") || !strings.Contains(out, "Dry run") {
		t.Fatalf("preview: %v\n%s", err, out)
	}
	out, _, err = runDotForTest("ai", "memory", "archive", "codex", note)
	if err != nil || !strings.Contains(out, "memories-archive/extensions/ad_hoc/notes/note-one.md") || !strings.Contains(out, "still named by") {
		t.Fatalf("codex preview: %v\n%s", err, out)
	}
	if treeDigest(t, home) != before {
		t.Fatal("a preview changed the tree")
	}

	events := filepath.Join(home, ".local/share/dotfiles/ai/events.jsonl")
	var reverse []string
	for _, c := range [][]string{{"claude", orphan}, {"codex", note}} {
		out, _, err = runDotForTest("ai", "memory", "archive", c[0], c[1], "--apply")
		if err != nil {
			t.Fatalf("apply %s: %v\n%s", c[0], err, out)
		}
		if _, err := os.Stat(c[1]); !os.IsNotExist(err) {
			t.Fatalf("%s still in place after --apply", c[1])
		}
		i := strings.Index(out, "mv -n ")
		if i < 0 {
			t.Fatalf("no reverse command in\n%s", out)
		}
		reverse = append(reverse, strings.TrimSpace(strings.SplitN(out[i:], "\n", 2)[0]))
	}
	data, _ := os.ReadFile(events)
	if n := strings.Count(string(data), `"ai.memory.archive"`); n != 2 {
		t.Fatalf("want one audit event per apply, got %d:\n%s", n, data)
	}
	if err := os.Remove(events); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range reverse {
		if out, err := (&exec.Cmd{Path: "/bin/sh", Args: []string{"sh", "-c", cmd}, Env: []string{"PATH=/bin:/usr/bin"}}).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v %s", cmd, err, out)
		}
	}
	// The audit log and the archive directories the moves created are the
	// only additions; once they go, every file is back where it was.
	for _, dir := range []string{".local", ".codex/memories-archive"} {
		if err := os.RemoveAll(filepath.Join(home, dir)); err != nil {
			t.Fatal(err)
		}
	}
	if got := treeDigest(t, home); got != before {
		t.Fatalf("reverse command did not restore the tree\nbefore:\n%s\nafter:\n%s", before, got)
	}
}

// AC5: generated Codex files, paths outside the store and symlinks that leave
// it are refused, as are directories and the store's own index.
func TestMemoryArchiveRefuses(t *testing.T) {
	home, _ := goldenMemoryFixture(t)
	claude := filepath.Join(home, ".claude/projects/-proj-a/memory")
	outside := filepath.Join(home, "outside.md")
	writeCLITestFile(t, outside, "x\n")
	if err := os.Symlink(outside, filepath.Join(claude, "link.md")); err != nil {
		t.Fatal(err)
	}
	before := treeDigest(t, home)
	for _, c := range []struct{ agent, path, want string }{
		{"codex", filepath.Join(home, ".codex/memories/memory_summary.md"), "generated by Codex"},
		{"codex", filepath.Join(home, ".codex/memories/MEMORY.md"), "generated by Codex"},
		{"codex", filepath.Join(home, ".codex/memories/raw_memories.md"), "generated by Codex"},
		{"codex", filepath.Join(home, ".codex/memories/rollout_summaries/r1.md"), "generated by Codex"},
		{"claude", outside, "not inside a claude memory store"},
		{"codex", filepath.Join(claude, "a.md"), "not inside a codex memory store"},
		{"claude", filepath.Join(claude, "link.md"), "resolves outside"},
		{"claude", filepath.Join(claude, "MEMORY.md"), "index"},
		{"claude", claude, "whole files only"},
		{"grok", filepath.Join(home, ".grok/memory-v2/global/MEMORY.md"), "archive supports"},
	} {
		_, _, err := runDotForTest("ai", "memory", "archive", c.agent, c.path, "--apply")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("archive %s %s: err %v, want %q", c.agent, c.path, err, c.want)
		}
	}
	if treeDigest(t, home) != before {
		t.Fatal("a refused archive changed the tree")
	}
}
