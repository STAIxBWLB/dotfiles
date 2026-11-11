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
same path removes the one left behind.`,
		SilenceUsage: true,
	}
	cmd.PersistentFlags().String("host", "", "remote host to use (default: the matching project's host, then remote.default)")
	cmd.PersistentFlags().Duration("wait", 0, "retry a held lease for this long before deferring (default 0: defer at once)")
	cmd.AddCommand(newRemoteSetupCmd())
	cmd.AddCommand(newRemoteRunCmd())
	cmd.AddCommand(newRemoteStatusCmd())
	return cmd
}

// remoteSession loads the config, resolves the host and wires the seams every
// 'dot remote' subcommand needs. checkout is the resolved path a job targets,
// which decides the host when --host is not given; it is empty for a command
// that addresses the host itself.
func remoteSession(cmd *cobra.Command, checkout string) (remote.Config, remote.Host, remote.Deps, error) {
	home := homeFor(cmd)
	var state *config.UserState
	var err error
	if override := homeOverrideFrom(cmd); override != "" {
		state, err = config.LoadStateForHome(override)
	} else {
		state, err = config.LoadState()
	}
	if err != nil {
		return remote.Config{}, remote.Host{}, remote.Deps{}, fmt.Errorf("loading config: %w", err)
	}
	cfg, err := remote.Load(state.Remote, home)
	if err != nil {
		return remote.Config{}, remote.Host{}, remote.Deps{}, err
	}
	name, _ := cmd.Flags().GetString("host")
	host, err := cfg.HostFor(name, checkout)
	if err != nil {
		return remote.Config{}, remote.Host{}, remote.Deps{}, err
	}
	deps := remote.DefaultDeps(watchdogRunner(false))
	deps.In, deps.Out, deps.Err = cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr()
	return cfg, host, deps, nil
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
	return cmd
}

func runRemoteSetup(cmd *cobra.Command, _ []string) error {
	_, host, deps, err := remoteSession(cmd, "")
	if err != nil {
		return err
	}
	p := printerFrom(cmd)
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	asJSON, _ := cmd.Flags().GetBool("json")
	report, err := deps.Setup(cmd.Context(), host, remote.SetupOptions{DryRun: dryRun})
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
remote.local_root, and may not be that root or $HOME.`,
		Args:         cobra.ArbitraryArgs,
		RunE:         runRemoteRun,
		SilenceUsage: true,
	}
	cmd.Flags().Bool("json", false, "print the defer record as JSON")
	return cmd
}

func runRemoteRun(cmd *cobra.Command, _ []string) error {
	p := printerFrom(cmd)
	path, command, err := splitRemoteArgs(cmd)
	if err != nil {
		return err
	}
	checkout := ""
	if path != "" {
		// Resolved here as well as in the run, because the config's project
		// match compares resolved paths: a project entry has to cover the same
		// checkout the run mirrors.
		checkout, err = remote.ResolvePath(path)
		if err != nil {
			return err
		}
	}
	cfg, _, deps, err := remoteSession(cmd, checkout)
	if err != nil {
		return err
	}
	ignore, source, err := remoteIgnore(deps.Store)
	if err != nil {
		return err
	}
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	wait, _ := cmd.Flags().GetDuration("wait")
	hostName, _ := cmd.Flags().GetString("host")
	if wait < 0 {
		return fmt.Errorf("--wait must not be negative")
	}
	err = deps.Run(cmd.Context(), cfg, remote.RunOptions{
		Path:      checkout,
		HostName:  hostName,
		Command:   command,
		Wait:      wait,
		Ignore:    ignore,
		IgnoreSrc: source,
		DryRun:    dryRun,
		Home:      homeFor(cmd),
	})
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
	p.KV("Runs", fmt.Sprintf("%d held, at most %d", countRuns(status.Active, "path"), status.MaxRuns))
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
