package remote

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/admission"
)

const fakeGPUs = "GPU-b, 50000\nGPU-a, 50000\nGPU-c, 100\n"

func TestParseGPUs(t *testing.T) {
	got, err := ParseGPUs(fakeGPUs)
	if err != nil {
		t.Fatal(err)
	}
	want := []GPU{{"GPU-a", 50000}, {"GPU-b", 50000}, {"GPU-c", 100}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseGPUs = %+v, want %+v", got, want)
	}
	for _, bad := range []string{"", "GPU-a\n", "NVIDIA-SMI has failed\n"} {
		if _, err := ParseGPUs(bad); err == nil {
			t.Errorf("ParseGPUs(%q) accepted", bad)
		}
	}
	// An unreadable free-memory value (MIG, degraded device) makes only that
	// device ineligible.
	mixed, err := ParseGPUs("GPU-m, [N/A]\nGPU-a, 50000\n")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(mixed, []GPU{{"GPU-a", 50000}, {"GPU-m", -1}}) ||
		!reflect.DeepEqual(eligibleGPUs(mixed, 0), []string{"GPU-a"}) {
		t.Fatalf("mixed probe = %+v", mixed)
	}
}

func TestGPUDeferAndProbeErrorsNameTheNextStep(t *testing.T) {
	f := newRunFixture(t)
	deps := f.deps()
	deps.Exec = (&fakeExec{t: t, gpus: "GPU-a, 10\n"}).execute
	err := deps.Hold(context.Background(), f.host, HoldOptions{Command: []string{"/missing/command"}, GPUs: 1, GPUMinFreeMB: 40000})
	requireGPUDefer(t, f.host, err)
	for _, want := range []string{"at least 40000 MiB free", "dot remote status", "--wait", "--gpu-min-free-mb"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("defer reason %q lacks %q", err, want)
		}
	}
	deps.Exec = (&fakeExec{t: t, gpus: "bash: nvidia-smi: command not found\n"}).execute
	err = deps.Hold(context.Background(), f.host, HoldOptions{Command: []string{"/missing/command"}, GPUs: 1})
	requireGPUDefer(t, f.host, err)
	if !strings.Contains(err.Error(), "ssh "+f.host.SSH+" nvidia-smi") {
		t.Errorf("probe failure %q does not say how to check nvidia-smi", err)
	}
	if held := f.leases(); len(held) != 0 {
		t.Fatalf("leases left: %+v", held)
	}
}

func TestHeldGPUsSkipsExpiredLeases(t *testing.T) {
	f := newRunFixture(t)
	self := selfLease(t, f.root)
	self.Host = f.host.Name
	for _, uuid := range []string{"GPU-a", "GPU-b"} {
		if _, _, err := f.store.AcquireWithOptions(context.Background(), GPUScope(f.host.Name, uuid), "remote", self, admission.AcquireOptions{Capacity: 1}); err != nil {
			t.Fatal(err)
		}
	}
	held, err := heldGPUs(f.store, f.host.Name, time.Now())
	if err != nil || !held["GPU-a"] || !held["GPU-b"] {
		t.Fatalf("live leases = %v (%v)", held, err)
	}
	later := f.leases()[0].Deadline.Add(time.Second)
	if held, err := heldGPUs(f.store, f.host.Name, later); err != nil || len(held) != 0 {
		t.Fatalf("expired leases counted: %v (%v)", held, err)
	}
}

// startGPUHold runs a hold whose command prints DOT_REMOTE_GPUS and waits on
// stdin. Each hold has its own fake transport, as two dot processes would.
func startGPUHold(t *testing.T, f *runFixture, gpus string) (string, func() error) {
	t.Helper()
	stdin, send := holdPipe(t)
	read, stdout := holdPipe(t)
	deps := f.deps()
	deps.Exec = (&fakeExec{t: t, loads: f.exec.loads, gpus: gpus}).execute
	deps.In, deps.Out, deps.Err = stdin, stdout, io.Discard
	done := make(chan error, 1)
	go func() {
		done <- deps.Hold(context.Background(), f.host, HoldOptions{
			Command: []string{"/bin/sh", "-c", `echo "gpus=$DOT_REMOTE_GPUS"; cat >/dev/null`},
			GPUs:    1, GPUMinFreeMB: 40000,
		})
		_ = stdout.Close()
	}()
	lines := bufio.NewScanner(read)
	if !lines.Scan() {
		t.Fatalf("hold printed nothing: %v", lines.Err())
	}
	return strings.TrimPrefix(lines.Text(), "gpus="), func() error {
		_ = send.Close()
		select {
		case err := <-done:
			return err
		case <-time.After(10 * time.Second):
			t.Fatal("hold did not exit")
			return nil
		}
	}
}

