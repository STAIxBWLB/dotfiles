package snapstore

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

type SnapshotInfo struct {
	Version string
	Path    string
}

func NewVersion(t time.Time) string {
	return t.UTC().Format("20060102T150405Z")
}

func UniqueVersion(t time.Time, versionPath func(string) string) (string, error) {
	base := NewVersion(t)
	candidate := base
	for i := 2; i < 100; i++ {
		if _, err := os.Stat(versionPath(candidate)); err != nil && os.IsNotExist(err) {
			return candidate, nil
		}
		candidate = fmt.Sprintf("%s-%d", base, i)
	}
	return "", fmt.Errorf("no free version id near %s", base)
}

func ReadLatestPointer(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func ResolveLatest(latestPointerPath, hostRoot string, versionPath func(string) string, versions func() ([]string, error)) (string, error) {
	if latest, err := ReadLatestPointer(latestPointerPath); err == nil {
		if latest != "" {
			if _, serr := os.Stat(versionPath(latest)); serr == nil {
				return latest, nil
			}
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}

	all, err := versions()
	if err != nil {
		return "", err
	}
	if len(all) == 0 {
		return "", fmt.Errorf("no snapshots under %s", hostRoot)
	}
	return all[0], nil
}

func Prune(runner *exec.Runner, keep int, all []SnapshotInfo, latest string) ([]string, error) {
	if keep < 1 {
		keep = 1
	}
	if len(all) <= keep {
		return nil, nil
	}
	removed := make([]string, 0, len(all)-keep)
	kept := 0
	for _, s := range all {
		if kept < keep || s.Version == latest {
			kept++
			continue
		}
		if err := runner.RemoveAll(s.Path); err != nil {
			return removed, err
		}
		removed = append(removed, s.Version)
	}
	return removed, nil
}

func PreRestoreDir(homeDir string, parts []string, t time.Time) string {
	baseParts := []string{homeDir, ".local", "share", "dotfiles", "backup"}
	baseParts = append(baseParts, parts...)
	baseParts = append(baseParts, NewVersion(t))
	base := filepath.Join(baseParts...)
	dir := base
	for i := 2; ; i++ {
		// Any stat error other than "exists" ends the probe: a persistent
		// error (e.g. EACCES on an ancestor) would repeat for every suffix
		// and loop forever — return the candidate and let the caller's
		// mkdir/copy surface the real error.
		if _, err := os.Stat(dir); err != nil {
			return dir
		}
		dir = fmt.Sprintf("%s-%d", base, i)
	}
}

func CopyFile(runner *exec.Runner, src, dst string, mode os.FileMode) (int64, error) {
	if runner.DryRun {
		runner.Logger.Info("dry-run: copy", "src", src, "dst", dst)
		return 0, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 0, err
	}
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	_ = os.Remove(tmp)
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode&0o777)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	return n, nil
}

func WriteYAML(runner *exec.Runner, path string, v any) error {
	data, err := yaml.Marshal(v)
	if err != nil {
		return err
	}
	if err := runner.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return runner.WriteFile(path, data, 0o644)
}

func ReadYAML[T any](path string) (*T, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var v T
	if err := yaml.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

func ListHosts(root, subtree string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, subtree))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, en := range entries {
		if en.IsDir() {
			out = append(out, en.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}
