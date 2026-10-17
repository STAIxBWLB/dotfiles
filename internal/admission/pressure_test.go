package admission

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// --- Probe parsers (fixtures) ---

func TestParseMemoryPressureLevel(t *testing.T) {
	for _, tc := range []struct {
		out  string
		want string
		ok   bool
	}{
		{"1\n", MemoryNormal, true},
		{"2\n", MemoryWarn, true},
		{"4\n", MemoryCritical, true},
		{"3\n", "", false},
		{"", "", false},
	} {
		got, ok := ParseMemoryPressureLevel(tc.out)
		if ok != tc.ok || got != tc.want {
			t.Errorf("ParseMemoryPressureLevel(%q) = %q, %v; want %q, %v", tc.out, got, ok, tc.want, tc.ok)
		}
	}
}

func TestParseMemoryFreePct(t *testing.T) {
	out := "The system has 206158430208 (12584984 pages with 16384 bytes per page)\nSystem-wide memory free percentage: 42%\n"
	pct, ok := ParseMemoryFreePct(out)
	if !ok || pct != 42 {
		t.Errorf("ParseMemoryFreePct = %v, %v; want 42, true", pct, ok)
	}
	if _, ok := ParseMemoryFreePct("no such line\n"); ok {
		t.Error("expected miss on output without the free-percentage line")
	}
}

func TestParseThermalState(t *testing.T) {
	for out, want := range map[string]int{"0\n": 0, "1\n": 1, " 2 ": 2, "3": 3} {
		if got, ok := ParseThermalState(out); !ok || got != want {
			t.Errorf("ParseThermalState(%q) = %d, %v; want %d, true", out, got, ok, want)
		}
	}
	for _, out := range []string{"", "4", "-1", "nominal", "Note: No thermal warning level has been recorded\n"} {
		if _, ok := ParseThermalState(out); ok {
			t.Errorf("ParseThermalState(%q) accepted invalid output", out)
		}
	}
}

func TestParseLoadAvgAndNCPU(t *testing.T) {
	load1, ok := ParseLoadAvg("{ 2.01 1.98 2.10 }\n")
	if !ok || load1 != 2.01 {
		t.Errorf("ParseLoadAvg = %v, %v", load1, ok)
	}
	if _, ok := ParseLoadAvg("{ }\n"); ok {
		t.Error("expected miss on empty loadavg")
	}
	n, ok := ParseNCPU("10\n")
	if !ok || n != 10 {
		t.Errorf("ParseNCPU = %d, %v", n, ok)
	}
	if _, ok := ParseNCPU("0\n"); ok {
		t.Error("zero CPUs is not valid telemetry")
	}
}

func TestParseTopCPUUsage(t *testing.T) {
	out := "Processes: 500 total\nCPU usage: 7.14% user, 14.28% sys, 78.57% idle\nSharedLibs: 300 resident\n"
	idle, ok := ParseTopCPUUsage(out)
	if !ok || idle != 78.57 {
		t.Errorf("ParseTopCPUUsage = %v, %v", idle, ok)
	}
	twoSamples := "CPU usage: 50.0% user, 10.0% sys, 40.0% idle\nCPU usage: 1.0% user, 1.0% sys, 98.0% idle\n"
	idle, ok = ParseTopCPUUsage(twoSamples)
	if !ok || idle != 98.0 {
		t.Errorf("ParseTopCPUUsage two samples = %v, %v; want last sample 98.0", idle, ok)
	}
	if _, ok := ParseTopCPUUsage("no cpu line\n"); ok {
		t.Error("expected miss without a CPU usage line")
	}
}

