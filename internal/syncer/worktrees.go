package syncer

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Linked worktrees are branch checkouts whose content travels through Git,
// never through peer sync (#135 policy: uncommitted work inside one never
// travels). Detection is by content, not by path: a directory whose .git is a
// FILE pointing at a gitdir that contains a commondir file is a linked
// worktree. A submodule's own gitdir has no commondir, so submodules keep
// syncing. The static worktreeRoots list stays as the floor; this detects the
// worktrees that list cannot name, at any depth, including worktrees of
// submodules.
//
// The exclude is a sticky union of the local detected list, the remote
// reported list and the stored previous list, applied identically to the
// local walk, the remote inventory and every transfer. A rule only one side
// applies becomes a propagated delete (#135: 6,867 deletes, max_delete
// stall), so entries are never silently dropped when a worktree disappears.

// DetectLinkedWorktrees walks root and returns the sorted workspace-relative
// paths of every linked-worktree root. Detected worktrees are not descended
// into (their content is not scanned), and .git directories are pruned. A
// root that does not exist yields an empty list — there is nothing to scan
// and nothing to sync; an unreadable subtree inside an existing root is an
// error, so callers can fail closed instead of trusting an incomplete list.
func DetectLinkedWorktrees(root string) ([]string, error) {
	return detectLinkedWorktrees(root, nil, "")
}

// detectLinkedWorktrees is DetectLinkedWorktrees for a peer run (#268). skip
// names directories the transfer excludes: they are pruned before they are
// read, so an unreadable or churning excluded tree cannot fail the run. A
// directory that vanishes after the walk listed it counts as gone. An
// unreadable included directory still fails closed, naming the path and the
// fix; ignoreFile is the exclude file the message points at.
func detectLinkedWorktrees(root string, skip func(rel string) bool, ignoreFile string) ([]string, error) {
	root = strings.TrimRight(root, "/")
	var found []string
	err := filepath.WalkDir(root, func(abs string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if abs == root {
				if os.IsNotExist(walkErr) {
					return nil
				}
				return walkErr
			}
			if errors.Is(walkErr, fs.ErrNotExist) {
				if d != nil && d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if errors.Is(walkErr, fs.ErrPermission) {
				where := "the peer ignore.txt"
				if ignoreFile != "" {
					where = ignoreFile
				}
				return fmt.Errorf("cannot read %s: %w; fix its permissions (chmod u+rx), or exclude it from peer sync in %s", abs, walkErr, where)
			}
			return walkErr
		}
		if !d.IsDir() || abs == root {
			return nil
		}
		if d.Name() == ".git" {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil {
			return err
		}
		rel = normalizeRel(rel)
		if skip != nil && skip(rel) {
			return filepath.SkipDir
		}
		if isLinkedWorktreeDir(abs) {
			if rel != "" {
				found = append(found, rel)
			}
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("detecting linked worktrees under %s: %w", root, err)
	}
	sort.Strings(found)
	return found, nil
}

// isLinkedWorktreeDir reports whether dir is a linked worktree checkout: its
// .git is a file (a real repo has a .git directory) whose gitdir target
// carries a commondir file. Only linked worktrees have one — a submodule's
// gitdir under .git/modules does not, and neither does the main checkout.
func isLinkedWorktreeDir(dir string) bool {
	link, err := os.ReadFile(filepath.Join(dir, ".git"))
	if err != nil {
		return false
	}
	target, ok := strings.CutPrefix(strings.TrimSpace(string(link)), "gitdir: ")
	if !ok || target == "" {
		return false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(dir, target)
	}
	info, err := os.Stat(filepath.Join(target, "commondir"))
	return err == nil && info.Mode().IsRegular()
}

// LoadPeerWorktrees reads the stored sticky worktree list. A missing file is
// an empty list, not an error. Blank lines and comments are skipped.
func LoadPeerWorktrees(path string) ([]string, error) {
	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("loading peer worktrees: %w", err)
	}
	var rels []string
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rels = append(rels, line)
	}
	return rels, nil
}

// SavePeerWorktrees writes the sticky worktree list to the peer store. The
// file is machine-local state (the managed .gitignore ignores every
// non-default profile store), so the two machines' lists converge only
// through the remote list exchanged each run.
func SavePeerWorktrees(path string, rels []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating peer store: %w", err)
	}
	var b strings.Builder
	b.WriteString("# Auto-generated by `dot peer sync` — do not edit.\n")
	b.WriteString("# Linked worktrees excluded from peer sync. The list is sticky:\n")
	b.WriteString("# entries are kept even after the worktree disappears, so both\n")
	b.WriteString("# machines apply the identical exclude. Remove a line by hand only\n")
	b.WriteString("# when the path should sync again on BOTH machines.\n")
	for _, rel := range rels {
		fmt.Fprintf(&b, "%s\n", rel)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("writing peer worktrees: %w", err)
	}
	return nil
}

