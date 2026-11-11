package admission

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// clearIgnoreEnv makes a test independent of the caller's shell. The one-off
// bypass is read from the environment, so a developer with DOT_ADMISSION_IGNORE
// exported would otherwise flip every default, precedence and expiry assertion.
func clearIgnoreEnv(t *testing.T) {
	t.Helper()
	t.Setenv(IgnoreEnv, "")
}

func writeAdmissionConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// AC1: a host with no config, no override and no environment resolves to the
// values the controller hard-coded, with no source recorded.
func TestResolvePolicyMissingConfigIsDefaults(t *testing.T) {
	clearIgnoreEnv(t)
	store := NewStore(filepath.Join(t.TempDir(), "admission"), nil)
	policy, err := store.ResolvePolicy(ResolveOptions{ConfigPath: filepath.Join(t.TempDir(), "absent.yaml")})
	if err != nil {
		t.Fatal(err)
	}
	if policy.Thresholds != DefaultThresholds() {
		t.Errorf("thresholds = %+v, want the policy defaults", policy.Thresholds)
	}
	want := DefaultPolicy()
	if policy.Prebuilt != want.Prebuilt || policy.Slots != want.Slots ||
		policy.Uncovered.Enabled != want.Uncovered.Enabled ||
		policy.Uncovered.CPUPercent != want.Uncovered.CPUPercent ||
		len(policy.Uncovered.IgnoreNames) != 0 {
		t.Errorf("policy = %+v, want the defaults %+v", policy, want)
	}
	if len(policy.Sources) != 0 || policy.Ignore.Any() || policy.Relax != nil {
		t.Errorf("a missing config reported overrides: %+v", policy)
	}
	if policy.Prebuilt.SafetyWindow != PrebuiltSafetyWindow {
		t.Errorf("safety window = %s, want the shipped %s", policy.Prebuilt.SafetyWindow, PrebuiltSafetyWindow)
	}
}

// AC2: each config key reaches its own rule, and an unset key keeps its default.
func TestResolvePolicyConfigLayers(t *testing.T) {
	clearIgnoreEnv(t)
	configPath := writeAdmissionConfig(t, `name: test
admission:
  thresholds:
    recover_sustain: 1m
    idle_defer_below: 5
    idle_recover_at: 10
  prebuilt:
    idle_defer_below: 10
    safety_window: 45s
  slots:
    heavy_per_repo: 2
    maintenance: 3
  uncovered:
    cpu_percent: 0
    ignore_names: [node, rustc]
`)
	store := NewStore(filepath.Join(t.TempDir(), "admission"), nil)
	policy, err := store.ResolvePolicy(ResolveOptions{ConfigPath: configPath})
	if err != nil {
		t.Fatal(err)
	}
	if policy.Thresholds.RecoverSustain != time.Minute || policy.Thresholds.IdleDeferBelow != 5 {
		t.Errorf("thresholds = %+v, want the config values", policy.Thresholds)
	}
	if policy.Source(KeyRecoverSustain) != SourceConfig || policy.Source(KeyIdleDeferBelow) != SourceConfig {
		t.Errorf("sources = %v, want config for the set keys", policy.Sources)
	}
	if policy.Source(KeyDeferSustain) != SourceDefault || policy.Thresholds.DeferSustain != DefaultDeferSustain {
		t.Errorf("defer_sustain = %s (%s), want the unchanged default",
			policy.Thresholds.DeferSustain, policy.Source(KeyDeferSustain))
	}
	if policy.Prebuilt.Thresholds.IdleDeferBelow != 10 || policy.Prebuilt.SafetyWindow != 45*time.Second {
		t.Errorf("prebuilt = %+v, want the config block", policy.Prebuilt)
	}
	if policy.Slots.HeavyPerRepo != 2 || policy.Slots.Maintenance != 3 {
		t.Errorf("slots = %+v, want 2 and 3", policy.Slots)
	}
	if policy.Uncovered.Enabled != true || policy.Uncovered.CPUPercent != 0 {
		t.Errorf("uncovered = %+v, want enabled with the CPU rule off", policy.Uncovered)
	}
	if len(policy.Uncovered.IgnoreNames) != 2 || policy.Uncovered.IgnoreNames[1] != "rustc" {
		t.Errorf("ignore_names = %v, want [node rustc]", policy.Uncovered.IgnoreNames)
	}
	if policy.Ignore.Any() {
		t.Error("config alone must not bypass a rule")
	}
}

