package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/tunnel"
)

func newTunnelClientCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "client",
		Short:        "Manage SSH client config for Cloudflare Access",
		Args:         cobra.NoArgs,
		RunE:         func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
		SilenceUsage: true,
	}
	cmd.AddCommand(newTunnelClientAddCmd(), newTunnelClientListCmd(), newTunnelClientRemoveCmd())
	return cmd
}

func newTunnelClientAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "add <hostname>",
		Short:        "Add an SSH ProxyCommand host block",
		Args:         cobra.ExactArgs(1),
		RunE:         runTunnelClientAdd,
		SilenceUsage: true,
	}
}

func runTunnelClientAdd(cmd *cobra.Command, args []string) error {
	hostname := strings.ToLower(strings.TrimSpace(args[0]))
	if err := tunnel.ValidateHostname(hostname); err != nil {
		return err
	}
	p := printerFrom(cmd)
	home := homeFromCmd(cmd)
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	if dryRun {
		p.Line("[dry-run] would add Host %s to %s", hostname, tunnel.DropInPath(home))
		return nil
	}
	added, warnings, err := tunnel.AddHost(home, hostname)
	if err != nil {
		return err
	}
	if added {
		p.Success("✓ added %s", hostname)
	} else {
		p.Line("%s is already configured; existing block left untouched.", hostname)
	}
	for _, warning := range warnings {
		p.Warn("%s", warning)
	}
	return nil
}

func newTunnelClientListCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "list",
		Short:        "List SSH hosts managed by dot tunnel client",
		Args:         cobra.NoArgs,
		RunE:         runTunnelClientList,
		SilenceUsage: true,
	}
}

func runTunnelClientList(cmd *cobra.Command, _ []string) error {
	p := printerFrom(cmd)
	hosts, err := tunnel.ListHosts(homeFromCmd(cmd))
	if err != nil {
		return err
	}
	if len(hosts) == 0 {
		p.Line("No tunnel SSH hosts configured.")
		return nil
	}
	for _, host := range hosts {
		p.Line("%s", host)
	}
	return nil
}

func newTunnelClientRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "remove <hostname>",
		Short:        "Remove an SSH ProxyCommand host block",
		Args:         cobra.ExactArgs(1),
		RunE:         runTunnelClientRemove,
		SilenceUsage: true,
	}
}

func runTunnelClientRemove(cmd *cobra.Command, args []string) error {
	hostname := strings.ToLower(strings.TrimSpace(args[0]))
	if err := tunnel.ValidateHostname(hostname); err != nil {
		return err
	}
	p := printerFrom(cmd)
	home := homeFromCmd(cmd)
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	if dryRun {
		p.Line("[dry-run] would remove Host %s from %s", hostname, tunnel.DropInPath(home))
		return nil
	}
	removed, err := tunnel.RemoveHost(home, hostname)
	if err != nil {
		return err
	}
	if removed {
		p.Success("✓ removed %s", hostname)
	} else {
		p.Line("%s was not configured.", hostname)
	}
	return nil
}
