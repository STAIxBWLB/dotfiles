package syncer

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// Peer deletions are normally tens of inbox paths. Keep the new-profile cap
// well below the mirror's broader 1000-path default.
const peerDefaultMaxDelete = 100

// peerAllowHeader seeds the peer profile's allow.txt. The cloud mirror keeps
// secrets out; a peer is a second machine the operator already trusts with the
// same work, and without env files and tokens the workspace does not actually
// run there. That difference in intent is exactly why the two profiles have
// separate allow files.
const peerAllowHeader = `# dot peer allow.txt — secrets opt-in for machine-to-machine sync.
#
# Unlike the cloud mirror, a peer is another machine you already trust with
# this workspace, and the workspace does not function there without these.
# The transport is ssh, so these never touch a cloud provider.
#
# Remove a line to stop syncing that class of secret.
/.maru/secrets/**
/.secrets/**
**/.env
**/.env.*
`

// peerHomePathsHeader documents the second rsync pass.
const peerHomePathsHeader = `# dot peer home-paths.txt — host-local paths carried to the peer.
#
# One path per line, relative to $HOME. Comments start with '#'.
# These sit outside the workspace but the workspace does not work without
# them: 16 of this workspace's 19 submodule remotes are SSH, so ~/.ssh is
# load-bearing, and the MCP server list lives in ~/.claude.json.
#
# Deliberately absent, because copying them breaks the target:
#   .maru/env                  venv console scripts bake an absolute
#                              interpreter path — rebuild, never copy
#   .maru/skills/_builtin      re-materialized by the Maru app
#   .maru/skills/_sources      branch-specific checkouts; reproduce, don't copy
#   .claude/plugins/cache      cache
#   .codex/sessions            history
#   .codex/auth.json           credential; re-auth instead
#   .codex/config.toml         machine-local: Codex rewrites it continuously
#                              (per-project trust, hook state, plugin flags),
#                              and its MCP server definitions hash-key the
#                              Keychain-stored MCP OAuth credentials — copying
#                              a peer's config orphans them on both machines
#
# Also unreachable by any file copy: tokens in the macOS keychain (gh, for
# one). They cannot be transferred and cannot even be verified over ssh.
#
# ~/.ssh is listed as a directory but known_hosts is excluded in code: it is
# per-machine trust state, merging it is meaningless, and overwriting it with the
# peer's copy deletes the host key for the channel this sync runs over. That is
# not hypothetical - it happened on the first real run and broke the next ssh.
.ssh
.gitconfig
.config/git
.config/dotfiles
.config/shell/90-secrets.sh
.claude.json
.claude/settings.json
.claude/settings.local.json
.claude/skills
.claude/plans
.agents
.codex/hooks.json
.codex/memories
.maru/settings.json
.maru/sites.json
.maru/agents.json
.maru/state
.maru/telegram
`

// peerHomeTrackedHeader documents the tracked subset of the host paths. The
// payload entry is appended by peerHomeTrackedSeed because Claude keys its
// project memory directory by the workspace path, which differs per machine.
const peerHomeTrackedHeader = `# dot peer home-paths-tracked.txt — host paths with delete propagation.
#
# One path per line, relative to $HOME, no trailing slash. Comments start
# with '#'. An entry listed here is synced by the tracked pass instead of the
# additive one: a delete on one machine removes the peer's copy (quarantined
# under ~/.dot-peer-conflicts on the receiving side), and a simultaneous edit
# on both machines quarantines the losing payload there too. Entries need not
# appear in home-paths.txt; when they do, this list owns them.
#
# Everything left in home-paths.txt alone keeps newest-mtime-wins additive
# behavior — deliberate for host runtime state, wrong for user-authored
# content like the Claude auto-memory below.
#
# The seeded Claude auto-memory entry appears only when the peer shares this
# machine's workspace path: Claude derives its project directory from that
# path, so with different workspace layouts the entry would name the wrong
# directory on one side. In that case add entries by hand with
# dot peer home-paths tracked set.
`

// peerHomeTrackedSeed renders the tracked list seeded by PeerInit and
// PeerSchedule. Claude Code names its project directory after the workspace
// path with '/', '.' and '_' flattened to '-', so the auto-memory entry is
// derived rather than part of the static header — and is included only when
// the peer's workspace path matches, since the flattened name is otherwise
// wrong on one machine.
func peerHomeTrackedSeed(cfg *Config, remotePath string) string {
	if remotePath != strings.TrimRight(cfg.LocalPath, "/") {
		return peerHomeTrackedHeader
	}
	project := strings.NewReplacer("/", "-", ".", "-", "_", "-").
		Replace(strings.TrimRight(cfg.LocalPath, "/"))
	return peerHomeTrackedHeader + ".claude/projects/" + project + "/memory\n"
}

// seedPeerHomeTracked writes the tracked host-path list when absent and
// reports whether it created the file. PeerInit and PeerSchedule (the
// documented refresh path after an upgrade) share it; PeerSync deliberately
// never calls it — an operator who deletes the file disables the feature and
// must not find it silently recreated by the next run.
func seedPeerHomeTracked(cfg *Config, remotePath string) (bool, error) {
	if cfg.LocalPaths == nil {
		// No resolved peer store means no peer profile to seed into; the
		// scheduler install (the caller's primary action) still proceeds.
		return false, nil
	}
	path := PeerHomeTrackedFile(cfg.LocalPaths)
	if _, err := os.Stat(path); err == nil {
		return false, nil
	}
	if err := seedFileIfAbsent(path, peerHomeTrackedSeed(cfg, remotePath)); err != nil {
		return false, err
	}
	return true, nil
}

// The PATH is explicit for the same reason the mirror unit's is: launchd hands a
// job a minimal PATH that finds Apple's /usr/bin/rsync (openrsync) before
// Homebrew's 3.x. A peer transfer with the wrong rsync fails at data time, not
// at probe time.
//
// DOT_SCHEDULED_RUN is how the run tells cli it is scheduled: launchd sets no
// distinguishing variable of its own, a TTY check misfires under CI and under
// a pipe, and a persistent flag would advertise a mode no human invokes. The
// dict was already here, so the marker costs one entry. It is read in exactly
// one place, internal/cli/peer_cmd.go.
const peerPlistTmpl = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.dotfiles.peer</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>peer</string>
    <string>sync</string>%s
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string>
    <key>DOT_SCHEDULED_RUN</key>
    <string>1</string>
  </dict>
  <key>StartInterval</key>
  <integer>%d</integer>
  <key>RunAtLoad</key>
  <true/>
  <key>StandardOutPath</key>
  <string>%s</string>
  <key>StandardErrorPath</key>
  <string>%s</string>