func TestParseProcFixtures(t *testing.T) {
	load1, ok := ParseProcLoadavg("2.01 1.98 2.10 3/456 7890\n")
	if !ok || load1 != 2.01 {
		t.Errorf("ParseProcLoadavg = %v, %v", load1, ok)
	}
	meminfo := "MemTotal:       16384000 kB\nMemFree:         1000000 kB\nMemAvailable:    8192000 kB\nBuffers:          100000 kB\n"
	pct, ok := ParseProcMemInfo(meminfo)
	if !ok || pct != 50 {
		t.Errorf("ParseProcMemInfo = %v, %v; want 50", pct, ok)
	}
	if _, ok := ParseProcMemInfo("MemTotal:       16384000 kB\n"); ok {
		t.Error("expected miss without MemAvailable")
	}
	stat := "cpu  1 2 3 4 5 6 7 8 9 10\nbtime 1758900000\nprocesses 12345\n"
	boot, ok := ParseProcStatBtime(stat)
	if !ok || boot.Unix() != 1758900000 {
		t.Errorf("ParseProcStatBtime = %v, %v", boot, ok)
	}
	sysctlBoot, ok := ParseSysctlBoottime("{ sec = 1758900000, usec = 0 } Sat Sep 26 12:00:00 2026\n")
	if !ok || sysctlBoot.Unix() != 1758900000 {
		t.Errorf("ParseSysctlBoottime = %v, %v", sysctlBoot, ok)
	}
	if _, ok := ParseSysctlBoottime("garbage\n"); ok {
		t.Error("expected miss on malformed boottime")
	}
}

func TestMemoryLevelFromAvailPct(t *testing.T) {
	th := DefaultThresholds()
	for pct, want := range map[float64]string{
		50:  MemoryNormal,
		10:  MemoryNormal,
		9.9: MemoryWarn,
		5:   MemoryWarn,
		4.9: MemoryCritical,
	} {
		if got := MemoryLevelFromAvailPct(pct, th); got != want {
			t.Errorf("MemoryLevelFromAvailPct(%v) = %q, want %q", pct, got, want)
		}
	}
}

// --- EvaluatePressure ---

var evalT0 = time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

func healthyDarwin() PressureSnapshot {
	return PressureSnapshot{
		Platform:         "darwin",
		MemoryLevel:      MemoryNormal,
		MemoryAvailable:  true,
		MemoryFreePct:    55,
		ThermalAvailable: true,
		Load1:            3.0,
		NumCPU:           10,
		LoadAvailable:    true,
		IdlePct:          85,
		IdleAvailable:    true,
		WSScanOK:         true,
		BootAvailable:    true,
		BootTime:         evalT0.Add(-time.Hour),
	}
}

func evalDeferReasons(d Decision) string {
	return strings.Join(d.Reasons, "; ")
}

func TestEvaluateHealthy(t *testing.T) {
	d := EvaluatePressure(healthyDarwin(), DefaultThresholds(), History{}, evalT0)
	if !d.Admit {
		t.Errorf("healthy snapshot deferred: %s", evalDeferReasons(d))
	}
}

func TestEvaluateMemoryWarnAndCritical(t *testing.T) {
	for _, level := range []string{MemoryWarn, MemoryCritical} {
		snap := healthyDarwin()
		snap.MemoryLevel = level
		d := EvaluatePressure(snap, DefaultThresholds(), History{}, evalT0)
		if d.Admit {
			t.Errorf("memory %s admitted", level)
		}
		if !d.Next.DeferActive {
			t.Errorf("memory %s did not open a defer episode", level)
		}
		if d.Next.DeferSince.IsZero() {
			t.Errorf("memory %s did not record the episode start", level)
		}
	}
}

func TestEvaluateThermalPressure(t *testing.T) {
	snap := healthyDarwin()
	for state, reason := range map[int]string{
		ThermalSerious:  "thermal pressure (state serious)",
		ThermalCritical: "thermal pressure (state critical)",
	} {
		snap.ThermalState = state
		d := EvaluatePressure(snap, DefaultThresholds(), History{}, evalT0)
		if d.Admit || !strings.Contains(evalDeferReasons(d), reason) {
			t.Errorf("thermal state %d = %+v, want defer with %q", state, d, reason)
		}
	}
	// Fair is not pressure: it must not block admission on its own.
	snap.ThermalState = ThermalFair
	if d := EvaluatePressure(snap, DefaultThresholds(), History{}, evalT0); strings.Contains(evalDeferReasons(d), "thermal") {
		t.Errorf("thermal state fair produced a thermal defer: %+v", d)
	}
}

