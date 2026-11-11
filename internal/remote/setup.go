package remote

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// SetupCheckScript reads everything setup needs in one ssh round trip. Missing
// tools are reported as "missing" instead of aborting, so one run names every
// gap in the host rather than the first.
const setupCheckScriptTemplate = `printf 'git=%%s\n' "$(command -v git || echo missing)"
printf 'rsync=%%s\n' "$(command -v rsync || echo missing)"
printf 'rsync_version=%%s\n' "$(rsync --version 2>/dev/null | head -1)"
printf 'runtime=%%s\n' "$(%s --version 2>/dev/null | head -1)"
printf 'user=%%s\n' "$(id -un 2>/dev/null || echo unknown)"
printf 'cpus=%%s\n' "$(nproc 2>/dev/null || echo 0)"`

// SetupReport is what setup verified on the host, and which image it left
// there.
type SetupReport struct {
	Host           string `json:"host"`
	SSH            string `json:"ssh"`
	User           string `json:"user,omitempty"`
	Git            string `json:"git,omitempty"`
	Rsync          string `json:"rsync,omitempty"`
	RsyncVersion   string `json:"rsync_version,omitempty"`
	Runtime        string `json:"runtime"`
	RuntimeVersion string `json:"runtime_version,omitempty"`
	CPUs           int    `json:"cpus,omitempty"`
	Image          string `json:"image"`
	ImageUser      string `json:"image_user,omitempty"`
	// Built says whether the image was built from a Dockerfile (true) or
	// pulled (false).
	Built bool `json:"built"`
}

// SetupOptions carries the shared flags setup honors.
type SetupOptions struct {
	DryRun bool
}

// Setup checks the host and builds or pulls the dev image. It is safe to
// re-run: after an image change this is the command that rebuilds it.
func (d Deps) Setup(ctx context.Context, h Host, opts SetupOptions) (SetupReport, error) {
	report := SetupReport{Host: h.Name, SSH: h.SSH, Runtime: h.Runtime, Image: h.Image(), Built: h.Dockerfile != ""}
	script := fmt.Sprintf(setupCheckScriptTemplate, h.Runtime)
	if opts.DryRun {
		// stderr, because stdout carries the report: `--json` has to stay one
		// document whether or not the run was a dry one.
		fmt.Fprintf(d.errOut(), "would check %s over ssh:\n  %s\n", h.SSH, script)
		if h.Dockerfile != "" {
			fmt.Fprintf(d.errOut(), "would build %s from %s on %s\n", h.Image(), h.Dockerfile, h.Name)
		} else {
			fmt.Fprintf(d.errOut(), "would pull %s on %s\n", h.Ref, h.Name)
		}
		return report, nil
	}
	out, err := d.capture(ctx, SSHArgv(h.SSH, script), nil)
	if err != nil {
		return report, fmt.Errorf("checking %s: %w", h.Name, err)
	}
	values := parseKeyValues(out)
	report.User = values["user"]
	report.Git = values["git"]
	report.Rsync = values["rsync"]
	report.RsyncVersion = values["rsync_version"]
	report.RuntimeVersion = values["runtime"]
	report.CPUs, _ = strconv.Atoi(values["cpus"])

	if report.Git == "" || report.Git == "missing" {
		return report, fmt.Errorf("%s has no git on PATH; install it there (the mirror is fed by rsync and git)", h.Name)
	}
	if report.Rsync == "" || report.Rsync == "missing" {
		return report, fmt.Errorf("%s has no rsync on PATH; install rsync %s or newer there", h.Name, rsyncMinimum)
	}
	if _, verr := CheckRsyncVersion(report.RsyncVersion); verr != nil {
		return report, fmt.Errorf("%s: %w", h.Name, verr)
	}
	if report.RuntimeVersion == "" {
		return report, fmt.Errorf("%s has no %s on PATH; install a container runtime there or set remote.hosts.%s.runtime", h.Name, h.Runtime, h.Name)
	}
	if h.Dockerfile != "" {
		if err := d.buildImage(ctx, h); err != nil {
			return report, err
		}
	} else if err := d.pullImage(ctx, h); err != nil {
		return report, err
	}
	user, err := d.checkImageUser(ctx, h)
	if err != nil {
		return report, err
	}
	report.ImageUser = user
	return report, nil
}

