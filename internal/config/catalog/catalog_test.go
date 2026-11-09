package catalog

import "testing"

func TestLoadMacApps(t *testing.T) {
	c, err := LoadMacApps()
	if err != nil {
		t.Fatalf("LoadMacApps: %v", err)
	}
	if len(c.Defaults) == 0 {
		t.Fatal("defaults empty")
	}
	if len(c.Groups) == 0 {
		t.Fatal("groups empty")
	}
	// Every default must appear somewhere in a group
	present := make(map[string]bool)
	for _, g := range c.Groups {
		for _, a := range g.Apps {
			if a.Token == "" {
				t.Errorf("group %q has entry with empty token", g.Name)
			}
			if a.Name == "" {
				t.Errorf("%q: empty display name", a.Token)
			}
			present[a.Token] = true
		}
	}
	for _, d := range c.Defaults {
		if !present[d] {
			t.Errorf("default %q not defined in any group", d)
		}
	}
	if len(c.Recommended) == 0 {
		t.Fatal("recommended empty")
	}
	recommended := make(map[string]bool, len(c.Recommended))
	for _, r := range c.Recommended {
		if !present[r] {
			t.Errorf("recommended %q not defined in any group", r)
		}
		recommended[r] = true
	}
	for _, d := range c.Defaults {
		if !recommended[d] {
			t.Errorf("default %q missing from recommended (defaults must be a subset)", d)
		}
	}
	if !recommended["maru-workspace"] {
		t.Error("maru-workspace should be a recommended app")
	}
	if !recommended["orca"] {
		t.Error("orca should be a recommended app")
	}
	if recommended["warp"] {
		t.Error("warp should remain selectable but should not be recommended by default")
	}
	taps := c.TapsForTokens([]string{"cmux", "maru-workspace", "orca", "wave", "cmux", "orca"})
	wantTaps := []string{"manaflow-ai/cmux", "staixbwlb/cask", "stablyai/orca"}
	if len(taps) != len(wantTaps) {
		t.Fatalf("tap metadata = %v, want %v", taps, wantTaps)
	}
	for i := range wantTaps {
		if taps[i] != wantTaps[i] {
			t.Errorf("tap %d = %q, want %q", i, taps[i], wantTaps[i])
		}
	}
	// AllTokens must be unique
	tokens := c.AllTokens()
	seen := make(map[string]bool)
	for _, t2 := range tokens {
		if seen[t2] {
			t.Errorf("AllTokens duplicate: %q", t2)
		}
		seen[t2] = true
	}
	// DisplayName fallback
	if got := c.DisplayName("__not-a-real-token__"); got != "__not-a-real-token__" {
		t.Errorf("DisplayName fallback: got %q", got)
	}
	if got := c.AppBundle("orca"); got != "Orca.app" {
		t.Errorf("AppBundle(orca) = %q, want Orca.app", got)
	}
}

func TestNormalizeCaskTokens(t *testing.T) {
	got := NormalizeCaskTokens([]string{"raycast", "", "maru-workspace", "raycast"})
	want := []string{"raycast", "maru-workspace"}
	if len(got) != len(want) {
		t.Fatalf("NormalizeCaskTokens = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("token %d = %q, want %q", i, got[i], want[i])
		}
	}
}
