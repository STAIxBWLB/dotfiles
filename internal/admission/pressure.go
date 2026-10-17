package admission

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// Threshold defaults mirror the workspace resource policy
// (work/_meta/rules/development-resource-policy.md): defer new heavy work on
// memory pressure warning/critical, verifiable thermal pressure, a fresh
// WindowServer watchdog termination, or CPU idle below 15% / load1 at the
// logical CPU count sustained 60s; resume one job only after five continuous
// minutes of normal memory with idle at or above 30% and load1 below 70% of
// the CPU count.
const (
	DefaultIdleDeferBelow  = 15.0 // percent
	DefaultIdleRecoverAt   = 30.0 // percent
	DefaultLoadDeferFrac   = 1.0  // load1 >= this * ncpu defers
	DefaultLoadRecoverFrac = 0.7  // load1 < this * ncpu recovers
	DefaultDeferSustain    = time.Minute
	DefaultRecoverSustain  = 5 * time.Minute
	DefaultMaxSampleGap    = 45 * time.Second

	// Linux memory levels derive from MemAvailable percent; the workspace
	// policy is written against macOS pressure levels, so these two cutoffs
	// are this package's chosen Linux analogs.
	DefaultMemAvailWarnPct     = 10.0
	DefaultMemAvailCriticalPct = 5.0
)

// Memory pressure levels, shared by the darwin sysctl mapping and the linux
// MemAvailable mapping.
const (
	MemoryNormal   = "normal"
	MemoryWarn     = "warn"
	MemoryCritical = "critical"
)

// Thresholds is the resolved knob set EvaluatePressure decides against.
type Thresholds struct {
	IdleDeferBelow      float64
	IdleRecoverAt       float64
	LoadDeferFrac       float64
	LoadRecoverFrac     float64
	DeferSustain        time.Duration
	RecoverSustain      time.Duration
	MaxSampleGap        time.Duration // largest supported interval between completed samples; <= 0 uses the default
	WSGrace             time.Duration
	MemAvailWarnPct     float64
	MemAvailCriticalPct float64
}

// DefaultThresholds returns the policy thresholds above.
func DefaultThresholds() Thresholds {
	return Thresholds{
		IdleDeferBelow:      DefaultIdleDeferBelow,
		IdleRecoverAt:       DefaultIdleRecoverAt,
		LoadDeferFrac:       DefaultLoadDeferFrac,
		LoadRecoverFrac:     DefaultLoadRecoverFrac,
		DeferSustain:        DefaultDeferSustain,
		RecoverSustain:      DefaultRecoverSustain,
		MaxSampleGap:        DefaultMaxSampleGap,
		WSGrace:             DefaultWSGrace,
		MemAvailWarnPct:     DefaultMemAvailWarnPct,
		MemAvailCriticalPct: DefaultMemAvailCriticalPct,
	}
}

// PressureSnapshot is one probe of host pressure. Every probe carries an
// availability flag: a probe that failed or is missing must defer admission
// (the no-false-healthy rule), which only an explicit flag can express.
// Probes that do not exist on a platform (thermal, CPU idle, and the
// WindowServer scan on linux) are simply skipped by EvaluatePressure.
type PressureSnapshot struct {
	Platform string `json:"platform"` // runtime GOOS the probe ran on

	MemoryLevel     string  `json:"memory_level"`     // MemoryNormal/Warn/Critical when MemoryAvailable
	MemoryAvailable bool    `json:"memory_available"` // the memory probe succeeded
	MemoryFreePct   float64 `json:"memory_free_pct"`  // informational; -1 when unknown

	ThermalState     int  `json:"thermal_state"`     // NSProcessInfo.thermalState: 0 nominal .. 3 critical; -1 when unknown
	ThermalAvailable bool `json:"thermal_available"` // the thermal probe succeeded

	Load1         float64 `json:"load1"`
	NumCPU        int     `json:"num_cpu"`
	LoadAvailable bool    `json:"load_available"` // load1 AND a positive CPU count were read

	IdlePct       float64 `json:"idle_pct"`
	IdleAvailable bool    `json:"idle_available"` // the CPU idle probe succeeded

	WSEvent  time.Time `json:"ws_event"`   // last WindowServer watchdog termination; zero = none
	WSScanOK bool      `json:"ws_scan_ok"` // the DiagnosticReports scan completed

	BootTime      time.Time `json:"boot_time"`
	BootAvailable bool      `json:"boot_available"` // boot time known; only suppresses pre-boot evidence

	VMActivityAvailable bool   `json:"vm_activity_available,omitempty"`
	SwapInBytes         uint64 `json:"swap_in_bytes,omitempty"`
	SwapInAvailable     bool   `json:"swap_in_available,omitempty"`
	SwapOutBytes        uint64 `json:"swap_out_bytes,omitempty"`
	SwapOutAvailable    bool   `json:"swap_out_available,omitempty"`
}

