package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/STAIxBWLB/dotfiles/internal/fileutil"
)

// Config is the root configuration struct, mapped from profile YAML + user state.
type Config struct {
	Extends       string         `yaml:"extends,omitempty"`
	Modules       ModulesConfig  `yaml:"modules"`
	Watchdog      WatchdogConfig `yaml:"watchdog,omitempty"`
	Packages      []string       `yaml:"packages"`
	PackagesExtra []string       `yaml:"packages_extra"`
	Casks         []string       `yaml:"casks,omitempty"`
	CasksExtra    []string       `yaml:"casks_extra,omitempty"`
	// Populated from user state, not profile YAML
	Name       string `yaml:"-"`
	Email      string `yaml:"-"`
	GithubUser string `yaml:"-"`
	Timezone   string `yaml:"-"`
	// Runtime info
	System *SystemInfo `yaml:"-"`
}

// ModulesConfig holds per-module configuration.
type ModulesConfig struct {
	Packages  ModuleToggle    `yaml:"packages"`
	Shell     ShellConfig     `yaml:"shell"`
	Node      ModuleToggle    `yaml:"node"`
	Git       GitConfig       `yaml:"git"`
	SSH       SSHModConfig    `yaml:"ssh"`
	Terminal  TermConfig      `yaml:"terminal"`
	Tmux      ModuleToggle    `yaml:"tmux"`
	Workspace WorkConfig      `yaml:"workspace"`
	AI        AIConfig        `yaml:"ai"`
	Fonts     FontsConfig     `yaml:"fonts"`
	Conda     ModuleToggle    `yaml:"conda"`
	GPG       ModuleToggle    `yaml:"gpg"`
	Secrets   ModuleToggle    `yaml:"secrets"`
	MacApps   MacAppsConfig   `yaml:"macapps"`
	Container ContainerConfig `yaml:"container"`
}

// UnmarshalYAML accepts the legacy modules.ai_tools key as read-only input and
// normalizes it into modules.ai.
//
// Deliberately OUT of DEBT-03's scope, which deleted the two UserState shims.
// The reason is mechanical: a profile Config is either embedded in the binary
// (loader.go, and no embedded profile carries the legacy key) or a file the
// user passes with --config, and nothing in this package ever marshals a
// Config back to disk -- the only yaml.Marshal here writes UserState. With no
// write path, "migrate then persist" has no meaning, and deleting this shim
// would drop a --config file's legacy key with nothing anywhere able to
// rewrite the file. Keeping it is what satisfies DEBT-03 rather than what
// evades it. Do not re-open without a write path.
func (m *ModulesConfig) UnmarshalYAML(value *yaml.Node) error {
	type raw ModulesConfig
	aux := struct {
		*raw     `yaml:",inline"`
		LegacyAI AIConfig `yaml:"ai_tools"`
	}{
		raw: (*raw)(m),
	}
	if err := value.Decode(&aux); err != nil {
		return err
	}
	if !m.AI.Enabled && aux.LegacyAI.Enabled {
		m.AI = aux.LegacyAI
	}
	return nil
}

// MacAppsConfig configures the macapps module.
type MacAppsConfig struct {
	Enabled    bool   `yaml:"enabled"`
	BackupRoot string `yaml:"backup_root,omitempty"` // shared root for app-settings/ + profiles/ snapshots
}

// ContainerConfig configures the shared `container` command (opt-in).
type ContainerConfig struct {
	Enabled bool     `yaml:"enabled"`
	Backend string   `yaml:"backend,omitempty"` // auto (default) | apple | docker | podman
	DNS     []string `yaml:"dns,omitempty"`     // default --dns servers; empty = backend default
}

// ModuleToggle is a simple enabled/disabled toggle.
type ModuleToggle struct {
	Enabled bool `yaml:"enabled"`
}

// Duration is a time.Duration that YAML-decodes from Go duration strings
// ("300s", "30m") and from plain integers (read as seconds), so profile YAML
// stays readable. yaml.v3 would otherwise demand integer nanoseconds.
type Duration time.Duration

// UnmarshalYAML decodes a duration string or an integer count of seconds.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		if i, err := strconv.ParseInt(value.Value, 10, 64); err == nil {
			*d = Duration(time.Duration(i) * time.Second)
			return nil
		}
		parsed, err := time.ParseDuration(value.Value)
		if err != nil {
			return fmt.Errorf("invalid duration %q: %w", value.Value, err)
		}
		*d = Duration(parsed)
		return nil
	}
	return fmt.Errorf("invalid duration node: expected a scalar like \"300s\"")
}

