package syncer

import (
	"fmt"
	"os"
	"strings"

	"github.com/STAIxBWLB/dotfiles/internal/template"
)

// FilterLayer is one row of the effective filter chain, in evaluation order,
// for `dot sync filters show`.
type FilterLayer struct {
	Name   string
	Detail []string
}

// FilterReport renders the effective filter chain in first-match-wins order.
// Read-only: nothing is materialized or migrated.
func FilterReport(cfg *Config) ([]FilterLayer, error) {
	local := strings.TrimRight(cfg.LocalPath, "/")

	countPatterns := func(path string) int {
		patterns, err := loadPatternFileOrDefault(path, func() ([]string, error) { return nil, nil })
		if err != nil {
			return 0
		}
		return len(patterns)
	}

	alwaysOn := []string{"/.dotfiles/", "/inbox/gdrive/", ".git (any depth, dir or gitlink)"}
	for _, root := range worktreeRoots {
		alwaysOn = append(alwaysOn, root+" (any depth, linked worktrees)")
	}
	layers := []FilterLayer{
		{Name: "1. always-on excludes", Detail: alwaysOn},
	}

	// Honor the profile's policy, not just the repo's submodule list. Reporting
	// "excluded here" while the transfer actually includes them makes an operator
	// distrust the config - and the peer profile deliberately carries submodule
	// working trees, because uncommitted work inside one is exactly what Git has
	// not seen.
	subs := gitSubmodulePaths(local)
	subDetail := []string{"(none)"}
	switch {
	case cfg.IncludeSubmodules:
		subDetail = []string{fmt.Sprintf(
			"include_submodules: %d submodule working tree(s) ARE carried (not excluded)", len(subs))}
	case len(subs) > 0:
		subDetail = append([]string{fmt.Sprintf("%d submodules — synced via Git, excluded here:", len(subs))}, subs...)
	}
	layers = append(layers, FilterLayer{Name: "2. submodule excludes (submodules.dyn.conf)", Detail: subDetail})

	allowDetail := []string{"(empty — secrets stay excluded)"}
	var allows []string
	for _, p := range cfg.AllowPatterns {
		p = strings.TrimSpace(p)
		if p != "" && !strings.HasPrefix(p, "#") {
			allows = append(allows, p)
		}
	}
	if len(allows) > 0 {
		allowDetail = append([]string{fmt.Sprintf("WARNING: %d secret pattern(s) sync to the target:", len(allows))}, allows...)
	}
	layers = append(layers,
		FilterLayer{Name: "3. allow.txt re-includes (secrets opt-in)", Detail: allowDetail},
		FilterLayer{Name: "4. secrets deny-by-default (built-in)", Detail: append(append([]string{}, secretExcludePatterns...), "(re-included: "+strings.Join(secretAllowBuiltins, ", ")+")")},
		FilterLayer{Name: "5. exclude.txt (junk)", Detail: []string{fmt.Sprintf("%d patterns — %s", countPatterns(cfg.ExcludesFile), cfg.ExcludesFile)}},
		FilterLayer{Name: "6. ignore.txt (operator)", Detail: []string{fmt.Sprintf("%d patterns — %s", countPatterns(cfg.IgnoreFile), cfg.IgnoreFile)}},
	)

	shared, err := ScanShared(strings.TrimRight(cfg.MirrorPath, "/"), cfg.SharedExcludes)
	if err != nil {
		return nil, err
	}
	sharedDetail := []string{"(none)"}
	if len(shared) > 0 {
		sharedDetail = sharedDetail[:0]
		for _, e := range shared {
			sharedDetail = append(sharedDetail, e.RelPath)
		}
	}
	layers = append(layers, FilterLayer{Name: "7. shared-folder excludes", Detail: sharedDetail})

	if normalizeFilterMode(cfg.FilterMode) == FilterModeInclude {
		trackedCount := len(gitTrackedForSync(local))
		baselineCount := 0
		if cfg.LocalPaths != nil {
			if baseline, err := LoadBaselineManifest(cfg.LocalPaths.BaselineFile); err == nil {
				baselineCount = len(baseline)
			}
		}
		layers = append(layers,
			FilterLayer{Name: "8. directory traversal", Detail: []string{"--include=*/"}},
			FilterLayer{Name: "9. tracked ∪ baseline includes (tracked-includes.dyn.conf)", Detail: []string{fmt.Sprintf("%d git-tracked + %d baseline entries", trackedCount, baselineCount)}},
			FilterLayer{Name: "10. binary allowlist (include.txt)", Detail: []string{fmt.Sprintf("%d patterns, case-insensitive — %s", countPatterns(cfg.IncludeFile), cfg.IncludeFile)}},
			FilterLayer{Name: "11. catch-all", Detail: []string{"--exclude=* (anything unanticipated does NOT sync)"}},
		)
	} else {
		layers = append(layers, FilterLayer{Name: "8. exclude-mode tail", Detail: []string{"everything not excluded above syncs"}})
	}
	return layers, nil
}

// ResetFilterFiles regenerates exclude.txt and include.txt from the embedded
// templates, backing up any existing files to <name>.bak-<timestamp>.
// ignore.txt and allow.txt are operator-owned and never touched.
func ResetFilterFiles(paths *LocalPaths) ([]string, error) {
	engine := template.NewEngine()
	var backups []string
	for _, ent := range []struct {
		path, tmpl string
	}{
		{paths.ExcludeFile, excludesTemplatePath},
		{paths.IncludeFile, includesTemplatePath},
	} {
		body, err := engine.ReadStatic(ent.tmpl)
		if err != nil {
			return backups, fmt.Errorf("reading embedded %s: %w", ent.tmpl, err)
		}
		if _, err := os.Stat(ent.path); err == nil {
			bak := ent.path + ".bak-" + newTimestamp()
			if err := os.Rename(ent.path, bak); err != nil {
				return backups, fmt.Errorf("backing up %s: %w", ent.path, err)
			}
			backups = append(backups, bak)
		}
		if err := os.MkdirAll(paths.StoreDir, 0755); err != nil {
			return backups, err
		}
		if err := os.WriteFile(ent.path, body, 0644); err != nil {
			return backups, fmt.Errorf("writing %s: %w", ent.path, err)
		}
	}
	return backups, nil
}
