package syncer

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/entelecheia/dotfiles-v2/internal/exec"
)

// PeerFile is one file in a peer inventory. Absent files are represented by
// Present=false; keeping absence explicit makes deletion decisions part of the
// same three-way classification as creates and updates.
type PeerFile struct {
	Present bool
	FP      Fingerprint
}

// PeerSnapshot is a filtered, file-only inventory keyed by workspace-relative
// path. Directories are traversal state, not payload, and symlinks are
// intentionally omitted to match rsync --no-links and the baseline walker.
type PeerSnapshot map[string]PeerFile

// PeerConflict identifies a path changed on both sides with different
// payloads. The coordinator's local version wins; the remote version is
// handled by the explicitly scoped conflict pass.
type PeerConflict struct {
	RelPath string
	Reason  string
}

// PeerPlan is the result of baseline-aware three-way classification.
//
// Pull and Push contain ordinary present-file transfers and are intentionally
// disjoint from QuarantineRemote. DeleteLocal and DeleteRemote are explicit
// absence transitions. QuarantineRemote is a subset of Push and is transferred
// once with --backup so a simultaneous remote edit is retained exactly once.
type PeerPlan struct {
	Pull             []string
	Push             []string
	DeleteLocal      []string
	DeleteRemote     []string
	QuarantineRemote []string
	Conflicts        []PeerConflict
	NextBaseline     PeerSnapshot
	LocalBefore      PeerSnapshot
	RemoteBefore     PeerSnapshot
	BaselineCount    int
	LocalCount       int
	RemoteCount      int
}

func (p *PeerPlan) HasChanges() bool {
	return p != nil && (len(p.Pull) > 0 || len(p.Push) > 0 ||
		len(p.DeleteLocal) > 0 || len(p.DeleteRemote) > 0)
}

func (p *PeerPlan) HasConflicts() bool {
	return p != nil && len(p.Conflicts) > 0
}

// PlanPeerReconcile classifies local and remote snapshots against the last
// committed common baseline. Local is the coordinator side, so it wins only
// the simultaneous-different case; one-sided remote changes still flow to the
// coordinator, including remote-only deletes.
func PlanPeerReconcile(baseline map[string]Fingerprint, local, remote PeerSnapshot) (*PeerPlan, error) {
	plan := &PeerPlan{
		NextBaseline: PeerSnapshot{},
		LocalBefore:  clonePeerSnapshot(local),
		RemoteBefore: clonePeerSnapshot(remote),
		LocalCount:   len(local),
		RemoteCount:  len(remote),
	}
	keys := make(map[string]struct{}, len(baseline)+len(local)+len(remote))
	for rel := range baseline {
		keys[rel] = struct{}{}
	}
	for rel := range local {
		keys[rel] = struct{}{}
	}
	for rel := range remote {
		keys[rel] = struct{}{}
	}

	rels := make([]string, 0, len(keys))
	for rel := range keys {
		if err := validateTombstoneRel(rel); err != nil {
			return nil, fmt.Errorf("peer plan: %w", err)
		}
		rels = append(rels, rel)
	}
	sort.Strings(rels)

	for _, rel := range rels {
		base, baseOK := baseline[rel]
		l, lOK := local[rel]
		r, rOK := remote[rel]
		// Baseline keys filtered out on both sides are no longer observable
		// payloads. Excluding them from this denominator prevents retired cache
		// or policy entries from masking a real reset/mass-delete signature.
		if baseOK && (peerPresent(l, lOK) || peerPresent(r, rOK)) {
			plan.BaselineCount++
		}
		localChanged := !peerMatchesBaseline(l, lOK, base, baseOK)
		remoteChanged := !peerMatchesBaseline(r, rOK, base, baseOK)

		switch {
		case !localChanged && !remoteChanged:
			// Both still carry the common state, including both absent.
			plan.setFinal(rel, l, lOK)
		case localChanged && !remoteChanged:
			// Local coordinator-only change wins by propagation.
			if lOK && l.Present {
				plan.Push = append(plan.Push, rel)
			} else if rOK && r.Present {
				plan.DeleteRemote = append(plan.DeleteRemote, rel)
			}
			plan.setFinal(rel, l, lOK)
		case !localChanged && remoteChanged:
			// Remote-only changes are accepted, rather than being mistaken for
			// peer-created content. This includes a remote deletion.
			if rOK && r.Present {
				plan.Pull = append(plan.Pull, rel)
			} else if lOK && l.Present {
				plan.DeleteLocal = append(plan.DeleteLocal, rel)
			}
			plan.setFinal(rel, r, rOK)
		default:
			// Both sides changed. Equal payloads need no transfer; otherwise
			// the local coordinator wins and the remote payload is retained once.
			if peerFilesEqual(l, lOK, r, rOK) {
				plan.setFinal(rel, l, lOK)
				continue
			}
			if lOK && l.Present {
				plan.Push = append(plan.Push, rel)
			} else if rOK && r.Present {
				plan.DeleteRemote = append(plan.DeleteRemote, rel)
			}
			if rOK && r.Present && lOK && l.Present {
				plan.QuarantineRemote = append(plan.QuarantineRemote, rel)
			}
			reason := "simultaneous edit/edit; coordinator copy pushed; peer payload quarantined"
			if !lOK || !l.Present {
				reason = "simultaneous local delete/peer edit; coordinator delete propagated; peer payload quarantined"
			} else if !rOK || !r.Present {
				reason = "simultaneous local edit/peer delete; coordinator copy recreated on peer"
			}
			plan.Conflicts = append(plan.Conflicts, PeerConflict{RelPath: rel, Reason: reason})
			plan.setFinal(rel, l, lOK)
		}
	}

	sort.Strings(plan.Pull)
	sort.Strings(plan.Push)
	sort.Strings(plan.DeleteLocal)
	sort.Strings(plan.DeleteRemote)
	sort.Strings(plan.QuarantineRemote)
	sort.Slice(plan.Conflicts, func(i, j int) bool { return plan.Conflicts[i].RelPath < plan.Conflicts[j].RelPath })
	return plan, nil
}