// MarshalYAML emits the Go duration string ("5m0s"). Without it yaml.v3
// marshals the underlying int64 as nanoseconds, which UnmarshalYAML would
// read back as seconds — a 10^9 round-trip skew (the watchdog setup snapshot
// round-trips this way, and a nanosecond-read-as-second sustain window means
// the scheduled reaper never acts).
func (d Duration) MarshalYAML() (any, error) {
	return time.Duration(d).String(), nil
}

// Std returns the value as a time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// IsZero lets yaml.v3 omit an unset duration from rendered output.
func (d Duration) IsZero() bool { return d == 0 }

// WatchdogConfig configures the watchdog: runaway-process reaping (P1),
// connectivity heal, power hardening, and external monitors (later phases).
// The full profile ships it disabled; hosts opt in explicitly.
type WatchdogConfig struct {
	Enabled bool                 `yaml:"enabled"`
	Reaper  WatchdogReaperConfig `yaml:"reaper,omitempty"`
	Warp    WatchdogWarpConfig   `yaml:"warp,omitempty"`
	Power   WatchdogPowerConfig  `yaml:"power,omitempty"`
	Monit   WatchdogMonitConfig  `yaml:"monit,omitempty"`
	Beszel  WatchdogBeszelConfig `yaml:"beszel,omitempty"`
	Notify  WatchdogNotifyConfig `yaml:"notify,omitempty"`
}

// IsZero lets yaml.v3 omit an unset watchdog block from rendered output.
func (c WatchdogConfig) IsZero() bool {
	return !c.Enabled && c.Reaper.IsZero() && c.Warp.IsZero() &&
		c.Power.IsZero() && c.Monit.IsZero() && c.Beszel.IsZero() && c.Notify.IsZero()
}

// WatchdogReaperConfig configures the runaway-process reaper.
type WatchdogReaperConfig struct {
	Mode         string              `yaml:"mode,omitempty"` // dry-run (default) | enforce
	Interval     Duration            `yaml:"interval,omitempty"`
	CPUThreshold float64             `yaml:"cpu_threshold,omitempty"` // percent, per process
	Sustain      Duration            `yaml:"sustain,omitempty"`
	Match        WatchdogMatchConfig `yaml:"match,omitempty"`
	Allow        []string            `yaml:"allow,omitempty"`
	KillGrace    Duration            `yaml:"kill_grace,omitempty"` // SIGTERM → wait → SIGKILL
}

// IsZero lets yaml.v3 omit an unset reaper block.
func (c WatchdogReaperConfig) IsZero() bool {
	return c.Mode == "" && c.Interval.IsZero() && c.CPUThreshold == 0 &&
		c.Sustain.IsZero() && c.Match.IsZero() && len(c.Allow) == 0 && c.KillGrace.IsZero()
}

// WatchdogMatchConfig selects which processes the reaper considers.
type WatchdogMatchConfig struct {
	OrphanOnly bool     `yaml:"orphan_only,omitempty"` // PPID == 1
	Paths      []string `yaml:"paths,omitempty"`       // globs against the executable path; $VARS expanded
	Args       []string `yaml:"args,omitempty"`        // substrings of the full command line
}

// IsZero lets yaml.v3 omit an unset match block.
func (c WatchdogMatchConfig) IsZero() bool {
	return !c.OrphanOnly && len(c.Paths) == 0 && len(c.Args) == 0
}

// WatchdogWarpConfig configures Cloudflare WARP self-heal (phase P2; schema only).
type WatchdogWarpConfig struct {
	Enabled       bool     `yaml:"enabled"`
	Interval      Duration `yaml:"interval,omitempty"`
	FailThreshold int      `yaml:"fail_threshold,omitempty"`
}

// IsZero lets yaml.v3 omit an unset warp block.
func (c WatchdogWarpConfig) IsZero() bool {
	return !c.Enabled && c.Interval.IsZero() && c.FailThreshold == 0
}

// WatchdogPowerConfig configures headless power hardening (later phase; schema only).
type WatchdogPowerConfig struct {
	Headless bool `yaml:"headless,omitempty"`
}

// IsZero lets yaml.v3 omit an unset power block.
func (c WatchdogPowerConfig) IsZero() bool { return !c.Headless }

