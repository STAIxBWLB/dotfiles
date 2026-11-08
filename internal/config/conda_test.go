package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCondaStateToggle(t *testing.T) {
	off, on := false, true
	for _, tc := range []struct {
		name  string
		state *bool
		want  bool
	}{
		{"unset keeps the profile default", nil, true},
		{"false turns it off", &off, false},
		{"true keeps it on", &on, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			in := &UserState{Name: "T", Profile: "full"}
			in.Modules.Conda.Enabled = tc.state
			if err := SaveStateForHome(home, in); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(home, ".config", "dotfiles", "config.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(string(data), "conda:"); got != (tc.state != nil) {
				t.Fatalf("conda key written = %v, want %v:\n%s", got, tc.state != nil, data)
			}
			out, err := LoadStateForHome(home)
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := Load("full", "", &SystemInfo{OS: "darwin"})
			if err != nil {
				t.Fatal(err)
			}
			if !cfg.IsModuleEnabled("conda") {
				t.Fatal("full profile no longer enables conda; this test assumes it does")
			}
			ApplyStateToConfig(cfg, out)
			if got := cfg.IsModuleEnabled("conda"); got != tc.want {
				t.Fatalf("conda enabled = %v, want %v", got, tc.want)
			}
		})
	}
}
