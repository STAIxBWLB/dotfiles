package config

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/sliceutil"
	"github.com/STAIxBWLB/dotfiles/internal/tunnel"
	"gopkg.in/yaml.v3"
)

// currentSchemaVersion is the version of the STATE SCHEMA this binary writes.
// It describes the on-disk shape of UserState, not the binary's own version:
// a release that changes no state field leaves this number alone (D-02).
//
// It is a single monotonically increasing integer. There is no range parsing
// and no per-field versioning; a reader compares two integers, which is the
// smallest thing that satisfies DEBT-02's error message. Version 0 is the
// sentinel for a file written before the field existed, which is a normal
// state and not an error.
// Version 3 adds portable AI policy; older writers must not drop it.
// Version 4 adds modules.container, for the same reason.
const currentSchemaVersion = 4

// peekSchemaVersion recovers the top-level schema_version from raw state bytes
// with a second decode into a one-field struct.
//
// The second pass is not redundant with the SchemaVersion field. The field is
// populated by the full decode, so when that decode fails the field is never
// set; the peek reads the version out of a document the full decode rejects,
// which is what lets a caller say "this file came from a newer dot" instead of
// surfacing a bare yaml type error.
//
// It SWALLOWS its own error and returns 0. Measured across five malformed
// inputs (07-RESEARCH.md Q3b: a sequence, a scalar, an empty document, broken
// syntax, and a non-integer version) the peek's own error is always worse than
// the decode error that follows it -- "cannot unmarshal !!seq into struct
// { SchemaVersion int }" tells a user nothing. Returning 0 lets the real error
// surface. Do not "improve" this into propagating.
func peekSchemaVersion(raw []byte) int {
	var probe struct {
		SchemaVersion int `yaml:"schema_version"`
	}
	if err := yaml.Unmarshal(raw, &probe); err != nil {
		return 0
	}
	return probe.SchemaVersion
}

// applyLegacyKeys overlays the legacy state keys onto an already-decoded
// state, reading them from the raw bytes in a second pass -- the same
// mechanism peekSchemaVersion uses, and the reason DEBT-01 strictly precedes
// DEBT-03.
//
// It exists because the UnmarshalYAML shims that used to read these keys are
// gone. DEBT-03 asks for them to be migrated AND deleted, and a shim cannot be
// both: read the keys here instead and the conversion stops depending on them.
// yaml.v3 drops unknown keys with a nil error, so without this pass deleting
// the shims would silently discard the values.
//
// The overlay runs only on a document the full decode already accepted, so its
// own decode error means nothing new and is swallowed like the peek's.
//
// It converts in memory and writes nothing. Every ordinary SaveState* then
// emits the canonical form on its own, because Warp is tagged `yaml:"-"` and
// every other destination is a serialized field -- seven write commands
// migrate implicitly and none of them needs to know. No read path may persist
// this (D-05, D-23): dot check, dot diff and internal/profilesnap all reach
// here, and profilesnap reads other homes' files merely to inspect them.
func applyLegacyKeys(state *UserState, raw []byte) {
	var legacy struct {
		Modules struct {
			AITools      bool `yaml:"ai_tools"`
			Warp         bool `yaml:"warp"`
			TerminalApps struct {
				Casks []string `yaml:"casks"`
			} `yaml:"terminal_apps"`
		} `yaml:"modules"`
	}
	if err := yaml.Unmarshal(raw, &legacy); err != nil {
		return
	}

	if legacy.Modules.AITools {
		state.carriedLegacy = append(state.carriedLegacy, "modules.ai_tools")
		if !state.Modules.AI.Enabled {
			state.Modules.AI.Enabled = true
		}
	}

	// Casks before warp, matching the old shim order: the terminal_apps shim
	// ran during the decode, so a file carrying both casks and warp had its
	// apps already populated by the time the warp branch tested them.
	//
	// A nil Apps slice means the canonical key was absent; an explicit
	// `apps: []` decodes to a non-nil zero-length slice (measured). That is
	// what keeps an explicit empty list winning over a populated casks list.
	if len(legacy.Modules.TerminalApps.Casks) > 0 {
		state.carriedLegacy = append(state.carriedLegacy, "modules.terminal_apps.casks")
		if state.Modules.TerminalApps.Apps == nil {
			state.Modules.TerminalApps.Apps = append([]string(nil), legacy.Modules.TerminalApps.Casks...)
		}
	}

	if legacy.Modules.Warp {
		state.carriedLegacy = append(state.carriedLegacy, "modules.warp")
		state.Modules.Warp = true
		if !state.Modules.TerminalApps.Enabled && len(state.Modules.TerminalApps.Apps) == 0 {
			state.Modules.TerminalApps.Enabled = true
			state.Modules.TerminalApps.Apps = []string{"warp"}
		}
	}
}

