package remote

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/admission"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// admitOptions is what a verify or src job asks of the admission controller.
type admitOptions struct {
	// Scope is the one-job-at-a-time lease: the checkout's path scope for
	// verify (shared with run), or a src scope per clone.
	Scope     string
	Container string
	// Busy explains a held Scope lease.
	Busy      func(*admission.Lease) string
	Cwd       string
	Wait      time.Duration
	Ignore    admission.IgnoreSet
	IgnoreSrc string
}

// srcScope is the lease scope of one third-party clone on one host. It is not
// a local path, so `dot remote status` lists it without a mirror.
func srcScope(h Host, name string) string {
	if name == "" {
		name = "."
	}
	return "remote-src:" + h.Name + "/" + name
}

// admit takes the job's own lease, then one of the host's max_runs leases,
// then reads the host's load - the order and the rules a run follows. Every
// defer releases what it already holds.
func (d Deps) admit(ctx context.Context, h Host, opts admitOptions) (*Leases, error) {
	if d.Store == nil {
		if d.StoreErr != nil {
			return nil, fmt.Errorf("the admission store is unavailable (%w), so a remote job cannot hold its leases: make sure the state directory is writable", d.StoreErr)
		}
		return nil, errors.New("the admission store is unavailable, so a remote job cannot hold its leases: run `dot admit status` to open and check the store")
	}
	runner := d.Runner
	if runner == nil {
		runner = exec.NewProbeRunner()
	}
	self, err := admission.SelfLease(ctx, runner, opts.Cwd)
	if err != nil {
		return nil, err
	}
	self.Host = h.Name
	self.Container = opts.Container
	if self.Session == "" {
		self.Session = "dot remote"
	}
	own, err := acquire(ctx, d.Store, opts.Scope, self, 1, opts.Wait, false, opts.Busy)
	if err != nil {
		return nil, err
	}
	leases := &Leases{Path: own}
	self.Container = "" // The shared host slot has no container of its own.
	hostSlot, err := AcquireHost(ctx, d.Store, self, h, opts.Wait, opts.Ignore.Slot)
	if err != nil {
		leases.Release()
		return nil, err
	}
	leases.Host = hostSlot
	leases.stop = heartbeat(own, hostSlot)
	admission.NoteOverflowSlot(d.errOut(), hostSlot.Lease(), h.MaxRuns, opts.IgnoreSrc)
	if h.MaxLoadRatio > 0 {
		load := d.ProbeLoad(ctx, h)
		if reason := h.LoadDefer(load, false); reason != "" {
			if !opts.Ignore.RemoteLoad {
				leases.Release()
				return nil, deferFor(HostScope(h.Name), reason, time.Minute)
			}
			source := opts.IgnoreSrc
			if source == "" {
				source = "--ignore"
			}
			fmt.Fprintf(d.errOut(), "admission: remote host load ignored (%s): %s\n", source, reason)
		}
	}
	return leases, nil
}

// preflightTemplate asks the host, read-only, about one container name and the
// image: whether the name is taken (running or stopped) and which user the image
// runs as. Every runtime query is checked, as a run's inspection is.
const preflightTemplate = `name=%s
runtime=%s
if listed=$("$runtime" ps -a --filter "name=^$name$" --format '{{.Names}}' 2>&1); then
  if [ -z "$listed" ]; then
    printf 'container\tabsent\n'
  elif running=$("$runtime" ps --filter "name=^$name$" --format '{{.Names}}' 2>&1); then
    if [ -n "$running" ]; then printf 'container\trunning\n'; else printf 'container\tstopped\n'; fi
  else
    printf 'container\tquery-failed\t%%s\n' "$running"
  fi
else
  printf 'container\tquery-failed\t%%s\n' "$listed"
fi
if [ -n "$listed" ]; then
  if owner=$("$runtime" inspect -f '{{index .Config.Labels "net.staix.dot.remote-owner"}}' "$name" 2>/dev/null); then
    printf 'owner\t%%s\n' "$owner"
  fi
fi
if user=$("$runtime" image inspect -f '{{.Config.User}}' %s 2>/dev/null); then
  image_known=yes
  printf 'image\tknown\nuser\t%%s\n' "$user"
else
  printf 'image\tunknown\n'
fi`

// containerOwner binds a container to the same machine and local lease scope.
// The admission store identity prevents a separate store from reclaiming a
// still-live job. A matching label is reclaimable only after the lease is held.
func (d Deps) containerOwner(scope string) string {
	store := ""
	if d.Store != nil {
		store = d.Store.Root
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join([]string{d.machine(), store, scope}, "\x00"))))
}

// verifyIdentityTemplate checks the host identity and the image account without
// mounting the repository or caches. setpriv --init-groups needs this account.
const verifyIdentityTemplate = `if uid=$(id -u) && gid=$(id -g); then
  if [ "$uid" = 0 ]; then
    printf 'identity\troot\n'
  elif [ "${image_known:-}" != yes ]; then
    printf 'identity\tunknown\n'
  elif %s run --rm --network none --read-only --user 0 --entrypoint getent %s passwd "$uid" >/dev/null 2>&1; then
    printf 'identity\tok\n'
  else
    printf 'identity\tmissing\t%%s:%%s\n' "$uid" "$gid"
  fi
else
  printf 'identity\tunknown\n'
fi`

