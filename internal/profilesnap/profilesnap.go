// Package profilesnap snapshots user-level dotfiles profile artifacts
// (`~/.config/dotfiles/` state, optional secret identities, extracted cask
// install/backup lists) into a host-scoped, version-directory layout under a
// shared backup root.
//
// Layout:
//
//	<Root>/profiles/<Hostname>/<Version>/
//	    meta.yaml            — snapshot metadata
//	    config.yaml          — copy of ~/.config/dotfiles/config.yaml
//	    apps/install.yaml    — extracted install list (Casks + CasksExtra)
//	    apps/backup.yaml     — extracted backup scope (BackupApps)
//	    secrets/             — optional copy of ~/.ssh/age_key* (omitted unless IncludeSecrets)
//	<Root>/profiles/<Hostname>/latest.txt  — contains the current version id
package profilesnap

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/snapstore"
)

// Meta records the human-readable provenance of a snapshot.
type Meta struct {
	Version        string    `yaml:"version"`
	Tag            string    `yaml:"tag,omitempty"`
	Hostname       string    `yaml:"hostname"`
	CreatedAt      time.Time `yaml:"created_at"`
	IncludeSecrets bool      `yaml:"include_secrets"`
	StateFile      string    `yaml:"state_file,omitempty"`
	User           string    `yaml:"user,omitempty"`
}

// Snapshot is one concrete version in the archive.
type Snapshot struct {
	Version    string
	Tag        string
	CreatedAt  time.Time
	Path       string // absolute path to the version directory
	IsLatest   bool
	WithSecret bool

	// Backup outcome.
	SecretsCopied int // number of age_key* files captured

	// Restore outcome.
	RestoredState    bool   // config.yaml was copied back to StatePath
	RestoredSecrets  int    // secret files ensured under SecretsDir
	PreRestoreBackup string // dir holding pre-overwrite copies, "" if none taken
}

// Engine executes backup/restore/list operations scoped to a host.
type Engine struct {
	Runner     *exec.Runner
	HomeDir    string
	Root       string // shared backup root (same as appsettings.Engine.Root)
	Hostname   string
	User       string
	StatePath  string // typically config.StatePath()
	SecretsDir string // typically ~/.ssh
}

// HostRoot returns <root>/profiles/<host>.
func (e *Engine) HostRoot() string {
	return filepath.Join(e.Root, "profiles", e.Hostname)
}

// VersionPath returns the absolute path for a given version id.
func (e *Engine) VersionPath(version string) string {
	return filepath.Join(e.HostRoot(), version)
}

// LatestPointerPath returns <host-root>/latest.txt.
func (e *Engine) LatestPointerPath() string {
	return filepath.Join(e.HostRoot(), "latest.txt")
}

// NewVersion produces a filesystem-safe UTC version id.
func NewVersion(t time.Time) string {
	return snapstore.NewVersion(t)
}

// uniqueVersion returns an unused version id near t; suffix "-N" is appended
// when an earlier snapshot already occupies the natural slot. Protects against
// back-to-back calls within the same 1s window.
func (e *Engine) uniqueVersion(t time.Time) (string, error) {
	version, err := snapstore.UniqueVersion(t, e.VersionPath)
	if err != nil {
		return "", fmt.Errorf("%w under %s", err, e.HostRoot())
	}
	return version, nil
}

// BackupOptions tune Backup behavior.
type BackupOptions struct {
	Tag            string
	IncludeSecrets bool
}

