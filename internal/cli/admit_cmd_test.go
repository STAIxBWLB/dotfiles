package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/admission"
	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

var admitTestClock = time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

func healthyAdmitSnapshot() admission.PressureSnapshot {
	return admission.PressureSnapshot{
		Platform:         "darwin",
		MemoryLevel:      admission.MemoryNormal,
		MemoryAvailable:  true,
		MemoryFreePct:    55,
		ThermalAvailable: true,
		Load1:            2,
		NumCPU:           10,
		LoadAvailable:    true,
		IdlePct:          85,
		IdleAvailable:    true,
		WSScanOK:         true,
	}
}

// stubAdmitMonitor pins the gate to deterministic pressure evidence and a
// fixed clock, the way the watchdog ForGOOS seams pin the platform.
func stubAdmitMonitor(t *testing.T, snap admission.PressureSnapshot) {
	t.Helper()
	stubAdmitMonitorAt(t, snap, admitTestClock)
}

// admitSandbox isolates HOME, the nested marker, and the working directory
// (a non-git temp dir, so the scope is the deterministic cwd fallback).
func admitSandbox(t *testing.T) (home, workdir string) {
	t.Helper()
	home = t.TempDir()
	workdir = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv(admission.IgnoreEnv, "")
	t.Setenv(admission.NestedEnv, "")
	t.Setenv(admission.OwnerEnv, "test-owner@test-host")
	t.Chdir(workdir)
	stubAdmitUncovered(t, nil)
	return home, workdir
}

// stubAdmitUncovered replaces the process-table scan with a fixed inventory.
func stubAdmitUncovered(t *testing.T, jobs []string) {
	t.Helper()
	stubAdmitScan(t, jobs, nil)
}

func stubAdmitScan(t *testing.T, jobs []string, err error) {
	t.Helper()
	old := admitFindUncovered
	admitFindUncovered = func(context.Context, string, bool, []int, admission.UncoveredSettings) ([]string, error) {
		return jobs, err
	}
	t.Cleanup(func() { admitFindUncovered = old })
}

func fallbackScopeFor(t *testing.T, dir string) string {
	t.Helper()
	scope, err := admission.FallbackScope(dir)
	if err != nil {
		t.Fatal(err)
	}
	return scope
}

func TestAdmitRejectsBadClass(t *testing.T) {
	admitSandbox(t)
	_, _, err := runDotForTest("admit", "--class", "bogus", "--", "true")
	if err == nil || !strings.Contains(err.Error(), "--class") {
		t.Errorf("err = %v, want a --class validation error", err)
	}
}

func TestAdmitNestedSkipsAcquire(t *testing.T) {
	_, workdir := admitSandbox(t)
	scope := fallbackScopeFor(t, workdir)
	t.Setenv(admission.NestedEnv, admission.NestedEnvValue(scope, admission.ClassHeavy))
	// No fixture monitor at all: the nested path must not touch the gate.
	out, errOut, err := runDotForTest("admit", "--json", "--", "true")
	if err != nil {
		t.Fatalf("nested admit = %v\nstderr=%s", err, errOut)
	}
	if !strings.Contains(errOut, `"outcome": "completed"`) {
		t.Errorf("stderr = %q, want the completed outcome (stdout stays the payload)", errOut)
	}
	if strings.Contains(out, "outcome") {
		t.Errorf("stdout = %q, want no JSON mixed into the payload stream", out)
	}
}

func TestAdmitPressureDeferExits75(t *testing.T) {
	admitSandbox(t)
	snap := healthyAdmitSnapshot()
	snap.MemoryLevel = admission.MemoryCritical
	stubAdmitMonitor(t, snap)
	out, _, err := runDotForTest("admit", "--json", "--wait", "0", "--", "true")
	var exitErr *ExitCodeError
	if !errors.As(err, &exitErr) || exitErr.Code != ExitDeferred {
		t.Fatalf("err = %v, want ExitCodeError %d", err, ExitDeferred)
	}
	if !strings.Contains(out, `"outcome": "deferred"`) || !strings.Contains(out, "memory pressure critical") {
		t.Errorf("out = %q, want the defer outcome naming the reason", out)
	}
}

func TestAdmitSlotBusyDefersWithOwner(t *testing.T) {
	home, workdir := admitSandbox(t)
	stubAdmitMonitor(t, healthyAdmitSnapshot())
	scope := fallbackScopeFor(t, workdir)

	// Occupy the slot with a fresh lease owned by someone else.
	store := admission.NewStore(admission.DefaultStateRoot(home), nil)
	slot, holder, err := store.Acquire(context.Background(), scope, admission.ClassHeavy, admission.Lease{
		Owner: "builder@mac-mini", PID: 4242, PIDStart: "Sun Sep 27 11:00:00 2026", CWD: workdir,
	})
	if err != nil || slot == nil || holder != nil {
		t.Fatalf("seeding the slot = %v, %v, %v", slot, holder, err)
	}

	out, _, err := runDotForTest("admit", "--json", "--wait", "0", "--", "true")
	var exitErr *ExitCodeError
	if !errors.As(err, &exitErr) || exitErr.Code != ExitDeferred {
		t.Fatalf("err = %v, want ExitCodeError %d", err, ExitDeferred)
	}
	if !strings.Contains(out, "builder@mac-mini") || !strings.Contains(out, "slot busy") {
		t.Errorf("out = %q, want the owner and the slot-busy reason", out)
	}
	if err := slot.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestAdmitSuccessRunsCommand(t *testing.T) {
	home, _ := admitSandbox(t)
	stubAdmitMonitor(t, healthyAdmitSnapshot())
	out, errOut, err := runDotForTest("admit", "--json", "--wait", "0", "--", "true")
	if err != nil {
		t.Fatalf("admit = %v\nstderr=%s", err, errOut)
	}
	if !strings.Contains(errOut, `"outcome": "completed"`) || !strings.Contains(errOut, `"exit_code": 0`) {
		t.Errorf("stderr = %q, want the completed outcome", errOut)
	}
	if strings.Contains(out, "outcome") {
		t.Errorf("stdout = %q, want no JSON mixed into the payload stream", out)
	}
	// The slot must be released after the run.
	store := admission.NewStore(admission.DefaultStateRoot(home), nil)
	owners, err := store.ListLeases()
	if err != nil {
		t.Fatal(err)
	}
	if len(owners) != 0 {
		t.Errorf("owners after the run = %v, want the slot released", owners)
	}
}

// TestAdmitRefusesUnverifiableIdentity: when the ps start-time probe fails,
// the wrapper must not acquire a slot at all — a lease with an empty
// PIDStart could later be reclaimed from a live owner.
func TestAdmitRefusesUnverifiableIdentity(t *testing.T) {
	home, _ := admitSandbox(t)
	stubAdmitMonitor(t, healthyAdmitSnapshot())
	// An empty PATH makes the ps probe fail; the fallback scope needs no
	// git either, and the child never starts.
	t.Setenv("PATH", t.TempDir())
	_, _, err := runDotForTest("admit", "--json", "--wait", "0", "--", "true")
	if err == nil || !strings.Contains(err.Error(), "unverifiable identity") {
		t.Fatalf("err = %v, want the unverifiable-identity refusal", err)
	}
	var exitErr *ExitCodeError
	if errors.As(err, &exitErr) {
		t.Errorf("err = %v, want a hard error, not a defer", err)
	}
	store := admission.NewStore(admission.DefaultStateRoot(home), nil)
	owners, lerr := store.ListLeases()
	if lerr != nil {
		t.Fatal(lerr)
	}
	if len(owners) != 0 {
		t.Errorf("owners = %v, want no slot acquired with an unverifiable identity", owners)
	}
}

// TestAdmitLeaseBindsChildIdentity: while the wrapped command runs, the
// lease on disk names the CHILD, not the wrapper — a wrapper SIGKILL must
// not free the slot under a live workload.
func TestAdmitLeaseBindsChildIdentity(t *testing.T) {
	admitSandbox(t)
	stubAdmitMonitor(t, healthyAdmitSnapshot())
	capture := filepath.Join(t.TempDir(), "slots-copy")
	// The rewire lands right after child.Start, concurrently with the
	// child's own start: poll until the lease names someone other than the
	// wrapper ($PPID) before copying.
	script := `for i in $(seq 1 100); do
  pid=$(grep -h -o '"pid": [0-9]*' "$HOME/.local/state/dot/admission/slots"/*/lease.json 2>/dev/null | grep -o '[0-9]*')
  if [ -n "$pid" ] && [ "$pid" != "$PPID" ]; then break; fi
  sleep 0.05
done
cp -R "$HOME/.local/state/dot/admission/slots" "$1"`
	_, _, err := runDotForTest("admit", "--wait", "0", "--", "sh", "-c", script, "sh", capture)
	if err != nil {
		t.Fatalf("admit = %v", err)
	}
	leases := findLeaseFiles(t, capture)
	if len(leases) != 1 {
		t.Fatalf("captured lease files = %v, want exactly 1", leases)
	}
	data, err := os.ReadFile(leases[0])
	if err != nil {
		t.Fatal(err)
	}
	var lease admission.Lease
	if err := json.Unmarshal(data, &lease); err != nil {
		t.Fatal(err)
	}
	if lease.PID == 0 || lease.PID == os.Getpid() {
		t.Errorf("lease pid = %d, want the child's pid, not the wrapper's %d", lease.PID, os.Getpid())
	}
	if lease.PIDStart == "" {
		t.Error("lease pid_start empty after the child identity rewire")
	}
}

func findLeaseFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			candidate := filepath.Join(root, e.Name(), "lease.json")
			if _, err := os.Stat(candidate); err == nil {
				out = append(out, candidate)
			}
		}
	}
	return out
}

