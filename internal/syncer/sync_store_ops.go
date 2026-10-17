package syncer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// trimTrailingSlash drops one trailing separator, leaving a bare "/" alone.
// It is deliberately not strings.TrimRight: the cli helper this was moved
// beside strips exactly one slash, and "//" must stay "/" rather than become
// the empty string.
func trimTrailingSlash(p string) string {
	if len(p) > 1 && p[len(p)-1] == '/' {
		return p[:len(p)-1]
	}
	return p
}

// EditableLocalConfig loads the workspace-local config for mutation,
// substituting a defaulted one when the store has none yet.
func EditableLocalConfig(cfg *Config) (*LocalConfig, error) {
	if cfg.LocalPaths == nil {
		return nil, fmt.Errorf("local paths unresolved")
	}
	localCfg, ok, err := LoadLocalConfig(cfg.LocalPaths)
	if err != nil {
		return nil, err
	}
	if !ok {
		localCfg = &LocalConfig{Propagation: DefaultPropagationPolicy()}
	}
	return localCfg, nil
}

// SetLocalSchedule mutates LocalConfig scheduler settings, persists, and
// keeps cfg in sync.
func SetLocalSchedule(cfg *Config, pushInterval, pullInterval int, pushMode, pullMode RunMode, dryRun bool) error {
	if cfg.LocalPaths == nil {
		return fmt.Errorf("local paths unresolved")
	}
	schedule, err := (ScheduleSettings{
		Interval:     pushInterval,
		PullInterval: pullInterval,
		PushMode:     pushMode,
		PullMode:     pullMode,
	}).Normalize()
	if err != nil {
		return err
	}
	local, ok, err := LoadLocalConfig(cfg.LocalPaths)
	if err != nil {
		return err
	}
	if !ok {
		local = &LocalConfig{Propagation: DefaultPropagationPolicy()}
	}
	schedule.ApplyToLocalConfig(local)
	if !dryRun {
		if err := SaveLocalConfig(cfg.LocalPaths, local); err != nil {
			return err
		}
	}
	cfg.Interval = schedule.Interval
	cfg.PullInterval = schedule.PullInterval
	cfg.PushMode = schedule.PushMode
	cfg.PullMode = schedule.PullMode
	return nil
}

// SetLocalOwner records the scheduler owner in the workspace-local config and
// keeps the resolved config in sync. Dry runs update only the in-memory config
// so later output can describe the setup they would install.
func SetLocalOwner(cfg *Config, owner string, dryRun bool) error {
	if cfg.LocalPaths == nil {
		return fmt.Errorf("local paths unresolved")
	}
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return fmt.Errorf("owner must not be empty")
	}
	local, err := EditableLocalConfig(cfg)
	if err != nil {
		return err
	}
	AssignOwner(local, owner)
	if !dryRun {
		if err := SaveLocalConfig(cfg.LocalPaths, local); err != nil {
			return err
		}
	}
	cfg.Owner = owner
	cfg.OwnerAliases = local.OwnerAliases
	return nil
}

// SetLocalPaused mutates the local config's Paused field, persists, and
// keeps cfg in sync so callers see the new value without re-running
// ResolveConfig.
func SetLocalPaused(cfg *Config, paused bool) error {
	if cfg.LocalPaths == nil {
		return fmt.Errorf("local paths unresolved")
	}
	local, ok, err := LoadLocalConfig(cfg.LocalPaths)
	if err != nil {
		return err
	}
	if !ok {
		// Should not happen — ResolveConfig migrates first. Defensive fallback.
		local = &LocalConfig{Propagation: DefaultPropagationPolicy()}
	}
	local.Paused = paused
	if err := SaveLocalConfig(cfg.LocalPaths, local); err != nil {
		return err
	}
	cfg.Paused = paused
	return nil
}

// SetLocalTarget writes the target into the workspace-local config so it takes
// effect immediately. Local targets also update the legacy MirrorPath field so
// older binaries reading this workspace resolve the same mirror.
func SetLocalTarget(cfg *Config, target Target) error {
	if cfg.LocalPaths == nil {
		return fmt.Errorf("local paths unresolved — bug in ResolveConfig")
	}
	localCfg, _, err := LoadLocalConfig(cfg.LocalPaths)
	if err != nil {
		return fmt.Errorf("load local config: %w", err)
	}
	localCfg.Target = target.String()
	if target.Kind == TargetLocal {
		localCfg.MirrorPath = target.Path
	}
	if err := SaveLocalConfig(cfg.LocalPaths, localCfg); err != nil {
		return fmt.Errorf("save local config: %w", err)
	}
	return nil
}

