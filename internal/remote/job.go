package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/admission"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// Exit statuses a job adds to the command's own.
const (
	// ExitUnavailable (EX_UNAVAILABLE) is a job the host cannot take, with no
	// fallback allowed.
	ExitUnavailable = 69
	// jobCheckTimeout bounds the one ssh round trip `check` makes.
	jobCheckTimeout = 30 * time.Second
	// jobConnectTimeout is ssh's own connect bound for every job round trip.
	jobConnectTimeout = "5"
)

// JobOptions is one `dot remote job run` request.
type JobOptions struct {
	HostName string
	Profile  string
	// Name is the job directory and container suffix; empty means a timestamp.
	Name       string
	Inputs     []string
	OutDir     string
	Command    []string
	RemoteOnly bool
	LocalOnly  bool
	Wait       time.Duration
	DryRun     bool
}

// JobScope is the lease scope one profile's jobs on one host share.
func JobScope(h Host, profile string) string { return "remote-job:" + h.Name + "/" + profile }

// Unavailable is a job the host could not take while nothing allowed it to run
// here: exit 69, with what the check found.
type Unavailable struct {
	Check JobCheck
	Err   error
}

func (u *Unavailable) Error() string { return u.Err.Error() }

// ExitCode is EX_UNAVAILABLE.
func (u *Unavailable) ExitCode() int { return ExitUnavailable }

// JobContainer is the container one remote job runs in.
func JobContainer(h Host, p JobProfile, name string) string {
	return h.Prefix + "-" + p.Name + "-" + name
}

// JobName returns the job's name: the one given, checked, or a timestamp.
func JobName(given string, now time.Time) (string, error) {
	if given == "" {
		return now.Format("060102-150405"), nil
	}
	if !jobNamePattern.MatchString(given) {
		return "", fmt.Errorf("--name %q must match %s", given, jobNamePattern)
	}
	return given, nil
}

// CheckInputs resolves each input to an absolute regular file and refuses
// missing files and duplicate base names, which would collide in in/. Nothing
// is sent before this passes.
func CheckInputs(inputs []string) ([]string, error) {
	out := make([]string, 0, len(inputs))
	seen := map[string]string{}
	var missing []string
	for _, input := range inputs {
		abs, err := filepath.Abs(input)
		if err != nil {
			return nil, fmt.Errorf("resolving %s: %w", input, err)
		}
		info, err := os.Stat(abs)
		if err != nil {
			missing = append(missing, input)
			continue
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("input %s is not a regular file: inputs are a flat list of files", input)
		}
		base := filepath.Base(abs)
		if other, dup := seen[base]; dup {
			return nil, fmt.Errorf("inputs %s and %s share the name %s, and both would land in in/: rename one", other, input, base)
		}
		seen[base] = input
		out = append(out, abs)
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing input: %s", strings.Join(missing, ", "))
	}
	return out, nil
}

// jobSSH is one job round trip: batch mode, with a bounded connect.
func jobSSH(h Host, command string) []string {
	return []string{"ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=" + jobConnectTimeout, h.SSH, command}
}

// jobRSH is the ssh rsync uses for a job's transfers.
const jobRSH = "ssh -o BatchMode=yes -o ConnectTimeout=" + jobConnectTimeout

// rootExpr is a profile root as a shell word on the host: an absolute root as
// it is, a relative one under the remote $HOME.
func rootExpr(root string) string {
	if path.IsAbs(root) {
		return ShellQuote(root)
	}
	return `"$HOME"/` + ShellQuote(root)
}

// jobDirExpr is one job's directory as a shell word on the host.
func jobDirExpr(p JobProfile, name string) string {
	return rootExpr(path.Join(p.Root, name))
}

// jobDirLabel is one job's directory the way a message names it.
func jobDirLabel(h Host, p JobProfile, name string) string {
	dir := path.Join(p.Root, name)
	if !path.IsAbs(dir) {
		dir = "~/" + dir
	}
	return h.SSH + ":" + dir
}