// History is the cross-invocation pressure state persisted between gate
// evaluations: which defer episode is active, when the current CPU/load
// excursion began (the 60-second sustain), and when the current all-normal
// streak began (the 5-minute recovery window).
type History struct {
	DeferActive          bool      `json:"defer_active,omitempty"`
	DeferSince           time.Time `json:"defer_since,omitempty"`
	OverSince            time.Time `json:"over_since,omitempty"`
	RecoverSince         time.Time `json:"recover_since,omitempty"`
	LastSampleAt         time.Time `json:"last_sample_at,omitempty"`
	PrebuiltSwapOutBytes uint64    `json:"prebuilt_swap_out_bytes,omitempty"`
	PrebuiltSwapInBytes  uint64    `json:"prebuilt_swap_in_bytes,omitempty"`
	PrebuiltSwapAt       time.Time `json:"prebuilt_swap_at,omitempty"`
}

// MarshalJSON omits newly-added sample metadata when it has not been set,
// while preserving the legacy zero-time fields in the existing history
// shape. This keeps empty-history diagnostic goldens stable.
func (h History) MarshalJSON() ([]byte, error) {
	type historyAlias History
	var lastSampleAt, prebuiltSwapAt *time.Time
	if !h.LastSampleAt.IsZero() {
		lastSampleAt = &h.LastSampleAt
	}
	if !h.PrebuiltSwapAt.IsZero() {
		prebuiltSwapAt = &h.PrebuiltSwapAt
	}
	return json.Marshal(struct {
		historyAlias
		LastSampleAt   *time.Time `json:"last_sample_at,omitempty"`
		PrebuiltSwapAt *time.Time `json:"prebuilt_swap_at,omitempty"`
	}{historyAlias: historyAlias(h), LastSampleAt: lastSampleAt, PrebuiltSwapAt: prebuiltSwapAt})
}

// Decision is the gate verdict plus the history the caller must persist.
// RetryAfter is advisory: when the defer is worth rechecking.
type Decision struct {
	Admit      bool
	Reasons    []string
	RetryAfter time.Duration
	// ProfileRetry marks a warning-tolerant profile window defer that is safe
	// to resample. Hard pressure and telemetry blockers never set it.
	ProfileRetry bool
	Next         History
}

