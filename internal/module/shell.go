package module

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/STAIxBWLB/dotfiles/internal/fileutil"
)

var shellSourcePins = []gitComponentPin{
	{
		Name: "oh-my-zsh-base", Repository: "https://github.com/ohmyzsh/ohmyzsh.git",
		Commit:        "146461f7c6d95f4ba1220559d66eb113418b40a8",
		RequiredPaths: []string{"oh-my-zsh.sh", "lib", "plugins", "themes", "templates", "tools"},
		PreservePaths: []string{"custom"}, Ownership: ohMyZshOwnership(),
	},
	{Name: "zsh-autosuggestions", Repository: "https://github.com/zsh-users/zsh-autosuggestions.git", Commit: "85919cd1ffa7d2d5412f6d3fe437ebdbeeec4fc5"},
	{Name: "zsh-syntax-highlighting", Repository: "https://github.com/zsh-users/zsh-syntax-highlighting.git", Commit: "2fc57d63067c18b1100ecdbf684fa5baf49459d1"},
	{Name: "zsh-completions", Repository: "https://github.com/zsh-users/zsh-completions.git", Commit: "8cd3bd78e8b1f17271cfdd8269074e5557d8d7b8"},
}

var zshCandidatePaths = []string{"/usr/local/bin/zsh", "/opt/homebrew/bin/zsh", "/bin/zsh", "/usr/bin/zsh"}

// shellFile describes a file managed by ShellModule.
type shellFile struct {
	templatePath string // path relative to templates/
	destPath     string // absolute destination path
	isTemplate   bool   // true = Render(), false = ReadStatic()
}

// ShellModule manages Oh My Zsh, plugins, and shell config files.
type ShellModule struct{}

func (m *ShellModule) Name() string { return "shell" }

func (m *ShellModule) files(homeDir string) []shellFile {
	cfg := filepath.Join(homeDir, ".config", "shell")
	return []shellFile{
		{"shell/zshrc.tmpl", filepath.Join(homeDir, ".zshrc"), true},
		{"shell/bashrc.tmpl", filepath.Join(homeDir, ".bashrc"), true},
		{"shell/00-exports.sh.tmpl", filepath.Join(cfg, "00-exports.sh"), true},
		{"shell/05-completion.sh", filepath.Join(cfg, "05-completion.sh"), false},
		{"shell/10-aliases.sh.tmpl", filepath.Join(cfg, "10-aliases.sh"), true},
		{"shell/20-functions.sh", filepath.Join(cfg, "20-functions.sh"), false},
		{"shell/50-tools-init.sh.tmpl", filepath.Join(cfg, "50-tools-init.sh"), true},
	}
}

func (m *ShellModule) omzDir(homeDir string) string {
	return filepath.Join(homeDir, ".oh-my-zsh")
}

func (m *ShellModule) pluginDir(homeDir, name string) string {
	return filepath.Join(homeDir, ".oh-my-zsh", "custom", "plugins", name)
}

func (m *ShellModule) Check(ctx context.Context, rc *RunContext) (*CheckResult, error) {
	var changes []Change
	cfg := rc.Config
	data := cfg.TemplateData(rc.HomeDir)

	for _, pin := range shellSourcePins {
		dir := m.sourceDestination(rc.HomeDir, pin)
		if !rc.Runner.IsDir(dir) || !m.sourcePinMatches(rc, dir, pin) {
			changes = append(changes, Change{
				Description: fmt.Sprintf("install pinned source %s", pin.Name),
				Command:     fmt.Sprintf("git fetch %s %s", pin.Repository, pin.Commit),
			})
		}
	}

	for _, f := range m.files(rc.HomeDir) {
		content, err := m.renderFile(rc, f, data)
		if err != nil {
			return nil, fmt.Errorf("reading template %s: %w", f.templatePath, err)
		}
		if fileutil.NeedsUpdate(rc.Runner, f.destPath, content) {
			changes = append(changes, Change{
				Description: fmt.Sprintf("write %s", f.destPath),
				Command:     fmt.Sprintf("render %s -> %s", f.templatePath, f.destPath),
			})
		}
	}

	return &CheckResult{Satisfied: len(changes) == 0, Changes: changes}, nil
}

