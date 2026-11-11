package admission

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

// Threshold keys, in reporting order. Each name is the key under
// admission.thresholds in the user config, the JSON field of a relax
// override, and the suffix of the matching `dot admit relax` flag.
const (
	KeyIdleDeferBelow      = "idle_defer_below"
	KeyIdleRecoverAt       = "idle_recover_at"
	KeyLoadDeferFrac       = "load_defer_frac"
	KeyLoadRecoverFrac     = "load_recover_frac"
	KeyDeferSustain        = "defer_sustain"
	KeyRecoverSustain      = "recover_sustain"
	KeyWSGrace             = "ws_grace"
	KeyMemAvailWarnPct     = "mem_avail_warn_pct"
	KeyMemAvailCriticalPct = "mem_avail_critical_pct"
)

// thresholdKeyPrefix is what every validation error names: the config path a
// person edits, not a Go field.
const thresholdKeyPrefix = "admission.thresholds."

// FlexDuration decodes a Go duration string ("1m", "90s") or a plain integer
// count of seconds, from YAML and JSON alike, and always writes the Go
// duration string back. Thresholds are written by hand in config.yaml, so
// integer nanoseconds (yaml.v3's default for time.Duration) would be a trap.
type FlexDuration time.Duration

// Std returns the value as a time.Duration.
func (d FlexDuration) Std() time.Duration { return time.Duration(d) }

// UnmarshalYAML decodes a duration string or an integer count of seconds.
func (d *FlexDuration) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.ScalarNode {
		return fmt.Errorf("invalid duration: expected a scalar like \"1m\"")
	}
	parsed, err := parseFlexDuration(value.Value)
	if err != nil {
		return err
	}
	*d = parsed
	return nil
}

// MarshalYAML emits the Go duration string ("5m0s").
func (d FlexDuration) MarshalYAML() (any, error) {
	return time.Duration(d).String(), nil
}

// UnmarshalJSON decodes a duration string or an integer count of seconds.
func (d *FlexDuration) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		parsed, err := parseFlexDuration(text)
		if err != nil {
			return err
		}
		*d = parsed
		return nil
	}
	var seconds int64
	if err := json.Unmarshal(data, &seconds); err != nil {
		return fmt.Errorf("invalid duration %s: expected a string like \"1m\" or a count of seconds", string(data))
	}
	*d = FlexDuration(time.Duration(seconds) * time.Second)
	return nil
}

// MarshalJSON emits the Go duration string ("5m0s").
func (d FlexDuration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func parseFlexDuration(text string) (FlexDuration, error) {
	if seconds, err := strconv.ParseInt(text, 10, 64); err == nil {
		return FlexDuration(time.Duration(seconds) * time.Second), nil
	}
	parsed, err := time.ParseDuration(text)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", text, err)
	}
	return FlexDuration(parsed), nil
}

// ThresholdOverrides is the optional admission.thresholds block. Every field
// is a pointer so an unset key keeps the layer below it; a pointer also keeps
// a meaningful zero (defer_sustain: 0 defers at once) distinct from unset.
type ThresholdOverrides struct {
	IdleDeferBelow      *float64      `yaml:"idle_defer_below,omitempty" json:"idle_defer_below,omitempty"`
	IdleRecoverAt       *float64      `yaml:"idle_recover_at,omitempty" json:"idle_recover_at,omitempty"`
	LoadDeferFrac       *float64      `yaml:"load_defer_frac,omitempty" json:"load_defer_frac,omitempty"`
	LoadRecoverFrac     *float64      `yaml:"load_recover_frac,omitempty" json:"load_recover_frac,omitempty"`
	DeferSustain        *FlexDuration `yaml:"defer_sustain,omitempty" json:"defer_sustain,omitempty"`
	RecoverSustain      *FlexDuration `yaml:"recover_sustain,omitempty" json:"recover_sustain,omitempty"`
	WSGrace             *FlexDuration `yaml:"ws_grace,omitempty" json:"ws_grace,omitempty"`
	MemAvailWarnPct     *float64      `yaml:"mem_avail_warn_pct,omitempty" json:"mem_avail_warn_pct,omitempty"`
	MemAvailCriticalPct *float64      `yaml:"mem_avail_critical_pct,omitempty" json:"mem_avail_critical_pct,omitempty"`
}

// IsZero reports whether the block sets nothing.
func (o ThresholdOverrides) IsZero() bool {
	return o.IdleDeferBelow == nil && o.IdleRecoverAt == nil &&
		o.LoadDeferFrac == nil && o.LoadRecoverFrac == nil &&
		o.DeferSustain == nil && o.RecoverSustain == nil && o.WSGrace == nil &&
		o.MemAvailWarnPct == nil && o.MemAvailCriticalPct == nil
}

