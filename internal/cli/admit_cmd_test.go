package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/admission"
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
	admitFindUncovered = func(context.Context, string, bool, []int) ([]string, error) { return jobs, err }
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

// AC4: a `dot ai run` lease shows in `dot admit status` with its purpose.
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
	admitFindUncovered = func(_ context.Context, _ string, _ bool, leased []int) ([]string, error) {
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
	admitGate = func(context.Context, *admission.Store, *admission.Monitor, admission.Thresholds) (admission.Decision, error) {
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
