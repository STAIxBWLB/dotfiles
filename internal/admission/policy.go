package admission

import (
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Admission policy keys, written under the `admission:` block of config.yaml
// and taken by `dot admit relax --set`. One key table serves the config file,
// the relax override file and the flags, so a key cannot mean one thing in one
// place and another in the next.
const (
	KeyIdleDeferBelow          = "thresholds.idle_defer_below"
	KeyIdleRecoverAt           = "thresholds.idle_recover_at"
	KeyLoadDeferFrac           = "thresholds.load_defer_frac"
	KeyLoadRecoverFrac         = "thresholds.load_recover_frac"
	KeyDeferSustain            = "thresholds.defer_sustain"
	KeyRecoverSustain          = "thresholds.recover_sustain"
	KeyWSGrace                 = "thresholds.ws_grace"
	KeyMemAvailWarnPct         = "thresholds.mem_avail_warn_pct"
	KeyMemAvailCriticalPct     = "thresholds.mem_avail_critical_pct"
	KeyPrebuiltIdleDeferBelow  = "prebuilt.idle_defer_below"
	KeyPrebuiltIdleRecoverAt   = "prebuilt.idle_recover_at"
	KeyPrebuiltLoadDeferFrac   = "prebuilt.load_defer_frac"
	KeyPrebuiltLoadRecoverFrac = "prebuilt.load_recover_frac"
	KeyPrebuiltSafetyWindow    = "prebuilt.safety_window"
	KeyHeavyPerRepo            = "slots.heavy_per_repo"
	KeyMaintenanceSlots        = "slots.maintenance"
	KeyUncoveredEnabled        = "uncovered.enabled"
	KeyUncoveredCPUPercent     = "uncovered.cpu_percent"
	KeyUncoveredIgnoreNames    = "uncovered.ignore_names"
)

// Policy key prefix and section names: every validation error names the path a
// person edits, not a Go field.
const (
	policyKeyPrefix   = "admission."
	SectionThresholds = "thresholds"
	SectionPrebuilt   = "prebuilt"
	SectionSlots      = "slots"
	SectionUncovered  = "uncovered"
)

// Slot capacity bounds. An upper bound is a ceiling because each concurrent
// lease is another heavy job on the host; docs/CEILINGS.md records it.
const (
	DefaultHeavyPerRepo     = 1
	DefaultMaintenanceSlots = 1
	MaxSlotsPerScope        = 8
	// MaxOverflowSlots bounds how far a slot bypass reaches past capacity: a
	// bypass admits next to a full scope, it does not remove the limit.
	MaxOverflowSlots = 8
)

// Uncovered-scan defaults. CPUPercent 0 turns the CPU-only rule off.
const DefaultUncoveredCPUPercent = 100

// KeyOrder lists every policy key in reporting order: thresholds, prebuilt,
// slots, uncovered.
func KeyOrder() []string {
	return []string{
		KeyIdleDeferBelow, KeyIdleRecoverAt, KeyLoadDeferFrac, KeyLoadRecoverFrac,
		KeyDeferSustain, KeyRecoverSustain, KeyWSGrace,
		KeyMemAvailWarnPct, KeyMemAvailCriticalPct,
		KeyPrebuiltIdleDeferBelow, KeyPrebuiltIdleRecoverAt,
		KeyPrebuiltLoadDeferFrac, KeyPrebuiltLoadRecoverFrac, KeyPrebuiltSafetyWindow,
		KeyHeavyPerRepo, KeyMaintenanceSlots,
		KeyUncoveredEnabled, KeyUncoveredCPUPercent, KeyUncoveredIgnoreNames,
	}
}

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
	parsed, err := secondsToDuration(seconds)
	if err != nil {
		return err
	}
	*d = parsed
	return nil
}