// warnForwardSchema reports that a state file was written by a newer dot. It
// does not fail the load: an additively-forward file still decodes, and an
// incompatibly-forward one gets this warning before its decode error so the
// user is pointed at the upgrade rather than at a yaml type error.
//
// It fires once per load, including from internal/profilesnap's walk over
// other homes' snapshots, so a fleet mid-upgrade would print one line per
// forward-version snapshot during `dot profile backup`. Accepted rather than
// adding a quiet variant: by D-01's reasoning no file in this release can
// carry a version above this binary's, so the case is unreachable in v1.0. A
// quiet variant is the one-line upgrade path if v1.1 makes it real.
func warnForwardSchema(path string, version int) {
	fmt.Fprintf(os.Stderr, "warning: %s was written by a newer dot (state schema version %d, this binary understands %d)\n", path, version, currentSchemaVersion)
	fmt.Fprintln(os.Stderr, "  Run 'dot update' to upgrade.")
}

// UserState holds user-configured settings persisted to disk.
type UserState struct {
	SchemaVersion int               `yaml:"schema_version"`
	Name          string            `yaml:"name"`
	Email         string            `yaml:"email"`
	GithubUser    string            `yaml:"github_user"`
	Timezone      string            `yaml:"timezone"`
	Profile       string            `yaml:"profile"`
	Modules       UserModulesState  `yaml:"modules,omitempty"`
	SSH           UserSSHState      `yaml:"ssh,omitempty"`
	Secrets       SecretsUserConfig `yaml:"secrets,omitempty"`

	// carriedLegacy names the legacy keys the file this state was read from
	// still used, in the order applyLegacyKeys applies them. It is unexported,
	// so yaml.v3 cannot see it: the flag can never be written to disk and
	// needs no tag. saveStateAt reads it to print one note per converted write.
	carriedLegacy []string
}

// UserModulesState holds module opt-in/config from user state.
type UserModulesState struct {
	Workspace    UserWorkspaceState    `yaml:"workspace,omitempty"`
	AI           UserAIState           `yaml:"ai,omitempty"`
	Git          UserGitState          `yaml:"git,omitempty"`
	Warp         bool                  `yaml:"-"`
	PromptStyle  string                `yaml:"prompt_style,omitempty"` // "minimal" or "rich"
	Editor       string                `yaml:"editor,omitempty"`       // "zed", "code", or "vi"
	TerminalApps UserTerminalAppsState `yaml:"terminal_apps,omitempty"`
	Fonts        UserFontsState        `yaml:"fonts,omitempty"`
	Rsync        UserRsyncState        `yaml:"rsync,omitempty"`
	Gsync        UserGsyncState        `yaml:"gdrive_sync,omitempty"`
	MacApps      UserMacAppsState      `yaml:"macapps,omitempty"`
	Tunnel       UserTunnelState       `yaml:"tunnel,omitempty"`
	Guard        UserGuardState        `yaml:"guard,omitempty"`
	Container    UserContainerState    `yaml:"container,omitempty"`
}

// UserContainerState holds the opt-in and settings for the container module.
type UserContainerState struct {
	Enabled bool     `yaml:"enabled,omitempty"`
	Backend string   `yaml:"backend,omitempty"`
	DNS     []string `yaml:"dns,omitempty"`
}

// IsZero lets yaml.v3 omit an unset container block from user state.
func (s UserContainerState) IsZero() bool {
	return !s.Enabled && s.Backend == "" && len(s.DNS) == 0
}

// UserAIState holds user selections for AI CLI/config helpers.
type UserAIState struct {
	Policy     *AIPolicyConfig  `yaml:"policy,omitempty" json:"policy,omitempty"`
	Tooling    *AIToolingConfig `yaml:"tooling,omitempty"`
	Enabled    bool             `yaml:"enabled,omitempty"`
	AgentsSSOT bool             `yaml:"agents_ssot,omitempty"`
	HUD        bool             `yaml:"hud,omitempty"`
	Skills     AISkillsConfig   `yaml:"skills,omitempty"`
}

// IsZero lets yaml.v3 omit an unset AI block from user state.
func (a UserAIState) IsZero() bool {
	return !a.Enabled && !a.AgentsSSOT && !a.HUD && a.Skills.IsZero() && a.Tooling == nil && a.Policy == nil
}