// JobCheckScript asks the host whether it can take a job now: the profile's
// image present and not root, load1 under max_load_ratio x CPUs, and more than
// min_free_gb free under the root. It prints "ok" or one "unavailable\t<why>"
// line; an unreadable value is a reason, never health.
func JobCheckScript(h Host, p JobProfile) string {
	ih := p.ImageHost(h)
	image := ShellQuote(ih.Image())
	root := rootExpr(p.Root)
	var b strings.Builder
	fmt.Fprintf(&b, "if ! user=$(%s image inspect -f '{{.Config.User}}' %s 2>/dev/null); then printf 'unavailable\\timage %%s is missing: run dot remote setup --profile %s\\n' %s; exit 0; fi\n",
		ih.Runtime, image, p.Name, image)
	b.WriteString(`case "${user%:*}" in ''|root|0) printf 'unavailable\timage runs as root (no USER)\n'; exit 0;; esac` + "\n")
	if p.MaxLoadRatio > 0 {
		fmt.Fprintf(&b, "if ! awk -v n=\"$(nproc)\" -v r=%s 'NR==1 { if (n > 0 && $1 < n*r) ok=1 } END { exit !ok }' /proc/loadavg 2>/dev/null; then printf 'unavailable\\tload1 %%s is at or over %s x %%s CPUs, or unreadable\\n' \"$(cut -d' ' -f1 /proc/loadavg 2>/dev/null)\" \"$(nproc 2>/dev/null)\"; exit 0; fi\n",
			strconv.FormatFloat(p.MaxLoadRatio, 'g', -1, 64), strconv.FormatFloat(p.MaxLoadRatio, 'g', -1, 64))
	}
	fmt.Fprintf(&b, "if ! mkdir -p %s 2>/dev/null; then printf 'unavailable\\tcannot create the job root\\n'; exit 0; fi\n", root)
	if p.MinFreeGB > 0 {
		fmt.Fprintf(&b, "free=$(df -Pk %s 2>/dev/null | awk 'NR==2 { print int($4 / 1048576) }')\n", root)
		fmt.Fprintf(&b, "case \"$free\" in ''|*[!0-9]*) printf 'unavailable\\tfree space under the job root is unreadable\\n'; exit 0;; esac\n")
		fmt.Fprintf(&b, "if [ \"$free\" -le %d ]; then printf 'unavailable\\t%%s GB free under the job root, need more than %d\\n' \"$free\"; exit 0; fi\n", p.MinFreeGB, p.MinFreeGB)
	}
	b.WriteString("printf 'ok\\n'")
	return b.String()
}

// JobCheck is what `check` found.
type JobCheck struct {
	Host      string `json:"host"`
	Profile   string `json:"profile"`
	Available bool   `json:"available"`
	Reason    string `json:"reason"`
}

// ParseJobCheck reads the check script's answer. Anything but a clean "ok" is
// unavailable, with the reason the host gave.
func ParseJobCheck(out string) (bool, string) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "ok" {
			return true, "ok"
		}
		if why, ok := strings.CutPrefix(line, "unavailable\t"); ok {
			return false, why
		}
	}
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return false, "the host gave no answer"
	}
	return false, "unreadable answer: " + firstLine(trimmed)
}

// CheckJob asks the host whether it can take one of p's jobs now, in one ssh
// round trip bounded at 30 seconds.
func (d Deps) CheckJob(ctx context.Context, h Host, p JobProfile) JobCheck {
	result := JobCheck{Host: h.Name, Profile: p.Name}
	ctx, cancel := context.WithTimeout(ctx, jobCheckTimeout)
	defer cancel()
	out, err := d.capture(ctx, jobSSH(h, JobCheckScript(h, p)), nil)
	if err != nil {
		if ctx.Err() != nil {
			result.Reason = fmt.Sprintf("ssh timed out after %s", jobCheckTimeout)
		} else {
			result.Reason = "ssh failed: " + err.Error()
		}
		return result
	}
	result.Available, result.Reason = ParseJobCheck(out)
	return result
}

