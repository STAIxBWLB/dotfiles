package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/tunnel"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

func newTunnelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tunnel",
		Short: "Manage Cloudflare Tunnel SSH access for this Mac",
		Long: `Configure a locally managed Cloudflare Tunnel for SSH access to this Mac.

Server commands are macOS-only and install a system LaunchDaemon. Client
commands only manage ~/.ssh/config.d/dot-tunnel and work cross-platform.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
		SilenceUsage: true,
	}
	cmd.AddCommand(
		newTunnelSetupCmd(),
		newTunnelStatusCmd(),
		newTunnelLogCmd(),
		newTunnelUninstallCmd(),
		newTunnelClientCmd(),
	)
	return cmd
}

func newTunnelStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "status",
		Short:        "Show Cloudflare Tunnel daemon and SSH status",
		Args:         cobra.NoArgs,
		RunE:         runTunnelStatus,
		SilenceUsage: true,
	}
}

func runTunnelStatus(cmd *cobra.Command, _ []string) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("dot tunnel status is macOS-only; use 'dot tunnel client' on other platforms")
	}
	if err := rejectTunnelHomeOverride(cmd); err != nil {
		return err
	}

	p := printerFrom(cmd)
	state, err := config.LoadState()
	if err != nil {
		return fmt.Errorf("loading state: %w", err)
	}

	// Header first, then probe: the pre-move order, so a runner warning on
	// stderr still interleaves the same way against stdout.
	p.Header("dot tunnel status")
	status := tunnel.Status(context.Background(), tunnelRunner(false), tunnel.StatusOptions{
		TunnelName: state.Modules.Tunnel.TunnelName,
	})

	if status.CloudflaredFound {
		p.KV("cloudflared", status.CloudflaredVersion)
	} else {
		p.KV("cloudflared", "not found")
	}
	p.KV("Tunnel", tunnelStateLabel(state.Modules.Tunnel))
	p.KV("Hostname", state.Modules.Tunnel.Hostname)
	p.KV("Config", filePresence(tunnel.ConfigPath))
	p.KV("Daemon", string(status.Daemon))
	if status.Port22Open {
		p.KV("Port 22", "open")
	} else {
		p.KV("Port 22", "closed")
	}

	connections := "(offline or unauthorized)"
	if status.ConnectionsKnown {
		connections = strconv.Itoa(status.Connections)
	}
	p.KV("Connectors", connections)
	p.Blank()
	p.Line("  Stop:  sudo launchctl bootout system/%s", tunnel.Label)
	p.Line("  Start: sudo launchctl bootstrap system %s", tunnel.PlistPath)
	return nil
}

func newTunnelLogCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "log [N]",
		Short:        "Tail the Cloudflare Tunnel daemon error log",
		Args:         cobra.MaximumNArgs(1),
		RunE:         runTunnelLog,
		SilenceUsage: true,
	}
}

func runTunnelLog(cmd *cobra.Command, args []string) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("dot tunnel log is macOS-only")
	}
	if err := rejectTunnelHomeOverride(cmd); err != nil {
		return err
	}
	n := 50
	if len(args) > 0 {
		parsed, err := strconv.Atoi(args[0])
		if err != nil || parsed <= 0 {
			return fmt.Errorf("log line count must be a positive integer")
		}
		n = parsed
	}
	p := printerFrom(cmd)
	log, err := tunnel.TailErrorLog(tunnel.LogOptions{Lines: n})
	if err != nil {
		p.Line("No log file found at %s", tunnel.LogErrPath)
		return nil
	}
	p.Line("%s", log.Text)
	return nil
}

func newTunnelUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the dot-managed Cloudflare Tunnel daemon",
		Long: `Remove the dot-managed Cloudflare Tunnel LaunchDaemon.