// TestEvaluateMissingTelemetryDefers is the no-false-healthy rule: every
// required probe that failed is its own defer reason.
func TestEvaluateMissingTelemetryDefers(t *testing.T) {
	snap := healthyDarwin()
	snap.MemoryAvailable = false
	snap.ThermalAvailable = false
	d := EvaluatePressure(snap, DefaultThresholds(), History{}, evalT0)
	reasons := evalDeferReasons(d)
	if d.Admit {
		t.Error("missing telemetry admitted")
	}
	if !strings.Contains(reasons, "telemetry unavailable: memory pressure") ||
		!strings.Contains(reasons, "telemetry unavailable: thermal pressure") {
		t.Errorf("reasons = %q, want both unavailable probes named", reasons)
	}
}

func TestEvaluateLoadSustain(t *testing.T) {
	th := DefaultThresholds()
	snap := healthyDarwin()
	snap.Load1 = 12 // >= 10 CPUs
	// First over-threshold sample: admitted, but the excursion clock starts.
	d := EvaluatePressure(snap, th, History{}, evalT0)
	if !d.Admit {
		t.Errorf("first over-threshold sample deferred: %s", evalDeferReasons(d))
	}
	if d.Next.OverSince.IsZero() {
		t.Error("excursion start not recorded")
	}
	d = EvaluatePressure(snap, th, d.Next, evalT0.Add(30*time.Second))
	// Sustained past DeferSustain: deferred.
	d = EvaluatePressure(snap, th, d.Next, evalT0.Add(th.DeferSustain+time.Second))
	if d.Admit || !strings.Contains(evalDeferReasons(d), "sustained") {
		t.Errorf("sustained load = %+v, want defer", d)
	}
	// A normal sample resets the excursion clock.
	d = EvaluatePressure(healthyDarwin(), th, d.Next, evalT0.Add(2*time.Minute))
	if !d.Next.OverSince.IsZero() {
		t.Error("normal sample did not reset the excursion clock")
	}
}

func TestEvaluateIdleSustain(t *testing.T) {
	th := DefaultThresholds()
	snap := healthyDarwin()
	snap.IdlePct = 10
	d := EvaluatePressure(snap, th, History{}, evalT0)
	if !d.Admit {
		t.Errorf("first low-idle sample deferred: %s", evalDeferReasons(d))
	}
	d = EvaluatePressure(snap, th, d.Next, evalT0.Add(30*time.Second))
	d = EvaluatePressure(snap, th, d.Next, evalT0.Add(61*time.Second))
	if d.Admit || !strings.Contains(evalDeferReasons(d), "cpu idle") {
		t.Errorf("sustained low idle = %+v, want defer", d)
	}
}

func TestEvaluateWindowServerGrace(t *testing.T) {
	th := DefaultThresholds()
	snap := healthyDarwin()
	snap.WSEvent = evalT0.Add(-10 * time.Minute) // inside the 30m grace
	d := EvaluatePressure(snap, th, History{}, evalT0)
	if d.Admit || !strings.Contains(evalDeferReasons(d), "WindowServer") {
		t.Errorf("recent WindowServer event = %+v, want defer", d)
	}
	if d.RetryAfter != th.MaxSampleGap/2 {
		t.Errorf("retry after = %v, want the sample-safe cadence %v", d.RetryAfter, th.MaxSampleGap/2)
	}
	// Outside the grace window: admitted.
	snap.WSEvent = evalT0.Add(-time.Hour)
	d = EvaluatePressure(snap, th, History{}, evalT0)
	if !d.Admit {
		t.Errorf("old WindowServer event deferred: %s", evalDeferReasons(d))
	}
	// Pre-boot evidence is suppressed.
	snap.WSEvent = evalT0.Add(-10 * time.Minute)
	snap.BootTime = evalT0.Add(-5 * time.Minute) // booted after the event
	d = EvaluatePressure(snap, th, History{}, evalT0)
	if !d.Admit {
		t.Errorf("pre-boot WindowServer event deferred: %s", evalDeferReasons(d))
	}
}

