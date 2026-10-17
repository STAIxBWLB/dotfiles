//go:build darwin || linux

package fileutil

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
	"golang.org/x/sys/unix"
)

var (
	backupNow       = time.Now
	writeBackupFile = func(runner *exec.Runner, path string, data []byte, perm os.FileMode) error {
		return runner.WriteFile(path, data, perm)
	}
	writeReservedBackup = func(file *os.File, data []byte) (int, error) { return file.Write(data) }
	closeReservedBackup = func(file *os.File) error { return file.Close() }

	// beforeBackupDirectoryHarden exists only to make the final-directory
	// replacement boundary deterministic in tests. Production keeps it a no-op.
	beforeBackupDirectoryHarden = func(string) error { return nil }
	chmodBackupFD               = func(fd int, _ string, mode os.FileMode) error { return unix.Fchmod(fd, uint32(mode.Perm())) }
)

type backupDirectory struct {
	fd       int
	parentFD int
	name     string
	path     string
}

func (d *backupDirectory) Close() error {
	err := unix.Close(d.fd)
	parentErr := unix.Close(d.parentFD)
	if err != nil {
		return err
	}
	return parentErr
}

// backup copies an existing file to the backup directory under home. Every
// component is opened relative to a trusted home descriptor with O_NOFOLLOW;
// once the final directory is open, all hardening and reservation operations
// remain anchored to that descriptor rather than to a replaceable pathname.
func backup(runner *exec.Runner, home, path string) error {
	if home == "" {
		return fmt.Errorf("backing up %q: no home directory given", path)
	}
	bdir := filepath.Join(home, backupDir)
	dir, err := openBackupDirectory(home, !runner.DryRun)
	if err != nil {
		if runner.DryRun && errors.Is(err, os.ErrNotExist) {
			data, readErr := runner.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			_, writeErr := writeBackupCopyDryRun(runner, bdir, filepath.Base(path), data)
			return writeErr
		}
		return err
	}
	defer dir.Close()

	if err := hardenBackupDirectory(dir, runner.DryRun); err != nil {
		return err
	}
	data, err := runner.ReadFile(path)
	if err != nil {
		return err
	}
	if runner.DryRun {
		_, err = writeBackupCopyDryRun(runner, bdir, filepath.Base(path), data)
		return err
	}
	_, err = writeBackupCopy(dir, filepath.Base(path), data)
	return err
}

func openBackupDirectory(home string, create bool) (*backupDirectory, error) {
	homeFD, err := unix.Open(home, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("opening backup home %q without following links: %w", home, err)
	}
	currentFD := homeFD
	parts := strings.Split(backupDir, string(filepath.Separator))
	for index, part := range parts {
		childFD, openErr := openDirectoryAt(currentFD, part)
		if errors.Is(openErr, unix.ENOENT) && create {
			mode := uint32(0o755)
			if index == len(parts)-1 {
				mode = 0o700
			}
			if mkdirErr := unix.Mkdirat(currentFD, part, mode); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				_ = unix.Close(currentFD)
				return nil, fmt.Errorf("creating backup path component %q: %w", part, mkdirErr)
			}
			childFD, openErr = openDirectoryAt(currentFD, part)
		}
		if openErr != nil {
			_ = unix.Close(currentFD)
			return nil, fmt.Errorf("opening backup path component %q without following links: %w", part, openErr)
		}
		if index == len(parts)-1 {
			return &backupDirectory{fd: childFD, parentFD: currentFD, name: part, path: filepath.Join(home, backupDir)}, nil
		}
		_ = unix.Close(currentFD)
		currentFD = childFD
	}
	panic("backup directory has no path components")
}

func openDirectoryAt(parentFD int, name string) (int, error) {
	return unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
}

// attached proves the name in the held parent still denotes the held final
// directory. This is an availability guard for a replacement detected at the
// testable boundary; security does not depend on it because later operations
// are descriptor-relative either way.
func (d *backupDirectory) attached() error {
	var held, current unix.Stat_t
	if err := unix.Fstat(d.fd, &held); err != nil {
		return fmt.Errorf("inspecting held backup directory %q: %w", d.path, err)
	}
	if err := unix.Fstatat(d.parentFD, d.name, &current, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("checking backup directory %q after open: %w", d.path, err)
	}
	if current.Mode&unix.S_IFMT != unix.S_IFDIR || held.Dev != current.Dev || held.Ino != current.Ino {
		return fmt.Errorf("backup directory %q changed after it was opened; retry after removing the replacement", d.path)
	}
	return nil
}

// hardenBackupDirectory establishes the owner-only recovery boundary before a
// new copy is reserved. It operates only through the opened directory and
// entry descriptors, so a symlink or rename cannot redirect chmod/read/write.
func hardenBackupDirectory(dir *backupDirectory, dryRun bool) error {
	if err := beforeBackupDirectoryHarden(dir.path); err != nil {
		return err
	}
	if err := dir.attached(); err != nil {
		return err
	}
	if !dryRun {
		if err := chmodBackupFD(dir.fd, dir.path, 0o700); err != nil {
			return fmt.Errorf("restricting backup directory %q: %w", dir.path, err)
		}
	}

	return hardenBackupEntries(dir.fd, dir.path, dryRun, true)
}

