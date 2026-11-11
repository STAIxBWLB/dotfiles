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

func writeAdmissionConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResolveThresholdsConfigLayer(t *testing.T) {
	configPath := writeAdmissionConfig(t, `name: test
admission:
  thresholds:
    recover_sustain: 1m
    idle_defer_below: 5
    idle_recover_at: 10
`)
	store := NewStore(filepath.Join(t.TempDir(), "admission"), nil)
	resolved, err := store.ResolveThresholds(ResolveOptions{ConfigPath: configPath})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Thresholds.RecoverSustain != time.Minute {
		t.Errorf("recover_sustain = %s, want 1m0s", resolved.Thresholds.RecoverSustain)
	}
	if resolved.Source(KeyRecoverSustain) != SourceConfig || resolved.Source(KeyIdleDeferBelow) != SourceConfig {
		t.Errorf("sources = %v, want config for the set keys", resolved.Sources)
	}
	if resolved.Source(KeyDeferSustain) != SourceDefault {
		t.Errorf("defer_sustain source = %s, want default", resolved.Source(KeyDeferSustain))
	}
	if resolved.Thresholds.DeferSustain != DefaultDeferSustain {
		t.Errorf("defer_sustain = %s, want the unchanged default %s", resolved.Thresholds.DeferSustain, DefaultDeferSustain)
	}
	if resolved.IgnorePressure {
		t.Error("config thresholds alone must not ignore pressure")
	}
}

func TestResolveThresholdsRejectsInvalidConfigNamingKey(t *testing.T) {
	configPath := writeAdmissionConfig(t, "admission:\n  thresholds:\n    idle_defer_below: 150\n")
	store := NewStore(filepath.Join(t.TempDir(), "admission"), nil)
	_, err := store.ResolveThresholds(ResolveOptions{ConfigPath: configPath})
	if err == nil || !strings.Contains(err.Error(), "admission.thresholds.idle_defer_below") {
		t.Fatalf("err = %v, want a message naming admission.thresholds.idle_defer_below", err)
	}
}

func TestResolveThresholdsConfigOrderingAcrossLayers(t *testing.T) {
	// One side of the comparison comes from the config and the other from the
	// defaults: raising the defer point above the default recovery point must
	// be refused rather than silently inverted.
	configPath := writeAdmissionConfig(t, "admission:\n  thresholds:\n    idle_defer_below: 50\n")
	store := NewStore(filepath.Join(t.TempDir(), "admission"), nil)
	_, err := store.ResolveThresholds(ResolveOptions{ConfigPath: configPath})
	if err == nil || !strings.Contains(err.Error(), "idle_recover_at") {
		t.Fatalf("err = %v, want the ordering error naming idle_recover_at", err)
	}
}

func TestResolveThresholdsPrecedence(t *testing.T) {
	base := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	configPath := writeAdmissionConfig(t, "admission:\n  thresholds:\n    recover_sustain: 2m\n")
	store := NewStore(filepath.Join(t.TempDir(), "admission"), nil)
	now := base
	store.Now = func() time.Time { return now }
	if err := store.SaveRelaxOverride(RelaxOverride{
		SetAt:          base,
		ExpiresAt:      base.Add(10 * time.Minute),
		IgnorePressure: true,
		Thresholds:     ThresholdOverrides{RecoverSustain: flexPtr(time.Minute)},
	}); err != nil {
		t.Fatal(err)
	}

	t.Run("relax layers over config", func(t *testing.T) {
		resolved, err := store.ResolveThresholds(ResolveOptions{ConfigPath: configPath})
		if err != nil {
			t.Fatal(err)
		}
		if resolved.Thresholds.RecoverSustain != time.Minute || resolved.Source(KeyRecoverSustain) != SourceRelax {
			t.Errorf("recover_sustain = %s (%s), want 1m0s from relax", resolved.Thresholds.RecoverSustain, resolved.Source(KeyRecoverSustain))
		}
		if !resolved.IgnorePressure || !strings.Contains(resolved.IgnoreSource, "dot admit relax") {
			t.Errorf("ignore = %v (%q), want the relax ignore", resolved.IgnorePressure, resolved.IgnoreSource)
		}
		if resolved.Relax == nil || !resolved.Relax.ExpiresAt.Equal(base.Add(10*time.Minute)) {
			t.Errorf("relax = %+v, want the active override", resolved.Relax)
		}
	})

	t.Run("flag beats relax", func(t *testing.T) {
		resolved, err := store.ResolveThresholds(ResolveOptions{ConfigPath: configPath, IgnorePressure: true})
		if err != nil {
			t.Fatal(err)
		}
		if resolved.IgnoreSource != "--ignore-pressure" {
			t.Errorf("ignore source = %q, want the flag label", resolved.IgnoreSource)
		}
	})

	t.Run("env beats relax", func(t *testing.T) {
		t.Setenv(IgnorePressureEnv, "1")
		resolved, err := store.ResolveThresholds(ResolveOptions{ConfigPath: configPath})
		if err != nil {
			t.Fatal(err)
		}
		if resolved.IgnoreSource != IgnorePressureEnv+"=1" {
			t.Errorf("ignore source = %q, want the env label", resolved.IgnoreSource)
		}
	})
}

