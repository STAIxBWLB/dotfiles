package watchdog

import (
	"strings"
	"testing"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/config"
)

func TestResolveReaper_Defaults(t *testing.T) {
	s, err := ResolveReaper(config.WatchdogReaperConfig{})
	if err != nil {
		t.Fatalf("ResolveReaper: %v", err)
	}
	if s.Mode != ModeDryRun {
		t.Errorf("default mode = %q, want dry-run", s.Mode)
	}
	if s.Interval != DefaultInterval || s.Sustain != DefaultSustain || s.KillGrace != DefaultKillGrace {
		t.Errorf("defaults not applied: %#v", s)
	}
	if s.CPUThreshold != DefaultCPUThreshold {
		t.Errorf("default threshold = %v", s.CPUThreshold)
	}
}

func TestResolveReaper_RejectsUnknownMode(t *testing.T) {
	_, err := ResolveReaper(config.WatchdogReaperConfig{Mode: "napalm"})
	if err == nil {
		t.Fatal("unknown mode must be rejected")
	}
}

func TestResolveReaper_KeepsExplicitValues(t *testing.T) {
	s, err := ResolveReaper(config.WatchdogReaperConfig{
		Mode:         "enforce",
		Interval:     config.Duration(120 * time.Second),
		CPUThreshold: 80,
		Sustain:      config.Duration(10 * time.Minute),
		KillGrace:    config.Duration(3 * time.Second),
		Match:        config.WatchdogMatchConfig{OrphanOnly: true, Args: []string{"bun"}},
		Allow:        []string{"cloudflared"},
	})
	if err != nil {
		t.Fatalf("ResolveReaper: %v", err)
	}
	if s.Mode != ModeEnforce || s.Interval != 120*time.Second || s.CPUThreshold != 80 ||
		s.Sustain != 10*time.Minute || s.KillGrace != 3*time.Second {
		t.Errorf("explicit values lost: %#v", s)
	}
	if !s.OrphanOnly || len(s.Args) != 1 || len(s.Allow) != 1 {
		t.Errorf("match/allow not carried: %#v", s)
	}
}

func TestResolveWarp_Defaults(t *testing.T) {
	s := ResolveWarp(config.WatchdogWarpConfig{})
	if s.Interval != DefaultWarpInterval || s.FailThreshold != DefaultWarpFailThreshold {
		t.Errorf("defaults = %#v, want 120s/2", s)
	}
}

func TestResolveWarp_KeepsExplicitValues(t *testing.T) {
	s := ResolveWarp(config.WatchdogWarpConfig{
		Interval:      config.Duration(60 * time.Second),
		FailThreshold: 4,
	})
	if s.Interval != 60*time.Second || s.FailThreshold != 4 {
		t.Errorf("explicit values lost: %#v", s)
	}
}

func TestResolveMonit_Defaults(t *testing.T) {
	s, err := ResolveMonit(config.WatchdogMonitConfig{Enabled: true}, 12)
	if err != nil {
		t.Fatalf("ResolveMonit: %v", err)
	}
	if s.Load1Threshold != 12 {
		t.Errorf("default load1 threshold = %v, want the injected logical CPU count 12", s.Load1Threshold)
	}
	if s.CPUUserThreshold != DefaultMonitCPUUserThreshold || s.CPUSystemThreshold != DefaultMonitCPUSystemThreshold {
		t.Errorf("default cpu thresholds = %#v", s)
	}
	if s.Cycles != DefaultMonitCycles {
		t.Errorf("default cycles = %d", s.Cycles)
	}
	if !s.ScreenSharing {
		t.Error("screensharing must default to on")
	}
}

func TestResolveMonit_KeepsExplicitValues(t *testing.T) {
	off := false
	s, err := ResolveMonit(config.WatchdogMonitConfig{
		Enabled: true, Load1Threshold: 6.5, CPUUserThreshold: 70, CPUSystemThreshold: 30, Cycles: 5, ScreenSharing: &off,
	}, 12)
	if err != nil {
		t.Fatalf("ResolveMonit: %v", err)
	}
	if s.Load1Threshold != 6.5 || s.CPUUserThreshold != 70 || s.CPUSystemThreshold != 30 || s.Cycles != 5 {
		t.Errorf("explicit values lost: %#v", s)
	}
	if s.ScreenSharing {
		t.Error("explicit screensharing: false must survive the true default")
	}
}