// JobContainerArgv is the runtime argv of one remote job, starting at "run".
// The job directory mount is spliced in by JobRunCommand, since it expands on
// the host. No cache volume is mounted.
func JobContainerArgv(h Host, p JobProfile, name string, command []string) []string {
	ih := p.ImageHost(h)
	argv := []string{"run", "--rm", "--init", "-i", "--name", JobContainer(h, p, name)}
	if p.CPUs > 0 {
		argv = append(argv, "--cpus", strconv.Itoa(p.CPUs))
	}
	argv = append(argv, "--memory", p.Memory, "-w", "/job")
	for _, pair := range p.JobEnv(p.CPUs) {
		argv = append(argv, "-e", pair.Key+"="+pair.Value)
	}
	return append(argv, ih.Image(), "bash", "-c", CommandScript(command))
}

// JobRunCommand is the remote shell command one job sends.
func JobRunCommand(h Host, p JobProfile, name string, command []string) string {
	argv := JobContainerArgv(h, p, name, command)
	return h.Runtime + " run --rm -v " + jobDirExpr(p, name) + ":/job " + ShellJoin(argv[2:])
}

// RunJob stages the inputs, runs CMD on the host (or, when the host cannot
// take it and the profile allows, on this machine under `dot admit`), and
// collects out/ into the output directory.
func (d Deps) RunJob(ctx context.Context, cfg Config, opts JobOptions) error {
	if opts.RemoteOnly && opts.LocalOnly {
		return errors.New("--remote-only and --local-only exclude each other")
	}
	if len(opts.Command) == 0 {
		return errors.New("needs a command: dot remote job run -o DIR [-i FILE]... -- COMMAND")
	}
	if strings.TrimSpace(opts.OutDir) == "" {
		return errors.New("needs -o DIR: where out/ is collected")
	}
	name, err := JobName(opts.Name, d.now())
	if err != nil {
		return err
	}
	inputs, err := CheckInputs(opts.Inputs)
	if err != nil {
		return err
	}
	outDir, err := filepath.Abs(opts.OutDir)
	if err != nil {
		return err
	}
	h, err := cfg.Host(opts.HostName)
	if err != nil {
		return err
	}
	p, err := h.Profile(opts.Profile)
	if err != nil {
		return err
	}
	if opts.DryRun {
		d.describeJob(h, p, name, inputs, outDir, opts)
		return nil
	}
	leases, err := d.acquireJob(ctx, h, p, opts.Wait)
	if err != nil {
		return err
	}
	defer leases.Release()

	if !opts.LocalOnly {
		check := d.CheckJob(ctx, h, p)
		if check.Available {
			fmt.Fprintf(d.errOut(), "dot remote: job %s (%s) on %s\n", name, p.Name, h.Name)
			return d.runRemoteJob(ctx, h, p, name, inputs, outDir, opts.Command)
		}
		why := check.Reason
		if opts.RemoteOnly || p.Fallback == FallbackNone {
			hint := "--remote-only"
			if !opts.RemoteOnly {
				hint = "fallback: none"
			}
			return &Unavailable{Check: check, Err: fmt.Errorf("%s cannot take a %s job (%s), and %s keeps it off this machine", h.Name, p.Name, why, hint)}
		}
		fmt.Fprintf(d.errOut(), "dot remote: %s cannot take the job (%s); running it locally\n", h.Name, why)
	} else {
		fmt.Fprintf(d.errOut(), "dot remote: running job %s locally (--local-only)\n", name)
	}
	return d.runLocalJob(ctx, p, name, inputs, outDir, opts)
}

// acquireJob takes one of the profile's max_jobs leases; a full profile defers
// with exit 75.
func (d Deps) acquireJob(ctx context.Context, h Host, p JobProfile, wait time.Duration) (*Leases, error) {
	if d.Store == nil {
		if d.StoreErr != nil {
			return nil, fmt.Errorf("the admission store is unavailable (%w), so a job cannot hold its lease", d.StoreErr)
		}
		return nil, errors.New("the admission store is unavailable, so a job cannot hold its lease: run `dot admit status` to check it")
	}
	runner := d.Runner
	if runner == nil {
		runner = exec.NewProbeRunner()
	}
	cwd, _ := os.Getwd()
	self, err := admission.SelfLease(ctx, runner, cwd)
	if err != nil {
		return nil, err
	}
	self.Host = h.Name
	if self.Session == "" {
		self.Session = "dot remote job"
	}
	scope := JobScope(h, p.Name)
	slot, err := acquire(ctx, d.Store, scope, self, p.MaxJobs, wait, false, func(holder *admission.Lease) string {
		who := "another job"
		if holder != nil {
			who = fmt.Sprintf("%s (pid %d)", holder.Owner, holder.PID)
		}
		return fmt.Sprintf("%d %s job(s) already active on %s from this machine, %s among them (remote.hosts.%s.jobs.%s.max_jobs)", p.MaxJobs, p.Name, h.Name, who, h.Name, p.Name)
	})
	if err != nil {
		return nil, err
	}
	leases := &Leases{Path: slot}
	leases.stop = heartbeat(slot)
	return leases, nil
}