</dict>
</plist>
`

var peerExecutable = os.Executable

// peerClockTolerance is how far the two clocks may drift before "newer wins"
// stops being a meaningful comparison.
const peerClockTolerance = 2 * time.Second

// PeerEventKind names one observable step of a peer run. The engine emits
// kinds; cli owns the wording (D-06/D-10).
type PeerEventKind int

const (
	PeerEventPullStart             PeerEventKind = iota // the pull-from-peer pass begins
	PeerEventRemoteDeletesHeld                          // remote-only deletes withheld for want of provenance
	PeerEventPropagateDeletesStart                      // the delete-propagation pass begins
	PeerEventLocalDeletesHeld                           // local deletes withheld for want of provenance
	PeerEventPushStart                                  // the push-to-peer pass begins
	PeerEventHostPathsStart                             // the additive host-path pass begins
	PeerEventHostPathsMissing                           // no host-path list exists; Path names where one would live
	PeerEventHomeTrackedStart                           // the tracked host-path pass begins
	PeerEventPartialTransfer                            // rsync moved some but not all of a pass; Err carries it
	PeerEventPeerLacksHandover                          // peer dot predates epochs; Path names the binary, "path (banner)"
	PeerEventReplicaPushFailed                          // the replica push after a complete run failed; Err carries it
	PeerEventDotVersionMismatch                         // peer dot release differs from this one; Path names the peer binary
	PeerEventPeerDotUnreleased                          // the peer has only a non-release dot and no remote_dot pin; Path names it
	PeerEventHostMerged                                 // a host_merge file was merged on both machines; Path names it
	PeerEventHostMergeHeld                              // a one-way run leaves a differing host_merge file alone; Path names it
	PeerEventOwnerAliasesRetired                        // both machines record the renamed owner; Path lists the profiles cleared
	PeerEventOwnerRenamePending                         // the peer still records this Mac's earlier name; Path is the command to run there
)

// PeerEvent is one step outcome. Only the fields its kind documents are set.
type PeerEvent struct {
	Kind PeerEventKind
	Path string
	Err  error
}

func emitPeer(progress func(PeerEvent), e PeerEvent) {
	if progress != nil {
		progress(e)
	}
}

// PeerHomePathsFile is the host-path allowlist inside a peer store.
func PeerHomePathsFile(paths *LocalPaths) string {
	return filepath.Join(paths.StoreDir, "home-paths.txt")
}

// PeerHomeTrackedFile is the tracked subset of the host-path list inside a
// peer store. Its entries get delete propagation and conflict quarantine;
// everything else stays additive.
func PeerHomeTrackedFile(paths *LocalPaths) string {
	return filepath.Join(paths.StoreDir, "home-paths-tracked.txt")
}

// peerHomeArg renders the ProgramArguments entry that re-supplies --home on
// every scheduled run, or nothing when the job was installed without an
// override. The value is interpolated raw, exactly as the executable path and
// the log path already are.
func peerHomeArg(cfg *Config) (string, error) {
	if cfg == nil || cfg.Home == "" {
		return "", nil
	}
	arg, err := plistHomeArgument(cfg.Home)
	if err != nil {
		return "", err
	}
	return "\n    <string>" + arg + "</string>", nil
}

func seedFileIfAbsent(path, body string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0o644)
}

// PeerInitOptions controls PeerInit. Host is already validated by the caller;
// an empty RemotePath means "same absolute layout on both machines".
type PeerInitOptions struct {
	Config     *Config
	Host       string
	RemotePath string

	// Runner is read for its own DryRun flag only — PeerInit writes files
	// directly and routes nothing through it. It is here so a caller that set
	// the flag on the runner and not on the option still gets a preview, the
	// same reconciliation the three aisettings managers make.
	Runner *exec.Runner

	// DryRun computes the profile and reports the files without writing any.
	DryRun bool
}

// PeerInitResult is what the operator is told about the profile just written,
// or — when DryRun is set — about the one that would have been.
type PeerInitResult struct {
	// DryRun reports that none of the files below were written. The
	// engine returns the flag and the paths; cli owns every string.
	DryRun          bool
	Target          string
	StoreDir        string
	ConfigFile      string
	AllowFile       string
	HomePathsFile   string
	HomeTrackedFile string
	Propagation     PropagationPolicy
	MaxDelete       int
}

// PeerInit writes the peer store: target, secrets opt-in, and the two
// host-path lists (additive and tracked).
func PeerInit(opts PeerInitOptions) (*PeerInitResult, error) {
	cfg := opts.Config
	paths := cfg.LocalPaths
	if paths == nil {
		return nil, fmt.Errorf("peer store unresolved")
	}
	remotePath := opts.RemotePath
	if remotePath == "" {
		// Same absolute layout on both machines is the norm here; the
		// workspace path is part of the environment being replicated.
		remotePath = strings.TrimRight(cfg.LocalPath, "/")
	}

	local, ok, err := LoadLocalConfig(paths)
	if err != nil {
		return nil, err
	}
	newProfile := !ok || local == nil
	if newProfile {
		local = &LocalConfig{}
	}
	local.Target = "ssh:" + opts.Host + ":" + remotePath
	// exclude-mode, not include-mode. The mirror uses an allowlist
	// (tracked files plus a binary-extension list) because untracked text
	// round-trips through Git anyway. A peer is different: "either machine
	// can continue the same work" has to cover an untracked scratch note,
	// which is neither tracked nor a binary extension and would otherwise
	// sync by neither channel. Measured: a new .md file never arrived.
	//
	// Safe because the junk layer already excludes the caches that make
	// this expensive - node_modules, target, .next, .venv, __pycache__ -
	// and secrets remain governed by allow.txt.
	local.FilterMode = FilterModeExclude
	if newProfile {
		// Deletions propagate for a new peer profile. Without this a file
		// removed on one machine returns on the next pull. Re-running init
		// must preserve an operator's later opt-out.
		local.Propagation = PropagationPolicy{Create: true, Update: true, Delete: true}
		local.MaxDelete = peerDefaultMaxDelete
	}
	local.IncludeSubmodules = true
	// Peer profiles are per-machine by construction (the store is
	// gitignored), so the owner is simply this machine. Use the DNS-safe
	// name rather than os.Hostname(), which can be the generic "Mac".
	if name := PreferredMachineName(); name != "" {
		AssignOwner(local, name)
	}

	result := &PeerInitResult{
		DryRun:          opts.DryRun || (opts.Runner != nil && opts.Runner.DryRun),
		Target:          local.Target,
		StoreDir:        paths.StoreDir,
		ConfigFile:      paths.ConfigFile,
		AllowFile:       paths.AllowFile,
		HomePathsFile:   PeerHomePathsFile(paths),
		HomeTrackedFile: PeerHomeTrackedFile(paths),
		Propagation:     local.Propagation,
		MaxDelete:       local.MaxDelete,
	}
	// The writes below bypass the runner entirely, so the flag has to be
	// honored here or a preview writes twelve files (BUG-13, D-03). Everything
	// the operator is shown was computed above and is identical either way.
	if result.DryRun {
		return result, nil
	}

	if err := SaveLocalConfig(paths, local); err != nil {
		return nil, err
	}
	if err := seedFileIfAbsent(paths.AllowFile, peerAllowHeader); err != nil {
		return nil, err
	}
	if err := seedFileIfAbsent(PeerHomePathsFile(paths), peerHomePathsHeader); err != nil {
		return nil, err
	}
	if _, err := seedPeerHomeTracked(cfg, remotePath); err != nil {
		return nil, err
	}
	return result, nil
}

// PeerDiffOptions controls PeerDiff. Probe is the always-live runner: a
// divergence report is read-only, so it must work under --dry-run.
type PeerDiffOptions struct {
	Config *Config
	Probe  *exec.Runner
	// Itemize also plans the tracked and additive host-path passes and
	// lists every action (#180).
	Itemize bool
}

// PeerDiffResult carries the divergence plan, or the fact that the peer was
// away. An unreachable peer is not an error. Items is set under Itemize, and
// is returned with the error when the workspace plan fails a safety check,
// so the refused deletions can be seen.
type PeerDiffResult struct {
	Unreachable bool
	Plan        *PeerPlan
	Items       *PeerRunPlan
}

// PeerDiff reports paths where the two machines disagree.
func PeerDiff(ctx context.Context, opts PeerDiffOptions) (*PeerDiffResult, error) {
	cfg := opts.Config
	if !cfg.Target.IsSSH() {
		return nil, fmt.Errorf("peer target is not configured; run dot peer init first")
	}
	if err := validateHostMerge(cfg.HostMerge); err != nil {
		return nil, err
	}
	if err := CheckSSH(ctx, opts.Probe, cfg.Target.Host); err != nil {
		return &PeerDiffResult{Unreachable: true}, nil
	}
	if err := resolvePeerRsync(ctx, opts.Probe, cfg); err != nil {
		return nil, err
	}

	// The plan must apply the same sticky worktree exclude a real run would,
	// or the displayed divergence disagrees with the transaction that follows
	// it. The fetch is best-effort: a diff stays useful when the remote
	// status cannot be read, and the stored ∪ local union still covers every
	// worktree any earlier complete run has seen. `peer diff` is always a
	// preview, so the union is never persisted here.
	var remoteWorktrees []string
	if wt, err := fetchRemotePeerWorktrees(ctx, opts.Probe, cfg); err == nil {
		remoteWorktrees = wt
	}
	worktrees, err := MergePeerWorktrees(cfg, remoteWorktrees, false)
	if err != nil {
		return nil, err
	}
	cfg.WorktreeExcludes = worktrees

	release, err := AcquireLockForRun(cfg.LockDir, true)
	if err != nil {
		return nil, fmt.Errorf("another sync is already running: %w", err)
	}
	defer release()

	// peer diff is always a preview: the filters materialize outside the store.
	plan, err := peerPlanForRun(ctx, opts.Probe, cfg, true)
	if err != nil {
		return nil, err
	}
	res := &PeerDiffResult{Plan: plan}
	if opts.Itemize {
		// The plan of a two-way run, with the deletions it would hold.
		authorized, err := PeerBaselineReady(cfg)
		if err != nil {
			return nil, err
		}
		evidence, err := ComputeTombstones(cfg)
		if err != nil {
			return nil, err
		}
		items := newPeerRunPlan(cfg)
		items.addPlan(plan, PlanScopeWorkspace, planRun{authorized: authorized, evidence: evidence})
		tracked, err := planPeerHomeTracked(ctx, opts.Probe, cfg, nil, true)
		if err != nil {
			return nil, err
		}
		if tracked != nil {
			run, err := tracked.run(false, false)
			if err != nil {
				return nil, err
			}
			items.addPlan(tracked.plan, PlanScopeHostTracked, run)
			// peer sync refuses the tracked plan on the same limits.
			if err := ValidatePeerPlanSafety(cfg, tracked.plan); err != nil {
				items.sortItems()
				res.Items = items
				return res, err
			}
		}
		// The merge decision first: the additive listing leaves its files
		// out, as the pass does.
		merges, err := planHostMerges(ctx, opts.Probe, cfg)
		if err != nil {
			return nil, err
		}
		additive, err := peerHomeAdditiveItems(ctx, opts.Probe, cfg, false, false)
		if err != nil {
			return nil, err
		}
		items.Items = append(items.Items, additive...)
		items.Items = annotateHotItems(ctx, opts.Probe, cfg, items.Items, "both", merges)
		items.sortItems()
		res.Items = items
		// peer sync stops on the same refusal before anything moves.
		if err := hostMergeRefusal(merges, "peer sync stops on it before anything moves"); err != nil {
			return res, err
		}
	}
	if err := ValidatePeerPlanSafety(cfg, plan); err != nil {
		if opts.Itemize {
			return res, err
		}
		return nil, err
	}
	return res, nil
}

// PeerSyncOptions controls PeerSync. Runner performs the transfers and honors
// --dry-run; Probe is always live so reachability, the remote rsync version
// and the clock can still be read during a preview.
type PeerSyncOptions struct {
	Config   *Config
	Runner   *exec.Runner
	Probe    *exec.Runner
	PushOnly bool
	PullOnly bool
	SkipHome bool
	DryRun   bool
	Progress func(PeerEvent)
	// Itemize returns the run's plan item by item (#180): the same plans
	// the run executes, and the additive host pass as rsync decides it.
	Itemize bool
	// LocalDotVersion is this binary's version ("2.70.22 (sha)"); a peer on
	// a different release is reported. Empty skips the comparison.
	LocalDotVersion string
	// WhileLocked runs once this run holds the sync lock, and its release
	// when the run ends: the caller's background lease then always belongs
	// to the run that owns the lock (#267). Nil holds nothing.
	WhileLocked func() (release func())
}

// PeerSyncResult reports how the transaction ended. Complete=false means
// destructive transitions were held back and the baseline was left alone.
// Demoted means this machine lost the epoch fence: it adopted the peer's
// owner and epoch and removed its scheduler instead of transferring.
type PeerSyncResult struct {
	Unreachable bool
	Complete    bool
	Demoted     bool
	// QuarantinedHere and QuarantinedOnPeer count the workspace deletions
	// this run moved into .sync-conflicts/<ConflictStamp>/ on each machine
	// instead of removing them (#182).
	QuarantinedHere   int
	QuarantinedOnPeer int
	ConflictStamp     string
	// Plan is the itemized plan under Itemize.
	Plan *PeerRunPlan
	// Hooks are the on_deactivate results of a demotion.
	Hooks []HookResult
}

// PeerSync exchanges the workspace and the host paths with the peer.
func PeerSync(ctx context.Context, opts PeerSyncOptions) (*PeerSyncResult, error) {
	cfg, runner, probe := opts.Config, opts.Runner, opts.Probe
	dryRun := opts.DryRun
	if !cfg.Target.IsSSH() {
		return nil, fmt.Errorf("peer target is not an ssh target; run: dot peer init --host <user@host>")
	}
	// The profile owner is the coordinator. This guard is intentionally
	// before any probe or transfer: a second machine must not perform a
	// half-run and then leave a different baseline behind.
	if err := CheckOwner(cfg); err != nil {
		return nil, err
	}

	// One peer run at a time. The scheduled job and a manual run would
	// otherwise overlap and drive concurrent rsync writes into the same
	// tree and the same conflict directory.
	release, err := AcquireLockForRun(cfg.LockDir, dryRun)
	if err != nil {
		return nil, fmt.Errorf("another peer sync is already running: %w", err)
	}
	defer release()
	if opts.WhileLocked != nil {
		defer opts.WhileLocked()()
	}

	if err := CheckSSH(ctx, probe, cfg.Target.Host); err != nil {
		return &PeerSyncResult{Unreachable: true}, nil
	}
	if err := resolvePeerRsync(ctx, probe, cfg); err != nil {
		return nil, err
	}
	// The fence runs on first contact, before any transfer. A previous-
	// release peer (no dotVersion, no epoch) gets exactly the pre-epoch
	// owner-mismatch behavior; an epoch pair resolves coordinator divergence
	// here and the loser demotes itself instead of syncing.
	remoteStatus, err := fetchRemotePeerStatus(ctx, probe, cfg)
	if err != nil {
		return nil, err
	}
	demote, legacy, err := peerFence(cfg, remoteStatus)
	if err != nil {
		return nil, err
	}
	switch {
	case legacy:
		emitPeer(opts.Progress, PeerEvent{Kind: PeerEventPeerLacksHandover, Path: cfg.remoteDot.String()})
	case cfg.remoteDot.Unreleased():
		emitPeer(opts.Progress, PeerEvent{Kind: PeerEventPeerDotUnreleased, Path: cfg.remoteDot.String()})
	case dotVersionsDiffer(opts.LocalDotVersion, cfg.remoteDot):
		emitPeer(opts.Progress, PeerEvent{Kind: PeerEventDotVersionMismatch, Path: cfg.remoteDot.String()})
	}
	if demote {
		hooks, err := demotePeer(ctx, runner, cfg, remoteStatus.Profile.Owner, remoteStatus.OwnerEpoch, dryRun)
		// The hook outcomes return with a failed demotion too.
		return &PeerSyncResult{Demoted: true, Hooks: hooks}, err
	}
	// The other Mac never ran its --local-only step after an offline rename:
	// the alias stays until it does, so every run past the fence (a preview
	// and a one-way run too) says what to run there.
	if old := remoteStatus.Profile.Owner; len(cfg.OwnerAliases) > 0 && NormalizeHostname(old) != NormalizeHostname(cfg.Owner) &&
		ownersMatch(old, nil, cfg.OwnerAliases...) {
		emitPeer(opts.Progress, PeerEvent{Kind: PeerEventOwnerRenamePending, Path: "dot sync owner --rename " + shellQuote(old) + " " + shellQuote(cfg.Owner) + " --local-only"})
	}
	// After the fence (a bad key must not keep a losing Mac from demoting)
	// and before anything moves: a bad key would otherwise stop every run
	// halfway, after the workspace pass. Every run with the host pass reads
	// it: a one-way run holds the files it names.
	if !opts.SkipHome {
		if err := validateHostMerge(cfg.HostMerge); err != nil {
			return nil, err
		}
	}

	// The linked-worktree exclude is a sticky union of the stored list, local
	// detection and the remote's report (#135): both machines must apply the
	// identical set to the local walk, the remote inventory and every
	// transfer, or a rule only one side applies becomes a propagated delete.
	// A real run persists the union; a preview changes nothing in the store.
	worktrees, err := MergePeerWorktrees(cfg, remoteStatus.Worktrees, !dryRun)
	if err != nil {
		return nil, err
	}
	cfg.WorktreeExcludes = worktrees

	if !dryRun {
		// Normalize opted-in NFD names before tombstones, inventory, plans,
		// and rsync filters materialize. The helper is marker-gated, so an
		// unmarked workspace remains unchanged until its explicit migration.
		if err := NormalizeWorkspaceNamesBeforePush(cfg); err != nil {
			return nil, fmt.Errorf("normalizing workspace names: %w", err)
		}
		if NFDMigrationMarked(cfg.LocalPaths.WorkspaceRoot) {
			if err := normalizeRemotePeerNames(ctx, runner, cfg); err != nil {
				return nil, err
			}
		}
	}

	// Compute deletion evidence before any pull mutates the coordinator
	// tree, then build the same three-way plan used by `peer diff`.
	tombstones, err := ComputeTombstones(cfg)
	if err != nil {
		return nil, err
	}
	cfg.Tombstones = tombstones
	plan, err := peerPlanForRun(ctx, probe, cfg, dryRun)
	if err != nil {
		return nil, err
	}
	if err := ValidateWorkspacePlanSafety(cfg, plan); err != nil {
		return nil, err
	}
	// host_merge decides here, before anything moves (#181). Its files are
	// never newest-wins input, in any run (peerHomeAdditiveArgs): a two-way
	// run merges those on both machines (one it cannot merge stops the run
	// now, not after the workspace pass), a one-way run holds them, and a
	// file on one Mac only is created on the other. The same decision draws
	// the plan; the merge step takes it again from the copies as they are.
	twoWay := !opts.PushOnly && !opts.PullOnly
	direction := runDirection(opts.PushOnly, opts.PullOnly)
	var merges []hostMerge
	if !opts.SkipHome {
		m, err := planHostMerges(ctx, probe, cfg)
		if err != nil {
			return nil, err
		}
		if twoWay {
			if err := hostMergeRefusal(m, "nothing was transferred"); err != nil {
				return nil, err
			}
		}
		merges = m
		for i := range m {
			if !twoWay && m[i].wouldMove(direction) {
				emitPeer(opts.Progress, PeerEvent{Kind: PeerEventHostMergeHeld, Path: m[i].rel})
			}
		}
	}
	conflict := NewConflictDir()
	complete := true
	quarantinedHere, quarantinedOnPeer := 0, 0
	baselineReady, err := PeerBaselineReady(cfg)
	if err != nil {
		return nil, err
	}
	var runPlan *PeerRunPlan
	if opts.Itemize {
		runPlan = newPeerRunPlan(cfg)
		runPlan.addPlan(plan, PlanScopeWorkspace, planRun{pushOnly: opts.PushOnly, pullOnly: opts.PullOnly, authorized: baselineReady, evidence: tombstones})
	}
	// A target marker authorizes destructive transitions. An initial
	// additive bootstrap remains allowed, but an unproven local/remote
	// deletion must stay pending and must not retire the baseline.
	deletesAuthorized := baselineReady

	if !opts.PushOnly {
		emitPeer(opts.Progress, PeerEvent{Kind: PeerEventPullStart})
		pullErr := PullPeerPlan(ctx, runner, cfg, plan, dryRun)
		if err := failOnPartial(opts.Progress, pullErr); err != nil {
			return nil, err
		}
		if cfg.Propagation.Delete && deletesAuthorized && len(plan.DeleteLocal) > 0 {
			if err := DeletePeerLocal(cfg, conflict, plan.DeleteLocal, dryRun); err != nil {
				return nil, err
			}
			quarantinedHere = len(plan.DeleteLocal)
		} else if len(plan.DeleteLocal) > 0 {
			complete = false
			emitPeer(opts.Progress, PeerEvent{Kind: PeerEventRemoteDeletesHeld})
		}
	}
	if !opts.PullOnly {
		// The remote owner is re-read immediately before the first remote
		// mutation after the pull pass: a coordinator change while the pull
		// was moving bytes must stop this run before it deletes or pushes
		// anything on the peer.
		if !dryRun {
			if err := recheckPeerOwnerBeforeMutation(ctx, probe, cfg); err != nil {
				return nil, err
			}
		}
		deleteSet := intersectPeerPaths(tombstones, plan.DeleteRemote)
		if !cfg.Propagation.Delete {
			deleteSet = nil
		}
		if len(deleteSet) > 0 && deletesAuthorized {
			emitPeer(opts.Progress, PeerEvent{Kind: PeerEventPropagateDeletesStart})
			if err := PropagateDeletes(ctx, runner, cfg, conflict, deleteSet, dryRun); err != nil {
				return nil, err
			}
			quarantinedOnPeer = len(deleteSet)
		} else if len(plan.DeleteRemote) > 0 {
			complete = false
			emitPeer(opts.Progress, PeerEvent{Kind: PeerEventLocalDeletesHeld})
		}
		if len(plan.Push) > len(plan.QuarantineRemote) {
			baseline, err := LoadBaselineManifest(cfg.LocalPaths.BaselineFile)
			if err != nil {
				return nil, fmt.Errorf("peer push revalidation: loading baseline: %w", err)
			}
			// Treat every planned push path as type-sensitive during this
			// inventory, including baseline-unknown creates that became links.
			for _, rel := range plan.Push {
				if _, ok := baseline[rel]; !ok {
					baseline[rel] = Fingerprint{}
				}
			}
			rf, err := PreparePeerPlanFilters(cfg, dryRun)
			if err != nil {
				return nil, fmt.Errorf("peer push revalidation: preparing filters: %w", err)
			}
			remoteNow, err := peerRemoteInventory(ctx, probe, cfg, rf, baseline)
			rf.Cleanup()
			if err != nil {
				return nil, err
			}
			if err := ValidatePeerPushRemoteStable(plan, remoteNow); err != nil {
				return nil, err
			}
		}
		emitPeer(opts.Progress, PeerEvent{Kind: PeerEventPushStart})
		pushErr := PushPeerPlan(ctx, runner, cfg, plan, conflict, dryRun)
		if err := failOnPartial(opts.Progress, pushErr); err != nil {
			return nil, err
		}
	}

	// Host paths are separate passes because they live outside the
	// workspace and are addressed by explicit lists rather than the
	// workspace filter chain. Without them the workspace does not run on
	// the peer: 16 of its 19 submodule remotes are SSH. The tracked pass
	// runs first so its three-way plan observes the tracked trees before
	// the additive pass touches anything.
	if !opts.SkipHome {
		trackedComplete, tracked, trackedErr := peerHomeTrackedSync(ctx, runner, probe, cfg, opts.Progress, dryRun, opts.PushOnly, opts.PullOnly)
		if err := failOnPartial(opts.Progress, trackedErr); err != nil {
			return nil, err
		}
		if !trackedComplete {
			complete = false
		}
		if runPlan != nil {
			if tracked != nil {
				run, err := tracked.run(opts.PushOnly, opts.PullOnly)
				if err != nil {
					return nil, err
				}
				runPlan.addPlan(tracked.plan, PlanScopeHostTracked, run)
			}
			// Listed before the pass runs: afterwards rsync has nothing left
			// to report.
			additive, err := peerHomeAdditiveItems(ctx, probe, cfg, opts.PushOnly, opts.PullOnly)
			if err != nil {
				return nil, err
			}
			runPlan.Items = append(runPlan.Items, additive...)
			runPlan.Items = annotateHotItems(ctx, probe, cfg, runPlan.Items, direction, merges)
		}
		// host_merge runs before the additive pass and only in a two-way run:
		// it writes both machines.
		if !dryRun && twoWay && len(cfg.HostMerge) > 0 {
			merged, err := mergePeerHostFiles(ctx, runner, probe, cfg)
			for _, rel := range merged {
				emitPeer(opts.Progress, PeerEvent{Kind: PeerEventHostMerged, Path: rel})
			}
			if err != nil {
				return nil, err
			}
		}
		emitPeer(opts.Progress, PeerEvent{Kind: PeerEventHostPathsStart})
		homeErr := peerHomeSync(ctx, runner, cfg, opts.Progress, dryRun, opts.PushOnly, opts.PullOnly)
		if err := failOnPartial(opts.Progress, homeErr); err != nil {
			return nil, err
		}
	}
	// A first successful additive run establishes provenance. A target
	// marker is not required for that bootstrap, but any held deletion is
	// enough to keep the baseline unchanged until a later verified run.
	canCommitBaseline := baselineReady ||
		(len(plan.DeleteLocal) == 0 && len(plan.DeleteRemote) == 0)
	if complete && !dryRun && !opts.PullOnly {
		if err := AppendPeerConflictAudit(cfg, plan); err != nil {
			return nil, err
		}
	}
	if complete && !dryRun && !opts.PushOnly && !opts.PullOnly && canCommitBaseline {
		if err := CommitPeerBaseline(cfg, plan.NextBaseline); err != nil {
			return nil, err
		}
	}
	if complete && !dryRun && !opts.PushOnly && !opts.PullOnly {
		// After every complete two-way run the coordinator pushes its
		// baselines and filter files to the peer store as the takeover
		// replica. The replica is a safety net: its failure degrades the next
		// unplanned switch, not this run, so it is reported, not fatal.
		if err := PushPeerReplica(ctx, runner, cfg); err != nil {
			emitPeer(opts.Progress, PeerEvent{Kind: PeerEventReplicaPushFailed, Err: err})
		}
		// A rename is finished once the peer records the same owner: the
		// old names retire here so they stop admitting writes (#185).
		// Only once this Mac answers to the new name itself: retiring while
		// it still answers only to an alias would lock it out of its own
		// profiles. Like the replica push, a failure here is reported, not
		// fatal: the run itself is complete.
		if len(cfg.OwnerAliases) > 0 && NormalizeHostname(remoteStatus.Profile.Owner) == NormalizeHostname(cfg.Owner) &&
			ownersMatch(cfg.Owner, nil, MachineNames()...) {
			retired, err := RetireOwnerAliases(strings.TrimRight(cfg.LocalPath, "/"), cfg.Owner)
			if err == nil {
				cfg.OwnerAliases = nil
			}
			if err != nil || len(retired) > 0 {
				emitPeer(opts.Progress, PeerEvent{Kind: PeerEventOwnerAliasesRetired, Path: strings.Join(retired, ", "), Err: err})
			}
		}
		if cfg.FencePending {
			// The first complete run after contact: the fence has done its
			// job and normal requirements apply again.
			if err := clearPeerFencePending(cfg); err != nil {
				return nil, err
			}
			cfg.FencePending = false
		}
	}
	if !dryRun {
		// `peer sync` is its own engine path, so the timestamps `dot peer
		// status` reads are stamped here. Every earlier exit - unreachable,
		// a safety refusal, a partial transfer - returns before this point,
		// which is what keeps status showing the last genuine exchange.
		if err := recordPeerRun(cfg.LocalPaths, opts.PushOnly, opts.PullOnly, complete); err != nil {
			return nil, err
		}
	}
	if runPlan != nil {
		runPlan.sortItems()
	}
	return &PeerSyncResult{Complete: complete, QuarantinedHere: quarantinedHere, QuarantinedOnPeer: quarantinedOnPeer, ConflictStamp: conflict.Timestamp, Plan: runPlan}, nil
}

