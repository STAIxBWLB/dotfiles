package remote

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/admission"
)

// gpuProbeCommand lists every device's UUID and free memory in MiB in one ssh
// round trip.
const gpuProbeCommand = `nvidia-smi --query-gpu=uuid,memory.free --format=csv,noheader,nounits`

// GPU is one device the probe reported.
type GPU struct {
	UUID   string `json:"uuid"`
	FreeMB int    `json:"free_mb"`
}

// GPUScope is the admission scope of one device on host. Capacity is 1: a
// device is held by at most one dot lease on this machine.
func GPUScope(host, uuid string) string { return "remote-gpu:" + host + ":" + uuid }

// ParseGPUs reads the nvidia-smi CSV the probe asks for, sorted by UUID.
func ParseGPUs(out string) ([]GPU, error) {
	var gpus []GPU
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, ",")
		if len(fields) != 2 {
			return nil, fmt.Errorf("unexpected nvidia-smi line %q", line)
		}
		uuid := strings.TrimSpace(fields[0])
		free, err := strconv.Atoi(strings.TrimSpace(fields[1]))
		if uuid == "" || err != nil {
			return nil, fmt.Errorf("unexpected nvidia-smi line %q", line)
		}
		gpus = append(gpus, GPU{UUID: uuid, FreeMB: free})
	}
	if len(gpus) == 0 {
		return nil, fmt.Errorf("nvidia-smi reported no devices")
	}
	sort.Slice(gpus, func(i, j int) bool { return gpus[i].UUID < gpus[j].UUID })
	return gpus, nil
}

// ProbeGPUs runs the nvidia-smi probe on the host.
func (d Deps) ProbeGPUs(ctx context.Context, h Host) ([]GPU, error) {
	ctx, cancel := context.WithTimeout(ctx, loadProbeTimeout)
	defer cancel()
	out, err := d.capture(ctx, SSHArgv(h.SSH, gpuProbeCommand), nil)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("nvidia-smi probe timed out after %s", loadProbeTimeout)
		}
		return nil, fmt.Errorf("nvidia-smi probe: %w", err)
	}
	return ParseGPUs(out)
}

// eligibleGPUs keeps the devices with at least minFreeMB free, in UUID order.
func eligibleGPUs(gpus []GPU, minFreeMB int) []string {
	var out []string
	for _, g := range gpus {
		if g.FreeMB >= minFreeMB {
			out = append(out, g.UUID)
		}
	}
	return out
}

// heldGPUs lists the devices on host that a live dot lease holds now.
func heldGPUs(store *admission.Store, host string) map[string]bool {
	held := map[string]bool{}
	if store == nil {
		return held
	}
	leases, _ := store.ListLeases()
	prefix := GPUScope(host, "")
	for _, l := range leases {
		if strings.HasPrefix(l.Scope, prefix) {
			held[strings.TrimPrefix(l.Scope, prefix)] = true
		}
	}
	return held
}

// acquireGPUs probes the host and takes n per-device leases, trying eligible
// devices in UUID order so two holders never wait on each other's partial
// sets. Short of n, it gives back what it took and retries until deadline,
// then defers.
func (d Deps) acquireGPUs(ctx context.Context, h Host, self admission.Lease, n, minFreeMB int, deadline time.Time) ([]*admission.Slot, []string, error) {
	self.Host = h.Name
	backoff := 2 * time.Second
	for {
		gpus, err := d.ProbeGPUs(ctx, h)
		var reason string
		if err != nil {
			reason = err.Error()
		} else {
			eligible := eligibleGPUs(gpus, minFreeMB)
			var slots []*admission.Slot
			var uuids []string
			for _, uuid := range eligible {
				if len(slots) == n {
					break
				}
				lease := self
				lease.Program, lease.GPUs = "", []string{uuid}
				slot, _, aerr := d.Store.AcquireWithOptions(ctx, GPUScope(h.Name, uuid), admission.ClassRemote, lease, admission.AcquireOptions{Capacity: 1})
				if aerr != nil {
					releaseSlots(slots)
					return nil, nil, aerr
				}
				if slot != nil {
					slots = append(slots, slot)
					uuids = append(uuids, uuid)
				}
			}
			if len(slots) == n {
				return slots, uuids, nil
			}
			releaseSlots(slots)
			reason = fmt.Sprintf("%d of %d GPUs on %s have %d MB free, %d of them free of other dot leases; %d requested",
				len(eligible), len(gpus), h.Name, minFreeMB, len(uuids), n)
		}
		if time.Now().Add(backoff).After(deadline) {
			return nil, nil, deferFor(GPUScope(h.Name, "*"), reason, backoff)
		}
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-time.After(backoff):
		}
		backoff = minDuration(backoff*3/2, 15*time.Second)
	}
}

func releaseSlots(slots []*admission.Slot) {
	for _, s := range slots {
		_ = s.Release()
	}
}
