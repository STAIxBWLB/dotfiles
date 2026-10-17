package module

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"strings"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/container"
	dotexec "github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/fileutil"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

// ContainerModule installs the backend behind the shared `container` command
// (apple/container on macOS, docker or podman on Linux) and writes its shim
// and state file. An existing docker is never installed over or reconfigured.
//
// modules.container.enabled travels with the synced config, but whether a
// host installs a backend is that host's choice, recorded in its state.json:
// dot container setup chooses yes, and dot apply asks a host that has not
// chosen. --yes and runs without a terminal never install on such a host.
type ContainerModule struct {
	// Chosen is set by dot container setup: this host chose to set up.
	Chosen bool
	// confirm answers the host-choice and docker prompts; nil uses ui.Confirm.
	confirm func(prompt string) (bool, error)
}

type hostChoice int

const (
	choiceUndecided hostChoice = iota
	choiceSetUp
	choiceDeclined
)

// hostChoice reads this host's decision from its state.json.
func (m *ContainerModule) hostChoice(rc *RunContext) hostChoice {
	if m.Chosen {
		return choiceSetUp
	}
	switch st := m.loadState(rc); {
	case st == nil || (st.Backend == "" && !st.Declined):
		return choiceUndecided
	case st.Declined:
		return choiceDeclined
	}
	return choiceSetUp
}

// chooseHost asks a host that has not chosen whether to set up the command.
// asked is false when no prompt could run (--yes, no terminal).
func (m *ContainerModule) chooseHost(rc *RunContext) (install, asked bool, err error) {
	if rc.Yes {
		return false, false, nil
	}
	confirm := m.confirm
	if confirm == nil {
		if !ui.TerminalAttached() {
			return false, false, nil
		}
		confirm = func(p string) (bool, error) { return ui.Confirm(p, false) }
	}
	install, err = confirm(hostChoicePrompt)
	return install, true, err
}

func (m *ContainerModule) Name() string { return "container" }

// containerHost is what the probes found. Check reports it; Apply acts on it.
type containerHost struct {
	unsupported string // why this host cannot run the backend; nothing is written
	stop        string // a fix only the user can make; Apply fails with it

	backend string // chosen or existing backend; "" until one is installed
	binary  string

	// macOS
	brewMissing, notInstalled, notRegistered, notRunning, noKernel bool
	// Linux: no usable backend yet, so install docker (offered) or podman.
	installDocker, installPodman bool
}

const (
	hostChoicePrompt = "modules.container is on in your synced config, but this host has not chosen. " +
		"Set up the container command here (on macOS it installs apple/container, about 431 MB)? " +
		"No skips this host until you run dot container setup."
	undecidedNote = "not set up on this host: run dot container setup to install it, " +
		"or dot apply from a terminal to choose"
	dockerOfferPrompt = "No docker or podman found. Install docker with sudo? " +
		"Members of the docker group are root-equivalent. Choosing No installs podman instead."
	reloginNote = "log out and back in so the docker group applies, then use container"
)