func (d Deps) describeJob(h Host, p JobProfile, name string, inputs []string, outDir string, opts JobOptions) {
	fmt.Fprintf(d.out(), "job %s, profile %s on %s\n", name, p.Name, h.Name)
	for _, input := range inputs {
		fmt.Fprintf(d.out(), "would stage %s as in/%s\n", input, filepath.Base(input))
	}
	if !opts.LocalOnly {
		fmt.Fprintf(d.out(), "would check %s, then run in %s:\n  %s\n", h.Name, jobDirLabel(h, p, name),
			strings.Join(jobSSH(h, JobRunCommand(h, p, name, opts.Command)), " "))
	}
	if !opts.RemoteOnly && p.Fallback == FallbackLocal {
		fmt.Fprintf(d.out(), "would run locally in %s under dot admit when the host cannot take it\n", filepath.Join(p.LocalRoot, name))
	}
	fmt.Fprintf(d.out(), "would collect out/ into %s\n", outDir)
}

// runRemoteJob is the host side: stage in/, run, collect out/, then delete
// the job directory when both the command and the collection succeeded.
func (d Deps) runRemoteJob(ctx context.Context, h Host, p JobProfile, name string, inputs []string, outDir string, command []string) error {
	dir := jobDirExpr(p, name)
	label := jobDirLabel(h, p, name)
	if _, err := d.capture(ctx, jobSSH(h, "mkdir -p "+dir+"/in "+dir+"/out"), nil); err != nil {
		return fmt.Errorf("creating %s: %w", label, err)
	}
	rsyncPath, err := d.localRsync(ctx)
	if err != nil {
		return err
	}
	remoteDir := path.Join(p.Root, name)
	if len(inputs) > 0 {
		argv := append([]string{rsyncPath, "-a", "-s", "-e", jobRSH}, inputs...)
		argv = append(argv, h.SSH+":"+remoteDir+"/in/")
		if _, err := d.capture(ctx, argv, nil); err != nil {
			fmt.Fprintf(d.errOut(), "dot remote: job kept at %s\n", label)
			return fmt.Errorf("staging the inputs: %w", err)
		}
	}
	container := JobContainer(h, p, name)
	exit, sig, runErr := d.ex()(ctx, jobSSH(h, JobRunCommand(h, p, name, command)), d.in(), d.out(), d.errOut())
	if runErr != nil {
		fmt.Fprintf(d.errOut(), "dot remote: job kept at %s\n", label)
		return runErr
	}
	if sig != 0 || exit == sshFailureStatus {
		removed := "removed " + container
		if err := d.removeContainerStrict(ctx, h, container); err != nil {
			removed = err.Error()
		}
		fmt.Fprintf(d.errOut(), "dot remote: job kept at %s\n", label)
		if sig != 0 {
			return &ExitCode{Code: 128 + int(sig), Err: fmt.Errorf("interrupted by signal %d; %s", int(sig), removed)}
		}
		return &ExitCode{Code: exit, Err: fmt.Errorf("ssh or the command exited %d; %s", exit, removed)}
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintf(d.errOut(), "dot remote: job kept at %s\n", label)
		return err
	}
	// From the host's $HOME, which is where rsync's remote side starts, so a
	// relative root needs no expansion.
	_, pullErr := d.capture(context.WithoutCancel(ctx), []string{rsyncPath, "-a", "-s", "-e", jobRSH, h.SSH + ":" + remoteDir + "/out/", outDir + "/"}, nil)
	if exit == 0 && pullErr == nil {
		if _, err := d.capture(context.WithoutCancel(ctx), jobSSH(h, "rm -rf "+dir), nil); err != nil {
			fmt.Fprintf(d.errOut(), "dot remote: could not delete %s (%v)\n", label, err)
		}
		return nil
	}
	fmt.Fprintf(d.errOut(), "dot remote: job kept at %s\n", label)
	if exit != 0 {
		return &ExitCode{Code: exit, Err: fmt.Errorf("the job exited with status %d", exit)}
	}
	return &ExitCode{Code: 1, Err: fmt.Errorf("collecting out/ into %s: %w", outDir, pullErr)}
}