// MarshalJSON emits the Go duration string ("5m0s").
func (d FlexDuration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// flexSecondsLimit is the largest count of seconds whose product with
// time.Second still fits an int64. One past it the multiplication wraps:
// -18446744073 seconds becomes a positive ~0.71s, which then passes every
// "not negative" check downstream.
const flexSecondsLimit = int64(math.MaxInt64) / int64(time.Second)

// secondsToDuration converts a count of seconds, refusing one whose
// multiplication by a second would overflow. Both integer decode paths (the
// YAML/JSON scalar and the JSON number) share it so neither can drift.
func secondsToDuration(seconds int64) (FlexDuration, error) {
	if seconds < -flexSecondsLimit || seconds > flexSecondsLimit {
		return 0, fmt.Errorf("invalid duration %d seconds: outside +/-%d seconds, the largest count that fits; pass a duration string like \"1m\"",
			seconds, flexSecondsLimit)
	}
	return FlexDuration(time.Duration(seconds) * time.Second), nil
}

func parseFlexDuration(text string) (FlexDuration, error) {
	if seconds, err := strconv.ParseInt(text, 10, 64); err == nil {
		return secondsToDuration(seconds)
	}
	parsed, err := time.ParseDuration(text)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", text, err)
	}
	return FlexDuration(parsed), nil
}

// PolicyOverrides is one layer of policy settings, flat over every key so the
// key table has a single home. Every field is a pointer: an unset key keeps
// the layer below it, and a meaningful zero (`defer_sustain: 0`,
// `uncovered.cpu_percent: 0`) stays distinct from "unset".
type PolicyOverrides struct {
	IdleDeferBelow          *float64
	IdleRecoverAt           *float64
	LoadDeferFrac           *float64
	LoadRecoverFrac         *float64
	DeferSustain            *FlexDuration
	RecoverSustain          *FlexDuration
	WSGrace                 *FlexDuration
	MemAvailWarnPct         *float64
	MemAvailCriticalPct     *float64
	PrebuiltIdleDeferBelow  *float64
	PrebuiltIdleRecoverAt   *float64
	PrebuiltLoadDeferFrac   *float64
	PrebuiltLoadRecoverFrac *float64
	PrebuiltSafetyWindow    *FlexDuration
	HeavyPerRepo            *int
	MaintenanceSlots        *int
	UncoveredEnabled        *bool
	UncoveredCPUPercent     *float64
	UncoveredIgnoreNames    *[]string
}

// IsZero reports whether this layer sets nothing.
func (o PolicyOverrides) IsZero() bool {
	return o.IdleDeferBelow == nil && o.IdleRecoverAt == nil &&
		o.LoadDeferFrac == nil && o.LoadRecoverFrac == nil &&
		o.DeferSustain == nil && o.RecoverSustain == nil && o.WSGrace == nil &&
		o.MemAvailWarnPct == nil && o.MemAvailCriticalPct == nil &&
		o.PrebuiltIdleDeferBelow == nil && o.PrebuiltIdleRecoverAt == nil &&
		o.PrebuiltLoadDeferFrac == nil && o.PrebuiltLoadRecoverFrac == nil &&
		o.PrebuiltSafetyWindow == nil &&
		o.HeavyPerRepo == nil && o.MaintenanceSlots == nil &&
		o.UncoveredEnabled == nil && o.UncoveredCPUPercent == nil &&
		o.UncoveredIgnoreNames == nil
}

