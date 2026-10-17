package module

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/STAIxBWLB/dotfiles/internal/fileutil"
	"github.com/STAIxBWLB/dotfiles/internal/ws"
)

// WorkspaceModule manages workspace symlink federation and shell config.
//
// Symlink safety rules:
//   - Only create symlinks when the link path does NOT exist at all.
//   - Never delete, overwrite, or modify existing symlinks or directories.
//   - Broken symlinks are repaired only if an explicit target is configured.
//
// Dual-workspace architecture:
//   - Path (e.g. ~/workspace) is the git workspace (text/md only).
//   - GdriveSymlink (e.g. ~/gdrive-workspace) → Gdrive (Google Drive physical path).
//   - inbox/downloads and inbox/incoming are symlinked to Drive for binary access.
type WorkspaceModule struct{}

func (m *WorkspaceModule) Name() string { return "workspace" }

// expandHome replaces a leading ~/ with the user's home directory.
func (m *WorkspaceModule) expandHome(rc *RunContext, path string) string {
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(rc.HomeDir, path[2:])
	}
	return path
}

func (m *WorkspaceModule) Check(ctx context.Context, rc *RunContext) (*CheckResult, error) {
	var changes []Change
	cfg := rc.Config.Modules.Workspace

	if !cfg.Enabled || cfg.Path == "" {
		return &CheckResult{Satisfied: true}, nil
	}

	// Expand ~ in all config paths for filesystem operations
	workspacePath := m.expandHome(rc, cfg.Path)
	symlink := m.expandHome(rc, cfg.Symlink)
	gdrive := m.expandHome(rc, cfg.Gdrive)
	gdriveSymlink := m.expandHome(rc, cfg.GdriveSymlink)

	// Git repo cloning: check if configured repos need cloning
	vaultPath := m.expandHome(rc, rc.Config.VaultCloneTarget(rc.HomeDir))
	for _, repo := range cfg.Repos {
		repoPath := ws.RepoTarget(workspacePath, repo.Name, vaultPath)
		if !rc.Runner.IsDir(repoPath) {
			changes = append(changes, Change{
				Description: fmt.Sprintf("clone %s into %s/%s", repo.Remote, cfg.Path, repo.Name),
				Command:     fmt.Sprintf("git clone --recurse-submodules %s %s", repo.Remote, repoPath),
			})
		}
	}

	// workspace symlink: only if explicit symlink target is configured
	if symlink != "" && !m.pathUsable(rc, workspacePath) {
		changes = append(changes, Change{
			Description: fmt.Sprintf("symlink %s -> %s", cfg.Path, cfg.Symlink),
			Command:     fmt.Sprintf("ln -sfn %q %q", symlink, workspacePath),
		})
	}

	// Google Drive symlink: gdrive_symlink → gdrive
	if gdriveSymlink != "" && gdrive != "" && !m.pathUsable(rc, gdriveSymlink) {
		changes = append(changes, Change{
			Description: fmt.Sprintf("symlink %s -> %s", cfg.GdriveSymlink, cfg.Gdrive),
			Command:     fmt.Sprintf("ln -sfn %q %q", gdrive, gdriveSymlink),
		})
	}

	// workspace/work/.gdrive → gdrive_symlink/work
	workDir := filepath.Join(workspacePath, "work")
	if rc.Runner.IsDir(workDir) {
		if gdriveSymlink != "" {
			gdriveWork := filepath.Join(gdriveSymlink, "work")
			gdriveXref := filepath.Join(workDir, ".gdrive")
			if m.targetReachable(rc, gdriveWork) && !m.pathUsable(rc, gdriveXref) {
				changes = append(changes, Change{
					Description: fmt.Sprintf("symlink %s -> %s", gdriveXref, gdriveWork),
					Command:     fmt.Sprintf("ln -sfn %q %q", gdriveWork, gdriveXref),
				})
			}
		}

		// inbox symlinks: work/inbox/{downloads,incoming} → gdrive/work/inbox/{downloads,incoming}
		if gdriveSymlink != "" {
			inboxDir := filepath.Join(workDir, "inbox")
			if rc.Runner.IsDir(inboxDir) {
				for _, sub := range []string{"downloads", "incoming"} {
					linkPath := filepath.Join(inboxDir, sub)
					gdriveTarget := filepath.Join(gdriveSymlink, "work", "inbox", sub)
					if m.targetReachable(rc, gdriveTarget) && !m.pathUsable(rc, linkPath) {
						changes = append(changes, Change{
							Description: fmt.Sprintf("symlink %s -> %s", linkPath, gdriveTarget),
							Command:     fmt.Sprintf("ln -sfn %q %q", gdriveTarget, linkPath),
						})
					}
				}
			}
		}
	}

	// shell config file
	shellDest := filepath.Join(rc.HomeDir, ".config", "shell", "40-workspace.sh")
	content, err := rc.Template.Render("shell/40-workspace.sh.tmpl", rc.Config.TemplateData(rc.HomeDir))
	if err != nil {
		return nil, fmt.Errorf("rendering shell/40-workspace.sh.tmpl: %w", err)
	}
	if fileutil.NeedsUpdate(rc.Runner, shellDest, content) {
		changes = append(changes, Change{
			Description: fmt.Sprintf("write %s", shellDest),
			Command:     "render shell/40-workspace.sh.tmpl -> ~/.config/shell/40-workspace.sh",
		})
	}

	return &CheckResult{Satisfied: len(changes) == 0, Changes: changes}, nil
}

