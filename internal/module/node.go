package module

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// globalNpmPackages are installed globally when npm is available.
var globalNpmPackages = []string{
	"@tobilu/qmd", // local search engine for markdown/docs
}

// legacyPnpmKeys are pnpm-only keys that used to be written to ~/.npmrc.
// npm 11+ warns about them, so we scrub them from any legacy ~/.npmrc.
var legacyPnpmKeys = []string{
	"virtual-store-dir",
	"store-dir",
	"cache-dir",
}

// NodeModule manages npm/pnpm configuration and global tools.
type NodeModule struct{}

func (m *NodeModule) Name() string { return "node" }

// pnpmNpmrcPath returns ~/.config/pnpm/npmrc — the pnpm-dedicated config path.
func pnpmNpmrcPath(rc *RunContext) string {
	return filepath.Join(rc.HomeDir, ".config", "pnpm", "npmrc")
}

// legacyNpmrcPath returns ~/.npmrc — the path we no longer manage but still clean.
func legacyNpmrcPath(rc *RunContext) string {
	return filepath.Join(rc.HomeDir, ".npmrc")
}

func pnpmNpmrcFile(rc *RunContext) templatedFile {
	return templatedFile{
		templatePath: "node/pnpm-npmrc.tmpl",
		destPath:     pnpmNpmrcPath(rc),
		isTemplate:   true,
		perm:         0644,
	}
}

// pnpmDirs returns the directories pnpm expects for its virtual-store/store/cache.
func pnpmDirs(rc *RunContext) []string {
	return []string{
		filepath.Join(rc.HomeDir, ".config", "pnpm"),
		filepath.Join(rc.HomeDir, ".local", "share", "pnpm", "virtual-store"),
		filepath.Join(rc.HomeDir, ".local", "share", "pnpm", "store"),
		filepath.Join(rc.HomeDir, ".cache", "pnpm"),
	}
}

func (m *NodeModule) Check(ctx context.Context, rc *RunContext) (*CheckResult, error) {
	changes, err := checkTemplatedFiles(rc, []templatedFile{pnpmNpmrcFile(rc)})
	if err != nil {
		return nil, err
	}

	for _, dir := range pnpmDirs(rc) {
		if !rc.Runner.IsDir(dir) {
			changes = append(changes, Change{
				Description: fmt.Sprintf("create directory %s", dir),
				Command:     fmt.Sprintf("mkdir -p %s", dir),
			})
		}
	}

	// Legacy ~/.npmrc cleanup — scrub pnpm-only keys so npm stops warning.
	legacyPath := legacyNpmrcPath(rc)
	if legacyNpmrcNeedsCleanup(rc.Runner, legacyPath) {
		changes = append(changes, Change{
			Description: fmt.Sprintf("scrub pnpm-only keys from legacy %s", legacyPath),
			Command:     "strip virtual-store-dir/store-dir/cache-dir from ~/.npmrc",
		})
	}

	// Node.js install via fnm — required so `npm` is usable after apply.
	fnmAvailable := rc.Runner.CommandExists("fnm")
	nodeInstalled := fnmAvailable && fnmHasInstalledVersion(ctx, rc)
	if fnmAvailable && !nodeInstalled {
		changes = append(changes, Change{
			Description: "install Node.js LTS via fnm",
			Command:     "fnm install --lts && fnm default lts-latest",
		})
	}

	// Global npm packages — install through fnm so the apply process resolves npm
	// without depending on shell-injected PATH from `fnm env`.
	if fnmAvailable && nodeInstalled {
		for _, pkg := range globalNpmPackages {
			if !isNpmPackageInstalled(ctx, rc, pkg) {
				changes = append(changes, Change{
					Description: fmt.Sprintf("install global npm package: %s", pkg),
					Command:     fmt.Sprintf("fnm exec --using=default -- npm install -g %s", pkg),
				})
			}
		}
	} else if fnmAvailable && !nodeInstalled {
		// Node will be installed in Apply; the global packages will follow in the same run.
		for _, pkg := range globalNpmPackages {
			changes = append(changes, Change{
				Description: fmt.Sprintf("install global npm package: %s", pkg),
				Command:     fmt.Sprintf("fnm exec --using=default -- npm install -g %s", pkg),
			})
		}
	} else if rc.Runner.CommandExists("npm") {
		// Fallback: fnm not present, but a system npm is available.
		for _, pkg := range globalNpmPackages {
			if !isNpmPackageInstalledViaNpm(ctx, rc, pkg) {
				changes = append(changes, Change{
					Description: fmt.Sprintf("install global npm package: %s", pkg),
					Command:     fmt.Sprintf("npm install -g %s", pkg),
				})
			}
		}
	}

	return &CheckResult{Satisfied: len(changes) == 0, Changes: changes}, nil
}