// SetKey sets one key from its written form — the config file's scalar text,
// a `--set key=value` argument, or the relax override's stored strings. An
// unknown key is an error naming it: a typo must not silently do nothing.
// Range and ordering checks run in Validate on the layered result.
func (o *PolicyOverrides) SetKey(key, value string) error {
	key = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(key), policyKeyPrefix))
	value = strings.TrimSpace(value)
	fail := func(err error) error { return fmt.Errorf("%s%s: %w", policyKeyPrefix, key, err) }
	setFloat := func(dst **float64) error {
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fail(fmt.Errorf("%q is not a number", value))
		}
		// ParseFloat accepts "NaN" and "Inf"; neither is a threshold, and NaN
		// compares false against every bound, so it would slip past each range
		// check and disable the rule it was meant to set.
		if math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return fail(fmt.Errorf("%q is not a finite number", value))
		}
		*dst = &parsed
		return nil
	}
	setDuration := func(dst **FlexDuration) error {
		parsed, err := parseFlexDuration(value)
		if err != nil {
			return fail(err)
		}
		*dst = &parsed
		return nil
	}
	setInt := func(dst **int) error {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return fail(fmt.Errorf("%q is not a whole number", value))
		}
		*dst = &parsed
		return nil
	}
	setBool := func(dst **bool) error {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return fail(fmt.Errorf("%q is not true or false", value))
		}
		*dst = &parsed
		return nil
	}
	setNames := func(dst **[]string) error {
		names := []string{}
		for _, name := range strings.Split(value, ",") {
			if name = strings.TrimSpace(name); name != "" {
				names = append(names, name)
			}
		}
		*dst = &names
		return nil
	}
	switch key {
	case KeyIdleDeferBelow:
		return setFloat(&o.IdleDeferBelow)
	case KeyIdleRecoverAt:
		return setFloat(&o.IdleRecoverAt)
	case KeyLoadDeferFrac:
		return setFloat(&o.LoadDeferFrac)
	case KeyLoadRecoverFrac:
		return setFloat(&o.LoadRecoverFrac)
	case KeyDeferSustain:
		return setDuration(&o.DeferSustain)
	case KeyRecoverSustain:
		return setDuration(&o.RecoverSustain)
	case KeyWSGrace:
		return setDuration(&o.WSGrace)
	case KeyMemAvailWarnPct:
		return setFloat(&o.MemAvailWarnPct)
	case KeyMemAvailCriticalPct:
		return setFloat(&o.MemAvailCriticalPct)
	case KeyPrebuiltIdleDeferBelow:
		return setFloat(&o.PrebuiltIdleDeferBelow)
	case KeyPrebuiltIdleRecoverAt:
		return setFloat(&o.PrebuiltIdleRecoverAt)
	case KeyPrebuiltLoadDeferFrac:
		return setFloat(&o.PrebuiltLoadDeferFrac)
	case KeyPrebuiltLoadRecoverFrac:
		return setFloat(&o.PrebuiltLoadRecoverFrac)
	case KeyPrebuiltSafetyWindow:
		return setDuration(&o.PrebuiltSafetyWindow)
	case KeyHeavyPerRepo:
		return setInt(&o.HeavyPerRepo)
	case KeyMaintenanceSlots:
		return setInt(&o.MaintenanceSlots)
	case KeyUncoveredEnabled:
		return setBool(&o.UncoveredEnabled)
	case KeyUncoveredCPUPercent:
		return setFloat(&o.UncoveredCPUPercent)
	case KeyUncoveredIgnoreNames:
		return setNames(&o.UncoveredIgnoreNames)
	}
	return fmt.Errorf("unknown admission policy key %q (known keys: %s)",
		policyKeyPrefix+key, strings.Join(KeyOrder(), ", "))
}

