package admission

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Threshold sources a resolved value can name.
const (
	SourceDefault = "default"
	SourceConfig  = "config"
	SourceRelax   = "relax"
)

// ResolveOptions selects the layers one gate evaluation reads. Precedence is
// fixed: one-off ignore (flag, then env) > active relax override > config >
// policy defaults.
type ResolveOptions struct {
	// ConfigPath is the user config file (~/.config/dotfiles/config.yaml).
	// Empty skips the config layer, which is what non-user callers and tests
	// with no file want.
	ConfigPath string
	// IgnorePressure is the one-off `dot admit --ignore-pressure` flag.
	IgnorePressure bool
	// KeepExpired leaves an expired relax override on disk. Status is a
	// read-only view and sets it; gate paths prune.
	KeepExpired bool
}

// ResolvedThresholds is the effective knob set plus where each layer came
// from. Sources holds only keys a non-default layer set, so a lookup that
// misses is a default.
type ResolvedThresholds struct {
	Thresholds     Thresholds
	IgnorePressure bool
	IgnoreSource   string // user-facing label; empty when nothing is ignored
	Relax          *RelaxOverride
	Sources        map[string]string
}

// Source names where one threshold key came from: default, config or relax.
func (r ResolvedThresholds) Source(key string) string {
	if source, ok := r.Sources[key]; ok {
		return source
	}
	return SourceDefault
}

// Rows returns the effective values in key order, each with its source.
func (r ResolvedThresholds) Rows() []ThresholdRow {
	return ThresholdRows(r.Thresholds, r.Source)
}

// Store.ResolveThresholds resolves the effective thresholds for this store:
// policy defaults, then the admission.thresholds block of the user config,
// then the active relax override, then the one-off ignore (flag, then env).
func (s *Store) ResolveThresholds(opts ResolveOptions) (ResolvedThresholds, error) {
	configOverrides, err := LoadConfigOverrides(opts.ConfigPath)
	if err != nil {
		return ResolvedThresholds{}, err
	}
	relax, err := s.LoadRelaxOverride(!opts.KeepExpired)
	if err != nil {
		return ResolvedThresholds{}, err
	}
	var relaxOverrides ThresholdOverrides
	relaxIgnore := false
	if relax != nil {
		relaxOverrides = relax.Thresholds
		relaxIgnore = relax.IgnorePressure
	}
	th, sources, err := resolveThresholds(configOverrides, relaxOverrides)
	if err != nil {
		return ResolvedThresholds{}, err
	}
	resolved := ResolvedThresholds{Thresholds: th, Relax: relax, Sources: sources}
	switch {
	case oneOffIgnore(opts.IgnorePressure):
		resolved.IgnorePressure = true
		resolved.IgnoreSource = ignoreLabel(opts.IgnorePressure)
	case relaxIgnore:
		resolved.IgnorePressure = true
		resolved.IgnoreSource = fmt.Sprintf("dot admit relax until %s", relax.ExpiresAt.Format("2006-01-02T15:04:05Z07:00"))
	}
	return resolved, nil
}

// resolveThresholds layers the two override sets over the defaults and
// validates both the individual values and the final ordering. It is the one
// place both the gate and `dot admit relax` (which validates before saving)
// use, so a saved override can never be one the gate would refuse.
func resolveThresholds(configOverrides, relaxOverrides ThresholdOverrides) (Thresholds, map[string]string, error) {
	if err := configOverrides.Validate(); err != nil {
		return Thresholds{}, nil, err
	}
	if err := relaxOverrides.Validate(); err != nil {
		return Thresholds{}, nil, err
	}
	th := DefaultThresholds()
	sources := make(map[string]string)
	th = configOverrides.apply(th, SourceConfig, sources)
	th = relaxOverrides.apply(th, SourceRelax, sources)
	if err := validateThresholds(th); err != nil {
		return Thresholds{}, nil, err
	}
	return th, sources, nil
}

// ValidateLayered checks a candidate override against the layer below it, so
// `dot admit relax` can refuse a value the gate itself would reject instead of
// saving an override that poisons every later evaluation.
func ValidateLayered(configOverrides, relaxOverrides ThresholdOverrides) error {
	_, _, err := resolveThresholds(configOverrides, relaxOverrides)
	return err
}

// admissionConfigFile is the slice of the user config file the gate reads.
// Unknown keys are ignored by yaml, so this never needs to track the rest of
// the state schema.
type admissionConfigFile struct {
	Admission struct {
		Thresholds ThresholdOverrides `yaml:"thresholds"`
	} `yaml:"admission"`
}

// LoadConfigOverrides reads the admission.thresholds block. A missing file is
// no block. A read or parse failure is an error: a config the gate cannot read
// must never be treated as "defaults", because that silently changes the
// limits the person set.
func LoadConfigOverrides(path string) (ThresholdOverrides, error) {
	if path == "" {
		return ThresholdOverrides{}, nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ThresholdOverrides{}, nil
	}
	if err != nil {
		return ThresholdOverrides{}, fmt.Errorf("reading admission thresholds from %s: %w", path, err)
	}
	var file admissionConfigFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		return ThresholdOverrides{}, fmt.Errorf("parsing admission thresholds from %s: %w", path, err)
	}
	return file.Admission.Thresholds, nil
}

// oneOffIgnore reports whether a one-off bypass is active: the explicit flag
// first, then the environment variable.
func oneOffIgnore(flag bool) bool {
	return flag || truthy(os.Getenv(IgnorePressureEnv))
}

// ignoreLabel names the active one-off source for the printed bypass line.
func ignoreLabel(flag bool) string {
	if flag {
		return "--ignore-pressure"
	}
	return IgnorePressureEnv + "=" + strings.TrimSpace(os.Getenv(IgnorePressureEnv))
}

func truthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// ApplyIgnore rewrites a pressure defer as an admitted decision, keeping the
// reasons it bypassed for the one-line stderr note. The history the evaluation
// produced is preserved untouched, so an ignored evaluation still samples and
// keeps the recovery streak continuous. Lock contention is not pressure and is
// left as the defer it is, to be retried inside the wait budget.
func ApplyIgnore(d Decision, source string) Decision {
	if d.Admit || IsGateBusy(d) {
		return d
	}
	d.IgnoredReasons = append([]string(nil), d.Reasons...)
	d.IgnoreSource = source
	d.PressureIgnored = true
	d.Admit = true
	d.Reasons = nil
	d.RetryAfter = 0
	d.Recovering = false
	d.ProfileRetry = false
	return d
}
