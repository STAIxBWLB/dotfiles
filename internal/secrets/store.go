// Package secrets owns the encrypted secrets store: where the archives
// live, which plaintext file each one maps to, and the age machinery that
// moves bytes between the two.
//
// Every entry point takes a per-command Options struct and returns a typed
// result; progress crosses back as Event values. No user-visible string is
// produced here — formatting is the command layer's job — and no decrypted
// secret material, identity byte, or passphrase is ever written to an
// output sink.
package secrets

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

var (
	backupNow          = time.Now
	writeRestoreBackup = func(file *os.File, data []byte) (int, error) { return file.Write(data) }
	closeRestoreBackup = func(file *os.File) error { return file.Close() }
)

// StoreDirRel is the encrypted store's path relative to a user home. Callers
// join it against the home for the run in hand — a --home override or the
// process home. There is deliberately no process-home resolver beside it:
// one is how every standalone `dot secrets` subcommand came to operate on the
// invoking user's store no matter which home it was pointed at (BUG-08).
const StoreDirRel = ".local/share/dotfiles-secrets"

// sshKeyName returns the configured SSH key name, rejecting values that
// would escape ~/.ssh or the secrets store when interpolated into paths.
func sshKeyName(state *config.UserState) (string, error) {
	keyName := state.SSH.KeyName
	if keyName == "" {
		return "id_ed25519", nil
	}
	if strings.ContainsAny(keyName, "/\\") || keyName == "." || keyName == ".." {
		return "", fmt.Errorf("invalid ssh.key_name %q: must be a bare file name", keyName)
	}
	return keyName, nil
}

// ResolveAgeIdentity returns the age identity path (default
// ~/.ssh/id_ed25519) with a leading ~ expanded.
func ResolveAgeIdentity(state *config.UserState, home string) string {
	identity := state.Secrets.AgeIdentity
	if identity == "" {
		return filepath.Join(home, ".ssh", "id_ed25519")
	}
	if strings.HasPrefix(identity, "~/") {
		return filepath.Join(home, identity[2:])
	}
	if identity == "~" {
		return home
	}
	return identity
}

// Entry maps one encrypted archive name to its plaintext location.
type Entry struct {
	Label   string      // human-readable name for reports
	AgeName string      // file name inside the store / backup dir
	Plain   string      // plaintext path (encrypt source, restore dest)
	DirPerm os.FileMode // permission for the plaintext parent dir
}

// Entries is the single source of truth for which files `dot secrets`
// manages — init, restore, and status all derive from it.
func Entries(state *config.UserState, home string) ([]Entry, error) {
	keyName, err := sshKeyName(state)
	if err != nil {
		return nil, err
	}
	return []Entry{
		{
			Label:   "SSH key",
			AgeName: keyName + ".age",
			Plain:   filepath.Join(home, ".ssh", keyName),
			DirPerm: 0o700,
		},
		{
			Label:   "Shell secrets",
			AgeName: "90-secrets.sh.age",
			Plain:   filepath.Join(home, ".config", "shell", "90-secrets.sh"),
			DirPerm: 0o755,
		},
		{
			Label:   "Beszel agent env",
			AgeName: "beszel-agent.env.age",
			Plain:   filepath.Join(home, ".config", "beszel", "agent.env"),
			DirPerm: 0o700,
		},
		{
			Label:   "Telegram bot env",
			AgeName: "telegram.env.age",
			Plain:   filepath.Join(home, ".config", "dot", "telegram.env"),
			DirPerm: 0o700,
		},
	}, nil
}

// shellSecretsTemplate seeds ~/.config/shell/90-secrets.sh when `dot secrets
// init --scaffold` finds it missing. It is file content, not report output.
const shellSecretsTemplate = `# Shell secrets — sourced by zsh at login via zshrc.
# Add environment exports for API keys, tokens, and other secrets.
# This file is encrypted by 'dot secrets init' into
#   ~/.local/share/dotfiles-secrets/90-secrets.sh.age
# Never commit the plaintext version to git or sync it to Drive.
#
# export OPENAI_API_KEY=sk-...
# export ANTHROPIC_API_KEY=sk-...
# export GITHUB_TOKEN=ghp_...
`

