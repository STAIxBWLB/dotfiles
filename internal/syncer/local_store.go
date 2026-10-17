package syncer

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/template"
)

// localStoreDir is the per-workspace root for sync runtime state
// and config. Lives directly under the local sync tree (e.g.
// ~/workspace/work/.dotfiles/sync/) so settings travel with the
// workspace instead of in the user's home config dir.
const (
	localStoreDirRel = ".dotfiles/sync"

	// legacyStoreDirRel is the pre-rename store location. Read-only
	// resolution falls back to it, and MigrateLegacyStore renames it.
	legacyStoreDirRel = ".dotfiles/gdrive-sync"

	localConfigName        = "config.yaml"
	localStateName         = "state.yaml"
	localIncludeName       = "include.txt"
	localExcludeName       = "exclude.txt"
	localIgnoreName        = "ignore.txt"
	localAllowName         = "allow.txt"
	localSharedDynName     = "shared-excludes.dyn.conf"
	localSubmodulesDynName = "submodules.dyn.conf"
	localTrackedDynName    = "tracked-includes.dyn.conf"
	localBaselineName      = "baseline.manifest"
	localImportsName       = "imports.manifest"
	localTombstonesName    = "tombstones.log"
	localWorktreesName     = "worktrees.txt"
	localWorktreesDynName  = "worktrees.dyn.conf"
	localLogDirRel         = "log"
	localLogFileName       = "sync.log"
	legacyLogFileName      = "gdrive-sync.log"

	gitignoreBlockHeader = "# dot sync: track shared baseline + filter policy, ignore machine-local state"
)

var gitignoreEntries = []string{
	gitignoreBlockHeader,
	"!/.dotfiles/",
	"/.dotfiles/*",
	"!/.dotfiles/sync/",
	"/.dotfiles/sync/*",
	"!/.dotfiles/sync/baseline.manifest",
	"!/.dotfiles/sync/exclude.txt",
	"!/.dotfiles/sync/ignore.txt",
	"!/.dotfiles/sync/allow.txt",
}

// allowFileHeader documents the secrets opt-in contract at the top of a
// freshly created allow.txt. The operator's env files and tokens may already
// live in the cloud mirror and the private Git remote — allowing them here is
// an explicit, recorded decision, never a default.
const allowFileHeader = `# dot sync allow.txt — explicit secrets opt-in.
#
# Patterns listed here re-include paths that the built-in secrets layer
# excludes by default (/.secrets/**, /.maru/secrets/**, _sys/mcp.local.json,
# **/.env, **/.env.*). One rsync pattern per line; comments start with '#'.
#
# WARNING: every pattern below ships the matching secrets to the sync
# target (cloud mirror or SSH remote). Only add a pattern when you have
# consciously accepted that exposure. 'dot sync status' reports how many
# allow patterns are active.
`

// PropagationPolicy controls which kinds of workspace changes Push
// propagates to the mirror. Default is `{true, true, false}` so the
// safe-by-default behavior is "creates and updates flow, deletes don't".
//
// `Push` translates this into rsync flags (see plan §B). An all-false
// policy is rejected at validation time — there's no rsync invocation
// that does nothing meaningful.
type PropagationPolicy struct {
	Create bool `yaml:"create"`
	Update bool `yaml:"update"`
	Delete bool `yaml:"delete"`
}

// DefaultPropagationPolicy is the safe baseline applied when nothing
// else says otherwise (fresh init, missing config field, etc.).
func DefaultPropagationPolicy() PropagationPolicy {
	return PropagationPolicy{Create: true, Update: true, Delete: false}
}

// Validate rejects no-op combinations.
func (p PropagationPolicy) Validate() error {
	if !p.Create && !p.Update && !p.Delete {
		return fmt.Errorf("propagation must enable at least one of {create, update, delete}")
	}
	return nil
}

