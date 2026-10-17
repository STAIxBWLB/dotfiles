package syncer

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/fileutil"
)

// Paths holds well-known file locations for sync artifacts.
type Paths struct {
	Profile        string // normalized sync profile that owns every scheduler identity
	LogFile        string // ~/.local/log/dotfiles-sync.log
	LockDir        string // ~/Library/Caches/dotfiles/sync.lock (macOS) or equivalent
	LaunchdPlist   string // ~/Library/LaunchAgents/com.dotfiles.sync.plist (macOS, push)
	SystemdService string // ~/.config/systemd/user/dotfiles-sync.service (Linux, push)
	SystemdTimer   string // ~/.config/systemd/user/dotfiles-sync.timer (Linux, push)
}

// PlistFor returns the launchd plist path for the given kind.
func (p *Paths) PlistFor(kind SchedulerKind) string {
	dir := filepath.Dir(p.LaunchdPlist)
	return filepath.Join(dir, p.LaunchdLabelFor(kind)+".plist")
}

// SystemdServiceFor returns the systemd service unit path for the kind.
func (p *Paths) SystemdServiceFor(kind SchedulerKind) string {
	dir := filepath.Dir(p.SystemdService)
	return filepath.Join(dir, p.SystemdServiceNameFor(kind))
}

// SystemdTimerFor returns the systemd timer unit path for the kind.
func (p *Paths) SystemdTimerFor(kind SchedulerKind) string {
	dir := filepath.Dir(p.SystemdTimer)
	return filepath.Join(dir, p.SystemdTimerNameFor(kind))
}

// LaunchdLabelFor returns the label derived from this path set's normalized
// profile. The default remains the historical static SchedulerKind label.
func (p *Paths) LaunchdLabelFor(kind SchedulerKind) string {
	profile := p.schedulerProfile()
	if profile == DefaultProfile {
		return kind.LaunchdLabel()
	}
	return strings.Replace(kind.LaunchdLabel(), "com.dotfiles.sync", "com.dotfiles."+profile, 1)
}

// SystemdServiceNameFor returns the service filename derived from this path
// set's normalized profile. The default remains byte-identical.
func (p *Paths) SystemdServiceNameFor(kind SchedulerKind) string {
	profile := p.schedulerProfile()
	if profile == DefaultProfile {
		return kind.SystemdServiceName()
	}
	return strings.Replace(kind.SystemdServiceName(), "dotfiles-sync", "dotfiles-"+profile, 1)
}

// SystemdTimerNameFor returns the timer filename derived from this path set's
// normalized profile. The default remains byte-identical.
func (p *Paths) SystemdTimerNameFor(kind SchedulerKind) string {
	profile := p.schedulerProfile()
	if profile == DefaultProfile {
		return kind.SystemdTimerName()
	}
	return strings.Replace(kind.SystemdTimerName(), "dotfiles-sync", "dotfiles-"+profile, 1)
}

func (p *Paths) schedulerProfile() string {
	if p == nil || p.Profile == "" {
		return DefaultProfile
	}
	return p.Profile
}

// resolvePaths returns the standard gsync artifact paths for the current user.
// The cache directory is the only environment-sensitive input: it comes from
// os.UserCacheDir here, or from cacheDirForHome for an explicit home, so
// LockDir stays out of the workspace tree.
func resolvePaths() (*Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolving home: %w", err)
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		// fall back to /tmp on platforms without UserCacheDir
		cacheDir = "/tmp"
	}
	return pathsFor(home, cacheDir), nil
}

// resolvePathsForHome resolves gsync artifact paths against an explicit home
// directory. Commands that honor --home reach it through
// resolvePathsForHomeProfile, so per-user artifact and lock paths follow the
// target home rather than the invoking user's. An empty home falls back to
// resolvePaths (current user).
func resolvePathsForHome(home string) (*Paths, error) {
	if home == "" {
		return resolvePaths()
	}
	return pathsFor(home, cacheDirForHome(home)), nil
}

// resolvePathsForHomeProfile builds the artifact layout for one sync profile
// under an explicit home. It is the single resolution entry point: home and
// profile are both arguments, so neither can be silently assumed by a caller.
//
// Scheduler units are per-profile, but the peer profile deliberately shares
// the default lock: peer and cloud runs can both mutate the same workspace
// tree, so they must never overlap. Other custom profiles keep independent
// locks.
//
// The default profile is unchanged by construction - "sync.lock" and
// "com.dotfiles.sync.plist" are exactly what the templated names produce when
// the profile is "sync".
func resolvePathsForHomeProfile(home, profile string) (*Paths, error) {
	if err := ValidateProfile(profile); err != nil {
		return nil, err
	}
	p, err := resolvePathsForHome(home)
	if err != nil {
		return nil, err
	}
	return withProfile(p, profile), nil
}

func withProfile(p *Paths, profile string) *Paths {
	prof := NormalizeProfile(profile)
	if prof == DefaultProfile {
		return p
	}
	out := *p
	out.Profile = prof
	if prof != PeerProfile {
		out.LockDir = filepath.Join(filepath.Dir(p.LockDir), prof+".lock")
	}
	out.LaunchdPlist = filepath.Join(filepath.Dir(p.LaunchdPlist), out.LaunchdLabelFor(SchedulerKindPush)+".plist")
	out.SystemdService = filepath.Join(filepath.Dir(p.SystemdService), out.SystemdServiceNameFor(SchedulerKindPush))
	out.SystemdTimer = filepath.Join(filepath.Dir(p.SystemdTimer), out.SystemdTimerNameFor(SchedulerKindPush))
	return &out
}

