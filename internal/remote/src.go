package remote

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/admission"
)

// ExitMissingClone is the status `dot remote src NAME` exits with when the host
// has no clone NAME (EX_NOINPUT).
const ExitMissingClone = 66

// srcName is what a clone NAME may be: one path segment, so it cannot leave
// src_root.
var srcName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// SrcOptions is one `dot remote src` request.
type SrcOptions struct {
	// Name is the clone under src_root; empty runs in src_root itself, which is
	// where a clone is made.
	Name      string
	HostName  string
	Command   []string
	Wait      time.Duration
	Ignore    admission.IgnoreSet
	IgnoreSrc string
	DryRun    bool
	// Cwd is the local directory the lease records; it never leaves this
	// machine.
	Cwd string
}

// SrcContainer is the container one src job runs in.
func (h Host) SrcContainer(name string) string {
	if name == "" {
		name = "root"
	}
	return h.Prefix + "-src-" + name
}

// missingCloneMarker is what the host prints when NAME has no clone, so the
// exit can be told apart from a command that happens to exit 66.
const missingCloneMarker = "dot-remote-src: no clone"

// SrcCommand is the remote shell command one src job sends. No cache volume is
// mounted: untrusted code must not reach the caches trusted builds reuse.
func SrcCommand(h Host, name string, command []string) string {
	return srcCommand(h, name, command, "")
}

func srcCommand(h Host, name string, command []string, owner string) string {
	base := `"$HOME"/` + ShellQuote(h.SrcRoot)
	workdir := "/src"
	guard := "mkdir -p " + base + " && "
	if name != "" {
		workdir = "/src/" + name
		// The runtime would create a missing -w directory as root on the bind
		// mount, so a typo fails here instead.
		guard = "test -d " + base + "/" + ShellQuote(name) + " || { echo " +
			ShellQuote(missingCloneMarker+" "+name+" on this host; clone it with: dot remote src -- git clone URL "+name) +
			" >&2; exit " + fmt.Sprint(ExitMissingClone) + "; }; "
	}
	argv := []string{"--init", "-i", "--name", h.SrcContainer(name)}
	if owner != "" {
		argv = append(argv, "--label", "net.staix.dot.remote-owner="+owner)
	}
	if h.CPUs > 0 {
		argv = append(argv, "--cpus", fmt.Sprint(h.CPUs))
	}
	argv = append(argv, "--memory", h.Memory, "-w", workdir, h.Image(), "bash", "-c", CommandScript(command))
	return guard + h.Runtime + " run --rm -v " + base + ":/src " + ShellJoin(argv)
}

// Src runs CMD in the host's third-party source area: src_root, or the clone
// src_root/NAME. Nothing local is sent and nothing comes back.
func (d Deps) Src(ctx context.Context, cfg Config, opts SrcOptions) error {
	if opts.Name != "" && !srcName.MatchString(opts.Name) {
		return fmt.Errorf("bad clone name %q: letters, digits, '.', '_' and '-' only, starting with a letter or digit", opts.Name)
	}
	if len(opts.Command) == 0 {
		return fmt.Errorf("needs a command: dot remote src [NAME] -- COMMAND [ARG...]")
	}
	h, err := cfg.Host(opts.HostName)
	if err != nil {
		return err
	}
	container := h.SrcContainer(opts.Name)
	remoteCommand := srcCommand(h, opts.Name, opts.Command, d.containerOwner(srcScope(h, opts.Name)))
	if opts.DryRun {
		fmt.Fprintf(d.out(), "would run in %s on %s:\n  %s\n", container, h.Name, strings.Join(SSHArgv(h.SSH, remoteCommand), " "))
		return nil
	}
	scope := srcScope(h, opts.Name)
	leases, err := d.admit(ctx, h, admitOptions{
		Scope: scope, Container: container, Cwd: opts.Cwd, Wait: opts.Wait, Ignore: opts.Ignore, IgnoreSrc: opts.IgnoreSrc,
		Busy: func(holder *admission.Lease) string {
			who := "another job"
			if holder != nil {
				who = fmt.Sprintf("%s (pid %d)", holder.Owner, holder.PID)
			}
			return fmt.Sprintf("%s is already running in %s on %s", who, srcLabel(h, opts.Name), h.Name)
		},
	})
	if err != nil {
		return err
	}
	defer leases.Release()
	if err := d.prepareContainer(ctx, h, container, scope, false); err != nil {
		return err
	}
	fmt.Fprintf(d.errOut(), "dot remote: running in %s on %s\n", srcLabel(h, opts.Name), h.Name)
	return d.runContainer(ctx, h, container, remoteCommand)
}

func srcLabel(h Host, name string) string {
	if name == "" {
		return "~/" + h.SrcRoot
	}
	return "~/" + h.SrcRoot + "/" + name
}