// WatchdogMonitConfig configures monit service-health supervision (phase P3):
// system load/CPU alerting and the Screen Sharing heal.
type WatchdogMonitConfig struct {
	Enabled bool `yaml:"enabled"`
	// Load1Threshold alerts when loadavg(5min) exceeds it; unset means the
	// host's logical CPU count (resolved at setup time).
	Load1Threshold     float64 `yaml:"load1_threshold,omitempty"`
	CPUUserThreshold   float64 `yaml:"cpu_user_threshold,omitempty"`   // percent; default 90
	CPUSystemThreshold float64 `yaml:"cpu_system_threshold,omitempty"` // percent; default 50
	Cycles             int     `yaml:"cycles,omitempty"`               // consecutive failed cycles before acting; default 3
	// ScreenSharing is a pointer so an explicit `false` survives the true
	// default: unset means the Screen Sharing heal check is rendered.
	ScreenSharing *bool `yaml:"screensharing,omitempty"`
}

// IsZero lets yaml.v3 omit an unset monit block.
func (c WatchdogMonitConfig) IsZero() bool {
	return !c.Enabled && c.Load1Threshold == 0 && c.CPUUserThreshold == 0 &&
		c.CPUSystemThreshold == 0 && c.Cycles == 0 && c.ScreenSharing == nil
}

// WatchdogBeszelConfig configures the Beszel external-monitoring agent
// (phase P4). The hub KEY/TOKEN never live here: they stay in the
// secrets-managed env file named by EnvPath.
type WatchdogBeszelConfig struct {
	Enabled bool   `yaml:"enabled"`
	HubURL  string `yaml:"hub_url,omitempty"`
	Listen  string `yaml:"listen,omitempty"`   // agent listen address, default ":45876"
	EnvPath string `yaml:"env_path,omitempty"` // default ~/.config/beszel/agent.env
}

// IsZero lets yaml.v3 omit an unset beszel block.
func (c WatchdogBeszelConfig) IsZero() bool {
	return !c.Enabled && c.HubURL == "" && c.Listen == "" && c.EnvPath == ""
}

// WatchdogNotifyConfig configures external alert fan-out.
type WatchdogNotifyConfig struct {
	MacOS    bool                   `yaml:"macos,omitempty"`    // osascript desktop notification
	NtfyURL  string                 `yaml:"ntfy_url,omitempty"` // ntfy topic URL; empty skips the POST
	Telegram WatchdogTelegramConfig `yaml:"telegram,omitempty"`
}

// IsZero lets yaml.v3 omit an unset notify block.
func (c WatchdogNotifyConfig) IsZero() bool {
	return !c.MacOS && c.NtfyURL == "" && c.Telegram.IsZero()
}

// WatchdogTelegramConfig configures the Telegram alert channel. The bot
// token and chat ID never live here: they stay in the secrets-managed env
// file named by EnvPath (TELEGRAM_BOT_TOKEN / TELEGRAM_CHAT_ID).
type WatchdogTelegramConfig struct {
	Enabled bool `yaml:"enabled"`
	// EnvPath names the KEY=VALUE env file with the bot credentials;
	// default ~/.config/dot/telegram.env.
	EnvPath string `yaml:"env_path,omitempty"`
	// ReminderInterval is the minimum time between repeat alerts while a
	// failure condition persists; default 30m.
	ReminderInterval Duration `yaml:"reminder_interval,omitempty"`
	// QuarantineThreshold alerts when one peer sync quarantines more
	// deletions than this; 0 disables that alert.
	QuarantineThreshold int `yaml:"quarantine_threshold,omitempty"`
}

// IsZero lets yaml.v3 omit an unset telegram block.
func (c WatchdogTelegramConfig) IsZero() bool {
	return !c.Enabled && c.EnvPath == "" && c.ReminderInterval.IsZero() && c.QuarantineThreshold == 0
}

// AIConfig configures AI helper files plus optional agents and skills SSOT deployment.
type AIConfig struct {
	Policy     *AIPolicyConfig  `yaml:"policy,omitempty" json:"policy,omitempty"`
	Tooling    *AIToolingConfig `yaml:"tooling,omitempty"`
	Enabled    bool             `yaml:"enabled"`
	AgentsSSOT bool             `yaml:"agents_ssot,omitempty"`
	HUD        bool             `yaml:"hud,omitempty"`
	Skills     AISkillsConfig   `yaml:"skills,omitempty"`
}

