package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/admission"
	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/remote"
)

// newRemoteCmd builds `dot remote`: heavy Linux-portable work moved to an
// ssh-reachable host without moving the source off this machine.
func newRemoteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remote",
		Short: "Run builds and tests in a container on an ssh-reachable host",
		Long: `Mirror a local git checkout to a host you can ssh to and run a command
there in a container. The local machine stays the only place source is edited:
a run sends git's view of the tree (tracked files plus untracked ones no ignore
rule covers), build outputs and secrets never leave the laptop, and nothing is
ever copied back. Results come back as the command's output and exit code.

Nothing is enabled until ~/.config/dotfiles/config.yaml carries a 'remote:'
block (see docs/guides/servers.md). A run holds two admission leases: one per
mirror path, so a repository and each of its worktrees run at most one job at a
time, and one per host, at most max_runs of them. Both show in
'dot admit status'. A run defers with exit 75 (EX_TEMPFAIL) when a lease is
held or the host's own load is at or over its limit; --wait retries a held
lease first, and DOT_ADMISSION_IGNORE=remote-load (or a
'dot admit relax --ignore remote-load' override) steps past the load check for
one run without relaxing anything local.

Interrupting a run removes its container. After a kill the next run of the
same path removes the one left behind.

'dot remote verify' runs a repo's own gate on the exact commit (or, with
--dirty, the working tree as it is) in a fresh checkout on the host, and
'dot remote src' clones and analyzes third-party code only on the host.`,
		SilenceUsage: true,
	}
	cmd.PersistentFlags().String("host", "", "remote host to use (default: the matching project's host, then remote.default)")
	cmd.PersistentFlags().Duration("wait", 0, "retry a held lease for this long before deferring (default 0: defer at once)")
	cmd.AddCommand(newRemoteSetupCmd())
	cmd.AddCommand(newRemoteRunCmd())
	cmd.AddCommand(newRemoteVerifyCmd())
	cmd.AddCommand(newRemoteSrcCmd())
	cmd.AddCommand(newRemoteStatusCmd())
	cmd.AddCommand(newRemotePruneCmd())
	cmd.AddCommand(newRemoteJobCmd())
	return cmd
}

// remoteSession loads the config and the seams every 'dot remote' subcommand
// needs, and resolves the host. checkout is the resolved path a job targets,
// which decides the host when --host is not given; it is empty for a command
// that addresses the host itself.
func remoteSession(cmd *cobra.Command, checkout string) (remote.Config, remote.Host, remote.Deps, error) {
	cfg, deps, err := remoteBase(cmd)
	if err != nil {
		return remote.Config{}, remote.Host{}, remote.Deps{}, err
	}
	name, _ := cmd.Flags().GetString("host")
	host, err := cfg.HostFor(name, checkout)
	if err != nil {
		return remote.Config{}, remote.Host{}, remote.Deps{}, err
	}
	return cfg, host, deps, nil
}

// remoteBase loads the config and wires the seams, without choosing a host.
// A run needs it that way round: the checkout it targets is what selects the
// project's host, and resolving the checkout needs the deps.
//
// --config is refused rather than ignored: it names a sync config (profile,
// packages, modules) and the remote block lives in the user state, so reading it
// as the state path would decode a document of the wrong shape, and ignoring it
// would build or pull on the default file's host while the person is reading
// another file. --home is the flag that picks a different state document.
func remoteBase(cmd *cobra.Command) (remote.Config, remote.Deps, error) {
	if configPath, _ := cmd.Flags().GetString("config"); configPath != "" {
		return remote.Config{}, remote.Deps{}, fmt.Errorf("dot remote reads its hosts from the user state, and --config names a sync config, which has no remote block (%s): drop --config, or pass --home (or DOTFILES_HOME) to read another home's state", configPath)
	}
	home := homeFor(cmd)
	var state *config.UserState
	var err error
	if override := homeOverrideFrom(cmd); override != "" {
		state, err = config.LoadStateForHome(override)
	} else {
		state, err = config.LoadState()
	}
	if err != nil {
		return remote.Config{}, remote.Deps{}, fmt.Errorf("loading config: %w", err)
	}
	cfg, err := remote.Load(state.Remote, home)
	if err != nil {
		return remote.Config{}, remote.Deps{}, err
	}
	deps := remote.DefaultDeps(watchdogRunner(false))
	deps.In, deps.Out, deps.Err = cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr()
	return cfg, deps, nil
}