// apply layers the set keys over p and records their source, when a map is
// supplied. An empty source is for callers that only want the values.
func (o PolicyOverrides) apply(p Policy, source string, sources map[string]string) Policy {
	set := func(key string) {
		if sources != nil && source != "" {
			sources[key] = source
		}
	}
	if o.IdleDeferBelow != nil {
		p.Thresholds.IdleDeferBelow = *o.IdleDeferBelow
		set(KeyIdleDeferBelow)
	}
	if o.IdleRecoverAt != nil {
		p.Thresholds.IdleRecoverAt = *o.IdleRecoverAt
		set(KeyIdleRecoverAt)
	}
	if o.LoadDeferFrac != nil {
		p.Thresholds.LoadDeferFrac = *o.LoadDeferFrac
		set(KeyLoadDeferFrac)
	}
	if o.LoadRecoverFrac != nil {
		p.Thresholds.LoadRecoverFrac = *o.LoadRecoverFrac
		set(KeyLoadRecoverFrac)
	}
	if o.DeferSustain != nil {
		p.Thresholds.DeferSustain = o.DeferSustain.Std()
		set(KeyDeferSustain)
	}
	if o.RecoverSustain != nil {
		p.Thresholds.RecoverSustain = o.RecoverSustain.Std()
		set(KeyRecoverSustain)
	}
	if o.WSGrace != nil {
		p.Thresholds.WSGrace = o.WSGrace.Std()
		set(KeyWSGrace)
	}
	if o.MemAvailWarnPct != nil {
		p.Thresholds.MemAvailWarnPct = *o.MemAvailWarnPct
		set(KeyMemAvailWarnPct)
	}
	if o.MemAvailCriticalPct != nil {
		p.Thresholds.MemAvailCriticalPct = *o.MemAvailCriticalPct
		set(KeyMemAvailCriticalPct)
	}
	if o.PrebuiltIdleDeferBelow != nil {
		p.Prebuilt.Thresholds.IdleDeferBelow = *o.PrebuiltIdleDeferBelow
		set(KeyPrebuiltIdleDeferBelow)
	}
	if o.PrebuiltIdleRecoverAt != nil {
		p.Prebuilt.Thresholds.IdleRecoverAt = *o.PrebuiltIdleRecoverAt
		set(KeyPrebuiltIdleRecoverAt)
	}
	if o.PrebuiltLoadDeferFrac != nil {
		p.Prebuilt.Thresholds.LoadDeferFrac = *o.PrebuiltLoadDeferFrac
		set(KeyPrebuiltLoadDeferFrac)
	}
	if o.PrebuiltLoadRecoverFrac != nil {
		p.Prebuilt.Thresholds.LoadRecoverFrac = *o.PrebuiltLoadRecoverFrac
		set(KeyPrebuiltLoadRecoverFrac)
	}
	if o.PrebuiltSafetyWindow != nil {
		p.Prebuilt.SafetyWindow = o.PrebuiltSafetyWindow.Std()
		set(KeyPrebuiltSafetyWindow)
	}
	if o.HeavyPerRepo != nil {
		p.Slots.HeavyPerRepo = *o.HeavyPerRepo
		set(KeyHeavyPerRepo)
	}
	if o.MaintenanceSlots != nil {
		p.Slots.Maintenance = *o.MaintenanceSlots
		set(KeyMaintenanceSlots)
	}
	if o.UncoveredEnabled != nil {
		p.Uncovered.Enabled = *o.UncoveredEnabled
		set(KeyUncoveredEnabled)
	}
	if o.UncoveredCPUPercent != nil {
		p.Uncovered.CPUPercent = *o.UncoveredCPUPercent
		set(KeyUncoveredCPUPercent)
	}
	if o.UncoveredIgnoreNames != nil {
		p.Uncovered.IgnoreNames = append([]string(nil), (*o.UncoveredIgnoreNames)...)
		set(KeyUncoveredIgnoreNames)
	}
	return p
}

// Validate checks the values this layer sets on their own: percents within
// 0-100, load fractions above zero, durations at or above zero, slot counts
// within 1-8, a CPU percent at or above zero, and process names bare.
// Ordering between keys is checked on the layered result
// (validatePolicy), because either side of a comparison can come from a
// different layer.
func (o PolicyOverrides) Validate() error {
	percents := []struct {
		key   string
		value *float64
	}{
		{KeyIdleDeferBelow, o.IdleDeferBelow},
		{KeyIdleRecoverAt, o.IdleRecoverAt},
		{KeyMemAvailWarnPct, o.MemAvailWarnPct},
		{KeyMemAvailCriticalPct, o.MemAvailCriticalPct},
		{KeyPrebuiltIdleDeferBelow, o.PrebuiltIdleDeferBelow},
		{KeyPrebuiltIdleRecoverAt, o.PrebuiltIdleRecoverAt},
	}
	for _, p := range percents {
		if p.value == nil {
			continue
		}
		if *p.value < 0 || *p.value > 100 {
			return fmt.Errorf("%s%s: %g is outside 0-100", policyKeyPrefix, p.key, *p.value)
		}
	}
	fractions := []struct {
		key   string
		value *float64
	}{
		{KeyLoadDeferFrac, o.LoadDeferFrac},
		{KeyLoadRecoverFrac, o.LoadRecoverFrac},
		{KeyPrebuiltLoadDeferFrac, o.PrebuiltLoadDeferFrac},
		{KeyPrebuiltLoadRecoverFrac, o.PrebuiltLoadRecoverFrac},
	}
	for _, f := range fractions {
		if f.value == nil {
			continue
		}
		if *f.value <= 0 {
			return fmt.Errorf("%s%s: %g must be greater than 0", policyKeyPrefix, f.key, *f.value)
		}
	}
	durations := []struct {
		key   string
		value *FlexDuration
	}{
		{KeyDeferSustain, o.DeferSustain},
		{KeyRecoverSustain, o.RecoverSustain},
		{KeyWSGrace, o.WSGrace},
		{KeyPrebuiltSafetyWindow, o.PrebuiltSafetyWindow},
	}
	for _, d := range durations {
		if d.value == nil {
			continue
		}
		if d.value.Std() < 0 {
			return fmt.Errorf("%s%s: %s must not be negative", policyKeyPrefix, d.key, d.value.Std())
		}
	}
	slots := []struct {
		key   string
		value *int
	}{
		{KeyHeavyPerRepo, o.HeavyPerRepo},
		{KeyMaintenanceSlots, o.MaintenanceSlots},
	}
	for _, s := range slots {
		if s.value == nil {
			continue
		}
		if *s.value < 1 || *s.value > MaxSlotsPerScope {
			return fmt.Errorf("%s%s: %d is outside 1-%d", policyKeyPrefix, s.key, *s.value, MaxSlotsPerScope)
		}
	}
	if o.UncoveredCPUPercent != nil && *o.UncoveredCPUPercent < 0 {
		return fmt.Errorf("%s%s: %g must not be negative", policyKeyPrefix, KeyUncoveredCPUPercent, *o.UncoveredCPUPercent)
	}
	if o.UncoveredIgnoreNames != nil {
		for _, name := range *o.UncoveredIgnoreNames {
			if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
				return fmt.Errorf("%s%s: %q is not a bare process name", policyKeyPrefix, KeyUncoveredIgnoreNames, name)
			}
		}
	}
	return nil
}

