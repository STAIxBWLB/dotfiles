package module

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/fileutil"
)

const componentPinMarkerSchema = 1

var expectedOhMyZshOwnedEntries = []string{
	".devcontainer", ".editorconfig", ".github", ".gitignore", ".prettierrc",
	"CODE_OF_CONDUCT.md", "CONTRIBUTING.md", "LICENSE.txt", "README.md", "SECURITY.md",
	"cache", "lib", "log", "oh-my-zsh.sh", "plugins", "templates", "themes", "tools",
}

// retiredOhMyZshOwnedEntries records paths removed from the current manifest.
// A release removing an entry from expectedOhMyZshOwnedEntries must add that
// exact entry here in the same change, or the obsolete managed path remains.
var retiredOhMyZshOwnedEntries = []string{}

// componentOwnership is the compiled ownership identity for a managed source.
// Current entries are installed by this binary; Retired entries may be removed
// only when a trusted marker proves an older binary owned them.
type componentOwnership struct {
	Current []string
	Retired []string
}

func (o componentOwnership) currentEntries() []string {
	return append([]string(nil), o.Current...)
}

func (o componentOwnership) trustedEntries() map[string]struct{} {
	entries := make(map[string]struct{}, len(o.Current)+len(o.Retired))
	for _, entry := range o.Current {
		entries[entry] = struct{}{}
	}
	for _, entry := range o.Retired {
		entries[entry] = struct{}{}
	}
	return entries
}

func (o componentOwnership) isEmpty() bool { return len(o.Current) == 0 }

// gitComponentPin identifies one executable upstream tree. Its marker is the
// only freshness authority; timestamps deliberately have no role here.
type gitComponentPin struct {
	Name          string
	Repository    string
	Commit        string
	MarkerPath    string
	RequiredPaths []string
	PreservePaths []string
	Ownership     componentOwnership
}

// fontAssetPin identifies an uploaded release asset by immutable bytes.
type fontAssetPin struct {
	Family    string
	Tag       string
	AssetName string
	URL       string
	SHA256    string
}

// componentPinMarker is intentionally deterministic so matching content can
// be accepted while offline. Files maps relative managed-file or symlink paths
// to type-separated hashes.
type componentPinMarker struct {
	Schema    int               `json:"schema"`
	Component string            `json:"component"`
	Source    string            `json:"source"`
	Commit    string            `json:"commit,omitempty"`
	Owned     []string          `json:"owned"`
	Files     map[string]string `json:"files"`
}

func (m componentPinMarker) validate() error {
	if m.Schema != componentPinMarkerSchema || m.Component == "" || m.Source == "" || len(m.Owned) == 0 || len(m.Files) == 0 {
		return errors.New("invalid component pin marker")
	}
	if !sort.StringsAreSorted(m.Owned) {
		return errors.New("component pin marker ownership is not sorted")
	}
	for i, entry := range m.Owned {
		if entry == "" || !filepath.IsLocal(entry) || (i > 0 && m.Owned[i-1] == entry) {
			return fmt.Errorf("invalid component marker owned entry %q", entry)
		}
	}
	return nil
}

func ohMyZshOwnership() componentOwnership {
	return componentOwnership{
		Current: append([]string(nil), expectedOhMyZshOwnedEntries...),
		Retired: append([]string(nil), retiredOhMyZshOwnedEntries...),
	}
}

func staleOwnedEntries(previous, desired []string) []string {
	desiredSet := make(map[string]struct{}, len(desired))
	for _, entry := range desired {
		desiredSet[entry] = struct{}{}
	}
	var stale []string
	for _, entry := range previous {
		if _, ok := desiredSet[entry]; !ok {
			stale = append(stale, entry)
		}
	}
	sort.Strings(stale)
	return stale
}

// trustedStaleOwnedEntries returns entries eligible for deletion only when the
// prior marker identifies the same compiled source and its ownership stays
// within the immutable ownership union compiled into this binary. A marker is
// integrity evidence for an installed component, not authority to nominate
// arbitrary operator paths for removal. The check is all-or-nothing: filtering
// one untrusted entry while deleting another lets a tampered marker partially
// authorize removal, including a legitimately retired path beside it.
func trustedStaleOwnedEntries(previous componentPinMarker, pin gitComponentPin, desired []string) []string {
	if previous.Component != pin.Name || previous.Source != pin.Repository || pin.Ownership.isEmpty() {
		return nil
	}
	allowed := pin.Ownership.trustedEntries()
	for _, entry := range previous.Owned {
		if _, ok := allowed[entry]; !ok {
			return nil
		}
	}
	return staleOwnedEntries(previous.Owned, desired)
}

