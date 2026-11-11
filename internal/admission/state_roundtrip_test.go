package admission_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/admission"
	"github.com/STAIxBWLB/dotfiles/internal/config"
)

// The admission.thresholds block lives in the user state file, so it must
// survive a state save/load and read back through the gate's own parser. The
// two structs are deliberately separate (internal/config cannot import
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
	state := &config.UserState{
		Name:     "Test Person",
		Email:    "test@example.com",
		Profile:  "full",
		Timezone: "Asia/Seoul",
		Admission: config.UserAdmissionState{Thresholds: config.UserAdmissionThresholds{
			IdleDeferBelow:      &idleDefer,
			IdleRecoverAt:       &idleRecover,
			LoadDeferFrac:       &loadDefer,
			LoadRecoverFrac:     &loadRecover,
			DeferSustain:        &deferSustain,
			RecoverSustain:      &recoverSustain,
			WSGrace:             &wsGrace,
			MemAvailWarnPct:     &memWarn,
			MemAvailCriticalPct: &memCritical,
		}},
	}
	if err := config.SaveStateForHome(home, state); err != nil {
		t.Fatalf("saving the state: %v", err)
	}
	loaded, err := config.LoadStateForHome(home)
	if err != nil {
		t.Fatalf("loading the state: %v", err)
	}
	if loaded.Admission.Thresholds.RecoverSustain == nil ||
		loaded.Admission.Thresholds.RecoverSustain.Std() != 2*time.Minute {
		t.Fatalf("reloaded recover_sustain = %v, want 2m0s", loaded.Admission.Thresholds.RecoverSustain)
	}

	store := admission.NewStore(filepath.Join(home, "admission"), nil)
	resolved, err := store.ResolveThresholds(admission.ResolveOptions{
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
	}
	rows := map[string]admission.ThresholdRow{}
	for _, row := range resolved.Rows() {
		rows[row.Key] = row
	}
	for _, check := range checks {
		row, ok := rows[check.key]
		if !ok || row.Value != check.want || row.Source != admission.SourceConfig {
			t.Errorf("%s = %+v, want %s (%s)", check.key, row, check.want, admission.SourceConfig)
		}
	}
	if resolved.Thresholds.LoadDeferFrac != 1.0 || resolved.Thresholds.LoadRecoverFrac != 0.7 {
		t.Errorf("load fractions = %g/%g, want 1/0.7", resolved.Thresholds.LoadDeferFrac, resolved.Thresholds.LoadRecoverFrac)
	}
}