// recordPeerRun stamps a finished peer run onto state.yaml. A held run still
// moved bytes, so its legs are recorded, but LastHeld marks it as the
// incomplete exchange it was.
//
// Only a two-way run clears that mark, for the same reason only a two-way run
// commits the baseline: a --push-only run never evaluates plan.DeleteLocal and
// a --pull-only run never evaluates plan.DeleteRemote, so neither can prove the
// held transition it skipped has been resolved.
func recordPeerRun(paths *LocalPaths, pushOnly, pullOnly, complete bool) error {
	now := time.Now().UTC()
	if err := UpdateLocalState(paths, func(s *LocalState) {
		if !pushOnly {
			s.LastPull = now
		}
		if !pullOnly {
			s.LastPush = now
		}
		switch {
		case !complete:
			s.LastHeld = now
		case !pushOnly && !pullOnly:
			s.LastHeld = time.Time{}
		}
	}); err != nil {
		return fmt.Errorf("peer state update: %w", err)
	}
	return nil
}

// PeerScheduleOptions controls PeerSchedule. Off removes the job; everything
// else is only read on the install path.
type PeerScheduleOptions struct {
	Config   *Config
	Runner   *exec.Runner
	Probe    *exec.Runner
	Interval time.Duration
	Off      bool
	DryRun   bool
}