// validatePolicy checks the ordering constraints a set of values must satisfy
// once every layer has been applied: the recovery side is never stricter than
// the defer side, in each threshold block independently.
func validatePolicy(p Policy) error {
	checks := []struct {
		recoverKey, deferKey     string
		recoverValue, deferValue float64
		// atMost: the recovery value must not exceed the defer value (a
		// fraction or a percentage). Otherwise it must not fall below it (an
		// idle percentage: recovering needs at least the idle the defer did).
		atMost bool
	}{
		{KeyIdleRecoverAt, KeyIdleDeferBelow, p.Thresholds.IdleRecoverAt, p.Thresholds.IdleDeferBelow, false},
		{KeyLoadRecoverFrac, KeyLoadDeferFrac, p.Thresholds.LoadRecoverFrac, p.Thresholds.LoadDeferFrac, true},
		{KeyMemAvailCriticalPct, KeyMemAvailWarnPct, p.Thresholds.MemAvailCriticalPct, p.Thresholds.MemAvailWarnPct, true},
		{KeyPrebuiltIdleRecoverAt, KeyPrebuiltIdleDeferBelow, p.Prebuilt.Thresholds.IdleRecoverAt, p.Prebuilt.Thresholds.IdleDeferBelow, false},
		{KeyPrebuiltLoadRecoverFrac, KeyPrebuiltLoadDeferFrac, p.Prebuilt.Thresholds.LoadRecoverFrac, p.Prebuilt.Thresholds.LoadDeferFrac, true},
	}
	for _, c := range checks {
		bad, relation := c.recoverValue < c.deferValue, "at least"
		if c.atMost {
			bad, relation = c.recoverValue > c.deferValue, "at most"
		}
		if bad {
			return fmt.Errorf("%s%s (%g) must be %s %s%s (%g)",
				policyKeyPrefix, c.recoverKey, c.recoverValue, relation,
				policyKeyPrefix, c.deferKey, c.deferValue)
		}
	}
	return nil
}

// Policy is the effective admission policy: every limit the controller
// enforces, plus the one-off bypasses and where each value came from.
type Policy struct {
	Thresholds Thresholds
	Prebuilt   PrebuiltPolicy
	Slots      SlotCapacity
	Uncovered  UncoveredSettings
	// Ignore holds the active bypasses, from the one-off flag, the
	// environment and any active relax override.
	Ignore       IgnoreSet
	IgnoreSource string
	// Relax is the active temporary override, nil when none is in effect.
	Relax *RelaxOverride
	// Sources holds only keys a non-default layer set, so a lookup that misses
	// is a default.
	Sources map[string]string
}