func TestHoldLeasesDistinctGPUs(t *testing.T) {
	f := newRunFixture(t)
	f.host.MaxRuns = 3
	first, stopFirst := startGPUHold(t, f, fakeGPUs)
	second, stopSecond := startGPUHold(t, f, fakeGPUs)
	if first != "GPU-a" || second != "GPU-b" {
		t.Fatalf("DOT_REMOTE_GPUS = %q and %q, want GPU-a and GPU-b in UUID order", first, second)
	}
	status, err := f.deps().Status(context.Background(), Config{Hosts: map[string]Host{f.host.Name: f.host}}, f.host)
	if err != nil {
		t.Fatal(err)
	}
	var shown []string
	for _, run := range status.Active {
		if run.Kind != "hold" {
			t.Fatalf("status row %+v, want only hold rows", run)
		}
		shown = append(shown, run.GPUs...)
	}
	if len(shown) != 2 {
		t.Fatalf("status GPUs = %v, want both leased devices", shown)
	}

	// GPU-c lacks memory: a third hold defers, starts nothing, keeps nothing.
	deps := f.deps()
	deps.Exec = (&fakeExec{t: t, gpus: fakeGPUs}).execute
	err = deps.Hold(context.Background(), f.host, HoldOptions{Command: []string{"/missing/command"}, GPUs: 1, GPUMinFreeMB: 40000})
	requireGPUDefer(t, f.host, err)
	if held := f.leases(); len(held) != 4 {
		t.Fatalf("leases = %+v, want two host and two GPU leases", held)
	}
	for _, stop := range []func() error{stopFirst, stopSecond} {
		if err := stop(); err != nil {
			t.Fatal(err)
		}
	}
	if held := f.leases(); len(held) != 0 {
		t.Fatalf("leases left: %+v", held)
	}
}

func TestHoldOneEligibleGPUDefersSecond(t *testing.T) {
	f := newRunFixture(t)
	f.host.MaxRuns = 2
	one := "GPU-a, 50000\nGPU-b, 10\n"
	got, stop := startGPUHold(t, f, one)
	if got != "GPU-a" {
		t.Fatalf("DOT_REMOTE_GPUS = %q", got)
	}
	deps := f.deps()
	deps.Exec = (&fakeExec{t: t, gpus: one}).execute
	err := deps.Hold(context.Background(), f.host, HoldOptions{Command: []string{"/missing/command"}, GPUs: 1, GPUMinFreeMB: 40000})
	requireGPUDefer(t, f.host, err)
	if err := stop(); err != nil {
		t.Fatal(err)
	}
}

func TestHoldGPUDryRunShowsProbeAndTakesNothing(t *testing.T) {
	f := newRunFixture(t)
	f.exec.gpus = fakeGPUs
	deps := f.deps()
	if err := deps.Hold(context.Background(), f.host, HoldOptions{Command: []string{"true"}, DryRun: true, JSON: true, GPUs: 2, GPUMinFreeMB: 40000}); err != nil {
		t.Fatal(err)
	}
	var plan struct {
		Probe   []GPU    `json:"gpu_probe"`
		GPUs    []string `json:"gpus"`
		Verdict string   `json:"verdict"`
	}
	if err := json.Unmarshal(f.out.Bytes(), &plan); err != nil || len(plan.Probe) != 3 ||
		!reflect.DeepEqual(plan.GPUs, []string{"GPU-a", "GPU-b"}) || plan.Verdict != "" {
		t.Fatalf("dry-run = %s (%v)", f.out.String(), err)
	}
	f.out.Reset()
	if err := deps.Hold(context.Background(), f.host, HoldOptions{Command: []string{"true"}, DryRun: true, GPUs: 3, GPUMinFreeMB: 40000}); err != nil {
		t.Fatal(err)
	}
	if out := f.out.String(); !strings.Contains(out, "GPU GPU-c 100 MiB free") || !strings.Contains(out, "would lease GPUs GPU-a, GPU-b") || !strings.Contains(out, "would defer") {
		t.Fatalf("dry-run text = %s", out)
	}
}

