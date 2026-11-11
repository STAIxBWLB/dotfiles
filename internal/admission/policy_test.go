package admission

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func floatPtr(v float64) *float64 { return &v }

func intPtr(v int) *int { return &v }

func boolPtr(v bool) *bool { return &v }

func flexPtr(d time.Duration) *FlexDuration {
	flex := FlexDuration(d)
	return &flex
}

func namesPtr(names ...string) *[]string { return &names }

// AC2: every key rejects an out-of-range value with a message naming it.
func TestPolicyOverridesValidate(t *testing.T) {
	cases := []struct {
		name    string
		value   PolicyOverrides
		wantKey string
	}{
		{"idle percent too high", PolicyOverrides{IdleDeferBelow: floatPtr(150)}, KeyIdleDeferBelow},
		{"idle percent negative", PolicyOverrides{IdleRecoverAt: floatPtr(-1)}, KeyIdleRecoverAt},
		{"warn percent too high", PolicyOverrides{MemAvailWarnPct: floatPtr(101)}, KeyMemAvailWarnPct},
		{"critical percent too high", PolicyOverrides{MemAvailCriticalPct: floatPtr(120)}, KeyMemAvailCriticalPct},
		{"prebuilt idle percent too high", PolicyOverrides{PrebuiltIdleDeferBelow: floatPtr(101)}, KeyPrebuiltIdleDeferBelow},
		{"prebuilt recover percent negative", PolicyOverrides{PrebuiltIdleRecoverAt: floatPtr(-0.5)}, KeyPrebuiltIdleRecoverAt},
		{"zero load fraction", PolicyOverrides{LoadDeferFrac: floatPtr(0)}, KeyLoadDeferFrac},
		{"negative recover fraction", PolicyOverrides{LoadRecoverFrac: floatPtr(-0.5)}, KeyLoadRecoverFrac},
		{"prebuilt zero defer fraction", PolicyOverrides{PrebuiltLoadDeferFrac: floatPtr(0)}, KeyPrebuiltLoadDeferFrac},
		{"prebuilt negative recover fraction", PolicyOverrides{PrebuiltLoadRecoverFrac: floatPtr(-1)}, KeyPrebuiltLoadRecoverFrac},
		{"negative duration", PolicyOverrides{RecoverSustain: flexPtr(-time.Minute)}, KeyRecoverSustain},
		{"negative grace", PolicyOverrides{WSGrace: flexPtr(-time.Second)}, KeyWSGrace},
		{"negative safety window", PolicyOverrides{PrebuiltSafetyWindow: flexPtr(-time.Second)}, KeyPrebuiltSafetyWindow},
		{"zero heavy slots", PolicyOverrides{HeavyPerRepo: intPtr(0)}, KeyHeavyPerRepo},
		{"nine maintenance slots", PolicyOverrides{MaintenanceSlots: intPtr(9)}, KeyMaintenanceSlots},
		{"negative cpu percent", PolicyOverrides{UncoveredCPUPercent: floatPtr(-1)}, KeyUncoveredCPUPercent},
		{"name with a path", PolicyOverrides{UncoveredIgnoreNames: namesPtr("bin/node")}, KeyUncoveredIgnoreNames},
		{"empty name", PolicyOverrides{UncoveredIgnoreNames: namesPtr("")}, KeyUncoveredIgnoreNames},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.value.Validate()
			if err == nil {
				t.Fatalf("%+v validated, want an error naming %s", tc.value, tc.wantKey)
			}
			if !strings.Contains(err.Error(), policyKeyPrefix+tc.wantKey) {
				t.Errorf("error %q does not name %s%s", err, policyKeyPrefix, tc.wantKey)
			}
		})
	}
}

