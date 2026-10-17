package aisettings

import (
	"os"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/claudecfg"
)

// BUG-16: the preview branch of applyClaudeHUD reported a fixed both-halves
// detail for any changed run, so a home whose settings were already
// dot-correct previewed a settings write the run's own hash-skip would
// decline to perform. The write path was narrowed by plan 05-02 Task 4; the
// preview was deliberately left byte-identical there, because that plan's
// acceptance pinned the dry-run report as unchanged.
//
// This table lives in its own file so hud_dryrun_test.go stays byte-
// unmodified: that test owns the dry-run WRITE guard, this one owns the
// dry-run REPORT.

// fileSnapshot is a path's content plus whether it was there at all, so a row
// can assert "still absent" as firmly as "still these bytes".
type fileSnapshot struct {
	body   string
	exists bool
}

func snapshotFile(t *testing.T, path string) fileSnapshot {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fileSnapshot{}
		}
		t.Fatalf("reading %s: %v", path, err)
	}
	return fileSnapshot{body: string(body), exists: true}
}

func TestApplyClaudeHUD_PreviewNamesOnlyTheHalvesItWouldWrite(t *testing.T) {
	for _, tc := range []struct {
		name    string
		seed    func(t *testing.T, m *HUDManager)
		changed bool
		drift   string
		detail  string
	}{
		{
			// BUG-16's exact reproduction: the settings half is already
			// dot-correct, so only the script would be written.
			name: "dot-correct settings, stale script",
			seed: func(t *testing.T, m *HUDManager) {
				seedDotCorrectSettings(t, m)
				writeHUDFile(t, m.claudeScriptPath(), "# stale statusline from an older dot\n")
			},
			changed: true,
			drift:   "out-of-sync",
			detail:  "write statusline-dot.py",
		},
		{
			// The mirror image, so the narrowing cannot be a hardcoded
			// preference for one half.
			name: "dot-correct script, stale settings",
			seed: func(t *testing.T, m *HUDManager) {
				writeHUDFile(t, claudecfg.SettingsPath(m.homeDir()), "{}\n")
				writeHUDFile(t, m.claudeScriptPath(), claudeHUDScript)
			},
			changed: true,
			drift:   "out-of-sync",
			detail:  "write statusLine",
		},
		{
			// The non-vacuity row: when both halves really are stale the
			// preview says exactly what it said before this change. A table
			// in which every row flipped would mean the fixture moved, not
			// the fix.
			name:    "fresh home, both halves stale",
			seed:    func(t *testing.T, m *HUDManager) {},
			changed: true,
			drift:   "out-of-sync",
			detail:  claudePreviewDetail,
		},
		{
			name: "in-sync home, nothing stale",
			seed: func(t *testing.T, m *HUDManager) {
				seedDotCorrectSettings(t, m)
				writeHUDFile(t, m.claudeScriptPath(), claudeHUDScript)
			},
			changed: false,
			drift:   "in-sync",
			detail:  "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newHUDManagerForTest(t)
			tc.seed(t, m)
			settingsPath := claudecfg.SettingsPath(m.homeDir())
			settingsBefore := snapshotFile(t, settingsPath)
			scriptBefore := snapshotFile(t, m.claudeScriptPath())

			item, err := m.applyClaudeHUD(true, false)
			if err != nil {
				t.Fatal(err)
			}

			want := HUDItem{
				ToolID:     "claude",
				TargetPath: settingsPath,
				Changed:    tc.changed,
				Drift:      tc.drift,
				Detail:     tc.detail,
			}
			if item != want {
				t.Errorf("item = %+v, want %+v", item, want)
			}

			// A row must not go green by having performed the write it
			// described. The dry-run guard test owns that assertion in
			// general; these two checks stop THIS table from passing on a
			// run that wrote.
			if got := snapshotFile(t, settingsPath); got != settingsBefore {
				t.Errorf("the preview wrote the settings half: %+v -> %+v", settingsBefore, got)
			}
			if got := snapshotFile(t, m.claudeScriptPath()); got != scriptBefore {
				t.Errorf("the preview wrote the script half: %+v -> %+v", scriptBefore, got)
			}
		})
	}
}