// String renders a compact human label like "create+update (delete off)".
func (p PropagationPolicy) String() string {
	on := []string{}
	if p.Create {
		on = append(on, "create")
	}
	if p.Update {
		on = append(on, "update")
	}
	if p.Delete {
		on = append(on, "delete")
	}
	if len(on) == 0 {
		return "(none — invalid)"
	}
	off := []string{}
	if !p.Create {
		off = append(off, "create")
	}
	if !p.Update {
		off = append(off, "update")
	}
	if !p.Delete {
		off = append(off, "delete")
	}
	if len(off) == 0 {
		return strings.Join(on, "+")
	}
	return strings.Join(on, "+") + " (" + strings.Join(off, ",") + " off)"
}

// LocalConfig is the workspace-local source of truth for gsync
// settings. Persists to <localStoreDir>/config.yaml.
type LocalConfig struct {
	// Target is the canonical destination spec ("local:~/Dropbox/work" or
	// "ssh:user@host:path"). When empty, MirrorPath (legacy) applies.
	Target string `yaml:"target,omitempty"`
	// Owner is the hostname allowed to push this profile. A mirror with two
	// pushers corrupts: both carry different baselines and delete propagation
	// is on, so each run undoes the other. Empty means unrestricted (the
	// pre-existing behavior, kept so upgrades are not gated on setting it).
	Owner string `yaml:"owner,omitempty"`
	// OwnerAliases are earlier names of the owner, recorded by
	// `dot sync owner --rename`. This machine is the owner when it answers to
	// any of them, and two profiles name the same owner when their names or
	// aliases meet, so a Mac rename never splits the pair mid-transition
	// (#185). Any deliberate owner change clears them.
	OwnerAliases []string `yaml:"owner_aliases,omitempty"`
	// OwnerEpoch orders coordinator transitions (peer profile). Every
	// deliberate owner change bumps it; on first contact after a switch the
	// higher epoch wins and the lower demotes itself. Zero means the profile
	// predates epochs and the fence stays in the pre-epoch refusal mode.
	OwnerEpoch int `yaml:"owner_epoch,omitempty"`
	// FencePending marks a takeover that has not yet met the old coordinator.
	// While set, PeerSchedule and PeerSync skip the reachability and
	// remote-owner requirements (an unreachable run still exits 0); the first
	// complete run after contact clears it.
	FencePending bool `yaml:"fence_pending,omitempty"`
	// IncludeSubmodules carries submodule working trees in the payload.
	// The cloud mirror deliberately leaves them out - they round-trip through
	// Git, and mirroring them would duplicate gigabytes. A peer profile wants
	// the opposite: "either machine can continue the same work" includes the
	// uncommitted state inside submodules, which Git by definition has not seen.
	IncludeSubmodules bool              `yaml:"include_submodules,omitempty"`
	MirrorPath        string            `yaml:"mirror_path,omitempty"`
	FilterMode        FilterMode        `yaml:"filter_mode,omitempty"`
	Propagation       PropagationPolicy `yaml:"propagation"`
	MaxDelete         int               `yaml:"max_delete,omitempty"`
	Interval          int               `yaml:"interval,omitempty"`      // push scheduler cadence (seconds)
	PullInterval      int               `yaml:"pull_interval,omitempty"` // pull scheduler cadence (0 = off)
	PushMode          RunMode           `yaml:"push_mode,omitempty"`     // automatic push mode (clean|force)
	PullMode          RunMode           `yaml:"pull_mode,omitempty"`     // automatic pull mode (clean|force)
	Paused            bool              `yaml:"paused,omitempty"`
	SharedExcludes    []string          `yaml:"shared_excludes,omitempty"`
	// HostMerge maps a host path (relative to $HOME) to the top-level JSON
	// keys whose entries a peer run merges from both copies before the
	// additive pass, so neither Mac's entries are lost (#181).
	HostMerge map[string][]string `yaml:"host_merge,omitempty"`
	// Hooks run around this machine's peer coordinator role (#184).
	Hooks PeerHooks `yaml:"hooks,omitempty"`
	// RemoteDot pins the dot binary a peer run uses on the other machine
	// ("~/" is the peer's home). Unset, the newest release found there wins;
	// set it to use a dev build (#176).
	RemoteDot string `yaml:"remote_dot,omitempty"`
}