// AC2: an invalid value fails loudly, naming the key, and a typo is refused
// rather than silently doing nothing.
func TestResolvePolicyRejectsInvalidConfigNamingKey(t *testing.T) {
	clearIgnoreEnv(t)
	cases := []struct{ body, wantKey string }{
		{"admission:\n  thresholds:\n    idle_defer_below: 150\n", KeyIdleDeferBelow},
		{"admission:\n  slots:\n    heavy_per_repo: 9\n", KeyHeavyPerRepo},
		{"admission:\n  uncovered:\n    cpu_percent: -1\n", KeyUncoveredCPUPercent},
		{"admission:\n  prebuilt:\n    safety_window: soon\n", KeyPrebuiltSafetyWindow},
		{"admission:\n  thresholds:\n    recover_sustain: 1x\n", KeyRecoverSustain},
	}
	for _, tc := range cases {
		store := NewStore(filepath.Join(t.TempDir(), "admission"), nil)
		configPath := writeAdmissionConfig(t, tc.body)
		_, err := store.ResolvePolicy(ResolveOptions{ConfigPath: configPath})
		if err == nil || !strings.Contains(err.Error(), policyKeyPrefix+tc.wantKey) || !strings.Contains(err.Error(), configPath) {
			t.Errorf("config %q: err = %v, want a message naming %s%s and the file %s", tc.body, err, policyKeyPrefix, tc.wantKey, configPath)
		}
	}
	store := NewStore(filepath.Join(t.TempDir(), "admission"), nil)
	configPath := writeAdmissionConfig(t, "admission:\n  thresholds:\n    idle_defer_belo: 5\n")
	_, err := store.ResolvePolicy(ResolveOptions{ConfigPath: configPath})
	if err == nil || !strings.Contains(err.Error(), "unknown admission policy key") || !strings.Contains(err.Error(), configPath) {
		t.Errorf("typo err = %v, want an unknown-key error naming %s", err, configPath)
	}
}

// A comparison can span two layers: the config may set one side and the
// defaults the other, and the ordering error must still name the file.
func TestResolvePolicyConfigOrderingAcrossLayers(t *testing.T) {
	clearIgnoreEnv(t)
	configPath := writeAdmissionConfig(t, "admission:\n  thresholds:\n    idle_defer_below: 50\n")
	store := NewStore(filepath.Join(t.TempDir(), "admission"), nil)
	_, err := store.ResolvePolicy(ResolveOptions{ConfigPath: configPath})
	if err == nil || !strings.Contains(err.Error(), KeyIdleRecoverAt) || !strings.Contains(err.Error(), configPath) {
		t.Fatalf("err = %v, want the ordering error naming %s and the file %s", err, KeyIdleRecoverAt, configPath)
	}
}