// Meaningful zeros and out-of-percent values that are legitimate must validate:
// the CPU-only rule's threshold is a share of one core, so 200 is allowed, and 0
// turns the rule (or the whole scan) off.
func TestPolicyOverridesValidateAcceptsEdgeValues(t *testing.T) {
	cases := []struct {
		name  string
		value PolicyOverrides
	}{
		{"zero defer sustain", PolicyOverrides{DeferSustain: flexPtr(0)}},
		{"zero safety window", PolicyOverrides{PrebuiltSafetyWindow: flexPtr(0)}},
		{"cpu rule off", PolicyOverrides{UncoveredCPUPercent: floatPtr(0)}},
		{"cpu rule above one core", PolicyOverrides{UncoveredCPUPercent: floatPtr(200)}},
		{"scan disabled", PolicyOverrides{UncoveredEnabled: boolPtr(false)}},
		{"eight slots", PolicyOverrides{HeavyPerRepo: intPtr(8), MaintenanceSlots: intPtr(8)}},
		{"names", PolicyOverrides{UncoveredIgnoreNames: namesPtr("node", "rustc")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.value.Validate(); err != nil {
				t.Fatalf("%+v rejected: %v", tc.value, err)
			}
		})
	}
	th := PolicyOverrides{DeferSustain: flexPtr(0)}.apply(DefaultPolicy(), "", nil).Thresholds
	if th.DeferSustain != 0 {
		t.Errorf("defer_sustain = %s, want 0", th.DeferSustain)
	}
}

func TestPolicyOverridesSetKeyParsesWrittenForms(t *testing.T) {
	var overrides PolicyOverrides
	cases := []struct{ key, value string }{
		{KeyIdleDeferBelow, "15"},
		{KeyLoadRecoverFrac, "0.7"},
		{KeyRecoverSustain, "1m"},
		{KeyPrebuiltSafetyWindow, "45"}, // seconds
		{KeyHeavyPerRepo, "2"},
		{KeyUncoveredEnabled, "false"},
		{KeyUncoveredCPUPercent, "150"},
		{KeyUncoveredIgnoreNames, "node, rustc"},
		{"admission." + KeyWSGrace, "20m"}, // the written prefix is tolerated
	}
	for _, tc := range cases {
		if err := overrides.SetKey(tc.key, tc.value); err != nil {
			t.Fatalf("SetKey(%q, %q) = %v", tc.key, tc.value, err)
		}
	}
	policy := overrides.apply(DefaultPolicy(), "", nil)
	if policy.Thresholds.IdleDeferBelow != 15 || policy.Thresholds.LoadRecoverFrac != 0.7 {
		t.Errorf("thresholds = %+v, want the parsed values", policy.Thresholds)
	}
	if policy.Thresholds.RecoverSustain != time.Minute || policy.Thresholds.WSGrace != 20*time.Minute {
		t.Errorf("durations = %s / %s, want 1m0s / 20m0s", policy.Thresholds.RecoverSustain, policy.Thresholds.WSGrace)
	}
	if policy.Prebuilt.SafetyWindow != 45*time.Second {
		t.Errorf("safety window = %s, want 45s", policy.Prebuilt.SafetyWindow)
	}
	if policy.Slots.HeavyPerRepo != 2 {
		t.Errorf("heavy_per_repo = %d, want 2", policy.Slots.HeavyPerRepo)
	}
	if policy.Uncovered.Enabled {
		t.Error("uncovered stayed enabled, want the parsed false")
	}
	if policy.Uncovered.CPUPercent != 150 {
		t.Errorf("cpu_percent = %g, want 150", policy.Uncovered.CPUPercent)
	}
	if len(policy.Uncovered.IgnoreNames) != 2 || policy.Uncovered.IgnoreNames[0] != "node" || policy.Uncovered.IgnoreNames[1] != "rustc" {
		t.Errorf("ignore_names = %v, want [node rustc]", policy.Uncovered.IgnoreNames)
	}
}

