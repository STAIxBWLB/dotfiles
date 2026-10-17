// Package appsettings implements host-scoped backup and restore of macOS
// application settings (plists, Application Support subtrees, sandbox
// containers). The manifest mapping tokens to their Library-relative paths
// is embedded from manifest.yaml.
package appsettings

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/snapstore"
)

//go:embed manifest.yaml
var manifestYAML []byte

// PathEntry describes one Library-relative path belonging to an app.
type PathEntry struct {
	Type string `yaml:"type"`
	Path string `yaml:"path"`
}

// AppEntry lists all backup paths for a single cask token.
type AppEntry struct {
	Token string      `yaml:"token"`
	Paths []PathEntry `yaml:"paths"`
}

// Manifest is the root of manifest.yaml.
type Manifest struct {
	Apps []AppEntry `yaml:"apps"`
}

// LoadManifest parses the embedded manifest.
func LoadManifest() (*Manifest, error) {
	var m Manifest
	if err := yaml.Unmarshal(manifestYAML, &m); err != nil {
		return nil, fmt.Errorf("parse manifest.yaml: %w", err)
	}
	return &m, nil
}

// Tokens returns every app token listed in the manifest (manifest order).
func (m *Manifest) Tokens() []string {
	out := make([]string, 0, len(m.Apps))
	for _, a := range m.Apps {
		out = append(out, a.Token)
	}
	return out
}

// App returns the entry for a token, or nil if unknown.
func (m *Manifest) App(token string) *AppEntry {
	for i, a := range m.Apps {
		if a.Token == token {
			return &m.Apps[i]
		}
	}
	return nil
}

// Engine executes Backup/Restore/Status operations scoped to a host directory.
//
// Archive layout (rooted at Root):
//
//	<Root>/app-settings/<Hostname>/<app-token>/<Library-relative-path>
//
// The "app-settings" prefix keeps app-setting snapshots sibling to the
// profile snapshots produced by `dot profile backup`.
type Engine struct {
	Runner   *exec.Runner
	HomeDir  string // user home (Library lives here)
	Root     string // shared backup root
	Hostname string
	Manifest *Manifest
}

// AppSettingsRoot is the subtree that holds per-app snapshots for every host.
func (e *Engine) AppSettingsRoot() string {
	return filepath.Join(e.Root, "app-settings")
}

// HostRoot is the per-host subtree inside AppSettingsRoot.
func (e *Engine) HostRoot() string {
	return filepath.Join(e.AppSettingsRoot(), e.Hostname)
}

// libraryPath returns the absolute source path for a manifest entry.
func (e *Engine) libraryPath(rel string) string {
	return filepath.Join(e.HomeDir, "Library", rel)
}

// archivePath returns the destination inside the backup archive.
func (e *Engine) archivePath(token, rel string) string {
	return filepath.Join(e.HostRoot(), token, rel)
}

// --- Summary types ---

// AppSummary captures the outcome of Backup/Restore for a single app.
type AppSummary struct {
	Token   string
	Copied  int // paths fully processed
	Missing int // paths that had no source (informational, not an error)
	Failed  int // paths whose copy errored — the operation did not complete
	Files   int // individual files copied (for directory walks)
	Bytes   int64
}

// Summary aggregates per-app results.
type Summary struct {
	Apps          []AppSummary
	Files         int
	Bytes         int64
	Failed        int    // total failed paths across apps
	PreBackupPath string // restore only: dir holding pre-overwrite copies, "" if none
}

// AppStatus is produced by Status(): counts of available/backed-up paths.
type AppStatus struct {
	Token       string
	PresentLive int // manifest paths that exist under $HOME/Library
	TotalLive   int // manifest path count
	PresentBak  int // manifest paths that exist inside the archive
	TotalBak    int
}

// --- Exclusion rules (shared between backup and restore) ---

var excludedSegments = []string{
	"Caches",
	"Cache",
	"Code Cache",
	"GPUCache",
	"IndexedDB",
	"Local Storage",
	"Logs",
	"logs",
	"blob_storage",
	"Crashpad",
	"Service Worker",
}

var excludedSuffixes = []string{
	".log",
	".lock",
}

var excludedExact = []string{
	".DS_Store",
	// macOS keeps this container-manager bookkeeping file unreadable even with
	// Full Disk Access, so including it fails the whole Containers copy. It is
	// regenerated on app launch and its contents are host-specific, so it is
	// nothing a restore should carry to another machine.
	".com.apple.containermanagerd.metadata.plist",
}

