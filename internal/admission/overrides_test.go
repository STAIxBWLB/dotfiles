package admission

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func floatPtr(v float64) *float64 { return &v }

func flexPtr(d time.Duration) *FlexDuration {
	flex := FlexDuration(d)
	return &flex
}

func TestThresholdOverridesValidate(t *testing.T) {
	cases := []struct {
		name    string
		value   ThresholdOverrides
		wantKey string
	}{
		{"idle percent too high", ThresholdOverrides{IdleDeferBelow: floatPtr(150)}, "idle_defer_below"},
		{"idle percent negative", ThresholdOverrides{IdleRecoverAt: floatPtr(-1)}, "idle_recover_at"},
		{"warn percent too high", ThresholdOverrides{MemAvailWarnPct: floatPtr(101)}, "mem_avail_warn_pct"},
		{"critical percent too high", ThresholdOverrides{MemAvailCriticalPct: floatPtr(120)}, "mem_avail_critical_pct"},
		{"zero load fraction", ThresholdOverrides{LoadDeferFrac: floatPtr(0)}, "load_defer_frac"},
		{"negative recover fraction", ThresholdOverrides{LoadRecoverFrac: floatPtr(-0.5)}, "load_recover_frac"},
		{"negative duration", ThresholdOverrides{RecoverSustain: flexPtr(-time.Minute)}, "recover_sustain"},
		{"negative grace", ThresholdOverrides{WSGrace: flexPtr(-time.Second)}, "ws_grace"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.value.Validate()
			if err == nil {
				t.Fatalf("%+v validated, want an error naming %s", tc.value, tc.wantKey)
			}
			if !strings.Contains(err.Error(), "admission.thresholds."+tc.wantKey) {
				t.Errorf("error %q does not name admission.thresholds.%s", err, tc.wantKey)
			}
		})
	}
}

func TestThresholdOverridesValidateAcceptsZeroDuration(t *testing.T) {
	// defer_sustain: 0 is meaningful (defer at the first over-threshold
	// sample), so a pointer must not be confused with unset.
	if err := (ThresholdOverrides{DeferSustain: flexPtr(0)}).Validate(); err != nil {
		t.Fatalf("zero defer_sustain rejected: %v", err)
	}
	th := ThresholdOverrides{DeferSustain: flexPtr(0)}.apply(DefaultThresholds(), "", nil)
	if th.DeferSustain != 0 {
		t.Errorf("defer_sustain = %s, want 0", th.DeferSustain)
	}
}

func TestValidateThresholdsOrdering(t *testing.T) {
	cases := []struct {
		name string
		th   Thresholds
		want string
	}{
		{
			"recover below defer",
			func() Thresholds { th := DefaultThresholds(); th.IdleRecoverAt = 10; return th }(),
			"idle_recover_at",
		},
		{
			"recover fraction above defer fraction",
			func() Thresholds { th := DefaultThresholds(); th.LoadRecoverFrac = 1.2; return th }(),
			"load_recover_frac",
		},
		{
			"critical above warn",
			func() Thresholds { th := DefaultThresholds(); th.MemAvailCriticalPct = 20; return th }(),
			"mem_avail_critical_pct",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateThresholds(tc.th)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validateThresholds(%+v) = %v, want an error naming %s", tc.th, err, tc.want)
			}
		})
	}
	if err := validateThresholds(DefaultThresholds()); err != nil {
		t.Fatalf("policy defaults fail their own ordering check: %v", err)
	}
}

func TestFlexDurationDecodesYAMLAndJSON(t *testing.T) {
	var yamlBlock struct {
		RecoverSustain FlexDuration `yaml:"recover_sustain"`
		DeferSustain   FlexDuration `yaml:"defer_sustain"`
	}
	if err := yaml.Unmarshal([]byte("recover_sustain: 1m\ndefer_sustain: 30\n"), &yamlBlock); err != nil {
		t.Fatal(err)
	}
	if yamlBlock.RecoverSustain.Std() != time.Minute || yamlBlock.DeferSustain.Std() != 30*time.Second {
		t.Errorf("yaml decode = %s / %s, want 1m0s / 30s", yamlBlock.RecoverSustain.Std(), yamlBlock.DeferSustain.Std())
	}

	var jsonBlock struct {
		RecoverSustain *FlexDuration `json:"recover_sustain"`
		DeferSustain   *FlexDuration `json:"defer_sustain"`
	}
	if err := json.Unmarshal([]byte(`{"recover_sustain":"90s","defer_sustain":5}`), &jsonBlock); err != nil {
		t.Fatal(err)
	}
	if jsonBlock.RecoverSustain == nil || jsonBlock.RecoverSustain.Std() != 90*time.Second {
		t.Errorf("json string decode = %v, want 1m30s", jsonBlock.RecoverSustain)
	}
	if jsonBlock.DeferSustain == nil || jsonBlock.DeferSustain.Std() != 5*time.Second {
		t.Errorf("json number decode = %v, want 5s", jsonBlock.DeferSustain)
	}

	encoded, err := json.Marshal(ThresholdOverrides{RecoverSustain: flexPtr(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"recover_sustain":"1m0s"`) {
		t.Errorf("json encode = %s, want the duration string", encoded)
	}
}