func TestPolicyOverridesSetKeyRejectsUnknownAndMalformed(t *testing.T) {
	var overrides PolicyOverrides
	err := overrides.SetKey("thresholds.nonsense", "1")
	if err == nil || !strings.Contains(err.Error(), "admission.thresholds.nonsense") ||
		!strings.Contains(err.Error(), KeyRecoverSustain) {
		t.Fatalf("unknown key err = %v, want the key named and the known keys listed", err)
	}
	malformed := []struct{ key, value, want string }{
		{KeyRecoverSustain, "soon", "invalid duration"},
		{KeyHeavyPerRepo, "two", "whole number"},
		{KeyUncoveredEnabled, "maybe", "true or false"},
		{KeyIdleDeferBelow, "low", "not a number"},
	}
	for _, tc := range malformed {
		err := overrides.SetKey(tc.key, tc.value)
		if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), policyKeyPrefix+tc.key) {
			t.Errorf("SetKey(%q, %q) = %v, want %q and the key named", tc.key, tc.value, err, tc.want)
		}
	}
}

// AC2: ordering is checked per threshold block, on the layered values, because
// either side of a comparison can come from a different layer.
func TestValidatePolicyOrdering(t *testing.T) {
	cases := []struct {
		name string
		th   Thresholds
		pre  PrebuiltPolicy
		want string
	}{
		{
			"recover below defer",
			func() Thresholds { th := DefaultThresholds(); th.IdleRecoverAt = 10; return th }(),
			DefaultPrebuiltPolicy(),
			KeyIdleRecoverAt,
		},
		{
			"recover fraction above defer fraction",
			func() Thresholds { th := DefaultThresholds(); th.LoadRecoverFrac = 1.2; return th }(),
			DefaultPrebuiltPolicy(),
			KeyLoadRecoverFrac,
		},
		{
			"critical above warn",
			func() Thresholds { th := DefaultThresholds(); th.MemAvailCriticalPct = 20; return th }(),
			DefaultPrebuiltPolicy(),
			KeyMemAvailCriticalPct,
		},
		{
			"prebuilt recover below defer",
			DefaultThresholds(),
			func() PrebuiltPolicy { p := DefaultPrebuiltPolicy(); p.Thresholds.IdleRecoverAt = 10; return p }(),
			KeyPrebuiltIdleRecoverAt,
		},
		{
			"prebuilt recover fraction above defer fraction",
			DefaultThresholds(),
			func() PrebuiltPolicy { p := DefaultPrebuiltPolicy(); p.Thresholds.LoadRecoverFrac = 0.9; return p }(),
			KeyPrebuiltLoadRecoverFrac,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			policy := DefaultPolicy()
			policy.Thresholds = tc.th
			policy.Prebuilt = tc.pre
			err := validatePolicy(policy)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validatePolicy = %v, want an error naming %s", err, tc.want)
			}
		})
	}
	if err := validatePolicy(DefaultPolicy()); err != nil {
		t.Fatalf("policy defaults fail their own ordering check: %v", err)
	}
}

// A prebuilt block is its own set: raising the standard idle threshold must not
// move the prebuilt one, and vice versa.
func TestPrebuiltBlockIsIndependentOfThresholds(t *testing.T) {
	var overrides PolicyOverrides
	if err := overrides.SetKey(KeyIdleDeferBelow, "5"); err != nil {
		t.Fatal(err)
	}
	if err := overrides.SetKey(KeyPrebuiltIdleDeferBelow, "50"); err != nil {
		t.Fatal(err)
	}
	policy := overrides.apply(DefaultPolicy(), SourceConfig, nil)
	if policy.Thresholds.IdleDeferBelow != 5 || policy.Prebuilt.Thresholds.IdleDeferBelow != 50 {
		t.Fatalf("thresholds = %g prebuilt = %g, want 5 and 50",
			policy.Thresholds.IdleDeferBelow, policy.Prebuilt.Thresholds.IdleDeferBelow)
	}
	if policy.Prebuilt.Thresholds.IdleRecoverAt != 30 {
		t.Errorf("prebuilt idle_recover_at = %g, want its own default 30", policy.Prebuilt.Thresholds.IdleRecoverAt)
	}
}

