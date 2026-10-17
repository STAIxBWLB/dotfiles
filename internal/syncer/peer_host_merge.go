package syncer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// hotHostPaths are host files both Macs rewrite on their own (#181):
// Claude Code rewrites ~/.claude.json whenever it starts, and the skills
// sync rewrites its manifests. Newest-wins then drops whatever only the
// losing copy had.
var hotHostPaths = []string{".claude.json", ".claude/skills/synced/*/manifest.json"}

// defaultHotKeys are the top-level keys compared for a hot JSON file with no
// host_merge policy: losing one of these entries loses an MCP server or a
// project's settings.
var defaultHotKeys = []string{"mcpServers", "projects"}

func isHotHostPath(rel string) bool {
	for _, pattern := range hotHostPaths {
		if ok, _ := path.Match(pattern, rel); ok {
			return true
		}
	}
	return false
}

// keyDiff is one configured key's difference between the two copies.
type keyDiff struct {
	key                 string
	onlyLocal, onlyPeer []string
	changed             []string
}

func (d keyDiff) String() string {
	var parts []string
	if len(d.onlyPeer) > 0 {
		parts = append(parts, "peer only "+strings.Join(d.onlyPeer, ", "))
	}
	if len(d.onlyLocal) > 0 {
		parts = append(parts, "here only "+strings.Join(d.onlyLocal, ", "))
	}
	if len(d.changed) > 0 {
		parts = append(parts, "differ "+strings.Join(d.changed, ", "))
	}
	return d.key + ": " + strings.Join(parts, "; ")
}

func decodeJSONObject(data []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber() // keep large integers exact
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		return nil, err
	}
	// null decodes to a nil map, which would merge as an empty object and
	// drop every other key of the file (its tokens among them).
	if obj == nil || dec.More() {
		return nil, fmt.Errorf("not a single JSON object")
	}
	return obj, nil
}

// diffJSONKeys compares the entries of each key's object value. A key whose
// value is not an object on both sides is compared whole.
func diffJSONKeys(local, peer map[string]any, keys []string) []keyDiff {
	var out []keyDiff
	for _, key := range keys {
		l, lok := local[key]
		r, rok := peer[key]
		d := keyDiff{key: key}
		lm, lIsObj := l.(map[string]any)
		rm, rIsObj := r.(map[string]any)
		switch {
		case !lok && !rok:
			continue
		case lIsObj && rIsObj:
			for name, lv := range lm {
				rv, ok := rm[name]
				switch {
				case !ok:
					d.onlyLocal = append(d.onlyLocal, name)
				case !jsonEqual(lv, rv):
					d.changed = append(d.changed, name)
				}
			}
			for name := range rm {
				if _, ok := lm[name]; !ok {
					d.onlyPeer = append(d.onlyPeer, name)
				}
			}
		case !lok:
			d.onlyPeer = []string{"(whole key)"}
		case !rok:
			d.onlyLocal = []string{"(whole key)"}
		case !jsonEqual(l, r):
			d.changed = []string{"(value)"}
		}
		if len(d.onlyLocal)+len(d.onlyPeer)+len(d.changed) == 0 {
			continue
		}
		sort.Strings(d.onlyLocal)
		sort.Strings(d.onlyPeer)
		sort.Strings(d.changed)
		out = append(out, d)
	}
	return out
}

func jsonEqual(a, b any) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ja, jb)
}

// mergeJSONKeys merges the configured keys' object entries: every entry
// either copy has survives, and the newer copy wins an entry both have.
// Everything else is the newer copy's.
func mergeJSONKeys(newer, older map[string]any, keys []string) map[string]any {
	merged := make(map[string]any, len(newer))
	for k, v := range newer {
		merged[k] = v
	}
	for _, key := range keys {
		ov, ook := older[key]
		if !ook {
			continue
		}
		nv, nok := newer[key]
		if !nok {
			merged[key] = ov
			continue
		}
		nm, nIsObj := nv.(map[string]any)
		om, oIsObj := ov.(map[string]any)
		if !nIsObj || !oIsObj {
			continue
		}
		union := make(map[string]any, len(nm)+len(om))
		for name, v := range om {
			union[name] = v
		}
		for name, v := range nm {
			union[name] = v
		}
		merged[key] = union
	}
	return merged
}