func (m *ContainerModule) probe(ctx context.Context, rc *RunContext) (*containerHost, error) {
	cfg := rc.Config.Modules.Container
	backend := cfg.Backend
	if backend == "" {
		backend = container.BackendAuto
	}
	shims := container.ShimsDir(rc.HomeDir)
	h := &containerHost{}
	switch goos := rc.Config.System.OS; goos {
	case "darwin":
		if backend != container.BackendAuto && backend != container.BackendApple {
			return nil, fmt.Errorf("modules.container.backend %q is not available on macOS; use auto or apple", backend)
		}
		if h.unsupported = ContainerUnsupported(ctx, rc.Runner, rc.Config.System); h.unsupported != "" {
			return h, nil
		}
		h.backend = container.BackendApple
		if rc.Brew == nil || !rc.Brew.IsAvailable() {
			h.brewMissing, h.notInstalled = true, true
			return h, nil
		}
		h.binary = appleBinary(shims)
		if h.binary == "" || !rc.Brew.IsInstalled("container") {
			h.notInstalled = true
			return h, nil
		}
		h.notRegistered = !rc.Brew.ServiceRegistered("container")
		res, _ := rc.Runner.RunQuery(ctx, h.binary, "system", "status", "--format", "json")
		status := container.AppleStatus{}
		if res != nil {
			status = container.ParseAppleStatus(res.Stdout)
		}
		h.notRunning = !status.Running()
		h.noKernel = !rc.Runner.FileExists(container.KernelPath(rc.HomeDir, status.Paths.AppRoot))
	case "linux":
		if backend == container.BackendApple {
			return nil, fmt.Errorf("modules.container.backend apple runs on macOS only; use auto, docker or podman")
		}
		docker := container.LookPath("docker", shims)
		podman := container.LookPath("podman", shims)
		switch {
		case backend == container.BackendPodman && podman != "":
			h.backend, h.binary = container.BackendPodman, podman
		case backend == container.BackendPodman:
			h.installPodman = true
		case docker != "":
			qctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			_, err := rc.Runner.RunQuery(qctx, docker, "info")
			cancel()
			switch st := m.loadState(rc); {
			case err == nil:
				h.backend, h.binary = container.BackendDocker, docker
			case backend == container.BackendAuto && st != nil && st.Backend == container.BackendPodman && podman != "":
				// An earlier setup fell back to podman after a failed docker install.
				h.backend, h.binary = container.BackendPodman, podman
			default:
				h.stop = "docker is installed but `docker info` fails for this user; " +
					"run `sudo usermod -aG docker $USER` and log in again (or start the daemon: " +
					"`sudo systemctl enable --now docker`), then rerun dot container setup"
			}
		case backend == container.BackendAuto && podman != "":
			h.backend, h.binary = container.BackendPodman, podman
		default:
			h.installDocker = true
		}
	default:
		h.unsupported = ContainerUnsupported(ctx, rc.Runner, rc.Config.System)
	}
	return h, nil
}

// ContainerUnsupported returns why this host cannot run a container backend,
// or "" when it can. Such a host is reported and skipped: dot writes nothing
// and falls back to no other backend.
func ContainerUnsupported(ctx context.Context, runner *dotexec.Runner, sys *config.SystemInfo) string {
	switch sys.OS {
	case "linux":
		return ""
	case "darwin":
		version := ""
		if res, err := runner.RunQuery(ctx, "sw_vers", "-productVersion"); err == nil {
			version = res.Stdout
		}
		return container.MacUnsupported(sys.Arch, version)
	}
	return fmt.Sprintf("no container backend for %s", sys.OS)
}

// appleBinary resolves apple/container's own binary, never the shim.
func appleBinary(shims string) string {
	if p := container.LookPath("container", shims); p != "" {
		return p
	}
	for _, p := range []string{"/opt/homebrew/bin/container", "/usr/local/bin/container"} {
		if fileutil.Exists(p) {
			return p
		}
	}
	return ""
}

