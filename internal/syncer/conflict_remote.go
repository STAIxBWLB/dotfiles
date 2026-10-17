package syncer

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// PeerHomeConflictRoot is the destination-relative quarantine used by the
// peer profile's second (host-path) rsync pass.
const PeerHomeConflictRoot = "~/.dot-peer-conflicts"

// RemoteConflictEntry is the remote equivalent of ConflictEntry. Remote
// sizing is captured by the read-only inventory pass so prune dry-runs and
// confirmed runs describe the same plan.
type RemoteConflictEntry struct {
	ConflictEntry
	Size int64
}

// RemoteTargetConflictRoot returns the exact conflict root under an SSH
// target. It deliberately does not use Config.MirrorPath: that field is empty
// for SSH targets by design.
func RemoteTargetConflictRoot(target Target) (string, error) {
	if !target.IsSSH() {
		return "", fmt.Errorf("remote conflict root requires an SSH target")
	}
	base := strings.TrimSpace(strings.TrimRight(target.Path, "/"))
	if base == "" || base == "/" {
		return "", fmt.Errorf("unsafe remote target path %q: refusing empty or root path", target.Path)
	}
	root := base + "/" + conflictsDirName
	if err := validateRemoteConflictRoot(root); err != nil {
		return "", err
	}
	return root, nil
}

// ListRemoteConflicts inventories one exact remote conflict root. The command
// is read-only and uses RunQuery so it still executes when the caller passed
// --dry-run; dry-run output therefore reflects the real remote store.
func ListRemoteConflicts(ctx context.Context, runner *exec.Runner, target Target, root string) ([]RemoteConflictEntry, error) {
	if !target.IsSSH() {
		return nil, fmt.Errorf("remote conflict listing requires an SSH target")
	}
	if err := validateRemoteConflictRoot(root); err != nil {
		return nil, err
	}
	command, err := remoteConflictListCommand(root)
	if err != nil {
		return nil, err
	}
	result, err := runner.RunQuery(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", target.Host, command)
	if err != nil {
		return nil, fmt.Errorf("listing remote conflicts under %s: %w", root, err)
	}
	entries, err := parseRemoteConflictList(result.Stdout, root)
	if err != nil {
		return nil, fmt.Errorf("parsing remote conflicts under %s: %w", root, err)
	}
	return entries, nil
}

// PruneRemoteConflicts computes and, when dryRun is false, applies a prune
// plan on one exact remote conflict root. The actual command receives only
// validated timestamp names from the inventory pass; it never accepts a
// wildcard or recursively removes the conflict root.
func PruneRemoteConflicts(ctx context.Context, runner *exec.Runner, target Target, root string, olderThan time.Time, dryRun bool) (*PruneResult, error) {
	entries, err := ListRemoteConflicts(ctx, runner, target, root)
	if err != nil {
		return nil, err
	}
	res := &PruneResult{Root: root, DryRun: dryRun}
	for _, entry := range entries {
		if !entry.ModTime.Before(olderThan) {
			res.Kept++
			continue
		}
		res.Pruned = append(res.Pruned, PrunedEntry(entry))
		res.Reclaimed += entry.Size
	}
	if dryRun || len(res.Pruned) == 0 {
		return res, nil
	}
	return ApplyRemoteConflictPrune(ctx, runner, target, root, res.Pruned)
}

// ApplyRemoteConflictPrune removes exactly the entries shown by a prior plan.
// It re-inventories first and refuses if any selected timestamp changed; new
// old-looking directories created after confirmation are never swept in.
func ApplyRemoteConflictPrune(ctx context.Context, runner *exec.Runner, target Target, root string, planned []PrunedEntry) (*PruneResult, error) {
	res := &PruneResult{Root: root}
	if len(planned) == 0 {
		return res, nil
	}
	current, err := ListRemoteConflicts(ctx, runner, target, root)
	if err != nil {
		return nil, err
	}
	byStamp := make(map[string]RemoteConflictEntry, len(current))
	for _, entry := range current {
		byStamp[entry.Timestamp] = entry
	}

	stamps := make([]string, 0, len(planned))
	for _, entry := range planned {
		currentEntry, ok := byStamp[entry.Timestamp]
		if !ok || !currentEntry.ModTime.Equal(entry.ModTime) || currentEntry.Size != entry.Size {
			return nil, fmt.Errorf("remote conflict %q changed after confirmation; refusing prune", entry.Timestamp)
		}
		stamps = append(stamps, entry.Timestamp)
		res.Pruned = append(res.Pruned, entry)
		res.Reclaimed += entry.Size
	}
	res.Kept = len(current) - len(planned)
	command, err := remoteConflictPruneCommand(root, stamps)
	if err != nil {
		return nil, err
	}
	if _, err := runner.Run(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", target.Host, command); err != nil {
		return nil, fmt.Errorf("pruning remote conflicts under %s: %w", root, err)
	}
	return res, nil
}

// validateRemoteConflictRoot is intentionally stricter than a generic path
// validator. Only the two roots this feature owns may be addressed remotely;
// this prevents a future caller from accidentally turning prune into an
// arbitrary SSH rm command.
func validateRemoteConflictRoot(root string) error {
	root = strings.TrimSpace(root)
	if root == "" || root == "/" {
		return fmt.Errorf("unsafe remote conflict root %q: refusing empty or root path", root)
	}
	if strings.ContainsAny(root, "\x00\r\n") {
		return fmt.Errorf("unsafe remote conflict root %q: control character", root)
	}
	trimmed := strings.TrimRight(root, "/")
	if trimmed == "" || trimmed == "/" {
		return fmt.Errorf("unsafe remote conflict root %q: refusing empty or root path", root)
	}
	base := path.Base(trimmed)
	if base != conflictsDirName && base != ".dot-peer-conflicts" {
		return fmt.Errorf("unsafe remote conflict root %q: unexpected directory", root)
	}
	for _, component := range strings.Split(trimmed, "/") {
		if component == "." || component == ".." {
			return fmt.Errorf("unsafe remote conflict root %q: path traversal", root)
		}
	}
	return nil
}

func validateRemoteConflictTimestamp(stamp string) error {
	if stamp == "" || stamp == "." || stamp == ".." {
		return fmt.Errorf("unsafe remote conflict timestamp %q", stamp)
	}
	if strings.ContainsAny(stamp, "/\\\x00\r\n") {
		return fmt.Errorf("unsafe remote conflict timestamp %q", stamp)
	}
	if !isASCIIAlphaNumeric(stamp[0]) {
		return fmt.Errorf("unsafe remote conflict timestamp %q", stamp)
	}
	for _, r := range stamp {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			continue
		}
		return fmt.Errorf("unsafe remote conflict timestamp %q", stamp)
	}
	validTimestamp := false
	for _, layout := range []string{"2006-01-02T15-04-05Z", "2006-01-02T15-04-05.000000Z"} {
		if _, err := time.Parse(layout, stamp); err == nil {
			validTimestamp = true
			break
		}
	}
	if !validTimestamp {
		return fmt.Errorf("unsafe remote conflict timestamp %q: not a managed timestamp", stamp)
	}
	return nil
}

