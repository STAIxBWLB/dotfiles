package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"os/signal"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/admission"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/resourceguard"
	"github.com/STAIxBWLB/dotfiles/internal/watchdog"
	"github.com/spf13/cobra"
)

// The run side of `dot admit`: lease identity, process launch, heartbeat,
// signal forwarding. The gate and slot acquisition live in admit_cmd.go.

// bypass records what one run stepped past, for the stderr notes and the JSON
// records: the rules, and the pressure reasons a bypassed defer held. The zero
// value means nothing was bypassed, and then no key is added at all, so an
// ordinary run's record and outcome stay byte-identical.
type bypass struct {
	rules   []string
	reasons []string
}

// pressure records a gate decision that admitted past a pressure defer. A run
// gates twice when it waits for a slot, so the rules and reasons accumulate
// without repeating: the record names everything the run stepped past, once.
func (b *bypass) pressure(p *Printer, d admission.Decision) {
	if !d.PressureIgnored {
		return
	}
	admission.NoteIgnoredPressure(p.Err, d)
	for _, rule := range d.Ignored {
		b.rules = admissionIgnoreRules(b.rules, rule)
	}
	for _, reason := range d.IgnoredReasons {
		if !slices.Contains(b.reasons, reason) {
			b.reasons = append(b.reasons, reason)
		}
	}
}

// uncovered records what a bypassed scan found. The findings are what the rule
// would have deferred on, so the JSON record names them instead of only printing
// them on stderr.
func (b *bypass) uncovered(jobs []string) {
	reason := "uncovered heavyweight work: " + strings.Join(jobs, ", ")
	if !slices.Contains(b.reasons, reason) {
		b.reasons = append(b.reasons, reason)
	}
}

// annotate adds the bypass keys to a JSON record when anything was bypassed.
func (b bypass) annotate(record map[string]any) {
	if len(b.rules) > 0 {
		record["ignored"] = b.rules
	}
	if len(b.reasons) > 0 {
		record["ignored_reasons"] = b.reasons
	}
}

