package syncer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// PeerStatusSchemaVersion is the schema version of the `dot peer status --json`
// document. The encoder lives in cli (D-12); the version is shared because the
// coordinator check below reads the same document off the remote machine.
const PeerStatusSchemaVersion = 1

// peerRemoteInventory uses an empty dry-run destination as a portable remote
// listing. `rsync --list-only` ignores --out-format on Apple's rsync, while a
// dry-run against an empty destination emits every remote file through the
// same rsync implementation used by the real transfer. No remote state is
// changed and paths omitted from the listing are unambiguously absent.
func peerRemoteInventory(ctx context.Context, runner *exec.Runner, cfg *Config, rf runtimeFilters, baseline map[string]Fingerprint) (PeerSnapshot, error) {
	if cfg == nil || !cfg.Target.IsSSH() {
		return nil, fmt.Errorf("peer inventory: target is not SSH")
	}
	root, err := os.MkdirTemp("", "dot-peer-inventory-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(root)

	// -8 keeps rsync 3.x from escaping high-bit bytes as \#ooo, which it does
	// for every non-ASCII name when the locale is C (a bare ssh command, a
	// launchd job). The names must arrive as bytes for the NFD check.
	args := []string{"-r", "-8", "--dry-run", "--no-links", "--out-format=@@%l\t%M\t%n"}
	args = append(args, PeerFilterArgs(cfg, rf)...)
	remoteRsync := cfg.RemoteRsyncPath
	if remoteRsync == "" {
		remoteRsync = "rsync"
	}
	// Force both rsync processes to render %M in UTC. A fixed offset obtained
	// from `date +%z` is wrong for historical timestamps across DST changes.
	args = append(args, "--rsync-path=env TZ=UTC "+remoteRsync)
	args = append(args, "-e", "ssh -o BatchMode=yes -o ConnectTimeout=5", cfg.Target.RsyncDest(), root+"/")
	res, runErr := runner.Run(ctx, "env", append([]string{"TZ=UTC", cfg.rsyncBin()}, args...)...)
	if runErr != nil {
		return nil, fmt.Errorf("peer inventory: %w", runErr)
	}
	requireNFD := NFDMigrationMarked(cfg.LocalPaths.WorkspaceRoot)
	snap, err := parsePeerRemoteInventory(res.Stdout, time.UTC, baseline, requireNFD)
	if errors.Is(err, errPeerNameNotNFD) {
		// The name lives on the peer, so the fix runs there (#182).
		return nil, fmt.Errorf("%w on %s; run there: dot sync names normalize --profile=peer --yes (dot peer doctor counts them)", err, cfg.Target.Host)
	}
	return snap, err
}

// errPeerNameNotNFD marks an inventory name the NFD-marked workspace
// requires in NFD but the peer holds in another form.
var errPeerNameNotNFD = errors.New("not NFD-normalized")

// remoteDotCandidates are the shell words the peer probe expands into dot
// candidates, in preference order for equal versions. Tests replace them so
// a probe never reaches a dot installed on the test host.
var remoteDotCandidates = `"$HOME/.local/bin/dot" /opt/homebrew/bin/dot /usr/local/bin/dot /home/linuxbrew/.linuxbrew/bin/dot "$(command -v dot 2>/dev/null || true)"`

// remoteDotProbe lists every executable dot candidate on the peer with the
// first line of its --version banner, one "path<TAB>banner" line each.
func remoteDotProbe(candidates string) string {
	return `# dot-peer: list dot candidates
for c in ` + candidates + `; do
  [ -n "$c" ] && [ -x "$c" ] || continue
  printf '%s\t%s\n' "$c" "$("$c" --version 2>/dev/null | head -n 1)"
done`
}

// remoteDot is the peer's dot binary a run talks to.
type remoteDot struct {
	Path    string
	Banner  string // first --version line, e.g. "dot version 2.70.22 (388a398)"
	release []int  // nil for a dev build or an unreadable banner
	// Pinned says the peer config's remote_dot chose it.
	Pinned bool
	// Passed names the other dot installs the probe passed over, "path
	// (banner)" each; copies of the chosen version are left out.
	Passed []string
}

// Unreleased reports a chosen binary that is not a release build and that no
// remote_dot pin asked for: the fallback when the peer has nothing newer.
func (d *remoteDot) Unreleased() bool {
	return d != nil && d.release == nil && !d.Pinned
}

// String renders the binary for notices: path and version together.
func (d *remoteDot) String() string {
	if d == nil {
		return "unknown"
	}
	banner := d.Banner
	if banner == "" {
		banner = "no version banner"
	}
	return d.Path + " (" + banner + ")"
}

// dotRelease parses "dot version 2.70.22 (sha)" into [2 70 22]. A dev build
// ("dot version dev (sha)") and anything unreadable yield nil.
func dotRelease(banner string) []int {
	fields := strings.Fields(banner)
	if len(fields) < 3 || fields[1] != "version" {
		return nil
	}
	return parseRelease(fields[2])
}

// parseRelease parses "2.70.22" (optionally "v"-prefixed); nil otherwise.
func parseRelease(v string) []int {
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(parts) != 3 {
		return nil
	}
	out := make([]int, 3)
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return nil
		}
		out[i] = n
	}
	return out
}

