// Package aitooling reconciles explicitly selected agent installations. Native
// providers own their files; this package never copies generic skill trees.
package aitooling

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/STAIxBWLB/dotfiles/internal/config"
)

type Entry struct{ ID, Kind, Name, Binary string }

func Catalog() []Entry {
	return []Entry{
		{"claude", "agent", "Claude Code", "claude"}, {"codex", "agent", "Codex", "codex"},
		{"kimi", "agent", "Kimi Code", "kimi"}, {"qwen", "agent", "Qwen Code", "qwen"},
		{"grok", "agent", "Grok", "grok"}, {"opencode", "agent", "OpenCode", "opencode"},
		{"gencode", "agent", "Genspark Gencode", "gencode"}, {"pi", "agent", "pi", "pi"},
		{"antigravity", "agent", "Antigravity CLI", "agy"},
		{"ripwire", "tool", "ripwire", "ripwire"}, {"ocr", "tool", "Open Code Review", "ocr"},
		{"gsd", "tool", "GSD", "gsd"}, {"ponytail", "tool", "ponytail", ""},
	}
}

var stableVersion = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+$`)
var versionInOutput = regexp.MustCompile(`\b[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9.]+)?\b`)

func ValidateSelection(s config.AIToolingConfig) error {
	for kind, ids := range map[string][]string{"agent": s.Agents, "tool": s.Tools} {
		seen := map[string]bool{}
		for _, id := range ids {
			valid := false
			for _, e := range Catalog() {
				if e.ID == id && e.Kind == kind {
					valid = true
				}
			}
			if !valid {
				return fmt.Errorf("unsupported %s %q; configure supported selections with dot ai setup", kind, id)
			}
			if seen[id] {
				return fmt.Errorf("duplicate %s %q", kind, id)
			}
			seen[id] = true
		}
	}
	seenSkills := map[string]bool{}
	for _, skill := range s.Skills {
		if strings.TrimSpace(skill) != skill || skill == "" || strings.Contains(skill, ",") || seenSkills[skill] {
			return fmt.Errorf("invalid or duplicate selected skill %q", skill)
		}
		seenSkills[skill] = true
	}
	for id, version := range s.Pins {
		if !slices.Contains(s.Agents, id) && !slices.Contains(s.Tools, id) && id != "gsd-pi" {
			return fmt.Errorf("pin %q is not selected", id)
		}
		if id == "gsd-pi" && !slices.Contains(s.Tools, "gsd") {
			return fmt.Errorf("gsd-pi pin requires gsd")
		}
		if !stableVersion.MatchString(version) {
			spec := binarySpecs()[id]
			if !spec.allowSuffix || versionInOutput.FindString(version) != version {
				return fmt.Errorf("pin for %s must be a stable X.Y.Z version", id)
			}
		}
	}
	return nil
}
