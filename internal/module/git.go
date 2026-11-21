package module

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/STAIxBWLB/dotfiles/internal/aisettings"
	"github.com/STAIxBWLB/dotfiles/internal/gittransport"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

// GitModule manages git configuration files.
type GitModule struct{}

func (m *GitModule) Name() string { return "git" }

func (m *GitModule) files(rc *RunContext) []templatedFile {
	return []templatedFile{
		{
			templatePath: "git/config.tmpl",
			destPath:     filepath.Join(rc.HomeDir, ".config", "git", "config"),
			isTemplate:   true,
			perm:         0644,
			// The template owns the file wholesale; keep a core.hooksPath dot
			// does not manage across the rewrite (the coauthor guard's config
			// hooks coexist with it).
			postRender: func(rc *RunContext, rendered []byte) ([]byte, error) {
				existing, err := os.ReadFile(filepath.Join(rc.HomeDir, ".config", "git", "config"))
				if os.IsNotExist(err) {
					return rendered, nil
				}
				if err != nil {
					return nil, err
				}
				return []byte(aisettings.PreserveForeignHooksPath(string(rendered), string(existing), rc.HomeDir)), nil
			},
		},
		{
			templatePath: "git/gitignore.global",
			destPath:     filepath.Join(rc.HomeDir, ".config", "git", "gitignore.global"),
			isTemplate:   false,
			perm:         0644,
		},
	}
}

// legacyGitIgnore is the static content dot deployed to ~/.config/git/ignore
// before gitignore.global became the managed excludesFile (#142). The file is
// removed on apply only when it still matches this content byte for byte; a
// locally edited copy is left alone.
const legacyGitIgnore = `.DS_Store
Thumbs.db
*.swp
*.swo
*~
.env
.env.local
.venv/
__pycache__/
node_modules/
.idea/
.vscode/
*.pyc

**/.claude/settings.local.json
`

func legacyIgnorePath(rc *RunContext) string {
	return filepath.Join(rc.HomeDir, ".config", "git", "ignore")
}

// legacyIgnoreRemovable reports whether ~/.config/git/ignore exists and is
// safe to delete: present and byte-identical to what dot deployed. A missing
// file and a diverged file both report false.
func legacyIgnoreRemovable(rc *RunContext) (present bool, removable bool, err error) {
	content, err := os.ReadFile(legacyIgnorePath(rc))
	if errors.Is(err, fs.ErrNotExist) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return true, string(content) == legacyGitIgnore, nil
}

// legacyIgnoreCustomLines returns the active pattern lines a diverged legacy
// ignore file carries beyond the content dot deployed — the lines that stop
// applying when core.excludesFile moves to gitignore.global. Comments and
// blank lines are not patterns and are left out.
func legacyIgnoreCustomLines(rc *RunContext) []string {
	content, err := os.ReadFile(legacyIgnorePath(rc))
	if err != nil {
		return nil
	}
	managed := map[string]bool{}
	for line := range strings.Lines(legacyGitIgnore) {
		managed[strings.TrimSpace(line)] = true
	}
	var custom []string
	for line := range strings.Lines(string(content)) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || managed[line] {
			continue
		}
		custom = append(custom, line)
	}
	return custom
}

// transportFileMissing reports whether the GitHub transport mode file the
// config template includes is absent. Without GithubUser the template
// includes nothing, so there is nothing to write.
func transportFileMissing(rc *RunContext) (bool, error) {
	if rc.Config.GithubUser == "" {
		return false, nil
	}
	_, err := os.Stat(gittransport.Path(rc.HomeDir))
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	return false, err
}

func (m *GitModule) Check(ctx context.Context, rc *RunContext) (*CheckResult, error) {
	changes, err := checkTemplatedFiles(rc, m.files(rc))
	if err != nil {
		return nil, err
	}

	_, removable, err := legacyIgnoreRemovable(rc)
	if err != nil {
		return nil, fmt.Errorf("reading legacy git ignore: %w", err)
	}
	if removable {
		changes = append(changes, Change{
			Description: fmt.Sprintf("remove legacy %s (superseded by gitignore.global)", legacyIgnorePath(rc)),
			Command:     "dot apply --module git",
		})
	}

	if missing, err := transportFileMissing(rc); err != nil {
		return nil, err
	} else if missing {
		changes = append(changes, Change{
			Description: "write " + gittransport.Path(rc.HomeDir) + " (ssh mode)",
			Command:     "dot apply --module git",
		})
	}

	if mode := rc.Config.Modules.Git.CoauthorGuard; mode != "" && mode != aisettings.CoauthorGuardOff {
		manager := aisettings.NewCoauthorGuardManager(rc.Runner, rc.HomeDir)
		status, err := manager.Status(mode)
		if err != nil {
			return nil, fmt.Errorf("coauthor guard status: %w", err)
		}
		if status.HookDrift != "in-sync" {
			changes = append(changes, Change{
				Description: fmt.Sprintf("write %s", status.HookPath),
				Command:     "dot ai coauthor-guard apply",
			})
		}
		if status.GitHooksSupported && status.HookConfigDrift != "in-sync" {
			changes = append(changes, Change{
				Description: "configure git hook.coauthor-guard in " + status.GitConfigPath,
				Command:     "dot ai coauthor-guard apply",
			})
		}
		if status.HooksPathLeftover != "" {
			changes = append(changes, Change{
				Description: "remove dot-managed core.hooksPath from " + status.GitConfigPath,
				Command:     "dot ai coauthor-guard apply",
			})
		}
		if status.AgentsDrift != "in-sync" {
			changes = append(changes, Change{
				Description: fmt.Sprintf("apply coauthor guard AGENTS instruction (%s)", status.AgentsDrift),
				Command:     "dot ai coauthor-guard apply",
			})
		}
	}

	return &CheckResult{Satisfied: len(changes) == 0, Changes: changes}, nil
}