// dotVersionsDiffer reports two releases that differ: the local version
// string ("2.70.22 (sha)", as cobra renders it) against the peer's banner. A
// dev build on either side is not compared.
func dotVersionsDiffer(local string, remote *remoteDot) bool {
	fields := strings.Fields(local)
	if len(fields) == 0 || remote == nil || remote.release == nil {
		return false
	}
	mine := parseRelease(fields[0])
	return mine != nil && (releaseNewer(mine, remote.release) || releaseNewer(remote.release, mine))
}

// peerDotOlder names the peer's dot when its release is older than local;
// "" otherwise, and for a dev build on either side.
func peerDotOlder(local string, remote *remoteDot) string {
	fields := strings.Fields(local)
	if len(fields) == 0 || remote == nil || remote.release == nil {
		return ""
	}
	if mine := parseRelease(fields[0]); mine != nil && releaseNewer(mine, remote.release) {
		return remote.String()
	}
	return ""
}

func releaseNewer(a, b []int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

// pickRemoteDot chooses the newest release among the probed candidates, so a
// stale dev build at ~/.local/bin/dot no longer shadows the installed
// release (#176). A dev build is used only when nothing else exists; the
// sync and doctor then name it (Unreleased). A candidate whose banner is not
// dot's (Graphviz also installs a `dot`) is ignored unless pinned.
func pickRemoteDot(probe string, pinned bool) (*remoteDot, error) {
	var all []*remoteDot
	seen := map[string]bool{}
	for _, line := range strings.Split(probe, "\n") {
		path, banner, ok := strings.Cut(strings.TrimRight(line, "\r"), "\t")
		banner = strings.TrimSpace(banner)
		if !ok || path == "" || seen[path] || (!pinned && !strings.HasPrefix(banner, "dot version ")) {
			continue
		}
		seen[path] = true
		all = append(all, &remoteDot{Path: path, Banner: banner, release: dotRelease(banner), Pinned: pinned})
	}
	if len(all) == 0 {
		return nil, fmt.Errorf("peer dot binary is missing from supported install locations")
	}
	best := all[0]
	for _, cand := range all[1:] {
		if cand.release != nil && (best.release == nil || releaseNewer(cand.release, best.release)) {
			best = cand
		}
	}
	for _, cand := range all {
		if cand != best && cand.Banner != best.Banner {
			best.Passed = append(best.Passed, cand.String())
		}
	}
	return best, nil
}

// resolveRemoteDot probes the peer once per run and caches the choice on the
// config. The peer profile's remote_dot pins the path (the way to use a dev
// build, read by scheduled runs too); it must still exist and run there.
func resolveRemoteDot(ctx context.Context, runner *exec.Runner, cfg *Config) (*remoteDot, error) {
	if cfg.remoteDot != nil {
		return cfg.remoteDot, nil
	}
	dot, err := probePeerDot(ctx, runner, cfg.Target.Host, cfg.RemoteDot)
	if err != nil {
		return nil, err
	}
	cfg.remoteDot = dot
	return dot, nil
}

// probePeerDot picks the dot on host: pin (a remote_dot) when set, which
// must exist and run there, else the newest release among the candidates.
func probePeerDot(ctx context.Context, runner *exec.Runner, host, pin string) (*remoteDot, error) {
	candidates := remoteDotCandidates
	pinned := strings.TrimSpace(pin)
	if pinned != "" {
		candidates = remoteShellPath(pinned)
	}
	res, err := runner.Run(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", host, remoteDotProbe(candidates))
	if err != nil {
		// The command is the whole probe script; say what failed instead.
		var cmdErr *exec.CmdError
		// %w keeps ssh's exit status for the process exit code.
		if errors.As(err, &cmdErr) {
			if details := cmdErr.Details(); details != "" {
				return nil, fmt.Errorf("peer dot probe on %s: %w: %s", host, cmdErr.Err, details)
			}
			return nil, fmt.Errorf("peer dot probe on %s: %w", host, cmdErr.Err)
		}
		return nil, fmt.Errorf("peer dot probe on %s: %w", host, err)
	}
	dot, err := pickRemoteDot(res.Stdout, pinned != "")
	if err != nil {
		if pinned != "" {
			return nil, fmt.Errorf("remote_dot %q in the peer config is not an executable on %s", pinned, host)
		}
		return nil, fmt.Errorf("%s on %s", err, host)
	}
	return dot, nil
}

// ResolvePeerDotPath is the dot on host that dot peer would run there (#176):
// pin, the peer profile's remote_dot, when set; otherwise the newest release
// among its install locations, so a stale dev build at ~/.local/bin does not
// shadow it. It serves callers outside the peer profile, such as dot ai
// memory sync --peer (#195).
func ResolvePeerDotPath(ctx context.Context, runner *exec.Runner, host, pin string) (string, error) {
	dot, err := probePeerDot(ctx, runner, host, pin)
	if err != nil {
		return "", err
	}
	return dot.Path, nil
}

// remoteShellPath quotes a peer path for the remote shell, letting a leading
// "~/" expand to the peer's home.
func remoteShellPath(path string) string {
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		return `"$HOME"/` + shellQuote(rest)
	}
	return shellQuote(path)
}

func remotePeerStatusCommand(dot string) string {
	return "exec " + shellQuote(dot) + " peer status --json"
}

func remotePeerNormalizeCommand(dot string) string {
	return "exec " + shellQuote(dot) + " sync names normalize --profile=peer --yes"
}

// remotePeerStatus is the narrow view of a remote `dot peer status --json`
// document the coordinator check reads. The full document is assembled by the
// cli encoder (D-12); decoding only the coordinator fields keeps the engine
// off that type. Unknown fields are ignored, as they were before the move.
//
// Worktrees is optional: a remote on a previous release omits it, decoding
// yields nil, and the run falls back to the stored ∪ local union. The schema
// version stays 1; new fields are additive only.
type remotePeerStatus struct {
	SchemaVersion int      `json:"schemaVersion"`
	Kind          string   `json:"kind"`
	Worktrees     []string `json:"worktrees"`
	// UnscannedWorktreeDirs are the directories the remote could not read
	// during its worktree scan; this Mac's filters judge them (#268).
	UnscannedWorktreeDirs []string `json:"unscannedWorktreeDirs"`
	OwnerEpoch            int      `json:"ownerEpoch"`
	FencePending          bool     `json:"fencePending"`
	DotVersion            string   `json:"dotVersion"`
	Profile               struct {
		Configured   bool     `json:"configured"`
		Owner        string   `json:"owner"`
		OwnerAliases []string `json:"ownerAliases"`
		MachineNames []string `json:"machineNames"`
		// CanPush is the remote's own owner guard: true means that machine
		// believes it is the coordinator.
		CanPush       bool   `json:"canPush"`
		WorkspacePath string `json:"workspacePath"`
		Target        struct {
			Path string `json:"path"`
		} `json:"target"`
	} `json:"profile"`
	// Job is the remote's peer scheduler; State is "not installed" when it
	// has none.
	Job struct {
		State string `json:"state"`
	} `json:"job"`
}

// checkRemotePeerOwner makes the single-coordinator invariant bilateral. A
// local owner guard alone is insufficient: two independently initialized Macs
// can each name themselves and both scheduled jobs would pass their own guard.
// The validated document is returned so the run can reuse what it carries
// (the remote's linked-worktree list) without a second ssh round trip.
func checkRemotePeerOwner(ctx context.Context, runner *exec.Runner, cfg *Config) (*remotePeerStatus, error) {
	status, err := fetchRemotePeerStatus(ctx, runner, cfg)
	if strings.TrimSpace(cfg.Owner) == "" {
		// Name the peer's owner when it can be read (#202).
		hint := noLocalOwnerFix(fenceSide{})
		if err == nil {
			hint = noLocalOwnerFix(remoteFenceSide(status))
		}
		return nil, fmt.Errorf("peer coordinator check: local peer owner is empty; %s", hint)
	}
	if err != nil {
		return nil, err
	}
	if err := checkRemotePeerTopology(cfg, status); err != nil {
		return nil, err
	}
	if err := checkRemotePeerOwnerMatch(cfg, status); err != nil {
		return nil, err
	}
	return status, nil
}

// fetchRemotePeerStatus reads the remote `dot peer status --json` document
// and validates only its envelope (schema, kind, configured). Owner and
// topology checks stay with the caller: PeerSchedule wants the bilateral
// owner agreement, while PeerSync resolves owner divergence through the
// epoch fence.
func fetchRemotePeerStatus(ctx context.Context, runner *exec.Runner, cfg *Config) (*remotePeerStatus, error) {
	if cfg == nil || !cfg.Target.IsSSH() {
		return nil, fmt.Errorf("peer coordinator check: target is not SSH")
	}
	dot, err := resolveRemoteDot(ctx, runner, cfg)
	if err != nil {
		return nil, fmt.Errorf("peer coordinator check: %w", err)
	}
	res, err := runner.Run(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", cfg.Target.Host, remotePeerStatusCommand(dot.Path))
	if err != nil {
		return nil, fmt.Errorf("peer coordinator check: reading remote peer status: %w", err)
	}
	return parseRemotePeerStatusDoc(res.Stdout)
}

// parseRemotePeerStatusDoc validates only the document envelope. Epoch
// fields stay zero when a previous release omits them, which the fence reads
// as "no epoch support".
func parseRemotePeerStatusDoc(raw string) (*remotePeerStatus, error) {
	var status remotePeerStatus
	if err := json.Unmarshal([]byte(raw), &status); err != nil {
		return nil, fmt.Errorf("peer coordinator check: invalid remote status JSON: %w", err)
	}
	if status.SchemaVersion != PeerStatusSchemaVersion || status.Kind != "peer" || !status.Profile.Configured {
		return nil, fmt.Errorf("peer coordinator check: remote peer profile is not configured with the supported schema")
	}
	return &status, nil
}

// parseRemotePeerStatus is the full pre-epoch validation: envelope, topology
// and owner match. Focused tests use it directly.
func parseRemotePeerStatus(cfg *Config, raw string) (*remotePeerStatus, error) {
	status, err := parseRemotePeerStatusDoc(raw)
	if err != nil {
		return nil, err
	}
	if err := checkRemotePeerTopology(cfg, status); err != nil {
		return nil, err
	}
	if err := checkRemotePeerOwnerMatch(cfg, status); err != nil {
		return nil, err
	}
	return status, nil
}

// fetchRemotePeerWorktrees reads the remote's linked-worktree list from the
// same status document, without the coordinator validation: `peer diff` runs
// it on machines where the owner check is not its business. A remote on a
// previous release answers with no worktrees field and the list is empty.
func fetchRemotePeerWorktrees(ctx context.Context, runner *exec.Runner, cfg *Config) (worktrees, unscanned []string, err error) {
	status, err := fetchRemotePeerStatus(ctx, runner, cfg)
	if err != nil {
		return nil, nil, err
	}
	return status.Worktrees, status.UnscannedWorktreeDirs, nil
}

// checkRemotePeerTopology proves the remote profile points back at this
// workspace: the pair must be exactly two machines, each naming the other.
func checkRemotePeerTopology(cfg *Config, status *remotePeerStatus) error {
	return topologyError(cfg.LocalPath, cfg.Target.Path, status.Profile.WorkspacePath, status.Profile.Target.Path)
}

// topologyError is the fence's first check: the remote profile must point
// back at this workspace. The doctor asks it from the coordinator's side.
func topologyError(localWorkspace, localTarget, remoteWorkspace, remoteTarget string) error {
	wantRemoteWorkspace := filepath.Clean(localTarget)
	wantRemoteTarget := filepath.Clean(strings.TrimRight(localWorkspace, "/"))
	if filepath.Clean(remoteWorkspace) != wantRemoteWorkspace || filepath.Clean(remoteTarget) != wantRemoteTarget {
		return fmt.Errorf(
			"peer coordinator check: remote profile does not point back to this workspace (remote workspace %q target %q; expected %q -> %q)",
			remoteWorkspace, remoteTarget, wantRemoteWorkspace, wantRemoteTarget)
	}
	return nil
}

// checkRemotePeerOwnerMatch is the pre-epoch refusal: without an epoch to
// order them, two different owners can never both proceed.
func checkRemotePeerOwnerMatch(cfg *Config, status *remotePeerStatus) error {
	return ownerMatchError(localFenceSide(cfg), remoteFenceSide(status))
}

func ownerMatchError(local, remote fenceSide) error {
	if strings.TrimSpace(remote.Owner) == "" && strings.TrimSpace(local.Owner) != "" {
		// A cleared peer passes its own guard, but `--set` on both Macs
		// would bump them apart and demote this one (#202).
		return fmt.Errorf("peer coordinator check: the peer records no owner (a `dot sync owner --clear` there?) — %s", noPeerOwnerFix(local, remote))
	}
	if remote.CanPush {
		return fmt.Errorf(
			"peer coordinator check: the peer also passes its own owner guard (its owner %q, local %q); two coordinators would write to each other. Set one owner on both machines with `dot sync owner --profile=peer --set <coordinator>`",
			remote.Owner, local.Owner)
	}
	if NormalizeHostname(local.Owner) == "" || !sameOwner(local.Owner, local.Aliases, remote.Owner, remote.Aliases) {
		return fmt.Errorf(
			"peer coordinator check: both profiles must name the same owner (local %q, remote %q); set the remote profile to %q and keep its scheduler off",
			local.Owner, remote.Owner, local.Owner)
	}
	return nil
}

func normalizeRemotePeerNames(ctx context.Context, runner *exec.Runner, cfg *Config) error {
	dot, err := resolveRemoteDot(ctx, runner, cfg)
	if err != nil {
		return fmt.Errorf("normalizing peer workspace names: %w", err)
	}
	if _, err := runner.Run(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", cfg.Target.Host, remotePeerNormalizeCommand(dot.Path)); err != nil {
		return fmt.Errorf("normalizing peer workspace names: %w", err)
	}
	return nil
}

func parsePeerRemoteInventory(stdout string, remoteLoc *time.Location, baseline map[string]Fingerprint, requireNFD bool) (PeerSnapshot, error) {
	out := PeerSnapshot{}
	for _, raw := range strings.Split(stdout, "\n") {
		if raw == "" {
			continue
		}
		if !strings.HasPrefix(raw, "@@") {
			const skippedPrefix = "skipping non-regular file "
			if strings.HasPrefix(raw, skippedPrefix) {
				skipped, err := strconv.Unquote(strings.TrimPrefix(raw, skippedPrefix))
				if err != nil {
					return nil, fmt.Errorf("peer inventory: malformed non-regular warning %q", raw)
				}
				rel, err := cleanPeerInventoryRel(skipped, false)
				if err != nil {
					return nil, err
				}
				if _, tracked := baseline[rel]; tracked {
					return nil, fmt.Errorf("peer inventory: baseline path %q is non-regular on the peer", rel)
				}
				// Unknown symlinks and sockets were never payloads. --no-links
				// deliberately omits them, so they cannot be edits or deletions.
				continue
			}
			// Stdout is reserved for the machine-readable --out-format. Treat a
			// continuation from a newline-bearing filename, or any other surprise,
			// as an incomplete inventory and fail closed.
			return nil, fmt.Errorf("peer inventory: unexpected rsync output %q", raw)
		}
		parts := strings.SplitN(strings.TrimPrefix(raw, "@@"), "\t", 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("peer inventory: malformed rsync record %q", raw)
		}
		rawRel := parts[2]
		isDir := strings.HasSuffix(rawRel, "/")
		rel, err := cleanPeerInventoryRel(rawRel, isDir)
		if err != nil {
			return nil, err
		}
		if rsyncEscapeRe.MatchString(rel) {
			return nil, fmt.Errorf("peer inventory: rsync printed the name %q with \\#ooo octal escapes, not an NFD problem: the name holds a control character (Finder's Icon\\r, say) or a literal \\# before digits, which rsync always escapes, or the client is openrsync; rename or exclude that file", rel)
		}
		if requireNFD && rel != "" && !NFDPathNormalized(rel) {
			return nil, fmt.Errorf("peer inventory: path %q is %w", rel, errPeerNameNotNFD)
		}
		// The dry-run listing includes directory traversal records. Inventory is
		// file-only, so skip them before trimming the rsync directory marker.
		// Keeping this check before TrimSuffix is important: treating a directory
		// as a zero-byte file would manufacture a peer change for every subtree.
		if isDir || rel == "" {
			continue
		}
		size, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("peer inventory: malformed size %q: %w", parts[0], err)
		}
		if size < 0 {
			return nil, fmt.Errorf("peer inventory: malformed negative size %q", parts[0])
		}
		mtime, err := parsePeerRsyncTime(parts[1], remoteLoc)
		if err != nil {
			return nil, fmt.Errorf("peer inventory: malformed mtime for %q: %w", rel, err)
		}
		if _, duplicate := out[rel]; duplicate {
			return nil, fmt.Errorf("peer inventory: duplicate path %q", rel)
		}
		out[filepath.ToSlash(rel)] = PeerFile{
			Present: true,
			FP:      Fingerprint{Size: size, Mtime: mtime},
		}
	}
	return out, nil
}

// rsyncEscapeRe matches rsync's \#ooo octal escape in an --out-format name.
// openrsync, and rsync 3.x without -8, print it for every non-ASCII byte;
// rsync 3.x with -8 still prints it for control characters and for a literal
// backslash before '#' and digits. Either way the inventory would read the
// escape as the file's real name, so it fails closed.
var rsyncEscapeRe = regexp.MustCompile(`\\#[0-7]{3}`)

func cleanPeerInventoryRel(raw string, isDir bool) (string, error) {
	if strings.ContainsAny(raw, "\x00\r\n\t") {
		return "", fmt.Errorf("peer inventory: unsafe path %q", raw)
	}
	rel := strings.TrimPrefix(raw, "./")
	if isDir {
		rel = strings.TrimSuffix(rel, "/")
	}
	if rel == "" || rel == "." {
		return "", nil
	}
	if filepath.IsAbs(filepath.FromSlash(rel)) {
		return "", fmt.Errorf("peer inventory: unsafe path %q", raw)
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel)))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != filepath.ToSlash(rel) {
		return "", fmt.Errorf("peer inventory: unsafe path %q", raw)
	}
	return clean, nil
}

func parsePeerRsyncTime(raw string, loc *time.Location) (time.Time, error) {
	if loc == nil {
		loc = time.UTC
	}
	for _, layout := range []string{"2006/01/02-15:04:05", "2006/01/02 15:04:05"} {
		if t, err := time.ParseInLocation(layout, strings.TrimSpace(raw), loc); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported rsync time %q", raw)
}

func intersectPeerPaths(a, b []string) []string {
	want := make(map[string]bool, len(b))
	for _, rel := range b {
		want[rel] = true
	}
	out := make([]string, 0, len(a))
	for _, rel := range a {
		if want[rel] {
			out = append(out, rel)
		}
	}
	sort.Strings(out)
	return out
}

// peerClockSkew reports peer-time minus local-time.
func peerClockSkew(ctx context.Context, runner *exec.Runner, host string) (time.Duration, error) {
	res, err := runner.Run(ctx, "ssh", "-o", "BatchMode=yes", host, "date -u +%s")
	if err != nil {
		return 0, err
	}
	remote, err := strconv.ParseInt(strings.TrimSpace(res.Stdout), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("unexpected peer clock output %q", strings.TrimSpace(res.Stdout))
	}
	return time.Duration(remote-time.Now().UTC().Unix()) * time.Second, nil
}
