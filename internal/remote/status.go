package remote

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/admission"
)

// ActiveRun is one lease this host holds from this machine.
type ActiveRun struct {
	// Kind is "path" for a run's own mirror path or "host" for the shared
	// per-host lease.
	Kind      string    `json:"kind"`
	Scope     string    `json:"scope"`
	Owner     string    `json:"owner"`
	PID       int       `json:"pid"`
	Since     time.Time `json:"since"`
	Container string    `json:"container,omitempty"`
	Session   string    `json:"session,omitempty"`
}

// DirSize is one directory under a remote root.
type DirSize struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
}

// Status is the `dot remote status` view: the host's own load, what this
// machine is running there, what the mirrors hold, and how old the image is.
type Status struct {
	Host         string      `json:"host"`
	SSH          string      `json:"ssh"`
	Runtime      string      `json:"runtime"`
	Image        string      `json:"image"`
	ImageCreated string      `json:"image_created,omitempty"`
	ImageAge     string      `json:"image_age,omitempty"`
	MaxRuns      int         `json:"max_runs"`
	MaxLoadRatio float64     `json:"max_load_ratio,omitempty"`
	Load         HostLoad    `json:"load"`
	Verdict      string      `json:"verdict,omitempty"`
	Mirrors      []DirSize   `json:"mirrors"`
	BareRepos    []DirSize   `json:"bare_repos"`
	Active       []ActiveRun `json:"active_runs"`
	// SizesUnavailable is set when the host could not report its mirrors, so an
	// empty list is never read as "nothing there".
	SizesUnavailable string `json:"sizes_unavailable,omitempty"`
}

// statusScript reads the sizes and the image age in one ssh round trip.
const statusScriptTemplate = `cd "$HOME"/%s 2>/dev/null && du -sk * 2>/dev/null | while read -r kb name; do printf 'mirror\t%%s\t%%s\n' "$kb" "$name"; done
cd "$HOME"/%s 2>/dev/null && du -sk * 2>/dev/null | while read -r kb name; do printf 'bare\t%%s\t%%s\n' "$kb" "$name"; done
printf 'created\t%%s\n' "$(%s image inspect -f '{{.Created}}' %s 2>/dev/null)"`

// Status reads the host's state. A host that cannot answer still yields the
// local half of the view: the leases this machine holds are facts the host does
// not have to confirm.
func (d Deps) Status(ctx context.Context, h Host) (Status, error) {
	status := Status{
		Host:         h.Name,
		SSH:          h.SSH,
		Runtime:      h.Runtime,
		Image:        h.Image(),
		MaxRuns:      h.MaxRuns,
		MaxLoadRatio: h.MaxLoadRatio,
		Mirrors:      []DirSize{},
		BareRepos:    []DirSize{},
		Active:       []ActiveRun{},
	}
	if d.Store != nil {
		leases, err := d.Store.ListLeases()
		if err != nil {
			return status, err
		}
		status.Active = activeRuns(h, leases, d.now())
	}
	status.Load = d.ProbeLoad(ctx, h)
	status.Verdict = h.LoadDefer(status.Load, false)

	script := fmt.Sprintf(statusScriptTemplate, ShellQuote(h.Root), ShellQuote(h.GitRoot), h.Runtime, ShellQuote(h.Image()))
	out, err := d.capture(ctx, SSHArgv(h.SSH, script), nil)
	if err != nil {
		status.SizesUnavailable = err.Error()
		return status, nil
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Split(strings.TrimSpace(line), "\t")
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "mirror", "bare":
			if len(fields) < 3 {
				continue
			}
			kb, kerr := strconv.ParseInt(fields[1], 10, 64)
			if kerr != nil {
				continue
			}
			size := DirSize{Name: fields[2], Bytes: kb * 1024}
			if fields[0] == "mirror" {
				status.Mirrors = append(status.Mirrors, size)
			} else {
				status.BareRepos = append(status.BareRepos, size)
			}
		case "created":
			if len(fields) < 2 {
				continue
			}
			status.ImageCreated = fields[1]
			if created, perr := time.Parse(time.RFC3339Nano, fields[1]); perr == nil {
				status.ImageAge = d.now().Sub(created).Round(time.Minute).String()
			}
		}
	}
	return status, nil
}

// activeRuns selects this host's remote leases and names each one's container.
func activeRuns(h Host, leases []admission.Lease, now time.Time) []ActiveRun {
	runs := []ActiveRun{}
	for _, lease := range leases {
		if lease.Class != admission.ClassRemote || lease.Host != h.Name {
			continue
		}
		run := ActiveRun{
			Kind:    "path",
			Scope:   lease.Scope,
			Owner:   lease.Owner,
			PID:     lease.PID,
			Since:   lease.AcquiredAt,
			Session: lease.Session,
		}
		if strings.HasPrefix(lease.Scope, "remote-host:") {
			run.Kind = "host"
		} else {
			run.Container = h.ContainerName(PathKey(lease.Scope))
		}
		if run.Since.IsZero() {
			run.Since = now
		}
		runs = append(runs, run)
	}
	return runs
}