// UserGitState holds user selections for git helper behavior.
type UserGitState struct {
	CoauthorGuard string `yaml:"coauthor_guard,omitempty"`
}

// UnmarshalYAML accepts either:
//
//	modules:
//	  ai:
//	    enabled: true
//
// or the shorthand `ai: true`.
func (a *UserAIState) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		var enabled bool
		if err := value.Decode(&enabled); err != nil {
			return err
		}
		a.Enabled = enabled
		return nil
	}
	type raw UserAIState
	return value.Decode((*raw)(a))
}

// UserTerminalAppsState holds cross-platform GUI terminal app selections.
type UserTerminalAppsState struct {
	Enabled bool     `yaml:"enabled,omitempty"`
	Apps    []string `yaml:"apps,omitempty"`
}

// IsZero lets yaml.v3 omit an unset terminal_apps block from user state.
func (s UserTerminalAppsState) IsZero() bool {
	return !s.Enabled && len(s.Apps) == 0
}

// UserMacAppsState holds user selections for the macapps module.
//
// Install vs. backup are separated: Casks/CasksExtra drive `dot apps install`,
// while BackupApps scopes `dot apps backup/restore`. BackupRoot is shared
// with `dot profile backup/restore` so a single folder (typically a Drive
// secrets dir) holds both app-settings snapshots and profile snapshots.
type UserMacAppsState struct {
	Enabled    bool     `yaml:"enabled,omitempty"`
	Casks      []string `yaml:"casks,omitempty"`       // install list (catalog selections)
	CasksExtra []string `yaml:"casks_extra,omitempty"` // install list (free-form additions)
	BackupApps []string `yaml:"backup_apps,omitempty"` // backup/restore scope (empty = manifest ∩ installed)
	BackupRoot string   `yaml:"backup_root,omitempty"` // single backup root; app-settings/ + profiles/ live below it

	LastBackup *BackupRecord `yaml:"last_backup,omitempty"`
}

// UserRsyncState holds rsync sync config from user state.
type UserRsyncState struct {
	RemoteHost string `yaml:"remote_host,omitempty"` // user@host (SSH)
	RemotePath string `yaml:"remote_path,omitempty"` // remote workspace path
	Interval   int    `yaml:"interval,omitempty"`    // sync interval in seconds, default 300
}

// UserGsyncState holds local↔local rsync mirror config from user state.
//
// `dot gsync` keeps ~/workspace/work and ~/gdrive-workspace/work in sync
// via local rsync (no SSH). Workspace is authoritative: pull uses --update only,
// push uses --delete-after. Last* timestamps are advisory (status display).
//
// Paused defaults to true on a fresh state so the user explicitly opts in by
// flipping it to false via `resume`.
type UserGsyncState struct {
	LocalPath      string    `yaml:"local_path,omitempty"`  // primary tree, default ~/workspace/work
	MirrorPath     string    `yaml:"mirror_path,omitempty"` // mirror tree, default ~/gdrive-workspace/work
	LastPull       time.Time `yaml:"last_pull,omitempty"`
	LastPush       time.Time `yaml:"last_push,omitempty"`
	ConflictDir    string    `yaml:"conflict_dir,omitempty"`    // default <local>/.sync-conflicts
	Paused         bool      `yaml:"paused,omitempty"`          // gates pull/push/sync; cleared by `resume`
	MaxDelete      int       `yaml:"max_delete,omitempty"`      // safety cap for push --delete-after, default 1000
	Interval       int       `yaml:"interval,omitempty"`        // auto-sync interval in seconds (launchd/systemd), default 300
	SharedExcludes []string  `yaml:"shared_excludes,omitempty"` // operator-curated owned-but-shared-out folders, relative to mirror_path
}

// UserTunnelState holds the Cloudflare Tunnel values managed by `dot tunnel`.
type UserTunnelState struct {
	TunnelName string `yaml:"tunnel_name,omitempty"`
	TunnelID   string `yaml:"tunnel_id,omitempty"`
	Hostname   string `yaml:"hostname,omitempty"`
}

// IsZero lets yaml.v3 omit an unset tunnel block from user state.
func (s UserTunnelState) IsZero() bool {
	return s.TunnelName == "" && s.TunnelID == "" && s.Hostname == ""
}

