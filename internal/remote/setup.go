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
	Host           string
	SSH            string
	User           string
	Git            string
	Rsync          string
	RsyncVersion   string
	Runtime        string
	RuntimeVersion string
	CPUs           int
	Image          string
	// Built says whether the image was built from a Dockerfile (true) or
	// pulled (false).
	Built bool
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
		fmt.Fprintf(d.out(), "would check %s over ssh:\n  %s\n", h.SSH, script)
		if h.Dockerfile != "" {
			fmt.Fprintf(d.out(), "would build %s from %s on %s\n", h.Image(), h.Dockerfile, h.Name)
		} else {
			fmt.Fprintf(d.out(), "would pull %s on %s\n", h.Ref, h.Name)
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
	return report, nil
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
		return fmt.Errorf("building %s on %s was interrupted by signal %d", h.Image(), h.Name, int(sig))
	}
	if exit != 0 {
		return fmt.Errorf("building %s on %s failed with status %d", h.Image(), h.Name, exit)
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
		return fmt.Errorf("pulling %s on %s was interrupted by signal %d", h.Ref, h.Name, int(sig))
	}
	if exit != 0 {
		return fmt.Errorf("pulling %s on %s failed with status %d", h.Ref, h.Name, exit)
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
