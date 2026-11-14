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
	// Kind is "path" for a run's or verify's own mirror path, "src" for a
	// `dot remote src` clone, or "host" for the shared per-host lease every job
	// also holds.
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

// statusScript reads the mirrors, the bare repos and the image age in one ssh
// round trip.
//
// Mirrors are not top-level directories: a checkout at local_root/work/dev/one
// is mirrored to <root>/work/dev/one, so `du -sk *` at the root would report
// "work" rather than that checkout. Every run records its mirror's own path
// under <root>/.dot-remote, and this script sizes each recorded mirror. A
// failure is printed as an `error` line rather than swallowed, so an empty list
// is never read as "nothing there".
const statusScriptTemplate = `records="$HOME"/%s/.dot-remote
if [ -d "$records" ]; then
  if ! listing=$(ls -A "$records" 2>&1); then
    printf 'error\tcannot list %%s (%%s)\n' "$records" "$listing"
  else
    for name in $listing; do
      record="$records/$name"
      [ -f "$record" ] || continue
      if ! rel=$(cat "$record" 2>&1); then
        printf 'error\tcannot read the record of %%s (%%s)\n' "$name" "$rel"
        continue
      fi
      if [ -z "$rel" ]; then
        printf 'error\tthe record of %%s is empty\n' "$name"
        continue
      fi
      size=$(du -sk "$HOME"/%s/"$rel" 2>/dev/null | cut -f1)
      if [ -n "$size" ]; then
        printf 'mirror\t%%s\t%%s\n' "$size" "$rel"
      else
        printf 'error\tcannot measure the mirror of %%s\n' "$rel"
      fi
    done
  fi
elif [ -d "$HOME"/%s ]; then
  printf 'error\tthe mirror tree holds mirrors but no per-mirror record: a run of this version writes one\n'
fi
if [ -d "$HOME"/%s ]; then
  for entry in "$HOME"/%s/*; do
    [ -d "$entry" ] || continue
    name=${entry##*/}
    size=$(du -sk "$entry" 2>/dev/null | cut -f1)
    if [ -n "$size" ]; then
      printf 'bare\t%%s\t%%s\n' "$size" "$name"
    else
      printf 'error\tcannot measure the bare repo %%s\n' "$name"
    fi
  done
fi
if created=$(%s image inspect -f '{{.Created}}' %s 2>/dev/null); then
  printf 'created\t%%s\n' "$created"
fi`

// Status reads the host's state. A host that cannot answer still yields the
// local half of the view: the leases this machine holds are facts the host does
// not have to confirm.
func (d Deps) Status(ctx context.Context, cfg Config, h Host) (Status, error) {
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
		status.Active = activeRuns(cfg, h, leases, d.now())
	}
	status.Load = d.ProbeLoad(ctx, h)
	status.Verdict = h.LoadDefer(status.Load, false)

	script := fmt.Sprintf(statusScriptTemplate,
		ShellQuote(h.Root), ShellQuote(h.Root), ShellQuote(h.Root),
		ShellQuote(h.GitRoot), ShellQuote(h.GitRoot), h.Runtime, ShellQuote(h.Image()))
	out, err := d.capture(ctx, SSHArgv(h.SSH, script), nil)
	if err != nil {
		status.SizesUnavailable = err.Error()
		return status, nil
	}
	var problems []string
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
		case "error":
			if len(fields) >= 2 && fields[1] != "" {
				problems = append(problems, fields[1])
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
	if len(problems) > 0 {
		status.SizesUnavailable = strings.Join(problems, "; ")
	}
	return status, nil
}

// activeRuns selects this host's remote leases and names each one's container.
// New verify/src leases record their container. Older leases fall back to
// deriving it from the path or src scope.
func activeRuns(cfg Config, h Host, leases []admission.Lease, now time.Time) []ActiveRun {
	runs := []ActiveRun{}
	for _, lease := range leases {
		if lease.Class != admission.ClassRemote || lease.Host != h.Name {
			continue
		}
		run := ActiveRun{
			Kind:      "path",
			Scope:     lease.Scope,
			Owner:     lease.Owner,
			PID:       lease.PID,
			Since:     lease.AcquiredAt,
			Session:   lease.Session,
			Container: leaseContainer(cfg, h, lease),
		}
		if strings.HasPrefix(lease.Scope, "remote-host:") {
			run.Kind = "host"
		} else if strings.HasPrefix(lease.Scope, "remote-src:") {
			run.Kind = "src"
		} else if strings.HasPrefix(lease.Scope, "remote-job:") {
			run.Kind = "job"
		}
		if run.Since.IsZero() {
			run.Since = now
		}
		runs = append(runs, run)
	}
	return runs
}

// leaseContainer preserves recorded names and derives names for older leases.
func leaseContainer(cfg Config, h Host, lease admission.Lease) string {
	if lease.Container != "" {
		return lease.Container
	}
	if strings.HasPrefix(lease.Scope, "remote-src:") {
		name := strings.TrimPrefix(lease.Scope, "remote-src:"+h.Name+"/")
		if name == "." {
			name = ""
		}
		return h.SrcContainer(name)
	}
	if rel, err := cfg.Rel(lease.Scope); err == nil {
		return h.ContainerName(rel)
	}
	return ""
}