// Precedence: relax layers over config, and a one-off bypass (flag, then
// environment) beats the relax override without narrowing it.
func TestResolvePolicyPrecedence(t *testing.T) {
	clearIgnoreEnv(t)
	base := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	configPath := writeAdmissionConfig(t, "admission:\n  thresholds:\n    recover_sustain: 2m\n")
	store := NewStore(filepath.Join(t.TempDir(), "admission"), nil)
	now := base
	store.Now = func() time.Time { return now }
	if err := store.SaveRelaxOverride(RelaxOverride{
		SetAt:     base,
		ExpiresAt: base.Add(10 * time.Minute),
		Ignore:    []string{RulePressure},
		Set:       map[string]string{KeyRecoverSustain: "1m", KeyHeavyPerRepo: "2"},
	}); err != nil {
		t.Fatal(err)
	}

	t.Run("relax layers over config", func(t *testing.T) {
		policy, err := store.ResolvePolicy(ResolveOptions{ConfigPath: configPath})
		if err != nil {
			t.Fatal(err)
		}
		if policy.Thresholds.RecoverSustain != time.Minute || policy.Source(KeyRecoverSustain) != SourceRelax {
			t.Errorf("recover_sustain = %s (%s), want 1m0s from relax",
				policy.Thresholds.RecoverSustain, policy.Source(KeyRecoverSustain))
		}
		if policy.Slots.HeavyPerRepo != 2 || policy.Source(KeyHeavyPerRepo) != SourceRelax {
			t.Errorf("heavy_per_repo = %d (%s), want 2 from relax",
				policy.Slots.HeavyPerRepo, policy.Source(KeyHeavyPerRepo))
		}
		if !policy.Ignore.Pressure || !strings.Contains(policy.IgnoreSource, "dot admit relax") {
			t.Errorf("ignore = %+v (%q), want the relax pressure bypass", policy.Ignore, policy.IgnoreSource)
		}
		if policy.Relax == nil || !policy.Relax.ExpiresAt.Equal(base.Add(10*time.Minute)) {
			t.Errorf("relax = %+v, want the active override", policy.Relax)
		}
	})

	t.Run("flag beats relax and adds to it", func(t *testing.T) {
		policy, err := store.ResolvePolicy(ResolveOptions{
			ConfigPath: configPath,
			Ignore:     IgnoreSet{Slot: true},
		})
		if err != nil {
			t.Fatal(err)
		}
		if policy.IgnoreSource != "--ignore slot" {
			t.Errorf("ignore source = %q, want the flag label", policy.IgnoreSource)
		}
		if !policy.Ignore.Pressure || !policy.Ignore.Slot {
			t.Errorf("ignore = %+v, want the flag's slot plus the window's pressure", policy.Ignore)
		}
	})

	t.Run("env beats relax", func(t *testing.T) {
		t.Setenv(IgnoreEnv, "slot,uncovered")
		policy, err := store.ResolvePolicy(ResolveOptions{ConfigPath: configPath})
		if err != nil {
			t.Fatal(err)
		}
		if policy.IgnoreSource != IgnoreEnv+"=slot,uncovered" {
			t.Errorf("ignore source = %q, want the env label", policy.IgnoreSource)
		}
		if !policy.Ignore.Slot || !policy.Ignore.Uncovered || !policy.Ignore.Pressure {
			t.Errorf("ignore = %+v, want the env rules plus the window's pressure", policy.Ignore)
		}
	})

	t.Run("env is validated", func(t *testing.T) {
		t.Setenv(IgnoreEnv, "slots")
		if _, err := store.ResolvePolicy(ResolveOptions{ConfigPath: configPath}); err == nil ||
			!strings.Contains(err.Error(), IgnoreEnv) {
			t.Fatalf("err = %v, want the variable named for a bad rule", err)
		}
	})
}