func (m *WorkspaceModule) Apply(ctx context.Context, rc *RunContext) (*ApplyResult, error) {
	var messages []string
	cfg := rc.Config.Modules.Workspace

	if !cfg.Enabled || cfg.Path == "" {
		return &ApplyResult{Changed: false, Messages: []string{"workspace path not configured"}}, nil
	}

	// Expand ~ in all config paths for filesystem operations
	workspacePath := m.expandHome(rc, cfg.Path)
	symlink := m.expandHome(rc, cfg.Symlink)
	gdrive := m.expandHome(rc, cfg.Gdrive)
	gdriveSymlink := m.expandHome(rc, cfg.GdriveSymlink)

	// Git repo cloning (before symlinks — dirs must exist first). Delegates to
	// ws.Init which handles gh auth, .gdrive symlink preservation, and fresh vs
	// populated detection. Non-fatal: errors here don't block symlink setup.
	if len(cfg.Repos) > 0 {
		initMsgs, err := ws.Init(ctx, rc.Runner, workspacePath, cfg.Repos, ws.InitOptions{
			Force:     false,
			Yes:       rc.Yes,
			VaultPath: m.expandHome(rc, rc.Config.VaultCloneTarget(rc.HomeDir)),
			Out:       rc.out(),
		})
		messages = append(messages, initMsgs...)
		if err != nil {
			fmt.Fprintf(rc.out(), "  ⚠ workspace: ws.Init: %v (continuing)\n", err)
		}
	}

	// workspace symlink: only create if explicit target is set and path doesn't exist
	if symlink != "" {
		if !m.pathUsable(rc, workspacePath) {
			if !m.targetReachable(rc, symlink) {
				return nil, fmt.Errorf("symlink target does not exist: %s", cfg.Symlink)
			}
			if err := rc.Runner.Symlink(symlink, workspacePath); err != nil {
				return nil, fmt.Errorf("symlinking workspace: %w", err)
			}
			messages = append(messages, fmt.Sprintf("symlinked %s -> %s", cfg.Path, cfg.Symlink))
		}
	}

	// Google Drive symlink
	if gdriveSymlink != "" && gdrive != "" {
		if !m.pathUsable(rc, gdriveSymlink) {
			if !m.targetReachable(rc, gdrive) {
				return nil, fmt.Errorf("gdrive target does not exist: %s", cfg.Gdrive)
			}
			if err := rc.Runner.Symlink(gdrive, gdriveSymlink); err != nil {
				return nil, fmt.Errorf("symlinking gdrive: %w", err)
			}
			messages = append(messages, fmt.Sprintf("symlinked %s -> %s", cfg.GdriveSymlink, cfg.Gdrive))
		}
	}

	// workspace/work/.gdrive → gdrive_symlink/work
	workDir := filepath.Join(workspacePath, "work")
	if rc.Runner.IsDir(workDir) {
		if gdriveSymlink != "" {
			gdriveWork := filepath.Join(gdriveSymlink, "work")
			gdriveXref := filepath.Join(workDir, ".gdrive")
			if m.targetReachable(rc, gdriveWork) && !m.pathUsable(rc, gdriveXref) {
				if err := rc.Runner.Symlink(gdriveWork, gdriveXref); err != nil {
					return nil, fmt.Errorf("symlinking work/.gdrive: %w", err)
				}
				messages = append(messages, fmt.Sprintf("symlinked %s -> %s", gdriveXref, gdriveWork))
			}
		}

		// inbox symlinks
		if gdriveSymlink != "" {
			inboxDir := filepath.Join(workDir, "inbox")
			if rc.Runner.IsDir(inboxDir) {
				for _, sub := range []string{"downloads", "incoming"} {
					linkPath := filepath.Join(inboxDir, sub)
					gdriveTarget := filepath.Join(gdriveSymlink, "work", "inbox", sub)
					if m.targetReachable(rc, gdriveTarget) && !m.pathUsable(rc, linkPath) {
						if err := rc.Runner.Symlink(gdriveTarget, linkPath); err != nil {
							return nil, fmt.Errorf("symlinking inbox/%s: %w", sub, err)
						}
						messages = append(messages, fmt.Sprintf("symlinked %s -> %s", linkPath, gdriveTarget))
					}
				}
			}
		}
	}

	// shell config
	shellDest := filepath.Join(rc.HomeDir, ".config", "shell", "40-workspace.sh")
	content, err := rc.Template.Render("shell/40-workspace.sh.tmpl", rc.Config.TemplateData(rc.HomeDir))
	if err != nil {
		return nil, fmt.Errorf("rendering shell/40-workspace.sh.tmpl: %w", err)
	}
	written, err := fileutil.EnsureFile(rc.Runner, rc.HomeDir, shellDest, content, 0644)
	if err != nil {
		return nil, fmt.Errorf("writing %s: %w", shellDest, err)
	}
	if written {
		messages = append(messages, fmt.Sprintf("wrote %s", shellDest))
	}

	return &ApplyResult{Changed: len(messages) > 0, Messages: messages}, nil
}

// pathUsable returns true if path exists and is functional.
// A valid symlink (pointing to an accessible target), regular directory, or file.
// Returns false for nonexistent paths or broken symlinks.
func (m *WorkspaceModule) pathUsable(rc *RunContext, path string) bool {
	if rc.Runner.IsSymlink(path) {
		target, err := rc.Runner.Readlink(path)
		if err != nil {
			return false
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		return rc.Runner.FileExists(target) || rc.Runner.IsDir(target)
	}
	return rc.Runner.FileExists(path) || rc.Runner.IsDir(path)
}

// targetReachable returns true if path exists and is accessible (follows symlinks).
func (m *WorkspaceModule) targetReachable(rc *RunContext, path string) bool {
	return rc.Runner.FileExists(path) || rc.Runner.IsDir(path)
}