// MergePeerWorktrees computes the effective worktree exclude set for one
// peer run: the stored previous list ∪ the local detected list ∪ the remote
// reported list. The union is sticky — an entry is never dropped because a
// worktree vanished — and persist writes it back to the store. A preview
// (persist=false) changes nothing in the peer store.
//
// Entries that fail relpath validation are dropped rather than aborting the
// run: the remote list is cross-machine input and a single corrupt path must
// not stop every sync.
func MergePeerWorktrees(cfg *Config, remote []string, persist bool) ([]string, error) {
	if cfg == nil || cfg.LocalPaths == nil {
		return nil, fmt.Errorf("peer worktrees: local paths unresolved")
	}
	seen := map[string]bool{}
	stored, err := LoadPeerWorktrees(cfg.LocalPaths.WorktreesFile)
	if err != nil {
		return nil, err
	}
	for _, rel := range stored {
		if validateTombstoneRel(rel) == nil {
			seen[normalizeRel(rel)] = true
		}
	}
	for _, rel := range remote {
		rel = normalizeRel(rel)
		if rel != "" && validateTombstoneRel(rel) == nil {
			seen[rel] = true
		}
	}
	// Stored and peer-reported roots are excluded from every transfer, so the
	// scan does not read them either.
	detected, err := detectPeerWorktrees(cfg, cfg.LocalPaths.WorkspaceRoot, seen)
	if err != nil {
		return nil, err
	}
	for _, rel := range detected {
		// A local worktree whose path cannot be represented exactly in the
		// line-oriented store and rsync filter must stop the run, loudly:
		// silently dropping it would sync the worktree as regular files.
		if err := validateTombstoneRel(rel); err != nil || strings.ContainsAny(rel, "\r\n") {
			return nil, fmt.Errorf("local linked worktree path %q cannot be represented in the peer worktree store; rename it and realign the store by hand", rel)
		}
		seen[rel] = true
	}
	out := make([]string, 0, len(seen))
	for rel := range seen {
		out = append(out, rel)
	}
	sort.Strings(out)
	if persist {
		if err := SavePeerWorktrees(cfg.LocalPaths.WorktreesFile, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// MaterializeWorktreesDynFile writes the linked-worktree exclude layer to
// <dir>/worktrees.dyn.conf, one anchored exclude per worktree root. dir is
// the store directory for a real run and a temp directory for a preview,
// matching the other runtime filter layers.
func MaterializeWorktreesDynFile(dir string, rels []string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating store dir: %w", err)
	}
	out := filepath.Join(dir, localWorktreesDynName)
	var b strings.Builder
	b.WriteString("# Auto-generated by `dot peer sync` — do not edit.\n")
	b.WriteString("# Linked Git worktrees are excluded from peer sync: they are branch\n")
	b.WriteString("# checkouts whose content travels through Git.\n")
	for _, rel := range rels {
		pattern, err := literalRsyncPattern(rel)
		if err != nil {
			return "", fmt.Errorf("writing worktree excludes: %w", err)
		}
		fmt.Fprintf(&b, "/%s\n", pattern)
		fmt.Fprintf(&b, "/%s/\n", pattern)
	}
	if err := os.WriteFile(out, []byte(b.String()), 0o644); err != nil {
		return "", fmt.Errorf("writing %s: %w", out, err)
	}
	return out, nil
}

// DetectPeerWorktrees is the scan behind the worktree list the other Mac
// reports in its status (#268). The coordinator applies its own filters to
// the transfer, so this side prunes only the code-owned layers both share,
// never its editable ignore.txt: a worktree hidden by a local-only rule would
// go unreported and its content would travel as regular files.
func DetectPeerWorktrees(cfg *Config, root string) ([]string, error) {
	volatile := peerVolatilePatterns(cfg)
	return detectLinkedWorktrees(root, func(rel string) bool {
		return isAlwaysExcluded(rel) || matchesAny(volatile, rel)
	}, cfg.IgnoreFile)
}

// detectPeerWorktrees also prunes the already known worktree roots.
func detectPeerWorktrees(cfg *Config, root string, known map[string]bool) ([]string, error) {
	skip, err := peerScanSkip(cfg)
	if err != nil {
		return nil, err
	}
	return detectLinkedWorktrees(root, func(rel string) bool { return known[rel] || skip(rel) }, cfg.IgnoreFile)
}

// peerScanSkip reports the directories a peer transfer excludes: the Go twin
// of the filter chain plus the code-owned volatile layer that precedes it.
// The worktree scan prunes them, so it never reads a tree nothing will sync.
func peerScanSkip(cfg *Config) (func(rel string) bool, error) {
	filter, err := newSyncFilter(cfg, strings.TrimRight(cfg.MirrorPath, "/"))
	if err != nil {
		return nil, fmt.Errorf("loading peer filters: %w", err)
	}
	volatile := peerVolatilePatterns(cfg)
	return func(rel string) bool {
		return matchesAny(volatile, rel) || filter.shouldSkip("", rel, true)
	}, nil
}

// peerVolatilePatterns is the code-owned volatile exclude layer as patterns.
func peerVolatilePatterns(cfg *Config) []excludePattern {
	var out []excludePattern
	for _, arg := range peerVolatileExcludeArgs(cfg) {
		out = append(out, excludePattern{raw: strings.TrimPrefix(arg, "--exclude=")})
	}
	return out
}

func matchesAny(patterns []excludePattern, dir string) bool {
	for _, p := range patterns {
		if p.matches(dir, true) {
			return true
		}
	}
	return false
}