// fnmHasInstalledVersion returns true when fnm reports at least one installed Node version.
func fnmHasInstalledVersion(ctx context.Context, rc *RunContext) bool {
	result, err := rc.Runner.RunQuery(ctx, "fnm", "list")
	if err != nil {
		return false
	}
	if result.ExitCode != 0 {
		return false
	}
	return parseFnmHasVersion(result.Stdout)
}

// parseFnmHasVersion inspects `fnm list` output and reports whether any installed
// Node version line is present. fnm prints lines like "* v20.11.0 default" — we
// look for any line containing a "vN" token.
func parseFnmHasVersion(output string) bool {
	for _, line := range strings.Split(output, "\n") {
		for _, tok := range strings.Fields(line) {
			tok = strings.TrimLeft(tok, "*")
			if len(tok) >= 2 && tok[0] == 'v' && tok[1] >= '0' && tok[1] <= '9' {
				return true
			}
		}
	}
	return false
}

// isNpmPackageInstalled checks if a global npm package is installed under the
// fnm-managed default Node version.
func isNpmPackageInstalled(ctx context.Context, rc *RunContext, pkg string) bool {
	result, err := rc.Runner.RunQuery(ctx, "fnm", "exec", "--using=default", "--", "npm", "list", "-g", "--depth=0", pkg)
	if err != nil {
		return false
	}
	return result.ExitCode == 0
}

// isNpmPackageInstalledViaNpm checks via a system-level `npm` (no fnm).
func isNpmPackageInstalledViaNpm(ctx context.Context, rc *RunContext, pkg string) bool {
	result, err := rc.Runner.RunQuery(ctx, "npm", "list", "-g", "--depth=0", pkg)
	if err != nil {
		return false
	}
	return result.ExitCode == 0
}

func (m *NodeModule) Apply(ctx context.Context, rc *RunContext) (*ApplyResult, error) {
	var messages []string

	// Ensure target directories exist
	for _, dir := range pnpmDirs(rc) {
		if !rc.Runner.IsDir(dir) {
			if err := rc.Runner.MkdirAll(dir, 0755); err != nil {
				return nil, fmt.Errorf("creating directory %s: %w", dir, err)
			}
			messages = append(messages, fmt.Sprintf("created %s", dir))
		}
	}

	// Render and write pnpm npmrc
	fileMessages, err := applyTemplatedFiles(rc, []templatedFile{pnpmNpmrcFile(rc)})
	if err != nil {
		return nil, err
	}
	messages = append(messages, fileMessages...)

	// Scrub pnpm-only keys from legacy ~/.npmrc (removes the file if it becomes empty).
	legacyPath := legacyNpmrcPath(rc)
	cleaned, removed, err := cleanLegacyNpmrc(rc.Runner, legacyPath)
	if err != nil {
		return nil, fmt.Errorf("cleaning legacy %s: %w", legacyPath, err)
	}
	if removed {
		messages = append(messages, fmt.Sprintf("removed stale %s (pnpm keys migrated)", legacyPath))
	} else if cleaned {
		messages = append(messages, fmt.Sprintf("scrubbed pnpm-only keys from %s", legacyPath))
	}

	// Ensure a Node.js version is installed via fnm so npm is usable after apply.
	fnmAvailable := rc.Runner.CommandExists("fnm")
	if fnmAvailable && !fnmHasInstalledVersion(ctx, rc) {
		if err := rc.Runner.RunAttached(ctx, "fnm", "install", "--lts"); err != nil {
			messages = append(messages, fmt.Sprintf("⚠ fnm install --lts failed: %v", err))
		} else {
			messages = append(messages, "installed Node.js LTS via fnm")
			if _, err := rc.Runner.Run(ctx, "fnm", "default", "lts-latest"); err != nil {
				messages = append(messages, fmt.Sprintf("⚠ fnm default lts-latest failed: %v", err))
			}
		}
	}

	// Install global npm packages — prefer fnm exec so it works even when shell
	// init hasn't been re-sourced for this process.
	switch {
	case fnmAvailable && fnmHasInstalledVersion(ctx, rc):
		for _, pkg := range globalNpmPackages {
			if isNpmPackageInstalled(ctx, rc, pkg) {
				continue
			}
			if _, err := rc.Runner.Run(ctx, "fnm", "exec", "--using=default", "--", "npm", "install", "-g", pkg); err != nil {
				messages = append(messages, fmt.Sprintf("⚠ failed to install %s: %v", pkg, err))
			} else {
				messages = append(messages, fmt.Sprintf("installed global npm: %s", pkg))
			}
		}
	case rc.Runner.CommandExists("npm"):
		for _, pkg := range globalNpmPackages {
			if isNpmPackageInstalledViaNpm(ctx, rc, pkg) {
				continue
			}
			if _, err := rc.Runner.Run(ctx, "npm", "install", "-g", pkg); err != nil {
				messages = append(messages, fmt.Sprintf("⚠ failed to install %s: %v", pkg, err))
			} else {
				messages = append(messages, fmt.Sprintf("installed global npm: %s", pkg))
			}
		}
	default:
		messages = append(messages, "⚠ npm not found — skipping global package install (install node via fnm first)")
	}

	return &ApplyResult{Changed: len(messages) > 0, Messages: messages}, nil
}