// UserGuardState holds the Claude Code safety-hook values managed by
// `dot guard`. The hook hot path reads Careful/FreezeDir live on every
// tool call, so freeze/unfreeze take effect mid-session. The registered
// hook command itself is not mirrored here: settings.json is the single
// source of truth (status reads it live via InspectHookEntries).
type UserGuardState struct {
	Careful   bool   `yaml:"careful,omitempty"`
	FreezeDir string `yaml:"freeze_dir,omitempty"` // absolute path; edits outside it are denied
}

// IsZero lets yaml.v3 omit an unset guard block from user state.
func (s UserGuardState) IsZero() bool {
	return !s.Careful && s.FreezeDir == ""
}

// RepoConfig describes a git repository to clone into the workspace.
type RepoConfig struct {
	Name   string `yaml:"name"`             // subdirectory name: "work" or "vault"
	Remote string `yaml:"remote,omitempty"` // git remote URL (HTTPS or SSH)
}

// UserWorkspaceState holds workspace config from user state.
type UserWorkspaceState struct {
	Path          string       `yaml:"path,omitempty"`
	Vault         string       `yaml:"vault,omitempty"` // vault directory (~-form allowed); empty → detected, default <Path>/work/vault
	Gdrive        string       `yaml:"gdrive,omitempty"`
	GdriveSymlink string       `yaml:"gdrive_symlink,omitempty"` // symlink name for the cloud root (e.g. ~/gdrive-workspace, ~/Dropbox)
	Symlink       string       `yaml:"symlink,omitempty"`        // explicit symlink target for Path
	Repos         []RepoConfig `yaml:"repos,omitempty"`          // git repos to clone into workspace
}

// UserFontsState holds font config from user state.
type UserFontsState struct {
	Family string `yaml:"family,omitempty"`
}

// UserSSHState holds SSH config from user state.
type UserSSHState struct {
	KeyName string `yaml:"key_name,omitempty"`
}

// validProfiles lists the allowed profile names.
var validProfiles = []string{"minimal", "full", "server"}

var validAISkillsTools = map[string]bool{
	"claude":      true,
	"codex":       true,
	"agents":      true, // legacy inventory-only value, normalized by diagnostics
	"gemini":      true, // legacy inventory-only value, normalized by diagnostics
	"antigravity": true, // legacy inventory-only value, normalized by diagnostics
}

