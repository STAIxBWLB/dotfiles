package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
)

// ErrUnknownCommand is returned by Execute when argv names no registered
// subcommand. Its message is never printed: unknownCommandGate has already
// written the user-facing guidance before this error reaches main.
var ErrUnknownCommand = errors.New("unknown command")

// NewRootCmd creates the root command with all subcommands.
func NewRootCmd(version, commit string) *cobra.Command {
	root := &cobra.Command{
		Use:   "dot",
		Short: "User environment & workspace management tool",
		Long: `dotfiles: Declarative user environment configuration with modular profiles.

Run without arguments to see a getting-started guide.
Run 'dot usecase' for detailed workflow examples.
Also available as 'dotfiles' for back-compat.`,
		Aliases:       []string{"dotfiles"},
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// No subcommand → show friendly welcome screen
			printWelcome(cmd, version, commit)
			return nil
		},
	}
	resolvedVer, resolvedCommit := ResolveVersion(version, commit)
	root.Version = resolvedVer + " (" + resolvedCommit + ")"

	// Persistent flags for all subcommands
	root.PersistentFlags().Bool("yes", false, "Unattended mode (skip all prompts)")
	root.PersistentFlags().Bool("dry-run", false, "Show what would be done without executing")
	root.PersistentFlags().String("profile", "", "Profile name (minimal, full, server)")
	root.PersistentFlags().StringSlice("module", nil, "Run specific modules only")
	root.PersistentFlags().String("config", "", "Path to custom config YAML")
	root.PersistentFlags().String("home", "", "Override home directory (for admin setup of other users)")

	// Existing commands
	root.AddCommand(newApplyCmd())
	root.AddCommand(newCheckCmd())
	root.AddCommand(newStatusCmd())
	root.AddCommand(newInitCmd())
	root.AddCommand(newDiffCmd())
	root.AddCommand(newPreflightCmd())
	root.AddCommand(newSecretsCmd())
	root.AddCommand(newUpgradeCmd(version))
	root.AddCommand(newReconfigureCmd())
	root.AddCommand(newVersionCmd(version, commit))
	root.AddCommand(newConfigCmd())
	root.AddCommand(newUsecaseCmd())
	root.AddCommand(newAICmd())

	// Shared `container` command (apple/container, docker or podman)
	root.AddCommand(newContainerCmd())

	// Workspace cleanup
	root.AddCommand(newCleanCmd())

	// Spotlight exclusion markers for build/cache dirs
	root.AddCommand(newNoindexCmd())

	// Sync (rsync workspace mirror: local cloud folder or SSH remote)
	root.AddCommand(newSyncCmd())
	root.AddCommand(newPeerCmd())

	// Cloudflare Tunnel SSH access
	root.AddCommand(newTunnelCmd())

	// Watchdog (runaway-process reaper, alerts)
	root.AddCommand(newWatchdogCmd())

	// Resource admission (per-repo heavy-job slots + host-pressure gate)
	root.AddCommand(newAdmitCmd())

	// Claude Code safety hooks (careful + freeze)
	root.AddCommand(newGuardCmd())

	// Workspace commands
	root.AddCommand(newOpenCmd())
	root.AddCommand(newStopCmd())
	root.AddCommand(newListCmd())
	root.AddCommand(newRegisterCmd())
	root.AddCommand(newUnregisterCmd())
	root.AddCommand(newLayoutsCmd())
	root.AddCommand(newDoctorCmd())

	// macOS apps + settings backup/restore
	root.AddCommand(newAppsCmd())

	// One-stop interactive backup/restore wizards
	root.AddCommand(newBackupCmd())
	root.AddCommand(newRestoreCmd())

	// Profile snapshots (config + app lists + optional secrets)
	root.AddCommand(newProfileCmd())

	// Dual-workspace ops
	root.AddCommand(newWorkspaceDualCmd())

	return root
}

// homeOverrideFrom returns the raw home override for this run: the --home flag
// the root command registers above, falling back to $DOTFILES_HOME. Empty
// means "the process home", which is what every caller's own fallback then
// resolves.
//
// It is the first two steps of the shape apply.go, check.go and diff.go
// spell out inline; commands that also load state keep that spelling so the
// state fork stays visible beside them.
func homeOverrideFrom(cmd *cobra.Command) string {
	var override string
	if cmd != nil {
		override, _ = cmd.Flags().GetString("home")
	}
	if override == "" {
		override = os.Getenv("DOTFILES_HOME")
	}
	return override
}

// homeFor resolves the home a command operates on: the override when there is
// one, the process home otherwise. Commands that also load state keep the
// fork visible at their own call site and use homeOverrideFrom instead.
func homeFor(cmd *cobra.Command) string {
	home := homeOverrideFrom(cmd)
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return home
}

// knownSubcommands is the set of all registered subcommand names + built-ins.
// Used by Execute to decide whether to inject "open" for implicit project routing.
func knownSubcommands(cmd *cobra.Command) map[string]bool {
	names := map[string]bool{
		"help":       true,
		"completion": true,
	}
	for _, sub := range cmd.Commands() {
		names[sub.Name()] = true
		for _, alias := range sub.Aliases {
			names[alias] = true
		}
	}
	return names
}

// unknownCommandGate inspects argv and decides whether the first argument
// names a registered subcommand. On an unrecognized argument it writes the
// six-line guidance to stderr and returns ErrUnknownCommand; otherwise it
// returns nil. It never terminates the process, so every deferred function
// registered by a caller between main and Execute still runs.
//
// The gate skips cobra's hidden runtime hooks (__complete, __completeNoDesc).
// Cobra registers them inside cmd.Execute() via InitDefaultCompletionCmd, so
// they're not yet present in knownSubcommands at gate time. User-facing
// commands never start with "__".
func unknownCommandGate(cmd *cobra.Command, args []string, stderr io.Writer) error {
	if len(args) <= 1 {
		return nil
	}
	first := args[1]
	if first == "" || first[0] == '-' || strings.HasPrefix(first, "__") {
		return nil
	}
	if knownSubcommands(cmd)[first] {
		return nil
	}
	fmt.Fprintf(stderr, "Unknown command %q\n", first)
	fmt.Fprintln(stderr, "")
	fmt.Fprintf(stderr, "If you meant to launch a workspace, use:\n")
	fmt.Fprintf(stderr, "  dot open %s\n", first)
	fmt.Fprintln(stderr, "")
	fmt.Fprintln(stderr, "See 'dot help' for available commands, or 'dot usecase' for examples.")
	return ErrUnknownCommand
}

// Execute runs the root command.
func Execute(version, commit string) error {
	cmd := NewRootCmd(version, commit)

	// If the first arg is not a known subcommand or flag, show an error
	// guiding the user to `dot open <project>` (explicit is safer than
	// implicit routing which could mask typos as project launches). The
	// decision is returned, not acted on here, so deferred cleanup
	// registered by any caller runs on this path.
	if err := unknownCommandGate(cmd, os.Args, cmd.ErrOrStderr()); err != nil {
		return err
	}

	return executeWithSignals(cmd)
}

// executeWithSignals lets owned child process groups finish cancellation and
// cleanup before main exits and releases the host-wide admission lock.
func executeWithSignals(cmd *cobra.Command) error {
	parent := cmd.Context()
	if parent == nil {
		parent = context.Background()
	}
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()
	return cmd.ExecuteContext(ctx)
}