// runLocalJob is the fallback: the same CMD in <local.root>/<name>, in/ as
// symlinks, through `dot admit` as an ordinary heavy job.
func (d Deps) runLocalJob(ctx context.Context, p JobProfile, name string, inputs []string, outDir string, opts JobOptions) error {
	work := filepath.Join(p.LocalRoot, name)
	if err := os.MkdirAll(filepath.Join(work, "in"), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(work, "out"), 0o755); err != nil {
		return err
	}
	for _, input := range inputs {
		link := filepath.Join(work, "in", filepath.Base(input))
		if _, err := os.Lstat(link); err == nil {
			continue
		}
		if err := os.Symlink(input, link); err != nil {
			return err
		}
	}
	argv, err := d.localAdmitArgv(opts.Wait)
	if err != nil {
		return err
	}
	argv = append(argv, "bash", "-c", CommandScript(opts.Command))
	exit, sig, runErr := d.exLocal()(ctx, argv, work, localJobEnv(p), d.in(), d.out(), d.errOut())
	if runErr != nil {
		fmt.Fprintf(d.errOut(), "dot remote: local job kept at %s\n", work)
		return runErr
	}
	if err := copyTree(filepath.Join(work, "out"), outDir); err != nil {
		fmt.Fprintf(d.errOut(), "dot remote: local job kept at %s\n", work)
		return fmt.Errorf("copying out/ into %s: %w", outDir, err)
	}
	if exit == 0 && sig == 0 {
		if err := os.RemoveAll(work); err != nil {
			fmt.Fprintf(d.errOut(), "dot remote: could not delete %s (%v)\n", work, err)
		}
		return nil
	}
	fmt.Fprintf(d.errOut(), "dot remote: local job kept at %s\n", work)
	if sig != 0 && exit == 0 {
		exit = 128 + int(sig)
	}
	return &ExitCode{Code: exit, Err: fmt.Errorf("the local job exited with status %d", exit)}
}

// localAdmitArgv is `dot admit` for the fallback, from this very binary.
func (d Deps) localAdmitArgv(wait time.Duration) ([]string, error) {
	bin := d.DotBinary
	if bin == "" {
		exe, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("finding dot for the local fallback: %w", err)
		}
		bin = exe
	}
	return []string{bin, "admit", "--wait", wait.String(), "--"}, nil
}

// localJobEnv is this process's env with local.path ahead of PATH and
// local.env applied.
func localJobEnv(p JobProfile) []string {
	env := os.Environ()
	set := map[string]string{}
	for _, pair := range p.LocalEnv {
		set[pair.Key] = pair.Value
	}
	if len(p.LocalPath) > 0 {
		set["PATH"] = strings.Join(p.LocalPath, string(os.PathListSeparator)) + string(os.PathListSeparator) + os.Getenv("PATH")
	}
	out := make([]string, 0, len(env)+len(set))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if _, override := set[k]; override {
			continue
		}
		out = append(out, kv)
	}
	for _, k := range sortedKeys(set) {
		out = append(out, k+"="+set[k])
	}
	return out
}

// copyTree copies src into dst: files with the same names are overwritten,
// others in dst are left alone. Symlinks are copied as links.
func copyTree(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	return filepath.WalkDir(src, func(p string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case entry.IsDir():
			return os.MkdirAll(target, 0o755)
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			_ = os.Remove(target)
			return os.Symlink(link, target)
		default:
			in, err := os.Open(p)
			if err != nil {
				return err
			}
			defer in.Close()
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, in); err != nil {
				out.Close()
				return err
			}
			return out.Close()
		}
	})
}
