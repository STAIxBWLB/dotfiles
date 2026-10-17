package syncer

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// The marker is workspace-wide rather than profile-specific. A workspace has
// one set of names, while each sync profile has its own filters and baseline.
// Keeping it below .dotfiles also keeps the migration state out of the sync
// payload and Git's working tree.
const nfdMigrationMarkerRel = ".dotfiles/nfd-normalized"

const (
	nfdMigrationVersion = "1"
	nfdTempPrefix       = ".dot-nfd-tmp-"
	// A bounded preflight error is easier to read than a terminal-sized dump
	// when a large workspace contains many invalid names or collisions.
	maxNamePreflightDetails = 12
)

// NameRename is one workspace path-component rename in an NFD plan.
// OldPath/NewPath are absolute paths. OldRel/NewRel are workspace-relative
// paths intended for reports and logs.
type NameRename struct {
	OldPath string
	NewPath string
	OldRel  string
	NewRel  string
}

// NameNormalizationPlan is a complete, preflighted set of workspace name
// changes. No filesystem mutation happens while producing a plan.
type NameNormalizationPlan struct {
	WorkspaceRoot string
	Renames       []NameRename
	Skipped       int
}

// NameNormalizationCollision describes two or more sibling names that map to
// the same NFD name.
type NameNormalizationCollision struct {
	Directory string
	Target    string
	Names     []string
}

// NameNormalizationPreflightError reports every invalid name and collision
// found during a scan. Returning an aggregate is important: callers must see
// all blockers before deciding whether to rerun after fixing the workspace.
type NameNormalizationPreflightError struct {
	InvalidUTF8 []string
	Collisions  []NameNormalizationCollision
}

func (e *NameNormalizationPreflightError) Error() string {
	if e == nil {
		return "NFD name normalization preflight failed"
	}
	var parts []string
	if len(e.InvalidUTF8) > 0 {
		parts = append(parts, fmt.Sprintf("%d invalid UTF-8 name(s)%s", len(e.InvalidUTF8), formatNameDetails(e.InvalidUTF8)))
	}
	if len(e.Collisions) > 0 {
		parts = append(parts, fmt.Sprintf("%d NFD collision(s)%s", len(e.Collisions), formatCollisionDetails(e.Collisions)))
	}
	if len(parts) == 0 {
		return "NFD name normalization preflight failed"
	}
	return "NFD name normalization preflight failed: " + strings.Join(parts, "; ")
}

// NameNormalizationResult reports the plan and the number of applied moves.
// Applied is zero for dry-runs and for an already-normalized workspace.
type NameNormalizationResult struct {
	Plan       *NameNormalizationPlan
	Applied    int
	DryRun     bool
	MarkerPath string
}

// NFDMigrationMarkerPath returns the profile-independent workspace marker
// path. Use this instead of constructing the path at call sites.
func NFDMigrationMarkerPath(workspaceRoot string) string {
	return filepath.Join(filepath.Clean(workspaceRoot), filepath.FromSlash(nfdMigrationMarkerRel))
}

// NFDMigrationMarked reports whether the workspace has opted into automatic
// pre-push normalization. A marker symlink is deliberately not trusted.
// During the staged rollout, accept the short-lived profile-store location as
// a read-only compatibility fallback; new markers are written at the
// workspace-wide path above.
func NFDMigrationMarked(workspaceRoot string) bool {
	for _, marker := range nfdMarkerCandidates(workspaceRoot) {
		info, err := os.Lstat(marker)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			continue
		}
		return true
	}
	return false
}

// NFDPathNormalized reports whether every Unicode code point in a relative
// path is valid UTF-8 and already stored in canonical decomposed form. Path
// separators and ASCII bytes are unchanged by NFD, so checking the complete
// relative path is equivalent to checking each component.
func NFDPathNormalized(rel string) bool {
	return utf8.ValidString(rel) && norm.NFD.IsNormalString(rel)
}