// hardenBackupEntries applies the owner-only boundary to every entry below an
// already-held directory descriptor.
//
// strict is true only for the backup root, where backup() reserves its own
// flat copies at a known 0600 and anything else is a tamper signal. Below it
// the tree holds opaque snapshot payloads written by the agents, app-settings
// and profile features -- real application state that legitimately nests
// directories and symlinks, and that restore has to put back verbatim. So the
// lenient pass only removes group/world access and keeps each file's owner
// bits: agent restores rename whole managed trees (.claude/hooks and plugin
// dirs) in with their modes intact, and a flat 0600 would strip the execute
// bit off every hook it preserved. Entries it cannot tighten safely are left
// alone -- non-regular ones (O_NOFOLLOW means a symlink is never followed) and
// multiply-linked regular files, whose inode may also be reachable outside the
// tree. Nothing is exposed by skipping them: the 0700 ancestors already keep
// the whole subtree owner-only.
func hardenBackupEntries(dirFD int, dirPath string, dryRun, strict bool) error {
	readFD, err := unix.Dup(dirFD)
	if err != nil {
		return fmt.Errorf("duplicating backup directory %q for reading: %w", dirPath, err)
	}
	readDir := os.NewFile(uintptr(readFD), dirPath)
	entries, readErr := readDir.ReadDir(-1)
	closeReadErr := readDir.Close()
	if readErr != nil {
		return fmt.Errorf("reading backup directory %q: %w", dirPath, readErr)
	}
	if closeReadErr != nil {
		return fmt.Errorf("closing backup directory %q after reading: %w", dirPath, closeReadErr)
	}
	for _, entry := range entries {
		entryPath := filepath.Join(dirPath, entry.Name())
		entryFD, err := unix.Openat(dirFD, entry.Name(), unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			if !strict && errors.Is(err, unix.ELOOP) {
				continue // a symlink in a snapshot payload; never followed
			}
			return fmt.Errorf("opening backup entry %q without following links: %w", entryPath, err)
		}
		var info unix.Stat_t
		statErr := unix.Fstat(entryFD, &info)
		isDir := statErr == nil && info.Mode&unix.S_IFMT == unix.S_IFDIR
		isRegular := statErr == nil && info.Mode&unix.S_IFMT == unix.S_IFREG
		if statErr == nil && !isDir && !isRegular {
			if !strict {
				_ = unix.Close(entryFD)
				continue
			}
			statErr = fmt.Errorf("must be a regular file or directory")
		}
		if statErr == nil && !dryRun {
			switch {
			case isDir:
				// Always traversable by the owner; a directory's bits are not
				// application state worth preserving.
				statErr = chmodBackupFD(entryFD, entryPath, 0o700)
			case strict:
				statErr = chmodBackupFD(entryFD, entryPath, 0o600)
			case info.Nlink == 1:
				statErr = chmodBackupFD(entryFD, entryPath, os.FileMode(info.Mode&0o700))
			}
		}
		var recurseErr error
		if statErr == nil && isDir {
			recurseErr = hardenBackupEntries(entryFD, entryPath, dryRun, false)
		}
		closeErr := unix.Close(entryFD)
		if statErr != nil {
			return fmt.Errorf("hardening backup entry %q: %w", entryPath, statErr)
		}
		if recurseErr != nil {
			return recurseErr
		}
		if closeErr != nil {
			return fmt.Errorf("closing backup entry %q: %w", entryPath, closeErr)
		}
	}
	return nil
}

// writeBackupCopy preserves the existing seconds-format backup spelling while
// reserving every candidate descriptor-relative to the held backup directory.
func writeBackupCopy(dir *backupDirectory, base string, data []byte) (string, error) {
	name := fmt.Sprintf("%s.%s", base, backupNow().Format("20060102-150405"))
	for suffix := 0; ; suffix++ {
		candidate := name
		if suffix > 0 {
			candidate = fmt.Sprintf("%s-%d", name, suffix)
		}
		fd, err := unix.Openat(dir.fd, candidate, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if err != nil {
			if errors.Is(err, unix.EEXIST) {
				continue
			}
			return "", fmt.Errorf("reserving %s: %w", filepath.Join(dir.path, candidate), err)
		}

		file := os.NewFile(uintptr(fd), filepath.Join(dir.path, candidate))
		written, writeErr := writeReservedBackup(file, data)
		if writeErr == nil && written != len(data) {
			writeErr = io.ErrShortWrite
		}
		if writeErr != nil {
			_ = file.Close()
			_ = unix.Unlinkat(dir.fd, candidate, 0)
			return "", fmt.Errorf("writing %s: %w", file.Name(), writeErr)
		}
		if err := closeReservedBackup(file); err != nil {
			_ = unix.Unlinkat(dir.fd, candidate, 0)
			return "", fmt.Errorf("closing %s: %w", file.Name(), err)
		}
		return filepath.Join(dir.path, candidate), nil
	}
}

func writeBackupCopyDryRun(runner *exec.Runner, bdir, base string, data []byte) (string, error) {
	dest := filepath.Join(bdir, fmt.Sprintf("%s.%s", base, backupNow().Format("20060102-150405")))
	if err := writeBackupFile(runner, dest, data, 0o600); err != nil {
		return "", err
	}
	return dest, nil
}
