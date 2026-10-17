package module

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/STAIxBWLB/dotfiles/internal/fileutil"
)

const (
	orcaTerminalToken   = "orca"
	orcaMacCask         = "stablyai/orca/orca"
	orcaMacAppBundle    = "Orca.app"
	orcaArchPackage     = "stably-orca-bin"
	orcaArchGitPackage  = "stably-orca-git"
	defaultMacAppFolder = "/Applications"
)

// TerminalModule manages terminal configuration files and selected terminal
// apps on platforms with an automatic installer.
type TerminalModule struct{}

func (m *TerminalModule) Name() string { return "terminal" }

func (m *TerminalModule) starshipTemplate(rc *RunContext) string {
	style := rc.Config.Modules.Terminal.PromptStyle
	if style == "" {
		style = "minimal"
	}
	return fmt.Sprintf("starship/starship-%s.toml", style)
}

func (m *TerminalModule) managedFiles(rc *RunContext) []shellFile {
	files := []shellFile{
		{
			templatePath: m.starshipTemplate(rc),
			destPath:     filepath.Join(rc.HomeDir, ".config", "starship.toml"),
			isTemplate:   false,
		},
	}

	cfg := rc.Config
	isDarwin := cfg.System != nil && cfg.System.OS == "darwin"
	if cfg.Modules.Terminal.Warp && isDarwin {
		files = append(files, shellFile{
			templatePath: "warp/dotfiles-v2.yaml",
			destPath:     filepath.Join(rc.HomeDir, ".warp", "themes", "dotfiles-v2.yaml"),
			isTemplate:   false,
		})
	}

	return files
}

func (m *TerminalModule) Check(ctx context.Context, rc *RunContext) (*CheckResult, error) {
	var changes []Change

	if m.shouldManageMacOrca(rc) && !m.macOrcaInstalledAt(rc, defaultMacAppFolder) {
		description := "install Orca with Homebrew"
		if rc.Brew == nil || !rc.Brew.IsAvailable() {
			description = "install Homebrew before installing Orca"
		}
		changes = append(changes, Change{
			Description: description,
			Command:     "brew install --cask " + orcaMacCask,
		})
	}

	if m.shouldManageArchOrca(rc) && !m.archOrcaInstalled(rc) {
		args := m.archOrcaInstallArgs(rc)
		description := "install Orca from the AUR"
		if !rc.Runner.CommandExists("yay") {
			description = "install yay before installing Orca from the AUR"
		}
		changes = append(changes, Change{
			Description: description,
			Command:     strings.Join(append([]string{"yay"}, args...), " "),
		})
	}

	for _, f := range m.managedFiles(rc) {
		content, err := rc.Template.ReadStatic(f.templatePath)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", f.templatePath, err)
		}
		if fileutil.NeedsUpdate(rc.Runner, f.destPath, content) {
			changes = append(changes, Change{
				Description: fmt.Sprintf("write %s", f.destPath),
				Command:     fmt.Sprintf("copy %s -> %s", f.templatePath, f.destPath),
			})
		}
	}

	return &CheckResult{Satisfied: len(changes) == 0, Changes: changes}, nil
}

func (m *TerminalModule) Apply(ctx context.Context, rc *RunContext) (*ApplyResult, error) {
	var messages []string

	installed, err := m.installMacOrcaAt(ctx, rc, defaultMacAppFolder)
	if err != nil {
		return nil, err
	}
	if installed {
		messages = append(messages, "installed Orca with Homebrew")
	}

	installed, err = m.installArchOrca(ctx, rc)
	if err != nil {
		return nil, err
	}
	if installed {
		messages = append(messages, "installed Orca from the AUR")
	}

	for _, f := range m.managedFiles(rc) {
		content, err := rc.Template.ReadStatic(f.templatePath)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", f.templatePath, err)
		}
		written, err := fileutil.EnsureFile(rc.Runner, rc.HomeDir, f.destPath, content, 0644)
		if err != nil {
			return nil, fmt.Errorf("writing %s: %w", f.destPath, err)
		}
		if written {
			messages = append(messages, fmt.Sprintf("wrote %s", f.destPath))
		}
	}

	return &ApplyResult{Changed: len(messages) > 0, Messages: messages}, nil
}