func isExcluded(rel string) bool {
	parts := strings.Split(rel, string(os.PathSeparator))
	for _, p := range parts {
		if strings.Contains(p, "Cache") {
			return true
		}
		for _, seg := range excludedSegments {
			if p == seg {
				return true
			}
		}
		for _, ex := range excludedExact {
			if p == ex {
				return true
			}
		}
		if strings.HasPrefix(p, "Singleton") {
			return true
		}
	}
	for _, suf := range excludedSuffixes {
		if strings.HasSuffix(rel, suf) {
			return true
		}
	}
	return false
}

// --- Backup ---

// BackupOptions selects what Backup copies. An empty Tokens list means every
// manifest app.
type BackupOptions struct {
	Tokens []string
}

// Backup copies the listed apps (or all manifest apps when Tokens is empty)
// into the host-scoped archive. Missing sources are reported but do not
// abort; copy errors are counted in Failed. Each app is staged into
// <HostRoot>/.staging/ and swapped into place only when every attempted copy
// succeeded, so a failed backup never corrupts the previous (and only)
// archived copy. Paths whose live source is gone are seeded from the
// existing archive so the swap preserves them.
func (e *Engine) Backup(ctx context.Context, opts BackupOptions) (*Summary, error) {
	targets := e.selectTokens(opts.Tokens)
	if len(targets) == 0 {
		return nil, fmt.Errorf("no apps selected for backup")
	}

	if err := e.Runner.MkdirAll(e.HostRoot(), 0o755); err != nil {
		return nil, fmt.Errorf("create host root %s: %w", e.HostRoot(), err)
	}
	if !e.Runner.DryRun {
		e.recoverStaging()
	}

	sum := &Summary{}
	for _, token := range targets {
		app := e.Manifest.App(token)
		if app == nil {
			continue
		}
		as := e.backupApp(token, app)
		sum.Apps = append(sum.Apps, as)
		sum.Files += as.Files
		sum.Bytes += as.Bytes
		sum.Failed += as.Failed
	}
	return sum, nil
}

// backupApp stages one app's paths and commits the staging dir atomically.
// In dry-run mode it copies straight at the final destinations (all copy
// primitives are no-ops there) so counters still reflect the plan.
func (e *Engine) backupApp(token string, app *AppEntry) AppSummary {
	as := AppSummary{Token: token}

	// Defense-in-depth: a token becomes a directory segment under HostRoot
	// and feeds os.RemoveAll/os.Rename. Tokens can originate from saved
	// state or display-name discovery, so refuse any that would escape the
	// host tree before touching the filesystem.
	if !safeToken(token) {
		e.Runner.Logger.Warn("backup: refusing unsafe app token", "token", token)
		as.Failed = 1
		return as
	}

	destFor := func(rel string) string { return e.archivePath(token, rel) }
	staging := ""
	if !e.Runner.DryRun {
		staging = filepath.Join(e.HostRoot(), ".staging", fmt.Sprintf("%s.%d", token, os.Getpid()))
		_ = os.RemoveAll(staging)
		destFor = func(rel string) string { return filepath.Join(staging, rel) }
	}

	var failedPaths []string
	for _, p := range app.Paths {
		src := e.libraryPath(p.Path)
		fi, err := os.Lstat(src)
		if err != nil {
			// Live source missing: carry the archived copy (if any) into the
			// staging tree so the swap never wipes the only remaining copy.
			if staging != "" {
				archived := e.archivePath(token, p.Path)
				if afi, aerr := os.Lstat(archived); aerr == nil {
					if _, _, serr := e.copyTree(archived, destFor(p.Path), afi); serr != nil {
						e.Runner.Logger.Warn("backup: preserving archived copy failed", "app", token, "path", p.Path, "err", serr)
						as.Failed++
						failedPaths = append(failedPaths, p.Path)
						continue
					}
				}
			}
			as.Missing++
			continue
		}
		files, bytes, err := e.copyTree(src, destFor(p.Path), fi)
		if err != nil {
			e.Runner.Logger.Warn("backup copy failed", "app", token, "src", src, "err", err)
			as.Failed++
			failedPaths = append(failedPaths, p.Path)
			continue
		}
		as.Copied++
		as.Files += files
		as.Bytes += bytes
	}

	if staging == "" {
		return as
	}
	if as.Failed > 0 {
		// Partial failure: keep the paths that DID stage by seeding each
		// failed path's previous archive copy into the staging tree, so the
		// atomic swap refreshes the readable paths without wiping the only
		// copy of an unreadable one (e.g. a TCC-protected Containers path
		// that fails on every run). If any failed path can't be seeded from
		// the archive, fall back to discarding the whole staging tree — a
		// partial commit would delete that path's sole archived copy.
		if !e.seedFailedPaths(token, staging, failedPaths) {
			_ = os.RemoveAll(staging)
			return as
		}
	}
	if err := e.commitStaging(token, staging); err != nil {
		e.Runner.Logger.Warn("backup commit failed; previous archive kept", "app", token, "err", err)
		_ = os.RemoveAll(staging)
		as.Failed++
	}
	return as
}

