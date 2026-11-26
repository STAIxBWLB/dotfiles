package remote

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/admission"
	probeexec "github.com/STAIxBWLB/dotfiles/internal/exec"
)

func TestHoldSharesHostCapacityAndPassesThroughCommand(t *testing.T) {
	f, repo := verifyFixture(t)
	f.host.MaxRuns = 1
	f.host.MaxLoadRatio = 0.5
	f.cfg.Hosts[f.host.Name] = f.host

	stdin, send := holdPipe(t)
	read, stdout := holdPipe(t)
	deps := f.deps()
	deps.In, deps.Out = stdin, stdout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- deps.Hold(ctx, f.host, HoldOptions{Command: []string{
			"/bin/sh", "-c", `echo "$$"; read -r input; printf '%s\n' "$input"; exit 42`,
		}})
		_ = stdout.Close()
		close(done)
	}()
	defer func() {
		cancel()
		for range done {
		}
	}()
	lines := bufio.NewScanner(read)
	if !lines.Scan() {
		t.Fatalf("no command PID: %v", lines.Err())
	}
	pid := lines.Text()
	status, err := f.deps().Status(ctx, f.cfg, f.host)
	if err != nil {
		t.Fatal(err)
	}
	// The command can print its PID immediately, before the parent records it.
	for len(status.Active) == 1 && status.Active[0].CommandPID == 0 && ctx.Err() == nil {
		time.Sleep(time.Millisecond)
		status, err = f.deps().Status(ctx, f.cfg, f.host)
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(status.Active) != 1 {
		t.Fatalf("active runs = %+v, want one", status.Active)
	}
	run := status.Active[0]
	if run.Kind != "hold" || run.Program != "sh" || run.CommandPID <= 0 || run.Since.IsZero() {
		t.Fatalf("hold row = %+v", run)
	}
	if held := f.leases(); len(held) != 1 || held[0].Program != "sh" || held[0].CommandPID != run.CommandPID {
		t.Fatalf("admission owners = %+v, want command PID %d", held, run.CommandPID)
	}
	if pid != strconv.Itoa(run.CommandPID) {
		t.Fatalf("lease command PID = %d, child reported %s", run.CommandPID, pid)
	}

	for _, check := range []struct {
		name string
		err  error
	}{
		{"hold", f.deps().Hold(ctx, f.host, HoldOptions{Command: []string{"true"}})},
		{"run", f.run(RunOptions{Command: []string{"true"}})},
		{"verify", f.verifyDeps().Verify(ctx, f.cfg, VerifyOptions{Path: repo, HostName: f.host.Name})},
	} {
		requireHoldDefer(t, f.host, check.name, check.err)
	}
	if _, err := io.WriteString(send, "from stdin\n"); err != nil {
		t.Fatal(err)
	}
	if !lines.Scan() || lines.Text() != "from stdin" {
		t.Fatalf("attached stdout = %q (%v)", lines.Text(), lines.Err())
	}
	result := <-done
	var exit *ExitCode
	if !errors.As(result, &exit) || exit.Code != 42 {
		t.Fatalf("Hold = %v, want exit 42", result)
	}
	if held := f.leases(); len(held) != 0 {
		t.Fatalf("leases left after exit: %+v", held)
	}
}

func TestHoldChecksLoadAndDryRunTakesNothing(t *testing.T) {
	for _, load := range []string{"2.00", "2.01"} {
		t.Run(load, func(t *testing.T) {
			f := newRunFixture(t)
			f.host.MaxLoadRatio = 0.5
			f.exec.loads = load + " 0 0 1/100 1\n4\n"
			deps := f.deps()
			// A missing executable would fail differently if Hold tried to start it.
			opts := HoldOptions{Command: []string{"/missing/command"}}
			requireHoldDefer(t, f.host, "hold", deps.Hold(context.Background(), f.host, opts))
			opts.DryRun, opts.JSON = true, true
			deps.Store = nil // Dry-run cannot depend on a writable store.
			if err := deps.Hold(context.Background(), f.host, opts); err != nil {
				t.Fatal(err)
			}
			var plan struct {
				Outcome string
				Scope   string
				Program string
				Load    HostLoad
				Verdict string
			}
			if err := json.Unmarshal(f.out.Bytes(), &plan); err != nil ||
				plan.Outcome != "dry-run" || plan.Scope != HostScope(f.host.Name) ||
				plan.Program != "command" || plan.Load.CPUs != 4 || plan.Verdict == "" {
				t.Fatalf("dry-run = %s (%v)", f.out.String(), err)
			}
			if held := f.leases(); len(held) != 0 {
				t.Fatalf("leases left after defer or dry-run: %+v", held)
			}
		})
	}
}