func TestHoldReclaimsKilledHoldersGPU(t *testing.T) {
	f := newRunFixture(t)
	f.host.MaxRuns = 1
	stdin, send := holdPipe(t)
	child := exec.Command(os.Args[0], "-test.run=^TestHoldProcess$")
	child.Env = append(os.Environ(), "DOT_TEST_HOLD_STORE="+f.store.Root, "DOT_TEST_HOLD_GPUS=GPU-a, 50000\n")
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
	_ = child.Process.Kill()
	_ = child.Wait()
	_ = send.Close()
	held := f.leases()
	if len(held) != 2 {
		t.Fatalf("killed holder's leases = %+v, want host and GPU", held)
	}
	f.store.Now = func() time.Time { return held[0].Deadline.Add(time.Second) }
	deps := f.deps()
	deps.Exec = (&fakeExec{t: t, gpus: "GPU-a, 50000\n"}).execute
	if err := deps.Hold(context.Background(), f.host, HoldOptions{Command: []string{"true"}, GPUs: 1}); err != nil {
		t.Fatalf("taking the dead holder's host and GPU: %v", err)
	}
	if held := f.leases(); len(held) != 0 {
		t.Fatalf("leases left: %+v", held)
	}
}

func requireGPUDefer(t *testing.T, host Host, err error) {
	t.Helper()
	deferErr, ok := err.(*Deferred)
	if !ok || deferErr.Outcome.Scope != GPUScope(host.Name, "*") {
		t.Fatalf("Hold = %v, want a GPU defer", err)
	}
}

func TestHoldGPUDryRunNamesHeldDevicesAndNextStep(t *testing.T) {
	f := newRunFixture(t)
	f.host.MaxRuns = 2
	_, stop := startGPUHold(t, f, fakeGPUs)
	defer func() { _ = stop() }()
	deps := f.deps()
	deps.Exec = (&fakeExec{t: t, gpus: fakeGPUs}).execute
	f.out.Reset()
	if err := deps.Hold(context.Background(), f.host, HoldOptions{Command: []string{"true"}, DryRun: true, GPUs: 2, GPUMinFreeMB: 40000}); err != nil {
		t.Fatal(err)
	}
	out := f.out.String()
	for _, want := range []string{"GPU-a held by other dot leases", "dot remote status", "--wait", "--gpu-min-free-mb"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run %q lacks %q", out, want)
		}
	}
}

func TestHoldGPUDryRunLeaseReadFailureLeasesNothing(t *testing.T) {
	f := newRunFixture(t)
	f.exec.gpus = fakeGPUs
	deps := f.deps()
	deps.Store = nil
	if err := deps.Hold(context.Background(), f.host, HoldOptions{Command: []string{"true"}, DryRun: true, GPUs: 1, GPUMinFreeMB: 40000}); err != nil {
		t.Fatal(err)
	}
	out := f.out.String()
	if strings.Contains(out, "would lease") || !strings.Contains(out, "could not read this machine's GPU leases") || !strings.Contains(out, "dot admit status") {
		t.Fatalf("dry-run = %s", out)
	}
}

func TestHoldGPUsOverDeviceCountDefersAtOnce(t *testing.T) {
	f := newRunFixture(t)
	deps := f.deps()
	deps.Exec = (&fakeExec{t: t, gpus: fakeGPUs}).execute
	start := time.Now()
	err := deps.Hold(context.Background(), f.host, HoldOptions{Command: []string{"/missing/command"}, GPUs: 4, Wait: time.Minute})
	requireGPUDefer(t, f.host, err)
	if time.Since(start) > 10*time.Second || !strings.Contains(err.Error(), "has 3") {
		t.Fatalf("defer %v after %s", err, time.Since(start))
	}
	if held := f.leases(); len(held) != 0 {
		t.Fatalf("leases left: %+v", held)
	}
}