// Backup writes a new version directory and advances latest.txt.
// Returns the created Snapshot. A partially written version directory is
// removed when any step before meta.yaml fails, so List/latest never see
// orphan snapshots.
func (e *Engine) Backup(opts BackupOptions) (*Snapshot, error) {
	version, err := e.uniqueVersion(time.Now())
	if err != nil {
		return nil, err
	}
	dest := e.VersionPath(version)
	if err := e.Runner.MkdirAll(dest, 0o755); err != nil {
		return nil, fmt.Errorf("create version dir: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = e.Runner.RemoveAll(dest)
		}
	}()

	// 1. state file
	if _, err := os.Stat(e.StatePath); err == nil {
		if err := copyFile(e.Runner, e.StatePath, filepath.Join(dest, "config.yaml")); err != nil {
			return nil, fmt.Errorf("copy state: %w", err)
		}

		// Extract install/backup lists into readable siblings.
		if state, err := config.LoadStateFrom(e.StatePath); err == nil {
			if err := e.writeInstallList(dest, state); err != nil {
				e.Runner.Logger.Warn("write install list", "err", err)
			}
			if err := e.writeBackupList(dest, state); err != nil {
				e.Runner.Logger.Warn("write backup list", "err", err)
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat state: %w", err)
	}

	// 2. secrets (age identities)
	secretsCopied := 0
	if opts.IncludeSecrets {
		secretsCopied, err = e.copySecrets(filepath.Join(dest, "secrets"))
		if err != nil {
			return nil, fmt.Errorf("copy secrets: %w", err)
		}
	}
	withSecrets := opts.IncludeSecrets && secretsCopied > 0

	// 3. meta — the snapshot is committed once meta.yaml exists.
	meta := Meta{
		Version:        version,
		Tag:            opts.Tag,
		Hostname:       e.Hostname,
		CreatedAt:      time.Now().UTC(),
		IncludeSecrets: withSecrets,
		StateFile:      e.StatePath,
		User:           e.User,
	}
	if err := writeMeta(e.Runner, filepath.Join(dest, "meta.yaml"), meta); err != nil {
		return nil, fmt.Errorf("write meta: %w", err)
	}
	committed = true

	// 4. latest pointer — failure here keeps the (complete) snapshot;
	// readLatest falls back to the newest directory.
	if err := e.Runner.WriteFile(e.LatestPointerPath(), []byte(version+"\n"), 0o644); err != nil {
		return nil, fmt.Errorf("write latest.txt: %w", err)
	}

	return &Snapshot{
		Version:       version,
		Tag:           opts.Tag,
		CreatedAt:     meta.CreatedAt,
		Path:          dest,
		IsLatest:      true,
		WithSecret:    withSecrets,
		SecretsCopied: secretsCopied,
	}, nil
}

// RestoreOptions selects which snapshot and which artifacts are restored.
type RestoreOptions struct {
	Version        string // "" means use latest.txt
	IncludeSecrets bool   // copy back secrets/* when present
	IncludeState   bool   // copy back config.yaml (defaults to true via caller)
}

// Restore copies state + optional secrets from a snapshot back to $HOME.
// Existing files that would be overwritten are first copied into a
// pre-restore backup directory under
// <HomeDir>/.local/share/dotfiles/backup/profile-pre-restore/<ts>/ —
// deliberately outside both SecretsDir (so copySecrets never sweeps backups
// into future snapshots) and HostRoot (so List/Prune never mistake them for
// versions). Returns the Snapshot that was applied.
func (e *Engine) Restore(opts RestoreOptions) (*Snapshot, error) {
	version := opts.Version
	if version == "" {
		v, err := e.readLatest()
		if err != nil {
			return nil, err
		}
		version = v
	}
	src := e.VersionPath(version)
	if _, err := os.Stat(src); err != nil {
		return nil, fmt.Errorf("snapshot %s not found at %s", version, src)
	}

	meta, _ := readMeta(filepath.Join(src, "meta.yaml"))

	preRoot := e.preRestoreDir(time.Now())
	preUsed := false
	backupExisting := func(live, sub string) error {
		if _, err := os.Stat(live); err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("stat %s: %w", live, err)
		}
		dst := filepath.Join(preRoot, sub)
		if err := e.Runner.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		if err := copyFile(e.Runner, live, dst); err != nil {
			return fmt.Errorf("pre-restore backup of %s: %w", live, err)
		}
		preUsed = true
		return nil
	}

	restoredState := false
	if opts.IncludeState {
		srcCfg := filepath.Join(src, "config.yaml")
		if _, err := os.Stat(srcCfg); err != nil {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("snapshot %s contains no config.yaml; use --no-state to skip state restore", version)
			}
			return nil, fmt.Errorf("stat snapshot config: %w", err)
		}
		if err := backupExisting(e.StatePath, "config.yaml"); err != nil {
			return nil, err
		}
		if err := e.Runner.MkdirAll(filepath.Dir(e.StatePath), 0o755); err != nil {
			return nil, err
		}
		if err := copyFile(e.Runner, srcCfg, e.StatePath); err != nil {
			return nil, fmt.Errorf("restore state: %w", err)
		}
		restoredState = true
	}

	restoredSecrets := 0
	if opts.IncludeSecrets {
		n, err := e.restoreSecrets(filepath.Join(src, "secrets"), backupExisting)
		if err != nil {
			return nil, err
		}
		restoredSecrets = n
	}

	latest, _ := e.readLatest()
	snap := &Snapshot{
		Version:         version,
		Path:            src,
		IsLatest:        version == latest,
		WithSecret:      meta != nil && meta.IncludeSecrets,
		RestoredState:   restoredState,
		RestoredSecrets: restoredSecrets,
	}
	if preUsed && !e.Runner.DryRun {
		snap.PreRestoreBackup = preRoot
	}
	if meta != nil {
		snap.Tag = meta.Tag
		snap.CreatedAt = meta.CreatedAt
	}
	return snap, nil
}

