// Package ws implements dual-workspace directory operations.
//
// The dual-workspace architecture mirrors two parallel trees:
//   - ~/workspace/work/         (git-tracked text)
//   - ~/gdrive-workspace/work/  (Google Drive backed binaries)
//
// This package provides idempotent mkdir/mv/rm that apply to both sides,
// plus audit to detect structural drift.
package ws

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/fileutil"
)

// Roots bundles the two parallel workspace roots (both pointing at the "work" dir).
type Roots struct {
	Work   string // e.g. /Users/yj/workspace/work
	Gdrive string // e.g. /Users/yj/gdrive-workspace/work
}

// Side describes where a directory exists.
type Side int

const (
	SideNone   Side = 0
	SideWork   Side = 1 << 0
	SideGdrive Side = 1 << 1
	SideBoth        = SideWork | SideGdrive
)

// Name returns a human-readable side name.
func (s Side) Name() string {
	switch s {
	case SideWork:
		return "work"
	case SideGdrive:
		return "gdrive"
	case SideBoth:
		return "both"
	case SideNone:
		return "none"
	}
	return "unknown"
}

// Other returns the opposite single-side value (SideWork ↔ SideGdrive).
// Returns SideNone for SideBoth or SideNone.
func (s Side) Other() Side {
	switch s {
	case SideWork:
		return SideGdrive
	case SideGdrive:
		return SideWork
	}
	return SideNone
}

// ValidateRelPath ensures a user-supplied path is safe and relative.
// Rejects: absolute paths, "..", "", ".", leading "/".
// Returns the cleaned relative path.
func ValidateRelPath(p string) (string, error) {
	if p == "" {
		return "", errors.New("path is empty")
	}
	if filepath.IsAbs(p) {
		return "", fmt.Errorf("path %q must be relative", p)
	}
	if strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("path %q must not start with /", p)
	}
	cleaned := filepath.Clean(p)
	if cleaned == "." || cleaned == "" {
		return "", fmt.Errorf("path %q refers to workspace root", p)
	}
	// After Clean, traversal attempts surface as leading ".."
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("path %q must not traverse outside workspace", p)
	}
	return cleaned, nil
}

// ResolvePair returns absolute paths on both sides for a given rel path.
func (r Roots) ResolvePair(rel string) (workAbs, gdriveAbs string) {
	return filepath.Join(r.Work, rel), filepath.Join(r.Gdrive, rel)
}

