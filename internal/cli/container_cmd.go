package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/container"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/fileutil"
	"github.com/STAIxBWLB/dotfiles/internal/module"
	"github.com/STAIxBWLB/dotfiles/internal/template"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

func newContainerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "container",
		Short: "Shared container command (apple/container on macOS, docker or podman on Linux)",
		Long: `One ` + "`container`" + ` command with Apple container syntax on every host.

On macOS it runs apple/container (Apple silicon, macOS 26+). On Linux a shim
translates the same syntax for docker, or podman when no docker is usable.
The module is opt-in: dot container setup enables it in user state. That
opt-in syncs with your config, but each host chooses whether to install: dot
apply asks a host that has not chosen (from a terminal only; --yes and
scheduled runs never install there), and dot container setup is a yes.

Config (~/.config/dotfiles/config.yaml):
  modules:
    container:
      backend: auto   # auto | apple | docker | podman
      dns: []         # default --dns servers, e.g. [1.1.1.1, 1.0.0.1]`,
	}
	cmd.AddCommand(newContainerSetupCmd(), newContainerStatusCmd(), newContainerExecCmd())
	return cmd
}

func newContainerSetupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Install the backend and write the container shim",
		Long: `Set up the container command on this host: install the backend and write
the shim and its state file, then enable the container module in user state
and render the shim's PATH entry (the shell module). Afterwards it is
equivalent to dot apply --module container. A host the module cannot set up
keeps its config and shell files untouched.

The opt-in in user state syncs to your other machines, but each host chooses
for itself: running setup is this host's yes, and it overrides an earlier no.

On Linux an existing, usable docker is used as is. With no docker or podman,
setup offers to install the distro docker package (--yes accepts) and falls
back to podman when declined or when the docker install fails.`,
		Args: cobra.NoArgs,
		RunE: runContainerSetup,
	}
}

func runContainerSetup(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	p := printerFrom(cmd)
	yes, _ := cmd.Flags().GetBool("yes")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	if os.Getenv("DOTFILES_YES") == "true" {
		yes = true
	}
	homeOverride := homeOverrideFrom(cmd)
	home := homeFor(cmd)

	state, err := loadStateFor(homeOverride)
	if err != nil {
		return fmt.Errorf("loading state: %w", err)
	}
	sysInfo, err := config.DetectSystem()
	if err != nil {
		return fmt.Errorf("detecting system: %w", err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	runner := exec.NewRunner(dryRun, logger)

	if reason := module.ContainerUnsupported(ctx, runner, sysInfo); reason != "" {
		p.Line("container: unsupported platform, skipped: %s", reason)
		return nil
	}

	cs := &state.Modules.Container
	changed := !cs.Enabled
	cs.Enabled = true
	if sysInfo.OS == "darwin" && len(cs.DNS) == 0 && warpHoldsDNS(ctx, runner) {
		p.Line("Cloudflare WARP holds port 53, so DNS inside containers fails without --dns (apple/container#402).")
		accept, err := ui.ConfirmBool(fmt.Sprintf("Default containers to --dns %s?", strings.Join(container.SuggestedDNS, " --dns ")), true, yes)
		if err != nil && !errors.Is(err, ui.ErrNoTerminal) {
			return err
		}
		if accept && err == nil {
			cs.DNS = append([]string(nil), container.SuggestedDNS...)
			changed = true
		} else {
			p.Line("  hint: set modules.container.dns: [%s] or pass --dns per call", strings.Join(container.SuggestedDNS, ", "))
		}
	}

	profileName, _ := cmd.Flags().GetString("profile")
	configPath, _ := cmd.Flags().GetString("config")
	if profileName == "" {
		profileName = os.Getenv("DOTFILES_PROFILE")
	}
	if profileName == "" {
		profileName = state.Profile
	}
	if profileName == "" && configPath == "" {
		profileName = sysInfo.SuggestProfile()
	}
	cfg, err := config.Load(profileName, configPath, sysInfo)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	applyStateWithToolingAuthority(cfg, state, configPath != "")
	config.ApplyEnvOverrides(cfg)

	rc := &module.RunContext{
		ExplicitHome: homeOverride != "",
		Config:       cfg,
		Runner:       runner,
		Brew:         exec.NewBrew(runner),
		Template:     template.NewEngine(),
		DryRun:       dryRun,
		Yes:          yes,
		HomeDir:      home,
		Out:          p.Out,
	}
	// The container module runs first, so a host it cannot set up (unusable
	// docker, no sudo) keeps its config and shell files untouched.
	registry := module.NewRegistry()
	registry.Register(&module.ContainerModule{Chosen: true}) // running setup is this host's choice
	if err := module.RunAll(ctx, registry.Resolve(cfg, []string{"container"}), rc); err != nil {
		return err
	}
	if changed {
		if err := saveStateFor(p, homeOverride, state, dryRun); err != nil {
			return err
		}
	}
	// The shell module renders the PATH entry that puts the shim first.
	if err := module.RunAll(ctx, registry.Resolve(cfg, []string{"shell"}), rc); err != nil {
		return err
	}
	if !dryRun && container.LookPath("container", "") != container.ShimPath(home) {
		p.Line("Open a new shell so PATH picks up %s.", container.ShimsDir(home))
	}
	return nil
}

func newContainerStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the container backend, shim, DNS defaults and legacy snippet",
		Args:  cobra.NoArgs,
		RunE:  runContainerStatus,
	}
	cmd.Flags().Bool("probe", false, "Also run container run --rm alpine nslookup example.com")
	return cmd
}