func clonePeerSnapshot(in PeerSnapshot) PeerSnapshot {
	out := make(PeerSnapshot, len(in))
	for rel, file := range in {
		out[rel] = file
	}
	return out
}

func peerPresent(file PeerFile, ok bool) bool { return ok && file.Present }

// ValidatePeerPlanSafety caps destructive transitions in both directions and
// refuses the characteristic signature of an empty/reset peer. The existing
// outgoing tombstone cap alone cannot protect the coordinator from accepting
// thousands of apparent remote deletions.
func ValidatePeerPlanSafety(cfg *Config, plan *PeerPlan) error {
	if cfg == nil || plan == nil {
		return fmt.Errorf("peer plan safety: missing config or plan")
	}
	if plan.BaselineCount > 0 && plan.LocalCount > 0 && plan.RemoteCount == 0 && len(plan.DeleteLocal) > 0 {
		return fmt.Errorf(
			"peer plan safety: remote inventory is empty but baseline has %d payload(s); refusing %d inbound deletion(s). Reset/rebootstrap the peer from the coordinator before syncing",
			plan.BaselineCount, len(plan.DeleteLocal))
	}
	if plan.BaselineCount > 0 && plan.RemoteCount*2 < plan.BaselineCount && len(plan.DeleteLocal)*2 > plan.BaselineCount {
		return fmt.Errorf(
			"peer plan safety: remote inventory retained only %d of %d baseline payload(s); refusing %d inbound deletion(s) as a probable reset",
			plan.RemoteCount, plan.BaselineCount, len(plan.DeleteLocal))
	}
	if plan.BaselineCount > 0 && plan.LocalCount*2 < plan.BaselineCount && len(plan.DeleteRemote)*2 > plan.BaselineCount {
		return fmt.Errorf(
			"peer plan safety: local inventory retained only %d of %d baseline payload(s); refusing %d outbound deletion(s) as a probable reset",
			plan.LocalCount, plan.BaselineCount, len(plan.DeleteRemote))
	}
	if cfg.MaxDelete > 0 && len(plan.DeleteLocal) > cfg.MaxDelete {
		return fmt.Errorf("peer plan safety: refusing %d inbound deletion(s): over max_delete=%d", len(plan.DeleteLocal), cfg.MaxDelete)
	}
	if cfg.MaxDelete > 0 && len(plan.DeleteRemote) > cfg.MaxDelete {
		return fmt.Errorf("peer plan safety: refusing %d outbound deletion(s): over max_delete=%d", len(plan.DeleteRemote), cfg.MaxDelete)
	}
	return nil
}