// Validate performs lightweight sanity checks on critical fields.
// Returns an error with a clear message for invalid values.
func (s *UserState) Validate() error {
	if s.Profile != "" {
		valid := false
		for _, p := range validProfiles {
			if s.Profile == p {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("invalid profile %q (must be one of: %s)", s.Profile, strings.Join(validProfiles, ", "))
		}
	}
	if s.Email != "" && !strings.Contains(s.Email, "@") {
		return fmt.Errorf("invalid email %q (missing @)", s.Email)
	}
	if s.GithubUser != "" {
		if len(s.GithubUser) > 39 {
			return fmt.Errorf("invalid github_user %q (max 39 characters)", s.GithubUser)
		}
		for _, r := range s.GithubUser {
			valid := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-'
			if !valid {
				return fmt.Errorf("invalid github_user %q (alphanumeric + hyphens only)", s.GithubUser)
			}
		}
	}
	if s.Modules.Rsync.Interval != 0 && (s.Modules.Rsync.Interval < 60 || s.Modules.Rsync.Interval > 86400) {
		return fmt.Errorf("rsync.interval must be 0 or 60..86400 seconds (got %d)", s.Modules.Rsync.Interval)
	}
	if s.Modules.Gsync.MaxDelete != 0 && (s.Modules.Gsync.MaxDelete < 1 || s.Modules.Gsync.MaxDelete > 1000000) {
		return fmt.Errorf("gdrive_sync.max_delete must be 0 or 1..1000000 (got %d)", s.Modules.Gsync.MaxDelete)
	}
	if s.Modules.Gsync.Interval != 0 && (s.Modules.Gsync.Interval < 60 || s.Modules.Gsync.Interval > 86400) {
		return fmt.Errorf("gdrive_sync.interval must be 0 or 60..86400 seconds (got %d)", s.Modules.Gsync.Interval)
	}
	if s.Modules.Git.CoauthorGuard != "" {
		switch s.Modules.Git.CoauthorGuard {
		case "off", "warn", "block":
		default:
			return fmt.Errorf("modules.git.coauthor_guard must be off, warn, or block (got %q)", s.Modules.Git.CoauthorGuard)
		}
	}
	switch s.Modules.Editor {
	case "", "zed", "code", "vi":
	default:
		return fmt.Errorf("modules.editor must be zed, code, or vi (got %q)", s.Modules.Editor)
	}
	if err := validateTunnelState(s.Modules.Tunnel); err != nil {
		return err
	}
	switch s.Modules.Container.Backend {
	case "", "auto", "apple", "docker", "podman":
	default:
		return fmt.Errorf("modules.container.backend must be auto, apple, docker, or podman (got %q)", s.Modules.Container.Backend)
	}
	for _, ip := range s.Modules.Container.DNS {
		if net.ParseIP(ip) == nil {
			return fmt.Errorf("modules.container.dns entry %q is not an IP address", ip)
		}
	}
	if s.Modules.Guard.FreezeDir != "" && !filepath.IsAbs(s.Modules.Guard.FreezeDir) {
		return fmt.Errorf("modules.guard.freeze_dir must be an absolute path (got %q)", s.Modules.Guard.FreezeDir)
	}
	if err := ValidateAIPolicy(s.Modules.AI.Policy); err != nil {
		return err
	}
	if err := ValidateAITooling(s.Modules.AI.Tooling); err != nil {
		return err
	}
	if err := validateAISkillsConfig(s.Modules.AI.Skills); err != nil {
		return err
	}
	for _, app := range s.Modules.TerminalApps.Apps {
		if !IsTerminalAppToken(app) {
			return fmt.Errorf("terminal_apps.apps entry %q must be one of the curated terminal apps", app)
		}
	}
	for _, p := range s.Modules.Gsync.SharedExcludes {
		// Paths must be relative to mirror_path. Absolute paths and parent
		// escapes would let the manual list reach outside the mirror tree
		// and exclude unrelated content (or be portable across machines
		// in misleading ways).
		if strings.HasPrefix(p, "/") {
			return fmt.Errorf("gdrive_sync.shared_excludes entry %q must be relative to mirror_path (no leading /)", p)
		}
		for _, seg := range strings.Split(p, "/") {
			if seg == ".." {
				return fmt.Errorf("gdrive_sync.shared_excludes entry %q may not contain '..' segments", p)
			}
		}
	}
	seen := make(map[string]bool)
	for _, repo := range s.Modules.Workspace.Repos {
		if repo.Name != "work" && repo.Name != "vault" {
			return fmt.Errorf("invalid workspace repo name %q (must be \"work\" or \"vault\")", repo.Name)
		}
		if repo.Remote == "" {
			return fmt.Errorf("workspace repo %q has empty remote URL", repo.Name)
		}
		if seen[repo.Name] {
			return fmt.Errorf("duplicate workspace repo name %q", repo.Name)
		}
		seen[repo.Name] = true
	}
	return nil
}

// validateTunnelState delegates to the canonical validators in
// internal/tunnel so `dot tunnel setup` and state validation can never
// drift apart (a value accepted at setup must load on the next run).
func validateTunnelState(t UserTunnelState) error {
	if t.TunnelName != "" {
		if err := tunnel.ValidateTunnelName(t.TunnelName); err != nil {
			return fmt.Errorf("modules.tunnel.tunnel_name: %w", err)
		}
	}
	if t.TunnelID != "" {
		if err := tunnel.ValidateTunnelID(t.TunnelID); err != nil {
			return fmt.Errorf("modules.tunnel.tunnel_id: %w", err)
		}
	}
	if t.Hostname != "" {
		if err := tunnel.ValidateHostname(t.Hostname); err != nil {
			return fmt.Errorf("modules.tunnel.hostname: %w", err)
		}
	}
	return nil
}

// validateAISkillsConfig validates the diagnostics defaults. skills.Enabled is
// deprecated and ignored (legacy configs with enabled: true must still load).
func validateAISkillsConfig(skills AISkillsConfig) error {
	if skills.IsZero() {
		return nil
	}
	provider := strings.ToLower(strings.TrimSpace(skills.Provider))
	switch provider {
	case "", "maru", "anchor", "path": // anchor is a legacy alias for maru; empty falls back to CLI defaults
	default:
		return fmt.Errorf("modules.ai.skills.provider must be maru or path (got %q)", skills.Provider)
	}
	if provider == "path" && strings.TrimSpace(skills.SSOTPath) == "" {
		return fmt.Errorf("modules.ai.skills.ssot_path is required when provider is path")
	}
	seen := map[string]bool{}
	for _, raw := range skills.Tools {
		tool := strings.ToLower(strings.TrimSpace(raw))
		if tool == "" {
			return fmt.Errorf("modules.ai.skills.tools may not contain empty entries")
		}
		if !validAISkillsTools[tool] {
			return fmt.Errorf("modules.ai.skills.tools entry %q must be one of: agents, antigravity, claude, codex, gemini", raw)
		}
		if seen[tool] {
			return fmt.Errorf("modules.ai.skills.tools contains duplicate entry %q", raw)
		}
		seen[tool] = true
	}
	return nil
}

// StateDir returns the path to the dotfiles config directory.
func StateDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "dotfiles")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "dotfiles")
}