// runAdmittedChild executes the wrapped command with stdio and signal
// forwarding, a heartbeat goroutine while a slot is held, and the nested-run
// marker in the environment. The child's exit code becomes dot's.
//
// Two contracts live here. First, once the child starts, the lease is
// rewired to the CHILD's identity: the wrapper can be SIGKILLed while the
// workload keeps running in its own process group, and only a lease that
// names the child keeps the slot busy until the workload actually exits.
// Failing to verify the child's identity kills the child and aborts rather
// than holding the slot with an unverifiable one. Second, with --json the
// completion record goes to STDERR: the wrapped command's stdout is the
// payload, and mixing the JSON object into it would corrupt the stream for
// downstream consumers (the process exit code is the machine-readable
// result). Defer outcomes never run a child, so they stay on stdout.
func runAdmittedChild(cmd *cobra.Command, runner *exec.Runner, args []string, scope, class string, slot *admission.Slot, asJSON bool, bypassed bypass) error {
	ctx := cmd.Context()
	p := printerFrom(cmd)
	child := osexec.Command(args[0], args[1:]...) // #nosec G204 -- the command is the user's own invocation
	child.Stdin = cmd.InOrStdin()
	child.Stdout = cmd.OutOrStdout()
	child.Stderr = cmd.ErrOrStderr()
	child.Dir, _ = cmd.Flags().GetString("project")
	child.Env = os.Environ()
	if unlimited, _ := cmd.Flags().GetBool("no-limit-threads"); !unlimited {
		child.Env = resourceguard.LimitedEnvironment(child.Env)
	}
	child.Env = append(child.Env, admission.NestedEnv+"="+admission.NestedEnvValue(scope, class))
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := child.Start(); err != nil {
		return fmt.Errorf("starting %q: %w", strings.Join(args, " "), err)
	}
	if slot != nil {
		started, err := admission.ProcessStart(ctx, runner, child.Process.Pid)
		if err == nil {
			err = slot.UpdateIdentity(child.Process.Pid, started)
		}
		if err != nil {
			_ = child.Process.Kill()
			_ = child.Wait()
			return fmt.Errorf("binding the slot lease to the child workload: %w", err)
		}
	}

	stopHB := make(chan struct{})
	if slot != nil {
		go func() {
			ticker := time.NewTicker(admission.DefaultHeartbeatInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					// A failed heartbeat (slot lost) only shows up in the
					// next probe; the child keeps running either way.
					_ = slot.Heartbeat()
				case <-stopHB:
					return
				}
			}
		}()
	}
	defer close(stopHB)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigCh)
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			select {
			case sig := <-sigCh:
				if s, ok := sig.(syscall.Signal); ok && child.Process != nil {
					if pgid, err := syscall.Getpgid(child.Process.Pid); err == nil {
						_ = syscall.Kill(-pgid, s)
					}
				}
			case <-done:
				return
			}
		}
	}()

	waitErr := child.Wait()
	exitCode := 0
	if waitErr != nil {
		exitCode = 1
		var exitErr *osexec.ExitError
		if errors.As(waitErr, &exitErr) {
			exitCode = exitErr.ExitCode()
			if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				exitCode = 128 + int(ws.Signal())
			}
		}
	}
	if asJSON {
		record := map[string]any{
			"outcome":   "completed",
			"scope":     scope,
			"class":     class,
			"exit_code": exitCode,
		}
		bypassed.annotate(record)
		data, err := json.MarshalIndent(record, "", "  ")
		if err != nil {
			return err
		}
		// STDERR, deliberately: the child's stdout is the payload, and the
		// exit code is the machine-readable result.
		if _, err := fmt.Fprintf(p.Err, "%s\n", data); err != nil {
			return err
		}
	}
	if exitCode != 0 {
		return &ExitCodeError{Code: exitCode, Err: fmt.Errorf("command exited with status %d", exitCode)}
	}
	return nil
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// deferExit prints the defer outcome (JSON when asked) and returns the
// EX_TEMPFAIL error main turns into exit 75.
func deferExit(p *Printer, scope, class, owner string, d admission.Decision, asJSON bool, bypassed bypass) error {
	reason := strings.Join(d.Reasons, "; ")
	outcome := admission.DeferOutcome{
		Outcome:           "deferred",
		Scope:             scope,
		Class:             class,
		Owner:             owner,
		Reason:            reason,
		RetryAfterSeconds: int64(d.RetryAfter / time.Second),
		Ignored:           bypassed.rules,
		IgnoredReasons:    bypassed.reasons,
	}
	if asJSON {
		data, err := json.MarshalIndent(outcome, "", "  ")
		if err != nil {
			return err
		}
		p.Line("%s", data)
	} else {
		p.Warn("admission deferred (scope %s, class %s)", scope, class)
		p.KV("Reason", reason)
		if owner != "" {
			p.KV("Owner", owner)
		}
		p.KV("Retry after", d.RetryAfter.Round(time.Second).String())
	}
	return &ExitCodeError{Code: ExitDeferred, Err: fmt.Errorf("admission deferred: %s", reason)}
}

// notifyDefer sends at most one alert per defer episode per scope through
// the watchdog notifier. The claim is atomic (O_EXCL per-episode file), so
// concurrent defer handlers cannot duplicate the alert; a failed send
// releases the claim so the next defer retries. Best-effort: a host
// without watchdog configuration simply gets no alert.
func notifyDefer(ctx context.Context, cmd *cobra.Command, store *admission.Store, scope, class string, d admission.Decision) {
	if d.Next.DeferSince.IsZero() {
		// Episode-less defers (gate contention, slot waits) are transient and
		// keyed on "now", so they can never deduplicate; send nothing.
		return
	}
	claimed, claimPath, err := store.ClaimNotify(scope, class, d.Next.DeferSince)
	if err != nil || !claimed {
		return
	}
	mgr := watchdog.NewManager(watchdogRunner(false), homeFor(cmd))
	wcfg, err := loadWatchdogSnapshot(mgr)
	if err != nil {
		_ = os.Remove(claimPath)
		return
	}
	notifier := watchdog.NewNotifier(watchdog.ResolveNotify(wcfg.Notify, homeFor(cmd)), watchdogRunner(false), runtime.GOOS)
	msg := fmt.Sprintf("heavy job deferred for %s: %s", scope, strings.Join(d.Reasons, "; "))
	if err := notifier.Notify(ctx, "warn", msg); err != nil {
		_ = os.Remove(claimPath)
	}
}
