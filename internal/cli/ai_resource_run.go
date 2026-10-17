package cli

import (
	"context"
	"fmt"
	"github.com/STAIxBWLB/dotfiles/internal/resourceguard"
	"github.com/spf13/cobra"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func newAIRunCmd() *cobra.Command {
	return newAIRunCmdWithAdmission(func(ctx context.Context, opts resourceguard.Options, wait time.Duration) (func(), error) {
		if wait == 0 {
			return resourceguard.Acquire(ctx, opts)
		}
		return resourceguard.WaitAcquire(ctx, opts, wait)
	})
}

func newAIRunCmdWithAdmission(admit func(context.Context, resourceguard.Options, time.Duration) (func(), error)) *cobra.Command {
	c := &cobra.Command{Use: "run [--wait 6m] -- COMMAND [ARGS...]", Short: "Run one repository heavyweight command under the admission controller", Long: `Run a command under the repository resource admission slot. Health is sampled
without launching work; unknown telemetry or uncovered heavy jobs defer it.
The child receives CARGO_BUILD_JOBS=2, RUST_TEST_THREADS=2, and GOMAXPROCS=2.
Pass tool-specific flags (Go -p 2 -parallel 2; browser E2E workers=1) yourself.
Unwrapped commands are detected conservatively, not automatically controlled.`, Args: cobra.MinimumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		if dryRun {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "Would acquire repository resource slot and run %q (no probes or writes)\n", args)
			return err
		}
		wait, _ := cmd.Flags().GetDuration("wait")
		if wait < 0 || wait > 10*time.Minute {
			return fmt.Errorf("--wait must be between 0 and 10m")
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		project, _ := cmd.Flags().GetString("project")
		opts := resourceguard.Options{HomeDir: homeFromCmd(cmd), Purpose: "manual heavyweight command", ProjectDir: project}
		release, err := admit(ctx, opts, wait)
		if err != nil {
			return err
		}
		defer release()
		return resourceguard.RunInDirectory(ctx, project, args, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
	}}
	c.Flags().String("project", "", "Checkout whose worktrees share the slot; a non-Git directory gets its own slot (defaults to current directory)")
	c.Flags().Duration("wait", 6*time.Minute, "Bounded time to wait for the slot and a healthy host (0 fails promptly)")
	return c
}