// PeerScheduleResult describes the job that was installed or removed.
type PeerScheduleResult struct {
	Off                      bool
	DryRun                   bool
	TargetUserActionRequired bool
	Plist                    string
	LogFile                  string
	Interval                 time.Duration
	// SeededHomeTrackedFile names the tracked host-path list when the install
	// created it (upgrade refresh); empty when the file already existed.
	SeededHomeTrackedFile string
	// Hooks are the on_activate (install) or on_deactivate (off) results.
	Hooks []HookResult
}

// PeerSchedule installs or removes the periodic `dot peer sync` job.
func PeerSchedule(ctx context.Context, opts PeerScheduleOptions) (*PeerScheduleResult, error) {
	cfg, runner := opts.Config, opts.Runner
	plist := filepath.Join(cfg.HomeDir(), "Library", "LaunchAgents", "com.dotfiles.peer.plist")
	if err := validateSchedulerMutationHome(cfg.Home); err != nil {
		return nil, fmt.Errorf("peer scheduler home %q rejected for plist %s: %w; existing artifact was left untouched; run dot peer setup after fixing the home path", cfg.Home, plist, err)
	}

	if opts.Off {
		if opts.DryRun || (runner != nil && runner.DryRun) {
			// A preview removes neither the plist nor any loaded job.
			return &PeerScheduleResult{
				Off:                      true,
				DryRun:                   true,
				TargetUserActionRequired: schedulerRequiresTargetUserServiceDomain(cfg),
				Plist:                    plist,
				Hooks:                    runPeerHooks(ctx, runner, cfg, HookOnDeactivate, true),
			}, nil
		}
		if !schedulerRequiresTargetUserServiceDomain(cfg) {
			_, _ = runner.Run(ctx, "launchctl", "bootout", "gui/"+strconv.Itoa(os.Getuid())+"/com.dotfiles.peer")
		}
		if err := os.Remove(plist); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		result := &PeerScheduleResult{
			Off:                      true,
			TargetUserActionRequired: schedulerRequiresTargetUserServiceDomain(cfg),
			Plist:                    plist,
			Hooks:                    runPeerHooks(ctx, runner, cfg, HookOnDeactivate, false),
		}
		if schedulerRequiresTargetUserServiceDomain(cfg) {
			return result, &SchedulerTargetUserActionRequiredError{}
		}
		return result, nil
	}
	if err := CheckOwner(cfg); err != nil {
		return nil, fmt.Errorf("refusing peer scheduler on a non-coordinator: %w", err)
	}
	if opts.Interval < time.Minute {
		return nil, fmt.Errorf("--interval must be at least 1m (got %s)", opts.Interval)
	}
	if !cfg.Target.IsSSH() {
		return nil, fmt.Errorf("peer target is not configured; run dot peer init first")
	}
	exe, err := peerExecutable()
	if err != nil {
		return nil, err
	}
	preparedExe, err := plistXMLText(exe, "")
	if err != nil {
		return nil, peerPlistPathError("executable", exe, plist, err)
	}
	preparedLogFile, err := plistXMLText(cfg.LogFile, "")
	if err != nil {
		return nil, peerPlistPathError("log file", cfg.LogFile, plist, err)
	}
	homeArg, err := peerHomeArg(cfg)
	if err != nil {
		return nil, fmt.Errorf("peer scheduler home %q rejected for plist %s: %w; existing artifact was left untouched; run dot peer setup after fixing the home path", cfg.Home, plist, err)
	}
	// All remaining local inputs are now representable in a plist. Check the
	// coordinator immediately before the dry-run/mutation branch so actual
	// scheduler installs still require bilateral owner agreement, while an
	// invalid local artifact can be rejected without a network dependency.
	// A takeover with a pending fence installs its scheduler while the old
	// coordinator is still away, so these two checks are skipped only then;
	// the fence on the first contact after the peer returns settles the role.
	if !cfg.FencePending {
		if err := CheckSSH(ctx, opts.Probe, cfg.Target.Host); err != nil {
			return nil, fmt.Errorf("checking peer coordinator before scheduler setup: %w", err)
		}
		if _, err := checkRemotePeerOwner(ctx, opts.Probe, cfg); err != nil {
			return nil, err
		}
	}
	// Mirror the off-arm above. The write below bypasses the runner entirely
	// (os.MkdirAll + os.WriteFile), so without this a preview leaves a plist on
	// disk with no loaded job behind it — the same inconsistent state the
	// off-arm refuses to create (BUG-14).
	//
	// The guard sits AFTER the whole validation chain on purpose: a preview that
	// skipped validation would be a different lie from the one being fixed. The
	// off-arm above keeps reading opts.DryRun alone, so its behavior is
	// untouched; only this arm reconciles the option with the runner's own flag,
	// the way the three aisettings managers do.
	if opts.DryRun || (runner != nil && runner.DryRun) {
		return &PeerScheduleResult{
			DryRun:                   true,
			TargetUserActionRequired: schedulerRequiresTargetUserServiceDomain(cfg),
			Plist:                    plist,
			LogFile:                  cfg.LogFile,
			Interval:                 opts.Interval,
			Hooks:                    runPeerHooks(ctx, runner, cfg, HookOnActivate, true),
		}, nil
	}
	logFile := cfg.LogFile
	if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
		return nil, err
	}
	body := fmt.Sprintf(peerPlistTmpl, preparedExe, homeArg, int(opts.Interval.Seconds()), preparedLogFile, preparedLogFile)
	if err := runner.WriteFileAtomic(plist, []byte(body), 0o644); err != nil {
		return nil, fmt.Errorf("writing peer plist %s for home %q: %w; existing artifact was left untouched; run dot peer setup after fixing the home path", plist, cfg.Home, err)
	}
	// `dot peer setup` is the documented refresh path after an upgrade, so the
	// tracked host-path list is seeded here as well as by init. The dry-run arm
	// above returns before this point, and PeerSync never seeds: an operator
	// who deletes the file disables the feature.
	seeded, err := seedPeerHomeTracked(cfg, strings.TrimRight(cfg.Target.Path, "/"))
	if err != nil {
		return nil, err
	}
	result := &PeerScheduleResult{
		TargetUserActionRequired: schedulerRequiresTargetUserServiceDomain(cfg),
		Plist:                    plist,
		LogFile:                  logFile,
		Interval:                 opts.Interval,
	}
	if seeded {
		result.SeededHomeTrackedFile = PeerHomeTrackedFile(cfg.LocalPaths)
	}
	if schedulerRequiresTargetUserServiceDomain(cfg) {
		return result, &SchedulerTargetUserActionRequiredError{}
	}
	label := "gui/" + strconv.Itoa(os.Getuid()) + "/com.dotfiles.peer"
	_, _ = runner.Run(ctx, "launchctl", "bootout", label)
	if _, err := runner.Run(ctx, "launchctl", "bootstrap", "gui/"+strconv.Itoa(os.Getuid()), plist); err != nil {
		return nil, fmt.Errorf("loading %s: %w", plist, err)
	}
	result.Hooks = runPeerHooks(ctx, runner, cfg, HookOnActivate, false)
	return result, nil
}

