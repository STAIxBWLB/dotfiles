// Package noindex keeps macOS Spotlight out of regenerable build and cache
// directories.
//
// An empty .metadata_never_index file makes mds skip the directory and
// everything under it. Without it a machine with a few hundred node_modules
// trees spends real CPU and disk indexing files nobody will ever search for.
//
// Two shapes of target:
//
//   - project roots are walked, and every matching directory inside them gets
//     its own marker, because they come and go with every install;
//   - cache roots get a single marker at the top, because the whole tree is
//     uninteresting and walking it every few hours to reach the same
//     conclusion would be wasteful.
package noindex

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/STAIxBWLB/dotfiles/internal/clean"
)

// Marker is the filename macOS looks for. The sibling token
// .metadata_never_index_unless_rootfs is a boot-volume escape hatch, not this.
const Marker = ".metadata_never_index"

// walkRootNames are home-relative trees where projects (and therefore
// short-lived build dirs) live.
var walkRootNames = []string{
	"workspace",
	"conductor",
	"CascadeProjects",
	"ZCodeProject",
	"Sites",
	"orca",
	"Fabric",
	".claude-worktrees",
}

// cacheRootNames are home-relative tool and cache trees that get one marker at
// the root instead of a walk. ~/.claude is deliberately absent: plans, skills
// and notes there are worth finding in Spotlight.
var cacheRootNames = []string{
	".local",
	".npm",
	".cache",
	".cursor",
	".vscode",
	".hermes",
	".antigravity",
	".nvm",
	".maru",
	"node_modules_store",
	filepath.Join("Library", "Caches"),
}

// DefaultWalkRoots returns the project trees to sweep, dropping any that do
// not exist on this machine.
func DefaultWalkRoots(home string) []string {
	return existingDirs(home, walkRootNames)
}

// DefaultCacheRoots returns the tool/cache trees to stamp at the top.
func DefaultCacheRoots(home string) []string {
	return existingDirs(home, cacheRootNames)
}

func existingDirs(home string, names []string) []string {
	var out []string
	for _, n := range names {
		p := filepath.Join(home, n)
		if isDir(p) {
			out = append(out, p)
		}
	}
	return out
}

// keepIndexed are directory names clean.DefaultPatterns lists but Spotlight
// should still see.
//
// "Regenerable" and "not worth searching" are not the same property. build/ and
// out/ are where finished deliverables land (rendered decks: PDF, PPTX, HTML;
// exported JSON), and a deck you cannot find by name costs more than the
// indexing it saves. dist/ and target/ stay excluded: their contents are
// compiled or bundled from sources that are indexed anyway.
var keepIndexed = map[string]bool{
	"build": true,
	"out":   true,
}

// dirPatterns is clean.DefaultPatterns filtered to directories, minus
// keepIndexed.
//
// Both commands ask a version of the same question, so they share one list
// rather than drifting apart. Note the coupling: adding a name to
// clean.DefaultPatterns also makes it deletable by `dot clean`.
//
// clean gates dist/target behind --all because deleting them can cost a
// rebuild. Marking them is free, so risk is ignored here.
func dirPatterns() map[string]clean.Pattern {
	m := make(map[string]clean.Pattern, len(clean.DefaultPatterns))
	for _, p := range clean.DefaultPatterns {
		if p.Kind == clean.KindDirectory && !keepIndexed[p.Name] {
			m[p.Name] = p
		}
	}
	return m
}

// Options configures a sweep.
type Options struct {
	WalkRoots  []string
	CacheRoots []string
	DryRun     bool
}

// Result reports what a sweep did.
type Result struct {
	Marked  []string // directories that got (or with DryRun, would get) a marker
	Present int      // directories that already had one
	Failed  []string // directories whose marker could not be written
}

// Sweep stamps the cache roots and walks the project roots.
func Sweep(opts Options) *Result {
	res := &Result{}

	for _, root := range opts.CacheRoots {
		if isDir(root) {
			res.mark(root, opts.DryRun)
		}
	}

	pats := dirPatterns()
	for _, root := range opts.WalkRoots {
		if !isDir(root) {
			continue
		}
		// WalkDir lstats the root, so a symlinked root (which `dot apply` will
		// happily create for ~/workspace) would be reported as a non-dir and
		// skip the whole tree. Resolve the root only; nested links still stop
		// the walk, which is what keeps it inside the tree.
		if resolved, err := filepath.EvalSymlinks(root); err == nil {
			root = resolved
		}
		// Callback errors are swallowed below, so WalkDir only fails on a root
		// we already know is readable.
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			// WalkDir reports a symlinked directory as a non-dir entry, so this
			// also stops the walk from following links out of the root.
			if !d.IsDir() {
				return nil
			}
			name := d.Name()
			if name == ".git" {
				return fs.SkipDir
			}
			p, ok := pats[name]
			if !ok {
				return nil
			}
			// env/ is too generic to trust on the name alone.
			if p.NeedProbe {
				if _, err := os.Stat(filepath.Join(path, "pyvenv.cfg")); err != nil {
					return nil
				}
			}
			res.mark(path, opts.DryRun)
			return fs.SkipDir
		})
	}
	return res
}

// mark creates the marker unless it is already there. A directory we cannot
// write to is recorded in Failed rather than failing the sweep: one read-only
// tree should not stop the other few hundred, but a silent skip is
// indistinguishable from success.
func (r *Result) mark(dir string, dryRun bool) {
	marker := filepath.Join(dir, Marker)
	if _, err := os.Lstat(marker); err == nil {
		r.Present++
		return
	}
	if dryRun {
		r.Marked = append(r.Marked, dir)
		return
	}
	f, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	switch {
	// A concurrent sweep (scheduled run overlapping a manual one) got there
	// between the Lstat and here. Already marked, not a failure.
	case errors.Is(err, fs.ErrExist):
		r.Present++
	case err != nil:
		r.Failed = append(r.Failed, dir)
	default:
		_ = f.Close()
		r.Marked = append(r.Marked, dir)
	}
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