// AISkillsConfig provides defaults for the read-only `dot ai skills`
// diagnostics (status/path). Runtime skill symlinks are managed by the Maru
// app; dot never writes them.
type AISkillsConfig struct {
	Enabled  bool     `yaml:"enabled,omitempty"`   // deprecated: ignored, kept so legacy configs still load
	Provider string   `yaml:"provider,omitempty"`  // maru or path (anchor is a legacy alias for maru)
	SSOTPath string   `yaml:"ssot_path,omitempty"` // optional for provider=maru
	Tools    []string `yaml:"tools,omitempty"`     // default tool targets for diagnostics
}

// IsZero lets yaml.v3 omit an unset skills block from user state/config output.
func (c AISkillsConfig) IsZero() bool {
	return !c.Enabled && c.Provider == "" && c.SSOTPath == "" && len(c.Tools) == 0
}

// ShellConfig configures the shell module.
type ShellConfig struct {
	Enabled bool   `yaml:"enabled"`
	Editor  string `yaml:"editor,omitempty"` // zed, code, or vi; empty → platform default
}

// EditorCommand returns the $EDITOR value for an editor choice.
// Empty choice falls back to the platform default.
func EditorCommand(editor string, isDarwin bool) string {
	switch editor {
	case "zed":
		return "zed --wait"
	case "code":
		return "code --wait"
	case "vi":
		return "vi"
	}
	if isDarwin {
		return "zed --wait"
	}
	return "nano"
}

// EditorCask returns the Homebrew cask providing the editor, or "" when the
// editor needs no install (vi) or no editor is selected.
func EditorCask(editor string) string {
	switch editor {
	case "zed":
		return "zed"
	case "code":
		return "visual-studio-code"
	}
	return ""
}

// GitConfig configures the git module.
type GitConfig struct {
	Enabled       bool   `yaml:"enabled"`
	Signing       bool   `yaml:"signing"`
	CoauthorGuard string `yaml:"coauthor_guard,omitempty"`
}

// SSHModConfig configures the ssh module.
type SSHModConfig struct {
	Enabled bool   `yaml:"enabled"`
	KeyName string `yaml:"key_name,omitempty"`
}

// TermConfig configures the terminal module.
type TermConfig struct {
	Enabled     bool     `yaml:"enabled"`
	Warp        bool     `yaml:"warp"`
	PromptStyle string   `yaml:"prompt_style,omitempty"` // "minimal" or "rich"
	Apps        []string `yaml:"apps,omitempty"`         // GUI terminal apps: orca, warp, wave, cmux, iterm2
}

// WorkConfig configures the workspace module.
type WorkConfig struct {
	Enabled       bool         `yaml:"enabled"`
	Path          string       `yaml:"path,omitempty"`           // workspace root (e.g. ~/workspace)
	Vault         string       `yaml:"vault,omitempty"`          // vault directory (~-form allowed); empty → detected, default <Path>/work/vault
	Gdrive        string       `yaml:"gdrive,omitempty"`         // cloud storage root (Google Drive or Dropbox); key kept for compat
	GdriveSymlink string       `yaml:"gdrive_symlink,omitempty"` // symlink name for the cloud root (e.g. ~/gdrive-workspace, ~/Dropbox)
	Symlink       string       `yaml:"symlink,omitempty"`        // explicit symlink target for Path (if set, Path → Symlink)
	Repos         []RepoConfig `yaml:"repos,omitempty"`          // git repos to clone into workspace
}

// FontsConfig configures the fonts module.
type FontsConfig struct {
	Enabled bool   `yaml:"enabled"`
	Family  string `yaml:"family,omitempty"`
}

// SecretsUserConfig holds user-specific secrets configuration.
type SecretsUserConfig struct {
	AgeIdentity   string        `yaml:"age_identity,omitempty"`
	AgeRecipients []string      `yaml:"age_recipients,omitempty"`
	LastBackup    *BackupRecord `yaml:"last_backup,omitempty"`
}

// BackupRecord records the most recent successful secrets backup.
type BackupRecord struct {
	Path  string    `yaml:"path"`
	Time  time.Time `yaml:"time"`
	Files int       `yaml:"files"`
}

// IsModuleEnabled returns whether a given module name is enabled in this config.
func (c *Config) IsModuleEnabled(name string) bool {
	switch name {
	case "packages":
		return c.Modules.Packages.Enabled
	case "shell":
		return c.Modules.Shell.Enabled
	case "node":
		return c.Modules.Node.Enabled
	case "git":
		return c.Modules.Git.Enabled
	case "ssh":
		return c.Modules.SSH.Enabled
	case "terminal":
		return c.Modules.Terminal.Enabled
	case "tmux":
		return c.Modules.Tmux.Enabled
	case "workspace":
		return c.Modules.Workspace.Enabled
	case "ai":
		return c.Modules.AI.Enabled
	case "fonts":
		return c.Modules.Fonts.Enabled
	case "conda":
		return c.Modules.Conda.Enabled
	case "gpg":
		return c.Modules.GPG.Enabled
	case "secrets":
		return c.Modules.Secrets.Enabled
	case "macapps":
		return c.Modules.MacApps.Enabled
	case "container":
		return c.Modules.Container.Enabled
	default:
		return false
	}
}