// StatePath returns the path to the user state file.
func StatePath() string {
	return filepath.Join(StateDir(), "config.yaml")
}

// LoadState reads user state from disk.
// Returns an empty state on missing file, an error on parse failure.
// Validation warnings are printed to stderr but do not fail the load
// (so users can recover by running 'dot reconfigure').
func LoadState() (*UserState, error) {
	return loadStateAt(StatePath())
}

// readStateFile is the one place a state file is read and decoded. Both read
// entry points route through it, because a version check attached only to
// loadStateAt would leave `dot init --from`, internal/cli/onestop.go and
// internal/profilesnap unversioned (D-03 as corrected).
//
// It returns the peeked schema version even when the decode fails, so the
// wrapper can warn about a forward-version file before returning that error.
// The noun ("state" or "config") keeps each wrapper's error wording byte-exact:
// the differential harness compares stderr byte for byte and the wordings are
// pinned by tests, so a merge that rewords either path is the thing that is
// wrong, not the wording.
func readStateFile(path, noun string) (*UserState, int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, fmt.Errorf("reading %s: %w", noun, err)
	}

	version := peekSchemaVersion(data)

	var state UserState
	if err := yaml.Unmarshal(data, &state); err != nil {
		return nil, version, fmt.Errorf("parsing %s: %w", noun, err)
	}
	applyLegacyKeys(&state, data)
	return &state, version, nil
}

func loadStateAt(path string) (*UserState, error) {
	state, version, err := readStateFile(path, "state")
	if version > currentSchemaVersion {
		warnForwardSchema(path, version)
	}
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &UserState{}, nil
		}
		return nil, err
	}
	if err := state.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: state file has invalid values: %v\n", err)
		fmt.Fprintln(os.Stderr, "  Run 'dot reconfigure' to fix.")
	}
	return state, nil
}

// SaveState writes user state to disk atomically.
// Validates before writing — invalid state is never persisted.
func SaveState(state *UserState) error {
	return saveStateAt(StatePath(), state)
}

// MarshalState serializes state as a state-file document, stamping the schema
// version this binary writes.
//
// The stamp happens here rather than in callers so one place decides and no
// caller can forget (DEBT-01). A copy keeps the caller's value untouched.
//
// It is exported because saveStateAt is NOT the only place a UserState becomes
// a file: `dot config export` writes one to an arbitrary path. Before this
// existed that path marshaled the struct directly, which meant a state loaded
// from a forward-version file was re-emitted still CLAIMING that version while
// yaml.v3 had already dropped the keys this binary has no fields for. The
// exported document was a v1 payload wearing a v2 label, and a newer binary
// reading it would trust the label. Found by review on PR #88.
//
// Refusing a forward-version source is the same rule saveStateAt applies to a
// forward-version destination, and it takes the same override.
func MarshalState(state *UserState) ([]byte, error) {
	if state.SchemaVersion > currentSchemaVersion && os.Getenv(schemaForceEnv) != "1" {
		return nil, fmt.Errorf(
			"refusing to write out state read from a newer dot (state schema version %d, this binary writes %d).\n"+
				"  Keys this binary does not know were already dropped when the file was read, so writing it\n"+
				"  back would produce a document claiming version %d without its version %d data.\n"+
				"  To upgrade this machine: dot update\n"+
				"  To write it anyway and lose those keys: %s=1",
			state.SchemaVersion, currentSchemaVersion,
			state.SchemaVersion, state.SchemaVersion, schemaForceEnv)
	}
	out := *state
	out.SchemaVersion = currentSchemaVersion
	data, err := yaml.Marshal(&out)
	if err != nil {
		return nil, fmt.Errorf("marshaling state: %w", err)
	}
	return data, nil
}