// TestEvaluateFullHysteresis walks a whole episode: open on memory warn,
// recover only after five continuous minutes of normality, restart the
// window when a condition breaks.
func TestEvaluateFullHysteresis(t *testing.T) {
	th := DefaultThresholds()
	snap := healthyDarwin()
	snap.MemoryLevel = MemoryWarn
	d := EvaluatePressure(snap, th, History{}, evalT0)
	if d.Admit || !d.Next.DeferActive {
		t.Fatal("episode did not open")
	}

	normal := healthyDarwin()
	// Pressure cleared, but the recovery window has just started.
	d = EvaluatePressure(normal, th, d.Next, evalT0.Add(time.Minute))
	if d.Admit || !strings.Contains(evalDeferReasons(d), "recovering") {
		t.Errorf("early recovery = %+v, want defer with recovering reason", d)
	}
	if d.Next.RecoverSince.IsZero() {
		t.Error("recovery streak start not recorded")
	}
	// Sample every 30 seconds through minute four so the recovery streak is
	// supported by observations within the maximum sample gap.
	for at := evalT0.Add(90 * time.Second); !at.After(evalT0.Add(4 * time.Minute)); at = at.Add(30 * time.Second) {
		d = EvaluatePressure(normal, th, d.Next, at)
	}
	if d.Admit {
		t.Error("admitted before the recovery window completed")
	}
	if !d.Next.RecoverSince.Equal(evalT0.Add(time.Minute)) {
		t.Errorf("recovery start moved across fresh samples: got %v, want %v", d.Next.RecoverSince, evalT0.Add(time.Minute))
	}
	// A condition breaks at minute four: the window restarts.
	blocked := healthyDarwin()
	blocked.IdlePct = 20 // above the 15% defer floor, below the 30% recovery bar
	d = EvaluatePressure(blocked, th, d.Next, evalT0.Add(4*time.Minute+30*time.Second))
	if d.Admit || !strings.Contains(evalDeferReasons(d), "recovery conditions not met") {
		t.Errorf("broken recovery = %+v, want defer with blockers", d)
	}
	if !d.Next.RecoverSince.IsZero() {
		t.Error("recovery streak should restart when a condition breaks")
	}
	// Five continuous minutes of normality after that: admitted, episode closed.
	d = EvaluatePressure(normal, th, d.Next, evalT0.Add(5*time.Minute))
	for at := evalT0.Add(5*time.Minute + 30*time.Second); !at.After(evalT0.Add(10 * time.Minute)); at = at.Add(30 * time.Second) {
		d = EvaluatePressure(normal, th, d.Next, at)
	}
	if !d.Admit {
		t.Errorf("completed recovery still deferred: %s", evalDeferReasons(d))
	}
	if d.Next.DeferActive {
		t.Error("episode did not close on recovery")
	}
}