// --- JSON golden fixtures (GUARD-04 matrix) ---

// goldenAdmitClock fixes every timestamp the admission goldens record.
var goldenAdmitClock = time.Date(2026, 9, 27, 12, 0, 0, 0, time.FixedZone("KST", 9*3600))

// goldenAdmitBase builds the admission sandbox for the golden surfaces: a
// symlink-resolved temp HOME, a real git repo as the working directory (so
// the scope is path-based and the @ROOT@ normalizer can pin it), and a
// monitor stubbed to deterministic pressure evidence on a fixed clock. The
// temp paths go through EvalSymlinks because git rev-parse reports the
// resolved path (/var -> /private/var on macOS) while the golden normalizer
// can only replace the path string it is handed.
func goldenAdmitBase(t *testing.T, snap admission.PressureSnapshot) (home, root string) {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv(admission.IgnoreEnv, "")
	t.Setenv(admission.NestedEnv, "")
	t.Setenv(admission.OwnerEnv, "golden-owner@golden-host")
	t.Setenv(admission.SessionEnv, "golden-session")
	stubAdmitMonitorAt(t, snap, goldenAdmitClock)
	stubAdmitUncovered(t, nil)
	repo := filepath.Join(root, "repo")
	runner := exec.NewRunner(false, slog.Default())
	if _, err := runner.Run(context.Background(), "git", "init", repo); err != nil {
		t.Fatalf("git init: %v", err)
	}
	t.Chdir(repo)
	return home, root
}

// stubAdmitMonitorAt is stubAdmitMonitor with the clock pinned, for golden
// fixtures whose output carries timestamps.
func stubAdmitMonitorAt(t *testing.T, snap admission.PressureSnapshot, now time.Time) {
	t.Helper()
	old := admitNewMonitor
	admitNewMonitor = func(*exec.Runner, string) *admission.Monitor {
		return &admission.Monitor{
			GOOS: "darwin",
			Now:  func() time.Time { return now },
			SnapshotFunc: func(context.Context, *admission.Monitor) admission.PressureSnapshot {
				return snap
			},
		}
	}
	t.Cleanup(func() { admitNewMonitor = old })
}

func goldenAdmitFixture(t *testing.T) (home, root string) {
	t.Helper()
	return goldenAdmitBase(t, healthyAdmitSnapshot())
}

func goldenAdmitDeferFixture(t *testing.T) (home, root string) {
	t.Helper()
	snap := healthyAdmitSnapshot()
	snap.MemoryLevel = admission.MemoryCritical
	return goldenAdmitBase(t, snap)
}

// goldenAdmitStatusFixture additionally seeds one active owner with
// fully-fixed lease fields, so the owners array pins every per-item field
// the way the AI fixture pins its one-item collections.
func goldenAdmitStatusFixture(t *testing.T) (home, root string) {
	t.Helper()
	home, root = goldenAdmitFixture(t)
	repo := filepath.Join(root, "repo")
	runner := exec.NewRunner(false, slog.Default())
	scope, err := admission.ResolveScope(context.Background(), runner, repo)
	if err != nil {
		t.Fatal(err)
	}
	store := admission.NewStore(admission.DefaultStateRoot(home), runner)
	store.Now = func() time.Time { return goldenAdmitClock }
	slot, holder, err := store.Acquire(context.Background(), scope, admission.ClassHeavy, admission.Lease{
		Owner:    "golden-owner@golden-host",
		Session:  "golden-session",
		PID:      4242,
		PIDStart: "Sun Sep 27 11:00:00 2026",
		PGID:     4242,
		CWD:      repo,
	})
	if err != nil || slot == nil || holder != nil {
		t.Fatalf("seeding the owner = %v, %v, %v", slot, holder, err)
	}
	return home, root
}

// TestAdmitStatusThermalRow: the text status names the thermal state and
// shows an unavailable probe as such (#161).
func TestAdmitStatusThermalRow(t *testing.T) {
	admitSandbox(t)
	snap := healthyAdmitSnapshot()
	// Fair does not defer, so the name can only come from the Thermal row,
	// not from a defer-reason bullet.
	snap.ThermalState = admission.ThermalFair
	stubAdmitMonitor(t, snap)
	out, _, _ := runDotForTest("admit", "status")
	if !regexp.MustCompile(`Thermal:\s+fair`).MatchString(out) {
		t.Errorf("Thermal row does not name the state:\n%s", out)
	}
	snap.ThermalAvailable = false
	snap.ThermalState = -1
	stubAdmitMonitor(t, snap)
	out, _, _ = runDotForTest("admit", "status")
	if !regexp.MustCompile(`Thermal:\s+\(unavailable\)`).MatchString(out) || strings.Contains(out, "nominal") {
		t.Errorf("unavailable thermal probe must print (unavailable):\n%s", out)
	}
}

