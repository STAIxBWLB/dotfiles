package aimemory

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fixture is a synthetic home with Claude, Codex, Qwen and Grok stores.
func fixture(t *testing.T) Options {
	t.Helper()
	home := t.TempDir()
	o := Options{Home: home, ClaudeDirs: []string{filepath.Join(home, ".claude")}, CodexHome: filepath.Join(home, ".codex"),
		MarkersPath: filepath.Join(home, "markers.yaml")}
	write(t, o.MarkersPath, "markers:\n  - pattern: Old-Plugin\n  - pattern: 'repo-v[0-9]'\n    regex: true\n")
	claude := filepath.Join(home, ".claude/projects/-p/memory")
	write(t, filepath.Join(claude, indexName), "- [A](a.md)\n- [Gone](gone.md#top)\n- [Web](https://x.invalid/y.md)\n- [S](my%20note.md)\n")
	write(t, filepath.Join(claude, "a.md"), "uses old-plugin\nsecret line\n")
	write(t, filepath.Join(claude, "my note.md"), "spaced\n")
	write(t, filepath.Join(claude, "orphan.md"), "repo-v1\n")
	write(t, filepath.Join(claude, archiveDir, "x.md"), "old-plugin\n")
	codex := filepath.Join(home, ".codex/memories")
	write(t, filepath.Join(codex, "memory_summary.md"), strings.Repeat("x", codexMaxBytes+1))
	write(t, filepath.Join(codex, indexName), "see rollout_summaries/r1.md and extensions/ad_hoc/notes/gone.md\n")
	write(t, filepath.Join(codex, "rollout_summaries/r1.md"), "r1\n")
	write(t, filepath.Join(codex, "rollout_summaries/r2.md"), "r2\n")
	write(t, filepath.Join(codex, "extensions/ad_hoc/notes/n.md"), "old-plugin\n")
	write(t, filepath.Join(home, ".qwen/memories", indexName), "- [F](feedback/f.md)\n")
	write(t, filepath.Join(home, ".qwen/memories/feedback/f.md"), "f\n")
	write(t, filepath.Join(home, ".grok/memory-v2/global", indexName), "OLD-PLUGIN\n")
	return o
}

func store(t *testing.T, r Report, agent string) Store {
	t.Helper()
	for _, s := range r.Stores {
		if s.Agent == agent && s.Status == StatusOK {
			return s
		}
	}
	t.Fatalf("no readable %s store in %+v", agent, r.Stores)
	return Store{}
}

func TestAudit(t *testing.T) {
	o := fixture(t)
	o.JcodeStats = func() (string, error) { return "  Total:            7\n", nil }
	r, err := Audit(o)
	if err != nil {
		t.Fatal(err)
	}
	c := store(t, r, "claude")
	if strings.Join(c.Broken, ",") != "gone.md" || strings.Join(c.Orphans, ",") != "orphan.md" || c.Files != 4 {
		t.Errorf("claude: broken %v orphans %v files %d", c.Broken, c.Orphans, c.Files)
	}
	if len(c.Markers) != 2 || c.Markers[0].Hits[0].Text != "" {
		t.Errorf("claude markers %+v (text must stay out without Show)", c.Markers)
	}
	x := store(t, r, "codex")
	if !x.Index.Over || strings.Join(x.Broken, ",") != "extensions/ad_hoc/notes/gone.md" || strings.Join(x.Orphans, ",") != "extensions/ad_hoc/notes/n.md,rollout_summaries/r2.md" {
		t.Errorf("codex: over %v broken %v orphans %v", x.Index.Over, x.Broken, x.Orphans)
	}
	if g := store(t, r, "grok"); len(g.Markers) != 1 {
		t.Errorf("grok marker hits %+v (case-insensitive text)", g.Markers)
	}
	if j := store(t, r, "jcode"); j.Files != 7 {
		t.Errorf("jcode total %d", j.Files)
	}
	if len(r.Problems) != 3 { // claude broken, codex over, codex broken
		t.Errorf("problems %v", r.Problems)
	}
	if got := r.CrossAgent[0].Counts; got["claude"] != 1 || got["codex"] != 1 || got["grok"] != 1 || got["qwen"] != 0 {
		t.Errorf("cross-agent counts %v", got)
	}
	o.Show, o.Agents = true, []string{"claude"}
	if r, _ = Audit(o); len(r.Stores) != 1 || r.Stores[0].Markers[0].Hits[0].Text != "uses old-plugin" {
		t.Errorf("--show and --agent: %+v", r.Stores)
	}
}

func TestAuditUnavailable(t *testing.T) {
	home := t.TempDir()
	o := Options{Home: home, ClaudeDirs: []string{filepath.Join(home, ".claude")}, CodexHome: filepath.Join(home, ".codex"), MarkersPath: filepath.Join(home, "none.yaml")}
	o.JcodeStats = func() (string, error) { return "no total here", nil }
	r, err := Audit(o)
	if err != nil {
		t.Fatal(err)
	}
	if r.MarkersStatus != StatusUnavailable || len(r.Problems) != 0 {
		t.Errorf("markers %s problems %v", r.MarkersStatus, r.Problems)
	}
	for _, s := range r.Stores {
		if s.Status != StatusUnavailable || s.Reason == "" {
			t.Errorf("%s %s: status %s reason %q", s.Agent, s.Path, s.Status, s.Reason)
		}
	}
}

func TestAuditUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every file")
	}
	o := fixture(t)
	a := filepath.Join(o.Home, ".claude/projects/-p/memory/a.md")
	if err := os.Chmod(a, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(a, 0o644) })
	r, err := Audit(o)
	if err != nil {
		t.Fatal(err)
	}
	if c := store(t, r, "claude"); strings.Join(c.Unreadable, ",") != "a.md" {
		t.Errorf("unreadable %v", c.Unreadable)
	}
	found := false
	for _, p := range r.Problems {
		found = found || strings.Contains(p, "could not be read")
	}
	if !found {
		t.Errorf("an unreadable file must be a problem: %v", r.Problems)
	}
}

func TestLoadMarkersErrors(t *testing.T) {
	dir := t.TempDir()
	for body, want := range map[string]string{
		"markers: [":                "not valid YAML",
		"markers:\n  - reason: x\n": "has no pattern",
		"markers:\n  - pattern: '('\n    regex: true\n": "not a valid Go regular expression",
	} {
		path := filepath.Join(dir, "m.yaml")
		write(t, path, body)
		if _, err := Audit(Options{MarkersPath: path}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err %v, want %q", body, err, want)
		}
	}
}

func TestArchiveRoundTrip(t *testing.T) {
	o := fixture(t)
	f := filepath.Join(o.Home, ".qwen/memories/feedback/f.md")
	n := filepath.Join(o.Home, ".codex/memories/extensions/ad_hoc/notes/n.md")
	moves, err := PlanArchive(o, "qwen", []string{f, f})
	if err != nil || len(moves) != 1 || !strings.HasSuffix(moves[0].To, "/.qwen/memories/_archive/feedback/f.md") || len(moves[0].IndexRefs) != 1 {
		t.Fatalf("qwen plan %+v %v", moves, err)
	}
	codex, err := PlanArchive(o, "codex", []string{n})
	if err != nil || !strings.HasSuffix(codex[0].To, "/.codex/memories-archive/extensions/ad_hoc/notes/n.md") {
		t.Fatalf("codex plan %+v %v", codex, err)
	}
	if _, err := os.Stat(f); err != nil {
		t.Fatal("planning moved a file")
	}
	done, err := ApplyArchive(append(moves, codex...))
	if err != nil || len(done) != 2 {
		t.Fatalf("apply %v %v", done, err)
	}
	for _, m := range done {
		if _, err := os.Stat(m.To); err != nil {
			t.Fatalf("%s not archived", m.To)
		}
	}
	cmd := exec.Command("/bin/sh", "-c", ReverseCommand(done))
	cmd.Env = []string{"PATH=/bin:/usr/bin"}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("reverse: %v %s", err, out)
	}
	for _, p := range []string{f, n} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s not restored", p)
		}
	}
}

func TestArchiveRefuses(t *testing.T) {
	o := fixture(t)
	claude := filepath.Join(o.Home, ".claude/projects/-p/memory")
	outside := filepath.Join(o.Home, "outside")
	write(t, filepath.Join(outside, "o.md"), "o\n")
	if err := os.Symlink(filepath.Join(outside, "o.md"), filepath.Join(claude, "link.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "nowhere.md"), filepath.Join(claude, "dangling.md")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(claude, "_archive/a.md"), "taken\n")
	cases := []struct{ agent, path, want string }{
		{"grok", filepath.Join(claude, "a.md"), "archive supports"},
		{"codex", filepath.Join(o.Home, ".codex/memories/memory_summary.md"), "generated by Codex"},
		{"codex", filepath.Join(o.Home, ".codex/memories/rollout_summaries/r1.md"), "generated by Codex"},
		{"claude", filepath.Join(outside, "o.md"), "not inside a claude memory store"},
		{"claude", filepath.Join(claude, "link.md"), "resolves outside"},
		{"claude", filepath.Join(claude, "dangling.md"), "broken symlink"},
		{"claude", filepath.Join(claude, indexName), "index"},
		{"claude", filepath.Join(claude, "_archive/x.md"), "already archived"},
		{"claude", filepath.Join(claude, "a.md"), "already exists"},
		{"claude", filepath.Join(claude, "missing.md"), "no such file"},
		{"claude", claude, "whole files only"},
	}
	for _, c := range cases {
		if _, err := PlanArchive(o, c.agent, []string{c.path}); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %s: err %v, want %q", c.agent, c.path, err, c.want)
		}
	}

	// An archive directory that is a symlink would carry the move outside.
	qwen := filepath.Join(o.Home, ".qwen/memories")
	if err := os.Symlink(outside, filepath.Join(qwen, archiveDir)); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanArchive(o, "qwen", []string{filepath.Join(qwen, "feedback/f.md")}); err == nil || !strings.Contains(err.Error(), "symlink or a file") {
		t.Errorf("symlinked _archive: err %v", err)
	}
}

// A symlink swapped in after the plan is caught when the move runs.
func TestApplyArchiveRechecksDestination(t *testing.T) {
	o := fixture(t)
	f := filepath.Join(o.Home, ".qwen/memories/feedback/f.md")
	moves, err := PlanArchive(o, "qwen", []string{f})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(o.Home, ".qwen/memories", archiveDir)); err != nil {
		t.Fatal(err)
	}
	done, err := ApplyArchive(moves)
	if err == nil || len(done) != 0 || !strings.Contains(err.Error(), "symlink or a file") {
		t.Fatalf("done %v err %v", done, err)
	}
	if _, err := os.Stat(f); err != nil {
		t.Fatal("the file moved through a symlinked archive")
	}
}