func peerPlistPathError(field, value, plist string, err error) error {
	return fmt.Errorf("peer scheduler %s %q rejected for plist %s: %w; existing artifact was left untouched; run dot peer setup after fixing the path", field, value, plist, err)
}

// PeerDoctorOptions controls PeerDoctor. The runner is always live: every
// check is a read-only question about the peer.
type PeerDoctorOptions struct {
	Config *Config
	Probe  *exec.Runner
	// LocalDotVersion is this binary's version, compared with the peer's.
	LocalDotVersion string
}

// PeerDoctorReport is the outcome of every precondition probe. Each check
// degrades into a field rather than aborting the report, so one broken probe
// does not hide the rest.
type PeerDoctorReport struct {
	Target            string
	LocalRsyncPath    string
	LocalRsyncVersion string
	LocalRsyncErr     error
	LocalDotPath      string
	LocalDotVersion   string
	RemoteDot         string   // "path (banner)" of the binary peer runs use
	RemoteDotPassed   []string // candidates the probe passed over
	RemoteDotErr      error
	DotMismatch       bool
	DotUnreleased     bool // only a non-release dot, and no remote_dot pin
	Unreachable       bool
	UnreachableErr    error
	RemoteRsyncPath   string
	RemoteRsyncErr    error
	ClockSkew         time.Duration
	ClockSkewErr      error
	ClockSkewOK       bool
	Disk              string
	DiskKnown         bool
	// Local and Peer are both machines' facts and Checks their comparison
	// (#182); both stay nil when the peer cannot say (PeerFactsErr says why).
	Local        *PeerSideFacts
	Peer         *PeerSideFacts
	PeerFactsErr error
	Checks       []DoctorCheck
	Problems     int
}

