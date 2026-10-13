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

	"github.com/entelecheia/dotfiles-v2/internal/exec"
)

const (
	homeBaselineName       = "baseline-home.manifest"
	homeBaselineTargetName = "baseline-home.peer-target"

	// homeConflictDirName is the quarantine root under each home for tracked
	// host paths. It is deliberately not conflictsDirName: the workspace
	// conflict tree lives under the synced root, while home paths are synced
	// against $HOME itself, where a ".sync-conflicts" sibling would sit next
	// to the user's Documents. PeerHomeConflictRoot is its remote spelling.
	homeConflictDirName = ".dot-peer-conflicts"

	// homeConflictSubFromPeer is the backup subdirectory for tracked home
	// payloads. One name serves both directions: from the receiver's side the
	// losing or deleted payload was removed because its peer changed.
	homeConflictSubFromPeer = "from-peer"
)

// peerHomeBaselineFile is the tracked-home baseline inside the peer store,
// kept separate from the workspace baseline so a host-path failure can never
// retire workspace evidence or vice versa.
func peerHomeBaselineFile(paths *LocalPaths) string {
	return filepath.Join(paths.StoreDir, homeBaselineName)
}

func peerHomeBaselineReady(cfg *Config) (bool, error) {
	if cfg.LocalPaths == nil {
		return false, fmt.Errorf("local paths unresolved")
	}
	return baselineMatchesTarget(peerHomeBaselineFile(cfg.LocalPaths), homeBaselineTargetName, cfg.Target.RsyncDest())
}

// commitPeerHomeBaseline records the tracked-home transaction's converged
// snapshot and target provenance. Like the workspace baseline it is written
// only after the complete transaction succeeds; a partial run must leave the
// old baseline in place so held deletes are retried, not forgotten.
func commitPeerHomeBaseline(cfg *Config, snapshot PeerSnapshot) error {
	if cfg == nil || cfg.Profile != PeerProfile || !cfg.Target.IsSSH() {
		return fmt.Errorf("commit peer home baseline: requires SSH peer profile")
	}
	if cfg.LocalPaths == nil {
		return fmt.Errorf("commit peer home baseline: local paths unresolved")
	}
	entries := make(map[string]Fingerprint, len(snapshot))
	for rel, f := range snapshot {
		if err := validateTombstoneRel(rel); err != nil {
			return fmt.Errorf("commit peer home baseline: %w", err)
		}
		if f.Present {
			entries[rel] = f.FP
		}
	}
	baselineFile := peerHomeBaselineFile(cfg.LocalPaths)
	if err := SaveBaselineManifest(baselineFile, entries); err != nil {
		return fmt.Errorf("saving peer home baseline: %w", err)
	}
	if err := markBaselineTarget(baselineFile, homeBaselineTargetName, cfg.Target.RsyncDest()); err != nil {
		return err
	}
	return nil
}

// readPeerHomeTrackedEntries parses a tracked list file: one home-relative
// path per line, comments and blanks skipped, duplicates removed.
func readPeerHomeTrackedEntries(path string) ([]string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.Split(string(body), "\n") {
		entry := strings.TrimSpace(line)
		if entry == "" || strings.HasPrefix(entry, "#") {
			continue
		}
		entry = strings.TrimSuffix(filepath.ToSlash(entry), "/")
		if err := validateTombstoneRel(entry); err != nil {
			return nil, fmt.Errorf("tracked home paths %s: %w", path, err)
		}
		if !seen[entry] {
			seen[entry] = true
			out = append(out, entry)
		}
	}
	return out, nil
}

