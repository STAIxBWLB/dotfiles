package module

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// globalNpmPackages are installed globally when npm is available.
var globalNpmPackages = []string{
	"@tobilu/qmd", // local search engine for markdown/docs
}

// NodeModule manages npm/pnpm configuration and global tools.
type NodeModule struct{}

func (m *NodeModule) Name() string { return "node" }

// pnpmNpmrcPath returns ~/.config/pnpm/npmrc — the pnpm-dedicated config path.
func pnpmNpmrcPath(rc *RunContext) string {
	return filepath.Join(rc.HomeDir, ".config", "pnpm", "npmrc")
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
