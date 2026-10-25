package syncer

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

// Dropbox cannot store a file or folder whose name ends with a space (or a
// period: both are Windows-incompatible name endings it refuses). On upload it
// renames the entry in place to "<name> (Unicode Encoding Conflict)", and that
// renamed twin is mirror-only and absent from the baseline, so the next push
// plan reports conflicts and a clean push refuses in a loop. Catching these
// names before upload — in the plan and in the rsync filter — is what breaks
// that loop. The Go predicate and the rsync patterns below are twins and must
// stay side by side.

// UnsupportedPathName reports whether any slash-separated segment of a
// sync-set relative path ends with a space or a period. A trailing dot is
// treated the same as a trailing space: both are Windows-incompatible name
// endings Dropbox refuses to store.
func UnsupportedPathName(rel string) bool {
	for _, segment := range strings.Split(rel, "/") {
		if strings.HasSuffix(segment, " ") || strings.HasSuffix(segment, ".") {
			return true
		}
	}
	return false
}

// unsupportedNameExcludeArgs renders the rsync twin of UnsupportedPathName
// for the push transfer. A pattern without a slash matches the leaf name at
// any depth, and excluding a directory leaf prunes its whole subtree, so
// these two rules also cover files parked under a trailing-space directory.
// SSH peer targets are unaffected: the peer runs its own baseline-scoped plan
// and must not silently drop names the other machine can hold.
func unsupportedNameExcludeArgs(cfg *Config) []string {
	if cfg == nil || cfg.Target.IsSSH() {
		return nil
	}
	return []string{"--exclude=* ", "--exclude=*."}
}