Removing /etc/cloudflared config/credentials and deleting the Cloudflare
tunnel itself are interactive-only prompts that default to No; --yes never
auto-confirms them.`,
		Args:         cobra.NoArgs,
		RunE:         runTunnelUninstall,
		SilenceUsage: true,
	}
}

func runTunnelUninstall(cmd *cobra.Command, _ []string) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("dot tunnel uninstall is macOS-only")
	}
	if err := rejectTunnelHomeOverride(cmd); err != nil {
		return err
	}
	p := printerFrom(cmd)
	yes, _ := cmd.Flags().GetBool("yes")
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	state, err := config.LoadState()
	if err != nil {
		return fmt.Errorf("loading state: %w", err)
	}
	if dryRun {
		p.Line("[dry-run] would bootout %s and remove %s", tunnel.Label, tunnel.PlistPath)
		p.Line("[dry-run] would optionally remove %s and credentials, delete the Cloudflare tunnel, and clear state", tunnel.ConfigDir)
		return nil
	}

	result, err := tunnel.Uninstall(context.Background(), tunnelRunner(dryRun), tunnel.UninstallOptions{
		TunnelID:   state.Modules.Tunnel.TunnelID,
		TunnelName: state.Modules.Tunnel.TunnelName,
		Confirm:    func(kind tunnel.ConfirmKind) (bool, error) { return confirmTunnelStep(kind, yes) },
		Progress:   func(event tunnel.Event) { renderTunnelEvent(p, "", "", event) },
	})
	if err != nil {
		return err
	}
	switch result.DeleteSkip {
	case tunnel.DeleteSkipCloudflaredMissing:
		p.Warn("cloudflared not found; skipping remote tunnel delete")
	case tunnel.DeleteSkipNoTunnelName:
		p.Warn("no tunnel name in state; skipping remote tunnel delete")
	case tunnel.DeleteSkipNone:
	}

	state.Modules.Tunnel = config.UserTunnelState{}
	if err := config.SaveState(state); err != nil {
		return fmt.Errorf("saving state: %w", err)
	}
	p.Success("✓ dot tunnel uninstalled")
	p.Line("Remote Login was not changed.")
	p.Line("Remove DNS CNAME records and the Cloudflare Access app manually in the Cloudflare dashboard.")
	return nil
}

// confirmTunnelStep owns the wording of every tunnel prompt and each one's
// unattended policy. The setup questions honor --yes; the two destructive
// uninstall extras default to No and are never auto-confirmed by it, because
// an unattended uninstall must not delete credentials or the remote tunnel
// (other hosts/clients may still route through it).
func confirmTunnelStep(kind tunnel.ConfirmKind, yes bool) (bool, error) {
	switch kind {
	case tunnel.ConfirmInstallCloudflared:
		return ui.Confirm("cloudflared not found. Install it with Homebrew?", yes)
	case tunnel.ConfirmEnableRemoteLogin:
		return ui.Confirm("Remote Login is off. Enable SSH on this Mac?", yes)
	case tunnel.ConfirmOverwriteDNS:
		return ui.Confirm("A DNS record already exists for this hostname. Overwrite it?", yes)
	case tunnel.ConfirmRemoveManagedFiles:
		return ui.ConfirmBool("Remove /etc/cloudflared config and credentials managed by dot?", false, yes)
	case tunnel.ConfirmDeleteRemoteTunnel:
		return ui.ConfirmBool("Delete the Cloudflare tunnel itself?", false, yes)
	}
	return false, fmt.Errorf("unrecognized tunnel confirmation")
}

// renderTunnelEvent turns one engine step outcome into the line the pre-move
// flow printed for it. tunnelName and hostname are the caller's own resolved
// inputs; the engine does not send them back.
func renderTunnelEvent(p *Printer, tunnelName, hostname string, event tunnel.Event) {
	check := ui.StyleSuccess.Render(ui.MarkPresent)
	switch event.Kind {
	case tunnel.EventCloudflaredFound:
		p.Line("  %s cloudflared found at %s", check, event.Path)
	case tunnel.EventCloudflaredWouldInstall:
		p.Line("  ~ cloudflared not found; would install with: brew install cloudflared")
	case tunnel.EventCloudflaredInstalled:
		p.Line("  %s cloudflared installed at %s", check, event.Path)
	case tunnel.EventCertFound:
		p.Line("  %s Cloudflare cert found at %s", check, event.Path)
	case tunnel.EventCertMissing:
		p.Line("Cloudflare login cert is missing.")
		p.Line("Choose the zone that will contain the SSH hostname, then complete the browser login.")
	case tunnel.EventCertWouldLogin:
		p.Line("  ~ would run: %s tunnel login", event.Path)
	case tunnel.EventRemoteLoginReachable:
		p.Line("  %s Remote Login is reachable on localhost:22", check)
	case tunnel.EventRemoteLoginWouldEnable:
		p.Line("  ~ port 22 is closed; would enable Remote Login with systemsetup, then launchctl fallback if needed")
	case tunnel.EventRemoteLoginEnabled:
		p.Line("  %s Remote Login enabled", check)
	case tunnel.EventRemoteLoginFallback:
		p.Warn("port 22 still closed; trying launchctl fallback")
	case tunnel.EventRemoteLoginEnabledViaFallback:
		p.Line("  %s Remote Login enabled via launchctl", check)
	case tunnel.EventTunnelReused:
		p.Line("  %s reusing tunnel %s (%s)", check, tunnelName, event.TunnelID)
	case tunnel.EventTunnelCreating:
		p.Line("Creating tunnel %s...", tunnelName)
	case tunnel.EventTunnelCreated:
		p.Line("  %s created tunnel %s (%s)", check, tunnelName, event.TunnelID)
	case tunnel.EventSudoPriming:
		p.Line("Priming sudo for /etc/cloudflared and LaunchDaemon installation...")
	case tunnel.EventCredentialsInstalled:
		p.Line("  %s credentials installed to %s", check, event.Path)
	case tunnel.EventCredentialsPresent:
		p.Line("  %s credentials already installed at %s", check, event.Path)
	case tunnel.EventConfigInstalled:
		p.Line("  %s config installed to %s", check, event.Path)
	case tunnel.EventDNSUnchanged:
		p.Line("  %s DNS route already configured", check)
	case tunnel.EventDNSConfigured:
		p.Line("  %s DNS route configured for %s", check, hostname)
	case tunnel.EventDNSOverwritten:
		p.Line("  %s DNS route overwritten for %s", check, hostname)
	case tunnel.EventDaemonInstalled:
		p.Line("  %s LaunchDaemon installed and bootstrapped", check)
	case tunnel.EventConnectorRegistered:
		p.Line("  %s tunnel connector registered (%d connection(s))", check, event.Connections)
	case tunnel.EventConnectorMissing:
		p.Warn("tunnel daemon installed, but no active connector was observed within 20s")
		p.Warn("check logs with: dot tunnel log 100")
	case tunnel.EventDaemonRemoved:
		p.Line("Removed LaunchDaemon plist (if present).")
	case tunnel.EventManagedFilesRemoved:
		p.Line("Removed /etc/cloudflared managed files (if present).")
	}
}

func tunnelRunner(dryRun bool) *exec.Runner {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return exec.NewRunner(dryRun, logger)
}

func tunnelStateLabel(state config.UserTunnelState) string {
	if state.TunnelName == "" && state.TunnelID == "" {
		return "(unset)"
	}
	if state.TunnelID == "" {
		return state.TunnelName
	}
	return fmt.Sprintf("%s (%s)", state.TunnelName, state.TunnelID)
}

func filePresence(path string) string {
	if _, err := os.Stat(path); err == nil {
		return path
	}
	return "missing"
}

func rejectTunnelHomeOverride(cmd *cobra.Command) error {
	if homeOverride, _ := cmd.Flags().GetString("home"); homeOverride != "" {
		return fmt.Errorf("--home is only supported for 'dot tunnel client'; server commands manage this Mac's system daemon")
	}
	return nil
}