// EvaluatePressure decides admission from one snapshot. Pure: same inputs,
// same decision. The hysteresis is full — once any reason opens a defer
// episode, recovery requires the policy's five continuous minutes of normal
// memory, idle >= 30%, and load1 < 70% of the CPU count before Admit returns
// true again, and a telemetry gap blocks recovery the same way it blocks
// admission.
func EvaluatePressure(snap PressureSnapshot, th Thresholds, hist History, now time.Time) Decision {
	next := PrepareHistorySample(hist, th, snap, now)
	next.LastSampleAt = now
	var reasons []string
	retry := 30 * time.Second
	bump := func(d time.Duration) {
		if d > retry {
			retry = d
		}
	}

	// Required telemetry first: a failed probe is a defer reason of its own,
	// never an implicit pass.
	probes := requiredProbes(snap)
	telemetryUnavailable := false
	for _, p := range probes {
		if !p.ok {
			telemetryUnavailable = true
			reasons = append(reasons, "telemetry unavailable: "+p.name)
		}
	}
	if snap.MemoryAvailable {
		switch snap.MemoryLevel {
		case MemoryWarn:
			reasons = append(reasons, "memory pressure warning")
		case MemoryCritical:
			reasons = append(reasons, "memory pressure critical")
		}
	}
	if thermalPressure(snap) {
		reasons = append(reasons, thermalReason(snap))
	}
	if windowServerApplies(snap.Platform) && snap.WSScanOK && !snap.WSEvent.IsZero() {
		// Pre-boot evidence is stale by construction: a WindowServer event
		// from before the current boot cannot describe this session's load.
		preBoot := snap.BootAvailable && snap.WSEvent.Before(snap.BootTime)
		if !preBoot {
			if d := snap.WSEvent.Add(th.WSGrace).Sub(now); d > 0 {
				reasons = append(reasons, "recent WindowServer watchdog termination at "+snap.WSEvent.Format(time.RFC3339))
				bump(d)
			}
		}
	}

	// CPU/load defers only after the excursion sustains for DeferSustain;
	// a brief spike is recorded but admitted.
	cpuOver, cpuDetail := cpuExcursion(snap, th)
	if telemetryUnavailable {
		cpuOver = false
	}
	if cpuOver {
		if next.OverSince.IsZero() {
			next.OverSince = now
		}
		if now.Sub(next.OverSince) >= th.DeferSustain {
			reasons = append(reasons, "cpu saturation sustained "+th.DeferSustain.String()+": "+cpuDetail)
		}
	} else {
		next.OverSince = time.Time{}
	}

	if len(reasons) > 0 {
		if !next.DeferActive {
			next.DeferSince = now
		}
		next.DeferActive = true
		next.RecoverSince = time.Time{}
		return Decision{Admit: false, Reasons: reasons, RetryAfter: boundedRetryAfter(th, retry), Next: next}
	}
	if !next.DeferActive {
		return Decision{Admit: true, Next: next}
	}

	// Inside an episode, admission stays closed until the recovery window
	// completes. The window restarts whenever any condition breaks.
	if blockers := recoveryBlockers(snap, th); len(blockers) > 0 {
		next.RecoverSince = time.Time{}
		return Decision{
			Admit:      false,
			Reasons:    []string{"recovery conditions not met: " + strings.Join(blockers, "; ")},
			RetryAfter: boundedRetryAfter(th, 0),
			Next:       next,
		}
	}
	if next.RecoverSince.IsZero() {
		next.RecoverSince = now
	}
	remaining := th.RecoverSustain - now.Sub(next.RecoverSince)
	if remaining <= 0 {
		next.DeferActive = false
		next.DeferSince = time.Time{}
		next.RecoverSince = time.Time{}
		return Decision{Admit: true, Next: next}
	}
	return Decision{
		Admit:      false,
		Reasons:    []string{fmt.Sprintf("recovering from resource pressure (%s of normal telemetry still required)", remaining.Round(time.Second))},
		RetryAfter: boundedRetryAfter(th, remaining),
		Next:       next,
	}
}

func boundedRetryAfter(th Thresholds, suggested time.Duration) time.Duration {
	maxGap := th.MaxSampleGap
	if maxGap <= 0 {
		maxGap = DefaultMaxSampleGap
	}
	cadence := 30 * time.Second
	if halfGap := maxGap / 2; halfGap < cadence {
		cadence = halfGap
	}
	if cadence <= 0 {
		cadence = maxGap
	}
	// A positive custom gap shorter than the snapshot's runtime may never
	// support a continuous streak; in that case each completed probe safely
	// starts a new streak instead of treating separated observations as proof.
	if suggested > 0 && suggested < cadence {
		return suggested
	}
	return cadence
}

// PrepareHistorySample drops time-based streaks that cannot be supported by
// the previous sample. An active defer episode is deliberately preserved.
func PrepareHistorySample(hist History, th Thresholds, snap PressureSnapshot, now time.Time) History {
	maxGap := th.MaxSampleGap
	if maxGap <= 0 {
		maxGap = DefaultMaxSampleGap
	}
	if hist.LastSampleAt.IsZero() || now.Before(hist.LastSampleAt) || now.Sub(hist.LastSampleAt) > maxGap {
		hist.OverSince = time.Time{}
		hist.RecoverSince = time.Time{}
	}
	for _, p := range requiredProbes(snap) {
		if !p.ok {
			hist.OverSince = time.Time{}
			hist.RecoverSince = time.Time{}
			break
		}
	}
	return hist
}

type probeCheck struct {
	name string
	ok   bool
}

// requiredProbes lists the telemetry that must succeed for a Healthy
// verdict on this platform. Memory and load are required everywhere;
// thermal, CPU idle, and the WindowServer scan are macOS-only concerns.
func requiredProbes(snap PressureSnapshot) []probeCheck {
	probes := []probeCheck{
		{name: "memory pressure", ok: snap.MemoryAvailable},
		{name: "load average", ok: snap.LoadAvailable},
	}
	if snap.Platform == "darwin" {
		probes = append(probes,
			probeCheck{name: "thermal pressure", ok: snap.ThermalAvailable},
			probeCheck{name: "cpu idle", ok: snap.IdleAvailable},
			probeCheck{name: "windowserver reports", ok: snap.WSScanOK},
		)
	}
	return probes
}

