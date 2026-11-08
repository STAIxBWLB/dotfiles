package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// deprecatedAICommand keeps a moved command callable for one release. Cobra's
// Deprecated field prints on stdout; warnings here must not corrupt payloads.
func deprecatedAICommand(cmd *cobra.Command, replacement string) *cobra.Command {
	cmd.Hidden = true
	cmd.Short = "Deprecated: use " + replacement
	cmd.Long = "Deprecated: use '" + replacement + "' instead. This compatibility path will be removed after one release.\n\n" + cmd.Long
	validate := cmd.Args
	cmd.Args = func(c *cobra.Command, args []string) error {
		fmt.Fprintf(c.ErrOrStderr(), "Warning: %s is deprecated; use '%s' instead. This compatibility path will be removed after one release.\n", c.CommandPath(), replacement)
		if validate != nil {
			return validate(c, args)
		}
		return nil
	}
	return cmd
}

func newAIMCPCmd() *cobra.Command {
	c := &cobra.Command{
		Use: "mcp",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return fmt.Errorf("unknown MCP action %q; use 'dot ai mcp install <id>' or 'dot ai mcp status <id>'", args[0])
			}
			return nil
		},
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
		Short: "Register and inspect amem, vmem and ares MCP integrations",
		Long: `Register an installed family tool's MCP integration or inspect its health.

Install the binary first with 'dot ai family install <id>'. Registration uses
only the selected agents and the files dot owns. Other agents still use the
printed native setup commands. No MCP service or LaunchAgent is started.
Memory audit/archive and legacy retirement stay under 'dot ai memory' and
'dot ai vault'.`,
	}
	for _, action := range []string{"install", "status"} {
		short := "Register one installed family tool's MCP integration"
		if action == "status" {
			short = "Show one MCP integration's registration and version health"
		}
		child := &cobra.Command{
			Use:       action + " <amem|vmem|ares>",
			Short:     short,
			ValidArgs: []string{"amem", "vmem", "ares"},
			Args: func(cmd *cobra.Command, args []string) error {
				if len(args) != 1 {
					return fmt.Errorf("%s requires one MCP integration; use '%s amem', '%s vmem' or '%s ares'", cmd.CommandPath(), cmd.CommandPath(), cmd.CommandPath(), cmd.CommandPath())
				}
				return nil
			},
			RunE: func(cmd *cobra.Command, args []string) error {
				var install, status func() *cobra.Command
				switch args[0] {
				case "amem":
					install, status = newAIMemoryInstallCmd, newAIMemoryStatusCmd
				case "vmem":
					install, status = newAIVaultInstallCmd, newAIVaultStatusCmd
				case "ares":
					install, status = newAIResearchInstallCmd, newAIResearchStatusCmd
				default:
					return fmt.Errorf("unknown MCP integration %q; choose amem, vmem or ares", args[0])
				}
				factory := status
				if cmd.Name() == "install" {
					factory = install
				}
				return factory().RunE(cmd, nil)
			},
		}
		c.AddCommand(child)
	}
	return c
}
