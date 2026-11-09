package syncer

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// PullOptions controls one tracked pull run.
type PullOptions struct {
	DryRun bool
	Force  bool
	// Strict forces sha256 fingerprints for every baseline entry instead
	// of the tiered fast-scan/strict-fallback comparison, catching content
	// changes that preserve size+mtime.
	Strict bool
}

// PullConflict captures a baseline-tracked file that changed on both sides.
type PullConflict struct {
	RelPath    string
	LocalPath  string
	MirrorPath string
	BackupPath string
	Reason     string
}

// PullResult summarizes one tracked pull run.
type PullResult struct {
	Pulled        []string
	Restored      []string
	LocalModified []string
	SkippedBase   []string
	Tombstones    []Tombstone
	Conflicts     []PullConflict
	DryRun        bool
}

// HasChanges reports whether the run did anything worth recording in state.
func (r *PullResult) HasChanges() bool {
	if r == nil {
		return false
	}
	return len(r.Pulled) > 0 || len(r.Restored) > 0 || len(r.Tombstones) > 0 || len(r.Conflicts) > 0
}

// PullTracked applies Drive-side changes only for paths already present in
// baseline.manifest. Baseline is the Git-shared Drive payload index; files not
// listed there are deliberately left for Intake to stage under inbox/gdrive.
func PullTracked(cfg *Config, opts PullOptions) (*PullResult, error) {
	if cfg.LocalPaths == nil {
		return nil, fmt.Errorf("pull: local paths unresolved")
	}
	if cfg.Target.IsSSH() {
		return nil, fmt.Errorf("baseline-tracked pull requires a local target; use the direct rsync pull for ssh targets")
	}
	if err := refuseSharedDriveMirror(cfg); err != nil {
		return nil, err
	}
	if err := refuseRetiredDestinationChange(cfg); err != nil {
		return nil, err
	}

	paths := cfg.LocalPaths
	local := strings.TrimRight(cfg.LocalPath, "/")
	mirror := strings.TrimRight(cfg.MirrorPath, "/")

	baseline, err := LoadBaselineManifest(paths.BaselineFile)
	if err != nil {
		return nil, fmt.Errorf("loading baseline: %w", err)
	}
	filter, err := newSyncFilter(cfg, mirror)
	if err != nil {
		return nil, fmt.Errorf("loading filters: %w", err)
	}
	tracked := gitTrackedRelPaths(local)

	now := time.Now().UTC()
	conflict := NewConflictDir()
	if opts.Force {
		// Only a forced pull backs up the local copy it overwrites.
		if err := refuseUnsafeBackupDir(local, conflict.PullLocalBackupRel()); err != nil {
			return nil, fmt.Errorf("pull: %w", err)
		}
	}
	result := &PullResult{DryRun: opts.DryRun}
	nextBaseline := make(map[string]Fingerprint, len(baseline))
	for rel, fp := range baseline {
		nextBaseline[rel] = fp
	}

	rels := make([]string, 0, len(baseline))
	for rel := range baseline {
		rels = append(rels, rel)
	}
	sort.Strings(rels)

	baselineChanged := false
	for _, rel := range rels {
		rel = normalizeRel(rel)
		if rel == "" || tracked[rel] {
			continue
		}
		base := baseline[rel]
		mirrorAbs := filepath.Join(mirror, rel)
		localAbs := filepath.Join(local, rel)

		mirrorInfo, err := os.Lstat(mirrorAbs)
		if errors.Is(err, fs.ErrNotExist) {
			result.Tombstones = append(result.Tombstones, Tombstone{
				RelPath: rel, BaselineFP: base, DetectedAt: now,
			})
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("stat mirror %s: %w", rel, err)
		}
		if shouldSkipPullCandidate(filter, mirrorAbs, rel, mirrorInfo) {
			continue
		}

		mirrorMatchesBase, mirrorFP, err := baselineMatchTiered(base, mirrorAbs, mirrorInfo, opts.Strict)
		if err != nil {
			return nil, fmt.Errorf("fingerprint mirror %s: %w", rel, err)
		}

		localInfo, err := os.Lstat(localAbs)
		localMissing := errors.Is(err, fs.ErrNotExist)
		if err != nil && !localMissing {
			return nil, fmt.Errorf("stat local %s: %w", rel, err)
		}
		if localMissing {
			if cfg.Propagation.Delete && UnsupportedPathName(rel) {
				// A name the push never sends that the workspace renamed
				// away: its proven mirror copy is a leftover the push moves
				// out. Restoring it would undo the rename for good (#225).
				continue
			}
			if err := pullCopy(mirrorAbs, localAbs, opts.DryRun); err != nil {
				return nil, fmt.Errorf("restore %s: %w", rel, err)
			}
			result.Restored = append(result.Restored, rel)
			result.Pulled = append(result.Pulled, rel)
			if mirrorFP, err = ensureStrictFingerprint(mirrorFP, mirrorAbs); err != nil {
				return nil, fmt.Errorf("fingerprint mirror %s: %w", rel, err)
			}
			if needsBaselineUpdate(base, mirrorFP) {
				nextBaseline[rel] = mirrorFP
				baselineChanged = true
			}
			continue
		}
		if localInfo.IsDir() || localInfo.Mode()&os.ModeSymlink != 0 {
			result.Conflicts = append(result.Conflicts, PullConflict{
				RelPath: rel, LocalPath: localAbs, MirrorPath: mirrorAbs,
				Reason: "local path is a directory or symlink; no backup was created",
			})
			continue
		}

		localMatchesBase, localFP, err := baselineMatchTiered(base, localAbs, localInfo, opts.Strict)
		if err != nil {
			return nil, fmt.Errorf("fingerprint local %s: %w", rel, err)
		}

		// When the mirror changed and the local file is about to be
		// overwritten, a fast-tier local match is not enough proof it is
		// pristine — a local edit that preserves size+mtime would be
		// silently destroyed. Re-verify with a hash (one extra hash only
		// on files about to be pulled over); on mismatch the case falls
		// into the conflict branch below, same as strict mode.
		if !mirrorMatchesBase && localMatchesBase && base.Sha != "" && localFP.Sha == "" {
			if localFP, err = ensureStrictFingerprint(localFP, localAbs); err != nil {
				return nil, fmt.Errorf("fingerprint local %s: %w", rel, err)
			}
			localMatchesBase = base.Sha == localFP.Sha
		}

		switch {
		case mirrorMatchesBase && localMatchesBase:
			if needsBaselineUpdate(base, mirrorFP) {
				nextBaseline[rel] = mirrorFP
				baselineChanged = true
			}
			result.SkippedBase = append(result.SkippedBase, rel)
		case mirrorMatchesBase && !localMatchesBase:
			result.LocalModified = append(result.LocalModified, rel)
		case !mirrorMatchesBase && localMatchesBase:
			if err := pullCopy(mirrorAbs, localAbs, opts.DryRun); err != nil {
				return nil, fmt.Errorf("pull %s: %w", rel, err)
			}
			result.Pulled = append(result.Pulled, rel)
			if mirrorFP, err = ensureStrictFingerprint(mirrorFP, mirrorAbs); err != nil {
				return nil, fmt.Errorf("fingerprint mirror %s: %w", rel, err)
			}
			nextBaseline[rel] = mirrorFP
			baselineChanged = true
		default:
			// Neither side matches baseline. Escalate both fingerprints to
			// strict before deciding adopt-vs-conflict — sha-equal files
			// with drifted mtimes must not be misread as a conflict.
			if localFP, err = ensureStrictFingerprint(localFP, localAbs); err != nil {
				return nil, fmt.Errorf("fingerprint local %s: %w", rel, err)
			}
			if mirrorFP, err = ensureStrictFingerprint(mirrorFP, mirrorAbs); err != nil {
				return nil, fmt.Errorf("fingerprint mirror %s: %w", rel, err)
			}
			if fingerprintsSame(localFP, mirrorFP) {
				nextBaseline[rel] = mirrorFP
				baselineChanged = true
				result.SkippedBase = append(result.SkippedBase, rel)
				continue
			}
			backup := ""
			if opts.Force {
				backup = plannedPullLocalBackup(local, rel, conflict)
				if err := backupLocalBeforePull(localAbs, backup, opts.DryRun); err != nil {
					return nil, fmt.Errorf("backup local %s: %w", rel, err)
				}
				if err := pullCopy(mirrorAbs, localAbs, opts.DryRun); err != nil {
					return nil, fmt.Errorf("force pull %s: %w", rel, err)
				}
				result.Pulled = append(result.Pulled, rel)
				nextBaseline[rel] = mirrorFP
				baselineChanged = true
			}
			result.Conflicts = append(result.Conflicts, PullConflict{
				RelPath: rel, LocalPath: localAbs, MirrorPath: mirrorAbs, BackupPath: backup,
				Reason: "local and mirror both changed after baseline",
			})
		}
	}

	sort.Strings(result.Pulled)
	sort.Strings(result.Restored)
	sort.Strings(result.LocalModified)
	sort.Strings(result.SkippedBase)
	sort.Slice(result.Tombstones, func(i, j int) bool { return result.Tombstones[i].RelPath < result.Tombstones[j].RelPath })
	sort.Slice(result.Conflicts, func(i, j int) bool { return result.Conflicts[i].RelPath < result.Conflicts[j].RelPath })

	if opts.DryRun {
		return result, nil
	}
	if baselineChanged {
		if err := SaveBaselineManifest(paths.BaselineFile, nextBaseline); err != nil {
			return nil, fmt.Errorf("saving baseline: %w", err)
		}
	}
	if len(result.Tombstones) > 0 {
		if err := AppendTombstones(paths.TombstonesFile, result.Tombstones); err != nil {
			return nil, fmt.Errorf("appending tombstones: %w", err)
		}
	}
	if result.HasChanges() || baselineChanged {
		if err := UpdateLocalState(paths, func(s *LocalState) {
			s.LastPull = now
		}); err != nil {
			return nil, fmt.Errorf("updating local state: %w", err)
		}
	}
	return result, nil
}