// inventoryPeerHomeTracked walks only the tracked entries under home and
// fingerprints every regular file and symlink beneath them, keyed home-relative. The
// tracked list is the entire filter: no workspace filter chain applies to
// paths outside the workspace. Symlinks are never followed, matching the
// rsync passes' link-preserving treatment of them.
func inventoryPeerHomeTracked(home string, entries []string) (PeerSnapshot, error) {
	home = strings.TrimRight(home, "/")
	rootInfo, err := os.Lstat(home)
	if err != nil {
		return nil, fmt.Errorf("peer home inventory: %w", err)
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("peer home inventory: home %s is not a directory", home)
	}
	out := PeerSnapshot{}
	addFile := func(abs, rel string) error {
		fp, err := FingerprintFile(abs, FingerprintFast)
		if err != nil {
			return err
		}
		out[rel] = PeerFile{Present: true, FP: fp}
		return nil
	}
	for _, entry := range entries {
		if err := validatePeerHomeAncestors(home, []string{entry}); err != nil {
			return nil, err
		}
		abs := filepath.Join(home, filepath.FromSlash(entry))
		info, err := os.Lstat(abs)
		if os.IsNotExist(err) {
			// Same rule as the transfer's --ignore-missing-args: a shared list
			// names paths that legitimately exist on one machine only.
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("peer home inventory: %w", err)
		}
		if info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			if err := addFile(abs, entry); err != nil {
				return nil, fmt.Errorf("peer home inventory: %w", err)
			}
			continue
		}
		if !info.IsDir() {
			continue
		}
		err = filepath.WalkDir(abs, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				// Skipping an unreadable subtree would make every file beneath
				// it look deleted. Fail closed, as ComputeTombstones does.
				return walkErr
			}
			rel, err := filepath.Rel(home, p)
			if err != nil {
				return err
			}
			rel = normalizeRel(rel)
			// The quarantine root lives under home; syncing it would quarantine
			// the quarantine on every run.
			if rel == homeConflictDirName || strings.HasPrefix(rel, homeConflictDirName+"/") {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
				return nil
			}
			return addFile(p, rel)
		})
		if err != nil {
			return nil, fmt.Errorf("peer home inventory: scanning %s: %w", entry, err)
		}
	}
	return out, nil
}

// computeHomeTombstones diffs the tracked-home snapshot against the home
// baseline. Baseline keys outside the CURRENT tracked entries are not
// deletions: an entry the operator removed from the list retires quietly at
// the next baseline commit, it must not delete the peer's files.
func computeHomeTombstones(snapshot PeerSnapshot, baseline map[string]Fingerprint, entries []string) ([]string, error) {
	var out []string
	for rel := range baseline {
		if err := validateTombstoneRel(rel); err != nil {
			return nil, fmt.Errorf("compute home tombstones: %w", err)
		}
		if f, ok := snapshot[rel]; ok && f.Present {
			continue
		}
		if !underTrackedEntry(rel, entries) {
			continue
		}
		out = append(out, rel)
	}
	sort.Strings(out)
	return out, nil
}

func underTrackedEntry(rel string, entries []string) bool {
	for _, entry := range entries {
		if rel == entry || strings.HasPrefix(rel, entry+"/") {
			return true
		}
	}
	return false
}

// Leaf links are payloads; a link in any parent would escape the home tree.
// Missing parents are legitimate creates/deletes and stop this walk.
func validatePeerHomeAncestors(home string, rels []string) error {
	for _, rel := range rels {
		if err := validateTombstoneRel(rel); err != nil {
			return err
		}
		parts := strings.Split(rel, "/")
		path := home
		for _, part := range parts[:len(parts)-1] {
			path = filepath.Join(path, part)
			info, err := os.Lstat(path)
			if os.IsNotExist(err) {
				break
			}
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return fmt.Errorf("peer home path %q: unsafe ancestor %s", rel, path)
			}
		}
	}
	return nil
}

const homeAncestorsPreflightScript = `set -eu
for rel do
  parent=${rel%/*}
  [ "$parent" != "$rel" ] || continue
  while :; do
    path=$HOME/$parent
    if [ -L "$path" ] || { [ -e "$path" ] && [ ! -d "$path" ]; }; then
      echo "unsafe peer home ancestor: $path" >&2
      exit 43
    fi
    next=${parent%/*}
    [ "$next" != "$parent" ] || break
    parent=$next
  done
done`

// Bound the single remote-command argument well below platform argv limits.
// Siblings share the same ancestor check, so send only one representative.
const peerHomePreflightCommandLimit = 32 * 1024

func peerHomeAncestorCommands(rels []string) ([]string, error) {
	prefix := "sh -c " + shellQuote(homeAncestorsPreflightScript) + " sh"
	command := prefix
	seen := map[string]bool{}
	var commands []string
	for _, rel := range rels {
		if err := validateTombstoneRel(rel); err != nil {
			return nil, err
		}
		separator := strings.LastIndexByte(rel, '/')
		if separator < 0 || seen[rel[:separator]] {
			continue
		}
		seen[rel[:separator]] = true
		argument := " " + shellQuote(rel)
		if len(prefix)+len(argument) > peerHomePreflightCommandLimit {
			return nil, fmt.Errorf("peer home ancestor preflight: path %q exceeds command limit", rel)
		}
		if len(command)+len(argument) > peerHomePreflightCommandLimit {
			commands = append(commands, command)
			command = prefix
		}
		command += argument
	}
	if command != prefix {
		commands = append(commands, command)
	}
	return commands, nil
}