func legacyRefreshRequiresInstall(hasRefresh, hasMarker bool) bool { return hasRefresh && !hasMarker }

func readComponentPinMarker(markerPath string) (componentPinMarker, error) {
	data, err := os.ReadFile(markerPath)
	if err != nil {
		return componentPinMarker{}, err
	}
	var marker componentPinMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		return componentPinMarker{}, fmt.Errorf("decoding marker: %w", err)
	}
	if err := marker.validate(); err != nil {
		return componentPinMarker{}, err
	}
	return marker, nil
}

func writeComponentPinMarker(markerPath string, marker componentPinMarker) error {
	if err := marker.validate(); err != nil {
		return err
	}
	data, err := json.Marshal(marker)
	if err != nil {
		return fmt.Errorf("encoding component pin marker: %w", err)
	}
	return os.WriteFile(markerPath, append(data, '\n'), 0600)
}

// verifyInstalledComponent performs no network or command operation. Any
// damaged, legacy, or mismatched active state is deliberately untrusted.
func verifyInstalledComponent(_ *exec.Runner, root string, desired componentPinMarker) error {
	if err := desired.validate(); err != nil {
		return fmt.Errorf("invalid desired marker: %w", err)
	}
	marker, err := readComponentPinMarker(filepath.Join(root, markerFileName(desired.Component)))
	if err != nil {
		return fmt.Errorf("marker unavailable: %w", err)
	}
	if marker.Schema != desired.Schema || marker.Component != desired.Component || marker.Source != desired.Source || marker.Commit != desired.Commit || !sameStrings(marker.Owned, desired.Owned) {
		return fmt.Errorf("component %q identity mismatch", desired.Component)
	}
	files, err := hashManagedFiles(root, desired.Owned)
	if err != nil {
		return err
	}
	if !sameFileHashes(files, marker.Files) || !sameFileHashes(files, desired.Files) {
		return fmt.Errorf("component %q managed-file digest mismatch", desired.Component)
	}
	return nil
}

func markerFileName(component string) string {
	if component == "oh-my-zsh-base" {
		return ".dotfiles-omz-base.json"
	}
	return ".dotfiles-" + component + ".json"
}

func sameStrings(a, b []string) bool {
	return len(a) == len(b) && func() bool {
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}()
}

func sameFileHashes(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for path, hash := range a {
		if b[path] != hash {
			return false
		}
	}
	return true
}

