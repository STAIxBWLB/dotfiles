package cli

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

// The release archive must hold only members dot update accepts. goreleaser's
// default file list also packs CHANGELOG*, so an unpinned list broke updates
// to v2.85.0; this pins .goreleaser.yaml to allowedPrebuiltMember.
func TestReleaseArchiveFilesMatchUpdateAllowList(t *testing.T) {
	raw, err := os.ReadFile("../../.goreleaser.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Archives []struct {
			Files []string `yaml:"files"`
		} `yaml:"archives"`
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Archives) == 0 {
		t.Fatal(".goreleaser.yaml has no archives")
	}
	for _, a := range cfg.Archives {
		if len(a.Files) == 0 {
			t.Fatal("archives.files is unset, so goreleaser adds its defaults (CHANGELOG* among them); list LICENSE and README.md")
		}
		for _, f := range a.Files {
			if !allowedPrebuiltMember(f) {
				t.Errorf("archives.files lists %q, which dot update rejects; add it to allowedPrebuiltMember or drop it", f)
			}
		}
	}
}