// PeerDoctor probes everything that silently breaks a peer transfer.
func PeerDoctor(ctx context.Context, opts PeerDoctorOptions) (*PeerDoctorReport, error) {
	cfg, runner := opts.Config, opts.Probe
	if !cfg.Target.IsSSH() {
		return nil, fmt.Errorf("peer profile target is %q, expected an ssh: target; run dot peer init", cfg.Target.String())
	}
	host := cfg.Target.Host
	report := &PeerDoctorReport{Target: cfg.Target.String(), LocalDotVersion: opts.LocalDotVersion}
	if exe, err := peerExecutable(); err == nil {
		report.LocalDotPath = exe
	}

	// The local client is checked first: it needs no peer, and a run on this
	// machine fails with it no matter how healthy the peer is.
	if path, ver, err := LocalRsyncPath(ctx, runner); err != nil {
		report.LocalRsyncErr = err
		report.Problems++
	} else {
		report.LocalRsyncPath, report.LocalRsyncVersion = path, ver
	}

	if err := CheckSSH(ctx, runner, host); err != nil {
		report.Unreachable = true
		report.UnreachableErr = err
		return report, nil
	}

	if dot, err := resolveRemoteDot(ctx, runner, cfg); err != nil {
		report.RemoteDotErr = err
		report.Problems++
	} else {
		report.RemoteDot = dot.String()
		report.RemoteDotPassed = dot.Passed
		report.DotMismatch = dotVersionsDiffer(opts.LocalDotVersion, dot)
		report.DotUnreleased = dot.Unreleased()
	}

	rp, err := RemoteRsyncPath(ctx, runner, host)
	if err != nil {
		report.RemoteRsyncErr = err
		report.Problems++
	} else {
		report.RemoteRsyncPath = rp
	}

	if skew, err := peerClockSkew(ctx, runner, host); err != nil {
		report.ClockSkewErr = err
	} else {
		report.ClockSkew = skew
		abs := skew
		if abs < 0 {
			abs = -abs
		}
		report.ClockSkewOK = abs <= peerClockTolerance
		if !report.ClockSkewOK {
			report.Problems++
		}
	}

	if out, err := runner.Run(ctx, "ssh", "-o", "BatchMode=yes", host, "df -h / | tail -1"); err == nil {
		report.Disk = strings.Join(strings.Fields(strings.TrimSpace(out.Stdout)), " ")
		report.DiskKnown = true
	}

	if report.RemoteDotErr == nil {
		report.Peer, report.PeerFactsErr = remotePeerSideFacts(ctx, runner, cfg)
		if report.PeerFactsErr != nil {
			// None of the both-machines checks ran: unknown is not a pass.
			report.Problems++
		}
	}
	if report.Peer != nil {
		// Only now: the NFD count walks the whole workspace.
		report.Local = LocalPeerSideFacts(ctx, runner, cfg, opts.LocalDotVersion)
		here := PreferredMachineName()
		if here == "" {
			here = "this machine"
		}
		hostMergeVerdicts(ctx, runner, cfg, report.Local, report.Peer, here, host)
		report.Checks = evaluatePeerSides(report.Local, report.Peer, here, host)
		for _, c := range report.Checks {
			if c.Level == DoctorFail {
				report.Problems++
			}
		}
	}
	return report, nil
}