func thermalApplies(platform string) bool { return platform == "darwin" }

// thermalPressure: serious (2) or critical (3) thermal state, the same
// threshold resourceguard uses.
func thermalPressure(snap PressureSnapshot) bool {
	return thermalApplies(snap.Platform) && snap.ThermalAvailable && snap.ThermalState >= ThermalSerious
}

func thermalReason(snap PressureSnapshot) string {
	return "thermal pressure (state " + ThermalStateName(snap.ThermalState) + ")"
}
func windowServerApplies(platform string) bool { return platform == "darwin" }

// cpuExcursion reports whether the snapshot is past the defer thresholds
// (idle below 15% or load1 at the CPU count), with the evidence detail.
func cpuExcursion(snap PressureSnapshot, th Thresholds) (bool, string) {
	var parts []string
	if snap.LoadAvailable && snap.NumCPU > 0 && snap.Load1 >= th.LoadDeferFrac*float64(snap.NumCPU) {
		parts = append(parts, fmt.Sprintf("load1 %.2f >= %d CPUs", snap.Load1, snap.NumCPU))
	}
	if snap.Platform == "darwin" && snap.IdleAvailable && snap.IdlePct < th.IdleDeferBelow {
		parts = append(parts, fmt.Sprintf("cpu idle %.0f%% < %.0f%%", snap.IdlePct, th.IdleDeferBelow))
	}
	return len(parts) > 0, strings.Join(parts, "; ")
}

// recoveryBlockers lists why the snapshot does not yet satisfy the recovery
// conditions (normal memory, idle >= 30%, load1 < 70% of ncpu, no thermal or
// WindowServer pressure). A telemetry gap is a blocker: recovery from an
// unmeasurable machine is unprovable.
func recoveryBlockers(snap PressureSnapshot, th Thresholds) []string {
	var out []string
	for _, p := range requiredProbes(snap) {
		if !p.ok {
			out = append(out, "telemetry unavailable: "+p.name)
		}
	}
	if snap.MemoryAvailable && snap.MemoryLevel != MemoryNormal {
		out = append(out, "memory pressure "+snap.MemoryLevel)
	}
	if thermalPressure(snap) {
		out = append(out, thermalReason(snap))
	}
	if snap.LoadAvailable && snap.NumCPU > 0 && snap.Load1 >= th.LoadRecoverFrac*float64(snap.NumCPU) {
		out = append(out, fmt.Sprintf("load1 %.2f >= %.0f%% of %d CPUs", snap.Load1, th.LoadRecoverFrac*100, snap.NumCPU))
	}
	if snap.Platform == "darwin" && snap.IdleAvailable && snap.IdlePct < th.IdleRecoverAt {
		out = append(out, fmt.Sprintf("cpu idle %.0f%% < %.0f%%", snap.IdlePct, th.IdleRecoverAt))
	}
	return out
}

// MemoryLevelFromAvailPct maps a Linux MemAvailable percentage onto the
// shared memory levels.
func MemoryLevelFromAvailPct(pct float64, th Thresholds) string {
	switch {
	case pct < th.MemAvailCriticalPct:
		return MemoryCritical
	case pct < th.MemAvailWarnPct:
		return MemoryWarn
	default:
		return MemoryNormal
	}
}

// Monitor probes host resource pressure. GOOS is injected (never a build
// tag) so tests pose as either platform; Now and SnapshotFunc are the seams
// the CLI's golden fixtures substitute for a fixed clock and deterministic
// evidence.
type Monitor struct {
	Runner *exec.Runner
	Home   string
	GOOS   string // empty means runtime.GOOS
	Now    func() time.Time
	// SnapshotFunc, when set, replaces live probing entirely.
	SnapshotFunc func(ctx context.Context, m *Monitor) PressureSnapshot
}

func (m *Monitor) goos() string {
	if m.GOOS != "" {
		return m.GOOS
	}
	return runtime.GOOS
}

// SnapshotPressure gathers one probe. Probe failures land in the snapshot's
// availability flags — they are never silently treated as healthy.
func (m *Monitor) SnapshotPressure(ctx context.Context) PressureSnapshot {
	if m.SnapshotFunc != nil {
		return m.SnapshotFunc(ctx, m)
	}
	if m.goos() == "darwin" {
		return m.snapshotDarwin(ctx)
	}
	return m.snapshotLinux(ctx)
}

