// Package aimemory audits the coding agents' native memory stores and moves
// stale files into an archive next to them (#367). The audit only reads; the
// archive only renames whole files. Nothing here edits or deletes memory text.
package aimemory

import (
	"bufio"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Agents is every agent the audit knows, in report order.
var Agents = []string{"claude", "codex", "qwen", "grok", "jcode"}

const (
	StatusOK          = "ok"
	StatusUnavailable = "unavailable"

	archiveDir = "_archive"
	indexName  = "MEMORY.md"

	// Claude Code loads the first 200 lines or 25 KB of MEMORY.md; Codex
	// injects memory_summary.md up to about 10 KB.
	claudeMaxLines = 200
	claudeMaxBytes = 25_000
	codexMaxBytes  = 10_000
)

// Options names the stores to read. The CLI fills the paths from the home and
// the agents' environment; tests point them at a fixture.
type Options struct {
	Home        string
	ClaudeDirs  []string // Claude config dirs: $CLAUDE_CONFIG_DIR and ~/.claude, deduplicated
	CodexHome   string
	MarkersPath string
	Agents      []string // empty: all
	Show        bool     // keep matched line text
	// JcodeStats runs `jcode memory stats`; nil when jcode is not reachable.
	JcodeStats func() (string, error)
}

func (o Options) qwenHome() string { return filepath.Join(o.Home, ".qwen") }
func (o Options) grokRoot() string { return filepath.Join(o.Home, ".grok", "memory-v2") }
func (o Options) codexRoot() string {
	return filepath.Join(o.CodexHome, "memories")
}

// Marker is one retired term from memory-markers.yaml.
type Marker struct {
	Pattern     string `yaml:"pattern" json:"pattern"`
	Regex       bool   `yaml:"regex,omitempty" json:"regex"`
	Retired     string `yaml:"retired,omitempty" json:"retired,omitempty"`
	Replacement string `yaml:"replacement,omitempty" json:"replacement,omitempty"`
	Reason      string `yaml:"reason,omitempty" json:"reason,omitempty"`
	re          *regexp.Regexp
}

func (m Marker) match(line string) bool {
	if m.re != nil {
		return m.re.MatchString(line)
	}
	return strings.Contains(strings.ToLower(line), strings.ToLower(m.Pattern))
}

type Index struct {
	Path     string `json:"path"`
	Lines    int    `json:"lines"`
	Bytes    int64  `json:"bytes"`
	MaxLines int    `json:"maxLines,omitempty"`
	MaxBytes int64  `json:"maxBytes,omitempty"`
	Over     bool   `json:"over"`
}

type Hit struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Text string `json:"text,omitempty"`
}

type MarkerHits struct {
	Marker string `json:"marker"`
	Hits   []Hit  `json:"hits"`
}

type Store struct {
	Agent    string   `json:"agent"`
	Path     string   `json:"path"`
	Status   string   `json:"status"`
	Reason   string   `json:"reason,omitempty"`
	Files    int      `json:"files"`
	Bytes    int64    `json:"bytes"`
	Modified string   `json:"modified,omitempty"`
	Index    *Index   `json:"index,omitempty"`
	Broken   []string `json:"brokenLinks,omitempty"`
	Orphans  []string `json:"orphans,omitempty"`
	// Unreadable lists what could not be read, so a permission error never
	// passes for a clean or empty store.
	Unreadable []string     `json:"unreadable,omitempty"`
	Markers    []MarkerHits `json:"markers,omitempty"`
}

// CrossRow counts one marker's hits per agent. An agent with no readable store
// has no key, so "absent" never reads as "clean".
type CrossRow struct {
	Marker string         `json:"marker"`
	Counts map[string]int `json:"counts"`
}

type Report struct {
	MarkersFile   string     `json:"markersFile"`
	MarkersStatus string     `json:"markersStatus"`
	Markers       []Marker   `json:"markers"`
	Stores        []Store    `json:"stores"`
	CrossAgent    []CrossRow `json:"crossAgent"`
	Problems      []string   `json:"problems"`
}