// pathsFor builds the artifact layout for a given home + cache dir.
func pathsFor(home, cacheDir string) *Paths {
	return &Paths{
		Profile:        DefaultProfile,
		LogFile:        filepath.Join(home, ".local", "log", "dotfiles-sync.log"),
		LockDir:        filepath.Join(cacheDir, "dotfiles", "sync.lock"),
		LaunchdPlist:   filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist"),
		SystemdService: filepath.Join(home, ".config", "systemd", "user", systemdServiceName),
		SystemdTimer:   filepath.Join(home, ".config", "systemd", "user", systemdTimerName),
	}
}

// cacheDirForHome mirrors os.UserCacheDir's layout for an explicit home so a
// --home target's lock lives under that home, not the invoking user's cache.
func cacheDirForHome(home string) string {
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Caches")
	}
	return filepath.Join(home, ".cache")
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// AcquireLock creates a POSIX-safe lock directory with a PID file inside.
// Returns a release function that removes the lock dir.
//
// On EEXIST, reads <lockDir>/lock.pid and probes the process with signal 0.
// If the PID is dead (ESRCH), removes the stale lock and retries once.
// Otherwise reports the lock is held by another running sync.
// The zero-value LockOptions is deliberate: it keeps the refusal wording and
// the one-hour pid-less horizon a sync lock has always had, so nothing an
// operator has already seen changes shape.
func AcquireLock(lockDir string) (func(), error) {
	return fileutil.AcquirePIDLock(lockDir, fileutil.LockOptions{})
}

// AcquireLockForRun is AcquireLock for a run that may be a preview. A preview
// must leave the target tree byte-identical, and AcquirePIDLock creates the
// lock parent unconditionally (internal/fileutil/lock.go), so acquiring the
// lock is itself a write on a home no sync has ever run in.
//
// When the parent is absent no sync can be holding the lock, so a preview
// proceeds lock-free and creates nothing. When the parent is present the real
// lock is taken: the lock directory it adds is removed by release, so the tree
// still comes back unchanged, and a preview run beside a live sync still
// reports the busy lock rather than reading a tree being rewritten underneath
// it. Skipping the lock outright would have traded the leak for a torn preview.
//
// The guard is here rather than at the seven call sites because a per-caller
// guard leaves the next sibling caller broken: every one of them acquired the
// lock as its first statement, above its own dry-run branch.
func AcquireLockForRun(lockDir string, dryRun bool) (func(), error) {
	if dryRun && !pathExists(filepath.Dir(lockDir)) {
		return func() {}, nil
	}
	return AcquireLock(lockDir)
}

// lockIsStale returns true when the PID file inside lockDir points at a
// process that no longer exists. A missing or unparseable PID file is
// treated as stale (defensive: better to break a held-but-corrupted lock
// than block forever).
func lockIsStale(lockDir string) bool {
	return fileutil.PIDLockIsStale(lockDir)
}

// ensureLogDir creates the parent directory of logFile if needed.
func ensureLogDir(logFile string) error {
	return os.MkdirAll(filepath.Dir(logFile), 0755)
}

// AppendLog writes one line to the log file describing a sync operation.
func AppendLog(logFile, op string, exitCode int) {
	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s op=%s exit=%d\n", time.Now().Format("2006-01-02 15:04:05"), op, exitCode)
}

// RotateLog trims logFile if it exceeds maxLines, keeping the last keepLines.
func RotateLog(logFile string, maxLines, keepLines int) {
	data, err := os.ReadFile(logFile)
	if err != nil {
		return
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) <= maxLines {
		return
	}
	if keepLines > len(lines) {
		keepLines = len(lines)
	}
	kept := lines[len(lines)-keepLines:]
	_ = os.WriteFile(logFile, []byte(strings.Join(kept, "\n")+"\n"), 0644)
}

// TailLog returns the last n lines of logFile as a single string.
func TailLog(logFile string, n int) (string, error) {
	return fileutil.TailLog(logFile, n)
}

// newTimestamp returns a filesystem-safe RFC3339 timestamp suitable for
// timestamped backup/conflict directories. Replaces ':' with '-' so it
// works on filesystems (e.g. SMB shares) that disallow colons in names.
func newTimestamp() string {
	return strings.ReplaceAll(time.Now().UTC().Format(time.RFC3339), ":", "-")
}

// newSubSecondTimestamp is like newTimestamp but adds microsecond
// resolution. Used for intake run-dirs where two runs in the same
// wall-clock second is plausible (a successful intake completing fast
// enough that a follow-up run finds new content), and same-dir
// collisions would silently merge their staged files.
func newSubSecondTimestamp() string {
	t := time.Now().UTC()
	// Format: 2026-05-02T10-00-00.123456Z. Both ':' (none here) and the
	// fractional separator '.' are filesystem-safe across darwin/linux/
	// SMB; no replace needed.
	return t.Format("2006-01-02T15-04-05.000000Z")
}
