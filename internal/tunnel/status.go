package tunnel

import (
	"context"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// connectorProbeTimeout bounds the connector count's Cloudflare API call so an
// offline machine doesn't make a local status command hang.
const connectorProbeTimeout = 10 * time.Second

// StatusOptions controls Status. TunnelName comes from saved state; empty
// means no tunnel is configured and the connector probe is skipped.
type StatusOptions struct {
	TunnelName string
}

// StatusResult is the probe snapshot the command layer renders. Every field is
// data: the labels, the "not found" fallback and the "(offline or
// unauthorized)" connector placeholder are presentation and stay in cli.
type StatusResult struct {
	CloudflaredPath    string
	CloudflaredFound   bool
	CloudflaredVersion string // version line, or the binary's base name
	Daemon             DaemonState
	Port22Open         bool
	Connections        int
	ConnectionsKnown   bool // false when the probe was skipped or failed
}

// Status probes cloudflared, the daemon, port 22, and the tunnel's connector
// count, in that order. It returns no error: every probe degrades into a
// field, so an unreachable Cloudflare API surfaces as ConnectionsKnown false
// rather than as a failure of the whole command.
func Status(ctx context.Context, runner *exec.Runner, opts StatusOptions) *StatusResult {
	result := &StatusResult{}
	result.CloudflaredPath, result.CloudflaredFound = lookupCloudflared()
	if result.CloudflaredFound {
		result.CloudflaredVersion = cloudflaredVersion(ctx, runner, result.CloudflaredPath)
	}
	result.Daemon = DaemonStateFor(ctx, runner)
	result.Port22Open = Port22Open(time.Second)

	if result.CloudflaredFound && opts.TunnelName != "" {
		lookupCtx, cancel := context.WithTimeout(ctx, connectorProbeTimeout)
		record, ok, err := LookupTunnelID(lookupCtx, runner, result.CloudflaredPath, opts.TunnelName)
		cancel()
		if err == nil && ok {
			result.Connections = record.Connections
			result.ConnectionsKnown = true
		}
	}
	return result
}

func lookupCloudflared() (string, bool) {
	path, err := osexec.LookPath("cloudflared")
	return path, err == nil
}

func cloudflaredVersion(ctx context.Context, runner *exec.Runner, cloudflaredPath string) string {
	result, err := runner.RunQuery(ctx, cloudflaredPath, "--version")
	if err != nil {
		return filepath.Base(cloudflaredPath)
	}
	line := strings.TrimSpace(result.Stdout)
	if line == "" {
		return filepath.Base(cloudflaredPath)
	}
	return line
}