func TestCancelHoldTerminalSignalForwarding(t *testing.T) {
	for _, tc := range []struct {
		name     string
		signal   syscall.Signal
		terminal bool
		want     syscall.Signal
	}{
		{"terminal Ctrl-C", syscall.SIGINT, true, 0},
		{"pipe SIGINT", syscall.SIGINT, false, syscall.SIGINT},
		{"terminal SIGTERM", syscall.SIGTERM, true, syscall.SIGTERM},
		{"pipe SIGTERM", syscall.SIGTERM, false, syscall.SIGTERM},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(context.Background())
			cancel(Interrupt(tc.signal))
			var forwarded os.Signal
			err := cancelHold(ctx, func() bool { return tc.terminal }, func(sig os.Signal) error {
				forwarded = sig
				return nil
			})
			if tc.want == 0 {
				if forwarded != nil || !errors.Is(err, os.ErrProcessDone) {
					t.Fatalf("forwarded %v, err %v; want no second signal", forwarded, err)
				}
			} else if forwarded != tc.want || err != nil {
				t.Fatalf("forwarded %v, err %v; want %v", forwarded, err, tc.want)
			}
		})
	}
}

func TestHoldForwardsInterruptAndReleases(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			f := newRunFixture(t)
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			read, stdout := holdPipe(t)
			stdin, _ := holdPipe(t)
			deps := f.deps()
			deps.Out, deps.In = stdout, stdin
			var stderr bytes.Buffer
			deps.Err = &stderr
			done := make(chan error, 1)
			go func() {
				done <- deps.Hold(ctx, f.host, HoldOptions{Command: []string{
					"/bin/sh", "-c", `trap 'echo int >&2; exit 0' INT; trap 'echo term >&2; exit 0' TERM; echo ready; read -r unused`,
				}})
				_ = stdout.Close()
			}()
			if !bufio.NewScanner(read).Scan() {
				t.Fatalf("command did not start: %v", <-done)
			}
			cancel(Interrupt(sig))
			err := <-done
			var exit *ExitCode
			if !errors.As(err, &exit) || exit.Code != 128+int(sig) {
				t.Fatalf("Hold = %v, want exit %d", err, 128+int(sig))
			}
			want := "int"
			if sig == syscall.SIGTERM {
				want = "term"
			}
			if strings.TrimSpace(stderr.String()) != want {
				t.Fatalf("command stderr = %q, want %q", stderr.String(), want)
			}
			if held := f.leases(); len(held) != 0 {
				t.Fatalf("leases left after interrupt: %+v", held)
			}
		})
	}
}

func TestHoldReclaimsKilledHolder(t *testing.T) {
	f := newRunFixture(t)
	f.host.MaxRuns = 1
	stdin, send := holdPipe(t)
	child := exec.Command(os.Args[0], "-test.run=^TestHoldProcess$")
	child.Env = append(os.Environ(), "DOT_TEST_HOLD_STORE="+f.store.Root)
	child.Stdin = stdin
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if child.ProcessState == nil {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	})
	if !bufio.NewScanner(stdout).Scan() {
		t.Fatal("holder never started its command")
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	_ = send.Close()
	held := f.leases()
	if len(held) != 1 || held[0].PID != child.Process.Pid || held[0].Program != "sh" {
		t.Fatalf("killed holder's lease = %+v", held)
	}
	// Advance past the shared store's stale heartbeat deadline.
	f.store.Now = func() time.Time { return held[0].Deadline.Add(time.Second) }
	if err := f.deps().Hold(context.Background(), f.host, HoldOptions{Command: []string{"true"}}); err != nil {
		t.Fatalf("taking the dead holder's slot: %v", err)
	}
	if held := f.leases(); len(held) != 0 {
		t.Fatalf("leases left after reclaim and release: %+v", held)
	}
}

func TestHoldProcess(t *testing.T) {
	root := os.Getenv("DOT_TEST_HOLD_STORE")
	if root == "" {
		return
	}
	host := testHost()
	host.MaxRuns = 1
	deps := Deps{Store: admission.NewStore(root, probeexec.NewProbeRunner())}
	gpus := 0
	if out := os.Getenv("DOT_TEST_HOLD_GPUS"); out != "" {
		fake := &fakeExec{t: t, gpus: out}
		deps.Exec, gpus = fake.execute, 1
	}
	if err := deps.Hold(context.Background(), host, HoldOptions{
		Command: []string{"/bin/sh", "-c", "echo ready; read -r unused"},
		GPUs:    gpus,
	}); err != nil {
		t.Fatal(err)
	}
}

func holdPipe(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	return r, w
}

func requireHoldDefer(t *testing.T, host Host, name string, err error) {
	t.Helper()
	var deferErr *Deferred
	if !errors.As(err, &deferErr) || deferErr.Outcome.Scope != HostScope(host.Name) {
		t.Fatalf("%s = %v, want a host defer", name, err)
	}
}