func preflightPeerHomeAncestors(ctx context.Context, runner *exec.Runner, cfg *Config, rels []string) error {
	commands, err := peerHomeAncestorCommands(rels)
	if err != nil {
		return err
	}
	for _, command := range commands {
		if _, err := runner.Run(ctx, "ssh", "-o", "BatchMode=yes", cfg.Target.Host, command); err != nil {
			return fmt.Errorf("peer home ancestor preflight: %w", err)
		}
	}
	return nil
}

// peerHomeScopedDir returns where the tracked pass stages its per-run list
// files: the store on a real run, a temp directory on a preview, mirroring
// the #103 rule that a dry run never materializes the peer store.
func peerHomeScopedDir(cfg *Config, dryRun bool) (string, func(), error) {
	if !dryRun {
		if cfg.ConfigDir == "" {
			return "", nil, fmt.Errorf("peer tracked home: config dir unresolved")
		}
		return cfg.ConfigDir, func() {}, nil
	}
	tmp, err := os.MkdirTemp("", "dot-peer-home-")
	if err != nil {
		return "", nil, fmt.Errorf("creating preview tracked-home dir: %w", err)
	}
	return tmp, func() { _ = os.RemoveAll(tmp) }, nil
}

// writePeerHomeList writes rels NUL-delimited for rsync --files-from --from0,
// the same encoding the workspace plan lists use.
func writePeerHomeList(cfg *Config, name string, rels []string, dryRun bool) (string, func(), error) {
	dir, cleanup, err := peerHomeScopedDir(cfg, dryRun)
	if err != nil {
		return "", nil, err
	}
	path := filepath.Join(dir, name)
	var b strings.Builder
	for _, rel := range rels {
		if err := validateTombstoneRel(rel); err != nil {
			cleanup()
			return "", nil, err
		}
		b.WriteString(rel)
		b.WriteByte(0)
	}
	if err := atomicWrite(path, []byte(b.String())); err != nil {
		cleanup()
		return "", nil, err
	}
	return path, cleanup, nil
}

// peerHomeUntrackedList returns the host-path list with entries the tracked
// pass owns stripped, so the additive --update pass never touches them. With
// no tracked list, or no overlap, the original file is used unchanged and
// untracked behavior stays byte-identical.
func peerHomeUntrackedList(cfg *Config, list string, dryRun bool) (string, func(), error) {
	noop := func() {}
	tracked, err := readPeerHomeTrackedEntries(PeerHomeTrackedFile(cfg.LocalPaths))
	if os.IsNotExist(err) || len(tracked) == 0 {
		return list, noop, nil
	}
	if err != nil {
		return "", nil, err
	}
	body, err := os.ReadFile(list)
	if err != nil {
		return "", nil, err
	}
	var active []string
	for _, line := range strings.Split(string(body), "\n") {
		entry := strings.TrimSpace(line)
		if entry == "" || strings.HasPrefix(entry, "#") {
			continue
		}
		active = append(active, entry)
	}
	kept, changed := filterUntrackedHomeEntries(active, tracked)
	if !changed {
		return list, noop, nil
	}
	dir, cleanup, err := peerHomeScopedDir(cfg, dryRun)
	if err != nil {
		return "", nil, err
	}
	var b strings.Builder
	b.WriteString("# Auto-generated per run by `dot peer sync` — tracked entries removed.\n")
	for _, entry := range kept {
		b.WriteString(entry)
		b.WriteByte('\n')
	}
	path := filepath.Join(dir, "home-paths-untracked.dyn")
	if err := atomicWrite(path, []byte(b.String())); err != nil {
		cleanup()
		return "", nil, err
	}
	return path, cleanup, nil
}

// filterUntrackedHomeEntries drops additive-list entries the tracked pass
// owns. Ownership covers exact matches and nesting in BOTH directions: the
// additive pass transfers directories recursively, so an untracked ancestor
// of a tracked entry (e.g. `.claude` over `.claude/projects/x/memory`) would
// re-sync the tracked subtree with --update semantics, and an untracked
// descendant of a tracked directory is already walked by the tracked pass.
func filterUntrackedHomeEntries(additive, tracked []string) (kept []string, changed bool) {
	for _, entry := range additive {
		normalized := strings.TrimSuffix(filepath.ToSlash(entry), "/")
		owned := false
		for _, t := range tracked {
			if normalized == t || strings.HasPrefix(normalized, t+"/") || strings.HasPrefix(t, normalized+"/") {
				owned = true
				break
			}
		}
		if owned {
			changed = true
			continue
		}
		kept = append(kept, entry)
	}
	return kept, changed
}