// InitResult describes the store `dot sync init` just healed, or — when DryRun
// is set — the one it would have healed.
type InitResult struct {
	// DryRun reports that nothing below was created. The engine returns the
	// flag and the paths; cli owns the wording it renders them with.
	DryRun bool

	// LegacyStoreDir names the pre-rename store the run would migrate to
	// StoreDir before doing anything else. Set only under DryRun, and only when
	// one is actually pending: a real run performs the rename and reports it on
	// stderr from resolveConfig, so there is nothing left to preview.
	LegacyStoreDir string

	StoreDir    string
	Workspace   string
	Mirror      string
	Propagation PropagationPolicy
	FilterMode  FilterMode
	InboxDir    string
	ConfigFile  string
	IncludeFile string
	IgnoreFile  string

	// WorkspaceIgnore is the one WORKSPACE-level file EnsureLocalLayout
	// touches: it ends with appendGitignoreBlock. Everything else it creates
	// lives under StoreDir.
	WorkspaceIgnore string
}

// InitStore heals the per-workspace store and creates the intake staging dir.
//
// Under dryRun it computes the same fully-populated result from the resolved
// paths and creates nothing. Creating the store is what this command is for, but
// a preview of that creation is still a preview (D-03), and since Bootstrap now
// takes the read-only resolver under --dry-run the tree this would heal may not
// exist at all.
//
// Without dryRun, Bootstrap has already triggered LoadOrMigrateLocalConfig, so
// the .dotfiles/<profile>/ tree exists by the time this runs. Heal anything
// missing (the operator may have deleted files) and create inbox/gdrive.
func InitStore(cfg *Config, dryRun bool) (*InitResult, error) {
	paths := cfg.LocalPaths
	if paths == nil {
		return nil, fmt.Errorf("local paths unresolved — bug in ResolveConfig")
	}
	var legacyStore string
	if dryRun {
		// Bootstrap hands a dry run the READ-ONLY resolver, which keeps the
		// pre-rename fallback. That is right for a read-only command and wrong
		// for this one: a real `dot sync init` migrates first and then operates
		// under .dotfiles/sync, so previewing the pre-migration paths describes
		// a world the run will have left behind. Re-resolve past the fallback
		// and name the rename as work this command would do. The two resolvers
		// agree when nothing is pending, so this is a no-op on a migrated
		// workspace.
		legacyStore = pendingLegacyStore(cfg.LocalPath)
		paths = ResolveLocalPathsPostMigration(cfg.LocalPath, cfg.Profile)
	}
	inboxGdrive := trimTrailingSlash(cfg.LocalPath) + "/inbox/gdrive"
	if !dryRun {
		if err := EnsureLocalLayout(paths); err != nil {
			return nil, fmt.Errorf("ensure layout: %w", err)
		}
		if err := os.MkdirAll(inboxGdrive, 0755); err != nil {
			return nil, fmt.Errorf("create inbox/gdrive: %w", err)
		}
	}
	return &InitResult{
		DryRun:         dryRun,
		LegacyStoreDir: legacyStore,
		StoreDir:       paths.StoreDir,
		Workspace:      trimTrailingSlash(cfg.LocalPath),
		Mirror:         trimTrailingSlash(cfg.MirrorPath),
		Propagation:    cfg.Propagation,
		FilterMode:     cfg.FilterMode,
		InboxDir:       inboxGdrive,
		ConfigFile:     paths.ConfigFile,
		IncludeFile:    paths.IncludeFile,
		IgnoreFile:     paths.IgnoreFile,

		WorkspaceIgnore: paths.WorkspaceIgnore,
	}, nil
}

// InboxReport summarizes the mirror intake staging area.
type InboxReport struct {
	StagingRoot string
	RunDirs     int
	Files       int
	Imports     int
	Tombstones  []Tombstone
}

