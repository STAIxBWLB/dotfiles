package admission_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/admission"
	"github.com/STAIxBWLB/dotfiles/internal/config"
)

// The admission block lives in the user state file, so every key must survive a
// state save/load and read back through the controller's own key table. The two
// struct sets are deliberately separate (internal/config cannot import
// internal/admission: the watchdog already links them the other way), so this
// pins that their YAML names and types have not drifted.
func TestUserStateAdmissionBlockRoundTrips(t *testing.T) {
	home := t.TempDir()
	idleDefer, idleRecover := 10.0, 25.0
	loadDefer, loadRecover := 1.0, 0.7
	memWarn, memCritical := 12.0, 6.0
	deferSustain := config.Duration(30 * time.Second)
	recoverSustain := config.Duration(2 * time.Minute)
	wsGrace := config.Duration(15 * time.Minute)
	prebuiltIdle, prebuiltRecover := 40.0, 45.0
	prebuiltWindow := config.Duration(45 * time.Second)
	heavySlots, maintenanceSlots := 2, 3
	scanDisabled := false
	cpuPercent := 0.0
	state := &config.UserState{
		Name:     "Test Person",
		Email:    "test@example.com",
		Profile:  "full",
		Timezone: "Asia/Seoul",
		Admission: config.UserAdmissionState{
			Thresholds: config.UserAdmissionThresholds{
				IdleDeferBelow:      &idleDefer,
				IdleRecoverAt:       &idleRecover,
				LoadDeferFrac:       &loadDefer,
				LoadRecoverFrac:     &loadRecover,
				DeferSustain:        &deferSustain,
				RecoverSustain:      &recoverSustain,
				WSGrace:             &wsGrace,
				MemAvailWarnPct:     &memWarn,
				MemAvailCriticalPct: &memCritical,
			},
			Prebuilt: config.UserAdmissionPrebuilt{
				IdleDeferBelow: &prebuiltIdle,
				IdleRecoverAt:  &prebuiltRecover,
				SafetyWindow:   &prebuiltWindow,
			},
			Slots: config.UserAdmissionSlots{
				HeavyPerRepo: &heavySlots,
				Maintenance:  &maintenanceSlots,
			},
			Uncovered: config.UserAdmissionUncovered{
				Enabled:     &scanDisabled,
				CPUPercent:  &cpuPercent,
				IgnoreNames: []string{"node", "rustc"},
			},
		},
	}
	if err := config.SaveStateForHome(home, state); err != nil {
		t.Fatalf("saving the state: %v", err)
	}
	loaded, err := config.LoadStateForHome(home)
	if err != nil {
		t.Fatalf("loading the state: %v", err)
	}
	if loaded.Admission.Slots.HeavyPerRepo == nil || *loaded.Admission.Slots.HeavyPerRepo != 2 {
		t.Fatalf("reloaded heavy_per_repo = %v, want 2", loaded.Admission.Slots.HeavyPerRepo)
	}

	store := admission.NewStore(filepath.Join(home, "admission"), nil)
	policy, err := store.ResolvePolicy(admission.ResolveOptions{
		ConfigPath: config.StatePathForHome(home),
	})
	if err != nil {
		t.Fatalf("resolving from the saved state: %v", err)
	}
	checks := []struct {
		key  string
		want string
	}{
		{admission.KeyIdleDeferBelow, "10%"},
		{admission.KeyIdleRecoverAt, "25%"},
		{admission.KeyDeferSustain, "30s"},
		{admission.KeyRecoverSustain, "2m0s"},
		{admission.KeyWSGrace, "15m0s"},
		{admission.KeyMemAvailWarnPct, "12%"},
		{admission.KeyMemAvailCriticalPct, "6%"},
		{admission.KeyPrebuiltIdleDeferBelow, "40%"},
		{admission.KeyPrebuiltSafetyWindow, "45s"},
		{admission.KeyHeavyPerRepo, "2"},
		{admission.KeyMaintenanceSlots, "3"},
		{admission.KeyUncoveredEnabled, "false"},
		{admission.KeyUncoveredCPUPercent, "0%"},
		{admission.KeyUncoveredIgnoreNames, "node,rustc"},
	}
	rows := map[string]admission.PolicyRow{}
	for _, row := range policy.Rows() {
		rows[row.Key] = row
	}
	for _, check := range checks {
		row, ok := rows[check.key]
		if !ok || row.Value != check.want || row.Source != admission.SourceConfig {
			t.Errorf("%s = %+v, want %s (%s)", check.key, row, check.want, admission.SourceConfig)
		}
	}
	if policy.Thresholds.LoadDeferFrac != 1.0 || policy.Thresholds.LoadRecoverFrac != 0.7 {
		t.Errorf("load fractions = %g/%g, want 1/0.7", policy.Thresholds.LoadDeferFrac, policy.Thresholds.LoadRecoverFrac)
	}
}