// seedFailedPaths copies each failed path's existing archive copy into the
// staging tree so the commit preserves it. Returns false (caller must
// discard staging) when any failed path has an archive copy that can't be
// seeded — committing then would lose that path's only copy.
func (e *Engine) seedFailedPaths(token, staging string, failedPaths []string) bool {
	for _, rel := range failedPaths {
		staged := filepath.Join(staging, rel)
		// copyTree can fail mid-walk and leave a partial staged subtree;
		// clear it so seeding never commits a mixed new/old tree.
		_ = os.RemoveAll(staged)
		archived := e.archivePath(token, rel)
		afi, err := os.Lstat(archived)
		if err != nil {
			// No prior archive copy — nothing to lose, nothing to seed.
			continue
		}
		if _, _, serr := e.copyTree(archived, staged, afi); serr != nil {
			e.Runner.Logger.Warn("backup: seeding previous archive copy failed", "app", token, "path", rel, "err", serr)
			return false
		}
	}
	return true
}

// commitStaging swaps the staged tree into place via a .prev rename so a
// crash at any point leaves either the old or the new archive recoverable
// (see recoverStaging). A staging dir that was never created (every path
// missing with no prior archive) is a no-op.
func (e *Engine) commitStaging(token, staging string) error {
	if _, err := os.Lstat(staging); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	final := filepath.Join(e.HostRoot(), token)
	prev := final + ".prev"
	_ = os.RemoveAll(prev)
	hadPrev := false
	if _, err := os.Lstat(final); err == nil {
		if err := os.Rename(final, prev); err != nil {
			return err
		}
		hadPrev = true
	}
	if err := os.Rename(staging, final); err != nil {
		if hadPrev {
			_ = os.Rename(prev, final)
		}
		return err
	}
	_ = os.RemoveAll(prev)
	return nil
}

// recoverStaging repairs the archive after an interrupted commit: a stray
// <token>.prev with no <token> dir is renamed back, leftover .prev and
// .staging trees are removed.
func (e *Engine) recoverStaging() {
	entries, err := os.ReadDir(e.HostRoot())
	if err != nil {
		return
	}
	for _, en := range entries {
		name := en.Name()
		if !en.IsDir() || !strings.HasSuffix(name, ".prev") {
			continue
		}
		prev := filepath.Join(e.HostRoot(), name)
		final := filepath.Join(e.HostRoot(), strings.TrimSuffix(name, ".prev"))
		if _, err := os.Lstat(final); os.IsNotExist(err) {
			_ = os.Rename(prev, final)
		} else {
			_ = os.RemoveAll(prev)
		}
	}
	_ = os.RemoveAll(filepath.Join(e.HostRoot(), ".staging"))
}

// --- Restore ---

// RestoreOptions selects what Restore copies back. An empty Tokens list
// means every manifest app.
type RestoreOptions struct {
	Tokens []string
}

