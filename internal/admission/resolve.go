package admission

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Sources a resolved value can name.
const (
	SourceDefault = "default"
	SourceConfig  = "config"
	SourceRelax   = "relax"
)

// IgnoreEnv is the one-off bypass every admission caller honors, so a wrapped
// update (`dot ai update`, `dot update`, scheduled maintenance) can step past a
// rule for one run without writing state. Its value is the list `--ignore`
// takes: pressure, slot, uncovered, or all.
const IgnoreEnv = "DOT_ADMISSION_IGNORE"

// ResolveOptions selects the layers one evaluation reads. Precedence is
// fixed: one-off bypass (flag, then environment) > active relax override >
// config > policy defaults.
type ResolveOptions struct {
	// ConfigPath is the user config file (~/.config/dotfiles/config.yaml).
	// Empty skips the config layer, which is what non-user callers and tests
	// with no file want.
	ConfigPath string
	// Ignore is the one-off `dot admit --ignore` flag.
	Ignore IgnoreSet
	// KeepExpired leaves an expired relax override on disk. Status is a
	// read-only view and sets it; gate paths prune.
	KeepExpired bool
}

// Store.ResolvePolicy resolves the effective policy for this store: policy
// defaults, then the admission block of the user config, then the active relax
// override, and finally the one-off bypasses.
func (s *Store) ResolvePolicy(opts ResolveOptions) (Policy, error) {
	configOverrides, err := LoadPolicyOverrides(opts.ConfigPath)
	if err != nil {
		return Policy{}, err
	}
	// A config value that fails its own validation names the file as well as
	// the key: the key alone does not say which file to edit, and the same key
	// can also arrive from an override.
	if opts.ConfigPath != "" {
		if err := configOverrides.Validate(); err != nil {
			return Policy{}, fmt.Errorf("%s: %w", opts.ConfigPath, err)
		}
	}
	relax, err := s.LoadRelaxOverride(!opts.KeepExpired)
	if err != nil {
		return Policy{}, err
	}
	var relaxOverrides PolicyOverrides
	var relaxIgnore IgnoreSet
	if relax != nil {
		if relaxOverrides, err = relax.Overrides(); err != nil {
			return Policy{}, err
		}
		if relaxIgnore, err = relax.IgnoreSet(); err != nil {
			return Policy{}, err
		}
	}
	policy, sources, err := resolvePolicy(configOverrides, relaxOverrides)
	if err != nil {
		// With no override in play the config layer is the only source of an
		// ordering error, so the file is the actionable part of the message.
		if relax == nil && opts.ConfigPath != "" {
			return Policy{}, fmt.Errorf("%s: %w", opts.ConfigPath, err)
		}
		return Policy{}, err
	}
	policy.Sources = sources
	policy.Relax = relax

	// A bypass accumulates: a one-off flag narrows nothing the window already
	// relaxed, and both end with the run. The source label names the one-off
	// first, because that is the layer that ends soonest.
	flagLabel := ""
	if opts.Ignore.Any() {
		flagLabel = "--ignore " + opts.Ignore.Label()
	}
	envLabel := ""
	envIgnore := IgnoreSet{}
	if raw := strings.TrimSpace(os.Getenv(IgnoreEnv)); raw != "" {
		parsed, perr := ParseIgnoreSet(raw)
		if perr != nil {
			return Policy{}, fmt.Errorf("%s=%q: %w", IgnoreEnv, raw, perr)
		}
		envIgnore = parsed
		envLabel = IgnoreEnv + "=" + raw
	}
	policy.Ignore = opts.Ignore.Union(envIgnore).Union(relaxIgnore)
	switch {
	case flagLabel != "":
		policy.IgnoreSource = flagLabel
	case envLabel != "":
		policy.IgnoreSource = envLabel
	case relaxIgnore.Any():
		policy.IgnoreSource = fmt.Sprintf("dot admit relax until %s", relax.ExpiresAt.Format(time.RFC3339))
	}
	return policy, nil
}