// InboxSummary counts what is staged and tracked under the profile store.
func InboxSummary(cfg *Config) (*InboxReport, error) {
	if cfg.LocalPaths == nil {
		return nil, fmt.Errorf("local paths unresolved")
	}
	stagingRoot := trimTrailingSlash(cfg.LocalPath) + "/inbox/gdrive"
	runDirs, _ := os.ReadDir(stagingRoot)
	report := &InboxReport{StagingRoot: stagingRoot}
	for _, e := range runDirs {
		if !e.IsDir() {
			continue
		}
		report.RunDirs++
		_ = filepath.WalkDir(filepath.Join(stagingRoot, e.Name()), func(_ string, d fs.DirEntry, _ error) error {
			if d != nil && !d.IsDir() {
				report.Files++
			}
			return nil
		})
	}

	imports, err := LoadImportsManifest(cfg.LocalPaths.ImportsFile)
	if err != nil {
		return nil, fmt.Errorf("loading imports: %w", err)
	}
	tomb, err := LoadTombstones(cfg.LocalPaths.TombstonesFile)
	if err != nil {
		return nil, fmt.Errorf("loading tombstones: %w", err)
	}
	report.Imports = len(imports)
	report.Tombstones = tomb
	return report, nil
}

// InboxForget drops a path from imports.manifest so the next intake re-stages
// it. It reports whether an entry was actually present.
func InboxForget(cfg *Config, raw string) (bool, error) {
	if cfg.LocalPaths == nil {
		return false, fmt.Errorf("local paths unresolved")
	}
	rel := strings.TrimSpace(raw)
	if rel == "" {
		return false, fmt.Errorf("relpath cannot be empty")
	}
	return ForgetImport(cfg.LocalPaths, rel)
}

// InboxManifestCounts reports how many imports and tombstones a clear would
// discard. Load failures collapse to zero, as they did before the move: an
// unreadable manifest is reported as "already empty" and the clear is a no-op.
func InboxManifestCounts(cfg *Config) (int, int, error) {
	if cfg.LocalPaths == nil {
		return 0, 0, fmt.Errorf("local paths unresolved")
	}
	imports, _ := LoadImportsManifest(cfg.LocalPaths.ImportsFile)
	tomb, _ := LoadTombstones(cfg.LocalPaths.TombstonesFile)
	return len(imports), len(tomb), nil
}

// SharedCount reports how many manual shared-exclude entries are configured,
// counted in the form `shared list` shows (treeRel), so the two agree. An
// entry the cleaning drops counts by its stored text, as the list shows it
// (#231).
func SharedCount(cfg *Config) (int, error) {
	localCfg, err := EditableLocalConfig(cfg)
	if err != nil {
		return 0, err
	}
	return len(StoredSharedEntries(localCfg.SharedExcludes)), nil
}