// peerPlanForRun builds the coordinator plan from one local inventory, one
// read-only remote inventory, and the last committed common baseline. The
// same helper is used by `peer sync` and `peer diff`; a displayed divergence
// therefore cannot disagree with the transaction that follows it. Under
// dryRun the filter files the inventory reads live in a temp directory and
// are removed before return.
func peerPlanForRun(ctx context.Context, runner *exec.Runner, cfg *Config, dryRun bool) (*PeerPlan, error) {
	if cfg == nil || cfg.LocalPaths == nil {
		return nil, fmt.Errorf("peer plan: local paths unresolved")
	}
	baseline, err := LoadBaselineManifest(cfg.LocalPaths.BaselineFile)
	if err != nil {
		return nil, fmt.Errorf("peer plan: loading baseline: %w", err)
	}
	if err := ValidatePeerBaselineLocalTypes(cfg, baseline); err != nil {
		return nil, err
	}
	rf, err := PreparePeerPlanFilters(cfg, dryRun)
	if err != nil {
		return nil, fmt.Errorf("peer plan: preparing filters: %w", err)
	}
	defer rf.Cleanup()
	local, err := InventoryPeer(cfg)
	if err != nil {
		return nil, err
	}
	remote, err := peerRemoteInventory(ctx, runner, cfg, rf, baseline)
	if err != nil {
		return nil, err
	}
	return PlanPeerReconcile(baseline, local, remote)
}

