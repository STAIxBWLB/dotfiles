package syncer

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// IntakeOptions controls one Intake run.
type IntakeOptions struct {
	Strict bool // sha256 mode (default: size+mtime fast mode)
	DryRun bool // skip staging copy + manifest writes
}

// IntakeResult summarizes one Intake run for the CLI/status layer.
type IntakeResult struct {
	StagingDir     string // <local>/inbox/gdrive/<ts>/ — empty if no files staged
	TimestampDir   string // <ts> alone, e.g. 2026-05-02T10-00-00Z
	Pull           *PullResult
	Intaked        []string
	SkippedBase    []string
	SkippedImports []string
	SkippedTracked []string
	Tombstones     []Tombstone
	Strict         bool
}

// driveMetadataExt are sentinel files written by the Drive client to
// point at cloud-side resources (Docs/Sheets/etc.). They are not
// content; intake skips them so the staging dir doesn't fill with
// stubs.
var driveMetadataExt = map[string]bool{
	".gdoc":      true,
	".gsheet":    true,
	".gslides":   true,
	".gform":     true,
	".gdraw":     true,
	".gtable":    true,
	".gnote":     true,
	".gjam":      true,
	".gmaplink":  true,
	".gsite":     true,
	".gshortcut": true,
}

// Intake compares mirror against baseline + imports manifests and stages only
// baseline-unknown GDrive-origin files into <local>/inbox/gdrive/<intake-ts>/.
// Changed baseline-tracked files are reported as skipped so the operator can
// review them with `dot sync pull`.
//
// Idempotency: once a file is in imports.manifest with a matching
// fingerprint, the next intake skips it — even if the operator moved
// it out of inbox/gdrive/ in the meantime. Use `inbox forget` to
// revoke that entry.
func Intake(ctx context.Context, runner *exec.Runner, cfg *Config, opts IntakeOptions) (*IntakeResult, error) {
	if cfg.LocalPaths == nil {
		return nil, fmt.Errorf("intake: local paths unresolved")
	}
	if cfg.Target.IsSSH() {
		return nil, fmt.Errorf("intake requires a local target (cannot walk an ssh remote)")
	}
	if err := refuseSharedDriveMirror(cfg); err != nil {
		return nil, err
	}

	paths := cfg.LocalPaths
	mirror := strings.TrimRight(cfg.MirrorPath, "/")
	local := strings.TrimRight(cfg.LocalPath, "/")
	tracked := gitTrackedRelPaths(local)

	baseline, err := LoadBaselineManifest(paths.BaselineFile)
	if err != nil {
		return nil, fmt.Errorf("loading baseline: %w", err)
	}
	imports, err := LoadImportsManifest(paths.ImportsFile)
	if err != nil {
		return nil, fmt.Errorf("loading imports: %w", err)
	}

	mode := FingerprintFast
	if opts.Strict {
		mode = FingerprintStrict
	}
	filter, err := newSyncFilter(cfg, mirror)
	if err != nil {
		return nil, fmt.Errorf("loading filters: %w", err)
	}

	now := time.Now().UTC()
	intakeTS := newSubSecondTimestamp()

	importsToWrite := make(map[string]ImportEntry, len(imports))
	for k, v := range imports {
		importsToWrite[k] = v
	}

	var (
		intaked        []string
		skippedBase    []string
		skippedImports []string
		skippedTracked []string
		toCopy         []string
	)

	walkErr := filepath.WalkDir(mirror, func(absPath string, d fs.DirEntry, err error) error {
		if err != nil {
			// A permission hiccup on a single file shouldn't abort
			// the whole intake. Bubble dir-level errors so the operator
			// sees them.
			if d != nil && !d.IsDir() {
				return nil
			}
			return err
		}
		if absPath == mirror {
			return nil
		}
		rel, err := filepath.Rel(mirror, absPath)
		if err != nil {
			return err
		}
		rel = normalizeRel(rel)
		if tracked[rel] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if filter.shouldSkip(absPath, rel, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if isDriveMetadata(rel) {
			return nil
		}
		// Symlinks: stat returned by WalkDir's fs.DirEntry doesn't
		// follow them; treat them as non-content and skip — push has
		// --no-links anyway, so they shouldn't be there.
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}

		fp, err := FingerprintFile(absPath, mode)
		if err != nil {
			// Unreadable file — skip silently. Best-effort intake.
			return nil
		}

		if base, ok := baseline[rel]; ok && FingerprintsCompatible(base, fp, absPath) {
			skippedBase = append(skippedBase, rel)
			return nil
		} else if ok {
			skippedTracked = append(skippedTracked, rel)
			return nil
		}
		if imp, ok := imports[rel]; ok && FingerprintsCompatible(imp.FP, fp, absPath) {
			skippedImports = append(skippedImports, rel)
			return nil
		}

		intaked = append(intaked, rel)
		toCopy = append(toCopy, rel)
		importsToWrite[rel] = ImportEntry{FP: fp, ImportedAt: now}
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("walking mirror: %w", walkErr)
	}

	sort.Strings(intaked)
	sort.Strings(skippedBase)
	sort.Strings(skippedImports)
	sort.Strings(skippedTracked)
	sort.Strings(toCopy)

	result := &IntakeResult{
		Intaked:        intaked,
		SkippedBase:    skippedBase,
		SkippedImports: skippedImports,
		SkippedTracked: skippedTracked,
		Strict:         opts.Strict,
	}

	if opts.DryRun {
		return result, nil
	}

	if len(toCopy) > 0 {
		stagingDir := filepath.Join(local, "inbox", "gdrive", intakeTS)
		if err := os.MkdirAll(stagingDir, 0o755); err != nil {
			return nil, fmt.Errorf("creating staging dir: %w", err)
		}
		if err := stageFilesToInbox(ctx, runner, cfg, mirror, stagingDir, toCopy); err != nil {
			return nil, fmt.Errorf("staging files: %w", err)
		}
		if err := SaveImportsManifest(paths.ImportsFile, importsToWrite); err != nil {
			return nil, fmt.Errorf("saving imports manifest: %w", err)
		}
		result.StagingDir = stagingDir
		result.TimestampDir = intakeTS
	}

	if len(toCopy) > 0 {
		if err := UpdateLocalState(paths, func(s *LocalState) {
			s.LastIntake = now
			if len(toCopy) > 0 {
				s.LastIntakeTSDir = intakeTS
			}
		}); err != nil {
			return nil, fmt.Errorf("updating local state: %w", err)
		}
	}

	return result, nil
}

// isAlwaysExcluded matches the workspace-anchored excludes that push
// enforces unconditionally — they must not round-trip back via intake.
func isAlwaysExcluded(rel string) bool {
	if rel == ".dotfiles" || strings.HasPrefix(rel, ".dotfiles/") {
		return true
	}
	if rel == "inbox/gdrive" || strings.HasPrefix(rel, "inbox/gdrive/") {
		return true
	}
	// .git and worktree roots at any depth, dir or file. Mirrors the hardcoded
	// rsync excludes so VCS internals and worktree checkouts never reach the target.
	if hasPathSegment(rel, ".git") {
		return true
	}
	for _, root := range worktreeRoots {
		if hasPathSegment(rel, root) {
			return true
		}
	}
	return false
}

// hasPathSegment reports whether seg (one or more whole path components)
// appears in rel at any depth, as the path itself or one of its parents.
func hasPathSegment(rel, seg string) bool {
	return rel == seg || strings.HasPrefix(rel, seg+"/") ||
		strings.HasSuffix(rel, "/"+seg) || strings.Contains(rel, "/"+seg+"/")
}

func isDriveMetadata(rel string) bool {
	return driveMetadataExt[strings.ToLower(filepath.Ext(rel))]
}

// stageFilesToInbox runs a single rsync invocation with --files-from
// to copy the chosen relpaths into stagingDir, preserving subtree
// structure. Falls back to a Go-side copy if rsync is unavailable
// (e.g., during tests on a setup that doesn't have rsync installed).
func stageFilesToInbox(ctx context.Context, runner *exec.Runner, cfg *Config, mirror, stagingDir string, rels []string) error {
	if len(rels) == 0 {
		return nil
	}

	if runner != nil && runner.CommandExists("rsync") {
		return rsyncStage(ctx, runner, cfg, mirror, stagingDir, rels)
	}
	return goCopyStage(mirror, stagingDir, rels)
}

func rsyncStage(ctx context.Context, runner *exec.Runner, cfg *Config, mirror, stagingDir string, rels []string) error {
	listFile, err := os.CreateTemp("", "gdrive-intake-files-from-*.txt")
	if err != nil {
		return err
	}
	defer os.Remove(listFile.Name())
	for _, r := range rels {
		if _, err := listFile.Write(append([]byte(r), 0)); err != nil {
			listFile.Close()
			return err
		}
	}
	if err := listFile.Close(); err != nil {
		return err
	}
	args := []string{
		"-a", "--no-links",
		"--from0",
		"--files-from=" + listFile.Name(),
		mirror + "/", stagingDir + "/",
	}
	if cfg.Verbose {
		return runner.RunAttached(ctx, "rsync", args...)
	}
	_, err = runner.Run(ctx, "rsync", args...)
	return err
}

// goCopyStage is the rsync-less fallback. Preserves mtime; doesn't try
// to be clever about hardlinks or special files (intake stages content
// only).
func goCopyStage(mirror, stagingDir string, rels []string) error {
	for _, rel := range rels {
		src := filepath.Join(mirror, rel)
		dst := filepath.Join(stagingDir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := copyFilePreservingMtime(src, dst); err != nil {
			return err
		}
	}
	return nil
}

func copyFilePreservingMtime(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chtimes(dst, info.ModTime(), info.ModTime())
}

// RefreshBaseline rebuilds <baseline.manifest> as the Git-shared sync payload
// index. For local targets it records files that exist on both mirror and
// local — including Git-tracked files, which are payload since the union
// filter. Mirror-only files stay out of baseline so target-origin new files
// continue to flow through inbox/gdrive until an operator accepts them.
//
// For SSH targets the remote tree cannot be walked, so the baseline records
// the local payload set instead — what the push just shipped. That keeps
// delete propagation working (a deleted file's baseline key keeps it in the
// include layer until the next refresh drops it).
func RefreshBaseline(cfg *Config, mode FingerprintMode) error {
	return refreshBaseline(cfg, mode, nil)
}

// refreshBaseline is RefreshBaseline that also records the mirror files a push
// just wrote (written) whose local twin is already gone. A file created and
// removed by a live process while the push ran is then a workspace deletion on
// the next push, not a mirror-origin file that blocks every clean push (#224).
func refreshBaseline(cfg *Config, mode FingerprintMode, written map[string]bool) error {
	if cfg.LocalPaths == nil {
		return fmt.Errorf("refresh baseline: local paths unresolved")
	}
	mirror := strings.TrimRight(cfg.MirrorPath, "/")
	local := strings.TrimRight(cfg.LocalPath, "/")
	filter, err := newSyncFilter(cfg, mirror)
	if err != nil {
		return fmt.Errorf("loading filters: %w", err)
	}
	walkRoot := mirror
	requireLocalTwin := true
	remoteBaseline := cfg.Target.IsSSH()
	if remoteBaseline {
		walkRoot = local
		requireLocalTwin = false
	}
	// A local baseline is rebuilt from the mirror, so an evicted file would
	// otherwise be recorded as a 0-byte stub and every later local edit would
	// read as divergence from it. Carrying the previous entry forward keeps
	// the path proven without inventing a fingerprint no file ever had.
	previous := map[string]Fingerprint{}
	if !remoteBaseline {
		previous, err = LoadBaselineManifest(cfg.LocalPaths.BaselineFile)
		if err != nil {
			return fmt.Errorf("loading baseline: %w", err)
		}
	}
	entries := map[string]Fingerprint{}
	err = filepath.WalkDir(walkRoot, func(absPath string, d fs.DirEntry, err error) error {
		if err != nil {
			// An incomplete SSH inventory would retire baseline keys and make
			// later local deletions indistinguishable from peer-only paths. Fail
			// closed there. Local mirrors can still skip slow cloud-placeholder
			// subtrees because their baseline is rebuilt from the mirror itself.
			if remoteBaseline || absPath == walkRoot {
				return err
			}
			fmt.Fprintf(os.Stderr, "warning: baseline walk skipping %s: %v\n", absPath, err)
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if absPath == walkRoot {
			return nil
		}
		rel, err := filepath.Rel(walkRoot, absPath)
		if err != nil {
			return err
		}
		rel = normalizeRel(rel)
		if filter.shouldSkip(absPath, rel, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if isDriveMetadata(rel) {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if !remoteBaseline && UnsupportedPathName(rel) {
			// A mirror push never sends these names (#131), so the mirror copy
			// proves nothing about the workspace: keep only an entry an earlier
			// push recorded, while the copy still matches it, and never
			// fingerprint the copy anew, or a cloud edit would become the proof
			// that moves it out as a leftover (#225). A refresh before #225 did
			// fingerprint the mirror copy, so while the workspace has the name
			// its copy must match the entry too. Without delete propagation an
			// entry whose local twin is gone drops, as for any other file.
			localAbs := filepath.Join(local, rel)
			if _, err := os.Lstat(localAbs); !os.IsNotExist(err) {
				// Present, or unreadable: carried only if its copy matches
				// too, so an unreadable twin drops the entry, never reads as gone.
				carryProvenEntry(entries, previous, rel, absPath, localAbs)
			} else if cfg.Propagation.Delete {
				carryProvenEntry(entries, previous, rel, absPath)
			}
			return nil
		}
		if requireLocalTwin && !written[rel] {
			localAbs := filepath.Join(local, rel)
			localInfo, err := os.Lstat(localAbs)
			if os.IsNotExist(err) && cfg.Propagation.Delete {
				// The workspace removed a file the baseline proves it put here,
				// and the mirror copy is unchanged: keep the proof, so the next
				// push deletes it instead of refusing it as mirror-origin (a
				// file an earlier push wrote can vanish during this one, #224).
				carryProvenEntry(entries, previous, rel, absPath)
				return nil
			}
			if err != nil || localInfo.IsDir() || localInfo.Mode()&os.ModeSymlink != 0 {
				return nil
			}
		}
		info, err := d.Info()
		if err != nil {
			if remoteBaseline {
				return fmt.Errorf("stat %s: %w", rel, err)
			}
			return nil
		}
		if dehydratedFile(absPath, info) {
			if prev, ok := previous[rel]; ok {
				entries[rel] = prev
			}
			return nil
		}
		fp, err := FingerprintFile(absPath, mode)
		if err != nil {
			if remoteBaseline {
				return fmt.Errorf("fingerprinting %s: %w", rel, err)
			}
			return nil
		}
		entries[rel] = fp
		return nil
	})
	if err != nil {
		return err
	}
	return SaveBaselineManifest(cfg.LocalPaths.BaselineFile, entries)
}

// carryProvenEntry keeps previous[rel] when every copy in copies (the mirror
// copy first) still matches it. A cloud placeholder or a changed copy is not
// carried: it drops out and is classified as mirror-origin, as before.
func carryProvenEntry(entries, previous map[string]Fingerprint, rel string, copies ...string) {
	prev, ok := previous[rel]
	if !ok {
		return
	}
	for _, absPath := range copies {
		info, err := os.Lstat(absPath)
		if err != nil || dehydratedFile(absPath, info) {
			return
		}
		fp, err := FingerprintFile(absPath, FingerprintFast)
		if err != nil || !FingerprintsCompatible(prev, fp, absPath) {
			return
		}
	}
	entries[rel] = prev
}