// LoadMarkers reads memory-markers.yaml. A missing file is not an error: the
// report says the marker checks are unavailable.
func LoadMarkers(path string) ([]Marker, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w; check its permissions, or move it aside to audit without marker checks", path, err)
	}
	var doc struct {
		Markers []Marker `yaml:"markers"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, false, fmt.Errorf("%s is not valid YAML (%w); fix it to the form shown by 'dot ai memory audit --help'", path, err)
	}
	for i := range doc.Markers {
		m := &doc.Markers[i]
		if strings.TrimSpace(m.Pattern) == "" {
			return nil, false, fmt.Errorf("%s: marker %d has no pattern; add 'pattern: <text>' to it or remove the entry", path, i+1)
		}
		if m.Regex {
			if m.re, err = regexp.Compile(m.Pattern); err != nil {
				return nil, false, fmt.Errorf("%s: marker %q is not a valid Go regular expression (%w); fix it, or drop 'regex: true' to match it as plain text", path, m.Pattern, err)
			}
		}
	}
	return doc.Markers, true, nil
}

// Audit reads every selected store. It writes nothing.
func Audit(o Options) (Report, error) {
	markers, found, err := LoadMarkers(o.MarkersPath)
	if err != nil {
		return Report{}, err
	}
	r := Report{MarkersFile: o.MarkersPath, MarkersStatus: StatusUnavailable, Markers: markers, Problems: []string{}}
	if found {
		r.MarkersStatus = StatusOK
	}
	if r.Markers == nil {
		r.Markers = []Marker{}
	}
	selected := map[string]bool{}
	for _, a := range o.Agents {
		selected[a] = true
	}
	for _, agent := range Agents {
		if len(selected) > 0 && !selected[agent] {
			continue
		}
		r.Stores = append(r.Stores, auditAgent(o, agent, markers)...)
	}
	for i := range r.Stores {
		r.Stores[i].Unreadable = uniqueSorted(r.Stores[i].Unreadable)
	}
	for _, s := range r.Stores {
		if s.Index != nil && s.Index.Over {
			r.Problems = append(r.Problems, fmt.Sprintf("%s %s: index over its limit; archive stale files with 'dot ai memory archive %s PATH' and remove their index lines", s.Agent, s.Index.Path, s.Agent))
		}
		if len(s.Broken) > 0 {
			r.Problems = append(r.Problems, fmt.Sprintf("%s %s: %d index link(s) to missing files; remove those lines from the index or restore the files", s.Agent, s.Path, len(s.Broken)))
		}
		if len(s.Unreadable) > 0 {
			r.Problems = append(r.Problems, fmt.Sprintf("%s %s: %d path(s) could not be read; check their permissions (ls -l) and re-run", s.Agent, s.Path, len(s.Unreadable)))
		}
	}
	r.CrossAgent = crossAgent(r.Stores, markers)
	return r, nil
}

func auditAgent(o Options, agent string, markers []Marker) []Store {
	switch agent {
	case "claude":
		roots := o.claudeRoots()
		if len(roots) == 0 {
			return []Store{missing(agent, filepath.Join(o.ClaudeDirs[len(o.ClaudeDirs)-1], "projects", "*", "memory"), "no project memory directory; Claude Code creates one when auto memory saves a note, or set CLAUDE_CONFIG_DIR to the profile to audit")}
		}
		var out []Store
		for _, root := range roots {
			out = append(out, linkedStore(agent, root, &Index{MaxLines: claudeMaxLines, MaxBytes: claudeMaxBytes}, markers, o.Show))
		}
		return out
	case "qwen":
		roots := append([]string{filepath.Join(o.qwenHome(), "memories")}, memoryDirs(filepath.Join(o.qwenHome(), "projects"))...)
		var out []Store
		for _, root := range roots {
			if !isDir(root) {
				out = append(out, missing(agent, root, "not found; Qwen creates it when it saves a memory"))
				continue
			}
			out = append(out, linkedStore(agent, root, &Index{}, markers, o.Show))
		}
		return out
	case "codex":
		root := o.codexRoot()
		if !isDir(root) {
			return []Store{missing(agent, root, "not found; Codex creates it when memories are on, or set CODEX_HOME to the home to audit")}
		}
		s := scanStore(agent, root, markers, o.Show)
		s.Index = indexSize(&s, filepath.Join(root, "memory_summary.md"), &Index{MaxBytes: codexMaxBytes})
		codexLinks(&s, root)
		return []Store{s}
	case "grok":
		root := o.grokRoot()
		if !isDir(root) {
			return []Store{missing(agent, root, "not found; Grok creates it when it saves a memory")}
		}
		indexes, _ := filepath.Glob(filepath.Join(root, "workspaces", "*", indexName))
		indexes = append([]string{filepath.Join(root, "global", indexName)}, indexes...)
		var out []Store
		for _, idx := range indexes {
			info, err := os.Stat(idx)
			if err != nil {
				continue
			}
			s := Store{Agent: agent, Path: filepath.Dir(idx), Status: StatusOK, Files: 1, Bytes: info.Size(), Modified: stamp(info.ModTime())}
			s.Index = indexSize(&s, idx, &Index{})
			s.Markers = scanMarkers(&s, filepath.Dir(idx), []string{indexName}, markers, o.Show)
			out = append(out, s)
		}
		if len(out) == 0 {
			return []Store{missing(agent, root, "no MEMORY.md index; Grok writes one with its first saved memory")}
		}
		return out
	case "jcode":
		if o.JcodeStats == nil {
			return []Store{missing(agent, "jcode memory stats", "jcode not on PATH, or --home is set; install jcode or run without --home")}
		}
		text, err := o.JcodeStats()
		total, ok := jcodeTotal(text)
		if err != nil || !ok {
			return []Store{missing(agent, "jcode memory stats", "'jcode memory stats' failed or printed no Total; run it by hand to see why")}
		}
		return []Store{{Agent: agent, Path: "jcode memory stats", Status: StatusOK, Files: total}}
	}
	return nil
}

// claudeRoots lists the project memory dirs of every Claude config dir once:
// an alternate profile often links its projects/ to ~/.claude/projects.
func (o Options) claudeRoots() []string {
	var out []string
	seen := map[string]bool{}
	for _, dir := range o.ClaudeDirs {
		for _, root := range memoryDirs(filepath.Join(dir, "projects")) {
			key := root
			if real, err := filepath.EvalSymlinks(root); err == nil {
				key = real
			}
			if !seen[key] {
				seen[key] = true
				out = append(out, root)
			}
		}
	}
	return out
}

func missing(agent, path, reason string) Store {
	return Store{Agent: agent, Path: path, Status: StatusUnavailable, Reason: reason}
}

func memoryDirs(projects string) []string {
	dirs, _ := filepath.Glob(filepath.Join(projects, "*", "memory"))
	var out []string
	for _, d := range dirs {
		if isDir(d) {
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}

func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

var jcodeTotalRe = regexp.MustCompile(`(?m)^\s*Total:\s*(\d+)`)

func jcodeTotal(text string) (int, bool) {
	m := jcodeTotalRe.FindStringSubmatch(text)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil
}

// files lists the store's regular files relative to root, outside _archive,
// and what it could not read.
func files(root string) (out, unreadable []string) {
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			rel, _ := filepath.Rel(root, path)
			unreadable = append(unreadable, filepath.ToSlash(rel))
			return nil
		}
		if d.IsDir() && d.Name() == archiveDir && path != root {
			return filepath.SkipDir
		}
		if d.Type().IsRegular() {
			rel, _ := filepath.Rel(root, path)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out, unreadable
}

func scanStore(agent, root string, markers []Marker, show bool) Store {
	s := Store{Agent: agent, Path: root, Status: StatusOK}
	var latest time.Time
	var md []string
	all, unreadable := files(root)
	s.Unreadable = unreadable
	for _, rel := range all {
		info, err := os.Stat(filepath.Join(root, rel))
		if err != nil {
			s.Unreadable = append(s.Unreadable, rel)
			continue
		}
		s.Files++
		s.Bytes += info.Size()
		if info.ModTime().After(latest) {
			latest = info.ModTime()
		}
		if strings.HasSuffix(rel, ".md") {
			md = append(md, rel)
		}
	}
	if !latest.IsZero() {
		s.Modified = stamp(latest)
	}
	s.Markers = scanMarkers(&s, root, md, markers, show)
	return s
}

// linkedStore is a Claude or Qwen store: a MEMORY.md index that links its files.
func linkedStore(agent, root string, limits *Index, markers []Marker, show bool) Store {
	s := scanStore(agent, root, markers, show)
	idx := filepath.Join(root, indexName)
	s.Index = indexSize(&s, idx, limits)
	linked := map[string]bool{}
	if data, err := os.ReadFile(idx); err == nil {
		for _, m := range linkRe.FindAllStringSubmatch(string(data), -1) {
			target, ok := localLink(m[1])
			if !ok {
				continue
			}
			rel := filepath.ToSlash(filepath.Clean(target))
			if linked[rel] {
				continue
			}
			linked[rel] = true
			if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
				s.Broken = append(s.Broken, rel)
			}
		}
	}
	all, _ := files(root)
	for _, rel := range all {
		if rel != indexName && strings.HasSuffix(rel, ".md") && !linked[rel] {
			s.Orphans = append(s.Orphans, rel)
		}
	}
	return s
}

// codexRefRe matches the store paths Codex writes into its generated indexes
// as plain text, such as rollout_summaries/<name>.md.
var codexRefRe = regexp.MustCompile(`\b((?:rollout_summaries|extensions|skills)/[A-Za-z0-9_./-]*?\.md)\b`)

// codexLinks checks the paths named by MEMORY.md and memory_summary.md, and
// lists rollout summaries and ad-hoc notes that neither names.
func codexLinks(s *Store, root string) {
	named := map[string]bool{}
	for _, name := range []string{indexName, "memory_summary.md"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			if !os.IsNotExist(err) {
				s.Unreadable = append(s.Unreadable, name)
			}
			continue
		}
		refs := codexRefRe.FindAllStringSubmatch(string(data), -1)
		for _, m := range linkRe.FindAllStringSubmatch(string(data), -1) {
			if target, ok := localLink(m[1]); ok {
				refs = append(refs, []string{"", target})
			}
		}
		for _, m := range refs {
			rel := filepath.ToSlash(filepath.Clean(m[1]))
			if named[rel] {
				continue
			}
			named[rel] = true
			if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
				s.Broken = append(s.Broken, rel)
			}
		}
	}
	all, _ := files(root)
	for _, rel := range all {
		source := strings.HasPrefix(rel, "rollout_summaries/") || strings.HasPrefix(rel, "extensions/ad_hoc/notes/")
		if source && strings.HasSuffix(rel, ".md") && !named[rel] {
			s.Orphans = append(s.Orphans, rel)
		}
	}
	sort.Strings(s.Broken)
}

var linkRe = regexp.MustCompile(`\]\(([^)\s]+)\)`)

// localLink returns a Markdown link target as a store-relative path, without
// its fragment and percent escapes; web, mail and absolute links are not.
func localLink(target string) (string, bool) {
	if i := strings.IndexByte(target, '#'); i >= 0 {
		target = target[:i]
	}
	if target == "" || strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") || filepath.IsAbs(target) {
		return "", false
	}
	if unescaped, err := url.PathUnescape(target); err == nil {
		target = unescaped
	}
	return target, true
}

// indexSize measures an injected index against its limits. A missing index
// returns a zero-size record, which is under any limit; an unreadable one is
// recorded on the store.
func indexSize(s *Store, path string, limits *Index) *Index {
	idx := *limits
	idx.Path = path
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			s.Unreadable = append(s.Unreadable, filepath.Base(path))
		}
		return &idx
	}
	idx.Bytes = int64(len(data))
	idx.Lines = strings.Count(string(data), "\n")
	if len(data) > 0 && data[len(data)-1] != '\n' {
		idx.Lines++
	}
	idx.Over = (idx.MaxLines > 0 && idx.Lines > idx.MaxLines) || (idx.MaxBytes > 0 && idx.Bytes > idx.MaxBytes)
	return &idx
}

func scanMarkers(s *Store, root string, rels []string, markers []Marker, show bool) []MarkerHits {
	var out []MarkerHits
	failed := map[string]bool{}
	for _, m := range markers {
		var hits []Hit
		for _, rel := range rels {
			f, err := os.Open(filepath.Join(root, rel))
			if err != nil {
				if !failed[rel] {
					failed[rel] = true
					s.Unreadable = append(s.Unreadable, rel)
				}
				continue
			}
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
			for n := 1; sc.Scan(); n++ {
				if line := sc.Text(); m.match(line) {
					h := Hit{File: rel, Line: n}
					if show {
						h.Text = line
					}
					hits = append(hits, h)
				}
			}
			if sc.Err() != nil && !failed[rel] {
				failed[rel] = true
				s.Unreadable = append(s.Unreadable, rel)
			}
			f.Close()
		}
		if len(hits) > 0 {
			out = append(out, MarkerHits{Marker: m.Pattern, Hits: hits})
		}
	}
	return out
}

func crossAgent(stores []Store, markers []Marker) []CrossRow {
	rows := []CrossRow{}
	for _, m := range markers {
		row := CrossRow{Marker: m.Pattern, Counts: map[string]int{}}
		for _, s := range stores {
			if s.Status != StatusOK || s.Agent == "jcode" {
				continue
			}
			row.Counts[s.Agent] += 0
			for _, mh := range s.Markers {
				if mh.Marker == m.Pattern {
					row.Counts[s.Agent] += len(mh.Hits)
				}
			}
		}
		rows = append(rows, row)
	}
	return rows
}

func uniqueSorted(list []string) []string {
	sort.Strings(list)
	out := list[:0]
	for i, v := range list {
		if i == 0 || v != list[i-1] {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
