package cli

import (
	"fmt"
	"log/slog"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/aisettings"
	"github.com/STAIxBWLB/dotfiles/internal/claudecfg"
	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/guard"
)

func newGuardCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "guard",
		Short: "Claude Code safety hooks (careful warnings + freeze boundary)",
		Long: `Manage dot-owned Claude Code PreToolUse safety hooks.

careful warns before destructive shell commands (rm -rf, DROP TABLE,
git push --force, ...). freeze denies Edit/Write outside a chosen
directory. Hooks live in ~/.claude/settings.json, tagged with a
"# dot-guard" marker; entries owned by other tools are never touched.

Guard is a guardrail, not a sandbox: careful only inspects the Bash
tool, and freeze cannot stop shell writes (sed, tee, ...). Temp dirs
and ~/.claude/plans stay writable while frozen.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
		SilenceUsage: true,
	}
	cmd.AddCommand(
		newGuardEnableCmd(),
		newGuardDisableCmd(),
		newGuardFreezeCmd(),
		newGuardUnfreezeCmd(),
		newGuardStatusCmd(),
		newGuardHookCmd(),
	)
	return cmd
}

func newGuardEnableCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "enable",
		Short:        "Register guard PreToolUse hooks in ~/.claude/settings.json",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p := printerFrom(cmd)
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			home := homeFromCmd(cmd)
			hookCmd := guard.HookCommand(resolveGuardDotPath(home))
			settingsPath := claudecfg.SettingsPath(home)

			p.Header("dot guard enable")
			if dryRun {
				p.Line("[dry-run] would register 2 PreToolUse hook entries in %s", settingsPath)
				p.Line("[dry-run] hook command: %s", hookCmd)
				p.Line("[dry-run] would set modules.guard.careful: true")
				return nil
			}

			changed, err := guard.EnsureHookEntries(guardRunner(false), home, hookCmd)
			if err != nil {
				return err
			}
			state, err := loadStateForCmd(cmd)
			if err != nil {
				return err
			}
			state.Modules.Guard.Careful = true
			if err := saveStateForCmd(cmd, state); err != nil {
				return err
			}

			if changed {
				p.Success("registered PreToolUse hooks in %s", settingsPath)
			} else {
				p.Success("PreToolUse hooks already registered in %s", settingsPath)
			}
			p.KV("Careful", "on (warns on rm -rf, DROP/TRUNCATE, force-push, reset --hard, kubectl delete, docker prune)")
			if state.Modules.Guard.FreezeDir != "" {
				p.KV("Freeze", state.Modules.Guard.FreezeDir)
			} else {
				p.KV("Freeze", "(unset) - run 'dot guard freeze <dir>' to add an edit boundary")
			}
			p.Line("Note: hooks take effect in NEW Claude Code sessions (running sessions snapshot hooks at startup).")
			return auditAIEvent(cmd, "ai.guard.enable", map[string]any{"changed": changed})
		},
	}
}

func newGuardDisableCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "disable",
		Short:        "Remove guard hook entries and clear guard state",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p := printerFrom(cmd)
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			home := homeFromCmd(cmd)
			settingsPath := claudecfg.SettingsPath(home)

			if dryRun {
				entries, err := guard.InspectHookEntries(home)
				if err != nil {
					return err
				}
				p.Line("[dry-run] would remove %d dot-guard hook entries from %s", len(entries), settingsPath)
				p.Line("[dry-run] would clear modules.guard state")
				return nil
			}

			removed, err := guard.RemoveHookEntries(guardRunner(false), home)
			if err != nil {
				return err
			}
			state, err := loadStateForCmd(cmd)
			if err != nil {
				return err
			}
			state.Modules.Guard = config.UserGuardState{}
			if err := saveStateForCmd(cmd, state); err != nil {
				return err
			}

			if removed > 0 {
				p.Success("removed %d dot-guard hook entries from %s (other hooks untouched)", removed, settingsPath)
			} else {
				p.Line("no dot-guard hook entries found in %s", settingsPath)
			}
			p.Success("cleared careful + freeze state")
			return auditAIEvent(cmd, "ai.guard.disable", map[string]any{"removed": removed})
		},
	}
}

func newGuardFreezeCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "freeze <dir>",
		Short:        "Deny Edit/Write outside the given directory",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			p := printerFrom(cmd)
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			home := homeFromCmd(cmd)

			dir, err := filepath.Abs(args[0])
			if err != nil {
				return fmt.Errorf("resolving %q: %w", args[0], err)
			}
			if resolved, err := filepath.EvalSymlinks(dir); err == nil {
				dir = resolved
			}
			info, err := os.Stat(dir)
			if err != nil {
				return fmt.Errorf("freeze boundary %s: %w", dir, err)
			}
			if !info.IsDir() {
				return fmt.Errorf("freeze boundary %s is not a directory", dir)
			}

			if dryRun {
				p.Line("[dry-run] would set modules.guard.freeze_dir: %s", dir)
				return nil
			}

			state, err := loadStateForCmd(cmd)
			if err != nil {
				return err
			}
			// Auto-register hooks so freeze works without a prior enable;
			// careful stays as-is (freeze alone must not opt into warnings).
			entries, err := guard.InspectHookEntries(home)
			if err != nil {
				return err
			}
			if len(entries) == 0 {
				hookCmd := guard.HookCommand(resolveGuardDotPath(home))
				if _, err := guard.EnsureHookEntries(guardRunner(false), home, hookCmd); err != nil {
					return err
				}
				p.Success("registered PreToolUse hooks in %s", claudecfg.SettingsPath(home))
				p.Line("Note: hooks take effect in NEW Claude Code sessions.")
			}
			state.Modules.Guard.FreezeDir = dir
			if err := saveStateForCmd(cmd, state); err != nil {
				return err
			}

			p.Success("freeze boundary set: %s", dir)
			p.Line("Edit/Write outside this directory will be denied (temp dirs and ~/.claude/plans are exempt).")
			p.Line("Takes effect immediately in sessions where guard hooks are registered.")
			return auditAIEvent(cmd, "ai.guard.freeze", map[string]any{"freeze_dir": dir})
		},
	}
}

func newGuardUnfreezeCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "unfreeze",
		Short:        "Clear the freeze boundary (hooks stay registered)",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p := printerFrom(cmd)
			dryRun, _ := cmd.Flags().GetBool("dry-run")

			state, err := loadStateForCmd(cmd)
			if err != nil {
				return err
			}
			prev := state.Modules.Guard.FreezeDir
			if prev == "" {
				p.Line("no freeze boundary set")
				return nil
			}
			if dryRun {
				p.Line("[dry-run] would clear modules.guard.freeze_dir (currently %s)", prev)
				return nil
			}
			state.Modules.Guard.FreezeDir = ""
			if err := saveStateForCmd(cmd, state); err != nil {
				return err
			}
			p.Success("freeze boundary cleared (was %s)", prev)
			return auditAIEvent(cmd, "ai.guard.unfreeze", map[string]any{"freeze_dir": prev})
		},
	}
}

func newGuardStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "status",
		Short:        "Show hook registration, careful/freeze state, and binary health",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p := printerFrom(cmd)
			home := homeFromCmd(cmd)

			entries, err := guard.InspectHookEntries(home)
			if err != nil {
				return err
			}
			state, err := loadStateForCmd(cmd)
			if err != nil {
				return err
			}
			g := state.Modules.Guard

			p.Header("dot guard status")
			if len(entries) > 0 {
				p.KV("Hooks", fmt.Sprintf("registered (%d entries in %s)", len(entries), claudecfg.SettingsPath(home)))
			} else {
				p.KV("Hooks", "not registered")
			}
			binaryOK := false
			if len(entries) > 0 {
				binary := guard.HookBinary(entries[0])
				switch {
				case binary == "":
				case strings.ContainsRune(binary, filepath.Separator):
					_, err := os.Stat(binary)
					binaryOK = err == nil
				default:
					// Bare command name: resolve via PATH, not the CWD.
					_, err := osexec.LookPath(binary)
					binaryOK = err == nil
				}
				p.KV("Hook command", fmt.Sprintf("%s (resolves: %v)", entries[0], binaryOK))
			}
			if g.Careful {
				p.KV("Careful", "on")
			} else {
				p.KV("Careful", "off")
			}
			if g.FreezeDir != "" {
				p.KV("Freeze", g.FreezeDir)
			} else {
				p.KV("Freeze", "(unset)")
			}
			if len(entries) > 0 && !binaryOK {
				p.Warn("hooks registered but binary missing - protection is inactive (fails open); rerun 'dot guard enable'")
			}
			if len(entries) == 0 && (g.Careful || g.FreezeDir != "") {
				p.Warn("guard state is set but hooks are not registered - run 'dot guard enable'")
			}
			return nil
		},
	}
}

func newGuardHookCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "hook",
		Short:  "Evaluate a PreToolUse payload from stdin (internal)",
		Hidden: true,
		// ArbitraryArgs: if a runner ever passes the `# dot-guard` marker as
		// argv instead of stripping it as a shell comment, the hook must not
		// error on every tool call.
		Args:         cobra.ArbitraryArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home := homeFromCmd(cmd)
			careful := false
			freezeDir := ""
			// State load failure falls through to zero values: the hook
			// must fail open, never block tool calls on a dot bug.
			if state, err := loadStateForCmd(cmd); err == nil {
				careful = state.Modules.Guard.Careful
				freezeDir = state.Modules.Guard.FreezeDir
			}
			d := guard.RunHook(cmd.InOrStdin(), cmd.OutOrStdout(), careful, freezeDir, home)
			if d.Permission != "" {
				// Best-effort fire log: pattern name only, never command content.
				_, _ = aisettings.AppendAIEvent(home, "ai.guard.hook_fire", map[string]any{
					"pattern":  d.Pattern,
					"decision": d.Permission,
				})
			}
			return nil
		},
	}
}

// resolveGuardDotPath picks the binary path written into settings.json.
// Prefer the stable install target so upgrades don't strand the hook on a
// stale build path; fall back to the running binary, then bare "dot".
func resolveGuardDotPath(homeDir string) string {
	stable := filepath.Join(homeDir, ".local", "bin", "dot")
	if _, err := os.Stat(stable); err == nil {
		return stable
	}
	if self, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(self); err == nil {
			return resolved
		}
		return self
	}
	return "dot"
}

func guardRunner(dryRun bool) *exec.Runner {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return exec.NewRunner(dryRun, logger)
}
