package ui

import (
	"bufio"
	"context"
	"os"
	osexec "os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/fileutil"
)

// detectGitConfig reads a value from git config (global).
func detectGitConfig(key string) string {
	res, err := exec.NewProbeRunner().RunQuery(context.Background(), "git", "config", "--global", "--get", key)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(res.Stdout)
}

// detectGithubUser attempts to get the current user from gh CLI.
func detectGithubUser() string {
	if _, err := osexec.LookPath("gh"); err != nil {
		return ""
	}
	res, err := exec.NewProbeRunner().RunQuery(context.Background(), "gh", "api", "user", "--jq", ".login")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(res.Stdout)
}

// detectTimezone reads the system timezone.
func detectTimezone() string {
	// Try /etc/localtime symlink (Linux + macOS)
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		// e.g., /var/db/timezone/zoneinfo/Asia/Seoul or /usr/share/zoneinfo/Asia/Seoul
		for _, prefix := range []string{"/var/db/timezone/zoneinfo/", "/usr/share/zoneinfo/"} {
			if strings.HasPrefix(target, prefix) {
				return strings.TrimPrefix(target, prefix)
			}
		}
		// Fallback: take last 2 segments
		parts := strings.Split(target, "/")
		if len(parts) >= 2 {
			return parts[len(parts)-2] + "/" + parts[len(parts)-1]
		}
	}
	// Try $TZ env
	if tz := os.Getenv("TZ"); tz != "" {
		return tz
	}
	// Try /etc/timezone (Debian/Ubuntu)
	if data, err := os.ReadFile("/etc/timezone"); err == nil {
		return strings.TrimSpace(string(data))
	}
	return ""
}

// detectWorkspacePath checks common workspace locations.
func detectWorkspacePath() string {
	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join(home, "workspace"),
		filepath.Join(home, "Workspace"),
		filepath.Join(home, "work"),
	}
	for _, c := range candidates {
		if fi, err := os.Lstat(c); err == nil && fi.IsDir() || fi != nil && fi.Mode()&os.ModeSymlink != 0 {
			// Return with ~ prefix for portability
			return "~/" + filepath.Base(c)
		}
	}
	return ""
}

// detectVaultCandidates returns existing vault directories under the
// workspace root, most preferred first, each rendered in ~-form when it
// lives under the user's home.
func detectVaultCandidates(workspacePath string) []string {
	home, _ := os.UserHomeDir()
	expanded := fileutil.ExpandHome(workspacePath)
	var out []string
	for _, rel := range []string{"work/vault", "vault"} {
		p := filepath.Join(expanded, rel)
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			out = append(out, tildePath(home, p))
		}
	}
	return out
}

// tildePath renders p in ~-form when it lives under home.
func tildePath(home, p string) string {
	if rel, err := filepath.Rel(home, p); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
		return "~/" + rel
	}
	return p
}

// detectCloudMounts lists cloud storage roots usable as the workspace mirror,
// Dropbox candidates first (same precedence as the secrets backup detector).
// Duplicates reached through home symlinks resolve to one canonical entry.
func detectCloudMounts(home string) []string {
	return detectCloudMountsIn(home, "/Volumes")
}

