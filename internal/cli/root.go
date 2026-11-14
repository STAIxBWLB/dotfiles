package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/ui"
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
	root.PersistentFlags().String("config", "", "Path to custom config YAML")
	root.PersistentFlags().String("home", "", "Override home directory (for admin setup of other users)")

	// Existing commands
	root.AddCommand(newApplyCmd())
	root.AddCommand(newStatusCmd())
	root.AddCommand(newInitCmd())
	root.AddCommand(newDiffCmd())
	root.AddCommand(newSecretsCmd())
	root.AddCommand(newUpgradeCmd(version))
	root.AddCommand(newRollbackCmd())
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

	// Remote hosts: heavy work in a container on another machine (#421)
	root.AddCommand(newRemoteCmd())

	// Claude Code safety hooks (careful + freeze)
	root.AddCommand(newGuardCmd())

	// Workspace commands
	root.AddCommand(newOpenCmd())
	root.AddCommand(newHubCmd())
	root.AddCommand(newStopCmd())
	root.AddCommand(newListCmd())
	root.AddCommand(newRegisterCmd())
	root.AddCommand(newUnregisterCmd())
	root.AddCommand(newLayoutsCmd())
	scopeProfileFlags(root)

	// macOS apps + settings backup/restore
	root.AddCommand(newAppsCmd())

	// One-stop interactive backup/restore wizards
	root.AddCommand(newBackupCmd())
	root.AddCommand(newRestoreCmd())

	// Profile snapshots (config + app lists + optional secrets)
	root.AddCommand(newProfileCmd())

	// Dual-workspace ops
	root.AddCommand(newWorkspaceDualCmd())

	configurePresentation(root)
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

// scopeProfileFlags registers --profile and --module only on the commands that
// read them; as root persistent flags they appeared on every command, where
// they meant nothing (#380). init takes --profile as the profile to preselect.
func scopeProfileFlags(root *cobra.Command) {
	readers := map[string]bool{
		"apply": true, "diff": true, "config": true, "status": true, "init": true,
		"watchdog setup": true, "watchdog notify": true, "container setup": true,
	}
	var walk func(c *cobra.Command, path string)
	walk = func(c *cobra.Command, path string) {
		for _, sub := range c.Commands() {
			name := strings.TrimSpace(path + " " + sub.Name())
			if readers[name] {
				sub.Flags().String("profile", "", "Profile name (minimal, full, server)")
				switch name {
				case "apply", "diff":
					sub.Flags().StringSlice("module", nil, "Run specific modules only")
				}
			}
			walk(sub, name)
		}
	}
	walk(root, "")
}

// configurePresentation registers invocation-wide policy before binding the
// handlers. Local verbose flags keep their existing names and meanings.
func configurePresentation(root *cobra.Command) {
	mode := ui.ColorAuto
	root.PersistentFlags().Var(&mode, "color", "Color output: auto, always or never")
	root.PersistentFlags().Bool("verbose", false, "Show library and debug logs on stderr")
	bindPresentation(root)
	defaultHelp := root.HelpFunc()
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		_ = withPresentation(cmd, func() error { defaultHelp(cmd, args); return nil })
	})
}

// bindPresentation scopes the registered handlers rather than relying on a
// post-run hook, which cobra skips when a handler returns an error.
func bindPresentation(cmd *cobra.Command) {
	if run := cmd.RunE; run != nil {
		cmd.RunE = func(c *cobra.Command, args []string) error {
			return withPresentation(c, func() error { return run(c, args) })
		}
	}
	if run := cmd.Run; run != nil {
		cmd.Run = func(c *cobra.Command, args []string) {
			_ = withPresentation(c, func() error { run(c, args); return nil })
		}
	}
	for _, child := range cmd.Commands() {
		bindPresentation(child)
	}
}

// withPresentation scopes the process logger and native UI policy even on
// failure. Printer binds each human-output stream independently. Raw child
// process payloads keep their original writers. The CLI runs one command per process.
func withPresentation(cmd *cobra.Command, run func() error) error {
	mode := ui.ColorAuto
	if value, err := cmd.Flags().GetString("color"); err == nil {
		mode = ui.ColorMode(value)
	}
	json, _ := cmd.Flags().GetBool("json")
	restoreColor := ui.SetColorPolicy(mode, json)
	defer restoreColor()
	previous := slog.Default()
	// slog.SetDefault also redirects the standard log package. Restore that
	// bridge too; restoring only the slog pointer can leave a stale sink.
	logWriter, logFlags, logPrefix := log.Writer(), log.Flags(), log.Prefix()
	slog.SetDefault(commandLogger(cmd))
	defer func() {
		slog.SetDefault(previous)
		log.SetOutput(logWriter)
		log.SetFlags(logFlags)
		log.SetPrefix(logPrefix)
	}()
	return run()
}

// commandLogger is for library execution details; user-facing diagnostics use
// Printer.Warn/Fail and remain available without verbose mode.
func commandLogger(cmd *cobra.Command) *slog.Logger {
	verbose, _ := cmd.Flags().GetBool("verbose")
	if !verbose {
		return slog.New(slog.DiscardHandler)
	}
	return slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), &slog.HandlerOptions{Level: slog.LevelDebug}))
}