// CountUnsupportedNames is the status-side probe: a name-only walk of the
// local tree honoring the same skip rules as the plan walk (sync filter, hard
// excludes, symlinks, drive metadata), without paying for fingerprints. Files
// and directories both count — an empty trailing-space directory cannot be
// stored either — and every descendant of an unsupported directory counts on
// its own, matching how the plan flags each sync-set path individually.
func CountUnsupportedNames(cfg *Config) (int, error) {
	if cfg == nil {
		return 0, fmt.Errorf("unsupported name count: nil sync config")
	}
	local := strings.TrimRight(cfg.LocalPath, "/")
	if local == "" {
		return 0, nil
	}
	filter, err := newSyncFilter(cfg, strings.TrimRight(cfg.MirrorPath, "/"))
	if err != nil {
		return 0, fmt.Errorf("loading filters: %w", err)
	}
	count := 0
	err = filepath.WalkDir(local, func(absPath string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if absPath == local {
				return walkErr
			}
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if absPath == local {
			return nil
		}
		rel, err := filepath.Rel(local, absPath)
		if err != nil {
			return err
		}
		rel = normalizeRel(rel)
		isDir := d.IsDir()
		if filter.shouldSkip(absPath, rel, isDir) {
			if isDir {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if !isDir && isDriveMetadata(rel) {
			return nil
		}
		if UnsupportedPathName(rel) {
			count++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}

// MoveMirrorLeftovers moves plan leftovers (mirror-only paths with a name
// Dropbox/Windows cannot store, which the baseline proves the workspace put
// there) into the workspace's .sync-conflicts/<ts>/from-mirror/ and returns
// that directory. The workspace no longer has them, so this is a workspace
// deletion that rsync cannot carry out: its exclude shields the name from
// --delete. The backup sits outside the mirror so it does not keep an
// unstorable name in the provider folder. Leftovers have their own max_delete
// budget, apart from rsync's deletes (#225).
//
// It runs after the push refreshed the baseline, which keeps a leftover only
// while its mirror copy still matches and is not a placeholder. A leftover the
// refresh dropped (edited in the cloud or evicted during the run) stays put.
// It returns the backup directory and how many leftovers it moved.
func MoveMirrorLeftovers(cfg *Config, rels []string) (string, int, error) {
	local := strings.TrimRight(cfg.LocalPath, "/")
	var baseline map[string]Fingerprint
	if len(rels) > 0 && cfg.LocalPaths != nil {
		var err error
		if baseline, err = LoadBaselineManifest(cfg.LocalPaths.BaselineFile); err != nil {
			return "", 0, fmt.Errorf("loading baseline: %w", err)
		}
	}
	rels = slices.DeleteFunc(slices.Clone(rels), func(rel string) bool {
		// The workspace has it now: a rename undone during the push, or the
		// same name in another Unicode form the plan's byte match missed.
		// Only a path proven absent moves; an unreadable one stays.
		if _, err := os.Lstat(filepath.Join(local, rel)); !errors.Is(err, fs.ErrNotExist) {
			return true
		}
		_, ok := baseline[rel]
		return baseline != nil && !ok
	})
	return moveOutOfMirror(cfg, rels, baseline, "mirror leftover")
}

// MoveShadowedMirrorFiles moves mirror files whose workspace path is now a
// symlink into the workspace's .sync-conflicts/<ts>/from-mirror/, as
// MoveMirrorLeftovers does for leftovers (#298). A proven path moves only
// while the refreshed baseline still lists it, so a copy edited in the cloud
// during the run stays. An unproven one is a conflict the operator pushed
// past, so it moves without that check unless it is a cloud placeholder.
// Either moves only while the workspace path is still a symlink.
func MoveShadowedMirrorFiles(cfg *Config, proven, unproven []string) (string, int, error) {
	if len(proven)+len(unproven) == 0 {
		return "", 0, nil
	}
	local := strings.TrimRight(cfg.LocalPath, "/")
	var baseline map[string]Fingerprint
	if cfg.LocalPaths != nil {
		var err error
		if baseline, err = LoadBaselineManifest(cfg.LocalPaths.BaselineFile); err != nil {
			return "", 0, fmt.Errorf("loading baseline: %w", err)
		}
	}
	stillSymlink := func(rel string) bool {
		info, err := os.Lstat(filepath.Join(local, rel))
		return err == nil && info.Mode()&os.ModeSymlink != 0
	}
	var rels []string
	for _, rel := range proven {
		if _, ok := baseline[rel]; stillSymlink(rel) && (baseline == nil || ok) {
			rels = append(rels, rel)
		}
	}
	mirror := strings.TrimRight(cfg.MirrorPath, "/")
	for _, rel := range unproven {
		// A cloud placeholder holds no local content to back up, so it stays
		// a conflict, as a placeholder leftover does.
		abs := filepath.Join(mirror, rel)
		if info, err := os.Lstat(abs); err != nil || dehydratedFile(abs, info) {
			continue
		}
		if stillSymlink(rel) {
			rels = append(rels, rel)
		}
	}
	return moveOutOfMirror(cfg, rels, baseline, "shadowed mirror file")
}

// moveOutOfMirror moves rels from the mirror into a fresh
// .sync-conflicts/<ts>/from-mirror/ in the workspace and drops their baseline
// rows. what names the paths in errors.
func moveOutOfMirror(cfg *Config, rels []string, baseline map[string]Fingerprint, what string) (string, int, error) {
	if len(rels) == 0 {
		return "", 0, nil
	}
	if len(rels) > cfg.MaxDelete {
		return "", 0, fmt.Errorf("%d %s(s) exceed max_delete %d; raise max_delete or move them out of %s by hand", len(rels), what, cfg.MaxDelete, cfg.MirrorPath)
	}
	local := strings.TrimRight(cfg.LocalPath, "/")
	mirror := strings.TrimRight(cfg.MirrorPath, "/")
	backupRel := NewConflictDir().LeftoverBackupRel()
	backup := filepath.Join(local, backupRel)
	moved := 0
	var moveErr error
	for _, rel := range rels {
		dst := filepath.Join(backup, rel)
		// Each directory is created and checked in turn, as for the peer
		// quarantine: a symlinked .sync-conflicts must not carry the backup
		// out of the workspace, or back into the provider folder.
		dir := local
		for _, part := range strings.Split(filepath.Join(backupRel, filepath.Dir(rel)), string(filepath.Separator)) {
			dir = filepath.Join(dir, part)
			if err := ensurePeerLocalDirectory(dir); err != nil {
				moveErr = fmt.Errorf("moving %s %s into %s: %w", what, rel, backup, err)
				break
			}
		}
		if moveErr != nil {
			break
		}
		if _, err := os.Lstat(dst); err == nil {
			// Two names one filesystem folds together (Unicode
			// normalization): a rename would replace the first backup.
			moveErr = fmt.Errorf("moving %s %s: %s already holds a backup", what, rel, dst)
			break
		}
		if err := moveFile(filepath.Join(mirror, rel), dst); err != nil {
			moveErr = fmt.Errorf("moving %s %s into %s: %w", what, rel, backup, err)
			break
		}
		pruneUnsupportedDirs(mirror, filepath.Dir(rel))
		moved++
	}
	// The push refreshed the baseline before this move, so it still lists
	// what just left the mirror; a pull would read those as mirror deletions.
	if baseline != nil && moved > 0 {
		for _, rel := range rels[:moved] {
			delete(baseline, rel)
		}
		if err := SaveBaselineManifest(cfg.LocalPaths.BaselineFile, baseline); err != nil && moveErr == nil {
			moveErr = fmt.Errorf("saving baseline after moving %d %s(s) into %s: %w", moved, what, backup, err)
		}
	}
	return backup, moved, moveErr
}

// moveFile renames src to dst, copying then removing when they are on
// different volumes.
func moveFile(src, dst string) error {
	err := os.Rename(src, dst)
	if !errors.Is(err, syscall.EXDEV) {
		return err
	}
	if err := copyFilePreservingMtime(src, dst); err != nil {
		return err
	}
	return os.Remove(src)
}

// pruneUnsupportedDirs removes rel and its parents while each is an empty
// directory inside a name Dropbox cannot store (old./sub/ as well as old./).
// Finder's .DS_Store does not keep one alive.
func pruneUnsupportedDirs(mirror, rel string) {
	for rel != "." && rel != "" && UnsupportedPathName(rel) {
		abs := filepath.Join(mirror, rel)
		entries, err := os.ReadDir(abs)
		if err != nil {
			return
		}
		if len(entries) == 1 && entries[0].Name() == ".DS_Store" {
			_ = os.Remove(filepath.Join(abs, ".DS_Store"))
		} else if len(entries) > 0 {
			return
		}
		if os.Remove(abs) != nil {
			return
		}
		rel = filepath.Dir(rel)
	}
}