// AllPackages returns the merged package list (base + extra).
func (c *Config) AllPackages() []string {
	seen := make(map[string]bool, len(c.Packages)+len(c.PackagesExtra))
	var result []string
	for _, p := range c.Packages {
		if !seen[p] {
			seen[p] = true
			result = append(result, p)
		}
	}
	for _, p := range c.PackagesExtra {
		if !seen[p] {
			seen[p] = true
			result = append(result, p)
		}
	}
	return result
}

// AllCasks returns the merged cask list (base + extra), de-duplicated.
func (c *Config) AllCasks() []string {
	seen := make(map[string]bool, len(c.Casks)+len(c.Modules.Terminal.Apps)+len(c.CasksExtra))
	var result []string
	for _, p := range c.Casks {
		if !seen[p] {
			seen[p] = true
			result = append(result, p)
		}
	}
	if c.Modules.Terminal.Enabled {
		for _, p := range c.Modules.Terminal.Apps {
			if !seen[p] {
				seen[p] = true
				result = append(result, p)
			}
		}
	}
	for _, p := range c.CasksExtra {
		if !seen[p] {
			seen[p] = true
			result = append(result, p)
		}
	}
	if cask := EditorCask(c.Modules.Shell.Editor); cask != "" && !seen[cask] {
		result = append(result, cask)
	}
	return result
}

// VaultPath returns the vault directory in ~-form, resolved from the
// workspace module config. home is the home the run targets, which detection
// stats. See ResolveVaultPath.
func (c *Config) VaultPath(home string) string {
	return ResolveVaultPath(c.Modules.Workspace.Vault, c.Modules.Workspace.Path, home)
}

// VaultCloneTarget returns the clone target for a standalone vault repo in
// ~-form, or "" when no vault location is configured or detectable. See
// ResolveVaultCloneTarget.
func (c *Config) VaultCloneTarget(home string) string {
	return ResolveVaultCloneTarget(c.Modules.Workspace.Vault, c.Modules.Workspace.Path, home)
}

// ResolveVaultPath returns the vault directory in ~-form. Resolution order:
// explicit vault value → detected existing directory (<ws>/work/vault first,
// then <ws>/vault) → default <ws>/work/vault. Render-time detection lets
// VAULT track the real location without a reconfigure. A relative explicit
// value (no ~ or / prefix) is anchored under the workspace path. home is the
// home a ~-form workspace path expands against for the on-disk detection.
func ResolveVaultPath(vault, wsPath, home string) string {
	if vault != "" {
		return anchorVaultPath(vault, wsPath)
	}
	if wsPath == "" {
		return ""
	}
	if detected := detectVaultDir(wsPath, home); detected != "" {
		return detected
	}
	return joinPathTilde(wsPath, "work/vault")
}

// ResolveVaultCloneTarget resolves where a standalone vault repo entry would
// clone to. Unlike ResolveVaultPath it never invents the fresh default: when
// the vault location is neither configured nor present on disk it returns "",
// so legacy setups (separate vault repo, no workspace.vault key) keep cloning
// into <ws>/vault instead of being redirected into the work tree.
func ResolveVaultCloneTarget(vault, wsPath, home string) string {
	if vault != "" {
		return anchorVaultPath(vault, wsPath)
	}
	if wsPath == "" {
		return ""
	}
	return detectVaultDir(wsPath, home)
}

// anchorVaultPath returns vault unchanged when it is ~-form or absolute;
// a relative vault is anchored under the workspace path.
func anchorVaultPath(vault, wsPath string) string {
	if strings.HasPrefix(vault, "/") || strings.HasPrefix(vault, "~") || wsPath == "" {
		return vault
	}
	return joinPathTilde(wsPath, vault)
}

