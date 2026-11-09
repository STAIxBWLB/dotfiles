// Package catalog exposes the embedded macOS application catalog used by the
// `apps` subcommands and the init wizard.
package catalog

import (
	_ "embed"
	"fmt"

	"gopkg.in/yaml.v3"
)

//go:embed macos-apps.yaml
var macOSAppsYAML []byte

// MacApp describes a single cask entry in the catalog.
type MacApp struct {
	Token string `yaml:"token"`
	Name  string `yaml:"name"`
	Tap   string `yaml:"tap,omitempty"`
	App   string `yaml:"app,omitempty"`
}

// MacAppGroup groups related casks under a human-readable label.
type MacAppGroup struct {
	Name string   `yaml:"name"`
	Apps []MacApp `yaml:"apps"`
}

// MacApps is the root of the catalog.
type MacApps struct {
	Defaults    []string      `yaml:"defaults"`
	Recommended []string      `yaml:"recommended"`
	Groups      []MacAppGroup `yaml:"groups"`
}

// LoadMacApps parses the embedded macOS app catalog.
func LoadMacApps() (*MacApps, error) {
	var c MacApps
	if err := yaml.Unmarshal(macOSAppsYAML, &c); err != nil {
		return nil, fmt.Errorf("parse macos-apps.yaml: %w", err)
	}
	return &c, nil
}

// AllTokens returns every token across all groups in definition order.
// Duplicates are filtered (first occurrence wins).
func (c *MacApps) AllTokens() []string {
	seen := make(map[string]bool)
	var out []string
	for _, g := range c.Groups {
		for _, a := range g.Apps {
			if seen[a.Token] {
				continue
			}
			seen[a.Token] = true
			out = append(out, a.Token)
		}
	}
	return out
}

// DisplayName looks up a human-readable name for a token. Falls back to the
// token itself if not found.
func (c *MacApps) DisplayName(token string) string {
	for _, g := range c.Groups {
		for _, a := range g.Apps {
			if a.Token == token {
				return a.Name
			}
		}
	}
	return token
}

// AppBundle returns the application bundle installed under /Applications for
// casks that need an explicit external-install check.
func (c *MacApps) AppBundle(token string) string {
	for _, g := range c.Groups {
		for _, a := range g.Apps {
			if a.Token == token {
				return a.App
			}
		}
	}
	return ""
}

// NormalizeCaskTokens de-duplicates cask tokens, preserving order. Retired
// tokens are rewritten when the state file loads (config.rewriteRetiredValues).
func NormalizeCaskTokens(tokens []string) []string {
	seen := make(map[string]bool, len(tokens))
	var out []string
	for _, token := range tokens {
		if token == "" || seen[token] {
			continue
		}
		seen[token] = true
		out = append(out, token)
	}
	return out
}

// TapsForTokens returns the de-duplicated Homebrew taps required by the given
// cask tokens, preserving the input token order.
func (c *MacApps) TapsForTokens(tokens []string) []string {
	byToken := make(map[string]string)
	for _, g := range c.Groups {
		for _, a := range g.Apps {
			if a.Tap != "" {
				byToken[a.Token] = a.Tap
			}
		}
	}

	seen := make(map[string]bool)
	var out []string
	for _, token := range tokens {
		tap := byToken[token]
		if tap == "" || seen[tap] {
			continue
		}
		seen[tap] = true
		out = append(out, tap)
	}
	return out
}