// peerHomeRemoteInventory lists the peer's tracked home paths by the same
// empty-destination dry-run trick as the workspace inventory. The tracked
// list itself is the whole filter — the workspace runtime filters do not
// apply outside the workspace.
//
// The canonical list file is never handed to rsync: it carries comments,
// which --files-from reads as literal paths, so a file whose name matches a
// comment line would be inventoried and synced. A sanitized NUL-delimited
// copy of the parsed entries is materialized per run instead, temp-dir'd
// under a preview like every other per-run list (#103).
func peerHomeRemoteInventory(ctx context.Context, runner *exec.Runner, cfg *Config, entries []string, baseline map[string]Fingerprint, dryRun bool) (PeerSnapshot, error) {
	if cfg == nil || !cfg.Target.IsSSH() {
		return nil, fmt.Errorf("peer home inventory: target is not SSH")
	}
	check := append([]string(nil), entries...)
	for rel := range baseline {
		if underTrackedEntry(rel, entries) {
			check = append(check, rel)
		}
	}
	if err := preflightPeerHomeAncestors(ctx, runner, cfg, check); err != nil {
		return nil, err
	}
	listFile, cleanup, err := writePeerHomeList(cfg, "home-tracked-inventory.dyn", entries, dryRun)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	root, err := os.MkdirTemp("", "dot-peer-home-inventory-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(root)

	args := []string{"-r", "-l", "-8", "--dry-run", "--ignore-missing-args", "--from0",
		"--out-format=@@%i\t%l\t%M\t%n\t%L", "--files-from=" + listFile}
	remoteRsync := cfg.RemoteRsyncPath
	if remoteRsync == "" {
		remoteRsync = "rsync"
	}
	// Force both rsync processes to render %M in UTC, as the workspace
	// inventory does.
	args = append(args, "--rsync-path=env TZ=UTC "+remoteRsync)
	args = append(args, "-e", "ssh -o BatchMode=yes -o ConnectTimeout=5", cfg.Target.Host+":", root+"/")
	res, err := runner.Run(ctx, "env", append([]string{"TZ=UTC", cfg.rsyncBin()}, args...)...)
	if err != nil {
		return nil, fmt.Errorf("peer home inventory: %w", err)
	}
	return parsePeerHomeRemoteInventory(res.Stdout, baseline)
}

// Home inventories carry the item type and %L separately from %n. Parsing a
// "name -> target" string would confuse a legal filename containing " -> ".
// Escaped targets fail closed instead of hashing rsync's display spelling.
func parsePeerHomeRemoteInventory(stdout string, baseline map[string]Fingerprint) (PeerSnapshot, error) {
	out := PeerSnapshot{}
	for _, raw := range strings.Split(stdout, "\n") {
		if raw == "" {
			continue
		}
		if !strings.HasPrefix(raw, "@@") {
			// Reuse the workspace warning validation: unknown sockets/FIFOs
			// are omitted, but a baseline payload becoming non-regular or
			// unexpected output still makes the inventory incomplete.
			if _, err := parsePeerRemoteInventory(raw, time.UTC, baseline, false); err != nil {
				return nil, err
			}
			continue
		}
		parts := strings.Split(strings.TrimPrefix(raw, "@@"), "\t")
		if len(parts) != 5 || len(parts[0]) < 2 {
			return nil, fmt.Errorf("peer home inventory: malformed rsync record %q", raw)
		}
		one, err := parsePeerRemoteInventory("@@"+strings.Join(parts[1:4], "\t"), time.UTC, baseline, false)
		if err != nil {
			return nil, err
		}
		for rel, file := range one {
			if _, duplicate := out[rel]; duplicate {
				return nil, fmt.Errorf("peer home inventory: duplicate path %q", rel)
			}
			if parts[0][1] == 'L' {
				if !strings.HasPrefix(parts[4], " -> ") || rsyncEscapeRe.MatchString(parts[4]) {
					return nil, fmt.Errorf("peer home inventory: unsafe link target for %q", rel)
				}
				file.FP = fingerprintLinkTarget(strings.TrimPrefix(parts[4], " -> "), file.FP.Mtime)
			} else if parts[0][1] != 'f' {
				return nil, fmt.Errorf("peer home inventory: unsupported payload type for %q", rel)
			}
			out[rel] = file
		}
	}
	return out, nil
}

// peerHomeTransferArgs are the shared flags for the tracked pull and push.
// There is deliberately no --update: the home baseline, not mtime, decides
// what moves. The scoped files-from list replaces the additive pass's
// exclusion layer.
func peerHomeTransferArgs(cfg *Config, listFile string, dryRun bool) []string {
	args := []string{"-aHAX", "--numeric-ids", "-r", "--human-readable", "--stats",
		"--ignore-missing-args", "--chmod=Du+w",
		"--files-from=" + listFile, "--from0"}
	if cfg.RemoteRsyncPath != "" {
		args = append(args, "--rsync-path="+cfg.RemoteRsyncPath)
	}
	if dryRun {
		args = append(args, "--dry-run")
	}
	return append(args, "-e", "ssh")
}

func peerHomeRemoteRoot(cfg *Config) string { return cfg.Target.Host + ":" }

func pullPeerHomePaths(ctx context.Context, runner *exec.Runner, cfg *Config, rels []string, dryRun bool) error {
	if len(rels) == 0 {
		return nil
	}
	if err := validatePeerHomeAncestors(cfg.HomeDir(), rels); err != nil {
		return err
	}
	if err := preflightPeerHomeAncestors(ctx, runner, cfg, rels); err != nil {
		return err
	}
	list, cleanup, err := writePeerHomeList(cfg, "peer-home-pull.dyn", rels, dryRun)
	if err != nil {
		return err
	}
	defer cleanup()
	args := append(peerHomeTransferArgs(cfg, list, dryRun), peerHomeRemoteRoot(cfg), strings.TrimRight(cfg.HomeDir(), "/")+"/")
	return runPeerRsync(ctx, runner, cfg, args)
}

// peerHomeRemoteBackupRel is the --backup-dir argument for tracked home
// deletes and conflict pushes, relative to the destination home root.
func peerHomeRemoteBackupRel(stamp string) string {
	return homeConflictDirName + "/" + stamp + "/" + homeConflictSubFromPeer
}

func pushPeerHomePlan(ctx context.Context, runner *exec.Runner, cfg *Config, plan *PeerPlan, stamp string, dryRun bool) error {
	if plan == nil {
		return nil
	}
	if len(plan.QuarantineRemote) > 0 {
		// The backup pass writes below the peer's home conflict root. Refuse a
		// symlinked or otherwise unsafe quarantine before rsync interprets
		// --backup-dir.
		if err := preflightPeerHomeQuarantine(ctx, runner, cfg, stamp, !dryRun); err != nil {
			return err
		}
	}
	if err := pushPeerHomePass(ctx, runner, cfg, plan.QuarantineRemote, stamp, true, dryRun); err != nil {
		return err
	}
	quarantine := map[string]bool{}
	for _, rel := range plan.QuarantineRemote {
		quarantine[rel] = true
	}
	normal := make([]string, 0, len(plan.Push))
	for _, rel := range plan.Push {
		if !quarantine[rel] {
			normal = append(normal, rel)
		}
	}
	return pushPeerHomePass(ctx, runner, cfg, normal, stamp, false, dryRun)
}

func pushPeerHomePass(ctx context.Context, runner *exec.Runner, cfg *Config, rels []string, stamp string, backup, dryRun bool) error {
	if len(rels) == 0 {
		return nil
	}
	if err := validatePeerHomeAncestors(cfg.HomeDir(), rels); err != nil {
		return err
	}
	if err := preflightPeerHomeAncestors(ctx, runner, cfg, rels); err != nil {
		return err
	}
	list, cleanup, err := writePeerHomeList(cfg, "peer-home-push.dyn", rels, dryRun)
	if err != nil {
		return err
	}
	defer cleanup()
	args := peerHomeTransferArgs(cfg, list, dryRun)
	if backup {
		args = append(args, "--backup", "--backup-dir="+peerHomeRemoteBackupRel(stamp))
	}
	args = append(args, strings.TrimRight(cfg.HomeDir(), "/")+"/", peerHomeRemoteRoot(cfg))
	return runPeerRsync(ctx, runner, cfg, args)
}

// homeQuarantinePreflightScript is the tracked-home twin of
// quarantinePreflightScript: the root is the receiver's $HOME, the conflict
// root beneath it the whitelisted .dot-peer-conflicts.
const homeQuarantinePreflightScript = `set -eu
stamp=$1
create=$2
q=$HOME/.dot-peer-conflicts
run=$q/$stamp
leaf=$run/from-peer
for p in "$q" "$run" "$leaf"; do
  if [ -L "$p" ] || { [ -e "$p" ] && [ ! -d "$p" ]; }; then
    echo "unsafe peer home quarantine path: $p" >&2
    exit 41
  fi
  if [ "$create" = 1 ] && [ ! -d "$p" ]; then
    mkdir "$p"
  fi
  if [ -e "$p" ] && { [ ! -d "$p" ] || [ -L "$p" ]; }; then
    echo "unsafe peer home quarantine path: $p" >&2
    exit 42
  fi
done`

// preflightPeerHomeQuarantine mirrors preflightPeerQuarantine for the
// whitelisted home conflict root, so a receiver-side symlink cannot turn a
// tracked delete or conflict backup into a write outside $HOME.
func preflightPeerHomeQuarantine(ctx context.Context, runner *exec.Runner, cfg *Config, stamp string, create bool) error {
	if err := validateRemoteConflictRoot(PeerHomeConflictRoot); err != nil {
		return fmt.Errorf("preflight peer home quarantine: %w", err)
	}
	if err := validateConflictStamp(stamp); err != nil {
		return fmt.Errorf("preflight peer home quarantine: %w", err)
	}
	createArg := "0"
	if create {
		createArg = "1"
	}
	command := "sh -c " + shellQuote(homeQuarantinePreflightScript) + " sh " +
		shellQuote(stamp) + " " + createArg
	if _, err := runner.Run(ctx, "ssh", "-o", "BatchMode=yes", cfg.Target.Host, command); err != nil {
		return fmt.Errorf("preflight peer home quarantine: %w", err)
	}
	return nil
}

// propagatePeerHomeDeletes removes locally deleted tracked paths from the
// peer's home, quarantining them under its .dot-peer-conflicts. Same shape as
// the workspace propagateDeletes: NUL-delimited --files-from against a temp
// source root of empty parents, --delete-missing-args scoped to the list.
func propagatePeerHomeDeletes(ctx context.Context, runner *exec.Runner, cfg *Config, stamp string, tombstones []string, dryRun bool) error {
	if len(tombstones) == 0 {
		return nil
	}
	if err := checkTombstoneCap(cfg, tombstones); err != nil {
		return err
	}
	if err := preflightPeerHomeAncestors(ctx, runner, cfg, tombstones); err != nil {
		return err
	}
	baselineFile := peerHomeBaselineFile(cfg.LocalPaths)
	baseline, err := LoadBaselineManifest(baselineFile)
	if err != nil {
		return fmt.Errorf("loading home baseline for delete pass: %w", err)
	}
	for _, rel := range tombstones {
		if err := validateTombstoneRel(rel); err != nil {
			return fmt.Errorf("propagate home deletes: %w", err)
		}
		if _, ok := baseline[rel]; !ok {
			return fmt.Errorf("propagate home deletes: path %q is not in the home baseline", rel)
		}
	}
	if err := preflightPeerHomeQuarantine(ctx, runner, cfg, stamp, !dryRun); err != nil {
		return err
	}
	listFile, cleanup, err := writePeerHomeList(cfg, "home-tombstones.list.dyn", tombstones, dryRun)
	if err != nil {
		return err
	}
	defer cleanup()
	sourceRoot, err := prepareTombstoneSource(filepath.Dir(listFile), tombstones)
	if err != nil {
		return err
	}
	defer os.RemoveAll(sourceRoot)
	args := peerHomeDeletePassArgs(cfg, listFile, sourceRoot, stamp, dryRun)
	fmt.Fprintf(cfg.out(), "  Delete: %d tracked home path(s) → %s (quarantined under %s)\n",
		len(tombstones), peerHomeRemoteRoot(cfg), PeerHomeConflictRoot)
	if dryRun {
		for _, rel := range tombstones {
			fmt.Fprintf(cfg.out(), "    %q → %q\n", rel, peerHomeRemoteBackupRel(stamp)+"/"+rel)
		}
	}
	if err := runDeleteRsync(ctx, runner, cfg, args); err != nil {
		return err
	}
	if dryRun {
		return nil
	}
	entries := make([]Tombstone, 0, len(tombstones))
	now := time.Now().UTC()
	for _, rel := range tombstones {
		entries = append(entries, Tombstone{RelPath: rel, BaselineFP: baseline[rel], DetectedAt: now})
	}
	if err := AppendTombstones(cfg.LocalPaths.TombstonesFile, entries); err != nil {
		return fmt.Errorf("recording home tombstones: %w", err)
	}
	return nil
}

func peerHomeDeletePassArgs(cfg *Config, listFile, sourceRoot, stamp string, dryRun bool) []string {
	args := []string{
		"-r",
		"--human-readable",
		"--stats",
		"--links",
		"--files-from=" + listFile,
		"--from0",
		"--ignore-missing-args",
		"--delete-missing-args",
		"--backup",
		"--backup-dir=" + peerHomeRemoteBackupRel(stamp),
	}
	if dryRun {
		args = append(args, "--dry-run")
	}
	args = append(args, rsyncTransportArgs(cfg)...)
	args = append(args, sourceRoot+"/", peerHomeRemoteRoot(cfg))
	return args
}

// deletePeerHomeLocal accepts a peer-side deletion of a tracked path without
// unlinking the local payload: it is renamed under the local home's
// .dot-peer-conflicts, mirroring DeletePeerLocal with the home as root.
func deletePeerHomeLocal(cfg *Config, stamp string, rels []string, dryRun bool) error {
	if err := validatePeerHomeAncestors(cfg.HomeDir(), rels); err != nil {
		return err
	}
	root := strings.TrimRight(cfg.HomeDir(), "/")
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("peer home local delete: checking home: %w", err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return fmt.Errorf("peer home local delete: home root is not a directory")
	}
	for _, rel := range rels {
		if err := validateTombstoneRel(rel); err != nil {
			return fmt.Errorf("peer home local delete: %w", err)
		}
		src := filepath.Join(root, filepath.FromSlash(rel))
		info, err := os.Lstat(src)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("peer home local delete %s: %w", rel, err)
		}
		if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("peer home local delete %s: refusing non-regular payload", rel)
		}
		if dryRun {
			continue
		}
		dst, err := ensurePeerQuarantineDir(root, homeConflictDirName, stamp, rel)
		if err != nil {
			return fmt.Errorf("peer home local delete quarantine %s: %w", rel, err)
		}
		if _, err := os.Lstat(dst); err == nil {
			return fmt.Errorf("peer home local delete quarantine %s: destination already exists", rel)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("peer home local delete quarantine %s: checking destination: %w", rel, err)
		}
		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("peer home local delete quarantine %s: %w", rel, err)
		}
	}
	return nil
}