// LocalState holds non-config runtime telemetry — the sticky timestamps
// that change every pull/push/intake. Splitting them from LocalConfig keeps
// the editable file (config.yaml) churn-free.
type LocalState struct {
	LastPull        time.Time `yaml:"last_pull,omitempty"`
	LastPush        time.Time `yaml:"last_push,omitempty"`
	LastIntake      time.Time `yaml:"last_intake,omitempty"`
	LastIntakeTSDir string    `yaml:"last_intake_ts_dir,omitempty"`
	// LastHeld marks the most recent peer run that finished with destructive
	// transitions held back. It is cleared by the next clean exchange, so
	// `dot peer status` can tell a held run from a complete one instead of
	// reading both as a successful sync.
	LastHeld time.Time `yaml:"last_held,omitempty"`
	// LastPushAttempt is the latest real push run. LastPushError holds why it
	// did not complete (a refusal or a failure) and LastPushErrorSince when the
	// failing streak began; a completed push clears both, so status can tell a
	// scheduled push that keeps refusing from a quiet workspace (#224).
	LastPushAttempt    time.Time `yaml:"last_push_attempt,omitempty"`
	LastPushError      string    `yaml:"last_push_error,omitempty"`
	LastPushErrorSince time.Time `yaml:"last_push_error_since,omitempty"`
}

// LocalPaths resolves every well-known path under
// <localPath>/.dotfiles/sync/. Constructed via ResolveLocalPaths
// so callers don't string-glue these by hand.
type LocalPaths struct {
	StoreDir          string
	ConfigFile        string
	StateFile         string
	IncludeFile       string
	ExcludeFile       string
	IgnoreFile        string
	AllowFile         string
	SharedDynFile     string
	SubmodulesDynFile string
	TrackedDynFile    string
	BaselineFile      string
	ImportsFile       string
	TombstonesFile    string
	WorktreesFile     string // sticky linked-worktree exclude list (peer profile)
	LogDir            string
	LogFile           string
	WorkspaceRoot     string // the local sync tree itself (parent of .dotfiles)
	WorkspaceIgnore   string // <local>/.gitignore (workspace-level)
}

// ResolveLocalPaths returns the canonical layout for a given local
// sync tree. localPath should be the absolute path to the workspace
// root (with or without trailing slash).
//
// When the workspace still carries only the pre-rename store
// (.dotfiles/gdrive-sync/) the layout points there, so read-only commands
// stay truthful before MigrateLegacyStore has run. Once .dotfiles/sync/
// exists it always wins.
func ResolveLocalPaths(localPath string) *LocalPaths {
	return ResolveLocalPathsForProfile(localPath, DefaultProfile)
}

// DefaultProfile is the store name for the cloud-mirror sync. A profile is
// just a store directory name under <workspace>/.dotfiles/, so each profile
// carries its own target, filter files, baseline, state, lock and scheduler
// unit. That separation is what lets a peer profile opt secrets IN via its own
// allow.txt without touching the mirror profile, which must keep them out.
//
// Non-default profiles are machine-local for free: the managed .gitignore block
// ignores /.dotfiles/* and whitelists only sync/, so a peer baseline never
// enters git. Two machines writing one shared baseline would otherwise produce
// merge conflicts in the very file that coordinates them.
const (
	DefaultProfile = "sync"
	PeerProfile    = "peer"
)

// NormalizeProfile trims and defaults a profile name.
func NormalizeProfile(profile string) string {
	p := strings.TrimSpace(profile)
	if p == "" {
		return DefaultProfile
	}
	return p
}

// ValidateProfile accepts one normalized ASCII-safe profile token. Profiles
// become path segments, scheduler labels, unit names, and command arguments,
// so the boundary must reject every target-specific metacharacter before any
// of those sinks are constructed.
func ValidateProfile(profile string) error {
	p := NormalizeProfile(profile)
	if !isASCIILetterOrDigit(p[0]) {
		return invalidProfileError(profile)
	}
	for i := 1; i < len(p); i++ {
		if !isASCIILetterOrDigit(p[i]) && p[i] != '.' && p[i] != '_' && p[i] != '-' {
			return invalidProfileError(profile)
		}
	}
	return nil
}

