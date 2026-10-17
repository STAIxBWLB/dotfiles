// Package claudecfg is the single owner of the ~/.claude/settings.json
// path, its lock, and its write. Every package that reads or writes that
// file goes through SettingsPath, Read, or Mutate; no other package joins
// a home directory with .claude and settings.json.
package claudecfg

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/fileutil"
)

// ErrNoChange short-circuits Mutate without writing. An edit closure
// returns it when the desired state already holds or when it declines to
// write (e.g. a foreign-owned block it must not overwrite).
var ErrNoChange = errors.New("claudecfg: no change")

// ErrInvalidJSON wraps every parse failure Read reports, so a read-only
// caller (a status or drift report) can translate the condition into a
// drift result instead of aborting, while writers still fail hard.
var ErrInvalidJSON = errors.New("claudecfg: settings json invalid")

// SettingsPath returns ~/.claude/settings.json for the given home.
func SettingsPath(home string) string {
	return filepath.Join(home, ".claude", "settings.json")
}

// LockDir returns the PID lock directory serializing Mutate calls. It
// lives inside Claude Code's tree, so docs/BOUNDARIES.md names it as a
// dotfiles write root.
func LockDir(home string) string {
	return filepath.Join(home, ".claude", ".dot-lock")
}

// Read parses settings.json into a generic map. A missing or empty file
// yields an empty map; invalid JSON is a hard error so dot never clobbers
// a file it cannot faithfully rewrite.
//
// Read deliberately takes no lock: a hook or status read that blocked on a
// stale or root-owned lock would be worse than the race a read lock would
// close. Only writers serialize.
func Read(home string) (map[string]any, error) {
	path := SettingsPath(home)
	data, err := readRaw(path)
	if err != nil {
		return nil, err
	}
	return parseSettings(path, data)
}

// readRaw returns settings.json's bytes. A missing file yields nil bytes,
// the same tolerance Read has, so Mutate can hash "absent" and "empty"
// distinctly from any real content.
func readRaw(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return data, nil
}

// parseSettings is the parse half of Read, split out so Mutate can hash the
// exact bytes it parsed instead of reading the file a second time.
func parseSettings(path string, data []byte) (map[string]any, error) {
	settings := map[string]any{}
	if len(bytes.TrimSpace(data)) == 0 {
		return settings, nil
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("parse %s: %w: %v (fix the JSON manually; dot will not overwrite it)", path, ErrInvalidJSON, err)
	}
	return settings, nil
}

// hashSettings fingerprints the on-disk bytes so Mutate can tell whether a
// foreign writer landed between its unlocked pre-read and its locked
// re-read. It lives here rather than in fileutil because only this one
// caller needs it, and fileutil already carries the EnsureFile/EnsureFileAtomic
// pair's worth of near-duplicate bodies.
func hashSettings(data []byte) [sha256.Size]byte {
	return sha256.Sum256(data)
}

// settingsLockStaleAfter is this caller's own pid-less lock horizon. See the
// acquire site in Mutate for why it is minutes rather than the package
// default's hour.
const settingsLockStaleAfter = 2 * time.Minute

// mutateAttempts bounds Mutate's retry: one pass, plus one more if a foreign
// writer landed inside the window. Two writers trading the file back and
// forth is a condition to report, not to spin on.
const mutateAttempts = 2

// Mutate is the write path for settings.json. Its order is, per attempt:
//
//  1. read the raw bytes UNLOCKED and hash them (a missing or empty file is
//     an empty document, the same tolerance Read has);
//  2. parse through the same code path Read uses, so invalid JSON still
//     fails hard with ErrInvalidJSON and dot never overwrites a file it
//     cannot faithfully rewrite;
//  3. apply fn. ErrNoChange returns (false, nil) IMMEDIATELY — on the first
//     pass that is before any lock acquire, so a no-op mutation creates
//     neither ~/.claude nor the lock directory; any other closure error is
//     returned with nothing written;
//  4. marshal with two-space indentation and a trailing newline;
//  5. acquire the PID lock at LockDir, once. The retry keeps holding it;
//  6. re-read and re-hash under the lock. A different hash means another
//     writer landed inside the window: discard the marshaled document and
//     go around from step 1 without releasing the lock, so fn is re-applied
//     to the foreign writer's content instead of clobbering it. A second
//     mismatch is an error naming the path;
//  7. persist with fileutil.EnsureFileAtomic at mode 0644 — hash-skip when
//     unchanged, backup-before-overwrite, dry-run aware through the runner,
//     and a temp-and-rename write, so an interrupted write leaves the
//     original intact and a symlink at the path is refused rather than
//     written through. It returns that call's changed flag.
//
// The lock is released on every exit path: the release is deferred as soon
// as it exists, so a marshal failure, a re-read failure, a declining retry
// and a write failure all drop it.
//
// ponytail: known ceiling, and a residual on purpose rather than a race
// claimed closed. Step 6's re-hash happens BEFORE EnsureFileAtomic, which
// then does its own read, a backup, a MkdirAll and only then the rename. A
// foreign write landing inside that final sub-interval is still lost. It is
// far smaller than the read-to-write window this restructure closes, but it
// is not zero, and the lock binds only dot. Closing it needs a
// compare-and-swap write: move the hash check inside Runner.WriteFileAtomic,
// between its temp-file write and its rename.
func Mutate(runner *exec.Runner, home string, fn func(map[string]any) error) (bool, error) {
	path := SettingsPath(home)
	var release func()
	defer func() {
		if release != nil {
			release()
		}
	}()

	for attempt := 0; attempt < mutateAttempts; attempt++ {
		raw, err := readRaw(path)
		if err != nil {
			return false, err
		}
		settings, err := parseSettings(path, raw)
		if err != nil {
			return false, err
		}
		if err := fn(settings); err != nil {
			if errors.Is(err, ErrNoChange) {
				return false, nil
			}
			return false, err
		}
		data, err := json.MarshalIndent(settings, "", "  ")
		if err != nil {
			return false, err
		}
		if release == nil {
			// A settings mutation is a sub-second operation, so a lock older
			// than a few minutes with no live pid behind it is abandoned by
			// definition. The package default of an hour was sized for a
			// legacy bare-directory sync lock; applied here it would block
			// dot ai hud and dot guard for an hour behind, say, a lock left
			// root-owned by a sudo run.
			if release, err = fileutil.AcquirePIDLock(LockDir(home), fileutil.LockOptions{
				Label:      "another dot write to the claude settings is running",
				StaleAfter: settingsLockStaleAfter,
			}); err != nil {
				return false, err
			}
		}
		current, err := readRaw(path)
		if err != nil {
			return false, err
		}
		if hashSettings(current) != hashSettings(raw) {
			continue
		}
		return fileutil.EnsureFileAtomic(runner, home, path, append(data, '\n'), 0o644)
	}
	return false, fmt.Errorf("%s changed underneath the write twice; another process is rewriting it, so dot stopped rather than overwrite it", path)
}