func TestIgnoreSetParse(t *testing.T) {
	all, err := ParseIgnoreSet("all")
	if err != nil || !all.Pressure || !all.Slot || !all.Uncovered {
		t.Fatalf("ParseIgnoreSet(all) = %+v, %v", all, err)
	}
	partial, err := ParseIgnoreSet(" slot , pressure ")
	if err != nil || !partial.Pressure || !partial.Slot || partial.Uncovered {
		t.Fatalf("ParseIgnoreSet = %+v, %v, want pressure and slot", partial, err)
	}
	if got := partial.Label(); got != "pressure,slot" {
		t.Errorf("label = %q, want the canonical order pressure,slot", got)
	}
	if _, err := ParseIgnoreSet("slots"); err == nil || !strings.Contains(err.Error(), `"slots"`) {
		t.Fatalf("unknown rule err = %v, want the rule named", err)
	}
	if got := (IgnoreSet{Pressure: true}).Union(IgnoreSet{Uncovered: true}); got.Label() != "pressure,uncovered" {
		t.Errorf("union = %q, want pressure,uncovered", got.Label())
	}
	if (IgnoreSet{}).Any() {
		t.Error("empty set reported a bypass")
	}
}

func TestPolicyRowsCoverEveryKeyWithItsSource(t *testing.T) {
	var overrides PolicyOverrides
	if err := overrides.SetKey(KeyRecoverSustain, "1m"); err != nil {
		t.Fatal(err)
	}
	if err := overrides.SetKey(KeyHeavyPerRepo, "2"); err != nil {
		t.Fatal(err)
	}
	sources := map[string]string{}
	policy := overrides.apply(DefaultPolicy(), SourceConfig, sources)
	policy.Sources = sources
	rows := policy.Rows()
	if len(rows) != len(KeyOrder()) {
		t.Fatalf("rows = %d, want one per key (%d)", len(rows), len(KeyOrder()))
	}
	byKey := map[string]PolicyRow{}
	for _, row := range rows {
		byKey[row.Key] = row
	}
	if row := byKey[KeyRecoverSustain]; row.Value != "1m0s" || row.Source != SourceConfig {
		t.Errorf("recover_sustain row = %+v, want 1m0s (config)", row)
	}
	if row := byKey[KeyHeavyPerRepo]; row.Value != "2" || row.Source != SourceConfig {
		t.Errorf("heavy_per_repo row = %+v, want 2 (config)", row)
	}
	if row := byKey[KeyUncoveredEnabled]; row.Value != "true" || row.Source != SourceDefault {
		t.Errorf("uncovered.enabled row = %+v, want true (default)", row)
	}
	if row := byKey[KeyUncoveredIgnoreNames]; row.Value != "(none)" {
		t.Errorf("ignore_names row = %+v, want (none) for an unset list", row)
	}
	if row := byKey[KeyPrebuiltSafetyWindow]; row.Value != "30s" || row.Source != SourceDefault {
		t.Errorf("safety_window row = %+v, want 30s (default)", row)
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

	encoded, err := json.Marshal(PolicyOverrides{RecoverSustain: flexPtr(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"1m0s"`) {
		t.Errorf("json encode = %s, want the duration string", encoded)
	}
}

func TestUncoveredSettingsIgnoredAndCoverage(t *testing.T) {
	settings := UncoveredSettings{Enabled: true, CPUPercent: 100, IgnoreNames: []string{"node"}}
	if !settings.Ignored("node") || settings.Ignored("nodejs") {
		t.Error("Ignored must match the exact basename only")
	}
	if !settings.UnshapedCPU(100) || settings.UnshapedCPU(99) {
		t.Error("UnshapedCPU must count at or above the configured percent")
	}
	off := settings
	off.CPUPercent = 0
	if off.UnshapedCPU(400) {
		t.Error("cpu_percent 0 must turn the CPU-only rule off")
	}
	if got := (UncoveredSettings{Enabled: false}).Coverage(); !strings.Contains(got, "disabled") {
		t.Errorf("coverage = %q, want the disabled wording", got)
	}
}