// preRestoreDir picks an unused timestamped directory for pre-overwrite
// copies. The directory itself is created lazily by the first backup.
func (e *Engine) preRestoreDir(t time.Time) string {
	return snapstore.PreRestoreDir(e.HomeDir, []string{"profile-pre-restore"}, t)
}

// restoreSecrets copies each snapshot secret file back into SecretsDir,
// backing up differing existing files first and forcing restrictive modes
// (0700 dir, 0600 files) regardless of the modes stored in the snapshot —
// cloud backends may have normalized them. Byte-identical files are left in
// place (permissions still healed). Returns the number of files ensured.
func (e *Engine) restoreSecrets(secSrc string, backupExisting func(live, sub string) error) (int, error) {
	entries, err := os.ReadDir(secSrc)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read snapshot secrets: %w", err)
	}
	restored := 0
	for _, en := range entries {
		if en.IsDir() {
			continue
		}
		name := en.Name()
		sp := filepath.Join(secSrc, name)
		dp := filepath.Join(e.SecretsDir, name)

		if restored == 0 {
			if err := e.Runner.MkdirAll(e.SecretsDir, 0o700); err != nil {
				return restored, err
			}
			if !e.Runner.DryRun {
				if err := os.Chmod(e.SecretsDir, 0o700); err != nil {
					return restored, fmt.Errorf("restoring permissions on %s: %w", e.SecretsDir, err)
				}
			}
		}

		newData, err := os.ReadFile(sp)
		if err != nil {
			return restored, fmt.Errorf("read snapshot secret %s: %w", name, err)
		}
		if oldData, err := os.ReadFile(dp); err == nil {
			if bytes.Equal(oldData, newData) {
				if !e.Runner.DryRun {
					if err := os.Chmod(dp, 0o600); err != nil {
						return restored, fmt.Errorf("restoring permissions on %s: %w", dp, err)
					}
				}
				restored++
				continue
			}
			if err := backupExisting(dp, filepath.Join("secrets", name)); err != nil {
				return restored, err
			}
		} else if !os.IsNotExist(err) {
			return restored, fmt.Errorf("read existing %s: %w", dp, err)
		}

		if err := copyFile(e.Runner, sp, dp); err != nil {
			return restored, fmt.Errorf("restore secret %s: %w", name, err)
		}
		if !e.Runner.DryRun {
			if err := os.Chmod(dp, 0o600); err != nil {
				return restored, fmt.Errorf("restoring permissions on %s: %w", dp, err)
			}
		}
		restored++
	}
	return restored, nil
}

// List enumerates snapshots for the host, newest-first.
func (e *Engine) List() ([]Snapshot, error) {
	return e.list(false)
}

// list optionally skips the latest-pointer lookup: readLatest's fallback
// enumerates directories via list(true), so going through List() there
// would recurse forever whenever latest.txt is missing.
func (e *Engine) list(withoutLatest bool) ([]Snapshot, error) {
	entries, err := os.ReadDir(e.HostRoot())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	latest := ""
	if !withoutLatest {
		latest, _ = e.readLatest()
	}
	var out []Snapshot
	for _, en := range entries {
		if !en.IsDir() {
			continue
		}
		v := en.Name()
		meta, err := readMeta(filepath.Join(e.HostRoot(), v, "meta.yaml"))
		if err != nil || meta == nil {
			// Not a committed snapshot (partial dir from an old failed
			// backup, or an unrelated directory) — never list, never let
			// the latest fallback or Prune act on it.
			continue
		}
		snap := Snapshot{Version: v, Path: filepath.Join(e.HostRoot(), v), IsLatest: v == latest}
		snap.Tag = meta.Tag
		snap.CreatedAt = meta.CreatedAt
		snap.WithSecret = meta.IncludeSecrets
		out = append(out, snap)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Version > out[j].Version
	})
	return out, nil
}