func isASCIILetterOrDigit(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

func invalidProfileError(profile string) error {
	return fmt.Errorf("invalid sync profile %q: must be an ASCII token beginning with a letter or digit and containing only letters, digits, dot, underscore, or hyphen", profile)
}

// ResolveLocalPathsForProfile returns the layout for one profile's store.
// The legacy gdrive-sync fallback applies only to the default profile: it
// exists to keep pre-rename workspaces truthful, and a new profile has no
// legacy twin to fall back to.
func ResolveLocalPathsForProfile(localPath, profile string) *LocalPaths {
	return resolveLocalPaths(localPath, profile, true)
}

// ResolveLocalPathsPostMigration is ResolveLocalPathsForProfile WITHOUT the
// pre-rename fallback: the layout a MUTATING command operates on, because such
// a command runs MigrateLegacyStore before it touches anything.
//
// The fallback above must stay for read-only callers - it is what keeps
// `dot sync status` truthful on a workspace that has not migrated yet - so this
// is a second view, not a replacement. Use it to PREVIEW a mutating command,
// and pair it with pendingLegacyStore so the preview names the rename too.
func ResolveLocalPathsPostMigration(localPath, profile string) *LocalPaths {
	return resolveLocalPaths(localPath, profile, false)
}

// pendingLegacyStore returns the pre-rename store MigrateLegacyStore would
// rename, or "" when there is nothing to migrate. MigrateLegacyStore itself
// asks the same function, so a preview and the run it previews cannot disagree
// about whether a rename is coming.
func pendingLegacyStore(localPath string) string {
	root := strings.TrimRight(localPath, "/")
	old := filepath.Join(root, legacyStoreDirRel)
	if pathExists(filepath.Join(root, localStoreDirRel)) || !pathExists(old) {
		return ""
	}
	return old
}

func resolveLocalPaths(localPath, profile string, legacyFallback bool) *LocalPaths {
	root := strings.TrimRight(localPath, "/")
	prof := NormalizeProfile(profile)
	storeRel := filepath.Join(".dotfiles", prof)
	if prof == DefaultProfile {
		storeRel = localStoreDirRel
		if legacyFallback && pendingLegacyStore(root) != "" {
			storeRel = legacyStoreDirRel
		}
	}
	store := filepath.Join(root, storeRel)
	logDir := filepath.Join(store, localLogDirRel)
	logFile := filepath.Join(logDir, localLogFileName)
	if storeRel == legacyStoreDirRel && !pathExists(logFile) {
		if legacy := filepath.Join(logDir, legacyLogFileName); pathExists(legacy) {
			logFile = legacy
		}
	}
	return &LocalPaths{
		StoreDir:          store,
		ConfigFile:        filepath.Join(store, localConfigName),
		StateFile:         filepath.Join(store, localStateName),
		IncludeFile:       filepath.Join(store, localIncludeName),
		ExcludeFile:       filepath.Join(store, localExcludeName),
		IgnoreFile:        filepath.Join(store, localIgnoreName),
		AllowFile:         filepath.Join(store, localAllowName),
		SharedDynFile:     filepath.Join(store, localSharedDynName),
		SubmodulesDynFile: filepath.Join(store, localSubmodulesDynName),
		TrackedDynFile:    filepath.Join(store, localTrackedDynName),
		BaselineFile:      filepath.Join(store, localBaselineName),
		ImportsFile:       filepath.Join(store, localImportsName),
		TombstonesFile:    filepath.Join(store, localTombstonesName),
		WorktreesFile:     filepath.Join(store, localWorktreesName),
		LogDir:            logDir,
		LogFile:           logFile,
		WorkspaceRoot:     root,
		WorkspaceIgnore:   filepath.Join(root, ".gitignore"),
	}
}

// MigrateLegacyStore renames <root>/.dotfiles/gdrive-sync/ to
// <root>/.dotfiles/sync/ and rewrites any gdrive-sync references in the
// workspace .gitignore (managed block plus operator-added whitelist lines).
// Returns true when a migration happened. No-op when the new store already
// exists or no legacy store is present.
func MigrateLegacyStore(localPath string) (bool, error) {
	root := strings.TrimRight(localPath, "/")
	newDir := filepath.Join(root, localStoreDirRel)
	oldDir := pendingLegacyStore(root)
	if oldDir == "" {
		return false, nil
	}
	if err := os.Rename(oldDir, newDir); err != nil {
		return false, fmt.Errorf("renaming %s -> %s: %w", oldDir, newDir, err)
	}
	// Keep log continuity under the new file name.
	oldLog := filepath.Join(newDir, localLogDirRel, legacyLogFileName)
	newLog := filepath.Join(newDir, localLogDirRel, localLogFileName)
	if pathExists(oldLog) && !pathExists(newLog) {
		_ = os.Rename(oldLog, newLog)
	}
	// Rewrite .gitignore lines in place so operator-added whitelist entries
	// (e.g. !/.dotfiles/gdrive-sync/exclude.txt) survive the rename.
	gitignorePath := filepath.Join(root, ".gitignore")
	if body, err := os.ReadFile(gitignorePath); err == nil &&
		strings.Contains(string(body), legacyStoreDirRel) {
		updated := strings.ReplaceAll(string(body), legacyStoreDirRel, localStoreDirRel)
		if err := os.WriteFile(gitignorePath, []byte(updated), 0644); err != nil {
			return true, fmt.Errorf("updating %s: %w", gitignorePath, err)
		}
	}
	return true, nil
}

// EnsureLocalLayout creates the .dotfiles/sync/ directory plus
// all default files (empty manifests, header-only ignore.txt, embedded
// include/exclude copies). Idempotent — existing files are left untouched.
//
// Does NOT load or write config.yaml — that's the migration path's job.
func EnsureLocalLayout(paths *LocalPaths) error {
	for _, dir := range []string{paths.StoreDir, paths.LogDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("creating %s: %w", dir, err)
		}
	}
	if err := materializeExcludeIfMissing(paths.ExcludeFile); err != nil {
		return err
	}
	if err := materializeIncludeIfMissing(paths.IncludeFile); err != nil {
		return err
	}
	for _, ent := range []struct {
		path   string
		header string
	}{
		{paths.IgnoreFile, "# User-supplied ignore patterns for `dot sync`.\n# One pattern per line; same syntax as exclude.txt. Layered after exclude.txt.\n"},
		{paths.AllowFile, allowFileHeader},
		{paths.BaselineFile, "# Auto-generated by `dot sync push` — do not edit.\n# relpath\\tsize\\tmtime-rfc3339\\tsha256-or-dash\n"},
		{paths.ImportsFile, "# Auto-generated by `dot sync intake` — clear via `dot sync inbox clear`.\n# relpath\\tsize\\tmtime-rfc3339\\tsha256-or-dash\\timported-rfc3339\n"},
		{paths.TombstonesFile, "# Auto-generated by `dot sync intake` — mirror deletions detected.\n# relpath\\tbaseline-fingerprint\\tdetected-rfc3339\n"},
	} {
		if err := writeIfMissing(ent.path, []byte(ent.header)); err != nil {
			return err
		}
	}
	return appendGitignoreBlock(paths.WorkspaceIgnore, gitignoreEntries)
}