// remoteIgnore resolves the rules a remote run steps past. It reads only the
// relax override and the environment, so an unrelated admission key that fails
// validation cannot block remote work.
func remoteIgnore(store *admission.Store) (admission.IgnoreSet, string, error) {
	var set admission.IgnoreSet
	source := ""
	if spec := strings.TrimSpace(os.Getenv(admission.IgnoreEnv)); spec != "" {
		parsed, err := admission.ParseIgnoreSet(spec)
		if err != nil {
			return admission.IgnoreSet{}, "", fmt.Errorf("%s=%q: %w", admission.IgnoreEnv, spec, err)
		}
		set = set.Union(parsed)
		source = admission.IgnoreEnv
	}
	if store == nil {
		return set, source, nil
	}
	override, err := store.LoadRelaxOverride(false)
	if err != nil {
		return set, source, err
	}
	if override == nil {
		return set, source, nil
	}
	relaxed, err := override.IgnoreSet()
	if err != nil {
		return set, source, err
	}
	if relaxed.Any() && source == "" {
		source = "dot admit relax"
	}
	return set.Union(relaxed), source, nil
}

func newRemoteSetupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Check the host and build or pull the dev image",
		Long: `Check that the host answers over ssh in batch mode and has git, rsync 3.2.3
or newer, and a container runtime, then build the image from your Dockerfile
(with the host user's UID/GID as build arguments) or pull the prebuilt ref.
Re-run it after editing the Dockerfile: it is the only thing that rebuilds the
image. dot ships no image of its own; the servers guide carries a starter.`,
		Args:         cobra.NoArgs,
		RunE:         runRemoteSetup,
		SilenceUsage: true,
	}
	cmd.Flags().Bool("json", false, "print the report as JSON")
	cmd.Flags().String("profile", "", "build or pull a job profile's image instead of the dev image (remote.hosts.<host>.jobs.<profile>)")
	return cmd
}

func runRemoteSetup(cmd *cobra.Command, _ []string) error {
	_, host, deps, err := remoteSession(cmd, "")
	if err != nil {
		return err
	}
	if cmd.Flags().Changed("profile") {
		name, _ := cmd.Flags().GetString("profile")
		profile, perr := host.Profile(name)
		if perr != nil {
			return perr
		}
		host = profile.ImageHost(host)
	}
	p := printerFrom(cmd)
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	asJSON, _ := cmd.Flags().GetBool("json")
	report, err := deps.Setup(cmd.Context(), host, remote.SetupOptions{DryRun: dryRun, JSON: asJSON})
	if err != nil {
		return err
	}
	if asJSON {
		data, merr := json.MarshalIndent(report, "", "  ")
		if merr != nil {
			return merr
		}
		p.Line("%s", data)
		return nil
	}
	if dryRun {
		return nil
	}
	p.Header("dot remote setup")
	p.KV("Host", fmt.Sprintf("%s (%s, %s)", report.Host, report.SSH, report.Runtime))
	p.KV("User", report.User)
	p.KV("CPUs", strconv.Itoa(report.CPUs))
	p.KV("Git", report.Git)
	p.KV("Rsync", report.RsyncVersion)
	p.KV("Runtime", report.RuntimeVersion)
	action := "pulled"
	if report.Built {
		action = "built"
	}
	p.KV("Image", report.Image+" ("+action+", runs as "+report.ImageUser+")")
	p.Success("%s is ready for `dot remote run`", report.Host)
	return nil
}

func newRemoteRunCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run [PATH] -- COMMAND [ARG...]",
		Short: "Mirror a checkout to the host and run a command in a container there",
		Long: `Mirror the checkout at PATH (default: the current directory) and run CMD in
a container with the mirror at /work/<PATH relative to remote.local_root>. One
CMD argument is a shell script; several are an argv. Caches named in the host's
'caches' block are mounted, so dependency downloads survive between runs.

Everything the command writes stays on the host: the mirror keeps build
outputs between runs and nothing comes back. PATH must be below
remote.local_root, and may not be that root or $HOME.

-p PORT publishes the container's PORT on the host's 127.0.0.1 only and
forwards local 127.0.0.1:PORT to it over the run's own ssh connection, for the
life of the run (repeatable). A port already taken here fails the run.

--cgroup gives the command a writable cgroup v2 subtree, its path in
DOT_REMOTE_CGROUP: the container starts as root with CAP_SYS_ADMIN and a private
cgroup namespace only to set the subtree up, then drops to the image's own user
with every capability removed before the command runs. The host must use
cgroup v2 and let the runtime grant CAP_SYS_ADMIN with AppArmor unconfined.`,
		Args:         cobra.ArbitraryArgs,
		RunE:         runRemoteRun,
		SilenceUsage: true,
	}
	cmd.Flags().Bool("json", false, "print the defer record as JSON")
	cmd.Flags().IntSliceP("port", "p", nil, "publish PORT on the host loopback and forward it to local 127.0.0.1:PORT (repeatable)")
	cmd.Flags().Bool("cgroup", false, "delegate a writable cgroup v2 subtree to the command (path in DOT_REMOTE_CGROUP)")
	return cmd
}