// detectCloudMountsIn is the testable core; volumesDir is injectable so unit
// tests stay hermetic on hosts with real /Volumes cloud mounts.
func detectCloudMountsIn(home, volumesDir string) []string {
	var dropbox, drive []string
	seen := map[string]bool{}
	add := func(list *[]string, path string) {
		// Only offer candidates that resolve to an existing directory —
		// broken symlinks or stray files would fail later at apply time.
		if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
			return
		}
		key := path
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			key = resolved
		}
		if seen[key] {
			return
		}
		seen[key] = true
		*list = append(*list, path)
	}
	// Canonical macOS location first, so it wins dedup over home symlinks.
	cloudRoot := filepath.Join(home, "Library", "CloudStorage")
	if entries, err := os.ReadDir(cloudRoot); err == nil {
		for _, e := range entries {
			name := e.Name()
			switch {
			case strings.HasPrefix(name, "Dropbox"):
				add(&dropbox, filepath.Join(cloudRoot, name))
			case strings.HasPrefix(name, "GoogleDrive"):
				path := filepath.Join(cloudRoot, name)
				if fi, err := os.Stat(filepath.Join(path, "My Drive")); err == nil && fi.IsDir() {
					path = filepath.Join(path, "My Drive")
				}
				add(&drive, path)
			}
		}
	}
	// Legacy home-dir mounts and symlinks (DriveFS "My Drive (...)" dirs).
	if entries, err := os.ReadDir(home); err == nil {
		for _, e := range entries {
			name := e.Name()
			switch {
			case strings.HasPrefix(name, "Dropbox"):
				add(&dropbox, filepath.Join(home, name))
			case strings.HasPrefix(name, "My Drive") || strings.Contains(name, "GoogleDrive"):
				add(&drive, filepath.Join(home, name))
			}
		}
	}
	// Old-style /Volumes Drive mounts.
	if entries, err := os.ReadDir(volumesDir); err == nil {
		for _, e := range entries {
			if strings.Contains(e.Name(), "GoogleDrive") || strings.Contains(e.Name(), "Google Drive") {
				add(&drive, filepath.Join(volumesDir, e.Name()))
			}
		}
	}
	return append(dropbox, drive...)
}

// defaultCloudSymlink picks the conventional symlink name for a cloud root.
func defaultCloudSymlink(cloudPath string) string {
	if strings.Contains(cloudPath, "Dropbox") {
		return "~/Dropbox"
	}
	return "~/gdrive-workspace"
}

// detectSSHKeys finds existing SSH key names in home/.ssh/.
func detectSSHKeys(home string) []string {
	sshDir := filepath.Join(home, ".ssh")
	entries, err := os.ReadDir(sshDir)
	if err != nil {
		return nil
	}

	var keys []string
	seen := make(map[string]bool)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasSuffix(name, ".pub") || strings.HasSuffix(name, ".age") {
			continue
		}
		if name == "config" || name == "known_hosts" || name == "authorized_keys" ||
			name == "authorized_age_keys" || name == "environment" || name == "agent" ||
			strings.HasPrefix(name, "config.") || strings.HasPrefix(name, "known_hosts.") ||
			strings.HasPrefix(name, "age_key") {
			continue
		}
		if !fileutil.Exists(filepath.Join(sshDir, name+".pub")) {
			continue
		}
		if !seen[name] {
			seen[name] = true
			keys = append(keys, name)
		}
	}

	sort.Slice(keys, func(i, j int) bool {
		iEd := strings.Contains(keys[i], "ed25519")
		jEd := strings.Contains(keys[j], "ed25519")
		if iEd != jEd {
			return iEd
		}
		return keys[i] < keys[j]
	})

	return keys
}

// detectAgeKeys finds existing age identity files in home/.ssh/.
func detectAgeKeys(home string) []string {
	sshDir := filepath.Join(home, ".ssh")
	entries, err := os.ReadDir(sshDir)
	if err != nil {
		return nil
	}

	var keys []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasSuffix(name, ".pub") {
			continue
		}
		if strings.HasPrefix(name, "age_key") && !strings.HasSuffix(name, ".pub") {
			keys = append(keys, filepath.Join("~/.ssh", name))
		}
	}
	sort.Strings(keys)
	return keys
}

// readAgePublicKey attempts to read an age public key from the resolved home.
func readAgePublicKey(home, identityPath string) string {
	expanded := fileutil.ExpandHomeFor(identityPath, home)
	pubPath := expanded + ".pub"
	f, err := os.Open(pubPath)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "age1") {
			return line
		}
	}
	return ""
}

// readSymlinkTarget returns the target of a symlink, or "" if not a symlink.
func readSymlinkTarget(path string) string {
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return ""
	}
	target, err := os.Readlink(path)
	if err != nil {
		return ""
	}
	return target
}