// DefaultPolicy is the policy a host with no config and no override runs:
// exactly the values the controller hard-coded before they were configurable.
func DefaultPolicy() Policy {
	return Policy{
		Thresholds: DefaultThresholds(),
		Prebuilt:   DefaultPrebuiltPolicy(),
		Slots:      SlotCapacity{HeavyPerRepo: DefaultHeavyPerRepo, Maintenance: DefaultMaintenanceSlots},
		Uncovered:  DefaultUncoveredSettings(),
	}
}

// Source names where one policy key came from: default, config or relax.
func (p Policy) Source(key string) string {
	if source, ok := p.Sources[key]; ok {
		return source
	}
	return SourceDefault
}

// Rows returns the effective value of every key in reporting order, each with
// its source.
func (p Policy) Rows() []PolicyRow {
	rows := make([]PolicyRow, 0, len(KeyOrder()))
	for _, key := range KeyOrder() {
		rows = append(rows, PolicyRow{Key: key, Value: p.value(key), Source: p.Source(key)})
	}
	return rows
}

func (p Policy) value(key string) string {
	percent := func(v float64) string { return fmt.Sprintf("%g%%", v) }
	fraction := func(v float64) string { return fmt.Sprintf("%g x CPUs", v) }
	switch key {
	case KeyIdleDeferBelow:
		return percent(p.Thresholds.IdleDeferBelow)
	case KeyIdleRecoverAt:
		return percent(p.Thresholds.IdleRecoverAt)
	case KeyLoadDeferFrac:
		return fraction(p.Thresholds.LoadDeferFrac)
	case KeyLoadRecoverFrac:
		return fraction(p.Thresholds.LoadRecoverFrac)
	case KeyDeferSustain:
		return p.Thresholds.DeferSustain.String()
	case KeyRecoverSustain:
		return p.Thresholds.RecoverSustain.String()
	case KeyWSGrace:
		return p.Thresholds.WSGrace.String()
	case KeyMemAvailWarnPct:
		return percent(p.Thresholds.MemAvailWarnPct)
	case KeyMemAvailCriticalPct:
		return percent(p.Thresholds.MemAvailCriticalPct)
	case KeyPrebuiltIdleDeferBelow:
		return percent(p.Prebuilt.Thresholds.IdleDeferBelow)
	case KeyPrebuiltIdleRecoverAt:
		return percent(p.Prebuilt.Thresholds.IdleRecoverAt)
	case KeyPrebuiltLoadDeferFrac:
		return fraction(p.Prebuilt.Thresholds.LoadDeferFrac)
	case KeyPrebuiltLoadRecoverFrac:
		return fraction(p.Prebuilt.Thresholds.LoadRecoverFrac)
	case KeyPrebuiltSafetyWindow:
		return p.Prebuilt.SafetyWindow.String()
	case KeyHeavyPerRepo:
		return strconv.Itoa(p.Slots.HeavyPerRepo)
	case KeyMaintenanceSlots:
		return strconv.Itoa(p.Slots.Maintenance)
	case KeyUncoveredEnabled:
		return strconv.FormatBool(p.Uncovered.Enabled)
	case KeyUncoveredCPUPercent:
		return percent(p.Uncovered.CPUPercent)
	case KeyUncoveredIgnoreNames:
		if len(p.Uncovered.IgnoreNames) == 0 {
			return "(none)"
		}
		return strings.Join(p.Uncovered.IgnoreNames, ",")
	}
	return ""
}

// PolicyRow is one effective policy value as status reports it.
type PolicyRow struct {
	Key    string
	Value  string
	Source string // default / config / relax
}

// SlotCapacity is how many concurrent leases each class allows per scope.
type SlotCapacity struct {
	HeavyPerRepo int
	Maintenance  int
}

// For returns the capacity of one class.
func (c SlotCapacity) For(class string) int {
	if class == ClassMaintenance {
		if c.Maintenance > 0 {
			return c.Maintenance
		}
		return DefaultMaintenanceSlots
	}
	if c.HeavyPerRepo > 0 {
		return c.HeavyPerRepo
	}
	return DefaultHeavyPerRepo
}

// UncoveredSettings is the uncovered heavy-work scan's configuration.
type UncoveredSettings struct {
	Enabled     bool
	CPUPercent  float64
	IgnoreNames []string
}