func (m *Monitor) snapshotDarwin(ctx context.Context) PressureSnapshot {
	snap := PressureSnapshot{Platform: "darwin", MemoryFreePct: -1, ThermalState: -1}
	if res, err := m.query(ctx, "sysctl", "-n", "kern.memorystatus_vm_pressure_level"); err == nil {
		if level, ok := ParseMemoryPressureLevel(res.Stdout); ok {
			snap.MemoryLevel = level
			snap.MemoryAvailable = true
		}
	}
	if res, err := m.query(ctx, "memory_pressure"); err == nil {
		if pct, ok := ParseMemoryFreePct(res.Stdout); ok {
			snap.MemoryFreePct = pct
		}
	}
	// `pmset -g thermlog` streams and prints nothing until a thermal event,
	// so it never answers within probeTimeout on an idle Mac (#161).
	if res, err := m.query(ctx, "osascript", "-l", "JavaScript", "-e", thermalStateScript); err == nil {
		if state, ok := ParseThermalState(res.Stdout); ok {
			snap.ThermalState = state
			snap.ThermalAvailable = true
		}
	}
	loadRes, loadErr := m.query(ctx, "sysctl", "-n", "vm.loadavg")
	ncpuRes, ncpuErr := m.query(ctx, "sysctl", "-n", "hw.ncpu")
	if loadErr == nil && ncpuErr == nil {
		load1, lok := ParseLoadAvg(loadRes.Stdout)
		ncpu, nok := ParseNCPU(ncpuRes.Stdout)
		if lok && nok {
			snap.Load1 = load1
			snap.NumCPU = ncpu
			snap.LoadAvailable = true
		}
	}
	if res, err := m.query(ctx, "top", "-l", "1", "-n", "0"); err == nil {
		if idle, ok := ParseTopCPUUsage(res.Stdout); ok {
			snap.IdlePct = idle
			snap.IdleAvailable = true
		}
	}
	event, ok, err := ScanWindowServerDirs(WindowServerReportDirs(m.Home), wsScanNewest)
	if err == nil {
		snap.WSEvent = event
		snap.WSScanOK = ok
	}
	if res, err := m.query(ctx, "sysctl", "-n", "kern.boottime"); err == nil {
		if boot, ok := ParseSysctlBoottime(res.Stdout); ok {
			snap.BootTime = boot
			snap.BootAvailable = true
		}
	}
	return snap
}

// probeTimeout bounds one telemetry probe. A hung probe (`top` on a
// saturated host is exactly the incident shape this controller exists for)
// must degrade to "unavailable" and defer, never hang the gate itself.
const probeTimeout = 10 * time.Second

func (m *Monitor) query(ctx context.Context, name string, args ...string) (*exec.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	return m.Runner.RunQuery(ctx, name, args...)
}

// snapshotLinux reads /proc directly: loadavg + CPU count, MemAvailable, and
// boot time. Thermal, CPU idle, and WindowServer are macOS-only concerns, so
// linux leaves those flags false and EvaluatePressure skips them there.
func (m *Monitor) snapshotLinux(ctx context.Context) PressureSnapshot {
	_ = ctx
	snap := PressureSnapshot{Platform: "linux", MemoryFreePct: -1, ThermalState: -1}
	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		if load1, ok := ParseProcLoadavg(string(data)); ok {
			snap.Load1 = load1
			snap.NumCPU = runtime.NumCPU()
			snap.LoadAvailable = snap.NumCPU > 0
		}
	}
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		if pct, ok := ParseProcMemInfo(string(data)); ok {
			snap.MemoryFreePct = pct
			snap.MemoryLevel = MemoryLevelFromAvailPct(pct, DefaultThresholds())
			snap.MemoryAvailable = true
		}
	}
	if data, err := os.ReadFile("/proc/stat"); err == nil {
		if boot, ok := ParseProcStatBtime(string(data)); ok {
			snap.BootTime = boot
			snap.BootAvailable = true
		}
	}
	return snap
}

// ParseMemoryPressureLevel maps `sysctl -n kern.memorystatus_vm_pressure_level`
// output: 1 normal, 2 warning, 4 critical (the values the workspace policy
// names). Anything else is unknown telemetry, which the caller must treat as
// unavailable rather than normal.
func ParseMemoryPressureLevel(out string) (string, bool) {
	switch strings.TrimSpace(out) {
	case "1":
		return MemoryNormal, true
	case "2":
		return MemoryWarn, true
	case "4":
		return MemoryCritical, true
	}
	return "", false
}