// prepareContainer makes container's name free for this job: a stopped
// leftover is removed; a running leftover with our ownership label is reclaimed
// while a foreign/unlabelled one defers. A missing or
// root image stops the job before anything is sent.
func (d Deps) prepareContainer(ctx context.Context, h Host, container, scope string, verify bool) error {
	script := fmt.Sprintf(preflightTemplate, ShellQuote(container), ShellQuote(h.Runtime), ShellQuote(h.Image()))
	if verify {
		script += "\n" + fmt.Sprintf(verifyIdentityTemplate, ShellQuote(h.Runtime), ShellQuote(h.Image()))
	}
	out, err := d.capture(ctx, SSHArgv(h.SSH, script), nil)
	if err != nil {
		return fmt.Errorf("checking %s on %s: %w", container, h.Name, err)
	}
	seen := parseContainerPreflight(out)
	switch seen.Container {
	case containerQueryFailed:
		return fmt.Errorf("%s could not report the container %s (%s): check that %s answers on %s", h.Name, container, seen.ContainerDetail, h.Runtime, h.Name)
	case containerRunning:
		if seen.Owner == d.containerOwner(scope) {
			if err := d.removeContainerStrict(ctx, h, container); err != nil {
				return err
			}
			fmt.Fprintf(d.errOut(), "dot remote: removed this machine's running leftover %s on %s\n", container, h.Name)
			break
		}
		return deferFor(scope, fmt.Sprintf("the container %s is already running on %s, so another machine is running this job; wait for it or remove it with `%s`",
			container, h.Name, sshCommand(h, h.Runtime+" rm -f "+quotedWord(container))), time.Minute)
	case containerStopped:
		if err := d.removeContainerStrict(ctx, h, container); err != nil {
			return err
		}
		fmt.Fprintf(d.errOut(), "dot remote: removed the stopped container %s left on %s\n", container, h.Name)
	}
	return seen.checkImage(h, verify)
}

type containerPreflight struct {
	inspection
	Owner    string
	Identity string
	HostIDs  string
}

func parseContainerPreflight(out string) containerPreflight {
	seen := containerPreflight{inspection: inspection{Container: containerAbsent, Image: imageUnknown}}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Split(strings.TrimSpace(line), "\t")
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "owner":
			seen.Owner = fields[1]
		case "identity":
			seen.Identity = fields[1]
			if len(fields) > 2 {
				seen.HostIDs = fields[2]
			}
		case "container":
			seen.Container = fields[1]
			if len(fields) > 2 {
				seen.ContainerDetail = strings.Join(fields[2:], " ")
			}
		case "image":
			seen.Image = fields[1]
		case "user":
			seen.ImageUser = fields[1]
		}
	}
	return seen
}

func (seen containerPreflight) checkImage(h Host, verify bool) error {
	if verify && seen.Identity == "root" {
		return fmt.Errorf("%s's ssh account has UID 0: change remote.hosts.%s.ssh to an unprivileged account before verifying", h.Name, h.Name)
	}

	if seen.Image == imageUnknown {
		return fmt.Errorf("%s does not have %s yet, so there is nothing to run in: run `dot remote setup`", h.Name, h.Image())
	}
	if imageRunsAsRoot(seen.ImageUser) {
		return fmt.Errorf("%s's image %s runs as root (no USER): give the image an unprivileged user (see docs/guides/servers.md) and re-run `dot remote setup`", h.Name, h.Image())
	}
	if verify && seen.Identity != "ok" {
		if seen.Identity != "missing" {
			return fmt.Errorf("could not determine the ssh account's UID/GID on %s with id -u and id -g: check remote.hosts.%s.ssh and the host's id command before verifying", h.Name, h.Name)
		}
		return fmt.Errorf("%s's image %s has no verified passwd entry for the host UID (%s): the image needs getent to check the account; install getent and build the image with the host UID and GID, then run `dot remote setup`", h.Name, h.Image(), seen.HostIDs)
	}
	return nil
}

// runContainer runs one remote command that starts container, and turns its
// end into dot's: the command's own status, 128+signal after removing the
// container on an interrupt, and a removal after ssh's own 255.
func (d Deps) runContainer(ctx context.Context, h Host, container, remoteCommand string) error {
	exit, sig, err := d.ex()(ctx, SSHArgv(h.SSH, remoteCommand), d.in(), d.out(), d.errOut())
	if err != nil {
		return err
	}
	if sig != 0 {
		if rerr := d.removeContainerStrict(ctx, h, container); rerr != nil {
			return &ExitCode{Code: 128 + int(sig), Err: fmt.Errorf("interrupted by signal %d; %w", int(sig), rerr)}
		}
		return &ExitCode{Code: 128 + int(sig), Err: fmt.Errorf("interrupted by signal %d; removed %s on %s", int(sig), container, h.Name)}
	}
	if exit == sshFailureStatus {
		if rerr := d.removeContainerStrict(ctx, h, container); rerr != nil {
			return &ExitCode{Code: exit, Err: fmt.Errorf("ssh or the command exited %d, so the job did not finish; %w", exit, rerr)}
		}
		return &ExitCode{Code: exit, Err: fmt.Errorf("ssh or the command exited %d, so the job did not finish; removed %s if it was left behind", exit, container)}
	}
	if exit != 0 {
		return &ExitCode{Code: exit, Err: fmt.Errorf("remote command exited with status %d", exit)}
	}
	return nil
}