func (m *ShellModule) Apply(ctx context.Context, rc *RunContext) (*ApplyResult, error) {
	var messages []string
	cfg := rc.Config
	data := cfg.TemplateData(rc.HomeDir)

	for _, pin := range shellSourcePins {
		dir := m.sourceDestination(rc.HomeDir, pin)
		if !rc.Runner.IsDir(dir) || !m.sourcePinMatches(rc, dir, pin) {
			if err := activateGitComponent(ctx, rc, dir, pin); err != nil {
				return nil, fmt.Errorf("installing pinned source %s: %w", pin.Name, err)
			}
			messages = append(messages, fmt.Sprintf("installed pinned source %s", pin.Name))
		}
	}

	// Config files
	for _, f := range m.files(rc.HomeDir) {
		content, err := m.renderFile(rc, f, data)
		if err != nil {
			return nil, fmt.Errorf("rendering %s: %w", f.templatePath, err)
		}
		written, err := fileutil.EnsureFile(rc.Runner, rc.HomeDir, f.destPath, content, 0644)
		if err != nil {
			return nil, fmt.Errorf("writing %s: %w", f.destPath, err)
		}
		if written {
			messages = append(messages, fmt.Sprintf("wrote %s", f.destPath))
		}
	}

	// Set default shell to zsh
	changed, err := m.ensureZshDefault(ctx, rc)
	if err != nil {
		rc.Runner.Logger.Warn("setting default shell failed", "err", err)
	} else if changed {
		messages = append(messages, "default shell set to zsh")
	}

	return &ApplyResult{Changed: len(messages) > 0, Messages: messages}, nil
}

func (m *ShellModule) sourceDestination(homeDir string, pin gitComponentPin) string {
	if pin.Name == "oh-my-zsh-base" {
		return m.omzDir(homeDir)
	}
	return m.pluginDir(homeDir, pin.Name)
}

func (m *ShellModule) sourcePinMatches(rc *RunContext, dir string, pin gitComponentPin) bool {
	owned := pin.Ownership.currentEntries()
	if len(owned) == 0 {
		marker, err := readComponentPinMarker(filepath.Join(dir, markerFileName(pin.Name)))
		if err != nil {
			return false
		}
		owned = marker.Owned
	}
	marker := componentPinMarker{Schema: componentPinMarkerSchema, Component: pin.Name, Source: pin.Repository, Commit: pin.Commit, Owned: owned}
	marker.Files, _ = hashManagedFiles(dir, owned)
	return verifyInstalledComponent(rc.Runner, dir, marker) == nil
}

func (m *ShellModule) renderFile(rc *RunContext, f shellFile, data map[string]any) ([]byte, error) {
	if f.isTemplate {
		return rc.Template.Render(f.templatePath, data)
	}
	return rc.Template.ReadStatic(f.templatePath)
}

func (m *ShellModule) ensureZshDefault(ctx context.Context, rc *RunContext) (bool, error) {
	// Find zsh path
	zshPath := ""
	for _, candidate := range zshCandidatePaths {
		if rc.Runner.FileExists(candidate) {
			zshPath = candidate
			break
		}
	}
	if zshPath == "" {
		return false, fmt.Errorf("zsh not found")
	}

	isLinux := rc.Config.System != nil && rc.Config.System.OS == "linux"

	if isLinux {
		// On Linux, check /etc/passwd for current shell
		result, err := rc.Runner.Run(ctx, "getent", "passwd", currentUser(rc))
		if err == nil && strings.Contains(result.Stdout, zshPath) {
			return false, nil // already zsh
		}
		_, err = rc.Runner.Run(ctx, "chsh", "-s", zshPath)
		return err == nil, err
	}

	// macOS: use dscl
	result, err := rc.Runner.Run(ctx, "dscl", ".", "-read", fmt.Sprintf("/Users/%s", currentUser(rc)), "UserShell")
	if err == nil && strings.Contains(result.Stdout, zshPath) {
		return false, nil // already zsh
	}

	_, err = rc.Runner.Run(ctx, "chsh", "-s", zshPath)
	return err == nil, err
}

func currentUser(rc *RunContext) string {
	// HomeDir is like /Users/foo or /home/foo
	return filepath.Base(rc.HomeDir)
}