// peerHomeSync exchanges the explicitly listed host paths.
//
// --ignore-missing-args matters: the list is shared between machines and a few
// entries legitimately do not exist on every one. A missing path must not abort
// a transfer that has already moved gigabytes.
//
// This is the additive pass. Entries also listed in home-paths-tracked.txt are
// stripped first (peerHomeUntrackedList): the tracked pass owns them with
// baseline-aware delete propagation and conflict quarantine, and letting this
// pass touch them too would resurrect tracked deletes via --update.
func peerHomeSync(ctx context.Context, runner *exec.Runner, cfg *Config, progress func(PeerEvent), dryRun, pushOnly, pullOnly bool) error {
	list := PeerHomePathsFile(cfg.LocalPaths)
	if _, err := os.Stat(list); err != nil {
		emitPeer(progress, PeerEvent{Kind: PeerEventHostPathsMissing, Path: list})
		return nil
	}
	list, cleanup, err := peerHomeUntrackedList(cfg, list, dryRun)
	if err != nil {
		return err
	}
	defer cleanup()
	home := cfg.HomeDir()
	// --update keeps a peer's newer host config from being overwritten by a
	// stale coordinator copy. Ordinary (untracked) host-path updates are
	// deliberately not backed up: they are not workspace conflicts, and an
	// unconditional backup on every run made `.dot-peer-conflicts` grow with
	// routine state churn. Tracked host paths are different - user-authored
	// content such as the Claude auto-memory directory - so those are handled
	// by peerHomeTrackedSync with delete propagation and quarantine instead.
	// Without --update this pass overwrites host config unconditionally, and the
	// first real run did exactly that: it replaced this machine's
	// ~/.ssh/known_hosts with the peer's copy, deleting the host key entry for
	// the very channel the sync runs over. The next ssh failed with "Host key
	// verification failed".
	//
	// The exclusions are machine-local trust and runtime state. known_hosts is
	// per-machine by nature - merging it is meaningless and losing it is
	// self-inflicted denial of service. Agent sockets are not files worth moving.
	// .codex/config.toml is excluded in code, not just from the seed template:
	// home-paths.txt is seed-once, so lists written before the entry was removed
	// still carry it, and Codex hash-keys its Keychain MCP OAuth credentials to
	// this file's server definitions - copying a peer's copy orphans them.
	remote := cfg.Target.Host + ":"
	pass := func(base []string) error {
		if dryRun {
			base = append(base, "--dry-run")
		}
		// Pull first, same reasoning as the workspace pass: the additive
		// direction records a conflict before this machine's version goes out.
		if !pushOnly {
			args := append(append([]string{}, base...), remote, home+"/")
			if err := runPeerRsync(ctx, runner, cfg, args); err != nil {
				return err
			}
		}
		if !pullOnly {
			args := append(append([]string{}, base...), home+"/", remote)
			if err := runPeerRsync(ctx, runner, cfg, args); err != nil {
				return err
			}
		}
		return nil
	}
	if err := pass(peerHomeAdditiveArgs(cfg, list, true, false)); err != nil {
		return err
	}
	hmList, hmCleanup, err := hostMergeList(cfg, dryRun)
	if err != nil || hmList == "" {
		return err
	}
	defer hmCleanup()
	return pass(peerHomeAdditiveArgs(cfg, hmList, true, true))
}

// peerHomeAdditiveArgs are the additive pass's rsync flags, shared with the
// itemized plan (#180) so a preview lists what the run would move. report
// adds the human-readable stats a transfer prints. createOnly is the pass
// over the host_merge files (hostMergeList): --ignore-existing creates a
// file only where it is absent (a copy that appeared during the run
// included, up to rsync's own check-then-rename; see docs/CEILINGS.md), and
// only a regular file, as host_merge writes nothing else: no links, no
// devices, no recursion into a directory. The merge step owns files on
// both Macs.
func peerHomeAdditiveArgs(cfg *Config, list string, report, createOnly bool) []string {
	args := []string{"-aHAX", "--numeric-ids", "-r"}
	if createOnly {
		args = []string{"-aHAX", "--numeric-ids", "--no-links", "--no-D"}
	}
	if report {
		args = append(args, "--human-readable", "--stats")
	}
	args = append(args,
		"--ignore-missing-args", "--chmod=Du+w",
		map[bool]string{false: "--update", true: "--ignore-existing"}[createOnly],
		"--exclude=known_hosts", "--exclude=known_hosts.old", "--exclude=known_hosts2",
		"--exclude=agent", "--exclude=agent/**", "--exclude=*.sock",
		"--exclude=/.codex/config.toml",
		"--exclude=.DS_Store")
	// A host_merge file is never newest-wins input, by config rather than by
	// who has a copy when: a stale save by a running app stays on its own
	// Mac, and the next two-way run's merge restores it there (#181).
	// The create-only pass excludes each key as a directory: --files-from
	// implies --dirs, which would create a listed directory empty on the
	// other Mac. The trailing slash matches only a directory, and exclude
	// rules reach the remote sender, so it holds in both directions.
	for _, rel := range slices.Sorted(maps.Keys(cfg.HostMerge)) {
		if pattern, err := literalRsyncPattern(rel); err == nil {
			if createOnly {
				pattern += "/"
			}
			args = append(args, "--exclude=/"+pattern)
		}
	}
	args = append(args, "--files-from="+list)
	if cfg.RemoteRsyncPath != "" {
		args = append(args, "--rsync-path="+cfg.RemoteRsyncPath)
	}
	return append(args, "-e", "ssh")
}

func runPeerRsync(ctx context.Context, runner *exec.Runner, cfg *Config, args []string) error {
	if cfg.Verbose {
		return ClassifyRsyncError(runner.RunAttached(ctx, cfg.rsyncBin(), args...))
	}
	_, err := runner.Run(ctx, cfg.rsyncBin(), args...)
	return ClassifyRsyncError(err)
}

// failOnPartial announces rsync's partial-transfer outcome, then returns it as
// a hard transaction failure. Exit 23/24 can mean a scoped conflict or delete
// did not finish; later passes and baseline publication must stop immediately.
// The wording of the announcement belongs to cli (D-06); only the
// classification and the abort decision are here.
func failOnPartial(progress func(PeerEvent), err error) error {
	if err == nil {
		return nil
	}
	if IsPartialTransfer(err) {
		emitPeer(progress, PeerEvent{Kind: PeerEventPartialTransfer, Err: err})
		return fmt.Errorf("peer sync incomplete: %w", err)
	}
	return err
}