func runRemoteRun(cmd *cobra.Command, _ []string) error {
	p := printerFrom(cmd)
	path, command, err := splitRemoteArgs(cmd)
	if err != nil {
		return err
	}
	cfg, deps, err := remoteBase(cmd)
	if err != nil {
		return err
	}
	// The checkout is resolved before the host is chosen: with no PATH the
	// default is the checkout the current directory belongs to, and a project
	// entry's host applies to it. Choosing the host first would only ever see
	// remote.default for that case. Resolved here rather than in the run because
	// the config's project match compares resolved paths.
	checkout, err := remote.ResolveCheckout(cmd.Context(), deps, path)
	if err != nil {
		return err
	}
	hostName, _ := cmd.Flags().GetString("host")
	ignore, source, err := remoteIgnore(deps.Store)
	if err != nil {
		return err
	}
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	wait, _ := cmd.Flags().GetDuration("wait")
	if wait < 0 {
		return fmt.Errorf("--wait must not be negative")
	}
	ports, _ := cmd.Flags().GetIntSlice("port")
	for _, port := range ports {
		if port < 1 || port > 65535 {
			return fmt.Errorf("-p %d is not a TCP port (1-65535)", port)
		}
	}
	cgroup, _ := cmd.Flags().GetBool("cgroup")
	err = deps.Run(cmd.Context(), cfg, remote.RunOptions{
		Path:      checkout,
		HostName:  hostName,
		Command:   command,
		Wait:      wait,
		Ignore:    ignore,
		IgnoreSrc: source,
		DryRun:    dryRun,
		Home:      homeFor(cmd),
		Ports:     ports,
		Cgroup:    cgroup,
	})
	var deferred *remote.Deferred
	if errors.As(err, &deferred) {
		asJSON, _ := cmd.Flags().GetBool("json")
		return remoteDeferExit(p, deferred, asJSON)
	}
	return err
}

func newRemoteVerifyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "verify [PATH] [--dirty] [-- COMMAND [ARG...]]",
		Short: "Run a repo's gate on its exact commit in a fresh checkout on the host",
		Long: `Push PATH's exact commit (HEAD, or with --dirty a throwaway commit of the
working tree) to an owner-only bare repository on the host, check it out fresh
in a container next to the base branch, and run COMMAND there. Without a
command, the matching remote.projects entry's 'verify' runs. The real git
history lets a repo's own gate (diff against main, commit checks) run unchanged.

--dirty stages the working tree (tracked files plus untracked ones no ignore
rule covers) in a throwaway index and writes a commit object on top of HEAD:
the checkout's index, branches and refs are untouched. The base is
remote.projects.<path>.base, or the remote's default branch. The pushed ref is
deleted when the job ends. verify and run of one checkout exclude each other.`,
		Args:         cobra.ArbitraryArgs,
		RunE:         runRemoteVerify,
		SilenceUsage: true,
	}
	cmd.Flags().Bool("dirty", false, "verify the working tree as it is, not HEAD (index and refs untouched)")
	cmd.Flags().Bool("json", false, "print the defer record as JSON")
	return cmd
}