var memoryFreeRE = regexp.MustCompile(`(?m)System-wide memory free percentage:\s*([0-9]+)%`)

// ParseMemoryFreePct extracts the free percentage from `memory_pressure`
// output (informational only; the pressure level carries the decision).
func ParseMemoryFreePct(out string) (float64, bool) {
	matches := memoryFreeRE.FindAllStringSubmatch(out, -1)
	if len(matches) == 0 {
		return 0, false
	}
	pct, err := strconv.ParseFloat(matches[len(matches)-1][1], 64)
	return pct, err == nil
}

// Thermal states reported by NSProcessInfo.thermalState.
const (
	ThermalNominal = iota
	ThermalFair
	ThermalSerious
	ThermalCritical
)

// thermalStateScript reads NSProcessInfo.thermalState through JXA; it
// answers immediately, unlike `pmset -g thermlog`.
const thermalStateScript = `ObjC.import("Foundation"); $.NSProcessInfo.processInfo.thermalState`

// ParseThermalState parses the osascript output: a single 0..3 integer.
func ParseThermalState(out string) (int, bool) {
	state, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil || state < ThermalNominal || state > ThermalCritical {
		return 0, false
	}
	return state, true
}

// ThermalStateName names a thermal state for status output and reasons.
func ThermalStateName(state int) string {
	switch state {
	case ThermalNominal:
		return "nominal"
	case ThermalFair:
		return "fair"
	case ThermalSerious:
		return "serious"
	case ThermalCritical:
		return "critical"
	}
	return "unknown"
}

// ParseLoadAvg parses `sysctl -n vm.loadavg` output: "{ 2.01 1.98 2.10 }".
func ParseLoadAvg(out string) (float64, bool) {
	fields := strings.Fields(strings.Trim(out, "{} \n"))
	if len(fields) < 1 {
		return 0, false
	}
	load1, err := strconv.ParseFloat(fields[0], 64)
	return load1, err == nil
}

// ParseNCPU parses `sysctl -n hw.ncpu` output.
func ParseNCPU(out string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

var topCPUUsageRE = regexp.MustCompile(`(?m)CPU usage:\s*[0-9.]+%\s*user,\s*[0-9.]+%\s*sys,\s*([0-9.]+)%\s*idle`)

// ParseTopCPUUsage extracts the idle percentage from `top -l 1 -n 0` output.
// The last "CPU usage" line wins so the parser stays correct if the probe
// ever moves to `top -l 2` (whose first sample is the since-boot average).
func ParseTopCPUUsage(out string) (float64, bool) {
	matches := topCPUUsageRE.FindAllStringSubmatch(out, -1)
	if len(matches) == 0 {
		return 0, false
	}
	idle, err := strconv.ParseFloat(matches[len(matches)-1][1], 64)
	return idle, err == nil
}

// ParseProcLoadavg parses /proc/loadavg: "2.01 1.98 2.10 3/456 7890".
func ParseProcLoadavg(content string) (float64, bool) {
	fields := strings.Fields(content)
	if len(fields) < 1 {
		return 0, false
	}
	load1, err := strconv.ParseFloat(fields[0], 64)
	return load1, err == nil
}

// ParseProcMemInfo computes MemAvailable as a percent of MemTotal.
func ParseProcMemInfo(content string) (float64, bool) {
	var total, available float64
	var haveTotal, haveAvail bool
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		kb, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			total, haveTotal = kb, true
		case "MemAvailable:":
			available, haveAvail = kb, true
		}
	}
	if !haveTotal || !haveAvail || total <= 0 {
		return 0, false
	}
	return available / total * 100, true
}

// ParseSysctlBoottime parses `sysctl -n kern.boottime` output:
// "{ sec = 1758900000, usec = 0 } Sat Sep 26 12:00:00 2026".
func ParseSysctlBoottime(out string) (time.Time, bool) {
	re := regexp.MustCompile(`sec\s*=\s*([0-9]+)`)
	m := re.FindStringSubmatch(out)
	if m == nil {
		return time.Time{}, false
	}
	sec, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(sec, 0), true
}

// ParseProcStatBtime parses the btime line of /proc/stat.
func ParseProcStatBtime(content string) (time.Time, bool) {
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "btime" {
			sec, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				return time.Time{}, false
			}
			return time.Unix(sec, 0), true
		}
	}
	return time.Time{}, false
}