// checkImageUser refuses an image whose user is root. A run passes no --user:
// the job runs as the image's own unprivileged user, so an image without a USER
// would write root-owned files into the mirror that the host user cannot clean
// up. This is the one place a person can still fix it cheaply, right after the
// build.
func (d Deps) checkImageUser(ctx context.Context, h Host) (string, error) {
	out, err := d.capture(ctx, SSHArgv(h.SSH, h.Runtime+" image inspect -f '{{.Config.User}}' "+ShellQuote(h.Image())), nil)
	if err != nil {
		return "", fmt.Errorf("%s cannot inspect %s (%w): check that the image was built or pulled there, then run `dot remote setup` again", h.Name, h.Image(), err)
	}
	if imageRunsAsRoot(out) {
		return "", fmt.Errorf("%s left %s without an unprivileged USER, so a run would write root-owned files into the mirror: add one to the image (ARG UID/GID, then USER dev - docs/guides/servers.md carries a starter Dockerfile) and re-run `dot remote setup`", h.Name, h.Image())
	}
	return strings.TrimSpace(out), nil
}

// buildImage builds the host's image from the local Dockerfile over stdin.
func (d Deps) buildImage(ctx context.Context, h Host) error {
	dockerfile, err := os.ReadFile(h.Dockerfile)
	if err != nil {
		return fmt.Errorf("reading %s: %w", h.Dockerfile, err)
	}
	// The UID/GID build arguments let the image's unprivileged user own a bind
	// mount the host user can read, which is what makes the mirror usable.
	command := fmt.Sprintf("%s build -t %s --build-arg UID=$(id -u) --build-arg GID=$(id -g) -",
		h.Runtime, ShellQuote(h.Image()))
	fmt.Fprintf(d.errOut(), "dot remote: building %s on %s from %s\n", h.Image(), h.Name, h.Dockerfile)
	exit, sig, err := d.ex()(ctx, SSHArgv(h.SSH, command), strings.NewReader(string(dockerfile)), d.out(), d.errOut())
	if err != nil {
		return err
	}
	if sig != 0 {
		return fmt.Errorf("building %s on %s was interrupted by signal %d: run `dot remote setup` again once the build is no longer running there", h.Image(), h.Name, int(sig))
	}
	if exit != 0 {
		return fmt.Errorf("building %s on %s failed with status %d: read the build output above, fix %s and run `dot remote setup` again", h.Image(), h.Name, exit, h.Dockerfile)
	}
	return nil
}

// pullImage pulls the prebuilt image a host names instead of building one.
func (d Deps) pullImage(ctx context.Context, h Host) error {
	fmt.Fprintf(d.errOut(), "dot remote: pulling %s on %s\n", h.Ref, h.Name)
	exit, sig, err := d.ex()(ctx, SSHArgv(h.SSH, h.Runtime+" pull "+ShellQuote(h.Ref)), nil, d.out(), d.errOut())
	if err != nil {
		return err
	}
	if sig != 0 {
		return fmt.Errorf("pulling %s on %s was interrupted by signal %d: run `dot remote setup` again", h.Ref, h.Name, int(sig))
	}
	if exit != 0 {
		return fmt.Errorf("pulling %s on %s failed with status %d: check the ref in remote.hosts.%s.image.ref and that %s can reach the registry, then run `dot remote setup` again",
			h.Ref, h.Name, exit, h.Name, h.Name)
	}
	return nil
}

// parseKeyValues reads the "key=value" lines the check script prints.
func parseKeyValues(out string) map[string]string {
	values := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), "=")
		key = strings.TrimSpace(key)
		if !found || key == "" {
			continue
		}
		values[key] = strings.TrimSpace(value)
	}
	return values
}