// Restore copies from the host-scoped archive back to $HOME/Library.
// Missing sources (not yet backed up) are skipped; copy errors are counted
// in Failed. Existing live paths are snapshotted to
// <HomeDir>/.local/share/dotfiles/backup/app-settings/<ts>/<token>/ before
// being overwritten; the location is reported via Summary.PreBackupPath.
// A path whose pre-restore snapshot fails is left untouched.
func (e *Engine) Restore(ctx context.Context, opts RestoreOptions) (*Summary, error) {
	if _, err := os.Stat(e.HostRoot()); err != nil {
		return nil, fmt.Errorf("no backup at %s (hostname=%s)", e.HostRoot(), e.Hostname)
	}
	if !e.Runner.DryRun {
		e.recoverStaging()
	}
	targets := e.selectTokens(opts.Tokens)
	if len(targets) == 0 {
		return nil, fmt.Errorf("no apps selected for restore")
	}

	preRoot := e.preRestoreDir(time.Now())
	preUsed := false
	sum := &Summary{}
	for _, token := range targets {
		app := e.Manifest.App(token)
		if app == nil {
			continue
		}
		// Defense-in-depth: token feeds archivePath + the preRoot snapshot
		// path; refuse traversal before any read/copy.
		if !safeToken(token) {
			e.Runner.Logger.Warn("restore: refusing unsafe app token", "token", token)
			sum.Apps = append(sum.Apps, AppSummary{Token: token, Failed: 1})
			sum.Failed++
			continue
		}
		as := AppSummary{Token: token}
		for _, p := range app.Paths {
			src := e.archivePath(token, p.Path)
			dst := e.libraryPath(p.Path)
			fi, err := os.Lstat(src)
			if err != nil {
				as.Missing++
				continue
			}
			if lfi, lerr := os.Lstat(dst); lerr == nil {
				if _, _, berr := e.copyTree(dst, filepath.Join(preRoot, token, p.Path), lfi); berr != nil {
					e.Runner.Logger.Warn("pre-restore snapshot failed; leaving live path untouched", "app", token, "path", dst, "err", berr)
					as.Failed++
					continue
				}
				preUsed = true
			}
			files, bytes, err := e.copyTree(src, dst, fi)
			if err != nil {
				e.Runner.Logger.Warn("restore copy failed", "app", token, "src", src, "err", err)
				as.Failed++
				continue
			}
			as.Copied++
			as.Files += files
			as.Bytes += bytes
		}
		sum.Apps = append(sum.Apps, as)
		sum.Files += as.Files
		sum.Bytes += as.Bytes
		sum.Failed += as.Failed
	}
	if preUsed && !e.Runner.DryRun {
		sum.PreBackupPath = preRoot
	}
	return sum, nil
}

// preRestoreDir picks an unused timestamped directory for pre-overwrite
// copies, outside the archive tree. Created lazily by the first copy.
func (e *Engine) preRestoreDir(t time.Time) string {
	return snapstore.PreRestoreDir(e.HomeDir, []string{"app-settings"}, t)
}

// FlushCFPrefsd asks cfprefsd to release cached plists so the target apps
// pick up the restored files on next launch. Best-effort, errors logged only.
func (e *Engine) FlushCFPrefsd(ctx context.Context) {
	if _, err := e.Runner.Run(ctx, "killall", "cfprefsd"); err != nil {
		e.Runner.Logger.Warn("killall cfprefsd", "err", err)
	}
}

// --- Status ---

// Status reports live/backup presence counts for each token (or all when tokens is empty).
func (e *Engine) Status(tokens []string) []AppStatus {
	targets := e.selectTokens(tokens)
	out := make([]AppStatus, 0, len(targets))
	for _, token := range targets {
		app := e.Manifest.App(token)
		if app == nil {
			continue
		}
		st := AppStatus{Token: token, TotalLive: len(app.Paths), TotalBak: len(app.Paths)}
		for _, p := range app.Paths {
			if _, err := os.Lstat(e.libraryPath(p.Path)); err == nil {
				st.PresentLive++
			}
			if _, err := os.Lstat(e.archivePath(token, p.Path)); err == nil {
				st.PresentBak++
			}
		}
		out = append(out, st)
	}
	return out
}

// --- Archived (non-manifest) apps ---

// AdoptArchivedApps synthesizes manifest entries for token directories that
// exist in the host archive but not in the embedded manifest — apps captured
// via DiscoverApp on a previous run (e.g. "Moom Classic"). Without this,
// selectTokens silently drops them and their archived settings can never be
// restored. Manifest entries keep precedence; returns the adopted tokens.
func (e *Engine) AdoptArchivedApps() []string {
	entries, err := os.ReadDir(e.HostRoot())
	if err != nil {
		return nil
	}
	var adopted []string
	for _, en := range entries {
		name := en.Name()
		if !en.IsDir() || name == ".staging" || strings.HasSuffix(name, ".prev") {
			continue
		}
		if e.Manifest.App(name) != nil {
			continue
		}
		if entry := e.archivedAppEntry(name); entry != nil {
			e.Manifest.Apps = append(e.Manifest.Apps, *entry)
			adopted = append(adopted, name)
		}
	}
	sort.Strings(adopted)
	return adopted
}

