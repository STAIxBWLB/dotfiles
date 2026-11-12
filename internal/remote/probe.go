package remote

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// loadProbeTimeout bounds the one ssh probe a run makes before it starts. A
// host that cannot answer inside it defers: missing telemetry is not health.
const loadProbeTimeout = 30 * time.Second

// HostLoad is the host's own one-minute load against its CPU count.
type HostLoad struct {
	Load1 float64 `json:"load1"`
	CPUs  int     `json:"cpus"`
	// Err says why the probe could not read the host. Empty means it did.
	Err string `json:"error,omitempty"`
}

// Available reports whether the probe read the host.
func (l HostLoad) Available() bool { return l.Err == "" }

// Ratio is the load as a fraction of the host's CPUs.
func (l HostLoad) Ratio() float64 {
	if l.CPUs <= 0 {
		return 0
	}
	return l.Load1 / float64(l.CPUs)
}

// String renders the evidence the way `dot remote status` shows it.
func (l HostLoad) String() string {
	if !l.Available() {
		return "unavailable (" + l.Err + ")"
	}
	return fmt.Sprintf("load1 %.2f on %d CPUs (%.2f)", l.Load1, l.CPUs, l.Ratio())
}

// loadProbeCommand reads both numbers in one ssh round trip. /proc/loadavg and
// nproc exist on every Linux host the container runtimes run on.
const loadProbeCommand = `cat /proc/loadavg 2>/dev/null; nproc 2>/dev/null`

// ProbeLoad reads the host's load and CPU count. A stopped or unreadable host
// comes back as an unavailable HostLoad, never as an error: the caller decides
// what an unreadable host means, and for a run it means defer.
func (d Deps) ProbeLoad(ctx context.Context, h Host) HostLoad {
	ctx, cancel := context.WithTimeout(ctx, loadProbeTimeout)
	defer cancel()
	out, err := d.capture(ctx, SSHArgv(h.SSH, loadProbeCommand), nil)
	if err != nil {
		if ctx.Err() != nil {
			return HostLoad{Err: fmt.Sprintf("ssh timed out after %s", loadProbeTimeout)}
		}
		return HostLoad{Err: err.Error()}
	}
	load, perr := ParseLoad(out)
	if perr != nil {
		return HostLoad{Err: perr.Error()}
	}
	return load
}

// ParseLoad reads the loadavg and nproc output the probe asks for.
func ParseLoad(out string) (HostLoad, error) {
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	if len(lines) < 2 {
		return HostLoad{}, fmt.Errorf("the host did not report both load and CPU count (%q)", strings.TrimSpace(out))
	}
	first := strings.Fields(lines[0])
	if len(first) == 0 {
		return HostLoad{}, fmt.Errorf("no load average in %q", lines[0])
	}
	load1, err := strconv.ParseFloat(first[0], 64)
	if err != nil {
		return HostLoad{}, fmt.Errorf("%q is not a load average", first[0])
	}
	cpus, err := strconv.Atoi(lastField(lines[len(lines)-1]))
	if err != nil || cpus < 1 {
		return HostLoad{}, fmt.Errorf("%q is not a CPU count", lines[len(lines)-1])
	}
	return HostLoad{Load1: load1, CPUs: cpus}, nil
}

// LoadDefer is why the host is too loaded for one more run, or "" when it is
// not, when the host sets no limit, or when the ratio was bypassed.
func (h Host) LoadDefer(load HostLoad, bypassed bool) string {
	if h.MaxLoadRatio <= 0 {
		return ""
	}
	if bypassed {
		return ""
	}
	if !load.Available() {
		return fmt.Sprintf("host load unreadable on %s (%s); missing telemetry is not health (remote.hosts.%s.max_load_ratio)", h.Name, load.Err, h.Name)
	}
	if float64(load.CPUs)*h.MaxLoadRatio <= load.Load1 {
		return fmt.Sprintf("host load1 %.2f on %d CPUs is at or over %g x its CPUs (remote.hosts.%s.max_load_ratio)", load.Load1, load.CPUs, h.MaxLoadRatio, h.Name)
	}
	return ""
}

func lastField(line string) string {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1]
}