// peerHomeTrackedSync runs the baseline-aware transaction over the tracked
// host paths. It is the tracked counterpart of the workspace plan flow:
// three-way plan against the home baseline, scoped pull, local quarantine of
// peer deletes, propagated deletes, push with backup for dual-edit conflicts,
// then its own baseline commit. complete=false means destructive transitions
// were held for want of home baseline provenance; the home baseline advances
// only after the complete transaction succeeds.
// homeTrackedPlan is the tracked host-path pass's three-way plan and the
// evidence the transaction needs after it.
type homeTrackedPlan struct {
	entries  []string
	baseline map[string]Fingerprint
	ready    bool
	local    PeerSnapshot
	plan     *PeerPlan
}

// run is what a run with these direction flags does with the tracked plan.
func (tp *homeTrackedPlan) run(pushOnly, pullOnly bool) (planRun, error) {
	evidence, err := computeHomeTombstones(tp.local, tp.baseline, tp.entries)
	return planRun{pushOnly: pushOnly, pullOnly: pullOnly, authorized: tp.ready, evidence: evidence}, err
}

// planPeerHomeTracked builds the tracked host-path plan without moving
// anything. A nil plan means there is no tracked list, or it is empty.
func planPeerHomeTracked(ctx context.Context, probe *exec.Runner, cfg *Config, progress func(PeerEvent), dryRun bool) (*homeTrackedPlan, error) {
	if cfg.LocalPaths == nil {
		return nil, fmt.Errorf("peer tracked home: local paths unresolved")
	}
	list := PeerHomeTrackedFile(cfg.LocalPaths)
	entries, err := readPeerHomeTrackedEntries(list)
	if os.IsNotExist(err) {
		emitPeer(progress, PeerEvent{Kind: PeerEventHostPathsMissing, Path: list})
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, nil
	}
	emitPeer(progress, PeerEvent{Kind: PeerEventHomeTrackedStart})

	baselineFile := peerHomeBaselineFile(cfg.LocalPaths)
	baseline, err := LoadBaselineManifest(baselineFile)
	if err != nil {
		return nil, fmt.Errorf("peer tracked home: loading baseline: %w", err)
	}
	ready, err := peerHomeBaselineReady(cfg)
	if err != nil {
		return nil, fmt.Errorf("peer tracked home: checking baseline provenance: %w", err)
	}
	local, err := inventoryPeerHomeTracked(cfg.HomeDir(), entries)
	if err != nil {
		return nil, err
	}
	var baselineRels []string
	for rel := range baseline {
		if underTrackedEntry(rel, entries) {
			baselineRels = append(baselineRels, rel)
		}
	}
	if err := validatePeerHomeAncestors(cfg.HomeDir(), baselineRels); err != nil {
		return nil, err
	}
	remote, err := peerHomeRemoteInventory(ctx, probe, cfg, entries, baseline, dryRun)
	if err != nil {
		return nil, err
	}
	plan, err := PlanPeerReconcile(baseline, local, remote)
	if err != nil {
		return nil, fmt.Errorf("peer tracked home: %w", err)
	}
	return &homeTrackedPlan{entries: entries, baseline: baseline, ready: ready, local: local, plan: plan}, nil
}