// apply layers the set keys over th and records their source, when a map is
// supplied. An empty source is for callers that only want the values.
func (o ThresholdOverrides) apply(th Thresholds, source string, sources map[string]string) Thresholds {
	set := func(key string) {
		if sources != nil && source != "" {
			sources[key] = source
		}
	}
	if o.IdleDeferBelow != nil {
		th.IdleDeferBelow = *o.IdleDeferBelow
		set(KeyIdleDeferBelow)
	}
	if o.IdleRecoverAt != nil {
		th.IdleRecoverAt = *o.IdleRecoverAt
		set(KeyIdleRecoverAt)
	}
	if o.LoadDeferFrac != nil {
		th.LoadDeferFrac = *o.LoadDeferFrac
		set(KeyLoadDeferFrac)
	}
	if o.LoadRecoverFrac != nil {
		th.LoadRecoverFrac = *o.LoadRecoverFrac
		set(KeyLoadRecoverFrac)
	}
	if o.DeferSustain != nil {
		th.DeferSustain = o.DeferSustain.Std()
		set(KeyDeferSustain)
	}
	if o.RecoverSustain != nil {
		th.RecoverSustain = o.RecoverSustain.Std()
		set(KeyRecoverSustain)
	}
	if o.WSGrace != nil {
		th.WSGrace = o.WSGrace.Std()
		set(KeyWSGrace)
	}
	if o.MemAvailWarnPct != nil {
		th.MemAvailWarnPct = *o.MemAvailWarnPct
		set(KeyMemAvailWarnPct)
	}
	if o.MemAvailCriticalPct != nil {
		th.MemAvailCriticalPct = *o.MemAvailCriticalPct
		set(KeyMemAvailCriticalPct)
	}
	return th
}

// Validate checks the values this block sets on their own: percents within
// 0-100, load fractions above zero, durations at or above zero. Ordering
// between keys is checked on the layered result (validateThresholds), because
// either side of a comparison can come from a different layer.
func (o ThresholdOverrides) Validate() error {
	percents := []struct {
		key   string
		value *float64
	}{
		{KeyIdleDeferBelow, o.IdleDeferBelow},
		{KeyIdleRecoverAt, o.IdleRecoverAt},
		{KeyMemAvailWarnPct, o.MemAvailWarnPct},
		{KeyMemAvailCriticalPct, o.MemAvailCriticalPct},
	}
	for _, p := range percents {
		if p.value == nil {
			continue
		}
		if *p.value < 0 || *p.value > 100 {
			return fmt.Errorf("%s%s: %g is outside 0-100", thresholdKeyPrefix, p.key, *p.value)
		}
	}
	fractions := []struct {
		key   string
		value *float64
	}{
		{KeyLoadDeferFrac, o.LoadDeferFrac},
		{KeyLoadRecoverFrac, o.LoadRecoverFrac},
	}
	for _, f := range fractions {
		if f.value == nil {
			continue
		}
		if *f.value <= 0 {
			return fmt.Errorf("%s%s: %g must be greater than 0", thresholdKeyPrefix, f.key, *f.value)
		}
	}
	durations := []struct {
		key   string
		value *FlexDuration
	}{
		{KeyDeferSustain, o.DeferSustain},
		{KeyRecoverSustain, o.RecoverSustain},
		{KeyWSGrace, o.WSGrace},
	}
	for _, d := range durations {
		if d.value == nil {
			continue
		}
		if d.value.Std() < 0 {
			return fmt.Errorf("%s%s: %s must not be negative", thresholdKeyPrefix, d.key, d.value.Std())
		}
	}
	return nil
}

// validateThresholds checks the ordering constraints on a fully layered set.
func validateThresholds(th Thresholds) error {
	if th.IdleRecoverAt < th.IdleDeferBelow {
		return fmt.Errorf("%s%s (%g) must be at least %s%s (%g)",
			thresholdKeyPrefix, KeyIdleRecoverAt, th.IdleRecoverAt, thresholdKeyPrefix, KeyIdleDeferBelow, th.IdleDeferBelow)
	}
	if th.LoadRecoverFrac > th.LoadDeferFrac {
		return fmt.Errorf("%s%s (%g) must be at most %s%s (%g)",
			thresholdKeyPrefix, KeyLoadRecoverFrac, th.LoadRecoverFrac, thresholdKeyPrefix, KeyLoadDeferFrac, th.LoadDeferFrac)
	}
	if th.MemAvailCriticalPct > th.MemAvailWarnPct {
		return fmt.Errorf("%s%s (%g) must be at most %s%s (%g)",
			thresholdKeyPrefix, KeyMemAvailCriticalPct, th.MemAvailCriticalPct, thresholdKeyPrefix, KeyMemAvailWarnPct, th.MemAvailWarnPct)
	}
	return nil
}

// ThresholdRow is one effective threshold as status reports it.
type ThresholdRow struct {
	Key    string
	Value  string
	Source string // default / config / relax
}

// ThresholdRows returns the effective values in key order, paired with the
// layer each value came from.
func ThresholdRows(th Thresholds, source func(string) string) []ThresholdRow {
	return []ThresholdRow{
		{KeyIdleDeferBelow, fmt.Sprintf("%g%%", th.IdleDeferBelow), source(KeyIdleDeferBelow)},
		{KeyIdleRecoverAt, fmt.Sprintf("%g%%", th.IdleRecoverAt), source(KeyIdleRecoverAt)},
		{KeyLoadDeferFrac, fmt.Sprintf("%g x CPUs", th.LoadDeferFrac), source(KeyLoadDeferFrac)},
		{KeyLoadRecoverFrac, fmt.Sprintf("%g x CPUs", th.LoadRecoverFrac), source(KeyLoadRecoverFrac)},
		{KeyDeferSustain, th.DeferSustain.String(), source(KeyDeferSustain)},
		{KeyRecoverSustain, th.RecoverSustain.String(), source(KeyRecoverSustain)},
		{KeyWSGrace, th.WSGrace.String(), source(KeyWSGrace)},
		{KeyMemAvailWarnPct, fmt.Sprintf("%g%%", th.MemAvailWarnPct), source(KeyMemAvailWarnPct)},
		{KeyMemAvailCriticalPct, fmt.Sprintf("%g%%", th.MemAvailCriticalPct), source(KeyMemAvailCriticalPct)},
	}
}