// ValidatePeerPushRemoteStable prevents a stale plan from overwriting an edit
// made on the peer after its first inventory. Conflict paths are excluded: the
// dedicated backup-enabled pass intentionally preserves their latest remote
// payload before the coordinator copy wins.
func ValidatePeerPushRemoteStable(plan *PeerPlan, current PeerSnapshot) error {
	if plan == nil {
		return fmt.Errorf("peer push revalidation: missing plan")
	}
	quarantine := make(map[string]bool, len(plan.QuarantineRemote))
	for _, rel := range plan.QuarantineRemote {
		quarantine[rel] = true
	}
	for _, rel := range plan.Push {
		if quarantine[rel] {
			continue
		}
		before, beforeOK := plan.RemoteBefore[rel]
		now, nowOK := current[rel]
		if !peerFilesEqual(before, beforeOK, now, nowOK) {
			return fmt.Errorf("peer push revalidation: remote path %q changed after planning; refusing backup-free overwrite", rel)
		}
	}
	return nil
}

func (p *PeerPlan) setFinal(rel string, f PeerFile, ok bool) {
	if ok && f.Present {
		p.NextBaseline[rel] = f
	}
}

func peerMatchesBaseline(current PeerFile, currentOK bool, base Fingerprint, baseOK bool) bool {
	if !baseOK {
		return !currentOK || !current.Present
	}
	if !currentOK || !current.Present {
		return false
	}
	return peerFingerprintEqual(base, current.FP)
}

func peerFilesEqual(a PeerFile, aOK bool, b PeerFile, bOK bool) bool {
	if !aOK || !a.Present {
		return !bOK || !b.Present
	}
	if !bOK || !b.Present {
		return false
	}
	return peerFingerprintEqual(a.FP, b.FP)
}

func peerFingerprintEqual(a, b Fingerprint) bool {
	if strings.HasPrefix(a.Sha, "link:") != strings.HasPrefix(b.Sha, "link:") {
		return false
	}
	if a.Sha != "" && b.Sha != "" {
		return a.Sha == b.Sha
	}
	return fingerprintsSameFast(a, b)
}