// saveStateAt performs an atomic write: marshal → temp file → fsync → rename.
// On POSIX filesystems, rename is atomic, so partial writes cannot corrupt
// the existing config file.
func saveStateAt(path string, state *UserState) error {
	if err := state.Validate(); err != nil {
		return fmt.Errorf("refusing to save invalid state: %w", err)
	}
	if err := checkSchemaDowngrade(path); err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating state dir: %w", err)
	}

	data, err := MarshalState(state)
	if err != nil {
		return err
	}

	// Write to temp file in the same directory (same filesystem → rename is atomic)
	tmpFile, err := os.CreateTemp(dir, ".config.yaml.*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		cleanup()
		return fmt.Errorf("writing temp file: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		cleanup()
		return fmt.Errorf("syncing temp file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		cleanup()
		return fmt.Errorf("closing temp file: %w", err)
	}
	if err := os.Chmod(tmpPath, 0644); err != nil {
		cleanup()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return fmt.Errorf("rename temp file: %w", err)
	}

	// The write choke point is the smallest correct site for this notice: it
	// fires once per write, from every write command, and no read path can
	// reach it. `note: ` is the prefix internal/syncer/sync.go:217 already uses
	// for a non-actionable informational line.
	if len(state.carriedLegacy) > 0 {
		fmt.Fprintf(os.Stderr, "note: migrated legacy state keys to their canonical form: %s\n",
			strings.Join(state.carriedLegacy, ", "))
	}
	return nil
}

// schemaForceEnv overrides the downgrade refusal below. It ships with the name
// DEBT-02 gives it. Three naming conventions are in play in this tree and none
// of them is this one -- the single existing DOT_* variable is an internal
// scheduler signal, all seven user-facing variables use the DOTFILES_ prefix,
// and the closest boolean read (internal/cli/apply.go:42) compares against an
// exact string -- so the comparison is a decision rather than a default: the
// exact string "1" and nothing else. It is documented in the README's
// environment-variable table, because an escape hatch that appears only inside
// an error message is a worse outcome than the refusal it relieves (D-20).
const schemaForceEnv = "DOT_SCHEMA_FORCE"

// checkSchemaDowngrade refuses to overwrite a state file written by a newer
// dot. yaml.v3 drops keys it does not know with a nil error, so the loss would
// otherwise be silent, and the state file physically arrives from other
// machines through the synced workspace.
//
// A missing destination is the ordinary first write, not a downgrade. An
// unreadable or unparseable destination peeks as 0 and is overwritten
// deliberately: refusing to write over a file we cannot read would brick
// recovery from a corrupt state file.
//
// Accepted risk: the destination is read here and renamed over later, so the
// file could change in between. It lives under the user's own config directory
// and closing the window means rewriting the atomic-write path around a single
// descriptor for no realistic gain.
func checkSchemaDowngrade(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	onDisk := peekSchemaVersion(data)
	if onDisk <= currentSchemaVersion {
		return nil
	}
	if os.Getenv(schemaForceEnv) == "1" {
		return nil
	}
	return fmt.Errorf(
		"refusing to overwrite %s: it was written by a newer dot (state schema version %d, this binary writes %d).\n"+
			"  Writing it here would drop every key this binary does not know, with no error anywhere.\n"+
			"  To upgrade this machine: dot update\n"+
			"  To overwrite it anyway and lose those keys: %s=1",
		path, onDisk, currentSchemaVersion, schemaForceEnv)
}

// StateDirForHome returns the state directory for a specific home directory.
func StateDirForHome(homeDir string) string {
	return filepath.Join(homeDir, ".config", "dotfiles")
}

// StatePathForHome returns the state file path for a specific home directory.
func StatePathForHome(homeDir string) string {
	return filepath.Join(StateDirForHome(homeDir), "config.yaml")
}

// LoadStateFrom reads user state from an arbitrary file path.
// Unlike LoadState, it returns an error if the file does not exist.
func LoadStateFrom(path string) (*UserState, error) {
	state, version, err := readStateFile(path, "config")
	if version > currentSchemaVersion {
		warnForwardSchema(path, version)
	}
	if err != nil {
		return nil, err
	}
	if err := state.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: imported config has invalid values: %v\n", err)
	}
	if state.Name == "" && state.Profile == "" {
		return nil, fmt.Errorf("imported config is empty (no name or profile set)")
	}
	return state, nil
}

// LoadStateForHome reads user state from a specific home directory.
func LoadStateForHome(homeDir string) (*UserState, error) {
	return loadStateAt(StatePathForHome(homeDir))
}

// SaveStateForHome writes user state to a specific home directory atomically.
func SaveStateForHome(homeDir string, state *UserState) error {
	return saveStateAt(StatePathForHome(homeDir), state)
}