func TestAdmitStatusDoesNotShowStaleRecoveryCountdown(t *testing.T) {
	home, _ := admitSandbox(t)
	stubAdmitMonitorAt(t, healthyAdmitSnapshot(), admitTestClock)
	store := admission.NewStore(admission.DefaultStateRoot(home), nil)
	stale := admitTestClock.Add(-2 * time.Minute)
	if err := admission.SaveHistory(store.HistoryPath(), admission.History{
		DeferActive:  true,
		DeferSince:   stale,
		RecoverSince: stale,
		LastSampleAt: stale,
	}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(store.HistoryPath())
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := runDotForTest("admit", "status")
	if err != nil {
		t.Fatalf("admit status: %v", err)
	}
	zeroCountdown := regexp.MustCompile(`(?m)^[ \t]*Recovery:[ \t]+0s of normal telemetry still required[ \t]*$`)
	if !strings.Contains(out, "waiting for normal telemetry") || zeroCountdown.MatchString(out) {
		t.Errorf("status showed a stale recovery countdown:\n%s", out)
	}
	after, err := os.ReadFile(store.HistoryPath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Errorf("status modified persisted admission history:\nbefore: %s\nafter:  %s", before, after)
	}
}

// AC6 (CLI side): heavy work that holds no slot defers `dot admit` (#162).
func TestAdmitDefersOnUncoveredWork(t *testing.T) {
	admitSandbox(t)
	stubAdmitMonitor(t, healthyAdmitSnapshot())
	stubAdmitUncovered(t, []string{"cargo(pid=77,parent=1,repo=)"})
	out, _, err := runDotForTest("admit", "--json", "--wait", "0", "--", "true")
	var exitErr *ExitCodeError
	if !errors.As(err, &exitErr) || exitErr.Code != ExitDeferred {
		t.Fatalf("err = %v, want ExitCodeError %d", err, ExitDeferred)
	}
	if !strings.Contains(out, "uncovered heavyweight work: cargo(pid=77") {
		t.Errorf("out = %q, want the uncovered job as the reason", out)
	}
}

// AC7: --home does not open a second slot; the slot lives under the real
// user's state root (#162).
func TestAdmitSlotIgnoresHomeFlag(t *testing.T) {
	home, workdir := admitSandbox(t)
	stubAdmitMonitor(t, healthyAdmitSnapshot())
	store := admission.NewStore(admission.DefaultStateRoot(home), nil)
	slot, _, err := store.Acquire(context.Background(), fallbackScopeFor(t, workdir), admission.ClassHeavy, admission.Lease{
		Owner: "builder@mac-mini", PID: 4242, PIDStart: "Sun Sep 27 11:00:00 2026", CWD: workdir,
	})
	if err != nil || slot == nil {
		t.Fatalf("seeding the slot = %v, %v", slot, err)
	}
	defer func() { _ = slot.Release() }()
	out, _, err := runDotForTest("--home", t.TempDir(), "admit", "--json", "--wait", "0", "--", "true")
	var exitErr *ExitCodeError
	if !errors.As(err, &exitErr) || exitErr.Code != ExitDeferred || !strings.Contains(out, "builder@mac-mini") {
		t.Fatalf("an alternate --home bypassed the slot: err=%v out=%q", err, out)
	}
}

// A resource admission lease shows in `dot admit status` with its purpose.
func TestAdmitStatusShowsLeaseSession(t *testing.T) {
	home, workdir := admitSandbox(t)
	stubAdmitMonitor(t, healthyAdmitSnapshot())
	store := admission.NewStore(admission.DefaultStateRoot(home), nil)
	slot, _, err := store.Acquire(context.Background(), fallbackScopeFor(t, workdir), admission.ClassHeavy, admission.Lease{
		Owner: "yj@mac", Session: "manual heavyweight command", PID: 4242, PIDStart: "Sun Sep 27 11:00:00 2026", CWD: workdir,
	})
	if err != nil || slot == nil {
		t.Fatalf("seeding the slot = %v, %v", slot, err)
	}
	defer func() { _ = slot.Release() }()
	out, _, _ := runDotForTest("admit", "status")
	if !strings.Contains(out, "yj@mac pid 4242") || !strings.Contains(out, "(manual heavyweight command)") {
		t.Errorf("status does not list the lease with its purpose:\n%s", out)
	}
}

// A busy slot reports its owner even when the holder's heavy work is on the
// process table: the scan runs only once the slot is held (#162).
func TestAdmitBusySlotReportsOwnerNotUncovered(t *testing.T) {
	home, workdir := admitSandbox(t)
	stubAdmitMonitor(t, healthyAdmitSnapshot())
	stubAdmitUncovered(t, []string{"cargo(pid=88,parent=4242,repo=)"})
	store := admission.NewStore(admission.DefaultStateRoot(home), nil)
	slot, _, err := store.Acquire(context.Background(), fallbackScopeFor(t, workdir), admission.ClassHeavy, admission.Lease{
		Owner: "builder@mac-mini", PID: 4242, PIDStart: "Sun Sep 27 11:00:00 2026", CWD: workdir,
	})
	if err != nil || slot == nil {
		t.Fatalf("seeding the slot = %v, %v", slot, err)
	}
	defer func() { _ = slot.Release() }()
	out, _, err := runDotForTest("admit", "--json", "--wait", "0", "--", "true")
	var exitErr *ExitCodeError
	if !errors.As(err, &exitErr) || !strings.Contains(out, "builder@mac-mini") || strings.Contains(out, "uncovered") {
		t.Fatalf("err=%v out=%q; want a slot-busy defer naming the owner", err, out)
	}
}

// A failed process scan defers with its cause and frees the slot it took.
func TestAdmitDefersOnScanError(t *testing.T) {
	home, workdir := admitSandbox(t)
	stubAdmitMonitor(t, healthyAdmitSnapshot())
	stubAdmitScan(t, nil, errors.New("ps timed out"))
	out, _, err := runDotForTest("admit", "--json", "--wait", "0", "--", "true")
	var exitErr *ExitCodeError
	if !errors.As(err, &exitErr) || exitErr.Code != ExitDeferred || !strings.Contains(out, "heavy-work inventory unavailable: ps timed out") {
		t.Fatalf("err=%v out=%q; want a defer naming the scan failure", err, out)
	}
	store := admission.NewStore(admission.DefaultStateRoot(home), nil)
	if slot, _, err := store.Acquire(context.Background(), fallbackScopeFor(t, workdir), admission.ClassHeavy, admission.Lease{Owner: "x", PID: 4242, PIDStart: "x"}); err != nil || slot == nil {
		t.Fatalf("slot leaked after a scan-error defer: %v, %v", slot, err)
	} else {
		_ = slot.Release()
	}
}

// dot admit hands the live leases' PIDs to the scan, so another slot's work is
// not counted as uncovered.
func TestAdmitPassesLeasesToScan(t *testing.T) {
	home, _ := admitSandbox(t)
	stubAdmitMonitor(t, healthyAdmitSnapshot())
	store := admission.NewStore(admission.DefaultStateRoot(home), nil)
	other, _, err := store.Acquire(context.Background(), admission.MaintenanceScope, admission.ClassMaintenance, admission.Lease{Owner: "tooling@mac", PID: 4242, PIDStart: "x"})
	if err != nil || other == nil {
		t.Fatalf("seeding = %v, %v", other, err)
	}
	defer func() { _ = other.Release() }()
	var got []int
	old := admitFindUncovered
	admitFindUncovered = func(_ context.Context, _ string, _ bool, leased []int, _ admission.UncoveredSettings) ([]string, error) {
		got = leased
		return nil, nil
	}
	t.Cleanup(func() { admitFindUncovered = old })
	if _, _, err := runDotForTest("admit", "--wait", "0", "--", "true"); err != nil {
		t.Fatalf("admit: %v", err)
	}
	found := false
	for _, pid := range got {
		found = found || pid == 4242
	}
	if !found {
		t.Fatalf("scan got leased PIDs %v, want the maintenance holder 4242", got)
	}
}

// stubAdmitGate scripts gate decisions: contention and pressure without
// probing the host or the history lock.
func stubAdmitGate(t *testing.T, decisions []admission.Decision) *int {
	t.Helper()
	calls := new(int)
	old := admitGate
	admitGate = func(context.Context, *admission.Store, *admission.Monitor, admission.GatePolicy) (admission.Decision, error) {
		*calls++
		if *calls > len(decisions) {
			return decisions[len(decisions)-1], nil
		}
		return decisions[*calls-1], nil
	}
	t.Cleanup(func() { admitGate = old })
	return calls
}

// AC1 (#168): history-lock contention is retried inside --wait; the second
// evaluation admits and the command runs.
func TestAdmitBusyGateRetriesWithinWait(t *testing.T) {
	admitSandbox(t)
	stubAdmitMonitor(t, healthyAdmitSnapshot())
	calls := stubAdmitGate(t, []admission.Decision{
		{Admit: false, Reasons: []string{admission.GateBusyReason}, RetryAfter: time.Millisecond},
		{Admit: true},
		{Admit: true},
	})
	_, _, err := runDotForTest("admit", "--wait", "1m", "--", "true")
	if err != nil {
		t.Fatalf("admit = %v, want the busy gate retried to admission", err)
	}
	if *calls < 2 {
		t.Errorf("gate evaluations = %d, want the busy evaluation retried", *calls)
	}
}

// The recovery countdown is resampled inside --wait: a one-shot defer would
// leave every later sample past the 45s gap and the episode never closes.
func TestAdmitRecoveringGateRetriesWithinWait(t *testing.T) {
	admitSandbox(t)
	stubAdmitMonitor(t, healthyAdmitSnapshot())
	calls := stubAdmitGate(t, []admission.Decision{
		{Admit: false, Reasons: []string{"recovering"}, Recovering: true, RetryAfter: time.Millisecond},
		{Admit: true},
		{Admit: true},
	})
	if _, _, err := runDotForTest("admit", "--wait", "1m", "--", "true"); err != nil {
		t.Fatalf("admit = %v, want the recovery countdown resampled to admission", err)
	}
	if *calls < 2 {
		t.Errorf("gate evaluations = %d, want the countdown resampled", *calls)
	}
}

// Hard pressure still defers at once, whatever the wait budget.
func TestAdmitHardPressureDefersWithoutRetry(t *testing.T) {
	admitSandbox(t)
	stubAdmitMonitor(t, healthyAdmitSnapshot())
	calls := stubAdmitGate(t, []admission.Decision{
		{Admit: false, Reasons: []string{"memory pressure critical"}, RetryAfter: time.Millisecond},
		{Admit: true},
	})
	_, _, err := runDotForTest("admit", "--wait", "1m", "--", "true")
	var exitErr *ExitCodeError
	if !errors.As(err, &exitErr) || exitErr.Code != ExitDeferred {
		t.Fatalf("err = %v, want ExitCodeError %d", err, ExitDeferred)
	}
	if *calls != 1 {
		t.Errorf("gate evaluations = %d, want 1", *calls)
	}
}

// AC1 (#168): contention defers with exit 75 only once the wait is used up.
func TestAdmitBusyGateDefersWhenWaitExhausted(t *testing.T) {
	admitSandbox(t)
	stubAdmitMonitor(t, healthyAdmitSnapshot())
	stubAdmitGate(t, []admission.Decision{
		{Admit: false, Reasons: []string{admission.GateBusyReason}, RetryAfter: 10 * time.Second},
	})
	out, _, err := runDotForTest("admit", "--json", "--wait", "0", "--", "true")
	var exitErr *ExitCodeError
	if !errors.As(err, &exitErr) || exitErr.Code != ExitDeferred {
		t.Fatalf("err = %v, want ExitCodeError %d", err, ExitDeferred)
	}
	if !strings.Contains(out, admission.GateBusyReason) {
		t.Errorf("out = %q, want the busy-gate reason", out)
	}
}

// AC2 (#168): an episode-less defer (gate contention, slot waits) claims no
// watchdog alert; alerts stay one per pressure episode.
func TestNotifyDeferSkipsEpisodeLessDefer(t *testing.T) {
	home, _ := admitSandbox(t)
	store := admission.NewStore(admission.DefaultStateRoot(home), nil)
	notifyDefer(context.Background(), nil, store, "scope", admission.ClassHeavy, admission.Decision{
		Reasons: []string{admission.GateBusyReason},
	})
	if _, err := os.Stat(filepath.Join(admission.DefaultStateRoot(home), "notify")); !os.IsNotExist(err) {
		t.Errorf("episode-less defer left a notify claim: %v", err)
	}
}

// Worker defaults apply on both normal and nested admission, while opting out
// preserves the caller's settings. JSON completion stays off the child payload.
func TestAdmitWorkerLimits(t *testing.T) {
	for _, nested := range []bool{false, true} {
		for _, unlimited := range []bool{false, true} {
			t.Run(fmt.Sprintf("nested=%t/unlimited=%t", nested, unlimited), func(t *testing.T) {
				_, workdir := admitSandbox(t)
				stubAdmitMonitor(t, healthyAdmitSnapshot())
				t.Setenv("CARGO_BUILD_JOBS", "1")
				t.Setenv("RUST_TEST_THREADS", "7")
				t.Setenv("GOMAXPROCS", "9")
				if nested {
					t.Setenv(admission.NestedEnv, admission.NestedEnvValue(fallbackScopeFor(t, workdir), admission.ClassHeavy))
				}
				args := []string{"admit", "--wait", "0", "--json"}
				if unlimited {
					args = append(args, "--no-limit-threads")
				}
				args = append(args, "--", "/bin/sh", "-c", `printf '%s/%s/%s' "$CARGO_BUILD_JOBS" "$RUST_TEST_THREADS" "$GOMAXPROCS"`)
				out, errOut, err := runDotForTest(args...)
				want := "2/2/2"
				if unlimited {
					want = "1/7/9"
				}
				if err != nil || out != want || !strings.Contains(errOut, `"outcome": "completed"`) {
					t.Fatalf("out=%q want=%q err=%v stderr=%s", out, want, err, errOut)
				}
				if os.Getenv("GOMAXPROCS") != "9" {
					t.Fatal("worker limits changed the wrapper environment")
				}
			})
		}
	}
}

func TestAdmitProjectBindsSlotAndChild(t *testing.T) {
	_, workdir := admitSandbox(t)
	project := t.TempDir()
	scope := fallbackScopeFor(t, project)
	t.Setenv(admission.NestedEnv, admission.NestedEnvValue(scope, admission.ClassHeavy))
	out, _, err := runDotForTest("admit", "--project", project, "--", "/bin/pwd")
	actual, _ := filepath.EvalSymlinks(strings.TrimSpace(out))
	expected, _ := filepath.EvalSymlinks(project)
	if err != nil || actual != expected {
		t.Fatalf("project cwd=%q want=%q err=%v", out, project, err)
	}
	cwd, _ := os.Getwd()
	actualCWD, _ := filepath.EvalSymlinks(cwd)
	expectedCWD, _ := filepath.EvalSymlinks(workdir)
	if actualCWD != expectedCWD {
		t.Fatal("--project changed the wrapper working directory")
	}
}

func TestAdmitDryRunHasNoProbesOrWrites(t *testing.T) {
	home, _ := admitSandbox(t)
	old := admitGate
	admitGate = func(context.Context, *admission.Store, *admission.Monitor, admission.GatePolicy) (admission.Decision, error) {
		t.Fatal("dry-run probed host")
		return admission.Decision{}, nil
	}
	t.Cleanup(func() { admitGate = old })
	out, _, err := runDotForTest("admit", "--dry-run", "--", "not-a-command")
	if err != nil || !strings.Contains(out, "no probes or writes") {
		t.Fatalf("out=%q err=%v", out, err)
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatalf("dry-run wrote home entries=%v err=%v", entries, err)
	}
}

func TestAdmitPreservesChildExitCode(t *testing.T) {
	_, workdir := admitSandbox(t)
	t.Setenv(admission.NestedEnv, admission.NestedEnvValue(fallbackScopeFor(t, workdir), admission.ClassHeavy))
	out, errOut, err := runDotForTest("admit", "--json", "--", "/bin/sh", "-c", "exit 7")
	var exitErr *ExitCodeError
	if !errors.As(err, &exitErr) || exitErr.Code != 7 || out != "" || !strings.Contains(errOut, `"exit_code": 7`) {
		t.Fatalf("exit result=%v stdout=%q stderr=%s", err, out, errOut)
	}
}

// --- configurable limits, relax and one-off bypass (#412) ---

// writeAdmitUserConfig writes the user state file a person edits, with the
// admission block under test.
func writeAdmitUserConfig(t *testing.T, home, body string) {
	t.Helper()
	path := filepath.Join(home, ".config", "dotfiles", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// stubAdmitGateCapture records the policy the CLI hands the gate.
func stubAdmitGateCapture(t *testing.T) *admission.GatePolicy {
	t.Helper()
	var got admission.GatePolicy
	old := admitGate
	admitGate = func(_ context.Context, _ *admission.Store, _ *admission.Monitor, policy admission.GatePolicy) (admission.Decision, error) {
		got = policy
		return admission.Decision{Admit: true}, nil
	}
	t.Cleanup(func() { admitGate = old })
	return &got
}

// AC2: the admission block reaches the gate and status reports each key with
// the layer it came from.
func TestAdmitConfigPolicyReachesGateAndStatus(t *testing.T) {
	home, _ := admitSandbox(t)
	stubAdmitMonitor(t, healthyAdmitSnapshot())
	writeAdmitUserConfig(t, home, `admission:
  thresholds:
    recover_sustain: 1m
  slots:
    heavy_per_repo: 2
  uncovered:
    ignore_names: [node]
`)

	policy := stubAdmitGateCapture(t)
	if _, _, err := runDotForTest("admit", "--wait", "0", "--", "true"); err != nil {
		t.Fatalf("admit = %v", err)
	}
	if policy.Thresholds.RecoverSustain != time.Minute {
		t.Errorf("gate recover_sustain = %s, want 1m0s", policy.Thresholds.RecoverSustain)
	}

	out, _, err := runDotForTest("admit", "status")
	if err != nil {
		t.Fatalf("status = %v", err)
	}
	checks := []string{
		`thresholds.recover_sustain:\s+1m0s \(config\)`,
		`thresholds.defer_sustain:\s+1m0s \(default\)`,
		`slots.heavy_per_repo:\s+2 \(config\)`,
		`uncovered.ignore_names:\s+node \(config\)`,
		`prebuilt.safety_window:\s+30s \(default\)`,
	}
	for _, want := range checks {
		if !regexp.MustCompile(want).MatchString(out) {
			t.Errorf("status does not show %s:\n%s", want, out)
		}
	}
}

// AC2: an invalid value fails loudly, naming the key, on both surfaces. Status
// is the exception: it reports the bad key and still renders the diagnostic
// view against the defaults, because that is when the person runs it.
func TestAdmitInvalidConfigPolicyNamesKey(t *testing.T) {
	home, _ := admitSandbox(t)
	stubAdmitMonitor(t, healthyAdmitSnapshot())
	writeAdmitUserConfig(t, home, "admission:\n  thresholds:\n    idle_defer_below: 150\n")

	_, _, err := runDotForTest("admit", "--wait", "0", "--", "true")
	if err == nil || !strings.Contains(err.Error(), "admission.thresholds.idle_defer_below") {
		t.Fatalf("admit err = %v, want the key named", err)
	}
	var exitErr *ExitCodeError
	if errors.As(err, &exitErr) {
		t.Errorf("err = %v, want a hard error, not a defer", err)
	}
	_, statusErrOut, err := runDotForTest("admit", "status")
	if err != nil {
		t.Fatalf("status = %v, want the diagnostic view even with a blocked config", err)
	}
	if !strings.Contains(statusErrOut, "admission.thresholds.idle_defer_below") {
		t.Errorf("status stderr = %q, want the offending key named", statusErrOut)
	}
	if _, _, err := runDotForTest("admit", "relax", "--set", "slots.heavy_per_repo=9"); err == nil ||
		!strings.Contains(err.Error(), "admission.slots.heavy_per_repo") {
		t.Fatalf("relax err = %v, want the key named", err)
	}
}

// AC4/§4: --ignore pressure admits past pressure, still records the sample, and
// marks the JSON records.
func TestAdmitIgnorePressureFlagAdmitsAndMarksJSON(t *testing.T) {
	home, _ := admitSandbox(t)
	snap := healthyAdmitSnapshot()
	snap.MemoryLevel = admission.MemoryCritical
	stubAdmitMonitor(t, snap)

	out, errOut, err := runDotForTest("admit", "--ignore", "pressure", "--json", "--wait", "0", "--", "true")
	if err != nil {
		t.Fatalf("admit = %v\nstderr=%s", err, errOut)
	}
	if out != "" {
		t.Errorf("stdout = %q, want no payload mixed into the stream", out)
	}
	if !strings.Contains(errOut, `"ignored": [`) || !strings.Contains(errOut, `"pressure"`) {
		t.Errorf("stderr = %q, want the ignored rule list in the completion record", errOut)
	}
	if !strings.Contains(errOut, "pressure gate ignored (--ignore pressure)") || !strings.Contains(errOut, "memory pressure critical") {
		t.Errorf("stderr = %q, want the one-line bypass note naming the reason", errOut)
	}
	store := admission.NewStore(admission.DefaultStateRoot(home), nil)
	hist, err := admission.LoadHistory(store.HistoryPath())
	if err != nil {
		t.Fatal(err)
	}
	if !hist.DeferActive {
		t.Errorf("history = %+v, want the bypassed sample recorded as a defer episode", hist)
	}
}

// The environment variable is the one-off bypass resourceguard honors; the CLI
// reads it through the same resolver.
func TestAdmitIgnorePressureEnvAdmits(t *testing.T) {
	admitSandbox(t)
	snap := healthyAdmitSnapshot()
	snap.MemoryLevel = admission.MemoryCritical
	stubAdmitMonitor(t, snap)
	t.Setenv(admission.IgnoreEnv, "pressure")

	_, errOut, err := runDotForTest("admit", "--json", "--wait", "0", "--", "true")
	if err != nil {
		t.Fatalf("admit = %v\nstderr=%s", err, errOut)
	}
	if !strings.Contains(errOut, `"ignored"`) || !strings.Contains(errOut, admission.IgnoreEnv+"=pressure") {
		t.Errorf("stderr = %q, want the env-labeled bypass", errOut)
	}
}

func TestAdmitIgnoreRejectsUnknownRule(t *testing.T) {
	admitSandbox(t)
	_, _, err := runDotForTest("admit", "--ignore", "slots", "--", "true")
	if err == nil || !strings.Contains(err.Error(), `"slots"`) {
		t.Fatalf("err = %v, want the unknown rule named", err)
	}
}

// AC4/§4: --ignore uncovered lets the scan's findings through, printing them.
func TestAdmitIgnoreUncoveredNamesWhatItFound(t *testing.T) {
	admitSandbox(t)
	stubAdmitMonitor(t, healthyAdmitSnapshot())
	stubAdmitScan(t, []string{"make(pid=4242,parent=1,repo=repo-a)"}, nil)

	_, _, err := runDotForTest("admit", "--json", "--wait", "0", "--", "true")
	var exitErr *ExitCodeError
	if !errors.As(err, &exitErr) || exitErr.Code != ExitDeferred {
		t.Fatalf("err = %v, want the uncovered defer", err)
	}

	_, errOut, err := runDotForTest("admit", "--ignore", "uncovered", "--json", "--wait", "0", "--", "true")
	if err != nil {
		t.Fatalf("admit under --ignore uncovered = %v\nstderr=%s", err, errOut)
	}
	if !strings.Contains(errOut, "uncovered heavy work ignored (--ignore uncovered)") || !strings.Contains(errOut, "make(pid=4242") {
		t.Errorf("stderr = %q, want the ignored findings named", errOut)
	}
	// The findings are what the rule would have deferred on, so the machine
	// record names them too instead of only printing them.
	if !strings.Contains(errOut, `"ignored_reasons"`) ||
		!strings.Contains(errOut, "uncovered heavyweight work: make(pid=4242,parent=1,repo=repo-a)") {
		t.Errorf("stderr = %q, want the ignored findings in ignored_reasons", errOut)
	}
}

// AC3: a relax override admits a deferring gate; --clear removes it and the
// gate defers again.
func TestAdmitRelaxAdmitsThenClearRestoresGate(t *testing.T) {
	home, _ := admitSandbox(t)
	snap := healthyAdmitSnapshot()
	snap.MemoryLevel = admission.MemoryCritical
	stubAdmitMonitor(t, snap)

	if out, _, err := runDotForTest("admit", "relax", "--for", "10m", "--ignore", "all"); err != nil {
		t.Fatalf("relax = %v", err)
	} else if !strings.Contains(out, "until") || !strings.Contains(out, "pressure,slot,uncovered") {
		t.Errorf("relax output = %q, want the expiry and the rules", out)
	}
	overridePath := filepath.Join(admission.DefaultStateRoot(home), admission.RelaxFileName)
	data, err := os.ReadFile(overridePath)
	if err != nil {
		t.Fatalf("reading the override: %v", err)
	}
	var override admission.RelaxOverride
	if err := json.Unmarshal(data, &override); err != nil {
		t.Fatal(err)
	}
	if len(override.Ignore) != 3 || override.ExpiresAt.IsZero() || override.SetAt.IsZero() {
		t.Errorf("override = %+v, want all three rules with both timestamps", override)
	}

	_, errOut, err := runDotForTest("admit", "--json", "--wait", "0", "--", "true")
	if err != nil || !strings.Contains(errOut, `"ignored"`) {
		t.Fatalf("admit under relax = %v\nstderr=%s", err, errOut)
	}

	if _, _, err := runDotForTest("admit", "relax", "--clear"); err != nil {
		t.Fatalf("clear = %v", err)
	}
	if _, err := os.Stat(overridePath); !os.IsNotExist(err) {
		t.Errorf("override file survived --clear: %v", err)
	}
	_, _, err = runDotForTest("admit", "--json", "--wait", "0", "--", "true")
	var exitErr *ExitCodeError
	if !errors.As(err, &exitErr) || exitErr.Code != ExitDeferred {
		t.Fatalf("err = %v, want the pressure defer back", err)
	}
}

// --set uses the same key table as the config file and layers over it.
func TestAdmitRelaxSetLayersAndRejectsUnknownKeys(t *testing.T) {
	admitSandbox(t)
	stubAdmitMonitor(t, healthyAdmitSnapshot())

	if _, _, err := runDotForTest("admit", "relax", "--for", "10m",
		"--set", "thresholds.recover_sustain=1m", "--set", "slots.heavy_per_repo=2"); err != nil {
		t.Fatalf("relax --set = %v", err)
	}
	out, _, err := runDotForTest("admit", "status")
	if err != nil {
		t.Fatalf("status = %v", err)
	}
	if !regexp.MustCompile(`thresholds.recover_sustain:\s+1m0s \(relax\)`).MatchString(out) {
		t.Errorf("status does not show the relax source:\n%s", out)
	}
	if !regexp.MustCompile(`slots.heavy_per_repo:\s+2 \(relax\)`).MatchString(out) {
		t.Errorf("status does not show the relaxed capacity:\n%s", out)
	}

	cases := []struct{ name, arg, want string }{
		{"unknown key", "thresholds.nonsense=1", "unknown admission policy key"},
		{"missing value", "thresholds.recover_sustain", "expected <key>=<value>"},
		{"bad value", "uncovered.enabled=maybe", "true or false"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := runDotForTest("admit", "relax", "--set", tc.arg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestAdmitRelaxRejectsBadInput(t *testing.T) {
	admitSandbox(t)
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"nothing to relax", []string{"admit", "relax", "--for", "1h"}, "nothing to relax"},
		{"too long", []string{"admit", "relax", "--for", "30h", "--ignore", "all"}, "maximum"},
		{"non-positive", []string{"admit", "relax", "--for", "0s", "--ignore", "all"}, "positive"},
		{"clear with options", []string{"admit", "relax", "--clear", "--ignore", "all"}, "--clear takes no other options"},
		{"unknown rule", []string{"admit", "relax", "--ignore", "slots"}, `"slots"`},
		{"invalid threshold", []string{"admit", "relax", "--set", "thresholds.idle_defer_below=150"},
			"admission.thresholds.idle_defer_below"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := runDotForTest(tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// A config value that is only wrong next to this --set surfaces in the layered
// check, the one place the file is not otherwise named: the person has to edit
// config.yaml, so the refusal points at it.
func TestAdmitRelaxNamesTheConfigFileOnCrossLayerError(t *testing.T) {
	home, _ := admitSandbox(t)
	writeAdmitUserConfig(t, home, "admission:\n  thresholds:\n    idle_recover_at: 5\n")
	_, _, err := runDotForTest("admit", "relax", "--for", "1h", "--set", "thresholds.idle_defer_below=10")
	if err == nil {
		t.Fatal("relax accepted a layer that breaks the idle ordering")
	}
	if !strings.Contains(err.Error(), config.StatePath()) {
		t.Errorf("err = %v, want the config file %s named", err, config.StatePath())
	}
	if !strings.Contains(err.Error(), "admission.thresholds.idle_recover_at") {
		t.Errorf("err = %v, want the offending key named", err)
	}
}

// Status reports an active relax with its expiry.
func TestAdmitStatusShowsRelaxExpiry(t *testing.T) {
	admitSandbox(t)
	stubAdmitMonitor(t, healthyAdmitSnapshot())
	if _, _, err := runDotForTest("admit", "relax", "--for", "10m", "--set", "thresholds.recover_sustain=1m"); err != nil {
		t.Fatalf("relax = %v", err)
	}
	out, _, err := runDotForTest("admit", "status")
	if err != nil {
		t.Fatalf("status = %v", err)
	}
	if !regexp.MustCompile(`Relax override:\s+active until`).MatchString(out) {
		t.Errorf("status does not show the relax expiry:\n%s", out)
	}
}

// AC4: a bypassed slot still takes a lease, in the first overflow slot past the
// configured capacity, and status lists it.
func TestAdmitIgnoreSlotTakesOverflowLease(t *testing.T) {
	home, workdir := admitSandbox(t)
	stubAdmitMonitor(t, healthyAdmitSnapshot())
	stubAdmitScan(t, nil, nil)
	runner := watchdogRunner(false)
	store := admission.NewStore(admission.DefaultStateRoot(home), runner)
	scope, err := admission.ResolveScope(context.Background(), runner, workdir)
	if err != nil {
		t.Fatal(err)
	}
	held, _, err := store.Acquire(context.Background(), scope, admission.ClassHeavy, admission.Lease{
		Owner: "holder@test", PID: 4242, PIDStart: "Mon Sep 28 00:00:00 2026",
	})
	if err != nil || held == nil {
		t.Fatalf("seeding the slot = %v, %v", held, err)
	}
	defer func() { _ = held.Release() }()

	// The configured capacity is one, so a plain run defers on the busy slot.
	_, _, err = runDotForTest("admit", "--wait", "0", "--", "true")
	var exitErr *ExitCodeError
	if !errors.As(err, &exitErr) || exitErr.Code != ExitDeferred {
		t.Fatalf("err = %v, want the busy-slot defer", err)
	}

	_, errOut, err := runDotForTest("admit", "--ignore", "slot", "--json", "--wait", "0", "--", "true")
	if err != nil {
		t.Fatalf("admit under --ignore slot = %v\nstderr=%s", err, errOut)
	}
	if !strings.Contains(errOut, "slot capacity 1 exceeded (--ignore slot): holding overflow slot 1") {
		t.Errorf("stderr = %q, want the overflow-slot note", errOut)
	}
}

// AC4: status lists every held index, so an overflow lease is visible next to
// the index-0 lease it is running beside.
func TestAdmitStatusListsOverflowSlot(t *testing.T) {
	home, workdir := admitSandbox(t)
	stubAdmitMonitor(t, healthyAdmitSnapshot())
	runner := watchdogRunner(false)
	store := admission.NewStore(admission.DefaultStateRoot(home), runner)
	scope, err := admission.ResolveScope(context.Background(), runner, workdir)
	if err != nil {
		t.Fatal(err)
	}
	lease := admission.Lease{Owner: "holder@test", PID: 4242, PIDStart: "Mon Sep 28 00:00:00 2026"}
	first, _, err := store.Acquire(context.Background(), scope, admission.ClassHeavy, lease)
	if err != nil || first == nil {
		t.Fatalf("seeding index 0 = %v, %v", first, err)
	}
	defer func() { _ = first.Release() }()
	overflow, _, err := store.AcquireWithOptions(context.Background(), scope, admission.ClassHeavy, lease,
		admission.AcquireOptions{Capacity: 1, Overflow: true})
	if err != nil || overflow == nil {
		t.Fatalf("seeding the overflow slot = %v, %v", overflow, err)
	}
	defer func() { _ = overflow.Release() }()

	out, _, err := runDotForTest("admit", "status")
	if err != nil {
		t.Fatalf("status = %v", err)
	}
	if !strings.Contains(out, "slot 1") {
		t.Errorf("status does not list the overflow slot:\n%s", out)
	}
}

// dry-run must not write state, including creating the state root. The relax
// command saved the override regardless of the inherited flag.
func TestAdmitRelaxDryRunWritesNothing(t *testing.T) {
	home, _ := admitSandbox(t)
	out, _, err := runDotForTest("--dry-run", "admit", "relax", "--for", "10m", "--ignore", "pressure")
	if err != nil {
		t.Fatalf("dry-run relax = %v", err)
	}
	if !strings.Contains(out, "dry-run: would set") || !strings.Contains(out, "--ignore pressure") {
		t.Errorf("dry-run output = %q, want the override it would write", out)
	}
	root := admission.DefaultStateRoot(home)
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("dry-run created the state root %s: %v", root, err)
	}

	// A dry-run clear leaves the override a real one wrote alone.
	if _, _, err := runDotForTest("admit", "relax", "--for", "10m", "--ignore", "pressure"); err != nil {
		t.Fatalf("relax = %v", err)
	}
	path := filepath.Join(admission.DefaultStateRoot(home), admission.RelaxFileName)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("relax did not write the override: %v", err)
	}
	out, _, err = runDotForTest("--dry-run", "admit", "relax", "--clear")
	if err != nil {
		t.Fatalf("dry-run clear = %v", err)
	}
	if !strings.Contains(out, "dry-run: would clear") {
		t.Errorf("dry-run clear output = %q, want what it would clear", out)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("dry-run clear removed the override: %v", err)
	}
}

// A wait of up to --wait is long enough for an override to expire, so the limits
// are resolved again before the post-wait gate: a relax that ended while the run
// waited for a slot must not still bypass at launch.
func TestAdmitRelaxExpiringDuringWaitRegatesOnFreshLimits(t *testing.T) {
	home, workdir := admitSandbox(t)
	snap := healthyAdmitSnapshot()
	snap.MemoryLevel = admission.MemoryCritical
	stubAdmitMonitor(t, snap)
	stubAdmitScan(t, nil, nil)
	runner := watchdogRunner(false)
	store := admission.NewStore(admission.DefaultStateRoot(home), runner)
	scope, err := admission.ResolveScope(context.Background(), runner, workdir)
	if err != nil {
		t.Fatal(err)
	}
	held, _, err := store.Acquire(context.Background(), scope, admission.ClassHeavy, admission.Lease{
		Owner: "holder@test", PID: 4242, PIDStart: "Mon Sep 28 00:00:00 2026",
	})
	if err != nil || held == nil {
		t.Fatalf("seeding the slot = %v, %v", held, err)
	}
	// The busy slot outlives the override: the run waits past the window, then
	// finds the slot free and must gate again on the limits that are now in force.
	go func() {
		time.Sleep(3 * time.Second)
		_ = held.Release()
	}()
	if _, _, err := runDotForTest("admit", "relax", "--for", "1s", "--ignore", "pressure"); err != nil {
		t.Fatalf("relax = %v", err)
	}

	out, _, err := runDotForTest("admit", "--json", "--wait", "15s", "--", "true")
	var exitErr *ExitCodeError
	if !errors.As(err, &exitErr) || exitErr.Code != ExitDeferred {
		t.Fatalf("err = %v, want the post-wait pressure defer", err)
	}
	if !strings.Contains(out, "memory pressure critical") {
		t.Errorf("out = %q, want the defer reason the current limits produce", out)
	}
}

// status --json says why its verdict came from the defaults, and shows the
// limits in force. A plain host carries neither key, so its record is unchanged.
func TestAdmitStatusJSONCarriesErrorAndLimits(t *testing.T) {
	home, _ := admitSandbox(t)
	stubAdmitMonitor(t, healthyAdmitSnapshot())

	out, _, err := runDotForTest("admit", "status", "--json")
	if err != nil {
		t.Fatalf("status = %v", err)
	}
	if strings.Contains(out, `"limits"`) || strings.Contains(out, `"resolve_error"`) || strings.Contains(out, `"relax_until"`) {
		t.Errorf("a default host's bundle gained limit keys:\n%s", out)
	}

	if _, _, err := runDotForTest("admit", "relax", "--for", "10m", "--set", "thresholds.recover_sustain=1m"); err != nil {
		t.Fatalf("relax = %v", err)
	}
	out, _, err = runDotForTest("admit", "status", "--json")
	if err != nil {
		t.Fatalf("status with a relax = %v", err)
	}
	if !strings.Contains(out, `"relax_until"`) || !strings.Contains(out, `"source": "relax"`) {
		t.Errorf("bundle does not carry the active relax:\n%s", out)
	}
	if !strings.Contains(out, `"key": "thresholds.recover_sustain"`) {
		t.Errorf("bundle does not list the effective keys:\n%s", out)
	}
	if _, _, err := runDotForTest("admit", "relax", "--clear"); err != nil {
		t.Fatalf("relax clear = %v", err)
	}

	// A config that blocks every gate: the record must not read as a healthy
	// admit, and the limits it does show are the defaults it fell back to.
	writeAdmitUserConfig(t, home, "admission:\n  slots:\n    heavy_per_repo: 99\n")
	out, _, err = runDotForTest("admit", "status", "--json")
	if err != nil {
		t.Fatalf("status with a blocked config = %v", err)
	}
	if !strings.Contains(out, `"resolve_error"`) || !strings.Contains(out, "admission.slots.heavy_per_repo") {
		t.Errorf("bundle does not carry the resolve error:\n%s", out)
	}
	if !strings.Contains(out, `"limits"`) || !strings.Contains(out, `"source": "default"`) {
		t.Errorf("bundle does not show the limits it fell back to:\n%s", out)
	}
}

// stubAdmitLiveClock pins the pressure evidence but keeps the real clock, for a
// test whose subject is a window elapsing while the gate retries.
func stubAdmitLiveClock(t *testing.T, snap admission.PressureSnapshot) {
	t.Helper()
	old := admitNewMonitor
	admitNewMonitor = func(*exec.Runner, string) *admission.Monitor {
		return &admission.Monitor{
			GOOS: "darwin",
			SnapshotFunc: func(context.Context, *admission.Monitor) admission.PressureSnapshot {
				return snap
			},
		}
	}
	t.Cleanup(func() { admitNewMonitor = old })
}

// The recovery countdown can run for the whole --wait, so the gate resolves the
// limits again before every sample. An override that expires during the
// countdown must not close the episode on its relaxed recover_sustain: the
// evaluation persists its verdict, so the default window would never run.
func TestAdmitRelaxExpiringDuringRecoveryDoesNotCloseTheEpisode(t *testing.T) {
	home, _ := admitSandbox(t)
	stubAdmitLiveClock(t, healthyAdmitSnapshot())
	runner := watchdogRunner(false)
	store := admission.NewStore(admission.DefaultStateRoot(home), runner)
	// A defer episode one second old: under the relaxed five-second sustain it
	// closes after four more seconds of normal telemetry, under the default five
	// minutes it does not.
	start := time.Now()
	seeded := admission.History{
		DeferActive:  true,
		DeferSince:   start.Add(-time.Minute),
		RecoverSince: start.Add(-time.Second),
		LastSampleAt: start.Add(-time.Second),
	}
	if err := admission.SaveHistory(store.HistoryPath(), seeded); err != nil {
		t.Fatalf("seeding the history: %v", err)
	}
	if _, _, err := runDotForTest("admit", "relax", "--for", "2s", "--set", "thresholds.recover_sustain=5s"); err != nil {
		t.Fatalf("relax = %v", err)
	}

	out, _, err := runDotForTest("admit", "--json", "--wait", "10s", "--", "true")
	var exitErr *ExitCodeError
	if !errors.As(err, &exitErr) || exitErr.Code != ExitDeferred {
		t.Fatalf("err = %v (out %s), want the recovery defer: the override expired, so the default sustain applies", err, out)
	}
	after, err := admission.LoadHistory(store.HistoryPath())
	if err != nil {
		t.Fatalf("reading the history: %v", err)
	}
	if !after.DeferActive {
		t.Error("the episode closed on the relaxed sustain the override no longer provided")
	}
}

// A slot bypass takes an overflow index even when a normal index is free: the
// stderr notice, the JSON "ignored" set and the resourceguard adapter all key
// on the index, so a bypass that quietly took index 0 would report nothing
// while two heavyweight jobs ran.
func TestAdmitIgnoreSlotLandsInAnOverflowIndex(t *testing.T) {
	home, workdir := admitSandbox(t)
	stubAdmitMonitor(t, healthyAdmitSnapshot())
	runner := watchdogRunner(false)
	store := admission.NewStore(admission.DefaultStateRoot(home), runner)
	scope, err := admission.ResolveScope(context.Background(), runner, workdir)
	if err != nil {
		t.Fatal(err)
	}
	// Capacity 2 fills indexes 0 and 1; releasing index 0 strands the lease at 1
	// with a free normal index, which is what a lowered capacity or an expired
	// relax override leaves behind.
	lease := admission.Lease{Owner: "holder@test", PID: 4242, PIDStart: "Mon Sep 28 00:00:00 2026"}
	first, _, err := store.AcquireWithOptions(context.Background(), scope, admission.ClassHeavy, lease, admission.AcquireOptions{Capacity: 2})
	if err != nil || first == nil {
		t.Fatalf("seeding index 0 = %v, %v", first, err)
	}
	stranded, _, err := store.AcquireWithOptions(context.Background(), scope, admission.ClassHeavy, lease, admission.AcquireOptions{Capacity: 2})
	if err != nil || stranded == nil {
		t.Fatalf("seeding index 1 = %v, %v", stranded, err)
	}
	defer func() { _ = stranded.Release() }()
	if err := first.Release(); err != nil {
		t.Fatalf("releasing index 0: %v", err)
	}

	_, errOut, err := runDotForTest("admit", "--ignore", "slot", "--json", "--wait", "0", "--", "true")
	if err != nil {
		t.Fatalf("admit under --ignore slot = %v\nstderr=%s", err, errOut)
	}
	if !strings.Contains(errOut, "slot capacity 1 exceeded (--ignore slot): holding overflow slot") {
		t.Errorf("stderr = %q, want the overflow-slot note", errOut)
	}
	if !strings.Contains(errOut, `"ignored"`) || !strings.Contains(errOut, `"slot"`) {
		t.Errorf("stderr = %q, want the bypassed rule in the JSON record", errOut)
	}
}