// StoredSharedEntries returns every stored shared entry once, in the form
// `shared list` shows it: cleaned (treeRel) where the cleaning keeps it, the
// stored text where it drops it (#231).
func StoredSharedEntries(manual []string) []string {
	var out []string
	for _, e := range manual {
		e = strings.TrimSpace(e)
		if rel := treeRel(e); rel != "" {
			e = rel
		}
		if !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	sort.Strings(out)
	return out
}

// DroppedSharedEntries returns the stored shared entries that name no path
// under the workspace (blank, an absolute path, one outside the tree), which
// ScanShared and both filter sides ignore. `shared list` shows them so they
// can be removed by their stored text (#231).
func DroppedSharedEntries(manual []string) []string {
	var out []string
	for _, e := range manual {
		if e = strings.TrimSpace(e); treeRel(e) == "" && !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	sort.Strings(out)
	return out
}

// SharedAdd appends the given paths to the manual shared-excludes list,
// returning only the ones that were not already present.
func SharedAdd(cfg *Config, args []string) ([]string, error) {
	mirror := trimTrailingSlash(cfg.MirrorPath)
	added := make([]string, 0, len(args))
	localCfg, err := EditableLocalConfig(cfg)
	if err != nil {
		return nil, err
	}
	current := append([]string(nil), localCfg.SharedExcludes...)

	for _, raw := range args {
		rel, err := RelativizeForMirror(raw, mirror)
		if err != nil {
			return nil, err
		}
		if !containsSharedPath(current, rel) {
			current = append(current, rel)
			added = append(added, rel)
		}
	}

	dedupedSorted := dedupSortedStrings(current)
	localCfg.SharedExcludes = dedupedSorted
	if err := SaveLocalConfig(cfg.LocalPaths, localCfg); err != nil {
		return nil, fmt.Errorf("saving local config: %w", err)
	}
	cfg.SharedExcludes = dedupedSorted
	return added, nil
}

// SharedRemove drops the given paths from the manual shared-excludes list,
// returning only the ones that were actually present.
func SharedRemove(cfg *Config, args []string) ([]string, error) {
	mirror := trimTrailingSlash(cfg.MirrorPath)
	removed := make([]string, 0, len(args))
	localCfg, err := EditableLocalConfig(cfg)
	if err != nil {
		return nil, err
	}
	current := append([]string(nil), localCfg.SharedExcludes...)

	for _, raw := range args {
		// An entry the cleaning drops is removed by the stored text `shared
		// list` shows for it.
		if dropped := strings.TrimSpace(raw); slices.Contains(DroppedSharedEntries(current), dropped) {
			current = slices.DeleteFunc(current, func(e string) bool { return strings.TrimSpace(e) == dropped })
			removed = append(removed, dropped)
			continue
		}
		rel, err := RelativizeForMirror(raw, mirror)
		if err != nil {
			return nil, err
		}
		next := current[:0]
		gone := false
		for _, e := range current {
			// A hand-edited entry ("team//ops") is removed by the form
			// `shared list` shows for it.
			if treeRel(strings.TrimSpace(e)) == rel {
				gone = true
				continue
			}
			next = append(next, e)
		}
		current = next
		if gone {
			removed = append(removed, rel)
		}
	}

	localCfg.SharedExcludes = current
	if err := SaveLocalConfig(cfg.LocalPaths, localCfg); err != nil {
		return nil, fmt.Errorf("saving local config: %w", err)
	}
	cfg.SharedExcludes = current
	return removed, nil
}

// SharedClear empties the manual shared-excludes list.
func SharedClear(cfg *Config) error {
	localCfg, err := EditableLocalConfig(cfg)
	if err != nil {
		return err
	}
	localCfg.SharedExcludes = nil
	if err := SaveLocalConfig(cfg.LocalPaths, localCfg); err != nil {
		return fmt.Errorf("saving local config: %w", err)
	}
	cfg.SharedExcludes = nil
	return nil
}

// RelativizeForMirror normalizes a user-supplied path so it lives under
// mirror as a relative path. Absolute paths must be inside mirror. The
// result is the canonical form every shared-entry operation and both filter
// sides use (treeRel: "team//ops" becomes "team/ops"). Empty results, the
// mirror root, parent escapes and a path that cannot be one filter line are
// rejected (#228).
func RelativizeForMirror(raw, mirror string) (string, error) {
	cleaned := strings.TrimSpace(raw)
	if cleaned == "" {
		return "", fmt.Errorf("empty path")
	}
	if filepath.IsAbs(cleaned) {
		mirrorAbs, err := filepath.Abs(mirror)
		if err != nil {
			return "", fmt.Errorf("resolving mirror %q: %w", mirror, err)
		}
		rel, err := filepath.Rel(mirrorAbs, cleaned)
		if err != nil {
			return "", fmt.Errorf("relativizing %q against %q: %w", cleaned, mirror, err)
		}
		cleaned = rel
	}
	rel := treeRel(filepath.ToSlash(cleaned))
	if rel == "" {
		if normalizeRel(path.Clean(filepath.ToSlash(cleaned))) == "" {
			return "", fmt.Errorf("path resolves to mirror root, refusing to exclude everything")
		}
		return "", fmt.Errorf("path %q escapes mirror root", raw)
	}
	if _, err := literalRsyncPattern(rel); err != nil {
		return "", fmt.Errorf("path %q cannot be a shared exclude: %w", raw, err)
	}
	return rel, nil
}

// dedupSortedStrings returns a stable, sorted copy of in with duplicates
// removed.
func dedupSortedStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// containsSharedPath reports whether the shared-excludes list already holds
// this relative path.
func containsSharedPath(haystack []string, needle string) bool {
	for _, s := range haystack {
		if treeRel(strings.TrimSpace(s)) == needle {
			return true
		}
	}
	return false
}

// OwnerOptions selects how `dot sync owner` rewrites the profile's writer.
// Exactly one of Clear, SetSelf or SetTo is meaningful; Clear wins, then
// SetSelf, matching the pre-move precedence.
type OwnerOptions struct {
	Config  *Config
	Clear   bool
	SetSelf bool
	SetTo   string
	DryRun  bool // report the owner it would write; write nothing
}

// SetOwner records which machine may push this profile and returns the owner
// as written; an empty string means the restriction was removed.
//
// On the peer profile every deliberate owner change also bumps the owner
// epoch and clears a pending fence: a recorded decision outranks any earlier
// takeover, and the epoch is what the first contact after a switch compares.
// An explicit Set/SetSelf bumps even when the owner is unchanged: the
// equal-epoch fence recovery tells the operator to set one coordinator on
// both machines, and only a bump on both keeps the chosen machine from
// reading the other's bumped record as a lost fence and demoting itself.
func SetOwner(opts OwnerOptions) (string, error) {
	cfg := opts.Config
	paths := cfg.LocalPaths
	local, ok, err := LoadLocalConfig(paths)
	if err != nil {
		return "", err
	}
	if !ok || local == nil {
		return "", fmt.Errorf("profile %q has no config yet; run dot sync init first", cfg.Profile)
	}
	previous := local.Owner
	// A deliberate owner change is a new decision, not a rename: earlier
	// names no longer stand for the owner, even when the name is unchanged.
	local.OwnerAliases = nil
	switch {
	case opts.Clear:
		local.Owner = ""
	case opts.SetSelf:
		local.Owner = PreferredMachineName()
		if local.Owner == "" {
			return "", fmt.Errorf("cannot determine this machine's name")
		}
	default:
		local.Owner = opts.SetTo
	}
	explicitSet := opts.SetSelf || opts.SetTo != ""
	if cfg.Profile == PeerProfile && (local.Owner != previous || explicitSet) {
		local.OwnerEpoch++
		local.FencePending = false
	}
	if opts.DryRun {
		return local.Owner, nil
	}
	if err := SaveLocalConfig(paths, local); err != nil {
		return "", err
	}
	return local.Owner, nil
}

// OwnerRenameResult names the profile stores RenameOwner rewrote.
type OwnerRenameResult struct {
	Profiles []string
	// Already names the stores a previous run renamed (owner <new>, alias
	// <old>), so a retry can go on to the peer.
	Already []string
	// OldKept says <old> was recorded as an alias (a generic name is not).
	OldKept bool
}

// ErrNoProfileOwned is RenameOwner's answer when no store is owned by <old>.
var ErrNoProfileOwned = errors.New("no profile is owned by that name")

// RenameOwner records that the owner machine was renamed from oldName to
// newName in every profile store of the workspace whose owner, or one of
// whose aliases, is oldName (#185). The old name stays as an alias, so the
// guard keeps matching a machine not renamed yet and a peer not migrated yet.
// Nothing else changes: not the epoch (a rename is not a coordinator
// change), not the target, not a baseline, so no run plans a deletion.
//
// keepAlias records <old> as an alias; only a Mac that answers to <old> or
// <new> needs it (the fence reads the coordinator's), so the other Mac's
// migration step records none.
func RenameOwner(workspaceRoot, oldName, newName string, dryRun, keepAlias bool) (*OwnerRenameResult, error) {
	oldName, newName = strings.TrimSpace(oldName), strings.TrimSpace(newName)
	if oldName == "" || newName == "" || strings.ContainsAny(newName, " \t\r\n'\"/") {
		return nil, fmt.Errorf("owner rename needs an old and a new machine name, the new one without spaces, quotes or slashes (got %q -> %q)", oldName, newName)
	}
	if strings.HasPrefix(oldName, "-") || strings.HasPrefix(newName, "-") {
		return nil, fmt.Errorf("owner rename: a machine name cannot start with '-' (got %q -> %q)", oldName, newName)
	}
	if strings.ContainsAny(oldName, "'\"\n") {
		return nil, fmt.Errorf("owner rename: the old name %q holds a quote; record the owner with dot sync owner --set instead", oldName)
	}
	if genericMachineNames[NormalizeHostname(newName)] {
		return nil, fmt.Errorf("owner rename: %q is a generic name many Macs answer to; give the Mac a specific name first", newName)
	}
	if NormalizeHostname(oldName) == NormalizeHostname(newName) {
		return nil, fmt.Errorf("owner rename: %q and %q are the same name", oldName, newName)
	}
	entries, err := os.ReadDir(filepath.Join(workspaceRoot, ".dotfiles"))
	if err != nil {
		return nil, fmt.Errorf("owner rename: reading profile stores: %w", err)
	}
	result := &OwnerRenameResult{}
	for _, entry := range entries {
		if !entry.IsDir() || ValidateProfile(entry.Name()) != nil {
			continue
		}
		paths := ResolveLocalPathsForProfile(workspaceRoot, entry.Name())
		local, ok, err := LoadLocalConfig(paths)
		if err != nil {
			return nil, err
		}
		if !ok || local == nil {
			continue
		}
		if ownersMatch(local.Owner, nil, newName) && ownersMatch(oldName, nil, local.OwnerAliases...) {
			result.Already = append(result.Already, entry.Name())
			continue
		}
		// Only the current owner is renamed. An alias names a machine as it
		// was; renaming through it would let a second Mac take the owner.
		if !ownersMatch(local.Owner, nil, oldName) {
			continue
		}
		seen := map[string]bool{NormalizeHostname(newName): true}
		var aliases []string
		earlier := slices.Clone(local.OwnerAliases)
		if keepAlias {
			earlier = append(earlier, local.Owner)
		}
		for _, alias := range earlier {
			// A generic name ("mac") identifies no machine; keeping it would
			// let any Mac with an unset HostName pass the guard.
			if n := NormalizeHostname(alias); n != "" && !seen[n] && !genericMachineNames[n] {
				seen[n] = true
				aliases = append(aliases, alias)
			}
		}
		local.Owner, local.OwnerAliases = newName, aliases
		result.OldKept = slices.ContainsFunc(aliases, func(a string) bool { return NormalizeHostname(a) == NormalizeHostname(oldName) })
		if !dryRun {
			if err := SaveLocalConfig(paths, local); err != nil {
				return nil, err
			}
		}
		result.Profiles = append(result.Profiles, entry.Name())
	}
	if len(result.Profiles)+len(result.Already) == 0 {
		return nil, fmt.Errorf("owner rename: no profile under %s is owned by %q: %w", filepath.Join(workspaceRoot, ".dotfiles"), oldName, ErrNoProfileOwned)
	}
	return result, nil
}

// RetireOwnerAliases drops the recorded earlier names from every profile
// store of the workspace owned by owner. A complete peer run calls it once
// the peer records the same owner: both machines are migrated and the old
// names must stop admitting writes. It returns the profiles it changed.
//
// ponytail: known ceiling. See docs/CEILINGS.md (owner aliases outside the coordinator's peer run).
func RetireOwnerAliases(workspaceRoot, owner string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(workspaceRoot, ".dotfiles"))
	if err != nil {
		return nil, err
	}
	// The peer store goes last: a run retries only while the peer profile
	// still holds aliases, so a failure on another store must leave them.
	slices.SortStableFunc(entries, func(a, b os.DirEntry) int {
		switch {
		case a.Name() == PeerProfile && b.Name() != PeerProfile:
			return 1
		case b.Name() == PeerProfile && a.Name() != PeerProfile:
			return -1
		}
		return 0
	})
	var retired []string
	for _, entry := range entries {
		if !entry.IsDir() || ValidateProfile(entry.Name()) != nil {
			continue
		}
		paths := ResolveLocalPathsForProfile(workspaceRoot, entry.Name())
		local, ok, err := LoadLocalConfig(paths)
		if err != nil {
			return retired, err
		}
		if !ok || local == nil || len(local.OwnerAliases) == 0 || !ownersMatch(local.Owner, nil, owner) {
			continue
		}
		local.OwnerAliases = nil
		if err := SaveLocalConfig(paths, local); err != nil {
			return retired, err
		}
		retired = append(retired, entry.Name())
	}
	return retired, nil
}

// RsyncOutcome names what EnsureRsync did about a missing rsync.
type RsyncOutcome int

const (
	RsyncPresent         RsyncOutcome = iota // already on PATH; Version is set
	RsyncWouldInstall                        // dry-run: an install would have been offered
	RsyncInstallDeclined                     // the operator said no
	RsyncInstalled                           // installed during this run; Version is set
)

// RsyncResult is what EnsureRsync found or did.
type RsyncResult struct {
	Outcome RsyncOutcome
	Version string
}

// EnsureRsync verifies rsync is available, offering to install it when it is
// not. out receives the installer's own output (SEAM-01); the four outcomes
// are worded by the caller.
func EnsureRsync(ctx context.Context, runner *exec.Runner, out io.Writer, dryRun bool, confirm ConfirmFunc) (*RsyncResult, error) {
	ver, ok := CheckRsync(runner)
	if ok {
		return &RsyncResult{Outcome: RsyncPresent, Version: ver}, nil
	}
	if dryRun {
		return &RsyncResult{Outcome: RsyncWouldInstall}, nil
	}
	confirmed, err := askSync(confirm, ConfirmRequest{Kind: ConfirmInstallRsync})
	if err != nil {
		return nil, err
	}
	if !confirmed {
		return &RsyncResult{Outcome: RsyncInstallDeclined}, nil
	}
	if err := InstallRsync(ctx, runner, out); err != nil {
		return nil, fmt.Errorf("installing rsync: %w", err)
	}
	ver, ok = CheckRsync(runner)
	if !ok {
		return nil, fmt.Errorf("rsync not found in PATH after install")
	}
	return &RsyncResult{Outcome: RsyncInstalled, Version: ver}, nil
}

// ResumeResult is what `dot sync resume` changed.
type ResumeResult struct {
	WasPaused        bool
	SchedulerOff     bool // no interval configured, so nothing to re-arm
	SchedulerResumed bool
	SchedulerErr     error
}

// SyncResume clears the paused gate and reattaches an installed scheduler.
func SyncResume(ctx context.Context, cfg *Config, runner *exec.Runner) (*ResumeResult, error) {
	res := &ResumeResult{WasPaused: cfg.Paused}
	if cfg.Paused {
		if err := SetLocalPaused(cfg, false); err != nil {
			return nil, fmt.Errorf("saving local config: %w", err)
		}
	}
	if cfg.Interval == 0 && cfg.PullInterval == 0 {
		res.SchedulerOff = true
		return res, nil
	}
	sched, _, err := ResolveScheduler(cfg, runner)
	if err != nil {
		// The state save succeeded; the scheduler is best-effort.
		return res, nil
	}
	states := [2]SchedulerState{
		sched.StateKind(ctx, SchedulerKindPush),
		sched.StateKind(ctx, SchedulerKindIntake),
	}
	resumed := false
	for index, kind := range [2]SchedulerKind{SchedulerKindPush, SchedulerKindIntake} {
		if states[index] == SchedulerNotInstalled {
			continue
		}
		resumed = true
		if err := sched.ResumeKind(ctx, kind); err != nil {
			res.SchedulerErr = err
			return res, nil
		}
	}
	res.SchedulerResumed = resumed
	return res, nil
}

// PauseResult is what `dot sync pause` changed.
type PauseResult struct {
	WasPaused        bool
	SchedulerStopped bool
	SchedulerErr     error
}

// SyncPause sets the paused gate and stops a running scheduler, so we do not
// waste invocations hitting the paused gate every Interval seconds.
func SyncPause(ctx context.Context, cfg *Config, runner *exec.Runner) (*PauseResult, error) {
	res := &PauseResult{WasPaused: cfg.Paused}
	if !cfg.Paused {
		if err := SetLocalPaused(cfg, true); err != nil {
			return nil, fmt.Errorf("saving local config: %w", err)
		}
	}
	sched, _, err := ResolveScheduler(cfg, runner)
	if err != nil {
		return res, nil
	}
	states := [2]SchedulerState{
		sched.StateKind(ctx, SchedulerKindPush),
		sched.StateKind(ctx, SchedulerKindIntake),
	}
	stopped := false
	for index, kind := range [2]SchedulerKind{SchedulerKindPush, SchedulerKindIntake} {
		if states[index] != SchedulerRunning && states[index] != SchedulerSpawnFailed && states[index] != SchedulerTargetUserActionRequired {
			continue
		}
		stopped = true
		if err := sched.PauseKind(ctx, kind); err != nil {
			res.SchedulerErr = err
			return res, nil
		}
	}
	res.SchedulerStopped = stopped
	return res, nil
}