// MarkNFDMigration writes the workspace migration marker atomically. It is
// idempotent and refuses a suspicious symlink at the marker path.
func MarkNFDMigration(workspaceRoot string) error {
	root, err := cleanWorkspaceRoot(workspaceRoot)
	if err != nil {
		return err
	}
	marker := NFDMigrationMarkerPath(root)
	if info, err := os.Lstat(marker); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("NFD migration marker %s is not a regular file", marker)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("checking NFD migration marker %s: %w", marker, err)
	}

	dotfiles := filepath.Dir(marker)
	if info, err := os.Lstat(dotfiles); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing NFD migration marker below symlinked directory %s", dotfiles)
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("checking %s: %w", dotfiles, err)
	}
	if err := os.MkdirAll(dotfiles, 0755); err != nil {
		return fmt.Errorf("creating %s: %w", dotfiles, err)
	}
	content := fmt.Sprintf("version: %s\nnormalization: NFD\n", nfdMigrationVersion)
	if err := atomicWrite(marker, []byte(content)); err != nil {
		return fmt.Errorf("writing NFD migration marker: %w", err)
	}
	return nil
}

// PlanWorkspaceNameNormalization performs the complete read-only preflight.
// The sync profile's effective filters are honored, while hard safety paths,
// conflict backups, symlinks, and their subtrees are never traversed.
func PlanWorkspaceNameNormalization(cfg *Config) (*NameNormalizationPlan, error) {
	if cfg == nil {
		return nil, fmt.Errorf("NFD name normalization: nil sync config")
	}
	root, err := configWorkspaceRoot(cfg)
	if err != nil {
		return nil, err
	}

	var filter *syncFilter
	filter, err = newSyncFilter(cfg, strings.TrimRight(cfg.MirrorPath, "/"))
	if err != nil {
		return nil, fmt.Errorf("loading sync filters for NFD normalization: %w", err)
	}

	// The code-owned peer volatile layer is never transferred, so nothing in it
	// needs a normalized name (#268); it is empty outside the peer profile.
	volatile := peerVolatilePatterns(cfg)
	plan := &NameNormalizationPlan{WorkspaceRoot: root}
	var invalid []string
	var collisions []NameNormalizationCollision
	collisionKeys := map[string]struct{}{}
	// Sibling names are collected before filter pruning. An excluded sibling
	// still occupies its normalized destination and must block an overwrite.
	siblings := map[string]map[string][]string{}

	err = filepath.WalkDir(root, func(absPath string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// Gone since the walk listed it: nothing left to rename.
			if absPath != root && errors.Is(walkErr, fs.ErrNotExist) {
				if d != nil && d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			return fmt.Errorf("walking %s: %w", absPath, walkErr)
		}
		if d == nil {
			return fmt.Errorf("walking %s: missing directory entry", absPath)
		}
		if absPath == root {
			return nil
		}

		name := d.Name()
		rel, err := filepath.Rel(root, absPath)
		if err != nil {
			return fmt.Errorf("relativizing %s: %w", absPath, err)
		}
		rel = filepath.ToSlash(rel)
		if !utf8.ValidString(name) {
			invalid = append(invalid, rel)
			// Continue the scan so the error reports every invalid name. There is
			// no safe normalized key for this component.
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		parent := filepath.Dir(absPath)
		normalizedName := norm.NFD.String(name)
		if byTarget, ok := siblings[parent]; ok {
			if existing := byTarget[normalizedName]; len(existing) > 0 {
				duplicate := false
				for _, prior := range existing {
					if prior == name {
						duplicate = true
						break
					}
				}
				if !duplicate {
					sort.Strings(existing)
					names := append(append([]string(nil), existing...), name)
					sort.Strings(names)
					dirRel, _ := filepath.Rel(root, parent)
					dirRel = filepath.ToSlash(dirRel)
					if dirRel == "." {
						dirRel = ""
					}
					key := dirRel + "\x00" + normalizedName
					if _, seen := collisionKeys[key]; !seen {
						collisionKeys[key] = struct{}{}
						collisions = append(collisions, NameNormalizationCollision{
							Directory: dirRel,
							Target:    normalizedName,
							Names:     names,
						})
					}
				}
			}
			byTarget[normalizedName] = append(byTarget[normalizedName], name)
		} else {
			siblings[parent] = map[string][]string{normalizedName: {name}}
		}

		// Never follow or rename links. Checking the DirEntry type before Info
		// also handles dangling links without turning them into walk errors.
		if d.Type()&os.ModeSymlink != 0 {
			plan.Skipped++
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("reading %s: %w", absPath, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			plan.Skipped++
			return nil
		}
		isDir := info.IsDir()

		if isNFDHardExcluded(rel) || matchesAny(volatile, rel, isDir) || filter.shouldSkip(absPath, rel, isDir) {
			plan.Skipped++
			if isDir {
				return filepath.SkipDir
			}
			return nil
		}

		if normalizedName == name {
			return nil
		}
		newAbs := filepath.Join(filepath.Dir(absPath), normalizedName)
		// Report the final workspace-relative spelling, including any parent
		// components that will be renamed later in this deepest-first plan.
		// NewPath intentionally keeps the current parent spelling so the child
		// move can happen before that parent directory moves.
		newRel := filepath.ToSlash(norm.NFD.String(filepath.ToSlash(rel)))
		plan.Renames = append(plan.Renames, NameRename{
			OldPath: absPath,
			NewPath: newAbs,
			OldRel:  rel,
			NewRel:  newRel,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(invalid) > 0 || len(collisions) > 0 {
		sort.Strings(invalid)
		sort.Slice(collisions, func(i, j int) bool {
			if collisions[i].Directory != collisions[j].Directory {
				return collisions[i].Directory < collisions[j].Directory
			}
			return collisions[i].Target < collisions[j].Target
		})
		return nil, &NameNormalizationPreflightError{InvalidUTF8: invalid, Collisions: collisions}
	}

	// Deeper paths must move before their parent directory. For equal-depth
	// entries, lexical order keeps plans stable and output reproducible.
	sort.Slice(plan.Renames, func(i, j int) bool {
		depthI := strings.Count(plan.Renames[i].OldRel, "/")
		depthJ := strings.Count(plan.Renames[j].OldRel, "/")
		if depthI != depthJ {
			return depthI > depthJ
		}
		return plan.Renames[i].OldRel < plan.Renames[j].OldRel
	})
	return plan, nil
}

// NormalizeWorkspaceNames applies a complete NFD plan. A dry run performs the
// same preflight and returns the plan without renaming or writing the marker.
// Any mutation failure rolls back already completed moves in reverse order.
func NormalizeWorkspaceNames(cfg *Config, dryRun bool) (*NameNormalizationResult, error) {
	plan, err := PlanWorkspaceNameNormalization(cfg)
	if err != nil {
		return nil, err
	}
	result := &NameNormalizationResult{
		Plan:       plan,
		DryRun:     dryRun,
		MarkerPath: NFDMigrationMarkerPath(plan.WorkspaceRoot),
	}
	if dryRun {
		return result, nil
	}

	if err := applyNameNormalizationPlan(plan); err != nil {
		return nil, err
	}
	if err := MarkNFDMigration(plan.WorkspaceRoot); err != nil {
		// A marker is the opt-in boundary for automatic normalization. If it
		// cannot be persisted, restore the names so a later run is not left in
		// an ambiguous half-migrated state.
		rollbackErr := rollbackNameRenames(plan.Renames)
		if rollbackErr != nil {
			return nil, fmt.Errorf("writing NFD migration marker: %w (rollback failed: %v)", err, rollbackErr)
		}
		return nil, fmt.Errorf("writing NFD migration marker: %w", err)
	}
	result.Applied = len(plan.Renames)
	return result, nil
}

// NormalizeWorkspaceNamesBeforePush is the narrow integration helper for
// Push/peer callers. Before the staged migration marker exists it detects
// selected non-NFD names and refuses the push with an explicit migration
// command; once opted in, every real push applies newly arrived names.
func NormalizeWorkspaceNamesBeforePush(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("NFD name normalization: nil sync config")
	}
	root, err := configWorkspaceRoot(cfg)
	if err != nil {
		return err
	}
	if NFDMigrationMarked(root) {
		_, err := NormalizeWorkspaceNames(cfg, false)
		return err
	}
	plan, err := PlanWorkspaceNameNormalization(cfg)
	if err != nil {
		return err
	}
	return nfdPushRefusal(false, len(plan.Renames), NormalizeProfile(cfg.Profile))
}

// nfdPushRefusal is the push preflight's verdict from a plan: an unmarked
// workspace with names to rename refuses; a marked one renames them. `dot
// peer doctor` asks it with a Mac's facts (#182).
func nfdPushRefusal(marked bool, renames int, profile string) error {
	if marked || renames == 0 {
		return nil
	}
	return fmt.Errorf(
		"found %d selected filename(s) requiring NFD migration; run `dot sync names normalize --profile=%s --dry-run`, then rerun with `--yes`",
		renames, profile)
}

func applyNameNormalizationPlan(plan *NameNormalizationPlan) error {
	if plan == nil {
		return fmt.Errorf("NFD name normalization: nil plan")
	}
	completed := make([]NameRename, 0, len(plan.Renames))
	usedTemps := map[string]struct{}{}
	for index, rename := range plan.Renames {
		tmp, err := allocateNFDSiblingTemp(rename.OldPath, index, usedTemps)
		if err != nil {
			return rollbackWithCause(completed, fmt.Errorf("allocating temporary name for %s: %w", rename.OldRel, err))
		}
		if err := ensureRenameSource(rename); err != nil {
			return rollbackWithCause(completed, err)
		}
		if err := ensureRenameDestinationFree(rename); err != nil {
			return rollbackWithCause(completed, err)
		}
		if err := os.Rename(rename.OldPath, tmp); err != nil {
			return rollbackWithCause(completed, fmt.Errorf("staging %s -> %s: %w", rename.OldRel, filepath.Base(tmp), err))
		}
		if err := os.Rename(tmp, rename.NewPath); err != nil {
			rollbackErr := os.Rename(tmp, rename.OldPath)
			if rollbackErr != nil {
				return rollbackWithCause(completed, fmt.Errorf("renaming %s -> %s: %w (current rollback failed: %v)", rename.OldRel, rename.NewRel, err, rollbackErr))
			}
			return rollbackWithCause(completed, fmt.Errorf("renaming %s -> %s: %w", rename.OldRel, rename.NewRel, err))
		}
		completed = append(completed, rename)
	}
	return nil
}

func ensureRenameSource(rename NameRename) error {
	info, err := os.Lstat(rename.OldPath)
	if err != nil {
		return fmt.Errorf("NFD normalization source %q disappeared: %w", rename.OldRel, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("NFD normalization source %q became a symlink; refusing", rename.OldRel)
	}
	return nil
}

func ensureRenameDestinationFree(rename NameRename) error {
	src, err := os.Lstat(rename.OldPath)
	if err != nil {
		return fmt.Errorf("checking NFD normalization source %q: %w", rename.OldRel, err)
	}
	dst, err := os.Lstat(rename.NewPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("checking NFD normalization destination %q: %w", rename.NewRel, err)
	}
	// On APFS/HFS+, old and new spellings can resolve to one directory entry.
	// It is exactly the case where the sibling-temp two-step is required.
	if os.SameFile(src, dst) {
		return nil
	}
	return fmt.Errorf("NFD normalization destination collision: %q already exists", rename.NewRel)
}

func allocateNFDSiblingTemp(source string, index int, used map[string]struct{}) (string, error) {
	parent := filepath.Dir(source)
	for attempt := 0; attempt < 1000; attempt++ {
		candidate := filepath.Join(parent, fmt.Sprintf("%s%d-%d", nfdTempPrefix, os.Getpid(), index+attempt))
		if _, ok := used[candidate]; ok {
			continue
		}
		_, err := os.Lstat(candidate)
		if os.IsNotExist(err) {
			used[candidate] = struct{}{}
			return candidate, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("could not allocate a free sibling temporary name for %s", source)
}

func rollbackWithCause(completed []NameRename, cause error) error {
	if rollbackErr := rollbackNameRenames(completed); rollbackErr != nil {
		return fmt.Errorf("%w (rollback failed: %v)", cause, rollbackErr)
	}
	return cause
}

func rollbackNameRenames(renames []NameRename) error {
	var failures []string
	for i := len(renames) - 1; i >= 0; i-- {
		rename := renames[i]
		if err := os.Rename(rename.NewPath, rename.OldPath); err != nil {
			failures = append(failures, fmt.Sprintf("%s -> %s: %v", rename.NewRel, rename.OldRel, err))
		}
	}
	if len(failures) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(failures, "; "))
}

func configWorkspaceRoot(cfg *Config) (string, error) {
	root := ""
	if cfg != nil && cfg.LocalPaths != nil {
		root = cfg.LocalPaths.WorkspaceRoot
	}
	if root == "" && cfg != nil {
		root = cfg.LocalPath
	}
	return cleanWorkspaceRoot(root)
}

func cleanWorkspaceRoot(root string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("NFD name normalization: empty workspace root")
	}
	root = filepath.Clean(root)
	info, err := os.Lstat(root)
	if err != nil {
		return "", fmt.Errorf("checking workspace root %s: %w", root, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("workspace root %s is not a non-symlink directory", root)
	}
	return root, nil
}

func nfdMarkerCandidates(root string) []string {
	root = filepath.Clean(root)
	return []string{
		NFDMigrationMarkerPath(root),
		filepath.Join(root, ".dotfiles", DefaultProfile, "nfd-normalized"),
	}
}

func isNFDHardExcluded(rel string) bool {
	return isAlwaysExcluded(rel) || rel == conflictsDirName || strings.HasPrefix(rel, conflictsDirName+"/")
}

func formatNameDetails(names []string) string {
	limit := len(names)
	if limit > maxNamePreflightDetails {
		limit = maxNamePreflightDetails
	}
	quoted := make([]string, 0, limit)
	for _, name := range names[:limit] {
		quoted = append(quoted, fmt.Sprintf("%q", name))
	}
	if len(names) > limit {
		return ": " + strings.Join(quoted, ", ") + fmt.Sprintf(" (+%d more)", len(names)-limit)
	}
	return ": " + strings.Join(quoted, ", ")
}

func formatCollisionDetails(collisions []NameNormalizationCollision) string {
	limit := len(collisions)
	if limit > maxNamePreflightDetails {
		limit = maxNamePreflightDetails
	}
	details := make([]string, 0, limit)
	for _, collision := range collisions[:limit] {
		dir := collision.Directory
		if dir == "" {
			dir = "."
		}
		details = append(details, fmt.Sprintf("%s/%q <- %s", dir, collision.Target, formatNameList(collision.Names)))
	}
	if len(collisions) > limit {
		return ": " + strings.Join(details, "; ") + fmt.Sprintf(" (+%d more)", len(collisions)-limit)
	}
	return ": " + strings.Join(details, "; ")
}

func formatNameList(names []string) string {
	quoted := make([]string, 0, len(names))
	for _, name := range names {
		quoted = append(quoted, fmt.Sprintf("%q", name))
	}
	return strings.Join(quoted, ", ")
}