func peerHomeTrackedSync(ctx context.Context, runner, probe *exec.Runner, cfg *Config, progress func(PeerEvent), dryRun, pushOnly, pullOnly bool) (bool, *homeTrackedPlan, error) {
	tp, err := planPeerHomeTracked(ctx, probe, cfg, progress, dryRun)
	if err != nil || tp == nil {
		return err == nil, nil, err
	}
	entries, baseline, ready, local, plan := tp.entries, tp.baseline, tp.ready, tp.local, tp.plan
	if err := ValidatePeerPlanSafety(cfg, plan); err != nil {
		return false, tp, err
	}
	stamp := NewConflictDir().Timestamp
	complete := true
	// As with the workspace: a target marker authorizes destructive
	// transitions; without one a home deletion stays pending.
	deletesAuthorized := ready

	if !pushOnly {
		if err := pullPeerHomePaths(ctx, runner, cfg, plan.Pull, dryRun); err != nil {
			return false, tp, err
		}
		if cfg.Propagation.Delete && deletesAuthorized && len(plan.DeleteLocal) > 0 {
			if err := deletePeerHomeLocal(cfg, stamp, plan.DeleteLocal, dryRun); err != nil {
				return false, tp, err
			}
		} else if len(plan.DeleteLocal) > 0 {
			complete = false
			emitPeer(progress, PeerEvent{Kind: PeerEventRemoteDeletesHeld})
		}
	}
	if !pullOnly {
		// Deletion evidence comes from the pre-pull snapshot, as in the
		// workspace flow: after a pull, a deletion is indistinguishable from a
		// file this machine never had.
		tombstones, err := computeHomeTombstones(local, baseline, entries)
		if err != nil {
			return false, tp, err
		}
		deleteSet := intersectPeerPaths(tombstones, plan.DeleteRemote)
		if !cfg.Propagation.Delete {
			deleteSet = nil
		}
		if len(deleteSet) > 0 && deletesAuthorized {
			emitPeer(progress, PeerEvent{Kind: PeerEventPropagateDeletesStart})
			if err := propagatePeerHomeDeletes(ctx, runner, cfg, stamp, deleteSet, dryRun); err != nil {
				return false, tp, err
			}
		} else if len(plan.DeleteRemote) > 0 {
			complete = false
			emitPeer(progress, PeerEvent{Kind: PeerEventLocalDeletesHeld})
		}
		if len(plan.Push) > len(plan.QuarantineRemote) {
			// The plan went stale if the peer edited a push path after the
			// inventory; a backup-free overwrite would discard that edit.
			checkBase := make(map[string]Fingerprint, len(baseline)+len(plan.Push))
			for rel, fp := range baseline {
				checkBase[rel] = fp
			}
			for _, rel := range plan.Push {
				if _, ok := checkBase[rel]; !ok {
					checkBase[rel] = Fingerprint{}
				}
			}
			remoteNow, err := peerHomeRemoteInventory(ctx, probe, cfg, entries, checkBase, dryRun)
			if err != nil {
				return false, tp, err
			}
			if err := ValidatePeerPushRemoteStable(plan, remoteNow); err != nil {
				return false, tp, err
			}
		}
		if err := pushPeerHomePlan(ctx, runner, cfg, plan, stamp, dryRun); err != nil {
			return false, tp, err
		}
	}
	if complete && !dryRun && !pullOnly {
		if err := AppendPeerConflictAudit(cfg, plan); err != nil {
			return false, tp, err
		}
	}
	canCommitBaseline := ready ||
		(len(plan.DeleteLocal) == 0 && len(plan.DeleteRemote) == 0)
	if complete && !dryRun && !pushOnly && !pullOnly && canCommitBaseline {
		if err := commitPeerHomeBaseline(cfg, plan.NextBaseline); err != nil {
			return false, tp, err
		}
	}
	return complete, tp, nil
}