// AC3: an expired override stops applying, the gate removes the file, and a
// read-only view leaves it alone. A file the controller cannot honor fails
// loudly instead of reading as "no override".
func TestResolvePolicyExpiresRelaxOverride(t *testing.T) {
	clearIgnoreEnv(t)
	base := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	store := NewStore(filepath.Join(t.TempDir(), "admission"), nil)
	now := base
	store.Now = func() time.Time { return now }

	t.Run("prunes after expiry", func(t *testing.T) {
		if err := store.SaveRelaxOverride(RelaxOverride{
			SetAt:     base,
			ExpiresAt: base.Add(10 * time.Minute),
			Ignore:    []string{"all"},
		}); err != nil {
			t.Fatal(err)
		}
		now = base.Add(11 * time.Minute)
		policy, err := store.ResolvePolicy(ResolveOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if policy.Ignore.Any() || policy.Relax != nil {
			t.Fatalf("expired override still applied: %+v", policy)
		}
		if _, err := os.Stat(store.OverridePath()); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("expired override file still present: %v", err)
		}
	})

	t.Run("status-style read leaves the file", func(t *testing.T) {
		if err := store.SaveRelaxOverride(RelaxOverride{
			SetAt:     base,
			ExpiresAt: base.Add(10 * time.Minute),
			Ignore:    []string{RulePressure},
		}); err != nil {
			t.Fatal(err)
		}
		now = base.Add(11 * time.Minute)
		policy, err := store.ResolvePolicy(ResolveOptions{KeepExpired: true})
		if err != nil {
			t.Fatal(err)
		}
		if policy.Ignore.Any() {
			t.Fatal("expired override applied by a read-only resolution")
		}
		if _, err := os.Stat(store.OverridePath()); err != nil {
			t.Errorf("read-only resolution removed the override: %v", err)
		}
		if err := store.ClearRelaxOverride(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("corrupt override fails loudly", func(t *testing.T) {
		if err := os.MkdirAll(store.Root, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(store.OverridePath(), []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ResolvePolicy(ResolveOptions{}); err == nil || !strings.Contains(err.Error(), "--clear") {
			t.Fatalf("err = %v, want a corrupt-override error naming --clear", err)
		}
		if err := store.ClearRelaxOverride(); err != nil {
			t.Fatal(err)
		}
		if err := store.ClearRelaxOverride(); err != nil {
			t.Errorf("second --clear = %v, want idempotent", err)
		}
	})

	t.Run("unknown stored key fails loudly", func(t *testing.T) {
		if err := store.SaveRelaxOverride(RelaxOverride{
			SetAt:     base,
			ExpiresAt: base.Add(10 * time.Minute),
			Set:       map[string]string{"thresholds.nonsense": "1"},
		}); err != nil {
			t.Fatal(err)
		}
		now = base.Add(time.Minute)
		_, err := store.ResolvePolicy(ResolveOptions{})
		if err == nil || !strings.Contains(err.Error(), "admission.thresholds.nonsense") {
			t.Fatalf("err = %v, want the unknown key named", err)
		}
		if err := store.ClearRelaxOverride(); err != nil {
			t.Fatal(err)
		}
	})
}

// AC3: a relax override admits a deferring gate; once it expires the gate
// defers again and the file is gone.
func TestRelaxIgnoreAdmitsThenExpires(t *testing.T) {
	clearIgnoreEnv(t)
	base := evalT0
	store := NewStore(filepath.Join(t.TempDir(), "admission"), nil)
	now := base
	store.Now = func() time.Time { return now }

	snap := healthyDarwin()
	snap.MemoryLevel = MemoryCritical
	monitor := &Monitor{
		GOOS:         "darwin",
		Now:          func() time.Time { return now },
		SnapshotFunc: func(context.Context, *Monitor) PressureSnapshot { return snap },
	}

	if err := store.SaveRelaxOverride(RelaxOverride{
		SetAt:     base,
		ExpiresAt: base.Add(10 * time.Minute),
		Ignore:    []string{"all"},
	}); err != nil {
		t.Fatal(err)
	}
	policy, err := store.ResolvePolicy(ResolveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	d, err := store.GateResolved(context.Background(), monitor, policy)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Admit || !d.PressureIgnored || d.Ignored[0] != RulePressure {
		t.Fatalf("decision inside the override = %+v, want a bypassed admission", d)
	}

	now = base.Add(11 * time.Minute)
	policy, err = store.ResolvePolicy(ResolveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if policy.Ignore.Any() {
		t.Fatal("expired override still bypassing")
	}
	d, err = store.GateResolved(context.Background(), monitor, policy)
	if err != nil {
		t.Fatal(err)
	}
	if d.Admit {
		t.Fatalf("decision after expiry = %+v, want the pressure defer back", d)
	}
	if _, err := os.Stat(store.OverridePath()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("override file survived expiry: %v", err)
	}
}

// A bypassed pressure rule still samples and records, so recovery stays
// continuous after the bypass ends; a busy slot is never treated as pressure.
func TestGateIgnoreStillSamplesAndRecords(t *testing.T) {
	clearIgnoreEnv(t)
	fixed := evalT0
	sampled := 0
	snap := healthyDarwin()
	snap.MemoryLevel = MemoryCritical
	monitor := &Monitor{
		GOOS: "darwin",
		Now:  func() time.Time { return fixed },
		SnapshotFunc: func(context.Context, *Monitor) PressureSnapshot {
			sampled++
			return snap
		},
	}

	store := testStore(t)
	d, err := store.Gate(context.Background(), monitor, DefaultThresholds())
	if err != nil {
		t.Fatal(err)
	}
	if d.Admit {
		t.Fatal("gate admitted under critical memory")
	}

	before := sampled
	policy := DefaultPolicy()
	policy.Ignore = IgnoreSet{Pressure: true}
	policy.IgnoreSource = "--ignore pressure"
	d, err = store.GateResolved(context.Background(), monitor, policy)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Admit || !d.PressureIgnored {
		t.Fatalf("decision = %+v, want a bypassed admission", d)
	}
	if len(d.IgnoredReasons) == 0 || !strings.Contains(strings.Join(d.IgnoredReasons, "; "), "memory pressure critical") {
		t.Errorf("ignored reasons = %v, want the bypassed memory reason", d.IgnoredReasons)
	}
	if sampled-before != 1 {
		t.Errorf("snapshot probes = %d, want the bypassed gate to sample once", sampled-before)
	}
	hist, err := LoadHistory(store.HistoryPath())
	if err != nil {
		t.Fatal(err)
	}
	if !hist.DeferActive || !hist.LastSampleAt.Equal(fixed) {
		t.Errorf("persisted history = %+v, want the bypassed sample recorded", hist)
	}
}

// A bypass of another rule never turns a pressure defer into an admission, and
// an already-admitting decision is never marked as bypassed.
func TestApplyIgnoreLeavesOtherRulesAndAdmissionsAlone(t *testing.T) {
	busy := Decision{Reasons: []string{GateBusyReason}, RetryAfter: 10 * time.Second}
	if got := ApplyIgnore(busy, IgnoreSet{Pressure: true}, "--ignore pressure"); got.Admit || got.PressureIgnored {
		t.Errorf("busy gate became %+v, want the defer kept", got)
	}
	deferred := Decision{Reasons: []string{"memory pressure critical"}}
	if got := ApplyIgnore(deferred, IgnoreSet{Slot: true}, "--ignore slot"); got.Admit {
		t.Errorf("slot-only bypass admitted a pressure defer: %+v", got)
	}
	admitted := Decision{Admit: true}
	if got := ApplyIgnore(admitted, IgnoreSet{Pressure: true}, "--ignore pressure"); got.PressureIgnored {
		t.Error("an already-admitting decision was marked bypassed")
	}
}

// The prune re-reads the override under the root lock instead of deleting what
// the gate read: a `dot admit relax` that landed in between must survive, or the
// override the person just set disappears and the host silently returns to the
// defaults it was bypassing.
func TestPruneExpiredOverrideRereadsUnderTheLock(t *testing.T) {
	clearIgnoreEnv(t)
	store := NewStore(filepath.Join(t.TempDir(), "admission"), nil)
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	store.Now = func() time.Time { return now }

	expired := RelaxOverride{SetAt: now.Add(-time.Hour), ExpiresAt: now.Add(-time.Minute)}
	if err := store.SaveRelaxOverride(expired); err != nil {
		t.Fatalf("saving the expired override: %v", err)
	}
	// The gate read the expired file; by the time it prunes, this one replaced it.
	fresh := RelaxOverride{SetAt: now, ExpiresAt: now.Add(10 * time.Minute), Ignore: []string{RulePressure}}
	if err := store.SaveRelaxOverride(fresh); err != nil {
		t.Fatalf("saving the fresh override: %v", err)
	}
	store.pruneExpiredOverride()
	loaded, err := store.LoadRelaxOverride(false)
	if err != nil {
		t.Fatalf("reading back the override: %v", err)
	}
	if loaded == nil || !loaded.ExpiresAt.Equal(fresh.ExpiresAt) {
		t.Fatalf("override = %+v, want the fresh one kept", loaded)
	}

	// The same call still removes an override that is expired at prune time.
	if err := store.SaveRelaxOverride(expired); err != nil {
		t.Fatalf("re-saving the expired override: %v", err)
	}
	store.pruneExpiredOverride()
	if _, err := os.Stat(store.OverridePath()); !os.IsNotExist(err) {
		t.Fatalf("prune left an expired override on disk: %v", err)
	}
}
