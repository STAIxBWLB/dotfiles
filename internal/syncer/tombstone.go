package syncer

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

const (
	tombstoneListName      = "tombstones.list.dyn"
	tombstoneExcludesName  = "tombstones.excludes.dyn"
	tombstoneSourcePrefix  = "tombstones.source-"
	peerBaselineTargetName = "baseline.peer-target"
)

// ComputeTombstones returns the relpaths that this machine deleted since the
// last successful push: present in baseline.manifest, absent from the local
// tree. Sorted, so callers and tests see a stable order.
//
// It must run BEFORE the pull. The pull uses --update, which recreates any
// path missing locally, so afterwards a deletion is indistinguishable from a
// file the peer holds and this machine never had.
//
// Only the local tree and the baseline are read, never the target. That is
// what lets this work against an SSH peer, whose tree cannot be walked from
// here.
//
// Safety gates return an empty set rather than an error for non-peer targets,
// delete-off policies, and baselines that have not been established for the
// current peer. Callers can therefore invoke this unconditionally:
//
//   - Non-SSH targets. For a mirror the baseline records the MIRROR tree
//     (RefreshBaseline walks the mirror), so baseline-minus-local would be
//     nonsense. Only SSH profiles keep a local-tree baseline.
//   - propagation.delete off. Nothing may be removed on the target.
func ComputeTombstones(cfg *Config) ([]string, error) {
	if cfg.Profile != PeerProfile || !cfg.Target.IsSSH() || !cfg.Propagation.Delete {
		return nil, nil
	}
	if !cfg.Propagation.Create || !cfg.Propagation.Update {
		return nil, fmt.Errorf("compute tombstones: peer delete propagation requires create and update propagation")
	}
	if cfg.LocalPaths == nil {
		return nil, fmt.Errorf("compute tombstones: local paths unresolved")
	}
	ready, err := peerBaselineMatchesTarget(cfg)
	if err != nil {
		return nil, fmt.Errorf("checking peer baseline provenance: %w", err)
	}
	if !ready {
		// A pre-feature baseline, or one built for a different peer, cannot
		// prove that any path reached this target. The next successful full push
		// establishes provenance; until then deletion must remain additive.
		return nil, nil
	}
	baseline, err := LoadBaselineManifest(cfg.LocalPaths.BaselineFile)
	if err != nil {
		return nil, fmt.Errorf("loading baseline: %w", err)
	}
	if len(baseline) == 0 {
		return nil, nil
	}

	local := strings.TrimRight(cfg.LocalPath, "/")
	rootInfo, err := os.Lstat(local)
	if err != nil {
		return nil, fmt.Errorf("scanning local tree: %w", err)
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("scanning local tree: workspace root %s is not a directory", local)
	}
	// Same filter the baseline was built with, so a path that is merely
	// excluded never reads as deleted.
	filter, err := newSyncFilter(cfg, strings.TrimRight(cfg.MirrorPath, "/"))
	if err != nil {
		return nil, fmt.Errorf("loading filters: %w", err)
	}

	present := map[string]bool{}
	err = filepath.WalkDir(local, func(absPath string, d fs.DirEntry, err error) error {
		if err != nil {
			// Skipping an unreadable subtree would make every baseline entry
			// beneath it look deleted. Tombstone detection is destructive, so any
			// incomplete inventory must fail closed.
			return err
		}
		if absPath == local {
			return nil
		}
		rel, err := filepath.Rel(local, absPath)
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
		// Rsync runs with --no-links and RefreshBaseline omits symlinks. Treating
		// one as a present payload would let a symlink replacement suppress the
		// tombstone for the peer's old regular file.
		if !d.IsDir() && d.Type()&os.ModeSymlink == 0 {
			present[rel] = true
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scanning local tree: %w", err)
	}

	var out []string
	for rel := range baseline {
		if err := validateTombstoneRel(rel); err != nil {
			return nil, fmt.Errorf("compute tombstones: %w", err)
		}
		if present[rel] {
			continue
		}
		if filter.shouldSkipFileOrAncestor(rel) {
			continue
		}
		out = append(out, rel)
	}
	sort.Strings(out)
	return out, nil
}

func peerBaselineTargetFile(cfg *Config) string {
	return baselineTargetFileFor(cfg.LocalPaths.BaselineFile, peerBaselineTargetName)
}

// baselineTargetFileFor locates the provenance marker next to the baseline it
// authorizes, so the workspace and tracked-home baselines each carry their own.
func baselineTargetFileFor(baselineFile, markerName string) string {
	return filepath.Join(filepath.Dir(baselineFile), markerName)
}

func baselineMatchesTarget(baselineFile, markerName, rsyncDest string) (bool, error) {
	raw, err := os.ReadFile(baselineTargetFileFor(baselineFile, markerName))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return string(raw) == rsyncDest+"\n", nil
}

func markBaselineTarget(baselineFile, markerName, rsyncDest string) error {
	if err := atomicWrite(baselineTargetFileFor(baselineFile, markerName), []byte(rsyncDest+"\n")); err != nil {
		return fmt.Errorf("mark peer baseline target: %w", err)
	}
	return nil
}

func peerBaselineMatchesTarget(cfg *Config) (bool, error) {
	if cfg.LocalPaths == nil {
		return false, fmt.Errorf("local paths unresolved")
	}
	return baselineMatchesTarget(cfg.LocalPaths.BaselineFile, peerBaselineTargetName, cfg.Target.RsyncDest())
}

func markPeerBaselineTarget(cfg *Config) error {
	if cfg.LocalPaths == nil {
		return fmt.Errorf("mark peer baseline: local paths unresolved")
	}
	return markBaselineTarget(cfg.LocalPaths.BaselineFile, peerBaselineTargetName, cfg.Target.RsyncDest())
}

// checkTombstoneCap refuses a delete pass whose set exceeds MaxDelete. A
// failed mount or a broken filter presents the entire tree as deleted; the
// cap turns that into a refusal instead of a mass removal on the peer.
func checkTombstoneCap(cfg *Config, tombstones []string) error {
	if cfg.MaxDelete > 0 && len(tombstones) > cfg.MaxDelete {
		return fmt.Errorf(
			"refusing to propagate %d deletions: over max_delete=%d. "+
				"Review baseline.manifest against the local tree; raise max_delete if this is genuine",
			len(tombstones), cfg.MaxDelete)
	}
	return nil
}

// writeTombstoneList writes the paths NUL-delimited for rsync --files-from
// --from0. This workspace has filenames with spaces and Korean text, which a
// newline-delimited list handles only by accident.
func writeTombstoneList(dir string, rels []string) (string, error) {
	path := filepath.Join(dir, tombstoneListName)
	var b strings.Builder
	for _, rel := range rels {
		if err := validateTombstoneRel(rel); err != nil {
			return "", fmt.Errorf("writing tombstone list: %w", err)
		}
		b.WriteString(rel)
		b.WriteByte(0)
	}
	if err := atomicWrite(path, []byte(b.String())); err != nil {
		return "", fmt.Errorf("writing tombstone list: %w", err)
	}
	return path, nil
}

// prepareTombstoneSource creates only the parent directories for each missing
// payload path. Rsync otherwise reports those implicit parents as vanished and
// exits 24 even when the requested receiver-side deletion succeeded. Using a
// private staging root makes the expected missing leaf clean without
// --no-implied-dirs, which could follow a receiver-side symlink outside the
// workspace.
func prepareTombstoneSource(dir string, rels []string) (string, error) {
	root, err := os.MkdirTemp(dir, tombstoneSourcePrefix)
	if err != nil {
		return "", fmt.Errorf("creating tombstone source: %w", err)
	}
	for _, rel := range rels {
		if err := validateTombstoneRel(rel); err != nil {
			_ = os.RemoveAll(root)
			return "", fmt.Errorf("creating tombstone source: %w", err)
		}
		parent := filepath.Dir(filepath.FromSlash(rel))
		if parent == "." {
			continue
		}
		if err := os.MkdirAll(filepath.Join(root, parent), 0o755); err != nil {
			_ = os.RemoveAll(root)
			return "", fmt.Errorf("creating tombstone source parent: %w", err)
		}
	}
	return root, nil
}

func validateTombstoneRel(rel string) error {
	if rel == "" || strings.ContainsRune(rel, 0) || filepath.IsAbs(rel) {
		return fmt.Errorf("unsafe path %q", rel)
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) ||
		filepath.ToSlash(clean) != filepath.ToSlash(rel) {
		return fmt.Errorf("unsafe path %q (not a clean workspace-relative path)", rel)
	}
	return nil
}

// MaterializeTombstoneExcludesFile writes the pull's protective layer: one
// anchored exclude per tombstoned path, so the pull cannot restore what this
// machine just deleted. Always written, even empty, matching how the other
// runtime filter layers behave.
//
// Paths are anchored with a leading slash (relative to the transfer root) so
// `inbox/drop/x.csv` cannot also match `elsewhere/inbox/drop/x.csv`.
func MaterializeTombstoneExcludesFile(dir string, rels []string) (string, error) {
	path := filepath.Join(dir, tombstoneExcludesName)
	var b strings.Builder
	b.WriteString("# Auto-generated per run by `dot peer sync` - do not edit.\n")
	b.WriteString("# Paths deleted locally, protected from being pulled back.\n")
	for _, rel := range rels {
		pattern, err := literalRsyncPattern(rel)
		if err != nil {
			return "", fmt.Errorf("writing tombstone excludes: %w", err)
		}
		b.WriteString("/" + pattern + "\n")
	}
	if err := atomicWrite(path, []byte(b.String())); err != nil {
		return "", fmt.Errorf("writing tombstone excludes: %w", err)
	}
	return path, nil
}

// literalRsyncPattern escapes the wildcard syntax used by --exclude-from.
// The file format is line-oriented, so names containing a line separator are
// rejected rather than being allowed to inject a second filter rule.
func literalRsyncPattern(rel string) (string, error) {
	if err := validateTombstoneRel(rel); err != nil {
		return "", err
	}
	if strings.ContainsAny(rel, "\r\n") {
		return "", fmt.Errorf("path %q contains a line separator", rel)
	}
	hasWildcard := strings.ContainsAny(rel, "*?[")
	var b strings.Builder
	for _, r := range rel {
		switch {
		case r == '\\' && hasWildcard:
			// Once a pattern contains wildcard syntax, rsync interprets
			// backslashes as escapes. Double literal backslashes to preserve them.
			b.WriteString("\\\\")
		case r == '*' || r == '?' || r == '[':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String(), nil
}

// literalRsyncPrefix is literalRsyncPattern for a path a pattern extends with
// a wildcard (rel + "/**"). The whole pattern is then wildcard-matched, so
// every backslash in rel is an escape and is doubled, wildcards or not.
func literalRsyncPrefix(rel string) (string, error) {
	pattern, err := literalRsyncPattern(rel)
	if err != nil || strings.ContainsAny(rel, "*?[") {
		return pattern, err
	}
	return strings.ReplaceAll(pattern, `\`, `\\`), nil
}

// deletePassArgs builds the rsync argv that removes exactly the listed paths
// from the target and quarantines them.
//
// --files-from scopes the run to the list, and --delete-missing-args removes
// those entries that are missing on the sender. Plain --delete is deliberately
// absent: it would remove every target path absent locally, including files
// the peer created that this machine has not pulled yet.
//
// --backup with --backup-dir moves each removal under .sync-conflicts/<ts>/
// on the target instead of unlinking it, preserving content and relative path
// until `dot sync conflicts prune` expires it.
func deletePassArgs(cfg *Config, conflict *ConflictDir, listFile, sourceRoot string, dryRun bool) []string {
	args := []string{
		"-r",
		"--human-readable",
		"--stats",
		"--no-links",
		"--files-from=" + listFile,
		"--from0",
		"--ignore-missing-args",
		"--delete-missing-args",
		"--backup",
		"--backup-dir=" + conflict.PushBackupRel(),
	}
	if dryRun {
		args = append(args, "--dry-run")
	}
	args = append(args, rsyncTransportArgs(cfg)...)
	args = append(args, sourceRoot+"/", cfg.Target.RsyncDest())
	return args
}

// PropagateDeletes removes tombstoned paths from the target, into quarantine.
//
// Runs after the pull and BEFORE the push. Push ends with RefreshBaseline,
// which walks the local tree and therefore drops these paths from the
// baseline; if the pass ran after that and failed, the tombstone evidence
// would be gone and the peer's copy would come back on the next pull. Failing
// here returns before the baseline is touched, so the deletion is retried on
// the next run.
//
// A successful pass needs no baseline surgery: the subsequent RefreshBaseline
// retires these keys on its own.
func PropagateDeletes(ctx context.Context, runner *exec.Runner, cfg *Config, conflict *ConflictDir, tombstones []string, dryRun bool) error {
	if len(tombstones) == 0 {
		return nil
	}
	if cfg.Profile != PeerProfile || !cfg.Target.IsSSH() {
		return fmt.Errorf("propagate deletes: requires the SSH peer profile")
	}
	if !cfg.Propagation.Delete || !cfg.Propagation.Create || !cfg.Propagation.Update {
		return fmt.Errorf("propagate deletes: requires create, update, and delete propagation")
	}
	if err := checkTombstoneCap(cfg, tombstones); err != nil {
		return err
	}
	ready, err := peerBaselineMatchesTarget(cfg)
	if err != nil {
		return fmt.Errorf("propagate deletes: checking peer baseline provenance: %w", err)
	}
	if !ready {
		return fmt.Errorf("propagate deletes: baseline was not established for %s", cfg.Target.RsyncDest())
	}
	if err := preflightPeerQuarantine(ctx, runner, cfg, conflict, !dryRun); err != nil {
		return err
	}
	return propagateDeletes(ctx, runner, cfg, conflict, tombstones, dryRun)
}

const quarantinePreflightScript = `set -eu
root=$1
stamp=$2
create=$3
case "$root" in
  "~/"*) root=$HOME/${root#\~/} ;;
esac
case "$root" in
  ""|"/") echo "unsafe peer workspace root: $root" >&2; exit 40 ;;
esac
if [ ! -d "$root" ] || [ -L "$root" ]; then
  echo "unsafe peer workspace root: $root" >&2
  exit 40
fi
q=$root/.sync-conflicts
run=$q/$stamp
leaf=$run/from-workspace
for p in "$q" "$run" "$leaf"; do
  if [ -L "$p" ] || { [ -e "$p" ] && [ ! -d "$p" ]; }; then
    echo "unsafe peer quarantine path: $p" >&2
    exit 41
  fi
  if [ "$create" = 1 ] && [ ! -d "$p" ]; then
    mkdir "$p"
  fi
  if [ -e "$p" ] && { [ ! -d "$p" ] || [ -L "$p" ]; }; then
    echo "unsafe peer quarantine path: $p" >&2
    exit 42
  fi
done`

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func validateConflictStamp(stamp string) error {
	if stamp == "" || strings.ContainsAny(stamp, "/\\\r\n\x00") || stamp == "." || stamp == ".." {
		return fmt.Errorf("unsafe conflict timestamp %q", stamp)
	}
	return nil
}

func remoteQuarantineCommand(root, stamp string, create bool) (string, error) {
	if err := validateRemoteWorkspaceRoot(root); err != nil {
		return "", err
	}
	if err := validateConflictStamp(stamp); err != nil {
		return "", err
	}
	createArg := "0"
	if create {
		createArg = "1"
	}
	return "sh -c " + shellQuote(quarantinePreflightScript) + " sh " +
		shellQuote(root) + " " + shellQuote(stamp) + " " + createArg, nil
}

func validateRemoteWorkspaceRoot(root string) error {
	root = strings.TrimSpace(strings.TrimRight(root, "/"))
	if root == "" || root == "/" || root == "~" || strings.ContainsAny(root, "\x00\r\n") {
		return fmt.Errorf("unsafe peer workspace root %q", root)
	}
	for _, component := range strings.Split(filepath.ToSlash(root), "/") {
		if component == "." || component == ".." {
			return fmt.Errorf("unsafe peer workspace root %q", root)
		}
	}
	return nil
}

// preflightPeerQuarantine refuses a receiver-side symlink before rsync uses
// --backup-dir. Without this check a delete can move the peer's file through
// .sync-conflicts into an arbitrary path outside the workspace.
func preflightPeerQuarantine(ctx context.Context, runner *exec.Runner, cfg *Config, conflict *ConflictDir, create bool) error {
	command, err := remoteQuarantineCommand(cfg.Target.Path, conflict.Timestamp, create)
	if err != nil {
		return fmt.Errorf("preflight peer quarantine: %w", err)
	}
	if _, err := runner.Run(ctx, "ssh", "-o", "BatchMode=yes", cfg.Target.Host, command); err != nil {
		return fmt.Errorf("preflight peer quarantine: %w", err)
	}
	return nil
}

// propagateDeletes contains the target-independent implementation so the
// local-directory integration test can exercise real rsync without an SSH
// fixture. Production callers use PropagateDeletes and its peer-only guards.
func propagateDeletes(ctx context.Context, runner *exec.Runner, cfg *Config, conflict *ConflictDir, tombstones []string, dryRun bool) error {
	if err := checkTombstoneCap(cfg, tombstones); err != nil {
		return err
	}
	if cfg.LocalPaths == nil {
		return fmt.Errorf("propagate deletes: local paths unresolved")
	}
	baseline, err := LoadBaselineManifest(cfg.LocalPaths.BaselineFile)
	if err != nil {
		return fmt.Errorf("loading baseline for delete pass: %w", err)
	}
	for _, rel := range tombstones {
		if err := validateTombstoneRel(rel); err != nil {
			return fmt.Errorf("propagate deletes: %w", err)
		}
		if _, ok := baseline[rel]; !ok {
			return fmt.Errorf("propagate deletes: path %q is not in the baseline", rel)
		}
	}
	listFile, err := writeTombstoneList(cfg.ConfigDir, tombstones)
	if err != nil {
		return err
	}
	sourceRoot, err := prepareTombstoneSource(cfg.ConfigDir, tombstones)
	if err != nil {
		return err
	}
	defer os.RemoveAll(sourceRoot)
	args := deletePassArgs(cfg, conflict, listFile, sourceRoot, dryRun)
	fmt.Fprintf(cfg.out(), "  Delete: %d path(s) → %s (quarantined under %s)\n",
		len(tombstones), cfg.Target.RsyncDest(), conflictsDirName)
	if dryRun {
		for _, rel := range tombstones {
			fmt.Fprintf(cfg.out(), "    %q → %q\n", rel, filepath.ToSlash(filepath.Join(conflict.PushBackupRel(), rel)))
		}
	}
	if err := runDeleteRsync(ctx, runner, cfg, args); err != nil {
		// --ignore-missing-args makes the expected absent sender paths clean.
		// Any remaining error, including exit 23/24, can mean the target delete
		// or quarantine failed. Returning keeps the baseline intact so the
		// tombstone is retried instead of silently retired by the following push.
		return err
	}
	if dryRun {
		return nil
	}
	// Audit trail only. Nothing reads this back; it is the sole local record
	// of a destructive change made on the other machine. Each row carries the
	// baseline fingerprint of what was removed; the following push retires it.
	entries := make([]Tombstone, 0, len(tombstones))
	now := time.Now().UTC()
	for _, rel := range tombstones {
		entries = append(entries, Tombstone{RelPath: rel, BaselineFP: baseline[rel], DetectedAt: now})
	}
	if err := AppendTombstones(cfg.LocalPaths.TombstonesFile, entries); err != nil {
		return fmt.Errorf("recording tombstones: %w", err)
	}
	return nil
}

func runDeleteRsync(ctx context.Context, runner *exec.Runner, cfg *Config, args []string) error {
	result, err := runner.Run(ctx, cfg.rsyncBin(), args...)
	if result != nil {
		output := strings.ToLower(result.Stdout + "\n" + result.Stderr)
		if strings.Contains(output, "cannot delete") {
			return fmt.Errorf("rsync delete pass left a requested path in place: %s", strings.TrimSpace(result.Stderr))
		}
		if cfg.Verbose {
			fmt.Fprint(cfg.out(), result.Stdout)
			fmt.Fprint(os.Stderr, result.Stderr)
		}
	}
	return classifyRsyncError(err)
}