func runRemoteVerify(cmd *cobra.Command, _ []string) error {
	p := printerFrom(cmd)
	args := cmd.Flags().Args()
	dash := cmd.ArgsLenAtDash()
	var path string
	var command []string
	switch {
	case dash < 0 && len(args) > 1:
		return fmt.Errorf("pass the command after --: dot remote verify [PATH] -- COMMAND")
	case dash < 0 && len(args) == 1:
		path = args[0]
	case dash > 1:
		return fmt.Errorf("pass at most one PATH before --: %s", strings.Join(args[:dash], " "))
	case dash >= 0:
		if dash == 1 {
			path = args[0]
		}
		command = args[dash:]
	}
	cfg, deps, err := remoteBase(cmd)
	if err != nil {
		return err
	}
	checkout, err := remote.ResolveCheckout(cmd.Context(), deps, path)
	if err != nil {
		return err
	}
	opts, err := remoteJobFlags(cmd, deps)
	if err != nil {
		return err
	}
	dirty, _ := cmd.Flags().GetBool("dirty")
	opts.Path, opts.Command, opts.Dirty, opts.Home = checkout, command, dirty, homeFor(cmd)
	return remoteDeferred(cmd, p, deps.Verify(cmd.Context(), cfg, opts))
}

func newRemoteSrcCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "src [NAME] -- COMMAND [ARG...]",
		Short: "Clone or analyze third-party source on the host, never on this machine",
		Long: `Run COMMAND in a container in the host's third-party source area: src_root
(default ~/dot-remote-src) without NAME, which is where a clone is made, or
src_root/NAME with it. Untrusted code is cloned and analyzed only on the host:
nothing local is sent, nothing comes back but output, and no cache volume is
mounted, so it cannot reach the caches trusted builds reuse.

  dot remote src -- git clone https://example.com/x.git x
  dot remote src x -- rg -n unsafe

A NAME the host has no clone of exits 66 (EX_NOINPUT).`,
		Args:         cobra.ArbitraryArgs,
		RunE:         runRemoteSrc,
		SilenceUsage: true,
	}
	cmd.Flags().Bool("json", false, "print the defer record as JSON")
	return cmd
}

func runRemoteSrc(cmd *cobra.Command, _ []string) error {
	p := printerFrom(cmd)
	name, command, err := splitRemoteArgs(cmd)
	if err != nil {
		return fmt.Errorf("%s", strings.Replace(err.Error(), "dot remote run [PATH]", "dot remote src [NAME]", 1))
	}
	cfg, deps, err := remoteBase(cmd)
	if err != nil {
		return err
	}
	job, err := remoteJobFlags(cmd, deps)
	if err != nil {
		return err
	}
	cwd, _ := os.Getwd()
	err = deps.Src(cmd.Context(), cfg, remote.SrcOptions{
		Name: name, HostName: job.HostName, Command: command, Wait: job.Wait,
		Ignore: job.Ignore, IgnoreSrc: job.IgnoreSrc, DryRun: job.DryRun, Cwd: cwd,
	})
	return remoteDeferred(cmd, p, err)
}

// remoteJobFlags reads the flags every remote job shares.
func remoteJobFlags(cmd *cobra.Command, deps remote.Deps) (remote.VerifyOptions, error) {
	hostName, _ := cmd.Flags().GetString("host")
	ignore, source, err := remoteIgnore(deps.Store)
	if err != nil {
		return remote.VerifyOptions{}, err
	}
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	wait, _ := cmd.Flags().GetDuration("wait")
	if wait < 0 {
		return remote.VerifyOptions{}, fmt.Errorf("--wait must not be negative")
	}
	return remote.VerifyOptions{HostName: hostName, Wait: wait, Ignore: ignore, IgnoreSrc: source, DryRun: dryRun}, nil
}

// remoteDeferred turns a deferred job into exit 75 and passes anything else on.
func remoteDeferred(cmd *cobra.Command, p *Printer, err error) error {
	var deferred *remote.Deferred
	if errors.As(err, &deferred) {
		asJSON, _ := cmd.Flags().GetBool("json")
		return remoteDeferExit(p, deferred, asJSON)
	}
	return err
}

// splitRemoteArgs separates PATH from CMD at the first `--`. Without one the
// whole argument list is CMD and the path is the current directory, because a
// bare first argument is far more often a command than a checkout.
func splitRemoteArgs(cmd *cobra.Command) (string, []string, error) {
	args := cmd.Flags().Args()
	dash := cmd.ArgsLenAtDash()
	if dash < 0 {
		dash = 0
	}
	if dash > 1 {
		return "", nil, fmt.Errorf("pass at most one PATH before --: %s", strings.Join(args[:dash], " "))
	}
	command := args[dash:]
	if len(command) == 0 {
		return "", nil, fmt.Errorf("needs a command: dot remote run [PATH] -- COMMAND [ARG...]")
	}
	if dash == 1 {
		return args[0], command, nil
	}
	return "", command, nil
}