func TestResolveMonit_Validation(t *testing.T) {
	if _, err := ResolveMonit(config.WatchdogMonitConfig{CPUUserThreshold: 101}, 8); err == nil {
		t.Fatal("cpu user threshold above 100% must be rejected")
	}
	if _, err := ResolveMonit(config.WatchdogMonitConfig{CPUSystemThreshold: 150}, 8); err == nil {
		t.Fatal("cpu system threshold above 100% must be rejected")
	}
	if _, err := ResolveMonit(config.WatchdogMonitConfig{Cycles: MonitCyclesMax + 1}, 8); err == nil {
		t.Fatal("cycles above monit's parser limit must be rejected")
	}
	if _, err := ResolveMonit(config.WatchdogMonitConfig{}, 0); err == nil {
		t.Fatal("an unset load threshold with no logical CPU count must fail")
	}
}

// Only zero means unset: a negative knob is a config mistake and must fail,
// not silently resolve to the default.
func TestResolveMonit_RejectsNegatives(t *testing.T) {
	for name, cfg := range map[string]config.WatchdogMonitConfig{
		"load1":      {Load1Threshold: -1},
		"cpu user":   {CPUUserThreshold: -5},
		"cpu system": {CPUSystemThreshold: -0.5},
		"cycles":     {Cycles: -1},
	} {
		if _, err := ResolveMonit(cfg, 8); err == nil {
			t.Errorf("%s: a negative value must be rejected", name)
		}
	}
}

func TestResolveBeszel_DefaultsAndHomeExpansion(t *testing.T) {
	s, err := ResolveBeszel(config.WatchdogBeszelConfig{HubURL: "https://hub.example"}, "/home/u")
	if err != nil {
		t.Fatalf("ResolveBeszel: %v", err)
	}
	if s.Listen != DefaultBeszelListen {
		t.Errorf("default listen = %q, want %q", s.Listen, DefaultBeszelListen)
	}
	if s.EnvPath != "/home/u/.config/beszel/agent.env" {
		t.Errorf("default env path = %q, want the home-expanded default", s.EnvPath)
	}
	if s.HubURL != "https://hub.example" {
		t.Errorf("hub url = %q", s.HubURL)
	}
}

func TestResolveBeszel_RequiresHubURLAtSetup(t *testing.T) {
	s, err := ResolveBeszel(config.WatchdogBeszelConfig{Enabled: true}, "/home/u")
	if err == nil {
		t.Fatal("an empty hub_url must fail validation")
	}
	if !strings.Contains(err.Error(), "hub_url") || !strings.Contains(err.Error(), "dot watchdog setup") {
		t.Errorf("validation error must name hub_url and setup: %v", err)
	}
	// The resolved defaults still come back alongside the error so status can
	// report the env path without a hub configured.
	if s.EnvPath != "/home/u/.config/beszel/agent.env" {
		t.Errorf("env path alongside the error = %q", s.EnvPath)
	}
}

func TestResolveBeszel_KeepsExplicitValues(t *testing.T) {
	s, err := ResolveBeszel(config.WatchdogBeszelConfig{
		HubURL:  "https://hub.example",
		Listen:  ":9999",
		EnvPath: "/var/lib/beszel/agent.env",
	}, "/home/u")
	if err != nil {
		t.Fatalf("ResolveBeszel: %v", err)
	}
	if s.Listen != ":9999" || s.EnvPath != "/var/lib/beszel/agent.env" {
		t.Errorf("explicit values lost: %#v", s)
	}
}

// A relative env_path would resolve against launchd's cwd (not the user's
// home) when the plist sources it; ResolveBeszel pins it against the manager
// home so BeszelEnvRef always renders an absolute-or-$HOME path.
func TestResolveBeszel_RelativeEnvPathIsAbsolutized(t *testing.T) {
	s, err := ResolveBeszel(config.WatchdogBeszelConfig{
		HubURL:  "https://hub.example",
		EnvPath: ".config/beszel/custom.env",
	}, "/home/u")
	if err != nil {
		t.Fatalf("ResolveBeszel: %v", err)
	}
	if s.EnvPath != "/home/u/.config/beszel/custom.env" {
		t.Errorf("relative env path = %q, want it joined against the home", s.EnvPath)
	}
	m := NewManager(nil, "/home/u")
	if ref := m.BeszelEnvRef(s.EnvPath); ref != "$HOME/.config/beszel/custom.env" {
		t.Errorf("absolutized env ref = %q, want $HOME-relative", ref)
	}
}