func (m *ContainerModule) Check(ctx context.Context, rc *RunContext) (*CheckResult, error) {
	choice := m.hostChoice(rc)
	if choice == choiceDeclined {
		return &CheckResult{Satisfied: true}, nil
	}
	h, err := m.probe(ctx, rc)
	if err != nil {
		return nil, err
	}
	var changes []Change
	add := func(desc, cmd string) { changes = append(changes, Change{Description: desc, Command: cmd}) }
	if h.unsupported != "" {
		add("unsupported platform, skipped: "+h.unsupported, "")
		return &CheckResult{Changes: changes}, nil
	}
	if choice == choiceUndecided {
		// Only the note: what a yes would install is not this host's plan yet.
		add(undecidedNote, "dot container setup")
		return &CheckResult{Changes: changes}, nil
	}
	switch {
	case h.stop != "":
		add(h.stop, "")
		return &CheckResult{Changes: changes}, nil
	case h.brewMissing:
		add("install Homebrew before apple/container", "")
	case h.notInstalled:
		add("install apple/container with Homebrew", "brew install container")
	case h.installDocker:
		add("install docker (offered; podman if declined)", strings.Join(m.dockerInstallCmds(rc)[0], " "))
	case h.installPodman:
		add("install podman", strings.Join(m.pkgInstall(rc, "podman"), " "))
	}
	if h.notRegistered {
		add("start the container service at login", "brew services start container")
	}
	if h.notRunning {
		add("start the container system", "container system start --disable-kernel-install")
	}
	if h.noKernel {
		add("install the recommended guest kernel", "container system kernel set --recommended")
	}
	if h.binary == "" || fileutil.NeedsUpdate(rc.Runner, container.ShimPath(rc.HomeDir), container.ShimScript(container.DotPath(rc.HomeDir), h.binary)) {
		add("write "+container.ShimPath(rc.HomeDir), "")
	}
	if want := m.state(ctx, rc, h); want == nil || !want.Same(m.loadState(rc)) {
		add("write "+container.StatePath(rc.HomeDir), "")
	}
	if legacySnippetRemovable(rc) {
		add("remove legacy "+container.LegacySnippetPath(rc.HomeDir)+" (replaced by the shim)", "")
	}
	return &CheckResult{Satisfied: len(changes) == 0, Changes: changes}, nil
}

func (m *ContainerModule) Apply(ctx context.Context, rc *RunContext) (*ApplyResult, error) {
	h, err := m.probe(ctx, rc)
	if err != nil {
		return nil, err
	}
	if h.unsupported != "" {
		return &ApplyResult{}, nil
	}
	switch m.hostChoice(rc) {
	case choiceDeclined:
		return &ApplyResult{}, nil
	case choiceUndecided:
		install, asked, err := m.chooseHost(rc)
		if err != nil {
			return nil, err
		}
		if !asked { // Check already printed the undecided note
			return &ApplyResult{}, nil
		}
		if !install {
			st := &container.State{Declined: true, CheckedAt: time.Now().UTC()}
			if _, err := fileutil.EnsureFileAtomic(rc.Runner, rc.HomeDir, container.StatePath(rc.HomeDir), st.Marshal(), 0o644); err != nil {
				return nil, fmt.Errorf("recording the declined choice: %w", err)
			}
			return &ApplyResult{Changed: true, Messages: []string{"declined on this host; dot container setup installs it later"}}, nil
		}
	}
	if h.stop != "" {
		return nil, errors.New(h.stop)
	}
	var msgs []string
	switch rc.Config.System.OS {
	case "darwin":
		if msgs, err = m.applyApple(ctx, rc, h); err != nil {
			return nil, err
		}
	case "linux":
		if h.installDocker || h.installPodman {
			if msgs, err = m.installLinux(ctx, rc, h); err != nil {
				return nil, err
			}
		}
	}

	if written, err := fileutil.EnsureFileAtomic(rc.Runner, rc.HomeDir, container.ShimPath(rc.HomeDir), container.ShimScript(container.DotPath(rc.HomeDir), h.binary), 0o755); err != nil {
		return nil, fmt.Errorf("writing shim: %w", err)
	} else if written {
		msgs = append(msgs, "wrote "+container.ShimPath(rc.HomeDir))
	}
	if want := m.state(ctx, rc, h); want != nil && !want.Same(m.loadState(rc)) {
		want.CheckedAt = time.Now().UTC()
		if _, err := fileutil.EnsureFileAtomic(rc.Runner, rc.HomeDir, container.StatePath(rc.HomeDir), want.Marshal(), 0o644); err != nil {
			return nil, fmt.Errorf("writing container state: %w", err)
		}
		msgs = append(msgs, fmt.Sprintf("backend %s (%s)", want.Backend, want.Binary))
	}
	if legacySnippetRemovable(rc) {
		if err := rc.Runner.Remove(container.LegacySnippetPath(rc.HomeDir)); err != nil {
			return nil, fmt.Errorf("removing legacy snippet: %w", err)
		}
		msgs = append(msgs, "removed legacy "+container.LegacySnippetPath(rc.HomeDir))
	}
	return &ApplyResult{Changed: len(msgs) > 0, Messages: msgs}, nil
}