func TestResolveThresholdsExpiresRelaxOverride(t *testing.T) {
	base := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	store := NewStore(filepath.Join(t.TempDir(), "admission"), nil)
	now := base
	store.Now = func() time.Time { return now }

	t.Run("prunes after expiry", func(t *testing.T) {
		if err := store.SaveRelaxOverride(RelaxOverride{
			SetAt:          base,
			ExpiresAt:      base.Add(10 * time.Minute),
			IgnorePressure: true,
		}); err != nil {
			t.Fatal(err)
		}
		now = base.Add(11 * time.Minute)
		resolved, err := store.ResolveThresholds(ResolveOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if resolved.IgnorePressure || resolved.Relax != nil {
			t.Fatalf("expired override still applied: %+v", resolved)
		}
		if _, err := os.Stat(store.OverridePath()); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("expired override file still present: %v", err)
		}
	})

	t.Run("status-style read leaves the file", func(t *testing.T) {
		if err := store.SaveRelaxOverride(RelaxOverride{
			SetAt:          base,
			ExpiresAt:      base.Add(10 * time.Minute),
			IgnorePressure: true,
		}); err != nil {
			t.Fatal(err)
		}
		now = base.Add(11 * time.Minute)
		resolved, err := store.ResolveThresholds(ResolveOptions{KeepExpired: true})
		if err != nil {
			t.Fatal(err)
		}
		if resolved.IgnorePressure {
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
		if _, err := store.ResolveThresholds(ResolveOptions{}); err == nil || !strings.Contains(err.Error(), "--clear") {
			t.Fatalf("err = %v, want a corrupt-override error naming --clear", err)
		}
		if err := store.ClearRelaxOverride(); err != nil {
			t.Fatal(err)
		}
		if err := store.ClearRelaxOverride(); err != nil {
			t.Errorf("second --clear = %v, want idempotent", err)
		}
	})
}

func TestGateIgnoreStillSamplesAndRecords(t *testing.T) {
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

	t.Run("deferred without ignore", func(t *testing.T) {
		store := testStore(t)
		d, err := store.Gate(context.Background(), monitor, DefaultThresholds())
		if err != nil {
			t.Fatal(err)
		}
		if d.Admit {
			t.Fatal("gate admitted under critical memory")
		}
	})

	store := testStore(t)
	before := sampled
	d, err := store.GateWithPolicy(context.Background(), monitor, GatePolicy{
		Thresholds:     DefaultThresholds(),
		IgnorePressure: true,
		IgnoreSource:   "--ignore-pressure",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !d.Admit || !d.PressureIgnored {
		t.Fatalf("decision = %+v, want an ignored admission", d)
	}
	if len(d.IgnoredReasons) == 0 || !strings.Contains(strings.Join(d.IgnoredReasons, "; "), "memory pressure critical") {
		t.Errorf("ignored reasons = %v, want the bypassed memory reason", d.IgnoredReasons)
	}
	if sampled-before != 1 {
		t.Errorf("snapshot probes = %d, want the ignored gate to sample once", sampled-before)
	}
	hist, err := LoadHistory(store.HistoryPath())
	if err != nil {
		t.Fatal(err)
	}
	if !hist.DeferActive || !hist.LastSampleAt.Equal(fixed) {
		t.Errorf("persisted history = %+v, want the ignored sample recorded", hist)
	}
}

func TestApplyIgnoreLeavesGateBusyAndAdmissionsAlone(t *testing.T) {
	busy := Decision{Reasons: []string{GateBusyReason}, RetryAfter: 10 * time.Second}
	if got := ApplyIgnore(busy, "--ignore-pressure"); got.Admit || got.PressureIgnored {
		t.Errorf("busy gate became %+v, want the defer kept", got)
	}
	admitted := Decision{Admit: true}
	if got := ApplyIgnore(admitted, "--ignore-pressure"); got.PressureIgnored {
		t.Error("an already-admitting decision was marked ignored")
	}
}

// AC3: a relax override admits a deferring gate, the override expires, the
// gate defers again, and the file is gone.
func TestRelaxIgnoreAdmitsThenExpires(t *testing.T) {
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
		SetAt:          base,
		ExpiresAt:      base.Add(10 * time.Minute),
		IgnorePressure: true,
	}); err != nil {
		t.Fatal(err)
	}
	resolved, err := store.ResolveThresholds(ResolveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	d, err := store.GateResolved(context.Background(), monitor, resolved)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Admit || !d.PressureIgnored {
		t.Fatalf("decision inside the override = %+v, want an ignored admission", d)
	}

	now = base.Add(11 * time.Minute)
	resolved, err = store.ResolveThresholds(ResolveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.IgnorePressure {
		t.Fatal("expired override still ignoring pressure")
	}
	d, err = store.GateResolved(context.Background(), monitor, resolved)
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

func TestResolveThresholdsMissingConfigIsDefaults(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "admission"), nil)
	resolved, err := store.ResolveThresholds(ResolveOptions{ConfigPath: filepath.Join(t.TempDir(), "absent.yaml")})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Thresholds != DefaultThresholds() {
		t.Errorf("thresholds = %+v, want the policy defaults", resolved.Thresholds)
	}
	if len(resolved.Sources) != 0 || resolved.IgnorePressure {
		t.Errorf("a missing config reported overrides: %+v", resolved)
	}
}