// DirPresence checks where a directory currently exists (files are ignored).
// Follows symlinks — a symlinked dir that resolves to a real dir counts as present.
func DirPresence(roots Roots, rel string) Side {
	workAbs, gdriveAbs := roots.ResolvePair(rel)
	var s Side
	if fileutil.IsDir(workAbs) {
		s |= SideWork
	}
	if fileutil.IsDir(gdriveAbs) {
		s |= SideGdrive
	}
	return s
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// Mkdir creates rel on both sides (idempotent — skips side that already has it).
// Returns one message per side indicating the action taken.
func Mkdir(runner *exec.Runner, roots Roots, rel string) ([]string, error) {
	rel, err := ValidateRelPath(rel)
	if err != nil {
		return nil, err
	}
	workAbs, gdriveAbs := roots.ResolvePair(rel)
	var msgs []string

	for _, pair := range []struct {
		side Side
		abs  string
	}{{SideWork, workAbs}, {SideGdrive, gdriveAbs}} {
		if fileutil.IsDir(pair.abs) {
			msgs = append(msgs, fmt.Sprintf("  ⋯ %s: already exists (%s)", pair.side.Name(), pair.abs))
			continue
		}
		if exists(pair.abs) {
			return msgs, fmt.Errorf("%s: %s exists but is not a directory", pair.side.Name(), pair.abs)
		}
		if err := runner.MkdirAll(pair.abs, 0755); err != nil {
			return msgs, fmt.Errorf("%s: mkdir %s: %w", pair.side.Name(), pair.abs, err)
		}
		msgs = append(msgs, fmt.Sprintf("  ✓ %s: created %s", pair.side.Name(), pair.abs))
	}
	return msgs, nil
}

// Move renames srcRel → dstRel on every side where src exists.
// Fails if dst exists on either side, or if src exists on neither.
func Move(ctx context.Context, runner *exec.Runner, roots Roots, srcRel, dstRel string) ([]string, error) {
	srcRel, err := ValidateRelPath(srcRel)
	if err != nil {
		return nil, fmt.Errorf("src: %w", err)
	}
	dstRel, err = ValidateRelPath(dstRel)
	if err != nil {
		return nil, fmt.Errorf("dst: %w", err)
	}

	srcPresence := DirPresence(roots, srcRel)
	if srcPresence == SideNone {
		return nil, fmt.Errorf("source %q does not exist on either side", srcRel)
	}
	workSrcAbs, gdriveSrcAbs := roots.ResolvePair(srcRel)
	workDstAbs, gdriveDstAbs := roots.ResolvePair(dstRel)
	if exists(workDstAbs) {
		return nil, fmt.Errorf("destination %q exists on work side", dstRel)
	}
	if exists(gdriveDstAbs) {
		return nil, fmt.Errorf("destination %q exists on gdrive side", dstRel)
	}

	var msgs []string
	for _, pair := range []struct {
		side   Side
		srcAbs string
		dstAbs string
	}{{SideWork, workSrcAbs, workDstAbs}, {SideGdrive, gdriveSrcAbs, gdriveDstAbs}} {
		if srcPresence&pair.side == 0 {
			continue
		}
		// Ensure parent exists
		parent := filepath.Dir(pair.dstAbs)
		if !fileutil.IsDir(parent) {
			if err := runner.MkdirAll(parent, 0755); err != nil {
				return msgs, fmt.Errorf("%s: mkdir parent %s: %w", pair.side.Name(), parent, err)
			}
		}
		if _, err := runner.Run(ctx, "mv", pair.srcAbs, pair.dstAbs); err != nil {
			return msgs, fmt.Errorf("%s: mv: %w", pair.side.Name(), err)
		}
		msgs = append(msgs, fmt.Sprintf("  ✓ %s: moved %s → %s", pair.side.Name(), pair.srcAbs, pair.dstAbs))
	}
	return msgs, nil
}

// Remove deletes rel from both sides. If recursive=false, each side must be an empty directory.
func Remove(ctx context.Context, runner *exec.Runner, roots Roots, rel string, recursive bool) ([]string, error) {
	rel, err := ValidateRelPath(rel)
	if err != nil {
		return nil, err
	}
	presence := DirPresence(roots, rel)
	if presence == SideNone {
		return []string{fmt.Sprintf("  ⋯ %s: does not exist on either side", rel)}, nil
	}
	workAbs, gdriveAbs := roots.ResolvePair(rel)
	var msgs []string

	for _, pair := range []struct {
		side Side
		abs  string
	}{{SideWork, workAbs}, {SideGdrive, gdriveAbs}} {
		if presence&pair.side == 0 {
			continue
		}
		if recursive {
			if _, err := runner.Run(ctx, "rm", "-rf", pair.abs); err != nil {
				return msgs, fmt.Errorf("%s: rm -rf: %w", pair.side.Name(), err)
			}
			msgs = append(msgs, fmt.Sprintf("  ✓ %s: removed %s (recursive)", pair.side.Name(), pair.abs))
		} else {
			if err := runner.Remove(pair.abs); err != nil {
				return msgs, fmt.Errorf("%s: remove %s: %w (pass --recursive for non-empty)", pair.side.Name(), pair.abs, err)
			}
			msgs = append(msgs, fmt.Sprintf("  ✓ %s: removed %s", pair.side.Name(), pair.abs))
		}
	}
	return msgs, nil
}
