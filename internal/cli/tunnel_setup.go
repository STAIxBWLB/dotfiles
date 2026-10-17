package cli

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/tunnel"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

func newTunnelSetupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "setup",
		Short:        "Configure this Mac for SSH over Cloudflare Tunnel",
		Args:         cobra.NoArgs,
		RunE:         func(cmd *cobra.Command, args []string) error { return runTunnelSetupForGOOS(cmd, args, runtime.GOOS) },
		SilenceUsage: true,
	}
	cmd.Flags().String("hostname", "", "public SSH hostname (required with --yes on first setup)")
	cmd.Flags().String("name", "", "tunnel name (default: dot-<short-hostname>)")
	return cmd
}

func runTunnelSetupForGOOS(cmd *cobra.Command, _ []string, goos string) error {
	if goos != "darwin" {
		return fmt.Errorf("dot tunnel setup is macOS-only; use 'dot tunnel client' on other platforms")
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
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cannot determine home directory: %w", err)
	}

	p.Header("dot tunnel setup")

	// Resolve all inputs before any mutation (brew install, browser login,
	// sudo) so an unattended run with missing inputs fails immediately
	// instead of midway through system changes. Setup validates them again
	// as its own first step, before it touches anything.
	tunnelName, hostname, err := resolveTunnelInputs(cmd, state, yes)
	if err != nil {
		return err
	}

	result, err := tunnel.Setup(context.Background(), tunnelRunner(dryRun), tunnel.SetupOptions{
		TunnelName: tunnelName,
		Hostname:   hostname,
		Home:       home,
		DryRun:     dryRun,
		Confirm:    func(kind tunnel.ConfirmKind) (bool, error) { return confirmTunnelStep(kind, yes) },
		Progress:   func(event tunnel.Event) { renderTunnelEvent(p, tunnelName, hostname, event) },
	})
	if err != nil {
		return err
	}
	if dryRun {
		printTunnelSetupDryRun(p, result.CloudflaredPath, tunnelName, hostname)
		return nil
	}

	state.Modules.Tunnel = config.UserTunnelState{
		TunnelName: tunnelName,
		TunnelID:   result.TunnelID,
		Hostname:   hostname,
	}
	if err := config.SaveState(state); err != nil {
		return fmt.Errorf("saving tunnel state: %w", err)
	}

	p.Success("✓ dot tunnel setup complete")
	printAccessGuide(p, hostname)
	return nil
}

// resolveTunnelInputs resolves the tunnel name and hostname from flags,
// saved state, and (interactively) prompts — in that precedence order.
func resolveTunnelInputs(cmd *cobra.Command, state *config.UserState, yes bool) (string, string, error) {
	flagName, _ := cmd.Flags().GetString("name")
	flagHostname, _ := cmd.Flags().GetString("hostname")

	tunnelName := strings.TrimSpace(flagName)
	if tunnelName == "" {
		defaultName := state.Modules.Tunnel.TunnelName
		if defaultName == "" {
			host, _ := os.Hostname()
			short := strings.Split(host, ".")[0]
			defaultName = "dot-" + strings.ToLower(short)
		}
		input, err := ui.Input("Tunnel name", defaultName, yes)
		if err != nil {
			return "", "", err
		}
		tunnelName = strings.TrimSpace(input)
	}

	hostname := strings.TrimSpace(flagHostname)
	if hostname == "" {
		input, err := ui.Input("SSH hostname (for example mac.example.com)", state.Modules.Tunnel.Hostname, yes)
		if err != nil {
			return "", "", err
		}
		hostname = strings.TrimSpace(input)
	}
	if hostname == "" {
		return "", "", fmt.Errorf("SSH hostname is required; pass --hostname (unattended) or run without --yes")
	}
	return tunnelName, strings.ToLower(hostname), nil
}

func printTunnelSetupDryRun(p *Printer, cloudflaredPath, tunnelName, hostname string) {
	p.Section("Dry run")
	p.Line("  would look up or create tunnel: %s", tunnelName)
	p.Line("  would install credentials to: %s/<tunnel-id>.json", tunnel.ConfigDir)
	p.Line("  would render config to: %s", tunnel.ConfigPath)
	p.Line("  would route DNS: %s tunnel route dns %s %s", cloudflaredPath, tunnelName, hostname)
	p.Line("  would install LaunchDaemon: %s", tunnel.PlistPath)
	p.Line("  plist command: %s --no-autoupdate --config %s tunnel run", cloudflaredPath, tunnel.ConfigPath)
}

func printAccessGuide(p *Printer, hostname string) {
	p.Section("Cloudflare Access")
	p.Line("  1. Open https://one.dash.cloudflare.com/")
	p.Line("  2. Create a Self-hosted Access app for %s", hostname)
	p.Line("  3. Add an Allow policy for your account or team")
	p.Line("  4. Optional: enable browser-rendered SSH")
	p.Blank()
	p.Line("Client:")
	p.Line("  brew install cloudflared")
	p.Line("  dot tunnel client add %s", hostname)
	p.Line("  ssh <user>@%s", hostname)
	p.Blank()
	p.Line("Optional hardening:")
	p.Line("  echo 'PasswordAuthentication no' | sudo tee /etc/ssh/sshd_config.d/99-dot-tunnel.conf")
}