// materializeExcludeIfMissing copies the embedded excludes baseline to
// disk so the operator can edit it. Existing on-disk content wins.
func materializeExcludeIfMissing(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	engine := template.NewEngine()
	body, err := engine.ReadStatic(excludesTemplatePath)
	if err != nil {
		return fmt.Errorf("reading embedded excludes: %w", err)
	}
	if err := os.WriteFile(path, body, 0644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// materializeIncludeIfMissing copies the embedded includes baseline to
// disk so the operator can edit it. Existing on-disk content wins.
func materializeIncludeIfMissing(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	engine := template.NewEngine()
	body, err := engine.ReadStatic(includesTemplatePath)
	if err != nil {
		return fmt.Errorf("reading embedded includes: %w", err)
	}
	if err := os.WriteFile(path, body, 0644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// writeIfMissing writes data to path only if path doesn't already exist.
func writeIfMissing(path string, data []byte) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// appendGitignoreBlock appends missing `lines` to the workspace .gitignore.
// Creates the file if missing. Idempotent — repeated calls are no-ops once
// every line is present. The order matters because the block can re-include
// baseline.manifest after an older broad "/.dotfiles/" ignore.
func appendGitignoreBlock(gitignorePath string, lines []string) error {
	body, err := os.ReadFile(gitignorePath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading %s: %w", gitignorePath, err)
	}

	existing := map[string]bool{}
	for _, line := range strings.Split(string(body), "\n") {
		existing[strings.TrimSpace(line)] = true
	}

	out := append([]byte{}, body...)
	if len(out) > 0 && !strings.HasSuffix(string(out), "\n") {
		out = append(out, '\n')
	}
	wrote := false
	for _, line := range lines {
		want := strings.TrimSpace(line)
		if want == "" || existing[want] {
			continue
		}
		out = append(out, []byte(want+"\n")...)
		wrote = true
	}
	if !wrote {
		return nil
	}
	if err := os.WriteFile(gitignorePath, out, 0644); err != nil {
		return fmt.Errorf("writing %s: %w", gitignorePath, err)
	}
	return nil
}

// LoadLocalConfig reads <localStoreDir>/config.yaml. Returns
// (config, true, nil) when the file exists and parses, (nil, false, nil)
// when missing (caller should migrate or use defaults), or an error on
// parse failure.
func LoadLocalConfig(paths *LocalPaths) (*LocalConfig, bool, error) {
	data, err := os.ReadFile(paths.ConfigFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("reading %s: %w", paths.ConfigFile, err)
	}
	var cfg LocalConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, false, fmt.Errorf("parsing %s: %w", paths.ConfigFile, err)
	}
	if err := cfg.Propagation.Validate(); err != nil {
		// Fall back to defaults when on-disk policy is unusable rather than blocking.
		fmt.Fprintf(os.Stderr, "warning: %s has invalid propagation (%v); using defaults\n", paths.ConfigFile, err)
		cfg.Propagation = DefaultPropagationPolicy()
	}
	if cfg.FilterMode == "" {
		cfg.FilterMode = DefaultFilterMode()
	} else if err := cfg.FilterMode.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %s has invalid filter_mode (%v); using %s\n", paths.ConfigFile, err, DefaultFilterMode())
		cfg.FilterMode = DefaultFilterMode()
	} else {
		cfg.FilterMode = normalizeFilterMode(cfg.FilterMode)
	}
	schedule := ScheduleSettingsFromLocalConfig(&cfg).NormalizeLenient(func(field string, err error, fallback any) {
		fmt.Fprintf(os.Stderr, "warning: %s has invalid %s (%v); using %v\n", paths.ConfigFile, field, err, fallback)
	})
	schedule.ApplyToLocalConfig(&cfg)
	return &cfg, true, nil
}

// SaveLocalConfig writes config atomically to <localStoreDir>/config.yaml.
func SaveLocalConfig(paths *LocalPaths, cfg *LocalConfig) error {
	if err := cfg.Propagation.Validate(); err != nil {
		return fmt.Errorf("refusing to save invalid propagation policy: %w", err)
	}
	if cfg.FilterMode == "" {
		cfg.FilterMode = DefaultFilterMode()
	}
	if err := cfg.FilterMode.Validate(); err != nil {
		return fmt.Errorf("refusing to save invalid filter_mode: %w", err)
	}
	cfg.FilterMode = normalizeFilterMode(cfg.FilterMode)
	schedule, err := ScheduleSettingsFromLocalConfig(cfg).Normalize()
	if err != nil {
		return fmt.Errorf("refusing to save invalid schedule config: %w", err)
	}
	schedule.ApplyToLocalConfig(cfg)
	if err := os.MkdirAll(paths.StoreDir, 0755); err != nil {
		return fmt.Errorf("creating %s: %w", paths.StoreDir, err)
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshaling local config: %w", err)
	}
	if data, err = keepUnknownConfigKeys(paths.ConfigFile, data); err != nil {
		return err
	}
	return atomicWrite(paths.ConfigFile, data)
}

// keepUnknownConfigKeys carries over the top-level keys of the config on
// disk that LocalConfig does not own, so a dot older than the key (a
// handover's adopt on the peer, a stale binary) no longer drops it on
// save (#196). A key LocalConfig owns is the struct's, cleared or not; a
// key nested under one is not carried. An unreadable file on disk keeps
// nothing: loading it already failed. Carried values are copied with their
// aliases resolved (an anchor on a key the struct rewrote is gone), and
// the result must load as a config, or the save refuses.
//
// ponytail: known ceiling. See docs/CEILINGS.md (unknown config keys, top level only).
func keepUnknownConfigKeys(path string, data []byte) ([]byte, error) {
	old, err := os.ReadFile(path)
	if err != nil {
		return data, nil
	}
	var was, now yaml.Node
	if yaml.Unmarshal(old, &was) != nil || len(was.Content) == 0 || was.Content[0].Kind != yaml.MappingNode {
		return data, nil
	}
	if err := yaml.Unmarshal(data, &now); err != nil || len(now.Content) == 0 || now.Content[0].Kind != yaml.MappingNode {
		return data, err
	}
	known := localConfigKeys()
	kept := false
	for i := 0; i+1 < len(was.Content[0].Content); i += 2 {
		// The key is judged as the decoder reads it (an alias key names the
		// key it points at). A merge key (<<) is not a key of its own: what
		// it sets for the struct was loaded and is written, and carrying it
		// would bring a cleared key back on the next load.
		key, err := resolveYAMLAliases(was.Content[0].Content[i])
		if err != nil {
			return nil, fmt.Errorf("refusing to save %s: %w", path, err)
		}
		if known[key.Value] || key.ShortTag() == "!!merge" {
			continue
		}
		value, err := resolveYAMLAliases(was.Content[0].Content[i+1])
		if err != nil {
			return nil, fmt.Errorf("refusing to save %s: its key %q: %w", path, key.Value, err)
		}
		now.Content[0].Content = append(now.Content[0].Content, key, value)
		kept = true
	}
	if !kept {
		return data, nil
	}
	out, err := yaml.Marshal(&now)
	if err != nil {
		return nil, fmt.Errorf("marshaling local config: %w", err)
	}
	var check LocalConfig
	if err := yaml.Unmarshal(out, &check); err != nil {
		return nil, fmt.Errorf("refusing to save %s: keeping its unknown keys would not load again: %w", path, err)
	}
	return out, nil
}

// resolveYAMLAliases copies n with every alias replaced by a copy of what
// it names, anchors dropped, so the copy stands alone in another document.
// An alias inside what it names, or an expansion past maxResolvedYAMLNodes,
// is an error: the decoder never read these values, so nothing guarded them.
//
// ponytail: fixed node budget; raise it if a real config's kept key needs more.
func resolveYAMLAliases(n *yaml.Node) (*yaml.Node, error) {
	budget := maxResolvedYAMLNodes
	return resolveYAMLAliasesIn(n, map[*yaml.Node]bool{}, &budget)
}

const maxResolvedYAMLNodes = 10000

func resolveYAMLAliasesIn(n *yaml.Node, open map[*yaml.Node]bool, budget *int) (*yaml.Node, error) {
	for n.Kind == yaml.AliasNode && n.Alias != nil {
		n = n.Alias
	}
	if open[n] {
		return nil, fmt.Errorf("anchor %q contains an alias to itself", n.Anchor)
	}
	if *budget--; *budget < 0 {
		return nil, fmt.Errorf("its aliases expand past %d nodes", maxResolvedYAMLNodes)
	}
	open[n] = true
	defer delete(open, n)
	c := *n
	c.Anchor = ""
	c.Content = make([]*yaml.Node, len(n.Content))
	for i, child := range n.Content {
		var err error
		if c.Content[i], err = resolveYAMLAliasesIn(child, open, budget); err != nil {
			return nil, err
		}
	}
	return &c, nil
}

// localConfigKeys are the top-level keys LocalConfig owns.
func localConfigKeys() map[string]bool {
	keys := map[string]bool{}
	t := reflect.TypeOf(LocalConfig{})
	for i := 0; i < t.NumField(); i++ {
		if name, _, _ := strings.Cut(t.Field(i).Tag.Get("yaml"), ","); name != "" && name != "-" {
			keys[name] = true
		}
	}
	return keys
}

// LoadLocalState reads state.yaml or returns a zero-value if missing.
func LoadLocalState(paths *LocalPaths) (*LocalState, error) {
	data, err := os.ReadFile(paths.StateFile)
	if err != nil {
		if os.IsNotExist(err) {
			return &LocalState{}, nil
		}
		return nil, fmt.Errorf("reading %s: %w", paths.StateFile, err)
	}
	var st LocalState
	if err := yaml.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", paths.StateFile, err)
	}
	return &st, nil
}

// SaveLocalState writes state.yaml atomically.
func SaveLocalState(paths *LocalPaths, st *LocalState) error {
	if err := os.MkdirAll(paths.StoreDir, 0755); err != nil {
		return fmt.Errorf("creating %s: %w", paths.StoreDir, err)
	}
	data, err := yaml.Marshal(st)
	if err != nil {
		return fmt.Errorf("marshaling local state: %w", err)
	}
	return atomicWrite(paths.StateFile, data)
}

// UpdateLocalState loads state.yaml, applies mutate, and saves it back.
// Mutate runs against a non-nil pointer; the caller doesn't need to
// guard for missing files. Used by intake/push to bump timestamps
// without each call site repeating Load → mutate → Save.
func UpdateLocalState(paths *LocalPaths, mutate func(*LocalState)) error {
	st, err := LoadLocalState(paths)
	if err != nil {
		return err
	}
	mutate(st)
	return SaveLocalState(paths, st)
}

func localConfigFromGlobal(globalState *config.UserState) *LocalConfig {
	gs := globalState.Modules.Gsync
	return &LocalConfig{
		MirrorPath:     gs.MirrorPath,
		FilterMode:     DefaultFilterMode(),
		Propagation:    DefaultPropagationPolicy(),
		MaxDelete:      gs.MaxDelete,
		Interval:       0,
		PullInterval:   0,
		PushMode:       ModeClean,
		PullMode:       ModeClean,
		Paused:         gs.Paused,
		SharedExcludes: append([]string(nil), gs.SharedExcludes...),
	}
}

// MigrateFromGlobal seeds <localStoreDir>/config.yaml from the legacy
// `state.modules.gdrive_sync` block when no local config exists yet.
// Returns the freshly-built LocalConfig (also persisted to disk).
//
// Subsequent calls find the local config and skip migration entirely —
// the global block is read at most once per workspace lifetime.
func MigrateFromGlobal(globalState *config.UserState, paths *LocalPaths) (*LocalConfig, error) {
	gs := globalState.Modules.Gsync
	cfg := localConfigFromGlobal(globalState)
	if err := EnsureLocalLayout(paths); err != nil {
		return nil, err
	}
	if err := SaveLocalConfig(paths, cfg); err != nil {
		return nil, err
	}
	// Seed state.yaml with the legacy timestamps so `status` keeps continuity.
	st := &LocalState{
		LastPull:   gs.LastPull,
		LastPush:   gs.LastPush,
		LastIntake: gs.LastPull, // legacy `Pull` retired into `intake` semantics
	}
	if err := SaveLocalState(paths, st); err != nil {
		return nil, err
	}
	return cfg, nil
}

// LoadOrMigrateLocalConfig is the single entry point ResolveConfig uses:
// returns the local config, performing a one-time migration from the
// global state if no local config exists yet.
func LoadOrMigrateLocalConfig(globalState *config.UserState, paths *LocalPaths) (*LocalConfig, error) {
	if cfg, ok, err := LoadLocalConfig(paths); err != nil {
		return nil, err
	} else if ok {
		// Even when config.yaml exists, ensure the surrounding layout
		// (manifests, ignore.txt, gitignore block) is intact — operators
		// can delete these and we should heal silently.
		if err := EnsureLocalLayout(paths); err != nil {
			return nil, err
		}
		return cfg, nil
	}
	return MigrateFromGlobal(globalState, paths)
}

// atomicWrite mirrors saveStateAt's marshal → temp → fsync → rename
// pattern from internal/config/state.go. Same filesystem guarantees,
// no torn writes.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("writing temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("syncing temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("closing temp: %w", err)
	}
	if err := os.Chmod(tmpPath, 0644); err != nil {
		cleanup()
		return fmt.Errorf("chmod temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}