// resolvePolicy layers the two override sets over the defaults and validates
// both the individual values and the final ordering. It is the one place the
// gate, `dot admit relax` (which validates before saving) and status use, so a
// saved override can never be one the gate would refuse.
func resolvePolicy(configOverrides, relaxOverrides PolicyOverrides) (Policy, map[string]string, error) {
	if err := configOverrides.Validate(); err != nil {
		return Policy{}, nil, err
	}
	if err := relaxOverrides.Validate(); err != nil {
		return Policy{}, nil, err
	}
	policy := DefaultPolicy()
	sources := make(map[string]string)
	policy = configOverrides.apply(policy, SourceConfig, sources)
	policy = relaxOverrides.apply(policy, SourceRelax, sources)
	if err := validatePolicy(policy); err != nil {
		return Policy{}, nil, err
	}
	return policy, sources, nil
}

// ValidateLayered checks a candidate override against the layer below it, so
// `dot admit relax` can refuse a value the gate itself would reject instead of
// saving an override that poisons every later evaluation.
func ValidateLayered(configOverrides, relaxOverrides PolicyOverrides) error {
	_, _, err := resolvePolicy(configOverrides, relaxOverrides)
	return err
}

// LoadPolicyOverrides reads the `admission` block of the user config. A
// missing file is no block. A read or parse failure is an error: a config the
// gate cannot read must never be treated as "defaults", because that silently
// changes the limits the person set.
//
// The block is read from the YAML tree rather than into a struct so every key
// goes through the same SetKey parser `--set` and the relax file use: one key
// table, one error naming the key, and an unknown key is refused instead of
// being dropped.
func LoadPolicyOverrides(path string) (PolicyOverrides, error) {
	if path == "" {
		return PolicyOverrides{}, nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return PolicyOverrides{}, nil
	}
	if err != nil {
		return PolicyOverrides{}, fmt.Errorf("reading the admission policy from %s: %w", path, err)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return PolicyOverrides{}, fmt.Errorf("parsing the admission policy from %s: %w", path, err)
	}
	overrides, err := policyOverridesFromNode(&root)
	if err != nil {
		return PolicyOverrides{}, fmt.Errorf("%s: %w", path, err)
	}
	return overrides, nil
}

func policyOverridesFromNode(root *yaml.Node) (PolicyOverrides, error) {
	var overrides PolicyOverrides
	node := root
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		node = node.Content[0]
	}
	if node.Kind != yaml.MappingNode {
		return overrides, nil // an empty file carries no block
	}
	admission := mappingValue(node, "admission")
	if admission == nil {
		return overrides, nil
	}
	if admission.Kind == yaml.ScalarNode && admission.Tag == "!!null" {
		return overrides, nil // a bare `admission:` with nothing under it
	}
	if admission.Kind != yaml.MappingNode {
		return overrides, fmt.Errorf("admission: must be a mapping of %s, %s, %s and %s",
			SectionThresholds, SectionPrebuilt, SectionSlots, SectionUncovered)
	}
	for i := 0; i+1 < len(admission.Content); i += 2 {
		section := admission.Content[i].Value
		block := admission.Content[i+1]
		if block.Kind == yaml.ScalarNode && block.Tag == "!!null" {
			continue
		}
		if block.Kind != yaml.MappingNode {
			return overrides, fmt.Errorf("%s%s: must be a mapping of keys", policyKeyPrefix, section)
		}
		for j := 0; j+1 < len(block.Content); j += 2 {
			key := section + "." + block.Content[j].Value
			value, err := policyScalar(block.Content[j+1])
			if err != nil {
				return overrides, fmt.Errorf("%s%s: %w", policyKeyPrefix, key, err)
			}
			if err := overrides.SetKey(key, value); err != nil {
				return overrides, err
			}
		}
	}
	return overrides, nil
}

// policyScalar renders one YAML leaf as the text SetKey parses. A sequence (the
// process-name list) is joined with the comma `--set` already uses.
func policyScalar(node *yaml.Node) (string, error) {
	switch node.Kind {
	case yaml.SequenceNode:
		parts := make([]string, 0, len(node.Content))
		for _, item := range node.Content {
			if item.Kind != yaml.ScalarNode {
				return "", fmt.Errorf("expected a list of names")
			}
			parts = append(parts, item.Value)
		}
		return strings.Join(parts, ","), nil
	case yaml.ScalarNode:
		if node.Tag == "!!null" {
			return "", fmt.Errorf("needs a value")
		}
		return node.Value, nil
	}
	return "", fmt.Errorf("expected a value")
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}