// InventoryPeer scans the local workspace using the same safety filters as
// the peer transfer plus the code-owned volatile deny layer. It is exported so
// the CLI can build the exact plan that `dot peer diff` displays.
func InventoryPeer(cfg *Config) (PeerSnapshot, error) {
	if cfg == nil || cfg.LocalPaths == nil {
		return nil, fmt.Errorf("peer inventory: local paths unresolved")
	}
	root := strings.TrimRight(cfg.LocalPath, "/")
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("peer inventory: workspace root %s is not a directory", root)
	}
	filter, err := newSyncFilter(cfg, strings.TrimRight(cfg.MirrorPath, "/"))
	if err != nil {
		return nil, fmt.Errorf("peer inventory: loading filters: %w", err)
	}
	out := PeerSnapshot{}
	err = filepath.WalkDir(root, func(abs string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if abs == root {
			return nil
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil {
			return err
		}
		rel = normalizeRel(rel)
		if isPeerVolatileRel(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if filter.shouldSkip(abs, rel, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || d.Type()&os.ModeSymlink != 0 || isDriveMetadata(rel) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		fp, err := FingerprintFile(abs, FingerprintFast)
		if err != nil {
			return err
		}
		out[rel] = PeerFile{Present: true, FP: fp}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("peer inventory: scanning %s: %w", root, err)
	}
	return out, nil
}

// ValidatePeerBaselineLocalTypes prevents an excluded symlink or type change
// from masquerading as a baseline-proven local deletion. Missing paths are
// legitimate deletions; every existing component must stay inside the real
// workspace tree and the final payload must remain a regular file.
func ValidatePeerBaselineLocalTypes(cfg *Config, baseline map[string]Fingerprint) error {
	if cfg == nil {
		return fmt.Errorf("peer baseline type check: nil config")
	}
	root := strings.TrimRight(cfg.LocalPath, "/")
	filter, err := newSyncFilter(cfg, strings.TrimRight(cfg.MirrorPath, "/"))
	if err != nil {
		return fmt.Errorf("peer baseline type check: loading filters: %w", err)
	}
	for rel := range baseline {
		if err := validateTombstoneRel(rel); err != nil {
			return fmt.Errorf("peer baseline type check: %w", err)
		}
		// A newly introduced immutable or operator filter deliberately retires a
		// stale baseline key. Its on-disk type is outside the current payload and
		// must not block reconciliation (runtime sockets are a common example).
		if isPeerVolatileRel(rel) || filter.shouldSkipFileOrAncestor(rel) {
			continue
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		current := root
		missing := false
		for i, part := range parts {
			current = filepath.Join(current, filepath.FromSlash(part))
			info, err := os.Lstat(current)
			if os.IsNotExist(err) {
				missing = true
				break
			}
			if err != nil {
				return fmt.Errorf("peer baseline type check %q: %w", rel, err)
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("peer baseline type check %q: component %q is a symlink", rel, strings.Join(parts[:i+1], "/"))
			}
			if i < len(parts)-1 && !info.IsDir() {
				return fmt.Errorf("peer baseline type check %q: component %q is not a directory", rel, strings.Join(parts[:i+1], "/"))
			}
			if i == len(parts)-1 && !info.Mode().IsRegular() {
				return fmt.Errorf("peer baseline type check %q: payload is not a regular file", rel)
			}
		}
		if missing {
			continue
		}
	}
	return nil
}

// PeerFilterArgs returns the read-only filter layer used by remote inventory
// probes. It intentionally does not include direction-specific flags or a
// destination, making it safe to reuse for the plan and diff commands.
//
// The include layer names the materialized runtime file, never the canonical
// store path: under a preview that file lives in a temp directory and the
// store path may not exist at all. The submodule and shared layers sit where
// commonArgs puts them, so the remote listing agrees with the Go inventory
// and the transfer (#231).
func PeerFilterArgs(cfg *Config, rf runtimeFilters) []string {
	if cfg == nil {
		return nil
	}
	args := append([]string{}, alwaysExcludeArgs()...)
	if rf.SubmodulesDyn != "" {
		args = append(args, "--exclude-from="+rf.SubmodulesDyn)
	}
	args = append(args, peerVolatileExcludeArgs(cfg)...)
	if rf.WorktreesDyn != "" {
		args = append(args, "--exclude-from="+rf.WorktreesDyn)
	}
	args = append(args, secretsFilterArgs(cfg.AllowPatterns)...)
	for _, f := range []string{cfg.ExcludesFile, cfg.IgnoreFile, rf.SharedDyn} {
		if f != "" {
			args = append(args, "--exclude-from="+f)
		}
	}
	if normalizeFilterMode(cfg.FilterMode) == FilterModeInclude {
		args = append(args, "--include=*/")
		if rf.TrackedDyn != "" {
			args = append(args, "--include-from="+rf.TrackedDyn)
		}
		for _, p := range cfg.IncludePatterns {
			p = strings.TrimSpace(p)
			if p != "" {
				args = append(args, "--include="+rsyncCaseFoldPattern(p))
			}
		}
		args = append(args, "--exclude=*")
	}
	return args
}

// PreparePeerPlanFilters materializes the same tracked/baseline and shared
// runtime filter files that a real peer transfer uses. The remote inventory
// runs before the scoped transfer, so include-mode profiles need this hook or
// a fresh profile would point rsync at a nonexistent tracked-includes file.
//
// A preview (dryRun) lands the files in a temp directory instead of the
// store. The caller must hold the returned value until the inventory has run
// and then call Cleanup.
func PreparePeerPlanFilters(cfg *Config, dryRun bool) (runtimeFilters, error) {
	return prepareRuntimeFilters(cfg, dryRun)
}

// IsPeerVolatile reports whether a relative path belongs to the immutable
// peer-only deny layer. It is also used by focused tests to ensure graphify-out
// remains transferable.
func IsPeerVolatile(rel string) bool { return isPeerVolatileRel(normalizeRel(rel)) }

func isPeerVolatileRel(rel string) bool {
	if rel == "" {
		return false
	}
	parts := strings.Split(rel, "/")
	for _, part := range parts {
		switch part {
		case ".cache", "test-results", "playwright-report", ".astro":
			return true
		}
		if strings.HasSuffix(part, ".tsbuildinfo") || part == ".metadata_never_index" {
			return true
		}
	}
	deskOutput := strings.TrimPrefix(rel, ".maru/desk-pipeline/")
	isDeskOutput := deskOutput != rel && !strings.Contains(deskOutput, "/") && strings.HasSuffix(deskOutput, ".out")
	return rel == ".maru/cache" || strings.HasPrefix(rel, ".maru/cache/") ||
		rel == ".maru/desk-pipeline/logs" || strings.HasPrefix(rel, ".maru/desk-pipeline/logs/") ||
		isDeskOutput ||
		rel == ".maru/runs" || strings.HasPrefix(rel, ".maru/runs/") ||
		rel == "scratchpad/temp" || strings.HasPrefix(rel, "scratchpad/temp/")
}

// PeerBaselineReady reports whether a baseline is authorized for this exact
// SSH target. A target marker is written only after a successful full peer
// push, so callers can fail closed before applying deletes.
func PeerBaselineReady(cfg *Config) (bool, error) { return peerBaselineMatchesTarget(cfg) }

// CommitPeerBaseline records the plan's proven converged snapshot and target
// provenance. It deliberately does not re-scan the live local tree: an edit
// made after its transfer must remain a change against this baseline on the
// next run, rather than being recorded as if it reached the peer.
func CommitPeerBaseline(cfg *Config, snapshot PeerSnapshot) error {
	if cfg == nil || cfg.Profile != PeerProfile || !cfg.Target.IsSSH() {
		return fmt.Errorf("commit peer baseline: requires SSH peer profile")
	}
	if cfg.LocalPaths == nil {
		return fmt.Errorf("commit peer baseline: local paths unresolved")
	}
	entries := make(map[string]Fingerprint, len(snapshot))
	for rel, f := range snapshot {
		if err := validateTombstoneRel(rel); err != nil {
			return fmt.Errorf("commit peer baseline: %w", err)
		}
		if f.Present {
			entries[rel] = f.FP
		}
	}
	if err := SaveBaselineManifest(cfg.LocalPaths.BaselineFile, entries); err != nil {
		return fmt.Errorf("saving peer baseline: %w", err)
	}
	if err := markPeerBaselineTarget(cfg); err != nil {
		return err
	}
	return nil
}

// AppendPeerConflictAudit records coordinator decisions that may not have a
// second payload file to inspect later (notably edit/delete conflicts). The
// JSONL file is machine-local under the peer store and is appended only after
// a complete transaction.
func AppendPeerConflictAudit(cfg *Config, plan *PeerPlan) error {
	if cfg == nil || plan == nil || len(plan.Conflicts) == 0 {
		return nil
	}
	if cfg.ConfigDir == "" {
		return fmt.Errorf("peer conflict audit: config dir unresolved")
	}
	path := filepath.Join(cfg.ConfigDir, "peer-conflicts.log")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("peer conflict audit: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("peer conflict audit: %w", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	now := time.Now().UTC()
	for _, conflict := range plan.Conflicts {
		if err := validateTombstoneRel(conflict.RelPath); err != nil {
			return fmt.Errorf("peer conflict audit: %w", err)
		}
		entry := struct {
			At      time.Time `json:"at"`
			Target  string    `json:"target"`
			RelPath string    `json:"relPath"`
			Reason  string    `json:"reason"`
		}{At: now, Target: cfg.Target.String(), RelPath: conflict.RelPath, Reason: conflict.Reason}
		if err := enc.Encode(entry); err != nil {
			return fmt.Errorf("peer conflict audit: %w", err)
		}
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("peer conflict audit: %w", err)
	}
	return nil
}

func peerPlanList(cfg *Config, rels []string, dryRun bool) (string, func(), error) {
	if cfg == nil {
		return "", nil, fmt.Errorf("peer plan list: config dir unresolved")
	}
	dir := cfg.ConfigDir
	cleanup := func() {}
	if dryRun {
		// A preview's plan list is consumed by the same run's rsync, so it
		// goes to a temp directory rather than the store - the same pattern
		// prepareRuntimeFilters uses for the dyn filter files.
		tmp, err := os.MkdirTemp("", "dot-peer-plan-")
		if err != nil {
			return "", nil, fmt.Errorf("creating preview plan list dir: %w", err)
		}
		dir = tmp
		cleanup = func() { _ = os.RemoveAll(tmp) }
	}
	if dir == "" {
		return "", nil, fmt.Errorf("peer plan list: config dir unresolved")
	}
	path := filepath.Join(dir, "peer-plan-files.dyn")
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

func peerScopedArgs(cfg *Config, rf runtimeFilters, rels []string, dryRun bool) ([]string, func(), error) {
	list, cleanup, err := peerPlanList(cfg, rels, dryRun)
	if err != nil {
		return nil, nil, err
	}
	// Keep the code-owned deny layer ahead of editable include rules for the
	// same first-match-wins reason as the regular peer arg builders.
	args := append([]string{}, peerVolatileExcludeArgs(cfg)...)
	args = append(args, commonArgs(cfg, rf)...)
	args = append(args, "--files-from="+list, "--from0")
	if dryRun {
		args = append(args, "--dry-run")
	}
	args = append(args, rsyncTransportArgs(cfg)...)
	return args, cleanup, nil
}

// PullPeerPlan applies only the remote-present paths selected by the plan.
// No backup is used: these paths were proven remote-only or are equal. A
// simultaneous conflict is never included in Pull.
func PullPeerPlan(ctx context.Context, runner *exec.Runner, cfg *Config, plan *PeerPlan, dryRun bool) error {
	if plan == nil || len(plan.Pull) == 0 {
		return nil
	}
	rf, err := prepareRuntimeFilters(cfg, dryRun)
	defer rf.Cleanup()
	if err != nil {
		return err
	}
	args, cleanup, err := peerScopedArgs(cfg, rf, plan.Pull, dryRun)
	if err != nil {
		return err
	}
	defer cleanup()
	args = append(args, cfg.Target.RsyncDest(), cfg.LocalPath)
	return runRsync(ctx, runner, cfg, args)
}

func peerPushPass(ctx context.Context, runner *exec.Runner, cfg *Config, rels []string, conflict *ConflictDir, backup, dryRun bool) error {
	if len(rels) == 0 {
		return nil
	}
	rf, err := prepareRuntimeFilters(cfg, dryRun)
	defer rf.Cleanup()
	if err != nil {
		return err
	}
	args, cleanup, err := peerScopedArgs(cfg, rf, rels, dryRun)
	if err != nil {
		return err
	}
	defer cleanup()
	if backup {
		args = append(args, "--backup", "--backup-dir="+conflict.PushBackupRel())
	}
	args = append(args, cfg.LocalPath, cfg.Target.RsyncDest())
	return runRsync(ctx, runner, cfg, args)
}

// PushPeerPlan sends coordinator-present paths. Conflict paths are sent in a
// separate backup-enabled pass, so each losing remote payload is quarantined
// exactly once; ordinary creates/updates remain backup-free.
func PushPeerPlan(ctx context.Context, runner *exec.Runner, cfg *Config, plan *PeerPlan, conflict *ConflictDir, dryRun bool) error {
	if plan == nil {
		return nil
	}
	if len(plan.QuarantineRemote) > 0 {
		if conflict == nil {
			return fmt.Errorf("peer conflict push: conflict directory unresolved")
		}
		// The backup pass writes below the remote workspace. Refuse a symlinked
		// or otherwise unsafe conflict root before rsync interprets --backup-dir.
		if err := preflightPeerQuarantine(ctx, runner, cfg, conflict, !dryRun); err != nil {
			return err
		}
	}
	if err := peerPushPass(ctx, runner, cfg, plan.QuarantineRemote, conflict, true, dryRun); err != nil {
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
	return peerPushPass(ctx, runner, cfg, normal, conflict, false, dryRun)
}

// DeletePeerLocal accepts a remote-only deletion without unlinking the local
// payload. The prior coordinator copy is moved into a local quarantine so a
// remote delete remains recoverable while the two trees converge.
func DeletePeerLocal(cfg *Config, conflict *ConflictDir, rels []string, dryRun bool) error {
	if cfg == nil || cfg.LocalPaths == nil {
		return fmt.Errorf("peer local delete: local paths unresolved")
	}
	if !dryRun && conflict == nil {
		return fmt.Errorf("peer local delete: conflict directory unresolved")
	}
	root := strings.TrimRight(cfg.LocalPath, "/")
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("peer local delete: checking workspace root: %w", err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return fmt.Errorf("peer local delete: workspace root is not a directory")
	}
	for _, rel := range rels {
		if err := validateTombstoneRel(rel); err != nil {
			return fmt.Errorf("peer local delete: %w", err)
		}
		src := filepath.Join(root, filepath.FromSlash(rel))
		info, err := os.Lstat(src)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("peer local delete %s: %w", rel, err)
		}
		if info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("peer local delete %s: refusing non-regular payload", rel)
		}
		if dryRun {
			continue
		}
		dst, err := ensurePeerLocalQuarantineDir(root, conflict.Timestamp, rel)
		if err != nil {
			return fmt.Errorf("peer local delete quarantine %s: %w", rel, err)
		}
		if _, err := os.Lstat(dst); err == nil {
			return fmt.Errorf("peer local delete quarantine %s: destination already exists", rel)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("peer local delete quarantine %s: checking destination: %w", rel, err)
		}
		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("peer local delete quarantine %s: %w", rel, err)
		}
	}
	return nil
}

// ensurePeerLocalQuarantineDir creates and validates every directory below the
// workspace before a remote-only deletion is moved there. In particular, a
// pre-existing .sync-conflicts symlink must never turn a recoverable delete
// into a write outside the workspace.
func ensurePeerLocalQuarantineDir(root, timestamp, rel string) (string, error) {
	return ensurePeerQuarantineDir(root, conflictsDirName, timestamp, rel)
}

// ensurePeerQuarantineDir is the root-name-parameterized form, shared with the
// tracked-home pass whose quarantine root is .dot-peer-conflicts under $HOME.
func ensurePeerQuarantineDir(root, dirName, timestamp, rel string) (string, error) {
	dir := filepath.Join(root, dirName, timestamp, "from-peer")
	for _, path := range []string{
		filepath.Join(root, dirName),
		filepath.Join(root, dirName, timestamp),
		filepath.Join(root, dirName, timestamp, "from-peer"),
	} {
		if err := ensurePeerLocalDirectory(path); err != nil {
			return "", err
		}
	}
	parent := filepath.ToSlash(filepath.Dir(rel))
	if parent != "." {
		for _, component := range strings.Split(parent, "/") {
			dir = filepath.Join(dir, filepath.FromSlash(component))
			if err := ensurePeerLocalDirectory(dir); err != nil {
				return "", err
			}
		}
	}
	return filepath.Join(dir, filepath.Base(filepath.FromSlash(rel))), nil
}

func ensurePeerLocalDirectory(path string) error {
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("unsafe quarantine directory %s", path)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		return err
	}
	info, err = os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("unsafe quarantine directory %s", path)
	}
	return nil
}