// detectVaultDir returns the first existing vault directory under the
// workspace path in ~-form (<ws>/work/vault preferred, then <ws>/vault),
// or "" when neither exists. home is the home a ~-form wsPath is stat'd
// under: a run pointed elsewhere must not probe the invoking user's tree.
func detectVaultDir(wsPath, home string) string {
	if strings.HasPrefix(wsPath, "~/") && home == "" {
		// Joining "" with the rest yields a relative path, so the stats below
		// would probe whatever directory the binary was started in. Detect
		// nothing instead: callers fall back to the fresh default.
		return ""
	}
	expanded := fileutil.ExpandHomeFor(wsPath, home)
	for _, rel := range []string{"work/vault", "vault"} {
		if fi, err := os.Stat(filepath.Join(expanded, rel)); err == nil && fi.IsDir() {
			return joinPathTilde(wsPath, rel)
		}
	}
	return ""
}

// joinPathTilde joins a relative path onto a base that may be in ~-form,
// preserving the ~ prefix.
func joinPathTilde(base, rel string) string {
	return strings.TrimSuffix(base, "/") + "/" + rel
}

// TemplateData returns a map suitable for Go template rendering. home is the
// home the run targets: it is published as {{.Home}} and rendered INTO the
// files the run writes, so a --home run must not embed the invoking user's
// home in them (BUG-20).
func (c *Config) TemplateData(home string) map[string]any {
	isDarwin := false
	hostname := ""
	if c.System != nil {
		isDarwin = c.System.OS == "darwin"
		hostname = c.System.Hostname
	}

	hasCUDA := false
	cudaHome := ""
	hasNVIDIAGPU := false
	if c.System != nil {
		hasCUDA = c.System.HasCUDA
		cudaHome = c.System.CUDAHome
		hasNVIDIAGPU = c.System.HasNVIDIAGPU
	}

	return map[string]any{
		"Home":            home,
		"Name":            c.Name,
		"Email":           c.Email,
		"GithubUser":      c.GithubUser,
		"Timezone":        c.Timezone,
		"Hostname":        hostname,
		"IsDarwin":        isDarwin,
		"Editor":          EditorCommand(c.Modules.Shell.Editor, isDarwin),
		"EnableWorkspace": c.Modules.Workspace.Enabled,
		"EnableAI":        c.Modules.AI.Enabled,
		"EnableContainer": c.Modules.Container.Enabled,
		"WorkspacePath":   c.Modules.Workspace.Path,
		"VaultPath":       c.VaultPath(home),
		"CloudSymlink":    c.Modules.Workspace.GdriveSymlink,
		"SSHKeyName":      c.Modules.SSH.KeyName,
		"CoauthorGuard":   c.Modules.Git.CoauthorGuard,
		"HasCUDA":         hasCUDA,
		"CUDAHome":        cudaHome,
		"HasNVIDIAGPU":    hasNVIDIAGPU,
	}
}

// AIToolingConfig is the portable, explicit selection. A nil pointer on AIConfig
// preserves legacy behavior; a non-nil empty selection manages no agents.
type AIToolingConfig struct {
	Pins    map[string]string      `yaml:"pins,omitempty" json:"pins,omitempty"`
	Agents  []string               `yaml:"agents" json:"agents"`
	Tools   []string               `yaml:"tools,omitempty" json:"tools,omitempty"`
	Skills  []string               `yaml:"skills,omitempty" json:"skills,omitempty"`
	Updates AIUpdateScheduleConfig `yaml:"updates,omitempty" json:"updates,omitempty"`
}

type AIUpdateScheduleConfig struct {
	Enabled bool `yaml:"enabled,omitempty" json:"enabled"`
}

// Clone keeps merged user configuration independent of its persisted slices.
func (c *AIToolingConfig) Clone() *AIToolingConfig {
	if c == nil {
		return nil
	}
	out := *c
	out.Pins = make(map[string]string, len(c.Pins))
	for k, v := range c.Pins {
		out.Pins[k] = v
	}
	out.Agents = append([]string{}, c.Agents...)
	out.Tools = withoutRetiredTools(c.Tools)
	out.Skills = append([]string{}, c.Skills...)
	delete(out.Pins, "claude-mem")
	return &out
}

// retiredTools are add-on IDs a saved selection may still name after the add-on
// left the catalog. A selection that carries one is read without it, so an
// upgrade never turns a working `dot apply` into a validation failure. claude-mem
// was replaced by amem (dotfiles#255); its removal from the machine is the
// person's `dot ai memory retire-claude-mem`.
var retiredTools = map[string]bool{"claude-mem": true}

func withoutRetiredTools(tools []string) []string {
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		if !retiredTools[tool] {
			out = append(out, tool)
		}
	}
	return out
}