func hashManagedFiles(root string, owned []string) (map[string]string, error) {
	files := make(map[string]string)
	for _, entry := range owned {
		full := filepath.Join(root, entry)
		if _, err := os.Lstat(full); err != nil {
			return nil, fmt.Errorf("required managed path %q unavailable: %w", entry, err)
		}
		err := filepath.WalkDir(full, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			h := sha256.New()
			switch {
			case d.Type().IsRegular():
				f, err := os.Open(path)
				if err != nil {
					return err
				}
				defer f.Close()
				if _, err := io.Copy(h, f); err != nil {
					return err
				}
			case d.Type()&os.ModeSymlink != 0:
				target, err := os.Readlink(path)
				if err != nil {
					return fmt.Errorf("reading managed symlink %q: %w", path, err)
				}
				resolved := filepath.Clean(filepath.Join(filepath.Dir(path), target))
				relativeTarget, err := filepath.Rel(root, resolved)
				if err != nil || !filepath.IsLocal(relativeTarget) {
					return fmt.Errorf("managed symlink %q escapes component root", path)
				}
				if _, err := os.Lstat(resolved); err != nil {
					return fmt.Errorf("managed symlink %q target unavailable: %w", path, err)
				}
				_, _ = h.Write([]byte("symlink\x00"))
				_, _ = h.Write([]byte(filepath.ToSlash(target)))
			default:
				return fmt.Errorf("managed path %q has unsupported type", path)
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			files[filepath.ToSlash(rel)] = hex.EncodeToString(h.Sum(nil))
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if len(files) == 0 {
		return nil, errors.New("managed component has no regular files")
	}
	return files, nil
}

var runPinnedGit = func(ctx context.Context, runner *exec.Runner, args ...string) (*exec.Result, error) {
	return runner.Run(ctx, "git", args...)
}

// stageGitComponent uses only argv-owned git commands and verifies the full
// object identity before any layout check or promotion. It never executes the
// staged content.
func stageGitComponent(ctx context.Context, rc *RunContext, pin gitComponentPin, stage string) error {
	if len(pin.Commit) != 40 {
		return fmt.Errorf("component %q has invalid compiled commit %q", pin.Name, pin.Commit)
	}
	if _, err := runPinnedGit(ctx, rc.Runner, "clone", "--no-checkout", "--depth", "1", pin.Repository, stage); err != nil {
		return fmt.Errorf("staging %s clone: %w", pin.Name, err)
	}
	if _, err := runPinnedGit(ctx, rc.Runner, "-C", stage, "fetch", "--depth", "1", "origin", pin.Commit); err != nil {
		return fmt.Errorf("staging %s fetch: %w", pin.Name, err)
	}
	if _, err := runPinnedGit(ctx, rc.Runner, "-C", stage, "checkout", "--detach", pin.Commit); err != nil {
		return fmt.Errorf("staging %s checkout: %w", pin.Name, err)
	}
	result, err := runPinnedGit(ctx, rc.Runner, "-C", stage, "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("staging %s revision: %w", pin.Name, err)
	}
	if strings.TrimSpace(result.Stdout) != pin.Commit {
		return fmt.Errorf("component %q source %s expected commit %s, observed %s; retry with the compiled pin or bump the source manifest in a new release", pin.Name, pin.Repository, pin.Commit, escapeControl(strings.TrimSpace(result.Stdout)))
	}
	return validateComponentLayout(stage, pin.RequiredPaths)
}

func validateComponentLayout(root string, required []string) error {
	for _, path := range required {
		if _, err := os.Lstat(filepath.Join(root, path)); err != nil {
			return fmt.Errorf("required path %q unavailable: %w", path, err)
		}
	}
	return nil
}

func escapeControl(value string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, value)
}

func stageOwnedEntries(stage string, explicit []string) ([]string, error) {
	if len(explicit) != 0 {
		return append([]string(nil), explicit...), nil
	}
	entries, err := os.ReadDir(stage)
	if err != nil {
		return nil, err
	}
	owned := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Name() != ".git" {
			owned = append(owned, entry.Name())
		}
	}
	sort.Strings(owned)
	if len(owned) == 0 {
		return nil, errors.New("staged component has no owned entries")
	}
	return owned, nil
}

func activateGitComponent(ctx context.Context, rc *RunContext, destination string, pin gitComponentPin) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		return fmt.Errorf("preparing component parent: %w", err)
	}
	stage, err := os.MkdirTemp(filepath.Dir(destination), "."+filepath.Base(destination)+".stage-")
	if err != nil {
		return err
	}
	if err := os.Remove(stage); err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := stageGitComponent(ctx, rc, pin, stage); err != nil {
		return err
	}
	owned, err := stageOwnedEntries(stage, pin.Ownership.currentEntries())
	if err != nil {
		return err
	}
	markerName := markerFileName(pin.Name)
	files, err := hashManagedFiles(stage, owned)
	if err != nil {
		return err
	}
	marker := componentPinMarker{Schema: componentPinMarkerSchema, Component: pin.Name, Source: pin.Repository, Commit: pin.Commit, Owned: owned, Files: files}
	if err := writeComponentPinMarker(filepath.Join(stage, markerName), marker); err != nil {
		return err
	}
	var stale []string
	if previous, err := readComponentPinMarker(filepath.Join(destination, markerName)); err == nil {
		stale = trustedStaleOwnedEntries(previous, pin, owned)
	}
	activationOwned := append(append([]string(nil), owned...), markerName)
	if err := fileutil.ActivateOwnedComponent(rc.Runner, fileutil.ActivationOptions{
		DestinationRoot: destination, StagedRoot: stage, OwnedEntries: activationOwned, StaleEntries: stale,
		Validate: func(root string) error { return validateComponentLayout(root, pin.RequiredPaths) },
	}); err != nil {
		return fmt.Errorf("activating %s: %w", pin.Name, err)
	}
	return nil
}