// archivedAppEntry rebuilds an AppEntry from the archive layout using the
// manifest's depth-2 convention: top-level files map to file entries
// (Preferences/<name>.plist) and each immediate child of a top-level dir
// maps to a directory root (Application Support/<dir>, Containers/<id>,
// Group Containers/<id>) that copyTree recurses into.
func (e *Engine) archivedAppEntry(token string) *AppEntry {
	tokenDir := filepath.Join(e.HostRoot(), token)
	top, err := os.ReadDir(tokenDir)
	if err != nil {
		return nil
	}
	typeFor := func(category string) string {
		switch category {
		case "Preferences":
			return "pref"
		case "Containers":
			return "container"
		case "Group Containers":
			return "group"
		default:
			return "support"
		}
	}
	var paths []PathEntry
	for _, t := range top {
		name := t.Name()
		if name == ".DS_Store" {
			continue
		}
		if !t.IsDir() {
			paths = append(paths, PathEntry{Type: typeFor(""), Path: name})
			continue
		}
		children, err := os.ReadDir(filepath.Join(tokenDir, name))
		if err != nil {
			continue
		}
		for _, c := range children {
			if c.Name() == ".DS_Store" {
				continue
			}
			paths = append(paths, PathEntry{Type: typeFor(name), Path: filepath.Join(name, c.Name())})
		}
	}
	if len(paths) == 0 {
		return nil
	}
	return &AppEntry{Token: token, Paths: paths}
}

// --- Last-backup stamp ---

// BackupStamp records when (and with what tag) the unversioned app-settings
// tree was last written, so multi-domain tooling can correlate it with the
// versioned profile/ai snapshots.
type BackupStamp struct {
	Tag       string    `yaml:"tag,omitempty"`
	CreatedAt time.Time `yaml:"created_at"`
	Tokens    []string  `yaml:"tokens,omitempty"`
	Files     int       `yaml:"files"`
}

// LastBackupStampPath returns <host-root>/last-backup.yaml.
func (e *Engine) LastBackupStampPath() string {
	return filepath.Join(e.HostRoot(), "last-backup.yaml")
}

// WriteLastBackupStamp persists the stamp; honors dry-run via the Runner.
func (e *Engine) WriteLastBackupStamp(stamp BackupStamp) error {
	data, err := yaml.Marshal(stamp)
	if err != nil {
		return err
	}
	return e.Runner.WriteFile(e.LastBackupStampPath(), data, 0o644)
}

// ReadLastBackupStamp loads the stamp; returns (nil, nil) when absent.
func (e *Engine) ReadLastBackupStamp() (*BackupStamp, error) {
	data, err := os.ReadFile(e.LastBackupStampPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var stamp BackupStamp
	if err := yaml.Unmarshal(data, &stamp); err != nil {
		return nil, err
	}
	return &stamp, nil
}

// --- Helpers ---

// safeToken reports whether an app token is usable as a single directory
// segment under HostRoot without escaping it: non-empty, not "."/"..", and
// free of path separators. Tokens can come from saved state or display-name
// discovery, and they feed os.RemoveAll/os.Rename, so unsafe ones must be
// refused before any filesystem operation.
func safeToken(token string) bool {
	if token == "" || token == "." || token == ".." {
		return false
	}
	return !strings.ContainsRune(token, '/') && !strings.ContainsRune(token, os.PathSeparator)
}

func (e *Engine) selectTokens(requested []string) []string {
	if len(requested) == 0 {
		return e.Manifest.Tokens()
	}
	valid := make(map[string]bool)
	for _, a := range e.Manifest.Apps {
		valid[a.Token] = true
	}
	var out []string
	for _, t := range requested {
		if valid[t] {
			out = append(out, t)
		}
	}
	return out
}

// copyTree copies src to dst. src may be a file, directory, or symlink.
// Returns (filesCopied, bytesCopied, err).
// Honors the Runner's DryRun flag.
func (e *Engine) copyTree(src, dst string, fi os.FileInfo) (int, int64, error) {
	if fi.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return 0, 0, fmt.Errorf("readlink %s: %w", src, err)
		}
		if err := e.Runner.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return 0, 0, err
		}
		if e.Runner.DryRun {
			return 1, 0, nil
		}
		_ = os.Remove(dst)
		if err := e.Runner.Symlink(target, dst); err != nil {
			return 0, 0, err
		}
		return 1, 0, nil
	}

	if fi.IsDir() {
		var files int
		var bytes int64
		walkErr := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, relErr := filepath.Rel(src, p)
			if relErr != nil {
				return relErr
			}
			if rel == "." {
				if mkErr := e.Runner.MkdirAll(dst, 0o755); mkErr != nil {
					return mkErr
				}
				return nil
			}
			if isExcluded(rel) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			tgt := filepath.Join(dst, rel)
			if d.IsDir() {
				return e.Runner.MkdirAll(tgt, 0o755)
			}
			info, infoErr := d.Info()
			if infoErr != nil {
				return infoErr
			}
			if info.Mode()&os.ModeSymlink != 0 {
				target, err := os.Readlink(p)
				if err != nil {
					return nil // skip bad link
				}
				if mkErr := e.Runner.MkdirAll(filepath.Dir(tgt), 0o755); mkErr != nil {
					return mkErr
				}
				if e.Runner.DryRun {
					files++
					return nil
				}
				_ = os.Remove(tgt)
				if err := e.Runner.Symlink(target, tgt); err != nil {
					return err
				}
				files++
				return nil
			}
			n, copyErr := copyFile(e.Runner, p, tgt, info.Mode())
			if copyErr != nil {
				return copyErr
			}
			files++
			bytes += n
			return nil
		})
		return files, bytes, walkErr
	}

	// Regular file
	if err := e.Runner.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 0, 0, err
	}
	n, err := copyFile(e.Runner, src, dst, fi.Mode())
	if err != nil {
		return 0, 0, err
	}
	return 1, n, nil
}