func TestRecoveryRetryCadencePreservesContinuity(t *testing.T) {
	th := DefaultThresholds()
	deferred := History{DeferActive: true, DeferSince: evalT0}
	blocked := healthyDarwin()
	blocked.IdlePct = 20
	d := EvaluatePressure(blocked, th, deferred, evalT0)
	if d.RetryAfter <= 0 || d.RetryAfter > th.MaxSampleGap {
		t.Fatalf("blocked recovery retry after %v exceeds sample gap %v", d.RetryAfter, th.MaxSampleGap)
	}
	now := evalT0.Add(d.RetryAfter)
	d = EvaluatePressure(healthyDarwin(), th, d.Next, now)
	for attempts := 0; !d.Admit && attempts < 32; attempts++ {
		if d.RetryAfter <= 0 || d.RetryAfter > th.MaxSampleGap {
			t.Fatalf("retry after %v exceeds sample gap %v", d.RetryAfter, th.MaxSampleGap)
		}
		now = now.Add(d.RetryAfter)
		d = EvaluatePressure(healthyDarwin(), th, d.Next, now)
	}
	if !d.Admit {
		t.Fatalf("recovery did not complete after retry-driven samples; last=%+v", d)
	}
	if now.Sub(evalT0) < th.RecoverSustain {
		t.Fatalf("recovered after %v, before required %v", now.Sub(evalT0), th.RecoverSustain)
	}
}

func TestEvaluateSampleGapBoundary(t *testing.T) {
	th := DefaultThresholds()
	over := healthyDarwin()
	over.Load1 = 12
	first := EvaluatePressure(over, th, History{}, evalT0)
	atBoundary := EvaluatePressure(over, th, first.Next, evalT0.Add(th.MaxSampleGap))
	if !atBoundary.Next.OverSince.Equal(evalT0) {
		t.Fatalf("sample at gap boundary restarted CPU streak: %+v", atBoundary.Next)
	}
	afterBoundaryAt := evalT0.Add(th.MaxSampleGap + time.Nanosecond)
	afterBoundary := EvaluatePressure(over, th, first.Next, afterBoundaryAt)
	if !afterBoundary.Next.OverSince.Equal(afterBoundaryAt) {
		t.Fatalf("sample after gap boundary retained CPU streak: %+v", afterBoundary.Next)
	}
}

func TestEvaluateSampleContinuity(t *testing.T) {
	th := DefaultThresholds()
	over := healthyDarwin()
	over.Load1 = 12
	first := EvaluatePressure(over, th, History{}, evalT0)
	far := EvaluatePressure(over, th, first.Next, evalT0.Add(2*time.Minute))
	if !far.Admit || far.Next.OverSince.Equal(first.Next.OverSince) {
		t.Fatalf("separated CPU samples claimed continuity: %+v", far)
	}
	if far.Next.DeferActive {
		t.Fatal("separated CPU samples opened a defer episode")
	}

	active := History{DeferActive: true, DeferSince: evalT0, RecoverSince: evalT0, LastSampleAt: evalT0}
	staleRecovery := EvaluatePressure(healthyDarwin(), th, active, evalT0.Add(2*time.Minute))
	if staleRecovery.Admit || !staleRecovery.Next.RecoverSince.Equal(evalT0.Add(2*time.Minute)) {
		t.Fatalf("stale recovery sample did not restart its window: %+v", staleRecovery)
	}
	if !staleRecovery.Next.DeferActive {
		t.Fatal("sample gap cleared the active defer episode")
	}
	for at := evalT0.Add(2*time.Minute + 30*time.Second); !at.After(evalT0.Add(7 * time.Minute)); at = at.Add(30 * time.Second) {
		staleRecovery = EvaluatePressure(healthyDarwin(), th, staleRecovery.Next, at)
	}
	if !staleRecovery.Admit || staleRecovery.Next.DeferActive {
		t.Fatalf("fresh regular samples did not complete recovery: %+v", staleRecovery)
	}
}