func (m *ContainerModule) applyApple(ctx context.Context, rc *RunContext, h *containerHost) ([]string, error) {
	var msgs []string
	if h.brewMissing {
		return nil, fmt.Errorf("apple/container needs Homebrew; install it, then rerun dot container setup")
	}
	if h.notInstalled {
		if err := rc.Brew.Install(ctx, []string{"container"}); err != nil {
			return nil, fmt.Errorf("brew install container: %w", err)
		}
		if h.binary = appleBinary(container.ShimsDir(rc.HomeDir)); h.binary == "" {
			return nil, fmt.Errorf("brew installed container but its binary was not found")
		}
		h.notRegistered, h.notRunning = !rc.Brew.ServiceRegistered("container"), true
		msgs = append(msgs, "installed apple/container")
	}
	if h.notRegistered {
		if err := rc.Brew.StartService(ctx, "container"); err != nil {
			return nil, fmt.Errorf("brew services start container: %w", err)
		}
		msgs = append(msgs, "container service starts at login")
	}
	if h.notRunning {
		// Synchronous, unlike the login job: the kernel step needs the API server.
		if _, err := rc.Runner.Run(ctx, h.binary, "system", "start", "--disable-kernel-install"); err != nil {
			return nil, fmt.Errorf("container system start: %w", err)
		}
		res, _ := rc.Runner.RunQuery(ctx, h.binary, "system", "status", "--format", "json")
		if res != nil {
			h.noKernel = !rc.Runner.FileExists(container.KernelPath(rc.HomeDir, container.ParseAppleStatus(res.Stdout).Paths.AppRoot))
		}
		msgs = append(msgs, "started the container system")
	}
	if h.noKernel {
		if err := rc.Runner.RunAttached(ctx, h.binary, "system", "kernel", "set", "--recommended"); err != nil {
			return nil, fmt.Errorf("container system kernel set --recommended: %w", err)
		}
		msgs = append(msgs, "installed the recommended guest kernel")
	}
	return msgs, nil
}

// pkgInstall is the distro install command for pkg, run through sudo.
func (m *ContainerModule) pkgInstall(rc *RunContext, pkg string) []string {
	if rc.Config.System.IsArchLinux() {
		args := []string{"sudo", "pacman", "-S", "--needed"}
		if rc.Yes {
			args = append(args, "--noconfirm")
		}
		return append(args, pkg)
	}
	return []string{"sudo", "apt-get", "install", "-y", pkg}
}

// dockerInstallCmds installs the distro docker package, starts the daemon and
// adds the user to the docker group. dot adds no third-party package source.
func (m *ContainerModule) dockerInstallCmds(rc *RunContext) [][]string {
	pkg := "docker.io"
	if rc.Config.System.IsArchLinux() {
		pkg = "docker"
	}
	return [][]string{
		m.pkgInstall(rc, pkg),
		{"sudo", "systemctl", "enable", "--now", "docker"},
		{"sudo", "usermod", "-aG", "docker", dockerGroupUser(rc)},
	}
}

// dockerGroupUser is who joins the docker group: the invoking user ($USER),
// or the target home's owner on an explicit --home run.
func dockerGroupUser(rc *RunContext) string {
	if !rc.ExplicitHome {
		if u, err := user.Current(); err == nil {
			return u.Username
		}
	}
	return currentUser(rc)
}