// Prune removes older snapshots, keeping the newest `keep` (including latest).
func (e *Engine) Prune(keep int) ([]string, error) {
	if keep < 1 {
		keep = 1
	}
	all, err := e.List()
	if err != nil {
		return nil, err
	}
	if len(all) <= keep {
		return nil, nil
	}
	latest, _ := e.ResolveLatest()
	infos := make([]snapstore.SnapshotInfo, 0, len(all))
	for _, s := range all {
		infos = append(infos, snapstore.SnapshotInfo{Version: s.Version, Path: s.Path})
	}
	return snapstore.Prune(e.Runner, keep, infos, latest)
}

// ResolveLatest returns the latest snapshot id. Missing, empty, or dangling
// latest.txt pointers fall back to the newest committed snapshot.
func (e *Engine) ResolveLatest() (string, error) {
	return snapstore.ResolveLatest(e.LatestPointerPath(), e.HostRoot(), e.VersionPath, func() ([]string, error) {
		all, err := e.list(true)
		if err != nil {
			return nil, err
		}
		versions := make([]string, 0, len(all))
		for _, s := range all {
			versions = append(versions, s.Version)
		}
		return versions, nil
	})
}

// --- internals ---

func (e *Engine) readLatest() (string, error) {
	return e.ResolveLatest()
}

// copySecrets captures ~/.ssh/age_key* into the snapshot and returns how
// many files it copied. The secrets/ directory is only created when at
// least one key exists, so meta.IncludeSecrets stays truthful.
func (e *Engine) copySecrets(destDir string) (int, error) {
	if _, err := os.Stat(e.SecretsDir); err != nil {
		return 0, nil // no ~/.ssh yet — nothing to copy
	}
	entries, err := os.ReadDir(e.SecretsDir)
	if err != nil {
		return 0, err
	}
	copied := 0
	for _, en := range entries {
		name := en.Name()
		if !strings.HasPrefix(name, "age_key") {
			continue
		}
		if copied == 0 {
			if err := e.Runner.MkdirAll(destDir, 0o700); err != nil {
				return 0, err
			}
		}
		if err := copyFile(e.Runner, filepath.Join(e.SecretsDir, name), filepath.Join(destDir, name)); err != nil {
			return copied, err
		}
		copied++
	}
	return copied, nil
}

func (e *Engine) writeInstallList(dest string, state *config.UserState) error {
	installDir := filepath.Join(dest, "apps")
	if err := e.Runner.MkdirAll(installDir, 0o755); err != nil {
		return err
	}
	doc := map[string]any{
		"casks":       state.Modules.MacApps.Casks,
		"casks_extra": state.Modules.MacApps.CasksExtra,
	}
	data, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	return e.Runner.WriteFile(filepath.Join(installDir, "install.yaml"), data, 0o644)
}

func (e *Engine) writeBackupList(dest string, state *config.UserState) error {
	installDir := filepath.Join(dest, "apps")
	if err := e.Runner.MkdirAll(installDir, 0o755); err != nil {
		return err
	}
	doc := map[string]any{
		"backup_apps": state.Modules.MacApps.BackupApps,
		"backup_root": state.Modules.MacApps.BackupRoot,
	}
	data, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	return e.Runner.WriteFile(filepath.Join(installDir, "backup.yaml"), data, 0o644)
}

func writeMeta(runner *exec.Runner, path string, m Meta) error {
	return snapstore.WriteYAML(runner, path, m)
}

func readMeta(path string) (*Meta, error) {
	return snapstore.ReadYAML[Meta](path)
}

func copyFile(runner *exec.Runner, src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	_, err = snapstore.CopyFile(runner, src, dst, info.Mode())
	return err
}

// ListHosts enumerates the hostnames that have profile snapshots under
// root, sorted. Returns (nil, nil) when the tree doesn't exist yet.
func ListHosts(root string) ([]string, error) {
	return snapstore.ListHosts(root, "profiles")
}