func TestEvaluateContinuityRollbackAndUnavailable(t *testing.T) {
	th := DefaultThresholds()
	over := healthyDarwin()
	over.Load1 = 12
	hist := History{OverSince: evalT0.Add(-time.Minute), LastSampleAt: evalT0}
	rollback := EvaluatePressure(over, th, hist, evalT0.Add(-time.Second))
	if !rollback.Admit || !rollback.Next.OverSince.Equal(evalT0.Add(-time.Second)) {
		t.Fatalf("clock rollback retained old CPU streak: %+v", rollback)
	}
	unavailable := over
	unavailable.IdleAvailable = false
	gap := EvaluatePressure(unavailable, th, History{OverSince: evalT0.Add(-time.Minute), RecoverSince: evalT0.Add(-time.Minute), LastSampleAt: evalT0}, evalT0.Add(10*time.Second))
	if !gap.Next.OverSince.IsZero() || !gap.Next.RecoverSince.IsZero() {
		t.Fatalf("unavailable probe retained a streak: %+v", gap.Next)
	}
}

func TestEvaluateRecoveryBlockedByTelemetryGap(t *testing.T) {
	th := DefaultThresholds()
	hist := History{DeferActive: true, DeferSince: evalT0.Add(-time.Hour)}
	snap := healthyDarwin()
	snap.IdleAvailable = false
	d := EvaluatePressure(snap, th, hist, evalT0)
	if d.Admit || !strings.Contains(evalDeferReasons(d), "telemetry unavailable") {
		t.Errorf("telemetry gap during recovery = %+v, want defer", d)
	}
}

// TestEvaluateLinuxApplicability: thermal, CPU idle, and the WindowServer
// scan do not exist on linux; their absence must not defer there.
func TestEvaluateLinuxApplicability(t *testing.T) {
	snap := PressureSnapshot{
		Platform:        "linux",
		MemoryLevel:     MemoryNormal,
		MemoryAvailable: true,
		Load1:           1.0,
		NumCPU:          8,
		LoadAvailable:   true,
	}
	d := EvaluatePressure(snap, DefaultThresholds(), History{}, evalT0)
	if !d.Admit {
		t.Errorf("linux snapshot deferred on macOS-only probes: %s", evalDeferReasons(d))
	}
	snap.Load1 = 9
	th := DefaultThresholds()
	hist := History{}
	for at := evalT0.Add(-90 * time.Second); !at.After(evalT0); at = at.Add(30 * time.Second) {
		d = EvaluatePressure(snap, th, hist, at)
		hist = d.Next
	}
	if d.Admit {
		t.Error("sustained linux load admitted")
	}
}

// TestSnapshotDarwinThermalProbe drives the real probe wiring with a stub
// osascript on PATH: the argument list and parsing are what #161 broke.
func TestSnapshotDarwinThermalProbe(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		wantState    int
		wantOK       bool
	}{
		{"serious", "echo 2", ThermalSerious, true},
		{"garbled", "echo nominal", -1, false},
		// A failed probe must stay unavailable even if it printed a digit.
		{"failed", "echo 0; exit 1", -1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := t.TempDir()
			// Builtins only: PATH holds just this stub.
			script := "#!/bin/sh\n[ \"$#\" -eq 4 ] || exit 2\n[ \"$1 $2 $3\" = \"-l JavaScript -e\" ] || exit 2\n" +
				"case \"$4\" in *'NSProcessInfo.processInfo.thermalState'*) ;; *) exit 2;; esac\n" + tc.output + "\n"
			if err := os.WriteFile(filepath.Join(bin, "osascript"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin)
			m := &Monitor{Runner: exec.NewProbeRunner(), GOOS: "darwin", Home: t.TempDir()}
			snap := m.SnapshotPressure(context.Background())
			if snap.ThermalAvailable != tc.wantOK || snap.ThermalState != tc.wantState {
				t.Errorf("thermal = %d, available %v; want %d, %v", snap.ThermalState, snap.ThermalAvailable, tc.wantState, tc.wantOK)
			}
		})
	}
}

func TestThermalStateName(t *testing.T) {
	for state, want := range map[int]string{-1: "unknown", 0: "nominal", 1: "fair", 2: "serious", 3: "critical", 4: "unknown"} {
		if got := ThermalStateName(state); got != want {
			t.Errorf("ThermalStateName(%d) = %q, want %q", state, got, want)
		}
	}
}