// DefaultUncoveredSettings is today's scan: enabled, any single process at
// 100% of one core counts as CPU-only heavy work, and no name is exempt.
func DefaultUncoveredSettings() UncoveredSettings {
	return UncoveredSettings{Enabled: true, CPUPercent: DefaultUncoveredCPUPercent}
}

// Ignored reports whether name is exempt from the scan (exact basename).
func (u UncoveredSettings) Ignored(name string) bool {
	for _, ignored := range u.IgnoreNames {
		if ignored == name {
			return true
		}
	}
	return false
}

// Ignore rules a one-off bypass can name.
const (
	RulePressure  = "pressure"
	RuleSlot      = "slot"
	RuleUncovered = "uncovered"
	// RuleRemoteLoad is the remote host's own load probe (#421). It is its own
	// rule because that load says nothing about this machine: a person may
	// decide to run on a busy host without relaxing anything local.
	RuleRemoteLoad = "remote-load"
)

// IgnoreSet is the set of rules one caller steps past. Each rule keeps its own
// semantics: pressure still samples and records, slot still takes a lease,
// uncovered still scans and names what it found, and remote-load still probes
// the host and names the load it found.
type IgnoreSet struct {
	Pressure   bool
	Slot       bool
	Uncovered  bool
	RemoteLoad bool
}

// ParseIgnoreSet parses "pressure,slot,uncovered,remote-load" (any order) or
// "all". Every name is checked wherever it appears: "all" expands to the four
// rules and the scan continues, so "all,slots" fails the same way "slots,all"
// does.
func ParseIgnoreSet(spec string) (IgnoreSet, error) {
	var set IgnoreSet
	for _, part := range strings.Split(spec, ",") {
		switch strings.TrimSpace(part) {
		case "":
			continue
		case "all":
			set.Pressure, set.Slot, set.Uncovered, set.RemoteLoad = true, true, true, true
		case RulePressure:
			set.Pressure = true
		case RuleSlot:
			set.Slot = true
		case RuleUncovered:
			set.Uncovered = true
		case RuleRemoteLoad:
			set.RemoteLoad = true
		default:
			return IgnoreSet{}, fmt.Errorf("unknown ignore rule %q: use pressure, slot, uncovered, remote-load or all", strings.TrimSpace(part))
		}
	}
	return set, nil
}

// Any reports whether any rule is bypassed.
func (s IgnoreSet) Any() bool { return s.Pressure || s.Slot || s.Uncovered || s.RemoteLoad }

// Union returns the rules either set bypasses.
func (s IgnoreSet) Union(other IgnoreSet) IgnoreSet {
	return IgnoreSet{
		Pressure:   s.Pressure || other.Pressure,
		Slot:       s.Slot || other.Slot,
		Uncovered:  s.Uncovered || other.Uncovered,
		RemoteLoad: s.RemoteLoad || other.RemoteLoad,
	}
}

// Strings lists the bypassed rules in canonical order, for JSON and messages.
func (s IgnoreSet) Strings() []string {
	var out []string
	if s.Pressure {
		out = append(out, RulePressure)
	}
	if s.Slot {
		out = append(out, RuleSlot)
	}
	if s.Uncovered {
		out = append(out, RuleUncovered)
	}
	if s.RemoteLoad {
		out = append(out, RuleRemoteLoad)
	}
	return out
}

// Label renders the rules the way a flag or the environment names them.
func (s IgnoreSet) Label() string { return strings.Join(s.Strings(), ",") }

// ApplyIgnore rewrites a pressure defer as an admitted decision, keeping the
// reasons it bypassed for the one-line stderr note and naming the rules that
// were stepped past. The history the evaluation produced is preserved
// untouched, so a bypassed evaluation still samples and keeps the recovery
// streak continuous. Lock contention is not pressure and is left as the defer
// it is, to be retried inside the wait budget.
func ApplyIgnore(d Decision, ignore IgnoreSet, source string) Decision {
	if d.Admit || IsGateBusy(d) || !ignore.Pressure {
		return d
	}
	d.IgnoredReasons = append([]string(nil), d.Reasons...)
	d.IgnoreSource = source
	d.Ignored = ignore.Strings()
	d.PressureIgnored = true
	d.Admit = true
	d.Reasons = nil
	d.RetryAfter = 0
	d.Recovering = false
	d.ProfileRetry = false
	return d
}
