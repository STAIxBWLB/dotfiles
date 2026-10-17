package module

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	dotexec "github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/template"
)

func TestTerminalModuleShouldManageArchOrca(t *testing.T) {
	m := &TerminalModule{}
	rc := terminalArchTestContext(t, false)

	if !m.shouldManageArchOrca(rc) {
		t.Fatal("selected Orca should be managed on Arch")
	}

	rc.Config.System = &config.SystemInfo{OS: "linux", DistroID: "ubuntu"}
	if m.shouldManageArchOrca(rc) {
		t.Fatal("Orca AUR installer should not run on non-Arch Linux")
	}
}

func TestTerminalModuleMacOrcaInstalledByAppBundle(t *testing.T) {
	m := &TerminalModule{}
	rc := terminalMacTestContext(t)
	applicationsDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(applicationsDir, "Orca.app"), 0o755); err != nil {
		t.Fatal(err)
	}

	if !m.macOrcaInstalledAt(rc, applicationsDir) {
		t.Fatal("Orca.app should satisfy the macOS install check")
	}
}

func TestTerminalModuleInstallMacOrcaAndVerify(t *testing.T) {
	m := &TerminalModule{}
	rc := terminalMacTestContext(t)
	binDir := firstPathDir(t)
	stateFile := filepath.Join(t.TempDir(), "orca-installed")
	argsFile := filepath.Join(t.TempDir(), "brew-args")
	writeExecutable(t, filepath.Join(binDir, "brew"), `#!/bin/sh
if [ "$1" = "list" ] && [ "$2" = "--cask" ] && [ "$3" = "orca" ]; then
  [ -f "$ORCA_BREW_STATE" ]
  exit
fi
if [ "$1" = "install" ]; then
  printf '%s\n' "$*" > "$ORCA_BREW_ARGS"
  : > "$ORCA_BREW_STATE"
  exit 0
fi
exit 1
`)
	t.Setenv("ORCA_BREW_STATE", stateFile)
	t.Setenv("ORCA_BREW_ARGS", argsFile)

	installed, err := m.installMacOrcaAt(context.Background(), rc, t.TempDir())
	if err != nil {
		t.Fatalf("installMacOrcaAt: %v", err)
	}
	if !installed {
		t.Fatal("successful Homebrew install should report a change")
	}
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(data)); got != "install --cask stablyai/orca/orca" {
		t.Fatalf("brew args = %q", got)
	}
}

func TestTerminalModuleArchOrcaInstalledByCommand(t *testing.T) {
	m := &TerminalModule{}
	rc := terminalArchTestContext(t, false)
	writeExecutable(t, filepath.Join(firstPathDir(t), "stably-orca"), "#!/bin/sh\nexit 0\n")

	if !m.archOrcaInstalled(rc) {
		t.Fatal("stably-orca command should satisfy the install check")
	}
}

func TestTerminalModuleArchOrcaInstalledByGitPackage(t *testing.T) {
	m := &TerminalModule{}
	rc := terminalArchTestContext(t, false)
	writeExecutable(t, filepath.Join(firstPathDir(t), "pacman"), `#!/bin/sh
if [ "$1" = "-Q" ] && [ "$2" = "stably-orca-git" ]; then
  exit 0
fi
exit 1
`)

	if !m.archOrcaInstalled(rc) {
		t.Fatal("stably-orca-git package should satisfy the install check")
	}
}

func TestTerminalModuleInstallArchOrcaRequiresYay(t *testing.T) {
	m := &TerminalModule{}
	rc := terminalArchTestContext(t, false)

	installed, err := m.installArchOrca(context.Background(), rc)
	if err == nil || !strings.Contains(err.Error(), "yay is not available") {
		t.Fatalf("installArchOrca error = %v, want missing-yay guidance", err)
	}
	if installed {
		t.Fatal("missing yay must not report Orca installed")
	}
}

func TestTerminalModuleInstallArchOrcaAndVerify(t *testing.T) {
	m := &TerminalModule{}
	rc := terminalArchTestContext(t, true)
	binDir := firstPathDir(t)
	source := filepath.Join(t.TempDir(), "stably-orca")
	target := filepath.Join(binDir, "stably-orca")
	argsFile := filepath.Join(t.TempDir(), "yay-args")
	writeExecutable(t, source, "#!/bin/sh\nexit 0\n")
	writeExecutable(t, filepath.Join(binDir, "yay"), `#!/bin/sh
printf '%s\n' "$*" > "$ORCA_TEST_ARGS"
/bin/cp "$ORCA_TEST_SOURCE" "$ORCA_TEST_TARGET"
/bin/chmod 755 "$ORCA_TEST_TARGET"
`)
	t.Setenv("ORCA_TEST_ARGS", argsFile)
	t.Setenv("ORCA_TEST_SOURCE", source)
	t.Setenv("ORCA_TEST_TARGET", target)

	installed, err := m.installArchOrca(context.Background(), rc)
	if err != nil {
		t.Fatalf("installArchOrca: %v", err)
	}
	if !installed {
		t.Fatal("successful AUR install should report a change")
	}
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(data)); got != "-S --needed --noconfirm stably-orca-bin" {
		t.Fatalf("yay args = %q", got)
	}
}

func TestTerminalModuleCheckPlansMissingYay(t *testing.T) {
	m := &TerminalModule{}
	rc := terminalArchTestContext(t, false)
	rc.Template = template.NewEngine()
	rc.HomeDir = t.TempDir()

	result, err := m.Check(context.Background(), rc)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if result.Satisfied {
		t.Fatal("missing Orca and yay should produce pending changes")
	}
	found := false
	for _, change := range result.Changes {
		if strings.Contains(change.Description, "install yay") &&
			change.Command == "yay -S --needed stably-orca-bin" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing-yay change not found: %#v", result.Changes)
	}
}

func terminalArchTestContext(t *testing.T, yes bool) *RunContext {
	t.Helper()
	binDir := t.TempDir()
	t.Setenv("PATH", binDir)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &RunContext{
		Config: &config.Config{
			System: &config.SystemInfo{OS: "linux", DistroID: "arch"},
			Modules: config.ModulesConfig{
				Terminal: config.TermConfig{
					Enabled: true,
					Apps:    []string{"orca"},
				},
			},
		},
		Runner: dotexec.NewRunner(false, logger),
		Yes:    yes,
	}
}

func terminalMacTestContext(t *testing.T) *RunContext {
	t.Helper()
	binDir := t.TempDir()
	t.Setenv("PATH", binDir)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner := dotexec.NewRunner(false, logger)
	return &RunContext{
		Config: &config.Config{
			System: &config.SystemInfo{OS: "darwin"},
			Modules: config.ModulesConfig{
				Terminal: config.TermConfig{
					Enabled: true,
					Apps:    []string{"orca"},
				},
			},
		},
		Runner: runner,
		Brew:   dotexec.NewBrew(runner),
	}
}

func firstPathDir(t *testing.T) string {
	t.Helper()
	path := os.Getenv("PATH")
	if path == "" {
		t.Fatal("PATH is empty")
	}
	return strings.Split(path, string(os.PathListSeparator))[0]
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}