func runContainerStatus(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	p := printerFrom(cmd)
	homeOverride := homeOverrideFrom(cmd)
	home := homeFor(cmd)
	runner := exec.NewProbeRunner()
	sysInfo, err := config.DetectSystem()
	if err != nil {
		return fmt.Errorf("detecting system: %w", err)
	}

	p.Header("Container")
	if reason := module.ContainerUnsupported(ctx, runner, sysInfo); reason != "" {
		p.KV("Platform", "unsupported: "+reason)
		return nil
	}
	userState, err := loadStateFor(homeOverride)
	if err != nil {
		return fmt.Errorf("loading state: %w", err)
	}
	cs := userState.Modules.Container
	p.KV("Module", map[bool]string{true: "enabled", false: "disabled (run dot container setup)"}[cs.Enabled])

	st, err := container.LoadState(home)
	if err != nil {
		return err
	}
	switch {
	case st != nil && st.Declined:
		p.KV("This host", "declined (dot container setup installs it here)")
	case (st == nil || st.Backend == "") && cs.Enabled:
		p.KV("This host", "not chosen (dot apply asks from a terminal; dot container setup installs)")
	case st != nil && st.Backend != "":
		p.KV("This host", "set up")
	}
	backend, binary := container.Resolve(home, sysInfo.OS, st)
	switch {
	case backend == "":
		p.KV("Backend", "none found")
	case st == nil || st.Backend == "":
		p.KV("Backend", fmt.Sprintf("%s (%s; not set up)", backend, binary))
	default:
		p.KV("Backend", fmt.Sprintf("%s (%s)", st.Backend, st.Binary))
		p.KV("Version", st.Version)
	}
	if backend == container.BackendApple {
		res, _ := runner.RunQuery(ctx, binary, "system", "status", "--format", "json")
		running := res != nil && container.ParseAppleStatus(res.Stdout).Running()
		p.KV("Service", map[bool]string{true: "running", false: "stopped (container system start)"}[running])
	}

	shim := container.ShimPath(home)
	switch resolved := container.LookPath("container", ""); {
	case !fileutil.Exists(shim):
		p.KV("Shim", "missing (run dot container setup)")
	case resolved == shim:
		p.KV("Shim", shim+" (resolves)")
	default:
		p.KV("Shim", fmt.Sprintf("%s (command -v container is %q; open a new shell)", shim, resolved))
	}

	dns := "backend default"
	if len(cs.DNS) > 0 {
		dns = strings.Join(cs.DNS, ", ")
	}
	p.KV("DNS", dns)
	if sysInfo.OS == "darwin" {
		warp := "not on port 53"
		if warpHoldsDNS(ctx, runner) {
			warp = "holds port 53 (container DNS needs --dns)"
		}
		p.KV("WARP", warp)
	}

	if data, err := os.ReadFile(container.LegacySnippetPath(home)); err == nil {
		note := "edited; remove it by hand, its container() function shadows the shim"
		if string(data) == container.LegacySnippet {
			note = "unchanged; dot container setup removes it"
		}
		p.KV("Legacy", container.LegacySnippetPath(home)+" ("+note+")")
	}

	if probe, _ := cmd.Flags().GetBool("probe"); probe {
		if !fileutil.Exists(shim) {
			return fmt.Errorf("probe needs the shim; run dot container setup")
		}
		p.Line("\n$ container run --rm alpine nslookup example.com")
		if err := runner.RunAttached(ctx, shim, "run", "--rm", "alpine", "nslookup", "example.com"); err != nil {
			return fmt.Errorf("probe failed: %w", err)
		}
	}
	return nil
}

func newContainerExecCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "exec -- <container args>",
		Short:              "Run Apple-syntax container args on this host's backend (called by the shim)",
		Hidden:             true,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 && args[0] == "--" {
				args = args[1:]
			}
			var dns []string
			if us, err := loadStateFor(homeOverrideFrom(cmd)); err == nil {
				dns = us.Modules.Container.DNS
			}
			return containerExec(homeFor(cmd), args, dns)
		},
	}
}

// containerExec replaces this process with the backend, so exit codes,
// signals and TTYs pass through untouched.
func containerExec(home string, args, dns []string) error {
	st, _ := container.LoadState(home)
	backend, binary := container.Resolve(home, runtime.GOOS, st)
	if binary == "" {
		return fmt.Errorf("no container backend found; run dot container setup")
	}
	t, err := container.Argv(backend, args, dns)
	if err != nil {
		return err
	}
	if t.Note != "" {
		fmt.Fprintln(os.Stderr, t.Note)
		return nil
	}
	argv := append([]string{binary}, t.Args...)
	if os.Getenv("DOT_CONTAINER_TRACE") == "1" {
		fmt.Fprintln(os.Stderr, "+ "+strings.Join(argv, " "))
	}
	return syscall.Exec(binary, argv, os.Environ())
}

func warpHoldsDNS(ctx context.Context, runner *exec.Runner) bool {
	res, err := runner.RunQuery(ctx, "scutil", "--dns")
	return err == nil && container.WarpHoldsDNS(res.Stdout)
}

func loadStateFor(homeOverride string) (*config.UserState, error) {
	if homeOverride != "" {
		return config.LoadStateForHome(homeOverride)
	}
	return config.LoadState()
}

func saveStateFor(p *Printer, homeOverride string, state *config.UserState, dryRun bool) error {
	path := config.StatePath()
	if homeOverride != "" {
		path = config.StatePathForHome(homeOverride)
	}
	if dryRun {
		p.Line("dry-run: would write %s", path)
		return nil
	}
	var err error
	if homeOverride != "" {
		err = config.SaveStateForHome(homeOverride, state)
	} else {
		err = config.SaveState(state)
	}
	if err != nil {
		return fmt.Errorf("saving state: %w", err)
	}
	p.Line("✓ container: saved modules.container in %s", path)
	return nil
}