func (m *GitModule) Apply(ctx context.Context, rc *RunContext) (*ApplyResult, error) {
	var messages []string

	fileMessages, err := applyTemplatedFiles(rc, m.files(rc))
	if err != nil {
		return nil, err
	}
	messages = append(messages, fileMessages...)

	present, removable, err := legacyIgnoreRemovable(rc)
	if err != nil {
		return nil, fmt.Errorf("reading legacy git ignore: %w", err)
	}
	switch {
	case present && removable:
		if !rc.DryRun {
			// Recheck immediately before deleting: the byte-for-byte rule is
			// the only thing protecting a locally edited copy, and the first
			// read is already a few operations old.
			_, stillRemovable, rerr := legacyIgnoreRemovable(rc)
			if rerr != nil {
				return nil, fmt.Errorf("rechecking legacy git ignore: %w", rerr)
			}
			if !stillRemovable {
				fmt.Fprintf(rc.progressOut(), "  %s git: kept %s (changed during apply; remove by hand)\n", ui.MarkerColumn(ui.RenderMarker(ui.MarkWarn)), legacyIgnorePath(rc))
				break
			}
			if err := os.Remove(legacyIgnorePath(rc)); err != nil {
				return nil, fmt.Errorf("removing legacy git ignore: %w", err)
			}
		}
		messages = append(messages, fmt.Sprintf("removed legacy %s (superseded by gitignore.global)", legacyIgnorePath(rc)))
	case present:
		// The excludesFile pointer just moved to gitignore.global, so any
		// pattern the user added to the legacy file stops applying with this
		// apply. Name those patterns explicitly: silently orphaning them can
		// re-expose files the user ignored on purpose (credentials included).
		fmt.Fprintf(rc.progressOut(), "  %s git: kept %s (local edits; git no longer reads it)\n", ui.MarkerColumn(ui.RenderMarker(ui.MarkWarn)), legacyIgnorePath(rc))
		for _, line := range legacyIgnoreCustomLines(rc) {
			fmt.Fprintf(rc.out(), "      orphaned pattern: %s\n", line)
		}
		fmt.Fprintln(rc.out(), "      move custom patterns into the dotfiles gitignore.global source or a per-repo .gitignore, then remove the file by hand")
	}

	// The included mode file must exist for GitHub URLs to keep today's ssh
	// rewrite. Apply stays offline: it never probes and never replaces a mode
	// that dot git transport chose.
	if missing, err := transportFileMissing(rc); err != nil {
		return nil, err
	} else if missing {
		if !rc.DryRun {
			if _, err := gittransport.Write(rc.HomeDir, gittransport.ModeSSH); err != nil {
				return nil, fmt.Errorf("writing git transport mode: %w", err)
			}
		}
		messages = append(messages, "wrote "+gittransport.Path(rc.HomeDir)+" (ssh mode)")
	}

	if mode := rc.Config.Modules.Git.CoauthorGuard; mode != "" && mode != aisettings.CoauthorGuardOff {
		manager := aisettings.NewCoauthorGuardManager(rc.Runner, rc.HomeDir)
		result, err := manager.Apply(aisettings.CoauthorGuardOptions{Mode: mode, DryRun: rc.DryRun})
		if err != nil {
			return nil, fmt.Errorf("applying coauthor guard: %w", err)
		}
		if result.HookChanged {
			messages = append(messages, fmt.Sprintf("wrote %s", result.Status.HookPath))
		}
		if result.ConfigChanged {
			messages = append(messages, fmt.Sprintf("configured git hook.coauthor-guard in %s", result.Status.GitConfigPath))
		}
		if result.Warning != "" {
			fmt.Fprintf(rc.progressOut(), "  %s git: %s\n", ui.MarkerColumn(ui.RenderMarker(ui.MarkWarn)), result.Warning)
		}
	}

	return &ApplyResult{Changed: len(messages) > 0, Messages: messages}, nil
}