func (m *TerminalModule) wantsOrca(rc *RunContext) bool {
	if rc == nil || rc.Config == nil {
		return false
	}
	for _, app := range rc.Config.Modules.Terminal.Apps {
		if app == orcaTerminalToken {
			return true
		}
	}
	return false
}

func (m *TerminalModule) shouldManageMacOrca(rc *RunContext) bool {
	return rc != nil &&
		rc.Config != nil &&
		rc.Config.System != nil &&
		rc.Config.System.OS == "darwin" &&
		m.wantsOrca(rc)
}

func (m *TerminalModule) shouldManageArchOrca(rc *RunContext) bool {
	if rc == nil || rc.Config == nil || rc.Config.System == nil || !rc.Config.System.IsArchLinux() {
		return false
	}
	return m.wantsOrca(rc)
}

func (m *TerminalModule) macOrcaInstalledAt(rc *RunContext, applicationsDir string) bool {
	if rc.Runner.FileExists(filepath.Join(applicationsDir, orcaMacAppBundle)) {
		return true
	}
	return rc.Brew != nil && rc.Brew.IsCaskInstalled("orca")
}

func (m *TerminalModule) installMacOrcaAt(
	ctx context.Context,
	rc *RunContext,
	applicationsDir string,
) (bool, error) {
	if !m.shouldManageMacOrca(rc) || m.macOrcaInstalledAt(rc, applicationsDir) {
		return false, nil
	}
	if rc.Brew == nil || !rc.Brew.IsAvailable() {
		return false, fmt.Errorf(
			"orca selected but Homebrew is not available; install Homebrew, then run `brew install --cask %s`",
			orcaMacCask,
		)
	}
	if err := rc.Brew.InstallCask(ctx, []string{orcaMacCask}, false); err != nil {
		return false, fmt.Errorf("install Orca with Homebrew: %w", err)
	}
	if !m.macOrcaInstalledAt(rc, applicationsDir) {
		return false, fmt.Errorf(
			"homebrew completed but Orca was not detected; expected cask orca or %s",
			filepath.Join(applicationsDir, orcaMacAppBundle),
		)
	}
	return true, nil
}

func (m *TerminalModule) archOrcaInstalled(rc *RunContext) bool {
	if rc.Runner.CommandExists("stably-orca") {
		return true
	}
	for _, pkg := range []string{orcaArchPackage, orcaArchGitPackage} {
		result, err := rc.Runner.RunQuery(context.Background(), "pacman", "-Q", pkg)
		if err == nil && result.ExitCode == 0 {
			return true
		}
	}
	return false
}

func (m *TerminalModule) archOrcaInstallArgs(rc *RunContext) []string {
	args := []string{"-S", "--needed"}
	if rc.Yes {
		args = append(args, "--noconfirm")
	}
	return append(args, orcaArchPackage)
}

func (m *TerminalModule) installArchOrca(ctx context.Context, rc *RunContext) (bool, error) {
	if !m.shouldManageArchOrca(rc) || m.archOrcaInstalled(rc) {
		return false, nil
	}
	if !rc.Runner.CommandExists("yay") {
		return false, fmt.Errorf(
			"orca selected but yay is not available; install yay, then run `yay -S %s`",
			orcaArchPackage,
		)
	}
	if err := rc.Runner.RunInteractive(ctx, "yay", m.archOrcaInstallArgs(rc)...); err != nil {
		return false, fmt.Errorf("install Orca from the AUR: %w", err)
	}
	if !m.archOrcaInstalled(rc) {
		return false, fmt.Errorf(
			"yay completed but Orca was not detected; expected package %s or command stably-orca",
			orcaArchPackage,
		)
	}
	return true, nil
}