func isASCIIAlphaNumeric(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

func parseRemoteConflictList(stdout, root string) ([]RemoteConflictEntry, error) {
	var out []RemoteConflictEntry
	seen := make(map[string]struct{})
	for _, line := range strings.Split(stdout, "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 4 || fields[0] != remoteConflictRecordMarker {
			return nil, fmt.Errorf("unexpected inventory record %q", line)
		}
		stamp := fields[1]
		if err := validateRemoteConflictTimestamp(stamp); err != nil {
			return nil, err
		}
		if _, ok := seen[stamp]; ok {
			return nil, fmt.Errorf("duplicate remote conflict timestamp %q", stamp)
		}
		seen[stamp] = struct{}{}
		unix, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || unix < 0 {
			return nil, fmt.Errorf("invalid mtime for %q: %q", stamp, fields[2])
		}
		size, err := strconv.ParseInt(fields[3], 10, 64)
		if err != nil || size < 0 {
			return nil, fmt.Errorf("invalid size for %q: %q", stamp, fields[3])
		}
		out = append(out, RemoteConflictEntry{
			ConflictEntry: ConflictEntry{
				Timestamp: stamp,
				Path:      strings.TrimRight(root, "/") + "/" + stamp,
				ModTime:   time.Unix(unix, 0).UTC(),
			},
			Size: size,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ModTime.Equal(out[j].ModTime) {
			return out[i].Timestamp < out[j].Timestamp
		}
		return out[i].ModTime.Before(out[j].ModTime)
	})
	return out, nil
}

const remoteConflictRecordMarker = "DOTFILES_CONFLICT_V1"

// This shell fragment validates the parent and conflict root before either
// listing or deleting. A missing conflict root is a valid empty inventory, but
// its parent must still exist and be a real directory so a typo cannot look
// like an empty remote store.
const remoteConflictValidationShell = `
set -eu
die() { printf '%s\n' "$1" >&2; exit 40; }
root=$1
case "$root" in
  "") die "unsafe remote conflict root: empty" ;;
  "~") root=$HOME ;;
  "~/"*) root=$HOME/${root#\~/} ;;
esac
case "$root" in
  ""|"/") die "unsafe remote conflict root: empty or /" ;;
esac
case "${root##*/}" in
  .sync-conflicts|.dot-peer-conflicts) ;;
  *) die "unsafe remote conflict root: unexpected directory" ;;
esac
parent=${root%/*}
[ "$parent" = "$root" ] && parent=.
if [ -L "$parent" ] || [ ! -e "$parent" ] || [ ! -d "$parent" ]; then
  die "unsafe remote conflict parent: $parent"
fi
if [ -L "$root" ]; then
  die "unsafe remote conflict root is a symlink: $root"
fi
`

const remoteConflictListScript = remoteConflictValidationShell + `
if [ ! -e "$root" ]; then
  exit 0
fi
if [ ! -d "$root" ]; then
  die "unsafe remote conflict root is not a directory: $root"
fi
for entry in "$root"/* "$root"/.[!.]* "$root"/..?*; do
  if [ -L "$entry" ]; then
    die "unsafe remote conflict timestamp is a symlink: $entry"
  fi
  if [ ! -e "$entry" ]; then
    continue
  fi
  if [ ! -d "$entry" ]; then
    die "unsafe remote conflict timestamp is not a directory: $entry"
  fi
  stamp=${entry##*/}
  case "$stamp" in
    [A-Za-z0-9]*) ;;
    *) die "unsafe remote conflict timestamp: $stamp" ;;
  esac
  case "$stamp" in
    *[!A-Za-z0-9._-]*) die "unsafe remote conflict timestamp: $stamp" ;;
  esac
  case "$(uname -s 2>/dev/null || true)" in
    Darwin) mtime=$(stat -f '%m' "$entry" 2>/dev/null) || die "cannot stat remote conflict: $entry" ;;
    *) mtime=$(stat -c '%Y' "$entry" 2>/dev/null || stat -f '%m' "$entry" 2>/dev/null) || die "cannot stat remote conflict: $entry" ;;
  esac
  blocks=$(du -sk "$entry" 2>/dev/null | awk 'NR == 1 {print $1}') || die "cannot size remote conflict: $entry"
  case "$mtime" in
    ''|*[!0-9-]*) die "invalid remote conflict mtime: $entry" ;;
  esac
  case "$blocks" in
    ''|*[!0-9]*) die "invalid remote conflict size: $entry" ;;
  esac
  size=$((blocks * 1024))
  printf '%s\t%s\t%s\t%s\n' ` + remoteConflictRecordMarker + ` "$stamp" "$mtime" "$size"
done
`

const remoteConflictPruneScript = remoteConflictValidationShell + `
if [ ! -e "$root" ]; then
  die "remote conflict root disappeared: $root"
fi
if [ ! -d "$root" ]; then
  die "unsafe remote conflict root is not a directory: $root"
fi
# Validate every direct child before deleting any selected entry. This keeps a
# newly introduced symlink/file from turning a previously safe plan into a
# broad or ambiguous deletion.
for entry in "$root"/* "$root"/.[!.]* "$root"/..?*; do
  if [ -L "$entry" ]; then
    die "unsafe remote conflict timestamp is a symlink: $entry"
  fi
  if [ ! -e "$entry" ]; then
    continue
  fi
  if [ ! -d "$entry" ]; then
    die "unsafe remote conflict timestamp is not a directory: $entry"
  fi
  stamp=${entry##*/}
  case "$stamp" in
    [A-Za-z0-9]*) ;;
    *) die "unsafe remote conflict timestamp: $stamp" ;;
  esac
  case "$stamp" in
    *[!A-Za-z0-9._-]*) die "unsafe remote conflict timestamp: $stamp" ;;
  esac
done
shift
if [ "$#" -eq 0 ]; then
  die "refusing broad remote conflict deletion"
fi
for stamp in "$@"; do
  case "$stamp" in
    [A-Za-z0-9]*) ;;
    *) die "unsafe remote conflict timestamp: $stamp" ;;
  esac
  case "$stamp" in
    *[!A-Za-z0-9._-]*) die "unsafe remote conflict timestamp: $stamp" ;;
  esac
  entry=$root/$stamp
  if [ -L "$entry" ] || [ ! -e "$entry" ] || [ ! -d "$entry" ]; then
    die "remote conflict timestamp changed or disappeared: $stamp"
  fi
  rm -rf -- "$entry" || die "could not remove remote conflict: $stamp"
done
# Match local prune's empty-root cleanup without ever recursively removing the
# root. rmdir can only succeed after all non-selected entries are gone.
rmdir "$root" 2>/dev/null || true
`

func remoteConflictListCommand(root string) (string, error) {
	if err := validateRemoteConflictRoot(root); err != nil {
		return "", err
	}
	return "sh -c " + shellQuote(remoteConflictListScript) + " sh " + shellQuote(root), nil
}

func remoteConflictPruneCommand(root string, stamps []string) (string, error) {
	if err := validateRemoteConflictRoot(root); err != nil {
		return "", err
	}
	if len(stamps) == 0 {
		return "", fmt.Errorf("refusing broad remote conflict deletion: no timestamp names")
	}
	args := []string{"sh -c", shellQuote(remoteConflictPruneScript), "sh", shellQuote(root)}
	for _, stamp := range stamps {
		if err := validateRemoteConflictTimestamp(stamp); err != nil {
			return "", err
		}
		args = append(args, shellQuote(stamp))
	}
	return strings.Join(args, " "), nil
}
