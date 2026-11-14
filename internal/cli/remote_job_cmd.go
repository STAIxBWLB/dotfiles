package cli

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/remote"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

// newRemoteJobCmd builds `dot remote job`: file-in, file-out jobs on a host's
// job profile, with a local fallback (#422).
func newRemoteJobCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "job",
		Short: "Run file-in, file-out jobs on a host's job profile, or locally when it is busy",
		Long: `A job sends named input files to the host, runs a command in an isolated job
directory there, and brings the files it wrote back. When the host cannot take
the job, the same command runs on this machine under 'dot admit'.

Profiles live under remote.hosts.<host>.jobs (see docs/guides/servers.md), each
with its own image: build or pull it with 'dot remote setup --profile P'.`,
		SilenceUsage: true,
	}
	cmd.PersistentFlags().String("profile", "", "job profile (default: the host's only one)")
	cmd.AddCommand(newRemoteJobRunCmd(), newRemoteJobCheckCmd())
	return cmd
}

func newRemoteJobRunCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run -o DIR [-i FILE]... [--name N] [--remote-only | --local-only] -- COMMAND [ARG...]",
		Short: "Stage files, run a command in a job directory, collect out/",
		Long: `Stage each -i FILE in the job's in/, run COMMAND with the job directory as its
working directory, and collect out/ into -o DIR. Write COMMAND with relative
paths (in/..., out/...) so it runs unchanged on the host or locally. One
COMMAND argument is a shell script; several are an argv.

The host is checked first (image present, load under max_load_ratio, free
space above min_free_gb). When it cannot take the job and the profile's
fallback is local, the job runs here in <local.root>/<name> under 'dot admit'
with in/ holding symlinks to the inputs. A job that fails, or whose out/
cannot be collected, keeps its directory and prints where.

Exit status: the command's own; 75 when the profile's max_jobs are already
active from this machine; 69 when the host cannot take the job and
--remote-only (or fallback: none) keeps it off this machine; 128+signal when
interrupted.`,
		Args:         cobra.ArbitraryArgs,
		RunE:         runRemoteJobRun,
		SilenceUsage: true,
	}
	cmd.Flags().StringP("out", "o", "", "directory out/ is collected into (created if missing)")
	cmd.Flags().StringArrayP("input", "i", nil, "input file staged in in/ (repeatable; base names must be unique)")
	cmd.Flags().String("name", "", "job name, [a-z0-9][a-z0-9-]{0,63} (default: a timestamp)")
	cmd.Flags().Bool("remote-only", false, "exit 69 instead of running locally when the host cannot take the job")
	cmd.Flags().Bool("local-only", false, "skip the host and run on this machine under dot admit")
	cmd.Flags().Bool("json", false, "print the defer or unavailable record as JSON")
	return cmd
}

func runRemoteJobRun(cmd *cobra.Command, _ []string) error {
	p := printerFrom(cmd)
	args := cmd.Flags().Args()
	if dash := cmd.ArgsLenAtDash(); dash > 0 {
		return fmt.Errorf("unexpected arguments before --: %v", args[:dash])
	}
	cfg, deps, err := remoteBase(cmd)
	if err != nil {
		return err
	}
	opts := remote.JobOptions{Command: args}
	opts.HostName, _ = cmd.Flags().GetString("host")
	opts.Profile, _ = cmd.Flags().GetString("profile")
	opts.Name, _ = cmd.Flags().GetString("name")
	opts.Inputs, _ = cmd.Flags().GetStringArray("input")
	opts.OutDir, _ = cmd.Flags().GetString("out")
	opts.RemoteOnly, _ = cmd.Flags().GetBool("remote-only")
	opts.LocalOnly, _ = cmd.Flags().GetBool("local-only")
	opts.DryRun, _ = cmd.Flags().GetBool("dry-run")
	opts.Wait, _ = cmd.Flags().GetDuration("wait")
	if opts.Wait < 0 {
		return usageError(cmd, fmt.Errorf("--wait must not be negative"))
	}
	err = deps.RunJob(cmd.Context(), cfg, opts)
	var deferred *remote.Deferred
	if errors.As(err, &deferred) {
		asJSON, _ := cmd.Flags().GetBool("json")
		return remoteDeferExit(p, deferred, asJSON)
	}
	var unavailable *remote.Unavailable
	if errors.As(err, &unavailable) {
		if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
			data, merr := json.MarshalIndent(unavailable.Check, "", "  ")
			if merr != nil {
				return merr
			}
			p.Line("%s", data)
		}
		return &ExitCodeError{Code: remote.ExitUnavailable, Err: err}
	}
	return err
}

func newRemoteJobCheckCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Exit 0 when the host can take a job now, 69 when not",
		Long: `One ssh round trip (30 seconds at most, 5 to connect): the profile's image is
present, load1 is under max_load_ratio x CPUs, and more than min_free_gb is
free under the job root. A timeout or an unreadable value counts as
unavailable. Exit 0 when the host can take a job, 69 with the reason when not.`,
		Args:         cobra.NoArgs,
		RunE:         runRemoteJobCheck,
		SilenceUsage: true,
	}
	cmd.Flags().Bool("json", false, "print the result as JSON")
	return cmd
}

func runRemoteJobCheck(cmd *cobra.Command, _ []string) error {
	p := printerFrom(cmd)
	_, host, deps, err := remoteSession(cmd, "")
	if err != nil {
		return err
	}
	name, _ := cmd.Flags().GetString("profile")
	profile, err := host.Profile(name)
	if err != nil {
		return err
	}
	if dry, _ := cmd.Flags().GetBool("dry-run"); dry {
		p.Line("would check %s over ssh:\n%s", host.SSH, remote.JobCheckScript(host, profile))
		return nil
	}
	result := deps.CheckJob(cmd.Context(), host, profile)
	if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
		data, merr := json.MarshalIndent(result, "", "  ")
		if merr != nil {
			return merr
		}
		p.Line("%s", data)
	} else {
		if result.Available {
			p.Bullet(ui.MarkPresent, fmt.Sprintf("%s can take a %s job now", host.Name, profile.Name))
			p.Summary(ui.CheckSummary{OK: 1})
		} else {
			p.Bullet(ui.MarkFail, fmt.Sprintf("%s cannot take a %s job: %s", host.Name, profile.Name, result.Reason))
			p.Summary(ui.CheckSummary{Failed: 1})
		}
	}
	if !result.Available {
		return &ExitCodeError{Code: remote.ExitUnavailable, Err: fmt.Errorf("%s cannot take a %s job: %s", host.Name, profile.Name, result.Reason)}
	}
	return nil
}