func shouldSkipPullCandidate(filter *syncFilter, abs, rel string, info os.FileInfo) bool {
	if info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return true
	}
	if isDriveMetadata(rel) {
		return true
	}
	return filter.shouldSkip(abs, rel, false)
}

func pullCopy(src, dst string, dryRun bool) error {
	if dryRun {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return copyFilePreservingMtime(src, dst)
}

func plannedPullLocalBackup(localRoot, rel string, conflict *ConflictDir) string {
	return filepath.Join(localRoot, conflict.PullLocalBackupRel(), rel)
}

func backupLocalBeforePull(localAbs, backup string, dryRun bool) error {
	if dryRun {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(backup), 0o755); err != nil {
		return err
	}
	return copyFilePreservingMtime(localAbs, backup)
}

// baselineMatchTiered reports whether the file at abs still matches base,
// hashing only when the fast (size+mtime) comparison disagrees with a
// sha-bearing baseline entry. The returned fingerprint is the freshest one
// computed (strict iff hashing happened). strict forces hashing.
//
// Tradeoff (same as push planning): a content change that preserves both
// size and mtime is invisible to the fast tier; --strict catches it.
func baselineMatchTiered(base Fingerprint, abs string, info os.FileInfo, strict bool) (bool, Fingerprint, error) {
	if strict {
		fp, err := FingerprintFile(abs, FingerprintStrict)
		if err != nil {
			return false, fp, err
		}
		return FingerprintsCompatible(base, fp, abs), fp, nil
	}
	fp := Fingerprint{Size: info.Size(), Mtime: info.ModTime().UTC()}
	if fingerprintsSameFast(base, fp) {
		return true, fp, nil
	}
	if base.Sha == "" {
		// No stronger signal exists; fast comparison is the verdict.
		return false, fp, nil
	}
	strictFP, err := FingerprintFile(abs, FingerprintStrict)
	if err != nil {
		return false, fp, err
	}
	return base.Sha == strictFP.Sha, strictFP, nil
}

// ensureStrictFingerprint upgrades fp with a sha when missing. Called
// immediately before a fingerprint is recorded into baseline.manifest so
// the Git-shared index stays content-addressed.
func ensureStrictFingerprint(fp Fingerprint, abs string) (Fingerprint, error) {
	if fp.Sha != "" {
		return fp, nil
	}
	return FingerprintFile(abs, FingerprintStrict)
}

func needsBaselineUpdate(base, current Fingerprint) bool {
	if !fingerprintsSame(base, current) {
		return true
	}
	if base.Sha == "" && current.Sha != "" {
		return true
	}
	// Sha proved equal but size/mtime drifted — refresh the baseline so
	// future fast comparisons succeed without re-hashing every pull.
	return current.Sha != "" && base.Sha == current.Sha && !fingerprintsSameFast(base, current)
}

func fingerprintsSame(a, b Fingerprint) bool {
	if a.Sha != "" && b.Sha != "" {
		return a.Sha == b.Sha
	}
	return fingerprintsSameFast(a, b)
}

func fingerprintsSameFast(a, b Fingerprint) bool {
	return a.Size == b.Size && sameMtime(a.Mtime, b.Mtime)
}