// ApplyStateToConfig merges user state into a Config loaded from a profile.
func ApplyStateToConfig(cfg *Config, state *UserState) {
	cfg.Name = state.Name
	cfg.Email = state.Email
	cfg.GithubUser = state.GithubUser
	cfg.Timezone = state.Timezone

	// Module opt-ins from state
	if state.Modules.Workspace.Path != "" {
		cfg.Modules.Workspace.Enabled = true
		cfg.Modules.Workspace.Path = state.Modules.Workspace.Path
		cfg.Modules.Workspace.Vault = state.Modules.Workspace.Vault
		cfg.Modules.Workspace.Gdrive = state.Modules.Workspace.Gdrive
		cfg.Modules.Workspace.GdriveSymlink = state.Modules.Workspace.GdriveSymlink
		cfg.Modules.Workspace.Symlink = state.Modules.Workspace.Symlink
		cfg.Modules.Workspace.Repos = state.Modules.Workspace.Repos
	}
	if state.Modules.AI.Policy != nil {
		cfg.Modules.AI.Enabled = true
		cfg.Modules.AI.Policy = state.Modules.AI.Policy.Clone()
	}
	if state.Modules.AI.Tooling != nil {
		cfg.Modules.AI.Enabled = true
		cfg.Modules.AI.Tooling = state.Modules.AI.Tooling.Clone()
	}
	if state.Modules.AI.Enabled {
		cfg.Modules.AI.Enabled = true
	}
	if state.Modules.AI.AgentsSSOT {
		cfg.Modules.AI.Enabled = true
		cfg.Modules.AI.AgentsSSOT = true
	}
	if state.Modules.AI.HUD {
		cfg.Modules.AI.Enabled = true
		cfg.Modules.AI.HUD = true
	}
	if !state.Modules.AI.Skills.IsZero() {
		cfg.Modules.AI.Enabled = true
		cfg.Modules.AI.Skills = state.Modules.AI.Skills
		cfg.Modules.AI.Skills.Tools = append([]string(nil), state.Modules.AI.Skills.Tools...)
	}
	if state.Modules.Git.CoauthorGuard != "" {
		cfg.Modules.Git.Enabled = true
		cfg.Modules.Git.CoauthorGuard = state.Modules.Git.CoauthorGuard
	}
	terminalAppsSet := state.Modules.TerminalApps.Enabled || len(state.Modules.TerminalApps.Apps) > 0
	if terminalAppsSet {
		cfg.Modules.Terminal.Apps = append([]string(nil), state.Modules.TerminalApps.Apps...)
		cfg.Modules.Terminal.Warp = sliceutil.Contains(cfg.Modules.Terminal.Apps, "warp")
	} else if state.Modules.Warp && !sliceutil.Contains(cfg.Modules.Terminal.Apps, "warp") {
		cfg.Modules.Terminal.Apps = append(cfg.Modules.Terminal.Apps, "warp")
		cfg.Modules.Terminal.Warp = true
	}
	if state.Modules.PromptStyle != "" {
		cfg.Modules.Terminal.PromptStyle = state.Modules.PromptStyle
	}
	if state.Modules.Editor != "" {
		cfg.Modules.Shell.Editor = state.Modules.Editor
	}
	if state.Modules.Fonts.Family != "" {
		cfg.Modules.Fonts.Family = state.Modules.Fonts.Family
	}
	if state.SSH.KeyName != "" {
		cfg.Modules.SSH.KeyName = state.SSH.KeyName
	}
	// MacApps: user state toggles the module and overlays cask selections.
	if state.Modules.MacApps.Enabled {
		cfg.Modules.MacApps.Enabled = true
	}
	if state.Modules.MacApps.BackupRoot != "" {
		cfg.Modules.MacApps.BackupRoot = state.Modules.MacApps.BackupRoot
	}
	if len(state.Modules.MacApps.Casks) > 0 {
		cfg.Casks = state.Modules.MacApps.Casks
	}
	if len(state.Modules.MacApps.CasksExtra) > 0 {
		cfg.CasksExtra = append(cfg.CasksExtra, state.Modules.MacApps.CasksExtra...)
	}
	if c := state.Modules.Container; c.Enabled {
		cfg.Modules.Container.Enabled = true
	}
	if c := state.Modules.Container; c.Backend != "" {
		cfg.Modules.Container.Backend = c.Backend
	}
	if c := state.Modules.Container; len(c.DNS) > 0 {
		cfg.Modules.Container.DNS = append([]string(nil), c.DNS...)
	}
}