// encryptFile encrypts src to dest without ever truncating an existing dest
// on failure: age writes into a 0600 temp file in the store directory, the
// result is optionally round-trip verified with verify, and only then
// renamed over dest. The dry-run guard comes first — otherwise the
// temp/rename dance would clobber a good archive with an empty file while
// runner.Run no-ops.
func encryptFile(
	ctx context.Context,
	runner *exec.Runner,
	recipientArgs []string,
	src, dest string,
	verify func(agePath string) error,
) error {
	if runner.DryRun {
		runner.Logger.Info("dry-run: would encrypt", "src", src, "dest", dest)
		return nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), "."+filepath.Base(dest)+".enc-*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmp.Name()
	// Registered before the close check can return, so a failing Close
	// cannot strand a partial archive in the store (BUG-09). No-op after a
	// successful rename.
	defer os.Remove(tmpPath)
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp file: %w", err)
	}

	args := append([]string{"-e"}, recipientArgs...)
	args = append(args, "-o", tmpPath, src)
	if _, err := runner.Run(ctx, "age", args...); err != nil {
		return fmt.Errorf("encrypting %s: %w (existing %s untouched)", src, err, dest)
	}
	if verify != nil {
		if err := verify(tmpPath); err != nil {
			return fmt.Errorf("round-trip verification of %s failed: %w (existing %s untouched)", src, err, dest)
		}
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		return fmt.Errorf("replacing %s: %w", dest, err)
	}
	return nil
}

// verifier builds the round-trip check used at encrypt time: it proves the
// configured identity can decrypt what the configured recipients produced,
// so undecryptable archives are caught while the plaintext still exists —
// not at restore time. Returns (nil, reason) when verification must be
// skipped (missing or passphrase-protected identity, which age would prompt
// for on /dev/tty and appear to hang).
func verifier(ctx context.Context, runner *exec.Runner, identity string) (func(string) error, string) {
	if runner.DryRun {
		return nil, ""
	}
	if !runner.FileExists(identity) {
		return nil, fmt.Sprintf("age identity %s not found", identity)
	}
	if head, err := os.ReadFile(identity); err == nil && !bytes.Contains(head, []byte("AGE-SECRET-KEY-")) {
		// SSH identity: detect passphrase protection without prompting.
		if runner.CommandExists("ssh-keygen") {
			if _, err := runner.RunQuery(ctx, "ssh-keygen", "-y", "-P", "", "-f", identity); err != nil {
				return nil, fmt.Sprintf("identity %s appears passphrase-protected", identity)
			}
		}
	}
	return func(agePath string) error {
		out, err := os.CreateTemp(filepath.Dir(agePath), ".verify-*")
		if err != nil {
			return err
		}
		outPath := out.Name()
		// Registered before the close check can return (BUG-09): this temp
		// file holds the round-trip decryption of a secret.
		defer os.Remove(outPath)
		if err := out.Close(); err != nil {
			return err
		}
		_, err = runner.Run(ctx, "age", "-d", "-i", identity, "-o", outPath, agePath)
		return err
	}, ""
}

// copyArchive copies one .age file atomically: read + 0600 temp + rename, so
// an interrupted copy can never truncate the previous (and possibly only)
// backup copy, and backup copies never inherit a loose mode from the store.
func copyArchive(runner *exec.Runner, src, dst string) error {
	if runner.DryRun {
		runner.Logger.Info("dry-run: would copy", "src", src, "dst", dst)
		return nil
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".copy-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	// Same registration order as the other three temp-file sites (BUG-09).
	// This one already removed on every error path explicitly; the defer
	// states the rule once instead of three times.
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, dst)
}

// restoreStatus reports what restoreFile did.
type restoreStatus int

const (
	restoreWritten   restoreStatus = iota // dest created or replaced
	restoreUnchanged                      // decrypted content == existing dest
	restoreSkipped                        // user declined overwrite
)

// backupTimestamp returns a filesystem-safe RFC3339 timestamp (UTC, ':'→'-').
func backupTimestamp() string {
	return strings.ReplaceAll(backupNow().UTC().Format(time.RFC3339), ":", "-")
}