// legacyNpmrcNeedsCleanup returns true when ~/.npmrc exists and contains any pnpm-only key.
func legacyNpmrcNeedsCleanup(runner *exec.Runner, path string) bool {
	if !runner.FileExists(path) {
		return false
	}
	data, err := runner.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if lineHasLegacyPnpmKey(line) {
			return true
		}
	}
	return false
}

// cleanLegacyNpmrc strips pnpm-only keys from ~/.npmrc. Returns (cleaned, removed, err):
//   - cleaned=true when at least one line was removed
//   - removed=true when the file ended up empty and was deleted
func cleanLegacyNpmrc(runner *exec.Runner, path string) (bool, bool, error) {
	if !runner.FileExists(path) {
		return false, false, nil
	}
	data, err := runner.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, false, nil
		}
		return false, false, err
	}

	lines := strings.Split(string(data), "\n")
	kept := make([]string, 0, len(lines))
	cleaned := false
	for _, line := range lines {
		if lineHasLegacyPnpmKey(line) {
			cleaned = true
			continue
		}
		kept = append(kept, line)
	}
	if !cleaned {
		return false, false, nil
	}

	// Also drop the legacy managed-by header so a fully-migrated file is considered empty.
	trimmedKept := dropLegacyHeaderComments(kept)
	if allBlank(trimmedKept) {
		if err := runner.Remove(path); err != nil {
			return false, false, err
		}
		return true, true, nil
	}

	out := strings.Join(trimmedKept, "\n")
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	if err := runner.WriteFile(path, []byte(out), 0644); err != nil {
		return false, false, err
	}
	return true, false, nil
}

func lineHasLegacyPnpmKey(line string) bool {
	trimmed := strings.TrimSpace(line)
	for _, key := range legacyPnpmKeys {
		if strings.HasPrefix(trimmed, key+"=") {
			return true
		}
	}
	return false
}

// dropLegacyHeaderComments removes the two comment lines we used to write above the pnpm keys
// so that a fully-migrated file is considered empty.
func dropLegacyHeaderComments(lines []string) []string {
	legacyHeaders := []string{
		"# Managed by dotfiles-v2 — do not edit manually",
		"# Relocate pnpm virtual store outside Google Drive",
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		matched := false
		for _, h := range legacyHeaders {
			if trimmed == h {
				matched = true
				break
			}
		}
		if !matched {
			out = append(out, line)
		}
	}
	return out
}

func allBlank(lines []string) bool {
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			return false
		}
	}
	return true
}
