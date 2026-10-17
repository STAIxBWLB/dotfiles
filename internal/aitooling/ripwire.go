package aitooling

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/STAIxBWLB/dotfiles/internal/config"
)

// The release's native installer stages skills but does not activate any
// inferred target. Its own skills installer receives only selected roots.
func (e *Engine) ripwireSkills(ctx context.Context, s config.AIToolingConfig, op Operation) []ItemResult {
	source := filepath.Join(e.opts.HomeDir, ".local", "share", "ripwire", "skills")
	script := filepath.Join(source, "install.sh")
	var items []ItemResult
	for _, agent := range s.Agents {
		r := ItemResult{ID: "ripwire/" + agent, Kind: "integration"}
		dest := filepath.Join(e.profile(agent), "skills")
		if !pathExists(script) {
			r.Status = "deferred-prerequisite"
			r.Detail = "matching native ripwire bundled skills are not staged"
			items = append(items, r)
			continue
		}
		collision := false
		entries, _ := os.ReadDir(dest)
		for _, ent := range entries {
			if !strings.HasPrefix(ent.Name(), "ripwire-") {
				continue
			}
			p := filepath.Join(dest, ent.Name())
			target, err := filepath.EvalSymlinks(p)
			if err != nil || !strings.HasPrefix(target, source+string(os.PathSeparator)) {
				collision = true
				break
			}
		}
		if collision {
			r.Status = "deferred-skill-collision"
			r.Detail = "existing ripwire skill destination is owned by another source"
			items = append(items, r)
			continue
		}
		if op == Inspect {
			r.Status = "missing"
			if pathExists(filepath.Join(dest, "ripwire-router", "SKILL.md")) {
				r.Status = "installed"
			}
			items = append(items, r)
			continue
		}
		if e.opts.DryRun {
			r.Status = "planned"
			r.Detail = "native skills installer, explicit selected root; hooks disabled"
			items = append(items, r)
			continue
		}
		_, err := e.run(ctx, "/bin/bash", script, dest)
		if err != nil {
			r.Status = "failed"
			r.Detail = err.Error()
		} else if !pathExists(filepath.Join(dest, "ripwire-router", "SKILL.md")) {
			r.Status = "partial"
			r.Detail = "native skill installer returned, but router skill is missing"
		} else {
			r.Status = "installed"
			r.Detail = "bundled skills deployed to selected root; optional hooks disabled"
		}
		items = append(items, r)
	}
	return items
}