// writeRestoreBackupFile preserves the existing seconds-format backup spelling.
// It is deliberately local to secrets because restore backups are plaintext and
// have different permissions and transaction rules than generic file backups.
func writeRestoreBackupFile(path string, data []byte) (string, error) {
	base := path + ".bak-" + backupTimestamp()
	for suffix := 0; ; suffix++ {
		backupPath := base
		if suffix > 0 {
			backupPath = fmt.Sprintf("%s-%d", base, suffix)
		}
		file, err := os.OpenFile(backupPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			if errors.Is(err, os.ErrExist) {
				continue
			}
			return "", fmt.Errorf("reserving %s: %w", backupPath, err)
		}

		written, writeErr := writeRestoreBackup(file, data)
		if writeErr == nil && written != len(data) {
			writeErr = io.ErrShortWrite
		}
		if writeErr != nil {
			_ = file.Close()
			_ = os.Remove(backupPath)
			return "", fmt.Errorf("writing %s: %w", backupPath, writeErr)
		}
		if err := closeRestoreBackup(file); err != nil {
			_ = os.Remove(backupPath)
			return "", fmt.Errorf("closing %s: %w", backupPath, err)
		}
		return backupPath, nil
	}
}

// restoreFile decrypts srcAge to destPath without ever truncating an
// existing destPath on failure: it decrypts into a 0600 temp file in the
// destination directory and atomically renames it over destPath. When an
// existing, different destPath would be replaced, confirm is consulted once;
// on acceptance the old content is saved to destPath+".bak-<timestamp>".
func restoreFile(
	ctx context.Context,
	runner *exec.Runner,
	identity, srcAge, destPath string,
	dirPerm os.FileMode,
	confirm func(dest string) (bool, error),
) (status restoreStatus, backupPath string, err error) {
	// Identity check before the dry-run short-circuit, so a dry-run preview
	// fails the same way the real restore would.
	if !runner.FileExists(identity) {
		return 0, "", fmt.Errorf("age identity not found: %s", identity)
	}

	if runner.DryRun {
		runner.Logger.Info("dry-run: would restore", "src", srcAge, "dest", destPath)
		return restoreWritten, "", nil
	}

	if err := os.MkdirAll(filepath.Dir(destPath), dirPerm); err != nil {
		return 0, "", fmt.Errorf("creating %s: %w", filepath.Dir(destPath), err)
	}

	// CreateTemp creates the file 0600, so the plaintext secret is never
	// world-readable, even transiently. age reopens it by path with
	// O_TRUNC, which preserves those permissions.
	tmp, err := os.CreateTemp(filepath.Dir(destPath), "."+filepath.Base(destPath)+".restore-*")
	if err != nil {
		return 0, "", fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmp.Name()
	// Registered before the close check can return: a failing Close would
	// otherwise strand a 0600 plaintext secret in the destination
	// directory (BUG-09). No-op after a successful rename.
	defer os.Remove(tmpPath)
	if err := tmp.Close(); err != nil {
		return 0, "", fmt.Errorf("closing temp file: %w", err)
	}

	if _, err := runner.Run(ctx, "age", "-d", "-i", identity, "-o", tmpPath, srcAge); err != nil {
		return 0, "", fmt.Errorf("decrypting %s: %w (existing %s untouched)", srcAge, err, destPath)
	}

	newData, err := os.ReadFile(tmpPath)
	if err != nil {
		return 0, "", fmt.Errorf("reading decrypted output: %w", err)
	}

	if oldData, err := os.ReadFile(destPath); err == nil {
		if bytes.Equal(oldData, newData) {
			// Still heal drifted permissions — ssh refuses
			// group/world-readable keys.
			if err := os.Chmod(destPath, 0600); err != nil {
				return 0, "", fmt.Errorf("restoring permissions on %s: %w", destPath, err)
			}
			return restoreUnchanged, "", nil
		}
		ok, err := confirm(destPath)
		if err != nil {
			return 0, "", err
		}
		if !ok {
			return restoreSkipped, "", nil
		}
		backupPath, err = writeRestoreBackupFile(destPath, oldData)
		if err != nil {
			return 0, "", fmt.Errorf("backing up %s: %w", destPath, err)
		}
	} else if !os.IsNotExist(err) {
		return 0, "", fmt.Errorf("reading existing %s: %w", destPath, err)
	}

	if err := os.Rename(tmpPath, destPath); err != nil {
		return 0, backupPath, fmt.Errorf("replacing %s: %w", destPath, err)
	}
	return restoreWritten, backupPath, nil
}
