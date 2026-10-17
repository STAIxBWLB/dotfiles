package cli

import (
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/syncer"
)

func TestApplyLocalConfigPreviewProjectsPendingChanges(t *testing.T) {
	cfg := &syncer.Config{
		Owner:       "old",
		Target:      syncer.Target{Kind: syncer.TargetLocal, Path: "/old/"},
		MirrorPath:  "/old/",
		FilterMode:  syncer.FilterModeInclude,
		Propagation: syncer.DefaultPropagationPolicy(),
		MaxDelete:   25,
		Interval:    60,
	}
	local := &syncer.LocalConfig{
		Target:       "ssh:user@peer:/work",
		Owner:        "new",
		FilterMode:   syncer.FilterModeExclude,
		Propagation:  syncer.PropagationPolicy{Create: true, Update: true, Delete: true},
		MaxDelete:    0,
		Interval:     600,
		PullInterval: 900,
		PushMode:     syncer.ModeForce,
		PullMode:     syncer.ModeClean,
	}
	// The home only matters for a local target's ~ expansion; this row's
	// target is an ssh spec, so any home leaves the projection unchanged.
	if err := applyLocalConfigPreview(cfg, local, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if cfg.Target.Kind != syncer.TargetSSH || cfg.MirrorPath != "" || cfg.Owner != "new" {
		t.Fatalf("target/owner preview not applied: %+v", cfg)
	}
	if cfg.FilterMode != syncer.FilterModeExclude || !cfg.Propagation.Delete {
		t.Fatalf("filter/propagation preview not applied: %+v", cfg)
	}
	if cfg.MaxDelete <= 0 || cfg.MaxDelete == 25 {
		t.Fatalf("max-delete zero sentinel not resolved to default: %d", cfg.MaxDelete)
	}
	if cfg.Interval != 600 || cfg.PullInterval != 900 || cfg.PushMode != syncer.ModeForce {
		t.Fatalf("schedule preview not applied: %+v", cfg)
	}
}