func copyFile(runner *exec.Runner, src, dst string, mode os.FileMode) (int64, error) {
	return snapstore.CopyFile(runner, src, dst, mode)
}

// --- BackupRoot resolution ---

// DefaultBackupRoot returns the fallback backup root for the current user.
// app-settings/ and profiles/ subtrees are created beneath it on demand.
func DefaultBackupRoot(homeDir string) string {
	return filepath.Join(homeDir, ".local", "share", "dotfiles", "backup")
}

// ExpandHome converts a leading "~/" to the home directory.
func ExpandHome(path, home string) string {
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:])
	}
	return path
}

// DetectCloudCandidate looks for the user's preferred cloud-storage secrets
// folder to use as the shared backup root on macOS. Dropbox is preferred over
// Google Drive (so a user who has switched to Dropbox auto-detects it). Returns
// "<cloud>/secrets/dotfiles-backup" for the first cloud root whose "secrets"
// marker exists, or "" when none is found.
func DetectCloudCandidate(home string) string {
	if dropbox := detectDropboxCandidate(home); dropbox != "" {
		return dropbox
	}
	return DetectDriveCandidate(home)
}

// detectDropboxCandidate scans for a Dropbox cloud root (under
// ~/Library/CloudStorage, then the ~/Dropbox symlink in home) that has a
// "secrets" subdirectory marker. CloudStorage is scanned first so the
// canonical path wins over the symlink. Returns "" when none qualifies.
func detectDropboxCandidate(home string) string {
	roots := []string{
		filepath.Join(home, "Library", "CloudStorage"),
		home,
	}
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !strings.HasPrefix(e.Name(), "Dropbox") {
				continue
			}
			// os.Stat follows the ~/Dropbox symlink and any symlinked secrets.
			secrets := filepath.Join(root, e.Name(), "secrets")
			if fi, err := os.Stat(secrets); err == nil && fi.IsDir() {
				return filepath.Join(secrets, "dotfiles-backup")
			}
		}
	}
	return ""
}

// DetectDriveCandidate looks for the user's Drive secrets folder to use as the
// shared backup root on macOS. Returns the first candidate whose parent exists.
func DetectDriveCandidate(home string) string {
	candidates := []string{
		filepath.Join(home, "Library", "CloudStorage"),
		home,
	}
	for _, root := range candidates {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if !strings.Contains(name, "GoogleDrive") && !strings.HasPrefix(name, "My Drive") {
				continue
			}
			drivePaths := []string{
				filepath.Join(root, name, "secrets"),
				filepath.Join(root, name, "My Drive", "secrets"),
			}
			for _, secrets := range drivePaths {
				if _, err := os.Stat(secrets); err == nil {
					return filepath.Join(secrets, "dotfiles-backup")
				}
			}
		}
	}
	return ""
}

// ListHosts enumerates the hostnames that have app-settings archives under
// root, sorted. Returns (nil, nil) when the tree doesn't exist yet.
func ListHosts(root string) ([]string, error) {
	return snapstore.ListHosts(root, "app-settings")
}
