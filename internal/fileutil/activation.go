package fileutil

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// ActivationOptions describes one same-filesystem replacement of a declared
// component. Only OwnedEntries may be renamed or removed from DestinationRoot.
type ActivationOptions struct {
	DestinationRoot string
	StagedRoot      string
	OwnedEntries    []string
	// StaleEntries were owned by a prior marker but are absent from the staged
	// component. They are moved into the same rollback transaction and removed
	// only after the promoted component validates.
	StaleEntries []string
	Validate     func(string) error
}

// activationRename is a narrow test seam for promotion and rollback failures.
// Production always uses the common parent Root, which keeps the complete
// transaction below the component parent even if a path is replaced while the
// transaction is in progress.
var activationRename = func(root *os.Root, oldName, newName string) error {
	return root.Rename(oldName, newName)
}

type activationMove struct {
	entry       string
	hadPrevious bool
	promoted    bool
}

// ActivateOwnedComponent validates staged content and then atomically promotes
// only declared component entries. It preserves every other destination child,
// and restores the prior owned entries if any cutover or final validation fails.
func ActivateOwnedComponent(runner *exec.Runner, opts ActivationOptions) error {
	if runner == nil {
		return errors.New("activation runner is required")
	}
	if err := validateActivationOptions(opts); err != nil {
		return err
	}

	lockDir := filepath.Join(filepath.Dir(opts.DestinationRoot), "."+filepath.Base(opts.DestinationRoot)+".activation.lock")
	unlock, err := AcquirePIDLock(lockDir, LockOptions{Label: "component activation is running"})
	if err != nil {
		return err
	}
	defer unlock()

	if err := opts.Validate(opts.StagedRoot); err != nil {
		return fmt.Errorf("validating staged component: %w", err)
	}
	for _, entry := range opts.OwnedEntries {
		if _, err := os.Lstat(filepath.Join(opts.StagedRoot, entry)); err != nil {
			return fmt.Errorf("staged owned entry %q: %w", entry, err)
		}
	}
	if runner.DryRun {
		runner.Logger.Info("dry-run: activate owned component", "destination", opts.DestinationRoot, "stage", opts.StagedRoot, "entries", len(opts.OwnedEntries))
		return nil
	}

	parentRoot, err := os.OpenRoot(filepath.Dir(opts.DestinationRoot))
	if err != nil {
		return fmt.Errorf("opening component parent: %w", err)
	}
	defer parentRoot.Close()

	destinationName := filepath.Base(opts.DestinationRoot)
	stageName := filepath.Base(opts.StagedRoot)
	if err := rejectSymlinkComponents(parentRoot, destinationName, true); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("checking component destination: %w", err)
	}
	if _, err := parentRoot.Lstat(destinationName); os.IsNotExist(err) {
		if err := parentRoot.Mkdir(destinationName, 0755); err != nil {
			return fmt.Errorf("preparing component destination: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("checking component destination: %w", err)
	}
	if err := rejectSymlinkComponents(parentRoot, stageName, true); err != nil {
		return fmt.Errorf("checking activation stage: %w", err)
	}
	rollbackName, err := makeActivationRollbackRoot(parentRoot, destinationName)
	if err != nil {
		return err
	}

	moves := make([]activationMove, 0, len(opts.OwnedEntries))
	for _, entry := range opts.OwnedEntries {
		move := activationMove{entry: entry}
		destination := filepath.Join(destinationName, entry)
		rollback := filepath.Join(rollbackName, entry)
		stage := filepath.Join(stageName, entry)

		if err := rejectSymlinkComponents(parentRoot, destination, false); err != nil && !os.IsNotExist(err) {
			return rollbackActivation(parentRoot, destinationName, rollbackName, moves, fmt.Errorf("checking active entry %q: %w", entry, err))
		}
		if err := rejectSymlinkComponents(parentRoot, stage, false); err != nil {
			return rollbackActivation(parentRoot, destinationName, rollbackName, moves, fmt.Errorf("checking staged entry %q: %w", entry, err))
		}
		if _, err := parentRoot.Lstat(destination); err == nil {
			if err := parentRoot.MkdirAll(filepath.Dir(rollback), 0755); err != nil {
				return rollbackActivation(parentRoot, destinationName, rollbackName, moves, fmt.Errorf("preparing rollback parent for %q: %w", entry, err))
			}
			if err := activationRename(parentRoot, destination, rollback); err != nil {
				return rollbackActivation(parentRoot, destinationName, rollbackName, moves, fmt.Errorf("moving active entry %q to rollback: %w", entry, err))
			}
			move.hadPrevious = true
		} else if !os.IsNotExist(err) {
			return rollbackActivation(parentRoot, destinationName, rollbackName, moves, fmt.Errorf("checking active entry %q: %w", entry, err))
		}

		if err := parentRoot.MkdirAll(filepath.Dir(destination), 0755); err != nil {
			moves = append(moves, move)
			return rollbackActivation(parentRoot, destinationName, rollbackName, moves, fmt.Errorf("preparing destination parent for %q: %w", entry, err))
		}
		if err := activationRename(parentRoot, stage, destination); err != nil {
			moves = append(moves, move)
			return rollbackActivation(parentRoot, destinationName, rollbackName, moves, fmt.Errorf("promoting staged entry %q: %w", entry, err))
		}
		move.promoted = true
		moves = append(moves, move)
	}
	for _, entry := range opts.StaleEntries {
		move := activationMove{entry: entry}
		destination := filepath.Join(destinationName, entry)
		rollback := filepath.Join(rollbackName, entry)
		if err := rejectSymlinkComponents(parentRoot, destination, false); err != nil && !os.IsNotExist(err) {
			return rollbackActivation(parentRoot, destinationName, rollbackName, moves, fmt.Errorf("checking stale entry %q: %w", entry, err))
		}
		if _, err := parentRoot.Lstat(destination); err == nil {
			if err := parentRoot.MkdirAll(filepath.Dir(rollback), 0755); err != nil {
				return rollbackActivation(parentRoot, destinationName, rollbackName, moves, fmt.Errorf("preparing stale rollback parent for %q: %w", entry, err))
			}
			if err := activationRename(parentRoot, destination, rollback); err != nil {
				return rollbackActivation(parentRoot, destinationName, rollbackName, moves, fmt.Errorf("moving stale active entry %q to rollback: %w", entry, err))
			}
			move.hadPrevious = true
		} else if !os.IsNotExist(err) {
			return rollbackActivation(parentRoot, destinationName, rollbackName, moves, fmt.Errorf("checking stale active entry %q: %w", entry, err))
		}
		moves = append(moves, move)
	}

	if err := opts.Validate(opts.DestinationRoot); err != nil {
		return rollbackActivation(parentRoot, destinationName, rollbackName, moves, fmt.Errorf("validating promoted component: %w", err))
	}
	if err := parentRoot.RemoveAll(rollbackName); err != nil {
		return fmt.Errorf("removing rollback directory: %w", err)
	}
	return nil
}

func validateActivationOptions(opts ActivationOptions) error {
	if !filepath.IsAbs(opts.DestinationRoot) || !filepath.IsAbs(opts.StagedRoot) {
		return errors.New("activation destination and stage must be absolute")
	}
	if filepath.Clean(opts.DestinationRoot) == filepath.Clean(opts.StagedRoot) || filepath.Dir(opts.DestinationRoot) != filepath.Dir(opts.StagedRoot) {
		return errors.New("activation stage must be a same-parent sibling of destination")
	}
	if opts.Validate == nil {
		return errors.New("activation validation is required")
	}
	info, err := os.Stat(opts.StagedRoot)
	if err != nil {
		return fmt.Errorf("checking activation stage: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("activation stage %q is not a directory", opts.StagedRoot)
	}
	if len(opts.OwnedEntries) == 0 {
		return errors.New("activation requires at least one owned entry")
	}
	allEntries := append(append([]string(nil), opts.OwnedEntries...), opts.StaleEntries...)
	for i, entry := range allEntries {
		if entry == "." || entry == "" || !filepath.IsLocal(entry) {
			return fmt.Errorf("unsafe owned entry %q", entry)
		}
		for j := 0; j < i; j++ {
			other := allEntries[j]
			if entry == other || strings.HasPrefix(entry, other+string(filepath.Separator)) || strings.HasPrefix(other, entry+string(filepath.Separator)) {
				return fmt.Errorf("overlapping owned entries %q and %q", other, entry)
			}
		}
	}
	return nil
}

func makeActivationRollbackRoot(root *os.Root, destinationName string) (string, error) {
	for i := 0; i < 100; i++ {
		name := fmt.Sprintf(".%s.rollback-%d", destinationName, i)
		if err := root.Mkdir(name, 0700); err == nil {
			return name, nil
		} else if !os.IsExist(err) {
			return "", fmt.Errorf("creating rollback directory: %w", err)
		}
	}
	return "", errors.New("creating rollback directory: exhausted unique names")
}

func rejectSymlinkComponents(root *os.Root, path string, includeFinal bool) error {
	parts := strings.Split(filepath.Clean(path), string(filepath.Separator))
	for i := range parts {
		if parts[i] == "." || parts[i] == "" {
			continue
		}
		if i == len(parts)-1 && !includeFinal {
			break
		}
		info, err := root.Lstat(filepath.Join(parts[:i+1]...))
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path component %q is a symbolic link", filepath.Join(parts[:i+1]...))
		}
	}
	return nil
}

func rollbackActivation(root *os.Root, destinationName, rollbackName string, moves []activationMove, primary error) error {
	var rollbackErr error
	for i := len(moves) - 1; i >= 0; i-- {
		move := moves[i]
		destination := filepath.Join(destinationName, move.entry)
		if move.promoted {
			if err := root.RemoveAll(destination); err != nil {
				rollbackErr = errors.Join(rollbackErr, fmt.Errorf("removing promoted entry %q: %w", move.entry, err))
				continue
			}
		}
		if move.hadPrevious {
			rollback := filepath.Join(rollbackName, move.entry)
			if err := root.MkdirAll(filepath.Dir(destination), 0755); err != nil {
				rollbackErr = errors.Join(rollbackErr, fmt.Errorf("preparing restore parent for %q: %w", move.entry, err))
				continue
			}
			if err := activationRename(root, rollback, destination); err != nil {
				rollbackErr = errors.Join(rollbackErr, fmt.Errorf("restoring active entry %q: %w", move.entry, err))
			}
		}
	}
	if rollbackErr != nil {
		return errors.Join(primary, fmt.Errorf("rollback artifacts preserved at %q: %w", rollbackName, rollbackErr))
	}
	if err := root.RemoveAll(rollbackName); err != nil {
		return errors.Join(primary, fmt.Errorf("removing rollback directory: %w", err))
	}
	return primary
}