// remoteDeferExit prints why a run did not start and returns exit 75.
func remoteDeferExit(p *Printer, deferred *remote.Deferred, asJSON bool) error {
	outcome := deferred.Outcome
	if asJSON {
		data, err := json.MarshalIndent(outcome, "", "  ")
		if err != nil {
			return err
		}
		p.Line("%s", data)
	} else {
		p.Warn("remote run deferred (scope %s)", outcome.Scope)
		p.KV("Reason", outcome.Reason)
		p.KV("Retry after", fmt.Sprintf("%ds", outcome.RetryAfterSeconds))
	}
	return &ExitCodeError{Code: ExitDeferred, Err: fmt.Errorf("remote run deferred: %s", outcome.Reason)}
}

func newRemoteStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the host's load, this machine's runs there, mirror sizes and image age",
		Long: `Report the host's own load against its limit, the runs this machine holds
there (mirror path, container, owner, age), the size of every mirror and bare
repository, and how old the dev image is. A host that cannot answer still shows
the local half: the leases this machine holds are facts the host does not have
to confirm.`,
		Args:         cobra.NoArgs,
		RunE:         runRemoteStatus,
		SilenceUsage: true,
	}
	cmd.Flags().Bool("json", false, "print the status as JSON")
	return cmd
}

func runRemoteStatus(cmd *cobra.Command, _ []string) error {
	p := printerFrom(cmd)
	cfg, host, deps, err := remoteSession(cmd, "")
	if err != nil {
		return err
	}
	status, err := deps.Status(cmd.Context(), cfg, host)
	if err != nil {
		return err
	}
	if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
		data, err := json.MarshalIndent(status, "", "  ")
		if err != nil {
			return err
		}
		p.Line("%s", data)
		return nil
	}
	p.Header("dot remote status")
	p.KV("Host", fmt.Sprintf("%s (%s, %s)", status.Host, status.SSH, status.Runtime))
	p.KV("Image", statusImageLine(status))
	load := status.Load.String()
	if status.MaxLoadRatio > 0 {
		load += fmt.Sprintf(", limit %g x CPUs", status.MaxLoadRatio)
	} else {
		load += ", no load limit"
	}
	p.KV("Load", load)
	if status.Verdict != "" {
		p.KV("Gate", "would defer: "+status.Verdict)
	} else {
		p.KV("Gate", "admitting")
	}
	p.KV("Runs", fmt.Sprintf("%d held, at most %d", countRuns(status.Active, "host"), status.MaxRuns))
	for _, run := range status.Active {
		line := fmt.Sprintf("  [%s] %s %s pid %d since %s", run.Kind, run.Scope, run.Owner, run.PID, run.Since.Format("2006-01-02 15:04"))
		if run.Container != "" {
			line += " container " + run.Container
		}
		p.Line("%s", line)
	}
	if status.SizesUnavailable != "" {
		p.KV("Mirrors", "unavailable: "+status.SizesUnavailable)
	} else {
		p.KV("Mirrors", fmt.Sprintf("%d (%s)", len(status.Mirrors), totalSize(status.Mirrors)))
		for _, dir := range status.Mirrors {
			p.Line("  %s %s", sizeLabel(dir.Bytes), dir.Name)
		}
		p.KV("Bare repos", fmt.Sprintf("%d (%s)", len(status.BareRepos), totalSize(status.BareRepos)))
	}
	p.Blank()
	p.Line("Note: %s", admission.UncoveredNote)
	return nil
}

func statusImageLine(status remote.Status) string {
	line := status.Image
	switch {
	case status.ImageAge != "":
		line += " (built " + status.ImageAge + " ago)"
	case status.ImageCreated != "":
		line += " (built " + status.ImageCreated + ")"
	default:
		line += " (the host did not report it; run `dot remote setup`)"
	}
	return line
}

func countRuns(runs []remote.ActiveRun, kind string) int {
	count := 0
	for _, run := range runs {
		if run.Kind == kind {
			count++
		}
	}
	return count
}

func totalSize(dirs []remote.DirSize) string {
	var total int64
	for _, dir := range dirs {
		total += dir.Bytes
	}
	return sizeLabel(total)
}

// sizeLabel renders a size in the units du reports, so a mirror listing is
// exact rather than 2.4% off from being passed through decimal units.
func sizeLabel(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	value := float64(bytes)
	for _, suffix := range []string{"KiB", "MiB", "GiB", "TiB"} {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%.1f PiB", value)
}