func encodeJSONObject(obj map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(obj); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// readPeerHostFile reads a host file on the peer, relative to its home. A
// missing file is (nil, nil).
func readPeerHostFile(ctx context.Context, runner *exec.Runner, cfg *Config, rel string) ([]byte, error) {
	// A symlink or another non-regular copy there is refused as it is here:
	// the merged copy's rsync -t would replace it with a regular file.
	q := shellQuote(rel)
	res, err := runner.Run(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", cfg.Target.Host,
		"if [ -L "+q+" ]; then echo __dot_symlink__ >&2; exit 4; elif [ -f "+q+" ]; then cat -- "+q+"; elif [ -e "+q+" ]; then echo __dot_nonregular__ >&2; exit 5; else echo __dot_absent__ >&2; exit 3; fi")
	if err != nil {
		if res != nil && strings.Contains(res.Stderr, "__dot_symlink__") {
			return nil, fmt.Errorf("%s on %s: %w", rel, cfg.Target.Host, errPeerHostSymlink)
		}
		if res != nil && strings.Contains(res.Stderr, "__dot_nonregular__") {
			return nil, fmt.Errorf("%s on %s: %w", rel, cfg.Target.Host, errPeerHostNonRegular)
		}
		if res != nil && strings.Contains(res.Stderr, "__dot_absent__") {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s on %s: %w", rel, cfg.Target.Host, err)
	}
	return []byte(res.Stdout), nil
}

var (
	errPeerHostSymlink    = errors.New("a symlink; host_merge writes regular files only")
	errPeerHostNonRegular = errors.New("not a regular file (a directory?); host_merge writes regular files only")
)

// skillRootPrefixes are the tool skill roots and Maru trees dot must not
// write (docs/BOUNDARIES.md); host_merge refuses files under them.
var skillRootPrefixes = []string{".claude/skills/", ".codex/skills/", ".agents/skills/", ".gemini/skills/",
	".gemini/antigravity/skills/", ".kimi-code/skills/", ".qwen/skills/", ".grok/skills/", ".config/opencode/skills/",
	".maru/skills/", ".maru/env/"}

// validateHostMerge checks every host_merge key is a clean path relative to
// $HOME, like .claude.json, outside the tool skill roots.
func validateHostMerge(m map[string][]string) error {
	for rel, keys := range m {
		// A line break would split the create-only pass's list and escape
		// the additive pass's exclusion.
		if validateTombstoneRel(rel) != nil || strings.HasPrefix(rel, "~") || strings.ContainsAny(rel, "\r\n") {
			return fmt.Errorf("host_merge: %q must be a path relative to $HOME, like .claude.json", rel)
		}
		// Excluded from newest-wins but merged by no key, the file would
		// never move.
		if len(keys) == 0 {
			return fmt.Errorf("host_merge: %q lists no keys; name the top-level JSON keys to merge, or drop it", rel)
		}
		for _, root := range skillRootPrefixes {
			// APFS is case-insensitive: .Claude/skills is the same directory.
			if strings.HasPrefix(strings.ToLower(rel), root) {
				return fmt.Errorf("host_merge: %q is under a tool skill root, which dot does not write", rel)
			}
		}
	}
	return nil
}

// hostMerge is one host_merge file's decision. planHostMerges takes it for
// the plan, the preflight and the run alike, so the plan shows what the run
// does (#180 AC2, #181).
type hostMerge struct {
	rel         string
	keys        []string
	local, peer map[string]any // both copies, decoded
	result      map[string]any // what both machines get
	localInfo   os.FileInfo    // re-checked right before the write
	peerData    []byte         // the peer's copy as read, re-checked likewise
	peerMtime   time.Time      // to the second; zero when not read
	refused     string         // why the run cannot merge it; it stops before anything moves
	same        bool           // equal in substance: nothing to write, still host_merge's
}

// wouldMove says whether newest-wins would move the file in direction,
// the plan's test for listing it as held by a one-way run. The peer's mtime
// has one-second precision.
func (m *hostMerge) wouldMove(direction string) bool {
	if m.same {
		return false
	}
	if m.localInfo == nil || m.peerMtime.IsZero() {
		return true // refused: unknown, so listed
	}
	local := m.localInfo.ModTime().Truncate(time.Second)
	if direction == "pull" {
		return !local.After(m.peerMtime)
	}
	return !m.peerMtime.After(local)
}

// hostMergeFiles are the host_merge files the additive host list
// (home-paths.txt without the tracked entries) covers: host_merge owns them
// in every run.
func hostMergeFiles(cfg *Config) ([]string, error) {
	if len(cfg.HostMerge) == 0 {
		return nil, nil
	}
	entries, err := additiveHomeEntries(cfg)
	if err != nil {
		return nil, err
	}
	var files []string
	for rel, keys := range cfg.HostMerge {
		if len(keys) > 0 && homeEntriesCover(entries, rel) {
			files = append(files, rel)
		}
	}
	sort.Strings(files)
	return files, nil
}

// hostMergeList writes hostMergeFiles as the create-only pass's
// --files-from list; "" when there are none.
func hostMergeList(cfg *Config, dryRun bool) (string, func(), error) {
	files, err := hostMergeFiles(cfg)
	if err != nil || len(files) == 0 {
		return "", func() {}, err
	}
	dir, cleanup, err := peerHomeScopedDir(cfg, dryRun)
	if err != nil {
		return "", nil, err
	}
	path := filepath.Join(dir, "host-merge.dyn")
	if err := atomicWrite(path, []byte(strings.Join(files, "\n")+"\n")); err != nil {
		cleanup()
		return "", nil, err
	}
	return path, cleanup, nil
}

// planHostMerges decides every host_merge file (hostMergeFiles) that
// exists on both machines: copies equal in substance need nothing; a
// symlink or another non-regular copy, or one that is not a JSON object, is
// a refusal; anything else merges into the newer copy. A file on one Mac
// only is the create-only pass's. It only reads.
func planHostMerges(ctx context.Context, probe *exec.Runner, cfg *Config) ([]hostMerge, error) {
	if len(cfg.HostMerge) == 0 {
		return nil, nil
	}
	if err := validateHostMerge(cfg.HostMerge); err != nil {
		return nil, err
	}
	files, err := hostMergeFiles(cfg)
	if err != nil {
		return nil, err
	}
	var merges []hostMerge
	for _, rel := range files {
		m := hostMerge{rel: rel, keys: cfg.HostMerge[rel]}
		localPath := filepath.Join(cfg.HomeDir(), filepath.FromSlash(rel))
		info, err := os.Lstat(localPath)
		if os.IsNotExist(err) {
			continue // absent here: the create-only pass brings the peer's
		}
		if err != nil {
			return nil, err
		}
		peerData, err := readPeerHostFile(ctx, probe, cfg, rel)
		switch {
		case errors.Is(err, errPeerHostSymlink):
			m.refused = "a symlink on " + cfg.Target.Host
			merges = append(merges, m)
			continue
		case errors.Is(err, errPeerHostNonRegular):
			m.refused = "not a regular file on " + cfg.Target.Host + " (a directory?)"
			merges = append(merges, m)
			continue
		}
		if err != nil {
			return nil, err
		}
		if peerData == nil {
			continue // absent there (or not a file): nothing to merge with
		}
		if !info.Mode().IsRegular() {
			m.refused = "not a regular file here (a symlink?)"
			merges = append(merges, m)
			continue
		}
		localData, err := os.ReadFile(localPath)
		if err != nil {
			return nil, err
		}
		local, lerr := decodeJSONObject(localData)
		peer, perr := decodeJSONObject(peerData)
		if lerr != nil || perr != nil {
			m.refused = "both copies must be JSON objects"
			merges = append(merges, m)
			continue
		}
		if jsonEqual(local, peer) {
			// Nothing to write, and still not newest-wins input: a stale
			// save later in the run must not reach the other Mac.
			m.same = true
			merges = append(merges, m)
			continue
		}
		peerMtime, err := peerHostMtime(ctx, probe, cfg, rel)
		if err != nil {
			return nil, err
		}
		newer, older := local, peer
		if peerMtime.After(info.ModTime()) {
			newer, older = peer, local
		}
		m.local, m.peer, m.localInfo, m.peerData, m.peerMtime = local, peer, info, peerData, peerMtime
		m.result = mergeJSONKeys(newer, older, m.keys)
		merges = append(merges, m)
	}
	return merges, nil
}

// hostMergeRefusal is the run's stop on any refused file; where says what
// has moved by then.
func hostMergeRefusal(merges []hostMerge, where string) error {
	for _, m := range merges {
		if m.refused != "" {
			return fmt.Errorf("host_merge %s: %s; fix it or drop it from host_merge (%s)", m.rel, m.refused, where)
		}
	}
	return nil
}

// additiveHomeEntries is the additive pass's list: home-paths.txt without
// the entries the tracked pass owns (peerHomeUntrackedList's rule).
func additiveHomeEntries(cfg *Config) ([]string, error) {
	body, err := os.ReadFile(PeerHomePathsFile(cfg.LocalPaths))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var active []string
	for _, line := range strings.Split(string(body), "\n") {
		entry := strings.TrimSpace(line)
		if entry != "" && !strings.HasPrefix(entry, "#") {
			active = append(active, entry)
		}
	}
	tracked, err := readPeerHomeTrackedEntries(PeerHomeTrackedFile(cfg.LocalPaths))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	kept, _ := filterUntrackedHomeEntries(active, tracked)
	return kept, nil
}

// homeEntriesCover reports whether an entry names rel or a directory
// holding it.
func homeEntriesCover(entries []string, rel string) bool {
	for _, e := range entries {
		entry := strings.TrimPrefix(strings.TrimSuffix(filepath.ToSlash(e), "/"), "./")
		if rel == entry || strings.HasPrefix(rel, entry+"/") {
			return true
		}
	}
	return false
}

// hostKeyPolicy is the key list to compare or merge for a host file, and
// whether it is a merge policy (host_merge) rather than the hot-file default.
func hostKeyPolicy(cfg *Config, rel string) ([]string, bool) {
	if keys, ok := cfg.HostMerge[rel]; ok && len(keys) > 0 {
		return keys, true
	}
	if isHotHostPath(rel) && strings.HasSuffix(rel, ".json") {
		return defaultHotKeys, false
	}
	return nil, false
}

// runDirection names what a run moves: "both", or the one direction of a
// --push-only or --pull-only run.
func runDirection(pushOnly, pullOnly bool) string {
	switch {
	case pushOnly:
		return "push"
	case pullOnly:
		return "pull"
	}
	return "both"
}

// annotateHotItems marks hot host items and lists the key-level
// differences of hot JSON files; a losing copy that holds entries the
// winner lacks is called out: newest-wins would drop them. The files
// planHostMerges decided are not newest-wins input and are not listed by
// the pass: each one that differs becomes an item here, merged in a two-way
// run (direction "both"), held in a one-way one.
func annotateHotItems(ctx context.Context, probe *exec.Runner, cfg *Config, items []PeerPlanItem, direction string, merges []hostMerge) []PeerPlanItem {
	for i := range items {
		it := &items[i]
		if it.Scope == PlanScopeWorkspace {
			continue
		}
		keys, merge := hostKeyPolicy(cfg, it.Path)
		it.Hot = isHotHostPath(it.Path) || merge
		if len(keys) == 0 {
			continue
		}
		localData, err := os.ReadFile(filepath.Join(cfg.HomeDir(), filepath.FromSlash(it.Path)))
		if err != nil {
			continue
		}
		peerData, err := readPeerHostFile(ctx, probe, cfg, it.Path)
		if err != nil || peerData == nil {
			continue
		}
		local, lerr := decodeJSONObject(localData)
		peer, perr := decodeJSONObject(peerData)
		if lerr != nil || perr != nil {
			continue
		}
		hint := "set host_merge for this file to keep them"
		if merge && it.Scope != PlanScopeHost {
			hint = "host_merge does not apply to tracked host paths"
		}
		var warnings []string
		for _, d := range diffJSONKeys(local, peer, keys) {
			it.Keys = append(it.Keys, d.String())
			// The copy this item overwrites loses what only it has.
			lost := d.onlyLocal
			if it.Direction == "push" {
				lost = d.onlyPeer
			}
			if len(lost) > 0 {
				warnings = append(warnings, fmt.Sprintf("%s entries %s exist only in the overwritten copy", d.key, strings.Join(lost, ", ")))
			}
		}
		if len(warnings) > 0 {
			it.Warning = "newest wins: " + strings.Join(warnings, "; ") + "; " + hint
		}
	}
	for i := range merges {
		m := &merges[i]
		if m.same || direction != "both" && !m.wouldMove(direction) {
			continue
		}
		it := PeerPlanItem{Path: m.rel, Scope: PlanScopeHost, Hot: true}
		if m.localInfo != nil {
			it.Local = &PlanSide{Size: m.localInfo.Size(), Mtime: m.localInfo.ModTime()}
		}
		if !m.peerMtime.IsZero() {
			it.Peer = &PlanSide{Size: int64(len(m.peerData)), Mtime: m.peerMtime}
		}
		renderHostMerge(&it, m, direction)
		items = append(items, it)
	}
	return items
}

func renderHostMerge(it *PeerPlanItem, m *hostMerge, direction string) {
	for _, d := range diffJSONKeys(m.local, m.peer, m.keys) {
		it.Keys = append(it.Keys, d.String())
	}
	switch {
	case direction != "both":
		it.Action, it.Direction = "update", direction
		it.Reason = "held: host_merge merges it only in a two-way run"
		if m.refused != "" {
			it.Reason += ", where it cannot: " + m.refused
		}
	case m.refused != "":
		it.Action, it.Direction = "conflict", "both"
		it.Reason = "host_merge cannot merge it: " + m.refused + "; the run stops before anything moves"
	default:
		it.Action, it.Direction = "merge", "both"
		it.Reason = "merged on both machines before the transfer (host_merge: " + strings.Join(m.keys, ", ") + ")"
	}
}

// mergePeerHostFiles runs planHostMerges again right before the additive
// pass, so it merges what the plan listed from the copies as they are now:
// each result is written here with a fresh mtime and pushed with it, so the
// pass then finds two equal copies. Both sides are always written: the
// result must not depend on which copy the pass would call newer (the
// peer's mtime is read to the second). Every file a decision covers joins
// the pass's exclusions, written or not. It returns the files it merged.
//
// ponytail: known ceiling. See docs/CEILINGS.md (host_merge read-merge-write race).
func mergePeerHostFiles(ctx context.Context, runner, probe *exec.Runner, cfg *Config) ([]string, error) {
	var merged []string
	// An app saving a file between the read and the write makes that
	// decision stale: decide again from the new copy. Skipping it instead
	// would hand the file to the additive pass, whose newest-wins push then
	// drops the peer-only entries on both machines.
	for attempt := 1; ; attempt++ {
		merges, err := planHostMerges(ctx, probe, cfg)
		if err != nil {
			return merged, err
		}
		if err := hostMergeRefusal(merges, "the host pass did not run; the next two-way run merges it"); err != nil {
			return merged, err
		}
		changed, err := writeHostMerges(ctx, runner, probe, cfg, merges, &merged)
		if err != nil || changed == "" {
			return merged, err
		}
		if attempt == hostMergeAttempts {
			// Stop before the additive pass: the run is partial, the baseline
			// stays, and the next run merges from both copies again.
			return merged, fmt.Errorf("host_merge %s: the file kept changing during the merge (%d attempts); the host pass did not run, the next two-way run merges it", changed, hostMergeAttempts)
		}
	}
}

// pushMergedCopy sends body to rel on the peer with mtime at, from a temp
// file that only this run writes.
func pushMergedCopy(ctx context.Context, runner *exec.Runner, cfg *Config, rel string, body []byte, at time.Time) error {
	dir, err := os.MkdirTemp("", "dot-host-merge-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	tmp := filepath.Join(dir, filepath.Base(rel))
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	if err := os.Chtimes(tmp, at, at); err != nil {
		return err
	}
	args := []string{"-t", "-e", "ssh -o BatchMode=yes -o ConnectTimeout=5"}
	if cfg.RemoteRsyncPath != "" {
		args = append(args, "--rsync-path="+cfg.RemoteRsyncPath)
	}
	args = append(args, tmp, cfg.Target.Host+":"+rel)
	if _, err := runner.Run(ctx, cfg.rsyncBin(), args...); err != nil {
		return fmt.Errorf("host_merge %s: pushing the merged copy: %w", rel, err)
	}
	return nil
}

// hostMergeAttempts bounds how often a file saved during the merge is
// decided again.
const hostMergeAttempts = 3

// writeHostMerges writes each decided merge on both machines, in order. It
// stops at a file changed on either machine since planHostMerges read it
// and names it.
func writeHostMerges(ctx context.Context, runner, probe *exec.Runner, cfg *Config, merges []hostMerge, merged *[]string) (string, error) {
	for _, m := range merges {
		if m.same {
			continue
		}
		localPath := filepath.Join(cfg.HomeDir(), filepath.FromSlash(m.rel))
		if now, err := os.Stat(localPath); err != nil || now.Size() != m.localInfo.Size() || !now.ModTime().Equal(m.localInfo.ModTime()) {
			return m.rel, nil
		}
		// The peer's copy is read again, whole: its mtime has one-second
		// precision there.
		if now, err := readPeerHostFile(ctx, probe, cfg, m.rel); err != nil || !bytes.Equal(now, m.peerData) {
			return m.rel, nil
		}
		body, err := encodeJSONObject(m.result)
		if err != nil {
			return "", err
		}
		// Keeps the mode (~/.claude.json holds tokens, 0600) and owner, and
		// refuses a symlink rather than replacing it.
		if err := runner.WriteFileAtomic(localPath, body, 0o600); err != nil {
			return "", fmt.Errorf("host_merge %s: %w", m.rel, err)
		}
		now := time.Now()
		if err := os.Chtimes(localPath, now, now); err != nil {
			return "", err
		}
		// The merged bytes go to the peer from a private copy: the live file
		// may already hold an app's newer save.
		if err := pushMergedCopy(ctx, runner, cfg, m.rel, body, now); err != nil {
			return "", err
		}
		if !slices.Contains(*merged, m.rel) {
			*merged = append(*merged, m.rel)
		}
	}
	return "", nil
}

func peerHostMtime(ctx context.Context, runner *exec.Runner, cfg *Config, rel string) (time.Time, error) {
	res, err := runner.Run(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", cfg.Target.Host,
		"f="+shellQuote(rel)+"; if stat -c %Y \"$f\" >/dev/null 2>&1; then stat -c %Y \"$f\"; else stat -f %m \"$f\"; fi")
	if err != nil {
		return time.Time{}, fmt.Errorf("reading the mtime of %s on %s: %w", rel, cfg.Target.Host, err)
	}
	var sec int64
	if _, err := fmt.Sscan(strings.TrimSpace(res.Stdout), &sec); err != nil {
		return time.Time{}, fmt.Errorf("unexpected mtime %q for %s", strings.TrimSpace(res.Stdout), rel)
	}
	return time.Unix(sec, 0), nil
}