func (m *ContainerModule) installLinux(ctx context.Context, rc *RunContext, h *containerHost) ([]string, error) {
	backendPref := rc.Config.Modules.Container.Backend
	interactive := m.confirm != nil || ui.TerminalAttached()
	canSudo := rc.Runner.CommandExists("sudo")
	if canSudo && !interactive {
		_, err := rc.Runner.RunQuery(ctx, "sudo", "-n", "true")
		canSudo = err == nil
	}
	if !rc.Config.System.IsArchLinux() && !rc.Runner.CommandExists("apt-get") {
		return nil, fmt.Errorf("no supported package manager (apt-get or pacman); install docker or podman yourself, then rerun dot container setup")
	}
	if !canSudo || (!rc.Yes && !interactive) {
		return nil, fmt.Errorf("installing a container backend needs sudo and either a terminal or --yes; run one of these, then rerun dot container setup:\n%s", m.manualInstall(rc, h))
	}

	if h.installDocker {
		accept, err := m.ask(rc, dockerOfferPrompt)
		if err != nil {
			return nil, err
		}
		if accept {
			var installErr error
			for _, cmd := range m.dockerInstallCmds(rc) {
				if installErr = rc.Runner.RunInteractive(ctx, cmd[0], cmd[1:]...); installErr != nil {
					break
				}
			}
			if installErr == nil {
				h.backend, h.binary = container.BackendDocker, m.installedBinary("docker")
				return []string{"installed docker; " + reloginNote}, nil
			}
			if backendPref == container.BackendDocker {
				return nil, fmt.Errorf("installing docker: %w", installErr)
			}
			fmt.Fprintf(rc.out(), "  ⚠ container: docker install failed (%v); installing podman instead\n", installErr)
		} else if backendPref == container.BackendDocker {
			return nil, fmt.Errorf("docker install declined, and modules.container.backend is docker")
		}
	}
	cmd := m.pkgInstall(rc, "podman")
	if err := rc.Runner.RunInteractive(ctx, cmd[0], cmd[1:]...); err != nil {
		return nil, fmt.Errorf("installing podman: %w", err)
	}
	h.backend, h.binary = container.BackendPodman, m.installedBinary("podman")
	return []string{"installed podman"}, nil
}

func (m *ContainerModule) manualInstall(rc *RunContext, h *containerHost) string {
	var lines []string
	if h.installDocker {
		lines = append(lines, "  docker (the docker group is root-equivalent):")
		for _, cmd := range m.dockerInstallCmds(rc) {
			lines = append(lines, "    "+strings.Join(cmd, " "))
		}
	}
	if rc.Config.Modules.Container.Backend != container.BackendDocker {
		lines = append(lines, "  podman:", "    "+strings.Join(m.pkgInstall(rc, "podman"), " "))
	}
	return strings.Join(lines, "\n")
}

func (m *ContainerModule) ask(rc *RunContext, prompt string) (bool, error) {
	if rc.Yes {
		return true, nil
	}
	if m.confirm != nil {
		return m.confirm(prompt)
	}
	return ui.Confirm(prompt, false)
}

// installedBinary finds a binary a package just installed, which a fresh
// install puts in /usr/bin even when this process's PATH predates it.
func (m *ContainerModule) installedBinary(name string) string {
	if p := container.LookPath(name, ""); p != "" {
		return p
	}
	return "/usr/bin/" + name
}

// state is the state.json this host should have, or nil while no backend is
// installed yet.
func (m *ContainerModule) state(ctx context.Context, rc *RunContext, h *containerHost) *container.State {
	if h.backend == "" || h.binary == "" {
		return nil
	}
	s := &container.State{Backend: h.backend, Binary: h.binary}
	if res, err := rc.Runner.RunQuery(ctx, h.binary, "--version"); err == nil {
		s.Version, _, _ = strings.Cut(strings.TrimSpace(res.Stdout), "\n")
	}
	return s
}

func (m *ContainerModule) loadState(rc *RunContext) *container.State {
	s, _ := container.LoadState(rc.HomeDir)
	return s
}

// legacySnippetRemovable reports whether the hand-written zsh workaround is
// present byte for byte; an edited copy is left for its owner.
func legacySnippetRemovable(rc *RunContext) bool {
	data, err := os.ReadFile(container.LegacySnippetPath(rc.HomeDir))
	return err == nil && string(data) == container.LegacySnippet
}
